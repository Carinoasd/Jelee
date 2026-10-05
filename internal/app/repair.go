package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// RepairRepository reads the plans of the self-healing repair actions
// (G50.4) and applies them. Page reads never modify anything; every write
// re-checks the row it changes and audits its batch.
type RepairRepository interface {
	ConsistencyLibraries(ctx context.Context, id string) ([]domain.ConsistencyLibrary, error)
	ConsistencyPage(ctx context.Context, check string, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error)
	// RepairStatsPage reports every drifted daily row of the library on
	// stored days; RepairVariantPage pages the whole variant index.
	RepairStatsPage(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error)
	RepairVariantPage(ctx context.Context, cursor string) (domain.ConsistencyPage, error)
	RepairActiveJob(ctx context.Context, library string) (domain.RepairActiveJob, bool, error)
	StartRepairRun(ctx context.Context, run domain.RepairRun) (domain.RepairRun, error)
	ApplyRepairFixes(ctx context.Context, run domain.RepairRun, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error)
	PurgeRepairProbeCache(ctx context.Context, run domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error)
	PurgeRepairVariantRows(ctx context.Context, run domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error)
	EnqueueRepairCatalogSync(ctx context.Context, run domain.RepairRun, library string, policy domain.JobPolicy) (domain.RepairJob, error)
	FinishRepairRun(ctx context.Context, run domain.RepairRun, result domain.RepairResult) error
	RevertRepairRun(ctx context.Context, actor domain.Actor, runID string) (domain.RepairRevertResult, error)
}

// RepairScanSubmitter queues a scan that revalidates NFO files. Only the
// running server has the NFO reader identity a scan job is pinned to; *Jobs
// implements it.
type RepairScanSubmitter interface {
	SubmitScanOptions(ctx context.Context, actor domain.Actor, library, key, priority string, probe, nfo bool, ignore domain.IgnoreIntent) (domain.Job, bool, error)
}

// RepairVariantStore is the live image variant store of the running server.
// LiveVariants counts the variants of the live generation; ClearVariants
// makes all of them stale at once, so each is rendered again on its next
// request.
type RepairVariantStore interface {
	LiveVariants() (entries, bytes int64)
	ClearVariants(ctx context.Context) error
}

// RepairOptions selects one dry run or execution.
type RepairOptions struct {
	Action string
	// Library limits the action to one library; empty covers every library
	// (and, for orphans, the database-wide image variant index).
	Library string
	DryRun  bool
	Origin  string
	// Actor is the administrator of an API request; empty on the command
	// line, which acts with the host's database credentials.
	Actor domain.Actor
	// StatBudget bounds the file and variant probes of the orphans action.
	StatBudget int
	// SessionRetention is the playback history retention; the stats action
	// leaves days whose sessions may be gone alone. Zero keeps every day.
	SessionRetention time.Duration
	// Policy bounds the jobs the items action queues.
	Policy domain.JobPolicy
	// CallTimeout bounds every repository call; zero leaves calls to ctx.
	CallTimeout time.Duration
}

// Repairer runs the repair actions. It holds no state between runs.
type Repairer struct {
	repository RepairRepository
	files      ConsistencyFileProber
	variants   ConsistencyVariantProber
	paths      ConsistencyPathRenderer
	scans      RepairScanSubmitter
	store      RepairVariantStore
	now        func() time.Time
}

// NewRepairer requires a repository, a file prober and a path renderer.
// Without a variant prober the orphans action leaves the variant index
// alone. The server-only actions need WithServer.
func NewRepairer(repository RepairRepository, files ConsistencyFileProber, variants ConsistencyVariantProber, paths ConsistencyPathRenderer) (*Repairer, error) {
	if repository == nil || files == nil || paths == nil {
		return nil, domain.ErrInvalid
	}
	return &Repairer{repository: repository, files: files, variants: variants, paths: paths, now: time.Now}, nil
}

// WithServer returns a repairer that can also revalidate NFO files and clear
// the image variant store. Either may be nil when the feature is off.
func (r *Repairer) WithServer(scans RepairScanSubmitter, store RepairVariantStore) *Repairer {
	next := *r
	next.scans, next.store = scans, store
	return &next
}

