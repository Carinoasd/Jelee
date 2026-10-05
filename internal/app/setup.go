package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SetupStateRepository persists G18 wizard progress. The PostgreSQL adapter
// implements it; every write is a compare-and-swap on SetupState.Version.
type SetupStateRepository interface {
	// LoadSetupState returns domain.ErrNotFound when no state was ever stored.
	LoadSetupState(context.Context) (domain.SetupState, error)
	// HasActiveAdmin reports an enabled, non-deleted administrator.
	HasActiveAdmin(context.Context) (bool, error)
	// SaveSetupState stores next if the stored version equals next.Version
	// (0: nothing stored yet) and returns it with the new version. A version
	// mismatch, or an attempt to modify a completed state, is domain.ErrConflict.
	SaveSetupState(context.Context, domain.SetupState) (domain.SetupState, error)
	// CreateSetupAdmin runs in one transaction: version check as above, fail
	// with domain.ErrSetupAdminExists if any active administrator exists,
	// create the administrator, then store next with Admin.UserID set.
	CreateSetupAdmin(context.Context, domain.UserInput, domain.SetupState) (domain.SetupState, error)
	// CompleteSetup runs in one transaction: version check, write the
	// initialization records derived from the state (libraries, metadata and
	// network settings) and store the completed state. Any failure rolls back.
	CompleteSetup(context.Context, domain.SetupState) (domain.SetupState, error)
}

// SetupEnvironment performs the live checks of G18.3.
// SetupAddressParser keeps net address parsing outside the application layer.
type SetupAddressParser interface {
	ParseListen(string) (domain.SetupListenAddress, bool)
	ValidProxyPrefix(string) bool
}

type SetupEnvironment interface {
	SetupAddressParser
	DatabaseStatus(context.Context) (SetupDatabaseStatus, error)
	InspectDirectory(context.Context, string) (SetupDirectoryStatus, error)
	// ListenAvailable must report the server's own current listener as
	// available, otherwise re-confirming the running address would fail.
	ListenAvailable(context.Context, string) (bool, error)
	DetectTools(context.Context) (SetupToolReport, error)
	// TMDBCredentialConfigured reports whether TMDB_API_KEY(_FILE) is set.
	// The wizard never accepts the key itself.
	TMDBCredentialConfigured() bool
}

type SetupPasswordHasher interface {
	Hash(context.Context, string) (string, error)
}

type SetupDatabaseStatus struct {
	ServerVersion    int // e.g. server_version_num 160004
	MinServerVersion int
	SchemaVersion    int
	RequiredSchema   int
	Clean            bool
}

type SetupDirectoryStatus struct {
	Exists    bool
	Directory bool
	Readable  bool
}

type SetupToolReport struct {
	Available []string
	Missing   []string
}

// SetupIssue is an actionable, fixed-code validation failure. Field names a
// stable input path; Code is a stable machine code the UI localizes. Neither
// echoes user input.
type SetupIssue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type SetupValidationError struct {
	Step   domain.SetupStep
	Issues []SetupIssue
}

func (e *SetupValidationError) Error() string {
	return fmt.Sprintf("setup step %s has %d validation issue(s)", e.Step, len(e.Issues))
}

func (e *SetupValidationError) Unwrap() error { return domain.ErrInvalid }

// Password creation policy. Length bounds mirror platform/password
// (MinPasswordBytes/MaxPasswordBytes); a CLI test pins them together.
const (
	SetupMinPasswordBytes   = 12
	SetupMaxPasswordBytes   = 1024
	setupMinDistinctRunes   = 4
	setupMaxLibraries       = 64
	setupMaxAllowedHosts    = 32
	setupMaxTrustedProxies  = 64
	setupMaxHostBytes       = 253
	setupMaxLibraryPathByte = 4096
)

type SetupAdminInput struct {
	Name        string
	DisplayName string
}

// SetupPlan is the complete non-sensitive input of a headless setup.
type SetupPlan struct {
	Locale              string
	Admin               SetupAdminInput
	Media               []domain.SetupLibrary
	TMDB                domain.SetupTMDB
	AcceptDegradedTools bool
	MetadataPolicy      domain.SetupMetadataPolicy
	Network             domain.SetupNetwork
}

