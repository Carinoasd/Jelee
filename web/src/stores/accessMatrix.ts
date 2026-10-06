import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  applyTemplate,
  bulkGrants,
  createTemplate,
  deleteTemplate,
  getGrantMatrix,
  listTemplates,
  updateTemplate,
  type AccessChangePreview,
  type AccessGrantMatrix,
  type AccessTemplate,
  type AccessTemplateInput,
  type GrantOperation,
} from "@/features/access/api";
import { resetOnUserChange } from "./userScoped";

function byName(a: AccessTemplate, b: AccessTemplate): number {
  return a.name.localeCompare(b.name);
}

// The user × library grant matrix and the access templates (G48.7).
export const useAccessMatrixStore = defineStore("accessMatrix", () => {
  const { client } = useApi();
  const matrix = shallowRef<AccessGrantMatrix | null>(null);
  const templates = shallowRef<readonly AccessTemplate[]>([]);
  /** A bulk change or template application is being previewed or written. */
  const applying = shallowRef(false);
  /** IDs of templates with a change in flight. */
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  const matrixRequest = useRequest(
    async () => {
      matrix.value = await getGrantMatrix(client);
      return matrix.value;
    },
    (data) => data.users.length === 0 || data.libraries.length === 0,
  );
  const templatesRequest = useRequest(
    async () => {
      templates.value = [...(await listTemplates(client))].sort(byName);
      return templates.value;
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

  async function withApplying<T>(work: () => Promise<T>): Promise<T> {
    applying.value = true;
    try {
      return await work();
    } finally {
      applying.value = false;
    }
  }

  function changeGrants(operations: readonly GrantOperation[], preview: boolean): Promise<AccessChangePreview> {
    return withApplying(() => bulkGrants(client, operations, preview));
  }

  function changeByTemplate(id: string, userIds: readonly string[], preview: boolean): Promise<AccessChangePreview> {
    return withApplying(() => applyTemplate(client, id, userIds, preview));
  }

  function put(template: AccessTemplate) {
    templates.value = [...templates.value.filter((entry) => entry.id !== template.id), template].sort(byName);
  }

  async function create(input: AccessTemplateInput): Promise<AccessTemplate> {
    const template = await createTemplate(client, input);
    put(template);
    if (templatesRequest.state.value.status !== "success") {
      await templatesRequest.run();
    }
    return template;
  }

  async function update(id: string, input: AccessTemplateInput): Promise<AccessTemplate> {
    setPending(id, true);
    try {
      const template = await updateTemplate(client, id, input);
      put(template);
      return template;
    } finally {
      setPending(id, false);
    }
  }

  async function remove(id: string): Promise<void> {
    setPending(id, true);
    try {
      await deleteTemplate(client, id);
      templates.value = templates.value.filter((entry) => entry.id !== id);
      if (templates.value.length === 0) {
        await templatesRequest.run();
      }
    } finally {
      setPending(id, false);
    }
  }

  function reset() {
    matrix.value = null;
    templates.value = [];
    applying.value = false;
    pending.value = new Set();
    matrixRequest.reset();
    templatesRequest.reset();
  }

  resetOnUserChange(reset);

  return {
    matrix,
    templates,
    applying,
    pending,
    matrixState: matrixRequest.state,
    templatesState: templatesRequest.state,
    loadMatrix: matrixRequest.run,
    loadTemplates: templatesRequest.run,
    changeGrants,
    changeByTemplate,
    create,
    update,
    remove,
    reset,
  };
});
