package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type productionReleaseRepoStub struct {
	interfaces.ProductionReleaseRepository
	mu                sync.Mutex
	txMu              sync.Mutex
	release           *types.ProductionRelease
	releases          []*types.ProductionRelease
	createCalls       int
	targets           map[string]*types.ProductionReleaseTarget
	history           []*types.ProductionReleaseTarget
	lock              int
	events            *[]string
	transitionCalls   int
	transitionErrAt   int
	transitionErr     error
	switchErr         error
	cleanupListCalls  int
	failureListCalls  int
	failureCandidates []*types.ProductionReleaseTarget
	onTargetLock      func(context.Context)
	now               func() time.Time
}

func (r *productionReleaseRepoStub) CreateRelease(_ context.Context, release *types.ProductionRelease, targets []*types.ProductionReleaseTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.releases {
		if existing.TenantID != release.TenantID || existing.ProjectID != release.ProjectID ||
			existing.DocumentID != release.DocumentID || existing.VersionID != release.VersionID {
			continue
		}
		if existing.SupersedesReleaseID == nil && release.SupersedesReleaseID == nil {
			return types.ErrProductionConflict
		}
		if existing.SupersedesReleaseID != nil && release.SupersedesReleaseID != nil &&
			*existing.SupersedesReleaseID == *release.SupersedesReleaseID {
			return types.ErrProductionConflict
		}
	}
	r.release = release
	r.release.Targets = targets
	r.releases = append(r.releases, release)
	r.createCalls++
	return nil
}

func (r *productionReleaseRepoStub) GetLatestReleaseForVersion(
	_ context.Context, tenantID uint64, documentID, versionID string,
) (*types.ProductionRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := len(r.releases) - 1; index >= 0; index-- {
		release := r.releases[index]
		if release.TenantID == tenantID && release.DocumentID == documentID && release.VersionID == versionID {
			copy := *release
			copy.Targets = make([]*types.ProductionReleaseTarget, 0, len(release.Targets))
			for _, target := range release.Targets {
				targetCopy := *target
				copy.Targets = append(copy.Targets, &targetCopy)
			}
			return &copy, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *productionReleaseRepoStub) GetTarget(_ context.Context, _ uint64, id string) (*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target := r.targets[id]
	if target == nil {
		return nil, errors.New("target missing")
	}
	copy := *target
	return &copy, nil
}

func (r *productionReleaseRepoStub) GetTargetForUpdate(ctx context.Context, tenantID uint64, id string) (*types.ProductionReleaseTarget, error) {
	target, err := r.GetTarget(ctx, tenantID, id)
	if err == nil && r.onTargetLock != nil {
		r.onTargetLock(ctx)
	}
	return target, err
}

func (r *productionReleaseRepoStub) ListProjectionHistory(context.Context, uint64, string, string) ([]*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*types.ProductionReleaseTarget, 0, len(r.history))
	for _, target := range r.history {
		copy := *target
		out = append(out, &copy)
	}
	return out, nil
}

func (r *productionReleaseRepoStub) ListCleanupEligible(_ context.Context, _ uint64, limit int) ([]*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupListCalls++
	now := time.Now()
	out := make([]*types.ProductionReleaseTarget, 0, limit)
	for _, target := range r.targets {
		if len(out) == limit {
			break
		}
		if (target.Status == types.ReleaseTargetRolledBack || target.Status == types.ReleaseTargetFailed || target.Status == types.ReleaseTargetCleanupPending) &&
			target.RetentionUntil != nil && !target.RetentionUntil.After(now) {
			copy := *target
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (r *productionReleaseRepoStub) ListBuildingTargetsWithFailedKnowledge(
	_ context.Context,
	_ uint64,
	limit int,
) ([]*types.ProductionReleaseTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failureListCalls++
	if r.failureCandidates != nil {
		out := make([]*types.ProductionReleaseTarget, 0, len(r.failureCandidates))
		for _, target := range r.failureCandidates {
			copy := *target
			out = append(out, &copy)
		}
		return out, nil
	}
	out := make([]*types.ProductionReleaseTarget, 0, limit)
	for _, target := range r.targets {
		if len(out) == limit {
			break
		}
		if target.Status == types.ReleaseTargetBuilding {
			copy := *target
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (r *productionReleaseRepoStub) TransitionTargetForRetry(
	_ context.Context,
	id string,
	from types.ProductionReleaseTargetStatus,
	expectedUpdatedAt time.Time,
) (*types.ProductionReleaseTarget, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target := r.targets[id]
	if target == nil || target.Status != from || !target.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, false, nil
	}
	next := expectedUpdatedAt.Add(time.Second)
	target.Status = types.ReleaseTargetBuilding
	target.FailedAt = nil
	target.RolledBackAt = nil
	target.RetentionUntil = nil
	target.FailureCode = ""
	target.FailureReason = ""
	target.UpdatedAt = next
	copy := *target
	return &copy, true, nil
}

func (r *productionReleaseRepoStub) DeferProjectionFailureRecovery(
	_ context.Context,
	id string,
	expectedUpdatedAt time.Time,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target := r.targets[id]
	if target == nil || target.Status != types.ReleaseTargetBuilding || !target.UpdatedAt.Equal(expectedUpdatedAt) {
		return false, nil
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	target.RecoveryAttemptedAt = &now
	return true, nil
}

func (r *productionReleaseRepoStub) SwitchHead(_ context.Context, _ uint64, _, _, targetID string, expected int) (*types.ProductionProjectionHead, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.switchErr != nil {
		return nil, r.switchErr
	}
	if expected != r.lock {
		return nil, types.ErrProductionProjectionConflict
	}
	for _, target := range r.targets {
		if target.Status == types.ReleaseTargetActive {
			target.Status = types.ReleaseTargetRolledBack
			rolledBack := time.Now()
			retained := rolledBack.Add(time.Hour)
			target.RolledBackAt = &rolledBack
			target.RetentionUntil = &retained
		}
	}
	target := r.targets[targetID]
	if target == nil || target.Status != types.ReleaseTargetReady {
		return nil, types.ErrProductionReleaseLifecycle
	}
	target.Status = types.ReleaseTargetActive
	target.RetentionUntil = nil
	target.FailedAt = nil
	target.RolledBackAt = nil
	r.lock++
	if r.events != nil {
		*r.events = append(*r.events, "projection.head_switched")
	}
	return &types.ProductionProjectionHead{TenantID: target.TenantID, DocumentID: target.DocumentID,
		TargetKnowledgeBaseID: target.TargetKnowledgeBaseID, ActiveReleaseTargetID: targetID, LockVersion: r.lock}, nil
}

func (r *productionReleaseRepoStub) TransitionTarget(_ context.Context, id string, from, to types.ProductionReleaseTargetStatus, patch types.JSONMap) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitionCalls++
	if r.transitionErrAt == r.transitionCalls {
		if r.transitionErr != nil {
			return false, r.transitionErr
		}
		return false, errors.New("injected transition failure")
	}
	target := r.targets[id]
	if target == nil || target.Status != from {
		return false, nil
	}
	target.Status = to
	if to == types.ReleaseTargetBuilding || to == types.ReleaseTargetReady {
		target.RetentionUntil = nil
		target.FailedAt = nil
		target.RolledBackAt = nil
		target.FailureCode = ""
		target.FailureReason = ""
	}
	if to == types.ReleaseTargetFailed {
		now := time.Now().UTC()
		if r.now != nil {
			now = r.now().UTC()
		}
		target.FailedAt = &now
		retentionUntil := now.Add(time.Duration(target.RetentionDays) * 24 * time.Hour)
		target.RetentionUntil = &retentionUntil
		target.FailureCode = types.ProductionProjectionFailureBuildFailed
		target.FailureReason = types.ProductionProjectionFailureReasonBuildFailed
		if code, ok := patch["failure_code"].(string); ok {
			target.FailureCode = code
		}
		if reason, ok := patch["failure_reason"].(string); ok {
			target.FailureReason = reason
		}
	}
	if to == types.ReleaseTargetCleanupPending {
		now := time.Now().UTC()
		if r.now != nil {
			now = r.now().UTC()
		}
		target.CleanupRequestedAt = &now
	}
	if to == types.ReleaseTargetCleaned {
		now := time.Now().UTC()
		if r.now != nil {
			now = r.now().UTC()
		}
		target.CleanedAt = &now
	}
	return true, nil
}

type productionReleaseDocumentsStub struct {
	interfaces.ProductionDocumentRepository
	document *types.ProductionDocument
	version  *types.ProductionDocumentVersion
}

func (s *productionReleaseDocumentsStub) GetDocument(context.Context, uint64, string) (*types.ProductionDocument, error) {
	return s.document, nil
}
func (s *productionReleaseDocumentsStub) GetVersion(context.Context, uint64, string) (*types.ProductionDocumentVersion, error) {
	return s.version, nil
}

type productionReleaseReviewsStub struct {
	interfaces.ProductionReviewRepository
	review *types.ProductionReviewRequest
}

type productionReleaseDocumentTypeRepoStub struct {
	interfaces.ProductionDocumentTypeRepository
	documentType *types.ProductionDocumentType
	requestedID  string
}

func (s *productionReleaseDocumentTypeRepoStub) GetByID(_ context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error) {
	s.requestedID = documentTypeID
	if s.documentType == nil || s.documentType.TenantID != tenantID || s.documentType.ID != documentTypeID {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *s.documentType
	return &copy, nil
}

func (s *productionReleaseReviewsStub) GetApprovedReviewForVersion(context.Context, uint64, string, string) (*types.ProductionReviewRequest, error) {
	return s.review, nil
}

type productionReleaseAuthorizerStub struct{ err error }

func (s productionReleaseAuthorizerStub) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return s.err
}

func (s productionReleaseAuthorizerStub) HasLiveRoleAssignee(context.Context, uint64, string, types.ProductionRole) (bool, error) {
	return true, s.err
}

type productionReleaseMembershipStub struct {
	interfaces.TenantMemberService
	role types.TenantRole
}

func (s productionReleaseMembershipStub) GetMembership(context.Context, string, uint64) (*types.TenantMember, error) {
	return &types.TenantMember{Role: s.role, Status: types.TenantMemberStatusActive}, nil
}

type productionReleaseTenantRepoStub struct {
	interfaces.TenantRepository
	tenant *types.Tenant
}

func (s productionReleaseTenantRepoStub) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return s.tenant, nil
}

type productionReleaseKBStub struct {
	interfaces.KnowledgeBaseService
	kb  *types.KnowledgeBase
	kbs []*types.KnowledgeBase
	err error
}

type productionReleaseCountingKBStub struct {
	interfaces.KnowledgeBaseService
	kb    *types.KnowledgeBase
	calls int
}

func (s *productionReleaseCountingKBStub) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	s.calls++
	copy := *s.kb
	return &copy, nil
}

func (s productionReleaseKBStub) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	copy := *s.kb
	return &copy, nil
}

func (s productionReleaseKBStub) ListKnowledgeBases(context.Context) ([]*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.kbs != nil {
		return s.kbs, nil
	}
	return []*types.KnowledgeBase{s.kb}, nil
}

type productionReleaseKBRepositoryBoundary struct {
	interfaces.KnowledgeBaseService
	repo interfaces.KnowledgeBaseRepository
}

func (s productionReleaseKBRepositoryBoundary) GetKnowledgeBaseByIDOnly(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	return s.repo.GetKnowledgeBaseByID(ctx, id)
}

type productionReleaseModelStub struct{ interfaces.ModelService }

func (productionReleaseModelStub) GetModelByID(_ context.Context, id string) (*types.Model, error) {
	typ := types.ModelTypeKnowledgeQA
	if id == "embedding-1" {
		typ = types.ModelTypeEmbedding
	}
	return &types.Model{ID: id, TenantID: 7, Type: typ, Status: types.ModelStatusActive}, nil
}

type productionReleaseKnowledgeStub struct {
	interfaces.KnowledgeService
	knowledge *types.Knowledge
	repo      interfaces.KnowledgeRepository
}

func (s productionReleaseKnowledgeStub) GetKnowledgeByID(context.Context, string) (*types.Knowledge, error) {
	copy := *s.knowledge
	return &copy, nil
}

func (s productionReleaseKnowledgeStub) GetKnowledgeByIDForSystem(ctx context.Context, id string) (*types.Knowledge, error) {
	return s.GetKnowledgeByID(ctx, id)
}

func (s productionReleaseKnowledgeStub) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	copy := *s.knowledge
	return &copy, nil
}

func (s productionReleaseKnowledgeStub) GetKnowledgeByIDOnlyForSystem(ctx context.Context, id string) (*types.Knowledge, error) {
	return s.GetKnowledgeByIDOnly(ctx, id)
}

func (s productionReleaseKnowledgeStub) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}

type productionReleaseKnowledgeRepoStub struct {
	interfaces.KnowledgeRepository
	mu        sync.Mutex
	knowledge *types.Knowledge
}

func (r *productionReleaseKnowledgeRepoStub) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.knowledge
	return &copy, nil
}

