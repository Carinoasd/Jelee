package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The unified authorization filter (G48.2, G48.8). Every user-facing read
// of catalog content binds its rows to the caller in SQL through the
// predicates of this file, never by filtering results afterwards. Rules are
// added here only; TestVisibilityPredicateHasOneSource fails when another
// statement reads the grant or rule tables itself.
//
// The predicates read the live user row as u: a statement either joins
// users u (enabled, not deleted) or selects principalColumnsSQL into a
// principal CTE and reads it as u. The per-request part (network
// attributes and the client control library set) arrives as one jsonb
// statement parameter, named by the rq argument of the predicates and
// bound with requestScopeArg.
//
// Precedence (docs/access-control.md):
//  0. The request restriction (G48.5, G47): a library with enabled network
//     rules is visible only to requests one of them matches (administrators
//     only answer to rules with include_admins), and a client control
//     restrict_libraries decision limits the request to its libraries. It
//     applies to every principal, guests included; nothing below widens it.
//  1. The library grant. Administrators see every library; a share guest
//     (G48.6) sees only the live share's library, and for an item share
//     only that item and its descendants; nothing below widens it.
//  2. Content rules apply to users with any restriction (content_filtered)
//     and to administrators only while access_policy.restrict_admins is on
//     and no developer mode session relaxes it (G48.9, see below).
//  3. A restricted time window of u that holds at the request time (G48.4)
//     hides everything, or caps the rating ceiling of 6; item rules do not
//     lift it.
//  4. The nearest explicit item rule on the item or an ancestor decides:
//     hide hides, allow shows despite 5 and 6 (not despite a window).
//  5. A blocked tag or genre, or a blocked keyword in a title (G48.4), on
//     the item or an ancestor hides it.
//  6. Under a rating ceiling, the highest recognized rating of the item and
//     its ancestors must not exceed it; without any recognized rating the
//     user's block_unrated, else the policy default, decides.

// visibilityUserColumns are the user columns the predicates read.
const visibilityUserColumns = `id,is_admin,content_filtered,parental_rating_max,block_unrated,share_id`

// principalColumnsSQL lists visibilityUserColumns qualified by alias, for
// principal CTEs that select from users under that alias.
func principalColumnsSQL(alias string) string {
	return alias + `.id,` + alias + `.is_admin,` + alias + `.content_filtered,` + alias + `.parental_rating_max,` + alias + `.block_unrated,` + alias + `.share_id`
}

// libraryVisibleSQL is the visibility of a library itself: the request
// restriction and the library grant. Administrators see every library,
// anyone else only the libraries granted in library_acl, and a share guest
// only the library of a live whole-library share. column names the library
// being checked; rq names the request parameter (requestScopeArg).
func libraryVisibleSQL(rq, column string) string {
	return `(` + libraryGrantSQL(column, true) + ` AND ` + requestLibrarySQL(rq, column) + `)`
}

// shareLiveSQL holds for a share row vz_s that is neither revoked nor
// expired. Revoking also revokes the guest's sessions; the filter checks
// the share itself so an expiry takes effect without any session change.
const shareLiveSQL = `vz_s.revoked_at IS NULL AND vz_s.expires_at>now()`

// libraryGrantSQL is the library grant of u. A guest has no library_acl
// rows; its grant is the library of its live share, and with whole only a
// whole-library share grants the library itself (an item share lists no
// library).
func libraryGrantSQL(column string, whole bool) string {
	scope := ``
	if whole {
		scope = ` AND vz_s.item_id IS NULL`
	}
	return `(u.is_admin OR (u.share_id IS NULL AND EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=` + column + `))
 OR EXISTS(SELECT 1 FROM share_links vz_s WHERE vz_s.id=u.share_id AND vz_s.library_id=` + column + scope + ` AND ` + shareLiveSQL + `))`
}

// shareItemSQL limits a guest of an item share to the item and its
// descendants; it holds for everyone else. The share's liveness is checked
// by libraryGrantSQL or grantedLibrariesSQL.
func shareItemSQL(item string) string {
	return `(u.share_id IS NULL OR EXISTS(SELECT 1 FROM share_links vz_s WHERE vz_s.id=u.share_id AND (vz_s.item_id IS NULL OR vz_s.item_id IN (SELECT vz_c.id FROM (` + itemChainSQL(item) + `) vz_c))))`
}

// requestLibrarySQL is the request restriction of the library in column
// (G48.5, G47). The libraries hidden from the request are an uncorrelated
// subquery of the request parameter, so PostgreSQL computes them once per
// statement (InitPlan) and each row only compares its library with that
// array: the libraries whose network rules the request fails, and with a
// client control library set every library outside it.
func requestLibrarySQL(rq, column string) string {
	return `(` + column + ` <> ALL(CASE WHEN u.is_admin THEN ` + requestHiddenSQL(rq, true) + ` ELSE ` + requestHiddenSQL(rq, false) + ` END))`
}

