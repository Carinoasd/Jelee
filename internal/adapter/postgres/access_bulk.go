package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Access administration for many users at once (G48.7): the user × library
// grant matrix, bulk grant changes, access templates and the preview of a
// change. Grants are written with the statements of account_acl.go and
// restrictions through writeContentAccess; what a change shows or hides is
// counted with the unified filter (grantVisibleSQL), never re-derived here.

// GetAccessGrantMatrix lists every live account but share guests with its
// library grants, and every library. Administrators only.
func (s *Store) GetAccessGrantMatrix(ctx context.Context, actor domain.Actor) (domain.AccessGrantMatrix, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessGrantMatrix{}, err
	}
	defer tx.Rollback(ctx)
	m := domain.AccessGrantMatrix{Libraries: make([]domain.LibraryGrant, 0), Users: make([]domain.AccessGrantMatrixUser, 0)}
	rows, err := tx.Query(ctx, `SELECT id::text,name FROM libraries ORDER BY lower(name),id LIMIT $1`, domain.AccessBulkLibrariesMax)
	if err != nil {
		return domain.AccessGrantMatrix{}, storageError(err)
	}
	for rows.Next() {
		var l domain.LibraryGrant
		if err = rows.Scan(&l.LibraryID, &l.Name); err != nil {
			rows.Close()
			return domain.AccessGrantMatrix{}, storageError(err)
		}
		m.Libraries = append(m.Libraries, l)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return domain.AccessGrantMatrix{}, storageError(err)
	}
	rows, err = tx.Query(ctx, `SELECT u.id::text,u.name,u.display_name,u.is_admin,u.disabled,`+accessGrantListSQL+`
 FROM users u WHERE u.deleted_at IS NULL AND u.share_id IS NULL ORDER BY lower(u.name),u.id LIMIT $1`, domain.AccessGrantMatrixUsersMax+1)
	if err != nil {
		return domain.AccessGrantMatrix{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u domain.AccessGrantMatrixUser
		if err = rows.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Admin, &u.Disabled, &u.LibraryIDs); err != nil {
			return domain.AccessGrantMatrix{}, storageError(err)
		}
		if len(m.Users) == domain.AccessGrantMatrixUsersMax {
			m.Truncated = true
			break
		}
		m.Users = append(m.Users, u)
	}
	if err = rows.Err(); err != nil {
		return domain.AccessGrantMatrix{}, storageError(err)
	}
	return m, storageError(tx.Commit(ctx))
}

// accessGrantListSQL selects the granted library IDs of user u.
const accessGrantListSQL = `ARRAY(SELECT a.library_id::text FROM library_acl a WHERE a.user_id=u.id ORDER BY a.library_id)`

// accessTargets locks the live, non-guest users of ids in ID order and
// returns their names; any other ID is ErrNotFound.
func accessTargets(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	rows, err := tx.Query(ctx, `SELECT u.id::text,u.name FROM users u WHERE u.id=ANY($1::uuid[]) AND u.deleted_at IS NULL AND u.share_id IS NULL ORDER BY u.id FOR UPDATE`, sorted)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	names := make(map[string]string, len(ids))
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			return nil, storageError(err)
		}
		names[id] = name
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if len(names) != len(sorted) {
		return nil, domain.ErrNotFound
	}
	return names, nil
}

