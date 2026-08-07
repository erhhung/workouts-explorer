import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery } from "@tanstack/react-query";
import { type CSSProperties, type FormEvent, type PointerEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import {
  api,
  type DateRangeEnum,
  type DateRangePreference,
  type ExactMetric,
  type Preferences,
  type SummaryTotals,
  type Workout,
  type WorkoutColumn,
  type WorkoutList,
  type WorkoutSortDirection,
  type WorkoutSummary,
} from "./api";
import { instantZone, offsetLabel, Tooltip, zoneAbbreviation, zoneOffsetAt, ZoneBadge } from "./Tooltip";

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
  calories: "Calories", heartRate: "Heart rate", elevation: "Elevation",
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

function initialRange(preference?: DateRangePreference | null) {
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
  return explicit ? `${explicit[1]} to ${explicit[2]}` : "Last 30 days";
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

function DurationValue({ value, focusable = true, aggregate = false }: { value: string; focusable?: boolean; aggregate?: boolean }) {
  const display = aggregate ? formatAggregateDuration(value) : formatDuration(value);
  const exact = durationTooltip(value, aggregate);
  if (!focusable) return <span className="duration-value" title={exact} aria-label={`Duration ${display}; rounded to nearest second ${exact}`}>{display}</span>;
  return <Tooltip content={exact} className="duration-value" focusable={focusable} label={`Duration ${display}; rounded to nearest second ${exact}`}>{display}</Tooltip>;
}

function Unavailable({ children = "Unavailable" }: { children?: ReactNode }) {
  return <span className="unavailable">{children}</span>;
}

function metric(metricValue: ExactMetric | null, kind: "distance" | "pace" | "calories" | "heartRate" | "elevation", units: Preferences["units"], includeUnit = true, aggregate = false) {
  if (!metricValue) return null;
  let value = Number(metricValue.value);
  let unit = metricValue.unit;
  if (units === "imperial" && kind === "distance" && unit === "km") { value *= 0.621371192; unit = "mi"; }
  if (units === "imperial" && kind === "elevation" && unit === "m") { value *= 3.280839895; unit = "ft"; }
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

function resolveZone(workoutZone: string | null, offset: number | null, instant: Date) {
  if (workoutZone) {
    const zoneOffset = zoneOffsetAt(workoutZone, instant);
    if (zoneOffset != null && (offset == null || zoneOffset === offset)) return workoutZone;
  }
  return null;
}

function rangeDate(value: string) {
  if (!validDate(value)) return value;
  const [year, month, day] = value.split("-").map(Number);
  return new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" })
    .format(new Date(Date.UTC(year, month - 1, day))).replace(",", "");
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
        zoneTitle: `${timeZone} (${offsetLabel(resolvedOffset)})`,
        unavailable: false,
      };
    }
    if (offset != null) {
      const shifted = new Date(instant.getTime() + offset * 60_000);
      return {
        dateTime: new Intl.DateTimeFormat("en-US", { ...options, timeZone: "UTC" }).format(shifted),
        weekday: new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone: "UTC" }).format(shifted),
        zoneLabel: offsetLabel(offset, true),
        zoneTitle: `Recorded offset (${offsetLabel(offset)}); timezone unavailable`,
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
  if (column === "duration") return <DurationValue value={workout.duration} />;
  const value = metric(workout[column], column, preferences.units, column !== "calories");
  return value ?? <Unavailable />;
}

function QueryError({ message, retry }: { message: string; retry: () => void }) {
  return <div className="summary-query-error" role="alert"><span>{message}</span><button className="secondary" onClick={retry}>Retry</button></div>;
}

function AggregateCard({ label, value, valueTitle, summary, format }: {
  label: string; value: ReactNode; valueTitle?: string; summary: WorkoutSummary; format: (totals: SummaryTotals) => ReactNode;
}) {
  const [pinned, setPinned] = useState(false);
  const [preview, setPreview] = useState(false);
  const panelId = useId();
  const valueTooltipId = useId();
  const visible = pinned || preview;
  function previewPointer(event: PointerEvent<HTMLButtonElement>, next: boolean) {
    if (event.pointerType === "mouse") setPreview(next);
  }
  return (
    <article className={`aggregate-card${visible ? " is-visible" : ""}`}>
      <button type="button" aria-expanded={visible} aria-controls={panelId} aria-describedby={valueTitle ? valueTooltipId : undefined}
        onPointerEnter={(event) => previewPointer(event, true)} onPointerLeave={(event) => previewPointer(event, false)}
        onFocus={(event) => { if (event.currentTarget.matches(":focus-visible")) setPreview(true); }} onBlur={() => setPreview(false)}
        onKeyDown={(event) => { if (event.key === "Escape") { setPinned(false); setPreview(false); } }}
        onClick={() => setPinned((current) => !current)}>
        <span>{label}</span><strong>{value}</strong><small>By workout type <span aria-hidden="true">+</span></small>
      </button>
      {valueTitle && <span className="tooltip-content aggregate-value-tooltip" id={valueTooltipId} role="tooltip">{valueTitle}</span>}
      <div id={panelId} className="aggregate-breakdown" aria-hidden={!visible}>
        {summary.byType.length ? summary.byType.map((entry) => <span key={entry.type.id}><b>{entry.type.displayName}</b><em>{format(entry.totals)}</em></span>) : <span>No workout types</span>}
      </div>
    </article>
  );
}

function WorkoutTable({ data, preferences, sort, onSort }: {
  data: WorkoutList; preferences: Preferences; sort: { field: WorkoutColumn; direction: WorkoutSortDirection };
  onSort: (field: WorkoutColumn) => void;
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
          <colgroup>{columns.map((column, index) => <col key={column} style={{ width: `${columnWidths[index]}%` }} />)}</colgroup>
          <thead><tr>{columns.map((column) => {
            const selected = sort.field === column;
            return <th key={column} aria-sort={selected ? (sort.direction === "asc" ? "ascending" : "descending") : "none"} scope="col"><button onClick={() => onSort(column)} disabled={!SORTABLE_COLUMNS.has(column)}><span className="column-label">{COLUMN_LABELS[column]}</span><span className="sort-indicator" aria-hidden="true">{selected ? (sort.direction === "asc" ? <>&#9650;</> : <>&#9660;</>) : <>&#9650; &#9660;</>}</span></button></th>;
          })}</tr></thead>
          <tbody>{data.items.map((workout) => <tr key={workout.id}>{columns.map((column) => <td key={column}><div className={`workout-cell workout-cell--${column}`}>{columnValue(column, workout, preferences)}</div></td>)}</tr>)}</tbody>
        </table>
      </div>
      <div className="mobile-workouts">
        {data.items.map((workout) => {
          const isExpanded = expanded.has(workout.id);
          const detailsId = `workout-details-${workout.id}`;
          const times = workoutTimes(workout, preferences);
          return <article className={`mobile-workout${isExpanded ? " is-expanded" : ""}`} key={workout.id}>
            <button aria-expanded={isExpanded} aria-controls={detailsId} title={durationTooltip(workout.duration)} onClick={() => setExpanded((current) => { const next = new Set(current); if (next.has(workout.id)) next.delete(workout.id); else next.add(workout.id); return next; })}>
              <DateTimeValue value={times.start} focusableHelp={false} />
              <strong>{workout.type.displayName}</strong><DurationValue value={workout.duration} focusable={false} />
            </button>
            <div id={detailsId} hidden={!isExpanded} className="mobile-workout-details">
              <dl>
                 <div><dt>Local start</dt><dd><DateTimeValue value={times.start} /></dd></div>
                 <div><dt>Local end</dt><dd><DateTimeValue value={times.end} /></dd></div>
                 <div><dt>Duration</dt><dd><DurationValue value={workout.duration} /></dd></div>
                {(["distance", "pace", "calories", "heartRate", "elevation"] as const).map((column) => <div key={column}><dt>{COLUMN_LABELS[column]}</dt><dd>{columnValue(column, workout, preferences)}</dd></div>)}
              </dl>
              <p>{workout.location ?? "Location unavailable"} / {workout.routeAvailable ? "Route available" : "Route unavailable"}</p>
            </div>
          </article>;
        })}
      </div>
    </>
  );
}

export function Summary({ preferences, csrfToken, onDateRangeSaved }: {
  preferences: Preferences; csrfToken: string; onDateRangeSaved: (dateRange: DateRangePreference) => void;
}) {
  const [range, setRange] = useState<DateRangePreference>(() => initialRange(preferences.dateRange));
  const rangeRef = useRef(range);
  const latestSelection = useRef(0);
  const [pageState, setPageState] = useState({ page: 1, pageSize: preferences.pageSize });
  const page = pageState.pageSize === preferences.pageSize ? pageState.page : 1;
  const [sort, setSort] = useState<{ field: WorkoutColumn; direction: WorkoutSortDirection }>({ field: "date", direction: "desc" });
  const [sortActivity, setSortActivity] = useState<{ field: WorkoutColumn; direction: WorkoutSortDirection; state: "sorting" | "complete" }>();
  const [customOpen, setCustomOpen] = useState(false);
  const [customError, setCustomError] = useState<{ message: string; fields: Array<"start" | "end"> }>();
  const [saveNotice, setSaveNotice] = useState("");
  const customErrorRef = useRef<HTMLParagraphElement>(null);
  const customErrorId = useId();
  const explicit = EXPLICIT_RANGE.exec(range);
  useEffect(() => {
    const next = initialRange(preferences.dateRange);
    if (next !== rangeRef.current) {
      latestSelection.current += 1;
      rangeRef.current = next;
      setRange(next);
      setPageState((current) => ({ ...current, page: 1 }));
      setSortActivity(undefined);
      setSaveNotice("");
    }
  }, [preferences.dateRange]);
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
    persistence.mutate({ dateRange: next, sequence });
  }
  function submitCustom(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    const start = String(values.get("startDate"));
    const end = String(values.get("endDate"));
    const invalidFields: Array<"start" | "end"> = [];
    if (!validDate(start)) invalidFields.push("start");
    if (!validDate(end)) invalidFields.push("end");
    if (invalidFields.length) { setCustomError({ message: "Enter valid start and end dates.", fields: invalidFields }); setTimeout(() => customErrorRef.current?.focus()); return; }
    if (start > end) { setCustomError({ message: "Start date must be on or before end date.", fields: ["start", "end"] }); setTimeout(() => customErrorRef.current?.focus()); return; }
    setCustomError(undefined); setCustomOpen(false); selectRange(`${start}/${end}`);
  }
  const summaryQuery = useQuery({
    queryKey: ["summary", range, preferences.timezone, preferences.firstWeekday],
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
  const updateSort = (field: WorkoutColumn) => {
    const next = sort.field === field ? { field, direction: sort.direction === "asc" ? "desc" as const : "asc" as const } : { field, direction: field === "date" ? "desc" as const : "asc" as const };
    setSort(next);
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
  const displayedWorkouts = pageNeedsCorrection ? undefined : workouts;
  const sortActivityLabel = sortActivity && `${COLUMN_LABELS[sortActivity.field]} ${sortActivity.direction === "asc" ? "ascending" : "descending"}`;
  const rangeZone = summary?.range && currentZone(summary.range.timezone);
  return (
    <main className="summary-page" style={{ "--summary-inset": "var(--space-5)" } as CSSProperties}>
      <div className="summary-contours" aria-hidden="true" />
      <header className="summary-heading">
        <DropdownMenu.Root>
          <DropdownMenu.Trigger className="range-trigger" aria-label="Select date range"><span>Date range</span><strong>{rangeLabel(range)}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger>
          <DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align="end" sideOffset={8}>
            <DropdownMenu.Label>Quick ranges</DropdownMenu.Label>
            {DATE_SHORTCUTS.map(([value, label]) => <DropdownMenu.Item key={value} onSelect={() => selectRange(value)}>{label}{range === value && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}
            <DropdownMenu.Separator />
            <DropdownMenu.Item onSelect={() => setCustomOpen(true)}>Custom{explicit && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>
          </DropdownMenu.Content></DropdownMenu.Portal>
        </DropdownMenu.Root>
      </header>
      {saveNotice && <div className="summary-notice" role="status">{saveNotice}</div>}
      <Dialog.Root open={customOpen} onOpenChange={(open) => { setCustomOpen(open); if (!open) setCustomError(undefined); }}>
        <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content custom-range-dialog">
          <div className="dialog-heading"><div><Dialog.Title>Custom date range</Dialog.Title><Dialog.Description>Choose inclusive calendar dates.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Custom date range">&times;</Dialog.Close></div>
          <form className="custom-range-form" onSubmit={submitCustom} noValidate>
            {customError && <p className="error-summary" id={customErrorId} role="alert" tabIndex={-1} ref={customErrorRef}>{customError.message}</p>}
            <div className="field-pair"><div className="field"><label htmlFor="summary-start-date">Start date</label><input id="summary-start-date" name="startDate" type="date" defaultValue={explicit?.[1]} required aria-invalid={customError?.fields.includes("start") || undefined} aria-describedby={customError?.fields.includes("start") ? customErrorId : undefined} /></div><div className="field"><label htmlFor="summary-end-date">End date</label><input id="summary-end-date" name="endDate" type="date" defaultValue={explicit?.[2]} required aria-invalid={customError?.fields.includes("end") || undefined} aria-describedby={customError?.fields.includes("end") ? customErrorId : undefined} /></div></div>
            <div className="dialog-actions"><Dialog.Close type="button" className="secondary">Cancel</Dialog.Close><button className="primary">Apply range</button></div>
          </form>
        </Dialog.Content></Dialog.Portal>
      </Dialog.Root>

      <section className="aggregate-section" aria-labelledby="totals-heading">
        <div className="section-line"><h2 id="totals-heading">Range totals</h2>{summary?.range && rangeZone && <span className="range-metadata"><span>{rangeDate(summary.range.startDate)} through {rangeDate(summary.range.endDate)}</span><ZoneBadge label={rangeZone.label} title={rangeZone.title} /></span>}</div>
        {summaryQuery.isPending && <p className="summary-loading" role="status">Loading range totals...</p>}
        {summaryQuery.isError && <QueryError message="Range totals are unavailable." retry={() => void summaryQuery.refetch()} />}
        {summary && <div className={`aggregate-grid${summaryQuery.isFetching ? " is-refetching" : ""}`} aria-busy={summaryQuery.isFetching}>
          <AggregateCard label="Workouts" value={decimal(String(summary.totals.count), 0)} summary={summary} format={(totals) => decimal(String(totals.count), 0)} />
          <AggregateCard label="Duration" value={<DurationValue value={summary.totals.duration} focusable={false} aggregate />} valueTitle={durationTooltip(summary.totals.duration, true)} summary={summary} format={(totals) => <DurationValue value={totals.duration} aggregate />} />
          <AggregateCard label="Distance" value={metric(summary.totals.distance, "distance", preferences.units, true, true) ?? <Unavailable />} summary={summary} format={(totals) => metric(totals.distance, "distance", preferences.units, true, true) ?? <Unavailable />} />
          <AggregateCard label="Energy" value={metric(summary.totals.energy, "calories", preferences.units) ?? <Unavailable />} summary={summary} format={(totals) => metric(totals.energy, "calories", preferences.units) ?? <Unavailable />} />
        </div>}
      </section>

      <section className="workouts-section" aria-labelledby="workouts-heading">
        <div className="section-line"><h2 id="workouts-heading">Workout log</h2>{displayedWorkouts && <span>{displayedWorkouts.pagination.totalItems} {displayedWorkouts.pagination.totalItems === 1 ? "session" : "sessions"} / Page {displayedWorkouts.pagination.page} of {Math.max(1, displayedWorkouts.pagination.totalPages)}</span>}</div>
        {(workoutsQuery.isPending || pageNeedsCorrection) && <p className="summary-loading" role="status">Loading workouts...</p>}
        {workoutsQuery.isError && <QueryError message="Workouts are unavailable." retry={() => void workoutsQuery.refetch()} />}
        {displayedWorkouts && displayedWorkouts.items.length === 0 && <div className="summary-empty"><strong>No workouts in this range.</strong><span>Choose another date range to continue tracing your archive.</span></div>}
        {sortActivity?.state === "sorting" && <p className="visually-hidden workout-sort-status" role="status">Sorting by {sortActivityLabel}...</p>}
        {sortActivity?.state === "complete" && <span className="visually-hidden" role="status">Sorted by {sortActivityLabel}.</span>}
        {displayedWorkouts && displayedWorkouts.items.length > 0 && <div className="workout-results" aria-busy={workoutsQuery.isFetching}><WorkoutTable data={displayedWorkouts} preferences={preferences} sort={sort} onSort={updateSort} /></div>}
        {displayedWorkouts && displayedWorkouts.pagination.totalPages > 0 && <nav className="pagination" aria-label="Workout pages"><button className="secondary" disabled={page <= 1 || workoutsQuery.isFetching} onClick={() => setPageState((current) => ({ page: current.page - 1, pageSize: preferences.pageSize }))}>Previous</button><span>Page {displayedWorkouts.pagination.page} of {displayedWorkouts.pagination.totalPages}</span><button className="secondary" disabled={page >= displayedWorkouts.pagination.totalPages || workoutsQuery.isFetching} onClick={() => setPageState((current) => ({ page: current.page + 1, pageSize: preferences.pageSize }))}>Next</button></nav>}
      </section>
    </main>
  );
}
