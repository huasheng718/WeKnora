package service

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type productionRunService struct {
	runs          interfaces.ProductionRunRepository
	documents     interfaces.ProductionDocumentRepository
	sources       interfaces.ProductionSourceRepository
	documentTypes interfaces.ProductionDocumentTypeRepository
	models        interfaces.ModelRepository
	projects      interfaces.ProductionProjectAuthorizer
	audit         interfaces.AuditLogService
	uow           interfaces.ProductionUnitOfWork
	resumer       interfaces.ProductionRunResumer
	now           func() time.Time
}

func NewProductionRunService(
	runs interfaces.ProductionRunRepository,
	documents interfaces.ProductionDocumentRepository,
	sources interfaces.ProductionSourceRepository,
	documentTypes interfaces.ProductionDocumentTypeRepository,
	models interfaces.ModelRepository,
	projects interfaces.ProductionProjectAuthorizer,
	audit interfaces.AuditLogService,
	uow interfaces.ProductionUnitOfWork,
	resumer interfaces.ProductionRunResumer,
) *productionRunService {
	return &productionRunService{
		runs: runs, documents: documents, sources: sources, documentTypes: documentTypes,
		models: models, projects: projects, audit: audit, uow: uow, resumer: resumer, now: time.Now,
	}
}

func (s *productionRunService) StartDocumentRun(
	ctx context.Context,
	documentID string,
	input interfaces.StartProductionDocumentRunInput,
) (*types.ProductionRun, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !canonicalProductionUUID(documentID) || !canonicalProductionUUID(input.ModelID) ||
		(input.RunType != types.ProductionRunWrite && input.RunType != types.ProductionRunRewrite && input.RunType != types.ProductionRunValidate) {
		return nil, errors.New("invalid production document run input")
	}
	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	if document == nil || document.ID != documentID || document.TenantID != tenantID || document.CurrentVersionID == nil {
		return nil, errProductionToolScope
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, document.ProjectID); err != nil {
		return nil, err
	}
	version, err := s.documents.GetVersion(ctx, tenantID, *document.CurrentVersionID)
	if err != nil {
		return nil, err
	}
	if version == nil || version.DocumentID != document.ID || version.TenantID != tenantID ||
		version.ProjectID != document.ProjectID || !canonicalProductionUUID(version.SourceSetID) {
		return nil, errProductionToolScope
	}
	sourceSet, documentType, err := s.loadRunDefinition(ctx, tenantID, document, version.SourceSetID, input.ModelID)
	if err != nil {
		return nil, err
	}
	if sourceSet.Status != types.ProductionSourceSetFrozen {
		return nil, types.ErrProductionDocumentSourceSetInvalid
	}
	return s.persistAndResume(ctx, newProductionRun(
		tenantID, document, sourceSet, documentType, input.ModelID, input.RunType, document.CurrentVersionID,
	))
}

