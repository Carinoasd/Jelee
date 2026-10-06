<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, shallowRef, useTemplateRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useAdminFeedback } from "@/features/users/feedback";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import AccessTabs from "./AccessTabs.vue";
import RatingCodesEditor from "./RatingCodesEditor.vue";

const { t } = useI18n();
const store = useAccessPolicyStore();
const feedback = useAdminFeedback();
const form = reactive({ blockUnrated: false, restrictAdmins: false });
const editingRatings = shallowRef(false);
const editButton = useTemplateRef<HTMLElement>("editButton");

async function closeRatings() {
  editingRatings.value = false;
  await nextTick();
  editButton.value?.querySelector("button")?.focus();
}

watch(
  () => store.policy,
  (policy) => {
    if (policy !== null) {
      form.blockUnrated = policy.blockUnrated;
      form.restrictAdmins = policy.restrictAdmins;
    }
  },
  { immediate: true },
);

const changed = computed(
  () => store.policy !== null && (store.policy.blockUnrated !== form.blockUnrated || store.policy.restrictAdmins !== form.restrictAdmins),
);

onMounted(() => {
  void store.loadPolicy();
  void store.loadRatings();
});

async function save() {
  try {
    await store.save({ blockUnrated: form.blockUnrated, restrictAdmins: form.restrictAdmins });
    await feedback.done("done", "access.policy.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-access" aria-labelledby="access-title">
    <h1 id="access-title" tabindex="-1">{{ t("access.title") }}</h1>
    <p class="jl-card__muted">{{ t("access.intro") }}</p>
    <AccessTabs />

    <section class="jl-card" aria-labelledby="access-policy-title">
      <h2 id="access-policy-title">{{ t("access.policy.title") }}</h2>
      <RequestStatus :state="store.policyState" @retry="store.loadPolicy">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 2" :key="n" shape="block" class="jl-skeleton-row" />
          </div>
        </template>
        <template #default>
          <div class="jl-access__policy">
            <UiCheckbox v-model="form.blockUnrated" :label="t('access.policy.blockUnrated')" :hint="t('access.policy.blockUnratedImpact')" />
            <UiCheckbox v-model="form.restrictAdmins" :label="t('access.policy.restrictAdmins')" :hint="t('access.policy.restrictAdminsImpact')" />
            <p class="jl-card__impact">{{ t("access.policy.impact") }}</p>
            <div class="jl-card__actions">
              <UiConfirmButton
                variant="secondary"
                :label="t('common.save')"
                :confirm-label="t('access.policy.confirm')"
                :prompt="t('access.policy.impact')"
                :disabled="!changed"
                :busy="store.saving"
                @confirm="save"
              />
            </div>
          </div>
        </template>
      </RequestStatus>
    </section>

    <section class="jl-card" aria-labelledby="access-order-title">
      <h2 id="access-order-title">{{ t("access.order.title") }}</h2>
      <ol class="jl-access__order">
        <li>{{ t("access.order.libraries") }}</li>
        <li>{{ t("access.order.unrestricted") }}</li>
        <li>{{ t("access.order.rules") }}</li>
        <li>{{ t("access.order.tags") }}</li>
        <li>{{ t("access.order.rating") }}</li>
      </ol>
      <p>
        {{ t("access.order.perUser") }}
        <RouterLink :to="{ name: 'admin-users' }">{{ t("access.order.usersLink") }}</RouterLink>
      </p>
    </section>

    <section class="jl-card" aria-labelledby="access-ratings-title">
      <div class="jl-access__head">
        <h2 id="access-ratings-title">{{ t("access.ratings.title") }}</h2>
        <span ref="editButton">
          <UiButton v-if="!editingRatings" variant="secondary" :disabled="store.ratingsState.status === 'loading' || store.ratingsState.status === 'error'" @click="editingRatings = true">
            {{ t("contentRules.ratings.edit") }}
          </UiButton>
        </span>
      </div>
      <p class="jl-card__muted">{{ t("access.ratings.intro") }}</p>
      <RatingCodesEditor v-if="editingRatings" @done="closeRatings" />
      <RequestStatus v-else :state="store.ratingsState" @retry="store.loadRatings">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 4" :key="n" class="jl-skeleton-row" />
          </div>
        </template>
        <template #default>
          <div class="jl-table-wrap">
            <table class="jl-table">
              <caption class="jl-visually-hidden">{{ t("access.ratings.caption") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("access.ratings.level") }}</th>
                  <th scope="col">{{ t("access.ratings.codes") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="level in store.levels" :key="level.level">
                  <th scope="row">{{ t("access.ratings.age", { level: level.level }) }}</th>
                  <td>{{ level.codes.join(", ") }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
        <template #empty>
          <UiEmptyState :title="t('access.ratings.empty')" />
        </template>
      </RequestStatus>
    </section>
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-access {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-access h1 {
  margin: 0;
}

.jl-access__policy {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-access__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
}

.jl-access__head h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-access__order {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
  padding-inline-start: var(--jl-space-6);
}

.jl-card > p {
  margin: 0;
}
</style>
