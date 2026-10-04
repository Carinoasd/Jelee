package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Clock is the time source of the progress buffer, injectable for tests.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ProgressRepository stores playback sessions, their samples and user data
// (G23.1, G23.2, G23.4, G20.4). Every read of user data applies the user's
// library grants in the same statement, so an item the user can no longer
// see never appears in user data or the continue watching list (G48.3).
type ProgressRepository interface {
	// StartPlayback opens a session, or rejoins the stored session of the
	// same user and play key when it is active or timed out and names the
	// same item. The actor's live native session and the item's visibility
	// are checked in the same statement: a web session, an invisible item
	// and a missing item are ErrNotFound. A stored session that was stopped,
	// failed or names another item is ErrConflict.
	StartPlayback(ctx context.Context, start domain.PlaybackStart) (domain.PlaybackSessionRecord, error)
	// FlushPlayback writes a batch of buffered session states in one
	// statement. Entries whose stored session is no longer active (ended,
	// cleared, deleted user) change nothing.
	FlushPlayback(ctx context.Context, entries []domain.PlaybackFlush) (domain.PlaybackFlushResult, error)
	// ListStalePlayback returns at most limit active sessions whose last
	// report is older than before.
	ListStalePlayback(ctx context.Context, before time.Time, limit int) ([]domain.PlaybackSessionRecord, error)
	// PurgePlaybackHistory deletes at most limit ended sessions (and their
	// samples) that ended before cutoff, returning how many it deleted.
	PurgePlaybackHistory(ctx context.Context, cutoff time.Time, limit int) (int, error)
	// UserItemData returns the user data of every requested item the user can
	// see; visible items without data get zero values, invisible and missing
	// items are absent.
	UserItemData(ctx context.Context, userID string, itemIDs []string) (map[string]domain.UserItemData, error)
	// SetPlayed marks a visible item played (one more play, no resume point)
	// or unplayed (no plays, no resume point). Invisible: ErrNotFound.
	SetPlayed(ctx context.Context, userID, itemID string, played bool, at time.Time) (domain.UserItemData, error)
	// ListResume pages the visible items with a resume point that are not
	// played, most recently played first.
	ListResume(ctx context.Context, userID string, query domain.ResumeQuery) (domain.ResumePage, error)
	// ClearPlaybackHistory deletes every session, sample and user data row of
	// the actor's own user and records the audit event.
	ClearPlaybackHistory(ctx context.Context, actor domain.Actor) error
	// ListActivePlayback lists active sessions for an administrator actor;
	// anyone else gets ErrForbidden.
	ListActivePlayback(ctx context.Context, actor domain.Actor, limit int) ([]domain.ActivePlayback, error)
}

// ProgressOptions configures the progress buffer. Zero durations and
// bounds take the defaults of config.DefaultPlaybackConfig.
type ProgressOptions struct {
	Clock          Clock
	FlushInterval  time.Duration
	SessionTimeout time.Duration
	ReportInterval time.Duration
	SampleInterval time.Duration
	// Retention is zero to keep ended sessions until users clear them.
	Retention   time.Duration
	MaxBatch    int
	MaxSessions int
	Rules       domain.WatchStatsRules
	Logger      *slog.Logger
	// OnEnded is called (without blocking) after sessions ended in storage,
	// so the statistics roll-up can aggregate them soon (G23.5).
	OnEnded func()
}

const (
	// maxPendingSamples bounds the samples one session buffers between
	// flushes; maxSessionSamples bounds what one session ever stores.
	maxPendingSamples = 64
	maxSessionSamples = 4096
	// orphanSweepFlushes and purgeEvery space the storage sweeps.
	orphanSweepFlushes = 6
	purgeEvery         = time.Hour
	purgeBatch         = 1000
)

// ProgressStats are cumulative counters for diagnostics and the write
// amplification evidence of G23.2.
type ProgressStats struct {
	Reports   int64
	Coalesced int64
	Flushes   int64
	// FlushStatements counts every FlushPlayback statement: periodic
	// flushes, stops and timeouts. StartWrites counts session opens.
	FlushStatements int64
	SessionRows     int64
	UserDataRows    int64
	SampleRows      int64
	StartWrites     int64
	StopWrites      int64
	TimedOut        int64
}

type progressCounters struct {
	reports, coalesced, flushes, statements, sessionRows, userDataRows, sampleRows, startWrites, stopWrites, timedOut atomic.Int64
}

