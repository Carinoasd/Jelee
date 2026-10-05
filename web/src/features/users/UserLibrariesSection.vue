<script setup lang="ts">
import { computed, onMounted, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useUserAdminStore } from "@/stores/userAdmin";
import type { User } from "./api";
import { useAdminFeedback } from "./feedback";

defineProps<{ user: User }>();
const { t } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();
const selected = shallowRef<ReadonlySet<string>>(new Set());

watch(
  () => store.grants,
  (grants) => {
    selected.value = new Set(grants);
  },
  { immediate: true },
);

const changed = computed(() => {
  const saved = store.grants;
  return saved.size !== selected.value.size || [...saved].some((id) => !selected.value.has(id));
});

onMounted(() => {
  void store.loadLibraries();
});

function toggle(id: string, on: boolean) {
  const next = new Set(selected.value);
  if (on) {
    next.add(id);
  } else {
    next.delete(id);
  }
  selected.value = next;
}

async function save() {
  try {
    await store.saveGrants([...selected.value]);
    await feedback.done("done", "users.libraries.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-libraries-title">
    <h2 id="user-libraries-title">{{ t("users.libraries.title") }}</h2>
    <div class="jl-card__impact">
      <p>{{ t("users.libraries.impact") }}</p>
      <p>{{ t("users.libraries.emptyMeans") }}</p>
    </div>
    <UiAlert v-if="user.admin">
      <p>{{ t("users.libraries.adminNote") }}</p>
    </UiAlert>
    <RequestStatus :state="store.librariesState" @retry="store.loadLibraries">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton v-for="n in 3" :key="n" class="jl-skeleton-row" />
        </div>
      </template>
      <template #default>
        <form class="jl-grants" novalidate @submit.prevent="save">
          <fieldset class="jl-grants__set">
            <legend class="jl-grants__legend">{{ t("users.libraries.legend") }}</legend>
            <UiCheckbox
              v-for="library in store.libraries"
              :key="library.id"
              :model-value="selected.has(library.id)"
              :label="library.name"
              @update:model-value="toggle(library.id, $event)"
            />
          </fieldset>
          <p v-if="selected.size === 0 && !user.admin" class="jl-card__muted" role="status">{{ t("users.libraries.noneSelected") }}</p>
          <div class="jl-card__actions">
            <UiButton type="submit" :disabled="!changed" :busy="store.busy === 'libraries'">{{ t("common.save") }}</UiButton>
          </div>
        </form>
      </template>
      <template #empty>
        <UiEmptyState :title="t('users.libraries.noLibraries')" />
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-grants {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-grants__set {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding: 0;
  border: 0;
}

.jl-grants__legend {
  margin-bottom: var(--jl-space-2);
  font-weight: 600;
}
</style>
