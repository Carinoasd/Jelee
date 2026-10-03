// Request state machine shared by every view (G31.5): idle -> loading ->
// success | empty | error. Results from superseded requests are ignored so a
// slow earlier response can never overwrite a newer one.
import { shallowRef, type ShallowRef } from "vue";
import { ApiError, networkError } from "./errors";

export type RequestState<T> =
  | { readonly status: "idle" }
  | { readonly status: "loading"; readonly previous: T | null }
  | { readonly status: "success"; readonly data: T }
  | { readonly status: "empty" }
  | { readonly status: "error"; readonly error: ApiError };

export type RequestEvent<T> =
  | { readonly type: "start" }
  | { readonly type: "resolve"; readonly data: T; readonly empty: boolean }
  | { readonly type: "reject"; readonly error: ApiError }
  | { readonly type: "reset" };

export function transition<T>(state: RequestState<T>, event: RequestEvent<T>): RequestState<T> {
  switch (event.type) {
    case "start":
      return { status: "loading", previous: state.status === "success" ? state.data : null };
    case "resolve":
      if (state.status !== "loading") {
        return state;
      }
      return event.empty ? { status: "empty" } : { status: "success", data: event.data };
    case "reject":
      if (state.status !== "loading") {
        return state;
      }
      return { status: "error", error: event.error };
    case "reset":
      return { status: "idle" };
  }
}

export interface RequestHandle<T> {
  readonly state: Readonly<ShallowRef<RequestState<T>>>;
  readonly run: () => Promise<void>;
  readonly reset: () => void;
}

export function useRequest<T>(load: () => Promise<T>, isEmpty: (data: T) => boolean = () => false): RequestHandle<T> {
  const state = shallowRef<RequestState<T>>({ status: "idle" });
  let generation = 0;
  const dispatch = (event: RequestEvent<T>) => {
    state.value = transition(state.value, event);
  };
  return {
    state,
    run: async () => {
      const current = ++generation;
      dispatch({ type: "start" });
      try {
        const data = await load();
        if (current === generation) {
          dispatch({ type: "resolve", data, empty: isEmpty(data) });
        }
      } catch (cause: unknown) {
        if (current === generation) {
          dispatch({ type: "reject", error: cause instanceof ApiError ? cause : networkError(cause) });
        }
      }
    },
    reset: () => {
      generation++;
      dispatch({ type: "reset" });
    },
  };
}
