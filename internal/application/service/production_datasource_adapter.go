package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	productionDataSourceFetchToolName = "fetch_all"
	productionDataSourceListToolName  = "list_resources"
)

type productionDataSourceResolver interface {
	GetDataSource(ctx context.Context, id string) (*types.DataSource, error)
}

// ProductionDataSourceAdapter resolves stored credentials only for the live
// connector call. Plans, results and evidence contain no DataSource.Config.
type ProductionDataSourceAdapter struct {
	dataSources productionDataSourceResolver
	registry    *datasource.ConnectorRegistry
	scope       productionToolScope
}

func NewProductionDataSourceAdapter(
	dataSources productionDataSourceResolver,
	registry *datasource.ConnectorRegistry,
	runs productionToolRunResolver,
	sources productionToolSourceResolver,
) *ProductionDataSourceAdapter {
	return &ProductionDataSourceAdapter{
		dataSources: dataSources, registry: registry,
		scope: productionToolScope{runs: runs, sources: sources},
	}
}

type productionDataSourceRequest struct {
	SourceItemID string   `json:"source_item_id"`
	ResourceIDs  []string `json:"resource_ids,omitempty"`
	ParentID     string   `json:"parent_id,omitempty"`
}

type productionDataSourcePrepared struct {
	plan                  *ProductionToolPlan
	request               productionDataSourceRequest
	connector             datasource.Connector
	config                *types.DataSourceConfig
	credentialValues      []string
	providerRedactedCount int
	item                  *types.ProductionSourceItem
}

func (a *ProductionDataSourceAdapter) Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error) {
	prepared, err := a.prepare(ctx, call)
	if err != nil {
		return nil, err
	}
	return prepared.plan, nil
}

func (a *ProductionDataSourceAdapter) Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	prepared, err := a.prepare(ctx, call)
	if err != nil {
		return nil, err
	}
	var providerOutput any
	switch call.ToolName {
	case productionDataSourceFetchToolName:
		items, fetchErr := prepared.connector.FetchAll(ctx, prepared.config, append([]string(nil), prepared.request.ResourceIDs...))
		if fetchErr != nil {
			return nil, errProductionProviderExecution
		}
		providerOutput = normalizeProductionFetchedItems(items)
	case productionDataSourceListToolName:
		resources, listErr := prepared.connector.ListResources(ctx, prepared.config, prepared.request.ParentID)
		if listErr != nil {
			return nil, errProductionProviderExecution
		}
		providerOutput = normalizeProductionResources(resources)
	default:
		return nil, errProductionToolCallInvalid
	}
	cleaned, redactedCount, err := redactProductionSecrets(providerOutput)
	if err != nil || containsProductionCredentialValue(cleaned, prepared.credentialValues) {
		return nil, errProductionProviderOutputUnsafe
	}
	content, err := canonicalProductionValue(cleaned)
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	metadata, err := canonicalProductionValue(map[string]any{
		"adapter":                  "datasource",
		"provider_digest":          prepared.plan.ProviderDigest,
		"provider_redacted_fields": prepared.providerRedactedCount,
		"redacted":                 redactedCount > 0,
		"redacted_fields":          redactedCount,
	})
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	evidence := newProductionToolEvidence(call, prepared.item.ID, content, metadata)
	response, err := canonicalProductionResponse(content, prepared.plan.ProviderDigest, metadata, evidence)
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	return &ProductionToolResult{
		ToolCallID: call.ID, PlanDigest: prepared.plan.Digest,
		ResponseSnapshot: response, ResponseDigest: productionToolDigest(response),
		ProviderDigest: prepared.plan.ProviderDigest, RedactionMetadata: metadata, Evidence: evidence,
	}, nil
}

