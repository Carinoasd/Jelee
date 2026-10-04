package postgres

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Plan actions for a source account or library.
const (
	legacyInsert = iota + 1
	legacyMerge
	legacyUnchanged
	legacySkip
	legacyConflict
)

type legacyUserPlan struct {
	user       domain.LegacyUser
	action     int
	target     string // existing account, or the preferred ID of a new one
	reason     string
	conflict   string
	dropLedger bool
}

// legacyKeyValid reports whether a source key fits the ledger.
func legacyKeyValid(key string) bool { return key != "" && len(key) <= 128 }

func legacyStrings(ctx context.Context, q legacyDB, query string, args ...any) ([][]string, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, storageError(err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) ([]string, error) {
		values, err := row.Values()
		if err != nil {
			return nil, err
		}
		out := make([]string, len(values))
		for i, v := range values {
			switch x := v.(type) {
			case string:
				out[i] = x
			case bool:
				if x {
					out[i] = "t"
				}
			}
		}
		return out, nil
	})
}

// resolveUsers decides what every account becomes; it never writes.
func (im *legacyImporter) resolveUsers(ctx context.Context, q legacyDB, users []domain.LegacyUser) ([]legacyUserPlan, error) {
	keys, names, ids := make([]string, 0, len(users)), make([]string, 0, len(users)), []string{}
	for _, u := range users {
		keys, names = append(keys, u.ID), append(names, u.Name)
		if domain.ValidID(u.ID) {
			ids = append(ids, u.ID)
		}
	}
	ledger, err := legacyStrings(ctx, q, `SELECT m.source_key,m.target_id::text,u.id IS NOT NULL,u.deleted_at IS NOT NULL FROM legacy_import_map m
 LEFT JOIN users u ON u.id=m.target_id WHERE m.kind='user' AND m.source_key=ANY($1::text[])`, keys)
	if err != nil {
		return nil, err
	}
	holders, err := legacyStrings(ctx, q, `SELECT x.n,u.id::text,u.deleted_at IS NOT NULL,COALESCE(m.source_key,'') FROM unnest($1::text[]) x(n)
 JOIN users u ON lower(u.name)=lower(x.n) LEFT JOIN legacy_import_map m ON m.kind='user' AND m.target_id=u.id`, names)
	if err != nil {
		return nil, err
	}
	taken, err := legacyStrings(ctx, q, `SELECT id::text FROM users WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	byKey, byName, takenIDs := map[string][]string{}, map[string][][]string{}, map[string]bool{}
	for _, r := range ledger {
		byKey[r[0]] = r
	}
	for _, r := range holders {
		byName[r[0]] = append(byName[r[0]], r)
	}
	for _, r := range taken {
		takenIDs[r[0]] = true
	}
	plans := make([]legacyUserPlan, 0, len(users))
	for _, u := range users {
		p := legacyUserPlan{user: u}
		if !legacyKeyValid(u.ID) {
			p.action, p.reason = legacySkip, domain.LegacyReasonInvalidID
			plans = append(plans, p)
			continue
		}
		if r, ok := byKey[u.ID]; ok {
			if r[2] == "t" {
				p.target = r[1]
				if r[3] == "t" {
					p.action, p.reason = legacySkip, domain.LegacyReasonTargetDeleted
				} else {
					p.action = legacyUnchanged
				}
				im.seenUsers[strings.ToLower(u.Name)] = u.ID
				plans = append(plans, p)
				continue
			}
			p.dropLedger = true
		}
		lower := strings.ToLower(u.Name)
		switch other, seen := im.seenUsers[lower]; {
		case !domain.ValidLegacyName(u.Name):
			p.action, p.conflict = legacyConflict, domain.LegacyReasonNameInvalid
		case seen && other != u.ID:
			p.action, p.conflict = legacyConflict, domain.LegacyReasonNameDuplicate
		default:
			p.action = legacyInsert
			for _, h := range byName[u.Name] {
				switch {
				case h[3] != "" && h[3] != u.ID:
					p.action, p.conflict = legacyConflict, domain.LegacyReasonNameDuplicate
				case h[2] == "t":
					p.action, p.conflict = legacyConflict, domain.LegacyReasonNameTakenDeleted
				case im.opts.MergeUsers:
					p.action, p.target = legacyMerge, h[1]
				default:
					p.action, p.conflict = legacyConflict, domain.LegacyReasonNameTaken
				}
			}
			if p.action == legacyInsert && domain.ValidID(u.ID) && !takenIDs[u.ID] {
				p.target = u.ID
			}
			im.seenUsers[lower] = u.ID
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// applyUsers creates and maps the accounts of one batch. Permission and
// preference rows are counted with their account.
func (im *legacyImporter) applyUsers(ctx context.Context, tx pgx.Tx, users []domain.LegacyUser) (legacyPhaseState, error) {
	var out legacyPhaseState
	plans, err := im.resolveUsers(ctx, tx, users)
	if err != nil {
		return out, err
	}
	accounts, perms, prefs := out.category(domain.LegacyCatUsers), out.category(domain.LegacyCatPermissions), out.category(domain.LegacyCatPreferences)
	for _, p := range plans {
		u := p.user
		if p.dropLedger {
			if _, err = tx.Exec(ctx, `DELETE FROM legacy_import_map WHERE kind='user' AND source_key=$1`, u.ID); err != nil {
				return out, storageError(err)
			}
		}
		rows := int64(len(u.Permissions))
		prefRows := int64(len(u.Preferences))
		switch p.action {
		case legacyInsert:
			a := u.Account()
			var id string
			if err = tx.QueryRow(ctx, `INSERT INTO users(id,name,is_admin,disabled,hidden,parental_rating_max,block_unrated,content_filtered,max_streams,max_kbps)
 VALUES(COALESCE(NULLIF($1,'')::uuid,gen_random_uuid()),$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`,
				p.target, a.Name, a.Admin, a.Disabled, a.Hidden, a.ParentalRatingMax, a.BlockUnrated, a.ParentalRatingMax != nil, a.MaxStreams, a.MaxKbps).Scan(&id); err != nil {
				return out, storageError(err)
			}
			if err = im.ledger(ctx, tx, "user", u.ID, id, "", true); err != nil {
				return out, err
			}
			accounts.Inserted++
			out.PasswordReset++
			for _, f := range u.Permissions {
				if reason := domain.LegacyPermissionOutcome(f.Kind); reason != "" {
					perms.Skip(reason, 1)
				} else {
					perms.Inserted++
				}
			}
			for _, f := range u.Preferences {
				if reason := domain.LegacyPreferenceOutcome(f.Kind); reason != "" {
					prefs.Skip(reason, 1)
				} else {
					prefs.Inserted++
				}
			}
		case legacyMerge:
			if err = im.ledger(ctx, tx, "user", u.ID, p.target, "", false); err != nil {
				return out, err
			}
			accounts.Matched++
			perms.Skip(domain.LegacyReasonUserMerged, rows)
			prefs.Skip(domain.LegacyReasonUserMerged, prefRows)
		case legacyUnchanged:
			if err = im.ledger(ctx, tx, "user", u.ID, p.target, "", false); err != nil {
				return out, err
			}
			accounts.Unchanged++
			perms.Unchanged += rows
			prefs.Unchanged += prefRows
		case legacySkip:
			accounts.Skip(p.reason, 1)
			perms.Skip(domain.LegacyReasonUserNotImported, rows)
			prefs.Skip(domain.LegacyReasonUserNotImported, prefRows)
		default:
			out.conflict(domain.LegacyCatUsers, u.ID, p.conflict)
			perms.Skip(domain.LegacyReasonUserNotImported, rows)
			prefs.Skip(domain.LegacyReasonUserNotImported, prefRows)
		}
	}
	return out, nil
}

// ledger records or refreshes a source row's target. created and the
// origin run of an existing entry are kept.
func (im *legacyImporter) ledger(ctx context.Context, tx pgx.Tx, kind, key, target, targetUser string, created bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO legacy_import_map(kind,source_key,target_id,target_user,created,origin_run,run_id) VALUES($1,$2,$3::uuid,NULLIF($4,'')::uuid,$5,$6::uuid,$6::uuid)
 ON CONFLICT(kind,source_key) DO UPDATE SET target_id=EXCLUDED.target_id,target_user=EXCLUDED.target_user,run_id=EXCLUDED.run_id,updated_at=now()`,
		kind, key, target, targetUser, created, im.runID)
	return storageError(err)
}

type legacyRootPlan struct {
	path     string
	action   int
	target   string
	reason   string
	conflict string
}

type legacyLibraryPlan struct {
	library    domain.LegacyLibrary
	action     int
	target     string
	reason     string
	conflict   string
	roots      []legacyRootPlan
	dropLedger bool
}

// resolveLibraries decides what every collection folder becomes; it never
// writes.
func (im *legacyImporter) resolveLibraries(ctx context.Context, q legacyDB, libraries []domain.LegacyLibrary) ([]legacyLibraryPlan, error) {
	keys, names, ids := []string{}, []string{}, []string{}
	for _, l := range libraries {
		keys, names = append(keys, l.ID), append(names, l.Name)
		if domain.ValidID(l.ID) {
			ids = append(ids, l.ID)
		}
	}
	roots, err := legacyStrings(ctx, q, `SELECT path,library_id::text FROM library_roots`)
	if err != nil {
		return nil, err
	}
	ledger, err := legacyStrings(ctx, q, `SELECT m.source_key,m.target_id::text,l.id IS NOT NULL FROM legacy_import_map m LEFT JOIN libraries l ON l.id=m.target_id
 WHERE m.kind='library' AND m.source_key=ANY($1::text[])`, keys)
	if err != nil {
		return nil, err
	}
	holders, err := legacyStrings(ctx, q, `SELECT l.name,l.id::text,COALESCE(m.source_key,'') FROM libraries l LEFT JOIN legacy_import_map m ON m.kind='library' AND m.target_id=l.id
 WHERE l.name=ANY($1::text[])`, names)
	if err != nil {
		return nil, err
	}
	taken, err := legacyStrings(ctx, q, `SELECT id::text FROM libraries WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	rootOwner, byKey, byName, takenIDs := map[string]string{}, map[string][]string{}, map[string][]string{}, map[string]bool{}
	for _, r := range roots {
		rootOwner[r[0]] = r[1]
	}
	for _, r := range ledger {
		byKey[r[0]] = r
	}
	for _, r := range holders {
		byName[r[0]] = r
	}
	for _, r := range taken {
		takenIDs[r[0]] = true
	}
	plans := make([]legacyLibraryPlan, 0, len(libraries))
	for _, l := range libraries {
		p := legacyLibraryPlan{library: l}
		reason := domain.LegacyLibraryOutcome(l.CollectionType)
		switch {
		case !legacyKeyValid(l.ID):
			reason = domain.LegacyReasonInvalidID
		case reason == "" && len(l.Locations) == 0:
			reason = domain.LegacyReasonNoLocations
		}
		if reason != "" {
			p.action, p.reason = legacySkip, reason
			for range l.Locations {
				p.roots = append(p.roots, legacyRootPlan{action: legacySkip, reason: reason})
			}
			plans = append(plans, p)
			continue
		}
		// Map the locations; an unmappable one is a conflict of its own.
		var usable []int
		seen := map[string]bool{}
		for _, location := range l.Locations {
			mapped, ok := domain.MapLegacyPath(im.opts.PathMap, location, filepath.Separator)
			switch {
			case !ok || !filepath.IsAbs(mapped) || !validText(mapped, 4096, false):
				p.roots = append(p.roots, legacyRootPlan{action: legacyConflict, conflict: domain.LegacyReasonRootUnmapped})
			case seen[mapped]:
				p.roots = append(p.roots, legacyRootPlan{path: mapped, action: legacySkip, reason: domain.LegacyReasonDuplicateLocation})
			default:
				seen[mapped] = true
				usable = append(usable, len(p.roots))
				p.roots = append(p.roots, legacyRootPlan{path: mapped})
			}
		}
		owner := func(path string) string {
			if o := rootOwner[path]; o != "" {
				return o
			}
			if src := im.seenRoots[path]; src != "" && src != l.ID {
				return "source:" + src
			}
			return ""
		}
		if r, ok := byKey[l.ID]; ok && r[2] == "t" {
			p.action, p.target = legacyUnchanged, r[1]
			for _, i := range usable {
				switch o := owner(p.roots[i].path); o {
				case p.target:
					p.roots[i].action, p.roots[i].target = legacyUnchanged, o
				case "":
					p.roots[i].action = legacyInsert
				default:
					p.roots[i].action, p.roots[i].conflict = legacyConflict, domain.LegacyReasonRootTaken
				}
			}
		} else {
			p.dropLedger = ok
			holder := byName[l.Name]
			switch other, dup := im.seenLibs[l.Name]; {
			case len(usable) == 0:
				p.action, p.conflict = legacyConflict, domain.LegacyReasonRootUnmapped
			case !domain.ValidLegacyName(l.Name):
				p.action, p.conflict = legacyConflict, domain.LegacyReasonNameInvalid
			case dup && other != l.ID || holder != nil && holder[2] != "" && holder[2] != l.ID:
				p.action, p.conflict = legacyConflict, domain.LegacyReasonNameDuplicate
			case holder != nil:
				// A library of the same name is this library only when it
				// already holds every mapped location.
				p.action, p.target = legacyMerge, holder[1]
				for _, i := range usable {
					if owner(p.roots[i].path) != holder[1] {
						p.action, p.target, p.conflict = legacyConflict, "", domain.LegacyReasonNameTaken
						break
					}
					p.roots[i].action, p.roots[i].target = legacyMerge, holder[1]
				}
			default:
				p.action = legacyInsert
				for _, i := range usable {
					if owner(p.roots[i].path) != "" {
						p.action, p.conflict = legacyConflict, domain.LegacyReasonRootTaken
					} else {
						p.roots[i].action = legacyInsert
					}
				}
				if p.action == legacyInsert && domain.ValidID(l.ID) && !takenIDs[l.ID] {
					p.target = l.ID
				}
			}
			if p.action == legacyConflict {
				for _, i := range usable {
					if p.conflict == domain.LegacyReasonRootTaken && owner(p.roots[i].path) != "" {
						p.roots[i].action, p.roots[i].conflict = legacyConflict, domain.LegacyReasonRootTaken
					} else {
						p.roots[i].action, p.roots[i].reason = legacySkip, domain.LegacyReasonLibraryNotImport
					}
				}
			}
		}
		im.seenLibs[l.Name] = l.ID
		if p.action != legacyConflict {
			for _, i := range usable {
				if p.roots[i].action != legacyConflict {
					im.seenRoots[p.roots[i].path] = l.ID
				}
			}
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// applyLibraries creates or maps libraries and their roots.
func (im *legacyImporter) applyLibraries(ctx context.Context, tx pgx.Tx, libraries []domain.LegacyLibrary) (legacyPhaseState, error) {
	var out legacyPhaseState
	plans, err := im.resolveLibraries(ctx, tx, libraries)
	if err != nil {
		return out, err
	}
	libs, roots := out.category(domain.LegacyCatLibraries), out.category(domain.LegacyCatLibraryRoots)
	for _, p := range plans {
		l := p.library
		if p.dropLedger {
			if _, err = tx.Exec(ctx, `DELETE FROM legacy_import_map WHERE kind='library' AND source_key=$1`, l.ID); err != nil {
				return out, storageError(err)
			}
		}
		target := p.target
		switch p.action {
		case legacyInsert:
			if err = tx.QueryRow(ctx, `INSERT INTO libraries(id,name) VALUES(COALESCE(NULLIF($1,'')::uuid,gen_random_uuid()),$2) RETURNING id::text`, p.target, l.Name).Scan(&target); err != nil {
				return out, storageError(err)
			}
			libs.Inserted++
		case legacyMerge:
			libs.Matched++
		case legacyUnchanged:
			libs.Unchanged++
		case legacySkip:
			libs.Skip(p.reason, 1)
		default:
			out.conflict(domain.LegacyCatLibraries, l.ID, p.conflict)
		}
		if p.action == legacyInsert || p.action == legacyMerge || p.action == legacyUnchanged {
			if err = im.ledger(ctx, tx, "library", l.ID, target, "", p.action == legacyInsert); err != nil {
				return out, err
			}
		}
		for _, r := range p.roots {
			switch r.action {
			case legacyInsert:
				if _, err = tx.Exec(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2)`, target, r.path); err != nil {
					return out, storageError(err)
				}
				roots.Inserted++
			case legacyMerge:
				roots.Matched++
			case legacyUnchanged:
				roots.Unchanged++
			case legacySkip:
				roots.Skip(r.reason, 1)
			default:
				out.conflict(domain.LegacyCatLibraryRoots, l.ID, r.conflict)
			}
		}
	}
	return out, nil
}

