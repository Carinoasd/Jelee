import type { ManifestIssueCode, PluginPermission } from "@jelee/plugin-sdk";
import type { PluginProblemCode, PluginStatus } from "@/plugins/host/store";

export const statusKey: Readonly<Record<PluginStatus, string>> = {
  rejected: "plugins.status.rejected",
  disabled: "plugins.status.disabled",
  blocked: "plugins.status.blocked",
  loading: "plugins.status.loading",
  active: "plugins.status.active",
  failed: "plugins.status.failed",
};

export const statusTone: Readonly<Record<PluginStatus, "neutral" | "accent" | "danger">> = {
  rejected: "danger",
  disabled: "neutral",
  blocked: "danger",
  loading: "neutral",
  active: "accent",
  failed: "danger",
};

export const permissionKey: Readonly<Record<PluginPermission, string>> = {
  "catalog.read": "plugins.permissions.catalogRead",
  "user.read": "plugins.permissions.userRead",
  "settings.storage": "plugins.permissions.settingsStorage",
  "ui.routes": "plugins.permissions.uiRoutes",
  "ui.theme": "plugins.permissions.uiTheme",
};

export const issueKey: Readonly<Record<ManifestIssueCode, string>> = {
  not_object: "plugins.issues.notObject",
  unknown_field: "plugins.issues.unknownField",
  invalid_id: "plugins.issues.invalidId",
  invalid_text: "plugins.issues.invalidText",
  invalid_version: "plugins.issues.invalidVersion",
  invalid_sdk_range: "plugins.issues.invalidSdkRange",
  sdk_incompatible: "plugins.issues.sdkIncompatible",
  invalid_min_jelee: "plugins.issues.invalidMinJelee",
  jelee_too_old: "plugins.issues.jeleeTooOld",
  invalid_permissions: "plugins.issues.invalidPermissions",
  unknown_permission: "plugins.issues.unknownPermission",
  invalid_hooks: "plugins.issues.invalidHooks",
  unknown_hook: "plugins.issues.unknownHook",
  hook_needs_permission: "plugins.issues.hookNeedsPermission",
  invalid_dependencies: "plugins.issues.invalidDependencies",
  invalid_entry: "plugins.issues.invalidEntry",
  invalid_author: "plugins.issues.invalidAuthor",
};

export const problemKey: Readonly<Record<PluginProblemCode, string>> = {
  duplicate_id: "plugins.problems.duplicateId",
  dependency_missing: "plugins.problems.dependencyMissing",
  dependency_version: "plugins.problems.dependencyVersion",
  dependency_inactive: "plugins.problems.dependencyInactive",
  load_failed: "plugins.problems.loadFailed",
  invalid_definition: "plugins.problems.invalidDefinition",
  setup_failed: "plugins.problems.setupFailed",
};
