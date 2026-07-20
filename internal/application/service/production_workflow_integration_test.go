package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	workflowE2EProjectID    = "76000000-0000-4000-8000-000000000001"
	workflowE2ETypeID       = "76000000-0000-4000-8000-000000000002"
	workflowE2ESourceSetID  = "76000000-0000-4000-8000-000000000003"
	workflowE2ESourceItemID = "76000000-0000-4000-8000-000000000004"
	workflowE2EMCPServiceID = "76000000-0000-4000-8000-000000000005"
	workflowE2EModelID      = "76000000-0000-4000-8000-000000000006"
	workflowE2EActorID      = "76000000-0000-4000-8000-000000000007"
	workflowE2ESecret       = "Bearer workflow-e2e-secret"
)

type productionWorkflowModelRepository struct {
	interfaces.ModelRepository
	model *types.Model
}

func (r *productionWorkflowModelRepository) GetByID(_ context.Context, tenantID uint64, id string) (*types.Model, error) {
	if r.model == nil || r.model.ID != id || r.model.TenantID != tenantID {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *r.model
	return &copy, nil
}

type productionWorkflowReplayExecutor struct {
	base        *ProductionExecutor
	mcp         *ProductionMCPAdapter
	replayCalls int
}

func (e *productionWorkflowReplayExecutor) ExecuteStep(
	ctx context.Context,
	run *types.ProductionRun,
	calls []*types.ProductionToolCall,
) (ProductionStepResult, error) {
	result, err := e.base.ExecuteStep(ctx, run, calls)
	if err != nil || result.ToolCallResult == nil {
		return result, err
	}
	if len(calls) != 1 {
		return ProductionStepResult{}, errors.New("workflow replay requires one persisted call")
	}
	replayed, replayErr := e.mcp.Execute(ctx, calls[0])
	if replayErr != nil {
		return ProductionStepResult{}, replayErr
	}
	if replayed.ToolCallID != result.ToolCallResult.ToolCallID || replayed.ResponseDigest != productionToolDigest(result.ToolCallResult.ResponseSnapshot) {
		return ProductionStepResult{}, fmt.Errorf("workflow replay did not match first execution")
	}
	e.replayCalls++
	return result, nil
}

func openProductionWorkflowE2EDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "production-workflow-e2e.db")
	db, err := gorm.Open(sqlite.Open(
		"file:"+path+"?_foreign_keys=1&_busy_timeout=10000&_journal_mode=WAL",
	), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationRoot := filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite")
	for _, name := range []string{
		"000001_knowledge_production_foundation.up.sql",
		"000002_knowledge_production_documents.up.sql",
		"000003_knowledge_production_runs.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationRoot, name))
		require.NoError(t, readErr)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	return db
}

func productionWorkflowUserContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, workflowE2EActorID)
	return context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
}

func productionWorkflowWorkerContext(t *testing.T, run *types.ProductionRun) context.Context {
	t.Helper()
	ctx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: run.TenantID, ProjectID: run.ProjectID, RunID: run.ID,
	})
	require.NoError(t, err)
	return ctx
}