// applyAccess grants library access and blocked tags to the accounts this
// run created. Accounts found in Jelee (merged) and accounts an earlier run
// created keep whatever the administrator set since.
func (im *legacyImporter) applyAccess(ctx context.Context, tx pgx.Tx, users []domain.LegacyUser) (legacyPhaseState, error) {
	var out legacyPhaseState
	keys := make([]string, 0, len(users))
	for _, u := range users {
		keys = append(keys, u.ID)
	}
	created, err := legacyStrings(ctx, tx, `SELECT m.source_key,m.target_id::text FROM legacy_import_map m JOIN users u ON u.id=m.target_id AND u.deleted_at IS NULL
 WHERE m.kind='user' AND m.created AND m.origin_run=$1::uuid AND m.source_key=ANY($2::text[])`, im.runID, keys)
	if err != nil {
		return out, err
	}
	libraries, err := legacyStrings(ctx, tx, `SELECT m.source_key,m.target_id::text FROM legacy_import_map m JOIN libraries l ON l.id=m.target_id WHERE m.kind='library' ORDER BY m.source_key`)
	if err != nil {
		return out, err
	}
	targets, libraryTarget := map[string]string{}, map[string]string{}
	for _, r := range created {
		targets[r[0]] = r[1]
	}
	for _, r := range libraries {
		libraryTarget[r[0]] = r[1]
	}
	grants := out.category(domain.LegacyCatLibraryAccess)
	grants.Derived = true
	for _, u := range users {
		target, ok := targets[u.ID]
		if !ok {
			continue
		}
		var ids []string
		if u.Permission(domain.LegacyPermAllFolders) {
			for _, r := range libraries {
				ids = append(ids, r[1])
			}
			grants.Source += int64(len(libraries))
		} else {
			wanted := map[string]bool{}
			for _, folder := range u.Preference(domain.LegacyPrefEnabledFolders) {
				folder = strings.ToLower(folder)
				if len(folder) == 32 {
					// Guid.ToString("N") form.
					folder = folder[:8] + "-" + folder[8:12] + "-" + folder[12:16] + "-" + folder[16:20] + "-" + folder[20:]
				}
				if wanted[folder] {
					continue
				}
				wanted[folder] = true
				grants.Source++
				if dst := libraryTarget[folder]; dst != "" {
					ids = append(ids, dst)
				} else {
					grants.Skip(domain.LegacyReasonLibraryNotImport, 1)
				}
			}
		}
		if len(ids) > 0 {
			tag, execErr := tx.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT $1::uuid,unnest($2::uuid[]) ON CONFLICT DO NOTHING`, target, ids)
			if execErr != nil {
				return out, storageError(execErr)
			}
			grants.Inserted += tag.RowsAffected()
			grants.Unchanged += int64(len(ids)) - tag.RowsAffected()
		}
		if tags := u.Account().BlockedTags; len(tags) > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO user_blocked_tags(user_id,tag) SELECT DISTINCT $1::uuid,lower(btrim(t)) FROM unnest($2::text[]) t
 WHERE length(lower(btrim(t))) BETWEEN 1 AND 128 ON CONFLICT DO NOTHING`, target, tags); err != nil {
				return out, storageError(err)
			}
		}
	}
	return out, nil
}

