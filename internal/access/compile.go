package access

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // window time zones must not depend on the host zoneinfo
	"unicode/utf8"
)

var (
	// ErrInvalidRule wraps every per-rule validation failure.
	ErrInvalidRule = errors.New("access: invalid client rule")
	// ErrLimitExceeded reports a rule set or rule beyond the configured Limits.
	ErrLimitExceeded = errors.New("access: client rule limit exceeded")
)

const (
	maxRuleIDLen      = 128
	maxReportedErrors = 100
)

// RuleError identifies the rule that failed to compile.
type RuleError struct {
	Index  int
	RuleID string
	Err    error
}

func (e *RuleError) Error() string {
	return fmt.Sprintf("access: rule %d (%q): %v", e.Index, e.RuleID, e.Err)
}

func (e *RuleError) Unwrap() error { return e.Err }

type dimKey struct {
	dim    Dimension
	header string
}

type compiledScope struct {
	kind   ScopeKind
	values map[string]struct{}
}

type compiledWindow struct {
	from, until time.Time
	daily       bool
	start, end  int // minutes since local midnight, end exclusive
	weekdays    uint8
	loc         *time.Location
}

type compiledRule struct {
	id        string
	rank      int
	priority  int
	action    Action
	intent    Action
	match     MatchKind
	fold      bool
	lit       string
	glob      *globPattern
	re        *regexp.Regexp
	prefix    netip.Prefix
	window    *compiledWindow
	scope     compiledScope
	libraries []string
	rate      *RateLimit
}

// lenIndex maps keys of a few distinct lengths to rules, so a value is probed
// once per distinct length instead of once per rule.
type lenIndex[K comparable] struct {
	lens []int
	m    map[K][]*compiledRule
}

func (x *lenIndex[K]) add(k K, n int, c *compiledRule) {
	if x.m == nil {
		x.m = map[K][]*compiledRule{}
	}
	if !slices.Contains(x.lens, n) {
		x.lens = append(x.lens, n)
		slices.Sort(x.lens)
	}
	x.m[k] = append(x.m[k], c)
}

type dimIndex struct {
	key        dimKey
	exact      map[string][]*compiledRule
	exactFold  map[string][]*compiledRule
	prefix     lenIndex[string]
	prefixFold lenIndex[string]
	cidr       lenIndex[netip.Prefix]
	scan       []*compiledRule
	absent     []*compiledRule
	anyFold    bool
}

// Snapshot is an immutable compiled rule set. It is safe for concurrent use.
type Snapshot struct {
	opts  Options
	dims  []*dimIndex
	rules int
}

// Options returns the options the snapshot was compiled with.
func (s *Snapshot) Options() Options { return s.opts }

// Len returns the number of enabled rules in the snapshot.
func (s *Snapshot) Len() int { return s.rules }

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&l.MaxRules, d.MaxRules)
	fill(&l.MaxPatternLen, d.MaxPatternLen)
	fill(&l.MaxRegexRules, d.MaxRegexRules)
	fill(&l.MaxRegexInsts, d.MaxRegexInsts)
	fill(&l.MaxLibraries, d.MaxLibraries)
	fill(&l.MaxNoteLen, d.MaxNoteLen)
	fill(&l.MaxScopeValues, d.MaxScopeValues)
	fill(&l.MaxValueLen, d.MaxValueLen)
	fill(&l.MaxHeaderValues, d.MaxHeaderValues)
	return l
}

