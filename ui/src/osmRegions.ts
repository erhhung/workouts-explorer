import { dateTimeFormatter, numberFormatter } from "./formatters";
import { formatEnglishDate } from "./date";

export type RegionStatus =
  | "Ready"
  | "Stale"
  | "Waiting"
  | "Downloading"
  | "Processing"
  | "Pausing"
  | "Canceling"
  | "Paused"
  | "Canceled"
  | "Failed"
  | "Deleting"
  | "Available";
export type RegionUpdateStep = {
  order: number;
  name: string;
  repeated: boolean;
  state: "pending" | "running" | "completed" | "failed" | "canceled" | "paused";
  batch: number;
  batches: number;
  startedAt: string | null;
  finishedAt: string | null;
  elapsedTotal?: number | null;
};
export type RegionUpdate = {
  id: string;
  regionId: string;
  state:
    | "waiting"
    | "downloading"
    | "processing"
    | "pausing"
    | "canceling"
    | "paused"
    | "canceled"
    | "failed"
    | "deleting"
    | "succeeded";
  phase: "Downloading" | "Processing";
  operation: "download" | "reprocess" | "delete";
  monitorDatabaseStorage?: boolean;
  storagePolicyRevision?: number;
  buildId: number | null;
  resumable: boolean;
  promoted: boolean;
  currentOrder: number;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  error: string | null;
  elapsedTotal?: number | null;
  elapsedSince?: string | null;
  executionElapsed?: number | null;
  executionSince?: string | null;
  cleared?: boolean;
  steps?: RegionUpdateStep[];
};
export const updateActive = (update: RegionUpdate | undefined) =>
  Boolean(
    update &&
    ["waiting", "downloading", "processing", "pausing", "canceling", "deleting"].includes(
      update.state,
    ),
  );
export const updateStatus = (update: RegionUpdate): RegionStatus | undefined =>
  (
    ({
      waiting: "Waiting",
      downloading: "Downloading",
      processing: "Processing",
      pausing: "Pausing",
      canceling: "Canceling",
      paused: "Paused",
      canceled: "Canceled",
      failed: "Failed",
      deleting: "Deleting",
    }) as Record<string, RegionStatus>
  )[update.state];
export type OSMRegion = {
  id: string;
  provider: string;
  providerId: string;
  parentId: string | null;
  name: string;
  available: boolean;
  areaM2: number;
  pbfBytes: number | null;
  pbfModifiedAt: string | null;
  metadataCheckedAt: string | null;
  metadataAttemptedAt: string | null;
  metadataErrorCode: string | null;
  generation: number | null;
  buildId?: number | null;
  downloadedAt: string | null;
  downloadedBytes: number | null;
  sourceTimestamp: string | null;
  promotedAt: string | null;
  generationElapsedSeconds?: number | null;
  databaseBytes: number | null;
  buildDatabaseBytes?: number | null;
  buildDatabaseId?: number | null;
  buildDatabaseMeasuredAt?: string | null;
  databaseMeasuredAt: string | null;
  processedDatabaseBytes: number | null;
  workoutCount: number;
  coverageRouteCount: number;
  missingRegionRouteCount?: number;
  status?: RegionStatus;
  reprocessingRequired?: boolean;
  update?: RegionUpdate;
};
export type CatalogRefresh = {
  id: string;
  state: "queued" | "running" | "succeeded" | "partially_succeeded" | "failed";
  providers: string[];
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  failureCode: string | null;
  progress: {
    phase?: string;
    provider?: string;
    total?: number;
    completed?: number;
    failed?: number;
    providersCompleted?: number;
    providersFailed?: number;
  };
};
export type OSMCatalogs = {
  refreshEnabled: boolean;
  updatesEnabled?: boolean;
  providers: {
    provider: string;
    state: string;
    refreshedAt: string | null;
    attemptedAt: string | null;
    regions: number;
    metadataFailed: number;
    failureCode: string | null;
  }[];
  regions: OSMRegion[];
  refresh: CatalogRefresh | null;
};
export type RegionSort =
  | "name"
  | "pbfModifiedAt"
  | "pbfBytes"
  | "downloadedAt"
  | "databaseBytes"
  | "workoutCount"
  | "status";
export const REGION_COLUMNS: {
  field: RegionSort | "generation";
  label: string;
  initial?: "asc" | "desc";
}[] = [
  { field: "name", label: "Name", initial: "asc" },
  { field: "pbfModifiedAt", label: "Date", initial: "desc" },
  { field: "pbfBytes", label: "PBF size", initial: "desc" },
  { field: "downloadedAt", label: "Downloaded", initial: "desc" },
  { field: "generation", label: "Generation" },
  { field: "databaseBytes", label: "Database usage", initial: "desc" },
  { field: "workoutCount", label: "Workouts", initial: "desc" },
  { field: "status", label: "Status", initial: "desc" },
];
export const baseRegionStatus = (region: OSMRegion): RegionStatus =>
  region.generation === null
    ? "Available"
    : (region.reprocessingRequired ?? region.status === "Stale")
      ? "Stale"
      : "Ready";
