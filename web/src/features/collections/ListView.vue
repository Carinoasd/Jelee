<script setup lang="ts">
import { shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { kindLabelKey } from "@/features/items/labels";
import { useToastStore } from "@/stores/toasts";
import { deletePlaylist, getPlaylist, movePlaylistEntry, moveTarget, removePlaylistEntry, updatePlaylist, type PlaylistView } from "./api";
import "./lists.css";

// One playlist with the entries the caller may see (G02.1, G48.3). Only the
// owner renames, shares, reorders and removes; others read a public one.
// This page lists items and never plays them (G27).
const props = defineProps<{ listId: string }>();
const { t } = useI18n();
const { client } = useApi();
const router = useRouter();
const toasts = useToastStore();
const view = shallowRef<PlaylistView | null>(null);
const busy = shallowRef(false);
const name = shallowRef("");
const shared = shallowRef(false);
const nameError = shallowRef<string | null>(null);

function show(next: PlaylistView) {
  view.value = next;
  name.value = next.playlist.name;
  shared.value = next.playlist.public;
}

const request = useRequest(async () => {
  const next = await getPlaylist(client, props.listId);
  show(next);
  return next;
});

async function act(action: () => Promise<void>) {
  busy.value = true;
  try {
    await action();
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  } finally {
    busy.value = false;
  }
}

function save() {
  nameError.value = name.value.trim() === "" ? t("collections.lists.form.nameRequired") : null;
  if (nameError.value !== null) {
    return;
  }
  void act(async () => {
    show(await updatePlaylist(client, props.listId, name.value.trim(), shared.value));
    toasts.push("collections.lists.saved", "success");
  });
}

function move(index: number, step: -1 | 1) {
  const entries = view.value?.entries ?? [];
  const before = moveTarget(entries, index, step);
  const entry = entries[index];
  if (before === undefined || entry === undefined) {
    return;
  }
  void act(async () => {
    show(await movePlaylistEntry(client, props.listId, entry.entryId, before));
  });
}

function remove(entryId: string) {
  void act(async () => {
    show(await removePlaylistEntry(client, props.listId, entryId));
    toasts.push("collections.lists.removed", "success");
  });
}

function destroy() {
  void act(async () => {
    await deletePlaylist(client, props.listId);
    toasts.push("collections.lists.deleted", "success");
    await router.push({ name: "lists" });
  });
}

watch(
  () => props.listId,
  () => {
    void request.run();
  },
  { immediate: true },
);
</script>

<template>
  <section aria-labelledby="playlist-title">
    <nav class="jl-lists__crumbs" :aria-label="t('common.breadcrumbs')">
      <RouterLink :to="{ name: 'lists' }">{{ t("collections.lists.title") }}</RouterLink>
    </nav>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #default>
        <template v-if="view">
          <h1 id="playlist-title" tabindex="-1">{{ view.playlist.name }}</h1>
          <p v-if="!view.playlist.owned" class="jl-lists__intro">{{ t("collections.lists.by", { name: view.playlist.ownerName }) }}</p>
          <form v-if="view.playlist.owned" class="jl-lists__form" data-testid="playlist-edit" @submit.prevent="save">
            <UiTextField v-model="name" :label="t('collections.lists.form.name')" :error="nameError" :maxlength="1024" />
            <UiCheckbox v-model="shared" :label="t('collections.lists.form.public')" :hint="t('collections.lists.form.publicHint')" />
            <div class="jl-lists__actions">
              <UiButton type="submit" :busy="busy">{{ t("collections.lists.form.save") }}</UiButton>
              <UiConfirmButton :label="t('collections.lists.delete')" :confirm-label="t('collections.lists.deleteConfirm')" :prompt="t('collections.lists.deletePrompt')" :busy="busy" @confirm="destroy" />
            </div>
          </form>
          <ol v-if="view.entries.length > 0" class="jl-lists__rows">
            <li v-for="(entry, index) in view.entries" :key="entry.entryId" class="jl-lists__row">
              <ItemPoster :item-id="entry.item.id" :title="entry.item.title" :width="96" />
              <RouterLink class="jl-lists__title" :to="{ name: 'item', params: { itemId: entry.item.id } }">{{ entry.item.title }}</RouterLink>
              <UiBadge>{{ t(kindLabelKey[entry.item.kind]) }}</UiBadge>
              <template v-if="view.playlist.owned">
                <UiButton variant="ghost" :disabled="busy || index === 0" :aria-label="t('collections.lists.moveUp', { title: entry.item.title })" @click="move(index, -1)">↑</UiButton>
                <UiButton
                  variant="ghost"
                  :disabled="busy || index === view.entries.length - 1"
                  :aria-label="t('collections.lists.moveDown', { title: entry.item.title })"
                  @click="move(index, 1)"
                >
                  ↓
                </UiButton>
                <UiButton variant="secondary" :disabled="busy" @click="remove(entry.entryId)">{{ t("collections.lists.remove") }}</UiButton>
              </template>
            </li>
          </ol>
          <UiEmptyState v-else :title="t('collections.lists.noEntries')">
            <p>{{ t("collections.lists.addHint") }}</p>
          </UiEmptyState>
        </template>
      </template>
    </RequestStatus>
  </section>
</template>
