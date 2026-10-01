package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type cachedNFO struct {
	stamp                                     domain.NFOStamp
	library, digest, status                   string
	libraryGeneration, rootGeneration, charge int64
	expires                                   time.Time
	summary                                   domain.NFOValidationSummary
	raw                                       []byte
}

func decodeNFOSummary(raw []byte) (domain.NFOValidationSummary, error) {
	var summary domain.NFOValidationSummary
	if len(raw) > domain.NFOSummaryMaxBytes {
		return summary, domain.ErrDatabase
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 12 {
		return summary, domain.ErrDatabase
	}
	for _, key := range []string{"schemaVersion", "status", "encoding", "encodingGuessed", "root", "entries", "failureCode", "warningCount", "errorCount", "issueCount", "issuesTruncated", "issues"} {
		field, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return summary, domain.ErrDatabase
		}
	}
	var issues []map[string]json.RawMessage
	if json.Unmarshal(fields["issues"], &issues) != nil {
		return summary, domain.ErrDatabase
	}
	for _, issue := range issues {
		if len(issue) != 4 {
			return summary, domain.ErrDatabase
		}
		for _, key := range []string{"severity", "code", "field", "entry"} {
			value, ok := issue[key]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return summary, domain.ErrDatabase
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&summary) != nil || domain.ValidateNFOSummary(summary) != nil {
		return domain.NFOValidationSummary{}, domain.ErrDatabase
	}
	return summary, nil
}
func readCachedNFO(ctx context.Context, tx pgx.Tx, root, path string) (cachedNFO, error) {
	var c cachedNFO
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT library_id::text,size,modified_unix_nano,encode(source_sha256,'hex'),fingerprint_version,encode(identity_digest,'hex'),library_generation,root_generation,status,summary::text,expires_at,charge_bytes FROM nfo_cache WHERE root_id=$1::uuid AND relative_path=$2 FOR UPDATE`, root, path).Scan(&c.library, &c.stamp.Size, &c.stamp.ModifiedUnixNano, &c.stamp.SHA256, &c.stamp.FingerprintVersion, &c.digest, &c.libraryGeneration, &c.rootGeneration, &c.status, &raw, &c.expires, &c.charge)
	if err != nil {
		return cachedNFO{}, storageError(err)
	}
	c.raw = raw
	if c.charge != int64(domain.NFORowAllowanceBytes+len(raw)) {
		return cachedNFO{}, domain.ErrDatabase
	}
	return c, nil
}
func nfoCacheMatches(c cachedNFO, p domain.NFOPhase, e nfoEntry, candidate domain.NFOCandidate) bool {
	return c.library == p.LibraryID && c.stamp == candidate.Stamp && c.digest == p.IdentityDigest && c.libraryGeneration == p.LibraryGeneration && c.rootGeneration == e.rootGeneration
}
func nfoLookup(ctx context.Context, tx pgx.Tx, p domain.NFOPhase, e nfoEntry, candidate domain.NFOCandidate) (string, cachedNFO, error) {
	c, err := readCachedNFO(ctx, tx, e.Inventory.RootID, e.Inventory.Path)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.NFOLookupMiss, c, nil
	}
	if err != nil {
		return "", cachedNFO{}, err
	}
	if !nfoCacheMatches(c, p, e, candidate) {
		return domain.NFOLookupMiss, c, nil
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, c.expires).Scan(&live); err != nil {
		return "", cachedNFO{}, storageError(err)
	}
	if !live {
		return domain.NFOLookupMiss, c, nil
	}
	c.summary, err = decodeNFOSummary(c.raw)
	if err != nil {
		return "", cachedNFO{}, err
	}
	if c.summary.Status != c.status {
		return "", cachedNFO{}, domain.ErrDatabase
	}
	if c.status == domain.NFOStatusInvalid {
		return domain.NFOLookupNegativeHit, c, nil
	}
	return domain.NFOLookupHit, c, nil
}
func (s *Store) LookupNFOBatch(parent context.Context, l domain.JobLease, token domain.NFOPageToken, candidates []domain.NFOCandidate) ([]domain.NFOLookup, error) {
	if err := domain.ValidateNFOPageToken(token); err != nil {
		return nil, err
	}
	if err := domain.ValidateNFOCandidateBatch(candidates); err != nil {
		return nil, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, true)
	if err != nil {
		return nil, err
	}
	entries, err := checkNFOPrefix(ctx, tx, p, token, candidates, true)
	if err != nil {
		return nil, err
	}
	result := make([]domain.NFOLookup, 0, len(candidates))
	for i, e := range entries {
		kind, _, e2 := nfoLookup(ctx, tx, p, e, candidates[i])
		if e2 != nil {
			return nil, e2
		}
		result = append(result, domain.NFOLookup{InventoryID: candidates[i].InventoryID, Kind: kind})
	}
	if _, err = commitNFOPhase(ctx, tx, l, p); err != nil {
		return nil, err
	}
	return result, nil
}
