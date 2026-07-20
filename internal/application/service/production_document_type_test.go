package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type productionDocumentTypeRepoStub struct {
	rows        map[uint64]map[string]*types.ProductionDocumentType
	createErr   error
	activateErr error
	getErr      error
	listErr     error
	activeErr   error
	activeCalls int
	created     *types.ProductionDocumentType
	activated   struct {
		tenantID      uint64
		code          string
		schemaVersion int
	}
}

func newProductionDocumentTypeRepoStub() *productionDocumentTypeRepoStub {
	return &productionDocumentTypeRepoStub{rows: map[uint64]map[string]*types.ProductionDocumentType{}}
}

func (r *productionDocumentTypeRepoStub) Create(_ context.Context, documentType *types.ProductionDocumentType) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = documentType
	if r.rows[documentType.TenantID] == nil {
		r.rows[documentType.TenantID] = map[string]*types.ProductionDocumentType{}
	}
	r.rows[documentType.TenantID][documentType.ID] = documentType
	return nil
}

func (r *productionDocumentTypeRepoStub) Activate(_ context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error) {
	if r.activateErr != nil {
		return nil, r.activateErr
	}
	r.activated.tenantID, r.activated.code, r.activated.schemaVersion = tenantID, code, schemaVersion
	for _, row := range r.rows[tenantID] {
		if row.Code == code && row.Status == types.ProductionDocumentTypeActive {
			row.Status = types.ProductionDocumentTypeRetired
		}
	}
	for _, row := range r.rows[tenantID] {
		if row.Code == code && row.SchemaVersion == schemaVersion && row.Status == types.ProductionDocumentTypeDraft {
			row.Status = types.ProductionDocumentTypeActive
			return row, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *productionDocumentTypeRepoStub) GetByID(_ context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if row := r.rows[tenantID][documentTypeID]; row != nil {
		return row, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *productionDocumentTypeRepoStub) GetActiveByIDForReview(
	ctx context.Context,
	tenantID uint64,
	documentTypeID string,
	schemaVersion int,
) (*types.ProductionDocumentType, error) {
	documentType, err := r.GetByID(ctx, tenantID, documentTypeID)
	if err != nil {
		return nil, err
	}
	if documentType.Status != types.ProductionDocumentTypeActive || documentType.SchemaVersion != schemaVersion {
		return nil, types.ErrProductionDocumentTypeInactive
	}
	return documentType, nil
}

func (r *productionDocumentTypeRepoStub) GetActiveByCode(_ context.Context, tenantID uint64, code string) (*types.ProductionDocumentType, error) {
	r.activeCalls++
	if r.activeErr != nil {
		return nil, r.activeErr
	}
	for _, row := range r.rows[tenantID] {
		if row.Code == code && row.Status == types.ProductionDocumentTypeActive {
			return row, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *productionDocumentTypeRepoStub) List(_ context.Context, tenantID uint64) ([]*types.ProductionDocumentType, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	var result []*types.ProductionDocumentType
	for _, row := range r.rows[tenantID] {
		result = append(result, row)
	}
	return result, nil
}

func (r *productionDocumentTypeRepoStub) add(row *types.ProductionDocumentType) {
	if r.rows[row.TenantID] == nil {
		r.rows[row.TenantID] = map[string]*types.ProductionDocumentType{}
	}
	r.rows[row.TenantID][row.ID] = row
}

func productionDocumentTypeInput() interfaces.CreateProductionDocumentTypeInput {
	return interfaces.CreateProductionDocumentTypeInput{
		Code: "baseline", Name: "Baseline", Description: "Definition", SchemaVersion: 1,
		BlockSchema: types.JSON(`{"type":"object"}`), SourceRequirements: types.JSON(`{}`),
		SkillBindings: types.JSON(`{}`), QualityRules: types.JSON(`{}`),
		ReviewPolicy: types.JSON(`{}`), PublicationPolicy: types.JSON(`{}`),
	}
}

func newProductionDocumentTypeServiceFixture(
	role types.TenantRole,
) (*productionDocumentTypeService, *productionDocumentTypeRepoStub, *productionMemberServiceStub, *productionAuditServiceStub) {
	repo := newProductionDocumentTypeRepoStub()
	members := newProductionMemberServiceStub()
	members.add(7, "actor", role)
	audit := &productionAuditServiceStub{}
	return NewProductionDocumentTypeService(repo, members, audit), repo, members, audit
}

func TestProductionDocumentTypeCreateRequiresTenantAdminOrOwner(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			svc, repo, _, audit := newProductionDocumentTypeServiceFixture(role)

			created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())

			require.NoError(t, err)
			require.Same(t, created, repo.created)
			require.NotEmpty(t, created.ID)
			require.Equal(t, "actor", created.CreatedBy)
			require.Equal(t, types.ProductionDocumentTypeDraft, created.Status)
			require.Len(t, audit.entries, 1)
			require.Equal(t, types.AuditActionProductionDocumentTypeCreated, audit.entries[0].Action)
		})
	}
}

func TestProductionDocumentTypeContributorCannotCreateOrActivate(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleContributor)

	created, createErr := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())
	activated, activateErr := svc.ActivateDocumentType(ctxForUser(7, "actor"), 7, "baseline", 1)

	require.Nil(t, created)
	require.ErrorIs(t, createErr, types.ErrProductionForbidden)
	require.Nil(t, activated)
	require.ErrorIs(t, activateErr, types.ErrProductionForbidden)
	require.Nil(t, repo.created)
	require.Zero(t, repo.activated.tenantID)
	require.Empty(t, audit.entries)
}

