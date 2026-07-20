CREATE UNIQUE INDEX IF NOT EXISTS uq_production_projects_id_tenant
    ON production_projects (id, tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_types_id_tenant
    ON production_document_types (id, tenant_id);

CREATE TABLE IF NOT EXISTS production_source_sets (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_type_id VARCHAR(36) NOT NULL,
    time_range_start DATETIME NULL,
    time_range_end DATETIME NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'collecting',
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    frozen_at DATETIME NULL,
    CONSTRAINT chk_production_source_sets_status CHECK (status IN ('collecting', 'ready', 'failed', 'frozen')),
    UNIQUE(id, tenant_id, project_id),
    FOREIGN KEY (project_id, tenant_id) REFERENCES production_projects(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (document_type_id, tenant_id) REFERENCES production_document_types(id, tenant_id) ON DELETE RESTRICT
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

CREATE TRIGGER IF NOT EXISTS trg_production_source_sets_prevent_frozen_delete
    BEFORE DELETE ON production_source_sets
    FOR EACH ROW
    WHEN OLD.status = 'frozen'
BEGIN
    SELECT RAISE(ABORT, 'frozen source sets cannot be deleted');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_source_sets_prevent_frozen_replace
    BEFORE INSERT ON production_source_sets
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_source_sets WHERE id = NEW.id AND status = 'frozen'
    )
BEGIN
    SELECT RAISE(ABORT, 'frozen source sets cannot be replaced');
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
    captured_by_tool_call_id VARCHAR(36) NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_evidence_snapshots_type CHECK (snapshot_type IN ('text', 'json', 'file', 'tool_result')),
    CONSTRAINT chk_production_evidence_snapshots_content CHECK ((storage_path IS NOT NULL) + (inline_content IS NOT NULL) = 1)
);

CREATE INDEX IF NOT EXISTS idx_production_evidence_snapshots_source_item
    ON production_evidence_snapshots (source_item_id);
CREATE INDEX IF NOT EXISTS idx_production_evidence_snapshots_captured_tool_call
    ON production_evidence_snapshots (captured_by_tool_call_id);

CREATE TABLE IF NOT EXISTS production_documents (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_type_id VARCHAR(36) NOT NULL,
    document_type_schema_version INTEGER NOT NULL,
    title VARCHAR(255) NOT NULL,
    current_version_id VARCHAR(36) NULL,
    latest_approved_version_id VARCHAR(36) NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_documents_status CHECK (status IN ('draft', 'annotating', 'in_review', 'approved', 'publishing', 'published', 'archived')),
    UNIQUE(id, tenant_id, project_id),
    FOREIGN KEY (project_id, tenant_id) REFERENCES production_projects(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (document_type_id, tenant_id) REFERENCES production_document_types(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (current_version_id, id) REFERENCES production_document_versions(id, document_id) ON DELETE RESTRICT,
    FOREIGN KEY (latest_approved_version_id, id) REFERENCES production_document_versions(id, document_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_documents_tenant
    ON production_documents (tenant_id);
CREATE INDEX IF NOT EXISTS idx_production_documents_project
    ON production_documents (project_id);
CREATE INDEX IF NOT EXISTS idx_production_documents_document_type
    ON production_documents (document_type_id);

CREATE TABLE IF NOT EXISTS production_document_versions (
    id VARCHAR(36) PRIMARY KEY,
    document_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    version_number INTEGER NOT NULL,
    parent_version_id VARCHAR(36) NULL,
    source_set_id VARCHAR(36) NOT NULL,
    origin VARCHAR(20) NOT NULL,
    change_summary TEXT NOT NULL DEFAULT '',
    content_digest VARCHAR(64) NOT NULL,
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    frozen_at DATETIME NULL,
    CONSTRAINT chk_production_document_versions_origin CHECK (origin IN ('ai', 'human', 'mixed', 'rollback')),
    UNIQUE(document_id, version_number),
    UNIQUE(id, document_id),
    FOREIGN KEY (parent_version_id, document_id) REFERENCES production_document_versions(id, document_id) ON DELETE RESTRICT,
    FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (source_set_id, tenant_id, project_id) REFERENCES production_source_sets(id, tenant_id, project_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_document_versions_document
    ON production_document_versions (document_id, tenant_id, project_id, version_number);
CREATE INDEX IF NOT EXISTS idx_production_document_versions_source_set
    ON production_document_versions (source_set_id, tenant_id, project_id);

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
	CONSTRAINT chk_production_block_lineage_relation CHECK (relation IN ('same', 'split', 'merged')),
	FOREIGN KEY (from_version_id, from_logical_block_id) REFERENCES production_document_blocks(version_id, logical_block_id) ON DELETE RESTRICT,
	FOREIGN KEY (to_version_id, to_logical_block_id) REFERENCES production_document_blocks(version_id, logical_block_id) ON DELETE RESTRICT,
    UNIQUE(from_version_id, from_logical_block_id, to_version_id, to_logical_block_id, relation)
);

CREATE INDEX IF NOT EXISTS idx_production_block_lineage_from_version
    ON production_block_lineage (from_version_id);
CREATE INDEX IF NOT EXISTS idx_production_block_lineage_to_version
    ON production_block_lineage (to_version_id);

CREATE TRIGGER IF NOT EXISTS trg_production_block_lineage_prevent_update
	BEFORE UPDATE ON production_block_lineage
	FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'block lineage is append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_block_lineage_prevent_delete
	BEFORE DELETE ON production_block_lineage
	FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'block lineage is append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_block_lineage_prevent_replace
	BEFORE INSERT ON production_block_lineage
	FOR EACH ROW
	WHEN EXISTS (
		SELECT 1 FROM production_block_lineage
		WHERE id = NEW.id OR (
			from_version_id = NEW.from_version_id
			AND from_logical_block_id = NEW.from_logical_block_id
			AND to_version_id = NEW.to_version_id
			AND to_logical_block_id = NEW.to_logical_block_id
			AND relation = NEW.relation
		)
	)
BEGIN
	SELECT RAISE(ABORT, 'block lineage is append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_source_items_prevent_frozen_insert
	BEFORE INSERT ON production_source_items
	FOR EACH ROW
	WHEN EXISTS (
		SELECT 1 FROM production_source_sets WHERE id = NEW.source_set_id AND status = 'frozen'
	)
BEGIN
	SELECT RAISE(ABORT, 'source items cannot be inserted into frozen source sets');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_source_items_prevent_frozen_update
    BEFORE UPDATE ON production_source_items
    FOR EACH ROW
	WHEN EXISTS (
		SELECT 1 FROM production_source_sets WHERE id = OLD.source_set_id AND status = 'frozen'
	) OR EXISTS (
		SELECT 1 FROM production_source_sets WHERE id = NEW.source_set_id AND status = 'frozen'
	)
BEGIN
	SELECT RAISE(ABORT, 'source items in frozen source sets are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_source_items_prevent_frozen_delete
    BEFORE DELETE ON production_source_items
    FOR EACH ROW
	WHEN EXISTS (
		SELECT 1 FROM production_source_sets WHERE id = OLD.source_set_id AND status = 'frozen'
	)
BEGIN
	SELECT RAISE(ABORT, 'source items in frozen source sets are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_evidence_snapshots_prevent_frozen_insert
	BEFORE INSERT ON production_evidence_snapshots
	FOR EACH ROW
	WHEN EXISTS (
		SELECT 1 FROM production_source_items item
		JOIN production_source_sets source_set ON source_set.id = item.source_set_id
		WHERE item.id = NEW.source_item_id AND source_set.status = 'frozen'
	)
BEGIN
	SELECT RAISE(ABORT, 'evidence cannot be inserted into frozen source sets');
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

CREATE TRIGGER IF NOT EXISTS trg_production_evidence_snapshots_prevent_replace
    BEFORE INSERT ON production_evidence_snapshots
    FOR EACH ROW
    WHEN EXISTS (SELECT 1 FROM production_evidence_snapshots WHERE id = NEW.id)
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

CREATE TRIGGER IF NOT EXISTS trg_production_document_versions_prevent_replace
    BEFORE INSERT ON production_document_versions
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1
        FROM production_document_versions
        WHERE id = NEW.id OR (document_id = NEW.document_id AND version_number = NEW.version_number)
    )
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

CREATE TRIGGER IF NOT EXISTS trg_production_document_blocks_prevent_replace
    BEFORE INSERT ON production_document_blocks
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1
        FROM production_document_blocks
        WHERE id = NEW.id OR (version_id = NEW.version_id AND logical_block_id = NEW.logical_block_id)
    )
BEGIN
    SELECT RAISE(ABORT, 'document blocks are append-only');
END;
