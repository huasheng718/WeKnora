package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	releaseIDOne      = "30000000-0000-4000-8000-000000000001"
	releaseIDTwo      = "30000000-0000-4000-8000-000000000002"
	releaseTarget1    = "30000000-0000-4000-8000-000000000003"
	releaseTarget2    = "30000000-0000-4000-8000-000000000004"
	releaseTarget3    = "30000000-0000-4000-8000-000000000005"
	releaseKBOne      = "30000000-0000-4000-8000-000000000006"
	releaseKBTwo      = "30000000-0000-4000-8000-000000000007"
	releaseKnowledge1 = "30000000-0000-4000-8000-000000000008"
	releaseKnowledge2 = "30000000-0000-4000-8000-000000000009"
	releaseKnowledge3 = "30000000-0000-4000-8000-00000000000a"
)

type productionReleaseTestClock struct{ current time.Time }

func (c *productionReleaseTestClock) Now() time.Time { return c.current }

func productionReleaseContext(tenantID uint64, actorID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, actorID)
}

func newProductionReleaseRepoFixture(t *testing.T, clock ProductionReleaseClock) (interfaces.ProductionReleaseRepository, *gorm.DB) {
	t.Helper()
	reviewRepo, db := newProductionReviewRepoFixture(t)
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	runsMigration, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite/000003_knowledge_production_runs.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(runsMigration)).Error)
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, UNIQUE(id, tenant_id))`).Error)
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../migrations/sqlite/000005_knowledge_production_publication.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id) VALUES (?, ?), (?, ?), (?, 8)`, releaseKBOne, reviewTenantID, releaseKBTwo, reviewTenantID, releaseKBTwo+"-other", 8).Error)

	approveProductionReleaseReview(t, db, reviewRepo, reviewID(700), reviewVersionOne, 700)
	approveProductionReleaseReview(t, db, reviewRepo, reviewID(710), reviewVersionTwo, 710)
	return NewProductionReleaseRepositoryWithClock(db, clock), db
}

func approveProductionReleaseReview(t *testing.T, db *gorm.DB, repo interfaces.ProductionReviewRepository, requestID, versionID string, stepStart int) {
	t.Helper()
	request := productionReviewRequest(requestID, versionID)
	steps := productionReviewSteps(request.ID, stepStart)
	require.NoError(t, repo.CreateReview(productionReviewContext(reviewTenantID, reviewAuthorID), request, steps))
	for index, actor := range []string{reviewBusinessActor, reviewEngineeringActor} {
		require.NoError(t, db.Model(&types.ProductionReviewStep{}).Where("id = ?", steps[index].ID).Updates(map[string]any{
			"decision": types.ProductionReviewApproved, "reviewer_user_id": actor,
			"comment": "approved", "decided_at": time.Now().UTC(),
		}).Error)
	}
	require.NoError(t, db.Model(&types.ProductionReviewRequest{}).Where("id = ?", requestID).Updates(map[string]any{
		"status": types.ProductionReviewApproved, "terminal_by": reviewEngineeringActor,
		"completed_at": time.Now().UTC(),
	}).Error)
	persisted, err := repo.GetReview(context.Background(), reviewTenantID, requestID)
	require.NoError(t, err)
	require.Equal(t, types.ProductionReviewStatus(types.ProductionReviewApproved), persisted.Status)
}

func productionRelease(id, versionID, reviewRequestID string) *types.ProductionRelease {
	return &types.ProductionRelease{
		ID: id, TenantID: reviewTenantID, ProjectID: reviewProjectID, DocumentID: reviewDocumentID,
		VersionID: versionID, ReviewRequestID: reviewRequestID, ReleaseDigest: strings.Repeat("a", 64),
		Status: types.ProductionReleaseBuilding, RetentionDays: 30,
	}
}

func productionReleaseTarget(id, kbID, knowledgeID string) *types.ProductionReleaseTarget {
	return &types.ProductionReleaseTarget{
		ID: id, TargetKnowledgeBaseID: kbID, KnowledgeID: knowledgeID,
		ConfigSnapshot: types.JSON(`{ "chunking": { "size": 512 }, "graph": true }`),
	}
}