// accessLibrariesExist refuses library IDs that name no library.
func accessLibrariesExist(ctx context.Context, tx pgx.Tx, ids []string) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM libraries WHERE id=ANY($1::uuid[])`, ids).Scan(&count); err != nil {
		return storageError(err)
	}
	if count != len(ids) {
		return domain.ErrNotFound
	}
	return nil
}

// accessGrants reads the library grants of users, sorted.
func accessGrants(ctx context.Context, tx pgx.Tx, users []string) (map[string][]string, error) {
	rows, err := tx.Query(ctx, `SELECT u.id::text,`+accessGrantListSQL+` FROM users u WHERE u.id=ANY($1::uuid[])`, users)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	grants := make(map[string][]string, len(users))
	for rows.Next() {
		var id string
		var libraries []string
		if err = rows.Scan(&id, &libraries); err != nil {
			return nil, storageError(err)
		}
		grants[id] = libraries
	}
	return grants, storageError(rows.Err())
}

// accessVisiblePairsSQL selects the items visible to each user of $1
// through grants and content rules at the time of $2 (grantVisibleSQL),
// limited to the libraries of $3 when it is not NULL.
var accessVisiblePairsSQL = `SELECT u.id AS user_id,i.id AS item_id FROM users u JOIN items i ON ($3::uuid[] IS NULL OR i.library_id=ANY($3::uuid[]))
 AND ` + grantVisibleSQL("$2", "i.library_id", "i.id") + ` WHERE u.id=ANY($1::uuid[])`

// accessPreview counts what a change does to the visibility of users' items.
// begin snapshots the visible pairs before the change into a transaction
// temporary table; count compares them with the pairs after it.
type accessPreview struct {
	users     []string
	libraries []string
	rq        any
}

func newAccessPreview(ctx context.Context, tx pgx.Tx, users, libraries []string) (*accessPreview, error) {
	p := &accessPreview{users: users, libraries: libraries, rq: requestScopeArg(ctx)}
	// The two passes judge every compared item once each; compiling the
	// long predicate costs far more than evaluating it (see the JIT note in
	// docs/access-control.md), so this transaction runs without JIT.
	if _, err := tx.Exec(ctx, `SET LOCAL jit=off`); err != nil {
		return nil, storageError(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE access_preview_before(user_id uuid NOT NULL,item_id uuid NOT NULL,PRIMARY KEY(user_id,item_id)) ON COMMIT DROP`); err != nil {
		return nil, storageError(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO access_preview_before `+accessVisiblePairsSQL, p.users, p.rq, p.libraries); err != nil {
		return nil, storageError(err)
	}
	return p, nil
}

// count fills the shown and hidden counts of changes (by user ID) and the
// totals of result.
func (p *accessPreview) count(ctx context.Context, tx pgx.Tx, result *domain.AccessChangePreview, changes map[string]*domain.AccessUserChange) error {
	rows, err := tx.Query(ctx, `WITH a AS (`+accessVisiblePairsSQL+`),
d AS MATERIALIZED (SELECT COALESCE(a.user_id,b.user_id) AS user_id,COALESCE(a.item_id,b.item_id) AS item_id,b.user_id IS NULL AS shown
 FROM a FULL JOIN access_preview_before b ON b.user_id=a.user_id AND b.item_id=a.item_id WHERE a.user_id IS NULL OR b.user_id IS NULL)
SELECT d.user_id::text,count(*) FILTER (WHERE d.shown),count(*) FILTER (WHERE NOT d.shown),(SELECT count(DISTINCT item_id) FROM d) FROM d GROUP BY d.user_id`,
		p.users, p.rq, p.libraries)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var shown, hidden, items int64
		if err = rows.Scan(&id, &shown, &hidden, &items); err != nil {
			return storageError(err)
		}
		c, ok := changes[id]
		if !ok {
			return domain.ErrDatabase
		}
		c.Shown, c.Hidden = shown, hidden
		result.Shown, result.Hidden, result.Items = result.Shown+shown, result.Hidden+hidden, items
	}
	return storageError(rows.Err())
}

// accessChangeResult orders the changed users by name and counts them.
func accessChangeResult(result *domain.AccessChangePreview, changes map[string]*domain.AccessUserChange) {
	result.Changes = make([]domain.AccessUserChange, 0, len(changes))
	for _, c := range changes {
		if len(c.AddedLibraryIDs) > 0 || len(c.RemovedLibraryIDs) > 0 || c.RestrictionsChanged || c.Shown > 0 || c.Hidden > 0 {
			result.Changes = append(result.Changes, *c)
		}
		if len(c.AddedLibraryIDs) > 0 || len(c.RemovedLibraryIDs) > 0 || c.RestrictionsChanged {
			result.Users++
		}
	}
	slices.SortFunc(result.Changes, func(a, b domain.AccessUserChange) int {
		if n := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return strings.Compare(a.UserID, b.UserID)
	})
}

// grantDifference fills the added and removed libraries of each user.
func grantDifference(changes map[string]*domain.AccessUserChange, before, after map[string][]string) {
	for id, c := range changes {
		c.AddedLibraryIDs, c.RemovedLibraryIDs = make([]string, 0), make([]string, 0)
		for _, l := range after[id] {
			if !slices.Contains(before[id], l) {
				c.AddedLibraryIDs = append(c.AddedLibraryIDs, l)
			}
		}
		for _, l := range before[id] {
			if !slices.Contains(after[id], l) {
				c.RemovedLibraryIDs = append(c.RemovedLibraryIDs, l)
			}
		}
	}
}

// auditGrantChanges records user.library_access_replaced for every user
// whose grants changed, like a single grant replacement does.
func auditGrantChanges(ctx context.Context, tx pgx.Tx, actor domain.Actor, changes map[string]*domain.AccessUserChange, before, after map[string][]string) error {
	ids := make([]string, 0, len(changes))
	for id := range changes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := changes[id]
		if len(c.AddedLibraryIDs) == 0 && len(c.RemovedLibraryIDs) == 0 {
			continue
		}
		if err := auditAccount(ctx, tx, actor, "user.library_access_replaced", id, map[string]any{"libraryIds": before[id]}, map[string]any{"libraryIds": after[id]}); err != nil {
			return err
		}
	}
	return nil
}

// accessSummaryAudit is the summary record of an applied change: what was
// asked and the counts the change produced, the same ones a preview shows.
func accessSummaryAudit(request map[string]any, result domain.AccessChangePreview) map[string]any {
	request["users"], request["items"], request["shown"], request["hidden"] = result.Users, result.Items, result.Shown, result.Hidden
	return request
}

// ApplyAccessGrants adds and removes library grants for many users in one
// transaction, in the order of ops (G48.7). With preview nothing is
// written: the result reports the users whose grants change and the items
// that become visible or hidden. Applied, every changed user is audited as
// user.library_access_replaced and the change as access.grants_bulk_applied
// with the same counts. Administrators only.
func (s *Store) ApplyAccessGrants(ctx context.Context, actor domain.Actor, ops []domain.AccessGrantOperation, preview bool) (domain.AccessChangePreview, error) {
	if !domain.ValidAccessGrantOperations(ops) {
		return domain.AccessChangePreview{}, domain.ErrInvalid
	}
	var users, libraries []string
	for _, op := range ops {
		for _, id := range op.UserIDs {
			if !slices.Contains(users, id) {
				users = append(users, id)
			}
		}
		for _, id := range op.LibraryIDs {
			if !slices.Contains(libraries, id) {
				libraries = append(libraries, id)
			}
		}
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	defer tx.Rollback(ctx)
	names, err := accessTargets(ctx, tx, users)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	if err = accessLibrariesExist(ctx, tx, libraries); err != nil {
		return domain.AccessChangePreview{}, err
	}
	before, err := accessGrants(ctx, tx, users)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	// Only the named libraries can change visibility.
	p, err := newAccessPreview(ctx, tx, users, libraries)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	for _, op := range ops {
		if op.Action == domain.AccessGrantAdd {
			_, err = tx.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT u,l FROM unnest($1::uuid[]) u CROSS JOIN unnest($2::uuid[]) l ON CONFLICT DO NOTHING`, op.UserIDs, op.LibraryIDs)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM library_acl WHERE user_id=ANY($1::uuid[]) AND library_id=ANY($2::uuid[])`, op.UserIDs, op.LibraryIDs)
		}
		if err != nil {
			return domain.AccessChangePreview{}, storageError(err)
		}
	}
	after, err := accessGrants(ctx, tx, users)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	changes := make(map[string]*domain.AccessUserChange, len(users))
	for _, id := range users {
		changes[id] = &domain.AccessUserChange{UserID: id, Name: names[id]}
	}
	grantDifference(changes, before, after)
	result := domain.AccessChangePreview{}
	if err = p.count(ctx, tx, &result, changes); err != nil {
		return domain.AccessChangePreview{}, err
	}
	accessChangeResult(&result, changes)
	if preview {
		return result, nil
	}
	if err = auditGrantChanges(ctx, tx, actor, changes, before, after); err != nil {
		return domain.AccessChangePreview{}, err
	}
	audited := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		audited = append(audited, map[string]any{"action": op.Action, "userIds": op.UserIDs, "libraryIds": op.LibraryIDs})
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.grants_bulk_applied", Actor: actor, After: accessSummaryAudit(map[string]any{"operations": audited}, result)}); err != nil {
		return domain.AccessChangePreview{}, err
	}
	result.Applied = true
	return result, storageError(tx.Commit(ctx))
}

// accessTemplateColumns are the columns scanTemplate reads, with the
// template's libraries.
const accessTemplateColumns = `t.id::text,t.name,ARRAY(SELECT l.library_id::text FROM access_template_libraries l WHERE l.template_id=t.id ORDER BY l.library_id),
 t.rating_max,t.block_unrated,t.blocked_tags,t.blocked_keywords,t.created_at,t.updated_at`

func scanTemplate(row pgx.Row) (domain.AccessTemplate, error) {
	var t domain.AccessTemplate
	var ceiling *int16
	if err := row.Scan(&t.ID, &t.Name, &t.LibraryIDs, &ceiling, &t.BlockUnrated, &t.BlockedTags, &t.BlockedKeywords, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return domain.AccessTemplate{}, storageError(err)
	}
	if ceiling != nil {
		level := int(*ceiling)
		t.ParentalRatingMax = &level
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	return t, nil
}

func accessTemplateAudit(t domain.AccessTemplate) map[string]any {
	return map[string]any{"id": t.ID, "name": t.Name, "libraryIds": t.LibraryIDs, "parentalRatingMax": t.ParentalRatingMax, "blockUnrated": t.BlockUnrated,
		"blockedTags": t.BlockedTags, "blockedKeywords": t.BlockedKeywords}
}

// normalizedTemplate trims the template's tags and keywords and drops
// repeats, keeping their order.
func normalizedTemplate(in domain.AccessTemplateInput) domain.AccessTemplateInput {
	clean := func(values []string) []string {
		out := make([]string, 0, len(values))
		for _, v := range values {
			if v = strings.TrimSpace(v); !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	in.BlockedTags, in.BlockedKeywords = clean(in.BlockedTags), clean(in.BlockedKeywords)
	libraries := slices.Clone(in.LibraryIDs)
	slices.Sort(libraries)
	in.LibraryIDs = libraries
	return in
}

// ListAccessTemplates lists the access templates by name. Administrators
// only.
func (s *Store) ListAccessTemplates(ctx context.Context, actor domain.Actor) ([]domain.AccessTemplate, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t ORDER BY lower(t.name),t.id LIMIT $1`, domain.AccessTemplatesMax)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]domain.AccessTemplate, 0)
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, storageError(tx.Commit(ctx))
}

