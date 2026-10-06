import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import en from "@/i18n/en-US/accessMatrix.json";
import { button, control, hasButton, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, type RouteFetch } from "@/test/routeFetch";
import type { AccessChangePreview, AccessGrantMatrix, AccessTemplate } from "./api";
import { allOn, grantDiffs, grantOperations, withCells, type Grants } from "./matrix";
import { templateInput, templateNameProblem } from "./templates";

const text = en.accessMatrix;
const page = "/admin/access/matrix";
const matrixPath = "/api/v1/access/library-grants";
const bulkPath = "/api/v1/access/library-grants/bulk";
const templatesPath = "/api/v1/access/templates";
const films = "10000000-0000-4000-8000-00000000000a";
const shows = "10000000-0000-4000-8000-00000000000b";
const kidsLib = "10000000-0000-4000-8000-00000000000c";
const admin = "00000000-0000-4000-8000-0000000000a1";
const kid = "00000000-0000-4000-8000-0000000000b2";
const teen = "00000000-0000-4000-8000-0000000000b3";

function matrix(): AccessGrantMatrix {
  return {
    libraries: [
      { libraryId: films, name: "Films" },
      { libraryId: shows, name: "Shows" },
      { libraryId: kidsLib, name: "Kids" },
    ],
    users: [
      { id: admin, name: "admin", displayName: "Admin", admin: true, disabled: false, libraryIds: [] },
      { id: kid, name: "kid", displayName: "Kid", admin: false, disabled: false, libraryIds: [kidsLib] },
      { id: teen, name: "teen", admin: false, disabled: true, libraryIds: [films, kidsLib] },
    ],
    truncated: false,
  };
}

const template: AccessTemplate = {
  id: "80000000-0000-4000-8000-000000000001",
  name: "Children",
  libraryIds: [kidsLib],
  parentalRatingMax: 7,
  blockedTags: ["horror"],
  blockedKeywords: ["blood"],
  createdAt: "2026-10-05T00:00:00Z",
  updatedAt: "2026-10-05T00:00:00Z",
};

const preview: AccessChangePreview = {
  applied: false,
  users: 2,
  items: 37,
  shown: 40,
  hidden: 12,
  changes: [
    { userId: kid, name: "Kid", addedLibraryIds: [films], removedLibraryIds: [], restrictionsChanged: false, shown: 40, hidden: 0 },
    { userId: teen, name: "teen", addedLibraryIds: [], removedLibraryIds: [kidsLib], restrictionsChanged: false, shown: 0, hidden: 12 },
  ],
};

function server(templates: AccessTemplate[] = []): RouteFetch {
  return createRouteFetch()
    .on("GET", matrixPath, () => data(matrix()))
    .on("GET", templatesPath, () => data(templates))
    .on("GET", "/api/v1/access/parental-ratings", () =>
      data([
        { code: "G", level: 0 },
        { code: "PG", level: 7 },
        { code: "R", level: 17 },
      ]),
    );
}

function row(wrapper: VueWrapper, name: string) {
  const found = wrapper.findAll(".jl-matrix__table tbody tr").find((candidate) => candidate.find("th").text().includes(name));
  if (found === undefined) {
    throw new Error("no row " + name);
  }
  return found;
}

function cell(wrapper: VueWrapper, user: string, library: string) {
  return wrapper.find<HTMLInputElement>(`input[aria-label="${library} for ${user}"]`);
}

afterEach(() => {
  unmountAll();
});

