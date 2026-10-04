<script setup lang="ts">
import { computed, shallowRef, useId } from "vue";
import { useI18n } from "vue-i18n";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import { useAppearanceStore } from "@/stores/appearance";
import { useToastStore } from "@/stores/toasts";
import { customCssMaxLength, fontHostLimit, normalizeFontHost, type CssIssueCode, type CssPolicy, type SanitizedCss } from "@/theme/customCss";

// Administrator CSS (G33.4). Checking shows what the sanitizer drops and
// why; saving applies the sanitized result at once. The raw text is kept so
// the administrator can fix it, and it is sanitized again on every load.
const { t } = useI18n();
const store = useAppearanceStore();
const toasts = useToastStore();
const cssId = useId();
const hostsId = useId();

const css = shallowRef(store.settings.css);
const allowExternalFonts = shallowRef(store.settings.policy.allowExternalFonts);
const hostsText = shallowRef(store.settings.policy.fontHosts.join("\n"));
const verdict = shallowRef<SanitizedCss | null>(null);

const hostEntries = computed(() => hostsText.value.split(/[\s,]+/).filter((entry) => entry !== ""));
const invalidHosts = computed(() => hostEntries.value.filter((entry) => normalizeFontHost(entry) === null));
const hostError = computed(() => {
  if (invalidHosts.value.length > 0) {
    return t("appearance.fonts.invalidHosts", { hosts: invalidHosts.value.join(", ") });
  }
  return hostEntries.value.length > fontHostLimit ? t("appearance.fonts.tooMany", { max: fontHostLimit }) : null;
});

function policy(): CssPolicy {
  return {
    allowExternalFonts: allowExternalFonts.value,
    fontHosts: hostEntries.value.map(normalizeFontHost).filter((host): host is string => host !== null),
  };
}

const issueKey: Readonly<Record<CssIssueCode, string>> = {
  too_long: "appearance.issues.tooLong",
  markup: "appearance.issues.markup",
  escape: "appearance.issues.escape",
  control_char: "appearance.issues.controlChar",
  unterminated_comment: "appearance.issues.unterminatedComment",
  unbalanced: "appearance.issues.unbalanced",
  too_deep: "appearance.issues.tooDeep",
  import_blocked: "appearance.issues.importBlocked",
  at_rule_blocked: "appearance.issues.atRuleBlocked",
  invalid_selector: "appearance.issues.invalidSelector",
  invalid_declaration: "appearance.issues.invalidDeclaration",
  nesting_unsupported: "appearance.issues.nestingUnsupported",
  expression: "appearance.issues.expression",
  script_url: "appearance.issues.scriptUrl",
  binding: "appearance.issues.binding",
  blocked_function: "appearance.issues.blockedFunction",
  external_url: "appearance.issues.externalUrl",
  external_font: "appearance.issues.externalFont",
};

function check() {
  verdict.value = store.check(css.value, policy());
}

function save() {
  if (hostError.value !== null) {
    return;
  }
  verdict.value = store.save(css.value, policy());
  toasts.push(verdict.value.rejected ? "appearance.savedRejected" : "appearance.saved", verdict.value.rejected ? "danger" : "success");
}

function clear() {
  store.clear();
  css.value = "";
  verdict.value = null;
  toasts.push("appearance.cleared", "success");
}
</script>

