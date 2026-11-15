import { expect, test } from "vitest";
import { createConsistencyScrollGroup } from "./consistencyTimeline";
import {
  consistencyChartSlots,
  CONSISTENCY_CHARTS,
  isPerfectConsistencyMonth,
  consistencyTitle,
} from "./consistency";
import type { ConsistencyBucket } from "./api";

function pane(max = 1000) {
  const el = document.createElement("div");
  Object.defineProperties(el, {
    clientWidth: { value: 600, configurable: true },
    scrollWidth: { value: 600 + max, configurable: true },
  });
  return el;
}
test("locks wheel/drag/keyboard offsets without programmatic-scroll feedback and restores reopened charts", () => {
  const group = createConsistencyScrollGroup(),
    a = pane(),
    b = pane(),
    c = pane();
  group.register(a);
  group.register(b);
  const remove = group.register(c);
  group.reconcile();
  expect([a.scrollLeft, b.scrollLeft, c.scrollLeft]).toEqual([1000, 1000, 1000]);
  group.synchronize(a);
  group.synchronize(b);
  group.synchronize(c);
  a.scrollLeft = 350;
  group.synchronize(a);
  expect([a.scrollLeft, b.scrollLeft, c.scrollLeft]).toEqual([350, 350, 350]);
  group.synchronize(b);
  group.synchronize(c);
  expect(a.scrollLeft).toBe(350);
  remove();
  c.scrollLeft = 0;
  b.scrollLeft = 200;
  group.synchronize(b);
  group.register(c);
  group.reconcile();
  expect([a.scrollLeft, b.scrollLeft, c.scrollLeft]).toEqual([200, 200, 200]);
});
test("alignment padding preserves variable axis/grid origins and a shared end offset", () => {
  for (const count of [2, 156])
    for (const scroll of [0, 200, 1000]) {
      const axes = [19, 48, 82.5],
        maximum = Math.max(...axes),
        width = 1400,
        label = 56,
        positions = [];
      for (const axis of axes) {
        const lead = maximum - axis,
          slots = consistencyChartSlots(count, label, width - axis, scroll, lead);
        positions.push(axis + lead + slots.slot - scroll);
        const nativeMax = Math.max(
          0,
          Math.ceil((count - 1) * slots.slot + label + maximum - width),
        );
        expect(nativeMax).toBe(Math.max(0, Math.ceil((count - 1) * 68 + 56 + maximum - width)));
      }
      expect(new Set(positions).size).toBe(1);
    }
});
test("awards only completed calendar months with leap and century rules", () => {
  for (const [date, days] of [
    ["2024-02-01", 29],
    ["2025-02-01", 28],
    ["2000-02-01", 29],
    ["2100-02-01", 28],
    ["2026-04-01", 30],
    ["2026-01-01", 31],
  ] as const) {
    const bucket = { bucketStart: date, activeDays: days } as ConsistencyBucket;
    expect(isPerfectConsistencyMonth(bucket, "month", CONSISTENCY_CHARTS[0])).toBe(true);
    expect(
      isPerfectConsistencyMonth(
        { ...bucket, activeDays: days - 1 },
        "month",
        CONSISTENCY_CHARTS[0],
      ),
    ).toBe(false);
    expect(isPerfectConsistencyMonth(bucket, "week", CONSISTENCY_CHARTS[0])).toBe(false);
    expect(isPerfectConsistencyMonth(bucket, "month", CONSISTENCY_CHARTS[1])).toBe(false);
  }
  expect(consistencyTitle(CONSISTENCY_CHARTS[4], "week")).toBe(
    "Longest duration per workout per week",
  );
});
