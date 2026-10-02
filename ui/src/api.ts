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
  coverageDiagnosticsEnabled: boolean;
  initialized: boolean;
  dateRange?: DateRangePreference | null;
}

export type DateRangeEnum = "thisWeek" | "lastWeek" | "last7Days" | "last30Days" | "thisMonth" | "lastMonth" | "thisYear" | "lastYear";
export type DateRangePreference = DateRangeEnum | `${string}/${string}`;
export type WorkoutColumn = "date" | "type" | "duration" | "distance" | "pace" | "calories" | "heartRate" | "elevationGain";
export type WorkoutSortDirection = "asc" | "desc";
export interface WorkoutSort { field: WorkoutColumn; direction: WorkoutSortDirection }
export const DEFAULT_WORKOUT_SORT: WorkoutSort = { field: "date", direction: "desc" };

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
  routeCount: number;
  routedDistance: ExactMetric | null;
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
  splitPaces: {
    kilometer: { fastestSeconds: string; slowestSeconds: string } | null;
    mile: { fastestSeconds: string; slowestSeconds: string } | null;
  };
  calories: ExactMetric | null;
  activeCalories: ExactMetric | null;
  heartRate: ExactMetric | null;
  maximumHeartRate: ExactMetric | null;
  elevationGain: ExactMetric | null;
  minimumElevation: ExactMetric | null;
  maximumElevation: ExactMetric | null;
  routePointCount: number;
  routeAvailable: boolean;
}

export interface WorkoutList {
  range: ResolvedDateRange;
  pagination: { page: number; pageSize: number; totalItems: number; totalPages: number };
  columnExtents: {
    duration: string | null;
    distance: ExactMetric | null;
    pace: ExactMetric | null;
    calories: ExactMetric | null;
    heartRate: ExactMetric | null;
    elevationGain: ExactMetric | null;
  };
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
  baseMaps: BaseMapsConfig;
  passwordMinimumLength: number;
  pageSizeMaximum: number;
  features: {
    coverageMatcherDiagnostics: boolean;
  };
}

export interface BaseMapAttribution {
  text: string;
  links: Array<{ label: string; url: string }>;
}

export interface BaseMapFamily {
  id: string;
  label: string;
  styles: { light: string; dark: string };
  attribution: BaseMapAttribution;
  resourceOrigins: string[];
}

export interface BaseMapsConfig {
  families: BaseMapFamily[];
  fallbackFamilyId: string;
  workoutTypeMappings: Array<{ providerLabel: string; normalizedTypeKey: string; familyId: string }>;
}

export interface MapSelectionWorkout {
  id: string;
  type: { id: string; key: string; name: string };
  startedAt: string;
  endedAt: string;
  duration: string;
  localStartDate: string | null;
  partialRoute: boolean;
  bounds: { minimumLongitude: number; minimumLatitude: number; maximumLongitude: number; maximumLatitude: number };
  distance: ExactMetric | null;
  pace: ExactMetric | null;
  calories: ExactMetric | null;
  heartRate: ExactMetric | null;
  elevationGain: ExactMetric | null;
  coverageReadiness: {
    mapDataStatus: "pending" | "unavailable" | "ready";
    processingStatus: "unprocessed" | "queued" | "running" | "current" | "failed" | "stale";
    resultStatus: "none" | "current" | "stale";
  };
}

export interface MapSelection {
  id: string;
  expiresAt: string;
  dataGeneration: number;
  range: { startDate: string; endDate: string };
  bounds: null | { minimumLongitude: number; minimumLatitude: number; maximumLongitude: number; maximumLatitude: number };
  workouts: MapSelectionWorkout[];
  focusedWorkoutId: string | null;
  routeTileUrl: string;
  coverageTileUrl: string;
}

export type CoverageFocusFeatureCollection = {
  type: "FeatureCollection";
  features: Array<{ type: "Feature"; properties: { entityKind: "path" | "park"; countBucket: number }; geometry: { type: "LineString"; coordinates: number[][] } | { type: "MultiLineString"; coordinates: number[][][] } }>;
};

export type RoadCoverageSortField = "rangeCount" | "name" | "cityOrRegion" | "rangeFirst" | "allTimeFirst" | "rangeLatest" | "allTimeLatest";
export interface RoadCoverageSort { field: RoadCoverageSortField; direction: WorkoutSortDirection }

