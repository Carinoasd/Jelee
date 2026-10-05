import js from "@eslint/js";
import vueI18n from "@intlify/eslint-plugin-vue-i18n";
import pluginVue from "eslint-plugin-vue";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/**", "src/api/schema.d.ts", "coverage/**"] },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...pluginVue.configs["flat/recommended"],
  {
    files: ["**/*.{ts,vue}"],
    languageOptions: {
      globals: globals.browser,
      parserOptions: {
        parser: tseslint.parser,
        projectService: true,
        extraFileExtensions: [".vue"],
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-unsafe-assignment": "error",
      "@typescript-eslint/no-unsafe-member-access": "error",
      "@typescript-eslint/no-unsafe-call": "error",
      "@typescript-eslint/no-unsafe-return": "error",
      "@typescript-eslint/no-unsafe-argument": "error",
      "@typescript-eslint/restrict-template-expressions": ["error", { allowNumber: true }],
      "no-restricted-globals": [
        "error",
        { name: "localStorage", message: "Credentials and session state must stay in memory (G35.1)." },
        { name: "sessionStorage", message: "Credentials and session state must stay in memory (G35.1)." },
        { name: "eval", message: "Breaks the strict Content-Security-Policy." },
      ],
      "no-restricted-properties": [
        "error",
        { object: "window", property: "localStorage", message: "Credentials must stay in memory (G35.1)." },
        { object: "window", property: "sessionStorage", message: "Credentials must stay in memory (G35.1)." },
        { object: "document", property: "cookie", message: "Script must not read or write cookies (G35.1)." },
      ],
      "no-new-func": "error",
      "vue/no-v-html": "error",
      "vue/no-v-text-v-html-on-component": "error",
      "vue/multi-word-component-names": "off",
      "vue/max-attributes-per-line": "off",
      "vue/singleline-html-element-content-newline": "off",
      "vue/html-self-closing": ["error", { html: { void: "always", normal: "always", component: "always" } }],
    },
  },
  {
    files: ["**/*.vue"],
    plugins: { "@intlify/vue-i18n": vueI18n },
    settings: {
      "vue-i18n": {
        localeDir: { pattern: "./src/i18n/*/*.json", localeKey: "path", localePattern: /^.*\/(?<locale>[A-Za-z]{2}-[A-Za-z]{2})\/.*\.json$/ },
        messageSyntaxVersion: "^11.0.0",
      },
    },
    rules: {
      "@intlify/vue-i18n/no-raw-text": ["error", { ignorePattern: "^[\\s\\p{P}\\p{S}]*$" }],
      "@intlify/vue-i18n/no-missing-keys": "error",
      "@intlify/vue-i18n/no-v-html": "error",
    },
  },
  {
    // The single storage adapter for presentation state without a server
    // API yet (layouts, plugin state, administrator CSS). It refuses keys
    // naming credentials; see the file header and docs/frontend-adr.md.
    files: ["src/stores/persist.ts"],
    rules: {
      "no-restricted-globals": ["error", { name: "sessionStorage", message: "Use src/stores/persist.ts." }, { name: "eval", message: "Breaks the strict Content-Security-Policy." }],
    },
  },
  {
    // @jelee/plugin-sdk stands alone: it may import Vue and nothing of the host.
    files: ["src/plugins/sdk/**/*.ts"],
    ignores: ["**/*.test.ts"],
    rules: {
      "no-restricted-imports": ["error", { patterns: [{ group: ["@/*", "../host/*", "../official/*"], message: "The SDK must not depend on the host application." }] }],
    },
  },
  {
    // Plugin sources (G32.3): only "vue" and "@jelee/plugin-sdk". No host
    // modules, stores, router or i18n internals, no injection tricks, and no
    // direct network or storage access: data comes from the SDK's restricted
    // API, settings from the plugin's own namespace.
    files: ["src/plugins/official/**/*.{ts,vue}"],
    ignores: ["**/*.test.ts"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            { name: "vue", importNames: ["getCurrentInstance", "inject", "provide", "createApp"], message: "Use usePlugin() from @jelee/plugin-sdk." },
            { name: "vue-i18n", message: "Use the plugin context's t()." },
            { name: "vue-router", message: "Use route.register and RouterLink." },
            { name: "pinia", message: "Plugins keep state in their settings namespace." },
            { name: "openapi-fetch", message: "Use the plugin context's api." },
          ],
          patterns: [{ group: ["@/*", "**/host/*", "**/sdk/*"], message: "Plugins import only vue and @jelee/plugin-sdk." }],
        },
      ],
      "no-restricted-globals": [
        "error",
        ...["localStorage", "sessionStorage", "indexedDB", "caches", "fetch", "XMLHttpRequest", "WebSocket", "EventSource", "eval"].map((name) => ({
          name,
          message: "Plugins use the SDK's restricted API and settings namespace (G32.3).",
        })),
      ],
      "no-restricted-properties": [
        "error",
        { object: "document", property: "cookie", message: "Plugins never touch cookies (G32.3)." },
        { object: "window", property: "fetch", message: "Use the plugin context's api." },
        { object: "window", property: "localStorage", message: "Use the plugin context's settings." },
        { object: "globalThis", property: "fetch", message: "Use the plugin context's api." },
        { object: "navigator", property: "sendBeacon", message: "Plugins do not send data out of band." },
      ],
      // Plugin messages live in the plugin, not in the host catalogs.
      "@intlify/vue-i18n/no-missing-keys": "off",
    },
  },
  {
    // Tests inspect browser storage and cookies to prove they stay empty.
    files: ["**/*.test.ts"],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
      "no-restricted-globals": "off",
      "no-restricted-properties": "off",
    },
  },
  {
    files: ["*.js", "scripts/**/*.mjs"],
    ...tseslint.configs.disableTypeChecked,
    languageOptions: { globals: globals.node },
  },
);
