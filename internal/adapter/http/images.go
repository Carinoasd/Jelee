package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) imageRoutes(router chi.Router) {
	router.With(s.imageBudget, s.authenticate).Get("/images/{type}/{id}", s.image)
	router.With(s.imageBudget, s.authenticate).Head("/images/{type}/{id}", s.image)
}

// Authentication, both catalog checks, rendering and response writes share
// the same non-queueing HTTP admission and deadline.
func (s *Server) imageBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.cfg.Images.TimeoutSeconds)*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		if err := ctx.Err(); err != nil {
			WriteError(w, r, err)
			return
		}
		select {
		case s.imageSlots <- struct{}{}:
			defer func() { <-s.imageSlots }()
		default:
			WriteError(w, r, domain.ErrImageBusy)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, domain.ErrUnauthenticated)
		return
	}
	query, err := strictQuery(r, "width", "height", "quality", "format")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	request := domain.ImageRequest{Type: chi.URLParam(r, "type")}
	for key, target := range map[string]*int{"width": &request.Width, "height": &request.Height, "quality": &request.Quality} {
		if raw, exists := query[key]; exists {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil || raw != strconv.Itoa(value) || value <= 0 {
				WriteError(w, r, domain.ErrInvalid)
				return
			}
			*target = value
		}
	}
	if format, exists := query["format"]; exists {
		if format == "" {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
		request.Format = format
	}
	if _, err = domain.NormalizeImageRequest(request); err != nil {
		WriteError(w, r, err)
		return
	}
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}
	result, err := s.images.Get(r.Context(), actor, chi.URLParam(r, "id"), request)
	if result.Body != nil {
		defer result.Body.Close()
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if result.Body == nil || result.ContentType != "image/jpeg" || result.Size <= 0 || result.Size > s.cfg.Images.MaxOutputBytes ||
		result.Width <= 0 || result.Height <= 0 || result.Width > s.cfg.Images.MaxOutputDimension || result.Height > s.cfg.Images.MaxOutputDimension ||
		!validImageETag(result.ETag) {
		WriteError(w, r, domain.ErrImageUnavailable)
		return
	}
	if err := r.Context().Err(); err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	w.Header().Add("Vary", "Authorization")
	if imageNotModified(r.Header.Values("If-None-Match"), result.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(result.Size, 10))
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// Never copy beyond the checked representation size. A short read or
		// failed write ends this response and still releases the held body.
		_, _ = io.CopyN(w, result.Body, result.Size)
	}
}

func validImageETag(value string) bool {
	if len(value) != 66 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, c := range value[1 : len(value)-1] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func imageNotModified(values []string, etag string) bool {
	// Bound parsing even when a caller bypasses net/http's header limits.
	size := 0
	for _, value := range values {
		size += len(value)
		if size > 4096 {
			return false
		}
	}
	matched := false
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" {
				return len(values) == 1 && strings.TrimSpace(value) == "*"
			}
			candidate = strings.TrimPrefix(candidate, "W/")
			if len(candidate) < 2 || candidate[0] != '"' || candidate[len(candidate)-1] != '"' || strings.ContainsFunc(candidate[1:len(candidate)-1], func(c rune) bool { return c < 0x21 || c == '"' || c == 0x7f }) {
				return false
			}
			matched = matched || candidate == etag
		}
	}
	return matched
}
