-- The cloud data model of r3 §10.6. A directory, a billing system, and a relay operator.
-- Nothing here decides access: that is the signed trust list on the devices.
--
-- Identifier columns are text. Workspace ids and device keys are defined by the core crate
-- (#5-#11) and their encoding is not settled, so the schema stores them opaquely and only
-- checks that they are non-empty.

CREATE TABLE accounts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email      text        NOT NULL UNIQUE CHECK (email <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One subscription per account (r3 §10.5: the owner's subscription covers the workspace,
-- members pay nothing), so account_id is the key rather than a foreign column.
CREATE TABLE subscriptions (
    account_id         uuid        PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    tier               text        NOT NULL CHECK (tier <> ''),
    status             text        NOT NULL CHECK (status IN ('active', 'grace', 'suspended', 'cancelled')),
    current_period_end timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- r3 §10.1. workspace_id is the primary key, which is the UNIQUE(workspace_id) the
-- requirement calls load-bearing: it is what makes "the first valid Enable Remote Access
-- wins, later ones get already connected to <account>" true rather than hopeful. Two
-- concurrent enables cannot both succeed, whatever the application layer forgets to check.
CREATE TABLE workspace_associations (
    workspace_id       text        PRIMARY KEY CHECK (workspace_id <> ''),
    owner_account_id   uuid        NOT NULL REFERENCES accounts(id),
    status             text        NOT NULL CHECK (status IN ('active', 'suspended', 'disabled')),
    trust_list_version bigint      NOT NULL CHECK (trust_list_version >= 0),
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX workspace_associations_owner_idx ON workspace_associations (owner_account_id);

-- r3 §10.3. The owner also holds a row here; the owner column on the association is the
-- authority on who that is, this table carries the role for uniform member listing.
CREATE TABLE workspace_members (
    workspace_id text NOT NULL REFERENCES workspace_associations(workspace_id) ON DELETE CASCADE,
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('owner', 'manager', 'member')),
    status       text NOT NULL CHECK (status IN ('active', 'removed')),
    PRIMARY KEY (workspace_id, account_id)
);

-- Only one active owner row per workspace.
CREATE UNIQUE INDEX workspace_members_one_owner_idx
    ON workspace_members (workspace_id)
    WHERE role = 'owner' AND status = 'active';

-- account_id is nullable: a device paired locally is never bound to an account (r3 §10.3,
-- "unbound devices are untouched").
CREATE TABLE devices (
    device_key   text PRIMARY KEY CHECK (device_key <> ''),
    account_id   uuid REFERENCES accounts(id) ON DELETE SET NULL,
    display_name text NOT NULL DEFAULT ''
);

CREATE INDEX devices_account_idx ON devices (account_id) WHERE account_id IS NOT NULL;

CREATE TABLE device_authorizations (
    workspace_id text NOT NULL REFERENCES workspace_associations(workspace_id) ON DELETE CASCADE,
    device_key   text NOT NULL REFERENCES devices(device_key) ON DELETE CASCADE,
    status       text NOT NULL CHECK (status IN ('active', 'revoked')),
    PRIMARY KEY (workspace_id, device_key)
);

COMMENT ON TABLE device_authorizations IS
    'MIRROR, NEVER AUTHORITATIVE. The authority on which devices belong to a workspace is '
    'the signed trust list held by the devices themselves (r3 §8.1, D5). This table is a '
    'copy the relay authorization endpoint reads to decide whether to admit a connection to '
    'a relay. It can only ever decline; it can never grant access to a file, and a device '
    'absent here but present in the signed trust list is still a member of the workspace. '
    'Do not make it the source of truth for access.';

-- The relay authorization endpoint (#24) looks up by device key, and divides a workspace
-- rate by the count of its active devices, so both directions are indexed. The rate itself
-- is an operator dial and has no column here yet; #24 adds one when it needs it.
CREATE INDEX device_authorizations_device_idx ON device_authorizations (device_key);
CREATE INDEX device_authorizations_active_idx ON device_authorizations (workspace_id) WHERE status = 'active';

-- r3 §8.3. account_id is the account that requested the pairing.
CREATE TABLE pairing_requests (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id text        NOT NULL REFERENCES workspace_associations(workspace_id) ON DELETE CASCADE,
    device_key   text        NOT NULL CHECK (device_key <> ''),
    account_id   uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    status       text        NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One pending request per (workspace, device); a resubmission replaces the old one.
CREATE UNIQUE INDEX pairing_requests_pending_idx
    ON pairing_requests (workspace_id, device_key)
    WHERE status = 'pending';

-- r3 §10.2. The 7-day expiry is written into expires_at by the caller.
CREATE TABLE transfer_requests (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id text        NOT NULL REFERENCES workspace_associations(workspace_id) ON DELETE CASCADE,
    from_account uuid        NOT NULL REFERENCES accounts(id),
    to_account   uuid        NOT NULL REFERENCES accounts(id),
    status       text        NOT NULL CHECK (status IN ('pending', 'accepted', 'confirmed', 'rejected', 'expired')),
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (from_account <> to_account)
);

-- A workspace can only be in one transfer at a time.
CREATE UNIQUE INDEX transfer_requests_open_idx
    ON transfer_requests (workspace_id)
    WHERE status IN ('pending', 'accepted');

-- r3 §18: an audit record of every privileged action. actor is an account id or a device
-- key, whichever took the action, so it is text and not a foreign key. Rows are never
-- updated or deleted, and there is deliberately no foreign key on workspace_id so that
-- deleting an association does not erase its history.
CREATE TABLE audit_events (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text        NOT NULL CHECK (workspace_id <> ''),
    actor        text        NOT NULL CHECK (actor <> ''),
    action       text        NOT NULL CHECK (action <> ''),
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_workspace_at_idx ON audit_events (workspace_id, at DESC);
