import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { createUser, listUsers, type CreateUserInput, type User, type UserPage } from "@/features/users/api";
import { resetOnUserChange } from "./userScoped";

// Account list of the administration pages, with cursor paging and the
// "include deleted accounts" filter.
export const useUsersStore = defineStore("users", () => {
  const { client } = useApi();
  const users = shallowRef<readonly User[]>([]);
  const nextCursor = shallowRef("");
  const includeDeleted = shallowRef(false);
  const creating = shallowRef(false);

  function accept(page: UserPage, append: boolean) {
    users.value = append ? [...users.value, ...page.users] : page.users;
    nextCursor.value = page.pagination.nextCursor;
    return users.value;
  }

  const firstPage = useRequest(
    async () => accept(await listUsers(client, { includeDeleted: includeDeleted.value }), false),
    (list) => list.length === 0,
  );
  const morePages = useRequest(async () =>
    accept(await listUsers(client, { cursor: nextCursor.value, includeDeleted: includeDeleted.value }), true),
  );

  async function load(): Promise<void> {
    morePages.reset();
    await firstPage.run();
  }

  async function setIncludeDeleted(value: boolean): Promise<void> {
    includeDeleted.value = value;
    await load();
  }

  /**
   * Creates an account with the caller's idempotency key (reused when the
   * same submission is retried) and reloads the list so it appears in
   * server order.
   */
  async function create(input: CreateUserInput, idempotencyKey: string): Promise<User> {
    creating.value = true;
    try {
      const user = await createUser(client, input, idempotencyKey);
      void load();
      return user;
    } finally {
      creating.value = false;
    }
  }

  function reset() {
    users.value = [];
    nextCursor.value = "";
    includeDeleted.value = false;
    creating.value = false;
    firstPage.reset();
    morePages.reset();
  }

  resetOnUserChange(reset);

  return {
    users,
    nextCursor,
    includeDeleted,
    creating,
    state: firstPage.state,
    moreState: morePages.state,
    load,
    loadMore: morePages.run,
    setIncludeDeleted,
    create,
    reset,
  };
});
