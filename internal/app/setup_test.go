package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/platform/netaddr"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"path/filepath"
	"runtime"
)

const setupTestPassword = "correct horse battery"

// setupMemoryRepository models the PostgreSQL contract: version CAS, no
// writes to a completed state, single-admin creation inside one "transaction".
type setupMemoryRepository struct {
	mu          sync.Mutex
	state       domain.SetupState
	stored      bool
	activeAdmin bool
	admins      []domain.UserInput
	saves       int
	failSave    error
	failFinish  error
}

func (r *setupMemoryRepository) LoadSetupState(context.Context) (domain.SetupState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stored {
		return domain.SetupState{}, domain.ErrNotFound
	}
	return r.copyState(r.state), nil
}

func (r *setupMemoryRepository) HasActiveAdmin(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeAdmin, nil
}

func (r *setupMemoryRepository) copyState(s domain.SetupState) domain.SetupState {
	encoded, _ := json.Marshal(s)
	var out domain.SetupState
	_ = json.Unmarshal(encoded, &out)
	out.Version = s.Version
	return out
}

func (r *setupMemoryRepository) casLocked(next domain.SetupState) error {
	if r.stored && (r.state.Completed() || r.state.Version != next.Version) || !r.stored && next.Version != 0 {
		return domain.ErrConflict
	}
	return nil
}

func (r *setupMemoryRepository) commitLocked(next domain.SetupState) domain.SetupState {
	next.Version++
	r.state, r.stored = r.copyState(next), true
	r.saves++
	return r.copyState(r.state)
}

func (r *setupMemoryRepository) SaveSetupState(_ context.Context, next domain.SetupState) (domain.SetupState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSave != nil {
		return domain.SetupState{}, r.failSave
	}
	if err := r.casLocked(next); err != nil {
		return domain.SetupState{}, err
	}
	return r.commitLocked(next), nil
}

func (r *setupMemoryRepository) CreateSetupAdmin(_ context.Context, input domain.UserInput, next domain.SetupState) (domain.SetupState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.casLocked(next); err != nil {
		return domain.SetupState{}, err
	}
	if r.activeAdmin {
		return domain.SetupState{}, domain.ErrSetupAdminExists
	}
	r.admins = append(r.admins, input)
	r.activeAdmin = true
	next.Admin.UserID = fmt.Sprintf("fdf8be29-41af-4b60-ab36-%012d", len(r.admins))
	return r.commitLocked(next), nil
}

func (r *setupMemoryRepository) CompleteSetup(_ context.Context, next domain.SetupState) (domain.SetupState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFinish != nil {
		return domain.SetupState{}, r.failFinish
	}
	if err := r.casLocked(next); err != nil {
		return domain.SetupState{}, err
	}
	return r.commitLocked(next), nil
}

type setupFakeEnvironment struct {
	netaddr.Setup
	db        SetupDatabaseStatus
	dirs      map[string]SetupDirectoryStatus
	busy      map[string]bool
	tools     SetupToolReport
	tmdbKey   bool
	dbErr     error
	dirChecks int
}

func newSetupFakeEnvironment() *setupFakeEnvironment {
	return &setupFakeEnvironment{
		db:      SetupDatabaseStatus{ServerVersion: 170000, MinServerVersion: 150000, SchemaVersion: 4, RequiredSchema: 4, Clean: true},
		dirs:    map[string]SetupDirectoryStatus{testAbsPath("/media/movies"): {true, true, true}, testAbsPath("/media/shows"): {true, true, true}},
		busy:    map[string]bool{},
		tools:   SetupToolReport{Available: []string{"ffprobe", "mkvmerge", "mediainfo"}},
		tmdbKey: true,
	}
}

func (e *setupFakeEnvironment) DatabaseStatus(context.Context) (SetupDatabaseStatus, error) {
	return e.db, e.dbErr
}
func (e *setupFakeEnvironment) InspectDirectory(_ context.Context, path string) (SetupDirectoryStatus, error) {
	e.dirChecks++
	return e.dirs[path], nil
}
func (e *setupFakeEnvironment) ListenAvailable(_ context.Context, address string) (bool, error) {
	return !e.busy[address], nil
}
func (e *setupFakeEnvironment) DetectTools(context.Context) (SetupToolReport, error) {
	return e.tools, nil
}
func (e *setupFakeEnvironment) TMDBCredentialConfigured() bool { return e.tmdbKey }