// Compile validates rules and builds an immutable snapshot. Every rule,
// enabled or not, is validated so that enabling one later cannot fail; only
// enabled rules are indexed. Zero Limits fields take their defaults; start
// from DefaultOptions to keep the G47.7 exemptions.
func Compile(rules []Rule, opts Options) (*Snapshot, error) {
	opts.Limits = opts.Limits.withDefaults()
	switch opts.UnknownClients {
	case "":
		opts.UnknownClients = UnknownAllow
	case UnknownAllow, UnknownReadOnly, UnknownDeny, UnknownPending:
	default:
		return nil, fmt.Errorf("%w: unknown client policy %q", ErrInvalidRule, opts.UnknownClients)
	}
	lim := opts.Limits
	if len(rules) > lim.MaxRules {
		return nil, fmt.Errorf("%w: %d rules, at most %d", ErrLimitExceeded, len(rules), lim.MaxRules)
	}

	var errs []error
	report := func(i int, id string, err error) {
		if len(errs) < maxReportedErrors {
			errs = append(errs, &RuleError{Index: i, RuleID: id, Err: err})
		}
	}
	seen := make(map[string]struct{}, len(rules))
	compiled := make([]*compiledRule, 0, len(rules))
	keys := make([]dimKey, 0, len(rules))
	regexCount := 0
	for i := range rules {
		r := &rules[i]
		if _, dup := seen[r.ID]; dup && r.ID != "" {
			report(i, r.ID, fmt.Errorf("%w: duplicate id", ErrInvalidRule))
			continue
		}
		seen[r.ID] = struct{}{}
		c, key, err := compileRule(r, lim)
		if err != nil {
			report(i, r.ID, err)
			continue
		}
		if !r.Enabled {
			continue
		}
		if c.re != nil {
			regexCount++
			if regexCount > lim.MaxRegexRules {
				report(i, r.ID, fmt.Errorf("%w: more than %d enabled regex rules", ErrLimitExceeded, lim.MaxRegexRules))
				continue
			}
		}
		compiled = append(compiled, c)
		keys = append(keys, key)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	order := make([]int, len(compiled))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ra, rb := compiled[order[a]], compiled[order[b]]
		if ra.priority != rb.priority {
			return ra.priority > rb.priority
		}
		return ra.id < rb.id
	})
	for rank, idx := range order {
		compiled[idx].rank = rank
	}

	s := &Snapshot{opts: opts, rules: len(compiled)}
	byKey := map[dimKey]*dimIndex{}
	for i, c := range compiled {
		idx := byKey[keys[i]]
		if idx == nil {
			idx = &dimIndex{key: keys[i], exact: map[string][]*compiledRule{}, exactFold: map[string][]*compiledRule{}}
			byKey[keys[i]] = idx
			s.dims = append(s.dims, idx)
		}
		switch {
		case c.match == MatchAbsent:
			idx.absent = append(idx.absent, c)
		case keys[i].dim == DimIP:
			idx.cidr.add(c.prefix, c.prefix.Bits(), c)
		case c.match == MatchPrefix:
			if c.fold {
				idx.prefixFold.add(c.lit, len(c.lit), c)
				idx.anyFold = true
			} else {
				idx.prefix.add(c.lit, len(c.lit), c)
			}
		case c.match == MatchExact:
			if c.fold {
				idx.exactFold[c.lit] = append(idx.exactFold[c.lit], c)
				idx.anyFold = true
			} else {
				idx.exact[c.lit] = append(idx.exact[c.lit], c)
			}
		default:
			if c.fold {
				idx.anyFold = true
			}
			idx.scan = append(idx.scan, c)
		}
	}
	return s, nil
}

