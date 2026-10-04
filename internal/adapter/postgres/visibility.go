package postgres

// The unified authorization filter (G48.2, G48.8). Every user-facing read
// of catalog content binds its rows to the caller in SQL through the
// predicates of this file, never by filtering results afterwards. Rules are
// added here only; TestVisibilityPredicateHasOneSource fails when another
// statement reads the grant or rule tables itself.
//
// The predicates read the live user row as u: a statement either joins
// users u (enabled, not deleted) or selects principalColumnsSQL into a
// principal CTE and reads it as u.
//
// Precedence (docs/access-control.md):
//  1. The library grant. Administrators see every library; nothing below
//     widens it.
//  2. Content rules apply to users with any restriction (content_filtered)
//     and to administrators only while access_policy.restrict_admins is on.
//  3. The nearest explicit item rule on the item or an ancestor decides:
//     hide hides, allow shows despite 4 and 5.
//  4. A blocked tag or genre on the item or an ancestor hides it.
//  5. Under a rating ceiling, the highest recognized rating of the item and
//     its ancestors must not exceed it; without any recognized rating the
//     user's block_unrated, else the policy default, decides.

// visibilityUserColumns are the user columns the predicates read.
const visibilityUserColumns = `id,is_admin,content_filtered,parental_rating_max,block_unrated`

// principalColumnsSQL lists visibilityUserColumns qualified by alias, for
// principal CTEs that select from users under that alias.
func principalColumnsSQL(alias string) string {
	return alias + `.id,` + alias + `.is_admin,` + alias + `.content_filtered,` + alias + `.parental_rating_max,` + alias + `.block_unrated`
}

// libraryVisibleSQL is the library grant: administrators see every library,
// anyone else only the libraries granted in library_acl. column names the
// library being checked.
func libraryVisibleSQL(column string) string {
	return `(u.is_admin OR EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=` + column + `))`
}

// itemVisibleSQL is the visibility of one catalog item: library names its
// library column and item its item ID column. Sources, sidecar tracks,
// images, user data and statistics rows are visible exactly when their
// item is.
func itemVisibleSQL(library, item string) string {
	return `(` + libraryVisibleSQL(library) + ` AND ` + contentVisibleSQL(item) + `)`
}

// contentVisibleSQL applies the content rules to the item ID expression
// item, whose library grant is checked separately. Every lookup is an index
// probe keyed by the item, its at most two ancestors and the user; an
// unrestricted user stops at content_filtered.
func contentVisibleSQL(item string) string {
	chain := itemChainSQL(item)
	return `(NOT u.content_filtered
 OR (u.is_admin AND NOT COALESCE((SELECT vz_p.restrict_admins FROM access_policy vz_p),false))
 OR COALESCE(
  (SELECT vz_r.effect='allow' FROM (` + chain + `) vz_c JOIN user_item_access_rules vz_r ON vz_r.user_id=u.id AND vz_r.item_id=vz_c.id
   ORDER BY vz_c.depth LIMIT 1),
  NOT EXISTS(SELECT 1 FROM (` + chain + `) vz_c
   JOIN item_metadata_facts vz_f ON vz_f.item_id=vz_c.id AND vz_f.field IN ('tags','genres')
   CROSS JOIN LATERAL jsonb_array_elements_text(vz_f.value) vz_t(tag)
   JOIN user_blocked_tags vz_b ON vz_b.user_id=u.id AND vz_b.tag=lower(btrim(vz_t.tag)))
  AND (u.parental_rating_max IS NULL OR COALESCE(
   (SELECT max(COALESCE(vz_l.level,CASE WHEN vz_n.code ~ '^[0-9]{1,2}\+?$' THEN least(rtrim(vz_n.code,'+')::int,21) END))
    FROM (` + chain + `) vz_c
    JOIN item_metadata_fields vz_m ON vz_m.item_id=vz_c.id AND vz_m.field IN ('mpaa','certification')
    CROSS JOIN LATERAL (SELECT ` + parentalRatingCodeSQL("vz_m.value") + ` AS code) vz_n
    LEFT JOIN parental_ratings vz_l ON vz_l.code=vz_n.code)<=u.parental_rating_max,
   NOT COALESCE(u.block_unrated,(SELECT vz_p.block_unrated FROM access_policy vz_p),false)))))`
}

// itemChainSQL selects the item and its ancestors with their distance:
// item_parent_links nest at most two levels (episode, season, series).
func itemChainSQL(item string) string {
	return `SELECT ` + item + ` AS id,0 AS depth
 UNION ALL SELECT vz_l1.parent_id,1 FROM item_parent_links vz_l1 WHERE vz_l1.item_id=` + item + `
 UNION ALL SELECT vz_l2.parent_id,2 FROM item_parent_links vz_l1 JOIN item_parent_links vz_l2 ON vz_l2.item_id=vz_l1.parent_id WHERE vz_l1.item_id=` + item
}

// parentalRatingCodeSQL normalizes a rating value for the parental_ratings
// lookup: trimmed, upper case, without a leading "Rated " or a two-letter
// country prefix such as "US:" or "DE: ".
func parentalRatingCodeSQL(value string) string {
	return `upper(btrim(regexp_replace(regexp_replace(btrim(` + value + `),'^rated\s+','','i'),'^[a-z]{2}\s*[:：]\s*','','i')))`
}

// grantedLibrariesSQL selects the library_id of every library granted to the
// non-administrator user alias, so a listing can walk granted libraries
// instead of filtering every item.
func grantedLibrariesSQL(alias string) string {
	return `SELECT a.library_id FROM library_acl a WHERE a.user_id=` + alias + `.id`
}
