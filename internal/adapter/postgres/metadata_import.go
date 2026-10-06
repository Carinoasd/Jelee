package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Metadata import (G36.4). The whole document is streamed into a temporary
// staging table inside one transaction; nothing is applied before the
// trailer verified every byte. Identities are then resolved in SQL, so the
// process holds one line at a time however large the catalog:
//
//   - users by ID, then by case-insensitive name; libraries by ID, then name;
//     roots by ID, then path;
//   - items by ID, then by any media file, series/season folder or scan group
//     they own in the target;
//   - anything else keeps its exported ID.
//
// A resolved identity that disagrees with the document (a name held by
// another user, a root under another library, an item of another kind or at
// two locations) is a conflict. Without SkipConflicts any conflict aborts
// before a row is written; with it the conflicting records and everything
// that refers to them are skipped and counted. Import merges: it inserts and
// updates, never deletes, and running it twice changes nothing the second
// time. Items found in the target keep their catalog rows (files, folders,
// parents, scan state) and only receive the user data.

const metadataStageTables = `
CREATE TEMP TABLE bk_stage(seq bigint PRIMARY KEY,kind text NOT NULL,doc jsonb NOT NULL,ref uuid) ON COMMIT DROP;
CREATE TEMP TABLE bk_map(kind text NOT NULL,src uuid NOT NULL,dst uuid,state text NOT NULL DEFAULT 'pending',reason text,PRIMARY KEY(kind,src)) ON COMMIT DROP;
CREATE TEMP TABLE bk_touched(item_id uuid PRIMARY KEY) ON COMMIT DROP;
CREATE OR REPLACE FUNCTION pg_temp.bk_texts(v jsonb) RETURNS text[] LANGUAGE sql IMMUTABLE AS
 $$SELECT CASE WHEN v IS NULL OR jsonb_typeof(v)='null' THEN NULL ELSE ARRAY(SELECT e FROM jsonb_array_elements_text(v) WITH ORDINALITY t(e,n) ORDER BY n) END$$;
CREATE OR REPLACE FUNCTION pg_temp.bk_dst(k text,v text) RETURNS uuid LANGUAGE sql STABLE AS
 $$SELECT dst FROM bk_map WHERE kind=k AND src=v::uuid AND state IN ('existing','new')$$;`

// metadataResolve runs in order after staging. Each statement only reads
// identities resolved by earlier statements.
var metadataResolve = []string{
	`UPDATE bk_stage SET ref=(doc->>'id')::uuid WHERE kind IN ('library','library_root','user','item','media_source','item_directory_source','item_image','client_rule','library_network_rule','webhook','collection','playlist','playlist_item')`,
	`CREATE UNIQUE INDEX bk_stage_ref ON bk_stage(kind,ref) WHERE ref IS NOT NULL`,
	`CREATE INDEX bk_stage_kind ON bk_stage(kind)`,
	`CREATE INDEX bk_stage_item ON bk_stage(kind,((doc->>'item_id')::uuid)) WHERE kind IN ('media_source','item_directory_source','item_parent_link')`,
	`ANALYZE bk_stage`,
	`INSERT INTO bk_map(kind,src) SELECT kind,ref FROM bk_stage WHERE kind IN ('library','user','library_root','item','media_source')`,
	`ANALYZE bk_map`,
	// Libraries: ID, then exact name.
	`UPDATE bk_map m SET dst=l.id,state='existing' FROM libraries l WHERE m.kind='library' AND l.id=m.src`,
	`UPDATE bk_map m SET dst=l.id,state='existing' FROM bk_stage s JOIN libraries l ON l.name=s.doc->>'name' WHERE m.kind='library' AND m.state='pending' AND s.kind='library' AND s.ref=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='library_name_taken' FROM bk_stage s WHERE m.kind='library' AND m.state='existing' AND s.kind='library' AND s.ref=m.src
 AND EXISTS(SELECT 1 FROM libraries l WHERE l.name=s.doc->>'name' AND l.id<>m.dst)`,
	`UPDATE bk_map SET dst=src,state='new' WHERE kind='library' AND state='pending'`,
	metadataAmbiguous("library"),
	// Users: ID, then case-insensitive name.
	`UPDATE bk_map m SET dst=u.id,state='existing' FROM users u WHERE m.kind='user' AND u.id=m.src`,
	`UPDATE bk_map m SET dst=u.id,state='existing' FROM bk_stage s JOIN users u ON lower(u.name)=lower(s.doc->>'name') WHERE m.kind='user' AND m.state='pending' AND s.kind='user' AND s.ref=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='user_name_taken' FROM bk_stage s WHERE m.kind='user' AND m.state='existing' AND s.kind='user' AND s.ref=m.src
 AND EXISTS(SELECT 1 FROM users u WHERE lower(u.name)=lower(s.doc->>'name') AND u.id<>m.dst)`,
	`UPDATE bk_map SET dst=src,state='new' WHERE kind='user' AND state='pending'`,
	metadataAmbiguous("user"),
	// Roots: ID, then path; the root must sit under the resolved library at
	// the exported path.
	`UPDATE bk_map m SET dst=r.id,state='existing' FROM library_roots r WHERE m.kind='library_root' AND r.id=m.src`,
	`UPDATE bk_map m SET dst=r.id,state='existing' FROM bk_stage s JOIN library_roots r ON r.path=s.doc->>'path' WHERE m.kind='library_root' AND m.state='pending' AND s.kind='library_root' AND s.ref=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='root_path_changed' FROM bk_stage s,library_roots r WHERE m.kind='library_root' AND m.state='existing' AND s.kind='library_root' AND s.ref=m.src AND r.id=m.dst AND r.path<>s.doc->>'path'`,
	`UPDATE bk_map m SET state='conflict',reason='root_library_mismatch' FROM bk_stage s,library_roots r WHERE m.kind='library_root' AND m.state='existing' AND s.kind='library_root' AND s.ref=m.src AND r.id=m.dst
 AND r.library_id IS DISTINCT FROM pg_temp.bk_dst('library',s.doc->>'library_id')`,
	`UPDATE bk_map m SET dst=m.src,state='new' FROM bk_stage s WHERE m.kind='library_root' AND m.state='pending' AND s.kind='library_root' AND s.ref=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='dependency_conflict' FROM bk_stage s WHERE m.kind='library_root' AND m.state='new' AND s.kind='library_root' AND s.ref=m.src AND pg_temp.bk_dst('library',s.doc->>'library_id') IS NULL`,
	metadataAmbiguous("library_root"),
	// Items: where the target already holds the item's files, folders or
	// scan group, that is the item.
	`CREATE TEMP TABLE bk_item_loc ON COMMIT DROP AS
 SELECT (s.doc->>'item_id')::uuid src,ms.item_id dst FROM bk_stage s JOIN bk_map r ON r.kind='library_root' AND r.src=(s.doc->>'root_id')::uuid AND r.state='existing'
  JOIN media_sources ms ON ms.root_id=r.dst AND ms.relative_path=s.doc->>'relative_path' WHERE s.kind='media_source'
 UNION SELECT (s.doc->>'item_id')::uuid,d.item_id FROM bk_stage s JOIN bk_map r ON r.kind='library_root' AND r.src=(s.doc->>'root_id')::uuid AND r.state='existing'
  JOIN item_directory_sources d ON d.root_id=r.dst AND d.relative_path=s.doc->>'relative_path' WHERE s.kind='item_directory_source'
 UNION SELECT (s.doc->>'item_id')::uuid,c.item_id FROM bk_stage s JOIN bk_map l ON l.kind='library' AND l.src=(s.doc->>'library_id')::uuid AND l.state='existing'
  JOIN catalog_scan_items c ON c.library_id=l.dst AND c.kind=s.doc->>'kind' AND c.group_digest=(s.doc->>'group_digest')::bytea WHERE s.kind='catalog_scan_item'`,
	`ANALYZE bk_item_loc`,
	`UPDATE bk_map m SET dst=i.id,state='existing' FROM items i WHERE m.kind='item' AND i.id=m.src`,
	`WITH agg AS (SELECT src,count(DISTINCT dst) n,min(dst::text)::uuid one FROM bk_item_loc GROUP BY src)
 UPDATE bk_map m SET dst=CASE WHEN a.n=1 THEN a.one END,state=CASE WHEN a.n=1 THEN 'existing' ELSE 'conflict' END,reason=CASE WHEN a.n>1 THEN 'item_location_ambiguous' END
 FROM agg a WHERE m.kind='item' AND m.state='pending' AND a.src=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='item_location_conflict' WHERE m.kind='item' AND m.state='existing' AND EXISTS(SELECT 1 FROM bk_item_loc l WHERE l.src=m.src AND l.dst<>m.dst)`,
	`UPDATE bk_map m SET state='conflict',reason='item_identity_mismatch' FROM bk_stage s,items i WHERE m.kind='item' AND m.state='existing' AND s.kind='item' AND s.ref=m.src AND i.id=m.dst
 AND (i.kind<>s.doc->>'kind' OR i.library_id IS DISTINCT FROM pg_temp.bk_dst('library',s.doc->>'library_id'))`,
	`UPDATE bk_map SET dst=src,state='new' WHERE kind='item' AND state='pending'`,
	metadataAmbiguous("item"),
	// A new item needs its library and roots, and IDs nobody else holds.
	`ANALYZE bk_map`,
	`UPDATE bk_map m SET state='conflict',reason='dependency_conflict' FROM bk_stage s WHERE m.kind='item' AND m.state='new' AND s.kind='item' AND s.ref=m.src AND pg_temp.bk_dst('library',s.doc->>'library_id') IS NULL`,
	`WITH lost AS MATERIALIZED (SELECT DISTINCT (x.doc->>'item_id')::uuid item FROM bk_stage x WHERE x.kind IN ('media_source','item_directory_source')
 AND NOT EXISTS(SELECT 1 FROM bk_map r WHERE r.kind='library_root' AND r.src=(x.doc->>'root_id')::uuid AND r.state IN ('existing','new')))
 UPDATE bk_map m SET state='conflict',reason='dependency_conflict' FROM lost WHERE m.kind='item' AND m.state='new' AND m.src=lost.item`,
	`WITH taken AS MATERIALIZED (SELECT (x.doc->>'item_id')::uuid item FROM bk_stage x JOIN media_sources t ON t.id=x.ref WHERE x.kind='media_source'
 UNION SELECT (x.doc->>'item_id')::uuid FROM bk_stage x JOIN item_directory_sources t ON t.id=x.ref WHERE x.kind='item_directory_source')
 UPDATE bk_map m SET state='conflict',reason='source_id_taken' FROM taken WHERE m.kind='item' AND m.state='new' AND m.src=taken.item`,
	// Series, season, episode: a new child of an unusable parent is unusable.
	metadataParentConflict, metadataParentConflict, metadataParentConflict,
	// Media files: ID, then location; files of new items are new, files of
	// items found in the target are absent unless the target has them.
	`UPDATE bk_map m SET dst=t.id,state='existing' FROM media_sources t WHERE m.kind='media_source' AND t.id=m.src`,
	`UPDATE bk_map m SET dst=t.id,state='existing' FROM bk_stage s JOIN bk_map r ON r.kind='library_root' AND r.state='existing' JOIN media_sources t ON t.root_id=r.dst
 WHERE m.kind='media_source' AND m.state='pending' AND s.kind='media_source' AND s.ref=m.src AND r.src=(s.doc->>'root_id')::uuid AND t.relative_path=s.doc->>'relative_path'`,
	`UPDATE bk_map m SET dst=m.src,state='new' FROM bk_stage s JOIN bk_map i ON i.kind='item' AND i.state='new' WHERE m.kind='media_source' AND m.state='pending' AND s.kind='media_source' AND s.ref=m.src AND i.src=(s.doc->>'item_id')::uuid`,
	`UPDATE bk_map m SET state='absent' FROM bk_stage s JOIN bk_map i ON i.kind='item' AND i.state='existing' WHERE m.kind='media_source' AND m.state='pending' AND s.kind='media_source' AND s.ref=m.src AND i.src=(s.doc->>'item_id')::uuid`,
	`UPDATE bk_map SET state='unresolved' WHERE kind='media_source' AND state='pending'`,
	// Collections: ID, then the NFO collection name they link to, which is
	// unique in the target.
	`INSERT INTO bk_map(kind,src) SELECT kind,ref FROM bk_stage WHERE kind IN ('collection','playlist')`,
	`UPDATE bk_map m SET dst=c.id,state='existing' FROM collections c WHERE m.kind='collection' AND c.id=m.src`,
	`UPDATE bk_map m SET dst=c.id,state='existing' FROM bk_stage s JOIN collections c ON lower(btrim(c.nfo_name))=lower(btrim(s.doc->>'nfo_name'))
 WHERE m.kind='collection' AND m.state='pending' AND s.kind='collection' AND s.ref=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='collection_nfo_name_taken' FROM bk_stage s WHERE m.kind='collection' AND m.state='existing' AND s.kind='collection' AND s.ref=m.src
 AND EXISTS(SELECT 1 FROM collections c WHERE lower(btrim(c.nfo_name))=lower(btrim(s.doc->>'nfo_name')) AND c.id<>m.dst)`,
	`UPDATE bk_map SET dst=src,state='new' WHERE kind='collection' AND state='pending'`,
	metadataAmbiguous("collection"),
	// Playlists keep their ID and need their owner. A playlist already in
	// the target must belong to the same account.
	`UPDATE bk_map m SET state='unresolved' FROM bk_stage s WHERE m.kind='playlist' AND s.kind='playlist' AND s.ref=m.src AND pg_temp.bk_dst('user',s.doc->>'owner_id') IS NULL`,
	`UPDATE bk_map m SET dst=p.id,state='existing' FROM playlists p WHERE m.kind='playlist' AND m.state='pending' AND p.id=m.src`,
	`UPDATE bk_map m SET state='conflict',reason='playlist_owner_mismatch' FROM bk_stage s,playlists p WHERE m.kind='playlist' AND m.state='existing' AND s.kind='playlist' AND s.ref=m.src
 AND p.id=m.dst AND p.owner_id<>pg_temp.bk_dst('user',s.doc->>'owner_id')`,
	`UPDATE bk_map SET dst=src,state='new' WHERE kind='playlist' AND state='pending'`,
	`ANALYZE bk_map`,
}

