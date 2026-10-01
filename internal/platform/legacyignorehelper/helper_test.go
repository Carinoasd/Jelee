package legacyignorehelper

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/dlclark/regexp2"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--verify-memory-limit" {
		if applyLimits() != nil || verifyAllocationDenied() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == Command {
		os.Exit(Main())
	}
	code := m.Run()
	regexp2.StopTimeoutClock()
	os.Exit(code)
}

func TestEvaluateWrapperAndProvenance(t *testing.T) {
	for _, tc := range []struct {
		source string
		paths  []string
		want   []legacyignore.Decision
	}{
		{"", []string{"/a"}, []legacyignore.Decision{{Kind: legacyignore.BlankExclude}}},
		{"(\x00|a).mkv", []string{"/media/a.mkv", "/media/b.mkv"}, []legacyignore.Decision{{Kind: legacyignore.RuleExclude, Line: 1}, {Kind: legacyignore.NoMatch}}},
		{"[", []string{"/a"}, []legacyignore.Decision{{Kind: legacyignore.InvalidSourceExclude}}},
		{"# comment\n[", []string{"/a"}, []legacyignore.Decision{{Kind: legacyignore.NoMatch}}},
		{"*.mkv\n!a.mkv\n*.mkv", []string{"/a.mkv", "/b.mkv"}, []legacyignore.Decision{{Kind: legacyignore.RuleExclude, Line: 3}, {Kind: legacyignore.RuleExclude, Line: 1}}},
		{"*.mkv\n !a.mkv ", []string{"/a.mkv"}, []legacyignore.Decision{{Kind: legacyignore.RuleInclude, Line: 2}}},
		{`\😀.mkv`, []string{"/😀.mkv"}, []legacyignore.Decision{{Kind: legacyignore.RuleExclude, Line: 1}}},
	} {
		batch := legacyignore.Batch{Source: tc.source, Paths: tc.paths}
		result, err := evaluate(batch)
		if err != nil || !reflect.DeepEqual(result.Decisions, tc.want) {
			t.Fatal("wrapper/provenance mismatch", err)
		}
		if err := legacyignore.ValidateResult(context.Background(), batch, result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHelperProcess(t *testing.T) {
	if raceEnabled {
		t.Skip("race runtime reserves address space beyond the production helper limit; run subprocess acceptance without -race")
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("unsupported resource platform")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, exe, "--verify-memory-limit").Run(); err != nil {
		t.Fatalf("hard allocation limit: %v", err)
	}
	batch := legacyignore.Batch{Source: "*.mkv\n!a.mkv", Paths: []string{"/a.mkv", "/b.mkv"}}
	frame, err := legacyignore.EncodeBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, Command)
	cmd.Stdin = bytes.NewReader(frame)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("limited helper failed: %v", err)
	}
	result, err := legacyignore.DecodeResult(ctx, batch, output)
	if err != nil || result.Decisions[0].Kind != legacyignore.RuleInclude || result.Decisions[1].Kind != legacyignore.RuleExclude {
		t.Fatal("helper output", err)
	}
	for _, input := range [][]byte{[]byte("invalid"), frame[:len(frame)-1]} {
		cmd = exec.CommandContext(ctx, exe, Command)
		cmd.Stdin = bytes.NewReader(input)
		output, err = cmd.Output()
		if err == nil || len(output) != 0 {
			t.Fatal("malformed helper input accepted")
		}
	}
}

func TestEvaluationTimeoutReturnsNoPartialResults(t *testing.T) {
	batch := legacyignore.Batch{Source: "(a|aa){1,100}b", Paths: []string{"/b", "/" + strings.Repeat("a", 40)}}
	result, err := evaluate(batch)
	if err == nil || len(result.Decisions) != 0 {
		t.Fatal("timeout produced successful or partial decisions")
	}
}
