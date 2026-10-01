package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

const nfoID1 = "00000000-0000-0000-0000-000000000001"
const nfoID2 = "00000000-0000-0000-0000-000000000002"

func nfoCandidate(id string) NFOCandidate {
	return NFOCandidate{InventoryID: id, Stamp: NFOStamp{Size: 8, ModifiedUnixNano: 42, SHA256: strings.Repeat("a", 64), FingerprintVersion: NFOFingerprintVersion}}
}

func TestNFOResourcePolicyBoundariesAndDefaults(t *testing.T) {
	defaults := DefaultNFOCachePolicy()
	if defaults.MaxRows != 100000 || defaults.MaxBytes != 256<<20 || defaults.LibraryMaxRows != 50000 || defaults.LibraryMaxBytes != 64<<20 || defaults.MaxLibraries != 1024 || defaults.PositiveTTL != 30*24*time.Hour || defaults.NegativeTTL != 15*time.Minute {
		t.Fatal("documented NFO resource defaults drifted")
	}
	low := NFOCachePolicy{1000, 16 << 20, 1000, 16 << 20, 1, time.Hour, time.Minute}
	high := NFOCachePolicy{1000000, 16 << 30, 500000, 16 << 30, 1024, 90 * 24 * time.Hour, 24 * time.Hour}
	for _, policy := range []NFOCachePolicy{defaults, low, high} {
		if err := ValidateNFOCachePolicy(policy); err != nil {
			t.Fatal("valid boundary refused", err)
		}
	}
	for _, change := range []func(*NFOCachePolicy){
		func(p *NFOCachePolicy) { p.MaxRows = 999 }, func(p *NFOCachePolicy) { p.MaxRows = 1000001 },
		func(p *NFOCachePolicy) { p.LibraryMaxRows = 999 }, func(p *NFOCachePolicy) { p.LibraryMaxRows = 500001 },
		func(p *NFOCachePolicy) { p.LibraryMaxRows = p.MaxRows + 1 },
		func(p *NFOCachePolicy) { p.MaxBytes = 16<<20 - 1 }, func(p *NFOCachePolicy) { p.MaxBytes = 16<<30 + 1 },
		func(p *NFOCachePolicy) { p.LibraryMaxBytes = 16<<20 - 1 }, func(p *NFOCachePolicy) { p.LibraryMaxBytes = p.MaxBytes + 1 },
		func(p *NFOCachePolicy) { p.MaxLibraries = 0 }, func(p *NFOCachePolicy) { p.MaxLibraries = 1025 },
		func(p *NFOCachePolicy) { p.PositiveTTL = time.Hour - time.Second }, func(p *NFOCachePolicy) { p.PositiveTTL = 90*24*time.Hour + time.Second },
		func(p *NFOCachePolicy) { p.NegativeTTL = time.Minute - time.Second }, func(p *NFOCachePolicy) { p.NegativeTTL = 24*time.Hour + time.Second },
		func(p *NFOCachePolicy) { p.PositiveTTL, p.NegativeTTL = time.Hour, 2*time.Hour },
		func(p *NFOCachePolicy) { p.PositiveTTL += time.Nanosecond }, func(p *NFOCachePolicy) { p.NegativeTTL += time.Microsecond },
	} {
		policy := defaults
		change(&policy)
		if !errors.Is(ValidateNFOCachePolicy(policy), ErrInvalid) {
			t.Fatal("unsafe or truncated resource limit accepted")
		}
	}
	if NFOPageMax != 32 || NFOBatchMax != 16 || NFOSweepMax != 128 || NFORequestGlobalMax != 4096 || NFORequestActorMax != 64 || NFORequestRetention != 24*time.Hour {
		t.Fatal("bounded page/retention protocol drifted")
	}
}

