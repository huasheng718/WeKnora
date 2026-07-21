package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const productionProjectionTaskRetention = 24 * time.Hour

type productionProjectionBuilder interface {
	Build(ctx context.Context, targetID string) (*types.Knowledge, error)
}

type productionApprovedReviewRepository interface {
	GetApprovedReviewForVersion(ctx context.Context, tenantID uint64, documentID, versionID string) (*types.ProductionReviewRequest, error)
}

type ProductionGraphReadiness interface {
	RequireSuccessfulGraphSubtasks(ctx context.Context, target *types.ProductionReleaseTarget, knowledge *types.Knowledge) error
}

type ProductionWikiLifecycleEnqueuer interface {
	EnqueueIngest(ctx context.Context, target *types.ProductionReleaseTarget) error
	EnqueueRetract(ctx context.Context, target *types.ProductionReleaseTarget) error
}

type ProductionProjectionCleanupScheduler interface {
	EnqueueCleanup(ctx context.Context, target *types.ProductionReleaseTarget) error
}

type ProductionReleaseService struct {
	releases   interfaces.ProductionReleaseRepository
	documents  interfaces.ProductionDocumentRepository
	reviews    interfaces.ProductionReviewRepository
	authorizer interfaces.ProductionProjectAuthorizer
	members    interfaces.TenantMemberService
	kbs        interfaces.KnowledgeBaseService
	storage    interfaces.StorageBackendResolver
	models     interfaces.ModelService
	registry   interfaces.RetrieveEngineRegistry
	ownership  retriever.TenantStoreOwnership
	builder    productionProjectionBuilder
	knowledge  interfaces.KnowledgeService
	graph      ProductionGraphReadiness
	wiki       ProductionWikiLifecycleEnqueuer
	cleanup    ProductionProjectionCleanupScheduler
	tasks      interfaces.TaskEnqueuer
	uow        interfaces.ProductionUnitOfWork
	audit      interfaces.AuditLogService
	now        func() time.Time
}

func NewProductionReleaseService(
	releases interfaces.ProductionReleaseRepository,
	documents interfaces.ProductionDocumentRepository,
	reviews interfaces.ProductionReviewRepository,
	authorizer interfaces.ProductionProjectAuthorizer,
	members interfaces.TenantMemberService,
	kbs interfaces.KnowledgeBaseService,
	storage interfaces.StorageBackendResolver,
	models interfaces.ModelService,
	registry interfaces.RetrieveEngineRegistry,
	ownership retriever.TenantStoreOwnership,
	builder *ProductionProjectionBuilder,
	knowledge interfaces.KnowledgeService,
	graph ProductionGraphReadiness,
	wiki ProductionWikiLifecycleEnqueuer,
	cleanup ProductionProjectionCleanupScheduler,
	tasks interfaces.TaskEnqueuer,
	uow interfaces.ProductionUnitOfWork,
	audit interfaces.AuditLogService,
) *ProductionReleaseService {
	return &ProductionReleaseService{
		releases: releases, documents: documents, reviews: reviews, authorizer: authorizer,
		members: members, kbs: kbs, storage: storage, models: models, registry: registry, ownership: ownership,
		builder: builder, knowledge: knowledge, graph: graph, wiki: wiki, cleanup: cleanup,
		tasks: tasks, uow: uow, audit: audit, now: time.Now,
	}
}

