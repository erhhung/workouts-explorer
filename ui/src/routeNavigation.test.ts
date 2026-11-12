import { describe, expect, test } from "vitest";
import { rangeForWorkoutNavigation, shortcutDates } from "./routeNavigation";

const prefs = { timezone: "UTC", firstWeekday: "monday" } as const;
const now = new Date("2026-11-09T12:00:00Z");

describe("range-aware workout navigation", () => {
  test.each([
    ["2026-11-09", "thisWeek"],
    ["2026-11-04", "lastWeek"],
    ["2026-10-25", "last30Days"],
    ["2026-08-03", "thisYear"],
    ["2025-05-01", "lastYear"],
    ["2022-03-02", "allTime"],
    ["2030-01-01", "allTime"],
  ])("chooses the first ordered shortcut covering %s", (date, expected) => {
    expect(rangeForWorkoutNavigation("2020-01-01/2020-01-31", date, prefs, now)).toBe(expected);
  });

  test.each(["2026-10-01/2026-11-30", "thisYear", "allTime"] as const)(
    "preserves an already encompassing %s selection",
    (current) => {
      expect(rangeForWorkoutNavigation(current, "2026-11-09", prefs, now)).toBe(current);
    },
  );

  test("uses the selected time zone and first weekday at a UTC day boundary", () => {
    const date = new Date("2026-11-09T01:00:00Z");
    expect(shortcutDates("thisWeek", { ...prefs, timezone: "America/Los_Angeles" }, date)).toEqual({
      startDate: "2026-11-02",
      endDate: "2026-11-08",
    });
    expect(shortcutDates("thisWeek", { ...prefs, firstWeekday: "sunday" }, date)).toEqual({
      startDate: "2026-11-08",
      endDate: "2026-11-14",
    });
  });

  test("does not manufacture finite bounds for All time or exclude undated targets", () => {
    expect(shortcutDates("allTime", prefs, now)).toBeUndefined();
    expect(rangeForWorkoutNavigation("thisWeek", null, prefs, now)).toBe("allTime");
  });

  test("handles leap months and calendar-year boundaries", () => {
    expect(shortcutDates("lastMonth", prefs, new Date("2024-03-31T12:00:00Z"))).toEqual({
      startDate: "2024-02-01",
      endDate: "2024-02-29",
    });
    expect(shortcutDates("lastYear", prefs, new Date("2024-01-01T12:00:00Z"))).toEqual({
      startDate: "2023-01-01",
      endDate: "2023-12-31",
    });
  });
});
