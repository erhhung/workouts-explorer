import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type CSSProperties,
  type FormEvent,
  type KeyboardEvent,
  Fragment,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  ApiError,
  api,
  type DataSync as DataSyncModel,
  type DataSyncSource,
  type IngestAccepted,
  type IngestCreate,
  type JobDetail,
  type JobDisplayStatus,
  type JobEventList,
  type JobFileList,
  type JobList,
  type JobLogList,
  type JobEvent,
  type JobLog,
  type JobProgress,
  type JobSummary,
  type JobStatus,
  type Notification,
  type Preferences,
  type SourceStatus,
} from "./api";
import { InfoTip, instantZone, ZoneBadge } from "./Tooltip";
import { completeDateRangeOnBlur } from "./dateRange";

const ACTIVE_STATUSES = new Set<JobStatus>(["queued", "running"]);
const RETRYABLE_STATUSES = new Set<JobStatus>(["failed", "cancelled", "partially_succeeded"]);
const RETRY_EXPLANATIONS = {
  already_current:
    "All unsuccessful routes from this historical run now have current coverage. No action is needed.",
  no_eligible_routes:
    "This run has no current failed or cancelled route targets eligible for retry. They may have been replaced or may no longer be ready for matching. Review the current runs or refresh the details.",
  not_latest: "A newer retry exists. Open the latest attempt in this chain.",
  retry_limit: "This run has reached its retry limit. Another retry cannot be created.",
  not_terminal: "This run is still active. Wait for it to finish before retrying.",
};

type RetryUnavailableReason = keyof typeof RETRY_EXPLANATIONS;

function retryBlockedReason(error: ApiError): RetryUnavailableReason | undefined {
  const prefix = "https://workouts-explorer.invalid/problems/coverage-retry/";
  if (error.problem.type?.startsWith(prefix)) {
    const reason = error.problem.type.slice(prefix.length).replaceAll("-", "_");
    if (Object.hasOwn(RETRY_EXPLANATIONS, reason)) return reason as RetryUnavailableReason;
  }
  return undefined;
}

function retryConflictMessage(error: ApiError) {
  const reason = retryBlockedReason(error);
  if (reason) return RETRY_EXPLANATIONS[reason];
  if (error.problem.type === "https://workouts-explorer.invalid/problems/coverage-retry/busy")
    return "Coverage work is being updated. Wait a moment, then try Retry run again.";
  return "The retry was not started. Review the refreshed run details for retry availability.";
}
const SOURCE_STATUS_LABELS: Record<SourceStatus, string> = {
  "checking-connection": "Checking connection",
  connected: "Connected",
  "connection-failed": "Connection failed",
};
const STATUS_LABELS: Record<JobDisplayStatus, string> = {
  queued: "Queued",
  running: "Running",
  succeeded: "Completed",
  partially_succeeded: "Partially completed",
  failed: "Failed",
  cancelled: "Canceled",
  superseded: "Superseded",
};
const PAGE_SIZE = 25;
const STATUS_OPTIONS = Object.entries(STATUS_LABELS) as Array<[JobDisplayStatus, string]>;
const OPERATION_OPTIONS = [
  ["manual_sync", "Manual sync"],
  ["automated_sync", "Automated sync"],
  ["workout_deletion", "Workout deletion"],
  ["coverage_update", "Coverage update"],
] as const;

function validDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return false;
  return (
    new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])))
      .toISOString()
      .slice(0, 10) === value
  );
}

function dateTime(
  value: string | undefined,
  preferences: Preferences,
  timeZone = preferences.timezone,
) {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unavailable";
  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone,
      hour12: preferences.clockFormat === "12h",
    }).format(date);
  } catch {
    return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(
      date,
    );
  }
}

function calendarDate(value: string | undefined) {
  if (!value) return "Never";
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isNaN(date.getTime())
    ? "Unavailable"
    : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(date);
}

function interval(seconds: number) {
  if (seconds === 86400) return "Once a day";
  if (seconds === 43200) return "Twice a day";
  const hours = seconds / 3600;
  return `Every ${new Intl.NumberFormat(undefined, { maximumFractionDigits: 6 }).format(hours)} ${hours === 1 ? "hour" : "hours"}`;
}

function historyOperation(job: JobSummary) {
  if (job.operation === "coverage_update") return "Coverage update";
  return job.operation === "workout_deletion"
    ? "Workout deletion"
    : job.trigger === "manual"
      ? "Manual sync"
      : "Automated sync";
}

function historyResults(job: JobSummary) {
  if (job.operation === "coverage_update")
    return `${job.routeStats?.processed ?? job.progress.current} of ${job.routeStats?.total ?? job.progress.total} routes`;
  if (job.operation === "workout_deletion")
    return `${job.progress.current} of ${job.progress.total} deleted`;
  return `${job.progress.filesSucceeded} files / ${job.progress.workoutsCreated} new`;
}

function CoverageProgress({ job }: { job: JobDetail }) {
  const stats = job.routeStats;
  if (!stats || stats.total === 0)
    return <p className="sync-progress sync-progress--unknown">Preparing routes</p>;
  return (
    <div className="sync-progress">
      <span>
        {stats.processed} of {stats.total} routes
      </span>
      <progress value={Math.min(stats.processed, stats.total)} max={stats.total} />
    </div>
  );
}

function CoverageResults({ job }: { job: JobDetail }) {
  const stats = job.routeStats;
  if (!stats) return null;
  return (
    <dl className="result-counts">
      <div>
        <dt>Routes Processed</dt>
        <dd>{stats.processed}</dd>
      </div>
      <div>
        <dt>Routes Succeeded</dt>
        <dd>{stats.succeeded}</dd>
      </div>
      <div>
        <dt>Routes Failed</dt>
        <dd>{stats.failed}</dd>
      </div>
      <div>
        <dt>Routes Canceled</dt>
        <dd>{stats.cancelled}</dd>
      </div>
      <div>
        <dt>Routes Superseded</dt>
        <dd>{stats.superseded}</dd>
      </div>
    </dl>
  );
}

function NextRun({ value, preferences }: { value?: string; preferences: Preferences }) {
  if (!value) return <>Never</>;
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return <>Unavailable</>;
  const zone = instantZone(preferences.timezone, instant);
  if (zone.label === "TZ unavailable") {
    const title = `${preferences.timezone} unavailable; displayed in UTC (UTC+0:00)`;
    return (
      <span className="next-run-value">
        <span>{dateTime(value, preferences, "UTC")}</span>
        <ZoneBadge label="UTC" title={title} />
      </span>
    );
  }
  return (
    <span className="next-run-value">
      <span>{dateTime(value, preferences)}</span>
      <ZoneBadge label={zone.label} title={zone.title} />
    </span>
  );
}

function sourceType(value: string) {
  if (value === "health-auto-export-icloud") return "Health Auto Export — iCloud Drive";
  return value === "health-auto-export-local" ? "Health Auto Export — Server Path" : value;
}

function StatusBadge({
  status,
  cancelRequested = false,
  displayStatus,
  style,
}: {
  status: JobStatus;
  cancelRequested?: boolean;
  displayStatus?: JobDisplayStatus;
  style?: CSSProperties;
}) {
  const cancellationRequested = cancelRequested && ACTIVE_STATUSES.has(status);
  return (
    <span
      className={`sync-status sync-status--${cancellationRequested ? "cancellation" : (displayStatus ?? status)}`}
      style={style}
    >
      {cancellationRequested ? "Cancellation requested" : STATUS_LABELS[displayStatus ?? status]}
    </span>
  );
}

function StatusText({ status }: { status: JobDisplayStatus }) {
  return (
    <span className={`sync-status-text sync-status-text--${status}`}>{STATUS_LABELS[status]}</span>
  );
}

