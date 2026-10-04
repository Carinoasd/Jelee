package domain

import (
	"context"
	"errors"
	"strings"
	"time"
)

// G18 initial setup. The wizard order is fixed by G18.1:
// language -> administrator -> PostgreSQL -> media directories -> TMDB ->
// toolchain -> NFO/image policy -> network publication/privacy -> complete.
type SetupStep int

const (
	SetupStepLanguage SetupStep = iota + 1
	SetupStepAdmin
	SetupStepDatabase
	SetupStepMedia
	SetupStepTMDB
	SetupStepToolchain
	SetupStepMetadataPolicy
	SetupStepNetwork
	SetupStepComplete
)

var (
	ErrSetupCompleted = errors.New("setup already completed")
	ErrSetupStepOrder = errors.New("setup step is not the current step")
	// ErrSetupAdminExists: an active administrator appeared outside the
	// wizard (for example `jelee-cli account bootstrap`) after it started.
	ErrSetupAdminExists = errors.New("setup administrator already exists")
)

var setupStepNames = [...]string{"", "language", "admin", "database", "media", "tmdb", "toolchain", "metadata-policy", "network", "complete"}

func (s SetupStep) Valid() bool { return s >= SetupStepLanguage && s <= SetupStepComplete }

func (s SetupStep) String() string {
	if !s.Valid() {
		return "invalid"
	}
	return setupStepNames[s]
}

func ParseSetupStep(value string) (SetupStep, bool) {
	for i := SetupStepLanguage; i <= SetupStepComplete; i++ {
		if setupStepNames[i] == value {
			return i, true
		}
	}
	return 0, false
}

func (s SetupStep) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalid
	}
	return []byte(s.String()), nil
}

func (s *SetupStep) UnmarshalText(text []byte) error {
	step, ok := ParseSetupStep(string(text))
	if !ok {
		return ErrInvalid
	}
	*s = step
	return nil
}

// SetupState is the persisted wizard progress. It deliberately has no field
// able to hold a password, password hash, session token or TMDB credential:
// the administrator password is hashed and written by the admin step and
// then discarded, and the TMDB key only ever comes from server configuration.
type SetupState struct {
	// Version is an optimistic concurrency counter owned by the repository.
	// Zero means the state has never been stored.
	Version     int64      `json:"version"`
	Current     SetupStep  `json:"current"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`

	Locale         string              `json:"locale,omitempty"`
	Admin          SetupAdmin          `json:"admin"`
	Database       SetupDatabase       `json:"database"`
	Media          []SetupLibrary      `json:"media,omitempty"`
	TMDB           SetupTMDB           `json:"tmdb"`
	Toolchain      SetupToolchain      `json:"toolchain"`
	MetadataPolicy SetupMetadataPolicy `json:"metadataPolicy"`
	Network        SetupNetwork        `json:"network"`
}

