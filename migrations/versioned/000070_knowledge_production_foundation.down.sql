DROP TRIGGER IF EXISTS trg_production_document_types_prevent_active_definition_update ON production_document_types;
DROP FUNCTION IF EXISTS prevent_active_production_document_type_definition_update();

DROP TABLE IF EXISTS production_idempotency_keys;
DROP TABLE IF EXISTS production_document_types;
DROP TABLE IF EXISTS production_project_members;
DROP TABLE IF EXISTS production_projects;