func compileRule(r *Rule, lim Limits) (*compiledRule, dimKey, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
	}
	var key dimKey
	if r.ID == "" || len(r.ID) > maxRuleIDLen {
		return nil, key, invalid("id must be 1..%d bytes", maxRuleIDLen)
	}
	if len(r.Note) > lim.MaxNoteLen {
		return nil, key, fmt.Errorf("%w: note longer than %d bytes", ErrLimitExceeded, lim.MaxNoteLen)
	}
	key.dim = r.Dimension
	switch r.Dimension {
	case DimUserAgent, DimAppName, DimAppVersion, DimDeviceID, DimDeviceName, DimDeviceType, DimIP, DimAPIKey:
		if r.Header != "" {
			return nil, key, invalid("header name is only valid for the header dimension")
		}
	case DimHeader:
		if !validHeaderName(r.Header) {
			return nil, key, invalid("invalid header name %q", r.Header)
		}
		key.header = http.CanonicalHeaderKey(r.Header)
	default:
		return nil, key, invalid("unknown dimension %q", r.Dimension)
	}

	c := &compiledRule{id: r.ID, priority: r.Priority, action: r.Action, match: r.Match}
	if len(r.Pattern) > lim.MaxPatternLen {
		return nil, key, fmt.Errorf("%w: pattern longer than %d bytes", ErrLimitExceeded, lim.MaxPatternLen)
	}
	if !utf8.ValidString(r.Pattern) {
		return nil, key, invalid("pattern is not valid UTF-8")
	}
	if r.Match == MatchAbsent {
		if r.Pattern != "" {
			return nil, key, invalid("absent match takes no pattern")
		}
	} else if r.Pattern == "" {
		return nil, key, invalid("empty pattern")
	}
	if r.Dimension == DimIP {
		switch r.Match {
		case MatchExact, MatchCIDR:
			p, err := parseIPPattern(r.Pattern)
			if err != nil {
				return nil, key, invalid("%v", err)
			}
			if r.Match == MatchExact && p.Bits() != p.Addr().BitLen() {
				return nil, key, invalid("exact ip match takes an address, use cidr for %q", r.Pattern)
			}
			c.prefix = p
		case MatchAbsent:
		default:
			return nil, key, invalid("ip dimension supports exact, cidr and absent, not %q", r.Match)
		}
	} else {
		c.fold = r.CaseFold
		pattern := r.Pattern
		if c.fold && r.Match != MatchRegex {
			pattern = strings.ToLower(pattern)
		}
		switch r.Match {
		case MatchExact, MatchPrefix:
			c.lit = pattern
		case MatchGlob:
			g := compileGlob(pattern)
			if lit, ok := g.literal(); ok {
				c.match, c.lit = MatchExact, lit
			} else {
				c.glob = g
			}
		case MatchRegex:
			re, err := compileRegex(pattern, c.fold, lim)
			if err != nil {
				return nil, key, err
			}
			c.re = re
		case MatchAbsent:
			c.fold = false
		case MatchCIDR:
			return nil, key, invalid("cidr match is only valid for the ip dimension")
		default:
			return nil, key, invalid("unknown match kind %q", r.Match)
		}
	}

	effective := r.Action
	switch {
	case r.Action.enforcing():
		if r.Intent != "" {
			return nil, key, invalid("intent is only valid for observe and shadow rules")
		}
	case r.Action.recordOnly():
		c.intent = r.Intent
		if c.intent == "" {
			c.intent = ActionDeny
		}
		if !c.intent.enforcing() {
			return nil, key, invalid("intent %q is not an enforcing action", r.Intent)
		}
		effective = c.intent
	default:
		return nil, key, invalid("unknown action %q", r.Action)
	}
	if effective == ActionRestrictLibraries {
		if len(r.Libraries) == 0 {
			return nil, key, invalid("restrict_libraries needs at least one library")
		}
		if len(r.Libraries) > lim.MaxLibraries {
			return nil, key, fmt.Errorf("%w: more than %d libraries", ErrLimitExceeded, lim.MaxLibraries)
		}
		libs := slices.Clone(r.Libraries)
		slices.Sort(libs)
		libs = slices.Compact(libs)
		if libs[0] == "" {
			return nil, key, invalid("empty library id")
		}
		c.libraries = libs
	} else if len(r.Libraries) > 0 {
		return nil, key, invalid("libraries are only valid for restrict_libraries")
	}
	if effective == ActionRateLimit {
		if r.RateLimit == nil || r.RateLimit.Requests <= 0 || r.RateLimit.Per <= 0 {
			return nil, key, invalid("rate_limit needs positive requests and period")
		}
		rl := *r.RateLimit
		c.rate = &rl
	} else if r.RateLimit != nil {
		return nil, key, invalid("rate limit is only valid for rate_limit")
	}

	scope, err := compileScope(r.Scope, lim)
	if err != nil {
		return nil, key, err
	}
	c.scope = scope
	if r.Window != nil {
		w, err := compileWindow(*r.Window)
		if err != nil {
			return nil, key, err
		}
		c.window = w
	}
	return c, key, nil
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0:
		default:
			return false
		}
	}
	return true
}

func parseIPPattern(pattern string) (netip.Prefix, error) {
	if strings.Contains(pattern, "/") {
		p, err := netip.ParsePrefix(pattern)
		if err != nil {
			return netip.Prefix{}, err
		}
		if p.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("use the IPv4 form of %q", pattern)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(pattern)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("zoned address %q", pattern)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// compileRegex accepts RE2 syntax only. RE2 runs in time linear in the input,
// so there is no catastrophic backtracking; the instruction cap bounds the
// per-byte cost and Limits.MaxValueLen bounds the input.
func compileRegex(pattern string, fold bool, lim Limits) (*regexp.Regexp, error) {
	if fold {
		pattern = "(?i)" + pattern
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRule, err)
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRule, err)
	}
	if len(prog.Inst) > lim.MaxRegexInsts {
		return nil, fmt.Errorf("%w: regex compiles to %d instructions, at most %d", ErrLimitExceeded, len(prog.Inst), lim.MaxRegexInsts)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRule, err)
	}
	return re, nil
}

