import { createApp } from "vue";
import App from "./App.vue";
import { installAppPlugins } from "./plugins";
import "./theme/tokens.css";
import "./theme/base.css";

const app = createApp(App);
const { router } = installAppPlugins(app);
void router.isReady().then(() => app.mount("#app"));
