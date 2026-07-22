package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type ProductionReviewService interface {
	Submit(ctx context.Context, documentID, versionID string) (*types.ProductionReviewRequest, error)
	List(ctx context.Context, documentID string, offset, limit int) ([]*types.ProductionReviewRequest, int64, error)
	Get(ctx context.Context, reviewID string) (*types.ProductionReviewRequest, error)
	Decide(ctx context.Context, stepID string, decision types.ProductionReviewDecision, comment string) error
	Reject(ctx context.Context, reviewID, reason string) error
	Cancel(ctx context.Context, reviewID, reason string) error
}

type productionReviewHistoryRepository interface {
	ListReviews(context.Context, uint64, string, int, int) ([]*types.ProductionReviewRequest, int64, error)
}

func (s *productionReviewService) List(
	ctx context.Context,
	documentID string,
	offset int,
	limit int,
) ([]*types.ProductionReviewRequest, int64, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	if err := requireProductionSourceID(documentID, "document id"); err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > types.ProductionReleaseMaxTargets {
		return nil, 0, types.ErrProductionReviewScopeInvalid
	}
	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, 0, err
	}
	if document == nil || document.ID != documentID || document.TenantID != tenantID || document.ProjectID == "" {
		return nil, 0, types.ErrProductionReviewScopeInvalid
	}
	if s.projects == nil {
		return nil, 0, types.ErrProductionForbidden
	}
	if err := s.projects.RequireProjectRole(ctx, document.ProjectID,
		types.ProductionRoleProjectOwner,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
		types.ProductionRolePublisher,
		types.ProductionRoleObserver,
	); err != nil {
		return nil, 0, err
	}
	history, ok := s.reviews.(productionReviewHistoryRepository)
	if !ok {
		return nil, 0, errors.New("production review history lookup is unavailable")
	}
	return history.ListReviews(ctx, tenantID, documentID, offset, limit)
}

