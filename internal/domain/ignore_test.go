package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const ignoreTestJobID = "11111111-1111-4111-8111-111111111111"
const ignoreTestLibraryID = "22222222-2222-4222-8222-222222222222"

func TestIgnoreIntentExactOptIn(t *testing.T) {
	for _, intent := range []IgnoreIntent{
		{},
		{Mode: IgnoreModeJeleeignore, CaseMode: IgnoreCaseSensitive},
		{Mode: IgnoreModeJeleeignore, CaseMode: IgnoreCaseASCIIInsensitive},
	} {
		if err := ValidateIgnoreIntent(intent); err != nil {
			t.Fatalf("supported intent rejected: %v", err)
		}
	}
	for name, intent := range map[string]IgnoreIntent{
		"off_with_case":     {CaseMode: IgnoreCaseSensitive},
		"explicit_off":      {Mode: "off"},
		"missing_case":      {Mode: IgnoreModeJeleeignore},
		"unknown_mode":      {Mode: "other", CaseMode: IgnoreCaseSensitive},
		"mode_case":         {Mode: "JELEEIGNORE", CaseMode: IgnoreCaseSensitive},
		"mode_whitespace":   {Mode: " jeleeignore", CaseMode: IgnoreCaseSensitive},
		"unknown_case":      {Mode: IgnoreModeJeleeignore, CaseMode: "unicode-insensitive"},
		"case_alias":        {Mode: IgnoreModeJeleeignore, CaseMode: "SENSITIVE"},
		"case_whitespace":   {Mode: IgnoreModeJeleeignore, CaseMode: "sensitive "},
		"embedded_control":  {Mode: IgnoreModeJeleeignore, CaseMode: "sensitive\x00"},
		"invalid_utf8":      {Mode: string([]byte{0xff}), CaseMode: IgnoreCaseSensitive},
		"unbounded_payload": {Mode: IgnoreModeJeleeignore, CaseMode: strings.Repeat("x", 65536)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateIgnoreIntent(intent); err != ErrInvalid {
				t.Fatalf("invalid intent returned %v", err)
			}
		})
	}
}

func TestIgnoreIdentityIsServerPinned(t *testing.T) {
	identity := DefaultIgnoreIdentity()
	if identity.ProgramVersion != "jeleeignore-v1" || identity.ProofVersion != "jeleeignore-proof-v1" || ValidateIgnoreIdentity(identity) != nil {
		t.Fatal("default identity does not describe the fixed contract")
	}
	for _, invalid := range []IgnoreIdentity{
		{},
		{ProgramVersion: identity.ProgramVersion},
		{ProofVersion: identity.ProofVersion},
		{ProgramVersion: "jeleeignore-v2", ProofVersion: identity.ProofVersion},
		{ProgramVersion: identity.ProgramVersion, ProofVersion: "jeleeignore-proof-v2"},
		{ProgramVersion: identity.ProgramVersion + " ", ProofVersion: identity.ProofVersion},
	} {
		if err := ValidateIgnoreIdentity(invalid); err != ErrInvalid {
			t.Fatal("unregistered identity accepted")
		}
	}
	identity.ProgramVersion = "caller mutation"
	if ValidateIgnoreIdentity(DefaultIgnoreIdentity()) != nil {
		t.Fatal("caller mutation changed the trusted default")
	}
	if ErrIgnoreUnavailable.Error() != "ignore_unavailable" || !errors.Is(fmt.Errorf("wrapped: %w", ErrIgnoreUnavailable), ErrIgnoreUnavailable) {
		t.Fatal("unavailable error is not a stable sentinel")
	}
}

func ignoreTestRequest() IgnoreRequest {
	return IgnoreRequest{
		JobID: ignoreTestJobID, LibraryID: ignoreTestLibraryID,
		Intent:   IgnoreIntent{Mode: IgnoreModeJeleeignore, CaseMode: IgnoreCaseSensitive},
		Identity: DefaultIgnoreIdentity(),
	}
}

