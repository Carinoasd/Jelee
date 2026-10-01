package legacyignore

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestResultProvenance(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		source string
		result BatchResult
		valid  bool
	}{
		{"", BatchResult{Decisions: []Decision{{Kind: BlankExclude}}}, true},
		{"", BatchResult{Decisions: []Decision{{Kind: NoMatch}}}, false},
		{"[", BatchResult{InvalidLines: []int{1}, Decisions: []Decision{{Kind: InvalidSourceExclude}}}, true},
		{"[", BatchResult{InvalidLines: []int{1}, Decisions: []Decision{{Kind: NoMatch}}}, false},
		{"# comment\n[", BatchResult{InvalidLines: []int{2}, Decisions: []Decision{{Kind: NoMatch}}}, true},
		{"# comment\n[", BatchResult{InvalidLines: []int{1, 2}, Decisions: []Decision{{Kind: InvalidSourceExclude}}}, false},
		{"a\n!a", BatchResult{Decisions: []Decision{{Kind: RuleInclude, Line: 2}}}, true},
		{"a\n!a", BatchResult{Decisions: []Decision{{Kind: RuleExclude, Line: 2}}}, false},
		{"a", BatchResult{Decisions: []Decision{{Kind: RuleExclude, Line: 9}}}, false},
		{"[\na", BatchResult{InvalidLines: []int{1}, Decisions: []Decision{{Kind: RuleExclude, Line: 1}}}, false},
		{"[", BatchResult{InvalidLines: []int{1, 1}, Decisions: []Decision{{Kind: NoMatch}}}, false},
		{"a", BatchResult{Decisions: []Decision{{Kind: 99}}}, false},
		{"a", BatchResult{Decisions: []Decision{{Kind: NoMatch, Line: 1}}}, false},
	} {
		batch := Batch{Source: tc.source, Paths: []string{"/a"}}
		frame, err := EncodeResult(ctx, batch, tc.result)
		if (err == nil) != tc.valid {
			t.Fatalf("unexpected acceptance for %+v: %v", tc.result, err)
		}
		if !tc.valid {
			continue
		}
		got, err := DecodeResult(ctx, batch, frame)
		if err != nil || !reflect.DeepEqual(got.Decisions, tc.result.Decisions) || len(got.InvalidLines) != len(tc.result.InvalidLines) {
			t.Fatal("round trip", err)
		}
		for i := 0; i < len(frame); i++ {
			if _, err := DecodeResult(ctx, batch, frame[:i]); err == nil {
				t.Fatal("truncation accepted")
			}
		}
		if _, err := DecodeResult(ctx, batch, append(bytes.Clone(frame), 0)); err == nil {
			t.Fatal("trailing data accepted")
		}
		if _, err := DecodeResult(ctx, Batch{Source: tc.source, Paths: []string{"/a", "/b"}}, frame); err == nil {
			t.Fatal("missing decisions accepted")
		}
	}
}
func FuzzResultDecoder(f *testing.F) {
	batch := Batch{Source: "a\n[", Paths: []string{"/a"}}
	data, _ := EncodeResult(context.Background(), batch, BatchResult{InvalidLines: []int{2}, Decisions: []Decision{{Kind: RuleExclude, Line: 1}}})
	f.Add(data)
	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := DecodeResult(context.Background(), batch, data)
		if err != nil {
			return
		}
		encoded, err := EncodeResult(context.Background(), batch, result)
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatal("noncanonical result")
		}
	})
}
