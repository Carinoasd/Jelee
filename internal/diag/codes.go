package diag

// codeInfo is fixed text: it never contains configured values, so a code's
// message and fix are always safe to print or bundle.
type codeInfo struct {
	Message string
	Fix     string
}

const (
	CodeOK            = "ok"
	CodeCheckTimeout  = "check_timeout"
	CodeCheckPanicked = "check_panicked"

	CodeConfigOK              = "config_ok"
	CodeConfigInvalid         = "config_invalid"
	CodeConfigDatabaseMissing = "config_database_missing"
	CodeConfigSecretFileMode  = "config_secret_file_permissions" //nolint:gosec // G101: a diagnostic code, not a credential

	CodeDBConnected     = "db_connected"
	CodeDBNotConfigured = "db_not_configured"
	CodeDBConfigInvalid = "db_config_invalid"
	CodeDBUnreachable   = "db_unreachable"
	CodeDBAuthFailed    = "db_auth_failed"
	CodeDBQueryFailed   = "db_query_failed"
	CodeDBUnchecked     = "db_unchecked"

	CodeSchemaCurrent   = "db_schema_current"
	CodeSchemaMissing   = "db_schema_missing"
	CodeMigrationDirty  = "db_migration_dirty"
	CodeMigrationBehind = "db_migration_behind"
	CodeSchemaNewer     = "db_schema_newer"

	CodeRootsNone        = "library_roots_none"
	CodeRootOK           = "library_root_ok"
	CodeRootMissing      = "library_root_missing"
	CodeRootNotDirectory = "library_root_not_directory"
	CodeRootUnreadable   = "library_root_unreadable"
	CodeRootTimeout      = "library_root_timeout"
	CodeRootsTruncated   = "library_roots_truncated"
	CodeRootsUnchecked   = "library_roots_unchecked"
	CodeToolVerified     = "tool_verified"
	CodeToolMissing      = "tool_missing"
	CodeToolHashMismatch = "tool_hash_mismatch"
	CodeToolUnreadable   = "tool_unreadable"
	CodeToolUnsupported  = "tool_platform_unsupported"
	CodeToolManifest     = "tool_manifest_invalid"
	// CodeEmbeddedCoversReady and CodeEmbeddedCoversNoTool report the G40.4
	// cover pass, which reads covers with ffprobe only.
	CodeEmbeddedCoversReady  = "embedded_covers_ready"
	CodeEmbeddedCoversNoTool = "embedded_covers_tool_missing"
	// CodeMatroskaVerified and the codes below report the optional
	// mkvtoolnix/MediaInfo runtime (E4).
	CodeMatroskaVerified    = "matroska_tool_verified"
	CodeMatroskaMissing     = "matroska_tool_missing"
	CodeMatroskaMismatch    = "matroska_tool_hash_mismatch"
	CodeMatroskaUnreadable  = "matroska_tool_unreadable"
	CodeMatroskaUnsupported = "matroska_tool_platform_unsupported"
	CodeDiskOK              = "disk_ok"
	CodeDiskSpaceLow        = "disk_space_low"
	CodeDiskSpaceCritical   = "disk_space_critical"
	CodeDiskInodesLow       = "disk_inodes_low"
	CodeDiskInodesCrit      = "disk_inodes_critical"
	CodeDiskUnavailable     = "disk_stat_unavailable"
	CodeDiskNoInodes        = "disk_inodes_not_applicable"

	CodeNetOK           = "net_ok"
	CodeNetListen       = "net_listen_invalid"
	CodeNetProxyInvalid = "net_proxy_invalid"
	CodeNetProxyBroad   = "net_proxy_too_broad"
	CodeNetProxyNone    = "net_proxy_none"

	CodeDirOK            = "dir_ok"
	CodeDirMissing       = "dir_missing"
	CodeDirNotDirectory  = "dir_not_directory"
	CodeDirSymlink       = "dir_symlink"
	CodeDirNotWritable   = "dir_not_writable"
	CodeDirPermissive    = "dir_permissions_too_broad"
	CodeDirOwner         = "dir_owner_mismatch"
	CodeDirNotSticky     = "dir_shared_not_sticky"
	CodeDirModeUnchecked = "dir_permissions_unchecked"
	CodeLogFileMode      = "log_file_permissions"

	CodePrivacyLoopback = "privacy_listen_loopback"
	CodePrivacyExposed  = "privacy_listen_exposed"
	CodePrivacyPublic   = "privacy_listen_public"
	CodePrivacyDebugLog = "privacy_debug_logging"
	CodePrivacyIPMask   = "privacy_ip_masked"
	CodePrivacyRelPath  = "privacy_paths_relative"
	CodePrivacyTMDB     = "privacy_tmdb_outbound"

	CodeDevDisabled          = "devmode_disabled"
	CodeDevEnvSet            = "devmode_env_set"
	CodeDevEnvInvalid        = "devmode_env_invalid"
	CodeDevCapable           = "devmode_capable"
	CodeDevProduction        = "devmode_production_environment"
	CodeDevProductionIgnored = "devmode_production_ignored"

	CodeExternalOK            = "external_tmdb_ok"
	CodeExternalNotConfigured = "external_tmdb_not_configured"
	CodeExternalCredentials   = "external_tmdb_credentials" //nolint:gosec // G101: a diagnostic code, not a credential
	CodeExternalRateLimited   = "external_tmdb_rate_limited"
	CodeExternalUnreachable   = "external_tmdb_unreachable"

	// Codes used by the CLI and the bundle exporter rather than by checks.
	CodeOutputUnsafe   = "doctor_output_unsafe"
	CodeExportExists   = "diag_output_exists"
	CodeExportFailed   = "diag_output_failed"
	CodeExportPath     = "diag_output_path_invalid"
	CodeExportFound    = "diag_sensitive_content"
	CodeExportTooLarge = "diag_bundle_too_large"
)

