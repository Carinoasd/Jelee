// Starts the extension host and administrator CSS. Loaded as a separate
// chunk after the core plugins, so neither the SDK, the manifest validator
// nor the CSS sanitizer weighs on the main bundle (G35.4).
import type { App } from "vue";
import { useAppearanceStore } from "@/stores/appearance";
import { usePluginStore } from "./store";

export function startExtensions(app: App) {
  return app.runWithContext(() => {
    useAppearanceStore();
    return usePluginStore();
  });
}
