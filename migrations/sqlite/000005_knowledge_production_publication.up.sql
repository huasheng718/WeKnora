CREATE UNIQUE INDEX IF NOT EXISTS uq_knowledge_bases_id_tenant
    ON knowledge_bases (id, tenant_id);

CREATE TABLE IF NOT EXISTS production_releases (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    review_request_id VARCHAR(36) NOT NULL,
    release_digest VARCHAR(64) NOT NULL,
    supersedes_release_id VARCHAR(36) NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'building',
    retention_days INTEGER NOT NULL DEFAULT 30,
    created_by VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_releases_digest CHECK (length(release_digest) = 64 AND release_digest NOT GLOB '*[^0-9a-f]*'),
    CONSTRAINT chk_production_releases_status CHECK (status IN ('building', 'ready', 'active', 'failed', 'rolled_back', 'cleanup_pending', 'cleaned')),
    CONSTRAINT chk_production_releases_retention CHECK (retention_days >= 1 AND retention_days <= 3650),
    UNIQUE(id, tenant_id, project_id, document_id, version_id),
    UNIQUE(id, tenant_id, project_id, document_id, version_id, release_digest),
    FOREIGN KEY (document_id, tenant_id, project_id)
        REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (version_id, document_id, tenant_id, project_id)
        REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (review_request_id, tenant_id, project_id, document_id, version_id)
        REFERENCES production_review_requests(id, tenant_id, project_id, document_id, version_id) ON DELETE RESTRICT,
    FOREIGN KEY (supersedes_release_id, tenant_id, project_id, document_id, version_id)
        REFERENCES production_releases(id, tenant_id, project_id, document_id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_releases_document_status
    ON production_releases (tenant_id, document_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_releases_version_root
    ON production_releases (tenant_id, project_id, document_id, version_id)
    WHERE supersedes_release_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_releases_successor
    ON production_releases (supersedes_release_id)
    WHERE supersedes_release_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS production_release_targets (
    id VARCHAR(36) PRIMARY KEY,
    release_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL UNIQUE,
    release_digest VARCHAR(64) NOT NULL,
    config_snapshot TEXT NOT NULL DEFAULT '{}',
    config_digest VARCHAR(64) NOT NULL DEFAULT '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    status VARCHAR(20) NOT NULL DEFAULT 'building',
    retention_days INTEGER NOT NULL DEFAULT 30,
    retention_until DATETIME NULL,
    activated_at DATETIME NULL,
    failed_at DATETIME NULL,
    rolled_back_at DATETIME NULL,
    cleanup_requested_at DATETIME NULL,
    cleaned_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_release_targets_digest CHECK (length(release_digest) = 64 AND release_digest NOT GLOB '*[^0-9a-f]*'),
    CONSTRAINT chk_production_release_targets_config_snapshot CHECK (
        json_valid(config_snapshot) AND json_type(config_snapshot) = 'object' AND
        json(config_snapshot) = config_snapshot AND length(CAST(config_snapshot AS BLOB)) <= 262144
    ),
    CONSTRAINT chk_production_release_targets_config_digest CHECK (
        length(config_digest) = 64 AND config_digest NOT GLOB '*[^0-9a-f]*'
    ),
    CONSTRAINT chk_production_release_targets_status CHECK (status IN ('building', 'ready', 'active', 'failed', 'rolled_back', 'cleanup_pending', 'cleaned')),
    CONSTRAINT chk_production_release_targets_retention CHECK (retention_days >= 1 AND retention_days <= 3650),
    UNIQUE(release_id, target_knowledge_base_id),
    UNIQUE(id, tenant_id, document_id, target_knowledge_base_id),
    FOREIGN KEY (release_id, tenant_id, project_id, document_id, version_id, release_digest)
        REFERENCES production_releases(id, tenant_id, project_id, document_id, version_id, release_digest) ON DELETE RESTRICT,
    FOREIGN KEY (target_knowledge_base_id, tenant_id)
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
    tenant_id INTEGER NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    target_knowledge_base_id VARCHAR(36) NOT NULL,
    active_release_target_id VARCHAR(36) NOT NULL,
    lock_version INTEGER NOT NULL DEFAULT 1,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, document_id, target_knowledge_base_id),
    CONSTRAINT chk_production_projection_heads_lock_version CHECK (lock_version >= 1),
    FOREIGN KEY (active_release_target_id, tenant_id, document_id, target_knowledge_base_id)
        REFERENCES production_release_targets(id, tenant_id, document_id, target_knowledge_base_id) ON DELETE RESTRICT
);

CREATE TRIGGER IF NOT EXISTS trg_production_releases_validate_approved_review
    BEFORE INSERT ON production_releases
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.status <> 'building'
        THEN RAISE(ABORT, 'production releases must be created building') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.review_request_id AND tenant_id = NEW.tenant_id
          AND project_id = NEW.project_id AND document_id = NEW.document_id
          AND version_id = NEW.version_id AND status = 'approved'
    ) THEN RAISE(ABORT, 'production releases require an approved review') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_releases_guard_identity
    BEFORE UPDATE ON production_releases
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id OR
         NEW.project_id IS NOT OLD.project_id OR NEW.document_id IS NOT OLD.document_id OR
         NEW.version_id IS NOT OLD.version_id OR NEW.review_request_id IS NOT OLD.review_request_id OR
         NEW.release_digest IS NOT OLD.release_digest OR NEW.supersedes_release_id IS NOT OLD.supersedes_release_id OR
         NEW.retention_days IS NOT OLD.retention_days OR
         NEW.created_by IS NOT OLD.created_by OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'production release identity is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_releases_guard_status
    BEFORE UPDATE OF status ON production_releases
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN
        (OLD.status = 'building' AND NEW.status NOT IN ('building', 'ready', 'failed', 'rolled_back')) OR
        (OLD.status = 'ready' AND NEW.status NOT IN ('ready', 'active', 'failed', 'rolled_back')) OR
        (OLD.status = 'active' AND NEW.status NOT IN ('active', 'failed', 'rolled_back')) OR
        (OLD.status = 'failed' AND NEW.status NOT IN ('failed', 'building', 'rolled_back', 'cleanup_pending')) OR
        (OLD.status = 'rolled_back' AND NEW.status NOT IN ('rolled_back', 'building', 'cleanup_pending')) OR
        (OLD.status = 'cleanup_pending' AND NEW.status NOT IN ('cleanup_pending', 'cleaned')) OR
        (OLD.status = 'cleaned' AND NEW.status <> 'cleaned')
        THEN RAISE(ABORT, 'invalid production release status transition') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_releases_prevent_delete
    BEFORE DELETE ON production_releases
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'production releases are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_releases_prevent_replace
    BEFORE INSERT ON production_releases
    FOR EACH ROW
    WHEN EXISTS (SELECT 1 FROM production_releases WHERE id = NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'production releases cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_release_targets_validate_initial_state
    BEFORE INSERT ON production_release_targets
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.status <> 'building' OR NEW.activated_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR
                          NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL
        THEN RAISE(ABORT, 'production release targets must be created building') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_release_targets_guard
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
    SELECT CASE WHEN NEW.status = 'active' AND (NEW.activated_at IS NULL OR NEW.failed_at IS NOT NULL OR NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL)
        THEN RAISE(ABORT, 'active production release targets cannot be cleaned') END;
    SELECT CASE WHEN NEW.status = 'active' AND NOT EXISTS (
        SELECT 1 FROM production_projection_heads
        WHERE tenant_id = NEW.tenant_id AND document_id = NEW.document_id
          AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND active_release_target_id = NEW.id
    ) THEN RAISE(ABORT, 'active production release targets require projection head activation') END;
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
    SELECT CASE WHEN OLD.status = 'active' AND NEW.status <> 'active' AND EXISTS (
        SELECT 1 FROM production_projection_heads
        WHERE tenant_id = OLD.tenant_id AND document_id = OLD.document_id
          AND target_knowledge_base_id = OLD.target_knowledge_base_id
          AND active_release_target_id = OLD.id
    ) THEN RAISE(ABORT, 'active projection heads prevent independent target deactivation') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_release_targets_prevent_delete
    BEFORE DELETE ON production_release_targets
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'production release targets are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_release_targets_prevent_replace
    BEFORE INSERT ON production_release_targets
    FOR EACH ROW
    WHEN EXISTS (SELECT 1 FROM production_release_targets WHERE id = NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'production release targets cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_projection_heads_validate_insert
    BEFORE INSERT ON production_projection_heads
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.lock_version <> 1
        THEN RAISE(ABORT, 'production projection heads must start with lock version one') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status IN ('ready', 'active')
    ) THEN RAISE(ABORT, 'production projection heads require ready or active targets') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_projection_heads_guard
    BEFORE UPDATE ON production_projection_heads
    FOR EACH ROW
BEGIN
    SELECT CASE WHEN NEW.tenant_id IS NOT OLD.tenant_id OR NEW.document_id IS NOT OLD.document_id OR
                          NEW.target_knowledge_base_id IS NOT OLD.target_knowledge_base_id
        THEN RAISE(ABORT, 'production projection head identity is immutable') END;
    SELECT CASE WHEN NEW.lock_version <> OLD.lock_version + 1
        THEN RAISE(ABORT, 'production projection head updates require CAS lock versions') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = OLD.active_release_target_id AND tenant_id = OLD.tenant_id
          AND document_id = OLD.document_id AND target_knowledge_base_id = OLD.target_knowledge_base_id
          AND status = 'active'
    ) THEN RAISE(ABORT, 'production projection heads must currently reference active targets') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status IN ('ready', 'active')
    ) THEN RAISE(ABORT, 'production projection heads require ready or active targets') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_projection_heads_activate_target_insert
    AFTER INSERT ON production_projection_heads
    FOR EACH ROW
