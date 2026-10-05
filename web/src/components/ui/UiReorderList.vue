<script setup lang="ts">
import { nextTick, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "./UiButton.vue";
import UiCheckbox from "./UiCheckbox.vue";

// Reorderable list (G33.5, G34.5). Every move works without a pointer: each
// row has "move up" and "move down" buttons, and Alt+ArrowUp/ArrowDown moves
// the row that holds focus. Dragging with a mouse is an extra. Focus stays
// with the moved row and a polite live region announces its new position.
export interface ReorderItem {
  readonly id: string;
  readonly label: string;
  readonly visible?: boolean;
}

const props = defineProps<{ items: readonly ReorderItem[]; label: string; toggleable?: boolean }>();
const emit = defineEmits<{ move: [from: number, to: number]; toggle: [id: string, visible: boolean] }>();
const { t } = useI18n();
const announcement = shallowRef("");
const dragging = shallowRef<number | null>(null);
const list = useTemplateRef<HTMLOListElement>("list");

async function move(from: number, to: number, direction: "up" | "down") {
  const item = props.items[from];
  if (item === undefined || to < 0 || to >= props.items.length) {
    return;
  }
  emit("move", from, to);
  announcement.value = t("layout.moved", { name: item.label, position: to + 1, total: props.items.length });
  await nextTick();
  // Keep focus on the moved row; at either end the pressed button is
  // disabled, so focus its counterpart.
  const row = [...(list.value?.querySelectorAll<HTMLElement>("[data-row]") ?? [])].find((element) => element.dataset.row === item.id);
  const preferred = row?.querySelector<HTMLButtonElement>(`[data-move="${direction}"]:not(:disabled)`);
  (preferred ?? row?.querySelector<HTMLButtonElement>("[data-move]:not(:disabled)"))?.focus();
}

function onKeydown(event: KeyboardEvent, index: number) {
  if (!event.altKey || (event.key !== "ArrowUp" && event.key !== "ArrowDown")) {
    return;
  }
  event.preventDefault();
  const up = event.key === "ArrowUp";
  void move(index, up ? index - 1 : index + 1, up ? "up" : "down");
}

function onDragStart(event: DragEvent, index: number) {
  dragging.value = index;
  event.dataTransfer?.setData("text/plain", String(index));
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
  }
}

function onDrop(index: number) {
  const from = dragging.value;
  dragging.value = null;
  if (from !== null && from !== index) {
    void move(from, index, from > index ? "up" : "down");
  }
}
</script>

<template>
  <div class="jl-reorder">
    <p class="jl-reorder__hint">{{ t("layout.keyboardHint") }}</p>
    <ol ref="list" class="jl-reorder__list" :aria-label="label">
      <li
        v-for="(item, index) in items"
        :key="item.id"
        class="jl-reorder__row"
        :class="{ 'jl-reorder__row--dragging': dragging === index, 'jl-reorder__row--hidden': item.visible === false }"
        :data-row="item.id"
        draggable="true"
        @dragstart="onDragStart($event, index)"
        @dragover.prevent
        @dragend="dragging = null"
        @drop.prevent="onDrop(index)"
        @keydown="onKeydown($event, index)"
      >
        <span class="jl-reorder__handle" aria-hidden="true">⠿</span>
        <UiCheckbox
          v-if="toggleable"
          class="jl-reorder__label"
          :label="item.label"
          :model-value="item.visible !== false"
          @update:model-value="(value: boolean) => emit('toggle', item.id, value)"
        />
        <span v-else class="jl-reorder__label">{{ item.label }}</span>
        <span class="jl-reorder__buttons">
          <UiButton
            variant="ghost"
            data-move="up"
            :disabled="index === 0"
            :aria-label="t('layout.moveUp', { name: item.label })"
            aria-keyshortcuts="Alt+ArrowUp"
            @click="move(index, index - 1, 'up')"
          >
            <span aria-hidden="true">↑</span>
          </UiButton>
          <UiButton
            variant="ghost"
            data-move="down"
            :disabled="index === items.length - 1"
            :aria-label="t('layout.moveDown', { name: item.label })"
            aria-keyshortcuts="Alt+ArrowDown"
            @click="move(index, index + 1, 'down')"
          >
            <span aria-hidden="true">↓</span>
          </UiButton>
        </span>
      </li>
    </ol>
    <p class="jl-visually-hidden" role="status" aria-live="polite">{{ announcement }}</p>
  </div>
</template>

<style scoped>
.jl-reorder__hint {
  margin: 0 0 var(--jl-space-2);
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-reorder__list {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-reorder__row {
  display: flex;
  align-items: center;
  gap: var(--jl-space-2);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-reorder__row--dragging {
  opacity: 0.6;
}

.jl-reorder__row--hidden .jl-reorder__label {
  color: var(--jl-color-text-muted);
}

.jl-reorder__handle {
  cursor: grab;
  color: var(--jl-color-text-muted);
}

.jl-reorder__label {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}

.jl-reorder__buttons {
  display: flex;
  gap: var(--jl-space-1);
}
</style>
