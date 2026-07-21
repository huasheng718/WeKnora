package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubKnowledgeBaseService struct {
	kb      *types.KnowledgeBase
	results []*types.SearchResult
}

type scopedGraphQueryCall struct {
	tenantID uint64
	kbID     string
	nodes    []string
}

type stubScopedGraphQueryService struct {
	mu     sync.Mutex
	calls  []scopedGraphQueryCall
	err    error
	errors map[string]error
	graphs map[string]*types.GraphData
}

func (s *stubScopedGraphQueryService) SearchKnowledgeGraph(ctx context.Context, tenantID uint64, knowledgeBaseID string, nodes []string) (*types.GraphData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, scopedGraphQueryCall{tenantID: tenantID, kbID: knowledgeBaseID, nodes: append([]string(nil), nodes...)})
	if err := s.errors[knowledgeBaseID]; err != nil {
		return nil, err
	}
	if s.err != nil {
		return nil, s.err
	}
	if graph := s.graphs[knowledgeBaseID]; graph != nil {
		return graph, nil
	}
	return &types.GraphData{Node: []*types.GraphNode{{Name: "active"}}}, nil
}

func (s *stubKnowledgeBaseService) CreateKnowledgeBase(context.Context, *types.KnowledgeBase) (*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func (s *stubKnowledgeBaseService) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func (s *stubKnowledgeBaseService) GetKnowledgeBasesByIDsOnly(context.Context, []string) ([]*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) FillKnowledgeBaseCounts(context.Context, *types.KnowledgeBase) error {
	return nil
}

func (s *stubKnowledgeBaseService) ListKnowledgeBases(context.Context) ([]*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) ListKnowledgeBasesByTenantID(context.Context, uint64) ([]*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) UpdateKnowledgeBase(
	context.Context,
	string,
	string,
	string,
	*types.KnowledgeBaseConfig,
) (*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) DeleteKnowledgeBase(context.Context, string) error {
	return nil
}

func (s *stubKnowledgeBaseService) TogglePinKnowledgeBase(context.Context, string) (*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) HybridSearch(context.Context, string, types.SearchParams) ([]*types.SearchResult, error) {
	return s.results, nil
}

func (s *stubKnowledgeBaseService) GetQueryEmbedding(context.Context, string, string) ([]float32, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return nil
}

func (s *stubKnowledgeBaseService) CopyKnowledgeBase(
	context.Context,
	string,
	string,
) (*types.KnowledgeBase, *types.KnowledgeBase, error) {
	return nil, nil, nil
}

func (s *stubKnowledgeBaseService) DuplicateKnowledgeBase(
	context.Context,
	string,
) (*types.KnowledgeBase, error) {
	return nil, nil
}

func (s *stubKnowledgeBaseService) GetRepository() interfaces.KnowledgeBaseRepository {
	return nil
}

func (s *stubKnowledgeBaseService) ProcessKBDelete(context.Context, *asynq.Task) error {
	return nil
}

func TestQueryKnowledgeGraph_ReportsConfiguredEntityAndRelationTypes(t *testing.T) {
	tool := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{
		kb: &types.KnowledgeBase{
			ID: "kb-1",
			ExtractConfig: &types.ExtractConfig{
				Enabled: true,
				Nodes: []*types.GraphNode{
					{Name: "合同"},
					{Name: "法务部门"},
					{Name: "审批流程"},
					{Name: "合同"},
					nil,
					{Name: ""},
				},
				Relations: []*types.GraphRelation{
					{Type: "属于"},
					{Type: "管理"},
					{Type: "审批"},
					{Type: "管理"},
					{Type: ""},
					nil,
				},
			},
		},
		results: []*types.SearchResult{
			{
				ID:             "chunk-approval-1",
				Content:        "合同审批流程由法务部门与采购部门共同维护，法务部门负责合规审查。",
				KnowledgeID:    "doc-approval",
				KnowledgeTitle: "合同审批管理制度",
				Score:          0.97,
				MatchType:      types.MatchTypeEmbedding,
			},
			{
				ID:             "chunk-approval-2",
				Content:        "采购申请提交后进入合同审批流程，审批完成后归档到合同台账。",
				KnowledgeID:    "doc-procurement",
				KnowledgeTitle: "采购与合同协作规范",
				Score:          0.89,
				MatchType:      types.MatchTypeKeywords,
			},
			{
				ID:             "chunk-approval-3",
				Content:        "法务部门管理标准合同模板，并维护合同风险审查清单。",
				KnowledgeID:    "doc-legal",
				KnowledgeTitle: "法务部职责说明",
				Score:          0.84,
				MatchType:      types.MatchTypeEmbedding,
			},
		},
	}, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 1}}, &stubScopedGraphQueryService{})

	args, err := json.Marshal(QueryKnowledgeGraphInput{
		KnowledgeBaseIDs: []string{"kb-1"},
		Query:            "合同审批与法务协作",
	})
	require.NoError(t, err)

	result, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	t.Logf("tool output:\n%s", result.Output)

	assert.Contains(t, result.Output, "Entity Types (3)")
	assert.Contains(t, result.Output, "Relationship Types (3)")
	assert.NotContains(t, result.Output, "No entity types configured")
	assert.NotContains(t, result.Output, "No relationship types configured")
	assert.Contains(t, result.Output, "合同")
	assert.Contains(t, result.Output, "法务部门")
	assert.Contains(t, result.Output, "审批流程")
	assert.Contains(t, result.Output, "管理")
	assert.Contains(t, result.Output, "审批")
	assert.Contains(t, result.Output, "✓ Found 3 relevant results (deduplicated)")
	assert.Contains(t, result.Output, "Result #1:")
	assert.Contains(t, result.Output, "Result #2:")
	assert.Contains(t, result.Output, "Result #3:")
	assert.Contains(t, result.Output, "合同审批管理制度")

	graphConfig, ok := result.Data["graph_config"].(map[string]interface{})
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"合同", "审批流程", "法务部门"}, graphConfig["nodes"])
	assert.ElementsMatch(t, []string{"属于", "审批", "管理"}, graphConfig["relations"])
}

