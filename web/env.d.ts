/// <reference types="vite/client" />

// Lets non-Vue-aware tools (typed ESLint) resolve single-file components;
// vue-tsc type-checks the real component files.
declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent;
  export default component;
}
