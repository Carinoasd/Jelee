<script setup lang="ts">
import { computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useAuthStore } from "@/stores/auth";
import { useUserAdminStore } from "@/stores/userAdmin";
import { useUserAdminContentStore } from "@/stores/userAdminContent";
import { userStatusKey, userStatusOf } from "./labels";
import UserContentSection from "./UserContentSection.vue";
import UserDeliverySection from "./UserDeliverySection.vue";
import UserLibrariesSection from "./UserLibrariesSection.vue";
import UserLifecycleSection from "./UserLifecycleSection.vue";
import UserNativeSection from "./UserNativeSection.vue";
import UserRulesSection from "./UserRulesSection.vue";
import UserSessionsSection from "./UserSessionsSection.vue";
import UserSettingsSection from "./UserSettingsSection.vue";
import UserTwoFactorSection from "./UserTwoFactorSection.vue";

const props = defineProps<{ userId: string }>();
const { t } = useI18n();
const auth = useAuthStore();
const store = useUserAdminStore();
const content = useUserAdminContentStore();

watch(
  () => props.userId,
  (id) => {
    void store.open(id);
    content.reset();
  },
  { immediate: true },
);

const user = computed(() => store.user);
const deleted = computed(() => user.value?.deletedAt !== undefined);
const heading = computed(() => {
  const current = user.value;
  if (current === null) {
    return t("users.detail.title");
  }
  return current.displayName === "" ? current.name : current.displayName;
});

// Content access is only meaningful for an account that still exists.
watch(
  () => (user.value !== null && !deleted.value ? user.value.id : null),
  (id) => {
    if (id !== null && content.accessState.status === "idle") {
      void content.open(id);
    }
  },
  { immediate: true },
);
</script>

<template>
  <section class="jl-user" aria-labelledby="user-title">
    <RouterLink :to="{ name: 'admin-users' }" class="jl-user__back">{{ t("users.detail.back") }}</RouterLink>
    <h1 id="user-title" tabindex="-1">{{ heading }}</h1>
    <RequestStatus :state="store.userState" @retry="store.reload">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton shape="title" width="40%" />
          <UiSkeleton v-for="n in 3" :key="n" shape="block" class="jl-skeleton-row" />
        </div>
      </template>
      <template #default>
        <div v-if="user" class="jl-user__body">
          <p class="jl-user__summary">
            <span>{{ user.name }}</span>
            <UiBadge :tone="user.admin ? 'accent' : 'neutral'">{{ user.admin ? t("users.role.admin") : t("users.role.user") }}</UiBadge>
            <UiBadge>{{ t(userStatusKey[userStatusOf(user)]) }}</UiBadge>
            <UiBadge v-if="user.hidden">{{ t("users.role.hidden") }}</UiBadge>
          </p>
          <UiAlert v-if="user.id === auth.user?.id">
            <p>{{ t("users.detail.selfNotice") }}</p>
          </UiAlert>
          <UiAlert v-if="deleted">
            <p>{{ t("users.detail.deletedNotice") }}</p>
          </UiAlert>
          <template v-if="!deleted">
            <UserSettingsSection :user="user" />
            <UserNativeSection :user="user" />
            <UserLibrariesSection :user="user" />
            <section class="jl-user__content" :aria-label="t('users.content.group')">
              <RequestStatus :state="content.accessState" @retry="content.reload">
                <template #loading>
                  <UiSkeleton shape="block" />
                </template>
                <template #default>
                  <template v-if="content.access">
                    <UserContentSection :access="content.access" />
                    <UserRulesSection :access="content.access" />
                  </template>
                </template>
              </RequestStatus>
            </section>
            <UserDeliverySection />
            <UserSessionsSection />
            <UserTwoFactorSection :key="user.id" :user="user" />
          </template>
          <UserLifecycleSection :user="user" />
        </div>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-user {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-user h1 {
  margin: 0;
  overflow-wrap: anywhere;
}

.jl-user__back {
  justify-self: start;
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
}

.jl-user__body,
.jl-user__content > :deep(.jl-request) {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-user__summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
  color: var(--jl-color-text-muted);
}
</style>
