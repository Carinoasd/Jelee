BEGIN;
-- Display choices of the web client (palette preset, poster ratio and size,
-- list density, default view) per user. NULL until the user saves any; the
-- API validates every member against its own allow-list.
ALTER TABLE user_preferences ADD COLUMN display jsonb CONSTRAINT user_preferences_display_check CHECK(display IS NULL OR jsonb_typeof(display)='object' AND octet_length(display::text)<=1024);
COMMIT;
