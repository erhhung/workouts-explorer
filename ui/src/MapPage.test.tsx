import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiError, SESSION_EXPIRED_EVENT, type BaseMapsConfig, type DateRangePreference, type MapSelection, type MapSelectionWorkout, type Preferences, type PublicConfig } from "./api";
import MapPage, { absoluteRouteTileTemplate, buildRawRouteDirectionMarkers, buildRawRouteEndpoints, buildSegmentedRawRoute, COVERAGE_HIGHLIGHT_DURATION_MS, coverageRegionLabel, formatRoutePopupDetails, formatRoutePopupDistance, onMapContextReady, pointyDirectionMarkerImage, privateRouteTileUnauthorized, privateRouteTileUnavailable, requestedWorkoutIds, resolveBaseFamily, retryInitialDiagnosticBusy, routeColor, routeColors, routeEndpointOutline, selectionRequest, sortMapWorkouts, startCoverageHighlightBlink, type RoadCoverageCache } from "./MapPage";

const mapInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const popupInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const mapBehavior = vi.hoisted(() => ({ emitInitialStyleLoad: true, emitSetStyleLoad: true, styleLoaded: true, moving: false, tilesLoaded: true }));
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
			moveLayer = vi.fn((id: string) => {
				const layer = this.layers.get(id);
				if (layer) {
					this.layers.delete(id);
					this.layers.set(id, layer);
				}
			});
      jumpTo = vi.fn();
      style: { layers?: Array<{ id: string }> };
      setStyle = vi.fn((style: unknown) => { this.style = typeof style === "string" ? { layers: [] } : style as { layers?: Array<{ id: string }> }; if (typeof style !== "string") mapBehavior.styleLoaded = true; if (typeof style !== "string" || mapBehavior.emitSetStyleLoad) this.handlers.get("style.load")?.(); });
      getStyle = vi.fn(() => this.style);
      isStyleLoaded = vi.fn(() => mapBehavior.styleLoaded);
      isMoving = vi.fn(() => mapBehavior.moving);
      areTilesLoaded = vi.fn(() => mapBehavior.tilesLoaded);
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
				this.style = (options as { style: { layers?: Array<{ id: string }> } }).style;
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
const preferences: Preferences = { theme: "dark", units: "metric", timezone: "America/Denver", firstWeekday: "monday", clockFormat: "24h", workoutColumns: ["date", "type"], pageSize: 25, coverageDiagnosticsEnabled: true, initialized: true, dateRange: "last30Days" };
const selection: MapSelection = {
  id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(), dataGeneration: 7,
  range: { startDate: "2026-07-10", endDate: "2026-08-08" },
  bounds: { minimumLongitude: -105.3, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40.1 },
  focusedWorkoutId: null,
  workouts: [
    { id: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", type: { id: "11111111111111111111111111111111", key: "running", name: "Running" }, startedAt: "2026-08-08T12:00:00Z", endedAt: "2026-08-08T13:45:00Z", duration: "6300", localStartDate: "2026-08-08", partialRoute: false, bounds: { minimumLongitude: -105.3, minimumLatitude: 39.9, maximumLongitude: -105.2, maximumLatitude: 40.1 }, distance: { value: "8.25", unit: "km" }, pace: { value: "5", unit: "min/km" }, calories: { value: "500", unit: "kcal" }, heartRate: { value: "120", unit: "count/min" }, elevationGain: { value: "100", unit: "m" }, coverageReadiness: { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" } },
    { id: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" }, startedAt: "2026-08-01T12:00:00Z", endedAt: "2026-08-01T13:00:00Z", duration: "3600", localStartDate: "2026-08-01", partialRoute: true, bounds: { minimumLongitude: -105.2, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40 }, distance: null, pace: null, calories: { value: "300", unit: "kcal" }, heartRate: { value: "100", unit: "count/min" }, elevationGain: { value: "250", unit: "m" }, coverageReadiness: { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" } },
  ],
  routeTileUrl: "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/route-tiles/7/{z}/{x}/{y}.pbf",
  coverageTileUrl: "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/coverage-tiles/7/{z}/{x}/{y}.pbf",
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

