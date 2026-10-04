import { describe, expect, it } from "vitest";
import { formatDateTime } from "@/i18n/format";
import { orderSessions, type Session } from "./api";

const session = (id: string, lastSeenAt?: string): Session => ({
  id,
  userId: "u",
  clientKind: "web",
  deviceName: id,
  createdAt: "2026-10-01T00:00:00Z",
  expiresAt: "2026-11-01T00:00:00Z",
  ...(lastSeenAt ? { lastSeenAt } : {}),
});

describe("orderSessions", () => {
  it("puts the current session first, then the most recently used", () => {
    const ordered = orderSessions(
      [session("old"), session("recent", "2026-10-04T00:00:00Z"), session("mine", "2026-10-02T00:00:00Z")],
      "mine",
    );
    expect(ordered.map((entry) => entry.id)).toEqual(["mine", "recent", "old"]);
  });
});

describe("formatDateTime", () => {
  it("formats valid timestamps for the locale and ignores bad input", () => {
    expect(formatDateTime("2026-10-04T12:00:00Z", "en-US")).toMatch(/2026/);
    expect(formatDateTime("not a date", "en-US")).toBe("");
    expect(formatDateTime(undefined, "en-US")).toBe("");
  });
});
