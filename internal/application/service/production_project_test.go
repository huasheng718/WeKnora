package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type productionProjectRepoStub struct {
	projects       map[uint64]map[string]*types.ProductionProject
	roles          map[uint64]map[string]map[string][]types.ProductionRole
	createErr      error
	assignErr      error
	removeErr      error
	getErr         error
	listErr        error
	listRolesErr   error
	created        *types.ProductionProject
	createdOwner   *types.ProductionProjectMember
	assigned       *types.ProductionProjectMember
	removed        *types.ProductionProjectMember
	lastRoleTenant uint64
	decorations    types.ProductionProjectDecorations
}

func newProductionProjectRepoStub() *productionProjectRepoStub {
	return &productionProjectRepoStub{
		projects: map[uint64]map[string]*types.ProductionProject{},
		roles:    map[uint64]map[string]map[string][]types.ProductionRole{},
	}
}

func (r *productionProjectRepoStub) Create(_ context.Context, project *types.ProductionProject, owner *types.ProductionProjectMember) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created, r.createdOwner = project, owner
	if r.projects[project.TenantID] == nil {
		r.projects[project.TenantID] = map[string]*types.ProductionProject{}
	}
	r.projects[project.TenantID][project.ID] = project
	r.setRoles(project.TenantID, project.ID, owner.UserID, []types.ProductionRole{owner.Role})
	return nil
}

