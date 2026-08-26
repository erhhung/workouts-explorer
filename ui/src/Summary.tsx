import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type CSSProperties, type PointerEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import {
  api,
  ApiError,
  downloadApi,
  type DateRangeEnum,
  type DateRangePreference,
  type ExactMetric,
  type Preferences,
  type SummaryTotals,
  type Workout,
  type WorkoutColumn,
  DEFAULT_WORKOUT_SORT,
  type WorkoutSort,
  type WorkoutList,
  type WorkoutDeletionAccepted,
  type WorkoutProvenance,
  type WorkoutProvenanceWarning,
  type WorkoutSortDirection,
  type WorkoutSummary,
} from "./api";
import { instantZone, offsetLabel, Tooltip, zoneAbbreviation, zoneOffsetAt, ZoneBadge } from "./Tooltip";
import { CustomDateRangeDialog } from "./CustomDateRangeDialog";
import { formatDateOnly } from "./date";

const DATE_SHORTCUTS: ReadonlyArray<[DateRangeEnum, string]> = [
  ["thisWeek", "This week"],
  ["lastWeek", "Last week"],
  ["last7Days", "Last 7 days"],
  ["last30Days", "Last 30 days"],
  ["thisMonth", "This month"],
  ["lastMonth", "Last month"],
  ["thisYear", "This year"],
  ["lastYear", "Last year"],
];

const COLUMN_LABELS: Record<WorkoutColumn, string> = {
  date: "Date", type: "Type", duration: "Duration", distance: "Distance", pace: "Pace",
  calories: "Calories", heartRate: "Heart rate", elevationGain: "Elev gain",
};

const CANONICAL_COLUMNS = Object.keys(COLUMN_LABELS) as WorkoutColumn[];
const SORTABLE_COLUMNS = new Set<WorkoutColumn>(CANONICAL_COLUMNS);
const EXPLICIT_RANGE = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/;

function isDateRangeEnum(value: string): value is DateRangeEnum {
  return DATE_SHORTCUTS.some(([shortcut]) => shortcut === value);
}

function validDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return false;
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  return date.toISOString().slice(0, 10) === value;
}

export function initialRange(preference?: DateRangePreference | null) {
  if (preference && (isDateRangeEnum(preference) || EXPLICIT_RANGE.test(preference))) return preference;
  return "last30Days" satisfies DateRangeEnum;
}

function rangeParams(range: DateRangePreference, timezone: string) {
  const params = new URLSearchParams();
  const explicit = EXPLICIT_RANGE.exec(range);
  if (explicit) {
    params.set("startDate", explicit[1]);
    params.set("endDate", explicit[2]);
  } else {
    params.set("dateRangeEnum", range);
    params.set("tz", timezone);
  }
  return params;
}

function rangeLabel(range: DateRangePreference) {
  const shortcut = DATE_SHORTCUTS.find(([value]) => value === range);
  if (shortcut) return shortcut[1];
  const explicit = EXPLICIT_RANGE.exec(range);
  return explicit ? `${formatDateOnly(explicit[1])} to ${formatDateOnly(explicit[2])}` : "Last 30 days";
}

function decimal(value: string, maximumFractionDigits = 2) {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits }).format(Number(value));
}

export function formatDuration(value: string) {
  const totalMinutes = Math.round(Number(value) / 60);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return hours ? `${hours}h ${String(minutes).padStart(2, "0")}m` : `${totalMinutes}m`;
}

export function formatAggregateDuration(value: string) {
  const totalMinutes = Math.round(Number(value) / 60);
  const days = Math.floor(totalMinutes / 1440);
  if (!days) return formatDuration(value);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;
  return `${days}d ${hours}h ${String(minutes).padStart(2, "0")}m`;
}

function durationTooltip(value: string, aggregate = false) {
  const totalSeconds = Math.round(Number(value));
  const days = aggregate ? Math.floor(totalSeconds / 86400) : 0;
  const hours = Math.floor((totalSeconds % (days ? 86400 : Number.POSITIVE_INFINITY)) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (days) return `${days}d ${hours}h ${String(minutes).padStart(2, "0")}m ${String(seconds).padStart(2, "0")}s`;
  if (hours) return `${hours}h ${String(minutes).padStart(2, "0")}m ${String(seconds).padStart(2, "0")}s`;
  if (minutes) return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
  return `${seconds}s`;
}

function DurationValue({ value, focusable = true, aggregate = false, detail }: { value: string; focusable?: boolean; aggregate?: boolean; detail?: string }) {
  const display = aggregate ? formatAggregateDuration(value) : formatDuration(value);
  const exact = detail ?? durationTooltip(value, aggregate);
  if (!focusable) return <span className="duration-value" title={exact} aria-label={`Duration ${display}; rounded to nearest second ${exact}`}>{display}</span>;
  return <Tooltip content={exact} className="duration-value" focusable={focusable} label={`Duration ${display}; rounded to nearest second ${exact}`}>{display}</Tooltip>;
}

function clockDuration(value: number) {
  const totalSeconds = Math.max(0, Math.round(value));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

function Unavailable({ children = "Unavailable" }: { children?: ReactNode }) {
  return <span className="unavailable">{children}</span>;
}

function metric(metricValue: ExactMetric | null, kind: "distance" | "pace" | "calories" | "heartRate" | "elevationGain", units: Preferences["units"], includeUnit = true, aggregate = false) {
  if (!metricValue) return null;
  let value = Number(metricValue.value);
  let unit = metricValue.unit;
  if (units === "imperial" && kind === "distance" && unit === "km") { value *= 0.621371192; unit = "mi"; }
  if (units === "imperial" && kind === "elevationGain" && unit === "m") { value *= 3.280839895; unit = "ft"; }
  if (units === "imperial" && kind === "pace" && unit === "min/km") { value *= 1.609344; unit = "min/mi"; }
  if (kind === "pace") {
    const roundedSeconds = Math.round(value * 60);
    return `${Math.floor(roundedSeconds / 60)}:${String(roundedSeconds % 60).padStart(2, "0")} ${unit}`;
  }
  const label = kind === "heartRate" && unit === "count/min" ? "bpm" : unit;
  const maximumFractionDigits = kind === "calories" || (aggregate && kind === "distance" && value >= 100) ? 0 : 2;
  const display = new Intl.NumberFormat(undefined, { maximumFractionDigits }).format(value);
  return includeUnit ? `${display} ${label}` : display;
}

function convertedElevation(metricValue: ExactMetric, units: Preferences["units"]) {
  let value = Number(metricValue.value);
  let unit = metricValue.unit;
  if (units === "imperial" && unit === "m") { value *= 3.280839895; unit = "ft"; }
  return { value, unit };
}

function resolveZone(workoutZone: string | null, offset: number | null, instant: Date) {
  if (workoutZone) {
    const zoneOffset = zoneOffsetAt(workoutZone, instant);
    if (zoneOffset != null && (offset == null || zoneOffset === offset)) return workoutZone;
  }
  return null;
}

function rangeDate(value: string) {
  return formatDateOnly(value);
}

function currentZone(timeZone: string) {
  const zone = instantZone(timeZone, new Date());
  return zone.label === "TZ unavailable" ? { ...zone, title: `${timeZone} (current offset unavailable)` } : zone;
}

interface LocalInstant {
  dateTime: string;
  weekday: string;
  zoneLabel: string;
  zoneTitle: string;
  unavailable: boolean;
}

function localInstant(value: string, workoutZone: string | null, offset: number | null, preferences: Preferences, knownDate?: string | null): LocalInstant {
  const instant = new Date(value);
  const options: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short", hour12: preferences.clockFormat === "12h" };
  if (!Number.isNaN(instant.getTime())) {
    const timeZone = resolveZone(workoutZone, offset, instant);
    if (timeZone) {
      const resolvedOffset = zoneOffsetAt(timeZone, instant)!;
      return {
        dateTime: new Intl.DateTimeFormat("en-US", { ...options, timeZone }).format(instant),
        weekday: new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone }).format(instant),
        zoneLabel: zoneAbbreviation(timeZone, instant),
        zoneTitle: `${offsetLabel(resolvedOffset)} recorded`,
        unavailable: false,
      };
    }
    if (offset != null) {
      const shifted = new Date(instant.getTime() + offset * 60_000);
      return {
        dateTime: new Intl.DateTimeFormat("en-US", { ...options, timeZone: "UTC" }).format(shifted),
        weekday: new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone: "UTC" }).format(shifted),
        zoneLabel: offsetLabel(offset, true),
        zoneTitle: `${offsetLabel(offset)} recorded | timezone unavailable`,
        unavailable: false,
      };
    }
  }
  if (knownDate && validDate(knownDate)) {
    const date = new Date(`${knownDate}T00:00:00Z`);
    return {
      dateTime: `${new Intl.DateTimeFormat("en-US", { dateStyle: "medium", timeZone: "UTC" }).format(date)} at time unavailable`,
      weekday: new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone: "UTC" }).format(date),
      zoneLabel: "TZ unavailable",
      zoneTitle: "Recorded offset/timezone unavailable",
      unavailable: true,
    };
  }
  return { dateTime: "Unavailable", weekday: "---", zoneLabel: "TZ unavailable", zoneTitle: "Recorded offset/timezone unavailable", unavailable: true };
}