func TestIgnoreRequestValidatesFrozenFields(t *testing.T) {
	request := ignoreTestRequest()
	for _, caseMode := range []string{IgnoreCaseSensitive, IgnoreCaseASCIIInsensitive} {
		request.Intent.CaseMode = caseMode
		if err := ValidateIgnoreRequest(request); err != nil {
			t.Fatalf("valid enabled request rejected: %v", err)
		}
	}
	for name, change := range map[string]func(*IgnoreRequest){
		"missing_job":      func(v *IgnoreRequest) { v.JobID = "" },
		"invalid_job":      func(v *IgnoreRequest) { v.JobID = "private-job" },
		"invalid_library":  func(v *IgnoreRequest) { v.LibraryID = "private-library" },
		"uppercase_uuid":   func(v *IgnoreRequest) { v.JobID = "AAAAAAAA-1111-4111-8111-111111111111" },
		"off":              func(v *IgnoreRequest) { v.Intent = IgnoreIntent{} },
		"unknown_intent":   func(v *IgnoreRequest) { v.Intent.Mode = "unknown" },
		"missing_case":     func(v *IgnoreRequest) { v.Intent.CaseMode = "" },
		"missing_identity": func(v *IgnoreRequest) { v.Identity = IgnoreIdentity{} },
		"old_program":      func(v *IgnoreRequest) { v.Identity.ProgramVersion = "old-program" },
		"old_proof":        func(v *IgnoreRequest) { v.Identity.ProofVersion = "old-proof" },
	} {
		t.Run(name, func(t *testing.T) {
			value := ignoreTestRequest()
			change(&value)
			if err := ValidateIgnoreRequest(value); err != ErrInvalid {
				t.Fatalf("invalid current request returned %v", err)
			}
		})
	}
}

func TestIgnoreRequestDoesNotSerializeInternalValues(t *testing.T) {
	const private = "private-source-or-rule-marker"
	request := IgnoreRequest{JobID: private, LibraryID: private,
		Intent:   IgnoreIntent{Mode: private, CaseMode: private},
		Identity: IgnoreIdentity{ProgramVersion: private, ProofVersion: private}}
	for _, value := range []any{request, &request} {
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "{}" {
			t.Fatalf("request JSON is not empty: err=%v", err)
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); got != "ignore request (data redacted)" || strings.Contains(got, private) {
				t.Fatal("request formatting exposed internal values")
			}
		}
	}
}

func TestIgnoreScanStageCombinationsPreserveOffBehavior(t *testing.T) {
	for _, scope := range []string{"", ProbeScopeIncremental, ProbeScopeLibraryRebuild, ProbeScopeItemRebuild} {
		for _, nfo := range []bool{false, true} {
			for _, caseMode := range []string{"", IgnoreCaseSensitive, IgnoreCaseASCIIInsensitive} {
				intent := ScanIntent{Probe: ProbeIntent{Scope: scope}, NFO: nfo}
				if scope == ProbeScopeItemRebuild {
					intent.Probe.TargetItemID = ignoreTestJobID
				}
				if caseMode != "" {
					intent.Ignore = IgnoreIntent{Mode: IgnoreModeJeleeignore, CaseMode: caseMode}
				}
				rebuild := scope == ProbeScopeLibraryRebuild || scope == ProbeScopeItemRebuild
				wantInvalid := rebuild && (nfo || caseMode != "")
				err := ValidateScanIntent(intent)
				if err != nil && err != ErrInvalid || (err != nil) != wantInvalid {
					t.Fatalf("scope=%q nfo=%v case=%q: invalid=%v want=%v", scope, nfo, caseMode, err != nil, wantInvalid)
				}
			}
		}
	}
	for _, intent := range []ScanIntent{
		{Ignore: IgnoreIntent{CaseMode: IgnoreCaseSensitive}},
		{Ignore: IgnoreIntent{Mode: IgnoreModeJeleeignore}},
		{Probe: ProbeIntent{Scope: "unknown"}},
		{Probe: ProbeIntent{Scope: ProbeScopeIncremental, TargetItemID: ignoreTestJobID}},
		{Probe: ProbeIntent{Scope: ProbeScopeItemRebuild}},
	} {
		if ValidateScanIntent(intent) != ErrInvalid {
			t.Fatal("invalid sub-intent crossed the combined boundary")
		}
	}
}
