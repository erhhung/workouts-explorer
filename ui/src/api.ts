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
  workoutColumns: string[];
  pageSize: number;
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

export async function api<T>(path: string, init: RequestInit = {}, csrfToken?: string): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json, application/problem+json");
  if (init.body) headers.set("Content-Type", "application/json");
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);

  const response = await fetch(path, { ...init, credentials: "same-origin", headers });
  if (!response.ok) {
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
