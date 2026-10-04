<script setup lang="ts">
import { onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useAuthStore } from "@/stores/auth";

// Opens a share link, /share#<token>. The token is read from the fragment
// (which the browser never sends to the server) and removed from the
// address at once, so it stays out of the history, bookmarks and Referer.
// It then lives only in this component until the redemption ends.
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const auth = useAuthStore();

type Phase = "working" | "missing" | "confirm" | "failed";
const phase = shallowRef<Phase>("working");
const failure = shallowRef<ApiError | null>(null);
let token = "";

function withoutFragment(state: unknown): unknown {
  // vue-router keeps the current location in history.state; drop the
  // fragment there too.
  if (typeof state !== "object" || state === null) {
    return state;
  }
  const copy: Record<string, unknown> = { ...(state as Record<string, unknown>) };
  if (typeof copy.current === "string") {
    copy.current = copy.current.split("#")[0];
  }
  return copy;
}

/** Reads the token and clears the fragment from the address bar. */
function takeToken(): string {
  const fragment = globalThis.location.hash !== "" ? globalThis.location.hash : route.hash;
  if (globalThis.location.hash !== "") {
    globalThis.history.replaceState(withoutFragment(globalThis.history.state), "", globalThis.location.pathname + globalThis.location.search);
  }
  try {
    return decodeURIComponent(fragment.replace(/^#/, "")).trim();
  } catch {
    return "";
  }
}

async function redeem() {
  phase.value = "working";
  failure.value = null;
  try {
    // A signed-in account is signed out first, so its session is ended on
    // the server instead of being left behind by the new cookie.
    if (auth.isAuthenticated) {
      await auth.logout().catch(() => undefined);
    }
    await auth.redeemShare(token);
    token = "";
    await router.replace({ name: "shared" });
  } catch (error: unknown) {
    failure.value = error instanceof ApiError ? error : networkError(error);
    phase.value = "failed";
  }
}

async function stay() {
  token = "";
  await router.replace({ name: "home" });
}

onMounted(() => {
  token = takeToken();
  if (token === "") {
    phase.value = "missing";
  } else if (auth.isAuthenticated && !auth.isGuest) {
    phase.value = "confirm";
  } else {
    void redeem();
  }
});
</script>

<template>
  <section class="jl-redeem" aria-labelledby="redeem-title">
    <h1 id="redeem-title" tabindex="-1">{{ t("shares.redeem.title") }}</h1>
    <p v-if="phase === 'working'" role="status">{{ t("shares.redeem.working") }}</p>
    <UiAlert v-else-if="phase === 'missing'" tone="danger">
      <p>{{ t("shares.redeem.missing") }}</p>
    </UiAlert>
    <template v-else-if="phase === 'confirm'">
      <UiAlert tone="info">
        <p>{{ t("shares.redeem.signedIn", { name: auth.user?.displayName || auth.user?.name || "" }) }}</p>
      </UiAlert>
      <div class="jl-redeem__actions">
        <UiButton @click="redeem">{{ t("shares.redeem.continue") }}</UiButton>
        <UiButton variant="secondary" @click="stay">{{ t("shares.redeem.stay") }}</UiButton>
      </div>
    </template>
    <UiAlert v-else-if="failure" tone="danger" data-testid="redeem-error">
      <p>{{ t("shares.redeem.failed") }}</p>
      <p>{{ t(errorMessageKey(failure)) }}</p>
      <p v-if="failure.traceId">{{ t("errors.traceId", { id: failure.traceId }) }}</p>
    </UiAlert>
  </section>
</template>

<style scoped>
.jl-redeem {
  display: grid;
  gap: var(--jl-space-4);
  max-width: 40rem;
}

.jl-redeem h1 {
  margin: 0;
}

.jl-redeem p {
  margin: 0;
}

.jl-redeem__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
