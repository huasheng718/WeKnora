package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type productionMCPApprovalPolicy interface {
	IsRequired(ctx context.Context, tenantID uint64, serviceID, toolName string) (bool, error)
}

type productionMCPServiceResolver interface {
	GetByID(ctx context.Context, tenantID uint64, id string) (*types.MCPService, error)
}

type productionMCPClientManager interface {
	GetOrCreateClient(ctx context.Context, service *types.MCPService) (mcp.MCPClient, error)
}

// ProductionMCPAdapter turns one persisted MCP call into immutable evidence.
// Provider credentials are resolved only after all durable scope and digest
// fences pass and never enter plans, results, evidence, or returned errors.
type ProductionMCPAdapter struct {
	approvals productionMCPApprovalPolicy
	services  productionMCPServiceResolver
	manager   productionMCPClientManager
	scope     productionToolScope
	evidence  productionToolEvidenceService
}

func NewProductionMCPAdapter(
	approvals productionMCPApprovalPolicy,
	services productionMCPServiceResolver,
	manager productionMCPClientManager,
	runs productionToolRunResolver,
	sources productionToolSourceResolver,
	evidence productionToolEvidenceService,
) *ProductionMCPAdapter {
	return &ProductionMCPAdapter{
		approvals: approvals, services: services, manager: manager,
		scope: productionToolScope{runs: runs, sources: sources}, evidence: evidence,
	}
}

func NewProductionMCPAdapterFromServices(
	approvals interfaces.MCPToolApprovalService,
	services interfaces.MCPServiceRepository,
	manager *mcp.MCPManager,
	runs interfaces.ProductionRunRepository,
	sources interfaces.ProductionSourceRepository,
	evidence interfaces.ProductionSourceService,
) *ProductionMCPAdapter {
	return NewProductionMCPAdapter(approvals, services, manager, runs, sources, evidence)
}

type productionMCPRequest struct {
	SourceItemID     string         `json:"source_item_id"`
	Arguments        map[string]any `json:"arguments"`
	ProviderDigest   string         `json:"provider_digest,omitempty"`
	ApprovalRequired *bool          `json:"approval_required,omitempty"`
}

func (a *ProductionMCPAdapter) Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error) {
	if err := a.validateDependencies(false); err != nil {
		return nil, err
	}
	if call == nil || strings.TrimSpace(call.ToolName) == "" || len(call.ToolName) > 255 ||
		!canonicalProductionUUID(call.ProviderID) {
		return nil, errProductionToolCallInvalid
	}
	if err := validateProductionToolCall(call, types.ProductionToolProviderMCP, types.ProductionToolCallPlanned, call.ToolName); err != nil {
		return nil, err
	}
	var request productionMCPRequest
	if err := decodeProductionToolRequest(call, &request); err != nil ||
		!canonicalProductionUUID(request.SourceItemID) || request.Arguments == nil ||
		request.ProviderDigest != "" || request.ApprovalRequired != nil {
		return nil, errProductionToolCallInvalid
	}
	_, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindMCP)
	if err != nil || item.ExternalID != call.ProviderID {
		return nil, errProductionToolScope
	}
	service, err := a.authoritativeService(ctx, call)
	if err != nil {
		return nil, err
	}
	if containsProductionCredentialValue(request.Arguments, productionMCPCredentialValues(service)) {
		return nil, errProductionToolCallInvalid
	}
	required, err := a.approvals.IsRequired(ctx, call.TenantID, call.ProviderID, call.ToolName)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	providerDigest, err := productionMCPProviderDigest(service)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	request.ProviderDigest = providerDigest
	request.ApprovalRequired = &required
	canonical, err := canonicalProductionValue(request)
	if err != nil || types.RejectProductionCredentialFields(canonical) != nil {
		return nil, errProductionToolCallInvalid
	}
	planned := *call
	planned.RequestSnapshot = canonical
	planned.RequestDigest = productionToolDigest(canonical)
	plan, err := newProductionToolPlan(&planned, providerDigest)
	if err != nil {
		return nil, err
	}
	plan.RequiresApproval = required
	return plan, nil
}

