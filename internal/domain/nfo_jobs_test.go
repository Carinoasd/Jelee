package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const nfoJobsTestID = "11111111-1111-4111-8111-111111111111"
const nfoJobsLibraryID = "22222222-2222-4222-8222-222222222222"

func nfoJobsRequest(t *testing.T) NFORequest {
	t.Helper()
	identity := DefaultNFOIdentity()
	digest, err := NFOIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	return NFORequest{JobID: nfoJobsTestID, LibraryID: nfoJobsLibraryID, Requested: true,
		Mode: NFOModeReadOnly, Identity: identity, IdentityDigest: digest, LibraryGeneration: 2}
}

func TestScanIntentStageCombinations(t *testing.T) {
	for _, scope := range []string{"", ProbeScopeIncremental, ProbeScopeLibraryRebuild, ProbeScopeItemRebuild} {
		for _, nfo := range []bool{false, true} {
			intent := ScanIntent{Probe: ProbeIntent{Scope: scope}, NFO: nfo}
			if scope == ProbeScopeItemRebuild {
				intent.Probe.TargetItemID = nfoJobsTestID
			}
			wantInvalid := nfo && scope != "" && scope != ProbeScopeIncremental
			if (ValidateScanIntent(intent) != nil) != wantInvalid {
				t.Fatalf("stage combination %q, nfo=%v", scope, nfo)
			}
		}
	}
	for _, intent := range []ScanIntent{
		{Probe: ProbeIntent{Scope: "private-scope"}},
		{Probe: ProbeIntent{Scope: ProbeScopeIncremental, TargetItemID: nfoJobsTestID}},
		{Probe: ProbeIntent{Scope: ProbeScopeItemRebuild}},
	} {
		if ValidateScanIntent(intent) != ErrInvalid {
			t.Fatal("invalid probe intent crossed combined boundary")
		}
	}
}

func TestNFORequestCurrentIdentityAndImmutableMatching(t *testing.T) {
	request := nfoJobsRequest(t)
	if ValidateNFORequest(request) != nil {
		t.Fatal("valid requested stage rejected")
	}
	off := request
	off.Requested, off.Mode = false, NFOModeOff
	if ValidateNFORequest(off) != nil {
		t.Fatal("compiled identity on off intent rejected")
	}
	for _, code := range []NFOPhaseError{NFOPhaseDisabled, NFOPhaseInvalidated, NFOPhaseCapacity, NFOPhaseIdentityMismatch, NFOPhaseUnavailable, NFOPhaseCancelled} {
		value := request
		value.ErrorCode = code
		if ValidateNFORequest(value) != nil {
			t.Fatal("fixed retained request error rejected")
		}
	}
	mutations := map[string]func(*NFORequest){
		"job":        func(v *NFORequest) { v.JobID = "bad" },
		"library":    func(v *NFORequest) { v.LibraryID = "bad" },
		"mode":       func(v *NFORequest) { v.Mode = "read-write" },
		"intent":     func(v *NFORequest) { v.Requested = false },
		"generation": func(v *NFORequest) { v.LibraryGeneration = 0 },
		"identity":   func(v *NFORequest) { v.Identity.ParserVersion = "old-parser" },
		"digest":     func(v *NFORequest) { v.IdentityDigest = strings.Repeat("a", 64) },
		"error":      func(v *NFORequest) { v.ErrorCode = "private-diagnostic" },
		"off_error":  func(v *NFORequest) { v.Requested, v.Mode, v.ErrorCode = false, NFOModeOff, NFOPhaseUnavailable },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			value := request
			change(&value)
			if ValidateNFORequest(value) != ErrInvalid {
				t.Fatal("unsafe current request accepted")
			}
		})
	}
	phase := NFOPhase{JobID: request.JobID, LibraryID: request.LibraryID, Mode: request.Mode,
		Identity: request.Identity, IdentityDigest: request.IdentityDigest, LibraryGeneration: request.LibraryGeneration}
	if !NFORequestMatchesPhase(request, phase) {
		t.Fatal("matching frozen fields rejected")
	}
	for name, change := range map[string]func(*NFOPhase){
		"job":        func(v *NFOPhase) { v.JobID = nfoJobsLibraryID },
		"library":    func(v *NFOPhase) { v.LibraryID = nfoJobsTestID },
		"mode":       func(v *NFOPhase) { v.Mode = NFOModeOff },
		"identity":   func(v *NFOPhase) { v.Identity.MaxSourceBytes-- },
		"digest":     func(v *NFOPhase) { v.IdentityDigest = strings.Repeat("b", 64) },
		"generation": func(v *NFOPhase) { v.LibraryGeneration++ },
	} {
		t.Run("match_"+name, func(t *testing.T) {
			value := phase
			change(&value)
			if NFORequestMatchesPhase(request, value) {
				t.Fatal("changed frozen field matched")
			}
		})
	}
	// Cleanup/history may compare retained versions without authorizing them.
	request.Identity.ParserVersion, phase.Identity.ParserVersion = "old-parser", "old-parser"
	if !NFORequestMatchesPhase(request, phase) || ValidateNFORequest(request) != ErrInvalid {
		t.Fatal("old identity matching became execution authority")
	}
}

