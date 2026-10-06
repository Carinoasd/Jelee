package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// httpRepairRepository is a library without problems; only the run
// bookkeeping is recorded.
type httpRepairRepository struct {
	started, finished int
	actor             domain.Actor
	reverted          string
}

func (*httpRepairRepository) ConsistencyLibraries(context.Context, string) ([]domain.ConsistencyLibrary, error) {
	return nil, nil
}
func (*httpRepairRepository) ConsistencyPage(context.Context, string, domain.ConsistencyScope, string) (domain.ConsistencyPage, error) {
	return domain.ConsistencyPage{}, nil
}
func (*httpRepairRepository) RepairStatsPage(context.Context, domain.ConsistencyScope, string) (domain.ConsistencyPage, error) {
	return domain.ConsistencyPage{}, nil
}
func (*httpRepairRepository) RepairVariantPage(context.Context, string) (domain.ConsistencyPage, error) {
	return domain.ConsistencyPage{}, nil
}
func (*httpRepairRepository) RepairActiveJob(context.Context, string) (domain.RepairActiveJob, bool, error) {
	return domain.RepairActiveJob{}, false, nil
}
func (r *httpRepairRepository) StartRepairRun(_ context.Context, run domain.RepairRun) (domain.RepairRun, error) {
	r.started++
	r.actor = run.Actor
	run.ID, run.StartedAt = "00000000-0000-4000-8000-0000000000aa", time.Now()
	return run, nil
}
func (*httpRepairRepository) ApplyRepairFixes(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return domain.ConsistencyFixResult{}, nil
}
func (*httpRepairRepository) PurgeRepairProbeCache(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return domain.ConsistencyFixResult{}, nil
}
func (*httpRepairRepository) PurgeRepairVariantRows(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return domain.ConsistencyFixResult{}, nil
}
func (*httpRepairRepository) EnqueueRepairCatalogSync(context.Context, domain.RepairRun, string, domain.JobPolicy) (domain.RepairJob, error) {
	return domain.RepairJob{}, domain.ErrInvalid
}
func (r *httpRepairRepository) FinishRepairRun(context.Context, domain.RepairRun, domain.RepairResult) error {
	r.finished++
	return nil
}
func (r *httpRepairRepository) RevertRepairRun(_ context.Context, _ domain.Actor, run string) (domain.RepairRevertResult, error) {
	r.reverted = run
	return domain.RepairRevertResult{RunID: run, Reverted: 2}, nil
}

type httpVariantStore struct{ entries int64 }

func (s *httpVariantStore) LiveVariants() (int64, int64) { return s.entries, s.entries * 100 }
func (s *httpVariantStore) ClearVariants(context.Context) error {
	s.entries = 0
	return nil
}

type httpNoFiles struct{}

func (httpNoFiles) FileExists(context.Context, string, string) (bool, error) { return false, nil }

type httpPaths struct{}

func (httpPaths) RenderPath(string, string) string { return "[redacted]" }

func httpRepairer(t *testing.T, repository app.RepairRepository, store app.RepairVariantStore) *app.Repairer {
	t.Helper()
	r, err := app.NewRepairer(repository, httpNoFiles{}, nil, httpPaths{})
	if err != nil {
		t.Fatal(err)
	}
	return r.WithServer(nil, store)
}

func newRepairHTTP(t *testing.T, repairer *app.Repairer) http.Handler {
	t.Helper()
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.EnableJobs = true
	cfg.Jobs = config.DefaultJobsConfig()
	cfg.MaxConnections = 8
	accounts, err := app.NewAccounts(httpAccountRepository{}, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		if token == strings.Repeat("a", 43) || token == strings.Repeat("u", 43) {
			return access.Principal{UserID: userID, SessionID: sessionID, Admin: token == strings.Repeat("a", 43), Locale: "zh-TW"}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
	h, err := newServer(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, nil, nil, nil, nil,
		[]Option{WithRepair(repairer, app.RepairOptions{Policy: cfg.Jobs.Policy()})})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func decodeRepair[T any](t *testing.T, body string) T {
	t.Helper()
	var envelope struct{ Data T }
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err, body)
	}
	return envelope.Data
}

func TestRepairRoutesAreAdministratorOnly(t *testing.T) {
	h := newRepairHTTP(t, httpRepairer(t, &httpRepairRepository{}, &httpVariantStore{}))
	body := `{"action":"stats","dryRun":true}`
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", body, ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", body, "u"); w.Code != 403 {
		t.Fatalf("user: %d", w.Code)
	}
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs/00000000-0000-4000-8000-0000000000aa/revert", `{"iUnderstand":true}`, "u"); w.Code != 403 {
		t.Fatalf("user revert: %d", w.Code)
	}
}

