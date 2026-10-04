BEGIN;
-- Manual version decisions (G20.3, G20.5) and per-user track preferences
-- (G16.5, G20.4).
--
-- Every administrator decision is one operation row. Its undo document
-- holds exactly what reversing it needs (moved sources, the rows of an
-- absorbed item, the user data the merge policy changed); it is kept until
-- undo_until and is never exported with metadata backups.
CREATE TABLE item_version_operations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 kind text NOT NULL CONSTRAINT item_version_operations_kind_check CHECK(kind IN ('split','merge','primary','unexclude')),
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 -- The item that stays: the original of a split, the target of a merge.
 item_id uuid NOT NULL,
 -- The item a split created or a merge absorbed.
 other_item_id uuid,
 source_ids uuid[] NOT NULL DEFAULT '{}' CONSTRAINT item_version_operations_sources_check CHECK(cardinality(source_ids)<=4096),
 undo jsonb NOT NULL CONSTRAINT item_version_operations_undo_check CHECK(jsonb_typeof(undo)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 undo_until timestamptz NOT NULL,
 undone_at timestamptz,
 undone_by uuid REFERENCES users(id) ON DELETE SET NULL,
 CONSTRAINT item_version_operations_window_check CHECK(undo_until>created_at),
 CONSTRAINT item_version_operations_other_check CHECK((kind IN ('split','merge'))=(other_item_id IS NOT NULL)),
 CONSTRAINT item_version_operations_undone_check CHECK(undone_by IS NULL OR undone_at IS NOT NULL)
);
CREATE INDEX item_version_operations_item_idx ON item_version_operations(item_id,created_at DESC);
CREATE INDEX item_version_operations_other_idx ON item_version_operations(other_item_id,created_at DESC) WHERE other_item_id IS NOT NULL;
CREATE INDEX item_version_operations_library_idx ON item_version_operations(library_id,created_at DESC);

-- "Not a version of this item": a file the administrator split off is never
-- grouped into that item again by catalog synchronisation, whether its
-- source survives or the file disappears and comes back.
CREATE TABLE item_version_exclusions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CONSTRAINT item_version_exclusions_path_check CHECK(octet_length(relative_path) BETWEEN 1 AND 1024),
 operation_id uuid REFERENCES item_version_operations(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 UNIQUE(item_id,root_id,relative_path)
);
CREATE INDEX item_version_exclusions_path_idx ON item_version_exclusions(root_id,relative_path);

-- The scan group of a merged item now names the target, so a file of the
-- absorbed group that appears later joins the target instead of recreating
-- the absorbed item.
CREATE TABLE catalog_scan_item_aliases (
 library_id uuid NOT NULL,
 kind text NOT NULL CONSTRAINT catalog_scan_item_aliases_kind_check CHECK(kind IN ('Movie','Series','Season','Episode')),
 group_digest bytea NOT NULL CONSTRAINT catalog_scan_item_aliases_digest_check CHECK(octet_length(group_digest)=32),
 item_id uuid NOT NULL,
 operation_id uuid REFERENCES item_version_operations(id) ON DELETE SET NULL,
 FOREIGN KEY(item_id,library_id,kind) REFERENCES items(id,library_id,kind) ON DELETE CASCADE,
 PRIMARY KEY(library_id,kind,group_digest)
);
CREATE INDEX catalog_scan_item_aliases_item_idx ON catalog_scan_item_aliases(item_id);

-- A scan source an administrator placed: synchronisation never takes its
-- file name as the item's title any more.
ALTER TABLE catalog_scan_sources ADD COLUMN manual boolean NOT NULL DEFAULT false;

-- The administrator's main version of an item: listed first and chosen
-- when a client does not name a version.
CREATE TABLE item_primary_versions (
 item_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 source_id uuid NOT NULL UNIQUE REFERENCES media_sources(id) ON DELETE CASCADE,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE
);

-- Track preferences of one user: the user's defaults (no item), one item
-- (every version) or one version (source). A null member inherits from the
-- broader level; a named track only exists per version, since stream
-- indexes and external tracks belong to one file.
CREATE TABLE user_track_preferences (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid REFERENCES items(id) ON DELETE CASCADE,
 source_id uuid REFERENCES media_sources(id) ON DELETE CASCADE,
 audio_language text CONSTRAINT user_track_preferences_audio_language_check CHECK(audio_language ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,2}$'),
 audio_commentary boolean,
 audio_track text CONSTRAINT user_track_preferences_audio_track_check CHECK(audio_track ~ '^(e:[0-9]{1,5}|x:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$'),
 subtitle_mode text CONSTRAINT user_track_preferences_subtitle_mode_check CHECK(subtitle_mode IN ('auto','always','forced','off')),
 subtitle_language text CONSTRAINT user_track_preferences_subtitle_language_check CHECK(subtitle_language ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,2}$'),
 subtitle_sdh boolean,
 subtitle_track text CONSTRAINT user_track_preferences_subtitle_track_check CHECK(subtitle_track ~ '^(e:[0-9]{1,5}|x:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$'),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CONSTRAINT user_track_preferences_updated_at_check CHECK(isfinite(updated_at)),
 CONSTRAINT user_track_preferences_scope_check CHECK(source_id IS NULL OR item_id IS NOT NULL),
 CONSTRAINT user_track_preferences_track_scope_check CHECK(source_id IS NOT NULL OR (audio_track IS NULL AND subtitle_track IS NULL)),
 CONSTRAINT user_track_preferences_values_check CHECK(num_nonnulls(audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track)>0)
);
CREATE UNIQUE INDEX user_track_preferences_user_idx ON user_track_preferences(user_id) WHERE item_id IS NULL;
CREATE UNIQUE INDEX user_track_preferences_item_idx ON user_track_preferences(user_id,item_id) WHERE item_id IS NOT NULL AND source_id IS NULL;
CREATE UNIQUE INDEX user_track_preferences_source_idx ON user_track_preferences(user_id,source_id) WHERE source_id IS NOT NULL;
CREATE INDEX user_track_preferences_item_cascade_idx ON user_track_preferences(item_id) WHERE item_id IS NOT NULL;
CREATE INDEX user_track_preferences_source_cascade_idx ON user_track_preferences(source_id) WHERE source_id IS NOT NULL;
COMMIT;