// liveSession is the buffered state of one session on this instance.
type liveSession struct {
	id, userID, itemID, sourceID string
	runtime                      int64
	position                     int64
	paused                       bool
	lastReport                   time.Time
	reports                      int
	dirty                        bool
	nextSeq                      int
	pending                      []domain.PlaybackSample
	lastKept                     *domain.PlaybackSample
	lastReceived                 *domain.PlaybackSample
	lastReceivedKept             bool
	// retryEnd is an ending whose write failed; the next flush retries it.
	retryEnd *domain.PlaybackFlush
}

// Progress buffers playback reports and writes them in batches (G23.2):
// every report updates the in-memory state of its session, repeated reports
// of a session between two flushes collapse into its latest state, and one
// statement per flush writes every dirty session together with its user
// data and the samples kept for statistics. Stops are written at once.
type Progress struct {
	repo    ProgressRepository
	sources PlaybackRepository
	opts    ProgressOptions

	mu       sync.Mutex
	sessions map[string]*liveSession // user ID + "\x00" + play key
	dirty    int

	flushMu    sync.Mutex
	flushCount int
	lastPurge  time.Time

	wake  chan struct{}
	stats progressCounters
}

// NewProgress builds the progress buffer. sources authorizes a start like
// playback information does (live native session, visible item) and gives
// the chosen version's runtime.
func NewProgress(repo ProgressRepository, sources PlaybackRepository, opts ProgressOptions) (*Progress, error) {
	if repo == nil || sources == nil {
		return nil, domain.ErrInvalid
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.FlushInterval == 0 {
		opts.FlushInterval = 10 * time.Second
	}
	if opts.SessionTimeout == 0 {
		opts.SessionTimeout = 5 * time.Minute
	}
	if opts.ReportInterval == 0 {
		opts.ReportInterval = 10 * time.Second
	}
	if opts.SampleInterval == 0 {
		opts.SampleInterval = time.Minute
	}
	if opts.MaxBatch == 0 {
		opts.MaxBatch = 500
	}
	if opts.MaxSessions == 0 {
		opts.MaxSessions = 10000
	}
	if opts.Rules == (domain.WatchStatsRules{}) {
		opts.Rules = domain.DefaultWatchStatsRules()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.FlushInterval < 0 || opts.SessionTimeout < 3*opts.FlushInterval || opts.ReportInterval < 0 || opts.SampleInterval <= 0 ||
		opts.Retention < 0 || opts.MaxBatch < 1 || opts.MaxSessions < 1 || opts.Rules.Validate() != nil {
		return nil, domain.ErrInvalid
	}
	return &Progress{repo: repo, sources: sources, opts: opts, sessions: map[string]*liveSession{}, wake: make(chan struct{}, 1)}, nil
}

// ReportInterval is the progress report interval clients should keep.
func (p *Progress) ReportInterval() time.Duration { return p.opts.ReportInterval }

// Stats returns the cumulative counters.
func (p *Progress) Stats() ProgressStats {
	c := &p.stats
	return ProgressStats{Reports: c.reports.Load(), Coalesced: c.coalesced.Load(), Flushes: c.flushes.Load(), FlushStatements: c.statements.Load(),
		SessionRows: c.sessionRows.Load(), UserDataRows: c.userDataRows.Load(), SampleRows: c.sampleRows.Load(),
		StartWrites: c.startWrites.Load(), StopWrites: c.stopWrites.Load(), TimedOut: c.timedOut.Load()}
}

func liveKey(userID, playKey string) string { return userID + "\x00" + playKey }

// Report applies one client report. Only native sessions may report; the
// caller has authenticated actor. A start or the first report of an unknown
// play key authorizes the item and opens or rejoins the stored session (one
// write); later progress and pings only touch memory; a stop is written at
// once. A repeated stop of an ended session is accepted and changes nothing.
func (p *Progress) Report(ctx context.Context, actor domain.Actor, report domain.PlaybackReport) error {
	if p == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrNotFound
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.stats.reports.Add(1)
	key := liveKey(actor.UserID, report.PlayKey)
	p.mu.Lock()
	s := p.sessions[key]
	if s != nil && report.ItemID != "" && s.itemID != report.ItemID {
		p.mu.Unlock()
		return domain.ErrConflict
	}
	if s == nil {
		full := len(p.sessions) >= p.opts.MaxSessions
		p.mu.Unlock()
		if full {
			return domain.ErrPlaybackBusy
		}
		if report.Kind == domain.PlaybackReportPing {
			// A ping of a session this instance does not hold has nothing
			// to keep alive here.
			return nil
		}
		if report.ItemID == "" {
			if report.Kind == domain.PlaybackReportStop {
				// Stop is idempotent: a session this instance does not
				// hold either ended already or ends by its timeout.
				return nil
			}
			return domain.ErrNotFound
		}
		opened, created, err := p.open(ctx, actor, report)
		if errors.Is(err, domain.ErrConflict) && report.Kind == domain.PlaybackReportStop {
			// Stop is idempotent: the session already ended.
			return nil
		}
		if err != nil {
			return err
		}
		p.mu.Lock()
		if existing := p.sessions[key]; existing != nil {
			// A concurrent report opened it first; both rejoined one row.
			s = existing
		} else {
			if len(p.sessions) >= p.opts.MaxSessions {
				p.mu.Unlock()
				return domain.ErrPlaybackBusy
			}
			s = opened
			p.sessions[key] = s
			if created {
				p.keepSample(s, domain.PlaybackSample{At: s.lastReport, Kind: domain.WatchSampleStart, PositionTicks: s.position, Paused: s.paused}, true)
				if report.Kind == domain.PlaybackReportStart {
					p.mu.Unlock()
					return nil
				}
			}
		}
	}
	now := p.opts.Clock.Now()
	switch report.Kind {
	case domain.PlaybackReportPing:
		s.lastReport = now
		p.markDirty(s)
		p.mu.Unlock()
		return nil
	case domain.PlaybackReportStop:
		p.apply(s, report, now)
		entry := p.endEntry(s, now, report)
		delete(p.sessions, key)
		p.mu.Unlock()
		p.flushMu.Lock()
		defer p.flushMu.Unlock()
		_, err := p.write(ctx, []domain.PlaybackFlush{entry})
		if err != nil {
			// Keep the state so the next flush or a retried stop writes it.
			p.restore(key, s, entry)
			return err
		}
		p.stats.stopWrites.Add(1)
		p.ended()
		return nil
	default:
		if s.dirty {
			// Overwrites state no flush has written yet.
			p.stats.coalesced.Add(1)
		}
		p.apply(s, report, now)
		p.markDirty(s)
		full := p.dirty >= p.opts.MaxBatch
		p.mu.Unlock()
		if full {
			select {
			case p.wake <- struct{}{}:
			default:
			}
		}
		return nil
	}
}

// open authorizes the item with the caller's session, picks the version and
// opens or rejoins the stored session. created reports a new session.
func (p *Progress) open(ctx context.Context, actor domain.Actor, report domain.PlaybackReport) (*liveSession, bool, error) {
	records, err := p.sources.ListPlaybackSources(ctx, actor, report.ItemID)
	if err != nil {
		return nil, false, err
	}
	var source *domain.PlaybackSource
	for _, record := range records {
		candidate := domain.BuildPlaybackSource(record)
		if report.SourceID != "" && candidate.ID != report.SourceID {
			continue
		}
		if source == nil || report.SourceID == "" && (candidate.Version.QualityScore > source.Version.QualityScore ||
			candidate.Version.QualityScore == source.Version.QualityScore && candidate.ID < source.ID) {
			c := candidate
			source = &c
		}
	}
	if report.SourceID != "" && source == nil {
		// A version that does not belong to the item is answered like an
		// invisible item.
		return nil, false, domain.ErrNotFound
	}
	start := domain.PlaybackStart{Actor: actor, PlayKey: report.PlayKey, ItemID: report.ItemID, At: p.opts.Clock.Now(), Paused: report.Paused}
	if report.PositionKnown {
		start.PositionTicks, start.PositionKnown = report.PositionTicks, true
	}
	if source != nil {
		start.SourceID = source.ID
		if source.DurationMicros != nil && *source.DurationMicros > 0 && *source.DurationMicros <= domain.PlaybackPositionMax/10 {
			start.RuntimeTicks = *source.DurationMicros * 10
		}
	}
	record, err := p.repo.StartPlayback(ctx, start)
	if err != nil {
		return nil, false, err
	}
	p.stats.startWrites.Add(1)
	s := &liveSession{id: record.ID, userID: actor.UserID, itemID: report.ItemID, sourceID: record.SourceID, runtime: record.RuntimeTicks,
		position: record.PositionTicks, paused: record.Paused, lastReport: start.At, nextSeq: record.SampleCount}
	created := record.Created
	if !created {
		// Rejoined: the report's position wins over the stored one.
		if report.PositionKnown {
			s.position = report.PositionTicks
		}
		s.paused = report.Paused
	}
	return s, created, nil
}

// apply updates a session with a report and keeps the samples statistics
// need. Callers hold p.mu.
func (p *Progress) apply(s *liveSession, report domain.PlaybackReport, now time.Time) {
	if report.PositionKnown {
		s.position = report.PositionTicks
	}
	kind := domain.WatchSampleProgress
	switch {
	case report.Kind == domain.PlaybackReportStop && report.Failed:
		kind = domain.WatchSampleFail
	case report.Kind == domain.PlaybackReportStop:
		kind = domain.WatchSampleStop
	case report.Paused && !s.paused:
		kind = domain.WatchSamplePause
	case !report.Paused && s.paused:
		kind = domain.WatchSampleResume
	}
	if report.Kind != domain.PlaybackReportStop {
		s.paused = report.Paused
	}
	s.lastReport = now
	s.reports++
	p.keepSample(s, domain.PlaybackSample{At: now, Kind: kind, PositionTicks: s.position, Paused: s.paused}, false)
}

// keepSample decides whether a sample is stored (G23.3 needs the report
// stream, not every report): state changes always, progress at most once
// per SampleInterval, and both sides of a discontinuity (seek, jump) so the
// statistics rules still see it. Callers hold p.mu.
func (p *Progress) keepSample(s *liveSession, sample domain.PlaybackSample, force bool) {
	previous := s.lastReceived
	keep := force || sample.Kind != domain.WatchSampleProgress || s.lastKept == nil || sample.At.Sub(s.lastKept.At) >= p.opts.SampleInterval
	if previous != nil && p.discontinuity(*previous, sample) {
		if !s.lastReceivedKept {
			p.store(s, *previous)
		}
		keep = true
	}
	s.lastReceived = &sample
	s.lastReceivedKept = keep && p.store(s, sample)
}

func (p *Progress) store(s *liveSession, sample domain.PlaybackSample) bool {
	if s.nextSeq >= maxSessionSamples || len(s.pending) >= maxPendingSamples && sample.Kind == domain.WatchSampleProgress {
		return false
	}
	if len(s.pending) >= maxPendingSamples {
		// State changes displace the oldest buffered progress sample.
		i := slices.IndexFunc(s.pending, func(x domain.PlaybackSample) bool { return x.Kind == domain.WatchSampleProgress })
		if i < 0 {
			return false
		}
		s.pending = slices.Delete(s.pending, i, i+1)
	}
	sample.Seq = s.nextSeq
	s.nextSeq++
	s.pending = append(s.pending, sample)
	kept := sample
	s.lastKept = &kept
	return true
}

// discontinuity applies the jump rule of ComputeWatchSession between two
// consecutive reports: a position change the elapsed time cannot explain.
func (p *Progress) discontinuity(previous, next domain.PlaybackSample) bool {
	wall := next.At.Sub(previous.At)
	advance := domain.TicksToDuration(next.PositionTicks - previous.PositionTicks)
	tol := p.opts.Rules.PositionTolerance
	if previous.Paused {
		return advance < -tol || advance > tol
	}
	return advance < -tol || float64(advance) > float64(wall)*p.opts.Rules.MaxPlaybackRate+float64(tol)
}

func (p *Progress) markDirty(s *liveSession) {
	if !s.dirty {
		s.dirty = true
		p.dirty++
	}
}

// entry snapshots a session for a flush and clears its buffer. Callers hold
// p.mu.
func (p *Progress) entry(s *liveSession) domain.PlaybackFlush {
	e := domain.PlaybackFlush{SessionID: s.id, PositionTicks: s.position, Paused: s.paused, LastReportAt: s.lastReport, Reports: s.reports,
		ResumeTicks: domain.ProgressResume(s.position, s.runtime, p.opts.Rules), SourceID: s.sourceID, Samples: s.pending}
	s.pending, s.reports = nil, 0
	if s.dirty {
		s.dirty = false
		p.dirty--
	}
	return e
}

// endEntry snapshots a session that ends now.
func (p *Progress) endEntry(s *liveSession, at time.Time, report domain.PlaybackReport) domain.PlaybackFlush {
	e := p.entry(s)
	e.ResumeTicks, e.Completed = domain.ResolvePlaybackEnd(s.position, s.runtime, p.opts.Rules)
	e.End = &domain.PlaybackEnding{State: domain.PlaybackStopped, At: at}
	if report.Failed {
		e.End.State, e.End.FailureReason = domain.PlaybackFailed, report.FailureReason
		if e.End.FailureReason == "" {
			e.End.FailureReason = domain.PlaybackFailureError
		}
		// A failed playback does not count as watched.
		e.ResumeTicks, e.Completed = domain.ProgressResume(s.position, s.runtime, p.opts.Rules), false
	}
	return e
}

// restore puts an unwritten entry back so the next flush retries it.
func (p *Progress) restore(key string, s *liveSession, e domain.PlaybackFlush) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.sessions[key]
	if e.End != nil {
		if current == nil {
			current = s
			p.sessions[key] = s
		}
		retry := e
		current.retryEnd = &retry
		p.markDirty(current)
		return
	}
	if current == nil {
		return
	}
	current.pending = append(e.Samples, current.pending...)
	current.reports += e.Reports
	p.markDirty(current)
}

func (p *Progress) write(ctx context.Context, entries []domain.PlaybackFlush) (domain.PlaybackFlushResult, error) {
	var total domain.PlaybackFlushResult
	for len(entries) > 0 {
		n := min(len(entries), p.opts.MaxBatch)
		result, err := p.repo.FlushPlayback(ctx, entries[:n])
		if err != nil {
			return total, err
		}
		total.Statements += result.Statements
		total.Sessions += result.Sessions
		total.UserData += result.UserData
		total.Samples += result.Samples
		entries = entries[n:]
	}
	p.stats.statements.Add(int64(total.Statements))
	p.stats.sessionRows.Add(int64(total.Sessions))
	p.stats.userDataRows.Add(int64(total.UserData))
	p.stats.sampleRows.Add(int64(total.Samples))
	return total, nil
}

// Flush writes every dirty session and closes the sessions that stopped
// reporting for SessionTimeout. Run calls it every FlushInterval and when
// MaxBatch sessions are dirty; tests call it with a fake clock.
func (p *Progress) Flush(ctx context.Context) (domain.PlaybackFlushResult, error) {
	if p == nil || ctx == nil {
		return domain.PlaybackFlushResult{}, domain.ErrInvalid
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	now := p.opts.Clock.Now()
	p.mu.Lock()
	entries := make([]domain.PlaybackFlush, 0, p.dirty)
	keys := make([]string, 0, p.dirty)
	sessions := make([]*liveSession, 0, p.dirty)
	for key, s := range p.sessions {
		switch {
		case s.retryEnd != nil:
			e := *s.retryEnd
			s.retryEnd = nil
			if s.dirty {
				s.dirty = false
				p.dirty--
			}
			delete(p.sessions, key)
			entries, keys, sessions = append(entries, e), append(keys, key), append(sessions, s)
		case now.Sub(s.lastReport) >= p.opts.SessionTimeout:
			e := p.entry(s)
			e.ResumeTicks, e.Completed = domain.ResolvePlaybackEnd(s.position, s.runtime, p.opts.Rules)
			e.End = &domain.PlaybackEnding{State: domain.PlaybackTimedOut, At: s.lastReport}
			delete(p.sessions, key)
			p.stats.timedOut.Add(1)
			entries, keys, sessions = append(entries, e), append(keys, key), append(sessions, s)
		case s.dirty:
			entries, keys, sessions = append(entries, p.entry(s)), append(keys, key), append(sessions, s)
		}
	}
	p.mu.Unlock()
	if len(entries) == 0 {
		return domain.PlaybackFlushResult{}, nil
	}
	result, err := p.write(ctx, entries)
	if err != nil {
		for i := range entries {
			p.restore(keys[i], sessions[i], entries[i])
		}
		p.opts.Logger.Warn("playback progress flush failed; retrying next interval", "component", "playback", "sessions", len(entries))
		return result, err
	}
	p.stats.flushes.Add(1)
	for _, e := range entries {
		if e.End != nil {
			p.ended()
			break
		}
	}
	return result, nil
}

func (p *Progress) ended() {
	if p.opts.OnEnded != nil {
		p.opts.OnEnded()
	}
}

// Maintain closes stored sessions no instance reports for (a restart or a
// crashed instance) and deletes history past the retention period.
func (p *Progress) Maintain(ctx context.Context) error {
	if p == nil || ctx == nil {
		return domain.ErrInvalid
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	now := p.opts.Clock.Now()
	stale, err := p.repo.ListStalePlayback(ctx, now.Add(-p.opts.SessionTimeout), p.opts.MaxBatch)
	if err != nil {
		return err
	}
	p.mu.Lock()
	held := make(map[string]bool, len(p.sessions))
	for _, s := range p.sessions {
		held[s.id] = true
	}
	p.mu.Unlock()
	var entries []domain.PlaybackFlush
	for _, r := range stale {
		if held[r.ID] {
			continue
		}
		e := domain.PlaybackFlush{SessionID: r.ID, PositionTicks: r.PositionTicks, Paused: r.Paused, LastReportAt: r.LastReportAt, SourceID: r.SourceID,
			End: &domain.PlaybackEnding{State: domain.PlaybackTimedOut, At: r.LastReportAt}}
		e.ResumeTicks, e.Completed = domain.ResolvePlaybackEnd(r.PositionTicks, r.RuntimeTicks, p.opts.Rules)
		entries = append(entries, e)
	}
	if len(entries) > 0 {
		if _, err := p.write(ctx, entries); err != nil {
			return err
		}
		p.stats.timedOut.Add(int64(len(entries)))
		p.ended()
	}
	if p.opts.Retention > 0 && now.Sub(p.lastPurge) >= purgeEvery {
		for {
			n, err := p.repo.PurgePlaybackHistory(ctx, now.Add(-p.opts.Retention), purgeBatch)
			if err != nil {
				return err
			}
			if n < purgeBatch {
				break
			}
		}
		p.lastPurge = now
	}
	return nil
}

// Run flushes every FlushInterval, or earlier when MaxBatch sessions are
// dirty, and maintains storage every few flushes, until ctx ends. The caller
// then calls Flush once more with a shutdown deadline.
func (p *Progress) Run(ctx context.Context) {
	ticker := time.NewTicker(p.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
		if _, err := p.Flush(ctx); err != nil && ctx.Err() == nil {
			continue
		}
		p.flushCount++
		if p.flushCount%orphanSweepFlushes == 0 {
			if err := p.Maintain(ctx); err != nil && ctx.Err() == nil {
				p.opts.Logger.Warn("playback maintenance failed", "component", "playback")
			}
		}
	}
}

// UserItemData returns the user data of the visible items among itemIDs.
// Positions buffered on this instance but not yet flushed are included.
func (p *Progress) UserItemData(ctx context.Context, userID string, itemIDs []string) (map[string]domain.UserItemData, error) {
	if p == nil || ctx == nil {
		return nil, domain.ErrInvalid
	}
	if !domain.ValidID(userID) || len(itemIDs) > domain.BrowseLimitMax {
		return nil, domain.ErrInvalid
	}
	for _, id := range itemIDs {
		if !domain.ValidID(id) {
			return nil, domain.ErrInvalid
		}
	}
	if len(itemIDs) == 0 {
		return map[string]domain.UserItemData{}, nil
	}
	data, err := p.repo.UserItemData(ctx, userID, itemIDs)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	for _, s := range p.sessions {
		if s.userID != userID || !s.dirty {
			continue
		}
		// Only items storage reported as visible are overlaid, so a buffered
		// session never makes an invisible item appear.
		if d, ok := data[s.itemID]; ok {
			d.ResumeTicks = domain.ProgressResume(s.position, s.runtime, p.opts.Rules)
			last := s.lastReport
			d.LastPlayedAt = &last
			data[s.itemID] = d
		}
	}
	p.mu.Unlock()
	return data, nil
}

// SetPlayed marks an item played or unplayed for userID.
func (p *Progress) SetPlayed(ctx context.Context, userID, itemID string, played bool, at *time.Time) (domain.UserItemData, error) {
	if p == nil || ctx == nil {
		return domain.UserItemData{}, domain.ErrInvalid
	}
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	when := p.opts.Clock.Now()
	if at != nil {
		when = *at
	}
	return p.repo.SetPlayed(ctx, userID, itemID, played, when)
}

// Resume pages the continue watching list of userID.
func (p *Progress) Resume(ctx context.Context, userID string, query domain.ResumeQuery) (domain.ResumePage, error) {
	if p == nil || ctx == nil {
		return domain.ResumePage{}, domain.ErrInvalid
	}
	if !domain.ValidID(userID) || !domain.ValidResumeQuery(query) {
		return domain.ResumePage{}, domain.ErrInvalid
	}
	return p.repo.ListResume(ctx, userID, query)
}

// ClearHistory deletes the actor's own playback history and user data
// (G23.4). Buffered sessions of the user are dropped first so no later flush
// writes them back.
func (p *Progress) ClearHistory(ctx context.Context, actor domain.Actor) error {
	if p == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrUnauthenticated
	}
	p.forget(actor.UserID)
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	return p.repo.ClearPlaybackHistory(ctx, actor)
}

// Forget drops every buffered session of a user, for user deletion.
func (p *Progress) forget(userID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, s := range p.sessions {
		if s.userID == userID {
			if s.dirty {
				p.dirty--
			}
			delete(p.sessions, key)
		}
	}
}

// ActiveSessions lists running sessions for an administrator, with the
// positions buffered on this instance.
func (p *Progress) ActiveSessions(ctx context.Context, actor domain.Actor) ([]domain.ActivePlayback, error) {
	if p == nil || ctx == nil {
		return nil, domain.ErrInvalid
	}
	list, err := p.repo.ListActivePlayback(ctx, actor, domain.ActivePlaybackMax)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	byID := make(map[string]*liveSession, len(p.sessions))
	for _, s := range p.sessions {
		byID[s.id] = s
	}
	for i := range list {
		if s := byID[list[i].ID]; s != nil {
			list[i].PositionTicks, list[i].Paused, list[i].LastReportAt = s.position, s.paused, s.lastReport
		}
	}
	p.mu.Unlock()
	return list, nil
}

// WithProgress enables playback reporting and user data on a catalog.
func (c *Catalog) WithProgress(progress *Progress) (*Catalog, error) {
	if c == nil || progress == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.progress = progress
	return &next, nil
}

// progressReady refuses instead of pretending nothing was played when the
// buffer is not wired.
func (c *Catalog) progressReady(ctx context.Context) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.progress == nil {
		return domain.ErrDatabase
	}
	return ctx.Err()
}

// HasProgress reports whether playback reporting is wired.
func (c *Catalog) HasProgress() bool { return c != nil && c.progress != nil }

// ReportPlayback applies one playback report of a native session.
func (c *Catalog) ReportPlayback(ctx context.Context, actor domain.Actor, report domain.PlaybackReport) error {
	if err := c.progressReady(ctx); err != nil {
		return err
	}
	return c.progress.Report(ctx, actor, report)
}

// PlaybackReportInterval is the client side report interval to announce.
func (c *Catalog) PlaybackReportInterval() time.Duration {
	if c == nil || c.progress == nil {
		return 10 * time.Second
	}
	return c.progress.ReportInterval()
}

// UserItemData returns userID's data for the visible items among itemIDs.
func (c *Catalog) UserItemData(ctx context.Context, userID string, itemIDs []string) (map[string]domain.UserItemData, error) {
	if err := c.progressReady(ctx); err != nil {
		return nil, err
	}
	return c.progress.UserItemData(ctx, userID, itemIDs)
}

// SetPlayed marks an item userID can see played or unplayed.
func (c *Catalog) SetPlayed(ctx context.Context, userID, itemID string, played bool, at *time.Time) (domain.UserItemData, error) {
	if err := c.progressReady(ctx); err != nil {
		return domain.UserItemData{}, err
	}
	return c.progress.SetPlayed(ctx, userID, itemID, played, at)
}

// Resume pages userID's continue watching list.
func (c *Catalog) Resume(ctx context.Context, userID string, query domain.ResumeQuery) (domain.ResumePage, error) {
	if err := c.progressReady(ctx); err != nil {
		return domain.ResumePage{}, err
	}
	return c.progress.Resume(ctx, userID, query)
}

// ClearPlaybackHistory deletes the actor's own playback history.
func (c *Catalog) ClearPlaybackHistory(ctx context.Context, actor domain.Actor) error {
	if err := c.progressReady(ctx); err != nil {
		return err
	}
	return c.progress.ClearHistory(ctx, actor)
}

// ActivePlayback lists running sessions for an administrator.
func (c *Catalog) ActivePlayback(ctx context.Context, actor domain.Actor) ([]domain.ActivePlayback, error) {
	if err := c.progressReady(ctx); err != nil {
		return nil, err
	}
	return c.progress.ActiveSessions(ctx, actor)
}
