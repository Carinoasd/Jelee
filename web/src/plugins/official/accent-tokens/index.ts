// Official example plugin: accent theme (G32.5). Shows theme.token (token
// overrides that follow the plugin's own settings reactively), a settings
// section and a page registered with route.register.
import { definePlugin } from "@jelee/plugin-sdk";
import { accents, isAccentName, roundedTokens } from "./accents";
import { messages } from "./messages";

export default definePlugin({
  messages,
  setup(context) {
    const accent = () => {
      const name = context.settings.get<string>("accent", "teal");
      return accents[isAccentName(name) ? name : "teal"];
    };
    const rounded = () => (context.settings.get<boolean>("rounded", false) ? roundedTokens : {});
    context.register("theme.token", {
      id: "accent",
      tokens: () => ({ ...accent().light, ...rounded() }),
      darkTokens: () => ({ ...accent().dark, ...rounded() }),
    });
    context.register("settings.section", {
      id: "accent",
      title: "options.title",
      component: () => import("./AccentSettings.vue"),
    });
    context.register("route.register", {
      path: "preview",
      title: "preview.title",
      component: () => import("./TokenPreview.vue"),
    });
  },
});
