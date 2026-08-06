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

export interface PublicConfig {
  productName: string;
  pollingIntervalSeconds: number;
  mapFitPaddingPixels: number;
  baseMapTileUrl?: string;
  baseMapAttribution?: string;
  passwordMinimumLength: number;
  pageSizeMaximum: number;
}

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

export async function api<T>(path: string, init: RequestInit = {}, csrfToken?: string): Promise<T> {
  const pathname = new URL(path, window.location.origin).pathname;
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json, application/problem+json");
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
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}