func TestNFOPolicyIntentAndTrustedIdentity(t *testing.T) {
	for _, mode := range []string{NFOModeOff, NFOModeReadOnly} {
		if ValidateNFOLibraryPolicy(NFOLibraryPolicy{nfoID1, mode, 1}) != nil || ValidateNFOPolicyUpdate(nfoID1, strings.Repeat("x", 128), 1, mode) != nil {
			t.Fatal("valid policy intent refused")
		}
	}
	for _, policy := range []NFOLibraryPolicy{{"bad", NFOModeOff, 1}, {nfoID1, "read-write", 1}, {nfoID1, NFOModeOff, 0}} {
		if ValidateNFOLibraryPolicy(policy) == nil {
			t.Fatal("invalid library policy accepted")
		}
	}
	for _, key := range []string{"", "with space", "with\nnewline", "中文", strings.Repeat("x", 129)} {
		if ValidateNFOPolicyUpdate(nfoID1, key, 1, NFOModeOff) == nil {
			t.Fatal("unbounded/noncanonical key accepted")
		}
	}
	if ValidateNFOPolicyUpdate("bad", "key", 1, NFOModeOff) == nil || ValidateNFOPolicyUpdate(nfoID1, "key", 0, NFOModeOff) == nil || ValidateNFOPolicyUpdate(nfoID1, "key", 1, "read-write") == nil {
		t.Fatal("invalid CAS intent accepted")
	}
	identity := DefaultNFOIdentity()
	digest, err := NFOIdentityDigest(identity)
	if err != nil || digest != "ee53267a4fa4de0baf49ae79f934896eb5f61b95c0a2e3728b78322eb38f2f53" {
		t.Fatalf("canonical parser identity drifted: %s %v", digest, err)
	}
	identity.MaxSourceBytes = NFOMaxSourceBytes
	other, err := NFOIdentityDigest(identity)
	if err != nil || other == digest {
		t.Fatal("source byte policy missing from identity")
	}
	identity.MaxSourceBytes = 1
	if ValidateNFOIdentity(identity) != nil {
		t.Fatal("valid minimum byte limit refused")
	}
	for _, change := range []func(*NFOIdentity){
		func(i *NFOIdentity) { i.ParserVersion = "future-parser" }, func(i *NFOIdentity) { i.SummarySchemaVersion++ },
		func(i *NFOIdentity) { i.FingerprintVersion = ProbeFingerprintVersion },
		func(i *NFOIdentity) { i.MaxSourceBytes = 0 }, func(i *NFOIdentity) { i.MaxSourceBytes = NFOMaxSourceBytes + 1 },
	} {
		bad := DefaultNFOIdentity()
		change(&bad)
		if value, err := NFOIdentityDigest(bad); value != "" || !errors.Is(err, ErrInvalid) {
			t.Fatal("untrusted identity accepted")
		}
	}
}

func TestNFOStampAndOrderedPrefixBounds(t *testing.T) {
	candidate := nfoCandidate(nfoID1)
	for _, size := range []int64{0, NFOMaxSourceBytes} {
		stamp := candidate.Stamp
		stamp.Size = size
		if ValidateNFOStamp(stamp) != nil {
			t.Fatal("full-hash size boundary refused")
		}
	}
	for _, change := range []func(*NFOStamp){
		func(s *NFOStamp) { s.Size = -1 }, func(s *NFOStamp) { s.Size = NFOMaxSourceBytes + 1 },
		func(s *NFOStamp) { s.SHA256 = strings.Repeat("A", 64) }, func(s *NFOStamp) { s.SHA256 = "" },
		func(s *NFOStamp) { s.FingerprintVersion = ProbeFingerprintVersion },
	} {
		stamp := candidate.Stamp
		change(&stamp)
		if ValidateNFOStamp(stamp) == nil {
			t.Fatal("unsafe source stamp accepted")
		}
	}
	for _, token := range []NFOPageToken{{1, ""}, {math.MaxInt64, nfoID1}} {
		if ValidateNFOPageToken(token) != nil {
			t.Fatal("valid cursor refused")
		}
	}
	if ValidateNFOPageToken(NFOPageToken{}) == nil || ValidateNFOPageToken(NFOPageToken{1, "../"}) == nil || ValidateNFOCandidate(nfoCandidate("bad")) == nil {
		t.Fatal("invalid inventory reference accepted")
	}
	batch := make([]NFOCandidate, NFOBatchMax)
	for i := range batch {
		batch[i] = nfoCandidate(fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1))
	}
	if ValidateNFOCandidateBatch(batch) != nil {
		t.Fatal("16-entry prefix refused")
	}
	for _, bad := range [][]NFOCandidate{nil, append(append([]NFOCandidate(nil), batch...), nfoCandidate("00000000-0000-0000-0000-000000000011")), {candidate, candidate}, {nfoCandidate(nfoID2), candidate}, {nfoCandidate("bad")}} {
		if ValidateNFOCandidateBatch(bad) == nil {
			t.Fatal("unordered, duplicate or unbounded prefix accepted")
		}
	}
}

