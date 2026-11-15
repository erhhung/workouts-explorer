import { describe, expect, test } from "vitest";
import {
  CONSISTENCY_CHARTS,
  consistencyBucketLabel,
  consistencyChartSlots,
  consistencyScale,
  formatConsistencyValue,
  consistencyValue,
  consistencyTooltipLabel,
  consistencyTickLabels,
} from "./consistency";
import type { ConsistencyBucket } from "./api";

describe("Consistency chart contracts", () => {
  test("keeps the requested fixed active-day scales", () => {
    expect(consistencyScale([0, 1, 6], CONSISTENCY_CHARTS[0], "week")).toEqual({
      max: 7,
      ticks: [2, 4, 6],
    });
    expect(consistencyScale([0, 29], CONSISTENCY_CHARTS[0], "month")).toEqual({
      max: 31,
      ticks: [7, 14, 21, 28],
    });
  });
  test("puts every positive non-outlier peak in the top quarter of four equal intervals", () => {
    const peaks = [
      0.001, 0.01, 0.1, 1, 2, 3, 4, 5, 9, 10, 10.001, 12, 14, 24, 28, 39.9, 40, 41, 59, 60, 61, 90,
      120, 240, 280, 300, 3599, 3600, 4000, 7200, 99999, 1000000,
    ];
    for (const chart of CONSISTENCY_CHARTS.filter((chart) => chart.kind !== "days"))
      for (const peak of peaks) {
        const scale = consistencyScale([peak / 2, peak * 0.75, peak], chart, "week");
        expect(peak / scale.max, `${chart.id} peak ${peak}`).toBeGreaterThanOrEqual(0.75 - 1e-12);
        expect(peak / scale.max, `${chart.id} peak ${peak}`).toBeLessThanOrEqual(1 + 1e-12);
        expect(scale.ticks).toHaveLength(4);
        scale.ticks.forEach((tick, index) =>
          expect(tick).toBeCloseTo((scale.max * (index + 1)) / 4, 8),
        );
        const labels = consistencyTickLabels(scale.ticks, chart, "imperial");
        expect(new Set(labels).size).toBe(4);
        expect(labels.every((label) => !/^0(?:\\s|$)/.test(label))).toBe(true);
      }
    const clipped = consistencyScale([10, 11, 12, 13, 10000], CONSISTENCY_CHARTS[2], "week");
    expect(13 / clipped.max).toBeGreaterThanOrEqual(0.75);
    expect(clipped.max).toBeLessThan(10000);
  });
  test("prevents rare extremes from dwarfing ordinary values without modifying the data", () => {
    const values = [10, 11, 12, 13, 10000];
    const original = [...values];
    const scale = consistencyScale(values, CONSISTENCY_CHARTS[2], "week");
    expect(scale.max).toBeLessThan(100);
    expect(scale.max).toBeGreaterThanOrEqual(13);
    expect(scale.ticks).toHaveLength(4);
    expect(scale.ticks.every((v) => v > 0)).toBe(true);
    expect(values).toEqual(original);
    expect(
      consistencyScale([10, 20, 30, 40], CONSISTENCY_CHARTS[2], "week").max,
    ).toBeGreaterThanOrEqual(40);
  });
  test("uses Workout log formatting and preferred distance units", () => {
    expect(formatConsistencyValue(7260, CONSISTENCY_CHARTS[1], "imperial")).toBe("2h 01m");
    expect(formatConsistencyValue(1234.56, CONSISTENCY_CHARTS[3], "imperial")).toBe("1,235 kcal");
    const bucket = { distance: { value: "1.609344", unit: "km" } } as ConsistencyBucket;
    expect(
      formatConsistencyValue(
        consistencyValue(bucket, CONSISTENCY_CHARTS[2], "imperial"),
        CONSISTENCY_CHARTS[2],
        "imperial",
      ),
    ).toBe("1 mi");
    expect(
      formatConsistencyValue(
        consistencyValue(bucket, CONSISTENCY_CHARTS[2], "metric"),
        CONSISTENCY_CHARTS[2],
        "metric",
      ),
    ).toBe("1.61 km");
    expect(formatConsistencyValue(null, CONSISTENCY_CHARTS[3], "metric")).toBe("n/a");
  });
  test("uses compact weekly labels and expanded, unpadded tooltip dates", () => {
    expect(consistencyBucketLabel("2026-01-05", "week")).toBe("1/5/26");
    expect(consistencyBucketLabel("2026-10-01", "month")).toBe("Oct 2026");
    expect(consistencyTooltipLabel("2022-02-07", "week")).toBe("Week of 2/7/2022");
    expect(consistencyTooltipLabel("2022-08-01", "month")).toBe("August 2022");
  });
  test("bounds rendered windows while preserving the complete timeline width and last label", () => {
    const first = consistencyChartSlots(1000, 80, 600, 0),
      last = consistencyChartSlots(1000, 80, 600, 999 * 92 + 80 - 600);
    expect(first.first).toBe(0);
    expect(first.last).toBeLessThan(20);
    expect(first.slot).toBe(92);
    expect(first.width).toBe(999 * 92 + 80);
    expect(last.last).toBe(1000);
  });
});
