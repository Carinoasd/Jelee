import { watch } from "vue";
import { useAuthStore } from "./auth";

/**
 * Clears a store's cached data whenever the signed-in user changes (sign-out,
 * expiry or another account signing in), so nothing from one session is
 * shown in the next. Call from a store's setup function.
 */
export function resetOnUserChange(reset: () => void): void {
  const auth = useAuthStore();
  watch(
    () => auth.user?.id,
    (next, previous) => {
      if (next !== previous) {
        reset();
      }
    },
  );
}
