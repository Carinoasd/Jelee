package ignore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func contractSources(count int, text string) []Source {
	out := make([]Source, count)
	for i := range out {
		out[i] = Source{Path: fmt.Sprintf("d%03d/.jeleeignore", i), Text: []byte(text)}
	}
	return out
}

// Comment-only bytes isolate aggregate text limits from rule/token limits.
func contractComments(size int) string {
	var b strings.Builder
	b.Grow(size)
	for size > 0 {
		n := min(size, 1024)
		b.WriteByte('#')
		if n > 1 {
			b.WriteString(strings.Repeat("a", n-2))
			b.WriteByte('\n')
		}
		size -= n
	}
	return b.String()
}

func contractUnicodeComments(size int) string {
	var b strings.Builder
	b.Grow(size)
	for size > 0 {
		n := min(size, 3072)
		b.WriteByte('#')
		if n > 1 {
			b.WriteString(strings.Repeat("界", (n-2)/3))
			b.WriteString(strings.Repeat("a", (n-2)%3))
			b.WriteByte('\n')
		}
		size -= n
	}
	return b.String()
}

func TestLimitsCompilationBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		at, over func() []Source
	}{
		{"sources", func() []Source { return contractSources(MaxSources, "") }, func() []Source { return contractSources(MaxSources+1, "") }},
		{"raw source bytes", func() []Source { return contractSources(1, contractComments(MaxSourceBytes)) }, func() []Source { return contractSources(1, contractComments(MaxSourceBytes+1)) }},
		{"source physical lines", func() []Source { return contractSources(1, strings.Repeat("\n", MaxSourceLines)) }, func() []Source { return contractSources(1, strings.Repeat("\n", MaxSourceLines+1)) }},
		{"aggregate physical lines", func() []Source {
			return contractSources(MaxTotalLines/MaxSourceLines, strings.Repeat("\n", MaxSourceLines))
		}, func() []Source {
			return append(contractSources(MaxTotalLines/MaxSourceLines, strings.Repeat("\n", MaxSourceLines)), Source{Path: "extra/.jeleeignore", Text: []byte("\n")})
		}},
		{"line and literal pattern tokens", func() []Source { return contractSources(1, strings.Repeat("a", MaxLineBytes)) }, func() []Source { return contractSources(1, strings.Repeat("a", MaxLineBytes+1)) }},
		{"aggregate tokens", func() []Source {
			return contractSources(1, strings.Repeat(strings.Repeat("a", MaxPatternTokens)+"\n", MaxTokens/MaxPatternTokens))
		}, func() []Source {
			return contractSources(1, strings.Repeat(strings.Repeat("a", MaxPatternTokens)+"\n", MaxTokens/MaxPatternTokens)+"a")
		}},
		{"character classes", func() []Source {
			return contractSources(1, strings.Repeat(strings.Repeat("[a]", 1024)+"\n", MaxClasses/1024))
		}, func() []Source {
			return contractSources(1, strings.Repeat(strings.Repeat("[a]", 1024)+"\n", MaxClasses/1024)+"[b]")
		}},
		{"aggregate raw bytes", func() []Source {
			return contractSources(MaxTotalSourceBytes/MaxSourceBytes, contractComments(MaxSourceBytes))
		}, func() []Source {
			return append(contractSources(MaxTotalSourceBytes/MaxSourceBytes, contractComments(MaxSourceBytes)), Source{Path: "extra/.jeleeignore", Text: []byte("#")})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Compile(context.Background(), tc.at(), Options{})
			if err != nil || p == nil {
				t.Fatal("exact boundary rejected", err)
			}
			p, err = Compile(context.Background(), tc.over(), Options{})
			if !errors.Is(err, ErrLimit) || p != nil {
				t.Fatal("boundary plus one did not fail without program", err)
			}
		})
	}
}

func TestLimitsDecodedAggregateIsIndependentOfRawBytes(t *testing.T) {
	makeSources := func(total int) []Source {
		var sources []Source
		for total > 0 {
			n := min(total, 200<<10)
			text := contractUTF16(contractUnicodeComments(n), false)
			if len(text) > MaxSourceBytes {
				t.Fatal("invalid boundary fixture")
			}
			sources = append(sources, Source{Path: fmt.Sprintf("u%d/.jeleeignore", len(sources)), Text: text})
			total -= n
		}
		return sources
	}
	for _, over := range []int{0, 1} {
		sources := makeSources(MaxTotalDecodedBytes + over)
		raw := 0
		for _, s := range sources {
			raw += len(s.Text)
		}
		if raw >= MaxTotalSourceBytes {
			t.Fatal("decoded limit masked by raw input limit")
		}
		p, err := Compile(context.Background(), sources, Options{})
		if over == 0 && (err != nil || p == nil) || over == 1 && (!errors.Is(err, ErrLimit) || p != nil) {
			t.Fatal("decoded aggregate boundary", over, err)
		}
	}
}