func (s *productionRunService) StartSourceSetCollection(
	ctx context.Context,
	sourceSetID string,
	input interfaces.StartProductionSourceSetCollectionInput,
) (*types.ProductionRun, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !canonicalProductionUUID(sourceSetID) || !canonicalProductionUUID(input.DocumentID) || !canonicalProductionUUID(input.ModelID) {
		return nil, errors.New("invalid production collection run input")
	}
	document, err := s.documents.GetDocument(ctx, tenantID, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if document == nil || document.TenantID != tenantID || document.ID != input.DocumentID {
		return nil, errProductionToolScope
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, document.ProjectID); err != nil {
		return nil, err
	}
	sourceSet, documentType, err := s.loadRunDefinition(ctx, tenantID, document, sourceSetID, input.ModelID)
	if err != nil {
		return nil, err
	}
	if sourceSet.Status == types.ProductionSourceSetFailed {
		return nil, types.ErrProductionConflict
	}
	return s.persistAndResume(ctx, newProductionRun(
		tenantID, document, sourceSet, documentType, input.ModelID, types.ProductionRunCollect, nil,
	))
}

func (s *productionRunService) GetRun(ctx context.Context, runID string) (*types.ProductionRun, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !canonicalProductionUUID(runID) {
		return nil, errors.New("run id must be a canonical UUID")
	}
	run, err := s.runs.Get(ctx, tenantID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.TenantID != tenantID || run.ID != runID {
		return nil, errProductionToolScope
	}
	if err := requireProductionDocumentReader(ctx, s.projects, run.ProjectID); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *productionRunService) DecideToolCall(
	ctx context.Context,
	callID string,
	decision interfaces.ProductionToolDecision,
) (*types.ProductionToolCall, error) {
	tenantID, actor, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !canonicalProductionUUID(callID) || !canonicalProductionUUID(actor) || actor == productionSystemActorID || !decision.IsValid() {
		return nil, types.ErrProductionForbidden
	}
	if s.runs == nil || s.projects == nil || s.audit == nil || s.uow == nil || s.resumer == nil {
		return nil, errors.New("production run decision dependencies are required")
	}
	call, err := s.runs.GetToolCall(ctx, tenantID, callID)
	if err != nil {
		return nil, err
	}
	if call == nil || call.ID != callID || call.TenantID != tenantID || call.Status != types.ProductionToolCallPendingApproval ||
		call.ApprovalStatus != types.ProductionToolApprovalPending {
		return nil, types.ErrProductionConflict
	}
	run, err := s.runs.Get(ctx, tenantID, call.RunID)
	if err != nil {
		return nil, err
	}
	if !productionDecisionScopeMatches(run, call) {
		return nil, types.ErrProductionConflict
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, run.ProjectID); err != nil {
		return nil, err
	}
	callStatus := types.ProductionToolCallApproved
	action := types.AuditActionProductionToolCallApproved
	to := types.ProductionRunQueued
	patch := interfaces.ProductionRunPatch{IncrementWakeup: true}
	if decision == interfaces.ProductionToolDecisionReject {
		callStatus = types.ProductionToolCallRejected
		action = types.AuditActionProductionToolCallRejected
		to = types.ProductionRunFailed
		patch.IncrementWakeup = false
		code, message, completed := "TOOL_CALL_REJECTED", "production tool call was rejected", s.now().UTC()
		patch.ErrorCode, patch.ErrorMessage, patch.CompletedAt = &code, &message, &completed
	}
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		changed, resolveErr := s.runs.ResolveToolCall(txCtx, tenantID, call.ID, interfaces.ProductionToolCallCAS{
			Status: call.Status, Attempt: call.Attempt, CurrentStep: call.CurrentStep,
		}, callStatus, actor)
		if resolveErr != nil {
			return resolveErr
		}
		if !changed {
			return types.ErrProductionConflict
		}
		_, changed, transitionErr := s.runs.Transition(txCtx, tenantID, run.ID, productionRunCAS(run), to, patch)
		if transitionErr != nil {
			return transitionErr
		}
		if !changed {
			return types.ErrProductionConflict
		}
		details, _ := canonicalProductionValue(map[string]any{"decision": decision, "run_id": run.ID})
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: actor, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: action, TargetType: "production_tool_call", TargetID: call.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
	if err != nil {
		return nil, err
	}
	resolved, err := s.runs.GetToolCall(ctx, tenantID, call.ID)
	if err != nil {
		return nil, err
	}
	if decision == interfaces.ProductionToolDecisionApprove {
		if err := s.resumeAfterCommit(ctx, tenantID, run.ID, run.Attempt); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func (s *productionRunService) loadRunDefinition(
	ctx context.Context,
	tenantID uint64,
	document *types.ProductionDocument,
	sourceSetID, modelID string,
) (*types.ProductionSourceSet, *types.ProductionDocumentType, error) {
	if s.sources == nil || s.documentTypes == nil || s.models == nil {
		return nil, nil, errors.New("production run definition dependencies are required")
	}
	sourceSet, err := s.sources.GetSet(ctx, tenantID, sourceSetID)
	if err != nil {
		return nil, nil, err
	}
	if sourceSet == nil || sourceSet.ID != sourceSetID || sourceSet.TenantID != tenantID ||
		sourceSet.ProjectID != document.ProjectID || sourceSet.DocumentTypeID != document.DocumentTypeID {
		return nil, nil, errProductionToolScope
	}
	documentType, err := s.documentTypes.GetByID(ctx, tenantID, document.DocumentTypeID)
	if err != nil {
		return nil, nil, err
	}
	if documentType == nil || documentType.ID != document.DocumentTypeID || documentType.TenantID != tenantID ||
		documentType.Status != types.ProductionDocumentTypeActive || documentType.SchemaVersion != document.DocumentTypeSchemaVersion {
		return nil, nil, types.ErrProductionDocumentTypeInactive
	}
	model, err := s.models.GetByID(ctx, tenantID, modelID)
	if err != nil {
		return nil, nil, err
	}
	if model == nil || model.ID != modelID || (!model.IsBuiltin && model.TenantID != tenantID) ||
		model.Status != types.ModelStatusActive || model.Type != types.ModelTypeKnowledgeQA {
		return nil, nil, errors.New("production model is unavailable")
	}
	return sourceSet, documentType, nil
}

func (s *productionRunService) persistAndResume(ctx context.Context, run *types.ProductionRun) (*types.ProductionRun, error) {
	if s.runs == nil || s.audit == nil || s.uow == nil || s.resumer == nil {
		return nil, errors.New("production run start dependencies are required")
	}
	_, actor, _ := productionCaller(ctx)
	err := s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.runs.Create(txCtx, run); err != nil {
			return err
		}
		details, _ := canonicalProductionValue(map[string]any{"run_type": run.RunType})
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: run.TenantID, ActorUserID: actor, ActorRole: string(types.TenantRoleFromContext(ctx)),
			Action: types.AuditActionProductionRunStarted, TargetType: "production_run", TargetID: run.ID,
			Outcome: types.AuditOutcomeSuccess, Details: details,
		})
	})
	if err != nil {
		return nil, err
	}
	if err := s.resumeAfterCommit(ctx, run.TenantID, run.ID, run.Attempt); err != nil {
		return nil, err
	}
	return s.runs.Get(ctx, run.TenantID, run.ID)
}

