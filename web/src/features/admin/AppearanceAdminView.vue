<script setup lang="ts">
import { themeTokenNames } from "@jelee/plugin-sdk";
import { computed, shallowRef, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { saveBlob } from "@/api/download";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { exportSiteSettings, importSiteSettings, type PageLayout, type SiteSettingsDocument } from "@/features/site/api";
import { useAppearanceStore } from "@/stores/appearance";
import { builtInPresets, normalizeLayout, toUserLayout, useLayoutStore } from "@/stores/layout";
import { themes, type Theme } from "@/stores/preferences";
import { useSiteAppearanceStore } from "@/stores/siteAppearance";
import { useToastStore } from "@/stores/toasts";
import {
  customCssMaxLength,
  fontHostLimit,
  normalizeFontHost,
  sanitizeTokenValue,
  type CssIssueCode,
  type CssPolicy,
  type SanitizedCss,
} from "@/theme/customCss";

// Site appearance (G33.2–G33.5). Checking shows what the sanitizer drops
// and why; saving applies the sanitized result at once. With the server's
// site settings, saving replaces them for every user (the server sanitizes
// again and refuses structural problems), and the defaults, the settings
// file and the reset are available; otherwise the CSS stays in this
// browser. The raw text is kept so the administrator can fix it.
const { t } = useI18n();
const { client } = useApi();
const store = useAppearanceStore();
const site = useSiteAppearanceStore();
const layoutStore = useLayoutStore();
const toasts = useToastStore();
const cssId = useId();
const hostsId = useId();
const lightId = useId();
const darkId = useId();
const fileId = useId();

const css = shallowRef("");
const allowExternalFonts = shallowRef(false);
const hostsText = shallowRef("");
const verdict = shallowRef<SanitizedCss | null>(null);
const busy = shallowRef(false);
const server = computed(() => store.config !== null);

// The editor follows the stored settings whenever they are (re)loaded.
watch(
  () => store.settings,
  (settings) => {
    css.value = settings.css;
    allowExternalFonts.value = settings.policy.allowExternalFonts;
    hostsText.value = settings.policy.fontHosts.join("\n");
  },
  { immediate: true },
);

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
    fontHosts: [...new Set(hostEntries.value.map(normalizeFontHost).filter((host): host is string => host !== null))],
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

function failed(error: unknown) {
  toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
}

function check() {
  verdict.value = store.check(css.value, policy());
}

async function save() {
  if (hostError.value !== null || busy.value) {
    return;
  }
  busy.value = true;
  try {
    verdict.value = await store.save(css.value, policy());
    toasts.push(verdict.value.rejected ? "appearance.savedRejected" : server.value ? "appearance.savedServer" : "appearance.saved", verdict.value.rejected ? "danger" : "success");
  } catch (error: unknown) {
    verdict.value = store.check(css.value, policy());
    failed(error);
  } finally {
    busy.value = false;
  }
}

async function clear() {
  try {
    await store.clear();
    css.value = "";
    verdict.value = null;
    toasts.push("appearance.cleared", "success");
  } catch (error: unknown) {
    failed(error);
  }
}

// Site defaults (server only): default theme, token overrides, default layout.
const themeKey: Readonly<Record<Theme, string>> = { system: "settings.theme.system", light: "settings.theme.light", dark: "settings.theme.dark" };
const themeOptions = computed(() => themes.map((value) => ({ value, label: t(themeKey[value]) })));
const defaultTheme = shallowRef<Theme>("system");
const lightTokens = shallowRef("");
const darkTokens = shallowRef("");
const tokenNames: ReadonlySet<string> = new Set<string>(themeTokenNames);

type LayoutChoice = "standard" | "focused" | "metadata" | "mine" | "keep";
const layoutChoice = shallowRef<LayoutChoice>("standard");
const layoutKey: Readonly<Record<LayoutChoice, string>> = {
  standard: "appearance.defaults.layouts.standard",
  focused: "appearance.defaults.layouts.focused",
  metadata: "appearance.defaults.layouts.metadata",
  mine: "appearance.defaults.layouts.mine",
  keep: "appearance.defaults.layouts.keep",
};
const sameLayout = (a: unknown, b: unknown) => JSON.stringify(normalizeLayout(a)) === JSON.stringify(normalizeLayout(b));
const layoutOptions = computed(() => {
  const choices: LayoutChoice[] = ["standard", "focused", "metadata", "mine"];
  if (layoutChoice.value === "keep") {
    choices.push("keep");
  }
  return choices.map((value) => ({ value, label: t(layoutKey[value]) }));
});

function tokenText(tokens: Readonly<Record<string, string>>): string {
  return Object.entries(tokens)
    .map(([name, value]) => name + ": " + value)
    .join("\n");
}

watch(
  () => store.config,
  (config) => {
    if (config === null) {
      return;
    }
    defaultTheme.value = config.defaultTheme;
    lightTokens.value = tokenText(config.tokens.light);
    darkTokens.value = tokenText(config.tokens.dark);
    const stored = config.defaultLayout;
    layoutChoice.value =
      stored === null
        ? "standard"
        : sameLayout(stored, builtInPresets.focused)
          ? "focused"
          : sameLayout(stored, builtInPresets.metadata)
            ? "metadata"
            : "keep";
  },
  { immediate: true },
);

interface ParsedTokens {
  readonly tokens: Record<string, string>;
  readonly invalid: string[];
}

function parseTokens(text: string): ParsedTokens {
  const tokens: Record<string, string> = {};
  const invalid: string[] = [];
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "") {
      continue;
    }
    const colon = trimmed.indexOf(":");
    const name = colon < 0 ? trimmed : trimmed.slice(0, colon).trim().replace(/^--jl-/, "");
    const value = colon < 0 ? null : sanitizeTokenValue(trimmed.slice(colon + 1), name === "font-family");
    if (!tokenNames.has(name) || value === null || name in tokens) {
      invalid.push(trimmed.length > 40 ? trimmed.slice(0, 37) + "..." : trimmed);
    } else {
      tokens[name] = value;
    }
  }
  return { tokens, invalid };
}

