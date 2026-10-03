package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

const setupCLIPassword = "correct horse battery"

type setupRunnerStub struct {
	plan     app.SetupPlan
	password string
	calls    int
	state    domain.SetupState
	err      error
}

func (s *setupRunnerStub) RunHeadless(_ context.Context, plan app.SetupPlan, secret string) (domain.SetupState, error) {
	s.calls++
	s.plan, s.password = plan, secret
	return s.state, s.err
}

type setupStdin struct {
	*strings.Reader
	reads int
}

func (s *setupStdin) Read(p []byte) (int, error) {
	s.reads++
	return s.Reader.Read(p)
}

func runSetupForTest(t *testing.T, ctx context.Context, argv []string, input string, runner *setupRunnerStub) (int, string, string, int, *setupStdin) {
	t.Helper()
	stdin := &setupStdin{Reader: strings.NewReader(input)}
	var stdout, stderr bytes.Buffer
	opened := 0
	code := runSetupCLIWith(ctx, argv, stdin, &stdout, &stderr, setupCLIDependencies{open: func(context.Context) (setupCLIRunner, func(), error) {
		opened++
		return runner, func() {}, nil
	}})
	return code, stdout.String(), stderr.String(), opened, stdin
}

func setupBaseArgs(extra ...string) []string {
	return append([]string{"--non-interactive", "--password-stdin", "--admin-name", "admin"}, extra...)
}

func TestParseSetupCLIDefaultsAndRepeatables(t *testing.T) {
	plan, err := parseSetupCLI(setupBaseArgs())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Locale != "zh-CN" || plan.MetadataPolicy.NFORead != domain.NFOModeReadOnly || plan.MetadataPolicy.NFOWrite != domain.SetupNFOWriteOff ||
		plan.Network.Mode != domain.SetupNetworkLocal || plan.Network.Listen != "127.0.0.1:8097" ||
		!slices.Equal(plan.Network.AllowedHosts, []string{"localhost", "127.0.0.1", "::1"}) || plan.TMDB.Enabled || len(plan.Media) != 0 {
		t.Fatalf("defaults: %+v", plan)
	}
	if issues := app.ValidateSetupPlan(plan); len(issues) != 0 {
		t.Fatalf("default plan invalid: %+v", issues)
	}
	plan, err = parseSetupCLI(setupBaseArgs(
		"--locale", "ja-JP", "--admin-display-name", "管理者",
		"--library", "Movies=/srv/media/movies", "--library", "Odd=/srv/a=b",
		"--tmdb", "--accept-degraded-tools", "--nfo-write", "write-back", "--image-fetch", "--image-write-back",
		"--network-mode", "reverse-proxy", "--listen", "0.0.0.0:8097",
		"--allowed-host", "media.example", "--allowed-host", "localhost",
		"--trusted-proxy", "172.18.0.0/16", "--accept-privacy-notice"))
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.SetupLibrary{{Name: "Movies", Path: "/srv/media/movies"}, {Name: "Odd", Path: "/srv/a=b"}}
	if !slices.Equal(plan.Media, want) || plan.TMDB != (domain.SetupTMDB{Enabled: true, Language: "ja-JP"}) || !plan.AcceptDegradedTools ||
		plan.MetadataPolicy != (domain.SetupMetadataPolicy{NFORead: "read-only", NFOWrite: "write-back", ImageFetch: true, ImageWriteBack: true}) ||
		!slices.Equal(plan.Network.AllowedHosts, []string{"media.example", "localhost"}) || !slices.Equal(plan.Network.TrustedProxies, []string{"172.18.0.0/16"}) ||
		!plan.Network.PrivacyAcknowledged || plan.Admin.DisplayName != "管理者" {
		t.Fatalf("parsed: %+v", plan)
	}
	if issues := app.ValidateSetupPlan(plan); len(issues) != 0 {
		t.Fatalf("plan invalid: %+v", issues)
	}
}

func TestParseSetupCLIUsageErrors(t *testing.T) {
	for name, argv := range map[string][]string{
		"no non-interactive": {"--password-stdin", "--admin-name", "admin"},
		"no password-stdin":  {"--non-interactive", "--admin-name", "admin"},
		"password flag":      setupBaseArgs("--password", "x"),
		"positional":         setupBaseArgs("extra"),
		"library no equals":  setupBaseArgs("--library", "/srv/media"),
		"unknown flag":       setupBaseArgs("--admin-password", "x"),
	} {
		if _, err := parseSetupCLI(argv); !errors.Is(err, errSetupUsage) {
			t.Errorf("%s: %v", name, err)
		}
		runner := &setupRunnerStub{}
		code, _, stderr, opened, stdin := runSetupForTest(t, context.Background(), argv, setupCLIPassword, runner)
		if code != 2 || !strings.HasPrefix(stderr, "usage: jelee-cli setup") || opened != 0 || stdin.reads != 0 || strings.Contains(stderr, "/srv") {
			t.Errorf("%s: code=%d opened=%d reads=%d stderr=%q", name, code, opened, stdin.reads, stderr)
		}
	}
}

