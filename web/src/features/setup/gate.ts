import type { ApiClient } from "@/api/client";
import { setupRequired } from "./api";

// Whether the server still needs initial setup, asked once per page load.
// Completion is final, so the wizard marks it done instead of asking again.
let required: Promise<boolean> | null = null;

export function needsSetup(client: ApiClient): Promise<boolean> {
  required ??= setupRequired(client);
  return required;
}

export function markSetupComplete(): void {
  required = Promise.resolve(false);
}
