CREATE UNIQUE INDEX IF NOT EXISTS uq_knowledge_bases_id_tenant
    ON knowledge_bases (id, tenant_id);

CREATE TABLE IF NOT EXISTS production_releases (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    review_request_id VARCHAR(36) NOT NULL,
    release_digest VARCHAR(64) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'building',
    retention_days INTEGER NOT NULL DEFAULT 30,
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_releases_digest CHECK (release_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_production_releases_status CHECK (status IN ('building', 'ready', 'active', 'failed', 'rolled_back', 'cleanup_pending', 'cleaned')),
    CONSTRAINT chk_production_releases_retention CHECK (retention_days >= 1 AND retention_days <= 3650),
    UNIQUE(id, tenant_id, project_id, document_id, version_id),
    UNIQUE(id, tenant_id, project_id, document_id, version_id, release_digest),
    UNIQUE(tenant_id, project_id, document_id, version_id),
    CONSTRAINT fk_production_releases_document_context FOREIGN KEY (document_id, tenant_id, project_id)
        REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_releases_version_context FOREIGN KEY (version_id, document_id, tenant_id, project_id)
        REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_releases_review_context FOREIGN KEY (review_request_id, tenant_id, project_id, document_id, version_id)
        REFERENCES production_review_requests(id, tenant_id, project_id, document_id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_releases_document_status
    ON production_releases (tenant_id, document_id, status);

CREATE TABLE IF NOT EXISTS production_release_targets (
    id VARCHAR(36) PRIMARY KEY,
    release_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL UNIQUE,
    release_digest VARCHAR(64) NOT NULL,
    config_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    config_digest VARCHAR(64) NOT NULL DEFAULT '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    status VARCHAR(20) NOT NULL DEFAULT 'building',
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    failure_reason VARCHAR(256) NOT NULL DEFAULT '',
    retention_days INTEGER NOT NULL DEFAULT 30,
    retention_until TIMESTAMP NULL,
    activated_at TIMESTAMP NULL,
    failed_at TIMESTAMP NULL,
    rolled_back_at TIMESTAMP NULL,
    cleanup_requested_at TIMESTAMP NULL,
    cleaned_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_release_targets_digest CHECK (release_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_production_release_targets_config_snapshot CHECK (
        jsonb_typeof(config_snapshot) = 'object' AND octet_length(config_snapshot::text) <= 262144
    ),
    CONSTRAINT chk_production_release_targets_config_digest CHECK (config_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_production_release_targets_status CHECK (status IN ('building', 'ready', 'active', 'failed', 'rolled_back', 'cleanup_pending', 'cleaned')),
    CONSTRAINT chk_production_release_targets_failure CHECK (
        failure_code ~ '^[A-Z0-9_]*$' AND
        ((status = 'failed' AND failure_code <> '' AND failure_reason <> '') OR status <> 'failed')
    ),
    CONSTRAINT chk_production_release_targets_retention CHECK (retention_days >= 1 AND retention_days <= 3650),
    UNIQUE(release_id, target_knowledge_base_id),
    UNIQUE(id, tenant_id, document_id, target_knowledge_base_id),
    CONSTRAINT fk_production_release_targets_release_context FOREIGN KEY (release_id, tenant_id, project_id, document_id, version_id, release_digest)
        REFERENCES production_releases(id, tenant_id, project_id, document_id, version_id, release_digest) ON DELETE RESTRICT,
    CONSTRAINT fk_production_release_targets_knowledge_base_context FOREIGN KEY (target_knowledge_base_id, tenant_id)
        REFERENCES knowledge_bases(id, tenant_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_release_targets_document_status
    ON production_release_targets (tenant_id, document_id, target_knowledge_base_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_release_targets_active_projection
    ON production_release_targets (tenant_id, document_id, target_knowledge_base_id)
    WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_production_release_targets_scope_status
    ON production_release_targets (tenant_id, target_knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS idx_production_release_targets_cleanup_eligibility
    ON production_release_targets (status, retention_until);

CREATE TABLE IF NOT EXISTS production_projection_heads (
    tenant_id BIGINT NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL,
    active_release_target_id VARCHAR(36) NOT NULL,
    lock_version INTEGER NOT NULL DEFAULT 1,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, document_id, target_knowledge_base_id),
    CONSTRAINT chk_production_projection_heads_lock_version CHECK (lock_version >= 1),
    CONSTRAINT fk_production_projection_heads_target FOREIGN KEY (active_release_target_id, tenant_id, document_id, target_knowledge_base_id)
        REFERENCES production_release_targets(id, tenant_id, document_id, target_knowledge_base_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_production_release_approved_review()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status <> 'building' THEN
        RAISE EXCEPTION 'production releases must be created building';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.review_request_id AND tenant_id = NEW.tenant_id
          AND project_id = NEW.project_id AND document_id = NEW.document_id
          AND version_id = NEW.version_id AND status = 'approved'
    ) THEN
        RAISE EXCEPTION 'production releases require an approved review';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_releases_validate_approved_review
    BEFORE INSERT ON production_releases
    FOR EACH ROW EXECUTE FUNCTION validate_production_release_approved_review();

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
       NEW.retention_days IS DISTINCT FROM OLD.retention_days OR
       NEW.created_by IS DISTINCT FROM OLD.created_by OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production release identity is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_releases_guard_identity
    BEFORE UPDATE ON production_releases
    FOR EACH ROW EXECUTE FUNCTION guard_production_release_identity();

CREATE OR REPLACE FUNCTION guard_production_release_status()
RETURNS TRIGGER AS $$
BEGIN
    IF (OLD.status = 'building' AND NEW.status NOT IN ('building', 'ready', 'failed', 'rolled_back')) OR
       (OLD.status = 'ready' AND NEW.status NOT IN ('ready', 'active', 'failed', 'rolled_back')) OR
       (OLD.status = 'active' AND NEW.status NOT IN ('active', 'failed', 'rolled_back')) OR
       (OLD.status = 'failed' AND NEW.status NOT IN ('failed', 'building', 'rolled_back', 'cleanup_pending')) OR
       (OLD.status = 'rolled_back' AND NEW.status NOT IN ('rolled_back', 'building', 'cleanup_pending')) OR
       (OLD.status = 'cleanup_pending' AND NEW.status NOT IN ('cleanup_pending', 'cleaned')) OR
       (OLD.status = 'cleaned' AND NEW.status <> 'cleaned') THEN
        RAISE EXCEPTION 'invalid production release status transition';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_releases_guard_status
    BEFORE UPDATE OF status ON production_releases
    FOR EACH ROW EXECUTE FUNCTION guard_production_release_status();

CREATE OR REPLACE FUNCTION prevent_production_release_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'production releases are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_releases_prevent_delete
    BEFORE DELETE ON production_releases
    FOR EACH ROW EXECUTE FUNCTION prevent_production_release_delete();

CREATE OR REPLACE FUNCTION prevent_production_release_replace()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM production_releases WHERE id = NEW.id) THEN
        RAISE EXCEPTION 'production releases cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_releases_prevent_replace
    BEFORE INSERT ON production_releases
    FOR EACH ROW EXECUTE FUNCTION prevent_production_release_replace();

CREATE OR REPLACE FUNCTION validate_production_release_target_initial_state()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status <> 'building' OR NEW.failure_code <> '' OR NEW.failure_reason <> '' OR
       NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR
       NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL THEN
        RAISE EXCEPTION 'production release targets must be created building';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_release_targets_validate_initial_state
    BEFORE INSERT ON production_release_targets
    FOR EACH ROW EXECUTE FUNCTION validate_production_release_target_initial_state();

CREATE OR REPLACE FUNCTION guard_production_release_target()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.release_id IS DISTINCT FROM OLD.release_id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.target_knowledge_base_id IS DISTINCT FROM OLD.target_knowledge_base_id OR
       NEW.knowledge_id IS DISTINCT FROM OLD.knowledge_id OR
       NEW.release_digest IS DISTINCT FROM OLD.release_digest OR
       NEW.config_snapshot IS DISTINCT FROM OLD.config_snapshot OR
       NEW.config_digest IS DISTINCT FROM OLD.config_digest OR
       NEW.retention_days IS DISTINCT FROM OLD.retention_days OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
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
          AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND active_release_target_id = NEW.id
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
          AND target_knowledge_base_id = OLD.target_knowledge_base_id
          AND active_release_target_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'active projection heads prevent independent target deactivation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_release_targets_guard
    BEFORE UPDATE ON production_release_targets
    FOR EACH ROW EXECUTE FUNCTION guard_production_release_target();

CREATE OR REPLACE FUNCTION prevent_production_release_target_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'production release targets are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_release_targets_prevent_delete
    BEFORE DELETE ON production_release_targets
    FOR EACH ROW EXECUTE FUNCTION prevent_production_release_target_delete();

CREATE OR REPLACE FUNCTION prevent_production_release_target_replace()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM production_release_targets WHERE id = NEW.id) THEN
        RAISE EXCEPTION 'production release targets cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_release_targets_prevent_replace
    BEFORE INSERT ON production_release_targets
    FOR EACH ROW EXECUTE FUNCTION prevent_production_release_target_replace();

CREATE OR REPLACE FUNCTION validate_production_projection_head_insert()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.lock_version <> 1 THEN
        RAISE EXCEPTION 'production projection heads must start with lock version one';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status IN ('ready', 'active')
    ) THEN
        RAISE EXCEPTION 'production projection heads require ready or active targets';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_projection_heads_validate_insert
    BEFORE INSERT ON production_projection_heads
    FOR EACH ROW EXECUTE FUNCTION validate_production_projection_head_insert();

CREATE OR REPLACE FUNCTION guard_production_projection_head()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.target_knowledge_base_id IS DISTINCT FROM OLD.target_knowledge_base_id THEN
        RAISE EXCEPTION 'production projection head identity is immutable';
    END IF;
    IF NEW.lock_version <> OLD.lock_version + 1 THEN
        RAISE EXCEPTION 'production projection head updates require CAS lock versions';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = OLD.active_release_target_id AND tenant_id = OLD.tenant_id
          AND document_id = OLD.document_id AND target_knowledge_base_id = OLD.target_knowledge_base_id
          AND status = 'active'
    ) THEN
        RAISE EXCEPTION 'production projection heads must currently reference active targets';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status IN ('ready', 'active')
    ) THEN
        RAISE EXCEPTION 'production projection heads require ready or active targets';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_projection_heads_guard
    BEFORE UPDATE ON production_projection_heads
    FOR EACH ROW EXECUTE FUNCTION guard_production_projection_head();

CREATE OR REPLACE FUNCTION activate_production_projection_head_target()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.active_release_target_id <> NEW.active_release_target_id THEN
        UPDATE production_release_targets
        SET status = 'rolled_back', activated_at = NULL, failed_at = NULL,
            rolled_back_at = CURRENT_TIMESTAMP, cleanup_requested_at = NULL,
            cleaned_at = NULL,
            retention_until = CURRENT_TIMESTAMP + (retention_days * INTERVAL '1 day')
        WHERE id = OLD.active_release_target_id AND status = 'active';
    END IF;

    UPDATE production_release_targets
    SET status = 'active', activated_at = CURRENT_TIMESTAMP, failed_at = NULL,
        rolled_back_at = NULL, cleanup_requested_at = NULL, cleaned_at = NULL,
        retention_until = NULL
    WHERE id = NEW.active_release_target_id AND status = 'ready';

    IF NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status = 'active'
    ) THEN
        RAISE EXCEPTION 'production projection heads must activate selected targets';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_projection_heads_activate_target
    AFTER INSERT OR UPDATE ON production_projection_heads
    FOR EACH ROW EXECUTE FUNCTION activate_production_projection_head_target();
