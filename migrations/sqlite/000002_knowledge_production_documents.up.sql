CREATE TABLE IF NOT EXISTS production_source_sets (
    id VARCHAR(36) PRIMARY KEY,
    project_id VARCHAR(36) NOT NULL REFERENCES production_projects(id) ON DELETE CASCADE,
    document_type_id VARCHAR(36) NOT NULL REFERENCES production_document_types(id) ON DELETE RESTRICT,
    time_range_start DATETIME NULL,
    time_range_end DATETIME NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'collecting',
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    frozen_at DATETIME NULL,
    CONSTRAINT chk_production_source_sets_status CHECK (status IN ('collecting', 'ready', 'failed', 'frozen'))
);

CREATE INDEX IF NOT EXISTS idx_production_source_sets_project
    ON production_source_sets (project_id);
CREATE INDEX IF NOT EXISTS idx_production_source_sets_document_type
    ON production_source_sets (document_type_id);

CREATE TRIGGER IF NOT EXISTS trg_production_source_sets_prevent_reopen
    BEFORE UPDATE OF status ON production_source_sets
    FOR EACH ROW
    WHEN OLD.status = 'frozen' AND NEW.status <> 'frozen'
BEGIN
    SELECT RAISE(ABORT, 'frozen source sets cannot be reopened');
END;

CREATE TABLE IF NOT EXISTS production_source_items (
    id VARCHAR(36) PRIMARY KEY,
    source_set_id VARCHAR(36) NOT NULL REFERENCES production_source_sets(id) ON DELETE CASCADE,
    source_kind VARCHAR(24) NOT NULL,
    source_system VARCHAR(255) NOT NULL DEFAULT '',
    external_id VARCHAR(255) NOT NULL DEFAULT '',
    source_uri TEXT NULL,
    title VARCHAR(255) NOT NULL,
    mime_type VARCHAR(127) NOT NULL,
    content_digest VARCHAR(64) NOT NULL,
    captured_at DATETIME NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'candidate',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_source_items_kind CHECK (source_kind IN ('upload', 'datasource', 'mcp', 'skill', 'manual')),
    CONSTRAINT chk_production_source_items_status CHECK (status IN ('candidate', 'accepted', 'rejected', 'unavailable'))
);

CREATE INDEX IF NOT EXISTS idx_production_source_items_source_set
    ON production_source_items (source_set_id);

