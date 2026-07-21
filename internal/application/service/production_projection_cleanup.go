package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type ProductionProjectionIndexUpdater interface {
	DisableChunks(ctx context.Context, target *types.ProductionReleaseTarget, chunks []*types.Chunk) error
}

type productionCleanupEligibleRepository interface {
	ListCleanupEligible(ctx context.Context, tenantID uint64, limit int) ([]*types.ProductionReleaseTarget, error)
}

type ProductionProjectionCleanup struct {
	releases interfaces.ProductionReleaseRepository
	chunks   interfaces.ChunkService
	indexes  ProductionProjectionIndexUpdater
	uow      interfaces.ProductionUnitOfWork
	audit    interfaces.AuditLogService
	now      func() time.Time
}

func NewProductionProjectionCleanup(
	releases interfaces.ProductionReleaseRepository,
	chunks interfaces.ChunkService,
	indexes ProductionProjectionIndexUpdater,
	uow interfaces.ProductionUnitOfWork,
	audit interfaces.AuditLogService,
) *ProductionProjectionCleanup {
	return &ProductionProjectionCleanup{releases: releases, chunks: chunks, indexes: indexes, uow: uow, audit: audit, now: time.Now}
}

func (s *ProductionProjectionCleanup) Cleanup(ctx context.Context, targetID string) error {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return types.ErrProductionForbidden
	}
	if s == nil || s.releases == nil || s.chunks == nil || s.indexes == nil || s.uow == nil || s.audit == nil {
		return errors.New("production cleanup dependencies are unavailable")
	}
	target, err := s.releases.GetTarget(ctx, tenantID, targetID)
	if err != nil {
		return err
	}
	if target == nil || target.TenantID != tenantID || target.ID != targetID {
		return types.ErrProductionForbidden
	}
	if target.Status == types.ReleaseTargetCleaned {
		return nil
	}
	if target.Status == types.ReleaseTargetActive || target.Status == types.ReleaseTargetReady || target.Status == types.ReleaseTargetBuilding {
		return types.ErrProductionReleaseLifecycle
	}
	if target.Status != types.ReleaseTargetCleanupPending {
		if target.Status != types.ReleaseTargetRolledBack && target.Status != types.ReleaseTargetFailed {
			return types.ErrProductionReleaseLifecycle
		}
		if target.RetentionUntil == nil || target.RetentionUntil.After(s.clockNow()) {
			return types.ErrProductionReleaseLifecycle
		}
		from := target.Status
		actorID, _ := types.UserIDFromContext(ctx)
		err := s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
			changed, transitionErr := s.releases.TransitionTarget(txCtx, target.ID, from, types.ReleaseTargetCleanupPending, nil)
			if transitionErr != nil {
				return transitionErr
			}
			if !changed {
				current, loadErr := s.releases.GetTarget(txCtx, tenantID, target.ID)
				if loadErr == nil && current != nil && current.Status == types.ReleaseTargetCleanupPending {
					return nil
				}
				if loadErr != nil {
					return loadErr
				}
				return types.ErrProductionProjectionConflict
			}
			details, _ := json.Marshal(map[string]any{"knowledge_id": target.KnowledgeID, "from_status": from})
			return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
				TenantID: target.TenantID, ActorUserID: actorID, ActorRole: "system",
				Action: types.AuditActionProductionProjectionCleanupStarted, TargetType: "production_release_target", TargetID: target.ID,
				Outcome: types.AuditOutcomeSuccess, Details: details,
			})
		})
		if err != nil {
			return err
		}
		target.Status = types.ReleaseTargetCleanupPending
	}

	chunks, err := s.chunks.ListChunksByKnowledgeID(ctx, target.KnowledgeID)
	if err != nil {
		return err
	}
	if err := s.indexes.DisableChunks(ctx, target, chunks); err != nil {
		return fmt.Errorf("disable production projection indexes: %w", err)
	}
	changedChunks := make([]*types.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk != nil && chunk.IsEnabled {
			copy := *chunk
			copy.IsEnabled = false
			changedChunks = append(changedChunks, &copy)
		}
	}
	if err := s.chunks.UpdateChunks(ctx, changedChunks); err != nil {
		return fmt.Errorf("disable production projection chunks: %w", err)
	}
	actorID, _ := types.UserIDFromContext(ctx)
	details, _ := json.Marshal(map[string]any{"knowledge_id": target.KnowledgeID, "disabled_chunks": len(chunks)})
	return s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		changed, err := s.releases.TransitionTarget(txCtx, target.ID, types.ReleaseTargetCleanupPending, types.ReleaseTargetCleaned, nil)
		if err != nil {
			return err
		}
		if !changed {
			current, loadErr := s.releases.GetTarget(txCtx, tenantID, target.ID)
			if loadErr == nil && current != nil && current.Status == types.ReleaseTargetCleaned {
				return nil
			}
			if loadErr != nil {
				return loadErr
			}
			return types.ErrProductionProjectionConflict
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: target.TenantID, ActorUserID: actorID, ActorRole: "system",
			Action: types.AuditActionProductionProjectionCleaned, TargetType: "production_release_target", TargetID: target.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
}

