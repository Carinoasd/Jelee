import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  getAccessPolicy,
  listParentalRatings,
  putAccessPolicy,
  ratingLevels,
  type AccessPolicy,
  type RatingLevel,
} from "@/features/access/api";
import { resetOnUserChange } from "./userScoped";

// Server-wide content access policy and the recognized rating levels; the
// levels also feed the rating ceiling choices of the user detail page.
export const useAccessPolicyStore = defineStore("accessPolicy", () => {
  const { client } = useApi();
  const policy = shallowRef<AccessPolicy | null>(null);
  const levels = shallowRef<readonly RatingLevel[]>([]);
  const saving = shallowRef(false);

  const policyRequest = useRequest(async () => {
    policy.value = await getAccessPolicy(client);
    return policy.value;
  });
  const ratingsRequest = useRequest(
    async () => {
      levels.value = ratingLevels(await listParentalRatings(client));
      return levels.value;
    },
    (data) => data.length === 0,
  );

  /** Loads the rating levels unless they are already here. */
  async function ensureRatings(): Promise<void> {
    const status = ratingsRequest.state.value.status;
    if (status === "idle" || status === "error") {
      await ratingsRequest.run();
    }
  }

  async function save(next: AccessPolicy): Promise<void> {
    saving.value = true;
    try {
      policy.value = await putAccessPolicy(client, next);
    } finally {
      saving.value = false;
    }
  }

  function reset() {
    policy.value = null;
    levels.value = [];
    saving.value = false;
    policyRequest.reset();
    ratingsRequest.reset();
  }

  resetOnUserChange(reset);

  return {
    policy,
    levels,
    saving,
    policyState: policyRequest.state,
    ratingsState: ratingsRequest.state,
    loadPolicy: policyRequest.run,
    loadRatings: ratingsRequest.run,
    ensureRatings,
    save,
    reset,
  };
});