func TestProductionReleaseRepositoryCreatesReleaseAndTargetsAtomicallyWithTrustedOwnership(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 123456789, time.UTC)
	ownedNow := now.Truncate(time.Second)
	repo, db := newProductionReleaseRepoFixture(t, &productionReleaseTestClock{current: now})
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
		productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2),
	}

	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets))
	require.Equal(t, reviewAuthorID, release.CreatedBy)
	require.Equal(t, ownedNow, release.CreatedAt)
	for _, target := range targets {
		require.Equal(t, release.ID, target.ReleaseID)
		require.Equal(t, release.TenantID, target.TenantID)
		require.Equal(t, release.ProjectID, target.ProjectID)
		require.Equal(t, release.DocumentID, target.DocumentID)
		require.Equal(t, release.VersionID, target.VersionID)
		require.Equal(t, release.ReleaseDigest, target.ReleaseDigest)
		canonicalConfig, _, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{"chunking":{"size":512},"graph":true}`))
		require.NoError(t, err)
		require.Equal(t, canonicalConfig, target.ConfigSnapshot)
		require.Len(t, target.ConfigDigest, 64)
		require.Equal(t, ownedNow, target.CreatedAt)
		persisted, err := repo.GetTarget(context.Background(), reviewTenantID, target.ID)
		require.NoError(t, err)
		require.Equal(t, target.ConfigSnapshot, persisted.ConfigSnapshot)
		require.Equal(t, target.ConfigDigest, persisted.ConfigDigest)
	}

	var releaseCount, targetCount int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&releaseCount).Error)
	require.NoError(t, db.Model(&types.ProductionReleaseTarget{}).Count(&targetCount).Error)
	require.EqualValues(t, 1, releaseCount)
	require.EqualValues(t, 2, targetCount)
}

func TestProductionReleaseRepositoryGetsReleaseByTenantAndHydratesTargets(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget2, releaseKBTwo, releaseKnowledge2),
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
	}
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets))

	got, err := repo.GetRelease(context.Background(), reviewTenantID, releaseIDOne)
	require.NoError(t, err)
	require.Equal(t, releaseIDOne, got.ID)
	require.Len(t, got.Targets, 2)
	require.Equal(t, releaseTarget1, got.Targets[0].ID)
	require.Equal(t, releaseTarget2, got.Targets[1].ID)
	_, err = repo.GetRelease(productionReleaseContext(8, reviewAuthorID), reviewTenantID, releaseIDOne)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseTargetFailureReasonIsBoundedSanitizedAndClearedOnRetry(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	require.NoError(t, repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID),
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)},
	))

	changed, err := repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed,
		types.JSONMap{"failure_code": types.ProductionProjectionFailureContentDigestMismatch,
			"failure_reason": types.ProductionProjectionFailureReasonContentDigestMismatch},
	)
	require.NoError(t, err)
	require.True(t, changed)
	failed, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, types.ProductionProjectionFailureContentDigestMismatch, failed.FailureCode)
	require.Equal(t, types.ProductionProjectionFailureReasonContentDigestMismatch, failed.FailureReason)

	changed, err = repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetFailed, types.ReleaseTargetBuilding, nil,
	)
	require.NoError(t, err)
	require.True(t, changed)
	retried, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Empty(t, retried.FailureCode)
	require.Empty(t, retried.FailureReason)

	_, err = repo.TransitionTarget(
		productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed,
		types.JSONMap{"failure_code": "RAW_PROVIDER_ERROR", "failure_reason": "token=secret"},
	)
	require.ErrorIs(t, err, types.ErrProductionReleasePatchInvalid)
}

func TestProductionReleaseCreateRejectsCallerSuppliedFailureMetadata(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	target.FailureCode = "RAW_PROVIDER_ERROR"
	target.FailureReason = "token=secret"

	err := repo.CreateRelease(
		productionReleaseContext(reviewTenantID, reviewAuthorID),
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		[]*types.ProductionReleaseTarget{target},
	)
	require.ErrorIs(t, err, types.ErrProductionReleaseInvalid)
}

func insertRawProductionReleaseTargetConfig(
	t *testing.T,
	db *gorm.DB,
	targetID, kbID, knowledgeID, snapshot, digest string,
) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, release_id, tenant_id, project_id, document_id, version_id,
		 target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetID, releaseIDOne, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne,
		kbID, knowledgeID, strings.Repeat("a", 64), snapshot, digest,
	).Error)
}

func TestProductionReleaseTargetReadsCanonicalizeAndVerifyPersistedConfig(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	raw := types.JSON(`{"z":1,"a":{"size":512}}`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	require.NotEqual(t, raw, canonical)
	insertRawProductionReleaseTargetConfig(t, db, releaseTarget2, releaseKBTwo, releaseKnowledge2, string(raw), digest)

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget2)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	require.Equal(t, digest, target.ConfigDigest)

	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBTwo)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, canonical, history[0].ConfigSnapshot)
	require.Equal(t, digest, history[0].ConfigDigest)
}

func TestProductionReleaseTargetReadsRejectPersistedConfigDigestMismatch(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	insertRawProductionReleaseTargetConfig(t, db, releaseTarget2, releaseKBTwo, releaseKnowledge2, `{}`, strings.Repeat("f", 64))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget2)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, target)

	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBTwo)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, history)
}

func TestProductionReleaseGetTargetRejectsMalformedPostgresJSONBScan(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
		WithArgs(reviewTenantID, releaseTarget1, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
			AddRow(releaseTarget1, reviewTenantID, []byte(`[]`), strings.Repeat("a", 64)))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	require.Nil(t, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleaseGetTargetCanonicalizesPostgresJSONBScan(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	raw := types.JSON(`{ "z": 1, "a": { "size": 512 } }`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
		WithArgs(reviewTenantID, releaseTarget1, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
			AddRow(releaseTarget1, reviewTenantID, []byte(raw), digest))

	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	require.Equal(t, digest, target.ConfigDigest)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProductionReleaseGetTargetRejectsNullOrEmptyPersistedConfig(t *testing.T) {
	_, emptyDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		snapshot any
	}{
		{name: "null", snapshot: nil},
		{name: "empty bytes", snapshot: []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, mockErr := sqlmock.New()
			require.NoError(t, mockErr)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, openErr := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, openErr)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
				WithArgs(reviewTenantID, releaseTarget1, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
					AddRow(releaseTarget1, reviewTenantID, tc.snapshot, emptyDigest))

			target, getErr := NewProductionReleaseRepository(db).GetTarget(context.Background(), reviewTenantID, releaseTarget1)
			require.ErrorIs(t, getErr, types.ErrProductionReleaseConfigInvalid)
			require.Nil(t, target)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleaseHistoryRejectsNullOrEmptyPersistedConfig(t *testing.T) {
	_, emptyDigest, err := types.CanonicalProductionReleaseTargetConfig(types.JSON(`{}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		snapshot any
	}{
		{name: "null", snapshot: nil},
		{name: "empty bytes", snapshot: []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, mockErr := sqlmock.New()
			require.NoError(t, mockErr)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, openErr := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, openErr)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).
				WithArgs(reviewTenantID, reviewDocumentID, releaseKBOne).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "document_id", "target_knowledge_base_id", "config_snapshot", "config_digest"}).
					AddRow(releaseTarget1, reviewTenantID, reviewDocumentID, releaseKBOne, tc.snapshot, emptyDigest))

			history, listErr := NewProductionReleaseRepository(db).ListProjectionHistory(
				context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne,
			)
			require.ErrorIs(t, listErr, types.ErrProductionReleaseConfigInvalid)
			require.Nil(t, history)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleaseTargetReadsTranslateInvalidJSONScanError(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*gorm.DB) error
		args []driver.Value
		row  *sqlmock.Rows
	}{
		{
			name: "get target",
			read: func(db *gorm.DB) error {
				target, err := NewProductionReleaseRepository(db).GetTarget(context.Background(), reviewTenantID, releaseTarget1)
				require.Nil(t, target)
				return err
			},
			args: []driver.Value{reviewTenantID, releaseTarget1, 1},
			row: sqlmock.NewRows([]string{"id", "tenant_id", "config_snapshot", "config_digest"}).
				AddRow(releaseTarget1, reviewTenantID, []byte(`{"broken":`), strings.Repeat("a", 64)),
		},
		{
			name: "history",
			read: func(db *gorm.DB) error {
				history, err := NewProductionReleaseRepository(db).ListProjectionHistory(
					context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne,
				)
				require.Nil(t, history)
				return err
			},
			args: []driver.Value{reviewTenantID, reviewDocumentID, releaseKBOne},
			row: sqlmock.NewRows([]string{"id", "tenant_id", "document_id", "target_knowledge_base_id", "config_snapshot", "config_digest"}).
				AddRow(releaseTarget1, reviewTenantID, reviewDocumentID, releaseKBOne, []byte(`{"broken":`), strings.Repeat("a", 64)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
				Logger: logger.Default.LogMode(logger.Silent),
			})
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT \* FROM "production_release_targets"`).WithArgs(tc.args...).WillReturnRows(tc.row)

			readErr := tc.read(db)
			require.ErrorIs(t, readErr, types.ErrProductionReleaseConfigInvalid)
			require.NotContains(t, readErr.Error(), "unexpected end of JSON input")
			require.NotContains(t, readErr.Error(), "Scan error")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProductionReleasePostgresLiveJSONBRoundTrip(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WEKNORA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set WEKNORA_TEST_POSTGRES_DSN to run the isolated PostgreSQL JSONB round-trip test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	schemaName := fmt.Sprintf("weknora_release_config_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schemaName+`"`).Error)
	t.Cleanup(func() { _ = db.Exec(`DROP SCHEMA IF EXISTS "` + schemaName + `" CASCADE`).Error })
	require.NoError(t, db.Exec(`SET search_path TO "`+schemaName+`"`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_release_targets (
		id TEXT PRIMARY KEY, release_id TEXT NOT NULL, tenant_id BIGINT NOT NULL,
		project_id TEXT NOT NULL, document_id TEXT NOT NULL, version_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, knowledge_id TEXT NOT NULL,
		release_digest TEXT NOT NULL, config_snapshot JSONB NOT NULL, config_digest TEXT NOT NULL,
		status TEXT NOT NULL, retention_days INTEGER NOT NULL,
		retention_until TIMESTAMPTZ, activated_at TIMESTAMPTZ, failed_at TIMESTAMPTZ,
		rolled_back_at TIMESTAMPTZ, cleanup_requested_at TIMESTAMPTZ, cleaned_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
	)`).Error)

	raw := types.JSON(`{ "z": 1, "a": { "size": 512 } }`)
	canonical, digest, err := types.CanonicalProductionReleaseTargetConfig(raw)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, release_id, tenant_id, project_id, document_id, version_id,
		 target_knowledge_base_id, knowledge_id, release_digest, config_snapshot, config_digest,
		 status, retention_days, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSONB), ?, ?, 30, now(), now())`,
		releaseTarget1, releaseIDOne, reviewTenantID, reviewProjectID, reviewDocumentID, reviewVersionOne,
		releaseKBOne, releaseKnowledge1, strings.Repeat("a", 64), string(raw), digest, types.ReleaseTargetBuilding,
	).Error)

	repo := NewProductionReleaseRepository(db)
	target, err := repo.GetTarget(context.Background(), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, canonical, target.ConfigSnapshot)
	history, err := repo.ListProjectionHistory(context.Background(), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, canonical, history[0].ConfigSnapshot)
}

