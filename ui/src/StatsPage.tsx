import * as Dialog from "@radix-ui/react-dialog";
import { ContentBackground } from "./ContentBackground";
import { dialogTablePageSize, dialogTableRowSlots } from "./dialogTable";
import { DialogTableViewport } from "./DialogTableViewport";
import { useKeyboardShortcut } from "./keyboardShortcuts";
import { useTabActive } from "./RetainedTab";
import {
  type Dispatch,
  type SetStateAction,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  api,
  ApiError,
  type DateRangePreference,
  type NewCoverageBucketList,
  type NewCoverageMetric,
  type NewCoverageVisit,
  type NewCoverageVisitList,
  type Pagination,
  type Preferences,
  type StatsGrouping,
  type StatsReportScope,
} from "./api";
import { CoverageCellValue } from "./CoverageTableCell";
import { CustomDateRangeDialog } from "./CustomDateRangeDialog";
import { DateRangePicker, dateRangeRequest } from "./DateRangePicker";
import { useRecentDateRanges } from "./RecentDateRanges";
import { Tooltip } from "./Tooltip";
import { PageNavigation } from "./PageNavigation";
import { formatDateOnly } from "./date";

export const STATS_REPORTS = [{ id: "new-coverage", title: "New Coverage" }] as const;
type BucketField = "bucket" | "allTime" | "pastYear";
type VisitField = "name" | "place" | "newVisit" | "previousVisit";
interface Sort<F extends string> {
  field: F;
  direction: "asc" | "desc";
}
export interface StatsViewState {
  activeReport: (typeof STATS_REPORTS)[number]["id"];
  newCoverage: { rangeKey?: string; groupBy: StatsGrouping; sort: Sort<BucketField>; page: number };
}
export const DEFAULT_STATS_STATE: StatsViewState = {
  activeReport: "new-coverage",
  newCoverage: { groupBy: "month", sort: { field: "bucket", direction: "desc" }, page: 1 },
};
const METRIC_LABELS: Record<NewCoverageMetric, string> = {
  all_time: "New visits (all time)",
  past_year: "New visits (past year)",
};

export function statsBucketLabel(value: string, group: StatsGrouping) {
  return group === "year"
    ? value.slice(0, 4)
    : `${new Intl.DateTimeFormat("en-US", { month: "long", timeZone: "UTC" }).format(new Date(`${value}T00:00:00Z`))} ${value.slice(0, 4)}`;
}

export type StatsMapTarget =
  | { kind: "entity"; entityId: string; entityKind: "path" | "park"; name: string }
  | { kind: "workout"; workoutId: string };

function nextSort<F extends string>(current: Sort<F>, field: F): Sort<F> {
  return {
    field,
    direction: current.field === field && current.direction === "desc" ? "asc" : "desc",
  };
}

