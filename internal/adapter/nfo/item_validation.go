package nfo

import (
	"context"
	"os"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOItemValidationReader = (*SummaryReader)(nil)

// ValidateItemNFO selects the NFO of an item exactly like ObserveItemNFO
// (the same directory observation, the same refusals) and, when there is one,
// validates it with the parser the library validation job uses. Unlike an item
// observation it keeps the summary of a document with errors, and reports a
// refused location as a path result instead of an error. The file system is
// observed before and after, so a file that changes meanwhile is
// domain.ErrNFOSourceChanged.
func (r *SummaryReader) ValidateItemNFO(ctx context.Context, scope domain.NFOItemScope) (domain.NFOItemValidation, error) {
	if ctx == nil || !domain.ValidNFOItemScope(scope) {
		return domain.NFOItemValidation{}, domain.ErrInvalid
	}
	if r == nil || domain.ValidateNFOIdentity(r.identity) != nil {
		return domain.NFOItemValidation{}, domain.ErrNFOReaderUnavailable
	}
	first, err := observeItemCandidates(ctx, scope)
	if err != nil {
		if code := pathIssueCode(err); code != "" {
			return domain.NFOItemValidation{Status: domain.NFOItemValidationPath, PathCode: code}, nil
		}
		return domain.NFOItemValidation{}, err
	}
	result := domain.NFOItemValidation{Status: domain.NFOItemValidationMissing}
	var stamp domain.NFOStamp
	if first.selected != "" {
		source, err := r.Read(ctx, domain.NFOSource{RootPath: scope.Source.RootPath, RelativePath: first.selected})
		if err != nil {
			return domain.NFOItemValidation{}, err
		}
		stamp = source.Stamp()
		if result, err = validateSource(ctx, source.(*summarySource)); err != nil {
			return domain.NFOItemValidation{}, err
		}
	}
	last, err := observeItemCandidates(ctx, scope)
	if err != nil {
		if pathIssueCode(err) != "" {
			return domain.NFOItemValidation{}, domain.ErrNFOSourceChanged
		}
		return domain.NFOItemValidation{}, err
	}
	if first.selected != last.selected || first.digest != last.digest || !os.SameFile(first.root, last.root) || !os.SameFile(first.directory, last.directory) || !sameItemAnchor(scope, first.media, last.media) || !sameObservedNFO(first, last) || first.selected != "" && (stamp.Size != last.nfo.Size() || stamp.ModifiedUnixNano != last.nfo.ModTime().UnixNano()) {
		return domain.NFOItemValidation{}, domain.ErrNFOSourceChanged
	}
	return result, nil
}

// validateSource parses the retained bytes of one NFO into the validation
// summary the job would store, plus what a repair needs and the summary cannot
// hold: every issue code and the NFO's edit locks.
func validateSource(ctx context.Context, source *summarySource) (domain.NFOItemValidation, error) {
	result := domain.NFOItemValidation{Status: domain.NFOItemValidationChecked}
	document, err := source.source.Parse(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return domain.NFOItemValidation{}, ctx.Err()
		}
		result.Summary, err = summaryParseFailure(err)
		return result, err
	}
	if result.Summary, err = projectSummary(ctx, document); err != nil {
		return domain.NFOItemValidation{}, err
	}
	for _, issue := range document.Issues {
		if !slices.Contains(result.IssueCodes, issue.Code) {
			result.IssueCodes = append(result.IssueCodes, issue.Code)
		}
	}
	slices.Sort(result.IssueCodes)
	if len(document.Entries) > 0 {
		entry := document.Entries[0]
		result.LockData = entry.LockData != nil && *entry.LockData
		result.LockedFields = slices.Clone(entry.LockedFields)
	}
	return result, nil
}
