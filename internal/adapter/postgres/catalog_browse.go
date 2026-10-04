package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// browsePrincipalSQL resolves the live user once per statement. A disabled
// or deleted user (or an unknown ID) yields no row, so every join on it is
// empty.
const browsePrincipalSQL = `WITH principal AS MATERIALIZED (
 SELECT ` + visibilityUserColumns + ` FROM users WHERE id=@user::uuid AND NOT disabled AND deleted_at IS NULL
)`

// browseMetadataSQL joins the display metadata of an item i: the sort
// title, overview and release date fields and the release year fact.
const browseMetadataSQL = ` LEFT JOIN item_parent_links p ON p.item_id=i.id
 LEFT JOIN item_metadata_fields fs ON fs.item_id=i.id AND fs.field='sortTitle'
 LEFT JOIN item_metadata_fields fo ON fo.item_id=i.id AND fo.field='overview'
 LEFT JOIN item_metadata_fields fd ON fd.item_id=i.id AND fd.field='date'
 LEFT JOIN item_metadata_facts fy ON fy.item_id=i.id AND fy.field='year'`

// browseYearSQL is the release year: the year fact, else the year of the
// release date.
const browseYearSQL = `CASE WHEN jsonb_typeof(fy.value)='number' THEN (fy.value::text)::int WHEN fd.value ~ '^[0-9]{4}-' THEN left(fd.value,4)::int END`

// libraryKindsSQL lists which top-level kinds the library in column holds.
// Each probe stops at the first matching item.
func libraryKindsSQL(column string) string {
	return `ARRAY(SELECT k FROM unnest(ARRAY['Movie','Series','Episode','HomeVideo']) WITH ORDINALITY AS t(k,n) WHERE EXISTS(SELECT 1 FROM items c WHERE c.library_id=` + column + ` AND c.kind=t.k) ORDER BY t.n)`
}