function SortHeader<F extends string>({
  field,
  label,
  sort,
  onSort,
  help,
}: {
  field: F;
  label: string;
  sort: Sort<F>;
  onSort: (field: F) => void;
  help?: string;
}) {
  const selected = sort.field === field;
  const button = (
    <button type="button" onClick={() => onSort(field)}>
      <span className="column-label">{label}</span>
      <span className="sort-indicator" aria-hidden="true">
        {selected ? sort.direction === "asc" ? <>&#9650;</> : <>&#9660;</> : <>&#9650; &#9660;</>}
      </span>
    </button>
  );
  return (
    <th
      scope="col"
      aria-sort={selected ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}
    >
      {help ? (
        <Tooltip content={help} portal multiline focusable={false} className="stats-header-tooltip">
          {button}
        </Tooltip>
      ) : (
        button
      )}
    </th>
  );
}

function useReportRead<T extends { scope: StatsReportScope }>(
  scope: StatsReportScope | undefined,
  path: string | undefined,
  refresh: number,
  onUnavailable: () => void,
  continuityKey: string,
) {
  const [state, setState] = useState<{
    path?: string;
    value?: T;
    error?: string;
    pending: boolean;
    continuityKey?: string;
    valuePath?: string;
    refresh?: number;
  }>({ pending: false });
  const unavailable = useRef(onUnavailable);
  unavailable.current = onUnavailable;
  useEffect(() => {
    if (!scope || !path) return;
    const controller = new AbortController();
    setState((current) => ({
      path,
      continuityKey,
      value: current.continuityKey === continuityKey ? current.value : undefined,
      valuePath: current.continuityKey === continuityKey ? current.valuePath : undefined,
      pending: true,
      refresh,
    }));
    void api<T>(path, { signal: controller.signal })
      .then((value) => {
        if (controller.signal.aborted) return;
        if (
          value.scope.id !== scope.id ||
          value.scope.dataGeneration !== scope.dataGeneration ||
          value.scope.coverageDisplayRevision !== scope.coverageDisplayRevision ||
          value.scope.coverageDisplayPolicy !== scope.coverageDisplayPolicy
        )
          throw new Error("Report scope changed");
        setState({ path, valuePath: path, continuityKey, value, pending: false, refresh });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && error.status === 404) {
          unavailable.current();
          return;
        }
        setState((current) => ({
          ...current,
          path,
          pending: false,
          error:
            error instanceof ApiError && error.status === 422
              ? "This report page exceeds the response limit. Reduce rows per page in Preferences."
              : "This report could not be loaded. Please try again.",
        }));
      });
    return () => controller.abort();
  }, [path, scope?.id, refresh, continuityKey]);
  const compatible = state.continuityKey === continuityKey;
  return {
    value: compatible ? state.value : undefined,
    pending: !scope || state.path !== path || state.pending || state.refresh !== refresh,
    current: compatible && state.valuePath === path && state.value?.scope.id === scope?.id,
    error: compatible && state.path === path ? state.error : undefined,
  };
}

function PageSpacer({
  pagination,
  items,
  columns,
  capacity,
}: {
  pagination: Pagination;
  items: number;
  columns: number;
  capacity?: number;
}) {
  const missing = Math.max(
    0,
    (capacity ?? Math.min(pagination.totalItems, pagination.pageSize)) - items,
  );
  return missing ? (
    <tr className="stats-page-spacer" aria-hidden="true">
      <td colSpan={columns} style={{ height: `${missing * 3}rem` }} />
    </tr>
  ) : null;
}

function usePagingScroll(value: unknown, pending: boolean, current: boolean) {
  const tabActive = useTabActive();
  const ref = useRef<HTMLDivElement>(null);
  const position = useRef<{ x: number; y: number; top: number; left: number } | undefined>(
    undefined,
  );
  useLayoutEffect(() => {
    if (!tabActive || !current || pending || !position.current) return;
    const saved = position.current;
    position.current = undefined;
    if (ref.current) {
      ref.current.scrollTop = saved.top;
      ref.current.scrollLeft = saved.left;
    }
    window.scrollTo(saved.x, saved.y);
  }, [tabActive, value, pending, current]);
  return {
    ref,
    reset(scroll = true) {
      position.current = undefined;
      if (scroll && ref.current) ref.current.scrollTop = 0;
    },
    capture() {
      position.current = {
        x: window.scrollX,
        y: window.scrollY,
        top: ref.current?.scrollTop ?? 0,
        left: ref.current?.scrollLeft ?? 0,
      };
    },
  };
}

