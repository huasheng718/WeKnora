package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newProductionIdempotencyRepoTestDB(
	t *testing.T,
) (interfaces.ProductionIdempotencyRepository, *gorm.DB) {
	t.Helper()
	_, db := newProductionRepoTestDB(t)
	return NewProductionIdempotencyRepository(db), db
}

func idempotencyRecord(
	tenantID uint64, actorUserID, route, key, digest string,
) *types.ProductionIdempotencyKey {
	return &types.ProductionIdempotencyKey{
		ID:             "reservation-" + digest,
		TenantID:       tenantID,
		ActorUserID:    actorUserID,
		Route:          route,
		IdempotencyKey: key,
		RequestDigest:  digest,
	}
}

func TestProductionIdempotencyReserveReturnsExistingRecord(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	ctx := context.Background()
	first := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")

	reserved, created, err := repo.Reserve(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, first.ID, reserved.ID)

	duplicate := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-b")
	existing, created, err := repo.Reserve(ctx, duplicate)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, existing.ID)
	require.Equal(t, first.RequestDigest, existing.RequestDigest)

	var count int64
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).Count(&count).Error)
	require.Equal(t, int64(1), count, "the database unique key must arbitrate duplicate reservations")
}

func TestProductionIdempotencyReserveRejectsNilRecord(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)

	_, _, err := repo.Reserve(context.Background(), nil)

	require.ErrorContains(t, err, "idempotency record")
}

func TestProductionIdempotencyReserveConcurrentCallersShareWinningRecord(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	const callers = 8
	start := make(chan struct{})
	results := make(chan string, callers)
	errors := make(chan error, callers)
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			record := idempotencyRecord(
				7, "author-1", "/production/projects", "request-1", fmt.Sprintf("digest-%d", i),
			)
			record.ID = fmt.Sprintf("reservation-%d", i)
			existing, _, err := repo.Reserve(context.Background(), record)
			if err != nil {
				errors <- err
				return
			}
			results <- existing.ID
		}(i)
	}

	close(start)
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
	var winner string
	resultCount := 0
	for id := range results {
		resultCount++
		if winner == "" {
			winner = id
		}
		require.Equal(t, winner, id)
	}
	require.Equal(t, callers, resultCount)
	require.NotEmpty(t, winner)

	var count int64
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionIdempotencyReserveScopesUniquenessByTenantActorAndRoute(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)
	ctx := context.Background()
	records := []*types.ProductionIdempotencyKey{
		idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a"),
		idempotencyRecord(8, "author-1", "/production/projects", "request-1", "digest-b"),
		idempotencyRecord(7, "author-2", "/production/projects", "request-1", "digest-c"),
		idempotencyRecord(7, "author-1", "/production/document-types", "request-1", "digest-d"),
	}

	for _, record := range records {
		_, created, err := repo.Reserve(ctx, record)
		require.NoError(t, err)
		require.True(t, created)
	}
}

func TestProductionIdempotencyCompletePersistsResponseForRetry(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)
	ctx := context.Background()
	record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.True(t, created)

	responseBody := types.JSON(`{"id":"project-1"}`)
	require.NoError(t, repo.Complete(ctx, record.ID, 201, responseBody))

	existing, created, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, existing.StatusCode)
	require.Equal(t, 201, *existing.StatusCode)
	require.JSONEq(t, string(responseBody), string(existing.ResponseBody))
	require.NotNil(t, existing.CompletedAt)
}

func TestProductionIdempotencyCompletePreservesFirstResponse(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)
	ctx := context.Background()
	record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.True(t, created)

	firstBody := types.JSON(`{"id":"project-1"}`)
	require.NoError(t, repo.Complete(ctx, record.ID, 201, firstBody))
	first, _, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.NotNil(t, first.CompletedAt)
	firstCompletedAt := *first.CompletedAt

	require.NoError(t, repo.Complete(ctx, record.ID, 201, firstBody))
	require.NoError(t, repo.Complete(ctx, record.ID, 500, types.JSON(`{"error":"conflict"}`)))

	persisted, _, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.NotNil(t, persisted.StatusCode)
	require.Equal(t, 201, *persisted.StatusCode)
	require.JSONEq(t, string(firstBody), string(persisted.ResponseBody))
	require.NotNil(t, persisted.CompletedAt)
	require.Equal(t, firstCompletedAt, *persisted.CompletedAt)
}

func TestProductionIdempotencyCompleteRejectsMissingReservation(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)

	err := repo.Complete(context.Background(), "missing", 200, types.JSON(`{}`))

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
