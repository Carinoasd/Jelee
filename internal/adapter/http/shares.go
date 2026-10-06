package httpapi

import (
	"context"
	"hash/maphash"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Share links and guest sessions (G48.6), library network rules (G48.5).
// The administrator API manages them; a share token is exchanged for a
// guest session through the redeem routes. A guest session is an ordinary
// session of the share's hidden guest account: the unified storage filter
// limits it to the share's scope, and the guest gate below limits it to the
// routes a guest needs. docs/access-control.md describes the model.

// guestRoutes are the routes a guest session may use, by method and route
// pattern. true marks a write that a read-only share refuses. Every other
// route answers 403 share_forbidden, before its handler runs.
var guestRoutes = map[string]bool{
	"GET /api/v1/users/me":                   false,
	"GET /api/v1/users/me/preferences":       false,
	"GET /api/v1/auth/csrf":                  false,
	"POST /api/v1/auth/logout":               false,
	"GET /api/v1/shares/current":             false,
	"GET /api/v1/site/appearance":            false,
	"GET /api/v1/site/plugins":               false,
	"GET /api/v1/items":                      false,
	"GET /api/v1/items/{id}":                 false,
	"GET /api/v1/items/{id}/details":         false,
	"GET /api/v1/items/{id}/sources":         false,
	"GET /api/v1/items/{id}/user-data":       false,
	"GET /api/v1/items/{id}/playback":        false,
	"POST /api/v1/items/{id}/playback/check": false,
	"GET /api/v1/users/me/resume":            false,
	"GET /api/v1/sources/{id}/stream":        false,
	"HEAD /api/v1/sources/{id}/stream":       false,
	"GET " + subtitleTrackRoute:              false,
	"HEAD " + subtitleTrackRoute:             false,
	"GET " + audioTrackRoute:                 false,
	"HEAD " + audioTrackRoute:                false,
	"GET " + embeddedSubtitleRoute:           false,
	"HEAD " + embeddedSubtitleRoute:          false,
	"GET " + attachmentRoute:                 false,
	"HEAD " + attachmentRoute:                false,
	"GET " + ocrSubtitleRoute:                false,
	"HEAD " + ocrSubtitleRoute:               false,
	"GET /images/{type}/{id}":                false,
	"HEAD /images/{type}/{id}":               false,
	"POST /api/v1/playback/start":            true,
	"POST /api/v1/playback/progress":         true,
	"POST /api/v1/playback/stop":             true,
	"PUT /api/v1/items/{id}/played":          true,
	"DELETE /api/v1/items/{id}/played":       true,
}

// guestRoute is the method and route pattern of r as the guest gate and
// the access records name it.
func guestRoute(r *http.Request) string {
	pattern := ""
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		pattern = rctx.RoutePattern()
	}
	if pattern == "" {
		pattern = "?"
	}
	return r.Method + " " + pattern
}

// guestCheck refuses a guest a route outside guestRoutes and, for a
// read-only share, a write.
func guestCheck(route string, p access.Principal) error {
	write, ok := guestRoutes[route]
	switch {
	case !ok:
		return domain.ErrShareForbidden
	case write && p.ShareReadOnly:
		return domain.ErrShareReadOnly
	}
	return nil
}

// shareAccessRecorder is a backend that audits guest session use.
type shareAccessRecorder interface {
	RecordShareAccess(context.Context, domain.ShareAccess) error
}

// shareAccessLog throttles guest access records to one per session, route,
// outcome and minute; repeated requests within a minute (a player's Range
// requests) share the record. The map is bounded and starts over when full,
// which only costs a few repeated records.
type shareAccessLog struct {
	mu     sync.Mutex
	seed   maphash.Seed
	recent map[uint64]time.Time
}

const shareAccessLogMax = 65536

func (l *shareAccessLog) due(a domain.ShareAccess, now time.Time) bool {
	var h maphash.Hash
	h.SetSeed(l.seed)
	h.WriteString(a.Actor.SessionID)
	h.WriteByte(0)
	h.WriteString(a.Route)
	if a.Refused {
		h.WriteByte(1)
	}
	k := h.Sum64()
	minute := now.Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil || len(l.recent) >= shareAccessLogMax {
		l.recent = map[uint64]time.Time{}
	}
	if last, ok := l.recent[k]; ok && last.Equal(minute) {
		return false
	}
	l.recent[k] = minute
	return true
}

