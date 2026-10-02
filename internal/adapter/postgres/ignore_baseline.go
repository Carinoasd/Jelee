package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"path"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type ignoreComparison struct {
	token                domain.IgnoreBaselineToken
	baselineCount, epoch int64
	comparable, complete bool
	counts               domain.IgnoreComparisonCounts
}

const ignoreBaselinePageSQL = `WITH raw AS MATERIALIZED (
 SELECT b.root_id,b.path FROM library_inventory_baseline_data b WHERE b.library_id=$1::uuid AND b.snapshot_id=(SELECT active_inventory_snapshot FROM libraries WHERE id=$1::uuid)
 AND (b.root_id,b.path COLLATE "C")>(COALESCE(NULLIF($3,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$4 COLLATE "C")
 ORDER BY b.root_id,b.path COLLATE "C" LIMIT 128)
 SELECT raw.root_id::text,raw.path,i.id IS NOT NULL FROM raw LEFT JOIN LATERAL
 (SELECT id FROM job_inventory WHERE job_id=$2::uuid AND root_id=raw.root_id AND path=raw.path LIMIT 1) i
 ON true ORDER BY raw.root_id,raw.path COLLATE "C"`

func loadIgnoreComparison(ctx context.Context, tx pgx.Tx, id string) (ignoreComparison, error) {
	var c ignoreComparison
	c.token.JobID = id
	err := tx.QueryRow(ctx, `SELECT baseline_revision,inventory_generation,baseline_count,scope_comparable,sequence,COALESCE(after_root_id::text,''),after_path,observed,missing,excluded,unknown,completed FROM job_ignore_comparisons WHERE job_id=$1::uuid FOR UPDATE`, id).Scan(&c.token.BaselineRevision, &c.epoch, &c.baselineCount, &c.comparable, &c.token.Sequence, &c.token.AfterRootID, &c.token.AfterPath, &c.counts.Observed, &c.counts.Missing, &c.counts.Excluded, &c.counts.Unknown, &c.complete)
	return c, storageError(err)
}

func ignoreComparisonFence(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, int64, error) {
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
	if err != nil {
		return current, 0, 0, err
	}
	var valid bool
	var revision int64
	err = tx.QueryRow(ctx, `SELECT NOT m.invalidated AND m.inventory_generation=$2,l.inventory_baseline_revision FROM job_ignore_manifests m JOIN jobs j ON j.id=m.job_id JOIN libraries l ON l.id=j.library_id WHERE m.job_id=$1::uuid`, l.Job.ID, epoch).Scan(&valid, &revision)
	if err != nil {
		return current, 0, 0, storageError(err)
	}
	if !valid {
		return current, 0, 0, domain.ErrInventoryInvalidated
	}
	return current, epoch, revision, nil
}

// BeginIgnoreBaselineComparison freezes the already drained inventory. It
// does not freeze the source manifest: classifying unseen paths can discover
// additional ancestor proofs. No caller completion flag is accepted.
func (s *Store) BeginIgnoreBaselineComparison(ctx context.Context, l domain.JobLease) error {
	return s.beginIgnoreBaselineComparison(ctx, l, false)
}

