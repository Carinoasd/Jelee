BEGIN;
-- G32.4 and G33.2–G33.5 site-wide web client settings. Each document is a
-- single row written only by administrators through /api/v1/site/*; every
-- signed-in user reads the effective values. revision guards against lost
-- updates between two administrators. Changes are audited
-- (site.appearance_changed, site.plugins_changed). The application validates
-- and sanitizes the content; the constraints below are coarse backstops.
CREATE TABLE site_appearance (
 id boolean PRIMARY KEY DEFAULT true CONSTRAINT site_appearance_singleton CHECK(id),
 default_theme text NOT NULL DEFAULT 'system' CONSTRAINT site_appearance_theme_check CHECK(default_theme IN ('system','light','dark')),
 tokens jsonb NOT NULL DEFAULT '{"light":{},"dark":{}}' CONSTRAINT site_appearance_tokens_check CHECK(jsonb_typeof(tokens)='object' AND jsonb_typeof(tokens->'light')='object' AND jsonb_typeof(tokens->'dark')='object' AND octet_length(tokens::text)<=16384),
 -- 64 Ki UTF-16 code units are at most 192 KiB of UTF-8.
 custom_css text NOT NULL DEFAULT '' CONSTRAINT site_appearance_css_check CHECK(octet_length(custom_css)<=196608),
 allow_external_fonts boolean NOT NULL DEFAULT false,
 font_hosts text[] NOT NULL DEFAULT '{}' CONSTRAINT site_appearance_font_hosts_check CHECK(cardinality(font_hosts)<=10 AND array_position(font_hosts,NULL) IS NULL),
 default_layout jsonb CONSTRAINT site_appearance_layout_check CHECK(default_layout IS NULL OR jsonb_typeof(default_layout)='object' AND octet_length(default_layout::text)<=16384),
 revision bigint NOT NULL DEFAULT 0 CONSTRAINT site_appearance_revision_check CHECK(revision>=0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO site_appearance DEFAULT VALUES;

CREATE TABLE site_plugins (
 id boolean PRIMARY KEY DEFAULT true CONSTRAINT site_plugins_singleton CHECK(id),
 plugins jsonb NOT NULL DEFAULT '[]' CONSTRAINT site_plugins_list_check CHECK(jsonb_typeof(plugins)='array' AND jsonb_array_length(plugins)<=64),
 settings jsonb NOT NULL DEFAULT '{}' CONSTRAINT site_plugins_settings_check CHECK(jsonb_typeof(settings)='object' AND octet_length(settings::text)<=1114112),
 revision bigint NOT NULL DEFAULT 0 CONSTRAINT site_plugins_revision_check CHECK(revision>=0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO site_plugins DEFAULT VALUES;

-- G33.5 page layout per user: the current layout and saved presets. NULL
-- until the user customizes it; the site default layout applies meanwhile.
ALTER TABLE user_preferences ADD COLUMN layout jsonb CONSTRAINT user_preferences_layout_check CHECK(layout IS NULL OR jsonb_typeof(layout)='object' AND octet_length(layout::text)<=32768);
COMMIT;
