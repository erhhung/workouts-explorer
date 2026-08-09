export interface IdentitySummary {
  id: string;
  username: string;
  fullName: string;
  role: "administrator" | "user";
}

export interface Profile {
  id: string;
  username: string;
  email: string;
  fullName: string;
  avatarUrl?: "/api/me/avatar";
}

export interface Session {
  id: string;
  expiresAt: string;
  csrfToken: string;
  identity: IdentitySummary;
}

export interface Preferences {
  theme: "light" | "dark";
  units: "imperial" | "metric";
  timezone: string;
  firstWeekday: "monday" | "sunday";
  clockFormat: "12h" | "24h";
  workoutColumns: WorkoutColumn[];
  pageSize: number;
  dateRange?: DateRangePreference | null;
}

export type DateRangeEnum = "thisWeek" | "lastWeek" | "last7Days" | "last30Days" | "thisMonth" | "lastMonth" | "thisYear" | "lastYear";
export type DateRangePreference = DateRangeEnum | `${string}/${string}`;
export type WorkoutColumn = "date" | "type" | "duration" | "distance" | "pace" | "calories" | "heartRate" | "elevation";
export type WorkoutSortDirection = "asc" | "desc";

export interface ResolvedDateRange {
  startDate: string;
  endDate: string;
  timezone: string;
}

export interface ExactMetric {
  value: string;
  unit: string;
}

export interface WorkoutType {
  id: string;
  key: string;
  displayName: string;
}

export interface SummaryTotals {
  count: number;
  duration: string;
  distance: ExactMetric | null;
  energy: ExactMetric | null;
}

export interface WorkoutSummary {
  range: ResolvedDateRange;
  totals: SummaryTotals;
  byType: Array<{ type: WorkoutType; totals: SummaryTotals }>;
}

export interface Workout {
  id: string;
  sourceId: string;
  type: WorkoutType;
  startedAt: string;
  endedAt: string;
  duration: string;
  localStartDate: string | null;
  displayTimezone: string | null;
  originalStartOffsetMinutes: number | null;
  originalEndOffsetMinutes: number | null;
  timezone: string | null;
  indoor: boolean | null;
  location: string | null;
  distance: ExactMetric | null;
  pace: ExactMetric | null;
  calories: ExactMetric | null;
  heartRate: ExactMetric | null;
  elevation: ExactMetric | null;
  routePointCount: number;
  routeAvailable: boolean;
}

export interface WorkoutList {
  range: ResolvedDateRange;
  pagination: { page: number; pageSize: number; totalItems: number; totalPages: number };
  items: Workout[];
}

export interface WorkoutProvenanceWarning {
  code: "incomplete_metric" | "unexpected_unit" | "invalid_optional_route_value";
  field: string;
  routePoint?: number;
}

export interface WorkoutProvenanceEvent {
  id: string;
  kind: "created" | "updated" | "matched_unchanged";
  jobId: string;
  sourceId: string;
  sourceName: string;
  sourceType: string;
  sourceFile: string;
  warnings: WorkoutProvenanceWarning[];
  importedAt: string;
}

export interface WorkoutProvenance {
  workoutId: string;
  items: WorkoutProvenanceEvent[];
}

export interface WorkoutDeletionAccepted {
  jobId: string;
  status: "queued" | "running" | "succeeded" | "failed" | "cancelled";
  reused: boolean;
  targetCount: number;
}

export interface PublicConfig {
  productName: string;
  pollingIntervalSeconds: number;
  mapFitPaddingPixels: number;
  baseMapTileUrl?: string;
  baseMapAttribution?: string;
  passwordMinimumLength: number;
  pageSizeMaximum: number;
}

export type SourceStatus = "checking-connection" | "connected" | "connection-failed";
export type JobStatus = "queued" | "running" | "succeeded" | "partially_succeeded" | "failed" | "cancelled";
export type JobTrigger = "manual" | "scheduled";
export type NotificationState = "unresolved" | "remind" | "resolved" | "dismissed";
export type NotificationSeverity = "info" | "warning" | "error";

export interface Pagination {
  page: number;
  pageSize: number;
  totalItems: number;
  totalPages: number;
}

export interface JobProgress {
  current: number;
  total: number;
  filesDiscovered: number;
  filesSkipped: number;
  filesSucceeded: number;
  filesFailed: number;
  workoutsCreated: number;
  workoutsUpdated: number;
  workoutsUnchanged: number;
  workoutsRejected: number;
}

export interface JobSourceContext {
  sourceId: string;
  generation: number;
  displayName: string;
  sourceType: string;
}

export interface JobSummary {
  id: string;
  operation?: "data_sync" | "workout_deletion";
  trigger: JobTrigger;
  status: JobStatus;
  progress: JobProgress;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  terminalAt?: string;
}

export interface JobResults {
  filesSucceeded?: number;
  filesFailed?: number;
  workoutsCreated?: number;
  workoutsUpdated?: number;
  workoutsUnchanged?: number;
  workoutsRejected?: number;
}

