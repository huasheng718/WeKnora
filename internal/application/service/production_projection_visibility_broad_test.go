package service

import (
	"context"
	"io"
	"strings"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type broadVisibilityKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows map[string]*types.Knowledge
}

func (r *broadVisibilityKnowledgeRepo) GetKnowledgeByID(_ context.Context, tenantID uint64, id string) (*types.Knowledge, error) {
	row := r.rows[id]
	if row == nil || row.TenantID != tenantID {
		return nil, apprepository.ErrKnowledgeNotFound
	}
	return row, nil
}

func (r *broadVisibilityKnowledgeRepo) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	row := r.rows[id]
	if row == nil {
		return nil, apprepository.ErrKnowledgeNotFound
	}
	return row, nil
}

func (r *broadVisibilityKnowledgeRepo) GetKnowledgeBatch(_ context.Context, tenantID uint64, ids []string) ([]*types.Knowledge, error) {
	rows := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		if row := r.rows[id]; row != nil && row.TenantID == tenantID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (r *broadVisibilityKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(_ context.Context, tenantID uint64, kbID string) ([]*types.Knowledge, error) {
	rows := make([]*types.Knowledge, 0, len(r.rows))
	for _, row := range r.rows {
		if row.TenantID == tenantID && row.KnowledgeBaseID == kbID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (r *broadVisibilityKnowledgeRepo) GetKnowledgeTags(context.Context, []string) (map[string][]*types.KnowledgeTag, error) {
	return nil, nil
}

type broadVisibilityFileService struct {
	interfaces.FileService
}

func (*broadVisibilityFileService) GetFile(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("hidden file body")), nil
}

func broadVisibilityKnowledge(t *testing.T, id, kbID, title string) *types.Knowledge {
	t.Helper()
	row := &types.Knowledge{
		ID: id, TenantID: 7, KnowledgeBaseID: kbID, Type: types.KnowledgeTypeManual,
		Title: title, Source: "manual", EnableStatus: "enabled",
	}
	require.NoError(t, row.SetManualMetadata(types.NewManualKnowledgeMetadata("# "+title, types.ManualKnowledgeStatusPublish, 1)))
	return row
}

func newBroadVisibilityService(t *testing.T) (*knowledgeService, *broadVisibilityKnowledgeRepo) {
	t.Helper()
	repo := &broadVisibilityKnowledgeRepo{rows: map[string]*types.Knowledge{
		"ordinary": broadVisibilityKnowledge(t, "ordinary", "kb-1", "Ordinary"),
		"active":   broadVisibilityKnowledge(t, "active", "kb-1", "Active"),
		"inactive": broadVisibilityKnowledge(t, "inactive", "kb-1", "Hidden history"),
	}}
	return &knowledgeService{
		repo:    repo,
		fileSvc: &broadVisibilityFileService{},
		productionReleaseRepo: &projectionResolverReleaseRepo{scopes: map[string]types.ProductionKnowledgeScope{
			"kb-1": {
				ActiveKnowledgeIDs:        []string{"active"},
				InactiveKnowledgeIDs:      []string{"inactive"},
				AllProductionKnowledgeIDs: []string{"active", "inactive"},
			},
		}},
	}, repo
}

func broadVisibilityContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.UserIDContextKey, "user-1")
}

func TestProductionProjectionUserReadsHideInactiveAndProjectActiveWithoutMutatingRepositoryRows(t *testing.T) {
	svc, repo := newBroadVisibilityService(t)
	ctx := broadVisibilityContext()

	for _, read := range []struct {
		name string
		fn   func() (*types.Knowledge, error)
	}{
		{name: "tenant scoped", fn: func() (*types.Knowledge, error) { return svc.GetKnowledgeByID(ctx, "inactive") }},
		{name: "cross tenant lookup", fn: func() (*types.Knowledge, error) { return svc.GetKnowledgeByIDOnly(ctx, "inactive") }},
	} {
		t.Run(read.name, func(t *testing.T) {
			_, err := read.fn()
			require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
		})
	}

	active, err := svc.GetKnowledgeByID(ctx, "active")
	require.NoError(t, err)
	require.Equal(t, "production", active.Source)
	require.True(t, active.ReadOnly)
	require.Equal(t, "manual", repo.rows["active"].Source)
	require.False(t, repo.rows["active"].ReadOnly)

	ordinary, err := svc.GetKnowledgeByID(ctx, "ordinary")
	require.NoError(t, err)
	require.Equal(t, "manual", ordinary.Source)
	require.False(t, ordinary.ReadOnly)
}

func TestProductionProjectionBatchAndNonPagedListOmitInactive(t *testing.T) {
	svc, _ := newBroadVisibilityService(t)
	ctx := broadVisibilityContext()

	batch, err := svc.GetKnowledgeBatch(ctx, 7, []string{"inactive", "active", "ordinary"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"active", "ordinary"}, projectionKnowledgeIDs(batch))
	require.True(t, knowledgeByID(batch, "active").ReadOnly)

	rows, err := svc.ListKnowledgeByKnowledgeBaseID(ctx, "kb-1")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"active", "ordinary"}, projectionKnowledgeIDs(rows))
	require.True(t, knowledgeByID(rows, "active").ReadOnly)
}

func TestProductionProjectionFileReadRejectsInactiveBeforeOpeningContent(t *testing.T) {
	svc, _ := newBroadVisibilityService(t)
	_, _, err := svc.GetKnowledgeFile(broadVisibilityContext(), "inactive")
	require.ErrorIs(t, err, apprepository.ErrKnowledgeNotFound)
}

func TestSessionFallbackListingCannotExposeInactiveProjectionMetadata(t *testing.T) {
	knowledge, _ := newBroadVisibilityService(t)
	session := &sessionService{knowledgeService: knowledge}
	listing := session.buildKBDocumentListing(broadVisibilityContext(), &types.ChatManage{
		PipelineRequest: types.PipelineRequest{KnowledgeBaseIDs: []string{"kb-1"}},
	})
	require.Contains(t, listing, "Active")
	require.Contains(t, listing, "Ordinary")
	require.NotContains(t, listing, "Hidden history")
}

func knowledgeByID(rows []*types.Knowledge, id string) *types.Knowledge {
	for _, row := range rows {
		if row != nil && row.ID == id {
			return row
		}
	}
	return nil
}
