import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  createNetworkRule,
  deleteNetworkRule,
  listNetworkRules,
  updateNetworkRule,
  type NetworkRule,
  type NetworkRuleInput,
} from "@/features/access/api";
import { resetOnUserChange } from "./userScoped";

function byLibrary(a: NetworkRule, b: NetworkRule): number {
  return a.libraryName.localeCompare(b.libraryName) || (a.createdAt < b.createdAt ? -1 : a.createdAt > b.createdAt ? 1 : 0);
}

/** Network rules of the libraries, grouped by library name. */
export const useNetworkRulesStore = defineStore("networkRules", () => {
  const { client } = useApi();
  const rules = shallowRef<readonly NetworkRule[]>([]);
  /** IDs of rules with a change in flight. */
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  const request = useRequest(
    async () => {
      rules.value = [...(await listNetworkRules(client))].sort(byLibrary);
      return rules.value;
    },
    (data) => data.length === 0,
  );

  function setPending(id: string, on: boolean) {
    const next = new Set(pending.value);
    if (on) {
      next.add(id);
    } else {
      next.delete(id);
    }
    pending.value = next;
  }

  function put(rule: NetworkRule) {
    rules.value = [...rules.value.filter((entry) => entry.id !== rule.id), rule].sort(byLibrary);
  }

  async function create(input: NetworkRuleInput): Promise<NetworkRule> {
    const rule = await createNetworkRule(client, input);
    put(rule);
    if (request.state.value.status !== "success") {
      await request.run();
    }
    return rule;
  }

  async function update(id: string, input: NetworkRuleInput): Promise<NetworkRule> {
    setPending(id, true);
    try {
      const rule = await updateNetworkRule(client, id, input);
      put(rule);
      return rule;
    } finally {
      setPending(id, false);
    }
  }

  async function remove(id: string): Promise<void> {
    setPending(id, true);
    try {
      await deleteNetworkRule(client, id);
      rules.value = rules.value.filter((entry) => entry.id !== id);
      if (rules.value.length === 0) {
        await request.run();
      }
    } finally {
      setPending(id, false);
    }
  }

  function reset() {
    rules.value = [];
    pending.value = new Set();
    request.reset();
  }

  resetOnUserChange(reset);

  return { rules, pending, state: request.state, load: request.run, create, update, remove, reset };
});
