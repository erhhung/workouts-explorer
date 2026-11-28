import type { ConsistencyBucket, ConsistencyGrouping, Preferences } from "./api";
import { numberFormatter } from "./formatters";
import { formatEnglishDate, type DateFormat } from "./date";
import { formatDuration, formatWorkoutMetric } from "./Summary";

export const CONSISTENCY_CHARTS = [
  { id: "activeDays", title: "Active days", kind: "days", perWorkout: false },
  { id: "duration", title: "Workout duration", kind: "duration", perWorkout: false },
  { id: "distance", title: "Distance covered", kind: "distance", perWorkout: false },
  { id: "calories", title: "Calories burned", kind: "calories", perWorkout: false },
  {
    id: "longestDuration",
    title: "Longest duration per workout",
    kind: "duration",
    perWorkout: true,
  },
  {
    id: "longestDistance",
    title: "Longest distance per workout",
    kind: "distance",
    perWorkout: true,
  },
  {
    id: "highestCalories",
    title: "Highest calories per workout",
    kind: "calories",
    perWorkout: true,
  },
] as const;
export type ConsistencyChart = (typeof CONSISTENCY_CHARTS)[number];
export type ConsistencyChartId = ConsistencyChart["id"];
export interface ConsistencyViewState {
  groupBy: ConsistencyGrouping;
  closed: ConsistencyChartId[];
}
export const DEFAULT_CONSISTENCY_STATE: ConsistencyViewState = { groupBy: "week", closed: [] };
export const CONSISTENCY_BAR_GAP = 12;

export function consistencyTitle(chart: ConsistencyChart, group: ConsistencyGrouping) {
  return `${chart.title} per ${group}`;
}

export function isPerfectConsistencyMonth(
  bucket: ConsistencyBucket,
  group: ConsistencyGrouping,
  chart: ConsistencyChart,
) {
  if (group !== "month" || chart.id !== "activeDays") return false;
  const year = Number(bucket.bucketStart.slice(0, 4)),
    month = Number(bucket.bucketStart.slice(5, 7));
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  return (
    bucket.activeDays === [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]
  );
}

export function consistencyBucketLabel(
  value: string,
  group: ConsistencyGrouping,
  order?: DateFormat,
) {
  if (group === "week")
    return formatEnglishDate(
      new Date(value + "T00:00:00Z"),
      { month: "numeric", day: "numeric", year: "2-digit", timeZone: "UTC" },
      order,
    );
  return formatEnglishDate(
    new Date(value + "T00:00:00Z"),
    { month: "short", year: "numeric", timeZone: "UTC" },
    order,
  );
}

export function consistencyTooltipLabel(
  value: string,
  group: ConsistencyGrouping,
  order?: DateFormat,
) {
  if (group === "week")
    return `Week of ${formatEnglishDate(new Date(value + "T00:00:00Z"), { month: "numeric", day: "numeric", year: "numeric", timeZone: "UTC" }, order)}`;
  return formatEnglishDate(
    new Date(value + "T00:00:00Z"),
    { month: "long", year: "numeric", timeZone: "UTC" },
    order,
  );
}

export function consistencyValue(
  bucket: ConsistencyBucket,
  chart: ConsistencyChart,
  units: Preferences["units"],
): number | null {
  const raw = bucket[chart.id];
  if (raw === null) return null;
  const value = Number(typeof raw === "object" ? raw.value : raw);
  return chart.kind === "distance" && units === "imperial" ? value * 0.621371192 : value;
}

export function formatConsistencyValue(
  value: number | null,
  chart: ConsistencyChart,
  units: Preferences["units"],
  tooltip = false,
): string {
  if (value === null) return "n/a";
  if (chart.kind === "days")
    return tooltip ? `${value} ${value === 1 ? "day" : "days"}` : String(value);
  if (chart.kind === "duration") return formatDuration(String(value));
  const unit = chart.kind === "distance" ? (units === "imperial" ? "mi" : "km") : "kcal";
  return formatWorkoutMetric({ value: String(value), unit }, chart.kind, units) ?? "n/a";
}

function median(values: number[]) {
  const middle = Math.floor(values.length / 2);
  return values.length % 2 ? values[middle] : (values[middle - 1] + values[middle]) / 2;
}

