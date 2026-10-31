<script setup lang="ts">
import { shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import UiButton from "@/components/ui/UiButton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import { setFavorite } from "./api";

// Marks or unmarks the item as one of the caller's favourites (G24.2). A
// toggle button: the label stays, aria-pressed carries the state.
const props = defineProps<{ itemId: string; favorite: boolean }>();
const { t } = useI18n();
const { client } = useApi();
const toasts = useToastStore();
const pressed = shallowRef(props.favorite);
const busy = shallowRef(false);

watch(
  () => props.favorite,
  (value) => {
    pressed.value = value;
  },
);

async function toggle() {
  busy.value = true;
  try {
    pressed.value = (await setFavorite(client, props.itemId, !pressed.value)).isFavorite;
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <UiButton variant="secondary" :pressed="pressed" :busy="busy" data-testid="favorite-toggle" @click="toggle">
    <span aria-hidden="true">{{ pressed ? "★" : "☆" }}</span>
    {{ t("items.detail.favorite") }}
  </UiButton>
</template>
