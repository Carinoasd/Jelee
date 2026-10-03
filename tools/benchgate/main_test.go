package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sampleOutput = `goos: linux
goarch: amd64
pkg: example.com/a
cpu: Example CPU
BenchmarkFoo-16    	  100	  1000 ns/op	  64 B/op	   2 allocs/op
BenchmarkFoo-16    	  100	  1200 ns/op	  64 B/op	   2 allocs/op
BenchmarkFoo-16    	  100	  1100 ns/op	  64 B/op	   2 allocs/op
BenchmarkSub/case-1-8	  10	  500 ns/op	  12.5 MB/s	  100.0 files/op	  0 B/op	  0 allocs/op
--- FAIL: something unrelated
BenchmarkBroken-16 	 notanumber	 1 ns/op
BenchmarkNoNs-16   	  10	  5 B/op
PASS
ok  	example.com/a	0.1s
pkg: example.com/b
BenchmarkFoo	  50	  2000 ns/op
`

func TestParseQualifiesStripsProcsAndCollectsRepeats(t *testing.T) {
	samples, err := Parse(strings.NewReader(sampleOutput))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/a.BenchmarkFoo", "example.com/a.BenchmarkSub/case-1", "example.com/b.BenchmarkFoo"}
	if got := sortedNames(samples); !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	foo := samples["example.com/a.BenchmarkFoo"]
	if !reflect.DeepEqual(foo[unitNs], []float64{1000, 1200, 1100}) || len(foo[unitAllocs]) != 3 || len(foo[unitBytes]) != 3 {
		t.Fatalf("foo samples %v", foo)
	}
	sub := samples["example.com/a.BenchmarkSub/case-1"]
	if sub["files/op"][0] != 100 || sub["MB/s"][0] != 12.5 || sub[unitAllocs][0] != 0 {
		t.Fatalf("custom metrics %v", sub)
	}
	if _, ok := samples["example.com/b.BenchmarkFoo"][unitBytes]; ok {
		t.Fatal("B/op invented for a run without -benchmem")
	}
}

func TestMedian(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
		ok   bool
	}{
		{nil, 0, false},
		{[]float64{7}, 7, true},
		{[]float64{3, 1, 2}, 2, true},
		{[]float64{4, 1, 3, 2}, 2.5, true},
		{[]float64{10, 10, 1000, 10, 10}, 10, true}, // outlier does not move it
	} {
		in := append([]float64(nil), tc.in...)
		got, ok := Median(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Median(%v) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
		if !reflect.DeepEqual(in, tc.in) {
			t.Errorf("Median reordered its input %v", tc.in)
		}
	}
}

func samplesOf(name string, ns, bytes, allocs []float64) Samples {
	units := map[string][]float64{unitNs: ns}
	if bytes != nil {
		units[unitBytes] = bytes
	}
	if allocs != nil {
		units[unitAllocs] = allocs
	}
	return Samples{name: units}
}

func defaultLimits() Thresholds {
	return Thresholds{unitNs: 15, unitAllocs: 10, unitBytes: -1}
}

func TestCompareThresholdsUseMedians(t *testing.T) {
	base := samplesOf("B", []float64{100, 100, 100}, []float64{1000}, []float64{10, 10, 10})
	cases := []struct {
		name    string
		current Samples
		want    map[string]bool // unit -> regression
	}{
		{"equal", samplesOf("B", []float64{100, 100, 100}, []float64{1000}, []float64{10, 10, 10}), map[string]bool{}},
		{"ns at limit passes", samplesOf("B", []float64{115, 115, 115}, []float64{1000}, []float64{10}), map[string]bool{}},
		{"ns over limit fails", samplesOf("B", []float64{116, 116, 116}, []float64{1000}, []float64{10}), map[string]bool{unitNs: true}},
		{"one slow sample ignored", samplesOf("B", []float64{100, 500, 101}, []float64{1000}, []float64{10}), map[string]bool{}},
		{"allocs over limit fails", samplesOf("B", []float64{90}, []float64{1000}, []float64{12}), map[string]bool{unitAllocs: true}},
		{"allocs at limit passes", samplesOf("B", []float64{90}, []float64{1000}, []float64{11}), map[string]bool{}},
		{"bytes not gated by default", samplesOf("B", []float64{100}, []float64{5000}, []float64{10}), map[string]bool{}},
		{"improvement passes", samplesOf("B", []float64{50}, []float64{10}, []float64{1}), map[string]bool{}},
		{"no benchmem skipped", samplesOf("B", []float64{100}, nil, nil), map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Compare(base, tc.current, defaultLimits())
			got := map[string]bool{}
			for _, d := range report.Regressions() {
				got[d.Unit] = true
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("regressions %v, want %v (%+v)", got, tc.want, report.Deltas)
			}
		})
	}
}

