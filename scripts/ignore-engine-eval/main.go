// Command ignore-engine-eval compares candidate engines against fixed upstream
// evidence. It is a separate tooling module, never linked into the server.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
	"unicode/utf16"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	v1 "github.com/dlclark/regexp2"
	v2 "github.com/dlclark/regexp2/v2"
)

type sample struct {
	Name    string
	Rules   []string
	Path    string
	Errors  []string
	Ignored bool
}
type matcher interface{ MatchRunes([]rune) (bool, error) }

func main() {
	defer v1.StopTimeoutClock()
	defer v2.StopTimeoutClock()
	data, err := os.ReadFile("../../internal/platform/legacyignore/testdata/ignore-021.json")
	if err != nil {
		panic(err)
	}
	var oracle struct{ Cases, EngineCases []sample }
	if err = json.Unmarshal(data, &oracle); err != nil {
		panic(err)
	}
	for _, version := range []string{"v1.12.0", "v2.8.1"} {
		for _, adapt := range []bool{false, true} {
			mismatches := []string{}
			for _, c := range append(oracle.Cases, oracle.EngineCases...) {
				ignored, invalid := false, 0
				for _, rule := range c.Rules {
					e, err := legacyignore.Translate(rule)
					if err != nil {
						panic(err)
					}
					if e.Inactive {
						continue
					}
					pattern := e.Pattern
					if adapt {
						pattern, err = legacyignore.UTF16Pattern(pattern)
						if err != nil {
							panic(err)
						}
					}
					var re matcher
					if version == "v1.12.0" {
						r, compileErr := v1.Compile(pattern, v1.IgnoreCase)
						err = compileErr
						if err == nil {
							r.MatchTimeout = 50 * time.Millisecond
							re = r
						}
					} else {
						r, compileErr := v2.Compile(pattern, v2.IgnoreCase, v2.OptionMaxBacktrackingStackSize(10000))
						err = compileErr
						if err == nil {
							r.MatchTimeout = 50 * time.Millisecond
							re = r
						}
					}
					if err != nil {
						invalid++
						continue
					}
					input := []rune(c.Path)
					if adapt {
						units := utf16.Encode(input)
						input = make([]rune, len(units))
						for i, u := range units {
							input[i] = rune(u)
						}
					}
					matched, err := re.MatchRunes(input)
					if err != nil {
						panic("candidate execution error")
					}
					if matched {
						ignored = !e.Negative
					}
				}
				if ignored != c.Ignored || invalid != len(c.Errors) {
					mismatches = append(mismatches, c.Name)
				}
			}
			report := map[string]any{"version": version, "utf16PatternAndInput": adapt, "cases": len(oracle.Cases) + len(oracle.EngineCases), "mismatches": mismatches}
			b, err := json.Marshal(report)
			if err != nil {
				panic(err)
			}
			fmt.Println(string(b))
		}
	}
}
