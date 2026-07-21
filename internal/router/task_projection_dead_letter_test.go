package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type projectionDeadLetterKnowledgeRepoStub struct {
	interfaces.KnowledgeRepository
	values map[string]interface{}
}

func (s *projectionDeadLetterKnowledgeRepoStub) UpdateKnowledgeColumns(
	_ context.Context,
	_ string,
	values map[string]interface{},
) error {
	s.values = values
	return nil
}

type projectionDeadLetterKnowledgeServiceStub struct {
	interfaces.KnowledgeService
	repo interfaces.KnowledgeRepository
}

func (s projectionDeadLetterKnowledgeServiceStub) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}

type projectionDeadLetterFailureRecorderStub struct {
	projection  bool
	err         error
	knowledgeID string
	taskType    string
}

func (s *projectionDeadLetterFailureRecorderStub) RecordProjectionKnowledgeFailure(
	_ context.Context,
	knowledgeID string,
	taskType string,
) (bool, error) {
	s.knowledgeID = knowledgeID
	s.taskType = taskType
	return s.projection, s.err
}

func TestProjectionDeadLetterTransitionsTargetAndSanitizesKnowledgeFailure(t *testing.T) {
	for _, taskType := range []string{
		types.TypeDocumentProcess,
		types.TypeManualProcess,
		types.TypeKnowledgePostProcess,
	} {
		t.Run(taskType, func(t *testing.T) {
			repo := &projectionDeadLetterKnowledgeRepoStub{}
			recorder := &projectionDeadLetterFailureRecorderStub{projection: true}
			callback := newDeadLetterKnowledgeFailer(
				projectionDeadLetterKnowledgeServiceStub{repo: repo}, nil, recorder,
			)
			payload, err := json.Marshal(map[string]any{"knowledge_id": "knowledge-1", "tenant_id": 7})
			require.NoError(t, err)
			rawFailure := errors.New("provider token=super-secret host=10.0.0.7")

			callback(context.Background(), asynq.NewTask(taskType, payload), rawFailure)

			require.Equal(t, "knowledge-1", recorder.knowledgeID)
			require.Equal(t, taskType, recorder.taskType)
			require.Equal(t, types.ParseStatusFailed, repo.values["parse_status"])
			require.Equal(t, types.ProductionProjectionFailureReasonBuildFailed, repo.values["error_message"])
			require.NotContains(t, repo.values["error_message"], "super-secret")
		})
	}
}
