package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const postgresProductionProjectionHeadCASSQL = `
UPDATE production_projection_heads
SET active_release_target_id = ?, lock_version = lock_version + 1, updated_at = ?
WHERE tenant_id = ? AND document_id = ? AND target_knowledge_base_id = ? AND lock_version = ?`

var productionReleaseDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ProductionReleaseClock interface {
	Now() time.Time
}

type productionReleaseSystemClock struct{}

func (productionReleaseSystemClock) Now() time.Time { return time.Now() }

type productionReleaseRepository struct {
	db    *gorm.DB
	clock ProductionReleaseClock
}

func NewProductionReleaseRepository(db *gorm.DB) interfaces.ProductionReleaseRepository {
	return NewProductionReleaseRepositoryWithClock(db, productionReleaseSystemClock{})
}

func NewProductionReleaseRepositoryWithClock(db *gorm.DB, clock ProductionReleaseClock) interfaces.ProductionReleaseRepository {
	if clock == nil {
		clock = productionReleaseSystemClock{}
	}
	return &productionReleaseRepository{db: db, clock: clock}
}

func (r *productionReleaseRepository) nowUTC() time.Time {
	return r.clock.Now().UTC().Truncate(time.Second)
}

func requireProductionReleaseTenantContext(ctx context.Context, tenantID uint64) error {
	if ctx == nil || tenantID == 0 {
		return types.ErrProductionForbidden
	}
	contextTenantID, ok := types.TenantIDFromContext(ctx)
	if ok && contextTenantID != tenantID {
		return types.ErrProductionForbidden
	}
	return nil
}

func trustedProductionReleaseActor(ctx context.Context, tenantID uint64, suppliedActorID string) (string, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return "", err
	}
	contextTenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || contextTenantID != tenantID {
		return "", types.ErrProductionForbidden
	}
	actorID, ok := types.UserIDFromContext(ctx)
	parsed, parseErr := uuid.Parse(actorID)
	if !ok || parseErr != nil || parsed == uuid.Nil || parsed.String() != actorID || actorID == types.ProductionSystemActorID {
		return "", types.ErrProductionForbidden
	}
	if suppliedActorID != "" && suppliedActorID != actorID {
		return "", types.ErrProductionForbidden
	}
	return actorID, nil
}

func requireProductionReleaseIdentity(name, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is required", types.ErrProductionReleaseInvalid, name)
	}
	return nil
}

func validateProductionReleaseForCreate(release *types.ProductionRelease, targets []*types.ProductionReleaseTarget) error {
	if release == nil || len(targets) == 0 {
		return fmt.Errorf("%w: release and at least one target are required", types.ErrProductionReleaseInvalid)
	}
	for name, value := range map[string]string{
		"id": release.ID, "project_id": release.ProjectID, "document_id": release.DocumentID,
		"version_id": release.VersionID, "review_request_id": release.ReviewRequestID,
	} {
		if err := requireProductionReleaseIdentity(name, value); err != nil {
			return err
		}
	}
	if release.TenantID == 0 || (release.ReleaseDigest != "" && !productionReleaseDigestPattern.MatchString(release.ReleaseDigest)) {
		return fmt.Errorf("%w: tenant and lowercase SHA-256 release digest are required", types.ErrProductionReleaseInvalid)
	}
	if release.ReleaseDigestVersion != 0 && release.ReleaseDigestVersion != types.ProductionReleaseDigestVersionCurrent {
		return types.ErrProductionReleaseReprepareRequired
	}
	if release.SupersedesReleaseID != nil {
		if err := requireProductionReleaseIdentity("supersedes_release_id", *release.SupersedesReleaseID); err != nil {
			return err
		}
		if *release.SupersedesReleaseID == release.ID {
			return fmt.Errorf("%w: a release cannot supersede itself", types.ErrProductionReleaseInvalid)
		}
	}
	if release.Status != "" && release.Status != types.ProductionReleaseBuilding {
		return types.ErrProductionReleaseLifecycle
	}
	if release.RetentionDays < 0 || release.RetentionDays > 3650 {
		return fmt.Errorf("%w: retention days are out of range", types.ErrProductionReleaseInvalid)
	}
	return nil
}

