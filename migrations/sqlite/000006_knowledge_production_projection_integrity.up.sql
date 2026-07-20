ALTER TABLE production_releases
    ADD COLUMN release_digest_version INTEGER NOT NULL DEFAULT 0 CHECK (release_digest_version >= 0);

ALTER TABLE production_release_targets
    ADD COLUMN failure_code VARCHAR(64) NOT NULL DEFAULT ''
        CHECK (length(failure_code) <= 64 AND failure_code NOT GLOB '*[^A-Z0-9_]*');
ALTER TABLE production_release_targets
    ADD COLUMN failure_reason VARCHAR(256) NOT NULL DEFAULT '' CHECK (length(failure_reason) <= 256);

UPDATE production_release_targets
SET failure_code = 'LEGACY_PROJECTION_FAILURE',
    failure_reason = 'legacy failed target migrated without recorded failure details'
WHERE status = 'failed';

DROP TRIGGER IF EXISTS trg_production_releases_guard_identity;
CREATE TRIGGER trg_production_releases_guard_identity
    BEFORE UPDATE ON production_releases
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id OR
         NEW.project_id IS NOT OLD.project_id OR NEW.document_id IS NOT OLD.document_id OR
         NEW.version_id IS NOT OLD.version_id OR NEW.review_request_id IS NOT OLD.review_request_id OR
         NEW.release_digest IS NOT OLD.release_digest OR NEW.release_digest_version IS NOT OLD.release_digest_version OR
         NEW.retention_days IS NOT OLD.retention_days OR NEW.created_by IS NOT OLD.created_by OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'production release identity is immutable');
END;

DROP TRIGGER IF EXISTS trg_production_release_targets_validate_initial_state;
CREATE TRIGGER trg_production_release_targets_validate_initial_state
    BEFORE INSERT ON production_release_targets
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.status <> 'building' OR NEW.failure_code <> '' OR NEW.failure_reason <> '' OR
                          NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR
                          NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL
        THEN RAISE(ABORT, 'production release targets must be created building') END;
END;

DROP TRIGGER IF EXISTS trg_production_release_targets_guard;
CREATE TRIGGER trg_production_release_targets_guard
    BEFORE UPDATE ON production_release_targets
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.release_id IS NOT OLD.release_id OR
                          NEW.tenant_id IS NOT OLD.tenant_id OR NEW.project_id IS NOT OLD.project_id OR
                          NEW.document_id IS NOT OLD.document_id OR NEW.version_id IS NOT OLD.version_id OR
                          NEW.target_knowledge_base_id IS NOT OLD.target_knowledge_base_id OR
                          NEW.knowledge_id IS NOT OLD.knowledge_id OR NEW.release_digest IS NOT OLD.release_digest OR
                          NEW.config_snapshot IS NOT OLD.config_snapshot OR NEW.config_digest IS NOT OLD.config_digest OR
                          NEW.retention_days IS NOT OLD.retention_days OR NEW.created_at IS NOT OLD.created_at
        THEN RAISE(ABORT, 'production release target identity is immutable') END;
    SELECT CASE WHEN (NEW.failure_code IS NOT OLD.failure_code OR NEW.failure_reason IS NOT OLD.failure_reason) AND
                          NOT ((NEW.status = 'failed' AND OLD.status <> 'failed') OR
                               (NEW.status = 'building' AND OLD.status IN ('failed', 'rolled_back') AND NEW.failure_code = '' AND NEW.failure_reason = ''))
        THEN RAISE(ABORT, 'production release target failure metadata is lifecycle-owned') END;
    SELECT CASE WHEN
        (OLD.status = 'building' AND NEW.status NOT IN ('building', 'ready', 'failed', 'rolled_back')) OR
        (OLD.status = 'ready' AND NEW.status NOT IN ('ready', 'active', 'failed', 'rolled_back')) OR
        (OLD.status = 'active' AND NEW.status NOT IN ('active', 'rolled_back')) OR
        (OLD.status = 'failed' AND NEW.status NOT IN ('failed', 'building', 'rolled_back', 'cleanup_pending')) OR
        (OLD.status = 'rolled_back' AND NEW.status NOT IN ('rolled_back', 'building', 'cleanup_pending')) OR
        (OLD.status = 'cleanup_pending' AND NEW.status NOT IN ('cleanup_pending', 'cleaned')) OR
        (OLD.status = 'cleaned' AND NEW.status <> 'cleaned')
        THEN RAISE(ABORT, 'invalid production release target status transition') END;
    SELECT CASE WHEN NEW.status IN ('building', 'ready') AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL)
        THEN RAISE(ABORT, 'building and ready production release targets must not retain lifecycle timestamps') END;
    SELECT CASE WHEN NEW.status = 'failed' AND (NEW.failure_code = '' OR NEW.failure_reason = '')
        THEN RAISE(ABORT, 'failed production release targets require failure metadata') END;
    SELECT CASE WHEN NEW.status IN ('building', 'ready', 'active') AND (NEW.failure_code <> '' OR NEW.failure_reason <> '')
        THEN RAISE(ABORT, 'non-failed production release targets must not retain failure metadata') END;
    SELECT CASE WHEN NEW.status = 'active' AND (NEW.activated_at IS NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL)
        THEN RAISE(ABORT, 'active production release targets cannot be cleaned') END;
    SELECT CASE WHEN NEW.status = 'active' AND NOT EXISTS (SELECT 1 FROM production_projection_heads WHERE tenant_id = NEW.tenant_id AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id AND active_release_target_id = NEW.id)
        THEN RAISE(ABORT, 'active production release targets require projection head activation') END;
    SELECT CASE WHEN NEW.status = 'failed' AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT datetime(NEW.failed_at, '+' || NEW.retention_days || ' days'))
        THEN RAISE(ABORT, 'failed production release targets require retention timestamps') END;
    SELECT CASE WHEN NEW.status = 'rolled_back' AND (NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT datetime(NEW.rolled_back_at, '+' || NEW.retention_days || ' days'))
        THEN RAISE(ABORT, 'rolled back production release targets require retention timestamps') END;
    SELECT CASE WHEN NEW.status = 'cleanup_pending' AND (NEW.activated_at IS NOT NULL OR NEW.cleanup_requested_at IS NULL OR NEW.cleaned_at IS NOT NULL OR ((NEW.failed_at IS NULL) = (NEW.rolled_back_at IS NULL)))
        THEN RAISE(ABORT, 'cleanup pending production release targets require timestamps') END;
    SELECT CASE WHEN NEW.status = 'cleaned' AND (NEW.activated_at IS NOT NULL OR NEW.cleanup_requested_at IS NULL OR NEW.cleaned_at IS NULL OR ((NEW.failed_at IS NULL) = (NEW.rolled_back_at IS NULL)))
        THEN RAISE(ABORT, 'cleaned production release targets require timestamps') END;
    SELECT CASE WHEN NEW.status IN ('cleanup_pending', 'cleaned') AND (NEW.retention_until IS NULL OR NEW.retention_until > CURRENT_TIMESTAMP)
        THEN RAISE(ABORT, 'production release target retention has not expired') END;
    SELECT CASE WHEN OLD.status = 'active' AND NEW.status <> 'active' AND EXISTS (SELECT 1 FROM production_projection_heads WHERE tenant_id = OLD.tenant_id AND document_id = OLD.document_id AND target_knowledge_base_id = OLD.target_knowledge_base_id AND active_release_target_id = OLD.id)
        THEN RAISE(ABORT, 'active projection heads prevent independent target deactivation') END;
END;