func TestQueryKnowledgeGraphUsesScopedGraphQueryAndReportsGraphErrors(t *testing.T) {
	graphQuery := &stubScopedGraphQueryService{}
	tool := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{kb: &types.KnowledgeBase{
		ID: "kb-1", ExtractConfig: &types.ExtractConfig{Enabled: true, Nodes: []*types.GraphNode{{Name: "term"}}},
	}}, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 200}}, graphQuery)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(200))
	args, err := json.Marshal(QueryKnowledgeGraphInput{KnowledgeBaseIDs: []string{"kb-1"}, Query: "term"})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, []scopedGraphQueryCall{{tenantID: 200, kbID: "kb-1", nodes: []string{"term"}}}, graphQuery.calls)
	graph, ok := result.Data["scoped_graph"].(*types.GraphData)
	require.True(t, ok)
	require.Equal(t, "active", graph.Node[0].Name)

	graphQuery.err = errors.New("neo4j unavailable")
	result, err = tool.Execute(ctx, args)
	require.ErrorIs(t, err, errQueryKnowledgeGraphAllFailed)
	require.False(t, result.Success)
	require.Contains(t, strings.Join(result.Data["errors"].([]string), "\n"), "graph query failed")
}

func TestQueryKnowledgeGraphUsesAuthorizedOwnerScopeAndRejectsUnauthorizedKnowledgeBase(t *testing.T) {
	graphQuery := &stubScopedGraphQueryService{}
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-shared", TenantID: 200}}
	tool := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{kb: &types.KnowledgeBase{
		ID: "kb-shared", ExtractConfig: &types.ExtractConfig{Enabled: true, Nodes: []*types.GraphNode{{Name: "term"}}},
	}}, targets, graphQuery)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	args, err := json.Marshal(QueryKnowledgeGraphInput{KnowledgeBaseIDs: []string{"kb-shared"}, Query: "term"})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, uint64(200), graphQuery.calls[0].tenantID)

	args, err = json.Marshal(QueryKnowledgeGraphInput{KnowledgeBaseIDs: []string{"kb-forbidden"}, Query: "term"})
	require.NoError(t, err)
	result, err = tool.Execute(ctx, args)
	require.Error(t, err)
	require.False(t, result.Success)
	require.Len(t, graphQuery.calls, 1)
}