function workoutTimes(workout: Workout, preferences: Preferences) {
  return {
    start: localInstant(workout.startedAt, workout.timezone, workout.originalStartOffsetMinutes, preferences, workout.localStartDate),
    end: localInstant(workout.endedAt, workout.timezone, workout.originalEndOffsetMinutes, preferences),
  };
}

function compactWorkoutTime(value: string, workoutZone: string | null, offset: number | null, preferences: Preferences) {
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return "time unavailable";
  const resolvedZone = resolveZone(workoutZone, offset, instant);
  const display = resolvedZone || offset == null ? instant : new Date(instant.getTime() + offset * 60_000);
  const parts = new Intl.DateTimeFormat("en-US", {
    hour: "numeric", minute: "2-digit", hour12: preferences.clockFormat === "12h",
    timeZone: resolvedZone ?? (offset == null ? preferences.timezone : "UTC"),
  }).formatToParts(display);
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
  return `${part("hour").replace(/^0/, "")}:${part("minute")}${preferences.clockFormat === "12h" ? part("dayPeriod").slice(0, 1).toLowerCase() : ""}`;
}

function workoutDurationDetail(workout: Workout, preferences: Preferences) {
  const start = compactWorkoutTime(workout.startedAt, workout.timezone, workout.originalStartOffsetMinutes, preferences);
  const end = compactWorkoutTime(workout.endedAt, workout.timezone, workout.originalEndOffsetMinutes, preferences);
  const elapsed = (new Date(workout.endedAt).getTime() - new Date(workout.startedAt).getTime()) / 1000;
  return `${clockDuration(Number(workout.duration))} | ${start} - ${end} (${clockDuration(elapsed)})`;
}

function metricTooltip(display: string, detail: string, label: string) {
  return <Tooltip content={detail} className="workout-metric-value" label={`${label} ${display}; ${detail}`}>{display}</Tooltip>;
}

function alignedMetric(display: string) {
  return <span className="workout-metric-display">{display}</span>;
}

function paceDisplay(pace: ExactMetric, preferences: Preferences) {
  let seconds = Number(pace.value) * 60;
  if (preferences.units === "imperial" && pace.unit === "min/km") seconds *= 1.609344;
  const rounded = Math.round(seconds);
  return `${Math.floor(rounded / 60)}m ${String(rounded % 60).padStart(2, "0")}s`;
}

function workoutMetricValue(column: Exclude<WorkoutColumn, "date" | "type" | "duration" | "distance">, workout: Workout, preferences: Preferences) {
  if (column === "calories") {
    if (!workout.calories) return <Unavailable />;
    const display = decimal(workout.calories.value, 0);
    const active = workout.activeCalories ? `${decimal(workout.activeCalories.value, 0)} kcal active` : "active unavailable";
    return metricTooltip(display, `${decimal(workout.calories.value, 0)} kcal total | ${active}`, "Calories");
  }
  if (column === "heartRate") {
    if (!workout.heartRate) return <Unavailable />;
    const display = `${decimal(workout.heartRate.value, 0)} bpm`;
    const maximum = workout.maximumHeartRate ? `${decimal(workout.maximumHeartRate.value, 0)} bpm maximum` : "maximum unavailable";
    return metricTooltip(display, `${decimal(workout.heartRate.value, 0)} bpm average | ${maximum}`, "Heart rate");
  }
  if (column === "pace") {
    if (!workout.pace) return <Unavailable />;
    const display = paceDisplay(workout.pace, preferences);
    const splits = preferences.units === "imperial" ? workout.splitPaces?.mile : workout.splitPaces?.kilometer;
    if (!splits) return alignedMetric(display);
    const splitUnit = preferences.units === "imperial" ? "mi" : "km";
    const splitPace = (value: string) => {
      const total = Math.round(Number(value));
      return `${Math.floor(total / 60)}'${String(total % 60).padStart(2, "0")}\"/${splitUnit}`;
    };
    return metricTooltip(display, `${splitPace(splits.fastestSeconds)} fastest | ${splitPace(splits.slowestSeconds)} slowest`, "Pace");
  }
  if (!workout.elevationGain) return <Unavailable />;
  const gain = convertedElevation(workout.elevationGain, preferences.units);
  const display = `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(gain.value)} ${gain.unit}`;
  if (!workout.minimumElevation || !workout.maximumElevation) return alignedMetric(display);
  const minimum = convertedElevation(workout.minimumElevation, preferences.units);
  const maximum = convertedElevation(workout.maximumElevation, preferences.units);
  const detail = `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(minimum.value)} ${minimum.unit} lowest | ${new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(maximum.value)} ${maximum.unit} highest`;
  return metricTooltip(display, detail, "Elevation gain");
}

function DateTimeValue({ value, focusableHelp = true }: { value: LocalInstant; focusableHelp?: boolean }) {
  return <span className={`workout-date${value.unavailable ? " unavailable" : ""}`}>
    <span className="weekday-badge" aria-hidden="true">{value.weekday}</span>
    <span className="local-date-time">{value.dateTime}</span>
    <ZoneBadge label={value.zoneLabel} title={value.zoneTitle} focusable={focusableHelp} />
  </span>;
}

function columnValue(column: WorkoutColumn, workout: Workout, preferences: Preferences) {
  if (column === "date") {
    const times = workoutTimes(workout, preferences);
    return <DateTimeValue value={times.start} />;
  }
  if (column === "type") return workout.type.displayName;
  if (column === "duration") return <DurationValue value={workout.duration} detail={workoutDurationDetail(workout, preferences)} />;
  if (column === "distance") {
    if (!workout.distance) return <Unavailable />;
    const display = metric(workout.distance, "distance", preferences.units)!;
    if (workout.distance.unit !== "km") return alignedMetric(display);
    const other = preferences.units === "imperial"
      ? `${decimal(workout.distance.value)} km`
      : `${decimal(String(Number(workout.distance.value) * 0.621371192))} mi`;
    return metricTooltip(display, other, "Distance");
  }
  return workoutMetricValue(column, workout, preferences);
}

