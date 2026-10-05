package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ConsistencyRepository reads bounded pages for the consistency checker
// (G50.3) and persists its runs. Page reads never modify anything.
type ConsistencyRepository interface {
	// ConsistencyLibraries returns one library, or every library when id is
	// empty, with its roots and the baseline active now.
	ConsistencyLibraries(ctx context.Context, id string) ([]domain.ConsistencyLibrary, error)
	// ConsistencyPage returns the page of check after cursor. Global checks
	// ignore the library of scope.
	ConsistencyPage(ctx context.Context, check string, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error)
	// ConsistencyBaselineCurrent reports whether the baseline the library was
	// read with is still the active one.
	ConsistencyBaselineCurrent(ctx context.Context, library domain.ConsistencyLibrary) (bool, error)
	StartConsistencyRun(ctx context.Context, run domain.ConsistencyRun) (domain.ConsistencyRun, error)
	FinishConsistencyRun(ctx context.Context, report domain.ConsistencyReport) error
}

// ConsistencyFixer applies the safe repairs of fixable findings: it
// journals the exact values before and after each one and audits the batch.
// A repair whose row changed since it was read is skipped.
type ConsistencyFixer interface {
	ApplyConsistencyFixes(ctx context.Context, runID string, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error)
}

// ConsistencyFileProber reports whether a regular file exists below a
// library root. It reads metadata only and never follows a link out of the
// root.
type ConsistencyFileProber interface {
	FileExists(ctx context.Context, rootPath, relative string) (bool, error)
}

// ConsistencyVariantProber reports whether the image store holds the
// variant an index row names (Object is "<source hex>/<variant hex>").
type ConsistencyVariantProber interface {
	VariantExists(ctx context.Context, object string) (bool, error)
}

// ConsistencyPathRenderer turns a root and a root-relative path into the
// report form the logging path mode allows; it never returns an absolute
// path.
type ConsistencyPathRenderer interface {
	RenderPath(rootPath, relative string) string
}

// ConsistencyOptions selects one run.
type ConsistencyOptions struct {
	Origin string
	JobID  string
	// Library limits the run to one library; empty checks every library.
	Library string
	Fix     bool
	// StatBudget bounds filesystem probes of the whole run.
	StatBudget int
	// WatchSample bounds statistics rows recomputed per library and
	// direction; VariantSample bounds image variant index rows probed.
	WatchSample   int
	VariantSample int
	// SessionRetention and DailyRetention are the playback history and
	// statistics retention; zero keeps rows without limit.
	SessionRetention time.Duration
	DailyRetention   time.Duration
	// CallTimeout bounds every repository call; zero leaves calls to ctx.
	CallTimeout time.Duration
}

// ConsistencyChecker runs the checks. It holds no state between runs.
type ConsistencyChecker struct {
	repository ConsistencyRepository
	files      ConsistencyFileProber
	variants   ConsistencyVariantProber
	paths      ConsistencyPathRenderer
	fixer      ConsistencyFixer
	now        func() time.Time
}

// NewConsistencyChecker requires a repository, a file prober and a path
// renderer. Without a variant prober the image variant check is skipped;
// without a fixer a fix run is refused.
func NewConsistencyChecker(repository ConsistencyRepository, files ConsistencyFileProber, variants ConsistencyVariantProber, paths ConsistencyPathRenderer, fixer ConsistencyFixer) (*ConsistencyChecker, error) {
	if repository == nil || files == nil || paths == nil {
		return nil, domain.ErrInvalid
	}
	return &ConsistencyChecker{repository: repository, files: files, variants: variants, paths: paths, fixer: fixer, now: time.Now}, nil
}

func validConsistencyOptions(o ConsistencyOptions) bool {
	switch {
	case o.Origin != domain.ConsistencyOriginCLI && o.Origin != domain.ConsistencyOriginJob,
		o.Origin == domain.ConsistencyOriginJob && (!domain.ValidID(o.JobID) || o.Library == "" || o.Fix),
		o.Origin == domain.ConsistencyOriginCLI && o.JobID != "",
		o.Library != "" && !domain.ValidID(o.Library),
		o.StatBudget < 0 || o.StatBudget > domain.ConsistencyMaxStatBudget,
		o.WatchSample < 0 || o.WatchSample > domain.ConsistencyMaxWatchSample,
		o.VariantSample < 0 || o.VariantSample > domain.ConsistencyMaxWatchSample,
		o.SessionRetention < 0 || o.DailyRetention < 0 || o.CallTimeout < 0:
		return false
	}
	return true
}

