package postgres

import (
	"context"
	"path"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// A page has at most 16 queries and 16*129 ancestor proofs. Ancestors are
// fetched together, not with a database round trip per directory or query.
const legacyObservationPageSize = 16

type legacyQuery struct {
	key      domain.IgnoreProofCursor
	selected *string
}

// ReadLegacyIgnoreObservationPage restores the original query boundaries from
// a frozen ledger. A last query's root and directory form the next cursor.
// Restored evidence is historical; this method does not check live sources.
func (s *Store) ReadLegacyIgnoreObservationPage(ctx context.Context, l domain.JobLease, after domain.IgnoreProofCursor) ([]domain.LegacyIgnoreObservation, error) {
	if after != (domain.IgnoreProofCursor{}) && (!domain.ValidID(after.RootID) || !validScanPath(after.Directory, true)) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT frozen AND NOT invalidated AND inventory_generation=$2 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID, epoch).Scan(&ready)
	if err != nil {
		return nil, storageError(err)
	}
	if !ready {
		return nil, domain.ErrConflict
	}
	result, err := legacyObservationPage(ctx, tx, l.Job.ID, after)
	if err != nil {
		return nil, err
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return nil, err
	}
	return result, nil
}

func legacyObservationPage(ctx context.Context, tx pgx.Tx, id string, after domain.IgnoreProofCursor) ([]domain.LegacyIgnoreObservation, error) {
	rows, err := tx.Query(ctx, `SELECT root_id::text,directory,selected_directory,proof_version FROM job_ignore_legacy_queries WHERE job_id=$1::uuid AND (root_id,directory)>(COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$3 COLLATE "C") ORDER BY job_ignore_legacy_queries.root_id,directory LIMIT $4`, id, after.RootID, after.Directory, legacyObservationPageSize)
	if err != nil {
		return nil, storageError(err)
	}
	var queries []legacyQuery
	for rows.Next() {
		var q legacyQuery
		var version string
		if err = rows.Scan(&q.key.RootID, &q.key.Directory, &q.selected, &version); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		if version != domain.LegacyIgnoreProofVersion || !domain.ValidID(q.key.RootID) || !validScanPath(q.key.Directory, true) {
			rows.Close()
			return nil, domain.ErrDatabase
		}
		queries = append(queries, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return restoreLegacyQueries(ctx, tx, id, queries)
}

func restoreLegacyQueries(ctx context.Context, tx pgx.Tx, id string, queries []legacyQuery) ([]domain.LegacyIgnoreObservation, error) {
	var rows pgx.Rows
	var err error
	var roots, names []string
	chains := make([][]domain.IgnoreProofCursor, len(queries))
	unique := make(map[domain.IgnoreProofCursor]bool)
	for i, q := range queries {
		name := q.key.Directory
		for {
			key := domain.IgnoreProofCursor{RootID: q.key.RootID, Directory: name}
			chains[i] = append(chains[i], key)
			if len(chains[i]) > 129 {
				return nil, domain.ErrDatabase
			}
			if !unique[key] {
				unique[key] = true
				roots = append(roots, key.RootID)
				names = append(names, key.Directory)
			}
			if name == "." {
				break
			}
			name = path.Dir(name)
		}
		slices.Reverse(chains[i])
	}
	proofs := make(map[domain.IgnoreProofCursor]domain.LegacyIgnoreDirectoryProof, len(unique))
	if len(names) > 0 {
		rows, err = tx.Query(ctx, `SELECT p.root_id::text,p.directory,p.parent_identity,p.identity,p.checked,p.rule_present,p.rule_identity,p.rule_size,p.rule_modified_nano,p.rule_sha256 FROM unnest($2::uuid[],$3::text[]) AS wanted(root_id,directory) JOIN job_ignore_legacy_proofs p ON p.job_id=$1::uuid AND p.root_id=wanted.root_id AND p.directory=wanted.directory`, id, roots, names)
		if err != nil {
			return nil, storageError(err)
		}
		for rows.Next() {
			p, e := scanLegacyProof(rows)
			if e != nil {
				rows.Close()
				return nil, e
			}
			proofs[domain.IgnoreProofCursor{RootID: p.RootID, Directory: p.Directory}] = p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, storageError(err)
		}
	}
	var result []domain.LegacyIgnoreObservation
	for i, q := range queries {
		chain := make([]domain.LegacyIgnoreDirectoryProof, len(chains[i]))
		for j, key := range chains[i] {
			p, ok := proofs[key]
			if !ok {
				return nil, domain.ErrDatabase
			}
			chain[j] = p
		}
		o, e := domain.RestoreLegacyIgnoreObservation(q.key.Directory, q.selected, chain)
		if e != nil {
			return nil, domain.ErrDatabase
		}
		result = append(result, o)
	}
	return result, nil
}
