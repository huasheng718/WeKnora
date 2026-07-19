package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const adapterDataSourceID = "10000000-0000-4000-8000-000000000007"

type fakeProductionDataSourceService struct {
	dataSource *types.DataSource
	calls      int
}

func (f *fakeProductionDataSourceService) GetDataSource(context.Context, string) (*types.DataSource, error) {
	f.calls++
	if f.dataSource == nil {
		return nil, errors.New("token=credential-value")
	}
	copy := *f.dataSource
	return &copy, nil
}

type fakeProductionConnector struct {
	listResult []types.Resource
	fetch      []types.FetchedItem
	err        error
	listCalls  int
	fetchCalls int
	seenToken  string
}

func (f *fakeProductionConnector) Type() string { return types.ConnectorTypeNotion }
func (f *fakeProductionConnector) Validate(context.Context, *types.DataSourceConfig) error {
	return nil
}
func (f *fakeProductionConnector) ResolveResourceAncestors(context.Context, *types.DataSourceConfig, []string) ([]string, error) {
	return nil, nil
}
func (f *fakeProductionConnector) ListResources(_ context.Context, cfg *types.DataSourceConfig, _ string) ([]types.Resource, error) {
	f.listCalls++
	f.seenToken, _ = cfg.Credentials["access_token"].(string)
	return f.listResult, f.err
}
func (f *fakeProductionConnector) FetchAll(_ context.Context, cfg *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	f.fetchCalls++
	f.seenToken, _ = cfg.Credentials["access_token"].(string)
	return f.fetch, f.err
}
func (f *fakeProductionConnector) FetchIncremental(context.Context, *types.DataSourceConfig, *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	return nil, nil, nil
}

func dataSourceAdapterFixture(t *testing.T, connector *fakeProductionConnector) (*ProductionDataSourceAdapter, *types.ProductionToolCall, *adapterScopeFixture) {
	t.Helper()
	config, err := json.Marshal(types.DataSourceConfig{
		Type:        types.ConnectorTypeNotion,
		Credentials: map[string]any{"access_token": "credential-value"},
		ResourceIDs: []string{"resource-b", "resource-a"},
		Settings:    map[string]any{"mode": "full"},
	})
	require.NoError(t, err)
	scope := &adapterScopeFixture{
		run: &types.ProductionRun{
			ID: adapterRunID, TenantID: 7, ProjectID: adapterProjectID, DocumentID: adapterDocumentID,
			SourceSetID: adapterSourceSetID, Attempt: 2, CurrentStep: 3,
		},
		item: &types.ProductionSourceItem{
			ID: adapterSourceItemID, SourceSetID: adapterSourceSetID, SourceKind: types.ProductionSourceKindDatasource,
			ExternalID: adapterDataSourceID, SourceSystem: types.ConnectorTypeNotion,
		},
		set:      &types.ProductionSourceSet{ID: adapterSourceSetID, TenantID: 7, ProjectID: adapterProjectID},
		evidence: make(map[string]*types.ProductionEvidenceSnapshot),
	}
	request := types.JSON(`{"source_item_id":"` + adapterSourceItemID + `","resource_ids":["resource-b","resource-a"]}`)
	request, err = types.CanonicalProductionJSON(request)
	require.NoError(t, err)
	call := &types.ProductionToolCall{
		ID: adapterCallID, RunID: adapterRunID, TenantID: 7, ProjectID: adapterProjectID,
		DocumentID: adapterDocumentID, SourceSetID: adapterSourceSetID, Attempt: 2, CurrentStep: 3,
		ProviderType: types.ProductionToolProviderDatasource, ProviderID: adapterDataSourceID,
		ToolName: productionDataSourceFetchToolName, RequestSnapshot: request, RequestDigest: adapterDigest(request),
		Status: types.ProductionToolCallExecuting,
	}
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(connector))
	adapter := NewProductionDataSourceAdapter(
		&fakeProductionDataSourceService{dataSource: &types.DataSource{
			ID: adapterDataSourceID, TenantID: 7, Type: types.ConnectorTypeNotion, Config: config,
		}},
		registry,
		scope,
		scope,
		&fakeProductionEvidenceService{scope: scope},
	)
	return adapter, call, scope
}