// accessTemplateLibraries replaces the libraries of a template.
func accessTemplateLibraries(ctx context.Context, tx pgx.Tx, id string, libraries []string) error {
	if err := accessLibrariesExist(ctx, tx, libraries); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM access_template_libraries WHERE template_id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	_, err := tx.Exec(ctx, `INSERT INTO access_template_libraries(template_id,library_id) SELECT $1::uuid,unnest($2::uuid[])`, id, libraries)
	return storageError(err)
}

// CreateAccessTemplate stores a template and audits
// access.template_created. A name taken by another template, compared
// case-insensitively, or more than AccessTemplatesMax templates is
// ErrConflict.
func (s *Store) CreateAccessTemplate(ctx context.Context, actor domain.Actor, in domain.AccessTemplateInput) (domain.AccessTemplate, error) {
	if !in.Valid() {
		return domain.AccessTemplate{}, domain.ErrInvalid
	}
	in = normalizedTemplate(in)
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessTemplate{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE access_templates IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return domain.AccessTemplate{}, storageError(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM access_templates`).Scan(&count); err != nil {
		return domain.AccessTemplate{}, storageError(err)
	}
	if count >= domain.AccessTemplatesMax {
		return domain.AccessTemplate{}, domain.ErrConflict
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO access_templates(name,rating_max,block_unrated,blocked_tags,blocked_keywords) VALUES($1,$2,$3,$4,$5) RETURNING id::text`,
		in.Name, in.ParentalRatingMax, in.BlockUnrated, in.BlockedTags, in.BlockedKeywords).Scan(&id); err != nil {
		return domain.AccessTemplate{}, storageError(err)
	}
	if err = accessTemplateLibraries(ctx, tx, id, in.LibraryIDs); err != nil {
		return domain.AccessTemplate{}, err
	}
	t, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t WHERE t.id=$1::uuid`, id))
	if err != nil {
		return domain.AccessTemplate{}, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.template_created", Actor: actor, After: accessTemplateAudit(t)}); err != nil {
		return domain.AccessTemplate{}, err
	}
	return t, storageError(tx.Commit(ctx))
}

// UpdateAccessTemplate replaces a template and audits
// access.template_updated; an unchanged template is a no-op without an
// audit row. Users the template was applied to keep what they received.
func (s *Store) UpdateAccessTemplate(ctx context.Context, actor domain.Actor, id string, in domain.AccessTemplateInput) (domain.AccessTemplate, error) {
	if !in.Valid() {
		return domain.AccessTemplate{}, domain.ErrInvalid
	}
	in = normalizedTemplate(in)
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessTemplate{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return domain.AccessTemplate{}, domain.ErrNotFound
	}
	before, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t WHERE t.id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return domain.AccessTemplate{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access_templates SET name=$2,rating_max=$3,block_unrated=$4,blocked_tags=$5,blocked_keywords=$6,updated_at=now() WHERE id=$1::uuid`,
		id, in.Name, in.ParentalRatingMax, in.BlockUnrated, in.BlockedTags, in.BlockedKeywords); err != nil {
		return domain.AccessTemplate{}, storageError(err)
	}
	if err = accessTemplateLibraries(ctx, tx, id, in.LibraryIDs); err != nil {
		return domain.AccessTemplate{}, err
	}
	after, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t WHERE t.id=$1::uuid`, id))
	if err != nil {
		return domain.AccessTemplate{}, err
	}
	if sameTemplate(before, after) {
		return before, nil
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.template_updated", Actor: actor, Before: accessTemplateAudit(before), After: accessTemplateAudit(after)}); err != nil {
		return domain.AccessTemplate{}, err
	}
	return after, storageError(tx.Commit(ctx))
}

func sameTemplate(a, b domain.AccessTemplate) bool {
	return a.Name == b.Name && slices.Equal(a.LibraryIDs, b.LibraryIDs) && sameContentAccess(a.ContentAccess, b.ContentAccess)
}

// DeleteAccessTemplate removes a template and audits
// access.template_deleted. Users it was applied to keep their settings.
func (s *Store) DeleteAccessTemplate(ctx context.Context, actor domain.Actor, id string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	t, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t WHERE t.id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM access_templates WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.template_deleted", Actor: actor, Before: accessTemplateAudit(t)}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// ApplyAccessTemplate gives every user of userIDs exactly the template's
// libraries and restrictions (ceiling, unrated override, blocked tags and
// keywords); item rules and time windows are kept (G48.7). With preview
// nothing is written. Applied, changed users are audited as
// user.library_access_replaced and user.content_access_changed and the
// application as access.template_applied with the counts.
func (s *Store) ApplyAccessTemplate(ctx context.Context, actor domain.Actor, templateID string, userIDs []string, preview bool) (domain.AccessChangePreview, error) {
	if !domain.ValidAccessUserIDs(userIDs) {
		return domain.AccessChangePreview{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(templateID) {
		return domain.AccessChangePreview{}, domain.ErrNotFound
	}
	t, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+accessTemplateColumns+` FROM access_templates t WHERE t.id=$1::uuid FOR SHARE`, templateID))
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	names, err := accessTargets(ctx, tx, userIDs)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	before, err := accessGrants(ctx, tx, userIDs)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	// Restrictions reach every library, so every item is compared.
	p, err := newAccessPreview(ctx, tx, userIDs, nil)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	changes := make(map[string]*domain.AccessUserChange, len(userIDs))
	for _, id := range userIDs {
		c := &domain.AccessUserChange{UserID: id, Name: names[id]}
		changes[id] = c
		if !slices.Equal(before[id], t.LibraryIDs) {
			if _, err = tx.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid`, id); err != nil {
				return domain.AccessChangePreview{}, storageError(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT $1::uuid,unnest($2::uuid[])`, id, t.LibraryIDs); err != nil {
				return domain.AccessChangePreview{}, storageError(err)
			}
		}
		if c.RestrictionsChanged, err = writeContentAccess(ctx, tx, actor, id, t.ContentAccess); err != nil {
			return domain.AccessChangePreview{}, err
		}
	}
	after, err := accessGrants(ctx, tx, userIDs)
	if err != nil {
		return domain.AccessChangePreview{}, err
	}
	grantDifference(changes, before, after)
	result := domain.AccessChangePreview{}
	if err = p.count(ctx, tx, &result, changes); err != nil {
		return domain.AccessChangePreview{}, err
	}
	accessChangeResult(&result, changes)
	if preview {
		return result, nil
	}
	if err = auditGrantChanges(ctx, tx, actor, changes, before, after); err != nil {
		return domain.AccessChangePreview{}, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.template_applied", Actor: actor,
		After: accessSummaryAudit(map[string]any{"templateId": t.ID, "name": t.Name, "userIds": userIDs}, result)}); err != nil {
		return domain.AccessChangePreview{}, err
	}
	result.Applied = true
	return result, storageError(tx.Commit(ctx))
}