func (s *ProductionReleaseService) Prepare(ctx context.Context, documentID, versionID string, kbIDs []string) (*types.ProductionRelease, error) {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil || s == nil || s.releases == nil || s.documents == nil || s.reviews == nil ||
		s.authorizer == nil || s.members == nil || s.kbs == nil || s.storage == nil || s.models == nil || s.uow == nil || s.audit == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("production release dependencies are unavailable")
	}
	if strings.TrimSpace(documentID) == "" || strings.TrimSpace(versionID) == "" || len(kbIDs) == 0 {
		return nil, types.ErrProductionReleaseInvalid
	}
	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	version, err := s.documents.GetVersion(ctx, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	if document == nil || version == nil || document.ID != documentID || version.ID != versionID ||
		document.TenantID != tenantID || version.TenantID != tenantID || document.ProjectID != version.ProjectID ||
		version.DocumentID != document.ID || version.FrozenAt == nil || document.LatestApprovedVersionID == nil ||
		*document.LatestApprovedVersionID != version.ID {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	if err := s.authorizer.RequireProjectRole(ctx, document.ProjectID, types.ProductionRolePublisher); err != nil {
		return nil, err
	}
	reviewReader, ok := s.reviews.(productionApprovedReviewRepository)
	if !ok {
		return nil, errors.New("approved production review lookup is unavailable")
	}
	review, err := reviewReader.GetApprovedReviewForVersion(ctx, tenantID, documentID, versionID)
	if err != nil {
		return nil, err
	}
	if review == nil || review.Status != types.ProductionReviewApproved || review.TenantID != tenantID ||
		review.ProjectID != document.ProjectID || review.DocumentID != documentID || review.VersionID != versionID {
		return nil, types.ErrProductionReviewScopeInvalid
	}

	release := &types.ProductionRelease{
		ID: uuid.NewString(), TenantID: tenantID, ProjectID: document.ProjectID,
		DocumentID: documentID, VersionID: versionID, ReviewRequestID: review.ID,
		ReleaseDigestVersion: types.ProductionReleaseDigestVersionCurrent,
		Status:               types.ProductionReleaseBuilding, RetentionDays: types.ProductionReleaseDefaultRetentionDays,
		CreatedBy: actorID,
	}
	release.ReleaseDigest = types.ComputeProductionReleaseDigest(release, version, review)
	targets := make([]*types.ProductionReleaseTarget, 0, len(kbIDs))
	seen := make(map[string]struct{}, len(kbIDs))
	for _, kbID := range kbIDs {
		if _, duplicate := seen[kbID]; duplicate || strings.TrimSpace(kbID) == "" {
			return nil, types.ErrProductionConflict
		}
		seen[kbID] = struct{}{}
		kb, err := s.requireWritableTargetKB(ctx, tenantID, actorID, kbID)
		if err != nil {
			return nil, err
		}
		snapshot, err := s.snapshotTargetProcessingConfig(ctx, tenantID, kb)
		if err != nil {
			return nil, err
		}
		canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(snapshot)
		if err != nil {
			return nil, err
		}
		targets = append(targets, &types.ProductionReleaseTarget{
			ID: uuid.NewString(), ReleaseID: release.ID, TenantID: tenantID, ProjectID: release.ProjectID,
			DocumentID: documentID, VersionID: versionID, TargetKnowledgeBaseID: kb.ID,
			KnowledgeID: uuid.NewString(), ReleaseDigest: release.ReleaseDigest,
			ConfigSnapshot: canonical, ConfigDigest: digest, Status: types.ReleaseTargetBuilding,
			RetentionDays: release.RetentionDays,
		})
	}
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.releases.CreateRelease(txCtx, release, targets); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"document_id": documentID, "version_id": versionID, "target_count": len(targets)})
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: actorID, ActorRole: string(types.ProductionRolePublisher),
			Action: types.AuditActionProductionReleaseConfirmed, TargetType: "production_release", TargetID: release.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
	if err != nil {
		return nil, err
	}
	release.Targets = targets
	return release, nil
}

func (s *ProductionReleaseService) Retry(ctx context.Context, targetID string) error {
	target, err := s.authorizeTarget(ctx, targetID)
	if err != nil {
		return err
	}
	if target.Status != types.ReleaseTargetFailed && target.Status != types.ReleaseTargetRolledBack && target.Status != types.ReleaseTargetBuilding {
		return types.ErrProductionReleaseLifecycle
	}
	if s.tasks == nil {
		return errors.New("production projection task queue is unavailable")
	}
	return s.enqueueProjectionTask(ctx, types.TypeProductionBuild, types.ProductionProjectionOperationBuild, target, 0)
}

func (s *ProductionReleaseService) Activate(ctx context.Context, targetID string, expectedLock int) error {
	target, err := s.authorizeTarget(ctx, targetID)
	if err != nil {
		return err
	}
	if err := s.enqueueProjectionTask(ctx, types.TypeProductionActivate, types.ProductionProjectionOperationActivate, target, expectedLock); err != nil {
		return err
	}
	return s.activateAuthorized(ctx, target, expectedLock)
}

func (s *ProductionReleaseService) Rollback(ctx context.Context, targetID string, expectedLock int) error {
	target, err := s.authorizeTarget(ctx, targetID)
	if err != nil {
		return err
	}
	if target.Status != types.ReleaseTargetActive && (target.Status != types.ReleaseTargetRolledBack ||
		target.RetentionUntil == nil || !target.RetentionUntil.After(s.clockNow())) {
		return types.ErrProductionReleaseLifecycle
	}
	if err := s.enqueueProjectionTask(ctx, types.TypeProductionActivate, types.ProductionProjectionOperationRollback, target, expectedLock); err != nil {
		return err
	}
	return s.rollbackAuthorized(ctx, target, expectedLock)
}

