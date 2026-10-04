<script setup lang="ts">
// Initial setup wizard (G18). Browsing and administration only: nothing
// here plays media. The one-time setup token is kept in memory only, so a
// reload asks for it again; the server keeps the wizard's progress.
import { computed, nextTick, reactive, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { supportedLocales, type Locale } from "@/i18n/locales";
import { completeSetup, readSetup, setupBack, SetupInputError, submitSetupStep, type SetupIssue, type SetupState } from "./api";
import { markSetupComplete } from "./gate";
import { setupIssueKey, setupIssueRow } from "./issues";

const { t } = useI18n();
const { client } = useApi();
const id = useId();

const steps = ["language", "admin", "database", "media", "tmdb", "toolchain", "metadata-policy", "network", "complete"] as const;
type Step = (typeof steps)[number];
const stepTitles: Readonly<Record<Step, string>> = {
  language: "setup.steps.language",
  admin: "setup.steps.admin",
  database: "setup.steps.database",
  media: "setup.steps.media",
  tmdb: "setup.steps.tmdb",
  toolchain: "setup.steps.toolchain",
  "metadata-policy": "setup.steps.metadataPolicy",
  network: "setup.steps.network",
  complete: "setup.steps.complete",
};
const localeNames: Readonly<Record<Locale, string>> = {
  "zh-CN": "common.localeNames.zhCN",
  "zh-TW": "common.localeNames.zhTW",
  "en-US": "common.localeNames.enUS",
  "ja-JP": "common.localeNames.jaJP",
};

const token = shallowRef("");
const state = shallowRef<SetupState | null>(null);
const finished = shallowRef(false);
const busy = shallowRef(false);
const failure = shallowRef<string | null>(null);
const issues = shallowRef<readonly SetupIssue[]>([]);
const alertBox = useTemplateRef<HTMLElement>("alert");

interface SetupForm {
  locale: Locale;
  adminName: string;
  adminDisplayName: string;
  password: string;
  libraries: { name: string; path: string }[];
  tmdbEnabled: boolean;
  tmdbLanguage: Locale;
  acceptDegraded: boolean;
  nfoRead: "off" | "read-only";
  nfoWrite: "off" | "write-back";
  imageFetch: boolean;
  imageWriteBack: boolean;
  networkMode: "local" | "lan" | "reverse-proxy";
  listen: string;
  allowedHosts: string;
  trustedProxies: string;
  privacyAcknowledged: boolean;
}

const form = reactive<SetupForm>({
  locale: "zh-CN",
  adminName: "",
  adminDisplayName: "",
  password: "",
  libraries: [{ name: "", path: "" }],
  tmdbEnabled: false,
  tmdbLanguage: "zh-CN",
  acceptDegraded: false,
  nfoRead: "read-only",
  nfoWrite: "off",
  imageFetch: false,
  imageWriteBack: false,
  networkMode: "local",
  listen: "127.0.0.1:8097",
  allowedHosts: "localhost, 127.0.0.1, ::1",
  trustedProxies: "",
  privacyAcknowledged: false,
});

const current = computed<Step>(() => state.value?.current ?? "language");
const position = computed(() => steps.indexOf(current.value) + 1);
const adminCreated = computed(() => (state.value?.admin.userId ?? "") !== "");

const list = (value: string) =>
  value
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");

/** Prefills the form from stored progress so a resumed wizard shows its data. */
function adopt(next: SetupState) {
  state.value = next;
  form.locale = next.locale ?? form.locale;
  form.adminName = next.admin.name ?? form.adminName;
  form.adminDisplayName = next.admin.displayName ?? form.adminDisplayName;
  if (next.media !== undefined && next.media.length > 0) {
    form.libraries = next.media.map((library) => ({ ...library }));
  }
  form.tmdbEnabled = next.tmdb.enabled;
  form.tmdbLanguage = next.tmdb.language ?? form.locale;
  form.acceptDegraded = next.toolchain.acceptDegraded;
  if (next.metadataPolicy.nfoRead === "off" || next.metadataPolicy.nfoRead === "read-only") {
    form.nfoRead = next.metadataPolicy.nfoRead;
  }
  if (next.metadataPolicy.nfoWrite === "off" || next.metadataPolicy.nfoWrite === "write-back") {
    form.nfoWrite = next.metadataPolicy.nfoWrite;
  }
  form.imageFetch = next.metadataPolicy.imageFetch;
  form.imageWriteBack = next.metadataPolicy.imageWriteBack;
  const mode = next.network.mode;
  if (mode === "local" || mode === "lan" || mode === "reverse-proxy") {
    form.networkMode = mode;
  }
  form.listen = next.network.listen ?? form.listen;
  if (next.network.allowedHosts !== undefined) {
    form.allowedHosts = next.network.allowedHosts.join(", ");
  }
  if (next.network.trustedProxies !== undefined) {
    form.trustedProxies = next.network.trustedProxies.join(", ");
  }
  form.privacyAcknowledged = next.network.privacyAcknowledged;
}

async function run(operation: () => Promise<SetupState>) {
  if (busy.value) {
    return;
  }
  busy.value = true;
  failure.value = null;
  issues.value = [];
  try {
    const next = await operation();
    if (next.completedAt !== undefined) {
      finished.value = true;
      state.value = next;
      markSetupComplete();
    } else {
      adopt(next);
    }
  } catch (error: unknown) {
    if (error instanceof SetupInputError) {
      issues.value = error.issues;
      failure.value = "setup.inputInvalid";
    } else if (error instanceof ApiError && error.code === "setup_token_invalid") {
      failure.value = "setup.tokenInvalid";
      state.value = null;
    } else if (error instanceof ApiError && error.code === "setup_completed") {
      finished.value = true;
      markSetupComplete();
    } else if (error instanceof ApiError && error.code === "setup_step_order") {
      failure.value = "setup.stepOrder";
    } else {
      failure.value = error instanceof ApiError ? errorMessageKey(error) : "errors.generic";
    }
    await nextTick();
    alertBox.value?.focus();
  } finally {
    form.password = "";
    busy.value = false;
  }
}

function start() {
  if (token.value.trim() === "") {
    failure.value = "setup.tokenRequired";
    return;
  }
  void run(() => readSetup(client, token.value.trim()));
}

function submit() {
  const secret = token.value.trim();
  switch (current.value) {
    case "language":
      return run(() => submitSetupStep(client, secret, "language", { locale: form.locale }));
    case "admin":
      return run(() =>
        submitSetupStep(client, secret, "admin", {
          name: form.adminName.trim(),
          displayName: form.adminDisplayName,
          password: form.password,
        }),
      );
    case "database":
      return run(() => submitSetupStep(client, secret, "database"));
    case "media":
      return run(() =>
        submitSetupStep(client, secret, "media", {
          libraries: form.libraries.filter((library) => library.name.trim() !== "" || library.path.trim() !== ""),
        }),
      );
    case "tmdb":
      return run(() =>
        submitSetupStep(client, secret, "tmdb", form.tmdbEnabled ? { enabled: true, language: form.tmdbLanguage } : { enabled: false }),
      );
    case "toolchain":
      return run(() => submitSetupStep(client, secret, "toolchain", { acceptDegraded: form.acceptDegraded }));
    case "metadata-policy":
      return run(() =>
        submitSetupStep(client, secret, "metadata-policy", {
          nfoRead: form.nfoRead,
          nfoWrite: form.nfoWrite,
          imageFetch: form.imageFetch,
          imageWriteBack: form.imageWriteBack,
        }),
      );
    case "network":
      return run(() =>
        submitSetupStep(client, secret, "network", {
          mode: form.networkMode,
          listen: form.listen.trim(),
          allowedHosts: list(form.allowedHosts),
          trustedProxies: list(form.trustedProxies),
          privacyAcknowledged: form.privacyAcknowledged,
        }),
      );
    case "complete":
      return run(() => completeSetup(client, secret));
  }
}

function back() {
  void run(() => setupBack(client, token.value.trim()));
}

function issueText(issue: SetupIssue): string {
  const row = setupIssueRow(issue.field);
  const message = t(setupIssueKey(issue.code));
  return row === null ? message : t("setup.issueRow", { row, message });
}
</script>

<template>
  <section class="jl-setup" aria-labelledby="setup-title">
    <h1 id="setup-title" tabindex="-1">{{ t("setup.title") }}</h1>

    <div v-if="finished">
      <UiAlert tone="success">{{ t("setup.finished") }}</UiAlert>
      <p>
        <RouterLink :to="{ name: 'login' }">{{ t("setup.goToLogin") }}</RouterLink>
      </p>
    </div>

    <form v-else-if="state === null" class="jl-setup__form" novalidate @submit.prevent="start">
      <p>{{ t("setup.tokenIntro") }}</p>
      <UiTextField v-model="token" :label="t('setup.token')" type="password" autocomplete="off" required :maxlength="128" />
      <div v-if="failure" ref="alert" tabindex="-1" class="jl-setup__alert">
        <UiAlert tone="danger">{{ t(failure) }}</UiAlert>
      </div>
      <UiButton type="submit" :busy="busy">{{ t("setup.start") }}</UiButton>
    </form>

    <form v-else class="jl-setup__form" novalidate @submit.prevent="submit">
      <p class="jl-setup__progress">{{ t("setup.progress", { current: position, total: steps.length }) }}</p>
      <h2>{{ t(stepTitles[current]) }}</h2>

      <template v-if="current === 'language'">
        <label :for="`${id}-locale`">{{ t("setup.fields.locale") }}</label>
        <select :id="`${id}-locale`" v-model="form.locale">
          <option v-for="code in supportedLocales" :key="code" :value="code">{{ t(localeNames[code]) }}</option>
        </select>
      </template>

      <template v-else-if="current === 'admin'">
        <p v-if="adminCreated">{{ t("setup.adminCreatedNote") }}</p>
        <UiTextField v-model="form.adminName" :label="t('setup.fields.adminName')" autocomplete="username" required :maxlength="128" />
        <template v-if="!adminCreated">
          <UiTextField v-model="form.adminDisplayName" :label="t('setup.fields.adminVisibleName')" :maxlength="128" />
          <UiTextField
            v-model="form.password"
            :label="t('setup.fields.password')"
            :hint="t('setup.passwordHint')"
            type="password"
            autocomplete="new-password"
            required
            :maxlength="1024"
          />
        </template>
      </template>

      <p v-else-if="current === 'database'">{{ t("setup.databaseIntro") }}</p>

      <template v-else-if="current === 'media'">
        <p>{{ t("setup.mediaIntro") }}</p>
        <fieldset v-for="(library, index) in form.libraries" :key="index" class="jl-setup__row">
          <legend>{{ t("setup.libraryLegend", { row: index + 1 }) }}</legend>
          <UiTextField v-model="library.name" :label="t('setup.fields.libraryName')" :maxlength="128" />
          <UiTextField v-model="library.path" :label="t('setup.fields.libraryPath')" :maxlength="4096" />
          <UiButton variant="ghost" @click="form.libraries.splice(index, 1)">{{ t("setup.removeLibrary") }}</UiButton>
        </fieldset>
        <UiButton variant="secondary" @click="form.libraries.push({ name: '', path: '' })">{{ t("setup.addLibrary") }}</UiButton>
      </template>

      <template v-else-if="current === 'tmdb'">
        <p>{{ t("setup.tmdbIntro") }}</p>
        <label class="jl-setup__check"><input v-model="form.tmdbEnabled" type="checkbox" /> {{ t("setup.fields.tmdbEnabled") }}</label>
        <template v-if="form.tmdbEnabled">
          <label :for="`${id}-tmdb`">{{ t("setup.fields.tmdbLanguage") }}</label>
          <select :id="`${id}-tmdb`" v-model="form.tmdbLanguage">
            <option v-for="code in supportedLocales" :key="code" :value="code">{{ t(localeNames[code]) }}</option>
          </select>
        </template>
      </template>

      <template v-else-if="current === 'toolchain'">
        <p>{{ t("setup.toolchainIntro") }}</p>
        <label class="jl-setup__check"><input v-model="form.acceptDegraded" type="checkbox" /> {{ t("setup.fields.acceptDegraded") }}</label>
      </template>

      <template v-else-if="current === 'metadata-policy'">
        <label :for="`${id}-nfo-read`">{{ t("setup.fields.nfoRead") }}</label>
        <select :id="`${id}-nfo-read`" v-model="form.nfoRead">
          <option value="read-only">{{ t("setup.options.nfoReadOnly") }}</option>
          <option value="off">{{ t("setup.options.off") }}</option>
        </select>
        <label :for="`${id}-nfo-write`">{{ t("setup.fields.nfoWrite") }}</label>
        <select :id="`${id}-nfo-write`" v-model="form.nfoWrite">
          <option value="off">{{ t("setup.options.off") }}</option>
          <option value="write-back">{{ t("setup.options.writeBack") }}</option>
        </select>
        <label class="jl-setup__check"><input v-model="form.imageFetch" type="checkbox" /> {{ t("setup.fields.imageFetch") }}</label>
        <label class="jl-setup__check"><input v-model="form.imageWriteBack" type="checkbox" /> {{ t("setup.fields.imageWriteBack") }}</label>
      </template>

      <template v-else-if="current === 'network'">
        <label :for="`${id}-mode`">{{ t("setup.fields.networkMode") }}</label>
        <select :id="`${id}-mode`" v-model="form.networkMode">
          <option value="local">{{ t("setup.options.local") }}</option>
          <option value="lan">{{ t("setup.options.lan") }}</option>
          <option value="reverse-proxy">{{ t("setup.options.reverseProxy") }}</option>
        </select>
        <UiTextField v-model="form.listen" :label="t('setup.fields.listen')" :maxlength="64" />
        <UiTextField v-model="form.allowedHosts" :label="t('setup.fields.allowedHosts')" :hint="t('setup.commaHint')" :maxlength="8192" />
        <UiTextField v-model="form.trustedProxies" :label="t('setup.fields.trustedProxies')" :hint="t('setup.commaHint')" :maxlength="4096" />
        <p v-if="form.networkMode !== 'local'">{{ t("setup.privacyNotice") }}</p>
        <label class="jl-setup__check"><input v-model="form.privacyAcknowledged" type="checkbox" /> {{ t("setup.fields.privacyAcknowledged") }}</label>
      </template>

      <p v-else>{{ t("setup.completeIntro") }}</p>

      <div v-if="failure" ref="alert" tabindex="-1" class="jl-setup__alert">
        <UiAlert tone="danger">
          <p>{{ t(failure) }}</p>
          <ul v-if="issues.length > 0">
            <li v-for="(issue, index) in issues" :key="index">{{ issueText(issue) }}</li>
          </ul>
        </UiAlert>
      </div>

      <div class="jl-setup__actions">
        <UiButton v-if="current !== 'language'" variant="secondary" :disabled="busy" @click="back">{{ t("setup.back") }}</UiButton>
        <UiButton type="submit" :busy="busy">{{ current === "complete" ? t("setup.finish") : t("setup.next") }}</UiButton>
      </div>
    </form>
  </section>
</template>

<style scoped>
.jl-setup {
  max-width: 560px;
  margin: var(--jl-space-8) auto;
  padding: var(--jl-space-6);
  background: var(--jl-color-surface);
  border-radius: var(--jl-radius-md);
  box-shadow: var(--jl-shadow-1);
}

.jl-setup h1 {
  margin-top: 0;
  font-size: var(--jl-font-size-xl);
}

.jl-setup__form {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-setup__row {
  display: grid;
  gap: var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-setup__check {
  display: flex;
  gap: var(--jl-space-2);
  align-items: center;
}

.jl-setup__progress {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-setup__actions {
  display: flex;
  gap: var(--jl-space-3);
  justify-content: flex-end;
}

.jl-setup__alert:focus {
  outline: none;
}
</style>
