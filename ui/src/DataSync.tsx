import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type CSSProperties, type FormEvent, type KeyboardEvent, useEffect, useId, useRef, useState } from "react";
import {
  ApiError,
  api,
  type DataSync as DataSyncModel,
  type IngestAccepted,
  type IngestCreate,
  type JobDetail,
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
} from "./api";
import { InfoTip, instantZone, ZoneBadge } from "./Tooltip";

const ACTIVE_STATUSES = new Set<JobStatus>(["queued", "running"]);
const RETRYABLE_STATUSES = new Set<JobStatus>(["failed", "cancelled", "partially_succeeded"]);
const STATUS_LABELS: Record<JobStatus, string> = {
  queued: "Queued",
  running: "Running",
  succeeded: "Completed",
  partially_succeeded: "Partially completed",
  failed: "Failed",
  cancelled: "Canceled",
};
const PAGE_SIZE = 25;
const STATUS_OPTIONS = Object.entries(STATUS_LABELS) as Array<[JobStatus, string]>;
const OPERATION_OPTIONS = [["manual_sync", "Manual sync"], ["automated_sync", "Automated sync"], ["workout_deletion", "Workout deletion"]] as const;

function validDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return false;
  return new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]))).toISOString().slice(0, 10) === value;
}

function dateTime(value: string | undefined, preferences: Preferences, timeZone = preferences.timezone) {
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
    return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
  }
}

function calendarDate(value: string | undefined) {
  if (!value) return "Never";
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isNaN(date.getTime()) ? "Unavailable" : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(date);
}

function interval(seconds: number) {
  if (seconds === 86400) return "Once a day";
  if (seconds === 43200) return "Twice a day";
  const hours = seconds / 3600;
  return `Every ${new Intl.NumberFormat(undefined, { maximumFractionDigits: 6 }).format(hours)} ${hours === 1 ? "hour" : "hours"}`;
}

function historyOperation(job: JobSummary) {
  return job.operation === "workout_deletion" ? "Workout deletion" : job.trigger === "manual" ? "Manual sync" : "Automated sync";
}

function historyResults(job: JobSummary) {
  if (job.operation === "workout_deletion") return `${job.progress.current} of ${job.progress.total} deleted`;
  return `${job.progress.filesSucceeded} files / ${job.progress.workoutsCreated} new`;
}

function NextRun({ value, preferences }: { value?: string; preferences: Preferences }) {
  if (!value) return <>Never</>;
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return <>Unavailable</>;
  const zone = instantZone(preferences.timezone, instant);
  if (zone.label === "TZ unavailable") {
    const title = `${preferences.timezone} unavailable; displayed in UTC (UTC+0:00)`;
    return <span className="next-run-value"><span>{dateTime(value, preferences, "UTC")}</span><ZoneBadge label="UTC" title={title} /></span>;
  }
  return <span className="next-run-value"><span>{dateTime(value, preferences)}</span><ZoneBadge label={zone.label} title={zone.title} /></span>;
}

function sourceType(value: string) {
  return value === "health-auto-export-local" ? "Health Auto Export" : value;
}

function StatusBadge({ status, cancelRequested = false, style }: { status: JobStatus; cancelRequested?: boolean; style?: CSSProperties }) {
  const cancellationRequested = cancelRequested && ACTIVE_STATUSES.has(status);
  return <span className={`sync-status sync-status--${cancellationRequested ? "cancellation" : status}`} style={style}>{cancellationRequested ? "Cancellation requested" : STATUS_LABELS[status]}</span>;
}

function Progress({ progress, status, style }: { progress: JobProgress; status: JobStatus; style?: CSSProperties }) {
  if (progress.total === 0 && ACTIVE_STATUSES.has(status)) return <p className="sync-progress sync-progress--unknown" style={style}>Discovering files; total not yet known</p>;
  if (progress.total === 0) return null;
  return <div className="sync-progress" style={style}><span>{progress.current} of {progress.total} files</span><progress value={Math.min(progress.current, progress.total)} max={progress.total} /></div>;
}

