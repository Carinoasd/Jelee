// Official example plugin: item facts (G32.5). Shows how a plugin adds a
// detail tab, an item action and a settings section, reads catalog data
// through the restricted API and keeps a setting in its own namespace.
import { definePlugin } from "@jelee/plugin-sdk";
import { messages } from "./messages";

export default definePlugin({
  messages,
  setup(context) {
    context.register("media.detail.tabs", {
      id: "facts",
      title: "tab.title",
      component: () => import("./ItemFactsTab.vue"),
    });
    context.register("item.action", {
      id: "copy-id",
      label: "action.copyId",
      async run(item, ui) {
        // Throws where the Clipboard API is unavailable (plain HTTP); the
        // host reports the failure instead of breaking the page.
        await navigator.clipboard.writeText(item.id);
        ui.notify("action.copied", "success");
      },
    });
    context.register("settings.section", {
      id: "options",
      title: "options.title",
      component: () => import("./ItemFactsSettings.vue"),
    });
  },
});