// consistencyRun is the mutable state of one Run call.
type consistencyRun struct {
	c        *ConsistencyChecker
	options  ConsistencyOptions
	runID    string
	statLeft int
	samples  int
	roots    map[string]string
}

// Run checks the selected libraries and the database-wide state, persists
// the report and returns it. Findings never fail a run; a check that cannot
// finish is reported incomplete. A cancelled run still persists what it
// found, then returns the context error together with the report.
func (c *ConsistencyChecker) Run(ctx context.Context, options ConsistencyOptions) (domain.ConsistencyReport, error) {
	if ctx == nil || !validConsistencyOptions(options) || options.Fix && c.fixer == nil {
		return domain.ConsistencyReport{}, domain.ErrInvalid
	}
	if options.StatBudget == 0 {
		options.StatBudget = domain.ConsistencyDefaultStatBudget
	}
	if options.WatchSample == 0 {
		options.WatchSample = domain.ConsistencyDefaultWatchSample
	}
	if options.VariantSample == 0 {
		options.VariantSample = domain.ConsistencyDefaultVariantProbe
	}
	var libraries []domain.ConsistencyLibrary
	if err := c.call(ctx, options, func(call context.Context) error {
		var err error
		libraries, err = c.repository.ConsistencyLibraries(call, options.Library)
		return err
	}); err != nil {
		return domain.ConsistencyReport{}, err
	}
	if options.Library != "" && len(libraries) != 1 {
		return domain.ConsistencyReport{}, domain.ErrNotFound
	}
	mode := domain.ConsistencyModeReport
	if options.Fix {
		mode = domain.ConsistencyModeFix
	}
	var run domain.ConsistencyRun
	if err := c.call(ctx, options, func(call context.Context) error {
		var err error
		run, err = c.repository.StartConsistencyRun(call, domain.ConsistencyRun{JobID: options.JobID, LibraryID: options.Library, Origin: options.Origin, Mode: mode})
		return err
	}); err != nil {
		return domain.ConsistencyReport{}, err
	}
	state := &consistencyRun{c: c, options: options, runID: run.ID, statLeft: options.StatBudget, roots: map[string]string{}}
	checks := len(libraries)*domain.ConsistencyLibraryCheckCount + domain.ConsistencyGlobalCheckCount
	state.samples = max(1, min(domain.ConsistencySamplesPerCheck, 2000/checks))
	report := domain.ConsistencyReport{
		Schema: domain.ConsistencyReportSchema, RunID: run.ID, Origin: options.Origin, JobID: options.JobID, LibraryID: options.Library,
		Mode: mode, StartedAt: run.StartedAt.UTC(), Libraries: make([]domain.ConsistencyLibraryReport, 0, len(libraries)),
		Limits: domain.ConsistencyLimits{StatBudget: options.StatBudget, WatchSample: options.WatchSample, VariantSample: options.VariantSample, SamplesPerCheck: state.samples, PageSize: domain.ConsistencyPageSize},
	}
	for _, library := range libraries {
		for _, root := range library.Roots {
			state.roots[root.ID] = root.Path
		}
		report.Libraries = append(report.Libraries, state.library(ctx, library))
	}
	global := domain.ConsistencyLibrary{}
	for _, check := range domain.ConsistencyGlobalChecks() {
		report.Global = append(report.Global, state.check(ctx, check, global))
	}
	report.Limits.StatsUsed = options.StatBudget - state.statLeft
	report.State = domain.ConsistencyRunCompleted
	for _, results := range append([][]domain.ConsistencyCheckResult{report.Global}, libraryChecks(report.Libraries)...) {
		for _, r := range results {
			report.Totals.Findings += r.Findings
			report.Totals.Fixable += r.Fixable
			report.Totals.Fixed += r.Fixed
			if r.Status == domain.ConsistencyStatusIncomplete {
				report.State = domain.ConsistencyRunPartial
			}
		}
	}
	runErr := ctx.Err()
	if runErr != nil {
		report.State = domain.ConsistencyRunCancelled
	}
	report.FinishedAt = c.now().UTC()
	// The report is persisted even when the run was cancelled; that write
	// gets its own short bound instead of the cancelled context.
	persist := ctx
	if runErr != nil {
		var cancel context.CancelFunc
		persist, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
	}
	if err := c.call(persist, options, func(call context.Context) error { return c.repository.FinishConsistencyRun(call, report) }); err != nil {
		return report, errors.Join(runErr, err)
	}
	return report, runErr
}

func libraryChecks(libraries []domain.ConsistencyLibraryReport) [][]domain.ConsistencyCheckResult {
	out := make([][]domain.ConsistencyCheckResult, 0, len(libraries))
	for _, l := range libraries {
		out = append(out, l.Checks)
	}
	return out
}