function Progress({
  progress,
  status,
  style,
}: {
  progress: JobProgress;
  status: JobStatus;
  style?: CSSProperties;
}) {
  if (progress.total === 0 && ACTIVE_STATUSES.has(status))
    return (
      <p className="sync-progress sync-progress--unknown" style={style}>
        Discovering files; total not yet known
      </p>
    );
  if (progress.total === 0) return null;
  return (
    <div className="sync-progress" style={style}>
      <span>
        {progress.current} of {progress.total} files
      </span>
      <progress value={Math.min(progress.current, progress.total)} max={progress.total} />
    </div>
  );
}

function Results({
  progress,
  results,
  status,
}: {
  progress: JobProgress;
  results?: JobDetail["results"];
  status: JobStatus;
}) {
  const rejectedHelpId = useId();
  const values = results ?? progress;
  const noData =
    (values.filesSucceeded ?? 0) === 0 &&
    (values.filesFailed ?? 0) === 0 &&
    (values.workoutsCreated ?? 0) === 0 &&
    (values.workoutsUpdated ?? 0) === 0 &&
    (values.workoutsUnchanged ?? 0) === 0 &&
    (values.workoutsRejected ?? 0) === 0;
  if (noData)
    return (
      <p className="sync-no-data">
        {status === "succeeded" ? "No new data." : "No files were processed."}
      </p>
    );
  return (
    <dl className="result-counts">
      <div>
        <dt>Files Processed</dt>
        <dd>{(values.filesSucceeded ?? 0) + (values.filesFailed ?? 0)}</dd>
      </div>
      <div>
        <dt>Workouts Imported</dt>
        <dd className="sync-workout-count">
          <span>{values.workoutsCreated ?? 0}</span>
          {values.workoutsCreatedWithRoute != null && (
            <>
              {" "}
              <span className="sync-route-count">({values.workoutsCreatedWithRoute} routes)</span>
            </>
          )}
        </dd>
      </div>
      <div>
        <dt>Workouts Unchanged</dt>
        <dd>{values.workoutsUnchanged ?? 0}</dd>
      </div>
      <div>
        <dt>Files Failed</dt>
        <dd>{values.filesFailed ?? 0}</dd>
      </div>
      <div>
        <dt>Workouts Updated</dt>
        <dd className="sync-workout-count">
          <span>{values.workoutsUpdated ?? 0}</span>
          {values.workoutsUpdatedWithRoute != null && (
            <>
              {" "}
              <span className="sync-route-count">({values.workoutsUpdatedWithRoute} routes)</span>
            </>
          )}
        </dd>
      </div>
      <div>
        <dt>Workouts Rejected</dt>
        <dd aria-describedby={rejectedHelpId}>{values.workoutsRejected ?? 0}</dd>
        <span id={rejectedHelpId} className="visually-hidden">
          Workout records skipped while their source file continued processing.
        </span>
      </div>
    </dl>
  );
}

function ordinal(value: number) {
  const words = [
    "",
    "First",
    "Second",
    "Third",
    "Fourth",
    "Fifth",
    "Sixth",
    "Seventh",
    "Eighth",
    "Ninth",
    "Tenth",
  ];
  if (words[value]) return words[value];
  const remainder = value % 100;
  const suffix =
    remainder >= 11 && remainder <= 13
      ? "th"
      : value % 10 === 1
        ? "st"
        : value % 10 === 2
          ? "nd"
          : value % 10 === 3
            ? "rd"
            : "th";
  return `${value}${suffix}`;
}

