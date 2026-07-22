package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type activationOrderTenantRepository struct {
	interfaces.TenantRepository
	activated bool
	getCalls  int
}

func (r *activationOrderTenantRepository) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	r.getCalls++
	if r.activated {
		return nil, errors.New("post-activation reads are fatal finalization work")
	}
	return &types.Tenant{ID: 42, Name: "pending", Status: types.TenantStatusProvisioning}, nil
}

func (r *activationOrderTenantRepository) ActivateProvisionedTenant(context.Context, uint64) error {
	r.activated = true
	return nil
}

func TestActivateProvisionedTenantDoesNotReadAfterCAS(t *testing.T) {
	repo := &activationOrderTenantRepository{}
	svc := &tenantService{repo: repo}

	activated, err := svc.ActivateProvisionedTenant(context.Background(), 42)
	if err != nil {
		t.Fatalf("ActivateProvisionedTenant: %v", err)
	}
	if activated == nil || activated.Status != types.TenantStatusActive {
		t.Fatalf("activated tenant = %+v, want active", activated)
	}
	if repo.getCalls != 1 {
		t.Fatalf("repository reads = %d, want exactly one pre-CAS read", repo.getCalls)
	}
}
