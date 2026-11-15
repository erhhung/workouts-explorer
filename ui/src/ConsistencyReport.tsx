import { useQuery } from "@tanstack/react-query";
import {
  useEffect,
  createContext,
  useContext,
  useCallback,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  api,
  ApiError,
  type ConsistencyReport as ReportData,
  type DateRangePreference,
  type Preferences,
} from "./api";
import { dateRangeRequest } from "./DateRangePicker";
import { useTabActive } from "./RetainedTab";
import { useKeyboardShortcut } from "./keyboardShortcuts";
import { dismissActivePortalTooltip, Tooltip } from "./Tooltip";
import {
  CONSISTENCY_CHARTS,
  CONSISTENCY_BAR_GAP,
  consistencyBucketLabel,
  consistencyChartSlots,
  consistencyCount,
  consistencyScale,
  consistencyTitle,
  consistencyTickLabels,
  consistencyTooltipLabel,
  consistencyValue,
  formatConsistencyValue,
  isPerfectConsistencyMonth,
  type ConsistencyChart,
  type ConsistencyViewState,
} from "./consistency";
import { createConsistencyScrollGroup } from "./consistencyTimeline";

const TimelineContext = createContext<{
  axisWidth: number;
  labelWidth: number;
  measure: (id: string, axis: number, label: number) => void;
  scroll: ReturnType<typeof createConsistencyScrollGroup>;
} | null>(null);

function ConsistencyTimeline({ children }: { children: ReactNode }) {
  const [sizes, setSizes] = useState<Record<string, { axis: number; label: number }>>({});
  const scroll = useMemo(createConsistencyScrollGroup, []);
  const measure = useCallback((id: string, axis: number, label: number) => {
    if (!label) return;
    setSizes((old) =>
      old[id]?.axis === axis && old[id]?.label === label ? old : { ...old, [id]: { axis, label } },
    );
  }, []);
  const axisWidth = Math.max(0, ...Object.values(sizes).map((size) => size.axis)),
    labelWidth = Math.max(0, ...Object.values(sizes).map((size) => size.label)) || 64;
  const value = useMemo(
    () => ({ axisWidth, labelWidth, measure, scroll }),
    [axisWidth, labelWidth, measure, scroll],
  );
  return <TimelineContext.Provider value={value}>{children}</TimelineContext.Provider>;
}

