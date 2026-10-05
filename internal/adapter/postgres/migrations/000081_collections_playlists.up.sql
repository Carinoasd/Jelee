BEGIN;
-- Collections and playlists (G02.1). Every read binds its member items to
-- the caller through the unified visibility filter (G48.3): a hidden item
-- never appears in a collection or a playlist, and a collection whose
-- members are all hidden is not listed to anyone but administrators.
-- docs/collections-playlists.md describes the model.

-- Collections are administered by administrators. Members are the manual
-- rows of collection_items and, when nfo_name is set, every item whose
-- collection metadata fact (NFO <set>/<collection>) carries that name,
-- compared trimmed and case-insensitively; those members follow the
-- metadata without being copied.
CREATE TABLE collections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL CONSTRAINT collections_name_check CHECK(octet_length(name) BETWEEN 1 AND 1024 AND btrim(name)<>''),
 overview text NOT NULL DEFAULT '' CONSTRAINT collections_overview_check CHECK(octet_length(overview)<=16384),
 nfo_name text CONSTRAINT collections_nfo_name_check CHECK(nfo_name IS NULL OR octet_length(nfo_name) BETWEEN 1 AND 1024 AND btrim(nfo_name)<>''),
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX collections_nfo_name_key ON collections(lower(btrim(nfo_name))) WHERE nfo_name IS NOT NULL;
CREATE INDEX collections_name_idx ON collections(lower(name),id);
CREATE TABLE collection_items (
 collection_id uuid NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 added_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(collection_id,item_id)
);
CREATE INDEX collection_items_item_idx ON collection_items(item_id);
-- Resolves the metadata members of an NFO-linked collection.
CREATE INDEX item_metadata_facts_collection_name_idx ON item_metadata_facts(lower(btrim(value->>'name'))) WHERE field='collection';

-- Playlists belong to one user, who alone changes them; a public playlist
-- is readable by every other user, each seeing only the items they may see.
-- Entries keep their order in position (ties broken by id) and may repeat
-- an item. Every change locks the playlist row first, so concurrent edits
-- of one playlist apply one after another. Deleting the owner deletes the
-- playlists (G07.7).
CREATE TABLE playlists (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name text NOT NULL CONSTRAINT playlists_name_check CHECK(octet_length(name) BETWEEN 1 AND 1024 AND btrim(name)<>''),
 public boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX playlists_owner_idx ON playlists(owner_id,lower(name),id);
CREATE INDEX playlists_public_idx ON playlists(lower(name),id) WHERE public;
CREATE TABLE playlist_items (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 playlist_id uuid NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 position integer NOT NULL CONSTRAINT playlist_items_position_check CHECK(position>=0),
 added_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX playlist_items_order_idx ON playlist_items(playlist_id,position,id);
CREATE INDEX playlist_items_item_idx ON playlist_items(item_id);
COMMIT;
