<script setup lang="ts">
import { computed, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import UiButton from "@/components/ui/UiButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import type { SelectOption } from "@/components/ui/types";
import { errorMessageKey } from "@/features/errors/messages";
import { useAuthStore } from "@/stores/auth";
import { useToastStore } from "@/stores/toasts";
import { addToCollection, addToPlaylist, collectionKinds, listCollections, listPlaylists, playlistKinds, type Collection, type Playlist } from "./api";

// Adds the shown item to one of the caller's playlists or, for
// administrators, to a collection (G02.1). Nothing is requested until the
// panel is opened.
const props = defineProps<{ itemId: string; kind: string }>();
const { t } = useI18n();
const { client } = useApi();
const auth = useAuthStore();
const toasts = useToastStore();
const opened = shallowRef(false);
const busy = shallowRef(false);
const playlists = shallowRef<readonly Playlist[]>([]);
const collections = shallowRef<readonly Collection[]>([]);
const playlistId = shallowRef("");
const collectionId = shallowRef("");

const forPlaylist = computed(() => playlistKinds.includes(props.kind));
const forCollection = computed(() => auth.isAdmin && collectionKinds.includes(props.kind));
const playlistOptions = computed<SelectOption[]>(() => [
  { value: "", label: t("collections.lists.choose") },
  ...playlists.value.filter((p) => p.owned).map((p) => ({ value: p.id, label: p.name })),
]);
const collectionOptions = computed<SelectOption[]>(() => [{ value: "", label: t("collections.choose") }, ...collections.value.map((c) => ({ value: c.id, label: c.name }))]);

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

function open() {
  opened.value = true;
  void act(async () => {
    const [mine, all] = await Promise.all([
      forPlaylist.value ? listPlaylists(client) : Promise.resolve({ items: [] }),
      forCollection.value ? listCollections(client) : Promise.resolve({ items: [] }),
    ]);
    playlists.value = mine.items;
    collections.value = all.items;
  });
}

function addPlaylist() {
  if (playlistId.value === "") {
    return;
  }
  void act(async () => {
    await addToPlaylist(client, playlistId.value, [props.itemId]);
    toasts.push("collections.lists.added", "success");
  });
}

function addCollection() {
  if (collectionId.value === "") {
    return;
  }
  void act(async () => {
    await addToCollection(client, collectionId.value, [props.itemId]);
    toasts.push("collections.added", "success");
  });
}
</script>

<template>
  <section v-if="forPlaylist || forCollection" class="jl-add-lists" data-testid="add-to-lists">
    <UiButton v-if="!opened" variant="secondary" @click="open">{{ t("collections.lists.addTo") }}</UiButton>
    <template v-else>
      <form v-if="forPlaylist" class="jl-add-lists__row" @submit.prevent="addPlaylist">
        <UiSelectField v-model="playlistId" :label="t('collections.lists.title')" :options="playlistOptions" :disabled="busy" />
        <UiButton type="submit" :busy="busy" :disabled="playlistId === ''">{{ t("collections.lists.add") }}</UiButton>
      </form>
      <form v-if="forCollection" class="jl-add-lists__row" @submit.prevent="addCollection">
        <UiSelectField v-model="collectionId" :label="t('collections.title')" :options="collectionOptions" :disabled="busy" />
        <UiButton type="submit" :busy="busy" :disabled="collectionId === ''">{{ t("collections.add") }}</UiButton>
      </form>
    </template>
  </section>
</template>

<style scoped>
.jl-add-lists {
  display: grid;
  gap: var(--jl-space-3);
  margin-block: var(--jl-space-4);
}

.jl-add-lists__row {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: var(--jl-space-2);
}
</style>
