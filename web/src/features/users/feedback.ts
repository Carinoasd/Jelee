import { useRouter } from "vue-router";
import { ApiError, networkError } from "@/api/errors";
import { errorMessageKey } from "@/features/errors/messages";
import type { ChangeOutcome } from "@/stores/userAdmin";
import { useToastStore } from "@/stores/toasts";

/**
 * Toasts for the administration sections: localized failures by error code
 * (never server text), and the sign-in page when a change revoked the
 * administrator's own session.
 */
export function useAdminFeedback() {
  const toasts = useToastStore();
  const router = useRouter();

  function failed(error: unknown): void {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  }

  async function done(outcome: ChangeOutcome, key: string, params: Record<string, string | number> = {}): Promise<void> {
    if (outcome === "signed-out") {
      toasts.push("users.signedOutSelf", "info");
      await router.replace({ name: "login" });
      return;
    }
    toasts.push(key, "success", params);
  }

  return { failed, done };
}
