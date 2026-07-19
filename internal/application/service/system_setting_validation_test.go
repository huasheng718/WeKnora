package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestValidateWorkerConcurrencyMinimums(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   any
		wantErr bool
	}{
		{name: "core zero", key: "asynq.core_concurrency", value: 0, wantErr: true},
		{name: "core minimum", key: "asynq.core_concurrency", value: 1},
		{name: "postprocess minimum", key: "asynq.postprocess_concurrency", value: 1},
		{name: "enrichment minimum", key: "asynq.enrichment_concurrency", value: 1},
		{name: "maintenance minimum", key: "asynq.maintenance_concurrency", value: 1},
		{name: "shared minimum", key: "asynq.shared_concurrency", value: 1},
		{name: "production zero", key: "asynq.production_concurrency", value: 0, wantErr: true},
		{name: "production minimum", key: "asynq.production_concurrency", value: 1},
		{name: "wiki zero", key: "asynq.wiki_concurrency", value: 0, wantErr: true},
		{name: "wiki minimum", key: "asynq.wiki_concurrency", value: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRegistryEntry(tt.key, tt.value)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestProductionWorkerConcurrencyUsesRegistryEnvAndDatabasePrecedence(t *testing.T) {
	const key = "asynq.production_concurrency"
	spec, ok := registry[key]
	if !ok {
		t.Fatalf("%q must be registered", key)
	}
	if spec.Type != "int" || spec.EnvName != "WEKNORA_ASYNQ_PRODUCTION_CONCURRENCY" ||
		spec.Default != int64(types.DefaultProductionWorkerConcurrency) || !spec.RequiresRestart {
		t.Fatalf("unexpected production worker setting spec: %+v", spec)
	}

	repo := &systemSettingTestRepository{rows: map[string]*types.SystemSetting{}}
	service := &systemSettingService{repo: repo, cache: make(map[string]*types.SystemSetting)}
	t.Setenv(spec.EnvName, "7")
	if got := service.GetInt(context.Background(), key, spec.EnvName, int64(types.DefaultProductionWorkerConcurrency)); got != 7 {
		t.Fatalf("environment fallback = %d, want 7", got)
	}

	if _, err := service.Update(context.Background(), key, 9); err != nil {
		t.Fatalf("positive production concurrency must update: %v", err)
	}
	if got := service.GetInt(context.Background(), key, spec.EnvName, int64(types.DefaultProductionWorkerConcurrency)); got != 9 {
		t.Fatalf("database override = %d, want 9", got)
	}
	if _, err := service.Update(context.Background(), key, 0); err == nil {
		t.Fatal("non-positive production concurrency must be rejected")
	}

	delete(repo.rows, key)
	t.Setenv(spec.EnvName, "")
	service.mu.Lock()
	delete(service.cache, key)
	service.mu.Unlock()
	if got := service.GetInt(context.Background(), key, spec.EnvName, int64(types.DefaultProductionWorkerConcurrency)); got != int64(types.DefaultProductionWorkerConcurrency) {
		t.Fatalf("default fallback = %d, want %d", got, types.DefaultProductionWorkerConcurrency)
	}
}

type systemSettingTestRepository struct {
	rows map[string]*types.SystemSetting
}

func (r *systemSettingTestRepository) Get(_ context.Context, key string) (*types.SystemSetting, error) {
	return r.rows[key], nil
}

func (r *systemSettingTestRepository) List(context.Context) ([]*types.SystemSetting, error) {
	rows := make([]*types.SystemSetting, 0, len(r.rows))
	for _, row := range r.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *systemSettingTestRepository) Upsert(_ context.Context, setting *types.SystemSetting) error {
	r.rows[setting.Key] = setting
	return nil
}

func (r *systemSettingTestRepository) Delete(_ context.Context, key string) (bool, error) {
	_, ok := r.rows[key]
	delete(r.rows, key)
	return ok, nil
}
