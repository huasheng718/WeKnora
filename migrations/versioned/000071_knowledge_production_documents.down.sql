DROP TRIGGER IF EXISTS trg_production_document_blocks_prevent_mutation ON production_document_blocks;
DROP FUNCTION IF EXISTS prevent_production_document_block_mutation();
DROP TRIGGER IF EXISTS trg_production_document_versions_prevent_mutation ON production_document_versions;
DROP FUNCTION IF EXISTS prevent_production_document_version_mutation();
DROP TRIGGER IF EXISTS trg_production_evidence_snapshots_prevent_mutation ON production_evidence_snapshots;
DROP FUNCTION IF EXISTS prevent_production_evidence_snapshot_mutation();
DROP TRIGGER IF EXISTS trg_production_source_items_prevent_frozen_accepted_mutation ON production_source_items;
DROP FUNCTION IF EXISTS prevent_frozen_production_source_item_mutation();
DROP TRIGGER IF EXISTS trg_production_source_sets_prevent_reopen ON production_source_sets;
DROP FUNCTION IF EXISTS prevent_frozen_production_source_set_reopen();

ALTER TABLE IF EXISTS production_documents DROP CONSTRAINT IF EXISTS fk_production_documents_latest_approved_version;
ALTER TABLE IF EXISTS production_documents DROP CONSTRAINT IF EXISTS fk_production_documents_current_version;

DROP TABLE IF EXISTS production_block_lineage;
DROP TABLE IF EXISTS production_document_blocks;
DROP TABLE IF EXISTS production_document_versions;
DROP TABLE IF EXISTS production_documents;
DROP TABLE IF EXISTS production_evidence_snapshots;
DROP TABLE IF EXISTS production_source_items;
DROP TABLE IF EXISTS production_source_sets;