function Results({ progress, results, status }: { progress: JobProgress; results?: JobDetail["results"]; status: JobStatus }) {
  const rejectedHelpId = useId();
  const values = results ?? progress;
  const noData = (values.filesSucceeded ?? 0) === 0 && (values.filesFailed ?? 0) === 0 &&
    (values.workoutsCreated ?? 0) === 0 && (values.workoutsUpdated ?? 0) === 0 && (values.workoutsUnchanged ?? 0) === 0 && (values.workoutsRejected ?? 0) === 0;
  if (noData) return <p className="sync-no-data">{status === "succeeded" ? "No new data." : "No files were processed."}</p>;
  return <dl className="result-counts">
    <div><dt>Files Processed</dt><dd>{(values.filesSucceeded ?? 0) + (values.filesFailed ?? 0)}</dd></div>
    <div><dt>Workouts Imported</dt><dd>{values.workoutsCreated ?? 0}</dd></div>
    <div><dt>Workouts Unchanged</dt><dd>{values.workoutsUnchanged ?? 0}</dd></div>
    <div><dt>Files Failed</dt><dd>{values.filesFailed ?? 0}</dd></div>
    <div><dt>Workouts Updated</dt><dd>{values.workoutsUpdated ?? 0}</dd></div>
    <div><dt>Workouts Rejected</dt><dd aria-describedby={rejectedHelpId}>{values.workoutsRejected ?? 0}</dd><span id={rejectedHelpId} className="visually-hidden">Workout records skipped while their source file continued processing.</span></div>
  </dl>;
}

function ordinal(value: number) {
  const words = ["", "First", "Second", "Third", "Fourth", "Fifth", "Sixth", "Seventh", "Eighth", "Ninth", "Tenth"];
  if (words[value]) return words[value];
  const remainder = value % 100;
  const suffix = remainder >= 11 && remainder <= 13 ? "th" : value % 10 === 1 ? "st" : value % 10 === 2 ? "nd" : value % 10 === 3 ? "rd" : "th";
  return `${value}${suffix}`;
}

