import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp } from "vue";
import { createMemoryHistory } from "vue-router";
import App from "@/App.vue";
import { installAppPlugins } from "@/plugins";
import { setupIssueKey, setupIssueRow } from "./issues";

const token = "s".repeat(43);
const steps = ["language", "admin", "database", "media", "tmdb", "toolchain", "metadata-policy", "network", "complete"];

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string, details: unknown = {}) =>
  json(status, { error: { code, message: code, details, traceId: "trace" } });

/** A server that still needs setup: only the wizard answers. */
function wizardServer() {
  const requests: Request[] = [];
  let current = 0;
  let completed = false;
  const bodies: Record<string, unknown> = {};
  const state = () => ({
    version: current + 1,
    current: steps[current],
    admin: current > 1 ? { userId: "00000000-0000-4000-8000-000000000009", name: "owner" } : {},
    database: {},
    tmdb: { enabled: false },
    toolchain: { acceptDegraded: false },
    metadataPolicy: { imageFetch: false, imageWriteBack: false },
    network: { privacyAcknowledged: false },
    ...(completed ? { completedAt: "2026-10-04T00:00:00Z" } : {}),
  });
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    requests.push(request);
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/setup/status") {
      return completed ? problem(410, "setup_completed") : json(200, { data: { setupRequired: true, tokenRequired: true } });
    }
    if (!path.startsWith("/api/v1/setup")) {
      return problem(503, "setup_required");
    }
    if (request.headers.get("X-Jelee-Setup-Token") !== token) {
      return problem(401, "setup_token_invalid");
    }
    if (path === "/api/v1/setup") {
      return json(200, { data: state() });
    }
    if (path === "/api/v1/setup/back") {
      current = Math.max(0, current - 1);
      return json(200, { data: state() });
    }
    if (path === "/api/v1/setup/complete") {
      completed = true;
      return json(200, { data: state() });
    }
    const step = path.split("/").at(-1) ?? "";
    const text = await request.text();
    bodies[step] = text === "" ? undefined : JSON.parse(text);
    if (step === "media" && (bodies[step] as { libraries: { path: string }[] }).libraries[0]?.path === "relative") {
      return problem(400, "setup_validation_failed", { step, issues: [{ field: "media[0].path", code: "media_path_not_absolute" }] });
    }
    current++;
    return json(200, { data: state() });
  });
  return { fetch, requests, bodies };
}

const mounted: { unmount(): void }[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) {
    wrapper.unmount();
  }
});

describe("setup wizard", () => {
  it("redirects to the wizard, needs the token, walks every step and opens sign-in", async () => {
    const server = wizardServer();
    const host = createApp(App);
    const plugins = installAppPlugins(host, { languages: ["en-US"], fetch: server.fetch, history: createMemoryHistory() });
    await plugins.router.push("/libraries");
    await plugins.router.isReady();
    const wrapper = mount(App, { global: { plugins: [plugins.router, plugins.i18n, plugins.pinia], provide: host._context.provides }, attachTo: document.body });
    mounted.push(wrapper);
    await flushPromises();
    expect(plugins.router.currentRoute.value.name).toBe("setup");
    expect(wrapper.find("h1").text()).toBe("Initial setup");

    const submit = async () => {
      await wrapper.find("form").trigger("submit");
      await flushPromises();
    };
    await wrapper.find("input[type=password]").setValue("wrong");
    await submit();
    expect(wrapper.text()).toContain("The setup token is wrong.");
    await wrapper.find("input[type=password]").setValue(token);
    await submit();
    expect(wrapper.text()).toContain("Step 1 of 9");

    await submit(); // language
    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue("owner");
    await inputs[2]!.setValue("correct horse battery");
    await submit(); // admin
    expect(server.bodies.admin).toEqual({ name: "owner", displayName: "", password: "correct horse battery" });
    // The password never lingers in the form after submitting.
    await submit(); // database
    expect(server.bodies.database).toBeUndefined();

    const media = wrapper.findAll("fieldset input");
    await media[0]!.setValue("Movies");
    await media[1]!.setValue("relative");
    await submit();
    expect(wrapper.text()).toContain("Entry 1: Enter an absolute folder path");
    await media[1]!.setValue("/srv/movies");
    await submit(); // media
    expect(server.bodies.media).toEqual({ libraries: [{ name: "Movies", path: "/srv/movies" }] });

    await submit(); // tmdb
    expect(server.bodies.tmdb).toEqual({ enabled: false });
    await submit(); // toolchain
    await submit(); // metadata policy
    expect(server.bodies["metadata-policy"]).toEqual({ nfoRead: "read-only", nfoWrite: "off", imageFetch: false, imageWriteBack: false });
    await submit(); // network
    expect(server.bodies.network).toEqual({ mode: "local", listen: "127.0.0.1:8097", allowedHosts: ["localhost", "127.0.0.1", "::1"], trustedProxies: [], privacyAcknowledged: false });
    expect(wrapper.text()).toContain("Step 9 of 9");

    await wrapper.findAll("button").find((button) => button.text() === "Back")!.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("Step 8 of 9");
    await submit();
    await submit(); // finish
    expect(wrapper.text()).toContain("Setup is complete.");
    expect(wrapper.html()).not.toMatch(/<video|<audio|<source|<track|<object|<embed|<iframe/i);
    // The token stays in memory: nothing is written to browser storage.
    expect(localStorage.length + sessionStorage.length).toBe(0);
    expect(server.requests.filter((request) => new URL(request.url).pathname.startsWith("/api/v1/setup/steps")).every((request) => request.headers.get("X-Jelee-Setup-Token") === token)).toBe(true);

    await plugins.router.push("/libraries");
    await flushPromises();
    expect(plugins.router.currentRoute.value.name).toBe("login");
  });
});

describe("setup issues", () => {
  it("maps fixed codes to catalog keys and finds list rows", () => {
    expect(setupIssueKey("listen_port_in_use")).toBe("setup.issues.listenPortInUse");
    expect(setupIssueKey("from_a_newer_server")).toBe("setup.issues.unknown");
    expect(setupIssueRow("media[2].path")).toBe(3);
    expect(setupIssueRow("network.listen")).toBeNull();
  });
});