func TestProductionDataSourceAdapterPlanIsSideEffectFreeAndCredentialFree(t *testing.T) {
	connector := &fakeProductionConnector{}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	call.Status = types.ProductionToolCallPlanned
	dataSources := adapter.dataSources.(*fakeProductionDataSourceService)

	plan, err := adapter.Plan(context.Background(), call)
	require.NoError(t, err)
	require.Zero(t, connector.fetchCalls)
	require.Zero(t, connector.listCalls)
	require.Zero(t, dataSources.calls)
	require.NotContains(t, string(plan.Canonical), "credential-value")
	require.NotContains(t, string(plan.Canonical), "access_token")
	require.Equal(t, adapterDigest(plan.Canonical), plan.Digest)
}

func TestProductionDataSourceAdapterRecursivelyRedactsSecretKeys(t *testing.T) {
	connector := &fakeProductionConnector{listResult: []types.Resource{{
		ExternalID: "doc-1", Name: "Doc", Type: "page",
		Metadata: map[string]any{
			"title":        "legitimate secretariat tokenization passwordless title",
			"Access-Token": "secret-1",
			"nested":       []any{map[string]any{"client.secret": "secret-2", "apiKey": "secret-3", "safe": "ok"}},
		},
	}}}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	call.ToolName = productionDataSourceListToolName
	request := types.JSON(`{"parent_id":"resource-a","source_item_id":"` + adapterSourceItemID + `"}`)
	request, err := types.CanonicalProductionJSON(request)
	require.NoError(t, err)
	call.RequestSnapshot, call.RequestDigest = request, adapterDigest(request)

	result, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	content := string(result.Evidence.InlineContent)
	require.NotContains(t, content, "secret-1")
	require.NotContains(t, content, "secret-2")
	require.NotContains(t, content, "secret-3")
	require.NotContains(t, content, "Access-Token")
	require.Contains(t, content, "secretariat tokenization passwordless")
	require.Contains(t, content, `"safe":"ok"`)
	require.Equal(t, "credential-value", connector.seenToken)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(result.Evidence.RedactionMetadata, &metadata))
	require.Equal(t, float64(3), metadata["redacted_fields"])
}

func TestProductionDataSourceAdapterRejectsCredentialValueEchoAndSanitizesConnectorErrors(t *testing.T) {
	connector := &fakeProductionConnector{fetch: []types.FetchedItem{{ExternalID: "doc", Title: "credential-value"}}}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	_, err := adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionProviderOutputUnsafe)
	require.NotContains(t, err.Error(), "credential-value")

	connector.fetch = nil
	connector.err = errors.New("authorization Bearer credential-value")
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionProviderExecution)
	require.NotContains(t, err.Error(), "credential-value")
}

func TestProductionDataSourceAdapterFencesTenantProjectAndSourceSet(t *testing.T) {
	connector := &fakeProductionConnector{}
	adapter, call, scope := dataSourceAdapterFixture(t, connector)
	adapter.dataSources = &fakeProductionDataSourceService{dataSource: &types.DataSource{
		ID: adapterDataSourceID, TenantID: 8, Type: types.ConnectorTypeNotion, Config: types.JSON(`{}`),
	}}
	_, err := adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionToolScope)
	require.Zero(t, connector.fetchCalls)

	adapter, call, scope = dataSourceAdapterFixture(t, connector)
	scope.set.ProjectID = "10000000-0000-4000-8000-000000000099"
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionToolScope)
	require.Zero(t, connector.fetchCalls)
}

func TestProductionDataSourceAdapterListRestrictsParentToConfiguredRoots(t *testing.T) {
	connector := &fakeProductionConnector{}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	call.ToolName = productionDataSourceListToolName
	request := types.JSON(`{"parent_id":"other-root","source_item_id":"` + adapterSourceItemID + `"}`)
	request, err := types.CanonicalProductionJSON(request)
	require.NoError(t, err)
	call.RequestSnapshot, call.RequestDigest = request, adapterDigest(request)
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionToolScope)
	require.Zero(t, connector.listCalls)

	request = types.JSON(`{"parent_id":"resource-a","source_item_id":"` + adapterSourceItemID + `"}`)
	request, err = types.CanonicalProductionJSON(request)
	require.NoError(t, err)
	call.RequestSnapshot, call.RequestDigest = request, adapterDigest(request)
	_, err = adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, 1, connector.listCalls)
}