func ValidateSetupLanguage(locale string) []SetupIssue {
	if !ValidLocale(locale) {
		return []SetupIssue{{"locale", "locale_unsupported"}}
	}
	return nil
}

func ValidateSetupAdmin(input SetupAdminInput) []SetupIssue {
	var issues []SetupIssue
	if !ValidUserName(input.Name) {
		issues = append(issues, SetupIssue{"admin.name", "admin_name_invalid"})
	}
	if !validText(input.DisplayName, 128, true) {
		issues = append(issues, SetupIssue{"admin.displayName", "admin_display_name_invalid"})
	}
	return issues
}

// ValidateSetupPassword is the G18.1 strength check: the account policy
// (12..1024 UTF-8 bytes, no trimming or normalisation) plus rejection of
// trivially weak values.
func ValidateSetupPassword(name, password string) []SetupIssue {
	switch {
	case !utf8.ValidString(password):
		return []SetupIssue{{"admin.password", "password_encoding_invalid"}}
	case len(password) < SetupMinPasswordBytes || len(password) > SetupMaxPasswordBytes:
		return []SetupIssue{{"admin.password", "password_length_invalid"}}
	}
	distinct := map[rune]bool{}
	for _, r := range password {
		distinct[r] = true
	}
	if len(distinct) < setupMinDistinctRunes {
		return []SetupIssue{{"admin.password", "password_too_simple"}}
	}
	if len(name) >= 3 && strings.Contains(strings.ToLower(password), strings.ToLower(name)) {
		return []SetupIssue{{"admin.password", "password_contains_name"}}
	}
	return nil
}

func ValidateSetupMedia(libraries []domain.SetupLibrary) []SetupIssue {
	if len(libraries) > setupMaxLibraries {
		return []SetupIssue{{"media", "media_too_many"}}
	}
	var issues []SetupIssue
	names := map[string]bool{}
	for i, library := range libraries {
		field := fmt.Sprintf("media[%d]", i)
		if !ValidUserName(library.Name) {
			issues = append(issues, SetupIssue{field + ".name", "media_name_invalid"})
		} else if key := strings.ToLower(library.Name); names[key] {
			issues = append(issues, SetupIssue{field + ".name", "media_name_duplicate"})
		} else {
			names[key] = true
		}
		if !validText(library.Path, setupMaxLibraryPathByte, false) || !filepath.IsAbs(library.Path) || filepath.Clean(library.Path) != library.Path {
			issues = append(issues, SetupIssue{field + ".path", "media_path_not_absolute"})
			continue
		}
		for j := range i {
			other := libraries[j].Path
			if other == library.Path {
				issues = append(issues, SetupIssue{field + ".path", "media_path_duplicate"})
				break
			}
			if setupPathWithin(other, library.Path) || setupPathWithin(library.Path, other) {
				issues = append(issues, SetupIssue{field + ".path", "media_path_nested"})
				break
			}
		}
	}
	return issues
}