export interface RoadCoverageEntity {
  entityId: string;
  entityKind: "path" | "park";
  name: string | null;
  localityName: string | null;
  regionId: string;
  regionName: string | null;
  broadClass: "road" | "cycleway" | "footway" | "trail" | "park" | "other";
  rangeWorkoutCount: number;
  rangeFirstDate: string;
  rangeFirstWorkoutId: string;
  rangeLatestDate: string;
  rangeLatestWorkoutId: string;
  allTimeWorkoutCount: number;
  allTimeFirstDate: string;
  allTimeFirstWorkoutId: string;
  allTimeLatestDate: string;
  allTimeLatestWorkoutId: string;
  bounds: { minimumLongitude: number; minimumLatitude: number; maximumLongitude: number; maximumLatitude: number };
}

export interface RoadCoverageList {
  pagination: Pagination;
  items: RoadCoverageEntity[];
}

export interface RoadCoverageDetail extends RoadCoverageEntity {
  geometry: { type: "LineString"; coordinates: number[][] } | { type: "MultiLineString"; coordinates: number[][][] };
  fitBounds: { minimumLongitude: number; minimumLatitude: number; maximumLongitude: number; maximumLatitude: number };
}

export type CoverageDiagnosticOverallLabel = "correct" | "incorrect" | "uncertain";
export type CoverageDiagnosticSegmentLabelValue = "expected" | "unexpected" | "uncertain";

export interface CoverageDiagnosticGeneration {
  regionId: string;
  generation: number;
  sourceUrl: string;
  sourceSha256: string;
  sourceHeaderTimestamp: string | null;
  importerVersion: number;
  derivationVersion: number;
  promotedAt: string;
}

export interface CoverageDiagnosticCounts {
  originalPoints: number;
  sampledPoints: number;
  matchedPoints: number;
  ambiguousPoints: number;
  unmatchedPoints: number;
  rejectedPoints: number;
  traversals: number;
  portions: number;
  uniqueSegments: number;
  durationMilliseconds: number;
}

export interface CoverageDiagnosticEvidenceProperties {
  portionOrdinal: number;
  physicalSegmentId: string;
  direction: "forward" | "reverse";
  regionId: string;
  generation: number;
  traversedMeters: number;
  evidenceClass: "matched" | "ambiguous";
}

export interface CoverageDiagnosticEvidenceFeature {
  type: "Feature";
  geometry: { type: "LineString"; coordinates: number[][] };
  properties: CoverageDiagnosticEvidenceProperties;
}

export interface CoverageDiagnosticEvidenceCollection {
  type: "FeatureCollection";
  features: CoverageDiagnosticEvidenceFeature[];
}

export interface CoverageDiagnosticSegmentLabel {
  portionOrdinal: number;
  label: CoverageDiagnosticSegmentLabelValue;
}

export interface CoverageDiagnosticLabelsPatch {
  overall?: CoverageDiagnosticOverallLabel;
  segments: CoverageDiagnosticSegmentLabel[];
}

export interface CoverageDiagnosticLabels {
  overall?: CoverageDiagnosticOverallLabel | null;
  segments: CoverageDiagnosticSegmentLabel[];
}

export interface CoverageDiagnosticUnavailableRegion {
  regionId: string;
  displayName: string;
}

