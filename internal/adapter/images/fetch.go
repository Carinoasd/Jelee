package images

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// Remote image fetching for NFO-referenced URLs and TMDB artwork (G40.3,
// G40.9, G11.4). Network access only goes through the controlled outbound
// transport; this file owns URL policy, admission, retry governance and
// content checks. Errors carry a stable reason and never the URL, host,
// headers or response bytes.

// FetchSource marks where a remote image URL came from.
type FetchSource string

const (
	FetchSourceNFO  FetchSource = "nfo"
	FetchSourceTMDB FetchSource = "tmdb"
)

var (
	ErrFetchLocalOnly   = errors.New("image_fetch_local_only")
	ErrFetchInvalidURL  = errors.New("image_fetch_url_rejected")
	ErrFetchDenied      = errors.New("image_fetch_target_denied")
	ErrFetchRedirect    = errors.New("image_fetch_redirect_denied")
	ErrFetchTooLarge    = errors.New("image_fetch_too_large")
	ErrFetchContentType = errors.New("image_fetch_content_type_rejected")
	ErrFetchFormat      = errors.New("image_fetch_format_rejected")
	ErrFetchNotFound    = errors.New("image_fetch_not_found")
	ErrFetchRejected    = errors.New("image_fetch_status_rejected")
	ErrFetchRateLimited = errors.New("image_fetch_rate_limited")
	ErrFetchUnavailable = errors.New("image_fetch_unavailable")
	ErrFetchTimeout     = errors.New("image_fetch_timeout")
	ErrFetchStore       = errors.New("image_fetch_store_failed")
)

// FetchError adds safe diagnostics to a reason sentinel. Status is the last
// HTTP status seen (0 when none) and Attempts counts network attempts.
type FetchError struct {
	Reason   error
	Status   int
	Attempts int
}

func (e *FetchError) Error() string {
	text := e.Reason.Error()
	if e.Status != 0 {
		text += " status=" + strconv.Itoa(e.Status)
	}
	return text + " attempts=" + strconv.Itoa(e.Attempts)
}

func (e *FetchError) Unwrap() error { return e.Reason }

// FetchReason returns a stable, log-safe classification of a Fetch error.
func FetchReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	for _, known := range fetchReasons {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "image_fetch_failed"
}

var fetchReasons = []error{ErrFetchLocalOnly, ErrFetchInvalidURL, ErrFetchDenied, ErrFetchRedirect, ErrFetchTooLarge, ErrFetchContentType,
	ErrFetchFormat, ErrFetchNotFound, ErrFetchRejected, ErrFetchRateLimited, ErrFetchUnavailable, ErrFetchTimeout, ErrFetchStore}

// FetchTransport is satisfied by *outbound.Client.
type FetchTransport interface {
	Stream(context.Context, string, outbound.StreamOptions) (*outbound.StreamResponse, error)
}

// FetchSink persists fetched bytes. Put must read body to EOF before
// committing and must not commit when a read fails; the body fails with the
// fetch's reason when the size bound, deadline or connection breaks.
type FetchSink interface {
	Put(ctx context.Context, body io.Reader) (digest string, size int64, err error)
}

// FetchOptions are injected by the runtime; zero values select defaults.
type FetchOptions struct {
	// LocalOnly rejects every fetch before any network work.
	LocalOnly bool
	// MaxConcurrent bounds in-flight requests across all hosts (default 4).
	MaxConcurrent int
	// MaxPerHost bounds in-flight requests per host (default 2).
	MaxPerHost int
	// HostInterval spaces request starts to one host (default 250ms).
	HostInterval time.Duration
	// MaxBytes bounds one image body (default 20 MiB, at most 64 MiB).
	MaxBytes int64
	// MaxRedirects applies to NFO URLs only; TMDB never redirects (default 3,
	// negative disables redirects).
	MaxRedirects int
	// Timeout covers queueing, every attempt, retry waits and storage
	// (default 2m). AttemptTimeout covers one request and its body (default 45s).
	Timeout        time.Duration
	AttemptTimeout time.Duration
	// MaxAttempts includes the first attempt (default 3, at most 5).
	MaxAttempts int
	// BackoffBase doubles per retry with 0-25% jitter (default 500ms).
	BackoffBase time.Duration
	// MaxRetryAfter is the longest server-requested wait a retry honours;
	// longer requests fail now but still cool the host down (default 30s).
	MaxRetryAfter time.Duration
	// InvalidRetryCooldown applies to 429/503 without a usable Retry-After
	// value; such responses are not retried (default 15s).
	InvalidRetryCooldown time.Duration
}

