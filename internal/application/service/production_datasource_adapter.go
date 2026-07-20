package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

const (
	productionDataSourceFetchToolName = "fetch_all"
	productionDataSourceListToolName  = "list_resources"
)

type productionDataSourceResolver interface {
	GetDataSource(ctx context.Context, id string) (*types.DataSource, error)
}

type productionConnectorRegistry interface {
	Get(connectorType string) (datasource.Connector, error)
}

// ProductionDataSourceAdapter resolves stored credentials only for the live
// connector call. Plans, results and evidence contain no DataSource.Config.
type ProductionDataSourceAdapter struct {
	dataSources productionDataSourceResolver
	registry    productionConnectorRegistry
	scope       productionToolScope
	evidence    productionToolEvidenceService
}

func NewProductionDataSourceAdapter(
	dataSources productionDataSourceResolver,
	registry productionConnectorRegistry,
	runs productionToolRunResolver,
	sources productionToolSourceResolver,
	evidence productionToolEvidenceService,
) *ProductionDataSourceAdapter {
	return &ProductionDataSourceAdapter{
		dataSources: dataSources, registry: registry,
		scope:    productionToolScope{runs: runs, sources: sources},
		evidence: evidence,
	}
}

func NewProductionDataSourceAdapterFromServices(
	dataSources interfaces.DataSourceService,
	registry *datasource.ConnectorRegistry,
	runs interfaces.ProductionRunRepository,
	sources interfaces.ProductionSourceRepository,
	evidence interfaces.ProductionSourceService,
) *ProductionDataSourceAdapter {
	return NewProductionDataSourceAdapter(dataSources, registry, runs, sources, evidence)
}

type productionDataSourceRequest struct {
	SourceItemID   string   `json:"source_item_id"`
	ResourceIDs    []string `json:"resource_ids,omitempty"`
	ParentID       string   `json:"parent_id,omitempty"`
	ProviderDigest string   `json:"provider_digest,omitempty"`
}

type productionDataSourcePrepared struct {
	plan                  *ProductionToolPlan
	request               productionDataSourceRequest
	connector             datasource.Connector
	config                *types.DataSourceConfig
	credentialValues      []productionCredentialValue
	providerRedactedCount int
	providerDigest        string
	item                  *types.ProductionSourceItem
}

func (a *ProductionDataSourceAdapter) Plan(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolPlan, error) {
	plan, _, _, err := a.plan(ctx, call, types.ProductionToolCallPlanned, true)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (a *ProductionDataSourceAdapter) Execute(ctx context.Context, call *types.ProductionToolCall) (*ProductionToolResult, error) {
	plan, request, item, err := a.plan(ctx, call, types.ProductionToolCallExecuting, false)
	if err != nil {
		return nil, err
	}
	if result, found, err := a.persistedResult(ctx, call, plan, item); err != nil || found {
		return result, err
	}
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
		"provider_digest":          prepared.providerDigest,
		"provider_redacted_fields": prepared.providerRedactedCount,
		"redacted":                 redactedCount > 0,
		"redacted_fields":          redactedCount,
	})
	if err != nil {
		return nil, errProductionProviderOutputUnsafe
	}
	persisted, err := a.evidence.AttachEvidence(ctx, prepared.item.ID, interfaces.CreateEvidenceSnapshotInput{
		EvidenceID: productionToolEvidenceID(call), SnapshotType: types.ProductionEvidenceSnapshotToolResult,
		InlineContent: content, ContentDigest: productionToolDigest(content),
		RedactionMetadata: metadata, CapturedByRunID: call.RunID, CapturedByToolCallID: call.ID,
	})
	if err != nil {
		return nil, err
	}
	_ = request
	return productionToolResultFromEvidence(call, plan, persisted, prepared.providerDigest)
}