func nfoJobPublicSummary() NFOJobSummary {
	return NFOJobSummary{JobID: nfoJobsTestID, LibraryID: nfoJobsLibraryID, Mode: NFOModeReadOnly,
		Phase: NFOSummaryRunning, Processed: 7, Hits: 1, NegativeHits: 1, Parsed: 2,
		Valid: 2, Invalid: 2, WarningFiles: 1, Changed: 1, Unavailable: 1, Rejected: 1}
}

func TestNFOJobSummaryHistoryCountsAndFixedErrors(t *testing.T) {
	for _, phase := range []string{NFOSummaryRunning, NFOSummaryDone, NFOSummaryCancelled} {
		value := nfoJobPublicSummary()
		value.Phase = phase
		if ValidateNFOJobSummary(value) != nil {
			t.Fatal("valid historical counters rejected")
		}
	}
	waiting := NFOJobSummary{JobID: nfoJobsTestID, LibraryID: nfoJobsLibraryID, Mode: NFOModeReadOnly, Phase: NFOSummaryWaitingScan}
	off := waiting
	off.Mode, off.Phase = NFOModeOff, NFOSummaryDisabled
	if ValidateNFOJobSummary(waiting) != nil || ValidateNFOJobSummary(off) != nil {
		t.Fatal("empty waiting/off summary rejected")
	}
	for _, code := range []string{"nfo_disabled", "nfo_invalidated", "nfo_cache_capacity", "nfo_identity_mismatch", "nfo_unavailable", "nfo_cancelled", "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted"} {
		value := nfoJobPublicSummary()
		value.Phase, value.ErrorCode = NFOSummaryAborted, code
		if !ValidNFOJobError(code) || ValidateNFOJobSummary(value) != nil {
			t.Fatalf("fixed aborted code %q rejected", code)
		}
	}
	for name, change := range map[string]func(*NFOJobSummary){
		"job":              func(v *NFOJobSummary) { v.JobID = "bad" },
		"library":          func(v *NFOJobSummary) { v.LibraryID = "bad" },
		"mode":             func(v *NFOJobSummary) { v.Mode = "read-write" },
		"phase":            func(v *NFOJobSummary) { v.Phase = "private-phase" },
		"off_progress":     func(v *NFOJobSummary) { v.Mode, v.Phase = NFOModeOff, NFOSummaryDisabled },
		"waiting_progress": func(v *NFOJobSummary) { v.Phase = NFOSummaryWaitingScan },
		"count_sum":        func(v *NFOJobSummary) { v.Processed++ },
		"negative":         func(v *NFOJobSummary) { v.Changed = -1 },
		"overflow":         func(v *NFOJobSummary) { v.Parsed = 1<<63 - 1 },
		"valid_sum":        func(v *NFOJobSummary) { v.Valid++ },
		"warnings":         func(v *NFOJobSummary) { v.WarningFiles = 5 },
		"running_error":    func(v *NFOJobSummary) { v.ErrorCode = "nfo_unavailable" },
		"unknown_error":    func(v *NFOJobSummary) { v.Phase, v.ErrorCode = NFOSummaryAborted, "private-error" },
		"missing_error":    func(v *NFOJobSummary) { v.Phase = NFOSummaryAborted },
	} {
		t.Run(name, func(t *testing.T) {
			value := nfoJobPublicSummary()
			change(&value)
			if ValidateNFOJobSummary(value) != ErrInvalid {
				t.Fatal("invalid public history accepted")
			}
		})
	}
	if ValidNFOJobError("scan_invalidated") || ValidNFOJobError("") {
		t.Fatal("unpublished parent error became public")
	}
}