func (a *ProductionDataSourceAdapter) prepare(ctx context.Context, call *types.ProductionToolCall) (*productionDataSourcePrepared, error) {
	if a == nil || a.dataSources == nil || a.registry == nil {
		return nil, errProductionProviderConfiguration
	}
	if err := validateProductionToolCall(
		call, types.ProductionToolProviderDatasource,
		productionDataSourceFetchToolName, productionDataSourceListToolName,
	); err != nil {
		return nil, err
	}
	if !canonicalProductionUUID(call.ProviderID) {
		return nil, errProductionToolCallInvalid
	}
	var request productionDataSourceRequest
	if err := decodeProductionToolRequest(call, &request); err != nil || !canonicalProductionUUID(request.SourceItemID) {
		return nil, errProductionToolCallInvalid
	}
	_, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindDatasource)
	if err != nil || item.ExternalID != call.ProviderID {
		return nil, errProductionToolScope
	}
	dataSource, err := a.dataSources.GetDataSource(ctx, call.ProviderID)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	if dataSource == nil || dataSource.ID != call.ProviderID || dataSource.TenantID != call.TenantID ||
		strings.TrimSpace(dataSource.Type) == "" || item.SourceSystem != dataSource.Type {
		return nil, errProductionToolScope
	}
	config, err := dataSource.ParseConfig()
	if err != nil || config == nil || config.Type != dataSource.Type {
		return nil, errProductionProviderConfiguration
	}
	connector, err := a.registry.Get(dataSource.Type)
	if err != nil || connector == nil || connector.Type() != dataSource.Type {
		return nil, errProductionProviderConfiguration
	}
	if err := validateProductionDataSourceRequest(call.ToolName, request, config.ResourceIDs); err != nil {
		return nil, err
	}
	credentialValues := productionCredentialValues(config.Credentials)
	providerDescriptor, redactedCount, err := redactProductionSecrets(map[string]any{
		"connector_type": config.Type,
		"datasource_id":  dataSource.ID,
		"resource_ids":   sortedProductionStrings(config.ResourceIDs),
		"settings":       config.Settings,
	})
	if err != nil || containsProductionCredentialValue(providerDescriptor, credentialValues) {
		return nil, errProductionProviderConfiguration
	}
	providerCanonical, err := canonicalProductionValue(providerDescriptor)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	plan, err := newProductionToolPlan(call, productionToolDigest(providerCanonical))
	if err != nil {
		return nil, errProductionToolCallInvalid
	}
	return &productionDataSourcePrepared{
		plan: plan, request: request, connector: connector, config: config,
		credentialValues: credentialValues, providerRedactedCount: redactedCount, item: item,
	}, nil
}

func validateProductionDataSourceRequest(toolName string, request productionDataSourceRequest, configured []string) error {
	switch toolName {
	case productionDataSourceFetchToolName:
		if request.ParentID != "" || len(request.ResourceIDs) == 0 {
			return errProductionToolCallInvalid
		}
		allowed := make(map[string]struct{}, len(configured))
		for _, id := range configured {
			allowed[id] = struct{}{}
		}
		seen := make(map[string]struct{}, len(request.ResourceIDs))
		for _, id := range request.ResourceIDs {
			if strings.TrimSpace(id) == "" {
				return errProductionToolCallInvalid
			}
			if _, ok := allowed[id]; !ok {
				return errProductionToolScope
			}
			if _, duplicate := seen[id]; duplicate {
				return errProductionToolCallInvalid
			}
			seen[id] = struct{}{}
		}
	case productionDataSourceListToolName:
		if len(request.ResourceIDs) != 0 {
			return errProductionToolCallInvalid
		}
	default:
		return errProductionToolCallInvalid
	}
	return nil
}

