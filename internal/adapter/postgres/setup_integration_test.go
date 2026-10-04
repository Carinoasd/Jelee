package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/netaddr"
)

// setupTestEnvironment passes every live check for the directories it knows.
type setupTestEnvironment struct {
	netaddr.Setup
	store *Store
	dirs  map[string]bool
}

func (e setupTestEnvironment) DatabaseStatus(ctx context.Context) (app.SetupDatabaseStatus, error) {
	facts, err := e.store.SetupDatabaseFacts(ctx)
	return app.SetupDatabaseStatus{ServerVersion: facts.ServerVersion, MinServerVersion: 150000, SchemaVersion: facts.SchemaVersion, RequiredSchema: SchemaVersion, Clean: !facts.Dirty}, err
}
func (e setupTestEnvironment) InspectDirectory(_ context.Context, path string) (app.SetupDirectoryStatus, error) {
	ok := e.dirs[path]
	return app.SetupDirectoryStatus{Exists: ok, Directory: ok, Readable: ok}, nil
}
func (setupTestEnvironment) ListenAvailable(context.Context, string) (bool, error) { return true, nil }
func (setupTestEnvironment) DetectTools(context.Context) (app.SetupToolReport, error) {
	return app.SetupToolReport{Missing: []string{"ffprobe"}}, nil
}
func (setupTestEnvironment) TMDBCredentialConfigured() bool { return false }

type setupTestHasher struct{ calls int }

func (h *setupTestHasher) Hash(context.Context, string) (string, error) {
	h.calls++
	return accountTestHash, nil
}

const setupIntegrationPassword = "correct horse battery"

