DELETE FROM production_document_types
WHERE origin = 'builtin'
  AND schema_version = 1
  AND template_key IN ('sop', 'policy_process', 'product_service_guide', 'faq', 'incident_playbook');

DROP TRIGGER IF EXISTS trg_production_document_types_prevent_active_definition_update ON production_document_types;
DROP FUNCTION IF EXISTS prevent_active_production_document_type_definition_update();

CREATE OR REPLACE FUNCTION prevent_active_production_document_type_definition_update()
RETURNS TRIGGER AS $$
BEGIN
    IF (OLD.status = 'active' AND NEW.status NOT IN ('active', 'retired')) OR
        (OLD.status = 'retired' AND NEW.status <> 'retired') THEN
        RAISE EXCEPTION 'invalid production document type status transition';
    END IF;
    IF OLD.status IN ('active', 'retired') AND (
        NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
        NEW.code IS DISTINCT FROM OLD.code OR
        NEW.name IS DISTINCT FROM OLD.name OR
        NEW.description IS DISTINCT FROM OLD.description OR
        NEW.schema_version IS DISTINCT FROM OLD.schema_version OR
        NEW.block_schema IS DISTINCT FROM OLD.block_schema OR
        NEW.source_requirements IS DISTINCT FROM OLD.source_requirements OR
        NEW.skill_bindings IS DISTINCT FROM OLD.skill_bindings OR
        NEW.workflow_plan IS DISTINCT FROM OLD.workflow_plan OR
        NEW.quality_rules IS DISTINCT FROM OLD.quality_rules OR
        NEW.review_policy IS DISTINCT FROM OLD.review_policy OR
        NEW.publication_policy IS DISTINCT FROM OLD.publication_policy OR
        NEW.created_by IS DISTINCT FROM OLD.created_by
    ) THEN
        RAISE EXCEPTION 'active production document type definitions are immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_document_types_prevent_active_definition_update
    BEFORE UPDATE ON production_document_types
    FOR EACH ROW
    EXECUTE FUNCTION prevent_active_production_document_type_definition_update();

DROP INDEX IF EXISTS uq_production_document_types_live_template_version;

ALTER TABLE production_document_types
    DROP CONSTRAINT IF EXISTS chk_production_document_types_builtin_template_key,
    DROP CONSTRAINT IF EXISTS chk_production_document_types_origin,
    DROP COLUMN template_key,
    DROP COLUMN origin;
