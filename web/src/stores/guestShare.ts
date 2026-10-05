import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { currentShare, type GuestShare } from "@/features/shares/api";
import { resetOnUserChange } from "./userScoped";

/**
 * The share behind a guest session (GET /api/v1/shares/current), kept so
 * guest pages can name the share without asking again. Guest pages must
 * call only the routes the server allows guests: every refused route is
 * audited as share.access_refused.
 */
export const useGuestShareStore = defineStore("guestShare", () => {
  const { client } = useApi();
  const share = shallowRef<GuestShare | null>(null);
  let pending: Promise<GuestShare> | null = null;

  /** Reads the share again. */
  function fetch(): Promise<GuestShare> {
    const current = currentShare(client).then(
      (value) => {
        if (pending === current) {
          share.value = value;
          pending = null;
        }
        return value;
      },
      (error: unknown) => {
        if (pending === current) {
          pending = null;
        }
        throw error;
      },
    );
    pending = current;
    return current;
  }

  /** The cached share, read once when not known yet; failures leave it null. */
  async function ensure(): Promise<void> {
    if (share.value === null) {
      await (pending ?? fetch()).catch(() => undefined);
    }
  }

  function reset() {
    share.value = null;
    pending = null;
  }

  resetOnUserChange(reset);

  return { share, fetch, ensure, reset };
});
