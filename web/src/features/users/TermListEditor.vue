<script setup lang="ts">
import { shallowRef, useId } from "vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";

// Editable list of short terms shown as chips (blocked tags, blocked
// keywords). Every text is passed in already translated; check returns the
// problem with a term to add, or null.
const props = defineProps<{
  title: string;
  impact: string;
  emptyText: string;
  inputLabel: string;
  addLabel: string;
  removeText: string;
  removeLabel: (term: string) => string;
  check: (term: string, existing: readonly string[]) => string | null;
}>();
const terms = defineModel<readonly string[]>({ required: true });
const titleId = useId();
const entry = shallowRef("");
const problem = shallowRef<string | null>(null);

function add() {
  problem.value = props.check(entry.value, terms.value);
  if (problem.value !== null) {
    return;
  }
  terms.value = [...terms.value, entry.value.trim()];
  entry.value = "";
}

function remove(term: string) {
  terms.value = terms.value.filter((existing) => existing !== term);
}
</script>

<template>
  <div class="jl-terms">
    <h3 :id="titleId">{{ title }}</h3>
    <p class="jl-terms__muted">{{ impact }}</p>
    <ul v-if="terms.length > 0" class="jl-terms__list" :aria-labelledby="titleId">
      <li v-for="term in terms" :key="term" class="jl-terms__term">
        <span>{{ term }}</span>
        <UiButton variant="ghost" :aria-label="removeLabel(term)" @click="remove(term)">{{ removeText }}</UiButton>
      </li>
    </ul>
    <p v-else class="jl-terms__muted">{{ emptyText }}</p>
    <form class="jl-terms__add" novalidate @submit.prevent="add">
      <UiTextField v-model="entry" :label="inputLabel" :error="problem" autocomplete="off" />
      <UiButton type="submit" variant="secondary">{{ addLabel }}</UiButton>
    </form>
  </div>
</template>

<style scoped>
.jl-terms {
  display: grid;
  gap: var(--jl-space-2);
}

.jl-terms h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-terms__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-terms__list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-terms__term {
  display: inline-flex;
  align-items: center;
  gap: var(--jl-space-1);
  max-width: 100%;
  padding-left: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-badge-bg);
  overflow-wrap: anywhere;
}

.jl-terms__add {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--jl-space-2);
}

.jl-terms__add > :first-child {
  flex: 1 1 16rem;
}
</style>