func (r *productionReleaseRepository) CreateRelease(
	ctx context.Context,
	release *types.ProductionRelease,
	targets []*types.ProductionReleaseTarget,
) error {
	if err := validateProductionReleaseForCreate(release, targets); err != nil {
		return err
	}
	actorID, err := trustedProductionReleaseActor(ctx, release.TenantID, release.CreatedBy)
	if err != nil {
		return err
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var version types.ProductionDocumentVersion
	if err := db.Where(
		"id = ? AND document_id = ? AND tenant_id = ? AND project_id = ?",
		release.VersionID, release.DocumentID, release.TenantID, release.ProjectID,
	).First(&version).Error; err != nil {
		return translateProductionReleaseError(err)
	}
	var review types.ProductionReviewRequest
	if err := db.Where(
		"id = ? AND tenant_id = ? AND project_id = ? AND document_id = ? AND version_id = ? AND status = ?",
		release.ReviewRequestID, release.TenantID, release.ProjectID, release.DocumentID,
		release.VersionID, types.ProductionReviewApproved,
	).First(&review).Error; err != nil {
		return translateProductionReleaseError(err)
	}
	authoritativeDigest, err := types.ComputeProductionReleaseDigestForVersion(
		types.ProductionReleaseDigestVersionCurrent, release, &version, &review,
	)
	if err != nil {
		return err
	}
	if release.ReleaseDigest != "" && release.ReleaseDigest != authoritativeDigest {
		return types.ErrProductionContentDigestMismatch
	}
	release.ReleaseDigestVersion = types.ProductionReleaseDigestVersionCurrent
	release.ReleaseDigest = authoritativeDigest

	now := r.nowUTC()
	if release.RetentionDays == 0 {
		release.RetentionDays = types.ProductionReleaseDefaultRetentionDays
	}
	release.Status = types.ProductionReleaseBuilding
	release.CreatedBy = actorID
	release.CreatedAt = now
	release.UpdatedAt = now

	seenIDs := make(map[string]struct{}, len(targets))
	seenKBs := make(map[string]struct{}, len(targets))
	seenKnowledgeIDs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil {
			return fmt.Errorf("%w: target is required", types.ErrProductionReleaseInvalid)
		}
		for name, value := range map[string]string{
			"target id": target.ID, "target knowledge base id": target.TargetKnowledgeBaseID,
			"target knowledge id": target.KnowledgeID,
		} {
			if err := requireProductionReleaseIdentity(name, value); err != nil {
				return err
			}
		}
		if _, exists := seenIDs[target.ID]; exists {
			return types.ErrProductionConflict
		}
		if _, exists := seenKBs[target.TargetKnowledgeBaseID]; exists {
			return types.ErrProductionConflict
		}
		if _, exists := seenKnowledgeIDs[target.KnowledgeID]; exists {
			return types.ErrProductionConflict
		}
		seenIDs[target.ID] = struct{}{}
		seenKBs[target.TargetKnowledgeBaseID] = struct{}{}
		seenKnowledgeIDs[target.KnowledgeID] = struct{}{}

		if (target.ReleaseID != "" && target.ReleaseID != release.ID) ||
			(target.TenantID != 0 && target.TenantID != release.TenantID) ||
			(target.ProjectID != "" && target.ProjectID != release.ProjectID) ||
			(target.DocumentID != "" && target.DocumentID != release.DocumentID) ||
			(target.VersionID != "" && target.VersionID != release.VersionID) ||
			(target.ReleaseDigest != "" && target.ReleaseDigest != release.ReleaseDigest) {
			return fmt.Errorf("%w: target scope does not match release", types.ErrProductionReleaseInvalid)
		}
		if target.Status != "" && target.Status != types.ReleaseTargetBuilding {
			return types.ErrProductionReleaseLifecycle
		}
		if target.RetentionDays < 0 || target.RetentionDays > 3650 ||
			target.FailureCode != "" || target.FailureReason != "" ||
			target.RecoveryAttemptedAt != nil ||
			target.RetentionUntil != nil || target.ActivatedAt != nil || target.FailedAt != nil ||
			target.RolledBackAt != nil || target.CleanupRequestedAt != nil || target.CleanedAt != nil {
			return fmt.Errorf("%w: target lifecycle fields are server-owned", types.ErrProductionReleaseInvalid)
		}
		canonicalConfig, configDigest, configErr := types.CanonicalProductionReleaseTargetConfig(target.ConfigSnapshot)
		if configErr != nil {
			return configErr
		}
		if target.ConfigDigest != "" && target.ConfigDigest != configDigest {
			return fmt.Errorf("%w: digest does not match canonical configuration", types.ErrProductionReleaseConfigInvalid)
		}
		target.ReleaseID = release.ID
		target.TenantID = release.TenantID
		target.ProjectID = release.ProjectID
		target.DocumentID = release.DocumentID
		target.VersionID = release.VersionID
		target.ReleaseDigest = release.ReleaseDigest
		target.ConfigSnapshot = canonicalConfig
		target.ConfigDigest = configDigest
		target.Status = types.ReleaseTargetBuilding
		target.FailureCode = ""
		target.FailureReason = ""
		if target.RetentionDays == 0 {
			target.RetentionDays = release.RetentionDays
		}
		target.CreatedAt = now
		target.UpdatedAt = now
	}

	err = database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		if createErr := db.Create(release).Error; createErr != nil {
			return translateProductionReleaseError(createErr)
		}
		for _, target := range targets {
			configValue := any(string(target.ConfigSnapshot))
			if db.Dialector.Name() == "postgres" {
				configValue = gorm.Expr("CAST(? AS JSONB)", string(target.ConfigSnapshot))
			}
			values := map[string]any{
				"id": target.ID, "release_id": target.ReleaseID, "tenant_id": target.TenantID,
				"project_id": target.ProjectID, "document_id": target.DocumentID, "version_id": target.VersionID,
				"target_knowledge_base_id": target.TargetKnowledgeBaseID, "knowledge_id": target.KnowledgeID,
				"release_digest": target.ReleaseDigest, "config_snapshot": configValue, "config_digest": target.ConfigDigest,
				"status": target.Status, "retention_days": target.RetentionDays,
				"failure_code": "", "failure_reason": "", "recovery_attempted_at": nil,
				"retention_until": nil, "activated_at": nil, "failed_at": nil, "rolled_back_at": nil,
				"cleanup_requested_at": nil, "cleaned_at": nil,
				"created_at": target.CreatedAt, "updated_at": target.UpdatedAt,
			}
			if createErr := db.Model(&types.ProductionReleaseTarget{}).Create(values).Error; createErr != nil {
				return translateProductionReleaseError(createErr)
			}
		}
		return nil
	})
	return err
}

