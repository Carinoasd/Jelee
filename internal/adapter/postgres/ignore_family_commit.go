package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func familyBaselineDigest(token domain.IgnoreBaselineToken, evaluations []domain.FamilyBaselineEvaluation) [32]byte {
	seed := ignoreDecisionsDigest(token, nil)
	digest := sha256.Sum256(append([]byte("jelee-family-baseline-page-v1"), seed[:]...))
	for _, e := range evaluations {
		d := e.Decision
		decision := ignoreDecisionsDigest(token, []domain.IgnoreBaselineDecision{{RootID: d.RootID, Path: d.Path, Outcome: d.Outcome, Reason: d.Reason, RuleDirectory: d.RuleDirectory, RuleLine: d.RuleLine, MatchedPath: d.MatchedPath}})
		b := append(digest[:len(digest):len(digest)], decision[:]...)
		b = binary.BigEndian.AppendUint64(b, uint64(len(d.Family)))
		b = append(b, d.Family...)
		b = binary.BigEndian.AppendUint64(b, uint64(len(e.CustomProofs)))
		b = binary.BigEndian.AppendUint64(b, uint64(len(e.LegacyObservations)))
		digest = sha256.Sum256(b)
		for _, p := range e.CustomProofs {
			digest = extendIgnoreVerification(digest, p)
		}
		for _, o := range e.LegacyObservations {
			digest = extendLegacyBaselineVerification(digest, o)
		}
	}
	return digest
}