func TestNFOCompletionPreventsRuntimeNegativeCaching(t *testing.T) {
	positive := nfoValidSummary()
	negative := NFOValidationSummary{SchemaVersion: 1, Status: NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: NFOFailureInvalidXML}
	for _, summary := range []*NFOValidationSummary{&positive, &negative} {
		if ValidateNFOCompletion(NFOCompletion{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionParsed, Summary: summary}) != nil {
			t.Fatal("fixed parse result refused")
		}
	}
	for _, kind := range []string{NFOCompletionHit, NFOCompletionNegativeHit} {
		if ValidateNFOCompletion(NFOCompletion{Candidate: nfoCandidate(nfoID1), Kind: kind}) != nil {
			t.Fatal("full-key hit refused")
		}
	}
	for _, kind := range []string{NFOCompletionChanged, NFOCompletionUnavailable, NFOCompletionRejected} {
		completion := NFOCompletion{Candidate: NFOCandidate{InventoryID: nfoID1}, Kind: kind}
		if kind == NFOCompletionRejected {
			completion.FailureCode = NFOFailureTooLarge
		}
		if ValidateNFOCompletion(completion) != nil {
			t.Fatal("noncached source outcome refused")
		}
		bad := completion
		bad.Candidate.Stamp = nfoCandidate(nfoID1).Stamp
		if ValidateNFOCompletion(bad) == nil {
			t.Fatal("failure accepted a false complete source fingerprint")
		}
		bad = completion
		bad.Summary = &negative
		if ValidateNFOCompletion(bad) == nil {
			t.Fatal("runtime/source failure accepted negative cache payload")
		}
	}
	for _, completion := range []NFOCompletion{
		{Candidate: nfoCandidate("bad"), Kind: NFOCompletionHit},
		{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionHit, Summary: &positive},
		{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionHit, FailureCode: NFOFailureInvalidXML},
		{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionParsed},
		{Candidate: NFOCandidate{InventoryID: nfoID1}, Kind: NFOCompletionParsed, Summary: &positive},
		{Candidate: NFOCandidate{InventoryID: nfoID1}, Kind: NFOCompletionRejected},
		{Candidate: nfoCandidate(nfoID1), Kind: "pending"},
	} {
		if ValidateNFOCompletion(completion) == nil {
			t.Fatal("ambiguous completion accepted")
		}
	}
	first := NFOCompletion{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionHit}
	second := NFOCompletion{Candidate: nfoCandidate(nfoID2), Kind: NFOCompletionNegativeHit}
	if ValidateNFOCompletionBatch([]NFOCompletion{first, second}) != nil {
		t.Fatal("mixed hit statuses refused")
	}
	for _, bad := range [][]NFOCompletion{nil, make([]NFOCompletion, NFOBatchMax+1), {first, first}, {second, first}, {first, {Candidate: nfoCandidate(nfoID2), Kind: NFOCompletionParsed, Summary: &positive}}} {
		if ValidateNFOCompletionBatch(bad) == nil {
			t.Fatal("fresh batch or stale/unordered input accepted")
		}
	}
	oversized := positive
	oversized.Issues = make([]NFOIssue, NFOIssuesMax+1)
	if err := ValidateNFOCompletionBatch([]NFOCompletion{{Candidate: nfoCandidate(nfoID1), Kind: NFOCompletionParsed, Summary: &oversized}}); !errors.Is(err, ErrNFOSummaryLimit) {
		t.Fatal("summary limit classification was lost")
	}
}