func newSetupForTest(t *testing.T, s *Store, dirs ...string) (*app.Setup, *setupTestHasher) {
	t.Helper()
	known := map[string]bool{}
	for _, dir := range dirs {
		known[dir] = true
	}
	hasher := &setupTestHasher{}
	setup, err := app.NewSetup(s, setupTestEnvironment{store: s, dirs: known}, hasher, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return setup, hasher
}

func setupMediaPlan(movies, shows string) []domain.SetupLibrary {
	return []domain.SetupLibrary{{Name: "Movies", Path: movies}, {Name: "Shows", Path: shows}}
}

func runSetupSteps(t *testing.T, ctx context.Context, setup *app.Setup, steps ...func() (domain.SetupState, error)) domain.SetupState {
	t.Helper()
	var state domain.SetupState
	var err error
	for i, step := range steps {
		if state, err = step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	return state
}

func setupCount(t *testing.T, ctx context.Context, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// G18.2: every step is persisted, an interrupted wizard resumes from storage
// in a fresh service instance, back keeps data, and re-entering the admin
// step never creates a second administrator. G18.4: completion writes the
// libraries and the final state together, then the state is final.
func TestSetupPostgresResumeReentryAndCompletion(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	movies, shows := t.TempDir(), t.TempDir()
	ctx = domain.WithSetupOrigin(ctx, domain.SetupOrigin{Channel: "http", IP: "192.0.2.7", RequestID: "req-setup-1"})
	first, hasher := newSetupForTest(t, s, movies, shows)
	status, err := first.Status(ctx)
	if err != nil || status.Completed || status.Adopted || status.State.Current != domain.SetupStepLanguage {
		t.Fatalf("fresh status: %+v %v", status, err)
	}
	runSetupSteps(t, ctx, first,
		func() (domain.SetupState, error) { return first.SubmitLanguage(ctx, "zh-TW") },
		func() (domain.SetupState, error) {
			return first.SubmitAdmin(ctx, app.SetupAdminInput{Name: "Owner", DisplayName: "Owner"}, setupIntegrationPassword)
		},
	)
	// Interrupt: a new process reads the stored progress.
	second, _ := newSetupForTest(t, s, movies, shows)
	status, err = second.Status(ctx)
	if err != nil || status.Completed || status.State.Current != domain.SetupStepDatabase || status.State.Locale != "zh-TW" || !domain.ValidID(status.State.Admin.UserID) {
		t.Fatalf("resumed status: %+v %v", status, err)
	}
	adminID := status.State.Admin.UserID
	// A half-initialised instance has an administrator already but must not
	// count as adopted: the stored wizard row wins.
	if active, err := s.HasActiveAdmin(ctx); err != nil || !active {
		t.Fatal("wizard administrator not active")
	}
	state := runSetupSteps(t, ctx, second,
		func() (domain.SetupState, error) { return second.SubmitDatabase(ctx) },
		func() (domain.SetupState, error) { return second.Back(ctx) },
		func() (domain.SetupState, error) { return second.Back(ctx) },
	)
	if state.Current != domain.SetupStepAdmin || state.Admin.UserID != adminID || state.Database.SchemaVersion != SchemaVersion {
		t.Fatalf("back kept data: %+v", state)
	}
	// Re-entry with another name is refused; the same name advances without
	// hashing or creating anything.
	var invalid *app.SetupValidationError
	if _, err = second.SubmitAdmin(ctx, app.SetupAdminInput{Name: "Intruder"}, setupIntegrationPassword); !errors.As(err, &invalid) || invalid.Issues[0].Code != "admin_already_created" {
		t.Fatalf("other name on re-entry: %v", err)
	}
	runSetupSteps(t, ctx, second,
		func() (domain.SetupState, error) {
			return second.SubmitAdmin(ctx, app.SetupAdminInput{Name: "owner"}, "ignored on re-entry")
		},
		func() (domain.SetupState, error) { return second.SubmitDatabase(ctx) },
		func() (domain.SetupState, error) { return second.SubmitMedia(ctx, setupMediaPlan(movies, shows)) },
		func() (domain.SetupState, error) {
			return second.SubmitTMDB(ctx, domain.SetupTMDB{})
		},
		func() (domain.SetupState, error) { return second.SubmitToolchain(ctx, true) },
		func() (domain.SetupState, error) {
			return second.SubmitMetadataPolicy(ctx, domain.SetupMetadataPolicy{NFORead: domain.NFOModeReadOnly, NFOWrite: domain.SetupNFOWriteOff})
		},
		func() (domain.SetupState, error) {
			return second.SubmitNetwork(ctx, domain.SetupNetwork{Mode: domain.SetupNetworkLocal, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}})
		},
	)
	if hasher.calls != 1 || setupCount(t, ctx, s, `SELECT count(*) FROM users`) != 1 {
		t.Fatal("re-entry created or re-hashed an administrator")
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM libraries`) != 0 {
		t.Fatal("libraries written before completion")
	}
	done, err := second.Finish(ctx)
	if err != nil || !done.Completed() {
		t.Fatalf("finish: %+v %v", done, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM libraries l JOIN library_roots r ON r.library_id=l.id WHERE l.nfo_mode='read-only' AND l.metadata_language='zh-TW' AND r.path IN ($1,$2)`, movies, shows) != 2 {
		t.Fatal("completion did not write the libraries with the chosen policy")
	}
	status, err = first.Status(ctx)
	if err != nil || !status.Completed || status.Adopted || status.State.CompletedAt == nil {
		t.Fatalf("completed status: %+v %v", status, err)
	}
	// Completion is final for the service, the repository and the database.
	if _, err = second.Back(ctx); !errors.Is(err, domain.ErrSetupCompleted) {
		t.Fatalf("back after completion: %v", err)
	}
	if _, err = second.SubmitLanguage(ctx, "en-US"); !errors.Is(err, domain.ErrSetupCompleted) {
		t.Fatalf("step after completion: %v", err)
	}
	reopened := status.State
	reopened.CompletedAt, reopened.Current = nil, domain.SetupStepNetwork
	if _, err = s.SaveSetupState(ctx, reopened); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("repository reopened completed setup: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE setup_state SET completed_at=NULL`); err == nil {
		t.Fatal("database allowed reopening completed setup")
	}
	if _, err = s.Pool.Exec(ctx, `DELETE FROM setup_state`); err == nil {
		t.Fatal("database allowed deleting completed setup")
	}
	// Audit trail: steps, the administrator and completion, with origin.
	for event, want := range map[string]int{"setup.step_saved": 11, "setup.admin_created": 1, "setup.completed": 1, "library.registered": 2} {
		if got := setupCount(t, ctx, s, `SELECT count(*) FROM audit_logs WHERE event=$1 AND host(actor_ip)='192.0.2.7'`, event); got != want {
			t.Errorf("audit %s: %d want %d", event, got, want)
		}
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM audit_logs WHERE event LIKE 'setup.%' AND (request_id IS DISTINCT FROM 'req-setup-1' OR after_state::text LIKE '%argon2%')`) != 0 {
		t.Fatal("setup audit lost its request id or stored a secret")
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM audit_logs WHERE event='setup.admin_created' AND target_id=$1::uuid`, adminID) != 1 {
		t.Fatal("administrator creation not audited against the user")
	}
}

// G18.4: a failure inside the completion transaction rolls back every
// initialization record; the wizard stays at the complete step and a retry
// after the cause is removed succeeds.
func TestSetupPostgresCompletionRollsBack(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	movies, shows := t.TempDir(), t.TempDir()
	setup, _ := newSetupForTest(t, s, movies, shows)
	plan := app.SetupPlan{Locale: "en-US", Admin: app.SetupAdminInput{Name: "Owner"}, Media: setupMediaPlan(movies, shows), AcceptDegradedTools: true,
		MetadataPolicy: domain.SetupMetadataPolicy{NFORead: domain.NFOModeOff, NFOWrite: domain.SetupNFOWriteOff},
		Network:        domain.SetupNetwork{Mode: domain.SetupNetworkLocal, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}}}
	// Another writer takes the second root after its step was validated, so
	// the second library insert fails after the first one succeeded.
	runSetupSteps(t, ctx, setup,
		func() (domain.SetupState, error) { return setup.SubmitLanguage(ctx, plan.Locale) },
		func() (domain.SetupState, error) { return setup.SubmitAdmin(ctx, plan.Admin, setupIntegrationPassword) },
		func() (domain.SetupState, error) { return setup.SubmitDatabase(ctx) },
		func() (domain.SetupState, error) { return setup.SubmitMedia(ctx, plan.Media) },
		func() (domain.SetupState, error) { return setup.SubmitTMDB(ctx, plan.TMDB) },
		func() (domain.SetupState, error) { return setup.SubmitToolchain(ctx, true) },
		func() (domain.SetupState, error) { return setup.SubmitMetadataPolicy(ctx, plan.MetadataPolicy) },
		func() (domain.SetupState, error) { return setup.SubmitNetwork(ctx, plan.Network) },
	)
	if _, err := s.RegisterLibrary(ctx, "Elsewhere", shows); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadSetupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audits := setupCount(t, ctx, s, `SELECT count(*) FROM audit_logs`)
	if _, err = setup.Finish(ctx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("finish with a taken root: %v", err)
	}
	after, err := s.LoadSetupState(ctx)
	if err != nil || after.Completed() || after.Version != before.Version || after.Current != domain.SetupStepComplete {
		t.Fatalf("state changed by a failed completion: %+v %v", after, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM libraries WHERE name IN ('Movies','Shows')`) != 0 || setupCount(t, ctx, s, `SELECT count(*) FROM audit_logs`) != audits {
		t.Fatal("failed completion left libraries or audit rows behind")
	}
	if status, err := setup.Status(ctx); err != nil || status.Completed {
		t.Fatal("failed completion counted as complete")
	}
	// A deactivated wizard administrator also refuses completion.
	if _, err = s.Pool.Exec(ctx, `DELETE FROM libraries WHERE name='Elsewhere'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE users SET disabled=true`); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Finish(ctx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("finish without an active wizard administrator: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE users SET disabled=false`); err != nil {
		t.Fatal(err)
	}
	if done, err := setup.Finish(ctx); err != nil || !done.Completed() {
		t.Fatalf("retry after rollback: %v", err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM libraries WHERE name IN ('Movies','Shows') AND nfo_mode='off' AND metadata_language='en-US'`) != 2 {
		t.Fatal("retry did not write the libraries")
	}
}

// Concurrent writers with the same version: exactly one wins, and two racing
// admin steps create exactly one administrator.
func TestSetupPostgresCompareAndSwap(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	state, err := s.SaveSetupState(ctx, domain.SetupState{Current: domain.SetupStepAdmin, Locale: "en-US"})
	if err != nil || state.Version != 1 {
		t.Fatalf("first save: %+v %v", state, err)
	}
	if _, err = s.SaveSetupState(ctx, domain.SetupState{Current: domain.SetupStepAdmin}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second insert: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := state
			next.Current = domain.SetupStepDatabase
			_, err := s.CreateSetupAdmin(ctx, domain.UserInput{Name: "Racer", Locale: "en-US", PasswordHash: accountTestHash}, next)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, domain.ErrConflict):
			t.Errorf("racing admin step: %v", err)
		}
	}
	if wins != 1 || setupCount(t, ctx, s, `SELECT count(*) FROM users`) != 1 {
		t.Fatalf("wins=%d", wins)
	}
	// A stale writer cannot overwrite the newer state.
	stale := state
	stale.Current = domain.SetupStepLanguage
	if _, err = s.SaveSetupState(ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale save: %v", err)
	}
	// An administrator created outside the wizard blocks the wizard's own.
	ctx2, s2, _ := accountTestStore(t)
	if _, err = s2.BootstrapAdmin(ctx2, accountInput("Outside")); err != nil {
		t.Fatal(err)
	}
	fresh, err := s2.SaveSetupState(ctx2, domain.SetupState{Current: domain.SetupStepAdmin, Locale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	fresh.Current = domain.SetupStepDatabase
	if _, err = s2.CreateSetupAdmin(ctx2, accountInput("Wizard"), fresh); !errors.Is(err, domain.ErrSetupAdminExists) {
		t.Fatalf("out-of-band administrator: %v", err)
	}
}

// Upgrading a deployment that already served must not lock it behind the
// wizard: the migration records an adopted, completed setup. A fresh
// database stays gated, and an unfinished wizard blocks the downgrade.
func TestSetupMigrationAdoptsExistingDeploymentUpDownUp(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	setup, _ := newSetupForTest(t, s)
	status, err := setup.Status(ctx)
	if err != nil || status.Completed {
		t.Fatalf("fresh database must require setup: %+v %v", status, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM setup_state`) != 0 {
		t.Fatal("fresh migration stored a setup row")
	}
	// An existing deployment: accounts and libraries made before upgrading.
	if _, err = s.BootstrapAdmin(ctx, accountInput("Operator")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterLibrary(ctx, "Existing", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	down := func() (uint, bool, error) { return Migrate(ctx, dsn, "down") }
	version, dirty, err := down()
	if err != nil || dirty || version >= SchemaVersion {
		t.Fatalf("down: version=%d dirty=%t err=%v", version, dirty, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM pg_class WHERE relname='setup_state' AND relnamespace=current_schema()::regnamespace`) != 0 {
		t.Fatal("down kept setup_state")
	}
	if version, dirty, err = Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("up: version=%d dirty=%t err=%v", version, dirty, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM setup_state WHERE adopted AND completed_at IS NOT NULL AND current_step='complete'`) != 1 {
		t.Fatal("upgrade did not adopt the existing deployment")
	}
	// Even with no active administrator left (only a regular account and a
	// library), the adopted row keeps the server open.
	if _, err = s.Pool.Exec(ctx, `UPDATE users SET is_admin=false`); err != nil {
		t.Fatal(err)
	}
	status, err = setup.Status(ctx)
	if err != nil || !status.Completed || status.State.CompletedAt == nil {
		t.Fatalf("adopted deployment gated: %+v %v", status, err)
	}
	// Adopted is final like a wizard completion; a second cycle keeps it.
	if version, dirty, err = down(); err != nil || dirty {
		t.Fatalf("second down: %d %t %v", version, dirty, err)
	}
	if version, dirty, err = Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("second up: %d %t %v", version, dirty, err)
	}
	if status, err = setup.Status(ctx); err != nil || !status.Completed {
		t.Fatal("second upgrade gated the deployment")
	}
}

func TestSetupMigrationRefusesDowngradeOfUnfinishedWizard(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	if _, err := s.SaveSetupState(ctx, domain.SetupState{Current: domain.SetupStepAdmin, Locale: "en-US"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Migrate(ctx, dsn, "down"); err == nil {
		t.Fatal("unfinished wizard downgraded")
	}
	version, dirty, err := Migrate(ctx, dsn, "status")
	if err != nil || !dirty || version >= SchemaVersion {
		t.Fatalf("refused downgrade state: version=%d dirty=%t err=%v", version, dirty, err)
	}
	if setupCount(t, ctx, s, `SELECT count(*) FROM setup_state WHERE completed_at IS NULL`) != 1 {
		t.Fatal("refused downgrade lost the wizard row")
	}
}

// An administrator created out of band on a fresh database (for example
// `jelee-cli account bootstrap`) adopts the instance at runtime too.
func TestSetupPostgresRuntimeAdoption(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	setup, _ := newSetupForTest(t, s)
	if _, err := s.BootstrapAdmin(ctx, accountInput("Bootstrap")); err != nil {
		t.Fatal(err)
	}
	status, err := setup.Status(ctx)
	if err != nil || !status.Completed || !status.Adopted {
		t.Fatalf("bootstrap-only install gated: %+v %v", status, err)
	}
	facts, err := s.SetupDatabaseFacts(ctx)
	if err != nil || facts.ServerVersion < 100000 || facts.SchemaVersion != SchemaVersion || facts.Dirty {
		t.Fatalf("database facts: %+v %v", facts, err)
	}
}
