import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import maplibregl, {
  type GeoJSONSource,
  type Map as MapLibreMap,
  type MapGeoJSONFeature,
  type MapMouseEvent,
  type StyleSpecification,
  type VectorTileSource,
} from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { type CSSProperties, useEffect, useId, useMemo, useRef, useState } from "react";
import {
  api,
  ApiError,
  DEFAULT_WORKOUT_SORT,
  SESSION_EXPIRED_EVENT,
  type BaseMapFamily,
  type BaseMapsConfig,
  type CoverageDiagnosticEvidenceCollection,
  type CoverageDiagnosticOverallLabel,
  type CoverageDiagnosticRun,
  type CoverageFocusFeatureCollection,
  type CoverageDiagnosticSegmentLabelValue,
  type DateRangeEnum,
  type DateRangePreference,
  type MapSelection,
  type MapSelectionWorkout,
  type Preferences,
  type PublicConfig,
  type RoadCoverageDetail,
  type RoadCoverageEntity,
  type RoadCoverageList,
  type RoadCoverageSort,
  type RoadCoverageSortField,
  type WorkoutColumn,
  type WorkoutSort,
} from "./api";
import { formatDateOnly } from "./date";
import { CustomDateRangeDialog } from "./CustomDateRangeDialog";
import { RecentDateRangeItems, useRecentDateRanges } from "./RecentDateRanges";
import { Tooltip } from "./Tooltip";
import type { AcceptedWorkoutDeletion } from "./Summary";
import { PrivateTiles } from "./privateTiles";
import { WorkoutTypeSelection } from "./WorkoutTypeSelection";

const ROUTES_LAYER = "private-workout-routes";
const HOVER_LAYER = "private-workout-route-hover";
const ROUTE_MARKERS_LAYER = "private-workout-route-markers";
const HOVER_MARKERS_LAYER = "private-workout-route-marker-hover";
const ROUTES_SOURCE = "private-workout-routes";
const RAW_FOCUS_SOURCE = "private-workout-route-focus";
const RAW_FOCUS_LAYER = "private-workout-route-focus";
const RAW_FOCUS_MARKERS_LAYER = "private-workout-route-focus-markers";
const COVERAGE_SOURCE = "private-workout-coverage";
const COVERAGE_FOCUS_SOURCE = "private-workout-coverage-focus";
const COVERAGE_LAYER = "private-workout-coverage";
const COVERAGE_PARK_LAYER = "private-workout-coverage-parks";
const COVERAGE_FOCUS_LAYER = "private-workout-coverage-focus";
const COVERAGE_PARK_FOCUS_LAYER = "private-workout-coverage-parks-focus";
const COVERAGE_HIGHLIGHT_SOURCE = "private-workout-coverage-highlight";
const COVERAGE_HIGHLIGHT_LAYER = "private-workout-coverage-highlight";
const ROUTE_ENDPOINTS_SOURCE = "private-workout-route-endpoints";
const ROUTE_START_LAYER = "private-workout-route-start";
const ROUTE_FINISH_LAYER = "private-workout-route-finish";
const DIAGNOSTIC_SOURCE = "coverage-diagnostic-overlay";
const DIAGNOSTIC_RAW_ROUTE_SOURCE = "coverage-diagnostic-raw-route";
const DIAGNOSTIC_BUSY_RETRY_DELAY_MS = 2000;
const DIAGNOSTIC_RAW_ROUTE_LAYER = "coverage-diagnostic-raw-route";
const DIAGNOSTIC_DIRECTION_SOURCE = "coverage-diagnostic-direction";
const DIAGNOSTIC_DIRECTION_LAYER = "coverage-diagnostic-direction";
const DIAGNOSTIC_DIRECTION_IMAGE = "coverage-diagnostic-direction-triangle";

function waitForDiagnosticRetry(signal: AbortSignal, delayMilliseconds: number) {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const aborted = () => {
      window.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", aborted);
      resolve();
    }, delayMilliseconds);
    signal.addEventListener("abort", aborted, { once: true });
  });
}

export async function retryInitialDiagnosticBusy<T>(
  request: () => Promise<T>,
  signal: AbortSignal,
  delayMilliseconds = DIAGNOSTIC_BUSY_RETRY_DELAY_MS,
) {
  try {
    return await request();
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 429) throw error;
  }
  await waitForDiagnosticRetry(signal, delayMilliseconds);
  return request();
}
const DIAGNOSTIC_MATCHED_LAYER = "coverage-diagnostic-matched";
const DIAGNOSTIC_AMBIGUOUS_LAYER = "coverage-diagnostic-ambiguous";
const DIAGNOSTIC_SELECTED_LAYER = "coverage-diagnostic-selected";
const DIAGNOSTIC_HIT_LAYER = "coverage-diagnostic-hit-target";
const DIAGNOSTIC_EVIDENCE_LAYERS = [
  DIAGNOSTIC_MATCHED_LAYER,
  DIAGNOSTIC_AMBIGUOUS_LAYER,
  DIAGNOSTIC_SELECTED_LAYER,
  DIAGNOSTIC_HIT_LAYER,
] as const;
const DIAGNOSTIC_LAYERS = [
  DIAGNOSTIC_RAW_ROUTE_LAYER,
  DIAGNOSTIC_DIRECTION_LAYER,
  ...DIAGNOSTIC_EVIDENCE_LAYERS,
] as const;
const ROUTE_LAYERS = [
  ROUTES_LAYER,
  ROUTE_MARKERS_LAYER,
  HOVER_LAYER,
  HOVER_MARKERS_LAYER,
  RAW_FOCUS_LAYER,
  RAW_FOCUS_MARKERS_LAYER,
  ROUTE_START_LAYER,
  ROUTE_FINISH_LAYER,
] as const;
const ROUTE_HOVER_DELAY_MS = 250;
const COVERAGE_READINESS_POLL_MS = 10_000;
const COVERAGE_HOVER_DELAY_MS = 750;
const COVERAGE_HOVER_LOOKUP_DELAY_MS = 250;
const COVERAGE_HOVER_CACHE_LIMIT = 128;
const COVERAGE_HOVER_CACHE_TTL_MS = 5 * 60_000;
const COVERAGE_DETAIL_TIMEOUT_MS = 30_000;
export const COVERAGE_HIGHLIGHT_DURATION_MS = 3000;
const COVERAGE_HIGHLIGHT_IDLE_FALLBACK_MS = 30000;
const DIAGNOSTIC_HOVER_DELAY_MS = 250;
const ROUTE_FINISH_MARKER_SIZE = 22;
const ROUTE_START_MARKER_RADIUS =
  (ROUTE_FINISH_MARKER_SIZE / Math.sqrt(Math.PI)) * 0.9 * 0.97 * 0.95;
const RAW_ROUTE_FADE = { duration: 400, delay: 0 } as const;
const MAP_SELECTION_RETRY_DELAYS_MS = [500, 1000, 2000, 4000, 5000];
const EXPLICIT_RANGE = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/;
const COMPACT_UUID = /^[0-9A-F]{32}$/;
const QUICK_RANGES: ReadonlyArray<[DateRangeEnum, string]> = [
  ["thisWeek", "This week"],
  ["lastWeek", "Last week"],
  ["last7Days", "Last 7 days"],
  ["last30Days", "Last 30 days"],
  ["thisMonth", "This month"],
  ["lastMonth", "Last month"],
  ["thisYear", "This year"],
  ["lastYear", "Last year"],
];
const ROUTE_PALETTE = [
  "#ef9b61",
  "#75bda6",
  "#e2c86e",
  "#68a9df",
  "#e07a9a",
  "#9f91df",
  "#69c3c8",
  "#d7a65c",
];
const SEMANTIC_ROUTE_COLORS = { walk: "#43d5e5", hiking: "#8ed081", cycling: "#69aef5" } as const;
const COVERAGE_RANGE_COLORS = [
  "#d95d0b",
  "#ed7d0c",
  "#f59e0b",
  "#f7b928",
  "#f9d64a",
  "#fff176",
] as const;
const COVERAGE_FOCUS_COLORS = [
  "#ff008c",
  "#ff5fb4",
  "#ff8bc8",
  "#ffaad2",
  "#ffc9e1",
  "#ffd8f0",
] as const;
const EMPTY_COVERAGE_FOCUS: CoverageFocusFeatureCollection = {
  type: "FeatureCollection",
  features: [],
};
const NON_FOCUSED_COVERAGE_LAYERS = [COVERAGE_LAYER, COVERAGE_PARK_LAYER] as const;
const FOCUSED_COVERAGE_LAYERS = [COVERAGE_FOCUS_LAYER, COVERAGE_PARK_FOCUS_LAYER] as const;
const COVERAGE_LAYERS = [
  COVERAGE_LAYER,
  COVERAGE_PARK_LAYER,
  COVERAGE_FOCUS_LAYER,
  COVERAGE_PARK_FOCUS_LAYER,
] as const;

function coverageColorExpression(colors: readonly string[]) {
  return [
    "match",
    ["get", "countBucket"],
    1,
    colors[0],
    2,
    colors[1],
    3,
    colors[2],
    4,
    colors[3],
    5,
    colors[4],
    6,
    colors[5],
    colors[0],
  ] as never;
}

export function startCoverageHighlightBlink(
  setOpacity: (opacity: number) => void,
  onReady: (ready: () => void) => () => void,
) {
  let countdownStarted = false;
  let stopped = false;
  let visible = true;
  let finishTimer: number | undefined;
  let fallbackTimer: number | undefined;
  let removeReadyListener: () => void = () => {};
  setOpacity(1);
  const blinkTimer = window.setInterval(() => {
    visible = !visible;
    setOpacity(visible ? 1 : 0);
  }, 250);
  const finish = () => {
    if (stopped) return;
    stopped = true;
    window.clearInterval(blinkTimer);
    if (fallbackTimer !== undefined) window.clearTimeout(fallbackTimer);
    removeReadyListener();
    setOpacity(0);
  };
  const startCountdown = () => {
    if (countdownStarted || stopped) return;
    countdownStarted = true;
    if (fallbackTimer !== undefined) window.clearTimeout(fallbackTimer);
    removeReadyListener();
    finishTimer = window.setTimeout(finish, COVERAGE_HIGHLIGHT_DURATION_MS);
  };
  removeReadyListener = onReady(startCountdown);
  if (countdownStarted) removeReadyListener();
  else fallbackTimer = window.setTimeout(startCountdown, COVERAGE_HIGHLIGHT_IDLE_FALLBACK_MS);
  return () => {
    if (stopped) return;
    stopped = true;
    window.clearInterval(blinkTimer);
    if (fallbackTimer !== undefined) window.clearTimeout(fallbackTimer);
    if (finishTimer !== undefined) window.clearTimeout(finishTimer);
    removeReadyListener();
    setOpacity(0);
  };
}

export function onMapContextReady(map: MapLibreMap, ready: () => void) {
  let stopped = false;
  const check = () => {
    if (stopped) return;
    if (map.isStyleLoaded() && !map.isMoving() && map.areTilesLoaded()) ready();
  };
  map.on("render", check);
  map.on("moveend", check);
  map.on("sourcedata", check);
  const initialCheck = window.setTimeout(check, 0);
  return () => {
    if (stopped) return;
    stopped = true;
    window.clearTimeout(initialCheck);
    map.off("render", check);
    map.off("moveend", check);
    map.off("sourcedata", check);
  };
}

export function selectionRequest(
  range: DateRangePreference,
  timezone: string,
  workoutIds?: string[],
  focusedWorkoutId?: string,
) {
  const explicit = EXPLICIT_RANGE.exec(range);
  const selector = explicit
    ? { startDate: explicit[1], endDate: explicit[2] }
    : { dateRangeEnum: range, tz: timezone };
  const selected = workoutIds === undefined ? selector : { ...selector, workoutIds };
  return focusedWorkoutId && (workoutIds === undefined || workoutIds.includes(focusedWorkoutId))
    ? { ...selected, focusedWorkoutId }
    : selected;
}

export function requestedWorkoutIds(search: string) {
  const params = new URLSearchParams(search);
  return [...params.getAll("workoutId"), ...params.getAll("workoutIds")]
    .map((id) => id.toUpperCase())
    .filter((id, index, ids) => COMPACT_UUID.test(id) && ids.indexOf(id) === index);
}

function canonicalQualifiedActivityLabel(label: string) {
  const normalized = label
    .normalize("NFKC")
    .replace(/\p{White_Space}+/gu, " ")
    .replace(/^ | $/g, "");
  const match =
    /^(indoor|outdoor) *(run|running|walk|walking|cycle|cycling|bike|biking|bicycle|bicycling)$/i.exec(
      normalized,
    );
  if (!match) return undefined;
  const activity = match[2].toLowerCase();
  return `${match[1].toLowerCase()} ${activity.startsWith("run") ? "run" : activity.startsWith("walk") ? "walk" : "cycle"}`;
}

export function resolveBaseFamily(baseMaps: BaseMapsConfig, workouts: MapSelectionWorkout[]) {
  const available = new Set(baseMaps.families.map((family) => family.id));
  const routedTypes = new Map(
    workouts.map((workout) => [`${workout.type.name}\n${workout.type.key}`, workout.type]),
  );
  const resolvedFamilies = [...routedTypes.values()]
    .map((type) => {
      const exact = baseMaps.workoutTypeMappings.find(
        (mapping) => mapping.normalizedTypeKey === type.key,
      );
      if (exact) return exact.familyId;
      const label = canonicalQualifiedActivityLabel(type.name);
      if (!label) return undefined;
      // Display aliases may select a family, but never rewrite provider identity or break a tie.
      const aliases = new Set(
        baseMaps.workoutTypeMappings
          .filter((mapping) => canonicalQualifiedActivityLabel(mapping.providerLabel) === label)
          .map((mapping) => mapping.familyId),
      );
      return aliases.size === 1 ? [...aliases][0] : undefined;
    })
    .filter((id): id is string => Boolean(id && available.has(id)));
  const mappedFamilies = new Set(resolvedFamilies);
  if (
    routedTypes.size > 0 &&
    resolvedFamilies.length === routedTypes.size &&
    mappedFamilies.size === 1
  )
    return resolvedFamilies[0];
  return available.has(baseMaps.fallbackFamilyId)
    ? baseMaps.fallbackFamilyId
    : (baseMaps.families[0]?.id ?? "");
}

function rangeLabel(range: DateRangePreference) {
  const quick = QUICK_RANGES.find(([value]) => value === range);
  const explicit = EXPLICIT_RANGE.exec(range);
  return (
    quick?.[1] ??
    (explicit ? `${formatDateOnly(explicit[1])} to ${formatDateOnly(explicit[2])}` : "Last 30 days")
  );
}

function formatCoveragePopupDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  return match ? `${Number(match[2])}/${Number(match[3])}/${match[1]}` : value;
}

function formatWorkoutDate(workout: MapSelectionWorkout, preferences: Preferences) {
  if (workout.localStartDate) {
    const [year, month, day] = workout.localStartDate.split("-");
    return `${Number(month)}/${day}/${year}`;
  }
  const instant = new Date(workout.startedAt);
  if (Number.isNaN(instant.getTime())) return "Date unavailable";
  return new Intl.DateTimeFormat("en-US", {
    year: "numeric",
    month: "numeric",
    day: "2-digit",
    timeZone: preferences.timezone,
  }).format(instant);
}

function topmostFeature(features: MapGeoJSONFeature[]) {
  return features.reduce<MapGeoJSONFeature | undefined>((newest, feature) => {
    const order = Number(feature.properties?.sortOrder ?? Number.NEGATIVE_INFINITY);
    const newestOrder = Number(newest?.properties?.sortOrder ?? Number.NEGATIVE_INFINITY);
    return !newest || order > newestOrder ? feature : newest;
  }, undefined);
}

function routeColorIndex(typeKey: string) {
  let hash = 2166136261;
  for (const character of typeKey) hash = Math.imul(hash ^ character.codePointAt(0)!, 16777619);
  return (hash >>> 0) % ROUTE_PALETTE.length;
}

function semanticRouteKind(typeName: string) {
  const normalized = typeName.trim().toLowerCase();
  if (normalized.includes("walk")) return "walk";
  if (normalized.includes("hik")) return "hiking";
  if (normalized.includes("cycl") || normalized.includes("bike")) return "cycling";
  return undefined;
}

export function routeColor(typeKey: string, typeName = "") {
  const semantic = semanticRouteKind(typeName);
  if (semantic) return SEMANTIC_ROUTE_COLORS[semantic];
  return ROUTE_PALETTE[routeColorIndex(typeKey)];
}

export function routeColors(workouts: MapSelectionWorkout[]) {
  return [
    ...new Map(
      workouts.map((workout) => [
        workout.type.key,
        routeColor(workout.type.key, workout.type.name),
      ]),
    ).entries(),
  ];
}

export function routeEndpointOutline(theme: Preferences["theme"]) {
  return theme === "dark" ? "#334155" : "#d1d5db";
}

export function absoluteRouteTileTemplate(template: string, origin: string) {
  return template.startsWith("/") ? `${origin}${template}` : template;
}

export function privateRouteTileUnauthorized(event: unknown) {
  if (!event) return false;
  const tileError = event as { sourceId?: string; error?: { status?: number; url?: string } };
  if (tileError.error?.status !== 401) return false;
  if (tileError.sourceId === ROUTES_SOURCE || tileError.sourceId === COVERAGE_SOURCE) return true;
  try {
    return /^\/api\/map-selections\/[^/]+\/(?:route|coverage)-tiles\/[^/]+\/[^/]+\/[^/]+\/[^/]+\.pbf$/.test(
      new URL(tileError.error.url ?? "", window.location.origin).pathname,
    );
  } catch {
    return false;
  }
}

export function privateRouteTileUnavailable(event: unknown, routeTemplate?: string) {
  if (!event || !routeTemplate) return false;
  const tileError = event as { sourceId?: string; error?: { status?: number; url?: string } };
  if (tileError.error?.status !== 404) return false;
  const prefix = absoluteRouteTileTemplate(routeTemplate, window.location.origin).split("{z}")[0];
  return Boolean(tileError.error.url?.startsWith(prefix));
}

type RouteBounds = NonNullable<MapSelection["bounds"]>;

function mapFitBounds(
  value: { bounds: RouteBounds | null; coverageBounds: RouteBounds | null },
  mode: MapMode,
) {
  return mode === "coverage" ? value.coverageBounds : value.bounds;
}
type RawRoutePoint = { recordedAt: string; longitude: number; latitude: number };
type RawRoutePoints = { points: RawRoutePoint[] };
type LoadedRawRoute = {
  workoutId: string;
  generation: number;
  route?: RawRouteFeature;
  endpoints?: RawRouteEndpoints;
};
type RawRouteFeature = {
  type: "Feature";
  geometry: { type: "MultiLineString"; coordinates: number[][][] };
  properties: Record<string, unknown>;
};
type RawRouteEndpoints = {
  type: "FeatureCollection";
  features: Array<{
    type: "Feature";
    geometry: { type: "Point"; coordinates: number[] };
    properties: { kind: "start" | "finish" };
  }>;
};
type RawRouteDirectionMarkers = {
  type: "FeatureCollection";
  features: Array<{
    type: "Feature";
    geometry: { type: "Point"; coordinates: number[] };
    properties: { bearing: number };
  }>;
};
type CoverageHighlight = {
  key: number;
  geometry: RoadCoverageDetail["geometry"];
  fitBounds: RouteBounds;
};
export type MapMode = "routes" | "coverage";
export type RoadCoverageCache = { key: string; loadedAt: number; entities: RoadCoverageEntity[] };

export class FocusGeometryCache<T> {
  private entries = new Map<string, { value: T; loadedAt: number; bytes: number }>();

  get(key: string) {
    const entry = this.entries.get(key);
    this.entries.delete(key);
    if (!entry || Date.now() - entry.loadedAt >= 5 * 60_000) return;
    this.entries.set(key, entry);
    return entry.value;
  }

  set(key: string, value: T) {
    const bytes = new TextEncoder().encode(JSON.stringify(value)).byteLength;
    this.entries.delete(key);
    if (bytes > 8 * 1024 * 1024) return;
    this.entries.set(key, { value, loadedAt: Date.now(), bytes });
    while (
      this.entries.size > 16 ||
      [...this.entries.values()].reduce((total, entry) => total + entry.bytes, 0) > 8 * 1024 * 1024
    )
      this.entries.delete(this.entries.keys().next().value!);
  }

  clear() {
    this.entries.clear();
  }
}

export class CoverageDetailCache {
  private entries = new Map<
    string,
    { detail: RoadCoverageDetail; bytes: number; loadedAt: number }
  >();

  get(key: string) {
    const entry = this.entries.get(key);
    this.entries.delete(key);
    if (!entry || Date.now() - entry.loadedAt >= 5 * 60_000) return;
    this.entries.set(key, entry);
    return entry.detail;
  }

  set(key: string, detail: RoadCoverageDetail) {
    const bytes = new TextEncoder().encode(JSON.stringify(detail)).byteLength;
    this.entries.delete(key);
    if (bytes > 8 * 1024 * 1024) return;
    this.entries.set(key, { detail, bytes, loadedAt: Date.now() });
    while (
      this.entries.size > 8 ||
      [...this.entries.values()].reduce((total, entry) => total + entry.bytes, 0) > 8 * 1024 * 1024
    )
      this.entries.delete(this.entries.keys().next().value!);
  }

  clear() {
    this.entries.clear();
  }
}

export const RAW_ROUTE_MAX_GAP_METERS = 200;

export function buildSegmentedRawRoute(points: RawRoutePoint[]): RawRouteFeature | undefined {
  if (points.length < 2) return undefined;
  const lines: number[][][] = [];
  let line: number[][] = [[points[0].longitude, points[0].latitude]];
  let positiveDeltaTotal = 0;
  let positiveDeltaCount = 0;
  let priorTime = Date.parse(points[0].recordedAt);
  for (const point of points.slice(1)) {
    const time = Date.parse(point.recordedAt);
    const delta = time - priorTime;
    const startsLine =
      delta > 0 && positiveDeltaCount > 0 && delta >= (3 * positiveDeltaTotal) / positiveDeltaCount;
    if (startsLine) {
      if (line.length >= 2) lines.push(line);
      line = [[point.longitude, point.latitude]];
      positiveDeltaTotal = 0;
      positiveDeltaCount = 0;
    } else {
      line.push([point.longitude, point.latitude]);
      if (delta > 0) {
        positiveDeltaTotal += delta;
        positiveDeltaCount++;
      }
    }
    priorTime = time;
  }
  if (line.length >= 2) lines.push(line);
  const continuous: number[][][] = [];
  // Match Coverage's 200 m spatial limit without reconnecting existing time gaps.
  for (const segment of lines) {
    let part = [segment[0]];
    for (const point of segment.slice(1)) {
      const previous = part[part.length - 1];
      const radians = Math.PI / 180;
      const latitude = Math.sin(((point[1] - previous[1]) * radians) / 2);
      const longitude = Math.sin(((point[0] - previous[0]) * radians) / 2);
      const haversine =
        latitude ** 2 +
        Math.cos(previous[1] * radians) * Math.cos(point[1] * radians) * longitude ** 2;
      const meters = 2 * 6371008.8 * Math.asin(Math.min(1, Math.sqrt(haversine)));
      if (meters > RAW_ROUTE_MAX_GAP_METERS) {
        if (part.length >= 2) continuous.push(part);
        part = [point];
      } else part.push(point);
    }
    if (part.length >= 2) continuous.push(part);
  }
  return continuous.length === 0
    ? undefined
    : {
        type: "Feature",
        geometry: { type: "MultiLineString", coordinates: continuous },
        properties: {},
      };
}

export function buildRawRouteEndpoints(points: RawRoutePoint[]): RawRouteEndpoints | undefined {
  if (points.length < 2) return undefined;
  const endpoint = (point: RawRoutePoint, kind: "start" | "finish") => ({
    type: "Feature" as const,
    geometry: { type: "Point" as const, coordinates: [point.longitude, point.latitude] },
    properties: { kind },
  });
  return {
    type: "FeatureCollection",
    features: [endpoint(points[0], "start"), endpoint(points[points.length - 1], "finish")],
  };
}

