package compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Accounts is the server's own account service. The user module only adapts
// its results to the legacy wire format: password verification, the native
// permission, failure counting, lockout, session limits and audit all stay in
// that one implementation.
type Accounts interface {
	LoginNative(ctx context.Context, name, password string, client domain.NativeClient, ip string) (domain.SessionGrant, error)
	Get(ctx context.Context, actor domain.Actor, id string) (domain.User, error)
	Revoke(ctx context.Context, actor domain.Actor, userID, sessionID string) error
}

// UserOptions connects the user module to the server's account machinery.
// Every field is required.
type UserOptions struct {
	Accounts Accounts
	// Admit claims one slot of the server's shared account admission budget
	// without waiting. ok is false when the budget is exhausted; otherwise
	// release must be called exactly once.
	Admit func() (release func(), ok bool)
	// AllowLogin is the server's shared login rate limiter (client address and
	// account name buckets); retry is the wait before the next attempt.
	AllowLogin func(ip, name string) (allowed bool, retry time.Duration)
	// ClientIP returns the client address as the server derived it from the
	// connection and its trusted proxy settings.
	ClientIP func(*http.Request) string
	// AdmitClient, when set, applies the server's client control rules to a
	// login before the body is read (G47). Its errors are the domain client
	// control errors, answered like the authenticated routes answer them.
	AdmitClient func(*http.Request) error
}

func (o *UserOptions) valid() bool {
	return o.Accounts != nil && o.Admit != nil && o.AllowLogin != nil && o.ClientIP != nil
}

// loginBodyLimit bounds the login body; the transformation guard has already
// buffered it under its own, larger bound.
const loginBodyLimit = 64 << 10

// Values reported by the user module. UserPolicy requires both provider
// identifiers to be non-empty; clients only echo them back. These are Jelee
// names, not upstream ones: Jelee has exactly one local password provider and
// no password reset provider that clients could drive.
const (
	authenticationProviderID = "Jelee.LocalPassword"
	passwordResetProviderID  = "Jelee.NoPasswordReset"
	// syncPlayNone is the upstream enum value for "no SyncPlay access": the
	// feature does not exist here.
	syncPlayNone = "None"
	// subtitleModeDefault is the upstream default subtitle playback mode.
	subtitleModeDefault = "Default"
	// wireTime is the upstream date format: UTC with seven fractional digits.
	wireTime = "2006-01-02T15:04:05.0000000Z"
)

// authenticationResult mirrors the upstream login response.
type authenticationResult struct {
	User        userDto     `json:"User"`
	SessionInfo sessionInfo `json:"SessionInfo"`
	AccessToken string      `json:"AccessToken"`
	ServerID    string      `json:"ServerId"`
}

// userDto carries only what Jelee knows about an account. Members that are
// null-able upstream and have no Jelee source (image tags, last login and
// activity dates, server name, aspect ratio) are omitted, which is the same
// as null on the wire.
type userDto struct {
	Name                      string            `json:"Name"`
	ServerID                  string            `json:"ServerId"`
	ID                        string            `json:"Id"`
	HasPassword               bool              `json:"HasPassword"`
	HasConfiguredPassword     bool              `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool              `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool              `json:"EnableAutoLogin"`
	Configuration             userConfiguration `json:"Configuration"`
	Policy                    userPolicy        `json:"Policy"`
}

// userConfiguration is the non-null part of the upstream user configuration
// at its upstream defaults. Jelee stores no per-user client preferences and
// the layer offers no route to change them, so the values are fixed; the
// null-able language preferences and cast receiver are omitted.
type userConfiguration struct {
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
}

