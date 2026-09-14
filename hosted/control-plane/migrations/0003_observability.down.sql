DROP TABLE IF EXISTS job_heartbeats;
DROP TRIGGER IF EXISTS audit_events_no_write ON audit_events;
DROP FUNCTION IF EXISTS reject_audit_write();
ALTER TABLE audit_events DROP COLUMN IF EXISTS details;
