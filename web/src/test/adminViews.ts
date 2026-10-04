// Test-only lookups shared by the users and access view tests. Not imported
// by application code.
import type { DOMWrapper, VueWrapper } from "@vue/test-utils";
import type { Pinia } from "pinia";
import { useToastStore } from "@/stores/toasts";

/** The button whose visible text is exactly text. */
export function button(wrapper: VueWrapper, text: string): DOMWrapper<HTMLButtonElement> {
  const found = wrapper.findAll("button").find((candidate) => candidate.text() === text);
  if (found === undefined) {
    throw new Error("no button " + text);
  }
  return found;
}

export function hasButton(wrapper: VueWrapper, text: string): boolean {
  return wrapper.findAll("button").some((candidate) => candidate.text() === text);
}

/** The form control labelled by a <label for> with exactly this text. */
export function control<T extends Element = HTMLInputElement>(wrapper: VueWrapper, label: string): DOMWrapper<T> {
  const labelElement = wrapper.findAll("label").find((candidate) => candidate.text() === label);
  const id = labelElement?.attributes("for");
  if (id === undefined) {
    throw new Error("no control labelled " + label);
  }
  return wrapper.find<T>(`[id="${id}"]`);
}

/** Catalog keys of the toasts pushed so far (the toast region lives in App.vue). */
export function toastKeys(wrapper: VueWrapper): string[] {
  const pinia = (wrapper.vm as unknown as { $pinia: Pinia }).$pinia;
  return useToastStore(pinia).toasts.map((toast) => toast.key);
}