func TestProductionReleaseRepositoryRejectsTargetConfigDigestMismatchAndRollsBack(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	target := productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)
	target.ConfigDigest = strings.Repeat("f", 64)

	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{target})
	require.ErrorIs(t, err, types.ErrProductionReleaseConfigInvalid)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReleaseRepositoryRollsBackReleaseWhenAnyTargetFails(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	targets := []*types.ProductionReleaseTarget{
		productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1),
		productionReleaseTarget(releaseTarget2, "missing-kb", releaseKnowledge2),
	}
	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release, targets)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionReleaseRepositoryRejectsUntrustedTenantAndActor(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	release.CreatedBy = reviewBusinessActor
	err := repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)})
	require.ErrorIs(t, err, types.ErrProductionForbidden)

	release.CreatedBy = ""
	err = repo.CreateRelease(productionReleaseContext(reviewTenantID+1, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)})
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseTargetTransitionIsScopedAndCannotBypassHeadOrTrustedTime(t *testing.T) {
	now := time.Date(2026, 7, 21, 4, 5, 6, 987654321, time.UTC)
	ownedNow := now.Truncate(time.Second)
	repo, _ := newProductionReleaseRepoFixture(t, &productionReleaseTestClock{current: now})
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))

	changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetActive, nil)
	require.ErrorIs(t, err, types.ErrProductionReleaseLifecycle)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID+1, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed, types.JSONMap{"failed_at": now.Add(-time.Hour)})
	require.ErrorIs(t, err, types.ErrProductionReleasePatchInvalid)
	require.False(t, changed)
	changed, err = repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget1,
		types.ReleaseTargetBuilding, types.ReleaseTargetFailed, nil)
	require.NoError(t, err)
	require.True(t, changed)
	target, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, ownedNow, *target.FailedAt)
	require.Equal(t, ownedNow.Add(30*24*time.Hour), *target.RetentionUntil)
}