type setupCountingHasher struct{ calls []string }

func (h *setupCountingHasher) Hash(_ context.Context, password string) (string, error) {
	h.calls = append(h.calls, password)
	return "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA", nil
}

type setupFixture struct {
	repo   *setupMemoryRepository
	env    *setupFakeEnvironment
	hasher *setupCountingHasher
	setup  *Setup
	now    time.Time
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	f := &setupFixture{repo: &setupMemoryRepository{}, env: newSetupFakeEnvironment(), hasher: &setupCountingHasher{}, now: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}
	f.setup = f.reopen(t)
	return f
}

// reopen models a restart or a reconnecting client: fresh use case, same storage.
func (f *setupFixture) reopen(t *testing.T) *Setup {
	t.Helper()
	setup, err := NewSetup(f.repo, f.env, f.hasher, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func setupTestPlan() SetupPlan {
	return SetupPlan{
		Locale:         "zh-CN",
		Admin:          SetupAdminInput{Name: "admin", DisplayName: "管理员"},
		Media:          []domain.SetupLibrary{{Name: "Movies", Path: testAbsPath("/media/movies")}, {Name: "Shows", Path: testAbsPath("/media/shows")}},
		TMDB:           domain.SetupTMDB{Enabled: true, Language: "zh-CN"},
		MetadataPolicy: domain.SetupMetadataPolicy{NFORead: domain.NFOModeReadOnly, NFOWrite: domain.SetupNFOWriteOff, ImageFetch: true},
		Network:        domain.SetupNetwork{Mode: domain.SetupNetworkLocal, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}},
	}
}

func mustSetup(t *testing.T) func(domain.SetupState, error) domain.SetupState {
	t.Helper()
	return func(state domain.SetupState, err error) domain.SetupState {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return state
	}
}

func setupIssueCodes(t *testing.T, err error) []string {
	t.Helper()
	var invalid *SetupValidationError
	if !errors.As(err, &invalid) || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want validation error, got %v", err)
	}
	codes := make([]string, 0, len(invalid.Issues))
	for _, issue := range invalid.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func wantSetupIssue(t *testing.T, err error, code string) {
	t.Helper()
	if codes := setupIssueCodes(t, err); !slices.Contains(codes, code) {
		t.Fatalf("issues %v do not contain %s", codes, code)
	}
}

func TestSetupWizardFullFlowPersistsEachStep(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	plan := setupTestPlan()
	status, err := f.setup.Status(ctx)
	if err != nil || status.Completed || status.State.Current != domain.SetupStepLanguage {
		t.Fatalf("fresh status %+v %v", status, err)
	}
	mustSetup(t)(f.setup.SubmitLanguage(ctx, plan.Locale))
	state := mustSetup(t)(f.setup.SubmitAdmin(ctx, plan.Admin, setupTestPassword))
	if !domain.ValidID(state.Admin.UserID) || len(f.repo.admins) != 1 || f.repo.admins[0].Locale != "zh-CN" || !f.repo.admins[0].Admin || f.repo.admins[0].PasswordHash == "" {
		t.Fatalf("admin creation: %+v %+v", state.Admin, f.repo.admins)
	}
	mustSetup(t)(f.setup.SubmitDatabase(ctx))
	mustSetup(t)(f.setup.SubmitMedia(ctx, plan.Media))
	mustSetup(t)(f.setup.SubmitTMDB(ctx, plan.TMDB))
	mustSetup(t)(f.setup.SubmitToolchain(ctx, false))
	mustSetup(t)(f.setup.SubmitMetadataPolicy(ctx, plan.MetadataPolicy))
	state = mustSetup(t)(f.setup.SubmitNetwork(ctx, plan.Network))
	if state.Current != domain.SetupStepComplete || f.repo.saves != 8 {
		t.Fatalf("before finish: current=%s saves=%d", state.Current, f.repo.saves)
	}
	state = mustSetup(t)(f.setup.Finish(ctx))
	if !state.Completed() || state.Database.SchemaVersion != 4 || len(state.Media) != 2 || state.Toolchain.Available[0] != "ffprobe" {
		t.Fatalf("finished state %+v", state)
	}
	encoded, _ := json.Marshal(f.repo.state)
	if strings.Contains(string(encoded), setupTestPassword) || strings.Contains(string(encoded), "argon2") {
		t.Fatalf("persisted state leaks credentials: %s", encoded)
	}
}

func TestSetupInterruptAndResume(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	plan := setupTestPlan()
	mustSetup(t)(f.setup.SubmitLanguage(ctx, "ja-JP"))
	mustSetup(t)(f.setup.SubmitAdmin(ctx, plan.Admin, setupTestPassword))
	// Restart: a new instance continues where storage says.
	resumed := f.reopen(t)
	status, err := resumed.Status(ctx)
	if err != nil || status.Completed || status.State.Current != domain.SetupStepDatabase || status.State.Locale != "ja-JP" {
		t.Fatalf("resume status %+v %v", status, err)
	}
	if _, err := resumed.SubmitLanguage(ctx, "zh-CN"); !errors.Is(err, domain.ErrSetupStepOrder) {
		t.Fatalf("submitting a past step without Back: %v", err)
	}
	if _, err := resumed.SubmitMedia(ctx, plan.Media); !errors.Is(err, domain.ErrSetupStepOrder) {
		t.Fatalf("skipping ahead: %v", err)
	}
	if state := mustSetup(t)(resumed.SubmitDatabase(ctx)); state.Current != domain.SetupStepMedia {
		t.Fatalf("resumed step: %s", state.Current)
	}
	if !domain.SetupRequiresAdminSession(f.repo.state) {
		t.Fatal("after admin creation the wizard must be bound to that admin")
	}
}

func TestSetupBackAndReenterDoesNotDuplicateAdmin(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	plan := setupTestPlan()
	mustSetup(t)(f.setup.SubmitLanguage(ctx, "zh-CN"))
	created := mustSetup(t)(f.setup.SubmitAdmin(ctx, plan.Admin, setupTestPassword))
	mustSetup(t)(f.setup.SubmitDatabase(ctx))
	for range 3 {
		mustSetup(t)(f.setup.Back(ctx))
	}
	state := mustSetup(t)(f.setup.SubmitLanguage(ctx, "en-US"))
	if state.Current != domain.SetupStepAdmin || state.Database.SchemaVersion != 4 {
		t.Fatalf("back must keep later data: %+v", state)
	}
	if _, err := f.setup.SubmitAdmin(ctx, SetupAdminInput{Name: "other"}, setupTestPassword); err == nil {
		t.Fatal("re-entry with a different admin name accepted")
	} else {
		wantSetupIssue(t, err, "admin_already_created")
	}
	state = mustSetup(t)(f.setup.SubmitAdmin(ctx, SetupAdminInput{Name: "ADMIN"}, ""))
	if state.Admin.UserID != created.Admin.UserID || len(f.repo.admins) != 1 || len(f.hasher.calls) != 1 {
		t.Fatalf("re-entry created another admin: %+v admins=%d hashes=%d", state.Admin, len(f.repo.admins), len(f.hasher.calls))
	}
	if _, err := f.setup.Back(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.setup.Back(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.setup.Back(ctx); !errors.Is(err, domain.ErrSetupStepOrder) {
		t.Fatalf("back from first step: %v", err)
	}
}

func runSetupToEnd(t *testing.T, f *setupFixture) {
	t.Helper()
	if _, err := f.setup.RunHeadless(context.Background(), setupTestPlan(), setupTestPassword); err != nil {
		t.Fatal(err)
	}
}

func TestSetupCompletedRejectsReentry(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	runSetupToEnd(t, f)
	plan := setupTestPlan()
	attempts := map[string]func() error{
		"language": func() error { _, err := f.setup.SubmitLanguage(ctx, "zh-CN"); return err },
		"admin":    func() error { _, err := f.setup.SubmitAdmin(ctx, plan.Admin, setupTestPassword); return err },
		"database": func() error { _, err := f.setup.SubmitDatabase(ctx); return err },
		"media":    func() error { _, err := f.setup.SubmitMedia(ctx, plan.Media); return err },
		"tmdb":     func() error { _, err := f.setup.SubmitTMDB(ctx, plan.TMDB); return err },
		"tools":    func() error { _, err := f.setup.SubmitToolchain(ctx, true); return err },
		"policy":   func() error { _, err := f.setup.SubmitMetadataPolicy(ctx, plan.MetadataPolicy); return err },
		"network":  func() error { _, err := f.setup.SubmitNetwork(ctx, plan.Network); return err },
		"back":     func() error { _, err := f.setup.Back(ctx); return err },
		"finish":   func() error { _, err := f.setup.Finish(ctx); return err },
		"headless": func() error { _, err := f.setup.RunHeadless(ctx, plan, setupTestPassword); return err },
	}
	saves := f.repo.saves
	for name, attempt := range attempts {
		if err := attempt(); !errors.Is(err, domain.ErrSetupCompleted) {
			t.Fatalf("%s after completion: %v", name, err)
		}
	}
	if f.repo.saves != saves || len(f.repo.admins) != 1 {
		t.Fatalf("completed state modified: saves %d->%d admins=%d", saves, f.repo.saves, len(f.repo.admins))
	}
}

func TestSetupExistingAdminInstallIsCompleted(t *testing.T) {
	f := newSetupFixture(t)
	f.repo.activeAdmin = true
	ctx := context.Background()
	status, err := f.setup.Status(ctx)
	if err != nil || !status.Completed || !status.Adopted {
		t.Fatalf("existing install status %+v %v", status, err)
	}
	if _, err := f.setup.SubmitLanguage(ctx, "zh-CN"); !errors.Is(err, domain.ErrSetupCompleted) {
		t.Fatalf("existing install entered wizard: %v", err)
	}
	if f.repo.stored || len(f.repo.admins) != 0 {
		t.Fatal("existing install was modified")
	}
}

func TestSetupOutOfBandAdminDuringWizard(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	mustSetup(t)(f.setup.SubmitLanguage(ctx, "zh-CN"))
	f.repo.activeAdmin = true // e.g. `jelee-cli account bootstrap` ran meanwhile
	status, _ := f.setup.Status(ctx)
	if status.Completed {
		t.Fatal("stored in-progress wizard must not be adopted")
	}
	_, err := f.setup.SubmitAdmin(ctx, setupTestPlan().Admin, setupTestPassword)
	wantSetupIssue(t, err, "admin_exists")
	if f.repo.state.Current != domain.SetupStepAdmin || len(f.repo.admins) != 0 {
		t.Fatalf("state changed: %+v", f.repo.state)
	}
}

func TestSetupConcurrentWriterConflict(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	mustSetup(t)(f.setup.SubmitLanguage(ctx, "zh-CN"))
	stale := f.repo.copyState(f.repo.state)
	mustSetup(t)(f.setup.Back(ctx))
	if _, err := f.repo.SaveSetupState(ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
}

func TestSetupStaticValidation(t *testing.T) {
	long := strings.Repeat("a", 129)
	cases := []struct {
		name   string
		issues []SetupIssue
		code   string
	}{
		{"locale", ValidateSetupLanguage("fr-FR"), "locale_unsupported"},
		{"admin name blank", ValidateSetupAdmin(SetupAdminInput{Name: " "}), "admin_name_invalid"},
		{"admin name padded", ValidateSetupAdmin(SetupAdminInput{Name: " admin"}), "admin_name_invalid"},
		{"admin display", ValidateSetupAdmin(SetupAdminInput{Name: "admin", DisplayName: long}), "admin_display_name_invalid"},
		{"password short", ValidateSetupPassword("admin", "short"), "password_length_invalid"},
		{"password long", ValidateSetupPassword("admin", strings.Repeat("ab12", 257)), "password_length_invalid"},
		{"password utf8", ValidateSetupPassword("admin", "valid-length\xff"), "password_encoding_invalid"},
		{"password simple", ValidateSetupPassword("admin", "aaaaaaaaaaab"), "password_too_simple"},
		{"password name", ValidateSetupPassword("operator", "my-OPERATOR-pass"), "password_contains_name"},
		{"media relative", ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: "media/a"}}), "media_path_not_absolute"},
		{"media unclean", ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: testAbsPath("/media/a/../b")}}), "media_path_not_absolute"},
		{"media dup name", ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: testAbsPath("/a")}, {Name: "a", Path: testAbsPath("/b")}}), "media_name_duplicate"},
		{"media dup path", ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: testAbsPath("/a")}, {Name: "B", Path: testAbsPath("/a")}}), "media_path_duplicate"},
		{"media nested", ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: testAbsPath("/media/a")}, {Name: "B", Path: testAbsPath("/media")}}), "media_path_nested"},
		{"media too many", ValidateSetupMedia(make([]domain.SetupLibrary, 65)), "media_too_many"},
		{"tmdb language", ValidateSetupTMDB(domain.SetupTMDB{Enabled: true, Language: "de-DE"}), "tmdb_language_invalid"},
		{"tmdb disabled language", ValidateSetupTMDB(domain.SetupTMDB{Language: "zh-CN"}), "tmdb_language_invalid"},
		{"nfo read", ValidateSetupMetadataPolicy(domain.SetupMetadataPolicy{NFORead: "read-write", NFOWrite: "off"}, true), "nfo_read_mode_invalid"},
		{"nfo write", ValidateSetupMetadataPolicy(domain.SetupMetadataPolicy{NFORead: "off", NFOWrite: "always"}, true), "nfo_write_mode_invalid"},
		{"nfo write needs read", ValidateSetupMetadataPolicy(domain.SetupMetadataPolicy{NFORead: "off", NFOWrite: "write-back"}, true), "nfo_write_requires_read"},
		{"image fetch needs tmdb", ValidateSetupMetadataPolicy(domain.SetupMetadataPolicy{NFORead: "off", NFOWrite: "off", ImageFetch: true}, false), "image_fetch_requires_tmdb"},
		{"network mode", ValidateSetupNetwork(domain.SetupNetwork{Mode: "public", Listen: "127.0.0.1:1", AllowedHosts: []string{"x"}}, netaddr.Setup{}), "network_mode_invalid"},
		{"listen", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "localhost:8097", AllowedHosts: []string{"x"}}, netaddr.Setup{}), "listen_invalid"},
		{"listen port 0", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "127.0.0.1:0", AllowedHosts: []string{"x"}}, netaddr.Setup{}), "listen_invalid"},
		{"local not loopback", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "0.0.0.0:8097", AllowedHosts: []string{"x"}}, netaddr.Setup{}), "listen_not_loopback"},
		{"lan loopback", ValidateSetupNetwork(domain.SetupNetwork{Mode: "lan", Listen: "[::1]:8097", AllowedHosts: []string{"x"}, PrivacyAcknowledged: true}, netaddr.Setup{}), "listen_loopback_only"},
		{"lan privacy", ValidateSetupNetwork(domain.SetupNetwork{Mode: "lan", Listen: "0.0.0.0:8097", AllowedHosts: []string{"x"}}, netaddr.Setup{}), "privacy_acknowledgement_required"},
		{"hosts empty", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "127.0.0.1:8097"}, netaddr.Setup{}), "allowed_hosts_count_invalid"},
		{"host upper", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "127.0.0.1:8097", AllowedHosts: []string{"Media.Example"}}, netaddr.Setup{}), "allowed_host_invalid"},
		{"host dup", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "127.0.0.1:8097", AllowedHosts: []string{"a", "a"}}, netaddr.Setup{}), "allowed_host_invalid"},
		{"proxy invalid", ValidateSetupNetwork(domain.SetupNetwork{Mode: "local", Listen: "127.0.0.1:8097", AllowedHosts: []string{"a"}, TrustedProxies: []string{"10.0.0.1"}}, netaddr.Setup{}), "trusted_proxy_invalid"},
		{"proxy required", ValidateSetupNetwork(domain.SetupNetwork{Mode: "reverse-proxy", Listen: "127.0.0.1:8097", AllowedHosts: []string{"a"}, PrivacyAcknowledged: true}, netaddr.Setup{}), "trusted_proxies_required"},
	}
	for _, c := range cases {
		found := false
		for _, issue := range c.issues {
			found = found || issue.Code == c.code
			if issue.Field == "" {
				t.Errorf("%s: issue without field", c.name)
			}
		}
		if !found {
			t.Errorf("%s: issues %+v lack %s", c.name, c.issues, c.code)
		}
	}
	plan := setupTestPlan()
	if issues := ValidateSetupPlan(plan, netaddr.Setup{}); len(issues) != 0 {
		t.Fatalf("valid plan rejected: %+v", issues)
	}
	if issues := ValidateSetupPassword(plan.Admin.Name, setupTestPassword); len(issues) != 0 {
		t.Fatalf("valid password rejected: %+v", issues)
	}
	valid := []domain.SetupNetwork{
		{Mode: "local", Listen: "[::1]:8097", AllowedHosts: []string{"localhost", "::1"}},
		{Mode: "lan", Listen: "0.0.0.0:8097", AllowedHosts: []string{"192.168.1.10"}, PrivacyAcknowledged: true},
		{Mode: "reverse-proxy", Listen: "127.0.0.1:8097", AllowedHosts: []string{"media.example"}, TrustedProxies: []string{"127.0.0.1/32", "::1/128"}, PrivacyAcknowledged: true},
	}
	for _, network := range valid {
		if issues := ValidateSetupNetwork(network, netaddr.Setup{}); len(issues) != 0 {
			t.Errorf("valid network %+v rejected: %+v", network, issues)
		}
	}
	if issues := ValidateSetupMedia([]domain.SetupLibrary{{Name: "A", Path: testAbsPath("/media/a")}, {Name: "B", Path: testAbsPath("/media/ab")}}); len(issues) != 0 {
		t.Fatalf("sibling prefix paths treated as nested: %+v", issues)
	}
}

