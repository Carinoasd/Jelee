import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  deleteItemRule,
  getContentAccess,
  putContentAccess,
  putContentWindows,
  putItemRule,
  searchItems,
  type AccessWindow,
  type ContentAccess,
  type ContentAccessView,
  type ItemAccessEffect,
} from "@/features/users/api";
import { resetOnUserChange } from "./userScoped";

// Content access of one account (G48.7): rating ceiling, unrated handling,
// blocked tags and keywords, time windows and item rules, plus the item
// search used to add a rule.
export const useUserAdminContentStore = defineStore("userAdminContent", () => {
  const { client } = useApi();
  const userId = shallowRef("");
  const access = shallowRef<ContentAccessView | null>(null);
  const saving = shallowRef(false);
  const savingWindows = shallowRef(false);
  /** Item IDs whose rule is being written or removed. */
  const pendingRules = shallowRef<ReadonlySet<string>>(new Set());
  const query = shallowRef("");

  const accessRequest = useRequest(async () => {
    access.value = await getContentAccess(client, userId.value);
    return access.value;
  });
  const searchRequest = useRequest(
    async () => searchItems(client, query.value),
    (items) => items.length === 0,
  );

  function reset() {
    userId.value = "";
    access.value = null;
    saving.value = false;
    savingWindows.value = false;
    pendingRules.value = new Set();
    query.value = "";
    accessRequest.reset();
    searchRequest.reset();
  }

  async function open(id: string): Promise<void> {
    if (id !== userId.value) {
      reset();
      userId.value = id;
    }
    await accessRequest.run();
  }

  async function save(next: ContentAccess): Promise<void> {
    const target = userId.value;
    saving.value = true;
    try {
      access.value = await putContentAccess(client, target, next);
    } finally {
      saving.value = false;
    }
  }

  async function saveWindows(windows: readonly AccessWindow[]): Promise<void> {
    const target = userId.value;
    savingWindows.value = true;
    try {
      access.value = await putContentWindows(client, target, windows);
    } finally {
      savingWindows.value = false;
    }
  }

  function setPending(itemId: string, on: boolean) {
    const next = new Set(pendingRules.value);
    if (on) {
      next.add(itemId);
    } else {
      next.delete(itemId);
    }
    pendingRules.value = next;
  }

  async function withRule<T>(itemId: string, work: () => Promise<T>): Promise<T> {
    setPending(itemId, true);
    try {
      return await work();
    } finally {
      setPending(itemId, false);
    }
  }

  /** Creates or replaces the rule on an item; a replaced rule keeps its place. */
  async function setRule(itemId: string, effect: ItemAccessEffect): Promise<void> {
    const target = userId.value;
    const rule = await withRule(itemId, () => putItemRule(client, target, itemId, effect));
    const current = access.value;
    if (current !== null) {
      const exists = current.rules.some((entry) => entry.itemId === rule.itemId);
      const rules = exists
        ? current.rules.map((entry) => (entry.itemId === rule.itemId ? rule : entry))
        : [...current.rules, rule];
      access.value = { ...current, rules };
    }
  }

  async function removeRule(itemId: string): Promise<void> {
    const target = userId.value;
    await withRule(itemId, () => deleteItemRule(client, target, itemId));
    const current = access.value;
    if (current !== null) {
      access.value = { ...current, rules: current.rules.filter((rule) => rule.itemId !== itemId) };
    }
  }

  async function search(text: string): Promise<void> {
    query.value = text.trim();
    if (query.value === "") {
      searchRequest.reset();
      return;
    }
    await searchRequest.run();
  }

  resetOnUserChange(reset);

  return {
    userId,
    access,
    saving,
    savingWindows,
    pendingRules,
    accessState: accessRequest.state,
    searchState: searchRequest.state,
    open,
    reload: accessRequest.run,
    save,
    saveWindows,
    setRule,
    removeRule,
    search,
    reset,
  };
});
