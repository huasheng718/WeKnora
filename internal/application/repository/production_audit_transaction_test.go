package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func prepareProductionAuditTransactionDB(t *testing.T) (*productionIdempotencyRepository, *auditLogRepository) {
	t.Helper()
	repo, db := newProductionIdempotencyRepoTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	return repo.(*productionIdempotencyRepository), NewAuditLogRepository(db).(*auditLogRepository)
}

func TestProductionAuditRepositoryJoinsContextTransactionAndRollsBack(t *testing.T) {
	idempotencyRepo, auditRepo := prepareProductionAuditTransactionDB(t)
	ctx, cancel := context.WithTimeout(productionTenantContext(7), 250*time.Millisecond)
	defer cancel()
	forceRollback := errors.New("force outer rollback")

	err := idempotencyRepo.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := auditRepo.Create(txCtx, &types.AuditLog{
			TenantID: 7, Action: types.AuditActionProductionProjectCreated,
			TargetType: "production_project", TargetID: "project-1",
		}); err != nil {
			return fmt.Errorf("audit create inside transaction: %w", err)
		}
		rows, err := auditRepo.List(txCtx, 7, nil)
		if err != nil {
			return fmt.Errorf("audit list inside transaction: %w", err)
		}
		if len(rows) != 1 {
			return fmt.Errorf("audit row not visible inside transaction: got %d", len(rows))
		}
		return forceRollback
	})

	require.ErrorIs(t, err, forceRollback)
	var count int64
	require.NoError(t, auditRepo.db.Model(&types.AuditLog{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionAuditRepositoryFailureUsesSavepoint(t *testing.T) {
	idempotencyRepo, auditRepo := prepareProductionAuditTransactionDB(t)
	db := auditRepo.db
	require.NoError(t, db.Exec(`
CREATE TRIGGER fail_production_audit_insert
BEFORE INSERT ON audit_logs
BEGIN
    SELECT RAISE(ABORT, 'forced audit failure');
END`).Error)
	projectRepo := NewProductionProjectRepository(db)
	ctx, cancel := context.WithTimeout(productionTenantContext(7), 250*time.Millisecond)
	defer cancel()

	err := idempotencyRepo.WithinTransaction(ctx, func(txCtx context.Context) error {
		auditErr := auditRepo.Create(txCtx, &types.AuditLog{
			TenantID: 7, Action: types.AuditActionProductionProjectCreated,
		})
		if auditErr == nil || !strings.Contains(auditErr.Error(), "forced audit failure") {
			return fmt.Errorf("expected isolated audit failure, got %v", auditErr)
		}
		return projectRepo.Create(txCtx, &types.ProductionProject{
			ID: "project-1", TenantID: 7, Name: "Foundation", OwnerUserID: "author-1",
			Status: types.ProductionProjectActive,
		}, &types.ProductionProjectMember{AssignedBy: "author-1"})
	})

	require.NoError(t, err)
	var projectCount, auditCount int64
	require.NoError(t, db.Model(&types.ProductionProject{}).Count(&projectCount).Error)
	require.NoError(t, db.Model(&types.AuditLog{}).Count(&auditCount).Error)
	require.Equal(t, int64(1), projectCount)
	require.Zero(t, auditCount)
}