func (s *Store) ListLibraryViews(ctx context.Context, userID string) ([]domain.LibraryView, error) {
	rows, err := s.Pool.Query(ctx, browsePrincipalSQL+`
SELECT l.id::text,l.name,`+libraryKindsSQL("l.id")+` FROM principal u JOIN libraries l ON `+libraryVisibleSQL("l.id")+`
ORDER BY lower(l.name) COLLATE "C",l.id LIMIT @limit`, pgx.NamedArgs{"user": userID, "limit": domain.BrowseViewsMax})
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	views := make([]domain.LibraryView, 0)
	for rows.Next() {
		var view domain.LibraryView
		if err = rows.Scan(&view.ID, &view.Name, &view.ContentKinds); err != nil {
			return nil, storageError(err)
		}
		views = append(views, view)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return views, nil
}

func (s *Store) GetBrowseItem(ctx context.Context, userID, id string) (domain.BrowseItem, error) {
	var item domain.BrowseItem
	err := s.Pool.QueryRow(ctx, browsePrincipalSQL+`
SELECT i.id::text,i.library_id::text,COALESCE(p.parent_id,i.library_id)::text,i.kind,i.title,COALESCE(fs.value,''),COALESCE(fo.value,''),COALESCE(NULLIF(fd.value,''),''),COALESCE(`+browseYearSQL+`,0),'{}'::text[]
 FROM principal u JOIN items i ON i.id=@id::uuid`+browseMetadataSQL+`
 WHERE `+itemVisibleSQL("i.library_id", "i.id")+`
UNION ALL
SELECT l.id::text,l.id::text,'',@library,l.name,'','','',0,`+libraryKindsSQL("l.id")+` FROM principal u JOIN libraries l ON l.id=@id::uuid WHERE `+libraryVisibleSQL("l.id")+`
LIMIT 1`, pgx.NamedArgs{"user": userID, "id": id, "library": domain.BrowseKindLibrary}).Scan(
		&item.ID, &item.LibraryID, &item.ParentID, &item.Kind, &item.Title, &item.SortTitle, &item.Overview, &item.PremiereDate, &item.Year, &item.ContentKinds)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BrowseItem{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.BrowseItem{}, storageError(err)
	}
	if item.Kind == domain.BrowseKindLibrary {
		item.ParentID = ""
	}
	return item, nil
}

// Browse scopes, chosen after the parent was resolved. Children share their
// parent's library (enforced by item_parent_links), and the library grant is
// still checked on every returned row.
const (
	browseScopeAll         = `true`
	browseScopeLibraryTop  = `i.library_id=@parent::uuid AND NOT EXISTS(SELECT 1 FROM item_parent_links l WHERE l.item_id=i.id)`
	browseScopeLibraryAll  = `i.library_id=@parent::uuid`
	browseScopeChildren    = `i.id IN (SELECT l.item_id FROM item_parent_links l WHERE l.parent_id=@parent::uuid)`
	browseScopeDescendants = `i.id IN (SELECT l.item_id FROM item_parent_links l WHERE l.parent_id=@parent::uuid
  UNION SELECT l2.item_id FROM item_parent_links l1 JOIN item_parent_links l2 ON l2.parent_id=l1.item_id WHERE l1.parent_id=@parent::uuid)`
)

// browseSortSQL maps the supported sort keys to expressions over the
// described rows. Only these fixed fragments reach the statement text.
var browseSortSQL = map[domain.BrowseSortKey]string{
	domain.BrowseSortName:           `lower(COALESCE(NULLIF(sort_title,''),title)) COLLATE "C"`,
	domain.BrowseSortPremiereDate:   `premiere`,
	domain.BrowseSortProductionYear: `production_year`,
}

func (s *Store) BrowseItems(ctx context.Context, userID string, q domain.BrowseQuery) (domain.BrowsePage, error) {
	if !domain.ValidBrowseQuery(q) {
		return domain.BrowsePage{}, domain.ErrInvalid
	}
	args := pgx.NamedArgs{"user": userID, "parent": q.ParentID, "library": q.LibraryID, "kinds": append([]string{}, q.Kinds...), "search": "",
		"overview": q.WithOverview, "limit": q.Limit, "offset": q.Offset}
	if q.SearchTerm != "" {
		args["search"] = "%" + escapeLike(q.SearchTerm) + "%"
	}
	scope := browseScopeAll
	if q.Scope != domain.BrowseAll {
		var kind string
		err := s.Pool.QueryRow(ctx, browsePrincipalSQL+`
SELECT 'library' FROM principal u JOIN libraries l ON l.id=@parent::uuid WHERE `+libraryVisibleSQL("l.id")+`
UNION ALL
SELECT 'item' FROM principal u JOIN items i ON i.id=@parent::uuid WHERE `+itemVisibleSQL("i.library_id", "i.id")+`
LIMIT 1`, args).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			// A missing and an invisible parent look the same: nothing.
			return domain.BrowsePage{Items: []domain.BrowseItem{}}, nil
		}
		if err != nil {
			return domain.BrowsePage{}, storageError(err)
		}
		recursive := q.Scope == domain.BrowseParentRecursive
		switch {
		case kind == "library" && recursive:
			scope = browseScopeLibraryAll
		case kind == "library":
			scope = browseScopeLibraryTop
		case recursive:
			scope = browseScopeDescendants
		default:
			scope = browseScopeChildren
		}
	}
	matched := browsePrincipalSQL + `, matched AS (
 SELECT i.id,i.library_id,i.title,i.kind FROM principal u JOIN items i ON ` + scope + `
 WHERE ` + itemVisibleSQL("i.library_id", "i.id") + `
  AND (@library::text='' OR i.library_id=NULLIF(@library::text,'')::uuid)
  AND (cardinality(@kinds::text[])=0 OR i.kind=ANY(@kinds::text[]))
  AND (@search::text='' OR i.title ILIKE @search::text ESCAPE '\')
)`
	var order strings.Builder
	sorts := q.Sort
	if len(sorts) == 0 {
		sorts = []domain.BrowseSort{{Key: domain.BrowseSortName}}
	}
	for _, sort := range sorts {
		order.WriteString(browseSortSQL[sort.Key])
		if sort.Descending {
			order.WriteString(" DESC NULLS LAST,")
		} else {
			order.WriteString(" ASC NULLS FIRST,")
		}
	}
	order.WriteString("id")
	rows, err := s.Pool.Query(ctx, matched+`, described AS (
 SELECT i.id,i.library_id,COALESCE(p.parent_id,i.library_id) AS parent,i.kind,i.title,fs.value AS sort_title,
  CASE WHEN @overview::boolean THEN fo.value END AS overview,NULLIF(fd.value,'') AS premiere,`+browseYearSQL+` AS production_year
 FROM matched i`+browseMetadataSQL+`
)
SELECT id::text,library_id::text,parent::text,kind,title,COALESCE(sort_title,''),COALESCE(overview,''),COALESCE(premiere,''),COALESCE(production_year,0),count(*) OVER()
 FROM described ORDER BY `+order.String()+` LIMIT @limit OFFSET @offset`, args)
	if err != nil {
		return domain.BrowsePage{}, storageError(err)
	}
	defer rows.Close()
	page := domain.BrowsePage{Items: make([]domain.BrowseItem, 0, min(q.Limit, 64))}
	for rows.Next() {
		var item domain.BrowseItem
		if err = rows.Scan(&item.ID, &item.LibraryID, &item.ParentID, &item.Kind, &item.Title, &item.SortTitle, &item.Overview, &item.PremiereDate, &item.Year, &page.Total); err != nil {
			return domain.BrowsePage{}, storageError(err)
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return domain.BrowsePage{}, storageError(err)
	}
	rows.Close()
	if len(page.Items) == 0 && q.Offset > 0 {
		// The window count is only seen with at least one row; a page past
		// the end still reports the total.
		if err = s.Pool.QueryRow(ctx, matched+` SELECT count(*) FROM matched`, args).Scan(&page.Total); err != nil {
			return domain.BrowsePage{}, storageError(err)
		}
	}
	return page, nil
}

// escapeLike quotes the LIKE wildcards and the escape character itself so a
// search term only ever matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// GetItemDetails reads the display metadata of one visible item with the
// same principal, grant and metadata joins as GetBrowseItem. Only display
// values leave this statement: no NFO origins, provider origins, roots or
// paths.
func (s *Store) GetItemDetails(ctx context.Context, userID, id string) (domain.ItemDetailsRecord, error) {
	var r domain.ItemDetailsRecord
	var observation []byte
	item := &r.Item
	err := s.Pool.QueryRow(ctx, browsePrincipalSQL+`
SELECT i.id::text,i.library_id::text,COALESCE(p.parent_id,i.library_id)::text,i.kind,i.title,COALESCE(fs.value,''),COALESCE(fo.value,''),COALESCE(NULLIF(fd.value,''),''),COALESCE(`+browseYearSQL+`,0),
 COALESCE((SELECT f.value FROM item_metadata_fields f WHERE f.item_id=i.id AND f.field='originalTitle'),''),
 COALESCE((SELECT f.value FROM item_metadata_fields f WHERE f.item_id=i.id AND f.field='tagline'),''),
 (SELECT f.value FROM item_metadata_facts f WHERE f.item_id=i.id AND f.field='genres'),
 (SELECT f.value FROM item_metadata_facts f WHERE f.item_id=i.id AND f.field='uniqueIds'),
 (SELECT o.observation FROM item_nfo_observations o WHERE o.item_id=i.id),
 COALESCE((SELECT m.revision FROM item_metadata_state m WHERE m.item_id=i.id),1),
 ARRAY(SELECT f.field FROM item_metadata_fields f WHERE f.item_id=i.id AND f.source='nfo' AND f.value<>'' AND f.field=ANY(@public::text[])
  UNION SELECT f.field FROM item_metadata_facts f WHERE f.item_id=i.id AND f.source='nfo' AND f.value<>'null'::jsonb AND f.field=ANY(@public::text[]))
 FROM principal u JOIN items i ON i.id=@id::uuid`+browseMetadataSQL+`
 WHERE `+itemVisibleSQL("i.library_id", "i.id"), pgx.NamedArgs{"user": userID, "id": id, "public": domain.ItemDetailsNFOFieldNames()}).Scan(
		&item.ID, &item.LibraryID, &item.ParentID, &item.Kind, &item.Title, &item.SortTitle, &item.Overview, &item.PremiereDate, &item.Year,
		&r.OriginalTitle, &r.Tagline, &r.Genres, &r.UniqueIDs, &observation, &r.Revision, &r.NFOFields)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ItemDetailsRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ItemDetailsRecord{}, storageError(err)
	}
	if len(observation) > 0 {
		var value domain.LastConfirmedNFOObservation
		// An undecodable observation is reported as unread by the domain
		// projection rather than failing the whole view.
		if json.Unmarshal(observation, &value) == nil {
			r.Observation = &value
		}
	}
	return r, nil
}
