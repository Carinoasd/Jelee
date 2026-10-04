<script setup lang="ts">
import { onMounted, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { useUserAdminStore } from "@/stores/userAdmin";
import { useAdminFeedback } from "./feedback";
import { limitsBody, limitText, maxBandwidthLimit, maxConcurrentLimit, parseLimit } from "./form";

const { t } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();
const concurrent = shallowRef("");
const bandwidth = shallowRef("");
const concurrentProblem = shallowRef(false);
const bandwidthProblem = shallowRef(false);

watch(
  () => store.limits,
  (limits) => {
    concurrent.value = limitText(limits.maxStreams);
    bandwidth.value = limitText(limits.maxKbps);
  },
  { immediate: true },
);

onMounted(() => {
  void store.loadLimits();
});

async function save() {
  const parsedConcurrent = parseLimit(concurrent.value, maxConcurrentLimit);
  const parsedBandwidth = parseLimit(bandwidth.value, maxBandwidthLimit);
  concurrentProblem.value = !parsedConcurrent.ok;
  bandwidthProblem.value = !parsedBandwidth.ok;
  if (!parsedConcurrent.ok || !parsedBandwidth.ok) {
    return;
  }
  try {
    await store.saveLimits(limitsBody(parsedConcurrent.value, parsedBandwidth.value));
    await feedback.done("done", "users.limits.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-limits-title">
    <h2 id="user-limits-title">{{ t("users.limits.title") }}</h2>
    <div class="jl-card__impact">
      <p>{{ t("users.limits.impact") }}</p>
      <p>{{ t("users.limits.serverSwitch") }}</p>
    </div>
    <RequestStatus :state="store.limitsState" @retry="store.loadLimits">
      <template #loading>
        <UiSkeleton shape="block" />
      </template>
      <template #default>
        <form class="jl-limits" novalidate @submit.prevent="save">
          <div class="jl-card__fields">
            <UiTextField
              v-model="concurrent"
              inputmode="numeric"
              :label="t('users.limits.maxConcurrent')"
              :hint="t('users.limits.maxConcurrentHint', { max: maxConcurrentLimit })"
              :error="concurrentProblem ? t('users.limits.invalid', { max: maxConcurrentLimit }) : null"
              autocomplete="off"
            />
            <UiTextField
              v-model="bandwidth"
              inputmode="numeric"
              :label="t('users.limits.bandwidth')"
              :hint="t('users.limits.bandwidthHint', { max: maxBandwidthLimit })"
              :error="bandwidthProblem ? t('users.limits.invalid', { max: maxBandwidthLimit }) : null"
              autocomplete="off"
            />
          </div>
          <div class="jl-card__actions">
            <UiButton type="submit" :busy="store.busy === 'limits'">{{ t("common.save") }}</UiButton>
          </div>
        </form>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-limits {
  display: grid;
  gap: var(--jl-space-4);
}
</style>