function workoutColumnExtent(column: WorkoutColumn, data: WorkoutList, preferences: Preferences) {
  const extent = data.columnExtents;
  if (!extent) return undefined;
  if (column === "duration") return extent.duration ? formatDuration(extent.duration) : undefined;
  if (column === "distance") return extent.distance ? metric(extent.distance, "distance", preferences.units) ?? undefined : undefined;
  if (column === "pace") return extent.pace ? paceDisplay(extent.pace, preferences) : undefined;
  if (column === "calories") return extent.calories ? decimal(extent.calories.value, 0) : undefined;
  if (column === "heartRate") return extent.heartRate ? `${decimal(extent.heartRate.value, 0)} bpm` : undefined;
  if (column === "elevationGain" && extent.elevationGain) {
    const converted = convertedElevation(extent.elevationGain, preferences.units);
    return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(converted.value)} ${converted.unit}`;
  }
  return undefined;
}

function workoutHasColumnValue(column: WorkoutColumn, workout: Workout) {
  if (column === "duration") return true;
  if (column === "distance") return workout.distance != null;
  if (column === "pace") return workout.pace != null;
  if (column === "calories") return workout.calories != null;
  if (column === "heartRate") return workout.heartRate != null;
  if (column === "elevationGain") return workout.elevationGain != null;
  return false;
}

function isWorkoutMetricColumn(column: WorkoutColumn) {
  return column !== "date" && column !== "type";
}

function QueryError({ message, retry }: { message: string; retry: () => void }) {
  return <div className="summary-query-error" role="alert"><span>{message}</span><button className="secondary" onClick={retry}>Retry</button></div>;
}

function WorkoutActions({ workout, onShowOnMap, onViewProvenance, onExportGeoJSON, onExportPoints, onDelete }: {
  workout: Workout;
  onShowOnMap?: (workoutId: string) => void;
  onViewProvenance: (workout: Workout, returnFocus: HTMLButtonElement | null) => void;
  onExportGeoJSON: (workout: Workout) => void;
  onExportPoints: (workout: Workout) => void;
  onDelete: (workout: Workout, returnFocus: HTMLButtonElement | null) => void;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger ref={triggerRef} className="workout-action-trigger" aria-label={`Actions for ${workout.type.displayName} on ${workout.localStartDate ?? "unknown date"}`}>
      <span aria-hidden="true">...</span>
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal><DropdownMenu.Content className="menu-content workout-action-menu" align="end" sideOffset={6}>
      {workout.routePointCount >= 2 && onShowOnMap && <DropdownMenu.Item onSelect={() => onShowOnMap(workout.id)}>Show on map</DropdownMenu.Item>}
      <DropdownMenu.Item onSelect={() => onViewProvenance(workout, triggerRef.current)}>View provenance</DropdownMenu.Item>
      {workout.routePointCount >= 2 && <DropdownMenu.Item onSelect={() => onExportGeoJSON(workout)}>Export GeoJSON</DropdownMenu.Item>}
      {workout.routeAvailable && <DropdownMenu.Item onSelect={() => onExportPoints(workout)}>Export points</DropdownMenu.Item>}
      <DropdownMenu.Separator />
      <DropdownMenu.Item className="danger-item" onSelect={() => onDelete(workout, triggerRef.current)}>Delete workout</DropdownMenu.Item>
    </DropdownMenu.Content></DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

function provenanceKindLabel(kind: WorkoutProvenance["items"][number]["kind"]) {
  if (kind === "created") return "Imported";
  if (kind === "updated") return "Updated";
  return "Matched unchanged";
}

function provenanceTime(value: string, preferences: Preferences) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Time unavailable";
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium", timeStyle: "medium", timeZone: preferences.timezone, hour12: preferences.clockFormat === "12h",
  }).format(date);
}

function warningLabel(warning: WorkoutProvenanceWarning) {
  const code = warning.code.replaceAll("_", " ");
  const field = warning.field.replaceAll("_", " ");
  return warning.routePoint == null ? `${code}: ${field}` : `${code}: ${field}, point ${warning.routePoint + 1}`;
}

function subtractExact(left: string, right: string) {
  const [leftWhole, leftFraction = ""] = left.split(".");
  const [rightWhole, rightFraction = ""] = right.split(".");
  const scale = Math.max(leftFraction.length, rightFraction.length);
  const scaled = (whole: string, fraction: string) => BigInt(whole + fraction.padEnd(scale, "0"));
  const value = scaled(leftWhole, leftFraction) - scaled(rightWhole, rightFraction);
  if (value <= 0n) return "0";
  const digits = value.toString().padStart(scale + 1, "0");
  if (scale === 0) return digits;
  const fraction = digits.slice(-scale).replace(/0+$/, "");
  return fraction ? `${digits.slice(0, -scale)}.${fraction}` : digits.slice(0, -scale);
}

function subtractWorkoutTotals(totals: SummaryTotals, workout: Workout): SummaryTotals {
  const subtractMetric = (total: ExactMetric | null, item: ExactMetric | null) => total && item && total.unit === item.unit
    ? { ...total, value: subtractExact(total.value, item.value) }
    : total;
  return {
    count: Math.max(0, totals.count - 1),
    duration: subtractExact(totals.duration, workout.duration),
    distance: subtractMetric(totals.distance, workout.distance),
    energy: subtractMetric(totals.energy, workout.calories),
    routeCount: Math.max(0, totals.routeCount - (workout.routeAvailable ? 1 : 0)),
    routedDistance: workout.routeAvailable ? subtractMetric(totals.routedDistance, workout.distance) : totals.routedDistance,
  };
}

function hideWorkoutFromSummary(summary: WorkoutSummary, workout: Workout): WorkoutSummary {
  return {
    ...summary,
    totals: subtractWorkoutTotals(summary.totals, workout),
    byType: summary.byType.flatMap((entry) => {
      if (entry.type.id !== workout.type.id) return [entry];
      const totals = subtractWorkoutTotals(entry.totals, workout);
      return totals.count === 0 ? [] : [{ ...entry, totals }];
    }),
  };
}

function emptyWorkoutSummary(summary: WorkoutSummary): WorkoutSummary {
  const zeroMetric = (metric: ExactMetric | null) => metric ? { ...metric, value: "0" } : null;
  return { ...summary, totals: { count: 0, duration: "0", distance: zeroMetric(summary.totals.distance), energy: zeroMetric(summary.totals.energy), routeCount: 0, routedDistance: zeroMetric(summary.totals.routedDistance) }, byType: [] };
}

interface AggregateBreakdown {
  id: string;
  label: string;
  value: ReactNode;
}

function average(value: string, count: number) {
  return count > 0 ? String(Number(value) / count) : "0";
}

function averageMetric(value: ExactMetric | null, count: number) {
  return value && count > 0 ? { ...value, value: average(value.value, count) } : null;
}

function descending<T>(items: T[], value: (item: T) => number, label: (item: T) => string) {
  return [...items].sort((left, right) => value(right) - value(left) || label(left).localeCompare(label(right)));
}

function workoutBreakdown(summary: WorkoutSummary): AggregateBreakdown[] {
  const entries = descending(summary.byType, (entry) => entry.totals.count, (entry) => entry.type.displayName);
  if (summary.totals.count === 0) return [];
  const shares = entries.map((entry, index) => {
    const exact = entry.totals.count * 100 / summary.totals.count;
    return { index, percentage: Math.floor(exact), remainder: exact - Math.floor(exact) };
  });
  let remaining = 100 - shares.reduce((sum, share) => sum + share.percentage, 0);
  for (const share of [...shares].sort((left, right) => right.remainder - left.remainder || left.index - right.index)) {
    if (remaining <= 0) break;
    share.percentage += 1;
    remaining -= 1;
  }
  return entries.map((entry, index) => ({
    id: entry.type.id,
    label: entry.type.displayName,
    value: `${decimal(String(entry.totals.count), 0)} (${shares[index].percentage}%)`,
  }));
}

function AggregateCard({ label, value, breakdown, averages, onToggleMode }: {
  label: string; value: ReactNode; breakdown: AggregateBreakdown[]; averages: boolean; onToggleMode: () => void;
}) {
  const [preview, setPreview] = useState(false);
  const [hint, setHint] = useState<{ x: number; y: number }>();
  const panelId = useId();
  const hintPosition = useRef({ x: 0, y: 0 });
  const showHintTimer = useRef<number | undefined>(undefined);
  const hideHintTimer = useRef<number | undefined>(undefined);
  const visible = preview;
  function clearHint() {
    window.clearTimeout(showHintTimer.current);
    window.clearTimeout(hideHintTimer.current);
    setHint(undefined);
  }
  function startPreview(event: PointerEvent<HTMLButtonElement>) {
    if (event.pointerType !== "mouse") return;
    setPreview(true);
    clearHint();
    hintPosition.current = { x: event.clientX ?? 0, y: event.clientY ?? 0 };
    showHintTimer.current = window.setTimeout(() => {
      setHint(hintPosition.current);
      hideHintTimer.current = window.setTimeout(() => setHint(undefined), 2000);
    }, 750);
  }
  useEffect(() => () => {
    window.clearTimeout(showHintTimer.current);
    window.clearTimeout(hideHintTimer.current);
  }, []);
  return (
    <article className={`aggregate-card${visible ? " is-visible" : ""}`}>
      <button type="button" aria-expanded={visible} aria-controls={panelId}
        onPointerEnter={startPreview} onPointerMove={(event) => { hintPosition.current = { x: event.clientX ?? 0, y: event.clientY ?? 0 }; if (hint) setHint(hintPosition.current); }}
        onPointerLeave={(event) => { if (event.pointerType === "mouse") { setPreview(false); clearHint(); } }}
        onFocus={(event) => { if (event.currentTarget.matches(":focus-visible")) setPreview(true); }} onBlur={() => { setPreview(false); clearHint(); }}
        aria-pressed={averages}
        onKeyDown={(event) => { if (event.key === "Escape") setPreview(false); }}
        onClick={() => { clearHint(); onToggleMode(); }}>
        <span>{label}</span><strong>{value}</strong><small>By workout type</small>
      </button>
      {hint && <span className="aggregate-mode-hint" role="tooltip" style={{ left: hint.x, top: hint.y }}>{averages ? "Click to see totals" : "Click to see averages"}</span>}
      <div id={panelId} className="aggregate-breakdown" aria-hidden={!visible}>
        {breakdown.length ? breakdown.map((entry) => <span key={entry.id}><b>{entry.label}</b><em>{entry.value}</em></span>) : <span>No workout types</span>}
      </div>
    </article>
  );
}

function WorkoutTable({ data, preferences, sort, onSort, onShowOnMap, onViewProvenance, onExportGeoJSON, onExportPoints, onDelete }: {
  data: WorkoutList; preferences: Preferences; sort: { field: WorkoutColumn; direction: WorkoutSortDirection };
  onSort: (field: WorkoutColumn) => void;
  onShowOnMap?: (workoutId: string) => void;
  onViewProvenance: (workout: Workout, returnFocus: HTMLButtonElement | null) => void;
  onExportGeoJSON: (workout: Workout) => void;
  onExportPoints: (workout: Workout) => void;
  onDelete: (workout: Workout, returnFocus: HTMLButtonElement | null) => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const selectedColumns = new Set<unknown>(Array.isArray(preferences.workoutColumns) ? preferences.workoutColumns : []);
  const filteredColumns = CANONICAL_COLUMNS.filter((column) => selectedColumns.has(column));
  const columns = filteredColumns.length ? filteredColumns : (["date", "type", "duration"] satisfies WorkoutColumn[]);
  const columnWeights = columns.map((column) => column === "date" ? 2 : column === "type" ? 1.4 : 1);
  const totalWeight = columnWeights.reduce((total, weight) => total + weight, 0);
  let assignedWidth = 0;
  const columnWidths = columnWeights.map((weight, index) => {
    const width = index === columnWeights.length - 1 ? 100 - assignedWidth : Number((weight / totalWeight * 100).toFixed(4));
    assignedWidth += width;
    return width.toFixed(4);
  });
  return (
    <>
      <div className="workout-table-wrap">
        <table className="workout-table">
          <colgroup>{columns.map((column, index) => <col key={column} style={{ width: `${columnWidths[index]}%` }} />)}<col className="workout-actions-col" /></colgroup>
          <thead><tr>{columns.map((column) => {
            const selected = sort.field === column;
            return <th key={column} aria-sort={selected ? (sort.direction === "asc" ? "ascending" : "descending") : "none"} scope="col"><button onClick={() => onSort(column)} disabled={!SORTABLE_COLUMNS.has(column)}><span className="column-label">{COLUMN_LABELS[column]}</span><span className="sort-indicator" aria-hidden="true">{selected ? (sort.direction === "asc" ? <>&#9650;</> : <>&#9660;</>) : <>&#9650; &#9660;</>}</span></button></th>;
          })}<th className="workout-actions-heading" scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>{data.items.map((workout) => <tr key={workout.id}>{columns.map((column) => {
            const hasValue = workoutHasColumnValue(column, workout);
            const value = isWorkoutMetricColumn(column) && !hasValue ? <span className="workout-table-na">n/a</span> : columnValue(column, workout, preferences);
            const extent = workoutColumnExtent(column, data, preferences);
            return <td key={column}><div className={`workout-cell workout-cell--${column}`}>{isWorkoutMetricColumn(column)
              ? <span className="workout-value-lane" data-width-sample={extent ?? "n/a"}><span className="workout-value-content">{value}</span></span>
              : value}</div></td>;
          })}<td className="workout-actions-cell"><WorkoutActions workout={workout} onShowOnMap={onShowOnMap} onViewProvenance={onViewProvenance} onExportGeoJSON={onExportGeoJSON} onExportPoints={onExportPoints} onDelete={onDelete} /></td></tr>)}</tbody>
        </table>
      </div>
      <div className="mobile-workouts">
        {data.items.map((workout) => {
          const isExpanded = expanded.has(workout.id);
          const detailsId = `workout-details-${workout.id}`;
          const times = workoutTimes(workout, preferences);
          return <article className={`mobile-workout${isExpanded ? " is-expanded" : ""}`} key={workout.id}>
            <div className="mobile-workout-summary">
              <button aria-expanded={isExpanded} aria-controls={detailsId} title={workoutDurationDetail(workout, preferences)} onClick={() => setExpanded((current) => { const next = new Set(current); if (next.has(workout.id)) next.delete(workout.id); else next.add(workout.id); return next; })}>
                <DateTimeValue value={times.start} focusableHelp={false} />
                <strong>{workout.type.displayName}</strong><DurationValue value={workout.duration} focusable={false} detail={workoutDurationDetail(workout, preferences)} />
              </button>
              <WorkoutActions workout={workout} onShowOnMap={onShowOnMap} onViewProvenance={onViewProvenance} onExportGeoJSON={onExportGeoJSON} onExportPoints={onExportPoints} onDelete={onDelete} />
            </div>
            <div id={detailsId} hidden={!isExpanded} className="mobile-workout-details">
              <dl>
                 <div><dt>Local start</dt><dd><DateTimeValue value={times.start} /></dd></div>
                 <div><dt>Local end</dt><dd><DateTimeValue value={times.end} /></dd></div>
                 <div><dt>Duration</dt><dd><DurationValue value={workout.duration} detail={workoutDurationDetail(workout, preferences)} /></dd></div>
                {(["distance", "pace", "calories", "heartRate", "elevationGain"] as const).map((column) => <div key={column}><dt>{COLUMN_LABELS[column]}</dt><dd>{columnValue(column, workout, preferences)}</dd></div>)}
              </dl>
              <p>{workout.location ?? "Location unavailable"} / {workout.routeAvailable ? "Route available" : "Route unavailable"}</p>
            </div>
          </article>;
        })}
      </div>
    </>
  );
}

export function Summary({ preferences, csrfToken, selectedDateRange, onDateRangeSelected, selectedSort, onSortChange, onShowOnMap, onDateRangeSaved }: {
  preferences: Preferences; csrfToken: string; selectedDateRange?: DateRangePreference; onDateRangeSelected?: (dateRange: DateRangePreference) => void;
  selectedSort?: WorkoutSort; onSortChange?: (sort: WorkoutSort) => void;
  onShowOnMap?: (workoutId: string) => void; onDateRangeSaved: (dateRange: DateRangePreference) => void;
}) {
  const queryClient = useQueryClient();
  const [range, setRange] = useState<DateRangePreference>(() => selectedDateRange ?? initialRange(preferences.dateRange));
  const [averages, setAverages] = useState(false);
  const rangeRef = useRef(range);
  const latestSelection = useRef(0);
  const [pageState, setPageState] = useState({ page: 1, pageSize: preferences.pageSize });
  const page = pageState.pageSize === preferences.pageSize ? pageState.page : 1;
  const [localSort, setLocalSort] = useState<WorkoutSort>(DEFAULT_WORKOUT_SORT);
  const sort = selectedSort ?? localSort;
  const [sortActivity, setSortActivity] = useState<{ field: WorkoutColumn; direction: WorkoutSortDirection; state: "sorting" | "complete" }>();
  const [customOpen, setCustomOpen] = useState(false);
  const [saveNotice, setSaveNotice] = useState("");
  const [exportError, setExportError] = useState("");
  const provenanceReturnFocus = useRef<HTMLButtonElement | null>(null);
  const [provenanceWorkout, setProvenanceWorkout] = useState<Workout>();
  const [deletionTarget, setDeletionTarget] = useState<Workout>();
  const [deletionError, setDeletionError] = useState("");
  const [deletionNotice, setDeletionNotice] = useState<string>();
  const deletionReturnFocus = useRef<HTMLButtonElement | null>(null);
  const deletionCancelRef = useRef<HTMLButtonElement>(null);
  const deletionErrorRef = useRef<HTMLParagraphElement>(null);
  const [rangeDeletionOpen, setRangeDeletionOpen] = useState(false);
  const [rangeConfirmation, setRangeConfirmation] = useState("");
  const [rangeDeletionError, setRangeDeletionError] = useState("");
  const rangeDeletionCancelRef = useRef<HTMLButtonElement>(null);
  const rangeDeletionErrorRef = useRef<HTMLParagraphElement>(null);
  const explicit = EXPLICIT_RANGE.exec(range);
  useEffect(() => {
    const next = selectedDateRange ?? initialRange(preferences.dateRange);
    if (next !== rangeRef.current) {
      latestSelection.current += 1;
      rangeRef.current = next;
      setRange(next);
      setPageState((current) => ({ ...current, page: 1 }));
      setSortActivity(undefined);
      if (!selectedSort) setLocalSort(DEFAULT_WORKOUT_SORT);
      setSaveNotice("");
    }
  }, [preferences.dateRange, selectedDateRange]);
  useEffect(() => {
    if (pageState.pageSize !== preferences.pageSize) { setPageState({ page: 1, pageSize: preferences.pageSize }); setSortActivity(undefined); }
  }, [pageState.pageSize, preferences.pageSize]);
  const persistence = useMutation({
    scope: { id: "summary-date-range" },
    mutationFn: ({ dateRange }: { dateRange: DateRangePreference; sequence: number }) => api<Preferences>("/api/me/preferences", { method: "PATCH", body: JSON.stringify({ dateRange }) }, csrfToken),
    onSuccess: (saved, selection) => {
      if (selection.sequence === latestSelection.current) {
        if (saved.dateRange === selection.dateRange) {
          setSaveNotice("");
          onDateRangeSaved(selection.dateRange);
        } else {
          setSaveNotice("Your default date range was not saved. This view will continue using your selection.");
        }
      }
    },
    onError: (_error, selection) => {
      if (selection.sequence === latestSelection.current) setSaveNotice("Your default date range was not saved. This view will continue using your selection.");
    },
  });
  function selectRange(next: DateRangePreference) {
    const sequence = latestSelection.current + 1;
    latestSelection.current = sequence;
    rangeRef.current = next;
    setRange(next); setPageState({ page: 1, pageSize: preferences.pageSize }); setSortActivity(undefined); setSaveNotice("");
    if (!selectedSort) setLocalSort(DEFAULT_WORKOUT_SORT);
    onDateRangeSelected?.(next);
    persistence.mutate({ dateRange: next, sequence });
  }
  const summaryQueryKey = ["summary", range, preferences.timezone, preferences.firstWeekday] as const;
  const summaryQuery = useQuery({
    queryKey: summaryQueryKey,
    queryFn: ({ signal }) => api<WorkoutSummary>(`/api/summary?${rangeParams(range, preferences.timezone)}`, { signal }),
  });
  const workoutBaseKey = ["workouts", range, preferences.timezone, preferences.firstWeekday, page, preferences.pageSize] as const;
  const workoutQueryKey = [...workoutBaseKey, sort.field, sort.direction] as const;
  const workoutsQuery = useQuery({
    queryKey: workoutQueryKey,
    queryFn: ({ signal }) => {
      const params = rangeParams(range, preferences.timezone);
      params.set("page", String(page)); params.set("pageSize", String(preferences.pageSize)); params.set("sort", `${sort.field}:${sort.direction}`);
      return api<WorkoutList>(`/api/workouts?${params}`, { signal });
    },
    placeholderData: (previousData, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      const sameFilter = previousKey?.length === workoutQueryKey.length &&
        previousKey[0] === workoutQueryKey[0] && previousKey[1] === range && previousKey[2] === preferences.timezone &&
        previousKey[3] === preferences.firstWeekday && previousKey[5] === preferences.pageSize;
      const sortChanged = previousKey?.[workoutBaseKey.length] !== sort.field || previousKey?.[workoutBaseKey.length + 1] !== sort.direction;
      return sameFilter && page === 1 && sortChanged ? previousData : undefined;
    },
  });
  const provenanceQuery = useQuery({
    queryKey: ["workout-provenance", provenanceWorkout?.id],
    queryFn: ({ signal }) => api<WorkoutProvenance>(`/api/workouts/${encodeURIComponent(provenanceWorkout!.id)}/provenance`, { signal }),
    enabled: Boolean(provenanceWorkout),
  });
  const deletion = useMutation({
    scope: { id: "workout-deletion" },
    mutationFn: (workout: Workout) => api<WorkoutDeletionAccepted>(`/api/workouts/${encodeURIComponent(workout.id)}`, { method: "DELETE" }, csrfToken),
    onMutate: async (workout) => {
      setDeletionError("");
      await Promise.all([
        queryClient.cancelQueries({ queryKey: workoutQueryKey, exact: true }),
        queryClient.cancelQueries({ queryKey: summaryQueryKey, exact: true }),
      ]);
      const previousWorkouts = queryClient.getQueryData<WorkoutList>(workoutQueryKey);
      const previousSummary = queryClient.getQueryData<WorkoutSummary>(summaryQueryKey);
      if (previousWorkouts) queryClient.setQueryData<WorkoutList>(workoutQueryKey, {
        ...previousWorkouts,
        items: previousWorkouts.items.filter((item) => item.id !== workout.id),
        pagination: {
          ...previousWorkouts.pagination,
          totalItems: Math.max(0, previousWorkouts.pagination.totalItems - 1),
          totalPages: Math.ceil(Math.max(0, previousWorkouts.pagination.totalItems - 1) / previousWorkouts.pagination.pageSize),
        },
      });
      if (previousSummary) queryClient.setQueryData(summaryQueryKey, hideWorkoutFromSummary(previousSummary, workout));
      return { previousWorkouts, previousSummary };
    },
    onError: (error, _workout, context) => {
      if (error instanceof ApiError && error.status === 404) {
        setDeletionTarget(undefined);
        setDeletionNotice("This workout is no longer available. Summary was refreshed.");
        void queryClient.invalidateQueries({ queryKey: ["workouts"] });
        void queryClient.invalidateQueries({ queryKey: ["summary"] });
        setTimeout(() => document.getElementById("workouts-heading")?.focus());
        return;
      }
      if (context?.previousWorkouts) queryClient.setQueryData(workoutQueryKey, context.previousWorkouts);
      if (context?.previousSummary) queryClient.setQueryData(summaryQueryKey, context.previousSummary);
      setDeletionError("The workout could not be queued for deletion. Nothing was changed. Try again.");
      setTimeout(() => deletionErrorRef.current?.focus());
    },
    onSuccess: (_accepted, workout) => {
      setDeletionTarget(undefined);
      setDeletionNotice(undefined);
      if (provenanceWorkout?.id === workout.id) setProvenanceWorkout(undefined);
      for (const key of [["workouts"], ["summary"], ["workout-provenance"], ["data-sync"], ["jobs"]]) void queryClient.invalidateQueries({ queryKey: key });
      setTimeout(() => document.getElementById("workouts-heading")?.focus());
    },
  });
  const rangeDeletion = useMutation({
    scope: { id: "workout-deletion" },
    mutationFn: ({ startDate, endDate }: { startDate: string; endDate: string }) => {
      const params = new URLSearchParams({ startDate, endDate });
      return api<WorkoutDeletionAccepted>(`/api/workouts?${params}`, { method: "DELETE" }, csrfToken);
    },
    onMutate: async () => {
      setRangeDeletionError("");
      await Promise.all([
        queryClient.cancelQueries({ queryKey: workoutQueryKey, exact: true }),
        queryClient.cancelQueries({ queryKey: summaryQueryKey, exact: true }),
      ]);
      const previousWorkouts = queryClient.getQueryData<WorkoutList>(workoutQueryKey);
      const previousSummary = queryClient.getQueryData<WorkoutSummary>(summaryQueryKey);
      if (previousWorkouts) queryClient.setQueryData<WorkoutList>(workoutQueryKey, {
        ...previousWorkouts, items: [], pagination: { ...previousWorkouts.pagination, totalItems: 0, totalPages: 0 },
      });
      if (previousSummary) queryClient.setQueryData(summaryQueryKey, emptyWorkoutSummary(previousSummary));
      return { previousWorkouts, previousSummary };
    },
    onError: (_error, _dates, context) => {
      if (context?.previousWorkouts) queryClient.setQueryData(workoutQueryKey, context.previousWorkouts);
      if (context?.previousSummary) queryClient.setQueryData(summaryQueryKey, context.previousSummary);
      setRangeDeletionError("The range could not be queued for deletion. Nothing was changed. Try again.");
      setTimeout(() => rangeDeletionErrorRef.current?.focus());
    },
    onSuccess: () => {
      setRangeDeletionOpen(false);
      setRangeConfirmation("");
      for (const key of [["workouts"], ["summary"], ["workout-provenance"], ["data-sync"], ["jobs"]]) void queryClient.invalidateQueries({ queryKey: key });
      setTimeout(() => document.getElementById("workouts-heading")?.focus());
    },
  });
  function openProvenance(workout: Workout, returnFocus: HTMLButtonElement | null) {
    provenanceReturnFocus.current = returnFocus;
    setProvenanceWorkout(workout);
  }
  function closeProvenance() {
    setProvenanceWorkout(undefined);
  }
  function openDeletion(workout: Workout, returnFocus: HTMLButtonElement | null) {
    deletionReturnFocus.current = returnFocus;
    setDeletionError("");
    setDeletionTarget(workout);
  }
  async function exportRoute(workout: Workout, format: "geojson" | "points") {
    setExportError("");
    try {
      const suffix = format === "geojson" ? "route" : "route/points";
      const accept = format === "geojson" ? "application/geo+json, application/problem+json" : "application/json, application/problem+json";
      const download = await downloadApi(`/api/workouts/${encodeURIComponent(workout.id)}/${suffix}`, {}, accept);
      const url = URL.createObjectURL(download.blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = download.filename;
      link.hidden = true;
      document.body.append(link);
      try {
        link.click();
      } finally {
        link.remove();
        URL.revokeObjectURL(url);
      }
    } catch {
      setExportError(`Workout ${format === "geojson" ? "GeoJSON" : "points"} could not be exported. Try again.`);
    }
  }
  const updateSort = (field: WorkoutColumn) => {
    const next = sort.field === field ? { field, direction: sort.direction === "asc" ? "desc" as const : "asc" as const } : { field, direction: field === "date" ? "desc" as const : "asc" as const };
    if (onSortChange) onSortChange(next); else setLocalSort(next);
    setSortActivity({ ...next, state: "sorting" });
    setPageState({ page: 1, pageSize: preferences.pageSize });
  };
  const summary = summaryQuery.data;
  const workouts = workoutsQuery.data;
  const pageNeedsCorrection = Boolean(workouts && workouts.pagination.totalItems > 0 &&
    (workouts.pagination.page > workouts.pagination.totalPages || workouts.items.length === 0));
  useEffect(() => {
    if (!workouts || !pageNeedsCorrection) return;
    const target = workouts.pagination.page > workouts.pagination.totalPages
      ? Math.max(1, workouts.pagination.totalPages)
      : 1;
    if (target !== page) setPageState({ page: target, pageSize: preferences.pageSize });
  }, [page, pageNeedsCorrection, preferences.pageSize, workouts]);
  useEffect(() => {
    if (sortActivity?.state !== "sorting" || sortActivity.field !== sort.field || sortActivity.direction !== sort.direction) return;
    if (workoutsQuery.isError) { setSortActivity(undefined); return; }
    if (workoutsQuery.isFetching || workoutsQuery.isPlaceholderData || !workoutsQuery.isSuccess) return;
    setSortActivity({ ...sortActivity, state: "complete" });
  }, [sort, sortActivity, workoutsQuery.isError, workoutsQuery.isFetching, workoutsQuery.isPlaceholderData, workoutsQuery.isSuccess]);
  useEffect(() => {
    if (!provenanceWorkout || !(provenanceQuery.error instanceof ApiError) || provenanceQuery.error.status !== 404) return;
    setProvenanceWorkout(undefined);
    void workoutsQuery.refetch();
    void summaryQuery.refetch();
    setTimeout(() => provenanceReturnFocus.current?.focus());
  }, [provenanceQuery.error, provenanceWorkout]);
  const displayedWorkouts = pageNeedsCorrection ? undefined : workouts;
  const sortActivityLabel = sortActivity && `${COLUMN_LABELS[sortActivity.field]} ${sortActivity.direction === "asc" ? "ascending" : "descending"}`;
  const rangeZone = summary?.range && currentZone(summary.range.timezone);
  const workoutTypes = summary?.byType ?? [];
  const durationBreakdown = descending(workoutTypes, (entry) => Number(entry.totals.duration) / (averages ? entry.totals.count : 1), (entry) => entry.type.displayName)
    .map((entry) => ({ id: entry.type.id, label: entry.type.displayName, value: <DurationValue value={averages ? average(entry.totals.duration, entry.totals.count) : entry.totals.duration} aggregate={!averages} /> }));
  const distanceTypes = workoutTypes.filter((entry) => entry.totals.routeCount > 0 && entry.totals.routedDistance);
  const distanceBreakdown = descending(distanceTypes, (entry) => Number(averages ? entry.totals.routedDistance?.value : entry.totals.distance?.value) / (averages ? entry.totals.routeCount : 1) || 0, (entry) => entry.type.displayName)
    .map((entry) => ({
      id: entry.type.id,
      label: entry.type.displayName,
      value: metric(averages ? averageMetric(entry.totals.routedDistance, entry.totals.routeCount) : entry.totals.distance, "distance", preferences.units, true, !averages) ?? <Unavailable />,
    }));
  const energyBreakdown = descending(workoutTypes, (entry) => Number(entry.totals.energy?.value) / (averages ? entry.totals.count : 1) || 0, (entry) => entry.type.displayName)
    .map((entry) => ({
      id: entry.type.id,
      label: entry.type.displayName,
      value: metric(averages ? averageMetric(entry.totals.energy, entry.totals.count) : entry.totals.energy, "calories", preferences.units) ?? <Unavailable />,
    }));
  const displayedDuration = summary ? (averages ? average(summary.totals.duration, summary.totals.count) : summary.totals.duration) : "0";
  const displayedDistance = summary && (averages ? averageMetric(summary.totals.routedDistance, summary.totals.routeCount) : summary.totals.distance);
  const displayedEnergy = summary && (averages ? averageMetric(summary.totals.energy, summary.totals.count) : summary.totals.energy);
  const toggleAggregateMode = () => setAverages((current) => !current);
  return (
    <main className="summary-page" style={{ "--summary-inset": "var(--space-5)" } as CSSProperties}>
      <div className="summary-contours" aria-hidden="true" />
      <header className="summary-heading">
        <DropdownMenu.Root>
          <DropdownMenu.Trigger className="range-trigger" aria-label="Select date range"><span>Date range</span><strong>{rangeLabel(range)}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger>
          <DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align="end" sideOffset={8}>
            {DATE_SHORTCUTS.map(([value, label]) => <DropdownMenu.Item key={value} onSelect={() => selectRange(value)}>{label}{range === value && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}
            <DropdownMenu.Separator />
            <DropdownMenu.Item onSelect={() => setCustomOpen(true)}>Custom...{explicit && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>
          </DropdownMenu.Content></DropdownMenu.Portal>
        </DropdownMenu.Root>
      </header>
      {saveNotice && <div className="summary-notice" role="status">{saveNotice}</div>}
      {exportError && <div className="summary-notice summary-export-error" role="alert">{exportError}</div>}
      {deletionNotice && <div className="summary-notice" role="status">{deletionNotice}</div>}
      <CustomDateRangeDialog open={customOpen} onOpenChange={setCustomOpen} range={range} onApply={selectRange} />

      <Dialog.Root open={rangeDeletionOpen} onOpenChange={(open) => { if (!rangeDeletion.isPending) { setRangeDeletionOpen(open); if (!open) { setRangeConfirmation(""); setRangeDeletionError(""); } } }}>
        <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content deletion-dialog single-deletion-dialog range-deletion-dialog"
          onOpenAutoFocus={(event) => { event.preventDefault(); setTimeout(() => rangeDeletionCancelRef.current?.focus()); }}
          onCloseAutoFocus={(event) => { event.preventDefault(); document.getElementById("workouts-heading")?.focus(); }}>
          <div className="dialog-heading"><div><Dialog.Title>Delete workouts in this range?</Dialog.Title><Dialog.Description>{explicit ? `Delete all workouts from ${rangeDate(explicit[1])} to ${rangeDate(explicit[2])}, inclusive?` : "Delete all workouts in this explicit range?"}</Dialog.Description></div></div>
          <p className="deletion-confirmation-message">These workouts will be hidden from view immediately. All associated data, including routes, import history, and derived data, will be purged by a background task. This cannot be undone.</p>
          {rangeDeletionError && <p ref={rangeDeletionErrorRef} className="error-summary" role="alert" tabIndex={-1}>{rangeDeletionError}</p>}
          <div className="field range-confirmation"><label htmlFor="range-delete-confirmation">Type <strong>DELETE</strong> to confirm.</label><input id="range-delete-confirmation" value={rangeConfirmation} placeholder="DELETE" autoComplete="off" onChange={(event) => setRangeConfirmation(event.target.value)} /></div>
          <div className="dialog-actions"><Dialog.Close ref={rangeDeletionCancelRef} type="button" className="secondary" disabled={rangeDeletion.isPending}>Cancel</Dialog.Close><button className="danger-action" disabled={rangeDeletion.isPending || rangeConfirmation !== "DELETE"} onClick={() => { if (explicit) rangeDeletion.mutate({ startDate: explicit[1], endDate: explicit[2] }); }}>Yes</button></div>
        </Dialog.Content></Dialog.Portal>
      </Dialog.Root>

      <Dialog.Root open={Boolean(deletionTarget)} onOpenChange={(open) => { if (!open && !deletion.isPending) { setDeletionTarget(undefined); setDeletionError(""); } }}>
        <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content deletion-dialog single-deletion-dialog"
          onOpenAutoFocus={(event) => { event.preventDefault(); setTimeout(() => deletionCancelRef.current?.focus()); }}
          onCloseAutoFocus={(event) => { event.preventDefault(); const target = deletionReturnFocus.current; if (target?.isConnected) target.focus(); else document.getElementById("workouts-heading")?.focus(); }}>
          <div className="dialog-heading"><div><Dialog.Title>Delete workout?</Dialog.Title><Dialog.Description>{deletionTarget ? `Delete ${deletionTarget.type.displayName} on ${deletionTarget.localStartDate ? rangeDate(deletionTarget.localStartDate) : "an unknown date"}?` : "Delete this workout?"}</Dialog.Description></div></div>
          <p className="deletion-confirmation-message">This workout will be hidden from view immediately. All associated data, including route, import history, and derived data, will be purged by a background task. This cannot be undone. Are you sure?</p>
          {deletionError && <p ref={deletionErrorRef} className="error-summary" role="alert" tabIndex={-1}>{deletionError}</p>}
          <div className="dialog-actions"><Dialog.Close ref={deletionCancelRef} type="button" className="secondary" disabled={deletion.isPending}>Cancel</Dialog.Close><button className="danger-action" disabled={deletion.isPending} onClick={() => { if (deletionTarget) deletion.mutate(deletionTarget); }}>Yes</button></div>
        </Dialog.Content></Dialog.Portal>
      </Dialog.Root>

      <Dialog.Root open={Boolean(provenanceWorkout)} onOpenChange={(open) => { if (!open) closeProvenance(); }}>
        <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content provenance-dialog" onCloseAutoFocus={(event) => { event.preventDefault(); provenanceReturnFocus.current?.focus(); }}>
          <div className="dialog-heading provenance-dialog-header"><div><Dialog.Title>Workout provenance</Dialog.Title><Dialog.Description>{provenanceWorkout ? `${provenanceWorkout.type.displayName} on ${provenanceWorkout.localStartDate ?? "an unknown date"}. Full import history, oldest first.` : "Full import history."}</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Workout provenance">&times;</Dialog.Close></div>
          <div className="provenance-dialog-body">
            {provenanceQuery.isPending && <p className="provenance-state" role="status">Loading provenance...</p>}
            {provenanceQuery.isError && !(provenanceQuery.error instanceof ApiError && provenanceQuery.error.status === 404) && <QueryError message="Provenance is unavailable." retry={() => void provenanceQuery.refetch()} />}
            {provenanceQuery.data?.items.length === 0 && <p className="provenance-state">No import events were recorded.</p>}
            {provenanceQuery.data && provenanceQuery.data.items.length > 0 && <ol className="provenance-timeline">
              {provenanceQuery.data.items.map((event) => <li key={event.id}>
                <div className="provenance-event-heading"><strong>{provenanceKindLabel(event.kind)}</strong><time dateTime={event.importedAt}>{provenanceTime(event.importedAt, preferences)}</time></div>
                <dl><div><dt>Source</dt><dd>{event.sourceName}</dd></div><div><dt>File</dt><dd>{event.sourceFile}</dd></div><div><dt>Source type</dt><dd>{event.sourceType}</dd></div><div><dt>Job ID</dt><dd><code>{event.jobId}</code></dd></div></dl>
                {event.warnings.length > 0 && <div className="provenance-warnings"><strong>{event.warnings.length} {event.warnings.length === 1 ? "warning" : "warnings"}</strong><ul>{event.warnings.map((warning, index) => <li key={`${warning.code}-${warning.field}-${warning.routePoint ?? "workout"}-${index}`}>{warningLabel(warning)}</li>)}</ul></div>}
              </li>)}
            </ol>}
          </div>
        </Dialog.Content></Dialog.Portal>
      </Dialog.Root>

      <section className="aggregate-section" aria-labelledby="totals-heading">
        <div className="section-line"><h2 id="totals-heading">Range {averages ? "averages" : "totals"}</h2>{summary?.range && rangeZone && <span className="range-metadata"><span>{rangeDate(summary.range.startDate)} to {rangeDate(summary.range.endDate)}</span><ZoneBadge label={rangeZone.label} title={rangeZone.title} /></span>}</div>
        {summaryQuery.isPending && <p className="summary-loading" role="status">Loading range {averages ? "averages" : "totals"}...</p>}
        {summaryQuery.isError && <QueryError message={`Range ${averages ? "averages" : "totals"} are unavailable.`} retry={() => void summaryQuery.refetch()} />}
        {summary && <div className={`aggregate-grid${summaryQuery.isFetching ? " is-refetching" : ""}`} aria-busy={summaryQuery.isFetching}>
          <AggregateCard label="Workouts" value={decimal(String(summary.totals.count), 0)} breakdown={workoutBreakdown(summary)} averages={averages} onToggleMode={toggleAggregateMode} />
          <AggregateCard label="Duration" value={averages ? formatDuration(displayedDuration) : formatAggregateDuration(displayedDuration)} breakdown={durationBreakdown} averages={averages} onToggleMode={toggleAggregateMode} />
          <AggregateCard label="Distance" value={metric(displayedDistance || null, "distance", preferences.units, true, !averages) ?? <Unavailable />} breakdown={distanceBreakdown} averages={averages} onToggleMode={toggleAggregateMode} />
          <AggregateCard label="Energy" value={metric(displayedEnergy || null, "calories", preferences.units) ?? <Unavailable />} breakdown={energyBreakdown} averages={averages} onToggleMode={toggleAggregateMode} />
        </div>}
      </section>

      <section className="workouts-section" aria-labelledby="workouts-heading">
        <div className="section-line"><h2 id="workouts-heading" tabIndex={-1}>Workout log</h2><div className="workout-log-actions">{explicit && displayedWorkouts && displayedWorkouts.pagination.totalItems > 0 && <button type="button" className="range-delete-trigger" aria-label="Delete workouts in this range" title="Delete workouts in this range" onClick={() => { setRangeConfirmation(""); setRangeDeletionError(""); setRangeDeletionOpen(true); }}><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 6h18" /><path d="M8 6V4h8v2" /><path d="M19 6l-1 14H6L5 6" /><path d="M10 11v5M14 11v5" /></svg></button>}{displayedWorkouts && <span>{displayedWorkouts.pagination.totalItems} {displayedWorkouts.pagination.totalItems === 1 ? "session" : "sessions"} | page {displayedWorkouts.pagination.page} of {Math.max(1, displayedWorkouts.pagination.totalPages)}</span>}</div></div>
        {(workoutsQuery.isPending || pageNeedsCorrection) && <p className="summary-loading" role="status">Loading workouts...</p>}
        {workoutsQuery.isError && <QueryError message="Workouts are unavailable." retry={() => void workoutsQuery.refetch()} />}
        {displayedWorkouts && displayedWorkouts.items.length === 0 && <div className="summary-empty"><strong>No workouts in this range.</strong><span>Choose another date range to continue tracing your archive.</span></div>}
        {sortActivity?.state === "sorting" && <p className="visually-hidden workout-sort-status" role="status">Sorting by {sortActivityLabel}...</p>}
        {sortActivity?.state === "complete" && <span className="visually-hidden" role="status">Sorted by {sortActivityLabel}.</span>}
        {displayedWorkouts && displayedWorkouts.items.length > 0 && <div className="workout-results" aria-busy={workoutsQuery.isFetching}><WorkoutTable data={displayedWorkouts} preferences={preferences} sort={sort} onSort={updateSort} onShowOnMap={onShowOnMap} onViewProvenance={openProvenance} onExportGeoJSON={(workout) => void exportRoute(workout, "geojson")} onExportPoints={(workout) => void exportRoute(workout, "points")} onDelete={openDeletion} /></div>}
        {displayedWorkouts && displayedWorkouts.pagination.totalPages > 0 && <nav className="pagination" aria-label="Workout pages"><button className="secondary" disabled={page <= 1 || workoutsQuery.isFetching} onClick={() => setPageState((current) => ({ page: current.page - 1, pageSize: preferences.pageSize }))}>Previous</button><span>Page {displayedWorkouts.pagination.page} of {displayedWorkouts.pagination.totalPages}</span><button className="secondary" disabled={page >= displayedWorkouts.pagination.totalPages || workoutsQuery.isFetching} onClick={() => setPageState((current) => ({ page: current.page + 1, pageSize: preferences.pageSize }))}>Next</button></nav>}
      </section>
    </main>
  );
}
