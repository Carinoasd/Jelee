<script setup lang="ts">
import { onMounted } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import type { Session } from "@/features/account/api";
import { formatDateTime } from "@/i18n/format";
import { useUserAdminStore } from "@/stores/userAdmin";
import { useAdminFeedback } from "./feedback";

const { t, locale } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();

onMounted(() => {
  void store.loadSessions();
});

function deviceLabel(session: Session): string {
  const app = [session.client, session.version].filter((part) => part !== undefined && part !== "").join(" ");
  return app === "" ? session.deviceName : `${session.deviceName} · ${app}`;
}

async function revokeAll() {
  try {
    await feedback.done(await store.revokeAllSessions(), "users.sessions.revokedAll");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-sessions-title">
    <h2 id="user-sessions-title">{{ t("users.sessions.title") }}</h2>
    <RequestStatus :state="store.sessionsState" @retry="store.loadSessions">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton v-for="n in 2" :key="n" shape="title" class="jl-skeleton-row" />
        </div>
      </template>
      <template #default>
        <div v-if="store.sessions.length > 0" class="jl-table-wrap">
          <table class="jl-table">
            <caption class="jl-visually-hidden">{{ t("users.sessions.caption") }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t("users.sessions.device") }}</th>
                <th scope="col">{{ t("users.sessions.kind") }}</th>
                <th scope="col">{{ t("users.sessions.signedIn") }}</th>
                <th scope="col">{{ t("users.sessions.lastSeen") }}</th>
                <th scope="col">{{ t("users.sessions.lastIp") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="session in store.sessions" :key="session.id">
                <th scope="row">{{ deviceLabel(session) }}</th>
                <td>
                  <UiBadge>{{ session.clientKind === "web" ? t("users.sessions.web") : t("users.sessions.native") }}</UiBadge>
                </td>
                <td>{{ formatDateTime(session.createdAt, locale) }}</td>
                <td>{{ formatDateTime(session.lastSeenAt, locale) }}</td>
                <td>{{ session.lastIp }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="jl-card__muted">{{ t("users.sessions.empty") }}</p>
      </template>
      <template #empty>
        <UiEmptyState :title="t('users.sessions.empty')" />
      </template>
    </RequestStatus>
    <p class="jl-card__impact">{{ t("users.sessions.revokeAllImpact") }}</p>
    <div class="jl-card__actions">
      <UiConfirmButton
        :label="t('users.sessions.revokeAll')"
        :confirm-label="t('users.sessions.revokeAllConfirm')"
        :prompt="t('users.sessions.revokeAllImpact')"
        :busy="store.busy === 'sessions'"
        @confirm="revokeAll"
      />
    </div>
  </section>
</template>

<style scoped src="./sections.css"></style>