type normalizedProductionFetchedItem struct {
	ExternalID  string            `json:"external_id"`
	Title       string            `json:"title"`
	Content     string            `json:"content"`
	ContentType string            `json:"content_type"`
	FileName    string            `json:"file_name"`
	URL         string            `json:"url"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	IsDeleted   bool              `json:"is_deleted"`
}

func normalizeProductionFetchedItems(items []types.FetchedItem) []normalizedProductionFetchedItem {
	normalized := make([]normalizedProductionFetchedItem, 0, len(items))
	for _, item := range items {
		normalized = append(normalized, normalizedProductionFetchedItem{
			ExternalID: item.ExternalID, Title: item.Title, Content: string(item.Content),
			ContentType: item.ContentType, FileName: item.FileName, URL: item.URL,
			UpdatedAt: canonicalProductionTime(item.UpdatedAt), Metadata: item.Metadata, IsDeleted: item.IsDeleted,
		})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].ExternalID != normalized[j].ExternalID {
			return normalized[i].ExternalID < normalized[j].ExternalID
		}
		if normalized[i].URL != normalized[j].URL {
			return normalized[i].URL < normalized[j].URL
		}
		return normalized[i].Title < normalized[j].Title
	})
	return normalized
}

type normalizedProductionResource struct {
	ExternalID  string         `json:"external_id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	ModifiedAt  string         `json:"modified_at,omitempty"`
	ParentID    string         `json:"parent_id,omitempty"`
	HasChildren bool           `json:"has_children,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func normalizeProductionResources(resources []types.Resource) []normalizedProductionResource {
	normalized := make([]normalizedProductionResource, 0, len(resources))
	for _, resource := range resources {
		normalized = append(normalized, normalizedProductionResource{
			ExternalID: resource.ExternalID, Name: resource.Name, Type: resource.Type,
			Description: resource.Description, URL: resource.URL, ModifiedAt: canonicalProductionTime(resource.ModifiedAt),
			ParentID: resource.ParentID, HasChildren: resource.HasChildren, Metadata: resource.Metadata,
		})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].ExternalID != normalized[j].ExternalID {
			return normalized[i].ExternalID < normalized[j].ExternalID
		}
		if normalized[i].Type != normalized[j].Type {
			return normalized[i].Type < normalized[j].Type
		}
		return normalized[i].Name < normalized[j].Name
	})
	return normalized
}

func canonicalProductionTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func sortedProductionStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func redactProductionSecrets(value any) (any, int, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, 0, err
	}
	var decoded any
	if err := decodeProductionJSON(raw, &decoded, false); err != nil {
		return nil, 0, err
	}
	return redactProductionDecodedValue(decoded)
}

func redactProductionDecodedValue(value any) (any, int, error) {
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		redacted := 0
		for key, nested := range typed {
			if productionSecretKey(key) {
				redacted++
				continue
			}
			cleanNested, nestedCount, err := redactProductionDecodedValue(nested)
			if err != nil {
				return nil, 0, err
			}
			cleaned[key] = cleanNested
			redacted += nestedCount
		}
		return cleaned, redacted, nil
	case []any:
		cleaned := make([]any, len(typed))
		redacted := 0
		for index, nested := range typed {
			cleanNested, nestedCount, err := redactProductionDecodedValue(nested)
			if err != nil {
				return nil, 0, err
			}
			cleaned[index] = cleanNested
			redacted += nestedCount
		}
		return cleaned, redacted, nil
	default:
		return value, 0, nil
	}
}

func productionCredentialValues(credentials map[string]any) []string {
	values := make([]string, 0)
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				values = append(values, typed)
			}
		case map[string]any:
			for _, nested := range typed {
				collect(nested)
			}
		case []any:
			for _, nested := range typed {
				collect(nested)
			}
		}
	}
	collect(credentials)
	sort.Strings(values)
	return values
}

func containsProductionCredentialValue(value any, credentials []string) bool {
	switch typed := value.(type) {
	case string:
		for _, credential := range credentials {
			if credential != "" && strings.Contains(typed, credential) {
				return true
			}
		}
	case map[string]any:
		for _, nested := range typed {
			if containsProductionCredentialValue(nested, credentials) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsProductionCredentialValue(nested, credentials) {
				return true
			}
		}
	}
	return false
}

var _ ProductionToolAdapter = (*ProductionDataSourceAdapter)(nil)
