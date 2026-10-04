package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const probeTestID = "00000000-0000-0000-0000-000000000001"

func probeTestIdentity() ProbeIdentity {
	return ProbeIdentity{
		Platform: "linux-amd64", VendorVersion: "vendor-1", UpstreamVersion: "9.0.2", SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("b", 64), RuntimeSHA256: strings.Repeat("c", 64), ParserVersion: ProbeParserVersion, MetadataSchemaVersion: ProbeMetadataSchemaVersion, ArgumentsSHA256: strings.Repeat("d", 64), SandboxVersion: "sandbox-v1", FingerprintVersion: ProbeFingerprintVersion,
	}
}
func probeTestStamp() ProbeStamp {
	return ProbeStamp{Size: 123, ModifiedUnixNano: -123, Fingerprint: strings.Repeat("e", 64), FingerprintVersion: ProbeFingerprintVersion}
}
func probeTestRef() ProbeIdentityRef {
	digest, _ := ProbeIdentityDigest(probeTestIdentity())
	return ProbeIdentityRef{ID: probeTestID, Digest: digest}
}
func probeTestLease() ProbeLease {
	return ProbeLease{InventoryID: probeTestID, RootID: probeTestID, Path: "folder/movie.mp4", LibraryID: probeTestID, LibraryGeneration: 1, RootGeneration: 1, Identity: probeTestRef(), Stamp: probeTestStamp(), Owner: "worker-1", Generation: 1, JobID: probeTestID, JobGeneration: 1, ExpiresAt: time.Unix(1, 0)}
}

func TestProbePolicyBounds(t *testing.T) {
	valid := DefaultProbeCachePolicy()
	if err := ValidateProbeCachePolicy(valid); err != nil {
		t.Fatal(err)
	}
	cases := []func(*ProbeCachePolicy){
		func(p *ProbeCachePolicy) { p.MaxRows = 999 }, func(p *ProbeCachePolicy) { p.MaxRows = 1000001 }, func(p *ProbeCachePolicy) { p.LibraryMaxRows = 999 }, func(p *ProbeCachePolicy) { p.LibraryMaxRows = 500001 }, func(p *ProbeCachePolicy) { p.LibraryMaxRows = p.MaxRows + 1 },
		func(p *ProbeCachePolicy) { p.MaxBytes = (16 << 20) - 1 }, func(p *ProbeCachePolicy) { p.MaxBytes = math.MaxInt64 }, func(p *ProbeCachePolicy) { p.LibraryMaxBytes = (16 << 20) - 1 }, func(p *ProbeCachePolicy) { p.LibraryMaxBytes = p.MaxBytes + 1 },
		func(p *ProbeCachePolicy) { p.MaxLibraries = 0 }, func(p *ProbeCachePolicy) { p.MaxLibraries = 1025 }, func(p *ProbeCachePolicy) { p.MaxToolVersions = 0 }, func(p *ProbeCachePolicy) { p.MaxToolVersions = 33 }, func(p *ProbeCachePolicy) { p.MaxLeases = 0 }, func(p *ProbeCachePolicy) { p.MaxLeases = 9 },
		func(p *ProbeCachePolicy) { p.PositiveTTL = time.Hour - time.Second }, func(p *ProbeCachePolicy) { p.PositiveTTL = 90*24*time.Hour + time.Second }, func(p *ProbeCachePolicy) { p.NegativeTTL = time.Minute - time.Second }, func(p *ProbeCachePolicy) { p.NegativeTTL = 24*time.Hour + time.Second }, func(p *ProbeCachePolicy) { p.LeaseDuration = 9 * time.Second }, func(p *ProbeCachePolicy) { p.LeaseDuration = 5*time.Minute + time.Second },
		func(p *ProbeCachePolicy) { p.PositiveTTL += time.Microsecond }, func(p *ProbeCachePolicy) { p.NegativeTTL += time.Microsecond }, func(p *ProbeCachePolicy) { p.LeaseDuration += time.Microsecond },
	}
	for i, change := range cases {
		p := valid
		change(&p)
		if ValidateProbeCachePolicy(p) != ErrInvalid {
			t.Fatalf("invalid policy %d accepted", i)
		}
	}
}

