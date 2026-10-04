import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createAppI18n } from "@/i18n";
import { ApiError } from "@/api/errors";
import { toastTimeoutMs, useToastStore } from "@/stores/toasts";
import RequestStatus from "./RequestStatus.vue";
import UiButton from "./UiButton.vue";
import UiEmptyState from "./UiEmptyState.vue";
import UiErrorState from "./UiErrorState.vue";
import UiSkeleton from "./UiSkeleton.vue";
import UiTextField from "./UiTextField.vue";
import UiToastRegion from "./UiToastRegion.vue";

const global = { plugins: [createAppI18n("en-US")] };

afterEach(() => {
  vi.useRealTimers();
});

describe("ui components", () => {
  it("UiButton is a native button with toggle and busy states", async () => {
    const onClick = vi.fn();
    const wrapper = mount(UiButton, { props: { pressed: true, onClick }, slots: { default: "Go" } });
    const button = wrapper.find("button");
    expect(button.attributes("type")).toBe("button");
    expect(button.attributes("aria-pressed")).toBe("true");
    await button.trigger("click");
    expect(onClick).toHaveBeenCalledOnce();
    await wrapper.setProps({ busy: true });
    expect(button.attributes("disabled")).toBeDefined();
    expect(button.attributes("aria-busy")).toBe("true");
    const plain = mount(UiButton, { slots: { default: "Go" } });
    expect(plain.find("button").attributes("aria-pressed")).toBeUndefined();
  });

  it("UiTextField links its label, hint and error", () => {
    const wrapper = mount(UiTextField, {
      props: { modelValue: "", label: "Name", hint: "Your login", error: "Required", required: true },
    });
    const input = wrapper.find("input");
    const label = wrapper.find("label");
    expect(label.attributes("for")).toBe(input.attributes("id"));
    expect(input.attributes("aria-invalid")).toBe("true");
    const described = (input.attributes("aria-describedby") ?? "").split(" ");
    expect(described).toHaveLength(2);
    for (const id of described) {
      expect(wrapper.find(`[id="${id}"]`).exists()).toBe(true);
    }
  });

  it("UiSkeleton is hidden from assistive technology", () => {
    expect(mount(UiSkeleton, { props: { shape: "poster" } }).attributes("aria-hidden")).toBe("true");
  });

  it("UiEmptyState and UiErrorState render localized, escaped content", async () => {
    const empty = mount(UiEmptyState, { props: { title: "<b>none</b>" }, slots: { default: "hint" } });
    expect(empty.find("b").exists()).toBe(false);
    expect(empty.attributes("role")).toBe("status");

    const error = mount(UiErrorState, {
      props: { error: new ApiError("csrf_failed", 403, "server text", "trace-1") },
      global,
    });
    expect(error.text()).toContain("security token");
    expect(error.text()).toContain("trace-1");
    expect(error.text()).not.toContain("server text");
    await error.find("button").trigger("click");
    expect(error.emitted("retry")).toHaveLength(1);
  });

  it("RequestStatus swaps the loading text for a skeleton slot but keeps it announced", () => {
    const wrapper = mount(RequestStatus, {
      props: { state: { status: "loading", previous: null } },
      slots: { loading: "<i class='sk'></i>" },
      global,
    });
    expect(wrapper.find("[role=status]").classes()).toContain("jl-visually-hidden");
    expect(wrapper.find(".sk").exists()).toBe(true);
    expect(wrapper.attributes("aria-busy")).toBe("true");
  });

  it("UiToastRegion announces toasts and dismisses with a labeled button", async () => {
    const wrapper = mount(UiToastRegion, {
      props: {
        toasts: [
          { id: 1, message: "Saved", tone: "success" },
          { id: 2, message: "Failed", tone: "danger" },
        ],
      },
      global,
    });
    expect(wrapper.find("[aria-live=polite]").exists()).toBe(true);
    expect(wrapper.findAll("[role=status]")).toHaveLength(1);
    expect(wrapper.findAll("[role=alert]")).toHaveLength(1);
    const close = wrapper.findAll("button");
    expect(close[0]!.attributes("aria-label")).toBe("Dismiss");
    await close[1]!.trigger("click");
    expect(wrapper.emitted("dismiss")).toEqual([[2]]);
  });

  it("toast store auto-dismisses notices but keeps errors", () => {
    vi.useFakeTimers();
    setActivePinia(createPinia());
    const store = useToastStore();
    store.push("auth.signedOut", "success");
    store.push("errors.generic", "danger");
    expect(store.toasts).toHaveLength(2);
    vi.advanceTimersByTime(toastTimeoutMs);
    expect(store.toasts.map((toast) => toast.tone)).toEqual(["danger"]);
  });
});

describe("charts and confirmation", () => {
  const rows = [
    { key: "a", label: "Alpha", value: 50, display: "50 min" },
    { key: "b", label: "Beta", value: 100, display: "100 min" },
    { key: "c", label: "Gamma", value: 0, display: "0 min" },
  ];

  it("UiBarChart keeps the figures in a table and scales decorative bars", async () => {
    const { default: UiBarChart } = await import("./UiBarChart.vue");
    const wrapper = mount(UiBarChart, { props: { caption: "Time", rows, labelHeader: "Name", valueHeader: "Time" } });
    expect(wrapper.find("caption").text()).toBe("Time");
    expect(wrapper.findAll("th[scope=row]").map((th) => th.text())).toEqual(["Alpha", "Beta", "Gamma"]);
    const bars = wrapper.findAll(".jl-bars__bar").map((bar) => (bar.element as HTMLElement).style.width);
    expect(bars).toEqual(["50%", "100%", "1%"]);
    expect(wrapper.find(".jl-bars__track").attributes("aria-hidden")).toBe("true");
  });

  it("UiColumnChart hides the SVG from assistive technology and lists the values", async () => {
    const { default: UiColumnChart } = await import("./UiColumnChart.vue");
    const wrapper = mount(UiColumnChart, { props: { caption: "Per day", rows, labelHeader: "Day", valueHeader: "Time" } });
    expect(wrapper.find("svg").attributes("aria-hidden")).toBe("true");
    const heights = wrapper.findAll("rect").map((rect) => Number(rect.attributes("height")));
    expect(heights).toEqual([60, 120, 0]);
    expect(wrapper.find("table").text()).toContain("Beta100 min");
  });

  it("UiConfirmButton emits only on the second, explicit press", async () => {
    const { default: UiConfirmButton } = await import("./UiConfirmButton.vue");
    const onConfirm = vi.fn();
    const wrapper = mount(UiConfirmButton, {
      props: { label: "Delete", confirmLabel: "Really delete", prompt: "Gone for good", onConfirm },
      global,
      attachTo: document.body,
    });
    await wrapper.find("button").trigger("click");
    expect(onConfirm).not.toHaveBeenCalled();
    expect(wrapper.find("[role=alert]").text()).toBe("Gone for good");
    const buttons = wrapper.findAll("button");
    expect(buttons.map((button) => button.text())).toEqual(["Really delete", "Cancel"]);
    expect(document.activeElement).toBe(buttons[0]!.element);
    await buttons[0]!.trigger("click");
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(wrapper.find("button").text()).toBe("Delete");
    wrapper.unmount();
  });
});