export function buildRawRouteHighlight(route: RawRouteFeature, workoutId: string) {
  const lines: number[][][] = [];
  const points: number[][] = [];
  for (const line of route.geometry.coordinates) {
    if (line.every((point) => point[0] === line[0][0] && point[1] === line[0][1]))
      points.push(line[0]);
    else lines.push(line);
  }
  return {
    type: "FeatureCollection" as const,
    features: [
      ...lines.map((coordinates) => ({
        type: "Feature" as const,
        geometry: { type: "LineString" as const, coordinates },
        properties: { workoutId },
      })),
      ...points.map((coordinates) => ({
        type: "Feature" as const,
        geometry: { type: "Point" as const, coordinates },
        properties: { workoutId },
      })),
    ],
  };
}

export function buildRawRouteDirectionMarkers(
  route: RawRouteFeature,
  project: (coordinate: number[]) => { x: number; y: number },
  spacingPixels = 96,
  smoothingPoints = 3,
): RawRouteDirectionMarkers {
  const features: RawRouteDirectionMarkers["features"] = [];
  for (const line of route.geometry.coordinates) {
    if (line.length < 2 * smoothingPoints + 1) continue;
    const projected = line.map(project);
    let distanceSinceMarker = spacingPixels / 2;
    for (let i = 1; i < line.length; i++) {
      distanceSinceMarker += Math.hypot(
        projected[i].x - projected[i - 1].x,
        projected[i].y - projected[i - 1].y,
      );
      if (
        i < smoothingPoints ||
        i + smoothingPoints >= line.length ||
        distanceSinceMarker < spacingPixels
      )
        continue;
      let fromIndex = i - smoothingPoints,
        toIndex = i + smoothingPoints;
      // Dense GPS samples still need a usable screen-space bearing at a fitted zoom.
      while (
        Math.hypot(
          projected[toIndex].x - projected[fromIndex].x,
          projected[toIndex].y - projected[fromIndex].y,
        ) < 4 &&
        (fromIndex > 0 || toIndex < line.length - 1)
      ) {
        fromIndex = Math.max(0, fromIndex - 1);
        toIndex = Math.min(line.length - 1, toIndex + 1);
      }
      const from = projected[fromIndex],
        to = projected[toIndex];
      if (Math.hypot(to.x - from.x, to.y - from.y) < 4) continue;
      features.push({
        type: "Feature",
        geometry: { type: "Point", coordinates: line[i] },
        properties: { bearing: (Math.atan2(to.x - from.x, from.y - to.y) * 180) / Math.PI },
      });
      distanceSinceMarker = 0;
    }
  }
  return { type: "FeatureCollection", features };
}

export function pointyDirectionMarkerImage() {
  const width = 11,
    height = 15,
    data = new Uint8Array(width * height * 4);
  const center = (width - 1) / 2;
  for (let y = 1; y < height - 1; y++) {
    const halfWidth = 0.35 + ((y - 1) * 4.15) / (height - 3);
    for (let x = 0; x < width; x++) {
      if (Math.abs(x - center) > halfWidth) continue;
      const offset = 4 * (y * width + x);
      data[offset] = 50;
      data[offset + 1] = 213;
      data[offset + 2] = 131;
      data[offset + 3] = 255;
    }
  }
  return { width, height, data };
}

function diagnosticBounds(overlay: CoverageDiagnosticEvidenceCollection): RouteBounds | undefined {
  const coordinates = overlay.features.flatMap((feature) => feature.geometry.coordinates);
  const valid = coordinates.filter(
    (coordinate) =>
      coordinate.length >= 2 && Number.isFinite(coordinate[0]) && Number.isFinite(coordinate[1]),
  );
  if (!valid.length) return undefined;
  return {
    minimumLongitude: Math.min(...valid.map((coordinate) => coordinate[0])),
    minimumLatitude: Math.min(...valid.map((coordinate) => coordinate[1])),
    maximumLongitude: Math.max(...valid.map((coordinate) => coordinate[0])),
    maximumLatitude: Math.max(...valid.map((coordinate) => coordinate[1])),
  };
}

function routePopupParts(value: string, preferences: Preferences) {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: preferences.timezone,
    year: "numeric",
    month: "numeric",
    day: "2-digit",
    hour: "numeric",
    minute: "2-digit",
    hour12: preferences.clockFormat === "12h",
  }).formatToParts(new Date(value));
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((item) => item.type === type)?.value ?? "";
  return {
    date: `${part("month")}/${part("day")}/${part("year")}`,
    time: `${part("hour").replace(/^0/, "")}:${part("minute")}${preferences.clockFormat === "12h" ? part("dayPeriod").slice(0, 1).toLowerCase() : ""}`,
  };
}

export function formatRoutePopupDetails(
  startedAt: string,
  endedAt: string,
  preferences: Preferences,
) {
  const start = routePopupParts(startedAt, preferences);
  const end = routePopupParts(endedAt, preferences);
  return { date: start.date, timeRange: `${start.time} - ${end.time}` };
}

