package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTenantNotFound           = errors.New("tenant not found")
	ErrTenantNotProvisioning    = errors.New("tenant is not awaiting activation")
	ErrProvisioningUserMismatch = errors.New("registration user does not belong to tenant")
	ErrTenantHasKnowledgeBase   = errors.New("tenant has associated knowledge bases")
)

// tenantRepository implements tenant repository interface
type tenantRepository struct {
	db *gorm.DB
}

// NewTenantRepository creates a new tenant repository
func NewTenantRepository(db *gorm.DB) interfaces.TenantRepository {
	return &tenantRepository{db: db}
}

// CreateTenant creates tenant
func (r *tenantRepository) CreateTenant(ctx context.Context, tenant *types.Tenant) error {
	return database.DBFromContext(ctx, r.db).WithContext(ctx).Create(tenant).Error
}

func (r *tenantRepository) ActivateProvisionedTenant(ctx context.Context, id uint64) error {
	result := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.Tenant{}).
		Where("id = ? AND status = ?", id, types.TenantStatusProvisioning).
		Updates(map[string]any{
			"status":     types.TenantStatusActive,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTenantNotProvisioning
	}
	return nil
}

// GetTenantByID gets tenant by ID
func (r *tenantRepository) GetTenantByID(ctx context.Context, id uint64) (*types.Tenant, error) {
	var tenant types.Tenant
	if err := database.DBFromContext(ctx, r.db).WithContext(ctx).Where("id = ?", id).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	return &tenant, nil
}

// GetTenantsByIDs batches GetTenantByID with a single IN-list query.
// Returns a map keyed by tenant ID; missing rows are simply absent from
// the map (no error). An empty input slice short-circuits to an empty map
// without hitting the database.
func (r *tenantRepository) GetTenantsByIDs(ctx context.Context, ids []uint64) (map[uint64]*types.Tenant, error) {
	if len(ids) == 0 {
		return map[uint64]*types.Tenant{}, nil
	}
	var tenants []*types.Tenant
	if err := database.DBFromContext(ctx, r.db).WithContext(ctx).Where("id IN ?", ids).Find(&tenants).Error; err != nil {
		return nil, err
	}
	out := make(map[uint64]*types.Tenant, len(tenants))
	for _, t := range tenants {
		if t != nil {
			out[t.ID] = t
		}
	}
	return out, nil
}