function niceStep(value: number) {
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const upper = (value * 4) / 3;
  for (const factors of [
    [1, 2, 2.5, 5, 10],
    [1.5, 3, 4, 6, 8],
    [1.25, 1.75, 3.5, 4.5, 7, 7.5, 9],
  ]) {
    const step = factors
      .map((factor) => factor * magnitude)
      .find((step) => step >= value && step <= upper);
    if (step !== undefined) return step;
  }
  return value;
}

export function consistencyTickLabels(
  ticks: number[],
  chart: ConsistencyChart,
  units: Preferences["units"],
) {
  const precise = numberFormatter(undefined, { maximumFractionDigits: 12 });
  if (chart.kind === "duration")
    return ticks.map((value) => {
      if (value % 60 === 0) return formatDuration(String(value));
      const hours = Math.floor(value / 3600),
        minutes = Math.floor((value % 3600) / 60),
        seconds = value % 60;
      return [
        hours ? `${hours}h` : "",
        minutes ? `${minutes}m` : "",
        seconds ? `${precise.format(seconds)}s` : "",
      ]
        .filter(Boolean)
        .join(" ");
    });
  if (chart.kind === "days") return ticks.map(String);
  const decimals = chart.kind === "calories" ? 0 : 2;
  if (ticks.some((value) => value !== Number(value.toFixed(decimals)))) {
    const unit = chart.kind === "calories" ? "kcal" : units === "imperial" ? "mi" : "km";
    return ticks.map((value) => `${precise.format(value)} ${unit}`);
  }
  return ticks.map((value) => formatConsistencyValue(value, chart, units));
}

export function consistencyScale(
  values: Array<number | null>,
  chart: ConsistencyChart,
  group: ConsistencyGrouping,
) {
  if (chart.kind === "days")
    return group === "week" ? { max: 7, ticks: [2, 4, 6] } : { max: 31, ticks: [7, 14, 21, 28] };
  const positive = values
    .filter((value): value is number => value !== null && Number.isFinite(value) && value > 0)
    .sort((a, b) => a - b);
  let typicalMax = positive.at(-1) ?? 0;
  if (positive.length >= 3) {
    const center = median(positive);
    const deviation = median(
      positive.map((value) => Math.abs(value - center)).sort((a, b) => a - b),
    );
    const fence = Math.max(center * 3, center + 6 * deviation);
    const typical = positive.filter((value) => value <= fence);
    if (
      typical.length >= Math.max(2, Math.ceil(positive.length * 0.6)) &&
      typical.at(-1)! * 1.5 < typicalMax
    )
      typicalMax = typical.at(-1)!;
  }
  const target =
    typicalMax > 0
      ? typicalMax
      : chart.kind === "duration"
        ? 240
        : chart.kind === "calories"
          ? 4
          : 1;
  let step: number;
  if (chart.kind === "duration") {
    const ordinary = [60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400];
    const finer = [
      1, 2, 3, 5, 10, 15, 20, 30, 45, 75, 90, 150, 180, 240, 450, 1200, 1500, 2700, 5400, 14400,
    ];
    step =
      ordinary.find((value) => value >= target / 4 && value <= target / 3) ??
      finer.find((value) => value >= target / 4 && value <= target / 3) ??
      niceStep(target / 4);
  } else step = niceStep(target / 4);
  return { max: step * 4, ticks: [step, step * 2, step * 3, step * 4] };
}

export function consistencyChartSlots(
  count: number,
  labelWidth: number,
  viewportWidth: number,
  scrollLeft: number,
  leading = 0,
) {
  const slot = labelWidth + CONSISTENCY_BAR_GAP;
  const width = Math.max(viewportWidth, leading + (count ? (count - 1) * slot + labelWidth : 0));
  const first = Math.min(count, Math.max(0, Math.floor((scrollLeft - leading) / slot) - 2));
  const last = Math.max(
    first,
    Math.min(count, Math.ceil((scrollLeft + viewportWidth - leading) / slot) + 3),
  );
  return { width, first, last, slot };
}

export function consistencyCount(value: number) {
  return numberFormatter(undefined, { maximumFractionDigits: 0 }).format(value);
}
