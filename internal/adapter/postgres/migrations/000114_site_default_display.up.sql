BEGIN;
-- Site default display choices (palette preset, poster ratio and size, list
-- density, default view) for users who did not choose their own. NULL until
-- an administrator sets any; the API validates every member against the
-- same allow-lists as user_preferences.display.
ALTER TABLE site_appearance ADD COLUMN default_display jsonb CONSTRAINT site_appearance_default_display_check CHECK(default_display IS NULL OR jsonb_typeof(default_display)='object' AND octet_length(default_display::text)<=1024);
COMMIT;
