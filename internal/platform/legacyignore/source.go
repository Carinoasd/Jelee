package legacyignore

import (
	"context"
	"strings"
)

const MaxSourceLines = 4096

// SourceRule preserves the physical source line after the pinned wrapper's
// TrimEntries and RemoveEmptyEntries. Comments are retained because a valid
// comment counts as a successful Add in that wrapper's all-invalid policy.
type SourceRule struct {
	Line int
	Text string
}

func (SourceRule) String() string   { return "legacy ignore rule (data redacted)" }
func (SourceRule) GoString() string { return "legacy ignore rule (data redacted)" }

type PreparedSource struct {
	Blank bool
	Rules []SourceRule
}

func (PreparedSource) String() string   { return "legacy ignore source (data redacted)" }
func (PreparedSource) GoString() string { return "legacy ignore source (data redacted)" }

// PrepareSource applies the fixed repository wrapper's newline/trim behavior.
// Blank means the wrapper excludes everything. A nonblank source with comments
// alone does not exclude everything. Regex validity and the all-invalid policy
// require a compatible compiler and cannot be inferred here.
func PrepareSource(ctx context.Context, text string) (PreparedSource, error) {
	if ctx == nil {
		return PreparedSource{}, ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return PreparedSource{}, err
	}
	if !batchStringValid(text, MaxBatchSourceBytes, true) {
		return PreparedSource{}, ErrBatch
	}
	result := PreparedSource{Blank: strings.TrimSpace(text) == ""}
	if result.Blank {
		return result, nil
	}
	for line := 1; ; line++ {
		if err := ctx.Err(); err != nil {
			return PreparedSource{}, err
		}
		if line > MaxSourceLines {
			return PreparedSource{}, ErrBatch
		}
		before, after, found := strings.Cut(text, "\n")
		rule := strings.TrimSpace(before)
		if len(rule) > MaxPatternBytes {
			return PreparedSource{}, ErrBatch
		}
		if rule != "" {
			result.Rules = append(result.Rules, SourceRule{Line: line, Text: rule})
		}
		if !found {
			break
		}
		text = after
	}
	return result, nil
}