func TestNFOFrozenOffPhaseAndProgressSafety(t *testing.T) {
	identity := DefaultNFOIdentity()
	digest, _ := NFOIdentityDigest(identity)
	base := NFOPhase{JobID: nfoID1, LibraryID: nfoID2, Mode: NFOModeReadOnly, State: NFOPhaseWaiting, Identity: identity, IdentityDigest: digest, LibraryGeneration: 1, Token: NFOPageToken{Revision: 1}}
	if ValidateNFOPhase(base) != nil {
		t.Fatal("new waiting phase refused")
	}
	off := base
	off.Mode, off.State, off.ErrorCode = NFOModeOff, NFOPhaseAborted, NFOPhaseDisabled
	if ValidateNFOPhase(off) != nil {
		t.Fatal("durable disabled phase refused")
	}
	for _, change := range []func(*NFOPhase){
		func(p *NFOPhase) { p.JobID = "bad" }, func(p *NFOPhase) { p.LibraryID = "bad" },
		func(p *NFOPhase) { p.Mode = "read-write" }, func(p *NFOPhase) { p.LibraryGeneration = 0 },
		func(p *NFOPhase) { p.Token.Revision = 0 }, func(p *NFOPhase) { p.IdentityDigest = strings.Repeat("a", 64) },
		func(p *NFOPhase) { p.Identity.ParserVersion = "old-version" }, func(p *NFOPhase) { p.State = "queued" },
		func(p *NFOPhase) { p.ErrorCode = NFOPhaseUnavailable }, func(p *NFOPhase) { p.Token.AfterID = nfoID1 },
		func(p *NFOPhase) { p.Mode = NFOModeOff },
	} {
		bad := base
		change(&bad)
		if ValidateNFOPhase(bad) == nil {
			t.Fatal("inconsistent or unsupported executable phase accepted")
		}
	}
	for _, state := range []string{NFOPhaseRunning, NFOPhaseDone, NFOPhaseAborted} {
		phase := base
		phase.State, phase.Token.AfterID, phase.Token.Revision = state, nfoID1, 2
		phase.Progress = NFOProgress{Processed: 4, Hits: 1, NegativeHits: 1, Parsed: 1, Valid: 2, Invalid: 1, WarningFiles: 1, Changed: 1}
		if state == NFOPhaseAborted {
			phase.ErrorCode = NFOPhaseInvalidated
		}
		if ValidateNFOPhase(phase) != nil {
			t.Fatal("consistent phase refused")
		}
		phase.Mode = NFOModeOff
		if ValidateNFOPhase(phase) == nil {
			t.Fatal("off phase acquired work")
		}
	}
	for _, progress := range []NFOProgress{
		{Processed: -1}, {Processed: math.MaxInt64}, {Processed: 1},
		{Processed: 1, Hits: 1, Invalid: 1}, {Processed: 1, NegativeHits: 1, Valid: 1},
		{Processed: 1, Parsed: 1}, {Processed: 1, Changed: 1, WarningFiles: 1},
	} {
		if ValidateNFOProgress(progress) == nil {
			t.Fatal("overflow or impossible committed counters accepted")
		}
	}
	for _, code := range []NFOPhaseError{NFOPhaseDisabled, NFOPhaseInvalidated, NFOPhaseCapacity, NFOPhaseIdentityMismatch, NFOPhaseUnavailable, NFOPhaseCancelled} {
		if !ValidNFOPhaseError(code) {
			t.Fatal("fixed abort code refused")
		}
	}
	if ValidNFOPhaseError("") || ValidNFOPhaseError("private/path") {
		t.Fatal("raw abort reason accepted")
	}
}