const (
	maxFetchURLBytes  = 2048
	maxFetchCooldown  = time.Hour
	fetchHostPruneLen = 256
	fetchSniffBytes   = 64
	fetchAccept       = "image/avif,image/webp,image/png,image/jpeg,image/gif,image/bmp,image/tiff;q=0.9"
)

func (o FetchOptions) withDefaults() (FetchOptions, bool) {
	set := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = 4
	}
	if o.MaxPerHost == 0 {
		o.MaxPerHost = min(2, o.MaxConcurrent)
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 20 << 20
	}
	if o.MaxRedirects == 0 {
		o.MaxRedirects = 3
	} else if o.MaxRedirects < 0 {
		o.MaxRedirects = 0
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	set(&o.HostInterval, 250*time.Millisecond)
	set(&o.Timeout, 2*time.Minute)
	set(&o.AttemptTimeout, 45*time.Second)
	set(&o.BackoffBase, 500*time.Millisecond)
	set(&o.MaxRetryAfter, 30*time.Second)
	set(&o.InvalidRetryCooldown, 15*time.Second)
	ok := o.MaxConcurrent >= 1 && o.MaxConcurrent <= 64 && o.MaxPerHost >= 1 && o.MaxPerHost <= o.MaxConcurrent &&
		o.HostInterval > 0 && o.HostInterval <= time.Minute && o.MaxBytes >= 1 && o.MaxBytes <= outbound.MaxStreamBytes &&
		o.MaxRedirects >= 0 && o.MaxRedirects <= outbound.MaxStreamRedirects && o.MaxAttempts >= 1 && o.MaxAttempts <= 5 &&
		o.Timeout > 0 && o.Timeout <= time.Hour && o.AttemptTimeout > 0 && o.AttemptTimeout <= o.Timeout &&
		o.BackoffBase > 0 && o.BackoffBase <= time.Minute && o.MaxRetryAfter > 0 && o.MaxRetryAfter <= maxFetchCooldown &&
		o.InvalidRetryCooldown > 0 && o.InvalidRetryCooldown <= maxFetchCooldown
	return o, ok
}

// FetchRequest names one remote image. URL is never echoed in errors.
type FetchRequest struct {
	Source FetchSource
	URL    string
}

// FetchResult describes stored bytes. Format is the sniffed image format.
type FetchResult struct {
	Source    FetchSource
	Digest    string
	Size      int64
	Format    string
	Attempts  int
	FetchedAt time.Time
}

// RemoteFetcher governs remote image downloads. It owns no goroutines; all
// waiting happens in callers and is released on cancellation.
type RemoteFetcher struct {
	transport FetchTransport
	options   FetchOptions
	global    chan struct{}
	mu        sync.Mutex
	hosts     map[string]*fetchHost
	now       func() time.Time
}

type fetchHost struct {
	slots    chan struct{}
	next     time.Time
	cooldown time.Time
	users    int
}

// NewRemoteFetcher validates options. transport may be nil only in local-only
// mode, where no request can ever be issued.
func NewRemoteFetcher(transport FetchTransport, options FetchOptions) (*RemoteFetcher, error) {
	o, ok := options.withDefaults()
	if !ok || (transport == nil && !o.LocalOnly) {
		return nil, ErrFetchUnavailable
	}
	return &RemoteFetcher{transport: transport, options: o, global: make(chan struct{}, o.MaxConcurrent),
		hosts: make(map[string]*fetchHost), now: time.Now}, nil
}

