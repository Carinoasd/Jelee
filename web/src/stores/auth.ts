import { defineStore } from "pinia";
import { computed, shallowRef } from "vue";
import { useApi } from "@/api";
import { login as loginRequest, logout as logoutRequest, type User } from "@/features/auth/api";

// Holds the signed-in user only. The credential itself stays inside the API
// layer's AuthStrategy so it never appears in reactive or devtools state.
export const useAuthStore = defineStore("auth", () => {
  const { client, auth } = useApi();
  const user = shallowRef<User | null>(null);
  const isAuthenticated = computed(() => user.value !== null);

  async function login(name: string, password: string): Promise<User> {
    const grant = await loginRequest(client, name, password);
    auth.establish(grant);
    user.value = grant.user;
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
    user.value = null;
  }

  return { user, isAuthenticated, login, logout, expire };
});