interface CoverageDiagnosticRunThroughV60 {
  id: string;
  workoutId: string;
  routeRevision: number;
  rulesVersion: "coverage-experimental-v1";
  samplingVersion: "coverage-sampling-experimental-v1";
  pathPolicyVersion: "coverage-path-policy-experimental-v1" | "coverage-path-policy-experimental-v2" | "coverage-path-policy-experimental-v3" | "coverage-path-policy-experimental-v4" | "coverage-path-policy-experimental-v5" | "coverage-path-policy-experimental-v6" | "coverage-path-policy-experimental-v7" | "coverage-path-policy-experimental-v8" | "coverage-path-policy-experimental-v9" | "coverage-path-policy-experimental-v10" | "coverage-path-policy-experimental-v11" | "coverage-path-policy-experimental-v12" | "coverage-path-policy-experimental-v13" | "coverage-path-policy-experimental-v14" | "coverage-path-policy-experimental-v15" | "coverage-path-policy-experimental-v16" | "coverage-path-policy-experimental-v17" | "coverage-path-policy-experimental-v18" | "coverage-path-policy-experimental-v19" | "coverage-path-policy-experimental-v20" | "coverage-path-policy-experimental-v21" | "coverage-path-policy-experimental-v22" | "coverage-path-policy-experimental-v23" | "coverage-path-policy-experimental-v24" | "coverage-path-policy-experimental-v25" | "coverage-path-policy-experimental-v26" | "coverage-path-policy-experimental-v27" | "coverage-path-policy-experimental-v28" | "coverage-path-policy-experimental-v29" | "coverage-path-policy-experimental-v30" | "coverage-path-policy-experimental-v31" | "coverage-path-policy-experimental-v32" | "coverage-path-policy-experimental-v33" | "coverage-path-policy-experimental-v34" | "coverage-path-policy-experimental-v35" | "coverage-path-policy-experimental-v36" | "coverage-path-policy-experimental-v37" | "coverage-path-policy-experimental-v38" | "coverage-path-policy-experimental-v39" | "coverage-path-policy-experimental-v40" | "coverage-path-policy-experimental-v41" | "coverage-path-policy-experimental-v42" | "coverage-path-policy-experimental-v43" | "coverage-path-policy-experimental-v44" | "coverage-path-policy-experimental-v45" | "coverage-path-policy-experimental-v46" | "coverage-path-policy-experimental-v47" | "coverage-path-policy-experimental-v48" | "coverage-path-policy-experimental-v49" | "coverage-path-policy-experimental-v50" | "coverage-path-policy-experimental-v51" | "coverage-path-policy-experimental-v52" | "coverage-path-policy-experimental-v53" | "coverage-path-policy-experimental-v54" | "coverage-path-policy-experimental-v55" | "coverage-path-policy-experimental-v56" | "coverage-path-policy-experimental-v57" | "coverage-path-policy-experimental-v58" | "coverage-path-policy-experimental-v59" | "coverage-path-policy-experimental-v60";
  movementMode: "foot" | "bicycle" | "shared_public";
  minimumTraversalMeters: number;
  outcome: "evaluated" | "no_evidence";
  counts: CoverageDiagnosticCounts;
  generations: CoverageDiagnosticGeneration[];
  unavailableRegions: CoverageDiagnosticUnavailableRegion[];
  overlay: CoverageDiagnosticEvidenceCollection;
  labels: CoverageDiagnosticLabels;
  createdAt: string;
}

export type CoverageDiagnosticRun = Omit<CoverageDiagnosticRunThroughV60, "pathPolicyVersion"> & {
  pathPolicyVersion: CoverageDiagnosticRunThroughV60["pathPolicyVersion"] | "coverage-path-policy-experimental-v61" | "coverage-path-policy-experimental-v62" | "coverage-path-policy-experimental-v63" | "coverage-path-policy-experimental-v64" | "coverage-path-policy-experimental-v65" | "coverage-path-policy-experimental-v66" | "coverage-path-policy-experimental-v67" | "coverage-path-policy-experimental-v68" | "coverage-path-policy-experimental-v69" | "coverage-path-policy-experimental-v70" | "coverage-path-policy-experimental-v71" | "coverage-path-policy-experimental-v72" | "coverage-path-policy-experimental-v73" | "coverage-path-policy-experimental-v74" | "coverage-path-policy-experimental-v75" | "coverage-path-policy-experimental-v76" | "coverage-path-policy-experimental-v77" | "coverage-path-policy-experimental-v78" | "coverage-path-policy-experimental-v79" | "coverage-path-policy-experimental-v80" | "coverage-path-policy-experimental-v81" | "coverage-path-policy-experimental-v82";
};

export type SourceStatus = "checking-connection" | "connected" | "connection-failed";
export type JobStatus = "queued" | "running" | "succeeded" | "partially_succeeded" | "failed" | "cancelled";
export type JobTrigger = "manual" | "scheduled" | "system";
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
  operation?: "data_sync" | "workout_deletion" | "coverage_update";
  trigger: JobTrigger;
  status: JobStatus;
  progress: JobProgress;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  terminalAt?: string;
  routeStats?: CoverageRouteStats;
}

export interface CoverageRouteStats {
  total: number;
  processed: number;
  running: number;
  succeeded: number;
  failed: number;
  cancelled: number;
  superseded: number;
}

export interface CoverageJobContext {
  regionId: string;
  targetOsmGeneration: number;
  targetWorkRevision: number;
  rulesVersion: string;
  samplingVersion: string;
  pathPolicyVersion: string;
}

export interface CoverageRouteContext {
  workoutId: string;
  startedAt: string;
  localStartDate?: string | null;
  workoutType: string;
  targetGenerations: Array<{ regionId: string; generation: number; latest: boolean }>;
  resultOutcome?: "applied" | "no_evidence" | "superseded";
  durationMilliseconds?: number;
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
  coverage?: CoverageJobContext;
  coverageRoute?: CoverageRouteContext;
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
