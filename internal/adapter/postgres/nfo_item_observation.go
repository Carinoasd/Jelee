package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func readConfirmedNFOObservation(ctx context.Context, tx pgx.Tx, item string, revision int64) (*domain.LastConfirmedNFOObservation, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT observation FROM item_nfo_observations WHERE item_id=$1::uuid`, item).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	var observation domain.LastConfirmedNFOObservation
	if json.Unmarshal(raw, &observation) != nil || !domain.ValidLastConfirmedNFOObservation(observation) || observation.AcceptedRevision > revision {
		return nil, domain.ErrMetadataUnavailable
	}
	return &observation, nil
}

func writeConfirmedNFOObservation(ctx context.Context, tx pgx.Tx, scope domain.NFOItemScope, state domain.NFOItemObservationState) error {
	observation, err := domain.ConfirmedNFOObservation(scope, state)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return domain.ErrInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO item_nfo_observations(item_id,observation) VALUES($1::uuid,$2::jsonb) ON CONFLICT(item_id) DO UPDATE SET observation=EXCLUDED.observation`, scope.ItemID, raw)
	return storageError(err)
}