// metadataAmbiguous refuses two exported records that became one target row.
func metadataAmbiguous(kind string) string {
	return `WITH dup AS MATERIALIZED (SELECT dst FROM bk_map WHERE kind='` + kind + `' AND dst IS NOT NULL AND state<>'pending' GROUP BY dst HAVING count(*)>1)
 UPDATE bk_map m SET state='conflict',reason='identity_ambiguous' FROM dup WHERE m.kind='` + kind + `' AND m.dst=dup.dst AND m.state IN ('existing','new')`
}

const metadataParentConflict = `WITH lost AS MATERIALIZED (SELECT (s.doc->>'item_id')::uuid item FROM bk_stage s WHERE s.kind='item_parent_link'
 AND NOT EXISTS(SELECT 1 FROM bk_map p WHERE p.kind='item' AND p.src=(s.doc->>'parent_id')::uuid AND p.state IN ('existing','new')))
 UPDATE bk_map m SET state='conflict',reason='dependency_conflict' FROM lost WHERE m.kind='item' AND m.state='new' AND m.src=lost.item`

// metadataApplyStep applies one record kind. Its statement returns the
// records it could use, how many it inserted and updated, and how many it
// set aside for reason special. The other unusable records name an
// identity that did not resolve.
type metadataApplyStep struct {
	kind    string
	special string
	sql     string
}

// Statements resolve an exported reference with pg_temp.bk_dst(kind, id),
// which yields the target ID or NULL. portableNFO holds when an NFO origin's
// item and root IDs mean the same rows in the target.
const portableNFO = `(m.dst=m.src AND (o.root IS NULL OR EXISTS(SELECT 1 FROM bk_map r WHERE r.kind='library_root' AND r.src::text=o.root AND r.dst=r.src AND r.state IN ('existing','new'))))`