function NewVisitsDialog({
  scope,
  group,
  bucket,
  metric,
  count,
  pageSize,
  refresh,
  onUnavailable,
  onClose,
  returnFocus,
  onShowOnMap,
}: {
  scope: StatsReportScope;
  group: StatsGrouping;
  bucket: string;
  metric: NewCoverageMetric;
  count: number;
  pageSize: number;
  refresh: number;
  onUnavailable: () => void;
  onClose: () => void;
  returnFocus: () => void;
  onShowOnMap?: (target: StatsMapTarget) => void;
}) {
  const searchId = useId();
  const [input, setInput] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<Sort<VisitField>>({ field: "newVisit", direction: "desc" });
  const [attempt, setAttempt] = useState(0);
  const effectivePageSize = dialogTablePageSize(pageSize);
  const rowSlots = dialogTableRowSlots(count, pageSize);
  const lastPagination = useRef<Pagination | undefined>(undefined);
  useEffect(() => {
    if (input === search) return;
    const timer = window.setTimeout(() => {
      setSearch(input);
      setPage(1);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [input, search]);
  const params = new URLSearchParams({
    generation: String(scope.dataGeneration),
    groupBy: group,
    bucketStart: bucket,
    metric,
    search,
    page: String(page),
    pageSize: String(effectivePageSize),
    sort: `${sort.field}:${sort.direction}`,
  });
  const path = `/api/stats/report-scopes/${scope.id}/new-coverage/visits?${params}`;
  const result = useReportRead<NewCoverageVisitList>(
    scope,
    path,
    refresh + attempt,
    onUnavailable,
    `${scope.id}:${group}:${bucket}:${metric}`,
  );
  const data = result.value;
  useLayoutEffect(() => {
    if (data && result.current && !result.pending) lastPagination.current = data.pagination;
  }, [data, result.current, result.pending]);
  const pagination = data?.pagination ??
    lastPagination.current ?? {
      page,
      pageSize: effectivePageSize,
      totalItems: count,
      totalPages: Math.ceil(count / effectivePageSize),
    };
  const paging = usePagingScroll(data, result.pending, result.current);
  const appliedSearch = useRef(search);
  useLayoutEffect(() => {
    paging.reset(false);
  }, [search]);
  useLayoutEffect(() => {
    if (data && result.current && !result.pending && appliedSearch.current !== search) {
      paging.reset();
      appliedSearch.current = search;
    }
  }, [data, result.current, result.pending, search]);
  const updating = result.pending || !result.current || input !== search;
  useEffect(() => {
    if (!result.current || result.pending) return;
    if (data && data.totalVisits !== count) onUnavailable();
    else if (data && page > Math.max(1, data.pagination.totalPages))
      setPage(Math.max(1, data.pagination.totalPages));
  }, [data, page, count, result.current, result.pending]);
  const inRange = (date: string) => date >= scope.range.startDate && date <= scope.range.endDate;
  const dateCell = (visit: NewCoverageVisit, previous: boolean) => {
    const date = previous ? visit.previousVisit : visit.newVisit;
    const workoutId = previous ? visit.previousVisitWorkoutId : visit.newVisitWorkoutId;
    if (!date) return <span className="workout-table-na">n/a</span>;
    return inRange(date) && workoutId && onShowOnMap ? (
      <button
        type="button"
        className="stats-cell-link"
        key={`${visit.entityId}:${workoutId}:${date}`}
        aria-disabled={updating}
        onClick={() => {
          if (updating) return;
          onClose();
          onShowOnMap({ kind: "workout", workoutId });
        }}
      >
        <CoverageCellValue text={formatDateOnly(date)} />
      </button>
    ) : (
      <CoverageCellValue text={formatDateOnly(date)} />
    );
  };
  const columns: { field: VisitField; label: string }[] = [
    { field: "name", label: "Road/Path/Park" },
    { field: "place", label: "City/County/Region" },
    { field: "newVisit", label: "New visit" },
    ...(metric === "past_year"
      ? [{ field: "previousVisit" as const, label: "Previous visit" }]
      : []),
  ];
  const title = `${METRIC_LABELS[metric]} in ${statsBucketLabel(bucket, group)}`;
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className={`dialog-content road-coverage-dialog stats-visits-dialog${metric === "past_year" ? " stats-visits-dialog--returns" : ""}`}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            returnFocus();
          }}
        >
          <div className="dialog-heading">
            <div>
              <Dialog.Title>{title}</Dialog.Title>
              <Dialog.Description className="visually-hidden">
                Named Coverage identities and the dates of their qualifying visits.
              </Dialog.Description>
            </div>
            <Dialog.Close className="icon-button" aria-label="Close new visits">
              &times;
            </Dialog.Close>
          </div>
          <div className="coverage-search">
            <svg
              aria-hidden="true"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
            >
              <circle cx="11" cy="11" r="7" />
              <path d="m16 16 5 5" />
            </svg>
            <label className="visually-hidden" htmlFor={searchId}>
              Search roads, paths, parks, cities, or regions
            </label>
            <input
              id={searchId}
              type="search"
              value={input}
              maxLength={256}
              onChange={(event) => setInput(event.target.value)}
              placeholder="Search roads, paths, parks, cities, or regions"
            />
          </div>
          {result.error && (
            <div className="map-banner stats-visits-error" role="alert">
              <span>{result.error}</span>
              <button className="secondary" onClick={() => setAttempt((value) => value + 1)}>
                Retry
              </button>
            </div>
          )}
          <DialogTableViewport
            scrollRef={paging.ref}
            className="stats-visits-table-wrap"
            busy={result.pending || input !== search}
            rowSlots={rowSlots}
            rowClassName="stats-row-metric"
            rowSample={<span className="coverage-cell-value">{"\u00a0"}</span>}
            message={
              result.error
                ? undefined
                : !data
                  ? "Loading visits..."
                  : !data.items.length
                    ? "No visits match this search."
                    : undefined
            }
          >
            <table className="workout-table stats-visits-table">
              <thead>
                <tr>
                  {columns.map((column) => (
                    <SortHeader
                      key={column.field}
                      {...column}
                      sort={sort}
                      onSort={(field) => {
                        setSort((current) => nextSort(current, field));
                        setPage(1);
                      }}
                    />
                  ))}
                </tr>
              </thead>
              <tbody>
                {data &&
                  data.items.length > 0 &&
                  Array.from({ length: rowSlots }, (_, index) => {
                    const visit = data.items[index];
                    return visit ? (
                      <tr key={index}>
                        <td className={onShowOnMap ? "stats-action-cell" : undefined}>
                          {onShowOnMap ? (
                            <button
                              key={visit.entityId}
                              type="button"
                              className="stats-cell-link"
                              aria-disabled={updating}
                              onClick={() => {
                                if (updating) return;
                                onClose();
                                onShowOnMap({
                                  kind: "entity",
                                  entityId: visit.entityId,
                                  entityKind: visit.entityKind,
                                  name: visit.name,
                                });
                              }}
                            >
                              <CoverageCellValue text={visit.name} />
                            </button>
                          ) : (
                            <CoverageCellValue text={visit.name} />
                          )}
                        </td>
                        <td>
                          <CoverageCellValue text={visit.place} />
                        </td>
                        <td
                          className={
                            onShowOnMap && inRange(visit.newVisit) ? "stats-action-cell" : undefined
                          }
                        >
                          {dateCell(visit, false)}
                        </td>
                        {metric === "past_year" && (
                          <td
                            className={
                              onShowOnMap && visit.previousVisit && inRange(visit.previousVisit)
                                ? "stats-action-cell"
                                : undefined
                            }
                          >
                            {dateCell(visit, true)}
                          </td>
                        )}
                      </tr>
                    ) : (
                      <tr key={index} hidden aria-hidden="true">
                        <td colSpan={columns.length} />
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </DialogTableViewport>
          <footer className="road-coverage-footer stats-visits-footer">
            <PageNavigation
              value={pagination}
              label="New visit pages"
              reserveTotalPages={Math.ceil(count / effectivePageSize)}
              busy={result.pending || !result.current || input !== search}
              unavailable={
                (!data && !lastPagination.current) || (Boolean(result.error) && !result.current)
              }
              onPage={(next) => {
                paging.capture();
                setPage(next);
              }}
            />
          </footer>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function StatsPage({
  accountKey,
  preferences,
  csrfToken,
  dateRange,
  onDateRangeSelected,
  onRecentDateRangesSaved,
  state,
  onStateChange,
  pollingIntervalSeconds,
  deletionRevision = 0,
  onShowOnMap,
}: {
  accountKey: string;
  preferences: Preferences;
  csrfToken: string;
  dateRange: DateRangePreference;
  onDateRangeSelected: (range: DateRangePreference) => void;
  onRecentDateRangesSaved: (ranges: string[]) => void;
  state: StatsViewState;
  onStateChange: Dispatch<SetStateAction<StatsViewState>>;
  pollingIntervalSeconds: number;
  deletionRevision?: number;
  onShowOnMap?: (target: StatsMapTarget) => void;
}) {
  const tabActive = useTabActive();
  const tabActiveRef = useRef(tabActive);
  tabActiveRef.current = tabActive;
  const recent = useRecentDateRanges(preferences, onRecentDateRangesSaved);
  const [customOpen, setCustomOpen] = useState(false);
  const [rangeError, setRangeError] = useState("");
  const rangeSaveChain = useRef(Promise.resolve());
  const rangeSaveSequence = useRef(0);
  const rangeKey = `${dateRange}:${preferences.timezone}`;
  const scopeKey = `${accountKey}:${rangeKey}:${deletionRevision}`;
  const current =
    state.newCoverage.rangeKey === rangeKey
      ? state.newCoverage
      : { ...state.newCoverage, rangeKey, sort: DEFAULT_STATS_STATE.newCoverage.sort, page: 1 };
  const currentRef = useRef(current);
  currentRef.current = current;
  const [prepared, setPrepared] = useState<{ key: string; scope: StatsReportScope }>();
  const scope = prepared?.key === scopeKey ? prepared.scope : undefined;
  const [scopeAttempt, setScopeAttempt] = useState(0);
  const [prepareError, setPrepareError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [dialog, setDialog] = useState<{
    bucket: string;
    metric: NewCoverageMetric;
    count: number;
    trigger: HTMLButtonElement;
  }>();
  const heading = useRef<HTMLHeadingElement>(null);
  const renewalTimer = useRef<number | undefined>(undefined);
  const renewals = useRef(0);
  const preparing = useRef(false);
  const refreshAction = useRef<() => void>(() => undefined);
  useEffect(() => {
    onStateChange((old) =>
      old.newCoverage.rangeKey === rangeKey
        ? old
        : {
            ...old,
            newCoverage: {
              ...old.newCoverage,
              rangeKey,
              page: 1,
              sort: DEFAULT_STATS_STATE.newCoverage.sort,
            },
          },
    );
    renewals.current = 0;
    setDialog(undefined);
  }, [rangeKey]);
  useEffect(() => {
    window.clearTimeout(renewalTimer.current);
    renewalTimer.current = undefined;
    renewals.current = 0;
  }, [scopeKey]);
  useEffect(() => {
    const controller = new AbortController();
    let owned: StatsReportScope | undefined;
    preparing.current = true;
    setPrepared(undefined);
    setPrepareError("");
    setDialog(undefined);
    const release = (value: StatsReportScope) => {
      void api<void>(
        `/api/stats/report-scopes/${value.id}`,
        { method: "DELETE", keepalive: true },
        csrfToken,
      ).catch(() => undefined);
    };
    void api<StatsReportScope>(
      "/api/stats/report-scopes",
      {
        method: "POST",
        body: JSON.stringify(dateRangeRequest(dateRange, preferences.timezone)),
        signal: controller.signal,
      },
      csrfToken,
    )
      .then((value) => {
        if (controller.signal.aborted) {
          release(value);
          return;
        }
        owned = value;
        preparing.current = false;
        setPrepared({ key: scopeKey, scope: value });
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          preparing.current = false;
          setPrepareError("New Coverage could not be prepared. Please try again.");
          renewScope("unavailable");
        }
      });
    return () => {
      controller.abort();
      preparing.current = false;
      if (owned) release(owned);
    };
  }, [scopeKey, scopeAttempt, csrfToken]);
  useEffect(() => {
    if (!tabActive) return;
    const update = () => {
      if (document.visibilityState !== "hidden") refreshAction.current();
    };
    const timer = window.setInterval(
      update,
      Math.max(10, Math.min(60, pollingIntervalSeconds)) * 1000,
    );
    document.addEventListener("visibilitychange", update);
    window.addEventListener("focus", update);
    window.addEventListener("online", update);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
      window.removeEventListener("focus", update);
      window.removeEventListener("online", update);
    };
  }, [tabActive, pollingIntervalSeconds]);
  const wasInactive = useRef(false);
  useLayoutEffect(() => {
    if (tabActive) {
      if (wasInactive.current) refreshAction.current();
      wasInactive.current = false;
    } else {
      wasInactive.current = true;
      setDialog(undefined);
      setCustomOpen(false);
      window.clearTimeout(renewalTimer.current);
      renewalTimer.current = undefined;
    }
  }, [tabActive]);
  useEffect(() => () => window.clearTimeout(renewalTimer.current), []);
  function renewScope(reason: "stale" | "unavailable" = "stale") {
    if (!tabActiveRef.current) return;
    if (renewalTimer.current != null) return;
    setDialog(undefined);
    setPrepared(undefined);
    if (renewals.current >= 3) {
      setPrepareError(
        reason === "stale"
          ? "Coverage is changing. Please retry the report in a moment."
          : "New Coverage could not be refreshed. Please try again.",
      );
      return;
    }
    const delay = 250 * 2 ** renewals.current++;
    renewalTimer.current = window.setTimeout(() => {
      renewalTimer.current = undefined;
      setScopeAttempt((value) => value + 1);
    }, delay);
  }
  refreshAction.current = () => {
    if (preparing.current || renewalTimer.current != null) return;
    if (!scope) {
      renewals.current = 0;
      setScopeAttempt((value) => value + 1);
    } else if (Date.parse(scope.expiresAt) <= Date.now() + 5000) {
      renewals.current = 0;
      renewScope();
    } else setRefresh((value) => value + 1);
  };
  const params = new URLSearchParams({
    generation: String(scope?.dataGeneration),
    groupBy: current.groupBy,
    sort: `${current.sort.field}:${current.sort.direction}`,
    page: String(current.page),
    pageSize: String(preferences.pageSize),
  });
  const path = scope ? `/api/stats/report-scopes/${scope.id}/new-coverage?${params}` : undefined;
  const result = useReportRead<NewCoverageBucketList>(scope, path, refresh, renewScope, scopeKey);
  const data = result.value;
  const displayedGroup = data?.groupBy ?? current.groupBy;
  const reportError = prepareError || result.error;
  const [visibleReportError, setVisibleReportError] = useState<string>();
  const reportRef = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const report = reportRef.current;
    if (!report) return;
    const alignFeedback = () => {
      const bounds = report.getBoundingClientRect();
      report.style.setProperty("--report-feedback-center", `${bounds.left + bounds.width / 2}px`);
      report.style.setProperty("--report-feedback-width", `${bounds.width}px`);
    };
    alignFeedback();
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(alignFeedback);
    observer?.observe(report);
    window.addEventListener("resize", alignFeedback);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", alignFeedback);
    };
  }, []);
  useEffect(() => {
    setVisibleReportError(undefined);
    if (!reportError) return;
    if (!data) {
      setVisibleReportError(reportError);
      return;
    }
    const timer = window.setTimeout(() => setVisibleReportError(reportError), 500);
    return () => window.clearTimeout(timer);
  }, [reportError, Boolean(data)]);
  const paging = usePagingScroll(data, result.pending, result.current);
  useEffect(() => {
    if (!data || !result.current || result.pending) return;
    renewals.current = 0;
    if (current.page > Math.max(1, data.pagination.totalPages)) {
      const page = Math.max(1, data.pagination.totalPages);
      onStateChange((old) => ({ ...old, newCoverage: { ...old.newCoverage, page } }));
    }
  }, [data, current.page, result.current, result.pending]);
  async function selectRange(next: DateRangePreference) {
    if (next === dateRange) return;
    if (dateRange === next) return;
    onDateRangeSelected(next);
    setDialog(undefined);
    setRangeError("");
    const sequence = ++rangeSaveSequence.current;
    const request = rangeSaveChain.current.then(() =>
      api<Preferences>(
        "/api/me/preferences",
        { method: "PATCH", body: JSON.stringify({ dateRange: next }) },
        csrfToken,
      ),
    );
    rangeSaveChain.current = request.then(
      () => undefined,
      () => undefined,
    );
    try {
      const saved = await request;
      recent.saved(saved.recentCustomDateRanges);
    } catch {
      if (sequence === rangeSaveSequence.current)
        setRangeError(
          "Your default date range was not saved. This report will continue using your selection.",
        );
    }
  }
  function updateReport(patch: Partial<StatsViewState["newCoverage"]>) {
    onStateChange((old) => ({ ...old, newCoverage: { ...currentRef.current, ...patch } }));
  }
  function selectGrouping(group: "month" | "year") {
    updateReport({ groupBy: group, page: 1, sort: DEFAULT_STATS_STATE.newCoverage.sort });
    setDialog(undefined);
  }
  useKeyboardShortcut("stats", reportRef, (shortcut) => {
    if (shortcut.kind !== "stats") return false;
    if (currentRef.current.groupBy !== shortcut.group) selectGrouping(shortcut.group);
    return true;
  });
  const active =
    STATS_REPORTS.find((report) => report.id === state.activeReport) ?? STATS_REPORTS[0];
  return (
    <main className="stats-page">
      <aside className="stats-sidebar" aria-label="Stats controls">
        <DateRangePicker
          range={dateRange}
          recent={recent.history}
          onSelect={(next) => void selectRange(next)}
          onCustom={() => setCustomOpen(true)}
        />
        <nav className="stats-report-nav" aria-label="Reports">
          <h2>Reports</h2>
          {STATS_REPORTS.map((report) => (
            <button
              key={report.id}
              type="button"
              aria-current={active.id === report.id ? "page" : undefined}
              onClick={() => onStateChange((old) => ({ ...old, activeReport: report.id }))}
            >
              {report.title}
            </button>
          ))}
        </nav>
      </aside>
      <section
        ref={reportRef}
        className="stats-report content-background"
        data-app-content
        tabIndex={-1}
        aria-labelledby="stats-report-heading"
      >
        <ContentBackground />
        <header className="stats-heading">
          <h1 id="stats-report-heading" ref={heading} tabIndex={-1}>
            {active.title}
          </h1>
        </header>
        {rangeError && (
          <div className="stats-message" role="status">
            {rangeError}
            <button className="secondary" onClick={() => setRangeError("")}>
              Dismiss
            </button>
          </div>
        )}
        <div className="stats-table-frame" data-paging-region>
          <fieldset className="stats-grouping">
            <legend>Show new visits by</legend>
            <div className="map-mode-controls stats-group-buttons">
              {(["month", "year"] as const).map((group) => (
                <button
                  key={group}
                  type="button"
                  aria-pressed={current.groupBy === group}
                  onClick={() => selectGrouping(group)}
                >
                  {group === "month" ? "Month" : "Year"}
                </button>
              ))}
            </div>
          </fieldset>
          <div
            ref={paging.ref}
            className="workout-table-wrap stats-table-wrap"
            aria-busy={(!scope && !prepareError) || result.pending}
          >
            <table className="workout-table stats-table">
              <thead>
                <tr>
                  <SortHeader
                    field="bucket"
                    label={displayedGroup === "month" ? "Month" : "Year"}
                    sort={current.sort}
                    onSort={(field) =>
                      updateReport({ sort: nextSort(current.sort, field), page: 1 })
                    }
                  />
                  <SortHeader
                    field="allTime"
                    label={METRIC_LABELS.all_time}
                    sort={current.sort}
                    onSort={(field) =>
                      updateReport({ sort: nextSort(current.sort, field), page: 1 })
                    }
                  />
                  <SortHeader
                    field="pastYear"
                    label={METRIC_LABELS.past_year}
                    help={"Includes first visits and return\nvisits after more than one year"}
                    sort={current.sort}
                    onSort={(field) =>
                      updateReport({ sort: nextSort(current.sort, field), page: 1 })
                    }
                  />
                </tr>
              </thead>
              <tbody>
                {/* Positional rows stay mounted while grouping or paging replaces their values. */}
                {data?.items.map((bucket, index) => (
                  <tr key={index}>
                    <td>{statsBucketLabel(bucket.bucketStart, displayedGroup)}</td>
                    {(["all_time", "past_year"] as const).map((metric) => {
                      const count = metric === "all_time" ? bucket.allTime : bucket.pastYear;
                      return (
                        <td key={metric} className={count ? "stats-count-cell" : undefined}>
                          {count ? (
                            <button
                              type="button"
                              className="stats-count-link"
                              disabled={!scope || result.pending || !result.current}
                              aria-label={`${METRIC_LABELS[metric]} in ${statsBucketLabel(bucket.bucketStart, displayedGroup)}: ${count.toLocaleString()}`}
                              onClick={(event) =>
                                setDialog({
                                  bucket: bucket.bucketStart,
                                  metric,
                                  count,
                                  trigger: event.currentTarget,
                                })
                              }
                            >
                              {count.toLocaleString()}
                            </button>
                          ) : (
                            <span className="stats-zero">0</span>
                          )}
                        </td>
                      );
                    })}
                  </tr>
                ))}
                {data && (
                  <PageSpacer pagination={data.pagination} items={data.items.length} columns={3} />
                )}
                {!data && (
                  <tr className="stats-state-row">
                    <td colSpan={3} role={prepareError || result.error ? undefined : "status"}>
                      {prepareError || result.error
                        ? "Report unavailable."
                        : "Loading new coverage..."}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          {data && (
            <PageNavigation
              value={data.pagination}
              label="New Coverage pages"
              keyboardDefault
              busy={result.pending || !result.current}
              unavailable={Boolean(result.error) && !result.current}
              onPage={(page) => {
                paging.capture();
                updateReport({ page });
              }}
            />
          )}
        </div>
        {visibleReportError && visibleReportError === reportError && (
          <div className="map-banner stats-report-feedback" role="alert">
            <span>{visibleReportError}</span>
            <button
              className="secondary"
              onClick={() => {
                if (prepareError) {
                  renewals.current = 0;
                  window.clearTimeout(renewalTimer.current);
                  renewalTimer.current = undefined;
                  setScopeAttempt((value) => value + 1);
                } else setRefresh((value) => value + 1);
              }}
            >
              Retry
            </button>
          </div>
        )}
      </section>
      <CustomDateRangeDialog
        open={customOpen}
        onOpenChange={setCustomOpen}
        range={dateRange}
        onApply={(next) => void selectRange(next)}
      />
      {dialog && scope && (
        <NewVisitsDialog
          key={`${scope.id}:${dialog.bucket}:${dialog.metric}`}
          scope={scope}
          group={displayedGroup}
          bucket={dialog.bucket}
          metric={dialog.metric}
          count={dialog.count}
          pageSize={preferences.pageSize}
          refresh={refresh}
          onUnavailable={renewScope}
          onClose={() => setDialog(undefined)}
          onShowOnMap={onShowOnMap}
          returnFocus={() => {
            if (dialog.trigger.isConnected) dialog.trigger.focus();
            else heading.current?.focus();
          }}
        />
      )}
    </main>
  );
}
