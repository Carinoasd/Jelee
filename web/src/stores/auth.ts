import { defineStore } from "pinia";
import { computed, shallowRef } from "vue";
import { useApi } from "@/api";
import {
  currentUser,
  login as loginRequest,
  logout as logoutRequest,
  readCsrf,
  type User,
} from "@/features/auth/api";

// Holds the signed-in user and the current session's ID only. Credential
// material stays inside the API layer's AuthStrategy so it never appears in
// reactive or devtools state.
export const useAuthStore = defineStore("auth", () => {
  const { client, auth } = useApi();
  const user = shallowRef<User | null>(null);
  /** ID of this browser's session; unknown after a reload until the next login. */
  const sessionId = shallowRef<string | null>(null);
  const isAuthenticated = computed(() => user.value !== null);
  const isAdmin = computed(() => user.value?.admin === true);
  let restoring: Promise<void> | null = null;

  /**
   * Resumes a session that survived a page reload (the HttpOnly cookie).
   * Runs at most once; any failure simply leaves the visitor signed out.
   */
  function restore(): Promise<void> {
    restoring ??= (async () => {
      if (!auth.survivesReload || user.value !== null) {
        return;
      }
      try {
        const me = await currentUser(client);
        auth.resume(await readCsrf(client));
        user.value = me;
      } catch {
        auth.clear();
      }
    })();
    return restoring;
  }

  async function login(name: string, password: string): Promise<User> {
    const grant = await loginRequest(client, name, password);
    auth.establish(grant);
    restoring = Promise.resolve();
    user.value = grant.user;
    sessionId.value = grant.session.id;
    return grant.user;
  }

  async function logout(): Promise<void> {
    try {
      if (auth.hasCredential()) {
        await logoutRequest(client);
      }
    } finally {
      expire();
    }
  }

  /** Forgets the session locally, e.g. after the server answered 401. */
  function expire(): void {
    auth.clear();
    restoring = Promise.resolve();
    user.value = null;
    sessionId.value = null;
  }

  return { user, sessionId, isAuthenticated, isAdmin, restore, login, logout, expire };
});
