import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { getPolicy, setPolicy, type ClientPolicy, type ClientPolicyInput } from "@/features/clients/api";
import { resetOnUserChange } from "./userScoped";

/** The unknown-client policy of client control (G47.5). */
export const useClientPolicyStore = defineStore("clientsPolicy", () => {
  const { client } = useApi();
  const policy = shallowRef<ClientPolicy | null>(null);
  const saving = shallowRef(false);

  const request = useRequest(async () => {
    policy.value = await getPolicy(client);
    return policy.value;
  });

  async function save(input: ClientPolicyInput): Promise<ClientPolicy> {
    saving.value = true;
    try {
      policy.value = await setPolicy(client, input);
      return policy.value;
    } finally {
      saving.value = false;
    }
  }

  function reset() {
    policy.value = null;
    saving.value = false;
    request.reset();
  }

  resetOnUserChange(reset);

  return { policy, saving, state: request.state, load: request.run, save, reset };
});
