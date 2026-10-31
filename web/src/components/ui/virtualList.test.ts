import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, shallowRef } from "vue";
import UiVirtualList from "./UiVirtualList.vue";
import { countTracks, virtualWindow } from "./virtualWindow";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("virtual window", () => {
  const base = { count: 1000, columns: 4, rowHeight: 100, viewportTop: 0, viewportHeight: 500, overscan: 2 };

  it("renders whole rows around the viewport", () => {
    expect(virtualWindow(base)).toEqual({ start: 0, end: 28, before: 0, after: 24300 });
    expect(virtualWindow({ ...base, viewportTop: 10_000 })).toEqual({ start: 392, end: 428, before: 9800, after: 14300 });
    // Past the end and before the list.
    expect(virtualWindow({ ...base, viewportTop: 99_999 })).toEqual({ start: 1000, end: 1000, before: 25_000, after: 0 });
    expect(virtualWindow({ ...base, viewportTop: -2_000 })).toEqual({ start: 0, end: 0, before: 0, after: 25_000 });
    expect(virtualWindow({ ...base, count: 0 })).toEqual({ start: 0, end: 0, before: 0, after: 0 });
    // A partial last row still renders.
    expect(virtualWindow({ ...base, count: 5, viewportHeight: 10_000 })).toEqual({ start: 0, end: 5, before: 0, after: 0 });
  });

  it("counts resolved grid tracks", () => {
    expect(countTracks("150px 150px 150px")).toBe(3);
    expect(countTracks("  ")).toBe(1);
    expect(countTracks("none")).toBe(1);
    expect(countTracks("repeat(auto-fill, minmax(150px, 1fr))")).toBe(1);
    expect(countTracks("minmax(0, 1fr) 20px")).toBe(2);
  });
});

describe("virtual list", () => {
  const items = Array.from({ length: 500 }, (_, n) => ({ id: `i${String(n)}` }));

  function host(list: readonly { id: string }[], onEnd = () => {}) {
    const source = shallowRef(list);
    const Host = defineComponent({
      render: () =>
        h(
          UiVirtualList<{ id: string }>,
          { items: source.value, itemKey: (item: { id: string }) => item.id, estimate: 100, overscan: 1, class: "list", onEnd },
          { default: ({ item }: { item: { id: string } }) => h("a", { href: "#" + item.id }, item.id) },
        ),
    });
    return { wrapper: mount(Host, { attachTo: document.body }), source };
  }

  it("renders everything below the threshold", () => {
    const { wrapper } = host(items.slice(0, 50));
    expect(wrapper.findAll("li[data-index]")).toHaveLength(50);
    expect(wrapper.find("ul").classes()).toContain("list");
    expect(wrapper.find("li[data-index]").attributes("aria-setsize")).toBeUndefined();
    wrapper.unmount();
  });

  it("renders only the rows near the viewport and follows the scroll position", async () => {
    vi.stubGlobal("innerHeight", 500);
    const { wrapper } = host(items);
    const list = wrapper.find("ul").element as HTMLElement;
    // 500 px viewport, 100 px rows, one overscan row: rows 0 to 5.
    expect(wrapper.findAll("li[data-index]").map((li) => li.text())).toEqual(["i0", "i1", "i2", "i3", "i4", "i5"]);
    expect(wrapper.find("li[data-index]").attributes()).toMatchObject({ "aria-setsize": "500", "aria-posinset": "1" });
    expect(list.style.paddingBottom).toBe(`${494 * 100}px`);

    vi.spyOn(list, "getBoundingClientRect").mockReturnValue(new DOMRect(0, -10_000, 800, 50_000));
    window.dispatchEvent(new Event("scroll"));
    await vi.waitFor(() => {
      expect(wrapper.find("li[data-index]").text()).toBe("i99");
    });
    expect(wrapper.findAll("li[data-index]").at(-1)!.text()).toBe("i105");
    expect(wrapper.find("li[data-index]").attributes("aria-posinset")).toBe("100");
    expect(list.style.paddingTop).toBe("9900px");
    wrapper.unmount();
  });

  it("asks for more when the end comes near", async () => {
    const callbacks: IntersectionObserverCallback[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(callback: IntersectionObserverCallback) {
          callbacks.push(callback);
        }
        observe() {}
        disconnect() {}
      },
    );
    const onEnd = vi.fn();
    const { wrapper, source } = host(items.slice(0, 10), onEnd);
    expect(callbacks).toHaveLength(1);
    callbacks[0]!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
    expect(onEnd).toHaveBeenCalledTimes(1);
    // A new page observes again, so a list that still fits keeps loading.
    source.value = items.slice(0, 20);
    await flushPromises();
    expect(callbacks).toHaveLength(2);
    wrapper.unmount();
  });
});