func compileScope(s Scope, lim Limits) (compiledScope, error) {
	c := compiledScope{kind: s.Kind}
	switch s.Kind {
	case "", ScopeGlobal:
		c.kind = ScopeGlobal
		if len(s.Values) > 0 {
			return c, fmt.Errorf("%w: global scope takes no values", ErrInvalidRule)
		}
		return c, nil
	case ScopeUser, ScopeGroup, ScopeLibrary, ScopeClientKind:
	default:
		return c, fmt.Errorf("%w: unknown scope %q", ErrInvalidRule, s.Kind)
	}
	if len(s.Values) == 0 {
		return c, fmt.Errorf("%w: %s scope needs values", ErrInvalidRule, s.Kind)
	}
	if len(s.Values) > lim.MaxScopeValues {
		return c, fmt.Errorf("%w: more than %d scope values", ErrLimitExceeded, lim.MaxScopeValues)
	}
	c.values = make(map[string]struct{}, len(s.Values))
	for _, v := range s.Values {
		if v == "" {
			return c, fmt.Errorf("%w: empty scope value", ErrInvalidRule)
		}
		c.values[v] = struct{}{}
	}
	return c, nil
}

func compileWindow(w Window) (*compiledWindow, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: window: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
	}
	c := &compiledWindow{from: w.From, until: w.Until, loc: time.UTC}
	if !w.From.IsZero() && !w.Until.IsZero() && !w.Until.After(w.From) {
		return nil, invalid("until must be after from")
	}
	switch w.TimeZone {
	case "", "UTC":
	case "Local":
		return nil, invalid("time zone must be explicit, not Local")
	default:
		loc, err := time.LoadLocation(w.TimeZone)
		if err != nil {
			return nil, invalid("time zone %q: %v", w.TimeZone, err)
		}
		c.loc = loc
	}
	if (w.DailyStart == "") != (w.DailyEnd == "") {
		return nil, invalid("daily start and end must be set together")
	}
	if w.DailyStart != "" {
		start, err := parseClock(w.DailyStart, false)
		if err != nil {
			return nil, invalid("%v", err)
		}
		end, err := parseClock(w.DailyEnd, true)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if start == end {
			return nil, invalid("daily start equals end")
		}
		c.daily, c.start, c.end = true, start, end
	}
	for _, d := range w.Weekdays {
		if d < time.Sunday || d > time.Saturday {
			return nil, invalid("weekday %d", d)
		}
		c.weekdays |= 1 << d
	}
	if c.weekdays != 0 && !c.daily {
		c.daily, c.start, c.end = true, 0, 24*60
	}
	if c.weekdays == 0 {
		c.weekdays = 0x7f
	}
	if !c.daily && w.TimeZone != "" {
		return nil, invalid("time zone without a daily window has no effect")
	}
	return c, nil
}

func parseClock(s string, allow24 bool) (int, error) {
	var h, m int
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("clock %q is not HH:MM", s)
	}
	for _, b := range []byte(s[:2] + s[3:]) {
		if b < '0' || b > '9' {
			return 0, fmt.Errorf("clock %q is not HH:MM", s)
		}
	}
	h = int(s[0]-'0')*10 + int(s[1]-'0')
	m = int(s[3]-'0')*10 + int(s[4]-'0')
	if m > 59 || h > 24 || (h == 24 && (m != 0 || !allow24)) {
		return 0, fmt.Errorf("clock %q out of range", s)
	}
	return h*60 + m, nil
}

// globPattern is a '*'-separated list of segments. Each segment has a fixed
// rune length, so matching scans each candidate start once per segment with
// leftmost placement: O(len(value) * len(pattern)) worst case, no backtracking.
type globPattern struct {
	segs     [][]globTok
	anchorL  bool // pattern does not start with '*'
	anchorR  bool // pattern does not end with '*'
	hasStars bool
}

type globTok struct {
	lit string // empty means '?'
}

