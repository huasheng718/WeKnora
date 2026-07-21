package service

import (
	"context"
	"path/filepath"
	"testing"

	apprepository "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type governedKBReleaseRepo struct {
	interfaces.ProductionReleaseRepository
	governed map[string]bool
}

func (r *governedKBReleaseRepo) ResolveScopes(_ context.Context, _ uint64, kbIDs []string) (map[string]types.ProductionKnowledgeScope, error) {
	result := make(map[string]types.ProductionKnowledgeScope, len(kbIDs))
	for _, kbID := range kbIDs {
		if r.governed[kbID] {
			result[kbID] = types.ProductionKnowledgeScope{
				InactiveKnowledgeIDs:      []string{"projection-history"},
				AllProductionKnowledgeIDs: []string{"projection-history"},
			}
		} else {
			result[kbID] = types.ProductionKnowledgeScope{}
		}
	}
	return result, nil
}

func TestCopyKnowledgeBaseRejectsGovernedSourceOrTargetBeforeCreatingOrSynchronizing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		governed map[string]bool
	}{
		{name: "source", governed: map[string]bool{"src": true}},
		{name: "target", governed: map[string]bool{"dst": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeKBRepo()
			repo.rows["src"] = &types.KnowledgeBase{ID: "src", TenantID: 1, EmbeddingModelID: "embed"}
			repo.rows["dst"] = &types.KnowledgeBase{ID: "dst", TenantID: 1, EmbeddingModelID: "embed"}
			svc := newPR3KBService(repo, &fakeRegistry{}, &fakeOwnership{})
			svc.productionReleaseRepo = &governedKBReleaseRepo{governed: tc.governed}

			_, _, err := svc.CopyKnowledgeBase(ctxWithTenant(1), "src", "dst")
			require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
			require.Len(t, repo.rows, 2)
		})
	}
}

func TestDeleteKnowledgeBaseRejectsGovernedTargetBeforeSoftDeleteOrEnqueue(t *testing.T) {
	repo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	repo.rows["kb-1"] = &types.KnowledgeBase{ID: "kb-1", TenantID: 1}
	svc := &knowledgeBaseService{
		repo: repo, asynqClient: kbDeleteTaskEnqueuer{},
		productionReleaseRepo: &governedKBReleaseRepo{governed: map[string]bool{"kb-1": true}},
	}

	err := svc.DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), "kb-1")
	require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	require.Empty(t, repo.deletedID)
	require.Contains(t, repo.rows, "kb-1")
}

func TestCopyKnowledgeBaseRejectsPersistedProjectionHistoryAfterSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projection-clone-guard.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE production_release_targets (
		id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, knowledge_id TEXT NOT NULL, status TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE production_projection_heads (
		tenant_id INTEGER NOT NULL, document_id TEXT NOT NULL,
		target_knowledge_base_id TEXT NOT NULL, active_release_target_id TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO production_release_targets
		(id, tenant_id, document_id, target_knowledge_base_id, knowledge_id, status)
		VALUES ('target-old', 1, 'document-1', 'src', 'projection-history', 'rolled_back')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	reopened, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	reopenedSQL, err := reopened.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopenedSQL.Close() })

	kbs := newFakeKBRepo()
	kbs.rows["src"] = &types.KnowledgeBase{ID: "src", TenantID: 1, EmbeddingModelID: "embed"}
	kbs.rows["dst"] = &types.KnowledgeBase{ID: "dst", TenantID: 1, EmbeddingModelID: "embed"}
	svc := newPR3KBService(kbs, &fakeRegistry{}, &fakeOwnership{})
	svc.productionReleaseRepo = apprepository.NewProductionReleaseRepository(reopened)

	_, _, err = svc.CopyKnowledgeBase(ctxWithTenant(1), "src", "dst")
	require.ErrorIs(t, err, types.ErrProductionProjectionImmutable)
	require.Len(t, kbs.rows, 2)
}
