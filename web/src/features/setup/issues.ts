/**
 * Catalog keys of the wizard's fixed validation codes. Codes are stable
 * server contract values (internal/app/setup.go); unknown ones fall back to
 * a generic message so a newer server never breaks the page.
 */
const issueKeys: Readonly<Record<string, string>> = {
  locale_unsupported: "setup.issues.localeUnsupported",
  admin_name_invalid: "setup.issues.adminNameInvalid",
  admin_display_name_invalid: "setup.issues.adminVisibleNameInvalid",
  admin_already_created: "setup.issues.adminAlreadyCreated",
  admin_exists: "setup.issues.adminExists",
  password_encoding_invalid: "setup.issues.passwordEncodingInvalid",
  password_length_invalid: "setup.issues.passwordLengthInvalid",
  password_too_simple: "setup.issues.passwordTooSimple",
  password_contains_name: "setup.issues.passwordContainsName",
  database_server_outdated: "setup.issues.databaseServerOutdated",
  database_migration_dirty: "setup.issues.databaseMigrationDirty",
  database_schema_outdated: "setup.issues.databaseSchemaOutdated",
  media_too_many: "setup.issues.mediaTooMany",
  media_name_invalid: "setup.issues.mediaNameInvalid",
  media_name_duplicate: "setup.issues.mediaNameDuplicate",
  media_path_not_absolute: "setup.issues.mediaPathNotAbsolute",
  media_path_duplicate: "setup.issues.mediaPathDuplicate",
  media_path_nested: "setup.issues.mediaPathNested",
  media_directory_missing: "setup.issues.mediaDirectoryMissing",
  media_directory_not_directory: "setup.issues.mediaDirectoryNotDirectory",
  media_directory_unreadable: "setup.issues.mediaDirectoryUnreadable",
  tmdb_language_invalid: "setup.issues.tmdbLanguageInvalid",
  tmdb_credential_missing: "setup.issues.tmdbCredentialMissing",
  toolchain_missing: "setup.issues.toolchainMissing",
  nfo_read_mode_invalid: "setup.issues.nfoReadModeInvalid",
  nfo_write_mode_invalid: "setup.issues.nfoWriteModeInvalid",
  nfo_write_requires_read: "setup.issues.nfoWriteRequiresRead",
  image_fetch_requires_tmdb: "setup.issues.imageFetchRequiresTmdb",
  network_mode_invalid: "setup.issues.networkModeInvalid",
  listen_invalid: "setup.issues.listenInvalid",
  listen_not_loopback: "setup.issues.listenNotLoopback",
  listen_loopback_only: "setup.issues.listenLoopbackOnly",
  listen_port_in_use: "setup.issues.listenPortInUse",
  allowed_hosts_count_invalid: "setup.issues.allowedHostsCountInvalid",
  allowed_host_invalid: "setup.issues.allowedHostInvalid",
  trusted_proxies_too_many: "setup.issues.trustedProxiesTooMany",
  trusted_proxy_invalid: "setup.issues.trustedProxyInvalid",
  trusted_proxies_required: "setup.issues.trustedProxiesRequired",
  privacy_acknowledgement_required: "setup.issues.privacyAcknowledgementRequired",
};

export function setupIssueKey(code: string): string {
  return issueKeys[code] ?? "setup.issues.unknown";
}

/** Splits "media[1].path" into a 1-based row label for list fields. */
export function setupIssueRow(field: string): number | null {
  const match = /^\w+\[(\d+)\]/.exec(field);
  return match === null ? null : Number(match[1]) + 1;
}
