<script setup lang="ts">
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import { useUserAdminStore } from "@/stores/userAdmin";
import type { User } from "./api";
import { useAdminFeedback } from "./feedback";

defineProps<{ user: User }>();
const { t } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();

async function set(allow: boolean) {
  try {
    await store.setNative(allow);
    await feedback.done("done", allow ? "users.native.allowedDone" : "users.native.revokedDone");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-native-title">
    <h2 id="user-native-title">{{ t("users.native.title") }}</h2>
    <p>{{ user.allowNative ? t("users.native.isAllowed") : t("users.native.isDenied") }}</p>
    <p class="jl-card__impact">{{ user.allowNative ? t("users.native.revokeImpact") : t("users.native.allowImpact") }}</p>
    <div class="jl-card__actions">
      <UiConfirmButton
        v-if="user.allowNative"
        :label="t('users.native.revoke')"
        :confirm-label="t('users.native.revokeConfirm')"
        :prompt="t('users.native.revokeImpact')"
        :busy="store.busy === 'native'"
        @confirm="set(false)"
      />
      <UiButton v-else variant="secondary" :busy="store.busy === 'native'" @click="set(true)">
        {{ t("users.native.allow") }}
      </UiButton>
    </div>
  </section>
</template>

<style scoped src="./sections.css"></style>
