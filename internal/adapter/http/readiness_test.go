package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
)

func readinessServer(t *testing.T, backend *fakeBackend, states func(context.Context) map[string]string) http.Handler {
	t.Helper()
	h, err := newServer(validConfig(), backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil, nil, nil, nil, []Option{WithReadiness(states)})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func readyzBody(h http.Handler) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/readyz", nil))
	return w.Code, w.Body.String()
}

// G50.5: /readyz lists dependency states as fixed codes and nothing else,
// keeps 200 while the database is usable and adds the states to 503.
func TestReadinessListsDependencyCodesOnly(t *testing.T) {
	states := func(context.Context) map[string]string {
		return map[string]string{"database": "ok", "schema": "current", "jobs": "stalled", "leak": "postgres://root:secret@db/x", "version": "83", "Bad Key": "ok"}
	}
	backend := &fakeBackend{}
	code, body := readyzBody(readinessServer(t, backend, states))
	if code != 200 || !strings.Contains(body, `"status":"ready"`) || !strings.Contains(body, `"jobs":"stalled"`) || !strings.Contains(body, `"schema":"current"`) {
		t.Fatalf("ready: %d %s", code, body)
	}
	for _, leaked := range []string{"secret", "83", "Bad Key", "leak"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("readiness leaked %q: %s", leaked, body)
		}
	}
	backend.readyError = errors.New("postgres://root:secret@192.168.0.15/private")
	code, body = readyzBody(readinessServer(t, backend, func(context.Context) map[string]string {
		return map[string]string{"database": "unavailable", "schema": "unknown"}
	}))
	if code != 503 || !strings.Contains(body, `"not_ready"`) || !strings.Contains(body, `"database":"unavailable"`) || strings.Contains(body, "192.168") {
		t.Fatalf("not ready: %d %s", code, body)
	}
	// Without states the document is unchanged.
	backend.readyError = nil
	if code, body = readyzBody(readinessServer(t, backend, nil)); code != 200 || strings.Contains(body, "checks") {
		t.Fatalf("no states: %d %s", code, body)
	}
}