func setupPathWithin(parent, child string) bool {
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func ValidateSetupTMDB(tmdb domain.SetupTMDB) []SetupIssue {
	if tmdb.Enabled && !domain.ValidMetadataLanguage(tmdb.Language) || !tmdb.Enabled && tmdb.Language != "" {
		return []SetupIssue{{"tmdb.language", "tmdb_language_invalid"}}
	}
	return nil
}

func ValidateSetupMetadataPolicy(policy domain.SetupMetadataPolicy, tmdbEnabled bool) []SetupIssue {
	var issues []SetupIssue
	if !domain.ValidNFOMode(policy.NFORead) {
		issues = append(issues, SetupIssue{"metadataPolicy.nfoRead", "nfo_read_mode_invalid"})
	}
	switch policy.NFOWrite {
	case domain.SetupNFOWriteOff:
	case domain.SetupNFOWriteBack:
		if policy.NFORead != domain.NFOModeReadOnly {
			issues = append(issues, SetupIssue{"metadataPolicy.nfoWrite", "nfo_write_requires_read"})
		}
	default:
		issues = append(issues, SetupIssue{"metadataPolicy.nfoWrite", "nfo_write_mode_invalid"})
	}
	if policy.ImageFetch && !tmdbEnabled {
		issues = append(issues, SetupIssue{"metadataPolicy.imageFetch", "image_fetch_requires_tmdb"})
	}
	return issues
}

func ValidateSetupNetwork(network domain.SetupNetwork, addresses SetupAddressParser) []SetupIssue {
	var issues []SetupIssue
	mode := network.Mode
	if mode != domain.SetupNetworkLocal && mode != domain.SetupNetworkLAN && mode != domain.SetupNetworkReverseProxy {
		issues = append(issues, SetupIssue{"network.mode", "network_mode_invalid"})
	}
	listen, ok := domain.SetupListenAddress{}, false
	if addresses != nil {
		listen, ok = addresses.ParseListen(network.Listen)
	}
	switch {
	case !ok || listen.Port == 0 || listen.Zoned:
		issues = append(issues, SetupIssue{"network.listen", "listen_invalid"})
	case mode == domain.SetupNetworkLocal && !listen.Loopback:
		issues = append(issues, SetupIssue{"network.listen", "listen_not_loopback"})
	case mode == domain.SetupNetworkLAN && listen.Loopback:
		issues = append(issues, SetupIssue{"network.listen", "listen_loopback_only"})
	}
	if len(network.AllowedHosts) == 0 || len(network.AllowedHosts) > setupMaxAllowedHosts {
		issues = append(issues, SetupIssue{"network.allowedHosts", "allowed_hosts_count_invalid"})
	}
	hosts := map[string]bool{}
	for i, host := range network.AllowedHosts {
		if host == "" || len(host) > setupMaxHostBytes || host != strings.ToLower(host) || strings.ContainsAny(host, " /\\@\r\n\t") || !utf8.ValidString(host) || hosts[host] {
			issues = append(issues, SetupIssue{fmt.Sprintf("network.allowedHosts[%d]", i), "allowed_host_invalid"})
		}
		hosts[host] = true
	}
	if len(network.TrustedProxies) > setupMaxTrustedProxies {
		issues = append(issues, SetupIssue{"network.trustedProxies", "trusted_proxies_too_many"})
	}
	for i, value := range network.TrustedProxies {
		if addresses == nil || !addresses.ValidProxyPrefix(value) {
			issues = append(issues, SetupIssue{fmt.Sprintf("network.trustedProxies[%d]", i), "trusted_proxy_invalid"})
		}
	}
	if mode == domain.SetupNetworkReverseProxy && len(network.TrustedProxies) == 0 {
		issues = append(issues, SetupIssue{"network.trustedProxies", "trusted_proxies_required"})
	}
	// Anything reachable beyond loopback exposes the server address to
	// clients (docs/network-privacy.md); the operator must acknowledge that.
	if (mode == domain.SetupNetworkLAN || mode == domain.SetupNetworkReverseProxy) && !network.PrivacyAcknowledged {
		issues = append(issues, SetupIssue{"network.privacyAcknowledged", "privacy_acknowledgement_required"})
	}
	return issues
}

// ValidateSetupPlan checks a headless plan without touching the environment.
// The password is checked separately with ValidateSetupPassword so callers
// can reject bad flags before reading a secret.
func ValidateSetupPlan(plan SetupPlan, addresses SetupAddressParser) []SetupIssue {
	var issues []SetupIssue
	issues = append(issues, ValidateSetupLanguage(plan.Locale)...)
	issues = append(issues, ValidateSetupAdmin(plan.Admin)...)
	issues = append(issues, ValidateSetupMedia(plan.Media)...)
	issues = append(issues, ValidateSetupTMDB(plan.TMDB)...)
	issues = append(issues, ValidateSetupMetadataPolicy(plan.MetadataPolicy, plan.TMDB.Enabled)...)
	issues = append(issues, ValidateSetupNetwork(plan.Network, addresses)...)
	return issues
}

// Setup is the G18 wizard use case shared by HTTP and `jelee-cli setup`.
type Setup struct {
	repository  SetupStateRepository
	environment SetupEnvironment
	passwords   SetupPasswordHasher
	now         func() time.Time
}

func NewSetup(repository SetupStateRepository, environment SetupEnvironment, passwords SetupPasswordHasher, now func() time.Time) (*Setup, error) {
	if repository == nil || environment == nil || passwords == nil || now == nil {
		return nil, domain.ErrInvalid
	}
	return &Setup{repository: repository, environment: environment, passwords: passwords, now: now}, nil
}

func (s *Setup) Status(ctx context.Context) (domain.SetupStatus, error) {
	state, err := s.repository.LoadSetupState(ctx)
	stored := true
	if errors.Is(err, domain.ErrNotFound) {
		stored, err = false, nil
	}
	if err != nil {
		return domain.SetupStatus{}, fmt.Errorf("load setup state: %w", err)
	}
	active := false
	if !stored {
		if active, err = s.repository.HasActiveAdmin(ctx); err != nil {
			return domain.SetupStatus{}, fmt.Errorf("check existing administrator: %w", err)
		}
	}
	return domain.EffectiveSetupStatus(state, stored, active), nil
}

func (s *Setup) editable(ctx context.Context) (domain.SetupState, error) {
	status, err := s.Status(ctx)
	if err != nil {
		return domain.SetupState{}, err
	}
	if status.Completed {
		return domain.SetupState{}, domain.ErrSetupCompleted
	}
	return status.State, nil
}

func (s *Setup) submit(ctx context.Context, step domain.SetupStep, check func(domain.SetupState) ([]SetupIssue, error), apply func(*domain.SetupState)) (domain.SetupState, error) {
	state, err := s.editable(ctx)
	if err != nil {
		return state, err
	}
	if !domain.SetupCanSubmit(state, step) {
		return state, domain.ErrSetupStepOrder
	}
	issues, err := check(state)
	if err != nil {
		return state, err
	}
	if len(issues) > 0 {
		return state, &SetupValidationError{Step: step, Issues: issues}
	}
	apply(&state)
	next, err := domain.SetupAdvance(state, step)
	if err != nil {
		return state, err
	}
	return s.repository.SaveSetupState(ctx, next)
}

func (s *Setup) SubmitLanguage(ctx context.Context, locale string) (domain.SetupState, error) {
	return s.submit(ctx, domain.SetupStepLanguage, func(domain.SetupState) ([]SetupIssue, error) {
		return ValidateSetupLanguage(locale), nil
	}, func(state *domain.SetupState) { state.Locale = locale })
}

// SubmitAdmin creates the administrator exactly once. When the wizard already
// created one (re-entry after Back or an interrupted run) the name must match
// and the password is neither re-validated nor re-hashed; password recovery
// goes through `jelee-cli account set-password`.
func (s *Setup) SubmitAdmin(ctx context.Context, input SetupAdminInput, password string) (domain.SetupState, error) {
	state, err := s.editable(ctx)
	if err != nil {
		return state, err
	}
	if !domain.SetupCanSubmit(state, domain.SetupStepAdmin) {
		return state, domain.ErrSetupStepOrder
	}
	if state.Admin.UserID != "" {
		if !strings.EqualFold(state.Admin.Name, input.Name) {
			return state, &SetupValidationError{Step: domain.SetupStepAdmin, Issues: []SetupIssue{{"admin.name", "admin_already_created"}}}
		}
		next, err := domain.SetupAdvance(state, domain.SetupStepAdmin)
		if err != nil {
			return state, err
		}
		return s.repository.SaveSetupState(ctx, next)
	}
	issues := ValidateSetupAdmin(input)
	issues = append(issues, ValidateSetupPassword(input.Name, password)...)
	if len(issues) > 0 {
		return state, &SetupValidationError{Step: domain.SetupStepAdmin, Issues: issues}
	}
	hash, err := s.passwords.Hash(ctx, password)
	if err != nil {
		return state, fmt.Errorf("hash setup administrator password: %w", err)
	}
	next := state
	next.Admin = domain.SetupAdmin{Name: input.Name, DisplayName: input.DisplayName}
	if next, err = domain.SetupAdvance(next, domain.SetupStepAdmin); err != nil {
		return state, err
	}
	stored, err := s.repository.CreateSetupAdmin(ctx, domain.UserInput{Name: input.Name, DisplayName: input.DisplayName, Locale: state.Locale, Admin: true, PasswordHash: hash}, next)
	if errors.Is(err, domain.ErrSetupAdminExists) {
		return state, &SetupValidationError{Step: domain.SetupStepAdmin, Issues: []SetupIssue{{"admin.name", "admin_exists"}}}
	}
	if err != nil {
		return state, fmt.Errorf("create setup administrator: %w", err)
	}
	if !domain.ValidID(stored.Admin.UserID) {
		return state, fmt.Errorf("create setup administrator: %w", domain.ErrDatabase)
	}
	return stored, nil
}

func (s *Setup) checkDatabase(ctx context.Context) ([]SetupIssue, int, error) {
	status, err := s.environment.DatabaseStatus(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("check database: %w", err)
	}
	var issues []SetupIssue
	if status.ServerVersion < status.MinServerVersion {
		issues = append(issues, SetupIssue{"database.server", "database_server_outdated"})
	}
	if !status.Clean {
		issues = append(issues, SetupIssue{"database.schema", "database_migration_dirty"})
	} else if status.SchemaVersion < 1 || status.SchemaVersion < status.RequiredSchema {
		issues = append(issues, SetupIssue{"database.schema", "database_schema_outdated"})
	}
	return issues, status.SchemaVersion, nil
}

func (s *Setup) SubmitDatabase(ctx context.Context) (domain.SetupState, error) {
	schema := 0
	return s.submit(ctx, domain.SetupStepDatabase, func(domain.SetupState) ([]SetupIssue, error) {
		issues, version, err := s.checkDatabase(ctx)
		schema = version
		return issues, err
	}, func(state *domain.SetupState) { state.Database.SchemaVersion = schema })
}

func (s *Setup) checkMedia(ctx context.Context, libraries []domain.SetupLibrary) ([]SetupIssue, error) {
	if issues := ValidateSetupMedia(libraries); len(issues) > 0 {
		return issues, nil
	}
	var issues []SetupIssue
	for i, library := range libraries {
		status, err := s.environment.InspectDirectory(ctx, library.Path)
		if err != nil {
			return nil, fmt.Errorf("inspect media directory: %w", err)
		}
		field := fmt.Sprintf("media[%d].path", i)
		switch {
		case !status.Exists:
			issues = append(issues, SetupIssue{field, "media_directory_missing"})
		case !status.Directory:
			issues = append(issues, SetupIssue{field, "media_directory_not_directory"})
		case !status.Readable:
			issues = append(issues, SetupIssue{field, "media_directory_unreadable"})
		}
	}
	return issues, nil
}

func (s *Setup) SubmitMedia(ctx context.Context, libraries []domain.SetupLibrary) (domain.SetupState, error) {
	return s.submit(ctx, domain.SetupStepMedia, func(domain.SetupState) ([]SetupIssue, error) {
		return s.checkMedia(ctx, libraries)
	}, func(state *domain.SetupState) { state.Media = append([]domain.SetupLibrary(nil), libraries...) })
}

func (s *Setup) SubmitTMDB(ctx context.Context, tmdb domain.SetupTMDB) (domain.SetupState, error) {
	return s.submit(ctx, domain.SetupStepTMDB, func(domain.SetupState) ([]SetupIssue, error) {
		issues := ValidateSetupTMDB(tmdb)
		if len(issues) == 0 && tmdb.Enabled && !s.environment.TMDBCredentialConfigured() {
			issues = append(issues, SetupIssue{"tmdb.enabled", "tmdb_credential_missing"})
		}
		return issues, nil
	}, func(state *domain.SetupState) { state.TMDB = tmdb })
}

func (s *Setup) SubmitToolchain(ctx context.Context, acceptDegraded bool) (domain.SetupState, error) {
	var report SetupToolReport
	return s.submit(ctx, domain.SetupStepToolchain, func(domain.SetupState) ([]SetupIssue, error) {
		var err error
		if report, err = s.environment.DetectTools(ctx); err != nil {
			return nil, fmt.Errorf("detect media tools: %w", err)
		}
		if len(report.Missing) > 0 && !acceptDegraded {
			return []SetupIssue{{"toolchain", "toolchain_missing"}}, nil
		}
		return nil, nil
	}, func(state *domain.SetupState) {
		state.Toolchain = domain.SetupToolchain{Available: append([]string(nil), report.Available...), Missing: append([]string(nil), report.Missing...), AcceptDegraded: acceptDegraded}
	})
}

func (s *Setup) SubmitMetadataPolicy(ctx context.Context, policy domain.SetupMetadataPolicy) (domain.SetupState, error) {
	return s.submit(ctx, domain.SetupStepMetadataPolicy, func(state domain.SetupState) ([]SetupIssue, error) {
		return ValidateSetupMetadataPolicy(policy, state.TMDB.Enabled), nil
	}, func(state *domain.SetupState) { state.MetadataPolicy = policy })
}

func (s *Setup) SubmitNetwork(ctx context.Context, network domain.SetupNetwork) (domain.SetupState, error) {
	return s.submit(ctx, domain.SetupStepNetwork, func(domain.SetupState) ([]SetupIssue, error) {
		if issues := ValidateSetupNetwork(network, s.environment); len(issues) > 0 {
			return issues, nil
		}
		available, err := s.environment.ListenAvailable(ctx, network.Listen)
		if err != nil {
			return nil, fmt.Errorf("check listen address: %w", err)
		}
		if !available {
			return []SetupIssue{{"network.listen", "listen_port_in_use"}}, nil
		}
		return nil, nil
	}, func(state *domain.SetupState) {
		network.AllowedHosts = append([]string(nil), network.AllowedHosts...)
		network.TrustedProxies = append([]string(nil), network.TrustedProxies...)
		state.Network = network
	})
}

func (s *Setup) Back(ctx context.Context) (domain.SetupState, error) {
	state, err := s.editable(ctx)
	if err != nil {
		return state, err
	}
	next, err := domain.SetupBack(state)
	if err != nil {
		return state, err
	}
	return s.repository.SaveSetupState(ctx, next)
}

// Finish rechecks the facts that may have changed since their step (database
// schema, media directories) and commits everything in one transaction.
func (s *Setup) Finish(ctx context.Context) (domain.SetupState, error) {
	state, err := s.editable(ctx)
	if err != nil {
		return state, err
	}
	next, err := domain.SetupFinish(state, s.now())
	if err != nil {
		return state, err
	}
	issues, _, err := s.checkDatabase(ctx)
	if err != nil {
		return state, err
	}
	mediaIssues, err := s.checkMedia(ctx, state.Media)
	if err != nil {
		return state, err
	}
	if issues = append(issues, mediaIssues...); len(issues) > 0 {
		return state, &SetupValidationError{Step: domain.SetupStepComplete, Issues: issues}
	}
	return s.repository.CompleteSetup(ctx, next)
}

// RunHeadless drives the whole wizard from a plan (`jelee-cli setup
// --non-interactive`). It resumes safely: an interrupted run is rewound to
// the first step and replayed, and the admin step is skipped if the earlier
// run already created the administrator.
func (s *Setup) RunHeadless(ctx context.Context, plan SetupPlan, password string) (domain.SetupState, error) {
	if issues := append(ValidateSetupPlan(plan, s.environment), ValidateSetupPassword(plan.Admin.Name, password)...); len(issues) > 0 {
		return domain.SetupState{}, &SetupValidationError{Issues: issues}
	}
	state, err := s.editable(ctx)
	if err != nil {
		return state, err
	}
	if state.Current != domain.SetupStepLanguage {
		for state.Current != domain.SetupStepLanguage {
			if state, err = domain.SetupBack(state); err != nil {
				return state, err
			}
		}
		if state, err = s.repository.SaveSetupState(ctx, state); err != nil {
			return state, err
		}
	}
	steps := []func() (domain.SetupState, error){
		func() (domain.SetupState, error) { return s.SubmitLanguage(ctx, plan.Locale) },
		func() (domain.SetupState, error) { return s.SubmitAdmin(ctx, plan.Admin, password) },
		func() (domain.SetupState, error) { return s.SubmitDatabase(ctx) },
		func() (domain.SetupState, error) { return s.SubmitMedia(ctx, plan.Media) },
		func() (domain.SetupState, error) { return s.SubmitTMDB(ctx, plan.TMDB) },
		func() (domain.SetupState, error) { return s.SubmitToolchain(ctx, plan.AcceptDegradedTools) },
		func() (domain.SetupState, error) { return s.SubmitMetadataPolicy(ctx, plan.MetadataPolicy) },
		func() (domain.SetupState, error) { return s.SubmitNetwork(ctx, plan.Network) },
		func() (domain.SetupState, error) { return s.Finish(ctx) },
	}
	for _, step := range steps {
		if state, err = step(); err != nil {
			return state, err
		}
	}
	return state, nil
}
