DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM scan_schedules WHERE watch_enabled) OR EXISTS (SELECT 1 FROM scan_watch_state) THEN
        RAISE EXCEPTION 'retained watch definitions or state prevent downgrade';
    END IF;
END $$;
DROP TABLE scan_watch_state;
DROP INDEX scan_schedules_last_job;
ALTER TABLE scan_schedules DROP COLUMN watch_enabled;