// userPolicy reports the account flags Jelee has (administrator, hidden,
// disabled) and declares every feature switch truthfully: no transcoding,
// remux, downloads, deletion, live TV, remote control, SyncPlay or public
// sharing. Folder and device lists, parental settings, schedules, counters
// and limits are omitted: they are null-able upstream, and the counters and
// limits live in server configuration this layer must not publish.
type userPolicy struct {
	IsAdministrator                 bool   `json:"IsAdministrator"`
	IsHidden                        bool   `json:"IsHidden"`
	EnableCollectionManagement      bool   `json:"EnableCollectionManagement"`
	EnableSubtitleManagement        bool   `json:"EnableSubtitleManagement"`
	EnableLyricManagement           bool   `json:"EnableLyricManagement"`
	IsDisabled                      bool   `json:"IsDisabled"`
	EnableUserPreferenceAccess      bool   `json:"EnableUserPreferenceAccess"`
	EnableRemoteControlOfOtherUsers bool   `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl       bool   `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess              bool   `json:"EnableRemoteAccess"`
	EnableLiveTvManagement          bool   `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess              bool   `json:"EnableLiveTvAccess"`
	EnableMediaPlayback             bool   `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding  bool   `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding  bool   `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing          bool   `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding    bool   `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion           bool   `json:"EnableContentDeletion"`
	EnableContentDownloading        bool   `json:"EnableContentDownloading"`
	EnableSyncTranscoding           bool   `json:"EnableSyncTranscoding"`
	EnableMediaConversion           bool   `json:"EnableMediaConversion"`
	EnableAllDevices                bool   `json:"EnableAllDevices"`
	EnableAllChannels               bool   `json:"EnableAllChannels"`
	EnableAllFolders                bool   `json:"EnableAllFolders"`
	EnablePublicSharing             bool   `json:"EnablePublicSharing"`
	AuthenticationProviderID        string `json:"AuthenticationProviderId"`
	PasswordResetProviderID         string `json:"PasswordResetProviderId"`
	SyncPlayAccess                  string `json:"SyncPlayAccess"`
}

// sessionInfo is the reduced upstream session DTO returned at login: the
// identity of the new session and its non-null members. The remote end point
// (a client address) and every playback, queue and capability member are
// omitted.
type sessionInfo struct {
	PlayableMediaTypes    []string `json:"PlayableMediaTypes"`
	ID                    string   `json:"Id"`
	UserID                string   `json:"UserId"`
	UserName              string   `json:"UserName"`
	Client                string   `json:"Client"`
	LastActivityDate      string   `json:"LastActivityDate"`
	LastPlaybackCheckIn   string   `json:"LastPlaybackCheckIn"`
	DeviceName            string   `json:"DeviceName,omitempty"`
	DeviceID              string   `json:"DeviceId"`
	ApplicationVersion    string   `json:"ApplicationVersion,omitempty"`
	IsActive              bool     `json:"IsActive"`
	SupportsMediaControl  bool     `json:"SupportsMediaControl"`
	SupportsRemoteControl bool     `json:"SupportsRemoteControl"`
	HasCustomDeviceName   bool     `json:"HasCustomDeviceName"`
	ServerID              string   `json:"ServerId"`
	SupportedCommands     []string `json:"SupportedCommands"`
}

func (rt *router) userRoutes() {
	rt.handle(http.MethodPost, "/Users/AuthenticateByName", false, rt.account(rt.authenticateByName))
	rt.handle(http.MethodGet, "/Users/Public", false, publicUsers)
	rt.handle(http.MethodGet, "/Users/Me", true, rt.account(rt.currentUser))
	rt.handle(http.MethodGet, "/Users/{id}", true, rt.account(rt.userByID))
	rt.handle(http.MethodPost, "/Sessions/Logout", true, rt.account(rt.logout))
}

// account applies the server's account admission budget and request bounds,
// like every account route of the server's own API: no queueing, a busy
// budget answers 503 with Retry-After, and reads, writes and the work itself
// are bounded by the request timeout.
func (rt *router) account(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		release, ok := rt.opts.Users.Admit()
		if !ok {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		defer release()
		rt.bounded(h)(w, r)
	}
}