func (c *ConsistencyChecker) call(ctx context.Context, options ConsistencyOptions, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.CallTimeout <= 0 {
		return fn(ctx)
	}
	call, cancel := context.WithTimeout(ctx, options.CallTimeout)
	defer cancel()
	return fn(call)
}

func (r *consistencyRun) library(ctx context.Context, library domain.ConsistencyLibrary) domain.ConsistencyLibraryReport {
	out := domain.ConsistencyLibraryReport{LibraryID: library.ID, Baseline: domain.ConsistencyBaseline{Available: library.Baseline, Revision: library.Revision, Entries: library.Entries}}
	for _, check := range domain.ConsistencyLibraryChecks() {
		out.Checks = append(out.Checks, r.check(ctx, check, library))
	}
	if !library.Baseline || ctx.Err() != nil {
		return out
	}
	// A baseline replaced while the run read it can make present files look
	// missing; the comparisons against it are void.
	current := false
	err := r.c.call(ctx, r.options, func(call context.Context) error {
		var err error
		current, err = r.c.repository.ConsistencyBaselineCurrent(call, library)
		return err
	})
	if err == nil && current {
		return out
	}
	for i, result := range out.Checks {
		if domain.ConsistencyBaselineCheck(result.Check) && result.Status != domain.ConsistencyStatusSkipped {
			out.Checks[i] = domain.ConsistencyCheckResult{Check: result.Check, Status: domain.ConsistencyStatusIncomplete, Reason: domain.ConsistencyReasonBaselineChanged, Examined: result.Examined, Samples: []domain.ConsistencyFinding{}}
			if err != nil {
				out.Checks[i].Reason = domain.ConsistencyReasonFailed
			}
		}
	}
	return out
}

func (r *consistencyRun) check(ctx context.Context, check string, library domain.ConsistencyLibrary) domain.ConsistencyCheckResult {
	result := domain.ConsistencyCheckResult{Check: check, Status: domain.ConsistencyStatusOK, Samples: []domain.ConsistencyFinding{}}
	switch {
	case ctx.Err() != nil:
		result.Status, result.Reason = domain.ConsistencyStatusIncomplete, domain.ConsistencyReasonCancelled
		return result
	case domain.ConsistencyBaselineCheck(check) && !library.Baseline:
		result.Status, result.Reason = domain.ConsistencyStatusSkipped, domain.ConsistencyReasonNoBaseline
		return result
	case check == domain.ConsistencyImageVariant && r.c.variants == nil:
		result.Status, result.Reason = domain.ConsistencyStatusSkipped, domain.ConsistencyReasonStoreDisabled
		return result
	}
	scope := domain.ConsistencyScope{Library: library, WatchSample: r.options.WatchSample, VariantSample: r.options.VariantSample}
	now := r.c.now().UTC()
	if r.options.SessionRetention > 0 {
		// A day's sessions all ended after the day began; two days of margin
		// cover the statistics time zone.
		scope.SessionCutoff = now.Add(-r.options.SessionRetention).Add(48 * time.Hour).Truncate(24 * time.Hour)
	}
	if r.options.DailyRetention > 0 {
		scope.DailyCutoff = now.Add(-r.options.DailyRetention).Add(48 * time.Hour).Truncate(24 * time.Hour)
	}
	cursor := ""
	for {
		var page domain.ConsistencyPage
		err := r.c.call(ctx, r.options, func(call context.Context) error {
			var err error
			page, err = r.c.repository.ConsistencyPage(call, check, scope, cursor)
			return err
		})
		if err != nil {
			result.Status, result.Reason = domain.ConsistencyStatusIncomplete, domain.ConsistencyReasonFailed
			if ctx.Err() != nil {
				result.Reason = domain.ConsistencyReasonCancelled
			}
			break
		}
		result.Examined += page.Examined
		addInfo(&result, page.Info)
		fixes := r.findings(ctx, &result, page.Candidates)
		if len(fixes) > 0 && r.options.Fix {
			var fixed domain.ConsistencyFixResult
			err = r.c.call(ctx, r.options, func(call context.Context) error {
				var err error
				fixed, err = r.c.fixer.ApplyConsistencyFixes(call, r.runID, fixes)
				return err
			})
			result.Fixed += fixed.Applied
			if err != nil {
				result.Status, result.Reason = domain.ConsistencyStatusIncomplete, domain.ConsistencyReasonFailed
				break
			}
		}
		if page.Next == "" {
			break
		}
		if page.Next == cursor {
			// A repository that does not advance would loop forever.
			result.Status, result.Reason = domain.ConsistencyStatusIncomplete, domain.ConsistencyReasonFailed
			break
		}
		cursor = page.Next
	}
	if result.Status == domain.ConsistencyStatusOK && result.Info[domain.ConsistencyInfoUnverified] > 0 {
		result.Status, result.Reason = domain.ConsistencyStatusIncomplete, domain.ConsistencyReasonUnverified
	}
	if result.Status == domain.ConsistencyStatusOK && result.Findings > 0 {
		result.Status = domain.ConsistencyStatusFindings
	}
	return result
}

