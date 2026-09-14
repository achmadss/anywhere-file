-- Observability for r3 section 18 (issue #27). Audit detail payload, append-only
-- enforcement, and a heartbeat table for the subscription state job.
--
-- The audit detail column carries typed ids and enum values only. It must never
-- carry a filesystem path (r3 section 7: the cloud never receives one). The shape
-- is enforced in Go by audit.go; this migration only adds storage for it.

-- Detail payload for one audit row. Keys are a fixed set defined in audit.go
-- (account_id, device_key, role, prev_role, from_status, to_status, reason).
-- No key in that set names a path, and no value may contain one.
ALTER TABLE audit_events
    ADD COLUMN details jsonb NOT NULL DEFAULT '{}';

-- Append-only at the database level, so no future code path can update or delete
-- history by accident. The application layer has no update or delete path either.
CREATE FUNCTION reject_audit_write() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_write
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_audit_write();

-- Last successful run of each periodic job, keyed by job name. The subscription
-- state job (#27 alert rules) records here so the "job not run in its interval"
-- alert survives a control plane restart. Gauges reset on restart; this does not.
CREATE TABLE job_heartbeats (
    name     text        PRIMARY KEY CHECK (name <> ''),
    last_run timestamptz NOT NULL DEFAULT now()
);
