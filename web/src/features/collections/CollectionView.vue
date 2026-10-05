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
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { kindLabelKey } from "@/features/items/labels";
import { useAuthStore } from "@/stores/auth";
import { useToastStore } from "@/stores/toasts";
import { deleteCollection, getCollection, removeFromCollection, updateCollection, type CollectionView } from "./api";
import "./lists.css";

// One collection with the members the caller may see (G02.1, G48.3).
// Administrators rename it, link it to an NFO collection name, remove
// manual members and delete it; members are added from an item's page.
const props = defineProps<{ collectionId: string }>();
const { t } = useI18n();
const { client } = useApi();
const auth = useAuthStore();
const router = useRouter();
const toasts = useToastStore();
const view = shallowRef<CollectionView | null>(null);
const busy = shallowRef(false);
const name = shallowRef("");
const nfoName = shallowRef("");
const nameError = shallowRef<string | null>(null);

function show(next: CollectionView) {
  view.value = next;
  name.value = next.collection.name;
  nfoName.value = next.collection.nfoName ?? "";
}

const request = useRequest(async () => {
  const next = await getCollection(client, props.collectionId);
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
  nameError.value = name.value.trim() === "" ? t("collections.form.nameRequired") : null;
  if (nameError.value !== null || view.value === null) {
    return;
  }
  const overview = view.value.collection.overview;
  void act(async () => {
    show(await updateCollection(client, props.collectionId, { name: name.value.trim(), overview, nfoName: nfoName.value.trim() === "" ? null : nfoName.value.trim() }));
    toasts.push("collections.saved", "success");
  });
}

function remove(itemId: string) {
  void act(async () => {
    show(await removeFromCollection(client, props.collectionId, itemId));
    toasts.push("collections.removed", "success");
  });
}

function destroy() {
  void act(async () => {
    await deleteCollection(client, props.collectionId);
    toasts.push("collections.deleted", "success");
    await router.push({ name: "collections" });
  });
}

watch(
  () => props.collectionId,
  () => {
    void request.run();
  },
  { immediate: true },
);
</script>

<template>
  <section aria-labelledby="collection-title">
    <nav class="jl-lists__crumbs" :aria-label="t('common.breadcrumbs')">
      <RouterLink :to="{ name: 'collections' }">{{ t("collections.title") }}</RouterLink>
    </nav>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #default>
        <template v-if="view">
          <h1 id="collection-title" tabindex="-1">{{ view.collection.name }}</h1>
          <p v-if="view.collection.overview" class="jl-lists__intro">{{ view.collection.overview }}</p>
          <p v-if="view.collection.nfoName" class="jl-lists__intro">{{ t("collections.nfoLinked", { name: view.collection.nfoName }) }}</p>
          <form v-if="auth.isAdmin" class="jl-lists__form" data-testid="collection-edit" @submit.prevent="save">
            <UiTextField v-model="name" :label="t('collections.form.name')" :error="nameError" :maxlength="1024" />
            <UiTextField v-model="nfoName" :label="t('collections.form.nfoName')" :hint="t('collections.form.nfoNameHint')" :maxlength="1024" />
            <div class="jl-lists__actions">
              <UiButton type="submit" :busy="busy">{{ t("collections.form.save") }}</UiButton>
              <UiConfirmButton :label="t('collections.delete')" :confirm-label="t('collections.deleteConfirm')" :prompt="t('collections.deletePrompt')" :busy="busy" @confirm="destroy" />
            </div>
          </form>
          <ul v-if="view.items.length > 0" class="jl-lists__rows">
            <li v-for="member in view.items" :key="member.id" class="jl-lists__row">
              <ItemPoster :item-id="member.id" :title="member.title" :width="96" />
              <RouterLink class="jl-lists__title" :to="{ name: 'item', params: { itemId: member.id } }">{{ member.title }}</RouterLink>
              <UiBadge>{{ t(kindLabelKey[member.kind]) }}</UiBadge>
              <UiBadge v-if="member.fromNfo">{{ t("collections.fromNfo") }}</UiBadge>
              <UiButton v-if="auth.isAdmin && member.manual" variant="secondary" :disabled="busy" @click="remove(member.id)">{{ t("collections.remove") }}</UiButton>
            </li>
          </ul>
          <UiEmptyState v-else :title="t('collections.noMembers')">
            <p>{{ t("collections.addHint") }}</p>
          </UiEmptyState>
          <p v-if="view.truncated" class="jl-lists__intro">{{ t("collections.truncated") }}</p>
        </template>
      </template>
    </RequestStatus>
  </section>
</template>
