import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import maplibregl, { type GeoJSONSource, type Map as MapLibreMap, type MapGeoJSONFeature, type MapMouseEvent, type StyleSpecification, type VectorTileSource } from "maplibre-gl";
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
import { Tooltip } from "./Tooltip";

const ROUTES_LAYER = "private-workout-routes";
const HOVER_LAYER = "private-workout-route-hover";
const ROUTE_MARKERS_LAYER = "private-workout-route-markers";
const HOVER_MARKERS_LAYER = "private-workout-route-marker-hover";
const ROUTES_SOURCE = "private-workout-routes";
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
    if (signal.aborted) { reject(new DOMException("Aborted", "AbortError")); return; }
    const aborted = () => { window.clearTimeout(timer); reject(new DOMException("Aborted", "AbortError")); };
    const timer = window.setTimeout(() => { signal.removeEventListener("abort", aborted); resolve(); }, delayMilliseconds);
    signal.addEventListener("abort", aborted, { once: true });
  });
}

export async function retryInitialDiagnosticBusy<T>(request: () => Promise<T>, signal: AbortSignal, delayMilliseconds = DIAGNOSTIC_BUSY_RETRY_DELAY_MS) {
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
const DIAGNOSTIC_EVIDENCE_LAYERS = [DIAGNOSTIC_MATCHED_LAYER, DIAGNOSTIC_AMBIGUOUS_LAYER, DIAGNOSTIC_SELECTED_LAYER, DIAGNOSTIC_HIT_LAYER] as const;
const DIAGNOSTIC_LAYERS = [DIAGNOSTIC_RAW_ROUTE_LAYER, DIAGNOSTIC_DIRECTION_LAYER, ...DIAGNOSTIC_EVIDENCE_LAYERS] as const;
const ROUTE_LAYERS = [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER, ROUTE_START_LAYER, ROUTE_FINISH_LAYER] as const;
const ROUTE_HOVER_DELAY_MS = 250;
const COVERAGE_READINESS_POLL_MS = 10_000;
const COVERAGE_HOVER_DELAY_MS = 750;
export const COVERAGE_HIGHLIGHT_DURATION_MS = 3000;
const COVERAGE_HIGHLIGHT_IDLE_FALLBACK_MS = 30000;
const DIAGNOSTIC_HOVER_DELAY_MS = 250;
const ROUTE_FINISH_MARKER_SIZE = 22;
const ROUTE_START_MARKER_RADIUS = ROUTE_FINISH_MARKER_SIZE / Math.sqrt(Math.PI) * 0.9 * 0.97 * 0.95;
const RAW_ROUTE_FADE = { duration: 400, delay: 0 } as const;
const MAP_SELECTION_RETRY_DELAYS_MS = [500, 1000, 2000, 4000, 5000];
const EXPLICIT_RANGE = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/;
const COMPACT_UUID = /^[0-9A-F]{32}$/;
const QUICK_RANGES: ReadonlyArray<[DateRangeEnum, string]> = [
  ["thisWeek", "This week"], ["lastWeek", "Last week"], ["last7Days", "Last 7 days"], ["last30Days", "Last 30 days"],
  ["thisMonth", "This month"], ["lastMonth", "Last month"], ["thisYear", "This year"], ["lastYear", "Last year"],
];
const ROUTE_PALETTE = ["#ef9b61", "#75bda6", "#e2c86e", "#68a9df", "#e07a9a", "#9f91df", "#69c3c8", "#d7a65c"];
const SEMANTIC_ROUTE_COLORS = { walk: "#43d5e5", hiking: "#8ed081", cycling: "#69aef5" } as const;
const COVERAGE_RANGE_COLORS = ["#d95d0b", "#ed7d0c", "#f59e0b", "#f7b928", "#f9d64a", "#fff176"] as const;
const COVERAGE_FOCUS_COLORS = ["#ff008c", "#ff5fb4", "#ff8bc8", "#ffaad2", "#ffc9e1", "#ffd8f0"] as const;
const EMPTY_COVERAGE_FOCUS: CoverageFocusFeatureCollection = { type: "FeatureCollection", features: [] };
const NON_FOCUSED_COVERAGE_LAYERS = [COVERAGE_LAYER, COVERAGE_PARK_LAYER] as const;
const FOCUSED_COVERAGE_LAYERS = [COVERAGE_FOCUS_LAYER, COVERAGE_PARK_FOCUS_LAYER] as const;
const COVERAGE_LAYERS = [COVERAGE_LAYER, COVERAGE_PARK_LAYER, COVERAGE_FOCUS_LAYER, COVERAGE_PARK_FOCUS_LAYER] as const;

function coverageColorExpression(colors: readonly string[]) {
  return ["match", ["get", "countBucket"], 1, colors[0], 2, colors[1], 3, colors[2], 4, colors[3], 5, colors[4], 6, colors[5], colors[0]] as never;
}

export function startCoverageHighlightBlink(setOpacity: (opacity: number) => void, onReady: (ready: () => void) => () => void) {
	let countdownStarted = false;
	let stopped = false;
	let visible = true;
	let finishTimer: number | undefined;
	let fallbackTimer: number | undefined;
	let removeReadyListener: () => void = () => {};
	setOpacity(1);
	const blinkTimer = window.setInterval(() => { visible = !visible; setOpacity(visible ? 1 : 0); }, 250);
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

export function selectionRequest(range: DateRangePreference, timezone: string, workoutIds?: string[], focusedWorkoutId?: string) {
  const explicit = EXPLICIT_RANGE.exec(range);
  const selector = explicit ? { startDate: explicit[1], endDate: explicit[2] } : { dateRangeEnum: range, tz: timezone };
  const selected = workoutIds === undefined ? selector : { ...selector, workoutIds };
  return focusedWorkoutId ? { ...selected, focusedWorkoutId } : selected;
}

export function requestedWorkoutIds(search: string) {
  const params = new URLSearchParams(search);
  return [...params.getAll("workoutId"), ...params.getAll("workoutIds")]
    .map((id) => id.toUpperCase()).filter((id, index, ids) => COMPACT_UUID.test(id) && ids.indexOf(id) === index);
}

export function resolveBaseFamily(baseMaps: BaseMapsConfig, workouts: MapSelectionWorkout[]) {
  const available = new Set(baseMaps.families.map((family) => family.id));
  const routedTypes = new Map(workouts.map((workout) => [`${workout.type.name}\n${workout.type.key}`, workout.type]));
  const resolvedFamilies = [...routedTypes.values()].map((type) => baseMaps.workoutTypeMappings.find((mapping) =>
    mapping.normalizedTypeKey === type.key)?.familyId).filter((id): id is string => Boolean(id && available.has(id)));
  const mappedFamilies = new Set(resolvedFamilies);
  if (routedTypes.size > 0 && resolvedFamilies.length === routedTypes.size && mappedFamilies.size === 1) return resolvedFamilies[0];
  return available.has(baseMaps.fallbackFamilyId) ? baseMaps.fallbackFamilyId : baseMaps.families[0]?.id ?? "";
}

function rangeLabel(range: DateRangePreference) {
  const quick = QUICK_RANGES.find(([value]) => value === range);
  const explicit = EXPLICIT_RANGE.exec(range);
  return quick?.[1] ?? (explicit ? `${formatDateOnly(explicit[1])} to ${formatDateOnly(explicit[2])}` : "Last 30 days");
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
  return new Intl.DateTimeFormat("en-US", { year: "numeric", month: "numeric", day: "2-digit", timeZone: preferences.timezone }).format(instant);
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
  return [...new Map(workouts.map((workout) => [workout.type.key, routeColor(workout.type.key, workout.type.name)])).entries()];
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
		return /^\/api\/map-selections\/[^/]+\/(?:route|coverage)-tiles\/[^/]+\/[^/]+\/[^/]+\/[^/]+\.pbf$/.test(new URL(tileError.error.url ?? "", window.location.origin).pathname);
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
type RawRoutePoint = { recordedAt: string; longitude: number; latitude: number };
type RawRoutePoints = { points: RawRoutePoint[] };
type RawRouteFeature = { type: "Feature"; geometry: { type: "MultiLineString"; coordinates: number[][][] }; properties: Record<string, unknown> };
type RawRouteEndpoints = { type: "FeatureCollection"; features: Array<{ type: "Feature"; geometry: { type: "Point"; coordinates: number[] }; properties: { kind: "start" | "finish" } }> };
type RawRouteDirectionMarkers = { type: "FeatureCollection"; features: Array<{ type: "Feature"; geometry: { type: "Point"; coordinates: number[] }; properties: { bearing: number } }> };
type CoverageHighlight = { key: number; geometry: RoadCoverageDetail["geometry"]; fitBounds: RouteBounds };
export type MapMode = "routes" | "coverage";
export type RoadCoverageCache = { key: string; loadedAt: number; entities: RoadCoverageEntity[] };

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
    const startsLine = delta > 0 && positiveDeltaCount > 0 && delta >= 3 * positiveDeltaTotal / positiveDeltaCount;
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
  return lines.length === 0 ? undefined : { type: "Feature", geometry: { type: "MultiLineString", coordinates: lines }, properties: {} };
}

export function buildRawRouteEndpoints(points: RawRoutePoint[]): RawRouteEndpoints | undefined {
	if (points.length < 2) return undefined;
	const endpoint = (point: RawRoutePoint, kind: "start" | "finish") => ({ type: "Feature" as const, geometry: { type: "Point" as const, coordinates: [point.longitude, point.latitude] }, properties: { kind } });
	return { type: "FeatureCollection", features: [endpoint(points[0], "start"), endpoint(points[points.length-1], "finish")] };
}

export function buildRawRouteDirectionMarkers(route: RawRouteFeature, project: (coordinate: number[]) => { x: number; y: number }, spacingPixels = 96, smoothingPoints = 3): RawRouteDirectionMarkers {
	const features: RawRouteDirectionMarkers["features"] = [];
	for (const line of route.geometry.coordinates) {
		if (line.length < 2 * smoothingPoints + 1) continue;
		const projected = line.map(project);
		let distanceSinceMarker = spacingPixels / 2;
		for (let i = 1; i < line.length; i++) {
			distanceSinceMarker += Math.hypot(projected[i].x - projected[i-1].x, projected[i].y - projected[i-1].y);
			if (i < smoothingPoints || i+smoothingPoints >= line.length || distanceSinceMarker < spacingPixels) continue;
			const from = projected[i-smoothingPoints], to = projected[i+smoothingPoints];
			if (Math.hypot(to.x-from.x, to.y-from.y) < 4) continue;
			features.push({ type: "Feature", geometry: { type: "Point", coordinates: line[i] }, properties: { bearing: Math.atan2(to.x-from.x, from.y-to.y) * 180 / Math.PI } });
			distanceSinceMarker = 0;
		}
	}
	return { type: "FeatureCollection", features };
}

export function pointyDirectionMarkerImage() {
	const width = 11, height = 15, data = new Uint8Array(width * height * 4);
	const center = (width-1)/2;
	for (let y = 1; y < height-1; y++) {
		const halfWidth = 0.35 + (y-1) * 4.15 / (height-3);
		for (let x = 0; x < width; x++) {
			if (Math.abs(x-center) > halfWidth) continue;
			const offset = 4 * (y*width+x);
			data[offset] = 50; data[offset+1] = 213; data[offset+2] = 131; data[offset+3] = 255;
		}
	}
	return { width, height, data };
}

function diagnosticBounds(overlay: CoverageDiagnosticEvidenceCollection): RouteBounds | undefined {
  const coordinates = overlay.features.flatMap((feature) => feature.geometry.coordinates);
  const valid = coordinates.filter((coordinate) => coordinate.length >= 2 && Number.isFinite(coordinate[0]) && Number.isFinite(coordinate[1]));
  if (!valid.length) return undefined;
  return {
    minimumLongitude: Math.min(...valid.map((coordinate) => coordinate[0])), minimumLatitude: Math.min(...valid.map((coordinate) => coordinate[1])),
    maximumLongitude: Math.max(...valid.map((coordinate) => coordinate[0])), maximumLatitude: Math.max(...valid.map((coordinate) => coordinate[1])),
  };
}

function routePopupParts(value: string, preferences: Preferences) {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: preferences.timezone, year: "numeric", month: "numeric", day: "2-digit",
    hour: "numeric", minute: "2-digit", hour12: preferences.clockFormat === "12h",
  }).formatToParts(new Date(value));
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
  return { date: `${part("month")}/${part("day")}/${part("year")}`, time: `${part("hour").replace(/^0/, "")}:${part("minute")}${preferences.clockFormat === "12h" ? part("dayPeriod").slice(0, 1).toLowerCase() : ""}` };
}

export function formatRoutePopupDetails(startedAt: string, endedAt: string, preferences: Preferences) {
  const start = routePopupParts(startedAt, preferences);
  const end = routePopupParts(endedAt, preferences);
  return { date: start.date, timeRange: `${start.time} - ${end.time}` };
}

