package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/netaddr"
)

type setupStatusRepository struct {
	completed bool
	err       error
}

func (r setupStatusRepository) LoadSetupState(context.Context) (domain.SetupState, error) {
	if r.err != nil {
		return domain.SetupState{}, r.err
	}
	if !r.completed {
		return domain.SetupState{}, domain.ErrNotFound
	}
	at := time.Unix(1_800_000_000, 0)
	return domain.SetupState{Version: 1, Current: domain.SetupStepComplete, CompletedAt: &at}, nil
}
func (setupStatusRepository) HasActiveAdmin(context.Context) (bool, error) { return false, nil }
func (setupStatusRepository) SaveSetupState(context.Context, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, domain.ErrConflict
}
func (setupStatusRepository) CreateSetupAdmin(context.Context, domain.UserInput, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, domain.ErrConflict
}
func (setupStatusRepository) CompleteSetup(context.Context, domain.SetupState) (domain.SetupState, error) {
	return domain.SetupState{}, domain.ErrConflict
}

type setupNoEnvironment struct{ netaddr.Setup }

func (setupNoEnvironment) DatabaseStatus(context.Context) (app.SetupDatabaseStatus, error) {
	return app.SetupDatabaseStatus{}, nil
}
func (setupNoEnvironment) InspectDirectory(context.Context, string) (app.SetupDirectoryStatus, error) {
	return app.SetupDirectoryStatus{}, nil
}
func (setupNoEnvironment) ListenAvailable(context.Context, string) (bool, error) { return false, nil }
func (setupNoEnvironment) DetectTools(context.Context) (app.SetupToolReport, error) {
	return app.SetupToolReport{}, nil
}
func (setupNoEnvironment) TMDBCredentialConfigured() bool { return false }
func (setupNoEnvironment) Hash(context.Context, string) (string, error) {
	return "", errors.New("unused")
}

func setupForStartup(t *testing.T, repository setupStatusRepository) *app.Setup {
	t.Helper()
	setup, err := app.NewSetup(repository, setupNoEnvironment{}, setupNoEnvironment{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func captureSetupOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	previous := setupTokenOutput
	setupTokenOutput = &out
	t.Cleanup(func() { setupTokenOutput = previous })
	return &out
}

func TestPrepareSetupIssuesTokenOnlyWhenIncomplete(t *testing.T) {
	out := captureSetupOutput(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx := context.Background()
	token, err := prepareSetup(ctx, config.Config{}, setupForStartup(t, setupStatusRepository{completed: true}), logger)
	if err != nil || token != "" || out.Len() != 0 {
		t.Fatalf("completed: token=%q out=%q err=%v", token, out.String(), err)
	}
	first, err := prepareSetup(ctx, config.Config{}, setupForStartup(t, setupStatusRepository{}), logger)
	if err != nil || len(first) != 43 || !strings.Contains(out.String(), first) {
		t.Fatalf("incomplete: token=%q out=%q err=%v", first, out.String(), err)
	}
	if strings.Contains(logs.String(), first) {
		t.Fatal("setup token reached the log")
	}
	second, _ := prepareSetup(ctx, config.Config{}, setupForStartup(t, setupStatusRepository{}), logger)
	if second == first {
		t.Fatal("setup token reused across starts")
	}
	if _, err = prepareSetup(ctx, config.Config{}, setupForStartup(t, setupStatusRepository{err: domain.ErrDatabase}), logger); err == nil {
		t.Fatal("unreadable setup state started the server")
	}
}

func TestPrepareSetupWritesPrivateTokenFile(t *testing.T) {
	out := captureSetupOutput(t)
	path := filepath.Join(t.TempDir(), "setup-token")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, err := prepareSetup(context.Background(), config.Config{SetupTokenFile: path}, setupForStartup(t, setupStatusRepository{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || token == "" || out.Len() != 0 {
		t.Fatalf("token file: token=%q out=%q err=%v", token, out.String(), err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != token+"\n" {
		t.Fatalf("file content: %q %v", data, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode: %v %v", info.Mode(), err)
		}
	}
	missing := filepath.Join(t.TempDir(), "no", "such", "dir", "token")
	if _, err = prepareSetup(context.Background(), config.Config{SetupTokenFile: missing}, setupForStartup(t, setupStatusRepository{}), slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("unwritable token file ignored")
	}
}