export const regionStatus = (region: OSMRegion): RegionStatus =>
  region.status ?? baseRegionStatus(region);
export function elapsedTotal(seconds: number | null | undefined) {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return undefined;
  if (seconds < 10) return `${seconds.toFixed(2)}s total`;
  const total = Math.floor(seconds),
    hours = Math.floor(total / 3600),
    minutes = Math.floor((total % 3600) / 60),
    remainder = total % 60;
  return `${[hours ? `${hours}h` : null, minutes ? `${minutes}m` : null, `${remainder}s`].filter(Boolean).join(" ")} total`;
}
export function updateElapsedLabel(update: RegionUpdate, now: number) {
  if (
    !["succeeded", "failed", "canceled"].includes(update.state) &&
    update.executionElapsed != null
  ) {
    const started = update.executionSince ? Date.parse(update.executionSince) : NaN;
    const seconds =
      update.executionElapsed + (Number.isFinite(started) ? Math.max(0, now - started) / 1000 : 0);
    return elapsedTotal(seconds)?.replace(/ total$/, " elapsed");
  }
  if (update.elapsedTotal == null) return undefined;
  let seconds = update.elapsedTotal;
  if (["downloading", "processing"].includes(update.state) && update.elapsedSince) {
    const started = Date.parse(update.elapsedSince);
    if (Number.isFinite(started)) seconds += Math.max(0, now - started) / 1000;
  }
  const total = elapsedTotal(seconds);
  return ["succeeded", "failed", "canceled"].includes(update.state)
    ? total
    : total?.replace(/ total$/, " elapsed");
}
export const canViewRegionProgress = (region: OSMRegion) =>
  Boolean(region.update && !region.update.cleared);
export const canOpenStatusProgress = canViewRegionProgress;
export const catalogPending = (job: CatalogRefresh | null | undefined) =>
  job?.state === "queued" || job?.state === "running";
export function osmBytes(value: number | null, fixed = false) {
  if (value === null) return "n/a";
  const index = Math.min(4, Math.max(0, Math.floor(Math.log(Math.max(value, 1)) / Math.log(1024))));
  return `${numberFormatter("en-US", { minimumFractionDigits: fixed ? 1 : 0, maximumFractionDigits: index || fixed ? 1 : 0 }).format(value / 1024 ** index)} ${["B", "KiB", "MiB", "GiB", "TiB"][index]}`;
}
export const exactBytes = (value: number | null) =>
  value === null ? undefined : `${numberFormatter("en-US").format(value)} bytes`;
export function retainedBuildBytes(region: OSMRegion) {
  return region.update &&
    !region.update.promoted &&
    region.update.operation !== "delete" &&
    region.update.buildId === region.buildDatabaseId &&
    ["Processing", "Pausing", "Paused", "Failed", "Canceling"].includes(regionStatus(region)) &&
    region.buildDatabaseMeasuredAt &&
    region.buildDatabaseBytes != null
    ? region.buildDatabaseBytes
    : null;
}
export function regionDatabaseTotalBytes(region: OSMRegion) {
  const build = retainedBuildBytes(region);
  return region.databaseBytes === null && build === null
    ? null
    : (region.databaseBytes ?? 0) + (build ?? 0);
}
export const regionDate = (value: string | null, long = false) =>
  value
    ? formatEnglishDate(new Date(value), {
        month: long ? "long" : "short",
        day: "numeric",
        year: "numeric",
      })
    : "n/a";
export const regionExactTime = (value: string | null) =>
  value
    ? `${regionDate(value, true)}, ${dateTimeFormatter("en-US", { hour: "numeric", minute: "2-digit", second: "2-digit", hour12: true, timeZoneName: "short" }).format(new Date(value))}`
    : undefined;
export function regionValue(region: OSMRegion, field: RegionSort | "generation") {
  switch (field) {
    case "workoutCount":
      return numberFormatter("en-US").format(region.workoutCount ?? 0);
    case "name":
      return region.name;
    case "pbfModifiedAt":
      return regionDate(region.pbfModifiedAt);
    case "downloadedAt":
      return regionDate(region.downloadedAt);
    case "pbfBytes":
      return osmBytes(region.pbfBytes);
    case "databaseBytes":
      return retainedBuildBytes(region) === null
        ? osmBytes(region.databaseBytes, true)
        : `${region.databaseBytes === null ? "" : `${osmBytes(region.databaseBytes, true)} + `}${osmBytes(retainedBuildBytes(region), true)}`;
    case "generation":
      return region.generation === null ? "n/a" : String(region.generation);
    case "status":
      return regionStatus(region);
  }
}
