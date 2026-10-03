import { describe, expect, it } from "vitest";
import { ApiError } from "./errors";
import { transition, useRequest, type RequestState } from "./requestState";

describe("request state machine", () => {
  it("moves idle -> loading -> success, empty or error", () => {
    const idle: RequestState<number> = { status: "idle" };
    const loading = transition(idle, { type: "start" });
    expect(loading).toEqual({ status: "loading", previous: null });
    expect(transition(loading, { type: "resolve", data: 3, empty: false })).toEqual({ status: "success", data: 3 });
    expect(transition(loading, { type: "resolve", data: 0, empty: true })).toEqual({ status: "empty" });
    const error = new ApiError("not_ready", 503, "x");
    expect(transition(loading, { type: "reject", error })).toEqual({ status: "error", error });
  });

  it("ignores results that arrive outside the loading state", () => {
    const success: RequestState<number> = { status: "success", data: 1 };
    expect(transition(success, { type: "resolve", data: 2, empty: false })).toBe(success);
    expect(transition({ status: "idle" }, { type: "reject", error: new ApiError("forbidden", 403, "") })).toEqual({
      status: "idle",
    });
    expect(transition(success, { type: "start" })).toEqual({ status: "loading", previous: 1 });
  });

  it("drops a slow earlier response in favour of the newest request", async () => {
    const resolvers: ((value: number) => void)[] = [];
    const request = useRequest(() => new Promise<number>((resolve) => resolvers.push(resolve)));
    const first = request.run();
    const second = request.run();
    resolvers[1]!(2);
    await second;
    resolvers[0]!(1);
    await first;
    expect(request.state.value).toEqual({ status: "success", data: 2 });
  });

  it("normalizes thrown values to ApiError", async () => {
    const request = useRequest<number>(() => Promise.reject(new TypeError("offline")));
    await request.run();
    const state = request.state.value;
    expect(state.status).toBe("error");
    expect(state.status === "error" && state.error.code).toBe("network_error");
  });
});
