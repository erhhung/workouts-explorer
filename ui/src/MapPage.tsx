import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import maplibregl, { type Map as MapLibreMap, type MapGeoJSONFeature, type MapMouseEvent, type StyleSpecification, type VectorTileSource } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  DEFAULT_WORKOUT_SORT,
  type BaseMapFamily,
  type BaseMapsConfig,
  type DateRangeEnum,
  type DateRangePreference,
  type MapSelection,
  type MapSelectionWorkout,
  type Preferences,
  type PublicConfig,
  type WorkoutColumn,
  type WorkoutSort,
} from "./api";

const ROUTES_LAYER = "private-workout-routes";
const HOVER_LAYER = "private-workout-route-hover";
const ROUTE_MARKERS_LAYER = "private-workout-route-markers";
const HOVER_MARKERS_LAYER = "private-workout-route-marker-hover";
const ROUTES_SOURCE = "private-workout-routes";
const ROUTE_HOVER_DELAY_MS = 250;
const EXPLICIT_RANGE = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/;
const COMPACT_UUID = /^[0-9A-F]{32}$/;
const QUICK_RANGES: ReadonlyArray<[DateRangeEnum, string]> = [
  ["thisWeek", "This week"], ["lastWeek", "Last week"], ["last7Days", "Last 7 days"], ["last30Days", "Last 30 days"],
  ["thisMonth", "This month"], ["lastMonth", "Last month"], ["thisYear", "This year"], ["lastYear", "Last year"],
];
const ROUTE_PALETTE = ["#ef9b61", "#75bda6", "#e2c86e", "#68a9df", "#e07a9a", "#9f91df", "#69c3c8", "#d7a65c"];
const SEMANTIC_ROUTE_COLORS = { walk: "#43d5e5", hiking: "#8ed081", cycling: "#69aef5" } as const;

export function selectionRequest(range: DateRangePreference, timezone: string, workoutIds?: string[]) {
  const explicit = EXPLICIT_RANGE.exec(range);
  const selector = explicit ? { startDate: explicit[1], endDate: explicit[2] } : { dateRangeEnum: range, tz: timezone };
  return workoutIds === undefined ? selector : { ...selector, workoutIds };
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
  return quick?.[1] ?? (explicit ? `${explicit[1]} to ${explicit[2]}` : "Last 30 days");
}

