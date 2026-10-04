<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { ApiError, networkError } from "@/api/errors";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useToastStore } from "@/stores/toasts";
import type { MediaSourceInfo } from "./api";
import {
  getVersionOverview,
  liftExclusion,
  mergeItem,
  setMainVersion,
  splitVersion,
  undoOperation,
  type VersionOperation,
  type VersionOverview,
} from "./versions";

// Administrator version decisions (G20.3, G20.5). Every change needs a second
// press; each one is audited on the server and can be undone from the list
// below for 30 days, newest first.
const props = defineProps<{ itemId: string; sources: readonly MediaSourceInfo[] }>();
const emit = defineEmits<{ changed: [] }>();
const { t, locale } = useI18n();
const { client } = useApi();
const toasts = useToastStore();
const overview = shallowRef<VersionOverview | null>(null);
const busy = shallowRef(false);
const exclude = shallowRef(true);
const mergeId = shallowRef("");

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const mergeError = computed(() => (mergeId.value.trim() === "" || uuidPattern.test(mergeId.value.trim()) ? null : t("versions.panel.mergeInvalid")));

const kindKey = {
  split: "versions.panel.kindSplit",
  merge: "versions.panel.kindMerge",
  primary: "versions.panel.kindMain",
  unexclude: "versions.panel.kindUnexclude",
} as const;
const blockedKey = {
  undone: "versions.panel.blockedUndone",
  expired: "versions.panel.blockedExpired",
  later_operation: "versions.panel.blockedLater",
} as const;

function versionName(source: MediaSourceInfo, index: number): string {
  const name = source.version.displayName;
  return name !== "" ? name : t("items.files.version", { n: index + 1 });
}

async function load() {
  try {
    overview.value = await getVersionOverview(client, props.itemId);
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  }
}

async function run(action: () => Promise<VersionOperation>, message: string) {
  busy.value = true;
  try {
    await action();
    toasts.push(message, "success");
    emit("changed");
    await load();
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  } finally {
    busy.value = false;
  }
}

const split = (source: MediaSourceInfo) => run(() => splitVersion(client, props.itemId, source.id, exclude.value), "versions.panel.splitDone");
const makeMain = (source: MediaSourceInfo) => run(() => setMainVersion(client, props.itemId, source.id), "versions.panel.mainDone");
const clearMain = () => run(() => setMainVersion(client, props.itemId, null), "versions.panel.mainDone");
const lift = (id: string) => run(() => liftExclusion(client, props.itemId, id), "versions.panel.liftDone");
const undo = (operation: VersionOperation) => run(() => undoOperation(client, operation.id), "versions.panel.undoDone");
async function merge() {
  const id = mergeId.value.trim();
  if (id === "" || mergeError.value !== null) {
    return;
  }
  await run(() => mergeItem(client, props.itemId, id), "versions.panel.mergeDone");
  mergeId.value = "";
}

onMounted(load);
</script>

<template>
  <section class="jl-versions" aria-labelledby="versions-title">
    <h3 id="versions-title" class="jl-versions__title">{{ t("versions.panel.title") }}</h3>
    <p class="jl-versions__muted">{{ t("versions.panel.intro") }}</p>
    <ul class="jl-versions__list">
      <li v-for="(source, index) in sources" :id="'version-' + source.id" :key="source.id" class="jl-versions__row">
        <span class="jl-versions__name">{{ versionName(source, index) }}</span>
        <UiBadge v-if="source.primary" tone="accent">{{ t("versions.panel.main") }}</UiBadge>
        <UiConfirmButton
          v-if="!source.primary"
          variant="secondary"
          :label="t('versions.panel.makeMain')"
          :confirm-label="t('versions.panel.makeMainConfirm')"
          :prompt="t('versions.panel.makeMainPrompt')"
          :busy="busy"
          :describedby="'version-' + source.id"
          @confirm="makeMain(source)"
        />
        <UiConfirmButton
          v-else
          variant="secondary"
          :label="t('versions.panel.clearMain')"
          :confirm-label="t('versions.panel.clearMainConfirm')"
          :busy="busy"
          :describedby="'version-' + source.id"
          @confirm="clearMain"
        />
        <UiConfirmButton
          :label="t('versions.panel.split')"
          :confirm-label="t('versions.panel.splitConfirm')"
          :prompt="t(exclude ? 'versions.panel.splitPromptExclude' : 'versions.panel.splitPrompt')"
          :busy="busy"
          :disabled="sources.length < 2"
          :describedby="'version-' + source.id"
          @confirm="split(source)"
        />
      </li>
    </ul>
    <UiCheckbox v-model="exclude" :label="t('versions.panel.exclude')" :hint="t('versions.panel.excludeHint')" />

    <div class="jl-versions__merge">
      <UiTextField v-model="mergeId" :label="t('versions.panel.mergeLabel')" :hint="t('versions.panel.mergeHint')" :error="mergeError" :maxlength="36" />
      <UiConfirmButton
        :label="t('versions.panel.merge')"
        :confirm-label="t('versions.panel.mergeConfirm')"
        :prompt="t('versions.panel.mergePrompt')"
        :busy="busy"
        :disabled="mergeId.trim() === '' || mergeError !== null"
        @confirm="merge"
      />
    </div>

    <template v-if="overview">
      <h4 class="jl-versions__subtitle">{{ t("versions.panel.exclusions") }}</h4>
      <p v-if="overview.exclusions.length === 0" class="jl-versions__muted">{{ t("versions.panel.noExclusions") }}</p>
      <ul v-else class="jl-versions__list">
        <li v-for="entry in overview.exclusions" :id="'exclusion-' + entry.id" :key="entry.id" class="jl-versions__row">
          <span class="jl-versions__name">{{ entry.fileName }}</span>
          <UiConfirmButton
            variant="secondary"
            :label="t('versions.panel.lift')"
            :confirm-label="t('versions.panel.liftConfirm')"
            :busy="busy"
            :describedby="'exclusion-' + entry.id"
            @confirm="lift(entry.id)"
          />
        </li>
      </ul>

      <h4 class="jl-versions__subtitle">{{ t("versions.panel.history") }}</h4>
      <p v-if="overview.operations.length === 0" class="jl-versions__muted">{{ t("versions.panel.noHistory") }}</p>
      <ul v-else class="jl-versions__list">
        <li v-for="operation in overview.operations" :id="'operation-' + operation.id" :key="operation.id" class="jl-versions__row">
          <span class="jl-versions__name">{{ t(kindKey[operation.kind]) }}</span>
          <span class="jl-versions__muted">{{ formatDateTime(operation.createdAt, locale) }}</span>
          <UiConfirmButton
            v-if="operation.undoable"
            :label="t('versions.panel.undo')"
            :confirm-label="t('versions.panel.undoConfirm')"
            :prompt="t('versions.panel.undoPrompt', { date: formatDateTime(operation.undoUntil, locale) })"
            :busy="busy"
            :describedby="'operation-' + operation.id"
            @confirm="undo(operation)"
          />
          <span v-else-if="operation.undoBlocked" class="jl-versions__muted">{{ t(blockedKey[operation.undoBlocked]) }}</span>
        </li>
      </ul>
    </template>
  </section>
</template>

<style scoped>
.jl-versions {
  display: grid;
  gap: var(--jl-space-2);
  margin-top: var(--jl-space-4);
}

.jl-versions__title {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-versions__subtitle {
  margin: var(--jl-space-3) 0 0;
  font-size: var(--jl-font-size-sm);
}

.jl-versions__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-versions__list {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-versions__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
}

.jl-versions__name {
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-versions__merge {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: var(--jl-space-2);
}
</style>
