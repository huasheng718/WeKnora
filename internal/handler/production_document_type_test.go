package handler

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type productionDocumentTypeServiceStub struct {
	documentTypes []*types.ProductionDocumentType
	created       interfaces.CreateProductionDocumentTypeInput
	derived       interfaces.DeriveProductionDocumentTypeInput
	deriveBaseID  string
	deriveCalls   int
	activatedCode string
	activatedVer  int
	getID         string
	err           error
}

func (s *productionDocumentTypeServiceStub) CreateDocumentType(_ context.Context, tenantID uint64, input interfaces.CreateProductionDocumentTypeInput) (*types.ProductionDocumentType, error) {
	s.created = input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionDocumentType{ID: productionDocumentTypeID, TenantID: tenantID, Code: input.Code, Name: input.Name, SchemaVersion: input.SchemaVersion, Status: types.ProductionDocumentTypeDraft, CreatedBy: "admin-1"}, nil
}
func (s *productionDocumentTypeServiceStub) DeriveDraft(_ context.Context, tenantID uint64, baseID string, input interfaces.DeriveProductionDocumentTypeInput) (*types.ProductionDocumentType, error) {
	s.deriveCalls++
	s.deriveBaseID, s.derived = baseID, input
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionDocumentType{
		ID: productionDocumentTypeID, TenantID: tenantID, Code: "sop", Name: input.Name,
		SchemaVersion: 2, Status: types.ProductionDocumentTypeDraft,
		Origin: types.ProductionDocumentTypeOriginCustom,
	}, nil
}
func (s *productionDocumentTypeServiceStub) ActivateDocumentType(_ context.Context, tenantID uint64, code string, schemaVersion int) (*types.ProductionDocumentType, error) {
	s.activatedCode, s.activatedVer = code, schemaVersion
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionDocumentType{ID: productionDocumentTypeID, TenantID: tenantID, Code: code, SchemaVersion: schemaVersion, Status: types.ProductionDocumentTypeActive}, nil
}
func (s *productionDocumentTypeServiceStub) GetDocumentType(_ context.Context, tenantID uint64, documentTypeID string) (*types.ProductionDocumentType, error) {
	s.getID = documentTypeID
	if s.err != nil {
		return nil, s.err
	}
	return &types.ProductionDocumentType{ID: documentTypeID, TenantID: tenantID, Code: "baseline", SchemaVersion: 3, Status: types.ProductionDocumentTypeDraft}, nil
}
func (s *productionDocumentTypeServiceStub) ListDocumentTypes(context.Context, uint64) ([]*types.ProductionDocumentType, error) {
	return s.documentTypes, s.err
}

func TestProductionDocumentTypeHandlerCreatesAndListsDefinitions(t *testing.T) {
	service := &productionDocumentTypeServiceStub{}
	h := NewProductionDocumentTypeHandler(service)
	create, createRecorder := newProductionHandlerContext(http.MethodPost, "/production/document-types", `{"code":"baseline","name":"Baseline","schema_version":1,"block_schema":{"type":"object"},"source_requirements":{},"skill_bindings":{},"quality_rules":{},"review_policy":{},"publication_policy":{}}`)
	h.Create(create)

	list, listRecorder := newProductionHandlerContext(http.MethodGet, "/production/document-types", "")
	service.documentTypes = []*types.ProductionDocumentType{{ID: productionDocumentTypeID, TenantID: 7, Code: "baseline", SchemaVersion: 1}}
	h.List(list)

	require.Equal(t, http.StatusCreated, createRecorder.Code)
	require.Equal(t, "baseline", service.created.Code)
	require.JSONEq(t, `{"type":"object"}`, string(service.created.BlockSchema))
	require.Equal(t, http.StatusOK, listRecorder.Code)
	require.Contains(t, listRecorder.Body.String(), productionDocumentTypeID)
}

func productionDocumentTypeRequestBody(t *testing.T, code, name string, schemaVersion int64) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"code": code, "name": name, "schema_version": schemaVersion,
		"block_schema": map[string]any{}, "source_requirements": map[string]any{},
		"skill_bindings": map[string]any{}, "quality_rules": map[string]any{},
		"review_policy": map[string]any{}, "publication_policy": map[string]any{},
	})
	require.NoError(t, err)
	return string(body)
}

func TestProductionDocumentTypeHandlerEnforcesDefinitionBounds(t *testing.T) {
	t.Run("accepts maximum code name and schema version", func(t *testing.T) {
		service := &productionDocumentTypeServiceStub{}
		h := NewProductionDocumentTypeHandler(service)
		value := strings.Repeat("d", 255)

		response := performProductionHandlerRequest(
			http.MethodPost, "/production/document-types", "/production/document-types",
			productionDocumentTypeRequestBody(t, "  "+value+"  ", "  "+value+"  ", math.MaxInt32), h.Create,
		)

		require.Equal(t, http.StatusCreated, response.Code)
		require.Equal(t, value, service.created.Code)
		require.Equal(t, value, service.created.Name)
		require.Equal(t, math.MaxInt32, service.created.SchemaVersion)
	})

	tests := []struct {
		name          string
		code          string
		displayName   string
		schemaVersion int64
	}{
		{name: "code overflow", code: strings.Repeat("c", 256), displayName: "Baseline", schemaVersion: 1},
		{name: "name overflow", code: "baseline", displayName: strings.Repeat("n", 256), schemaVersion: 1},
		{name: "schema version overflow", code: "baseline", displayName: "Baseline", schemaVersion: int64(math.MaxInt32) + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &productionDocumentTypeServiceStub{}
			h := NewProductionDocumentTypeHandler(service)

			response := performProductionHandlerRequest(
				http.MethodPost, "/production/document-types", "/production/document-types",
				productionDocumentTypeRequestBody(t, test.code, test.displayName, test.schemaVersion), h.Create,
			)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Empty(t, service.created.Code)
		})
	}
}