// CleanupExpired is the bounded retention sweep used by maintenance callers.
// The repository query excludes every target referenced by an active head;
// Cleanup repeats the status guard so stale query results remain harmless.
func (s *ProductionProjectionCleanup) CleanupExpired(ctx context.Context, limit int) error {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return types.ErrProductionForbidden
	}
	repo, ok := s.releases.(productionCleanupEligibleRepository)
	if !ok {
		return errors.New("production cleanup eligibility lookup is unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	targets, err := repo.ListCleanupEligible(ctx, tenantID, limit)
	if err != nil {
		return err
	}
	var result error
	for _, target := range targets {
		if target == nil {
			continue
		}
		if err := s.Cleanup(ctx, target.ID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *ProductionProjectionCleanup) clockNow() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

type ProductionProjectionRetrieveIndexUpdater struct {
	kbs       interfaces.KnowledgeBaseService
	registry  interfaces.RetrieveEngineRegistry
	ownership retriever.TenantStoreOwnership
}

func NewProductionProjectionRetrieveIndexUpdater(
	kbs interfaces.KnowledgeBaseService,
	registry interfaces.RetrieveEngineRegistry,
	ownership retriever.TenantStoreOwnership,
) ProductionProjectionIndexUpdater {
	return &ProductionProjectionRetrieveIndexUpdater{kbs: kbs, registry: registry, ownership: ownership}
}

func (u *ProductionProjectionRetrieveIndexUpdater) DisableChunks(ctx context.Context, target *types.ProductionReleaseTarget, chunks []*types.Chunk) error {
	if u == nil || u.kbs == nil || u.registry == nil || target == nil {
		return errors.New("production index updater dependencies are unavailable")
	}
	kb, err := u.kbs.GetKnowledgeBaseByIDOnly(ctx, target.TargetKnowledgeBaseID)
	if err != nil {
		return err
	}
	if kb == nil || kb.ID != target.TargetKnowledgeBaseID || kb.TenantID != target.TenantID {
		return types.ErrProductionForbidden
	}
	vectorStoreID, retrieverEngines, externalIndexes, err := authenticatedProductionProjectionRetrievalSnapshot(target)
	if err != nil {
		return err
	}
	if !externalIndexes {
		return nil
	}
	status := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		if chunk != nil {
			status[chunk.ID] = false
		}
	}
	if len(status) == 0 {
		return nil
	}
	if vectorStoreID != "" {
		if u.ownership == nil {
			return errors.New("production vector store ownership is unavailable")
		}
		owned, err := u.ownership.StoreOwnedBy(ctx, vectorStoreID, target.TenantID)
		if err != nil {
			return err
		}
		if !owned {
			return types.ErrProductionForbidden
		}
		engine, err := u.registry.GetByStoreID(vectorStoreID)
		if err != nil {
			return err
		}
		return engine.BatchUpdateChunkEnabledStatus(ctx, status)
	}
	engine, err := retriever.NewCompositeRetrieveEngine(u.registry, retrieverEngines)
	if err != nil {
		return err
	}
	return engine.BatchUpdateChunkEnabledStatus(ctx, status)
}

func authenticatedProductionProjectionRetrievalSnapshot(target *types.ProductionReleaseTarget) (string, []types.RetrieverEngineParams, bool, error) {
	if target == nil {
		return "", nil, false, fmt.Errorf("%w: retained target is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(target.ConfigSnapshot)
	if err != nil || digest != target.ConfigDigest || string(canonical) != string(target.ConfigSnapshot) {
		return "", nil, false, fmt.Errorf("%w: retained configuration authentication failed", types.ErrProductionReleaseConfigInvalid)
	}
	var snapshot struct {
		IndexingStrategy *types.IndexingStrategy       `json:"indexing_strategy"`
		VectorStoreID    *string                       `json:"vector_store_id"`
		RetrieverEngines []types.RetrieverEngineParams `json:"retriever_engines"`
	}
	if err := json.Unmarshal(canonical, &snapshot); err != nil {
		return "", nil, false, fmt.Errorf("%w: retained retrieval configuration cannot be decoded", types.ErrProductionReleaseConfigInvalid)
	}
	if snapshot.IndexingStrategy == nil || !snapshot.IndexingStrategy.HasAnyIndexing() {
		return "", nil, false, fmt.Errorf("%w: retained indexing strategy is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	if !snapshot.IndexingStrategy.NeedsEmbedding() {
		return "", nil, false, nil
	}
	vectorStoreID := strings.TrimSpace(valueOrEmpty(snapshot.VectorStoreID))
	hasVectorStore := vectorStoreID != ""
	hasRetrieverEngines := len(snapshot.RetrieverEngines) != 0
	if hasVectorStore && hasRetrieverEngines {
		return "", nil, false, fmt.Errorf("%w: retained retrieval configuration is ambiguous", types.ErrProductionReleaseConfigInvalid)
	}
	if !hasVectorStore && !hasRetrieverEngines {
		return "", nil, false, fmt.Errorf("%w: retained retrieval configuration is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	return vectorStoreID, snapshot.RetrieverEngines, true, nil
}
