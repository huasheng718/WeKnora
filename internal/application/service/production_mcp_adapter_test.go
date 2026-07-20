package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	mcpAdapterRunID       = "71000000-0000-4000-8000-000000000001"
	mcpAdapterProjectID   = "71000000-0000-4000-8000-000000000002"
	mcpAdapterDocumentID  = "71000000-0000-4000-8000-000000000003"
	mcpAdapterSourceSetID = "71000000-0000-4000-8000-000000000004"
	mcpAdapterSourceID    = "71000000-0000-4000-8000-000000000005"
	mcpAdapterServiceID   = "71000000-0000-4000-8000-000000000006"
	mcpAdapterCallID      = "71000000-0000-4000-8000-000000000007"
)

type productionMCPApprovalStub struct {
	required bool
	err      error
	calls    int
}

func (s *productionMCPApprovalStub) IsRequired(context.Context, uint64, string, string) (bool, error) {
	s.calls++
	return s.required, s.err
}

type productionMCPServiceStub struct {
	service *types.MCPService
	err     error
	calls   int
}

func (s *productionMCPServiceStub) GetByID(context.Context, uint64, string) (*types.MCPService, error) {
	s.calls++
	return s.service, s.err
}

type productionMCPManagerStub struct {
	client mcp.MCPClient
	err    error
	calls  int
}

func (s *productionMCPManagerStub) GetOrCreateClient(context.Context, *types.MCPService) (mcp.MCPClient, error) {
	s.calls++
	return s.client, s.err
}

type productionMCPClientStub struct {
	serviceID string
	result    *mcp.CallToolResult
	err       error
	calls     int
}

func (s *productionMCPClientStub) Connect(context.Context) error { return nil }
func (s *productionMCPClientStub) Disconnect() error             { return nil }
func (s *productionMCPClientStub) Initialize(context.Context) (*mcp.InitializeResult, error) {
	return &mcp.InitializeResult{}, nil
}
func (s *productionMCPClientStub) ListTools(context.Context) ([]*types.MCPTool, error) {
	return nil, nil
}
func (s *productionMCPClientStub) ListResources(context.Context) ([]*types.MCPResource, error) {
	return nil, nil
}
func (s *productionMCPClientStub) CallTool(context.Context, string, map[string]interface{}) (*mcp.CallToolResult, error) {
	s.calls++
	return s.result, s.err
}
func (s *productionMCPClientStub) ReadResource(context.Context, string) (*mcp.ReadResourceResult, error) {
	return nil, nil
}
func (s *productionMCPClientStub) IsConnected() bool    { return true }
func (s *productionMCPClientStub) GetServiceID() string { return s.serviceID }

type productionMCPSourceStub struct {
	run      *types.ProductionRun
	item     *types.ProductionSourceItem
	set      *types.ProductionSourceSet
	evidence *types.ProductionEvidenceSnapshot
}

func (s *productionMCPSourceStub) Get(context.Context, uint64, string) (*types.ProductionRun, error) {
	return s.run, nil
}
func (s *productionMCPSourceStub) GetItem(context.Context, uint64, string) (*types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	return s.item, s.set, nil
}
func (s *productionMCPSourceStub) GetEvidence(context.Context, uint64, string) (*types.ProductionEvidenceSnapshot, *types.ProductionSourceItem, *types.ProductionSourceSet, error) {
	if s.evidence == nil {
		return nil, nil, nil, gorm.ErrRecordNotFound
	}
	return s.evidence, s.item, s.set, nil
}

type productionMCPEvidenceStub struct {
	scope *productionMCPSourceStub
	calls int
}

func (s *productionMCPEvidenceStub) AttachEvidence(_ context.Context, itemID string, input interfaces.CreateEvidenceSnapshotInput) (*types.ProductionEvidenceSnapshot, error) {
	s.calls++
	evidence := &types.ProductionEvidenceSnapshot{
		ID: input.EvidenceID, SourceItemID: itemID, SnapshotType: input.SnapshotType,
		InlineContent: input.InlineContent, ContentDigest: input.ContentDigest,
		RedactionMetadata: input.RedactionMetadata, CapturedByRunID: input.CapturedByRunID,
	}
	s.scope.evidence = evidence
	return evidence, nil
}

