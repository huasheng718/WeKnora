package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type projectionMutationKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	byID        map[string]*types.Knowledge
	batch       []*types.Knowledge
	updateCalls int
}

func (r *projectionMutationKnowledgeRepo) GetKnowledgeBatch(context.Context, uint64, []string) ([]*types.Knowledge, error) {
	return r.batch, nil
}

func (r *projectionMutationKnowledgeRepo) GetKnowledgeByID(_ context.Context, _ uint64, id string) (*types.Knowledge, error) {
	return r.byID[id], nil
}

func (r *projectionMutationKnowledgeRepo) UpdateKnowledge(context.Context, *types.Knowledge) error {
	r.updateCalls++
	return nil
}

func (r *projectionMutationKnowledgeRepo) UpdateKnowledgeColumn(context.Context, string, string, interface{}) error {
	r.updateCalls++
	return nil
}

type projectionMutationTenantRepo struct {
	interfaces.TenantRepository
}

func (r *projectionMutationTenantRepo) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: 1}, nil
}

func TestProcessKnowledgeListReparseRejectsMixedProjectionBeforeMutation(t *testing.T) {
	projection := productionProjectionKnowledgeForPostProcess(t)
	ordinary := &types.Knowledge{
		ID: "ordinary-1", TenantID: projection.TenantID, KnowledgeBaseID: projection.KnowledgeBaseID,
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusCompleted,
	}
	require.NoError(t, ordinary.SetManualMetadata(types.NewManualKnowledgeMetadata(
		"# ordinary", types.ManualKnowledgeStatusPublish, 1,
	)))
	repo := &projectionMutationKnowledgeRepo{
		byID:  map[string]*types.Knowledge{projection.ID: projection, ordinary.ID: ordinary},
		batch: []*types.Knowledge{projection, ordinary},
	}
	tasks := &createKnowledgeTaskEnqueuerStub{}
	service := &knowledgeService{
		repo: repo, tenantRepo: &projectionMutationTenantRepo{}, task: tasks,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{
			ID: projection.KnowledgeBaseID, TenantID: projection.TenantID,
		}},
	}
	payload, err := json.Marshal(types.KnowledgeListReparsePayload{
		TenantID: projection.TenantID, KnowledgeIDs: []string{projection.ID, ordinary.ID},
	})
	require.NoError(t, err)

	err = service.ProcessKnowledgeListReparse(context.Background(), asynq.NewTask(types.TypeKnowledgeListReparse, payload))
	require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	require.Zero(t, repo.updateCalls)
	require.Zero(t, tasks.calls)
}