func TestProbeIdentityPinsAndBounds(t *testing.T) {
	v := probeTestIdentity()
	digest, err := ProbeIdentityDigest(v)
	if err != nil || len(digest) != 64 {
		t.Fatal("valid identity rejected", err)
	}
	// media-metadata-v2 (attached_pic disposition, G40.4) changed this value.
	if digest != "1739172f794721157e008bd26e1ed951146401721f951b072089afa779cc3dc9" {
		t.Fatal("versioned identity encoding changed")
	}
	// Every current identity field either changes the key or fails validation;
	// unsupported parser/schema/fingerprint/platform revisions cannot be stored.
	rv := reflect.ValueOf(&v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		copy := probeTestIdentity()
		field := reflect.ValueOf(&copy).Elem().Field(i)
		if field.Kind() == reflect.String {
			s := field.String()
			if strings.HasSuffix(rv.Type().Field(i).Name, "SHA256") || rv.Type().Field(i).Name == "SourceRevision" {
				s = "f" + s[1:]
			} else {
				s += "x"
			}
			field.SetString(s)
		} else {
			field.SetInt(field.Int() + 1)
		}
		got, err := ProbeIdentityDigest(copy)
		if err == nil && got == digest {
			t.Fatalf("identity field %s omitted from key", rv.Type().Field(i).Name)
		}
		if err != nil && got != "" {
			t.Fatal("invalid identity returned digest")
		}
	}
	for _, change := range []func(*ProbeIdentity){
		func(v *ProbeIdentity) { v.Platform = "windows-amd64" }, func(v *ProbeIdentity) { v.VendorVersion = strings.Repeat("v", 257) }, func(v *ProbeIdentity) { v.UpstreamVersion = strings.Repeat("v", 65) }, func(v *ProbeIdentity) { v.SandboxVersion = strings.Repeat("v", 65) }, func(v *ProbeIdentity) { v.VendorVersion = "secret\npath" }, func(v *ProbeIdentity) { v.SandboxVersion = string([]byte{0xff}) }, func(v *ProbeIdentity) { v.SourceRevision = strings.Repeat("A", 40) }, func(v *ProbeIdentity) { v.ExecutableSHA256 = "bad" }, func(v *ProbeIdentity) { v.RuntimeSHA256 = strings.Repeat("z", 64) }, func(v *ProbeIdentity) { v.ArgumentsSHA256 = "" },
	} {
		copy := v
		change(&copy)
		if ValidateProbeIdentity(copy) != ErrInvalid {
			t.Fatal("invalid identity accepted")
		}
	}
	copy := v
	copy.VendorVersion = strings.Repeat("v", 256)
	copy.UpstreamVersion = strings.Repeat("v", 64)
	copy.SandboxVersion = strings.Repeat("v", 64)
	if ValidateProbeIdentity(copy) != nil {
		t.Fatal("SQL length boundary rejected")
	}
}