func (s *productionReviewService) Get(
	ctx context.Context,
	reviewID string,
) (*types.ProductionReviewRequest, error) {
	tenantID, _, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	review, err := s.reviews.GetReview(ctx, tenantID, reviewID)
	if err != nil {
		return nil, err
	}
	if review == nil || review.TenantID != tenantID || review.ProjectID == "" {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	if s.projects == nil {
		return nil, types.ErrProductionForbidden
	}
	if err := s.projects.RequireProjectRole(ctx, review.ProjectID,
		types.ProductionRoleProjectOwner,
		types.ProductionRoleAuthor,
		types.ProductionRoleBusinessReviewer,
		types.ProductionRoleEngineeringReviewer,
		types.ProductionRoleComplianceReviewer,
		types.ProductionRoleObserver,
	); err != nil {
		return nil, err
	}
	return review, nil
}

type productionReviewService struct {
	reviews       interfaces.ProductionReviewRepository
	documents     interfaces.ProductionDocumentRepository
	sources       interfaces.ProductionSourceRepository
	documentTypes interfaces.ProductionDocumentTypeRepository
	projects      interfaces.ProductionProjectAuthorizer
	resources     interfaces.ResourceCatalog
	members       interfaces.TenantMemberService
	audit         interfaces.AuditLogService
	uow           interfaces.ProductionUnitOfWork
}

func NewProductionReviewService(
	reviews interfaces.ProductionReviewRepository,
	documents interfaces.ProductionDocumentRepository,
	sources interfaces.ProductionSourceRepository,
	documentTypes interfaces.ProductionDocumentTypeRepository,
	projects interfaces.ProductionProjectAuthorizer,
	resources interfaces.ResourceCatalog,
	members interfaces.TenantMemberService,
	audit interfaces.AuditLogService,
	uow interfaces.ProductionUnitOfWork,
) *productionReviewService {
	return &productionReviewService{
		reviews: reviews, documents: documents, sources: sources, documentTypes: documentTypes,
		projects: projects, resources: resources, members: members, audit: audit, uow: uow,
	}
}

type productionReviewPolicy struct {
	Steps []types.ProductionRole `json:"steps"`
}

func materializeProductionReviewPolicy(
	raw types.JSON,
	request *types.ProductionReviewRequest,
) ([]*types.ProductionReviewStep, error) {
	canonical, digest, err := types.CanonicalProductionReviewPolicy(raw)
	if err != nil {
		return nil, err
	}
	var policy productionReviewPolicy
	if err := json.Unmarshal(canonical, &policy); err != nil {
		return nil, errors.Join(types.ErrProductionReviewPolicyInvalid, err)
	}
	if len(policy.Steps) == 0 {
		return nil, fmt.Errorf("%w: at least one policy step is required", types.ErrProductionReviewPolicyInvalid)
	}
	seen := make(map[types.ProductionRole]struct{}, len(policy.Steps))
	steps := make([]*types.ProductionReviewStep, 0, len(policy.Steps))
	for index, role := range policy.Steps {
		if role != types.ProductionRoleBusinessReviewer &&
			role != types.ProductionRoleEngineeringReviewer &&
			role != types.ProductionRoleComplianceReviewer {
			return nil, fmt.Errorf("%w: step %d requires a professional review role", types.ErrProductionReviewPolicyInvalid, index+1)
		}
		if _, duplicate := seen[role]; duplicate {
			return nil, fmt.Errorf("%w: policy roles must be unique", types.ErrProductionReviewPolicyInvalid)
		}
		seen[role] = struct{}{}
		steps = append(steps, &types.ProductionReviewStep{
			ID: uuid.NewString(), RequiredRole: role, Sequence: index + 1,
			Decision: types.ProductionReviewPending,
		})
	}
	request.PolicySnapshot = canonical
	request.PolicyDigest = digest
	return steps, nil
}

func (s *productionReviewService) Submit(
	ctx context.Context,
	documentID, versionID string,
) (*types.ProductionReviewRequest, error) {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return nil, err
	}
	membership, _, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(documentID, "document id"); err != nil {
		return nil, err
	}
	if err := requireProductionSourceID(versionID, "version id"); err != nil {
		return nil, err
	}
	document, err := s.documents.GetDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	if err := requireProductionDocumentAuthor(ctx, s.projects, document.ProjectID); err != nil {
		return nil, err
	}
	if s.uow == nil {
		return nil, errors.New("production unit of work is required")
	}

	request := &types.ProductionReviewRequest{
		ID: uuid.NewString(), TenantID: tenantID, SubmittedBy: actorID,
		DocumentID: documentID, VersionID: versionID, Status: types.ProductionReviewPending,
	}
	var digestMismatch error
	err = s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		liveDocument, loadErr := s.documents.GetDocument(txCtx, tenantID, documentID)
		if loadErr != nil {
			return loadErr
		}
		if liveDocument.CurrentVersionID == nil || *liveDocument.CurrentVersionID != versionID {
			return types.ErrProductionReviewScopeInvalid
		}
		request.ProjectID = liveDocument.ProjectID
		if lockErr := s.reviews.LockCurrentReviewVersion(txCtx, request); lockErr != nil {
			return lockErr
		}
		version, loadErr := s.documents.GetVersion(txCtx, tenantID, versionID)
		if loadErr != nil {
			return loadErr
		}
		if version.DocumentID != documentID || version.ProjectID != liveDocument.ProjectID ||
			version.FrozenAt == nil {
			return types.ErrProductionReviewScopeInvalid
		}
		documentType, loadErr := s.documentTypes.GetByID(txCtx, tenantID, liveDocument.DocumentTypeID)
		if loadErr != nil {
			return loadErr
		}
		if documentType == nil || documentType.ID != liveDocument.DocumentTypeID || documentType.TenantID != tenantID ||
			documentType.SchemaVersion != liveDocument.DocumentTypeSchemaVersion ||
			(documentType.Status != types.ProductionDocumentTypeActive && documentType.Status != types.ProductionDocumentTypeRetired) {
			return types.ErrProductionDocumentTypeInactive
		}
		documentTypeConfig, configErr := productionDocumentTypeConfig(documentType)
		if configErr != nil {
			return errors.Join(types.ErrProductionReviewPolicyInvalid, configErr)
		}
		blocking, loadErr := s.reviews.CountOpenBlocking(txCtx, tenantID, versionID)
		if loadErr != nil {
			return loadErr
		}
		if blocking != 0 {
			return types.ErrProductionBlockingAnnotations
		}
		_, evidenceByID, loadErr := loadProductionAcceptedEvidence(
			txCtx, s.sources, s.resources, tenantID, liveDocument.ProjectID, version.SourceSetID,
		)
		if loadErr != nil {
			return loadErr
		}
		if verifyErr := VerifyProductionVersionDigests(version, evidenceByID); verifyErr != nil {
			digestMismatch = errors.Join(types.ErrProductionContentDigestMismatch, verifyErr)
			return digestMismatch
		}
		steps, policyErr := materializeProductionReviewPolicy(documentTypeConfig.Canonical.ReviewPolicy, request)
		if policyErr != nil {
			return policyErr
		}
		for _, step := range steps {
			available, availabilityErr := s.projects.HasLiveRoleAssignee(
				txCtx, tenantID, liveDocument.ProjectID, step.RequiredRole,
			)
			if availabilityErr != nil {
				return availabilityErr
			}
			if !available {
				return errors.Join(
					types.ErrProductionReviewPolicyInvalid,
					fmt.Errorf("reviewer_role_unavailable:%s", step.RequiredRole),
				)
			}
		}
		request.Steps = steps
		if createErr := s.reviews.CreateCurrentReview(txCtx, request, steps); createErr != nil {
			return createErr
		}
		return emitRequiredProductionAudit(txCtx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: actorID, ActorRole: string(membership.Role),
			Action: types.AuditActionProductionReviewSubmitted, TargetType: "production_review_request",
			TargetID: request.ID, Outcome: types.AuditOutcomeSuccess,
		})
	})
	if digestMismatch != nil {
		auditErr := emitRequiredProductionAudit(ctx, s.audit, &types.AuditLog{
			TenantID: tenantID, ActorUserID: actorID, ActorRole: string(membership.Role),
			Action:     types.AuditActionProductionContentDigestMismatch,
			TargetType: "production_document_version", TargetID: versionID,
			Outcome: types.AuditOutcomeDenied,
		})
		if auditErr != nil {
			return nil, errors.Join(digestMismatch, auditErr)
		}
		return nil, digestMismatch
	}
	if err != nil {
		return nil, err
	}
	return request, nil
}

