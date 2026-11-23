import { expect, test } from "vitest";
import { elapsedTotal } from "./osmRegions";

test.each([
  [0, "0.00s total"],
  [0.879, "0.88s total"],
  [2.744, "2.74s total"],
  [2.45, "2.45s total"],
  [9.999, "10.00s total"],
  [10, "10s total"],
  [10.999, "10s total"],
  [924.999, "15m 24s total"],
  [59063.5, "16h 24m 23s total"],
])("formats %s successful seconds as %s", (seconds, expected) => {
  expect(elapsedTotal(seconds)).toBe(expected);
});