func TestSetupLiveChecks(t *testing.T) {
	ctx := context.Background()
	plan := setupTestPlan()
	advanceTo := func(t *testing.T, f *setupFixture, step domain.SetupStep) {
		t.Helper()
		submit := []func() error{
			func() error { _, err := f.setup.SubmitLanguage(ctx, plan.Locale); return err },
			func() error { _, err := f.setup.SubmitAdmin(ctx, plan.Admin, setupTestPassword); return err },
			func() error { _, err := f.setup.SubmitDatabase(ctx); return err },
			func() error { _, err := f.setup.SubmitMedia(ctx, plan.Media); return err },
			func() error { _, err := f.setup.SubmitTMDB(ctx, plan.TMDB); return err },
			func() error { _, err := f.setup.SubmitToolchain(ctx, false); return err },
			func() error { _, err := f.setup.SubmitMetadataPolicy(ctx, plan.MetadataPolicy); return err },
			func() error { _, err := f.setup.SubmitNetwork(ctx, plan.Network); return err },
		}
		for i := 0; domain.SetupStep(i+1) < step; i++ {
			if err := submit[i](); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("database", func(t *testing.T) {
		for code, mutate := range map[string]func(*SetupDatabaseStatus){
			"database_server_outdated": func(s *SetupDatabaseStatus) { s.ServerVersion = 140000 },
			"database_schema_outdated": func(s *SetupDatabaseStatus) { s.SchemaVersion = 3 },
			"database_migration_dirty": func(s *SetupDatabaseStatus) { s.Clean = false },
		} {
			f := newSetupFixture(t)
			advanceTo(t, f, domain.SetupStepDatabase)
			mutate(&f.env.db)
			_, err := f.setup.SubmitDatabase(ctx)
			wantSetupIssue(t, err, code)
			if f.repo.state.Current != domain.SetupStepDatabase {
				t.Fatal("failed check advanced the wizard")
			}
		}
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepDatabase)
		f.env.dbErr = domain.ErrDatabase
		if _, err := f.setup.SubmitDatabase(ctx); !errors.Is(err, domain.ErrDatabase) {
			t.Fatalf("probe error: %v", err)
		}
	})
	t.Run("media", func(t *testing.T) {
		for code, status := range map[string]SetupDirectoryStatus{
			"media_directory_missing":       {},
			"media_directory_not_directory": {Exists: true},
			"media_directory_unreadable":    {Exists: true, Directory: true},
		} {
			f := newSetupFixture(t)
			advanceTo(t, f, domain.SetupStepMedia)
			f.env.dirs[testAbsPath("/media/shows")] = status
			_, err := f.setup.SubmitMedia(ctx, plan.Media)
			wantSetupIssue(t, err, code)
		}
	})
	t.Run("tmdb credential", func(t *testing.T) {
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepTMDB)
		f.env.tmdbKey = false
		_, err := f.setup.SubmitTMDB(ctx, plan.TMDB)
		wantSetupIssue(t, err, "tmdb_credential_missing")
		mustSetup(t)(f.setup.SubmitTMDB(ctx, domain.SetupTMDB{}))
	})
	t.Run("toolchain", func(t *testing.T) {
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepToolchain)
		f.env.tools = SetupToolReport{Available: []string{"ffprobe"}, Missing: []string{"mediainfo"}}
		_, err := f.setup.SubmitToolchain(ctx, false)
		wantSetupIssue(t, err, "toolchain_missing")
		state := mustSetup(t)(f.setup.SubmitToolchain(ctx, true))
		if !state.Toolchain.AcceptDegraded || state.Toolchain.Missing[0] != "mediainfo" {
			t.Fatalf("degraded toolchain: %+v", state.Toolchain)
		}
	})
	t.Run("image fetch depends on recorded tmdb", func(t *testing.T) {
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepTMDB)
		mustSetup(t)(f.setup.SubmitTMDB(ctx, domain.SetupTMDB{}))
		mustSetup(t)(f.setup.SubmitToolchain(ctx, false))
		_, err := f.setup.SubmitMetadataPolicy(ctx, plan.MetadataPolicy)
		wantSetupIssue(t, err, "image_fetch_requires_tmdb")
	})
	t.Run("port in use", func(t *testing.T) {
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepNetwork)
		f.env.busy["127.0.0.1:8097"] = true
		_, err := f.setup.SubmitNetwork(ctx, plan.Network)
		wantSetupIssue(t, err, "listen_port_in_use")
	})
	t.Run("finish rechecks and rolls back", func(t *testing.T) {
		f := newSetupFixture(t)
		advanceTo(t, f, domain.SetupStepComplete)
		f.env.dirs[testAbsPath("/media/movies")] = SetupDirectoryStatus{Exists: true, Directory: true}
		_, err := f.setup.Finish(ctx)
		wantSetupIssue(t, err, "media_directory_unreadable")
		f.env.dirs[testAbsPath("/media/movies")] = SetupDirectoryStatus{true, true, true}
		f.repo.failFinish = domain.ErrDatabase
		if _, err := f.setup.Finish(ctx); !errors.Is(err, domain.ErrDatabase) {
			t.Fatalf("finish failure: %v", err)
		}
		if f.repo.state.Completed() {
			t.Fatal("failed completion left a completed state")
		}
		f.repo.failFinish = nil
		if state := mustSetup(t)(f.setup.Finish(ctx)); !state.Completed() {
			t.Fatal("retry after rollback did not complete")
		}
	})
}