BEGIN
    UPDATE production_release_targets
    SET status = 'active', activated_at = CURRENT_TIMESTAMP, failed_at = NULL,
        rolled_back_at = NULL, cleanup_requested_at = NULL, cleaned_at = NULL,
        retention_until = NULL
    WHERE id = NEW.active_release_target_id AND status = 'ready';
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status = 'active'
    ) THEN RAISE(ABORT, 'production projection heads must activate selected targets') END;
END;

CREATE TRIGGER IF NOT EXISTS trg_production_projection_heads_activate_target
    AFTER UPDATE ON production_projection_heads
    FOR EACH ROW
BEGIN
    UPDATE production_release_targets
    SET status = 'rolled_back', activated_at = NULL, failed_at = NULL,
        rolled_back_at = CURRENT_TIMESTAMP, cleanup_requested_at = NULL,
        cleaned_at = NULL,
        retention_until = datetime(CURRENT_TIMESTAMP, '+' || retention_days || ' days')
    WHERE OLD.active_release_target_id <> NEW.active_release_target_id
      AND id = OLD.active_release_target_id AND status = 'active';
    UPDATE production_release_targets
    SET status = 'active', activated_at = CURRENT_TIMESTAMP, failed_at = NULL,
        rolled_back_at = NULL, cleanup_requested_at = NULL, cleaned_at = NULL,
        retention_until = NULL
    WHERE id = NEW.active_release_target_id AND status = 'ready';
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM production_release_targets
        WHERE id = NEW.active_release_target_id AND tenant_id = NEW.tenant_id
          AND document_id = NEW.document_id AND target_knowledge_base_id = NEW.target_knowledge_base_id
          AND status = 'active'
    ) THEN RAISE(ABORT, 'production projection heads must activate selected targets') END;