func (s *Store) CommitFamilyIgnoreBaselinePage(ctx context.Context, l domain.JobLease, token domain.IgnoreBaselineToken, evaluations []domain.FamilyBaselineEvaluation) error {
	if token.JobID != l.Job.ID || token.BaselineRevision < 1 || token.Sequence < 0 || token.Sequence > 3907 || len(evaluations) > 128 ||
		(token.AfterRootID == "") != (token.AfterPath == "") || token.AfterRootID != "" && (!domain.ValidID(token.AfterRootID) || !domain.ValidNFOObservationPath(token.AfterPath)) {
		return domain.ErrInvalid
	}
	for _, e := range evaluations {
		if err := domain.ValidateFamilyBaselineEvaluation(e); err != nil {
			return err
		}
	}
	digest := familyBaselineDigest(token, evaluations)
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, true)
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
		if string(prior) != string(digest[:]) {
			return domain.ErrConflict
		}
		return commitFamilyComparison(ctx, tx, current, epoch, revision)
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
			continue
		}
		if index >= len(evaluations) || evaluations[index].Decision.RootID != r.root || evaluations[index].Decision.Path != r.path {
			return domain.ErrConflict
		}
		index++
	}
	if index != len(evaluations) {
		return domain.ErrConflict
	}
	if err = recordFamilyBaselineEvidence(ctx, tx, current, epoch, evaluations); err != nil {
		if errors.Is(err, domain.ErrInventoryInvalidated) {
			if e := commitFamilyComparison(ctx, tx, current, epoch, revision); e != nil {
				return e
			}
		}
		return err
	}
	for _, r := range prefix {
		if r.seen {
			c.counts.Observed++
		}
	}
	for _, e := range evaluations {
		d := e.Decision
		// Retain the existing requirement that the last existing parent was
		// completely enumerated. Legacy provenance is bound by the validated
		// observation saved above, not by a custom rule-file proof.
		coverage := domain.IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: d.Outcome, Reason: d.Reason, RuleDirectory: d.RuleDirectory, RuleLine: d.RuleLine, MatchedPath: d.MatchedPath}
		if d.Outcome == domain.IgnoreBaselineExcluded {
			coverage.Reason = ""
			if d.Family == domain.IgnoreFamilyLegacy {
				coverage = domain.IgnoreBaselineDecision{RootID: d.RootID, Path: d.MatchedPath, Outcome: domain.IgnoreBaselineMissing}
			}
		}
		if err = ignoreDecisionProofs(ctx, tx, l.Job.ID, coverage); err != nil {
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
		_, err = tx.Exec(ctx, `INSERT INTO job_ignore_family_decisions(job_id,root_id,path,outcome,family,reason,rule_directory,rule_line,matched_path) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`, l.Job.ID, d.RootID, d.Path, d.Outcome, d.Family, d.Reason, d.RuleDirectory, d.RuleLine, d.MatchedPath)
		if err != nil {
			return storageError(err)
		}
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
	if _, err = tx.Exec(ctx, `INSERT INTO job_ignore_comparison_pages(job_id,sequence,digest) VALUES($1::uuid,$2,$3)`, l.Job.ID, token.Sequence, digest[:]); err != nil {
		return storageError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_comparisons SET sequence=sequence+1,after_root_id=NULLIF($2,'')::uuid,after_path=$3,processed=$4,observed=$5,missing=$6,excluded=$7,unknown=$8,completed=$9 WHERE job_id=$1::uuid`, l.Job.ID, c.token.AfterRootID, c.token.AfterPath, processed, c.counts.Observed, c.counts.Missing, c.counts.Excluded, c.counts.Unknown, len(prefix) == 0)
	if err != nil {
		return storageError(err)
	}
	return commitFamilyComparison(ctx, tx, current, epoch, revision)
}

func commitFamilyComparison(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch, revision int64) error {
	var actual int64
	if err := tx.QueryRow(ctx, `SELECT inventory_baseline_revision FROM libraries WHERE id=$1::uuid FOR SHARE`, l.Job.LibraryID).Scan(&actual); err != nil {
		return storageError(err)
	}
	if actual != revision {
		return domain.ErrInventoryInvalidated
	}
	return commitIgnoreManifest(ctx, tx, l, epoch)
}

func recordFamilyBaselineEvidence(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, evaluations []domain.FamilyBaselineEvaluation) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT family_baseline_evidence`); err != nil {
		return storageError(err)
	}
	invalidate := func() error {
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT family_baseline_evidence`); err != nil {
			return storageError(err)
		}
		for _, table := range []string{"job_ignore_manifests", "job_ignore_legacy_manifests"} {
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
				return storageError(err)
			}
		}
		return domain.ErrInventoryInvalidated
	}
	roots := []string{}
	type rootEvidence struct {
		custom map[string]domain.IgnoreDirectoryProof
		legacy map[string]domain.LegacyIgnoreBaselineObservation
	}
	groups := map[string]*rootEvidence{}
	for _, e := range evaluations {
		if e.Decision.Outcome == domain.IgnoreBaselineUnknown {
			continue
		}
		group := groups[e.Decision.RootID]
		if group == nil {
			roots = append(roots, e.Decision.RootID)
			group = &rootEvidence{custom: map[string]domain.IgnoreDirectoryProof{}, legacy: map[string]domain.LegacyIgnoreBaselineObservation{}}
			groups[e.Decision.RootID] = group
		}
		for _, p := range e.CustomProofs {
			if old, ok := group.custom[p.Directory]; ok && old != p {
				return invalidate()
			}
			group.custom[p.Directory] = p
		}
		for _, o := range e.LegacyObservations {
			if old, ok := group.legacy[o.LookupDirectory]; ok && (old.Version != o.Version || old.Source.Version != o.Source.Version || old.Source.Directory != o.Source.Directory || old.MissingDirectory != o.MissingDirectory || !slices.Equal(old.Source.Proofs, o.Source.Proofs)) {
				return invalidate()
			}
			group.legacy[o.LookupDirectory] = o
		}
	}
	slices.Sort(roots)
	for _, root := range roots {
		group := groups[root]
		names := make([]string, 0, len(group.custom))
		for name := range group.custom {
			names = append(names, name)
		}
		slices.Sort(names) // Canonical paths sort parents before descendants.
		proofs := make([]domain.IgnoreDirectoryProof, 0, len(names))
		for _, name := range names {
			proofs = append(proofs, group.custom[name])
		}
		err := recordIgnoreProofs(ctx, tx, l, epoch, proofs)
		if errors.Is(err, domain.ErrInventoryInvalidated) {
			return invalidate()
		}
		if err != nil {
			return err
		}
		names = names[:0]
		for name := range group.legacy {
			names = append(names, name)
		}
		slices.Sort(names)
		for start := 0; start < len(names); start += 128 {
			end := min(start+128, len(names))
			observations := make([]domain.LegacyIgnoreBaselineObservation, 0, end-start)
			for _, name := range names[start:end] {
				observations = append(observations, group.legacy[name])
			}
			err = recordLegacyBaselineObservations(ctx, tx, l, epoch, observations)
			if errors.Is(err, domain.ErrInventoryInvalidated) {
				return invalidate()
			}
			if err != nil {
				return err
			}
		}
	}
	// Compare against earlier pages too, including a previously retained
	// absence which the other family now claims is an existing directory.
	var conflict bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM job_ignore_proofs c JOIN job_ignore_legacy_proofs p USING(job_id,root_id,directory)
 WHERE c.job_id=$1::uuid AND c.root_id=ANY($2::uuid[]) AND (c.missing_directory OR c.identity<>p.identity OR c.parent_identity<>p.parent_identity)) OR EXISTS(
 SELECT 1 FROM job_ignore_legacy_baseline_queries q JOIN job_ignore_proofs c ON c.job_id=q.job_id AND c.root_id=q.root_id AND c.directory=q.missing_directory
 WHERE q.job_id=$1::uuid AND q.root_id=ANY($2::uuid[]) AND (NOT c.missing_directory OR c.parent_identity<>q.missing_parent_identity))`, l.Job.ID, roots).Scan(&conflict)
	if err != nil {
		return storageError(err)
	}
	if conflict {
		return invalidate()
	}
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT family_baseline_evidence`)
	return storageError(err)
}
