import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiError, SESSION_EXPIRED_EVENT, type BaseMapsConfig, type MapSelection, type Preferences, type PublicConfig } from "./api";
import MapPage, { absoluteRouteTileTemplate, buildRawRouteDirectionMarkers, buildRawRouteEndpoints, buildSegmentedRawRoute, formatRoutePopupDetails, formatRoutePopupDistance, pointyDirectionMarkerImage, privateRouteTileUnauthorized, privateRouteTileUnavailable, requestedWorkoutIds, resolveBaseFamily, routeColor, routeColors, routeEndpointOutline, selectionRequest, sortMapWorkouts } from "./MapPage";

const mapInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const popupInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const mapBehavior = vi.hoisted(() => ({ emitInitialStyleLoad: true, emitSetStyleLoad: true, styleLoaded: true }));
const originalGeolocation = Object.getOwnPropertyDescriptor(navigator, "geolocation");
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

vi.mock("maplibre-gl", () => ({
  default: (() => {
    class MockMap {
      layers = new Map<string, unknown>();
      sources = new Map<string, unknown>();
      handlers = new Map<string, (...args: never[]) => void>();
      canvas = Object.assign(document.createElement("canvas"), { tabIndex: 0 });
      addLayer = vi.fn((layer: { id: string }) => { this.layers.set(layer.id, layer); });
      addSource = vi.fn((id: string, source: object) => { this.sources.set(id, { ...source, setTiles: vi.fn(), setData: vi.fn() }); });
      removeLayer = vi.fn((id: string) => { this.layers.delete(id); });
      removeSource = vi.fn((id: string) => { this.sources.delete(id); });
      getLayer = vi.fn((id: string) => this.layers.get(id));
      getSource = vi.fn((id: string) => this.sources.get(id));
      fitBounds = vi.fn();
      setFilter = vi.fn();
      setPaintProperty = vi.fn();
      setLayoutProperty = vi.fn((id: string, property: string, value: unknown) => {
        const layer = this.layers.get(id) as { layout?: Record<string, unknown> } | undefined;
        if (layer) layer.layout = { ...layer.layout, [property]: value };
      });
			moveLayer = vi.fn();
      jumpTo = vi.fn();
      setStyle = vi.fn((style: unknown) => { if (typeof style !== "string") mapBehavior.styleLoaded = true; if (typeof style !== "string" || mapBehavior.emitSetStyleLoad) this.handlers.get("style.load")?.(); });
      getStyle = vi.fn(() => ({}));
      isStyleLoaded = vi.fn(() => mapBehavior.styleLoaded);
      queryRenderedFeatures = vi.fn(() => []);
		project = vi.fn((coordinate: [number, number]) => ({ x: coordinate[0] * 10000, y: -coordinate[1] * 10000 }));
      getCanvas = vi.fn(() => this.canvas);
      addControl = vi.fn();
		images = new Map<string, unknown>();
		hasImage = vi.fn((id: string) => this.images.has(id));
		addImage = vi.fn((id: string, image: unknown) => { this.images.set(id, image); });
      remove = vi.fn();
      on = vi.fn((event: string, layerOrHandler: string | ((...args: never[]) => void), handler?: (...args: never[]) => void) => {
        const callback = typeof layerOrHandler === "function" ? layerOrHandler : handler!;
        const previous = this.handlers.get(event);
        this.handlers.set(event, previous ? ((...args: never[]) => { previous(...args); callback(...args); }) : callback);
        if (event === "style.load" && mapBehavior.emitInitialStyleLoad) callback();
      });
      off = vi.fn();
      constructor(public options: unknown) {
        (options as { container: HTMLElement }).container.append(this.canvas);
        mapInstances.push(this);
      }
    }
    return {
      Map: MockMap,
      NavigationControl: class NavigationControl {},
      Popup: class Popup {
        content?: HTMLElement;
        constructor() { popupInstances.push(this); }
        setLngLat = vi.fn(() => this);
        setDOMContent = vi.fn((content: HTMLElement) => { this.content = content; return this; });
        addTo = vi.fn(() => this);
        remove = vi.fn();
      },
    };
  })(),
}));

