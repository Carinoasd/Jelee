import { defineStore } from "pinia";
import { shallowRef } from "vue";

export type ToastTone = "info" | "success" | "danger";

/** A transient notice; messages are catalog keys, rendered in the current locale. */
export interface Toast {
  readonly id: number;
  readonly key: string;
  readonly params: Readonly<Record<string, string | number>>;
  readonly tone: ToastTone;
}

/** Long enough to read two lines; errors stay until dismissed. */
export const toastTimeoutMs = 6000;
const maxToasts = 4;

export const useToastStore = defineStore("toasts", () => {
  const toasts = shallowRef<readonly Toast[]>([]);
  let nextId = 1;

  function dismiss(id: number): void {
    toasts.value = toasts.value.filter((toast) => toast.id !== id);
  }

  function push(key: string, tone: ToastTone = "info", params: Record<string, string | number> = {}): number {
    const id = nextId++;
    toasts.value = [...toasts.value, { id, key, params, tone }].slice(-maxToasts);
    if (tone !== "danger") {
      setTimeout(() => {
        dismiss(id);
      }, toastTimeoutMs);
    }
    return id;
  }

  function clear(): void {
    toasts.value = [];
  }

  return { toasts, push, dismiss, clear };
});
