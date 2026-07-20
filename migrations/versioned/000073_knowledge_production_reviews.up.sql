CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_versions_review_context
    ON production_document_versions (id, document_id, tenant_id, project_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_production_document_blocks_review_anchor
    ON production_document_blocks (id, version_id);

CREATE TABLE IF NOT EXISTS production_annotations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    block_id VARCHAR(36) NOT NULL,
    annotation_type VARCHAR(20) NOT NULL,
    quality_tag VARCHAR(32) NULL,
    severity VARCHAR(16) NOT NULL DEFAULT 'info',
    anchor JSONB NOT NULL DEFAULT '{}'::jsonb,
    body TEXT NOT NULL,
    suggested_content TEXT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_by VARCHAR(36) NOT NULL,
    resolved_by VARCHAR(36) NULL,
    resolved_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_annotations_type CHECK (annotation_type IN ('comment', 'suggestion', 'quality_tag')),
    CONSTRAINT chk_production_annotations_quality_tag CHECK (
        (annotation_type = 'quality_tag' AND quality_tag IS NOT NULL AND quality_tag IN ('missing_evidence', 'factual_risk', 'unclear', 'incomplete', 'conflict', 'compliance_risk')) OR
        (annotation_type IN ('comment', 'suggestion') AND quality_tag IS NULL)
    ),
    CONSTRAINT chk_production_annotations_severity CHECK (severity IN ('info', 'warning', 'blocking')),
    CONSTRAINT chk_production_annotations_anchor CHECK (jsonb_typeof(anchor) = 'object'),
    CONSTRAINT chk_production_annotations_body CHECK (char_length(body) BETWEEN 1 AND 20000),
    CONSTRAINT chk_production_annotations_suggested_content CHECK (suggested_content IS NULL OR char_length(suggested_content) <= 20000),
    CONSTRAINT chk_production_annotations_status CHECK (status IN ('open', 'resolved', 'dismissed')),
    CONSTRAINT chk_production_annotations_resolution CHECK (
        (status = 'open' AND resolved_by IS NULL AND resolved_at IS NULL) OR
        (status IN ('resolved', 'dismissed') AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL)
    ),
    CONSTRAINT fk_production_annotations_document_context FOREIGN KEY (document_id, tenant_id, project_id)
        REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_annotations_version_context FOREIGN KEY (version_id, document_id, tenant_id, project_id)
        REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_annotations_block_anchor FOREIGN KEY (block_id, version_id)
        REFERENCES production_document_blocks(id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_annotations_document_version_status
    ON production_annotations (document_id, version_id, status);
CREATE INDEX IF NOT EXISTS idx_production_annotations_block_status
    ON production_annotations (block_id, status);

CREATE TABLE IF NOT EXISTS production_review_requests (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    policy_snapshot JSONB NOT NULL,
    policy_digest VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    submitted_by VARCHAR(36) NOT NULL,
    submitted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    terminal_by VARCHAR(36) NULL,
    terminal_reason TEXT NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_review_requests_policy CHECK (jsonb_typeof(policy_snapshot) = 'object'),
    CONSTRAINT chk_production_review_requests_policy_digest CHECK (
        policy_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT chk_production_review_requests_status CHECK (status IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled', 'changes_requested')),
    CONSTRAINT chk_production_review_requests_terminal CHECK (
        (status = 'pending' AND terminal_by IS NULL AND terminal_reason IS NULL AND completed_at IS NULL) OR
        (status = 'approved' AND terminal_by IS NOT NULL AND completed_at IS NOT NULL) OR
        (status IN ('rejected', 'obsolete', 'cancelled', 'changes_requested') AND terminal_by IS NOT NULL AND terminal_reason IS NOT NULL AND char_length(terminal_reason) BETWEEN 1 AND 5000 AND completed_at IS NOT NULL)
    ),
    UNIQUE(id, tenant_id, project_id, document_id, version_id),
    CONSTRAINT fk_production_review_requests_document_context FOREIGN KEY (document_id, tenant_id, project_id)
        REFERENCES production_documents(id, tenant_id, project_id) ON DELETE RESTRICT,
    CONSTRAINT fk_production_review_requests_version_context FOREIGN KEY (version_id, document_id, tenant_id, project_id)
        REFERENCES production_document_versions(id, document_id, tenant_id, project_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_production_review_requests_version
    ON production_review_requests (tenant_id, project_id, document_id, version_id);
CREATE INDEX IF NOT EXISTS idx_production_review_requests_document_version_status
    ON production_review_requests (document_id, version_id, status);

CREATE TABLE IF NOT EXISTS production_review_steps (
    id VARCHAR(36) PRIMARY KEY,
    review_request_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL,
    project_id VARCHAR(36) NOT NULL,
    document_id VARCHAR(36) NOT NULL,
    version_id VARCHAR(36) NOT NULL,
    required_role VARCHAR(32) NOT NULL,
    sequence INTEGER NOT NULL,
    reviewer_user_id VARCHAR(36) NULL,
    decision VARCHAR(24) NOT NULL DEFAULT 'pending',
    comment TEXT NOT NULL DEFAULT '',
    decided_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_production_review_steps_role CHECK (required_role IN ('business_reviewer', 'engineering_reviewer', 'compliance_reviewer')),
    CONSTRAINT chk_production_review_steps_sequence CHECK (sequence >= 1),
    CONSTRAINT chk_production_review_steps_decision CHECK (decision IN ('pending', 'approved', 'changes_requested', 'rejected', 'cancelled')),
    CONSTRAINT chk_production_review_steps_comment CHECK (char_length(comment) <= 5000),
    CONSTRAINT chk_production_review_steps_decision_actor CHECK (
        (decision = 'pending' AND reviewer_user_id IS NULL AND decided_at IS NULL AND comment = '') OR
        (decision IN ('approved', 'changes_requested', 'rejected') AND reviewer_user_id IS NOT NULL AND decided_at IS NOT NULL) OR
        (decision = 'cancelled' AND reviewer_user_id IS NULL AND decided_at IS NOT NULL AND comment = '')
    ),
    UNIQUE(review_request_id, sequence),
    UNIQUE(review_request_id, required_role),
    CONSTRAINT fk_production_review_steps_request_context FOREIGN KEY (review_request_id, tenant_id, project_id, document_id, version_id)
        REFERENCES production_review_requests(id, tenant_id, project_id, document_id, version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_sequence
    ON production_review_steps (review_request_id, sequence);
CREATE INDEX IF NOT EXISTS idx_production_review_steps_request_role_decision
    ON production_review_steps (review_request_id, required_role, decision);

CREATE OR REPLACE FUNCTION guard_production_annotation()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('resolved', 'dismissed') THEN
        RAISE EXCEPTION 'terminal production annotations are immutable';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.block_id IS DISTINCT FROM OLD.block_id OR
       NEW.annotation_type IS DISTINCT FROM OLD.annotation_type OR
       NEW.quality_tag IS DISTINCT FROM OLD.quality_tag OR
       NEW.severity IS DISTINCT FROM OLD.severity OR
       NEW.anchor IS DISTINCT FROM OLD.anchor OR
       NEW.body IS DISTINCT FROM OLD.body OR
       NEW.suggested_content IS DISTINCT FROM OLD.suggested_content OR
       NEW.created_by IS DISTINCT FROM OLD.created_by OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production annotation anchor is immutable';
    END IF;
    IF OLD.status = 'open' AND NEW.status NOT IN ('open', 'resolved', 'dismissed') THEN
        RAISE EXCEPTION 'invalid production annotation status transition';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_annotations_guard
    BEFORE UPDATE ON production_annotations
    FOR EACH ROW EXECUTE FUNCTION guard_production_annotation();

CREATE OR REPLACE FUNCTION prevent_production_annotation_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'production annotations are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_annotations_prevent_delete
    BEFORE DELETE ON production_annotations
    FOR EACH ROW EXECUTE FUNCTION prevent_production_annotation_delete();

CREATE OR REPLACE FUNCTION prevent_production_annotation_replace()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM production_annotations WHERE id = NEW.id) THEN
        RAISE EXCEPTION 'production annotations cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_annotations_prevent_replace
    BEFORE INSERT ON production_annotations
    FOR EACH ROW EXECUTE FUNCTION prevent_production_annotation_replace();

CREATE OR REPLACE FUNCTION validate_production_review_request_submission()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status <> 'pending' OR NEW.terminal_by IS NOT NULL OR
       NEW.terminal_reason IS NOT NULL OR NEW.completed_at IS NOT NULL THEN
        RAISE EXCEPTION 'production review requests must be submitted pending';
    END IF;
    IF EXISTS (
        SELECT 1 FROM production_annotations
        WHERE tenant_id = NEW.tenant_id AND project_id = NEW.project_id
          AND document_id = NEW.document_id AND version_id = NEW.version_id
          AND severity = 'blocking' AND status = 'open'
    ) THEN
        RAISE EXCEPTION 'open blocking annotations prevent review submission';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_requests_validate_submission
    BEFORE INSERT ON production_review_requests
    FOR EACH ROW EXECUTE FUNCTION validate_production_review_request_submission();

CREATE OR REPLACE FUNCTION guard_production_review_request_identity()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'terminal production review requests are immutable';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.policy_snapshot IS DISTINCT FROM OLD.policy_snapshot OR
       NEW.policy_digest IS DISTINCT FROM OLD.policy_digest OR
       NEW.submitted_by IS DISTINCT FROM OLD.submitted_by OR
       NEW.submitted_at IS DISTINCT FROM OLD.submitted_at OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production review request identity is immutable';
    END IF;
    IF NEW.status NOT IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled', 'changes_requested') THEN
        RAISE EXCEPTION 'invalid production review request status transition';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_requests_guard_identity
    BEFORE UPDATE ON production_review_requests
    FOR EACH ROW EXECUTE FUNCTION guard_production_review_request_identity();

CREATE OR REPLACE FUNCTION prevent_production_review_request_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'production review requests are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_requests_prevent_delete
    BEFORE DELETE ON production_review_requests
    FOR EACH ROW EXECUTE FUNCTION prevent_production_review_request_delete();

CREATE OR REPLACE FUNCTION prevent_production_review_request_replace()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.id OR (
            tenant_id = NEW.tenant_id AND project_id = NEW.project_id
            AND document_id = NEW.document_id AND version_id = NEW.version_id
        )
    ) THEN
        RAISE EXCEPTION 'production review requests cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_requests_prevent_replace
    BEFORE INSERT ON production_review_requests
    FOR EACH ROW EXECUTE FUNCTION prevent_production_review_request_replace();

CREATE OR REPLACE FUNCTION fence_terminal_production_review_children()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status = 'pending' AND NEW.status IN ('approved', 'rejected', 'cancelled', 'obsolete', 'changes_requested') THEN
        IF NOT EXISTS (SELECT 1 FROM production_review_steps WHERE review_request_id = OLD.id) THEN
            RAISE EXCEPTION 'production review requests require materialized review steps';
        END IF;
        IF NEW.status = 'approved' AND EXISTS (
            SELECT 1 FROM production_review_steps
            WHERE review_request_id = OLD.id AND decision <> 'approved'
        ) THEN
            RAISE EXCEPTION 'all production review steps must approve the review';
        END IF;
        IF NEW.status IN ('rejected', 'cancelled', 'obsolete', 'changes_requested') THEN
            UPDATE production_review_steps
            SET decision = 'cancelled', reviewer_user_id = NULL, comment = '',
                decided_at = COALESCE(NEW.completed_at, CURRENT_TIMESTAMP)
            WHERE review_request_id = OLD.id AND decision = 'pending';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_requests_fence_terminal_children
    BEFORE UPDATE OF status ON production_review_requests
    FOR EACH ROW EXECUTE FUNCTION fence_terminal_production_review_children();

CREATE OR REPLACE FUNCTION validate_production_review_step_parent()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.decision <> 'pending' OR NEW.reviewer_user_id IS NOT NULL OR
       NEW.comment <> '' OR NEW.decided_at IS NOT NULL THEN
        RAISE EXCEPTION 'production review steps must be inserted pending';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM production_review_requests
        WHERE id = NEW.review_request_id AND tenant_id = NEW.tenant_id
          AND project_id = NEW.project_id AND document_id = NEW.document_id
          AND version_id = NEW.version_id AND status = 'pending'
    ) THEN
        RAISE EXCEPTION 'terminal production review requests reject review steps';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_steps_fence_parent_insert
    BEFORE INSERT ON production_review_steps
    FOR EACH ROW EXECUTE FUNCTION validate_production_review_step_parent();

CREATE OR REPLACE FUNCTION guard_production_review_step_decision()
RETURNS TRIGGER AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM production_review_requests WHERE id = OLD.review_request_id AND status = 'pending') THEN
        RAISE EXCEPTION 'terminal production review requests reject review steps';
    END IF;
    IF OLD.decision <> 'pending' THEN
        RAISE EXCEPTION 'terminal production review steps are immutable';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.review_request_id IS DISTINCT FROM OLD.review_request_id OR
       NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
       NEW.project_id IS DISTINCT FROM OLD.project_id OR
       NEW.document_id IS DISTINCT FROM OLD.document_id OR
       NEW.version_id IS DISTINCT FROM OLD.version_id OR
       NEW.required_role IS DISTINCT FROM OLD.required_role OR
       NEW.sequence IS DISTINCT FROM OLD.sequence OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'production review step identity is immutable';
    END IF;
    IF NEW.decision IN ('approved', 'changes_requested', 'rejected') AND NOT EXISTS (
        SELECT 1 FROM production_project_members
        WHERE project_id = NEW.project_id AND user_id = NEW.reviewer_user_id
          AND role = NEW.required_role AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'review decision requires the required review role';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_steps_guard_decision
    BEFORE UPDATE ON production_review_steps
    FOR EACH ROW EXECUTE FUNCTION guard_production_review_step_decision();

CREATE OR REPLACE FUNCTION prevent_production_review_step_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'production review steps are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_steps_prevent_delete
    BEFORE DELETE ON production_review_steps
    FOR EACH ROW EXECUTE FUNCTION prevent_production_review_step_delete();

CREATE OR REPLACE FUNCTION prevent_production_review_step_replace()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM production_review_steps
        WHERE id = NEW.id OR
            (review_request_id = NEW.review_request_id AND sequence = NEW.sequence) OR
            (review_request_id = NEW.review_request_id AND required_role = NEW.required_role)
    ) THEN
        RAISE EXCEPTION 'production review steps cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_production_review_steps_prevent_replace
    BEFORE INSERT ON production_review_steps
    FOR EACH ROW EXECUTE FUNCTION prevent_production_review_step_replace();
