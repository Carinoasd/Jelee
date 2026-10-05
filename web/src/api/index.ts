// The API context is created once in main.ts and provided to the app, so
// stores and features receive it by injection instead of a module global.
import { inject, type InjectionKey } from "vue";
import type { createApiClient } from "./client";

export type ApiContext = ReturnType<typeof createApiClient>;

export const apiKey: InjectionKey<ApiContext> = Symbol("jelee.api");

export function useApi(): ApiContext {
  const context = inject(apiKey, null);
  if (context === null) {
    throw new Error("API context is not provided");
  }
  return context;
}