function BarChart({
  data,
  chart,
  preferences,
  active,
}: {
  data: ReportData;
  chart: ConsistencyChart;
  preferences: Preferences;
  active: boolean;
}) {
  const scroll = useRef<HTMLDivElement>(null),
    measureLabel = useRef<HTMLSpanElement>(null);
  const axis = useRef<HTMLDivElement>(null),
    chartRoot = useRef<HTMLDivElement>(null),
    timeline = useContext(TimelineContext)!;
  const [layout, setLayout] = useState({
    width: 600,
    labelWidth: 64,
    left: Math.max(0, consistencyChartSlots(data.buckets.length, 64, 600, 0).width - 600),
    scrollbar: 0,
    axisWidth: 0,
    chartWidth: 600,
  });
  const [selected, setSelected] = useState(Math.max(0, data.buckets.length - 1));
  const drag = useRef<
    { pointerId: number; x: number; y: number; left: number; moved: boolean } | undefined
  >(undefined);
  const suppressClick = useRef(false);
  const [pan, setPan] = useState<"idle" | "pressed" | "dragging">("idle");
  const labels = useMemo(
    () => data.buckets.map((bucket) => consistencyBucketLabel(bucket.bucketStart, data.groupBy)),
    [data.buckets, data.groupBy],
  );
  const values = useMemo(
    () => data.buckets.map((bucket) => consistencyValue(bucket, chart, preferences.units)),
    [data.buckets, chart, preferences.units],
  );
  const scale = useMemo(
    () => consistencyScale(values, chart, data.groupBy),
    [values, chart, data.groupBy],
  );
  const ticks = consistencyTickLabels(scale.ticks, chart, preferences.units);
  const labelWidth = timeline.labelWidth,
    leading = Math.max(0, timeline.axisWidth - layout.axisWidth);
  const slots = consistencyChartSlots(
    data.buckets.length,
    labelWidth,
    layout.width,
    layout.left,
    leading,
  );
  const contentWidth =
    layout.width +
    Math.max(
      0,
      Math.ceil(
        (data.buckets.length ? (data.buckets.length - 1) * slots.slot + labelWidth : 0) +
          timeline.axisWidth -
          layout.chartWidth,
      ),
    );
  const indices = Array.from(
    { length: slots.last - slots.first },
    (_, index) => slots.first + index,
  );
  const selectedIndex = Math.min(selected, Math.max(0, data.buckets.length - 1));
  if (data.buckets.length && !indices.includes(selectedIndex)) indices.push(selectedIndex);
  useLayoutEffect(() => {
    if (!active) return;
    const element = scroll.current;
    if (!element) return;
    let frame = 0,
      disposed = false;
    const update = () => {
      if (disposed || !element.clientWidth) return;
      const labelWidth = Math.ceil(measureLabel.current?.getBoundingClientRect().width || 64);
      const width = element.clientWidth,
        left =
          Math.floor(element.scrollLeft / (labelWidth + CONSISTENCY_BAR_GAP)) *
          (labelWidth + CONSISTENCY_BAR_GAP);
      const scrollbar = element.offsetHeight - element.clientHeight;
      const axisWidth = axis.current?.getBoundingClientRect().width ?? 0,
        chartWidth = chartRoot.current?.getBoundingClientRect().width || width;
      timeline.measure(chart.id, axisWidth, labelWidth);
      setLayout((old) =>
        old.width === width &&
        old.labelWidth === labelWidth &&
        old.left === left &&
        old.scrollbar === scrollbar &&
        old.axisWidth === axisWidth &&
        old.chartWidth === chartWidth
          ? old
          : { width, labelWidth, left, scrollbar, axisWidth, chartWidth },
      );
    };
    const schedule = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(schedule);
    observer?.observe(element);
    if (measureLabel.current) observer?.observe(measureLabel.current);
    if (axis.current) observer?.observe(axis.current);
    const unregister = timeline.scroll.register(element);
    const onScroll = () => {
      timeline.scroll.synchronize(element);
      schedule();
    };
    element.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", schedule);
    void document.fonts?.ready.then(() => {
      if (!disposed) {
        schedule();
      }
    });
    update();
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      observer?.disconnect();
      element.removeEventListener("scroll", onScroll);
      unregister();
      window.removeEventListener("resize", schedule);
    };
  }, [active, data.groupBy]);
  useLayoutEffect(() => {
    const element = scroll.current;
    if (!active || !element?.clientWidth) return;
    timeline.scroll.reconcile();
    const left = Math.floor(element.scrollLeft / slots.slot) * slots.slot;
    setLayout((old) => (old.left === left ? old : { ...old, left }));
  }, [active, layout.width, labelWidth, leading, contentWidth, slots.slot, timeline.scroll]);
  function finishPan() {
    const pointer = drag.current;
    if (!pointer) return;
    drag.current = undefined;
    setPan("idle");
    const element = scroll.current;
    if (element?.hasPointerCapture?.(pointer.pointerId))
      element.releasePointerCapture(pointer.pointerId);
  }
  useEffect(() => {
    if (!active) finishPan();
    const cancel = () => finishPan();
    const visibility = () => {
      if (document.visibilityState === "hidden") cancel();
    };
    window.addEventListener("blur", cancel);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      cancel();
      window.removeEventListener("blur", cancel);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [active]);
  function focusBar(index: number) {
    setSelected(index);
    const element = scroll.current;
    if (element) {
      const left = leading + index * slots.slot,
        right = left + labelWidth;
      if (left < element.scrollLeft) element.scrollLeft = left;
      else if (right > element.scrollLeft + element.clientWidth)
        element.scrollLeft = right - element.clientWidth;
      timeline.scroll.synchronize(element);
    }
    requestAnimationFrame(() =>
      scroll.current
        ?.querySelector<HTMLElement>(`[data-consistency-index="${index}"] .consistency-bar-key`)
        ?.focus({ preventScroll: true }),
    );
  }
  return (
    <>
      <div
        className="consistency-chart"
        ref={chartRoot}
        role="group"
        aria-label={consistencyTitle(chart, data.groupBy)}
        style={{ "--consistency-scrollbar-height": `${layout.scrollbar}px` } as CSSProperties}
      >
        <div className="consistency-y-axis" aria-hidden="true" ref={axis}>
          <span className="consistency-axis-width">
            {ticks.reduce(
              (longest, label) => (label.length > longest.length ? label : longest),
              "",
            )}
          </span>
          <div className="consistency-axis-plot">
            {scale.ticks.map((tick, index) => (
              <span key={index} style={{ bottom: `${(tick / scale.max) * 100}%` }}>
                {ticks[index]}
              </span>
            ))}
          </div>
        </div>
        <div
          className={`consistency-chart-scroll${pan === "dragging" ? " is-dragging" : ""}`}
          ref={scroll}
          onPointerDown={(event) => {
            const element = event.currentTarget;
            if (
              !active ||
              event.button !== 0 ||
              event.isPrimary === false ||
              event.altKey ||
              event.ctrlKey ||
              event.metaKey ||
              element.scrollWidth <= element.clientWidth
            )
              return;
            suppressClick.current = false;
            drag.current = {
              pointerId: event.pointerId,
              x: event.clientX,
              y: event.clientY,
              left: element.scrollLeft,
              moved: false,
            };
            setPan("pressed");
            dismissActivePortalTooltip();
            if (event.pointerType !== "touch") event.preventDefault();
            element.setPointerCapture?.(event.pointerId);
          }}
          onPointerMove={(event) => {
            const pointer = drag.current;
            if (!pointer || pointer.pointerId !== event.pointerId) return;
            const dx = event.clientX - pointer.x,
              dy = event.clientY - pointer.y;
            if (!pointer.moved) {
              if (
                Math.abs(dx) < 4 ||
                (event.pointerType === "touch" && Math.abs(dy) > Math.abs(dx))
              )
                return;
              pointer.moved = true;
              suppressClick.current = true;
              setPan("dragging");
            }
            event.preventDefault();
            event.currentTarget.scrollLeft = Math.max(
              0,
              Math.min(
                event.currentTarget.scrollWidth - event.currentTarget.clientWidth,
                pointer.left - dx,
              ),
            );
            timeline.scroll.synchronize(event.currentTarget);
          }}
          onPointerUp={(event) => {
            if (drag.current?.pointerId === event.pointerId) finishPan();
          }}
          onPointerCancel={(event) => {
            if (drag.current?.pointerId === event.pointerId) finishPan();
          }}
          onLostPointerCapture={(event) => {
            if (drag.current?.pointerId === event.pointerId) finishPan();
          }}
          onClickCapture={(event) => {
            if (suppressClick.current) {
              event.preventDefault();
              event.stopPropagation();
              suppressClick.current = false;
            }
          }}
        >
          <span className="consistency-date-measure" ref={measureLabel} aria-hidden="true">
            <span>May 0000</span>
            <span>12/31/00</span>
          </span>
          <div className="consistency-chart-canvas" style={{ width: contentWidth }}>
            <div className="consistency-plot-lines" aria-hidden="true">
              {[0, ...scale.ticks].map((tick) => (
                <span key={tick} style={{ bottom: `${(tick / scale.max) * 100}%` }} />
              ))}
            </div>
            {indices
              .sort((a, b) => a - b)
              .map((index) => {
                const value = values[index],
                  capped = value !== null && value > scale.max;
                const ratio = value === null ? 0 : Math.max(0, Math.min(1, value / scale.max));
                const formatted = formatConsistencyValue(value, chart, preferences.units, true);
                const description = `${consistencyTooltipLabel(data.buckets[index].bucketStart, data.groupBy)} \u00a0━\u00a0 ${formatted}${value === null && data.buckets[index].workoutCount === 0 ? " (no workouts)" : ""}`;
                const perfect = isPerfectConsistencyMonth(data.buckets[index], data.groupBy, chart);
                return (
                  <div
                    className="consistency-bar-slot"
                    key={data.buckets[index].bucketStart}
                    data-consistency-index={index}
                    style={{ left: leading + index * slots.slot, width: labelWidth }}
                  >
                    <div
                      className={`consistency-bar-target${value === null ? " is-missing" : ""}`}
                      style={
                        {
                          height: capped ? "calc(100% + 0.5rem)" : `${ratio * 100}%`,
                          "--consistency-cap": capped ? "0.5rem" : "0px",
                        } as CSSProperties
                      }
                    >
                      <Tooltip
                        content={description}
                        portal
                        focusable={false}
                        keyboardFocusOnly
                        dismissOnPointerLeave
                        followPointer
                        enabled={active && pan === "idle"}
                        className="consistency-bar-help"
                      >
                        <span
                          className="consistency-bar-key"
                          tabIndex={selectedIndex === index ? 0 : -1}
                          role="img"
                          aria-label={description + (perfect ? ", perfect month" : "")}
                          onFocus={() => setSelected(index)}
                          onKeyDown={(event) => {
                            if (event.altKey || event.ctrlKey || event.metaKey) return;
                            const next =
                              event.key === "ArrowLeft"
                                ? Math.max(0, index - 1)
                                : event.key === "ArrowRight"
                                  ? Math.min(labels.length - 1, index + 1)
                                  : event.key === "Home"
                                    ? 0
                                    : event.key === "End"
                                      ? labels.length - 1
                                      : undefined;
                            if (next !== undefined) {
                              event.preventDefault();
                              event.stopPropagation();
                              focusBar(next);
                            }
                          }}
                        >
                          <span className="consistency-bar-fill" aria-hidden="true" />
                          {capped && <span className="consistency-bar-cap" aria-hidden="true" />}
                          {perfect && (
                            <span
                              className="consistency-perfect-star"
                              aria-hidden="true"
                              style={{
                                left: (labelWidth * 0.75 - 22) / 2,
                                top: (labelWidth * 0.75 - 22) / 2,
                              }}
                            >
                              ⭐
                            </span>
                          )}
                        </span>
                      </Tooltip>
                    </div>
                    <span className="consistency-x-label" aria-hidden="true">
                      {labels[index]}
                    </span>
                  </div>
                );
              })}
          </div>
        </div>
      </div>
    </>
  );
}

