package service

import (
	"context"
	"strings"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// productionProjectionResolver applies release-head visibility to one request's
// retrieval targets. It owns no mutable state and is safe to create per call.
type productionProjectionResolver struct {
	releases interfaces.ProductionReleaseRepository
}

func newProductionProjectionResolver(releases interfaces.ProductionReleaseRepository) *productionProjectionResolver {
	return &productionProjectionResolver{releases: releases}
}

func (r *productionProjectionResolver) Apply(ctx context.Context, tenantID uint64, targets []*types.SearchTarget) error {
	if r == nil || r.releases == nil || len(targets) == 0 {
		return nil
	}
	targetsByTenant := make(map[uint64][]*types.SearchTarget)
	for _, target := range targets {
		if target == nil || strings.TrimSpace(target.KnowledgeBaseID) == "" {
			continue
		}
		scopeTenantID := target.TenantID
		if scopeTenantID == 0 {
			scopeTenantID = tenantID
		}
		targetsByTenant[scopeTenantID] = append(targetsByTenant[scopeTenantID], target)
	}
	for scopeTenantID, tenantTargets := range targetsByTenant {
		kbIDs := make([]string, 0, len(tenantTargets))
		seenKBs := make(map[string]struct{}, len(tenantTargets))
		for _, target := range tenantTargets {
			if _, ok := seenKBs[target.KnowledgeBaseID]; !ok {
				seenKBs[target.KnowledgeBaseID] = struct{}{}
				kbIDs = append(kbIDs, target.KnowledgeBaseID)
			}
		}
		scopeCtx := ctx
		if scopeTenantID != tenantID {
			scopeCtx = context.WithValue(ctx, types.TenantIDContextKey, scopeTenantID)
		}
		scopes, err := r.releases.ResolveScopes(scopeCtx, scopeTenantID, kbIDs)
		if err != nil {
			return err
		}
		for _, target := range tenantTargets {
			scope := scopes[target.KnowledgeBaseID]
			if target.Type == types.SearchTargetTypeKnowledgeBase {
				target.ExcludeKnowledgeIDs = mergeUniqueKnowledgeIDs(target.ExcludeKnowledgeIDs, scope.InactiveKnowledgeIDs)
				continue
			}
			if target.Type == types.SearchTargetTypeKnowledge && intersectsKnowledgeIDs(target.KnowledgeIDs, scope.InactiveKnowledgeIDs) {
				return types.ErrProductionProjectionInactive
			}
		}
	}
	return nil
}

// AuthorizeExplicitKnowledgeIDs is retained as the explicit-ID guard used by
// callers that have already scoped their candidate production knowledge IDs.
func (r *productionProjectionResolver) AuthorizeExplicitKnowledgeIDs(ctx context.Context, tenantID uint64, knowledgeIDs []string) error {
	if r == nil || r.releases == nil || len(knowledgeIDs) == 0 {
		return nil
	}
	scopes, err := r.releases.ResolveScopesForKnowledgeIDs(ctx, tenantID, knowledgeIDs)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if intersectsKnowledgeIDs(knowledgeIDs, scope.InactiveKnowledgeIDs) {
			return types.ErrProductionProjectionInactive
		}
	}
	return nil
}

// ProjectVisibleKnowledge is the single user-facing projection boundary for
// direct and non-hybrid Knowledge reads. Inactive release history is omitted;
// active projections are returned as response copies so repository-owned rows
// are never mutated while being marked read-only.
func (r *productionProjectionResolver) ProjectVisibleKnowledge(
	ctx context.Context,
	rows []*types.Knowledge,
	rejectInactive bool,
) ([]*types.Knowledge, error) {
	if len(rows) == 0 || r == nil || r.releases == nil {
		return rows, nil
	}
	rowsByTenant := make(map[uint64][]*types.Knowledge)
	for _, row := range rows {
		if row != nil && row.ID != "" && row.TenantID != 0 {
			rowsByTenant[row.TenantID] = append(rowsByTenant[row.TenantID], row)
		}
	}
	active := make(map[string]struct{})
	inactive := make(map[string]struct{})
	for tenantID, tenantRows := range rowsByTenant {
		ids := make([]string, 0, len(tenantRows))
		for _, row := range tenantRows {
			ids = append(ids, row.ID)
		}
		scopeCtx := context.WithValue(ctx, types.TenantIDContextKey, tenantID)
		scopes, err := r.releases.ResolveScopesForKnowledgeIDs(scopeCtx, tenantID, ids)
		if err != nil {
			return nil, err
		}
		for _, scope := range scopes {
			for _, id := range scope.ActiveKnowledgeIDs {
				active[id] = struct{}{}
			}
			for _, id := range scope.InactiveKnowledgeIDs {
				inactive[id] = struct{}{}
			}
		}
	}

	visible := make([]*types.Knowledge, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		if _, hidden := inactive[row.ID]; hidden {
			if rejectInactive {
				return nil, apprepository.ErrKnowledgeNotFound
			}
			continue
		}
		if _, isActive := active[row.ID]; isActive {
			response := *row
			response.Source = "production"
			response.ReadOnly = true
			visible = append(visible, &response)
			continue
		}
		visible = append(visible, row)
	}
	return visible, nil
}

// RejectGovernedKnowledgeBaseMutation protects KB-wide operations that could
// copy or destroy release-managed projection history. The guard is intentionally
// repository-backed so handler and worker entry points share the same rule.
func (r *productionProjectionResolver) RejectGovernedKnowledgeBaseMutation(
	ctx context.Context,
	tenantID uint64,
	kbIDs ...string,
) error {
	if r == nil || r.releases == nil || len(kbIDs) == 0 {
		return nil
	}
	scopes, err := r.releases.ResolveScopes(ctx, tenantID, kbIDs)
	if err != nil {
		return err
	}
	for _, kbID := range kbIDs {
		if len(scopes[kbID].AllProductionKnowledgeIDs) > 0 {
			return types.ErrProductionProjectionImmutable
		}
	}
	return nil
}

func (r *productionProjectionResolver) RejectKnowledgeMutation(
	ctx context.Context,
	tenantID uint64,
	knowledgeIDs []string,
) error {
	if r == nil || r.releases == nil || len(knowledgeIDs) == 0 {
		return nil
	}
	scopes, err := r.releases.ResolveScopesForKnowledgeIDs(ctx, tenantID, knowledgeIDs)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if intersectsKnowledgeIDs(knowledgeIDs, scope.AllProductionKnowledgeIDs) {
			return types.ErrProductionProjectionImmutable
		}
	}
	return nil
}

func mergeUniqueKnowledgeIDs(existing, additions []string) []string {
	result := make([]string, 0, len(existing)+len(additions))
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, ids := range [][]string{existing, additions} {
		for _, id := range ids {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

func intersectsKnowledgeIDs(left, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(right))
	for _, id := range right {
		set[id] = struct{}{}
	}
	for _, id := range left {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
}
