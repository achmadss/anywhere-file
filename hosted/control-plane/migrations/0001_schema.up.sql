-- The data model of docs/new-arch.md "Database model", minus device_tunnel_credentials
-- (ADR 0005). The server is a directory of accounts, devices and who may reach which
-- device. It never sees a file or a path.

-- Users. Passwords use a salted iterated hash. Session, verification and reset tokens are
-- random; only their SHA-256 hex digest is stored.
CREATE TABLE accounts (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text        NOT NULL UNIQUE CHECK (email <> ''),
    password_hash     text,
    email_verified_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Billing is stubbed (#21): a status an operator sets. No provider, no periods.
CREATE TABLE subscriptions (
    account_id uuid        PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    status     text        NOT NULL CHECK (status IN ('active', 'suspended')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Web and client sessions. The raw token goes to the caller once; the table keeps only
-- its hash. Revocation sets revoked_at; expiry is checked on read.
CREATE TABLE sessions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE CHECK (token_hash <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    user_agent text        NOT NULL DEFAULT '',
    ip         text        NOT NULL DEFAULT ''
);

CREATE INDEX sessions_account_idx ON sessions (account_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Email verification links. Single use: confirming sets used_at and a second use fails.
CREATE TABLE email_verification_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE CHECK (token_hash <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE INDEX email_verification_tokens_account_idx ON email_verification_tokens (account_id);

-- Password reset links, with the same used_at rule.
CREATE TABLE password_reset_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE CHECK (token_hash <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE INDEX password_reset_tokens_account_idx ON password_reset_tokens (account_id);

-- A PC running the agent. public_key is the hex Ed25519 key the agent generated; the
-- device never uploads its private key. device_id is derived here from the key, so an
-- agent cannot pick one: an INSERT that supplies device_id is rejected by PostgreSQL.
CREATE TABLE devices (
    public_key text        NOT NULL UNIQUE CHECK (public_key ~ '^[0-9a-f]{64}$'),
    device_id  text        PRIMARY KEY GENERATED ALWAYS AS (encode(sha256(decode(public_key, 'hex')), 'hex')) STORED,
    name       text        NOT NULL DEFAULT '',
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Who may reach a device, and as what. One row per (device, user); revoked_at null means
-- active. Re-admitting a revoked user clears revoked_at on the same row.
CREATE TABLE device_users (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text        NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('admin', 'guest')),
    created_by uuid        REFERENCES accounts(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    UNIQUE (device_id, user_id)
);

CREATE INDEX device_users_user_idx ON device_users (user_id);

-- An invitation is a bearer code. The raw code is returned once and only its hash is
-- stored. Consuming it is one UPDATE guarded by used_at IS NULL, and the trigger below
-- makes a used row read-only so no code path can consume it twice.
CREATE TABLE invites (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text        NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    created_by uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    code_hash  text        NOT NULL UNIQUE CHECK (code_hash <> ''),
    role       text        NOT NULL CHECK (role IN ('admin', 'guest')),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    used_by    uuid        REFERENCES accounts(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (used_by IS NULL OR used_at IS NOT NULL)
);

CREATE FUNCTION reject_invite_reuse() RETURNS trigger AS $$
BEGIN
    IF OLD.used_at IS NOT NULL THEN
        RAISE EXCEPTION 'invite already used';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER invites_single_use
    BEFORE UPDATE ON invites
    FOR EACH ROW EXECUTE FUNCTION reject_invite_reuse();

-- Applications the agent exposes on a device, reported by the agent (#84).
CREATE TABLE device_apps (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  text        NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (name <> ''),
    type       text        NOT NULL CHECK (type <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (device_id, name)
);

-- Anti-replay store for signed device requests. A nonce is accepted once inside its
-- lifetime. Old rows are deleted on read; the table stays small without a background job.
CREATE TABLE device_nonces (
    nonce      text        PRIMARY KEY CHECK (nonce <> ''),
    device_key text        NOT NULL CHECK (device_key <> ''),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX device_nonces_expires_idx ON device_nonces (expires_at);

-- One row per privileged action. actor is an account id or a device id, whichever acted.
-- device_id has no foreign key so history outlives the device. details carries typed ids
-- and enum values only, never a path; audit.go enforces the shape.
CREATE TABLE audit_events (
    id        bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id text        CHECK (device_id <> ''),
    actor     text        NOT NULL CHECK (actor <> ''),
    action    text        NOT NULL CHECK (action <> ''),
    details   jsonb       NOT NULL DEFAULT '{}',
    at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_device_at_idx ON audit_events (device_id, at DESC);

-- Append-only at the database level, so no future code path can update or delete
-- history by accident.
CREATE FUNCTION reject_audit_write() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_write
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_audit_write();

-- Last successful run of each periodic job, so the "job not run" alert survives a restart.
CREATE TABLE job_heartbeats (
    name     text        PRIMARY KEY CHECK (name <> ''),
    last_run timestamptz NOT NULL DEFAULT now()
);