func TestLimitsRulesReachCapWithoutTokenAmplification(t *testing.T) {
	// MaxRules equals MaxTotalLines. A +1 public input necessarily also exceeds
	// the physical-line limit; do not pretend that isolates the rules guard.
	sources := contractSources(MaxRules/MaxSourceLines, strings.Repeat("x\n", MaxSourceLines))
	p, err := Compile(context.Background(), sources, Options{})
	if err != nil || p == nil {
		t.Fatal("maximum number of short rules rejected", err)
	}
	sources = append(sources, Source{Path: "extra/.jeleeignore", Text: []byte("x")})
	if p, err = Compile(context.Background(), sources, Options{}); !errors.Is(err, ErrLimit) || p != nil {
		t.Fatal("rule/line aggregate overflow accepted", err)
	}
}

func TestLimitsPathBytesAndComponents(t *testing.T) {
	p := contractCompile(t, "*")
	for _, name := range []string{strings.Repeat("a", MaxPathBytes), strings.TrimSuffix(strings.Repeat("a/", MaxPathComponents), "/")} {
		contractMatch(t, p, name, File, Exclude)
	}
	for _, name := range []string{strings.Repeat("a", MaxPathBytes+1), strings.TrimSuffix(strings.Repeat("a/", MaxPathComponents+1), "/")} {
		if m, err := p.Evaluate(context.Background(), name, File); !errors.Is(err, ErrLimit) || m != (Match{}) {
			t.Fatal("candidate limit not enforced", err)
		}
	}
	for _, name := range []string{strings.Repeat("a", MaxPathBytes-len("/.jeleeignore")) + "/.jeleeignore", strings.Repeat("a/", MaxPathComponents-1) + ".jeleeignore"} {
		if p, err := Compile(context.Background(), []Source{{Path: name}}, Options{}); err != nil || p == nil {
			t.Fatal("source path exact boundary", err)
		}
		if p, err := Compile(context.Background(), []Source{{Path: "a/" + name}}, Options{}); !errors.Is(err, ErrLimit) || p != nil {
			t.Fatal("source path exceeded boundary", err)
		}
	}
}

type contractPollContext struct {
	context.Context
	calls    int
	cancelAt int
}

func (c *contractPollContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestLimitsWorkMeterExactBudgetAndPolling(t *testing.T) {
	for _, maximum := range []int{MaxCompileWork, MaxEvaluateWork} {
		m := newWorkMeter(context.Background(), maximum)
		for left := maximum; left > 0; {
			n := min(left, 1024)
			if err := m.spend(n); err != nil {
				t.Fatal("exact work boundary rejected", err)
			}
			left -= n
		}
		if err := m.spend(1); !errors.Is(err, ErrWorkLimit) {
			t.Fatal("work overrun accepted")
		}
	}
	c := &contractPollContext{Context: context.Background(), cancelAt: 2}
	m := newWorkMeter(c, MaxEvaluateWork)
	used := 0
	for ; used <= 1024; used++ {
		if err := m.spend(1); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			break
		}
	}
	if used > 1024 || c.calls != 2 {
		t.Fatal("meter failed to check within 1024 charged units")
	}
	if err := newWorkMeter(context.Background(), 10).spend(-1); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative work changed accounting")
	}
}

func TestLimitsAdversarialEvaluationExhaustsWorkAndReturnsZero(t *testing.T) {
	p := contractCompile(t, strings.Repeat("*a*a*a*a*a*a*a*a*a*a*b\n", 1024))
	m, err := p.Evaluate(context.Background(), strings.Repeat("a", MaxPathBytes), File)
	if !errors.Is(err, ErrWorkLimit) || m != (Match{}) {
		t.Fatal("adversarial transition work not bounded", err)
	}
	contractMatch(t, p, "aaaaaaaaaab", File, Exclude)
}

func TestLimitsCompilationChargesCombinedSortAndTextWork(t *testing.T) {
	// Each individual input dimension remains valid. Reverse-ordered, long
	// source paths plus comment text spend a shared budget, not a fresh budget
	// per source or per stage of compilation.
	sources := make([]Source, MaxSources)
	for i := range sources {
		suffix := fmt.Sprintf("/d%03d/.jeleeignore", MaxSources-i-1)
		sources[i] = Source{
			Path: strings.Repeat("a", MaxPathBytes-len(suffix)) + suffix,
			Text: []byte(contractComments(17 << 10)),
		}
	}
	if p, err := Compile(context.Background(), sources, Options{}); !errors.Is(err, ErrWorkLimit) || p != nil {
		t.Fatal("combined valid input dimensions bypassed compile work budget", err)
	}
	// The same text without the expensive source ordering remains usable.
	if p, err := Compile(context.Background(), contractSources(MaxSources, contractComments(17<<10)), Options{}); err != nil || p == nil {
		t.Fatal("ordinary source values rejected after exhausted compilation", err)
	}
}
