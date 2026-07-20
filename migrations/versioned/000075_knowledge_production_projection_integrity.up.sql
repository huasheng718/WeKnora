ALTER TABLE production_releases
    ADD COLUMN release_digest_version INTEGER NOT NULL DEFAULT 0;

ALTER TABLE production_releases
    ADD CONSTRAINT chk_production_releases_digest_version CHECK (release_digest_version >= 0);

ALTER TABLE production_release_targets
    ADD COLUMN failure_code VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN failure_reason VARCHAR(256) NOT NULL DEFAULT '';

UPDATE production_release_targets
SET failure_code = 'LEGACY_PROJECTION_FAILURE',
    failure_reason = 'legacy failed target migrated without recorded failure details'
WHERE status = 'failed';

ALTER TABLE production_release_targets
    ADD CONSTRAINT chk_production_release_targets_failure CHECK (
        failure_code ~ '^[A-Z0-9_]*$' AND
        ((status = 'failed' AND failure_code <> '' AND failure_reason <> '') OR status <> 'failed')
    );

CREATE OR REPLACE FUNCTION guard_production_release_identity()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.review_request_id IS DISTINCT FROM OLD.review_request_id OR
       NEW.release_digest IS DISTINCT FROM OLD.release_digest OR
       NEW.release_digest_version IS DISTINCT FROM OLD.release_digest_version OR
       NEW.retention_days IS DISTINCT FROM OLD.retention_days OR
       NEW.created_by IS DISTINCT FROM OLD.created_by OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production release identity is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION validate_production_release_target_initial_state()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status <> 'building' OR NEW.failure_code <> '' OR NEW.failure_reason <> '' OR
       NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR
       NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR
       NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL THEN
        RAISE EXCEPTION 'production release targets must be created building';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION guard_production_release_target()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.release_id IS DISTINCT FROM OLD.release_id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.target_knowledge_base_id IS DISTINCT FROM OLD.target_knowledge_base_id OR
       NEW.knowledge_id IS DISTINCT FROM OLD.knowledge_id OR NEW.release_digest IS DISTINCT FROM OLD.release_digest OR
       NEW.config_snapshot IS DISTINCT FROM OLD.config_snapshot OR NEW.config_digest IS DISTINCT FROM OLD.config_digest OR
       NEW.retention_days IS DISTINCT FROM OLD.retention_days OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production release target identity is immutable';
    END IF;
    IF (NEW.failure_code IS DISTINCT FROM OLD.failure_code OR NEW.failure_reason IS DISTINCT FROM OLD.failure_reason) AND
       NOT ((NEW.status = 'failed' AND OLD.status <> 'failed') OR
            (NEW.status = 'building' AND OLD.status IN ('failed', 'rolled_back') AND NEW.failure_code = '' AND NEW.failure_reason = '')) THEN
        RAISE EXCEPTION 'production release target failure metadata is lifecycle-owned';
    END IF;
    IF (OLD.status = 'building' AND NEW.status NOT IN ('building', 'ready', 'failed', 'rolled_back')) OR
       (OLD.status = 'ready' AND NEW.status NOT IN ('ready', 'active', 'failed', 'rolled_back')) OR
       (OLD.status = 'active' AND NEW.status NOT IN ('active', 'rolled_back')) OR
       (OLD.status = 'failed' AND NEW.status NOT IN ('failed', 'building', 'rolled_back', 'cleanup_pending')) OR
       (OLD.status = 'rolled_back' AND NEW.status NOT IN ('rolled_back', 'building', 'cleanup_pending')) OR
       (OLD.status = 'cleanup_pending' AND NEW.status NOT IN ('cleanup_pending', 'cleaned')) OR
       (OLD.status = 'cleaned' AND NEW.status <> 'cleaned') THEN
        RAISE EXCEPTION 'invalid production release target status transition';
    END IF;
    IF NEW.status IN ('building', 'ready') AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL) THEN
        RAISE EXCEPTION 'building and ready production release targets must not retain lifecycle timestamps';
    END IF;
    IF NEW.status IN ('building', 'ready', 'active') AND (NEW.failure_code <> '' OR NEW.failure_reason <> '') THEN
        RAISE EXCEPTION 'non-failed production release targets must not retain failure metadata';
    END IF;
    IF NEW.status = 'active' AND (NEW.activated_at IS NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL) THEN
        RAISE EXCEPTION 'active production release targets cannot be cleaned';
    END IF;
    IF NEW.status = 'active' AND NOT EXISTS (
        SELECT 1 FROM production_projection_heads
        WHERE tenant_id = NEW.tenant_id AND document_id = NEW.document_id
          AND target_knowledge_base_id = NEW.target_knowledge_base_id AND active_release_target_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'active production release targets require projection head activation';
    END IF;
    IF NEW.status = 'failed' AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS DISTINCT FROM NEW.failed_at + (NEW.retention_days * INTERVAL '1 day')) THEN
        RAISE EXCEPTION 'failed production release targets require retention timestamps';
    END IF;
    IF NEW.status = 'rolled_back' AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS DISTINCT FROM NEW.rolled_back_at + (NEW.retention_days * INTERVAL '1 day')) THEN
        RAISE EXCEPTION 'rolled back production release targets require retention timestamps';
    END IF;
    IF NEW.status = 'cleanup_pending' AND (NEW.activated_at IS NOT NULL OR NEW.cleanup_requested_at IS NULL OR NEW.cleaned_at IS NOT NULL OR ((NEW.failed_at IS NULL) = (NEW.rolled_back_at IS NULL))) THEN
        RAISE EXCEPTION 'cleanup pending production release targets require timestamps';
    END IF;
    IF NEW.status = 'cleaned' AND (NEW.activated_at IS NOT NULL OR NEW.cleanup_requested_at IS NULL OR NEW.cleaned_at IS NULL OR ((NEW.failed_at IS NULL) = (NEW.rolled_back_at IS NULL))) THEN
        RAISE EXCEPTION 'cleaned production release targets require timestamps';
    END IF;
    IF NEW.status IN ('cleanup_pending', 'cleaned') AND (NEW.retention_until IS NULL OR NEW.retention_until > CURRENT_TIMESTAMP) THEN
        RAISE EXCEPTION 'production release target retention has not expired';
    END IF;
    IF OLD.status = 'active' AND NEW.status <> 'active' AND EXISTS (
        SELECT 1 FROM production_projection_heads
        WHERE tenant_id = OLD.tenant_id AND document_id = OLD.document_id
          AND target_knowledge_base_id = OLD.target_knowledge_base_id AND active_release_target_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'active projection heads prevent independent target deactivation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