// ListTenants lists all tenants
func (r *tenantRepository) ListTenants(ctx context.Context) ([]*types.Tenant, error) {
	var tenants []*types.Tenant
	if err := database.DBFromContext(ctx, r.db).WithContext(ctx).Order("created_at DESC").Find(&tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
}

// SearchTenants searches tenants with pagination and filters
func (r *tenantRepository) SearchTenants(ctx context.Context, keyword string, tenantID uint64, page, pageSize int) ([]*types.Tenant, int64, error) {
	var tenants []*types.Tenant
	var total int64

	query := database.DBFromContext(ctx, r.db).WithContext(ctx).Model(&types.Tenant{})

	// Build search conditions
	if tenantID > 0 && keyword != "" {
		escaped := escapeLikeKeyword(keyword)
		query = query.Where("id = ? OR name LIKE ? OR description LIKE ?", tenantID, "%"+escaped+"%", "%"+escaped+"%")
	} else if tenantID > 0 {
		query = query.Where("id = ?", tenantID)
	} else if keyword != "" {
		escaped := escapeLikeKeyword(keyword)
		query = query.Where("name LIKE ? OR description LIKE ?", "%"+escaped+"%", "%"+escaped+"%")
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Apply pagination
	if page > 0 && pageSize > 0 {
		offset := (page - 1) * pageSize
		query = query.Offset(offset).Limit(pageSize)
	}

	// Order by created_at DESC
	query = query.Order("created_at DESC")

	// Execute query
	if err := query.Find(&tenants).Error; err != nil {
		return nil, 0, err
	}

	return tenants, total, nil
}

// UpdateTenant updates tenant.
func (r *tenantRepository) UpdateTenant(ctx context.Context, tenant *types.Tenant) error {
	return database.DBFromContext(ctx, r.db).WithContext(ctx).Model(&types.Tenant{}).Where("id = ?", tenant.ID).Updates(tenant).Error
}

// DeleteTenant soft-deletes the tenant and every active membership row
// for that tenant in one transaction. Without the membership purge,
// /auth/me still lists the defunct tenant (name lookup fails → UI shows
// "#<id>").
func (r *tenantRepository) DeleteTenant(ctx context.Context, id uint64) error {
	return database.DBFromContext(ctx, r.db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ?", id).Delete(&types.TenantMember{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&types.Tenant{}).Error
	})
}

// PurgeProvisionedTenant permanently removes the rows created while
// provisioning a workspace that never became externally usable. It accepts
// only the tenant ID so storage credential snapshots are neither loaded nor
// exposed during compensation. Normal workspace deletion uses DeleteTenant.
func (r *tenantRepository) PurgeProvisionedTenant(ctx context.Context, id uint64) error {
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		tx := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		if err := tx.Unscoped().Where("tenant_id = ?", id).Delete(&types.ProductionDocumentType{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("tenant_id = ?", id).Delete(&types.StorageBackend{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("tenant_id = ?", id).Delete(&types.TenantMember{}).Error; err != nil {
			return err
		}
		result := tx.Unscoped().Where("id = ? AND status = ?", id, types.TenantStatusProvisioning).
			Delete(&types.Tenant{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTenantNotProvisioning
		}
		return nil
	})
}

// PurgeProvisionedRegistration compensates failures after the registration
// user is persisted. Every hard delete shares one transaction, and the final
// status-qualified tenant delete prevents cleanup from winning over activation.
func (r *tenantRepository) PurgeProvisionedRegistration(ctx context.Context, tenantID uint64, userID string) error {
	return database.WithTransactionContext(ctx, r.db, func(txCtx context.Context) error {
		tx := database.DBFromContext(txCtx, r.db).WithContext(txCtx)
		locking := func(query *gorm.DB) *gorm.DB {
			switch tx.Dialector.Name() {
			case "postgres", "mysql":
				return query.Clauses(clause.Locking{Strength: "UPDATE"})
			default:
				return query
			}
		}
		var tenantState struct {
			Status string `gorm:"column:status"`
		}
		result := locking(tx.Unscoped().Model(&types.Tenant{}).Select("status")).
			Where("id = ?", tenantID).Take(&tenantState)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrTenantNotProvisioning
		}
		if result.Error != nil {
			return result.Error
		}
		if tenantState.Status != types.TenantStatusProvisioning {
			return ErrTenantNotProvisioning
		}
		var userState struct {
			ID string `gorm:"column:id"`
		}
		result = tx.Unscoped().Model(&types.User{}).Select("id").
			Where("id = ? AND tenant_id = ?", userID, tenantID).Take(&userState)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrProvisioningUserMismatch
		}
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Unscoped().Where("tenant_id = ?", tenantID).Delete(&types.ProductionDocumentType{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("tenant_id = ?", tenantID).Delete(&types.StorageBackend{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("tenant_id = ?", tenantID).Delete(&types.TenantMember{}).Error; err != nil {
			return err
		}
		userResult := tx.Unscoped().Where("id = ? AND tenant_id = ?", userID, tenantID).Delete(&types.User{})
		if userResult.Error != nil {
			return userResult.Error
		}
		if userResult.RowsAffected != 1 {
			return ErrProvisioningUserMismatch
		}
		tenantResult := tx.Unscoped().Where("id = ? AND status = ?", tenantID, types.TenantStatusProvisioning).
			Delete(&types.Tenant{})
		if tenantResult.Error != nil {
			return tenantResult.Error
		}
		if tenantResult.RowsAffected != 1 {
			return ErrTenantNotProvisioning
		}
		return nil
	})
}

func (r *tenantRepository) AdjustStorageUsed(ctx context.Context, tenantID uint64, delta int64) error {
	return database.DBFromContext(ctx, r.db).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tenant types.Tenant
		// 使用悲观锁确保并发安全
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tenant, tenantID).Error; err != nil {
			return err
		}

		tenant.StorageUsed += delta
		// 保存更新并验证业务规则
		if tenant.StorageUsed < 0 {
			logger.Errorf(ctx, "tenant storage used is negative %d: %d", tenant.ID, tenant.StorageUsed)
			tenant.StorageUsed = 0
		}

		return tx.Save(&tenant).Error
	})
}

// BulkSetStorageQuota writes quotaBytes to storage_quota for every
// tenant in one statement. We don't WHERE-filter (the action is
// "apply globally"), so the affected count equals the row count of
// the tenants table.
//
// No transaction here: the operation is a single statement and we
// don't want to hold a long lock just to update a single column. If
// a concurrent CreateTenant lands in the middle, the new row gets
// the new default via the system-setting resolver in the handler —
// no risk of the new tenant being skipped.
func (r *tenantRepository) BulkSetStorageQuota(ctx context.Context, quotaBytes int64) (int64, error) {
	res := database.DBFromContext(ctx, r.db).WithContext(ctx).
		Model(&types.Tenant{}).
		Where("1 = 1"). // GORM refuses unconditional UPDATEs without an explicit WHERE
		Update("storage_quota", quotaBytes)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
