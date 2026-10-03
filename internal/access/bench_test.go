package access

import (
	"fmt"
	"net/netip"
	"testing"
)

// benchRules builds a mixed rule set: mostly exact device/API-key entries
// (the common block-list shape), plus prefix, glob, regex and CIDR rules.
func benchRules(n int) []Rule {
	rules := make([]Rule, 0, n)
	for i := 0; len(rules) < n; i++ {
		id := fmt.Sprintf("r%05d", i)
		var r Rule
		switch i % 10 {
		case 0, 1, 2, 3:
			r = Rule{Dimension: DimDeviceID, Match: MatchExact, Pattern: fmt.Sprintf("device-%d", i)}
		case 4, 5:
			r = Rule{Dimension: DimAPIKey, Match: MatchExact, Pattern: fmt.Sprintf("sha256:%08x", i), CaseFold: true}
		case 6:
			r = Rule{Dimension: DimUserAgent, Match: MatchPrefix, Pattern: fmt.Sprintf("Client%d/", i), CaseFold: true}
		case 7:
			r = Rule{Dimension: DimUserAgent, Match: MatchGlob, Pattern: fmt.Sprintf("*Bot%d*(*)", i)}
		case 8:
			if i%100 == 8 {
				r = Rule{Dimension: DimUserAgent, Match: MatchRegex, Pattern: fmt.Sprintf(`(?:crawler|spider)-%d\b`, i)}
			} else {
				r = Rule{Dimension: DimAppName, Match: MatchExact, Pattern: fmt.Sprintf("App%d", i)}
			}
		case 9:
			r = Rule{Dimension: DimIP, Match: MatchCIDR, Pattern: fmt.Sprintf("10.%d.%d.0/24", (i/256)%256, i%256)}
		}
		r.ID, r.Action, r.Priority, r.Enabled = id, ActionDeny, i%7, true
		rules = append(rules, r)
	}
	return rules
}

func benchmarkEvaluate(b *testing.B, n int) {
	s, err := Compile(benchRules(n), testOptions())
	if err != nil {
		b.Fatal(err)
	}
	req := baseRequest()
	req.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"
	req.IP = netip.MustParseAddr("192.0.2.44")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if s.Evaluate(req).Verdict != VerdictAllow {
			b.Fatal("unexpected block")
		}
	}
}

func BenchmarkEvaluate1k(b *testing.B)  { benchmarkEvaluate(b, 1000) }
func BenchmarkEvaluate10k(b *testing.B) { benchmarkEvaluate(b, 10000) }

func BenchmarkCompile10k(b *testing.B) {
	rules := benchRules(10000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Compile(rules, testOptions()); err != nil {
			b.Fatal(err)
		}
	}
}