const parsedLight = computed(() => parseTokens(lightTokens.value));
const parsedDark = computed(() => parseTokens(darkTokens.value));
const tokenError = computed(() => {
  const invalid = [...parsedLight.value.invalid, ...parsedDark.value.invalid];
  return invalid.length === 0 ? null : t("appearance.defaults.invalidTokens", { tokens: invalid.join(", ") });
});

function chosenLayout(): PageLayout | null {
  switch (layoutChoice.value) {
    case "standard":
      return null;
    case "focused":
    case "metadata":
      return toUserLayout(builtInPresets[layoutChoice.value], []).current;
    case "mine":
      return toUserLayout(layoutStore.layout, []).current;
    case "keep":
      return store.config?.defaultLayout ?? null;
  }
}

async function saveDefaults() {
  if (tokenError.value !== null || busy.value) {
    return;
  }
  busy.value = true;
  try {
    await store.saveDocument({
      defaultTheme: defaultTheme.value,
      tokens: { light: parsedLight.value.tokens, dark: parsedDark.value.tokens },
      defaultLayout: chosenLayout(),
    });
    toasts.push("appearance.defaults.saved", "success");
  } catch (error: unknown) {
    failed(error);
  } finally {
    busy.value = false;
  }
}

// Settings file (G33.3): appearance and plugins together.
async function exportFile() {
  try {
    const document = await exportSiteSettings(client);
    saveBlob(new Blob([JSON.stringify(document, null, 2) + "\n"], { type: "application/json" }), "jelee-site-settings.json");
  } catch (error: unknown) {
    failed(error);
  }
}