func TestProductionDataSourceAdapterNormalizesOutputAndRetryIdentityDeterministically(t *testing.T) {
	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.FixedZone("offset", 8*60*60))
	connector := &fakeProductionConnector{fetch: []types.FetchedItem{
		{ExternalID: "b", Title: "B", Content: []byte("two"), ContentType: "text/plain", UpdatedAt: now},
		{ExternalID: "a", Title: "A", Content: []byte("one"), ContentType: "text/plain", UpdatedAt: now.UTC()},
	}}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)

	first, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	connector.fetch = []types.FetchedItem{{ExternalID: "changed", Title: "Provider changed"}}
	second, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, connector.fetchCalls)
	require.Equal(t, adapterDigest(first.Evidence.InlineContent), first.Evidence.ContentDigest)
	require.Equal(t, first.ResponseDigest, adapterDigest(first.ResponseSnapshot))
	require.NotEmpty(t, first.ProviderDigest)
	require.Equal(t, adapterCallID, first.ToolCallID)
}

func TestProductionDataSourceAdapterSecretValuePolicyAvoidsShortSubstringFalsePositive(t *testing.T) {
	connector := &fakeProductionConnector{fetch: []types.FetchedItem{{ExternalID: "item-1", Title: "documentary tokenized content"}}}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	config := types.DataSourceConfig{
		Type: types.ConnectorTypeNotion, Credentials: map[string]any{"token": "doc"},
		ResourceIDs: []string{"resource-a", "resource-b"}, Settings: map[string]any{"safe": true},
	}
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	adapter.dataSources = &fakeProductionDataSourceService{dataSource: &types.DataSource{
		ID: adapterDataSourceID, TenantID: 7, Type: types.ConnectorTypeNotion, Config: raw,
	}}
	_, err = adapter.Execute(context.Background(), call)
	require.NoError(t, err)

	longToken := "long-token-1234567890-ABCDEFG"
	config.Credentials = map[string]any{"auth_headers": "Bearer " + longToken, "app_secret": longToken}
	raw, err = json.Marshal(config)
	require.NoError(t, err)
	adapter.dataSources = &fakeProductionDataSourceService{dataSource: &types.DataSource{
		ID: adapterDataSourceID, TenantID: 7, Type: types.ConnectorTypeNotion, Config: raw,
	}}
	connector.fetch = []types.FetchedItem{{ExternalID: "doc", URL: "https://example.test/?key=" + longToken}}
	call.ID = "10000000-0000-4000-8000-000000000008"
	_, err = adapter.Execute(context.Background(), call)
	require.ErrorIs(t, err, errProductionProviderOutputUnsafe)
}

func TestProductionDataSourceAdapterRejectsNonExecutableLifecycleBeforeProviderAccess(t *testing.T) {
	connector := &fakeProductionConnector{}
	adapter, call, _ := dataSourceAdapterFixture(t, connector)
	for _, status := range []types.ProductionToolCallStatus{
		types.ProductionToolCallPlanned, types.ProductionToolCallPendingApproval,
		types.ProductionToolCallRejected, types.ProductionToolCallCompleted, types.ProductionToolCallFailed,
	} {
		copy := *call
		copy.Status = status
		_, err := adapter.Execute(context.Background(), &copy)
		require.ErrorIs(t, err, errProductionToolCallInvalid)
	}
	require.Zero(t, connector.fetchCalls)
	require.Zero(t, connector.listCalls)
}

func TestProductionDataSourceAdapterRejectsNilMalformedAndSecretRequests(t *testing.T) {
	adapter, call, _ := dataSourceAdapterFixture(t, &fakeProductionConnector{})
	_, err := adapter.Execute(context.Background(), nil)
	require.ErrorIs(t, err, errProductionToolCallInvalid)

	malformed := *call
	malformed.ProviderID = "not-a-uuid"
	_, err = adapter.Execute(context.Background(), &malformed)
	require.ErrorIs(t, err, errProductionToolCallInvalid)

	unsafe := *call
	unsafe.RequestSnapshot = types.JSON(`{"access.token":"never-store","source_item_id":"` + adapterSourceItemID + `"}`)
	unsafe.RequestDigest = adapterDigest(unsafe.RequestSnapshot)
	_, err = adapter.Execute(context.Background(), &unsafe)
	require.ErrorIs(t, err, errProductionToolCallInvalid)
	require.NotContains(t, err.Error(), "never-store")
}
