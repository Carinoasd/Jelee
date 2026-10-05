<script setup lang="ts">
import { onMounted } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useLibrariesStore } from "@/stores/libraries";

const { t } = useI18n();
const store = useLibrariesStore();
const skeletons = 6;

onMounted(() => {
  void store.load();
});
</script>

<template>
  <section aria-labelledby="libraries-title">
    <h1 id="libraries-title" tabindex="-1">{{ t("libraries.title") }}</h1>
    <RequestStatus :state="store.state" @retry="store.load">
      <template #loading>
        <ul class="jl-libraries" aria-hidden="true">
          <li v-for="n in skeletons" :key="n" class="jl-libraries__item jl-libraries__item--skeleton">
            <UiSkeleton shape="title" width="60%" />
            <UiSkeleton width="40%" />
          </li>
        </ul>
      </template>
      <template #default>
        <ul class="jl-libraries">
          <li v-for="library in store.libraries" :key="library.id">
            <RouterLink class="jl-libraries__item" :to="{ name: 'library', params: { libraryId: library.id } }">
              <h2 class="jl-libraries__name">{{ library.name }}</h2>
              <p class="jl-libraries__meta">{{ t("libraries.roots", { count: library.roots }) }}</p>
            </RouterLink>
          </li>
        </ul>
        <RequestStatus v-if="store.moreState.status !== 'idle'" :state="store.moreState" @retry="store.loadMore">
          <template #default />
        </RequestStatus>
        <UiButton
          v-if="store.nextCursor !== '' && store.moreState.status !== 'loading'"
          variant="secondary"
          @click="store.loadMore"
        >
          {{ t("common.loadMore") }}
        </UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('libraries.empty')">
          <p>{{ t("libraries.emptyHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-libraries {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: var(--jl-space-4);
  margin: 0 0 var(--jl-space-4);
  padding: 0;
  list-style: none;
}

.jl-libraries__item {
  display: block;
  height: 100%;
  padding: var(--jl-space-4);
  background: var(--jl-color-surface);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  color: inherit;
  text-decoration: none;
  transition:
    border-color var(--jl-motion-duration) var(--jl-motion-easing),
    box-shadow var(--jl-motion-duration) var(--jl-motion-easing);
}

a.jl-libraries__item:hover {
  border-color: var(--jl-color-primary);
  box-shadow: var(--jl-shadow-1);
}

.jl-libraries__name {
  margin: 0 0 var(--jl-space-1);
  font-size: var(--jl-font-size-lg);
  overflow-wrap: anywhere;
}

.jl-libraries__meta {
  margin: 0;
  color: var(--jl-color-text-muted);
}
</style>
