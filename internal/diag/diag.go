// Package diag implements jelee-cli doctor (G50.1) and the redacted
// diagnostic bundle (G50.2).
//
// Every check returns fixed codes and fixed remediation text. Values that
// could identify a deployment (credentials, connection strings, hostnames,
// absolute paths) are never placed in results: paths are named by their
// configuration key or by an opaque database identifier instead.
package diag

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Status is the outcome of one finding or check.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

func (s Status) rank() int {
	switch s {
	case StatusFail:
		return 2
	case StatusWarn:
		return 1
	}
	return 0
}

// Finding is one observation of a check. Subject is a configuration key or
// an opaque label such as "root:<uuid>", never a filesystem path.
type Finding struct {
	Subject string `json:"subject,omitempty"`
	Status  Status `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Detail is an optional fixed-vocabulary explanation, such as a
	// configuration validation message that never contains values.
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Result aggregates the findings of one check. Status and Code are those of
// the most severe finding (the first one on ties).
type Result struct {
	Check    string            `json:"check"`
	Status   Status            `json:"status"`
	Code     string            `json:"code"`
	Fix      string            `json:"fix,omitempty"`
	Findings []Finding         `json:"findings"`
	Facts    map[string]string `json:"facts,omitempty"`
}

// Check is implemented by every doctor check.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// Report is the full doctor output.
type Report struct {
	Format      int       `json:"format"`
	GeneratedAt time.Time `json:"generatedAt"`
	Status      Status    `json:"status"`
	Summary     Summary   `json:"summary"`
	Results     []Result  `json:"results"`
}

type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// ReportFormat versions the JSON shape of Report.
const ReportFormat = 1

// CheckTimeout bounds one check so a hung filesystem or database cannot
// stall the whole report.
const CheckTimeout = 20 * time.Second

// checkTimeout is CheckTimeout; tests shorten it.
var checkTimeout = CheckTimeout

// Run executes checks sequentially. A check that panics or overruns its
// deadline is reported as failed without stopping the others.
func Run(ctx context.Context, checks []Check, now func() time.Time) Report {
	if now == nil {
		now = time.Now
	}
	report := Report{Format: ReportFormat, GeneratedAt: now().UTC(), Status: StatusOK, Results: make([]Result, 0, len(checks))}
	for _, check := range checks {
		result := runOne(ctx, check)
		report.Results = append(report.Results, result)
		switch result.Status {
		case StatusFail:
			report.Summary.Fail++
		case StatusWarn:
			report.Summary.Warn++
		default:
			report.Summary.OK++
		}
		if result.Status.rank() > report.Status.rank() {
			report.Status = result.Status
		}
	}
	return report
}

func runOne(parent context.Context, check Check) (result Result) {
	name := check.Name()
	ctx, cancel := context.WithTimeout(parent, checkTimeout)
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- newResult(name, failf("", CodeCheckPanicked))
			}
		}()
		done <- check.Run(ctx)
	}()
	select {
	case result = <-done:
	case <-ctx.Done():
		// The goroutine may stay blocked in a kernel call; it only writes to
		// the buffered channel, so abandoning it is safe for a CLI process.
		result = newResult(name, failf("", CodeCheckTimeout))
	}
	result.Check = name
	return finish(result)
}

// newResult builds a result from findings and computes its summary fields.
func newResult(name string, findings ...Finding) Result {
	return finish(Result{Check: name, Findings: findings})
}

func finish(r Result) Result {
	r.Status, r.Code, r.Fix = StatusOK, CodeOK, ""
	if len(r.Findings) == 0 {
		r.Findings = []Finding{okf("", CodeOK)}
	}
	worst := -1
	for i, f := range r.Findings {
		if worst < 0 || f.Status.rank() > r.Findings[worst].Status.rank() {
			worst = i
		}
	}
	r.Status, r.Code, r.Fix = r.Findings[worst].Status, r.Findings[worst].Code, r.Findings[worst].Fix
	return r
}

func finding(status Status, subject, code string) Finding {
	info, ok := codes[code]
	if !ok {
		panic(fmt.Sprintf("diag: unregistered code %q", code))
	}
	f := Finding{Subject: subject, Status: status, Code: code, Message: info.Message}
	if status != StatusOK {
		f.Fix = info.Fix
	}
	return f
}

func okf(subject, code string) Finding   { return finding(StatusOK, subject, code) }
func warnf(subject, code string) Finding { return finding(StatusWarn, subject, code) }
func failf(subject, code string) Finding { return finding(StatusFail, subject, code) }

// Codes returns every registered code in sorted order; documentation tests
// use it to require a troubleshooting entry for each.
func Codes() []string {
	out := make([]string, 0, len(codes))
	for code := range codes {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// SelectChecks keeps the named checks in their original order. Unknown names
// are reported so the CLI can reject them.
func SelectChecks(checks []Check, names []string) ([]Check, []string) {
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	var selected []Check
	for _, c := range checks {
		if want[c.Name()] {
			selected = append(selected, c)
			delete(want, c.Name())
		}
	}
	unknown := make([]string, 0, len(want))
	for name := range want {
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return selected, unknown
}

// CheckFunc adapts a function into a Check.
type CheckFunc struct {
	CheckName string
	Fn        func(context.Context) Result
}

func (c CheckFunc) Name() string                   { return c.CheckName }
func (c CheckFunc) Run(ctx context.Context) Result { return c.Fn(ctx) }
