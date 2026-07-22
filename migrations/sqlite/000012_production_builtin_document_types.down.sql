DROP TRIGGER IF EXISTS trg_production_document_types_prevent_referenced_builtin_delete;

CREATE TRIGGER trg_production_document_types_prevent_referenced_builtin_delete
    BEFORE DELETE ON production_document_types
    FOR EACH ROW
    WHEN OLD.origin = 'builtin' AND (
        EXISTS (
            SELECT 1 FROM production_source_sets
            WHERE tenant_id = OLD.tenant_id AND document_type_id = OLD.id
        ) OR EXISTS (
            SELECT 1 FROM production_documents
            WHERE tenant_id = OLD.tenant_id AND document_type_id = OLD.id
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'referenced built-in production document types prevent rollback');
END;

DELETE FROM production_document_types
WHERE origin = 'builtin'
  AND schema_version = 1
  AND template_key IN ('sop', 'policy_process', 'product_service_guide', 'faq', 'incident_playbook');

DROP TRIGGER IF EXISTS trg_production_document_types_prevent_referenced_builtin_delete;

PRAGMA foreign_keys = OFF;

DROP TRIGGER IF EXISTS trg_production_document_types_prevent_active_definition_update;
DROP TRIGGER IF EXISTS trg_production_document_types_enforce_status_transition;
DROP INDEX IF EXISTS uq_production_document_types_live_template_version;

CREATE TABLE production_document_types_without_lineage (
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

INSERT INTO production_document_types_without_lineage (
    id, tenant_id, code, name, description, schema_version, block_schema,
    source_requirements, skill_bindings, workflow_plan, quality_rules, review_policy,
    publication_policy, status, created_by, created_at, updated_at, deleted_at
)
SELECT
    id, tenant_id, code, name, description, schema_version, block_schema,
    source_requirements, skill_bindings, workflow_plan, quality_rules, review_policy,
    publication_policy, status, created_by, created_at, updated_at, deleted_at
FROM production_document_types
WHERE origin = 'custom';

DROP TABLE production_document_types;
ALTER TABLE production_document_types_without_lineage RENAME TO production_document_types;

CREATE UNIQUE INDEX uq_production_document_types_live_version
    ON production_document_types (tenant_id, code, schema_version)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX uq_production_document_types_active_code
    ON production_document_types (tenant_id, code)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE UNIQUE INDEX uq_production_document_types_id_tenant
    ON production_document_types (id, tenant_id);

CREATE TRIGGER trg_production_document_types_prevent_active_definition_update
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

CREATE TRIGGER trg_production_document_types_enforce_status_transition
    BEFORE UPDATE OF status ON production_document_types
    FOR EACH ROW
    WHEN (OLD.status = 'active' AND NEW.status NOT IN ('active', 'retired')) OR
        (OLD.status = 'retired' AND NEW.status <> 'retired')
BEGIN
    SELECT RAISE(ABORT, 'invalid production document type status transition');
END;

PRAGMA foreign_keys = ON;