func productionMCPFixture(t *testing.T, required bool) (*ProductionMCPAdapter, *types.ProductionToolCall, *productionMCPManagerStub, *productionMCPClientStub, *productionMCPSourceStub) {
	t.Helper()
	request, err := canonicalProductionValue(map[string]any{
		"arguments":      map[string]any{"query": "status"},
		"source_item_id": mcpAdapterSourceID,
	})
	require.NoError(t, err)
	call := &types.ProductionToolCall{
		ID: mcpAdapterCallID, RunID: mcpAdapterRunID, TenantID: 7,
		ProjectID: mcpAdapterProjectID, DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID,
		Attempt: 1, CurrentStep: 0, ProviderType: types.ProductionToolProviderMCP,
		ProviderID: mcpAdapterServiceID, ToolName: "lookup", RequestSnapshot: request,
		RequestDigest: productionToolDigest(request), Status: types.ProductionToolCallPlanned,
		ApprovalStatus: types.ProductionToolApprovalNotRequired,
	}
	scope := &productionMCPSourceStub{
		run:  &types.ProductionRun{ID: mcpAdapterRunID, TenantID: 7, ProjectID: mcpAdapterProjectID, DocumentID: mcpAdapterDocumentID, SourceSetID: mcpAdapterSourceSetID, Attempt: 1, CurrentStep: 0},
		item: &types.ProductionSourceItem{ID: mcpAdapterSourceID, SourceSetID: mcpAdapterSourceSetID, SourceKind: types.ProductionSourceKindMCP, ExternalID: mcpAdapterServiceID},
		set:  &types.ProductionSourceSet{ID: mcpAdapterSourceSetID, TenantID: 7, ProjectID: mcpAdapterProjectID},
	}
	client := &productionMCPClientStub{serviceID: mcpAdapterServiceID, result: &mcp.CallToolResult{Content: []mcp.ContentItem{{Type: "text", Text: "healthy"}}}}
	manager := &productionMCPManagerStub{client: client}
	service := &types.MCPService{ID: mcpAdapterServiceID, TenantID: 7, Name: "ops", Enabled: true, TransportType: types.MCPTransportHTTPStreamable, Headers: types.MCPHeaders{"Authorization": "Bearer super-secret-value"}}
	adapter := NewProductionMCPAdapter(
		&productionMCPApprovalStub{required: required}, &productionMCPServiceStub{service: service},
		manager, scope, scope, &productionMCPEvidenceStub{scope: scope},
	)
	return adapter, call, manager, client, scope
}

func TestProductionMCPPlanSnapshotsApprovalWithoutProviderAccessOrSecrets(t *testing.T) {
	adapter, call, manager, client, _ := productionMCPFixture(t, true)

	plan, err := adapter.Plan(context.Background(), call)

	require.NoError(t, err)
	require.True(t, plan.RequiresApproval)
	require.Zero(t, manager.calls)
	require.Zero(t, client.calls)
	require.NotContains(t, string(plan.RequestSnapshot), "super-secret-value")
	require.NotContains(t, strings.ToLower(string(plan.RequestSnapshot)), "authorization")
}

func TestProductionMCPExecutePersistsEvidenceAndReplaysWithoutProviderAccess(t *testing.T) {
	adapter, call, manager, client, _ := productionMCPFixture(t, true)
	plan, err := adapter.Plan(context.Background(), call)
	require.NoError(t, err)
	call.RequestSnapshot = plan.RequestSnapshot
	call.RequestDigest = plan.RequestDigest
	call.Status = types.ProductionToolCallExecuting
	call.ApprovalStatus = types.ProductionToolApprovalApproved

	first, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	second, err := adapter.Execute(context.Background(), call)
	require.NoError(t, err)
	require.Equal(t, first.ResponseDigest, second.ResponseDigest)
	require.Equal(t, 1, manager.calls)
	require.Equal(t, 1, client.calls)
}

func TestProductionMCPRejectsPolicyFailureMutationAndCredentialLeaks(t *testing.T) {
	t.Run("policy failure", func(t *testing.T) {
		adapter, call, _, _, _ := productionMCPFixture(t, false)
		adapter.approvals = &productionMCPApprovalStub{err: errors.New("policy store unavailable")}
		_, err := adapter.Plan(context.Background(), call)
		require.ErrorIs(t, err, errProductionProviderConfiguration)
	})

	t.Run("provider mismatch", func(t *testing.T) {
		adapter, call, manager, _, _ := productionMCPFixture(t, false)
		plan, err := adapter.Plan(context.Background(), call)
		require.NoError(t, err)
		call.RequestSnapshot, call.RequestDigest = plan.RequestSnapshot, plan.RequestDigest
		call.Status = types.ProductionToolCallExecuting
		adapter.services.(*productionMCPServiceStub).service.Name = "mutated"
		_, err = adapter.Execute(context.Background(), call)
		require.ErrorIs(t, err, errProductionProviderDigestMismatch)
		require.Zero(t, manager.calls)
	})

	t.Run("credential echo", func(t *testing.T) {
		adapter, call, _, client, _ := productionMCPFixture(t, false)
		plan, err := adapter.Plan(context.Background(), call)
		require.NoError(t, err)
		call.RequestSnapshot, call.RequestDigest = plan.RequestSnapshot, plan.RequestDigest
		call.Status = types.ProductionToolCallExecuting
		client.result.Content[0].Text = "Bearer super-secret-value"
		_, err = adapter.Execute(context.Background(), call)
		require.ErrorIs(t, err, errProductionProviderOutputUnsafe)
		require.NotContains(t, err.Error(), "super-secret-value")
	})
}

var _ mcp.MCPClient = (*productionMCPClientStub)(nil)