// SetupAdmin records the administrator the wizard created. A non-empty UserID
// makes the admin step idempotent: re-entering never creates a second account.
type SetupAdmin struct {
	UserID      string `json:"userId,omitempty"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

type SetupDatabase struct {
	SchemaVersion int `json:"schemaVersion,omitempty"`
}

type SetupLibrary struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type SetupTMDB struct {
	Enabled  bool   `json:"enabled"`
	Language string `json:"language,omitempty"`
}

type SetupToolchain struct {
	Available      []string `json:"available,omitempty"`
	Missing        []string `json:"missing,omitempty"`
	AcceptDegraded bool     `json:"acceptDegraded"`
}

const (
	SetupNFOWriteOff  = "off"
	SetupNFOWriteBack = "write-back"
)

// SetupMetadataPolicy covers G18.1 "NFO and image policy (read/write-back/fetch)".
type SetupMetadataPolicy struct {
	NFORead        string `json:"nfoRead,omitempty"` // NFOModeOff or NFOModeReadOnly
	NFOWrite       string `json:"nfoWrite,omitempty"`
	ImageFetch     bool   `json:"imageFetch"`
	ImageWriteBack bool   `json:"imageWriteBack"`
}

const (
	SetupNetworkLocal        = "local"
	SetupNetworkLAN          = "lan"
	SetupNetworkReverseProxy = "reverse-proxy"
)

type SetupNetwork struct {
	Mode                string   `json:"mode,omitempty"`
	Listen              string   `json:"listen,omitempty"`
	AllowedHosts        []string `json:"allowedHosts,omitempty"`
	TrustedProxies      []string `json:"trustedProxies,omitempty"`
	PrivacyAcknowledged bool     `json:"privacyAcknowledged"`
}

func NewSetupState() SetupState { return SetupState{Current: SetupStepLanguage} }

func (s SetupState) Completed() bool { return s.CompletedAt != nil }

// SetupStatus is the effective view used by HTTP, CLI and the request gate.
type SetupStatus struct {
	Completed bool `json:"completed"`
	// Adopted marks an installation that predates the wizard: no setup state
	// was ever stored but an active administrator exists. It counts as
	// completed so upgrading an existing deployment never locks it out.
	Adopted bool       `json:"adopted"`
	State   SetupState `json:"state"`
}

// EffectiveSetupStatus folds repository facts into a status. stored reports
// whether a setup state row exists. An admin created by the wizard itself
// does not complete an in-progress wizard; only a never-started wizard with
// an active admin is adopted.
func EffectiveSetupStatus(state SetupState, stored, activeAdmin bool) SetupStatus {
	if !stored {
		state = NewSetupState()
		return SetupStatus{Completed: activeAdmin, Adopted: activeAdmin, State: state}
	}
	return SetupStatus{Completed: state.Completed(), State: state}
}

// SetupAdvance records that step was accepted and moves to the next step.
// Callers fill the step's data before calling it; the step must be current.
func SetupAdvance(state SetupState, step SetupStep) (SetupState, error) {
	if err := setupEditable(state); err != nil {
		return state, err
	}
	if !step.Valid() || step == SetupStepComplete {
		return state, ErrInvalid
	}
	if step != state.Current {
		return state, ErrSetupStepOrder
	}
	state.Current = step + 1
	return state, nil
}

// SetupBack returns to the previous step without discarding recorded data.
func SetupBack(state SetupState) (SetupState, error) {
	if err := setupEditable(state); err != nil {
		return state, err
	}
	if state.Current == SetupStepLanguage {
		return state, ErrSetupStepOrder
	}
	state.Current--
	return state, nil
}

// SetupFinish marks the wizard complete. Every earlier step must have been
// accepted, which the sequential Advance rule guarantees once Current is
// SetupStepComplete; the data is rechecked so a corrupted row cannot finish.
func SetupFinish(state SetupState, now time.Time) (SetupState, error) {
	if err := setupEditable(state); err != nil {
		return state, err
	}
	if state.Current != SetupStepComplete {
		return state, ErrSetupStepOrder
	}
	if state.Locale == "" || !ValidID(state.Admin.UserID) || state.Database.SchemaVersion < 1 || state.MetadataPolicy.NFORead == "" || state.Network.Mode == "" || now.IsZero() {
		return state, ErrInvalid
	}
	at := now.UTC()
	state.CompletedAt = &at
	return state, nil
}

// SetupCanSubmit reports whether step can accept input now.
func SetupCanSubmit(state SetupState, step SetupStep) bool {
	return !state.Completed() && state.Current.Valid() && step == state.Current && step != SetupStepComplete
}

// SetupRequiresAdminSession reports whether further wizard calls must be made
// by the administrator the wizard created. Before that the wizard is open, as
// on any fresh install; afterwards an anonymous caller must not steer it.
func SetupRequiresAdminSession(state SetupState) bool {
	return !state.Completed() && state.Admin.UserID != ""
}

func setupEditable(state SetupState) error {
	if state.Completed() {
		return ErrSetupCompleted
	}
	if !state.Current.Valid() {
		return ErrInvalid
	}
	return nil
}

// SetupGate is the HTTP middleware decision for a request.
type SetupGate int

const (
	SetupGateAllow SetupGate = iota
	// SetupGateRequired: answer 503 with problem code "setup_required".
	SetupGateRequired
	// SetupGateCompleted: any wizard request after completion; answer 410
	// with problem code "setup_completed". The wizard is gone for good.
	SetupGateCompleted
)

// SetupAPIPrefix is where the wizard HTTP API is expected to live.
const SetupAPIPrefix = "/api/v1/setup"

// setupOpenPaths stay reachable on a half-initialised instance so probes,
// clients and the wizard can discover state. Everything else is blocked.
var setupOpenPaths = map[string]bool{
	"/healthz":             true,
	"/readyz":              true,
	"/api/v1/system":       true,
	"/api/v1/openapi.json": true,
	"/api-docs":            true,
}

// SetupGateFor decides how a request is treated given the effective setup
// completion. It fails closed: any path not explicitly listed is blocked
// while setup is incomplete, including non-canonical spellings.
func SetupGateFor(completed bool, method, path string) SetupGate {
	wizard := path == SetupAPIPrefix || strings.HasPrefix(path, SetupAPIPrefix+"/")
	canonical := strings.HasPrefix(path, "/") && !strings.Contains(path, "//") && !strings.Contains(path, "/./") && !strings.Contains(path, "/../") && !strings.HasSuffix(path, "/.") && !strings.HasSuffix(path, "/..") && !strings.Contains(path, "\\") && !strings.Contains(path, "%")
	if completed {
		if wizard {
			return SetupGateCompleted
		}
		return SetupGateAllow
	}
	if !canonical {
		return SetupGateRequired
	}
	if wizard || setupOpenPaths[path] {
		return SetupGateAllow
	}
	return SetupGateRequired
}

// SetupListenAddress is a parsed listen address, produced outside the domain.
type SetupListenAddress struct {
	Port     uint16
	Loopback bool
	Zoned    bool
}

// SetupOrigin describes who drove a wizard change, for the audit log. It
// travels in the request context so the repository port stays unchanged.
type SetupOrigin struct {
	// Channel is "http" or "cli".
	Channel   string
	IP        string
	RequestID string
}

type setupOriginKey struct{}

func WithSetupOrigin(ctx context.Context, origin SetupOrigin) context.Context {
	return context.WithValue(ctx, setupOriginKey{}, origin)
}

func SetupOriginFrom(ctx context.Context) SetupOrigin {
	origin, _ := ctx.Value(setupOriginKey{}).(SetupOrigin)
	return origin
}
