package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionRunRepositoryListsRecentDocumentRunsByTenant(t *testing.T) {
	repo, _ := newProductionRunRepoTestDB(t)
	first := newTestProductionRun(7)
	second := newTestProductionRun(7)
	require.NoError(t, repo.Create(context.Background(), first))
	require.NoError(t, repo.Create(context.Background(), second))

	runs, err := repo.ListDocumentRuns(context.Background(), 7, repoDocumentID, 50)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, second.ID, runs[0].ID)

	runs, err = repo.ListDocumentRuns(context.Background(), 8, repoDocumentID, 50)
	require.NoError(t, err)
	require.Empty(t, runs)
	runs, err = repo.ListDocumentRuns(context.Background(), 7, "ffffffff-ffff-4fff-8fff-ffffffffffff", 50)
	require.NoError(t, err)
	require.Empty(t, runs)
}