describe("grant matrix helpers", () => {
  const server: Grants = new Map([
    ["u1", new Set(["a"])],
    ["u2", new Set(["a", "b"])],
    ["u3", new Set<string>()],
  ]);

  it("turns the differences into grouped add and remove operations", () => {
    let draft = withCells(server, ["u1", "u3"], ["c", "b"], true);
    draft = withCells(draft, ["u2"], ["a"], false);
    const diffs = grantDiffs(server, draft, ["a", "b", "c"]);
    expect(diffs).toEqual([
      { userId: "u1", added: ["b", "c"], removed: [] },
      { userId: "u2", added: [], removed: ["a"] },
      { userId: "u3", added: ["b", "c"], removed: [] },
    ]);
    expect(grantOperations(diffs)).toEqual([
      { action: "add", userIds: ["u1", "u3"], libraryIds: ["b", "c"] },
      { action: "remove", userIds: ["u2"], libraryIds: ["a"] },
    ]);
    // Undoing a tick removes the difference again.
    expect(grantDiffs(server, withCells(withCells(server, ["u1"], ["b"], true), ["u1"], ["b"], false), ["a", "b"])).toEqual([]);
    expect(grantOperations([])).toEqual([]);
  });

  it("knows whether a row or column is full", () => {
    expect(allOn(server, ["u2"], ["a", "b"])).toBe(true);
    expect(allOn(server, ["u1", "u2"], ["a"])).toBe(true);
    expect(allOn(server, ["u1", "u2"], ["b"])).toBe(false);
    expect(allOn(server, [], ["a"])).toBe(false);
  });

  it("builds template bodies and checks names case-insensitively", () => {
    expect(templateInput({ name: " Kids ", libraryIds: ["a"], ceiling: "", unrated: "policy", tags: [], keywords: ["x"] })).toEqual({
      name: "Kids",
      libraryIds: ["a"],
      blockedTags: [],
      blockedKeywords: ["x"],
    });
    expect(templateInput({ name: "T", libraryIds: [], ceiling: "13", unrated: "show", tags: ["t"], keywords: [] })).toMatchObject({
      parentalRatingMax: 13,
      blockUnrated: false,
    });
    expect(templateNameProblem(" ", [], null)).toBe("accessMatrix.templates.nameRequired");
    expect(templateNameProblem("x".repeat(65), [], null)).toBe("accessMatrix.templates.nameTooLong");
    expect(templateNameProblem("children", [template], null)).toBe("accessMatrix.templates.nameTaken");
    expect(templateNameProblem("children", [template], template.id)).toBeNull();
  });
});

