package access

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The literal prefilter may only skip rules that cannot match: a snapshot
// must report exactly the rules a direct run of every matcher reports, for
// random globs and regexes, with and without case folding, on values that
// include the case-folding traps (Kelvin sign, long s, dotted capital I).
func TestPrefilterAgreesWithDirectMatching(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	atoms := []string{"ab", "Ab", "sk", "S", "K", "K", "ſ", "İ", "é", "-x", "1"}
	regexAtoms := []string{"ab", "(?:sk|ab)", "s+", "k{2}", "x?", "[a-c]", "(?i:ab)", "-x", "é", "1\\b", "K"}
	pick := func(set []string, n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(set[rng.IntN(len(set))])
		}
		return b.String()
	}
	for round := 0; round < 300; round++ {
		var rules []Rule
		type direct struct {
			id   string
			glob *globPattern
			re   *regexp.Regexp
			fold bool
		}
		var checks []direct
		for i := 0; i < 12; i++ {
			fold := rng.IntN(2) == 0
			id := string(rune('a'+i)) + "-" + string(rune('a'+round%26))
			if rng.IntN(2) == 0 {
				p := "*" + pick(atoms, rng.IntN(3)+1) + "*"
				if rng.IntN(3) == 0 {
					p = pick(atoms, 1) + "?" + p
				}
				rules = append(rules, Rule{ID: id, Dimension: DimUserAgent, Match: MatchGlob, Pattern: p, CaseFold: fold, Action: ActionDeny, Enabled: true})
				gp := p
				if fold {
					gp = strings.ToLower(p)
				}
				checks = append(checks, direct{id: id, glob: compileGlob(gp), fold: fold})
				continue
			}
			p := pick(regexAtoms, rng.IntN(3)+1)
			rules = append(rules, Rule{ID: id, Dimension: DimUserAgent, Match: MatchRegex, Pattern: p, CaseFold: fold, Action: ActionDeny, Enabled: true})
			rp := p
			if fold {
				rp = "(?i)" + p
			}
			checks = append(checks, direct{id: id, re: regexp.MustCompile(rp)})
		}
		s := mustCompile(t, testOptions(), rules...)
		for v := 0; v < 40; v++ {
			value := pick(atoms, rng.IntN(6)+1)
			req := baseRequest()
			req.UserAgent = value
			var got, want []string
			for _, h := range s.Evaluate(req).Matched {
				got = append(got, h.RuleID)
			}
			for _, c := range checks {
				ok := false
				if c.re != nil {
					ok = c.re.MatchString(value)
				} else if c.fold {
					ok = c.glob.match(strings.ToLower(value))
				} else {
					ok = c.glob.match(value)
				}
				if ok {
					want = append(want, c.id)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("value %q: snapshot %v, direct %v (rules %+v)", value, got, want, rules)
			}
		}
	}
}

func TestRegexLiterals(t *testing.T) {
	for _, c := range []struct {
		pattern string
		want    []string
		folded  bool
		ok      bool
	}{
		{`(?:crawler|spider)-12\b`, []string{"crawler", "spider"}, false, true},
		{`^curl/[0-9]+`, []string{"curl/"}, false, true},
		{`(?i)crawler`, []string{"crawler"}, true, true},
		{`(?i)spider`, nil, false, false}, // folded 's' is not searchable lower-cased
		{`a*`, nil, false, false},
		{`[a-z]+bot`, []string{"bot"}, false, true},
		{`Foo(?i:bar)`, []string{"Foo"}, false, true},
		{`x`, nil, false, false}, // too short to be worth filing
	} {
		got, folded, ok := regexLiterals(c.pattern)
		if ok != c.ok || folded != c.folded || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v %v", c.pattern, got, folded, ok)
		}
	}
}