// requestHiddenSQL selects the libraries hidden from the request for an
// administrator (admins) or anyone else.
func requestHiddenSQL(rq string, admins bool) string {
	return `ARRAY(` + networkHiddenSQL(rq, admins) + `
 UNION ALL SELECT vz_b.id FROM libraries vz_b WHERE jsonb_typeof(` + rq + `::jsonb->'libraries')='array' AND NOT (` + rq + `::jsonb->'libraries') ? vz_b.id::text)`
}

// networkHiddenSQL selects the libraries whose enabled network rules all
// fail for the request (G48.5). Without a request parameter every condition
// is unknown and only a rule without conditions matches, so a statement
// run outside a request fails closed.
func networkHiddenSQL(rq string, admins bool) string {
	only := ``
	if admins {
		only = ` AND vz_n.include_admins`
	}
	return `SELECT vz_n.library_id FROM library_network_rules vz_n WHERE vz_n.enabled` + only + ` GROUP BY vz_n.library_id
 HAVING NOT COALESCE(bool_or(CASE vz_n.network WHEN 'lan' THEN (` + rq + `::jsonb->>'lan')::boolean WHEN 'wan' THEN NOT (` + rq + `::jsonb->>'lan')::boolean ELSE true END
  AND (cardinality(vz_n.cidrs)=0 OR (` + rq + `::jsonb->>'ip')::inet <<= ANY(vz_n.cidrs))
  AND (cardinality(vz_n.client_kinds)=0 OR (` + rq + `::jsonb->>'kind')=ANY(vz_n.client_kinds))),false)`
}

// itemVisibleSQL is the visibility of one catalog item: library names its
// library column and item its item ID column, rq the request parameter.
// Sources, sidecar tracks, images, user data and statistics rows are
// visible exactly when their item is.
func itemVisibleSQL(rq, library, item string) string {
	return `(` + libraryGrantSQL(library, false) + ` AND ` + shareItemSQL(item) + ` AND ` + requestLibrarySQL(rq, library) + ` AND ` + contentVisibleSQL(rq, item) + `)`
}

// walkedItemVisibleSQL is itemVisibleSQL for a statement that already
// walks the granted libraries (grantedLibrariesSQL) or is an
// administrator's: everything but the library grant itself.
func walkedItemVisibleSQL(rq, library, item string) string {
	return `(` + shareItemSQL(item) + ` AND ` + requestLibrarySQL(rq, library) + ` AND ` + contentVisibleSQL(rq, item) + `)`
}

// grantVisibleSQL is itemVisibleSQL without the request restriction: the
// visibility an item has for u through its grants and content rules at the
// time of rq, whatever network a request comes from. Only administrative
// previews use it (G48.7), to count what a change of grants or
// restrictions shows or hides; a user-facing read uses itemVisibleSQL.
func grantVisibleSQL(rq, library, item string) string {
	return `(` + libraryGrantSQL(library, false) + ` AND ` + shareItemSQL(item) + ` AND ` + contentVisibleSQL(rq, item) + `)`
}

// sharePlaybackSQL refuses direct delivery to a guest whose share does not
// allow playback. Such a share never issues a native session; this is the
// storage backstop.
const sharePlaybackSQL = `(u.share_id IS NULL OR EXISTS(SELECT 1 FROM share_links vz_s WHERE vz_s.id=u.share_id AND vz_s.allow_playback AND ` + shareLiveSQL + `))`

// guestLiveSQL holds for a user u that is no guest, or whose share is
// live: a guest session ends with its share even before the session row
// expires or is revoked.
const guestLiveSQL = `(u.share_id IS NULL OR EXISTS(SELECT 1 FROM share_links vz_s WHERE vz_s.id=u.share_id AND ` + shareLiveSQL + `))`

// guestReadOnlySQL reports a guest u whose share refuses writes.
const guestReadOnlySQL = `COALESCE((SELECT vz_s.read_only FROM share_links vz_s WHERE vz_s.id=u.share_id),false)`

// shareStreamsSQL is the concurrent playback cap of a guest's share; 0 for
// everyone else. It applies whatever the server-wide stream limit switch.
const shareStreamsSQL = `COALESCE((SELECT vz_s.max_streams::int FROM share_links vz_s WHERE vz_s.id=u.share_id),0)`