// Fetch downloads one image into sink. Caller cancellation is returned as the
// context error; everything else is a *FetchError with a reason sentinel.
func (f *RemoteFetcher) Fetch(ctx context.Context, request FetchRequest, sink FetchSink) (FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return FetchResult{}, err
	}
	if f.options.LocalOnly {
		return FetchResult{}, &FetchError{Reason: ErrFetchLocalOnly}
	}
	if sink == nil {
		return FetchResult{}, &FetchError{Reason: ErrFetchStore}
	}
	target, host, redirects, ok := f.fetchTarget(request)
	if !ok {
		return FetchResult{}, &FetchError{Reason: ErrFetchInvalidURL}
	}
	op, cancel := context.WithTimeout(ctx, f.options.Timeout)
	defer cancel()
	status := 0
	for attempt := 1; ; attempt++ {
		h, release, err := f.admit(op, host)
		if err != nil {
			return FetchResult{}, f.contextError(ctx, status, attempt-1)
		}
		outcome := f.attempt(op, target, redirects, sink)
		if outcome.status != 0 {
			status = outcome.status
		}
		var retryAfter time.Duration
		retryOK := true
		if outcome.status == http.StatusTooManyRequests || outcome.status == http.StatusServiceUnavailable {
			// Publish the cooldown before another caller can take the slot.
			retryAfter, retryOK = parseRetryAfter(outcome.retryAfter, f.now())
			if retryOK {
				f.postpone(h, retryAfter)
			} else {
				f.postpone(h, f.options.InvalidRetryCooldown)
			}
		}
		release()
		if outcome.err == nil {
			outcome.result.Source = request.Source
			outcome.result.Attempts = attempt
			outcome.result.FetchedAt = f.now().UTC()
			return outcome.result, nil
		}
		if ctx.Err() != nil {
			return FetchResult{}, ctx.Err()
		}
		if op.Err() != nil {
			return FetchResult{}, &FetchError{Reason: ErrFetchTimeout, Status: status, Attempts: attempt}
		}
		failed := &FetchError{Reason: outcome.err, Status: status, Attempts: attempt}
		if !outcome.retry || !retryOK || attempt >= f.options.MaxAttempts || retryAfter > f.options.MaxRetryAfter {
			return FetchResult{}, failed
		}
		delay := max(f.backoff(attempt), retryAfter)
		if deadline, ok := op.Deadline(); ok && delay >= time.Until(deadline) {
			return FetchResult{}, failed
		}
		if err := waitFetch(op, delay); err != nil {
			return FetchResult{}, f.contextError(ctx, status, attempt)
		}
	}
}

func (f *RemoteFetcher) contextError(caller context.Context, status, attempts int) error {
	if err := caller.Err(); err != nil {
		return err
	}
	return &FetchError{Reason: ErrFetchTimeout, Status: status, Attempts: attempts}
}

func (f *RemoteFetcher) backoff(attempt int) time.Duration {
	base := f.options.BackoffBase << (attempt - 1)
	return base + time.Duration(rand.Int64N(int64(base)/4+1))
}

type fetchOutcome struct {
	result     FetchResult
	err        error
	retry      bool
	status     int
	retryAfter string
}

func (f *RemoteFetcher) attempt(op context.Context, target string, redirects int, sink FetchSink) fetchOutcome {
	ctx, cancel := context.WithTimeout(op, f.options.AttemptTimeout)
	defer cancel()
	response, err := f.transport.Stream(ctx, target, outbound.StreamOptions{MaxBytes: f.options.MaxBytes, MaxRedirects: redirects, Accept: fetchAccept})
	if err != nil {
		reason, retry := transportReason(err)
		return fetchOutcome{err: reason, retry: retry}
	}
	defer response.Body.Close()
	out := fetchOutcome{status: response.Status, retryAfter: response.RetryAfter}
	switch {
	case response.Status == http.StatusOK:
	case response.Status == http.StatusTooManyRequests:
		out.err, out.retry = ErrFetchRateLimited, true
		return out
	case response.Status == http.StatusInternalServerError || response.Status == http.StatusBadGateway ||
		response.Status == http.StatusServiceUnavailable || response.Status == http.StatusGatewayTimeout:
		out.err, out.retry = ErrFetchUnavailable, true
		return out
	case response.Status == http.StatusNotFound || response.Status == http.StatusGone:
		out.err = ErrFetchNotFound
		return out
	case response.Status >= 300 && response.Status <= 399:
		out.err = ErrFetchRedirect
		return out
	default:
		out.err = ErrFetchRejected
		return out
	}
	declared := declaredImageFormat(response.ContentType)
	if declared == "" {
		out.err = ErrFetchContentType
		return out
	}
	body := &fetchBody{r: response.Body}
	head := make([]byte, fetchSniffBytes)
	n, err := io.ReadFull(body, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		if body.err != nil {
			out.err, out.retry = transportReason(body.err)
		} else {
			out.err = ErrFetchFormat
		}
		return out
	}
	head = head[:n]
	if sniffed := sniffImageFormat(head); sniffed == "" || sniffed != declared {
		out.err = ErrFetchFormat
		return out
	}
	stored := &fetchBody{r: io.MultiReader(bytes.NewReader(head), body)}
	digest, size, err := sink.Put(ctx, stored)
	if body.err != nil {
		// The source failed mid-stream; whatever Put reported, nothing valid
		// was stored for this attempt.
		out.err, out.retry = transportReason(body.err)
		return out
	}
	if err != nil {
		if ctx.Err() != nil {
			out.err, out.retry = ErrFetchTimeout, true
		} else {
			out.err = ErrFetchStore
		}
		return out
	}
	if !stored.eof || size != stored.n || digest == "" {
		out.err = ErrFetchStore
		return out
	}
	out.result = FetchResult{Digest: digest, Size: size, Format: declared}
	return out
}

