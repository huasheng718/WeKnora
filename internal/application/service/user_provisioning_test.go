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
	createCalls   int
	activateCalls int
	deleteCalls   int
	purgeCalls    int
	activateErr   error
	unavailable   map[uint64]bool
}

func (s *provisioningTenantService) CreateTenant(context.Context, *types.Tenant) (*types.Tenant, error) {
	s.createCalls++
	return &types.Tenant{ID: 99, Name: "personal", Status: types.TenantStatusProvisioning}, nil
}

func (s *provisioningTenantService) ActivateProvisionedTenant(context.Context, uint64) (*types.Tenant, error) {
	s.activateCalls++
	if s.activateErr != nil {
		return nil, s.activateErr
	}
	return &types.Tenant{ID: 99, Name: "personal", Status: types.TenantStatusActive}, nil
}

func (s *provisioningTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if s.unavailable[id] {
		return nil, errors.New("tenant is not active")
	}
	return &types.Tenant{ID: id, Name: "active", Status: types.TenantStatusActive}, nil
}

func (s *provisioningTenantService) GetTenantsByIDs(_ context.Context, ids []uint64) (map[uint64]*types.Tenant, error) {
	tenants := make(map[uint64]*types.Tenant, len(ids))
	for _, id := range ids {
		if !s.unavailable[id] {
			tenants[id] = &types.Tenant{ID: id, Name: "active", Status: types.TenantStatusActive}
		}
	}
	return tenants, nil
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

func (s *provisioningMemberService) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	for _, member := range s.members {
		if member != nil && member.UserID == userID && member.TenantID == tenantID {
			return member, nil
		}
	}
	return nil, nil
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

func TestBuildLoginMembershipsOmitsPendingHomeTenant(t *testing.T) {
	tenantSvc := &provisioningTenantService{unavailable: map[uint64]bool{42: true}}
	memberSvc := &provisioningMemberService{members: []*types.TenantMember{
		{TenantID: 42, Status: types.TenantMemberStatusActive, Role: types.TenantRoleOwner},
	}}
	svc := &userService{tenantService: tenantSvc, memberService: memberSvc}

	memberships := svc.BuildLoginMemberships(
		context.Background(),
		&types.User{ID: "alice", TenantID: 42},
		nil,
	)

	if len(memberships) != 0 {
		t.Fatalf("memberships = %+v, want empty while tenant is pending", memberships)
	}
}

func TestResolveLoginTenantIDRejectsPendingHomeTenant(t *testing.T) {
	tenantSvc := &provisioningTenantService{unavailable: map[uint64]bool{42: true}}
	svc := &userService{tenantService: tenantSvc}

	if got := svc.resolveLoginTenantID(context.Background(), &types.User{ID: "alice", TenantID: 42}); got != 0 {
		t.Fatalf("resolved tenant = %d, want 0 while home tenant is pending", got)
	}
}

func TestUserServiceRegisterActivatesProvisionedTenant(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{}
	members := &provisioningMemberService{}
	svc := &userService{userRepo: repo, tenantService: tenantSvc, memberService: members}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username: "alice", Email: "alice@example.com", Password: "supersecret",
		TenantProvisioning: types.TenantProvisioningCreatePersonal,
	})

	if err != nil || user == nil {
		t.Fatalf("Register = (%v, %v), want success", user, err)
	}
	if tenantSvc.activateCalls != 1 {
		t.Fatalf("ActivateProvisionedTenant calls = %d, want 1", tenantSvc.activateCalls)
	}
	if repo.deleteCalls != 0 || tenantSvc.purgeCalls != 0 {
		t.Fatalf("unexpected rollback: user deletes=%d tenant purges=%d", repo.deleteCalls, tenantSvc.purgeCalls)
	}
}

func TestUserServiceRegisterPurgesProvisionedTenantWhenActivationFails(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{activateErr: errors.New("activation failed")}
	members := &provisioningMemberService{}
	svc := &userService{userRepo: repo, tenantService: tenantSvc, memberService: members}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username: "alice", Email: "alice@example.com", Password: "supersecret",
		TenantProvisioning: types.TenantProvisioningCreatePersonal,
	})

	if err == nil || user != nil {
		t.Fatalf("Register = (%v, %v), want activation failure", user, err)
	}
	if tenantSvc.activateCalls != 1 {
		t.Fatalf("ActivateProvisionedTenant calls = %d, want 1", tenantSvc.activateCalls)
	}
	if repo.deleteCalls != 1 || tenantSvc.purgeCalls != 1 {
		t.Fatalf("rollback calls: user deletes=%d tenant purges=%d, want 1/1", repo.deleteCalls, tenantSvc.purgeCalls)
	}
}

func TestUserServiceRegisterPurgesProvisionedTenantWhenMemberServiceUnavailable(t *testing.T) {
	repo := &provisioningUserRepo{}
	tenantSvc := &provisioningTenantService{}
	svc := &userService{userRepo: repo, tenantService: tenantSvc}

	user, err := svc.Register(context.Background(), &types.RegisterRequest{
		Username: "alice", Email: "alice@example.com", Password: "supersecret",
		TenantProvisioning: types.TenantProvisioningCreatePersonal,
	})

	if err == nil || user != nil {
		t.Fatalf("Register = (%v, %v), want owner finalization failure", user, err)
	}
	if tenantSvc.activateCalls != 0 {
		t.Fatalf("ActivateProvisionedTenant calls = %d, want 0", tenantSvc.activateCalls)
	}
	if repo.deleteCalls != 1 || tenantSvc.purgeCalls != 1 {
		t.Fatalf("rollback calls: user deletes=%d tenant purges=%d, want 1/1", repo.deleteCalls, tenantSvc.purgeCalls)
	}
}

func TestSwitchTenantRejectsPendingTenantAndAllowsItAfterActivation(t *testing.T) {
	tenantSvc := &provisioningTenantService{unavailable: map[uint64]bool{42: true}}
	members := &provisioningMemberService{members: []*types.TenantMember{
		{UserID: "alice", TenantID: 42, Status: types.TenantMemberStatusActive, Role: types.TenantRoleOwner},
	}}
	svc := &userService{
		tenantService: tenantSvc,
		memberService: members,
		tokenRepo:     &stubAuthTokenRepo{},
	}
	user := &types.User{ID: "alice", TenantID: 1, IsActive: true}

	response, err := svc.SwitchTenant(context.Background(), user, 42, "")
	if err == nil || response != nil {
		t.Fatalf("SwitchTenant pending = (%v, %v), want failure", response, err)
	}

	delete(tenantSvc.unavailable, 42)
	response, err = svc.SwitchTenant(context.Background(), user, 42, "")
	if err != nil || response == nil || response.ActiveTenant == nil || response.ActiveTenant.ID != 42 {
		t.Fatalf("SwitchTenant active = (%v, %v), want tenant 42", response, err)
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
