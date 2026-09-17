-- Deleting an account nulls used_by on the invites it redeemed, through the foreign key's
-- ON DELETE SET NULL. The single-use trigger read that as a second use and refused, so an
-- account that had ever redeemed an invite could not be deleted.
--
-- The rule stays what it was: a used invite cannot be used again. Releasing the reference
-- to a deleted account is the one update a used row now accepts, and used_at, the column
-- the consuming UPDATE guards on, still cannot move.
CREATE OR REPLACE FUNCTION reject_invite_reuse() RETURNS trigger AS $$
BEGIN
    IF OLD.used_at IS NOT NULL THEN
        IF NEW.used_at = OLD.used_at AND OLD.used_by IS NOT NULL AND NEW.used_by IS NULL THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'invite already used';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