func (r *productionProjectRepoStub) GetByID(_ context.Context, tenantID uint64, projectID string) (*types.ProductionProject, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if project := r.projects[tenantID][projectID]; project != nil {
		return project, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *productionProjectRepoStub) ListByUser(_ context.Context, tenantID uint64, userID string) ([]*types.ProductionProject, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	var result []*types.ProductionProject
	for projectID, project := range r.projects[tenantID] {
		if len(r.roles[tenantID][projectID][userID]) != 0 {
			result = append(result, project)
		}
	}
	return result, nil
}

func (r *productionProjectRepoStub) ListDecorationsByUser(_ context.Context, tenantID uint64, userID string) (types.ProductionProjectDecorations, error) {
	return r.decorations, nil
}

func (r *productionProjectRepoStub) AssignRole(_ context.Context, tenantID uint64, member *types.ProductionProjectMember) error {
	if r.assignErr != nil {
		return r.assignErr
	}
	r.assigned = member
	roles := append([]types.ProductionRole(nil), r.roles[tenantID][member.ProjectID][member.UserID]...)
	r.setRoles(tenantID, member.ProjectID, member.UserID, append(roles, member.Role))
	return nil
}

func (r *productionProjectRepoStub) RemoveRole(_ context.Context, tenantID uint64, projectID, userID string, role types.ProductionRole) error {
	if r.removeErr != nil {
		return r.removeErr
	}
	r.removed = &types.ProductionProjectMember{ProjectID: projectID, UserID: userID, Role: role}
	roles := r.roles[tenantID][projectID][userID]
	for i, candidate := range roles {
		if candidate == role {
			r.setRoles(tenantID, projectID, userID, append(roles[:i:i], roles[i+1:]...))
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (r *productionProjectRepoStub) ListRoles(_ context.Context, tenantID uint64, projectID, userID string) ([]types.ProductionRole, error) {
	r.lastRoleTenant = tenantID
	if r.listRolesErr != nil {
		return nil, r.listRolesErr
	}
	return append([]types.ProductionRole(nil), r.roles[tenantID][projectID][userID]...), nil
}

func (r *productionProjectRepoStub) addProject(project *types.ProductionProject) {
	if r.projects[project.TenantID] == nil {
		r.projects[project.TenantID] = map[string]*types.ProductionProject{}
	}
	r.projects[project.TenantID][project.ID] = project
}

func (r *productionProjectRepoStub) setRoles(tenantID uint64, projectID, userID string, roles []types.ProductionRole) {
	if r.roles[tenantID] == nil {
		r.roles[tenantID] = map[string]map[string][]types.ProductionRole{}
	}
	if r.roles[tenantID][projectID] == nil {
		r.roles[tenantID][projectID] = map[string][]types.ProductionRole{}
	}
	r.roles[tenantID][projectID][userID] = roles
}

type productionMemberServiceStub struct {
	members map[uint64]map[string]*types.TenantMember
	getErr  error
}

func newProductionMemberServiceStub() *productionMemberServiceStub {
	return &productionMemberServiceStub{members: map[uint64]map[string]*types.TenantMember{}}
}

func (s *productionMemberServiceStub) add(tenantID uint64, userID string, role types.TenantRole) {
	if s.members[tenantID] == nil {
		s.members[tenantID] = map[string]*types.TenantMember{}
	}
	s.members[tenantID][userID] = &types.TenantMember{TenantID: tenantID, UserID: userID, Role: role, Status: types.TenantMemberStatusActive}
}

func (s *productionMemberServiceStub) AddMember(context.Context, string, uint64, types.TenantRole, *string) (*types.TenantMember, error) {
	panic("unexpected AddMember")
}
func (s *productionMemberServiceStub) EnsureOwner(context.Context, string, uint64) (*types.TenantMember, error) {
	panic("unexpected EnsureOwner")
}
func (s *productionMemberServiceStub) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.members[tenantID][userID], nil
}
func (s *productionMemberServiceStub) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	panic("unexpected ListByUser")
}
func (s *productionMemberServiceStub) ListByTenant(context.Context, uint64) ([]*types.TenantMember, error) {
	panic("unexpected ListByTenant")
}
func (s *productionMemberServiceStub) ListMembersPage(context.Context, uint64, string, int, int) ([]*types.TenantMember, int64, error) {
	panic("unexpected ListMembersPage")
}
func (s *productionMemberServiceStub) HasAnyMembers(context.Context, uint64) (bool, error) {
	panic("unexpected HasAnyMembers")
}
func (s *productionMemberServiceStub) UpdateRole(context.Context, string, uint64, types.TenantRole) error {
	panic("unexpected UpdateRole")
}
func (s *productionMemberServiceStub) RemoveMember(context.Context, string, uint64) error {
	panic("unexpected RemoveMember")
}

type productionAuditServiceStub struct {
	entries []*types.AuditLog
	err     error
}

func (s *productionAuditServiceStub) Log(_ context.Context, entry *types.AuditLog) error {
	copy := *entry
	s.entries = append(s.entries, &copy)
	return s.err
}
func (s *productionAuditServiceStub) LogDenied(context.Context, *gin.Context, uint64, string, string, types.TenantRole) error {
	panic("unexpected LogDenied")
}
func (s *productionAuditServiceStub) List(context.Context, uint64, *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
	panic("unexpected List")
}
func (s *productionAuditServiceStub) Purge(context.Context, int) (int64, error) {
	panic("unexpected Purge")
}

func ctxForUser(tenantID uint64, userID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, userID)
}

func newProductionProjectServiceFixture(
	t *testing.T,
	tenantRole types.TenantRole,
	projectRoles []types.ProductionRole,
) (*productionProjectService, *productionProjectRepoStub, *productionMemberServiceStub, *productionAuditServiceStub) {
	t.Helper()
	repo := newProductionProjectRepoStub()
	repo.addProject(&types.ProductionProject{ID: "project-1", TenantID: 7, Name: "Foundation", OwnerUserID: "creator", Status: types.ProductionProjectActive})
	repo.setRoles(7, "project-1", "owner-user", projectRoles)
	members := newProductionMemberServiceStub()
	members.add(7, "author-user", tenantRole)
	members.add(7, "owner-user", tenantRole)
	members.add(7, "reviewer", types.TenantRoleContributor)
	audit := &productionAuditServiceStub{}
	return NewProductionProjectService(repo, members, audit), repo, members, audit
}

func TestProductionProjectAssignRoleRequiresTenantAdminOrProjectOwner(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)

	err := svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleBusinessReviewer)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Nil(t, repo.assigned)
	require.Empty(t, audit.entries)
}