describe("grant matrix view", () => {
  it("shows the grants as a table with administrators marked", async () => {
    const routes = server([template]);
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    expect(wrapper.find("a[href='/admin/access/network']").exists()).toBe(true);
    expect(wrapper.find(".jl-matrix__table caption").text()).toBe(text.caption);
    expect(wrapper.findAll(".jl-matrix__table thead th[scope=col]")).toHaveLength(5);
    expect(wrapper.findAll(".jl-matrix__table tbody th[scope=row]")).toHaveLength(3);
    expect(row(wrapper, "Admin").text()).toContain(text.adminBadge);
    expect(row(wrapper, "teen").text()).toContain(text.disabledBadge);
    expect(cell(wrapper, "Kid", "Kids").element.checked).toBe(true);
    expect(cell(wrapper, "Kid", "Films").element.checked).toBe(false);
    expect(cell(wrapper, "teen", "Films").element.checked).toBe(true);
    expect(wrapper.text()).toContain(text.noChanges);
    expect(hasButton(wrapper, text.previewChanges)).toBe(true);
    expect(button(wrapper, text.previewChanges).attributes("disabled")).toBeDefined();
    // The template list shows the library names.
    const templateRow = wrapper.find("section[aria-labelledby='access-templates-title'] tbody tr");
    expect(templateRow.text()).toContain("Children");
    expect(templateRow.text()).toContain("Kids");
    expect(templateRow.text()).toContain("Age 7");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("previews the ticked changes and applies them only after the confirmation", async () => {
    let applied = false;
    const routes = server().on("POST", bulkPath, ({ body }) => {
      const preview_ = (body as { preview: boolean }).preview;
      if (!preview_) {
        applied = true;
      }
      return data({ ...preview, applied: !preview_ });
    });
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    await cell(wrapper, "Kid", "Films").setValue(true);
    await cell(wrapper, "teen", "Kids").setValue(false);
    // A tick undone is no change.
    await cell(wrapper, "Kid", "Shows").setValue(true);
    await cell(wrapper, "Kid", "Shows").setValue(false);
    expect(wrapper.findAll(".jl-matrix__cell--changed")).toHaveLength(2);
    expect(wrapper.text()).toContain("2 changed grants for 2 accounts, not saved yet.");

    await button(wrapper, text.previewChanges).trigger("click");
    await flushPromises();
    expect(routes.calls("POST", bulkPath).map((request) => request.body)).toEqual([
      {
        operations: [
          { action: "add", userIds: [kid], libraryIds: [films] },
          { action: "remove", userIds: [teen], libraryIds: [kidsLib] },
        ],
        preview: true,
      },
    ]);
    const dialog = wrapper.find("[role=dialog]");
    expect(dialog.find("h2").text()).toBe(text.preview.titleGrants);
    expect(dialog.find("[data-count=users]").text()).toBe("2");
    expect(dialog.find("[data-count=items]").text()).toBe("37");
    expect(dialog.find("[data-count=shown]").text()).toBe("+40");
    expect(dialog.find("[data-count=hidden]").text()).toBe("−12");
    const details = dialog.findAll("tbody tr");
    expect(details[0]!.text()).toContain("Kid");
    expect(details[0]!.text()).toContain("Films");
    expect(details[1]!.text()).toContain("Kids");
    expect(details[1]!.text()).toContain("−12");
    expect(applied).toBe(false);
    expect(cell(wrapper, "Kid", "Films").attributes("disabled")).toBeDefined();

    // Cancel backs out without writing; the ticks stay.
    await button(wrapper, "Cancel").trigger("click");
    await flushPromises();
    expect(wrapper.find("[role=dialog]").exists()).toBe(false);
    expect(applied).toBe(false);
    expect(cell(wrapper, "Kid", "Films").element.checked).toBe(true);

    await button(wrapper, text.previewChanges).trigger("click");
    await flushPromises();
    routes.on("GET", matrixPath, () => {
      const next = matrix();
      next.users[1]!.libraryIds = [films, kidsLib];
      next.users[2]!.libraryIds = [films];
      return data(next);
    });
    await button(wrapper, text.preview.apply).trigger("click");
    await flushPromises();
    const calls = routes.calls("POST", bulkPath);
    expect(calls).toHaveLength(3);
    expect(calls[2]!.body).toMatchObject({ preview: false });
    expect((calls[2]!.body as { operations: unknown }).operations).toEqual((calls[1]!.body as { operations: unknown }).operations);
    expect(routes.calls("GET", matrixPath)).toHaveLength(2);
    expect(toastKeys(wrapper)).toContain("accessMatrix.applied");
    expect(wrapper.find("[role=dialog]").exists()).toBe(false);
    expect(wrapper.findAll(".jl-matrix__cell--changed")).toHaveLength(0);
    expect(cell(wrapper, "Kid", "Films").element.checked).toBe(true);
    expect(wrapper.text()).toContain(text.noChanges);
  });

  it("fills a column and a row, and discards the changes", async () => {
    const { wrapper } = await mountView(page, { fetch: server().fetch, user: adminUser });
    await wrapper.find(`button[aria-label="Grant Shows to every account"]`).trigger("click");
    expect(["Admin", "Kid", "teen"].every((user) => cell(wrapper, user, "Shows").element.checked)).toBe(true);
    expect(wrapper.find(`button[aria-label="Remove Shows from every account"]`).exists()).toBe(true);
    await wrapper.find(`button[aria-label="Grant every library to Kid"]`).trigger("click");
    expect(["Films", "Shows", "Kids"].every((library) => cell(wrapper, "Kid", library).element.checked)).toBe(true);
    expect(wrapper.text()).toContain("4 changed grants for 3 accounts");
    await button(wrapper, text.discard).trigger("click");
    expect(wrapper.text()).toContain(text.noChanges);
    expect(cell(wrapper, "Kid", "Shows").element.checked).toBe(false);
  });

  it("asks for batches above 100 changed accounts", async () => {
    const many: AccessGrantMatrix = {
      libraries: [{ libraryId: films, name: "Films" }],
      users: Array.from({ length: 101 }, (_, i) => ({
        id: `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`,
        name: `user${i}`,
        admin: false,
        disabled: false,
        libraryIds: [],
      })),
      truncated: true,
    };
    const routes = server().on("GET", matrixPath, () => data(many));
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    expect(wrapper.text()).toContain("Only the first 101 accounts are listed.");
    await wrapper.find(`button[aria-label="Grant Films to every account"]`).trigger("click");
    expect(wrapper.text()).toContain("101 accounts changed; one change may touch at most 100.");
    expect(button(wrapper, text.previewChanges).attributes("disabled")).toBeDefined();
  });

  it("shows a refused preview with its trace ID and a failed load with retry", async () => {
    let fail = true;
    const routes = server()
      .on("GET", matrixPath, () => (fail ? apiError(503, "internal_error") : data(matrix())))
      .on("POST", bulkPath, () => apiError(400, "invalid_request"));
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    expect(wrapper.text()).toContain("trace-internal_error");
    expect(wrapper.text()).not.toContain("raw server text");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    await cell(wrapper, "Kid", "Films").setValue(true);
    await button(wrapper, text.previewChanges).trigger("click");
    await flushPromises();
    expect(wrapper.find("[role=dialog]").exists()).toBe(false);
    expect(wrapper.text()).toContain("trace-invalid_request");
    expect(wrapper.text()).not.toContain("raw server text");
  });

  it("creates a template and applies it to the selected accounts after a preview", async () => {
    const stored: AccessTemplate[] = [];
    const routes = server(stored)
      .on("POST", templatesPath, ({ body }) => {
        const created: AccessTemplate = { ...template, ...(body as Partial<AccessTemplate>), id: template.id };
        stored.push(created);
        return data(created, 201);
      })
      .on("POST", `${templatesPath}/:id/apply`, ({ body }) =>
        data({
          applied: !(body as { preview: boolean }).preview,
          users: 1,
          items: 5,
          shown: 0,
          hidden: 5,
          changes: [{ userId: teen, name: "teen", addedLibraryIds: [], removedLibraryIds: [films], restrictionsChanged: true, shown: 0, hidden: 5 }],
        }),
      );
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    expect(wrapper.text()).toContain(text.templates.empty);

    await button(wrapper, text.templates.add).trigger("click");
    await flushPromises();
    await button(wrapper, text.templates.submitCreate).trigger("click");
    expect(routes.calls("POST", templatesPath)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.templates.nameRequired);
    const form = wrapper.find(".jl-template-form");
    await control(wrapper, text.templates.name).setValue(" Children ");
    const kids = form.findAll("label").find((label) => label.text() === "Kids")!;
    await form.find(`[id="${kids.attributes("for")}"]`).setValue(true);
    await control<HTMLSelectElement>(wrapper, "Rating ceiling").setValue("7");
    await control(wrapper, "Tag or genre to block").setValue("horror");
    await form.findAll("form")[0]!.trigger("submit");
    await control(wrapper, "Keyword to block").setValue("blood");
    await form.findAll("form")[1]!.trigger("submit");
    await button(wrapper, text.templates.submitCreate).trigger("click");
    await flushPromises();
    expect(routes.calls("POST", templatesPath).map((request) => request.body)).toEqual([
      { name: "Children", libraryIds: [kidsLib], parentalRatingMax: 7, blockedTags: ["horror"], blockedKeywords: ["blood"] },
    ]);
    expect(toastKeys(wrapper)).toContain("accessMatrix.templates.created");
    expect(wrapper.find(".jl-template-form").exists()).toBe(false);

    const preview = button(wrapper, text.apply.preview);
    await control<HTMLSelectElement>(wrapper, text.apply.template).setValue(template.id);
    expect(preview.attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain(text.apply.noneSelected);
    await wrapper.find(`input[aria-label="Select teen"]`).setValue(true);
    await button(wrapper, text.apply.preview).trigger("click");
    await flushPromises();
    const applyPath = `${templatesPath}/${template.id}/apply`;
    expect(routes.calls("POST", applyPath).map((request) => request.body)).toEqual([{ userIds: [teen], preview: true }]);
    const dialog = wrapper.find("[role=dialog]");
    expect(dialog.find("h2").text()).toBe('Confirm template "Children" for 1 accounts');
    expect(dialog.find("[data-count=hidden]").text()).toBe("−5");
    expect(dialog.text()).toContain(text.preview.restrictionsReplaced);
    await button(wrapper, text.preview.apply).trigger("click");
    await flushPromises();
    expect(routes.calls("POST", applyPath).map((request) => request.body)).toEqual([
      { userIds: [teen], preview: true },
      { userIds: [teen], preview: false },
    ]);
    expect(routes.calls("GET", matrixPath)).toHaveLength(2);
    expect(wrapper.find<HTMLInputElement>(`input[aria-label="Select teen"]`).element.checked).toBe(false);
  });

  it("refuses a duplicate template name from the server and deletes after confirmation", async () => {
    const routes = server([template])
      .on("PUT", `${templatesPath}/:id`, () => apiError(409, "conflict"))
      .on("DELETE", `${templatesPath}/:id`, () => noContent());
    const { wrapper } = await mountView(page, { fetch: routes.fetch, user: adminUser });
    await button(wrapper, "Edit").trigger("click");
    await flushPromises();
    expect(control(wrapper, text.templates.name).element.value).toBe("Children");
    await button(wrapper, text.templates.submitUpdate).trigger("click");
    await flushPromises();
    expect(routes.calls("PUT", `${templatesPath}/${template.id}`)[0]!.body).toEqual({
      name: "Children",
      libraryIds: [kidsLib],
      parentalRatingMax: 7,
      blockedTags: ["horror"],
      blockedKeywords: ["blood"],
    });
    expect(wrapper.text()).toContain(text.templates.nameTaken);
    expect(wrapper.text()).toContain("trace-conflict");
    await button(wrapper, "Cancel").trigger("click");
    await flushPromises();

    await button(wrapper, "Delete").trigger("click");
    await flushPromises();
    expect(routes.calls("DELETE", `${templatesPath}/${template.id}`)).toHaveLength(0);
    routes.on("GET", templatesPath, () => data([]));
    await button(wrapper, text.templates.deleteConfirm).trigger("click");
    await flushPromises();
    expect(routes.calls("DELETE", `${templatesPath}/${template.id}`)).toHaveLength(1);
    expect(toastKeys(wrapper)).toContain("accessMatrix.templates.deleted");
    expect(wrapper.text()).toContain(text.templates.empty);
  });
});
