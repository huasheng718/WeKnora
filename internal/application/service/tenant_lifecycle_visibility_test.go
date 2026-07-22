package service

import (
	"context"
	"net/http"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type tenantLifecycleRepository struct {
	interfaces.TenantRepository
	tenants     []*types.Tenant
	deleteErr   error
	updateCalls int
}

func (r *tenantLifecycleRepository) ListTenants(context.Context) ([]*types.Tenant, error) {
	return r.tenants, nil
}

func (r *tenantLifecycleRepository) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return r.tenants[0], nil
}

func (r *tenantLifecycleRepository) UpdateTenant(context.Context, *types.Tenant) error {
	r.updateCalls++
	return nil
}

func (r *tenantLifecycleRepository) DeleteTenant(context.Context, uint64) error {
	return r.deleteErr
}

func newTenantLifecycleBoundaryService(repo interfaces.TenantRepository) interfaces.TenantService {
	return NewTenantService(repo, nil, nil, nil, nil)
}

func TestTenantListBoundariesExcludeProvisioningRows(t *testing.T) {
	repo := &tenantLifecycleRepository{tenants: []*types.Tenant{
		{ID: 1, Name: "active", Status: types.TenantStatusActive},
		{ID: 2, Name: "pending", Status: types.TenantStatusProvisioning},
	}}
	svc := newTenantLifecycleBoundaryService(repo)

	for name, list := range map[string]func(context.Context) ([]*types.Tenant, error){
		"tenant list":     svc.ListTenants,
		"all tenant list": svc.ListAllTenants,
	} {
		t.Run(name, func(t *testing.T) {
			tenants, err := list(context.Background())
			require.NoError(t, err)
			require.Len(t, tenants, 1)
			require.Equal(t, uint64(1), tenants[0].ID)
			require.Equal(t, types.TenantStatusActive, tenants[0].Status)
		})
	}
}

func TestUpdateTenantRejectsProvisioningBeforeRepositoryWrite(t *testing.T) {
	repo := &tenantLifecycleRepository{tenants: []*types.Tenant{
		{ID: 2, Name: "pending", Status: types.TenantStatusProvisioning},
	}}
	svc := newTenantLifecycleBoundaryService(repo)

	updated, err := svc.UpdateTenant(context.Background(), repo.tenants[0])
	require.Nil(t, updated)
	require.Zero(t, repo.updateCalls)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, appErr.HTTPCode)
	require.NotContains(t, appErr.Message, "provision")
	require.NotContains(t, appErr.Message, "active")
}

func TestDeleteTenantMapsInactiveStateToGenericConflict(t *testing.T) {
	repo := &tenantLifecycleRepository{
		tenants:   []*types.Tenant{{ID: 2, Name: "pending", Status: types.TenantStatusProvisioning}},
		deleteErr: apprepo.ErrTenantNotActive,
	}
	svc := newTenantLifecycleBoundaryService(repo)

	err := svc.DeleteTenant(context.Background(), 2)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, appErr.HTTPCode)
	require.NotContains(t, appErr.Message, "provision")
	require.NotContains(t, appErr.Message, "active")
}

func TestWeKnoraCloudCredentialsRejectProvisioningBeforeRepositoryWrite(t *testing.T) {
	repo := &tenantLifecycleRepository{tenants: []*types.Tenant{
		{ID: 2, Name: "pending", Status: types.TenantStatusProvisioning},
	}}
	svc := &weKnoraCloudService{tenantRepo: repo}

	err := svc.updateTenantCredentials(context.Background(), 2, "app-id", "app-secret")
	require.Zero(t, repo.updateCalls)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, appErr.HTTPCode)
	require.NotContains(t, appErr.Message, "provision")
	require.NotContains(t, appErr.Message, "active")
}
