import { describe, expect, test } from "vitest";
import {
  formatDateOnly,
  formatEnglishDate,
  formatDateEntry,
  parseDateEntry,
  type DateFormat,
} from "./date";

describe("English preference-aware dates", () => {
  test.each([
    ["mm/dd/yyyy", "Oct 5, 2026", "10/5/2026", "October 2026", "Oct 2026", "10/5/2026"],
    ["dd/mm/yyyy", "5 Oct 2026", "5/10/2026", "October 2026", "Oct 2026", "5/10/2026"],
    ["yyyy/mm/dd", "2026 Oct 5", "2026/10/5", "2026 October", "2026 Oct", "2026/10/5"],
  ] as const)(
    "orders all common date presentations for %s",
    (order, written, numeric, longMonth, shortMonth, entry) => {
      const date = new Date("2026-10-05T12:00:00Z");
      expect(formatDateOnly("2026-10-05", order)).toBe(written);
      expect(
        formatEnglishDate(
          date,
          { year: "numeric", month: "numeric", day: "numeric", timeZone: "UTC" },
          order,
        ),
      ).toBe(numeric);
      expect(
        formatEnglishDate(date, { year: "numeric", month: "long", timeZone: "UTC" }, order),
      ).toBe(longMonth);
      expect(
        formatEnglishDate(date, { year: "numeric", month: "short", timeZone: "UTC" }, order),
      ).toBe(shortMonth);
      expect(formatDateEntry("2026-10-05", order)).toBe(entry);
      expect(parseDateEntry(entry, order)).toBe("2026-10-05");
    },
  );
  test("calendar dates do not shift days and timestamp dates use their actual time zone", () => {
    const instant = new Date("2026-01-01T00:30:00Z");
    expect(
      formatEnglishDate(
        instant,
        { dateStyle: "medium", timeZone: "America/Los_Angeles" },
        "dd/mm/yyyy",
      ),
    ).toBe("31 Dec 2025");
    expect(formatEnglishDate(instant, { dateStyle: "medium", timeZone: "UTC" }, "yyyy/mm/dd")).toBe(
      "2026 Jan 1",
    );
    expect(formatDateOnly("2026-01-01", "dd/mm/yyyy")).toBe("1 Jan 2026");
  });
  test.each([
    ["mm/dd/yyyy", "7/4/2026"],
    ["dd/mm/yyyy", "4/7/2026"],
    ["yyyy/mm/dd", "2026/7/4"],
  ] as const)("numeric dates never pad month/day in %s", (order, expected) => {
    const value = new Date("2026-07-04T12:00:00Z");
    expect(
      formatEnglishDate(
        value,
        { month: "2-digit", day: "2-digit", year: "numeric", timeZone: "UTC" },
        order,
      ),
    ).toBe(expected);
    expect(formatDateEntry("2026-07-04", order)).toBe(expected);
    expect(parseDateEntry(expected, order)).toBe("2026-07-04");
  });
  test("timestamp ordering preserves clock precision, zone and DST behavior", () => {
    for (const value of ["2026-03-08T09:30:00Z", "2026-03-08T10:30:00Z"]) {
      const date = new Date(value),
        options = {
          dateStyle: "medium",
          timeStyle: "short",
          timeZone: "America/Los_Angeles",
          hour12: false,
        } as const;
      const native = new Intl.DateTimeFormat("en-US", options).format(date);
      expect(formatEnglishDate(date, options, "dd/mm/yyyy")).toBe(
        native.replace("Mar 8, 2026", "8 Mar 2026"),
      );
    }
  });
  test.each(["mm/dd/yyyy", "dd/mm/yyyy", "yyyy/mm/dd"] as DateFormat[])(
    "rejects impossible dates without guessing in %s",
    (order) => {
      for (const value of ["2026-02-30", "31/31/2026", "2026/13/02", ""]) {
        expect(parseDateEntry(value, order)).toBe("");
      }
      expect(parseDateEntry("2024-02-29", order)).toBe("2024-02-29");
      expect(formatDateOnly("2026-02-30", order)).toBe("2026-02-30");
    },
  );
  test("ambiguous numeric entry follows the selected order and mobile compact entry is supported", () => {
    expect(parseDateEntry("10/05/2026", "mm/dd/yyyy")).toBe("2026-10-05");
    expect(parseDateEntry("10/05/2026", "dd/mm/yyyy")).toBe("2026-05-10");
    expect(parseDateEntry("05102026", "dd/mm/yyyy")).toBe("2026-10-05");
  });
});
