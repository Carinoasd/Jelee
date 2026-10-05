<script setup lang="ts">
import { computed, onMounted, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useClientPolicyStore } from "@/stores/clientsPolicy";
import { useToastStore } from "@/stores/toasts";
import type { ClientPolicy, UnknownClientsPolicy } from "./api";
import { asApiError, unknownClientsKeys } from "./labels";

const { t, locale } = useI18n();
const store = useClientPolicyStore();
const toasts = useToastStore();
/** Server-side recovery command when a policy locks administrators out. */
const recoveryCommand = "jelee-cli access reset-policies";

const unknownClients = shallowRef<string>("allow");
const exemptAdmins = shallowRef(true);
const exemptLoopback = shallowRef(true);

function isUnknownPolicy(value: string): value is UnknownClientsPolicy {
  return value in unknownClientsKeys;
}

function fill(policy: ClientPolicy | null) {
  if (policy !== null) {
    unknownClients.value = policy.unknownClients;
    exemptAdmins.value = policy.exemptAdmins;
    exemptLoopback.value = policy.exemptLoopback;
  }
}

watch(() => store.policy, fill, { immediate: true });

onMounted(() => {
  void store.load();
});

const options = computed(() =>
  (Object.keys(unknownClientsKeys) as UnknownClientsPolicy[]).map((value) => ({ value, label: t(unknownClientsKeys[value].label) })),
);
const selected = computed(() => (isUnknownPolicy(unknownClients.value) ? unknownClients.value : "allow"));
const changed = computed(() => {
  const current = store.policy;
  return (
    current !== null &&
    (current.unknownClients !== selected.value || current.exemptAdmins !== exemptAdmins.value || current.exemptLoopback !== exemptLoopback.value)
  );
});
const prompt = computed(() => {
  const parts = [t("clients.policy.confirmPrompt", { impact: t(unknownClientsKeys[selected.value].impact) })];
  if (!exemptAdmins.value && selected.value !== "allow") {
    parts.push(t("clients.policy.adminWarning"));
  }
  parts.push(t("clients.policy.recovery", { command: recoveryCommand }));
  return parts.join(" ");
});

async function save() {
  try {
    await store.save({ unknownClients: selected.value, exemptAdmins: exemptAdmins.value, exemptLoopback: exemptLoopback.value });
    toasts.push("clients.policy.saved", "success");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(asApiError(error)), "danger");
  }
}
</script>

<template>
  <section class="jl-cc-card" aria-labelledby="clients-policy-title">
    <h2 id="clients-policy-title">{{ t("clients.policy.title") }}</h2>
    <p class="jl-cc-muted">{{ t("clients.policy.hint") }}</p>
    <RequestStatus :state="store.state" @retry="store.load">
      <template #loading>
        <UiSkeleton shape="block" />
      </template>
      <template #default="{ data }">
        <form class="jl-cc-form" @submit.prevent>
          <UiSelectField
            v-model="unknownClients"
            :label="t('clients.policy.unknownClients')"
            :options="options"
            :hint="t(unknownClientsKeys[selected].impact)"
          />
          <UiCheckbox v-model="exemptAdmins" :label="t('clients.policy.exemptAdmins')" :hint="t('clients.policy.exemptAdminsHint')" />
          <UiCheckbox v-model="exemptLoopback" :label="t('clients.policy.exemptLoopback')" :hint="t('clients.policy.exemptLoopbackHint')" />
          <UiAlert tone="info">
            <p>{{ t("clients.policy.recovery", { command: recoveryCommand }) }}</p>
          </UiAlert>
          <p class="jl-cc-muted">
            {{ t("clients.policy.version", { version: data.version, date: formatDateTime(data.updatedAt, locale) }) }}
          </p>
          <div class="jl-cc-actions">
            <UiConfirmButton
              variant="secondary"
              :label="t('clients.policy.save')"
              :confirm-label="t('clients.policy.saveConfirm')"
              :prompt="prompt"
              :busy="store.saving"
              :disabled="!changed"
              @confirm="save"
            />
            <span v-if="!changed" class="jl-cc-muted">{{ t("clients.policy.unchanged") }}</span>
          </div>
        </form>
      </template>
    </RequestStatus>
  </section>
</template>
