CREATE TABLE IF NOT EXISTS production_projects (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_user_id VARCHAR(36) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL
);

CREATE INDEX IF NOT EXISTS idx_production_projects_tenant
    ON production_projects (tenant_id)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS production_project_members (
    project_id VARCHAR(36) NOT NULL REFERENCES production_projects(id) ON DELETE CASCADE,
    user_id VARCHAR(36) NOT NULL,
    role VARCHAR(32) NOT NULL,
    assigned_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_project_members_live_role
    ON production_project_members (project_id, user_id, role)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS production_document_types (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    code VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL,
    block_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_requirements JSONB NOT NULL DEFAULT '{}'::jsonb,
    skill_bindings JSONB NOT NULL DEFAULT '{}'::jsonb,
    quality_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    review_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    publication_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_live_version
    ON production_document_types (tenant_id, code, schema_version)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_active_code
    ON production_document_types (tenant_id, code)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS production_idempotency_keys (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    actor_user_id VARCHAR(36) NOT NULL,
    route VARCHAR(255) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    request_digest VARCHAR(64) NOT NULL,
    status_code INTEGER NULL,
    response_body JSONB NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, actor_user_id, route, idempotency_key)
);