function HistoryFilter({ label, value, allLabel, options, onChange }: {
  label: string;
  value: string;
  allLabel: string;
  options: ReadonlyArray<readonly [string, string]>;
  onChange: (value: string) => void;
}) {
  const selectedLabel = options.find(([option]) => option === value)?.[1] ?? allLabel;
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger className="range-trigger history-filter-trigger" aria-label={`Filter by ${label.toLowerCase()}`}>
      <span>{label}</span><strong>{selectedLabel}</strong><span aria-hidden="true">v</span>
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal><DropdownMenu.Content className="menu-content history-filter-menu" align="end" sideOffset={8}>
      <DropdownMenu.RadioGroup value={value} onValueChange={onChange}>
        <DropdownMenu.RadioItem value="">{allLabel}<DropdownMenu.ItemIndicator aria-hidden="true">&#10003;</DropdownMenu.ItemIndicator></DropdownMenu.RadioItem>
        {options.map(([option, optionLabel]) => <DropdownMenu.RadioItem key={option} value={option}>{optionLabel}<DropdownMenu.ItemIndicator aria-hidden="true">&#10003;</DropdownMenu.ItemIndicator></DropdownMenu.RadioItem>)}
      </DropdownMenu.RadioGroup>
    </DropdownMenu.Content></DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

function QueryError({ copy, retry }: { copy: string; retry: () => void }) {
  return <div className="sync-query-error" role="alert"><span>{copy}</span><button type="button" className="secondary" onClick={retry}>Try again</button></div>;
}

type ArtifactKind = "files" | "events" | "logs";

function ArtifactDisclosure({ jobId, kind, preferences }: { jobId: string; kind: ArtifactKind; preferences: Preferences }) {
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState(1);
  const label = kind[0].toUpperCase() + kind.slice(1);
  const query = useQuery({
    queryKey: ["job-artifacts", jobId, kind, page],
    queryFn: ({ signal }) => api<JobFileList | JobEventList | JobLogList>(`/api/jobs/${encodeURIComponent(jobId)}/${kind}?page=${page}&pageSize=${PAGE_SIZE}`, { signal }),
    enabled: open,
  });
  function escape(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") setOpen(false);
  }
  return <div className="artifact-disclosure" onKeyDown={escape}>
    <button type="button" className="artifact-trigger" aria-expanded={open} onClick={() => setOpen((value) => !value)}>{label}<span aria-hidden="true">{open ? "-" : "+"}</span></button>
    {open && <div className="artifact-panel" aria-busy={query.isFetching}>
      {query.isPending && <p role="status">Loading {kind}...</p>}
      {query.isError && <QueryError copy={`${label} are unavailable.`} retry={() => void query.refetch()} />}
      {query.data && query.data.items.length === 0 && <p>No {kind} recorded.</p>}
      {query.data && kind === "files" && <ul className="artifact-list">{(query.data as JobFileList).items.map((file) => <li key={file.id}>
        <strong>{file.basename}</strong><span>{file.source.displayName} / {file.state}</span><span>{file.sizeBytes.toLocaleString()} bytes</span>
        {file.failureSummary && <span className="failure-copy">{file.failureSummary}</span>}
      </li>)}</ul>}
      {query.data && kind !== "files" && <ul className="artifact-list">{(query.data.items as Array<JobEvent | JobLog>).map((item) => <li key={item.id}>
        <strong>{item.severity}: {item.message}</strong><span>{dateTime(item.createdAt, preferences)} / {item.code}</span>
        {Object.keys(item.fields).length > 0 && <dl className="safe-fields">{Object.entries(item.fields).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>}
      </li>)}</ul>}
      {query.data && query.data.pagination.totalPages > 1 && <nav className="pagination" aria-label={`${label} pages`}>
        <button type="button" className="secondary" disabled={page <= 1 || query.isFetching} onClick={() => setPage((value) => value - 1)}>Previous</button>
        <span>Page {query.data.pagination.page} of {query.data.pagination.totalPages}</span>
        <button type="button" className="secondary" disabled={page >= query.data.pagination.totalPages || query.isFetching} onClick={() => setPage((value) => value + 1)}>Next</button>
      </nav>}
    </div>}
  </div>;
}

function JobDetailCard({ job, preferences, busy, onCancel, onRetry, onSelectJob }: {
  job: JobDetail;
  preferences: Preferences;
  busy: "cancel" | "retry" | null;
  onCancel: () => void;
  onRetry: () => void;
  onSelectJob: (jobId: string) => void;
}) {
  const deletion = job.operation === "workout_deletion";
  return <article className="sync-card job-detail-card">
    <div className="sync-card-heading"><div><p className="card-kicker">Job {job.id.slice(0, 8)}</p><h2>{deletion ? "Workout deletion" : "Run detail"}</h2></div><StatusBadge status={job.status} cancelRequested={job.cancelRequested} /></div>
    <dl className="job-metadata">
      <div><dt>{deletion ? "Operation" : "Trigger"}</dt><dd>{deletion ? "Workout deletion" : job.trigger === "manual" ? "Manual" : "Scheduled"}</dd></div>
      <div><dt>{deletion ? "Targets" : "Sources"}</dt><dd>{deletion ? job.progress.total : job.children.length || (job.source ? 1 : 0)}</dd></div>
      <div><dt>Queued</dt><dd>{dateTime(job.createdAt, preferences)}</dd></div>
      <div><dt>Started</dt><dd>{dateTime(job.startedAt, preferences)}</dd></div>
      <div><dt>Finished</dt><dd>{dateTime(job.terminalAt, preferences)}</dd></div>
      {job.retryRootJobId && job.retryOrdinal != null && <div><dt>Retry of job <a href={`/data-sync/jobs/${job.retryRootJobId}`} onClick={(event) => { event.preventDefault(); onSelectJob(job.retryRootJobId!); }}>{job.retryRootJobId.slice(0, 8)}</a></dt><dd>{ordinal(job.retryOrdinal)}</dd></div>}
      {job.latestRetryJobId && job.latestRetryOrdinal != null && <div><dt>Retry by job <a href={`/data-sync/jobs/${job.latestRetryJobId}`} onClick={(event) => { event.preventDefault(); onSelectJob(job.latestRetryJobId!); }}>{job.latestRetryJobId.slice(0, 8)}</a></dt><dd>{ordinal(job.latestRetryOrdinal)}</dd></div>}
    </dl>
    {!deletion && <Progress progress={job.progress} status={job.status} />}
    {!deletion && (!ACTIVE_STATUSES.has(job.status) || job.progress.current > 0) && <Results progress={job.progress} results={job.results} status={job.status} />}
    {job.failureSummary && <p className="failure-copy"><strong>Run issue:</strong> {job.failureSummary}</p>}
    {job.children.length > 0 && <section className="source-runs" aria-labelledby="source-runs-heading"><h3 id="source-runs-heading">Source runs</h3>{job.children.map((child) => <article key={child.id} className="source-run">
      <div className="source-run-heading"><div><strong>{child.source?.displayName ?? "Source unavailable"}</strong> <span>({child.source ? sourceType(child.source.sourceType) : "Type unavailable"})</span></div><StatusBadge status={child.status} cancelRequested={child.cancelRequested} style={{ position: "relative", top: ".2rem" }} /></div>
      <Progress progress={child.progress} status={child.status} style={{ margin: ".7rem 0 -.3rem 0" }} />
      {(!ACTIVE_STATUSES.has(child.status) || child.progress.current > 0) && <Results progress={child.progress} results={child.results} status={child.status} />}
      {child.failureSummary && <p className="failure-copy">{child.failureSummary}</p>}
    </article>)}</section>}
    {!deletion && (ACTIVE_STATUSES.has(job.status) || RETRYABLE_STATUSES.has(job.status)) && <div className="job-actions">
      {ACTIVE_STATUSES.has(job.status) && <button type="button" className="secondary danger-button" disabled={busy !== null || job.cancelRequested} onClick={onCancel}>{busy === "cancel" || job.cancelRequested ? "Cancelling..." : "Cancel run"}</button>}
      {RETRYABLE_STATUSES.has(job.status) && !job.latestRetryJobId && <button type="button" className="primary" disabled={busy !== null} onClick={onRetry}>{busy === "retry" ? "Retrying..." : "Retry run"}</button>}
    </div>}
    {deletion && RETRYABLE_STATUSES.has(job.status) && !job.latestRetryJobId && <div className="job-actions"><button type="button" className="primary" disabled={busy !== null} onClick={onRetry}>{busy === "retry" ? "Retrying..." : "Retry deletion"}</button></div>}
    {!deletion && <div className="artifact-group" aria-label="Run records">
      <ArtifactDisclosure key={`${job.id}-files`} jobId={job.id} kind="files" preferences={preferences} />
      <ArtifactDisclosure key={`${job.id}-events`} jobId={job.id} kind="events" preferences={preferences} />
      <ArtifactDisclosure key={`${job.id}-logs`} jobId={job.id} kind="logs" preferences={preferences} />
    </div>}
  </article>;
}

function NotificationBanner({ notification, pending, onDismiss, onSelectJob }: { notification: Notification; pending: boolean; onDismiss: () => void; onSelectJob: (jobId: string) => void }) {
  return <article className={`sync-notification sync-notification--${notification.severity}`}>
    <div><strong>{notification.title}</strong><p>{notification.message}</p>{notification.state === "remind" && notification.remindAt && <small>Reminded until a later review.</small>}</div>
    <div className="notification-actions">{notification.jobId && <button type="button" className="detail-link" onClick={() => onSelectJob(notification.jobId!)}>View detail</button>}{(notification.state === "unresolved" || notification.state === "remind") && <button type="button" className="secondary" disabled={pending} onClick={onDismiss}>{pending ? "Dismissing..." : "Dismiss"}</button>}</div>
  </article>;
}

export function DataSync({ csrfToken, preferences, pollingIntervalSeconds, selectedJobId, navigate }: {
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
  const [announcement, setAnnouncement] = useState("");
  const previousStatus = useRef<JobStatus | undefined>(undefined);
  const formErrorId = useId();

  const snapshot = useQuery({
    queryKey: ["data-sync"],
    queryFn: ({ signal }) => api<DataSyncModel>("/api/data-sync", { signal }),
    refetchInterval: pollingIntervalSeconds * 1000,
  });
  useEffect(() => {
    if (!snapshot.data || selectionInitialized.current) return;
    selectionInitialized.current = true;
    setSelectedSources(new Set(snapshot.data.sources.filter((source) => source.status === "connected" && source.autoSyncEnabled).map((source) => source.id)));
  }, [snapshot.data]);

  const effectiveJobId = selectedJobId ?? snapshot.data?.activeJob?.id ?? snapshot.data?.latestJob?.id;
  const detail = useQuery({
    queryKey: ["job", effectiveJobId],
    queryFn: ({ signal }) => api<JobDetail>(`/api/jobs/${encodeURIComponent(effectiveJobId!)}`, { signal }),
    enabled: Boolean(effectiveJobId),
    refetchInterval: (query) => query.state.data && ACTIVE_STATUSES.has(query.state.data.status) ? pollingIntervalSeconds * 1000 : false,
  });
  const snapshotJobSignature = `${snapshot.data?.activeJob?.id ?? ""}:${snapshot.data?.activeJob?.status ?? ""}:${snapshot.data?.activeJob?.updatedAt ?? ""}:${snapshot.data?.latestJob?.id ?? ""}:${snapshot.data?.latestJob?.status ?? ""}:${snapshot.data?.latestJob?.updatedAt ?? ""}`;
  const previousSnapshotJobs = useRef<string | null>(null);
  useEffect(() => {
    if (!snapshot.data || previousSnapshotJobs.current === snapshotJobSignature) return;
    const hadSnapshot = previousSnapshotJobs.current !== null;
    previousSnapshotJobs.current = snapshotJobSignature;
    if (hadSnapshot) void queryClient.invalidateQueries({ queryKey: ["jobs"] });
  }, [queryClient, snapshot.data, snapshotJobSignature]);
  useEffect(() => {
    const status = detail.data?.status;
    if (status && previousStatus.current && ACTIVE_STATUSES.has(previousStatus.current) && !ACTIVE_STATUSES.has(status)) {
      setAnnouncement(`Run ${STATUS_LABELS[status].toLowerCase()}.`);
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
    }
    previousStatus.current = status;
  }, [detail.data?.status, queryClient]);
  const historyParams = new URLSearchParams({ page: String(historyPage), pageSize: String(PAGE_SIZE) });
  if (operationFilter) historyParams.set("operation", operationFilter);
  if (statusFilter) historyParams.set("status", statusFilter);
  const jobs = useQuery({
    queryKey: ["jobs", historyPage, operationFilter, statusFilter],
    queryFn: ({ signal }) => api<JobList>(`/api/jobs?${historyParams}`, { signal }),
  });

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
    mutationFn: (body: IngestCreate) => api<IngestAccepted>("/api/ingest", { method: "POST", body: JSON.stringify(body) }, csrfToken),
    onSuccess: (accepted) => selectJob(accepted.jobId, accepted.reused ? "Existing run selected." : "Run accepted."),
    onError: () => { setFormError("The sync could not be started. Please try again."); setFormErrorArea("request"); setTimeout(() => formErrorRef.current?.focus()); },
  });
  const [commandBusy, setCommandBusy] = useState<"cancel" | "retry" | null>(null);
  async function cancel() {
    if (!detail.data) return;
    setCommandBusy("cancel");
    try {
      const updated = await api<JobDetail>(`/api/jobs/${encodeURIComponent(detail.data.id)}/cancellation`, { method: "POST", body: "{}" }, csrfToken);
      queryClient.setQueryData(["job", detail.data.id], updated);
      setAnnouncement(ACTIVE_STATUSES.has(updated.status) ? "Cancellation requested." : `Run ${STATUS_LABELS[updated.status].toLowerCase()}.`);
      await refreshOperations(detail.data.id);
    } catch (error) {
      await refreshOperations(detail.data.id);
      setAnnouncement(error instanceof ApiError && error.status === 409 ? "The run changed before cancellation could be requested." : "Cancellation could not be requested.");
    } finally { setCommandBusy(null); }
  }
  async function retry() {
    if (!detail.data) return;
    setCommandBusy("retry");
    try {
      const accepted = await api<IngestAccepted>(`/api/jobs/${encodeURIComponent(detail.data.id)}/retry`, { method: "POST", body: "{}" }, csrfToken);
      selectJob(accepted.jobId, accepted.reused ? "Existing retry selected." : "Retry accepted.");
      await refreshOperations(detail.data.id);
    } catch (error) {
      await refreshOperations(detail.data.id);
      setAnnouncement(error instanceof ApiError && error.status === 409 ? "The run changed before retry could be started." : "The retry could not be started.");
    }
    finally { setCommandBusy(null); }
  }

  const dismiss = useMutation({
    mutationFn: (id: string) => api<Notification>(`/api/notifications/${encodeURIComponent(id)}/dismissal`, { method: "POST", body: "{}" }, csrfToken),
    onSuccess: () => {
      void snapshot.refetch();
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    const startDate = String(values.get("startDate") ?? "");
    const endDate = String(values.get("endDate") ?? "");
    const connectedIds = new Set(snapshot.data?.sources.filter((source) => source.status === "connected").map((source) => source.id));
    const requestedSources = [...selectedSources].filter((id) => connectedIds.has(id));
    if (requestedSources.length === 0) { setFormError("Select at least one connected source."); setFormErrorArea("sources"); setTimeout(() => formErrorRef.current?.focus()); return; }
    if (rangeMode === "bounded" && (!validDate(startDate) || !validDate(endDate))) { setFormError("Enter both inclusive dates."); setFormErrorArea("dates"); setTimeout(() => formErrorRef.current?.focus()); return; }
    if (rangeMode === "bounded" && startDate > endDate) { setFormError("Start date must be on or before end date."); setFormErrorArea("dates"); setTimeout(() => formErrorRef.current?.focus()); return; }
    setFormError(""); setFormErrorArea(null);
    const body: IngestCreate = { sourceIds: requestedSources };
    if (rangeMode === "bounded") { body.startDate = startDate; body.endDate = endDate; }
    ingest.mutate(body);
  }

  const connected = snapshot.data?.sources.filter((source) => source.status === "connected") ?? [];
  const notifications = snapshot.data?.notifications ?? [];
  return <main className="data-sync-page">
    <div className="summary-contours" aria-hidden="true" />
    <a className="mobile-back" href="/" onClick={(event) => { event.preventDefault(); navigate("/"); }}>Back to Summary</a>
    <span className="visually-hidden" aria-live="polite">{announcement}</span>

    {notifications.map((notification) => <NotificationBanner key={notification.id} notification={notification} pending={dismiss.isPending && dismiss.variables === notification.id} onDismiss={() => dismiss.mutate(notification.id)} onSelectJob={(jobId) => selectJob(jobId)} />)}
    {snapshot.data?.notificationsTruncated && <p className="notification-truncation">More notifications are available.</p>}
    {dismiss.isError && <p className="sync-inline-error" role="alert">The notification could not be dismissed.</p>}

    <div className="sync-operational-grid">
      <section className="sync-card manual-card" aria-labelledby="manual-sync-heading">
        <div className="sync-card-heading"><div><p className="card-kicker">Manual</p><h2 id="manual-sync-heading">Start a sync</h2></div></div>
        {snapshot.isPending && <p role="status" aria-busy="true">Loading sources...</p>}
        {snapshot.isError && <QueryError copy="Data Sync status is unavailable." retry={() => void snapshot.refetch()} />}
        {snapshot.data && <form onSubmit={submit} noValidate aria-busy={ingest.isPending}>
          {formError && <div className="error-summary" id={formErrorId} role="alert" tabIndex={-1} ref={formErrorRef}>{formError}</div>}
          <fieldset aria-describedby={formErrorArea === "sources" ? formErrorId : undefined}><legend>Sources</legend>
            {connected.length >= 2 && <button type="button" className="select-all" onClick={() => setSelectedSources(new Set(connected.map((source) => source.id)))}>Select all connected</button>}
            <div className="sync-source-options">{snapshot.data.sources.map((source) => <label key={source.id} className="sync-source-option">
              <input type="checkbox" checked={source.status === "connected" && selectedSources.has(source.id)} disabled={source.status !== "connected"} onChange={(event) => setSelectedSources((current) => { const next = new Set(current); if (event.target.checked) next.add(source.id); else next.delete(source.id); return next; })} />
              <span><strong>{source.displayName}</strong><small>{sourceType(source.type)} / {source.status === "checking-connection" ? "Checking connection" : source.status === "connection-failed" ? "Connection failed" : source.autoSyncEnabled ? "Connected, automatic sync" : "Connected"}</small></span>
            </label>)}</div>
          </fieldset>
          <fieldset aria-describedby={formErrorArea === "dates" ? formErrorId : undefined}><legend>Data range</legend><div className="sync-radio-options">
            <label><input type="radio" name="mode" checked={rangeMode === "incremental"} onChange={() => setRangeMode("incremental")} /> <span><strong>New data only</strong><small>Import data not previously processed.</small></span></label>
            <label><input type="radio" name="mode" checked={rangeMode === "bounded"} onChange={() => setRangeMode("bounded")} /> <span><strong>Specific date range</strong><small>Use inclusive calendar dates.</small></span></label>
          </div></fieldset>
          {rangeMode === "bounded" && <div className="field-pair sync-date-fields"><div className="field"><label htmlFor="sync-start-date">Start date</label><input id="sync-start-date" name="startDate" type="date" aria-invalid={formErrorArea === "dates" || undefined} aria-describedby={formErrorArea === "dates" ? formErrorId : undefined} /></div><div className="field"><label htmlFor="sync-end-date">End date</label><input id="sync-end-date" name="endDate" type="date" aria-invalid={formErrorArea === "dates" || undefined} aria-describedby={formErrorArea === "dates" ? formErrorId : undefined} /></div></div>}
          <button type="submit" className="primary" disabled={ingest.isPending}>{ingest.isPending ? "Starting sync..." : "Start sync"}</button>
        </form>}
      </section>

      <section className="sync-card schedule-card" aria-labelledby="schedule-heading">
        <div className="sync-card-heading"><div><p className="card-kicker">Automated</p><h2 id="schedule-heading">Schedule & freshness</h2></div>{snapshot.data && <span className={`schedule-state schedule-state--${snapshot.data.schedule.enabled ? "enabled" : "disabled"}`}>{snapshot.data.schedule.enabled ? "Enabled" : "Disabled"}</span>}</div>
        {snapshot.isPending && <p role="status">Loading schedule...</p>}
        {snapshot.isError && <QueryError copy="Schedule details are unavailable." retry={() => void snapshot.refetch()} />}
        {snapshot.data && <>
          <dl className="schedule-details">
            <div><dt><span>Interval</span><InfoTip label="About Interval" content="Automatically run a sync job on this interval" /></dt><dd>{interval(snapshot.data.schedule.cadenceSeconds)}</dd></div>
            <div><dt><span>Sources enabled</span><InfoTip label="About Sources enabled" content="Number of sources with automated sync enabled" /></dt><dd>{snapshot.data.schedule.sourceCount}</dd></div>
            <div><dt><span>Stale after</span><InfoTip label="About Stale after" content="Show a warning after this period if no new workouts have been imported by auto-sync" /></dt><dd>{snapshot.data.schedule.staleDays} {snapshot.data.schedule.staleDays === 1 ? "day" : "days"}</dd></div>
            <div><dt><span>Next run</span><InfoTip label="About Next run" content="Local date/time when the next sync job will run" /></dt><dd><NextRun value={snapshot.data.schedule.nextRunAt} preferences={preferences} /></dd></div>
          </dl>
          <div className="freshness-list">{snapshot.data.sources.map((source) => <article key={source.id} className={source.freshness.staleSince ? "is-stale" : ""}>
            <div><strong>{source.displayName}</strong>{source.freshness.staleSince && <span className="stale-flag">Stale</span>}</div>
            <dl><div><dt>Last successful sync</dt><dd>{dateTime(source.freshness.lastSyncSucceededAt, preferences)}</dd></div><div><dt>Latest export date</dt><dd>{calendarDate(source.freshness.lastNewExportDate)}</dd></div>{source.freshness.lastSyncStartedAt && <div><dt>Last sync started</dt><dd>{dateTime(source.freshness.lastSyncStartedAt, preferences)}</dd></div>}</dl>
            {source.freshness.staleSince && <p>No new export has been discovered since {calendarDate(source.freshness.staleSince)}.</p>}
          </article>)}</div>
        </>}
      </section>
    </div>

    {effectiveJobId && <section className="detail-region" ref={detailRef} tabIndex={-1} aria-label="Selected run" aria-busy={detail.isFetching}>
      {detail.isPending && <div className="sync-card"><p role="status">Loading run detail...</p></div>}
      {detail.isError && <div className="sync-card"><QueryError copy="Run detail is unavailable." retry={() => void detail.refetch()} /></div>}
      {detail.data && <JobDetailCard job={detail.data} preferences={preferences} busy={commandBusy} onCancel={() => void cancel()} onRetry={() => void retry()} onSelectJob={selectJob} />}
    </section>}

    <section className="history-section" aria-labelledby="history-heading">
      <div className="history-heading"><div><p className="card-kicker">Task history</p><h2 id="history-heading">Recent activity</h2></div><div className="history-filters">
        <HistoryFilter label="Operation" value={operationFilter} allLabel="All operations" options={OPERATION_OPTIONS} onChange={(value) => { setOperationFilter(value); setHistoryPage(1); }} />
        <HistoryFilter label="Status" value={statusFilter} allLabel="All statuses" options={STATUS_OPTIONS} onChange={(value) => { setStatusFilter(value); setHistoryPage(1); }} />
      </div></div>
      {jobs.isPending && <p className="sync-history-state" role="status">Loading task history...</p>}
      {jobs.isError && <QueryError copy="Task history is unavailable." retry={() => void jobs.refetch()} />}
      {jobs.data?.items.length === 0 && <p className="sync-history-state">No tasks match these filters.</p>}
      {jobs.data && jobs.data.items.length > 0 && <div aria-busy={jobs.isFetching}>
        <div className="sync-history-table"><table><thead><tr><th scope="col">Started</th><th scope="col">Operation</th><th scope="col">Results</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Action</span></th></tr></thead><tbody>{jobs.data.items.map((job) => <tr key={job.id}>
          <td>{dateTime(job.startedAt ?? job.createdAt, preferences)}</td><td>{historyOperation(job)}</td><td>{historyResults(job)}</td><td><span className={`sync-status-text sync-status-text--${job.status}`}>{STATUS_LABELS[job.status]}</span></td><td><button type="button" className="detail-link" onClick={() => selectJob(job.id)}>View detail</button></td>
        </tr>)}</tbody></table></div>
        <div className="sync-history-cards">{jobs.data.items.map((job) => <article key={job.id}><div><time>{dateTime(job.startedAt ?? job.createdAt, preferences)}</time><StatusBadge status={job.status} /></div><dl><div><dt>Operation</dt><dd>{historyOperation(job)}</dd></div><div><dt>Results</dt><dd>{historyResults(job)}</dd></div></dl><button type="button" className="detail-link" onClick={() => selectJob(job.id)}>View detail</button></article>)}</div>
      </div>}
      {jobs.data && jobs.data.pagination.totalPages > 1 && <nav className="pagination" aria-label="Task history pages"><button type="button" className="secondary" disabled={historyPage <= 1 || jobs.isFetching} onClick={() => setHistoryPage((page) => page - 1)}>Previous</button><span>Page {jobs.data.pagination.page} of {jobs.data.pagination.totalPages}</span><button type="button" className="secondary" disabled={historyPage >= jobs.data.pagination.totalPages || jobs.isFetching} onClick={() => setHistoryPage((page) => page + 1)}>Next</button></nav>}
    </section>
  </main>;
}
