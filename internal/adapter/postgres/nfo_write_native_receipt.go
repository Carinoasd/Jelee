package postgres

import (
	"context"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Missing historical columns remain NULL, never a receipt from today's files.
// Only column metadata is inspected; no whole private XML row is JSON encoded.
func nfoHistoricalNativeColumns(ctx context.Context, tx pgx.Tx, columns, table string) (string, error) {
	columns, err := nfoHistoricalRootColumns(ctx, tx, columns, table)
	if err != nil {
		return "", err
	}
	var present bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute WHERE attrelid=to_regclass($1) AND attname='native_receipt' AND NOT attisdropped)`, table).Scan(&present); err != nil {
		return "", storageError(err)
	}
	if !present {
		columns = strings.ReplaceAll(columns, "COALESCE(native_receipt,NULL::bytea)", "NULL::bytea")
		columns = strings.ReplaceAll(columns, "COALESCE(e.native_receipt,NULL::bytea)", "NULL::bytea")
	}
	return columns, nil
}

func readNFONativeReceipt(data []byte) (domain.NFONativePreparationReceipt, error) {
	if data == nil {
		return domain.NFONativePreparationReceipt{}, nil
	}
	receipt, err := domain.ParseNFONativePreparationReceipt(data)
	if err != nil {
		return domain.NFONativePreparationReceipt{}, domain.ErrDatabase
	}
	return receipt, nil
}