func TestQueryKnowledgeGraphFailsWhenScopedDependencyOrAllScopesFail(t *testing.T) {
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}}
	args, err := json.Marshal(QueryKnowledgeGraphInput{KnowledgeBaseIDs: []string{"kb-1"}, Query: "term"})
	require.NoError(t, err)

	missing := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{kb: &types.KnowledgeBase{ID: "kb-1", ExtractConfig: &types.ExtractConfig{Nodes: []*types.GraphNode{{Name: "term"}}}}}, targets, nil)
	result, err := missing.Execute(context.Background(), args)
	require.Error(t, err)
	require.False(t, result.Success)

	failed := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{kb: &types.KnowledgeBase{ID: "kb-1", ExtractConfig: &types.ExtractConfig{Nodes: []*types.GraphNode{{Name: "term"}}}}}, targets, &stubScopedGraphQueryService{err: errors.New("neo4j unavailable")})
	result, err = failed.Execute(context.Background(), args)
	require.Error(t, err)
	require.False(t, result.Success)
}

func TestQueryKnowledgeGraphReturnsPartialSuccessAndPreservesProjectionVariantsAcrossKnowledgeBases(t *testing.T) {
	graphQuery := &stubScopedGraphQueryService{graphs: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{{Name: "shared", Chunks: []string{"chunk-one"}, ProjectionVariants: []*types.GraphNodeVariant{{KnowledgeIDs: []string{"knowledge-one"}, Chunks: []string{"chunk-one"}}}}}, Relation: []*types.GraphRelation{{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-one"}}}},
		"kb-2": {Node: []*types.GraphNode{{Name: "shared", Chunks: []string{"chunk-two"}, ProjectionVariants: []*types.GraphNodeVariant{{KnowledgeIDs: []string{"knowledge-two"}, Chunks: []string{"chunk-two"}}}}}, Relation: []*types.GraphRelation{{Node1: "shared", Node2: "peer", Type: "related", KnowledgeIDs: []string{"knowledge-two"}}}},
	}}
	targets := types.SearchTargets{
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7},
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-2", TenantID: 7},
	}
	tool := NewQueryKnowledgeGraphTool(&stubKnowledgeBaseService{kb: &types.KnowledgeBase{ExtractConfig: &types.ExtractConfig{Nodes: []*types.GraphNode{{Name: "term"}}}}}, targets, graphQuery)
	args, err := json.Marshal(QueryKnowledgeGraphInput{KnowledgeBaseIDs: []string{"kb-1", "kb-2"}, Query: "term"})
	require.NoError(t, err)

	result, err := tool.Execute(context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)), args)
	require.NoError(t, err)
	require.True(t, result.Success)
	graph := result.Data["scoped_graph"].(*types.GraphData)
	require.Len(t, graph.Node, 1)
	require.Len(t, graph.Node[0].ProjectionVariants, 2)
	require.ElementsMatch(t, []string{"knowledge-one", "knowledge-two"}, graph.Relation[0].KnowledgeIDs)

	graphQuery.errors = map[string]error{"kb-2": errors.New("neo4j unavailable")}
	result, err = tool.Execute(context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)), args)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, strings.Join(result.Data["errors"].([]string), "\n"), "KB kb-2: graph query failed")
}
