DROP TABLE IF EXISTS agent_messages;
DROP TABLE IF EXISTS device_nonces;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS email_verification_tokens;
DROP TABLE IF EXISTS sessions;
ALTER TABLE accounts
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS password_hash;