// guestGate applies guestCheck to a guest request and audits it: every
// guest request is covered by an access record of its session, route and
// minute. A record that cannot be written refuses the request, so no guest
// access goes unaudited.
func (s *Server) guestGate(w http.ResponseWriter, r *http.Request, p access.Principal) bool {
	route := guestRoute(r)
	refusal := guestCheck(route, p)
	a := domain.ShareAccess{ShareID: p.ShareID, Actor: domain.Actor{UserID: p.UserID, SessionID: p.SessionID, IP: requestClientIP(r)}, ClientKind: string(p.Kind),
		Route: route, Refused: refusal != nil}
	if recorder, ok := s.backend.(shareAccessRecorder); ok && s.shareAccess.due(a, time.Now()) {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		err := recorder.RecordShareAccess(ctx, a)
		cancel()
		if err != nil {
			s.logger.Warn("share access not recorded", "component", "share", "code", "share_access_record_failed")
			WriteError(w, r, domain.ErrDatabase)
			return false
		}
	}
	if refusal != nil {
		WriteError(w, r, refusal)
		return false
	}
	return true
}

// requestScope is the per-request input of the unified storage filter:
// the client address as resolved through the trusted proxies, the session
// kind, the libraries a client control decision left (nil: all) and the
// request time restricted time windows are decided at (G48.4).
func (s *Server) requestScope(address string, kind access.ClientKind, libraries []string) *access.RequestScope {
	now := time.Now
	if s.accessNow != nil {
		now = s.accessNow
	}
	scope := &access.RequestScope{Kind: kind, Libraries: libraries, At: now()}
	if addr, err := netip.ParseAddr(address); err == nil {
		scope.IP = addr.Unmap().WithZone("")
	}
	return scope
}

// shareRoutes registers the share administration, redemption and network
// rule API.
func (s *Server) shareRoutes(r chi.Router) {
	r.With(s.accountBudget).Post("/api/v1/shares/redeem", s.redeemShare)
	r.With(s.accountBudget).Post("/api/v1/shares/redeem/native", s.redeemShareNative)
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		r.Get("/api/v1/shares/current", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			v, err := s.accounts.CurrentShare(r.Context(), a)
			return v, 200, err
		}))
		r.Get("/api/v1/shares", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			return memoryList(r, "/api/v1/shares", func() (any, error) { return s.accounts.Shares(r.Context(), a) })
		}))
		r.Post("/api/v1/shares", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				LibraryID     string     `json:"libraryId"`
				ItemID        string     `json:"itemId"`
				ExpiresAt     *time.Time `json:"expiresAt"`
				ReadOnly      *bool      `json:"readOnly"`
				AllowPlayback *bool      `json:"allowPlayback"`
				MaxStreams    *int       `json:"maxStreams"`
				Note          string     `json:"note"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			if input.ExpiresAt == nil || input.ReadOnly == nil || input.AllowPlayback == nil {
				return nil, 0, domain.ErrInvalid
			}
			streams := 1
			if input.MaxStreams != nil {
				streams = *input.MaxStreams
			}
			v, err := s.accounts.CreateShare(r.Context(), a, domain.ShareInput{LibraryID: input.LibraryID, ItemID: input.ItemID, ExpiresAt: *input.ExpiresAt,
				ReadOnly: *input.ReadOnly, AllowPlayback: *input.AllowPlayback, MaxStreams: streams, Note: input.Note})
			return v, 201, err
		}))
		r.Get("/api/v1/shares/{id}", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			v, err := s.accounts.Share(r.Context(), a, chi.URLParam(r, "id"))
			return v, 200, err
		}))
		r.Post("/api/v1/shares/{id}/revoke", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			v, err := s.accounts.RevokeShare(r.Context(), a, chi.URLParam(r, "id"))
			return v, 200, err
		}))
		r.Get("/api/v1/shares/{id}/access", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			q, err := listQuery(r, "/api/v1/shares/{id}/access")
			if err != nil {
				return nil, 0, err
			}
			records, next, err := keysetPage(q, func(cursor string, limit int) ([]domain.ShareAccessRecord, string, error) {
				return s.accounts.ShareAccess(r.Context(), a, chi.URLParam(r, "id"), cursor, limit)
			})
			if err != nil {
				return nil, 0, err
			}
			return listData(q, map[string]any{"records": records, "pagination": map[string]any{"nextCursor": next, "limit": q.Limit}})
		}))
		r.Get("/api/v1/access/network-rules", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			return memoryList(r, "/api/v1/access/network-rules", func() (any, error) { return s.accounts.NetworkRules(r.Context(), a) })
		}))
		r.Post("/api/v1/access/network-rules", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			in, err := decodeNetworkRule(w, r)
			if err != nil {
				return nil, 0, err
			}
			v, err := s.accounts.CreateNetworkRule(r.Context(), a, in)
			return v, 201, err
		}))
		r.Put("/api/v1/access/network-rules/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			in, err := decodeNetworkRule(w, r)
			if err != nil {
				return nil, 0, err
			}
			v, err := s.accounts.UpdateNetworkRule(r.Context(), a, chi.URLParam(r, "id"), in)
			return v, 200, err
		}))
		r.Delete("/api/v1/access/network-rules/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.DeleteNetworkRule(r.Context(), a, chi.URLParam(r, "id"))
		}))
	})
}

func decodeNetworkRule(w http.ResponseWriter, r *http.Request) (domain.NetworkRuleInput, error) {
	var input struct {
		LibraryID     string   `json:"libraryId"`
		Network       string   `json:"network"`
		CIDRs         []string `json:"cidrs"`
		ClientKinds   []string `json:"clientKinds"`
		IncludeAdmins *bool    `json:"includeAdmins"`
		Enabled       *bool    `json:"enabled"`
		Note          string   `json:"note"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return domain.NetworkRuleInput{}, err
	}
	if input.Enabled == nil || input.Network == "" {
		return domain.NetworkRuleInput{}, domain.ErrInvalid
	}
	in := domain.NetworkRuleInput{LibraryID: input.LibraryID, Network: input.Network, CIDRs: input.CIDRs, ClientKinds: input.ClientKinds, Enabled: *input.Enabled, Note: input.Note}
	if input.IncludeAdmins != nil {
		in.IncludeAdmins = *input.IncludeAdmins
	}
	if in.CIDRs == nil {
		in.CIDRs = []string{}
	}
	if in.ClientKinds == nil {
		in.ClientKinds = []string{}
	}
	return in, nil
}