beforeEach(() => { history.replaceState({}, "", "/map"); mapInstances.splice(0); popupInstances.splice(0); mapBehavior.emitInitialStyleLoad = true; mapBehavior.emitSetStyleLoad = true; mapBehavior.styleLoaded = true; mapBehavior.moving = false; mapBehavior.tilesLoaded = true; });
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
  test("formats provider region IDs for display", () => {
    expect(coverageRegionLabel("geofabrik:norcal")).toBe("Norcal");
    expect(coverageRegionLabel("geofabrik:new-york")).toBe("New York");
    expect(coverageRegionLabel("colorado")).toBe("Colorado");
    expect(coverageRegionLabel("future-provider:upper-valley")).not.toContain("future-provider");
  });

  test("starts highlighting immediately and counts three seconds only after map context is ready", () => {
    vi.useFakeTimers();
    try {
      const opacities: number[] = [];
      let ready: () => void = () => {};
      const cleanup = startCoverageHighlightBlink((opacity) => opacities.push(opacity), (handler) => { ready = handler; return () => undefined; });
      expect(opacities).toEqual([1]);
      vi.advanceTimersByTime(5000);
      expect(opacities.length).toBeGreaterThan(10);
      ready();
      const atIdle = opacities.length;
      vi.advanceTimersByTime(COVERAGE_HIGHLIGHT_DURATION_MS - 1);
      expect(opacities.length).toBeGreaterThan(atIdle);
      vi.advanceTimersByTime(1);
      expect(opacities.at(-1)).toBe(0);
      const atFinish = opacities.length;
      vi.advanceTimersByTime(1000);
      expect(opacities).toHaveLength(atFinish);
      cleanup();
    } finally {
      vi.useRealTimers();
    }
  });

  test("detects map context readiness from render without waiting for idle", () => {
    vi.useFakeTimers();
    try {
      let moving = true, tilesLoaded = false;
      const handlers = new Map<string, () => void>();
      const map = {
        isStyleLoaded: () => true,
        isMoving: () => moving,
        areTilesLoaded: () => tilesLoaded,
        on: (event: string, handler: () => void) => handlers.set(event, handler),
        off: (event: string) => handlers.delete(event),
      } as never;
      const ready = vi.fn();
      const cleanup = onMapContextReady(map, ready);
      vi.advanceTimersByTime(0);
      expect(ready).not.toHaveBeenCalled();
      handlers.get("render")?.();
      expect(ready).not.toHaveBeenCalled();
      moving = false; tilesLoaded = true;
      handlers.get("render")?.();
      expect(ready).toHaveBeenCalledTimes(1);
      cleanup();
      expect(handlers).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });

  test("builds enum and explicit selectors and canonicalizes compact workout IDs", () => {
    expect(selectionRequest("last7Days", "America/Denver", ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"])).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver", workoutIds: ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"] });
    expect(selectionRequest("last7Days", "America/Denver", [])).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver", workoutIds: [] });
    expect(selectionRequest("last7Days", "America/Denver", ["A".repeat(32)], "A".repeat(32))).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver", workoutIds: ["A".repeat(32)], focusedWorkoutId: "A".repeat(32) });
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
		expect(privateRouteTileUnauthorized({ sourceId: "private-workout-coverage", error: { status: 401 } })).toBe(true);
		expect(privateRouteTileUnauthorized({ error: { status: 401, url: `${window.location.origin}/api/map-selections/${"A".repeat(32)}/route-tiles/7/12/654/1583.pbf` } })).toBe(true);
		expect(privateRouteTileUnauthorized({ error: { status: 401, url: `${window.location.origin}/api/map-selections/${"A".repeat(32)}/coverage-tiles/7/12/654/1583.pbf` } })).toBe(true);
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
	test("moves keyboard focus to the canvas for an explicit tab-navigation request", async () => {
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			if (String(input) === "/api/map-selections" && init?.method === "POST") return Promise.resolve(json(selection));
			if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
		});
		const props = { config, preferences, csrfToken: "csrf-map", dateRange: "last30Days" as const, onDateRangeSelected: vi.fn() };
		const view = render(<MapPage {...props} explicitCanvasFocusRequest={0} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const map = mapInstances.at(-1)!;
		map.canvas.blur();
		expect(document.activeElement).not.toBe(map.canvas);
		view.rerender(<MapPage {...props} explicitCanvasFocusRequest={1} />);
		await waitFor(() => expect(document.activeElement).toBe(map.canvas));
	});

	test("retries an initial busy diagnostic once and keeps later failures", async () => {
		const controller = new AbortController();
		const succeeds = vi.fn()
			.mockRejectedValueOnce(new ApiError(429))
			.mockResolvedValueOnce("prepared");
		await expect(retryInitialDiagnosticBusy(succeeds, controller.signal, 0)).resolves.toBe("prepared");
		expect(succeeds).toHaveBeenCalledTimes(2);

		const remainsBusy = vi.fn().mockRejectedValue(new ApiError(429));
		await expect(retryInitialDiagnosticBusy(remainsBusy, controller.signal, 0)).rejects.toMatchObject({ status: 429 });
		expect(remainsBusy).toHaveBeenCalledTimes(2);
	});

	test("cancels a pending busy diagnostic retry when the route changes", async () => {
		const controller = new AbortController();
		const request = vi.fn().mockRejectedValue(new ApiError(429));
		const result = retryInitialDiagnosticBusy(request, controller.signal, 60_000);
		controller.abort();
		await expect(result).rejects.toMatchObject({ name: "AbortError" });
		expect(request).toHaveBeenCalledTimes(1);
	});

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
		expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-raw-route")?.[0].paint["line-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
		expect(map.addImage).toHaveBeenCalledWith("coverage-diagnostic-direction-triangle", expect.objectContaining({ width: 11, height: 15 }));
		expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "coverage-diagnostic-direction")?.[0]).toMatchObject({ type: "symbol", layout: { "icon-image": "coverage-diagnostic-direction-triangle", "icon-rotate": ["get", "bearing"] }, paint: { "icon-opacity-transition": { duration: 0, delay: 0 } } });
		expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-direction", "icon-opacity-transition", { duration: 400, delay: 0 });
		const directionSource = map.sources.get("coverage-diagnostic-direction");
		directionSource.setData.mockClear();
		act(() => map.handlers.get("moveend")?.());
		expect(directionSource.setData).toHaveBeenCalledWith(expect.objectContaining({ type: "FeatureCollection" }));
		expect(map.addSource).toHaveBeenCalledWith("private-workout-route-endpoints", { type: "geojson", data: rawRouteEndpoints });
		const startLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-start")?.[0];
		const finishLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-finish")?.[0];
		expect(startLayer.paint["circle-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
		expect(startLayer.paint["circle-stroke-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
		expect(finishLayer.paint["text-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
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
		expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-direction", "icon-opacity", 0);
		expect(map.setLayoutProperty).not.toHaveBeenCalledWith("coverage-diagnostic-direction", "visibility", "none");
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 0);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-start", "circle-stroke-opacity", 0);
		expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-finish", "text-opacity", 0);
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-raw-route", "line-opacity", 1));
		expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-direction", "icon-opacity", 0.95);
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
    map.setPaintProperty.mockClear();
    await userEvent.click(screen.getByRole("button", { name: "Routes" }));
    expect(map.setPaintProperty).toHaveBeenCalledWith("coverage-diagnostic-direction", "icon-opacity-transition", { duration: 0, delay: 0 });
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

	test("explains when all diagnostic workers are busy", async () => {
		const enabledConfig: PublicConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
		let diagnosticRequests = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (path.endsWith("/coverage-diagnostic-runs") && method === "POST") { diagnosticRequests++; return Promise.resolve(json({ title: "Too Many Requests", detail: "coverage diagnostic capacity is busy" }, 429)); }
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={enabledConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
		await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
		expect(await screen.findByRole("alert", {}, { timeout: 3500 })).toHaveTextContent("The diagnostic overlay could not be prepared because all available workers are currently busy. Please try again later.");
		expect(diagnosticRequests).toBe(2);
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
    expect(screen.getByLabelText("Active map attribution for Road")).toHaveTextContent("Road map");
    expect(screen.queryByRole("link", { name: "Map data" })).not.toBeInTheDocument();
    expect(requests[0]).toEqual({ path: "/api/map-selections", method: "POST", body: { dateRangeEnum: "last30Days", tz: "America/Denver" }, csrf: "csrf-map" });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] }));
    expect(map.addLayer.mock.calls.some((call: any[]) => call[0]["source-layer"] === "routes" && call[0].layout["line-sort-key"][1] === "sortOrder")).toBe(true);
    expect([...map.sources.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    expect([...map.layers.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    const routeLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-routes")?.[0];
    expect(routeLayer.paint["line-color"].filter((value: unknown) => value === "running")).toHaveLength(1);
    expect(map.addLayer.mock.calls.some((call: any[]) => call[0].paint["line-color"] === "#c026ff" && call[0].paint["line-opacity"] === 1)).toBe(true);
    const markerLayer = map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-markers")?.[0];
    expect(markerLayer).toMatchObject({ type: "circle", "source-layer": "routes", filter: ["==", ["geometry-type"], "Point"] });
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-route-marker-hover")?.[0].paint["circle-color"]).toBe("#c026ff");
    expect(screen.getByRole("application", { name: "Workout route map" })).not.toHaveAttribute("aria-keyshortcuts");
    map.setPaintProperty.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-routes", "line-opacity", 0);
    expect(map.fitBounds).toHaveBeenCalledWith([[-105.3, 39.8], [-105.1, 40.1]], { padding: 48, duration: 350 });
    view.unmount();
    await waitFor(() => expect(requests).toContainEqual({ path: `/api/map-selections/${selection.id}`, method: "DELETE", body: undefined, csrf: "csrf-map" }));
    expect(map.remove).toHaveBeenCalled();
  });

  test("shows aggregate coverage and statistics when diagnostics preference is disabled", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const coverageEntity = { entityId: "E".repeat(32), entityKind: "park", name: "Example Park", localityName: null, regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "park", rangeWorkoutCount: 3, rangeFirstDate: "2026-07-12", rangeFirstWorkoutId: selection.workouts[1].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: 7, allTimeFirstDate: "2026-01-01", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds } as const;
    const secondEntity = { ...coverageEntity, entityId: "D".repeat(32), entityKind: "path", name: "Alpha Road", localityName: "Alpha City", broadClass: "road", rangeWorkoutCount: 1 } as const;
    const unnamedEntity = { ...secondEntity, entityId: "C".repeat(32), name: null, localityName: null, broadClass: "footway" } as const;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
      if (path.startsWith(`/api/map-selections/${selection.id}/coverage/paths?`)) {
        const requestedPage = Number(new URL(path, "https://test").searchParams.get("page"));
        return Promise.resolve(json({ pagination: { page: requestedPage, pageSize: 100, totalItems: 3, totalPages: 2 }, items: requestedPage === 1 ? [coverageEntity] : [secondEntity, unnamedEntity] }));
      }
      if (path === `/api/map-selections/${selection.id}/coverage/park/${coverageEntity.entityId}?generation=${selection.dataGeneration}`) return Promise.resolve(json({ ...coverageEntity, geometry: { type: "LineString", coordinates: [[-105.2, 40], [-105.19, 40.01]] }, fitBounds: { minimumLongitude: -105.205, minimumLatitude: 39.995, maximumLongitude: -105.185, maximumLatitude: 40.015 } }));
      if (path === `/api/map-selections/${selection.id}` && method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function CachedCoverageMap() {
      const [cache, setCache] = useState<RoadCoverageCache>();
      return <MapPage config={{ ...config, features: { coverageMatcherDiagnostics: true } }} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={vi.fn()} roadCoverageCache={cache} onRoadCoverageCacheChange={setCache} />;
    }
    render(<CachedCoverageMap />);
    const coverage = await screen.findByRole("button", { name: "Coverage" });
    expect(coverage).toBeEnabled();
    await userEvent.click(coverage);
    expect(await screen.findByRole("region", { name: "Coverage statistics" })).toHaveTextContent("Road coverage");
    expect(screen.getByLabelText("Coverage workout count legend")).toHaveTextContent("26+ workouts");
    const map = mapInstances.at(-1)!;
    expect(screen.getByRole("button", { name: "Fit" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("dialog", { name: "Road Coverage" })).toBeVisible();
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(screen.queryByRole("region", { name: "Coverage statistics" })).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Workouts/ })).toHaveAttribute("aria-sort", "descending");
    expect(screen.getAllByRole("button", { name: "Jul 12, 2026" })[0]).toBeVisible();
    expect(screen.getAllByText("Jan 1, 2026")[0].tagName).toBe("SPAN");
    const coverageRequestCount = vi.mocked(globalThis.fetch).mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length;
    expect(coverageRequestCount).toBe(2);
    await userEvent.click(screen.getByRole("button", { name: /City\/County\/Region/ }));
    expect(screen.getByRole("columnheader", { name: /City\/County\/Region/ })).toHaveAttribute("aria-sort", "ascending");
    expect(within(screen.getByRole("table")).getAllByRole("button", { name: /Alpha Road|Example Park/ }).map((button) => button.textContent)).toEqual(["Alpha Road", "Example Park"]);
    expect(screen.queryByRole("button", { name: "Pedestrian path" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }));
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    expect(screen.getAllByText("Northern California").length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: /Workouts/ }));
    expect(within(screen.getByRole("table")).getAllByRole("button", { name: /Example Park|Alpha Road|Pedestrian path/ }).map((button) => button.textContent)).toEqual(["Example Park", "Alpha Road", "Pedestrian path"]);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "northern california" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible(), { timeout: 1000 });
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "path" } });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(), { timeout: 1000 });
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "pedestrian" } });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(), { timeout: 1000 });
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "not present" } });
    await waitFor(() => expect(screen.getByText("No roads, paths, or parks match this search.")).toBeVisible(), { timeout: 1000 });
    expect(screen.getByText("Page 1 of 1", { selector: ".pagination span" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "park" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Example Park" })).toBeVisible(), { timeout: 1000 });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(), { timeout: 1000 });
    expect(vi.mocked(globalThis.fetch).mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length).toBe(coverageRequestCount);
    await userEvent.click(screen.getByRole("button", { name: "Example Park" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument());
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-coverage", { type: "vector", tiles: [`${window.location.origin}${selection.coverageTileUrl}`], minzoom: 0, maxzoom: 22 }));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-coverage-focus", expect.objectContaining({ type: "geojson" }));
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage")?.[0]["source-layer"]).toBe("coverage");
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage-focus")?.[0].filter).toEqual(["==", ["get", "entityKind"], "path"]);
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage-focus")?.[0].source).toBe("private-workout-coverage-focus");
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage")?.[0].paint["line-color"][1]).toEqual(["get", "countBucket"]);
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage-focus")?.[0].paint["line-color"][1]).toEqual(["get", "countBucket"]);
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-coverage-focus");
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-coverage-parks-focus");
    expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage-focus")?.[0].paint["line-color"]).toContain("#ffd8f0");
    for (const layer of ["private-workout-coverage", "private-workout-coverage-parks"]) {
      expect(map.addLayer.mock.calls.find((call: any[]) => call[0].id === layer)?.[0].paint["line-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    }
    expect(map.setLayoutProperty).toHaveBeenCalledWith("private-workout-coverage", "visibility", "visible");
    expect(map.setLayoutProperty).toHaveBeenCalledWith("private-workout-routes", "visibility", "none");
    expect(map.addSource).toHaveBeenCalledWith("private-workout-coverage-highlight", expect.objectContaining({ type: "geojson" }));
    expect(map.fitBounds).toHaveBeenCalledWith([[-105.205, 39.995], [-105.185, 40.015]], { padding: 48, duration: 350 });
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute("aria-keyshortcuts", "Space");
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    map.removeLayer.mockClear(); map.removeSource.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-coverage", "line-opacity", 0));
    expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-coverage-parks", "line-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-coverage-focus", "line-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-coverage-parks-focus", "line-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-coverage-highlight", "line-opacity", 0);
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-coverage", "line-opacity", 0.94));
    expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-coverage-parks", "line-opacity", 0.94);
    for (const layer of ["private-workout-coverage", "private-workout-coverage-parks"]) {
      expect(map.setLayoutProperty).not.toHaveBeenCalledWith(layer, "visibility", "none");
      expect(map.removeLayer).not.toHaveBeenCalledWith(layer);
    }
    expect(map.removeSource).not.toHaveBeenCalled();
    const requestsAfterInitialLoad = vi.mocked(globalThis.fetch).mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length;
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    fireEvent.keyDown(screen.getByRole("dialog", { name: "Road Coverage" }), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument());
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    expect(vi.mocked(globalThis.fetch).mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length).toBe(requestsAfterInitialLoad);
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([input]) => String(input).includes("coverage-diagnostic-runs"))).toBe(false);
  });

  test("switches pink production Coverage to delayed list hover without replacing persistent focus", async () => {
    const focusedSelection = { ...selection, focusedWorkoutId: selection.workouts[0].id };
    const focusGeometry = (offset: number) => ({ type: "FeatureCollection", features: [{ type: "Feature", properties: { entityKind: "path", countBucket: 1 }, geometry: { type: "LineString", coordinates: [[-105.2 + offset, 40], [-105.19 + offset, 40.01]] } }] });
    const focusRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(focusedSelection));
      if (path.includes("/coverage-focus/")) { focusRequests.push(path); return Promise.resolve(json(path.includes(selection.workouts[1].id) ? focusGeometry(0.01) : focusGeometry(0))); }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const onFocusedWorkoutChange = vi.fn();
    render(<MapPage config={config} preferences={{ ...preferences, coverageDiagnosticsEnabled: false }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={onFocusedWorkoutChange} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    const aggregateSources = () => map.addSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage");
    await waitFor(() => expect(focusRequests.some((path) => path.includes(selection.workouts[0].id))).toBe(true));
    const focusSource = map.getSource("private-workout-coverage-focus") as { setData: ReturnType<typeof vi.fn> };
    const aggregateAddsBeforeHover = aggregateSources().length;
    const aggregateRemovalsBeforeHover = map.removeSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage").length;
    const fitsBeforeHover = map.fitBounds.mock.calls.length;
    const stylesBeforeHover = map.setStyle.mock.calls.length;

    vi.useFakeTimers();
    const hikingButton = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ });
    fireEvent.pointerEnter(hikingButton);
    await act(async () => { await vi.advanceTimersByTimeAsync(249); });
    expect(focusRequests.filter((path) => path.includes(selection.workouts[1].id))).toHaveLength(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(focusRequests.filter((path) => path.includes(selection.workouts[1].id))).toHaveLength(1);
    expect(focusSource.setData).toHaveBeenLastCalledWith(focusGeometry(0.01));
    expect(aggregateSources()).toHaveLength(aggregateAddsBeforeHover);
    expect(map.removeSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage")).toHaveLength(aggregateRemovalsBeforeHover);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeHover);
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeHover);
    expect(onFocusedWorkoutChange).not.toHaveBeenCalled();

    fireEvent.pointerLeave(hikingButton.closest("ol")!);
    await act(async () => { await Promise.resolve(); });
    expect(focusSource.setData).toHaveBeenLastCalledWith(focusGeometry(0));
    expect(aggregateSources()).toHaveLength(aggregateAddsBeforeHover);
    expect(map.removeSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage")).toHaveLength(aggregateRemovalsBeforeHover);
    expect(focusRequests.filter((path) => path.includes(selection.workouts[0].id))).toHaveLength(1);
    vi.useRealTimers();
  });

  test("shows Coverage readiness, disables result-less focus, and polls until a result is applied", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const pendingSelection: MapSelection = {
      ...selection,
      workouts: [
        { ...selection.workouts[0], coverageReadiness: { mapDataStatus: "ready", processingStatus: "queued", resultStatus: "none" } },
        { ...selection.workouts[1], coverageReadiness: { mapDataStatus: "ready", processingStatus: "failed", resultStatus: "stale" } },
      ],
    };
    const currentSelection: MapSelection = {
      ...pendingSelection,
      id: "D".repeat(32),
      workouts: [
        { ...pendingSelection.workouts[0], coverageReadiness: { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" } },
        pendingSelection.workouts[1],
      ],
    };
    let selectionPosts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        selectionPosts++;
        return Promise.resolve(json(selectionPosts < 3 ? pendingSelection : currentSelection));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const running = await screen.findByRole("button", { name: /Running.*8\/08\/2026/ });
    expect(running.querySelector(".route-swatch")).toBeInTheDocument();

    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const unavailableRunning = screen.getByRole("button", { name: /Coverage pending.*Running.*8\/08\/2026/ });
    const staleHiking = screen.getByRole("button", { name: /Coverage stale.*Hiking.*8\/01\/2026/ });
    expect(unavailableRunning).toBeDisabled();
    expect(unavailableRunning.closest("li")).toHaveClass("is-coverage-unavailable");
    expect(unavailableRunning.querySelector(".coverage-route-status--pending")).toBeInTheDocument();
    expect(screen.getByText("Waiting for coverage update")).toHaveAttribute("role", "tooltip");
    expect(staleHiking).toBeEnabled();
    expect(staleHiking.querySelector(".coverage-route-status--stale")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Running.*8\/08\/2026/ })?.querySelector(".route-swatch")).not.toBeInTheDocument();

    await act(async () => { await vi.advanceTimersByTimeAsync(9_999); });
    expect(selectionPosts).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(selectionPosts).toBe(2);
    expect(screen.getByRole("button", { name: /Coverage pending.*Running.*8\/08\/2026/ })).toBeDisabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(selectionPosts).toBe(3);
    const currentRunning = screen.getByRole("button", { name: /Coverage current.*Running.*8\/08\/2026/ });
    expect(currentRunning).toBeEnabled();
    expect(currentRunning.querySelector(".coverage-route-status--current")).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(selectionPosts).toBe(3);
    vi.useRealTimers();
  });

  test("explains unavailable map data and failed Coverage updates on disabled rows", async () => {
    const unavailableSelection: MapSelection = {
      ...selection,
      workouts: [
        { ...selection.workouts[0], coverageReadiness: { mapDataStatus: "unavailable", processingStatus: "unprocessed", resultStatus: "none" } },
        { ...selection.workouts[1], coverageReadiness: { mapDataStatus: "ready", processingStatus: "failed", resultStatus: "none" } },
      ],
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") return Promise.resolve(json(unavailableSelection));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(<MapPage config={config} preferences={{ ...preferences, coverageDiagnosticsEnabled: false }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const coverageMode = screen.getByRole("button", { name: "Coverage" });
    await waitFor(() => expect(coverageMode).toBeEnabled());
    fireEvent.click(coverageMode);

    const unavailable = screen.getByRole("button", { name: /Coverage unavailable.*Running/ });
    const failed = screen.getByRole("button", { name: /Coverage failed.*Hiking/ });
    expect(unavailable).toBeDisabled();
    expect(failed).toBeDisabled();
    expect(unavailable.querySelector(".coverage-route-status--unavailable")).toBeInTheDocument();
    expect(failed.querySelector(".coverage-route-status--failed")).toBeInTheDocument();
    expect(screen.getByText("Map data not available")).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Coverage update failed")).toHaveAttribute("role", "tooltip");
  });

  test("renders every Coverage status with its production indicator and tooltip", async () => {
    const readinessStates: MapSelectionWorkout["coverageReadiness"][] = [
      { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" },
      { mapDataStatus: "ready", processingStatus: "stale", resultStatus: "stale" },
      { mapDataStatus: "pending", processingStatus: "unprocessed", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "queued", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "failed", resultStatus: "none" },
      { mapDataStatus: "unavailable", processingStatus: "unprocessed", resultStatus: "none" },
    ];
    const previewSelection: MapSelection = {
      ...selection,
      workouts: Array.from({ length: 6 }, (_, index) => ({
        ...selection.workouts[0],
        id: String.fromCharCode(68 + index).repeat(32),
        startedAt: `2026-08-${String(8 - index).padStart(2, "0")}T12:00:00Z`,
        localStartDate: `2026-08-${String(8 - index).padStart(2, "0")}`,
        coverageReadiness: readinessStates[index],
      })),
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") return Promise.resolve(json(previewSelection));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(<MapPage config={config} preferences={{ ...preferences, coverageDiagnosticsEnabled: false }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    expect(await screen.findAllByRole("checkbox", { name: /Show Running/ })).toHaveLength(6);
    const coverageMode = screen.getByRole("button", { name: "Coverage" });
    await waitFor(() => expect(coverageMode).toBeEnabled());
    fireEvent.click(coverageMode);

    expect(document.querySelectorAll(".coverage-route-status--current")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--stale")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--pending")).toHaveLength(2);
    expect(document.querySelectorAll(".coverage-route-status--failed")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--unavailable")).toHaveLength(1);
    expect(document.querySelector(".coverage-route-status--current")).toHaveTextContent("✓");
    expect(document.querySelector(".coverage-route-status--failed")).toHaveTextContent("✗");
    expect(document.querySelector(".coverage-route-status--unavailable")).toHaveTextContent("×");
    expect(screen.getByText("Waiting for map data")).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Waiting for coverage update")).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Coverage update failed")).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Map data not available")).toHaveAttribute("role", "tooltip");

    fireEvent.click(screen.getByRole("button", { name: /Coverage current.*Running/ }));
    expect(new URL(window.location.href).searchParams.get("workoutId")).toBe("D".repeat(32));
  });

  test("shows delayed exact road coverage details after one transient failure without exposing a raw focused route", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const focusedSelection = { ...selection, focusedWorkoutId: selection.workouts[0].id };
    const entityID = "E".repeat(32);
    let detailAttempts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(focusedSelection));
      if (path === `/api/map-selections/${selection.id}/coverage/path/${entityID}?generation=${selection.dataGeneration}`) {
        detailAttempts++;
        if (detailAttempts === 1) return Promise.resolve(json({ title: "Service Unavailable" }, 503));
        return Promise.resolve(json({ entityId: entityID, entityKind: "path", name: null, localityName: "Yosemite National Park", regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "cycleway", rangeWorkoutCount: 3, rangeFirstDate: "2025-10-25", rangeFirstWorkoutId: selection.workouts[0].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: 8, allTimeFirstDate: "2024-04-02", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds, fitBounds: selection.bounds, geometry: { type: "LineString", coordinates: [[-105.2, 40], [-105.19, 40.01]] } }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const diagnosticConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
    const view = render(<MapPage config={diagnosticConfig} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    map.queryRenderedFeatures.mockImplementation((_point: unknown, options: { layers?: string[] }) => options.layers?.some((layer) => layer.includes("coverage")) ? [
      { properties: { entityId: "D".repeat(32), entityKind: "park", name: "Yosemite National Park" } },
      { properties: { entityId: entityID, entityKind: "path", name: null, localityName: "Yosemite National Park" } },
    ] : []);
    vi.useFakeTimers();
    act(() => map.handlers.get("mousemove")?.({ point: { x: 50, y: 50 }, lngLat: { lng: -105.2, lat: 40 } }));
    expect(map.queryRenderedFeatures).toHaveBeenCalledWith([[47, 47], [53, 53]], { layers: ["private-workout-coverage-parks", "private-workout-coverage"] });
    act(() => vi.advanceTimersByTime(749));
    expect(popupInstances).toHaveLength(0);
    await act(async () => { vi.advanceTimersByTime(1); await Promise.resolve(); await Promise.resolve(); });
    expect(popupInstances).toHaveLength(0);
    await act(async () => { vi.advanceTimersByTime(250); await Promise.resolve(); await Promise.resolve(); });
    expect(popupInstances).toHaveLength(1);
    expect(detailAttempts).toBe(2);
    expect(popupInstances[0].content).toHaveTextContent("Bike pathYosemite National ParkWorkouts3Earliest visit10/25/2025First visit ever4/2/2024Most recent visit8/5/2026Last visit ever8/5/2026");
    expect(map.setLayoutProperty).toHaveBeenCalledWith("private-workout-routes", "visibility", "none");
    expect(map.getLayer("coverage-diagnostic-raw-route")).toBeUndefined();
    vi.useRealTimers();
    view.rerender(<MapPage config={diagnosticConfig} preferences={{ ...productionPreferences, coverageDiagnosticsEnabled: true }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={vi.fn()} />);
    await waitFor(() => expect(popupInstances[0].remove).toHaveBeenCalled());
  });

  test("replaces a failed Road Coverage table with a compact retryable error dialog", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    let coverageRequests = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
      if (path.includes("/coverage/paths?")) { coverageRequests++; return Promise.resolve(json({ title: "Service Unavailable" }, 503)); }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    const dialog = await screen.findByRole("dialog", { name: "Road Coverage" });
    expect(dialog).toHaveTextContent("Road coverage could not be loaded.");
    expect(within(dialog).queryByRole("table")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("searchbox")).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(coverageRequests).toBe(2));
    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument();
  });

  test("refreshes a stale map selection and resumes Road Coverage loading after a 404", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    let selectionRequests = 0, coverageRequests = 0;
    const coverageEntity = { entityId: "E".repeat(32), entityKind: "park", name: "Example Park", localityName: "Test City", regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "park", rangeWorkoutCount: 3, rangeFirstDate: "2026-07-12", rangeFirstWorkoutId: selection.workouts[1].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: 7, allTimeFirstDate: "2026-01-01", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds } as const;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input), method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        selectionRequests++;
        return Promise.resolve(json(selectionRequests === 1 ? selection : { ...selection, id: "8".repeat(32), dataGeneration: selection.dataGeneration + 1 }));
      }
      if (path.includes("/coverage/paths?")) {
        coverageRequests++;
        return Promise.resolve(coverageRequests === 1 ? json({ title: "Not Found" }, 404) : json({ pagination: { page: 1, pageSize: 100, totalItems: 1, totalPages: 1 }, items: [coverageEntity] }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(selectionRequests).toBeGreaterThanOrEqual(2);
    expect(coverageRequests).toBe(2);
    expect(screen.queryByText("Road coverage could not be loaded.")).not.toBeInTheDocument();
  });

  test("refreshes Road Coverage when an updating entity disappears", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const staleEntity = { entityId: "E".repeat(32), entityKind: "path", name: null, localityName: "Mountain View", regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "footway", rangeWorkoutCount: 25, rangeFirstDate: "2025-04-03", rangeFirstWorkoutId: selection.workouts[1].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: 27, allTimeFirstDate: "2025-04-03", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds } as const;
    const replacement = { ...staleEntity, entityId: "D".repeat(32), name: "Replacement Path" } as const;
    let listRequests = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input), method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
      if (path.includes("/coverage/paths?")) {
        listRequests++;
        return Promise.resolve(json({ pagination: { page: 1, pageSize: 100, totalItems: 1, totalPages: 1 }, items: listRequests === 1 ? [staleEntity] : [replacement] }));
      }
      if (path.includes(`/coverage/path/${staleEntity.entityId}`)) return Promise.resolve(json({ title: "Not Found" }, 404));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }));
    await userEvent.click(await screen.findByRole("button", { name: "Pedestrian path" }));
    expect(await screen.findByRole("button", { name: "Replacement Path" })).toBeVisible();
    expect(listRequests).toBe(2);
    expect(screen.queryByText("That road coverage could not be shown on the map.")).not.toBeInTheDocument();
  });

  test("restores the cached Road Coverage page and resets it for search", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false, pageSize: 1 };
    const entity = (id: string, name: string, count: number) => ({ entityId: id.repeat(32), entityKind: "path", name, localityName: "Test City", regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "road", rangeWorkoutCount: count, rangeFirstDate: "2026-07-12", rangeFirstWorkoutId: selection.workouts[1].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: count, allTimeFirstDate: "2026-07-12", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds } as const);
    const high = entity("E", "High Road", 2), low = entity("F", "Low Road", 1);
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input), method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        return Promise.resolve(json(body.dateRangeEnum === "last7Days" ? { ...selection, id: "9".repeat(32), range: { startDate: "2026-07-30", endDate: "2026-08-05" } } : selection));
      }
      if (path.includes("/coverage/paths?")) return Promise.resolve(json({ pagination: { page: 1, pageSize: 100, totalItems: 2, totalPages: 1 }, items: [high, low] }));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function CachedCoverageMap({ range }: { range: DateRangePreference }) {
      const [cache, setCache] = useState<RoadCoverageCache>();
      return <MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange={range} onDateRangeSelected={vi.fn()} roadCoverageCache={cache} onRoadCoverageCacheChange={setCache} />;
    }
    const view = render(<CachedCoverageMap range="last30Days" />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "High Road" })).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Page 2 of 2")).toBeVisible();
    expect(screen.getByRole("button", { name: "Low Road" })).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Close Road Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByText("Page 2 of 2")).toBeVisible();
    expect(screen.getByRole("button", { name: "Low Road" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "High" } });
    await waitFor(() => expect(screen.getByText("Page 1 of 1")).toBeVisible(), { timeout: 1000 });
    expect(screen.getByRole("button", { name: "High Road" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Low Road" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }));
    await userEvent.click(screen.getByRole("button", { name: "Close Road Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByText("Page 1 of 1")).toBeVisible();
    expect(screen.getByRole("searchbox")).toHaveValue("High");
    expect(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" })).toBeChecked();
    expect(screen.getByRole("button", { name: "High Road" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } });
    expect(screen.getByRole("searchbox")).toHaveValue("");
    await waitFor(() => expect(screen.getByText("Page 1 of 2")).toBeVisible(), { timeout: 1000 });
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Page 2 of 2")).toBeVisible();
    view.rerender(<CachedCoverageMap range="last7Days" />);
    await waitFor(() => expect(screen.getByText("Page 1 of 2")).toBeVisible());
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" })).not.toBeChecked();
  });

  test("uses a two-row loading body before locking Road Coverage to the loaded result height", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    let resolveCoverage!: (response: Response) => void;
    const deferredCoverage = new Promise<Response>((resolve) => { resolveCoverage = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input); const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
      if (path.includes("/coverage/paths?")) return deferredCoverage;
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={productionPreferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} persistedFocusedWorkoutId={selection.workouts[0].id} onFocusedWorkoutChange={vi.fn()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    const dialog = await screen.findByRole("dialog", { name: "Road Coverage" });
    const frame = dialog.querySelector<HTMLElement>(".road-coverage-table-wrap")!;
    expect(frame.style.getPropertyValue("--road-coverage-row-slots")).toBe("2");
    expect(await within(dialog).findByText("Loading coverage...", {}, { timeout: 1500 })).toHaveAttribute("role", "status");
    resolveCoverage(json({ pagination: { page: 1, pageSize: 100, totalItems: 1, totalPages: 1 }, items: [{ entityId: "E".repeat(32), entityKind: "park", name: "Example Park", localityName: "Test City", regionId: "geofabrik:norcal", regionName: "Northern California", broadClass: "park", rangeWorkoutCount: 3, rangeFirstDate: "2026-07-12", rangeFirstWorkoutId: selection.workouts[1].id, rangeLatestDate: "2026-08-05", rangeLatestWorkoutId: selection.workouts[0].id, allTimeWorkoutCount: 7, allTimeFirstDate: "2026-01-01", allTimeFirstWorkoutId: selection.workouts[1].id, allTimeLatestDate: "2026-08-05", allTimeLatestWorkoutId: selection.workouts[0].id, bounds: selection.bounds }] }));
    expect(await within(dialog).findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(frame.style.getPropertyValue("--road-coverage-row-slots")).toBe("1");
    expect(within(dialog).queryByRole("button", { name: "Fit" })).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Close Road Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument();
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledWith([[-105.3, 39.9], [-105.2, 40.1]], { padding: 48, duration: 350 }));
    expect(map.getCanvas()).toHaveFocus();
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
    expect(highlightLayer.filter).toEqual(["==", ["get", "workoutId"], requested.id]);
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
    const focusButton = screen.getByRole("button", { name: /Running.*8\/08\/2026/ });
    const row = focusButton.closest("li")!;
    vi.useFakeTimers();
    fireEvent.pointerEnter(checkbox.closest("label")!);
    act(() => vi.advanceTimersByTime(250));
    expect(row).not.toHaveClass("is-hovered");
    fireEvent.pointerLeave(checkbox.closest("label")!);
    fireEvent.pointerEnter(focusButton);
    act(() => vi.advanceTimersByTime(249));
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(1));
    expect(row).toHaveClass("is-hovered");
    fireEvent.pointerEnter(checkbox.closest("label")!);
    expect(row).not.toHaveClass("is-hovered");
    fireEvent.pointerEnter(focusButton);
    act(() => vi.advanceTimersByTime(250));
    expect(row).toHaveClass("is-hovered");
    fireEvent.click(checkbox);
    expect(checkbox).not.toBeChecked();
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    fireEvent.pointerLeave(focusButton);
    fireEvent.pointerEnter(focusButton);
    act(() => vi.advanceTimersByTime(250));
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    fireEvent.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(row).not.toHaveClass("is-focused");
    const hikingButton = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ });
    const hikingRow = hikingButton.closest("li")!;
    fireEvent.pointerEnter(hikingButton);
    act(() => vi.advanceTimersByTime(250));
    expect(hikingRow).toHaveClass("is-hovered");
    expect(row).not.toHaveClass("is-hovered", "is-focused");
    // Crossing the inter-row gap leaves the button but remains inside the list.
    act(() => vi.advanceTimersByTime(500));
    expect(hikingRow).toHaveClass("is-hovered");
    fireEvent.pointerEnter(focusButton);
    act(() => vi.advanceTimersByTime(249));
    expect(hikingRow).toHaveClass("is-hovered");
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(1));
    expect(row).toHaveClass("is-hovered");
    expect(hikingRow).not.toHaveClass("is-hovered");
    fireEvent.pointerLeave(focusButton.closest("ol")!);
    expect(hikingRow).not.toHaveClass("is-hovered", "is-focused");
    expect(row).not.toHaveClass("is-hovered", "is-focused");
  });

  test("fits a clicked workout without making its checkbox the item action", async () => {
    const mixedConfig = { ...config, baseMaps: { ...baseMaps, workoutTypeMappings: baseMaps.workoutTypeMappings.map((mapping) => mapping.normalizedTypeKey === "hiking" ? { ...mapping, familyId: "road" } : mapping) } };
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={mixedConfig} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    const checkbox = await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledWith([[-105.3, 39.8], [-105.1, 40.1]], { padding: 48, duration: 350 }));
    const fitsBeforeToggle = map.fitBounds.mock.calls.length;
    const stylesBeforeToggle = map.setStyle.mock.calls.length;
    const user = userEvent.setup();
    await user.click(checkbox);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeToggle);
    await user.click(checkbox);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeToggle);
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeToggle);
    expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).not.toHaveClass("is-focused");
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    expect(map.fitBounds).toHaveBeenLastCalledWith([[-105.3, 39.9], [-105.2, 40.1]], { padding: 48, duration: 350 });
    expect(map.setStyle).toHaveBeenLastCalledWith("https://tiles.example.test/outdoor-dark.json", expect.objectContaining({ transformStyle: expect.any(Function) }));
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute("aria-keyshortcuts", "Space");
    expect(map.getLayer("private-workout-routes").paint["line-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    expect(map.getLayer("private-workout-route-markers").paint["circle-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    map.removeLayer.mockClear(); map.removeSource.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-routes", "line-opacity", 0));
    expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-markers", "circle-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-route-hover", "line-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-route-marker-hover", "circle-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-route-start", "circle-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("private-workout-route-finish", "text-opacity", 0);
    expect(map.setPaintProperty).not.toHaveBeenCalledWith("coverage-diagnostic-direction", "icon-opacity", 0);
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() => expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-routes", "line-opacity", 0.82));
    expect(map.setPaintProperty).toHaveBeenCalledWith("private-workout-route-markers", "circle-opacity", 0.9);
    for (const layer of ["private-workout-routes", "private-workout-route-markers", "private-workout-route-hover", "private-workout-route-marker-hover"]) {
      expect(map.setLayoutProperty).not.toHaveBeenCalledWith(layer, "visibility", "none");
      expect(map.removeLayer).not.toHaveBeenCalledWith(layer);
    }
    expect(map.removeSource).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await user.click(screen.getByRole("button", { name: "Routes" }));
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await user.click(checkbox);
    expect(new URL(window.location.href).searchParams.has("workoutId")).toBe(false);
    expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).not.toHaveClass("is-focused");
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
    await waitFor(() => expect(posted).toContainEqual({ dateRangeEnum: "last30Days", tz: "America/Denver", workoutIds: [selection.workouts[0].id], focusedWorkoutId: selection.workouts[0].id }));
    await waitFor(() => expect(map.removeSource).toHaveBeenCalledWith("private-workout-routes"));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "F".repeat(32))}`] });
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
    const map = mapInstances.at(-1)!;
    const stylesBeforeVisibilityChanges = map.setStyle.mock.calls.length;
    expect(bulk).toBeChecked();
    expect(bulk.closest(".map-route-toolbar")).toBeInTheDocument();
    expect(bulk.closest(".map-route-toggle")).toBeInTheDocument();
    expect(screen.queryByText("All routes")).not.toBeInTheDocument();

    await user.click(bulk);
    await waitFor(() => expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => !(checkbox as HTMLInputElement).checked)).toBe(true));
    expect(bulk).not.toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);

    await user.click(bulk);
    await waitFor(() => expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true));
    expect(bulk).toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);

    await user.click(screen.getByRole("checkbox", { name: /Show Running/ }));
    expect(bulk).not.toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);
    await user.click(bulk);
    expect(screen.getAllByRole("checkbox", { name: /Show / }).every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true);
    expect(bulk).toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);
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
    map.queryRenderedFeatures.mockReturnValue([{ properties: { workoutId: selection.workouts[0].id, sortOrder: 1 } }]);
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

	test("keeps direction markers above recreated magenta layers when changing focused routes", async () => {
		let selectionCount = 0;
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			if (path === "/api/map-selections" && method === "POST") {
				selectionCount++;
				return Promise.resolve(json({ ...selection,
					routeTileUrl: `/api/map-selections/${selection.id}/route-tiles/${selectionCount}/{z}/{x}/{y}.pbf`,
				}));
			}
			if (path.endsWith("/route/points") && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={config} preferences={{ ...preferences, coverageDiagnosticsEnabled: false }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const map = mapInstances.at(-1)!;
		for (const name of [/Running.*8\/08\/2026/, /Hiking.*8\/01\/2026/, /Running.*8\/08\/2026/]) {
			const previousSelections = selectionCount;
			await userEvent.click(screen.getByRole("button", { name }));
			// Changing the selected set still replaces the capability without losing overlay order.
			const otherRoute = name.source.startsWith("Running") ? /Show Hiking/ : /Show Running/;
			await userEvent.click(screen.getByRole("checkbox", { name: otherRoute }));
			await waitFor(() => expect(selectionCount).toBeGreaterThan(previousSelections));
			await waitFor(() => {
				const layers = [...map.layers.keys()];
				const direction = layers.indexOf("coverage-diagnostic-direction");
				expect(direction).toBeGreaterThan(layers.indexOf("private-workout-route-hover"));
				expect(layers).toContain("private-workout-route-marker-hover");
				expect(direction).toBeGreaterThan(layers.indexOf("private-workout-route-marker-hover"));
				expect(layers.indexOf("private-workout-route-finish")).toBeGreaterThan(direction);
				expect(layers.indexOf("private-workout-route-start")).toBeGreaterThan(direction);
			});
			await userEvent.click(screen.getByRole("checkbox", { name: otherRoute }));
		}
		expect(mapInstances).toHaveLength(1);
		expect(map.removeLayer).toHaveBeenCalledWith("private-workout-route-hover");
	});

	test("focuses selected raw routes without renewing capabilities or tearing down rendered layers", async () => {
		const sameFamilyConfig = { ...config, baseMaps: { ...baseMaps, fallbackFamilyId: "outdoor" } };
		const requests: string[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
			const path = String(input); const method = init?.method ?? "GET";
			requests.push(`${method} ${path}`);
			if (path === "/api/map-selections" && method === "POST") return Promise.resolve(json(selection));
			if (path.endsWith("/route/points") && method === "GET") return Promise.resolve(json(rawRoutePoints));
			if (method === "DELETE") return Promise.resolve(json(undefined, 204));
			throw new Error(`Unexpected request ${method} ${path}`);
		});
		render(<MapPage config={sameFamilyConfig} preferences={{ ...preferences, coverageDiagnosticsEnabled: false }} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
		await screen.findByRole("checkbox", { name: /Show Running/ });
		const map = mapInstances.at(-1)!;
		const routeSource = map.getSource("private-workout-routes");
		const routeLayer = map.getLayer("private-workout-routes");
		map.fitBounds.mockClear();
		map.removeSource.mockClear(); map.removeLayer.mockClear(); map.setStyle.mockClear();
		for (const [name, workout] of [[/Running.*8\/08\/2026/, selection.workouts[0]], [/Hiking.*8\/01\/2026/, selection.workouts[1]]] as const) {
			await userEvent.click(screen.getByRole("button", { name }));
			await waitFor(() => expect(map.getLayer("coverage-diagnostic-direction")).toBeTruthy());
			expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity-transition"]).toEqual({ duration: 0, delay: 0 });
			expect(map.fitBounds).toHaveBeenLastCalledWith(
				[[workout.bounds.minimumLongitude, workout.bounds.minimumLatitude], [workout.bounds.maximumLongitude, workout.bounds.maximumLatitude]],
				{ padding: 48, duration: 350 },
			);
			expect(map.setFilter).toHaveBeenCalledWith("private-workout-route-hover", ["==", ["get", "workoutId"], workout.id]);
			expect(map.getSource("private-workout-routes")).toBe(routeSource);
			expect(map.getLayer("private-workout-routes")).toBe(routeLayer);
		}
		expect(requests.filter((request) => request === "POST /api/map-selections")).toHaveLength(1);
		expect(map.removeSource).not.toHaveBeenCalledWith("private-workout-routes");
		expect(map.removeLayer).not.toHaveBeenCalledWith("private-workout-route-hover");
		expect(map.setStyle).not.toHaveBeenCalled();
		expect(mapInstances).toHaveLength(1);
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
		map.queryRenderedFeatures.mockReturnValue([{ properties: { workoutId: selection.workouts[1].id, sortOrder: 2 } }]);
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
    vi.useFakeTimers();
    act(() => map.handlers.get("error")?.());
    expect(screen.getByText("The public base map could not be loaded. Your private routes remain available. Retrying in 5 seconds...")).toBeInTheDocument();
    expect(map.setStyle).toHaveBeenCalledWith(expect.objectContaining({ version: 8 }));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] });
    mapBehavior.emitSetStyleLoad = true;
    act(() => vi.advanceTimersByTime(4999));
    expect(map.setStyle).not.toHaveBeenLastCalledWith("https://tiles.example.test/road-dark.json", expect.anything());
    act(() => vi.advanceTimersByTime(1));
    expect(map.setStyle).toHaveBeenLastCalledWith("https://tiles.example.test/road-dark.json", expect.objectContaining({ transformStyle: expect.any(Function) }));
    expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();
  });

  test("clears a pending base map retry when the provider style loads before the timer", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    vi.useFakeTimers();
    act(() => map.handlers.get("error")?.());
    expect(screen.getByText(/The public base map could not be loaded/)).toBeInTheDocument();
    const callsBeforeRecovery = map.setStyle.mock.calls.length;
    mapBehavior.emitSetStyleLoad = true;
    act(() => map.setStyle("https://tiles.example.test/road-dark.json"));
    expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();
    act(() => vi.advanceTimersByTime(5000));
    expect(map.setStyle).toHaveBeenCalledTimes(callsBeforeRecovery + 1);
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
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`] }));
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
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "E".repeat(32))}`] }));
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
		await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`] }));
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
