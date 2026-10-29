package httpapi

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/app"
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
	query, err := strictQuery(r, "width", "height", "fillWidth", "fillHeight", "quality", "format", "index", "tag")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	request := domain.ImageRequest{Type: chi.URLParam(r, "type")}
	if raw, exists := query["index"]; exists {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || raw != strconv.Itoa(value) || value < 0 || value > domain.ItemImageMaxIndex {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
		request.Index = value
	}
	tag, tagged := query["tag"]
	if tagged && !validImageTag(tag) {
		WriteError(w, r, domain.ErrInvalid)
		return
	}
	var fillWidth, fillHeight int
	for key, target := range map[string]*int{"width": &request.Width, "height": &request.Height, "quality": &request.Quality, "fillWidth": &fillWidth, "fillHeight": &fillHeight} {
		if raw, exists := query[key]; exists {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil || raw != strconv.Itoa(value) || value <= 0 {
				WriteError(w, r, domain.ErrInvalid)
				return
			}
			*target = value
		}
	}
	// A fill box crops (G40.6): both sides are required and it replaces the
	// fitted width and height.
	_, fillW := query["fillWidth"]
	_, fillH := query["fillHeight"]
	if fillW || fillH {
		if fillWidth == 0 || fillHeight == 0 || request.Width != 0 || request.Height != 0 {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
		request.Width, request.Height, request.Crop = fillWidth, fillHeight, true
	}
	format, exists := query["format"]
	if exists && format == "" {
		WriteError(w, r, domain.ErrInvalid)
		return
	}
	if request.Format, err = s.imageFormat(format, false); err != nil {
		WriteError(w, r, err)
		return
	}
	if _, err = domain.NormalizeImageRequest(request); err != nil {
		WriteError(w, r, err)
		return
	}
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}
	result, err := s.renderImage(r.Context(), actor, chi.URLParam(r, "id"), request)
	if result.Body != nil {
		defer result.Body.Close()
	}
	if err != nil {
		WriteError(w, r, s.hiddenContentError(err))
		return
	}
	writeImage(w, r, result, tag, tagged, "Authorization, Cookie")
}

// renderImage runs the shared image pipeline for an authenticated actor and
// checks the representation against the configured limits. The caller
// closes a non-nil result.Body, also on error.
func (s *Server) renderImage(ctx context.Context, actor domain.Actor, itemID string, request domain.ImageRequest) (app.ImageResult, error) {
	result, err := s.images.Get(ctx, actor, itemID, request)
	if err != nil {
		return result, err
	}
	if result.Body == nil || result.ContentType != imageContentType(request.Format) || result.Size <= 0 || result.Size > s.cfg.Images.MaxOutputBytes ||
		result.Width <= 0 || result.Height <= 0 || result.Width > s.cfg.Images.MaxOutputDimension || result.Height > s.cfg.Images.MaxOutputDimension ||
		!validImageETag(result.ETag) {
		return result, domain.ErrImageUnavailable
	}
	return result, ctx.Err()
}

// writeImage writes a checked representation: validators, cache policy, a
// 304 for a matching If-None-Match, and the body unless the method is HEAD.
// vary names the request headers that carry the caller's credentials.
func writeImage(w http.ResponseWriter, r *http.Request, result app.ImageResult, tag string, tagged bool, vary string) {
	w.Header().Set("ETag", result.ETag)
	// A tag that names the original's content digest makes the URL change
	// whenever the image does, so it may be cached for a year (G40.8). Any
	// other tag keeps the revalidated private policy. Vary still keys every
	// cached copy to the caller's credentials.
	if tagged && imageTagMatches(tag, result.ContentSHA256) {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	}
	w.Header().Add("Vary", vary)
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

// compatImages lets the compatibility layer serve item images through the
// same pipeline, admission and deadline as /images. The layer parses its own
// parameters and writes its own errors; nothing here is duplicated there.
type compatImages struct{ s *Server }

func (c compatImages) ServeImage(w http.ResponseWriter, r *http.Request, actor domain.Actor, image compat.ImageDelivery) error {
	s := c.s
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.cfg.Images.TimeoutSeconds)*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.imageSlots <- struct{}{}:
		defer func() { <-s.imageSlots }()
	default:
		return domain.ErrImageBusy
	}
	// Upstream clients name a format they can display; one this server does
	// not offer falls back to the configured default instead of failing.
	format, err := s.imageFormat(image.Request.Format, true)
	if err != nil {
		return err
	}
	image.Request.Format = format
	result, err := s.renderImage(ctx, actor, image.ItemID, image.Request)
	if result.Body != nil {
		defer result.Body.Close()
	}
	if err != nil {
		return err
	}
	writeImage(w, r, result, image.Tag, image.Tag != "", image.Vary)
	return nil
}

func (c compatImages) ImageSummaries(ctx context.Context, userID string, itemIDs []string) (map[string][]domain.ItemImageSummary, error) {
	return c.s.images.Summaries(ctx, userID, itemIDs)
}

// imageFormat resolves a requested output format against the configuration
// (G40.6): empty selects the configured default. A known format that is not
// offered (AVIF always, WebP when disabled) is ErrImageUnsupported unless
// fallback is set, which selects the default instead; an unknown name is
// ErrInvalid.
func (s *Server) imageFormat(requested string, fallback bool) (string, error) {
	defaultFormat := s.cfg.Images.DefaultFormat
	if defaultFormat == "" {
		defaultFormat = domain.ImageFormatJPEG
	}
	switch {
	case requested == "":
		return defaultFormat, nil
	case s.cfg.Images.AllowsFormat(requested):
		return requested, nil
	case requested != domain.ImageFormatAVIF && !domain.ValidImageOutputFormat(requested):
		return "", domain.ErrInvalid
	case fallback:
		return defaultFormat, nil
	}
	return "", domain.ErrImageUnsupported
}

func imageContentType(format string) string {
	if format == domain.ImageFormatWebP {
		return "image/webp"
	}
	return "image/jpeg"
}

// validImageTag accepts 1–128 hexadecimal digits in either case; only the
// full lowercase SHA-256 of the original can ever match.
func validImageTag(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func imageTagMatches(tag string, content [32]byte) bool {
	return content != [32]byte{} && strings.EqualFold(tag, hex.EncodeToString(content[:]))
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
