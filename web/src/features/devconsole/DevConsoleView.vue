<script setup lang="ts">
import { computed, onMounted, reactive, shallowRef, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { copyText } from "@/features/settings/clipboard";
import { loadLazyMessages } from "@/i18n";
import { useToastStore } from "@/stores/toasts";
import { checkConsoleAccess, loadOperations, sendConsoleRequest, type ConsoleAccess, type ConsoleResponse } from "./api";
import { buildRequest, curlCommand, paramKey, type ConsoleOperation, type ConsoleParam } from "./spec";

// Developer mode API console (G49.4). Reaching the route already needed an
// administrator and an active developer mode session (router guard); the
// page checks again with the server (GET /api/v1/dev, which only a capable
// instance routes) before it reads the instance's OpenAPI document. It
// lists every operation except playback and direct-delivery routes, sends
// requests with the page's own session (the cookie and the CSRF header are
// added by the API client; their values never appear here), and copies a
// cURL command with placeholders instead of credentials. Writes need the
// two-step confirmation, dangerous operations an acknowledgement as well.
const { t } = useI18n();
const i18nGlobal = useI18n({ useScope: "global" });
const { client } = useApi();
const toasts = useToastStore();
const bodyId = useId();
const curlId = useId();

const ready = shallowRef(false);
const access = shallowRef<ConsoleAccess | "checking">("checking");
const operations = shallowRef<readonly ConsoleOperation[]>([]);
const loadFailed = shallowRef(false);
const filter = shallowRef("");
const selectedId = shallowRef("");
const values = reactive<Record<string, string>>({});
const bodyText = shallowRef("");
const acknowledged = shallowRef(false);
const busy = shallowRef(false);
const result = shallowRef<ConsoleResponse | null>(null);
const failed = shallowRef(false);

onMounted(async () => {
  await loadLazyMessages(i18nGlobal, "devconsole");
  ready.value = true;
  access.value = await checkConsoleAccess(client);
  if (access.value !== "granted") {
    return;
  }
  try {
    operations.value = await loadOperations(client);
    selectedId.value = operations.value[0]?.id ?? "";
  } catch {
    loadFailed.value = true;
  }
});

const selected = computed(() => operations.value.find((operation) => operation.id === selectedId.value) ?? null);

const options = computed(() => {
  const needle = filter.value.trim().toLowerCase();
  return operations.value
    .filter((operation) => operation.id === selectedId.value || needle === "" || (operation.id + " " + operation.summary).toLowerCase().includes(needle))
    .map((operation) => ({ value: operation.id, label: operation.summary === "" ? operation.id : operation.id + " — " + operation.summary }));
});

// A new operation starts from empty parameters and its body skeleton.
watch(selected, (operation) => {
  for (const key of Object.keys(values)) {
    Reflect.deleteProperty(values, key);
  }
  for (const param of operation?.params ?? []) {
    values[paramKey(param)] = "";
  }
  bodyText.value = operation?.bodyTemplate ?? "";
  acknowledged.value = false;
  result.value = null;
  failed.value = false;
});

const built = computed(() => (selected.value === null ? null : buildRequest(selected.value, values, bodyText.value)));
const request = computed(() => (built.value !== null && "path" in built.value ? built.value : null));

const problem = computed(() => {
  const value = built.value;
  if (value === null || "path" in value) {
    return null;
  }
  switch (value.kind) {
    case "missing":
      return t("devconsole.problems.missing", { name: value.name });
    case "segment":
      return t("devconsole.problems.segment", { name: value.name });
    case "excluded":
      return t("devconsole.problems.excluded");
    default:
      return t("devconsole.problems.body");
  }
});

const curl = computed(() => (selected.value !== null && request.value !== null ? curlCommand(globalThis.location.origin, selected.value.method, request.value) : ""));

function setValue(param: ConsoleParam, value: string) {
  values[paramKey(param)] = value;
}

function paramOptions(param: ConsoleParam) {
  return [{ value: "", label: t("devconsole.unset") }, ...param.options.map((option) => ({ value: option, label: option }))];
}

function paramLabel(param: ConsoleParam) {
  const where = { path: t("devconsole.where.path"), query: t("devconsole.where.query"), header: t("devconsole.where.header") }[param.in];
  return t(param.required ? "devconsole.paramRequired" : "devconsole.paramOptional", { name: param.name, where });
}

async function send() {
  const operation = selected.value;
  const value = request.value;
  if (operation === null || value === null || busy.value || (operation.dangerous && !acknowledged.value)) {
    return;
  }
  busy.value = true;
  failed.value = false;
  try {
    result.value = await sendConsoleRequest(client, operation.method, value);
  } catch {
    result.value = null;
    failed.value = true;
  } finally {
    busy.value = false;
    acknowledged.value = false;
  }
}

// Enter in a field sends only a read; a write always goes through the
// confirmation buttons.
function submit() {
  if (selected.value !== null && !selected.value.write) {
    void send();
  }
}

async function copy(text: string, done: string) {
  toasts.push((await copyText(text)) ? done : "devconsole.copyFailed", "info");
}
</script>

<template>
  <section class="jl-devconsole" aria-labelledby="devconsole-title">
    <template v-if="!ready">
      <UiSkeleton shape="title" width="40%" />
    </template>
    <template v-else>
      <h1 id="devconsole-title">{{ t("devconsole.title") }}</h1>
      <p>{{ t("devconsole.intro") }}</p>
      <div v-if="access === 'checking'" role="status" :aria-label="t('devconsole.checking')">
        <UiSkeleton shape="block" />
      </div>
      <UiAlert v-else-if="access !== 'granted'" tone="danger" data-testid="devconsole-unavailable">{{ t("devconsole.unavailable") }}</UiAlert>
      <UiAlert v-else-if="loadFailed" tone="danger">{{ t("devconsole.loadFailed") }}</UiAlert>
      <template v-else>
        <UiAlert tone="info">{{ t("devconsole.notice") }}</UiAlert>
        <form class="jl-devconsole__form" novalidate data-testid="devconsole-form" @submit.prevent="submit">
          <UiTextField v-model="filter" type="search" :label="t('devconsole.filter')" />
          <UiSelectField v-model="selectedId" :label="t('devconsole.operation')" :options="options" :hint="t('devconsole.operationHint', { count: operations.length })" />
          <p v-if="selected" class="jl-devconsole__badges">
            <UiBadge v-if="selected.devOnly">{{ t("devconsole.devOnly") }}</UiBadge>
            <UiBadge v-if="selected.dangerous" tone="danger">{{ t("devconsole.dangerous") }}</UiBadge>
            <UiBadge v-else-if="selected.write">{{ t("devconsole.write") }}</UiBadge>
          </p>

          <fieldset v-if="selected && selected.params.length > 0" class="jl-devconsole__params">
            <legend>{{ t("devconsole.params") }}</legend>
            <template v-for="param in selected.params" :key="paramKey(param)">
              <UiSelectField
                v-if="param.options.length > 0"
                :model-value="values[paramKey(param)] ?? ''"
                :label="paramLabel(param)"
                :options="paramOptions(param)"
                :hint="param.description || undefined"
                @update:model-value="setValue(param, $event)"
              />
              <UiTextField
                v-else :model-value="values[paramKey(param)] ?? ''"
                :label="paramLabel(param)" :required="param.required" :hint="param.description || undefined" @update:model-value="setValue(param, $event)"
              />
            </template>
          </fieldset>

          <div v-if="selected?.hasBody" class="jl-devconsole__field">
            <label :for="bodyId">{{ t("devconsole.body") }}</label>
            <textarea
              :id="bodyId"
              v-model="bodyText"
              class="jl-devconsole__code"
              rows="10"
              spellcheck="false"
              autocomplete="off"
              :aria-invalid="built !== null && 'kind' in built && built.kind === 'body' ? 'true' : undefined"
              :aria-describedby="bodyId + '-hint'"
            />
            <p :id="bodyId + '-hint'" class="jl-devconsole__muted">{{ t("devconsole.bodyHint") }}</p>
          </div>

          <UiAlert v-if="problem" tone="danger">{{ problem }}</UiAlert>

          <div v-if="selected" class="jl-devconsole__actions">
            <UiButton v-if="!selected.write" type="submit" :busy="busy" :disabled="problem !== null" data-testid="devconsole-send">{{ t("devconsole.send") }}</UiButton>
            <template v-else>
              <UiCheckbox v-if="selected.dangerous" v-model="acknowledged" :label="t('devconsole.acknowledge')" :hint="t('devconsole.acknowledgeHint')" />
              <UiConfirmButton
                :label="t('devconsole.sendWrite', { method: selected.method.toUpperCase() })"
                :confirm-label="t('devconsole.confirmSend', { method: selected.method.toUpperCase() })"
                :prompt="t('devconsole.confirmPrompt', { request: selected.id })"
                :busy="busy"
                :disabled="problem !== null || (selected.dangerous && !acknowledged)"
                :variant="selected.dangerous ? 'danger' : 'secondary'"
                @confirm="send"
              />
            </template>
            <UiButton variant="secondary" :disabled="curl === ''" data-testid="devconsole-copy-curl" @click="copy(curl, 'devconsole.curlCopied')">{{ t("devconsole.copyCurl") }}</UiButton>
          </div>

          <div v-if="curl" class="jl-devconsole__field">
            <p :id="curlId" class="jl-devconsole__muted">{{ t("devconsole.curlHint") }}</p>
            <pre class="jl-devconsole__code" :aria-describedby="curlId" data-testid="devconsole-curl">{{ curl }}</pre>
          </div>
        </form>

        <section class="jl-devconsole__result" aria-live="polite" :aria-busy="busy" :aria-label="t('devconsole.response')">
          <UiAlert v-if="failed" tone="danger">{{ t("devconsole.networkError") }}</UiAlert>
          <template v-if="result">
            <h2>{{ t("devconsole.response") }}</h2>
            <dl class="jl-devconsole__summary">
              <dt>{{ t("devconsole.status") }}</dt>
              <dd data-testid="devconsole-status">{{ result.status }} {{ result.statusText }}</dd>
              <dt>{{ t("devconsole.duration") }}</dt>
              <dd>{{ t("devconsole.milliseconds", { ms: result.durationMs }) }}</dd>
              <dt>{{ t("devconsole.traceId") }}</dt>
              <dd>
                <code data-testid="devconsole-trace">{{ result.traceId || t("devconsole.none") }}</code>
                <UiButton v-if="result.traceId" variant="ghost" @click="copy(result.traceId, 'devconsole.traceCopied')">{{ t("devconsole.copyTrace") }}</UiButton>
              </dd>
              <dt>{{ t("devconsole.size") }}</dt>
              <dd>{{ t("devconsole.bytes", { bytes: result.size }) }}</dd>
            </dl>
            <h3>{{ t("devconsole.headers") }}</h3>
            <table class="jl-devconsole__headers" data-testid="devconsole-headers">
              <thead>
                <tr>
                  <th scope="col">{{ t("devconsole.headerName") }}</th>
                  <th scope="col">{{ t("devconsole.headerValue") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="[name, value] in result.headers" :key="name">
                  <th scope="row">{{ name }}</th>
                  <td>{{ value }}</td>
                </tr>
              </tbody>
            </table>
            <h3>{{ t("devconsole.responseBody") }}</h3>
            <p v-if="result.body.kind === 'empty'" class="jl-devconsole__muted">{{ t("devconsole.emptyBody") }}</p>
            <p v-else-if="result.body.kind === 'binary'" class="jl-devconsole__muted">{{ t("devconsole.binaryBody", { bytes: result.size }) }}</p>
            <pre v-else class="jl-devconsole__code" tabindex="0" data-testid="devconsole-body">{{ result.body.text }}</pre>
          </template>
        </section>
      </template>
    </template>
  </section>
</template>

<style scoped>
.jl-devconsole,
.jl-devconsole__form,
.jl-devconsole__params,
.jl-devconsole__field,
.jl-devconsole__result {
  display: grid;
  gap: var(--jl-space-3);
  min-width: 0;
}

.jl-devconsole__params {
  margin: 0;
  padding: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-devconsole__badges,
.jl-devconsole__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
}

.jl-devconsole__code {
  overflow: auto;
  max-height: 32rem;
  margin: 0;
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font-family: ui-monospace, monospace;
  font-size: var(--jl-font-size-sm);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.jl-devconsole__muted {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-devconsole__summary {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: 0;
}

.jl-devconsole__summary dd {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
  overflow-wrap: anywhere;
}

.jl-devconsole__headers {
  border-collapse: collapse;
  font-size: var(--jl-font-size-sm);
}

.jl-devconsole__headers th,
.jl-devconsole__headers td {
  padding: var(--jl-space-1) var(--jl-space-2);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  overflow-wrap: anywhere;
}
</style>