func TestNFORequestAndWorkRedactIdentity(t *testing.T) {
	request := nfoJobsRequest(t)
	request.Identity.ParserVersion = "private-identity-marker"
	phase := &NFOPhase{Identity: request.Identity, IdentityDigest: request.IdentityDigest}
	for _, value := range []any{request, &request, NFOWork{Request: &request, Phase: phase}} {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != "{}" {
			t.Fatal("internal worker contract exposed JSON fields")
		}
		for _, pattern := range []string{"%v", "%+v", "%#v"} {
			text := fmt.Sprintf(pattern, value)
			if !strings.Contains(text, "redacted") || strings.Contains(text, request.IdentityDigest) || strings.Contains(text, "private-identity-marker") {
				t.Fatal("internal worker contract exposed fmt identity")
			}
		}
	}
	raw, err := json.Marshal(nfoJobPublicSummary())
	if err != nil || strings.Contains(string(raw), "identity") || strings.Contains(string(raw), "requested") || strings.Contains(string(raw), "Progress") {
		t.Fatal("public history crossed private contract")
	}
}

func TestImageProgressBoundsAndPartialComparison(t *testing.T) {
	for _, value := range []ImageProgress{
		{}, {Added: 500000, Missing: 500000, ComparisonComplete: true},
		{Changed: 23, Unchanged: 477, ComparisonComplete: true},
		{Uncompared: 10}, // Unknown old attributes suppress missing-image claims.
	} {
		if ValidateImageProgress(value) != nil || ValidateImageJobSummary(ImageJobSummary{JobID: nfoJobsTestID, LibraryID: nfoJobsLibraryID, ImageProgress: value}) != nil {
			t.Fatal("bounded image observations rejected")
		}
	}
	for _, value := range []ImageProgress{{Added: -1}, {Changed: 500001}, {Missing: 1<<63 - 1}, {Unchanged: 500000, Added: 1}, {Uncompared: 1, ComparisonComplete: true}, {Uncompared: 10, Missing: 2}} {
		if ValidateImageProgress(value) != ErrInvalid {
			t.Fatal("invalid image counters accepted")
		}
	}
	if ValidateImageJobSummary(ImageJobSummary{JobID: "bad", LibraryID: nfoJobsLibraryID}) != ErrInvalid ||
		ValidateImageJobSummary(ImageJobSummary{JobID: nfoJobsTestID, LibraryID: "bad"}) != ErrInvalid {
		t.Fatal("invalid image job identifier accepted")
	}
	raw, err := json.Marshal(ImageJobSummary{JobID: nfoJobsTestID, LibraryID: nfoJobsLibraryID, ImageProgress: ImageProgress{Added: 1}})
	if err != nil || strings.Contains(string(raw), "ImageProgress") || !strings.Contains(string(raw), `"added":1`) {
		t.Fatal("image progress is not flat public JSON")
	}
}