func TestProductionProjectionHeadInitialActivationAndCASSwitchRefreshTriggerState(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))
	for _, targetID := range []string{releaseTarget1, releaseTarget2} {
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetID,
			types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}

	head, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, head.LockVersion)
	firstTarget, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetActive, firstTarget.Status)

	head, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID,
		reviewDocumentID, releaseKBOne, releaseTarget2, 1)
	require.NoError(t, err)
	require.Equal(t, 2, head.LockVersion)
	require.Equal(t, releaseTarget2, head.ActiveReleaseTargetID)
	firstTarget, err = repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget1)
	require.NoError(t, err)
	secondTarget, err := repo.GetTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, releaseTarget2)
	require.NoError(t, err)
	require.Equal(t, types.ReleaseTargetRolledBack, firstTarget.Status)
	require.NotNil(t, firstTarget.RetentionUntil)
	require.Equal(t, types.ReleaseTargetActive, secondTarget.Status)
}

func TestProductionProjectionHeadRejectsStaleLockAndPreservesCurrentHead(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))
	for _, targetID := range []string{releaseTarget1, releaseTarget2} {
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetID, types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}
	_, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)
	_, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 0)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	scope, err := repo.ResolveScopes(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, []string{releaseKBOne})
	require.NoError(t, err)
	require.Equal(t, []string{releaseKnowledge1}, scope[releaseKBOne].ActiveKnowledgeIDs)
}

