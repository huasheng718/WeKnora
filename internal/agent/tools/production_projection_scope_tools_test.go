package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type projectionScopeKnowledgeService struct {
	interfaces.KnowledgeService
	knowledge *types.Knowledge
}

func (s *projectionScopeKnowledgeService) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return s.knowledge, nil
}

func inactiveProjectionTagTarget() types.SearchTargets {
	return types.SearchTargets{&types.SearchTarget{
		Type:                types.SearchTargetTypeKnowledgeBase,
		KnowledgeBaseID:     "kb-1",
		TagIDs:              []string{"tag-production"},
		ExcludeKnowledgeIDs: []string{"knowledge-old"},
	}}
}

func TestListKnowledgeChunksRejectsInactiveTagScopedProjection(t *testing.T) {
	tool := NewListKnowledgeChunksTool(&projectionScopeKnowledgeService{knowledge: &types.Knowledge{
		ID: "knowledge-old", KnowledgeBaseID: "kb-1", TenantID: 7,
	}}, nil, inactiveProjectionTagTarget())
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_id":"knowledge-old"}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not within the current @mention scope")
}

func TestGetDocumentInfoRejectsInactiveTagScopedProjection(t *testing.T) {
	tool := NewGetDocumentInfoTool(&projectionScopeKnowledgeService{knowledge: &types.Knowledge{
		ID: "knowledge-old", KnowledgeBaseID: "kb-1", TenantID: 7,
	}}, nil, inactiveProjectionTagTarget())
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_ids":["knowledge-old"]}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not within the current @mention scope")
}
