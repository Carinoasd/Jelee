package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func readItemMetadata(ctx context.Context, tx pgx.Tx, item string, lock bool) (domain.ItemMetadata, error) {
	value := domain.ItemMetadata{ItemID: item, Fields: []domain.ItemMetadataField{}}
	var title string
	query := `SELECT i.library_id::text,i.title,i.kind,l.nfo_mode FROM items i JOIN libraries l ON l.id=i.library_id WHERE i.id=$1::uuid`
	if lock {
		query += ` FOR UPDATE OF i`
	}
	if err := tx.QueryRow(ctx, query, item).Scan(&value.LibraryID, &title, &value.Kind, &value.NFOMode); err != nil {
		return value, storageError(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM item_metadata_state WHERE item_id=$1::uuid),1)`, item).Scan(&value.Revision); err != nil {
		return value, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT field,value,source,locked,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at,nfo_origin FROM item_metadata_fields WHERE item_id=$1::uuid ORDER BY CASE field WHEN 'title' THEN 0 WHEN 'originalTitle' THEN 1 WHEN 'overview' THEN 2 ELSE 3 END`, item)
	if err != nil {
		return value, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var field domain.ItemMetadataField
		var resource, sourceURL, language *string
		var id *int32
		var fetched *time.Time
		var nfoOrigin []byte
		if err := rows.Scan(&field.Field, &field.Value, &field.Source, &field.Locked, &field.UpdatedAt, &resource, &id, &sourceURL, &language, &fetched, &nfoOrigin); err != nil {
			return value, storageError(err)
		}
		if resource != nil {
			field.ProviderOrigin = &domain.MetadataProviderOrigin{Resource: *resource, ProviderID: *id, SourceURL: *sourceURL, RequestedLanguage: *language, FetchedAt: fetched.UTC()}
		}
		if len(nfoOrigin) > 0 {
			var origin domain.NFOItemOrigin
			if json.Unmarshal(nfoOrigin, &origin) != nil || !domain.ValidNFOItemOrigin(origin) {
				return value, domain.ErrMetadataUnavailable
			}
			field.NFOOrigin = &origin
		}
		value.Fields = append(value.Fields, field)
	}
	if err := rows.Err(); err != nil {
		return value, storageError(err)
	}
	if len(value.Fields) == 0 || value.Fields[0].Field != "title" {
		value.Fields = append([]domain.ItemMetadataField{{Field: "title", Value: title, Source: "existing"}}, value.Fields...)
	}
	return value, nil
}

func (s *Store) ItemMetadata(ctx context.Context, actor domain.Actor, item string) (domain.ItemMetadata, error) {
	if !domain.ValidID(item) {
		return domain.ItemMetadata{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	defer tx.Rollback(ctx)
	value, err := readItemMetadata(ctx, tx, item, false)
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	return value, storageError(tx.Commit(ctx))
}

func (s *Store) UpdateItemMetadata(ctx context.Context, actor domain.Actor, item string, expected int64, patches []domain.ItemMetadataPatch) (domain.ItemMetadata, error) {
	if !domain.ValidItemMetadataPatches(item, expected, patches) {
		return domain.ItemMetadata{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readItemMetadata(ctx, tx, item, true)
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	if before.Revision != expected {
		return domain.ItemMetadata{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, item, expected+1); err != nil {
		return domain.ItemMetadata{}, storageError(err)
	}
	now := time.Now().UTC()
	for _, patch := range patches {
		field := domain.ItemMetadataField{Field: patch.Field, Source: "existing"}
		for _, old := range before.Fields {
			if old.Field == patch.Field {
				field = old
				break
			}
		}
		if patch.Value != nil {
			field.Value = *patch.Value
			field.Source = "manual"
			field.ProviderOrigin = nil
			field.NFOOrigin = nil
		}
		if patch.Locked != nil {
			field.Locked = *patch.Locked
		}
		if err = writeItemMetadataField(ctx, tx, item, field, now); err != nil {
			return domain.ItemMetadata{}, err
		}
	}
	after, err := readItemMetadata(ctx, tx, item, false)
	if err != nil {
		return domain.ItemMetadata{}, err
	}
	if err = auditAccount(ctx, tx, actor, "item.metadata_changed", item, before, after); err != nil {
		return domain.ItemMetadata{}, err
	}
	return after, storageError(tx.Commit(ctx))
}

func writeItemMetadataField(ctx context.Context, tx pgx.Tx, item string, field domain.ItemMetadataField, now time.Time) error {
	var resource, sourceURL, language *string
	var id *int32
	var fetched *time.Time
	if origin := field.ProviderOrigin; origin != nil {
		resource, sourceURL, language = &origin.Resource, &origin.SourceURL, &origin.RequestedLanguage
		id, fetched = &origin.ProviderID, &origin.FetchedAt
	}
	var nfoOrigin []byte
	if field.NFOOrigin != nil {
		var err error
		nfoOrigin, err = json.Marshal(field.NFOOrigin)
		if err != nil {
			return domain.ErrInvalid
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at,provider_resource,provider_id,provider_source_url,provider_language,provider_fetched_at,nfo_origin) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb) ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value,source=EXCLUDED.source,locked=EXCLUDED.locked,updated_at=EXCLUDED.updated_at,provider_resource=EXCLUDED.provider_resource,provider_id=EXCLUDED.provider_id,provider_source_url=EXCLUDED.provider_source_url,provider_language=EXCLUDED.provider_language,provider_fetched_at=EXCLUDED.provider_fetched_at,nfo_origin=EXCLUDED.nfo_origin`, item, field.Field, field.Value, field.Source, field.Locked, now, resource, id, sourceURL, language, fetched, nfoOrigin); err != nil {
		return storageError(err)
	}
	if field.Field == "title" {
		_, err := tx.Exec(ctx, `UPDATE items SET title=$2 WHERE id=$1::uuid`, item, field.Value)
		return storageError(err)
	}
	return nil
}
