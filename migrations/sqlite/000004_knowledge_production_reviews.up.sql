CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_versions_review_context
    ON production_document_versions (id, document_id, tenant_id, project_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_blocks_review_anchor
    ON production_document_blocks (id, version_id);

CREATE TABLE IF NOT EXISTS production_annotations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    block_id VARCHAR(36) NOT NULL,
    annotation_type VARCHAR(20) NOT NULL,
    quality_tag VARCHAR(32) NULL,
    severity VARCHAR(16) NOT NULL DEFAULT 'info',
    anchor TEXT NOT NULL DEFAULT '{}',
    body TEXT NOT NULL,
    suggested_content TEXT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_by VARCHAR(36) NOT NULL,
    resolved_by VARCHAR(36) NULL,
    resolved_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_annotations_type CHECK (annotation_type IN ('comment', 'suggestion', 'quality_tag')),
    CONSTRAINT chk_production_annotations_quality_tag CHECK (
        (annotation_type = 'quality_tag' AND quality_tag IS NOT NULL AND quality_tag IN ('missing_evidence', 'factual_risk', 'unclear', 'incomplete', 'conflict', 'compliance_risk')) OR
        (annotation_type IN ('comment', 'suggestion') AND quality_tag IS NULL)
    ),
    CONSTRAINT chk_production_annotations_severity CHECK (severity IN ('info', 'warning', 'blocking')),
    CONSTRAINT chk_production_annotations_anchor CHECK (json_valid(anchor) AND json_type(anchor) = 'object'),
    CONSTRAINT chk_production_annotations_body CHECK (length(body) BETWEEN 1 AND 20000),
    CONSTRAINT chk_production_annotations_suggested_content CHECK (suggested_content IS NULL OR length(suggested_content) <= 20000),
    CONSTRAINT chk_production_annotations_status CHECK (status IN ('open', 'resolved', 'dismissed')),
    CONSTRAINT chk_production_annotations_resolution CHECK (
        (status = 'open' AND resolved_by IS NULL AND resolved_at IS NULL) OR
        (status IN ('resolved', 'dismissed') AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL)
    ),
    FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (version_id, document_id, tenant_id, project_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (block_id, version_id) REFERENCES production_document_blocks(id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_annotations_document_version_status
    ON production_annotations (document_id, version_id, status);
CREATE INDEX IF NOT EXISTS idx_production_annotations_blocking_count
    ON production_annotations (tenant_id, version_id, severity, status);
CREATE INDEX IF NOT EXISTS idx_production_annotations_block_status
    ON production_annotations (block_id, status);

CREATE TABLE IF NOT EXISTS production_review_requests (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    policy_snapshot TEXT NOT NULL,
    policy_digest VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    submitted_by VARCHAR(36) NOT NULL,
    submitted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    terminal_by VARCHAR(36) NULL,
    terminal_reason TEXT NULL,
    completed_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_review_requests_policy CHECK (
        json_valid(policy_snapshot) AND json_type(policy_snapshot) = 'object' AND
        policy_snapshot = json(policy_snapshot)
    ),
    CONSTRAINT chk_production_review_requests_policy_digest CHECK (length(policy_digest) = 64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
    CONSTRAINT chk_production_review_requests_status CHECK (status IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled', 'changes_requested')),
    CONSTRAINT chk_production_review_requests_terminal CHECK (
        (status = 'pending' AND terminal_by IS NULL AND terminal_reason IS NULL AND completed_at IS NULL) OR
        (status = 'approved' AND terminal_by IS NOT NULL AND completed_at IS NOT NULL) OR
        (status IN ('rejected', 'obsolete', 'cancelled', 'changes_requested') AND terminal_by IS NOT NULL AND terminal_reason IS NOT NULL AND length(terminal_reason) BETWEEN 1 AND 5000 AND completed_at IS NOT NULL)
    ),
    UNIQUE(id, tenant_id, project_id, document_id, version_id),
    FOREIGN KEY (document_id, tenant_id, project_id) REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (version_id, document_id, tenant_id, project_id) REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_review_requests_version
    ON production_review_requests (tenant_id, project_id, document_id, version_id);
CREATE INDEX IF NOT EXISTS idx_production_review_requests_document_version_status
    ON production_review_requests (document_id, version_id, status);

CREATE TABLE IF NOT EXISTS production_review_steps (
    id VARCHAR(36) PRIMARY KEY,
    review_request_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    required_role VARCHAR(32) NOT NULL,
    sequence INTEGER NOT NULL,
    reviewer_user_id VARCHAR(36) NULL,
    decision VARCHAR(24) NOT NULL DEFAULT 'pending',
    comment TEXT NOT NULL DEFAULT '',
    decided_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_review_steps_role CHECK (required_role IN ('business_reviewer', 'engineering_reviewer', 'compliance_reviewer')),
    CONSTRAINT chk_production_review_steps_sequence CHECK (sequence >= 1),
    CONSTRAINT chk_production_review_steps_decision CHECK (decision IN ('pending', 'approved', 'changes_requested', 'rejected', 'cancelled')),
    CONSTRAINT chk_production_review_steps_comment CHECK (length(comment) <= 5000),
    CONSTRAINT chk_production_review_steps_decision_actor CHECK (
        (decision = 'pending' AND reviewer_user_id IS NULL AND decided_at IS NULL AND comment = '') OR
        (decision IN ('approved', 'changes_requested', 'rejected') AND reviewer_user_id IS NOT NULL AND decided_at IS NOT NULL) OR
        (decision = 'cancelled' AND reviewer_user_id IS NULL AND decided_at IS NOT NULL AND comment = '')
    ),
    UNIQUE(review_request_id, sequence),
    UNIQUE(review_request_id, required_role),
    FOREIGN KEY (review_request_id, tenant_id, project_id, document_id, version_id)
        REFERENCES production_review_requests(id, tenant_id, project_id, document_id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_sequence
    ON production_review_steps (review_request_id, sequence);
CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_role_decision
    ON production_review_steps (review_request_id, required_role, decision);

CREATE TRIGGER IF NOT EXISTS trg_production_annotations_guard
    BEFORE UPDATE ON production_annotations
    FOR EACH ROW
    WHEN OLD.status IN ('resolved', 'dismissed')
BEGIN
    SELECT RAISE(ABORT, 'terminal production annotations are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_annotations_guard_anchor
    BEFORE UPDATE ON production_annotations
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id OR
         NEW.project_id IS NOT OLD.project_id OR NEW.document_id IS NOT OLD.document_id OR
         NEW.version_id IS NOT OLD.version_id OR NEW.block_id IS NOT OLD.block_id OR
         NEW.annotation_type IS NOT OLD.annotation_type OR NEW.quality_tag IS NOT OLD.quality_tag OR
         NEW.severity IS NOT OLD.severity OR NEW.anchor IS NOT OLD.anchor OR
         NEW.body IS NOT OLD.body OR NEW.suggested_content IS NOT OLD.suggested_content OR
         NEW.created_by IS NOT OLD.created_by OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'production annotation anchor is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_annotations_prevent_delete
    BEFORE DELETE ON production_annotations
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'production annotations are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_annotations_prevent_replace
    BEFORE INSERT ON production_annotations
    FOR EACH ROW
    WHEN EXISTS (SELECT 1 FROM production_annotations WHERE id = NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'production annotations cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_blocking_annotations
    BEFORE INSERT ON production_review_requests
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_annotations
        WHERE tenant_id = NEW.tenant_id AND project_id = NEW.project_id
          AND document_id = NEW.document_id AND version_id = NEW.version_id
          AND severity = 'blocking' AND status = 'open'
    )
BEGIN
    SELECT RAISE(ABORT, 'open blocking annotations prevent review submission');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_validate_initial_state
    BEFORE INSERT ON production_review_requests
    FOR EACH ROW
    WHEN NEW.status <> 'pending' OR NEW.terminal_by IS NOT NULL OR
         NEW.terminal_reason IS NOT NULL OR NEW.completed_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'production review requests must be submitted pending');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_guard_terminal
    BEFORE UPDATE ON production_review_requests
    FOR EACH ROW
    WHEN OLD.status <> 'pending'
BEGIN
    SELECT RAISE(ABORT, 'terminal production review requests are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_guard_identity
    BEFORE UPDATE ON production_review_requests
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id OR
         NEW.project_id IS NOT OLD.project_id OR NEW.document_id IS NOT OLD.document_id OR
         NEW.version_id IS NOT OLD.version_id OR NEW.policy_snapshot IS NOT OLD.policy_snapshot OR
         NEW.policy_digest IS NOT OLD.policy_digest OR NEW.submitted_by IS NOT OLD.submitted_by OR
         NEW.submitted_at IS NOT OLD.submitted_at OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'production review request identity is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_prevent_delete
    BEFORE DELETE ON production_review_requests
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'production review requests are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_prevent_replace
    BEFORE INSERT ON production_review_requests
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.id OR (
            tenant_id = NEW.tenant_id AND project_id = NEW.project_id
            AND document_id = NEW.document_id AND version_id = NEW.version_id
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'production review requests cannot be replaced');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_fence_terminal_children
    BEFORE UPDATE OF status ON production_review_requests
    FOR EACH ROW
    WHEN OLD.status = 'pending' AND NEW.status IN ('approved', 'rejected', 'cancelled', 'obsolete', 'changes_requested') AND (
        NOT EXISTS (SELECT 1 FROM production_review_steps WHERE review_request_id = OLD.id) OR
        (NEW.status = 'approved' AND EXISTS (SELECT 1 FROM production_review_steps WHERE review_request_id = OLD.id AND decision <> 'approved'))
    )
BEGIN
    SELECT RAISE(ABORT, 'all production review steps must approve the review');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_requests_terminalize_children
    BEFORE UPDATE OF status ON production_review_requests
    FOR EACH ROW
    WHEN OLD.status = 'pending' AND NEW.status IN ('rejected', 'cancelled', 'obsolete', 'changes_requested')
BEGIN
    UPDATE production_review_steps
    SET decision = 'cancelled', reviewer_user_id = NULL, comment = '',
        decided_at = COALESCE(NEW.completed_at, CURRENT_TIMESTAMP)
    WHERE review_request_id = OLD.id AND decision = 'pending';
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_fence_parent_insert
    BEFORE INSERT ON production_review_steps
    FOR EACH ROW
    WHEN NOT EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.review_request_id AND tenant_id = NEW.tenant_id
          AND project_id = NEW.project_id AND document_id = NEW.document_id
          AND version_id = NEW.version_id AND status = 'pending'
    )
BEGIN
    SELECT RAISE(ABORT, 'terminal production review requests reject review steps');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_validate_initial_state
    BEFORE INSERT ON production_review_steps
    FOR EACH ROW
    WHEN NEW.decision <> 'pending' OR NEW.reviewer_user_id IS NOT NULL OR
         NEW.comment <> '' OR NEW.decided_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'production review steps must be inserted pending');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_fence_parent_update
    BEFORE UPDATE ON production_review_steps
    FOR EACH ROW
    WHEN NOT EXISTS (SELECT 1 FROM production_review_requests WHERE id = OLD.review_request_id AND status = 'pending')
BEGIN
    SELECT RAISE(ABORT, 'terminal production review requests reject review steps');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_guard_terminal
    BEFORE UPDATE ON production_review_steps
    FOR EACH ROW
    WHEN OLD.decision <> 'pending'
BEGIN
    SELECT RAISE(ABORT, 'terminal production review steps are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_guard_identity
    BEFORE UPDATE ON production_review_steps
    FOR EACH ROW
    WHEN NEW.id IS NOT OLD.id OR NEW.review_request_id IS NOT OLD.review_request_id OR
         NEW.tenant_id IS NOT OLD.tenant_id OR NEW.project_id IS NOT OLD.project_id OR
         NEW.document_id IS NOT OLD.document_id OR NEW.version_id IS NOT OLD.version_id OR
         NEW.required_role IS NOT OLD.required_role OR NEW.sequence IS NOT OLD.sequence OR
         NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'production review step identity is immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_validate_role
    BEFORE UPDATE ON production_review_steps
    FOR EACH ROW
    WHEN NEW.decision IN ('approved', 'changes_requested', 'rejected') AND NOT EXISTS (
        SELECT 1 FROM production_project_members
        WHERE project_id = NEW.project_id AND user_id = NEW.reviewer_user_id
          AND role = NEW.required_role AND deleted_at IS NULL
    )
BEGIN
    SELECT RAISE(ABORT, 'review decision requires the required review role');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_prevent_delete
    BEFORE DELETE ON production_review_steps
    FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'production review steps are append-only');
END;

CREATE TRIGGER IF NOT EXISTS trg_production_review_steps_prevent_replace
    BEFORE INSERT ON production_review_steps
    FOR EACH ROW
    WHEN EXISTS (
        SELECT 1 FROM production_review_steps
        WHERE id = NEW.id OR
            (review_request_id = NEW.review_request_id AND sequence = NEW.sequence) OR
            (review_request_id = NEW.review_request_id AND required_role = NEW.required_role)
    )
BEGIN
    SELECT RAISE(ABORT, 'production review steps cannot be replaced');
END;
