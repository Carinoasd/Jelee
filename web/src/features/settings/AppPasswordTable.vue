<script setup lang="ts">
import { useId } from "vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import { formatDateTime } from "@/i18n/format";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import type { AppPassword } from "./twoFactorApi";

// Application passwords of one account, each with a confirmed revoke
// (shared by the settings page and the administrator's user page).
defineProps<{ passwords: readonly AppPassword[]; busyId: string | null }>();
const emit = defineEmits<{ revoke: [password: AppPassword] }>();
const { t, locale } = useTwoFactorI18n();
const prefix = useId();
</script>

<template>
  <div v-if="passwords.length > 0" class="jl-apppw-wrap">
    <table class="jl-apppw">
      <caption class="jl-visually-hidden">{{ t("twoFactor.appPasswords.caption") }}</caption>
      <thead>
        <tr>
          <th scope="col">{{ t("twoFactor.appPasswords.name") }}</th>
          <th scope="col">{{ t("twoFactor.appPasswords.created") }}</th>
          <th scope="col">{{ t("twoFactor.appPasswords.lastUsed") }}</th>
          <th scope="col">{{ t("twoFactor.appPasswords.actions") }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="password in passwords" :key="password.id">
          <th :id="`${prefix}-${password.id}`" scope="row">{{ password.name }}</th>
          <td>{{ formatDateTime(password.createdAt, locale) }}</td>
          <td>{{ password.lastUsedAt ? formatDateTime(password.lastUsedAt, locale) : t("twoFactor.appPasswords.never") }}</td>
          <td>
            <UiConfirmButton
              :label="t('twoFactor.appPasswords.revoke')"
              :confirm-label="t('twoFactor.appPasswords.revokeConfirm')"
              :prompt="t('twoFactor.appPasswords.revokeImpact')"
              :busy="busyId === password.id"
              :describedby="`${prefix}-${password.id}`"
              @confirm="emit('revoke', password)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="jl-apppw-empty">{{ t("twoFactor.appPasswords.empty") }}</p>
</template>

<style scoped>
.jl-apppw-wrap {
  overflow-x: auto;
}

.jl-apppw {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--jl-font-size-sm);
}

.jl-apppw th,
.jl-apppw td {
  padding: var(--jl-space-2) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  vertical-align: middle;
  overflow-wrap: anywhere;
}

.jl-apppw thead th {
  color: var(--jl-color-text-muted);
  font-weight: 600;
}

.jl-apppw-empty {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}
</style>
