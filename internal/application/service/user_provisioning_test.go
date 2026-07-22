package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type provisioningUserRepo struct {
	interfaces.UserRepository
	created       *types.User
	updatedTenant uint64
	createErr     error
	deleteCalls   int
}

func (r *provisioningUserRepo) GetUserByEmail(context.Context, string) (*types.User, error) {
	return nil, nil
}

func (r *provisioningUserRepo) GetUserByUsername(context.Context, string) (*types.User, error) {
	return nil, nil
}

func (r *provisioningUserRepo) CreateUser(_ context.Context, user *types.User) error {
	if r.createErr != nil {
		return r.createErr
	}
	copy := *user
	r.created = &copy
	return nil
}

func (r *provisioningUserRepo) UpdateUser(_ context.Context, user *types.User) error {
	r.updatedTenant = user.TenantID
	return nil
}

func (r *provisioningUserRepo) DeleteUser(context.Context, string) error {
	r.deleteCalls++
	return nil
}

type provisioningTenantService struct {
	interfaces.TenantService
	createCalls int
	deleteCalls int
	purgeCalls  int
}

func (s *provisioningTenantService) CreateTenant(context.Context, *types.Tenant) (*types.Tenant, error) {
	s.createCalls++
	return &types.Tenant{ID: 99}, nil
}

func (s *provisioningTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: id}, nil
}

func (s *provisioningTenantService) DeleteTenant(context.Context, uint64) error {
	s.deleteCalls++
	return nil
}

func (s *provisioningTenantService) PurgeProvisionedTenant(context.Context, uint64) error {
	s.purgeCalls++
	return nil
}

type provisioningMemberService struct {
	interfaces.TenantMemberService
	members   []*types.TenantMember
	ensureErr error
}

func (s *provisioningMemberService) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	return s.members, nil
}

func (s *provisioningMemberService) EnsureOwner(context.Context, string, uint64) (*types.TenantMember, error) {
	if s.ensureErr != nil {
		return nil, s.ensureErr
	}
	return &types.TenantMember{Role: types.TenantRoleOwner}, nil
}

func TestUserServiceRegisterTenantlessSkipsTenantCreation(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{}
	svc := &userService{userRepo: repo, tenantService: tenantSvc}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username:           "alice",
		Email:              "alice@example.com",
		Password:           "supersecret",
		TenantProvisioning: types.TenantProvisioningTenantless,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if tenantSvc.createCalls != 0 {
		t.Fatalf("tenant create calls = %d, want 0", tenantSvc.createCalls)
	}
	if user.TenantID != 0 || repo.created == nil || repo.created.TenantID != 0 {
		t.Fatalf("tenantless user persisted with tenant: user=%d created=%v", user.TenantID, repo.created)
	}
}

func TestResolveLoginTenantIDRepairsTenantlessUserWithMembership(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{}
	memberSvc := &provisioningMemberService{members: []*types.TenantMember{
		{TenantID: 42, Status: types.TenantMemberStatusActive},
	}}
	svc := &userService{userRepo: repo, tenantService: tenantSvc, memberService: memberSvc}
	user := &types.User{ID: "alice", TenantID: 0}

	if got := svc.resolveLoginTenantID(context.Background(), user); got != 42 {
		t.Fatalf("resolved tenant = %d, want 42", got)
	}
	if repo.updatedTenant != 42 || user.TenantID != 42 {
		t.Fatalf("repair was not persisted: repo=%d user=%d", repo.updatedTenant, user.TenantID)
	}
}

func TestUserServiceRegisterPurgesProvisionedTenantWhenUserCreateFails(t *testing.T) {
	repo := &provisioningUserRepo{createErr: errors.New("create user failed")}
	tenantSvc := &provisioningTenantService{}
	svc := &userService{userRepo: repo, tenantService: tenantSvc}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username: "alice", Email: "alice@example.com", Password: "supersecret",
		TenantProvisioning: types.TenantProvisioningCreatePersonal,
	})

	if err == nil || user != nil {
		t.Fatalf("Register = (%v, %v), want failure", user, err)
	}
	if tenantSvc.purgeCalls != 1 || tenantSvc.deleteCalls != 0 {
		t.Fatalf("rollback calls: purge=%d delete=%d, want 1/0", tenantSvc.purgeCalls, tenantSvc.deleteCalls)
	}
}

func TestUserServiceRegisterPurgesProvisionedTenantWhenOwnerFinalizationFails(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{}
	members := &provisioningMemberService{ensureErr: errors.New("ensure owner failed")}
	svc := &userService{userRepo: repo, tenantService: tenantSvc, memberService: members}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username: "alice", Email: "alice@example.com", Password: "supersecret",
		TenantProvisioning: types.TenantProvisioningCreatePersonal,
	})

	if err == nil || user != nil {
		t.Fatalf("Register = (%v, %v), want failure", user, err)
	}
	if repo.deleteCalls != 1 {
		t.Fatalf("DeleteUser calls = %d, want 1", repo.deleteCalls)
	}
	if tenantSvc.purgeCalls != 1 || tenantSvc.deleteCalls != 0 {
		t.Fatalf("rollback calls: purge=%d delete=%d, want 1/0", tenantSvc.purgeCalls, tenantSvc.deleteCalls)
	}
}
