package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type productionDocumentTypeRepoStub struct {
	rows        map[uint64]map[string]*types.ProductionDocumentType
	createErr   error
	activateErr error
	getErr      error
	listErr     error
	activeErr   error
	deriveErr   error
	activeCalls int
	created     *types.ProductionDocumentType
	derived     *types.ProductionDocumentType
	derivedBase string
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

func (r *productionDocumentTypeRepoStub) DeriveDraft(
	_ context.Context,
	tenantID uint64,
	baseID, _ string,
	draft *types.ProductionDocumentType,
) (*types.ProductionDocumentType, error) {
	if r.deriveErr != nil {
		return nil, r.deriveErr
	}
	base := r.rows[tenantID][baseID]
	if base == nil {
		return nil, gorm.ErrRecordNotFound
	}
	r.derivedBase = baseID
	draft.Code = base.Code
	draft.SchemaVersion = base.SchemaVersion + 1
	draft.Status = types.ProductionDocumentTypeDraft
	draft.Origin = types.ProductionDocumentTypeOriginCustom
	draft.TemplateKey = base.TemplateKey
	r.derived = draft
	return draft, nil
}

func (*productionDocumentTypeRepoStub) SeedBuiltins(
	context.Context,
	uint64,
	string,
	[]types.ProductionDocumentType,
) error {
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
		BlockSchema:        types.JSON(`{ "version": 1, "required_sections": ["Scope"], "allowed_block_types": ["paragraph"] }`),
		SourceRequirements: types.JSON(`{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["upload"],"require_evidence_section":true,"allow_unsupported_facts":false}`),
		SkillBindings:      types.JSON(`{"version":1,"skills":[]}`),
		WorkflowPlan:       types.JSON(`{"version":1,"steps":[]}`),
		QualityRules:       types.JSON(`{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness"]}`),
		ReviewPolicy:       types.JSON(`{"steps":["business_reviewer"]}`),
		PublicationPolicy:  types.JSON(`{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}`),
	}
}

func productionDocumentTypeDeriveInput() interfaces.DeriveProductionDocumentTypeInput {
	create := productionDocumentTypeInput()
	return interfaces.DeriveProductionDocumentTypeInput{
		Name: create.Name, Description: create.Description,
		BlockSchema: create.BlockSchema, SourceRequirements: create.SourceRequirements,
		SkillBindings: create.SkillBindings, WorkflowPlan: create.WorkflowPlan,
		QualityRules: create.QualityRules, ReviewPolicy: create.ReviewPolicy,
		PublicationPolicy: create.PublicationPolicy,
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
	require.Equal(t, types.AuditAction("production.document_type_derived"), types.AuditActionProductionDocumentTypeDerived)
	require.Equal(t, types.AuditAction("production.source_frozen"), types.AuditActionProductionSourceFrozen)
	require.Equal(t, types.AuditAction("production.version_created"), types.AuditActionProductionVersionCreated)
}

func TestDocumentTypeServiceDerivesCanonicalDraftForAdminAndOwner(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			svc, repo, _, audit := newProductionDocumentTypeServiceFixture(role)
			templateKey := "sop"
			repo.add(&types.ProductionDocumentType{
				ID: "base-1", TenantID: 7, Code: "sop", SchemaVersion: 4,
				Status: types.ProductionDocumentTypeRetired, Origin: types.ProductionDocumentTypeOriginBuiltin,
				TemplateKey: &templateKey,
			})

			derived, err := svc.DeriveDraft(ctxForUser(7, "actor"), 7, "base-1", productionDocumentTypeDeriveInput())

			require.NoError(t, err)
			require.Equal(t, "base-1", repo.derivedBase)
			require.Equal(t, types.ProductionDocumentTypeOriginCustom, derived.Origin)
			require.Equal(t, templateKey, *derived.TemplateKey)
			require.Equal(t, types.JSON(`{"allowed_block_types":["paragraph"],"required_sections":["Scope"],"version":1}`), derived.BlockSchema)
			require.Len(t, audit.entries, 1)
			require.Equal(t, types.AuditActionProductionDocumentTypeDerived, audit.entries[0].Action)
			var details map[string]any
			require.NoError(t, json.Unmarshal(audit.entries[0].Details, &details))
			require.Equal(t, "base-1", details["base_document_type_id"])
			require.Equal(t, float64(4), details["base_schema_version"])
			require.Equal(t, float64(5), details["derived_schema_version"])
		})
	}
}

