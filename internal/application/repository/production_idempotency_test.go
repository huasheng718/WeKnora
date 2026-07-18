package repository

import (
	"context"
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

func TestProductionIdempotencyCompleteRejectsMissingReservation(t *testing.T) {
	repo, _ := newProductionIdempotencyRepoTestDB(t)

	err := repo.Complete(context.Background(), "missing", 200, types.JSON(`{}`))

	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
