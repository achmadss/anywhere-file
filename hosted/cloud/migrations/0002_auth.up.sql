-- Accounts, authentication, sessions, and account deletion (#20).
--
-- An account is a user identity in the cloud, needed only for remote access (r3 section 3).
-- It is never a file identity: no column here names a share, a path, or a trust-list role.
-- Passwords use a salted iterated hash. Session, verification, and reset tokens are random;
-- only their SHA-256 hex digest is stored.

ALTER TABLE accounts
    ADD COLUMN password_hash text,
    ADD COLUMN email_verified_at timestamptz,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

-- Web dashboard sessions. The raw token goes to the browser once (cookie plus JSON body);
-- the table keeps only its hash. Revocation sets revoked_at; expiry is checked on read.
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

-- Password reset links. Single use with the same used_at rule as verification tokens.
CREATE TABLE password_reset_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE CHECK (token_hash <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE INDEX password_reset_tokens_account_idx ON password_reset_tokens (account_id);

-- Anti-replay store for agent signed requests (r3 section 10.1: the device signs and
-- submits with the user's session). A nonce is accepted once inside its lifetime.
-- Old rows are deleted on read; the table stays small without a background job.
CREATE TABLE device_nonces (
    nonce      text        PRIMARY KEY CHECK (nonce <> ''),
    device_key text        NOT NULL CHECK (device_key <> ''),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX device_nonces_expires_idx ON device_nonces (expires_at);

-- Outbox that tells agents about account deletion (r3 section 14). Owned workspaces
-- become local-only and memberships are removed; files and device keys are untouched,
-- so the rows here name workspaces and devices only. Agents poll by device key.
CREATE TABLE agent_messages (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text      NOT NULL CHECK (workspace_id <> ''),
    device_key text        NOT NULL CHECK (device_key <> ''),
    kind       text        NOT NULL CHECK (kind IN ('workspace.local_only', 'membership.removed')),
    body       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agent_messages_device_idx ON agent_messages (device_key, id);
