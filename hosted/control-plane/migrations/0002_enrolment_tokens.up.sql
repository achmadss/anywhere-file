-- Single-use tokens a signed-in user mints so their PC can enrol as their device (#83).
-- The agent presents the raw token beside its device signature; the table keeps the hash.
CREATE TABLE enrolment_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash text        NOT NULL UNIQUE CHECK (token_hash <> ''),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
