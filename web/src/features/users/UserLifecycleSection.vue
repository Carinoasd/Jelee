<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import { formatDateTime } from "@/i18n/format";
import { useUserAdminStore } from "@/stores/userAdmin";
import type { User } from "./api";
import { useAdminFeedback } from "./feedback";

const props = defineProps<{ user: User }>();
const { t, locale } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();
const deleted = computed(() => props.user.deletedAt !== undefined);

async function unlock() {
  try {
    await store.unlock();
    await feedback.done("done", "users.lifecycle.unlocked", { name: props.user.name });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

async function remove() {
  try {
    await feedback.done(await store.remove(), "users.lifecycle.deleted", { name: props.user.name });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

async function restore() {
  try {
    await store.restore();
    await feedback.done("done", "users.lifecycle.restored", { name: props.user.name });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-lifecycle-title">
    <h2 id="user-lifecycle-title">{{ t("users.lifecycle.title") }}</h2>
    <template v-if="deleted">
      <p>{{ t("users.lifecycle.deletedAt", { date: formatDateTime(user.deletedAt, locale) }) }}</p>
      <p class="jl-card__impact">{{ t("users.lifecycle.restoreImpact") }}</p>
      <div class="jl-card__actions">
        <UiButton :busy="store.busy === 'restore'" @click="restore">{{ t("users.lifecycle.restore") }}</UiButton>
      </div>
    </template>
    <template v-else>
      <div class="jl-lifecycle__row">
        <h3>{{ t("users.lifecycle.unlockTitle") }}</h3>
        <p class="jl-card__impact">{{ t("users.lifecycle.unlockImpact") }}</p>
        <div class="jl-card__actions">
          <UiButton variant="secondary" :busy="store.busy === 'unlock'" @click="unlock">{{ t("users.lifecycle.unlock") }}</UiButton>
        </div>
      </div>
      <div class="jl-lifecycle__row">
        <h3>{{ t("users.lifecycle.deleteTitle") }}</h3>
        <p class="jl-card__impact">{{ t("users.lifecycle.deleteImpact") }}</p>
        <div class="jl-card__actions">
          <UiConfirmButton
            :label="t('users.lifecycle.delete')"
            :confirm-label="t('users.lifecycle.deleteConfirm')"
            :prompt="t('users.lifecycle.deletePrompt', { name: user.name })"
            :busy="store.busy === 'delete'"
            @confirm="remove"
          />
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-lifecycle__row {
  display: grid;
  gap: var(--jl-space-2);
}

.jl-lifecycle__row + .jl-lifecycle__row {
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-lifecycle__row h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}
</style>