func TestProbeValuesAndOrdering(t *testing.T) {
	stamp := probeTestStamp()
	if ValidateProbeStamp(stamp) != nil {
		t.Fatal("signed nanosecond stamp rejected")
	}
	for _, s := range []ProbeStamp{{}, {Size: -1, Fingerprint: stamp.Fingerprint, FingerprintVersion: stamp.FingerprintVersion}, {Size: 1, Fingerprint: strings.Repeat("E", 64), FingerprintVersion: stamp.FingerprintVersion}, {Fingerprint: stamp.Fingerprint, FingerprintVersion: "other"}} {
		if ValidateProbeStamp(s) != ErrInvalid {
			t.Fatal("invalid stamp accepted")
		}
	}
	ref := probeTestRef()
	if ValidateProbeIdentityRef(ref) != nil || ValidateProbeIdentityRef(ProbeIdentityRef{}) != ErrInvalid {
		t.Fatal("reference validation")
	}
	for _, scope := range []string{ProbeScopeIncremental, ProbeScopeLibraryRebuild, ProbeScopeItemRebuild} {
		v := ProbePhaseStart{Identity: ref, Scope: scope}
		if scope == ProbeScopeItemRebuild {
			v.TargetItemID = probeTestID
		}
		if ValidateProbePhaseStart(v) != nil {
			t.Fatal("valid phase rejected")
		}
	}
	for _, v := range []ProbePhaseStart{{}, {Identity: ref, Scope: "other"}, {Identity: ref, Scope: ProbeScopeItemRebuild}, {Identity: ref, Scope: ProbeScopeIncremental, TargetItemID: probeTestID}} {
		if ValidateProbePhaseStart(v) != ErrInvalid {
			t.Fatal("invalid phase accepted")
		}
	}
	for _, v := range []ProbePageToken{{Revision: 1}, {Revision: math.MaxInt64, AfterID: probeTestID}} {
		if ValidateProbePageToken(v) != nil {
			t.Fatal("valid token rejected")
		}
	}
	if ValidateProbePageToken(ProbePageToken{}) != ErrInvalid || ValidateProbePageToken(ProbePageToken{Revision: 1, AfterID: "bad"}) != ErrInvalid {
		t.Fatal("invalid page accepted")
	}
	valid := ProbeCandidate{InventoryID: probeTestID, Stamp: stamp}
	second := valid
	second.InventoryID = "00000000-0000-0000-0000-000000000002"
	if ValidateProbeCandidateBatch([]ProbeCandidate{valid, second}) != nil {
		t.Fatal("ordered candidates rejected")
	}
	for _, v := range [][]ProbeCandidate{nil, make([]ProbeCandidate, 17), {{InventoryID: "bad", Stamp: stamp}}, {{InventoryID: probeTestID}}, {valid, valid}, {second, valid}} {
		if ValidateProbeCandidateBatch(v) != ErrInvalid {
			t.Fatal("invalid candidate batch accepted")
		}
	}
	lease := probeTestLease()
	if ValidateProbeLease(lease) != nil {
		t.Fatal("past local time must not reject DB-renewed lease")
	}
	for _, change := range []func(*ProbeLease){func(v *ProbeLease) { v.Path = "../escape" }, func(v *ProbeLease) { v.Path = "C:/private" }, func(v *ProbeLease) { v.Path = "x\ny" }, func(v *ProbeLease) { v.Path = strings.Repeat("x/", 128) + "file" }, func(v *ProbeLease) { v.Owner = "" }, func(v *ProbeLease) { v.Generation = 0 }, func(v *ProbeLease) { v.JobGeneration = 0 }, func(v *ProbeLease) { v.LibraryGeneration = 0 }, func(v *ProbeLease) { v.RootGeneration = 0 }, func(v *ProbeLease) { v.ExpiresAt = time.Time{} }, func(v *ProbeLease) { v.ItemGeneration = 1 }, func(v *ProbeLease) { v.ItemID = probeTestID }, func(v *ProbeLease) { v.ItemID = "bad"; v.ItemGeneration = 1 }} {
		v := lease
		change(&v)
		if ValidateProbeLease(v) != ErrInvalid {
			t.Fatal("invalid lease accepted")
		}
	}
	lease.ItemID = probeTestID
	lease.ItemGeneration = 1
	if ValidateProbeLease(lease) != nil {
		t.Fatal("valid item lease rejected")
	}
}

