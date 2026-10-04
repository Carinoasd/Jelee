<script setup lang="ts">
import { nextTick, onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useAdminFeedback } from "@/features/users/feedback";
import { formatDateTime } from "@/i18n/format";
import { useSharesStore } from "@/stores/shares";
import { useToastStore } from "@/stores/toasts";
import { shareLink, type Share, type ShareGrant } from "./api";
import ShareAccessLog from "./ShareAccessLog.vue";
import ShareForm from "./ShareForm.vue";
import { stateKeys } from "./labels";

// Administration of share links (G48.6). A new link's token is shown once,
// held only in this component until the notice is closed; the server keeps
// only its digest, so it can never be shown again.
const { t, locale } = useI18n();
const store = useSharesStore();
const toasts = useToastStore();
const feedback = useAdminFeedback();
const creating = shallowRef(false);
/** The one-time link of the link just created; null once dismissed. */
const fresh = shallowRef<{ share: Share; url: string } | null>(null);
/** The link whose access log is open. */
const inspecting = shallowRef<Share | null>(null);
const newButton = useTemplateRef<HTMLElement>("newButton");
const grantBox = useTemplateRef<HTMLElement>("grantBox");

onMounted(() => {
  void store.load();
});

function scopeLabel(share: Share): string {
  return share.itemId === undefined
    ? t("shares.list.libraryScope", { library: share.libraryName })
    : t("shares.list.itemScope", { title: share.itemTitle ?? share.itemId, library: share.libraryName });
}

async function focusNewButton() {
  await nextTick();
  newButton.value?.querySelector("button")?.focus();
}

async function cancelCreate() {
  creating.value = false;
  await focusNewButton();
}

function selectAll(event: FocusEvent) {
  (event.target as HTMLInputElement | null)?.select();
}

async function created(grant: ShareGrant) {
  creating.value = false;
  fresh.value = { share: grant.share, url: shareLink(grant.token) };
  toasts.push("shares.created", "success");
  await nextTick();
  grantBox.value?.querySelector<HTMLElement>("h2")?.focus();
}

async function copy() {
  if (fresh.value === null) {
    return;
  }
  try {
    await navigator.clipboard.writeText(fresh.value.url);
    toasts.push("shares.grant.copied", "success");
  } catch {
    toasts.push("shares.grant.copyFailed", "danger");
    grantBox.value?.querySelector<HTMLInputElement>("input")?.select();
  }
}

async function dismiss() {
  fresh.value = null;
  await focusNewButton();
}

async function revoke(share: Share) {
  try {
    await store.revoke(share.id);
    toasts.push("shares.list.revoked", "success");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-shares" aria-labelledby="shares-title">
    <div class="jl-shares__head">
      <h1 id="shares-title" tabindex="-1">{{ t("shares.title") }}</h1>
      <span ref="newButton">
        <UiButton v-if="!creating && fresh === null" @click="creating = true">{{ t("shares.create.open") }}</UiButton>
      </span>
    </div>
    <p class="jl-card__muted">{{ t("shares.intro") }}</p>

    <ShareForm v-if="creating" @created="created" @cancel="cancelCreate" />

    <section v-if="fresh" ref="grantBox" class="jl-card" aria-labelledby="share-grant-title" data-testid="share-grant">
      <h2 id="share-grant-title" tabindex="-1">{{ t("shares.grant.title") }}</h2>
      <UiAlert tone="info">
        <p>{{ t("shares.grant.warning") }}</p>
      </UiAlert>
      <label class="jl-shares__label" for="share-grant-url">{{ t("shares.grant.label") }}</label>
      <input id="share-grant-url" class="jl-shares__url" :value="fresh.url" readonly @focus="selectAll" />
      <p class="jl-card__muted">{{ scopeLabel(fresh.share) }}</p>
      <div class="jl-card__actions">
        <UiButton @click="copy">{{ t("shares.grant.copy") }}</UiButton>
        <UiButton variant="secondary" @click="dismiss">{{ t("shares.grant.close") }}</UiButton>
      </div>
    </section>

    <section class="jl-card" aria-labelledby="shares-list-title">
      <h2 id="shares-list-title">{{ t("shares.list.title") }}</h2>
      <RequestStatus :state="store.state" @retry="store.load">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 3" :key="n" class="jl-skeleton-row" />
          </div>
        </template>
        <template #empty>
          <UiEmptyState :title="t('shares.list.empty')">
            <p>{{ t("shares.list.emptyHint") }}</p>
          </UiEmptyState>
        </template>
        <template #default>
          <div class="jl-table-wrap">
            <table class="jl-table">
              <caption class="jl-visually-hidden">{{ t("shares.list.title") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("shares.list.scope") }}</th>
                  <th scope="col">{{ t("shares.list.state") }}</th>
                  <th scope="col">{{ t("shares.list.expires") }}</th>
                  <th scope="col">{{ t("shares.list.sessions") }}</th>
                  <th scope="col">{{ t("shares.list.lastUsed") }}</th>
                  <th scope="col">{{ t("common.actions") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="share in store.shares" :key="share.id" :data-share="share.id">
                  <th :id="`share-${share.id}`" scope="row">
                    {{ scopeLabel(share) }}
                    <br />
                    <UiBadge v-if="share.readOnly">{{ t("shares.list.readOnly") }}</UiBadge>
                    <UiBadge v-if="share.allowPlayback" tone="accent">{{ t("shares.list.native", { count: share.maxStreams }) }}</UiBadge>
                    <span v-if="share.note" class="jl-card__muted"><br />{{ share.note }}</span>
                  </th>
                  <td>
                    <UiBadge :tone="share.state === 'active' ? 'accent' : share.state === 'revoked' ? 'danger' : 'neutral'">
                      {{ t(stateKeys[share.state]) }}
                    </UiBadge>
                  </td>
                  <td>{{ formatDateTime(share.state === "revoked" ? share.revokedAt : share.expiresAt, locale) }}</td>
                  <td>{{ share.activeSessions.toLocaleString(locale) }}</td>
                  <td>{{ share.lastUsedAt ? formatDateTime(share.lastUsedAt, locale) : t("shares.list.never") }}</td>
                  <td>
                    <div class="jl-card__actions">
                      <UiButton variant="ghost" :aria-describedby="`share-${share.id}`" @click="inspecting = share">
                        {{ t("shares.list.accessLog") }}
                      </UiButton>
                      <UiConfirmButton
                        v-if="share.state === 'active'"
                        :label="t('shares.list.revoke')"
                        :confirm-label="t('shares.list.revokeConfirm')"
                        :prompt="t('shares.list.revokePrompt', { count: share.activeSessions })"
                        :busy="store.pending.has(share.id)"
                        :describedby="`share-${share.id}`"
                        @confirm="revoke(share)"
                      />
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </RequestStatus>
    </section>

    <ShareAccessLog v-if="inspecting" :key="inspecting.id" :share="inspecting" :scope="scopeLabel(inspecting)" @close="inspecting = null" />
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-shares {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-shares__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
}

.jl-shares h1 {
  margin: 0;
}

.jl-shares__label {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-shares__url {
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font-family: ui-monospace, monospace;
  width: 100%;
}
</style>