CREATE TABLE IF NOT EXISTS production_evidence_snapshots (
    id VARCHAR(36) PRIMARY KEY,
    source_item_id VARCHAR(36) NOT NULL REFERENCES production_source_items(id) ON DELETE CASCADE,
    snapshot_type VARCHAR(24) NOT NULL,
    storage_path TEXT NULL,
    inline_content TEXT NULL,
    content_digest VARCHAR(64) NOT NULL,
    redaction_metadata TEXT NOT NULL DEFAULT '{}',
    captured_by_run_id VARCHAR(36) NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_evidence_snapshots_type CHECK (snapshot_type IN ('text', 'json', 'file', 'tool_result')),
    CONSTRAINT chk_production_evidence_snapshots_content CHECK (storage_path IS NOT NULL OR inline_content IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_production_evidence_snapshots_source_item
    ON production_evidence_snapshots (source_item_id);

CREATE TABLE IF NOT EXISTS production_documents (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL REFERENCES production_projects(id) ON DELETE RESTRICT,
    document_type_id VARCHAR(36) NOT NULL REFERENCES production_document_types(id) ON DELETE RESTRICT,
    document_type_schema_version INTEGER NOT NULL,
    title VARCHAR(255) NOT NULL,
    current_version_id VARCHAR(36) NULL REFERENCES production_document_versions(id) ON DELETE RESTRICT,
    latest_approved_version_id VARCHAR(36) NULL REFERENCES production_document_versions(id) ON DELETE RESTRICT,
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_documents_status CHECK (status IN ('draft', 'annotating', 'in_review', 'approved', 'publishing', 'published', 'archived'))
);

CREATE INDEX IF NOT EXISTS idx_production_documents_tenant
    ON production_documents (tenant_id);
CREATE INDEX IF NOT EXISTS idx_production_documents_project
    ON production_documents (project_id);
CREATE INDEX IF NOT EXISTS idx_production_documents_document_type
    ON production_documents (document_type_id);

CREATE TABLE IF NOT EXISTS production_document_versions (
    id VARCHAR(36) PRIMARY KEY,
    document_id VARCHAR(36) NOT NULL REFERENCES production_documents(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL,
    parent_version_id VARCHAR(36) NULL REFERENCES production_document_versions(id) ON DELETE RESTRICT,
    source_set_id VARCHAR(36) NOT NULL REFERENCES production_source_sets(id) ON DELETE RESTRICT,
    origin VARCHAR(20) NOT NULL,
    change_summary TEXT NOT NULL DEFAULT '',
    content_digest VARCHAR(64) NOT NULL,
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    frozen_at DATETIME NULL,
    CONSTRAINT chk_production_document_versions_origin CHECK (origin IN ('ai', 'human', 'mixed', 'rollback')),
    UNIQUE(document_id, version_number)
);

CREATE INDEX IF NOT EXISTS idx_production_document_versions_document
    ON production_document_versions (document_id, version_number);
CREATE INDEX IF NOT EXISTS idx_production_document_versions_source_set
    ON production_document_versions (source_set_id);

CREATE TABLE IF NOT EXISTS production_document_blocks (
    id VARCHAR(36) PRIMARY KEY,
    version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    logical_block_id VARCHAR(36) NOT NULL,
    block_type VARCHAR(24) NOT NULL,
    position INTEGER NOT NULL,
    content TEXT NOT NULL,
    attributes TEXT NOT NULL,
    evidence_refs TEXT NOT NULL,
    ai_provenance TEXT NOT NULL,
    content_digest VARCHAR(64) NOT NULL,
    UNIQUE(version_id, logical_block_id)
);

CREATE INDEX IF NOT EXISTS idx_production_document_blocks_version
    ON production_document_blocks (version_id, position);

CREATE TABLE IF NOT EXISTS production_block_lineage (
    id VARCHAR(36) PRIMARY KEY,
    from_version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    from_logical_block_id VARCHAR(36) NOT NULL,
    to_version_id VARCHAR(36) NOT NULL REFERENCES production_document_versions(id) ON DELETE CASCADE,
    to_logical_block_id VARCHAR(36) NOT NULL,
    relation VARCHAR(24) NOT NULL,
    UNIQUE(from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation)
);

CREATE INDEX IF NOT EXISTS idx_production_block_lineage_from_version
    ON production_block_lineage (from_version_id);
CREATE INDEX IF NOT EXISTS idx_production_block_lineage_to_version
    ON production_block_lineage (to_version_id);

CREATE TRIGGER IF NOT EXISTS trg_production_source_items_prevent_frozen_accepted_update
    BEFORE UPDATE ON production_source_items
    FOR EACH ROW
    WHEN OLD.status = 'accepted' AND EXISTS (
        SELECT 1 FROM production_source_sets WHERE id = OLD.source_set_id AND status = 'frozen'
    )
BEGIN
    SELECT RAISE(ABORT, 'accepted source items are immutable when their source set is frozen');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_source_items_prevent_frozen_accepted_delete
    BEFORE DELETE ON production_source_items
    FOR EACH ROW
    WHEN OLD.status = 'accepted' AND EXISTS (
        SELECT 1 FROM production_source_sets WHERE id = OLD.source_set_id AND status = 'frozen'
    )
BEGIN
    SELECT RAISE(ABORT, 'accepted source items are immutable when their source set is frozen');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_evidence_snapshots_prevent_update
    BEFORE UPDATE ON production_evidence_snapshots
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'evidence snapshots are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_evidence_snapshots_prevent_delete
    BEFORE DELETE ON production_evidence_snapshots
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'evidence snapshots are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_document_versions_prevent_update
    BEFORE UPDATE ON production_document_versions
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'document versions are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_document_versions_prevent_delete
    BEFORE DELETE ON production_document_versions
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'document versions are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_document_blocks_prevent_update
    BEFORE UPDATE ON production_document_blocks
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'document blocks are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_document_blocks_prevent_delete
    BEFORE DELETE ON production_document_blocks
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'document blocks are append-only');
END;
