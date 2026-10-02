package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func readItemMetadataFacts(ctx context.Context, tx pgx.Tx, value *domain.ItemMetadata) error {
	value.Facts = []domain.ItemMetadataFact{}
	rows, err := tx.Query(ctx, `SELECT field,value,source,locked,updated_at,nfo_origin FROM item_metadata_facts WHERE item_id=$1::uuid ORDER BY field`, value.ItemID)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var fact domain.ItemMetadataFact
		var origin []byte
		if err := rows.Scan(&fact.Field, &fact.Value, &fact.Source, &fact.Locked, &fact.UpdatedAt, &origin); err != nil {
			return storageError(err)
		}
		if !domain.ValidItemMetadataFactValue(fact.Field, fact.Value) {
			return domain.ErrMetadataUnavailable
		}
		if len(origin) > 0 {
			var proof domain.NFOItemOrigin
			if json.Unmarshal(origin, &proof) != nil || !domain.ValidNFOItemOrigin(proof) || (proof.Projection != domain.NFOItemYearFieldsVersion && proof.Projection != domain.NFOItemNumericFieldsVersion && proof.Projection != domain.NFOItemListFieldsVersion) || !slices.Contains(domain.NFOItemFieldNames(proof.Projection), fact.Field) {
				return domain.ErrMetadataUnavailable
			}
			fact.NFOOrigin = &proof
		}
		value.Facts = append(value.Facts, fact)
	}
	return storageError(rows.Err())
}

func writeItemMetadataFact(ctx context.Context, tx pgx.Tx, item string, fact domain.ItemMetadataFact, now time.Time) error {
	if !domain.ValidItemMetadataFactValue(fact.Field, fact.Value) {
		return domain.ErrInvalid
	}
	if (fact.Field == "rating" || fact.Field == "userRating") && string(fact.Value) != "null" {
		var n float64
		_ = json.Unmarshal(fact.Value, &n)
		fact.Value, _ = json.Marshal(n)
	}
	var origin []byte
	if fact.NFOOrigin != nil {
		var err error
		origin, err = json.Marshal(fact.NFOOrigin)
		if err != nil {
			return domain.ErrInvalid
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO item_metadata_facts(item_id,field,value,source,locked,updated_at,nfo_origin) VALUES($1::uuid,$2,$3::jsonb,$4,$5,$6,$7::jsonb) ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value,source=EXCLUDED.source,locked=EXCLUDED.locked,updated_at=EXCLUDED.updated_at,nfo_origin=EXCLUDED.nfo_origin`, item, fact.Field, []byte(fact.Value), fact.Source, fact.Locked, now, origin)
	return storageError(err)
}

func applyNFOFacts(ctx context.Context, tx pgx.Tx, before domain.ItemMetadata, scope domain.NFOItemScope, fields domain.NFOItemFields, identity string, now time.Time, result *domain.MetadataApplyResult) error {
	incomingFacts := []domain.ItemMetadataFact{}
	for _, incoming := range fields.Facts {
		incomingFacts = append(incomingFacts, domain.ItemMetadataFact{Field: incoming.Field, Value: json.RawMessage(strconv.Itoa(incoming.Value))})
	}
	for _, incoming := range fields.NumberFacts {
		raw, err := json.Marshal(incoming.Value)
		if err != nil {
			return domain.ErrInvalid
		}
		incomingFacts = append(incomingFacts, domain.ItemMetadataFact{Field: incoming.Field, Value: raw})
	}
	for _, list := range fields.Lists {
		raw, err := json.Marshal(list.Values)
		if err != nil {
			return domain.ErrInvalid
		}
		incomingFacts = append(incomingFacts, domain.ItemMetadataFact{Field: list.Field, Value: raw})
	}
	for _, incoming := range incomingFacts {
		old := domain.ItemMetadataFact{Field: incoming.Field}
		for _, existing := range before.Facts {
			if existing.Field == incoming.Field {
				old = existing
				break
			}
		}
		reason := ""
		if old.Locked || old.NFOOrigin != nil && old.NFOOrigin.Locked || old.NFOLockOrigin != nil && old.NFOLockOrigin.Locked {
			reason = "locked"
		} else if old.Source == "manual" {
			reason = "manual"
		}
		if reason != "" {
			result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: incoming.Field, Reason: reason})
			continue
		}
		origin := &domain.NFOItemOrigin{SourceID: scope.SourceID, RootID: scope.RootID, Generation: scope.Generation, SHA256: fields.Stamp.SHA256, IdentityDigest: identity, Projection: fields.Version, ReadAt: fields.ReadAt.UTC(), Locked: domain.NFOFieldLocked(fields, incoming.Field)}
		fact := domain.ItemMetadataFact{Field: incoming.Field, Value: incoming.Value, Source: "nfo", NFOOrigin: origin}
		if err := writeItemMetadataFact(ctx, tx, scope.ItemID, fact, now); err != nil {
			return err
		}
		result.Applied = append(result.Applied, incoming.Field)
	}
	return nil
}

func updateManualFacts(ctx context.Context, tx pgx.Tx, item string, before domain.ItemMetadata, patches []domain.ItemMetadataFactPatch, now time.Time) error {
	for _, patch := range patches {
		fact := domain.ItemMetadataFact{Field: patch.Field, Value: json.RawMessage("null"), Source: "existing"}
		for _, old := range before.Facts {
			if old.Field == patch.Field {
				fact = old
				break
			}
		}
		if patch.Value != nil {
			fact.Value = append(json.RawMessage(nil), patch.Value...)
			fact.Source = "manual"
			fact.NFOOrigin = nil
			fact.NFOLockOrigin = nil
			if _, err := tx.Exec(ctx, `DELETE FROM item_nfo_field_locks WHERE item_id=$1::uuid AND field=$2`, item, patch.Field); err != nil {
				return storageError(err)
			}
		}
		if patch.Locked != nil {
			fact.Locked = *patch.Locked
		}
		if fact.Value == nil {
			fact.Value = json.RawMessage("null")
		}
		if err := writeItemMetadataFact(ctx, tx, item, fact, now); err != nil {
			return err
		}
	}
	return nil
}
