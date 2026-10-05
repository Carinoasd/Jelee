import { readFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vitest/config";

// The development server proxies API calls to a locally running Jelee
// (JELEE_LISTEN in .env.example). Override with JELEE_DEV_API.
const apiTarget = process.env.JELEE_DEV_API ?? "http://127.0.0.1:8097";

// Plugins compare their minJeleeVersion with the web client's version.
const { version } = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")) as { version: string };

export default defineConfig({
  plugins: [vue()],
  define: { __JELEE_VERSION__: JSON.stringify(version) },
  resolve: {
    alias: {
      "@jelee/plugin-sdk": fileURLToPath(new URL("./src/plugins/sdk/index.ts", import.meta.url)),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    proxy: { "/api": { target: apiTarget, changeOrigin: false } },
  },
  build: {
    target: "es2022",
    // No inline polyfill script, so the page works under a strict CSP
    // without 'unsafe-inline' (see docs/frontend-adr.md).
    modulePreload: { polyfill: false },
    sourcemap: false,
    assetsInlineLimit: 0,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts", "scripts/**/*.test.ts"],
    restoreMocks: true,
  },
});
