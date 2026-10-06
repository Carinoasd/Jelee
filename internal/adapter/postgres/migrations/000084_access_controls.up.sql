BEGIN;
-- Content access controls, part two (G48.4, G48.7). Blocked keywords and
-- restricted time windows narrow what a user sees like the rules of
-- 000069; the unified filter in visibility.go is their only reader.
-- Access templates are administration data: applying one writes the
-- library grants and restrictions of 000069 and nothing reads a template
-- to authorize content. docs/access-control.md describes the precedence.

-- Blocked keywords (G48.4). An item is hidden when its title, its metadata
-- title or original title, or one of its ancestors', contains a keyword.
-- Keywords are stored the way the filter compares: NFKC-normalized (full
-- width and half width forms fold together), trimmed and in lower case.
CREATE TABLE user_blocked_keywords (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 keyword text NOT NULL CONSTRAINT user_blocked_keywords_keyword_check CHECK(keyword=lower(btrim(normalize(keyword,NFKC))) AND length(keyword) BETWEEN 1 AND 512),
 PRIMARY KEY(user_id,keyword)
);

-- Restricted time windows (G48.4). While the request time, read in the
-- window's IANA time zone, falls into one of a user's windows, the window
-- applies: rating_max caps the user's rating ceiling for that time, and a
-- NULL rating_max hides every item. weekdays (0 is Sunday; empty is every
-- day) name the day a window opens; end_minute at or before start_minute
-- crosses midnight.
CREATE TABLE user_access_windows (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 position smallint NOT NULL CONSTRAINT user_access_windows_position_check CHECK(position BETWEEN 0 AND 19),
 weekdays smallint[] NOT NULL DEFAULT '{}' CONSTRAINT user_access_windows_weekdays_check CHECK(cardinality(weekdays)<=7 AND 0<=ALL(weekdays) AND 6>=ALL(weekdays)),
 start_minute smallint NOT NULL CONSTRAINT user_access_windows_start_check CHECK(start_minute BETWEEN 0 AND 1439),
 end_minute smallint NOT NULL CONSTRAINT user_access_windows_end_check CHECK(end_minute BETWEEN 1 AND 1440),
 time_zone text NOT NULL CONSTRAINT user_access_windows_time_zone_check CHECK(octet_length(time_zone) BETWEEN 1 AND 64),
 rating_max smallint CONSTRAINT user_access_windows_rating_max_check CHECK(rating_max BETWEEN 0 AND 21),
 CONSTRAINT user_access_windows_span_check CHECK(start_minute<>end_minute),
 PRIMARY KEY(user_id,position)
);

-- New keywords and windows mark their user as filtered in the same
-- statement, like rules and blocked tags (000069).
CREATE TRIGGER user_blocked_keywords_filtered AFTER INSERT OR UPDATE ON user_blocked_keywords
 FOR EACH ROW EXECUTE FUNCTION mark_user_content_filtered();
CREATE TRIGGER user_access_windows_filtered AFTER INSERT OR UPDATE ON user_access_windows
 FOR EACH ROW EXECUTE FUNCTION mark_user_content_filtered();

-- Access templates (G48.7): a named set of libraries and restrictions an
-- administrator applies to many users at once. Applying copies the
-- template into each user's grants and restrictions; later template edits
-- do not change users it was applied to.
CREATE TABLE access_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL CONSTRAINT access_templates_name_check CHECK(name=btrim(name) AND length(name) BETWEEN 1 AND 64),
 rating_max smallint CONSTRAINT access_templates_rating_check CHECK(rating_max BETWEEN 0 AND 21),
 block_unrated boolean,
 blocked_tags text[] NOT NULL DEFAULT '{}' CONSTRAINT access_templates_tags_check CHECK(cardinality(blocked_tags)<=100),
 blocked_keywords text[] NOT NULL DEFAULT '{}' CONSTRAINT access_templates_keywords_check CHECK(cardinality(blocked_keywords)<=100),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX access_templates_name_key ON access_templates(lower(name));
CREATE TABLE access_template_libraries (
 template_id uuid NOT NULL REFERENCES access_templates(id) ON DELETE CASCADE,
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 PRIMARY KEY(template_id,library_id)
);
-- Library deletion cascades through this index.
CREATE INDEX access_template_libraries_library_idx ON access_template_libraries(library_id);
COMMIT;
