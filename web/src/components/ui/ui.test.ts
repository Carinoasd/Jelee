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