func validRepairOptions(o RepairOptions) bool {
	switch {
	case !domain.ValidRepairAction(o.Action),
		o.Origin != domain.RepairOriginCLI && o.Origin != domain.RepairOriginAPI,
		o.Origin == domain.RepairOriginAPI && !validActor(o.Actor),
		o.Origin == domain.RepairOriginCLI && o.Actor != (domain.Actor{}),
		o.Library != "" && !domain.ValidID(o.Library),
		o.Action == domain.RepairImageVariants && o.Library != "",
		o.StatBudget < 0 || o.StatBudget > domain.ConsistencyMaxStatBudget,
		o.SessionRetention < 0 || o.CallTimeout < 0:
		return false
	}
	return true
}

// repairRun is the mutable state of one Run call.
type repairRun struct {
	r        *Repairer
	options  RepairOptions
	run      domain.RepairRun
	result   *domain.RepairResult
	targets  map[string]*domain.RepairTargetCount
	statLeft int
	roots    map[string]string
}

// Run plans the action and, unless it is a dry run, applies the plan. A dry
// run writes nothing. An execution records a repair run, audits every batch
// and the run, and applies exactly what a dry run at the same moment would
// list; objects that changed in between are skipped and counted. A failed
// execution persists what it did and returns the error with the result.
func (r *Repairer) Run(ctx context.Context, options RepairOptions) (domain.RepairResult, error) {
	if ctx == nil || !validRepairOptions(options) {
		return domain.RepairResult{}, domain.ErrInvalid
	}
	switch options.Action {
	case domain.RepairNFO:
		if r.scans == nil {
			return domain.RepairResult{}, errors.Join(domain.ErrRepairUnavailable, domain.ErrNFOReaderUnavailable)
		}
	case domain.RepairImageVariants:
		if r.store == nil {
			return domain.RepairResult{}, errors.Join(domain.ErrRepairUnavailable, domain.ErrImageUnavailable)
		}
	case domain.RepairItems:
		if !options.DryRun && options.Policy.QueueLimit < 1 {
			return domain.RepairResult{}, domain.ErrInvalid
		}
	}
	if options.StatBudget == 0 {
		options.StatBudget = domain.RepairDefaultStatBudget
	}
	var libraries []domain.ConsistencyLibrary
	if options.Action != domain.RepairImageVariants {
		if err := r.call(ctx, options, func(call context.Context) error {
			var err error
			libraries, err = r.repository.ConsistencyLibraries(call, options.Library)
			return err
		}); err != nil {
			return domain.RepairResult{}, err
		}
		if options.Library != "" && len(libraries) != 1 {
			return domain.RepairResult{}, domain.ErrNotFound
		}
	}
	state := &repairRun{r: r, options: options, targets: map[string]*domain.RepairTargetCount{}, statLeft: options.StatBudget, roots: map[string]string{}}
	result := domain.RepairResult{Schema: domain.RepairResultSchema, Action: options.Action, Origin: options.Origin, LibraryID: options.Library, DryRun: options.DryRun,
		State: domain.RepairStatePlanned, Revertible: domain.RepairRevertible(options.Action) && !options.DryRun, Targets: []domain.RepairTargetCount{}, Samples: []domain.RepairSample{},
		StartedAt: r.now().UTC()}
	state.result = &result
	if !options.DryRun {
		if err := r.call(ctx, options, func(call context.Context) error {
			var err error
			state.run, err = r.repository.StartRepairRun(call, domain.RepairRun{Action: options.Action, LibraryID: options.Library, Origin: options.Origin, Actor: options.Actor})
			return err
		}); err != nil {
			return domain.RepairResult{}, err
		}
		state.run.Actor = options.Actor
		result.RunID = state.run.ID
	}
	for _, library := range libraries {
		for _, root := range library.Roots {
			state.roots[root.ID] = root.Path
		}
	}
	runErr := state.execute(ctx, libraries)
	for _, kind := range repairTargetOrder(options.Action) {
		if t, ok := state.targets[kind]; ok {
			result.Targets = append(result.Targets, *t)
			result.Planned += t.Planned
			result.Applied += t.Applied
			result.Skipped += t.Skipped
		}
	}
	switch {
	case runErr != nil && result.Applied > 0:
		result.State, result.Reason = domain.RepairStatePartial, domain.RepairReasonFailed
	case runErr != nil:
		result.State, result.Reason = domain.RepairStateFailed, domain.RepairReasonFailed
	case !options.DryRun:
		result.State = domain.RepairStateCompleted
	}
	result.FinishedAt = r.now().UTC()
	if options.DryRun {
		return result, runErr
	}
	persist := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		persist, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
	}
	if err := r.call(persist, options, func(call context.Context) error { return r.repository.FinishRepairRun(call, state.run, result) }); err != nil {
		return result, errors.Join(runErr, err)
	}
	return result, runErr
}

