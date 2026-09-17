CREATE OR REPLACE FUNCTION reject_invite_reuse() RETURNS trigger AS $$
BEGIN
    IF OLD.used_at IS NOT NULL THEN
        RAISE EXCEPTION 'invite already used';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
