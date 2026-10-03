// benchgate compares two `go test -bench` outputs (a baseline and the current
// run) and fails when a benchmark regresses past a configured threshold.
// Repeated runs of one benchmark (-count) are reduced to their median before
// comparing, so a single noisy sample cannot pass or fail the gate alone.
//
//	go run ./tools/benchgate -base docs/evidence/bench-baseline.txt -current .testdata/bench-current.txt
//
// Exit status: 0 no regression, 1 regression or missing benchmark, 2 usage or
// input error.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Metric units the gate understands. Custom b.ReportMetric units are parsed
// but never gated.
const (
	unitNs     = "ns/op"
	unitBytes  = "B/op"
	unitAllocs = "allocs/op"
)

var gatedUnits = []string{unitNs, unitBytes, unitAllocs}

// Samples maps a qualified benchmark name to every value seen per unit.
type Samples map[string]map[string][]float64

// procSuffix is the GOMAXPROCS suffix go test appends (BenchmarkX-16). It is
// dropped so baselines from machines with different core counts still match.
var procSuffix = regexp.MustCompile(`-\d+$`)

// Parse reads `go test -bench` text output. Benchmark names are qualified by
// the most recent "pkg:" line so equal names in different packages stay
// distinct. Lines that are not benchmark results are ignored.
func Parse(r io.Reader) (Samples, error) {
	samples := Samples{}
	pkg := ""
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "pkg:"); ok {
			pkg = strings.TrimSpace(rest)
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		fields := strings.Fields(line)
		// name, iterations, then value/unit pairs.
		if len(fields) < 4 || len(fields)%2 != 0 {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		name := procSuffix.ReplaceAllString(fields[0], "")
		if pkg != "" {
			name = pkg + "." + name
		}
		values := map[string]float64{}
		valid := true
		for i := 2; i < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				valid = false
				break
			}
			values[fields[i+1]] = v
		}
		if !valid {
			continue
		}
		if _, ok := values[unitNs]; !ok {
			continue
		}
		units := samples[name]
		if units == nil {
			units = map[string][]float64{}
			samples[name] = units
		}
		for unit, v := range values {
			units[unit] = append(units[unit], v)
		}
	}
	return samples, scanner.Err()
}

// Median returns the median of values; ok is false for an empty slice. The
// input is not modified.
func Median(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid], true
	}
	return (sorted[mid-1] + sorted[mid]) / 2, true
}

// Thresholds are maximum allowed increases in percent per unit. A negative
// threshold disables gating for that unit (it is still reported).
type Thresholds map[string]float64

// Delta is one benchmark/unit comparison of medians.
type Delta struct {
	Name, Unit    string
	Base, Current float64
	// Percent is the relative change; +Inf when the baseline is zero and the
	// current value is not.
	Percent    float64
	Regression bool
}

// Report is the outcome of comparing two sample sets.
type Report struct {
	Deltas []Delta
	// Missing lists baseline benchmarks absent from the current run; Added
	// lists current benchmarks with no baseline.
	Missing, Added []string
}

func (r Report) Regressions() []Delta {
	var out []Delta
	for _, d := range r.Deltas {
		if d.Regression {
			out = append(out, d)
		}
	}
	return out
}

// Compare computes median deltas for every benchmark present in both inputs.
// A unit gated by thresholds but absent from one side (e.g. a run without
// -benchmem) is skipped, not treated as a regression.
func Compare(base, current Samples, limits Thresholds) Report {
	var report Report
	for _, name := range sortedNames(base) {
		cur, ok := current[name]
		if !ok {
			report.Missing = append(report.Missing, name)
			continue
		}
		for _, unit := range gatedUnits {
			b, okBase := Median(base[name][unit])
			c, okCur := Median(cur[unit])
			if !okBase || !okCur {
				continue
			}
			d := Delta{Name: name, Unit: unit, Base: b, Current: c, Percent: percent(b, c)}
			if limit, ok := limits[unit]; ok && limit >= 0 && d.Percent > limit {
				d.Regression = true
			}
			report.Deltas = append(report.Deltas, d)
		}
	}
	for _, name := range sortedNames(current) {
		if _, ok := base[name]; !ok {
			report.Added = append(report.Added, name)
		}
	}
	return report
}