// allowRedeem applies the shared login budget to a redemption, keyed by
// address: share tokens carry 256 random bits, the budget only bounds the
// work an address can cause.
func (s *Server) allowRedeem(w http.ResponseWriter, r *http.Request, ip string) bool {
	return s.allowLogin(w, r, ip, "\x00share")
}

// redeemShare exchanges a share token for a web guest session: the
// session cookie and CSRF token like a web login. Web sessions never play.
func (s *Server) redeemShare(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout()))
	var input struct {
		Token      string `json:"token"`
		DeviceName string `json:"deviceName"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		WriteError(w, r, err)
		return
	}
	if s.clients != nil {
		if err := s.clients.admitLogin(r, clientLabels{deviceName: input.DeviceName}); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	ip := requestClientIP(r)
	if !s.allowRedeem(w, r, ip) {
		return
	}
	grant, err := s.accounts.RedeemShare(ctx, input.Token, input.DeviceName, ip)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": issueWebGrant(w, grant)})
}

// redeemShareNative exchanges a share token for a native guest session,
// which may direct play under the delivery rules, for shares that allow
// playback. Like the native login it refuses anything a browser sends.
func (s *Server) redeemShareNative(w http.ResponseWriter, r *http.Request) {
	if browserRequest(r) {
		writeProblem(w, r, 403, "forbidden", "Operation is not permitted.")
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout()))
	var input struct {
		Token    string `json:"token"`
		Client   string `json:"client"`
		Device   string `json:"device"`
		DeviceID string `json:"deviceId"`
		Version  string `json:"version"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		WriteError(w, r, err)
		return
	}
	if s.clients != nil {
		if err := s.clients.admitLogin(r, clientLabels{app: input.Client, version: input.Version, deviceID: input.DeviceID, deviceName: input.Device}); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	ip := requestClientIP(r)
	if !s.allowRedeem(w, r, ip) {
		return
	}
	grant, err := s.accounts.RedeemShareNative(ctx, input.Token, domain.NativeClient{Name: input.Client, Device: input.Device, DeviceID: input.DeviceID, Version: input.Version}, ip)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": grant})
}
