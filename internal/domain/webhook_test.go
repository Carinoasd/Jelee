package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var webhookNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func webhookSecret(fill byte) WebhookSecret { return WebhookSecret(bytes.Repeat([]byte{fill}, 32)) }

func TestWebhookEventCatalogueCoversG12_1(t *testing.T) {
	want := []string{
		"media.added", "media.updated", "media.deleted",
		"scan.started", "scan.completed", "scan.failed",
		"playback.started", "playback.paused", "playback.progress", "playback.stopped",
		"user.login", "user.login_failed", "user.locked",
		"session.created", "session.ended",
		"nfo.written", "images.fetched", "system.alert",
	}
	got := WebhookEventTypes()
	if len(got) != len(want) {
		t.Fatalf("catalogue has %d types, want %d", len(got), len(want))
	}
	for i, name := range want {
		if string(got[i]) != name {
			t.Fatalf("type %d = %q, want %q", i, got[i], name)
		}
		parsed, ok := ParseWebhookEventType(name)
		if !ok || parsed != got[i] {
			t.Fatalf("parse %q failed", name)
		}
		if _, ok := WebhookSubjectKindFor(parsed); !ok {
			t.Fatalf("%q has no subject kind", name)
		}
	}
	got[0] = "mutated"
	if WebhookEventTypes()[0] != WebhookMediaAdded {
		t.Fatal("WebhookEventTypes exposes internal storage")
	}
	for _, bad := range []string{"", "media", "Media.Added", "transcode.started"} {
		if _, ok := ParseWebhookEventType(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
}

func TestWebhookEnvelopeJSON(t *testing.T) {
	occurred := time.Date(2026, 10, 4, 20, 0, 0, 123456789, time.FixedZone("CST", 8*3600))
	event, err := NewWebhookEvent("01J9ABCDEF", WebhookMediaAdded, occurred, WebhookSubject{Kind: WebhookSubjectItem, ID: "item-1"}, map[string]any{"title": "Film"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"eventId":"01J9ABCDEF","type":"media.added","version":1,"occurredAt":"2026-10-04T12:00:00.123Z","subject":{"kind":"item","id":"item-1"},"data":{"title":"Film"}}`
	if string(raw) != want {
		t.Fatalf("envelope\n got %s\nwant %s", raw, want)
	}
	var back WebhookEvent
	if err := json.Unmarshal(raw, &back); err != nil || back.Validate() != nil || back.EventID != event.EventID {
		t.Fatalf("round trip: %v %v", err, back.Validate())
	}
}

func TestWebhookEnvelopeValidation(t *testing.T) {
	item := WebhookSubject{Kind: WebhookSubjectItem, ID: "i"}
	cases := []struct {
		name    string
		id      string
		typ     WebhookEventType
		at      time.Time
		subject WebhookSubject
		data    map[string]any
	}{
		{"empty id", "", WebhookMediaAdded, webhookNow, item, nil},
		{"id with slash", "a/b", WebhookMediaAdded, webhookNow, item, nil},
		{"id too long", strings.Repeat("a", 65), WebhookMediaAdded, webhookNow, item, nil},
		{"unknown type", "e1", "media.moved", webhookNow, item, nil},
		{"zero time", "e1", WebhookMediaAdded, time.Time{}, item, nil},
		{"wrong subject kind", "e1", WebhookMediaAdded, webhookNow, WebhookSubject{Kind: WebhookSubjectUser, ID: "u"}, nil},
		{"missing subject id", "e1", WebhookMediaAdded, webhookNow, WebhookSubject{Kind: WebhookSubjectItem}, nil},
		{"credential subject id", "e1", WebhookMediaAdded, webhookNow, WebhookSubject{Kind: WebhookSubjectItem, ID: "x?token=1"}, nil},
		{"unsupported value", "e1", WebhookMediaAdded, webhookNow, item, map[string]any{"ch": make(chan int)}},
		{"nan", "e1", WebhookMediaAdded, webhookNow, item, map[string]any{"n": nanValue()}},
		{"too deep", "e1", WebhookMediaAdded, webhookNow, item, map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{}}}}}},
		{"bad utf8", "e1", WebhookMediaAdded, webhookNow, item, map[string]any{"s": "\xff"}},
	}
	for _, c := range cases {
		if _, err := NewWebhookEvent(c.id, c.typ, c.at, c.subject, c.data); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err=%v, want ErrInvalid", c.name, err)
		}
	}
	if _, err := NewWebhookEvent("e1", WebhookSystemAlert, webhookNow, WebhookSubject{Kind: WebhookSubjectSystem}, nil); err != nil {
		t.Fatalf("system event without subject id: %v", err)
	}
	// A stored event whose data predates current redaction rules is refused.
	stale := WebhookEvent{EventID: "e1", Type: WebhookUserLogin, Version: 1, OccurredAt: webhookNow,
		Subject: WebhookSubject{Kind: WebhookSubjectUser, ID: "u"}, Data: map[string]any{"accessToken": "abc"}}
	if !errors.Is(stale.Validate(), ErrInvalid) {
		t.Fatal("unredacted stored event validated")
	}
	future := stale
	future.Data, future.Version = nil, WebhookEventVersion+1
	if !errors.Is(future.Validate(), ErrInvalid) {
		t.Fatal("unknown version validated")
	}
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

func TestRedactWebhookData(t *testing.T) {
	input := map[string]any{
		"title":        "Film",
		"password":     "hunter2",
		"newPassword":  map[string]any{"value": "x"},
		"access_token": "abc",
		"API-Key":      123,
		"cookie":       "sid=1",
		"passwordHash": "$argon2id$",
		"ipAddress":    "203.0.113.9",
		"deviceId":     "dev",
		"path":         "/srv/media/Movies/Film (2020)/Film.mkv",
		"nfoFile":      "Movies/Film/movie.nfo",
		"windows":      `D:\Media\Show\S01E01.mkv`,
		"unc":          `\\nas\share\a.mkv`,
		"fileUrl":      "file:///srv/a.mkv",
		"home":         "~/videos/a.mkv",
		"imageUrl":     "https://img.example/x.jpg?api_key=secret",
		"userinfo":     "https://user:pass@example.com/hook",
		"header":       "Authorization: Bearer abc",
		"legacyHeader": "X-Client-Token: abc",
		"pem":          "-----BEGIN PRIVATE KEY-----",
		"director":     "AC/DC",
		"profile":      "main/high",
		"genres":       []string{"Drama", "/abs/genre"},
		"nested":       map[string]any{"list": []any{map[string]any{"secret": 1, "ok": true}}},
		"count":        int64(3),
		"ratio":        0.5,
		"empty":        nil,
	}
	got, err := RedactWebhookData(input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"title":        "Film",
		"password":     WebhookRedacted,
		"newPassword":  WebhookRedacted,
		"access_token": WebhookRedacted,
		"API-Key":      WebhookRedacted,
		"cookie":       WebhookRedacted,
		"passwordHash": WebhookRedacted,
		"ipAddress":    WebhookRedacted,
		"deviceId":     WebhookRedacted,
		"path":         "Film.mkv",
		"nfoFile":      "movie.nfo",
		"windows":      "S01E01.mkv",
		"unc":          "a.mkv",
		"fileUrl":      "a.mkv",
		"home":         "a.mkv",
		"imageUrl":     WebhookRedacted,
		"userinfo":     WebhookRedacted,
		"header":       WebhookRedacted,
		"legacyHeader": WebhookRedacted,
		"pem":          WebhookRedacted,
		"director":     "AC/DC",
		"profile":      "main/high",
		"genres":       []any{"Drama", "genre"},
		"nested":       map[string]any{"list": []any{map[string]any{"secret": WebhookRedacted, "ok": true}}},
		"count":        int64(3),
		"ratio":        0.5,
		"empty":        nil,
	}
	if !webhookDataEqual(got, want) {
		gj, _ := json.Marshal(got)
		t.Fatalf("redacted:\n%s", gj)
	}
	if input["password"] != "hunter2" {
		t.Fatal("input mutated")
	}
	raw, _ := json.Marshal(got)
	for _, leak := range []string{"hunter2", "/srv", `D:\\`, "nas", "api_key", "pass@", "Bearer", "203.0.113"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("redacted payload leaks %q: %s", leak, raw)
		}
	}
	long, _ := RedactWebhookData(map[string]any{"s": strings.Repeat("é", 2000)})
	if s := long["s"].(string); len(s) > 2048 || !strings.HasPrefix(s, "é") || strings.ToValidUTF8(s, "") != s {
		t.Fatalf("long string not truncated on a rune boundary: %d", len(s))
	}
	if got, err := RedactWebhookData(nil); got != nil || err != nil {
		t.Fatal("nil data")
	}
	again, _ := RedactWebhookData(got)
	if !webhookDataEqual(again, got) {
		t.Fatal("redaction is not idempotent")
	}
}

func signingKeys() WebhookSigningKeys { return WebhookSigningKeys{Current: webhookSecret('a')} }

func TestWebhookSignatureRoundTrip(t *testing.T) {
	body := []byte(`{"eventId":"e1"}`)
	h, err := SignWebhook(signingKeys(), body, webhookNow)
	if err != nil {
		t.Fatal(err)
	}
	if h.Timestamp != fmt.Sprint(webhookNow.Unix()) || !strings.HasPrefix(h.Signature, "v1=") || len(h.Signature) != 3+64 {
		t.Fatalf("headers %+v", h)
	}
	// Known-answer vector computed independently (Python hmac):
	// HMAC-SHA256(key=32*'a', "1791115200.{\"eventId\":\"e1\"}").
	if h.Timestamp != "1791115200" || h.Signature != "v1=51ae7e8875b5cdea4451bb31675001629f222755dcb884ba1497c3508e425d72" {
		t.Fatalf("known-answer mismatch: %+v", h)
	}
	if err := VerifyWebhookSignature([]WebhookSecret{webhookSecret('a')}, h, body, webhookNow.Add(time.Minute), 0); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Deterministic: same input, same signature.
	h2, _ := SignWebhook(signingKeys(), body, webhookNow)
	if h2 != h {
		t.Fatal("signature not deterministic")
	}
}

func TestWebhookSignatureRejects(t *testing.T) {
	body := []byte(`{"eventId":"e1","type":"media.added"}`)
	secrets := []WebhookSecret{webhookSecret('a')}
	h, _ := SignWebhook(signingKeys(), body, webhookNow)
	tampered := bytes.Replace(body, []byte("added"), []byte("deled"), 1)
	cases := []struct {
		name string
		h    WebhookSignedHeaders
		body []byte
		now  time.Time
		sec  []WebhookSecret
		want error
	}{
		{"tampered body", h, tampered, webhookNow, secrets, ErrWebhookSignatureMismatch},
		{"tampered timestamp", WebhookSignedHeaders{Timestamp: fmt.Sprint(webhookNow.Unix() + 1), Signature: h.Signature}, body, webhookNow, secrets, ErrWebhookSignatureMismatch},
		{"wrong secret", h, body, webhookNow, []WebhookSecret{webhookSecret('b')}, ErrWebhookSignatureMismatch},
		{"no secrets", h, body, webhookNow, nil, ErrWebhookSignatureMismatch},
		{"flipped hex", WebhookSignedHeaders{Timestamp: h.Timestamp, Signature: h.Signature[:len(h.Signature)-1] + flipHex(h.Signature[len(h.Signature)-1])}, body, webhookNow, secrets, ErrWebhookSignatureMismatch},
		{"expired", h, body, webhookNow.Add(DefaultWebhookReplayWindow + time.Second), secrets, ErrWebhookTimestampExpired},
		{"from the future", h, body, webhookNow.Add(-DefaultWebhookReplayWindow - time.Second), secrets, ErrWebhookTimestampExpired},
		{"missing signature", WebhookSignedHeaders{Timestamp: h.Timestamp}, body, webhookNow, secrets, ErrWebhookSignatureMissing},
		{"missing timestamp", WebhookSignedHeaders{Signature: h.Signature}, body, webhookNow, secrets, ErrWebhookSignatureMissing},
		{"non-decimal timestamp", WebhookSignedHeaders{Timestamp: "+" + h.Timestamp, Signature: h.Signature}, body, webhookNow, secrets, ErrWebhookSignatureMalformed},
		{"short mac", WebhookSignedHeaders{Timestamp: h.Timestamp, Signature: "v1=abcd"}, body, webhookNow, secrets, ErrWebhookSignatureMalformed},
		{"unknown scheme only", WebhookSignedHeaders{Timestamp: h.Timestamp, Signature: "v0=" + h.Signature[3:]}, body, webhookNow, secrets, ErrWebhookSignatureMalformed},
		{"too many entries", WebhookSignedHeaders{Timestamp: h.Timestamp, Signature: strings.Repeat(h.Signature+",", 5) + h.Signature}, body, webhookNow, secrets, ErrWebhookSignatureMalformed},
	}
	for _, c := range cases {
		if err := VerifyWebhookSignature(c.sec, c.h, c.body, c.now, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: err=%v, want %v", c.name, err, c.want)
		}
	}
	// Window boundaries are inclusive; a custom window is honored.
	if err := VerifyWebhookSignature(secrets, h, body, webhookNow.Add(DefaultWebhookReplayWindow), 0); err != nil {
		t.Fatalf("at window edge: %v", err)
	}
	if err := VerifyWebhookSignature(secrets, h, body, webhookNow.Add(31*time.Second), 30*time.Second); !errors.Is(err, ErrWebhookTimestampExpired) {
		t.Fatalf("custom window: %v", err)
	}
}

func flipHex(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}

func TestWebhookReplayWithinWindowIsDeduplicated(t *testing.T) {
	body := []byte(`{"eventId":"evt-1"}`)
	secrets := []WebhookSecret{webhookSecret('a')}
	h, _ := SignWebhook(signingKeys(), body, webhookNow)
	dedup := NewWebhookDedup(0)
	receive := func(at time.Time) (accepted, duplicate bool) {
		if VerifyWebhookSignature(secrets, h, body, at, 0) != nil {
			return false, false
		}
		return true, dedup.Seen("evt-1", at)
	}
	if ok, dup := receive(webhookNow); !ok || dup {
		t.Fatal("first delivery")
	}
	if ok, dup := receive(webhookNow.Add(2 * time.Minute)); !ok || !dup {
		t.Fatal("verbatim replay inside window not flagged as duplicate")
	}
	if ok, _ := receive(webhookNow.Add(10 * time.Minute)); ok {
		t.Fatal("verbatim replay after window accepted")
	}
	// Memory is bounded by the window.
	dedup.Seen("evt-2", webhookNow.Add(30*time.Minute))
	if dedup.Len() != 1 {
		t.Fatalf("dedup kept %d ids", dedup.Len())
	}
	// A clock that jumps back does not keep stale entries forever.
	dedup.Seen("evt-3", webhookNow.Add(-time.Hour))
	if dedup.Len() != 1 {
		t.Fatalf("dedup after clock rollback kept %d ids", dedup.Len())
	}
}

func TestWebhookKeyRotation(t *testing.T) {
	old, cur := webhookSecret('o'), webhookSecret('n')
	keys := WebhookSigningKeys{Current: cur, Previous: old, PreviousUntil: webhookNow.Add(time.Hour)}
	body := []byte("{}")
	h, err := SignWebhook(keys, body, webhookNow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(h.Signature, "v1=") != 2 {
		t.Fatalf("rotation should dual-sign: %s", h.Signature)
	}
	for _, s := range []WebhookSecret{old, cur} {
		if err := VerifyWebhookSignature([]WebhookSecret{s}, h, body, webhookNow, 0); err != nil {
			t.Fatalf("consumer on one secret rejected rotation signature: %v", err)
		}
	}
	after, _ := SignWebhook(keys, body, webhookNow.Add(time.Hour))
	if strings.Count(after.Signature, "v1=") != 1 || VerifyWebhookSignature([]WebhookSecret{old}, after, body, webhookNow.Add(time.Hour), 0) == nil {
		t.Fatal("previous secret still signs after its deadline")
	}
	for _, bad := range []WebhookSigningKeys{
		{},
		{Current: WebhookSecret("short")},
		{Current: cur, Previous: old},
		{Current: cur, Previous: cur, PreviousUntil: webhookNow},
		{Current: cur, Previous: WebhookSecret("short"), PreviousUntil: webhookNow},
	} {
		if _, err := SignWebhook(bad, body, webhookNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid keys signed: %v", err)
		}
	}
	if s := fmt.Sprintf("%v %+v %#v %s", keys, keys, keys, cur); strings.Contains(s, "nnnn") || strings.Contains(s, "110") {
		t.Fatalf("secret printed: %s", s)
	}
}

func TestWebhookRetrySchedule(t *testing.T) {
	p := WebhookRetryPolicy{MaxAttempts: 8, BaseDelay: 10 * time.Second, MaxDelay: 5 * time.Minute, Jitter: 0.2}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	mid := func() float64 { return 0.5 } // factor 1.0
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := p.Delay(i+1, mid); got != w {
			t.Fatalf("attempt %d delay %v, want %v", i+1, got, w)
		}
	}
	// Jitter bounds: [0.8, 1.2) of the base value, never above MaxDelay.
	if got := p.Delay(2, func() float64 { return 0 }); got != 16*time.Second {
		t.Fatalf("low jitter %v", got)
	}
	if got := p.Delay(2, func() float64 { return 0.999999 }); got < 23*time.Second || got >= 24*time.Second {
		t.Fatalf("high jitter %v", got)
	}
	if got := p.Delay(10, func() float64 { return 0.999999 }); got != p.MaxDelay {
		t.Fatalf("capped jitter %v", got)
	}
	for _, r := range []float64{-1, 2, nanValue()} {
		if got := p.Delay(1, func() float64 { return r }); got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("hostile jitter %v gave %v", r, got)
		}
	}
	// Huge attempt numbers do not overflow.
	if got := p.Delay(1000, nil); got != p.MaxDelay {
		t.Fatalf("overflow %v", got)
	}
	// Full sequence through DecideWebhookRetry ends in dead-letter.
	at := webhookNow
	timeout := WebhookOutcome{Kind: WebhookOutcomeTimeout}
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		d := DecideWebhookRetry(p, attempt, timeout, at, mid)
		if attempt == p.MaxAttempts {
			if d.State != WebhookDeliveryDead || !d.NextAt.IsZero() {
				t.Fatalf("attempt %d: %+v, want dead", attempt, d)
			}
			break
		}
		if d.State != WebhookDeliveryPending || d.NextAt != at.Add(d.Delay) || d.Delay != p.Delay(attempt, mid) {
			t.Fatalf("attempt %d: %+v", attempt, d)
		}
		at = d.NextAt
	}
	if d := DecideWebhookRetry(p, 1, WebhookOutcome{Kind: WebhookOutcomeDelivered, StatusCode: 204}, at, mid); d.State != WebhookDeliveryDelivered {
		t.Fatalf("success: %+v", d)
	}
	// Retry-After lengthens but is capped.
	if d := DecideWebhookRetry(p, 1, WebhookOutcome{Kind: WebhookOutcomeHTTP, StatusCode: 429, RetryAfter: time.Minute}, at, mid); d.Delay != time.Minute {
		t.Fatalf("retry-after: %+v", d)
	}
	if d := DecideWebhookRetry(p, 1, WebhookOutcome{Kind: WebhookOutcomeHTTP, StatusCode: 503, RetryAfter: 48 * time.Hour}, at, mid); d.Delay != p.MaxDelay {
		t.Fatalf("retry-after cap: %+v", d)
	}
	if d := DecideWebhookRetry(p, 1, WebhookOutcome{Kind: WebhookOutcomeHTTP, StatusCode: 503, RetryAfter: time.Second}, at, mid); d.Delay != 10*time.Second {
		t.Fatalf("short retry-after shortened delay: %+v", d)
	}
}

func TestWebhookOutcomeClassification(t *testing.T) {
	retry := map[WebhookOutcome]bool{
		{Kind: WebhookOutcomeTimeout}:                    true,
		{Kind: WebhookOutcomeNetwork}:                    true,
		{Kind: WebhookOutcomeHTTP, StatusCode: 500}:      true,
		{Kind: WebhookOutcomeHTTP, StatusCode: 503}:      true,
		{Kind: WebhookOutcomeHTTP, StatusCode: 429}:      true,
		{Kind: WebhookOutcomeHTTP, StatusCode: 408}:      true,
		{Kind: WebhookOutcomeHTTP, StatusCode: 400}:      false,
		{Kind: WebhookOutcomeHTTP, StatusCode: 401}:      false,
		{Kind: WebhookOutcomeHTTP, StatusCode: 410}:      false,
		{Kind: WebhookOutcomeHTTP, StatusCode: 302}:      false,
		{Kind: WebhookOutcomeBlocked}:                    false,
		{Kind: WebhookOutcomeTLS}:                        false,
		{Kind: WebhookOutcomeInvalid}:                    false,
		{Kind: WebhookOutcomeDelivered, StatusCode: 200}: false,
	}
	p := DefaultWebhookRetryPolicy()
	for o, want := range retry {
		if o.Retryable() != want {
			t.Errorf("%+v retryable=%v", o, !want)
		}
		if o.Kind != WebhookOutcomeDelivered && !want {
			if d := DecideWebhookRetry(p, 1, o, webhookNow, nil); d.State != WebhookDeliveryDead {
				t.Errorf("%+v not dead-lettered: %+v", o, d)
			}
		}
	}
}

func TestWebhookRetryPolicyValidation(t *testing.T) {
	if err := DefaultWebhookRetryPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	base := DefaultWebhookRetryPolicy()
	for _, mutate := range []func(*WebhookRetryPolicy){
		func(p *WebhookRetryPolicy) { p.MaxAttempts = 0 },
		func(p *WebhookRetryPolicy) { p.MaxAttempts = MaxWebhookAttempts + 1 },
		func(p *WebhookRetryPolicy) { p.BaseDelay = time.Millisecond },
		func(p *WebhookRetryPolicy) { p.MaxDelay = p.BaseDelay - 1 },
		func(p *WebhookRetryPolicy) { p.MaxDelay = 25 * time.Hour },
		func(p *WebhookRetryPolicy) { p.Jitter = -0.1 },
		func(p *WebhookRetryPolicy) { p.Jitter = 1.1 },
		func(p *WebhookRetryPolicy) { p.Jitter = nanValue() },
	} {
		p := base
		mutate(&p)
		if !errors.Is(p.Validate(), ErrInvalid) {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestWebhookHeadersAndFilter(t *testing.T) {
	if err := ValidateWebhookHeaders(map[string]string{"X-Team": "media", "Authorization-Hint": "a\tb"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"X-Jelee-Signature": "x"},
		{"x-jelee-custom": "x"},
		{"Content-Type": "text/plain"},
		{"Host": "evil"},
		{"Bad Header": "x"},
		{"X-A": "line\r\nInjected: 1"},
		{"X-A": strings.Repeat("a", 1025)},
		{"X-A": "a", "x-a": "b"},
	} {
		if !errors.Is(ValidateWebhookHeaders(bad), ErrInvalid) {
			t.Errorf("accepted %v", bad)
		}
	}
	f, err := NormalizeWebhookFilter([]WebhookEventType{WebhookScanFailed, WebhookMediaAdded, WebhookScanFailed})
	if err != nil || len(f) != 2 || f[0] != WebhookMediaAdded {
		t.Fatalf("filter %v %v", f, err)
	}
	if _, err := NormalizeWebhookFilter([]WebhookEventType{"nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown filter type accepted")
	}
	if !WebhookFilterMatches(nil, WebhookSystemAlert) || WebhookFilterMatches(nil, "nope") ||
		!WebhookFilterMatches(f, WebhookMediaAdded) || WebhookFilterMatches(f, WebhookUserLogin) {
		t.Fatal("filter matching")
	}
}
