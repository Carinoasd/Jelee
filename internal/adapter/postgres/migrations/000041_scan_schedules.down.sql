DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM scan_schedules) THEN
        RAISE EXCEPTION 'retained scan schedules prevent downgrade';
    END IF;
END $$;
DROP TABLE scan_schedules;
