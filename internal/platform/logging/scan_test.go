package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sensitiveSamples are realistic secrets injected through every logging
// path. needles are the fragments that must never appear in any output.
var sensitiveSamples = []struct {
	name, value string
	needles     []string
}{
	{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJqZWxlZSJ9.c2lnbmF0dXJlU2VjcmV0", []string{"eyJhbGciOiJIUzI1NiJ9", "c2lnbmF0dXJlU2VjcmV0"}},
	{"apiKey", "jk_live_8f3a9c2e7b1d4f6a0c5e9b2d7f1a3c8e", []string{"8f3a9c2e7b1d4f6a0c5e9b2d7f1a3c8e"}},
	{"password", "Hunter2!Passw0rd", []string{"Hunter2", "Passw0rd"}},
	{"dsn", "postgres://jelee:S3cretDbPass@db.internal:5432/jelee?sslmode=disable", []string{"S3cretDbPass", "db.internal", "postgres://"}},
	{"cookie", "jelee_session=Zm9vYmFyc2VjcmV0Y29va2ll; Path=/; HttpOnly", []string{"Zm9vYmFyc2VjcmV0Y29va2ll", "jelee_session="}},
	{"authorization", "Bearer jelee-access-token-9f8e7d6c5b4a", []string{"9f8e7d6c5b4a", "Bearer "}},
	{"basicAuth", "Basic amVsZWU6c2VjcmV0", []string{"amVsZWU6c2VjcmV0"}},
	{"unixPath", "/srv/media/Private Collection/secret-movie.mkv", []string{"/srv/media"}},
	{"windowsPath", `C:\Users\someone\Videos\private-film.mkv`, []string{`C:\`, `C:\\`, "someone"}},
	{"webhookSecret", "whsec_Q2xhdWRlU2VjcmV0V2ViaG9vaw", []string{"Q2xhdWRlU2VjcmV0V2ViaG9vaw"}},
	{"ipv4", "203.0.113.77", []string{"203.0.113.77"}},
	{"ipv6", "2001:db8:85a3::8a2e:370:7334", []string{"8a2e:370:7334"}},
	{"ipPort", "198.51.100.23:51234", []string{"198.51.100.23"}},
}

// defaultOnlyNeedles may legitimately appear once an operator opts into
// relative paths, but never under the default redaction.
var defaultOnlyNeedles = []string{"secret-movie", "Private Collection"}

// scanKeys covers credential-looking keys, arbitrary keys and every
// whitelisted key, whose values are validated rather than trusted.
var scanKeys = []string{
	"token", "accessToken", "password", "apiKey", "api_key", "secret", "webhookSecret", "cookie", "Cookie", "Set-Cookie",
	"authorization", "Authorization", "dsn", "database", "databaseUrl", "path", "file", "clientIp", "ip", "remoteAddr",
	"error", "query", "url", "userAgent", "reason", "libraryId",
	"msg", "time", "level", "component", "requestId", "method", "status", "durationMs", "event", "count", "taskId", "state", "code",
}

type stringer struct{ s string }

func (s stringer) String() string { return s.s }

type valuer struct{ s string }

func (v valuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("token", v.s), slog.String("path", v.s), slog.Any("nested", valuerString{v.s}))
}

type valuerString struct{ s string }

func (v valuerString) LogValue() slog.Value { return slog.StringValue(v.s) }

type credentials struct {
	User, Password string
	Header         map[string]string
}

// inject writes the sample through every attribute shape slog supports.
func inject(log *slog.Logger, sample string) {
	ctx := context.Background()
	for _, key := range scanKeys {
		log.Info("scan probe", key, sample)
		log.Warn("scan probe", "component", "http", key, sample)
		log.With(key, sample).Error("scan probe scoped")
		log.WithGroup("request").With(key, sample).Info("scan probe grouped")
		log.LogAttrs(ctx, slog.LevelInfo, "scan probe attrs", slog.Group("outer", slog.Group("inner", slog.String(key, sample))))
	}
	log.Info(sample)
	log.Info("login failed for " + sample)
	log.Info(fmt.Sprintf("request to %s failed", sample))
	odd := []any{sample} // a dangling value becomes slog's !BADKEY attribute
	log.Info("scan probe odd", odd...)
	log.Info("scan probe key", sample, "value")
	log.LogAttrs(ctx, slog.LevelInfo, "scan probe group name", slog.Group(sample, slog.String("count", sample)))
	log.WithGroup(sample).Info("scan probe handler group", "count", 1)
	log.Info("scan probe any",
		"map", map[string]string{"Authorization": sample, "Cookie": sample},
		"struct", credentials{User: "jelee", Password: sample, Header: map[string]string{"Authorization": sample}},
		"slice", []string{sample, sample},
		"error", errors.New("connect "+sample+": refused"),
		"wrapped", fmt.Errorf("open %q: %w", sample, os.ErrPermission),
		"stringer", stringer{sample},
		"valuer", valuer{sample},
		"bytes", []byte(sample),
	)
	log.Info("scan probe valuer", "path", valuerString{sample}, "clientIp", valuerString{sample}, "requestId", valuerString{sample})
	log.Debug("scan probe debug", "token", sample)
}

func assertNoLeak(t *testing.T, handler, output string, needles []string) {
	t.Helper()
	if !strings.Contains(output, "scan probe") {
		t.Fatalf("%s produced no scan output", handler)
	}
	for _, needle := range needles {
		if strings.Contains(output, needle) {
			i := strings.Index(output, needle)
			start, end := max(0, i-120), min(len(output), i+len(needle)+40)
			t.Errorf("%s leaked %q near: %s", handler, needle, output[start:end])
		}
	}
}

type captureForwarder struct{ syncBuffer }

func (*captureForwarder) Kind() ForwarderKind           { return ForwardLoki }
func (*captureForwarder) Flush(context.Context) error   { return nil }
func (*captureForwarder) Close() error                  { return nil }
func (c *captureForwarder) Write(p []byte) (int, error) { return c.syncBuffer.Write(p) }

func TestSensitiveSampleScanFindsNothing(t *testing.T) {
	type target struct {
		name  string
		opts  Options
		modes bool // non-default IP/path options are active
	}
	targets := []target{
		{name: "json", opts: Options{Level: slog.LevelDebug}},
		{name: "console", opts: Options{Level: slog.LevelDebug, Format: FormatConsole}},
		{name: "json+mask+relative", opts: Options{Level: slog.LevelDebug, IPMode: IPMask, PathMode: PathRelative, PathRoots: []string{"/srv/media", `C:\Users\someone`}}, modes: true},
		{name: "console+mask+relative", opts: Options{Level: slog.LevelDebug, Format: FormatConsole, IPMode: IPMask, PathMode: PathRelative, PathRoots: []string{"/srv/media"}}, modes: true},
	}
	for _, tc := range targets {
		for _, output := range []Output{OutputStdout, OutputBoth} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, output), func(t *testing.T) {
				opts := tc.opts
				opts.Output = output
				opts.BufferEntries = 1 << 16
				file := filepath.Join(t.TempDir(), "jelee.log")
				opts.File = RotateOptions{Path: file, MaxBytes: 1 << 30, MaxBackups: 1}
				forwarder := &captureForwarder{}
				opts.Forwarders = []Forwarder{forwarder}
				r, stdout := openTest(t, opts)
				for _, sample := range sensitiveSamples {
					inject(r.Logger(), sample.value)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				if r.Dropped() != 0 {
					t.Fatalf("scan lost %d records", r.Dropped())
				}
				outputs := map[string]string{"stdout": stdout.String(), "forwarder": forwarder.String()}
				if output == OutputBoth {
					data, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					outputs["file"] = string(data)
				}
				for sink, text := range outputs {
					for _, sample := range sensitiveSamples {
						needles := sample.needles
						if !tc.modes {
							needles = append(append([]string(nil), needles...), defaultOnlyNeedles...)
						}
						assertNoLeak(t, sink, text, needles)
					}
				}
			})
		}
	}
	t.Run("legacy New", func(t *testing.T) {
		var out syncBuffer
		log := New(&out)
		for _, sample := range sensitiveSamples {
			inject(log, sample.value)
		}
		for _, sample := range sensitiveSamples {
			assertNoLeak(t, "New", out.String(), append(append([]string(nil), sample.needles...), defaultOnlyNeedles...))
		}
	})
}

func TestIPMaskOption(t *testing.T) {
	r, out := openTest(t, Options{IPMode: IPMask})
	for _, ip := range []string{"203.0.113.77", "198.51.100.23:51234", "2001:db8:85a3::8a2e:370:7334", "::ffff:192.0.2.9", "[2001:db8::1]:443", "not-an-ip", "fe80::1%eth0"} {
		r.Logger().Info("client seen", "clientIp", ip)
	}
	got := flushed(t, r, out)
	for _, want := range []string{`"clientIp":"203.0.113.0/24"`, `"clientIp":"198.51.100.0/24"`, `"clientIp":"2001:db8:85a3::/48"`, `"clientIp":"192.0.2.0/24"`, `"clientIp":"2001:db8::/48"`, `"clientIp":"fe80::/48"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Count(got, `"clientIp":"[redacted]"`) != 1 || strings.Contains(got, "eth0") {
		t.Errorf("invalid address handling: %s", got)
	}
	r2, out2 := openTest(t, Options{})
	r2.Logger().Info("client seen", "clientIp", "203.0.113.77")
	if got := flushed(t, r2, out2); !strings.Contains(got, `"clientIp":"[redacted]"`) {
		t.Errorf("IP not redacted by default: %s", got)
	}
}

func TestRelativePathOption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	r, out := openTest(t, Options{PathMode: PathRelative, PathRoots: []string{root, "relative/root", ""}})
	cases := map[string]string{
		filepath.Join(root, "Movies", "A (2020)", "a.mkv"):  `"path":"Movies/A (2020)/a.mkv"`,
		filepath.Join(root, "..", "library-other", "b.mkv"): `"path":"[redacted]"`,
		root:                       `"path":"[redacted]"`,
		filepath.Join(root, ".."):  `"path":"[redacted]"`,
		"/etc/passwd":              `"path":"[redacted]"`,
		"Movies/relative.mkv":      `"path":"[redacted]"`,
		root + "/x/../../escape":   `"path":"[redacted]"`,
		root + "/line\nbreak.mkv":  `"path":"[redacted]"`,
		root + "-sibling/file.mkv": `"path":"[redacted]"`,
	}
	for input, want := range cases {
		r.Logger().Info("path seen", "path", input)
		if got := flushed(t, r, out); !strings.Contains(got, want) {
			t.Errorf("path %q => %s want %s", input, got, want)
		}
	}
	r.Logger().Info("path seen", "file", filepath.Join(root, "a.mkv"))
	if got := flushed(t, r, out); !strings.Contains(got, `"file":"[redacted]"`) {
		t.Errorf("non-path key kept a path: %s", got)
	}
	other := filepath.Join(t.TempDir(), "other")
	r.SetPathRoots([]string{other})
	r.Logger().Info("path seen", "path", filepath.Join(root, "Movies", "a.mkv"))
	r.Logger().Info("path seen", "path", filepath.Join(other, "c.mkv"))
	if got := flushed(t, r, out); !strings.Contains(got, `"path":"[redacted]"`) || !strings.Contains(got, `"path":"c.mkv"`) {
		t.Errorf("runtime roots not applied: %s", got)
	}
}

func TestMessageAndKeyGuards(t *testing.T) {
	for _, msg := range []string{"request completed", "metrics shutdown failed; pool retained", "Jelee started", "job state could not be persisted", "HTTP listener failed", ""} {
		if !safeMessage(msg) {
			t.Errorf("constant message rejected: %q", msg)
		}
	}
	for _, msg := range []string{"GET /secret", "token=abc", "user@example.com", "c:\\x", "ok\nforged", "abcdefghijklmnopqrstu", "tok3n1234567", strings.Repeat("a ", 101)} {
		if safeMessage(msg) {
			t.Errorf("unsafe message accepted: %q", msg)
		}
	}
	for _, key := range []string{"requestId", "durationMs", "api_key", "x", ""} {
		if safeKey(key) != key {
			t.Errorf("key %q rewritten", key)
		}
	}
	for _, key := range []string{"Set-Cookie", "!BADKEY", "two words", "9lives", "a.b", "whsec_Q2xhdWRl", strings.Repeat("k", 33)} {
		if safeKey(key) != "redactedKey" {
			t.Errorf("key %q kept", key)
		}
	}
}