func (r *productionReleaseKnowledgeRepoStub) GetKnowledgeByIDOnlyForUpdate(ctx context.Context, id string) (*types.Knowledge, error) {
	return r.GetKnowledgeByIDOnly(ctx, id)
}

func (r *productionReleaseKnowledgeRepoStub) ClaimFailedKnowledgeRetry(_ context.Context, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.knowledge.ParseStatus != types.ParseStatusFailed {
		return false, nil
	}
	r.knowledge.ParseStatus = types.ParseStatusPending
	r.knowledge.PendingSubtasksCount = 0
	r.knowledge.ErrorMessage = ""
	r.knowledge.UpdatedAt = time.Now().UTC()
	return true, nil
}

func (r *productionReleaseKnowledgeRepoStub) UpdateKnowledgeColumns(
	_ context.Context,
	_ string,
	updates map[string]interface{},
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status, ok := updates["parse_status"].(string); ok {
		r.knowledge.ParseStatus = status
	}
	if message, ok := updates["error_message"].(string); ok {
		r.knowledge.ErrorMessage = message
	}
	return nil
}

type productionReleaseGraphStub struct{ err error }

func (s productionReleaseGraphStub) RequireSuccessfulGraphSubtasks(context.Context, *types.ProductionReleaseTarget, *types.Knowledge) error {
	return s.err
}

type productionReleaseBuilderStub struct {
	err   error
	calls int
	repo  *productionReleaseRepoStub
}

func (s *productionReleaseBuilderStub) Build(ctx context.Context, targetID string) (*types.Knowledge, error) {
	s.calls++
	if s.repo != nil {
		target, err := s.repo.GetTarget(ctx, 7, targetID)
		if err != nil {
			return nil, err
		}
		if target.Status == types.ReleaseTargetFailed || target.Status == types.ReleaseTargetRolledBack {
			if _, err := s.repo.TransitionTarget(ctx, targetID, target.Status, types.ReleaseTargetBuilding, nil); err != nil {
				return nil, err
			}
		}
	}
	return nil, s.err
}

type productionReleaseFileServiceStub struct {
	interfaces.FileService
	err error
}

func (s productionReleaseFileServiceStub) CheckConnectivity(context.Context) error { return s.err }

type productionReleaseStorageResolverStub struct {
	interfaces.StorageBackendResolver
	mu               sync.Mutex
	backend          *types.StorageBackend
	resolveErr       error
	fileErr          error
	connectErr       error
	nilFileService   bool
	resolvedID       string
	resolvedProvider string
	resolvedTenant   uint64
	fileID           string
	fileProvider     string
	fileTenant       uint64
}

func (s *productionReleaseStorageResolverStub) ResolveBackend(_ context.Context, tenant *types.Tenant, backendID, provider string) (*types.StorageBackend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolvedID = backendID
	s.resolvedProvider = provider
	if tenant != nil {
		s.resolvedTenant = tenant.ID
	}
	return s.backend, s.resolveErr
}

func (s *productionReleaseStorageResolverStub) ResolveFileService(_ context.Context, tenant *types.Tenant, backendID, provider, _ string) (interfaces.FileService, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileID = backendID
	s.fileProvider = provider
	if tenant != nil {
		s.fileTenant = tenant.ID
	}
	if s.fileErr != nil {
		return nil, "", s.fileErr
	}
	if s.nilFileService {
		return nil, provider, nil
	}
	return productionReleaseFileServiceStub{err: s.connectErr}, provider, nil
}

type productionReleaseWikiStub struct {
	events *[]string
	err    error
}

type productionReleaseWikiNoopStub struct{}

func (productionReleaseWikiNoopStub) EnqueueIngest(context.Context, *types.ProductionReleaseTarget) error {
	return nil
}
func (productionReleaseWikiNoopStub) EnqueueRetract(context.Context, *types.ProductionReleaseTarget) error {
	return nil
}

func (s productionReleaseWikiStub) EnqueueIngest(_ context.Context, target *types.ProductionReleaseTarget) error {
	*s.events = append(*s.events, "wiki.ingest:"+target.KnowledgeID)
	return s.err
}
func (s productionReleaseWikiStub) EnqueueRetract(_ context.Context, target *types.ProductionReleaseTarget) error {
	*s.events = append(*s.events, "wiki.retract:"+target.KnowledgeID)
	return s.err
}

type productionReleaseCleanupStub struct {
	events *[]string
	err    error
}

type productionReleaseCleanupNoopStub struct{}

func (productionReleaseCleanupNoopStub) EnqueueCleanup(context.Context, *types.ProductionReleaseTarget) error {
	return nil
}

type productionReleaseTaskEnqueuerStub struct {
	mu       sync.Mutex
	accepted map[string]*asynq.Task
	err      error
	calls    int
}

func (s *productionReleaseTaskEnqueuerStub) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	taskID := ""
	queue := ""
	for _, opt := range opts {
		switch opt.Type() {
		case asynq.TaskIDOpt:
			taskID, _ = opt.Value().(string)
		case asynq.QueueOpt:
			queue, _ = opt.Value().(string)
		}
	}
	if taskID != "" {
		if _, duplicate := s.accepted[taskID]; duplicate {
			return nil, asynq.ErrTaskIDConflict
		}
		s.accepted[taskID] = task
	}
	return &asynq.TaskInfo{ID: taskID, Queue: queue, Type: task.Type()}, nil
}

type productionReleasePendingOpsStub struct {
	interfaces.TaskPendingOpsRepository
	err error
	ops []*types.TaskPendingOp
}

func (s *productionReleasePendingOpsStub) Enqueue(_ context.Context, op *types.TaskPendingOp) error {
	if s.err != nil {
		return s.err
	}
	s.ops = append(s.ops, op)
	return nil
}

func (s productionReleaseCleanupStub) EnqueueCleanup(_ context.Context, target *types.ProductionReleaseTarget) error {
	*s.events = append(*s.events, "cleanup:"+target.ID)
	return s.err
}

type productionReleaseAuditStub struct {
	interfaces.AuditLogService
	mu      sync.Mutex
	err     error
	entries []*types.AuditLog
}