func TestProductionWorkflowSQLiteApprovalReplayAndGovernedWrite(t *testing.T) {
	db := openProductionWorkflowE2EDB(t)
	runs := repository.NewProductionRunRepository(db)
	sources := repository.NewProductionSourceRepository(db)
	documents := repository.NewProductionDocumentRepository(db)
	documentTypes := repository.NewProductionDocumentTypeRepository(db)
	uow := repository.NewProductionUnitOfWork(db)
	audit := NewAuditLogService(repository.NewAuditLogRepository(db))
	authorizer := &productionDocumentAuthorizerStub{}

	skillBindings := types.JSON(`{"version":1,"skills":[]}`)
	workflow, err := canonicalProductionWorkflowPlan(types.JSON(`{"version":1,"steps":[{"provider_type":"mcp","provider_id":"`+workflowE2EMCPServiceID+`","tool_name":"lookup","request":{"source_item_id":"`+workflowE2ESourceItemID+`","arguments":{"query":"status"}}}]}`), skillBindings)
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.ProductionProject{
		ID: workflowE2EProjectID, TenantID: 7, Name: "Workflow E2E", OwnerUserID: workflowE2EActorID,
		Status: types.ProductionProjectActive,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionDocumentType{
		ID: workflowE2ETypeID, TenantID: 7, Code: "software-development-baseline", Name: "Baseline",
		SchemaVersion: 3, BlockSchema: types.JSON(`{}`), SourceRequirements: types.JSON(`{}`),
		SkillBindings: skillBindings, WorkflowPlan: workflow, QualityRules: types.JSON(`{}`),
		ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
		Status: types.ProductionDocumentTypeActive, CreatedBy: workflowE2EActorID,
	}).Error)
	frozenAt := time.Now().UTC()
	require.NoError(t, db.Create(&types.ProductionSourceSet{
		ID: workflowE2ESourceSetID, TenantID: 7, ProjectID: workflowE2EProjectID,
		DocumentTypeID: workflowE2ETypeID, Status: types.ProductionSourceSetCollecting,
		CreatedBy: workflowE2EActorID,
	}).Error)
	require.NoError(t, db.Create(&types.ProductionSourceItem{
		ID: workflowE2ESourceItemID, SourceSetID: workflowE2ESourceSetID,
		SourceKind: types.ProductionSourceKindMCP, ExternalID: workflowE2EMCPServiceID,
		Title: "MCP status", MimeType: "application/json", ContentDigest: strings.Repeat("a", 64),
		CapturedAt: frozenAt, Metadata: types.JSON(`{}`), Status: types.ProductionSourceItemAccepted,
	}).Error)
	require.NoError(t, db.Model(&types.ProductionSourceSet{}).Where("id = ?", workflowE2ESourceSetID).
		Updates(map[string]any{"status": types.ProductionSourceSetFrozen, "frozen_at": frozenAt}).Error)

	documentService := NewProductionDocumentService(
		documents, sources, documentTypes, authorizer, nil, audit, uow,
	)
	document, err := documentService.CreateDocument(productionWorkflowUserContext(), interfaces.CreateProductionDocumentInput{
		ProjectID: workflowE2EProjectID, DocumentTypeID: workflowE2ETypeID,
		SourceSetID: workflowE2ESourceSetID, Title: "Workflow report",
	})
	require.NoError(t, err)
	require.NotNil(t, document.CurrentVersionID)

	approvalPolicy := &productionMCPApprovalStub{required: true}
	mcpService := &types.MCPService{
		ID: workflowE2EMCPServiceID, TenantID: 7, Name: "workflow-mcp", Enabled: true,
		TransportType: types.MCPTransportHTTPStreamable,
		Headers:       types.MCPHeaders{"Authorization": workflowE2ESecret},
	}
	client := &productionMCPClientStub{
		serviceID: workflowE2EMCPServiceID,
		result:    &mcp.CallToolResult{Content: []mcp.ContentItem{{Type: "text", Text: "healthy"}}},
	}
	manager := &productionMCPManagerStub{client: client}
	sourceService := NewProductionSourceService(sources, authorizer, nil, audit, uow)
	mcpAdapter := NewProductionMCPAdapter(
		approvalPolicy, &productionMCPServiceStub{service: mcpService}, manager, runs, sources, sourceService,
	)
	chatModel := &productionWriterChatStub{}
	writer := NewProductionWriter(
		&productionWriterModelServiceStub{model: chatModel}, runs, sources, documents, nil, documentService,
	)
	newExecutor := func() *productionWorkflowReplayExecutor {
		base := newProductionStepExecutor(
			runs, &productionExecutorAdapterStub{}, mcpAdapter, &productionExecutorAdapterStub{}, writer,
		)
		return &productionWorkflowReplayExecutor{base: base, mcp: mcpAdapter}
	}
	enqueuer := newProductionTaskEnqueuerFake()
	firstExecutor := newExecutor()
	firstOrchestrator := NewProductionOrchestrator(runs, uow, firstExecutor, enqueuer)
	modelRepo := &productionWorkflowModelRepository{model: &types.Model{
		ID: workflowE2EModelID, TenantID: 7, Name: "writer", Type: types.ModelTypeKnowledgeQA,
		Status: types.ModelStatusActive,
	}}
	startService := NewProductionRunService(
		runs, documents, sources, documentTypes, modelRepo, authorizer, audit, uow, firstOrchestrator,
	)

	run, err := startService.StartDocumentRun(productionWorkflowUserContext(), document.ID, interfaces.StartProductionDocumentRunInput{
		RunType: types.ProductionRunWrite, ModelID: workflowE2EModelID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, enqueuer.callCount())
	payload := types.ProductionRunPayload{TenantID: run.TenantID, RunID: run.ID, Attempt: run.Attempt}
	require.NoError(t, firstOrchestrator.HandleRun(productionWorkflowWorkerContext(t, run), payload))

	waiting, err := runs.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionRunWaitingApproval, waiting.Status)
	calls, err := runs.ListToolCalls(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, types.ProductionToolCallPendingApproval, calls[0].Status)
	require.Zero(t, manager.calls)
	require.Zero(t, client.calls)
	require.NotContains(t, string(calls[0].RequestSnapshot), workflowE2ESecret)

	secondExecutor := newExecutor()
	secondOrchestrator := NewProductionOrchestrator(runs, uow, secondExecutor, enqueuer)
	approvalService := NewProductionRunService(
		runs, documents, sources, documentTypes, modelRepo, authorizer, audit, uow, secondOrchestrator,
	)
	approved, err := approvalService.DecideToolCall(
		productionWorkflowUserContext(), calls[0].ID, interfaces.ProductionToolDecisionApprove,
	)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallApproved, approved.Status)
	require.Equal(t, 2, enqueuer.callCount())

	_, err = approvalService.DecideToolCall(
		productionWorkflowUserContext(), calls[0].ID, interfaces.ProductionToolDecisionApprove,
	)
	require.ErrorIs(t, err, types.ErrProductionConflict)
	require.Equal(t, 2, enqueuer.callCount(), "duplicate approval must not enqueue")

	queued, err := runs.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.NoError(t, secondOrchestrator.HandleRun(productionWorkflowWorkerContext(t, queued), payload))
	require.Equal(t, 1, client.calls)
	require.Equal(t, 1, manager.calls)
	require.Equal(t, 1, secondExecutor.replayCalls)
	require.Equal(t, 3, enqueuer.callCount())

	completedCall, err := runs.GetToolCall(context.Background(), 7, calls[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionToolCallCompleted, completedCall.Status)
	require.NotNil(t, completedCall.ResponseEvidenceID)
	evidence, item, set, err := sources.GetEvidence(context.Background(), 7, *completedCall.ResponseEvidenceID)
	require.NoError(t, err)
	require.Equal(t, workflowE2ESourceItemID, item.ID)
	require.Equal(t, types.ProductionSourceItemAccepted, item.Status)
	require.Equal(t, types.ProductionSourceSetFrozen, set.Status)
	require.Equal(t, run.ID, evidence.CapturedByRunID)
	require.NotContains(t, string(evidence.InlineContent), workflowE2ESecret)

	queuedForWrite, err := runs.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, queuedForWrite.CurrentStep)
	chatModel.response = &types.ChatResponse{Content: productionWriterOutput(t,
		writerFact("workflow-fact", "MCP reports healthy", []string{evidence.ID}, false),
	)}
	mismatchCtx, err := types.WithProductionInternalPrincipal(context.Background(), types.ProductionInternalPrincipal{
		ActorID: types.ProductionSystemActorID, ActorKind: types.ProductionInternalActorWorker,
		TenantID: 7, ProjectID: workflowE2EProjectID, RunID: uuid.NewString(),
	})
	require.NoError(t, err)
	mismatchRun := *queuedForWrite
	mismatchRun.Status = types.ProductionRunRunning
	_, err = writer.Write(mismatchCtx, &mismatchRun)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Zero(t, chatModel.calls)

	thirdExecutor := newExecutor()
	thirdOrchestrator := NewProductionOrchestrator(runs, uow, thirdExecutor, enqueuer)
	require.NoError(t, thirdOrchestrator.HandleRun(productionWorkflowWorkerContext(t, queuedForWrite), payload))
	finalRun, err := runs.Get(context.Background(), 7, run.ID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionRunCompleted, finalRun.Status)
	require.NotNil(t, finalRun.OutputVersionID)
	require.Equal(t, 3, enqueuer.callCount())
	require.Equal(t, 1, chatModel.calls)
	version, err := documents.GetVersion(context.Background(), 7, *finalRun.OutputVersionID)
	require.NoError(t, err)
	require.Equal(t, 2, version.VersionNumber)
	require.Equal(t, types.ProductionDocumentOriginAI, version.Origin)
	require.NotEmpty(t, version.Blocks)

	var audits []*types.AuditLog
	require.NoError(t, db.Order("id ASC").Find(&audits).Error)
	approvedAudits := 0
	for _, entry := range audits {
		if entry.Action == types.AuditActionProductionToolCallApproved {
			approvedAudits++
		}
	}
	require.Equal(t, 1, approvedAudits)

	var durable struct {
		Runs     []*types.ProductionRun
		Calls    []*types.ProductionToolCall
		Evidence []*types.ProductionEvidenceSnapshot
		Audits   []*types.AuditLog
	}
	require.NoError(t, db.Find(&durable.Runs).Error)
	require.NoError(t, db.Find(&durable.Calls).Error)
	require.NoError(t, db.Find(&durable.Evidence).Error)
	durable.Audits = audits
	encoded, err := json.Marshal(durable)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), workflowE2ESecret)
	require.NotContains(t, strings.ToLower(string(encoded)), "authorization")
}
