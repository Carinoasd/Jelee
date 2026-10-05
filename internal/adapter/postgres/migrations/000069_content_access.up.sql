BEGIN;
-- Content access rules (G48.1, G48.4). They narrow what a user already sees
-- through the library grants; the unified filter in visibility.go is their
-- only reader. docs/access-control.md describes the precedence.

-- Server-wide policy, one row. restrict_admins applies the content rules
-- below to administrators too (library grants never restrict them);
-- block_unrated is the default for users with a rating ceiling whose own
-- block_unrated is NULL.
CREATE TABLE access_policy (
 id boolean PRIMARY KEY DEFAULT true CONSTRAINT access_policy_single_row CHECK(id),
 restrict_admins boolean NOT NULL DEFAULT false,
 block_unrated boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO access_policy DEFAULT VALUES;

-- Rating codes and the minimum age each stands for. The filter normalizes
-- an item's mpaa and certification values (trimmed, upper case, without a
-- leading "Rated " or a two-letter country prefix such as "US:") and looks
-- them up here; a bare age such as "16" or "16+" stands for itself. Any
-- other value counts as unrated.
CREATE TABLE parental_ratings (
 code text PRIMARY KEY CONSTRAINT parental_ratings_code_check CHECK(code=upper(btrim(code)) AND length(code) BETWEEN 1 AND 32),
 level smallint NOT NULL CONSTRAINT parental_ratings_level_check CHECK(level BETWEEN 0 AND 21)
);
INSERT INTO parental_ratings(code,level) VALUES
 ('G',0),('PG',10),('PG-13',13),('R',17),('NC-17',18),
 ('TV-Y',0),('TV-Y7',7),('TV-Y7-FV',7),('TV-G',0),('TV-PG',10),('TV-14',14),('TV-MA',17),
 ('U',0),('UC',0),('12A',12),('R18',18),
 ('PG12',12),('R15+',15),('R18+',18),
 ('FSK 0',0),('FSK 6',6),('FSK 12',12),('FSK 16',16),('FSK 18',18),
 ('普遍級',0),('保護級',6),('輔導級',12),('輔12級',12),('輔15級',15),('限制級',18),
 ('普遍级',0),('保护级',6),('辅导级',12),('辅12级',12),('辅15级',15),('限制级',18);

-- Per-user content restrictions. parental_rating_max is the highest
-- allowed level (NULL: no ceiling); block_unrated overrides the policy
-- default for unrated items under a ceiling. content_filtered is true
-- whenever the user has any restriction, so the filter skips every rule
-- lookup for unrestricted users; it may stay true after the last rule went
-- away (that only costs the lookups), never false while one exists.
ALTER TABLE users
 ADD COLUMN parental_rating_max smallint CONSTRAINT users_parental_rating_max_check CHECK(parental_rating_max BETWEEN 0 AND 21),
 ADD COLUMN block_unrated boolean,
 ADD COLUMN content_filtered boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT users_content_filtered_check CHECK(parental_rating_max IS NULL OR content_filtered);

-- Explicit item rules. A rule covers the item and its descendants through
-- item_parent_links (series, season, episode); the nearest rule wins.
CREATE TABLE user_item_access_rules (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 effect text NOT NULL CONSTRAINT user_item_access_rules_effect_check CHECK(effect IN ('allow','hide')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,item_id)
);
-- Item deletion cascades through this index.
CREATE INDEX user_item_access_rules_item_idx ON user_item_access_rules(item_id);

-- Blocked tags and genres, matched case-insensitively against the item's
-- and its ancestors' tags and genres facts.
CREATE TABLE user_blocked_tags (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 tag text NOT NULL CONSTRAINT user_blocked_tags_tag_check CHECK(tag=lower(btrim(tag)) AND length(tag) BETWEEN 1 AND 128),
 PRIMARY KEY(user_id,tag)
);

-- Any new rule or blocked tag marks its user as filtered in the same
-- statement, whichever path wrote it.
CREATE FUNCTION mark_user_content_filtered() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE users SET content_filtered=true WHERE id=NEW.user_id AND NOT content_filtered;
 RETURN NULL;
END $$;
CREATE TRIGGER user_item_access_rules_filtered AFTER INSERT OR UPDATE ON user_item_access_rules
 FOR EACH ROW EXECUTE FUNCTION mark_user_content_filtered();
CREATE TRIGGER user_blocked_tags_filtered AFTER INSERT OR UPDATE ON user_blocked_tags
 FOR EACH ROW EXECUTE FUNCTION mark_user_content_filtered();
COMMIT;