export function ConsistencyReport({
  accountKey,
  preferences,
  dateRange,
  deletionRevision,
  pollingIntervalSeconds,
  active,
  state,
  onChange,
}: {
  accountKey: string;
  preferences: Preferences;
  dateRange: DateRangePreference;
  deletionRevision: number;
  pollingIntervalSeconds: number;
  active: boolean;
  state: ConsistencyViewState;
  onChange: (state: ConsistencyViewState) => void;
}) {
  const tabActive = useTabActive(),
    root = useRef<HTMLDivElement>(null),
    id = useId();
  const enabled = tabActive && active;
  const scope = `${accountKey}:${dateRange}:${preferences.timezone}:${preferences.firstWeekday}:${deletionRevision}`;
  const query = useQuery<ReportData>({
    queryKey: ["consistency", scope, state.groupBy],
    enabled,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({
        groupBy: state.groupBy,
        firstWeekday: preferences.firstWeekday,
      });
      for (const [key, value] of Object.entries(dateRangeRequest(dateRange, preferences.timezone)))
        if (value !== undefined) params.set(key, value);
      const result = await api<ReportData>(`/api/stats/consistency?${params}`, { signal });
      if (result.groupBy !== state.groupBy || result.firstWeekday !== preferences.firstWeekday)
        throw new Error("Consistency context changed");
      return result;
    },
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === scope ? previous : undefined,
    refetchInterval: enabled ? Math.max(10, Math.min(60, pollingIntervalSeconds)) * 1000 : false,
    retry: false,
  });
  const accepted = useRef<{ scope: string; data: ReportData } | undefined>(undefined);
  if (query.isSuccess && !query.isPlaceholderData) accepted.current = { scope, data: query.data };
  const data =
    query.data ??
    (query.isError && accepted.current?.scope === scope ? accepted.current.data : undefined);
  const group = data?.groupBy ?? state.groupBy;
  const closed = new Set(state.closed);
  useEffect(() => {
    if (!enabled) dismissActivePortalTooltip();
  }, [enabled]);
  function selectGroup(groupBy: ConsistencyViewState["groupBy"]) {
    if (state.groupBy === groupBy) return;
    if (document.activeElement?.closest(".consistency-chart"))
      root.current
        ?.querySelector<HTMLButtonElement>(`[data-consistency-group="${groupBy}"]`)
        ?.focus({ preventScroll: true });
    onChange({ ...state, groupBy });
  }
  useKeyboardShortcut("stats", root, (shortcut) => {
    if (!enabled || shortcut.kind !== "stats" || shortcut.group === "year") return false;
    selectGroup(shortcut.group);
    return true;
  });
  return (
    <div className="consistency-report" ref={root} aria-busy={query.isFetching}>
      <div className="consistency-controls">
        <fieldset className="stats-grouping consistency-grouping">
          <legend>Show aggregate stats by</legend>
          <div className="map-mode-controls stats-group-buttons">
            {(["week", "month"] as const).map((value) => (
              <button
                type="button"
                key={value}
                data-consistency-group={value}
                aria-pressed={state.groupBy === value}
                onClick={() => selectGroup(value)}
                onKeyDown={(event) => {
                  if (
                    event.altKey ||
                    event.ctrlKey ||
                    event.metaKey ||
                    event.nativeEvent.isComposing
                  )
                    return;
                  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
                  event.preventDefault();
                  event.stopPropagation();
                  const next = value === "week" ? "month" : "week";
                  selectGroup(next);
                  root.current
                    ?.querySelector<HTMLButtonElement>(`[data-consistency-group="${next}"]`)
                    ?.focus();
                }}
              >
                {value === "week" ? "Week" : "Month"}
              </button>
            ))}
          </div>
        </fieldset>
        <button
          type="button"
          className="workout-clear-filters consistency-toggle-all"
          onClick={() =>
            onChange({
              ...state,
              closed:
                closed.size === CONSISTENCY_CHARTS.length
                  ? []
                  : CONSISTENCY_CHARTS.map((chart) => chart.id),
            })
          }
        >
          {closed.size === CONSISTENCY_CHARTS.length ? "Open all sections" : "Close all sections"}
        </button>
      </div>
      {query.isFetching && (
        <span className="visually-hidden" role="status">
          {data ? "Updating Consistency..." : "Loading Consistency..."}
        </span>
      )}
      {query.isError && (
        <div className="map-banner stats-report-feedback" role="alert">
          <span>
            {query.error instanceof ApiError && query.error.status === 422
              ? "This range is too large to chart. Choose a shorter range or Month grouping."
              : "Consistency could not be loaded. Please try again."}
          </span>
          <button className="secondary" onClick={() => void query.refetch()}>
            Retry
          </button>
        </div>
      )}
      {data && data.undatedWorkoutCount > 0 && (
        <p className="consistency-undated">
          {consistencyCount(data.undatedWorkoutCount)} workouts without a recorded local date are
          excluded.
        </p>
      )}
      <ConsistencyTimeline key={`${scope}:${group}`}>
        <div className="consistency-sections">
          {CONSISTENCY_CHARTS.map((chart) => {
            const open = !closed.has(chart.id),
              title = consistencyTitle(chart, group);
            return (
              <section
                className="sync-card consistency-section"
                key={chart.id}
                aria-labelledby={`${id}-${chart.id}-heading`}
              >
                <div className="sync-card-heading">
                  <h2 id={`${id}-${chart.id}-heading`}>
                    <button
                      type="button"
                      className="consistency-disclosure"
                      aria-expanded={open}
                      aria-controls={`${id}-${chart.id}-body`}
                      onClick={() =>
                        onChange({
                          ...state,
                          closed: open
                            ? [...closed, chart.id]
                            : state.closed.filter((value) => value !== chart.id),
                        })
                      }
                    >
                      <span>{title}</span>
                      <svg viewBox="0 0 24 24" aria-hidden="true" className={open ? "is-open" : ""}>
                        <path d="m5 9 7 7 7-7" fill="none" stroke="currentColor" strokeWidth="2" />
                      </svg>
                    </button>
                  </h2>
                </div>
                <div
                  id={`${id}-${chart.id}-body`}
                  hidden={!open}
                  inert={!open}
                  className="consistency-section-body"
                >
                  {data ? (
                    <BarChart
                      key={`${scope}:${group}`}
                      data={data}
                      chart={chart}
                      preferences={preferences}
                      active={enabled && open}
                    />
                  ) : (
                    <div className="consistency-chart consistency-placeholder">
                      {query.isError ? "Report unavailable." : "Loading chart..."}
                    </div>
                  )}
                </div>
              </section>
            );
          })}
        </div>
      </ConsistencyTimeline>
    </div>
  );
}
