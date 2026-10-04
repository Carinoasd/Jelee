import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest, type RequestHandle, type RequestState } from "@/api/requestState";
import {
  getDelivery,
  getWebhook,
  listDeliveries,
  replayDelivery,
  type DeliveryState,
  type Webhook,
  type WebhookDelivery,
  type WebhookDeliveryDetail,
} from "@/features/webhooks/api";
import { resetOnUserChange } from "./userScoped";

/** One webhook endpoint with its delivery log (G12). */
export const useWebhookDetailStore = defineStore("webhookDetail", () => {
  const { client } = useApi();
  const webhookId = shallowRef("");
  const webhook = shallowRef<Webhook | null>(null);
  const filter = shallowRef<DeliveryState | "">("");
  const deliveries = shallowRef<readonly WebhookDelivery[]>([]);
  const nextCursor = shallowRef("");
  const details = shallowRef<ReadonlyMap<string, RequestHandle<WebhookDeliveryDetail>>>(new Map());
  const replaying = shallowRef<ReadonlySet<string>>(new Set());
  let generation = 0;

  const endpoint = useRequest(async () => {
    const id = webhookId.value;
    const loaded = await getWebhook(client, id);
    if (id === webhookId.value) {
      webhook.value = loaded;
    }
    return loaded;
  });

  // Pages started for another endpoint, filter or before a reload are dropped.
  async function loadPage(append: boolean) {
    const current = append ? generation : ++generation;
    const page = await listDeliveries(client, webhookId.value, filter.value, append ? nextCursor.value : "");
    if (current === generation) {
      deliveries.value = append ? [...deliveries.value, ...page.deliveries] : page.deliveries;
      nextCursor.value = page.pagination.nextCursor;
    }
    return deliveries.value;
  }

  const firstPage = useRequest(
    () => loadPage(false),
    (list) => list.length === 0,
  );
  const morePages = useRequest(() => loadPage(true));

  function reset() {
    generation++;
    webhookId.value = "";
    webhook.value = null;
    filter.value = "";
    deliveries.value = [];
    nextCursor.value = "";
    details.value = new Map();
    replaying.value = new Set();
    endpoint.reset();
    firstPage.reset();
    morePages.reset();
  }

  /** Shows an endpoint: its settings and the first page of its log. */
  async function open(id: string) {
    reset();
    webhookId.value = id;
    await Promise.all([endpoint.run(), firstPage.run()]);
  }

  async function setFilter(next: DeliveryState | "") {
    filter.value = next;
    morePages.reset();
    await firstPage.run();
  }

  /** Keeps the shown settings in step after an edit elsewhere in the page. */
  function replace(updated: Webhook) {
    if (updated.id === webhookId.value) {
      webhook.value = updated;
    }
  }

  function detailHandle(deliveryId: string): RequestHandle<WebhookDeliveryDetail> {
    let handle = details.value.get(deliveryId);
    if (handle === undefined) {
      const id = webhookId.value;
      handle = useRequest(() => getDelivery(client, id, deliveryId));
      details.value = new Map(details.value).set(deliveryId, handle);
    }
    return handle;
  }

  /** Attempt history of one delivery; loads it on first use. */
  async function loadDetail(deliveryId: string) {
    await detailHandle(deliveryId).run();
  }

  function detailState(deliveryId: string): RequestState<WebhookDeliveryDetail> {
    return details.value.get(deliveryId)?.state.value ?? { status: "idle" };
  }

  async function replay(deliveryId: string): Promise<WebhookDelivery> {
    replaying.value = new Set(replaying.value).add(deliveryId);
    try {
      const queued = await replayDelivery(client, webhookId.value, deliveryId);
      deliveries.value = deliveries.value.map((entry) => (entry.id === deliveryId ? queued : entry));
      const open = details.value.get(deliveryId);
      if (open !== undefined && open.state.value.status !== "idle") {
        void open.run();
      }
      return queued;
    } finally {
      const next = new Set(replaying.value);
      next.delete(deliveryId);
      replaying.value = next;
    }
  }

  resetOnUserChange(reset);

  return {
    webhookId,
    webhook,
    filter,
    deliveries,
    nextCursor,
    replaying,
    state: endpoint.state,
    deliveriesState: firstPage.state,
    moreState: morePages.state,
    open,
    reload: endpoint.run,
    reloadDeliveries: firstPage.run,
    loadMore: morePages.run,
    setFilter,
    replace,
    loadDetail,
    detailState,
    replay,
    reset,
  };
});