function validDate(value: string) {
  const date = new Date(`${value}T00:00:00Z`);
  return /^\d{4}-\d{2}-\d{2}$/.test(value) && !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
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
    const order = Number(feature.properties?.sort_order ?? Number.NEGATIVE_INFINITY);
    const newestOrder = Number(newest?.properties?.sort_order ?? Number.NEGATIVE_INFINITY);
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

export function absoluteRouteTileTemplate(template: string, origin: string) {
  return template.startsWith("/") ? `${origin}${template}` : template;
}

type RouteBounds = NonNullable<MapSelection["bounds"]>;

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

function MapCanvas({ family, preferences, selection, workouts, fitPadding, hoveredWorkoutId, fitRequest, onHover, onBaseMapError }: {
  family: BaseMapFamily; preferences: Preferences; selection?: MapSelection; workouts: MapSelectionWorkout[];
  fitPadding: number; hoveredWorkoutId?: string; fitRequest?: { key: number; bounds: RouteBounds };
  onHover: (id?: string) => void; onBaseMapError: () => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibreMap | undefined>(undefined);
  const selectionRef = useRef(selection);
  const routeUrlRef = useRef(selection?.routeTileUrl);
  const installedRouteUrlRef = useRef<string | undefined>(undefined);
  const visibleRouteCountRef = useRef(selection?.workouts.length ?? 0);
  const replaceEmptySourceRef = useRef(false);
  const hoverRef = useRef(hoveredWorkoutId);
  const workoutsRef = useRef(workouts);
  const preferencesRef = useRef(preferences);
  const styleUrlRef = useRef(family.styles[preferences.theme]);
  const routeColorsRef = useRef(routeColors(selection?.workouts ?? []));
  const styleLoadedRef = useRef(false);
  const fallbackInstalledRef = useRef(false);
  const defaultViewportSetRef = useRef(false);
  const popupRef = useRef<maplibregl.Popup | undefined>(undefined);
  const popupTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const popupWorkoutRef = useRef<string | undefined>(undefined);
  const popupLocationRef = useRef<maplibregl.LngLat | undefined>(undefined);
  const fallbackStyleRef = useRef({ version: 8 as const, sources: {}, layers: [{ id: "fallback-background", type: "background" as const, paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" } }] });

  function syncPrivateLayers(map: MapLibreMap) {
    if (!routeUrlRef.current) return;
    const absoluteURL = absoluteRouteTileTemplate(routeUrlRef.current, window.location.origin);
    let existingSource = map.getSource(ROUTES_SOURCE) as VectorTileSource | undefined;
    if (existingSource && replaceEmptySourceRef.current) {
      if (map.getLayer(HOVER_MARKERS_LAYER)) map.removeLayer(HOVER_MARKERS_LAYER);
      if (map.getLayer(HOVER_LAYER)) map.removeLayer(HOVER_LAYER);
      if (map.getLayer(ROUTE_MARKERS_LAYER)) map.removeLayer(ROUTE_MARKERS_LAYER);
      if (map.getLayer(ROUTES_LAYER)) map.removeLayer(ROUTES_LAYER);
      map.removeSource(ROUTES_SOURCE);
      installedRouteUrlRef.current = undefined;
      existingSource = undefined;
    }
    replaceEmptySourceRef.current = false;
    if (!existingSource) map.addSource(ROUTES_SOURCE, { type: "vector", tiles: [absoluteURL] });
    else if (installedRouteUrlRef.current !== absoluteURL) existingSource.setTiles([absoluteURL]);
    installedRouteUrlRef.current = absoluteURL;
    const colorExpression: unknown[] = ["match", ["get", "workout_type_key"]];
    for (const [typeKey, color] of routeColorsRef.current) colorExpression.push(typeKey, color);
    colorExpression.push("#e9a852");
    if (!map.getLayer(ROUTES_LAYER)) map.addLayer({
        id: ROUTES_LAYER, type: "line", source: ROUTES_SOURCE, "source-layer": "routes",
        layout: { "line-cap": "round", "line-join": "round", "line-sort-key": ["get", "sort_order"] },
        paint: { "line-color": colorExpression as never, "line-width": ["interpolate", ["linear"], ["zoom"], 5, 2, 14, 4], "line-opacity": 0.82 },
      });
    else map.setPaintProperty(ROUTES_LAYER, "line-color", colorExpression as never);
    if (!map.getLayer(ROUTE_MARKERS_LAYER)) map.addLayer({
        id: ROUTE_MARKERS_LAYER, type: "circle", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["==", ["geometry-type"], "Point"],
        layout: { "circle-sort-key": ["get", "sort_order"] },
        paint: { "circle-color": colorExpression as never, "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 3, 14, 5], "circle-opacity": 0.9 },
      });
    else map.setPaintProperty(ROUTE_MARKERS_LAYER, "circle-color", colorExpression as never);
    if (!map.getLayer(HOVER_LAYER)) map.addLayer({
        id: HOVER_LAYER, type: "line", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["==", ["get", "workout_id"], hoverRef.current ?? ""],
        layout: { "line-cap": "round", "line-join": "round" },
        paint: { "line-color": "#c026ff", "line-width": ["interpolate", ["linear"], ["zoom"], 5, 4, 14, 7], "line-opacity": 1 },
      });
    if (!map.getLayer(HOVER_MARKERS_LAYER)) map.addLayer({
        id: HOVER_MARKERS_LAYER, type: "circle", source: ROUTES_SOURCE, "source-layer": "routes",
        filter: ["all", ["==", ["geometry-type"], "Point"], ["==", ["get", "workout_id"], hoverRef.current ?? ""]],
        layout: { "circle-sort-key": ["get", "sort_order"] },
        paint: { "circle-color": "#c026ff", "circle-radius": ["interpolate", ["linear"], ["zoom"], 5, 6, 14, 9], "circle-opacity": 1 },
      });
  }

  function preservePrivateLayers(previous: StyleSpecification | undefined, next: StyleSpecification) {
    if (!previous?.sources[ROUTES_SOURCE]) return next;
    const privateLayers = previous.layers.filter((layer) => [ROUTES_LAYER, ROUTE_MARKERS_LAYER, HOVER_LAYER, HOVER_MARKERS_LAYER].includes(layer.id));
    return { ...next, sources: { ...next.sources, [ROUTES_SOURCE]: previous.sources[ROUTES_SOURCE] }, layers: [...next.layers, ...privateLayers] };
  }

  function removePopup() {
    if (popupTimerRef.current) clearTimeout(popupTimerRef.current);
    popupTimerRef.current = undefined;
    popupWorkoutRef.current = undefined;
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

  useEffect(() => {
    if (!containerRef.current) return;
    const map = new maplibregl.Map({ container: containerRef.current, style: fallbackStyleRef.current, attributionControl: false });
    mapRef.current = map;
    const restore = () => {
      styleLoadedRef.current = true;
      syncPrivateLayers(map);
    };
    const restoreMissedInitialStyle = () => {
      if (!styleLoadedRef.current) restore();
    };
    const styleError = () => {
      if (styleLoadedRef.current || fallbackInstalledRef.current) return;
      fallbackInstalledRef.current = true;
      onBaseMapError();
      map.setStyle(fallbackStyleRef.current);
    };
    const hover = (event: MapMouseEvent) => {
      const feature = topmostFeature(map.queryRenderedFeatures(event.point, { layers: [ROUTES_LAYER, ROUTE_MARKERS_LAYER] }));
      const workoutID = typeof feature?.properties?.workout_id === "string" ? feature.properties.workout_id.toUpperCase() : undefined;
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
    map.on("mousemove", ROUTES_LAYER, hover);
    map.on("mouseleave", ROUTES_LAYER, leave);
    map.on("mousemove", ROUTE_MARKERS_LAYER, hover);
    map.on("mouseleave", ROUTE_MARKERS_LAYER, leave);
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
    styleLoadedRef.current = false;
    fallbackInstalledRef.current = false;
    map.setStyle(family.styles[preferences.theme], { transformStyle: preservePrivateLayers });
    return () => { map.off("style.load", restore); map.off("load", restoreMissedInitialStyle); map.off("load", showDefaultViewport); map.off("error", styleError); map.off("mousemove", ROUTES_LAYER, hover); map.off("mouseleave", ROUTES_LAYER, leave); map.off("mousemove", ROUTE_MARKERS_LAYER, hover); map.off("mouseleave", ROUTE_MARKERS_LAYER, leave); removePopup(); map.remove(); mapRef.current = undefined; };
  }, []);

  useEffect(() => {
    const nextRouteCount = selection?.workouts.length ?? 0;
    replaceEmptySourceRef.current = visibleRouteCountRef.current === 0 && nextRouteCount > 0;
    visibleRouteCountRef.current = nextRouteCount;
    routeUrlRef.current = selection?.routeTileUrl;
    selectionRef.current = selection;
    routeColorsRef.current = routeColors(selection?.workouts ?? []);
    workoutsRef.current = workouts;
    preferencesRef.current = preferences;
    const map = mapRef.current;
    if (map && styleLoadedRef.current) syncPrivateLayers(map);
  }, [preferences, selection?.id, selection?.routeTileUrl, workouts]);

  useEffect(() => {
    const map = mapRef.current;
    const nextStyle = family.styles[preferences.theme];
    fallbackStyleRef.current = { version: 8, sources: {}, layers: [{ id: "fallback-background", type: "background", paint: { "background-color": preferences.theme === "dark" ? "#0b1514" : "#f3f0e7" } }] };
    if (map && styleUrlRef.current !== nextStyle) {
      styleUrlRef.current = nextStyle;
      styleLoadedRef.current = false;
      fallbackInstalledRef.current = false;
      map.setStyle(nextStyle, { transformStyle: preservePrivateLayers });
    }
  }, [family.id, family.styles, preferences.theme]);

  useEffect(() => {
    hoverRef.current = hoveredWorkoutId;
    const map = mapRef.current;
    if (map?.getLayer(HOVER_LAYER)) map.setFilter(HOVER_LAYER, ["==", ["get", "workout_id"], hoveredWorkoutId ?? ""]);
    if (map?.getLayer(HOVER_MARKERS_LAYER)) map.setFilter(HOVER_MARKERS_LAYER, ["all", ["==", ["geometry-type"], "Point"], ["==", ["get", "workout_id"], hoveredWorkoutId ?? ""]]);
  }, [hoveredWorkoutId]);

  useEffect(() => {
    const map = mapRef.current;
    const bounds = fitRequest?.bounds;
    if (map && bounds) map.fitBounds([[bounds.minimumLongitude, bounds.minimumLatitude], [bounds.maximumLongitude, bounds.maximumLatitude]], { padding: fitPadding, duration: 350 });
  }, [fitRequest?.key]);

  return <div ref={containerRef} className="map-canvas" role="application" aria-label="Workout route map" />;
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
    return comparison ? (sort.direction === "asc" ? comparison : -comparison) : left.id.localeCompare(right.id);
  });
}

function WorkoutRouteList({ workouts, preferences, sort, visibleIDs, highlightedWorkoutId, focusedWorkoutId, onToggle, onFocus, onHover }: {
  workouts: MapSelectionWorkout[]; preferences: Preferences; sort: WorkoutSort; visibleIDs?: string[]; highlightedWorkoutId?: string; focusedWorkoutId?: string;
  onToggle: (id: string) => void; onFocus: (workout: MapSelectionWorkout) => void; onHover: (id?: string) => void;
}) {
  return <ol className="map-route-list map-workout-list">{sortMapWorkouts(workouts, sort).map((workout) => <li key={workout.id} className={`${highlightedWorkoutId === workout.id ? "is-hovered" : ""}${focusedWorkoutId === workout.id ? " is-focused" : ""}`} onPointerEnter={() => onHover(workout.id)} onPointerLeave={() => onHover(undefined)}>
    <label className="map-route-toggle" onClick={(event) => event.stopPropagation()}><input type="checkbox" aria-label={`Show ${workout.type.name} from ${formatWorkoutDate(workout, preferences)}`} checked={visibleIDs === undefined || visibleIDs.includes(workout.id)} onChange={() => onToggle(workout.id)} /></label>
    <button type="button" className="map-route-focus" onClick={() => onFocus(workout)} onFocus={() => onHover(workout.id)} onBlur={() => onHover(undefined)}>
      <span className={`route-swatch route-swatch--${semanticRouteKind(workout.type.name) ?? routeColorIndex(workout.type.key)}`} aria-hidden="true" /><span className="map-route-type">{workout.type.name}</span><small>{formatWorkoutDate(workout, preferences)}</small>
    </button>
  </li>)}</ol>;
}

export default function MapPage({ config, preferences, csrfToken, dateRange, onDateRangeSelected, sort = DEFAULT_WORKOUT_SORT }: {
  config: PublicConfig; preferences: Preferences; csrfToken: string; dateRange: DateRangePreference;
  onDateRangeSelected: (range: DateRangePreference) => void; sort?: WorkoutSort;
}) {
  const [selection, setSelection] = useState<MapSelection>();
  const [selectionPending, setSelectionPending] = useState(true);
  const [selectionError, setSelectionError] = useState("");
  const [baseMapError, setBaseMapError] = useState("");
  const [rangeError, setRangeError] = useState("");
  const [overrideFamilyId, setOverrideFamilyId] = useState<string>();
  const [hoveredWorkoutId, setHoveredWorkoutId] = useState<string>();
  const [fitRequest, setFitRequest] = useState<{ key: number; bounds: RouteBounds }>();
  const [focusedWorkoutId, setFocusedWorkoutId] = useState<string>();
  const [sheetOpen, setSheetOpen] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [customError, setCustomError] = useState("");
  const requestedIds = useMemo(() => requestedWorkoutIds(window.location.search), [window.location.search]);
  const [selectedWorkoutIds, setSelectedWorkoutIds] = useState<string[] | undefined>(() => requestedIds.length ? requestedIds : undefined);
  const [availableWorkouts, setAvailableWorkouts] = useState<MapSelectionWorkout[]>([]);
  const rangeSaveSequence = useRef(0);
  const rangeSaveChain = useRef<Promise<void>>(Promise.resolve());
  const activeSelectionRef = useRef<string | undefined>(undefined);
  const pendingFitAllRef = useRef(true);
  const hoveredWorkoutRef = useRef<string | undefined>(undefined);
  const hoverCandidateRef = useRef<string | undefined>(undefined);
  const hoverTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const visibleWorkoutIds = new Set(selectedWorkoutIds ?? availableWorkouts.map((workout) => workout.id));
  const visibleWorkoutIdsRef = useRef(visibleWorkoutIds);
  visibleWorkoutIdsRef.current = visibleWorkoutIds;

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
    cancelRouteHover();
    hoverCandidateRef.current = workoutId;
    hoverTimerRef.current = setTimeout(() => {
      hoverTimerRef.current = undefined;
      hoverCandidateRef.current = undefined;
      if (visibleWorkoutIdsRef.current.has(workoutId)) setRouteHover(workoutId);
    }, ROUTE_HOVER_DELAY_MS);
  }

  useEffect(() => {
    setSelectedWorkoutIds(requestedIds.length ? requestedIds : undefined);
    setAvailableWorkouts([]);
  }, [requestedIds.join(",")]);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    const removeSelection = (id: string) => api<void>(`/api/map-selections/${encodeURIComponent(id.toUpperCase())}`, { method: "DELETE" }, csrfToken).catch(() => undefined);
    setSelectionPending(true); setSelectionError("");
    void api<MapSelection>("/api/map-selections", { method: "POST", body: JSON.stringify(selectionRequest(dateRange, preferences.timezone, selectedWorkoutIds)), signal: controller.signal }, csrfToken)
      .then((created) => {
        if (!active) { void removeSelection(created.id); return; }
        const normalizedWorkouts = created.workouts.map((workout) => ({ ...workout, id: workout.id.toUpperCase() }));
        const normalized = { ...created, id: created.id.toUpperCase(), workouts: normalizedWorkouts };
        const previousID = activeSelectionRef.current;
        activeSelectionRef.current = normalized.id;
        setSelection(normalized);
        setAvailableWorkouts((current) => selectedWorkoutIds === undefined || current.length === 0 ? normalizedWorkouts : current);
        if (normalized.bounds && (pendingFitAllRef.current || !previousID)) setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: normalized.bounds! }));
        pendingFitAllRef.current = false;
        setSelectionPending(false);
        if (previousID && previousID !== normalized.id) void removeSelection(previousID);
      })
      .catch((error) => { if (active && !(error instanceof DOMException && error.name === "AbortError")) { setSelectionError("Routes could not be prepared for this map."); setSelectionPending(false); } });
    return () => { active = false; controller.abort(); };
  }, [csrfToken, dateRange, preferences.timezone, selectedWorkoutIds?.join(",") ?? "all"]);

  useEffect(() => () => {
    if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    if (activeSelectionRef.current) void api<void>(`/api/map-selections/${encodeURIComponent(activeSelectionRef.current)}`, { method: "DELETE" }, csrfToken).catch(() => undefined);
  }, [csrfToken]);

  const focusedWorkout = availableWorkouts.find((workout) => workout.id === focusedWorkoutId);
  const automaticFamilyId = focusedWorkout ? resolveBaseFamily(config.baseMaps, [focusedWorkout]) : selection ? resolveBaseFamily(config.baseMaps, selection.workouts) : config.baseMaps.fallbackFamilyId;
  const familyId = overrideFamilyId && config.baseMaps.families.some((family) => family.id === overrideFamilyId) ? overrideFamilyId : automaticFamilyId;
  const family = config.baseMaps.families.find((candidate) => candidate.id === familyId) ?? config.baseMaps.families[0];
  const preferredHighlightId = hoveredWorkoutId ?? focusedWorkoutId ?? (requestedIds.length === 1 ? requestedIds[0] : undefined);
  const highlightedWorkoutId = preferredHighlightId && visibleWorkoutIds.has(preferredHighlightId) ? preferredHighlightId : undefined;
  const visibleFocusedWorkoutId = focusedWorkoutId && visibleWorkoutIds.has(focusedWorkoutId) ? focusedWorkoutId : undefined;

  function toggleWorkout(workoutId: string) {
    const current = selectedWorkoutIds ?? availableWorkouts.map((workout) => workout.id);
    if (current.includes(workoutId)) {
      cancelRouteHover(workoutId);
      if (focusedWorkoutId === workoutId) setFocusedWorkoutId(undefined);
      setSelectedWorkoutIds(current.filter((id) => id !== workoutId));
    } else {
      cancelRouteHover();
      setFocusedWorkoutId(workoutId);
      setSelectedWorkoutIds([...current, workoutId].sort());
    }
  }

  function focusWorkout(workout: MapSelectionWorkout) {
    setFocusedWorkoutId(workout.id);
    const current = selectedWorkoutIds ?? availableWorkouts.map((item) => item.id);
    if (!current.includes(workout.id)) setSelectedWorkoutIds([...current, workout.id].sort());
    setFitRequest((currentRequest) => ({ key: (currentRequest?.key ?? 0) + 1, bounds: workout.bounds }));
  }

  async function selectRange(next: DateRangePreference) {
    onDateRangeSelected(next);
    pendingFitAllRef.current = true;
    setSelectedWorkoutIds(undefined);
    setAvailableWorkouts([]);
    setFocusedWorkoutId(undefined);
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

  function submitCustom(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    const start = String(values.get("startDate")); const end = String(values.get("endDate"));
    if (!validDate(start) || !validDate(end)) { setCustomError("Enter valid start and end dates."); return; }
    if (start > end) { setCustomError("Start date must be on or before end date."); return; }
    setCustomError(""); setCustomOpen(false); void selectRange(`${start}/${end}`);
  }

  const controls = (suffix: string) => <>
    <DropdownMenu.Root><DropdownMenu.Trigger className="range-trigger" aria-label="Select date range"><span>Date range</span><strong>{rangeLabel(dateRange)}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align={suffix === "desktop" ? "start" : "center"} sideOffset={8}>{QUICK_RANGES.map(([value, label]) => <DropdownMenu.Item key={value} onSelect={() => void selectRange(value)}>{label}{dateRange === value && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}<DropdownMenu.Separator /><DropdownMenu.Item onSelect={() => setCustomOpen(true)}>Custom...{EXPLICIT_RANGE.test(dateRange) && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item></DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>
    <DropdownMenu.Root><DropdownMenu.Trigger className="range-trigger" aria-label="Select base map"><span>Base map</span><strong>{overrideFamilyId ? family?.label : `Automatic / ${family?.label ?? "Unavailable"}`}</strong><span aria-hidden="true">v</span></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content range-menu" align={suffix === "desktop" ? "start" : "center"} sideOffset={8}><DropdownMenu.Item onSelect={() => setOverrideFamilyId(undefined)}>Automatic{!overrideFamilyId && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item><DropdownMenu.Separator />{config.baseMaps.families.map((candidate) => <DropdownMenu.Item key={candidate.id} onSelect={() => setOverrideFamilyId(candidate.id)}>{candidate.label}{overrideFamilyId === candidate.id && <span aria-label="selected">&#10003;</span>}</DropdownMenu.Item>)}</DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>
    <fieldset className="map-workout-filter"><legend>Workout routes</legend><div className="map-filter-actions"><button type="button" onClick={() => setSelectedWorkoutIds(undefined)}>All</button><button type="button" onClick={() => { cancelRouteHover(); setFocusedWorkoutId(undefined); setSelectedWorkoutIds([]); }}>None</button></div><div className="map-filter-options">{availableWorkouts.length ? <WorkoutRouteList workouts={availableWorkouts} preferences={preferences} sort={sort} visibleIDs={selectedWorkoutIds} highlightedWorkoutId={highlightedWorkoutId} focusedWorkoutId={visibleFocusedWorkoutId} onToggle={toggleWorkout} onFocus={focusWorkout} onHover={requestRouteHover} /> : <p className="map-routes-empty">No workout routes in this range.</p>}</div></fieldset>
    <button type="button" className="secondary map-fit-button" disabled={!selection?.bounds} onClick={() => selection?.bounds && setFitRequest((current) => ({ key: (current?.key ?? 0) + 1, bounds: selection.bounds! }))}>Fit routes</button>
  </>;

  return <main className="map-page">
    <Dialog.Root open={customOpen} onOpenChange={(open) => { setCustomOpen(open); if (!open) setCustomError(""); }}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content custom-range-dialog"><div className="dialog-heading"><div><Dialog.Title>Custom map range</Dialog.Title><Dialog.Description>Choose inclusive calendar dates.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Custom map range">&times;</Dialog.Close></div><form className="custom-range-form" onSubmit={submitCustom} noValidate>{customError && <p className="error-summary" role="alert">{customError}</p>}<div className="field-pair"><div className="field"><label htmlFor="map-start-date">Start date</label><input id="map-start-date" name="startDate" type="date" /></div><div className="field"><label htmlFor="map-end-date">End date</label><input id="map-end-date" name="endDate" type="date" /></div></div><div className="dialog-actions"><Dialog.Close type="button" className="secondary">Cancel</Dialog.Close><button className="primary">Apply range</button></div></form></Dialog.Content></Dialog.Portal></Dialog.Root>
    <aside className="map-sidebar" aria-label="Map controls"><div className="map-controls">{controls("desktop")}</div></aside>
    <section className="map-stage" aria-live="polite">
      {(rangeError || baseMapError || selectionError || selectionPending) && <div className="map-banner" role="status">{rangeError || baseMapError || selectionError || "Updating routes..."}</div>}
      {family && <MapCanvas family={family} preferences={preferences} selection={selection} workouts={availableWorkouts} fitPadding={config.mapFitPaddingPixels} hoveredWorkoutId={highlightedWorkoutId} fitRequest={fitRequest} onHover={requestRouteHover} onBaseMapError={() => setBaseMapError("The public base map could not be loaded. Your private routes remain available.")} />}
      {family && <div className="map-attribution" aria-label={`Active map attribution for ${family.label}`}><span>{family.attribution.text}</span>{family.attribution.links.map((link) => <a key={`${link.label}-${link.url}`} href={link.url} target="_blank" rel="noreferrer">{link.label}</a>)}</div>}
      <Dialog.Root open={sheetOpen} onOpenChange={setSheetOpen}><Dialog.Trigger asChild><button type="button" className="mobile-map-sheet-trigger">Routes and controls</button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="map-sheet-overlay" /><Dialog.Content id="mobile-map-sheet" className="mobile-map-sheet"><div className="mobile-sheet-handle" aria-hidden="true" /><div className="mobile-sheet-heading"><div><Dialog.Title>Routes and controls</Dialog.Title><Dialog.Description>Filter visible workouts and choose how the base map is presented.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Routes and controls">&times;</Dialog.Close></div><div className="map-controls">{controls("mobile")}</div></Dialog.Content></Dialog.Portal></Dialog.Root>
    </section>
  </main>;
}
