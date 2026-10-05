<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { ApiError, networkError } from "@/api/errors";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useAuthStore } from "@/stores/auth";
import { useSessionsStore } from "@/stores/sessions";
import { useToastStore } from "@/stores/toasts";
import type { Session } from "./api";

const { t, locale } = useI18n();
const auth = useAuthStore();
const sessions = useSessionsStore();
const toasts = useToastStore();
const router = useRouter();
const confirmingAll = shallowRef(false);
const revokingAll = shallowRef(false);

const localeNames = {
  "zh-CN": "common.localeNames.zhCN",
  "zh-TW": "common.localeNames.zhTW",
  "ja-JP": "common.localeNames.jaJP",
  "en-US": "common.localeNames.enUS",
} as const;

const profile = computed(() => auth.user);

onMounted(() => {
  void sessions.load();
});

function deviceLabel(session: Session): string {
  const app = [session.client, session.version].filter((part) => part !== undefined && part !== "").join(" ");
  return app === "" ? session.deviceName : `${session.deviceName} · ${app}`;
}

function failure(error: unknown): ApiError {
  return error instanceof ApiError ? error : networkError(error);
}

async function signedOut(key: string) {
  toasts.push(key, "info");
  await router.replace({ name: "login" });
}

async function revoke(session: Session) {
  try {
    const outcome = await sessions.revoke(session.id);
    if (outcome === "signed-out") {
      await signedOut("account.signedOutHere");
    } else {
      toasts.push("account.revoked", "success", { name: session.deviceName });
    }
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

async function revokeAll() {
  revokingAll.value = true;
  try {
    await sessions.revokeAll();
    await signedOut("account.signedOutEverywhere");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  } finally {
    revokingAll.value = false;
    confirmingAll.value = false;
  }
}
</script>

<template>
  <section class="jl-account" aria-labelledby="account-title">
    <h1 id="account-title" tabindex="-1">{{ t("account.title") }}</h1>

    <section v-if="profile" class="jl-account__card" aria-labelledby="profile-title">
      <h2 id="profile-title">{{ t("account.profile") }}</h2>
      <dl class="jl-account__profile">
        <dt>{{ t("account.profileName") }}</dt>
        <dd>{{ profile.displayName || profile.name }}</dd>
        <dt>{{ t("account.name") }}</dt>
        <dd>{{ profile.name }}</dd>
        <dt>{{ t("account.role") }}</dt>
        <dd>{{ profile.admin ? t("account.roleAdmin") : t("account.roleUser") }}</dd>
        <dt>{{ t("account.language") }}</dt>
        <dd>{{ t(localeNames[profile.locale]) }}</dd>
        <dt>{{ t("account.createdAt") }}</dt>
        <dd>{{ formatDateTime(profile.createdAt, locale) }}</dd>
      </dl>
    </section>

    <section class="jl-account__card" aria-labelledby="sessions-title">
      <h2 id="sessions-title">{{ t("account.sessions") }}</h2>
      <p class="jl-account__muted">{{ t("account.sessionsHint") }}</p>
      <RequestStatus :state="sessions.state" @retry="sessions.load">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 3" :key="n" shape="block" class="jl-account__skeleton" />
          </div>
        </template>
        <template #default>
          <ul class="jl-sessions">
            <li v-for="session in sessions.sessions" :key="session.id" class="jl-session">
              <div class="jl-session__info">
                <p class="jl-session__device">
                  <span :id="`session-${session.id}`">{{ deviceLabel(session) }}</span>
                  <UiBadge>{{ session.clientKind === "web" ? t("account.clientWeb") : t("account.clientNative") }}</UiBadge>
                  <UiBadge v-if="session.id === auth.sessionId" tone="accent">{{ t("account.current") }}</UiBadge>
                </p>
                <p class="jl-session__meta">
                  <span>{{ t("account.signedIn", { date: formatDateTime(session.createdAt, locale) }) }}</span>
                  <span v-if="session.lastSeenAt">{{ t("account.lastSeen", { date: formatDateTime(session.lastSeenAt, locale) }) }}</span>
                  <span>{{ t("account.expires", { date: formatDateTime(session.expiresAt, locale) }) }}</span>
                  <span v-if="session.lastIp">{{ t("account.lastIp", { ip: session.lastIp }) }}</span>
                </p>
              </div>
              <UiButton
                variant="danger"
                :busy="sessions.pending.has(session.id)"
                :aria-describedby="`session-${session.id}`"
                @click="revoke(session)"
              >
                {{ session.id === auth.sessionId ? t("account.revokeCurrent") : t("account.revoke") }}
              </UiButton>
            </li>
          </ul>
          <p v-if="sessions.sessions.length === 0" class="jl-account__muted">{{ t("account.empty") }}</p>
        </template>
        <template #empty>
          <UiEmptyState :title="t('account.empty')" />
        </template>
      </RequestStatus>

      <div class="jl-account__all">
        <p class="jl-account__muted">{{ t("account.revokeAllHint") }}</p>
        <div class="jl-account__actions">
          <UiButton v-if="!confirmingAll" variant="danger" @click="confirmingAll = true">
            {{ t("account.revokeAll") }}
          </UiButton>
          <template v-else>
            <UiButton variant="danger" :busy="revokingAll" @click="revokeAll">{{ t("account.revokeAllConfirm") }}</UiButton>
            <UiButton variant="secondary" :disabled="revokingAll" @click="confirmingAll = false">
              {{ t("common.cancel") }}
            </UiButton>
          </template>
        </div>
      </div>
    </section>
  </section>
</template>

<style scoped>
.jl-account {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-account h1 {
  margin: 0;
}

.jl-account__card {
  padding: var(--jl-space-6);
  background: var(--jl-color-surface);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-account__card h2 {
  margin: 0 0 var(--jl-space-4);
  font-size: var(--jl-font-size-lg);
}

.jl-account__profile {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--jl-space-2) var(--jl-space-6);
  margin: 0;
}

.jl-account__profile dt {
  color: var(--jl-color-text-muted);
}

.jl-account__profile dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.jl-account__muted {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-account__skeleton {
  margin-bottom: var(--jl-space-3);
}

.jl-sessions {
  display: grid;
  gap: var(--jl-space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-session {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-session__info {
  min-width: 0;
}

.jl-session__device {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-session__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: var(--jl-space-1) 0 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-account__all {
  margin-top: var(--jl-space-6);
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-account__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
