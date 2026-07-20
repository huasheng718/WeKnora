package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

const postgresProductionReviewLockSQL = `
SELECT id
FROM production_document_versions
WHERE id = ? AND document_id = ? AND tenant_id = ? AND project_id = ?
FOR UPDATE`

const productionReviewObsoleteReason = "superseded by a newer document version"

type ProductionReviewClock interface {
	Now() time.Time
}

type productionReviewSystemClock struct{}

func (productionReviewSystemClock) Now() time.Time { return time.Now() }

type productionReviewRepository struct {
	db    *gorm.DB
	clock ProductionReviewClock
}

func NewProductionReviewRepository(db *gorm.DB) interfaces.ProductionReviewRepository {
	return NewProductionReviewRepositoryWithClock(db, productionReviewSystemClock{})
}

func NewProductionReviewRepositoryWithClock(db *gorm.DB, clock ProductionReviewClock) interfaces.ProductionReviewRepository {
	if clock == nil {
		clock = productionReviewSystemClock{}
	}
	return &productionReviewRepository{db: db, clock: clock}
}

func (r *productionReviewRepository) nowUTC() time.Time { return r.clock.Now().UTC() }

func requireProductionReviewUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value || len(value) != 36 {
		return fmt.Errorf("%w: %s must be a canonical UUID", types.ErrProductionReviewScopeInvalid, name)
	}
	return nil
}

func requireProductionReviewTenantContext(ctx context.Context, tenantID uint64) error {
	if ctx == nil {
		return types.ErrProductionForbidden
	}
	contextTenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || contextTenantID == 0 || contextTenantID != tenantID {
		return types.ErrProductionForbidden
	}
	return nil
}

func trustedProductionReviewActor(ctx context.Context, tenantID uint64, suppliedActorID string) (string, error) {
	if err := requireProductionReviewTenantContext(ctx, tenantID); err != nil {
		return "", err
	}
	actorID, ok := types.UserIDFromContext(ctx)
	if !ok || actorID == types.ProductionSystemActorID {
		return "", types.ErrProductionForbidden
	}
	if err := requireProductionReviewUUID("context actor", actorID); err != nil {
		return "", types.ErrProductionForbidden
	}
	if suppliedActorID != "" && suppliedActorID != actorID {
		return "", types.ErrProductionForbidden
	}
	return actorID, nil
}

func canonicalProductionReviewObject(raw types.JSON, fallback string, maxBytes, maxDepth int) (types.JSON, error) {
	if len(raw) == 0 {
		raw = types.JSON(fallback)
	}
	if err := types.ValidateProductionJSONResource(raw, maxBytes, maxDepth); err != nil {
		return nil, err
	}
	canonical, err := types.CanonicalProductionJSON(raw)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil || object == nil {
		return nil, errors.New("JSON value must be an object")
	}
	return canonical, nil
}

func translateProductionReviewError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "open blocking annotations prevent review submission"):
		return errors.Join(types.ErrProductionBlockingAnnotations, err)
	case strings.Contains(lower, "terminal production annotations are immutable"),
		strings.Contains(lower, "production annotation anchor is immutable"),
		strings.Contains(lower, "production annotations are append-only"),
		strings.Contains(lower, "production annotations cannot be replaced"):
		return errors.Join(types.ErrProductionAnnotationImmutable, err)
	case strings.Contains(lower, "invalid production annotation status transition"):
		return errors.Join(types.ErrProductionAnnotationLifecycle, err)
	case strings.Contains(lower, "production review requests cannot be replaced"),
		strings.Contains(lower, "production review steps cannot be replaced"):
		return errors.Join(types.ErrProductionConflict, err)
	case strings.Contains(lower, "terminal production review requests are immutable"),
		strings.Contains(lower, "production review request identity is immutable"),
		strings.Contains(lower, "production review requests are append-only"),
		strings.Contains(lower, "terminal production review steps are immutable"),
		strings.Contains(lower, "production review step identity is immutable"),
		strings.Contains(lower, "production review steps are append-only"):
		return errors.Join(types.ErrProductionReviewImmutable, err)
	case strings.Contains(lower, "production review requests must be submitted pending"),
		strings.Contains(lower, "production review steps must be inserted pending"),
		strings.Contains(lower, "terminal production review requests reject review steps"),
		strings.Contains(lower, "all production review steps must approve the review"),
		strings.Contains(lower, "invalid production review"):
		return errors.Join(types.ErrProductionReviewLifecycle, err)
	case strings.Contains(lower, "review decision requires the required review role"):
		return errors.Join(types.ErrProductionForbidden, err)
	default:
		return translateProductionWriteError(err)
	}
}

