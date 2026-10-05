import type { PluginComponent } from "@jelee/plugin-sdk";
import { defineAsyncComponent, type Component } from "vue";

const cache = new WeakMap<PluginComponent, Component>();

/**
 * Wraps a plugin's component loader once. A loader that rejects or times out
 * raises an error inside the surrounding PluginBoundary, which degrades only
 * that plugin's area.
 */
export function pluginComponent(loader: PluginComponent): Component {
  let component = cache.get(loader);
  if (component === undefined) {
    component = defineAsyncComponent({ loader, timeout: 15_000 });
    cache.set(loader, component);
  }
  return component;
}