const baseMaps: BaseMapsConfig = {
  families: [
    { id: "outdoor", label: "Outdoor", styles: { light: "https://tiles.example.test/outdoor-light.json", dark: "https://tiles.example.test/outdoor-dark.json" }, attribution: { text: "Outdoor map", links: [{ label: "Map data", url: "https://attribution.example.test" }] }, resourceOrigins: ["https://tiles.example.test"] },
    { id: "road", label: "Road", styles: { light: "https://tiles.example.test/road-light.json", dark: "https://tiles.example.test/road-dark.json" }, attribution: { text: "Road map", links: [] }, resourceOrigins: ["https://tiles.example.test"] },
  ],
  fallbackFamilyId: "road",
  workoutTypeMappings: [
    { providerLabel: "Running", normalizedTypeKey: "running", familyId: "outdoor" },
    { providerLabel: "Hiking", normalizedTypeKey: "hiking", familyId: "outdoor" },
    { providerLabel: "Cycling", normalizedTypeKey: "cycling", familyId: "road" },
  ],
};
const config: PublicConfig = { productName: "Workouts Explorer", pollingIntervalSeconds: 30, mapFitPaddingPixels: 48, passwordMinimumLength: 12, pageSizeMaximum: 100, features: { coverageMatcherDiagnostics: false }, baseMaps };
const preferences: Preferences = { theme: "dark", units: "metric", timezone: "America/Denver", firstWeekday: "monday", clockFormat: "24h", workoutColumns: ["date", "type"], pageSize: 25, initialized: true, dateRange: "last30Days" };
const selection: MapSelection = {
  id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(), dataGeneration: 7,
  range: { startDate: "2026-07-10", endDate: "2026-08-08" },
  bounds: { minimumLongitude: -105.3, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40.1 },
  workouts: [
    { id: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", type: { id: "11111111111111111111111111111111", key: "running", name: "Running" }, startedAt: "2026-08-08T12:00:00Z", endedAt: "2026-08-08T13:45:00Z", duration: "6300", localStartDate: "2026-08-08", partialRoute: false, bounds: { minimumLongitude: -105.3, minimumLatitude: 39.9, maximumLongitude: -105.2, maximumLatitude: 40.1 }, distance: { value: "8.25", unit: "km" }, pace: { value: "5", unit: "min/km" }, calories: { value: "500", unit: "kcal" }, heartRate: { value: "120", unit: "count/min" }, elevationGain: { value: "100", unit: "m" }, coverageReadiness: { state: "notProcessed" } },
    { id: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" }, startedAt: "2026-08-01T12:00:00Z", endedAt: "2026-08-01T13:00:00Z", duration: "3600", localStartDate: "2026-08-01", partialRoute: true, bounds: { minimumLongitude: -105.2, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40 }, distance: null, pace: null, calories: { value: "300", unit: "kcal" }, heartRate: { value: "100", unit: "count/min" }, elevationGain: { value: "250", unit: "m" }, coverageReadiness: { state: "pending", reason: "region_not_active" } },
  ],
  routeTileUrl: "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/route-tiles/7/{z}/{x}/{y}.pbf",
};
const diagnostic = {
  id: "D".repeat(32), workoutId: selection.workouts[0].id, routeRevision: 2,
  rulesVersion: "coverage-experimental-v1", samplingVersion: "coverage-sampling-experimental-v1", pathPolicyVersion: "coverage-path-policy-experimental-v7",
  movementMode: "foot", minimumTraversalMeters: 5, outcome: "evaluated",
  counts: { originalPoints: 100, sampledPoints: 50, matchedPoints: 40, ambiguousPoints: 5, unmatchedPoints: 5, rejectedPoints: 0, traversals: 2, portions: 2, uniqueSegments: 2, durationMilliseconds: 1250 },
  generations: [], unavailableRegions: [],
  overlay: { type: "FeatureCollection", features: [
    { type: "Feature", geometry: { type: "LineString", coordinates: [[-105.28, 39.91], [-105.24, 39.96]] }, properties: { portionOrdinal: 0, physicalSegmentId: "E".repeat(32), direction: "forward", regionId: "colorado", generation: 1, traversedMeters: 42.25, evidenceClass: "matched" } },
    { type: "Feature", geometry: { type: "LineString", coordinates: [[-105.24, 39.96], [-105.21, 40.02]] }, properties: { portionOrdinal: 1, physicalSegmentId: "F".repeat(32), direction: "reverse", regionId: "colorado", generation: 1, traversedMeters: 18, evidenceClass: "ambiguous" } },
  ] },
  labels: { overall: "uncertain", segments: [{ portionOrdinal: 0, label: "expected" }] }, createdAt: "2026-08-08T14:00:00Z",
} as const;
const rawRoutePoints = { points: [
  { recordedAt: "2026-01-01T00:00:00Z", longitude: -105.29, latitude: 39.9 },
  { recordedAt: "2026-01-01T00:00:05Z", longitude: -105.28, latitude: 39.91 },
  { recordedAt: "2026-01-01T00:00:10Z", longitude: -105.27, latitude: 39.92 },
  { recordedAt: "2026-01-01T00:05:00Z", longitude: -105.21, latitude: 40.01 },
  { recordedAt: "2026-01-01T00:05:05Z", longitude: -105.2, latitude: 40.02 },
] };
const rawRoute = { type: "Feature", geometry: { type: "MultiLineString", coordinates: [
  [[-105.29, 39.9], [-105.28, 39.91], [-105.27, 39.92]],
  [[-105.21, 40.01], [-105.2, 40.02]],
] }, properties: {} };
const rawRouteEndpoints = { type: "FeatureCollection", features: [
  { type: "Feature", geometry: { type: "Point", coordinates: [-105.29, 39.9] }, properties: { kind: "start" } },
  { type: "Feature", geometry: { type: "Point", coordinates: [-105.2, 40.02] }, properties: { kind: "finish" } },
] };

beforeEach(() => { history.replaceState({}, "", "/map"); mapInstances.splice(0); popupInstances.splice(0); mapBehavior.emitInitialStyleLoad = true; mapBehavior.emitSetStyleLoad = true; mapBehavior.styleLoaded = true; });
afterEach(() => {
  vi.useRealTimers();
  if (originalGeolocation) Object.defineProperty(navigator, "geolocation", originalGeolocation);
  else Object.defineProperty(navigator, "geolocation", { configurable: true, value: undefined });
  if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
});

function json(body: unknown, status = 200) {
  return status === 204 ? new Response(null, { status }) : new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("map contract helpers", () => {
  test("builds enum and explicit selectors and canonicalizes compact workout IDs", () => {
    expect(selectionRequest("last7Days", "America/Denver", ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"])).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver", workoutIds: ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"] });
    expect(selectionRequest("last7Days", "America/Denver", [])).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver", workoutIds: [] });
    expect(selectionRequest("2026-08-01/2026-08-08", "ignored")).toEqual({ startDate: "2026-08-01", endDate: "2026-08-08" });
    expect(requestedWorkoutIds("?workoutId=abcdefabcdefabcdefabcdefabcdefab&workoutId=bad&workoutIds=ABCDEFABCDEFABCDEFABCDEFABCDEFAB")).toEqual(["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"]);
  });

  test("uses a common mapped family only when every visible workout type resolves to it", () => {
    expect(resolveBaseFamily(baseMaps, selection.workouts)).toBe("outdoor");
    expect(resolveBaseFamily(baseMaps, [...selection.workouts, { ...selection.workouts[0], id: "D".repeat(32), type: { id: "3".repeat(32), key: "cycling", name: "Cycling" } }])).toBe("road");
    expect(resolveBaseFamily(baseMaps, [{ ...selection.workouts[0], type: { id: "4".repeat(32), key: "unknown", name: "Unknown" } }])).toBe("road");
    expect(resolveBaseFamily(baseMaps, [selection.workouts[0], { ...selection.workouts[1], type: { ...selection.workouts[1].type, key: "unknown" } }])).toBe("road");
    expect(resolveBaseFamily(baseMaps, [{ ...selection.workouts[0], type: { ...selection.workouts[0].type, name: "RUNNING" } }])).toBe("outdoor");
    expect(routeColors([selection.workouts[0], { ...selection.workouts[0], id: "D".repeat(32) }])).toHaveLength(1);
    expect(absoluteRouteTileTemplate("/api/map/{z}/{x}/{y}.pbf", "https://workouts.example.test")).toBe("https://workouts.example.test/api/map/{z}/{x}/{y}.pbf");
		expect(privateRouteTileUnauthorized({ sourceId: "private-workout-routes", error: { status: 401 } })).toBe(true);
		expect(privateRouteTileUnauthorized({ error: { status: 401, url: `${window.location.origin}/api/map-selections/${"A".repeat(32)}/route-tiles/7/12/654/1583.pbf` } })).toBe(true);
		expect(privateRouteTileUnauthorized({ error: { status: 401, url: "https://tiles.example.test/12/654/1583.pbf" } })).toBe(false);
		expect(privateRouteTileUnavailable({ sourceId: "private-workout-routes", error: { status: 404, url: `${window.location.origin}/api/map-selections/${selection.id}/route-tiles/7/12/654/1583.pbf` } }, selection.routeTileUrl)).toBe(true);
		expect(privateRouteTileUnavailable({ error: { status: 404, url: `${window.location.origin}/api/map-selections/${selection.id}/route-tiles/7/12/654/1583.pbf` } }, selection.routeTileUrl)).toBe(true);
		expect(privateRouteTileUnavailable({ sourceId: "private-workout-routes", error: { status: 404, url: `${window.location.origin}/api/map-selections/${"F".repeat(32)}/route-tiles/7/12/654/1583.pbf` } }, selection.routeTileUrl)).toBe(false);
    const imperial = { ...preferences, clockFormat: "12h", units: "imperial", timezone: "America/Denver" } as const;
    expect(formatRoutePopupDetails("2026-04-20T22:30:00Z", "2026-04-21T01:45:00Z", imperial)).toEqual({ date: "4/20/2026", timeRange: "4:30p - 7:45p" });
    expect(formatRoutePopupDistance({ ...selection.workouts[0], distance: { value: "13.276", unit: "km" } }, "imperial")).toBe("8.25 mi");
    expect(routeColor("walk", "Outdoor Walk")).toBe("#43d5e5");
    expect(routeColor("hike", "Hiking")).toBe("#8ed081");
    expect(routeColor("ride", "Outdoor Cycling")).toBe("#69aef5");
    expect(sortMapWorkouts(selection.workouts, { field: "type", direction: "asc" }).map((workout) => workout.type.name)).toEqual(["Hiking", "Running"]);
    expect(sortMapWorkouts(selection.workouts, { field: "duration", direction: "asc" }).map((workout) => workout.id)).toEqual([selection.workouts[1].id, selection.workouts[0].id]);
    expect(sortMapWorkouts(selection.workouts, { field: "distance", direction: "desc" }).map((workout) => workout.id)).toEqual([selection.workouts[0].id, selection.workouts[1].id]);
    expect(sortMapWorkouts(selection.workouts, { field: "elevationGain", direction: "desc" }).map((workout) => workout.id)).toEqual([selection.workouts[1].id, selection.workouts[0].id]);
		const calorieTie = [
			{ ...selection.workouts[0], id: "A".repeat(32), startedAt: "2025-05-01T12:00:00Z", calories: { value: "500", unit: "kcal" } },
			{ ...selection.workouts[0], id: "C".repeat(32), startedAt: "2025-07-24T12:00:00Z", calories: { value: "500", unit: "kcal" } },
			{ ...selection.workouts[0], id: "B".repeat(32), startedAt: "2025-06-25T12:00:00Z", calories: { value: "500", unit: "kcal" } },
		];
		expect(sortMapWorkouts(calorieTie, { field: "calories", direction: "desc" }).map((workout) => workout.startedAt.slice(0, 10))).toEqual(["2025-07-24", "2025-06-25", "2025-05-01"]);
  });

	test("spaces smoothed raw-route direction markers in projected pixels", () => {
		const route = buildSegmentedRawRoute(Array.from({ length: 21 }, (_, i) => ({ recordedAt: new Date(i * 1000).toISOString(), longitude: i, latitude: 0 })))!;
		const lowZoom = buildRawRouteDirectionMarkers(route, ([x, y]) => ({ x: x * 5, y: -y * 5 }), 40, 3);
		const highZoom = buildRawRouteDirectionMarkers(route, ([x, y]) => ({ x: x * 20, y: -y * 20 }), 40, 3);
		expect(lowZoom.features.length).toBeGreaterThan(0);
		expect(highZoom.features.length).toBeGreaterThan(lowZoom.features.length);
		for (const feature of highZoom.features) expect(feature.properties.bearing).toBeCloseTo(90);
		const marker = pointyDirectionMarkerImage();
		expect([marker.width, marker.height]).toEqual([11, 15]);
		expect(marker.data[(1*marker.width+5)*4+3]).toBe(255);
		expect(marker.data[(13*marker.width)*4+3]).toBe(0);
		expect(marker.data[(13*marker.width+5)*4+3]).toBe(255);
	});
});

describe("MapPage", () => {
  test("keeps timestamp-separated raw route components disjoint", () => {
    expect(buildSegmentedRawRoute(rawRoutePoints.points)).toEqual(rawRoute);
		expect(buildRawRouteEndpoints(rawRoutePoints.points)).toEqual(rawRouteEndpoints);
		expect(routeEndpointOutline("dark")).toBe("#334155");
		expect(routeEndpointOutline("light")).toBe("#d1d5db");
  });

  test("runs and reviews an enabled focused diagnostic without recreating the map", async () => {
    const enabledConfig: PublicConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
    const requests: Array<{ path: string; method: string; body?: unknown; csrf: string | null }> = [];
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      requests.push({ path, method, body: init?.body ? JSON.parse(String(init.body)) : undefined, csrf: new Headers(init?.headers).get("X-CSRF-Token") });
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
      if (path === `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs` && method === "POST") return Promise.resolve(json(diagnostic, 201));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
      if (path === `/api/coverage-diagnostic-runs/${diagnostic.id}/labels` && method === "PATCH") return Promise.resolve(json({ overall: "correct", segments: diagnostic.labels.segments }));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const view = render(<MapPage config={enabledConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const coverage = screen.getByRole("button", { name: "Coverage" });
    expect(coverage).toBeDisabled();
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
		expect(new URLSearchParams(location.search).get("workoutId")).toBe(selection.workouts[0].id);
    await waitFor(() => expect(coverage).toBeEnabled());
		const map = mapInstances.at(-1)!;
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", { type: "geojson", data: rawRoute }));
		expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-direction", { type: "geojson", data: expect.objectContaining({ type: "FeatureCollection" }) });
		expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-direction")?.[0].paint["icon-opacity"]).toBe(0.95);
		expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 0);
		expect(map.sources.has("private-workout-route-endpoints")).toBe(true);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 1);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-finish", "text-opacity", 1);
		const rawSourceAdds = map.addSource.mock.calls.filter((call: any[]) => call[0] === "coverage-diagnostic-raw-route").length;
		const preloadedRawSource = map.sources.get("coverage-diagnostic-raw-route");
		preloadedRawSource.setData.mockClear();
		map.setPaintProperty.mockClear();
		map.setLayoutProperty.mockClear();
    await userEvent.click(coverage);
    const review = await screen.findByRole("region", { name: "Coverage diagnostic review" });
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute("aria-keyshortcuts", "Space");
    expect(review).toHaveTextContent("Matched40");
    expect(review).toHaveTextContent("Ambiguous5");
    expect(review).toHaveTextContent("Unmatched5");
    expect(review).toHaveTextContent("Runtime1.25 s");
    expect(review).toHaveTextContent("Owner diagnostic / v7");
    expect(review.querySelector(".coverage-diagnostic-save")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Uncertain" })).toHaveAttribute("aria-pressed", "true");
    expect(requests).toContainEqual({ path: `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs`, method: "POST", body: {}, csrf: "csrf-map" });
    expect(mapInstances).toHaveLength(1);
    expect(document.activeElement).toBe(map.canvas);
    expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-overlay", expect.objectContaining({ type: "geojson" }));
    expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", { type: "geojson", data: rawRoute });
		expect(map.addSource.mock.calls.filter((call: any[]) => call[0] === "coverage-diagnostic-raw-route")).toHaveLength(rawSourceAdds);
		expect(preloadedRawSource.setData).not.toHaveBeenCalled();
		expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-raw-route")?.[0].paint["line-opacity-transition"]).toEqual({ duration: 150, delay: 0 });
		expect(map.addImage).toHaveBeenCalledWith("coverage-diagnostic-direction-triangle", expect.objectContaining({ width: 11, height: 15 }));
		expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-direction")?.[0]).toMatchObject({ type: "symbol", layout: { "icon-image": "coverage-diagnostic-direction-triangle", "icon-rotate": ["get", "bearing"] } });
		const directionSource = map.sources.get("coverage-diagnostic-direction");
		directionSource.setData.mockClear();
		act(() => map.handlers.get("moveend")?.());
		expect(directionSource.setData).toHaveBeenCalledWith(expect.objectContaining({ type: "FeatureCollection" }));
		expect(map.addSource).toHaveBeenCalledWith("private-workout-route-endpoints", { type: "geojson", data: rawRouteEndpoints });
		const startLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-start")?.[0];
		const finishLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-finish")?.[0];
		expect(startLayer.paint["circle-opacity-transition"]).toEqual({ duration: 150, delay: 0 });
		expect(startLayer.paint["circle-stroke-opacity-transition"]).toEqual({ duration: 150, delay: 0 });
		expect(finishLayer.paint["text-opacity-transition"]).toEqual({ duration: 150, delay: 0 });
		expect(startLayer.paint).toMatchObject({ "circle-color": "#20a464", "circle-radius": 22 / Math.sqrt(Math.PI) * 0.9 * 0.97 * 0.95, "circle-stroke-color": "#334155" });
		expect(finishLayer.layout["text-field"]).toBe("■");
		expect(finishLayer.paint["text-halo-color"]).toBe("#334155");
		expect(map.moveLayer).toHaveBeenCalledWith("private-workout-route-start");
		expect(map.moveLayer).toHaveBeenCalledWith("private-workout-route-finish");
		expect(map.moveLayer.mock.calls.at(-1)).toEqual(["private-workout-route-start"]);
		const hitLayerIndex = map.addLayer.mock.calls.findIndex((call: any[]) => call[0].id === "coverage-diagnostic-hit-target");
		expect(map.moveLayer.mock.invocationCallOrder.at(-1)).toBeGreaterThan(map.addLayer.mock.invocationCallOrder[hitLayerIndex]);
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-raw-route")?.[0].paint["line-color"]).toBe("#c026ff");
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-matched")?.[0].paint["line-color"]).toBe("#19c7c9");
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-ambiguous")?.[0].paint["line-color"]).toBe("#e7a83d");
		await waitFor(() => expect(map.setLayoutProperty).toHaveBeenCalledWith("private-workout-routes", "visibility", "none"));
		const rawVisibleIndex = map.setPaintProperty.mock.calls.findIndex((call: any[]) => call[0] === "coverage-diagnostic-raw-route" && call[1] === "line-opacity" && call[2] === 1);
		const vectorsHiddenIndex = map.setLayoutProperty.mock.calls.findIndex((call: any[]) => call[0] === "private-workout-routes" && call[2] === "none");
		expect(map.setPaintProperty.mock.invocationCallOrder[rawVisibleIndex]).toBeLessThan(map.setLayoutProperty.mock.invocationCallOrder[vectorsHiddenIndex]);
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalled());
    expect(document.activeElement).toBe(map.canvas);
    map.setPaintProperty.mockClear();
    const requestCount = requests.length;
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 0));
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 0);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-stroke-opacity", 0);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-finish", "text-opacity", 0);
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 1));
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 1);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-finish", "text-opacity", 1);
    expect(requests).toHaveLength(requestCount);
    map.setPaintProperty.mockClear();
    fireEvent.keyDown(coverage, { key: " ", code: "Space" });
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 0);
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 0));
    act(() => { window.dispatchEvent(new Event("blur")); });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 1));
    expect(requests).toHaveLength(requestCount);
    map.handlers.get("style.load")?.();
    expect(map.sources.get("coverage-diagnostic-overlay").setData).toHaveBeenCalledWith(diagnostic.overlay);
		expect(map.sources.get("coverage-diagnostic-raw-route").setData).not.toHaveBeenCalled();
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 20, y: 20 } }));
    expect(map.queryRenderedFeatures).toHaveBeenCalledWith({ x: 20, y: 20 }, { layers: ["coverage-diagnostic-ambiguous", "coverage-diagnostic-matched"] });
    expect(map.setFilter).not.toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 0]);
    await waitFor(() => expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 0]));
    expect(screen.getByText("Select a traversal on the map to review it.")).toBeInTheDocument();
    act(() => map.handlers.get("click")?.({ point: { x: 20, y: 20 } }));
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.getByText(/42.3 m traveled/)).toHaveTextContent("42.3 m traveled / matched / forward");
    await userEvent.click(screen.getByRole("button", { name: "Copy OSM segment ID" }));
		expect(writeText).toHaveBeenCalledWith("E".repeat(32));
		expect(await screen.findByRole("button", { name: "OSM segment ID copied" })).toBeInTheDocument();
		expect(document.activeElement).toBe(map.canvas);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 1 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 24, y: 24 } }));
    expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 0]);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 20, y: 20 } }));
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(map.setFilter).not.toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 1]);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 1 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 24, y: 24 } }));
    await waitFor(() => expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 1]));
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.queryByText("Traversal 2")).not.toBeInTheDocument();
    act(() => map.handlers.get("click")?.({ point: { x: 24, y: 24 } }));
    expect(screen.getByText("Traversal 2")).toBeInTheDocument();
    expect(screen.getByText(/18.0 m traveled/)).toHaveTextContent("18.0 m traveled / ambiguous / reverse");
    expect(screen.getByText(`OSM segment ${"F".repeat(32)}`)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy OSM segment ID" }));
    expect(writeText).toHaveBeenLastCalledWith("F".repeat(32));
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("click")?.({ point: { x: 20, y: 20 } }));
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.getByText(`OSM segment ${"E".repeat(32)}`)).toBeInTheDocument();
    act(() => map.handlers.get("mouseleave")?.());
    expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", ["==", ["get", "portionOrdinal"], 0]);
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Correct" }));
    await waitFor(() => expect(requests).toContainEqual({ path: `/api/coverage-diagnostic-runs/${diagnostic.id}/labels`, method: "PATCH", body: { overall: "correct", segments: diagnostic.labels.segments }, csrf: "csrf-map" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Correct" }));
    await userEvent.click(screen.getByRole("button", { name: "Routes" }));
    expect(screen.queryByRole("region", { name: "Coverage diagnostic review" })).not.toBeInTheDocument();
    expect(map.setLayoutProperty).toHaveBeenCalledWith("private-workout-routes", "visibility", "visible");
    await userEvent.click(coverage);
    expect(await screen.findByRole("region", { name: "Coverage diagnostic review" })).toHaveTextContent("Coverage review");
    expect(document.activeElement).toBe(map.canvas);
    expect(requests.filter((request) => request.path.endsWith("coverage-diagnostic-runs"))).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Rerun" }));
    await waitFor(() => expect(requests.filter((request) => request.path.endsWith("coverage-diagnostic-runs"))).toHaveLength(2));
    expect(mapInstances).toHaveLength(1);
    view.unmount();
    expect(map.removeLayer.mock.invocationCallOrder.at(-1)).toBeLessThan(map.removeSource.mock.invocationCallOrder.at(-1));
  });

	test("shows required inactive OSM regions instead of zero-match review controls", async () => {
		const enabledConfig: PublicConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
		const unavailable = {
			...diagnostic, outcome: "no_evidence", unavailableRegions: [{ regionId: "geofabrik:new-york", displayName: "New York" }],
			counts: { ...diagnostic.counts, matchedPoints: 0, ambiguousPoints: 0, traversals: 0, portions: 0, uniqueSegments: 0 },
			overlay: { type: "FeatureCollection", features: [] }, labels: { segments: [] },
		} as const;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (path.endsWith("/coverage-diagnostic-runs") && method === "POST") return Promise.resolve(json(unavailable, 201));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={enabledConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
		await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
		const review = await screen.findByRole("region", { name: "Coverage diagnostic review" });
		expect(within(review).getByRole("alert")).toHaveTextContent("Coverage is unavailable because OSM data is not loaded for New York (geofabrik:new-york).");
		expect(within(review).getByRole("button", { name: "Retry" })).toBeInTheDocument();
		expect(within(review).queryByRole("button", { name: "Fit" })).not.toBeInTheDocument();
		expect(within(review).queryByRole("button", { name: "Rerun" })).not.toBeInTheDocument();
		expect(within(review).queryByText("Overall result")).not.toBeInTheDocument();
		expect(within(review).queryByText("Matched")).not.toBeInTheDocument();
	});

  test("aborts and ignores a late diagnostic when focus changes", async () => {
    const enabledConfig: PublicConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
    let resolveDiagnostic!: (response: Response) => void;
    const diagnosticRequest: { signal?: AbortSignal } = {};
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
      if (path.endsWith("/coverage-diagnostic-runs") && method === "POST") { diagnosticRequest.signal = init?.signal ?? undefined; return new Promise((resolve) => { resolveDiagnostic = resolve; }); }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={enabledConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const preparing = await screen.findByRole("status", { name: "" });
    expect(preparing).toHaveClass("coverage-diagnostic-preparing");
    expect(preparing).toHaveAttribute("aria-busy", "true");
    expect(preparing).toHaveTextContent("Preparing diagnostic overlay...");
		const map = mapInstances.at(-1)!;
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", { type: "geojson", data: rawRoute }));
		expect(map.addSource).toHaveBeenCalledWith("private-workout-route-endpoints", { type: "geojson", data: rawRouteEndpoints });
		expect(map.sources.has("coverage-diagnostic-overlay")).toBe(false);
		expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 1);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 1);
    await userEvent.click(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
    expect(diagnosticRequest.signal?.aborted).toBe(true);
    resolveDiagnostic(json(diagnostic, 201));
    await act(async () => { await Promise.resolve(); });
    expect(screen.queryByRole("region", { name: "Coverage diagnostic review" })).not.toBeInTheDocument();
    expect([...mapInstances.at(-1)!.sources.keys()]).not.toContain("coverage-diagnostic-overlay");
    expect(mapInstances).toHaveLength(1);
  });

  test("creates and deletes a private selection, restores private layers, and renders active attribution", async () => {
    const requests: Array<{ path: string; method: string; body?: unknown; csrf: string | null }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      requests.push({ path, method, body: init?.body ? JSON.parse(String(init.body)) : undefined, csrf: new Headers(init?.headers).get("X-CSRF-Token") });
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json({ ...selection, id: selection.id.toLowerCase(), workouts: selection.workouts.map((workout) => ({ ...workout, id: workout.id.toLowerCase() })) }));
      if (path === `/api/map-selections/${selection.id}` && method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const view = render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select date range" })).toHaveClass("range-trigger");
    expect(screen.getByRole("button", { name: "Select base map" })).toHaveClass("range-trigger");
    expect(screen.queryByText("Route atlas")).not.toBeInTheDocument();
    expect(screen.queryByText("Visible routes")).not.toBeInTheDocument();
    expect(screen.queryByText(/Coverage \/ Milestone/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Routes" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "Coverage" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Select all workout routes" })).toBeChecked();
    expect(screen.queryByText("Coverage map data is pending")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Active map attribution for Outdoor")).toHaveTextContent("Outdoor mapMap data");
    expect(screen.getByRole("link", { name: "Map data" })).toHaveAttribute("href", "https://attribution.example.test");
    expect(requests[0]).toEqual({ path: "/api/map-selections", method: "POST", body: { dateRangeEnum: "last30Days", tz: "America/Denver" }, csrf: "csrf-map" });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] }));
    expect(map.addLayer.mock.calls.some((call: any[]) => call[0]["source-layer"] === "routes" && call[0].layout["line-sort-key"][1] === "sort_order")).toBe(true);
    expect([...map.sources.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    expect([...map.layers.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    const routeLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-routes")?.[0];
    expect(routeLayer.paint["line-color"].filter((value: unknown) => value === "running")).toHaveLength(1);
    expect(map.addLayer.mock.calls.some((call: any[]) => call[0].paint["line-color"] === "#c026ff" && call[0].paint["line-opacity"] === 1)).toBe(true);
    const markerLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-markers")?.[0];
    expect(markerLayer).toMatchObject({ type: "circle", "source-layer": "routes", filter: ["==", ["geometry-type"], "Point"] });
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-marker-hover")?.[0].paint["circle-color"]).toBe("#c026ff");
    expect(map.fitBounds).toHaveBeenCalledWith([[-105.3, 39.8], [-105.1, 40.1]], { padding: 48, duration: 350 });
    view.unmount();
    await waitFor(() => expect(requests).toContainEqual({ path: `/api/map-selections/${selection.id}`, method: "DELETE", body: undefined, csrf: "csrf-map" }));
    expect(map.remove).toHaveBeenCalled();
  });

  test("keeps a family override visit-local and exposes the accessible mobile sheet", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Select date range" }));
    expect(screen.getByRole("separator")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Last 30 days/ })).toHaveTextContent("✓");
    expect(screen.queryByText("Selected")).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: /^Custom/ }));
    expect(await screen.findByRole("dialog", { name: "Custom date range" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveClass("range-dialog-action");
    expect(screen.getByRole("button", { name: "Apply" })).toHaveClass("range-dialog-action");
    await user.type(screen.getByLabelText("Start date"), "2026-03-15");
    await user.tab();
    expect(screen.getByLabelText("End date")).toHaveValue("2026-03-15");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Select base map" }));
    expect(screen.getByRole("separator")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Automatic/ })).toHaveTextContent("✓");
    await user.click(screen.getByRole("menuitem", { name: /^Road/ }));
    expect(screen.getByLabelText("Active map attribution for Road")).toHaveTextContent("Road map");
    expect(mapInstances.at(-1)?.setStyle).toHaveBeenLastCalledWith("https://tiles.example.test/road-dark.json", expect.objectContaining({ transformStyle: expect.any(Function) }));
    const transform = mapInstances.at(-1)?.setStyle.mock.calls.at(-1)?.[1].transformStyle;
    const transformed = transform({ version: 8, sources: { "private-workout-routes": { type: "vector", tiles: ["private"] } }, layers: [{ id: "private-workout-routes", type: "line" }] }, { version: 8, sources: { base: { type: "vector" } }, layers: [{ id: "base", type: "line" }] });
    expect(transformed.sources).toHaveProperty("private-workout-routes");
    expect(transformed.layers.map((layer: { id: string }) => layer.id)).toEqual(["base", "private-workout-routes"]);
    const trigger = screen.getByRole("button", { name: "Routes and controls" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("dialog", { name: "Routes and controls" })).toBeVisible();
    expect(screen.getByRole("dialog", { name: "Routes and controls" })).toContainElement(screen.getByRole("button", { name: "Select base map" }));
  });

  test("formats an explicit date range consistently in the trigger", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="2026-03-06/2026-03-20" onDateRangeSelected={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Select date range" })).toHaveTextContent("Mar 6, 2026 to Mar 20, 2026");
  });

  test("replaces the private selection when a workout route is filtered", async () => {
    const posted: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        const filtered = Array.isArray(body.workoutIds) ? selection.workouts.filter((workout) => body.workoutIds.includes(workout.id)) : selection.workouts;
        const id = filtered.length === 1 ? "D".repeat(32) : selection.id;
        return Promise.resolve(json({ ...selection, id, routeTileUrl: selection.routeTileUrl.replace(selection.id, id), workouts: filtered }));
      }
			if (path === `/api/workouts/${selection.workouts[1].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox", { name: /Running/ }));
    await waitFor(() => expect(posted).toContainEqual({ dateRangeEnum: "last30Days", tz: "America/Denver", workoutIds: [selection.workouts[1].id] }));
    expect(mapInstances).toHaveLength(1);
		await waitFor(() => expect(mapInstances[0].removeSource).toHaveBeenCalledWith("private-workout-routes"));
		expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`] });
		await waitFor(() => expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-route-endpoints", { type: "geojson", data: rawRouteEndpoints }));
    expect(mapInstances[0].remove).not.toHaveBeenCalled();
  });

  test("fits and highlights a requested route without fitting the merged checked selection", async () => {
		const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
    const requested = selection.workouts[0];
    const previouslySelected = selection.workouts[1];
    history.replaceState({}, "", `/map?workoutId=${requested.id}`);
    const posted: Array<{ workoutIds?: string[] }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body)); posted.push(body);
        const workouts = body.workoutIds === undefined ? selection.workouts : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function ControlledMap() {
      const [workoutIds, setWorkoutIds] = useState<string[] | undefined>([previouslySelected.id]);
      const [available, setAvailable] = useState(selection.workouts);
      return <MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()}
        persistedWorkoutIds={workoutIds} onWorkoutSelectionChange={setWorkoutIds}
        persistedAvailableWorkouts={available} onAvailableWorkoutsChange={setAvailable} />;
    }
    render(<ControlledMap />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(posted).toContainEqual(expect.objectContaining({ workoutIds: [requested.id, previouslySelected.id].sort() })));
    await waitFor(() => expect(map.fitBounds).toHaveBeenLastCalledWith(
      [[requested.bounds.minimumLongitude, requested.bounds.minimumLatitude], [requested.bounds.maximumLongitude, requested.bounds.maximumLatitude]],
      { padding: 48, duration: 350 },
    ));
    expect(map.fitBounds).toHaveBeenCalledTimes(1);
    const highlightLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-hover")?.[0];
    expect(highlightLayer.filter).toEqual(["==", ["get", "workout_id"], requested.id]);
    expect(highlightLayer.paint["line-color"]).toBe("#c026ff");
    expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).toHaveClass("is-hovered", "is-focused");
		expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", inline: "nearest" });
		scrollIntoView.mockClear();
		await userEvent.click(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
		expect(scrollIntoView).not.toHaveBeenCalled();
    history.replaceState({}, "", "/map");
  });

	test("restores a persisted focused route with a one-route fit after remount", async () => {
		const requested = selection.workouts[0];
		const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
		history.replaceState({}, "", "/map");
		const posted: Array<{ workoutIds?: string[] }> = [];
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				const body = JSON.parse(String(init?.body)); posted.push(body);
				const workouts = body.workoutIds === undefined ? selection.workouts : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
				return Promise.resolve(json({ ...selection, bounds: requested.bounds, workouts }));
			}
			if (path === `/api/workouts/${requested.id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		function ControlledRemount() {
			const [workoutIds, setWorkoutIds] = useState<string[] | undefined>([requested.id]);
			const [available, setAvailable] = useState<typeof selection.workouts>([]);
			const [focused, setFocused] = useState<string | undefined>(requested.id);
			return <MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()}
				persistedWorkoutIds={workoutIds} onWorkoutSelectionChange={setWorkoutIds}
				persistedAvailableWorkouts={available} onAvailableWorkoutsChange={setAvailable}
				persistedFocusedWorkoutId={focused} onFocusedWorkoutChange={setFocused} />;
		}
		render(<ControlledRemount />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).not.toBeChecked();
		expect(posted[0].workoutIds).toBeUndefined();
		expect(posted.at(-1)?.workoutIds).toEqual([requested.id]);
		const map = mapInstances.at(-1)!;
		await waitFor(() => expect(map.fitBounds).toHaveBeenCalledWith(
			[[requested.bounds.minimumLongitude, requested.bounds.minimumLatitude], [requested.bounds.maximumLongitude, requested.bounds.maximumLatitude]],
			{ padding: 48, duration: 350 },
		));
		expect(map.fitBounds).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).toHaveClass("is-hovered", "is-focused");
		expect(scrollIntoView).toHaveBeenCalledTimes(1);
		expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", inline: "nearest" });
	});

  test("delays list highlighting and clears it immediately when the route is hidden", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        const workouts = body.workoutIds === undefined ? selection.workouts : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const checkbox = await screen.findByRole("checkbox", { name: /Show Running/ });
    const row = screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")!;
    vi.useFakeTimers();
    fireEvent.pointerEnter(row);
    act(() => vi.advanceTimersByTime(249));
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(1));
    expect(row).toHaveClass("is-hovered");
    fireEvent.click(checkbox);
    expect(checkbox).not.toBeChecked();
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    fireEvent.pointerLeave(row);
    fireEvent.pointerEnter(row);
    act(() => vi.advanceTimersByTime(250));
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    fireEvent.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(row).toHaveClass("is-focused");
    const hikingRow = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")!;
    fireEvent.pointerEnter(hikingRow);
    act(() => vi.advanceTimersByTime(250));
    expect(hikingRow).toHaveClass("is-hovered");
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    fireEvent.pointerLeave(hikingRow);
    expect(hikingRow).not.toHaveClass("is-hovered", "is-focused");
    expect(row).toHaveClass("is-hovered", "is-focused");
  });

  test("fits a clicked workout without making its checkbox the item action", async () => {
    const mixedConfig = { ...config, baseMaps: { ...baseMaps, workoutTypeMappings: baseMaps.workoutTypeMappings.map((mapping) => mapping.normalizedTypeKey === "hiking" ? { ...mapping, familyId: "road" } : mapping) } };
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={mixedConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const checkbox = await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledWith([[-105.3, 39.8], [-105.1, 40.1]], { padding: 48, duration: 350 }));
    const fitsBeforeToggle = map.fitBounds.mock.calls.length;
    const user = userEvent.setup();
    await user.click(checkbox);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeToggle);
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    expect(map.fitBounds).toHaveBeenLastCalledWith([[-105.3, 39.9], [-105.2, 40.1]], { padding: 48, duration: 350 });
    expect(map.setStyle).toHaveBeenLastCalledWith("https://tiles.example.test/outdoor-dark.json", expect.objectContaining({ transformStyle: expect.any(Function) }));
  });

  test("replaces a stale empty source when a route is re-enabled", async () => {
    const posted: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body)); posted.push(body);
        const workouts = body.workoutIds === undefined ? selection.workouts : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        const id = workouts.length === 0 ? "E".repeat(32) : workouts.length === 1 ? "F".repeat(32) : selection.id;
        return Promise.resolve(json({ ...selection, id, routeTileUrl: selection.routeTileUrl.replace(selection.id, id), bounds: workouts.length ? workouts[0].bounds : null, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup(); const map = mapInstances.at(-1)!;
    await user.click(screen.getByRole("checkbox", { name: "Select all workout routes" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Fit routes" })).toBeDisabled());
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    await waitFor(() => expect(posted).toContainEqual({ dateRangeEnum: "last30Days", tz: "America/Denver", workoutIds: [selection.workouts[0].id] }));
    await waitFor(() => expect(map.removeSource).toHaveBeenCalledWith("private-workout-routes"));
    expect(map.addSource).toHaveBeenLastCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "F".repeat(32))}`] });
    expect(mapInstances).toHaveLength(1);
    expect(map.remove).not.toHaveBeenCalled();
  });

  test("uses the route-aligned bulk checkbox to toggle all routes", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const method = init?.method ?? "GET";
      if (String(input) === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        const workouts = body.workoutIds === undefined ? selection.workouts : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, bounds: workouts.length ? selection.bounds : null, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${String(input)}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const user = userEvent.setup();
    const bulk = await screen.findByRole("checkbox", { name: "Select all workout routes" });
    expect(bulk).toBeChecked();
    expect(bulk.closest(".map-route-toolbar")).toBeInTheDocument();
    expect(bulk.closest(".map-route-toggle")).toBeInTheDocument();
    expect(screen.queryByText("All routes")).not.toBeInTheDocument();

    await user.click(bulk);
    await waitFor(() => expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => !(checkbox as HTMLInputElement).checked)).toBe(true));
    expect(bulk).not.toBeChecked();

    await user.click(bulk);
    await waitFor(() => expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true));
    expect(bulk).toBeChecked();

    await user.click(screen.getByRole("checkbox", { name: /Show Running/ }));
    expect(bulk).not.toBeChecked();
    await user.click(bulk);
    expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true);
    expect(bulk).toBeChecked();
  });

  test("delays the two-line route popup and synchronizes the workout highlight", async () => {
		const requests: string[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET"; requests.push(`${method} ${path}`);
			if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path.endsWith("/route/points") && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    map.queryRenderedFeatures.mockReturnValue([{ properties: { workout_id: selection.workouts[0].id, sort_order: 1 } }]);
    vi.useFakeTimers();
    act(() => map.handlers.get("mousemove")?.({ point: { x: 50, y: 50 }, lngLat: { lng: -105.2, lat: 40 } }));
    const row = screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li");
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(249));
    act(() => map.handlers.get("mousemove")?.({ point: { x: 51, y: 51 }, lngLat: { lng: -105.2, lat: 40 } }));
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(1));
    expect(row).toHaveClass("is-hovered");
		await act(async () => { await Promise.resolve(); await Promise.resolve(); });
		expect(requests).toContain(`GET /api/workouts/${selection.workouts[0].id}/route/points`);
		expect(map.getLayer("coverage-diagnostic-direction")).toBeTruthy();
		expect(map.getLayer("private-workout-route-start")).toBeTruthy();
		expect(map.getLayer("private-workout-route-finish")).toBeTruthy();
		expect(map.moveLayer.mock.calls.at(-1)).toEqual(["private-workout-route-start"]);
    act(() => vi.advanceTimersByTime(499));
    expect(popupInstances).toHaveLength(0);
    act(() => vi.advanceTimersByTime(1));
    expect(popupInstances).toHaveLength(1);
    expect(popupInstances[0].content).toHaveTextContent("Running8.25 km");
    expect(popupInstances[0].content.children).toHaveLength(4);
    expect(popupInstances[0].content).toHaveTextContent("8/08/20266:00 - 7:45");
    vi.useRealTimers();
  });

	test("immediately focuses a map-clicked route and centers its list item", async () => {
		const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
		const requests: string[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET"; requests.push(`${method} ${path}`);
			if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path.endsWith("/route/points") && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		const hikingCheckbox = await screen.findByRole("checkbox", { name: /Show Hiking/ });
		const map = mapInstances.at(-1)!;
		map.fitBounds.mockClear(); scrollIntoView.mockClear();
		map.queryRenderedFeatures.mockReturnValue([{ properties: { workout_id: selection.workouts[1].id, sort_order: 2 } }]);
		act(() => map.handlers.get("click")?.({ point: { x: 80, y: 60 } }));
		const row = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")!;
		await waitFor(() => expect(row).toHaveClass("is-hovered", "is-focused"));
		expect(hikingCheckbox).toBeChecked();
		expect(scrollIntoView).toHaveBeenCalledTimes(1);
		expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", inline: "nearest" });
		expect(map.fitBounds).toHaveBeenCalledWith(
			[[selection.workouts[1].bounds.minimumLongitude, selection.workouts[1].bounds.minimumLatitude], [selection.workouts[1].bounds.maximumLongitude, selection.workouts[1].bounds.maximumLatitude]],
			{ padding: 48, duration: 350 },
		);
		await waitFor(() => expect(requests).toContain(`GET /api/workouts/${selection.workouts[1].id}/route/points`));
		expect(map.getLayer("coverage-diagnostic-direction")).toBeTruthy();
		expect(map.getLayer("private-workout-route-start")).toBeTruthy();
		expect(map.getLayer("private-workout-route-finish")).toBeTruthy();
		expect(map.moveLayer.mock.calls.at(-1)).toEqual(["private-workout-route-start"]);
	});

  test("shows safe empty and error states without contacting a map provider", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ ...selection, workouts: [], bounds: null }));
    const view = render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    expect(await screen.findByText("No workout routes in this range.")).toBeInTheDocument();
    expect(mapInstances).toHaveLength(1);
    mapInstances[0].handlers.get("load")?.();
    expect(mapInstances[0].fitBounds).toHaveBeenCalledWith([[-125, 24], [-66.5, 49.5]], { padding: 48, duration: 0 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    view.unmount();

    fetchMock.mockRejectedValueOnce(new Error("private provider detail"));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    expect(await screen.findByText("Routes could not be prepared for this map.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss route preparation error" }));
    expect(screen.queryByText("Routes could not be prepared for this map.")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private provider detail");
  });

  test("retries transient map selection failures while retaining the updating state", async () => {
    let attempts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") {
        attempts++;
        return attempts < 3 ? Promise.reject(new ApiError(503)) : Promise.resolve(json(selection));
      }
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    expect(screen.getByText("Updating routes...")).toBeInTheDocument();
    expect(await screen.findByRole("checkbox", { name: /Show Running/ }, { timeout: 4000 })).toBeInTheDocument();
    expect(attempts).toBe(3);
    expect(screen.queryByText("Routes could not be prepared for this map.")).not.toBeInTheDocument();
  });

  test("centers an empty initial map on an available browser location", async () => {
    Object.defineProperty(navigator, "geolocation", { configurable: true, value: { getCurrentPosition: vi.fn((success: PositionCallback) => success({ coords: { longitude: -104.99, latitude: 39.74 } } as GeolocationPosition)) } });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ ...selection, workouts: [], bounds: null }));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByText("No workout routes in this range.");
    const map = mapInstances.at(-1)!;
    map.handlers.get("load")?.();
    expect(map.jumpTo).toHaveBeenCalledWith({ center: [-104.99, 39.74], zoom: 11 });
  });

  test("keeps private routes on a fallback background and clears the warning after a provider style recovers", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    map.handlers.get("error")?.();
    expect(await screen.findByText("The public base map could not be loaded. Your private routes remain available.")).toBeInTheDocument();
    expect(map.setStyle).toHaveBeenCalledWith(expect.objectContaining({ version: 8 }));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Dismiss base map warning" }));
    expect(screen.queryByText("The public base map could not be loaded. Your private routes remain available.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    act(() => map.handlers.get("error")?.());
    expect(await screen.findByText("The public base map could not be loaded. Your private routes remain available.")).toBeInTheDocument();
    mapBehavior.emitSetStyleLoad = true;
    await user.click(screen.getByRole("button", { name: "Select base map" }));
    await user.click(screen.getByRole("menuitem", { name: /^Road/ }));
    await waitFor(() => expect(screen.queryByText("The public base map could not be loaded. Your private routes remain available.")).not.toBeInTheDocument());
  });

	test("turns a private route tile 401 into session expiration", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
		const expired = vi.fn();
		window.addEventListener(SESSION_EXPIRED_EVENT, expired);
		render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const tileURL = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
		act(() => mapInstances.at(-1)?.handlers.get("error")?.({ sourceId: "private-workout-routes", error: { status: 401, url: tileURL } }));
		expect(expired).toHaveBeenCalledTimes(1);
		window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
	});

	test("refreshes an unavailable current route capability and ignores stale tile errors", async () => {
		let posts = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				posts++;
				const id = posts === 1 ? selection.id : "D".repeat(32);
				return Promise.resolve(json({ ...selection, id, expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(), routeTileUrl: selection.routeTileUrl.replace(selection.id, id) }));
			}
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const map = mapInstances[0];
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", expect.anything()));
		const currentTile = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
		act(() => map.handlers.get("error")?.({ sourceId: "private-workout-routes", error: { status: 404, url: currentTile } }));
		await waitFor(() => expect(posts).toBe(2));
		await waitFor(() => expect(map.addSource).toHaveBeenLastCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`] }));
		act(() => map.handlers.get("error")?.({ sourceId: "private-workout-routes", error: { status: 404, url: currentTile } }));
		await new Promise((resolve) => setTimeout(resolve, 5));
		expect(posts).toBe(2);
	});

	test("does not refit a deep-linked route when its capability refreshes", async () => {
		const requested = selection.workouts[0];
		history.replaceState({}, "", `/map?workoutId=${requested.id}`);
		let posts = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				posts++;
				const id = posts === 1 ? selection.id : posts === 2 ? "D".repeat(32) : "E".repeat(32);
				return Promise.resolve(json({ ...selection, id, workouts: [requested], bounds: requested.bounds, expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(), routeTileUrl: selection.routeTileUrl.replace(selection.id, id) }));
			}
			if (path === `/api/workouts/${requested.id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const map = mapInstances[0];
		await waitFor(() => expect(posts).toBe(2));
		await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
		map.jumpTo({ center: [-104.5, 39.5], zoom: 13 });
		map.fitBounds.mockClear();
		const currentTile = `${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32)).replace("{z}/{x}/{y}", "12/654/1583")}`;
		act(() => map.handlers.get("error")?.({ sourceId: "private-workout-routes", error: { status: 404, url: currentTile } }));
		await waitFor(() => expect(posts).toBe(3));
		await waitFor(() => expect(map.addSource).toHaveBeenLastCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "E".repeat(32))}`] }));
		expect(map.fitBounds).not.toHaveBeenCalled();
	});

	test("keeps recreated private route layers hidden when a capability refreshes in Coverage mode", async () => {
		const enabledConfig: PublicConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
		let posts = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				posts++;
				const id = posts === 1 ? selection.id : "D".repeat(32);
				return Promise.resolve(json({ ...selection, id, expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(), routeTileUrl: selection.routeTileUrl.replace(selection.id, id) }));
			}
			if (path === `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs` && method === "POST") return Promise.resolve(json(diagnostic, 201));
			if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={enabledConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
		const coverage = screen.getByRole("button", { name: "Coverage" });
		await waitFor(() => expect(coverage).toBeEnabled());
		await userEvent.click(coverage);
		await screen.findByRole("region", { name: "Coverage diagnostic review" });
		const map = mapInstances[0];
		const routeLayers = ["private-workout-routes", "private-workout-route-markers", "private-workout-route-hover", "private-workout-route-marker-hover"];
		const visibility = (id: string) => (map.layers.get(id) as { layout?: { visibility?: string } } | undefined)?.layout?.visibility ?? "visible";
		await waitFor(() => expect(routeLayers.map(visibility)).toEqual(["none", "none", "none", "none"]));
		await waitFor(() => expect(map.fitBounds).toHaveBeenCalled());
		map.fitBounds.mockClear();
		const currentTile = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
		act(() => map.handlers.get("error")?.({ sourceId: "private-workout-routes", error: { status: 404, url: currentTile } }));
		await waitFor(() => expect(posts).toBe(2));
		await waitFor(() => expect(map.addSource).toHaveBeenLastCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`] }));
		expect(routeLayers.map(visibility)).toEqual(["none", "none", "none", "none"]);
		for (const layer of routeLayers) expect(map.setLayoutProperty).toHaveBeenCalledWith(layer, "visibility", "none");
		expect(map.fitBounds).not.toHaveBeenCalled();
		expect(coverage).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByRole("region", { name: "Coverage diagnostic review" })).toBeInTheDocument();
	});

	test("refreshes an expired map selection capability on resume", async () => {
		let posts = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				posts++;
				const id = posts === 1 ? selection.id : "E".repeat(32);
				return Promise.resolve(json({ ...selection, id, expiresAt: posts === 1 ? "2000-01-01T00:00:00Z" : new Date(Date.now() + 30 * 60 * 1000).toISOString(), routeTileUrl: selection.routeTileUrl.replace(selection.id, id) }));
			}
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await waitFor(() => expect(posts).toBe(2));
		await waitFor(() => expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "E".repeat(32))}`] }));
	});

  test("installs private routes when an initial style event is missed", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    expect(map.addSource).not.toHaveBeenCalled();
    mapBehavior.styleLoaded = true;
    map.handlers.get("load")?.();
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] });
  });

  test("installs a refresh selection after style.load while base tiles are still pending", async () => {
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() => expect(mapInstances.at(-1)?.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] }));
  });
});
