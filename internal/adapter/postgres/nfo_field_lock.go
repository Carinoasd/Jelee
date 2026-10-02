package postgres

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func readNFOFieldLocks(ctx context.Context, tx pgx.Tx, value *domain.ItemMetadata) error {
	rows, err := tx.Query(ctx, `SELECT field,origin FROM item_nfo_field_locks WHERE item_id=$1::uuid`, value.ItemID)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return storageError(err)
		}
		var origin domain.NFOFieldLockOrigin
		if json.Unmarshal(raw, &origin) != nil || !domain.ValidNFOFieldLockOrigin(origin) {
			return domain.ErrMetadataUnavailable
		}
		if name == "year" || name == "runtimeMinutes" || name == "rating" || name == "userRating" || domain.IsItemMetadataListField(name) || name == "actors" {
			index := -1
			for i := range value.Facts {
				if value.Facts[i].Field == name {
					index = i
					break
				}
			}
			if index < 0 {
				value.Facts = append(value.Facts, domain.ItemMetadataFact{Field: name, Source: "existing"})
				index = len(value.Facts) - 1
			}
			value.Facts[index].NFOLockOrigin = &origin
			continue
		}
		index := -1
		for i := range value.Fields {
			if value.Fields[i].Field == name {
				index = i
				break
			}
		}
		if index < 0 {
			value.Fields = append(value.Fields, domain.ItemMetadataField{Field: name, Source: "existing"})
			index = len(value.Fields) - 1
		}
		value.Fields[index].NFOLockOrigin = &origin
	}
	if err := rows.Err(); err != nil {
		return storageError(err)
	}
	order := map[string]int{}
	for i, name := range domain.ItemMetadataFieldNames() {
		order[name] = i
	}
	sort.Slice(value.Facts, func(i, j int) bool { return value.Facts[i].Field < value.Facts[j].Field })
	sort.Slice(value.Fields, func(i, j int) bool { return order[value.Fields[i].Field] < order[value.Fields[j].Field] })
	return nil
}

func writeNFOFieldLocks(ctx context.Context, tx pgx.Tx, scope domain.NFOItemScope, fields domain.NFOItemFields, identity string) error {
	origin := domain.NFOFieldLockOrigin{SourceID: scope.SourceID, RootID: scope.RootID, Generation: scope.Generation, Stamp: domain.NFOItemObservationStamp{Size: fields.Stamp.Size, ModifiedUnixNano: fields.Stamp.ModifiedUnixNano, SHA256: fields.Stamp.SHA256, FingerprintVersion: fields.Stamp.FingerprintVersion}, IdentityDigest: identity, Projection: fields.Version, ReadAt: fields.ReadAt.UTC(), Locked: true}
	if !domain.ValidNFOFieldLockOrigin(origin) {
		return domain.ErrInvalid
	}
	raw, err := json.Marshal(origin)
	if err != nil {
		return domain.ErrInvalid
	}
	for _, field := range domain.NFOItemFieldNames(fields.Version) {
		if !domain.NFOFieldLocked(fields, field) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO item_nfo_field_locks(item_id,field,origin) VALUES($1::uuid,$2,$3::jsonb) ON CONFLICT(item_id,field) DO UPDATE SET origin=EXCLUDED.origin`, scope.ItemID, field, raw); err != nil {
			return storageError(err)
		}
	}
	return nil
}