func (s *productionReleaseAuditStub) Log(_ context.Context, entry *types.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, entry)
	return nil
}

type productionReleaseUOWStub struct {
	interfaces.ProductionUnitOfWork
	repo *productionReleaseRepoStub
}

func (s productionReleaseUOWStub) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	if s.repo == nil {
		return fn(ctx)
	}
	s.repo.txMu.Lock()
	defer s.repo.txMu.Unlock()
	s.repo.mu.Lock()
	targets := make(map[string]types.ProductionReleaseTarget, len(s.repo.targets))
	for id, target := range s.repo.targets {
		targets[id] = *target
	}
	lock := s.repo.lock
	eventLen := 0
	if s.repo.events != nil {
		eventLen = len(*s.repo.events)
	}
	s.repo.mu.Unlock()
	err := fn(ctx)
	if err == nil {
		return nil
	}
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	for id, target := range targets {
		*s.repo.targets[id] = target
	}
	s.repo.lock = lock
	if s.repo.events != nil {
		*s.repo.events = (*s.repo.events)[:eventLen]
	}
	return err
}

func productionReleaseContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "00000000-0000-4000-8000-000000000007")
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7, RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{
		{RetrieverType: types.VectorRetrieverType, RetrieverEngineType: types.PostgresRetrieverEngineType},
	}}})
	return ctx
}

func newProductionReleaseServiceFixture(t *testing.T) (*ProductionReleaseService, *productionReleaseRepoStub, *[]string) {
	t.Helper()
	events := make([]string, 0)
	old := &types.ProductionReleaseTarget{ID: "target-old", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1",
		VersionID: "version-2", TargetKnowledgeBaseID: "kb-1", KnowledgeID: "knowledge-old", Status: types.ReleaseTargetActive,
		ConfigSnapshot: types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true},"retriever_engines":[{"retriever_engine_type":"postgres","retriever_type":"vector"}]}`)}
	old.ConfigSnapshot, old.ConfigDigest, _ = types.CanonicalProductionReleaseTargetConfig(old.ConfigSnapshot)
	ready := &types.ProductionReleaseTarget{ID: "target-new", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1",
		VersionID: "version-3", TargetKnowledgeBaseID: "kb-1", KnowledgeID: "knowledge-new", Status: types.ReleaseTargetReady,
		ConfigSnapshot: types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":true,"graph_enabled":true},"chunking":{"strategy":"recursive","chunk_size":256},"embedding_model_id":"embedding-1","summary_model_id":"summary-1","retriever_engines":[{"retriever_engine_type":"postgres","retriever_type":"vector"}],"graph":{"enabled":true,"model_id":"summary-1","extract_config":{"enabled":true}}}`)}
	ready.ConfigSnapshot, ready.ConfigDigest, _ = types.CanonicalProductionReleaseTargetConfig(ready.ConfigSnapshot)
	repo := &productionReleaseRepoStub{targets: map[string]*types.ProductionReleaseTarget{old.ID: old, ready.ID: ready}, history: []*types.ProductionReleaseTarget{ready, old}, lock: 1, events: &events}
	storage := &productionReleaseStorageResolverStub{backend: &types.StorageBackend{
		ID: "storage-1", TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive,
	}}
	knowledge := productionReleaseProjectionKnowledge(t, ready, types.ParseStatusCompleted)
	knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
	service := &ProductionReleaseService{
		releases: repo,
		documents: &productionReleaseDocumentsStub{document: &types.ProductionDocument{ID: "document-1", TenantID: 7, ProjectID: "project-1", DocumentTypeID: "type-1", DocumentTypeSchemaVersion: 1, LatestApprovedVersionID: productionStringPtr("version-3")},
			version: &types.ProductionDocumentVersion{ID: "version-3", DocumentID: "document-1", TenantID: 7, ProjectID: "project-1", FrozenAt: productionTimePtr(time.Now())}},
		documentTypes: &productionReleaseDocumentTypeRepoStub{documentType: &types.ProductionDocumentType{
			ID: "type-1", TenantID: 7, SchemaVersion: 1, Status: types.ProductionDocumentTypeActive,
			PublicationPolicy: types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
		}},
		reviews:    &productionReleaseReviewsStub{review: &types.ProductionReviewRequest{ID: "review-1", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1", VersionID: "version-3", Status: types.ProductionReviewApproved}},
		authorizer: productionReleaseAuthorizerStub{}, members: productionReleaseMembershipStub{role: types.TenantRoleContributor},
		tenants: productionReleaseTenantRepoStub{tenant: &types.Tenant{
			ID: 7,
			RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{{
				RetrieverType: types.VectorRetrieverType, RetrieverEngineType: types.PostgresRetrieverEngineType,
			}}},
		}},
		models: productionReleaseModelStub{}, storage: storage,
		kbs:       productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument}},
		knowledge: productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo},
		graph:     productionReleaseGraphStub{}, wiki: productionReleaseWikiStub{events: &events}, cleanup: productionReleaseCleanupStub{events: &events},
		uow: productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{}, now: time.Now,
	}
	return service, repo, &events
}

func configuredProductionReleaseTargetKB() *types.KnowledgeBase {
	return &types.KnowledgeBase{
		ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007",
		Type:                  types.KnowledgeBaseTypeDocument,
		StorageProviderConfig: &types.StorageProviderConfig{Provider: "local"},
		ChunkingConfig:        types.ChunkingConfig{Strategy: "recursive", ChunkSize: 256},
		EmbeddingModelID:      "embedding-1",
		SummaryModelID:        "summary-1",
		IndexingStrategy:      types.IndexingStrategy{VectorEnabled: true, GraphEnabled: true},
		ExtractConfig:         &types.ExtractConfig{Enabled: true},
	}
}

func TestProductionReleasePrepareEnforcesExactBoundPublicationPolicy(t *testing.T) {
	tests := []struct {
		name       string
		policy     types.JSON
		wantReason string
	}{
		{
			name:       "non knowledge base target",
			policy:     types.JSON(`{"version":1,"target_type":"wiki","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
			wantReason: "publication_target_type_invalid",
		},
		{
			name:       "chunking override",
			policy:     types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"fixed","knowledge_graph":"inherit_target","require_approved_review":true}`),
			wantReason: "publication_chunking_mode_invalid",
		},
		{
			name:       "knowledge graph override",
			policy:     types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"disabled","require_approved_review":true}`),
			wantReason: "publication_knowledge_graph_mode_invalid",
		},
		{
			name:       "approved review disabled",
			policy:     types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":false}`),
			wantReason: "publication_approved_review_required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			svc.kbs = productionReleaseKBStub{kb: configuredProductionReleaseTargetKB()}
			document := svc.documents.(*productionReleaseDocumentsStub).document
			document.DocumentTypeID = "type-bound"
			document.DocumentTypeSchemaVersion = 4
			svc.documentTypes = &productionReleaseDocumentTypeRepoStub{documentType: &types.ProductionDocumentType{
				ID: "type-bound", TenantID: 7, SchemaVersion: 4, Status: types.ProductionDocumentTypeActive,
				PublicationPolicy: test.policy,
			}}

			release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})

			require.Nil(t, release)
			require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
			require.ErrorContains(t, err, test.wantReason)
			require.Nil(t, repo.release)
		})
	}
}

func TestProductionReleasePrepareUsesRetiredExactBoundPublicationPolicy(t *testing.T) {
	svc, _, _ := newProductionReleaseServiceFixture(t)
	svc.kbs = productionReleaseKBStub{kb: configuredProductionReleaseTargetKB()}
	document := svc.documents.(*productionReleaseDocumentsStub).document
	document.DocumentTypeID = "type-bound"
	document.DocumentTypeSchemaVersion = 4
	typesRepo := &productionReleaseDocumentTypeRepoStub{documentType: &types.ProductionDocumentType{
		ID: "type-bound", TenantID: 7, SchemaVersion: 4, Status: types.ProductionDocumentTypeRetired,
		PublicationPolicy: types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
	}}
	svc.documentTypes = typesRepo

	release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})

	require.NoError(t, err)
	require.NotNil(t, release)
	require.Equal(t, "type-bound", typesRepo.requestedID)
}

func TestProductionReleasePrepareNormalizesMissingDocumentTypeBinding(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	svc.kbs = productionReleaseKBStub{kb: configuredProductionReleaseTargetKB()}
	document := svc.documents.(*productionReleaseDocumentsStub).document
	document.DocumentTypeID = "missing-type"
	document.DocumentTypeSchemaVersion = 4
	svc.documentTypes = &productionReleaseDocumentTypeRepoStub{}

	release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})

	require.Nil(t, release)
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
	require.ErrorContains(t, err, "publication_document_type_binding_invalid")
	require.Nil(t, repo.release)
}

func TestProductionReleasePreflightReturnsRenderedSnapshotAndOnlyWritableTargets(t *testing.T) {
	service, _, _ := newProductionReleaseServiceFixture(t)
	version := service.documents.(*productionReleaseDocumentsStub).version
	version.Blocks = []*types.ProductionDocumentBlock{{
		ID: "block-1", VersionID: version.ID, LogicalBlockID: "intro", BlockType: "heading", Position: 0,
		Content: types.JSON(`"Release snapshot"`), Attributes: types.JSON(`{"level":1}`),
	}}
	writable := configuredProductionReleaseTargetKB()
	writable.Name = "Production KB"
	foreign := *writable
	foreign.ID = "kb-foreign"
	foreign.TenantID = 8
	service.kbs = productionReleaseKBStub{kb: writable, kbs: []*types.KnowledgeBase{writable, &foreign}}

	preflightService, ok := any(service).(interface {
		Preflight(context.Context, string, string, int, int) (*types.ProductionReleasePreflight, error)
	})
	require.True(t, ok)
	result, err := preflightService.Preflight(productionReleaseContext(), "document-1", "version-3", 1, 100)
	require.NoError(t, err)
	require.Contains(t, result.RenderedMarkdown, "Release snapshot")
	require.Len(t, result.Targets, 1)
	require.Equal(t, writable.ID, result.Targets[0].KnowledgeBaseID)
	require.True(t, result.Targets[0].Ready)
	require.NotEmpty(t, result.Targets[0].ConfigSnapshot)
	require.Equal(t, 2, result.Total)
	require.False(t, result.HasMore)
}