func percent(base, current float64) float64 {
	switch {
	case base == current:
		return 0
	case base == 0:
		return math.Inf(1)
	default:
		return (current - base) * 100 / base
	}
}

func sortedNames(s Samples) []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func formatPercent(p float64) string {
	if math.IsInf(p, 1) {
		return "+inf%"
	}
	return fmt.Sprintf("%+.1f%%", p)
}

func readSamples(path string) (Samples, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	samples, err := Parse(file)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, errors.New("no benchmark results found")
	}
	return samples, nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("benchgate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "baseline `go test -bench` output")
	currentPath := flags.String("current", "", "current `go test -bench` output")
	nsLimit := flags.Float64("ns", 15, "max ns/op increase in percent (negative disables)")
	allocsLimit := flags.Float64("allocs", 10, "max allocs/op increase in percent (negative disables)")
	bytesLimit := flags.Float64("bytes", -1, "max B/op increase in percent (negative disables; reported either way)")
	allowMissing := flags.Bool("allow-missing", false, "do not fail when a baseline benchmark is absent from the current run")
	match := flags.String("match", "", "only compare benchmarks whose qualified name matches this regexp")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *basePath == "" || *currentPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: benchgate -base FILE -current FILE [-ns PCT] [-allocs PCT] [-bytes PCT] [-allow-missing] [-match RE]")
		return 2
	}
	base, err := readSamples(*basePath)
	if err != nil {
		fmt.Fprintf(stderr, "benchgate: baseline %s: %v\n", *basePath, err)
		return 2
	}
	current, err := readSamples(*currentPath)
	if err != nil {
		fmt.Fprintf(stderr, "benchgate: current %s: %v\n", *currentPath, err)
		return 2
	}
	if *match != "" {
		re, err := regexp.Compile(*match)
		if err != nil {
			fmt.Fprintf(stderr, "benchgate: -match: %v\n", err)
			return 2
		}
		base, current = filter(base, re), filter(current, re)
	}
	report := Compare(base, current, Thresholds{unitNs: *nsLimit, unitAllocs: *allocsLimit, unitBytes: *bytesLimit})
	if len(report.Deltas) == 0 && len(report.Missing) == 0 {
		fmt.Fprintln(stderr, "benchgate: no benchmarks in common between baseline and current run")
		return 2
	}
	writeReport(stdout, report, base, current)
	regressions := report.Regressions()
	failed := len(regressions) > 0 || (len(report.Missing) > 0 && !*allowMissing)
	if failed {
		fmt.Fprintf(stdout, "\nFAIL: %d regression(s), %d missing benchmark(s)\n", len(regressions), len(report.Missing))
		for _, d := range regressions {
			fmt.Fprintf(stdout, "  regression %s %s: %s -> %s (%s)\n", d.Name, d.Unit, formatValue(d.Base), formatValue(d.Current), formatPercent(d.Percent))
		}
		if !*allowMissing {
			for _, name := range report.Missing {
				fmt.Fprintf(stdout, "  missing %s\n", name)
			}
		}
		return 1
	}
	fmt.Fprintln(stdout, "\nPASS: no benchmark regression beyond thresholds")
	return 0
}

func filter(s Samples, re *regexp.Regexp) Samples {
	out := Samples{}
	for name, units := range s {
		if re.MatchString(name) {
			out[name] = units
		}
	}
	return out
}

func formatValue(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func writeReport(w io.Writer, report Report, base, current Samples) {
	width := len("benchmark")
	for _, d := range report.Deltas {
		width = max(width, len(d.Name))
	}
	fmt.Fprintf(w, "%-*s %-9s %14s %14s %9s %s\n", width, "benchmark", "unit", "base(median)", "cur(median)", "delta", "n(base/cur)")
	for _, d := range report.Deltas {
		mark := ""
		if d.Regression {
			mark = "  REGRESSION"
		}
		fmt.Fprintf(w, "%-*s %-9s %14s %14s %9s %d/%d%s\n", width, d.Name, d.Unit, formatValue(d.Base), formatValue(d.Current), formatPercent(d.Percent),
			len(base[d.Name][d.Unit]), len(current[d.Name][d.Unit]), mark)
	}
	for _, name := range report.Missing {
		fmt.Fprintf(w, "missing from current run: %s\n", name)
	}
	for _, name := range report.Added {
		fmt.Fprintf(w, "new (no baseline, not gated): %s\n", name)
	}
}