// fetchBody records the first non-EOF read error and the bytes delivered so
// a sink cannot mask a truncated or oversized source.
type fetchBody struct {
	r   io.Reader
	n   int64
	err error
	eof bool
}

func (b *fetchBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if err == io.EOF {
		b.eof = true
	} else if err != nil && b.err == nil {
		b.err = err
	}
	return n, err
}

func transportReason(err error) (error, bool) {
	switch {
	case errors.Is(err, outbound.ErrTooLarge):
		return ErrFetchTooLarge, false
	case errors.Is(err, outbound.ErrRedirect):
		return ErrFetchRedirect, false
	case errors.Is(err, outbound.ErrDenied):
		return ErrFetchDenied, false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ErrFetchTimeout, true
	default:
		return ErrFetchUnavailable, true
	}
}

// admit waits for a per-host slot, the host's start spacing and cooldown, and
// then a global slot. No global slot is held while waiting on one host.
func (f *RemoteFetcher) admit(ctx context.Context, key string) (*fetchHost, func(), error) {
	f.mu.Lock()
	h := f.hosts[key]
	if h == nil {
		if len(f.hosts) >= fetchHostPruneLen {
			f.pruneLocked()
		}
		h = &fetchHost{slots: make(chan struct{}, f.options.MaxPerHost)}
		f.hosts[key] = h
	}
	h.users++
	f.mu.Unlock()
	leave := func() {
		f.mu.Lock()
		h.users--
		f.mu.Unlock()
	}
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		leave()
		return nil, nil, ctx.Err()
	}
	fail := func(err error) (*fetchHost, func(), error) {
		<-h.slots
		leave()
		return nil, nil, err
	}
	for {
		f.mu.Lock()
		wait := h.readyLocked().Sub(f.now())
		f.mu.Unlock()
		if wait > 0 {
			if err := waitFetch(ctx, wait); err != nil {
				return fail(err)
			}
			continue
		}
		select {
		case f.global <- struct{}{}:
		case <-ctx.Done():
			return fail(ctx.Err())
		}
		f.mu.Lock()
		now := f.now()
		if h.readyLocked().After(now) {
			// A response extended the cooldown while we queued globally.
			f.mu.Unlock()
			<-f.global
			continue
		}
		h.next = now.Add(f.options.HostInterval)
		f.mu.Unlock()
		return h, func() { <-f.global; <-h.slots; leave() }, nil
	}
}

func (h *fetchHost) readyLocked() time.Time {
	if h.cooldown.After(h.next) {
		return h.cooldown
	}
	return h.next
}

func (f *RemoteFetcher) postpone(h *fetchHost, delay time.Duration) {
	delay = min(delay, maxFetchCooldown)
	f.mu.Lock()
	defer f.mu.Unlock()
	if until := f.now().Add(delay); until.After(h.cooldown) {
		h.cooldown = until
	}
}

// pruneLocked drops idle hosts whose spacing and cooldown have passed, so the
// table only grows with hosts that are busy or still cooling down.
func (f *RemoteFetcher) pruneLocked() {
	now := f.now()
	for key, h := range f.hosts {
		if h.users == 0 && !h.readyLocked().After(now) {
			delete(f.hosts, key)
		}
	}
}