func TestProbeCompletionShapesAndNoRuntimeNegativeCaching(t *testing.T) {
	lease := probeTestLease()
	candidate := ProbeCandidate{InventoryID: probeTestID, Stamp: lease.Stamp}
	metadata := MediaMetadata{Streams: []MediaStream{{Index: 0, Kind: "video", Video: &MediaVideo{}}}}
	valid := []ProbeCompletion{
		{Candidate: candidate, Kind: ProbeCompletionHit}, {Candidate: candidate, Kind: ProbeCompletionNegativeHit},
		{Candidate: candidate, Kind: ProbeCompletionSucceeded, Lease: &lease, Metadata: &metadata},
		{Candidate: candidate, Kind: ProbeCompletionFailed, Lease: &lease, FailureCode: ProbeFailureMedia},
		{Candidate: ProbeCandidate{InventoryID: probeTestID}, Kind: ProbeCompletionChanged},
		{Candidate: ProbeCandidate{InventoryID: probeTestID}, Kind: ProbeCompletionUnavailable},
		{Candidate: candidate, Kind: ProbeCompletionChanged, Lease: &lease},
	}
	for _, v := range valid {
		if ValidateProbeCompletionBatch([]ProbeCompletion{v}) != nil {
			t.Fatalf("valid completion %s rejected", v.Kind)
		}
	}
	for _, code := range []ProbeFailureCode{ProbeFailureMedia, ProbeFailureMetadataInvalid, ProbeFailureMetadataLimit, ProbeFailureOutputLimit, ProbeFailureTimeout} {
		if !ValidProbeFailureCode(code) {
			t.Fatal("known failure rejected")
		}
	}
	for _, code := range []ProbeFailureCode{"probe_runtime_unavailable", "context canceled", "/private/file", "", "process_cleanup"} {
		v := valid[3]
		v.FailureCode = code
		if ValidateProbeCompletion(v) != ErrInvalid {
			t.Fatal("runtime/private error negative cached")
		}
	}
	for _, code := range []ProbePhaseError{ProbePhaseRuntimeUnavailable, ProbePhaseCapacity, ProbePhaseInvalidated, ProbePhaseIdentityMismatch} {
		if !ValidProbePhaseError(code) {
			t.Fatal("known phase failure rejected")
		}
	}
	if ValidProbePhaseError("/private/file") {
		t.Fatal("raw phase error accepted")
	}
	for _, change := range []func(*ProbeCompletion){func(v *ProbeCompletion) { v.Kind = "invalid" }, func(v *ProbeCompletion) { v.Candidate.InventoryID = "bad" }, func(v *ProbeCompletion) { v.Lease = nil }, func(v *ProbeCompletion) { v.Metadata = nil }, func(v *ProbeCompletion) { v.FailureCode = ProbeFailureMedia }, func(v *ProbeCompletion) { v.Candidate.Stamp.Size++ }, func(v *ProbeCompletion) { v.Metadata = &MediaMetadata{} }} {
		v := valid[2]
		change(&v)
		if ValidateProbeCompletion(v) != ErrInvalid {
			t.Fatal("invalid success accepted")
		}
	}
	for _, v := range []ProbeCompletion{{Candidate: candidate, Kind: ProbeCompletionHit, Lease: &lease}, {Candidate: candidate, Kind: ProbeCompletionHit, Metadata: &metadata}, {Candidate: candidate, Kind: ProbeCompletionFailed, Lease: &lease, Metadata: &metadata}, {Candidate: candidate, Kind: ProbeCompletionChanged, Metadata: &metadata}, {Candidate: ProbeCandidate{InventoryID: probeTestID, Stamp: ProbeStamp{Size: 1}}, Kind: ProbeCompletionUnavailable}} {
		if ValidateProbeCompletion(v) != ErrInvalid {
			t.Fatal("invalid completion shape accepted")
		}
	}
	hit2 := valid[0]
	hit2.Candidate.InventoryID = "00000000-0000-0000-0000-000000000002"
	if ValidateProbeCompletionBatch([]ProbeCompletion{valid[0], hit2}) != nil {
		t.Fatal("hit prefix rejected")
	}
	for _, batch := range [][]ProbeCompletion{nil, make([]ProbeCompletion, 17), {hit2, valid[0]}, {valid[0], valid[0]}, {valid[2], hit2}} {
		if ValidateProbeCompletionBatch(batch) != ErrInvalid {
			t.Fatal("non-prefix batch accepted")
		}
	}
}

func TestProbeSourceSerializationRedactsBothPaths(t *testing.T) {
	v := ProbeSource{RootPath: "/private/media", RelativePath: "private movie.mp4"}
	encoded, err := json.Marshal(v)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("source path serialized")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if s := fmt.Sprintf(format, v); strings.Contains(s, "private") {
			t.Fatal("source path leaked in formatting")
		}
	}
}