export function formatRoutePopupDistance(workout: MapSelectionWorkout, units: Preferences["units"]) {
  if (!workout.distance) return "Distance unavailable";
  let value = Number(workout.distance.value);
  let unit = workout.distance.unit;
  if (units === "imperial" && unit === "km") { value *= 0.621371192; unit = "mi"; }
  return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(value)} ${unit}`;
}

function MapCanvas({ family, preferences, selection, coverageFocus, workouts, fitPadding, hoveredWorkoutId, fitRequest, focusRequest, coverageHighlight, diagnosticEnabled, productionCoverageEnabled, diagnosticMode, routeHidingEnabled, nonFocusedRoutesHidden, rawRouteHidden, nonFocusedCoverageHidden, diagnosticRawRoute, directionRoute, rawRouteEndpoints, rawRouteEndpointsVisible, diagnostic, highlightedPortionOrdinal, onDiagnosticHover, onDiagnosticLock, onHover, onRouteClick, onBaseMapError, onBaseMapReady, onRouteTilesUnavailable }: {
  family: BaseMapFamily; preferences: Preferences; selection?: MapSelection; workouts: MapSelectionWorkout[];
  coverageFocus?: CoverageFocusFeatureCollection;
  fitPadding: number; hoveredWorkoutId?: string; fitRequest?: { key: number; bounds: RouteBounds; focusCanvas?: boolean };
  focusRequest: number;
  coverageHighlight?: CoverageHighlight;
  diagnosticEnabled: boolean; productionCoverageEnabled: boolean; diagnosticMode: boolean; routeHidingEnabled: boolean; nonFocusedRoutesHidden: boolean; rawRouteHidden: boolean; nonFocusedCoverageHidden: boolean; diagnosticRawRoute?: RawRouteFeature; directionRoute?: RawRouteFeature; rawRouteEndpoints?: RawRouteEndpoints; rawRouteEndpointsVisible: boolean; diagnostic?: CoverageDiagnosticRun; highlightedPortionOrdinal?: number;
  onDiagnosticHover: (ordinal?: number) => void; onDiagnosticLock: (ordinal: number) => void;
  onHover: (id?: string) => void; onRouteClick: (id: string) => void; onBaseMapError: () => void; onBaseMapReady: () => void; onRouteTilesUnavailable: () => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | undefined>(undefined);
  const selectionRef = useRef(selection);
  const routeUrlRef = useRef(selection?.routeTileUrl);
  const coverageUrlRef = useRef(selection?.coverageTileUrl);
  const coverageFocusRef = useRef<CoverageFocusFeatureCollection>(coverageFocus ?? { type: "FeatureCollection", features: [] });
  const installedRouteUrlRef = useRef<string | undefined>(undefined);
  const installedCoverageUrlRef = useRef<string | undefined>(undefined);
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
  const installedDiagnosticRawRouteRef = useRef<RawRouteFeature | undefined>(undefined);
  const coverageHandoffTimerRef = useRef<number | undefined>(undefined);
  const coverageHandoffCompleteRef = useRef(false);
  const rawRouteEndpointsRef = useRef(rawRouteEndpoints);
  const rawRouteEndpointsVisibleRef = useRef(rawRouteEndpointsVisible);
  const installedRawRouteEndpointsRef = useRef<RawRouteEndpoints | undefined>(undefined);
  const diagnosticRef = useRef(diagnostic);
  const highlightedPortionRef = useRef(highlightedPortionOrdinal);
  const coverageHighlightRef = useRef(coverageHighlight);
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
  const stopCoverageBlinkRef = useRef<() => void>(() => undefined);
  const fallbackStyleRef = useRef({ version: 8 as const, sources: {}, layers: [{ id: "fallback-background", type: "background" as const, paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" } }] });

  function syncPrivateLayers(map: MapLibreMap) {
    if (!routeUrlRef.current) return;
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
    if (!existingSource) map.addSource(ROUTES_SOURCE, { type: "vector", tiles: [absoluteURL] });
    installedRouteUrlRef.current = absoluteURL;
    const colorExpression: unknown[] = ["match", ["get", "workoutTypeKey"]];
    for (const [typeKey, color] of routeColorsRef.current) colorExpression.push(typeKey, color);
    colorExpression.push("#e9a852");
    if (!map.getLayer(ROUTES_LAYER)) map.addLayer({
        id: ROUTES_LAYER, type: "line", source: ROUTES_SOURCE, "source-layer": "routes",
        layout: { "line-cap": "round", "line-join": "round", "line-sort-key": ["get", "sortOrder"] },
        paint: { "line-color": colorExpression as never, "line-width": ["interpolate", ["linear"], ["zoom"], 5, 2, 14, 4], "line-opacity": nonFocusedRoutesHiddenRef.current ? 0 : 0.82, "line-opacity-transition": RAW_ROUTE_FADE },
      });
    else { map.setPaintProperty(ROUTES_LAYER, "line-color", colorExpression as never); map.setPaintProperty(ROUTES_LAYER, "line-opacity", nonFocusedRoutesHiddenRef.current ? 0 : 0.82); }
    if (!map.getLayer(ROUTE_MARKERS_LAYER)) map.addLayer({
        id: ROUTE_MARKERS_LAYER, type: "circle", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["==", ["geometry-type"], "Point"],
        layout: { "circle-sort-key": ["get", "sortOrder"] },
        paint: { "circle-color": colorExpression as never, "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 3, 14, 5], "circle-opacity": nonFocusedRoutesHiddenRef.current ? 0 : 0.9, "circle-opacity-transition": RAW_ROUTE_FADE },
      });
    else { map.setPaintProperty(ROUTE_MARKERS_LAYER, "circle-color", colorExpression as never); map.setPaintProperty(ROUTE_MARKERS_LAYER, "circle-opacity", nonFocusedRoutesHiddenRef.current ? 0 : 0.9); }
    if (coverageUrlRef.current && productionCoverageEnabledRef.current) {
      const coverageURL = absoluteRouteTileTemplate(coverageUrlRef.current, window.location.origin);
      let coverageSource = map.getSource(COVERAGE_SOURCE);
      if (coverageSource && installedCoverageUrlRef.current !== coverageURL) {
        for (const layer of [...NON_FOCUSED_COVERAGE_LAYERS].reverse()) if (map.getLayer(layer)) map.removeLayer(layer);
        map.removeSource(COVERAGE_SOURCE);
        coverageSource = undefined;
      }
      if (!coverageSource) map.addSource(COVERAGE_SOURCE, { type: "vector", tiles: [coverageURL], minzoom: 0, maxzoom: 22 });
      installedCoverageUrlRef.current = coverageURL;
      const visibility = diagnosticModeRef.current && productionCoverageEnabledRef.current ? "visible" : "none";
      const aggregateDefinitions = [
        { id: COVERAGE_LAYER, sourceLayer: "coverage", colors: COVERAGE_RANGE_COLORS, width: [2, 5] },
        { id: COVERAGE_PARK_LAYER, sourceLayer: "coverage_parks", colors: COVERAGE_RANGE_COLORS, width: [3, 6] },
      ] as const;
      for (const definition of aggregateDefinitions) {
        if (!map.getLayer(definition.id)) map.addLayer({
          id: definition.id, type: "line", source: COVERAGE_SOURCE, "source-layer": definition.sourceLayer,
          layout: { visibility, "line-cap": "round", "line-join": "round" },
          paint: { "line-color": coverageColorExpression(definition.colors), "line-width": ["interpolate", ["linear"], ["zoom"], 5, definition.width[0], 14, definition.width[1]], "line-opacity": nonFocusedCoverageHiddenRef.current ? 0 : 0.94, "line-opacity-transition": RAW_ROUTE_FADE },
        });
        else {
          map.setLayoutProperty(definition.id, "visibility", visibility);
          map.setPaintProperty(definition.id, "line-color", coverageColorExpression(definition.colors));
          map.setPaintProperty(definition.id, "line-opacity", nonFocusedCoverageHiddenRef.current ? 0 : 0.94);
        }
      }
      const focusSource = map.getSource(COVERAGE_FOCUS_SOURCE) as GeoJSONSource | undefined;
      if (!focusSource) map.addSource(COVERAGE_FOCUS_SOURCE, { type: "geojson", data: coverageFocusRef.current });
      else if (installedCoverageFocusRef.current !== coverageFocusRef.current) focusSource.setData(coverageFocusRef.current as never);
      installedCoverageFocusRef.current = coverageFocusRef.current;
      const focusDefinitions = [
        { id: COVERAGE_FOCUS_LAYER, entityKind: "path", colors: COVERAGE_FOCUS_COLORS, width: [4, 8] },
        { id: COVERAGE_PARK_FOCUS_LAYER, entityKind: "park", colors: COVERAGE_FOCUS_COLORS, width: [5, 9] },
      ] as const;
      for (const definition of focusDefinitions) {
        if (!map.getLayer(definition.id)) map.addLayer({
          id: definition.id, type: "line", source: COVERAGE_FOCUS_SOURCE, filter: ["==", ["get", "entityKind"], definition.entityKind],
          layout: { visibility, "line-cap": "round", "line-join": "round" },
          paint: { "line-color": coverageColorExpression(definition.colors), "line-width": ["interpolate", ["linear"], ["zoom"], 5, definition.width[0], 14, definition.width[1]], "line-opacity": 0.94, "line-opacity-transition": RAW_ROUTE_FADE },
        });
        else {
          map.setLayoutProperty(definition.id, "visibility", visibility);
          map.setPaintProperty(definition.id, "line-color", coverageColorExpression(definition.colors));
          map.setPaintProperty(definition.id, "line-opacity", 0.94);
        }
      }
      for (const definition of focusDefinitions) if (map.getLayer(definition.id)) map.moveLayer(definition.id);
    }
    if (!map.getLayer(HOVER_LAYER)) map.addLayer({
        id: HOVER_LAYER, type: "line", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["==", ["get", "workoutId"], hoverRef.current ?? ""],
        layout: { "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#c026ff", "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7], "line-opacity": 1 },
      });
    if (!map.getLayer(HOVER_MARKERS_LAYER)) map.addLayer({
        id: HOVER_MARKERS_LAYER, type: "circle", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["all", ["==", ["geometry-type"], "Point"], ["==", ["get", "workoutId"], hoverRef.current ?? ""]],
        layout: { "circle-sort-key": ["get", "sortOrder"] },
        paint: { "circle-color": "#c026ff", "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 9], "circle-opacity": 1 },
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
		if (!endpointSource) map.addSource(ROUTE_ENDPOINTS_SOURCE, { type: "geojson", data: rawRouteEndpointsRef.current });
		else if (installedRawRouteEndpointsRef.current !== rawRouteEndpointsRef.current) endpointSource.setData(rawRouteEndpointsRef.current);
		installedRawRouteEndpointsRef.current = rawRouteEndpointsRef.current;
		if (!map.getLayer(ROUTE_START_LAYER)) map.addLayer({
			id: ROUTE_START_LAYER, type: "circle", source: ROUTE_ENDPOINTS_SOURCE, filter: ["==", ["get", "kind"], "start"],
			layout: { visibility: "visible" },
			paint: { "circle-color": "#20a464", "circle-radius": ROUTE_START_MARKER_RADIUS, "circle-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0, "circle-opacity-transition": RAW_ROUTE_FADE, "circle-stroke-color": endpointOutline, "circle-stroke-width": 2, "circle-stroke-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0, "circle-stroke-opacity-transition": RAW_ROUTE_FADE },
		});
		else map.setPaintProperty(ROUTE_START_LAYER, "circle-stroke-color", endpointOutline);
		if (!map.getLayer(ROUTE_FINISH_LAYER)) map.addLayer({
			id: ROUTE_FINISH_LAYER, type: "symbol", source: ROUTE_ENDPOINTS_SOURCE, filter: ["==", ["get", "kind"], "finish"],
			layout: { visibility: "visible", "text-field": "■", "text-size": ROUTE_FINISH_MARKER_SIZE, "text-allow-overlap": true, "text-ignore-placement": true },
			paint: { "text-color": "#d3423f", "text-opacity": rawRouteEndpointsVisibleRef.current ? 1 : 0, "text-opacity-transition": RAW_ROUTE_FADE, "text-halo-color": endpointOutline, "text-halo-width": 1.5 },
		});
		else map.setPaintProperty(ROUTE_FINISH_LAYER, "text-halo-color", endpointOutline);
  }

  function syncDiagnosticLayers(map: MapLibreMap) {
    const hasMemoryRoute = Boolean(diagnosticRawRouteRef.current);
    const useMemoryRoute = diagnosticModeRef.current && hasMemoryRoute;
    if (hasMemoryRoute) {
      const rawSource = map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE) as GeoJSONSource | undefined;
			const routeChanged = installedDiagnosticRawRouteRef.current !== diagnosticRawRouteRef.current;
			if (routeChanged) {
				if (coverageHandoffTimerRef.current !== undefined) window.clearTimeout(coverageHandoffTimerRef.current);
				coverageHandoffTimerRef.current = undefined;
				coverageHandoffCompleteRef.current = false;
			}
      if (!rawSource) map.addSource(DIAGNOSTIC_RAW_ROUTE_SOURCE, { type: "geojson", data: diagnosticRawRouteRef.current });
			else if (routeChanged) rawSource.setData(diagnosticRawRouteRef.current);
			installedDiagnosticRawRouteRef.current = diagnosticRawRouteRef.current;
      if (!map.getLayer(DIAGNOSTIC_RAW_ROUTE_LAYER)) map.addLayer({
        id: DIAGNOSTIC_RAW_ROUTE_LAYER, type: "line", source: DIAGNOSTIC_RAW_ROUTE_SOURCE,
        layout: { visibility: "visible", "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#c026ff", "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7], "line-opacity": useMemoryRoute && !rawRouteHiddenRef.current ? 1 : 0, "line-opacity-transition": RAW_ROUTE_FADE },
      });
			map.setPaintProperty(DIAGNOSTIC_RAW_ROUTE_LAYER, "line-opacity", useMemoryRoute && !rawRouteHiddenRef.current ? 1 : 0);
    }
    else {
      if (map.getLayer(DIAGNOSTIC_RAW_ROUTE_LAYER)) map.removeLayer(DIAGNOSTIC_RAW_ROUTE_LAYER);
      if (map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE)) map.removeSource(DIAGNOSTIC_RAW_ROUTE_SOURCE);
			installedDiagnosticRawRouteRef.current = undefined;
    }
		const directionSource = map.getSource(DIAGNOSTIC_DIRECTION_SOURCE) as GeoJSONSource | undefined;
		if (directionRouteRef.current) {
			const directionData = buildRawRouteDirectionMarkers(directionRouteRef.current, (coordinate) => map.project(coordinate as [number, number]));
			const directionVisible = diagnosticModeRef.current ? useMemoryRoute && !rawRouteHiddenRef.current : true;
			const directionFade = diagnosticModeRef.current && diagnosticEnabledRef.current ? RAW_ROUTE_FADE : { duration: 0, delay: 0 };
			if (!map.hasImage(DIAGNOSTIC_DIRECTION_IMAGE)) map.addImage(DIAGNOSTIC_DIRECTION_IMAGE, pointyDirectionMarkerImage());
			if (!directionSource) map.addSource(DIAGNOSTIC_DIRECTION_SOURCE, { type: "geojson", data: directionData });
			else directionSource.setData(directionData);
			if (!map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.addLayer({
				id: DIAGNOSTIC_DIRECTION_LAYER, type: "symbol", source: DIAGNOSTIC_DIRECTION_SOURCE,
				layout: { visibility: "visible", "icon-image": DIAGNOSTIC_DIRECTION_IMAGE, "icon-rotate": ["get", "bearing"], "icon-rotation-alignment": "viewport", "icon-allow-overlap": true, "icon-ignore-placement": true },
				paint: { "icon-opacity": directionVisible ? 0.95 : 0, "icon-opacity-transition": directionFade },
			});
			else {
				map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity-transition", directionFade);
				map.setPaintProperty(DIAGNOSTIC_DIRECTION_LAYER, "icon-opacity", directionVisible ? 0.95 : 0);
			}
		} else {
			if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.removeLayer(DIAGNOSTIC_DIRECTION_LAYER);
			if (directionSource) map.removeSource(DIAGNOSTIC_DIRECTION_SOURCE);
		}
		const endpointOpacity = rawRouteEndpointsVisibleRef.current && (!diagnosticModeRef.current || !rawRouteHiddenRef.current) ? 1 : 0;
		if (map.getLayer(ROUTE_START_LAYER)) {
			map.setPaintProperty(ROUTE_START_LAYER, "circle-opacity", endpointOpacity);
			map.setPaintProperty(ROUTE_START_LAYER, "circle-stroke-opacity", endpointOpacity);
		}
		if (map.getLayer(ROUTE_FINISH_LAYER)) map.setPaintProperty(ROUTE_FINISH_LAYER, "text-opacity", endpointOpacity);
		const setVectorVisibility = (visibility: "visible" | "none") => {
			for (const layer of [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER]) if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", visibility);
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
		}
		else {
			if (coverageHandoffTimerRef.current !== undefined) window.clearTimeout(coverageHandoffTimerRef.current);
			coverageHandoffTimerRef.current = undefined;
			coverageHandoffCompleteRef.current = false;
			for (const layer of [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER]) if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", diagnosticModeRef.current ? "none" : "visible");
		}
		for (const layer of COVERAGE_LAYERS) if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", diagnosticModeRef.current && productionCoverageEnabledRef.current ? "visible" : "none");
    if (!diagnosticEnabledRef.current || !diagnosticRef.current) {
      for (const layer of [...DIAGNOSTIC_EVIDENCE_LAYERS].reverse()) if (map.getLayer(layer)) map.removeLayer(layer);
      if (map.getSource(DIAGNOSTIC_SOURCE)) map.removeSource(DIAGNOSTIC_SOURCE);
			if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.moveLayer(DIAGNOSTIC_DIRECTION_LAYER);
			for (const layer of [ROUTE_FINISH_LAYER, ROUTE_START_LAYER]) if (map.getLayer(layer)) map.moveLayer(layer);
      return;
    }
    const data = diagnosticRef.current.overlay;
    const source = map.getSource(DIAGNOSTIC_SOURCE) as GeoJSONSource | undefined;
    if (!source) map.addSource(DIAGNOSTIC_SOURCE, { type: "geojson", data });
    else source.setData(data);
    const visibility = diagnosticModeRef.current ? "visible" : "none";
    if (!map.getLayer(DIAGNOSTIC_MATCHED_LAYER)) map.addLayer({
      id: DIAGNOSTIC_MATCHED_LAYER, type: "line", source: DIAGNOSTIC_SOURCE,
      filter: ["==", ["get", "evidenceClass"], "matched"], layout: { visibility, "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#19c7c9", "line-width": 5, "line-opacity": 0.9 },
    });
    if (!map.getLayer(DIAGNOSTIC_AMBIGUOUS_LAYER)) map.addLayer({
      id: DIAGNOSTIC_AMBIGUOUS_LAYER, type: "line", source: DIAGNOSTIC_SOURCE,
      filter: ["==", ["get", "evidenceClass"], "ambiguous"], layout: { visibility, "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#e7a83d", "line-width": 5, "line-opacity": 0.94 },
    });
    if (!map.getLayer(DIAGNOSTIC_SELECTED_LAYER)) map.addLayer({
      id: DIAGNOSTIC_SELECTED_LAYER, type: "line", source: DIAGNOSTIC_SOURCE,
      filter: ["==", ["get", "portionOrdinal"], highlightedPortionRef.current ?? -1], layout: { visibility, "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#f7fbff", "line-width": 8, "line-opacity": 1 },
    });
    if (!map.getLayer(DIAGNOSTIC_HIT_LAYER)) map.addLayer({
      id: DIAGNOSTIC_HIT_LAYER, type: "line", source: DIAGNOSTIC_SOURCE, layout: { visibility, "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#000000", "line-width": 18, "line-opacity": 0 },
    });
		for (const layer of DIAGNOSTIC_EVIDENCE_LAYERS) if (map.getLayer(layer)) map.setLayoutProperty(layer, "visibility", visibility);
		if (map.getLayer(DIAGNOSTIC_DIRECTION_LAYER)) map.moveLayer(DIAGNOSTIC_DIRECTION_LAYER);
		for (const layer of [ROUTE_FINISH_LAYER, ROUTE_START_LAYER]) if (map.getLayer(layer)) map.moveLayer(layer);
  }

  function syncCoverageHighlight(map: MapLibreMap) {
    const highlight = coverageHighlightRef.current;
    const source = map.getSource(COVERAGE_HIGHLIGHT_SOURCE) as GeoJSONSource | undefined;
    if (!highlight) {
      if (map.getLayer(COVERAGE_HIGHLIGHT_LAYER)) map.removeLayer(COVERAGE_HIGHLIGHT_LAYER);
      if (source) map.removeSource(COVERAGE_HIGHLIGHT_SOURCE);
      return;
    }
    const feature = { type: "Feature" as const, geometry: highlight.geometry, properties: {} };
    if (!source) map.addSource(COVERAGE_HIGHLIGHT_SOURCE, { type: "geojson", data: feature });
    else source.setData(feature);
    if (!map.getLayer(COVERAGE_HIGHLIGHT_LAYER)) map.addLayer({
      id: COVERAGE_HIGHLIGHT_LAYER, type: "line", source: COVERAGE_HIGHLIGHT_SOURCE,
      layout: { "line-cap": "round", "line-join": "round" },
      paint: { "line-color": "#ffffff", "line-width": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 10], "line-opacity": 0 },
    });
    else map.moveLayer(COVERAGE_HIGHLIGHT_LAYER);
  }

  function syncMapLayers(map: MapLibreMap) {
    syncPrivateLayers(map);
    syncDiagnosticLayers(map);
    syncCoverageHighlight(map);
  }

  function preservePrivateLayers(previous: StyleSpecification | undefined, next: StyleSpecification) {
    if (!previous?.sources[ROUTES_SOURCE]) return next;
    const sources: StyleSpecification["sources"] = { ...next.sources, [ROUTES_SOURCE]: previous.sources[ROUTES_SOURCE] };
    if (previous.sources[COVERAGE_SOURCE]) sources[COVERAGE_SOURCE] = previous.sources[COVERAGE_SOURCE];
    if (previous.sources[COVERAGE_FOCUS_SOURCE]) sources[COVERAGE_FOCUS_SOURCE] = previous.sources[COVERAGE_FOCUS_SOURCE];
    if (previous.sources[COVERAGE_HIGHLIGHT_SOURCE]) sources[COVERAGE_HIGHLIGHT_SOURCE] = previous.sources[COVERAGE_HIGHLIGHT_SOURCE];
    if (previous.sources[DIAGNOSTIC_SOURCE]) sources[DIAGNOSTIC_SOURCE] = previous.sources[DIAGNOSTIC_SOURCE];
    if (previous.sources[DIAGNOSTIC_RAW_ROUTE_SOURCE]) sources[DIAGNOSTIC_RAW_ROUTE_SOURCE] = previous.sources[DIAGNOSTIC_RAW_ROUTE_SOURCE];
		if (previous.sources[DIAGNOSTIC_DIRECTION_SOURCE]) sources[DIAGNOSTIC_DIRECTION_SOURCE] = previous.sources[DIAGNOSTIC_DIRECTION_SOURCE];
		if (previous.sources[ROUTE_ENDPOINTS_SOURCE]) sources[ROUTE_ENDPOINTS_SOURCE] = previous.sources[ROUTE_ENDPOINTS_SOURCE];
    const privateLayers = previous.layers.filter((layer) => [...ROUTE_LAYERS, ...DIAGNOSTIC_LAYERS, ...COVERAGE_LAYERS, COVERAGE_HIGHLIGHT_LAYER].includes(layer.id as never));
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

  function popupContent(workout: MapSelectionWorkout) {
    const content = document.createElement("div");
    content.className = "map-route-tooltip";
    const type = document.createElement("strong"); type.className = "map-route-tooltip-type"; type.textContent = workout.type.name;
    const distance = document.createElement("strong"); distance.className = "map-route-tooltip-distance"; distance.textContent = formatRoutePopupDistance(workout, preferencesRef.current.units);
    const timing = formatRoutePopupDetails(workout.startedAt, workout.endedAt, preferencesRef.current);
    const date = document.createElement("span"); date.className = "map-route-tooltip-date"; date.textContent = timing.date;
    const times = document.createElement("span"); times.className = "map-route-tooltip-times"; times.textContent = timing.timeRange;
    content.append(type, distance, date, times);
    return content;
  }

  function coveragePopupContent(detail: RoadCoverageDetail) {
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
      const term = document.createElement("span"); term.textContent = label;
      const description = document.createElement("strong"); description.textContent = value;
      content.append(term, description);
    }
    return content;
  }

  useEffect(() => {
    if (!containerRef.current) return;
    const map = new maplibregl.Map({ container: containerRef.current, style: fallbackStyleRef.current, attributionControl: false });
    mapRef.current = map;
    const restore = () => {
      styleLoadedRef.current = true;
      syncMapLayers(map);
			const fallbackLoaded = map.getStyle().layers?.some((layer) => layer.id === "fallback-background") ?? false;
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
		if (privateRouteTileUnauthorized(event)) {
			window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
			return;
		}
		if (privateRouteTileUnavailable(event, routeUrlRef.current) || privateRouteTileUnavailable(event, coverageUrlRef.current)) {
			onRouteTilesUnavailableRef.current();
			return;
		}
      if (styleLoadedRef.current || fallbackInstalledRef.current) return;
      fallbackInstalledRef.current = true;
      onBaseMapError();
      map.setStyle(fallbackStyleRef.current);
      if (baseMapRetryTimerRef.current === undefined) {
        baseMapRetryTimerRef.current = window.setTimeout(() => {
          baseMapRetryTimerRef.current = undefined;
          styleLoadedRef.current = false;
          fallbackInstalledRef.current = false;
          map.setStyle(styleUrlRef.current, { transformStyle: preservePrivateLayers });
        }, 5000);
      }
    };
    const hover = (event: MapMouseEvent) => {
      const feature = topmostFeature(map.queryRenderedFeatures(event.point, { layers: [ROUTES_LAYER, ROUTE_MARKERS_LAYER] }));
      const workoutID = typeof feature?.properties?.workoutId === "string" ? feature.properties.workoutId.toUpperCase() : undefined;
      onHover(workoutID);
      map.getCanvas().style.cursor = feature ? "pointer" : "";
      if (!workoutID) { removePopup(); return; }
      popupLocationRef.current = event.lngLat;
      if (popupRef.current && popupWorkoutRef.current === workoutID) { popupRef.current.setLngLat(event.lngLat); return; }
      if (popupWorkoutRef.current === workoutID && popupTimerRef.current) return;
      removePopup();
      popupWorkoutRef.current = workoutID;
      popupTimerRef.current = setTimeout(() => {
        const workout = workoutsRef.current.find((item) => item.id === workoutID);
        if (!workout || !popupLocationRef.current) return;
        popupRef.current = new maplibregl.Popup({ closeButton: false, closeOnClick: false, offset: 12, className: "map-route-popup", maxWidth: "none" })
          .setLngLat(popupLocationRef.current).setDOMContent(popupContent(workout)).addTo(map);
        popupTimerRef.current = undefined;
      }, 750);
    };
    const leave = () => { onHover(undefined); map.getCanvas().style.cursor = ""; removePopup(); };
		const hoverCoverage = (event: MapMouseEvent) => {
			if (!diagnosticModeRef.current || !productionCoverageEnabledRef.current) return;
			const hitBox: [[number, number], [number, number]] = [
				[event.point.x - 3, event.point.y - 3],
				[event.point.x + 3, event.point.y + 3],
			];
			const features = map.queryRenderedFeatures(hitBox, { layers: [COVERAGE_PARK_LAYER, COVERAGE_LAYER] });
			const parkNames = new Set(features.flatMap((candidate) => {
				const name = candidate.properties?.entityKind === "park" ? candidate.properties?.name : undefined;
				return typeof name === "string" && name.trim() ? [name.trim()] : [];
			}));
			const regionalParkPath = features.find((candidate) => {
				const locality = candidate.properties?.entityKind === "path" ? candidate.properties?.localityName : undefined;
				return typeof locality === "string" && parkNames.has(locality.trim());
			});
			const feature = regionalParkPath
				?? features.find((candidate) => candidate.properties?.entityKind === "path" && typeof candidate.properties?.name === "string" && candidate.properties.name.trim())
				?? features.find((candidate) => candidate.properties?.entityKind === "park") ?? features[0];
			const entityID = typeof feature?.properties?.entityId === "string" ? feature.properties.entityId.toUpperCase() : undefined;
			const entityKind = feature?.properties?.entityKind === "park" ? "park" : feature?.properties?.entityKind === "path" ? "path" : undefined;
			map.getCanvas().style.cursor = feature ? "pointer" : "";
			if (!entityID || !entityKind || !selectionRef.current) { removePopup(); return; }
			const key = `${entityKind}:${entityID}`;
			popupLocationRef.current = event.lngLat;
			if (popupRef.current && coveragePopupEntityRef.current === key) { popupRef.current.setLngLat(event.lngLat); return; }
			if (coveragePopupEntityRef.current === key && (popupTimerRef.current || coveragePopupAbortRef.current)) return;
			removePopup();
			coveragePopupEntityRef.current = key;
			popupTimerRef.current = setTimeout(() => {
				popupTimerRef.current = undefined;
				const activeSelection = selectionRef.current;
				if (!activeSelection || coveragePopupEntityRef.current !== key) return;
				const controller = new AbortController();
				coveragePopupAbortRef.current = controller;
				const loadDetail = async () => {
					for (let attempt = 0; ; attempt++) {
						try {
							return await api<RoadCoverageDetail>(`/api/map-selections/${encodeURIComponent(activeSelection.id)}/coverage/${entityKind}/${encodeURIComponent(entityID)}?generation=${activeSelection.dataGeneration}`, { signal: controller.signal });
						} catch (error) {
							if (error instanceof ApiError && error.status === 404) onRouteTilesUnavailableRef.current();
							if (!(error instanceof ApiError) || error.status !== 503 || attempt > 0) throw error;
							await new Promise((resolve) => window.setTimeout(resolve, 250));
						}
					}
				};
				void loadDetail()
					.then((detail) => {
						if (controller.signal.aborted || coveragePopupEntityRef.current !== key || !popupLocationRef.current) return;
						popupRef.current = new maplibregl.Popup({ closeButton: false, closeOnClick: false, offset: 12, className: "map-route-popup map-coverage-popup", maxWidth: "none" })
							.setLngLat(popupLocationRef.current).setDOMContent(coveragePopupContent(detail)).addTo(map);
					})
					.catch(() => undefined)
					.finally(() => { if (coveragePopupAbortRef.current === controller) coveragePopupAbortRef.current = undefined; });
			}, COVERAGE_HOVER_DELAY_MS);
		};
		const clickRoute = (event: MapMouseEvent) => {
			if (diagnosticModeRef.current) return;
			const feature = topmostFeature(map.queryRenderedFeatures(event.point, { layers: [ROUTES_LAYER, ROUTE_MARKERS_LAYER] }));
			const workoutID = typeof feature?.properties?.workoutId === "string" ? feature.properties.workoutId.toUpperCase() : undefined;
			if (workoutID) onRouteClickRef.current(workoutID);
		};
    const diagnosticPortionAt = (event: MapMouseEvent) => {
      const visibleFeature = map.queryRenderedFeatures(event.point, { layers: [DIAGNOSTIC_AMBIGUOUS_LAYER, DIAGNOSTIC_MATCHED_LAYER] })[0];
      const feature = visibleFeature ?? map.queryRenderedFeatures(event.point, { layers: [DIAGNOSTIC_HIT_LAYER] })[0];
      const ordinal = Number(feature?.properties?.portionOrdinal);
      map.getCanvas().style.cursor = feature ? "pointer" : "";
      return Number.isInteger(ordinal) && ordinal >= 0 ? ordinal : undefined;
    };
    const hoverDiagnosticPortion = (event: MapMouseEvent) => { onDiagnosticHover(diagnosticPortionAt(event)); };
    const lockDiagnosticPortion = (event: MapMouseEvent) => { const ordinal = diagnosticPortionAt(event); if (ordinal !== undefined) onDiagnosticLock(ordinal); };
    const showDefaultViewport = () => {
      if (defaultViewportSetRef.current) return;
      defaultViewportSetRef.current = true;
      if (selectionRef.current?.bounds) return;
      const fullUS = () => { if (!selectionRef.current?.bounds) map.fitBounds([[-125, 24], [-66.5, 49.5]], { padding: fitPadding, duration: 0 }); };
      if (!navigator.geolocation) { fullUS(); return; }
      navigator.geolocation.getCurrentPosition(
        (position) => { if (!selectionRef.current?.bounds) map.jumpTo({ center: [position.coords.longitude, position.coords.latitude], zoom: 11 }); },
        fullUS, { enableHighAccuracy: false, timeout: 5000, maximumAge: 300000 },
      );
    };
    map.on("style.load", restore);
    map.on("load", restoreMissedInitialStyle);
    map.on("load", showDefaultViewport);
    map.on("error", styleError);
		const refreshDirectionMarkers = () => {
			const source = map.getSource(DIAGNOSTIC_DIRECTION_SOURCE) as GeoJSONSource | undefined;
			if (source && directionRouteRef.current) source.setData(buildRawRouteDirectionMarkers(directionRouteRef.current, (coordinate) => map.project(coordinate as [number, number])));
		};
		map.on("moveend", refreshDirectionMarkers);
    map.on("mousemove", ROUTES_LAYER, hover);
    map.on("mouseleave", ROUTES_LAYER, leave);
    map.on("mousemove", ROUTE_MARKERS_LAYER, hover);
    map.on("mouseleave", ROUTE_MARKERS_LAYER, leave);
		map.on("mousemove", hoverCoverage);
		map.on("click", clickRoute);
    map.on("mousemove", DIAGNOSTIC_HIT_LAYER, hoverDiagnosticPortion);
    map.on("click", DIAGNOSTIC_HIT_LAYER, lockDiagnosticPortion);
    map.on("mouseleave", DIAGNOSTIC_HIT_LAYER, () => { onDiagnosticHover(undefined); map.getCanvas().style.cursor = ""; });
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
    styleLoadedRef.current = false;
    fallbackInstalledRef.current = false;
    map.setStyle(family.styles[preferences.theme], { transformStyle: preservePrivateLayers });
    return () => { if (coverageHandoffTimerRef.current !== undefined) window.clearTimeout(coverageHandoffTimerRef.current); if (baseMapRetryTimerRef.current !== undefined) window.clearTimeout(baseMapRetryTimerRef.current); stopCoverageBlinkRef.current(); map.off("style.load", restore); map.off("load", restoreMissedInitialStyle); map.off("load", showDefaultViewport); map.off("error", styleError); map.off("moveend", refreshDirectionMarkers); map.off("mousemove", ROUTES_LAYER, hover); map.off("mouseleave", ROUTES_LAYER, leave); map.off("mousemove", ROUTE_MARKERS_LAYER, hover); map.off("mouseleave", ROUTE_MARKERS_LAYER, leave); map.off("mousemove", hoverCoverage); map.off("click", clickRoute); map.off("mousemove", DIAGNOSTIC_HIT_LAYER, hoverDiagnosticPortion); map.off("click", DIAGNOSTIC_HIT_LAYER, lockDiagnosticPortion); removePopup(); for (const layer of [...DIAGNOSTIC_LAYERS].reverse()) if (map.getLayer(layer)) map.removeLayer(layer); if (map.getSource(DIAGNOSTIC_SOURCE)) map.removeSource(DIAGNOSTIC_SOURCE); if (map.getSource(DIAGNOSTIC_DIRECTION_SOURCE)) map.removeSource(DIAGNOSTIC_DIRECTION_SOURCE); if (map.getSource(DIAGNOSTIC_RAW_ROUTE_SOURCE)) map.removeSource(DIAGNOSTIC_RAW_ROUTE_SOURCE); map.remove(); mapRef.current = undefined; };
  }, []);

  useEffect(() => {
    routeUrlRef.current = selection?.routeTileUrl;
    coverageUrlRef.current = selection?.coverageTileUrl;
    coverageFocusRef.current = coverageFocus ?? { type: "FeatureCollection", features: [] };
    selectionRef.current = selection;
    routeColorsRef.current = routeColors(selection?.workouts ?? []);
    workoutsRef.current = workouts;
    preferencesRef.current = preferences;
    const map = mapRef.current;
    if (map && styleLoadedRef.current) syncMapLayers(map);
  }, [preferences, selection?.id, selection?.routeTileUrl, selection?.coverageTileUrl, coverageFocus, workouts]);

	useEffect(() => { onRouteTilesUnavailableRef.current = onRouteTilesUnavailable; }, [onRouteTilesUnavailable]);
	useEffect(() => { onRouteClickRef.current = onRouteClick; }, [onRouteClick]);

  useEffect(() => {
    diagnosticEnabledRef.current = diagnosticEnabled;
    productionCoverageEnabledRef.current = productionCoverageEnabled;
    diagnosticModeRef.current = diagnosticMode;
    rawRouteHiddenRef.current = rawRouteHidden;
    nonFocusedRoutesHiddenRef.current = nonFocusedRoutesHidden;
    nonFocusedCoverageHiddenRef.current = nonFocusedCoverageHidden;
    diagnosticRawRouteRef.current = diagnosticRawRoute;
		directionRouteRef.current = directionRoute;
    rawRouteEndpointsRef.current = rawRouteEndpoints;
    rawRouteEndpointsVisibleRef.current = rawRouteEndpointsVisible;
    diagnosticRef.current = diagnostic;
    highlightedPortionRef.current = highlightedPortionOrdinal;
		if ((!diagnosticMode || !productionCoverageEnabled) && coveragePopupEntityRef.current) removePopup();
    const map = mapRef.current;
		if (map && styleLoadedRef.current) syncMapLayers(map);
    if (map?.getLayer(DIAGNOSTIC_SELECTED_LAYER)) map.setFilter(DIAGNOSTIC_SELECTED_LAYER, ["==", ["get", "portionOrdinal"], highlightedPortionOrdinal ?? -1]);
  }, [diagnosticEnabled, productionCoverageEnabled, diagnosticMode, routeHidingEnabled, nonFocusedRoutesHidden, rawRouteHidden, nonFocusedCoverageHidden, diagnosticRawRoute, directionRoute, rawRouteEndpoints, rawRouteEndpointsVisible, diagnostic, highlightedPortionOrdinal]);

  useEffect(() => {
    coverageHighlightRef.current = coverageHighlight;
    const map = mapRef.current;
    if (!map || !styleLoadedRef.current) return;
    syncCoverageHighlight(map);
    if (!coverageHighlight) return;
    map.fitBounds([[coverageHighlight.fitBounds.minimumLongitude, coverageHighlight.fitBounds.minimumLatitude], [coverageHighlight.fitBounds.maximumLongitude, coverageHighlight.fitBounds.maximumLatitude]], { padding: fitPadding, duration: 350 });
    const opacity = (value: number) => { if (map.getLayer(COVERAGE_HIGHLIGHT_LAYER)) map.setPaintProperty(COVERAGE_HIGHLIGHT_LAYER, "line-opacity", value); };
    stopCoverageBlinkRef.current();
    stopCoverageBlinkRef.current = startCoverageHighlightBlink(opacity, (ready) => onMapContextReady(map, ready));
    return () => stopCoverageBlinkRef.current();
  }, [coverageHighlight?.key]);

  useEffect(() => {
    if (!diagnosticMode || !diagnostic) return;
    mapRef.current?.getCanvas().focus({ preventScroll: true });
  }, [diagnosticMode, diagnostic?.id]);

  useEffect(() => {
    const map = mapRef.current;
    const nextStyle = family.styles[preferences.theme];
    fallbackStyleRef.current = { version: 8, sources: {}, layers: [{ id: "fallback-background", type: "background", paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" } }] };
    if (map && styleUrlRef.current !== nextStyle) {
      if (baseMapRetryTimerRef.current !== undefined) { window.clearTimeout(baseMapRetryTimerRef.current); baseMapRetryTimerRef.current = undefined; }
      styleUrlRef.current = nextStyle;
      styleLoadedRef.current = false;
      fallbackInstalledRef.current = false;
      map.setStyle(nextStyle, { transformStyle: preservePrivateLayers });
    }
  }, [family.id, family.styles, preferences.theme]);

  useEffect(() => {
    hoverRef.current = hoveredWorkoutId;
    const map = mapRef.current;
    if (map?.getLayer(HOVER_LAYER)) map.setFilter(HOVER_LAYER, ["==", ["get", "workoutId"], hoveredWorkoutId ?? ""]);
    if (map?.getLayer(HOVER_MARKERS_LAYER)) map.setFilter(HOVER_MARKERS_LAYER, ["all", ["==", ["geometry-type"], "Point"], ["==", ["get", "workoutId"], hoveredWorkoutId ?? ""]]);
  }, [hoveredWorkoutId]);

  useEffect(() => {
    const map = mapRef.current;
    const bounds = fitRequest?.bounds;
    if (map && fallbackInstalledRef.current) {
      if (baseMapRetryTimerRef.current !== undefined) { window.clearTimeout(baseMapRetryTimerRef.current); baseMapRetryTimerRef.current = undefined; }
      styleLoadedRef.current = false;
      fallbackInstalledRef.current = false;
      map.setStyle(styleUrlRef.current, { transformStyle: preservePrivateLayers });
    }
    if (map && bounds) {
      map.fitBounds([[bounds.minimumLongitude, bounds.minimumLatitude], [bounds.maximumLongitude, bounds.maximumLatitude]], { padding: fitPadding, duration: 350 });
      if (fitRequest.focusCanvas) map.getCanvas().focus({ preventScroll: true });
    }
  }, [fitRequest?.key]);

	useEffect(() => {
		if (focusRequest > 0) mapRef.current?.getCanvas().focus({ preventScroll: true });
	}, [focusRequest]);

  return <div ref={containerRef} className="map-canvas" role="application" aria-label="Workout route map" aria-keyshortcuts={(routeHidingEnabled || diagnosticMode && (diagnosticEnabled || productionCoverageEnabled)) ? "Space" : undefined} />;
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
    const leftValue = workoutSortValue(left, sort.field); const rightValue = workoutSortValue(right, sort.field);
    if (leftValue == null || Number.isNaN(leftValue)) return rightValue == null || Number.isNaN(rightValue) ? left.id.localeCompare(right.id) : 1;
    if (rightValue == null || Number.isNaN(rightValue)) return -1;
    const comparison = typeof leftValue === "string" && typeof rightValue === "string" ? (leftValue < rightValue ? -1 : leftValue > rightValue ? 1 : 0) : Number(leftValue) - Number(rightValue);
    if (comparison) return sort.direction === "asc" ? comparison : -comparison;
		if (sort.field !== "date" && left.startedAt !== right.startedAt) return left.startedAt > right.startedAt ? -1 : 1;
		return left.id.localeCompare(right.id);
  });
}

function mergeWorkoutReadiness(current: MapSelectionWorkout[], updates: MapSelectionWorkout[]) {
  const replacements = new Map(updates.map((workout) => [workout.id, workout]));
  return current.map((workout) => replacements.get(workout.id) ?? workout);
}

function coverageRouteStatus(workout: MapSelectionWorkout) {
  const readiness = workout.coverageReadiness;
  if (readiness.resultStatus === "current") return { kind: "current", label: "Coverage current", symbol: "✓" };
  if (readiness.resultStatus === "stale") return { kind: "stale", label: "Coverage stale", symbol: "" };
  if (readiness.mapDataStatus === "unavailable") return { kind: "unavailable", label: "Coverage unavailable", symbol: "×" };
  if (readiness.mapDataStatus === "pending" || readiness.processingStatus === "queued" || readiness.processingStatus === "running") return { kind: "pending", label: "Coverage pending", symbol: "" };
  if (readiness.processingStatus === "failed") return { kind: "failed", label: "Coverage failed", symbol: "✗" };
  return { kind: "pending", label: "Coverage pending", symbol: "" };
}

function shouldPollCoverage(workout: MapSelectionWorkout) {
  return workout.coverageReadiness.resultStatus === "none" && workout.coverageReadiness.mapDataStatus !== "unavailable" && workout.coverageReadiness.processingStatus !== "failed";
}

function unavailableCoverageReason(workout: MapSelectionWorkout) {
  const readiness = workout.coverageReadiness;
  if (readiness.mapDataStatus === "unavailable") return "Map data not available";
  if (readiness.mapDataStatus === "pending") return "Waiting for map data";
  if (readiness.processingStatus === "failed") return "Coverage update failed";
  return "Waiting for coverage update";
}

function WorkoutRouteList({ workouts, preferences, sort, mode, diagnosticCoverage, visibleIDs, highlightedWorkoutId, focusedWorkoutId, scrollRequest, onToggle, onFocus, onHover }: {
  workouts: MapSelectionWorkout[]; preferences: Preferences; sort: WorkoutSort; mode: MapMode; diagnosticCoverage: boolean; visibleIDs?: string[]; highlightedWorkoutId?: string; focusedWorkoutId?: string; scrollRequest?: { key: number; workoutId: string };
  onToggle: (id: string) => void; onFocus: (workout: MapSelectionWorkout) => void; onHover: (id?: string) => void;
}) {
  const listRef = useRef<HTMLOListElement>(null);
	const scrolledRequestRef = useRef<number | undefined>(undefined);
	useEffect(() => {
		if (!scrollRequest || focusedWorkoutId !== scrollRequest.workoutId || scrolledRequestRef.current === scrollRequest.key) return;
		const target = listRef.current?.querySelector<HTMLElement>(`[data-workout-id="${scrollRequest.workoutId}"]`);
		if (!target) return;
		target.scrollIntoView({ block: "center", inline: "nearest" });
		scrolledRequestRef.current = scrollRequest.key;
	}, [focusedWorkoutId, scrollRequest, workouts]);
  return <ol ref={listRef} className="map-route-list map-workout-list" onPointerLeave={() => onHover(undefined)}>{sortMapWorkouts(workouts, sort).map((workout) => {
    const coverageStatus = coverageRouteStatus(workout);
    const focusDisabled = mode === "coverage" && !diagnosticCoverage && workout.coverageReadiness.resultStatus === "none";
    const focusControl = <button type="button" className="map-route-focus" disabled={focusDisabled} onClick={() => onFocus(workout)} onPointerEnter={() => { if (focusDisabled) onHover(undefined); else onHover(workout.id); }} onFocus={() => { if (!focusDisabled) onHover(workout.id); }} onBlur={() => onHover(undefined)}>
      {mode === "routes"
        ? <span className={`route-swatch route-swatch--${semanticRouteKind(workout.type.name) ?? routeColorIndex(workout.type.key)}`} aria-hidden="true" />
        : <span className={`coverage-route-status coverage-route-status--${coverageStatus.kind}`}><span aria-hidden="true">{coverageStatus.symbol}</span><span className="visually-hidden">{coverageStatus.label}</span></span>}
      <span className="map-route-type">{workout.type.name}</span><small>{formatWorkoutDate(workout, preferences)}</small>
    </button>;
    return <li key={workout.id} data-workout-id={workout.id} className={`${highlightedWorkoutId === workout.id ? "is-hovered" : ""}${focusedWorkoutId === workout.id ? " is-focused" : ""}${focusDisabled ? " is-coverage-unavailable" : ""}`}>
      <label className="map-route-toggle" onPointerEnter={() => onHover(undefined)} onClick={(event) => event.stopPropagation()}><input type="checkbox" aria-label={`Show ${workout.type.name} from ${formatWorkoutDate(workout, preferences)}`} checked={visibleIDs === undefined || visibleIDs.includes(workout.id)} onChange={() => onToggle(workout.id)} /></label>
      {focusDisabled ? <Tooltip content={unavailableCoverageReason(workout)} className="map-route-readiness-tooltip" label={`${workout.type.name}: ${unavailableCoverageReason(workout)}`}>{focusControl}</Tooltip> : focusControl}
    </li>;
  })}</ol>;
}

function safeCount(value: number) {
  return Number.isSafeInteger(value) && value >= 0 ? new Intl.NumberFormat("en-US").format(value) : "Unavailable";
}

function safeDuration(milliseconds: number) {
  return Number.isFinite(milliseconds) && milliseconds >= 0 ? `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(milliseconds / 1000)} s` : "Unavailable";
}

function DiagnosticReviewCard({ run, pending, error, selectedPortionOrdinal, savePending, saveError, onRetry, onFit, onCopyFocus, onOverallLabel, onSegmentLabel }: {
  run?: CoverageDiagnosticRun; pending: boolean; error: string; selectedPortionOrdinal?: number; savePending: boolean; saveError: string;
  onRetry: () => void; onFit: () => void; onCopyFocus: () => void; onOverallLabel: (label: CoverageDiagnosticOverallLabel) => void;
  onSegmentLabel: (ordinal: number, label: CoverageDiagnosticSegmentLabelValue) => void;
}) {
	const [copiedSegmentId, setCopiedSegmentId] = useState("");
  const portion = run?.overlay.features.find((feature) => feature.properties.portionOrdinal === selectedPortionOrdinal);
  const segmentLabel = run?.labels.segments.find((label) => label.portionOrdinal === selectedPortionOrdinal)?.label;
  const policyLabel = run?.pathPolicyVersion.replace("coverage-path-policy-experimental-", "");
  const unavailableRegions = run?.outcome === "no_evidence" ? run.unavailableRegions : [];
  const unavailableMessage = unavailableRegions?.length
    ? `Coverage is unavailable because OSM data is not loaded for ${new Intl.ListFormat("en", { style: "long", type: "conjunction" }).format(unavailableRegions.map((region) => `${region.displayName} (${region.regionId})`))}.`
    : "";
  return <section className="coverage-diagnostic-card" aria-label="Coverage diagnostic review">
    <header><div><span className="coverage-diagnostic-eyebrow">Owner diagnostic{policyLabel ? ` / ${policyLabel}` : ""}</span><h2>Coverage review</h2></div>{run && !unavailableMessage && <div className="coverage-diagnostic-actions"><button type="button" className="coverage-diagnostic-fit" onClick={onFit}>Fit</button><button type="button" className="coverage-diagnostic-rerun" disabled={pending || savePending} onClick={onRetry}>Rerun</button></div>}</header>
    {pending && <div className="coverage-diagnostic-preparing" role="status" aria-live="polite" aria-busy="true"><p>Preparing diagnostic overlay...</p></div>}
    {error && <div className="coverage-diagnostic-error" role="alert"><span>{error}</span><button type="button" onClick={onRetry}>Retry</button></div>}
    {unavailableMessage && <div className="coverage-diagnostic-error" role="alert"><span>{unavailableMessage}</span><button type="button" onClick={onRetry}>Retry</button></div>}
    {run && !unavailableMessage && <>
      <dl className="coverage-diagnostic-counts"><div><dt>Matched</dt><dd>{safeCount(run.counts.matchedPoints)}</dd></div><div><dt>Ambiguous</dt><dd>{safeCount(run.counts.ambiguousPoints)}</dd></div><div><dt>Unmatched</dt><dd>{safeCount(run.counts.unmatchedPoints)}</dd></div><div><dt>Runtime</dt><dd>{safeDuration(run.counts.durationMilliseconds)}</dd></div></dl>
      <fieldset className="coverage-diagnostic-labels" disabled={savePending}><legend>Overall result</legend><div role="group">{(["correct", "incorrect", "uncertain"] as const).map((label) => <button key={label} type="button" aria-pressed={run.labels.overall === label} onClick={() => onOverallLabel(label)}>{label[0].toUpperCase() + label.slice(1)}</button>)}</div></fieldset>
      <div className="coverage-diagnostic-portion">
        {portion ? <><h3>Traversal {portion.properties.portionOrdinal + 1}</h3><p>{Number.isFinite(portion.properties.traversedMeters) ? `${portion.properties.traversedMeters.toFixed(1)} m traveled` : "Distance unavailable"} <span aria-hidden="true">/</span> {portion.properties.evidenceClass} <span aria-hidden="true">/</span> {portion.properties.direction}</p><p className="coverage-diagnostic-segment"><span>OSM segment {portion.properties.physicalSegmentId}</span><button type="button" className="coverage-diagnostic-copy" aria-label={copiedSegmentId === portion.properties.physicalSegmentId ? "OSM segment ID copied" : "Copy OSM segment ID"} title={copiedSegmentId === portion.properties.physicalSegmentId ? "Copied" : "Copy OSM segment ID"} onClick={() => { void navigator.clipboard.writeText(portion.properties.physicalSegmentId).then(() => { setCopiedSegmentId(portion.properties.physicalSegmentId); onCopyFocus(); }).catch(() => setCopiedSegmentId("")); }}><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><rect x="8" y="8" width="11" height="11" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" /></svg></button></p><fieldset className="coverage-diagnostic-labels" disabled={savePending}><legend>Expected result</legend><div role="group">{(["expected", "unexpected", "uncertain"] as const).map((label) => <button key={label} type="button" aria-pressed={segmentLabel === label} onClick={() => onSegmentLabel(portion.properties.portionOrdinal, label)}>{label[0].toUpperCase() + label.slice(1)}</button>)}</div></fieldset></> : <p>Select a traversal on the map to review it.</p>}
      </div>
      {(savePending || saveError) && <p className="coverage-diagnostic-save" role="status" aria-live="polite">{savePending ? "Saving labels..." : saveError}</p>}
    </>}
  </section>;
}

const COVERAGE_BUCKET_LABELS = ["1 workout", "2 workouts", "3-5 workouts", "6-10 workouts", "11-25 workouts", "26+ workouts"];

function CoverageOverviewCard({ fitDisabled, onFit, onOpen }: { fitDisabled: boolean; onFit: () => void; onOpen: () => void }) {
  return <section className="coverage-overview-card" aria-label="Coverage statistics">
    <header><div><span className="coverage-diagnostic-eyebrow">Checked routes</span><h2>Road coverage</h2></div><button type="button" className="coverage-diagnostic-fit" disabled={fitDisabled} onClick={onFit}>Fit</button></header>
    <div className="coverage-legend" aria-label="Coverage workout count legend">{COVERAGE_BUCKET_LABELS.map((label, index) => <div key={label}><span className="coverage-swatch-pair" aria-hidden="true"><span style={{ background: COVERAGE_RANGE_COLORS[index] }} /><span style={{ background: COVERAGE_FOCUS_COLORS[index] }} /></span><span>{label}</span></div>)}</div>
    <button type="button" className="secondary coverage-rankings-button" onClick={onOpen}>Coverage by road...</button>
  </section>;
}

const ROAD_COVERAGE_COLUMNS: ReadonlyArray<{ field: RoadCoverageSortField; label: string; initial: "asc" | "desc" }> = [
  { field: "rangeCount", label: "Workouts", initial: "desc" },
  { field: "name", label: "Road/Path/Park", initial: "asc" },
  { field: "cityOrRegion", label: "City/County/Region", initial: "asc" },
  { field: "rangeFirst", label: "Earliest visit", initial: "asc" },
  { field: "allTimeFirst", label: "First visit ever", initial: "asc" },
  { field: "rangeLatest", label: "Most recent visit", initial: "desc" },
  { field: "allTimeLatest", label: "Last visit ever", initial: "desc" },
];
const ROAD_COVERAGE_FETCH_PAGE_SIZE = 100;
const TABLE_LOADING_DELAY_MS = 500;
const ROAD_COVERAGE_CACHE_MAX_AGE_MS = 5 * 60 * 1000;

function coverageEntityName(entity: RoadCoverageEntity) {
  if (entity.name) return entity.name;
  if (entity.entityKind === "park") return "Unnamed park";
  switch (entity.broadClass) {
    case "road": return "Unnamed road";
    case "cycleway": return "Bike path";
    case "footway": return "Pedestrian path";
    case "trail": return "Trail";
    default: return "Unnamed path";
  }
}

export function coverageRegionLabel(regionId: string) {
  const slug = regionId.includes(":") ? regionId.slice(regionId.lastIndexOf(":") + 1) : regionId;
  return slug.split(/[-_]+/).filter(Boolean).map((word) => word.charAt(0).toLocaleUpperCase() + word.slice(1).toLocaleLowerCase()).join(" ") || regionId;
}

function coverageEntityPlace(entity: Pick<RoadCoverageEntity, "localityName" | "regionId" | "regionName">) {
  return entity.localityName ?? entity.regionName ?? coverageRegionLabel(entity.regionId);
}

function RoadCoverageDialog({ open, selection, pageSize, cache, onCacheChange, onOpenChange, onReturnFocus, onSelectionUnavailable, onShowEntity, onShowWorkout }: {
  open: boolean; selection?: MapSelection; pageSize: number; onOpenChange: (open: boolean) => void;
  cache?: RoadCoverageCache; onCacheChange?: (cache?: RoadCoverageCache) => void;
  onReturnFocus: () => void;
  onSelectionUnavailable: () => void;
  onShowEntity: (entity: RoadCoverageEntity) => Promise<void>; onShowWorkout: (workoutId: string) => void;
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
  const rangeKey = selection ? `${selection.range.startDate}/${selection.range.endDate}` : undefined;
  useEffect(() => {
    if (!open) {
      setError(undefined); setEntities(undefined);
    }
  }, [open]);
  useEffect(() => {
    if (!rangeKey) return;
    if (previousRangeKey.current && previousRangeKey.current !== rangeKey) {
      setInput(""); setSearch(""); setShowUnnamed(false); setPage(1);
    }
    previousRangeKey.current = rangeKey;
  }, [rangeKey]);
  useEffect(() => {
    const timer = window.setTimeout(() => { setSearch(input.trim()); setPage(1); }, 500);
    return () => window.clearTimeout(timer);
  }, [input]);
  useEffect(() => {
    if (!open || !selection) return;
    const cacheKey = `${selection.range.startDate}/${selection.range.endDate}@${selection.dataGeneration}`;
    if (cache?.key === cacheKey && Date.now() - cache.loadedAt < ROAD_COVERAGE_CACHE_MAX_AGE_MS) {
      const viewportRows = Math.max(5, Math.floor((window.innerHeight - 330) / 34));
      const initialVisibleCount = cache.entities.filter((entity) => entity.entityKind !== "path" || Boolean(entity.name)).length;
      setRowSlots(Math.max(1, Math.min(pageSize, viewportRows, initialVisibleCount || 1)));
      setEntities(cache.entities); setLoading(false); setShowLoading(false); setError(undefined);
      return;
    }
    const controller = new AbortController();
    const loadingTimer = window.setTimeout(() => setShowLoading(true), TABLE_LOADING_DELAY_MS);
    const loadPage = (requestedPage: number) => {
      const params = new URLSearchParams({ generation: String(selection.dataGeneration), page: String(requestedPage), pageSize: String(ROAD_COVERAGE_FETCH_PAGE_SIZE), sort: "rangeCount:desc" });
      return api<RoadCoverageList>(`/api/map-selections/${encodeURIComponent(selection.id)}/coverage/paths?${params}`, { signal: controller.signal });
    };
    setEntities(undefined); setLoading(true); setShowLoading(false); setError(undefined); setRowSlots(2);
    void (async () => {
      const first = await loadPage(1);
      const items = [...first.items];
      for (let nextPage = 2; nextPage <= first.pagination.totalPages; nextPage += 3) {
        const batch = await Promise.all(Array.from({ length: Math.min(3, first.pagination.totalPages - nextPage + 1) }, (_, offset) => loadPage(nextPage + offset)));
        for (const result of batch) items.push(...result.items);
      }
      if (controller.signal.aborted) return;
      window.clearTimeout(loadingTimer);
      const viewportRows = Math.max(5, Math.floor((window.innerHeight - 330) / 34));
      const initialVisibleCount = items.filter((entity) => entity.entityKind !== "path" || Boolean(entity.name)).length;
      setRowSlots(Math.max(1, Math.min(pageSize, viewportRows, initialVisibleCount || 1)));
      setEntities(items); setLoading(false); setShowLoading(false);
      onCacheChange?.({ key: cacheKey, loadedAt: Date.now(), entities: items });
    })().catch((loadError) => {
      if (controller.signal.aborted) return;
      window.clearTimeout(loadingTimer);
      if (loadError instanceof ApiError && loadError.status === 404) {
        setEntities(undefined); setLoading(true); setShowLoading(true); setError(undefined);
        onSelectionUnavailable();
        return;
      }
      setLoading(false); setShowLoading(false);
      setError({ message: "Road coverage could not be loaded.", retry: () => setLoadAttempt((value) => value + 1) });
    });
    return () => { controller.abort(); window.clearTimeout(loadingTimer); };
  }, [cache?.key, loadAttempt, open, pageSize, selection?.dataGeneration, selection?.id]);
  const visibleEntities = useMemo(() => {
    if (!entities) return [];
    const needle = search.toLocaleLowerCase();
    const eligible = showUnnamed ? entities : entities.filter((entity) => entity.entityKind !== "path" || Boolean(entity.name));
    const filtered = needle ? eligible.filter((entity) => [coverageEntityName(entity), coverageEntityPlace(entity)].join(" ").toLocaleLowerCase().includes(needle)) : eligible;
    const value = (entity: RoadCoverageEntity): string | number => {
      switch (sort.field) {
        case "rangeCount": return entity.rangeWorkoutCount;
        case "name": return coverageEntityName(entity);
        case "cityOrRegion": return coverageEntityPlace(entity);
        case "rangeFirst": return entity.rangeFirstDate;
        case "allTimeFirst": return entity.allTimeFirstDate;
        case "rangeLatest": return entity.rangeLatestDate;
        case "allTimeLatest": return entity.allTimeLatestDate;
      }
    };
    return [...filtered].sort((left, right) => {
      const leftValue = value(left), rightValue = value(right);
      const comparison = typeof leftValue === "number" && typeof rightValue === "number" ? leftValue - rightValue : String(leftValue).localeCompare(String(rightValue), undefined, { sensitivity: "base" });
      if (comparison) return sort.direction === "asc" ? comparison : -comparison;
      if (sort.field !== "name") {
        const nameComparison = coverageEntityName(left).localeCompare(coverageEntityName(right), undefined, { sensitivity: "base" });
        if (nameComparison) return nameComparison;
      }
      if (sort.field !== "cityOrRegion") {
        const placeComparison = coverageEntityPlace(left).localeCompare(coverageEntityPlace(right), undefined, { sensitivity: "base" });
        if (placeComparison) return placeComparison;
      }
      return left.entityKind.localeCompare(right.entityKind) || left.entityId.localeCompare(right.entityId);
    });
  }, [entities, search, showUnnamed, sort.direction, sort.field]);
  const totalPages = Math.ceil(visibleEntities.length / pageSize);
  const pageItems = visibleEntities.slice((page - 1) * pageSize, page * pageSize);
  useEffect(() => {
    if (!open || !entities) return;
    if (totalPages === 0 && page !== 1) setPage(1);
    else if (totalPages > 0 && page > totalPages) setPage(totalPages);
  }, [entities, open, page, totalPages]);
  const inRange = (date: string) => Boolean(selection && date >= selection.range.startDate && date <= selection.range.endDate);
  const updateSort = (field: RoadCoverageSortField) => {
    const column = ROAD_COVERAGE_COLUMNS.find((candidate) => candidate.field === field)!;
    setSort((current) => current.field === field ? { field, direction: current.direction === "asc" ? "desc" : "asc" } : { field, direction: column.initial });
    setPage(1);
  };
  const dateLink = (date: string, workoutId: string, enabled = true) => enabled
    ? <button type="button" className="coverage-table-link" onClick={() => { onOpenChange(false); onShowWorkout(workoutId); }}>{formatDateOnly(date)}</button>
    : <span>{formatDateOnly(date)}</span>;
  const showEntity = (entity: RoadCoverageEntity) => {
    setError(undefined);
    void onShowEntity(entity).catch((showError) => {
      if (showError instanceof ApiError && showError.status === 404) {
        onCacheChange?.(undefined);
        setEntities(undefined); setLoading(true); setShowLoading(false);
        setLoadAttempt((value) => value + 1);
        return;
      }
      setError({ message: "That road coverage could not be shown on the map.", retry: () => showEntity(entity) });
    });
  };
  if (error) return <Dialog.Root open={open} onOpenChange={onOpenChange}>
    <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content road-coverage-error-dialog" onCloseAutoFocus={(event) => { event.preventDefault(); onReturnFocus(); }}>
      <div className="dialog-heading"><div><Dialog.Title>Road Coverage</Dialog.Title><Dialog.Description>{error.message}</Dialog.Description></div></div>
      <div className="dialog-actions"><button type="button" onClick={error.retry}>Retry</button><Dialog.Close type="button" className="secondary">Close</Dialog.Close></div>
    </Dialog.Content></Dialog.Portal>
  </Dialog.Root>;
  return <Dialog.Root open={open} onOpenChange={onOpenChange}>
    <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content road-coverage-dialog" onCloseAutoFocus={(event) => { event.preventDefault(); onReturnFocus(); }}>
      <div className="dialog-heading"><div><Dialog.Title>Road Coverage</Dialog.Title><Dialog.Description className="visually-hidden">Coverage rankings for this date range.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Road Coverage">&times;</Dialog.Close></div>
      <div className="coverage-search"><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="11" cy="11" r="7" /><path d="m16 16 5 5" /></svg><label className="visually-hidden" htmlFor={searchID}>Search roads, paths, parks, cities, or regions</label><input id={searchID} type="search" value={input} onChange={(event) => setInput(event.target.value)} placeholder="Search roads, paths, parks, cities, or regions" /></div>
      <div className={`road-coverage-table-wrap${entities ? "" : " is-loading"}`} aria-busy={loading} style={{ "--road-coverage-row-slots": rowSlots } as CSSProperties}>
        <table className="workout-table road-coverage-table"><thead><tr>{ROAD_COVERAGE_COLUMNS.map((column) => {
          const selected = sort.field === column.field;
          return <th key={column.field} scope="col" aria-sort={selected ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}><button type="button" onClick={() => updateSort(column.field)}><span className="column-label">{column.label}</span><span className="sort-indicator" aria-hidden="true">{selected ? (sort.direction === "asc" ? <>&#9650;</> : <>&#9660;</>) : <>&#9650; &#9660;</>}</span></button></th>;
        })}</tr></thead><tbody>{showLoading && <tr className="road-coverage-state-row"><td colSpan={ROAD_COVERAGE_COLUMNS.length} role="status">Loading coverage...</td></tr>}{!loading && pageItems.map((entity) => <tr key={`${entity.entityKind}-${entity.entityId}`}>
          <td><span className="coverage-count-lane">{entity.rangeWorkoutCount.toLocaleString()}</span></td>
          <td><button type="button" className="coverage-table-link" onClick={() => showEntity(entity)}>{coverageEntityName(entity)}</button></td>
          <td>{coverageEntityPlace(entity)}</td>
          <td>{dateLink(entity.rangeFirstDate, entity.rangeFirstWorkoutId)}</td>
          <td>{dateLink(entity.allTimeFirstDate, entity.allTimeFirstWorkoutId, inRange(entity.allTimeFirstDate))}</td>
          <td>{dateLink(entity.rangeLatestDate, entity.rangeLatestWorkoutId)}</td>
          <td>{dateLink(entity.allTimeLatestDate, entity.allTimeLatestWorkoutId, inRange(entity.allTimeLatestDate))}</td>
        </tr>)}{!loading && pageItems.length === 0 && <tr className="road-coverage-state-row"><td colSpan={ROAD_COVERAGE_COLUMNS.length}>No roads, paths, or parks match this search.</td></tr>}</tbody></table>
      </div>
      {entities && <footer className="road-coverage-footer"><label className="road-coverage-unnamed"><input type="checkbox" checked={showUnnamed} onChange={(event) => { setShowUnnamed(event.target.checked); setPage(1); }} />Show unnamed roads and paths</label><nav className="pagination" aria-label="Road coverage pages"><button type="button" className="secondary" disabled={page <= 1 || totalPages === 0} onClick={() => setPage((value) => value - 1)}>Previous</button><span>Page {Math.max(1, page)} of {Math.max(1, totalPages)}</span><button type="button" className="secondary" disabled={totalPages === 0 || page >= totalPages} onClick={() => setPage((value) => value + 1)}>Next</button></nav></footer>}
    </Dialog.Content></Dialog.Portal>
  </Dialog.Root>;
}

export default function MapPage({ config, preferences, csrfToken, dateRange, onDateRangeSelected, sort = DEFAULT_WORKOUT_SORT, persistedWorkoutIds, onWorkoutSelectionChange, persistedAvailableWorkouts, onAvailableWorkoutsChange, persistedFocusedWorkoutId, onFocusedWorkoutChange, persistedMapMode, onMapModeChange, roadCoverageCache, onRoadCoverageCacheChange, explicitCanvasFocusRequest = 0 }: {
  config: PublicConfig; preferences: Preferences; csrfToken: string; dateRange: DateRangePreference;
  onDateRangeSelected: (range: DateRangePreference) => void; sort?: WorkoutSort;
  persistedWorkoutIds?: string[]; onWorkoutSelectionChange?: (workoutIds?: string[]) => void;
  persistedAvailableWorkouts?: MapSelectionWorkout[]; onAvailableWorkoutsChange?: (workouts: MapSelectionWorkout[]) => void;
  persistedFocusedWorkoutId?: string; onFocusedWorkoutChange?: (workoutId?: string) => void;
  persistedMapMode?: MapMode; onMapModeChange?: (mode: MapMode) => void;
  roadCoverageCache?: RoadCoverageCache; onRoadCoverageCacheChange?: (cache?: RoadCoverageCache) => void;
  explicitCanvasFocusRequest?: number;
}) {
  const [selection, setSelection] = useState<MapSelection>();
  const [selectionPending, setSelectionPending] = useState(true);
  const [selectionError, setSelectionError] = useState("");
  const [baseMapError, setBaseMapError] = useState("");
  const [rangeError, setRangeError] = useState("");
  const [overrideFamilyId, setOverrideFamilyId] = useState<string>();
  const [stickyAutomaticFamilyId, setStickyAutomaticFamilyId] = useState(config.baseMaps.fallbackFamilyId);
  const [hoveredWorkoutId, setHoveredWorkoutId] = useState<string>();
  const [coverageFocus, setCoverageFocus] = useState<CoverageFocusFeatureCollection>(EMPTY_COVERAGE_FOCUS);
  const [fitRequest, setFitRequest] = useState<{ key: number; bounds: RouteBounds; focusCanvas?: boolean }>();
	const [canvasFocusRequest, setCanvasFocusRequest] = useState(0);
	useEffect(() => {
		if (explicitCanvasFocusRequest > 0) setCanvasFocusRequest((value) => value + 1);
	}, [explicitCanvasFocusRequest]);
  const [localFocusedWorkoutId, setLocalFocusedWorkoutId] = useState<string>();
  const [sheetOpen, setSheetOpen] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [localMapMode, setLocalMapMode] = useState<MapMode>("routes");
  const mapMode = preferences.coverageDiagnosticsEnabled ? localMapMode : onMapModeChange ? persistedMapMode ?? "routes" : localMapMode;
  const setMapMode = (mode: MapMode) => preferences.coverageDiagnosticsEnabled ? setLocalMapMode(mode) : onMapModeChange ? onMapModeChange(mode) : setLocalMapMode(mode);
  const [roadCoverageOpen, setRoadCoverageOpen] = useState(false);
  const [coverageHighlight, setCoverageHighlight] = useState<CoverageHighlight>();
  const [diagnostic, setDiagnostic] = useState<CoverageDiagnosticRun>();
	const [singleRawRoute, setSingleRawRoute] = useState<{ workoutId: string; route?: RawRouteFeature; endpoints?: RawRouteEndpoints }>();
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
	const requestedIds = useRef(requestedWorkoutIds(window.location.search)).current;
	const requestedFocusActiveRef = useRef(requestedIds.length === 1);
  const initialListFocusId = useRef(requestedIds.length === 1 ? requestedIds[0] : persistedFocusedWorkoutId).current;
	const [listScrollRequest, setListScrollRequest] = useState<{ key: number; workoutId: string } | undefined>(() => initialListFocusId ? { key: 1, workoutId: initialListFocusId } : undefined);
  const [localWorkoutIds, setLocalWorkoutIds] = useState<string[] | undefined>(() => requestedIds.length ? requestedIds : undefined);
  const selectedWorkoutIds = onWorkoutSelectionChange ? persistedWorkoutIds : localWorkoutIds;
  const focusedWorkoutId = onFocusedWorkoutChange ? persistedFocusedWorkoutId : localFocusedWorkoutId;
  const selectionFocusedWorkoutId = focusedWorkoutId ?? (requestedFocusActiveRef.current ? requestedIds[0] : undefined);
	const selectionFocusRef = useRef(selectionFocusedWorkoutId);
	selectionFocusRef.current = selectionFocusedWorkoutId;
  const [localAvailableWorkouts, setLocalAvailableWorkouts] = useState<MapSelectionWorkout[]>([]);
  const availableWorkouts = onAvailableWorkoutsChange ? persistedAvailableWorkouts ?? [] : localAvailableWorkouts;
  const rangeSaveSequence = useRef(0);
  const rangeSaveChain = useRef<Promise<void>>(Promise.resolve());
  const activeSelectionRef = useRef<string | undefined>(undefined);
	const selectionPendingRef = useRef(false);
	const selectionRefreshAtRef = useRef(0);
  const pendingSelectionFitRef = useRef<"all" | "requested" | undefined>(requestedIds.length === 1 || persistedFocusedWorkoutId ? "requested" : "all");
  const hoveredWorkoutRef = useRef<string | undefined>(undefined);
  const hoverCandidateRef = useRef<string | undefined>(undefined);
  const hoverTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const temporaryCoverageSequenceRef = useRef(0);
  const coverageFocusCacheRef = useRef(new Map<string, CoverageFocusFeatureCollection>());
  const hoveredPortionRef = useRef<number | undefined>(undefined);
  const portionHoverCandidateRef = useRef<number | undefined>(undefined);
  const portionHoverTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const diagnosticSequenceRef = useRef(0);
  const labelSequenceRef = useRef(0);
	const rawRouteCacheRef = useRef(new Map<string, { workoutId: string; route?: RawRouteFeature; endpoints?: RawRouteEndpoints }>());
  const visibleWorkoutIds = new Set(selectedWorkoutIds ?? availableWorkouts.map((workout) => workout.id));
  const visibleWorkoutIdsRef = useRef(visibleWorkoutIds);
  visibleWorkoutIdsRef.current = visibleWorkoutIds;
	selectionPendingRef.current = selectionPending;

	function refreshSelection() {
		const now = Date.now();
		if (selectionPendingRef.current || now-selectionRefreshAtRef.current < 1000) return;
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
    history.replaceState(history.state, "", `${location.pathname}${location.search}${location.hash}`);
    updateFocusedWorkout(undefined);
  }

  function updateAvailableWorkouts(workouts: MapSelectionWorkout[]) {
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
    if (!workoutId || !visibleWorkoutIdsRef.current.has(workoutId)) { cancelRouteHover(); return; }
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
    if (portionOrdinal === undefined) { setDiagnosticHover(undefined); return; }
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
    else if (!onWorkoutSelectionChange) setLocalWorkoutIds(undefined);
    if (!onAvailableWorkoutsChange) setLocalAvailableWorkouts([]);
  }, [requestedIds.join(",")]);

  useEffect(() => {
    if (requestedIds.length !== 1) return;
    updateFocusedWorkout(requestedIds[0]);
  }, [requestedIds.join(",")]);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    const fitIntent = pendingSelectionFitRef.current;
		const requestFocusId = selectionFocusRef.current;
    const fitWorkoutId = requestedIds.length === 1 ? requestedIds[0] : requestFocusId;
    const bootstrapAvailableWorkouts = selectedWorkoutIds !== undefined && availableWorkouts.length === 0;
    const removeSelection = (id: string) => api<void>(`/api/map-selections/${encodeURIComponent(id.toUpperCase())}`, { method: "DELETE" }, csrfToken).catch(() => undefined);
    const createSelection = async (workoutIds?: string[]) => {
      for (let attempt = 0; ; attempt++) {
        try {
          return await api<MapSelection>("/api/map-selections", { method: "POST", body: JSON.stringify(selectionRequest(dateRange, preferences.timezone, workoutIds, requestFocusId)), signal: controller.signal }, csrfToken);
        } catch (error) {
          if (!(error instanceof ApiError) || error.status !== 503 || attempt >= MAP_SELECTION_RETRY_DELAYS_MS.length) throw error;
          await new Promise((resolve) => setTimeout(resolve, MAP_SELECTION_RETRY_DELAYS_MS[attempt]));
          if (!active) throw new DOMException("Map selection cancelled", "AbortError");
        }
      }
    };
    const loadSelection = async () => {
      if (!bootstrapAvailableWorkouts) return { created: await createSelection(selectedWorkoutIds) };
      const catalog = await createSelection();
      try {
        return { created: await createSelection(selectedWorkoutIds), catalog };
      } catch (error) {
        void removeSelection(catalog.id);
        throw error;
      }
    };
    setSelectionPending(true); setSelectionError("");
    void loadSelection()
      .then(({ created, catalog }) => {
        if (!active) { void removeSelection(created.id); if (catalog) void removeSelection(catalog.id); return; }
        const normalizedWorkouts = created.workouts.map((workout) => ({ ...workout, id: workout.id.toUpperCase() }));
        const normalizedAvailableWorkouts = (catalog?.workouts ?? created.workouts).map((workout) => ({ ...workout, id: workout.id.toUpperCase() }));
        const normalized = { ...created, id: created.id.toUpperCase(), workouts: normalizedWorkouts };
        const previousID = activeSelectionRef.current;
        activeSelectionRef.current = normalized.id;
        setSelection(normalized);
        updateAvailableWorkouts(selectedWorkoutIds === undefined || availableWorkouts.length === 0 ? normalizedAvailableWorkouts : mergeWorkoutReadiness(availableWorkouts, normalizedWorkouts));
        const requestedWorkout = fitIntent === "requested" && fitWorkoutId
          ? (availableWorkouts.find((workout) => workout.id === fitWorkoutId) ?? normalizedWorkouts.find((workout) => workout.id === fitWorkoutId))
          : undefined;
        if (requestedWorkout) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: requestedWorkout.bounds }));
        else if (fitIntent !== undefined && normalized.bounds) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: normalized.bounds! }));
        if (pendingSelectionFitRef.current === fitIntent) pendingSelectionFitRef.current = undefined;
        setSelectionPending(false);
		if (catalog && catalog.id.toUpperCase() !== normalized.id) void removeSelection(catalog.id);
		if (previousID && previousID !== normalized.id) window.setTimeout(() => { void removeSelection(previousID); }, 5000);
      })
      .catch((error) => { if (active && !(error instanceof DOMException && error.name === "AbortError")) { setSelectionError("Routes could not be prepared for this map."); setSelectionPending(false); } });
    // Focus only changes local overlays and the camera, not the tile capability's workout set.
    return () => { active = false; controller.abort(); };
  }, [csrfToken, dateRange, preferences.timezone, selectedWorkoutIds?.join(",") ?? "all", selectionRefresh]);

	useEffect(() => {
		if (!selection) return;
		const expiresAt = Date.parse(selection.expiresAt);
		if (!Number.isFinite(expiresAt)) return;
		const refreshIfExpired = () => { if (Date.now() >= expiresAt) refreshSelection(); };
		const timer = window.setTimeout(refreshSelection, Math.min(2_147_483_647, Math.max(0, expiresAt-Date.now())));
		const visibility = () => { if (document.visibilityState === "visible") refreshIfExpired(); };
		window.addEventListener("focus", refreshIfExpired);
		document.addEventListener("visibilitychange", visibility);
		return () => { window.clearTimeout(timer); window.removeEventListener("focus", refreshIfExpired); document.removeEventListener("visibilitychange", visibility); };
	}, [selection?.id, selection?.expiresAt]);

  const coverageReadinessPollKey = selection?.workouts.map((workout) => `${workout.id}:${workout.coverageReadiness.mapDataStatus}:${workout.coverageReadiness.processingStatus}:${workout.coverageReadiness.resultStatus}`).join(",") ?? "";
  const coverageReadinessPolling = mapMode === "coverage" && !preferences.coverageDiagnosticsEnabled && Boolean(selection?.workouts.some(shouldPollCoverage));
  useEffect(() => {
    if (!coverageReadinessPolling || !selection) return;
    const controller = new AbortController();
    let active = true;
    let timer: number | undefined;
    const removeSelection = (id: string) => api<void>(`/api/map-selections/${encodeURIComponent(id.toUpperCase())}`, { method: "DELETE" }, csrfToken).catch(() => undefined);
    const schedule = () => { timer = window.setTimeout(poll, COVERAGE_READINESS_POLL_MS); };
    const poll = async () => {
      if (selectionPendingRef.current) { schedule(); return; }
      try {
        const created = await api<MapSelection>("/api/map-selections", { method: "POST", body: JSON.stringify(selectionRequest(dateRange, preferences.timezone, selectedWorkoutIds, selectionFocusedWorkoutId)), signal: controller.signal }, csrfToken);
        const normalizedWorkouts = created.workouts.map((workout) => ({ ...workout, id: workout.id.toUpperCase() }));
        const prior = new Map(selection.workouts.map((workout) => [workout.id, workout.coverageReadiness]));
        const changed = normalizedWorkouts.some((workout) => JSON.stringify(prior.get(workout.id)) !== JSON.stringify(workout.coverageReadiness));
        const normalizedID = created.id.toUpperCase();
        if (!active) { if (normalizedID !== activeSelectionRef.current) void removeSelection(normalizedID); return; }
        if (!changed) { if (normalizedID !== activeSelectionRef.current) void removeSelection(normalizedID); schedule(); return; }
        const previousID = activeSelectionRef.current;
        activeSelectionRef.current = normalizedID;
        setSelection({ ...created, id: normalizedID, workouts: normalizedWorkouts });
        updateAvailableWorkouts(mergeWorkoutReadiness(availableWorkouts, normalizedWorkouts));
        if (previousID && previousID !== normalizedID) window.setTimeout(() => { void removeSelection(previousID); }, 5000);
      } catch {
        if (active) schedule();
      }
    };
    schedule();
    return () => { active = false; controller.abort(); if (timer !== undefined) window.clearTimeout(timer); };
  }, [coverageReadinessPolling, coverageReadinessPollKey, selection?.id, csrfToken, dateRange, preferences.timezone, selectedWorkoutIds?.join(",") ?? "all", selectionFocusedWorkoutId]);

  useEffect(() => () => {
    temporaryCoverageSequenceRef.current++;
    if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    if (portionHoverTimerRef.current) clearTimeout(portionHoverTimerRef.current);
    coverageFocusCacheRef.current.clear();
    if (activeSelectionRef.current) void api<void>(`/api/map-selections/${encodeURIComponent(activeSelectionRef.current)}`, { method: "DELETE" }, csrfToken).catch(() => undefined);
  }, [csrfToken]);

  useEffect(() => { setCoverageHighlight(undefined); }, [dateRange, selection?.dataGeneration]);
  useEffect(() => { if (preferences.coverageDiagnosticsEnabled) setLocalMapMode("routes"); }, [preferences.coverageDiagnosticsEnabled]);

  const focusedWorkout = availableWorkouts.find((workout) => workout.id === focusedWorkoutId);
  const focusedFamilyId = focusedWorkout ? resolveBaseFamily(config.baseMaps, [focusedWorkout]) : undefined;
  const automaticFamilyId = focusedFamilyId ?? stickyAutomaticFamilyId;
  const familyId = overrideFamilyId && config.baseMaps.families.some((family) => family.id === overrideFamilyId) ? overrideFamilyId : automaticFamilyId;
  const family = config.baseMaps.families.find((candidate) => candidate.id === familyId) ?? config.baseMaps.families[0];
  const preferredHighlightId = hoveredWorkoutId ?? focusedWorkoutId;
  const highlightedWorkoutId = preferredHighlightId && visibleWorkoutIds.has(preferredHighlightId) ? preferredHighlightId : undefined;
  const visibleFocusedWorkoutId = focusedWorkoutId && visibleWorkoutIds.has(focusedWorkoutId) ? focusedWorkoutId : undefined;
  const focusedCoverageBounds = visibleFocusedWorkoutId ? focusedWorkout?.bounds : undefined;
  const displayedFocusedWorkoutId = hoveredWorkoutId && hoveredWorkoutId !== visibleFocusedWorkoutId ? undefined : visibleFocusedWorkoutId;

  useEffect(() => {
    const sequence = ++temporaryCoverageSequenceRef.current;
    const targetWorkoutID = hoveredWorkoutId ?? visibleFocusedWorkoutId;
    const targetWorkout = targetWorkoutID ? availableWorkouts.find((workout) => workout.id === targetWorkoutID) : undefined;
    if (mapMode !== "coverage" || preferences.coverageDiagnosticsEnabled || !selection || !targetWorkout || targetWorkout.coverageReadiness.resultStatus === "none") {
      setCoverageFocus(EMPTY_COVERAGE_FOCUS);
      return;
    }
    const targetID = targetWorkout.id;
    const key = `${selection.id}:${targetID}`;
    const cached = coverageFocusCacheRef.current.get(key);
    if (cached) { setCoverageFocus(cached); return; }
    void api<CoverageFocusFeatureCollection>(`/api/map-selections/${encodeURIComponent(selection.id)}/coverage-focus/${encodeURIComponent(targetID)}?generation=${selection.dataGeneration}`, {}, csrfToken)
      .then((value) => {
        if (temporaryCoverageSequenceRef.current !== sequence || activeSelectionRef.current !== selection.id) return;
        coverageFocusCacheRef.current.set(key, value);
        setCoverageFocus(value);
      })
      .catch((error) => {
        if (error instanceof ApiError && error.status === 404) refreshSelection();
        if (temporaryCoverageSequenceRef.current === sequence) setCoverageFocus(EMPTY_COVERAGE_FOCUS);
      });
  }, [hoveredWorkoutId, visibleFocusedWorkoutId, mapMode, preferences.coverageDiagnosticsEnabled, selection?.id, selection?.dataGeneration, csrfToken]);

	const endpointRawWorkoutId = visibleFocusedWorkoutId ?? (visibleWorkoutIds.size === 1 ? [...visibleWorkoutIds][0] : undefined);
	const rawRouteWorkoutId = mapMode === "routes" ? highlightedWorkoutId ?? endpointRawWorkoutId : preferences.coverageDiagnosticsEnabled ? visibleFocusedWorkoutId : undefined;
	const loadedRawRoute = rawRouteWorkoutId && singleRawRoute?.workoutId === rawRouteWorkoutId ? singleRawRoute : rawRouteWorkoutId ? rawRouteCacheRef.current.get(rawRouteWorkoutId) : undefined;
	const displayedSingleRawRoute = endpointRawWorkoutId && singleRawRoute?.workoutId === endpointRawWorkoutId ? singleRawRoute : endpointRawWorkoutId ? rawRouteCacheRef.current.get(endpointRawWorkoutId) : undefined;
	const directionRawRoute = mapMode === "routes" && highlightedWorkoutId ? loadedRawRoute?.route : mapMode === "coverage" && preferences.coverageDiagnosticsEnabled ? displayedSingleRawRoute?.route : undefined;
	const markerRawRoute = mapMode === "routes" && highlightedWorkoutId ? loadedRawRoute : preferences.coverageDiagnosticsEnabled ? displayedSingleRawRoute : undefined;
	const showRawRouteEndpoints = mapMode === "coverage" ? preferences.coverageDiagnosticsEnabled : Boolean(highlightedWorkoutId) || visibleWorkoutIds.size === 1;
	const diagnosticFeatureEnabled = config.features.coverageMatcherDiagnostics && preferences.coverageDiagnosticsEnabled;
	const diagnosticCompatible = diagnosticFeatureEnabled && Boolean(visibleFocusedWorkoutId && selection?.workouts.some((workout) => workout.id === visibleFocusedWorkoutId)) && !selectionPending && !selectionError;
	const productionCoverageCompatible = !preferences.coverageDiagnosticsEnabled && Boolean(selection && visibleWorkoutIds.size) && !selectionPending && !selectionError;
	const coverageCompatible = preferences.coverageDiagnosticsEnabled ? diagnosticCompatible : productionCoverageCompatible;
  const allRoutesSelected = availableWorkouts.length > 0 && availableWorkouts.every((workout) => visibleWorkoutIds.has(workout.id));
  const statusMessage = rangeError || baseMapError || selectionError || (selectionPending ? "Updating routes..." : "");
  const dismissibleStatusError = !rangeError && Boolean(baseMapError || selectionError);
  const dismissStatusError = () => { if (baseMapError) setBaseMapError(""); else setSelectionError(""); };

  useEffect(() => {
    if (focusedFamilyId) {
      setStickyAutomaticFamilyId(focusedFamilyId);
    }
  }, [focusedFamilyId]);

  useEffect(() => {
    if (mapMode === "coverage" && !preferences.coverageDiagnosticsEnabled && focusedWorkout?.coverageReadiness.resultStatus === "none") clearFocusedWorkout();
  }, [mapMode, preferences.coverageDiagnosticsEnabled, focusedWorkout?.id, focusedWorkout?.coverageReadiness.resultStatus]);

  useEffect(() => {
    requestDiagnosticHover(undefined);
    setDiagnostic(undefined);
    setDiagnosticError("");
    setHoveredPortionOrdinal(undefined);
    setLockedPortionOrdinal(undefined);
    setLabelSaveError("");
		if (preferences.coverageDiagnosticsEnabled) setMapMode("routes");
	}, [focusedWorkoutId, dateRange, selectedWorkoutIds?.join(",") ?? "all", preferences.coverageDiagnosticsEnabled]);

  useEffect(() => {
    const diagnosticActive = mapMode === "coverage" && Boolean(diagnostic);
    const productionActive = mapMode === "coverage" && !preferences.coverageDiagnosticsEnabled && Boolean(visibleFocusedWorkoutId);
    const routesActive = mapMode === "routes" && Boolean(visibleFocusedWorkoutId);
    if (!diagnosticActive && !productionActive && !routesActive) { setRawRouteHidden(false); setNonFocusedRoutesHidden(false); setNonFocusedCoverageHidden(false); return; }
    const editableTarget = (target: EventTarget | null) => target instanceof Element && Boolean(target.closest("input, textarea, select, button, a, [contenteditable='true']"));
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
		if (!rawRouteWorkoutId) { setSingleRawRoute(undefined); return; }
		const cached = rawRouteCacheRef.current.get(rawRouteWorkoutId);
		if (cached) { setSingleRawRoute(cached); return; }
		const controller = new AbortController();
		setSingleRawRoute(undefined);
		void api<RawRoutePoints>(`/api/workouts/${encodeURIComponent(rawRouteWorkoutId)}/route/points`, { signal: controller.signal }, csrfToken)
			.then(({ points }) => { if (!controller.signal.aborted) { const loaded = { workoutId: rawRouteWorkoutId, route: buildSegmentedRawRoute(points), endpoints: buildRawRouteEndpoints(points) }; rawRouteCacheRef.current.set(rawRouteWorkoutId, loaded); setSingleRawRoute(loaded); } })
			.catch(() => { if (!controller.signal.aborted) setSingleRawRoute(undefined); });
		return () => controller.abort();
	}, [csrfToken, rawRouteWorkoutId]);

  useEffect(() => {
    if (mapMode !== "coverage" || !diagnosticCompatible || !visibleFocusedWorkoutId || diagnostic?.workoutId === visibleFocusedWorkoutId) return;
    const controller = new AbortController();
    const sequence = ++diagnosticSequenceRef.current;
    setDiagnosticPending(true); setDiagnosticError(""); requestDiagnosticHover(undefined); setLockedPortionOrdinal(undefined);
    const requestDiagnostic = () => api<CoverageDiagnosticRun>(`/api/workouts/${encodeURIComponent(visibleFocusedWorkoutId)}/coverage-diagnostic-runs`, { method: "POST", body: "{}", signal: controller.signal }, csrfToken);
    void retryInitialDiagnosticBusy(requestDiagnostic, controller.signal)
      .then((created) => {
        if (controller.signal.aborted || sequence !== diagnosticSequenceRef.current) return;
        const normalized = { ...created, id: created.id.toUpperCase(), workoutId: created.workoutId.toUpperCase() };
        setDiagnostic(normalized); setDiagnosticPending(false);
        const bounds = diagnosticBounds(normalized.overlay);
        if (bounds) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds }));
      })
      .catch((error) => {
        if (controller.signal.aborted || sequence !== diagnosticSequenceRef.current || (error instanceof DOMException && error.name === "AbortError")) return;
        setDiagnosticPending(false);
        setDiagnosticError(error instanceof ApiError && error.status === 429
          ? "The diagnostic overlay could not be prepared because all available workers are currently busy. Please try again later."
          : "The diagnostic overlay could not be prepared.");
      });
    return () => { controller.abort(); diagnosticSequenceRef.current++; };
  }, [csrfToken, dateRange, diagnosticCompatible, diagnosticRetry, focusedWorkoutId, mapMode, selection?.id, selectedWorkoutIds?.join(",") ?? "all"]);

  useEffect(() => () => { diagnosticSequenceRef.current++; labelSequenceRef.current++; }, []);

  async function saveDiagnosticLabels(next: CoverageDiagnosticRun["labels"]) {
    if (!diagnostic) return;
    const sequence = ++labelSequenceRef.current;
    setDiagnostic((current) => current ? { ...current, labels: next } : current);
    setLabelSavePending(true); setLabelSaveError("");
    const body = { ...(next.overall ? { overall: next.overall } : {}), segments: next.segments };
    try {
      const labels = await api<CoverageDiagnosticRun["labels"]>(`/api/coverage-diagnostic-runs/${encodeURIComponent(diagnostic.id)}/labels`, { method: "PATCH", body: JSON.stringify(body) }, csrfToken);
      if (sequence !== labelSequenceRef.current) return;
      setDiagnostic((current) => current?.id === diagnostic.id ? { ...current, labels } : current);
      setLabelSavePending(false);
    } catch {
      if (sequence !== labelSequenceRef.current) return;
      setLabelSavePending(false); setLabelSaveError("Labels were not saved. Try again.");
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
		if (mapMode === "coverage" && !preferences.coverageDiagnosticsEnabled && workout.coverageReadiness.resultStatus === "none") return;
		requestedFocusActiveRef.current = false;
		const location = new URL(window.location.href);
		location.pathname = "/map";
		location.searchParams.set("workoutId", workout.id);
		history.replaceState(history.state, "", `${location.pathname}${location.search}${location.hash}`);
    updateFocusedWorkout(workout.id);
    const current = selectedWorkoutIds ?? availableWorkouts.map((item) => item.id);
    if (!current.includes(workout.id)) updateWorkoutSelection([...current, workout.id].sort());
    setFitRequest((currentRequest) => ({ key: (currentRequest?.key ?? 0) + 1, bounds: workout.bounds }));
    setCanvasFocusRequest((value) => value + 1);
  }

	function focusWorkoutFromMap(workoutId: string) {
		const workout = availableWorkouts.find((item) => item.id === workoutId);
		if (!workout) return;
		cancelRouteHover();
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

  async function selectRange(next: DateRangePreference) {
    pendingSelectionFitRef.current = "all";
    onDateRangeSelected(next);
    updateWorkoutSelection(undefined);
    updateAvailableWorkouts([]);
    clearFocusedWorkout();
    cancelRouteHover();
    setRangeError("");
    const sequence = ++rangeSaveSequence.current;
    const request = rangeSaveChain.current.then(() => api<Preferences>("/api/me/preferences", { method: "PATCH", body: JSON.stringify({ dateRange: next }) }, csrfToken));
    rangeSaveChain.current = request.then(() => undefined, () => undefined);
    try {
      await request;
      if (sequence === rangeSaveSequence.current) setRangeError("");
    } catch {
      if (sequence === rangeSaveSequence.current) setRangeError("Your default date range was not saved. This map will continue using your selection.");
    }
  }

  async function showCoverageEntity(entity: RoadCoverageEntity) {
    if (!selection) throw new Error("Map selection is unavailable");
    const detail = await api<RoadCoverageDetail>(`/api/map-selections/${encodeURIComponent(selection.id)}/coverage/${entity.entityKind}/${encodeURIComponent(entity.entityId)}?generation=${selection.dataGeneration}`);
    setRoadCoverageOpen(false);
    setCoverageHighlight((current) => ({ key: (current?.key ?? 0) + 1, geometry: detail.geometry, fitBounds: detail.fitBounds }));
  }

  function showCoverageWorkout(workoutId: string) {
    setMapMode("coverage");
    focusWorkoutFromMap(workoutId.toUpperCase());
  }

  const controls = (suffix: string) => <>
    <DropdownMenu.Root><DropdownMenu.Trigger className="range-trigger" aria-label="Select date range"><span>Date range</span><strong>{rangeLabel(dateRange)}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align={suffix === "desktop" ? "start" : "center"} sideOffset={8}>{QUICK_RANGES.map(([value, label]) => <DropdownMenu.Item key={value} onSelect={() => void selectRange(value)}>{label}{dateRange === value && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}<DropdownMenu.Separator /><DropdownMenu.Item onSelect={() => setCustomOpen(true)}>Custom...{EXPLICIT_RANGE.test(dateRange) && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item></DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>
    <DropdownMenu.Root><DropdownMenu.Trigger className="range-trigger" aria-label="Select base map"><span>Base map</span><strong>{overrideFamilyId ? family?.label : `Automatic / ${family?.label ?? "Unavailable"}`}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align={suffix === "desktop" ? "start" : "center"} sideOffset={8}><DropdownMenu.Item onSelect={() => setOverrideFamilyId(undefined)}>Automatic{!overrideFamilyId && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item><DropdownMenu.Separator />{config.baseMaps.families.map((candidate) => <DropdownMenu.Item key={candidate.id} onSelect={() => setOverrideFamilyId(candidate.id)}>{candidate.label}{overrideFamilyId === candidate.id && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}</DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>
		<fieldset className="map-workout-filter"><legend>Workout routes</legend><div className="map-route-toolbar"><label className="map-route-toggle"><input type="checkbox" aria-label="Select all workout routes" checked={allRoutesSelected} disabled={!availableWorkouts.length} onChange={toggleAllWorkouts} /></label><div className="map-mode-controls" role="group" aria-label="Map mode"><button type="button" aria-pressed={mapMode === "routes"} onClick={() => { setMapMode("routes"); setCanvasFocusRequest((value) => value + 1); }}>Routes</button><button type="button" aria-pressed={mapMode === "coverage"} disabled={!coverageCompatible} onClick={() => { setMapMode("coverage"); setCanvasFocusRequest((value) => value + 1); }}>Coverage</button></div></div><div className="map-filter-options">{availableWorkouts.length ? <WorkoutRouteList workouts={availableWorkouts} preferences={preferences} sort={sort} mode={mapMode} diagnosticCoverage={preferences.coverageDiagnosticsEnabled} visibleIDs={selectedWorkoutIds} highlightedWorkoutId={highlightedWorkoutId} focusedWorkoutId={displayedFocusedWorkoutId} scrollRequest={listScrollRequest} onToggle={toggleWorkout} onFocus={focusWorkout} onHover={requestRouteHover} /> : <p className="map-routes-empty">No workout routes in this range.</p>}</div></fieldset>
    <button type="button" className="secondary map-fit-button" disabled={!selection?.bounds} onClick={() => selection?.bounds && setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: selection.bounds! }))}>Fit routes</button>
  </>;

  return <main className="map-page">
    <CustomDateRangeDialog open={customOpen} onOpenChange={setCustomOpen} range={dateRange} onApply={(next) => void selectRange(next)} />
    <aside className="map-sidebar" aria-label="Map controls"><div className="map-controls">{controls("desktop")}</div></aside>
    <section className="map-stage" aria-live="polite">
      {statusMessage && <div className="map-banner" role="status"><span>{statusMessage}</span>{dismissibleStatusError && <button type="button" className="map-banner-dismiss" aria-label={baseMapError ? "Dismiss base map warning" : "Dismiss route preparation error"} onClick={dismissStatusError}>&times;</button>}</div>}
      {family && <MapCanvas family={family} preferences={preferences} selection={selection} coverageFocus={coverageFocus} workouts={availableWorkouts} fitPadding={config.mapFitPaddingPixels} hoveredWorkoutId={highlightedWorkoutId} fitRequest={fitRequest} focusRequest={canvasFocusRequest} coverageHighlight={coverageHighlight} diagnosticEnabled={diagnosticFeatureEnabled} productionCoverageEnabled={!preferences.coverageDiagnosticsEnabled} diagnosticMode={mapMode === "coverage"} routeHidingEnabled={mapMode === "routes" && Boolean(visibleFocusedWorkoutId)} nonFocusedRoutesHidden={nonFocusedRoutesHidden} rawRouteHidden={rawRouteHidden} nonFocusedCoverageHidden={nonFocusedCoverageHidden} diagnosticRawRoute={preferences.coverageDiagnosticsEnabled ? displayedSingleRawRoute?.route : undefined} directionRoute={directionRawRoute} rawRouteEndpoints={markerRawRoute?.endpoints} rawRouteEndpointsVisible={showRawRouteEndpoints} diagnostic={diagnostic} highlightedPortionOrdinal={hoveredPortionOrdinal ?? lockedPortionOrdinal} onDiagnosticHover={requestDiagnosticHover} onDiagnosticLock={setLockedPortionOrdinal} onHover={requestRouteHover} onRouteClick={focusWorkoutFromMap} onBaseMapError={() => setBaseMapError("The public base map could not be loaded. Your private routes remain available. Retrying in 5 seconds...")} onBaseMapReady={() => setBaseMapError("")} onRouteTilesUnavailable={refreshSelection} />}
		{mapMode === "coverage" && preferences.coverageDiagnosticsEnabled && <DiagnosticReviewCard run={diagnostic} pending={diagnosticPending} error={diagnosticError} selectedPortionOrdinal={lockedPortionOrdinal} savePending={labelSavePending} saveError={labelSaveError}
        onRetry={() => { setDiagnostic(undefined); setDiagnosticRetry((value) => value + 1); }}
        onFit={() => { const bounds = diagnostic && diagnosticBounds(diagnostic.overlay); if (bounds) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds, focusCanvas: true })); }}
			onCopyFocus={() => setCanvasFocusRequest((value) => value + 1)}
        onOverallLabel={(overall) => diagnostic && void saveDiagnosticLabels({ ...diagnostic.labels, overall })}
			onSegmentLabel={(portionOrdinal, label) => { if (!diagnostic) return; const segments = [...diagnostic.labels.segments.filter((item) => item.portionOrdinal !== portionOrdinal), { portionOrdinal, label }].sort((left, right) => left.portionOrdinal - right.portionOrdinal); void saveDiagnosticLabels({ ...diagnostic.labels, segments }); }} />}
		{mapMode === "coverage" && !preferences.coverageDiagnosticsEnabled && selection && !roadCoverageOpen && <CoverageOverviewCard fitDisabled={!focusedCoverageBounds} onFit={() => { if (focusedCoverageBounds) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: focusedCoverageBounds, focusCanvas: true })); }} onOpen={() => setRoadCoverageOpen(true)} />}
      <RoadCoverageDialog open={roadCoverageOpen} selection={selection} pageSize={preferences.pageSize} cache={roadCoverageCache} onCacheChange={onRoadCoverageCacheChange} onOpenChange={(open) => { setRoadCoverageOpen(open); if (!open) setCanvasFocusRequest((value) => value + 1); }} onReturnFocus={() => setCanvasFocusRequest((value) => value + 1)} onSelectionUnavailable={refreshSelection} onShowEntity={showCoverageEntity} onShowWorkout={showCoverageWorkout} />
      {family && <div className="map-attribution" aria-label={`Active map attribution for ${family.label}`}><span>{family.attribution.text}</span>{family.attribution.links.map((link) => <a key={`${link.label}-${link.url}`} href={link.url} target="_blank" rel="noreferrer">{link.label}</a>)}</div>}
      <Dialog.Root open={sheetOpen} onOpenChange={setSheetOpen}><Dialog.Trigger asChild><button type="button" className="mobile-map-sheet-trigger">Routes and controls</button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="map-sheet-overlay" /><Dialog.Content id="mobile-map-sheet" className="mobile-map-sheet"><div className="mobile-sheet-handle" aria-hidden="true" /><div className="mobile-sheet-heading"><div><Dialog.Title>Routes and controls</Dialog.Title><Dialog.Description>Filter visible workouts and choose how the base map is presented.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Routes and controls">&times;</Dialog.Close></div><div className="map-controls">{controls("mobile")}</div></Dialog.Content></Dialog.Portal></Dialog.Root>
    </section>
  </main>;
}