// Revert undoes a stats or counts execution from its journal.
func (r *Repairer) Revert(ctx context.Context, actor domain.Actor, runID string) (domain.RepairRevertResult, error) {
	if ctx == nil || !domain.ValidID(runID) || actor != (domain.Actor{}) && !validActor(actor) {
		return domain.RepairRevertResult{}, domain.ErrInvalid
	}
	return r.repository.RevertRepairRun(ctx, actor, runID)
}

func repairTargetOrder(action string) []string {
	switch action {
	case domain.RepairItems:
		return []string{domain.RepairTargetCatalogVideo}
	case domain.RepairImageVariants:
		return []string{domain.RepairTargetImageVariant}
	case domain.RepairCaches:
		return []string{domain.RepairTargetProbeStale}
	case domain.RepairStats:
		return []string{domain.RepairTargetDailyCounters}
	case domain.RepairOrphans:
		return []string{domain.RepairTargetProbeOrphan, domain.RepairTargetVariantOrphan}
	case domain.RepairNFO:
		return []string{domain.RepairTargetNFOObservation}
	}
	return []string{domain.RepairTargetUserData, domain.RepairTargetSession}
}

func (r *Repairer) call(ctx context.Context, options RepairOptions, fn func(context.Context) error) error {
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

func (s *repairRun) call(ctx context.Context, fn func(context.Context) error) error {
	return s.r.call(ctx, s.options, fn)
}

func (s *repairRun) target(kind string) *domain.RepairTargetCount {
	t, ok := s.targets[kind]
	if !ok {
		t = &domain.RepairTargetCount{Kind: kind}
		s.targets[kind] = t
	}
	return t
}

func (s *repairRun) info(key string, n int64) {
	if n == 0 {
		return
	}
	if s.result.Info == nil {
		s.result.Info = map[string]int64{}
	}
	s.result.Info[key] += n
}

// sample records an affected object while the sample bound allows.
func (s *repairRun) sample(kind string, f domain.ConsistencyFinding) {
	if len(s.result.Samples) >= domain.RepairSamples {
		s.result.SamplesTruncated = true
		return
	}
	value := domain.RepairSample{Kind: kind, LibraryID: f.LibraryID, ItemID: f.ItemID, SourceID: f.SourceID, UserID: f.UserID, Day: f.Day, Object: f.Object}
	if f.RelativePath != "" {
		value.Path = s.r.paths.RenderPath(s.roots[f.RootID], f.RelativePath)
	}
	s.result.Samples = append(s.result.Samples, value)
}

func (s *repairRun) execute(ctx context.Context, libraries []domain.ConsistencyLibrary) error {
	switch s.options.Action {
	case domain.RepairImageVariants:
		return s.imageVariants(ctx)
	case domain.RepairOrphans:
		for _, l := range libraries {
			if err := s.libraryPass(ctx, l); err != nil {
				return err
			}
		}
		if s.options.Library != "" {
			return nil
		}
		return s.variantIndex(ctx)
	}
	for _, l := range libraries {
		if err := s.libraryPass(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

// libraryPass plans and applies the action for one library.
func (s *repairRun) libraryPass(ctx context.Context, library domain.ConsistencyLibrary) error {
	scope := domain.ConsistencyScope{Library: library}
	if s.options.SessionRetention > 0 {
		// A day's sessions all ended after the day began; two days of margin
		// cover the statistics time zone (as the consistency check does).
		scope.SessionCutoff = s.r.now().UTC().Add(-s.options.SessionRetention).Add(48 * time.Hour).Truncate(24 * time.Hour)
	}
	baselineAction := s.options.Action == domain.RepairItems || s.options.Action == domain.RepairCaches || s.options.Action == domain.RepairOrphans || s.options.Action == domain.RepairNFO
	if baselineAction && !library.Baseline {
		s.info(domain.RepairReasonNoBaseline, 1)
		if s.options.Library != "" {
			s.result.Reason = domain.RepairReasonNoBaseline
		}
		return nil
	}
	var fetch func(context.Context, string) (domain.ConsistencyPage, error)
	switch s.options.Action {
	case domain.RepairStats:
		fetch = func(call context.Context, cursor string) (domain.ConsistencyPage, error) {
			return s.r.repository.RepairStatsPage(call, scope, cursor)
		}
	default:
		check := map[string]string{domain.RepairItems: domain.ConsistencyOrphanFile, domain.RepairCaches: domain.ConsistencyProbeCache, domain.RepairOrphans: domain.ConsistencyProbeCache,
			domain.RepairNFO: domain.ConsistencyNFOState, domain.RepairCounts: domain.ConsistencyVersionCount}[s.options.Action]
		fetch = func(call context.Context, cursor string) (domain.ConsistencyPage, error) {
			return s.r.repository.ConsistencyPage(call, check, scope, cursor)
		}
	}
	var planned int64
	err := s.walk(ctx, fetch, func(page domain.ConsistencyPage) error {
		targets := s.choose(ctx, page)
		planned += int64(len(targets))
		if s.options.DryRun || len(targets) == 0 {
			return nil
		}
		return s.applyPage(ctx, targets)
	})
	if err != nil {
		return err
	}
	if planned == 0 || s.options.DryRun {
		return nil
	}
	switch s.options.Action {
	case domain.RepairItems:
		return s.queue(ctx, planned, domain.RepairTargetCatalogVideo, func(call context.Context) (domain.RepairJob, error) {
			return s.r.repository.EnqueueRepairCatalogSync(call, s.run, library.ID, s.options.Policy)
		})
	case domain.RepairNFO:
		return s.queue(ctx, planned, domain.RepairTargetNFOObservation, func(call context.Context) (domain.RepairJob, error) {
			return s.submitNFO(call, library.ID)
		})
	}
	return nil
}

// walk reads every page of a plan; a repository that does not advance is
// an error rather than an endless loop.
func (s *repairRun) walk(ctx context.Context, fetch func(context.Context, string) (domain.ConsistencyPage, error), handle func(domain.ConsistencyPage) error) error {
	cursor := ""
	for {
		var page domain.ConsistencyPage
		if err := s.call(ctx, func(call context.Context) error {
			var err error
			page, err = fetch(call, cursor)
			return err
		}); err != nil {
			return err
		}
		if err := handle(page); err != nil {
			return err
		}
		if page.Next == "" {
			return nil
		}
		if page.Next == cursor {
			return domain.ErrDatabase
		}
		cursor = page.Next
	}
}

// choose turns the candidates of one page into the action's targets,
// confirming file absence where the action needs it, and counts them.
func (s *repairRun) choose(ctx context.Context, page domain.ConsistencyPage) []domain.ConsistencyFinding {
	if n := page.Info[domain.ConsistencyInfoPending]; n > 0 && s.options.Action == domain.RepairItems {
		s.info(domain.ConsistencyInfoPending, n)
	}
	if n := page.Info[domain.ConsistencyInfoUnverified]; n > 0 {
		s.info(domain.ConsistencyInfoUnverified, n)
	}
	var targets []domain.ConsistencyFinding
	for _, f := range page.Candidates {
		kind := ""
		switch s.options.Action {
		case domain.RepairItems:
			if f.Code == domain.ConsistencyVideoNotCataloged {
				kind = domain.RepairTargetCatalogVideo
			}
		case domain.RepairCaches:
			if f.Code == domain.ConsistencyProbeCacheChanged {
				kind = domain.RepairTargetProbeStale
			}
		case domain.RepairOrphans:
			if f.Code == domain.ConsistencyProbeCacheOrphan && s.confirmAbsent(ctx, f) {
				kind = domain.RepairTargetProbeOrphan
			}
		case domain.RepairStats:
			if f.Code == domain.ConsistencyDailyCounterDrift {
				kind = domain.RepairTargetDailyCounters
			}
		case domain.RepairNFO:
			kind = domain.RepairTargetNFOObservation
		case domain.RepairCounts:
			switch f.Code {
			case domain.ConsistencyUserDataForeignSource:
				kind = domain.RepairTargetUserData
			case domain.ConsistencySessionForeignSource:
				kind = domain.RepairTargetSession
			default:
				// Structural problems need a person: merge, move or delete.
				s.info("manual", 1)
			}
		}
		if kind == "" {
			continue
		}
		s.target(kind).Planned++
		s.sample(kind, f)
		targets = append(targets, f)
	}
	return targets
}

// confirmAbsent stats a file the baseline lacks. Only a confirmed absence
// is a target; a present file means the baseline is stale, and a failed or
// unbudgeted probe leaves the row alone.
func (s *repairRun) confirmAbsent(ctx context.Context, f domain.ConsistencyFinding) bool {
	root, known := s.roots[f.RootID]
	if !known || s.statLeft <= 0 || ctx.Err() != nil {
		s.info(domain.ConsistencyInfoUnconfirmed, 1)
		s.result.Reason = domain.RepairReasonUnconfirmed
		return false
	}
	s.statLeft--
	exists, err := s.r.files.FileExists(ctx, root, f.RelativePath)
	switch {
	case err != nil:
		s.info(domain.ConsistencyInfoUnconfirmed, 1)
		s.result.Reason = domain.RepairReasonUnconfirmed
		return false
	case exists:
		s.info(domain.ConsistencyInfoBaselineStale, 1)
		return false
	}
	return true
}

// applyPage applies the targets of one page.
func (s *repairRun) applyPage(ctx context.Context, targets []domain.ConsistencyFinding) error {
	var apply func(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error)
	switch s.options.Action {
	case domain.RepairStats, domain.RepairCounts:
		apply = s.r.repository.ApplyRepairFixes
	case domain.RepairCaches, domain.RepairOrphans:
		apply = s.r.repository.PurgeRepairProbeCache
	default:
		// Job actions queue once per library after the whole plan is read.
		return nil
	}
	var fixed domain.ConsistencyFixResult
	err := s.call(ctx, func(call context.Context) error {
		var err error
		fixed, err = apply(call, s.run, targets)
		return err
	})
	if err != nil {
		return err
	}
	s.attribute(targets, fixed)
	return nil
}

// attribute adds a batch result to the target kinds of the batch. Every
// page a plan reads holds a single kind (the counts plan reads user data and
// sessions in separate phases), so the split is exact; a mixed batch would
// still keep exact totals.
func (s *repairRun) attribute(targets []domain.ConsistencyFinding, fixed domain.ConsistencyFixResult) {
	kinds := map[string]int64{}
	order := []string{}
	for _, f := range targets {
		kind := s.kindOf(f)
		if _, ok := kinds[kind]; !ok {
			order = append(order, kind)
		}
		kinds[kind]++
	}
	applied := fixed.Applied
	for _, kind := range order {
		n := min(applied, kinds[kind])
		s.target(kind).Applied += n
		s.target(kind).Skipped += kinds[kind] - n
		applied -= n
	}
}

func (s *repairRun) kindOf(f domain.ConsistencyFinding) string {
	switch f.Code {
	case domain.ConsistencyUserDataForeignSource:
		return domain.RepairTargetUserData
	case domain.ConsistencySessionForeignSource:
		return domain.RepairTargetSession
	case domain.ConsistencyDailyCounterDrift:
		return domain.RepairTargetDailyCounters
	case domain.ConsistencyProbeCacheChanged:
		return domain.RepairTargetProbeStale
	case domain.ConsistencyProbeCacheOrphan:
		return domain.RepairTargetProbeOrphan
	}
	return domain.RepairTargetVariantOrphan
}

// queue hands a library's planned targets to one job. A job of the same
// kind already active for the library is reported as a replay and applies
// nothing new.
func (s *repairRun) queue(ctx context.Context, planned int64, kind string, submit func(context.Context) (domain.RepairJob, error)) error {
	var job domain.RepairJob
	err := s.call(ctx, func(call context.Context) error {
		var err error
		job, err = submit(call)
		return err
	})
	if errors.Is(err, domain.ErrNFODisabled) {
		// The library does not read NFO files; its observations stay until
		// an administrator turns validation on.
		s.info("nfoDisabled", 1)
		s.target(kind).Skipped += planned
		return nil
	}
	if err != nil {
		return err
	}
	s.result.Jobs = append(s.result.Jobs, job)
	if job.Replayed {
		s.target(kind).Skipped += planned
	} else {
		s.target(kind).Applied += planned
	}
	return nil
}

// submitNFO queues a scan with NFO validation unless the library already
// runs one.
func (s *repairRun) submitNFO(ctx context.Context, library string) (domain.RepairJob, error) {
	active, found, err := s.r.repository.RepairActiveJob(ctx, library)
	if err != nil {
		return domain.RepairJob{}, err
	}
	if found {
		if active.Kind == "inventory_scan" && active.NFO {
			return domain.RepairJob{LibraryID: library, JobID: active.ID, Replayed: true}, nil
		}
		return domain.RepairJob{}, domain.ErrJobBusy
	}
	job, replayed, err := s.r.scans.SubmitScanOptions(ctx, s.options.Actor, library, "repair-nfo:"+s.run.ID+":"+library, domain.JobPriorityManual, false, true, domain.IgnoreIntent{})
	if err != nil {
		return domain.RepairJob{}, err
	}
	return domain.RepairJob{LibraryID: library, JobID: job.ID, Replayed: replayed}, nil
}

// variantIndex removes index rows whose variant file is missing from the
// live generation. Each probe counts against the stat budget.
func (s *repairRun) variantIndex(ctx context.Context) error {
	if s.r.variants == nil {
		s.info(domain.RepairReasonStoreDisabled, 1)
		return nil
	}
	var pending []domain.ConsistencyFinding
	flush := func() error {
		if s.options.DryRun || len(pending) == 0 {
			pending = pending[:0]
			return nil
		}
		var fixed domain.ConsistencyFixResult
		if err := s.call(ctx, func(call context.Context) error {
			var err error
			fixed, err = s.r.repository.PurgeRepairVariantRows(call, s.run, pending)
			return err
		}); err != nil {
			return err
		}
		t := s.target(domain.RepairTargetVariantOrphan)
		t.Applied += fixed.Applied
		t.Skipped += fixed.Skipped
		pending = pending[:0]
		return nil
	}
	err := s.walk(ctx, s.r.repository.RepairVariantPage, func(page domain.ConsistencyPage) error {
		for _, f := range page.Candidates {
			if s.statLeft <= 0 || ctx.Err() != nil {
				s.info(domain.ConsistencyInfoUnconfirmed, 1)
				s.result.Reason = domain.RepairReasonUnconfirmed
				continue
			}
			s.statLeft--
			exists, err := s.r.variants.VariantExists(ctx, f.Object)
			if err != nil {
				s.info(domain.ConsistencyInfoUnverified, 1)
				continue
			}
			if exists {
				continue
			}
			s.target(domain.RepairTargetVariantOrphan).Planned++
			s.sample(domain.RepairTargetVariantOrphan, f)
			pending = append(pending, f)
		}
		return flush()
	})
	if err != nil {
		return err
	}
	return flush()
}

// imageVariants clears the live variant generation when it holds any.
func (s *repairRun) imageVariants(ctx context.Context) error {
	entries, bytes := s.r.store.LiveVariants()
	t := s.target(domain.RepairTargetImageVariant)
	t.Planned = entries
	s.info("bytes", bytes)
	if s.options.DryRun || entries == 0 {
		return nil
	}
	if err := s.r.store.ClearVariants(ctx); err != nil {
		return err
	}
	t.Applied = entries
	return nil
}
