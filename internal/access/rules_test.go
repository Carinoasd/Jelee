package access

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var noon = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // a Monday

func mustCompile(t *testing.T, opts Options, rules ...Rule) *Snapshot {
	t.Helper()
	s, err := Compile(rules, opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func testOptions() Options {
	o := DefaultOptions()
	o.ExemptAdmins, o.ExemptLoopback = false, false
	return o
}

func deny(id string, dim Dimension, match MatchKind, pattern string) Rule {
	return Rule{ID: id, Dimension: dim, Match: match, Pattern: pattern, Action: ActionDeny, Enabled: true}
}

func baseRequest() Request {
	return Request{
		UserAgent: "Infuse/7.8 (iPhone)", AppName: "Infuse", AppVersion: "7.8.1",
		DeviceID: "dev-123", DeviceName: "Living Room", DeviceType: "tv",
		IP: netip.MustParseAddr("203.0.113.9"), APIKeyFingerprint: "sha256:abcd",
		Headers:   http.Header{"X-Client-Name": {"Infuse-Direct"}},
		Principal: Principal{UserID: "u1", SessionID: "s1", Kind: ClientNative},
		Groups:    []string{"family"}, LibraryID: "lib-movies", Known: true, Time: noon,
	}
}

func verdict(t *testing.T, s *Snapshot, req Request) Verdict {
	t.Helper()
	return s.Evaluate(req).Verdict
}

func TestMatchKindsPerDimension(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want bool
	}{
		{"exact ua", deny("r", DimUserAgent, MatchExact, "Infuse/7.8 (iPhone)"), true},
		{"exact ua miss", deny("r", DimUserAgent, MatchExact, "Infuse/7.8"), false},
		{"prefix ua", deny("r", DimUserAgent, MatchPrefix, "Infuse/"), true},
		{"prefix miss", deny("r", DimUserAgent, MatchPrefix, "Kodi/"), false},
		{"glob star", deny("r", DimUserAgent, MatchGlob, "Infuse/*(iPhone)"), true},
		{"glob question", deny("r", DimAppVersion, MatchGlob, "7.?.1"), true},
		{"glob anchored", deny("r", DimAppVersion, MatchGlob, "7.?"), false},
		{"glob literal", deny("r", DimAppName, MatchGlob, "Infuse"), true},
		{"glob escaped star", deny("r", DimAppName, MatchGlob, `Inf\*`), false},
		{"regex search", deny("r", DimUserAgent, MatchRegex, `iPhone\)$`), true},
		{"regex miss", deny("r", DimUserAgent, MatchRegex, `^iPhone`), false},
		{"device id", deny("r", DimDeviceID, MatchExact, "dev-123"), true},
		{"device name", deny("r", DimDeviceName, MatchPrefix, "Living"), true},
		{"device type", deny("r", DimDeviceType, MatchExact, "tv"), true},
		{"api key", deny("r", DimAPIKey, MatchExact, "sha256:abcd"), true},
		{"header", Rule{ID: "r", Dimension: DimHeader, Header: "x-client-name", Match: MatchPrefix, Pattern: "Infuse", Action: ActionDeny, Enabled: true}, true},
		{"header other", Rule{ID: "r", Dimension: DimHeader, Header: "X-Other", Match: MatchPrefix, Pattern: "Infuse", Action: ActionDeny, Enabled: true}, false},
		{"absent header", Rule{ID: "r", Dimension: DimHeader, Header: "X-Other", Match: MatchAbsent, Action: ActionDeny, Enabled: true}, true},
		{"absent present", Rule{ID: "r", Dimension: DimUserAgent, Match: MatchAbsent, Action: ActionDeny, Enabled: true}, false},
		{"disabled", Rule{ID: "r", Dimension: DimUserAgent, Match: MatchPrefix, Pattern: "Infuse", Action: ActionDeny}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustCompile(t, testOptions(), tc.rule)
			if got := verdict(t, s, baseRequest()) == VerdictDeny; got != tc.want {
				t.Fatalf("denied = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmptyValueOnlyMatchesAbsent(t *testing.T) {
	s := mustCompile(t, testOptions(),
		deny("glob-all", DimDeviceID, MatchGlob, "*"),
		deny("regex-any", DimDeviceName, MatchRegex, ".*"),
	)
	req := baseRequest()
	req.DeviceID, req.DeviceName = "", ""
	if v := verdict(t, s, req); v != VerdictAllow {
		t.Fatalf("empty value matched a pattern: %v", v)
	}
	s = mustCompile(t, testOptions(), Rule{ID: "no-ua", Dimension: DimUserAgent, Match: MatchAbsent, Action: ActionDeny, Enabled: true})
	req.UserAgent = ""
	if d := s.Evaluate(req); d.Verdict != VerdictDeny || d.Code != CodeClientBlocked {
		t.Fatalf("absent ua not denied: %+v", d)
	}
}

func TestGlobMatcher(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"*", "", true},
		{"*", "anything", true},
		{"a*", "a", true},
		{"a*", "ba", false},
		{"*a", "ba", true},
		{"*a", "ab", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "acb", false},
		{"a*b*c", "abcbc", true},
		{"a*bc", "abcbc", true},
		{"?", "é", true},
		{"??", "é", false},
		{"*?x", "x", false},
		{"*?x", "yx", true},
		{"a**b", "ab", true},
		{`a\?`, "a?", true},
		{`a\?`, "ab", false},
		{`tail\`, `tail\`, true},
		{"*ab*ab*", "xabyab", true},
		{"*ab*ab*", "xab", false},
		{"中*文", "中間的文", true},
	}
	for _, tc := range cases {
		if got := compileGlob(tc.pattern).match(tc.value); got != tc.want {
			t.Errorf("glob %q on %q = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// TestGlobAgreesWithRegexp cross-checks the hand-written matcher against an
// equivalent anchored RE2 pattern on random inputs.
func TestGlobAgreesWithRegexp(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"a", "b", "é", "*", "?"}
	for n := 0; n < 5000; n++ {
		var p, re strings.Builder
		re.WriteString(`^`)
		for range rng.IntN(7) + 1 {
			tok := alphabet[rng.IntN(len(alphabet))]
			p.WriteString(tok)
			switch tok {
			case "*":
				re.WriteString(`.*`)
			case "?":
				re.WriteString(`.`)
			default:
				re.WriteString(regexp.QuoteMeta(tok))
			}
		}
		re.WriteString(`$`)
		g, want := compileGlob(p.String()), regexp.MustCompile("(?s)"+re.String())
		var v strings.Builder
		for range rng.IntN(8) {
			v.WriteString(alphabet[rng.IntN(3)])
		}
		if got := g.match(v.String()); got != want.MatchString(v.String()) {
			t.Fatalf("glob %q on %q = %v, regexp says %v", p.String(), v.String(), got, !got)
		}
	}
}

func TestGlobIsNotExponential(t *testing.T) {
	pattern := strings.Repeat("a*", 40) + "b"
	value := strings.Repeat("a", 8000)
	g := compileGlob(pattern)
	start := time.Now()
	if g.match(value) {
		t.Fatal("unexpected match")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pathological glob took %v", elapsed)
	}
}

func TestCaseFolding(t *testing.T) {
	for _, tc := range []struct {
		match   MatchKind
		pattern string
	}{
		{MatchExact, "INFUSE/7.8 (IPHONE)"},
		{MatchPrefix, "infuse/"},
		{MatchGlob, "INFUSE/*"},
		{MatchRegex, `^infuse/7\.8`},
	} {
		t.Run(string(tc.match), func(t *testing.T) {
			sensitive := deny("r", DimUserAgent, tc.match, tc.pattern)
			if v := verdict(t, mustCompile(t, testOptions(), sensitive), baseRequest()); v != VerdictAllow {
				t.Fatalf("case-sensitive rule matched: %v", v)
			}
			folded := sensitive
			folded.CaseFold = true
			if v := verdict(t, mustCompile(t, testOptions(), folded), baseRequest()); v != VerdictDeny {
				t.Fatalf("case-folded rule missed: %v", v)
			}
		})
	}
}

func TestIPAndCIDR(t *testing.T) {
	cases := []struct {
		match   MatchKind
		pattern string
		ip      string
		want    bool
	}{
		{MatchExact, "203.0.113.9", "203.0.113.9", true},
		{MatchExact, "203.0.113.9", "203.0.113.10", false},
		{MatchExact, "203.0.113.9", "::ffff:203.0.113.9", true},
		{MatchCIDR, "203.0.113.0/24", "203.0.113.200", true},
		{MatchCIDR, "203.0.113.0/24", "203.0.114.1", false},
		{MatchCIDR, "203.0.113.77/24", "203.0.113.1", true}, // host bits are masked
		{MatchCIDR, "10.0.0.0/8", "::ffff:10.1.2.3", true},
		{MatchCIDR, "2001:db8::/32", "2001:db8:1::5", true},
		{MatchCIDR, "2001:db8::/32", "2001:db9::5", false},
		{MatchExact, "2001:db8::1", "2001:db8::1", true},
		{MatchCIDR, "2001:db8::/32", "fe80::1%eth0", false},
		{MatchCIDR, "fe80::/10", "fe80::1%eth0", true},
		{MatchCIDR, "0.0.0.0/0", "198.51.100.1", true},
		{MatchCIDR, "0.0.0.0/0", "2001:db8::1", false},
		{MatchCIDR, "::/0", "2001:db8::1", true},
	}
	for _, tc := range cases {
		s := mustCompile(t, testOptions(), deny("ip", DimIP, tc.match, tc.pattern))
		req := baseRequest()
		req.IP = netip.MustParseAddr(tc.ip)
		if got := verdict(t, s, req) == VerdictDeny; got != tc.want {
			t.Errorf("%s %s vs %s: denied=%v want %v", tc.match, tc.pattern, tc.ip, got, tc.want)
		}
	}
	s := mustCompile(t, testOptions(), Rule{ID: "no-ip", Dimension: DimIP, Match: MatchAbsent, Action: ActionDeny, Enabled: true})
	req := baseRequest()
	req.IP = netip.Addr{}
	if verdict(t, s, req) != VerdictDeny {
		t.Fatal("absent ip rule missed")
	}
}

func windowRule(w Window) Rule {
	r := deny("w", DimAppName, MatchExact, "Infuse")
	r.Window = &w
	return r
}

func TestWindowAbsoluteBoundaries(t *testing.T) {
	from := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	s := mustCompile(t, testOptions(), windowRule(Window{From: from, Until: until}))
	for _, tc := range []struct {
		at   time.Time
		want Verdict
	}{
		{from.Add(-time.Nanosecond), VerdictAllow},
		{from, VerdictDeny},
		{until.Add(-time.Nanosecond), VerdictDeny},
		{until, VerdictAllow},
	} {
		req := baseRequest()
		req.Time = tc.at
		if v := verdict(t, s, req); v != tc.want {
			t.Errorf("at %v: %v, want %v", tc.at, v, tc.want)
		}
	}
}

func TestWindowDailyTimeZoneAndWrap(t *testing.T) {
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	// 22:00–06:00 Taipei time, only for windows opening on Friday.
	s := mustCompile(t, testOptions(), windowRule(Window{DailyStart: "22:00", DailyEnd: "06:00", TimeZone: "Asia/Taipei", Weekdays: []time.Weekday{time.Friday}}))
	at := func(y int, m time.Month, d, h, min, sec int) time.Time {
		return time.Date(y, m, d, h, min, sec, 0, taipei)
	}
	for _, tc := range []struct {
		at   time.Time
		want Verdict
	}{
		{at(2026, 10, 9, 21, 59, 59), VerdictAllow}, // Friday before start
		{at(2026, 10, 9, 22, 0, 0), VerdictDeny},    // start inclusive
		{at(2026, 10, 10, 5, 59, 59), VerdictDeny},  // Saturday morning belongs to Friday's window
		{at(2026, 10, 10, 6, 0, 0), VerdictAllow},   // end exclusive
		{at(2026, 10, 10, 22, 30, 0), VerdictAllow}, // Saturday's own window is not selected
		{at(2026, 10, 9, 3, 0, 0), VerdictAllow},    // Friday early hours belong to Thursday
		// The same instant expressed in UTC (14:00 Friday UTC = 22:00 Taipei).
		{time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC), VerdictDeny},
		{time.Date(2026, 10, 9, 13, 59, 0, 0, time.UTC), VerdictAllow},
	} {
		req := baseRequest()
		req.Time = tc.at
		if v := verdict(t, s, req); v != tc.want {
			t.Errorf("at %v: %v, want %v", tc.at, v, tc.want)
		}
	}

	// A plain daytime window in New York crosses a DST change by wall clock.
	ny := mustCompile(t, testOptions(), windowRule(Window{DailyStart: "09:00", DailyEnd: "17:00", TimeZone: "America/New_York"}))
	for _, tc := range []struct {
		at   time.Time
		want Verdict
	}{
		{time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC), VerdictDeny},  // 09:00 EDT
		{time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC), VerdictAllow},  // 08:00 EST
		{time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC), VerdictDeny},   // 09:00 EST
		{time.Date(2026, 11, 2, 21, 59, 0, 0, time.UTC), VerdictDeny},  // 16:59 EST
		{time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC), VerdictAllow},  // 17:00 EST
		{time.Date(2026, 11, 2, 12, 59, 0, 0, time.UTC), VerdictAllow}, // 07:59 EST
	} {
		req := baseRequest()
		req.Time = tc.at
		if v := verdict(t, ny, req); v != tc.want {
			t.Errorf("NY at %v: %v, want %v", tc.at, v, tc.want)
		}
	}

	allDay := mustCompile(t, testOptions(), windowRule(Window{Weekdays: []time.Weekday{time.Sunday}, TimeZone: "Asia/Taipei", DailyStart: "00:00", DailyEnd: "24:00"}))
	req := baseRequest()
	req.Time = at(2026, 10, 11, 23, 59, 59) // Sunday in Taipei, still Sunday in UTC
	if verdict(t, allDay, req) != VerdictDeny {
		t.Fatal("whole-day Sunday window missed")
	}
	req.Time = at(2026, 10, 12, 0, 30, 0) // Monday in Taipei but Sunday in UTC
	if verdict(t, allDay, req) != VerdictAllow {
		t.Fatal("weekday must follow the window time zone")
	}
}

func TestPriorityAndConflicts(t *testing.T) {
	allow := func(id string, prio int) Rule {
		return Rule{ID: id, Dimension: DimAppName, Match: MatchExact, Pattern: "Infuse", Action: ActionAllow, Priority: prio, Enabled: true}
	}
	denyP := func(id string, prio int) Rule {
		r := deny(id, DimUserAgent, MatchPrefix, "Infuse")
		r.Priority = prio
		return r
	}
	restrict := func(id string, prio int, a Action) Rule {
		r := Rule{ID: id, Dimension: DimDeviceType, Match: MatchExact, Pattern: "tv", Action: a, Priority: prio, Enabled: true}
		return r
	}

	t.Run("higher allow shields lower deny", func(t *testing.T) {
		d := mustCompile(t, testOptions(), denyP("deny", 1), allow("allow", 10)).Evaluate(baseRequest())
		if d.Verdict != VerdictAllow || !slices.Equal(d.Outcome.Rules, []string{"allow"}) || len(d.Matched) != 2 || d.Matched[0].RuleID != "allow" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("higher deny beats lower allow", func(t *testing.T) {
		d := mustCompile(t, testOptions(), denyP("deny", 10), allow("allow", 1)).Evaluate(baseRequest())
		if d.Verdict != VerdictDeny || !slices.Equal(d.Outcome.Rules, []string{"deny"}) {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("deny wins at equal priority", func(t *testing.T) {
		d := mustCompile(t, testOptions(), allow("allow", 5), denyP("deny", 5)).Evaluate(baseRequest())
		if d.Verdict != VerdictDeny {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("restrictions at or above an allow still apply", func(t *testing.T) {
		low := restrict("low-rate", 1, ActionRateLimit)
		low.RateLimit = &RateLimit{Requests: 1, Per: time.Second}
		d := mustCompile(t, testOptions(), allow("allow", 5), restrict("ro", 5, ActionReadOnly), restrict("relog", 9, ActionForceRelogin), low).Evaluate(baseRequest())
		if d.Verdict != VerdictAllow || !d.ReadOnly || !d.ForceRelogin || d.RateLimit != nil || len(d.Matched) != 4 || !slices.Equal(d.Outcome.Rules, []string{"relog", "allow", "ro"}) {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("restrictions below an allow are shielded", func(t *testing.T) {
		d := mustCompile(t, testOptions(), allow("allow", 5), restrict("ro", 1, ActionReadOnly)).Evaluate(baseRequest())
		if d.ReadOnly {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("deny below restrictions still denies", func(t *testing.T) {
		d := mustCompile(t, testOptions(), restrict("ro", 9, ActionReadOnly), denyP("deny", 1)).Evaluate(baseRequest())
		if d.Verdict != VerdictDeny || d.ReadOnly || d.Code != CodeClientBlocked {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("libraries intersect and rate limits keep the strictest", func(t *testing.T) {
		libA := restrict("lib-a", 3, ActionRestrictLibraries)
		libA.Libraries = []string{"m", "s", "k", "m"}
		libB := restrict("lib-b", 2, ActionRestrictLibraries)
		libB.Libraries = []string{"s", "k", "x"}
		slow := restrict("slow", 2, ActionRateLimit)
		slow.RateLimit = &RateLimit{Requests: 100, Per: time.Hour}
		fast := restrict("fast", 4, ActionRateLimit)
		fast.RateLimit = &RateLimit{Requests: 10, Per: time.Minute}
		d := mustCompile(t, testOptions(), libA, libB, slow, fast).Evaluate(baseRequest())
		if !slices.Equal(d.Libraries, []string{"k", "s"}) || d.RateLimit == nil || *d.RateLimit != *slow.RateLimit {
			t.Fatalf("%+v rate=%+v", d, d.RateLimit)
		}
		libC := restrict("lib-c", 1, ActionRestrictLibraries)
		libC.Libraries = []string{"z"}
		d = mustCompile(t, testOptions(), libA, libC).Evaluate(baseRequest())
		if d.Libraries == nil || len(d.Libraries) != 0 || d.Verdict != VerdictAllow {
			t.Fatalf("disjoint restriction must leave no library: %+v", d)
		}
	})
	t.Run("ties are ordered by id", func(t *testing.T) {
		d := mustCompile(t, testOptions(), denyP("b", 1), denyP("a", 1)).Evaluate(baseRequest())
		if !slices.Equal(d.Outcome.Rules, []string{"a", "b"}) {
			t.Fatalf("%+v", d)
		}
	})
}

func TestScopes(t *testing.T) {
	scoped := func(kind ScopeKind, values ...string) Rule {
		r := deny("s", DimAppName, MatchExact, "Infuse")
		r.Scope = Scope{Kind: kind, Values: values}
		return r
	}
	cases := []struct {
		rule Rule
		want Verdict
	}{
		{scoped(ScopeGlobal), VerdictDeny},
		{scoped(ScopeUser, "u1"), VerdictDeny},
		{scoped(ScopeUser, "u2"), VerdictAllow},
		{scoped(ScopeGroup, "kids", "family"), VerdictDeny},
		{scoped(ScopeGroup, "kids"), VerdictAllow},
		{scoped(ScopeLibrary, "lib-movies"), VerdictDeny},
		{scoped(ScopeLibrary, "lib-music"), VerdictAllow},
		{scoped(ScopeClientKind, string(ClientNative)), VerdictDeny},
		{scoped(ScopeClientKind, string(ClientWeb)), VerdictAllow},
	}
	for _, tc := range cases {
		if v := verdict(t, mustCompile(t, testOptions(), tc.rule), baseRequest()); v != tc.want {
			t.Errorf("scope %+v: %v, want %v", tc.rule.Scope, v, tc.want)
		}
	}
	req := baseRequest()
	req.LibraryID = ""
	if v := verdict(t, mustCompile(t, testOptions(), scoped(ScopeLibrary, "lib-movies")), req); v != VerdictAllow {
		t.Fatal("library rule applied to a request without a library")
	}
}

func TestExemptions(t *testing.T) {
	rule := deny("all", DimAppName, MatchExact, "Infuse")
	admin := baseRequest()
	admin.Principal.Admin = true
	local := baseRequest()
	local.IP = netip.MustParseAddr("::1")
	local4 := baseRequest()
	local4.IP = netip.MustParseAddr("::ffff:127.0.0.1")

	def := mustCompile(t, DefaultOptions(), rule)
	for name, req := range map[string]Request{"admin": admin, "loopback6": local, "loopback4-mapped": local4} {
		d := def.Evaluate(req)
		if d.Verdict != VerdictAllow || d.Exempt == ExemptNone || len(d.Matched) != 1 {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if verdict(t, def, baseRequest()) != VerdictDeny {
		t.Fatal("non-exempt request not denied")
	}
	// Behind an untrusted local proxy the loopback address is the proxy's.
	proxied := local
	proxied.Proxied = true
	if d := def.Evaluate(proxied); d.Verdict != VerdictDeny || d.Exempt != ExemptNone {
		t.Errorf("proxied loopback exempted: %+v", d)
	}

	off := mustCompile(t, testOptions(), rule)
	for name, req := range map[string]Request{"admin": admin, "loopback": local} {
		if d := off.Evaluate(req); d.Verdict != VerdictDeny || d.Exempt != ExemptNone {
			t.Errorf("%s with exemptions off: %+v", name, d)
		}
	}
	// Default unknown policy is also lifted for exempt requests.
	o := DefaultOptions()
	o.UnknownClients = UnknownDeny
	admin.Known = false
	if verdict(t, mustCompile(t, o), admin) != VerdictAllow {
		t.Fatal("exempt admin hit unknown-client policy")
	}
}

func TestUnknownClientPolicy(t *testing.T) {
	unknown := baseRequest()
	unknown.Known = false
	for _, tc := range []struct {
		policy   UnknownClientPolicy
		want     Verdict
		code     string
		readOnly bool
	}{
		{UnknownAllow, VerdictAllow, "", false},
		{"", VerdictAllow, "", false},
		{UnknownReadOnly, VerdictAllow, "", true},
		{UnknownDeny, VerdictDeny, CodeClientBlocked, false},
		{UnknownPending, VerdictPending, CodeClientPending, false},
	} {
		o := testOptions()
		o.UnknownClients = tc.policy
		s := mustCompile(t, o)
		d := s.Evaluate(unknown)
		if d.Verdict != tc.want || d.Code != tc.code || d.ReadOnly != tc.readOnly || d.DefaultApplied != (tc.policy != UnknownAllow && tc.policy != "") {
			t.Errorf("%q: %+v", tc.policy, d)
		}
		if v := verdict(t, s, baseRequest()); v != VerdictAllow {
			t.Errorf("%q: known client got %v", tc.policy, v)
		}
		// An allow rule vouches for an otherwise unknown client.
		allowRule := Rule{ID: "ok", Dimension: DimDeviceID, Match: MatchExact, Pattern: "dev-123", Action: ActionAllow, Enabled: true}
		if d := mustCompile(t, o, allowRule).Evaluate(unknown); d.Verdict != VerdictAllow || d.DefaultApplied {
			t.Errorf("%q: allow-listed unknown client: %+v", tc.policy, d)
		}
	}
	o := testOptions()
	o.UnknownClients = "maybe"
	if _, err := Compile(nil, o); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("bad policy: %v", err)
	}
}

func TestObserveAndShadow(t *testing.T) {
	obs := Rule{ID: "obs", Dimension: DimUserAgent, Match: MatchPrefix, Pattern: "Infuse", Action: ActionObserve, Enabled: true}
	sh := Rule{ID: "sh", Dimension: DimDeviceType, Match: MatchExact, Pattern: "tv", Action: ActionShadow, Intent: ActionReadOnly, Enabled: true}
	d := mustCompile(t, testOptions(), obs, sh).Evaluate(baseRequest())
	if d.Verdict != VerdictAllow || d.ReadOnly || len(d.Matched) != 0 {
		t.Fatalf("record-only rules enforced: %+v", d)
	}
	if !slices.Equal(d.Observed, []Hit{{"obs", ActionDeny}}) || !slices.Equal(d.Shadowed, []Hit{{"sh", ActionReadOnly}}) {
		t.Fatalf("hits: %+v %+v", d.Observed, d.Shadowed)
	}
	if d.Simulated == nil || d.Simulated.Verdict != VerdictDeny || !slices.Equal(d.Simulated.Rules, []string{"obs"}) {
		t.Fatalf("simulated: %+v", d.Simulated)
	}
	// A real higher-priority allow still shields the would-be block.
	allow := Rule{ID: "allow", Dimension: DimAppName, Match: MatchExact, Pattern: "Infuse", Action: ActionAllow, Priority: 9, Enabled: true}
	d = mustCompile(t, testOptions(), obs, allow).Evaluate(baseRequest())
	if d.Simulated == nil || d.Simulated.Verdict != VerdictAllow {
		t.Fatalf("simulated with allow: %+v", d.Simulated)
	}
	if d = mustCompile(t, testOptions(), allow).Evaluate(baseRequest()); d.Simulated != nil {
		t.Fatal("simulation without record-only hits")
	}
}

func TestOversizedValuesFailClosed(t *testing.T) {
	o := testOptions()
	o.Limits.MaxValueLen = 64
	s := mustCompile(t, o, deny("r", DimUserAgent, MatchRegex, `bot$`))
	req := baseRequest()
	req.UserAgent = strings.Repeat("x", 65) + "bot"
	if d := s.Evaluate(req); d.Verdict != VerdictDeny || !d.Oversized {
		t.Fatalf("%+v", d)
	}
	req = baseRequest()
	req.Headers = http.Header{"X-Client-Name": slices.Repeat([]string{"a"}, 40)}
	s = mustCompile(t, o, Rule{ID: "h", Dimension: DimHeader, Header: "X-Client-Name", Match: MatchExact, Pattern: "b", Action: ActionDeny, Enabled: true})
	if d := s.Evaluate(req); d.Verdict != VerdictDeny || !d.Oversized {
		t.Fatalf("too many header values: %+v", d)
	}
}

func TestCompileRejectsInvalidAndOversizedRules(t *testing.T) {
	ok := deny("ok", DimUserAgent, MatchExact, "x")
	with := func(f func(*Rule)) Rule {
		r := ok
		f(&r)
		return r
	}
	lim := DefaultLimits()
	cases := map[string]struct {
		rule Rule
		want error
	}{
		"empty id":            {with(func(r *Rule) { r.ID = "" }), ErrInvalidRule},
		"bad dimension":       {with(func(r *Rule) { r.Dimension = "tls" }), ErrInvalidRule},
		"bad match":           {with(func(r *Rule) { r.Match = "fuzzy" }), ErrInvalidRule},
		"empty pattern":       {with(func(r *Rule) { r.Pattern = "" }), ErrInvalidRule},
		"absent with pattern": {with(func(r *Rule) { r.Match = MatchAbsent }), ErrInvalidRule},
		"cidr on ua":          {with(func(r *Rule) { r.Match = MatchCIDR }), ErrInvalidRule},
		"glob on ip":          {with(func(r *Rule) { r.Dimension, r.Match, r.Pattern = DimIP, MatchGlob, "10.*" }), ErrInvalidRule},
		"bad cidr":            {with(func(r *Rule) { r.Dimension, r.Match, r.Pattern = DimIP, MatchCIDR, "10.0.0.0/33" }), ErrInvalidRule},
		"exact with prefix":   {with(func(r *Rule) { r.Dimension, r.Match, r.Pattern = DimIP, MatchExact, "10.0.0.0/8" }), ErrInvalidRule},
		"header missing":      {with(func(r *Rule) { r.Dimension = DimHeader }), ErrInvalidRule},
		"header bad":          {with(func(r *Rule) { r.Dimension, r.Header = DimHeader, "X Bad" }), ErrInvalidRule},
		"header on ua":        {with(func(r *Rule) { r.Header = "X-A" }), ErrInvalidRule},
		"bad regex":           {with(func(r *Rule) { r.Match, r.Pattern = MatchRegex, "(" }), ErrInvalidRule},
		"backreference":       {with(func(r *Rule) { r.Match, r.Pattern = MatchRegex, `(a)\1` }), ErrInvalidRule},
		"huge regex":          {with(func(r *Rule) { r.Match, r.Pattern = MatchRegex, strings.Repeat(`[a-z]{1000}`, 5) }), ErrLimitExceeded},
		"long pattern":        {with(func(r *Rule) { r.Pattern = strings.Repeat("a", lim.MaxPatternLen+1) }), ErrLimitExceeded},
		"long note":           {with(func(r *Rule) { r.Note = strings.Repeat("n", lim.MaxNoteLen+1) }), ErrLimitExceeded},
		"bad action":          {with(func(r *Rule) { r.Action = "explode" }), ErrInvalidRule},
		"intent on deny":      {with(func(r *Rule) { r.Intent = ActionReadOnly }), ErrInvalidRule},
		"observe intent":      {with(func(r *Rule) { r.Action, r.Intent = ActionObserve, ActionShadow }), ErrInvalidRule},
		"libs missing":        {with(func(r *Rule) { r.Action = ActionRestrictLibraries }), ErrInvalidRule},
		"libs on deny":        {with(func(r *Rule) { r.Libraries = []string{"a"} }), ErrInvalidRule},
		"empty lib":           {with(func(r *Rule) { r.Action, r.Libraries = ActionRestrictLibraries, []string{""} }), ErrInvalidRule},
		"shadow libs missing": {with(func(r *Rule) { r.Action, r.Intent = ActionShadow, ActionRestrictLibraries }), ErrInvalidRule},
		"rate missing":        {with(func(r *Rule) { r.Action = ActionRateLimit }), ErrInvalidRule},
		"rate zero":           {with(func(r *Rule) { r.Action, r.RateLimit = ActionRateLimit, &RateLimit{Requests: 1} }), ErrInvalidRule},
		"rate on deny":        {with(func(r *Rule) { r.RateLimit = &RateLimit{1, time.Second} }), ErrInvalidRule},
		"scope kind":          {with(func(r *Rule) { r.Scope = Scope{Kind: "planet"} }), ErrInvalidRule},
		"scope empty":         {with(func(r *Rule) { r.Scope = Scope{Kind: ScopeUser} }), ErrInvalidRule},
		"global values":       {with(func(r *Rule) { r.Scope = Scope{Values: []string{"x"}} }), ErrInvalidRule},
		"window order":        {with(func(r *Rule) { r.Window = &Window{From: noon, Until: noon} }), ErrInvalidRule},
		"window zone":         {with(func(r *Rule) { r.Window = &Window{DailyStart: "01:00", DailyEnd: "02:00", TimeZone: "Mars/Olympus"} }), ErrInvalidRule},
		"window local":        {with(func(r *Rule) { r.Window = &Window{DailyStart: "01:00", DailyEnd: "02:00", TimeZone: "Local"} }), ErrInvalidRule},
		"window half":         {with(func(r *Rule) { r.Window = &Window{DailyStart: "01:00"} }), ErrInvalidRule},
		"window equal":        {with(func(r *Rule) { r.Window = &Window{DailyStart: "01:00", DailyEnd: "01:00"} }), ErrInvalidRule},
		"window clock":        {with(func(r *Rule) { r.Window = &Window{DailyStart: "1:00", DailyEnd: "02:00"} }), ErrInvalidRule},
		"window 24 start":     {with(func(r *Rule) { r.Window = &Window{DailyStart: "24:00", DailyEnd: "02:00"} }), ErrInvalidRule},
		"window minutes":      {with(func(r *Rule) { r.Window = &Window{DailyStart: "01:60", DailyEnd: "02:00"} }), ErrInvalidRule},
		"window weekday":      {with(func(r *Rule) { r.Window = &Window{Weekdays: []time.Weekday{7}} }), ErrInvalidRule},
		"zone without daily":  {with(func(r *Rule) { r.Window = &Window{From: noon, TimeZone: "Asia/Taipei"} }), ErrInvalidRule},
		"disabled invalid":    {with(func(r *Rule) { r.Enabled, r.Pattern = false, "" }), ErrInvalidRule},
	}
	for name, tc := range cases {
		_, err := Compile([]Rule{tc.rule}, testOptions())
		var re *RuleError
		if !errors.Is(err, tc.want) || !errors.As(err, &re) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := Compile([]Rule{ok, ok}, testOptions()); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("duplicate id: %v", err)
	}
}

func TestCompileCountLimits(t *testing.T) {
	o := testOptions()
	o.Limits.MaxRules = 3
	rules := make([]Rule, 4)
	for i := range rules {
		rules[i] = deny(fmt.Sprint(i), DimUserAgent, MatchExact, "x")
	}
	if _, err := Compile(rules, o); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("rule count: %v", err)
	}
	o.Limits.MaxRules = 0
	o.Limits.MaxRegexRules = 2
	for i := range rules {
		rules[i].Match = MatchRegex
	}
	if _, err := Compile(rules, o); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("regex count: %v", err)
	}
	rules[2].Enabled, rules[3].Enabled = false, false
	if _, err := Compile(rules, o); err != nil {
		t.Fatalf("disabled regex rules count against the limit: %v", err)
	}
}

func TestCompileDoesNotAliasInput(t *testing.T) {
	r := Rule{ID: "lib", Dimension: DimAppName, Match: MatchExact, Pattern: "Infuse", Action: ActionRestrictLibraries, Libraries: []string{"a", "b"}, RateLimit: nil, Enabled: true}
	s := mustCompile(t, testOptions(), r)
	r.Libraries[0] = "z"
	d := s.Evaluate(baseRequest())
	if !slices.Equal(d.Libraries, []string{"a", "b"}) {
		t.Fatalf("snapshot aliases caller slice: %v", d.Libraries)
	}
	d.Libraries[0] = "mutated"
	if got := s.Evaluate(baseRequest()).Libraries; got[0] != "a" {
		t.Fatalf("decision aliases snapshot: %v", got)
	}
}

func TestEngineSwapsAtomically(t *testing.T) {
	e := NewEngine(nil)
	if e.Evaluate(baseRequest()).Verdict != VerdictAllow || e.Snapshot().Len() != 0 {
		t.Fatal("empty engine must allow")
	}
	if err := e.Replace([]Rule{deny("d", DimAppName, MatchExact, "Infuse")}, testOptions()); err != nil {
		t.Fatal(err)
	}
	if e.Evaluate(baseRequest()).Verdict != VerdictDeny {
		t.Fatal("replaced rules not in force")
	}
	if err := e.Replace([]Rule{{ID: "bad"}}, testOptions()); err == nil {
		t.Fatal("invalid rule set accepted")
	}
	if e.Evaluate(baseRequest()).Verdict != VerdictDeny {
		t.Fatal("failed replace dropped the previous snapshot")
	}
	if prev := e.Store(nil); prev != e.Snapshot() {
		t.Fatal("nil store changed the snapshot")
	}

	allowAll := mustCompile(t, testOptions())
	denyAll := mustCompile(t, testOptions(), deny("d", DimAppName, MatchExact, "Infuse"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if j%2 == 0 {
					e.Store(allowAll)
				} else {
					e.Store(denyAll)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if v := e.Evaluate(baseRequest()).Verdict; v != VerdictAllow && v != VerdictDeny {
					t.Errorf("verdict %q", v)
				}
			}
		}()
	}
	wg.Wait()
}

func TestZeroTimeUsesClock(t *testing.T) {
	r := deny("future", DimAppName, MatchExact, "Infuse")
	r.Window = &Window{From: time.Now().Add(time.Hour)}
	req := baseRequest()
	req.Time = time.Time{}
	if verdict(t, mustCompile(t, testOptions(), r), req) != VerdictAllow {
		t.Fatal("future window applied to a zero-time request")
	}
}

func TestSnapshotUses(t *testing.T) {
	s := mustCompile(t, testOptions(), deny("a", DimAPIKey, MatchExact, "sha256:1"), Rule{ID: "b", Dimension: DimUserAgent, Match: MatchExact, Pattern: "x", Action: ActionDeny})
	if !s.Uses(DimAPIKey) || s.Uses(DimUserAgent) || s.Uses(DimIP) {
		t.Fatal("Uses must report only enabled rule dimensions")
	}
}