func waitFetch(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter accepts delta-seconds or an HTTP-date (RFC 9110 10.2.3).
// An empty header means "no server minimum"; malformed values are rejected.
func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	if raw == "" {
		return 0, true
	}
	if len(raw) > 128 {
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	digits := true
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > uint64(maxFetchCooldown/time.Second) {
			// A valid but huge minimum is clamped, never shortened below the cap.
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return 0, false
			}
			return maxFetchCooldown, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	at, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	return max(at.Sub(now), 0), true
}

// fetchTarget canonicalises a request URL and returns the per-host governance
// key and the redirect allowance. TMDB artwork is rebuilt from the fixed
// official image host and a validated size/file path; NFO URLs may name any
// public HTTPS host on the default port (the transport enforces address policy).
func (f *RemoteFetcher) fetchTarget(r FetchRequest) (target, host string, redirects int, ok bool) {
	if len(r.URL) == 0 || len(r.URL) > maxFetchURLBytes {
		return "", "", 0, false
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" {
		return "", "", 0, false
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", "", 0, false
	}
	host = strings.ToLower(u.Hostname())
	switch r.Source {
	case FetchSourceTMDB:
		if host != tmdbImageHost || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
			return "", "", 0, false
		}
		rest, found := strings.CutPrefix(u.Path, "/t/p/")
		size, file, cut := strings.Cut(rest, "/")
		if !found || !cut {
			return "", "", 0, false
		}
		target, err = TMDBImageURL(size, "/"+file)
		if err != nil || target != r.URL {
			return "", "", 0, false
		}
		return target, host, 0, true
	case FetchSourceNFO:
		if strings.HasSuffix(host, ".") || host == "" {
			return "", "", 0, false
		}
		u.Fragment, u.RawFragment = "", ""
		u.Host = strings.ToLower(u.Host)
		return u.String(), host, f.options.MaxRedirects, true
	}
	return "", "", 0, false
}

const tmdbImageHost = "image.tmdb.org"

// TMDB configuration sizes across poster, backdrop, logo, profile and still.
var tmdbImageSizes = map[string]bool{"original": true, "w45": true, "w92": true, "w154": true, "w185": true, "w300": true,
	"w342": true, "w500": true, "w780": true, "w1280": true, "h632": true}

// TMDBImageURL builds an artwork URL on the official image host. filePath
// follows the metadata adapter's TMDB file_path rule.
func TMDBImageURL(size, filePath string) (string, error) {
	if !tmdbImageSizes[size] || !validTMDBImagePath(filePath) {
		return "", ErrFetchInvalidURL
	}
	return "https://" + tmdbImageHost + "/t/p/" + size + filePath, nil
}

// validTMDBImagePath mirrors metadata.validImagePath: one segment of
// [A-Za-z0-9_-] with a .jpg/.png/.webp extension.
func validTMDBImagePath(path string) bool {
	if len(path) < 6 || len(path) > 256 || path[0] != '/' {
		return false
	}
	name := path[1:]
	index := strings.LastIndexByte(name, '.')
	if index < 1 || index > 192 {
		return false
	}
	ext := name[index:]
	if ext != ".jpg" && ext != ".png" && ext != ".webp" {
		return false
	}
	for _, ch := range name[:index] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// declaredImageFormat maps a Content-Type to a supported format (G40.2 list).
func declaredImageFormat(contentType string) string {
	if contentType == "" || len(contentType) > 256 {
		return ""
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	switch media {
	case "image/jpeg", "image/jpg", "image/pjpeg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/avif":
		return "avif"
	case "image/bmp", "image/x-bmp", "image/x-ms-bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	}
	return ""
}

// sniffImageFormat identifies a supported format by its magic bytes.
func sniffImageFormat(head []byte) string {
	switch {
	case len(head) >= 3 && head[0] == 0xff && head[1] == 0xd8 && head[2] == 0xff:
		return "jpeg"
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a")):
		return "gif"
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(head, []byte("II*\x00")) || bytes.HasPrefix(head, []byte("MM\x00*")):
		return "tiff"
	case len(head) >= 14 && head[0] == 'B' && head[1] == 'M':
		return "bmp"
	case isAVIF(head):
		return "avif"
	}
	return ""
}

// isAVIF checks the leading ISO-BMFF ftyp box for an avif/avis brand.
func isAVIF(head []byte) bool {
	if len(head) < 16 || !bytes.Equal(head[4:8], []byte("ftyp")) {
		return false
	}
	size := int(head[0])<<24 | int(head[1])<<16 | int(head[2])<<8 | int(head[3])
	if size < 16 || size%4 != 0 {
		return false
	}
	end := min(size, len(head))
	for i := 8; i+4 <= end; i += 4 {
		if i == 12 {
			continue // minor_version
		}
		if brand := string(head[i : i+4]); brand == "avif" || brand == "avis" {
			return true
		}
	}
	return false
}