export function formatRoutePopupDistance(
  workout: MapSelectionWorkout,
  units: Preferences["units"],
) {
  if (!workout.distance) return "Distance unavailable";
  let value = Number(workout.distance.value);
  let unit = workout.distance.unit;
  if (units === "imperial" && unit === "km") {
    value *= 0.621371192;
    unit = "mi";
  }
  return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(value)} ${unit}`;
}

function MapCanvas({
  family,
  preferences,
  selection,
  coverageFocus,
  workouts,
  fitPadding,
  hoveredWorkoutId,
  fitRequest,
  focusRequest,
  coverageHighlight,
  diagnosticEnabled,
  productionCoverageEnabled,
  diagnosticMode,
  routeHidingEnabled,
  nonFocusedRoutesHidden,
  rawRouteHidden,
  nonFocusedCoverageHidden,
  diagnosticRawRoute,
  directionRoute,
  rawHighlightRoute,
  rawRouteEndpoints,
  rawRouteEndpointsVisible,
  diagnostic,
  highlightedPortionOrdinal,
  onDiagnosticHover,
  onDiagnosticLock,
  onHover,
  onRouteClick,
  onBaseMapError,
  onBaseMapReady,
  onRouteTilesUnavailable,
}: {
  family: BaseMapFamily;
  preferences: Preferences;
  selection?: MapSelection;
  workouts: MapSelectionWorkout[];
  coverageFocus?: CoverageFocusFeatureCollection;
  fitPadding: number;
  hoveredWorkoutId?: string;
  fitRequest?: { key: number; bounds: RouteBounds; focusCanvas?: boolean };
  focusRequest: number;
  coverageHighlight?: CoverageHighlight;
  diagnosticEnabled: boolean;
  productionCoverageEnabled: boolean;
  diagnosticMode: boolean;
  routeHidingEnabled: boolean;
  nonFocusedRoutesHidden: boolean;
  rawRouteHidden: boolean;
  nonFocusedCoverageHidden: boolean;
  diagnosticRawRoute?: RawRouteFeature;
  directionRoute?: RawRouteFeature;
  rawHighlightRoute?: RawRouteFeature;
  rawRouteEndpoints?: RawRouteEndpoints;
  rawRouteEndpointsVisible: boolean;
  diagnostic?: CoverageDiagnosticRun;
  highlightedPortionOrdinal?: number;
  onDiagnosticHover: (ordinal?: number) => void;
  onDiagnosticLock: (ordinal: number) => void;
  onHover: (id?: string) => void;
  onRouteClick: (id: string) => void;
  onBaseMapError: () => void;
  onBaseMapReady: () => void;
  onRouteTilesUnavailable: () => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | undefined>(undefined);
  const selectionRef = useRef(selection);
  const routeUrlRef = useRef(selection?.routeTileUrl);
  const coverageUrlRef = useRef(selection?.coverageTileUrl);
  const coverageFocusRef = useRef<CoverageFocusFeatureCollection>(
    coverageFocus ?? { type: "FeatureCollection", features: [] },
  );
  const installedRouteUrlRef = useRef<string | undefined>(undefined);
  const installedCoverageUrlRef = useRef<string | undefined>(undefined);
  const privateTilesRef = useRef<PrivateTiles | undefined>(undefined);
  const installedCoverageFocusRef = useRef<CoverageFocusFeatureCollection | undefined>(undefined);
  const onRouteTilesUnavailableRef = useRef(onRouteTilesUnavailable);
  const onRouteClickRef = useRef(onRouteClick);
  const hoverRef = useRef(hoveredWorkoutId);
  const workoutsRef = useRef(workouts);
  const preferencesRef = useRef(preferences);
  const styleUrlRef = useRef(family.styles[preferences.theme]);
  const routeColorsRef = useRef(routeColors(selection?.workouts ?? []));
  const diagnosticEnabledRef = useRef(diagnosticEnabled);
  const productionCoverageEnabledRef = useRef(productionCoverageEnabled);
  const diagnosticModeRef = useRef(diagnosticMode);
  const rawRouteHiddenRef = useRef(rawRouteHidden);
  const nonFocusedRoutesHiddenRef = useRef(nonFocusedRoutesHidden);
  const nonFocusedCoverageHiddenRef = useRef(nonFocusedCoverageHidden);
  const diagnosticRawRouteRef = useRef(diagnosticRawRoute);
  const directionRouteRef = useRef(directionRoute);
  const rawHighlightRouteRef = useRef(rawHighlightRoute);
  const rawHighlightUpdateRef = useRef<
    { source: GeoJSONSource; route: RawRouteFeature; ready: boolean; revealed: boolean } | undefined
  >(undefined);
  const rawFallbackFiltersRef = useRef<{
    workoutId?: string;
    line?: ReturnType<MapLibreMap["getLayer"]>;
    markers?: ReturnType<MapLibreMap["getLayer"]>;
  }>({});
  const directionStateRef = useRef<{
    moving: boolean;
    update?: {
      source: GeoJSONSource;
      route: RawRouteFeature;
      projectionKey: string;
      viewportReady: boolean;
      status: "worker" | "tiles" | "ready";
    };
  }>({ moving: false });
  const installedDiagnosticRawRouteRef = useRef<RawRouteFeature | undefined>(undefined);
  const coverageHandoffTimerRef = useRef<number | undefined>(undefined);
  const coverageHandoffCompleteRef = useRef(false);
  const rawRouteEndpointsRef = useRef(rawRouteEndpoints);
  const rawRouteEndpointsVisibleRef = useRef(rawRouteEndpointsVisible);
  const installedRawRouteEndpointsRef = useRef<RawRouteEndpoints | undefined>(undefined);
  const diagnosticRef = useRef(diagnostic);
  const highlightedPortionRef = useRef(highlightedPortionOrdinal);
  const coverageHighlightRef = useRef(coverageHighlight);
  const installedCoverageHighlightRef = useRef<
    | {
        source: GeoJSONSource;
        geometry: RoadCoverageDetail["geometry"];
      }
    | undefined
  >(undefined);
  const startedCoverageHighlightKeyRef = useRef<number | undefined>(undefined);
  const styleLoadedRef = useRef(false);
  const fallbackInstalledRef = useRef(false);
  const baseMapRetryTimerRef = useRef<number | undefined>(undefined);
  const defaultViewportSetRef = useRef(false);
  const popupRef = useRef<maplibregl.Popup | undefined>(undefined);
  const popupTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const popupWorkoutRef = useRef<string | undefined>(undefined);
  const popupLocationRef = useRef<maplibregl.LngLat | undefined>(undefined);
  const coveragePopupAbortRef = useRef<AbortController | undefined>(undefined);
  const coveragePopupEntityRef = useRef<string | undefined>(undefined);
  const coverageHoverContext = JSON.stringify([
    selection?.id,
    selection?.dataGeneration,
    selection?.coverageDisplayPolicy,
    selection?.coverageDisplayRevision,
    selection?.range.startDate,
    selection?.range.endDate,
  ]);
  const coverageHoverContextRef = useRef(coverageHoverContext);
  const coverageHoverCacheRef = useRef(
    new Map<string, { entity: RoadCoverageEntity; loadedAt: number }>(),
  );
  const coverageHoverRequestsRef = useRef(
    new Map<
      string,
      {
        controller: AbortController;
        result: Promise<{ entity: RoadCoverageEntity; loadedAt: number }>;
      }
    >(),
  );
  const stopCoverageBlinkRef = useRef<() => void>(() => undefined);
  const fallbackStyleRef = useRef({
    version: 8 as const,
    sources: {},
    layers: [
      {
        id: "fallback-background",
        type: "background" as const,
        paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" },
      },
    ],
  });

  function cancelPrivateTilesForStyle() {
    privateTilesRef.current?.reset();
    // Preserved vector sources must get new identities after their pending loads were aborted.
    installedRouteUrlRef.current = undefined;
    installedCoverageUrlRef.current = undefined;
  }

  function syncPrivateLayers(map: MapLibreMap) {
    if (!routeUrlRef.current) {
      privateTilesRef.current?.removeSource(ROUTES_SOURCE);
      privateTilesRef.current?.removeSource(COVERAGE_SOURCE);
      for (const layer of [...ROUTE_LAYERS, ...COVERAGE_LAYERS].reverse())
        if (map.getLayer(layer)) map.removeLayer(layer);
      for (const source of [
        ROUTES_SOURCE,
        RAW_FOCUS_SOURCE,
        COVERAGE_SOURCE,
        COVERAGE_FOCUS_SOURCE,
        ROUTE_ENDPOINTS_SOURCE,
      ])
        if (map.getSource(source)) map.removeSource(source);
      installedRouteUrlRef.current = undefined;
      installedCoverageUrlRef.current = undefined;
      installedRawRouteEndpointsRef.current = undefined;
      return;
    }
    const absoluteURL = absoluteRouteTileTemplate(routeUrlRef.current, window.location.origin);
    let existingSource = map.getSource(ROUTES_SOURCE) as VectorTileSource | undefined;
    if (existingSource && installedRouteUrlRef.current !== absoluteURL) {
      if (map.getLayer(HOVER_MARKERS_LAYER)) map.removeLayer(HOVER_MARKERS_LAYER);
      if (map.getLayer(HOVER_LAYER)) map.removeLayer(HOVER_LAYER);
      if (map.getLayer(ROUTE_MARKERS_LAYER)) map.removeLayer(ROUTE_MARKERS_LAYER);
      if (map.getLayer(ROUTES_LAYER)) map.removeLayer(ROUTES_LAYER);
      map.removeSource(ROUTES_SOURCE);
      installedRouteUrlRef.current = undefined;
      existingSource = undefined;
    }
    if (!existingSource)
      map.addSource(ROUTES_SOURCE, {
        type: "vector",
        tiles: [privateTilesRef.current!.setSource(ROUTES_SOURCE, absoluteURL, true)],
      });
    installedRouteUrlRef.current = absoluteURL;
    const colorExpression: unknown[] = ["match", ["get", "workoutTypeKey"]];
    for (const [typeKey, color] of routeColorsRef.current) colorExpression.push(typeKey, color);
    colorExpression.push("#e9a852");
    if (!map.getLayer(ROUTES_LAYER))
      map.addLayer({
        id: ROUTES_LAYER,
        type: "line",
        source: ROUTES_SOURCE,
        "source-layer": "routes",
        layout: {
          "line-cap": "round",
          "line-join": "round",
          "line-sort-key": ["get", "sortOrder"],
        },
        paint: {
          "line-color": colorExpression as never,
          "line-width": ["interpolate", ["linear"], ["zoom"], 5, 2, 14, 4],
          "line-opacity": nonFocusedRoutesHiddenRef.current ? 0 : 0.82,
          "line-opacity-transition": RAW_ROUTE_FADE,
        },
      });
    else {
      map.setPaintProperty(ROUTES_LAYER, "line-color", colorExpression as never);
      map.setPaintProperty(
        ROUTES_LAYER,
        "line-opacity",
        nonFocusedRoutesHiddenRef.current ? 0 : 0.82,
      );
    }
    if (!map.getLayer(ROUTE_MARKERS_LAYER))
      map.addLayer({
        id: ROUTE_MARKERS_LAYER,
        type: "circle",
        source: ROUTES_SOURCE,
        "source-layer": "routes",
        filter: ["==", ["geometry-type"], "Point"],
        layout: { "circle-sort-key": ["get", "sortOrder"] },
        paint: {
          "circle-color": colorExpression as never,
          "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 3, 14, 5],
          "circle-opacity": nonFocusedRoutesHiddenRef.current ? 0 : 0.9,
          "circle-opacity-transition": RAW_ROUTE_FADE,
        },
      });
    else {
      map.setPaintProperty(ROUTE_MARKERS_LAYER, "circle-color", colorExpression as never);
      map.setPaintProperty(
        ROUTE_MARKERS_LAYER,
        "circle-opacity",
        nonFocusedRoutesHiddenRef.current ? 0 : 0.9,
      );
    }
    if (coverageUrlRef.current && productionCoverageEnabledRef.current) {
      const coverageURL = absoluteRouteTileTemplate(coverageUrlRef.current, window.location.origin);
      let coverageSource = map.getSource(COVERAGE_SOURCE);
      if (coverageSource && installedCoverageUrlRef.current !== coverageURL) {
        for (const layer of [...NON_FOCUSED_COVERAGE_LAYERS].reverse())
          if (map.getLayer(layer)) map.removeLayer(layer);
        map.removeSource(COVERAGE_SOURCE);
        coverageSource = undefined;
      }
      if (!coverageSource)
        map.addSource(COVERAGE_SOURCE, {
          type: "vector",
          tiles: [privateTilesRef.current!.setSource(COVERAGE_SOURCE, coverageURL, true)],
          minzoom: 0,
          maxzoom: 22,
        });
      installedCoverageUrlRef.current = coverageURL;
      const visibility =
        diagnosticModeRef.current && productionCoverageEnabledRef.current ? "visible" : "none";
      const aggregateDefinitions = [
        {
          id: COVERAGE_LAYER,
          sourceLayer: "coverage",
          colors: COVERAGE_RANGE_COLORS,
          width: [2, 5],
        },
        {
          id: COVERAGE_PARK_LAYER,
          sourceLayer: "coverage_parks",
          colors: COVERAGE_RANGE_COLORS,
          width: [3, 6],
        },
      ] as const;
      for (const definition of aggregateDefinitions) {
        if (!map.getLayer(definition.id))
          map.addLayer({
            id: definition.id,
            type: "line",
            source: COVERAGE_SOURCE,
            "source-layer": definition.sourceLayer,
            layout: { visibility, "line-cap": "round", "line-join": "round" },
            paint: {
              "line-color": coverageColorExpression(definition.colors),
              "line-width": [
                "interpolate",
                ["linear"],
                ["zoom"],
                5,
                definition.width[0],
                14,
                definition.width[1],
              ],
              "line-opacity": nonFocusedCoverageHiddenRef.current ? 0 : 0.94,
              "line-opacity-transition": RAW_ROUTE_FADE,
            },
          });
        else {
          map.setLayoutProperty(definition.id, "visibility", visibility);
          map.setPaintProperty(
            definition.id,
            "line-color",
            coverageColorExpression(definition.colors),
          );
          map.setPaintProperty(
            definition.id,
            "line-opacity",
            nonFocusedCoverageHiddenRef.current ? 0 : 0.94,
          );
        }
      }
      const focusSource = map.getSource(COVERAGE_FOCUS_SOURCE) as GeoJSONSource | undefined;
      if (!focusSource)
        map.addSource(COVERAGE_FOCUS_SOURCE, {
          type: "geojson",
          data: coverageFocusRef.current,
          tolerance: 0,
          maxzoom: 22,
        });
      else if (installedCoverageFocusRef.current !== coverageFocusRef.current)
        focusSource.setData(coverageFocusRef.current as never);
      installedCoverageFocusRef.current = coverageFocusRef.current;
      const focusDefinitions = [
        {
          id: COVERAGE_FOCUS_LAYER,
          entityKind: "path",
          colors: COVERAGE_FOCUS_COLORS,
          width: [4, 8],
        },
        {
          id: COVERAGE_PARK_FOCUS_LAYER,
          entityKind: "park",
          colors: COVERAGE_FOCUS_COLORS,
          width: [5, 9],
        },
      ] as const;
      for (const definition of focusDefinitions) {
        if (!map.getLayer(definition.id))
          map.addLayer({
            id: definition.id,
            type: "line",
            source: COVERAGE_FOCUS_SOURCE,
            filter: ["==", ["get", "sourceEntityKind"], definition.entityKind],
            layout: { visibility, "line-cap": "round", "line-join": "round" },
            paint: {
              "line-color": coverageColorExpression(definition.colors),
              "line-width": [
                "interpolate",
                ["linear"],
                ["zoom"],
                5,
                definition.width[0],
                14,
                definition.width[1],
              ],
              "line-opacity": 0.94,
              "line-opacity-transition": RAW_ROUTE_FADE,
            },
          });
        else {
          map.setLayoutProperty(definition.id, "visibility", visibility);
          map.setPaintProperty(
            definition.id,
            "line-color",
            coverageColorExpression(definition.colors),
          );
          map.setPaintProperty(definition.id, "line-opacity", 0.94);
        }
      }
      for (const definition of focusDefinitions)
        if (map.getLayer(definition.id)) map.moveLayer(definition.id);
    }
    if (!map.getLayer(HOVER_LAYER))
      map.addLayer({
        id: HOVER_LAYER,
        type: "line",
        source: ROUTES_SOURCE,
        "source-layer": "routes",
        filter: ["==", ["get", "workoutId"], hoverRef.current ?? ""],
        layout: { "line-cap": "round", "line-join": "round" },
        paint: {
          "line-color": "#c026ff",
          "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7],
          "line-opacity": 1,
        },
      });
    if (!map.getLayer(HOVER_MARKERS_LAYER))
      map.addLayer({
        id: HOVER_MARKERS_LAYER,
        type: "circle",
        source: ROUTES_SOURCE,
        "source-layer": "routes",
        filter: [
          "all",
          ["==", ["geometry-type"], "Point"],
          ["==", ["get", "workoutId"], hoverRef.current ?? ""],
        ],
        layout: { "circle-sort-key": ["get", "sortOrder"] },
        paint: {
          "circle-color": "#c026ff",
          "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 9],
          "circle-opacity": 1,
        },
      });
    const endpointSource = map.getSource(ROUTE_ENDPOINTS_SOURCE) as GeoJSONSource | undefined;
    const endpointOutline = routeEndpointOutline(preferencesRef.current.theme);
    if (!rawRouteEndpointsRef.current) {
      if (map.getLayer(ROUTE_FINISH_LAYER)) map.removeLayer(ROUTE_FINISH_LAYER);
      if (map.getLayer(ROUTE_START_LAYER)) map.removeLayer(ROUTE_START_LAYER);
      if (endpointSource) map.removeSource(ROUTE_ENDPOINTS_SOURCE);
      installedRawRouteEndpointsRef.current = undefined;
      return;
    }
    if (!endpointSource)
      map.addSource(ROUTE_ENDPOINTS_SOURCE, {
        type: "geojson",
        data: rawRouteEndpointsRef.current,
      });
    else if (installedRawRouteEndpointsRef.current !== rawRouteEndpointsRef.current)
      endpointSource.setData(rawRouteEndpointsRef.current);
    installedRawRouteEndpointsRef.current = rawRouteEndpointsRef.current;
    if (!map.getLayer(ROUTE_START_LAYER))
      map.addLayer({
        id: ROUTE_START_LAYER,
        type: "circle",
        source: ROUTE_ENDPOINTS_SOURCE,
        filter: ["==", ["get", "kind"], "start"],
        layout: { visibility: "visible" },
        paint: {
          "circle-color": "#20a464",
          "circle-radius": ROUTE_START_MARKER_RADIUS,
          "circle-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0,
          "circle-opacity-transition": RAW_ROUTE_FADE,
          "circle-stroke-color": endpointOutline,
          "circle-stroke-width": 2,
          "circle-stroke-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0,
          "circle-stroke-opacity-transition": RAW_ROUTE_FADE,
        },
      });
    else map.setPaintProperty(ROUTE_START_LAYER, "circle-stroke-color", endpointOutline);
    if (!map.getLayer(ROUTE_FINISH_LAYER))
      map.addLayer({
        id: ROUTE_FINISH_LAYER,
        type: "symbol",
        source: ROUTE_ENDPOINTS_SOURCE,
        filter: ["==", ["get", "kind"], "finish"],
        layout: {
          visibility: "visible",
          "text-field": "■",
          "text-size": ROUTE_FINISH_MARKER_SIZE,
          "text-allow-overlap": true,
          "text-ignore-placement": true,
        },
        paint: {
          "text-color": "#d3423f",
          "text-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0,
          "text-opacity-transition": RAW_ROUTE_FADE,
          "text-halo-color": endpointOutline,
          "text-halo-width": 1.5,
        },
      });
    else map.setPaintProperty(ROUTE_FINISH_LAYER, "text-halo-color", endpointOutline);
  }

  function invalidateDirectionMarkers(map: MapLibreMap) {
    directionStateRef.current.update = undefined;
    if (!map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) return;
    // Keep the source used so its worker and tile buckets can reload while hidden.
    map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity-transition", {
      duration: 0,
      delay: 0,
    });
    map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity", 0);
  }

  function rawHighlightReady(map: MapLibreMap) {
    const update = rawHighlightUpdateRef.current;
    if (
      !update?.ready ||
      update.route !== rawHighlightRouteRef.current ||
      update.source !== map.getSource(RAW_FOCUS_SOURCE)
    )
      return false;
    // New viewport tiles must not hide geometry that completed its initial handoff.
    if (!update.revealed && map.isSourceLoaded(RAW_FOCUS_SOURCE)) update.revealed = true;
    return update.revealed;
  }

  function syncRawHighlightVisibility(map: MapLibreMap) {
    if (!styleLoadedRef.current) return;
    const ready = rawHighlightReady(map);
    const opacity = ready && !diagnosticModeRef.current ? 1 : 0;
    for (const [layer, property] of [
      [RAW_FOCUS_LAYER, "line-opacity"],
      [RAW_FOCUS_MARKERS_LAYER, "circle-opacity"],
    ])
      if (map.getLayer(layer) && map.getPaintProperty(layer, property) !== opacity)
        map.setPaintProperty(layer, property, opacity);
    const fallbackId = ready ? "" : (hoverRef.current ?? "");
    const line = map.getLayer(HOVER_LAYER);
    const markers = map.getLayer(HOVER_MARKERS_LAYER);
    const installed = rawFallbackFiltersRef.current;
    if (line && (installed.line !== line || installed.workoutId !== fallbackId))
      map.setFilter(HOVER_LAYER, ["==", ["get", "workoutId"], fallbackId]);
    if (markers && (installed.markers !== markers || installed.workoutId !== fallbackId))
      map.setFilter(HOVER_MARKERS_LAYER, [
        "all",
        ["==", ["geometry-type"], "Point"],
        ["==", ["get", "workoutId"], fallbackId],
      ]);
    rawFallbackFiltersRef.current = { workoutId: fallbackId, line, markers };
  }

  function syncRawHighlight(map: MapLibreMap) {
    const route = rawHighlightRouteRef.current;
    let source = map.getSource(RAW_FOCUS_SOURCE) as GeoJSONSource | undefined;
    if (!route) {
      rawHighlightUpdateRef.current = undefined;
      for (const layer of [RAW_FOCUS_MARKERS_LAYER, RAW_FOCUS_LAYER])
        if (map.getLayer(layer)) map.removeLayer(layer);
      if (source) map.removeSource(RAW_FOCUS_SOURCE);
      syncRawHighlightVisibility(map);
      return;
    }
    if (!source) {
      map.addSource(RAW_FOCUS_SOURCE, {
        type: "geojson",
        data: { type: "FeatureCollection", features: [] },
        tolerance: 0,
        maxzoom: 22,
      });
      source = map.getSource(RAW_FOCUS_SOURCE) as GeoJSONSource;
    }
    if (!map.getLayer(RAW_FOCUS_LAYER))
      map.addLayer({
        id: RAW_FOCUS_LAYER,
        type: "line",
        source: RAW_FOCUS_SOURCE,
        filter: ["==", ["geometry-type"], "LineString"],
        layout: { "line-cap": "round", "line-join": "round" },
        paint: {
          "line-color": "#c026ff",
          "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7],
          "line-opacity": 0,
        },
      });
    if (!map.getLayer(RAW_FOCUS_MARKERS_LAYER))
      map.addLayer({
        id: RAW_FOCUS_MARKERS_LAYER,
        type: "circle",
        source: RAW_FOCUS_SOURCE,
        filter: ["==", ["geometry-type"], "Point"],
        paint: {
          "circle-color": "#c026ff",
          "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 9],
          "circle-opacity": 0,
        },
      });
    const installed = rawHighlightUpdateRef.current;
    if (installed?.source !== source || installed.route !== route) {
      const update = { source, route, ready: false, revealed: false };
      rawHighlightUpdateRef.current = update;
      void source
        .setData(buildRawRouteHighlight(route, hoverRef.current ?? ""), true)
        .then(() => {
          if (mapRef.current !== map || rawHighlightUpdateRef.current !== update) return;
          update.ready = true;
          syncRawHighlightVisibility(map);
          syncDirectionVisibility(map);
        })
        .catch(() => undefined);
    }
    map.moveLayer(RAW_FOCUS_LAYER);
    map.moveLayer(RAW_FOCUS_MARKERS_LAYER);
    syncRawHighlightVisibility(map);
  }

  function directionProjectionKey(map: MapLibreMap) {
    const canvas = map.getCanvas();
    return JSON.stringify([
      map.getZoom(),
      map.getBearing(),
      map.getPitch(),
      canvas.clientWidth,
      canvas.clientHeight,
      map.getProjection(),
    ]);
  }

  function syncDirectionVisibility(map: MapLibreMap) {
    const { moving, update } = directionStateRef.current;
    if (
      mapRef.current !== map ||
      !styleLoadedRef.current ||
      !update ||
      update.route !== directionRouteRef.current ||
      update.source !== map.getSource(DIAGNOSTIC_DIRECTION_SOURCE) ||
      !map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)
    )
      return;
    if (update.projectionKey !== directionProjectionKey(map)) {
      invalidateDirectionMarkers(map);
      if (!moving && !map.isMoving()) syncDirectionMarkers(map);
      return;
    }
    if (!update.viewportReady) return;
    if ((moving || map.isMoving()) && update.status !== "ready") return;
    const movingReady = (moving || map.isMoving()) && update.status === "ready";
    const routeSource = diagnosticModeRef.current ? DIAGNOSTIC_RAW_ROUTE_SOURCE : RAW_FOCUS_SOURCE;
    const routeReady = diagnosticModeRef.current
      ? movingReady || Boolean(map.getSource(routeSource) && map.isSourceLoaded(routeSource))
      : rawHighlightReady(map) && (update.status === "ready" || map.isSourceLoaded(routeSource));
    if (update.status === "tiles" && map.isSourceLoaded(DIAGNOSTIC_DIRECTION_SOURCE) && routeReady)
      update.status = "ready";
    const ready = update.status === "ready" && routeReady;
    const visible = diagnosticModeRef.current
      ? Boolean(diagnosticRawRouteRef.current) && !rawRouteHiddenRef.current
      : true;
    const transition =
      ready && diagnosticModeRef.current && diagnosticEnabledRef.current
        ? RAW_ROUTE_FADE
        : { duration: 0, delay: 0 };
    const priorTransition = map.getPaintProperty(
      DIAGNOSTIC_DIRECTION_LAYER,
      "icon-opacity-transition",
    ) as typeof transition | undefined;
    if (
      priorTransition?.duration !== transition.duration ||
      priorTransition.delay !== transition.delay
    )
      map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity-transition", transition);
    const opacity = ready && visible ? 0.95 : 0;
    if (map.getPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity") !== opacity)
      map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity", opacity);
  }

  function syncDirectionMarkers(map: MapLibreMap) {
    if (mapRef.current !== map || !styleLoadedRef.current) return;
    let source = map.getSource(DIAGNOSTIC_DIRECTION_SOURCE) as GeoJSONSource | undefined;
    const route = directionRouteRef.current;
    if (!route) {
      directionStateRef.current.update = undefined;
      if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.removeLayer(DIAGNOSTIC_DIRECTION_LAYER);
      if (source) map.removeSource(DIAGNOSTIC_DIRECTION_SOURCE);
      return;
    }
    if (!map.hasImage(DIAGNOSTIC_DIRECTION_IMAGE))
      map.addImage(DIAGNOSTIC_DIRECTION_IMAGE, pointyDirectionMarkerImage());
    if (!source) {
      map.addSource(DIAGNOSTIC_DIRECTION_SOURCE, {
        type: "geojson",
        data: { type: "FeatureCollection", features: [] },
      });
      source = map.getSource(DIAGNOSTIC_DIRECTION_SOURCE) as GeoJSONSource;
    }
    if (!map.getLayer(DIAGNOSTIC_DIRECTION_LAYER))
      map.addLayer({
        id: DIAGNOSTIC_DIRECTION_LAYER,
        type: "symbol",
        source: DIAGNOSTIC_DIRECTION_SOURCE,
        layout: {
          visibility: "visible",
          "icon-image": DIAGNOSTIC_DIRECTION_IMAGE,
          "icon-rotate": ["get", "bearing"],
          "icon-rotation-alignment": "viewport",
          "icon-allow-overlap": true,
          "icon-ignore-placement": true,
        },
        paint: {
          "icon-opacity": 0,
          "icon-opacity-transition": { duration: 0, delay: 0 },
        },
      });
    const installed = directionStateRef.current.update;
    const projectionKey = directionProjectionKey(map);
    if (directionStateRef.current.moving || map.isMoving()) {
      if (
        installed?.source !== source ||
        installed.route !== route ||
        installed.status !== "ready" ||
        installed.projectionKey !== projectionKey
      )
        invalidateDirectionMarkers(map);
      else syncDirectionVisibility(map);
      return;
    }
    if (
      installed?.source === source &&
      installed.route === route &&
      installed.projectionKey === projectionKey
    ) {
      syncDirectionVisibility(map);
      return;
    }
    invalidateDirectionMarkers(map);
    const update = {
      source,
      route,
      projectionKey,
      viewportReady: false,
      status: "worker" as "worker" | "tiles" | "ready",
    };
    directionStateRef.current.update = update;
    const data = buildRawRouteDirectionMarkers(route, (coordinate) =>
      map.project(coordinate as [number, number]),
    );
    void source
      .setData(data, true)
      .then(() => {
        // Update identity invalidates completions from earlier cameras, routes, or styles.
        if (directionStateRef.current.update !== update) return;
        update.status = "tiles";
        syncDirectionVisibility(map);
      })
      .catch(() => undefined);
  }

  function syncDiagnosticLayers(map: MapLibreMap) {
    const hasMemoryRoute = Boolean(diagnosticRawRouteRef.current);
    const useMemoryRoute = diagnosticModeRef.current && hasMemoryRoute;
    if (hasMemoryRoute) {
      const rawSource = map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE) as GeoJSONSource | undefined;
      const routeChanged = installedDiagnosticRawRouteRef.current !== diagnosticRawRouteRef.current;
      if (routeChanged) {
        if (coverageHandoffTimerRef.current !== undefined)
          window.clearTimeout(coverageHandoffTimerRef.current);
        coverageHandoffTimerRef.current = undefined;
        coverageHandoffCompleteRef.current = false;
      }
      if (!rawSource)
        map.addSource(DIAGNOSTIC_RAW_ROUTE_SOURCE, {
          type: "geojson",
          data: diagnosticRawRouteRef.current,
        });
      else if (routeChanged) rawSource.setData(diagnosticRawRouteRef.current);
      installedDiagnosticRawRouteRef.current = diagnosticRawRouteRef.current;
      if (!map.getLayer(DIAGNOSTIC_RAW_ROUTE_LAYER))
        map.addLayer({
          id: DIAGNOSTIC_RAW_ROUTE_LAYER,
          type: "line",
          source: DIAGNOSTIC_RAW_ROUTE_SOURCE,
          layout: { visibility: "visible", "line-cap": "round", "line-join": "round" },
          paint: {
            "line-color": "#c026ff",
            "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7],
            "line-opacity": useMemoryRoute && !rawRouteHiddenRef.current ? 1 : 0,
            "line-opacity-transition": RAW_ROUTE_FADE,
          },
        });
      map.setPaintProperty(
        DIAGNOSTIC_RAW_ROUTE_LAYER,
        "line-opacity",
        useMemoryRoute && !rawRouteHiddenRef.current ? 1 : 0,
      );
    } else {
      if (map.getLayer(DIAGNOSTIC_RAW_ROUTE_LAYER)) map.removeLayer(DIAGNOSTIC_RAW_ROUTE_LAYER);
      if (map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE)) map.removeSource(DIAGNOSTIC_RAW_ROUTE_SOURCE);
      installedDiagnosticRawRouteRef.current = undefined;
    }
    syncDirectionMarkers(map);
    const endpointOpacity =
      rawRouteEndpointsVisibleRef.current &&
      (!diagnosticModeRef.current || !rawRouteHiddenRef.current)
        ? 1
        : 0;
    if (map.getLayer(ROUTE_START_LAYER)) {
      map.setPaintProperty(ROUTE_START_LAYER, "circle-opacity", endpointOpacity);
      map.setPaintProperty(ROUTE_START_LAYER, "circle-stroke-opacity", endpointOpacity);
    }
    if (map.getLayer(ROUTE_FINISH_LAYER))
      map.setPaintProperty(ROUTE_FINISH_LAYER, "text-opacity", endpointOpacity);
    const setVectorVisibility = (visibility: "visible" | "none") => {
      for (const layer of [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER])
        if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", visibility);
    };
    if (useMemoryRoute) {
      if (coverageHandoffCompleteRef.current) setVectorVisibility("none");
      else {
        setVectorVisibility("visible");
        if (coverageHandoffTimerRef.current === undefined) {
          const route = diagnosticRawRouteRef.current;
          coverageHandoffTimerRef.current = window.setTimeout(() => {
            coverageHandoffTimerRef.current = undefined;
            if (!diagnosticModeRef.current || diagnosticRawRouteRef.current !== route) return;
            coverageHandoffCompleteRef.current = true;
            setVectorVisibility("none");
          }, RAW_ROUTE_FADE.duration);
        }
      }
    } else {
      if (coverageHandoffTimerRef.current !== undefined)
        window.clearTimeout(coverageHandoffTimerRef.current);
      coverageHandoffTimerRef.current = undefined;
      coverageHandoffCompleteRef.current = false;
      for (const layer of [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER])
        if (map.getLayer(layer))
          map.setLayoutProperty(
            layer,
            "visibility",
            diagnosticModeRef.current ? "none" : "visible",
          );
    }
    for (const layer of COVERAGE_LAYERS)
      if (map.getLayer(layer))
        map.setLayoutProperty(
          layer,
          "visibility",
          diagnosticModeRef.current && productionCoverageEnabledRef.current ? "visible" : "none",
        );
    if (!diagnosticEnabledRef.current || !diagnosticRef.current) {
      for (const layer of [...DIAGNOSTIC_EVIDENCE_LAYERS].reverse())
        if (map.getLayer(layer)) map.removeLayer(layer);
      if (map.getSource(DIAGNOSTIC_SOURCE)) map.removeSource(DIAGNOSTIC_SOURCE);
      if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.moveLayer(DIAGNOSTIC_DIRECTION_LAYER);
      for (const layer of [ROUTE_FINISH_LAYER, ROUTE_START_LAYER])
        if (map.getLayer(layer)) map.moveLayer(layer);
      return;
    }
    const data = diagnosticRef.current.overlay;
    const source = map.getSource(DIAGNOSTIC_SOURCE) as GeoJSONSource | undefined;
    if (!source) map.addSource(DIAGNOSTIC_SOURCE, { type: "geojson", data });
    else source.setData(data);
    const visibility = diagnosticModeRef.current ? "visible" : "none";
    if (!map.getLayer(DIAGNOSTIC_MATCHED_LAYER))
      map.addLayer({
        id: DIAGNOSTIC_MATCHED_LAYER,
        type: "line",
        source: DIAGNOSTIC_SOURCE,
        filter: ["==", ["get", "evidenceClass"], "matched"],
        layout: { visibility, "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#19c7c9", "line-width": 5, "line-opacity": 0.9 },
      });
    if (!map.getLayer(DIAGNOSTIC_AMBIGUOUS_LAYER))
      map.addLayer({
        id: DIAGNOSTIC_AMBIGUOUS_LAYER,
        type: "line",
        source: DIAGNOSTIC_SOURCE,
        filter: ["==", ["get", "evidenceClass"], "ambiguous"],
        layout: { visibility, "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#e7a83d", "line-width": 5, "line-opacity": 0.94 },
      });
    if (!map.getLayer(DIAGNOSTIC_SELECTED_LAYER))
      map.addLayer({
        id: DIAGNOSTIC_SELECTED_LAYER,
        type: "line",
        source: DIAGNOSTIC_SOURCE,
        filter: ["==", ["get", "portionOrdinal"], highlightedPortionRef.current ?? -1],
        layout: { visibility, "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#f7fbff", "line-width": 8, "line-opacity": 1 },
      });
    if (!map.getLayer(DIAGNOSTIC_HIT_LAYER))
      map.addLayer({
        id: DIAGNOSTIC_HIT_LAYER,
        type: "line",
        source: DIAGNOSTIC_SOURCE,
        layout: { visibility, "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#000000", "line-width": 18, "line-opacity": 0 },
      });
    for (const layer of DIAGNOSTIC_EVIDENCE_LAYERS)
      if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", visibility);
    if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.moveLayer(DIAGNOSTIC_DIRECTION_LAYER);
    for (const layer of [ROUTE_FINISH_LAYER, ROUTE_START_LAYER])
      if (map.getLayer(layer)) map.moveLayer(layer);
  }

  function syncCoverageHighlight(map: MapLibreMap) {
    const highlight = coverageHighlightRef.current;
    const source = map.getSource(COVERAGE_HIGHLIGHT_SOURCE) as GeoJSONSource | undefined;
    if (!highlight) {
      stopCoverageBlinkRef.current();
      startedCoverageHighlightKeyRef.current = undefined;
      installedCoverageHighlightRef.current = undefined;
      if (map.getLayer(COVERAGE_HIGHLIGHT_LAYER)) map.removeLayer(COVERAGE_HIGHLIGHT_LAYER);
      if (source) map.removeSource(COVERAGE_HIGHLIGHT_SOURCE);
      return;
    }
    const installed = installedCoverageHighlightRef.current;
    if (!source || installed?.source !== source || installed.geometry !== highlight.geometry) {
      const feature = { type: "Feature" as const, geometry: highlight.geometry, properties: {} };
      if (!source) map.addSource(COVERAGE_HIGHLIGHT_SOURCE, { type: "geojson", data: feature });
      else source.setData(feature);
      installedCoverageHighlightRef.current = {
        source: map.getSource(COVERAGE_HIGHLIGHT_SOURCE) as GeoJSONSource,
        geometry: highlight.geometry,
      };
    }
    if (!map.getLayer(COVERAGE_HIGHLIGHT_LAYER))
      map.addLayer({
        id: COVERAGE_HIGHLIGHT_LAYER,
        type: "line",
        source: COVERAGE_HIGHLIGHT_SOURCE,
        layout: { "line-cap": "round", "line-join": "round" },
        paint: {
          "line-color": "#ffffff",
          "line-width": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 10],
          "line-opacity": 0,
        },
      });
    else map.moveLayer(COVERAGE_HIGHLIGHT_LAYER);
    if (startedCoverageHighlightKeyRef.current === highlight.key) return;
    startedCoverageHighlightKeyRef.current = highlight.key;
    stopCoverageBlinkRef.current();
    map.fitBounds(
      [
        [highlight.fitBounds.minimumLongitude, highlight.fitBounds.minimumLatitude],
        [highlight.fitBounds.maximumLongitude, highlight.fitBounds.maximumLatitude],
      ],
      { padding: fitPadding, duration: 350 },
    );
    stopCoverageBlinkRef.current = startCoverageHighlightBlink(
      (value) => {
        if (map.getLayer(COVERAGE_HIGHLIGHT_LAYER))
          map.setPaintProperty(COVERAGE_HIGHLIGHT_LAYER, "line-opacity", value);
      },
      (ready) => onMapContextReady(map, ready),
    );
  }

  function syncMapLayers(map: MapLibreMap) {
    syncPrivateLayers(map);
    syncRawHighlight(map);
    syncDiagnosticLayers(map);
    syncCoverageHighlight(map);
  }

  function preservePrivateLayers(
    previous: StyleSpecification | undefined,
    next: StyleSpecification,
  ) {
    if (!previous?.sources[ROUTES_SOURCE]) return next;
    const sources: StyleSpecification["sources"] = {
      ...next.sources,
      [ROUTES_SOURCE]: previous.sources[ROUTES_SOURCE],
    };
    if (previous.sources[COVERAGE_SOURCE])
      sources[COVERAGE_SOURCE] = previous.sources[COVERAGE_SOURCE];
    if (previous.sources[RAW_FOCUS_SOURCE])
      sources[RAW_FOCUS_SOURCE] = previous.sources[RAW_FOCUS_SOURCE];
    if (previous.sources[COVERAGE_FOCUS_SOURCE])
      sources[COVERAGE_FOCUS_SOURCE] = previous.sources[COVERAGE_FOCUS_SOURCE];
    if (previous.sources[COVERAGE_HIGHLIGHT_SOURCE])
      sources[COVERAGE_HIGHLIGHT_SOURCE] = previous.sources[COVERAGE_HIGHLIGHT_SOURCE];
    if (previous.sources[DIAGNOSTIC_SOURCE])
      sources[DIAGNOSTIC_SOURCE] = previous.sources[DIAGNOSTIC_SOURCE];
    if (previous.sources[DIAGNOSTIC_RAW_ROUTE_SOURCE])
      sources[DIAGNOSTIC_RAW_ROUTE_SOURCE] = previous.sources[DIAGNOSTIC_RAW_ROUTE_SOURCE];
    if (previous.sources[DIAGNOSTIC_DIRECTION_SOURCE])
      sources[DIAGNOSTIC_DIRECTION_SOURCE] = previous.sources[DIAGNOSTIC_DIRECTION_SOURCE];
    if (previous.sources[ROUTE_ENDPOINTS_SOURCE])
      sources[ROUTE_ENDPOINTS_SOURCE] = previous.sources[ROUTE_ENDPOINTS_SOURCE];
    const privateLayers = previous.layers.filter((layer) =>
      [
        ...ROUTE_LAYERS,
        ...DIAGNOSTIC_LAYERS,
        ...COVERAGE_LAYERS,
        COVERAGE_HIGHLIGHT_LAYER,
      ].includes(layer.id as never),
    );
    return { ...next, sources, layers: [...next.layers, ...privateLayers] };
  }

  function removePopup() {
    if (popupTimerRef.current) clearTimeout(popupTimerRef.current);
    popupTimerRef.current = undefined;
    popupWorkoutRef.current = undefined;
    coveragePopupEntityRef.current = undefined;
    coveragePopupAbortRef.current?.abort();
    coveragePopupAbortRef.current = undefined;
    popupRef.current?.remove();
    popupRef.current = undefined;
  }

  function cancelCoverageHoverRequests() {
    for (const request of coverageHoverRequestsRef.current.values()) request.controller.abort();
    coverageHoverRequestsRef.current.clear();
  }

  function popupContent(workout: MapSelectionWorkout) {
    const content = document.createElement("div");
    content.className = "map-route-tooltip";
    const type = document.createElement("strong");
    type.className = "map-route-tooltip-type";
    type.textContent = workout.type.name;
    const distance = document.createElement("strong");
    distance.className = "map-route-tooltip-distance";
    distance.textContent = formatRoutePopupDistance(workout, preferencesRef.current.units);
    const timing = formatRoutePopupDetails(
      workout.startedAt,
      workout.endedAt,
      preferencesRef.current,
    );
    const date = document.createElement("span");
    date.className = "map-route-tooltip-date";
    date.textContent = timing.date;
    const times = document.createElement("span");
    times.className = "map-route-tooltip-times";
    times.textContent = timing.timeRange;
    content.append(type, distance, date, times);
    return content;
  }

  function coveragePopupContent(detail: RoadCoverageEntity) {
    const content = document.createElement("div");
    content.className = "map-route-tooltip map-coverage-tooltip";
    const title = document.createElement("strong");
    title.className = "map-coverage-tooltip-title";
    title.textContent = coverageEntityName(detail);
    const place = document.createElement("span");
    place.className = "map-coverage-tooltip-place";
    place.textContent = coverageEntityPlace(detail);
    content.append(title, place);
    const rows: Array<[string, string]> = [
      ["Workouts", detail.rangeWorkoutCount.toLocaleString()],
      ["Earliest visit", formatCoveragePopupDate(detail.rangeFirstDate)],
      ["First visit ever", formatCoveragePopupDate(detail.allTimeFirstDate)],
      ["Most recent visit", formatCoveragePopupDate(detail.rangeLatestDate)],
      ["Last visit ever", formatCoveragePopupDate(detail.allTimeLatestDate)],
    ];
    for (const [label, value] of rows) {
      const term = document.createElement("span");
      term.textContent = label;
      const description = document.createElement("strong");
      description.textContent = value;
      content.append(term, description);
    }
    return content;
  }

  // Publish the latest intent before any layer-sync effects or style-load replay.
  useEffect(() => {
    coverageHighlightRef.current = coverageHighlight;
    const map = mapRef.current;
    if (map && styleLoadedRef.current) syncCoverageHighlight(map);
    else {
      stopCoverageBlinkRef.current();
      startedCoverageHighlightKeyRef.current = undefined;
    }
  }, [coverageHighlight?.key]);

  useEffect(() => {
    if (!containerRef.current) return;
    const map = new maplibregl.Map({
      container: containerRef.current,
      style: fallbackStyleRef.current,
      attributionControl: false,
    });
    mapRef.current = map;
    let disposed = false;
    const privateTiles = new PrivateTiles(window.location.origin, {
      unauthorized: () => {
        if (!disposed && styleLoadedRef.current && mapRef.current === map)
          window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      },
      unavailable: () => {
        if (!disposed && styleLoadedRef.current && mapRef.current === map)
          onRouteTilesUnavailableRef.current();
      },
    });
    privateTilesRef.current = privateTiles;
    const restore = () => {
      if (disposed) return;
      invalidateDirectionMarkers(map);
      styleLoadedRef.current = true;
      installedCoverageHighlightRef.current = undefined;
      rawFallbackFiltersRef.current = {};
      syncMapLayers(map);
      const fallbackLoaded =
        map.getStyle().layers?.some((layer) => layer.id === "fallback-background") ?? false;
      if (fallbackLoaded) return;
      fallbackInstalledRef.current = false;
      if (baseMapRetryTimerRef.current !== undefined) {
        window.clearTimeout(baseMapRetryTimerRef.current);
        baseMapRetryTimerRef.current = undefined;
      }
      onBaseMapReady();
    };
    const restoreMissedInitialStyle = () => {
      if (!styleLoadedRef.current) restore();
    };
    const styleError = (event?: unknown) => {
      if (disposed) return;
      const tileError = event as
        { sourceId?: string; error?: { status?: number; url?: string; name?: string } } | undefined;
      // Protocol failures already retried or invalidated their exact current scope.
      if (["PrivateTileError", "AbortError"].includes(tileError?.error?.name ?? "")) return;
      if (privateRouteTileUnauthorized(event)) {
        if (
          tileError?.error?.url &&
          ![routeUrlRef.current, coverageUrlRef.current].some(
            (url) =>
              url &&
              tileError.error!.url!.startsWith(
                absoluteRouteTileTemplate(url, window.location.origin).split("{z}")[0],
              ),
          )
        )
          return;
        privateTiles.cancel();
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
        return;
      }
      if (
        privateRouteTileUnavailable(event, routeUrlRef.current) ||
        privateRouteTileUnavailable(event, coverageUrlRef.current)
      ) {
        privateTiles.cancel();
        onRouteTilesUnavailableRef.current();
        return;
      }
      if (styleLoadedRef.current || fallbackInstalledRef.current) return;
      fallbackInstalledRef.current = true;
      onBaseMapError();
      cancelPrivateTilesForStyle();
      invalidateDirectionMarkers(map);
      map.setStyle(fallbackStyleRef.current);
      if (baseMapRetryTimerRef.current === undefined) {
        baseMapRetryTimerRef.current = window.setTimeout(() => {
          baseMapRetryTimerRef.current = undefined;
          styleLoadedRef.current = false;
          fallbackInstalledRef.current = false;
          cancelPrivateTilesForStyle();
          invalidateDirectionMarkers(map);
          map.setStyle(styleUrlRef.current, { transformStyle: preservePrivateLayers });
        }, 5000);
      }
    };
    const routeFeatureAt = (event: MapMouseEvent) => {
      const background = nonFocusedRoutesHiddenRef.current
        ? []
        : map.queryRenderedFeatures(event.point, {
            layers: [ROUTES_LAYER, ROUTE_MARKERS_LAYER],
          });
      // The background keeps newest-workout priority even above a focused overlay.
      if (background.length) return topmostFeature(background);
      const layers = [RAW_FOCUS_LAYER, RAW_FOCUS_MARKERS_LAYER].filter((id) => map.getLayer(id));
      return layers.length && rawHighlightReady(map)
        ? topmostFeature(map.queryRenderedFeatures(event.point, { layers }))
        : undefined;
    };
    const hover = (event: MapMouseEvent) => {
      if (diagnosticModeRef.current) return;
      const feature = routeFeatureAt(event);
      const workoutID =
        typeof feature?.properties?.workoutId === "string"
          ? feature.properties.workoutId.toUpperCase()
          : undefined;
      onHover(workoutID);
      map.getCanvas().style.cursor = feature ? "pointer" : "";
      if (!workoutID) {
        removePopup();
        return;
      }
      popupLocationRef.current = event.lngLat;
      if (popupRef.current && popupWorkoutRef.current === workoutID) {
        popupRef.current.setLngLat(event.lngLat);
        return;
      }
      if (popupWorkoutRef.current === workoutID && popupTimerRef.current) return;
      removePopup();
      popupWorkoutRef.current = workoutID;
      popupTimerRef.current = setTimeout(() => {
        const workout = workoutsRef.current.find((item) => item.id === workoutID);
        if (!workout || !popupLocationRef.current) return;
        popupRef.current = new maplibregl.Popup({
          closeButton: false,
          closeOnClick: false,
          offset: 12,
          className: "map-route-popup",
          maxWidth: "none",
        })
          .setLngLat(popupLocationRef.current)
          .setDOMContent(popupContent(workout))
          .addTo(map);
        popupTimerRef.current = undefined;
      }, 750);
    };
    const leave = () => {
      onHover(undefined);
      map.getCanvas().style.cursor = "";
      removePopup();
    };
    const leaveRoute = () => {
      if (!diagnosticModeRef.current) leave();
    };
    const visibilityChanged = () => {
      if (document.visibilityState === "hidden") leave();
    };
    const hoverCoverage = (event: MapMouseEvent) => {
      if (!diagnosticModeRef.current || !productionCoverageEnabledRef.current) return;
      const hitBox: [[number, number], [number, number]] = [
        [event.point.x - 3, event.point.y - 3],
        [event.point.x + 3, event.point.y + 3],
      ];
      let features = nonFocusedCoverageHiddenRef.current
        ? []
        : map.queryRenderedFeatures(hitBox, {
            layers: [COVERAGE_PARK_LAYER, COVERAGE_LAYER],
          });
      if (!features.length) {
        const layers = [...FOCUSED_COVERAGE_LAYERS].filter((id) => map.getLayer(id));
        if (layers.length) features = map.queryRenderedFeatures(hitBox, { layers });
      }
      const parkNames = new Set(
        features.flatMap((candidate) => {
          const name =
            candidate.properties?.sourceEntityKind === "park"
              ? candidate.properties?.name
              : undefined;
          return typeof name === "string" && name.trim() ? [name.trim()] : [];
        }),
      );
      const regionalParkPath = features.find((candidate) => {
        const locality =
          candidate.properties?.sourceEntityKind === "path"
            ? candidate.properties?.localityName
            : undefined;
        return typeof locality === "string" && parkNames.has(locality.trim());
      });
      const feature =
        regionalParkPath ??
        features.find(
          (candidate) =>
            candidate.properties?.sourceEntityKind === "path" &&
            typeof candidate.properties?.name === "string" &&
            candidate.properties.name.trim(),
        ) ??
        features.find((candidate) => candidate.properties?.sourceEntityKind === "park") ??
        features[0];
      const entityID =
        typeof feature?.properties?.entityId === "string"
          ? feature.properties.entityId.toUpperCase()
          : undefined;
      const entityKind =
        feature?.properties?.entityKind === "park"
          ? "park"
          : feature?.properties?.entityKind === "path"
            ? "path"
            : undefined;
      map.getCanvas().style.cursor = feature ? "pointer" : "";
      if (!entityID || !entityKind || !selectionRef.current) {
        removePopup();
        return;
      }
      const activeSelection = selectionRef.current;
      const context = coverageHoverContextRef.current;
      const key = `${context}:${entityKind}:${entityID}`;
      popupLocationRef.current = event.lngLat;
      if (popupRef.current && coveragePopupEntityRef.current === key) {
        popupRef.current.setLngLat(event.lngLat);
        return;
      }
      if (
        coveragePopupEntityRef.current === key &&
        (popupTimerRef.current || coveragePopupAbortRef.current)
      )
        return;
      removePopup();
      coveragePopupEntityRef.current = key;
      popupTimerRef.current = setTimeout(() => {
        popupTimerRef.current = undefined;
        if (coverageHoverContextRef.current !== context || coveragePopupEntityRef.current !== key)
          return;
        const controller = new AbortController();
        coveragePopupAbortRef.current = controller;
        const isCurrent = () =>
          !disposed &&
          mapRef.current === map &&
          !controller.signal.aborted &&
          coverageHoverContextRef.current === context &&
          coveragePopupEntityRef.current === key;
        const loadStats = async () => {
          const cache = coverageHoverCacheRef.current;
          const cached = cache.get(key);
          cache.delete(key);
          if (cached && Date.now() - cached.loadedAt < COVERAGE_HOVER_CACHE_TTL_MS) {
            cache.set(key, cached);
            return cached;
          }
          const requests = coverageHoverRequestsRef.current;
          const existing = requests.get(key);
          if (existing) return existing.result;
          // A lookup can outlive its popup, but not its selection context.
          const requestController = new AbortController();
          const contextIsCurrent = () =>
            !disposed &&
            mapRef.current === map &&
            !requestController.signal.aborted &&
            coverageHoverContextRef.current === context;
          const requestStats = async () => {
            for (let attempt = 0; ; attempt++) {
              if (!contextIsCurrent() || (attempt > 0 && coveragePopupEntityRef.current !== key))
                throw new DOMException("Aborted", "AbortError");
              try {
                return await api<RoadCoverageEntity>(
                  `/api/map-selections/${encodeURIComponent(activeSelection.id)}/coverage/${entityKind}/${encodeURIComponent(entityID)}/stats?generation=${activeSelection.dataGeneration}`,
                  { signal: requestController.signal },
                );
              } catch (error) {
                if (!contextIsCurrent() || coveragePopupEntityRef.current !== key) throw error;
                if (error instanceof ApiError && error.status === 404)
                  onRouteTilesUnavailableRef.current();
                if (!(error instanceof ApiError) || error.status !== 503 || attempt > 0)
                  throw error;
                await waitForDiagnosticRetry(requestController.signal, 250);
              }
            }
          };
          const result = requestStats()
            .then((entity) => {
              if (!contextIsCurrent()) throw new DOMException("Aborted", "AbortError");
              const value = { entity, loadedAt: Date.now() };
              cache.delete(key);
              cache.set(key, value);
              if (cache.size > COVERAGE_HOVER_CACHE_LIMIT) cache.delete(cache.keys().next().value!);
              return value;
            })
            .finally(() => {
              if (requests.get(key)?.controller === requestController) requests.delete(key);
            });
          requests.set(key, { controller: requestController, result });
          return result;
        };
        // Overlap the lookup with the remaining dwell, but never show a popup early.
        void Promise.all([
          loadStats(),
          waitForDiagnosticRetry(
            controller.signal,
            COVERAGE_HOVER_DELAY_MS - COVERAGE_HOVER_LOOKUP_DELAY_MS,
          ),
        ])
          .then(([cached]) => {
            if (!isCurrent() || !popupLocationRef.current) return;
            popupRef.current = new maplibregl.Popup({
              closeButton: false,
              closeOnClick: false,
              offset: 12,
              className: "map-route-popup map-coverage-popup",
              maxWidth: "none",
            })
              .setLngLat(popupLocationRef.current)
              .setDOMContent(coveragePopupContent(cached.entity))
              .addTo(map);
          })
          .catch(() => controller.abort())
          .finally(() => {
            if (coveragePopupAbortRef.current === controller)
              coveragePopupAbortRef.current = undefined;
          });
      }, COVERAGE_HOVER_LOOKUP_DELAY_MS);
    };
    const clickRoute = (event: MapMouseEvent) => {
      if (diagnosticModeRef.current) return;
      const feature = routeFeatureAt(event);
      const workoutID =
        typeof feature?.properties?.workoutId === "string"
          ? feature.properties.workoutId.toUpperCase()
          : undefined;
      if (workoutID) onRouteClickRef.current(workoutID);
    };
    const diagnosticPortionAt = (event: MapMouseEvent) => {
      const visibleFeature = map.queryRenderedFeatures(event.point, {
        layers: [DIAGNOSTIC_AMBIGUOUS_LAYER, DIAGNOSTIC_MATCHED_LAYER],
      })[0];
      const feature =
        visibleFeature ??
        map.queryRenderedFeatures(event.point, { layers: [DIAGNOSTIC_HIT_LAYER] })[0];
      const ordinal = Number(feature?.properties?.portionOrdinal);
      map.getCanvas().style.cursor = feature ? "pointer" : "";
      return Number.isInteger(ordinal) && ordinal >= 0 ? ordinal : undefined;
    };
    const hoverDiagnosticPortion = (event: MapMouseEvent) => {
      onDiagnosticHover(diagnosticPortionAt(event));
    };
    const lockDiagnosticPortion = (event: MapMouseEvent) => {
      const ordinal = diagnosticPortionAt(event);
      if (ordinal !== undefined) onDiagnosticLock(ordinal);
    };
    const leaveDiagnosticPortion = () => {
      onDiagnosticHover(undefined);
      map.getCanvas().style.cursor = "";
    };
    const showDefaultViewport = () => {
      if (defaultViewportSetRef.current) return;
      defaultViewportSetRef.current = true;
      if (selectionRef.current?.bounds) return;
      const fullUS = () => {
        if (!selectionRef.current?.bounds)
          map.fitBounds(
            [
              [-125, 24],
              [-66.5, 49.5],
            ],
            { padding: fitPadding, duration: 0 },
          );
      };
      if (!navigator.geolocation) {
        fullUS();
        return;
      }
      navigator.geolocation.getCurrentPosition(
        (position) => {
          if (!selectionRef.current?.bounds)
            map.jumpTo({ center: [position.coords.longitude, position.coords.latitude], zoom: 11 });
        },
        fullUS,
        { enableHighAccuracy: false, timeout: 5000, maximumAge: 300000 },
      );
    };
    map.on("style.load", restore);
    map.on("load", restoreMissedInitialStyle);
    map.on("load", showDefaultViewport);
    map.on("error", styleError);
    const beginDirectionMovement = (event?: { originalEvent?: Event }) => {
      if (disposed) return;
      directionStateRef.current.moving = true;
      if (event?.originalEvent && map.dragPan.isActive()) syncDirectionMarkers(map);
      else invalidateDirectionMarkers(map);
    };
    const invalidateCameraDirections = () => {
      if (!disposed) invalidateDirectionMarkers(map);
    };
    const refreshDirectionMarkers = () => {
      if (disposed || map.isMoving()) return;
      const { moving, update } = directionStateRef.current;
      directionStateRef.current.moving = false;
      // Only a user drag can retain ready markers; programmatic movement invalidates them.
      if (!moving || update?.status !== "ready") invalidateDirectionMarkers(map);
      syncDirectionMarkers(map);
    };
    const directionSourceReady = (event: { sourceId?: string }) => {
      if (
        !disposed &&
        [DIAGNOSTIC_DIRECTION_SOURCE, RAW_FOCUS_SOURCE, DIAGNOSTIC_RAW_ROUTE_SOURCE].includes(
          event.sourceId ?? "",
        )
      ) {
        syncRawHighlightVisibility(map);
        syncDirectionVisibility(map);
      }
    };
    const directionViewportRendered = () => {
      const state = directionStateRef.current;
      if (disposed || state.moving || map.isMoving() || !state.update) return;
      // The final camera's GeoJSON source tiles are issued during rendering, not moveend.
      state.update.viewportReady = true;
      syncRawHighlightVisibility(map);
      syncDirectionVisibility(map);
    };
    map.on("movestart", beginDirectionMovement);
    map.on("zoomstart", invalidateCameraDirections);
    map.on("rotatestart", invalidateCameraDirections);
    map.on("pitchstart", invalidateCameraDirections);
    map.on("moveend", refreshDirectionMarkers);
    map.on("sourcedata", directionSourceReady);
    map.on("render", directionViewportRendered);
    map.on("mousemove", ROUTES_LAYER, hover);
    map.on("mouseleave", ROUTES_LAYER, leaveRoute);
    map.on("mousemove", ROUTE_MARKERS_LAYER, hover);
    map.on("mouseleave", ROUTE_MARKERS_LAYER, leaveRoute);
    map.on("mousemove", RAW_FOCUS_LAYER, hover);
    map.on("mouseleave", RAW_FOCUS_LAYER, leaveRoute);
    map.on("mousemove", RAW_FOCUS_MARKERS_LAYER, hover);
    map.on("mouseleave", RAW_FOCUS_MARKERS_LAYER, leaveRoute);
    map.on("mousemove", hoverCoverage);
    map.getCanvas().addEventListener("mouseleave", leave);
    window.addEventListener("blur", leave);
    document.addEventListener("visibilitychange", visibilityChanged);
    map.on("click", clickRoute);
    map.on("mousemove", DIAGNOSTIC_HIT_LAYER, hoverDiagnosticPortion);
    map.on("click", DIAGNOSTIC_HIT_LAYER, lockDiagnosticPortion);
    map.on("mouseleave", DIAGNOSTIC_HIT_LAYER, leaveDiagnosticPortion);
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
    styleLoadedRef.current = false;
    fallbackInstalledRef.current = false;
    invalidateDirectionMarkers(map);
    map.setStyle(family.styles[preferences.theme], { transformStyle: preservePrivateLayers });
    return () => {
      disposed = true;
      directionStateRef.current.update = undefined;
      rawHighlightUpdateRef.current = undefined;
      styleLoadedRef.current = false;
      privateTiles.close();
      privateTilesRef.current = undefined;
      if (coverageHandoffTimerRef.current !== undefined)
        window.clearTimeout(coverageHandoffTimerRef.current);
      if (baseMapRetryTimerRef.current !== undefined)
        window.clearTimeout(baseMapRetryTimerRef.current);
      stopCoverageBlinkRef.current();
      map.off("style.load", restore);
      map.off("load", restoreMissedInitialStyle);
      map.off("load", showDefaultViewport);
      map.off("error", styleError);
      map.off("movestart", beginDirectionMovement);
      map.off("zoomstart", invalidateCameraDirections);
      map.off("rotatestart", invalidateCameraDirections);
      map.off("pitchstart", invalidateCameraDirections);
      map.off("moveend", refreshDirectionMarkers);
      map.off("sourcedata", directionSourceReady);
      map.off("render", directionViewportRendered);
      map.off("mousemove", ROUTES_LAYER, hover);
      map.off("mouseleave", ROUTES_LAYER, leaveRoute);
      map.off("mousemove", ROUTE_MARKERS_LAYER, hover);
      map.off("mouseleave", ROUTE_MARKERS_LAYER, leaveRoute);
      map.off("mousemove", RAW_FOCUS_LAYER, hover);
      map.off("mouseleave", RAW_FOCUS_LAYER, leaveRoute);
      map.off("mousemove", RAW_FOCUS_MARKERS_LAYER, hover);
      map.off("mouseleave", RAW_FOCUS_MARKERS_LAYER, leaveRoute);
      map.off("mousemove", hoverCoverage);
      map.getCanvas().removeEventListener("mouseleave", leave);
      window.removeEventListener("blur", leave);
      document.removeEventListener("visibilitychange", visibilityChanged);
      map.off("click", clickRoute);
      map.off("mousemove", DIAGNOSTIC_HIT_LAYER, hoverDiagnosticPortion);
      map.off("click", DIAGNOSTIC_HIT_LAYER, lockDiagnosticPortion);
      map.off("mouseleave", DIAGNOSTIC_HIT_LAYER, leaveDiagnosticPortion);
      removePopup();
      cancelCoverageHoverRequests();
      coverageHoverCacheRef.current.clear();
      for (const layer of [...DIAGNOSTIC_LAYERS].reverse())
        if (map.getLayer(layer)) map.removeLayer(layer);
      if (map.getSource(DIAGNOSTIC_SOURCE)) map.removeSource(DIAGNOSTIC_SOURCE);
      if (map.getSource(DIAGNOSTIC_DIRECTION_SOURCE)) map.removeSource(DIAGNOSTIC_DIRECTION_SOURCE);
      if (map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE)) map.removeSource(DIAGNOSTIC_RAW_ROUTE_SOURCE);
      map.remove();
      mapRef.current = undefined;
    };
  }, []);

  useEffect(() => {
    if (coverageHoverContextRef.current !== coverageHoverContext) {
      removePopup();
      cancelCoverageHoverRequests();
      coverageHoverCacheRef.current.clear();
      coverageHoverContextRef.current = coverageHoverContext;
    }
    privateTilesRef.current?.invalidateSource(
      ROUTES_SOURCE,
      selection?.routeTileUrl
        ? absoluteRouteTileTemplate(selection.routeTileUrl, window.location.origin)
        : undefined,
    );
    privateTilesRef.current?.invalidateSource(
      COVERAGE_SOURCE,
      selection?.coverageTileUrl
        ? absoluteRouteTileTemplate(selection.coverageTileUrl, window.location.origin)
        : undefined,
    );
    routeUrlRef.current = selection?.routeTileUrl;
    coverageUrlRef.current = selection?.coverageTileUrl;
    coverageFocusRef.current = coverageFocus ?? { type: "FeatureCollection", features: [] };
    selectionRef.current = selection;
    routeColorsRef.current = routeColors(selection?.workouts ?? []);
    workoutsRef.current = workouts;
    preferencesRef.current = preferences;
    const map = mapRef.current;
    if (map && styleLoadedRef.current) syncMapLayers(map);
  }, [
    preferences,
    selection?.id,
    coverageHoverContext,
    selection?.routeTileUrl,
    selection?.coverageTileUrl,
    coverageFocus,
    workouts,
  ]);

  useEffect(() => {
    onRouteTilesUnavailableRef.current = onRouteTilesUnavailable;
  }, [onRouteTilesUnavailable]);
  useEffect(() => {
    onRouteClickRef.current = onRouteClick;
  }, [onRouteClick]);

  useEffect(() => {
    if (diagnosticModeRef.current !== diagnosticMode) removePopup();
    diagnosticEnabledRef.current = diagnosticEnabled;
    productionCoverageEnabledRef.current = productionCoverageEnabled;
    diagnosticModeRef.current = diagnosticMode;
    rawRouteHiddenRef.current = rawRouteHidden;
    nonFocusedRoutesHiddenRef.current = nonFocusedRoutesHidden;
    nonFocusedCoverageHiddenRef.current = nonFocusedCoverageHidden;
    diagnosticRawRouteRef.current = diagnosticRawRoute;
    directionRouteRef.current = directionRoute;
    rawHighlightRouteRef.current = rawHighlightRoute;
    hoverRef.current = hoveredWorkoutId;
    rawRouteEndpointsRef.current = rawRouteEndpoints;
    rawRouteEndpointsVisibleRef.current = rawRouteEndpointsVisible;
    diagnosticRef.current = diagnostic;
    highlightedPortionRef.current = highlightedPortionOrdinal;
    if ((!diagnosticMode || !productionCoverageEnabled) && coveragePopupEntityRef.current)
      removePopup();
    const map = mapRef.current;
    if (map && styleLoadedRef.current) syncMapLayers(map);
    if (map?.getLayer(DIAGNOSTIC_SELECTED_LAYER))
      map.setFilter(DIAGNOSTIC_SELECTED_LAYER, [
        "==",
        ["get", "portionOrdinal"],
        highlightedPortionOrdinal ?? -1,
      ]);
  }, [
    diagnosticEnabled,
    productionCoverageEnabled,
    diagnosticMode,
    routeHidingEnabled,
    nonFocusedRoutesHidden,
    rawRouteHidden,
    nonFocusedCoverageHidden,
    diagnosticRawRoute,
    directionRoute,
    rawHighlightRoute,
    hoveredWorkoutId,
    rawRouteEndpoints,
    rawRouteEndpointsVisible,
    diagnostic,
    highlightedPortionOrdinal,
  ]);

  useEffect(() => {
    if (!diagnosticMode || !diagnostic) return;
    mapRef.current?.getCanvas().focus({ preventScroll: true });
  }, [diagnosticMode, diagnostic?.id]);

  useEffect(() => {
    const map = mapRef.current;
    const nextStyle = family.styles[preferences.theme];
    fallbackStyleRef.current = {
      version: 8,
      sources: {},
      layers: [
        {
          id: "fallback-background",
          type: "background",
          paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" },
        },
      ],
    };
    if (map && styleUrlRef.current !== nextStyle) {
      if (baseMapRetryTimerRef.current !== undefined) {
        window.clearTimeout(baseMapRetryTimerRef.current);
        baseMapRetryTimerRef.current = undefined;
      }
      styleUrlRef.current = nextStyle;
      styleLoadedRef.current = false;
      fallbackInstalledRef.current = false;
      cancelPrivateTilesForStyle();
      invalidateDirectionMarkers(map);
      map.setStyle(nextStyle, { transformStyle: preservePrivateLayers });
    }
  }, [family.id, family.styles, preferences.theme]);

  useEffect(() => {
    hoverRef.current = hoveredWorkoutId;
    const map = mapRef.current;
    if (map) syncRawHighlightVisibility(map);
  }, [hoveredWorkoutId]);

  useEffect(() => {
    const map = mapRef.current;
    const bounds = fitRequest?.bounds;
    if (map && fallbackInstalledRef.current) {
      if (baseMapRetryTimerRef.current !== undefined) {
        window.clearTimeout(baseMapRetryTimerRef.current);
        baseMapRetryTimerRef.current = undefined;
      }
      styleLoadedRef.current = false;
      fallbackInstalledRef.current = false;
      cancelPrivateTilesForStyle();
      invalidateDirectionMarkers(map);
      map.setStyle(styleUrlRef.current, { transformStyle: preservePrivateLayers });
    }
    if (map && bounds) {
      map.fitBounds(
        [
          [bounds.minimumLongitude, bounds.minimumLatitude],
          [bounds.maximumLongitude, bounds.maximumLatitude],
        ],
        { padding: fitPadding, duration: 350 },
      );
      if (fitRequest.focusCanvas) map.getCanvas().focus({ preventScroll: true });
    }
  }, [fitRequest?.key]);

  useEffect(() => {
    if (focusRequest > 0) mapRef.current?.getCanvas().focus({ preventScroll: true });
  }, [focusRequest]);

  return (
    <div
      ref={containerRef}
      className="map-canvas"
      role="application"
      aria-label="Workout route map"
      aria-keyshortcuts={
        routeHidingEnabled || (diagnosticMode && (diagnosticEnabled || productionCoverageEnabled))
          ? "Space"
          : undefined
      }
    />
  );
}

function workoutSortValue(workout: MapSelectionWorkout, field: WorkoutColumn) {
  if (field === "date") return workout.startedAt;
  if (field === "type") return workout.type.name;
  if (field === "duration") return Number(workout.duration);
  const metric = workout[field];
  return metric ? Number(metric.value) : null;
}

export function sortMapWorkouts(workouts: MapSelectionWorkout[], sort: WorkoutSort) {
  return [...workouts].sort((left, right) => {
    const leftValue = workoutSortValue(left, sort.field);
    const rightValue = workoutSortValue(right, sort.field);
    if (leftValue == null || Number.isNaN(leftValue))
      return rightValue == null || Number.isNaN(rightValue) ? left.id.localeCompare(right.id) : 1;
    if (rightValue == null || Number.isNaN(rightValue)) return -1;
    const comparison =
      typeof leftValue === "string" && typeof rightValue === "string"
        ? leftValue < rightValue
          ? -1
          : leftValue > rightValue
            ? 1
            : 0
        : Number(leftValue) - Number(rightValue);
    if (comparison) return sort.direction === "asc" ? comparison : -comparison;
    if (sort.field !== "date" && left.startedAt !== right.startedAt)
      return left.startedAt > right.startedAt ? -1 : 1;
    return left.id.localeCompare(right.id);
  });
}

function mergeWorkoutReadiness(current: MapSelectionWorkout[], updates: MapSelectionWorkout[]) {
  const replacements = new Map(updates.map((workout) => [workout.id, workout]));
  return current.map((workout) => replacements.get(workout.id) ?? workout);
}

function hasRenderableCoverage(workout: MapSelectionWorkout) {
  return (
    ["current", "stale"].includes(workout.coverageReadiness.resultStatus) &&
    workout.coverageBounds != null
  );
}

function coverageRouteStatus(workout: MapSelectionWorkout) {
  const readiness = workout.coverageReadiness;
  if (readiness.resultStatus !== "none" && !hasRenderableCoverage(workout))
    return { kind: "unavailable", label: "No map segments matched", symbol: "" };
  if (readiness.resultStatus === "current")
    return { kind: "current", label: "Coverage current", symbol: "✓" };
  if (readiness.resultStatus === "stale")
    return { kind: "stale", label: "Coverage stale", symbol: "" };
  if (readiness.mapDataStatus === "unavailable")
    return { kind: "unavailable", label: "Coverage unavailable", symbol: "" };
  if (readiness.processingStatus === "failed" && readiness.mapDataStatus !== "pending")
    return { kind: "failed", label: "Coverage failed", symbol: "!" };
  return { kind: "pending", label: "Coverage pending", symbol: "" };
}

function shouldPollCoverage(workout: MapSelectionWorkout) {
  return (
    workout.coverageReadiness.resultStatus === "none" &&
    workout.coverageReadiness.mapDataStatus !== "unavailable" &&
    workout.coverageReadiness.processingStatus !== "failed"
  );
}

function unavailableCoverageReason(workout: MapSelectionWorkout) {
  const readiness = workout.coverageReadiness;
  if (readiness.resultStatus !== "none" && !hasRenderableCoverage(workout))
    return "No map segments matched";
  if (readiness.mapDataStatus === "unavailable") return "Map data not available";
  if (readiness.mapDataStatus === "pending") return "Waiting for map data";
  if (readiness.processingStatus === "failed") return "Coverage update failed";
  return "Waiting for coverage update";
}

function WorkoutRouteList({
  workouts,
  preferences,
  sort,
  mode,
  diagnosticCoverage,
  visibleIDs,
  highlightedWorkoutId,
  focusedWorkoutId,
  scrollRequest,
  onToggle,
  onFocus,
  onHover,
}: {
  workouts: MapSelectionWorkout[];
  preferences: Preferences;
  sort: WorkoutSort;
  mode: MapMode;
  diagnosticCoverage: boolean;
  visibleIDs?: string[];
  highlightedWorkoutId?: string;
  focusedWorkoutId?: string;
  scrollRequest?: { key: number; workoutId: string };
  onToggle: (id: string) => void;
  onFocus: (workout: MapSelectionWorkout) => void;
  onHover: (id?: string) => void;
}) {
  const listRef = useRef<HTMLOListElement>(null);
  const scrolledRequestRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (
      !scrollRequest ||
      focusedWorkoutId !== scrollRequest.workoutId ||
      scrolledRequestRef.current === scrollRequest.key
    )
      return;
    const target = listRef.current?.querySelector<HTMLElement>(
      `[data-workout-id="${scrollRequest.workoutId}"]`,
    );
    if (!target) return;
    target.scrollIntoView({ block: "center", inline: "nearest" });
    scrolledRequestRef.current = scrollRequest.key;
  }, [focusedWorkoutId, scrollRequest, workouts]);
  return (
    <ol
      ref={listRef}
      className="map-route-list map-workout-list"
      onPointerLeave={() => onHover(undefined)}
    >
      {sortMapWorkouts(workouts, sort).map((workout) => {
        const coverageStatus = coverageRouteStatus(workout);
        const focusDisabled =
          mode === "coverage" && !diagnosticCoverage && !hasRenderableCoverage(workout);
        const focusControl = (
          <button
            type="button"
            className="map-route-focus"
            disabled={focusDisabled}
            onClick={() => onFocus(workout)}
            onPointerEnter={() => {
              if (focusDisabled) onHover(undefined);
              else onHover(workout.id);
            }}
            onFocus={() => {
              if (!focusDisabled) onHover(workout.id);
            }}
            onBlur={() => onHover(undefined)}
          >
            {mode === "routes" ? (
              <span
                className={`route-swatch route-swatch--${semanticRouteKind(workout.type.name) ?? routeColorIndex(workout.type.key)}`}
                aria-hidden="true"
              />
            ) : (
              <span
                className={`coverage-route-status coverage-route-status--${coverageStatus.kind}`}
              >
                {coverageStatus.kind === "unavailable" ? (
                  <svg
                    viewBox="0 0 16 16"
                    width="16"
                    height="16"
                    aria-hidden="true"
                    focusable="false"
                  >
                    <circle cx="8" cy="8" r="8" fill="var(--status-neutral)" />
                    <path
                      d="M5 5L11 11M11 5L5 11"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.5"
                      strokeLinecap="round"
                    />
                  </svg>
                ) : (
                  <span aria-hidden="true">{coverageStatus.symbol}</span>
                )}
                <span className="visually-hidden">{coverageStatus.label}</span>
              </span>
            )}
            <span className="map-route-type">{workout.type.name}</span>
            <small>{formatWorkoutDate(workout, preferences)}</small>
          </button>
        );
        return (
          <li
            key={workout.id}
            data-workout-id={workout.id}
            className={`${highlightedWorkoutId === workout.id ? "is-hovered" : ""}${focusedWorkoutId === workout.id ? " is-focused" : ""}${focusDisabled ? " is-coverage-unavailable" : ""}`}
          >
            <label
              className="map-route-toggle"
              onPointerEnter={() => onHover(undefined)}
              onClick={(event) => event.stopPropagation()}
            >
              <input
                type="checkbox"
                aria-label={`Show ${workout.type.name} from ${formatWorkoutDate(workout, preferences)}`}
                checked={visibleIDs === undefined || visibleIDs.includes(workout.id)}
                onChange={() => onToggle(workout.id)}
              />
            </label>
            {focusDisabled ? (
              <Tooltip
                portal
                content={unavailableCoverageReason(workout)}
                className="map-route-readiness-tooltip"
                label={`${workout.type.name}: ${unavailableCoverageReason(workout)}`}
              >
                {focusControl}
              </Tooltip>
            ) : (
              focusControl
            )}
          </li>
        );
      })}
    </ol>
  );
}

function safeCount(value: number) {
  return Number.isSafeInteger(value) && value >= 0
    ? new Intl.NumberFormat("en-US").format(value)
    : "Unavailable";
}

function safeDuration(milliseconds: number) {
  return Number.isFinite(milliseconds) && milliseconds >= 0
    ? `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(milliseconds / 1000)} s`
    : "Unavailable";
}

function DiagnosticReviewCard({
  run,
  pending,
  error,
  selectedPortionOrdinal,
  savePending,
  saveError,
  onRetry,
  onFit,
  onCopyFocus,
  onOverallLabel,
  onSegmentLabel,
}: {
  run?: CoverageDiagnosticRun;
  pending: boolean;
  error: string;
  selectedPortionOrdinal?: number;
  savePending: boolean;
  saveError: string;
  onRetry: () => void;
  onFit: () => void;
  onCopyFocus: () => void;
  onOverallLabel: (label: CoverageDiagnosticOverallLabel) => void;
  onSegmentLabel: (ordinal: number, label: CoverageDiagnosticSegmentLabelValue) => void;
}) {
  const [copiedSegmentId, setCopiedSegmentId] = useState("");
  const portion = run?.overlay.features.find(
    (feature) => feature.properties.portionOrdinal === selectedPortionOrdinal,
  );
  const segmentLabel = run?.labels.segments.find(
    (label) => label.portionOrdinal === selectedPortionOrdinal,
  )?.label;
  const policyLabel = run?.pathPolicyVersion.replace("coverage-path-policy-experimental-", "");
  const unavailableRegions = run?.outcome === "no_evidence" ? run.unavailableRegions : [];
  const unavailableMessage = unavailableRegions?.length
    ? `Coverage is unavailable because OSM data is not loaded for ${new Intl.ListFormat("en", { style: "long", type: "conjunction" }).format(unavailableRegions.map((region) => `${region.displayName} (${region.regionId})`))}.`
    : "";
  return (
    <section className="coverage-diagnostic-card" aria-label="Coverage diagnostic review">
      <header>
        <div>
          <span className="coverage-diagnostic-eyebrow">
            Owner diagnostic{policyLabel ? ` / ${policyLabel}` : ""}
          </span>
          <h2>Coverage review</h2>
        </div>
        {run && !unavailableMessage && (
          <div className="coverage-diagnostic-actions">
            <button type="button" className="coverage-diagnostic-fit" onClick={onFit}>
              Fit
            </button>
            <button
              type="button"
              className="coverage-diagnostic-rerun"
              disabled={pending || savePending}
              onClick={onRetry}
            >
              Rerun
            </button>
          </div>
        )}
      </header>
      {pending && (
        <div
          className="coverage-diagnostic-preparing"
          role="status"
          aria-live="polite"
          aria-busy="true"
        >
          <p>Preparing diagnostic overlay...</p>
        </div>
      )}
      {error && (
        <div className="coverage-diagnostic-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={onRetry}>
            Retry
          </button>
        </div>
      )}
      {unavailableMessage && (
        <div className="coverage-diagnostic-error" role="alert">
          <span>{unavailableMessage}</span>
          <button type="button" onClick={onRetry}>
            Retry
          </button>
        </div>
      )}
      {run && !unavailableMessage && (
        <>
          <dl className="coverage-diagnostic-counts">
            <div>
              <dt>Matched</dt>
              <dd>{safeCount(run.counts.matchedPoints)}</dd>
            </div>
            <div>
              <dt>Ambiguous</dt>
              <dd>{safeCount(run.counts.ambiguousPoints)}</dd>
            </div>
            <div>
              <dt>Unmatched</dt>
              <dd>{safeCount(run.counts.unmatchedPoints)}</dd>
            </div>
            <div>
              <dt>Runtime</dt>
              <dd>{safeDuration(run.counts.durationMilliseconds)}</dd>
            </div>
          </dl>
          <fieldset className="coverage-diagnostic-labels" disabled={savePending}>
            <legend>Overall result</legend>
            <div role="group">
              {(["correct", "incorrect", "uncertain"] as const).map((label) => (
                <button
                  key={label}
                  type="button"
                  aria-pressed={run.labels.overall === label}
                  onClick={() => onOverallLabel(label)}
                >
                  {label[0].toUpperCase() + label.slice(1)}
                </button>
              ))}
            </div>
          </fieldset>
          <div className="coverage-diagnostic-portion">
            {portion ? (
              <>
                <h3>Traversal {portion.properties.portionOrdinal + 1}</h3>
                <p>
                  {Number.isFinite(portion.properties.traversedMeters)
                    ? `${portion.properties.traversedMeters.toFixed(1)} m traveled`
                    : "Distance unavailable"}{" "}
                  <span aria-hidden="true">/</span> {portion.properties.evidenceClass}{" "}
                  <span aria-hidden="true">/</span> {portion.properties.direction}
                </p>
                <p className="coverage-diagnostic-segment">
                  <span>OSM segment {portion.properties.physicalSegmentId}</span>
                  <button
                    type="button"
                    className="coverage-diagnostic-copy"
                    aria-label={
                      copiedSegmentId === portion.properties.physicalSegmentId
                        ? "OSM segment ID copied"
                        : "Copy OSM segment ID"
                    }
                    title={
                      copiedSegmentId === portion.properties.physicalSegmentId
                        ? "Copied"
                        : "Copy OSM segment ID"
                    }
                    onClick={() => {
                      void navigator.clipboard
                        .writeText(portion.properties.physicalSegmentId)
                        .then(() => {
                          setCopiedSegmentId(portion.properties.physicalSegmentId);
                          onCopyFocus();
                        })
                        .catch(() => setCopiedSegmentId(""));
                    }}
                  >
                    <svg
                      aria-hidden="true"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.8"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    >
                      <rect x="8" y="8" width="11" height="11" rx="2" />
                      <path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />
                    </svg>
                  </button>
                </p>
                <fieldset className="coverage-diagnostic-labels" disabled={savePending}>
                  <legend>Expected result</legend>
                  <div role="group">
                    {(["expected", "unexpected", "uncertain"] as const).map((label) => (
                      <button
                        key={label}
                        type="button"
                        aria-pressed={segmentLabel === label}
                        onClick={() => onSegmentLabel(portion.properties.portionOrdinal, label)}
                      >
                        {label[0].toUpperCase() + label.slice(1)}
                      </button>
                    ))}
                  </div>
                </fieldset>
              </>
            ) : (
              <p>Select a traversal on the map to review it.</p>
            )}
          </div>
          {(savePending || saveError) && (
            <p className="coverage-diagnostic-save" role="status" aria-live="polite">
              {savePending ? "Saving labels..." : saveError}
            </p>
          )}
        </>
      )}
    </section>
  );
}

const COVERAGE_BUCKET_LABELS = [
  "1 workout",
  "2 workouts",
  "3-5 workouts",
  "6-10 workouts",
  "11-25 workouts",
  "26+ workouts",
];

function CoverageOverviewCard({
  fitDisabled,
  onFit,
  onOpen,
}: {
  fitDisabled: boolean;
  onFit: () => void;
  onOpen: () => void;
}) {
  return (
    <section className="coverage-overview-card" aria-label="Coverage statistics">
      <header>
        <div>
          <span className="coverage-diagnostic-eyebrow">Checked routes</span>
          <h2>Road coverage</h2>
        </div>
        <button
          type="button"
          className="coverage-diagnostic-fit"
          disabled={fitDisabled}
          onClick={onFit}
        >
          Fit
        </button>
      </header>
      <div className="coverage-legend" aria-label="Coverage workout count legend">
        {COVERAGE_BUCKET_LABELS.map((label, index) => (
          <div key={label}>
            <span className="coverage-swatch-pair" aria-hidden="true">
              <span style={{ background: COVERAGE_RANGE_COLORS[index] }} />
              <span style={{ background: COVERAGE_FOCUS_COLORS[index] }} />
            </span>
            <span>{label}</span>
          </div>
        ))}
      </div>
      <button type="button" className="secondary coverage-rankings-button" onClick={onOpen}>
        Coverage by road...
      </button>
    </section>
  );
}

const ROAD_COVERAGE_COLUMNS: ReadonlyArray<{
  field: RoadCoverageSortField;
  label: string;
  initial: "asc" | "desc";
}> = [
  { field: "rangeCount", label: "Workouts", initial: "desc" },
  { field: "name", label: "Road/Path/Park", initial: "asc" },
  { field: "cityOrRegion", label: "City/County/Region", initial: "asc" },
  { field: "rangeFirst", label: "Earliest visit", initial: "asc" },
  { field: "allTimeFirst", label: "First visit ever", initial: "asc" },
  { field: "rangeLatest", label: "Most recent visit", initial: "desc" },
  { field: "allTimeLatest", label: "Last visit ever", initial: "desc" },
];
const TABLE_LOADING_DELAY_MS = 500;
const ROAD_COVERAGE_CACHE_MAX_AGE_MS = 5 * 60 * 1000;

function coverageEntityName(entity: RoadCoverageEntity) {
  if (entity.name) return entity.name;
  if (entity.entityKind === "park") return "Unnamed park";
  switch (entity.broadClass) {
    case "road":
      return "Unnamed road";
    case "cycleway":
      return "Bike path";
    case "footway":
      return "Pedestrian path";
    case "trail":
      return "Trail";
    default:
      return "Unnamed path";
  }
}

export function coverageRegionLabel(regionId: string) {
  const slug = regionId.includes(":") ? regionId.slice(regionId.lastIndexOf(":") + 1) : regionId;
  return (
    slug
      .split(/[-_]+/)
      .filter(Boolean)
      .map((word) => word.charAt(0).toLocaleUpperCase() + word.slice(1).toLocaleLowerCase())
      .join(" ") || regionId
  );
}

function coverageEntityPlace(
  entity: Pick<RoadCoverageEntity, "localityName" | "regionId" | "regionName">,
) {
  return entity.localityName ?? entity.regionName ?? coverageRegionLabel(entity.regionId);
}

export function CoverageCellValue({ text }: { text: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [truncated, setTruncated] = useState(false);
  useEffect(() => {
    const element = ref.current!;
    let active = true;
    const measure = () => {
      if (active)
        setTruncated(element.clientWidth > 0 && element.scrollWidth > element.clientWidth);
    };
    measure();
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    observer?.observe(element);
    window.addEventListener("resize", measure);
    document.fonts?.addEventListener("loadingdone", measure);
    void document.fonts?.ready.then(measure);
    return () => {
      active = false;
      observer?.disconnect();
      window.removeEventListener("resize", measure);
      document.fonts?.removeEventListener("loadingdone", measure);
    };
  }, [text]);
  return (
    <Tooltip
      content={text}
      className="coverage-cell-tooltip"
      enabled={truncated}
      focusable={false}
      portal
      singleLine
    >
      <span ref={ref} className="coverage-cell-value">
        {text}
      </span>
    </Tooltip>
  );
}

function RoadCoverageDialog({
  open,
  selection,
  pageSize,
  cache,
  onCacheChange,
  onOpenChange,
  onReturnFocus,
  onSelectionUnavailable,
  onShowEntity,
  onShowWorkout,
}: {
  open: boolean;
  selection?: MapSelection;
  pageSize: number;
  onOpenChange: (open: boolean) => void;
  cache?: RoadCoverageCache;
  onCacheChange?: (cache?: RoadCoverageCache) => void;
  onReturnFocus: () => void;
  onSelectionUnavailable: () => void;
  onShowEntity: (entity: RoadCoverageEntity) => void;
  onShowWorkout: (workoutId: string) => void;
}) {
  const searchID = useId();
  const [input, setInput] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<RoadCoverageSort>({ field: "rangeCount", direction: "desc" });
  const [entities, setEntities] = useState<RoadCoverageEntity[]>();
  const [loading, setLoading] = useState(false);
  const [showLoading, setShowLoading] = useState(false);
  const [rowSlots, setRowSlots] = useState(2);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [showUnnamed, setShowUnnamed] = useState(false);
  const [error, setError] = useState<{ message: string; retry: () => void }>();
  const previousRangeKey = useRef<string | undefined>(undefined);
  const rangeKey = selection
    ? `${selection.range.startDate}/${selection.range.endDate}`
    : undefined;
  useEffect(() => {
    if (!open) {
      setError(undefined);
      setEntities(undefined);
    }
  }, [open]);
  useEffect(() => {
    if (!rangeKey) return;
    if (previousRangeKey.current && previousRangeKey.current !== rangeKey) {
      setInput("");
      setSearch("");
      setShowUnnamed(false);
      setPage(1);
    }
    previousRangeKey.current = rangeKey;
  }, [rangeKey]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setSearch(input.trim());
      setPage(1);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [input]);
  useEffect(() => {
    if (!open || !selection) return;
    const cacheKey = JSON.stringify([
      selection.range.startDate,
      selection.range.endDate,
      selection.dataGeneration,
      selection.coverageDisplayPolicy,
      selection.coverageDisplayRevision,
    ]);
    if (cache?.key === cacheKey && Date.now() - cache.loadedAt < ROAD_COVERAGE_CACHE_MAX_AGE_MS) {
      const viewportRows = Math.max(5, Math.floor((window.innerHeight - 330) / 34));
      const initialVisibleCount = cache.entities.filter(
        (entity) => entity.entityKind !== "path" || Boolean(entity.name),
      ).length;
      setRowSlots(Math.max(1, Math.min(pageSize, viewportRows, initialVisibleCount || 1)));
      setEntities(cache.entities);
      setLoading(false);
      setShowLoading(false);
      setError(undefined);
      return;
    }
    const controller = new AbortController();
    const loadingTimer = window.setTimeout(() => setShowLoading(true), TABLE_LOADING_DELAY_MS);
    const loadBulk = () => {
      const params = new URLSearchParams({
        generation: String(selection.dataGeneration),
        bulk: "true",
      });
      return api<RoadCoverageList>(
        `/api/map-selections/${encodeURIComponent(selection.id)}/coverage/paths?${params}`,
        { signal: controller.signal },
      );
    };
    setEntities(undefined);
    setLoading(true);
    setShowLoading(false);
    setError(undefined);
    setRowSlots(2);
    void (async () => {
      const result = await loadBulk();
      if (
        result.items.length !== result.pagination.totalItems ||
        result.pagination.page !== 1 ||
        result.pagination.totalPages !== (result.items.length ? 1 : 0)
      ) {
        throw new Error("Incomplete bulk road coverage response");
      }
      const items = result.items;
      if (controller.signal.aborted) return;
      window.clearTimeout(loadingTimer);
      const viewportRows = Math.max(5, Math.floor((window.innerHeight - 330) / 34));
      const initialVisibleCount = items.filter(
        (entity) => entity.entityKind !== "path" || Boolean(entity.name),
      ).length;
      setRowSlots(Math.max(1, Math.min(pageSize, viewportRows, initialVisibleCount || 1)));
      setEntities(items);
      setLoading(false);
      setShowLoading(false);
      onCacheChange?.({ key: cacheKey, loadedAt: Date.now(), entities: items });
    })().catch((loadError) => {
      if (controller.signal.aborted) return;
      window.clearTimeout(loadingTimer);
      if (loadError instanceof ApiError && loadError.status === 404) {
        setEntities(undefined);
        setLoading(true);
        setShowLoading(true);
        setError(undefined);
        onSelectionUnavailable();
        return;
      }
      setLoading(false);
      setShowLoading(false);
      setError({
        message:
          loadError instanceof ApiError && loadError.status === 422
            ? "Road coverage exceeds the bulk limit of 50,000 entities or 64 MiB. Choose a smaller date range."
            : "Road coverage could not be loaded.",
        retry: () => setLoadAttempt((value) => value + 1),
      });
    });
    return () => {
      controller.abort();
      window.clearTimeout(loadingTimer);
    };
  }, [
    cache?.key,
    loadAttempt,
    open,
    pageSize,
    selection?.dataGeneration,
    selection?.coverageDisplayPolicy,
    selection?.coverageDisplayRevision,
    selection?.id,
    rangeKey,
  ]);
  const visibleEntities = useMemo(() => {
    if (!entities) return [];
    const needle = search.toLocaleLowerCase();
    const eligible = showUnnamed
      ? entities
      : entities.filter((entity) => entity.entityKind !== "path" || Boolean(entity.name));
    const filtered = needle
      ? eligible.filter((entity) =>
          [coverageEntityName(entity), coverageEntityPlace(entity)]
            .join(" ")
            .toLocaleLowerCase()
            .includes(needle),
        )
      : eligible;
    const value = (entity: RoadCoverageEntity): string | number => {
      switch (sort.field) {
        case "rangeCount":
          return entity.rangeWorkoutCount;
        case "name":
          return coverageEntityName(entity);
        case "cityOrRegion":
          return coverageEntityPlace(entity);
        case "rangeFirst":
          return entity.rangeFirstDate;
        case "allTimeFirst":
          return entity.allTimeFirstDate;
        case "rangeLatest":
          return entity.rangeLatestDate;
        case "allTimeLatest":
          return entity.allTimeLatestDate;
      }
    };
    const collator = new Intl.Collator(undefined, { sensitivity: "base" });
    return [...filtered].sort((left, right) => {
      const leftValue = value(left),
        rightValue = value(right);
      const comparison =
        typeof leftValue === "number" && typeof rightValue === "number"
          ? leftValue - rightValue
          : collator.compare(String(leftValue), String(rightValue));
      if (comparison) return sort.direction === "asc" ? comparison : -comparison;
      if (sort.field !== "name") {
        const nameComparison = collator.compare(
          coverageEntityName(left),
          coverageEntityName(right),
        );
        if (nameComparison) return nameComparison;
      }
      if (sort.field !== "cityOrRegion") {
        const placeComparison = collator.compare(
          coverageEntityPlace(left),
          coverageEntityPlace(right),
        );
        if (placeComparison) return placeComparison;
      }
      return (
        left.entityKind.localeCompare(right.entityKind) ||
        left.entityId.localeCompare(right.entityId)
      );
    });
  }, [entities, search, showUnnamed, sort.direction, sort.field]);
  const totalPages = Math.ceil(visibleEntities.length / pageSize);
  const pageItems = visibleEntities.slice((page - 1) * pageSize, page * pageSize);
  useEffect(() => {
    if (!open || !entities) return;
    if (totalPages === 0 && page !== 1) setPage(1);
    else if (totalPages > 0 && page > totalPages) setPage(totalPages);
  }, [entities, open, page, totalPages]);
  const inRange = (date: string) =>
    Boolean(selection && date >= selection.range.startDate && date <= selection.range.endDate);
  const updateSort = (field: RoadCoverageSortField) => {
    const column = ROAD_COVERAGE_COLUMNS.find((candidate) => candidate.field === field)!;
    setSort((current) =>
      current.field === field
        ? { field, direction: current.direction === "asc" ? "desc" : "asc" }
        : { field, direction: column.initial },
    );
    setPage(1);
  };
  const dateLink = (date: string, workoutId: string, enabled = true) =>
    enabled ? (
      <button
        type="button"
        className="coverage-table-link"
        onClick={() => {
          onOpenChange(false);
          onShowWorkout(workoutId);
        }}
      >
        <CoverageCellValue text={formatDateOnly(date)} />
      </button>
    ) : (
      <CoverageCellValue text={formatDateOnly(date)} />
    );
  if (error)
    return (
      <Dialog.Root open={open} onOpenChange={onOpenChange}>
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content road-coverage-error-dialog"
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              onReturnFocus();
            }}
          >
            <div className="dialog-heading">
              <div>
                <Dialog.Title>Road Coverage</Dialog.Title>
                <Dialog.Description>{error.message}</Dialog.Description>
              </div>
            </div>
            <div className="dialog-actions">
              <button type="button" onClick={error.retry}>
                Retry
              </button>
              <Dialog.Close type="button" className="secondary">
                Close
              </Dialog.Close>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    );
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content road-coverage-dialog"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onReturnFocus();
          }}
        >
          <div className="dialog-heading">
            <div>
              <Dialog.Title>Road Coverage</Dialog.Title>
              <Dialog.Description className="visually-hidden">
                Coverage rankings for this date range.
              </Dialog.Description>
            </div>
            <Dialog.Close className="icon-button" aria-label="Close Road Coverage">
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
            <label className="visually-hidden" htmlFor={searchID}>
              Search roads, paths, parks, cities, or regions
            </label>
            <input
              id={searchID}
              type="search"
              value={input}
              onChange={(event) => setInput(event.target.value)}
              placeholder="Search roads, paths, parks, cities, or regions"
            />
          </div>
          <div
            className={`road-coverage-table-wrap${entities ? "" : " is-loading"}`}
            aria-busy={loading}
            style={{ "--road-coverage-row-slots": rowSlots } as CSSProperties}
          >
            <table className="workout-table road-coverage-table">
              <thead>
                <tr>
                  {ROAD_COVERAGE_COLUMNS.map((column) => {
                    const selected = sort.field === column.field;
                    return (
                      <th
                        key={column.field}
                        scope="col"
                        aria-sort={
                          selected
                            ? sort.direction === "asc"
                              ? "ascending"
                              : "descending"
                            : "none"
                        }
                      >
                        <button type="button" onClick={() => updateSort(column.field)}>
                          <span className="column-label">{column.label}</span>
                          <span className="sort-indicator" aria-hidden="true">
                            {selected ? (
                              sort.direction === "asc" ? (
                                <>&#9650;</>
                              ) : (
                                <>&#9660;</>
                              )
                            ) : (
                              <>&#9650; &#9660;</>
                            )}
                          </span>
                        </button>
                      </th>
                    );
                  })}
                </tr>
              </thead>
              <tbody>
                {showLoading && (
                  <tr className="road-coverage-state-row">
                    <td colSpan={ROAD_COVERAGE_COLUMNS.length} role="status">
                      Loading coverage...
                    </td>
                  </tr>
                )}
                {!loading &&
                  pageItems.map((entity) => (
                    <tr key={`${entity.entityKind}-${entity.entityId}`}>
                      <td>
                        <span className="coverage-count-lane">
                          <CoverageCellValue text={entity.rangeWorkoutCount.toLocaleString()} />
                        </span>
                      </td>
                      <td>
                        <button
                          type="button"
                          className="coverage-table-link"
                          onClick={() => onShowEntity(entity)}
                        >
                          <CoverageCellValue text={coverageEntityName(entity)} />
                        </button>
                      </td>
                      <td>
                        <CoverageCellValue text={coverageEntityPlace(entity)} />
                      </td>
                      <td>{dateLink(entity.rangeFirstDate, entity.rangeFirstWorkoutId)}</td>
                      <td>
                        {dateLink(
                          entity.allTimeFirstDate,
                          entity.allTimeFirstWorkoutId,
                          inRange(entity.allTimeFirstDate),
                        )}
                      </td>
                      <td>{dateLink(entity.rangeLatestDate, entity.rangeLatestWorkoutId)}</td>
                      <td>
                        {dateLink(
                          entity.allTimeLatestDate,
                          entity.allTimeLatestWorkoutId,
                          inRange(entity.allTimeLatestDate),
                        )}
                      </td>
                    </tr>
                  ))}
                {!loading && pageItems.length === 0 && (
                  <tr className="road-coverage-state-row">
                    <td colSpan={ROAD_COVERAGE_COLUMNS.length}>
                      No roads, paths, or parks match this search.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          {entities && (
            <footer className="road-coverage-footer">
              <label className="road-coverage-unnamed">
                <input
                  type="checkbox"
                  checked={showUnnamed}
                  onChange={(event) => {
                    setShowUnnamed(event.target.checked);
                    setPage(1);
                  }}
                />
                Show unnamed roads and paths
              </label>
              <nav className="pagination" aria-label="Road coverage pages">
                <button
                  type="button"
                  className="secondary"
                  disabled={page <= 1 || totalPages === 0}
                  onClick={() => setPage((value) => value - 1)}
                >
                  Previous
                </button>
                <span>
                  Page {Math.max(1, page)} of {Math.max(1, totalPages)}
                </span>
                <button
                  type="button"
                  className="secondary"
                  disabled={totalPages === 0 || page >= totalPages}
                  onClick={() => setPage((value) => value + 1)}
                >
                  Next
                </button>
              </nav>
            </footer>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export default function MapPage({
  config,
  preferences,
  csrfToken,
  dateRange,
  onDateRangeSelected,
  onRecentDateRangesSaved,
  sort = DEFAULT_WORKOUT_SORT,
  persistedWorkoutIds,
  onWorkoutSelectionChange,
  persistedAvailableWorkouts,
  onAvailableWorkoutsChange,
  persistedFocusedWorkoutId,
  onFocusedWorkoutChange,
  persistedMapMode,
  onMapModeChange,
  roadCoverageCache,
  onRoadCoverageCacheChange,
  explicitCanvasFocusRequest = 0,
  deletions,
  availableWorkoutsLoaded,
}: {
  config: PublicConfig;
  preferences: Preferences;
  csrfToken: string;
  dateRange: DateRangePreference;
  onDateRangeSelected: (range: DateRangePreference) => void;
  onRecentDateRangesSaved?: (ranges: string[]) => void;
  sort?: WorkoutSort;
  persistedWorkoutIds?: string[];
  onWorkoutSelectionChange?: (workoutIds?: string[]) => void;
  persistedAvailableWorkouts?: MapSelectionWorkout[];
  onAvailableWorkoutsChange?: (workouts: MapSelectionWorkout[]) => void;
  persistedFocusedWorkoutId?: string;
  onFocusedWorkoutChange?: (workoutId?: string) => void;
  persistedMapMode?: MapMode;
  onMapModeChange?: (mode: MapMode) => void;
  roadCoverageCache?: RoadCoverageCache;
  onRoadCoverageCacheChange?: (cache?: RoadCoverageCache) => void;
  explicitCanvasFocusRequest?: number;
  deletions?: { revision: number; events: AcceptedWorkoutDeletion[] };
  availableWorkoutsLoaded?: boolean;
}) {
  const recentRanges = useRecentDateRanges(preferences, onRecentDateRangesSaved);
  const deletionRevision = deletions?.revision ?? 0;
  const deletionRef = useRef(deletions);
  deletionRef.current = deletions;
  function deletedWorkout(workout: Pick<MapSelectionWorkout, "id" | "localStartDate">) {
    return (
      deletionRef.current?.events.some((event) =>
        event.workoutIds.includes(workout.id.toUpperCase()),
      ) ?? false
    );
  }
  const [selection, setSelection] = useState<MapSelection>();
  const [selectionPending, setSelectionPending] = useState(true);
  const [selectionPendingDismissed, setSelectionPendingDismissed] = useState(false);
  const [selectionError, setSelectionError] = useState("");
  const [baseMapError, setBaseMapError] = useState("");
  const [rangeError, setRangeError] = useState("");
  const [overrideFamilyId, setOverrideFamilyId] = useState<string>();
  const [stickyAutomaticFamilyId, setStickyAutomaticFamilyId] = useState(
    config.baseMaps.fallbackFamilyId,
  );
  const [hoveredWorkoutId, setHoveredWorkoutId] = useState<string>();
  const [coverageFocusResult, setCoverageFocus] = useState<{
    key: string;
    value: CoverageFocusFeatureCollection;
  }>();
  const [fitRequest, setFitRequest] = useState<{
    key: number;
    bounds: RouteBounds;
    focusCanvas?: boolean;
  }>();
  const [canvasFocusRequest, setCanvasFocusRequest] = useState(0);
  useEffect(() => {
    if (explicitCanvasFocusRequest > 0) setCanvasFocusRequest((value) => value + 1);
  }, [explicitCanvasFocusRequest]);
  const [localFocusedWorkoutId, setLocalFocusedWorkoutId] = useState<string>();
  const [sheetOpen, setSheetOpen] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [localMapMode, setLocalMapMode] = useState<MapMode>("routes");
  const mapMode = preferences.coverageDiagnosticsEnabled
    ? localMapMode
    : onMapModeChange
      ? (persistedMapMode ?? "routes")
      : localMapMode;
  const mapModeRef = useRef(mapMode);
  mapModeRef.current = mapMode;
  const fittedModeRef = useRef(mapMode);
  const setMapMode = (mode: MapMode) =>
    preferences.coverageDiagnosticsEnabled
      ? setLocalMapMode(mode)
      : onMapModeChange
        ? onMapModeChange(mode)
        : setLocalMapMode(mode);
  const [roadCoverageOpen, setRoadCoverageOpen] = useState(false);
  const [coverageHighlight, setCoverageHighlight] = useState<CoverageHighlight>();
  const [coverageDetailStatus, setCoverageDetailStatus] = useState<{
    entity: RoadCoverageEntity;
    loading: boolean;
    unavailable?: boolean;
  }>();
  const coverageDetailCacheRef = useRef(new CoverageDetailCache());
  const coverageDetailSequenceRef = useRef(0);
  const coverageDetailRequestRef = useRef<
    | {
        controller: AbortController;
        timer: number;
      }
    | undefined
  >(undefined);
  const [diagnostic, setDiagnostic] = useState<CoverageDiagnosticRun>();
  const [singleRawRoute, setSingleRawRoute] = useState<LoadedRawRoute>();
  const [diagnosticPending, setDiagnosticPending] = useState(false);
  const [diagnosticError, setDiagnosticError] = useState("");
  const [diagnosticRetry, setDiagnosticRetry] = useState(0);
  const [selectionRefresh, setSelectionRefresh] = useState(0);
  const [hoveredPortionOrdinal, setHoveredPortionOrdinal] = useState<number>();
  const [lockedPortionOrdinal, setLockedPortionOrdinal] = useState<number>();
  const [labelSavePending, setLabelSavePending] = useState(false);
  const [labelSaveError, setLabelSaveError] = useState("");
  const [rawRouteHidden, setRawRouteHidden] = useState(false);
  const [nonFocusedRoutesHidden, setNonFocusedRoutesHidden] = useState(false);
  const [nonFocusedCoverageHidden, setNonFocusedCoverageHidden] = useState(false);
  const unavailableRequestedIdsRef = useRef(new Set<string>());
  const requestedIds = useRef(requestedWorkoutIds(window.location.search)).current.filter(
    (id) =>
      !unavailableRequestedIdsRef.current.has(id) && !deletedWorkout({ id, localStartDate: null }),
  );
  const requestedFocusActiveRef = useRef(requestedIds.length === 1);
  const initialListFocusId = useRef(
    requestedIds.length === 1 ? requestedIds[0] : persistedFocusedWorkoutId,
  ).current;
  const [listScrollRequest, setListScrollRequest] = useState<
    { key: number; workoutId: string } | undefined
  >(() => (initialListFocusId ? { key: 1, workoutId: initialListFocusId } : undefined));
  const [localWorkoutIds, setLocalWorkoutIds] = useState<string[] | undefined>(() =>
    requestedIds.length ? requestedIds : undefined,
  );
  const selectedWorkoutIds = onWorkoutSelectionChange ? persistedWorkoutIds : localWorkoutIds;
  const focusedWorkoutId = onFocusedWorkoutChange
    ? persistedFocusedWorkoutId
    : localFocusedWorkoutId;
  const selectionFocusedWorkoutId =
    focusedWorkoutId ?? (requestedFocusActiveRef.current ? requestedIds[0] : undefined);
  const selectionFocusRef = useRef(selectionFocusedWorkoutId);
  selectionFocusRef.current = selectionFocusedWorkoutId;
  const [localAvailableWorkouts, setLocalAvailableWorkouts] = useState<MapSelectionWorkout[]>([]);
  const availableWorkouts = onAvailableWorkoutsChange
    ? (persistedAvailableWorkouts ?? [])
    : localAvailableWorkouts;
  const availableWorkoutsRef = useRef(availableWorkouts);
  availableWorkoutsRef.current = availableWorkouts;
  const recoveredSelectionRef = useRef<string | undefined>(undefined);
  const catalogLoadedRef = useRef(
    availableWorkoutsLoaded ?? Boolean(persistedAvailableWorkouts?.length),
  );
  if (availableWorkoutsLoaded !== undefined) catalogLoadedRef.current = availableWorkoutsLoaded;
  const rangeSaveSequence = useRef(0);
  const rangeSaveChain = useRef<Promise<void>>(Promise.resolve());
  const activeSelectionRef = useRef<string | undefined>(undefined);
  const selectionPendingRef = useRef(false);
  const selectionRefreshAtRef = useRef(0);
  const pendingSelectionFitRef = useRef<"all" | "requested" | undefined>(
    requestedIds.length === 1 || persistedFocusedWorkoutId ? "requested" : "all",
  );
  const hoveredWorkoutRef = useRef<string | undefined>(undefined);
  const hoverCandidateRef = useRef<string | undefined>(undefined);
  const hoverTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const temporaryCoverageSequenceRef = useRef(0);
  const coverageFocusCacheRef = useRef(new FocusGeometryCache<CoverageFocusFeatureCollection>());
  const coverageFocusCacheContextRef = useRef("");
  const hoveredPortionRef = useRef<number | undefined>(undefined);
  const portionHoverCandidateRef = useRef<number | undefined>(undefined);
  const portionHoverTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const diagnosticSequenceRef = useRef(0);
  const labelSequenceRef = useRef(0);
  const rawRouteCacheRef = useRef(new FocusGeometryCache<LoadedRawRoute>());
  const rawGenerationRef = useRef(selection?.dataGeneration);
  rawGenerationRef.current = selection?.dataGeneration;
  const visibleWorkoutIds = new Set(
    selectedWorkoutIds ?? availableWorkouts.map((workout) => workout.id),
  );
  const visibleWorkoutIdsRef = useRef(visibleWorkoutIds);
  visibleWorkoutIdsRef.current = visibleWorkoutIds;
  selectionPendingRef.current = selectionPending;

  function refreshSelection() {
    const now = Date.now();
    if (selectionPendingRef.current || now - selectionRefreshAtRef.current < 1000) return;
    selectionRefreshAtRef.current = now;
    setSelectionRefresh((value) => value + 1);
  }

  function updateWorkoutSelection(workoutIds?: string[]) {
    if (onWorkoutSelectionChange) onWorkoutSelectionChange(workoutIds);
    else setLocalWorkoutIds(workoutIds);
  }

  function updateFocusedWorkout(workoutId?: string) {
    if (onFocusedWorkoutChange) onFocusedWorkoutChange(workoutId);
    else setLocalFocusedWorkoutId(workoutId);
  }

  function clearFocusedWorkout() {
    requestedFocusActiveRef.current = false;
    const location = new URL(window.location.href);
    location.searchParams.delete("workoutId");
    history.replaceState(
      history.state,
      "",
      `${location.pathname}${location.search}${location.hash}`,
    );
    updateFocusedWorkout(undefined);
  }

  function updateAvailableWorkouts(workouts: MapSelectionWorkout[]) {
    catalogLoadedRef.current = true;
    if (onAvailableWorkoutsChange) onAvailableWorkoutsChange(workouts);
    else setLocalAvailableWorkouts(workouts);
  }

  function setRouteHover(workoutId?: string) {
    hoveredWorkoutRef.current = workoutId;
    setHoveredWorkoutId(workoutId);
  }

  function cancelRouteHover(workoutId?: string) {
    if (!workoutId || hoverCandidateRef.current === workoutId) {
      if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
      hoverTimerRef.current = undefined;
      hoverCandidateRef.current = undefined;
    }
    if (!workoutId || hoveredWorkoutRef.current === workoutId) setRouteHover(undefined);
  }

  function requestRouteHover(workoutId?: string) {
    if (!workoutId || !visibleWorkoutIdsRef.current.has(workoutId)) {
      cancelRouteHover();
      return;
    }
    if (hoveredWorkoutRef.current === workoutId || hoverCandidateRef.current === workoutId) return;
    if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    hoverCandidateRef.current = workoutId;
    hoverTimerRef.current = setTimeout(() => {
      hoverTimerRef.current = undefined;
      hoverCandidateRef.current = undefined;
      if (visibleWorkoutIdsRef.current.has(workoutId)) setRouteHover(workoutId);
    }, ROUTE_HOVER_DELAY_MS);
  }

  function setDiagnosticHover(portionOrdinal?: number) {
    hoveredPortionRef.current = portionOrdinal;
    setHoveredPortionOrdinal(portionOrdinal);
  }

  function requestDiagnosticHover(portionOrdinal?: number) {
    if (portionHoverTimerRef.current) clearTimeout(portionHoverTimerRef.current);
    portionHoverTimerRef.current = undefined;
    portionHoverCandidateRef.current = undefined;
    if (portionOrdinal === undefined) {
      setDiagnosticHover(undefined);
      return;
    }
    if (hoveredPortionRef.current === portionOrdinal) return;
    setDiagnosticHover(undefined);
    portionHoverCandidateRef.current = portionOrdinal;
    portionHoverTimerRef.current = setTimeout(() => {
      portionHoverTimerRef.current = undefined;
      if (portionHoverCandidateRef.current !== portionOrdinal) return;
      portionHoverCandidateRef.current = undefined;
      setDiagnosticHover(portionOrdinal);
    }, DIAGNOSTIC_HOVER_DELAY_MS);
  }

  useEffect(() => {
    if (requestedIds.length && selectedWorkoutIds !== undefined) {
      const merged = [...new Set([...selectedWorkoutIds, ...requestedIds])].sort();
      if (merged.length !== selectedWorkoutIds.length) updateWorkoutSelection(merged);
    }
  }, [requestedIds.join(",")]);

  useEffect(() => {
    if (requestedIds.length !== 1) return;
    updateFocusedWorkout(requestedIds[0]);
  }, [requestedIds.join(",")]);

  useEffect(() => {
    if (!deletionRevision) return;
    rawRouteCacheRef.current.clear();
    cancelRouteHover();
    temporaryCoverageSequenceRef.current++;
    diagnosticSequenceRef.current++;
    labelSequenceRef.current++;
    coverageFocusCacheRef.current.clear();
    setCoverageFocus(undefined);
    setCoverageHighlight(undefined);
    setDiagnostic(undefined);
    setDiagnosticPending(false);
    setSingleRawRoute(undefined);
    setSelection(undefined);
    setFitRequest(undefined);
    if (
      requestedWorkoutIds(window.location.search).some((id) =>
        deletedWorkout({ id, localStartDate: null }),
      ) ||
      (selectionFocusedWorkoutId &&
        deletedWorkout(
          availableWorkoutsRef.current.find((item) => item.id === selectionFocusedWorkoutId) ?? {
            id: selectionFocusedWorkoutId,
            localStartDate: null,
          },
        ))
    )
      clearFocusedWorkout();
  }, [deletionRevision]);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    const revision = deletionRevision;
    const current = () => active && revision === (deletionRef.current?.revision ?? 0);
    const fitIntent = pendingSelectionFitRef.current;
    const requestFocusId = selectionFocusRef.current;
    const fitWorkoutId = requestedIds.length === 1 ? requestedIds[0] : requestFocusId;
    const bootstrapAvailableWorkouts =
      selectedWorkoutIds !== undefined && !catalogLoadedRef.current;
    const removeSelection = (id: string) =>
      api<void>(
        `/api/map-selections/${encodeURIComponent(id.toUpperCase())}`,
        { method: "DELETE" },
        csrfToken,
      ).catch(() => undefined);
    const createSelection = async (workoutIds?: string[], focusId = requestFocusId) => {
      for (let attempt = 0; ; attempt++) {
        if (!current()) throw new DOMException("Map selection cancelled", "AbortError");
        try {
          return await api<MapSelection>(
            "/api/map-selections",
            {
              method: "POST",
              body: JSON.stringify(
                selectionRequest(dateRange, preferences.timezone, workoutIds, focusId),
              ),
              signal: controller.signal,
            },
            csrfToken,
          );
        } catch (error) {
          if (
            !(error instanceof ApiError) ||
            error.status !== 503 ||
            attempt >= MAP_SELECTION_RETRY_DELAYS_MS.length
          )
            throw error;
          if (!current()) throw new DOMException("Map selection cancelled", "AbortError");
          await waitForDiagnosticRetry(controller.signal, MAP_SELECTION_RETRY_DELAYS_MS[attempt]);
        }
      }
    };
    const loadSelection = async () => {
      let catalog: MapSelection | undefined;
      const recoveryKey = `${dateRange}:${preferences.timezone}:${deletionRevision}:${selectedWorkoutIds?.join(",") ?? "all"}`;
      try {
        if (bootstrapAvailableWorkouts) catalog = await createSelection(undefined, "");
        return { created: await createSelection(selectedWorkoutIds), catalog };
      } catch (error) {
        if (
          error instanceof ApiError &&
          error.status === 404 &&
          (selectedWorkoutIds !== undefined || requestFocusId !== undefined) &&
          recoveredSelectionRef.current !== recoveryKey &&
          current()
        ) {
          // Spend the recovery budget before requesting the catalog, even if it fails.
          recoveredSelectionRef.current = recoveryKey;
          catalog ??= await createSelection(undefined, "");
          if (!current()) {
            void removeSelection(catalog.id);
            throw error;
          }
          const fresh = catalog.workouts
            .map((workout) => ({ ...workout, id: workout.id.toUpperCase() }))
            .filter((workout) => !deletedWorkout(workout));
          const membership = new Set(fresh.map((workout) => workout.id));
          const retained = selectedWorkoutIds?.filter((id) => membership.has(id));
          recoveredSelectionRef.current = `${dateRange}:${preferences.timezone}:${deletionRevision}:${retained?.join(",") ?? "all"}`;
          for (const id of requestedIds)
            if (!membership.has(id)) unavailableRequestedIdsRef.current.add(id);
          updateAvailableWorkouts(fresh);
          if (requestFocusId && !membership.has(requestFocusId)) {
            clearFocusedWorkout();
            cancelRouteHover();
            rawRouteCacheRef.current.clear();
            temporaryCoverageSequenceRef.current++;
            diagnosticSequenceRef.current++;
            labelSequenceRef.current++;
            coverageFocusCacheRef.current.clear();
            setCoverageFocus(undefined);
            setCoverageHighlight(undefined);
            onRoadCoverageCacheChange?.(undefined);
            setDiagnostic(undefined);
            setDiagnosticPending(false);
            setSingleRawRoute(undefined);
            setFitRequest(undefined);
            setListScrollRequest(undefined);
          }
          // An all-range catalog is already the requested capability; do not POST it again.
          if (selectedWorkoutIds === undefined) return { created: catalog, catalog };
          if (retained && retained.join(",") !== selectedWorkoutIds.join(",")) {
            updateWorkoutSelection(retained);
            void removeSelection(catalog.id);
            throw new DOMException("Selection membership refreshed", "AbortError");
          }
          try {
            return {
              created: await createSelection(
                retained,
                requestFocusId && membership.has(requestFocusId) ? requestFocusId : "",
              ),
              catalog,
            };
          } catch (retryError) {
            void removeSelection(catalog.id);
            throw retryError;
          }
        }
        if (catalog) void removeSelection(catalog.id);
        throw error;
      }
    };
    setSelectionPending(true);
    setSelectionPendingDismissed(false);
    setSelectionError("");
    void loadSelection()
      .then(({ created, catalog }) => {
        if (!current()) {
          void removeSelection(created.id);
          if (catalog) void removeSelection(catalog.id);
          return;
        }
        const normalizedWorkouts = created.workouts
          .map((workout) => ({
            ...workout,
            id: workout.id.toUpperCase(),
          }))
          .filter((workout) => !deletedWorkout(workout));
        const normalizedAvailableWorkouts = (catalog?.workouts ?? created.workouts)
          .map((workout) => ({ ...workout, id: workout.id.toUpperCase() }))
          .filter((workout) => !deletedWorkout(workout));
        const normalized = {
          ...created,
          id: created.id.toUpperCase(),
          workouts: normalizedWorkouts,
        };
        const previousID = activeSelectionRef.current;
        activeSelectionRef.current = normalized.id;
        setSelection(normalized);
        updateAvailableWorkouts(
          catalog
            ? mergeWorkoutReadiness(normalizedAvailableWorkouts, normalizedWorkouts)
            : selectedWorkoutIds === undefined || availableWorkouts.length === 0
              ? normalizedAvailableWorkouts
              : mergeWorkoutReadiness(availableWorkoutsRef.current, normalizedWorkouts),
        );
        const requestedWorkout =
          fitIntent === "requested" && fitWorkoutId
            ? normalizedWorkouts.find((workout) => workout.id === fitWorkoutId)
            : undefined;
        let fitMode = mapModeRef.current;
        if (
          requestedIds.length === 1 &&
          requestedWorkout &&
          mapModeRef.current === "coverage" &&
          !preferences.coverageDiagnosticsEnabled &&
          !hasRenderableCoverage(requestedWorkout)
        ) {
          fitMode = "routes";
          setMapMode("routes");
        }
        fittedModeRef.current = fitMode;
        const requestedBounds = requestedWorkout && mapFitBounds(requestedWorkout, fitMode);
        const selectionBounds = mapFitBounds(normalized, fitMode);
        if (requestedWorkout && requestedBounds)
          setFitRequest((current) => ({
            key: (current?.key ?? 0) + 1,
            bounds: requestedBounds,
          }));
        else if (!requestedWorkout && fitIntent !== undefined && selectionBounds)
          setFitRequest((current) => ({
            key: (current?.key ?? 0) + 1,
            bounds: selectionBounds,
          }));
        if (pendingSelectionFitRef.current === fitIntent)
          pendingSelectionFitRef.current = undefined;
        setSelectionPending(false);
        if (catalog && catalog.id.toUpperCase() !== normalized.id) void removeSelection(catalog.id);
        if (previousID && previousID !== normalized.id)
          window.setTimeout(() => {
            void removeSelection(previousID);
          }, 5000);
      })
      .catch((error) => {
        if (current() && !(error instanceof DOMException && error.name === "AbortError")) {
          setSelectionError("Routes could not be prepared for this map.");
          setSelectionPending(false);
        }
      });
    // Focus only changes local overlays and the camera, not the tile capability's workout set.
    return () => {
      active = false;
      controller.abort();
    };
  }, [
    csrfToken,
    dateRange,
    preferences.timezone,
    selectedWorkoutIds?.join(",") ?? "all",
    selectionRefresh,
    deletionRevision,
  ]);

  useEffect(() => {
    if (!selection) return;
    const expiresAt = Date.parse(selection.expiresAt);
    if (!Number.isFinite(expiresAt)) return;
    const refreshIfExpired = () => {
      if (Date.now() >= expiresAt) refreshSelection();
    };
    const timer = window.setTimeout(
      refreshSelection,
      Math.min(2_147_483_647, Math.max(0, expiresAt - Date.now())),
    );
    const visibility = () => {
      if (document.visibilityState === "visible") refreshIfExpired();
    };
    window.addEventListener("focus", refreshIfExpired);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("focus", refreshIfExpired);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [selection?.id, selection?.expiresAt]);

  const coverageReadinessPollKey =
    selection?.workouts
      .map(
        (workout) =>
          `${workout.id}:${workout.coverageReadiness.mapDataStatus}:${workout.coverageReadiness.processingStatus}:${workout.coverageReadiness.resultStatus}`,
      )
      .join(",") ?? "";
  const coverageReadinessPolling =
    mapMode === "coverage" &&
    !preferences.coverageDiagnosticsEnabled &&
    Boolean(selection?.workouts.some(shouldPollCoverage));
  const coverageGenerationPolling =
    mapMode === "coverage" &&
    !preferences.coverageDiagnosticsEnabled &&
    Boolean(
      selection?.workouts.some((workout) => workout.coverageReadiness.resultStatus !== "none"),
    );
  const coveragePollInterval = coverageReadinessPolling
    ? COVERAGE_READINESS_POLL_MS
    : Math.min(60_000, Math.max(COVERAGE_READINESS_POLL_MS, config.pollingIntervalSeconds * 1000));
  useEffect(() => {
    if ((!coverageReadinessPolling && !coverageGenerationPolling) || !selection) return;
    const controller = new AbortController();
    let active = true;
    const revision = deletionRevision;
    let timer: number | undefined;
    const removeSelection = (id: string) =>
      api<void>(
        `/api/map-selections/${encodeURIComponent(id.toUpperCase())}`,
        { method: "DELETE" },
        csrfToken,
      ).catch(() => undefined);
    const schedule = () => {
      timer = window.setTimeout(poll, coveragePollInterval);
    };
    const poll = async () => {
      if (selectionPendingRef.current || document.visibilityState === "hidden") {
        schedule();
        return;
      }
      try {
        const created = await api<MapSelection>(
          "/api/map-selections",
          {
            method: "POST",
            body: JSON.stringify(
              selectionRequest(
                dateRange,
                preferences.timezone,
                selectedWorkoutIds,
                selectionFocusedWorkoutId,
              ),
            ),
            signal: controller.signal,
          },
          csrfToken,
        );
        const normalizedWorkouts = created.workouts.map((workout) => ({
          ...workout,
          id: workout.id.toUpperCase(),
        }));
        const prior = new Map(
          selection.workouts.map((workout) => [workout.id, workout.coverageReadiness]),
        );
        const changed =
          created.dataGeneration !== selection.dataGeneration ||
          created.coverageDisplayPolicy !== selection.coverageDisplayPolicy ||
          created.coverageDisplayRevision !== selection.coverageDisplayRevision ||
          normalizedWorkouts.length !== selection.workouts.length ||
          normalizedWorkouts.some(
            (workout) =>
              JSON.stringify(prior.get(workout.id)) !== JSON.stringify(workout.coverageReadiness),
          );
        const normalizedID = created.id.toUpperCase();
        if (!active || revision !== (deletionRef.current?.revision ?? 0)) {
          if (normalizedID !== activeSelectionRef.current) void removeSelection(normalizedID);
          return;
        }
        if (!changed) {
          if (normalizedID !== activeSelectionRef.current) void removeSelection(normalizedID);
          schedule();
          return;
        }
        const previousID = activeSelectionRef.current;
        activeSelectionRef.current = normalizedID;
        setSelection({ ...created, id: normalizedID, workouts: normalizedWorkouts });
        updateAvailableWorkouts(
          selectedWorkoutIds === undefined
            ? normalizedWorkouts.filter((workout) => !deletedWorkout(workout))
            : mergeWorkoutReadiness(availableWorkoutsRef.current, normalizedWorkouts).filter(
                (workout) => !deletedWorkout(workout),
              ),
        );
        if (previousID && previousID !== normalizedID)
          window.setTimeout(() => {
            void removeSelection(previousID);
          }, 5000);
      } catch (error) {
        if (active && revision === (deletionRef.current?.revision ?? 0)) {
          if (error instanceof ApiError && error.status === 404) refreshSelection();
          else schedule();
        }
      }
    };
    schedule();
    return () => {
      active = false;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [
    coverageReadinessPolling,
    coverageGenerationPolling,
    coveragePollInterval,
    coverageReadinessPollKey,
    selection?.id,
    selection?.dataGeneration,
    selection?.coverageDisplayPolicy,
    selection?.coverageDisplayRevision,
    csrfToken,
    dateRange,
    preferences.timezone,
    selectedWorkoutIds?.join(",") ?? "all",
    selectionFocusedWorkoutId,
    deletionRevision,
  ]);

  useEffect(
    () => () => {
      temporaryCoverageSequenceRef.current++;
      if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
      if (portionHoverTimerRef.current) clearTimeout(portionHoverTimerRef.current);
      coverageFocusCacheRef.current.clear();
      if (activeSelectionRef.current)
        void api<void>(
          `/api/map-selections/${encodeURIComponent(activeSelectionRef.current)}`,
          { method: "DELETE" },
          csrfToken,
        ).catch(() => undefined);
    },
    [csrfToken],
  );

  const coverageDetailSelectionKey = JSON.stringify([
    selection?.id,
    selection?.dataGeneration,
    selection?.coverageDisplayPolicy,
    selection?.coverageDisplayRevision,
    selection?.range.startDate,
    selection?.range.endDate,
  ]);
  const coverageDetailContext = JSON.stringify([
    coverageDetailSelectionKey,
    dateRange,
    preferences.timezone,
    selectedWorkoutIds?.join(",") ?? "all",
    deletionRevision,
    mapMode,
    preferences.coverageDiagnosticsEnabled,
    config.features.coverageMatcherDiagnostics,
    selectionRefresh,
    selectionPending,
  ]);
  const coverageDetailContextRef = useRef(coverageDetailContext);

  function cancelCoverageDetail() {
    coverageDetailSequenceRef.current++;
    const request = coverageDetailRequestRef.current;
    request?.controller.abort();
    if (request) window.clearTimeout(request.timer);
    coverageDetailRequestRef.current = undefined;
  }

  function changeRoadCoverageOpen(open: boolean) {
    if (open) {
      cancelCoverageDetail();
      setCoverageDetailStatus(undefined);
    }
    setRoadCoverageOpen(open);
    if (!open) setCanvasFocusRequest((value) => value + 1);
  }

  useEffect(() => {
    coverageDetailContextRef.current = coverageDetailContext;
    setCoverageDetailStatus(undefined);
    setCoverageHighlight(undefined);
    coverageDetailCacheRef.current.clear();
    return cancelCoverageDetail;
  }, [coverageDetailContext]);
  useEffect(() => {
    if (preferences.coverageDiagnosticsEnabled) setLocalMapMode("routes");
  }, [preferences.coverageDiagnosticsEnabled]);

  const focusedWorkout = availableWorkouts.find((workout) => workout.id === focusedWorkoutId);
  const focusedFamilyId = focusedWorkout
    ? resolveBaseFamily(config.baseMaps, [focusedWorkout])
    : undefined;
  const automaticFamilyId = focusedFamilyId ?? stickyAutomaticFamilyId;
  const familyId =
    overrideFamilyId && config.baseMaps.families.some((family) => family.id === overrideFamilyId)
      ? overrideFamilyId
      : automaticFamilyId;
  const family =
    config.baseMaps.families.find((candidate) => candidate.id === familyId) ??
    config.baseMaps.families[0];
  const preferredHighlightId = hoveredWorkoutId ?? focusedWorkoutId;
  const highlightedWorkoutId =
    preferredHighlightId && visibleWorkoutIds.has(preferredHighlightId)
      ? preferredHighlightId
      : undefined;
  const visibleFocusedWorkoutId =
    focusedWorkoutId && visibleWorkoutIds.has(focusedWorkoutId) ? focusedWorkoutId : undefined;
  const focusedCoverageBounds = visibleFocusedWorkoutId
    ? focusedWorkout?.coverageBounds
    : undefined;
  const selectedFitBounds = selection && mapFitBounds(selection, mapMode);
  useEffect(() => {
    if (fittedModeRef.current === mapMode) return;
    fittedModeRef.current = mapMode;
    const bounds =
      visibleFocusedWorkoutId && focusedWorkout && mapFitBounds(focusedWorkout, mapMode);
    if (
      bounds &&
      !selectionPending &&
      !preferences.coverageDiagnosticsEnabled &&
      (mapMode !== "coverage" || (focusedWorkout && hasRenderableCoverage(focusedWorkout)))
    )
      setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds }));
  }, [
    mapMode,
    visibleFocusedWorkoutId,
    focusedWorkout,
    preferences.coverageDiagnosticsEnabled,
    selectionPending,
  ]);
  const displayedFocusedWorkoutId =
    hoveredWorkoutId && hoveredWorkoutId !== visibleFocusedWorkoutId
      ? undefined
      : visibleFocusedWorkoutId;
  const coverageFocusContext = JSON.stringify([
    selection?.id,
    selection?.dataGeneration,
    selection?.coverageDisplayPolicy,
    selection?.coverageDisplayRevision,
    selection?.range.startDate,
    selection?.range.endDate,
  ]);
  const coverageFocusTarget = hoveredWorkoutId ?? visibleFocusedWorkoutId;
  const coverageFocusOwnerKey = JSON.stringify([
    coverageFocusContext,
    coverageFocusTarget,
    mapMode,
    preferences.coverageDiagnosticsEnabled,
    deletionRevision,
  ]);
  const coverageFocusOwnerRef = useRef(coverageFocusOwnerKey);
  coverageFocusOwnerRef.current = coverageFocusOwnerKey;
  const coverageFocus =
    coverageFocusResult?.key === coverageFocusOwnerKey
      ? coverageFocusResult.value
      : EMPTY_COVERAGE_FOCUS;

  useEffect(() => {
    const sequence = ++temporaryCoverageSequenceRef.current;
    const revision = deletionRevision;
    if (coverageFocusCacheContextRef.current !== coverageFocusContext) {
      coverageFocusCacheRef.current.clear();
      coverageFocusCacheContextRef.current = coverageFocusContext;
    }
    const targetWorkoutID = coverageFocusTarget;
    const targetWorkout = targetWorkoutID
      ? availableWorkouts.find((workout) => workout.id === targetWorkoutID)
      : undefined;
    if (
      mapMode !== "coverage" ||
      preferences.coverageDiagnosticsEnabled ||
      !selection ||
      !targetWorkout ||
      !hasRenderableCoverage(targetWorkout)
    ) {
      setCoverageFocus(undefined);
      return;
    }
    const targetID = targetWorkout.id;
    const key = `${coverageFocusContext}:${targetID}`;
    const cached = coverageFocusCacheRef.current.get(key);
    if (cached) {
      setCoverageFocus({ key: coverageFocusOwnerKey, value: cached });
      return;
    }
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      temporaryCoverageSequenceRef.current === sequence &&
      coverageFocusOwnerRef.current === coverageFocusOwnerKey &&
      revision === (deletionRef.current?.revision ?? 0) &&
      activeSelectionRef.current === selection.id;
    setCoverageFocus(undefined);
    void api<CoverageFocusFeatureCollection>(
      `/api/map-selections/${encodeURIComponent(selection.id)}/coverage-focus/${encodeURIComponent(targetID)}?generation=${selection.dataGeneration}`,
      { signal: controller.signal },
      csrfToken,
    )
      .then((value) => {
        if (!isCurrent()) return;
        coverageFocusCacheRef.current.set(key, value);
        setCoverageFocus({ key: coverageFocusOwnerKey, value });
      })
      .catch((error) => {
        if (!isCurrent()) return;
        if (error instanceof ApiError && error.status === 404) refreshSelection();
        setCoverageFocus(undefined);
      });
    return () => controller.abort();
  }, [coverageFocusOwnerKey, csrfToken]);

  const endpointRawWorkoutId =
    visibleFocusedWorkoutId ??
    (visibleWorkoutIds.size === 1 ? [...visibleWorkoutIds][0] : undefined);
  const rawRouteWorkoutId =
    mapMode === "routes"
      ? (highlightedWorkoutId ?? endpointRawWorkoutId)
      : preferences.coverageDiagnosticsEnabled
        ? visibleFocusedWorkoutId
        : undefined;
  const loadedRawRoute =
    rawRouteWorkoutId &&
    singleRawRoute?.workoutId === rawRouteWorkoutId &&
    singleRawRoute.generation === selection?.dataGeneration
      ? singleRawRoute
      : rawRouteWorkoutId
        ? rawRouteCacheRef.current.get(`${selection?.dataGeneration}:${rawRouteWorkoutId}`)
        : undefined;
  const displayedSingleRawRoute =
    endpointRawWorkoutId &&
    singleRawRoute?.workoutId === endpointRawWorkoutId &&
    singleRawRoute.generation === selection?.dataGeneration
      ? singleRawRoute
      : endpointRawWorkoutId
        ? rawRouteCacheRef.current.get(`${selection?.dataGeneration}:${endpointRawWorkoutId}`)
        : undefined;
  const directionRawRoute =
    mapMode === "routes" && highlightedWorkoutId
      ? loadedRawRoute?.route
      : mapMode === "coverage" && preferences.coverageDiagnosticsEnabled
        ? displayedSingleRawRoute?.route
        : undefined;
  const markerRawRoute =
    mapMode === "routes" && highlightedWorkoutId
      ? loadedRawRoute
      : preferences.coverageDiagnosticsEnabled
        ? displayedSingleRawRoute
        : undefined;
  const showRawRouteEndpoints =
    mapMode === "coverage"
      ? preferences.coverageDiagnosticsEnabled
      : Boolean(highlightedWorkoutId) || visibleWorkoutIds.size === 1;
  const diagnosticFeatureEnabled =
    config.features.coverageMatcherDiagnostics && preferences.coverageDiagnosticsEnabled;
  const diagnosticCompatible =
    diagnosticFeatureEnabled &&
    Boolean(
      visibleFocusedWorkoutId &&
      selection?.workouts.some((workout) => workout.id === visibleFocusedWorkoutId),
    ) &&
    !selectionPending &&
    !selectionError;
  const productionCoverageCompatible =
    !preferences.coverageDiagnosticsEnabled &&
    Boolean(selection && visibleWorkoutIds.size) &&
    !selectionPending &&
    !selectionError;
  const coverageCompatible = preferences.coverageDiagnosticsEnabled
    ? diagnosticCompatible
    : productionCoverageCompatible;
  const allRoutesSelected =
    availableWorkouts.length > 0 &&
    availableWorkouts.every((workout) => visibleWorkoutIds.has(workout.id));
  const statusMessage =
    rangeError ||
    baseMapError ||
    selectionError ||
    (selectionPending && !selectionPendingDismissed ? "Updating routes..." : "");
  const coverageDetailMessage = coverageDetailStatus
    ? coverageDetailStatus.loading
      ? `Loading exact road coverage for ${coverageEntityName(coverageDetailStatus.entity)}...`
      : coverageDetailStatus.unavailable
        ? "That road coverage changed or expired. Retry to refresh the map, then choose a road again."
        : "That road coverage could not be shown on the map."
    : "";
  const dismissStatusMessage = () => {
    if (rangeError) setRangeError("");
    else if (baseMapError) setBaseMapError("");
    else if (selectionError) setSelectionError("");
    else setSelectionPendingDismissed(true);
  };

  useEffect(() => {
    if (focusedFamilyId) {
      setStickyAutomaticFamilyId(focusedFamilyId);
    }
  }, [focusedFamilyId]);

  useEffect(() => {
    if (
      mapMode === "coverage" &&
      !preferences.coverageDiagnosticsEnabled &&
      !selectionPending &&
      !selectionError &&
      focusedWorkout &&
      !hasRenderableCoverage(focusedWorkout)
    )
      clearFocusedWorkout();
  }, [
    mapMode,
    preferences.coverageDiagnosticsEnabled,
    selectionPending,
    selectionError,
    focusedWorkout?.id,
    focusedWorkout?.coverageReadiness.resultStatus,
    focusedWorkout?.coverageBounds,
  ]);

  useEffect(() => {
    requestDiagnosticHover(undefined);
    setDiagnostic(undefined);
    setDiagnosticError("");
    setHoveredPortionOrdinal(undefined);
    setLockedPortionOrdinal(undefined);
    setLabelSaveError("");
    if (preferences.coverageDiagnosticsEnabled) setMapMode("routes");
  }, [
    focusedWorkoutId,
    dateRange,
    selectedWorkoutIds?.join(",") ?? "all",
    preferences.coverageDiagnosticsEnabled,
  ]);

  useEffect(() => {
    const diagnosticActive = mapMode === "coverage" && Boolean(diagnostic);
    const productionActive =
      mapMode === "coverage" &&
      !preferences.coverageDiagnosticsEnabled &&
      Boolean(visibleFocusedWorkoutId);
    const routesActive = mapMode === "routes" && Boolean(visibleFocusedWorkoutId);
    if (!diagnosticActive && !productionActive && !routesActive) {
      setRawRouteHidden(false);
      setNonFocusedRoutesHidden(false);
      setNonFocusedCoverageHidden(false);
      return;
    }
    const editableTarget = (target: EventTarget | null) =>
      target instanceof Element &&
      Boolean(target.closest("input, textarea, select, button, a, [contenteditable='true']"));
    const keyDown = (event: KeyboardEvent) => {
      if (event.code !== "Space" || event.repeat || editableTarget(event.target)) return;
      event.preventDefault();
      if (diagnosticActive) setRawRouteHidden(true);
      if (productionActive) setNonFocusedCoverageHidden(true);
      if (routesActive) setNonFocusedRoutesHidden(true);
    };
    const restore = (event?: KeyboardEvent) => {
      if (event && event.code !== "Space") return;
      if (event) event.preventDefault();
      setRawRouteHidden(false);
      setNonFocusedRoutesHidden(false);
      setNonFocusedCoverageHidden(false);
    };
    const restoreOnBlur = () => restore();
    window.addEventListener("keydown", keyDown);
    window.addEventListener("keyup", restore);
    window.addEventListener("blur", restoreOnBlur);
    return () => {
      window.removeEventListener("keydown", keyDown);
      window.removeEventListener("keyup", restore);
      window.removeEventListener("blur", restoreOnBlur);
      setRawRouteHidden(false);
      setNonFocusedRoutesHidden(false);
      setNonFocusedCoverageHidden(false);
    };
  }, [mapMode, diagnostic?.id, preferences.coverageDiagnosticsEnabled, visibleFocusedWorkoutId]);

  useEffect(() => {
    rawRouteCacheRef.current.clear();
    setSingleRawRoute(undefined);
  }, [selection?.dataGeneration]);

  useEffect(() => {
    if (!rawRouteWorkoutId || !selection) {
      setSingleRawRoute(undefined);
      return;
    }
    const generation = selection.dataGeneration;
    const key = `${generation}:${rawRouteWorkoutId}`;
    const cached = rawRouteCacheRef.current.get(key);
    if (cached) {
      setSingleRawRoute(cached);
      return;
    }
    const controller = new AbortController();
    const revision = deletionRevision;
    setSingleRawRoute(undefined);
    void api<RawRoutePoints>(
      `/api/workouts/${encodeURIComponent(rawRouteWorkoutId)}/route/points`,
      { signal: controller.signal },
      csrfToken,
    )
      .then(({ points }) => {
        if (
          !controller.signal.aborted &&
          rawGenerationRef.current === generation &&
          revision === (deletionRef.current?.revision ?? 0)
        ) {
          const loaded = {
            workoutId: rawRouteWorkoutId,
            generation,
            route: buildSegmentedRawRoute(points),
            endpoints: buildRawRouteEndpoints(points),
          };
          rawRouteCacheRef.current.set(key, loaded);
          setSingleRawRoute(loaded);
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setSingleRawRoute(undefined);
      });
    return () => controller.abort();
  }, [csrfToken, rawRouteWorkoutId, selection?.dataGeneration, deletionRevision]);

  useEffect(() => {
    if (
      mapMode !== "coverage" ||
      !diagnosticCompatible ||
      !visibleFocusedWorkoutId ||
      diagnostic?.workoutId === visibleFocusedWorkoutId
    )
      return;
    const controller = new AbortController();
    const sequence = ++diagnosticSequenceRef.current;
    const revision = deletionRevision;
    setDiagnosticPending(true);
    setDiagnosticError("");
    requestDiagnosticHover(undefined);
    setLockedPortionOrdinal(undefined);
    const requestDiagnostic = () =>
      api<CoverageDiagnosticRun>(
        `/api/workouts/${encodeURIComponent(visibleFocusedWorkoutId)}/coverage-diagnostic-runs`,
        { method: "POST", body: "{}", signal: controller.signal },
        csrfToken,
      );
    void retryInitialDiagnosticBusy(requestDiagnostic, controller.signal)
      .then((created) => {
        if (
          controller.signal.aborted ||
          sequence !== diagnosticSequenceRef.current ||
          revision !== (deletionRef.current?.revision ?? 0)
        )
          return;
        const normalized = {
          ...created,
          id: created.id.toUpperCase(),
          workoutId: created.workoutId.toUpperCase(),
        };
        setDiagnostic(normalized);
        setDiagnosticPending(false);
        const bounds = diagnosticBounds(normalized.overlay);
        if (bounds) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds }));
      })
      .catch((error) => {
        if (
          controller.signal.aborted ||
          sequence !== diagnosticSequenceRef.current ||
          (error instanceof DOMException && error.name === "AbortError")
        )
          return;
        setDiagnosticPending(false);
        setDiagnosticError(
          error instanceof ApiError && error.status === 429
            ? "The diagnostic overlay could not be prepared because all available workers are currently busy. Please try again later."
            : "The diagnostic overlay could not be prepared.",
        );
      });
    return () => {
      controller.abort();
      diagnosticSequenceRef.current++;
    };
  }, [
    csrfToken,
    dateRange,
    diagnosticCompatible,
    diagnosticRetry,
    focusedWorkoutId,
    mapMode,
    selection?.id,
    selectedWorkoutIds?.join(",") ?? "all",
    deletionRevision,
  ]);

  useEffect(
    () => () => {
      diagnosticSequenceRef.current++;
      labelSequenceRef.current++;
    },
    [],
  );

  async function saveDiagnosticLabels(next: CoverageDiagnosticRun["labels"]) {
    if (!diagnostic) return;
    const sequence = ++labelSequenceRef.current;
    setDiagnostic((current) => (current ? { ...current, labels: next } : current));
    setLabelSavePending(true);
    setLabelSaveError("");
    const body = { ...(next.overall ? { overall: next.overall } : {}), segments: next.segments };
    try {
      const labels = await api<CoverageDiagnosticRun["labels"]>(
        `/api/coverage-diagnostic-runs/${encodeURIComponent(diagnostic.id)}/labels`,
        { method: "PATCH", body: JSON.stringify(body) },
        csrfToken,
      );
      if (sequence !== labelSequenceRef.current) return;
      setDiagnostic((current) =>
        current?.id === diagnostic.id ? { ...current, labels } : current,
      );
      setLabelSavePending(false);
    } catch {
      if (sequence !== labelSequenceRef.current) return;
      setLabelSavePending(false);
      setLabelSaveError("Labels were not saved. Try again.");
    }
  }

  function toggleWorkout(workoutId: string) {
    const current = selectedWorkoutIds ?? availableWorkouts.map((workout) => workout.id);
    if (current.includes(workoutId)) {
      cancelRouteHover(workoutId);
      if (focusedWorkoutId === workoutId) clearFocusedWorkout();
      updateWorkoutSelection(current.filter((id) => id !== workoutId));
    } else {
      cancelRouteHover();
      updateWorkoutSelection([...current, workoutId].sort());
    }
  }

  function focusWorkout(workout: MapSelectionWorkout) {
    if (
      mapMode === "coverage" &&
      !preferences.coverageDiagnosticsEnabled &&
      !hasRenderableCoverage(workout)
    )
      return;
    cancelRouteHover();
    requestedFocusActiveRef.current = false;
    const location = new URL(window.location.href);
    location.pathname = "/map";
    location.searchParams.set("workoutId", workout.id);
    history.replaceState(
      history.state,
      "",
      `${location.pathname}${location.search}${location.hash}`,
    );
    updateFocusedWorkout(workout.id);
    const current = selectedWorkoutIds ?? availableWorkouts.map((item) => item.id);
    if (!current.includes(workout.id)) updateWorkoutSelection([...current, workout.id].sort());
    const bounds = mapFitBounds(workout, mapMode);
    if (bounds)
      setFitRequest((currentRequest) => ({
        key: (currentRequest?.key ?? 0) + 1,
        bounds,
      }));
    setCanvasFocusRequest((value) => value + 1);
  }

  function focusWorkoutFromMap(workoutId: string) {
    const workout = availableWorkouts.find((item) => item.id === workoutId);
    if (!workout) return;
    focusWorkout(workout);
    setListScrollRequest((current) => ({ key: (current?.key ?? 0) + 1, workoutId }));
  }

  function toggleAllWorkouts() {
    cancelRouteHover();
    if (allRoutesSelected) {
      clearFocusedWorkout();
      updateWorkoutSelection([]);
    } else {
      updateWorkoutSelection(undefined);
    }
  }

  function applyWorkoutTypes(ids: string[]) {
    const next = new Set(ids);
    if (next.size === visibleWorkoutIds.size && [...next].every((id) => visibleWorkoutIds.has(id)))
      return;
    cancelRouteHover();
    if (focusedWorkoutId && !next.has(focusedWorkoutId)) clearFocusedWorkout();
    updateWorkoutSelection(ids.length === availableWorkouts.length ? undefined : ids.sort());
  }

  async function selectRange(next: DateRangePreference) {
    pendingSelectionFitRef.current = "all";
    onDateRangeSelected(next);
    updateWorkoutSelection(undefined);
    updateAvailableWorkouts([]);
    clearFocusedWorkout();
    cancelRouteHover();
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
      recentRanges.saved(saved.recentCustomDateRanges);
      if (sequence === rangeSaveSequence.current) setRangeError("");
    } catch {
      if (sequence === rangeSaveSequence.current)
        setRangeError(
          "Your default date range was not saved. This map will continue using your selection.",
        );
    }
  }

  function showCoverageEntity(entity: RoadCoverageEntity) {
    if (
      !selection ||
      selectionPending ||
      mapMode !== "coverage" ||
      preferences.coverageDiagnosticsEnabled
    )
      return;
    cancelCoverageDetail();
    const sequence = coverageDetailSequenceRef.current;
    const context = coverageDetailContext;
    const revision = deletionRevision;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      sequence === coverageDetailSequenceRef.current &&
      context === coverageDetailContextRef.current &&
      revision === (deletionRef.current?.revision ?? 0) &&
      mapModeRef.current === "coverage";
    const key = `${coverageDetailSelectionKey}:${entity.entityKind}:${entity.entityId}`;
    const cached = coverageDetailCacheRef.current.get(key);
    setRoadCoverageOpen(false);
    setCanvasFocusRequest((value) => value + 1);
    const showDetail = (detail: RoadCoverageDetail) => {
      setCoverageDetailStatus(undefined);
      setCoverageHighlight({
        key: sequence,
        geometry: detail.geometry,
        fitBounds: detail.fitBounds,
      });
    };
    if (cached) {
      showDetail(cached);
      return;
    }
    setCoverageHighlight(undefined);
    setCoverageDetailStatus({ entity, loading: true });
    const timer = window.setTimeout(() => {
      if (!isCurrent()) return;
      controller.abort();
      setCoverageDetailStatus({ entity, loading: false });
    }, COVERAGE_DETAIL_TIMEOUT_MS);
    coverageDetailRequestRef.current = { controller, timer };
    void api<RoadCoverageDetail>(
      `/api/map-selections/${encodeURIComponent(selection.id)}/coverage/${entity.entityKind}/${encodeURIComponent(entity.entityId)}?generation=${selection.dataGeneration}`,
      { signal: controller.signal },
    )
      .then((detail) => {
        if (!isCurrent()) return;
        coverageDetailCacheRef.current.set(key, detail);
        showDetail(detail);
      })
      .catch((error) => {
        if (!isCurrent()) return;
        const unavailable = error instanceof ApiError && error.status === 404;
        if (unavailable) {
          coverageDetailCacheRef.current.clear();
          onRoadCoverageCacheChange?.(undefined);
        }
        setCoverageDetailStatus({ entity, loading: false, unavailable });
      })
      .finally(() => {
        window.clearTimeout(timer);
        if (coverageDetailRequestRef.current?.controller === controller)
          coverageDetailRequestRef.current = undefined;
      });
  }

  function showCoverageWorkout(workoutId: string) {
    setMapMode("coverage");
    focusWorkoutFromMap(workoutId.toUpperCase());
  }

  const controls = (suffix: string) => (
    <>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger className="range-trigger" aria-label="Select date range">
          <span>Date range</span>
          <strong>{rangeLabel(dateRange)}</strong>
          <span aria-hidden="true">v</span>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content
            className="menu-content range-menu"
            align="start"
            sideOffset={12}
            avoidCollisions={false}
            collisionPadding={12}
          >
            {QUICK_RANGES.map(([value, label]) => (
              <DropdownMenu.Item key={value} onSelect={() => void selectRange(value)}>
                {label}
                {dateRange === value && <span aria-label="selected">&#10003;</span>}
              </DropdownMenu.Item>
            ))}
            <DropdownMenu.Separator />
            <DropdownMenu.Item onSelect={() => setCustomOpen(true)}>
              Custom...
              {EXPLICIT_RANGE.test(dateRange) && <span aria-label="selected">&#10003;</span>}
            </DropdownMenu.Item>
            <RecentDateRangeItems
              ranges={recentRanges.history}
              current={dateRange}
              onSelect={(next) => void selectRange(next)}
            />
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger className="range-trigger" aria-label="Select base map">
          <span>Base map</span>
          <strong>
            {overrideFamilyId ? family?.label : `Automatic / ${family?.label ?? "Unavailable"}`}
          </strong>
          <span aria-hidden="true">v</span>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content
            className="menu-content range-menu"
            align="start"
            sideOffset={12}
            avoidCollisions={false}
            collisionPadding={12}
          >
            <DropdownMenu.Item onSelect={() => setOverrideFamilyId(undefined)}>
              Automatic{!overrideFamilyId && <span aria-label="selected">&#10003;</span>}
            </DropdownMenu.Item>
            <DropdownMenu.Separator />
            {config.baseMaps.families.map((candidate) => (
              <DropdownMenu.Item
                key={candidate.id}
                onSelect={() => setOverrideFamilyId(candidate.id)}
              >
                {candidate.label}
                {overrideFamilyId === candidate.id && <span aria-label="selected">&#10003;</span>}
              </DropdownMenu.Item>
            ))}
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <fieldset className="map-workout-filter">
        <legend>Workout routes</legend>
        <div className="map-route-toolbar">
          <WorkoutTypeSelection
            workouts={availableWorkouts}
            selectedIds={selectedWorkoutIds}
            allSelected={allRoutesSelected}
            onToggleAll={toggleAllWorkouts}
            onApply={applyWorkoutTypes}
          />
          <div className="map-mode-controls" role="group" aria-label="Map mode">
            <button
              type="button"
              aria-pressed={mapMode === "routes"}
              onClick={() => {
                setMapMode("routes");
                setCanvasFocusRequest((value) => value + 1);
              }}
            >
              Routes
            </button>
            <button
              type="button"
              aria-pressed={mapMode === "coverage"}
              disabled={!coverageCompatible}
              onClick={() => {
                setMapMode("coverage");
                setCanvasFocusRequest((value) => value + 1);
              }}
            >
              Coverage
            </button>
          </div>
        </div>
        <div className="map-filter-options">
          {availableWorkouts.length ? (
            <WorkoutRouteList
              workouts={availableWorkouts}
              preferences={preferences}
              sort={sort}
              mode={mapMode}
              diagnosticCoverage={preferences.coverageDiagnosticsEnabled}
              visibleIDs={selectedWorkoutIds}
              highlightedWorkoutId={highlightedWorkoutId}
              focusedWorkoutId={displayedFocusedWorkoutId}
              scrollRequest={listScrollRequest}
              onToggle={toggleWorkout}
              onFocus={focusWorkout}
              onHover={requestRouteHover}
            />
          ) : (
            <p className="map-routes-empty">No workout routes in this range.</p>
          )}
        </div>
      </fieldset>
      <button
        type="button"
        className="secondary map-fit-button"
        disabled={!selectedFitBounds}
        onClick={() =>
          selectedFitBounds &&
          setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: selectedFitBounds }))
        }
      >
        Fit routes
      </button>
    </>
  );

  return (
    <main className="map-page">
      <CustomDateRangeDialog
        open={customOpen}
        onOpenChange={setCustomOpen}
        range={dateRange}
        onApply={(next) => void selectRange(next)}
      />
      <aside className="map-sidebar" aria-label="Map controls">
        <div className="map-controls">{controls("desktop")}</div>
      </aside>
      <section className="map-stage" aria-live="polite">
        {(statusMessage || coverageDetailMessage) && (
          <div
            className={`map-banner${coverageDetailStatus ? " map-banner--coverage-detail" : ""}`}
            role="status"
          >
            <span>{[statusMessage, coverageDetailMessage].filter(Boolean).join(" ")}</span>
            {coverageDetailStatus && (
              <>
                {!coverageDetailStatus.loading && (
                  <button
                    type="button"
                    onClick={() => {
                      if (coverageDetailStatus.unavailable) {
                        cancelCoverageDetail();
                        setCoverageDetailStatus(undefined);
                        setSelection(undefined);
                        setSelectionRefresh((value) => value + 1);
                        setCanvasFocusRequest((value) => value + 1);
                      } else showCoverageEntity(coverageDetailStatus.entity);
                    }}
                  >
                    Retry
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => {
                    cancelCoverageDetail();
                    setCoverageDetailStatus(undefined);
                    setCanvasFocusRequest((value) => value + 1);
                  }}
                >
                  {coverageDetailStatus.loading ? "Cancel" : "Close"}
                </button>
              </>
            )}
            {statusMessage && (
              <button
                type="button"
                className="map-banner-dismiss"
                aria-label={
                  rangeError
                    ? "Dismiss date range warning"
                    : baseMapError
                      ? "Dismiss base map warning"
                      : selectionError
                        ? "Dismiss route preparation error"
                        : "Dismiss route update message"
                }
                onClick={dismissStatusMessage}
              >
                &times;
              </button>
            )}
          </div>
        )}
        {family && (
          <MapCanvas
            family={family}
            preferences={preferences}
            selection={selection}
            coverageFocus={coverageFocus}
            workouts={availableWorkouts}
            fitPadding={config.mapFitPaddingPixels}
            hoveredWorkoutId={highlightedWorkoutId}
            fitRequest={fitRequest}
            focusRequest={canvasFocusRequest}
            coverageHighlight={coverageHighlight}
            diagnosticEnabled={diagnosticFeatureEnabled}
            productionCoverageEnabled={!preferences.coverageDiagnosticsEnabled}
            diagnosticMode={mapMode === "coverage"}
            routeHidingEnabled={mapMode === "routes" && Boolean(visibleFocusedWorkoutId)}
            nonFocusedRoutesHidden={nonFocusedRoutesHidden}
            rawRouteHidden={rawRouteHidden}
            nonFocusedCoverageHidden={nonFocusedCoverageHidden}
            diagnosticRawRoute={
              preferences.coverageDiagnosticsEnabled ? displayedSingleRawRoute?.route : undefined
            }
            directionRoute={directionRawRoute}
            rawHighlightRoute={
              mapMode === "routes" && highlightedWorkoutId ? loadedRawRoute?.route : undefined
            }
            rawRouteEndpoints={markerRawRoute?.endpoints}
            rawRouteEndpointsVisible={showRawRouteEndpoints}
            diagnostic={diagnostic}
            highlightedPortionOrdinal={hoveredPortionOrdinal ?? lockedPortionOrdinal}
            onDiagnosticHover={requestDiagnosticHover}
            onDiagnosticLock={setLockedPortionOrdinal}
            onHover={requestRouteHover}
            onRouteClick={focusWorkoutFromMap}
            onBaseMapError={() =>
              setBaseMapError(
                "The public base map could not be loaded. Your private routes remain available. Retrying in 5 seconds...",
              )
            }
            onBaseMapReady={() => setBaseMapError("")}
            onRouteTilesUnavailable={refreshSelection}
          />
        )}
        {mapMode === "coverage" && preferences.coverageDiagnosticsEnabled && (
          <DiagnosticReviewCard
            run={diagnostic}
            pending={diagnosticPending}
            error={diagnosticError}
            selectedPortionOrdinal={lockedPortionOrdinal}
            savePending={labelSavePending}
            saveError={labelSaveError}
            onRetry={() => {
              setDiagnostic(undefined);
              setDiagnosticRetry((value) => value + 1);
            }}
            onFit={() => {
              const bounds = diagnostic && diagnosticBounds(diagnostic.overlay);
              if (bounds)
                setFitRequest((current) => ({
                  key: (current?.key ?? 0) + 1,
                  bounds,
                  focusCanvas: true,
                }));
            }}
            onCopyFocus={() => setCanvasFocusRequest((value) => value + 1)}
            onOverallLabel={(overall) =>
              diagnostic && void saveDiagnosticLabels({ ...diagnostic.labels, overall })
            }
            onSegmentLabel={(portionOrdinal, label) => {
              if (!diagnostic) return;
              const segments = [
                ...diagnostic.labels.segments.filter(
                  (item) => item.portionOrdinal !== portionOrdinal,
                ),
                { portionOrdinal, label },
              ].sort((left, right) => left.portionOrdinal - right.portionOrdinal);
              void saveDiagnosticLabels({ ...diagnostic.labels, segments });
            }}
          />
        )}
        {mapMode === "coverage" &&
          !preferences.coverageDiagnosticsEnabled &&
          selection &&
          !roadCoverageOpen && (
            <CoverageOverviewCard
              fitDisabled={!focusedCoverageBounds}
              onFit={() => {
                if (focusedCoverageBounds)
                  setFitRequest((current) => ({
                    key: (current?.key ?? 0) + 1,
                    bounds: focusedCoverageBounds,
                    focusCanvas: true,
                  }));
              }}
              onOpen={() => changeRoadCoverageOpen(true)}
            />
          )}
        <RoadCoverageDialog
          open={roadCoverageOpen}
          selection={selection}
          pageSize={preferences.pageSize}
          cache={roadCoverageCache}
          onCacheChange={onRoadCoverageCacheChange}
          onOpenChange={changeRoadCoverageOpen}
          onReturnFocus={() => setCanvasFocusRequest((value) => value + 1)}
          onSelectionUnavailable={refreshSelection}
          onShowEntity={showCoverageEntity}
          onShowWorkout={showCoverageWorkout}
        />
        {family && (
          <div
            className="map-attribution"
            aria-label={`Active map attribution for ${family.label}`}
          >
            <span>{family.attribution.text}</span>
            {family.attribution.links.map((link) => (
              <a key={`${link.label}-${link.url}`} href={link.url} target="_blank" rel="noreferrer">
                {link.label}
              </a>
            ))}
          </div>
        )}
        <Dialog.Root open={sheetOpen} onOpenChange={setSheetOpen}>
          <Dialog.Trigger asChild>
            <button type="button" className="mobile-map-sheet-trigger">
              Routes and controls
            </button>
          </Dialog.Trigger>
          <Dialog.Portal>
            <Dialog.Overlay className="map-sheet-overlay" />
            <Dialog.Content id="mobile-map-sheet" className="mobile-map-sheet">
              <div className="mobile-sheet-handle" aria-hidden="true" />
              <div className="mobile-sheet-heading">
                <div>
                  <Dialog.Title>Routes and controls</Dialog.Title>
                  <Dialog.Description>
                    Filter visible workouts and choose how the base map is presented.
                  </Dialog.Description>
                </div>
                <Dialog.Close className="icon-button" aria-label="Close Routes and controls">
                  &times;
                </Dialog.Close>
              </div>
              <div className="map-controls">{controls("mobile")}</div>
            </Dialog.Content>
          </Dialog.Portal>
        </Dialog.Root>
      </section>
    </main>
  );
}