func (a *ProductionMCPAdapter) Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	if err := a.validateDependencies(true); err != nil {
		return nil, err
	}
	request, run, item, plan, err := a.executablePlan(ctx, call)
	if err != nil {
		return nil, err
	}
	if result, found, replayErr := a.persistedResult(ctx, call, plan, item, request.ProviderDigest); replayErr != nil || found {
		return result, replayErr
	}
	if call.Attempt < run.Attempt && call.StartedAt != nil {
		return nil, types.ErrProductionToolReconciliationRequired
	}
	service, err := a.authoritativeService(ctx, call)
	if err != nil {
		return nil, err
	}
	providerDigest, err := productionMCPProviderDigest(service)
	if err != nil || providerDigest != request.ProviderDigest {
		return nil, errProductionProviderDigestMismatch
	}
	required, err := a.approvals.IsRequired(ctx, call.TenantID, call.ProviderID, call.ToolName)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	if request.ApprovalRequired == nil || *request.ApprovalRequired != required {
		return nil, errProductionProviderDigestMismatch
	}
	client, err := a.manager.GetOrCreateClient(ctx, service)
	if err != nil || client == nil || client.GetServiceID() != service.ID {
		return nil, errProductionProviderExecution
	}
	providerResult, err := client.CallTool(ctx, call.ToolName, request.Arguments)
	if err != nil || providerResult == nil || providerResult.IsError {
		return nil, errProductionProviderExecution
	}
	cleaned, redactedCount, err := redactProductionSecrets(providerResult)
	credentials := productionMCPCredentialValues(service)
	if err != nil || containsProductionCredentialValue(cleaned, credentials) {
		return nil, errProductionProviderOutputUnsafe
	}
	content, err := canonicalProductionValue(cleaned)
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	metadata, err := canonicalProductionValue(map[string]any{
		"adapter": "mcp", "provider_digest": providerDigest,
		"redacted": redactedCount > 0, "redacted_fields": redactedCount,
	})
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	persisted, err := a.evidence.AttachEvidence(ctx, item.ID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: productionToolEvidenceID(call), SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: content, ContentDigest: productionToolDigest(content), RedactionMetadata: metadata,
		CapturedByRunID: call.RunID, CapturedByToolCallID: call.ID,
	})
	if err != nil {
		return nil, err
	}
	return productionToolResultFromEvidence(call, plan, persisted, providerDigest)
}

func (a *ProductionMCPAdapter) executablePlan(
	ctx context.Context,
	call *types.ProductionToolCall,
) (productionMCPRequest, *types.ProductionRun, *types.ProductionSourceItem, *ProductionToolPlan, error) {
	var request productionMCPRequest
	if call == nil || strings.TrimSpace(call.ToolName) == "" || len(call.ToolName) > 255 {
		return request, nil, nil, nil, errProductionToolCallInvalid
	}
	if call.Status != types.ProductionToolCallApproved && call.Status != types.ProductionToolCallExecuting {
		return request, nil, nil, nil, errProductionToolCallInvalid
	}
	status := call.Status
	if err := validateProductionToolCall(call, types.ProductionToolProviderMCP, status, call.ToolName); err != nil {
		return request, nil, nil, nil, err
	}
	if err := decodeProductionToolRequest(call, &request); err != nil ||
		!canonicalProductionUUID(request.SourceItemID) || request.Arguments == nil ||
		!canonicalProductionSHA256(request.ProviderDigest) || request.ApprovalRequired == nil {
		return request, nil, nil, nil, errProductionToolCallInvalid
	}
	if *request.ApprovalRequired {
		if call.ApprovalStatus != types.ProductionToolApprovalApproved {
			return request, nil, nil, nil, errProductionToolCallInvalid
		}
	} else if call.ApprovalStatus != types.ProductionToolApprovalNotRequired {
		return request, nil, nil, nil, errProductionToolCallInvalid
	}
	run, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindMCP)
	if err != nil || item.ExternalID != call.ProviderID {
		return request, nil, nil, nil, errProductionToolScope
	}
	plan, err := newProductionToolPlan(call, request.ProviderDigest)
	if err == nil {
		plan.RequiresApproval = *request.ApprovalRequired
	}
	return request, run, item, plan, err
}