func (r *productionReleaseRepository) GetTarget(ctx context.Context, tenantID uint64, targetID string) (*types.ProductionReleaseTarget, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return nil, err
	}
	var target types.ProductionReleaseTarget
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, targetID).First(&target).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	if err := normalizeProductionReleaseTargetConfig(&target); err != nil {
		return nil, err
	}
	return &target, nil
}

func (r *productionReleaseRepository) GetTargetForUpdate(
	ctx context.Context,
	tenantID uint64,
	targetID string,
) (*types.ProductionReleaseTarget, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return nil, err
	}
	var target types.ProductionReleaseTarget
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", tenantID, targetID).First(&target).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	if err := normalizeProductionReleaseTargetConfig(&target); err != nil {
		return nil, err
	}
	return &target, nil
}

// ClaimProjectionBuildGeneration is the first write of a projection build
// worker. Locking target before Knowledge matches retry/recovery lock order, so
// a stale generation can never race a lifecycle transition into mutating data.
func (r *productionReleaseRepository) ClaimProjectionBuildGeneration(
	ctx context.Context,
	targetID string,
	knowledgeID string,
	expectedUpdatedAt time.Time,
) (bool, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 || expectedUpdatedAt.IsZero() {
		return false, types.ErrProductionForbidden
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return false, err
	}
	if err := requireProductionReleaseIdentity("knowledge_id", knowledgeID); err != nil {
		return false, err
	}
	claimed := false
	err := database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		var target types.ProductionReleaseTarget
		loadErr := db.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(
				"tenant_id = ? AND id = ? AND knowledge_id = ? AND status = ? AND updated_at = ?",
				tenantID, targetID, knowledgeID, types.ReleaseTargetBuilding, expectedUpdatedAt,
			).
			Take(&target).Error
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if loadErr != nil {
			return translateProductionReleaseTargetReadError(loadErr)
		}
		result := db.Model(&types.Knowledge{}).
			Where(
				"tenant_id = ? AND id = ? AND knowledge_base_id = ? AND parse_status IN ?",
				tenantID, knowledgeID, target.TargetKnowledgeBaseID,
				[]string{types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFailed},
			).
			Updates(map[string]any{
				"parse_status":  types.ParseStatusProcessing,
				"error_message": "",
				"updated_at":    r.nowUTC(),
			})
		if result.Error != nil {
			return translateProductionReleaseError(result.Error)
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	return claimed, err
}

func (r *productionReleaseRepository) GetRelease(
	ctx context.Context, tenantID uint64, releaseID string,
) (*types.ProductionRelease, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("release_id", releaseID); err != nil {
		return nil, err
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var release types.ProductionRelease
	if err := db.Where("tenant_id = ? AND id = ?", tenantID, releaseID).First(&release).Error; err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	return loadProductionReleaseTargets(db, tenantID, &release)
}

func (r *productionReleaseRepository) ListReleases(
	ctx context.Context,
	tenantID uint64,
	documentID string,
	offset int,
	limit int,
) ([]*types.ProductionRelease, int64, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, 0, err
	}
	if err := requireProductionReleaseIdentity("document_id", documentID); err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > types.ProductionReleaseMaxTargets {
		return nil, 0, types.ErrProductionReleaseInvalid
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var total int64
	if err := db.Model(&types.ProductionRelease{}).
		Where("tenant_id = ? AND document_id = ?", tenantID, documentID).Count(&total).Error; err != nil {
		return nil, 0, translateProductionReleaseTargetReadError(err)
	}
	var releases []*types.ProductionRelease
	if err := db.Where("tenant_id = ? AND document_id = ?", tenantID, documentID).
		Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&releases).Error; err != nil {
		return nil, 0, translateProductionReleaseTargetReadError(err)
	}
	var heads []*types.ProductionProjectionHead
	if err := db.Where("tenant_id = ? AND document_id = ?", tenantID, documentID).Find(&heads).Error; err != nil {
		return nil, 0, translateProductionReleaseTargetReadError(err)
	}
	headByKB := make(map[string]*types.ProductionProjectionHead, len(heads))
	for _, head := range heads {
		headByKB[head.TargetKnowledgeBaseID] = head
	}
	for _, release := range releases {
		if _, err := loadProductionReleaseTargets(db, tenantID, release); err != nil {
			return nil, 0, err
		}
		for _, target := range release.Targets {
			if head := headByKB[target.TargetKnowledgeBaseID]; head != nil {
				target.HeadLockVersion = head.LockVersion
				target.IsActive = head.ActiveReleaseTargetID == target.ID
			}
		}
	}
	return releases, total, nil
}

func (r *productionReleaseRepository) GetLatestReleaseForVersion(
	ctx context.Context,
	tenantID uint64,
	documentID string,
	versionID string,
) (*types.ProductionRelease, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("document_id", documentID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("version_id", versionID); err != nil {
		return nil, err
	}
	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var release types.ProductionRelease
	err := db.Where(
		"production_releases.tenant_id = ? AND production_releases.document_id = ? AND production_releases.version_id = ? "+
			"AND NOT EXISTS (SELECT 1 FROM production_releases successor WHERE successor.supersedes_release_id = production_releases.id)",
		tenantID, documentID, versionID,
	).Order("production_releases.created_at DESC, production_releases.id DESC").First(&release).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	return loadProductionReleaseTargets(db, tenantID, &release)
}

func loadProductionReleaseTargets(
	db *gorm.DB,
	tenantID uint64,
	release *types.ProductionRelease,
) (*types.ProductionRelease, error) {
	if err := db.Where("tenant_id = ? AND release_id = ?", tenantID, release.ID).
		Order("id ASC").Find(&release.Targets).Error; err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	for _, target := range release.Targets {
		if err := normalizeProductionReleaseTargetConfig(target); err != nil {
			return nil, err
		}
	}
	return release, nil
}

// normalizeProductionReleaseTargetConfig authenticates driver-returned
// JSONB/TEXT before a target crosses the repository boundary.
func normalizeProductionReleaseTargetConfig(target *types.ProductionReleaseTarget) error {
	if target == nil {
		return fmt.Errorf("%w: persisted target is missing", types.ErrProductionReleaseConfigInvalid)
	}
	if len(target.ConfigSnapshot) == 0 {
		return fmt.Errorf("%w: persisted snapshot is missing", types.ErrProductionReleaseConfigInvalid)
	}
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(target.ConfigSnapshot)
	if err != nil {
		return err
	}
	if target.ConfigDigest != digest {
		return fmt.Errorf("%w: persisted digest does not match canonical configuration", types.ErrProductionReleaseConfigInvalid)
	}
	target.ConfigSnapshot = canonical
	return nil
}

func translateProductionReleaseTargetReadError(err error) error {
	if err == nil {
		return nil
	}
	var syntaxError *json.SyntaxError
	lower := strings.ToLower(err.Error())
	if errors.As(err, &syntaxError) ||
		(strings.Contains(lower, "config_snapshot") && strings.Contains(lower, "scan error")) {
		return fmt.Errorf("%w: persisted snapshot cannot be decoded", types.ErrProductionReleaseConfigInvalid)
	}
	return err
}

func (r *productionReleaseRepository) TransitionTarget(
	ctx context.Context,
	targetID string,
	from, to types.ProductionReleaseTargetStatus,
	patch types.JSONMap,
) (bool, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return false, types.ErrProductionForbidden
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return false, err
	}
	failureCode := ""
	failureReason := ""
	if to == types.ReleaseTargetFailed {
		failureCode = types.ProductionProjectionFailureBuildFailed
		failureReason = types.ProductionProjectionFailureReasonBuildFailed
		if len(patch) != 0 {
			code, codeOK := patch["failure_code"].(string)
			reason, reasonOK := patch["failure_reason"].(string)
			if len(patch) != 2 || !codeOK || !reasonOK ||
				code != types.ProductionProjectionFailureContentDigestMismatch ||
				reason != types.ProductionProjectionFailureReasonContentDigestMismatch {
				return false, types.ErrProductionReleasePatchInvalid
			}
			failureCode, failureReason = code, reason
		}
	} else if len(patch) != 0 {
		return false, types.ErrProductionReleasePatchInvalid
	}
	if !types.CanTransitionReleaseTarget(from, to) || to == types.ReleaseTargetActive || from == types.ReleaseTargetActive {
		return false, types.ErrProductionReleaseLifecycle
	}

	db := database.DBFromContext(ctx, r.db).WithContext(ctx)
	var currentGeneration struct {
		UpdatedAt time.Time
	}
	loadErr := db.Model(&types.ProductionReleaseTarget{}).
		Select("updated_at").
		Where("tenant_id = ? AND id = ? AND status = ?", tenantID, targetID, from).
		Take(&currentGeneration).Error
	if errors.Is(loadErr, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if loadErr != nil {
		return false, translateProductionReleaseError(loadErr)
	}
	lifecycleNow := r.nowUTC()
	nextUpdatedAt := lifecycleNow
	if !nextUpdatedAt.After(currentGeneration.UpdatedAt) {
		nextUpdatedAt = currentGeneration.UpdatedAt.UTC().Add(time.Second)
	}
	updates := map[string]any{"status": to, "updated_at": nextUpdatedAt}
	clearLifecycle := func() {
		updates["retention_until"] = nil
		updates["activated_at"] = nil
		updates["failed_at"] = nil
		updates["rolled_back_at"] = nil
		updates["cleanup_requested_at"] = nil
		updates["cleaned_at"] = nil
	}
	switch to {
	case types.ReleaseTargetBuilding, types.ReleaseTargetReady:
		clearLifecycle()
		updates["failure_code"] = ""
		updates["failure_reason"] = ""
	case types.ReleaseTargetFailed:
		clearLifecycle()
		updates["failure_code"] = failureCode
		updates["failure_reason"] = failureReason
		updates["failed_at"] = lifecycleNow
		updates["retention_until"] = r.retentionDeadlineExpression(lifecycleNow)
	case types.ReleaseTargetRolledBack:
		clearLifecycle()
		updates["rolled_back_at"] = lifecycleNow
		updates["retention_until"] = r.retentionDeadlineExpression(lifecycleNow)
	case types.ReleaseTargetCleanupPending:
		updates["activated_at"] = nil
		updates["cleanup_requested_at"] = lifecycleNow
		updates["cleaned_at"] = nil
	case types.ReleaseTargetCleaned:
		updates["activated_at"] = nil
		updates["cleaned_at"] = lifecycleNow
	}

	result := db.
		Model(&types.ProductionReleaseTarget{}).
		Where("tenant_id = ? AND id = ? AND status = ? AND updated_at = ?",
			tenantID, targetID, from, currentGeneration.UpdatedAt).
		Updates(updates)
	if result.Error != nil {
		return false, translateProductionReleaseError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *productionReleaseRepository) TransitionTargetForRetry(
	ctx context.Context,
	targetID string,
	from types.ProductionReleaseTargetStatus,
	expectedUpdatedAt time.Time,
) (*types.ProductionReleaseTarget, bool, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, false, types.ErrProductionForbidden
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return nil, false, err
	}
	if from != types.ReleaseTargetBuilding && from != types.ReleaseTargetFailed {
		return nil, false, types.ErrProductionReleaseLifecycle
	}
	nextGeneration := r.nowUTC()
	if !nextGeneration.After(expectedUpdatedAt) {
		nextGeneration = expectedUpdatedAt.UTC().Add(time.Second)
	}
	updates := map[string]any{
		"status":                types.ReleaseTargetBuilding,
		"failure_code":          "",
		"failure_reason":        "",
		"recovery_attempted_at": nil,
		"retention_until":       nil,
		"activated_at":          nil,
		"failed_at":             nil,
		"rolled_back_at":        nil,
		"cleanup_requested_at":  nil,
		"cleaned_at":            nil,
		"updated_at":            nextGeneration,
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionReleaseTarget{}).
		Where("tenant_id = ? AND id = ? AND status = ? AND updated_at = ?",
			tenantID, targetID, from, expectedUpdatedAt).
		Updates(updates)
	if result.Error != nil {
		return nil, false, translateProductionReleaseError(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, false, nil
	}
	target, err := r.GetTarget(ctx, tenantID, targetID)
	if err != nil {
		return nil, false, err
	}
	return target, true, nil
}

func (r *productionReleaseRepository) retentionDeadlineExpression(now time.Time) any {
	if r.db != nil && r.db.Dialector.Name() == "postgres" {
		return gorm.Expr("? + retention_days * INTERVAL '1 day'", now)
	}
	return gorm.Expr("datetime(?, '+' || retention_days || ' days')", now)
}

type productionScopeRow struct {
	TargetKnowledgeBaseID string
	KnowledgeID           string
	IsActive              bool
}

func (r *productionReleaseRepository) ResolveScopes(ctx context.Context, tenantID uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	result := make(map[string]types.ProductionKnowledgeScope, len(kbIDs))
	uniqueKBs := make([]string, 0, len(kbIDs))
	for _, kbID := range kbIDs {
		if strings.TrimSpace(kbID) == "" || kbID != strings.TrimSpace(kbID) {
			return nil, fmt.Errorf("%w: knowledge base id is required", types.ErrProductionReleaseInvalid)
		}
		if _, exists := result[kbID]; !exists {
			result[kbID] = types.ProductionKnowledgeScope{}
			uniqueKBs = append(uniqueKBs, kbID)
		}
	}
	if len(uniqueKBs) == 0 {
		return result, nil
	}

	var rows []productionScopeRow
	query := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_release_targets AS target").
		Select(`target.target_knowledge_base_id, target.knowledge_id,
			CASE WHEN head.active_release_target_id = target.id AND target.status = ? THEN TRUE ELSE FALSE END AS is_active`, types.ReleaseTargetActive).
		Joins(`LEFT JOIN production_projection_heads AS head
			ON head.tenant_id = target.tenant_id
			AND head.document_id = target.document_id
			AND head.target_knowledge_base_id = target.target_knowledge_base_id
			AND head.active_release_target_id = target.id`)
	query = query.Where("target.tenant_id = ? AND target.target_knowledge_base_id IN ?", tenantID, uniqueKBs)
	err := query.Order("target.target_knowledge_base_id ASC, target.knowledge_id ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		scope := result[row.TargetKnowledgeBaseID]
		scope.AllProductionKnowledgeIDs = append(scope.AllProductionKnowledgeIDs, row.KnowledgeID)
		if row.IsActive {
			scope.ActiveKnowledgeIDs = append(scope.ActiveKnowledgeIDs, row.KnowledgeID)
		} else {
			scope.InactiveKnowledgeIDs = append(scope.InactiveKnowledgeIDs, row.KnowledgeID)
		}
		result[row.TargetKnowledgeBaseID] = scope
	}
	for kbID, scope := range result {
		sort.Strings(scope.ActiveKnowledgeIDs)
		sort.Strings(scope.InactiveKnowledgeIDs)
		sort.Strings(scope.AllProductionKnowledgeIDs)
		result[kbID] = scope
	}
	return result, nil
}

// ResolveScopesForKnowledgeIDs resolves only the requested production
// projections. Explicit-ID authorization must never scan a tenant's complete
// projection history just to reject one stale ID.
func (r *productionReleaseRepository) ResolveScopesForKnowledgeIDs(ctx context.Context, tenantID uint64, knowledgeIDs []string) (map[string]types.ProductionKnowledgeScope, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	uniqueIDs := make([]string, 0, len(knowledgeIDs))
	seen := make(map[string]struct{}, len(knowledgeIDs))
	for _, knowledgeID := range knowledgeIDs {
		if strings.TrimSpace(knowledgeID) == "" || knowledgeID != strings.TrimSpace(knowledgeID) {
			return nil, fmt.Errorf("%w: knowledge id is required", types.ErrProductionReleaseInvalid)
		}
		if _, ok := seen[knowledgeID]; !ok {
			seen[knowledgeID] = struct{}{}
			uniqueIDs = append(uniqueIDs, knowledgeID)
		}
	}
	result := make(map[string]types.ProductionKnowledgeScope)
	if len(uniqueIDs) == 0 {
		return result, nil
	}
	var rows []productionScopeRow
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_release_targets AS target").
		Select(`target.target_knowledge_base_id, target.knowledge_id,
			CASE WHEN head.active_release_target_id = target.id AND target.status = ? THEN TRUE ELSE FALSE END AS is_active`, types.ReleaseTargetActive).
		Joins(`LEFT JOIN production_projection_heads AS head
			ON head.tenant_id = target.tenant_id
			AND head.document_id = target.document_id
			AND head.target_knowledge_base_id = target.target_knowledge_base_id
			AND head.active_release_target_id = target.id`).
		Where("target.tenant_id = ? AND target.knowledge_id IN ?", tenantID, uniqueIDs).
		Order("target.target_knowledge_base_id ASC, target.knowledge_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		scope := result[row.TargetKnowledgeBaseID]
		scope.AllProductionKnowledgeIDs = append(scope.AllProductionKnowledgeIDs, row.KnowledgeID)
		if row.IsActive {
			scope.ActiveKnowledgeIDs = append(scope.ActiveKnowledgeIDs, row.KnowledgeID)
		} else {
			scope.InactiveKnowledgeIDs = append(scope.InactiveKnowledgeIDs, row.KnowledgeID)
		}
		result[row.TargetKnowledgeBaseID] = scope
	}
	for kbID, scope := range result {
		sort.Strings(scope.ActiveKnowledgeIDs)
		sort.Strings(scope.InactiveKnowledgeIDs)
		sort.Strings(scope.AllProductionKnowledgeIDs)
		result[kbID] = scope
	}
	return result, nil
}

func (r *productionReleaseRepository) SwitchHead(
	ctx context.Context,
	tenantID uint64,
	documentID, kbID, targetID string,
	expectedLock int,
) (*types.ProductionProjectionHead, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	for name, value := range map[string]string{"document_id": documentID, "knowledge_base_id": kbID, "target_id": targetID} {
		if err := requireProductionReleaseIdentity(name, value); err != nil {
			return nil, err
		}
	}
	if expectedLock < 0 {
		return nil, types.ErrProductionProjectionConflict
	}

	var head types.ProductionProjectionHead
	err := database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		db := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		now := r.nowUTC()
		if expectedLock == 0 {
			head = types.ProductionProjectionHead{
				TenantID: tenantID, DocumentID: documentID, TargetKnowledgeBaseID: kbID,
				ActiveReleaseTargetID: targetID, LockVersion: 1, UpdatedAt: now,
			}
			if createErr := db.Create(&head).Error; createErr != nil {
				if isProductionDuplicateKey(createErr) {
					return errors.Join(types.ErrProductionProjectionConflict, createErr)
				}
				return translateProductionReleaseError(createErr)
			}
		} else {
			var update *gorm.DB
			if db.Dialector.Name() == "postgres" {
				update = db.Exec(postgresProductionProjectionHeadCASSQL,
					targetID, now, tenantID, documentID, kbID, expectedLock)
			} else {
				update = db.Model(&types.ProductionProjectionHead{}).
					Where("tenant_id = ? AND document_id = ? AND target_knowledge_base_id = ? AND lock_version = ?",
						tenantID, documentID, kbID, expectedLock).
					Updates(map[string]any{
						"active_release_target_id": targetID,
						"lock_version":             gorm.Expr("lock_version + 1"),
						"updated_at":               now,
					})
			}
			if update.Error != nil {
				return translateProductionReleaseError(update.Error)
			}
			if update.RowsAffected != 1 {
				return types.ErrProductionProjectionConflict
			}
		}
		return db.Where("tenant_id = ? AND document_id = ? AND target_knowledge_base_id = ?",
			tenantID, documentID, kbID).First(&head).Error
	})
	if err != nil {
		return nil, err
	}
	return &head, nil
}

func (r *productionReleaseRepository) ListProjectionHistory(
	ctx context.Context,
	tenantID uint64,
	documentID, kbID string,
) ([]*types.ProductionReleaseTarget, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("document_id", documentID); err != nil {
		return nil, err
	}
	if err := requireProductionReleaseIdentity("knowledge_base_id", kbID); err != nil {
		return nil, err
	}
	var targets []*types.ProductionReleaseTarget
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND document_id = ? AND target_knowledge_base_id = ?", tenantID, documentID, kbID).
		Order("created_at DESC, id DESC").Find(&targets).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	for _, target := range targets {
		if err := normalizeProductionReleaseTargetConfig(target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (r *productionReleaseRepository) ListCleanupEligible(
	ctx context.Context,
	tenantID uint64,
	limit int,
) ([]*types.ProductionReleaseTarget, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var targets []*types.ProductionReleaseTarget
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Where("tenant_id = ? AND status IN ? AND retention_until IS NOT NULL AND retention_until <= ?",
			tenantID, []types.ProductionReleaseTargetStatus{
				types.ReleaseTargetFailed,
				types.ReleaseTargetRolledBack,
				types.ReleaseTargetCleanupPending,
			}, r.nowUTC()).
		Where(`NOT EXISTS (
			SELECT 1 FROM production_projection_heads AS head
			WHERE head.tenant_id = production_release_targets.tenant_id
			  AND head.document_id = production_release_targets.document_id
			  AND head.target_knowledge_base_id = production_release_targets.target_knowledge_base_id
			  AND head.active_release_target_id = production_release_targets.id
		)`).
		Order("retention_until ASC, id ASC").Limit(limit).Find(&targets).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	for _, target := range targets {
		if err := normalizeProductionReleaseTargetConfig(target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (r *productionReleaseRepository) ListBuildingTargetsWithFailedKnowledge(
	ctx context.Context,
	tenantID uint64,
	limit int,
) ([]*types.ProductionReleaseTarget, error) {
	if err := requireProductionReleaseTenantContext(ctx, tenantID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var targets []*types.ProductionReleaseTarget
	err := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Table("production_release_targets AS target").
		Select("target.*").
		Joins(`JOIN knowledges AS knowledge
			ON knowledge.id = target.knowledge_id
			AND knowledge.tenant_id = target.tenant_id
			AND knowledge.knowledge_base_id = target.target_knowledge_base_id`).
		Where("target.tenant_id = ? AND target.status = ?", tenantID, types.ReleaseTargetBuilding).
		Where("knowledge.parse_status = ? AND knowledge.deleted_at IS NULL", types.ParseStatusFailed).
		Order("target.recovery_attempted_at ASC NULLS FIRST, target.id ASC").
		Limit(limit).
		Find(&targets).Error
	if err != nil {
		return nil, translateProductionReleaseTargetReadError(err)
	}
	for _, target := range targets {
		if err := normalizeProductionReleaseTargetConfig(target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (r *productionReleaseRepository) DeferProjectionFailureRecovery(
	ctx context.Context,
	targetID string,
	expectedUpdatedAt time.Time,
) (bool, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return false, types.ErrProductionForbidden
	}
	if err := requireProductionReleaseIdentity("target_id", targetID); err != nil {
		return false, err
	}
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.ProductionReleaseTarget{}).
		Where("tenant_id = ? AND id = ? AND status = ? AND updated_at = ?",
			tenantID, targetID, types.ReleaseTargetBuilding, expectedUpdatedAt).
		Update("recovery_attempted_at", r.nowUTC())
	if result.Error != nil {
		return false, translateProductionReleaseError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func translateProductionReleaseError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "config_snapshot"), strings.Contains(lower, "config_digest"):
		return errors.Join(types.ErrProductionReleaseConfigInvalid, err)
	case strings.Contains(lower, "production projection head updates require cas lock versions"):
		return errors.Join(types.ErrProductionProjectionConflict, err)
	case strings.Contains(lower, "invalid production release target status transition"),
		strings.Contains(lower, "production release targets require"),
		strings.Contains(lower, "production release target retention has not expired"),
		strings.Contains(lower, "active projection heads prevent independent target deactivation"),
		strings.Contains(lower, "production projection heads require ready or active targets"),
		strings.Contains(lower, "production projection heads must activate selected targets"):
		return errors.Join(types.ErrProductionReleaseLifecycle, err)
	case strings.Contains(lower, "production releases require an approved review"),
		strings.Contains(lower, "production releases must be created building"),
		strings.Contains(lower, "production release targets must be created building"):
		return errors.Join(types.ErrProductionReleaseInvalid, err)
	default:
		return translateProductionWriteError(err)
	}
}

var _ interfaces.ProductionReleaseRepository = (*productionReleaseRepository)(nil)
