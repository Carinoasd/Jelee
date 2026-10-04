package httpapi

import (
	"bytes"
	"encoding/json"
	"runtime"
	"sort"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// errorCodeStatuses is the public error code table. Every code written by this
// adapter must appear with each HTTP status it can be sent with; the contract
// tests audit production sources against this table.
var errorCodeStatuses = map[string][]int{
	"account_busy":               {503},
	"app_password_required":      {403},
	"auth_rate_limited":          {429},
	"authentication_required":    {401},
	"body_too_large":             {413},
	"client_blocked":             {403},
	"client_pending_approval":    {403},
	"client_rate_limited":        {429},
	"client_read_only":           {403},
	"confirmation_required":      {400},
	"conflict":                   {409},
	"devmode_inactive":           {409},
	"devmode_toggle_unavailable": {409},
	"csrf_failed":                {403},
	"custom_css_rejected":        {400},
	"version_identity_conflict":  {409},
	"version_merge_incompatible": {409},
	"version_undo_unavailable":   {409},
	"version_item_busy":          {409},
	"device_stream_limit":        {429},
	"feature_removed":            {501},
	"forbidden":                  {403},
	"ignore_unavailable":         {503},
	"image_busy":                 {503},
	"image_too_large":            {413},
	"image_unavailable":          {404},
	"image_unsupported":          {415},
	"internal_error":             {500},
	"invalid_host":               {400},
	"invalid_two_factor_code":    {400},
	"invalid_password":           {400},
	"invalid_range":              {416},
	"invalid_request":            {400},
	"job_busy":                   {409},
	"job_queue_full":             {429},
	"jobs_busy":                  {503},
	"login_challenge_invalid":    {401},
	"last_admin":                 {409},
	"lookup_timeout":             {504},
	"metadata_unavailable":       {503},
	"method_not_allowed":         {405},
	"native_login_disabled":      {403},
	"metrics_busy":               {503},
	"nfo_cache_capacity":         {409},
	"nfo_disabled":               {409},
	"nfo_identity_mismatch":      {409},
	"nfo_invalidated":            {409},
	"nfo_reader_unavailable":     {503},
	"not_found":                  {404},
	"not_ready":                  {503},
	"playback_busy":              {503},
	"precondition_failed":        {412},
	"probe_cache_capacity":       {409},
	"probe_disabled":             {409},
	"probe_identity_mismatch":    {409},
	"probe_invalidated":          {409},
	"probe_runtime_unavailable":  {503},
	"request_timeout":            {408},
	"scan_limit":                 {409},
	"scan_unavailable":           {503},
	"session_limit":              {429},
	"share_forbidden":            {403},
	"share_playback_disabled":    {403},
	"share_read_only":            {403},
	"share_unavailable":          {404},
	"setup_completed":            {410},
	"setup_required":             {503},
	"setup_step_order":           {409},
	"setup_token_invalid":        {401},
	"setup_validation_failed":    {400},
	"stats_export_limit":         {409},
	"stream_limit":               {429},
	"two_factor_unavailable":     {409},
	"transcode_disabled":         {409},
	"unsupported_media_type":     {415},
	"user_stream_limit":          {429},
	"web_playback_disabled":      {403},
	"webhook_target_denied":      {400},
}

// errorSpecification documents the single error envelope written by
// writeProblem and attaches it to every default and 4xx/5xx response.
func errorSpecification(paths, schemas map[string]any) {
	codes := make([]string, 0, len(errorCodeStatuses))
	table := map[string]any{}
	for code, statuses := range errorCodeStatuses {
		codes = append(codes, code)
		values := make([]string, 0, len(statuses))
		for _, status := range statuses {
			values = append(values, strconv.Itoa(status))
		}
		table[code] = values
	}
	sort.Strings(codes)
	schemas["ErrorCode"] = map[string]any{"type": "string", "enum": codes, "description": "Stable machine-readable error code. x-jelee-statuses lists the HTTP status codes each value is sent with.", "x-jelee-statuses": table}
	schemas["Error"] = objectSchema(map[string]any{"error": objectSchema(map[string]any{
		"code":    schemaRef("ErrorCode"),
		"message": map[string]any{"type": "string", "description": "Human-readable message localized from Accept-Language or the authenticated user's locale; see Content-Language."},
		"details": map[string]any{"type": "object", "description": "Structured details. Empty except for setup_validation_failed, which carries step (wizard step name) and issues (array of {field, code}: a fixed input path and a stable machine code; input values are never echoed)."},
		"traceId": map[string]any{"type": "string", "description": "Same value as the X-Request-ID response header."},
	}, "code", "message", "details", "traceId")}, "error")
	content := map[string]any{"application/json": map[string]any{"schema": schemaRef("Error")}}
	for _, item := range paths {
		for _, op := range item.(map[string]any) {
			for status, raw := range op.(map[string]any)["responses"].(map[string]any) {
				response := raw.(map[string]any)
				if status != "default" && (len(status) != 3 || status[0] < '4' || status[0] > '5') {
					continue
				}
				if _, exists := response["content"]; !exists {
					response["content"] = content
				}
			}
		}
	}
}

// ReferenceConfig is the rollout rendered into the committed api/openapi.json:
// every optional feature enabled with default limits. The TMDB key is a
// syntactically valid placeholder and is never written into the document.
func ReferenceConfig() config.Config {
	return config.Config{
		Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost", "127.0.0.1", "::1"},
		DatabaseURL: "postgres://localhost/jelee", TMDBAPIKey: "00000000000000000000000000000000",
		MaxConnections: 8, MaxStreams: 8, RequestTimeoutSeconds: 15,
		EnableCatalog: true, EnableDirect: true, EnableAccounts: true, EnableMetrics: true, EnableImages: true, EnableJobs: true, EnableProbe: true, EnableFamilyIgnore: true, EnableCompat: true, EnableWebhooks: true,
		Webhooks: func() config.WebhooksConfig {
			webhooks := config.DefaultWebhooksConfig()
			// Placeholder key: rendering validates the configuration only.
			webhooks.MasterKey = "0000000000000000000000000000000000000000000000000000000000000000"
			return webhooks
		}(),
		Accounts: config.DefaultAccountsConfig(), Jobs: config.DefaultJobsConfig(),
		Images: func() config.ImagesConfig {
			images := config.DefaultImagesConfig()
			images.TempRoot = referenceImageTempRoot()
			return images
		}(),
		// The optional extraction routes (E4) are part of the reference
		// document; the cache root only has to pass validation.
		Matroska: config.MatroskaConfig{EnableExtraction: true, CacheRoot: referenceImageTempRoot(), CacheMaxBytes: 1 << 30},
	}
}

// referenceImageTempRoot only has to pass configuration validation, which
// requires an absolute path on the running platform; it never appears in the
// document, so the rendered specification is identical on every OS.
func referenceImageTempRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\jelee\image-tmp`
	}
	return "/var/lib/jelee/image-tmp"
}

// MarshalSpecification renders Specification(cfg) deterministically: object
// keys are sorted by encoding/json, indentation is two spaces and the output
// ends with a newline.
func MarshalSpecification(cfg config.Config) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(Specification(cfg)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
