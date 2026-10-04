package access

import (
	"slices"
	"strings"
	"time"
)

// Evaluate applies the snapshot to req. It is pure apart from reading the
// clock when req.Time is zero.
//
// Precedence and conflict merging (G47.2, G47.4):
//
//  1. A rule is a candidate when it is enabled, its pattern matches, its Scope
//     covers the request and its Window contains req.Time. A request value that
//     is empty never matches a pattern; only MatchAbsent matches it.
//  2. Enforcing candidates are ordered by Priority (larger first), then by ID,
//     and walked one priority tier at a time:
//     - a deny in the tier decides the request (deny wins over an allow of the
//     same priority);
//     - otherwise every restriction in the tier is merged in: read_only and
//     force_relogin are ORed, restrict_libraries sets are intersected and
//     the strictest rate_limit is kept;
//     - an allow in the tier (allow list) stops the walk, so lower-priority
//     denies and restrictions no longer apply, while restrictions of the
//     same or a higher priority still do.
//  3. When no allow rule stopped the walk and the request is not Known, the
//     unknown-client policy is merged in last (read_only adds a restriction,
//     deny and pending_approval decide the request).
//  4. A dimension value longer than Limits.MaxValueLen (or more header values
//     than MaxHeaderValues) is not matched and denies the request: an attacker
//     must not dodge a rule, or burn CPU, by padding a value.
//  5. Exempt administrators (Principal.Admin) and loopback peers (not
//     Proxied) are allowed unconditionally when the options say so; matches
//     are still reported.
//  6. observe and shadow rules never change the outcome. They are reported in
//     Observed/Shadowed, and Simulated shows the outcome if they enforced
//     their Intent alongside the real rules.
func (s *Snapshot) Evaluate(req Request) Decision {
	at := req.Time
	if at.IsZero() {
		at = time.Now()
	}
	var d Decision
	var hits []*compiledRule
	lim := s.opts.Limits
	addr := req.IP
	if addr.IsValid() {
		addr = addr.Unmap().WithZone("")
	}

	for _, idx := range s.dims {
		if idx.key.dim == DimIP {
			if !addr.IsValid() {
				hits = append(hits, idx.absent...)
				continue
			}
			for _, bits := range idx.cidr.lens {
				if bits > addr.BitLen() {
					break
				}
				if p, err := addr.Prefix(bits); err == nil {
					hits = append(hits, idx.cidr.m[p]...)
				}
			}
			continue
		}
		var values []string
		if idx.key.dim == DimHeader {
			values = req.Headers.Values(idx.key.header)
			if len(values) > lim.MaxHeaderValues {
				d.Oversized = true
				continue
			}
		} else if v := requestValue(&req, idx.key.dim); v != "" {
			values = []string{v}
		}
		present := false
		for _, v := range values {
			if v == "" {
				continue
			}
			present = true
			if len(v) > lim.MaxValueLen {
				d.Oversized = true
				continue
			}
			hits = idx.matchValue(v, hits)
		}
		if !present {
			hits = append(hits, idx.absent...)
		}
	}

	// Filter by scope and window, then order by rank and drop duplicates
	// (a header rule can match more than one value).
	kept := hits[:0]
	for _, c := range hits {
		if c.scope.covers(&req) && c.window.contains(at) {
			kept = append(kept, c)
		}
	}
	slices.SortFunc(kept, func(a, b *compiledRule) int { return a.rank - b.rank })
	kept = slices.CompactFunc(kept, func(a, b *compiledRule) bool { return a == b })

	var enforcing, simulated []*compiledRule
	for _, c := range kept {
		switch c.action {
		case ActionObserve:
			d.Observed = append(d.Observed, Hit{RuleID: c.id, Action: c.intent})
		case ActionShadow:
			d.Shadowed = append(d.Shadowed, Hit{RuleID: c.id, Action: c.intent})
		default:
			d.Matched = append(d.Matched, Hit{RuleID: c.id, Action: c.action})
			enforcing = append(enforcing, c)
		}
		simulated = append(simulated, c)
	}

	switch {
	case s.opts.ExemptAdmins && req.Principal.Admin:
		d.Exempt = ExemptAdmin
	case s.opts.ExemptLoopback && !req.Proxied && addr.IsValid() && addr.IsLoopback():
		d.Exempt = ExemptLoopback
	}
	if d.Exempt != ExemptNone {
		d.Outcome = Outcome{Verdict: VerdictAllow}
		if len(simulated) > len(enforcing) {
			d.Simulated = &Outcome{Verdict: VerdictAllow}
		}
		return d
	}
	unknown := !req.Known
	d.Outcome = s.merge(enforcing, unknown, false, d.Oversized)
	if len(simulated) > len(enforcing) {
		o := s.merge(simulated, unknown, true, d.Oversized)
		d.Simulated = &o
	}
	return d
}

func requestValue(req *Request, dim Dimension) string {
	switch dim {
	case DimUserAgent:
		return req.UserAgent
	case DimAppName:
		return req.AppName
	case DimAppVersion:
		return req.AppVersion
	case DimDeviceID:
		return req.DeviceID
	case DimDeviceName:
		return req.DeviceName
	case DimDeviceType:
		return req.DeviceType
	case DimAPIKey:
		return req.APIKeyFingerprint
	}
	return ""
}

