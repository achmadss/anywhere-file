-- Browser enrolment (#139). The agent asks for a code, signed with its device key. The
-- person approves it while signed in on the website. Approval alone binds nothing and the
-- device key alone binds nothing: collecting the enrolment token needs the code and a
-- signature from the device the code was minted for.
--
-- The code is short enough to read down a phone line, so only its hash is stored and the
-- routes that take one are rate limited. Entropy alone is not what stops guessing here.
CREATE TABLE device_enrolments (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash   text        NOT NULL UNIQUE CHECK (code_hash <> ''),
    device_key  text        NOT NULL CHECK (device_key <> ''),
    name        text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'refused')),
    answered_by uuid        REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