func TestRepairExecutionNeedsConfirmation(t *testing.T) {
	repository := &httpRepairRepository{}
	h := newRepairHTTP(t, httpRepairer(t, repository, &httpVariantStore{}))
	for _, body := range []string{`{"action":"counts"}`, `{"action":"counts","dryRun":false,"iUnderstand":false}`} {
		if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", body, "a"); w.Code != 400 || !strings.Contains(w.Body.String(), "confirmation_required") {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs/00000000-0000-4000-8000-0000000000aa/revert", `{}`, "a"); w.Code != 400 || !strings.Contains(w.Body.String(), "confirmation_required") {
		t.Fatalf("revert without confirmation: %d", w.Code)
	}
	for _, body := range []string{`{"action":"rebuild"}`, `{"action":"stats","dryRun":true,"libraryId":"x"}`, `{"action":"stats","dryRun":true,"statBudget":-1}`, `{"action":"image-variants","dryRun":true,"libraryId":"00000000-0000-4000-8000-0000000000bb"}`} {
		if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", body, "a"); w.Code != 400 {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if repository.started != 0 {
		t.Fatal("a refused request started a run")
	}
}

func TestRepairImageVariantsDryRunMatchesExecution(t *testing.T) {
	repository, store := &httpRepairRepository{}, &httpVariantStore{entries: 7}
	h := newRepairHTTP(t, httpRepairer(t, repository, store))
	w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", `{"action":"image-variants","dryRun":true}`, "a")
	dry := decodeRepair[domain.RepairResult](t, w.Body.String())
	if w.Code != 200 || dry.Planned != 7 || dry.Applied != 0 || !dry.DryRun || dry.RunID != "" || repository.started != 0 || store.entries != 7 || dry.Info["bytes"] != 700 {
		t.Fatalf("dry run: %d %+v", w.Code, dry)
	}
	w = jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", `{"action":"image-variants","iUnderstand":true}`, "a")
	done := decodeRepair[domain.RepairResult](t, w.Body.String())
	if w.Code != 200 || done.Planned != 7 || done.Applied != 7 || done.State != domain.RepairStateCompleted || done.Revertible || store.entries != 0 || repository.finished != 1 {
		t.Fatalf("execution: %d %+v", w.Code, done)
	}
	if repository.actor.UserID != userID || done.Origin != domain.RepairOriginAPI {
		t.Fatalf("run actor %+v origin %s", repository.actor, done.Origin)
	}
	w = jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", `{"action":"image-variants","iUnderstand":true}`, "a")
	if again := decodeRepair[domain.RepairResult](t, w.Body.String()); w.Code != 200 || again.Planned != 0 || again.Applied != 0 {
		t.Fatalf("rerun: %+v", again)
	}
	w = jobRequest(h, http.MethodPost, "/api/v1/admin/repairs/00000000-0000-4000-8000-0000000000aa/revert", `{"iUnderstand":true}`, "a")
	if reverted := decodeRepair[domain.RepairRevertResult](t, w.Body.String()); w.Code != 200 || reverted.Reverted != 2 || repository.reverted != "00000000-0000-4000-8000-0000000000aa" {
		t.Fatalf("revert: %d %+v", w.Code, reverted)
	}
}

func TestRepairUnavailableActionsCarryTheirReason(t *testing.T) {
	h := newRepairHTTP(t, httpRepairer(t, &httpRepairRepository{}, nil))
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", `{"action":"image-variants","dryRun":true}`, "a"); w.Code != 404 || !strings.Contains(w.Body.String(), "image_unavailable") {
		t.Fatalf("image store off: %d %s", w.Code, w.Body.String())
	}
	if w := jobRequest(h, http.MethodPost, "/api/v1/admin/repairs", `{"action":"nfo","dryRun":true}`, "a"); w.Code != 503 || !strings.Contains(w.Body.String(), "nfo_reader_unavailable") {
		t.Fatalf("scan pipeline off: %d %s", w.Code, w.Body.String())
	}
}
