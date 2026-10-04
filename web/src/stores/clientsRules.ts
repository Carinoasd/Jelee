import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  createRule,
  deleteRule,
  enforceRule,
  listRules,
  observeRule,
  updateRule,
  type ClientRule,
  type ClientRuleInput,
} from "@/features/clients/api";
import { resetOnUserChange } from "./userScoped";

function byPrecedence(a: ClientRule, b: ClientRule): number {
  return b.priority - a.priority || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
}

/** Client rules in precedence order, with the observe/enforce switch. */
export const useClientRulesStore = defineStore("clientsRules", () => {
  const { client } = useApi();
  const rules = shallowRef<readonly ClientRule[]>([]);
  /** IDs of rules with a change in flight. */
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  const request = useRequest(
    async () => {
      rules.value = await listRules(client);
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

  function put(rule: ClientRule) {
    rules.value = [...rules.value.filter((entry) => entry.id !== rule.id), rule].sort(byPrecedence);
  }

  async function withPending<T>(id: string, work: () => Promise<T>): Promise<T> {
    setPending(id, true);
    try {
      return await work();
    } finally {
      setPending(id, false);
    }
  }

  async function create(input: ClientRuleInput): Promise<ClientRule> {
    const rule = await createRule(client, input);
    put(rule);
    return rule;
  }

  function update(id: string, input: ClientRuleInput): Promise<ClientRule> {
    return withPending(id, async () => {
      const rule = await updateRule(client, id, input);
      put(rule);
      return rule;
    });
  }

  function remove(id: string): Promise<void> {
    return withPending(id, async () => {
      await deleteRule(client, id);
      rules.value = rules.value.filter((entry) => entry.id !== id);
    });
  }

  /** Observe/shadow -> enforcing; callers must have confirmed first. */
  function enforce(id: string): Promise<ClientRule> {
    return withPending(id, async () => {
      const rule = await enforceRule(client, id);
      put(rule);
      return rule;
    });
  }

  function observe(id: string): Promise<ClientRule> {
    return withPending(id, async () => {
      const rule = await observeRule(client, id);
      put(rule);
      return rule;
    });
  }

  function reset() {
    rules.value = [];
    pending.value = new Set();
    request.reset();
  }

  resetOnUserChange(reset);

  return { rules, pending, state: request.state, load: request.run, create, update, remove, enforce, observe, reset };
});