export interface JobDetail extends JobSummary {
  parentJobId?: string;
  retryRootJobId?: string;
  retryOrdinal?: number;
  latestRetryJobId?: string;
  latestRetryOrdinal?: number;
  attempt: number;
  source?: JobSourceContext;
  children: JobDetail[];
  results?: JobResults;
  failureCode?: string;
  failureSummary?: string;
  cancelRequested: boolean;
  cancelRequestedAt?: string;
  retryOfJobId?: string;
  retriedByJobIds: string[];
}

export interface JobList {
  pagination: Pagination;
  items: JobSummary[];
}

export interface SourceFreshness {
  lastSyncStartedAt?: string;
  lastSyncSucceededAt?: string;
  lastNewExportDiscoveredAt?: string;
  lastNewExportDate?: string;
  staleSince?: string;
}

export interface DataSyncSource {
  id: string;
  displayName: string;
  type: string;
  status: SourceStatus;
  autoSyncEnabled: boolean;
  checkedAt?: string;
  freshness: SourceFreshness;
}

export interface DataSyncSchedule {
  enabled: boolean;
  sourceCount: number;
  cadence: string | null;
  cadenceSeconds: number;
  staleDays: number;
  nextRunAt?: string;
  lastEnqueuedAt?: string;
  lastJobId?: string;
}

export interface Notification {
  id: string;
  type: string;
  severity: NotificationSeverity;
  state: NotificationState;
  subjectType: "account" | "job" | "source";
  subjectId?: string;
  jobId?: string;
  sourceId?: string;
  title: string;
  message: string;
  createdAt: string;
  updatedAt: string;
  resolvedAt?: string;
  remindAt?: string;
}

export interface NotificationList {
  pagination: Pagination;
  items: Notification[];
}

export interface DataSync {
  schedule: DataSyncSchedule;
  sources: DataSyncSource[];
  activeJob?: JobSummary;
  latestJob?: JobSummary;
  notifications: Notification[];
  notificationsTruncated: boolean;
}

export interface IngestCreate {
  sourceIds: string[];
  startDate?: string;
  endDate?: string;
}

export interface IngestAccepted {
  jobId: string;
  status: "queued" | "running";
  reused: boolean;
}

export interface SafeFields { [key: string]: string | number }

export interface JobFile {
  id: string;
  jobId: string;
  source: JobSourceContext;
  basename: string;
  state: "discovered" | "processing" | "succeeded" | "failed";
  sizeBytes: number;
  processingStartedAt?: string;
  processedAt?: string;
  failureCode?: string;
  failureSummary?: string;
  createdAt: string;
  updatedAt: string;
}

export interface JobEvent {
  id: number;
  jobId: string;
  severity: NotificationSeverity;
  code: string;
  message: string;
  fields: SafeFields;
  createdAt: string;
}

export interface JobLog extends Omit<JobEvent, "severity"> {
  severity: "debug" | NotificationSeverity;
}

export interface JobFileList { pagination: Pagination; items: JobFile[] }
export interface JobEventList { pagination: Pagination; items: JobEvent[] }
export interface JobLogList { pagination: Pagination; items: JobLog[] }

export interface ApiProblem {
  title?: string;
  detail?: string;
  errors?: Array<{ field: string; code: string; message?: string }>;
}

export class ApiError extends Error {
  constructor(public status: number, public problem: ApiProblem = {}) {
    super(problem.title ?? "Request failed");
  }
}

export const SESSION_EXPIRED_EVENT = "workouts-explorer:session-expired";

function isPublicRequest(pathname: string, method: string) {
  if (pathname === "/api/config") return method === "GET";
  if (pathname === "/api/session") return method === "GET" || method === "POST";
  if (pathname === "/api/session-tokens") return method === "POST";
  if (/^\/api\/invitations\/[^/]+$/.test(pathname)) return method === "GET";
  return method === "POST" && (pathname === "/api/registrations" || pathname === "/api/password-reset-requests" || pathname === "/api/password-resets");
}

async function request(path: string, init: RequestInit, csrfToken: string | undefined, accept: string) {
  const pathname = new URL(path, window.location.origin).pathname;
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  headers.set("Accept", accept);
  if (init.body) headers.set("Content-Type", "application/json");
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);

  const response = await fetch(path, { ...init, credentials: "same-origin", headers });
  if (!response.ok) {
    if (response.status === 401 && !isPublicRequest(pathname, method)) {
      window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    }
    let problem: ApiProblem = {};
    try {
      problem = (await response.json()) as ApiProblem;
    } catch {
      // Error copy stays generic when the response is not valid Problem Details.
    }
    throw new ApiError(response.status, problem);
  }
  return response;
}

export async function api<T>(path: string, init: RequestInit = {}, csrfToken?: string): Promise<T> {
  const response = await request(path, init, csrfToken, "application/json, application/problem+json");
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export async function downloadApi(path: string, init: RequestInit = {}, accept = "application/json, application/problem+json"): Promise<{ blob: Blob; filename: string }> {
  const response = await request(path, init, undefined, accept);
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const filename = /^attachment;\s*filename="([A-Za-z0-9._-]+)"$/.exec(disposition)?.[1] ?? "workout-export.json";
  return { blob: await response.blob(), filename };
}
