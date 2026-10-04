import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  blockKnownClient,
  kickKnownClient,
  listKnownClients,
  updateKnownClient,
  type ClientRule,
  type KnownClient,
  type KnownClientUpdate,
} from "@/features/clients/api";
import { resetOnUserChange } from "./userScoped";

/** Clients seen on authenticated requests, most recently seen first. */
export const useKnownClientsStore = defineStore("clientsKnown", () => {
  const { client } = useApi();
  const clients = shallowRef<readonly KnownClient[]>([]);
  const nextCursor = shallowRef("");
  const pending = shallowRef<ReadonlySet<string>>(new Set());
  let generation = 0;

  // A page started before a reload is dropped instead of being appended.
  async function loadPage(append: boolean) {
    const current = append ? generation : ++generation;
    const page = await listKnownClients(client, append ? nextCursor.value : "");
    if (current === generation) {
      clients.value = append ? [...clients.value, ...page.clients] : page.clients;
      nextCursor.value = page.pagination.nextCursor;
    }
    return clients.value;
  }

  const firstPage = useRequest(
    () => loadPage(false),
    (list) => list.length === 0,
  );
  const morePages = useRequest(() => loadPage(true));

  function setPending(id: string, on: boolean) {
    const next = new Set(pending.value);
    if (on) {
      next.add(id);
    } else {
      next.delete(id);
    }
    pending.value = next;
  }

  async function withPending<T>(id: string, work: () => Promise<T>): Promise<T> {
    setPending(id, true);
    try {
      return await work();
    } finally {
      setPending(id, false);
    }
  }

  function update(id: string, change: KnownClientUpdate): Promise<KnownClient> {
    return withPending(id, async () => {
      const updated = await updateKnownClient(client, id, change);
      clients.value = clients.value.map((entry) => (entry.id === id ? updated : entry));
      return updated;
    });
  }

  /** Adds the block rule; the client then reports itself blocked by it. */
  function block(id: string): Promise<ClientRule> {
    return withPending(id, async () => {
      const rule = await blockKnownClient(client, id);
      clients.value = clients.value.map((entry) => (entry.id === id ? { ...entry, blocked: true, blockRuleId: rule.id } : entry));
      return rule;
    });
  }

  /** Revokes the client's sessions; resolves to the number revoked. */
  function kick(id: string): Promise<number> {
    return withPending(id, async () => {
      const revoked = await kickKnownClient(client, id);
      clients.value = clients.value.map((entry) => (entry.id === id ? { ...entry, activeSessions: 0 } : entry));
      return revoked;
    });
  }

  function reset() {
    generation++;
    clients.value = [];
    nextCursor.value = "";
    pending.value = new Set();
    firstPage.reset();
    morePages.reset();
  }

  resetOnUserChange(reset);

  return {
    clients,
    nextCursor,
    pending,
    state: firstPage.state,
    moreState: morePages.state,
    load: firstPage.run,
    loadMore: morePages.run,
    update,
    block,
    kick,
    reset,
  };
});