func TestProductionProjectionHeadConcurrentCASHasSingleWinner(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	releases := []*types.ProductionRelease{
		productionRelease(releaseIDOne, reviewVersionOne, reviewID(700)),
		productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710)),
	}
	targetIDs := []string{releaseTarget1, releaseTarget2}
	knowledgeIDs := []string{releaseKnowledge1, releaseKnowledge2}
	for index := range releases {
		require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), releases[index],
			[]*types.ProductionReleaseTarget{productionReleaseTarget(targetIDs[index], releaseKBOne, knowledgeIDs[index])}))
		changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), targetIDs[index], types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
		require.NoError(t, err)
		require.True(t, changed)
	}
	_, err := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget1, 0)
	require.NoError(t, err)

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []string{releaseTarget1, releaseTarget2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, switchErr := repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, candidate, 1)
			results <- switchErr
		}()
	}
	wg.Wait()
	close(results)
	var successes int
	for switchErr := range results {
		if switchErr == nil {
			successes++
		} else {
			require.ErrorIs(t, switchErr, types.ErrProductionProjectionConflict)
		}
	}
	require.Equal(t, 1, successes)
}

func TestProductionReleaseResolveScopesClassifiesRequestedKBsDeterministically(t *testing.T) {
	repo, _ := newProductionReleaseRepoFixture(t, nil)
	release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), release,
		[]*types.ProductionReleaseTarget{
			productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2),
			productionReleaseTarget(releaseTarget1, releaseKBTwo, releaseKnowledge1),
		}))
	changed, err := repo.TransitionTarget(productionReleaseContext(reviewTenantID, reviewAuthorID), releaseTarget2, types.ReleaseTargetBuilding, types.ReleaseTargetReady, nil)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = repo.SwitchHead(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 0)
	require.NoError(t, err)

	scopes, err := repo.ResolveScopes(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, []string{releaseKBTwo, releaseKBOne, releaseKBOne, "missing-kb"})
	require.NoError(t, err)
	require.Len(t, scopes, 3)
	require.Equal(t, []string{releaseKnowledge2}, scopes[releaseKBOne].ActiveKnowledgeIDs)
	require.Empty(t, scopes[releaseKBOne].InactiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge2}, scopes[releaseKBOne].AllProductionKnowledgeIDs)
	require.Empty(t, scopes[releaseKBTwo].ActiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge1}, scopes[releaseKBTwo].InactiveKnowledgeIDs)
	require.Equal(t, []string{releaseKnowledge1}, scopes[releaseKBTwo].AllProductionKnowledgeIDs)
	require.Empty(t, scopes["missing-kb"].AllProductionKnowledgeIDs)
}

