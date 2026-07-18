package repository

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

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

func productionTenantContext(tenantID uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
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
	ctx := productionTenantContext(7)
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
	ctx := productionTenantContext(7)
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

func TestProductionIdempotencyCompleteRejectsCrossTenantReservation(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(context.Background(), record)
	require.NoError(t, err)
	require.True(t, created)

	err = repo.Complete(productionTenantContext(8), record.ID, 403, types.JSON(`{"error":"forbidden"}`))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	var unchanged types.ProductionIdempotencyKey
	require.NoError(t, db.First(&unchanged, "id = ?", record.ID).Error)
	require.Nil(t, unchanged.StatusCode)
	require.Nil(t, unchanged.ResponseBody)
	require.Nil(t, unchanged.CompletedAt)

	require.NoError(t, repo.Complete(
		productionTenantContext(7), record.ID, 201, types.JSON(`{"id":"project-1"}`),
	))
	require.NoError(t, db.First(&unchanged, "id = ?", record.ID).Error)
	require.NotNil(t, unchanged.StatusCode)
	require.Equal(t, 201, *unchanged.StatusCode)
	require.NotNil(t, unchanged.CompletedAt)
}

func TestProductionIdempotencyCompleteRequiresNonzeroTenantContext(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(context.Background(), record)
	require.NoError(t, err)
	require.True(t, created)

	contexts := []context.Context{
		context.Background(),
		productionTenantContext(0),
	}
	for _, ctx := range contexts {
		err = repo.Complete(ctx, record.ID, 201, types.JSON(`{"id":"project-1"}`))
		require.ErrorContains(t, err, "tenant")
	}

	var unchanged types.ProductionIdempotencyKey
	require.NoError(t, db.First(&unchanged, "id = ?", record.ID).Error)
	require.Nil(t, unchanged.StatusCode)
	require.Nil(t, unchanged.ResponseBody)
	require.Nil(t, unchanged.CompletedAt)
}

func TestProductionIdempotencyCompleteRejectsMissingReservation(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)

	err := repo.Complete(productionTenantContext(7), "missing", 200, types.JSON(`{}`))

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestProductionIdempotencyReleaseAllowsImmediateRetry(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	ctx := productionTenantContext(7)
	first := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(ctx, first)
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, repo.Release(ctx, first.ID))

	second := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-b")
	reserved, created, err := repo.Reserve(ctx, second)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, second.ID, reserved.ID)
	require.Equal(t, "digest-b", reserved.RequestDigest)
	var count int64
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestProductionIdempotencyReleaseDoesNotDeleteCompletedResponse(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)
	ctx := productionTenantContext(7)
	record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, repo.Complete(ctx, record.ID, 201, types.JSON(`{"id":"project-1"}`)))

	require.NoError(t, repo.Release(ctx, record.ID))

	existing, created, err := repo.Reserve(ctx, record)
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, existing.StatusCode)
	require.Equal(t, 201, *existing.StatusCode)
}

func TestProductionIdempotencyStaleDifferentDigestIsNotReclaimed(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	ctx := productionTenantContext(7)
	first := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	_, created, err := repo.Reserve(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).
		Where("id = ?", first.ID).
		Update("created_at", time.Now().Add(-productionIdempotencyLease-time.Minute)).Error)

	retry := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-b")
	retry.ID = "reservation-retry"
	existing, created, err := repo.Reserve(ctx, retry)

	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, existing.ID)
	require.Equal(t, "digest-a", existing.RequestDigest)
	require.Less(t, existing.CreatedAt, time.Now().Add(-productionIdempotencyLease))
	var count int64
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func reclaimStaleSameDigest(
	t *testing.T,
	repo interfaces.ProductionIdempotencyRepository,
	db *gorm.DB,
) (*types.ProductionIdempotencyKey, *types.ProductionIdempotencyKey) {
	t.Helper()
	ctx := productionTenantContext(7)
	old := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	old.ID = "reservation-old"
	_, created, err := repo.Reserve(ctx, old)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).
		Where("id = ?", old.ID).
		Update("created_at", time.Now().Add(-productionIdempotencyLease-time.Minute)).Error)

	retry := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	retry.ID = "reservation-new"
	reclaimed, created, err := repo.Reserve(ctx, retry)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, retry.ID, reclaimed.ID, "reclaim must rotate the fencing token")
	require.Equal(t, old.RequestDigest, reclaimed.RequestDigest)
	require.WithinDuration(t, time.Now(), reclaimed.CreatedAt, time.Second)
	return old, reclaimed
}

func TestProductionIdempotencyOldLeaseHolderCannotCompleteAfterReclaim(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	old, _ := reclaimStaleSameDigest(t, repo, db)

	err := repo.Complete(
		productionTenantContext(7), old.ID, http.StatusCreated, types.JSON(`{"stale":true}`),
	)

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestProductionIdempotencyOldLeaseHolderCannotReleaseAfterReclaim(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	old, current := reclaimStaleSameDigest(t, repo, db)

	err := repo.Release(productionTenantContext(7), old.ID)

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var persisted types.ProductionIdempotencyKey
	require.NoError(t, db.First(&persisted, "id = ?", current.ID).Error)
}

func TestProductionIdempotencyNewLeaseHolderCanCompleteAndReplay(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	_, current := reclaimStaleSameDigest(t, repo, db)
	ctx := productionTenantContext(7)
	responseBody := types.JSON(`{"id":"project-1"}`)

	require.NoError(t, repo.Complete(ctx, current.ID, http.StatusCreated, responseBody))
	retry := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-a")
	retry.ID = "reservation-later"
	replayed, created, err := repo.Reserve(ctx, retry)

	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, current.ID, replayed.ID)
	require.NotNil(t, replayed.StatusCode)
	require.Equal(t, http.StatusCreated, *replayed.StatusCode)
	require.JSONEq(t, string(responseBody), string(replayed.ResponseBody))
}

func TestProductionIdempotencyStaleReclaimHasSingleWinner(t *testing.T) {
	repo, db := newProductionIdempotencyRepoTestDB(t)
	ctx := productionTenantContext(7)
	first := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-stale")
	_, created, err := repo.Reserve(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.Model(&types.ProductionIdempotencyKey{}).
		Where("id = ?", first.ID).
		Update("created_at", time.Now().Add(-productionIdempotencyLease-time.Minute)).Error)

	start := make(chan struct{})
	type result struct {
		id      string
		created bool
		err     error
	}
	results := make(chan result, 2)
	for _, id := range []string{"reservation-a", "reservation-b"} {
		go func(id string) {
			<-start
			record := idempotencyRecord(7, "author-1", "/production/projects", "request-1", "digest-stale")
			record.ID = id
			reserved, created, err := repo.Reserve(ctx, record)
			results <- result{id: reservedID(reserved), created: created, err: err}
		}(id)
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	require.NotEqual(t, firstResult.created, secondResult.created, "exactly one caller renews the stale lease")
	require.Equal(t, firstResult.id, secondResult.id, "both callers observe the winning fencing token")
	require.NotEqual(t, first.ID, firstResult.id, "winner must rotate the expired fencing token")
}

func reservedID(record *types.ProductionIdempotencyKey) string {
	if record == nil {
		return ""
	}
	return record.ID
}