func TestDocumentTypeServiceDeriveDeniesContributorAndCrossTenantBase(t *testing.T) {
	t.Run("contributor", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleContributor)
		derived, err := svc.DeriveDraft(ctxForUser(7, "actor"), 7, "base-1", productionDocumentTypeDeriveInput())
		require.Nil(t, derived)
		require.ErrorIs(t, err, types.ErrProductionForbidden)
		require.Nil(t, repo.derived)
		require.Empty(t, audit.entries)
	})

	t.Run("cross tenant base", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
		repo.add(&types.ProductionDocumentType{ID: "tenant-8-base", TenantID: 8, Code: "sop", SchemaVersion: 1})
		derived, err := svc.DeriveDraft(ctxForUser(7, "actor"), 7, "tenant-8-base", productionDocumentTypeDeriveInput())
		require.Nil(t, derived)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		require.Empty(t, audit.entries)
	})
}

func TestDocumentTypeServiceDeriveRejectsStrictConfigAndAuditsOnlySuccessfulWrite(t *testing.T) {
	svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
	repo.add(&types.ProductionDocumentType{ID: "base-1", TenantID: 7, Code: "sop", SchemaVersion: 1})
	invalid := productionDocumentTypeDeriveInput()
	invalid.QualityRules = types.JSON(`{"version":1,"unknown":true}`)

	derived, err := svc.DeriveDraft(ctxForUser(7, "actor"), 7, "base-1", invalid)
	require.Nil(t, derived)
	require.ErrorIs(t, err, types.ErrProductionDocumentTypeConfigInvalid)
	require.Nil(t, repo.derived)
	require.Empty(t, audit.entries)

	repo.deriveErr = errors.New("derive failed")
	derived, err = svc.DeriveDraft(ctxForUser(7, "actor"), 7, "base-1", productionDocumentTypeDeriveInput())
	require.Nil(t, derived)
	require.ErrorContains(t, err, "derive failed")
	require.Empty(t, audit.entries)
}

func TestDocumentTypeServiceClassifiesUnboundWorkflowAsInvalidConfigWithoutMutation(t *testing.T) {
	unboundWorkflow := types.JSON(`{"version":1,"steps":[{"provider_type":"skill","provider_id":"unbound-skill","tool_name":"load_instructions","request":{"source_item_id":"71000000-0000-4000-8000-000000000005"}}]}`)

	t.Run("create", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
		input := productionDocumentTypeInput()
		input.WorkflowPlan = unboundWorkflow

		created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, input)

		require.Nil(t, created)
		require.ErrorIs(t, err, types.ErrProductionDocumentTypeConfigInvalid)
		require.Nil(t, repo.created)
		require.Empty(t, audit.entries)
	})

	t.Run("derive", func(t *testing.T) {
		svc, repo, _, audit := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)
		repo.add(&types.ProductionDocumentType{
			ID: "base-1", TenantID: 7, Code: "sop", SchemaVersion: 1,
			Status: types.ProductionDocumentTypeActive,
		})
		input := productionDocumentTypeDeriveInput()
		input.WorkflowPlan = unboundWorkflow

		derived, err := svc.DeriveDraft(ctxForUser(7, "actor"), 7, "base-1", input)

		require.Nil(t, derived)
		require.ErrorIs(t, err, types.ErrProductionDocumentTypeConfigInvalid)
		require.Nil(t, repo.derived)
		require.Empty(t, audit.entries)
	})
}