func TestProductionReleaseHistoryIsTenantScopedAndDeterministic(t *testing.T) {
	clock := &productionReleaseTestClock{current: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}
	repo, _ := newProductionReleaseRepoFixture(t, clock)
	first := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
	second := productionRelease(releaseIDTwo, reviewVersionTwo, reviewID(710))
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), first,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
	clock.current = time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.CreateRelease(productionReleaseContext(reviewTenantID, reviewAuthorID), second,
		[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget2, releaseKBOne, releaseKnowledge2)}))

	history, err := repo.ListProjectionHistory(productionReleaseContext(reviewTenantID, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.NoError(t, err)
	require.Equal(t, []string{releaseTarget2, releaseTarget1}, []string{history[0].ID, history[1].ID})
	_, err = repo.ListProjectionHistory(productionReleaseContext(reviewTenantID+1, reviewAuthorID), reviewTenantID, reviewDocumentID, releaseKBOne)
	require.ErrorIs(t, err, types.ErrProductionForbidden)
}

func TestProductionReleaseRepositoryJoinsExistingUnitOfWorkTransaction(t *testing.T) {
	repo, db := newProductionReleaseRepoFixture(t, nil)
	uow := NewProductionUnitOfWork(db)
	errRollback := context.Canceled
	err := uow.WithinTransaction(productionReleaseContext(reviewTenantID, reviewAuthorID), func(txCtx context.Context) error {
		release := productionRelease(releaseIDOne, reviewVersionOne, reviewID(700))
		require.NoError(t, repo.CreateRelease(txCtx, release,
			[]*types.ProductionReleaseTarget{productionReleaseTarget(releaseTarget1, releaseKBOne, releaseKnowledge1)}))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	var count int64
	require.NoError(t, db.Model(&types.ProductionRelease{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProductionProjectionHeadPostgresUsesExactCASPredicate(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewProductionReleaseRepository(db)
	ctx := productionReleaseContext(reviewTenantID, reviewAuthorID)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE production_projection_heads`).
		WithArgs(releaseTarget2, sqlmock.AnyArg(), reviewTenantID, reviewDocumentID, releaseKBOne, 7).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = repo.SwitchHead(ctx, reviewTenantID, reviewDocumentID, releaseKBOne, releaseTarget2, 7)
	require.ErrorIs(t, err, types.ErrProductionProjectionConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
