package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type productionReleaseRepoStub struct {
	interfaces.ProductionReleaseRepository
	mu              sync.Mutex
	txMu            sync.Mutex
	release         *types.ProductionRelease
	targets         map[string]*types.ProductionReleaseTarget
	history         []*types.ProductionReleaseTarget
	lock            int
	events          *[]string
	transitionCalls int
	transitionErrAt int
	transitionErr   error
	switchErr       error
}

func (r *productionReleaseRepoStub) CreateRelease(_ context.Context, release *types.ProductionRelease, targets []*types.ProductionReleaseTarget) error {
	r.release = release
	r.release.Targets = targets
	return nil
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
	now := time.Now()
	out := make([]*types.ProductionReleaseTarget, 0, limit)
	for _, target := range r.targets {
		if len(out) == limit {
			break
		}
		if (target.Status == types.ReleaseTargetRolledBack || target.Status == types.ReleaseTargetFailed) &&
			target.RetentionUntil != nil && !target.RetentionUntil.After(now) {
			copy := *target
			out = append(out, &copy)
		}
	}
	return out, nil
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
			retained := time.Now().Add(time.Hour)
			target.RetentionUntil = &retained
		}
	}
	target := r.targets[targetID]
	if target == nil || target.Status != types.ReleaseTargetReady {
		return nil, types.ErrProductionReleaseLifecycle
	}
	target.Status = types.ReleaseTargetActive
	target.RetentionUntil = nil
	r.lock++
	if r.events != nil {
		*r.events = append(*r.events, "projection.head_switched")
	}
	return &types.ProductionProjectionHead{TenantID: target.TenantID, DocumentID: target.DocumentID,
		TargetKnowledgeBaseID: target.TargetKnowledgeBaseID, ActiveReleaseTargetID: targetID, LockVersion: r.lock}, nil
}

func (r *productionReleaseRepoStub) TransitionTarget(_ context.Context, id string, from, to types.ProductionReleaseTargetStatus, _ types.JSONMap) (bool, error) {
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

func (s *productionReleaseReviewsStub) GetApprovedReviewForVersion(context.Context, uint64, string, string) (*types.ProductionReviewRequest, error) {
	return s.review, nil
}

type productionReleaseAuthorizerStub struct{ err error }

func (s productionReleaseAuthorizerStub) RequireProjectRole(context.Context, string, ...types.ProductionRole) error {
	return s.err
}

type productionReleaseMembershipStub struct {
	interfaces.TenantMemberService
	role types.TenantRole
}

func (s productionReleaseMembershipStub) GetMembership(context.Context, string, uint64) (*types.TenantMember, error) {
	return &types.TenantMember{Role: s.role, Status: types.TenantMemberStatusActive}, nil
}

type productionReleaseKBStub struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s productionReleaseKBStub) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	copy := *s.kb
	return &copy, nil
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
}

func (s productionReleaseKnowledgeStub) GetKnowledgeByID(context.Context, string) (*types.Knowledge, error) {
	copy := *s.knowledge
	return &copy, nil
}

type productionReleaseGraphStub struct{ err error }

func (s productionReleaseGraphStub) RequireSuccessfulGraphSubtasks(context.Context, *types.ProductionReleaseTarget, *types.Knowledge) error {
	return s.err
}

type productionReleaseBuilderStub struct {
	err   error
	calls int
}

func (s *productionReleaseBuilderStub) Build(context.Context, string) (*types.Knowledge, error) {
	s.calls++
	return nil, s.err
}

type productionReleaseFileServiceStub struct {
	interfaces.FileService
	err error
}

func (s productionReleaseFileServiceStub) CheckConnectivity(context.Context) error { return s.err }

type productionReleaseStorageResolverStub struct {
	interfaces.StorageBackendResolver
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
	s.resolvedID = backendID
	s.resolvedProvider = provider
	if tenant != nil {
		s.resolvedTenant = tenant.ID
	}
	return s.backend, s.resolveErr
}

