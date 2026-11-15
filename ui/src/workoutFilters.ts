import type {
  Preferences,
  WorkoutColumn,
  WorkoutComparison,
  WorkoutFilters,
  WorkoutList,
  WorkoutNumericColumn,
} from "./api";
import { numberFormatter } from "./formatters";

export type NumericFilter = {
  enabled: boolean;
  operator: WorkoutComparison;
  value: string;
  pace?: { value: string; unit: "s/km" | "s/mi" };
};
export type ColumnFilters = {
  date?: string[];
  type?: string[];
} & Partial<Record<WorkoutNumericColumn, NumericFilter>>;

export const WORKOUT_COLUMN_LABELS: Record<WorkoutColumn, string> = {
  date: "Date",
  type: "Type",
  duration: "Duration",
  distance: "Distance",
  pace: "Pace",
  calories: "Calories",
  heartRate: "Heart rate",
  elevationGain: "Elev gain",
};

export function numericFilterScale(
  column: WorkoutNumericColumn,
  extents: WorkoutList["columnExtents"] | undefined,
  units: Preferences["units"],
) {
  const imperial = units === "imperial";
  const factor =
    column === "distance"
      ? imperial
        ? 1 / 1.609344
        : 1
      : column === "elevationGain"
        ? imperial
          ? 1 / 0.3048
          : 1
        : column === "pace"
          ? 60 * (imperial ? 1.609344 : 1)
          : 1;
  const step =
    column === "duration"
      ? 900
      : column === "distance"
        ? 0.5
        : column === "pace"
          ? 30
          : column === "heartRate"
            ? 10
            : column === "elevationGain" && !imperial
              ? 25
              : 100;
  const min = column === "pace" ? 60 : 0;
  const cap = column === "duration" ? 86400 : column === "pace" ? 3600 : Infinity;
  const extent = column === "duration" ? extents?.duration : extents?.[column]?.value;
  const max = Math.max(
    min + step,
    Math.min(cap, Math.ceil((Number(extent ?? 0) * factor) / step) * step),
  );
  const format = (value: number) => {
    if (column === "duration") {
      const minutes = Math.round(value / 60);
      return minutes >= 60 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m` : `${minutes}m`;
    }
    if (column === "pace") {
      const seconds = Math.round(value);
      return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
    }
    const unit =
      column === "distance"
        ? imperial
          ? "mi"
          : "km"
        : column === "elevationGain"
          ? imperial
            ? "ft"
            : "m"
          : column === "calories"
            ? "kcal"
            : "bpm";
    return `${numberFormatter(undefined, {
      minimumFractionDigits: column === "distance" && value > 0 ? 1 : 0,
      maximumFractionDigits: column === "distance" ? 1 : 0,
    }).format(value)} ${unit}`;
  };
  return {
    min,
    max,
    step,
    factor,
    format,
    toCanonical: (value: number) => String(Number((value / factor).toPrecision(15))),
    toSlider: (value: string) =>
      Math.max(min, Math.min(max, Math.round((Number(value) * factor) / step) * step)),
    defaultValue: column === "pace" ? max : min,
  };
}

export function columnFilterEnabled(filters: ColumnFilters, column: WorkoutColumn) {
  if (column === "date" || column === "type") return filters[column] !== undefined;
  return filters[column]?.enabled === true;
}

export function workoutFilterParams(filters: ColumnFilters): WorkoutFilters {
  const result: WorkoutFilters = {};
  if (filters.date !== undefined) result.weekdays = filters.date.map(Number);
  if (filters.type !== undefined) result.typeIds = filters.type;
  for (const column of Object.keys(WORKOUT_COLUMN_LABELS) as WorkoutColumn[]) {
    if (column === "date" || column === "type") continue;
    const filter = filters[column];
    if (filter?.enabled)
      result[column] =
        column === "pace" && filter.pace
          ? { operator: filter.operator, ...filter.pace }
          : { operator: filter.operator, value: filter.value };
  }
  return result;
}

export function checkboxColumns<T>(items: T[]) {
  const rows = Math.ceil(items.length / Math.max(1, Math.ceil(items.length / 8)));
  if (!rows) return [];
  return Array.from({ length: Math.ceil(items.length / rows) }, (_, index) =>
    items.slice(index * rows, (index + 1) * rows),
  );
}