func (a *ProductionDataSourceAdapter) plan(
	ctx context.Context,
	call *types.ProductionToolCall,
	status types.ProductionToolCallStatus,
	resolveProvider bool,
) (*ProductionToolPlan, productionDataSourceRequest, *types.ProductionSourceItem, error) {
	var request productionDataSourceRequest
	if a == nil || a.scope.runs == nil || a.scope.sources == nil {
		return nil, request, nil, errProductionProviderConfiguration
	}
	if err := validateProductionToolCall(call, types.ProductionToolProviderDatasource, status,
		productionDataSourceFetchToolName, productionDataSourceListToolName); err != nil {
		return nil, request, nil, err
	}
	if !canonicalProductionUUID(call.ProviderID) {
		return nil, request, nil, errProductionToolCallInvalid
	}
	if err := decodeProductionToolRequest(call, &request); err != nil || !canonicalProductionUUID(request.SourceItemID) {
		return nil, request, nil, errProductionToolCallInvalid
	}
	if err := validateProductionDataSourceRequestShape(call.ToolName, request); err != nil {
		return nil, request, nil, err
	}
	_, item, _, err := a.scope.resolve(ctx, call, request.SourceItemID, types.ProductionSourceKindDatasource)
	if err != nil || item.ExternalID != call.ProviderID {
		return nil, request, nil, errProductionToolScope
	}
	providerDigest := request.ProviderDigest
	if resolveProvider {
		dataSource, err := a.resolveDataSource(ctx, call, item)
		if err != nil {
			return nil, request, nil, err
		}
		providerDigest, err = productionDataSourceProviderDigest(dataSource)
		if err != nil {
			return nil, request, nil, errProductionProviderConfiguration
		}
		if request.ProviderDigest != "" && request.ProviderDigest != providerDigest {
			return nil, request, nil, errProductionProviderDigestMismatch
		}
	} else if !canonicalProductionSHA256(providerDigest) {
		return nil, request, nil, errProductionToolCallInvalid
	}
	plannedCall := call
	if resolveProvider {
		request.ProviderDigest = providerDigest
		pinnedSnapshot, err := canonicalProductionValue(request)
		if err != nil {
			return nil, request, nil, errProductionToolCallInvalid
		}
		copy := *call
		copy.RequestSnapshot = pinnedSnapshot
		copy.RequestDigest = productionToolDigest(pinnedSnapshot)
		plannedCall = &copy
	}
	plan, err := newProductionToolPlan(plannedCall, providerDigest)
	return plan, request, item, err
}

func (a *ProductionDataSourceAdapter) persistedResult(
	ctx context.Context,
	call *types.ProductionToolCall,
	plan *ProductionToolPlan,
	item *types.ProductionSourceItem,
) (*ProductionToolResult, bool, error) {
	evidence, evidenceItem, evidenceSet, err := a.scope.sources.GetEvidence(ctx, call.TenantID, productionToolEvidenceID(call))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if evidenceItem == nil || evidenceSet == nil || evidenceItem.ID != item.ID || evidenceItem.SourceSetID != call.SourceSetID ||
		evidenceSet.ID != call.SourceSetID || evidenceSet.TenantID != call.TenantID || evidenceSet.ProjectID != call.ProjectID {
		return nil, false, types.ErrProductionEvidenceConflict
	}
	result, err := productionToolResultFromEvidence(call, plan, evidence, plan.ProviderDigest)
	return result, err == nil, err
}

