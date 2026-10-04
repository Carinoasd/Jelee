import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { createShare, listShares, revokeShare, type Share, type ShareGrant, type ShareInput } from "@/features/shares/api";
import { resetOnUserChange } from "./userScoped";

/**
 * Share links for the administration page. Tokens are never kept here: the
 * view holds a new link's token only while it shows it once.
 */
export const useSharesStore = defineStore("shares", () => {
  const { client } = useApi();
  const shares = shallowRef<readonly Share[]>([]);
  /** IDs of links with a revocation in flight. */
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  const request = useRequest(
    async () => {
      shares.value = await listShares(client);
      return shares.value;
    },
    (data) => data.length === 0,
  );

  function put(share: Share) {
    const others = shares.value.filter((entry) => entry.id !== share.id);
    // Newest first, like the server's list.
    shares.value = [share, ...others].sort((a, b) => (a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0));
  }

  async function create(input: ShareInput): Promise<ShareGrant> {
    const grant = await createShare(client, input);
    put(grant.share);
    if (request.state.value.status !== "success") {
      await request.run();
    }
    return grant;
  }

  async function revoke(id: string): Promise<Share> {
    pending.value = new Set([...pending.value, id]);
    try {
      const share = await revokeShare(client, id);
      put(share);
      return share;
    } finally {
      pending.value = new Set([...pending.value].filter((entry) => entry !== id));
    }
  }

  function reset() {
    shares.value = [];
    pending.value = new Set();
    request.reset();
  }

  resetOnUserChange(reset);

  return { shares, pending, state: request.state, load: request.run, create, revoke, reset };
});
