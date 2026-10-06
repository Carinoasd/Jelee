import { describe, expect, it } from "vitest";
import { clockMinutes, newWindowDraft, windowBody, windowDraftOf, windowProblems, windowsOf } from "./windows";

describe("time window form", () => {
  it("reads HH:MM clocks, 24:00 only as an end", () => {
    expect(clockMinutes("00:00", false)).toBe(0);
    expect(clockMinutes("23:59", false)).toBe(23 * 60 + 59);
    expect(clockMinutes("24:00", false)).toBeNull();
    expect(clockMinutes("24:00", true)).toBe(24 * 60);
    expect(clockMinutes("24:01", true)).toBeNull();
    expect(clockMinutes("9:30", false)).toBeNull();
    expect(clockMinutes("12:60", false)).toBeNull();
  });

  it("builds the request body, omitting every-day and hide-all", () => {
    const draft = { ...newWindowDraft("Asia/Taipei"), weekdays: [6, 0, 6], start: " 22:00 ", end: "06:00" };
    expect(windowBody(draft)).toEqual({ weekdays: [0, 6], start: "22:00", end: "06:00", timeZone: "Asia/Taipei" });
    expect(windowBody({ ...draft, weekdays: [0, 1, 2, 3, 4, 5, 6], mode: "ceiling", ratingMax: "12" })).toEqual({
      start: "22:00",
      end: "06:00",
      timeZone: "Asia/Taipei",
      ratingMax: 12,
    });
    expect(windowsOf([{ weekdays: [], start: "08:00", end: "09:00", timeZone: "UTC", ratingMax: 0 }])).toEqual([
      { start: "08:00", end: "09:00", timeZone: "UTC", ratingMax: 0 },
    ]);
    expect(windowDraftOf({ start: "08:00", end: "09:00", timeZone: "UTC" }).mode).toBe("hide");
  });

  it("reports the problems of a window", () => {
    const draft = newWindowDraft("UTC");
    expect(Object.values(windowProblems(draft)).every((problem) => problem === null)).toBe(true);
    expect(windowProblems({ ...draft, start: "24:00" }).start).toBe("contentRules.windows.badStart");
    expect(windowProblems({ ...draft, start: "10:00", end: "10:00" }).end).toBe("contentRules.windows.sameTimes");
    expect(windowProblems({ ...draft, timeZone: "Local" }).timeZone).toBe("contentRules.windows.badZone");
    expect(windowProblems({ ...draft, timeZone: " " }).timeZone).toBe("contentRules.windows.badZone");
    expect(windowProblems({ ...draft, mode: "ceiling" }).ratingMax).toBe("contentRules.windows.ceilingRequired");
  });
});
