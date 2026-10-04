<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, useId, useTemplateRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import { normalizeQuery, searchMaxLength } from "./api";

// Header search field. On the search page typing updates the results after a
// short pause (debounced, the URL keeps the query); elsewhere Enter opens the
// search page, so focus is never pulled away while typing. "/" focuses the
// field from anywhere outside another text field; Escape clears it.
const props = withDefaults(defineProps<{ debounceMs?: number }>(), { debounceMs: 300 });
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const id = useId();
const text = shallowRef("");
const input = useTemplateRef<HTMLInputElement>("input");
let timer: ReturnType<typeof setTimeout> | undefined;

watch(
  () => (route.name === "search" ? route.query.q : undefined),
  (q) => {
    if (route.name === "search" && normalizeQuery(q) !== normalizeQuery(text.value)) {
      text.value = typeof q === "string" ? q : "";
    }
  },
  { immediate: true },
);

function cancelPending() {
  if (timer !== undefined) {
    clearTimeout(timer);
    timer = undefined;
  }
}

function go(replace: boolean) {
  cancelPending();
  const q = normalizeQuery(text.value);
  const query = { ...(route.name === "search" ? route.query : {}), q: q === "" ? undefined : q };
  const target = { name: "search", query };
  void (replace ? router.replace(target) : router.push(target));
}

function onInput() {
  if (route.name !== "search") {
    return;
  }
  cancelPending();
  timer = setTimeout(() => {
    go(true);
  }, props.debounceMs);
}

function onEscape() {
  if (text.value !== "") {
    text.value = "";
    onInput();
  }
}

function isTextTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  return target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
}

function onGlobalKey(event: KeyboardEvent) {
  if (event.key === "/" && !event.ctrlKey && !event.metaKey && !event.altKey && !isTextTarget(event.target)) {
    event.preventDefault();
    input.value?.focus();
  }
}

onMounted(() => {
  document.addEventListener("keydown", onGlobalKey);
});
onBeforeUnmount(() => {
  cancelPending();
  document.removeEventListener("keydown", onGlobalKey);
});
</script>

<template>
  <form role="search" class="jl-search-box" @submit.prevent="go(route.name === 'search')">
    <label :for="id" class="jl-visually-hidden">{{ t("search.label") }}</label>
    <input
      :id="id"
      ref="input"
      v-model="text"
      class="jl-search-box__input"
      type="search"
      enterkeyhint="search"
      autocomplete="off"
      :maxlength="searchMaxLength"
      :placeholder="t('search.placeholder')"
      aria-keyshortcuts="/"
      @input="onInput"
      @keydown.esc.prevent="onEscape"
    />
    <button type="submit" class="jl-search-box__submit">
      <span class="jl-visually-hidden">{{ t("common.search") }}</span>
      <span aria-hidden="true">⌕</span>
    </button>
  </form>
</template>

<style scoped>
.jl-search-box {
  display: flex;
  align-items: stretch;
  min-width: 0;
}

.jl-search-box__input {
  flex: 1;
  min-width: 8rem;
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-right: none;
  border-radius: var(--jl-radius-sm) 0 0 var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-search-box__submit {
  min-width: var(--jl-touch-target);
  border: 1px solid var(--jl-color-border);
  border-radius: 0 var(--jl-radius-sm) var(--jl-radius-sm) 0;
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font-size: var(--jl-font-size-lg);
  cursor: pointer;
}
</style>