func TestProductionDocumentTypeHandlerActivatesVersionAddressedByID(t *testing.T) {
	service := &productionDocumentTypeServiceStub{}
	h := NewProductionDocumentTypeHandler(service)
	c, recorder := newProductionHandlerContext(http.MethodPut, "/production/document-types/"+productionDocumentTypeID+"/activate", "")
	c.Params = append(c.Params, gin.Param{Key: "id", Value: productionDocumentTypeID})

	h.Activate(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, productionDocumentTypeID, service.getID)
	require.Equal(t, "baseline", service.activatedCode)
	require.Equal(t, 3, service.activatedVer)
	require.Contains(t, recorder.Body.String(), `"status":"active"`)
}

func TestProductionDocumentTypeHandlerRejectsMalformedIDBeforeServiceAccess(t *testing.T) {
	service := &productionDocumentTypeServiceStub{}
	h := NewProductionDocumentTypeHandler(service)

	response := performProductionHandlerRequest(
		http.MethodPut,
		"/production/document-types/:id/activate",
		"/production/document-types/not-a-uuid/activate",
		"",
		h.Activate,
	)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "valid UUID")
	require.Empty(t, service.getID)
}

func productionDocumentTypeDeriveRequestBody() string {
	return `{
		"name":"Tenant SOP","description":"Derived",
		"block_schema":{"version":1,"required_sections":["Scope"],"allowed_block_types":["paragraph"]},
		"source_requirements":{"version":1,"min_accepted_evidence":1,"allowed_source_kinds":["upload"],"require_evidence_section":true,"allow_unsupported_facts":false},
		"skill_bindings":{"version":1,"skills":[]},
		"workflow_plan":{"version":1,"steps":[]},
		"quality_rules":{"version":1,"require_evidence_for_facts":true,"block_needs_confirmation":true,"gates":["section_completeness"]},
		"review_policy":{"steps":["business_reviewer"]},
		"publication_policy":{"version":1,"target_type":"knowledge_base","chunking":"inherit_target","knowledge_graph":"inherit_target","require_approved_review":true}
	}`
}

func TestDocumentTypeHandlerDerivesDraftFromValidatedPathAndEditableBody(t *testing.T) {
	service := &productionDocumentTypeServiceStub{}
	h := NewProductionDocumentTypeHandler(service)

	response := performProductionHandlerRequest(
		http.MethodPost,
		"/production/document-types/:id/drafts",
		"/production/document-types/"+productionDocumentTypeID+"/drafts",
		productionDocumentTypeDeriveRequestBody(),
		h.DeriveDraft,
	)

	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Equal(t, productionDocumentTypeID, service.deriveBaseID)
	require.Equal(t, "Tenant SOP", service.derived.Name)
	require.Equal(t, 1, service.deriveCalls)
	require.Contains(t, response.Body.String(), `"origin":"custom"`)
}

func TestDocumentTypeHandlerDeriveRejectsMalformedBaseIDBeforeServiceAccess(t *testing.T) {
	service := &productionDocumentTypeServiceStub{}
	h := NewProductionDocumentTypeHandler(service)

	response := performProductionHandlerRequest(
		http.MethodPost,
		"/production/document-types/:id/drafts",
		"/production/document-types/not-a-uuid/drafts",
		productionDocumentTypeDeriveRequestBody(),
		h.DeriveDraft,
	)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "valid UUID")
	require.Zero(t, service.deriveCalls)
}

func TestDocumentTypeHandlerMapsInvalidConfigToBadRequest(t *testing.T) {
	tests := []struct {
		name, pattern, path, body string
		handler                   func(*ProductionDocumentTypeHandler) gin.HandlerFunc
	}{
		{
			name: "create", pattern: "/production/document-types", path: "/production/document-types",
			body:    productionDocumentTypeRequestBody(t, "baseline", "Baseline", 1),
			handler: func(h *ProductionDocumentTypeHandler) gin.HandlerFunc { return h.Create },
		},
		{
			name: "derive", pattern: "/production/document-types/:id/drafts",
			path:    "/production/document-types/" + productionDocumentTypeID + "/drafts",
			body:    productionDocumentTypeDeriveRequestBody(),
			handler: func(h *ProductionDocumentTypeHandler) gin.HandlerFunc { return h.DeriveDraft },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &productionDocumentTypeServiceStub{err: types.ErrProductionDocumentTypeConfigInvalid}
			h := NewProductionDocumentTypeHandler(service)

			response := performProductionHandlerRequest(
				http.MethodPost, test.pattern, test.path, test.body, test.handler(h),
			)

			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}

var _ interfaces.ProductionDocumentTypeService = (*productionDocumentTypeServiceStub)(nil)