// authenticateByName issues a native session through the server's native
// login (decision: compatibility logins are native sessions and require the
// account's native permission). The client identity comes from the
// parameterised authorization header, as upstream reads it. Outcomes:
//   - unknown account, wrong password, disabled, deleted or locked: 401;
//   - correct password but native devices not allowed: 403 (upstream answers
//     a refused account with 403 too); the decision is made only after the
//     password matched, so it reveals nothing to a caller without it;
//   - the account password of an account with a second factor: 403 with
//     X-Jelee-Error: app_password_required and a plain text explanation;
//     an application password signs in instead (G07.8);
//   - missing or invalid client identity or body: 400;
//   - shared rate limit exhausted or session limit reached: 429.
func (rt *router) authenticateByName(w http.ResponseWriter, r *http.Request) {
	// Browser engines attach these and page scripts cannot remove them; the
	// layer boundary already refused Origin. Same rule as the native route.
	if len(r.Header.Values("Sec-Fetch-Site")) > 0 || len(r.Header.Values("Sec-Fetch-Mode")) > 0 {
		writeError(w, http.StatusForbidden)
		return
	}
	client, err := ParseClientAuth(r.Header, r.URL.Query())
	if errors.Is(err, ErrMalformedAuth) {
		writeError(w, http.StatusBadRequest)
		return
	}
	if admit := rt.opts.Users.AdmitClient; admit != nil {
		if err := admit(r); err != nil {
			writeClientControlError(w, err)
			return
		}
	}
	// A stale or foreign token next to the login is ignored, as upstream
	// ignores it; the client fields parsed before it are kept.
	var input struct {
		Username *string `json:"Username"`
		Pw       *string `json:"Pw"`
	}
	if !decodeJSON(r, &input) || input.Username == nil || input.Pw == nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	ip := rt.opts.Users.ClientIP(r)
	if allowed, retry := rt.opts.Users.AllowLogin(ip, *input.Username); !allowed {
		domain.ForceTraceSampling(r.Context())
		seconds := int64((retry + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests)
		return
	}
	native := domain.NativeClient{Name: client.Client, Device: client.Device, DeviceID: client.DeviceID, Version: client.Version}
	grant, err := rt.opts.Users.Accounts.LoginNative(r.Context(), *input.Username, *input.Pw, native, ip)
	if err != nil {
		// A failed login is a security event (G46.6).
		domain.ForceTraceSampling(r.Context())
		writeAccountError(w, err)
		return
	}
	user, err := rt.userDto(grant.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	session, err := rt.sessionInfo(grant)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, authenticationResult{User: user, SessionInfo: session, AccessToken: grant.Token, ServerID: rt.opts.ServerID})
}

// appPasswordRequiredText is shown by clients that display the body of a
// failed login. Upstream clients cannot ask for a second factor, so an
// account with one signs in here with an application password (G07.8).
const appPasswordRequiredText = "Two-factor authentication is enabled for this account. Sign in with an application password created in the web interface (Settings, Application passwords) instead of the account password."

// writeAppPasswordRequired refuses the account password of an account with
// a second factor: 403 like a refused account, with the stable code in
// X-Jelee-Error and an explanation in the body.
func writeAppPasswordRequired(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(appPasswordRequiredText)))
	h.Set("X-Jelee-Error", "app_password_required")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, appPasswordRequiredText)
}

// publicUsers always answers an empty list. Upstream lists visible accounts
// for a login screen; here that would hand every unauthenticated caller the
// account names that login deliberately refuses to confirm (unknown and
// wrong-password logins look the same). Clients fall back to asking for the
// user name. It needs no session and never touches the account store.
func publicUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, []struct{}{})
}

func (rt *router) currentUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := rt.actor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized)
		return
	}
	rt.writeUser(w, r, actor, actor.UserID)
}

// userByID answers the caller's own account, or any account to an
// administrator. The store decides from the live session: another account
// is refused with 403 before it is looked up, so the answer never depends on
// whether it exists. Soft-deleted accounts are answered with 404.
func (rt *router) userByID(w http.ResponseWriter, r *http.Request) {
	actor, ok := rt.actor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized)
		return
	}
	id, err := ParseID(chi.URLParam(r, "id"))
	if err != nil {
		// Upstream fails model binding of a malformed identifier with 400.
		writeError(w, http.StatusBadRequest)
		return
	}
	rt.writeUser(w, r, actor, id)
}

