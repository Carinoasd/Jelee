package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestPlanNFOExportMissingCreatesOnlyWhereNoNFOExists(t *testing.T) {
	scope := nfoApplyScope(nfoExportWorkerActor, 3)
	manual := func(field, value string) domain.ItemMetadataField {
		return domain.ItemMetadataField{Field: field, Value: value, Source: "manual"}
	}
	edits := func(pairs ...string) []domain.NFOWriteTextEdit {
		var out []domain.NFOWriteTextEdit
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, domain.NFOWriteTextEdit{Field: pairs[i], Value: pairs[i+1]})
		}
		return out
	}
	missing := nfoExportState(domain.NFOItemObservedMissing, domain.NFOItemFields{})
	for _, tc := range []struct {
		name    string
		meta    domain.ItemMetadata
		state   *domain.NFOItemObservationState
		outcome nfoExportOutcome
		edits   []domain.NFOWriteTextEdit
	}{
		{"valid NFO is left alone", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "Other")}), nfoExportExisting, nil},
		{"invalid NFO is left alone", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T")}), nfoExportState(domain.NFOItemObservedInvalid, domain.NFOItemFields{}), nfoExportExisting, nil},
		{"locked NFO is left alone", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{LockData: true}), nfoExportExisting, nil},
		{"no observation state", nfoExportMeta(nil), nil, nfoExportSkipped, nil},
		{"unknown status", nfoExportMeta(nil), nfoExportState("other", domain.NFOItemFields{}), nfoExportSkipped, nil},
		{"nothing stored", nfoExportMeta(nil), missing, nfoExportUnchanged, nil},
		{"scanned name and empty values are no stored values", nfoExportMeta([]domain.ItemMetadataField{{Field: "title", Value: "Scanned", Source: "existing"}, manual("overview", "")}), missing, nfoExportUnchanged, nil},
		{"date is not exported", nfoExportMeta([]domain.ItemMetadataField{{Field: "date", Value: "2024-01-02", Source: "manual"}}), missing, nfoExportUnchanged, nil},
		{"every stored field in the fixed order", nfoExportMeta([]domain.ItemMetadataField{manual("overview", "P"), manual("title", "T"), manual("sortTitle", "S"), manual("originalTitle", "O"), manual("tagline", "G"), manual("outline", "L"), manual("mpaa", "R"), manual("certification", "PG")},
			domain.ItemMetadataFact{Field: "showTitle", Value: json.RawMessage(`"Show"`), Source: "manual"}, domain.ItemMetadataFact{Field: "seriesStatus", Value: json.RawMessage(`"Ended"`), Source: "nfo"}), missing, nfoExportPrepared,
			edits("title", "T", "originaltitle", "O", "plot", "P", "sorttitle", "S", "tagline", "G", "outline", "L", "mpaa", "R", "certification", "PG", "showtitle", "Show", "status", "Ended")},
		{"only the stored field", nfoExportMeta([]domain.ItemMetadataField{manual("overview", "P"), {Field: "title", Value: "Scanned", Source: "existing"}}), missing, nfoExportPrepared, edits("plot", "P")},
		{"cleared fact", nfoExportMeta(nil, domain.ItemMetadataFact{Field: "seriesStatus", Value: json.RawMessage(`null`), Source: "manual"}), missing, nfoExportUnchanged, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, request := planNFOExportMissing(tc.meta, scope, nfoItemRead{state: tc.state})
			if outcome != tc.outcome || (request != nil) != (tc.outcome == nfoExportPrepared) {
				t.Fatalf("outcome %d request %v, want %d", outcome, request, tc.outcome)
			}
			if request == nil {
				return
			}
			if !reflect.DeepEqual(request.Edits, tc.edits) || request.ItemID != scope.ItemID || request.Revision != scope.Revision || !request.Create || request.CreateMissing || request.Backups != 0 || request.MaxBytes != domain.NFODefaultSourceBytes {
				t.Fatalf("request %+v, want edits %v", *request, tc.edits)
			}
			if err := domain.ValidateNFOWritePrepareRequest(*request); err != nil {
				t.Fatal("the request is not a valid create preparation", err)
			}
		})
	}
}