func TestProductionReleasePreflightKeepsWritableTargetWithGenericReadinessFailure(t *testing.T) {
	service, _, _ := newProductionReleaseServiceFixture(t)
	broken := configuredProductionReleaseTargetKB()
	broken.Name = "Needs configuration"
	broken.EmbeddingModelID = ""
	service.kbs = productionReleaseKBStub{kb: broken, kbs: []*types.KnowledgeBase{broken}}

	preflightService := any(service).(interface {
		Preflight(context.Context, string, string, int, int) (*types.ProductionReleasePreflight, error)
	})
	result, err := preflightService.Preflight(productionReleaseContext(), "document-1", "version-3", 1, 100)
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)
	require.False(t, result.Targets[0].Ready)
	require.Equal(t, "processing_configuration_unavailable", result.Targets[0].Reason)
	require.Empty(t, result.Targets[0].ConfigSnapshot)
}

func TestProductionReleasePreflightRejectsPaginationBeforeKBReadiness(t *testing.T) {
	service, _, _ := newProductionReleaseServiceFixture(t)
	counting := &productionReleaseCountingKBStub{kb: configuredProductionReleaseTargetKB()}
	service.kbs = counting
	preflightService := any(service).(interface {
		Preflight(context.Context, string, string, int, int) (*types.ProductionReleasePreflight, error)
	})
	_, err := preflightService.Preflight(
		productionReleaseContext(), "document-1", "version-3", 1, types.ProductionReleaseMaxTargets+1,
	)
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
	require.Zero(t, counting.calls)
}

func TestProductionReleasePrepareRequiresPublisherAndOwnedWritableKB(t *testing.T) {
	svc, _, _ := newProductionReleaseServiceFixture(t)
	svc.authorizer = productionReleaseAuthorizerStub{err: types.ErrProductionForbidden}
	_, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	svc.authorizer = productionReleaseAuthorizerStub{}
	svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 8, Type: types.KnowledgeBaseTypeDocument}}
	_, err = svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleasePrepareRejectsTooManyTargetsBeforeReadinessWork(t *testing.T) {
	svc, _, _ := newProductionReleaseServiceFixture(t)
	kbs := &productionReleaseCountingKBStub{kb: configuredProductionReleaseTargetKB()}
	svc.kbs = kbs
	targetIDs := make([]string, 101)
	for index := range targetIDs {
		targetIDs[index] = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
	}

	_, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", targetIDs)
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
	require.Zero(t, kbs.calls)
}

func TestProductionActivationRequiresReprepareWhenVectorStoreBindingChanged(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	snapshot, digest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{
		"version":1,
		"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false},
		"chunking":{"strategy":"recursive","chunk_size":256},
		"embedding_model_id":"embedding-1","summary_model_id":"summary-1",
		"graph":{"enabled":false,"model_id":"summary-1"},
		"vector_store_id":"snapshot-store"
	}`))
	require.NoError(t, err)
	target.ConfigSnapshot = snapshot
	target.ConfigDigest = digest
	currentStore := "current-store"
	svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007",
		Type: types.KnowledgeBaseTypeDocument, VectorStoreID: &currentStore,
	}}

	err = svc.Activate(productionReleaseContext(), target.ID, 1)
	require.ErrorIs(t, err, types.ErrProductionReleaseReprepareRequired)
	require.Equal(t, 1, repo.lock)
}

func TestProductionReleaseWritableTargetKBNormalizesConcreteRepositoryNotFoundAndForeignTenant(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}))
	repo := apprepository.NewKnowledgeBaseRepository(db)

	svc, _, _ := newProductionReleaseServiceFixture(t)
	svc.kbs = productionReleaseKBRepositoryBoundary{repo: repo}

	_, err = svc.requireWritableTargetKB(productionReleaseContext(), 7, "00000000-0000-4000-8000-000000000007", "missing-kb")
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	foreign := &types.KnowledgeBase{ID: "foreign-kb", TenantID: 8, CreatorID: "foreign-user", Type: types.KnowledgeBaseTypeDocument}
	require.NoError(t, repo.CreateKnowledgeBase(context.Background(), foreign))
	_, err = svc.requireWritableTargetKB(productionReleaseContext(), 7, "00000000-0000-4000-8000-000000000007", foreign.ID)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleasePrepareSnapshotsImmutableTargetProcessingConfig(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	version := &types.ProductionDocumentVersion{ID: "version-3", DocumentID: "document-1", TenantID: 7, ProjectID: "project-1", FrozenAt: productionTimePtr(time.Now())}
	review := &types.ProductionReviewRequest{ID: "review-1", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1", VersionID: "version-3", Status: types.ProductionReviewApproved}
	svc.documents = &productionReleaseDocumentsStub{document: &types.ProductionDocument{
		ID: "document-1", TenantID: 7, ProjectID: "project-1", DocumentTypeID: "type-1",
		DocumentTypeSchemaVersion: 1, LatestApprovedVersionID: productionStringPtr("version-3"),
	}, version: version}
	svc.reviews = &productionReleaseReviewsStub{review: review}
	svc.models = productionReleaseModelStub{}
	svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument,
		StorageProviderConfig: &types.StorageProviderConfig{Provider: "local"}, ChunkingConfig: types.ChunkingConfig{Strategy: "recursive", ChunkSize: 256},
		EmbeddingModelID: "embedding-1", SummaryModelID: "summary-1", IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, GraphEnabled: true}, ExtractConfig: &types.ExtractConfig{Enabled: true}}}
	release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.NoError(t, err)
	require.Len(t, release.Targets, 1)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"embedding_model_id":"embedding-1"`)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"strategy":"recursive"`)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"retriever_engines"`)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"storage_backend_id":"storage-1"`)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"storage_provider":"local"`)
	require.NotEmpty(t, release.Targets[0].ConfigDigest)
	require.Same(t, release, repo.release)
}

func TestProductionReleasePrepareReusesEquivalentLatestAndSupersedesChangedConfig(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	kb := configuredProductionReleaseTargetKB()
	svc.kbs = productionReleaseKBStub{kb: kb}

	first, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.NoError(t, err)
	replayed, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.NoError(t, err)
	require.Equal(t, first.ID, replayed.ID)
	require.Equal(t, 1, repo.createCalls)

	kb.ChunkingConfig.ChunkSize = 512
	successor, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, successor.ID)
	require.NotNil(t, successor.SupersedesReleaseID)
	require.Equal(t, first.ID, *successor.SupersedesReleaseID)
	require.Equal(t, 2, repo.createCalls)
}

func TestProductionReleasePrepareConcurrentEquivalentReprepareCreatesOneSuccessor(t *testing.T) {
	const callers = 32
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	kb := configuredProductionReleaseTargetKB()
	svc.kbs = productionReleaseKBStub{kb: kb}

	first, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
	require.NoError(t, err)
	kb.ChunkingConfig.ChunkSize = 512

	start := make(chan struct{})
	results := make(chan *types.ProductionRelease, callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			release, prepareErr := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})
			results <- release
			errs <- prepareErr
		}()
	}
	close(start)

	var successorID string
	for range callers {
		require.NoError(t, <-errs)
		release := <-results
		require.NotNil(t, release)
		if successorID == "" {
			successorID = release.ID
		}
		require.Equal(t, successorID, release.ID)
		require.NotNil(t, release.SupersedesReleaseID)
		require.Equal(t, first.ID, *release.SupersedesReleaseID)
	}
	require.Equal(t, 2, repo.createCalls, "the initial release and one successor are the only writes")
}

func TestProductionReleasePrepareRequiresHealthyConcreteStorageBackend(t *testing.T) {
	backendID := "storage-1"
	tests := []struct {
		name     string
		resolver *productionReleaseStorageResolverStub
	}{
		{name: "missing", resolver: &productionReleaseStorageResolverStub{}},
		{name: "foreign tenant", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: backendID, TenantID: 8, Provider: "local", Status: types.StorageBackendStatusActive}}},
		{name: "disabled", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: backendID, TenantID: 7, Provider: "local", Status: types.StorageBackendStatusDisabled}}},
		{name: "wrong identity", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: "storage-other", TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive}}},
		{name: "resolution failure", resolver: &productionReleaseStorageResolverStub{resolveErr: errors.New("storage lookup failed")}},
		{name: "file resolution failure", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: backendID, TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive}, fileErr: errors.New("file service failed")}},
		{name: "missing file service", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: backendID, TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive}, nilFileService: true}},
		{name: "connectivity failure", resolver: &productionReleaseStorageResolverStub{backend: &types.StorageBackend{ID: backendID, TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive}, connectErr: errors.New("storage offline")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			svc.storage = tc.resolver
			svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{
				ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument,
				StorageBackendID: &backendID, StorageProviderConfig: &types.StorageProviderConfig{Provider: "local"},
				ChunkingConfig: types.ChunkingConfig{Strategy: "recursive", ChunkSize: 256}, EmbeddingModelID: "embedding-1", SummaryModelID: "summary-1",
				IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
			}}

			_, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})

			require.Error(t, err)
			require.Nil(t, repo.release)
		})
	}
}

