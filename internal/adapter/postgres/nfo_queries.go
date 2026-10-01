package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOQueryRepository = (*Store)(nil)

func (s *Store) GetNFOJobSummary(parent context.Context, a domain.Actor, id string) (domain.NFOJobSummary, error) {
	if parent == nil {
		return domain.NFOJobSummary{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.NFOJobSummary{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.NFOJobSummary{}, err
	}
	defer tx.Rollback(ctx)
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, id))
	if err != nil {
		return domain.NFOJobSummary{}, err
	}
	r, err := loadNFORequest(ctx, tx, id)
	if err != nil {
		return domain.NFOJobSummary{}, err
	}
	p, phaseErr := loadNFOPhase(ctx, tx, id)
	if phaseErr != nil && !errors.Is(phaseErr, domain.ErrNotFound) {
		return domain.NFOJobSummary{}, phaseErr
	}
	if r != nil {
		if phaseErr != nil {
			return domain.NFOJobSummary{}, domain.ErrConflict
		}
		if !nfoRequestMatchesPhase(r, p) {
			return domain.NFOJobSummary{}, domain.ErrNFOIdentityMismatch
		}
	}
	if phaseErr == nil && p.Mode == domain.NFOModeOff && !frozenNFOOff(p) {
		return domain.NFOJobSummary{}, domain.ErrDatabase
	}
	out := domain.NFOJobSummary{JobID: j.ID, LibraryID: j.LibraryID, Mode: domain.NFOModeOff, Phase: domain.NFOSummaryDisabled}
	if phaseErr == nil && p.Mode == domain.NFOModeReadOnly {
		out.Mode = p.Mode
		out.Phase = p.State
		if p.State == domain.NFOPhaseWaiting {
			out.Phase = domain.NFOSummaryWaitingScan
		}
		out.Processed = p.Progress.Processed
		out.Hits = p.Progress.Hits
		out.NegativeHits = p.Progress.NegativeHits
		out.Parsed = p.Progress.Parsed
		out.Valid = p.Progress.Valid
		out.Invalid = p.Progress.Invalid
		out.WarningFiles = p.Progress.WarningFiles
		out.Changed = p.Progress.Changed
		out.Unavailable = p.Progress.Unavailable
		out.Rejected = p.Progress.Rejected
		out.ErrorCode = string(p.ErrorCode)
		if r != nil && r.ErrorCode != "" {
			out.Phase = domain.NFOSummaryAborted
			out.ErrorCode = string(r.ErrorCode)
		}
		if j.State == domain.JobFailed {
			out.Phase = domain.NFOSummaryAborted
			if out.ErrorCode == "" {
				out.ErrorCode = j.ErrorCode
			}
		}
		if j.State == domain.JobCancelled || j.CancelRequested {
			out.Phase = domain.NFOSummaryCancelled
			out.ErrorCode = ""
		}
		if j.State == domain.JobSucceeded && out.Phase != domain.NFOSummaryDone {
			return domain.NFOJobSummary{}, domain.ErrConflict
		}
	}
	if err = domain.ValidateNFOJobSummary(out); err != nil {
		return domain.NFOJobSummary{}, domain.ErrDatabase
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.NFOJobSummary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOJobSummary{}, storageError(err)
	}
	return out, nil
}

// The query observes the database at the supplied transaction-local wall-clock
// instant. HTTP does not reread files, and pagination is not a cross-page snapshot.
const nfoCurrentObservationSQL = `SELECT c.observation_id::text,c.root_id::text,c.relative_path,c.status,c.summary::text,c.updated_at,c.expires_at FROM nfo_cache c JOIN library_roots r ON r.id=c.root_id AND r.library_id=c.library_id WHERE c.library_id=$1::uuid AND c.identity_digest=$2 AND c.library_generation=$3 AND c.root_generation=r.nfo_generation AND c.expires_at>$4::timestamptz AND c.observation_id>COALESCE(NULLIF($5,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY c.observation_id LIMIT $6`