func TestProductionProjectOwnerCanAssignRole(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, []types.ProductionRole{types.ProductionRoleProjectOwner})

	err := svc.AssignRole(ctxForUser(7, "owner-user"), "project-1", "reviewer", types.ProductionRoleBusinessReviewer)

	require.NoError(t, err)
	require.Equal(t, "owner-user", repo.assigned.AssignedBy)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionProjectRoleSet, audit.entries[0].Action)
	require.Equal(t, "reviewer", audit.entries[0].TargetUserID)
}

func TestProductionProjectTenantAdminAndOwnerCanAssignRole(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			svc, repo, _, _ := newProductionProjectServiceFixture(t, role, nil)
			require.NoError(t, svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleAuthor))
			require.NotNil(t, repo.assigned)
		})
	}
}

func TestProductionProjectAssignRoleRejectsCrossTenantProjectOwner(t *testing.T) {
	svc, repo, members, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, []types.ProductionRole{types.ProductionRoleProjectOwner})
	members.add(8, "owner-user", types.TenantRoleContributor)
	members.add(8, "reviewer", types.TenantRoleContributor)

	err := svc.AssignRole(ctxForUser(8, "owner-user"), "project-1", "reviewer", types.ProductionRoleAuthor)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Equal(t, uint64(8), repo.lastRoleTenant)
	require.Nil(t, repo.assigned)
	require.Empty(t, audit.entries)
}

func TestProductionProjectAssignRoleRejectsUserOutsideTenant(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleAdmin, nil)

	err := svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "outsider", types.ProductionRoleProjectOwner)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Nil(t, repo.assigned)
	require.Empty(t, audit.entries)
}

func TestProductionProjectAssignRoleCannotEscalateViewerToWriteRole(t *testing.T) {
	svc, repo, members, audit := newProductionProjectServiceFixture(t, types.TenantRoleAdmin, nil)
	members.add(7, "read-only-user", types.TenantRoleViewer)

	err := svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "read-only-user", types.ProductionRoleAuthor)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Nil(t, repo.assigned)
	require.Empty(t, audit.entries)
	require.NoError(t, svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "read-only-user", types.ProductionRoleObserver))
}

func TestProductionProjectRemoveRoleUsesSameAuthorityAndAuditsSuccess(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, []types.ProductionRole{types.ProductionRoleProjectOwner})
	repo.setRoles(7, "project-1", "reviewer", []types.ProductionRole{types.ProductionRoleAuthor})

	err := svc.RemoveRole(ctxForUser(7, "owner-user"), "project-1", "reviewer", types.ProductionRoleAuthor)

	require.NoError(t, err)
	require.NotNil(t, repo.removed)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionProjectRoleSet, audit.entries[0].Action)
}

func TestProductionProjectMutationFailureDoesNotEmitSuccessAudit(t *testing.T) {
	t.Run("assign", func(t *testing.T) {
		svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleAdmin, nil)
		repo.assignErr = errors.New("write failed")

		err := svc.AssignRole(ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleAuthor)

		require.ErrorContains(t, err, "write failed")
		require.Empty(t, audit.entries)
	})

	t.Run("remove", func(t *testing.T) {
		svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleAdmin, nil)
		repo.removeErr = errors.New("write failed")

		err := svc.RemoveRole(ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleAuthor)

		require.ErrorContains(t, err, "write failed")
		require.Empty(t, audit.entries)
	})
}

func TestProductionProjectAssignRolePreservesConflictSentinel(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleAdmin, nil)
	repo.assignErr = types.ErrProductionConflict

	err := svc.AssignRole(
		ctxForUser(7, "author-user"), "project-1", "reviewer", types.ProductionRoleAuthor,
	)

	require.ErrorIs(t, err, types.ErrProductionConflict)
	require.Empty(t, audit.entries)
}

func TestProductionProjectCreatePersistsCallerOwnerAtomicallyAndAudits(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)

	project, err := svc.CreateProject(ctxForUser(7, "author-user"), interfaces.CreateProductionProjectInput{Name: "New", Description: "Project"})

	require.NoError(t, err)
	require.NotEmpty(t, project.ID)
	require.Equal(t, uint64(7), project.TenantID)
	require.Equal(t, "author-user", project.OwnerUserID)
	require.Equal(t, types.ProductionProjectActive, project.Status)
	require.Same(t, project, repo.created)
	require.Equal(t, project.ID, repo.createdOwner.ProjectID)
	require.Equal(t, "author-user", repo.createdOwner.UserID)
	require.Equal(t, types.ProductionRoleProjectOwner, repo.createdOwner.Role)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionProjectCreated, audit.entries[0].Action)
}

