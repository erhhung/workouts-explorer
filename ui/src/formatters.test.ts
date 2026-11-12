import { describe, expect, test } from "vitest";
import { dateTimeFormatter, numberFormatter } from "./formatters";
import { zoneAbbreviation, zoneOffsetAt } from "./Tooltip";

describe("shared Intl formatters", () => {
  test("reuses equivalent option sets without depending on property order", () => {
    expect(dateTimeFormatter("en-US", { timeZone: "UTC", weekday: "short" })).toBe(
      dateTimeFormatter("en-US", { weekday: "short", timeZone: "UTC", hour: undefined }),
    );
    expect(numberFormatter(undefined, { maximumFractionDigits: 2 })).toBe(
      numberFormatter(undefined, { maximumFractionDigits: 2 }),
    );
  });

  test("keeps time zones, clock formats, locales and numeric precision independent", () => {
    const instant = new Date("2026-07-01T19:12:00Z");
    for (const timeZone of ["UTC", "America/Denver", "Pacific/Chatham"]) {
      for (const hour12 of [true, false]) {
        const options = { dateStyle: "medium", timeStyle: "short", timeZone, hour12 } as const;
        expect(dateTimeFormatter("en-US", options).format(instant)).toBe(
          new Intl.DateTimeFormat("en-US", options).format(instant),
        );
      }
    }
    for (const locale of [undefined, "en-US", "de-DE"]) {
      for (const maximumFractionDigits of [0, 2]) {
        expect(numberFormatter(locale, { maximumFractionDigits }).format(12345.678)).toBe(
          new Intl.NumberFormat(locale, { maximumFractionDigits }).format(12345.678),
        );
      }
    }
  });

  test("computes each instant's offset and abbreviation across DST and half-hour zones", () => {
    const before = new Date("2026-03-08T08:59:00Z");
    const after = new Date("2026-03-08T09:01:00Z");
    expect(zoneOffsetAt("America/Denver", before)).toBe(-420);
    expect(zoneOffsetAt("America/Denver", after)).toBe(-360);
    expect(zoneAbbreviation("America/Denver", before)).toBe("MST");
    expect(zoneAbbreviation("America/Denver", after)).toBe("MDT");
    expect(zoneOffsetAt("Asia/Kolkata", after)).toBe(330);
    expect(zoneOffsetAt("Pacific/Chatham", after)).toBe(825);
  });

  test("does not substitute a cached formatter for an invalid time zone", () => {
    expect(zoneOffsetAt("UTC", new Date(0))).toBe(0);
    expect(zoneOffsetAt("Not/AZone", new Date(0))).toBeNull();
    expect(() => dateTimeFormatter("en-US", { timeZone: "Not/AZone" })).toThrow(RangeError);
    expect(zoneOffsetAt("UTC", new Date(0))).toBe(0);
  });

  test("bounds the date cache while retaining recently used entries", () => {
    const first = dateTimeFormatter("en-AU", { timeZone: "Pacific/Honolulu", year: "numeric" });
    let last: Intl.DateTimeFormat | undefined;
    for (const locale of ["en-US", "en-GB", "de-DE", "fr-FR", "ja-JP"]) {
      for (let offset = 0; offset <= 12; offset++) {
        last = dateTimeFormatter(locale, { timeZone: `Etc/GMT+${offset}` });
      }
    }
    expect(dateTimeFormatter("en-AU", { timeZone: "Pacific/Honolulu", year: "numeric" })).not.toBe(
      first,
    );
    expect(dateTimeFormatter("ja-JP", { timeZone: "Etc/GMT+12" })).toBe(last);
  });

  test("bounds numeric formatter configuration without retaining formatted values", () => {
    const first = numberFormatter("en-US", { style: "currency", currency: "AED" });
    let last: Intl.NumberFormat | undefined;
    for (const currency of [
      "USD",
      "EUR",
      "GBP",
      "JPY",
      "KRW",
      "AUD",
      "CAD",
      "CHF",
      "CNY",
      "INR",
      "BRL",
      "MXN",
      "NZD",
      "SEK",
      "NOK",
      "DKK",
      "PLN",
      "ZAR",
    ]) {
      last = numberFormatter("en-US", { style: "currency", currency });
    }
    expect(numberFormatter("en-US", { style: "currency", currency: "AED" })).not.toBe(first);
    expect(numberFormatter("en-US", { style: "currency", currency: "ZAR" })).toBe(last);
    expect(last!.format(12)).not.toBe(last!.format(13));
  });
});