func (s *Store) ListNFOObservations(parent context.Context, a domain.Actor, library, cursor string, limit int, identity domain.NFOIdentity) (domain.NFOObservationPage, error) {
	if parent == nil || limit < 1 || limit > domain.NFOObservationPageMax || cursor != "" && !domain.ValidID(cursor) {
		return domain.NFOObservationPage{}, domain.ErrInvalid
	}
	if !domain.ValidID(library) {
		return domain.NFOObservationPage{}, domain.ErrNotFound
	}
	digest, err := domain.NFOIdentityDigest(identity)
	if err != nil {
		return domain.NFOObservationPage{}, err
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.NFOObservationPage{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readNFOLibraryPolicy(ctx, tx, library)
	if err != nil {
		return domain.NFOObservationPage{}, err
	}
	result := domain.NFOObservationPage{Items: make([]domain.NFOObservation, 0, limit)}
	if policy.Mode == domain.NFOModeReadOnly {
		var asOf time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&asOf); err != nil {
			return domain.NFOObservationPage{}, storageError(err)
		}
		rows, e := tx.Query(ctx, nfoCurrentObservationSQL, library, probeBytes(digest), policy.Generation, asOf, cursor, limit+1)
		if e != nil {
			return domain.NFOObservationPage{}, storageError(e)
		}
		for rows.Next() {
			var item domain.NFOObservation
			var raw []byte
			if err = rows.Scan(&item.ID, &item.RootID, &item.Path, &item.Status, &raw, &item.ObservedAt, &item.ExpiresAt); err != nil {
				rows.Close()
				return domain.NFOObservationPage{}, storageError(err)
			}
			if len(result.Items) == limit {
				result.NextCursor = result.Items[len(result.Items)-1].ID
				continue
			}
			summary, e := decodeNFOSummary(raw)
			if e != nil {
				rows.Close()
				return domain.NFOObservationPage{}, e
			}
			if summary.Status != item.Status {
				rows.Close()
				return domain.NFOObservationPage{}, domain.ErrDatabase
			}
			item.Entries = summary.Entries
			item.FailureCode = summary.FailureCode
			item.WarningCount = summary.WarningCount
			item.ErrorCount = summary.ErrorCount
			item.IssueCount = summary.IssueCount
			item.IssuesTruncated = summary.IssuesTruncated
			result.Items = append(result.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return domain.NFOObservationPage{}, storageError(err)
		}
	}
	if err = domain.ValidateNFOObservationPage(result); err != nil {
		return domain.NFOObservationPage{}, domain.ErrDatabase
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.NFOObservationPage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOObservationPage{}, storageError(err)
	}
	return result, nil
}
func (s *Store) GetNFOObservationIssues(parent context.Context, a domain.Actor, library, observation string, offset, limit int, identity domain.NFOIdentity) (domain.NFOIssuesPage, error) {
	if parent == nil || offset < 0 || offset > domain.NFOIssuesMax || limit < 1 || limit > domain.NFOIssuesPageMax {
		return domain.NFOIssuesPage{}, domain.ErrInvalid
	}
	if !domain.ValidID(library) || !domain.ValidID(observation) {
		return domain.NFOIssuesPage{}, domain.ErrNotFound
	}
	digest, err := domain.NFOIdentityDigest(identity)
	if err != nil {
		return domain.NFOIssuesPage{}, err
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.NFOIssuesPage{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readNFOLibraryPolicy(ctx, tx, library)
	if err != nil {
		return domain.NFOIssuesPage{}, err
	}
	if policy.Mode != domain.NFOModeReadOnly {
		return domain.NFOIssuesPage{}, domain.ErrNotFound
	}
	var raw []byte
	var status string
	err = tx.QueryRow(ctx, `SELECT c.summary::text,c.status FROM nfo_cache c JOIN library_roots r ON r.id=c.root_id AND r.library_id=c.library_id WHERE c.library_id=$1::uuid AND c.observation_id=$2::uuid AND c.identity_digest=$3 AND c.library_generation=$4 AND c.root_generation=r.nfo_generation AND c.expires_at>clock_timestamp()`, library, observation, probeBytes(digest), policy.Generation).Scan(&raw, &status)
	if err != nil {
		return domain.NFOIssuesPage{}, storageError(err)
	}
	summary, err := decodeNFOSummary(raw)
	if err != nil {
		return domain.NFOIssuesPage{}, err
	}
	if summary.Status != status {
		return domain.NFOIssuesPage{}, domain.ErrDatabase
	}
	if offset > len(summary.Issues) {
		return domain.NFOIssuesPage{}, domain.ErrInvalid
	}
	end := min(offset+limit, len(summary.Issues))
	result := domain.NFOIssuesPage{ObservationID: observation, Entries: summary.Entries, FailureCode: summary.FailureCode, IssueCount: summary.IssueCount, IssuesTruncated: summary.IssuesTruncated, Offset: offset, Issues: append([]domain.NFOIssue{}, summary.Issues[offset:end]...)}
	if end < len(summary.Issues) {
		result.NextOffset = &end
	}
	if err = domain.ValidateNFOIssuesPage(result); err != nil {
		return domain.NFOIssuesPage{}, domain.ErrDatabase
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.NFOIssuesPage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOIssuesPage{}, storageError(err)
	}
	return result, nil
}