func compileGlob(p string) *globPattern {
	g := &globPattern{}
	var seg []globTok
	var lit strings.Builder
	flushLit := func() {
		if lit.Len() > 0 {
			seg = append(seg, globTok{lit: lit.String()})
			lit.Reset()
		}
	}
	flushSeg := func() {
		flushLit()
		g.segs = append(g.segs, seg)
		seg = nil
	}
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRuneInString(p[i:])
		switch r {
		case '\\':
			if i+size < len(p) {
				nr, nsize := utf8.DecodeRuneInString(p[i+size:])
				lit.WriteRune(nr)
				i += size + nsize
				continue
			}
			lit.WriteRune(r)
		case '*':
			flushSeg()
			g.hasStars = true
		case '?':
			flushLit()
			seg = append(seg, globTok{})
		default:
			lit.WriteRune(r)
		}
		i += size
	}
	flushSeg()
	g.anchorL = len(g.segs[0]) > 0 || !g.hasStars
	g.anchorR = len(g.segs[len(g.segs)-1]) > 0 || !g.hasStars
	// Drop empty segments produced by leading, trailing or repeated stars.
	segs := g.segs[:0]
	for _, s := range g.segs {
		if len(s) > 0 {
			segs = append(segs, s)
		}
	}
	g.segs = segs
	return g
}

// literal reports whether the glob has no wildcards at all.
func (g *globPattern) literal() (string, bool) {
	if g.hasStars {
		return "", false
	}
	var b strings.Builder
	for _, s := range g.segs {
		for _, t := range s {
			if t.lit == "" {
				return "", false
			}
			b.WriteString(t.lit)
		}
	}
	return b.String(), true
}

// matchAt returns the end offset when seg matches v starting at i.
func matchSegAt(seg []globTok, v string, i int) (int, bool) {
	for _, t := range seg {
		if t.lit == "" {
			if i >= len(v) {
				return 0, false
			}
			_, size := utf8.DecodeRuneInString(v[i:])
			i += size
			continue
		}
		if !strings.HasPrefix(v[i:], t.lit) {
			return 0, false
		}
		i += len(t.lit)
	}
	return i, true
}

func (g *globPattern) match(v string) bool {
	if len(g.segs) == 0 {
		return g.hasStars || v == "" // "*" matches anything
	}
	pos := 0
	segs := g.segs
	if g.anchorL {
		end, ok := matchSegAt(segs[0], v, 0)
		if !ok {
			return false
		}
		if len(segs) == 1 && g.anchorR {
			return end == len(v)
		}
		pos, segs = end, segs[1:]
	}
	last := len(segs)
	if g.anchorR {
		last-- // the final segment is placed against the end below
	}
	for _, seg := range segs[:last] {
		end, ok := scanSeg(seg, v, pos, false)
		if !ok {
			return false
		}
		pos = end
	}
	if !g.anchorR {
		return true
	}
	_, ok := scanSeg(segs[last], v, pos, true)
	return ok
}

// scanSeg finds the leftmost start at or after pos where seg matches (and,
// with toEnd, ends exactly at len(v)). A leading literal is located with
// strings.Index rather than probed at every rune.
func scanSeg(seg []globTok, v string, pos int, toEnd bool) (int, bool) {
	lead := seg[0].lit
	for i := pos; i <= len(v); {
		if lead != "" {
			j := strings.Index(v[i:], lead)
			if j < 0 {
				return 0, false
			}
			i += j
		}
		if end, ok := matchSegAt(seg, v, i); ok && (!toEnd || end == len(v)) {
			return end, true
		}
		if i == len(v) {
			break
		}
		_, size := utf8.DecodeRuneInString(v[i:])
		i += size
	}
	return 0, false
}

// Engine holds the current snapshot behind an atomic pointer so rule changes
// swap in without blocking evaluation.
type Engine struct {
	cur atomic.Pointer[Snapshot]
}

// NewEngine starts with s, or with an empty DefaultOptions snapshot when s is nil.
func NewEngine(s *Snapshot) *Engine {
	if s == nil {
		s, _ = Compile(nil, DefaultOptions())
	}
	e := &Engine{}
	e.cur.Store(s)
	return e
}

// Snapshot returns the snapshot currently in force.
func (e *Engine) Snapshot() *Snapshot { return e.cur.Load() }

// Store installs s and returns the previous snapshot. A nil s is ignored.
func (e *Engine) Store(s *Snapshot) *Snapshot {
	if s == nil {
		return e.cur.Load()
	}
	return e.cur.Swap(s)
}

// Replace compiles rules and installs them; on error the current snapshot stays.
func (e *Engine) Replace(rules []Rule, opts Options) error {
	s, err := Compile(rules, opts)
	if err != nil {
		return err
	}
	e.cur.Store(s)
	return nil
}

// Evaluate runs req against the current snapshot.
func (e *Engine) Evaluate(req Request) Decision { return e.cur.Load().Evaluate(req) }