func (s *productionReleaseStorageResolverStub) ResolveFileService(_ context.Context, tenant *types.Tenant, backendID, provider, _ string) (interfaces.FileService, string, error) {
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
		VersionID: "version-2", TargetKnowledgeBaseID: "kb-1", KnowledgeID: "knowledge-old", Status: types.ReleaseTargetActive}
	ready := &types.ProductionReleaseTarget{ID: "target-new", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1",
		VersionID: "version-3", TargetKnowledgeBaseID: "kb-1", KnowledgeID: "knowledge-new", Status: types.ReleaseTargetReady,
		ConfigSnapshot: types.JSON(`{"version":1,"indexing_strategy":{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":true,"graph_enabled":true},"chunking":{"strategy":"recursive","chunk_size":256},"embedding_model_id":"embedding-1","summary_model_id":"summary-1","graph":{"enabled":true,"model_id":"summary-1","extract_config":{"enabled":true}}}`)}
	repo := &productionReleaseRepoStub{targets: map[string]*types.ProductionReleaseTarget{old.ID: old, ready.ID: ready}, history: []*types.ProductionReleaseTarget{ready, old}, lock: 1, events: &events}
	storage := &productionReleaseStorageResolverStub{backend: &types.StorageBackend{
		ID: "storage-1", TenantID: 7, Provider: "local", Status: types.StorageBackendStatusActive,
	}}
	service := &ProductionReleaseService{
		releases: repo,
		documents: &productionReleaseDocumentsStub{document: &types.ProductionDocument{ID: "document-1", TenantID: 7, ProjectID: "project-1", LatestApprovedVersionID: productionStringPtr("version-3")},
			version: &types.ProductionDocumentVersion{ID: "version-3", DocumentID: "document-1", TenantID: 7, ProjectID: "project-1", FrozenAt: productionTimePtr(time.Now())}},
		reviews:    &productionReleaseReviewsStub{review: &types.ProductionReviewRequest{ID: "review-1", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1", VersionID: "version-3", Status: types.ProductionReviewApproved}},
		authorizer: productionReleaseAuthorizerStub{}, members: productionReleaseMembershipStub{role: types.TenantRoleContributor}, models: productionReleaseModelStub{}, storage: storage,
		kbs:       productionReleaseKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7, CreatorID: "00000000-0000-4000-8000-000000000007", Type: types.KnowledgeBaseTypeDocument}},
		knowledge: productionReleaseKnowledgeStub{knowledge: &types.Knowledge{ID: "knowledge-new", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}},
		graph:     productionReleaseGraphStub{}, wiki: productionReleaseWikiStub{events: &events}, cleanup: productionReleaseCleanupStub{events: &events},
		uow: productionReleaseUOWStub{repo: repo}, audit: &productionReleaseAuditStub{}, now: time.Now,
	}
	return service, repo, &events
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

func TestProductionReleasePrepareSnapshotsImmutableTargetProcessingConfig(t *testing.T) {
	svc, repo, _ := newProductionReleaseServiceFixture(t)
	version := &types.ProductionDocumentVersion{ID: "version-3", DocumentID: "document-1", TenantID: 7, ProjectID: "project-1", FrozenAt: productionTimePtr(time.Now())}
	review := &types.ProductionReviewRequest{ID: "review-1", TenantID: 7, ProjectID: "project-1", DocumentID: "document-1", VersionID: "version-3", Status: types.ProductionReviewApproved}
	svc.documents = &productionReleaseDocumentsStub{document: &types.ProductionDocument{ID: "document-1", TenantID: 7, ProjectID: "project-1", LatestApprovedVersionID: productionStringPtr("version-3")}, version: version}
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
	retained := time.Now().Add(time.Hour)
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
	target := &types.ProductionReleaseTarget{
		ID: "target-old", TenantID: 7, ProjectID: "project-1", KnowledgeID: "knowledge-old", RetentionUntil: &retained,
	}

	require.NoError(t, scheduler.EnqueueCleanup(productionReleaseContext(), target))
	require.Len(t, tasks.accepted, 1)
	for _, task := range tasks.accepted {
		var payload types.ProductionProjectionTaskPayload
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		require.Equal(t, types.ProductionProjectionOperationCleanup, payload.Operation)
	}
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
	retained := time.Now().Add(time.Hour)
	repo.targets["target-old"].Status = types.ReleaseTargetRolledBack
	repo.targets["target-old"].RetentionUntil = &retained
	repo.targets["target-new"].Status = types.ReleaseTargetActive
	repo.history = []*types.ProductionReleaseTarget{repo.targets["target-new"], repo.targets["target-old"]}
	repo.lock = 2
	svc.knowledge = productionReleaseKnowledgeStub{knowledge: &types.Knowledge{ID: "knowledge-old", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusCompleted}}
	return retained
}

func productionStringPtr(value string) *string     { return &value }
func productionTimePtr(value time.Time) *time.Time { return &value }
