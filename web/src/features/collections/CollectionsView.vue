<script setup lang="ts">
import { onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { useAuthStore } from "@/stores/auth";
import { useToastStore } from "@/stores/toasts";
import { createCollection, listCollections, syncNfoCollections, type Collection } from "./api";
import "./lists.css";

// Collections (G02.1): every user sees the collections with at least one
// item they may see; administrators create them, by hand or from the NFO
// collection names of the library metadata.
const { t } = useI18n();
const { client } = useApi();
const auth = useAuthStore();
const router = useRouter();
const toasts = useToastStore();
const collections = shallowRef<readonly Collection[]>([]);
const nextCursor = shallowRef("");
const busy = shallowRef(false);
const name = shallowRef("");
const nfoName = shallowRef("");
const nameError = shallowRef<string | null>(null);

const request = useRequest(
  async () => {
    const page = await listCollections(client);
    collections.value = page.items;
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
    const page = await listCollections(client, nextCursor.value);
    collections.value = [...collections.value, ...page.items];
    nextCursor.value = page.nextCursor;
  } catch (error: unknown) {
    fail(error);
  } finally {
    busy.value = false;
  }
}

async function create() {
  nameError.value = name.value.trim() === "" ? t("collections.form.nameRequired") : null;
  if (nameError.value !== null) {
    return;
  }
  busy.value = true;
  try {
    const view = await createCollection(client, name.value.trim(), nfoName.value.trim() === "" ? null : nfoName.value.trim());
    toasts.push("collections.created", "success");
    await router.push({ name: "collection", params: { collectionId: view.collection.id } });
  } catch (error: unknown) {
    fail(error);
  } finally {
    busy.value = false;
  }
}

async function sync() {
  busy.value = true;
  try {
    const created = await syncNfoCollections(client);
    toasts.push("collections.synced", "success", { count: created });
    await request.run();
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
  <section aria-labelledby="collections-title">
    <h1 id="collections-title" tabindex="-1">{{ t("collections.title") }}</h1>
    <p class="jl-lists__intro">{{ t("collections.intro") }}</p>
    <form v-if="auth.isAdmin" class="jl-lists__form" data-testid="collection-create" @submit.prevent="create">
      <UiTextField v-model="name" :label="t('collections.form.name')" :error="nameError" :maxlength="1024" />
      <UiTextField v-model="nfoName" :label="t('collections.form.nfoName')" :hint="t('collections.form.nfoNameHint')" :maxlength="1024" />
      <div class="jl-lists__actions">
        <UiButton type="submit" :busy="busy">{{ t("collections.form.create") }}</UiButton>
        <UiButton variant="secondary" :disabled="busy" @click="sync">{{ t("collections.sync") }}</UiButton>
      </div>
    </form>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #default>
        <ul class="jl-lists">
          <li v-for="collection in collections" :key="collection.id">
            <RouterLink class="jl-lists__card" :to="{ name: 'collection', params: { collectionId: collection.id } }">
              <ItemPoster v-if="collection.coverItemId" :item-id="collection.coverItemId" :title="collection.name" :width="160" />
              <span class="jl-lists__name">{{ collection.name }}</span>
              <span class="jl-lists__meta">{{ t("collections.count", { count: collection.itemCount }) }}</span>
            </RouterLink>
          </li>
        </ul>
        <UiButton v-if="nextCursor !== ''" variant="secondary" :busy="busy" @click="loadMore">{{ t("common.loadMore") }}</UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('collections.empty')" />
      </template>
    </RequestStatus>
  </section>
</template>