func TestSetupCLIRejectsInvalidValuesBeforeReadingPassword(t *testing.T) {
	runner := &setupRunnerStub{}
	argv := setupBaseArgs("--locale", "fr-FR", "--library", "Movies=relative/secret-dir", "--network-mode", "lan", "--listen", "0.0.0.0:8097")
	code, stdout, stderr, opened, stdin := runSetupForTest(t, context.Background(), argv, setupCLIPassword, runner)
	if code != 2 || stdout != "" || opened != 0 || stdin.reads != 0 {
		t.Fatalf("code=%d opened=%d reads=%d", code, opened, stdin.reads)
	}
	for _, want := range []string{"setup_input_invalid\n", "locale locale_unsupported\n", "media[0].path media_path_not_absolute\n", "network.privacyAcknowledged privacy_acknowledgement_required\n"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q lacks %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "secret-dir") || strings.Contains(stderr, "fr-FR") {
		t.Fatalf("stderr echoes input: %q", stderr)
	}
}

func TestSetupCLIPasswordFromStdin(t *testing.T) {
	for name, tc := range map[string]struct{ input, code string }{
		"short":      {"short\n", "admin.password password_length_invalid"},
		"too long":   {strings.Repeat("ab12", 300), "admin.password password_length_invalid"},
		"simple":     {"aaaaaaaaaaaaaaaa\n", "admin.password password_too_simple"},
		"has name":   {"my-admin-password\n", "admin.password password_contains_name"},
		"bad utf8":   {"valid-length\xff", "admin.password password_length_invalid"},
		"empty":      {"", "admin.password password_length_invalid"},
		"extra line": {"x\n\n", "admin.password password_length_invalid"},
	} {
		runner := &setupRunnerStub{}
		code, _, stderr, opened, _ := runSetupForTest(t, context.Background(), setupBaseArgs(), tc.input, runner)
		if code != 2 || opened != 0 || !strings.Contains(stderr, tc.code) || strings.Contains(stderr, strings.TrimSpace(tc.input)) && tc.input != "" {
			t.Errorf("%s: code=%d opened=%d stderr=%q", name, code, opened, stderr)
		}
	}
	runner := &setupRunnerStub{state: domain.SetupState{Version: 9, Current: domain.SetupStepComplete}}
	code, stdout, stderr, opened, _ := runSetupForTest(t, context.Background(), setupBaseArgs("--library", "Movies=/srv/movies"), setupCLIPassword+"\r\n", runner)
	if code != 0 || opened != 1 || runner.calls != 1 || runner.password != setupCLIPassword || runner.plan.Admin.Name != "admin" || runner.plan.Media[0].Path != "/srv/movies" || stderr != "" {
		t.Fatalf("success: code=%d opened=%d calls=%d stderr=%q", code, opened, runner.calls, stderr)
	}
	var out domain.SetupState
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.Version != 9 || out.Current != domain.SetupStepComplete || strings.Contains(stdout, setupCLIPassword) {
		t.Fatalf("stdout %q %v", stdout, err)
	}
}

func TestSetupCLIRunnerErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelExpired()
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		exit int
		out  string
	}{
		{"validation", context.Background(), &app.SetupValidationError{Step: domain.SetupStepNetwork, Issues: []app.SetupIssue{{Field: "network.listen", Code: "listen_port_in_use"}}}, 2, "setup_input_invalid\nnetwork.listen listen_port_in_use\n"},
		{"completed", context.Background(), domain.ErrSetupCompleted, 1, "setup_already_completed\n"},
		{"conflict", context.Background(), domain.ErrConflict, 1, "setup_conflict\n"},
		{"database", context.Background(), domain.ErrDatabase, 1, "setup_database_unavailable\n"},
		{"unknown", context.Background(), errors.New("pq: relation setup_state at 10.0.0.5"), 1, "setup_failed\n"},
		{"cancelled", cancelled, nil, 130, "setup_cancelled\n"},
		{"timeout", expired, nil, 124, "setup_timeout\n"},
	}
	for _, c := range cases {
		runner := &setupRunnerStub{err: c.err}
		code, stdout, stderr, _, _ := runSetupForTest(t, c.ctx, setupBaseArgs(), setupCLIPassword, runner)
		if code != c.exit || stderr != c.out || stdout != "" {
			t.Errorf("%s: code=%d stderr=%q stdout=%q", c.name, code, stderr, stdout)
		}
	}
}

// The registered command reaches the app ports, which report that storage
// is not wired yet, instead of pretending success.
func TestSetupCLIDefaultPortsAreUnimplemented(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runSetupCLI(context.Background(), setupBaseArgs(), strings.NewReader(setupCLIPassword), &stdout, &stderr)
	if code != 1 || stderr.String() != "setup_not_implemented\n" || stdout.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestSetupPasswordBoundsMatchPasswordPackage(t *testing.T) {
	if app.SetupMinPasswordBytes != password.MinPasswordBytes || app.SetupMaxPasswordBytes != password.MaxPasswordBytes {
		t.Fatal("setup password bounds drifted from platform/password")
	}
}