func TestDocumentTypeServiceCreateCanonicalizesConfigsAndForcesCustomLineage(t *testing.T) {
	svc, repo, _, _ := newProductionDocumentTypeServiceFixture(types.TenantRoleAdmin)

	created, err := svc.CreateDocumentType(ctxForUser(7, "actor"), 7, productionDocumentTypeInput())

	require.NoError(t, err)
	require.Same(t, repo.created, created)
	require.Equal(t, types.ProductionDocumentTypeOriginCustom, created.Origin)
	require.Nil(t, created.TemplateKey)
	require.Equal(t, types.JSON(`{"allowed_block_types":["paragraph"],"required_sections":["Scope"],"version":1}`), created.BlockSchema)
}

type revokingProductionDocumentTypeRepo struct {
	interfaces.ProductionDocumentTypeRepository
	db     *gorm.DB
	revoke func(*gorm.DB) error
}

func (r *revokingProductionDocumentTypeRepo) DeriveDraft(
	ctx context.Context,
	tenantID uint64,
	baseID, actorID string,
	draft *types.ProductionDocumentType,
) (*types.ProductionDocumentType, error) {
	if err := r.revoke(r.db); err != nil {
		return nil, err
	}
	return r.ProductionDocumentTypeRepository.DeriveDraft(ctx, tenantID, baseID, actorID, draft)
}

func TestDocumentTypeServiceDeriveRejectsMembershipRevokedAfterPreflightWithoutAudit(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*gorm.DB) error
	}{
		{
			name: "demoted",
			revoke: func(db *gorm.DB) error {
				return db.Model(&types.TenantMember{}).
					Where("tenant_id = ? AND user_id = ?", 7, "actor").
					Update("role", types.TenantRoleContributor).Error
			},
		},
		{
			name: "removed",
			revoke: func(db *gorm.DB) error {
				return db.Where("tenant_id = ? AND user_id = ?", 7, "actor").
					Delete(&types.TenantMember{}).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", t.Name())
			db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(&types.ProductionDocumentType{}, &types.TenantMember{}))
			require.NoError(t, db.Create(&types.TenantMember{
				UserID: "actor", TenantID: 7, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive,
			}).Error)
			input := productionDocumentTypeDeriveInput()
			base := &types.ProductionDocumentType{
				ID: "base-reauth", TenantID: 7, Code: "sop", Name: "SOP", SchemaVersion: 1,
				BlockSchema: input.BlockSchema, SourceRequirements: input.SourceRequirements,
				SkillBindings: input.SkillBindings, WorkflowPlan: input.WorkflowPlan,
				QualityRules: input.QualityRules, ReviewPolicy: input.ReviewPolicy,
				PublicationPolicy: input.PublicationPolicy, Status: types.ProductionDocumentTypeActive,
				Origin: types.ProductionDocumentTypeOriginCustom, CreatedBy: "actor",
			}
			require.NoError(t, db.Create(base).Error)

			realRepo := apprepository.NewProductionDocumentTypeRepository(db)
			repo := &revokingProductionDocumentTypeRepo{
				ProductionDocumentTypeRepository: realRepo, db: db, revoke: test.revoke,
			}
			members := newProductionMemberServiceStub()
			members.add(7, "actor", types.TenantRoleAdmin)
			audit := &productionAuditServiceStub{}
			svc := NewProductionDocumentTypeService(repo, members, audit)

			derived, err := svc.DeriveDraft(
				ctxForUser(7, "actor"), 7, base.ID, productionDocumentTypeDeriveInput(),
			)

			require.Nil(t, derived)
			require.ErrorIs(t, err, types.ErrProductionForbidden)
			require.Empty(t, audit.entries)
			var count int64
			require.NoError(t, db.Model(&types.ProductionDocumentType{}).
				Where("tenant_id = ? AND id <> ?", 7, base.ID).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}