func isProductionForeignKeyError(err error) bool {
	if err == nil {
		return false
	}
	var state sqlStateError
	if errors.As(err, &state) && state.SQLState() == "23503" {
		return true
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintForeignKey {
		return true
	}
	var sqliteErrPointer *sqlite3.Error
	return errors.As(err, &sqliteErrPointer) && sqliteErrPointer != nil &&
		sqliteErrPointer.ExtendedCode == sqlite3.ErrConstraintForeignKey
}

func validateProductionAnnotation(annotation *types.ProductionAnnotation) error {
	if annotation == nil || annotation.TenantID == 0 {
		return types.ErrProductionReviewScopeInvalid
	}
	for name, value := range map[string]string{
		"annotation id": annotation.ID, "project id": annotation.ProjectID,
		"document id": annotation.DocumentID, "version id": annotation.VersionID,
		"block id": annotation.BlockID, "created by": annotation.CreatedBy,
	} {
		if err := requireProductionReviewUUID(name, value); err != nil {
			return err
		}
	}
	if !annotation.AnnotationType.IsValid() || !annotation.Severity.IsValid() {
		return errors.New("invalid production annotation type or severity")
	}
	if annotation.AnnotationType == types.ProductionAnnotationQualityTag {
		if annotation.QualityTag == nil || !annotation.QualityTag.IsValid() {
			return errors.New("production quality annotation requires a valid category")
		}
	} else if annotation.QualityTag != nil {
		return errors.New("production comment and suggestion annotations cannot have a quality category")
	}
	annotation.Body = strings.TrimSpace(annotation.Body)
	if length := utf8.RuneCountInString(annotation.Body); length < 1 || length > 20000 {
		return errors.New("production annotation body must contain between 1 and 20000 characters")
	}
	if annotation.SuggestedContent != nil && utf8.RuneCountInString(*annotation.SuggestedContent) > 20000 {
		return errors.New("production annotation suggested content exceeds 20000 characters")
	}
	anchor, err := canonicalProductionReviewObject(
		annotation.Anchor,
		"{}",
		types.ProductionAnnotationAnchorMaxBytes,
		types.ProductionAnnotationAnchorMaxDepth,
	)
	if err != nil {
		return errors.Join(types.ErrProductionAnnotationAnchorInvalid, err)
	}
	annotation.Anchor = anchor
	if annotation.Status != types.ProductionAnnotationOpen || annotation.ResolvedBy != nil || annotation.ResolvedAt != nil {
		return types.ErrProductionAnnotationLifecycle
	}
	return nil
}

func (r *productionReviewRepository) CreateAnnotation(ctx context.Context, annotation *types.ProductionAnnotation) error {
	if annotation == nil {
		return types.ErrProductionReviewScopeInvalid
	}
	actorID, err := trustedProductionReviewActor(ctx, annotation.TenantID, annotation.CreatedBy)
	if err != nil {
		return err
	}
	annotation.CreatedBy = actorID
	if err := validateProductionAnnotation(annotation); err != nil {
		return err
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	now := r.nowUTC()
	annotation.CreatedAt = now
	annotation.UpdatedAt = now
	anchorValue := any(string(annotation.Anchor))
	if db.Dialector.Name() == "postgres" {
		anchorValue = gorm.Expr("CAST(? AS JSONB)", string(annotation.Anchor))
	}
	var qualityTag, suggestedContent any
	if annotation.QualityTag != nil {
		qualityTag = string(*annotation.QualityTag)
	}
	if annotation.SuggestedContent != nil {
		suggestedContent = *annotation.SuggestedContent
	}
	values := map[string]any{
		"id": annotation.ID, "tenant_id": annotation.TenantID, "project_id": annotation.ProjectID,
		"document_id": annotation.DocumentID, "version_id": annotation.VersionID, "block_id": annotation.BlockID,
		"annotation_type": annotation.AnnotationType, "quality_tag": qualityTag, "severity": annotation.Severity,
		"anchor": anchorValue, "body": annotation.Body, "suggested_content": suggestedContent,
		"status": annotation.Status, "created_by": annotation.CreatedBy,
		"resolved_by": nil, "resolved_at": nil, "created_at": annotation.CreatedAt, "updated_at": annotation.UpdatedAt,
	}
	err = db.Model(&types.ProductionAnnotation{}).Create(values).Error
	if isProductionForeignKeyError(err) {
		return errors.Join(types.ErrProductionAnnotationAnchorInvalid, err)
	}
	return translateProductionReviewError(err)
}

func (r *productionReviewRepository) ResolveAnnotation(
	ctx context.Context,
	tenantID uint64,
	annotationID, actorID string,
	resolution types.ProductionAnnotationStatus,
) (bool, error) {
	if tenantID == 0 {
		return false, types.ErrProductionReviewScopeInvalid
	}
	trustedActorID, err := trustedProductionReviewActor(ctx, tenantID, actorID)
	if err != nil {
		return false, err
	}
	if err := requireProductionReviewUUID("annotation id", annotationID); err != nil {
		return false, err
	}
	if resolution != types.ProductionAnnotationResolved && resolution != types.ProductionAnnotationDismissed {
		return false, types.ErrProductionAnnotationLifecycle
	}
	now := r.nowUTC()
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionAnnotation{}).
		Where("tenant_id = ? AND id = ? AND status = ?", tenantID, annotationID, types.ProductionAnnotationOpen).
		Updates(map[string]any{
			"status": resolution, "resolved_by": trustedActorID, "resolved_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return false, translateProductionReviewError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *productionReviewRepository) CountOpenBlocking(ctx context.Context, tenantID uint64, versionID string) (int64, error) {
	if tenantID == 0 {
		return 0, types.ErrProductionReviewScopeInvalid
	}
	if err := requireProductionReviewUUID("version id", versionID); err != nil {
		return 0, err
	}
	var count int64
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionAnnotation{}).
		Where("tenant_id = ? AND version_id = ? AND severity = ? AND status = ?",
			tenantID, versionID, types.ProductionAnnotationBlocking, types.ProductionAnnotationOpen).
		Count(&count).Error
	return count, err
}

func validateProductionReviewRequest(request *types.ProductionReviewRequest, steps []*types.ProductionReviewStep) error {
	if request == nil || request.TenantID == 0 {
		return types.ErrProductionReviewScopeInvalid
	}
	for name, value := range map[string]string{
		"review id": request.ID, "project id": request.ProjectID,
		"document id": request.DocumentID, "version id": request.VersionID,
		"submitted by": request.SubmittedBy,
	} {
		if err := requireProductionReviewUUID(name, value); err != nil {
			return err
		}
	}
	if request.Status != types.ProductionReviewPending || request.TerminalBy != nil ||
		request.TerminalReason != nil || request.CompletedAt != nil {
		return types.ErrProductionReviewLifecycle
	}
	canonical, digest, err := types.CanonicalProductionReviewPolicy(request.PolicySnapshot)
	if err != nil {
		return err
	}
	if request.PolicyDigest != digest {
		return fmt.Errorf("%w: policy digest does not match the canonical snapshot", types.ErrProductionReviewPolicyInvalid)
	}
	request.PolicySnapshot = canonical
	request.PolicyDigest = digest
	if len(steps) == 0 {
		return fmt.Errorf("%w: at least one review step is required", types.ErrProductionReviewLifecycle)
	}
	sequences := make(map[int]struct{}, len(steps))
	roles := make(map[types.ProductionRole]struct{}, len(steps))
	for _, step := range steps {
		if step == nil {
			return errors.New("production review step is required")
		}
		if err := requireProductionReviewUUID("review step id", step.ID); err != nil {
			return err
		}
		if step.ReviewRequestID != "" && step.ReviewRequestID != request.ID {
			return types.ErrProductionReviewScopeInvalid
		}
		if step.TenantID != 0 && step.TenantID != request.TenantID {
			return types.ErrProductionReviewScopeInvalid
		}
		for scopeName, pair := range map[string][2]string{
			"project":  {step.ProjectID, request.ProjectID},
			"document": {step.DocumentID, request.DocumentID},
			"version":  {step.VersionID, request.VersionID},
		} {
			if stepScopeMismatch(pair[0], pair[1]) {
				return fmt.Errorf("%w: review step %s scope does not match request", types.ErrProductionReviewScopeInvalid, scopeName)
			}
		}
		if step.RequiredRole != types.ProductionRoleBusinessReviewer &&
			step.RequiredRole != types.ProductionRoleEngineeringReviewer &&
			step.RequiredRole != types.ProductionRoleComplianceReviewer {
			return errors.New("production review step requires a professional review role")
		}
		if _, duplicate := roles[step.RequiredRole]; duplicate {
			return errors.New("production review step roles must be unique")
		}
		roles[step.RequiredRole] = struct{}{}
		if step.Sequence < 1 || step.Sequence > len(steps) {
			return errors.New("production review step sequence must be contiguous from one")
		}
		if _, duplicate := sequences[step.Sequence]; duplicate {
			return errors.New("production review step sequences must be unique")
		}
		sequences[step.Sequence] = struct{}{}
		if step.Decision != types.ProductionReviewPending || step.ReviewerUserID != nil ||
			step.DecidedAt != nil || step.Comment != "" {
			return types.ErrProductionReviewLifecycle
		}
		step.ReviewRequestID = request.ID
		step.TenantID = request.TenantID
		step.ProjectID = request.ProjectID
		step.DocumentID = request.DocumentID
		step.VersionID = request.VersionID
	}
	return nil
}

func stepScopeMismatch(supplied, authoritative string) bool {
	return supplied != "" && supplied != authoritative
}

func lockProductionReviewVersion(db *gorm.DB, request *types.ProductionReviewRequest) error {
	var id string
	if db.Dialector.Name() == "postgres" {
		err := db.Raw(postgresProductionReviewLockSQL,
			request.VersionID, request.DocumentID, request.TenantID, request.ProjectID,
		).Scan(&id).Error
		if err != nil {
			return err
		}
		if id == "" {
			return types.ErrProductionReviewScopeInvalid
		}
		return nil
	}
	// SQLite has no row-level locks. Take its write reservation before the
	// authoritative version read so duplicate submissions serialize and the
	// loser observes the stable review uniqueness conflict.
	lockedDocument := db.Model(&types.ProductionDocument{}).
		Where("id = ? AND tenant_id = ? AND project_id = ?", request.DocumentID, request.TenantID, request.ProjectID).
		UpdateColumn("updated_at", gorm.Expr("updated_at"))
	if lockedDocument.Error != nil {
		return lockedDocument.Error
	}
	if lockedDocument.RowsAffected != 1 {
		return types.ErrProductionReviewScopeInvalid
	}
	err := db.Raw(`SELECT id FROM production_document_versions
WHERE id = ? AND document_id = ? AND tenant_id = ? AND project_id = ?`,
		request.VersionID, request.DocumentID, request.TenantID, request.ProjectID,
	).Scan(&id).Error
	if err != nil {
		return err
	}
	if id == "" {
		return types.ErrProductionReviewScopeInvalid
	}
	return nil
}

func (r *productionReviewRepository) CreateReview(
	ctx context.Context,
	request *types.ProductionReviewRequest,
	steps []*types.ProductionReviewStep,
) error {
	if request == nil {
		return types.ErrProductionReviewScopeInvalid
	}
	actorID, err := trustedProductionReviewActor(ctx, request.TenantID, request.SubmittedBy)
	if err != nil {
		return err
	}
	request.SubmittedBy = actorID
	if err := validateProductionReviewRequest(request, steps); err != nil {
		return err
	}
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		if err := lockProductionReviewVersion(db, request); err != nil {
			return translateProductionReviewError(err)
		}
		now := r.nowUTC()
		request.SubmittedAt = now
		request.CreatedAt = now
		request.UpdatedAt = now
		for _, step := range steps {
			step.CreatedAt = now
			step.UpdatedAt = now
		}
		policyValue := any(string(request.PolicySnapshot))
		if db.Dialector.Name() == "postgres" {
			policyValue = gorm.Expr("CAST(? AS JSONB)", string(request.PolicySnapshot))
		}
		requestValues := map[string]any{
			"id": request.ID, "tenant_id": request.TenantID, "project_id": request.ProjectID,
			"document_id": request.DocumentID, "version_id": request.VersionID,
			"policy_snapshot": policyValue, "policy_digest": request.PolicyDigest,
			"status": request.Status, "submitted_by": request.SubmittedBy,
			"submitted_at": request.SubmittedAt, "terminal_by": nil, "terminal_reason": nil,
			"completed_at": nil, "created_at": request.CreatedAt, "updated_at": request.UpdatedAt,
		}
		if err := translateProductionReviewError(
			db.Model(&types.ProductionReviewRequest{}).Create(requestValues).Error,
		); err != nil {
			return err
		}
		for _, step := range steps {
			if err := translateProductionReviewError(db.Create(step).Error); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *productionReviewRepository) GetReview(ctx context.Context, tenantID uint64, reviewID string) (*types.ProductionReviewRequest, error) {
	if tenantID == 0 {
		return nil, types.ErrProductionReviewScopeInvalid
	}
	if err := requireProductionReviewUUID("review id", reviewID); err != nil {
		return nil, err
	}
	var request types.ProductionReviewRequest
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Preload("Steps", func(db *gorm.DB) *gorm.DB { return db.Order("sequence ASC") }).
		Where("tenant_id = ? AND id = ?", tenantID, reviewID).
		First(&request).Error
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func (r *productionReviewRepository) DecideStep(
	ctx context.Context,
	tenantID uint64,
	stepID string,
	from, to types.ProductionReviewDecision,
	actorID, comment string,
) (bool, error) {
	if tenantID == 0 {
		return false, types.ErrProductionReviewScopeInvalid
	}
	trustedActorID, err := trustedProductionReviewActor(ctx, tenantID, actorID)
	if err != nil {
		return false, err
	}
	if err := requireProductionReviewUUID("review step id", stepID); err != nil {
		return false, err
	}
	if !types.CanTransitionReviewStep(from, to) {
		return false, types.ErrProductionReviewLifecycle
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 5000 {
		return false, errors.New("production review comment exceeds 5000 characters")
	}
	now := r.nowUTC()
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionReviewStep{}).
		Where("tenant_id = ? AND id = ? AND decision = ?", tenantID, stepID, from).
		Updates(map[string]any{
			"decision": to, "reviewer_user_id": trustedActorID, "comment": comment,
			"decided_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return false, translateProductionReviewError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *productionReviewRepository) ObsoletePendingByDocument(
	ctx context.Context,
	tenantID uint64,
	documentID, exceptVersionID string,
) error {
	if tenantID == 0 {
		return types.ErrProductionReviewScopeInvalid
	}
	if err := requireProductionReviewTenantContext(ctx, tenantID); err != nil {
		return err
	}
	if err := requireProductionReviewUUID("document id", documentID); err != nil {
		return err
	}
	if err := requireProductionReviewUUID("except version id", exceptVersionID); err != nil {
		return err
	}
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		var exactVersionID string
		if err := db.Raw(`SELECT id FROM production_document_versions
WHERE tenant_id = ? AND document_id = ? AND id = ?`, tenantID, documentID, exceptVersionID).
			Scan(&exactVersionID).Error; err != nil {
			return err
		}
		if exactVersionID == "" {
			return types.ErrProductionReviewScopeInvalid
		}
		now := r.nowUTC()
		result := db.Model(&types.ProductionReviewRequest{}).
			Where("tenant_id = ? AND document_id = ? AND version_id <> ? AND status = ?",
				tenantID, documentID, exceptVersionID, types.ProductionReviewPending).
			Updates(map[string]any{
				"status":          types.ProductionReviewObsolete,
				"terminal_by":     types.ProductionSystemActorID,
				"terminal_reason": productionReviewObsoleteReason,
				"completed_at":    now,
				"updated_at":      now,
			})
		return translateProductionReviewError(result.Error)
	})
}

var _ interfaces.ProductionReviewRepository = (*productionReviewRepository)(nil)
