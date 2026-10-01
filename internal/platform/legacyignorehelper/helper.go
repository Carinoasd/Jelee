// Package legacyignorehelper is the one-shot legacy regex child process.
// Its entry point must run before application configuration or services.
package legacyignorehelper

import (
	"context"
	"io"
	"os"
	"runtime/debug"
	"time"
	"unicode/utf16"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/dlclark/regexp2"
)

const Command = "--internal-ignore-helper"
const (
	ExitInvalid     = 64
	ExitUnavailable = 78
	ExitEvaluation  = 70
	MemoryBytes     = 2 << 30
)

// Main only accepts the fixed command with no extra arguments. Limits precede
// all untrusted input. No raw regex errors, source text or paths are printed.
// The owning parent must additionally enforce wall time and reap this process.
func Main() int {
	if len(os.Args) != 2 || os.Args[1] != Command {
		return ExitInvalid
	}
	if applyLimits() != nil {
		return ExitUnavailable
	}
	debug.SetMemoryLimit(128 << 20) // GC target, not the hard OS allocation limit.
	data, err := io.ReadAll(io.LimitReader(os.Stdin, legacyignore.MaxBatchBytes+1))
	if err != nil {
		return ExitInvalid
	}
	batch, err := legacyignore.DecodeBatch(context.Background(), data)
	if err != nil {
		return ExitInvalid
	}
	result, err := evaluate(batch)
	if err != nil {
		return ExitEvaluation
	}
	encoded, err := legacyignore.EncodeResult(context.Background(), batch, result)
	if err != nil {
		return ExitEvaluation
	}
	if _, err = os.Stdout.Write(encoded); err != nil {
		return ExitEvaluation
	}
	return 0
}

type compiledRule struct {
	line     int
	negative bool
	regex    *regexp2.Regexp
}

func evaluate(batch legacyignore.Batch) (legacyignore.BatchResult, error) {
	ctx := context.Background()
	source, err := legacyignore.PrepareSource(ctx, batch.Source)
	if err != nil {
		return legacyignore.BatchResult{}, err
	}
	result := legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(batch.Paths))}
	rules := make([]compiledRule, 0, len(source.Rules))
	for _, rule := range source.Rules {
		expr, err := legacyignore.Translate(rule.Text)
		if err != nil {
			return legacyignore.BatchResult{}, err
		}
		if expr.Inactive {
			rules = append(rules, compiledRule{line: rule.Line})
			continue
		}
		pattern, err := legacyignore.UTF16Pattern(expr.Pattern)
		if err != nil {
			return legacyignore.BatchResult{}, err
		}
		regex, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
		if err != nil {
			result.InvalidLines = append(result.InvalidLines, rule.Line)
			continue
		}
		regex.MatchTimeout = 50 * time.Millisecond
		rules = append(rules, compiledRule{line: rule.Line, negative: expr.Negative, regex: regex})
	}
	for i, path := range batch.Paths {
		if source.Blank {
			result.Decisions[i].Kind = legacyignore.BlankExclude
			continue
		}
		if len(rules) == 0 {
			result.Decisions[i].Kind = legacyignore.InvalidSourceExclude
			continue
		}
		units := utf16.Encode([]rune(path))
		input := make([]rune, len(units))
		for j, v := range units {
			input[j] = rune(v)
		}
		ignored := false
		for _, rule := range rules {
			// Match only state-changing rules, exactly as the pinned dependency does.
			if rule.regex == nil || rule.negative != ignored {
				continue
			}
			matched, err := rule.regex.MatchRunes(input)
			if err != nil {
				return legacyignore.BatchResult{}, err
			}
			if matched {
				ignored = !rule.negative
				kind := legacyignore.RuleExclude
				if rule.negative {
					kind = legacyignore.RuleInclude
				}
				result.Decisions[i] = legacyignore.Decision{Kind: kind, Line: rule.line}
			}
		}
	}
	return result, nil
}