func (rt *router) writeUser(w http.ResponseWriter, r *http.Request, actor domain.Actor, id string) {
	user, err := rt.opts.Users.Accounts.Get(r.Context(), actor, id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if user.DeletedAt != nil {
		writeError(w, http.StatusNotFound)
		return
	}
	dto, err := rt.userDto(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, dto)
}

// logout revokes the session that authenticated the request; other sessions
// of the account stay valid. Upstream answers 204 and ignores any body.
func (rt *router) logout(w http.ResponseWriter, r *http.Request) {
	actor, ok := rt.actor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized)
		return
	}
	if err := rt.opts.Users.Accounts.Revoke(r.Context(), actor, actor.UserID, actor.SessionID); err != nil {
		writeAccountError(w, err)
		return
	}
	w.Header().Del("Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

func (rt *router) actor(r *http.Request) (domain.Actor, bool) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		return domain.Actor{}, false
	}
	return domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: rt.opts.Users.ClientIP(r)}, true
}

func (rt *router) userDto(u domain.User) (userDto, error) {
	id, err := FormatID(u.ID)
	if err != nil {
		return userDto{}, err
	}
	return userDto{
		Name:     u.Name,
		ServerID: rt.opts.ServerID,
		ID:       id,
		// Every account that can log in has a password: accounts without one
		// are refused at login, and Jelee has no easy (PIN) password or
		// automatic login.
		HasPassword:           true,
		HasConfiguredPassword: true,
		Configuration: userConfiguration{
			PlayDefaultAudioTrack:      true,
			GroupedFolders:             []string{},
			SubtitleMode:               subtitleModeDefault,
			OrderedViews:               []string{},
			LatestItemsExcludes:        []string{},
			MyMediaExcludes:            []string{},
			HidePlayedInLatest:         true,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
			EnableNextEpisodeAutoPlay:  true,
		},
		Policy: userPolicy{
			IsAdministrator: u.Admin,
			IsHidden:        u.Hidden,
			IsDisabled:      u.Disabled,
			// Only accounts allowed native devices can hold a session that
			// plays media (direct delivery only).
			EnableMediaPlayback: u.AllowNative,
			EnableRemoteAccess:  true,
			EnableAllDevices:    true,
			// Library grants apply to everyone but administrators.
			EnableAllFolders:         u.Admin,
			AuthenticationProviderID: authenticationProviderID,
			PasswordResetProviderID:  passwordResetProviderID,
			SyncPlayAccess:           syncPlayNone,
		},
	}, nil
}

func (rt *router) sessionInfo(grant domain.SessionGrant) (sessionInfo, error) {
	id, err := FormatID(grant.Session.ID)
	if err != nil {
		return sessionInfo{}, err
	}
	userID, err := FormatID(grant.User.ID)
	if err != nil {
		return sessionInfo{}, err
	}
	return sessionInfo{
		PlayableMediaTypes:  []string{},
		ID:                  id,
		UserID:              userID,
		UserName:            grant.User.Name,
		Client:              grant.Session.Client,
		LastActivityDate:    grant.Session.CreatedAt.UTC().Format(wireTime),
		LastPlaybackCheckIn: time.Time{}.Format(wireTime),
		DeviceName:          grant.Session.DeviceName,
		DeviceID:            grant.Session.DeviceID,
		ApplicationVersion:  grant.Session.Version,
		IsActive:            true,
		ServerID:            rt.opts.ServerID,
		SupportedCommands:   []string{},
	}, nil
}

// decodeJSON reads one JSON object. Member names match case-insensitively
// and unknown members are ignored, as the upstream binder does; a missing or
// non-JSON body, trailing data or an oversized body is refused.
func decodeJSON(r *http.Request, v any) bool {
	if r.Body == nil {
		return false
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, loginBodyLimit+1))
	if err := decoder.Decode(v); err != nil {
		return false
	}
	var extra json.RawMessage
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

// writeAccountError maps account service errors to the layer's error forms.
// Nothing about the account or the cause reaches the body.
func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrSecondFactorRequired):
		writeAppPasswordRequired(w)
	case errors.Is(err, domain.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized)
	case errors.Is(err, domain.ErrNativeLoginDisabled), errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, domain.ErrSessionLimit):
		writeError(w, http.StatusTooManyRequests)
	case errors.Is(err, domain.ErrDatabase), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable)
	default:
		writeError(w, http.StatusInternalServerError)
	}
}