func (s *productionReviewService) Decide(
	ctx context.Context,
	stepID string,
	decision types.ProductionReviewDecision,
	comment string,
) error {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	membership, _, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return err
	}
	if !decision.IsProfessionalDecision() {
		return types.ErrProductionReviewLifecycle
	}
	if s.uow == nil {
		return errors.New("production unit of work is required")
	}
	return s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		changed, decideErr := s.reviews.DecideStep(
			txCtx, tenantID, stepID, types.ProductionReviewPending, decision, actorID, comment,
		)
		if decideErr != nil {
			return decideErr
		}
		if !changed {
			return types.ErrProductionReviewLifecycle
		}
		return emitProductionReviewDecisionAudit(
			txCtx, s.audit, tenantID, actorID, membership.Role, stepID, string(decision),
		)
	})
}

func (s *productionReviewService) Reject(ctx context.Context, reviewID, reason string) error {
	return s.terminalizeByTenantAuthority(ctx, reviewID, reason, types.ProductionReviewRejected)
}

func (s *productionReviewService) Cancel(ctx context.Context, reviewID, reason string) error {
	return s.terminalizeByTenantAuthority(ctx, reviewID, reason, types.ProductionReviewCancelled)
}

func (s *productionReviewService) terminalizeByTenantAuthority(
	ctx context.Context,
	reviewID, reason string,
	status types.ProductionReviewStatus,
) error {
	tenantID, actorID, err := productionCaller(ctx)
	if err != nil {
		return err
	}
	membership, _, err := productionMembership(ctx, s.members, tenantID)
	if err != nil {
		return err
	}
	if !membership.Role.HasPermission(types.TenantRoleAdmin) {
		return types.ErrProductionForbidden
	}
	if s.uow == nil {
		return errors.New("production unit of work is required")
	}
	return s.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		var changed bool
		var terminalErr error
		if status == types.ProductionReviewRejected {
			changed, terminalErr = s.reviews.RejectReviewByTenantAuthority(txCtx, tenantID, reviewID, actorID, reason)
		} else {
			changed, terminalErr = s.reviews.CancelReviewByTenantAuthority(txCtx, tenantID, reviewID, actorID, reason)
		}
		if terminalErr != nil {
			return terminalErr
		}
		if !changed {
			liveMembership, _, liveErr := productionMembership(txCtx, s.members, tenantID)
			if liveErr != nil {
				return liveErr
			}
			if !liveMembership.Role.HasPermission(types.TenantRoleAdmin) {
				return types.ErrProductionForbidden
			}
			review, loadErr := s.reviews.GetReview(txCtx, tenantID, reviewID)
			if loadErr != nil {
				return loadErr
			}
			if review != nil && review.Status == status {
				return nil
			}
			if review != nil && review.Status != types.ProductionReviewPending {
				return types.ErrProductionConflict
			}
			return types.ErrProductionForbidden
		}
		return emitProductionReviewDecisionAudit(
			txCtx, s.audit, tenantID, actorID, membership.Role, reviewID, string(status),
		)
	})
}

func emitProductionReviewDecisionAudit(
	ctx context.Context,
	audit interfaces.AuditLogService,
	tenantID uint64,
	actorID string,
	actorRole types.TenantRole,
	targetID, decision string,
) error {
	details, err := json.Marshal(map[string]string{"decision": decision})
	if err != nil {
		return err
	}
	return emitRequiredProductionAudit(ctx, audit, &types.AuditLog{
		TenantID: tenantID, ActorUserID: actorID, ActorRole: string(actorRole),
		Action: types.AuditActionProductionReviewDecided, TargetType: "production_review",
		TargetID: targetID, Outcome: types.AuditOutcomeSuccess, Details: types.JSON(details),
	})
}

var _ ProductionReviewService = (*productionReviewService)(nil)
