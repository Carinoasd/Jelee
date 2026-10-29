package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Fix mode (G39.11 --fix) of an nfo_export job. Each item's NFO is validated
// with the parser of the library validation job and only one of its issues is
// repaired, through the same controlled edit and write path as every other
// export: nfo_title_missing, where the title is set to the title stored in the
// database (never the scanned name). Any rewrite also adds a missing Jelee
// identifier (EnsureID, which never overwrites an identifier that exists), so
// G39.10 is met on every write; it is not a validation issue and fix mode never
// rewrites a file only to add an identifier.
//
// Every other issue, and every NFO the writer would refuse, is left alone and
// counted as unfixable.

const nfoIssueTitleMissing = "nfo_title_missing"

// validateItemNFO resolves the item's NFO scope and validates the NFO it
// selects. A scope or file that cannot be used is domain.ErrMetadataUnavailable
// and a file that changed meanwhile domain.ErrConflict, like readItemNFO.
func (m *Metadata) validateItemNFO(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.NFOItemScope, domain.NFOItemValidation, error) {
	repository, ok := m.items.(NFOItemScopeRepository)
	reader, readerOK := m.nfoFields.(NFOItemValidationReader)
	if !ok || !readerOK {
		return domain.NFOItemScope{}, domain.NFOItemValidation{}, domain.ErrMetadataUnavailable
	}
	scope, err := repository.ResolveItemNFO(ctx, actor, item, expected)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemValidation{}, err
	}
	if !domain.ValidNFOItemScope(scope) || scope.ItemID != item || scope.Revision != expected {
		return domain.NFOItemScope{}, domain.NFOItemValidation{}, domain.ErrMetadataUnavailable
	}
	validation, err := reader.ValidateItemNFO(ctx, scope)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemValidation{}, nfoItemError(ctx, err)
	}
	return scope, validation, nil
}

// nfoFixRoot is the root element the summary reports for an NFO of kind.
func nfoFixRootMatches(kind, root string) bool {
	switch kind {
	case "Movie", "HomeVideo":
		return root == "movie"
	case "Series":
		return root == "tvshow"
	case "Season":
		return root == "season"
	case "Episode":
		return root == "episode" || root == "episodedetails"
	}
	return false
}

// planNFOExportFix decides what a fix-mode run does with one item. It never
// writes anything the validation did not call for:
//
//   - no NFO: skipped_missing (a fix never creates files);
//   - an illegal location, a document with a parse failure or an error issue,
//     a guessed encoding, a root that is not the item's kind, an NFO that locks
//     what the repair needs: unfixable, the file is not touched;
//   - no issue at all: unchanged;
//   - only issues the fix does not repair: unfixable;
//   - otherwise a prepared edit: the stored title when the title is missing and
//     one is stored.
//
// A written NFO always receives an identifier when it has none (the writer
// ensures it), so a title repair of an NFO without an ID also adds the ID.
func planNFOExportFix(before domain.ItemMetadata, scope domain.NFOItemScope, v domain.NFOItemValidation) (nfoExportOutcome, *domain.NFOWritePrepareRequest) {
	switch v.Status {
	case domain.NFOItemValidationMissing:
		return nfoExportMissing, nil
	case domain.NFOItemValidationPath:
		return nfoExportUnfixable, nil
	case domain.NFOItemValidationChecked:
	default:
		return nfoExportSkipped, nil
	}
	s := v.Summary
	if s.FailureCode == "" && s.IssueCount == 0 {
		return nfoExportUnchanged, nil
	}
	if s.FailureCode != "" || s.ErrorCount > 0 || s.EncodingGuessed || s.Entries != 1 || !nfoFixRootMatches(scope.Kind, s.Root) ||
		v.Has("nfo_unknown_root") || v.Has("nfo_wrapper_fields_ignored") || v.Has("nfo_kind_unknown") || v.LockData {
		return nfoExportUnfixable, nil
	}
	if !v.Has(nfoIssueTitleMissing) {
		return nfoExportUnfixable, nil
	}
	var edits []domain.NFOWriteTextEdit
	{
		titleLocked := false
		for _, name := range v.LockedFields {
			if nfoExportLockedEdit(name) == "title" {
				titleLocked = true
			}
		}
		if want, ok := nfoExportDatabaseValue(before, nfoExportField{field: "title", edit: "title"}); ok && want != "" && !titleLocked {
			edits = append(edits, domain.NFOWriteTextEdit{Field: "title", Value: want})
		}
	}
	if len(edits) == 0 {
		return nfoExportUnfixable, nil
	}
	return nfoExportPrepared, &domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: scope.Revision, Edits: edits, CreateMissing: true,
		BOM: "preserve", MaxBytes: domain.NFODefaultSourceBytes, Backups: nfoExportBackups}
}