<template>
  <section class="jl-appearance" aria-labelledby="appearance-title">
    <h1 id="appearance-title" tabindex="-1">{{ t("appearance.title") }}</h1>
    <p class="jl-appearance__muted">{{ t("appearance.intro") }}</p>
    <ul class="jl-appearance__rules">
      <li>{{ t("appearance.rules.markup") }}</li>
      <li>{{ t("appearance.rules.urls") }}</li>
      <li>{{ t("appearance.rules.fonts") }}</li>
      <li>{{ t("appearance.rules.csp") }}</li>
    </ul>
    <UiAlert tone="info">{{ t("appearance.storageNote") }}</UiAlert>
    <UiAlert v-if="!store.supported" tone="danger">{{ t("appearance.unsupported") }}</UiAlert>

    <form class="jl-appearance__form" novalidate @submit.prevent="save">
      <div class="jl-appearance__field">
        <label :for="cssId">{{ t("appearance.cssLabel") }}</label>
        <textarea
          :id="cssId"
          v-model="css"
          class="jl-appearance__textarea"
          rows="14"
          spellcheck="false"
          autocomplete="off"
          :maxlength="customCssMaxLength"
          :aria-describedby="cssId + '-hint'"
        />
        <p :id="cssId + '-hint'" class="jl-appearance__muted">{{ t("appearance.cssHint", { max: customCssMaxLength }) }}</p>
      </div>

      <fieldset class="jl-appearance__fonts">
        <legend>{{ t("appearance.fonts.legend") }}</legend>
        <UiCheckbox v-model="allowExternalFonts" :label="t('appearance.fonts.allow')" :hint="t('appearance.fonts.allowHint')" />
        <div class="jl-appearance__field">
          <label :for="hostsId">{{ t("appearance.fonts.hosts") }}</label>
          <textarea
            :id="hostsId"
            v-model="hostsText"
            class="jl-appearance__textarea"
            rows="3"
            spellcheck="false"
            :disabled="!allowExternalFonts"
            :aria-invalid="hostError ? 'true' : undefined"
            :aria-describedby="hostsId + '-hint' + (hostError ? ' ' + hostsId + '-error' : '')"
          />
          <p :id="hostsId + '-hint'" class="jl-appearance__muted">{{ t("appearance.fonts.hostsHint", { max: fontHostLimit }) }}</p>
          <p v-if="hostError" :id="hostsId + '-error'" class="jl-appearance__error">{{ hostError }}</p>
        </div>
      </fieldset>

      <div class="jl-appearance__actions">
        <UiButton variant="secondary" @click="check">{{ t("appearance.check") }}</UiButton>
        <UiButton type="submit" :disabled="hostError !== null">{{ t("appearance.save") }}</UiButton>
        <UiConfirmButton :label="t('appearance.clear')" :confirm-label="t('appearance.clearConfirm')" :prompt="t('appearance.clearPrompt')" @confirm="clear" />
      </div>
    </form>

    <section v-if="verdict" class="jl-appearance__result" aria-labelledby="appearance-result" aria-live="polite">
      <h2 id="appearance-result">{{ t("appearance.result") }}</h2>
      <p v-if="verdict.rejected" class="jl-appearance__error">{{ t("appearance.rejected") }}</p>
      <p v-else>{{ t("appearance.accepted", { length: verdict.css.length, count: verdict.issues.length }) }}</p>
      <ul v-if="verdict.issues.length > 0" class="jl-appearance__issues">
        <li v-for="(issue, index) in verdict.issues" :key="index">
          {{ t(issueKey[issue.code]) }}
          <code>{{ issue.excerpt }}</code>
        </li>
      </ul>
    </section>
  </section>
</template>

<style scoped>
.jl-appearance {
  display: grid;
  gap: var(--jl-space-4);
  max-width: 820px;
}

.jl-appearance h1,
.jl-appearance h2 {
  margin: 0;
}

.jl-appearance h2 {
  font-size: var(--jl-font-size-lg);
}

.jl-appearance__muted {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-appearance__rules {
  margin: 0;
  padding-inline-start: var(--jl-space-6);
}

.jl-appearance__form,
.jl-appearance__fonts {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-appearance__fonts {
  margin: 0;
  padding: var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-appearance__field {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-appearance__field label {
  font-weight: 600;
}

.jl-appearance__textarea {
  width: 100%;
  box-sizing: border-box;
  padding: var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font-family: ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace;
  font-size: var(--jl-font-size-sm);
}

.jl-appearance__error {
  margin: 0;
  color: var(--jl-color-danger);
}

.jl-appearance__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: var(--jl-space-2);
}

.jl-appearance__issues {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding-inline-start: var(--jl-space-6);
}

.jl-appearance__issues code {
  overflow-wrap: anywhere;
}
</style>