func (s *ProductionReleaseService) enqueueProjectionTask(
	ctx context.Context,
	taskType string,
	operation types.ProductionProjectionOperation,
	target *types.ProductionReleaseTarget,
	expectedLock int,
) error {
	// Unit tests and in-process callers can exercise the state machine without
	// a queue. Runtime construction always supplies the enqueuer.
	if s.tasks == nil {
		return nil
	}
	actorID, _ := types.UserIDFromContext(ctx)
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{Operation: operation, TenantID: target.TenantID,
		ProjectID: target.ProjectID, TargetID: target.ID, ActorUserID: actorID, ExpectedLock: expectedLock})
	if err != nil {
		return err
	}
	phase := string(operation)
	if operation == types.ProductionProjectionOperationBuild {
		phase, err = productionProjectionBuildTaskPhase(target)
		if err != nil {
			return err
		}
	} else if operation == types.ProductionProjectionOperationActivate || operation == types.ProductionProjectionOperationRollback {
		phase = fmt.Sprintf("%s-lock-%d", operation, expectedLock)
	}
	taskOptions := []asynq.Option{asynq.Queue(types.QueueProduction), asynq.MaxRetry(8)}
	if taskType == types.TypeProductionActivate {
		// Leave the synchronous caller a small uncontended window. If it
		// crashes after committing the head, this delayed replay reconciles
		// the derived Wiki and cleanup work from durable target state.
		taskOptions = append(taskOptions, asynq.ProcessIn(time.Second))
	}
	task := asynq.NewTask(taskType, payload, taskOptions...)
	_, err = s.tasks.Enqueue(task,
		asynq.TaskID(productionProjectionTaskID(target.ID, target.KnowledgeID, phase)),
		asynq.Retention(productionProjectionTaskRetention),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

func productionProjectionBuildTaskPhase(target *types.ProductionReleaseTarget) (string, error) {
	if target == nil {
		return "", types.ErrProductionReleaseInvalid
	}
	switch target.Status {
	case types.ReleaseTargetBuilding:
		return "build-initial", nil
	case types.ReleaseTargetFailed:
		if target.FailedAt == nil || target.FailedAt.IsZero() {
			return "", fmt.Errorf("%w: failed target generation is unavailable", types.ErrProductionReleaseLifecycle)
		}
		return fmt.Sprintf("build-failed-%d", target.FailedAt.UTC().UnixNano()), nil
	case types.ReleaseTargetRolledBack:
		if target.RolledBackAt == nil || target.RolledBackAt.IsZero() {
			return "", fmt.Errorf("%w: rolled-back target generation is unavailable", types.ErrProductionReleaseLifecycle)
		}
		return fmt.Sprintf("build-rolled-back-%d", target.RolledBackAt.UTC().UnixNano()), nil
	default:
		return "", types.ErrProductionReleaseLifecycle
	}
}

func (s *ProductionReleaseService) activateAuthorized(ctx context.Context, target *types.ProductionReleaseTarget, expectedLock int) error {
	if target == nil || expectedLock < 0 {
		return types.ErrProductionProjectionConflict
	}
	if target.Status == types.ReleaseTargetActive {
		return s.enqueueActivationFollowups(ctx, target)
	}
	if target.Status != types.ReleaseTargetReady {
		return types.ErrProductionReleaseLifecycle
	}
	if _, err := s.requireProjectionReady(ctx, target); err != nil {
		return err
	}
	history, err := s.releases.ListProjectionHistory(ctx, target.TenantID, target.DocumentID, target.TargetKnowledgeBaseID)
	if err != nil {
		return err
	}
	previous := activeProjectionTarget(history, target.ID)
	actorID, _ := types.UserIDFromContext(ctx)
	if s.uow == nil || s.audit == nil {
		return errors.New("production activation transaction dependencies are unavailable")
	}
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		head, switchErr := s.releases.SwitchHead(txCtx, target.TenantID, target.DocumentID,
			target.TargetKnowledgeBaseID, target.ID, expectedLock)
		if switchErr != nil {
			return switchErr
		}
		details, _ := json.Marshal(map[string]any{"lock_version": head.LockVersion, "previous_target_id": targetID(previous)})
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: target.TenantID, ActorUserID: actorID, ActorRole: string(types.ProductionRolePublisher),
			Action: types.AuditActionProductionProjectionActivated, TargetType: "production_release_target", TargetID: target.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
	if err != nil {
		if errors.Is(err, types.ErrProductionProjectionConflict) {
			current, loadErr := s.releases.GetTarget(ctx, target.TenantID, target.ID)
			if loadErr == nil && current != nil && current.Status == types.ReleaseTargetActive {
				return s.enqueueActivationFollowups(ctx, current)
			}
			if loadErr != nil {
				return loadErr
			}
		}
		return err
	}
	target.Status = types.ReleaseTargetActive
	return s.enqueueActivationFollowupsWithPrevious(ctx, target, previous)
}

func (s *ProductionReleaseService) rollbackAuthorized(ctx context.Context, target *types.ProductionReleaseTarget, expectedLock int) error {
	if target == nil || expectedLock < 0 {
		return types.ErrProductionProjectionConflict
	}
	if target.Status == types.ReleaseTargetActive {
		return s.enqueueActivationFollowups(ctx, target)
	}
	if target.Status != types.ReleaseTargetRolledBack || target.RetentionUntil == nil || !target.RetentionUntil.After(s.clockNow()) {
		return types.ErrProductionReleaseLifecycle
	}
	if _, err := s.requireProjectionReady(ctx, target); err != nil {
		return err
	}
	history, err := s.releases.ListProjectionHistory(ctx, target.TenantID, target.DocumentID, target.TargetKnowledgeBaseID)
	if err != nil {
		return err
	}
	previous := activeProjectionTarget(history, target.ID)
	actorID, _ := types.UserIDFromContext(ctx)
	if s.uow == nil || s.audit == nil {
		return errors.New("production rollback transaction dependencies are unavailable")
	}
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		changed, transitionErr := s.releases.TransitionTarget(
			txCtx, target.ID, types.ReleaseTargetRolledBack, types.ReleaseTargetBuilding, nil,
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return types.ErrProductionProjectionConflict
		}
		changed, transitionErr = s.releases.TransitionTarget(
			txCtx, target.ID, types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil,
		)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return types.ErrProductionProjectionConflict
		}
		head, switchErr := s.releases.SwitchHead(
			txCtx, target.TenantID, target.DocumentID, target.TargetKnowledgeBaseID, target.ID, expectedLock,
		)
		if switchErr != nil {
			return switchErr
		}
		details, _ := json.Marshal(map[string]any{"lock_version": head.LockVersion, "previous_target_id": targetID(previous)})
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: target.TenantID, ActorUserID: actorID, ActorRole: string(types.ProductionRolePublisher),
			Action: types.AuditActionProductionProjectionRolledBack, TargetType: "production_release_target", TargetID: target.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
	if err != nil {
		if errors.Is(err, types.ErrProductionProjectionConflict) {
			current, loadErr := s.releases.GetTarget(ctx, target.TenantID, target.ID)
			if loadErr == nil && current != nil && current.Status == types.ReleaseTargetActive {
				return s.enqueueActivationFollowups(ctx, current)
			}
			if loadErr != nil {
				return loadErr
			}
		}
		return err
	}
	target.Status = types.ReleaseTargetActive
	target.RetentionUntil = nil
	return s.enqueueActivationFollowupsWithPrevious(ctx, target, previous)
}