var metadataApply = []metadataApplyStep{
	{kind: "library", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='library' AND m.src=s.ref WHERE s.kind='library' AND m.state IN ('existing','new')),
ins AS (INSERT INTO libraries(id,name,nfo_mode,metadata_language,metadata_image_languages,metadata_preferences_revision,catalog_sync_auto)
 SELECT id,doc->>'name',doc->>'nfo_mode',doc->>'metadata_language',pg_temp.bk_texts(doc->'metadata_image_languages'),(doc->>'metadata_preferences_revision')::integer,(doc->>'catalog_sync_auto')::boolean FROM x WHERE state='new' RETURNING 1),
upd AS (UPDATE libraries l SET name=x.doc->>'name',nfo_mode=x.doc->>'nfo_mode',
 nfo_generation=l.nfo_generation+CASE WHEN l.nfo_mode<>x.doc->>'nfo_mode' THEN 1 ELSE 0 END,
 metadata_language=x.doc->>'metadata_language',metadata_image_languages=pg_temp.bk_texts(x.doc->'metadata_image_languages'),
 metadata_preferences_revision=l.metadata_preferences_revision+CASE WHEN (l.metadata_language,l.metadata_image_languages) IS DISTINCT FROM (x.doc->>'metadata_language',pg_temp.bk_texts(x.doc->'metadata_image_languages')) THEN 1 ELSE 0 END,
 catalog_sync_auto=(x.doc->>'catalog_sync_auto')::boolean
 FROM x WHERE x.state='existing' AND l.id=x.id AND (l.name,l.nfo_mode,l.metadata_language,l.metadata_image_languages,l.catalog_sync_auto)
 IS DISTINCT FROM (x.doc->>'name',x.doc->>'nfo_mode',x.doc->>'metadata_language',pg_temp.bk_texts(x.doc->'metadata_image_languages'),(x.doc->>'catalog_sync_auto')::boolean) RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),(SELECT count(*) FROM upd),0`},
	{kind: "library_root", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='library_root' AND m.src=s.ref WHERE s.kind='library_root' AND m.state IN ('existing','new')),
ins AS (INSERT INTO library_roots(id,library_id,path) SELECT id,pg_temp.bk_dst('library',doc->>'library_id'),doc->>'path' FROM x WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	// A stored password hash is applied to new accounts and to accounts
	// without one; an existing password is never replaced. Any change to an
	// existing account invalidates its sessions like an administrator edit.
	{kind: "user", special: "", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='user' AND m.src=s.ref WHERE s.kind='user' AND m.state IN ('existing','new')),
ins AS (INSERT INTO users(id,name,is_admin,disabled,hidden,display_name,locale,created_at,deleted_at,allow_native,max_streams,max_kbps,parental_rating_max,block_unrated,content_filtered,password_hash)
 SELECT id,doc->>'name',(doc->>'is_admin')::boolean,(doc->>'disabled')::boolean,(doc->>'hidden')::boolean,doc->>'display_name',doc->>'locale',(doc->>'created_at')::timestamptz,(doc->>'deleted_at')::timestamptz,
 (doc->>'allow_native')::boolean,(doc->>'max_streams')::integer,(doc->>'max_kbps')::bigint,(doc->>'parental_rating_max')::smallint,(doc->>'block_unrated')::boolean,(doc->>'content_filtered')::boolean,
 CASE WHEN $1 THEN doc->>'password_hash' END FROM x WHERE state='new' RETURNING 1),
upd AS (UPDATE users u SET name=x.doc->>'name',is_admin=(x.doc->>'is_admin')::boolean,disabled=(x.doc->>'disabled')::boolean,hidden=(x.doc->>'hidden')::boolean,
 display_name=x.doc->>'display_name',locale=x.doc->>'locale',deleted_at=(x.doc->>'deleted_at')::timestamptz,allow_native=(x.doc->>'allow_native')::boolean,
 max_streams=(x.doc->>'max_streams')::integer,max_kbps=(x.doc->>'max_kbps')::bigint,parental_rating_max=(x.doc->>'parental_rating_max')::smallint,
 block_unrated=(x.doc->>'block_unrated')::boolean,content_filtered=o.content_filtered OR (x.doc->>'content_filtered')::boolean,
 password_hash=COALESCE(o.password_hash,CASE WHEN $1 THEN x.doc->>'password_hash' END),auth_version=o.auth_version+1
 FROM x JOIN users o ON o.id=x.id WHERE x.state='existing' AND u.id=x.id AND
 (o.name,o.is_admin,o.disabled,o.hidden,o.display_name,o.locale,o.deleted_at,o.allow_native,o.max_streams,o.max_kbps,o.parental_rating_max,o.block_unrated,o.content_filtered,o.password_hash)
 IS DISTINCT FROM (x.doc->>'name',(x.doc->>'is_admin')::boolean,(x.doc->>'disabled')::boolean,(x.doc->>'hidden')::boolean,x.doc->>'display_name',x.doc->>'locale',(x.doc->>'deleted_at')::timestamptz,
 (x.doc->>'allow_native')::boolean,(x.doc->>'max_streams')::integer,(x.doc->>'max_kbps')::bigint,(x.doc->>'parental_rating_max')::smallint,(x.doc->>'block_unrated')::boolean,
 o.content_filtered OR (x.doc->>'content_filtered')::boolean,COALESCE(o.password_hash,CASE WHEN $1 THEN x.doc->>'password_hash' END))
 RETURNING u.id),
rev AS (UPDATE sessions SET revoked_at=now() WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM upd) RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),(SELECT count(*) FROM upd),0`},
	{kind: "library_acl", sql: `WITH x AS (SELECT pg_temp.bk_dst('user',s.doc->>'user_id') u,pg_temp.bk_dst('library',s.doc->>'library_id') l FROM bk_stage s WHERE s.kind='library_acl'),
ok AS (SELECT * FROM x WHERE u IS NOT NULL AND l IS NOT NULL),
ins AS (INSERT INTO library_acl(user_id,library_id) SELECT u,l FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	{kind: "item", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=s.ref WHERE s.kind='item' AND m.state IN ('existing','new')),
ins AS (INSERT INTO items(id,library_id,title,kind) SELECT id,pg_temp.bk_dst('library',doc->>'library_id'),doc->>'title',doc->>'kind' FROM x WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	{kind: "media_source", special: "source_absent", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='media_source' AND m.src=s.ref WHERE s.kind='media_source' AND m.state IN ('existing','new')),
ins AS (INSERT INTO media_sources(id,item_id,library_id,root_id,relative_path,content_type)
 SELECT id,pg_temp.bk_dst('item',doc->>'item_id'),pg_temp.bk_dst('library',doc->>'library_id'),pg_temp.bk_dst('library_root',doc->>'root_id'),doc->>'relative_path',doc->>'content_type' FROM x WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,(SELECT count(*) FROM bk_map WHERE kind='media_source' AND state='absent')`},
	// Catalog structure is written for new items only; an item found in the
	// target keeps the folder, parent and scan state the target gave it.
	{kind: "item_directory_source", sql: `WITH x AS (SELECT s.ref id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid WHERE s.kind='item_directory_source' AND m.state IN ('existing','new')),
ins AS (INSERT INTO item_directory_sources(id,item_id,library_id,kind,root_id,relative_path)
 SELECT id,pg_temp.bk_dst('item',doc->>'item_id'),pg_temp.bk_dst('library',doc->>'library_id'),doc->>'kind',pg_temp.bk_dst('library_root',doc->>'root_id'),doc->>'relative_path' FROM x WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	{kind: "item_parent_link", sql: `WITH x AS (SELECT m.state,s.doc,pg_temp.bk_dst('item',s.doc->>'parent_id') parent FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid WHERE s.kind='item_parent_link' AND m.state IN ('existing','new')),
ok AS (SELECT * FROM x WHERE parent IS NOT NULL),
ins AS (INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind)
 SELECT pg_temp.bk_dst('item',doc->>'item_id'),pg_temp.bk_dst('library',doc->>'library_id'),doc->>'item_kind',parent,doc->>'parent_kind' FROM ok WHERE state='new' ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	{kind: "catalog_scan_item", sql: `WITH x AS (SELECT m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid WHERE s.kind='catalog_scan_item' AND m.state IN ('existing','new')),
ins AS (INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end)
 SELECT pg_temp.bk_dst('item',doc->>'item_id'),pg_temp.bk_dst('library',doc->>'library_id'),doc->>'kind',(doc->>'group_digest')::bytea,doc->>'parser_version',doc->>'scan_title',
 (doc->>'year')::integer,(doc->>'season')::integer,(doc->>'episode')::integer,(doc->>'episode_end')::integer FROM x WHERE state='new' ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	{kind: "catalog_scan_source", sql: `WITH x AS (SELECT m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='media_source' AND m.src=(s.doc->>'source_id')::uuid WHERE s.kind='catalog_scan_source' AND m.state IN ('existing','new')),
ins AS (INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version,missing_since,manual)
 SELECT pg_temp.bk_dst('media_source',doc->>'source_id'),pg_temp.bk_dst('library',doc->>'library_id'),pg_temp.bk_dst('item',doc->>'item_id'),pg_temp.bk_dst('library_root',doc->>'root_id'),doc->>'relative_path',
 (doc->>'size')::bigint,(doc->>'modified_unix_nano')::bigint,doc->>'parser_version',(doc->>'missing_since')::timestamptz,COALESCE((doc->>'manual')::boolean,false) FROM x WHERE state='new' ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	// Version decisions (G20.3) are administrator choices, so unlike scan
	// state they also reach items found in the target; a target that already
	// decided keeps its own decision.
	{kind: "catalog_scan_item_alias", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('library',s.doc->>'library_id') l,pg_temp.bk_dst('item',s.doc->>'item_id') i FROM bk_stage s WHERE s.kind='catalog_scan_item_alias'),
ok AS (SELECT * FROM x WHERE l IS NOT NULL AND i IS NOT NULL AND EXISTS(SELECT 1 FROM items t WHERE t.id=x.i AND t.library_id=x.l AND t.kind=x.doc->>'kind')),
ins AS (INSERT INTO catalog_scan_item_aliases(library_id,kind,group_digest,item_id) SELECT l,doc->>'kind',(doc->>'group_digest')::bytea,i FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	{kind: "item_version_exclusion", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('library',s.doc->>'library_id') l,pg_temp.bk_dst('item',s.doc->>'item_id') i,pg_temp.bk_dst('library_root',s.doc->>'root_id') r FROM bk_stage s WHERE s.kind='item_version_exclusion'),
ok AS (SELECT * FROM x WHERE l IS NOT NULL AND i IS NOT NULL AND r IS NOT NULL),
ins AS (INSERT INTO item_version_exclusions(item_id,library_id,root_id,relative_path,created_at) SELECT i,l,r,doc->>'relative_path',(doc->>'created_at')::timestamptz FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	{kind: "item_primary_version", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('library',s.doc->>'library_id') l,pg_temp.bk_dst('item',s.doc->>'item_id') i,pg_temp.bk_dst('media_source',s.doc->>'source_id') m FROM bk_stage s WHERE s.kind='item_primary_version'),
ok AS (SELECT * FROM x WHERE l IS NOT NULL AND i IS NOT NULL AND EXISTS(SELECT 1 FROM media_sources t WHERE t.id=x.m AND t.item_id=x.i)),
ins AS (INSERT INTO item_primary_versions(item_id,library_id,source_id,updated_at) SELECT i,l,m,(doc->>'updated_at')::timestamptz FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	// New items take the exported revision; items found in the target get a
	// state row when their metadata arrives and one revision bump at the end.
	{kind: "item_metadata_state", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid WHERE s.kind='item_metadata_state' AND m.state IN ('existing','new')),
ins AS (INSERT INTO item_metadata_state(item_id,revision) SELECT id,(doc->>'revision')::integer FROM x WHERE state='new' ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	// NFO provenance names source and root IDs; it is only kept where those
	// IDs mean the same thing in the target. Elsewhere an NFO refresh
	// rebuilds it.
	{kind: "item_metadata_field", special: "nfo_origin_not_portable", sql: `WITH x AS (SELECT m.dst item,s.doc,(s.doc->>'source'<>'nfo' OR ` + portableNFO + `) portable
 FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid CROSS JOIN LATERAL (SELECT s.doc->'nfo_origin'->>'rootId' root) o
 WHERE s.kind='item_metadata_field' AND m.state IN ('existing','new')),
ok AS (SELECT * FROM x WHERE portable),
st AS (INSERT INTO item_metadata_state(item_id,revision) SELECT DISTINCT item,2 FROM ok ON CONFLICT DO NOTHING RETURNING 1),
up AS (INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at,nfo_origin)
 SELECT item,doc->>'field',doc->>'value',doc->>'source',(doc->>'locked')::boolean,(doc->>'updated_at')::timestamptz,doc->>'provider_resource',(doc->>'provider_id')::integer,
 doc->>'provider_source_url',doc->>'provider_language',(doc->>'provider_fetched_at')::timestamptz,NULLIF(doc->'nfo_origin','null'::jsonb) FROM ok
 ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value,source=EXCLUDED.source,locked=EXCLUDED.locked,updated_at=EXCLUDED.updated_at,provider_resource=EXCLUDED.provider_resource,
 provider_id=EXCLUDED.provider_id,provider_source_url=EXCLUDED.provider_source_url,provider_language=EXCLUDED.provider_language,provider_fetched_at=EXCLUDED.provider_fetched_at,nfo_origin=EXCLUDED.nfo_origin
 WHERE (item_metadata_fields.value,item_metadata_fields.source,item_metadata_fields.locked,item_metadata_fields.updated_at,item_metadata_fields.provider_resource,item_metadata_fields.provider_id,
 item_metadata_fields.provider_source_url,item_metadata_fields.provider_language,item_metadata_fields.provider_fetched_at,item_metadata_fields.nfo_origin)
 IS DISTINCT FROM (EXCLUDED.value,EXCLUDED.source,EXCLUDED.locked,EXCLUDED.updated_at,EXCLUDED.provider_resource,EXCLUDED.provider_id,EXCLUDED.provider_source_url,EXCLUDED.provider_language,EXCLUDED.provider_fetched_at,EXCLUDED.nfo_origin)
 RETURNING item_id,(xmax=0) inserted),
t AS (INSERT INTO bk_touched SELECT DISTINCT item_id FROM up ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),(SELECT count(*) FROM x WHERE NOT portable)`},
	{kind: "item_metadata_fact", special: "nfo_origin_not_portable", sql: `WITH x AS (SELECT m.dst item,s.doc,(s.doc->>'source'<>'nfo' OR ` + portableNFO + `) portable
 FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid CROSS JOIN LATERAL (SELECT s.doc->'nfo_origin'->>'rootId' root) o
 WHERE s.kind='item_metadata_fact' AND m.state IN ('existing','new')),
ok AS (SELECT * FROM x WHERE portable),
up AS (INSERT INTO item_metadata_facts(item_id,field,value,source,locked,updated_at,nfo_origin)
 SELECT item,doc->>'field',doc->'value',doc->>'source',(doc->>'locked')::boolean,(doc->>'updated_at')::timestamptz,NULLIF(doc->'nfo_origin','null'::jsonb) FROM ok
 ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value,source=EXCLUDED.source,locked=EXCLUDED.locked,updated_at=EXCLUDED.updated_at,nfo_origin=EXCLUDED.nfo_origin
 WHERE (item_metadata_facts.value,item_metadata_facts.source,item_metadata_facts.locked,item_metadata_facts.updated_at,item_metadata_facts.nfo_origin)
 IS DISTINCT FROM (EXCLUDED.value,EXCLUDED.source,EXCLUDED.locked,EXCLUDED.updated_at,EXCLUDED.nfo_origin)
 RETURNING item_id,(xmax=0) inserted),
t AS (INSERT INTO bk_touched SELECT DISTINCT item_id FROM up ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),(SELECT count(*) FROM x WHERE NOT portable)`},
	{kind: "item_nfo_field_lock", special: "nfo_origin_not_portable", sql: `WITH x AS (SELECT m.dst item,s.doc,` + portableNFO + ` portable
 FROM bk_stage s JOIN bk_map m ON m.kind='item' AND m.src=(s.doc->>'item_id')::uuid CROSS JOIN LATERAL (SELECT s.doc->'origin'->>'rootId' root) o
 WHERE s.kind='item_nfo_field_lock' AND m.state IN ('existing','new')),
ok AS (SELECT * FROM x WHERE portable),
up AS (INSERT INTO item_nfo_field_locks(item_id,field,origin) SELECT item,doc->>'field',doc->'origin' FROM ok
 ON CONFLICT(item_id,field) DO UPDATE SET origin=EXCLUDED.origin WHERE item_nfo_field_locks.origin IS DISTINCT FROM EXCLUDED.origin
 RETURNING item_id,(xmax=0) inserted),
t AS (INSERT INTO bk_touched SELECT DISTINCT item_id FROM up ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),(SELECT count(*) FROM x WHERE NOT portable)`},
	// A slot holds one locked row; a target that locked another source for
	// the slot keeps its choice.
	{kind: "item_image", special: "image_slot_locked", sql: `WITH x AS (SELECT s.ref,s.doc,pg_temp.bk_dst('item',s.doc->>'item_id') item,pg_temp.bk_dst('library',s.doc->>'library_id') lib,
 pg_temp.bk_dst('library_root',s.doc->>'root_id') root FROM bk_stage s WHERE s.kind='item_image'),
usable AS (SELECT *,EXISTS(SELECT 1 FROM item_images g WHERE g.item_id=x.item AND g.image_type=x.doc->>'image_type' AND g.image_index=(x.doc->>'image_index')::integer AND g.locked AND g.source_kind<>x.doc->>'source_kind') blocked
 FROM x WHERE item IS NOT NULL AND lib IS NOT NULL AND (doc->>'root_id' IS NULL OR root IS NOT NULL)),
ok AS (SELECT * FROM usable WHERE NOT blocked),
up AS (INSERT INTO item_images(id,item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,width,height,format,byte_size,average_color,fetched_at,source_mtime_unix_nano,source_size,locked,created_at,updated_at)
 SELECT CASE WHEN EXISTS(SELECT 1 FROM item_images g WHERE g.id=ok.ref) THEN gen_random_uuid() ELSE ok.ref END,item,lib,doc->>'image_type',(doc->>'image_index')::integer,doc->>'source_kind',root,doc->>'relative_path',doc->>'remote_url',
 (doc->>'content_sha256')::bytea,(doc->>'width')::integer,(doc->>'height')::integer,doc->>'format',(doc->>'byte_size')::bigint,(doc->>'average_color')::integer,(doc->>'fetched_at')::timestamptz,
 (doc->>'source_mtime_unix_nano')::bigint,(doc->>'source_size')::bigint,true,(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz FROM ok
 ON CONFLICT(item_id,image_type,image_index,source_kind) DO UPDATE SET root_id=EXCLUDED.root_id,relative_path=EXCLUDED.relative_path,remote_url=EXCLUDED.remote_url,content_sha256=EXCLUDED.content_sha256,
 width=EXCLUDED.width,height=EXCLUDED.height,format=EXCLUDED.format,byte_size=EXCLUDED.byte_size,average_color=EXCLUDED.average_color,fetched_at=EXCLUDED.fetched_at,
 source_mtime_unix_nano=EXCLUDED.source_mtime_unix_nano,source_size=EXCLUDED.source_size,locked=true,updated_at=greatest(EXCLUDED.updated_at,item_images.created_at)
 WHERE (item_images.root_id,item_images.relative_path,item_images.remote_url,item_images.content_sha256,item_images.width,item_images.height,item_images.format,item_images.byte_size,item_images.average_color,
 item_images.fetched_at,item_images.source_mtime_unix_nano,item_images.source_size,item_images.locked)
 IS DISTINCT FROM (EXCLUDED.root_id,EXCLUDED.relative_path,EXCLUDED.remote_url,EXCLUDED.content_sha256,EXCLUDED.width,EXCLUDED.height,EXCLUDED.format,EXCLUDED.byte_size,EXCLUDED.average_color,
 EXCLUDED.fetched_at,EXCLUDED.source_mtime_unix_nano,EXCLUDED.source_size,true)
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),(SELECT count(*) FROM usable WHERE blocked)`},
	// Progress keeps whichever side was updated last, so importing an old
	// file into a live server never rewinds newer playback.
	{kind: "user_item_data", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('user',s.doc->>'user_id') u,pg_temp.bk_dst('item',s.doc->>'item_id') i FROM bk_stage s WHERE s.kind='user_item_data'),
ok AS (SELECT * FROM x WHERE u IS NOT NULL AND i IS NOT NULL),
up AS (INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at)
 SELECT u,i,(doc->>'resume_ticks')::bigint,(doc->>'played')::boolean,(doc->>'play_count')::integer,(doc->>'last_played_at')::timestamptz,pg_temp.bk_dst('media_source',doc->>'last_source_id'),(doc->>'updated_at')::timestamptz FROM ok
 ON CONFLICT(user_id,item_id) DO UPDATE SET resume_ticks=EXCLUDED.resume_ticks,played=EXCLUDED.played,play_count=EXCLUDED.play_count,last_played_at=EXCLUDED.last_played_at,last_source_id=EXCLUDED.last_source_id,updated_at=EXCLUDED.updated_at
 WHERE EXCLUDED.updated_at>user_item_data.updated_at
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	// Like progress, a preference keeps whichever side was updated last; a
	// version level needs its version on its item in the target.
	{kind: "user_track_preference", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('user',s.doc->>'user_id') u,pg_temp.bk_dst('item',s.doc->>'item_id') i,pg_temp.bk_dst('media_source',s.doc->>'source_id') m FROM bk_stage s WHERE s.kind='user_track_preference'),
ok AS (SELECT * FROM x WHERE u IS NOT NULL AND (doc->>'item_id' IS NULL OR i IS NOT NULL) AND (doc->>'source_id' IS NULL OR EXISTS(SELECT 1 FROM media_sources t WHERE t.id=x.m AND t.item_id=x.i))),
v AS (SELECT u,CASE WHEN doc->>'item_id' IS NULL THEN NULL ELSE i END i,CASE WHEN doc->>'source_id' IS NULL THEN NULL ELSE m END m,doc->>'audio_language' al,(doc->>'audio_commentary')::boolean ac,doc->>'audio_track' atr,
 doc->>'subtitle_mode' sm,doc->>'subtitle_language' sl,(doc->>'subtitle_sdh')::boolean ss,doc->>'subtitle_track' st,(doc->>'updated_at')::timestamptz ts FROM ok),
up_user AS (INSERT INTO user_track_preferences AS p(user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track,updated_at)
 SELECT u,i,m,al,ac,atr,sm,sl,ss,st,ts FROM v WHERE i IS NULL ON CONFLICT(user_id) WHERE item_id IS NULL DO UPDATE SET audio_language=EXCLUDED.audio_language,audio_commentary=EXCLUDED.audio_commentary,
 subtitle_mode=EXCLUDED.subtitle_mode,subtitle_language=EXCLUDED.subtitle_language,subtitle_sdh=EXCLUDED.subtitle_sdh,updated_at=EXCLUDED.updated_at WHERE EXCLUDED.updated_at>p.updated_at RETURNING (xmax=0) inserted),
up_item AS (INSERT INTO user_track_preferences AS p(user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track,updated_at)
 SELECT u,i,m,al,ac,atr,sm,sl,ss,st,ts FROM v WHERE i IS NOT NULL AND m IS NULL ON CONFLICT(user_id,item_id) WHERE item_id IS NOT NULL AND source_id IS NULL DO UPDATE SET audio_language=EXCLUDED.audio_language,audio_commentary=EXCLUDED.audio_commentary,
 subtitle_mode=EXCLUDED.subtitle_mode,subtitle_language=EXCLUDED.subtitle_language,subtitle_sdh=EXCLUDED.subtitle_sdh,updated_at=EXCLUDED.updated_at WHERE EXCLUDED.updated_at>p.updated_at RETURNING (xmax=0) inserted),
up_version AS (INSERT INTO user_track_preferences AS p(user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track,updated_at)
 SELECT u,i,m,al,ac,atr,sm,sl,ss,st,ts FROM v WHERE m IS NOT NULL ON CONFLICT(user_id,source_id) WHERE source_id IS NOT NULL DO UPDATE SET item_id=EXCLUDED.item_id,audio_language=EXCLUDED.audio_language,audio_commentary=EXCLUDED.audio_commentary,
 audio_track=EXCLUDED.audio_track,subtitle_mode=EXCLUDED.subtitle_mode,subtitle_language=EXCLUDED.subtitle_language,subtitle_sdh=EXCLUDED.subtitle_sdh,subtitle_track=EXCLUDED.subtitle_track,updated_at=EXCLUDED.updated_at
 WHERE EXCLUDED.updated_at>p.updated_at RETURNING (xmax=0) inserted),
up AS (SELECT inserted FROM up_user UNION ALL SELECT inserted FROM up_item UNION ALL SELECT inserted FROM up_version)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	{kind: "access_policy", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='access_policy'),
upd AS (UPDATE access_policy p SET restrict_admins=(x.doc->>'restrict_admins')::boolean,block_unrated=(x.doc->>'block_unrated')::boolean,updated_at=now() FROM x
 WHERE p.id AND (p.restrict_admins,p.block_unrated) IS DISTINCT FROM ((x.doc->>'restrict_admins')::boolean,(x.doc->>'block_unrated')::boolean) RETURNING 1)
SELECT (SELECT count(*) FROM x),0,(SELECT count(*) FROM upd),0`},
	{kind: "parental_rating", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='parental_rating'),
up AS (INSERT INTO parental_ratings(code,level) SELECT doc->>'code',(doc->>'level')::smallint FROM x
 ON CONFLICT(code) DO UPDATE SET level=EXCLUDED.level WHERE parental_ratings.level<>EXCLUDED.level RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	{kind: "user_item_access_rule", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('user',s.doc->>'user_id') u,pg_temp.bk_dst('item',s.doc->>'item_id') i FROM bk_stage s WHERE s.kind='user_item_access_rule'),
ok AS (SELECT * FROM x WHERE u IS NOT NULL AND i IS NOT NULL),
up AS (INSERT INTO user_item_access_rules(user_id,item_id,effect,created_at) SELECT u,i,doc->>'effect',(doc->>'created_at')::timestamptz FROM ok
 ON CONFLICT(user_id,item_id) DO UPDATE SET effect=EXCLUDED.effect WHERE user_item_access_rules.effect<>EXCLUDED.effect RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	{kind: "user_blocked_tag", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('user',s.doc->>'user_id') u FROM bk_stage s WHERE s.kind='user_blocked_tag'),
ok AS (SELECT * FROM x WHERE u IS NOT NULL),
ins AS (INSERT INTO user_blocked_tags(user_id,tag) SELECT u,doc->>'tag' FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	{kind: "client_control_policy", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='client_control_policy'),
upd AS (UPDATE client_control_policy p SET unknown_clients=x.doc->>'unknown_clients',exempt_admins=(x.doc->>'exempt_admins')::boolean,exempt_loopback=(x.doc->>'exempt_loopback')::boolean,version=p.version+1,updated_at=now() FROM x
 WHERE p.id AND (p.unknown_clients,p.exempt_admins,p.exempt_loopback) IS DISTINCT FROM (x.doc->>'unknown_clients',(x.doc->>'exempt_admins')::boolean,(x.doc->>'exempt_loopback')::boolean) RETURNING 1)
SELECT (SELECT count(*) FROM x),0,(SELECT count(*) FROM upd),0`},
	// Hit counters start over; the rule author is kept when that account
	// came along.
	{kind: "client_rule", sql: `WITH x AS (SELECT s.ref,s.doc FROM bk_stage s WHERE s.kind='client_rule'),
up AS (INSERT INTO client_rules(id,dimension,header_name,match_kind,pattern,case_fold,priority,action,intent,rate_requests,rate_period_seconds,scope_kind,scope_values,window_from,window_until,daily_start,daily_end,weekdays,time_zone,enabled,note,created_by,created_at,updated_at,libraries)
 SELECT ref,doc->>'dimension',doc->>'header_name',doc->>'match_kind',doc->>'pattern',(doc->>'case_fold')::boolean,(doc->>'priority')::integer,doc->>'action',doc->>'intent',(doc->>'rate_requests')::integer,(doc->>'rate_period_seconds')::integer,
 doc->>'scope_kind',pg_temp.bk_texts(doc->'scope_values'),(doc->>'window_from')::timestamptz,(doc->>'window_until')::timestamptz,doc->>'daily_start',doc->>'daily_end',pg_temp.bk_texts(doc->'weekdays')::smallint[],doc->>'time_zone',
 (doc->>'enabled')::boolean,doc->>'note',pg_temp.bk_dst('user',doc->>'created_by'),(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz,
 ARRAY(SELECT COALESCE(pg_temp.bk_dst('library',v),v::uuid) FROM jsonb_array_elements_text(COALESCE(doc->'libraries','[]'::jsonb)) v) FROM x
 ON CONFLICT(id) DO UPDATE SET dimension=EXCLUDED.dimension,header_name=EXCLUDED.header_name,match_kind=EXCLUDED.match_kind,pattern=EXCLUDED.pattern,case_fold=EXCLUDED.case_fold,priority=EXCLUDED.priority,
 action=EXCLUDED.action,intent=EXCLUDED.intent,rate_requests=EXCLUDED.rate_requests,rate_period_seconds=EXCLUDED.rate_period_seconds,scope_kind=EXCLUDED.scope_kind,scope_values=EXCLUDED.scope_values,
 window_from=EXCLUDED.window_from,window_until=EXCLUDED.window_until,daily_start=EXCLUDED.daily_start,daily_end=EXCLUDED.daily_end,weekdays=EXCLUDED.weekdays,time_zone=EXCLUDED.time_zone,
 enabled=EXCLUDED.enabled,note=EXCLUDED.note,libraries=EXCLUDED.libraries,updated_at=greatest(EXCLUDED.updated_at,client_rules.created_at)
 WHERE (client_rules.dimension,client_rules.header_name,client_rules.match_kind,client_rules.pattern,client_rules.case_fold,client_rules.priority,client_rules.action,client_rules.intent,client_rules.rate_requests,
 client_rules.rate_period_seconds,client_rules.scope_kind,client_rules.scope_values,client_rules.window_from,client_rules.window_until,client_rules.daily_start,client_rules.daily_end,client_rules.weekdays,
 client_rules.time_zone,client_rules.enabled,client_rules.note,client_rules.libraries)
 IS DISTINCT FROM (EXCLUDED.dimension,EXCLUDED.header_name,EXCLUDED.match_kind,EXCLUDED.pattern,EXCLUDED.case_fold,EXCLUDED.priority,EXCLUDED.action,EXCLUDED.intent,EXCLUDED.rate_requests,
 EXCLUDED.rate_period_seconds,EXCLUDED.scope_kind,EXCLUDED.scope_values,EXCLUDED.window_from,EXCLUDED.window_until,EXCLUDED.daily_start,EXCLUDED.daily_end,EXCLUDED.weekdays,EXCLUDED.time_zone,EXCLUDED.enabled,EXCLUDED.note,EXCLUDED.libraries)
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	// A network rule follows its library; a library that did not come along
	// leaves its rule out (G48.5).
	{kind: "library_network_rule", sql: `WITH x AS (SELECT s.ref,s.doc,pg_temp.bk_dst('library',s.doc->>'library_id') l FROM bk_stage s WHERE s.kind='library_network_rule'),
ok AS (SELECT * FROM x WHERE l IS NOT NULL),
up AS (INSERT INTO library_network_rules(id,library_id,network,cidrs,client_kinds,include_admins,enabled,note,created_by,created_at,updated_at)
 SELECT ref,l,doc->>'network',pg_temp.bk_texts(doc->'cidrs')::cidr[],pg_temp.bk_texts(doc->'client_kinds'),(doc->>'include_admins')::boolean,(doc->>'enabled')::boolean,doc->>'note',
 pg_temp.bk_dst('user',doc->>'created_by'),(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz FROM ok
 ON CONFLICT(id) DO UPDATE SET library_id=EXCLUDED.library_id,network=EXCLUDED.network,cidrs=EXCLUDED.cidrs,client_kinds=EXCLUDED.client_kinds,include_admins=EXCLUDED.include_admins,
 enabled=EXCLUDED.enabled,note=EXCLUDED.note,updated_at=greatest(EXCLUDED.updated_at,library_network_rules.created_at)
 WHERE (library_network_rules.library_id,library_network_rules.network,library_network_rules.cidrs,library_network_rules.client_kinds,library_network_rules.include_admins,library_network_rules.enabled,library_network_rules.note)
 IS DISTINCT FROM (EXCLUDED.library_id,EXCLUDED.network,EXCLUDED.cidrs,EXCLUDED.client_kinds,EXCLUDED.include_admins,EXCLUDED.enabled,EXCLUDED.note)
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),(SELECT count(*) FROM x)-(SELECT count(*) FROM ok)`},
	{kind: "webhook", sql: `WITH x AS (SELECT s.ref,s.doc FROM bk_stage s WHERE s.kind='webhook'),
up AS (INSERT INTO webhooks(id,name,url,enabled,events,header_names,headers_sealed,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed,previous_secret_sealed,previous_until,created_at,updated_at)
 SELECT ref,doc->>'name',doc->>'url',(doc->>'enabled')::boolean,pg_temp.bk_texts(doc->'events'),pg_temp.bk_texts(doc->'header_names'),(doc->>'headers_sealed')::bytea,(doc->>'timeout_ms')::integer,
 (doc->>'max_attempts')::integer,(doc->>'base_delay_ms')::bigint,(doc->>'max_delay_ms')::bigint,(doc->>'jitter')::double precision,(doc->>'secret_sealed')::bytea,(doc->>'previous_secret_sealed')::bytea,
 (doc->>'previous_until')::timestamptz,(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz FROM x
 ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,url=EXCLUDED.url,enabled=EXCLUDED.enabled,events=EXCLUDED.events,header_names=EXCLUDED.header_names,headers_sealed=EXCLUDED.headers_sealed,
 timeout_ms=EXCLUDED.timeout_ms,max_attempts=EXCLUDED.max_attempts,base_delay_ms=EXCLUDED.base_delay_ms,max_delay_ms=EXCLUDED.max_delay_ms,jitter=EXCLUDED.jitter,secret_sealed=EXCLUDED.secret_sealed,
 previous_secret_sealed=EXCLUDED.previous_secret_sealed,previous_until=EXCLUDED.previous_until,updated_at=greatest(EXCLUDED.updated_at,webhooks.created_at)
 WHERE (webhooks.name,webhooks.url,webhooks.enabled,webhooks.events,webhooks.header_names,webhooks.headers_sealed,webhooks.timeout_ms,webhooks.max_attempts,webhooks.base_delay_ms,webhooks.max_delay_ms,
 webhooks.jitter,webhooks.secret_sealed,webhooks.previous_secret_sealed,webhooks.previous_until)
 IS DISTINCT FROM (EXCLUDED.name,EXCLUDED.url,EXCLUDED.enabled,EXCLUDED.events,EXCLUDED.header_names,EXCLUDED.headers_sealed,EXCLUDED.timeout_ms,EXCLUDED.max_attempts,EXCLUDED.base_delay_ms,
 EXCLUDED.max_delay_ms,EXCLUDED.jitter,EXCLUDED.secret_sealed,EXCLUDED.previous_secret_sealed,EXCLUDED.previous_until)
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	// A schedule already running in the target keeps its next due time.
	{kind: "scan_schedule", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('library',s.doc->>'library_id') l,pg_temp.bk_dst('user',s.doc->>'owner_id') o FROM bk_stage s WHERE s.kind='scan_schedule'),
ok AS (SELECT * FROM x WHERE l IS NOT NULL AND o IS NOT NULL),
up AS (INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,watch_enabled)
 SELECT l,o,(doc->>'revision')::bigint,(doc->>'enabled')::boolean,doc->>'mode',(doc->>'interval_seconds')::integer,doc->>'cron',doc->>'timezone',(doc->>'probe')::boolean,(doc->>'nfo')::boolean,
 doc->>'ignore_mode',doc->>'ignore_case',(doc->>'next_due')::timestamptz,(doc->>'watch_enabled')::boolean FROM ok
 ON CONFLICT(library_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,revision=scan_schedules.revision+1,enabled=EXCLUDED.enabled,mode=EXCLUDED.mode,interval_seconds=EXCLUDED.interval_seconds,cron=EXCLUDED.cron,
 timezone=EXCLUDED.timezone,probe=EXCLUDED.probe,nfo=EXCLUDED.nfo,ignore_mode=EXCLUDED.ignore_mode,ignore_case=EXCLUDED.ignore_case,
 next_due=CASE WHEN EXCLUDED.enabled THEN COALESCE(scan_schedules.next_due,EXCLUDED.next_due) END,watch_enabled=EXCLUDED.watch_enabled,retry_after=NULL,last_error='',updated_at=clock_timestamp()
 WHERE (scan_schedules.owner_id,scan_schedules.enabled,scan_schedules.mode,scan_schedules.interval_seconds,scan_schedules.cron,scan_schedules.timezone,scan_schedules.probe,scan_schedules.nfo,
 scan_schedules.ignore_mode,scan_schedules.ignore_case,scan_schedules.watch_enabled)
 IS DISTINCT FROM (EXCLUDED.owner_id,EXCLUDED.enabled,EXCLUDED.mode,EXCLUDED.interval_seconds,EXCLUDED.cron,EXCLUDED.timezone,EXCLUDED.probe,EXCLUDED.nfo,EXCLUDED.ignore_mode,EXCLUDED.ignore_case,EXCLUDED.watch_enabled)
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	// Collections are administrator configuration: like client rules and
	// webhooks the exported state wins; the author is kept when that account
	// came along. Members are merged, never removed.
	{kind: "collection", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='collection' AND m.src=s.ref WHERE s.kind='collection' AND m.state IN ('existing','new')),
ins AS (INSERT INTO collections(id,name,overview,nfo_name,created_by,created_at,updated_at)
 SELECT id,doc->>'name',doc->>'overview',doc->>'nfo_name',pg_temp.bk_dst('user',doc->>'created_by'),(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz FROM x WHERE state='new' RETURNING 1),
upd AS (UPDATE collections c SET name=x.doc->>'name',overview=x.doc->>'overview',nfo_name=x.doc->>'nfo_name',updated_at=greatest((x.doc->>'updated_at')::timestamptz,c.created_at)
 FROM x WHERE x.state='existing' AND c.id=x.id AND (c.name,c.overview,c.nfo_name) IS DISTINCT FROM (x.doc->>'name',x.doc->>'overview',x.doc->>'nfo_name') RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),(SELECT count(*) FROM upd),0`},
	{kind: "collection_item", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('collection',s.doc->>'collection_id') c,pg_temp.bk_dst('item',s.doc->>'item_id') i FROM bk_stage s WHERE s.kind='collection_item'),
ok AS (SELECT * FROM x WHERE c IS NOT NULL AND i IS NOT NULL),
ins AS (INSERT INTO collection_items(collection_id,item_id,added_at) SELECT c,i,(doc->>'added_at')::timestamptz FROM ok ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,0`},
	// A playlist already in the target is the owner's live copy: it keeps
	// its name, visibility and entries. Entries are written into new
	// playlists only; an entry of a kept playlist is unchanged when the
	// target holds it as exported and set aside as playlist_kept otherwise.
	{kind: "playlist", sql: `WITH x AS (SELECT m.dst id,m.state,s.doc FROM bk_stage s JOIN bk_map m ON m.kind='playlist' AND m.src=s.ref WHERE s.kind='playlist' AND m.state IN ('existing','new')),
ins AS (INSERT INTO playlists(id,owner_id,name,public,created_at,updated_at)
 SELECT id,pg_temp.bk_dst('user',doc->>'owner_id'),doc->>'name',(doc->>'public')::boolean,(doc->>'created_at')::timestamptz,(doc->>'updated_at')::timestamptz FROM x WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM x),(SELECT count(*) FROM ins),0,0`},
	{kind: "playlist_item", special: "playlist_kept", sql: `WITH x AS (SELECT s.ref,s.doc,m.state,m.dst p,pg_temp.bk_dst('item',s.doc->>'item_id') i
 FROM bk_stage s JOIN bk_map m ON m.kind='playlist' AND m.src=(s.doc->>'playlist_id')::uuid WHERE s.kind='playlist_item' AND m.state IN ('existing','new')),
usable AS (SELECT * FROM x WHERE i IS NOT NULL),
ok AS (SELECT * FROM usable u WHERE state='new' OR EXISTS(SELECT 1 FROM playlist_items e WHERE e.id=u.ref AND e.playlist_id=u.p AND e.item_id=u.i AND e.position=(u.doc->>'position')::integer)),
ins AS (INSERT INTO playlist_items(id,playlist_id,item_id,position,added_at)
 SELECT CASE WHEN EXISTS(SELECT 1 FROM playlist_items e WHERE e.id=ok.ref) THEN gen_random_uuid() ELSE ok.ref END,p,i,(doc->>'position')::integer,(doc->>'added_at')::timestamptz FROM ok WHERE state='new' RETURNING 1)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM ins),0,(SELECT count(*) FROM usable)-(SELECT count(*) FROM ok)`},
	// Like progress, a preference keeps whichever side was updated last.
	// Layouts were checked before anything was applied.
	{kind: "user_preference", sql: `WITH x AS (SELECT s.doc,pg_temp.bk_dst('user',s.doc->>'user_id') u FROM bk_stage s WHERE s.kind='user_preference'
 AND NOT EXISTS(SELECT 1 FROM bk_map b WHERE b.kind='user_preference' AND b.src=(s.doc->>'user_id')::uuid)),
ok AS (SELECT * FROM x WHERE u IS NOT NULL),
up AS (INSERT INTO user_preferences AS p(user_id,theme,density,layout,updated_at)
 SELECT u,doc->>'theme',doc->>'density',NULLIF(doc->'layout','null'::jsonb),(doc->>'updated_at')::timestamptz FROM ok
 ON CONFLICT(user_id) DO UPDATE SET theme=EXCLUDED.theme,density=EXCLUDED.density,layout=EXCLUDED.layout,updated_at=EXCLUDED.updated_at WHERE EXCLUDED.updated_at>p.updated_at
 RETURNING (xmax=0) inserted)
SELECT (SELECT count(*) FROM ok),(SELECT count(*) FROM up WHERE inserted),(SELECT count(*) FROM up WHERE NOT inserted),0`},
	// The single-row documents take the exported values, normalized and
	// checked by validateMetadataSettings, with a new revision.
	{kind: "site_appearance", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='site_appearance' AND NOT EXISTS(SELECT 1 FROM bk_map b WHERE b.kind='site_appearance')),
v AS (SELECT doc->>'default_theme' theme,doc->'tokens' tokens,doc->>'custom_css' css,(doc->>'allow_external_fonts')::boolean fonts,COALESCE(pg_temp.bk_texts(doc->'font_hosts'),'{}') hosts,NULLIF(doc->'default_layout','null'::jsonb) layout FROM x),
upd AS (UPDATE site_appearance a SET default_theme=v.theme,tokens=v.tokens,custom_css=v.css,allow_external_fonts=v.fonts,font_hosts=v.hosts,default_layout=v.layout,revision=a.revision+1,updated_at=now()
 FROM v WHERE a.id AND (a.default_theme,a.tokens,a.custom_css,a.allow_external_fonts,a.font_hosts,a.default_layout) IS DISTINCT FROM (v.theme,v.tokens,v.css,v.fonts,v.hosts,v.layout) RETURNING 1)
SELECT (SELECT count(*) FROM x),0,(SELECT count(*) FROM upd),0`},
	{kind: "site_plugins", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='site_plugins' AND NOT EXISTS(SELECT 1 FROM bk_map b WHERE b.kind='site_plugins')),
upd AS (UPDATE site_plugins p SET plugins=x.doc->'plugins',settings=x.doc->'settings',revision=p.revision+1,updated_at=now()
 FROM x WHERE p.id AND (p.plugins,p.settings) IS DISTINCT FROM (x.doc->'plugins',x.doc->'settings') RETURNING 1)
SELECT (SELECT count(*) FROM x),0,(SELECT count(*) FROM upd),0`},
	{kind: "audit_retention", sql: `WITH x AS (SELECT s.doc FROM bk_stage s WHERE s.kind='audit_retention' AND NOT EXISTS(SELECT 1 FROM bk_map b WHERE b.kind='audit_retention')),
upd AS (UPDATE audit_retention r SET audit_days=(x.doc->>'audit_days')::integer,security_days=(x.doc->>'security_days')::integer,revision=r.revision+1,updated_at=now()
 FROM x WHERE r.singleton AND (r.audit_days,r.security_days) IS DISTINCT FROM ((x.doc->>'audit_days')::integer,(x.doc->>'security_days')::integer) RETURNING 1)
SELECT (SELECT count(*) FROM x),0,(SELECT count(*) FROM upd),0`},
}

// metadataStageSource feeds verified lines to COPY. The reader's error, a
// digest or count mismatch included, aborts the COPY and so the transaction.
type metadataStageSource struct {
	in   *domain.MetadataBackupReader
	seq  int64
	kind string
	data json.RawMessage
	err  error
}

func (s *metadataStageSource) Next() bool {
	kind, data, err := s.in.Next()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.err = err
		}
		return false
	}
	s.seq++
	s.kind, s.data = kind, data
	return true
}

func (s *metadataStageSource) Values() ([]any, error) { return []any{s.seq, s.kind, s.data}, nil }
func (s *metadataStageSource) Err() error             { return s.err }

// ImportMetadata applies a metadata backup in one transaction. A conflict
// returns the report with ErrMetadataBackupConflict and writes nothing.
func (s *Store) ImportMetadata(ctx context.Context, r io.Reader, opts domain.MetadataImportOptions) (domain.MetadataImportReport, error) {
	report := domain.MetadataImportReport{FormatVersion: domain.MetadataBackupFormatVersion, TargetSchemaVersion: SchemaVersion, DryRun: opts.DryRun}
	if ctx == nil || r == nil {
		return report, domain.ErrInvalid
	}
	in, header, err := domain.NewMetadataBackupReader(r, SchemaVersion)
	report.SourceSchemaVersion, report.PasswordHashes = header.SchemaVersion, header.PasswordHashes
	if err != nil {
		return report, err
	}
	report.FormatVersion = header.FormatVersion
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return report, storageError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL TimeZone='UTC'; SET LOCAL statement_timeout=0; SET LOCAL lock_timeout='10s'`); err != nil {
		return report, storageError(err)
	}
	// Serialize with scans, imports and every other catalog writer.
	if err = lockJobs(ctx, tx); err != nil {
		return report, err
	}
	var version int
	var dirty bool
	if err = tx.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		return report, storageError(err)
	}
	if dirty || version != SchemaVersion {
		return report, domain.ErrMetadataBackupUnsupported
	}
	if _, err = tx.Exec(ctx, metadataStageTables); err != nil {
		return report, storageError(err)
	}
	src := &metadataStageSource{in: in}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"bk_stage"}, []string{"seq", "kind", "doc"}, src); err != nil {
		if src.err != nil {
			return report, src.err
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		// The server rejected a line, for example data that is not JSON.
		return report, domain.ErrMetadataBackupCorrupt
	}
	trailer, ok := in.Trailer()
	if !ok {
		return report, domain.ErrMetadataBackupTruncated
	}
	report.SHA256 = trailer.SHA256
	for kind, n := range trailer.Counts {
		report.Kind(kind).Records = n
	}
	for _, statement := range metadataResolve {
		if _, err = tx.Exec(ctx, statement); err != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			// Malformed identifiers or duplicate records in a document
			// whose digest matched: the producer wrote nonsense.
			return report, domain.ErrMetadataBackupCorrupt
		}
	}
	var setupOpen bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM setup_state WHERE completed_at IS NULL)`).Scan(&setupOpen); err != nil {
		return report, storageError(err)
	}
	if err = validateMetadataSettings(ctx, tx); err != nil {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		return report, err
	}
	if err = readMetadataConflicts(ctx, tx, &report); err != nil {
		return report, err
	}
	if setupOpen {
		// A half-finished wizard owns the database; finish or reset it first.
		addMetadataConflict(&report, domain.MetadataConflict{Kind: "setup", Reason: "setup_in_progress"})
		return report, domain.ErrMetadataBackupConflict
	}
	if len(report.ConflictTotals) > 0 && !opts.SkipConflicts {
		return report, domain.ErrMetadataBackupConflict
	}
	var adminsBefore int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL`).Scan(&adminsBefore); err != nil {
		return report, storageError(err)
	}
	retentionBefore, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton`))
	if err != nil {
		return report, err
	}
	if err = applyMetadata(ctx, tx, &report, header.PasswordHashes); err != nil {
		return report, err
	}
	// A restored retention changes what the purge may remove, so it is
	// audited like an administrator change.
	if r := report.Kinds["audit_retention"]; r != nil && r.Updated > 0 {
		after, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton`))
		if err != nil {
			return report, err
		}
		if err = appendAudit(ctx, tx, AuditEntry{Event: "audit.retention_changed", TargetRef: "audit_retention", Before: retentionBefore, After: after}); err != nil {
			return report, err
		}
	}
	if err = finishMetadataImport(ctx, tx, &report, adminsBefore); err != nil {
		return report, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "metadata.imported", TargetRef: "sha256:" + report.SHA256, After: metadataImportAudit(report)}); err != nil {
		return report, err
	}
	if opts.DryRun {
		return report, nil
	}
	// Temporary functions outlive the transaction on the pooled session.
	if _, err = tx.Exec(ctx, `DROP FUNCTION pg_temp.bk_texts(jsonb); DROP FUNCTION pg_temp.bk_dst(text,text)`); err != nil {
		return report, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return report, storageError(err)
	}
	report.Committed = true
	return report, nil
}

func addMetadataConflict(report *domain.MetadataImportReport, c domain.MetadataConflict) {
	if report.ConflictTotals == nil {
		report.ConflictTotals = map[string]int64{}
	}
	report.ConflictTotals[c.Kind+":"+c.Reason]++
	if len(report.Conflicts) < domain.MetadataImportConflictSample {
		report.Conflicts = append(report.Conflicts, c)
	}
}

func readMetadataConflicts(ctx context.Context, tx pgx.Tx, report *domain.MetadataImportReport) error {
	rows, err := tx.Query(ctx, `SELECT kind,reason,count(*) FROM bk_map WHERE state='conflict' GROUP BY kind,reason ORDER BY kind,reason`)
	if err != nil {
		return storageError(err)
	}
	totals := map[string]int64{}
	for rows.Next() {
		var kind, reason string
		var n int64
		if err = rows.Scan(&kind, &reason, &n); err != nil {
			rows.Close()
			return storageError(err)
		}
		totals[kind+":"+reason] = n
		report.Kind(kind).Skip(reason, n)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return storageError(err)
	}
	if len(totals) == 0 {
		return nil
	}
	report.ConflictTotals = totals
	rows, err = tx.Query(ctx, `SELECT kind,src::text,reason FROM bk_map WHERE state='conflict' ORDER BY kind,src LIMIT $1`, domain.MetadataImportConflictSample)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c domain.MetadataConflict
		if err = rows.Scan(&c.Kind, &c.ID, &c.Reason); err != nil {
			return storageError(err)
		}
		report.Conflicts = append(report.Conflicts, c)
	}
	return storageError(rows.Err())
}

func applyMetadata(ctx context.Context, tx pgx.Tx, report *domain.MetadataImportReport, passwordHashes bool) error {
	for _, step := range metadataApply {
		var args []any
		if step.kind == "user" {
			args = append(args, passwordHashes)
		}
		var eligible, inserted, updated, special int64
		if err := tx.QueryRow(ctx, step.sql, args...).Scan(&eligible, &inserted, &updated, &special); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return storageError(err)
		}
		k := report.Kind(step.kind)
		k.Inserted, k.Updated, k.Unchanged = inserted, updated, eligible-inserted-updated
		k.Skip(step.special, special)
		// Conflicts of the kind itself were counted when they were read.
		accounted := eligible
		for _, n := range k.Skipped {
			accounted += n
		}
		k.Skip("unresolved_reference", k.Records-accounted)
	}
	return nil
}

// finishMetadataImport bumps what readers cache on, checks the global
// limits a merge can exceed, and marks setup done for a restored server.
func finishMetadataImport(ctx context.Context, tx pgx.Tx, report *domain.MetadataImportReport, adminsBefore int64) error {
	if _, err := tx.Exec(ctx, `UPDATE item_metadata_state st SET revision=st.revision+1 FROM bk_touched t JOIN bk_map m ON m.kind='item' AND m.dst=t.item_id AND m.state='existing' WHERE st.item_id=t.item_id`); err != nil {
		return storageError(err)
	}
	if r := report.Kinds["client_rule"]; r != nil && r.Inserted+r.Updated > 0 {
		if p := report.Kinds["client_control_policy"]; p == nil || p.Updated == 0 {
			if err := bumpClientVersion(ctx, tx); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scan_watch_state(library_id) SELECT library_id FROM scan_schedules WHERE watch_enabled ON CONFLICT DO NOTHING`); err != nil {
		return storageError(err)
	}
	var admins, watched int64
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL),(SELECT count(*) FROM scan_schedules WHERE watch_enabled)`).Scan(&admins, &watched); err != nil {
		return storageError(err)
	}
	limit := func(kind, reason string) error {
		addMetadataConflict(report, domain.MetadataConflict{Kind: kind, Reason: reason})
		return domain.ErrMetadataBackupConflict
	}
	if adminsBefore > 0 && admins == 0 {
		return limit("user", "last_admin")
	}
	if watched > domain.MaxWatchLibraries {
		return limit("scan_schedule", "watch_limit")
	}
	if err := checkClientRuleLimits(ctx, tx); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return limit("client_rule", "client_rule_limit")
		}
		return err
	}
	// The collection and playlist bounds the API enforces per request.
	var collections, crowded, playlists, longest int64
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM collections),
 (SELECT count(*) FROM (SELECT 1 FROM collection_items GROUP BY collection_id HAVING count(*)>$1) c),
 (SELECT count(*) FROM (SELECT 1 FROM playlists GROUP BY owner_id HAVING count(*)>$2) p),
 (SELECT count(*) FROM (SELECT 1 FROM playlist_items GROUP BY playlist_id HAVING count(*)>$3 OR max(position)>=$4) e)`,
		domain.CollectionManualItemsMax, domain.PlaylistsPerUserMax, domain.PlaylistEntriesMax, playlistPositionMax).Scan(&collections, &crowded, &playlists, &longest); err != nil {
		return storageError(err)
	}
	switch {
	case collections > domain.CollectionsMax:
		return limit("collection", "collection_limit")
	case crowded > 0:
		return limit("collection_item", "collection_items_limit")
	case playlists > 0:
		return limit("playlist", "playlist_limit")
	case longest > 0:
		return limit("playlist_item", "playlist_entries_limit")
	}
	// Like migration 071 for upgrades: a restored server with accounts or
	// libraries is not sent back through the wizard.
	_, err := tx.Exec(ctx, `INSERT INTO setup_state(id,version,current_step,state,adopted,completed_at)
 SELECT 1,1,'complete','{}'::jsonb,true,now() WHERE NOT EXISTS(SELECT 1 FROM setup_state)
 AND (EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL) OR EXISTS(SELECT 1 FROM libraries))`)
	return storageError(err)
}

func metadataImportAudit(report domain.MetadataImportReport) map[string]any {
	var records, inserted, updated, skipped int64
	for _, k := range report.Kinds {
		records += k.Records
		inserted += k.Inserted
		updated += k.Updated
		for _, n := range k.Skipped {
			skipped += n
		}
	}
	return map[string]any{"sourceSchemaVersion": report.SourceSchemaVersion, "records": records, "inserted": inserted, "updated": updated,
		"skipped": skipped, "passwordHashes": report.PasswordHashes, "dryRun": report.DryRun}
}