type legacyRoot struct{ id, path string }

// applyItems maps source items to catalog items by file or folder location.
func (im *legacyImporter) applyItems(ctx context.Context, tx pgx.Tx, items []domain.LegacyItem) (legacyPhaseState, error) {
	var out legacyPhaseState
	rows, err := legacyStrings(ctx, tx, `SELECT id::text,path FROM library_roots`)
	if err != nil {
		return out, err
	}
	roots := make([]legacyRoot, 0, len(rows))
	for _, r := range rows {
		roots = append(roots, legacyRoot{r[0], r[1]})
	}
	// The deepest root wins when roots nest.
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].path) > len(roots[j].path) })
	cat := out.category(domain.LegacyCatItems)
	type staged struct {
		key, root, rel, path string
		dir                  bool
	}
	var stage []staged
	var unmatched, keep []string
	pendingKey, pendingReason, pendingPath := []string{}, []string{}, []string{}
	pend := func(key, reason, path string) {
		cat.Pend(reason, 1)
		pendingKey, pendingReason, pendingPath = append(pendingKey, key), append(pendingReason, reason), append(pendingPath, path)
	}
	for _, item := range items {
		if !legacyKeyValid(item.ID) {
			cat.Skip(domain.LegacyReasonInvalidID, 1)
			continue
		}
		shape, reason := item.Classify()
		var mapped string
		if reason == "" {
			var ok bool
			mapped, ok = domain.MapLegacyPath(im.opts.PathMap, item.Path, filepath.Separator)
			if !ok || !filepath.IsAbs(mapped) || !validText(mapped, 4096, false) {
				reason = domain.LegacyReasonPathUnmapped
			}
		}
		if reason != "" {
			cat.Skip(reason, 1)
			unmatched = append(unmatched, item.ID)
			continue
		}
		root, rel := "", ""
		for _, r := range roots {
			if v, ok := domain.LegacyRootRelative(r.path, mapped, filepath.Separator); ok && (shape == domain.LegacyShapeFolder || v != ".") {
				root, rel = r.id, v
				break
			}
		}
		switch {
		case root == "" && shape == domain.LegacyShapeFolder:
			cat.Skip(domain.LegacyReasonFolderUnmatched, 1)
			unmatched = append(unmatched, item.ID)
		case root == "":
			pend(item.ID, domain.LegacyReasonOutsideRoots, mapped)
			unmatched = append(unmatched, item.ID)
			keep = append(keep, item.ID)
		default:
			stage = append(stage, staged{key: item.ID, root: root, rel: rel, path: mapped, dir: shape == domain.LegacyShapeFolder})
		}
	}
	var matchKey, matchTarget []string
	if len(stage) > 0 {
		keys, rootIDs, rels, dirs := make([]string, len(stage)), make([]string, len(stage)), make([]string, len(stage)), make([]bool, len(stage))
		for i, s := range stage {
			keys[i], rootIDs[i], rels[i], dirs[i] = s.key, s.root, s.rel, s.dir
		}
		found, err := legacyStrings(ctx, tx, `SELECT s.k,COALESCE(m.item_id,d.item_id)::text FROM unnest($1::text[],$2::uuid[],$3::text[],$4::bool[]) s(k,root,rel,dir)
 LEFT JOIN media_sources m ON NOT s.dir AND m.root_id=s.root AND m.relative_path=s.rel
 LEFT JOIN item_directory_sources d ON s.dir AND d.root_id=s.root AND d.relative_path=s.rel`, keys, rootIDs, rels, dirs)
		if err != nil {
			return out, err
		}
		target := map[string]string{}
		for _, r := range found {
			target[r[0]] = r[1]
		}
		for _, s := range stage {
			switch t := target[s.key]; {
			case t != "":
				cat.Matched++
				matchKey, matchTarget = append(matchKey, s.key), append(matchTarget, t)
			case s.dir:
				cat.Skip(domain.LegacyReasonFolderUnmatched, 1)
				unmatched = append(unmatched, s.key)
			default:
				reason := domain.LegacyReasonNotScanned
				if im.opts.FileExists != nil && !im.opts.FileExists(s.path) {
					reason = domain.LegacyReasonFileMissing
				}
				pend(s.key, reason, s.path)
				unmatched = append(unmatched, s.key)
				keep = append(keep, s.key)
			}
		}
	}
	if len(matchKey) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO legacy_import_map(kind,source_key,target_id,created,origin_run,run_id) SELECT 'item',k,t,false,$3::uuid,$3::uuid FROM unnest($1::text[],$2::uuid[]) x(k,t)
 ON CONFLICT(kind,source_key) DO UPDATE SET target_id=EXCLUDED.target_id,run_id=EXCLUDED.run_id,updated_at=now()`, matchKey, matchTarget, im.runID); err != nil {
			return out, storageError(err)
		}
	}
	// A source item that no longer matches loses its ledger entry, and only
	// pending items stay listed as pending.
	if _, err = tx.Exec(ctx, `DELETE FROM legacy_import_map WHERE kind='item' AND source_key=ANY($1::text[])`, unmatched); err != nil {
		return out, storageError(err)
	}
	gone := make([]string, 0, len(items))
	pendingSet := map[string]bool{}
	for _, k := range keep {
		pendingSet[k] = true
	}
	for _, item := range items {
		if !pendingSet[item.ID] {
			gone = append(gone, item.ID)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM legacy_import_pending WHERE kind='item' AND source_key=ANY($1::text[])`, gone); err != nil {
		return out, storageError(err)
	}
	if len(pendingKey) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO legacy_import_pending(kind,source_key,reason,path,run_id) SELECT 'item',k,r,p,$4::uuid FROM unnest($1::text[],$2::text[],$3::text[]) x(k,r,p)
 ON CONFLICT(kind,source_key) DO UPDATE SET reason=EXCLUDED.reason,path=EXCLUDED.path,run_id=EXCLUDED.run_id,updated_at=now()`, pendingKey, pendingReason, pendingPath, im.runID); err != nil {
			return out, storageError(err)
		}
	}
	return out, nil
}

type legacyProgress struct {
	key, user, item string
	data            domain.LegacyUserData
}

// newer orders two candidate progress rows for one Jelee user and item.
func (a legacyProgress) newer(b legacyProgress) bool {
	switch {
	case (a.data.LastPlayed == nil) != (b.data.LastPlayed == nil):
		return a.data.LastPlayed != nil
	case a.data.LastPlayed != nil && !a.data.LastPlayed.Equal(*b.data.LastPlayed):
		return a.data.LastPlayed.After(*b.data.LastPlayed)
	case a.data.Played != b.data.Played:
		return a.data.Played
	case a.data.Position != b.data.Position:
		return a.data.Position > b.data.Position
	}
	return a.key < b.key
}

// applyUserData imports played state and resume positions. The side that
// was played last wins, so importing an old database never rewinds newer
// progress; rows without a play date count as oldest.
func (im *legacyImporter) applyUserData(ctx context.Context, tx pgx.Tx, data []domain.LegacyUserData) (legacyPhaseState, error) {
	var out legacyPhaseState
	userKeys, itemKeys := []string{}, []string{}
	for _, d := range data {
		userKeys, itemKeys = append(userKeys, d.UserID), append(itemKeys, d.ItemID)
	}
	users, err := legacyStrings(ctx, tx, `SELECT m.source_key,m.target_id::text FROM legacy_import_map m JOIN users u ON u.id=m.target_id AND u.deleted_at IS NULL
 WHERE m.kind='user' AND m.source_key=ANY($1::text[])`, userKeys)
	if err != nil {
		return out, err
	}
	items, err := legacyStrings(ctx, tx, `SELECT m.source_key,m.target_id::text FROM legacy_import_map m JOIN items i ON i.id=m.target_id WHERE m.kind='item' AND m.source_key=ANY($1::text[])`, itemKeys)
	if err != nil {
		return out, err
	}
	pending, err := legacyStrings(ctx, tx, `SELECT source_key FROM legacy_import_pending WHERE kind='item' AND source_key=ANY($1::text[])`, itemKeys)
	if err != nil {
		return out, err
	}
	userTarget, itemTarget, isPending := map[string]string{}, map[string]string{}, map[string]bool{}
	for _, r := range users {
		userTarget[r[0]] = r[1]
	}
	for _, r := range items {
		itemTarget[r[0]] = r[1]
	}
	for _, r := range pending {
		isPending[r[0]] = true
	}
	cat := out.category(domain.LegacyCatUserData)
	best := map[[2]string]legacyProgress{}
	for _, d := range data {
		if d.Rows > 1 {
			cat.Skip(domain.LegacyReasonMergedDuplicate, int64(d.Rows-1))
		}
		shape, kindReason := domain.LegacyItemShape(d.ItemType)
		user, item := userTarget[d.UserID], itemTarget[d.ItemID]
		reason := ""
		switch {
		case d.ItemID == domain.LegacyPlaceholderItemID:
			reason = domain.LegacyReasonItemDetached
		case !d.UserExists:
			reason = domain.LegacyReasonUserMissing
		case d.ItemType == "":
			reason = domain.LegacyReasonItemMissing
		case shape == domain.LegacyShapeNone:
			reason = kindReason
		case user == "":
			reason = domain.LegacyReasonUserNotImported
		case d.Empty() && d.Favorite:
			reason = domain.LegacyReasonFavoriteOnly
		case d.Empty():
			reason = domain.LegacyReasonEmpty
		case item == "" && isPending[d.ItemID]:
			cat.Pend(domain.LegacyReasonItemPending, 1)
			continue
		case item == "" || !legacyKeyValid(d.Key()):
			reason = domain.LegacyReasonItemNotMapped
		}
		if reason != "" {
			cat.Skip(reason, 1)
			continue
		}
		if d.Favorite {
			out.FavoritesDropped++
		}
		candidate := legacyProgress{key: d.Key(), user: user, item: item, data: d}
		pair := [2]string{user, item}
		if current, ok := best[pair]; ok {
			cat.Skip(domain.LegacyReasonMergedVersion, 1)
			if !candidate.newer(current) {
				continue
			}
		}
		best[pair] = candidate
	}
	if len(best) == 0 {
		return out, nil
	}
	n := len(best)
	keys, us, is := make([]string, 0, n), make([]string, 0, n), make([]string, 0, n)
	positions, counts, played, last := make([]int64, 0, n), make([]int32, 0, n), make([]bool, 0, n), make([]*time.Time, 0, n)
	for _, p := range best {
		keys, us, is = append(keys, p.key), append(us, p.user), append(is, p.item)
		positions = append(positions, min(max(p.data.Position, 0), domain.MaxLegacyTicks))
		counts = append(counts, int32(min(max(p.data.PlayCount, 0), 1<<31-1))) //nolint:gosec // G115: clamped to the int32 range
		played, last = append(played, p.data.Played), append(last, p.data.LastPlayed)
	}
	var inserted, updated int64
	if err = tx.QueryRow(ctx, `WITH x AS (SELECT * FROM unnest($1::uuid[],$2::uuid[],$3::bigint[],$4::bool[],$5::int[],$6::timestamptz[]) x(u,i,pos,played,cnt,last)),
