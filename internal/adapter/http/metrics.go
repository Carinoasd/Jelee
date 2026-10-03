package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const metricsTimeout = 3 * time.Second

func (s *Server) metricsRoutes(router chi.Router) {
	router.With(s.metricsBudget, s.authenticate).Get("/metrics", s.serveMetrics)
}

// Keep authentication and collection in the same bounded admission budget.
// The handler remains synchronous so cancellation never leaves detached work.
func (s *Server) metricsBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), metricsTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		if err := ctx.Err(); err != nil {
			WriteError(w, r, err)
			return
		}
		select {
		case s.metricsSlots <- struct{}{}:
			defer func() { <-s.metricsSlots }()
		default:
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, http.StatusServiceUnavailable, "metrics_busy", "Metrics service is busy. Try again later.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, domain.ErrUnauthenticated)
		return
	}
	if !principal.Admin {
		WriteError(w, r, domain.ErrForbidden)
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := r.Context().Err(); err != nil {
		WriteError(w, r, err)
		return
	}
	s.metrics.ServeHTTP(w, r)
}
