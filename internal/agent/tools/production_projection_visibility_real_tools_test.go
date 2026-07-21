package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionToolKnowledgeRepo struct {
	interfaces.KnowledgeRepository
}

func (*projectionToolKnowledgeRepo) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if id != "inactive" {
		return nil, apprepository.ErrKnowledgeNotFound
	}
	return &types.Knowledge{ID: id, TenantID: 7, KnowledgeBaseID: "kb-1", Title: "SECRET TOOL METADATA"}, nil
}

type projectionToolReleaseRepo struct {
	interfaces.ProductionReleaseRepository
}

func (*projectionToolReleaseRepo) ResolveScopesForKnowledgeIDs(context.Context, uint64, []string) (map[string]types.ProductionKnowledgeScope, error) {
	return map[string]types.ProductionKnowledgeScope{
		"kb-1": {
			InactiveKnowledgeIDs:      []string{"inactive"},
			AllProductionKnowledgeIDs: []string{"inactive"},
		},
	}, nil
}

type projectionToolChunkRepo struct {
	interfaces.ChunkRepository
	calls int
}

func (r *projectionToolChunkRepo) ListPagedChunksByKnowledgeID(
	context.Context, uint64, string, *types.Pagination, []types.ChunkType,
	string, string, string, string, string,
) ([]*types.Chunk, int64, error) {
	r.calls++
	return []*types.Chunk{{Content: "SECRET TOOL CONTENT"}}, 1, nil
}

func newProjectionToolKnowledgeService(t *testing.T) interfaces.KnowledgeService {
	t.Helper()
	service, err := appservice.NewKnowledgeService(
		nil, &projectionToolKnowledgeRepo{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		&projectionToolReleaseRepo{},
	)
	require.NoError(t, err)
	return service
}

func TestDataSchemaRealToolCannotReadInactiveProjection(t *testing.T) {
	chunks := &projectionToolChunkRepo{}
	tool := agenttools.NewDataSchemaTool(newProjectionToolKnowledgeService(t), chunks)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_id":"inactive"}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.NotContains(t, result.Error, "SECRET TOOL METADATA")
	require.Zero(t, chunks.calls)
}

func TestDataAnalysisRealToolCannotMaterializeInactiveProjection(t *testing.T) {
	tool := agenttools.NewDataAnalysisTool(nil, newProjectionToolKnowledgeService(t), nil, nil, nil, "session-1")
	_, err := tool.LoadFromKnowledgeID(context.Background(), "inactive")
	require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
	require.NotContains(t, err.Error(), "SECRET TOOL METADATA")
}
