import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  createWebhook,
  deleteWebhook,
  listWebhooks,
  rotateWebhookSecret,
  testWebhook,
  toInput,
  updateWebhook,
  type Webhook,
  type WebhookEvent,
  type WebhookInput,
  type WebhookList,
  type WebhookTestResult,
} from "@/features/webhooks/api";
import { resetOnUserChange } from "./userScoped";

/** A signing secret the server returned once, until the user dismisses it. */
export interface RevealedSecret {
  readonly webhookId: string;
  readonly name: string;
  readonly secret: string;
}

/** Webhook endpoints, the event catalog and the one-time secret notice. */
export const useWebhooksStore = defineStore("webhooks", () => {
  const { client } = useApi();
  const webhooks = shallowRef<readonly Webhook[]>([]);
  const events = shallowRef<readonly WebhookEvent[]>([]);
  // Lives only here, in memory: never persisted, cleared when dismissed,
  // when the view that showed it unmounts and when the user changes.
  const secret = shallowRef<RevealedSecret | null>(null);
  const testResults = shallowRef<ReadonlyMap<string, WebhookTestResult>>(new Map());
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  const request = useRequest<WebhookList>(
    async () => {
      const list = await listWebhooks(client);
      webhooks.value = list.webhooks;
      events.value = list.events;
      return list;
    },
    (list) => list.webhooks.length === 0,
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

  async function withPending<T>(id: string, work: () => Promise<T>): Promise<T> {
    setPending(id, true);
    try {
      return await work();
    } finally {
      setPending(id, false);
    }
  }

  /** Replaces or appends one endpoint in the cached list. */
  function put(webhook: Webhook) {
    const known = webhooks.value.some((entry) => entry.id === webhook.id);
    webhooks.value = known ? webhooks.value.map((entry) => (entry.id === webhook.id ? webhook : entry)) : [...webhooks.value, webhook];
  }

  /** Loads the list unless it is already there (the detail view needs the catalog). */
  async function ensureLoaded() {
    if (request.state.value.status === "idle" || request.state.value.status === "error") {
      await request.run();
    }
  }

  async function create(input: WebhookInput): Promise<Webhook> {
    const created = await createWebhook(client, input);
    put(created.webhook);
    secret.value = { webhookId: created.webhook.id, name: created.webhook.name, secret: created.secret };
    if (request.state.value.status === "empty") {
      await request.run();
    }
    return created.webhook;
  }

  function update(id: string, input: WebhookInput): Promise<Webhook> {
    return withPending(id, async () => {
      const updated = await updateWebhook(client, id, input);
      put(updated);
      return updated;
    });
  }

  /** Enables or disables an endpoint by replacing its settings (headers kept). */
  function setEnabled(webhook: Webhook, enabled: boolean): Promise<Webhook> {
    return update(webhook.id, { ...toInput(webhook), enabled });
  }

  function remove(id: string): Promise<void> {
    return withPending(id, async () => {
      await deleteWebhook(client, id);
      webhooks.value = webhooks.value.filter((entry) => entry.id !== id);
      if (webhooks.value.length === 0) {
        await request.run();
      }
    });
  }

  function rotate(id: string, graceSeconds: number): Promise<Webhook> {
    return withPending(id, async () => {
      const rotated = await rotateWebhookSecret(client, id, graceSeconds);
      put(rotated.webhook);
      secret.value = { webhookId: rotated.webhook.id, name: rotated.webhook.name, secret: rotated.secret };
      return rotated.webhook;
    });
  }

  function test(id: string): Promise<WebhookTestResult> {
    return withPending(id, async () => {
      const result = await testWebhook(client, id);
      testResults.value = new Map(testResults.value).set(id, result);
      return result;
    });
  }

  function dismissSecret() {
    secret.value = null;
  }

  function reset() {
    webhooks.value = [];
    events.value = [];
    secret.value = null;
    testResults.value = new Map();
    pending.value = new Set();
    request.reset();
  }

  resetOnUserChange(reset);

  return {
    webhooks,
    events,
    secret,
    testResults,
    pending,
    state: request.state,
    load: request.run,
    ensureLoaded,
    put,
    create,
    update,
    setEnabled,
    remove,
    rotate,
    test,
    dismissSecret,
    reset,
  };
});