func (s *Store) beginIgnoreBaselineComparison(ctx context.Context, l domain.JobLease, family bool) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, family)
	if err != nil {
		return err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err == nil {
		if c.epoch != epoch || c.token.BaselineRevision != revision {
			return domain.ErrInventoryInvalidated
		}
		return commitIgnoreManifest(ctx, tx, current, epoch)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	covered, skipped, err := inventoryCoverage(ctx, tx, current)
	if err != nil {
		return err
	}
	if !covered || skipped || current.Job.Skipped != 0 {
		return domain.ErrConflict
	}
	var rootsCovered bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.library_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM job_ignore_proofs p WHERE p.job_id=$1::uuid AND p.root_id=r.id AND p.directory='.' AND NOT p.missing_directory))`, l.Job.ID, current.Job.LibraryID).Scan(&rootsCovered)
	if err != nil {
		return storageError(err)
	}
	if !rootsCovered {
		return domain.ErrConflict
	}
	if family {
		if err = familyBaselineRootCoverage(ctx, tx, current); err != nil {
			return err
		}
	}
	var total, unknown int64
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE NOT attributes_known OR inventory_generation IS DISTINCT FROM $2::bigint) FROM library_inventory_baseline WHERE library_id=$1::uuid`, current.Job.LibraryID, epoch).Scan(&total, &unknown)
	if err != nil {
		return storageError(err)
	}
	if total > 500000 {
		return domain.ErrScanLimit
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_comparisons(job_id,baseline_revision,inventory_generation,baseline_count,scope_comparable,completed) VALUES($1::uuid,$2,$3,$4,$5,$6)`, l.Job.ID, revision, epoch, total, unknown == 0, unknown != 0)
	if err != nil {
		return storageError(err)
	}
	return commitIgnoreManifest(ctx, tx, current, epoch)
}

type ignoreRawBaseline struct {
	root, path string
	seen       bool
}

func ignoreBaselinePrefix(ctx context.Context, tx pgx.Tx, library string, c ignoreComparison) ([]ignoreRawBaseline, error) {
	rows, err := tx.Query(ctx, ignoreBaselinePageSQL, library, c.token.JobID, c.token.AfterRootID, c.token.AfterPath)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var result []ignoreRawBaseline
	for rows.Next() {
		var r ignoreRawBaseline
		if err := rows.Scan(&r.root, &r.path, &r.seen); err != nil {
			return nil, storageError(err)
		}
		result = append(result, r)
	}
	return result, storageError(rows.Err())
}

func (s *Store) NextIgnoreBaselinePage(ctx context.Context, l domain.JobLease) (domain.IgnoreBaselinePage, error) {
	return s.nextIgnoreBaselinePage(ctx, l, false)
}

func (s *Store) nextIgnoreBaselinePage(ctx context.Context, l domain.JobLease, family bool) (domain.IgnoreBaselinePage, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.IgnoreBaselinePage{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, family)
	if err != nil {
		return domain.IgnoreBaselinePage{}, err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.IgnoreBaselinePage{}, err
	}
	if c.epoch != epoch || c.token.BaselineRevision != revision {
		return domain.IgnoreBaselinePage{}, domain.ErrInventoryInvalidated
	}
	result := domain.IgnoreBaselinePage{Token: c.token, Complete: c.complete, End: c.complete}
	if !c.complete {
		prefix, err := ignoreBaselinePrefix(ctx, tx, current.Job.LibraryID, c)
		if err != nil {
			return domain.IgnoreBaselinePage{}, err
		}
		result.RawCount = len(prefix)
		result.End = len(prefix) == 0
		for _, r := range prefix {
			if !r.seen {
				result.Unseen = append(result.Unseen, domain.IgnoreBaselineCandidate{RootID: r.root, Path: r.path})
			}
		}
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return domain.IgnoreBaselinePage{}, err
	}
	return result, nil
}

func ignoreDecisionsDigest(token domain.IgnoreBaselineToken, decisions []domain.IgnoreBaselineDecision) [32]byte {
	buffer := []byte("jelee-ignore-baseline-page-v1")
	put := func(s string) {
		buffer = binary.BigEndian.AppendUint64(buffer, uint64(len(s)))
		buffer = append(buffer, s...)
	}
	put(token.JobID)
	put(token.AfterRootID)
	put(token.AfterPath)
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(token.BaselineRevision))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(token.Sequence))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(len(decisions)))
	for _, d := range decisions {
		put(d.RootID)
		put(d.Path)
		put(d.Outcome)
		put(d.RuleDirectory)
		put(d.MatchedPath)
		put(d.Reason)
		buffer = binary.BigEndian.AppendUint64(buffer, uint64(d.RuleLine))
	}
	return sha256.Sum256(buffer)
}

// Every rule-affecting ancestor must have a retained proof. A confirmed missing
// directory terminates the chain; a missing database row never proves absence.
func ignoreDecisionProofs(ctx context.Context, tx pgx.Tx, job string, d domain.IgnoreBaselineDecision) error {
	if d.Outcome == domain.IgnoreBaselineUnknown {
		return nil
	}
	target := d.Path
	if d.Outcome == domain.IgnoreBaselineExcluded {
		target = d.MatchedPath
	}
	parts := strings.Split(path.Dir(target), "/")
	ancestors := []string{"."}
	prefix := ""
	if parts[0] != "." {
		for _, part := range parts {
			if prefix == "" {
				prefix = part
			} else {
				prefix += "/" + part
			}
			ancestors = append(ancestors, prefix)
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+ignoreProofColumns+` FROM job_ignore_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=ANY($3::text[])`, job, d.RootID, ancestors)
	if err != nil {
		return storageError(err)
	}
	proofs := make(map[string]domain.IgnoreDirectoryProof, len(ancestors))
	for rows.Next() {
		p, e := scanIgnoreProof(rows)
		if e != nil {
			rows.Close()
			return e
		}
		proofs[p.Directory] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	var previous domain.IgnoreDirectoryProof
	lastOpened := ""
	for i, name := range ancestors {
		p, ok := proofs[name]
		if !ok {
			return domain.ErrConflict
		}
		if i > 0 && !domain.IgnoreProofParentMatches(p, previous) {
			return domain.ErrConflict
		}
		if p.MissingDirectory {
			break
		}
		lastOpened = name
		previous = p
	}
	var enumerated bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=$3 AND done AND skipped=0)`, job, d.RootID, lastOpened).Scan(&enumerated)
	if err != nil {
		return storageError(err)
	}
	if !enumerated {
		return domain.ErrConflict
	}
	if d.Outcome == domain.IgnoreBaselineExcluded {
		p, ok := proofs[d.RuleDirectory]
		if !ok || p.MissingDirectory || !p.RulePresent {
			return domain.ErrConflict
		}
	}
	return nil
}