func TestProductionReleasePrepareSnapshotsResolvedStorageIdentity(t *testing.T) {
	requestedID := "storage-resolved"
	resolver := &productionReleaseStorageResolverStub{backend: &types.StorageBackend{
		ID: requestedID, TenantID: 7, Provider: "s3", Status: types.StorageBackendStatusActive,
	}}
	svc, _, _ := newProductionReleaseServiceFixture(t)
	svc.storage = resolver
	svc.kbs = productionReleaseKBStub{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument,
		StorageBackendID: &requestedID, StorageProviderConfig: &types.StorageProviderConfig{Provider: "local"},
		ChunkingConfig: types.ChunkingConfig{Strategy: "recursive", ChunkSize: 256}, EmbeddingModelID: "embedding-1", SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}}

	release, err := svc.Prepare(productionReleaseContext(), "document-1", "version-3", []string{"kb-1"})

	require.NoError(t, err)
	require.Equal(t, uint64(7), resolver.resolvedTenant)
	require.Equal(t, requestedID, resolver.resolvedID)
	require.Equal(t, requestedID, resolver.fileID)
	require.Equal(t, "s3", resolver.fileProvider)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"storage_backend_id":"storage-resolved"`)
	require.Contains(t, string(release.Targets[0].ConfigSnapshot), `"storage_provider":"s3"`)
}

func TestProductionReleaseBuildFailureLeavesOldHeadActive(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	svc.builder = &productionReleaseBuilderStub{err: errors.New("build failed")}
	svc.tasks = &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task), err: errors.New("queue unavailable")}
	repo.targets["target-new"].Status = types.ReleaseTargetFailed
	require.Error(t, svc.Retry(productionReleaseContext(), "target-new"))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
}

func TestProductionReleaseRetryOnlyEnqueuesOneDeterministicBuild(t *testing.T) {
	const callers = 32
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	repo.targets["target-new"].Status = types.ReleaseTargetFailed
	failedAt := time.Now()
	retained := time.Now().Add(time.Hour)
	repo.targets["target-new"].FailedAt = &failedAt
	repo.targets["target-new"].RetentionUntil = &retained
	builder := &productionReleaseBuilderStub{}
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	svc.builder = builder
	svc.tasks = tasks

	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			errs <- svc.Retry(productionReleaseContext(), "target-new")
		}()
	}
	close(start)
	for range callers {
		require.NoError(t, <-errs)
	}
	require.Zero(t, builder.calls, "Retry must never execute worker logic inline")
	require.Len(t, tasks.accepted, 1)

	var task *asynq.Task
	for _, accepted := range tasks.accepted {
		task = accepted
	}
	require.NotNil(t, task)
	var payload types.ProductionProjectionTaskPayload
	require.NoError(t, json.Unmarshal(task.Payload(), &payload))
	require.Equal(t, types.ProductionProjectionOperationBuild, payload.Operation)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})
	require.NoError(t, handler.Handle(productionReleaseContext(), task))
	require.Equal(t, 1, builder.calls)
}

func TestProductionReleaseRetryTaskIDTracksPersistedFailureGeneration(t *testing.T) {
	const callers = 24
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	builder := &productionReleaseBuilderStub{err: errors.New("worker failed")}
	svc.tasks = tasks
	svc.builder = builder
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetFailed
	firstFailure := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	firstRetention := firstFailure.Add(time.Hour)
	target.FailedAt = &firstFailure
	target.RetentionUntil = &firstRetention

	retryConcurrently(t, svc, callers)
	require.Len(t, tasks.accepted, 1)
	firstID, firstTask := onlyAcceptedProductionTask(t, tasks, "")
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})
	require.Error(t, handler.Handle(productionReleaseContext(), firstTask))
	require.Equal(t, 1, builder.calls)

	secondFailure := firstFailure.Add(time.Minute)
	secondRetention := secondFailure.Add(time.Hour)
	repo.mu.Lock()
	target.Status = types.ReleaseTargetFailed
	target.FailedAt = &secondFailure
	target.RetentionUntil = &secondRetention
	repo.mu.Unlock()

	retryConcurrently(t, svc, callers)
	require.Len(t, tasks.accepted, 2, "a later persisted failure must create a fresh retained task ID")
	secondID, secondTask := onlyAcceptedProductionTask(t, tasks, firstID)
	require.NotEqual(t, firstID, secondID)
	require.Error(t, handler.Handle(productionReleaseContext(), secondTask))
	require.Equal(t, 2, builder.calls)
}

func TestProductionProjectionWorkerPersistsDistinctFailureGenerationsWithoutRawErrors(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetBuilding
	target.RetentionDays = 30
	failureTimes := []time.Time{
		time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 21, 9, 1, 0, 0, time.UTC),
	}
	failureIndex := 0
	repo.now = func() time.Time {
		at := failureTimes[failureIndex]
		failureIndex++
		return at
	}
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	rawFailure := errors.New("provider rejected token=super-secret endpoint=10.0.0.7")
	builder := &productionReleaseBuilderStub{err: rawFailure, repo: repo}
	svc.tasks = tasks
	svc.builder = builder
	audit := svc.audit.(*productionReleaseAuditStub)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

	require.NoError(t, svc.enqueueProjectionTask(
		productionReleaseContext(), types.TypeProductionBuild,
		types.ProductionProjectionOperationBuild, target, 0,
	))
	firstID, firstTask := onlyAcceptedProductionTask(t, tasks, "")
	require.ErrorIs(t, handler.Handle(context.Background(), firstTask), rawFailure)
	firstFailed, err := repo.GetTarget(context.Background(), 7, target.ID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, firstFailed.Status)
	require.NotNil(t, firstFailed.FailedAt)
	require.Equal(t, failureTimes[0], *firstFailed.FailedAt)
	require.Equal(t, types.ProductionProjectionFailureBuildFailed, firstFailed.FailureCode)
	require.Equal(t, types.ProductionProjectionFailureReasonBuildFailed, firstFailed.FailureReason)
	require.NotContains(t, firstFailed.FailureReason, "super-secret")

	require.NoError(t, svc.Retry(productionReleaseContext(), target.ID))
	secondID, secondTask := onlyAcceptedProductionTask(t, tasks, firstID)
	require.NotEqual(t, firstID, secondID)
	require.ErrorIs(t, handler.Handle(context.Background(), secondTask), rawFailure)
	secondFailed, err := repo.GetTarget(context.Background(), 7, target.ID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, secondFailed.Status)
	require.NotNil(t, secondFailed.FailedAt)
	require.Equal(t, failureTimes[1], *secondFailed.FailedAt)
	require.NotEqual(t, firstFailed.FailedAt, secondFailed.FailedAt)

	require.NoError(t, svc.Retry(productionReleaseContext(), target.ID))
	thirdID, _ := onlyAcceptedProductionTask(t, tasks, secondID)
	require.NotEqual(t, secondID, thirdID)
	require.Len(t, tasks.accepted, 3)
	require.Equal(t, 2, builder.calls)
	require.Len(t, audit.entries, 2)
	for _, entry := range audit.entries {
		require.NotContains(t, string(entry.Details), "super-secret")
		require.LessOrEqual(t, len(entry.Details), types.ProductionProjectionFailureReasonMaxLength)
	}
}

func TestProductionProjectionDeadLetterFailureResolvesImmutableKnowledgeMetadata(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetBuilding
	target.RetentionDays = 30
	failedAt := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return failedAt }
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID,
		KnowledgeBaseID: target.TargetKnowledgeBaseID, Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusProcessing,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: projectionKnowledgeContentDigest(content), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge}

	isProjection, err := svc.RecordProjectionKnowledgeFailure(
		context.Background(), knowledge.ID, types.TypeKnowledgePostProcess,
	)

	require.NoError(t, err)
	require.True(t, isProjection)
	failed, err := repo.GetTarget(context.Background(), target.TenantID, target.ID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, failed.Status)
	require.Equal(t, failedAt, *failed.FailedAt)
	require.Equal(t, types.ProductionProjectionFailureBuildFailed, failed.FailureCode)
	require.Equal(t, types.ProductionProjectionFailureReasonBuildFailed, failed.FailureReason)

	target.Status = types.ReleaseTargetFailed
	target.FailureCode = types.ProductionProjectionFailureContentDigestMismatch
	target.FailureReason = types.ProductionProjectionFailureReasonContentDigestMismatch
	isProjection, err = svc.RecordProjectionKnowledgeFailure(
		context.Background(), knowledge.ID, types.TypeDocumentProcess,
	)
	require.NoError(t, err)
	require.True(t, isProjection)
	require.Equal(t, types.ProductionProjectionFailureContentDigestMismatch, target.FailureCode)
	require.Equal(t, types.ProductionProjectionFailureReasonContentDigestMismatch, target.FailureReason)
}

func TestProductionProjectionDeadLetterRejectsTamperedKnowledgeMetadata(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetBuilding
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID,
		KnowledgeBaseID: target.TargetKnowledgeBaseID, Type: types.KnowledgeTypeManual,
	}
	meta := types.NewManualKnowledgeMetadata("tampered", types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: projectionKnowledgeContentDigest("original"), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge}

	isProjection, err := svc.RecordProjectionKnowledgeFailure(
		context.Background(), knowledge.ID, types.TypeManualProcess,
	)

	require.ErrorIs(t, err, types.ErrProductionContentDigestMismatch)
	require.True(t, isProjection, "tampered projection envelopes must still suppress raw task errors")
	require.Equal(t, types.ReleaseTargetBuilding, target.Status)
}

func TestProductionFailureReconciliationConvergesAndIsIdempotent(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetBuilding
	target.RetentionDays = 30
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID,
		KnowledgeBaseID: target.TargetKnowledgeBaseID, Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusFailed,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: projectionKnowledgeContentDigest(content), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo}

	require.NoError(t, svc.ReconcileFailedProjectionKnowledge(productionReleaseContext(), 100))
	failed, err := repo.GetTarget(context.Background(), target.TenantID, target.ID)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetFailed, failed.Status)
	audit := svc.audit.(*productionReleaseAuditStub)
	require.Len(t, audit.entries, 1)
	require.Contains(t, string(audit.entries[0].Details), `"failure_stage":"recovery"`)

	require.NoError(t, svc.ReconcileFailedProjectionKnowledge(productionReleaseContext(), 100))
	require.Len(t, audit.entries, 1, "a terminal target must not emit duplicate failure audit")
	require.Equal(t, 2, repo.failureListCalls)
}

func TestProductionFailureReconciliationRetriesAfterRequiredAuditFailure(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	target.Status = types.ReleaseTargetBuilding
	target.RetentionDays = 30
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID,
		KnowledgeBaseID: target.TargetKnowledgeBaseID, Type: types.KnowledgeTypeManual,
		ParseStatus: types.ParseStatusFailed,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: projectionKnowledgeContentDigest(content), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo}
	audit := svc.audit.(*productionReleaseAuditStub)
	audit.err = errors.New("audit database unavailable")

	err := svc.ReconcileFailedProjectionKnowledge(productionReleaseContext(), 100)
	require.ErrorContains(t, err, "audit database unavailable")
	require.Equal(t, types.ReleaseTargetBuilding, repo.targets[target.ID].Status)
	require.NotNil(t, repo.targets[target.ID].RecoveryAttemptedAt)

	audit.err = nil
	require.NoError(t, svc.ReconcileFailedProjectionKnowledge(productionReleaseContext(), 100))
	require.Equal(t, types.ReleaseTargetFailed, repo.targets[target.ID].Status)
	require.Len(t, audit.entries, 1)
}

func TestProductionReleaseRetryClaimsFailedKnowledgeBeforeQueueAndReplaysGeneration(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	failedAt := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	target.CreatedAt = failedAt.Add(-time.Hour)
	target.UpdatedAt = failedAt
	target.Status = types.ReleaseTargetFailed
	target.FailedAt = &failedAt
	retention := failedAt.Add(30 * 24 * time.Hour)
	target.RetentionUntil = &retention
	target.FailureCode = types.ProductionProjectionFailureBuildFailed
	target.FailureReason = types.ProductionProjectionFailureReasonBuildFailed
	knowledge := productionReleaseProjectionKnowledge(t, target, types.ParseStatusFailed)
	knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo}
	queueErr := errors.New("queue response unavailable")
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task), err: queueErr}
	svc.tasks = tasks

	err := svc.Retry(productionReleaseContext(), target.ID)
	require.ErrorIs(t, err, queueErr)
	require.Equal(t, types.ParseStatusPending, knowledge.ParseStatus)
	require.Equal(t, types.ReleaseTargetBuilding, target.Status)
	require.True(t, target.UpdatedAt.After(failedAt))
	generation := target.UpdatedAt

	tasks.err = nil
	require.NoError(t, svc.Retry(productionReleaseContext(), target.ID))
	require.Equal(t, generation, target.UpdatedAt, "replay after queue failure must retain one claimed generation")
	require.Len(t, tasks.accepted, 1)
	for taskID := range tasks.accepted {
		require.Contains(t, taskID, fmt.Sprintf("build-retry-%d", generation.UnixNano()))
	}
}

func TestProductionFailureReconciliationSkipsNewerPendingRetryGeneration(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	target := repo.targets["target-new"]
	oldGeneration := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	target.Status = types.ReleaseTargetBuilding
	target.CreatedAt = oldGeneration.Add(-time.Hour)
	target.UpdatedAt = oldGeneration.Add(time.Second)
	candidate := *target
	candidate.UpdatedAt = oldGeneration
	repo.failureCandidates = []*types.ProductionReleaseTarget{&candidate}
	knowledge := productionReleaseProjectionKnowledge(t, target, types.ParseStatusPending)
	knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo}

	require.NoError(t, svc.ReconcileFailedProjectionKnowledge(productionReleaseContext(), 100))
	require.Equal(t, types.ReleaseTargetBuilding, target.Status)
	require.Empty(t, svc.audit.(*productionReleaseAuditStub).entries)
}

func TestProductionRetryAndRecoverySerializeBothLockOrdersWithoutLostReady(t *testing.T) {
	for _, first := range []string{"retry", "recovery"} {
		t.Run(first+"_locks_first", func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			target := repo.targets["target-new"]
			generation := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
			target.Status = types.ReleaseTargetBuilding
			target.CreatedAt = generation.Add(-time.Hour)
			target.UpdatedAt = generation
			candidate := *target
			repo.failureCandidates = []*types.ProductionReleaseTarget{&candidate}
			knowledge := productionReleaseProjectionKnowledge(t, target, types.ParseStatusFailed)
			knowledgeRepo := &productionReleaseKnowledgeRepoStub{knowledge: knowledge}
			svc.knowledge = productionReleaseKnowledgeStub{knowledge: knowledge, repo: knowledgeRepo}
			svc.tasks = &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}

			entered := make(chan string, 2)
			retryRelease := make(chan struct{})
			recoveryRelease := make(chan struct{})
			repo.onTargetLock = func(ctx context.Context) {
				actor, _ := types.UserIDFromContext(ctx)
				label := "retry"
				release := retryRelease
				if actor == types.ProductionSystemActorID {
					label = "recovery"
					release = recoveryRelease
				}
				entered <- label
				<-release
			}

			retryResult := make(chan error, 1)
			recoveryResult := make(chan error, 1)
			waitEntered := func() string {
				t.Helper()
				select {
				case label := <-entered:
					return label
				case <-time.After(time.Second):
					t.Fatal("retry/recovery did not enter the target lock boundary")
					return ""
				}
			}
			startRetry := func() { go func() { retryResult <- svc.Retry(productionReleaseContext(), target.ID) }() }
			startRecovery := func() {
				recoveryCtx := context.WithValue(productionReleaseContext(), types.UserIDContextKey, types.ProductionSystemActorID)
				go func() { recoveryResult <- svc.ReconcileFailedProjectionKnowledge(recoveryCtx, 100) }()
			}
			if first == "retry" {
				startRetry()
			} else {
				startRecovery()
			}
			require.Equal(t, first, waitEntered())
			if first == "retry" {
				startRecovery()
				close(retryRelease)
				require.NoError(t, <-retryResult)
				require.Equal(t, "recovery", waitEntered())
				close(recoveryRelease)
				require.NoError(t, <-recoveryResult)
			} else {
				startRetry()
				close(recoveryRelease)
				require.NoError(t, <-recoveryResult)
				require.Equal(t, "retry", waitEntered())
				close(retryRelease)
				require.NoError(t, <-retryResult)
			}

			require.Equal(t, types.ParseStatusPending, knowledge.ParseStatus)
			require.Equal(t, types.ReleaseTargetBuilding, target.Status)
			knowledgeRepo.mu.Lock()
			knowledge.ParseStatus = types.ParseStatusCompleted
			knowledgeRepo.mu.Unlock()
			require.NoError(t, markProductionProjectionReady(
				productionReleaseContext(), knowledgeRepo, repo, knowledge.ID,
			))
			require.Equal(t, types.ReleaseTargetReady, target.Status)
		})
	}
}

func productionReleaseProjectionKnowledge(
	t *testing.T,
	target *types.ProductionReleaseTarget,
	parseStatus string,
) *types.Knowledge {
	t.Helper()
	content := "# governed projection"
	knowledge := &types.Knowledge{
		ID: target.KnowledgeID, TenantID: target.TenantID,
		KnowledgeBaseID: target.TargetKnowledgeBaseID, Type: types.KnowledgeTypeManual,
		ParseStatus: parseStatus,
	}
	meta := types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1)
	meta.ProductionProjection = &types.ProductionProjectionMetadata{
		DocumentID: target.DocumentID, VersionID: target.VersionID, ReleaseTargetID: target.ID,
		ContentDigest: projectionKnowledgeContentDigest(content), SummaryModelID: "summary-1",
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}
	require.NoError(t, knowledge.SetManualMetadata(meta))
	return knowledge
}

func TestProductionReleaseRetryFailsClosedWithoutTaskQueue(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	repo.targets["target-new"].Status = types.ReleaseTargetFailed
	builder := &productionReleaseBuilderStub{}
	svc.builder = builder
	svc.tasks = nil

	require.Error(t, svc.Retry(productionReleaseContext(), "target-new"))
	require.Zero(t, builder.calls)
}

func TestProductionReleaseActivateRequiresReadyCompletedParseAndGraph(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	repo.targets["target-new"].Status = types.ReleaseTargetBuilding
	require.ErrorIs(t, svc.Activate(productionReleaseContext(), "target-new", 1), types.ErrProductionReleaseLifecycle)

	repo.targets["target-new"].Status = types.ReleaseTargetReady
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: &types.Knowledge{ID: "knowledge-new", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusFinalizing, PendingSubtasksCount: 1}}
	require.ErrorIs(t, svc.Activate(productionReleaseContext(), "target-new", 1), types.ErrProductionReleaseLifecycle)

	svc.knowledge = productionReleaseKnowledgeStub{knowledge: &types.Knowledge{ID: "knowledge-new", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}}
	svc.graph = productionReleaseGraphStub{err: errors.New("graph failed")}
	require.Error(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
}

func TestProductionReleaseRejectedActivationDoesNotLeaveDurableIntent(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	svc.tasks = tasks
	repo.targets["target-new"].Status = types.ReleaseTargetBuilding

	err := svc.Activate(productionReleaseContext(), "target-new", 1)

	require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
	require.Zero(t, tasks.calls)
	require.Empty(t, tasks.accepted)

	// A later worker completion cannot revive an intent the API rejected.
	repo.targets["target-new"].Status = types.ReleaseTargetReady
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Equal(t, 1, repo.lock)
}

func TestProductionReleaseActivateCASAndConcurrentActivation(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	require.ErrorIs(t, svc.Activate(productionReleaseContext(), "target-new", 99), types.ErrProductionProjectionConflict)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)

	svc, repo, _ = newProductionReleaseServiceFixture(t)
	repo.events = nil
	svc.wiki = productionReleaseWikiNoopStub{}
	svc.cleanup = productionReleaseCleanupNoopStub{}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() { <-start; errs <- svc.Activate(productionReleaseContext(), "target-new", 1) }()
	}
	close(start)
	err1, err2 := <-errs, <-errs
	require.True(t, (err1 == nil) != (err2 == nil) || (err1 == nil && err2 == nil))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
}

func TestProductionReleaseHeadSwitchPrecedesWikiAndCleanup(t *testing.T) {
	svc, _, events := newProductionReleaseServiceFixture(t)
	require.NoError(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Equal(t, []string{"projection.head_switched", "wiki.ingest:knowledge-new", "wiki.retract:knowledge-old", "cleanup:target-old"}, *events)
}

func TestProductionReleaseFollowupFailureRetriesWithoutHeadRollback(t *testing.T) {
	svc, repo, events := newProductionReleaseServiceFixture(t)
	svc.wiki = productionReleaseWikiStub{events: events, err: errors.New("queue unavailable")}
	require.Error(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)

	svc.wiki = productionReleaseWikiStub{events: events}
	require.NoError(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
}

func TestProductionReleaseActivationTaskIsDeterministicAndQueueScoped(t *testing.T) {
	svc, _, _ := newProductionReleaseServiceFixture(t)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	svc.tasks = tasks
	require.NoError(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.NoError(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Len(t, tasks.accepted, 1)
	for _, task := range tasks.accepted {
		require.Equal(t, types.TypeProductionActivate, task.Type())
		var payload types.ProductionProjectionTaskPayload
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		require.Equal(t, "target-new", payload.TargetID)
		require.Equal(t, 1, payload.ExpectedLock)
		require.Equal(t, types.ProductionProjectionOperationActivate, payload.Operation)
	}
}

func TestProductionWikiLifecycleQueueReportsEveryDurableEnqueueBoundary(t *testing.T) {
	target := &types.ProductionReleaseTarget{ID: "target-1", TenantID: 7, TargetKnowledgeBaseID: "kb-1", KnowledgeID: "knowledge-1"}
	pendingErr := errors.New("pending store unavailable")
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	queue := NewProductionWikiLifecycleQueue(tasks, &productionReleasePendingOpsStub{err: pendingErr})
	require.ErrorIs(t, queue.EnqueueIngest(productionReleaseContext(), target), pendingErr)
	require.Zero(t, tasks.calls)

	triggerErr := errors.New("task queue unavailable")
	pending := &productionReleasePendingOpsStub{}
	tasks.err = triggerErr
	queue = NewProductionWikiLifecycleQueue(tasks, pending)
	require.ErrorIs(t, queue.EnqueueRetract(productionReleaseContext(), target), triggerErr)
	require.Len(t, pending.ops, 1)
	require.Equal(t, WikiOpRetract, pending.ops[0].Op)
}

func TestProductionReleaseRollbackIsGuardedHeadSwitchToRetainedTarget(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	prepareProductionRollbackFixture(svc, repo)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	svc.tasks = tasks
	require.NoError(t, svc.Rollback(productionReleaseContext(), "target-old", 2))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Equal(t, types.ReleaseTargetRolledBack, repo.targets["target-new"].Status)
	require.Len(t, tasks.accepted, 1)
	for _, task := range tasks.accepted {
		var payload types.ProductionProjectionTaskPayload
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		require.Equal(t, types.ProductionProjectionOperationRollback, payload.Operation)
	}
}

func TestProductionCleanupSchedulerRetainsCleanupOperation(t *testing.T) {
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	scheduler := NewProductionCleanupTaskScheduler(tasks)
	retained := time.Now().Add(time.Hour)
	rolledBack := retained.Add(-time.Hour)
	target := &types.ProductionReleaseTarget{
		ID: "target-old", TenantID: 7, ProjectID: "project-1", KnowledgeID: "knowledge-old",
		Status: types.ReleaseTargetRolledBack, RolledBackAt: &rolledBack, RetentionUntil: &retained,
	}

	require.NoError(t, scheduler.EnqueueCleanup(productionReleaseContext(), target))
	require.Len(t, tasks.accepted, 1)
	for _, task := range tasks.accepted {
		var payload types.ProductionProjectionTaskPayload
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		require.Equal(t, types.ProductionProjectionOperationCleanup, payload.Operation)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(task.Payload(), &raw))
		require.NotEmpty(t, raw["cleanup_generation"])
	}
}

func TestProductionCleanupSchedulerUsesRetentionGeneration(t *testing.T) {
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	scheduler := NewProductionCleanupTaskScheduler(tasks)
	firstRollback := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	firstRetention := firstRollback.Add(time.Hour)
	target := &types.ProductionReleaseTarget{
		ID: "target-old", TenantID: 7, ProjectID: "project-1", KnowledgeID: "knowledge-old",
		Status: types.ReleaseTargetRolledBack, RolledBackAt: &firstRollback, RetentionUntil: &firstRetention,
	}
	require.NoError(t, scheduler.EnqueueCleanup(productionReleaseContext(), target))
	firstID, _ := onlyAcceptedProductionTask(t, tasks, "")

	secondRollback := firstRollback.Add(time.Minute)
	secondRetention := secondRollback.Add(time.Hour)
	target.RolledBackAt = &secondRollback
	target.RetentionUntil = &secondRetention
	require.NoError(t, scheduler.EnqueueCleanup(productionReleaseContext(), target))

	require.Len(t, tasks.accepted, 2)
	secondID, _ := onlyAcceptedProductionTask(t, tasks, firstID)
	require.NotEqual(t, firstID, secondID)
}

func TestProductionCleanupStaleTaskReconcilesCurrentRetentionGeneration(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	scheduler := NewProductionCleanupTaskScheduler(tasks)
	svc.cleanup = scheduler
	firstRollback := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	firstRetention := firstRollback.Add(time.Hour)
	target := repo.targets["target-old"]
	target.Status = types.ReleaseTargetRolledBack
	target.RolledBackAt = &firstRollback
	target.RetentionUntil = &firstRetention
	require.NoError(t, scheduler.EnqueueCleanup(productionReleaseContext(), target))
	firstID, firstTask := onlyAcceptedProductionTask(t, tasks, "")

	secondRollback := firstRollback.Add(2 * time.Minute)
	secondRetention := secondRollback.Add(time.Hour)
	target.Status = types.ReleaseTargetRolledBack
	target.RolledBackAt = &secondRollback
	target.RetentionUntil = &secondRetention
	chunks := &productionCleanupChunksStub{chunks: []*types.Chunk{{ID: "chunk-1", KnowledgeID: target.KnowledgeID, IsEnabled: true}}}
	indexes := &productionCleanupIndexStub{}
	cleanup := &ProductionProjectionCleanup{
		releases: repo, chunks: chunks, indexes: indexes, uow: productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{},
		now: func() time.Time { return firstRetention.Add(time.Second) },
	}
	handler := NewProductionProjectionTaskHandler(svc, cleanup)

	require.NoError(t, handler.Handle(context.Background(), firstTask), "obsolete cleanup must reconcile instead of retrying")
	require.Equal(t, types.ReleaseTargetRolledBack, target.Status)
	require.Zero(t, indexes.calls)
	require.Len(t, tasks.accepted, 2)
	_, secondTask := onlyAcceptedProductionTask(t, tasks, firstID)

	cleanup.now = func() time.Time { return secondRetention.Add(time.Second) }
	require.NoError(t, handler.Handle(context.Background(), secondTask))
	require.Equal(t, types.ReleaseTargetCleaned, target.Status)
	require.Equal(t, 1, indexes.calls)
}

func TestProductionReleaseRollbackReadinessFailurePrecedesMutation(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	retained := prepareProductionRollbackFixture(svc, repo)
	svc.graph = productionReleaseGraphStub{err: errors.New("graph not ready")}

	require.Error(t, svc.Rollback(productionReleaseContext(), "target-old", 2))
	require.Equal(t, types.ReleaseTargetRolledBack, repo.targets["target-old"].Status)
	require.Equal(t, retained, *repo.targets["target-old"].RetentionUntil)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
	require.Equal(t, 2, repo.lock)
	require.Zero(t, repo.transitionCalls)
}

func TestProductionReleaseRejectedRollbackDoesNotLeaveDurableIntent(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	retained := prepareProductionRollbackFixture(svc, repo)
	tasks := &productionReleaseTaskEnqueuerStub{accepted: make(map[string]*asynq.Task)}
	svc.tasks = tasks
	svc.graph = productionReleaseGraphStub{err: errors.New("graph not ready")}

	err := svc.Rollback(productionReleaseContext(), "target-old", 2)

	require.Error(t, err)
	require.Zero(t, tasks.calls)
	require.Empty(t, tasks.accepted)
	require.Equal(t, types.ReleaseTargetRolledBack, repo.targets["target-old"].Status)
	require.Equal(t, retained, *repo.targets["target-old"].RetentionUntil)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
	require.Equal(t, 2, repo.lock)
}

func TestProductionReleaseRollbackMutationAndAuditAreAtomic(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ProductionReleaseService, *productionReleaseRepoStub)
	}{
		{name: "first transition", configure: func(_ *ProductionReleaseService, repo *productionReleaseRepoStub) { repo.transitionErrAt = 1 }},
		{name: "second transition", configure: func(_ *ProductionReleaseService, repo *productionReleaseRepoStub) { repo.transitionErrAt = 2 }},
		{name: "head CAS", configure: func(_ *ProductionReleaseService, repo *productionReleaseRepoStub) {
			repo.switchErr = errors.New("head unavailable")
		}},
		{name: "audit", configure: func(svc *ProductionReleaseService, _ *productionReleaseRepoStub) {
			svc.audit = &productionReleaseAuditStub{err: errors.New("audit unavailable")}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, events := newProductionReleaseServiceFixture(t)
			retained := prepareProductionRollbackFixture(svc, repo)
			tc.configure(svc, repo)

			require.Error(t, svc.Rollback(productionReleaseContext(), "target-old", 2))
			require.Equal(t, types.ReleaseTargetRolledBack, repo.targets["target-old"].Status)
			require.Equal(t, retained, *repo.targets["target-old"].RetentionUntil)
			require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
			require.Equal(t, 2, repo.lock)
			require.Empty(t, *events)
		})
	}
}

func TestProductionReleaseRollbackReplayRetainsIntentAcrossCrashWindow(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	prepareProductionRollbackFixture(svc, repo)
	audit := svc.audit.(*productionReleaseAuditStub)
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
		Operation: types.ProductionProjectionOperationRollback,
		TenantID:  7, ProjectID: "project-1", TargetID: "target-old",
		ActorUserID: "00000000-0000-4000-8000-000000000007", ExpectedLock: 2,
	})
	require.NoError(t, err)
	task := asynq.NewTask(types.TypeProductionActivate, payload)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

	require.NoError(t, handler.Handle(context.Background(), task), "durable replay must close a crash-before-CAS window")
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionProjectionRolledBack, audit.entries[0].Action)
	require.NoError(t, handler.Handle(context.Background(), task), "post-commit replay must be idempotent")
	require.Len(t, audit.entries, 1)
}

func TestProductionProjectionDelayedActivationReauthorizesRevokedPublisher(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	svc.authorizer = productionReleaseAuthorizerStub{err: types.ErrProductionForbidden}
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
		Operation: types.ProductionProjectionOperationActivate,
		TenantID:  7, ProjectID: "project-1", TargetID: "target-new",
		ActorUserID: "00000000-0000-4000-8000-000000000007", ExpectedLock: 1,
	})
	require.NoError(t, err)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

	err = handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionActivate, payload))
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Equal(t, 1, repo.lock)
	require.Equal(t, types.ReleaseTargetReady, repo.targets["target-new"].Status)
}

func TestProductionProjectionHeadMutationWorkerSkipsRetryForPermanentRejections(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(*ProductionReleaseService, *productionReleaseRepoStub)
		expectedErr  error
		expectedLock int
	}{
		{
			name: "lifecycle",
			configure: func(_ *ProductionReleaseService, repo *productionReleaseRepoStub) {
				repo.targets["target-new"].Status = types.ReleaseTargetBuilding
			},
			expectedErr:  types.ErrProductionReleaseLifecycle,
			expectedLock: 1,
		},
		{
			name:         "stale lock",
			configure:    func(_ *ProductionReleaseService, _ *productionReleaseRepoStub) {},
			expectedErr:  types.ErrProductionProjectionConflict,
			expectedLock: 99,
		},
		{
			name: "authorization",
			configure: func(svc *ProductionReleaseService, _ *productionReleaseRepoStub) {
				svc.authorizer = productionReleaseAuthorizerStub{err: types.ErrProductionForbidden}
			},
			expectedErr:  types.ErrProductionForbidden,
			expectedLock: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			tc.configure(svc, repo)
			payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
				Operation: types.ProductionProjectionOperationActivate,
				TenantID:  7, ProjectID: "project-1", TargetID: "target-new",
				ActorUserID: "00000000-0000-4000-8000-000000000007", ExpectedLock: tc.expectedLock,
			})
			require.NoError(t, err)
			handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

			err = handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionActivate, payload))

			require.ErrorIs(t, err, tc.expectedErr)
			require.ErrorIs(t, err, asynq.SkipRetry)
		})
	}
}

func TestProductionProjectionDelayedHeadMutationRejectsMissingActor(t *testing.T) {
	tests := []struct {
		name      string
		operation types.ProductionProjectionOperation
		targetID  string
		lock      int
		prepare   func(*ProductionReleaseService, *productionReleaseRepoStub)
	}{
		{name: "activate", operation: types.ProductionProjectionOperationActivate, targetID: "target-new", lock: 1},
		{name: "rollback", operation: types.ProductionProjectionOperationRollback, targetID: "target-old", lock: 2,
			prepare: func(svc *ProductionReleaseService, repo *productionReleaseRepoStub) {
				prepareProductionRollbackFixture(svc, repo)
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newProductionReleaseServiceFixture(t)
			if tc.prepare != nil {
				tc.prepare(svc, repo)
			}
			payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
				Operation: tc.operation, TenantID: 7, ProjectID: "project-1", TargetID: tc.targetID, ExpectedLock: tc.lock,
			})
			require.NoError(t, err)
			handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

			err = handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionActivate, payload))
			require.ErrorIs(t, err, types.ErrProductionForbidden)
			require.Equal(t, tc.lock, repo.lock)
		})
	}
}

func TestProductionProjectionPostCommitReplayAllowsSystemFollowups(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	repo.targets["target-new"].Status = types.ReleaseTargetActive
	repo.targets["target-old"].Status = types.ReleaseTargetRolledBack
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
		Operation: types.ProductionProjectionOperationActivate,
		TenantID:  7, ProjectID: "project-1", TargetID: "target-new", ExpectedLock: 2,
	})
	require.NoError(t, err)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

	require.NoError(t, handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionActivate, payload)))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-new"].Status)
}

func TestProductionReleaseConcurrentRollbackIsStable(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	prepareProductionRollbackFixture(svc, repo)
	svc.wiki = productionReleaseWikiNoopStub{}
	svc.cleanup = productionReleaseCleanupNoopStub{}
	audit := svc.audit.(*productionReleaseAuditStub)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- svc.Rollback(productionReleaseContext(), "target-old", 2)
		}()
	}
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Equal(t, 3, repo.lock)
	require.Len(t, audit.entries, 1)
}

func TestProductionProjectionTaskRejectsOperationTypeMismatch(t *testing.T) {
	svc, _, _ := newProductionReleaseServiceFixture(t)
	payload, err := json.Marshal(types.ProductionProjectionTaskPayload{
		Operation: types.ProductionProjectionOperationActivate,
		TenantID:  7, ProjectID: "project-1", TargetID: "target-new", ExpectedLock: 1,
	})
	require.NoError(t, err)
	handler := NewProductionProjectionTaskHandler(svc, &ProductionProjectionCleanup{})

	err = handler.Handle(context.Background(), asynq.NewTask(types.TypeProductionBuild, payload))
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
}

func TestProductionReleaseAuditFailurePreventsHeadSwitch(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	svc.audit = &productionReleaseAuditStub{err: errors.New("audit unavailable")}
	require.Error(t, svc.Activate(productionReleaseContext(), "target-new", 1))
	require.Equal(t, types.ReleaseTargetActive, repo.targets["target-old"].Status)
	require.Equal(t, types.ReleaseTargetReady, repo.targets["target-new"].Status)
}

func prepareProductionRollbackFixture(svc *ProductionReleaseService, repo *productionReleaseRepoStub) time.Time {
	rolledBack := time.Now()
	retained := rolledBack.Add(time.Hour)
	repo.targets["target-old"].Status = types.ReleaseTargetRolledBack
	repo.targets["target-old"].RolledBackAt = &rolledBack
	repo.targets["target-old"].RetentionUntil = &retained
	repo.targets["target-new"].Status = types.ReleaseTargetActive
	repo.history = []*types.ProductionReleaseTarget{repo.targets["target-new"], repo.targets["target-old"]}
	repo.lock = 2
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: &types.Knowledge{ID: "knowledge-old", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}}
	return retained
}

func retryConcurrently(t *testing.T, svc *ProductionReleaseService, callers int) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			errs <- svc.Retry(productionReleaseContext(), "target-new")
		}()
	}
	close(start)
	for range callers {
		require.NoError(t, <-errs)
	}
}

func onlyAcceptedProductionTask(t *testing.T, tasks *productionReleaseTaskEnqueuerStub, exceptID string) (string, *asynq.Task) {
	t.Helper()
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	for id, task := range tasks.accepted {
		if id != exceptID {
			return id, task
		}
	}
	t.Fatal("accepted production task not found")
	return "", nil
}

func productionStringPtr(value string) *string     { return &value }
func productionTimePtr(value time.Time) *time.Time { return &value }