func (s *ProductionReleaseService) requireProjectionReady(ctx context.Context, target *types.ProductionReleaseTarget) (*types.Knowledge, error) {
	if s.knowledge == nil {
		return nil, errors.New("production knowledge service is unavailable")
	}
	knowledge, err := s.knowledge.GetKnowledgeByID(ctx, target.KnowledgeID)
	if err != nil {
		return nil, err
	}
	if knowledge == nil || knowledge.ID != target.KnowledgeID || knowledge.TenantID != target.TenantID ||
		knowledge.KnowledgeBaseID != target.TargetKnowledgeBaseID || knowledge.ParseStatus != types.ParseStatusCompleted ||
		knowledge.PendingSubtasksCount != 0 {
		return nil, types.ErrProductionReleaseLifecycle
	}
	if s.graph == nil {
		return nil, errors.New("production graph readiness checker is unavailable")
	}
	if err := s.graph.RequireSuccessfulGraphSubtasks(ctx, target, knowledge); err != nil {
		return nil, err
	}
	return knowledge, nil
}

func (s *ProductionReleaseService) enqueueActivationFollowups(ctx context.Context, target *types.ProductionReleaseTarget) error {
	history, err := s.releases.ListProjectionHistory(ctx, target.TenantID, target.DocumentID, target.TargetKnowledgeBaseID)
	if err != nil {
		return err
	}
	return s.enqueueActivationFollowupsWithPrevious(ctx, target, latestRetainedTarget(history, target.ID))
}

