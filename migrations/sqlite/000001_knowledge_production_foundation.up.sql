CREATE TABLE IF NOT EXISTS production_projects (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_user_id VARCHAR(36) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME NULL,
    CONSTRAINT chk_production_projects_status CHECK (status IN ('active', 'archived'))
);

CREATE INDEX IF NOT EXISTS idx_production_projects_tenant
    ON production_projects (tenant_id)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS production_project_members (
    project_id VARCHAR(36) NOT NULL REFERENCES production_projects(id) ON DELETE CASCADE,
    user_id VARCHAR(36) NOT NULL,
    role VARCHAR(32) NOT NULL,
    assigned_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_project_members_live_role
    ON production_project_members (project_id, user_id, role)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS production_document_types (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    code VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL,
    block_schema TEXT NOT NULL DEFAULT '{}',
    source_requirements TEXT NOT NULL DEFAULT '{}',
    skill_bindings TEXT NOT NULL DEFAULT '{}',
    workflow_plan TEXT NOT NULL DEFAULT '{"steps":[],"version":1}',
    quality_rules TEXT NOT NULL DEFAULT '{}',
    review_policy TEXT NOT NULL DEFAULT '{}',
    publication_policy TEXT NOT NULL DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME NULL,
    CONSTRAINT chk_production_document_types_status CHECK (status IN ('draft', 'active', 'retired'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_live_version
    ON production_document_types (tenant_id, code, schema_version)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_active_code
    ON production_document_types (tenant_id, code)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE TRIGGER IF NOT EXISTS trg_production_document_types_prevent_active_definition_update
    BEFORE UPDATE OF tenant_id, code, name, description, schema_version, block_schema,
        source_requirements, skill_bindings, workflow_plan, quality_rules, review_policy,
        publication_policy, created_by ON production_document_types
    FOR EACH ROW
    WHEN OLD.status IN ('active', 'retired') AND (
        NEW.tenant_id IS NOT OLD.tenant_id OR
        NEW.code IS NOT OLD.code OR
        NEW.name IS NOT OLD.name OR
        NEW.description IS NOT OLD.description OR
        NEW.schema_version IS NOT OLD.schema_version OR
        NEW.block_schema IS NOT OLD.block_schema OR
        NEW.source_requirements IS NOT OLD.source_requirements OR
        NEW.skill_bindings IS NOT OLD.skill_bindings OR
        NEW.workflow_plan IS NOT OLD.workflow_plan OR
        NEW.quality_rules IS NOT OLD.quality_rules OR
        NEW.review_policy IS NOT OLD.review_policy OR
        NEW.publication_policy IS NOT OLD.publication_policy OR
        NEW.created_by IS NOT OLD.created_by
    )
BEGIN
    SELECT RAISE(ABORT, 'active production document type definitions are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_document_types_enforce_status_transition
    BEFORE UPDATE OF status ON production_document_types
    FOR EACH ROW
    WHEN (OLD.status = 'active' AND NEW.status NOT IN ('active', 'retired')) OR
        (OLD.status = 'retired' AND NEW.status <> 'retired')
BEGIN
    SELECT RAISE(ABORT, 'invalid production document type status transition');
END;

CREATE TABLE IF NOT EXISTS production_idempotency_keys (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    actor_user_id VARCHAR(36) NOT NULL,
    route VARCHAR(255) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    request_digest VARCHAR(64) NOT NULL,
    status_code INTEGER NULL,
    response_body TEXT NULL,
    completed_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, actor_user_id, route, idempotency_key)
);
