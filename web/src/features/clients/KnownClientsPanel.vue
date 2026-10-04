<script setup lang="ts">
import { nextTick, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useKnownClientsStore } from "@/stores/clientsKnown";
import { useToastStore } from "@/stores/toasts";
import type { KnownClient } from "./api";
import { asApiError, clientKindKeys } from "./labels";

const { t, locale } = useI18n();
const store = useKnownClientsStore();
const toasts = useToastStore();
const renaming = shallowRef<string | null>(null);
const aliasDraft = shallowRef("");

onMounted(() => {
  void store.load();
});

function clientName(client: KnownClient): string {
  return client.alias || client.appName || client.deviceName || t("clients.known.unnamed");
}

function device(client: KnownClient): string {
  return [client.deviceName, client.deviceId].filter((part) => part !== undefined && part !== "").join(" · ");
}

function version(client: KnownClient): string {
  return [client.appName, client.appVersion].filter((part) => part !== undefined && part !== "").join(" ");
}

async function startRename(client: KnownClient) {
  renaming.value = client.id;
  aliasDraft.value = client.alias ?? "";
  await nextTick();
  document.querySelector<HTMLInputElement>(`#known-${client.id} input`)?.focus();
}

async function run(work: () => Promise<void>) {
  try {
    await work();
  } catch (error: unknown) {
    toasts.push(errorMessageKey(asApiError(error)), "danger");
  }
}

function rename(client: KnownClient) {
  return run(async () => {
    await store.update(client.id, { alias: aliasDraft.value.trim() });
    renaming.value = null;
    toasts.push("clients.known.renamed", "success");
  });
}

function setTrusted(client: KnownClient, trusted: boolean) {
  return run(async () => {
    await store.update(client.id, { trusted });
    toasts.push(trusted ? "clients.known.trustedDone" : "clients.known.untrustedDone", "success", { name: clientName(client) });
  });
}

function block(client: KnownClient) {
  return run(async () => {
    await store.block(client.id);
    toasts.push("clients.known.blocked", "success", { name: clientName(client) });
  });
}

function kick(client: KnownClient) {
  return run(async () => {
    const count = await store.kick(client.id);
    toasts.push("clients.known.kicked", "success", { count, name: clientName(client) });
  });
}
</script>

<template>
  <section class="jl-cc-card" aria-labelledby="clients-known-title">
    <h2 id="clients-known-title">{{ t("clients.known.title") }}</h2>
    <p class="jl-cc-muted">{{ t("clients.known.hint") }}</p>
    <RequestStatus :state="store.state" @retry="store.load">
      <template #loading>
        <UiSkeleton v-for="n in 3" :key="n" shape="block" />
      </template>
      <template #empty>
        <UiEmptyState :title="t('clients.known.empty')" />
      </template>
      <template #default>
        <ul class="jl-known">
          <li v-for="client in store.clients" :id="`known-${client.id}`" :key="client.id" class="jl-known__item">
            <p class="jl-known__name">
              <span :id="`known-name-${client.id}`">{{ clientName(client) }}</span>
              <UiBadge v-if="client.trusted" tone="accent">{{ t("clients.known.trustedBadge") }}</UiBadge>
              <UiBadge v-if="client.clientKind">{{ t(clientKindKeys[client.clientKind]) }}</UiBadge>
            </p>
            <dl class="jl-known__facts">
              <dt>{{ t("clients.known.version") }}</dt>
              <dd>{{ version(client) || "—" }}</dd>
              <dt>{{ t("clients.known.device") }}</dt>
              <dd>{{ device(client) || "—" }}</dd>
              <dt>{{ t("clients.known.userAgent") }}</dt>
              <dd class="jl-cc-code">{{ client.userAgent || "—" }}</dd>
              <dt>{{ t("clients.known.lastSeen") }}</dt>
              <dd>{{ formatDateTime(client.lastSeenAt, locale) }}</dd>
              <dt>{{ t("clients.known.lastIp") }}</dt>
              <dd class="jl-cc-code">{{ client.lastIp || "—" }}</dd>
              <dt>{{ t("clients.known.sessions") }}</dt>
              <dd>{{ client.activeSessions }}</dd>
            </dl>

            <form v-if="renaming === client.id" class="jl-known__rename" @submit.prevent="rename(client)">
              <UiTextField v-model="aliasDraft" :label="t('clients.known.alias')" :hint="t('clients.known.aliasHint')" :maxlength="128" />
              <div class="jl-cc-actions">
                <UiButton type="submit" :busy="store.pending.has(client.id)">{{ t("common.save") }}</UiButton>
                <UiButton variant="secondary" @click="renaming = null">{{ t("common.cancel") }}</UiButton>
              </div>
            </form>
            <div v-else class="jl-cc-actions">
              <UiButton variant="ghost" :aria-describedby="`known-name-${client.id}`" @click="startRename(client)">
                {{ t("clients.known.rename") }}
              </UiButton>
              <UiButton
                variant="secondary"
                :busy="store.pending.has(client.id)"
                :aria-describedby="`known-name-${client.id}`"
                @click="setTrusted(client, !client.trusted)"
              >
                {{ client.trusted ? t("clients.known.untrust") : t("clients.known.trust") }}
              </UiButton>
              <UiConfirmButton
                :label="t('clients.known.kick')"
                :confirm-label="t('clients.known.kickConfirm')"
                :prompt="t('clients.known.kickPrompt', { name: clientName(client) })"
                :busy="store.pending.has(client.id)"
                @confirm="kick(client)"
              />
              <UiConfirmButton
                :label="t('clients.known.block')"
                :confirm-label="t('clients.known.blockConfirm')"
                :prompt="t('clients.known.blockPrompt', { name: clientName(client) })"
                :busy="store.pending.has(client.id)"
                @confirm="block(client)"
              />
            </div>
          </li>
        </ul>
        <UiErrorState v-if="store.moreState.status === 'error'" :error="store.moreState.error" @retry="store.loadMore" />
        <UiButton v-if="store.nextCursor !== ''" variant="secondary" :busy="store.moreState.status === 'loading'" @click="store.loadMore">
          {{ t("common.loadMore") }}
        </UiButton>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-known {
  display: grid;
  gap: var(--jl-space-3);
  margin: 0 0 var(--jl-space-4);
  padding: 0;
  list-style: none;
}

.jl-known__item {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-known__name {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-known__facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: 0;
  font-size: var(--jl-font-size-sm);
}

.jl-known__facts dt {
  color: var(--jl-color-text-muted);
}

.jl-known__facts dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}

.jl-known__rename {
  display: grid;
  gap: var(--jl-space-2);
  max-width: 28rem;
}
</style>