func (idx *dimIndex) matchValue(v string, hits []*compiledRule) []*compiledRule {
	hits = append(hits, idx.exact[v]...)
	folded := v
	if idx.anyFold {
		folded = strings.ToLower(v)
		hits = append(hits, idx.exactFold[folded]...)
		hits = matchPrefixes(&idx.prefixFold, folded, hits)
	}
	hits = matchPrefixes(&idx.prefix, v, hits)
	for _, c := range idx.scan {
		if c.matchScan(v, folded) {
			hits = append(hits, c)
		}
	}
	// Prefiltered rules run only when a literal they require occurs. A rule
	// can be reported once per occurrence; it is checked and added once.
	start := len(hits)
	var checked []*compiledRule
	verify := func(c *compiledRule) {
		if !slices.Contains(hits[start:], c) && !slices.Contains(checked, c) {
			if c.matchScan(v, folded) {
				hits = append(hits, c)
			} else {
				checked = append(checked, c)
			}
		}
	}
	idx.litRaw.each(v, verify)
	if idx.anyFold {
		idx.litFold.each(folded, verify)
	}
	return hits
}

// matchScan runs a glob or regex rule's full matcher.
func (c *compiledRule) matchScan(v, folded string) bool {
	switch {
	case c.re != nil:
		return c.re.MatchString(v) // case folding is compiled into the regex
	case c.glob != nil:
		if c.fold {
			return c.glob.match(folded)
		}
		return c.glob.match(v)
	}
	return false
}

func matchPrefixes(x *lenIndex[string], v string, hits []*compiledRule) []*compiledRule {
	for _, n := range x.lens {
		if n > len(v) {
			break
		}
		hits = append(hits, x.m[v[:n]]...)
	}
	return hits
}

func (sc *compiledScope) covers(req *Request) bool {
	has := func(v string) bool {
		_, ok := sc.values[v]
		return v != "" && ok
	}
	switch sc.kind {
	case ScopeUser:
		return has(req.Principal.UserID)
	case ScopeGroup:
		for _, g := range req.Groups {
			if has(g) {
				return true
			}
		}
		return false
	case ScopeLibrary:
		return has(req.LibraryID)
	case ScopeClientKind:
		return has(string(req.Principal.Kind))
	}
	return true
}

func (w *compiledWindow) contains(t time.Time) bool {
	if w == nil {
		return true
	}
	if !w.from.IsZero() && t.Before(w.from) {
		return false
	}
	if !w.until.IsZero() && !t.Before(w.until) {
		return false
	}
	if !w.daily {
		return true
	}
	lt := t.In(w.loc)
	m := lt.Hour()*60 + lt.Minute()
	day := lt.Weekday()
	switch {
	case w.start < w.end:
		if m < w.start || m >= w.end {
			return false
		}
	case m >= w.start:
	case m < w.end:
		day = (day + 6) % 7 // the window opened the previous day
	default:
		return false
	}
	return w.weekdays&(1<<day) != 0
}

// merge folds ordered hits into an Outcome; see Evaluate for the rules. When
// simulate is set, observe/shadow rules count as their Intent.
func (s *Snapshot) merge(hits []*compiledRule, unknown, simulate, oversized bool) Outcome {
	if oversized {
		return Outcome{Verdict: VerdictDeny, Code: CodeClientBlocked}
	}
	var out Outcome
	act := func(c *compiledRule) Action {
		if simulate && c.action.recordOnly() {
			return c.intent
		}
		return c.action
	}
	allowed := false
	for i := 0; i < len(hits) && !allowed; {
		j := i
		for j < len(hits) && hits[j].priority == hits[i].priority {
			j++
		}
		tier := hits[i:j]
		var denies []string
		for _, c := range tier {
			if act(c) == ActionDeny {
				denies = append(denies, c.id)
			}
		}
		if len(denies) > 0 {
			return Outcome{Verdict: VerdictDeny, Code: CodeClientBlocked, Rules: denies}
		}
		for _, c := range tier {
			switch act(c) {
			case ActionReadOnly:
				out.ReadOnly = true
			case ActionForceRelogin:
				out.ForceRelogin = true
			case ActionRestrictLibraries:
				if out.Libraries == nil {
					out.Libraries = slices.Clone(c.libraries)
				} else {
					out.Libraries = intersectSorted(out.Libraries, c.libraries)
				}
			case ActionRateLimit:
				if out.RateLimit == nil || stricter(c.rate, out.RateLimit) {
					rl := *c.rate
					out.RateLimit = &rl
				}
			case ActionAllow:
				allowed = true
			default:
				continue
			}
			out.Rules = append(out.Rules, c.id)
		}
		i = j
	}
	out.Verdict = VerdictAllow
	if !allowed && unknown {
		switch s.opts.UnknownClients {
		case UnknownReadOnly:
			out.ReadOnly, out.DefaultApplied = true, true
		case UnknownDeny:
			return Outcome{Verdict: VerdictDeny, Code: CodeClientBlocked, DefaultApplied: true}
		case UnknownPending:
			return Outcome{Verdict: VerdictPending, Code: CodeClientPending, DefaultApplied: true}
		}
	}
	return out
}

func intersectSorted(a, b []string) []string {
	out := a[:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// stricter reports whether a permits fewer requests per unit time than b.
func stricter(a, b *RateLimit) bool {
	ra := float64(a.Requests) / a.Per.Seconds()
	rb := float64(b.Requests) / b.Per.Seconds()
	return ra < rb
}
