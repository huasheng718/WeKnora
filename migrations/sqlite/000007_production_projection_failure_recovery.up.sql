ALTER TABLE production_release_targets
    ADD COLUMN recovery_attempted_at DATETIME NULL;

CREATE INDEX IF NOT EXISTS idx_production_release_targets_failure_recovery
    ON production_release_targets (tenant_id, status, recovery_attempted_at, id);

DROP TRIGGER IF EXISTS trg_production_release_targets_guard_recovery_attempt;
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