func TestProductionProjectCreateDeniesViewerAndDoesNotAudit(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleViewer, nil)

	project, err := svc.CreateProject(ctxForUser(7, "author-user"), interfaces.CreateProductionProjectInput{Name: "New"})

	require.Nil(t, project)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Nil(t, repo.created)
	require.Empty(t, audit.entries)
}

func TestProductionProjectCreateRepositoryFailureDoesNotAudit(t *testing.T) {
	svc, repo, _, audit := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)
	repo.createErr = errors.New("transaction rolled back")

	project, err := svc.CreateProject(ctxForUser(7, "author-user"), interfaces.CreateProductionProjectInput{Name: "New"})

	require.Nil(t, project)
	require.ErrorContains(t, err, "transaction rolled back")
	require.Empty(t, audit.entries)
}

func TestProductionProjectRequireRoleIsTenantScoped(t *testing.T) {
	svc, repo, members, _ := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)
	members.add(8, "author-user", types.TenantRoleContributor)
	repo.setRoles(7, "project-1", "author-user", []types.ProductionRole{types.ProductionRolePublisher})

	err := svc.RequireProjectRole(ctxForUser(8, "author-user"), "project-1", types.ProductionRolePublisher)

	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Equal(t, uint64(8), repo.lastRoleTenant)
}

func TestProductionProjectRequireRoleRejectsMissingContextIdentity(t *testing.T) {
	svc, _, _, _ := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)

	require.ErrorIs(t, svc.RequireProjectRole(context.Background(), "project-1", types.ProductionRoleAuthor), types.ErrProductionForbidden)
	tenantOnly := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	require.ErrorIs(t, svc.RequireProjectRole(tenantOnly, "project-1", types.ProductionRoleAuthor), types.ErrProductionForbidden)
}

func TestProductionProjectReadMethodsEnforceCallerTenantAndAssignment(t *testing.T) {
	svc, _, _, _ := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)

	project, err := svc.GetProject(ctxForUser(8, "author-user"), 7, "project-1")
	require.Nil(t, project)
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	projects, err := svc.ListProjects(ctxForUser(7, "author-user"), 7, "owner-user")
	require.Nil(t, projects)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionProjectReadMethodsReturnAuthorizedRows(t *testing.T) {
	svc, repo, _, _ := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)
	repo.setRoles(7, "project-1", "author-user", []types.ProductionRole{types.ProductionRoleAuthor})

	project, err := svc.GetProject(ctxForUser(7, "author-user"), 7, "project-1")
	require.NoError(t, err)
	require.Equal(t, "project-1", project.ID)

	projects, err := svc.ListProjects(ctxForUser(7, "author-user"), 7, "author-user")
	require.NoError(t, err)
	require.Len(t, projects, 1)
}

func TestProductionProjectListDecoratesClonesWithCallerRolesAndSummary(t *testing.T) {
	svc, repo, _, _ := newProductionProjectServiceFixture(t, types.TenantRoleContributor, nil)
	repo.setRoles(7, "project-1", "author-user", []types.ProductionRole{types.ProductionRoleAuthor})
	repo.decorations = types.ProductionProjectDecorations{
		"project-1": {
			CurrentUserRoles: []types.ProductionRole{types.ProductionRoleAuthor},
			Summary:          types.ProductionProjectSummary{DocumentCount: 3, SourceSetCount: 2},
		},
	}
	original := repo.projects[7]["project-1"]

	projects, err := svc.ListProjects(ctxForUser(7, "author-user"), 7, "author-user")

	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.NotSame(t, original, projects[0])
	require.Equal(t, []types.ProductionRole{types.ProductionRoleAuthor}, projects[0].CurrentUserRoles)
	require.Equal(t, int64(3), projects[0].Summary.DocumentCount)
	require.Empty(t, original.CurrentUserRoles)
	require.Nil(t, original.Summary)
}
