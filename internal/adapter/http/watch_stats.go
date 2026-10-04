package httpapi

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// watchStatsExportTimeout bounds one export stream.
const watchStatsExportTimeout = 2 * time.Minute

// watchStatsRoutes serves watch statistics (G23.3, G23.4). Every route
// reads the daily roll-up only, answers web and native sessions alike and
// carries no delivery URL: statistics name items, never their files.
func (s *Server) watchStatsRoutes(r chi.Router) {
	r.Get("/api/v1/users/me/watch-stats", s.accountEndpoint(false, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		return s.watchStatsReport(r, a, a.UserID)
	}))
	r.Get("/api/v1/users/{id}/watch-stats", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		id := chi.URLParam(r, "id")
		if !domain.ValidID(id) {
			return nil, 0, domain.ErrNotFound
		}
		return s.watchStatsReport(r, a, id)
	}))
	r.Get("/api/v1/watch-stats", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		return s.watchStatsReport(r, a, "")
	}))
	r.Get("/api/v1/watch-stats/export", s.watchStatsExport)
}

func (s *Server) watchStatsReport(r *http.Request, actor domain.Actor, subject string) (any, int, error) {
	query, err := strictQuery(r, "from", "to", "period", "top")
	if err != nil {
		return nil, 0, err
	}
	request := domain.WatchStatsRequest{SubjectID: subject, From: query["from"], To: query["to"], Period: query["period"]}
	if raw, ok := query["top"]; ok {
		if request.Top, err = strconv.Atoi(raw); err != nil || request.Top < 1 {
			return nil, 0, domain.ErrInvalid
		}
	}
	report, err := s.catalog.WatchStats(r.Context(), actor, request)
	if err != nil {
		return nil, 0, err
	}
	return report, http.StatusOK, nil
}

// csvSafe keeps a text cell from being read as a spreadsheet formula.
func csvSafe(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

var watchStatsCSVHeader = []string{"day", "user_id", "user_name", "item_id", "library_id", "item_kind", "item_title",
	"effective_seconds", "sessions", "views", "first_plays", "rewatches", "completions", "completion_rate"}

// watchStatsExport streams the daily rows of a range for administrators as
// CSV or NDJSON (G23.4). The export is counted, refused over its limit
// (409 stats_export_limit) and audited before the first byte is sent.
func (s *Server) watchStatsExport(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, domain.ErrUnauthenticated)
		return
	}
	if !principal.Admin {
		WriteError(w, r, domain.ErrForbidden)
		return
	}
	query, err := strictQuery(r, "from", "to", "format", "limit", "userId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	request := app.WatchStatsExportRequest{SubjectID: query["userId"], From: query["from"], To: query["to"], Format: query["format"]}
	if raw, ok := query["limit"]; ok {
		if request.Limit, err = strconv.Atoi(raw); err != nil || request.Limit < 1 {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
	}
	if raw, ok := query["userId"]; ok && !domain.ValidID(raw) {
		WriteError(w, r, domain.ErrInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), watchStatsExportTimeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(watchStatsExportTimeout))
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}
	format := request.Format
	if format == "" {
		format = domain.WatchStatsExportCSV
	}
	var (
		buffered *bufio.Writer
		table    *csv.Writer
		lines    *json.Encoder
		started  bool
	)
	begin := func(rows int) error {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Jelee-Export-Rows", strconv.Itoa(rows))
		name := "watch-stats." + format
		if format == domain.WatchStatsExportCSV {
			h.Set("Content-Type", "text/csv; charset=utf-8")
		} else {
			h.Set("Content-Type", "application/x-ndjson")
		}
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
		h.Set("Trailer", "X-Jelee-Export-Complete")
		w.WriteHeader(http.StatusOK)
		started = true
		buffered = bufio.NewWriterSize(w, 32<<10)
		if format == domain.WatchStatsExportCSV {
			table = csv.NewWriter(buffered)
			return table.Write(watchStatsCSVHeader)
		}
		lines = json.NewEncoder(buffered)
		return nil
	}
	write := func(row domain.WatchStatsExportRow) error {
		if table != nil {
			return table.Write([]string{row.Day, row.UserID, csvSafe(row.UserName), row.ItemID, row.LibraryID, csvSafe(row.Kind), csvSafe(row.Title),
				strconv.FormatInt(row.EffectiveMillis/1000, 10), strconv.FormatInt(row.Sessions, 10), strconv.FormatInt(row.Views, 10),
				strconv.FormatInt(row.FirstPlays, 10), strconv.FormatInt(row.Rewatches, 10), strconv.FormatInt(row.Completions, 10),
				strconv.FormatFloat(row.CompletionRate, 'f', 3, 64)})
		}
		return lines.Encode(row)
	}
	err = s.catalog.ExportWatchStats(ctx, actor, request, begin, write)
	if !started {
		if err == nil {
			err = domain.ErrDatabase
		}
		WriteError(w, r, err)
		return
	}
	if table != nil {
		table.Flush()
	}
	if buffered != nil {
		_ = buffered.Flush()
	}
	// The status is sent before the rows, so completion is reported in a
	// trailer; X-Jelee-Export-Rows also lets a client check the row count.
	complete := "true"
	if err != nil {
		complete = "false"
		s.logger.Warn("watch statistics export ended early", "component", "watch_stats", "requestId", w.Header().Get("X-Request-ID"))
	}
	w.Header().Set("X-Jelee-Export-Complete", complete)
}
