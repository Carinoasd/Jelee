package devmode

// Toggle names one individually switchable developer-mode item. Relaxing a
// toggle only has effect while developer mode is active; every toggle reverts
// to its production default when the session ends.
type Toggle string

// ToggleKind separates G45.4 restrictions from G45.5 debug options.
type ToggleKind uint8

const (
	// KindRestriction toggles relax a production restriction (G45.4).
	KindRestriction ToggleKind = iota + 1
	// KindDebug toggles enable a debug capability (G45.5).
	KindDebug
)

// G45.4 restrictions that may be relaxed one by one.
const (
	RelaxLoginRateLimit      Toggle = "relax_login_rate_limit"
	RelaxAPIRateLimit        Toggle = "relax_api_rate_limit"
	RelaxPlaybackConcurrency Toggle = "relax_playback_concurrency"
	RelaxBandwidthLimit      Toggle = "relax_bandwidth_limit"
	RelaxIgnoreRules         Toggle = "relax_ignore_rules"
	RelaxNFOReadOnly         Toggle = "relax_nfo_read_only"
	RelaxImageLock           Toggle = "relax_image_lock"
	RelaxPermissionStrict    Toggle = "relax_permission_strict"
	RelaxHostStrict          Toggle = "relax_host_strict"
	RelaxSSRFStrict          Toggle = "relax_ssrf_strict"
	RelaxPublicIPHiding      Toggle = "relax_public_ip_hiding"
	RelaxClientUABlock       Toggle = "relax_client_ua_block"
)

// G45.5 debug options that may be enabled one by one.
const (
	DebugVerboseLogging   Toggle = "debug_verbose_logging"
	DebugSQLLogging       Toggle = "debug_sql_logging"
	DebugBodyLogging      Toggle = "debug_body_logging"
	DebugPprof            Toggle = "debug_pprof"
	DebugOpenAPIInternal  Toggle = "debug_openapi_internal"
	DebugErrorStacks      Toggle = "debug_error_stacks"
	DebugSimulatedClients Toggle = "debug_simulated_clients"
	DebugTranscode        Toggle = "dev-transcode"
	DebugMockExternal     Toggle = "debug_mock_external"
	DebugSeedData         Toggle = "debug_seed_data"
	DebugForceJobs        Toggle = "debug_force_jobs"
)

type toggleSpec struct {
	toggle    Toggle
	kind      ToggleKind
	dangerous bool
}

// toggleSpecs is the closed catalogue in requirement order. Dangerous toggles
// need an explicit IUnderstand confirmation to be switched on (G45.6).
var toggleSpecs = []toggleSpec{
	{RelaxLoginRateLimit, KindRestriction, false},
	{RelaxAPIRateLimit, KindRestriction, false},
	{RelaxPlaybackConcurrency, KindRestriction, false},
	{RelaxBandwidthLimit, KindRestriction, false},
	{RelaxIgnoreRules, KindRestriction, false},
	{RelaxNFOReadOnly, KindRestriction, true},
	{RelaxImageLock, KindRestriction, false},
	{RelaxPermissionStrict, KindRestriction, true},
	{RelaxHostStrict, KindRestriction, true},
	{RelaxSSRFStrict, KindRestriction, true},
	{RelaxPublicIPHiding, KindRestriction, true},
	{RelaxClientUABlock, KindRestriction, false},

	{DebugVerboseLogging, KindDebug, false},
	{DebugSQLLogging, KindDebug, false},
	{DebugBodyLogging, KindDebug, true},
	{DebugPprof, KindDebug, true},
	{DebugOpenAPIInternal, KindDebug, false},
	{DebugErrorStacks, KindDebug, true},
	{DebugSimulatedClients, KindDebug, false},
	{DebugTranscode, KindDebug, true},
	{DebugMockExternal, KindDebug, false},
	{DebugSeedData, KindDebug, false},
	{DebugForceJobs, KindDebug, false},
}

var toggleIndex = func() map[Toggle]toggleSpec {
	m := make(map[Toggle]toggleSpec, len(toggleSpecs))
	for _, s := range toggleSpecs {
		m[s.toggle] = s
	}
	return m
}()

// Toggles lists every known toggle in requirement order.
func Toggles() []Toggle {
	out := make([]Toggle, len(toggleSpecs))
	for i, s := range toggleSpecs {
		out[i] = s.toggle
	}
	return out
}

// Known reports whether t is part of the catalogue.
func (t Toggle) Known() bool { _, ok := toggleIndex[t]; return ok }

// Kind returns the toggle kind, or zero for unknown toggles.
func (t Toggle) Kind() ToggleKind { return toggleIndex[t].kind }

// Dangerous reports whether switching t on requires IUnderstand.
func (t Toggle) Dangerous() bool { return toggleIndex[t].dangerous }

// DangerousOperation names a G45.6 operation that always requires an explicit
// confirmation and an audit record.
type DangerousOperation string

const (
	OpDeleteAllData      DangerousOperation = "delete_all_data"
	OpRebuildLibrary     DangerousOperation = "rebuild_library"
	OpClearCache         DangerousOperation = "clear_cache"
	OpDisableAuth        DangerousOperation = "disable_auth"
	OpImportUntrustedNFO DangerousOperation = "import_untrusted_nfo"
)

// DangerousOperations lists every G45.6 operation.
func DangerousOperations() []DangerousOperation {
	return []DangerousOperation{OpDeleteAllData, OpRebuildLibrary, OpClearCache, OpDisableAuth, OpImportUntrustedNFO}
}

// Known reports whether op is part of the catalogue.
func (op DangerousOperation) Known() bool {
	for _, k := range DangerousOperations() {
		if k == op {
			return true
		}
	}
	return false
}

// RequiresDevMode reports whether op is only reachable while developer mode is
// active. Disabling authentication is a dev-only capability.
func (op DangerousOperation) RequiresDevMode() bool { return op == OpDisableAuth }

// Confirmation carries the explicit acknowledgement for dangerous actions,
// i.e. `--i-understand` on the CLI or the second UI confirmation.
type Confirmation struct {
	IUnderstand bool
}