var codes = map[string]codeInfo{
	CodeOK:            {"check passed", ""},
	CodeCheckTimeout:  {"check did not finish within its time limit", "Re-run doctor; if it repeats, look for a hung network filesystem or database."},
	CodeCheckPanicked: {"check stopped on an internal error", "Report the issue with the doctor --json output attached."},

	CodeConfigOK:              {"configuration is valid", ""},
	CodeConfigInvalid:         {"configuration failed validation", "Fix the setting named in the message (environment or JELEE_CONFIG file) and re-run doctor."},
	CodeConfigDatabaseMissing: {"no database connection is configured", "Set JELEE_DATABASE_URL or JELEE_DATABASE_URL_FILE to the PostgreSQL database."},
	CodeConfigSecretFileMode:  {"a credential or configuration file is readable by other users", "Restrict the file: chmod 600 and owned by the service account."},

	CodeDBConnected:     {"PostgreSQL connection succeeded", ""},
	CodeDBNotConfigured: {"database check skipped: no connection configured", "Set JELEE_DATABASE_URL or JELEE_DATABASE_URL_FILE."},
	CodeDBConfigInvalid: {"database connection setting cannot be parsed", "Use a PostgreSQL URL naming host, port and database; prefer JELEE_DATABASE_URL_FILE for the credential."},
	CodeDBUnreachable:   {"PostgreSQL is unreachable", "Check that PostgreSQL is running, the host and port are reachable from this machine, and TLS settings match."},
	CodeDBAuthFailed:    {"PostgreSQL rejected the credentials", "Correct the user and password in the database URL or credential file, or the server's pg_hba.conf."},
	CodeDBQueryFailed:   {"a read-only diagnostic query failed", "Grant the service account read access to its schema and pg_stat views; check statement timeouts."},
	CodeDBUnchecked:     {"not checked because the database is unavailable", "Fix the database connection first."},

	CodeSchemaCurrent:   {"schema is at the version this binary requires and clean", ""},
	CodeSchemaMissing:   {"schema has never been migrated", "Run: jelee-migrate up"},
	CodeMigrationDirty:  {"last migration did not complete (dirty)", "Restore from backup or repair the failed migration, then run jelee-migrate status; do not start the server on a dirty schema."},
	CodeMigrationBehind: {"schema is older than this binary requires", "Back up the database, then run: jelee-migrate up"},
	CodeSchemaNewer:     {"schema is newer than this binary supports", "Run the matching newer Jelee release, or restore the backup taken before the upgrade."},

	CodeRootsNone:            {"no library roots are registered", ""},
	CodeRootOK:               {"library root exists and is readable", ""},
	CodeRootMissing:          {"library root does not exist", "Mount the media volume or correct the library root; the server only reads media and never creates roots."},
	CodeRootNotDirectory:     {"library root is not a directory", "Point the library root at a directory."},
	CodeRootUnreadable:       {"library root cannot be listed by the service account", "Grant read and execute permission on the root to the service account (read-only mounts are fine)."},
	CodeRootTimeout:          {"library root did not respond in time", "Check the network or removable filesystem backing this root."},
	CodeRootsTruncated:       {"more library roots exist than were checked", "Re-run doctor with a larger --max-roots value."},
	CodeRootsUnchecked:       {"library roots not checked because the database is unavailable", "Fix the database connection first."},
	CodeToolVerified:         {"pinned ffprobe matches the manifest hash", ""},
	CodeToolMissing:          {"pinned ffprobe is not installed", "Container: use the official runtime image. Development: run make bootstrap-media (scripts/make.ps1 bootstrap-media on Windows)."},
	CodeToolHashMismatch:     {"ffprobe does not match the pinned SHA-256 in tools/manifest.json", "Reinstall the pinned tool (make tools-clean bootstrap-media) or rebuild the runtime image; never replace it with a system ffprobe."},
	CodeToolUnreadable:       {"ffprobe exists but cannot be read", "Make the tool file a regular file readable by the service account."},
	CodeToolUnsupported:      {"no pinned ffprobe exists for this platform", "Probing is available on linux-amd64 only; other platforms run without media probing."},
	CodeToolManifest:         {"embedded tool manifest is invalid", "Rebuild jelee-cli from a clean checkout."},
	CodeEmbeddedCoversReady:  {"embedded cover extraction is enabled and its pinned ffprobe is verified (ffmpeg is not used)", ""},
	CodeEmbeddedCoversNoTool: {"embedded cover extraction is enabled but no verified pinned ffprobe exists; the pass stays off", "Use the official runtime image (it ships ffprobe, never ffmpeg) or set JELEE_ENABLE_EMBEDDED_COVERS=false."},
	CodeMatroskaVerified:     {"optional mkvtoolnix/MediaInfo tool matches the manifest hash", ""},
	CodeMatroskaMissing:      {"optional mkvtoolnix/MediaInfo tool is not installed; its feature stays off", "Container: use the official runtime image. Development: make bootstrap-matroska (scripts/make.ps1 bootstrap-matroska). Required only for Matroska subtitle/font extraction and the MediaInfo probe supplement."},
	CodeMatroskaMismatch:     {"mkvtoolnix/MediaInfo tool does not match the pinned SHA-256 in tools/manifest.json", "Reinstall the pinned tool (remove .tools/matroska, make bootstrap-matroska) or rebuild the runtime image; never replace it with a system copy."},
	CodeMatroskaUnreadable:   {"mkvtoolnix/MediaInfo tool exists but cannot be read", "Make the tool file a regular file readable by the service account."},
	CodeMatroskaUnsupported:  {"no pinned mkvtoolnix/MediaInfo exists for this platform", "The tools are pinned for linux-amd64 and windows-amd64; the sandboxed runtime runs on linux-amd64 only."},
	CodeDiskOK:               {"free space and inodes are sufficient", ""},
	CodeDiskSpaceLow:         {"free disk space is low", "Free space on this volume or move the directory to a larger volume."},
	CodeDiskSpaceCritical:    {"free disk space is critically low", "Free space now; temporary files and logs will fail to write."},
	CodeDiskInodesLow:        {"free inodes are low", "Remove many small files (stale temporary or log files) on this volume."},
	CodeDiskInodesCrit:       {"free inodes are critically low", "Remove stale small files now; new files cannot be created when inodes run out."},
	CodeDiskUnavailable:      {"disk usage could not be read", "Check the directory exists; on unsupported platforms check free space manually."},
	CodeDiskNoInodes:         {"inode counts are not reported on this platform", ""},

	CodeNetOK:           {"listen address and proxy settings are valid", ""},
	CodeNetListen:       {"listen address is not an explicit IP and port", "Set JELEE_LISTEN to IP:port, for example 127.0.0.1:8097."},
	CodeNetProxyInvalid: {"a trusted proxy entry is not a valid CIDR", "Fix JELEE_TRUSTED_PROXIES; each entry must be a CIDR such as 127.0.0.1/32."},
	CodeNetProxyBroad:   {"a trusted proxy range is so broad that clients can forge their address", "List only the proxy's own addresses (IPv4 /8 or narrower, IPv6 /16 or narrower); see docs/trusted-proxies.md."},
	CodeNetProxyNone:    {"no trusted proxies are configured; forwarded headers are ignored", ""},

	CodeDirOK:            {"directory exists, is writable and its permissions are safe", ""},
	CodeDirMissing:       {"directory does not exist", "Create the directory with mode 0700, owned by the service account."},
	CodeDirNotDirectory:  {"path is not a directory", "Point the setting at a directory."},
	CodeDirSymlink:       {"directory is a symbolic link", "Configure the real directory; symbolic link roots are refused."},
	CodeDirNotWritable:   {"directory is not writable by the service account", "Grant write permission to the service account."},
	CodeDirPermissive:    {"directory is accessible to other users", "Run: chmod 700 on the directory."},
	CodeDirOwner:         {"directory is owned by another account", "Change the owner to the service account."},
	CodeDirNotSticky:     {"shared temporary directory is world-writable without the sticky bit", "Run: chmod 1777 on the temporary directory, or set TMPDIR to a private directory."},
	CodeDirModeUnchecked: {"permission bits are not checked on this platform", ""},
	CodeLogFileMode:      {"log file is readable by other users", "Run: chmod 600 on the log file and its rotated backups."},

	CodePrivacyLoopback: {"service listens on loopback only", ""},
	CodePrivacyExposed:  {"service listens on all interfaces or a private network address", "Confirm this is intended; keep a firewall or reverse proxy in front; see docs/network-privacy.md."},
	CodePrivacyPublic:   {"service listens directly on a public address; clients will learn this address", "Put a reverse proxy in front and listen on loopback; see docs/network-privacy.md."},
	CodePrivacyDebugLog: {"DEBUG logging is enabled", "Use info level in production: unset JELEE_LOG_LEVEL or set it to info."},
	CodePrivacyIPMask:   {"client addresses are logged as masked networks", ""},
	CodePrivacyRelPath:  {"log paths keep the part below configured roots", ""},
	CodePrivacyTMDB:     {"TMDB credentials are configured; metadata requests go to TMDB", ""},

	CodeDevDisabled:          {"developer mode is off: JELEE_DEV_MODE=true and dev.enabled are not both set", ""},
	CodeDevEnvSet:            {"only one of JELEE_DEV_MODE=true and dev.enabled is set; developer mode stays off", "Remove the leftover setting unless you are preparing a development instance; see docs/developer-mode.md."},
	CodeDevEnvInvalid:        {"JELEE_DEV_MODE is neither true nor false; the server refuses to start", "Set JELEE_DEV_MODE to true or false, or unset it."},
	CodeDevCapable:           {"developer mode can be enabled on this instance (JELEE_DEV_MODE=true and dev.enabled)", "Never run this configuration in production: unset JELEE_DEV_MODE or set dev.enabled to false; see docs/developer-mode.md."},
	CodeDevProduction:        {"JELEE_ENV=production forces developer mode off", ""},
	CodeDevProductionIgnored: {"developer settings are present but ignored because JELEE_ENV=production", "Remove JELEE_DEV_MODE and dev.enabled from the production deployment."},

	CodeExternalOK:            {"TMDB accepted the configured credentials", ""},
	CodeExternalNotConfigured: {"TMDB is not configured; the server runs in local mode", ""},
	CodeExternalCredentials:   {"TMDB rejected the configured credentials", "Replace TMDB_API_KEY or TMDB_API_KEY_FILE with a valid v3 API key."},
	CodeExternalRateLimited:   {"TMDB rate-limited the check", "Wait and re-run; this does not indicate a configuration fault."},
	CodeExternalUnreachable:   {"TMDB could not be reached through the controlled outbound client", "Check DNS, firewall and outbound HTTPS to api.themoviedb.org."},

	CodeOutputUnsafe:   {"doctor output contained a value that looks sensitive and was withheld", "Report the issue; run with --json and inspect locally before sharing."},
	CodeExportExists:   {"diagnostic bundle output file already exists", "Choose a new --out file; existing files are never overwritten."},
	CodeExportFailed:   {"diagnostic bundle could not be written", "Check the output directory exists and is writable."},
	CodeExportPath:     {"diagnostic bundle output must be a .zip file name", "Pass --out with a .zip file name."},
	CodeExportFound:    {"diagnostic bundle contained a sensitive value and was deleted", "Report the issue; no bundle was kept."},
	CodeExportTooLarge: {"diagnostic bundle exceeded its size limit and was deleted", "Lower --max-log-bytes or --since."},
}