END;

ALTER TABLE production_releases
    ADD COLUMN release_digest_version INTEGER NOT NULL DEFAULT 0 CHECK (release_digest_version >= 0);

ALTER TABLE production_release_targets
    ADD COLUMN failure_code VARCHAR(64) NOT NULL DEFAULT ''
        CHECK (length(failure_code) <= 64 AND failure_code NOT GLOB '*[^A-Z0-9_]*');
ALTER TABLE production_release_targets
    ADD COLUMN failure_reason VARCHAR(256) NOT NULL DEFAULT '' CHECK (length(failure_reason) <= 256);
ALTER TABLE production_release_targets
    ADD COLUMN recovery_attempted_at DATETIME NULL;

CREATE INDEX IF NOT EXISTS idx_production_release_targets_failure_recovery
    ON production_release_targets (tenant_id, status, recovery_attempted_at, id);

DROP TRIGGER IF EXISTS trg_production_releases_guard_identity;
CREATE TRIGGER trg_production_releases_guard_identity
    BEFORE UPDATE ON production_releases
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id OR
         NEW.project_id IS NOT OLD.project_id OR NEW.document_id IS NOT OLD.document_id OR
         NEW.version_id IS NOT OLD.version_id OR NEW.review_request_id IS NOT OLD.review_request_id OR
         NEW.release_digest IS NOT OLD.release_digest OR NEW.release_digest_version IS NOT OLD.release_digest_version OR
         NEW.supersedes_release_id IS NOT OLD.supersedes_release_id OR
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
                          NEW.rolled_back_at IS NOT NULL OR NEW.cleanup_requested_at IS NOT NULL OR
                          NEW.cleaned_at IS NOT NULL OR NEW.retention_until IS NOT NULL OR NEW.recovery_attempted_at IS NOT NULL
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
    SELECT CASE WHEN OLD.status IN ('failed', 'rolled_back', 'cleanup_pending', 'cleaned') AND
                          NEW.status = OLD.status AND
                          NEW.retention_until IS NOT OLD.retention_until
        THEN RAISE(ABORT, 'production release target retention_until is immutable after eligibility') END;
    SELECT CASE WHEN ((OLD.status IN ('failed', 'rolled_back') AND NEW.status = 'cleanup_pending') OR
                           (OLD.status = 'cleanup_pending' AND NEW.status = 'cleaned')) AND
                          NEW.retention_until IS NOT OLD.retention_until
        THEN RAISE(ABORT, 'production release target retention_until is immutable after eligibility') END;
    SELECT CASE WHEN OLD.status IN ('cleanup_pending', 'cleaned') AND
                          NEW.cleanup_requested_at IS NOT OLD.cleanup_requested_at
        THEN RAISE(ABORT, 'production release target cleanup_requested_at is immutable after cleanup entry') END;
    SELECT CASE WHEN OLD.status = 'cleaned' AND NEW.cleaned_at IS NOT OLD.cleaned_at
        THEN RAISE(ABORT, 'production release target cleaned_at is immutable after completion') END;
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

CREATE TRIGGER trg_production_release_targets_guard_recovery_attempt
    BEFORE UPDATE OF recovery_attempted_at ON production_release_targets
    FOR EACH ROW
    WHEN NEW.recovery_attempted_at IS NOT OLD.recovery_attempted_at AND NOT (
         (OLD.status = 'building' AND NEW.status = 'building' AND NEW.recovery_attempted_at IS NOT NULL AND
          (OLD.recovery_attempted_at IS NULL OR NEW.recovery_attempted_at >= OLD.recovery_attempted_at)) OR
         (OLD.status IN ('building', 'failed', 'rolled_back') AND NEW.status = 'building' AND
          NEW.recovery_attempted_at IS NULL AND NEW.updated_at > OLD.updated_at)
    )
BEGIN
    SELECT RAISE(ABORT, 'production release target recovery attempts are monotonic and building-only');
END;