function HistoryFilter({
  label,
  value,
  allLabel,
  options,
  onChange,
}: {
  label: string;
  value: string;
  allLabel: string;
  options: ReadonlyArray<readonly [string, string]>;
  onChange: (value: string) => void;
}) {
  const selectedLabel = options.find(([option]) => option === value)?.[1] ?? allLabel;
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger
        className="range-trigger history-filter-trigger"
        aria-label={`Filter by ${label.toLowerCase()}`}
      >
        <span>{label}</span>
        <strong>{selectedLabel}</strong>
        <span aria-hidden="true">v</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content history-filter-menu"
          align="end"
          sideOffset={8}
        >
          <DropdownMenu.RadioGroup value={value} onValueChange={onChange}>
            <DropdownMenu.RadioItem value="">
              {allLabel}
              <DropdownMenu.ItemIndicator aria-hidden="true">&#10003;</DropdownMenu.ItemIndicator>
            </DropdownMenu.RadioItem>
            {options.map(([option, optionLabel]) => (
              <DropdownMenu.RadioItem key={option} value={option}>
                {optionLabel}
                <DropdownMenu.ItemIndicator aria-hidden="true">&#10003;</DropdownMenu.ItemIndicator>
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function QueryError({ copy, retry }: { copy: string; retry: () => void }) {
  return (
    <div className="sync-query-error" role="alert">
      <span>{copy}</span>
      <button type="button" className="secondary" onClick={retry}>
        Try again
      </button>
    </div>
  );
}

type ArtifactKind = "files" | "events" | "logs";

function ArtifactDisclosure({
  jobId,
  kind,
  preferences,
}: {
  jobId: string;
  kind: ArtifactKind;
  preferences: Preferences;
}) {
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState(1);
  const [retainedPage, setRetainedPage] = useState<{
    page: number;
    data: JobFileList | JobEventList | JobLogList;
  }>();
  const resultsRef = useRef<HTMLDivElement>(null);
  const paginationRef = useRef<HTMLElement>(null);
  const fullPageHeight = useRef(0);
  const pendingScroll = useRef<
    { page: number; x: number; y: number; paginationTop?: number; restore: boolean } | undefined
  >(undefined);
  const [resultsMinHeight, setResultsMinHeight] = useState<number>();
  const label = kind[0].toUpperCase() + kind.slice(1);
  const query = useQuery({
    queryKey: ["job-artifacts", jobId, kind, page],
    queryFn: ({ signal }) =>
      api<JobFileList | JobEventList | JobLogList>(
        `/api/jobs/${encodeURIComponent(jobId)}/${kind}?page=${page}&pageSize=${PAGE_SIZE}`,
        { signal },
      ),
    enabled: open,
    placeholderData: (previousData, previousQuery) => {
      const key = previousQuery?.queryKey;
      return key?.[0] === "job-artifacts" && key[1] === jobId && key[2] === kind && key[3] !== page
        ? previousData
        : undefined;
    },
  });
  const data = query.data ?? (retainedPage?.page === page ? retainedPage.data : undefined);

  useLayoutEffect(
    () => () => {
      pendingScroll.current = undefined;
    },
    [],
  );

  useLayoutEffect(() => {
    if (!open) return;
    const stopRestoring = () => {
      if (pendingScroll.current) pendingScroll.current.restore = false;
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        (target.isContentEditable || target.closest("input, textarea, select"))
      )
        return;
      if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) {
        if (event.key === " " && target instanceof HTMLElement && target.closest("button")) return;
        stopRestoring();
      }
    };
    window.addEventListener("wheel", stopRestoring, { passive: true });
    window.addEventListener("touchmove", stopRestoring, { passive: true });
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("wheel", stopRestoring);
      window.removeEventListener("touchmove", stopRestoring);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  function reserveHeight() {
    const height = resultsRef.current?.getBoundingClientRect().height;
    if (height) setResultsMinHeight((previous) => Math.max(previous ?? 0, height));
  }

  function changePage(nextPage: number) {
    const pagination = paginationRef.current?.getBoundingClientRect();
    pendingScroll.current = {
      page: nextPage,
      x: window.scrollX,
      y: window.scrollY,
      paginationTop: pagination?.height ? pagination.top : undefined,
      restore: true,
    };
    reserveHeight();
    if (data) setRetainedPage({ page: nextPage, data });
    setPage(nextPage);
  }

  function close() {
    pendingScroll.current = undefined;
    fullPageHeight.current = 0;
    setResultsMinHeight(undefined);
    setRetainedPage(undefined);
    setOpen(false);
  }

  useLayoutEffect(() => {
    if (!open || query.isPlaceholderData || !query.isSuccess || query.data.pagination.page !== page)
      return;
    const loaded = query.data;
    const finalPartial =
      loaded.pagination.totalPages === 0 ||
      (loaded.pagination.page === loaded.pagination.totalPages &&
        loaded.items.length < loaded.pagination.pageSize);
    // Cached full pages need immediate anchoring; shrinking waits for fresh data.
    if (finalPartial && query.isFetching) return;
    if (!finalPartial) {
      fullPageHeight.current = Math.max(
        fullPageHeight.current,
        resultsRef.current?.getBoundingClientRect().height ?? 0,
      );
    }
    setRetainedPage(undefined);
    const pending = pendingScroll.current;
    if (!pending || pending.page !== page) return;
    setResultsMinHeight(finalPartial ? undefined : fullPageHeight.current || undefined);
    const frame = window.requestAnimationFrame(() => {
      if (pendingScroll.current !== pending) return;
      if (!query.isFetching) pendingScroll.current = undefined;
      if (!pending.restore) return;
      const pagination = paginationRef.current?.getBoundingClientRect();
      const y =
        pending.paginationTop !== undefined && pagination?.height
          ? window.scrollY + pagination.top - pending.paginationTop
          : pending.y;
      window.scrollTo(pending.x, y);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [open, page, query.data, query.isFetching, query.isPlaceholderData, query.isSuccess]);

  function escape(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") close();
  }
  return (
    <div className="artifact-disclosure" onKeyDown={escape}>
      <button
        type="button"
        className="artifact-trigger"
        aria-expanded={open}
        onClick={() => (open ? close() : setOpen(true))}
      >
        {label}
        <span aria-hidden="true">{open ? "-" : "+"}</span>
      </button>
      {open && (
        <div className="artifact-panel" aria-busy={query.isFetching}>
          <div
            className="artifact-results"
            ref={resultsRef}
            style={resultsMinHeight ? { minHeight: resultsMinHeight } : undefined}
          >
            {query.isPending && !data && <p role="status">Loading {kind}...</p>}
            {query.isError && (
              <QueryError
                copy={`${label} are unavailable.`}
                retry={() => {
                  reserveHeight();
                  void query.refetch();
                }}
              />
            )}
            {data && data.items.length === 0 && <p>No {kind} recorded.</p>}
            {data && kind === "files" && (
              <ul className="artifact-list">
                {(data as JobFileList).items.map((file) => (
                  <li key={file.id}>
                    <strong>{file.basename}</strong>
                    <span>
                      {file.source.displayName} / {file.state}
                    </span>
                    <span>{file.sizeBytes.toLocaleString()} bytes</span>
                    {file.failureSummary && (
                      <span className="failure-copy">{file.failureSummary}</span>
                    )}
                  </li>
                ))}
              </ul>
            )}
            {data && kind !== "files" && (
              <ul className="artifact-list">
                {(data.items as Array<JobEvent | JobLog>).map((item) => (
                  <li key={item.id}>
                    <strong>
                      {item.severity}: {item.message}
                    </strong>
                    <span>
                      {dateTime(item.createdAt, preferences)} / {item.code}
                    </span>
                    {Object.keys(item.fields).length > 0 && (
                      <dl className="safe-fields">
                        {Object.entries(item.fields).map(([key, value]) => (
                          <div key={key}>
                            <dt>{key}</dt>
                            <dd>{value}</dd>
                          </div>
                        ))}
                      </dl>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
          {data && data.pagination.totalPages > 1 && (
            <nav className="pagination" aria-label={`${label} pages`} ref={paginationRef}>
              <button
                type="button"
                className="secondary"
                disabled={page <= 1 || query.isFetching || data.pagination.page !== page}
                onClick={() => changePage(page - 1)}
              >
                Previous
              </button>
              <span>
                Page {data.pagination.page} of {data.pagination.totalPages}
              </span>
              <button
                type="button"
                className="secondary"
                disabled={
                  page >= data.pagination.totalPages ||
                  query.isFetching ||
                  data.pagination.page !== page
                }
                onClick={() => changePage(page + 1)}
              >
                Next
              </button>
            </nav>
          )}
        </div>
      )}
    </div>
  );
}

function JobDetailCard({
  job,
  preferences,
  busy,
  onCancel,
  onRetry,
  onSelectJob,
  blockedRetryReason,
}: {
  job: JobDetail;
  preferences: Preferences;
  busy: "cancel" | "retry" | null;
  onCancel: () => void;
  onRetry: () => void;
  onSelectJob: (jobId: string) => void;
  blockedRetryReason?: RetryUnavailableReason;
}) {
  const deletion = job.operation === "workout_deletion";
  const coverage = job.operation === "coverage_update";
  const retryEligibility = coverage ? job.retryEligibility : undefined;
  const superseded = coverage && job.displayStatus === "superseded";
  const retryBlocked =
    superseded ||
    (RETRYABLE_STATUSES.has(job.status) &&
      (retryEligibility?.canRetry === false || blockedRetryReason !== undefined));
  const retryReason =
    blockedRetryReason ?? (superseded ? "already_current" : retryEligibility?.reason);
  const alreadyCurrent = retryReason === "already_current";
  const retryExplanationId = useId();
  const waitingAfterStart = coverage && job.status === "queued" && Boolean(job.startedAt);
  return (
    <article className="sync-card job-detail-card">
      <div className="sync-card-heading">
        <div>
          <p className="card-kicker">Job {job.id.slice(0, 8)}</p>
          <h2>{deletion ? "Workout deletion" : coverage ? "Coverage update" : "Run detail"}</h2>
        </div>
        <StatusBadge
          status={job.status}
          displayStatus={job.displayStatus}
          cancelRequested={job.cancelRequested}
        />
      </div>
      <dl className="job-metadata">
        <div>
          <dt>{deletion || coverage ? "Operation" : "Trigger"}</dt>
          <dd>
            {deletion
              ? "Workout deletion"
              : coverage
                ? "Coverage update"
                : job.trigger === "manual"
                  ? "Manual"
                  : "Scheduled"}
          </dd>
        </div>
        <div>
          <dt>{deletion ? "Targets" : coverage ? "Routes" : "Sources"}</dt>
          <dd>
            {deletion
              ? job.progress.total
              : coverage
                ? (job.routeStats?.total ?? job.children.length)
                : job.children.length || (job.source ? 1 : 0)}
          </dd>
        </div>
        <div>
          <dt>Queued</dt>
          <dd>{dateTime(job.createdAt, preferences)}</dd>
        </div>
        {coverage && job.coverage && (
          <div>
            <dt>Regions</dt>
            <dd>
              {[
                ...new Set(
                  job.coverage.regionIds?.length ? job.coverage.regionIds : [job.coverage.regionId],
                ),
              ].join(", ")}
            </dd>
          </div>
        )}
        <div>
          <dt>{coverage ? "First started" : "Started"}</dt>
          <dd>{dateTime(job.startedAt, preferences)}</dd>
        </div>
        {waitingAfterStart && (
          <div>
            <dt>Waiting since</dt>
            <dd>{dateTime(job.updatedAt, preferences)}</dd>
          </div>
        )}
        <div>
          <dt>Finished</dt>
          <dd>{dateTime(job.terminalAt, preferences)}</dd>
        </div>
        {job.retryRootJobId && job.retryOrdinal != null && job.retryOrdinal > 1 ? (
          <div>
            <dt>
              Retry of job{" "}
              <a
                href={`/data-sync/jobs/${job.retryRootJobId}`}
                onClick={(event) => {
                  event.preventDefault();
                  onSelectJob(job.retryRootJobId!);
                }}
              >
                {job.retryRootJobId.slice(0, 8)}
              </a>
            </dt>
            <dd className="retry-ordinal">
              <span>{ordinal(job.retryOrdinal)}</span>
              {job.retryOrdinal > 1 && job.retryOfJobId && (
                <small className="retry-previous">
                  |&nbsp; view{" "}
                  <a
                    href={`/data-sync/jobs/${job.retryOfJobId}`}
                    onClick={(event) => {
                      event.preventDefault();
                      onSelectJob(job.retryOfJobId!);
                    }}
                  >
                    {ordinal(job.retryOrdinal - 1).toLowerCase()}
                  </a>
                </small>
              )}
            </dd>
          </div>
        ) : (
          job.latestRetryJobId && (
            <div>
              <dt>
                Retry by job{" "}
                <a
                  href={`/data-sync/jobs/${job.latestRetryJobId}`}
                  onClick={(event) => {
                    event.preventDefault();
                    onSelectJob(job.latestRetryJobId!);
                  }}
                >
                  {job.latestRetryJobId.slice(0, 8)}
                </a>
              </dt>
              <dd className="visually-hidden">Latest retry in chain</dd>
            </div>
          )
        )}
      </dl>
      {coverage ? (
        <CoverageProgress job={job} />
      ) : (
        !deletion && <Progress progress={job.progress} status={job.status} />
      )}
      {coverage ? (
        <CoverageResults job={job} />
      ) : (
        !deletion &&
        (!ACTIVE_STATUSES.has(job.status) || job.progress.current > 0) && (
          <Results progress={job.progress} results={job.results} status={job.status} />
        )
      )}
      {job.failureSummary && (
        <p className="failure-copy">
          <strong>{superseded ? "Historical run issue:" : "Run issue:"}</strong>{" "}
          {job.failureSummary}
        </p>
      )}
      {retryBlocked && (
        <div
          className="sync-feedback"
          role="note"
          aria-label="Retry availability"
          id={retryExplanationId}
        >
          <strong>{alreadyCurrent ? "No retry needed. " : "Retry unavailable. "}</strong>
          {RETRY_EXPLANATIONS[retryReason as keyof typeof RETRY_EXPLANATIONS] ??
            "Review the refreshed run details for retry availability."}
        </div>
      )}
      {retryEligibility && retryEligibility.currentCoverageJobIds.length > 0 && (
        <p className="sync-no-data">
          Newer runs providing current Coverage (not retry ancestry):{" "}
          {retryEligibility.currentCoverageJobIds.map((id, index) => (
            <Fragment key={id}>
              {index > 0 ? ", " : ""}
              <a
                href={`/data-sync/jobs/${id}`}
                aria-label={`View current Coverage run ${id.slice(0, 8)}`}
                onClick={(event) => {
                  event.preventDefault();
                  onSelectJob(id);
                }}
              >
                {id.slice(0, 8)}
              </a>
            </Fragment>
          ))}
        </p>
      )}
      {job.children.length > 0 && (
        <section className="source-runs" aria-labelledby="source-runs-heading">
          <h3 id="source-runs-heading">{coverage ? "Route runs" : "Source runs"}</h3>
          {job.children.map((child) => (
            <article
              key={child.id}
              className={`source-run${coverage ? " coverage-route-run" : ""}`}
            >
              {coverage ? (
                <div className="coverage-route-run-heading">
                  <div className="coverage-route-title">
                    <strong>{child.coverageRoute?.workoutType ?? "Workout unavailable"}</strong>{" "}
                    <span>
                      (
                      {child.coverageRoute?.localStartDate
                        ? calendarDate(child.coverageRoute.localStartDate)
                        : dateTime(child.coverageRoute?.startedAt, preferences)}
                      )
                    </span>
                  </div>
                  {child.coverageRoute && (
                    <table className="coverage-generation-list">
                      <caption className="visually-hidden">OSM generations</caption>
                      <tbody>
                        {child.coverageRoute.targetGenerations.map((generation) => (
                          <tr key={`${generation.regionId}-${generation.generation}`}>
                            <td>{generation.regionId}</td>
                            <td className={generation.latest ? "is-latest" : undefined}>
                              {generation.generation}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                  <StatusBadge status={child.status} cancelRequested={child.cancelRequested} />
                </div>
              ) : (
                <div className="source-run-heading">
                  <div>
                    <strong>{child.source?.displayName ?? "Source unavailable"}</strong>{" "}
                    <span>
                      ({child.source ? sourceType(child.source.sourceType) : "Type unavailable"})
                    </span>
                  </div>
                  <StatusBadge
                    status={child.status}
                    cancelRequested={child.cancelRequested}
                    style={{ position: "relative", top: ".2rem" }}
                  />
                </div>
              )}
              {coverage ? (
                <p className="sync-no-data">
                  {child.coverageRoute?.resultOutcome?.replace("_", " ") ??
                    (ACTIVE_STATUSES.has(child.status) ? "Processing" : "No result")}
                  {child.coverageRoute?.durationMilliseconds != null
                    ? ` / ${child.coverageRoute.durationMilliseconds.toLocaleString()} ms`
                    : ""}
                </p>
              ) : (
                <Progress
                  progress={child.progress}
                  status={child.status}
                  style={{ margin: ".7rem 0 -.3rem 0" }}
                />
              )}
              {!coverage && (!ACTIVE_STATUSES.has(child.status) || child.progress.current > 0) && (
                <Results progress={child.progress} results={child.results} status={child.status} />
              )}
              {child.failureSummary && <p className="failure-copy">{child.failureSummary}</p>}
            </article>
          ))}
        </section>
      )}
      {!deletion && (ACTIVE_STATUSES.has(job.status) || RETRYABLE_STATUSES.has(job.status)) && (
        <div className="job-actions">
          {ACTIVE_STATUSES.has(job.status) && (
            <button
              type="button"
              className="secondary danger-button"
              disabled={busy !== null || job.cancelRequested}
              onClick={onCancel}
            >
              {busy === "cancel" || job.cancelRequested ? "Cancelling..." : "Cancel run"}
            </button>
          )}
          {RETRYABLE_STATUSES.has(job.status) &&
            !job.latestRetryJobId &&
            (!coverage || !job.parentJobId) && (
              <button
                type="button"
                className="primary"
                disabled={busy !== null || retryBlocked}
                aria-describedby={retryBlocked ? retryExplanationId : undefined}
                onClick={onRetry}
              >
                {busy === "retry" ? "Retrying..." : "Retry run"}
              </button>
            )}
        </div>
      )}
      {deletion && RETRYABLE_STATUSES.has(job.status) && !job.latestRetryJobId && (
        <div className="job-actions">
          <button type="button" className="primary" disabled={busy !== null} onClick={onRetry}>
            {busy === "retry" ? "Retrying..." : "Retry deletion"}
          </button>
        </div>
      )}
      {!deletion && (
        <div className="artifact-group" aria-label="Run records">
          {!coverage && (
            <ArtifactDisclosure
              key={`${job.id}-files`}
              jobId={job.id}
              kind="files"
              preferences={preferences}
            />
          )}
          <ArtifactDisclosure
            key={`${job.id}-events`}
            jobId={job.id}
            kind="events"
            preferences={preferences}
          />
          <ArtifactDisclosure
            key={`${job.id}-logs`}
            jobId={job.id}
            kind="logs"
            preferences={preferences}
          />
        </div>
      )}
    </article>
  );
}

function NotificationBanner({
  notification,
  source,
  preferences,
  pending,
  onDismiss,
  onSelectJob,
}: {
  notification: Notification;
  source?: DataSyncSource;
  preferences: Preferences;
  pending: boolean;
  onDismiss: () => void;
  onSelectJob: (jobId: string) => void;
}) {
  const [showSourceDetail, setShowSourceDetail] = useState(false);
  const sourceDetailId = useId();
  const sourceFailure =
    notification.subjectType === "source" && notification.type === "source-connection-failed";
  return (
    <article className={`sync-notification sync-notification--${notification.severity}`}>
      <div>
        <strong>{notification.title}</strong>
        <p>{notification.message}</p>
        {notification.state === "remind" && notification.remindAt && (
          <small>Reminded until a later review.</small>
        )}
        {sourceFailure && showSourceDetail && (
          <div id={sourceDetailId}>
            {source ? (
              <>
                <p>
                  <strong>{source.displayName}</strong>: {SOURCE_STATUS_LABELS[source.status]}
                </p>
                {source.statusSummary && <p>{source.statusSummary}</p>}
                {source.statusCode && (
                  <p>
                    Code: <code>{source.statusCode}</code>
                  </p>
                )}
                <p>Last checked: {dateTime(source.checkedAt, preferences)}</p>
                {source.type === "health-auto-export-icloud" &&
                  source.statusCode === "source-auth-expired" && (
                    <p>
                      Reconnect with Rclone, then update this source&apos;s cookies and trustToken
                      through the API. Automatic sync can resume after a successful connection check
                      if enabled.
                    </p>
                  )}
              </>
            ) : (
              <p>This source is no longer available.</p>
            )}
          </div>
        )}
      </div>
      <div className="notification-actions">
        {sourceFailure ? (
          <button
            type="button"
            className="detail-link"
            aria-expanded={showSourceDetail}
            aria-controls={sourceDetailId}
            onClick={() => setShowSourceDetail((shown) => !shown)}
          >
            {showSourceDetail ? "Hide detail" : "View detail"}
          </button>
        ) : notification.subjectType === "job" && notification.jobId ? (
          <button
            type="button"
            className="detail-link"
            onClick={() => onSelectJob(notification.jobId!)}
          >
            View detail
          </button>
        ) : null}
        {(notification.state === "unresolved" || notification.state === "remind") && (
          <button type="button" className="secondary" disabled={pending} onClick={onDismiss}>
            {pending ? "Dismissing..." : "Dismiss"}
          </button>
        )}
      </div>
    </article>
  );
}

export function DataSync({
  csrfToken,
  preferences,
  pollingIntervalSeconds,
  selectedJobId,
  navigate,
}: {
  csrfToken: string;
  preferences: Preferences;
  pollingIntervalSeconds: number;
  selectedJobId?: string;
  navigate: (to: string, replace?: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [historyPage, setHistoryPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState("");
  const [operationFilter, setOperationFilter] = useState("");
  const [selectedSources, setSelectedSources] = useState<Set<string>>(new Set());
  const selectionInitialized = useRef(false);
  const [rangeMode, setRangeMode] = useState<"incremental" | "bounded">("incremental");
  const [formError, setFormError] = useState("");
  const [formErrorArea, setFormErrorArea] = useState<"sources" | "dates" | "request" | null>(null);
  const formErrorRef = useRef<HTMLDivElement>(null);
  const detailRef = useRef<HTMLElement>(null);
  const historyResultsRef = useRef<HTMLDivElement>(null);
  const pendingHistoryPageScroll = useRef<{ x: number; y: number } | undefined>(undefined);
  const [historyResultsMinHeight, setHistoryResultsMinHeight] = useState<number>();
  const [announcement, setAnnouncement] = useState("");
  const [blockedRetry, setBlockedRetry] = useState<{
    jobId: string;
    reason: RetryUnavailableReason;
    dataUpdateCount: number;
  } | null>(null);
  const previousStatus = useRef<{ id: string; status: JobStatus } | undefined>(undefined);
  const formErrorId = useId();

  const snapshot = useQuery({
    queryKey: ["data-sync"],
    queryFn: ({ signal }) => api<DataSyncModel>("/api/data-sync", { signal }),
    refetchInterval: pollingIntervalSeconds * 1000,
  });
  useEffect(() => {
    if (!snapshot.data || selectionInitialized.current) return;
    selectionInitialized.current = true;
    setSelectedSources(
      new Set(
        snapshot.data.sources
          .filter((source) => source.status === "connected" && source.autoSyncEnabled)
          .map((source) => source.id),
      ),
    );
  }, [snapshot.data]);

  const effectiveJobId =
    selectedJobId ?? snapshot.data?.activeJob?.id ?? snapshot.data?.latestJob?.id;
  const detail = useQuery({
    queryKey: ["job", effectiveJobId],
    queryFn: ({ signal }) =>
      api<JobDetail>(`/api/jobs/${encodeURIComponent(effectiveJobId!)}`, { signal }),
    enabled: Boolean(effectiveJobId),
    retry: (failureCount, error) =>
      !(error instanceof ApiError && error.status === 404) && failureCount < 3,
    refetchOnWindowFocus: (query) =>
      !(query.state.error instanceof ApiError && query.state.error.status === 404),
    refetchOnReconnect: (query) =>
      !(query.state.error instanceof ApiError && query.state.error.status === 404),
    refetchInterval: (query) =>
      !(query.state.error instanceof ApiError && query.state.error.status === 404) &&
      query.state.data &&
      (ACTIVE_STATUSES.has(query.state.data.status) ||
        (query.state.data.operation === "coverage_update" &&
          !query.state.data.parentJobId &&
          RETRYABLE_STATUSES.has(query.state.data.status)))
        ? pollingIntervalSeconds * 1000
        : false,
  });
  const detailUpdateCount =
    queryClient.getQueryState(["job", effectiveJobId])?.dataUpdateCount ?? 0;
  useEffect(() => {
    if (
      blockedRetry &&
      blockedRetry.jobId === effectiveJobId &&
      !detail.error &&
      detail.data?.retryEligibility &&
      detailUpdateCount > blockedRetry.dataUpdateCount
    ) {
      setBlockedRetry(null);
      if (detail.data?.retryEligibility?.canRetry)
        setAnnouncement((current) =>
          current === RETRY_EXPLANATIONS[blockedRetry.reason]
            ? "Run details refreshed. Retry is now available."
            : current,
        );
    }
  }, [
    blockedRetry,
    effectiveJobId,
    detail.error,
    detailUpdateCount,
    detail.data?.retryEligibility?.canRetry,
  ]);
  const detailNotFound = detail.error instanceof ApiError && detail.error.status === 404;
  useEffect(() => {
    if (selectedJobId && detailNotFound) navigate("/data-sync", true);
  }, [selectedJobId, detailNotFound, navigate]);
  const snapshotJobSignature = `${snapshot.data?.activeJob?.id ?? ""}:${snapshot.data?.activeJob?.status ?? ""}:${snapshot.data?.activeJob?.updatedAt ?? ""}:${snapshot.data?.latestJob?.id ?? ""}:${snapshot.data?.latestJob?.status ?? ""}:${snapshot.data?.latestJob?.updatedAt ?? ""}`;
  const previousSnapshotJobs = useRef<string | null>(null);
  useEffect(() => {
    if (!snapshot.data || previousSnapshotJobs.current === snapshotJobSignature) return;
    const hadSnapshot = previousSnapshotJobs.current !== null;
    previousSnapshotJobs.current = snapshotJobSignature;
    if (hadSnapshot) void queryClient.invalidateQueries({ queryKey: ["jobs"] });
  }, [queryClient, snapshot.data, snapshotJobSignature]);
  const historyParams = new URLSearchParams({
    page: String(historyPage),
    pageSize: String(PAGE_SIZE),
  });
  if (operationFilter) historyParams.set("operation", operationFilter);
  if (statusFilter) historyParams.set("status", statusFilter);
  const historyQueryKey = ["jobs", historyPage, operationFilter, statusFilter] as const;
  const jobs = useQuery({
    queryKey: historyQueryKey,
    queryFn: ({ signal }) => api<JobList>(`/api/jobs?${historyParams}`, { signal }),
    // Terminal Coverage projections and filtered totals can change independently.
    refetchInterval: pollingIntervalSeconds * 1000,
    placeholderData: (previousData, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      const sameFilters =
        previousKey?.length === historyQueryKey.length &&
        previousKey[0] === "jobs" &&
        previousKey[2] === operationFilter &&
        previousKey[3] === statusFilter;
      return sameFilters && previousKey?.[1] !== historyPage ? previousData : undefined;
    },
  });

  function changeHistoryPage(nextPage: number) {
    pendingHistoryPageScroll.current = { x: window.scrollX, y: window.scrollY };
    const currentHeight = historyResultsRef.current?.getBoundingClientRect().height;
    if (currentHeight) setHistoryResultsMinHeight((height) => Math.max(height ?? 0, currentHeight));
    setHistoryPage(nextPage);
  }

  function resetHistoryPage() {
    pendingHistoryPageScroll.current = undefined;
    setHistoryResultsMinHeight(undefined);
    setHistoryPage(1);
  }

  useEffect(() => {
    if (
      !pendingHistoryPageScroll.current ||
      jobs.isFetching ||
      jobs.isPlaceholderData ||
      !jobs.isSuccess
    )
      return;
    const position = pendingHistoryPageScroll.current;
    pendingHistoryPageScroll.current = undefined;
    const frame = window.requestAnimationFrame(() => window.scrollTo(position.x, position.y));
    return () => window.cancelAnimationFrame(frame);
  }, [jobs.dataUpdatedAt, jobs.isFetching, jobs.isPlaceholderData, jobs.isSuccess]);

  function selectJob(jobId: string, command?: string) {
    navigate(`/data-sync/jobs/${jobId}`);
    if (command) setAnnouncement(command);
    void queryClient.invalidateQueries({ queryKey: ["data-sync"] });
    void queryClient.invalidateQueries({ queryKey: ["jobs"] });
    setTimeout(() => detailRef.current?.focus());
  }

  async function refreshOperations(jobId?: string) {
    const refreshes = [
      queryClient.invalidateQueries({ queryKey: ["data-sync"] }),
      queryClient.invalidateQueries({ queryKey: ["jobs"] }),
    ];
    if (jobId) refreshes.push(queryClient.invalidateQueries({ queryKey: ["job", jobId] }));
    await Promise.all(refreshes);
  }

  const ingest = useMutation({
    mutationFn: (body: IngestCreate) =>
      api<IngestAccepted>("/api/ingest", { method: "POST", body: JSON.stringify(body) }, csrfToken),
    onMutate: () => setAnnouncement("Starting sync..."),
    onSuccess: async (accepted) => {
      selectJob(
        accepted.jobId,
        accepted.reused
          ? "Existing run selected."
          : accepted.status === "queued"
            ? "Manual sync job has been queued."
            : "Sync started.",
      );
      // Acceptance is final; detail retrieval errors belong to the run panel, not the form.
      await queryClient
        .fetchQuery({
          queryKey: ["job", accepted.jobId],
          queryFn: ({ signal }) =>
            api<JobDetail>(`/api/jobs/${encodeURIComponent(accepted.jobId)}`, { signal }),
          retry: false,
        })
        .catch(() => undefined);
    },
    onError: () => {
      setAnnouncement("");
      setFormError("The sync could not be started. Please try again.");
      setFormErrorArea("request");
      setTimeout(() => formErrorRef.current?.focus());
    },
  });
  useEffect(() => {
    const status = detail.data?.status;
    const id = detail.data?.id;
    if (
      !ingest.isPending &&
      status &&
      previousStatus.current &&
      previousStatus.current.id === id &&
      ACTIVE_STATUSES.has(previousStatus.current.status) &&
      !ACTIVE_STATUSES.has(status)
    ) {
      setAnnouncement(`Run ${STATUS_LABELS[detail.data?.displayStatus ?? status].toLowerCase()}.`);
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
    }
    previousStatus.current = status && id ? { id, status } : undefined;
  }, [
    detail.data?.id,
    detail.data?.status,
    detail.data?.displayStatus,
    ingest.isPending,
    queryClient,
  ]);
  const [commandBusy, setCommandBusy] = useState<"cancel" | "retry" | null>(null);
  async function cancel() {
    if (!detail.data) return;
    setCommandBusy("cancel");
    try {
      const updated = await api<JobDetail>(
        `/api/jobs/${encodeURIComponent(detail.data.id)}/cancellation`,
        { method: "POST", body: "{}" },
        csrfToken,
      );
      queryClient.setQueryData(["job", detail.data.id], updated);
      setAnnouncement(
        ACTIVE_STATUSES.has(updated.status)
          ? "Cancellation requested."
          : `Run ${STATUS_LABELS[updated.status].toLowerCase()}.`,
      );
      await refreshOperations(detail.data.id);
    } catch (error) {
      await refreshOperations(detail.data.id);
      setAnnouncement(
        error instanceof ApiError && error.status === 409
          ? "The run changed before cancellation could be requested."
          : "Cancellation could not be requested.",
      );
    } finally {
      setCommandBusy(null);
    }
  }
  async function retry() {
    if (
      !detail.data ||
      (detail.data.operation === "coverage_update" &&
        (detail.data.displayStatus === "superseded" ||
          detail.data.retryEligibility?.canRetry === false ||
          blockedRetry?.jobId === detail.data.id))
    )
      return;
    setCommandBusy("retry");
    try {
      const accepted = await api<IngestAccepted>(
        `/api/jobs/${encodeURIComponent(detail.data.id)}/retry`,
        { method: "POST", body: "{}" },
        csrfToken,
      );
      selectJob(accepted.jobId, accepted.reused ? "Existing retry selected." : "Retry accepted.");
      await refreshOperations(detail.data.id);
    } catch (error) {
      const updateCount = queryClient.getQueryState(["job", detail.data.id])?.dataUpdateCount ?? 0;
      const reason =
        error instanceof ApiError && error.status === 409 ? retryBlockedReason(error) : undefined;
      if (error instanceof ApiError && error.status === 409) {
        if (reason)
          setBlockedRetry({
            jobId: detail.data.id,
            reason,
            dataUpdateCount: updateCount,
          });
      }
      await refreshOperations(detail.data.id);
      const refreshed = queryClient.getQueryState<JobDetail>(["job", detail.data.id]);
      setAnnouncement(
        reason &&
          !refreshed?.error &&
          (refreshed?.dataUpdateCount ?? 0) > updateCount &&
          refreshed?.data?.retryEligibility?.canRetry
          ? "Run details refreshed. Retry is now available."
          : error instanceof ApiError && error.status === 409
            ? retryConflictMessage(error)
            : "The retry could not be started.",
      );
    } finally {
      setCommandBusy(null);
    }
  }

  const dismiss = useMutation({
    mutationFn: (id: string) =>
      api<Notification>(
        `/api/notifications/${encodeURIComponent(id)}/dismissal`,
        { method: "POST", body: "{}" },
        csrfToken,
      ),
    onSuccess: () => {
      void snapshot.refetch();
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (ingest.isPending) return;
    const values = new FormData(event.currentTarget);
    const startDate = String(values.get("startDate") ?? "");
    const endDate = String(values.get("endDate") ?? "");
    const connectedIds = new Set(
      snapshot.data?.sources
        .filter((source) => source.status === "connected")
        .map((source) => source.id),
    );
    const requestedSources = [...selectedSources].filter((id) => connectedIds.has(id));
    if (requestedSources.length === 0) {
      setFormError("Select at least one connected source.");
      setFormErrorArea("sources");
      setTimeout(() => formErrorRef.current?.focus());
      return;
    }
    if (rangeMode === "bounded" && (!validDate(startDate) || !validDate(endDate))) {
      setFormError("Enter both inclusive dates.");
      setFormErrorArea("dates");
      setTimeout(() => formErrorRef.current?.focus());
      return;
    }
    if (rangeMode === "bounded" && startDate > endDate) {
      setFormError("Start date must be on or before end date.");
      setFormErrorArea("dates");
      setTimeout(() => formErrorRef.current?.focus());
      return;
    }
    setFormError("");
    setFormErrorArea(null);
    const body: IngestCreate = { sourceIds: requestedSources };
    if (rangeMode === "bounded") {
      body.startDate = startDate;
      body.endDate = endDate;
    }
    ingest.mutate(body);
  }

  const connected = snapshot.data?.sources.filter((source) => source.status === "connected") ?? [];
  const notifications = snapshot.data?.notifications ?? [];
  const dismissibleConfirmation = [
    "Manual sync job has been queued.",
    "Sync started.",
    "Existing run selected.",
  ].includes(announcement);
  return (
    <main className="data-sync-page">
      <div className="summary-contours" aria-hidden="true" />
      <a
        className="mobile-back"
        href="/"
        onClick={(event) => {
          event.preventDefault();
          navigate("/");
        }}
      >
        Back to Summary
      </a>
      <div
        className={announcement ? "sync-feedback sync-command-feedback" : "visually-hidden"}
        role="status"
        aria-label="Sync feedback"
        aria-atomic="true"
      >
        {announcement && <span>{announcement}</span>}
        {dismissibleConfirmation && (
          <button type="button" className="secondary" onClick={() => setAnnouncement("")}>
            Dismiss
          </button>
        )}
      </div>

      {notifications.map((notification) => (
        <NotificationBanner
          key={notification.id}
          notification={notification}
          source={snapshot.data?.sources.find((source) => source.id === notification.sourceId)}
          preferences={preferences}
          pending={dismiss.isPending && dismiss.variables === notification.id}
          onDismiss={() => dismiss.mutate(notification.id)}
          onSelectJob={(jobId) => selectJob(jobId)}
        />
      ))}
      {snapshot.data?.notificationsTruncated && (
        <p className="notification-truncation">More notifications are available.</p>
      )}
      {dismiss.isError && (
        <p className="sync-inline-error" role="alert">
          The notification could not be dismissed.
        </p>
      )}

      <div className="sync-operational-grid">
        <section className="sync-card manual-card" aria-labelledby="manual-sync-heading">
          <div className="sync-card-heading">
            <div>
              <p className="card-kicker">Manual</p>
              <h2 id="manual-sync-heading">Start a sync</h2>
            </div>
          </div>
          {snapshot.isPending && (
            <p className="sync-retrieval-state" role="status" aria-busy="true">
              Retrieving sources...
            </p>
          )}
          {snapshot.isError && (
            <QueryError
              copy="Data Sync status is unavailable."
              retry={() => void snapshot.refetch()}
            />
          )}
          {snapshot.data && (
            <form onSubmit={submit} noValidate aria-busy={ingest.isPending}>
              {formError && (
                <div
                  className="error-summary"
                  id={formErrorId}
                  role="alert"
                  tabIndex={-1}
                  ref={formErrorRef}
                >
                  {formError}
                </div>
              )}
              <fieldset aria-describedby={formErrorArea === "sources" ? formErrorId : undefined}>
                <legend>Sources</legend>
                {connected.length >= 2 && (
                  <button
                    type="button"
                    className="select-all"
                    onClick={() =>
                      setSelectedSources(new Set(connected.map((source) => source.id)))
                    }
                  >
                    Select all connected
                  </button>
                )}
                <div className="sync-source-options">
                  {snapshot.data.sources.map((source) => (
                    <label key={source.id} className="sync-source-option">
                      <input
                        type="checkbox"
                        checked={source.status === "connected" && selectedSources.has(source.id)}
                        disabled={source.status !== "connected"}
                        onChange={(event) =>
                          setSelectedSources((current) => {
                            const next = new Set(current);
                            if (event.target.checked) next.add(source.id);
                            else next.delete(source.id);
                            return next;
                          })
                        }
                      />
                      <span>
                        <strong>{source.displayName}</strong>
                        <small>
                          {sourceType(source.type)} / {SOURCE_STATUS_LABELS[source.status]}
                          {source.status === "connected" && source.autoSyncEnabled
                            ? ", automatic sync"
                            : ""}
                        </small>
                        {source.status === "connection-failed" && source.statusSummary && (
                          <small>{source.statusSummary}</small>
                        )}
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>
              <fieldset aria-describedby={formErrorArea === "dates" ? formErrorId : undefined}>
                <legend>Data range</legend>
                <div className="sync-radio-options">
                  <label>
                    <input
                      type="radio"
                      name="mode"
                      checked={rangeMode === "incremental"}
                      onChange={() => setRangeMode("incremental")}
                    />{" "}
                    <span>
                      <strong>New data only</strong>
                      <small>Import data not previously processed.</small>
                    </span>
                  </label>
                  <label>
                    <input
                      type="radio"
                      name="mode"
                      checked={rangeMode === "bounded"}
                      onChange={() => setRangeMode("bounded")}
                    />{" "}
                    <span>
                      <strong>Specific date range</strong>
                      <small>Use inclusive calendar dates.</small>
                    </span>
                  </label>
                </div>
              </fieldset>
              {rangeMode === "bounded" && (
                <div className="field-pair sync-date-fields">
                  <div className="field">
                    <label htmlFor="sync-start-date">Start date</label>
                    <input
                      id="sync-start-date"
                      name="startDate"
                      type="date"
                      aria-invalid={formErrorArea === "dates" || undefined}
                      aria-describedby={formErrorArea === "dates" ? formErrorId : undefined}
                      onBlur={completeDateRangeOnBlur}
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="sync-end-date">End date</label>
                    <input
                      id="sync-end-date"
                      name="endDate"
                      type="date"
                      aria-invalid={formErrorArea === "dates" || undefined}
                      aria-describedby={formErrorArea === "dates" ? formErrorId : undefined}
                    />
                  </div>
                </div>
              )}
              <button type="submit" className="primary" disabled={ingest.isPending}>
                {ingest.isPending ? "Starting sync..." : "Start sync"}
              </button>
            </form>
          )}
        </section>

        <section className="sync-card schedule-card" aria-labelledby="schedule-heading">
          <div className="sync-card-heading">
            <div>
              <p className="card-kicker">Automated</p>
              <h2 id="schedule-heading">Schedule & freshness</h2>
            </div>
            {snapshot.data && (
              <span
                className={`schedule-state schedule-state--${snapshot.data.schedule.enabled ? "enabled" : "disabled"}`}
              >
                {snapshot.data.schedule.enabled ? "Enabled" : "Disabled"}
              </span>
            )}
          </div>
          {snapshot.isPending && (
            <p className="sync-retrieval-state" role="status">
              Retrieving schedule...
            </p>
          )}
          {snapshot.isError && (
            <QueryError
              copy="Schedule details are unavailable."
              retry={() => void snapshot.refetch()}
            />
          )}
          {snapshot.data && (
            <>
              <dl className="schedule-details">
                <div>
                  <dt>
                    <span>Interval</span>
                    <InfoTip
                      label="About Interval"
                      content="Automatically run a sync job on this interval"
                    />
                  </dt>
                  <dd>{interval(snapshot.data.schedule.cadenceSeconds)}</dd>
                </div>
                <div>
                  <dt>
                    <span>Sources enabled</span>
                    <InfoTip
                      label="About Sources enabled"
                      content="Number of sources with automated sync enabled"
                    />
                  </dt>
                  <dd>{snapshot.data.schedule.sourceCount}</dd>
                </div>
                <div>
                  <dt>
                    <span>Stale after</span>
                    <InfoTip
                      label="About Stale after"
                      content="Show a warning after this period if no new workouts have been imported by auto-sync"
                    />
                  </dt>
                  <dd>
                    {snapshot.data.schedule.staleDays}{" "}
                    {snapshot.data.schedule.staleDays === 1 ? "day" : "days"}
                  </dd>
                </div>
                <div>
                  <dt>
                    <span>Next run</span>
                    <InfoTip
                      label="About Next run"
                      content="Local date/time when the next sync job will run"
                    />
                  </dt>
                  <dd>
                    <NextRun value={snapshot.data.schedule.nextRunAt} preferences={preferences} />
                  </dd>
                </div>
              </dl>
              <div className="freshness-list">
                {snapshot.data.sources.map((source) => (
                  <article
                    key={source.id}
                    className={source.freshness.staleSince ? "is-stale" : ""}
                  >
                    <div>
                      <strong>{source.displayName}</strong>
                      {source.freshness.staleSince && <span className="stale-flag">Stale</span>}
                    </div>
                    <dl>
                      <div>
                        <dt>Last successful sync</dt>
                        <dd>{dateTime(source.freshness.lastSyncSucceededAt, preferences)}</dd>
                      </div>
                      <div>
                        <dt>Latest export date</dt>
                        <dd>{calendarDate(source.freshness.lastNewExportDate)}</dd>
                      </div>
                      {source.freshness.lastSyncStartedAt && (
                        <div>
                          <dt>Last sync started</dt>
                          <dd>{dateTime(source.freshness.lastSyncStartedAt, preferences)}</dd>
                        </div>
                      )}
                    </dl>
                    {source.freshness.staleSince && (
                      <p>
                        No new export has been discovered since{" "}
                        {calendarDate(source.freshness.staleSince)}.
                      </p>
                    )}
                  </article>
                ))}
              </div>
            </>
          )}
        </section>
      </div>

      {effectiveJobId && !detailNotFound && (
        <section
          className="detail-region"
          ref={detailRef}
          tabIndex={-1}
          aria-label="Selected run"
          aria-busy={detail.isFetching}
        >
          {detail.isPending && (
            <div className="sync-card">
              <p role="status">Loading run detail...</p>
            </div>
          )}
          {detail.isError && (
            <div className="sync-card">
              <QueryError copy="Run detail is unavailable." retry={() => void detail.refetch()} />
            </div>
          )}
          {detail.data && (
            <JobDetailCard
              job={detail.data}
              preferences={preferences}
              busy={commandBusy}
              onCancel={() => void cancel()}
              onRetry={() => void retry()}
              onSelectJob={selectJob}
              blockedRetryReason={
                blockedRetry && blockedRetry.jobId === detail.data.id
                  ? blockedRetry.reason
                  : undefined
              }
            />
          )}
        </section>
      )}

      <section className="history-section" aria-labelledby="history-heading">
        <div className="history-heading">
          <div>
            <p className="card-kicker">Task history</p>
            <h2 id="history-heading">Recent activity</h2>
          </div>
          <div className="history-filters">
            <HistoryFilter
              label="Operation"
              value={operationFilter}
              allLabel="All operations"
              options={OPERATION_OPTIONS}
              onChange={(value) => {
                setOperationFilter(value);
                resetHistoryPage();
              }}
            />
            <HistoryFilter
              label="Status"
              value={statusFilter}
              allLabel="All statuses"
              options={STATUS_OPTIONS}
              onChange={(value) => {
                setStatusFilter(value);
                resetHistoryPage();
              }}
            />
          </div>
        </div>
        {jobs.isPending && (
          <p className="sync-history-state" role="status">
            Loading task history...
          </p>
        )}
        {jobs.isError && (
          <QueryError copy="Task history is unavailable." retry={() => void jobs.refetch()} />
        )}
        {jobs.data?.items.length === 0 && (
          <p className="sync-history-state">No tasks match these filters.</p>
        )}
        {jobs.data && jobs.data.items.length > 0 && (
          <div
            ref={historyResultsRef}
            className="sync-history-results"
            style={historyResultsMinHeight ? { minHeight: historyResultsMinHeight } : undefined}
            aria-busy={jobs.isFetching}
          >
            <div className="sync-history-table">
              <table>
                <colgroup>
                  <col className="history-started-column" />
                  <col className="history-operation-column" />
                  <col className="history-results-column" />
                  <col className="history-status-column" />
                  <col className="history-action-column" />
                </colgroup>
                <thead>
                  <tr>
                    <th scope="col">Started</th>
                    <th scope="col">Operation</th>
                    <th scope="col">Results</th>
                    <th scope="col">Status</th>
                    <th scope="col">
                      <span className="visually-hidden">Action</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {jobs.data.items.map((job) => (
                    <tr key={job.id}>
                      <td>{dateTime(job.startedAt ?? job.createdAt, preferences)}</td>
                      <td>{historyOperation(job)}</td>
                      <td>{historyResults(job)}</td>
                      <td>
                        <StatusText status={job.displayStatus ?? job.status} />
                      </td>
                      <td>
                        <button
                          type="button"
                          className="detail-link"
                          onClick={() => selectJob(job.id)}
                        >
                          View detail
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="sync-history-cards">
              {jobs.data.items.map((job) => (
                <article key={job.id}>
                  <div>
                    <time>{dateTime(job.startedAt ?? job.createdAt, preferences)}</time>
                    <StatusText status={job.displayStatus ?? job.status} />
                  </div>
                  <dl>
                    <div>
                      <dt>Operation</dt>
                      <dd>{historyOperation(job)}</dd>
                    </div>
                    <div>
                      <dt>Results</dt>
                      <dd>{historyResults(job)}</dd>
                    </div>
                  </dl>
                  <button type="button" className="detail-link" onClick={() => selectJob(job.id)}>
                    View detail
                  </button>
                </article>
              ))}
            </div>
          </div>
        )}
        {jobs.data && jobs.data.pagination.totalPages > 1 && (
          <nav className="pagination" aria-label="Task history pages">
            <button
              type="button"
              className="secondary"
              disabled={historyPage <= 1 || jobs.isFetching}
              onClick={() => changeHistoryPage(historyPage - 1)}
            >
              Previous
            </button>
            <span>
              Page {jobs.data.pagination.page} of {jobs.data.pagination.totalPages}
            </span>
            <button
              type="button"
              className="secondary"
              disabled={historyPage >= jobs.data.pagination.totalPages || jobs.isFetching}
              onClick={() => changeHistoryPage(historyPage + 1)}
            >
              Next
            </button>
          </nav>
        )}
      </section>
    </main>
  );
}
