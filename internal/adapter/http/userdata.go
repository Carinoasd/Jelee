package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// userDataExportLimit is the number of personal data exports one instance
// streams at once; each holds a database snapshot for its duration.
const userDataExportLimit = 2

// userDataExportGate admits at most userDataExportLimit exports, and one per
// user. The zero value is ready.
type userDataExportGate struct {
	mu      sync.Mutex
	running map[string]bool
}

func (g *userDataExportGate) enter(userID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[userID] || len(g.running) >= userDataExportLimit {
		return false
	}
	if g.running == nil {
		g.running = map[string]bool{}
	}
	g.running[userID] = true
	return true
}

func (g *userDataExportGate) leave(userID string) {
	g.mu.Lock()
	delete(g.running, userID)
	g.mu.Unlock()
}

// userDataRoutes serve the data rights of G07.7: the personal data export
// and the permanent deletion of an account.
func (s *Server) userDataRoutes(r chi.Router) {
	r.Get("/api/v1/users/{id}/data-export", s.userDataExport)
	r.Post("/api/v1/users/me/purge", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Password     *string `json:"password"`
			Code         string  `json:"code"`
			RecoveryCode string  `json:"recoveryCode"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Password == nil || input.Code != "" && input.RecoveryCode != "" {
			return nil, 0, domain.ErrInvalid
		}
		// The password and the code share the second factor budget, like
		// disabling the second factor.
		if err := s.allowSecondFactor(w, a.IP, "user:"+a.UserID); err != nil {
			return nil, 0, err
		}
		err := s.accounts.PurgeSelf(r.Context(), a, *input.Password, input.Code, input.RecoveryCode)
		if err == nil {
			clearRevokedSessionCookie(w, r)
		}
		return nil, 204, err
	}))
	r.Post("/api/v1/users/{id}/purge", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.PurgeUser(r.Context(), a, chi.URLParam(r, "id"))
	}))
}

// userDataExport streams the personal data of self or, for administrators,
// of any user as NDJSON. Authorization and the audit record happen before
// the status is sent; completion is reported by the final "end" record and
// the X-Jelee-Export-Complete trailer.
func (s *Server) userDataExport(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, domain.ErrUnauthenticated)
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if !domain.ValidID(id) {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	if !principal.Admin && id != principal.UserID {
		WriteError(w, r, domain.ErrForbidden)
		return
	}
	if !s.userDataExports.enter(principal.UserID) {
		w.Header().Set("Retry-After", "30")
		WriteError(w, r, domain.ErrConflict)
		return
	}
	defer s.userDataExports.leave(principal.UserID)
	ctx, cancel := context.WithTimeout(r.Context(), domain.UserDataExportTimeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(domain.UserDataExportTimeout))
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}
	var (
		buffered *bufio.Writer
		lines    *json.Encoder
		records  int
	)
	begin := func(header domain.UserDataExportHeader) error {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Type", "application/x-ndjson")
		h.Set("Content-Disposition", `attachment; filename="jelee-user-data-`+header.UserID+`.ndjson"`)
		h.Set("Trailer", "X-Jelee-Export-Complete")
		w.WriteHeader(http.StatusOK)
		buffered = bufio.NewWriterSize(w, 32<<10)
		lines = json.NewEncoder(buffered)
		data, err := json.Marshal(header)
		if err != nil {
			return err
		}
		return lines.Encode(domain.UserDataRecord{Type: "export", Data: data})
	}
	write := func(record domain.UserDataRecord) error {
		records++
		return lines.Encode(record)
	}
	err := s.accounts.ExportUserData(ctx, actor, id, begin, write)
	if lines == nil {
		if err == nil {
			err = domain.ErrDatabase
		}
		WriteError(w, r, err)
		return
	}
	complete := "true"
	if err == nil {
		err = lines.Encode(domain.UserDataRecord{Type: "end", Data: json.RawMessage(`{"records":` + strconv.Itoa(records) + `}`)})
	}
	if err == nil {
		err = buffered.Flush()
	} else {
		_ = buffered.Flush()
	}
	if err != nil {
		complete = "false"
		s.logger.WarnContext(ctx, "user data export ended early", "component", "accounts", "requestId", w.Header().Get("X-Request-ID"))
	}
	w.Header().Set("X-Jelee-Export-Complete", complete)
}
