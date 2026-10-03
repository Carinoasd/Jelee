<script setup lang="ts">
import { onMounted } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import { useLibrariesStore } from "@/stores/libraries";

const { t } = useI18n();
const store = useLibrariesStore();

onMounted(() => {
  void store.load();
});
</script>

<template>
  <section aria-labelledby="libraries-title">
    <h1 id="libraries-title">{{ t("libraries.title") }}</h1>
    <RequestStatus :state="store.state" @retry="store.load">
      <template #default>
        <ul class="jl-libraries">
          <li v-for="library in store.libraries" :key="library.id" class="jl-libraries__item">
            <h2 class="jl-libraries__name">{{ library.name }}</h2>
            <p class="jl-libraries__meta">{{ t("libraries.roots", { count: library.roots }) }}</p>
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
        <p>{{ t("libraries.empty") }}</p>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-libraries {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: var(--jl-space-4);
  padding: 0;
  list-style: none;
}

.jl-libraries__item {
  padding: var(--jl-space-4);
  background: var(--jl-color-surface);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
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