func addInfo(result *domain.ConsistencyCheckResult, info map[string]int64) {
	for key, n := range info {
		if n == 0 {
			continue
		}
		if result.Info == nil {
			result.Info = map[string]int64{}
		}
		result.Info[key] += n
	}
}

// findings confirms the candidates of one page, records the findings and
// returns the fixable ones.
func (r *consistencyRun) findings(ctx context.Context, result *domain.ConsistencyCheckResult, candidates []domain.ConsistencyFinding) []domain.ConsistencyFinding {
	var fixes []domain.ConsistencyFinding
	for _, f := range candidates {
		switch f.Confirm {
		case domain.ConsistencyConfirmAbsent:
			root, known := r.roots[f.RootID]
			if !known || r.statLeft <= 0 || ctx.Err() != nil {
				f.Code = domain.ConsistencyUnconfirmedCode(f.Code)
				addInfo(result, map[string]int64{domain.ConsistencyInfoUnconfirmed: 1})
				break
			}
			r.statLeft--
			exists, err := r.c.files.FileExists(ctx, root, f.RelativePath)
			if err == nil && exists {
				addInfo(result, map[string]int64{domain.ConsistencyInfoBaselineStale: 1})
				continue
			}
			if err != nil {
				f.Code = domain.ConsistencyUnconfirmedCode(f.Code)
				addInfo(result, map[string]int64{domain.ConsistencyInfoUnconfirmed: 1})
			}
		case domain.ConsistencyConfirmVariant:
			exists, err := r.c.variants.VariantExists(ctx, f.Object)
			if err != nil {
				addInfo(result, map[string]int64{domain.ConsistencyInfoUnverified: 1})
				continue
			}
			if exists {
				continue
			}
		}
		result.Findings++
		if f.Fixable {
			result.Fixable++
			fixes = append(fixes, f)
		}
		if len(result.Samples) < r.samples {
			if f.RelativePath != "" {
				f.Path = r.c.paths.RenderPath(r.roots[f.RootID], f.RelativePath)
			}
			result.Samples = append(result.Samples, f)
		} else {
			result.SamplesTruncated = true
		}
	}
	return fixes
}

// ConsistencyExecutionRepository makes a consistency_check job terminal under
// its lease fence.
type ConsistencyExecutionRepository interface {
	FinishConsistencyCheck(ctx context.Context, lease domain.JobLease, state, code string) error
}

// ConsistencyScheduleRepository queues the periodic check of at most one
// library that was not checked within interval and has no active job.
type ConsistencyScheduleRepository interface {
	DispatchConsistencyCheck(ctx context.Context, interval time.Duration, policy domain.JobPolicy) (bool, error)
}

// consistencyDispatchSpacing bounds how often the scheduler loop asks the
// database for a library due for its periodic check.
const consistencyDispatchSpacing = time.Minute

type consistencySchedule struct {
	repository ConsistencyScheduleRepository
	interval   time.Duration
	now        func() time.Time
	mu         sync.Mutex
	last       time.Time
}

// NewJobsWithConsistencySchedule makes the schedule dispatcher also queue a
// background consistency_check job for each library every interval (G50.3,
// G13.6). Scan schedules keep priority: the check is only considered when no
// scan schedule was due.
func NewJobsWithConsistencySchedule(j *Jobs, repository ConsistencyScheduleRepository, interval time.Duration) (*Jobs, error) {
	if j == nil || j.schedules == nil || repository == nil || interval < time.Hour {
		return nil, domain.ErrInvalid
	}
	next := *j
	next.consistency = &consistencySchedule{repository: repository, interval: interval, now: time.Now}
	return &next, nil
}

func (c *consistencySchedule) dispatch(ctx context.Context, policy domain.JobPolicy) (bool, error) {
	c.mu.Lock()
	now := c.now()
	if !c.last.IsZero() && now.Sub(c.last) < consistencyDispatchSpacing {
		c.mu.Unlock()
		return false, nil
	}
	c.last = now
	c.mu.Unlock()
	return c.repository.DispatchConsistencyCheck(ctx, c.interval, policy)
}