func (a *ProductionMCPAdapter) persistedResult(
	ctx context.Context,
	call *types.ProductionToolCall,
	plan *ProductionToolPlan,
	item *types.ProductionSourceItem,
	providerDigest string,
) (*ProductionToolResult, bool, error) {
	evidence, evidenceItem, evidenceSet, err := a.scope.sources.GetEvidence(ctx, call.TenantID, productionToolEvidenceID(call))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if evidenceItem == nil || evidenceSet == nil || evidenceItem.ID != item.ID ||
		evidenceItem.SourceSetID != call.SourceSetID || evidenceSet.ID != call.SourceSetID ||
		evidenceSet.TenantID != call.TenantID || evidenceSet.ProjectID != call.ProjectID {
		return nil, false, types.ErrProductionEvidenceConflict
	}
	result, err := productionToolResultFromEvidence(call, plan, evidence, providerDigest)
	return result, err == nil, err
}

func (a *ProductionMCPAdapter) authoritativeService(ctx context.Context, call *types.ProductionToolCall) (*types.MCPService, error) {
	service, err := a.services.GetByID(ctx, call.TenantID, call.ProviderID)
	if err != nil || service == nil || service.ID != call.ProviderID || !service.Enabled ||
		(service.TenantID != call.TenantID && !service.IsBuiltin) {
		return nil, errProductionProviderConfiguration
	}
	return service, nil
}

func (a *ProductionMCPAdapter) validateDependencies(execute bool) error {
	if a == nil || a.approvals == nil || a.services == nil || a.scope.runs == nil || a.scope.sources == nil {
		return errProductionProviderConfiguration
	}
	if execute && (a.manager == nil || a.evidence == nil) {
		return errProductionProviderConfiguration
	}
	return nil
}

func productionMCPProviderDigest(service *types.MCPService) (string, error) {
	if service == nil {
		return "", errProductionProviderConfiguration
	}
	configuration, err := canonicalProductionValue(service)
	if err != nil {
		return "", err
	}
	endpointDigest := ""
	if service.URL != nil {
		endpointDigest = productionToolDigest(types.JSON(*service.URL))
	}
	snapshot, err := canonicalProductionValue(map[string]any{
		"configuration_digest": productionToolDigest(configuration),
		"enabled":              service.Enabled, "endpoint_digest": endpointDigest,
		"id": service.ID, "name": service.Name, "tenant_id": service.TenantID,
		"transport_type": service.TransportType,
	})
	if err != nil {
		return "", err
	}
	return productionToolDigest(snapshot), nil
}

func productionMCPCredentialValues(service *types.MCPService) []productionCredentialValue {
	if service == nil {
		return nil
	}
	values := map[string]any{
		"headers":  productionMCPStringMap(service.Headers),
		"env_vars": productionMCPStringMap(service.EnvVars),
	}
	if service.AuthConfig != nil {
		values["auth_config"] = map[string]any{
			"api_key": service.AuthConfig.APIKey, "token": service.AuthConfig.Token,
			"custom_headers": productionMCPStringMap(service.AuthConfig.CustomHeaders),
		}
	}
	if service.URL != nil {
		if parsed, err := url.Parse(*service.URL); err == nil {
			query := make(map[string]any)
			for key, entry := range parsed.Query() {
				values := make([]any, len(entry))
				for index := range entry {
					values[index] = entry[index]
				}
				query[key] = values
			}
			values["url_query"] = query
			if parsed.User != nil {
				password, _ := parsed.User.Password()
				values["url_user"] = map[string]string{"username": parsed.User.Username(), "password": password}
			}
		}
	}
	return productionCredentialValues(values)
}

func productionMCPStringMap[T ~map[string]string](input T) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

var _ ProductionToolAdapter = (*ProductionMCPAdapter)(nil)

// Keep the compiler checking that the concrete manager remains compatible
// with the narrow injected provider boundary.
var _ productionMCPClientManager = (*mcp.MCPManager)(nil)