// requestScopeArg binds the request parameter of the predicates: the
// network attributes and client control library set the HTTP layer
// attached to the principal of ctx. Without one it is NULL, and network
// rules then fail closed.
func requestScopeArg(ctx context.Context) any {
	p, ok := access.PrincipalFromContext(ctx)
	if !ok || p.Request == nil {
		return nil
	}
	scope := map[string]any{"kind": string(p.Request.Kind)}
	if !p.Request.At.IsZero() {
		// The request time decides restricted time windows (G48.4), so every
		// statement of a request agrees and tests can inject the clock.
		scope["at"] = p.Request.At.UTC().Format(time.RFC3339Nano)
	}
	if p.Request.IP.IsValid() {
		scope["ip"] = p.Request.IP.Unmap().WithZone("").String()
		scope["lan"] = p.Request.LAN()
	}
	if p.Request.Libraries != nil {
		libraries := make([]string, 0, len(p.Request.Libraries))
		for _, id := range p.Request.Libraries {
			if domain.ValidID(id) && !slices.Contains(libraries, id) {
				libraries = append(libraries, id)
			}
		}
		scope["libraries"] = libraries
	}
	data, err := json.Marshal(scope)
	if err != nil {
		// A map of strings, booleans and a string slice always encodes;
		// fail closed rather than unrestricted should it ever not.
		return `{"libraries":[]}`
	}
	return string(data)
}

// contentVisibleSQL applies the content rules to the item ID expression
// item, whose library grant is checked separately; rq names the request
// parameter, whose time decides the restricted time windows. Every lookup
// is an index probe keyed by the item, its at most two ancestors and the
// user; an unrestricted user stops at content_filtered.
func contentVisibleSQL(rq, item string) string {
	chain := itemChainSQL(item)
	return `(NOT u.content_filtered
 OR (u.is_admin AND (NOT COALESCE((SELECT vz_p.restrict_admins FROM access_policy vz_p),false) OR ` + devPermissionRelaxedSQL + `))
 OR COALESCE((SELECT NOT vz_w.closed
  AND COALESCE(
   (SELECT vz_r.effect='allow' FROM (` + chain + `) vz_c JOIN user_item_access_rules vz_r ON vz_r.user_id=u.id AND vz_r.item_id=vz_c.id
    ORDER BY vz_c.depth LIMIT 1),
   NOT EXISTS(SELECT 1 FROM (` + chain + `) vz_c
    JOIN item_metadata_facts vz_f ON vz_f.item_id=vz_c.id AND vz_f.field IN ('tags','genres')
    CROSS JOIN LATERAL jsonb_array_elements_text(vz_f.value) vz_t(tag)
    JOIN user_blocked_tags vz_b ON vz_b.user_id=u.id AND vz_b.tag=lower(btrim(vz_t.tag)))
   AND ` + keywordsClearSQL(chain) + `
   AND ` + ratingWithinSQL("u.parental_rating_max") + `)
  AND ` + ratingWithinSQL("vz_w.ceiling") + `
  FROM (` + activeWindowsSQL(rq) + `) vz_w
  CROSS JOIN LATERAL (SELECT CASE WHEN u.parental_rating_max IS NULL AND vz_w.ceiling IS NULL THEN NULL ELSE
   (SELECT max(COALESCE(vz_l.level,CASE WHEN vz_n.code ~ '^[0-9]{1,2}\+?$' THEN least(rtrim(vz_n.code,'+')::int,21) END))
    FROM (` + chain + `) vz_c
    JOIN item_metadata_fields vz_m ON vz_m.item_id=vz_c.id AND vz_m.field IN ('mpaa','certification')
    CROSS JOIN LATERAL (SELECT ` + parentalRatingCodeSQL("vz_m.value") + ` AS code) vz_n
    LEFT JOIN parental_ratings vz_l ON vz_l.code=vz_n.code) END AS level) vz_e),false))`
}

// ratingWithinSQL holds when the effective rating vz_e.level (the highest
// recognized rating of the item and its ancestors) is within ceiling, or
// there is no ceiling. Without a recognized rating the user's block_unrated,
// else the policy default, decides.
func ratingWithinSQL(ceiling string) string {
	return `(` + ceiling + ` IS NULL OR COALESCE(vz_e.level<=` + ceiling + `,NOT COALESCE(u.block_unrated,(SELECT vz_p.block_unrated FROM access_policy vz_p),false)))`
}

