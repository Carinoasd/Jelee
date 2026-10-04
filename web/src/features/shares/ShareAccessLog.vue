<script setup lang="ts">
import { onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { listShareAccess, type Share, type ShareAccessRecord } from "./api";
import { clientKindKeys, eventKeys, refusalKeys } from "./labels";

// The audited events of one link, newest first, a page at a time. Only the
// route pattern of a guest request is recorded, never its path or query.
const props = defineProps<{ share: Share; scope: string }>();
const emit = defineEmits<{ close: [] }>();
const { t, locale } = useI18n();
const { client } = useApi();
const root = useTemplateRef<HTMLElement>("root");
const records = shallowRef<readonly ShareAccessRecord[]>([]);
const nextCursor = shallowRef("");
const loadingMore = shallowRef(false);
const moreError = shallowRef<ApiError | null>(null);

const first = useRequest(
  async () => {
    const page = await listShareAccess(client, props.share.id);
    records.value = page.records;
    nextCursor.value = page.pagination.nextCursor;
    return records.value;
  },
  (data) => data.length === 0,
);

async function loadMore() {
  loadingMore.value = true;
  moreError.value = null;
  try {
    const page = await listShareAccess(client, props.share.id, nextCursor.value);
    records.value = [...records.value, ...page.records];
    nextCursor.value = page.pagination.nextCursor;
  } catch (error: unknown) {
    moreError.value = error instanceof ApiError ? error : networkError(error);
  } finally {
    loadingMore.value = false;
  }
}

function who(record: ShareAccessRecord): string {
  const parts = [record.deviceName ?? "", record.clientKind ? t(clientKindKeys[record.clientKind]) : "", record.ip ?? ""];
  return parts.filter((part) => part !== "").join(" · ");
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h2")?.focus();
  void first.run();
});
</script>

<template>
  <section ref="root" class="jl-card" aria-labelledby="share-access-title">
    <div class="jl-share-log__head">
      <h2 id="share-access-title" tabindex="-1">{{ t("shares.access.title", { scope }) }}</h2>
      <UiButton variant="secondary" @click="emit('close')">{{ t("shares.access.close") }}</UiButton>
    </div>
    <RequestStatus :state="first.state.value" @retry="first.run">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton v-for="n in 3" :key="n" class="jl-skeleton-row" />
        </div>
      </template>
      <template #empty>
        <UiEmptyState :title="t('shares.access.empty')" />
      </template>
      <template #default>
        <div class="jl-table-wrap">
          <table class="jl-table">
            <caption class="jl-visually-hidden">{{ t("shares.access.caption") }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t("shares.access.time") }}</th>
                <th scope="col">{{ t("shares.access.eventColumn") }}</th>
                <th scope="col">{{ t("shares.access.who") }}</th>
                <th scope="col">{{ t("shares.access.route") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="record in records" :key="record.id">
                <td>{{ formatDateTime(record.occurredAt, locale) }}</td>
                <th scope="row">
                  {{ t(eventKeys[record.event]) }}
                  <span v-if="record.reason" class="jl-card__muted"><br />{{ t(refusalKeys[record.reason]) }}</span>
                </th>
                <td>{{ who(record) }}</td>
                <td class="jl-share-log__route">{{ record.route ?? "" }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <UiAlert v-if="moreError" tone="danger">
          <p>{{ t(errorMessageKey(moreError)) }}</p>
        </UiAlert>
        <div v-if="nextCursor !== ''" class="jl-card__actions">
          <UiButton variant="secondary" :busy="loadingMore" @click="loadMore">{{ t("common.loadMore") }}</UiButton>
        </div>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-share-log__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
}

.jl-share-log__head h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-share-log__route {
  font-family: ui-monospace, monospace;
}
</style>