func (s *ProductionReleaseService) enqueueActivationFollowupsWithPrevious(ctx context.Context, target, previous *types.ProductionReleaseTarget) error {
	if s.wiki == nil || s.cleanup == nil {
		return errors.New("production activation follow-up dependencies are unavailable")
	}
	var result error
	if err := s.wiki.EnqueueIngest(ctx, target); err != nil {
		result = errors.Join(result, fmt.Errorf("enqueue production wiki ingest: %w", err))
	}
	if previous != nil {
		if current, err := s.releases.GetTarget(ctx, previous.TenantID, previous.ID); err == nil && current != nil {
			previous = current
		}
		if err := s.wiki.EnqueueRetract(ctx, previous); err != nil {
			result = errors.Join(result, fmt.Errorf("enqueue production wiki retract: %w", err))
		}
		if err := s.cleanup.EnqueueCleanup(ctx, previous); err != nil {
			result = errors.Join(result, fmt.Errorf("enqueue production cleanup: %w", err))
		}
	}
	return result
}

func (s *ProductionReleaseService) authorizeTarget(ctx context.Context, targetID string) (*types.ProductionReleaseTarget, error) {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil || s == nil || s.releases == nil || s.authorizer == nil || s.members == nil || s.kbs == nil {
		if err != nil {
			return nil, err
		}
		return nil, types.ErrProductionForbidden
	}
	target, err := s.releases.GetTarget(ctx, tenantID, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.TenantID != tenantID || target.ID != targetID {
		return nil, types.ErrProductionForbidden
	}
	if err := s.authorizer.RequireProjectRole(ctx, target.ProjectID, types.ProductionRolePublisher); err != nil {
		return nil, err
	}
	if _, err := s.requireWritableTargetKB(ctx, tenantID, actorID, target.TargetKnowledgeBaseID); err != nil {
		return nil, err
	}
	return target, nil
}

func (s *ProductionReleaseService) requireWritableTargetKB(ctx context.Context, tenantID uint64, actorID, kbID string) (*types.KnowledgeBase, error) {
	kb, err := s.kbs.GetKnowledgeBaseByIDOnly(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if kb == nil || kb.ID != kbID || kb.TenantID != tenantID || kb.Type != types.KnowledgeBaseTypeDocument || kb.IsTemporary {
		return nil, types.ErrProductionForbidden
	}
	membership, err := s.members.GetMembership(ctx, actorID, tenantID)
	if err != nil {
		return nil, err
	}
	if membership == nil || membership.Status != types.TenantMemberStatusActive ||
		!membership.Role.HasPermission(types.TenantRoleContributor) {
		return nil, types.ErrProductionForbidden
	}
	if membership.Role == types.TenantRoleContributor && kb.CreatorID != actorID {
		return nil, types.ErrProductionForbidden
	}
	return kb, nil
}

func (s *ProductionReleaseService) snapshotTargetProcessingConfig(ctx context.Context, tenantID uint64, kb *types.KnowledgeBase) (types.JSON, error) {
	kb.EnsureDefaults()
	tenant, ok := types.TenantInfoFromContext(ctx)
	if !ok || tenant == nil || tenant.ID != tenantID || s.storage == nil {
		return nil, fmt.Errorf("%w: target storage context is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	requestedBackendID := strings.TrimSpace(valueOrEmpty(kb.StorageBackendID))
	requestedProvider := strings.ToLower(strings.TrimSpace(kb.GetStorageProvider()))
	backend, err := s.storage.ResolveBackend(ctx, tenant, requestedBackendID, requestedProvider)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve target storage: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	if backend == nil || strings.TrimSpace(backend.ID) == "" || backend.TenantID != tenantID ||
		backend.Status != types.StorageBackendStatusActive || strings.TrimSpace(backend.Provider) == "" ||
		(requestedBackendID != "" && backend.ID != requestedBackendID) ||
		(requestedBackendID == "" && requestedProvider != "" && !strings.EqualFold(backend.Provider, requestedProvider)) {
		return nil, fmt.Errorf("%w: target storage backend is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	resolvedBackendID := strings.TrimSpace(backend.ID)
	resolvedProvider := strings.ToLower(strings.TrimSpace(backend.Provider))
	fileService, fileProvider, err := s.storage.ResolveFileService(
		ctx, tenant, resolvedBackendID, resolvedProvider, strings.TrimSpace(os.Getenv("LOCAL_STORAGE_BASE_DIR")),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve target file service: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	if fileService == nil || !strings.EqualFold(strings.TrimSpace(fileProvider), resolvedProvider) {
		return nil, fmt.Errorf("%w: target file service identity is unavailable", types.ErrProductionReleaseConfigInvalid)
	}
	if err := fileService.CheckConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("%w: target storage connectivity failed: %v", types.ErrProductionReleaseConfigInvalid, err)
	}
	if !kb.IndexingStrategy.HasAnyIndexing() || kb.ChunkingConfig.ChunkSize <= 0 || strings.TrimSpace(kb.ChunkingConfig.Strategy) == "" ||
		strings.TrimSpace(kb.SummaryModelID) == "" || (kb.IndexingStrategy.NeedsEmbedding() && strings.TrimSpace(kb.EmbeddingModelID) == "") ||
		(kb.IndexingStrategy.GraphEnabled && (kb.ExtractConfig == nil || !kb.ExtractConfig.Enabled)) {
		return nil, fmt.Errorf("%w: target processing configuration is incomplete", types.ErrProductionReleaseConfigInvalid)
	}
	for _, requirement := range []struct {
		id        string
		embedding bool
	}{{id: kb.SummaryModelID}, {id: kb.EmbeddingModelID, embedding: true}} {
		modelID := requirement.id
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		model, err := s.models.GetModelByID(ctx, modelID)
		if err != nil {
			return nil, err
		}
		wrongType := requirement.embedding && model != nil && model.Type != types.ModelTypeEmbedding
		if !requirement.embedding && model != nil && (model.Type == types.ModelTypeEmbedding || model.Type == types.ModelTypeRerank || model.Type == types.ModelTypeASR) {
			wrongType = true
		}
		if model == nil || model.Status != types.ModelStatusActive || wrongType || (!model.IsBuiltin && model.TenantID != tenantID) {
			return nil, fmt.Errorf("%w: target model is unavailable", types.ErrProductionReleaseConfigInvalid)
		}
	}
	if kb.VectorStoreID != nil && strings.TrimSpace(*kb.VectorStoreID) != "" {
		if s.registry == nil || s.ownership == nil {
			return nil, fmt.Errorf("%w: target vector store readiness is unavailable", types.ErrProductionReleaseConfigInvalid)
		}
		if err := retriever.VerifyBinding(ctx, s.registry, s.ownership, tenantID, strings.TrimSpace(*kb.VectorStoreID)); err != nil {
			return nil, fmt.Errorf("%w: target vector store is unavailable: %v", types.ErrProductionReleaseConfigInvalid, err)
		}
	}
	var retrieverEngines []types.RetrieverEngineParams
	if strings.TrimSpace(valueOrEmpty(kb.VectorStoreID)) == "" {
		retrieverEngines = append(retrieverEngines, tenant.GetEffectiveEngines()...)
		if kb.IndexingStrategy.NeedsEmbedding() && len(retrieverEngines) == 0 {
			return nil, fmt.Errorf("%w: target retrieval engines are unavailable", types.ErrProductionReleaseConfigInvalid)
		}
		if s.registry != nil && len(retrieverEngines) != 0 {
			if _, err := retriever.NewCompositeRetrieveEngine(s.registry, retrieverEngines); err != nil {
				return nil, fmt.Errorf("%w: target retrieval engines are unavailable: %v", types.ErrProductionReleaseConfigInvalid, err)
			}
		}
	}
	graphModelID := ""
	if kb.IndexingStrategy.GraphEnabled {
		graphModelID = kb.SummaryModelID
	}
	snapshot := struct {
		Version                  int                             `json:"version"`
		IndexingStrategy         types.IndexingStrategy          `json:"indexing_strategy"`
		Chunking                 types.ChunkingConfig            `json:"chunking"`
		ParserEngineRules        []types.ParserEngineRule        `json:"parser_engine_rules,omitempty"`
		EmbeddingModelID         string                          `json:"embedding_model_id"`
		SummaryModelID           string                          `json:"summary_model_id"`
		QuestionGenerationConfig *types.QuestionGenerationConfig `json:"question_generation_config,omitempty"`
		StorageBackendID         *string                         `json:"storage_backend_id,omitempty"`
		StorageProvider          string                          `json:"storage_provider"`
		VectorStoreID            *string                         `json:"vector_store_id,omitempty"`
		RetrieverEngines         []types.RetrieverEngineParams   `json:"retriever_engines,omitempty"`
		Graph                    struct {
			Enabled       bool                 `json:"enabled"`
			ModelID       string               `json:"model_id"`
			ExtractConfig *types.ExtractConfig `json:"extract_config,omitempty"`
		} `json:"graph"`
	}{
		Version: 1, IndexingStrategy: kb.IndexingStrategy, Chunking: kb.ChunkingConfig,
		ParserEngineRules: append([]types.ParserEngineRule(nil), kb.ChunkingConfig.ParserEngineRules...),
		EmbeddingModelID:  kb.EmbeddingModelID, SummaryModelID: kb.SummaryModelID,
		QuestionGenerationConfig: kb.QuestionGenerationConfig,
		StorageBackendID:         &resolvedBackendID, StorageProvider: resolvedProvider, VectorStoreID: kb.VectorStoreID,
		RetrieverEngines: retrieverEngines,
	}
	snapshot.Graph.Enabled = kb.IndexingStrategy.GraphEnabled
	snapshot.Graph.ModelID = graphModelID
	snapshot.Graph.ExtractConfig = kb.ExtractConfig
	encoded, err := json.Marshal(snapshot)
	return types.JSON(encoded), err
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *ProductionReleaseService) clockNow() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func activeProjectionTarget(history []*types.ProductionReleaseTarget, except string) *types.ProductionReleaseTarget {
	for _, target := range history {
		if target != nil && target.ID != except && target.Status == types.ReleaseTargetActive {
			return target
		}
	}
	return nil
}

func latestRetainedTarget(history []*types.ProductionReleaseTarget, except string) *types.ProductionReleaseTarget {
	for _, target := range history {
		if target != nil && target.ID != except && target.Status == types.ReleaseTargetRolledBack {
			return target
		}
	}
	return nil
}

func targetID(target *types.ProductionReleaseTarget) string {
	if target == nil {
		return ""
	}
	return target.ID
}

// ProductionGraphSpanReadiness validates that every required per-chunk graph
// subtask in the latest completed parse attempt succeeded.
type ProductionGraphSpanReadiness struct {
	spans  apprepository.KnowledgeSpanRepository
	chunks interfaces.ChunkService
}

func NewProductionGraphSpanReadiness(spans apprepository.KnowledgeSpanRepository, chunks interfaces.ChunkService) ProductionGraphReadiness {
	return &ProductionGraphSpanReadiness{spans: spans, chunks: chunks}
}

func (r *ProductionGraphSpanReadiness) RequireSuccessfulGraphSubtasks(ctx context.Context, target *types.ProductionReleaseTarget, knowledge *types.Knowledge) error {
	_, _, _, graphModelID, strategy, err := productionProjectionProcessSnapshot(target.ConfigSnapshot)
	if err != nil {
		return err
	}
	if !strategy.GraphEnabled {
		return nil
	}
	if strings.TrimSpace(graphModelID) == "" || r == nil || r.spans == nil || r.chunks == nil {
		return errors.New("production graph readiness dependencies are unavailable")
	}
	chunks, err := r.chunks.ListChunksByKnowledgeID(ctx, knowledge.ID)
	if err != nil {
		return err
	}
	expected := 0
	for _, chunk := range chunks {
		if chunk != nil && chunk.ChunkType == types.ChunkTypeText {
			expected++
		}
	}
	if expected == 0 {
		return nil
	}
	attempt, err := r.spans.LatestAttempt(ctx, knowledge.ID)
	if err != nil || attempt <= 0 {
		return types.ErrProductionReleaseLifecycle
	}
	spans, err := r.spans.ListByAttempt(ctx, knowledge.ID, attempt)
	if err != nil {
		return err
	}
	latest := make(map[string]types.KnowledgeProcessingSpan, expected)
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "postprocess.graph.chunk[") {
			current, exists := latest[span.Name]
			if !exists || span.ID > current.ID {
				latest[span.Name] = span
			}
		}
	}
	if len(latest) != expected {
		return types.ErrProductionReleaseLifecycle
	}
	for _, span := range latest {
		if span.Status != types.SpanStatusDone {
			return types.ErrProductionReleaseLifecycle
		}
	}
	return nil
}

type ProductionWikiLifecycleQueue struct {
	tasks   interfaces.TaskEnqueuer
	pending interfaces.TaskPendingOpsRepository
}

func NewProductionWikiLifecycleQueue(tasks interfaces.TaskEnqueuer, pending interfaces.TaskPendingOpsRepository) ProductionWikiLifecycleEnqueuer {
	return &ProductionWikiLifecycleQueue{tasks: tasks, pending: pending}
}

func (q *ProductionWikiLifecycleQueue) EnqueueIngest(ctx context.Context, target *types.ProductionReleaseTarget) error {
	return q.enqueue(ctx, target, WikiPendingOp{Op: WikiOpIngest, KnowledgeID: target.KnowledgeID})
}

func (q *ProductionWikiLifecycleQueue) EnqueueRetract(ctx context.Context, target *types.ProductionReleaseTarget) error {
	return q.enqueue(ctx, target, WikiPendingOp{Op: WikiOpRetract, KnowledgeID: target.KnowledgeID})
}

func (q *ProductionWikiLifecycleQueue) enqueue(ctx context.Context, target *types.ProductionReleaseTarget, op WikiPendingOp) error {
	if q == nil || q.tasks == nil || q.pending == nil || target == nil {
		return errors.New("production wiki queue dependencies are unavailable")
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if err := q.pending.Enqueue(ctx, &types.TaskPendingOp{TenantID: target.TenantID, TaskType: wikiTaskType,
		Scope: wikiTaskScope, ScopeID: target.TargetKnowledgeBaseID, Op: op.Op,
		DedupKey: target.KnowledgeID, Payload: payload}); err != nil {
		return err
	}
	trigger, _ := json.Marshal(WikiIngestPayload{TenantID: target.TenantID, KnowledgeBaseID: target.TargetKnowledgeBaseID})
	_, err = q.tasks.Enqueue(asynq.NewTask(types.TypeWikiIngest, trigger,
		asynq.Queue(types.QueueWiki), asynq.MaxRetry(wikiIngestMaxRetry), asynq.Timeout(60*time.Minute)))
	return err
}

type ProductionCleanupTaskScheduler struct{ tasks interfaces.TaskEnqueuer }

func NewProductionCleanupTaskScheduler(tasks interfaces.TaskEnqueuer) ProductionProjectionCleanupScheduler {
	return &ProductionCleanupTaskScheduler{tasks: tasks}
}

func (s *ProductionCleanupTaskScheduler) EnqueueCleanup(_ context.Context, target *types.ProductionReleaseTarget) error {
	if s == nil || s.tasks == nil || target == nil {
		return errors.New("production cleanup scheduler is unavailable")
	}
	generation, err := productionProjectionCleanupGeneration(target)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
		Operation: types.ProductionProjectionOperationCleanup, TenantID: target.TenantID,
		ProjectID: target.ProjectID, TargetID: target.ID, CleanupGeneration: generation,
	})
	if err != nil {
		return err
	}
	task := asynq.NewTask(types.TypeProductionCleanup, payload, asynq.Queue(types.QueueProduction), asynq.MaxRetry(8))
	opts := []asynq.Option{
		asynq.TaskID(productionProjectionTaskID(target.ID, target.KnowledgeID, "cleanup-"+generation)),
		asynq.Retention(productionProjectionTaskRetention),
	}
	if target.RetentionUntil != nil && target.RetentionUntil.After(time.Now()) {
		opts = append(opts, asynq.ProcessAt(*target.RetentionUntil))
	}
	_, err = s.tasks.Enqueue(task, opts...)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

func productionProjectionCleanupGeneration(target *types.ProductionReleaseTarget) (string, error) {
	if target == nil || target.RetentionUntil == nil || target.RetentionUntil.IsZero() {
		return "", fmt.Errorf("%w: cleanup retention generation is unavailable", types.ErrProductionReleaseLifecycle)
	}
	var kind string
	var lifecycleAt *time.Time
	switch {
	case target.RolledBackAt != nil && !target.RolledBackAt.IsZero():
		kind, lifecycleAt = "rolled-back", target.RolledBackAt
	case target.FailedAt != nil && !target.FailedAt.IsZero():
		kind, lifecycleAt = "failed", target.FailedAt
	default:
		return "", fmt.Errorf("%w: cleanup lifecycle generation is unavailable", types.ErrProductionReleaseLifecycle)
	}
	return fmt.Sprintf("%s-%d-retain-%d", kind, lifecycleAt.UTC().UnixNano(), target.RetentionUntil.UTC().UnixNano()), nil
}