func (a *ProductionDataSourceAdapter) prepare(ctx context.Context, call *types.ProductionToolCall) (*productionDataSourcePrepared, error) {
	if a == nil || a.dataSources == nil || a.registry == nil || a.evidence == nil {
		return nil, errProductionProviderConfiguration
	}
	plan, request, item, err := a.plan(ctx, call, types.ProductionToolCallExecuting, false)
	if err != nil {
		return nil, err
	}
	dataSource, err := a.resolveDataSource(ctx, call, item)
	if err != nil {
		return nil, err
	}
	providerDigest, err := productionDataSourceProviderDigest(dataSource)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	if providerDigest != request.ProviderDigest {
		return nil, errProductionProviderDigestMismatch
	}
	if item.SourceSystem != dataSource.Type {
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
	_, redactedCount, err := redactProductionSecrets(config.Settings)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	return &productionDataSourcePrepared{
		plan: plan, request: request, connector: connector, config: config,
		credentialValues: credentialValues, providerRedactedCount: redactedCount,
		providerDigest: request.ProviderDigest, item: item,
	}, nil
}

func (a *ProductionDataSourceAdapter) resolveDataSource(
	ctx context.Context,
	call *types.ProductionToolCall,
	item *types.ProductionSourceItem,
) (*types.DataSource, error) {
	if a.dataSources == nil {
		return nil, errProductionProviderConfiguration
	}
	dataSource, err := a.dataSources.GetDataSource(ctx, call.ProviderID)
	if err != nil {
		return nil, errProductionProviderConfiguration
	}
	if dataSource == nil || dataSource.ID != call.ProviderID || dataSource.TenantID != call.TenantID || strings.TrimSpace(dataSource.Type) == "" {
		return nil, errProductionToolScope
	}
	return dataSource, nil
}

func productionDataSourceProviderDigest(dataSource *types.DataSource) (string, error) {
	if dataSource == nil || !canonicalProductionUUID(dataSource.ID) || strings.TrimSpace(dataSource.Type) == "" {
		return "", errProductionProviderConfiguration
	}
	descriptor, err := canonicalProductionValue(map[string]any{
		"config_digest":  productionToolDigest(dataSource.Config),
		"connector_type": dataSource.Type,
		"datasource_id":  dataSource.ID,
	})
	if err != nil {
		return "", err
	}
	return productionToolDigest(descriptor), nil
}

func validateProductionDataSourceRequestShape(toolName string, request productionDataSourceRequest) error {
	switch toolName {
	case productionDataSourceFetchToolName:
		if request.ParentID != "" || len(request.ResourceIDs) == 0 {
			return errProductionToolCallInvalid
		}
		seen := make(map[string]struct{}, len(request.ResourceIDs))
		for _, id := range request.ResourceIDs {
			if strings.TrimSpace(id) == "" {
				return errProductionToolCallInvalid
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
		if len(configured) == 0 {
			if request.ParentID != "" {
				return errProductionToolScope
			}
			return nil
		}
		if request.ParentID == "" {
			return errProductionToolScope
		}
		allowed := false
		for _, root := range configured {
			if request.ParentID == root {
				allowed = true
				break
			}
		}
		if !allowed {
			return errProductionToolScope
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

type productionCredentialValue struct {
	canonical string
	text      string
}

func productionCredentialValues(credentials map[string]any) []productionCredentialValue {
	values := make([]productionCredentialValue, 0)
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for _, nested := range typed {
				collect(nested)
			}
		case []any:
			for _, nested := range typed {
				collect(nested)
			}
		case string, bool, float64, json.Number:
			canonical, err := canonicalProductionValue(typed)
			if err != nil || string(canonical) == `""` {
				return
			}
			entry := productionCredentialValue{canonical: string(canonical)}
			if text, ok := typed.(string); ok {
				entry.text = text
			}
			values = append(values, entry)
		}
	}
	collect(credentials)
	sort.Slice(values, func(i, j int) bool { return values[i].canonical < values[j].canonical })
	return values
}

func containsProductionCredentialValue(value any, credentials []productionCredentialValue) bool {
	canonical, err := canonicalProductionValue(value)
	if err == nil {
		for _, credential := range credentials {
			if string(canonical) == credential.canonical {
				return true
			}
		}
	}
	switch typed := value.(type) {
	case string:
		for _, credential := range credentials {
			if credential.text != "" && strings.Contains(typed, credential.text) {
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
