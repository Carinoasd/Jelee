package domain

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestProbeIntentRestrictsPublicScopeAndTarget(t *testing.T) {
	id := "12345678-1234-1234-1234-123456789abc"
	for _, v := range []ProbeIntent{{}, {Scope: ProbeScopeIncremental}, {Scope: ProbeScopeLibraryRebuild}, {Scope: ProbeScopeItemRebuild, TargetItemID: id}} {
		if err := ValidateProbeIntent(v); err != nil {
			t.Fatalf("valid intent rejected: %+v: %v", v, err)
		}
	}
	for _, v := range []ProbeIntent{
		{TargetItemID: id},
		{Scope: ProbeScopeIncremental, TargetItemID: id},
		{Scope: ProbeScopeLibraryRebuild, TargetItemID: id},
		{Scope: ProbeScopeItemRebuild},
		{Scope: ProbeScopeItemRebuild, TargetItemID: "../../../private"},
		{Scope: ProbeScopeItemRebuild, TargetItemID: "12345678-1234-1234-1234-123456789ABC"},
		{Scope: "INCREMENTAL"},
		{Scope: "incremental\x00"},
		{Scope: "metadata; private-command"},
	} {
		if err := ValidateProbeIntent(v); err != ErrInvalid {
			t.Fatalf("invalid intent accepted: %+v: %v", v, err)
		}
	}
}

func TestProbePublicProjectionHasOnlyDocumentedJSONKeys(t *testing.T) {
	cases := []struct {
		name  string
		value any
		keys  []string
	}{
		{"capability", ProbeCapability{Enabled: true, Available: false, State: "unavailable", Reason: "runtime_unavailable"}, []string{"enabled", "available", "state", "reason"}},
		{"scan only summary", ProbeJobSummary{JobID: "job", LibraryID: "library", Phase: ProbeSummaryDisabled}, []string{"jobId", "libraryId", "enabled", "phase", "processed", "hits", "negativeHits", "succeeded", "failed", "changed", "unavailable"}},
		{"probe summary", ProbeJobSummary{JobID: "job", LibraryID: "library", Enabled: true, Scope: ProbeScopeItemRebuild, TargetItemID: "item", Phase: ProbeSummaryAborted, ErrorCode: "runtime_unavailable"}, []string{"jobId", "libraryId", "enabled", "scope", "targetItemId", "phase", "processed", "hits", "negativeHits", "succeeded", "failed", "changed", "unavailable", "errorCode"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			sort.Strings(tc.keys)
			if !reflect.DeepEqual(keys, tc.keys) {
				t.Fatalf("public projection keys %v; want %v", keys, tc.keys)
			}
		})
	}
}