func TestCompareBytesGateAndZeroBaseline(t *testing.T) {
	base := samplesOf("B", []float64{100}, []float64{0}, []float64{0})
	current := samplesOf("B", []float64{100}, []float64{16}, []float64{1})
	report := Compare(base, current, Thresholds{unitNs: 15, unitAllocs: 10, unitBytes: 20})
	if len(report.Regressions()) != 2 {
		t.Fatalf("zero-baseline growth must regress: %+v", report.Deltas)
	}
	for _, d := range report.Regressions() {
		if !math.IsInf(d.Percent, 1) || formatPercent(d.Percent) != "+inf%" {
			t.Fatalf("zero baseline percent %v", d.Percent)
		}
	}
	report = Compare(base, base, Thresholds{unitNs: 15, unitAllocs: 10, unitBytes: 20})
	if len(report.Regressions()) != 0 {
		t.Fatal("zero to zero is not a regression")
	}
}

func TestCompareMissingAndAdded(t *testing.T) {
	base := Samples{"a.Old": {unitNs: {1}}, "a.Kept": {unitNs: {1}}}
	current := Samples{"a.Kept": {unitNs: {1}}, "a.New": {unitNs: {9}}}
	report := Compare(base, current, defaultLimits())
	if !reflect.DeepEqual(report.Missing, []string{"a.Old"}) || !reflect.DeepEqual(report.Added, []string{"a.New"}) || len(report.Regressions()) != 0 {
		t.Fatalf("report %+v", report)
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "base.txt", "pkg: p\nBenchmarkA-4 10 100 ns/op 8 B/op 1 allocs/op\nBenchmarkA-4 10 102 ns/op 8 B/op 1 allocs/op\nBenchmarkGone-4 10 5 ns/op\n")
	same := writeFile(t, dir, "same.txt", "pkg: p\nBenchmarkA-16 10 101 ns/op 8 B/op 1 allocs/op\nBenchmarkGone-16 10 5 ns/op\n")
	slow := writeFile(t, dir, "slow.txt", "pkg: p\nBenchmarkA-16 10 200 ns/op 8 B/op 1 allocs/op\nBenchmarkGone-16 10 5 ns/op\n")
	dropped := writeFile(t, dir, "dropped.txt", "pkg: p\nBenchmarkA-16 10 101 ns/op 8 B/op 1 allocs/op\n")
	empty := writeFile(t, dir, "empty.txt", "PASS\n")
	other := writeFile(t, dir, "other.txt", "pkg: q\nBenchmarkZ 10 1 ns/op\n")
	for _, tc := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"pass across procs suffix", []string{"-base", base, "-current", same}, 0, "PASS"},
		{"ns regression", []string{"-base", base, "-current", slow}, 1, "regression p.BenchmarkA ns/op"},
		{"ns gate disabled", []string{"-base", base, "-current", slow, "-ns", "-1"}, 0, "PASS"},
		{"raised threshold", []string{"-base", base, "-current", slow, "-ns", "150"}, 0, "PASS"},
		{"missing fails", []string{"-base", base, "-current", dropped}, 1, "missing p.BenchmarkGone"},
		{"missing allowed", []string{"-base", base, "-current", dropped, "-allow-missing"}, 0, "PASS"},
		{"match narrows", []string{"-base", base, "-current", dropped, "-match", `BenchmarkA$`}, 0, "PASS"},
		{"empty input", []string{"-base", base, "-current", empty}, 2, ""},
		{"nothing in common", []string{"-base", base, "-current", other, "-match", "BenchmarkZ"}, 2, ""},
		{"absent file", []string{"-base", filepath.Join(dir, "nope"), "-current", same}, 2, ""},
		{"usage", []string{"-base", base}, 2, ""},
		{"bad regexp", []string{"-base", base, "-current", same, "-match", "("}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout.String(), stderr.String())
			}
			if tc.out != "" && !strings.Contains(stdout.String(), tc.out) {
				t.Fatalf("output lacks %q:\n%s", tc.out, stdout.String())
			}
		})
	}
}