func TestProductionDocumentTypeWritesRejectCrossTenantContext(t *testing.T) {
	svc, repo, members, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	members.add(8, "actor", types.TenantRoleAdmin)

	created, err := svc.CreateDocumentType(ctxForUser(8, "actor"), 7, productionDocumentTypeInput())

	require.Nil(t, created)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
	require.Nil(t, repo.created)
	require.Empty(t, audit.entries)
}

func TestProductionDocumentTypeActivateReturnsActiveImmutableDefinitionAndAudits(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	repo.add(&types.ProductionDocumentType{
		ID: "type-1", TenantID: 7, Code: "baseline", Name: "Baseline", SchemaVersion: 1,
		Status: types.ProductionDocumentTypeDraft, CreatedBy: "actor",
	})

	activated, err := svc.ActivateDocumentType(ctxForUser(7, "actor"), 7, "baseline", 1)

	require.NoError(t, err)
	require.Equal(t, types.ProductionDocumentTypeActive, activated.Status)
	require.ErrorIs(t, activated.CanMutateDefinition(), types.ErrProductionDocumentTypeImmutable)
	require.Len(t, audit.entries, 1)
	require.Equal(t, types.AuditActionProductionDocumentTypeActivated, audit.entries[0].Action)
	require.Equal(t, "type-1", audit.entries[0].TargetID)
}

func TestProductionDocumentTypeActivateHasNoPostCommitRetrievalBoundary(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	repo.add(&types.ProductionDocumentType{
		ID: "type-atomic", TenantID: 7, Code: "baseline", Name: "Baseline", SchemaVersion: 3,
		Status: types.ProductionDocumentTypeDraft, CreatedBy: "actor",
	})
	repo.activeErr = errors.New("post-commit read failed")

	activated, err := svc.ActivateDocumentType(ctxForUser(7, "actor"), 7, "baseline", 3)

	require.NoError(t, err)
	require.Equal(t, "type-atomic", activated.ID)
	require.Zero(t, repo.activeCalls)
	require.Len(t, audit.entries, 1)
	require.Equal(t, "type-atomic", audit.entries[0].TargetID)
}

func TestProductionDocumentTypeOwnerCanActivate(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleOwner)
	repo.add(&types.ProductionDocumentType{
		ID: "type-owner", TenantID: 7, Code: "baseline", Name: "Baseline", SchemaVersion: 2,
		Status: types.ProductionDocumentTypeDraft, CreatedBy: "actor",
	})

	activated, err := svc.ActivateDocumentType(ctxForUser(7, "actor"), 7, "baseline", 2)

	require.NoError(t, err)
	require.Equal(t, "type-owner", activated.ID)
	require.Len(t, audit.entries, 1)
}

func TestProductionDocumentTypeMutationFailuresDoNotEmitSuccessAudit(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
		repo.createErr = errors.New("create failed")
		created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())
		require.Nil(t, created)
		require.ErrorContains(t, err, "create failed")
		require.Empty(t, audit.entries)
	})

	t.Run("activate", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleOwner)
		repo.activateErr = errors.New("activate failed")
		activated, err := svc.ActivateDocumentType(ctxForUser(7, "actor"), 7, "baseline", 1)
		require.Nil(t, activated)
		require.ErrorContains(t, err, "activate failed")
		require.Empty(t, audit.entries)
	})
}

func TestProductionDocumentTypeCreatePreservesConflictSentinel(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	repo.createErr = types.ErrProductionConflict

	created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())

	require.Nil(t, created)
	require.ErrorIs(t, err, types.ErrProductionConflict)
	require.Empty(t, audit.entries)
}

func TestProductionDocumentTypeAuditFailureDoesNotRollBackSuccessfulMutation(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	audit.err = errors.New("audit unavailable")

	created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())

	require.NoError(t, err)
	require.Same(t, created, repo.created)
	require.Len(t, audit.entries, 1)
}

func TestProductionDocumentTypeReadMethodsAreTenantScoped(t *testing.T) {
	svc, repo, _, _ := newProductionDocumentTypeServiceFixture(types.TenantRoleViewer)
	repo.add(&types.ProductionDocumentType{ID: "type-1", TenantID: 7, Code: "baseline", Status: types.ProductionDocumentTypeDraft})

	row, err := svc.GetDocumentType(ctxForUser(8, "actor"), 7, "type-1")
	require.Nil(t, row)
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	rows, err := svc.ListDocumentTypes(ctxForUser(7, "actor"), 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestProductionProjectAndDocumentTypeAuditActionWireValues(t *testing.T) {
	require.Equal(t, types.AuditAction("production.project_created"), types.AuditActionProductionProjectCreated)
	require.Equal(t, types.AuditAction("production.project_role_set"), types.AuditActionProductionProjectRoleSet)
	require.Equal(t, types.AuditAction("production.document_type_created"), types.AuditActionProductionDocumentTypeCreated)
	require.Equal(t, types.AuditAction("production.document_type_activated"), types.AuditActionProductionDocumentTypeActivated)
	require.Equal(t, types.AuditAction("production.source_frozen"), types.AuditActionProductionSourceFrozen)
	require.Equal(t, types.AuditAction("production.version_created"), types.AuditActionProductionVersionCreated)
}