async function importFile(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (file === undefined) {
    return;
  }
  let document: SiteSettingsDocument;
  try {
    document = JSON.parse(await file.text()) as SiteSettingsDocument;
  } catch {
    toasts.push("appearance.file.invalid", "danger");
    return;
  }
  try {
    const result = await importSiteSettings(client, document);
    store.imported(result.appearance);
    site.pluginsChanged();
    verdict.value = null;
    toasts.push("appearance.file.imported", "success");
  } catch (error: unknown) {
    failed(error);
  }
}

async function resetAll() {
  try {
    await store.reset();
    verdict.value = null;
    toasts.push("appearance.file.resetDone", "success");
  } catch (error: unknown) {
    failed(error);
  }
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
    <UiAlert tone="info">{{ t(server ? "appearance.storageServer" : "appearance.storageNote") }}</UiAlert>
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
        <UiButton type="submit" :disabled="hostError !== null" :busy="busy">{{ t("appearance.save") }}</UiButton>
        <UiConfirmButton :label="t('appearance.clear')" :confirm-label="t('appearance.clearConfirm')" :prompt="t('appearance.clearPrompt')" @confirm="clear" />
      </div>
    </form>

    <section v-if="server" class="jl-appearance__section" aria-labelledby="appearance-defaults">
      <h2 id="appearance-defaults">{{ t("appearance.defaults.title") }}</h2>
      <p class="jl-appearance__muted">{{ t("appearance.defaults.intro") }}</p>
      <form class="jl-appearance__form" novalidate @submit.prevent="saveDefaults">
        <UiSelectField v-model="defaultTheme" :label="t('appearance.defaults.theme')" :options="themeOptions" :hint="t('appearance.defaults.themeHint')" />
        <div class="jl-appearance__field">
          <label :for="lightId">{{ t("appearance.defaults.tokensLight") }}</label>
          <textarea
            :id="lightId"
            v-model="lightTokens"
            class="jl-appearance__textarea"
            rows="4"
            spellcheck="false"
            :aria-describedby="lightId + '-hint'"
          />
          <p :id="lightId + '-hint'" class="jl-appearance__muted">{{ t("appearance.defaults.tokensHint", { names: themeTokenNames.join(", ") }) }}</p>
        </div>
        <div class="jl-appearance__field">
          <label :for="darkId">{{ t("appearance.defaults.tokensDark") }}</label>
          <textarea :id="darkId" v-model="darkTokens" class="jl-appearance__textarea" rows="4" spellcheck="false" />
        </div>
        <p v-if="tokenError" class="jl-appearance__error" role="alert">{{ tokenError }}</p>
        <UiSelectField v-model="layoutChoice" :label="t('appearance.defaults.layout')" :options="layoutOptions" :hint="t('appearance.defaults.layoutHint')" />
        <div class="jl-appearance__actions">
          <UiButton type="submit" :disabled="tokenError !== null" :busy="busy">{{ t("appearance.defaults.save") }}</UiButton>
        </div>
      </form>
    </section>

    <section v-if="server" class="jl-appearance__section" aria-labelledby="appearance-file">
      <h2 id="appearance-file">{{ t("appearance.file.title") }}</h2>
      <p class="jl-appearance__muted">{{ t("appearance.file.intro") }}</p>
      <div class="jl-appearance__actions">
        <UiButton variant="secondary" @click="exportFile">{{ t("appearance.file.export") }}</UiButton>
        <label class="jl-appearance__file" :for="fileId">
          {{ t("appearance.file.import") }}
          <input :id="fileId" type="file" accept="application/json,.json" @change="importFile" />
        </label>
        <UiConfirmButton
          :label="t('appearance.file.reset')"
          :confirm-label="t('appearance.file.resetConfirm')"
          :prompt="t('appearance.file.resetPrompt')"
          @confirm="resetAll"
        />
      </div>
    </section>

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

.jl-appearance__section {
  display: grid;
  gap: var(--jl-space-3);
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-appearance__file {
  display: inline-grid;
  gap: var(--jl-space-1);
  font-weight: 600;
}

.jl-appearance__file input {
  min-height: var(--jl-touch-target);
}
</style>