// CommitIgnoreBaselinePage accepts exactly the unseen rows of the next raw
// prefix. A page with no unseen entries must still be committed to advance.
// Receipts permit exact replay across reclaim, without adding counts twice.
func (s *Store) CommitIgnoreBaselinePage(ctx context.Context, l domain.JobLease, token domain.IgnoreBaselineToken, decisions []domain.IgnoreBaselineDecision) error {
	if token.JobID != l.Job.ID || token.BaselineRevision < 1 || token.Sequence < 0 || token.Sequence > 3907 || len(decisions) > 128 {
		return domain.ErrInvalid
	}
	if (token.AfterRootID == "") != (token.AfterPath == "") || token.AfterRootID != "" && (!domain.ValidID(token.AfterRootID) || !domain.ValidNFOObservationPath(token.AfterPath)) {
		return domain.ErrInvalid
	}
	for _, d := range decisions {
		if err := domain.ValidateIgnoreBaselineDecision(d); err != nil {
			return err
		}
	}
	digest := ignoreDecisionsDigest(token, decisions)
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, revision, err := ignoreComparisonFence(ctx, tx, l)
	if err != nil {
		return err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if c.epoch != epoch || c.token.BaselineRevision != revision || token.BaselineRevision != revision {
		return domain.ErrInventoryInvalidated
	}
	if token.Sequence < c.token.Sequence {
		var prior []byte
		if err = tx.QueryRow(ctx, `SELECT digest FROM job_ignore_comparison_pages WHERE job_id=$1::uuid AND sequence=$2`, l.Job.ID, token.Sequence).Scan(&prior); err != nil {
			return storageError(err)
		}
		if len(prior) != 32 || string(prior) != string(digest[:]) {
			return domain.ErrConflict
		}
		return commitIgnoreManifest(ctx, tx, current, epoch)
	}
	if c.complete || token != c.token {
		return domain.ErrConflict
	}
	prefix, err := ignoreBaselinePrefix(ctx, tx, current.Job.LibraryID, c)
	if err != nil {
		return err
	}
	index := 0
	for _, r := range prefix {
		if r.seen {
			c.counts.Observed++
			continue
		}
		if index >= len(decisions) || decisions[index].RootID != r.root || decisions[index].Path != r.path {
			return domain.ErrConflict
		}
		d := decisions[index]
		index++
		if err = ignoreDecisionProofs(ctx, tx, l.Job.ID, d); err != nil {
			return err
		}
		switch d.Outcome {
		case domain.IgnoreBaselineMissing:
			c.counts.Missing++
		case domain.IgnoreBaselineExcluded:
			c.counts.Excluded++
		case domain.IgnoreBaselineUnknown:
			c.counts.Unknown++
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_ignore_decisions(job_id,root_id,path,outcome,rule_directory,rule_line,matched_path,reason) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)`, l.Job.ID, d.RootID, d.Path, d.Outcome, d.RuleDirectory, d.RuleLine, d.MatchedPath, d.Reason)
		if err != nil {
			return storageError(err)
		}
	}
	if index != len(decisions) {
		return domain.ErrConflict
	}
	processed := c.counts.Observed + c.counts.Missing + c.counts.Excluded + c.counts.Unknown
	if processed > c.baselineCount || len(prefix) == 0 && processed != c.baselineCount {
		return domain.ErrInventoryInvalidated
	}
	if len(prefix) > 0 {
		tail := prefix[len(prefix)-1]
		c.token.AfterRootID = tail.root
		c.token.AfterPath = tail.path
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_comparison_pages(job_id,sequence,digest) VALUES($1::uuid,$2,$3)`, l.Job.ID, token.Sequence, digest[:])
	if err != nil {
		return storageError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_comparisons SET sequence=sequence+1,after_root_id=NULLIF($2,'')::uuid,after_path=$3,processed=$4,observed=$5,missing=$6,excluded=$7,unknown=$8,completed=$9 WHERE job_id=$1::uuid`, l.Job.ID, c.token.AfterRootID, c.token.AfterPath, processed, c.counts.Observed, c.counts.Missing, c.counts.Excluded, c.counts.Unknown, len(prefix) == 0)
	if err != nil {
		return storageError(err)
	}
	return commitIgnoreManifest(ctx, tx, current, epoch)
}