func (s *productionRunService) resumeAfterCommit(ctx context.Context, tenantID uint64, runID string, attempt int) error {
	callback := func(runCtx context.Context) error {
		_, err := s.resumer.Resume(runCtx, tenantID, runID, attempt)
		return err
	}
	if types.RegisterProductionAfterCommit(ctx, callback) {
		return nil
	}
	return callback(ctx)
}

func newProductionRun(
	tenantID uint64,
	document *types.ProductionDocument,
	sourceSet *types.ProductionSourceSet,
	documentType *types.ProductionDocumentType,
	modelID string,
	runType types.ProductionRunType,
	inputVersionID *string,
) *types.ProductionRun {
	snapshot, _ := canonicalProductionValue(map[string]any{
		"id": documentType.ID, "code": documentType.Code, "name": documentType.Name,
		"schema_version": documentType.SchemaVersion, "block_schema": productionJSONValue(documentType.BlockSchema),
		"source_requirements": productionJSONValue(documentType.SourceRequirements),
		"skill_bindings":      productionJSONValue(documentType.SkillBindings), "quality_rules": productionJSONValue(documentType.QualityRules),
		"review_policy": productionJSONValue(documentType.ReviewPolicy), "publication_policy": productionJSONValue(documentType.PublicationPolicy),
	})
	return &types.ProductionRun{
		ID: uuid.NewString(), TenantID: tenantID, ProjectID: document.ProjectID, DocumentID: document.ID,
		SourceSetID: sourceSet.ID, RunType: runType, Status: types.ProductionRunQueued,
		Attempt: 1, CurrentStep: 0, WakeupVersion: 1, StatePayload: types.JSON(`{}`),
		ModelID: modelID, DocumentTypeSnapshot: snapshot, InputVersionID: inputVersionID,
		IdempotencyKey: uuid.NewString(),
	}
}

func productionJSONValue(raw types.JSON) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := productionDecodeJSON(raw, `{}`, &value); err != nil {
		return map[string]any{}
	}
	return value
}

func productionDecisionScopeMatches(run *types.ProductionRun, call *types.ProductionToolCall) bool {
	return run != nil && call != nil && run.ID == call.RunID && run.TenantID == call.TenantID &&
		run.ProjectID == call.ProjectID && run.DocumentID == call.DocumentID && run.SourceSetID == call.SourceSetID &&
		run.Status == types.ProductionRunWaitingApproval && run.Attempt == call.Attempt && run.CurrentStep == call.CurrentStep &&
		!isProductionRunTerminal(run.Status)
}

var _ interfaces.ProductionRunService = (*productionRunService)(nil)
