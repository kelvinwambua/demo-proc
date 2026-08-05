-- Fix: 'approved' was missing from quotes.approval_status CHECK constraint, causing
-- DecideQuote approve/reject actions to fail with SQLSTATE 23514 when transitioning
-- from pending_manager/pending_director to approved.

DO $$
DECLARE
    conname text;
BEGIN
    SELECT tc.conname INTO conname
    FROM pg_constraint tc
    JOIN pg_class t ON t.oid = tc.conrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
    WHERE t.relname = 'quotes'
      AND n.nspname = current_schema()
      AND contype = 'c'
      AND pg_get_constraintdef(tc.oid) LIKE '%approval_status%IN%';

    IF conname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE quotes DROP CONSTRAINT %I;', conname);
    END IF;
END $$;

ALTER TABLE quotes
    ADD CONSTRAINT quotes_approval_status_check
    CHECK (approval_status IN ('auto_approved','pending_manager','pending_director','approved','rejected'));
