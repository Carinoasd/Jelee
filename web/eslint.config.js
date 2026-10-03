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