// keywordsClearSQL holds when no blocked keyword of u occurs in the scan
// title, metadata title or original title of the item or an ancestor
// (G48.4). Both sides are compared NFKC-normalized and in lower case; a
// user without keywords stops at one index probe.
func keywordsClearSQL(chain string) string {
	return `(NOT EXISTS(SELECT 1 FROM user_blocked_keywords vz_k WHERE vz_k.user_id=u.id) OR NOT EXISTS(SELECT 1 FROM (` + chain + `) vz_c
    JOIN items vz_i ON vz_i.id=vz_c.id
    CROSS JOIN LATERAL (SELECT vz_i.title AS v UNION ALL SELECT vz_m.value FROM item_metadata_fields vz_m WHERE vz_m.item_id=vz_c.id AND vz_m.field IN ('title','originalTitle')) vz_h
    JOIN user_blocked_keywords vz_k ON vz_k.user_id=u.id AND strpos(lower(normalize(vz_h.v,NFKC)),vz_k.keyword)>0))`
}

// activeWindowsSQL selects one row for u: whether one of its restricted time
// windows that holds at the request time hides everything (closed), and the
// lowest rating ceiling of those that cap it (G48.4). The time is the
// request time the HTTP layer took (requestScopeArg), else the statement
// time; each window reads it in its own time zone. A window whose end is at
// or before its start crosses midnight and belongs to the day it opened.
func activeWindowsSQL(rq string) string {
	return `SELECT COALESCE(bool_or(vz_aw.rating_max IS NULL),false) AS closed,min(vz_aw.rating_max) AS ceiling
 FROM user_access_windows vz_aw
 CROSS JOIN LATERAL (SELECT COALESCE((` + rq + `::jsonb->>'at')::timestamptz,statement_timestamp()) AT TIME ZONE vz_aw.time_zone AS t) vz_at
 CROSS JOIN LATERAL (SELECT (extract(hour FROM vz_at.t)*60+extract(minute FROM vz_at.t))::int AS m,extract(dow FROM vz_at.t)::int AS d) vz_am
 WHERE vz_aw.user_id=u.id AND CASE
  WHEN vz_aw.start_minute<vz_aw.end_minute THEN vz_am.m>=vz_aw.start_minute AND vz_am.m<vz_aw.end_minute AND (cardinality(vz_aw.weekdays)=0 OR vz_am.d=ANY(vz_aw.weekdays))
  WHEN vz_am.m>=vz_aw.start_minute THEN cardinality(vz_aw.weekdays)=0 OR vz_am.d=ANY(vz_aw.weekdays)
  WHEN vz_am.m<vz_aw.end_minute THEN cardinality(vz_aw.weekdays)=0 OR (vz_am.d+6)%7=ANY(vz_aw.weekdays)
  ELSE false END`
}

// devPermissionRelaxedSQL is true while a developer mode session has
// relax_permission_strict on (G48.9). It only suspends restrict_admins for
// administrators: library grants and every rule of other users stay in force,
// so the relaxation can never show a non-administrator anything new. The
// session row expires by its own deadline here, and the runtime switches it
// off on every instance that does not meet the developer mode thresholds.
const devPermissionRelaxedSQL = `EXISTS(SELECT 1 FROM dev_mode_state vz_d WHERE vz_d.active AND vz_d.expires_at>now() AND 'relax_permission_strict'=ANY(vz_d.toggles))`

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
// instead of filtering every item: the library_acl grants, or the library
// of a guest's live whole-library share. Rows walked this way still need
// walkedItemVisibleSQL. A guest of an item share walks sharedItemsSQL.
func grantedLibrariesSQL(alias string) string {
	return `SELECT a.library_id FROM library_acl a WHERE a.user_id=` + alias + `.id AND ` + alias + `.share_id IS NULL
 UNION ALL SELECT vz_s.library_id FROM share_links vz_s WHERE vz_s.id=` + alias + `.share_id AND vz_s.item_id IS NULL AND ` + shareLiveSQL
}

// sharedItemsSQL selects the item of a guest's live item share and its
// descendants (item_parent_links nest at most two levels), so a listing
// walks the shared subtree instead of the whole library. Rows walked this
// way still need walkedItemVisibleSQL.
func sharedItemsSQL(alias string) string {
	return `SELECT vz_d.id FROM share_links vz_s CROSS JOIN LATERAL (SELECT vz_s.item_id AS id
  UNION ALL SELECT vz_l1.item_id FROM item_parent_links vz_l1 WHERE vz_l1.parent_id=vz_s.item_id
  UNION ALL SELECT vz_l2.item_id FROM item_parent_links vz_l1 JOIN item_parent_links vz_l2 ON vz_l2.parent_id=vz_l1.item_id WHERE vz_l1.parent_id=vz_s.item_id) vz_d
 WHERE vz_s.id=` + alias + `.share_id AND vz_s.item_id IS NOT NULL AND ` + shareLiveSQL
}
