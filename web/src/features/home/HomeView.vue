<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiReorderList from "@/components/ui/UiReorderList.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { useAuthStore } from "@/stores/auth";
import { useLayoutStore, type HomeBlockId } from "@/stores/layout";
import { useLibrariesStore } from "@/stores/libraries";
import { listLatest } from "./api";

// Home page (G33.5): blocks in the order and visibility the user chose. The
// "customize" toggle shows a reorder list right here; the settings page
// offers the same plus item page panels and presets.
const { t } = useI18n();
const { client } = useApi();
const auth = useAuthStore();
const layout = useLayoutStore();
const libraries = useLibrariesStore();
const latest = useRequest(() => listLatest(client), (items) => items.length === 0);
const editing = shallowRef(false);

const blockKey: Readonly<Record<HomeBlockId, string>> = {
  welcome: "layout.blocks.welcome",
  libraries: "layout.blocks.libraries",
  latest: "layout.blocks.latest",
};
const visible = computed(() => layout.visibleIds("home"));
const editorItems = computed(() => layout.layout.home.map((entry) => ({ id: entry.id, label: t(blockKey[entry.id]), visible: entry.visible })));

onMounted(() => {
  // Load only what visible blocks need.
  if (visible.value.includes("libraries")) {
    void libraries.ensureLoaded();
  }
  if (visible.value.includes("latest")) {
    void latest.run();
  }
});

function toggle(id: string, value: boolean) {
  layout.setVisible("home", id, value);
  if (value && id === "latest" && latest.state.value.status === "idle") {
    void latest.run();
  }
  if (value && id === "libraries") {
    void libraries.ensureLoaded();
  }
}
</script>

<template>
  <section class="jl-home" aria-labelledby="home-title">
    <div class="jl-home__head">
      <h1 id="home-title" tabindex="-1">{{ t("layout.home.title") }}</h1>
      <UiButton variant="secondary" :pressed="editing" @click="editing = !editing">{{ t("layout.home.customize") }}</UiButton>
    </div>

    <div v-if="editing" class="jl-home__editor">
      <UiReorderList
        :items="editorItems"
        :label="t('layout.home.blocks')"
        toggleable
        @move="(from: number, to: number) => layout.move('home', from, to)"
        @toggle="toggle"
      />
      <p class="jl-home__muted">{{ t("layout.home.moreInSettings") }}</p>
    </div>

    <p v-if="visible.length === 0" class="jl-home__muted">{{ t("layout.home.allHidden") }}</p>

    <template v-for="block in visible" :key="block">
      <section v-if="block === 'welcome'" class="jl-home__block" aria-labelledby="home-welcome">
        <h2 id="home-welcome">{{ t("layout.home.welcome", { name: auth.user?.displayName || auth.user?.name || "" }) }}</h2>
        <ul class="jl-home__links">
          <li><RouterLink :to="{ name: 'search' }">{{ t("layout.home.searchLink") }}</RouterLink></li>
          <li><RouterLink :to="{ name: 'stats' }">{{ t("common.stats") }}</RouterLink></li>
          <li><RouterLink :to="{ name: 'settings' }">{{ t("common.settings") }}</RouterLink></li>
        </ul>
      </section>

      <section v-else-if="block === 'libraries'" class="jl-home__block" aria-labelledby="home-libraries">
        <h2 id="home-libraries">{{ t("libraries.title") }}</h2>
        <RequestStatus :state="libraries.state" @retry="libraries.load">
          <template #loading>
            <UiSkeleton width="40%" />
          </template>
          <template #default>
            <ul class="jl-home__libraries">
              <li v-for="library in libraries.libraries" :key="library.id">
                <RouterLink :to="{ name: 'library', params: { libraryId: library.id } }">{{ library.name }}</RouterLink>
              </li>
            </ul>
          </template>
          <template #empty>
            <UiEmptyState :title="t('libraries.empty')" />
          </template>
        </RequestStatus>
      </section>

      <section v-else-if="block === 'latest'" class="jl-home__block" aria-labelledby="home-latest">
        <h2 id="home-latest">{{ t("layout.blocks.latest") }}</h2>
        <RequestStatus :state="latest.state.value" @retry="latest.run">
          <template #loading>
            <UiSkeleton shape="block" />
          </template>
          <template #default="{ data }">
            <ul class="jl-home__posters">
              <li v-for="item in data" :key="item.id">
                <RouterLink class="jl-home__poster" :to="{ name: 'item', params: { itemId: item.id } }">
                  <ItemPoster :item-id="item.id" :title="item.title" :width="200" />
                  <span>{{ item.title }}</span>
                </RouterLink>
              </li>
            </ul>
          </template>
          <template #empty>
            <UiEmptyState :title="t('layout.home.latestEmpty')" />
          </template>
        </RequestStatus>
      </section>
    </template>
  </section>
</template>

<style scoped>
.jl-home {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-home__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-4);
}

.jl-home__head h1 {
  margin: 0;
}

.jl-home__editor {
  max-width: 480px;
}

.jl-home__block h2 {
  margin: 0 0 var(--jl-space-3);
  font-size: var(--jl-font-size-lg);
}

.jl-home__muted {
  color: var(--jl-color-text-muted);
}

.jl-home__links,
.jl-home__libraries {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2) var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-home__links a,
.jl-home__libraries a {
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
}

.jl-home__posters {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(var(--jl-poster-min-width), 1fr));
  gap: var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-home__poster {
  display: grid;
  gap: var(--jl-space-2);
  color: inherit;
  text-decoration: none;
  overflow-wrap: anywhere;
}
</style>
