<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { formatDateTime } from "@/i18n/format";
import { useUsersStore } from "@/stores/users";
import CreateUserForm from "./CreateUserForm.vue";
import { userStatusKey, userStatusOf } from "./labels";

const { t, locale } = useI18n();
const store = useUsersStore();
const skeletonRows = 4;

const includeDeleted = computed({
  get: () => store.includeDeleted,
  set: (value: boolean) => {
    void store.setIncludeDeleted(value);
  },
});

onMounted(() => {
  void store.load();
});
</script>

<template>
  <section class="jl-users" aria-labelledby="users-title">
    <h1 id="users-title" tabindex="-1">{{ t("users.title") }}</h1>
    <p class="jl-card__muted">{{ t("users.intro") }}</p>

    <section class="jl-card" aria-labelledby="users-list-title">
      <h2 id="users-list-title">{{ t("users.list.title") }}</h2>
      <UiCheckbox v-model="includeDeleted" :label="t('users.list.includeDeleted')" :hint="t('users.list.includeDeletedHint')" />
      <RequestStatus :state="store.state" @retry="store.load">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in skeletonRows" :key="n" shape="title" class="jl-skeleton-row" />
          </div>
        </template>
        <template #default>
          <div class="jl-table-wrap">
            <table class="jl-table">
              <caption class="jl-visually-hidden">{{ t("users.list.caption") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("users.form.name") }}</th>
                  <th scope="col">{{ t("users.form.profileName") }}</th>
                  <th scope="col">{{ t("users.list.role") }}</th>
                  <th scope="col">{{ t("users.list.status") }}</th>
                  <th scope="col">{{ t("users.list.native") }}</th>
                  <th scope="col">{{ t("users.list.createdAt") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="user in store.users" :key="user.id">
                  <th scope="row">
                    <RouterLink :to="{ name: 'admin-user', params: { userId: user.id } }">{{ user.name }}</RouterLink>
                  </th>
                  <td>{{ user.displayName }}</td>
                  <td>
                    <span class="jl-users__badges">
                      <UiBadge :tone="user.admin ? 'accent' : 'neutral'">
                        {{ user.admin ? t("users.role.admin") : t("users.role.user") }}
                      </UiBadge>
                      <UiBadge v-if="user.hidden">{{ t("users.role.hidden") }}</UiBadge>
                    </span>
                  </td>
                  <td>{{ t(userStatusKey[userStatusOf(user)]) }}</td>
                  <td>{{ user.allowNative ? t("users.native.allowedShort") : t("users.native.deniedShort") }}</td>
                  <td>{{ formatDateTime(user.createdAt, locale) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <RequestStatus v-if="store.moreState.status !== 'idle'" :state="store.moreState" @retry="store.loadMore">
            <template #default />
          </RequestStatus>
          <div v-if="store.nextCursor !== '' && store.moreState.status !== 'loading'" class="jl-card__actions">
            <UiButton variant="secondary" @click="store.loadMore">{{ t("common.loadMore") }}</UiButton>
          </div>
        </template>
        <template #empty>
          <UiEmptyState :title="t('users.list.empty')">
            <p>{{ t("users.list.emptyHint") }}</p>
          </UiEmptyState>
        </template>
      </RequestStatus>
    </section>

    <CreateUserForm />
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-users {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-users h1 {
  margin: 0;
}

.jl-users__badges {
  display: inline-flex;
  flex-wrap: wrap;
  gap: var(--jl-space-1);
}
</style>
