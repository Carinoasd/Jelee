import { describe, expect, it } from "vitest";
import { cachedRequest, clearRequestCache, requestCacheLimit, requestCacheTtlMs } from "./requestCache";

describe("request cache", () => {
  it("shares requests in flight and reuses answers until they expire", async () => {
    const owner = {};
    let now = 1_000;
    let calls = 0;
    const load = () => {
      calls++;
      return Promise.resolve(calls);
    };
    const options = { now: () => now };
    const [a, b] = await Promise.all([cachedRequest(owner, "k", load, options), cachedRequest(owner, "k", load, options)]);
    expect([a, b, calls]).toEqual([1, 1, 1]);
    now += requestCacheTtlMs - 1;
    expect(await cachedRequest(owner, "k", load, options)).toBe(1);
    expect(await cachedRequest(owner, "k", load, { ...options, force: true })).toBe(2);
    now += requestCacheTtlMs;
    expect(await cachedRequest(owner, "k", load, options)).toBe(3);
    // Another owner (API client) never sees these entries.
    expect(await cachedRequest({}, "k", load, options)).toBe(4);
    clearRequestCache(owner);
    expect(await cachedRequest(owner, "k", load, options)).toBe(5);
  });

  it("never caches failures and keeps a bounded number of entries", async () => {
    const owner = {};
    let fail = true;
    const load = () => (fail ? Promise.reject(new Error("down")) : Promise.resolve("ok"));
    await expect(cachedRequest(owner, "k", load)).rejects.toThrow("down");
    fail = false;
    expect(await cachedRequest(owner, "k", load)).toBe("ok");

    let loads = 0;
    const count = () => Promise.resolve(++loads);
    for (let n = 0; n <= requestCacheLimit; n++) {
      await cachedRequest(owner, `n${String(n)}`, count);
    }
    // The oldest entry was evicted; the newest is still cached.
    expect(await cachedRequest(owner, "n0", count)).toBe(requestCacheLimit + 2);
    expect(await cachedRequest(owner, `n${String(requestCacheLimit)}`, count)).toBe(requestCacheLimit + 1);
  });
});
