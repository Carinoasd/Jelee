package legacyignore

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPinnedWrapperSourcePreparation(t *testing.T) {
	for _, tc := range []struct {
		text string
		want PreparedSource
	}{
		{"", PreparedSource{Blank: true}},
		{" \t\r\n\u0085\u3000", PreparedSource{Blank: true}},
		{" # comment\r\n\n a.mkv \n !b.mkv\t", PreparedSource{Rules: []SourceRule{{1, "# comment"}, {3, "a.mkv"}, {4, "!b.mkv"}}}},
		{"\\# literal\n[\n", PreparedSource{Rules: []SourceRule{{1, `\# literal`}, {2, "["}}}},
		{"a\rb", PreparedSource{Rules: []SourceRule{{1, "a\rb"}}}},
		{"\ufeff", PreparedSource{Rules: []SourceRule{{1, "\ufeff"}}}},
	} {
		got, err := PrepareSource(context.Background(), tc.text)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("source preparation differs: %#v %v", got, err)
		}
	}
}
func TestSourcePreparationBoundsAndCancel(t *testing.T) {
	for _, text := range []string{"\x00", string([]byte{0xff}), strings.Repeat("x", MaxPatternBytes+1), strings.Repeat("\n", MaxSourceLines) + "x", strings.Repeat(" ", MaxBatchSourceBytes+1)} {
		if _, err := PrepareSource(context.Background(), text); err != ErrBatch {
			t.Fatal("invalid source accepted")
		}
	}
	text := strings.Repeat("\n", MaxSourceLines-1) + "a"
	got, err := PrepareSource(context.Background(), text)
	if err != nil || len(got.Rules) != 1 || got.Rules[0].Line != MaxSourceLines {
		t.Fatal("physical line bound", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareSource(ctx, "a"); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := PrepareSource(nil, "a"); err != ErrBatch {
		t.Fatal(err)
	}
}
