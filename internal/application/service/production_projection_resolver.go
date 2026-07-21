package service

import (
	"context"
	"strings"

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
