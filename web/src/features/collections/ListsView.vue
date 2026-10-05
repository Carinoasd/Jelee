<script setup lang="ts">
import { onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { useToastStore } from "@/stores/toasts";
import { createPlaylist, listPlaylists, type Playlist } from "./api";
import "./lists.css";

// Playlists (G02.1): the user's own and other users' public playlists, each
// counted with only the items the user may see. Items are added from an
// item's page.
const { t } = useI18n();
const { client } = useApi();
const router = useRouter();
const toasts = useToastStore();
const playlists = shallowRef<readonly Playlist[]>([]);
const nextCursor = shallowRef("");
const busy = shallowRef(false);
const name = shallowRef("");
const shared = shallowRef(false);
const nameError = shallowRef<string | null>(null);

const request = useRequest(
  async () => {
    const page = await listPlaylists(client);
    playlists.value = page.items;
    nextCursor.value = page.nextCursor;
    return page.items;
  },
  (items) => items.length === 0,
);

function fail(error: unknown) {
  toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
}

async function loadMore() {
  busy.value = true;
  try {
    const page = await listPlaylists(client, nextCursor.value);
    playlists.value = [...playlists.value, ...page.items];
    nextCursor.value = page.nextCursor;
  } catch (error: unknown) {
    fail(error);
  } finally {
    busy.value = false;
  }
}

async function create() {
  nameError.value = name.value.trim() === "" ? t("collections.lists.form.nameRequired") : null;
  if (nameError.value !== null) {
    return;
  }
  busy.value = true;
  try {
    const view = await createPlaylist(client, name.value.trim(), shared.value);
    toasts.push("collections.lists.created", "success");
    await router.push({ name: "list", params: { listId: view.playlist.id } });
  } catch (error: unknown) {
    fail(error);
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  void request.run();
});
</script>

<template>
  <section aria-labelledby="playlists-title">
    <h1 id="playlists-title" tabindex="-1">{{ t("collections.lists.title") }}</h1>
    <p class="jl-lists__intro">{{ t("collections.lists.intro") }}</p>
    <form class="jl-lists__form" data-testid="playlist-create" @submit.prevent="create">
      <UiTextField v-model="name" :label="t('collections.lists.form.name')" :error="nameError" :maxlength="1024" />
      <UiCheckbox v-model="shared" :label="t('collections.lists.form.public')" :hint="t('collections.lists.form.publicHint')" />
      <div class="jl-lists__actions">
        <UiButton type="submit" :busy="busy">{{ t("collections.lists.form.create") }}</UiButton>
      </div>
    </form>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #default>
        <ul class="jl-lists">
          <li v-for="playlist in playlists" :key="playlist.id">
            <RouterLink class="jl-lists__card" :to="{ name: 'list', params: { listId: playlist.id } }">
              <ItemPoster v-if="playlist.coverItemId" :item-id="playlist.coverItemId" :title="playlist.name" :width="160" />
              <span class="jl-lists__name">{{ playlist.name }}</span>
              <span class="jl-lists__meta">{{ t("collections.lists.count", { count: playlist.itemCount }) }}</span>
              <span v-if="!playlist.owned" class="jl-lists__meta">{{ t("collections.lists.by", { name: playlist.ownerName }) }}</span>
              <UiBadge v-else-if="playlist.public">{{ t("collections.lists.public") }}</UiBadge>
            </RouterLink>
          </li>
        </ul>
        <UiButton v-if="nextCursor !== ''" variant="secondary" :busy="busy" @click="loadMore">{{ t("common.loadMore") }}</UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('collections.lists.empty')">
          <p>{{ t("collections.lists.addHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>
