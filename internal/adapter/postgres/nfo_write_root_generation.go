package postgres

import (
	"context"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Dirty or old schemas still permit private historical observations. A missing
// column becomes zero, never today's generation. Avoid JSON encoding a complete
// row, which would duplicate all bounded XML payloads merely to inspect shape.
func nfoHistoricalRootColumns(ctx context.Context, tx pgx.Tx, columns, table string) (string, error) {
	if table != "nfo_write_preparations" && table != "nfo_write_entries" {
		return "", domain.ErrInvalid
	}
	var present bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute WHERE attrelid=to_regclass($1) AND attname='root_generation' AND NOT attisdropped)`, table).Scan(&present); err != nil {
		return "", storageError(err)
	}
	if !present {
		columns = strings.ReplaceAll(columns, "COALESCE(root_generation,0)", "0")
		columns = strings.ReplaceAll(columns, "COALESCE(e.root_generation,0)", "0")
	}
	return columns, nil
}