func TestSetupHeadlessResumesAfterInterruption(t *testing.T) {
	f := newSetupFixture(t)
	ctx := context.Background()
	f.env.busy["127.0.0.1:8097"] = true
	_, err := f.setup.RunHeadless(ctx, setupTestPlan(), setupTestPassword)
	wantSetupIssue(t, err, "listen_port_in_use")
	if f.repo.state.Current != domain.SetupStepNetwork || len(f.repo.admins) != 1 {
		t.Fatalf("interrupted headless state: %+v admins=%d", f.repo.state, len(f.repo.admins))
	}
	delete(f.env.busy, "127.0.0.1:8097")
	state, err := f.reopen(t).RunHeadless(ctx, setupTestPlan(), setupTestPassword)
	if err != nil || !state.Completed() || len(f.repo.admins) != 1 || len(f.hasher.calls) != 1 {
		t.Fatalf("resumed headless: %v completed=%v admins=%d hashes=%d", err, state.Completed(), len(f.repo.admins), len(f.hasher.calls))
	}
}

func TestSetupHeadlessRejectsInvalidPlanBeforeStorage(t *testing.T) {
	f := newSetupFixture(t)
	plan := setupTestPlan()
	plan.Locale = "xx"
	_, err := f.setup.RunHeadless(context.Background(), plan, "short")
	codes := setupIssueCodes(t, err)
	if !slices.Contains(codes, "locale_unsupported") || !slices.Contains(codes, "password_length_invalid") {
		t.Fatalf("codes %v", codes)
	}
	if f.repo.stored || len(f.hasher.calls) != 0 {
		t.Fatal("invalid plan touched storage or hashed")
	}
}

func TestNewSetupRequiresDependencies(t *testing.T) {
	if _, err := NewSetup(nil, newSetupFakeEnvironment(), &setupCountingHasher{}, time.Now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
}

// testAbsPath turns a slash path into an absolute path on the running OS:
// Windows needs a volume, so the tests use C: there.
func testAbsPath(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return p
}