up AS (INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at)
 SELECT u,i,pos,played,cnt,last,COALESCE(last,'epoch'::timestamptz) FROM x
 ON CONFLICT(user_id,item_id) DO UPDATE SET resume_ticks=EXCLUDED.resume_ticks,played=EXCLUDED.played,play_count=EXCLUDED.play_count,last_played_at=EXCLUDED.last_played_at,updated_at=EXCLUDED.updated_at
 WHERE EXCLUDED.updated_at>user_item_data.updated_at RETURNING (xmax=0) inserted)
SELECT count(*) FILTER (WHERE inserted),count(*) FILTER (WHERE NOT inserted) FROM up`, us, is, positions, played, counts, last).Scan(&inserted, &updated); err != nil {
		return out, storageError(err)
	}
	cat.Inserted += inserted
	cat.Updated += updated
	cat.Unchanged += int64(n) - inserted - updated
	if _, err = tx.Exec(ctx, `INSERT INTO legacy_import_map(kind,source_key,target_id,target_user,created,origin_run,run_id)
 SELECT 'user_data',k,i,u,false,$4::uuid,$4::uuid FROM unnest($1::text[],$2::uuid[],$3::uuid[]) x(k,i,u)
 ON CONFLICT(kind,source_key) DO UPDATE SET target_id=EXCLUDED.target_id,target_user=EXCLUDED.target_user,run_id=EXCLUDED.run_id,updated_at=now()`, keys, is, us, im.runID); err != nil {
		return out, storageError(err)
	}
	return out, nil
}
