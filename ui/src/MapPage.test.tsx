import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  ApiError,
  SESSION_EXPIRED_EVENT,
  type BaseMapsConfig,
  type DateRangePreference,
  type MapSelection,
  type MapSelectionWorkout,
  type Preferences,
  type PublicConfig,
  type RoadCoverageDetail,
  type RoadCoverageEntity,
} from "./api";
import MapPage, {
  absoluteRouteTileTemplate,
  buildRawRouteDirectionMarkers,
  buildRawRouteEndpoints,
  buildRawRouteHighlight,
  buildSegmentedRawRoute,
  COVERAGE_HIGHLIGHT_DURATION_MS,
  CoverageCellValue,
  CoverageDetailCache,
  FocusGeometryCache,
  coverageRegionLabel,
  formatRoutePopupDetails,
  formatRoutePopupDistance,
  onMapContextReady,
  pointyDirectionMarkerImage,
  privateRouteTileUnauthorized,
  privateRouteTileUnavailable,
  requestedWorkoutIds,
  resolveBaseFamily,
  retryInitialDiagnosticBusy,
  routeColor,
  routeColors,
  routeEndpointOutline,
  selectionRequest,
  sortMapWorkouts,
  startCoverageHighlightBlink,
  type RoadCoverageCache,
} from "./MapPage";

const mapInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const popupInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const tileProtocols = vi.hoisted(
  () =>
    new Map<
      string,
      (request: { url: string }, controller: AbortController) => Promise<{ data: ArrayBuffer }>
    >(),
);
const mapBehavior = vi.hoisted(() => ({
  emitInitialStyleLoad: true,
  emitSetStyleLoad: true,
  styleLoaded: true,
  moving: false,
  dragging: false,
  tilesLoaded: true,
  directionSourceLoaded: true,
  routeSourceLoaded: true,
  rawFocusSourceLoaded: true,
  projectionScale: 10000,
}));
const originalGeolocation = Object.getOwnPropertyDescriptor(navigator, "geolocation");
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

vi.mock("maplibre-gl", () => ({
  default: (() => {
    class MockMap {
      layers = new Map<string, unknown>();
      sources = new Map<string, unknown>();
      handlers = new Map<string, (...args: never[]) => void>();
      canvas = Object.assign(document.createElement("canvas"), { tabIndex: 0 });
      addLayer = vi.fn((layer: { id: string }) => {
        this.layers.set(layer.id, { ...layer });
      });
      addSource = vi.fn((id: string, source: object) => {
        this.sources.set(id, {
          ...source,
          setTiles: vi.fn(),
          setData: vi.fn((_data: unknown, waitForCompletion?: boolean) =>
            waitForCompletion
              ? Promise.resolve().then(() => this.handlers.get("render")?.())
              : undefined,
          ),
        });
      });
      removeLayer = vi.fn((id: string) => {
        this.layers.delete(id);
      });
      removeSource = vi.fn((id: string) => {
        this.sources.delete(id);
      });
      getLayer = vi.fn((id: string) => this.layers.get(id));
      getSource = vi.fn((id: string) => this.sources.get(id));
      fitBounds = vi.fn();
      setFilter = vi.fn();
      setPaintProperty = vi.fn((id: string, property: string, value: unknown) => {
        const layer = this.layers.get(id) as { paint?: Record<string, unknown> } | undefined;
        if (layer) layer.paint = { ...layer.paint, [property]: value };
      });
      getPaintProperty = vi.fn(
        (id: string, property: string) =>
          (this.layers.get(id) as { paint?: Record<string, unknown> } | undefined)?.paint?.[
            property
          ],
      );
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
      setStyle = vi.fn((style: unknown) => {
        this.style =
          typeof style === "string"
            ? { layers: [] }
            : (style as { layers?: Array<{ id: string }> });
        if (typeof style !== "string") mapBehavior.styleLoaded = true;
        if (typeof style !== "string" || mapBehavior.emitSetStyleLoad)
          this.handlers.get("style.load")?.();
      });
      getStyle = vi.fn(() => this.style);
      isStyleLoaded = vi.fn(() => mapBehavior.styleLoaded);
      isMoving = vi.fn(() => mapBehavior.moving);
      getZoom = vi.fn(() => Math.log2(mapBehavior.projectionScale));
      getBearing = vi.fn(() => 0);
      getPitch = vi.fn(() => 0);
      getProjection = vi.fn(() => ({ type: "mercator" }));
      dragPan = { isActive: vi.fn(() => mapBehavior.dragging) };
      areTilesLoaded = vi.fn(() => mapBehavior.tilesLoaded);
      isSourceLoaded = vi.fn((id: string) =>
        id === "coverage-diagnostic-direction"
          ? mapBehavior.directionSourceLoaded
          : id === "private-workout-routes"
            ? mapBehavior.routeSourceLoaded
            : id === "private-workout-route-focus"
              ? mapBehavior.rawFocusSourceLoaded
              : true,
      );
      queryRenderedFeatures = vi.fn(() => []);
      project = vi.fn((coordinate: [number, number]) => ({
        x: coordinate[0] * mapBehavior.projectionScale,
        y: -coordinate[1] * mapBehavior.projectionScale,
      }));
      getCanvas = vi.fn(() => this.canvas);
      addControl = vi.fn();
      images = new Map<string, unknown>();
      hasImage = vi.fn((id: string) => this.images.has(id));
      addImage = vi.fn((id: string, image: unknown) => {
        this.images.set(id, image);
      });
      remove = vi.fn();
      on = vi.fn(
        (
          event: string,
          layerOrHandler: string | ((...args: never[]) => void),
          handler?: (...args: never[]) => void,
        ) => {
          const callback = typeof layerOrHandler === "function" ? layerOrHandler : handler!;
          const previous = this.handlers.get(event);
          this.handlers.set(
            event,
            previous
              ? (...args: never[]) => {
                  previous(...args);
                  callback(...args);
                }
              : callback,
          );
          if (event === "style.load" && mapBehavior.emitInitialStyleLoad) callback();
        },
      );
      off = vi.fn();
      constructor(public options: unknown) {
        this.style = (options as { style: { layers?: Array<{ id: string }> } }).style;
        (options as { container: HTMLElement }).container.append(this.canvas);
        mapInstances.push(this);
      }
    }
    return {
      Map: MockMap,
      addProtocol: vi.fn(
        (
          name: string,
          handler: (
            request: { url: string },
            controller: AbortController,
          ) => Promise<{ data: ArrayBuffer }>,
        ) => tileProtocols.set(name, handler),
      ),
      removeProtocol: vi.fn((name: string) => tileProtocols.delete(name)),
      NavigationControl: class NavigationControl {},
      Popup: class Popup {
        content?: HTMLElement;
        constructor() {
          popupInstances.push(this);
        }
        setLngLat = vi.fn(() => this);
        setDOMContent = vi.fn((content: HTMLElement) => {
          this.content = content;
          return this;
        });
        addTo = vi.fn(() => this);
        remove = vi.fn();
      },
    };
  })(),
}));

const baseMaps: BaseMapsConfig = {
  families: [
    {
      id: "outdoor",
      label: "Outdoor",
      styles: {
        light: "https://tiles.example.test/outdoor-light.json",
        dark: "https://tiles.example.test/outdoor-dark.json",
      },
      attribution: {
        text: "Outdoor map",
        links: [{ label: "Map data", url: "https://attribution.example.test" }],
      },
      resourceOrigins: ["https://tiles.example.test"],
    },
    {
      id: "road",
      label: "Road",
      styles: {
        light: "https://tiles.example.test/road-light.json",
        dark: "https://tiles.example.test/road-dark.json",
      },
      attribution: { text: "Road map", links: [] },
      resourceOrigins: ["https://tiles.example.test"],
    },
  ],
  fallbackFamilyId: "road",
  workoutTypeMappings: [
    { providerLabel: "Running", normalizedTypeKey: "running", familyId: "outdoor" },
    { providerLabel: "Hiking", normalizedTypeKey: "hiking", familyId: "outdoor" },
    { providerLabel: "Cycling", normalizedTypeKey: "cycling", familyId: "road" },
  ],
};
const config: PublicConfig = {
  productName: "Workouts Explorer",
  pollingIntervalSeconds: 30,
  mapFitPaddingPixels: 48,
  passwordMinimumLength: 12,
  pageSizeMaximum: 100,
  features: { coverageMatcherDiagnostics: false },
  baseMaps,
};
const preferences: Preferences = {
  theme: "dark",
  units: "metric",
  timezone: "America/Denver",
  firstWeekday: "monday",
  clockFormat: "24h",
  workoutColumns: ["date", "type"],
  pageSize: 25,
  customDateRangeLimit: 3,
  recentCustomDateRanges: [],
  coverageDiagnosticsEnabled: true,
  initialized: true,
  dateRange: "last30Days",
};
const selection: MapSelection = {
  id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
  dataGeneration: 7,
  coverageDisplayPolicy: "coverage-display-identity-v1",
  coverageDisplayRevision: 1,
  range: { startDate: "2026-07-10", endDate: "2026-08-08" },
  bounds: {
    minimumLongitude: -105.3,
    minimumLatitude: 39.8,
    maximumLongitude: -105.1,
    maximumLatitude: 40.1,
  },
  focusedWorkoutId: null,
  workouts: [
    {
      id: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
      type: { id: "11111111111111111111111111111111", key: "running", name: "Running" },
      startedAt: "2026-08-08T12:00:00Z",
      endedAt: "2026-08-08T13:45:00Z",
      duration: "6300",
      localStartDate: "2026-08-08",
      partialRoute: false,
      bounds: {
        minimumLongitude: -105.3,
        minimumLatitude: 39.9,
        maximumLongitude: -105.2,
        maximumLatitude: 40.1,
      },
      distance: { value: "8.25", unit: "km" },
      pace: { value: "5", unit: "min/km" },
      calories: { value: "500", unit: "kcal" },
      heartRate: { value: "120", unit: "count/min" },
      elevationGain: { value: "100", unit: "m" },
      coverageReadiness: {
        mapDataStatus: "ready",
        processingStatus: "current",
        resultStatus: "current",
      },
    },
    {
      id: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
      type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" },
      startedAt: "2026-08-01T12:00:00Z",
      endedAt: "2026-08-01T13:00:00Z",
      duration: "3600",
      localStartDate: "2026-08-01",
      partialRoute: true,
      bounds: {
        minimumLongitude: -105.2,
        minimumLatitude: 39.8,
        maximumLongitude: -105.1,
        maximumLatitude: 40,
      },
      distance: null,
      pace: null,
      calories: { value: "300", unit: "kcal" },
      heartRate: { value: "100", unit: "count/min" },
      elevationGain: { value: "250", unit: "m" },
      coverageReadiness: {
        mapDataStatus: "ready",
        processingStatus: "current",
        resultStatus: "current",
      },
    },
  ],
  routeTileUrl:
    "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/route-tiles/7/{z}/{x}/{y}.pbf",
  coverageTileUrl:
    "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/coverage-tiles/7/{z}/{x}/{y}.pbf",
};
const diagnostic = {
  id: "D".repeat(32),
  workoutId: selection.workouts[0].id,
  routeRevision: 2,
  rulesVersion: "coverage-experimental-v1",
  samplingVersion: "coverage-sampling-experimental-v1",
  pathPolicyVersion: "coverage-path-policy-experimental-v7",
  movementMode: "foot",
  minimumTraversalMeters: 5,
  outcome: "evaluated",
  counts: {
    originalPoints: 100,
    sampledPoints: 50,
    matchedPoints: 40,
    ambiguousPoints: 5,
    unmatchedPoints: 5,
    rejectedPoints: 0,
    traversals: 2,
    portions: 2,
    uniqueSegments: 2,
    durationMilliseconds: 1250,
  },
  generations: [],
  unavailableRegions: [],
  overlay: {
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        geometry: {
          type: "LineString",
          coordinates: [
            [-105.28, 39.91],
            [-105.24, 39.96],
          ],
        },
        properties: {
          portionOrdinal: 0,
          physicalSegmentId: "E".repeat(32),
          direction: "forward",
          regionId: "colorado",
          generation: 1,
          traversedMeters: 42.25,
          evidenceClass: "matched",
        },
      },
      {
        type: "Feature",
        geometry: {
          type: "LineString",
          coordinates: [
            [-105.24, 39.96],
            [-105.21, 40.02],
          ],
        },
        properties: {
          portionOrdinal: 1,
          physicalSegmentId: "F".repeat(32),
          direction: "reverse",
          regionId: "colorado",
          generation: 1,
          traversedMeters: 18,
          evidenceClass: "ambiguous",
        },
      },
    ],
  },
  labels: { overall: "uncertain", segments: [{ portionOrdinal: 0, label: "expected" }] },
  createdAt: "2026-08-08T14:00:00Z",
} as const;
const rawRoutePoints = {
  points: [
    { recordedAt: "2026-01-01T00:00:00Z", longitude: -105.29, latitude: 39.9 },
    { recordedAt: "2026-01-01T00:00:05Z", longitude: -105.2895, latitude: 39.9005 },
    { recordedAt: "2026-01-01T00:00:10Z", longitude: -105.289, latitude: 39.901 },
    { recordedAt: "2026-01-01T00:05:00Z", longitude: -105.2005, latitude: 40.0195 },
    { recordedAt: "2026-01-01T00:05:05Z", longitude: -105.2, latitude: 40.02 },
  ],
};
const rawRoute = {
  type: "Feature",
  geometry: {
    type: "MultiLineString",
    coordinates: [
      [
        [-105.29, 39.9],
        [-105.2895, 39.9005],
        [-105.289, 39.901],
      ],
      [
        [-105.2005, 40.0195],
        [-105.2, 40.02],
      ],
    ],
  },
  properties: {},
};
const rawRouteEndpoints = {
  type: "FeatureCollection",
  features: [
    {
      type: "Feature",
      geometry: { type: "Point", coordinates: [-105.29, 39.9] },
      properties: { kind: "start" },
    },
    {
      type: "Feature",
      geometry: { type: "Point", coordinates: [-105.2, 40.02] },
      properties: { kind: "finish" },
    },
  ],
};

beforeEach(() => {
  history.replaceState({}, "", "/map");
  mapInstances.splice(0);
  popupInstances.splice(0);
  mapBehavior.emitInitialStyleLoad = true;
  mapBehavior.emitSetStyleLoad = true;
  mapBehavior.styleLoaded = true;
  mapBehavior.moving = false;
  mapBehavior.dragging = false;
  mapBehavior.tilesLoaded = true;
  mapBehavior.directionSourceLoaded = true;
  mapBehavior.routeSourceLoaded = true;
  mapBehavior.rawFocusSourceLoaded = true;
  mapBehavior.projectionScale = 10000;
});
afterEach(() => {
  vi.useRealTimers();
  if (originalGeolocation) Object.defineProperty(navigator, "geolocation", originalGeolocation);
  else Object.defineProperty(navigator, "geolocation", { configurable: true, value: undefined });
  if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
});

function json(body: unknown, status = 200) {
  return status === 204
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
}

function privateTemplate(template: string) {
  const path = template.startsWith(window.location.origin)
    ? template.slice(window.location.origin.length)
    : template;
  return expect.stringMatching(
    new RegExp(
      `^workouts-private-\\d+://tiles/\\d+${path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`,
    ),
  );
}

describe("Coverage cell values", () => {
  afterEach(() => vi.unstubAllGlobals());

  test("only exposes the full text on hover when it overflows, including after resizing", () => {
    let width = 120;
    const text = "North Meadow Regional Preserve and Cedar Valley County Park";
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(120);
    let resized = () => {};
    const disconnect = vi.fn();
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: () => void) {
          resized = callback;
        }
        observe() {}
        disconnect = disconnect;
      },
    );
    const view = render(
      <button type="button">
        <CoverageCellValue text={text} />
      </button>,
    );
    const value = screen.getByText(text);
    const button = screen.getByRole("button", { name: text });
    expect(value).not.toHaveAttribute("title");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    button.focus();
    width = 119;
    act(resized);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip).toHaveTextContent(text);
    expect(tooltip).toHaveClass("tooltip-content--portal", "tooltip-content--single-line");
    expect(tooltip.parentElement).toBe(document.body);
    expect(value).not.toHaveAttribute("title");
    const trigger = button.querySelector<HTMLElement>(".tooltip-trigger")!;
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue({
      left: 40,
      right: 159,
      top: 80,
      bottom: 100,
      width: 119,
      height: 20,
    } as DOMRect);
    fireEvent.pointerEnter(trigger);
    expect(tooltip).toHaveClass("is-open");
    expect(button).toHaveFocus();
    width = 200;
    act(resized);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(value).not.toHaveAttribute("title");
    expect(within(view.container).getByText(text)).toBe(value);
    expect(button).toHaveFocus();
    view.unmount();
    expect(disconnect).toHaveBeenCalledOnce();
  });

  test("remeasures changed values and omits tooltips for fitting or hidden text", () => {
    let width = 100;
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return (this.textContent?.length ?? 0) * 8;
    });
    const long = "Example County and Surrounding Administrative Region";
    const view = render(<CoverageCellValue text={long} />);
    expect(screen.getByRole("tooltip")).toHaveTextContent(long);
    expect(within(view.container).getByText(long)).not.toHaveAttribute("title");
    view.rerender(<CoverageCellValue text="Oak Road" />);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(screen.getByText("Oak Road")).not.toHaveAttribute("title");
    view.rerender(<CoverageCellValue text={long} />);
    expect(screen.getByRole("tooltip")).toHaveTextContent(long);
    width = 0;
    fireEvent.resize(window);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    width = 100;
    fireEvent.resize(window);
    expect(screen.getByRole("tooltip")).toHaveTextContent(long);
  });
});

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
      const cleanup = startCoverageHighlightBlink(
        (opacity) => opacities.push(opacity),
        (handler) => {
          ready = handler;
          return () => undefined;
        },
      );
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
      let moving = true,
        tilesLoaded = false;
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
      moving = false;
      tilesLoaded = true;
      handlers.get("render")?.();
      expect(ready).toHaveBeenCalledTimes(1);
      cleanup();
      expect(handlers).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });

  test("builds enum and explicit selectors and canonicalizes compact workout IDs", () => {
    expect(
      selectionRequest("last7Days", "America/Denver", ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"]),
    ).toEqual({
      dateRangeEnum: "last7Days",
      tz: "America/Denver",
      workoutIds: ["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"],
    });
    expect(selectionRequest("last7Days", "America/Denver", [])).toEqual({
      dateRangeEnum: "last7Days",
      tz: "America/Denver",
      workoutIds: [],
    });
    expect(
      selectionRequest("last7Days", "America/Denver", ["A".repeat(32)], "A".repeat(32)),
    ).toEqual({
      dateRangeEnum: "last7Days",
      tz: "America/Denver",
      workoutIds: ["A".repeat(32)],
      focusedWorkoutId: "A".repeat(32),
    });
    expect(selectionRequest("2026-08-01/2026-08-08", "ignored")).toEqual({
      startDate: "2026-08-01",
      endDate: "2026-08-08",
    });
    expect(
      requestedWorkoutIds(
        "?workoutId=abcdefabcdefabcdefabcdefabcdefab&workoutId=bad&workoutIds=ABCDEFABCDEFABCDEFABCDEFABCDEFAB",
      ),
    ).toEqual(["ABCDEFABCDEFABCDEFABCDEFABCDEFAB"]);
  });

  test("uses a common mapped family only when every visible workout type resolves to it", () => {
    expect(resolveBaseFamily(baseMaps, selection.workouts)).toBe("outdoor");
    expect(
      resolveBaseFamily(baseMaps, [
        ...selection.workouts,
        {
          ...selection.workouts[0],
          id: "D".repeat(32),
          type: { id: "3".repeat(32), key: "cycling", name: "Cycling" },
        },
      ]),
    ).toBe("road");
    expect(
      resolveBaseFamily(baseMaps, [
        { ...selection.workouts[0], type: { id: "4".repeat(32), key: "unknown", name: "Unknown" } },
      ]),
    ).toBe("road");
    expect(
      resolveBaseFamily(baseMaps, [
        selection.workouts[0],
        { ...selection.workouts[1], type: { ...selection.workouts[1].type, key: "unknown" } },
      ]),
    ).toBe("road");
    expect(
      resolveBaseFamily(baseMaps, [
        { ...selection.workouts[0], type: { ...selection.workouts[0].type, name: "RUNNING" } },
      ]),
    ).toBe("outdoor");
    expect(
      routeColors([selection.workouts[0], { ...selection.workouts[0], id: "D".repeat(32) }]),
    ).toHaveLength(1);
    expect(
      absoluteRouteTileTemplate("/api/map/{z}/{x}/{y}.pbf", "https://workouts.example.test"),
    ).toBe("https://workouts.example.test/api/map/{z}/{x}/{y}.pbf");
    expect(
      privateRouteTileUnauthorized({ sourceId: "private-workout-routes", error: { status: 401 } }),
    ).toBe(true);
    expect(
      privateRouteTileUnauthorized({
        sourceId: "private-workout-coverage",
        error: { status: 401 },
      }),
    ).toBe(true);
    expect(
      privateRouteTileUnauthorized({
        error: {
          status: 401,
          url: `${window.location.origin}/api/map-selections/${"A".repeat(32)}/route-tiles/7/12/654/1583.pbf`,
        },
      }),
    ).toBe(true);
    expect(
      privateRouteTileUnauthorized({
        error: {
          status: 401,
          url: `${window.location.origin}/api/map-selections/${"A".repeat(32)}/coverage-tiles/7/12/654/1583.pbf`,
        },
      }),
    ).toBe(true);
    expect(
      privateRouteTileUnauthorized({
        error: { status: 401, url: "https://tiles.example.test/12/654/1583.pbf" },
      }),
    ).toBe(false);
    expect(
      privateRouteTileUnavailable(
        {
          sourceId: "private-workout-routes",
          error: {
            status: 404,
            url: `${window.location.origin}/api/map-selections/${selection.id}/route-tiles/7/12/654/1583.pbf`,
          },
        },
        selection.routeTileUrl,
      ),
    ).toBe(true);
    expect(
      privateRouteTileUnavailable(
        {
          error: {
            status: 404,
            url: `${window.location.origin}/api/map-selections/${selection.id}/route-tiles/7/12/654/1583.pbf`,
          },
        },
        selection.routeTileUrl,
      ),
    ).toBe(true);
    expect(
      privateRouteTileUnavailable(
        {
          sourceId: "private-workout-routes",
          error: {
            status: 404,
            url: `${window.location.origin}/api/map-selections/${"F".repeat(32)}/route-tiles/7/12/654/1583.pbf`,
          },
        },
        selection.routeTileUrl,
      ),
    ).toBe(false);
    const imperial = {
      ...preferences,
      clockFormat: "12h",
      units: "imperial",
      timezone: "America/Denver",
    } as const;
    expect(
      formatRoutePopupDetails("2026-04-20T22:30:00Z", "2026-04-21T01:45:00Z", imperial),
    ).toEqual({ date: "4/20/2026", timeRange: "4:30p - 7:45p" });
    expect(
      formatRoutePopupDistance(
        { ...selection.workouts[0], distance: { value: "13.276", unit: "km" } },
        "imperial",
      ),
    ).toBe("8.25 mi");
    expect(routeColor("walk", "Outdoor Walk")).toBe("#43d5e5");
    expect(routeColor("hike", "Hiking")).toBe("#8ed081");
    expect(routeColor("ride", "Outdoor Cycle")).toBe("#69aef5");
    expect(
      sortMapWorkouts(selection.workouts, { field: "type", direction: "asc" }).map(
        (workout) => workout.type.name,
      ),
    ).toEqual(["Hiking", "Running"]);
    expect(
      sortMapWorkouts(selection.workouts, { field: "duration", direction: "asc" }).map(
        (workout) => workout.id,
      ),
    ).toEqual([selection.workouts[1].id, selection.workouts[0].id]);
    expect(
      sortMapWorkouts(selection.workouts, { field: "distance", direction: "desc" }).map(
        (workout) => workout.id,
      ),
    ).toEqual([selection.workouts[0].id, selection.workouts[1].id]);
    expect(
      sortMapWorkouts(selection.workouts, { field: "elevationGain", direction: "desc" }).map(
        (workout) => workout.id,
      ),
    ).toEqual([selection.workouts[1].id, selection.workouts[0].id]);
    const calorieTie = [
      {
        ...selection.workouts[0],
        id: "A".repeat(32),
        startedAt: "2025-05-01T12:00:00Z",
        calories: { value: "500", unit: "kcal" },
      },
      {
        ...selection.workouts[0],
        id: "C".repeat(32),
        startedAt: "2025-07-24T12:00:00Z",
        calories: { value: "500", unit: "kcal" },
      },
      {
        ...selection.workouts[0],
        id: "B".repeat(32),
        startedAt: "2025-06-25T12:00:00Z",
        calories: { value: "500", unit: "kcal" },
      },
    ];
    expect(
      sortMapWorkouts(calorieTie, { field: "calories", direction: "desc" }).map((workout) =>
        workout.startedAt.slice(0, 10),
      ),
    ).toEqual(["2025-07-24", "2025-06-25", "2025-05-01"]);
  });

  test.each([
    ["Indoor", "Run", ["Run", "Running"]],
    ["Outdoor", "Run", ["Run", "Running"]],
    ["Indoor", "Walk", ["Walk", "Walking"]],
    ["Outdoor", "Walk", ["Walk", "Walking"]],
    ["Indoor", "Cycle", ["Cycle", "Cycling", "Bike", "Biking", "Bicycle", "Bicycling"]],
    ["Outdoor", "Cycle", ["Cycle", "Cycling", "Bike", "Biking", "Bicycle", "Bicycling"]],
  ] as const)(
    "resolves %s %s display aliases in either mapping direction",
    (prefix, activity, aliases) => {
      const canonicalName = `${prefix} ${activity}`;
      for (const alias of aliases) {
        const legacyKey = `legacy:${prefix}:${alias}`;
        const canonicalKey = `current:${prefix}:${activity}`;
        for (const label of [
          `${prefix} ${alias}`,
          `${prefix}${alias}`,
          ` \t${prefix.toUpperCase()}\u00a0 \n${alias.toLowerCase()} `,
        ]) {
          const legacyConfig = {
            ...baseMaps,
            workoutTypeMappings: [
              { providerLabel: label, normalizedTypeKey: legacyKey, familyId: "outdoor" },
            ],
          };
          const newWorkout = {
            ...selection.workouts[0],
            type: { ...selection.workouts[0].type, key: canonicalKey, name: canonicalName },
          };
          expect(resolveBaseFamily(legacyConfig, [newWorkout])).toBe("outdoor");
          const canonicalConfig = {
            ...baseMaps,
            workoutTypeMappings: [
              {
                providerLabel: canonicalName,
                normalizedTypeKey: canonicalKey,
                familyId: "outdoor",
              },
            ],
          };
          for (const name of [label, canonicalName]) {
            const oldWorkout = {
              ...newWorkout,
              type: { ...newWorkout.type, key: legacyKey, name },
            };
            expect(resolveBaseFamily(canonicalConfig, [oldWorkout])).toBe("outdoor");
            expect(oldWorkout.type).toEqual({
              ...selection.workouts[0].type,
              key: legacyKey,
              name,
            });
          }
          expect(legacyConfig.workoutTypeMappings[0]).toEqual({
            providerLabel: label,
            normalizedTypeKey: legacyKey,
            familyId: "outdoor",
          });
          expect(newWorkout.type).toEqual({
            ...selection.workouts[0].type,
            key: canonicalKey,
            name: canonicalName,
          });
        }
      }
    },
  );

  test("preserves configured Cycle families for both new and legacy raw keys", () => {
    const legacyConfig = {
      ...baseMaps,
      workoutTypeMappings: [
        {
          providerLabel: "Outdoor Cycling",
          normalizedTypeKey: "outdoor cycling",
          familyId: "outdoor",
        },
      ],
    };
    const canonicalConfig = {
      ...baseMaps,
      workoutTypeMappings: [
        { providerLabel: "Outdoor Cycle", normalizedTypeKey: "outdoor cycle", familyId: "outdoor" },
      ],
    };
    for (const maps of [legacyConfig, canonicalConfig]) {
      for (const key of ["outdoor cycle", "outdoor cycling"]) {
        const workout = {
          ...selection.workouts[0],
          type: { ...selection.workouts[0].type, key, name: "Outdoor Cycle" },
        };
        expect(resolveBaseFamily(maps, [workout])).toBe("outdoor");
      }
    }
  });

  test("normalizes qualified English labels with NFKC and Unicode whitespace", () => {
    const fullwidth =
      "\uff2f\uff55\uff54\uff44\uff4f\uff4f\uff52\u3000\uff23\uff59\uff43\uff4c\uff49\uff4e\uff47";
    for (const label of [fullwidth, "\u0085OUTDOOR\u0085CYCLING\u0085"]) {
      const maps = {
        ...baseMaps,
        workoutTypeMappings: [
          { providerLabel: label, normalizedTypeKey: "old-provider-key", familyId: "outdoor" },
        ],
      };
      expect(
        resolveBaseFamily(maps, [
          {
            ...selection.workouts[0],
            type: { ...selection.workouts[0].type, key: "new-provider-key", name: "Outdoor Cycle" },
          },
        ]),
      ).toBe("outdoor");
      maps.workoutTypeMappings[0].providerLabel = "Outdoor Cycle";
      expect(
        resolveBaseFamily(maps, [
          {
            ...selection.workouts[0],
            type: { ...selection.workouts[0].type, key: "new-provider-key", name: label },
          },
        ]),
      ).toBe("outdoor");
    }
  });

  test("prefers exact keys and falls back for conflicting display-alias families", () => {
    const mappings = [
      {
        providerLabel: "Outdoor Cycling",
        normalizedTypeKey: "outdoor cycling",
        familyId: "outdoor",
      },
      { providerLabel: "Outdoor Cycle", normalizedTypeKey: "outdoor cycle", familyId: "road" },
    ];
    for (const workoutTypeMappings of [mappings, [...mappings].reverse()]) {
      const maps = { ...baseMaps, workoutTypeMappings };
      for (const [key, family] of [
        ["outdoor cycling", "outdoor"],
        ["outdoor cycle", "road"],
        ["other-provider-key", "road"],
      ]) {
        expect(
          resolveBaseFamily(maps, [
            {
              ...selection.workouts[0],
              type: { ...selection.workouts[0].type, key, name: "Outdoor Cycle" },
            },
          ]),
        ).toBe(family);
      }
    }
    const sameFamily = {
      ...baseMaps,
      workoutTypeMappings: mappings.map((mapping) => ({ ...mapping, familyId: "outdoor" })),
    };
    expect(
      resolveBaseFamily(sameFamily, [
        {
          ...selection.workouts[0],
          type: { ...selection.workouts[0].type, key: "other-provider-key", name: "Outdoor Cycle" },
        },
      ]),
    ).toBe("outdoor");
  });

  test.each([
    "",
    "Running",
    "Walking",
    "Cycling",
    "Cycle",
    "Bike",
    "Bicycle",
    " Run ",
    "Climbing",
    "Outdoor Hiking",
    "Indoor Rowing",
    "Outdoor Run Intervals",
    "Indoor Cycling Class",
    "Outdoor-Cycling",
    "Cycling Outdoor",
    "Out doorRun",
    "Indoor Run ning",
    "Private provider label\t",
    "\uff23\uff59\uff43\uff4c\uff49\uff4e\uff47",
    "\ufeffOutdoor Cycle",
  ])("requires exact provider keys for unqualified or unknown label %j", (name) => {
    const maps = {
      ...baseMaps,
      workoutTypeMappings: [
        { providerLabel: name, normalizedTypeKey: "configured-provider-key", familyId: "outdoor" },
      ],
    };
    const workout = {
      ...selection.workouts[0],
      type: { ...selection.workouts[0].type, key: "different-provider-key", name },
    };
    expect(resolveBaseFamily(maps, [workout])).toBe("road");
    expect(
      resolveBaseFamily(maps, [
        { ...workout, type: { ...workout.type, key: "configured-provider-key" } },
      ]),
    ).toBe("outdoor");
  });

  test("falls back for missing, mixed, or unavailable alias families", () => {
    const maps = {
      ...baseMaps,
      workoutTypeMappings: [
        { providerLabel: "Outdoor Running", normalizedTypeKey: "legacy-run", familyId: "outdoor" },
        { providerLabel: "Outdoor Walking", normalizedTypeKey: "legacy-walk", familyId: "outdoor" },
        { providerLabel: "Outdoor Cycling", normalizedTypeKey: "legacy-cycle", familyId: "road" },
        {
          providerLabel: "Indoor Running",
          normalizedTypeKey: "legacy-indoor-run",
          familyId: "missing",
        },
      ],
    };
    const workouts = [
      "Outdoor Run",
      "Outdoor Walk",
      "Outdoor Cycle",
      "Indoor Run",
      "Indoor Walk",
    ].map((name) => ({
      ...selection.workouts[0],
      type: { ...selection.workouts[0].type, key: `new:${name}`, name },
    }));
    expect(resolveBaseFamily(maps, workouts.slice(0, 2))).toBe("outdoor");
    expect(resolveBaseFamily(maps, workouts.slice(0, 3))).toBe("road");
    expect(resolveBaseFamily(maps, [workouts[0], workouts[3]])).toBe("road");
    expect(resolveBaseFamily(maps, [workouts[0], workouts[4]])).toBe("road");
    expect(
      resolveBaseFamily(maps, [
        { ...workouts[3], type: { ...workouts[3].type, key: "legacy-indoor-run" } },
      ]),
    ).toBe("road");
    expect(resolveBaseFamily(maps, [])).toBe("road");
    expect(resolveBaseFamily({ ...maps, fallbackFamilyId: "missing" }, [workouts[4]])).toBe(
      "outdoor",
    );
    expect(resolveBaseFamily({ ...maps, families: [] }, [workouts[0]])).toBe("");
  });

  test("keeps canonical and legacy Cycle route colors keyed by provider identity", () => {
    const workouts = [
      ["outdoor cycle", "Outdoor Cycle"],
      ["outdoor cycling", "Outdoor Cycle"],
      ["old-cycling", "Outdoor Cycling"],
      ["indoor cycle", "Indoor Cycle"],
      ["old-bike", "Indoor Bike"],
    ].map(([key, name]) => ({
      ...selection.workouts[0],
      type: { ...selection.workouts[0].type, key, name },
    }));
    expect(routeColors(workouts)).toEqual(workouts.map((workout) => [workout.type.key, "#69aef5"]));
    expect(routeColor("outdoor run", "Outdoor Run")).toBe(
      routeColor("outdoor run", "Outdoor Running"),
    );
    expect(routeColor("outdoor walk", "Outdoor Walk")).toBe(
      routeColor("outdoor walk", "Outdoor Walking"),
    );
  });

  test("spaces smoothed raw-route direction markers in projected pixels", () => {
    const route = buildSegmentedRawRoute(
      Array.from({ length: 21 }, (_, i) => ({
        recordedAt: new Date(i * 1000).toISOString(),
        longitude: i / 10000,
        latitude: 0,
      })),
    )!;
    const lowZoom = buildRawRouteDirectionMarkers(
      route,
      ([x, y]) => ({ x: x * 50000, y: -y * 50000 }),
      40,
      3,
    );
    const highZoom = buildRawRouteDirectionMarkers(
      route,
      ([x, y]) => ({ x: x * 200000, y: -y * 200000 }),
      40,
      3,
    );
    expect(lowZoom.features.length).toBeGreaterThan(0);
    expect(highZoom.features.length).toBeGreaterThan(lowZoom.features.length);
    for (const feature of highZoom.features) expect(feature.properties.bearing).toBeCloseTo(90);
    const marker = pointyDirectionMarkerImage();
    expect([marker.width, marker.height]).toEqual([11, 15]);
    expect(marker.data[(1 * marker.width + 5) * 4 + 3]).toBe(255);
    expect(marker.data[13 * marker.width * 4 + 3]).toBe(0);
    expect(marker.data[(13 * marker.width + 5) * 4 + 3]).toBe(255);
  });

  test("cuts spatial jumps even before a timestamp baseline exists, preserving observed runs", () => {
    const points = [0, 0.0001, 0.1, 0.1001].map((longitude, i) => ({
      longitude,
      latitude: 0,
      recordedAt: new Date(i * 1000).toISOString(),
    }));
    const before = structuredClone(points);
    expect(buildSegmentedRawRoute(points)?.geometry.coordinates).toEqual([
      [
        [0, 0],
        [0.0001, 0],
      ],
      [
        [0.1, 0],
        [0.1001, 0],
      ],
    ]);
    expect(points).toEqual(before);
    expect(buildSegmentedRawRoute(points.slice(1, 3))).toBeUndefined();
  });

  test("drops isolated outlier line fragments without inventing links or removing stationary runs", () => {
    const points = [0, 0.0001, 0.1, 0.0002, 0.0003].map((longitude, i) => ({
      longitude,
      latitude: 0,
      recordedAt: new Date(i * 1000).toISOString(),
    }));
    expect(buildSegmentedRawRoute(points)?.geometry.coordinates).toEqual([
      [
        [0, 0],
        [0.0001, 0],
      ],
      [
        [0.0002, 0],
        [0.0003, 0],
      ],
    ]);
    const stationary = points.slice(0, 2).map((point) => ({ ...point, longitude: 0 }));
    expect(buildSegmentedRawRoute(stationary)?.geometry.coordinates).toEqual([
      [
        [0, 0],
        [0, 0],
      ],
    ]);
  });

  test("keeps fitted arrow density for dense GPS samples instead of discarding subpixel bearings", () => {
    const project = ([x, y]: number[]) => ({ x: x * 1048576, y: -y * 1048576 });
    for (const count of [61, 7681]) {
      const route = buildSegmentedRawRoute(
        Array.from({ length: count }, (_, i) => ({
          recordedAt: new Date(i * 1000).toISOString(),
          longitude: (0.00091552734375 * i) / (count - 1),
          latitude: 0,
        })),
      )!;
      const arrows = buildRawRouteDirectionMarkers(route, project);
      expect(arrows.features).toHaveLength(10);
      expect(buildRawRouteDirectionMarkers(route, project)).toEqual(arrows);
      for (const arrow of arrows.features) expect(arrow.properties.bearing).toBeCloseTo(90);
    }
  });
});

describe("exact coverage identity intent", () => {
  const entity: RoadCoverageEntity = {
    entityId: "E".repeat(32),
    entityKind: "path",
    name: "Exact Road",
    localityName: "Test City",
    regionId: "colorado",
    regionName: "Colorado",
    broadClass: "road",
    rangeWorkoutCount: 3,
    rangeFirstDate: "2026-07-12",
    rangeFirstWorkoutId: selection.workouts[1].id,
    rangeLatestDate: "2026-08-05",
    rangeLatestWorkoutId: selection.workouts[0].id,
    allTimeWorkoutCount: 7,
    allTimeFirstDate: "2026-01-01",
    allTimeFirstWorkoutId: selection.workouts[1].id,
    allTimeLatestDate: "2026-08-05",
    allTimeLatestWorkoutId: selection.workouts[0].id,
    bounds: selection.bounds!,
  };
  const park: RoadCoverageEntity = { ...entity, entityKind: "park", name: "Exact Park" };
  const detail: RoadCoverageDetail = {
    ...entity,
    geometry: {
      type: "LineString",
      coordinates: [
        [-105.2, 40],
        [-105.199, 40.001],
      ],
    },
    // Exact API fit bounds, including its minimum-span padding, not the table bounds.
    fitBounds: {
      minimumLongitude: -105.205,
      minimumLatitude: 39.995,
      maximumLongitude: -105.195,
      maximumLatitude: 40.005,
    },
  };
  const highlightSource = "private-workout-coverage-highlight";
  const highlightLayer = "private-workout-coverage-highlight";

  async function mountDetail(polling = false) {
    let activeSelection = selection;
    const requests: Array<{
      path: string;
      signal: AbortSignal;
      resolve: (response: Response) => void;
      reject: (error: Error) => void;
    }> = [];
    const lists: string[] = [];
    const posts: string[] = [];
    const cacheChanged = vi.fn();
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/map-selections" && init?.method === "POST") {
        posts.push(path);
        return Promise.resolve(json(activeSelection));
      }
      if (path.includes("/coverage/paths?")) {
        lists.push(path);
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 50000, totalItems: 2, totalPages: 1 },
            items: [entity, park],
          }),
        );
      }
      if (/\/coverage\/(path|park)\//.test(path))
        return new Promise<Response>((resolve, reject) => {
          requests.push({ path, signal: init!.signal as AbortSignal, resolve, reject });
        });
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`);
    });
    function Harness({ overrides = {} }: { overrides?: Partial<Parameters<typeof MapPage>[0]> }) {
      const [cache, setCache] = useState<RoadCoverageCache>();
      return (
        <MapPage
          config={config}
          preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          roadCoverageCache={cache}
          onRoadCoverageCacheChange={(next) => {
            cacheChanged(next);
            setCache(next);
          }}
          {...overrides}
        />
      );
    }
    const view = render(<Harness />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() => expect(screen.getByRole("button", { name: "Coverage" })).toBeEnabled());
    if (polling) vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    const open = async () => {
      await act(async () =>
        fireEvent.click(screen.getByRole("button", { name: "Coverage by road..." })),
      );
      if (polling) expect(screen.getByRole("button", { name: "Exact Road" })).toBeVisible();
      else await screen.findByRole("button", { name: "Exact Road" });
    };
    const choose = async (name = "Exact Road") => {
      await open();
      fireEvent.click(screen.getByRole("button", { name }));
    };
    const refresh = async (next: MapSelection) => {
      const old = activeSelection;
      activeSelection = next;
      await act(async () =>
        map.handlers.get("error")?.({
          error: {
            status: 404,
            url: `${window.location.origin}${old.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`,
          },
        }),
      );
    };
    return {
      ...view,
      map,
      requests,
      lists,
      posts,
      cacheChanged,
      open,
      choose,
      refresh,
      nextSelection: (next: MapSelection) => {
        activeSelection = next;
      },
      context: (overrides: Partial<Parameters<typeof MapPage>[0]>) =>
        view.rerender(<Harness overrides={overrides} />),
    };
  }

  test("dismisses and focuses immediately, shows cancellable loading, and waits for exact geometry and fit bounds", async () => {
    const { choose, requests, map, lists } = await mountDetail();
    const fits = map.fitBounds.mock.calls.length;
    await choose();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(map.getCanvas()).toHaveFocus();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Loading exact road coverage for Exact Road...",
    );
    expect(screen.getByRole("button", { name: "Cancel" })).toBeVisible();
    expect(map.fitBounds).toHaveBeenCalledTimes(fits);
    expect(map.getSource(highlightSource)).toBeUndefined();
    expect(requests).toHaveLength(1);
    expect(requests[0].path).toBe(
      `/api/map-selections/${selection.id}/coverage/path/${entity.entityId}?generation=7`,
    );
    await act(async () => requests[0].resolve(json(detail)));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(map.getSource(highlightSource).data.geometry).toEqual(detail.geometry);
    expect(map.fitBounds).toHaveBeenLastCalledWith(
      [
        [-105.205, 39.995],
        [-105.195, 40.005],
      ],
      { padding: 48, duration: 350 },
    );
    expect(lists).toHaveLength(1);
  });

  test("cancels loading and ignores late success without caching it", async () => {
    const { choose, requests, map } = await mountDetail();
    await choose();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(requests[0].signal.aborted).toBe(true);
    expect(map.getCanvas()).toHaveFocus();
    await act(async () => requests[0].resolve(json(detail)));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(map.getSource(highlightSource)).toBeUndefined();
    await choose();
    expect(requests).toHaveLength(2);
  });

  test.each([200, 503, 404])(
    "keeps B after out-of-order A (%s), without closing a reopened table or caching A",
    async (status) => {
      const { choose, open, requests, map, cacheChanged, posts } = await mountDetail();
      await choose();
      await choose("Exact Park");
      expect(requests[0].signal.aborted).toBe(true);
      const b = {
        ...detail,
        ...park,
        geometry: {
          ...detail.geometry,
          coordinates: [
            [1, 2],
            [3, 4],
          ],
        },
      };
      await act(async () => requests[1].resolve(json(b)));
      const fits = map.fitBounds.mock.calls.length;
      await open();
      const cacheWrites = cacheChanged.mock.calls.length;
      await act(async () =>
        requests[0].resolve(json(status === 200 ? detail : { title: "Failed" }, status)),
      );
      expect(screen.getByRole("dialog", { name: "Road Coverage" })).toBeVisible();
      expect(map.getSource(highlightSource).data.geometry).toEqual(b.geometry);
      expect(map.fitBounds).toHaveBeenCalledTimes(fits);
      expect(cacheChanged).toHaveBeenCalledTimes(cacheWrites);
      expect(posts).toHaveLength(1);
      fireEvent.click(screen.getByRole("button", { name: "Exact Road" }));
      expect(requests).toHaveLength(3);
    },
  );

  test.each([200, 503, 404])(
    "reopening cancels current detail and ignores its late completion (%s) without recovering the table",
    async (status) => {
      const { choose, open, requests, map, cacheChanged, posts, lists } = await mountDetail();
      await choose();
      const fits = map.fitBounds.mock.calls.length;
      const cacheWrites = cacheChanged.mock.calls.length;
      const postCount = posts.length;
      const bulkRequests = lists.length;
      await open();
      expect(requests[0].signal.aborted).toBe(true);
      expect(screen.queryByText(/Loading exact road coverage/)).not.toBeInTheDocument();
      await act(async () =>
        requests[0].resolve(json(status === 200 ? detail : { title: "Failed" }, status)),
      );
      expect(screen.getByRole("dialog", { name: "Road Coverage" })).toBeVisible();
      expect(screen.getByRole("table")).toBeVisible();
      expect(screen.queryByText(/That road coverage/)).not.toBeInTheDocument();
      expect(map.fitBounds).toHaveBeenCalledTimes(fits);
      expect(map.getSource(highlightSource)).toBeUndefined();
      expect(cacheChanged).toHaveBeenCalledTimes(cacheWrites);
      expect(posts).toHaveLength(postCount);
      expect(lists).toHaveLength(bulkRequests);
      fireEvent.click(screen.getByRole("button", { name: "Exact Road" }));
      expect(requests).toHaveLength(2);
      expect(requests[1].path).toBe(requests[0].path);
      expect(requests[1].signal.aborted).toBe(false);
      expect(screen.getByRole("status")).toHaveTextContent("Loading exact road coverage");
    },
  );

  test("reopening dismisses an existing detail error without recovering the table", async () => {
    const { choose, open, requests, cacheChanged, posts, lists } = await mountDetail();
    await choose();
    await act(async () => requests[0].resolve(json({ title: "Failed" }, 503)));
    expect(screen.getByRole("status")).toHaveTextContent("could not be shown on the map");
    const cacheWrites = cacheChanged.mock.calls.length;
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Close Road Coverage" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(cacheChanged).toHaveBeenCalledTimes(cacheWrites);
    expect(posts).toHaveLength(1);
    expect(lists).toHaveLength(1);
  });

  test.each(
    [
      "selection",
      "generation",
      "policy",
      "display revision",
      "resolved dates",
      "requested dates",
      "deletion",
      "mode",
      "diagnostics",
      "feature",
      "unmount",
    ].flatMap((change) => [200, 404].map((status) => [change, status] as const)),
  )("aborts on %s and ignores late response %s", async (change, status) => {
    const { choose, requests, map, context, refresh, unmount, posts, cacheChanged } =
      await mountDetail();
    await choose();
    if (change === "selection") await refresh({ ...selection, id: "D".repeat(32) });
    else if (change === "generation") await refresh({ ...selection, dataGeneration: 8 });
    else if (change === "policy")
      await refresh({ ...selection, coverageDisplayPolicy: "coverage-display-identity-v2" });
    else if (change === "display revision")
      await refresh({ ...selection, coverageDisplayRevision: 2 });
    else if (change === "resolved dates")
      await refresh({ ...selection, range: { startDate: "2026-07-11", endDate: "2026-08-07" } });
    else if (change === "requested dates") context({ dateRange: "last7Days" });
    else if (change === "deletion") context({ deletions: { revision: 1, events: [] } });
    else if (change === "mode") fireEvent.click(screen.getByRole("button", { name: "Routes" }));
    else if (change === "diagnostics") context({ preferences });
    else if (change === "feature")
      context({ config: { ...config, features: { coverageMatcherDiagnostics: true } } });
    else unmount();
    expect(requests[0].signal.aborted).toBe(true);
    const fits = map.fitBounds.mock.calls.length;
    const postCount = posts.length;
    const writes = cacheChanged.mock.calls.length;
    await act(async () =>
      requests[0].resolve(json(status === 200 ? detail : { title: "Missing" }, status)),
    );
    expect(map.fitBounds).toHaveBeenCalledTimes(fits);
    expect(map.getSource(highlightSource)).toBeUndefined();
    expect(screen.queryByText(/That road coverage/)).not.toBeInTheDocument();
    expect(posts).toHaveLength(postCount);
    expect(cacheChanged).toHaveBeenCalledTimes(writes);
  });

  test.each([503, "network", "timeout"] as const)(
    "shows a bounded map error for %s with manual Retry and Close, never caching failures",
    async (failure) => {
      const { choose, open, requests, map } = await mountDetail();
      await open();
      if (failure === "timeout") vi.useFakeTimers();
      fireEvent.click(screen.getByRole("button", { name: "Exact Road" }));
      if (failure === "timeout") {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(30_000);
        });
        expect(requests[0].signal.aborted).toBe(true);
        await act(async () => requests[0].resolve(json(detail)));
        vi.useRealTimers();
      } else
        await act(async () => {
          if (failure === "network") requests[0].reject(new Error("Offline"));
          else requests[0].resolve(json({ title: "Unavailable" }, failure));
        });
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent(
        "That road coverage could not be shown on the map.",
      );
      expect(requests).toHaveLength(1);
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));
      expect(requests).toHaveLength(2);
      await act(async () => requests[1].resolve(json({ title: "Unavailable" }, 503)));
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
      expect(map.getCanvas()).toHaveFocus();
      await choose();
      expect(requests).toHaveLength(3);
    },
  );

  test("reuses exact detail but restarts the same entity blink without repeated source uploads", async () => {
    const { choose, open, requests, map, lists } = await mountDetail();
    await choose();
    vi.useFakeTimers();
    await act(async () => requests[0].resolve(json(detail)));
    const source = map.getSource(highlightSource);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3001);
    });
    expect(map.getLayer(highlightLayer).paint["line-opacity"]).toBe(0);
    vi.useRealTimers();
    const fits = map.fitBounds.mock.calls.length;
    await open();
    expect(map.getSource(highlightSource)).toBe(source);
    expect(map.fitBounds).toHaveBeenCalledTimes(fits);
    fireEvent.click(screen.getByRole("button", { name: "Exact Road" }));
    expect(requests).toHaveLength(1);
    expect(lists).toHaveLength(1);
    expect(map.fitBounds).toHaveBeenCalledTimes(fits + 1);
    expect(map.getLayer(highlightLayer).paint["line-opacity"]).toBe(1);
    expect(source.setData).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    expect(source.setData).not.toHaveBeenCalled();
    await choose("Exact Park");
    expect(requests).toHaveLength(2);
    expect(requests[1].path).toContain(`/coverage/park/${entity.entityId}?`);
  });

  test("a current 404 invalidates both caches and offers a visible capability refresh without silently reopening the table", async () => {
    const { choose, requests, lists, posts, cacheChanged, nextSelection } = await mountDetail();
    await choose();
    await act(async () => requests[0].resolve(json(detail)));
    await choose("Exact Park");
    await act(async () => requests[1].resolve(json({ title: "Missing" }, 404)));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("changed or expired");
    expect(cacheChanged).toHaveBeenLastCalledWith(undefined);
    expect(posts).toHaveLength(1);
    const fresh = { ...selection, id: "D".repeat(32), dataGeneration: 8 };
    nextSelection(fresh);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByRole("button", { name: "Coverage by road..." });
    expect(posts).toHaveLength(2);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await choose();
    expect(lists).toHaveLength(2);
    expect(lists[1]).toContain(`/map-selections/${fresh.id}/coverage/paths?generation=8`);
    expect(requests).toHaveLength(3);
    expect(requests[2].path).toContain(
      `/map-selections/${fresh.id}/coverage/path/${entity.entityId}?generation=8`,
    );
  });

  test.each(["generation", "policy", "display revision", "dates", "deletion"])(
    "does not reuse completed detail across a %s change",
    async (change) => {
      const { choose, requests, refresh, context } = await mountDetail();
      await choose();
      await act(async () => requests[0].resolve(json(detail)));
      if (change === "generation") await refresh({ ...selection, dataGeneration: 8 });
      else if (change === "policy")
        await refresh({ ...selection, coverageDisplayPolicy: "coverage-display-identity-v2" });
      else if (change === "display revision")
        await refresh({ ...selection, coverageDisplayRevision: 2 });
      else if (change === "dates")
        await refresh({ ...selection, range: { ...selection.range, startDate: "2026-07-11" } });
      else context({ deletions: { revision: 1, events: [] } });
      await screen.findByRole("button", { name: "Coverage by road..." });
      await choose();
      expect(requests).toHaveLength(2);
      if (change === "generation") expect(requests[1].path).toContain("generation=8");
    },
  );

  test.each([
    ["policy", { coverageDisplayPolicy: "coverage-display-identity-v2" }],
    ["revision", { coverageDisplayRevision: 2 }],
  ] as const)(
    "automatically refreshes bulk and exact detail caches when only display %s changes",
    async (_name, metadata) => {
      const { choose, open, requests, lists, posts, cacheChanged, nextSelection } =
        await mountDetail(true);
      await choose();
      await act(async () => requests[0].resolve(json(detail)));
      await open();
      expect(lists).toHaveLength(1);
      const previousKey = cacheChanged.mock.calls.at(-1)![0].key;
      nextSelection({ ...selection, ...metadata });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(posts).toHaveLength(2);
      expect(lists).toHaveLength(2);
      expect(cacheChanged.mock.calls.at(-1)![0].key).not.toBe(previousKey);
      fireEvent.click(screen.getByRole("button", { name: "Exact Road" }));
      expect(requests).toHaveLength(2);
      expect(requests[1].path).toBe(requests[0].path);
    },
  );

  test.each([
    ["policy", { coverageDisplayPolicy: "coverage-display-identity-v2" }],
    ["revision", { coverageDisplayRevision: 2 }],
  ] as const)(
    "aborts pending detail on an automatic display %s refresh and ignores late success",
    async (_name, metadata) => {
      const { choose, requests, map, posts, nextSelection } = await mountDetail(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1000);
      });
      await choose();
      nextSelection({ ...selection, ...metadata });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(29_000);
      });
      expect(posts).toHaveLength(2);
      expect(requests[0].signal.aborted).toBe(true);
      const fits = map.fitBounds.mock.calls.length;
      await act(async () => requests[0].resolve(json(detail)));
      expect(map.fitBounds).toHaveBeenCalledTimes(fits);
      expect(map.getSource(highlightSource)).toBeUndefined();
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
    },
  );

  test("replays exact fit and blink after delayed style load, reinstalls changed sources, and preserves the readiness countdown", async () => {
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.moving = true;
    mapBehavior.tilesLoaded = false;
    const { choose, requests, map } = await mountDetail();
    await choose();
    const fits = map.fitBounds.mock.calls.length;
    await act(async () => requests[0].resolve(json(detail)));
    expect(map.getSource(highlightSource)).toBeUndefined();
    expect(map.fitBounds).toHaveBeenCalledTimes(fits);
    vi.useFakeTimers();
    act(() => map.handlers.get("style.load")?.());
    expect(map.getSource(highlightSource).data.geometry).toEqual(detail.geometry);
    expect(map.fitBounds).toHaveBeenCalledTimes(fits + 1);
    const source = map.getSource(highlightSource);
    act(() => map.handlers.get("style.load")?.());
    expect(source.setData).toHaveBeenCalledOnce();
    map.sources.delete(highlightSource);
    map.layers.delete(highlightLayer);
    act(() => map.handlers.get("style.load")?.());
    expect(map.getSource(highlightSource)).not.toBe(source);
    expect(map.getSource(highlightSource).data.geometry).toEqual(detail.geometry);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    map.setPaintProperty.mockClear();
    mapBehavior.moving = false;
    act(() => map.handlers.get("render")?.());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(
      map.setPaintProperty.mock.calls.filter(([id]: string[]) => id === highlightLayer).length,
    ).toBeGreaterThan(5);
    mapBehavior.tilesLoaded = true;
    act(() => map.handlers.get("render")?.());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(map.getLayer(highlightLayer).paint["line-opacity"]).toBe(0);
    map.setPaintProperty.mockClear();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(
      map.setPaintProperty.mock.calls.filter(([id]: string[]) => id === highlightLayer),
    ).toHaveLength(0);
  });

  test("shares the existing banner with base-map warnings and dismisses each independently", async () => {
    mapBehavior.emitSetStyleLoad = false;
    const { choose, requests, map } = await mountDetail();
    act(() => map.handlers.get("error")?.({ error: { status: 503 } }));
    await choose();
    expect(document.querySelectorAll(".map-banner")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent(
      "The public base map could not be loaded.",
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading exact road coverage");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss base map warning" }));
    expect(requests[0].signal.aborted).toBe(false);
    expect(screen.getByRole("status")).toHaveTextContent("Loading exact road coverage");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  test("bounds exact detail to eight LRU entries with a fixed five-minute TTL", () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(0);
    const cache = new CoverageDetailCache();
    for (let i = 0; i < 8; i++) cache.set(String(i), detail);
    expect(cache.get("0")).toBe(detail);
    cache.set("8", detail);
    expect(cache.get("1")).toBeUndefined();
    now.mockReturnValue(299_999);
    expect(cache.get("0")).toBe(detail);
    now.mockReturnValue(300_000);
    expect(cache.get("0")).toBeUndefined();
    cache.clear();
    expect(cache.get("8")).toBeUndefined();
  });

  test("bounds retained JSON to eight MiB using UTF-8 bytes and skips oversized details", () => {
    const cache = new CoverageDetailCache();
    const large = { ...detail, name: "\u00e9".repeat(3 * 1024 * 1024) };
    cache.set("large", large);
    expect(cache.get("large")).toBe(large);
    cache.set("next", { ...detail, name: "a".repeat(3 * 1024 * 1024) });
    expect(cache.get("large")).toBeUndefined();
    cache.set("oversized", { ...detail, name: "a".repeat(8 * 1024 * 1024) });
    expect(cache.get("oversized")).toBeUndefined();
    expect(cache.get("next")).toBeDefined();
  });
});

describe("coverage hover stats", () => {
  const entity: RoadCoverageEntity = {
    entityId: "E".repeat(32),
    entityKind: "path",
    name: "Hover Road",
    localityName: "Test City",
    regionId: "colorado",
    regionName: "Colorado",
    broadClass: "road",
    rangeWorkoutCount: 3,
    rangeFirstDate: "2026-07-12",
    rangeFirstWorkoutId: selection.workouts[1].id,
    rangeLatestDate: "2026-08-05",
    rangeLatestWorkoutId: selection.workouts[0].id,
    allTimeWorkoutCount: 7,
    allTimeFirstDate: "2026-01-01",
    allTimeFirstWorkoutId: selection.workouts[1].id,
    allTimeLatestDate: "2026-08-05",
    allTimeLatestWorkoutId: selection.workouts[0].id,
    bounds: selection.bounds!,
  };

  async function mountHover(response?: (path: string) => Promise<Response>) {
    let activeSelection = selection;
    const stats: Array<{ path: string; signal: AbortSignal }> = [];
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(activeSelection));
      if (path.includes("/stats?")) {
        stats.push({ path, signal: init!.signal as AbortSignal });
        return response ? response(path) : Promise.resolve(json(entity));
      }
      if (path.includes("/coverage/paths?"))
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 50000, totalItems: 1, totalPages: 1 },
            items: [entity],
          }),
        );
      if (path.includes(`/coverage/path/${entity.entityId}?`))
        return Promise.resolve(
          json({
            ...entity,
            geometry: {
              type: "LineString",
              coordinates: [
                [-105.2, 40],
                [-105.19, 40.01],
              ],
            },
            fitBounds: entity.bounds,
          }),
        );
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`);
    });
    const view = render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    vi.useFakeTimers();
    const move = (id = entity.entityId, kind = entity.entityKind) => {
      map.queryRenderedFeatures.mockImplementation(
        (_point: unknown, options: { layers: string[] }) =>
          options.layers.includes("private-workout-coverage")
            ? [
                {
                  properties: {
                    entityId: id.toLowerCase(),
                    entityKind: kind,
                    name: entity.name,
                    sourceEntityKind: kind,
                    sourceEntityId: id.toLowerCase(),
                  },
                },
              ]
            : [],
      );
      act(() =>
        map.handlers.get("mousemove")?.({
          point: { x: 50, y: 50 },
          lngLat: { lng: -105.2, lat: 40 },
        }),
      );
    };
    const leave = () => fireEvent.mouseLeave(map.getCanvas());
    const advance = async (ms = 750) => {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });
    };
    const refresh = async (next: MapSelection) => {
      const old = activeSelection;
      activeSelection = next;
      await act(async () => {
        map.handlers.get("error")?.({
          sourceId: "private-workout-routes",
          error: {
            status: 404,
            url: `${window.location.origin}${old.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`,
          },
        });
      });
    };
    return { ...view, map, stats, fetch, move, leave, advance, refresh };
  }

  test("fetches after 250ms, shows only after 750ms even when cached, and reserves detail for highlighting", async () => {
    const { move, leave, advance, stats, map, fetch, unmount } = await mountHover();
    const fits = map.fitBounds.mock.calls.length;
    move();
    await advance(249);
    expect(stats).toHaveLength(0);
    await advance(1);
    expect(stats.map(({ path }) => path)).toEqual([
      `/api/map-selections/${selection.id}/coverage/path/${entity.entityId}/stats?generation=7`,
    ]);
    expect(popupInstances).toHaveLength(0);
    move();
    await advance(499);
    expect(stats).toHaveLength(1);
    expect(popupInstances).toHaveLength(0);
    await advance(1);
    expect(popupInstances).toHaveLength(1);
    expect(popupInstances[0].content).toHaveTextContent("Hover Road");
    expect(map.fitBounds).toHaveBeenCalledTimes(fits);
    expect(map.getSource("private-workout-coverage-highlight")).toBeUndefined();
    expect(
      map.queryRenderedFeatures.mock.calls.some(([, options]: [unknown, { layers: string[] }]) =>
        options.layers.includes("private-workout-routes"),
      ),
    ).toBe(false);
    leave();
    expect(popupInstances[0].remove).toHaveBeenCalledOnce();
    move();
    await advance(749);
    expect(popupInstances).toHaveLength(1);
    await advance(1);
    expect(popupInstances).toHaveLength(2);
    expect(stats).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    await advance(0);
    fireEvent.click(screen.getByRole("button", { name: "Hover Road" }));
    await advance(0);
    expect(
      fetch.mock.calls.filter(([path]) =>
        String(path).includes(`/coverage/path/${entity.entityId}?`),
      ),
    ).toHaveLength(1);
    expect(map.getSource("private-workout-coverage-highlight")).toBeDefined();
    expect(stats).toHaveLength(1);
    unmount();
  });

  test("does not fetch while sweeping across identities for less than 250ms each", async () => {
    const { move, leave, advance, stats, unmount } = await mountHover();
    for (const id of ["A", "B", "C"]) {
      move(id.repeat(32));
      await advance(249);
      expect(stats).toHaveLength(0);
    }
    leave();
    await advance(1000);
    expect(stats).toHaveLength(0);
    expect(popupInstances).toHaveLength(0);
    unmount();
  });

  test("shows a slow response immediately when it arrives after the visibility dwell", async () => {
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => {
      resolve = done;
    });
    const { move, advance, stats, unmount } = await mountHover(() => pending);
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await advance(750);
    expect(popupInstances).toHaveLength(0);
    await act(async () => resolve(json(entity)));
    expect(popupInstances).toHaveLength(1);
    unmount();
  });

  test.each(["mouseleave", "blur", "hidden", "unmount"])(
    "keeps completed prefetches without showing a popup after %s",
    async (change) => {
      const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const { move, leave, advance, stats, unmount } = await mountHover();
      move();
      await advance(250);
      expect(stats).toHaveLength(1);
      expect(popupInstances).toHaveLength(0);
      if (change === "mouseleave") leave();
      else if (change === "blur") fireEvent(window, new Event("blur"));
      else if (change === "hidden") {
        visibility.mockReturnValue("hidden");
        fireEvent(document, new Event("visibilitychange"));
      } else unmount();
      expect(stats[0].signal.aborted).toBe(false);
      await advance(1000);
      expect(popupInstances).toHaveLength(0);
      if (change !== "unmount") {
        visibility.mockReturnValue("visible");
        move();
        await advance(250);
        expect(stats).toHaveLength(1);
        await advance(500);
        expect(popupInstances).toHaveLength(1);
        unmount();
      }
    },
  );

  test.each(["mouseleave", "identity", "blur", "hidden"])(
    "caches a late successful response after %s without restoring the popup",
    async (change) => {
      let resolve!: (response: Response) => void;
      const pending = new Promise<Response>((done) => {
        resolve = done;
      });
      const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const { move, leave, advance, stats, unmount } = await mountHover(() => pending);
      move();
      await advance(250);
      if (change === "mouseleave") leave();
      else if (change === "identity") move("D".repeat(32));
      else if (change === "blur") fireEvent(window, new Event("blur"));
      else {
        visibility.mockReturnValue("hidden");
        fireEvent(document, new Event("visibilitychange"));
      }
      expect(stats[0].signal.aborted).toBe(false);
      await act(async () => resolve(json(entity)));
      if (change === "identity") leave();
      await advance(1000);
      expect(popupInstances).toHaveLength(0);
      visibility.mockReturnValue("visible");
      move();
      await advance(749);
      expect(stats).toHaveLength(1);
      expect(popupInstances).toHaveLength(0);
      await advance(1);
      expect(popupInstances).toHaveLength(1);
      unmount();
    },
  );

  test("reuses an in-flight request on return but restarts the full visibility dwell", async () => {
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => {
      resolve = done;
    });
    const { move, leave, advance, stats, unmount } = await mountHover(() => pending);
    move();
    await advance(250);
    leave();
    await advance(100);
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await act(async () => resolve(json(entity)));
    await advance(499);
    expect(popupInstances).toHaveLength(0);
    await advance(1);
    expect(popupInstances).toHaveLength(1);
    expect(stats).toHaveLength(1);
    unmount();
  });

  test("overlaps a successful busy retry without showing before 750ms", async () => {
    let attempts = 0;
    const { move, advance, stats, unmount } = await mountHover(() =>
      Promise.resolve(++attempts === 1 ? json({ title: "Busy" }, 503) : json(entity)),
    );
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await advance(249);
    expect(stats).toHaveLength(1);
    await advance(1);
    expect(stats).toHaveLength(2);
    expect(popupInstances).toHaveLength(0);
    await advance(249);
    expect(popupInstances).toHaveLength(0);
    await advance(1);
    expect(popupInstances).toHaveLength(1);
    unmount();
  });

  test("keeps an old identity's prefetch and restarts both deadlines for its replacement", async () => {
    const { move, leave, advance, stats, unmount } = await mountHover();
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await advance(100);
    move("D".repeat(32));
    expect(stats[0].signal.aborted).toBe(false);
    await advance(249);
    expect(stats).toHaveLength(1);
    await advance(1);
    expect(stats).toHaveLength(2);
    expect(stats[1].path).toContain(`/coverage/path/${"D".repeat(32)}/stats?`);
    await advance(499);
    expect(popupInstances).toHaveLength(0);
    await advance(1);
    expect(popupInstances).toHaveLength(1);
    leave();
    move();
    await advance(250);
    expect(stats).toHaveLength(2);
    unmount();
  });

  test.each([
    ["selection", { ...selection, id: "D".repeat(32) }],
    ["generation", { ...selection, dataGeneration: selection.dataGeneration + 1 }],
    ["policy", { ...selection, coverageDisplayPolicy: "coverage-display-identity-v2" }],
    ["display revision", { ...selection, coverageDisplayRevision: 2 }],
    ["dates", { ...selection, range: { startDate: "2026-07-11", endDate: "2026-08-08" } }],
  ])("invalidates cache and removes visible popups on %s changes", async (_name, next) => {
    const { move, advance, refresh, stats, unmount } = await mountHover();
    move();
    await advance();
    await refresh(next);
    expect(popupInstances[0].remove).toHaveBeenCalledOnce();
    move();
    await advance();
    expect(stats).toHaveLength(2);
    expect(stats[1].path).toBe(
      `/api/map-selections/${next.id}/coverage/path/${entity.entityId}/stats?generation=${next.dataGeneration}`,
    );
    unmount();
  });

  test.each(["selection", "generation", "policy", "display revision", "dates", "unmount"])(
    "cancels pending stats and ignores late success after %s",
    async (change) => {
      let resolve!: (response: Response) => void;
      const pending = new Promise<Response>((done) => {
        resolve = done;
      });
      const { move, leave, advance, refresh, stats, unmount } = await mountHover(() => pending);
      move();
      await advance(250);
      expect(stats).toHaveLength(1);
      leave();
      expect(stats[0].signal.aborted).toBe(false);
      if (change === "unmount") unmount();
      else
        await refresh({
          ...selection,
          ...(change === "selection"
            ? { id: "D".repeat(32) }
            : change === "generation"
              ? { dataGeneration: 8 }
              : change === "policy"
                ? { coverageDisplayPolicy: "coverage-display-identity-v2" }
                : change === "display revision"
                  ? { coverageDisplayRevision: 2 }
                  : { range: { startDate: "2026-07-11", endDate: "2026-08-08" } }),
        });
      expect(stats[0].signal.aborted).toBe(true);
      await act(async () => resolve(json(entity)));
      expect(popupInstances).toHaveLength(0);
      if (change !== "unmount") {
        move();
        await advance();
        expect(stats).toHaveLength(2);
        unmount();
      }
    },
  );

  test.each(["named path", "regional park path"])(
    "prioritizes the source %s but requests its canonical park identity",
    async (priority) => {
      const { map, advance, stats, unmount } = await mountHover();
      const canonicalID = "C".repeat(32);
      const sourceID = "9".repeat(32);
      const features = [
        {
          properties: {
            entityKind: "park",
            entityId: "D".repeat(32),
            sourceEntityKind: "park",
            sourceEntityId: "B".repeat(32),
            name: "Regional Park",
          },
        },
        {
          properties: {
            entityKind: "park",
            entityId: canonicalID.toLowerCase(),
            sourceEntityKind: "path",
            sourceEntityId: sourceID,
            name: priority === "named path" ? "Shared Place" : null,
            localityName: priority === "regional park path" ? "Regional Park" : "Test City",
          },
        },
      ];
      map.queryRenderedFeatures.mockImplementation(
        (_point: unknown, options: { layers: string[] }) =>
          options.layers.includes("private-workout-coverage") ? features : [],
      );
      act(() =>
        map.handlers.get("mousemove")?.({
          point: { x: 50, y: 50 },
          lngLat: { lng: -105.2, lat: 40 },
        }),
      );
      await advance();
      expect(stats.map(({ path }) => path)).toEqual([
        `/api/map-selections/${selection.id}/coverage/park/${canonicalID}/stats?generation=7`,
      ]);
      expect(stats[0].path).not.toContain(sourceID);
      unmount();
    },
  );

  test.each(["blur", "hidden"])(
    "cancels dwell and removes visible popups on %s without discarding valid cached stats",
    async (change) => {
      const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const { move, advance, stats, unmount } = await mountHover();
      const cancel = () => {
        if (change === "blur") fireEvent(window, new Event("blur"));
        else {
          visibility.mockReturnValue("hidden");
          fireEvent(document, new Event("visibilitychange"));
        }
      };
      move();
      await advance(249);
      cancel();
      await advance(1000);
      expect(stats).toHaveLength(0);
      expect(popupInstances).toHaveLength(0);
      visibility.mockReturnValue("visible");
      move();
      await advance();
      expect(stats).toHaveLength(1);
      expect(popupInstances).toHaveLength(1);
      fireEvent(document, new Event("visibilitychange"));
      expect(popupInstances[0].remove).not.toHaveBeenCalled();
      cancel();
      expect(popupInstances[0].remove).toHaveBeenCalledOnce();
      visibility.mockReturnValue("visible");
      move();
      await advance(749);
      expect(popupInstances).toHaveLength(1);
      await advance(1);
      expect(popupInstances).toHaveLength(2);
      expect(stats).toHaveLength(1);
      unmount();
    },
  );

  test("cancels the busy retry when hidden and cleans up focus and visibility listeners", async () => {
    const addWindowListener = vi.spyOn(window, "addEventListener");
    const addDocumentListener = vi.spyOn(document, "addEventListener");
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const { move, advance, stats, map, unmount } = await mountHover(() =>
      Promise.resolve(json({ title: "Busy" }, 503)),
    );
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await advance(249);
    visibility.mockReturnValue("hidden");
    fireEvent(document, new Event("visibilitychange"));
    await advance(1000);
    expect(stats).toHaveLength(1);
    expect(popupInstances).toHaveLength(0);
    const removeWindowListener = vi.spyOn(window, "removeEventListener");
    const removeDocumentListener = vi.spyOn(document, "removeEventListener");
    const mapListeners = [...map.on.mock.calls];
    unmount();
    expect(removeWindowListener).toHaveBeenCalledWith(
      "blur",
      addWindowListener.mock.calls.find(([event]) => event === "blur")![1],
    );
    expect(removeDocumentListener).toHaveBeenCalledWith(
      "visibilitychange",
      addDocumentListener.mock.calls.find(([event]) => event === "visibilitychange")![1],
    );
    for (const args of mapListeners) expect(map.off).toHaveBeenCalledWith(...args);
  });

  test("cancels dwell and retry timers on canvas exit without leaking its listener", async () => {
    const addListener = vi.spyOn(HTMLCanvasElement.prototype, "addEventListener");
    const { move, leave, advance, stats, map, unmount } = await mountHover(() =>
      Promise.resolve(json({ title: "Busy" }, 503)),
    );
    move();
    await advance(249);
    leave();
    await advance(1);
    expect(stats).toHaveLength(0);
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    leave();
    await advance(250);
    expect(stats).toHaveLength(1);
    const removeListener = vi.spyOn(map.getCanvas(), "removeEventListener");
    unmount();
    expect(removeListener).toHaveBeenCalledWith(
      "mouseleave",
      addListener.mock.calls.find(([event]) => event === "mouseleave")![1],
    );
    expect(popupInstances).toHaveLength(0);
  });

  test("bounds busy retries to one at 250ms and does not cache failures", async () => {
    const { move, leave, advance, stats, unmount } = await mountHover(() =>
      Promise.resolve(json({ title: "Busy" }, 503)),
    );
    move();
    await advance(250);
    expect(stats).toHaveLength(1);
    await advance(249);
    expect(stats).toHaveLength(1);
    await advance(1);
    expect(stats).toHaveLength(2);
    await advance(1000);
    expect(stats).toHaveLength(2);
    expect(popupInstances).toHaveLength(0);
    leave();
    move();
    await advance(250);
    expect(stats).toHaveLength(3);
    unmount();
  });

  test("refreshes the selection on a current 404 but ignores a late 404 after exit", async () => {
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => {
      resolve = done;
    });
    let attempts = 0;
    const { move, leave, advance, stats, fetch, unmount } = await mountHover(() =>
      ++attempts === 1 ? pending : Promise.resolve(json({ title: "Missing" }, 404)),
    );
    const posts = () =>
      fetch.mock.calls.filter(
        ([path, init]) => path === "/api/map-selections" && init?.method === "POST",
      ).length;
    move();
    await advance();
    leave();
    await act(async () => resolve(json({ title: "Missing" }, 404)));
    expect(posts()).toBe(1);
    move();
    await advance();
    expect(stats).toHaveLength(2);
    expect(posts()).toBe(2);
    expect(popupInstances).toHaveLength(0);
    unmount();
  });

  test("expires stats after five minutes without extending TTL on cache hits", async () => {
    const { move, leave, advance, stats, unmount } = await mountHover();
    move();
    await advance();
    leave();
    await advance(4 * 60_000);
    move();
    await advance();
    expect(stats).toHaveLength(1);
    leave();
    await advance(60_000);
    move();
    await advance();
    expect(stats).toHaveLength(2);
    unmount();
  });

  test("bounds the cache to 128 LRU entries and distinguishes entity kinds", async () => {
    const { move, leave, advance, stats, unmount } = await mountHover();
    const id = (index: number) => index.toString(16).padStart(32, "0").toUpperCase();
    for (let index = 0; index < 128; index++) {
      move(id(index));
      await advance();
      leave();
    }
    expect(stats).toHaveLength(128);
    move(id(0));
    await advance();
    leave();
    move(id(128));
    await advance();
    leave();
    move(id(0));
    await advance();
    leave();
    expect(stats).toHaveLength(129);
    move(id(1));
    await advance();
    leave();
    expect(stats).toHaveLength(130);
    move(id(1), "park");
    await advance();
    expect(stats).toHaveLength(131);
    expect(stats.at(-1)!.path).toContain(`/coverage/park/${id(1)}/stats?`);
    unmount();
  });
});

describe("Show on map coverage fallback", () => {
  const target = selection.workouts[0];
  const none: MapSelectionWorkout["coverageReadiness"] = {
    mapDataStatus: "pending",
    processingStatus: "unprocessed",
    resultStatus: "none",
  };

  async function mountRequested({
    readiness = none,
    cachedReadiness = target.coverageReadiness,
    requested = true,
    deferred = false,
    deferPost = 1,
    diagnostics = false,
    absent = false,
  }: {
    readiness?: MapSelectionWorkout["coverageReadiness"];
    cachedReadiness?: MapSelectionWorkout["coverageReadiness"];
    requested?: boolean;
    deferred?: boolean;
    deferPost?: number;
    diagnostics?: boolean;
    absent?: boolean;
  } = {}) {
    history.replaceState({}, "", requested ? `/map?workoutId=${target.id.toLowerCase()}` : "/map");
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => {
      resolve = done;
    });
    const bodies: unknown[] = [];
    const fresh = {
      ...selection,
      focusedWorkoutId: target.id,
      workouts: absent
        ? [selection.workouts[1]]
        : [
            { ...target, id: target.id.toLowerCase(), coverageReadiness: readiness },
            selection.workouts[1],
          ],
    };
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/map-selections" && init?.method === "POST") {
        bodies.push(JSON.parse(String(init.body)));
        return deferred && bodies.length === deferPost ? pending : Promise.resolve(json(fresh));
      }
      if (path.endsWith("/route/points")) return Promise.resolve(json(rawRoutePoints));
      if (path.endsWith("/coverage-diagnostic-runs")) return Promise.resolve(json(diagnostic, 201));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`);
    });
    const modeChange = vi.fn();
    function GroupRoot() {
      const [mode, setMode] = useState<"coverage" | "routes">("coverage");
      const [ids, setIds] = useState<string[] | undefined>(selection.workouts.map(({ id }) => id));
      const [rows, setRows] = useState([
        { ...target, coverageReadiness: cachedReadiness },
        selection.workouts[1],
      ]);
      const [focus, setFocus] = useState<string | undefined>(target.id);
      return (
        <MapPage
          config={{ ...config, features: { coverageMatcherDiagnostics: diagnostics } }}
          preferences={{ ...preferences, coverageDiagnosticsEnabled: diagnostics }}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          persistedMapMode={mode}
          onMapModeChange={(next) => {
            modeChange(next);
            setMode(next);
          }}
          persistedWorkoutIds={ids}
          onWorkoutSelectionChange={setIds}
          persistedAvailableWorkouts={rows}
          onAvailableWorkoutsChange={setRows}
          persistedFocusedWorkoutId={focus}
          onFocusedWorkoutChange={setFocus}
          availableWorkoutsLoaded
        />
      );
    }
    render(<GroupRoot />);
    await waitFor(() => expect(bodies).toHaveLength(1));
    return {
      bodies,
      fetch,
      modeChange,
      resolve: () => resolve(json(fresh)),
      map: mapInstances.at(-1)!,
    };
  }

  test.each([
    [
      "unknown map",
      { mapDataStatus: "pending", processingStatus: "unprocessed", resultStatus: "none" },
      "Routes",
    ],
    [
      "unprocessed",
      { mapDataStatus: "ready", processingStatus: "unprocessed", resultStatus: "none" },
      "Routes",
    ],
    [
      "queued",
      { mapDataStatus: "ready", processingStatus: "queued", resultStatus: "none" },
      "Routes",
    ],
    [
      "running",
      { mapDataStatus: "ready", processingStatus: "running", resultStatus: "none" },
      "Routes",
    ],
    [
      "failed",
      { mapDataStatus: "ready", processingStatus: "failed", resultStatus: "none" },
      "Routes",
    ],
    [
      "unavailable map",
      { mapDataStatus: "unavailable", processingStatus: "failed", resultStatus: "none" },
      "Routes",
    ],
    [
      "current",
      { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" },
      "Coverage",
    ],
    [
      "stale",
      { mapDataStatus: "ready", processingStatus: "stale", resultStatus: "stale" },
      "Coverage",
    ],
    [
      "stale applied result after failure",
      { mapDataStatus: "unavailable", processingStatus: "failed", resultStatus: "stale" },
      "Coverage",
    ],
  ] as const)(
    "uses %s fresh readiness for the requested target",
    async (_name, readiness, mode) => {
      const { bodies, modeChange, map } = await mountRequested({ readiness });
      await waitFor(() =>
        expect(screen.getByRole("button", { name: mode })).toHaveAttribute("aria-pressed", "true"),
      );
      await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
      expect(map.fitBounds).toHaveBeenLastCalledWith(
        [
          [-105.3, 39.9],
          [-105.2, 40.1],
        ],
        { padding: 48, duration: 350 },
      );
      expect(screen.getByRole("checkbox", { name: /Show Running/ })).toBeChecked();
      expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
      expect(
        screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li"),
      ).toHaveClass("is-focused");
      expect(map.getLayer("private-workout-routes").layout.visibility).toBe(
        mode === "Routes" ? "visible" : "none",
      );
      expect(modeChange.mock.calls).toEqual(mode === "Routes" ? [["routes"]] : []);
      expect(bodies).toEqual([
        {
          dateRangeEnum: "last30Days",
          tz: "America/Denver",
          workoutIds: selection.workouts.map(({ id }) => id),
          focusedWorkoutId: target.id,
        },
      ]);
    },
  );

  test("fresh current readiness outranks cached none", async () => {
    const { modeChange, map, bodies } = await mountRequested({
      readiness: target.coverageReadiness,
      cachedReadiness: none,
    });
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(modeChange).not.toHaveBeenCalled();
    expect(bodies).toHaveLength(1);
  });

  test("manual Coverage after fallback survives subsequent capability refresh without another fit", async () => {
    const { bodies, modeChange, map } = await mountRequested();
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    expect(bodies).toHaveLength(1);
    await userEvent.click(screen.getByRole("checkbox", { name: /Show Hiking/ }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(modeChange.mock.calls).toEqual([["routes"], ["coverage"]]);
    expect(map.fitBounds).toHaveBeenCalledTimes(1);
  });

  test("persisted focus without a URL request does not force Routes", async () => {
    const { modeChange, map } = await mountRequested({ requested: false });
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(modeChange).not.toHaveBeenCalled();
  });

  test("respects the user's Routes choice while capability creation is in flight", async () => {
    const { resolve, modeChange, map, bodies } = await mountRequested({ deferred: true });
    await userEvent.click(screen.getByRole("button", { name: "Routes" }));
    await act(async () => resolve());
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    expect(modeChange.mock.calls).toEqual([["routes"]]);
    expect(screen.getByRole("button", { name: "Routes" })).toHaveAttribute("aria-pressed", "true");
    expect(bodies).toHaveLength(1);
  });

  test("diagnostics starts in Routes and preserves manually selected raw Coverage on refresh", async () => {
    const { resolve, map, bodies, modeChange } = await mountRequested({
      diagnostics: true,
      deferred: true,
      deferPost: 2,
    });
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "Routes" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(bodies).toHaveLength(1);
    await waitFor(() => expect(map.sources.has("coverage-diagnostic-overlay")).toBe(true));
    const fitsBeforeRefresh = map.fitBounds.mock.calls.length;
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: {
          status: 404,
          url: `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`,
        },
      }),
    );
    await waitFor(() => expect(bodies).toHaveLength(2));
    await act(async () => resolve());
    await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(modeChange).not.toHaveBeenCalled();
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeRefresh);
    expect(bodies).toHaveLength(2);
  });

  test("does not fit cached target bounds when the fresh capability omits the target", async () => {
    const { map, modeChange } = await mountRequested({ absent: true });
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    expect(map.fitBounds).toHaveBeenLastCalledWith(
      [
        [-105.3, 39.8],
        [-105.1, 40.1],
      ],
      { padding: 48, duration: 350 },
    );
    expect(modeChange).not.toHaveBeenCalled();
  });
});

describe("private tile recovery", () => {
  async function mountRecovery(
    selections = [selection],
    respond: (url: string, init?: RequestInit) => Promise<Response> = async () =>
      new Response(null, { status: 204 }),
  ) {
    let posts = 0;
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(selections[Math.min(posts++, selections.length - 1)]));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      if (String(input).startsWith(window.location.origin + "/api/map-selections/"))
        return respond(String(input), init);
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    const props = {
      config,
      preferences: { ...preferences, coverageDiagnosticsEnabled: false },
      csrfToken: "csrf-map",
      dateRange: "last30Days" as DateRangePreference,
      onDateRangeSelected: vi.fn(),
    };
    const view = render(<MapPage {...props} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.sources.has("private-workout-coverage")).toBe(true));
    vi.useFakeTimers();
    const load = (
      kind: "routes" | "coverage",
      x = 654,
      controller = new AbortController(),
      template?: string,
    ) => {
      const url = (template ?? map.sources.get(`private-workout-${kind}`).tiles[0]).replace(
        "{z}/{x}/{y}",
        `12/${x}/1583`,
      );
      const result = tileProtocols.get(url.split(":")[0])!({ url }, controller);
      void result.catch(() => undefined);
      return result;
    };
    return { view, props, map, fetch, load };
  }

  test.each([
    ["routes", 502],
    ["routes", 503],
    ["routes", 504],
    ["coverage", 502],
    ["coverage", 503],
    ["coverage", 504],
  ] as const)(
    "retries current %s %i tiles without reloading successes, UI, or capabilities",
    async (kind, status) => {
      let attempts = 0;
      const { view, map, fetch, load } = await mountRecovery([selection], async (url) => {
        if (url.endsWith("/654/1583.pbf") && ++attempts < 3) return new Response(null, { status });
        return new Response(null, { status: 204 });
      });
      fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
      const source = map.sources.get(`private-workout-${kind}`);
      const other = map.sources.get(`private-workout-${kind === "routes" ? "coverage" : "routes"}`);
      const layers = [...map.layers.entries()];
      const visibility = layers.map(([, layer]) => layer.layout?.visibility);
      const requestCount = fetch.mock.calls.filter(([, init]) => init?.method === "POST").length;
      const mutations = [
        map.addSource,
        map.removeSource,
        map.addLayer,
        map.removeLayer,
        map.setStyle,
        map.setFilter,
        map.setPaintProperty,
        map.setLayoutProperty,
        map.fitBounds,
      ];
      const counts = mutations.map((method) => method.mock.calls.length);
      const failed = load(kind);
      await load(kind, 655);
      await act(async () => vi.advanceTimersByTimeAsync(2000));
      await expect(failed).resolves.toMatchObject({ data: new ArrayBuffer(0) });
      expect(attempts).toBe(3);
      expect(source.setTiles).not.toHaveBeenCalled();
      expect(other.setTiles).not.toHaveBeenCalled();
      expect(fetch.mock.calls.filter(([url]) => String(url).includes("-tiles/"))).toHaveLength(4);
      expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(
        requestCount,
      );
      expect(mutations.map((method) => method.mock.calls.length)).toEqual(counts);
      expect([...map.layers.entries()]).toEqual(layers);
      expect(layers.map(([, layer]) => layer.layout?.visibility)).toEqual(visibility);
      expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(screen.getByRole("checkbox", { name: /Show Running/ })).toBeChecked();
      expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();

      // Source-loaded includes errored tiles; it must not drive retry budgets.
      act(() =>
        map.handlers.get("sourcedata")?.({
          sourceId: `private-workout-${kind}`,
          isSourceLoaded: true,
        }),
      );
      act(() => map.handlers.get("style.load")?.());
      expect(source.setTiles).not.toHaveBeenCalled();
      expect(fetch.mock.calls.filter(([url]) => String(url).includes("-tiles/"))).toHaveLength(4);
      expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(
        requestCount,
      );
      expect(mapInstances).toHaveLength(1);
      view.unmount();
    },
  );

  test("gives new viewport tiles a fresh budget after earlier failures exhaust attempts", async () => {
    const { view, load, fetch, map } = await mountRecovery(
      [selection],
      async () => new Response(null, { status: 503 }),
    );
    const first = load("routes");
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    await expect(first).rejects.toMatchObject({ status: 503 });
    act(() =>
      map.handlers.get("sourcedata")?.({
        sourceId: "private-workout-routes",
        isSourceLoaded: true,
      }),
    );
    const second = load("routes", 655);
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    await expect(second).rejects.toMatchObject({ status: 503 });
    expect(fetch.mock.calls.filter(([url]) => String(url).includes("/route-tiles/"))).toHaveLength(
      6,
    );
    view.unmount();
  });

  test.each(["routes", "coverage"] as const)(
    "handles %s 404 before MapLibre can swallow it and ignores old URLs",
    async (kind) => {
      const updated = {
        ...selection,
        id: "D".repeat(32),
        routeTileUrl: selection.routeTileUrl.replace(selection.id, "D".repeat(32)),
        coverageTileUrl: selection.coverageTileUrl.replace(selection.id, "D".repeat(32)),
      };
      const { view, load, map, fetch } = await mountRecovery(
        [selection, updated],
        async () => new Response(null, { status: 404 }),
      );
      const oldTemplate = map.sources.get(`private-workout-${kind}`).tiles[0];
      await act(async () => {
        await expect(load(kind)).rejects.toMatchObject({ status: 410 });
      });
      expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(2);
      expect(map.sources.get(`private-workout-${kind}`).tiles[0]).toEqual(
        privateTemplate(kind === "routes" ? updated.routeTileUrl : updated.coverageTileUrl),
      );
      await expect(load(kind, 655, new AbortController(), oldTemplate)).rejects.toMatchObject({
        name: "AbortError",
      });
      expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(2);
      view.unmount();
    },
  );

  test("dispatches session expiration once across concurrent route and coverage 401s", async () => {
    const { view, load, map } = await mountRecovery(
      [selection],
      async () => new Response(null, { status: 401 }),
    );
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    try {
      const results = [load("routes"), load("coverage")];
      await Promise.allSettled(results);
      expect(expired).toHaveBeenCalledTimes(1);
      act(() =>
        map.handlers.get("error")?.({
          error: {
            name: "PrivateTileError",
            status: 401,
            url: window.location.origin + selection.routeTileUrl,
          },
        }),
      );
      expect(expired).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
      view.unmount();
    }
  });

  test("cancels delayed old-URL reloads and gives new URLs a fresh budget", async () => {
    const updated = {
      ...selection,
      id: "D".repeat(32),
      routeTileUrl: selection.routeTileUrl.replace(selection.id, "D".repeat(32)),
      coverageTileUrl: selection.coverageTileUrl.replace(selection.id, "D".repeat(32)),
    };
    const { view, props, map, load, fetch } = await mountRecovery(
      [selection, updated],
      async () => new Response(null, { status: 503 }),
    );
    const oldRoute = map.sources.get("private-workout-routes").tiles[0];
    const oldCoverage = map.sources.get("private-workout-coverage").tiles[0];
    const pending = [load("routes"), load("coverage")];
    await act(async () => vi.advanceTimersByTimeAsync(1));
    await act(async () => view.rerender(<MapPage {...props} dateRange="last7Days" />));
    for (const result of pending)
      await expect(result).rejects.toMatchObject({ name: "AbortError" });
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    await expect(load("routes", 655, new AbortController(), oldRoute)).rejects.toMatchObject({
      name: "AbortError",
    });
    await expect(load("coverage", 655, new AbortController(), oldCoverage)).rejects.toMatchObject({
      name: "AbortError",
    });
    expect(fetch.mock.calls.filter(([url]) => String(url).includes("-tiles/"))).toHaveLength(2);
    view.unmount();
  });

  test("cancels backoff on a style change without source-wide reloads", async () => {
    const { view, props, map, load, fetch } = await mountRecovery(
      [selection],
      async () => new Response(null, { status: 503 }),
    );
    const pending = load("routes");
    const oldTemplate = map.sources.get("private-workout-routes").tiles[0];
    await act(async () => vi.advanceTimersByTimeAsync(1));
    view.rerender(<MapPage {...props} preferences={{ ...props.preferences, theme: "light" }} />);
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(load("routes", 655, new AbortController(), oldTemplate)).rejects.toMatchObject({
      name: "AbortError",
    });
    expect(map.sources.get("private-workout-routes").tiles[0]).not.toBe(oldTemplate);
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(fetch.mock.calls.filter(([url]) => String(url).includes("-tiles/"))).toHaveLength(1);
    expect(map.sources.get("private-workout-routes").setTiles).not.toHaveBeenCalled();
    view.unmount();
  });

  test.each([200, 204])(
    "retains valid empty %i as data without invalidating the selection",
    async (status) => {
      const { view, load, fetch } = await mountRecovery(
        [selection],
        async () =>
          new Response(null, {
            status,
            headers: { "Content-Type": "application/vnd.mapbox-vector-tile" },
          }),
      );
      await expect(load("routes")).resolves.toMatchObject({ data: new ArrayBuffer(0) });
      expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
      view.unmount();
    },
  );

  test("clears pending route/coverage jobs and protocol registration on unmount", async () => {
    const { view, map, load } = await mountRecovery(
      [selection],
      async () => new Response(null, { status: 503 }),
    );
    const pending = [load("routes"), load("coverage")];
    await act(async () => vi.advanceTimersByTimeAsync(1));
    view.unmount();
    for (const result of pending)
      await expect(result).rejects.toMatchObject({ name: "AbortError" });
    expect(vi.getTimerCount()).toBe(0);
    expect(tileProtocols.size).toBe(0);
    expect(map.remove).toHaveBeenCalledTimes(1);
  });

  test("style-transition AbortErrors do not install a base-map fallback", async () => {
    const { view, props, map } = await mountRecovery();
    mapBehavior.emitSetStyleLoad = false;
    view.rerender(<MapPage {...props} preferences={{ ...props.preferences, theme: "light" }} />);
    const styles = map.setStyle.mock.calls.length;
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { name: "AbortError" },
      }),
    );
    expect(map.setStyle).toHaveBeenCalledTimes(styles);
    expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();
    view.unmount();
  });
});

describe("independent workout focus", () => {
  const bounds = {
    minimumLongitude: 0.1,
    minimumLatitude: 0.01,
    maximumLongitude: 0.2,
    maximumLatitude: 0.02,
  };
  const fixture: MapSelection = {
    ...selection,
    id: "1".repeat(32),
    dataGeneration: 41,
    range: { startDate: "2001-01-01", endDate: "2001-01-02" },
    bounds,
    focusedWorkoutId: null,
    routeTileUrl: `/api/map-selections/${"1".repeat(32)}/route-tiles/41/{z}/{x}/{y}.pbf`,
    coverageTileUrl: `/api/map-selections/${"1".repeat(32)}/coverage-tiles/41/{z}/{x}/{y}.pbf`,
    workouts: selection.workouts.slice(0, 2).map((workout, index) => ({
      ...workout,
      id: String(index + 2).repeat(32),
      type: {
        id: String(index + 6).repeat(32),
        key: `synthetic-${index}`,
        name: `Synthetic ${index ? "B" : "A"}`,
      },
      localStartDate: "2001-01-01",
      startedAt: "2001-01-01T00:00:00Z",
      endedAt: "2001-01-01T00:02:00Z",
      bounds,
    })),
  };
  const points = {
    points: [
      ...Array.from({ length: 21 }, (_, i) => ({
        recordedAt: new Date(Date.UTC(2001, 0, 1, 0, 0, i)).toISOString(),
        longitude: 0.1 + i * 0.001,
        latitude: 0.01,
      })),
      ...[100, 101].map((i) => ({
        recordedAt: new Date(Date.UTC(2001, 0, 1, 0, 0, i)).toISOString(),
        longitude: 0.2,
        latitude: 0.02,
      })),
    ],
  };
  const focusGeometry = (marker: number) => ({
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        properties: {
          entityId: "4".repeat(32),
          entityKind: "park",
          sourceEntityId: "5".repeat(32),
          sourceEntityKind: "path",
          broadClass: "park",
          name: "Synthetic trail",
          localityName: "Synthetic park",
          regionId: "synthetic:test",
          rangeWorkoutCount: 2,
          countBucket: 2,
        },
        geometry: {
          type: "LineString",
          coordinates: [
            [0.1, 0.01],
            [0.12, 0.01 + marker * 0.001],
          ],
        },
      },
    ],
  });

  async function mount(mode: "routes" | "coverage", deferredRaw = false) {
    let current = fixture;
    const pending: Array<{
      kind: "raw" | "coverage";
      url: string;
      signal: AbortSignal;
      resolve: (value: unknown, status?: number) => void;
    }> = [];
    const stats: string[] = [];
    const rawReads: string[] = [];
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const url = String(input);
      if (url === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(current));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      if (url.endsWith("/stats?generation=41")) {
        stats.push(url);
        return Promise.resolve(
          json({
            ...focusGeometry(0).features[0].properties,
            regionName: "Synthetic region",
            bounds,
            rangeFirstDate: "2001-01-01",
            rangeLatestDate: "2001-01-02",
            allTimeFirstDate: "2001-01-01",
            allTimeLatestDate: "2001-01-02",
            allTimeWorkoutCount: 2,
          }),
        );
      }
      if (url.includes("/coverage-focus/") || url.endsWith("/route/points")) {
        const kind = url.includes("/coverage-focus/") ? "coverage" : "raw";
        if (kind === "raw") rawReads.push(url);
        if (kind === "raw" && !deferredRaw) return Promise.resolve(json(points));
        return new Promise<Response>((resolve) =>
          pending.push({
            kind,
            url,
            signal: init!.signal as AbortSignal,
            resolve: (value, status = 200) => resolve(json(value, status)),
          }),
        );
      }
      throw new Error(`Unexpected ${init?.method ?? "GET"} ${url}`);
    });
    function Harness({
      dateRange = "2001-01-01/2001-01-02",
      deleted = false,
    }: {
      dateRange?: DateRangePreference;
      deleted?: boolean;
    }) {
      const [focused, setFocused] = useState<string | undefined>(fixture.workouts[0].id);
      const [mapMode, setMapMode] = useState(mode);
      return (
        <MapPage
          config={config}
          preferences={{ ...preferences, timezone: "UTC", coverageDiagnosticsEnabled: false }}
          csrfToken="synthetic-csrf"
          dateRange={dateRange}
          onDateRangeSelected={vi.fn()}
          persistedFocusedWorkoutId={focused}
          onFocusedWorkoutChange={setFocused}
          persistedMapMode={mapMode}
          onMapModeChange={setMapMode}
          deletions={
            deleted
              ? { revision: 1, events: [{ workoutIds: [fixture.workouts[0].id] }] }
              : undefined
          }
        />
      );
    }
    const view = render(<Harness />);
    await screen.findByRole("button", { name: /Synthetic A/ });
    const map = mapInstances.at(-1)!;
    const choose = async (name: "A" | "B") => {
      fireEvent.click(screen.getByRole("button", { name: new RegExp(`Synthetic ${name}`) }));
      await act(async () => {
        await Promise.resolve();
      });
    };
    return {
      view,
      map,
      pending,
      stats,
      rawReads,
      choose,
      posts: () =>
        fetch.mock.calls.filter(
          ([url, init]) => String(url) === "/api/map-selections" && init?.method === "POST",
        ).length,
      refresh: async (next: MapSelection, deleted = false) => {
        current = next;
        view.rerender(<Harness dateRange="2001-01-01/2001-01-03" deleted={deleted} />);
        await act(async () => {
          await Promise.resolve();
        });
      },
    };
  }

  test("bounds focus caches by LRU count, fixed TTL and retained JSON bytes", () => {
    vi.useFakeTimers();
    const cache = new FocusGeometryCache<string>();
    for (let i = 0; i < 16; i++) cache.set(String(i), `geometry-${i}`);
    expect(cache.get("0")).toBe("geometry-0");
    cache.set("16", "geometry-16");
    expect(cache.get("1")).toBeUndefined();
    vi.advanceTimersByTime(4 * 60_000);
    expect(cache.get("0")).toBe("geometry-0");
    vi.advanceTimersByTime(60_000);
    expect(cache.get("0")).toBeUndefined();
    cache.clear();
    cache.set("large-a", "x".repeat(5 * 1024 * 1024));
    cache.set("large-b", "x".repeat(5 * 1024 * 1024));
    expect(cache.get("large-a")).toBeUndefined();
    expect(cache.get("large-b")).toBeDefined();
    cache.set("oversize", "x".repeat(8 * 1024 * 1024));
    expect(cache.get("oversize")).toBeUndefined();
  });

  test("renders segmented raw lines and stationary dots with background tiles pending, preserving Space and hit priority", async () => {
    mapBehavior.routeSourceLoaded = false;
    mapBehavior.tilesLoaded = false;
    const { map, posts, choose } = await mount("routes");
    await waitFor(() =>
      expect(map.getLayer("private-workout-route-focus")?.paint["line-opacity"]).toBe(1),
    );
    const background = map.getSource("private-workout-routes");
    const source = map.getSource("private-workout-route-focus");
    const geometry = source.setData.mock.calls.at(-1)[0];
    expect(geometry).toEqual(
      buildRawRouteHighlight(buildSegmentedRawRoute(points.points)!, fixture.workouts[0].id),
    );
    expect(geometry.features.map((feature: any) => feature.geometry.type)).toEqual([
      "LineString",
      "Point",
    ]);
    expect(geometry.features[1].geometry.coordinates).toEqual([0.2, 0.02]);
    expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(1);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0.95);
    expect(map.setFilter).toHaveBeenLastCalledWith("private-workout-route-marker-hover", [
      "all",
      ["==", ["geometry-type"], "Point"],
      ["==", ["get", "workoutId"], ""],
    ]);
    fireEvent.keyDown(window, { code: "Space" });
    expect(map.getLayer("private-workout-routes").paint["line-opacity"]).toBe(0);
    expect(map.getLayer("private-workout-route-markers").paint["circle-opacity"]).toBe(0);
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(1);
    expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(1);
    map.queryRenderedFeatures.mockImplementation(
      (_point: unknown, { layers }: { layers: string[] }) =>
        layers.includes("private-workout-route-focus")
          ? [{ properties: geometry.features[0].properties }]
          : [],
    );
    act(() => map.handlers.get("click")?.({ point: { x: 1, y: 1 } }));
    expect(map.queryRenderedFeatures).toHaveBeenCalledWith(
      { x: 1, y: 1 },
      { layers: ["private-workout-route-focus", "private-workout-route-focus-markers"] },
    );
    expect(
      map.queryRenderedFeatures.mock.calls.every(
        ([, options]: any[]) => !options.layers.includes("private-workout-routes"),
      ),
    ).toBe(true);
    fireEvent.keyUp(window, { code: "Space" });
    map.queryRenderedFeatures.mockImplementation(
      (_point: unknown, { layers }: { layers: string[] }) =>
        layers.includes("private-workout-routes")
          ? [
              { properties: { workoutId: fixture.workouts[0].id, sortOrder: 0 } },
              { properties: { workoutId: fixture.workouts[1].id, sortOrder: 1 } },
            ]
          : [{ properties: { workoutId: fixture.workouts[0].id } }],
    );
    act(() => map.handlers.get("click")?.({ point: { x: 1, y: 1 } }));
    await waitFor(() =>
      expect(
        map.getSource("private-workout-route-focus").setData.mock.calls.at(-1)[0].features[0]
          .properties.workoutId,
      ).toBe(fixture.workouts[1].id),
    );
    await choose("A");
    expect(map.getSource("private-workout-routes")).toBe(background);
    expect(background.setTiles).not.toHaveBeenCalled();
    expect(posts()).toBe(1);
  });

  test("holds vector fallback and arrows until the independent raw worker and source are ready", async () => {
    const { map, pending } = await mount("routes", true);
    const addSource = map.addSource.getMockImplementation();
    let complete!: () => void;
    map.addSource.mockImplementation((id: string, spec: object) => {
      addSource(id, spec);
      if (id === "private-workout-route-focus")
        map.getSource(id).setData.mockImplementation(
          () =>
            new Promise<void>((resolve) => {
              complete = resolve;
            }),
        );
    });
    await act(async () => pending[0].resolve(points));
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(0);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0);
    expect(map.setFilter).toHaveBeenCalledWith("private-workout-route-hover", [
      "==",
      ["get", "workoutId"],
      fixture.workouts[0].id,
    ]);
    mapBehavior.rawFocusSourceLoaded = false;
    await act(async () => complete());
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(0);
    mapBehavior.rawFocusSourceLoaded = true;
    mapBehavior.routeSourceLoaded = false;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" }));
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(1);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0.95);
  });

  test("does not schedule filter updates on stable renders and reinstalls them for replaced layers or styles", async () => {
    const { map } = await mount("routes");
    await waitFor(() =>
      expect(map.getLayer("coverage-diagnostic-direction")?.paint["icon-opacity"]).toBe(0.95),
    );
    map.setFilter.mockClear();
    map.setPaintProperty.mockClear();
    act(() => {
      for (let i = 0; i < 100; i++) {
        map.handlers.get("render")?.();
        map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" });
      }
    });
    expect(map.setFilter).not.toHaveBeenCalled();
    expect(map.setPaintProperty).not.toHaveBeenCalled();

    const layerId = "private-workout-route-hover";
    map.layers.set(layerId, { ...map.getLayer(layerId) });
    act(() => map.handlers.get("render")?.());
    expect(map.setFilter).toHaveBeenCalledExactlyOnceWith(layerId, [
      "==",
      ["get", "workoutId"],
      "",
    ]);
    map.setFilter.mockClear();
    await act(async () => map.handlers.get("style.load")?.());
    expect(map.setFilter).toHaveBeenCalledTimes(2);
    map.setFilter.mockClear();
    act(() => {
      for (let i = 0; i < 100; i++) map.handlers.get("render")?.();
    });
    expect(map.setFilter).not.toHaveBeenCalled();
  });

  test("keeps current raw lines, dots and arrows visible while user-pan viewport tiles are pending", async () => {
    mapBehavior.routeSourceLoaded = false;
    mapBehavior.tilesLoaded = false;
    const { map } = await mount("routes");
    await waitFor(() =>
      expect(map.getLayer("coverage-diagnostic-direction")?.paint["icon-opacity"]).toBe(0.95),
    );
    const source = map.getSource("private-workout-route-focus");
    const background = map.getSource("private-workout-routes");
    source.setData.mockClear();
    map.setFilter.mockClear();
    mapBehavior.dragging = true;
    act(() =>
      map.handlers.get("movestart")?.({
        originalEvent: new MouseEvent("mousemove", { buttons: 1 }),
      }),
    );
    mapBehavior.moving = true;
    mapBehavior.rawFocusSourceLoaded = false;
    act(() => {
      for (let i = 0; i < 10; i++) {
        map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" });
        map.handlers.get("render")?.();
      }
    });
    const expectVisible = () => {
      expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(1);
      expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(1);
      expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0.95);
      expect(map.getSource("private-workout-route-focus")).toBe(source);
      expect(map.getSource("private-workout-routes")).toBe(background);
    };
    expectVisible();
    mapBehavior.moving = false;
    mapBehavior.dragging = false;
    act(() => {
      map.handlers.get("moveend")?.();
      map.handlers.get("render")?.();
    });
    expectVisible();
    expect(source.setData).not.toHaveBeenCalled();
    expect(map.setFilter).not.toHaveBeenCalled();

    // A reused source ID is not proof that its new instance owns the installed geometry.
    map.sources.set("private-workout-route-focus", { ...source });
    act(() => map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" }));
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(0);
    expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(0);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0);
  });

  test("cannot reveal a new raw owner from an old worker completion using the same source ID", async () => {
    const { map, pending, choose } = await mount("routes", true);
    const addSource = map.addSource.getMockImplementation();
    const completions: Array<() => void> = [];
    map.addSource.mockImplementation((id: string, spec: object) => {
      addSource(id, spec);
      if (id === "private-workout-route-focus")
        map
          .getSource(id)
          .setData.mockImplementation(
            () => new Promise<void>((resolve) => completions.push(resolve)),
          );
    });
    await act(async () => pending[0].resolve(points));
    const oldSource = map.getSource("private-workout-route-focus");
    await choose("B");
    await act(async () => pending[1].resolve(points));
    const currentSource = map.getSource("private-workout-route-focus");
    expect(currentSource).not.toBe(oldSource);
    expect(completions).toHaveLength(2);
    await act(async () => completions[0]());
    act(() => {
      map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" });
      map.handlers.get("render")?.();
    });
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(0);
    expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(0);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0);
    await act(async () => completions[1]());
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(1);
    expect(map.getLayer("private-workout-route-focus-markers").paint["circle-opacity"]).toBe(1);
    expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0.95);
    expect(currentSource.setData.mock.calls.at(-1)[0].features[0].properties.workoutId).toBe(
      fixture.workouts[1].id,
    );
  });

  test.each([200, 404])(
    "ignores aborted Coverage %s responses through A/B/A without caching or renewing tiles",
    async (status) => {
      const { map, pending, choose, posts } = await mount("coverage");
      await waitFor(() => expect(pending).toHaveLength(1));
      await choose("B");
      expect(pending[0].signal.aborted).toBe(true);
      await choose("A");
      expect(pending).toHaveLength(3);
      expect(pending[1].signal.aborted).toBe(true);
      const aggregate = map.getSource("private-workout-coverage");
      await act(async () => {
        pending[0].resolve(focusGeometry(9), status);
        pending[1].resolve(focusGeometry(8));
      });
      const focused = map.getSource("private-workout-coverage-focus");
      expect(focused.setData.mock.calls.at(-1)?.[0] ?? focused.data).toEqual({
        type: "FeatureCollection",
        features: [],
      });
      expect(posts()).toBe(1);
      await act(async () => pending[2].resolve(focusGeometry(1)));
      expect(focused.setData).toHaveBeenLastCalledWith(focusGeometry(1));
      await choose("B");
      expect(pending).toHaveLength(4);
      expect(focused.setData).toHaveBeenLastCalledWith({ type: "FeatureCollection", features: [] });
      await act(async () => pending[3].resolve(focusGeometry(2)));
      await choose("A");
      expect(pending).toHaveLength(4);
      expect(focused.setData).toHaveBeenLastCalledWith(focusGeometry(1));
      expect(map.getSource("private-workout-coverage")).toBe(aggregate);
      expect(aggregate.setTiles).not.toHaveBeenCalled();
      expect(posts()).toBe(1);
    },
  );

  test.each(["selection", "generation", "policy", "revision", "deletion", "unmount"])(
    "invalidates Coverage ownership on %s before accepting late results",
    async (change) => {
      const { view, map, pending, refresh, posts } = await mount("coverage");
      await waitFor(() => expect(pending).toHaveLength(1));
      if (change === "unmount") view.unmount();
      else
        await refresh(
          {
            ...fixture,
            id: change === "selection" ? "9".repeat(32) : fixture.id,
            dataGeneration: change === "generation" ? 42 : 41,
            coverageDisplayPolicy:
              change === "policy" ? "synthetic-policy-v2" : fixture.coverageDisplayPolicy,
            coverageDisplayRevision: change === "revision" ? 2 : 1,
            workouts: change === "deletion" ? [fixture.workouts[1]] : fixture.workouts,
          },
          change === "deletion",
        );
      expect(pending[0].signal.aborted).toBe(true);
      const count = posts();
      map.setPaintProperty.mockClear();
      await act(async () => pending[0].resolve(focusGeometry(9), 404));
      expect(posts()).toBe(count);
      if (change === "unmount") expect(map.setPaintProperty).not.toHaveBeenCalled();
      else {
        const source = map.getSource("private-workout-coverage-focus");
        expect(source?.setData.mock.calls.at(-1)?.[0] ?? source?.data).not.toEqual(
          focusGeometry(9),
        );
      }
    },
  );

  test("invalidates raw geometry on generation changes and ignores late points after deletion or unmount", async () => {
    const { view, map, pending, choose, refresh } = await mount("routes", true);
    await waitFor(() => expect(pending).toHaveLength(1));
    await act(async () => pending[0].resolve(points));
    await choose("B");
    await act(async () => pending[1].resolve(points));
    await choose("A");
    expect(pending).toHaveLength(2);
    await refresh({ ...fixture, dataGeneration: 42 });
    await waitFor(() => expect(pending).toHaveLength(3));
    expect(map.getSource("private-workout-route-focus")).toBeUndefined();
    await choose("B");
    expect(pending[2].signal.aborted).toBe(true);
    await act(async () => pending[2].resolve(points));
    expect(map.getSource("private-workout-route-focus")).toBeUndefined();
    view.unmount();
    expect(pending[3].signal.aborted).toBe(true);
    map.addSource.mockClear();
    await act(async () => pending[3].resolve(points));
    expect(map.addSource).not.toHaveBeenCalled();
  });

  test("aborts raw points on accepted deletion and cannot restore the deleted overlay", async () => {
    const { map, pending, refresh } = await mount("routes", true);
    await waitFor(() => expect(pending).toHaveLength(1));
    await refresh({ ...fixture, workouts: [fixture.workouts[1]], dataGeneration: 42 }, true);
    expect(pending[0].signal.aborted).toBe(true);
    await act(async () => pending[0].resolve(points));
    expect(map.getSource("private-workout-route-focus")).toBeUndefined();
    expect(screen.queryByRole("button", { name: /Synthetic A/ })).not.toBeInTheDocument();
  });

  test("keeps an active raw overlay after cache expiry but refetches expired geometry on return", async () => {
    const { map, rawReads, choose } = await mount("routes");
    await waitFor(() =>
      expect(map.getLayer("private-workout-route-focus")?.paint["line-opacity"]).toBe(1),
    );
    const source = map.getSource("private-workout-route-focus");
    vi.useFakeTimers();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000 + 1);
    });
    await choose("A");
    expect(map.getSource("private-workout-route-focus")).toBe(source);
    expect(map.getLayer("private-workout-route-focus").paint["line-opacity"]).toBe(1);
    expect(rawReads).toHaveLength(1);
    await choose("B");
    await choose("A");
    expect(rawReads).toHaveLength(3);
  });

  test("prunes successful Coverage cache entries when the selection context changes", async () => {
    const { pending, choose, refresh } = await mount("coverage");
    await waitFor(() => expect(pending).toHaveLength(1));
    await act(async () => pending[0].resolve(focusGeometry(1)));
    await choose("B");
    await act(async () => pending[1].resolve(focusGeometry(2)));
    await refresh({ ...fixture, id: "9".repeat(32) });
    await waitFor(() => expect(pending).toHaveLength(3));
    await act(async () => pending[2].resolve(focusGeometry(3)));
    await choose("A");
    expect(pending).toHaveLength(4);
    expect(pending[3].url).toContain(
      `/9${"9".repeat(31)}/coverage-focus/${fixture.workouts[0].id}`,
    );
  });

  test("uses focused Coverage source attribution when aggregate geometry is missing or Space-hidden", async () => {
    const { map, pending, stats } = await mount("coverage");
    await waitFor(() => expect(pending).toHaveLength(1));
    await act(async () => pending[0].resolve(focusGeometry(0)));
    const path = { properties: focusGeometry(0).features[0].properties };
    const park = {
      properties: {
        ...path.properties,
        entityId: "8".repeat(32),
        sourceEntityKind: "park",
        name: "Synthetic park",
      },
    };
    map.queryRenderedFeatures.mockImplementation(
      (_point: unknown, { layers }: { layers: string[] }) =>
        layers.includes("private-workout-coverage-focus") ? [park, path] : [],
    );
    vi.useFakeTimers();
    const move = () =>
      map.handlers.get("mousemove")?.({ point: { x: 1, y: 1 }, lngLat: { lng: 0.1, lat: 0.01 } });
    act(move);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(750);
    });
    expect(stats).toEqual([
      `/api/map-selections/${fixture.id}/coverage/park/${path.properties.entityId}/stats?generation=41`,
    ]);
    expect(popupInstances.at(-1)?.content).toHaveTextContent("Synthetic trail");
    fireEvent.mouseLeave(map.getCanvas());
    fireEvent.keyDown(window, { code: "Space" });
    map.queryRenderedFeatures.mockClear();
    act(move);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(750);
    });
    expect(
      map.queryRenderedFeatures.mock.calls.every(
        ([, options]: any[]) => !options.layers.includes("private-workout-coverage"),
      ),
    ).toBe(true);
    expect(map.getLayer("private-workout-coverage-focus").paint["line-opacity"]).toBe(0.94);
    expect(stats).toHaveLength(1);
    expect(popupInstances.at(-1)?.content).toHaveTextContent("Synthetic trail");
    fireEvent.keyUp(window, { code: "Space" });
  });
});

describe("direction marker camera readiness", () => {
  const directionLayer = "coverage-diagnostic-direction";
  const directionPoints = (latitude: number) => ({
    points: Array.from({ length: 41 }, (_, i) => ({
      recordedAt: new Date(i * 1000).toISOString(),
      longitude: -105.3 + i * 0.001,
      latitude: latitude + i * 0.0005,
    })),
  });
  const firstPoints = directionPoints(39.9);
  const secondPoints = directionPoints(40);

  async function mountDirections(deferSecondRoute = false) {
    let resolveSecondRoute: (response: Response) => void = () => {};
    const secondResponse = deferSecondRoute
      ? new Promise<Response>((resolve) => {
          resolveSecondRoute = resolve;
        })
      : Promise.resolve(json(secondPoints));
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(selection));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points`)
        return Promise.resolve(json(firstPoints));
      if (path === `/api/workouts/${selection.workouts[1].id}/route/points`) return secondResponse;
      if (path.endsWith("/coverage-diagnostic-runs")) return Promise.resolve(json(diagnostic, 201));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${path}`);
    });
    const props = {
      config: {
        ...config,
        features: { coverageMatcherDiagnostics: true },
        baseMaps: { ...baseMaps, fallbackFamilyId: "outdoor" },
      },
      preferences,
      csrfToken: "csrf-map",
      dateRange: "last30Days" as const,
      onDateRangeSelected: vi.fn(),
    };
    const view = render(<MapPage {...props} />);
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.getLayer(directionLayer)?.paint["icon-opacity"]).toBe(0.95));
    return { view, props, map, resolveSecondRoute: () => resolveSecondRoute(json(secondPoints)) };
  }

  function deferDirections(map: Record<string, any>) {
    const completions: Array<() => void> = [];
    const source = map.getSource(directionLayer);
    source.setData.mockImplementation((_data: unknown, waitForCompletion?: boolean) =>
      waitForCompletion
        ? new Promise<void>((resolve) =>
            completions.push(() => {
              map.handlers.get("render")?.();
              resolve();
            }),
          )
        : undefined,
    );
    return { source, completions };
  }

  test("repeated A/B focus fits retain each route's final-camera arrow count", async () => {
    const { map } = await mountDirections();
    map.fitBounds.mockImplementation((bounds: number[][]) => {
      mapBehavior.moving = true;
      map.handlers.get("movestart")?.();
      mapBehavior.projectionScale =
        904 /
        Math.max(Math.abs(bounds[1][0] - bounds[0][0]), Math.abs(bounds[1][1] - bounds[0][1]));
      mapBehavior.moving = false;
      map.handlers.get("moveend")?.();
    });
    const counts = new Map<number, number>();
    for (const index of [1, 0, 1, 0, 1, 0]) {
      const label = index === 0 ? /Running.*8\/08\/2026/ : /Hiking.*8\/01\/2026/;
      await userEvent.click(screen.getByRole("button", { name: label }));
      const points = index === 0 ? firstPoints : secondPoints;
      const scale = mapBehavior.projectionScale;
      const expected = buildRawRouteDirectionMarkers(
        buildSegmentedRawRoute(points.points)!,
        ([x, y]) => ({ x: x * scale, y: -y * scale }),
      );
      expect(expected.features.length).toBeGreaterThan(1);
      await waitFor(() => {
        expect(map.getLayer(directionLayer)?.paint["icon-opacity"]).toBe(0.95);
        expect(map.getSource(directionLayer).setData.mock.calls.at(-1)?.[0]).toEqual(expected);
      });
      const count = map.getSource(directionLayer).setData.mock.calls.at(-1)[0].features.length;
      if (counts.has(index)) expect(count).toBe(counts.get(index));
      else counts.set(index, count);
    }
    expect(counts.size).toBe(2);
  });

  test("does not reuse markers at a different projection even when route and source identities match", async () => {
    const { map } = await mountDirections();
    const source = map.getSource(directionLayer);
    const original = source.setData.mock.calls.at(-1)[0];
    source.setData.mockClear();
    mapBehavior.projectionScale = 2000;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    await waitFor(() => expect(source.setData).toHaveBeenCalledTimes(1));
    expect(source.setData.mock.calls[0][0].features.length).toBeLessThan(original.features.length);
    await waitFor(() => expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95));
    mapBehavior.projectionScale = 10000;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" }));
    await waitFor(() => expect(source.setData).toHaveBeenCalledTimes(2));
    expect(source.setData.mock.calls[1][0]).toEqual(original);
  });

  test("waits for a final-camera render and its independent route tiles before revealing ready arrows", async () => {
    const { map } = await mountDirections();
    const source = map.getSource(directionLayer);
    let complete!: () => void;
    source.setData.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          complete = resolve;
        }),
    );
    act(() => map.handlers.get("moveend")?.());
    await act(async () => complete());
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.rawFocusSourceLoaded = false;
    act(() => map.handlers.get("render")?.());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.rawFocusSourceLoaded = true;
    mapBehavior.routeSourceLoaded = false;
    mapBehavior.tilesLoaded = false;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    map.setPaintProperty.mockClear();
    act(() => {
      for (let i = 0; i < 5; i++) map.handlers.get("render")?.();
    });
    expect(map.setPaintProperty).not.toHaveBeenCalled();
    expect(map.areTilesLoaded).not.toHaveBeenCalled();
  });

  test.each(["pending", "active"])(
    "explicit focus cancels a %s preview of a different route",
    async (preview) => {
      const { map } = await mountDirections();
      await userEvent.hover(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
      if (preview === "active")
        await waitFor(() =>
          expect(map.getSource("private-workout-route-endpoints").data).toEqual(
            buildRawRouteEndpoints(secondPoints.points),
          ),
        );
      // Keyboard/programmatic activation need not move the pointer off the old preview.
      fireEvent.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
      await act(async () => {
        await new Promise((resolve) => window.setTimeout(resolve, 800));
      });
      await waitFor(() =>
        expect(map.getSource(directionLayer).setData.mock.calls.at(-1)?.[0]).toEqual(
          buildRawRouteDirectionMarkers(buildSegmentedRawRoute(firstPoints.points)!, ([x, y]) => ({
            x: x * mapBehavior.projectionScale,
            y: -y * mapBehavior.projectionScale,
          })),
        ),
      );
      const endpoints = map.getSource("private-workout-route-endpoints");
      expect(endpoints.setData.mock.calls.at(-1)?.[0] ?? endpoints.data).toEqual(
        buildRawRouteEndpoints(firstPoints.points),
      );
    },
  );

  test("skips late route projection during zoom-out and waits for the final worker and source tiles", async () => {
    const { view, props, map, resolveSecondRoute } = await mountDirections(true);
    const highZoom = map.getSource(directionLayer).setData.mock.calls.at(-1)[0];
    expect(highZoom.features.length).toBeGreaterThan(0);
    map.project.mockClear();
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    // movestart can precede isMoving becoming true.
    act(() => {
      map.handlers.get("movestart")?.();
      map.handlers.get("zoomstart")?.();
    });
    expect(map.setPaintProperty.mock.calls.slice(-2)).toEqual([
      [directionLayer, "icon-opacity-transition", { duration: 0, delay: 0 }],
      [directionLayer, "icon-opacity", 0],
    ]);
    expect(map.setLayoutProperty).not.toHaveBeenCalled();
    mapBehavior.moving = true;
    await userEvent.click(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
    await act(async () => resolveSecondRoute());
    await waitFor(() =>
      expect(map.getSource("private-workout-route-endpoints").data).toEqual(
        buildRawRouteEndpoints(secondPoints.points),
      ),
    );
    expect(map.project).not.toHaveBeenCalled();
    const { source, completions } = deferDirections(map);
    expect(source.setData).not.toHaveBeenCalled();
    expect(map.getLayer(directionLayer).layout.visibility).toBe("visible");
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    expect(map.getLayer("private-workout-route-start").paint["circle-opacity"]).toBe(1);
    expect(map.getLayer("private-workout-route-finish").paint["text-opacity"]).toBe(1);
    expect(map.getLayer("private-workout-routes").layout.visibility).toBe("visible");
    mapBehavior.projectionScale = 2000;
    act(() => map.handlers.get("moveend")?.());
    expect(source.setData).not.toHaveBeenCalled(); // Another pan is still moving.
    mapBehavior.moving = false;
    view.rerender(<MapPage {...props} preferences={{ ...preferences, clockFormat: "12h" }} />);
    expect(map.project).not.toHaveBeenCalled(); // Wait for moveend, not just isMoving.
    act(() => map.handlers.get("moveend")?.());
    const expected = buildRawRouteDirectionMarkers(
      buildSegmentedRawRoute(secondPoints.points)!,
      ([x, y]) => ({
        x: x * 2000,
        y: -y * 2000,
      }),
    );
    expect(source.setData).toHaveBeenCalledExactlyOnceWith(expected, true);
    expect(expected.features.length).toBeGreaterThan(0);
    expect(expected.features.length).toBeLessThan(highZoom.features.length);
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer, isSourceLoaded: true }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.directionSourceLoaded = false;
    await act(async () => completions[0]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.directionSourceLoaded = true;
    mapBehavior.tilesLoaded = false;
    mapBehavior.rawFocusSourceLoaded = false;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: "private-workout-route-focus" }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.rawFocusSourceLoaded = true;
    mapBehavior.routeSourceLoaded = false;
    act(() =>
      map.handlers.get("sourcedata")?.({
        sourceId: "private-workout-route-focus",
        isSourceLoaded: true,
      }),
    );
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    expect(map.isSourceLoaded).toHaveBeenCalledWith(directionLayer);
    expect(map.isSourceLoaded).toHaveBeenCalledWith("private-workout-route-focus");
    expect(map.areTilesLoaded).not.toHaveBeenCalled();
  });

  test("invalidates pending workers across repeated movements and also guards actual isMoving", async () => {
    const { map } = await mountDirections();
    const { source, completions } = deferDirections(map);
    source.setData.mockClear();
    act(() => {
      map.handlers.get("movestart")?.();
      map.handlers.get("zoomstart")?.();
      map.handlers.get("moveend")?.();
      map.handlers.get("movestart")?.();
      map.handlers.get("movestart")?.();
    });
    await act(async () => completions[0]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    act(() => map.handlers.get("moveend")?.());
    mapBehavior.moving = true;
    await act(async () => completions[1]());
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.moving = false;
    act(() => map.handlers.get("moveend")?.());
    expect(completions).toHaveLength(3);
    await act(async () => completions[2]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
  });

  test.each(["zoomstart", "rotatestart", "pitchstart"])(
    "keeps ready arrows visible during panning but invalidates them for %s",
    async (cameraEvent) => {
      const { map, view, props } = await mountDirections();
      const source = map.getSource(directionLayer);
      source.setData.mockClear();
      map.project.mockClear();
      map.setPaintProperty.mockClear();
      mapBehavior.dragging = true;
      act(() =>
        map.handlers.get("movestart")?.({
          originalEvent: new MouseEvent("mousemove", { buttons: 1 }),
        }),
      );
      mapBehavior.moving = true;
      act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
      view.rerender(<MapPage {...props} preferences={{ ...preferences, clockFormat: "12h" }} />);
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
      expect(source.setData).not.toHaveBeenCalled();
      expect(map.project).not.toHaveBeenCalled();
      mapBehavior.moving = false;
      mapBehavior.dragging = false;
      act(() => map.handlers.get("moveend")?.());
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
      expect(source.setData).not.toHaveBeenCalled();
      expect(map.project).not.toHaveBeenCalled();
      expect(map.setPaintProperty.mock.calls).not.toContainEqual([
        directionLayer,
        "icon-opacity",
        0,
      ]);
      const { completions } = deferDirections(map);
      act(() => {
        map.handlers.get("movestart")?.();
        map.handlers.get(cameraEvent)?.();
      });
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
      act(() => map.handlers.get("moveend")?.());
      expect(completions).toHaveLength(1);
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
      await act(async () => completions[0]());
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
      view.unmount();
      expect(map.off).toHaveBeenCalledWith(cameraEvent, expect.any(Function));
    },
  );

  test.each([undefined, "click", "mousemove"])(
    "hides ready arrows during programmatic pan without zoom (original event: %s)",
    async (originalEvent) => {
      const { map, view, props } = await mountDirections();
      const { source, completions } = deferDirections(map);
      source.setData.mockClear();
      map.project.mockClear();
      act(() =>
        map.handlers.get("movestart")?.(
          originalEvent
            ? { originalEvent: new MouseEvent(originalEvent, { buttons: 1 }) }
            : undefined,
        ),
      );
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
      mapBehavior.moving = true;
      view.rerender(<MapPage {...props} preferences={{ ...preferences, clockFormat: "12h" }} />);
      act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
      expect(source.setData).not.toHaveBeenCalled();
      expect(map.project).not.toHaveBeenCalled();
      mapBehavior.moving = false;
      act(() => map.handlers.get("moveend")?.());
      expect(completions).toHaveLength(1);
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
      await act(async () => completions[0]());
      expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    },
  );

  test("programmatic movement invalidates arrows even when a mouse drag is still active", async () => {
    const { map } = await mountDirections();
    mapBehavior.dragging = true;
    act(() =>
      map.handlers.get("movestart")?.({
        originalEvent: new MouseEvent("mousemove", { buttons: 1 }),
      }),
    );
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    act(() => map.handlers.get("movestart")?.());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
  });

  test("hides stationary route replacements until fresh data completes and ignores replaced sources", async () => {
    const { map } = await mountDirections();
    const { source: oldSource, completions: oldCompletions } = deferDirections(map);
    act(() => map.handlers.get("moveend")?.());
    const addSource = map.addSource.getMockImplementation();
    let current: ReturnType<typeof deferDirections> | undefined;
    map.addSource.mockImplementation((id: string, spec: object) => {
      addSource(id, spec);
      if (id === directionLayer) current = deferDirections(map);
    });
    await userEvent.click(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
    // Source readiness alone cannot reveal a pending worker, including a stationary replacement.
    await waitFor(() => expect(map.getSource(directionLayer)).not.toBe(oldSource));
    expect(current?.completions).toHaveLength(1);
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    await act(async () => oldCompletions[0]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.directionSourceLoaded = false;
    await act(async () => current!.completions[0]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    mapBehavior.directionSourceLoaded = true;
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    const { source, completions } = current!;
    act(() => map.handlers.get("moveend")?.());
    const replacement = { setData: vi.fn() };
    map.sources.set(directionLayer, replacement);
    mapBehavior.directionSourceLoaded = true;
    await act(async () => completions[1]());
    act(() => map.handlers.get("sourcedata")?.({ sourceId: directionLayer }));
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    expect(source.setData).toHaveBeenLastCalledWith(
      expect.objectContaining({ type: "FeatureCollection" }),
      true,
    );
  });

  test("ignores old style and unmounted completions and resynchronizes a style hidden", async () => {
    const { view, props, map } = await mountDirections();
    const { completions } = deferDirections(map);
    act(() => map.handlers.get("moveend")?.());
    mapBehavior.emitSetStyleLoad = false;
    view.rerender(<MapPage {...props} preferences={{ ...preferences, theme: "light" }} />);
    await act(async () => completions[0]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    act(() => map.handlers.get("style.load")?.());
    expect(completions).toHaveLength(2);
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0);
    await act(async () => completions[1]());
    expect(map.getLayer(directionLayer).paint["icon-opacity"]).toBe(0.95);
    act(() => map.handlers.get("moveend")?.());
    view.unmount();
    map.setPaintProperty.mockClear();
    await act(async () => completions[2]());
    act(() => {
      map.handlers.get("sourcedata")?.({ sourceId: directionLayer });
      map.handlers.get("style.load")?.();
      map.handlers.get("moveend")?.();
    });
    expect(map.setPaintProperty).not.toHaveBeenCalled();
    expect(map.remove).toHaveBeenCalledOnce();
  });

  test("caches stationary data while retaining Diagnostic fade and raw-route hide semantics", async () => {
    const { view, props, map } = await mountDirections();
    const source = map.getSource(directionLayer);
    source.setData.mockClear();
    map.project.mockClear();
    view.rerender(<MapPage {...props} preferences={{ ...preferences, clockFormat: "12h" }} />);
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    await screen.findByRole("region", { name: "Coverage diagnostic review" });
    const layer = () => map.getLayer(directionLayer);
    expect(layer().paint["icon-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    expect(layer().paint["icon-opacity"]).toBe(0);
    expect(layer().paint["icon-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    expect(layer().paint["icon-opacity"]).toBe(0.95);
    expect(source.setData).not.toHaveBeenCalled();
    expect(map.project).not.toHaveBeenCalled();
    const raw = map.getLayer("coverage-diagnostic-raw-route");
    const start = map.getLayer("private-workout-route-start");
    const finish = map.getLayer("private-workout-route-finish");
    const vector = map.getLayer("private-workout-routes");
    mapBehavior.dragging = true;
    act(() =>
      map.handlers.get("movestart")?.({
        originalEvent: new MouseEvent("mousemove", { buttons: 1 }),
      }),
    );
    mapBehavior.moving = true;
    expect(layer().paint["icon-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    expect(layer().paint["icon-opacity"]).toBe(0.95);
    expect(map.getLayer("coverage-diagnostic-raw-route")).toBe(raw);
    expect(raw.paint["line-opacity"]).toBe(1);
    expect(map.getLayer("private-workout-route-start")).toBe(start);
    expect(map.getLayer("private-workout-route-finish")).toBe(finish);
    expect(map.getLayer("private-workout-routes")).toBe(vector);
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    expect(layer().paint["icon-opacity"]).toBe(0);
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    expect(layer().paint["icon-opacity"]).toBe(0.95);
    act(() => map.handlers.get("zoomstart")?.());
    expect(layer().paint["icon-opacity-transition"]).toEqual({ duration: 0, delay: 0 });
    expect(layer().paint["icon-opacity"]).toBe(0);
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    const { completions } = deferDirections(map);
    mapBehavior.moving = false;
    await act(async () => map.handlers.get("moveend")?.());
    await act(async () => completions[0]());
    expect(layer().paint["icon-opacity"]).toBe(0);
    expect(layer().paint["icon-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    expect(layer().paint["icon-opacity"]).toBe(0.95);
    expect(source.setData).toHaveBeenCalledTimes(1);
    expect(map.project).toHaveBeenCalledTimes(firstPoints.points.length);
  });
});

describe("MapPage", () => {
  test("moves keyboard focus to the canvas for an explicit tab-navigation request", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(selection));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    const props = {
      config,
      preferences,
      csrfToken: "csrf-map",
      dateRange: "last30Days" as const,
      onDateRangeSelected: vi.fn(),
    };
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
    const succeeds = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(429))
      .mockResolvedValueOnce("prepared");
    await expect(retryInitialDiagnosticBusy(succeeds, controller.signal, 0)).resolves.toBe(
      "prepared",
    );
    expect(succeeds).toHaveBeenCalledTimes(2);

    const remainsBusy = vi.fn().mockRejectedValue(new ApiError(429));
    await expect(
      retryInitialDiagnosticBusy(remainsBusy, controller.signal, 0),
    ).rejects.toMatchObject({ status: 429 });
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
    const enabledConfig: PublicConfig = {
      ...config,
      features: { coverageMatcherDiagnostics: true },
    };
    const requests: Array<{ path: string; method: string; body?: unknown; csrf: string | null }> =
      [];
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      requests.push({
        path,
        method,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
        csrf: new Headers(init?.headers).get("X-CSRF-Token"),
      });
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (
        path === `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs` &&
        method === "POST"
      )
        return Promise.resolve(json(diagnostic, 201));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (path === `/api/coverage-diagnostic-runs/${diagnostic.id}/labels` && method === "PATCH")
        return Promise.resolve(json({ overall: "correct", segments: diagnostic.labels.segments }));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const view = render(
      <MapPage
        config={enabledConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const coverage = screen.getByRole("button", { name: "Coverage" });
    expect(coverage).toBeDisabled();
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    expect(new URLSearchParams(location.search).get("workoutId")).toBe(selection.workouts[0].id);
    await waitFor(() => expect(coverage).toBeEnabled());
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", {
        type: "geojson",
        data: rawRoute,
      }),
    );
    expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-direction", {
      type: "geojson",
      data: expect.objectContaining({ type: "FeatureCollection" }),
    });
    await waitFor(() =>
      expect(map.getLayer("coverage-diagnostic-direction").paint["icon-opacity"]).toBe(0.95),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-raw-route",
      "line-opacity",
      0,
    );
    expect(map.sources.has("private-workout-route-endpoints")).toBe(true);
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-opacity",
      1,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-finish",
      "text-opacity",
      1,
    );
    const rawSourceAdds = map.addSource.mock.calls.filter(
      (call: any[]) => call[0] === "coverage-diagnostic-raw-route",
    ).length;
    const preloadedRawSource = map.sources.get("coverage-diagnostic-raw-route");
    preloadedRawSource.setData.mockClear();
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    await userEvent.click(coverage);
    const review = await screen.findByRole("region", { name: "Coverage diagnostic review" });
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute(
      "aria-keyshortcuts",
      "Space",
    );
    expect(review).toHaveTextContent("Matched40");
    expect(review).toHaveTextContent("Ambiguous5");
    expect(review).toHaveTextContent("Unmatched5");
    expect(review).toHaveTextContent("Runtime1.25 s");
    expect(review).toHaveTextContent("Owner diagnostic / v7");
    expect(review.querySelector(".coverage-diagnostic-save")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Uncertain" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(requests).toContainEqual({
      path: `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs`,
      method: "POST",
      body: {},
      csrf: "csrf-map",
    });
    expect(mapInstances).toHaveLength(1);
    expect(document.activeElement).toBe(map.canvas);
    expect(map.addSource).toHaveBeenCalledWith(
      "coverage-diagnostic-overlay",
      expect.objectContaining({ type: "geojson" }),
    );
    expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", {
      type: "geojson",
      data: rawRoute,
    });
    expect(
      map.addSource.mock.calls.filter((call: any[]) => call[0] === "coverage-diagnostic-raw-route"),
    ).toHaveLength(rawSourceAdds);
    expect(preloadedRawSource.setData).not.toHaveBeenCalled();
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "coverage-diagnostic-raw-route",
      )?.[0].paint["line-opacity-transition"],
    ).toEqual({ duration: 400, delay: 0 });
    expect(map.addImage).toHaveBeenCalledWith(
      "coverage-diagnostic-direction-triangle",
      expect.objectContaining({ width: 11, height: 15 }),
    );
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "coverage-diagnostic-direction",
      )?.[0],
    ).toMatchObject({
      type: "symbol",
      layout: {
        "icon-image": "coverage-diagnostic-direction-triangle",
        "icon-rotate": ["get", "bearing"],
      },
      paint: { "icon-opacity-transition": { duration: 0, delay: 0 } },
    });
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "icon-opacity-transition",
      { duration: 400, delay: 0 },
    );
    const directionSource = map.sources.get("coverage-diagnostic-direction");
    directionSource.setData.mockClear();
    await act(async () => map.handlers.get("moveend")?.());
    expect(directionSource.setData).toHaveBeenCalledWith(
      expect.objectContaining({ type: "FeatureCollection" }),
      true,
    );
    expect(map.addSource).toHaveBeenCalledWith("private-workout-route-endpoints", {
      type: "geojson",
      data: rawRouteEndpoints,
    });
    const startLayer = map.addLayer.mock.calls.find(
      (call: any[]) => call[0].id === "private-workout-route-start",
    )?.[0];
    const finishLayer = map.addLayer.mock.calls.find(
      (call: any[]) => call[0].id === "private-workout-route-finish",
    )?.[0];
    expect(startLayer.paint["circle-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    expect(startLayer.paint["circle-stroke-opacity-transition"]).toEqual({
      duration: 400,
      delay: 0,
    });
    expect(finishLayer.paint["text-opacity-transition"]).toEqual({ duration: 400, delay: 0 });
    expect(startLayer.paint).toMatchObject({
      "circle-color": "#20a464",
      "circle-radius": (22 / Math.sqrt(Math.PI)) * 0.9 * 0.97 * 0.95,
      "circle-stroke-color": "#334155",
    });
    expect(finishLayer.layout["text-field"]).toBe("■");
    expect(finishLayer.paint["text-halo-color"]).toBe("#334155");
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-route-start");
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-route-finish");
    expect(map.moveLayer.mock.calls.at(-1)).toEqual(["private-workout-route-start"]);
    const hitLayerIndex = map.addLayer.mock.calls.findIndex(
      (call: any[]) => call[0].id === "coverage-diagnostic-hit-target",
    );
    expect(map.moveLayer.mock.invocationCallOrder.at(-1)).toBeGreaterThan(
      map.addLayer.mock.invocationCallOrder[hitLayerIndex],
    );
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "coverage-diagnostic-raw-route",
      )?.[0].paint["line-color"],
    ).toBe("#c026ff");
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "coverage-diagnostic-matched",
      )?.[0].paint["line-color"],
    ).toBe("#19c7c9");
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "coverage-diagnostic-ambiguous",
      )?.[0].paint["line-color"],
    ).toBe("#e7a83d");
    await waitFor(() =>
      expect(map.setLayoutProperty).toHaveBeenCalledWith(
        "private-workout-routes",
        "visibility",
        "none",
      ),
    );
    const rawVisibleIndex = map.setPaintProperty.mock.calls.findIndex(
      (call: any[]) =>
        call[0] === "coverage-diagnostic-raw-route" && call[1] === "line-opacity" && call[2] === 1,
    );
    const vectorsHiddenIndex = map.setLayoutProperty.mock.calls.findIndex(
      (call: any[]) => call[0] === "private-workout-routes" && call[2] === "none",
    );
    expect(map.setPaintProperty.mock.invocationCallOrder[rawVisibleIndex]).toBeLessThan(
      map.setLayoutProperty.mock.invocationCallOrder[vectorsHiddenIndex],
    );
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalled());
    expect(document.activeElement).toBe(map.canvas);
    map.setPaintProperty.mockClear();
    const requestCount = requests.length;
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "coverage-diagnostic-raw-route",
        "line-opacity",
        0,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "icon-opacity",
      0,
    );
    expect(map.setLayoutProperty).not.toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "visibility",
      "none",
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-opacity",
      0,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-stroke-opacity",
      0,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-finish",
      "text-opacity",
      0,
    );
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "coverage-diagnostic-raw-route",
        "line-opacity",
        1,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "icon-opacity",
      0.95,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-opacity",
      1,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-finish",
      "text-opacity",
      1,
    );
    expect(requests).toHaveLength(requestCount);
    map.setPaintProperty.mockClear();
    fireEvent.keyDown(coverage, { key: " ", code: "Space" });
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "coverage-diagnostic-raw-route",
      "line-opacity",
      0,
    );
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "coverage-diagnostic-raw-route",
        "line-opacity",
        0,
      ),
    );
    act(() => {
      window.dispatchEvent(new Event("blur"));
    });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "coverage-diagnostic-raw-route",
        "line-opacity",
        1,
      ),
    );
    expect(requests).toHaveLength(requestCount);
    map.handlers.get("style.load")?.();
    expect(map.sources.get("coverage-diagnostic-overlay").setData).toHaveBeenCalledWith(
      diagnostic.overlay,
    );
    expect(map.sources.get("coverage-diagnostic-raw-route").setData).not.toHaveBeenCalled();
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 20, y: 20 } }));
    expect(map.queryRenderedFeatures).toHaveBeenCalledWith(
      { x: 20, y: 20 },
      { layers: ["coverage-diagnostic-ambiguous", "coverage-diagnostic-matched"] },
    );
    expect(map.setFilter).not.toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
      "==",
      ["get", "portionOrdinal"],
      0,
    ]);
    await waitFor(() =>
      expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
        "==",
        ["get", "portionOrdinal"],
        0,
      ]),
    );
    expect(screen.getByText("Select a traversal on the map to review it.")).toBeInTheDocument();
    act(() => map.handlers.get("click")?.({ point: { x: 20, y: 20 } }));
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.getByText(/42.3 m traveled/)).toHaveTextContent(
      "42.3 m traveled / matched / forward",
    );
    await userEvent.click(screen.getByRole("button", { name: "Copy OSM segment ID" }));
    expect(writeText).toHaveBeenCalledWith("E".repeat(32));
    expect(
      await screen.findByRole("button", { name: "OSM segment ID copied" }),
    ).toBeInTheDocument();
    expect(document.activeElement).toBe(map.canvas);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 1 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 24, y: 24 } }));
    expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
      "==",
      ["get", "portionOrdinal"],
      0,
    ]);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 20, y: 20 } }));
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(map.setFilter).not.toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
      "==",
      ["get", "portionOrdinal"],
      1,
    ]);
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 1 } }]);
    act(() => map.handlers.get("mousemove")?.({ point: { x: 24, y: 24 } }));
    await waitFor(() =>
      expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
        "==",
        ["get", "portionOrdinal"],
        1,
      ]),
    );
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.queryByText("Traversal 2")).not.toBeInTheDocument();
    act(() => map.handlers.get("click")?.({ point: { x: 24, y: 24 } }));
    expect(screen.getByText("Traversal 2")).toBeInTheDocument();
    expect(screen.getByText(/18.0 m traveled/)).toHaveTextContent(
      "18.0 m traveled / ambiguous / reverse",
    );
    expect(screen.getByText(`OSM segment ${"F".repeat(32)}`)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy OSM segment ID" }));
    expect(writeText).toHaveBeenLastCalledWith("F".repeat(32));
    map.queryRenderedFeatures.mockReturnValue([{ properties: { portionOrdinal: 0 } }]);
    act(() => map.handlers.get("click")?.({ point: { x: 20, y: 20 } }));
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    expect(screen.getByText(`OSM segment ${"E".repeat(32)}`)).toBeInTheDocument();
    act(() => map.handlers.get("mouseleave")?.());
    expect(map.setFilter).toHaveBeenLastCalledWith("coverage-diagnostic-selected", [
      "==",
      ["get", "portionOrdinal"],
      0,
    ]);
    expect(screen.getByText("Traversal 1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Correct" }));
    await waitFor(() =>
      expect(requests).toContainEqual({
        path: `/api/coverage-diagnostic-runs/${diagnostic.id}/labels`,
        method: "PATCH",
        body: { overall: "correct", segments: diagnostic.labels.segments },
        csrf: "csrf-map",
      }),
    );
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Correct" }));
    map.setPaintProperty.mockClear();
    await userEvent.click(screen.getByRole("button", { name: "Routes" }));
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "icon-opacity-transition",
      { duration: 0, delay: 0 },
    );
    expect(
      screen.queryByRole("region", { name: "Coverage diagnostic review" }),
    ).not.toBeInTheDocument();
    expect(map.setLayoutProperty).toHaveBeenCalledWith(
      "private-workout-routes",
      "visibility",
      "visible",
    );
    await userEvent.click(coverage);
    expect(
      await screen.findByRole("region", { name: "Coverage diagnostic review" }),
    ).toHaveTextContent("Coverage review");
    expect(document.activeElement).toBe(map.canvas);
    expect(
      requests.filter((request) => request.path.endsWith("coverage-diagnostic-runs")),
    ).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Rerun" }));
    await waitFor(() =>
      expect(
        requests.filter((request) => request.path.endsWith("coverage-diagnostic-runs")),
      ).toHaveLength(2),
    );
    expect(mapInstances).toHaveLength(1);
    view.unmount();
    expect(map.removeLayer.mock.invocationCallOrder.at(-1)).toBeLessThan(
      map.removeSource.mock.invocationCallOrder.at(-1),
    );
  });

  test("shows required inactive OSM regions instead of zero-match review controls", async () => {
    const enabledConfig: PublicConfig = {
      ...config,
      features: { coverageMatcherDiagnostics: true },
    };
    const unavailable = {
      ...diagnostic,
      outcome: "no_evidence",
      unavailableRegions: [{ regionId: "geofabrik:new-york", displayName: "New York" }],
      counts: {
        ...diagnostic.counts,
        matchedPoints: 0,
        ambiguousPoints: 0,
        traversals: 0,
        portions: 0,
        uniqueSegments: 0,
      },
      overlay: { type: "FeatureCollection", features: [] },
      labels: { segments: [] },
    } as const;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (path.endsWith("/coverage-diagnostic-runs") && method === "POST")
        return Promise.resolve(json(unavailable, 201));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={enabledConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const review = await screen.findByRole("region", { name: "Coverage diagnostic review" });
    expect(within(review).getByRole("alert")).toHaveTextContent(
      "Coverage is unavailable because OSM data is not loaded for New York (geofabrik:new-york).",
    );
    expect(within(review).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(within(review).queryByRole("button", { name: "Fit" })).not.toBeInTheDocument();
    expect(within(review).queryByRole("button", { name: "Rerun" })).not.toBeInTheDocument();
    expect(within(review).queryByText("Overall result")).not.toBeInTheDocument();
    expect(within(review).queryByText("Matched")).not.toBeInTheDocument();
  });

  test("explains when all diagnostic workers are busy", async () => {
    const enabledConfig: PublicConfig = {
      ...config,
      features: { coverageMatcherDiagnostics: true },
    };
    let diagnosticRequests = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (path.endsWith("/coverage-diagnostic-runs") && method === "POST") {
        diagnosticRequests++;
        return Promise.resolve(
          json({ title: "Too Many Requests", detail: "coverage diagnostic capacity is busy" }, 429),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={enabledConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    expect(await screen.findByRole("alert", {}, { timeout: 3500 })).toHaveTextContent(
      "The diagnostic overlay could not be prepared because all available workers are currently busy. Please try again later.",
    );
    expect(diagnosticRequests).toBe(2);
  });

  test.each(["deleted", "foreign", "out-of-range", "empty", "persistent"])(
    "recovers fresh all-range %s focus once without backoff or narrowing the checked scope",
    async (scenario) => {
      const unavailableId = "F".repeat(32);
      history.replaceState({}, "", `/map?workoutId=${unavailableId}`);
      const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
      const cacheChanged = vi.fn();
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        if (String(input) === "/api/map-selections" && init?.method === "POST") {
          const body = JSON.parse(String(init.body));
          bodies.push(body);
          if (body.focusedWorkoutId === unavailableId || scenario === "persistent")
            return Promise.resolve(
              json({ title: "Not Found", detail: "private foreign workout or hidden FK" }, 404),
            );
          return Promise.resolve(
            json(
              scenario === "empty" ? { ...selection, workouts: [], bounds: null } : selection,
              201,
            ),
          );
        }
        if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
        throw new Error(`Unexpected ${input}`);
      });
      function Harness({ range }: { range: DateRangePreference }) {
        const [ids, setIds] = useState<string[] | undefined>();
        const [available, setAvailable] = useState<MapSelectionWorkout[]>([]);
        const [focus, setFocus] = useState<string | undefined>();
        return (
          <MapPage
            config={config}
            preferences={preferences}
            csrfToken="csrf-map"
            dateRange={range}
            onDateRangeSelected={vi.fn()}
            persistedWorkoutIds={ids}
            onWorkoutSelectionChange={setIds}
            persistedAvailableWorkouts={available}
            onAvailableWorkoutsChange={setAvailable}
            persistedFocusedWorkoutId={focus}
            onFocusedWorkoutChange={setFocus}
            onRoadCoverageCacheChange={cacheChanged}
            deletions={{ revision: 0, events: [] }}
          />
        );
      }
      // Flush network and React work without advancing time: 404 recovery must not back off.
      vi.useFakeTimers();
      let view!: ReturnType<typeof render>;
      await act(async () => {
        view = render(<Harness range="last30Days" />);
      });
      if (scenario === "persistent") {
        expect(screen.getByText("Routes could not be prepared for this map.")).toBeVisible();
      } else {
        if (scenario === "empty")
          expect(screen.getByText("No workout routes in this range.")).toBeVisible();
        else {
          expect(screen.getByRole("checkbox", { name: /Show Running/ })).toBeChecked();
          expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
        }
        expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument();
        expect(
          screen.queryByText("Routes could not be prepared for this map."),
        ).not.toBeInTheDocument();
        expect(location.search).toBe("");
        expect(cacheChanged).toHaveBeenCalledWith(undefined);
      }
      expect(bodies).toEqual([
        { dateRangeEnum: "last30Days", tz: "America/Denver", focusedWorkoutId: unavailableId },
        { dateRangeEnum: "last30Days", tz: "America/Denver" },
      ]);
      expect(document.body).not.toHaveTextContent("private foreign workout or hidden FK");
      await act(async () => vi.advanceTimersByTimeAsync(30_000));
      expect(bodies).toHaveLength(2);
      vi.useRealTimers();
      if (scenario !== "persistent") {
        view.rerender(<Harness range="last7Days" />);
        await waitFor(() => expect(bodies).toHaveLength(3));
        expect(bodies[2]).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver" });
      }
    },
  );

  test.each([false, true])(
    "bounds explicit 404 membership recovery without selecting unchecked routes (retry fails: %s)",
    async (retryFails) => {
      const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        if (String(input) === "/api/map-selections" && init?.method === "POST") {
          const body = JSON.parse(String(init.body));
          bodies.push(body);
          if (bodies.length === 1 || (retryFails && bodies.length === 3))
            return Promise.resolve(json({ title: "Unavailable" }, 404));
          return Promise.resolve(
            json({
              ...selection,
              workouts: body.workoutIds === undefined ? [selection.workouts[1]] : [],
            }),
          );
        }
        if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
        throw new Error(`Unexpected ${input}`);
      });
      function Harness() {
        const [ids, setIds] = useState<string[] | undefined>([selection.workouts[0].id]);
        const [available, setAvailable] = useState(selection.workouts);
        const [focus, setFocus] = useState<string | undefined>(selection.workouts[0].id);
        return (
          <MapPage
            config={config}
            preferences={preferences}
            csrfToken="csrf-map"
            dateRange="last30Days"
            onDateRangeSelected={vi.fn()}
            persistedWorkoutIds={ids}
            onWorkoutSelectionChange={setIds}
            persistedAvailableWorkouts={available}
            onAvailableWorkoutsChange={setAvailable}
            persistedFocusedWorkoutId={focus}
            onFocusedWorkoutChange={setFocus}
          />
        );
      }
      render(<Harness />);
      await waitFor(() => expect(bodies).toHaveLength(3));
      expect(bodies[1]).toEqual({ dateRangeEnum: "last30Days", tz: "America/Denver" });
      expect(bodies[2]).toEqual({
        dateRangeEnum: "last30Days",
        tz: "America/Denver",
        workoutIds: [],
      });
      expect(
        screen.queryByRole("button", { name: /Running.*8\/08\/2026/ }),
      ).not.toBeInTheDocument();
      expect(screen.getByRole("checkbox", { name: /Hiking/ })).not.toBeChecked();
      if (retryFails)
        expect(
          await screen.findByText("Routes could not be prepared for this map."),
        ).toBeInTheDocument();
      expect(bodies).toHaveLength(3);
    },
  );

  test.each(["local", "shell", "valid-focus", "unchanged"])(
    "does not remerge unavailable requested IDs during explicit %s recovery",
    async (mode) => {
      const missingId = "F".repeat(32);
      const validId = selection.workouts[0].id;
      const validFocus = mode === "valid-focus" || mode === "unchanged";
      if (!validFocus) history.replaceState({}, "", `/map?workoutId=${missingId}`);
      const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        const path = String(input);
        if (path === "/api/map-selections" && init?.method === "POST") {
          const body = JSON.parse(String(init.body));
          bodies.push(body);
          if (body.workoutIds?.includes(missingId) || (mode === "unchanged" && bodies.length === 1))
            return Promise.resolve(json({ title: "Not Found" }, 404));
          return Promise.resolve(
            json(
              {
                ...selection,
                focusedWorkoutId: body.focusedWorkoutId ?? null,
                workouts: selection.workouts.filter(
                  (workout) =>
                    (mode !== "unchanged" || workout.id === validId) &&
                    (body.workoutIds === undefined || body.workoutIds.includes(workout.id)),
                ),
              },
              201,
            ),
          );
        }
        if (path.endsWith("/route/points")) return Promise.resolve(json({ points: [] }));
        if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
        throw new Error(`Unexpected ${input}`);
      });
      function Harness() {
        const [ids, setIds] = useState<string[] | undefined>(
          mode === "unchanged"
            ? [validId]
            : mode === "valid-focus"
              ? [validId, missingId]
              : [missingId],
        );
        const [available, setAvailable] = useState(selection.workouts);
        const [focus, setFocus] = useState<string | undefined>(validFocus ? validId : missingId);
        return (
          <MapPage
            config={config}
            preferences={preferences}
            csrfToken="csrf-map"
            dateRange="last30Days"
            onDateRangeSelected={vi.fn()}
            {...(mode === "local"
              ? {}
              : {
                  persistedWorkoutIds: ids,
                  onWorkoutSelectionChange: setIds,
                  persistedAvailableWorkouts: available,
                  onAvailableWorkoutsChange: setAvailable,
                  persistedFocusedWorkoutId: focus,
                  onFocusedWorkoutChange: setFocus,
                })}
          />
        );
      }
      render(<Harness />);
      await waitFor(() => expect(bodies).toHaveLength(3));
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      expect(bodies.at(-1)).toEqual({
        dateRangeEnum: "last30Days",
        tz: "America/Denver",
        workoutIds: validFocus ? [validId] : [],
        ...(validFocus ? { focusedWorkoutId: validId } : {}),
      });
      if (mode === "unchanged")
        expect(screen.queryByRole("checkbox", { name: /Show Hiking/ })).not.toBeInTheDocument();
      else expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).not.toBeChecked();
      expect(screen.getByRole("checkbox", { name: /Show Running/ })).toHaveProperty(
        "checked",
        validFocus,
      );
      expect(location.search).toBe("");
      expect(
        screen.queryByText("Routes could not be prepared for this map."),
      ).not.toBeInTheDocument();
      if (validFocus)
        expect(
          screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li"),
        ).toHaveClass("is-focused");
      vi.useFakeTimers();
      await act(async () => vi.advanceTimersByTimeAsync(30_000));
      expect(bodies).toHaveLength(3);
    },
  );

  test.each(["range", "deletion"])(
    "discards a late all-range recovery catalog after a newer %s revision",
    async (change) => {
      const missingId = "F".repeat(32);
      history.replaceState({}, "", `/map?workoutId=${missingId}`);
      let resolveCatalog!: (response: Response) => void;
      let catalogSignal: AbortSignal | null | undefined;
      let posts = 0;
      const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        if (String(input) === "/api/map-selections" && init?.method === "POST") {
          posts++;
          if (posts === 1) return Promise.resolve(json({ title: "Not Found" }, 404));
          if (posts === 2) {
            catalogSignal = init.signal;
            return new Promise((resolve) => {
              resolveCatalog = resolve;
            });
          }
          return Promise.resolve(json({ ...selection, workouts: [selection.workouts[1]] }, 201));
        }
        if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
        throw new Error(`Unexpected ${input}`);
      });
      const update = vi.fn();
      const focusUpdate = vi.fn();
      const props = {
        config,
        preferences,
        csrfToken: "csrf-map",
        dateRange: "last30Days" as DateRangePreference,
        onDateRangeSelected: vi.fn(),
        onWorkoutSelectionChange: vi.fn(),
        onAvailableWorkoutsChange: update,
        onFocusedWorkoutChange: focusUpdate,
      };
      const view = render(<MapPage {...props} />);
      await waitFor(() => expect(posts).toBe(2));
      view.rerender(
        <MapPage
          {...props}
          dateRange={change === "range" ? "last7Days" : "last30Days"}
          deletions={
            change === "deletion"
              ? { revision: 1, events: [{ workoutIds: [selection.workouts[0].id] }] }
              : undefined
          }
        />,
      );
      await waitFor(() => expect(update).toHaveBeenCalledExactlyOnceWith([selection.workouts[1]]));
      const focusesBefore = focusUpdate.mock.calls.length;
      expect(catalogSignal?.aborted).toBe(true);
      const oldCatalog = { ...selection, id: "E".repeat(32) };
      await act(async () => resolveCatalog(json(oldCatalog, 201)));
      expect(update).toHaveBeenCalledTimes(1);
      expect(focusUpdate).toHaveBeenCalledTimes(focusesBefore);
      expect(posts).toBe(3);
      expect(
        fetch.mock.calls.some(
          ([input, init]) =>
            String(input) === `/api/map-selections/${oldCatalog.id}` && init?.method === "DELETE",
        ),
      ).toBe(true);
    },
  );

  test("aborts a late capability on accepted deletion and never restores its catalog", async () => {
    let resolveOld!: (response: Response) => void;
    let oldSignal: AbortSignal | null | undefined;
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") {
        posts++;
        if (posts === 1) {
          oldSignal = init.signal;
          return new Promise((resolve) => {
            resolveOld = resolve;
          });
        }
        return Promise.resolve(json({ ...selection, workouts: [selection.workouts[1]] }));
      }
      if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
      throw new Error(`Unexpected ${input}`);
    });
    const update = vi.fn();
    const props = {
      config,
      preferences,
      csrfToken: "csrf-map",
      dateRange: "last30Days" as const,
      onDateRangeSelected: vi.fn(),
      persistedAvailableWorkouts: selection.workouts,
      onAvailableWorkoutsChange: update,
    };
    const view = render(<MapPage {...props} />);
    await waitFor(() => expect(posts).toBe(1));
    view.rerender(
      <MapPage
        {...props}
        persistedAvailableWorkouts={[selection.workouts[1]]}
        deletions={{ revision: 1, events: [{ workoutIds: [selection.workouts[0].id] }] }}
      />,
    );
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => {
      resolveOld(json(selection));
    });
    expect(update).toHaveBeenCalledTimes(1);
    expect(update.mock.calls[0][0]).toEqual([selection.workouts[1]]);
  });

  test("excludes focus outside an explicit selection, including none", () => {
    expect(selectionRequest("last30Days", "UTC", [], "A".repeat(32))).not.toHaveProperty(
      "focusedWorkoutId",
    );
    expect(
      selectionRequest("last30Days", "UTC", ["B".repeat(32)], "A".repeat(32)),
    ).not.toHaveProperty("focusedWorkoutId");
  });

  test("accepts new same-day imports after range deletion while rejecting an obsolete unknown bootstrap", async () => {
    const knownDeleted = selection.workouts[0];
    const newImport = {
      ...knownDeleted,
      id: "F".repeat(32),
      type: { ...knownDeleted.type, name: "New import" },
    };
    const unknownOld = {
      ...knownDeleted,
      id: "E".repeat(32),
      type: { ...knownDeleted.type, name: "Obsolete bootstrap" },
    };
    let resolveOld!: (response: Response) => void;
    let oldSignal: AbortSignal | null | undefined;
    const bodies: Array<{ workoutIds?: string[] }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        bodies.push(body);
        if (bodies.length === 1) {
          oldSignal = init.signal;
          return new Promise((resolve) => {
            resolveOld = resolve;
          });
        }
        return Promise.resolve(
          json({
            ...selection,
            workouts: body.workoutIds === undefined ? [newImport, knownDeleted] : [],
          }),
        );
      }
      if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
      throw new Error(`Unexpected ${input}`);
    });
    const update = vi.fn();
    const onSelectionChange = vi.fn();
    function Harness({ revision }: { revision: number }) {
      const [available, setAvailable] = useState<MapSelectionWorkout[]>([]);
      return (
        <MapPage
          config={config}
          preferences={preferences}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          persistedWorkoutIds={[]}
          onWorkoutSelectionChange={onSelectionChange}
          persistedAvailableWorkouts={available}
          onAvailableWorkoutsChange={(workouts) => {
            update(workouts);
            setAvailable(workouts);
          }}
          deletions={{
            revision,
            events: revision
              ? [
                  {
                    workoutIds: [knownDeleted.id],
                    range: { startDate: "2026-08-08", endDate: "2026-08-08" },
                  },
                ]
              : [],
          }}
        />
      );
    }
    const view = render(<Harness revision={0} />);
    await waitFor(() => expect(bodies).toHaveLength(1));
    view.rerender(<Harness revision={1} />);
    expect(
      await screen.findByRole("button", { name: /New import.*8\/08\/2026/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /New import/ })).not.toBeChecked();
    expect(screen.queryByRole("button", { name: /Running.*8\/08\/2026/ })).not.toBeInTheDocument();
    expect(update).toHaveBeenCalledExactlyOnceWith([newImport]);
    expect(bodies).toHaveLength(3);
    expect(bodies[2].workoutIds).toEqual([]);
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => {
      resolveOld(json({ ...selection, workouts: [knownDeleted, unknownOld] }));
    });
    expect(update).toHaveBeenCalledExactlyOnceWith([newImport]);
    expect(bodies).toHaveLength(3);
    expect(screen.queryByRole("button", { name: /Obsolete bootstrap/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /New import.*8\/08\/2026/ })).toBeInTheDocument();
  });

  test("aborts and ignores a late diagnostic when focus changes", async () => {
    const enabledConfig: PublicConfig = {
      ...config,
      features: { coverageMatcherDiagnostics: true },
    };
    let resolveDiagnostic!: (response: Response) => void;
    const diagnosticRequest: { signal?: AbortSignal } = {};
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (path.endsWith("/coverage-diagnostic-runs") && method === "POST") {
        diagnosticRequest.signal = init?.signal ?? undefined;
        return new Promise((resolve) => {
          resolveDiagnostic = resolve;
        });
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={enabledConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const preparing = await screen.findByRole("status", { name: "" });
    expect(preparing).toHaveClass("coverage-diagnostic-preparing");
    expect(preparing).toHaveAttribute("aria-busy", "true");
    expect(preparing).toHaveTextContent("Preparing diagnostic overlay...");
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("coverage-diagnostic-raw-route", {
        type: "geojson",
        data: rawRoute,
      }),
    );
    expect(map.addSource).toHaveBeenCalledWith("private-workout-route-endpoints", {
      type: "geojson",
      data: rawRouteEndpoints,
    });
    expect(map.sources.has("coverage-diagnostic-overlay")).toBe(false);
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "coverage-diagnostic-raw-route",
      "line-opacity",
      1,
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-opacity",
      1,
    );
    await userEvent.click(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }));
    expect(diagnosticRequest.signal?.aborted).toBe(true);
    resolveDiagnostic(json(diagnostic, 201));
    await act(async () => {
      await Promise.resolve();
    });
    expect(
      screen.queryByRole("region", { name: "Coverage diagnostic review" }),
    ).not.toBeInTheDocument();
    expect([...mapInstances.at(-1)!.sources.keys()]).not.toContain("coverage-diagnostic-overlay");
    expect(mapInstances).toHaveLength(1);
  });

  test("creates and deletes a private selection, restores private layers, and renders active attribution", async () => {
    const requests: Array<{ path: string; method: string; body?: unknown; csrf: string | null }> =
      [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      requests.push({
        path,
        method,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
        csrf: new Headers(init?.headers).get("X-CSRF-Token"),
      });
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(
          json({
            ...selection,
            id: selection.id.toLowerCase(),
            workouts: selection.workouts.map((workout) => ({
              ...workout,
              id: workout.id.toLowerCase(),
            })),
          }),
        );
      if (path === `/api/map-selections/${selection.id}` && method === "DELETE")
        return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const view = render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select date range" })).toHaveClass("range-trigger");
    expect(screen.getByRole("button", { name: "Select base map" })).toHaveClass("range-trigger");
    expect(screen.queryByText("Route atlas")).not.toBeInTheDocument();
    expect(screen.queryByText("Visible routes")).not.toBeInTheDocument();
    expect(screen.queryByText(/Coverage \/ Milestone/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Routes" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(screen.getByRole("button", { name: "Coverage" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Select all workout routes" })).toBeChecked();
    expect(screen.queryByText("Coverage map data is pending")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Active map attribution for Road")).toHaveTextContent("Road map");
    expect(screen.queryByRole("link", { name: "Map data" })).not.toBeInTheDocument();
    expect(requests[0]).toEqual({
      path: "/api/map-selections",
      method: "POST",
      body: { dateRangeEnum: "last30Days", tz: "America/Denver" },
      csrf: "csrf-map",
    });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl)],
      }),
    );
    expect(
      map.addLayer.mock.calls.some(
        (call: any[]) =>
          call[0]["source-layer"] === "routes" &&
          call[0].layout["line-sort-key"][1] === "sortOrder",
      ),
    ).toBe(true);
    expect([...map.sources.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    expect([...map.layers.keys()].some((id) => id.toLowerCase().includes("coverage"))).toBe(false);
    const routeLayer = map.addLayer.mock.calls.find(
      (call: any[]) => call[0].id === "private-workout-routes",
    )?.[0];
    expect(
      routeLayer.paint["line-color"].filter((value: unknown) => value === "running"),
    ).toHaveLength(1);
    expect(
      map.addLayer.mock.calls.some(
        (call: any[]) =>
          call[0].paint["line-color"] === "#c026ff" && call[0].paint["line-opacity"] === 1,
      ),
    ).toBe(true);
    const markerLayer = map.addLayer.mock.calls.find(
      (call: any[]) => call[0].id === "private-workout-route-markers",
    )?.[0];
    expect(markerLayer).toMatchObject({
      type: "circle",
      "source-layer": "routes",
      filter: ["==", ["geometry-type"], "Point"],
    });
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "private-workout-route-marker-hover",
      )?.[0].paint["circle-color"],
    ).toBe("#c026ff");
    expect(screen.getByRole("application", { name: "Workout route map" })).not.toHaveAttribute(
      "aria-keyshortcuts",
    );
    map.setPaintProperty.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-routes",
      "line-opacity",
      0,
    );
    expect(map.fitBounds).toHaveBeenCalledWith(
      [
        [-105.3, 39.8],
        [-105.1, 40.1],
      ],
      { padding: 48, duration: 350 },
    );
    view.unmount();
    await waitFor(() =>
      expect(requests).toContainEqual({
        path: `/api/map-selections/${selection.id}`,
        method: "DELETE",
        body: undefined,
        csrf: "csrf-map",
      }),
    );
    expect(map.remove).toHaveBeenCalled();
  });

  test("shows aggregate coverage and statistics when diagnostics preference is disabled", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const coverageEntity = {
      entityId: "E".repeat(32),
      entityKind: "park",
      name: "Example Park",
      localityName: null,
      regionId: "geofabrik:norcal",
      regionName: "Northern California",
      broadClass: "park",
      rangeWorkoutCount: 3,
      rangeFirstDate: "2026-07-12",
      rangeFirstWorkoutId: selection.workouts[1].id,
      rangeLatestDate: "2026-08-05",
      rangeLatestWorkoutId: selection.workouts[0].id,
      allTimeWorkoutCount: 7,
      allTimeFirstDate: "2026-01-01",
      allTimeFirstWorkoutId: selection.workouts[1].id,
      allTimeLatestDate: "2026-08-05",
      allTimeLatestWorkoutId: selection.workouts[0].id,
      bounds: selection.bounds,
    } as const;
    const secondEntity = {
      ...coverageEntity,
      entityId: "D".repeat(32),
      entityKind: "path",
      name: "Alpha Road",
      localityName: "Alpha City",
      broadClass: "road",
      rangeWorkoutCount: 1,
    } as const;
    const unnamedEntity = {
      ...secondEntity,
      entityId: "C".repeat(32),
      name: null,
      localityName: null,
      broadClass: "footway",
    } as const;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.startsWith(`/api/map-selections/${selection.id}/coverage/paths?`)) {
        expect(new URL(path, "https://test").searchParams).toEqual(
          new URLSearchParams({ generation: String(selection.dataGeneration), bulk: "true" }),
        );
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 50000, totalItems: 3, totalPages: 1 },
            items: [coverageEntity, secondEntity, unnamedEntity],
          }),
        );
      }
      if (
        path ===
        `/api/map-selections/${selection.id}/coverage/park/${coverageEntity.entityId}?generation=${selection.dataGeneration}`
      )
        return Promise.resolve(
          json({
            ...coverageEntity,
            geometry: {
              type: "LineString",
              coordinates: [
                [-105.2, 40],
                [-105.19, 40.01],
              ],
            },
            fitBounds: {
              minimumLongitude: -105.205,
              minimumLatitude: 39.995,
              maximumLongitude: -105.185,
              maximumLatitude: 40.015,
            },
          }),
        );
      if (path === `/api/map-selections/${selection.id}` && method === "DELETE")
        return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function CachedCoverageMap() {
      const [cache, setCache] = useState<RoadCoverageCache>();
      return (
        <MapPage
          config={{ ...config, features: { coverageMatcherDiagnostics: true } }}
          preferences={productionPreferences}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          persistedFocusedWorkoutId={selection.workouts[0].id}
          onFocusedWorkoutChange={vi.fn()}
          roadCoverageCache={cache}
          onRoadCoverageCacheChange={setCache}
        />
      );
    }
    render(<CachedCoverageMap />);
    const coverage = await screen.findByRole("button", { name: "Coverage" });
    expect(coverage).toBeEnabled();
    await userEvent.click(coverage);
    expect(await screen.findByRole("region", { name: "Coverage statistics" })).toHaveTextContent(
      "Road coverage",
    );
    expect(screen.getByLabelText("Coverage workout count legend")).toHaveTextContent(
      "26+ workouts",
    );
    const map = mapInstances.at(-1)!;
    expect(screen.getByRole("button", { name: "Fit" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("dialog", { name: "Road Coverage" })).toBeVisible();
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(
      screen
        .getByRole("button", { name: "Example Park" })
        .closest("tr")!
        .querySelectorAll(".coverage-cell-value"),
    ).toHaveLength(screen.getAllByRole("columnheader").length);
    expect(screen.queryByRole("region", { name: "Coverage statistics" })).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Workouts/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    expect(screen.getAllByRole("button", { name: "Jul 12, 2026" })[0]).toBeVisible();
    expect(screen.getAllByText("Jan 1, 2026")[0].tagName).toBe("SPAN");
    const coverageRequestCount = vi
      .mocked(globalThis.fetch)
      .mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length;
    expect(coverageRequestCount).toBe(1);
    await userEvent.click(screen.getByRole("button", { name: /City\/County\/Region/ }));
    expect(screen.getByRole("columnheader", { name: /City\/County\/Region/ })).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    expect(
      within(screen.getByRole("table"))
        .getAllByRole("button", { name: /Alpha Road|Example Park/ })
        .map((button) => button.textContent),
    ).toEqual(["Alpha Road", "Example Park"]);
    expect(screen.queryByRole("button", { name: "Pedestrian path" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }));
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    expect(screen.getAllByText("Northern California").length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: /Workouts/ }));
    expect(
      within(screen.getByRole("table"))
        .getAllByRole("button", { name: /Example Park|Alpha Road|Pedestrian path/ })
        .map((button) => button.textContent),
    ).toEqual(["Example Park", "Alpha Road", "Pedestrian path"]);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "northern california" } });
    await waitFor(
      () => expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible(),
      { timeout: 1000 },
    );
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "path" } });
    await waitFor(
      () => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(),
      { timeout: 1000 },
    );
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "pedestrian" } });
    await waitFor(
      () => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(),
      { timeout: 1000 },
    );
    expect(screen.getByRole("button", { name: "Pedestrian path" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "not present" } });
    await waitFor(
      () => expect(screen.getByText("No roads, paths, or parks match this search.")).toBeVisible(),
      { timeout: 1000 },
    );
    expect(screen.getByText("Page 1 of 1", { selector: ".pagination span" })).toBeVisible();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "park" } });
    await waitFor(
      () => expect(screen.getByRole("button", { name: "Example Park" })).toBeVisible(),
      { timeout: 1000 },
    );
    await waitFor(
      () => expect(screen.queryByRole("button", { name: "Alpha Road" })).not.toBeInTheDocument(),
      { timeout: 1000 },
    );
    expect(
      vi
        .mocked(globalThis.fetch)
        .mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length,
    ).toBe(coverageRequestCount);
    await userEvent.click(screen.getByRole("button", { name: "Example Park" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-coverage", {
        type: "vector",
        tiles: [privateTemplate(selection.coverageTileUrl)],
        minzoom: 0,
        maxzoom: 22,
      }),
    );
    expect(map.addSource).toHaveBeenCalledWith(
      "private-workout-coverage-focus",
      expect.objectContaining({ type: "geojson" }),
    );
    expect(
      map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage")?.[0][
        "source-layer"
      ],
    ).toBe("coverage");
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "private-workout-coverage-focus",
      )?.[0].filter,
    ).toEqual(["==", ["get", "sourceEntityKind"], "path"]);
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "private-workout-coverage-focus",
      )?.[0].source,
    ).toBe("private-workout-coverage-focus");
    expect(
      map.addLayer.mock.calls.find((call: any[]) => call[0].id === "private-workout-coverage")?.[0]
        .paint["line-color"][1],
    ).toEqual(["get", "countBucket"]);
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "private-workout-coverage-focus",
      )?.[0].paint["line-color"][1],
    ).toEqual(["get", "countBucket"]);
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-coverage-focus");
    expect(map.moveLayer).toHaveBeenCalledWith("private-workout-coverage-parks-focus");
    expect(
      map.addLayer.mock.calls.find(
        (call: any[]) => call[0].id === "private-workout-coverage-focus",
      )?.[0].paint["line-color"],
    ).toContain("#ffd8f0");
    for (const layer of ["private-workout-coverage", "private-workout-coverage-parks"]) {
      expect(
        map.addLayer.mock.calls.find((call: any[]) => call[0].id === layer)?.[0].paint[
          "line-opacity-transition"
        ],
      ).toEqual({ duration: 400, delay: 0 });
    }
    expect(map.setLayoutProperty).toHaveBeenCalledWith(
      "private-workout-coverage",
      "visibility",
      "visible",
    );
    expect(map.setLayoutProperty).toHaveBeenCalledWith(
      "private-workout-routes",
      "visibility",
      "none",
    );
    expect(map.addSource).toHaveBeenCalledWith(
      "private-workout-coverage-highlight",
      expect.objectContaining({ type: "geojson" }),
    );
    expect(map.fitBounds).toHaveBeenCalledWith(
      [
        [-105.205, 39.995],
        [-105.185, 40.015],
      ],
      { padding: 48, duration: 350 },
    );
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute(
      "aria-keyshortcuts",
      "Space",
    );
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    map.removeLayer.mockClear();
    map.removeSource.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "private-workout-coverage",
        "line-opacity",
        0,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-coverage-parks",
      "line-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-coverage-focus",
      "line-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-coverage-parks-focus",
      "line-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-coverage-highlight",
      "line-opacity",
      0,
    );
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "private-workout-coverage",
        "line-opacity",
        0.94,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-coverage-parks",
      "line-opacity",
      0.94,
    );
    for (const layer of ["private-workout-coverage", "private-workout-coverage-parks"]) {
      expect(map.setLayoutProperty).not.toHaveBeenCalledWith(layer, "visibility", "none");
      expect(map.removeLayer).not.toHaveBeenCalledWith(layer);
    }
    expect(map.removeSource).not.toHaveBeenCalled();
    const requestsAfterInitialLoad = vi
      .mocked(globalThis.fetch)
      .mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length;
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    fireEvent.keyDown(screen.getByRole("dialog", { name: "Road Coverage" }), { key: "Escape" });
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    expect(
      vi
        .mocked(globalThis.fetch)
        .mock.calls.filter(([input]) => String(input).includes("/coverage/paths?")).length,
    ).toBe(requestsAfterInitialLoad);
    expect(
      vi
        .mocked(globalThis.fetch)
        .mock.calls.some(([input]) => String(input).includes("coverage-diagnostic-runs")),
    ).toBe(false);
  });

  test("switches pink production Coverage to delayed list hover without replacing persistent focus", async () => {
    const focusedSelection = { ...selection, focusedWorkoutId: selection.workouts[0].id };
    const focusGeometry = (offset: number) => ({
      type: "FeatureCollection",
      features: [
        {
          type: "Feature",
          properties: { entityKind: "path", countBucket: 1 },
          geometry: {
            type: "LineString",
            coordinates: [
              [-105.2 + offset, 40],
              [-105.19 + offset, 40.01],
            ],
          },
        },
      ],
    });
    const focusRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(focusedSelection));
      if (path.includes("/coverage-focus/")) {
        focusRequests.push(path);
        return Promise.resolve(
          json(path.includes(selection.workouts[1].id) ? focusGeometry(0.01) : focusGeometry(0)),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const onFocusedWorkoutChange = vi.fn();
    render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
        persistedFocusedWorkoutId={selection.workouts[0].id}
        onFocusedWorkoutChange={onFocusedWorkoutChange}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    const aggregateSources = () =>
      map.addSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage");
    await waitFor(() =>
      expect(focusRequests.some((path) => path.includes(selection.workouts[0].id))).toBe(true),
    );
    const focusSource = map.getSource("private-workout-coverage-focus") as {
      setData: ReturnType<typeof vi.fn>;
    };
    const aggregateAddsBeforeHover = aggregateSources().length;
    const aggregateRemovalsBeforeHover = map.removeSource.mock.calls.filter(
      (call: any[]) => call[0] === "private-workout-coverage",
    ).length;
    const fitsBeforeHover = map.fitBounds.mock.calls.length;
    const stylesBeforeHover = map.setStyle.mock.calls.length;

    vi.useFakeTimers();
    const hikingButton = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ });
    fireEvent.pointerEnter(hikingButton);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(249);
    });
    expect(focusRequests.filter((path) => path.includes(selection.workouts[1].id))).toHaveLength(0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(focusRequests.filter((path) => path.includes(selection.workouts[1].id))).toHaveLength(1);
    expect(focusSource.setData).toHaveBeenLastCalledWith(focusGeometry(0.01));
    expect(aggregateSources()).toHaveLength(aggregateAddsBeforeHover);
    expect(
      map.removeSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage"),
    ).toHaveLength(aggregateRemovalsBeforeHover);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeHover);
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeHover);
    expect(onFocusedWorkoutChange).not.toHaveBeenCalled();

    fireEvent.pointerLeave(hikingButton.closest("ol")!);
    await act(async () => {
      await Promise.resolve();
    });
    expect(focusSource.setData).toHaveBeenLastCalledWith(focusGeometry(0));
    expect(aggregateSources()).toHaveLength(aggregateAddsBeforeHover);
    expect(
      map.removeSource.mock.calls.filter((call: any[]) => call[0] === "private-workout-coverage"),
    ).toHaveLength(aggregateRemovalsBeforeHover);
    expect(focusRequests.filter((path) => path.includes(selection.workouts[0].id))).toHaveLength(1);
    vi.useRealTimers();
  });

  test("shows Coverage readiness, disables result-less focus, and polls until a result is applied", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const pendingSelection: MapSelection = {
      ...selection,
      workouts: [
        {
          ...selection.workouts[0],
          coverageReadiness: {
            mapDataStatus: "ready",
            processingStatus: "queued",
            resultStatus: "none",
          },
        },
        {
          ...selection.workouts[1],
          coverageReadiness: {
            mapDataStatus: "ready",
            processingStatus: "failed",
            resultStatus: "stale",
          },
        },
      ],
    };
    const currentSelection: MapSelection = {
      ...pendingSelection,
      id: "D".repeat(32),
      workouts: [
        {
          ...pendingSelection.workouts[0],
          coverageReadiness: {
            mapDataStatus: "ready",
            processingStatus: "current",
            resultStatus: "current",
          },
        },
        pendingSelection.workouts[1],
      ],
    };
    let selectionPosts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        selectionPosts++;
        return Promise.resolve(json(selectionPosts < 3 ? pendingSelection : currentSelection));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={productionPreferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const running = await screen.findByRole("button", { name: /Running.*8\/08\/2026/ });
    expect(running.querySelector(".route-swatch")).toBeInTheDocument();

    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    const unavailableRunning = screen.getByRole("button", {
      name: /Coverage pending.*Running.*8\/08\/2026/,
    });
    const staleHiking = screen.getByRole("button", { name: /Coverage stale.*Hiking.*8\/01\/2026/ });
    expect(unavailableRunning).toBeDisabled();
    expect(unavailableRunning.closest("li")).toHaveClass("is-coverage-unavailable");
    expect(unavailableRunning.querySelector(".coverage-route-status--pending")).toBeInTheDocument();
    expect(screen.getByText("Waiting for coverage update")).toHaveAttribute("role", "tooltip");
    expect(staleHiking).toBeEnabled();
    expect(staleHiking.querySelector(".coverage-route-status--stale")).toBeInTheDocument();
    expect(
      screen
        .queryByRole("button", { name: /Running.*8\/08\/2026/ })
        ?.querySelector(".route-swatch"),
    ).not.toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(9_999);
    });
    expect(selectionPosts).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(selectionPosts).toBe(2);
    expect(
      screen.getByRole("button", { name: /Coverage pending.*Running.*8\/08\/2026/ }),
    ).toBeDisabled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(selectionPosts).toBe(3);
    const currentRunning = screen.getByRole("button", {
      name: /Coverage current.*Running.*8\/08\/2026/,
    });
    expect(currentRunning).toBeEnabled();
    expect(currentRunning.querySelector(".coverage-route-status--current")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(selectionPosts).toBe(3);
    vi.useRealTimers();
  });

  test.each([
    { label: "pending", processingStatus: "queued", resultStatus: "none", interval: 10_000 },
    { label: "current", processingStatus: "current", resultStatus: "current", interval: 30_000 },
    { label: "stale", processingStatus: "running", resultStatus: "stale", interval: 30_000 },
  ] as const)(
    "renews $label Coverage when its generation changes without a readiness change",
    async ({ processingStatus, resultStatus, interval }) => {
      const initial: MapSelection = {
        ...selection,
        workouts: selection.workouts.map((workout) => ({
          ...workout,
          coverageReadiness: { mapDataStatus: "ready", processingStatus, resultStatus },
        })),
      };
      const updated: MapSelection = {
        ...initial,
        id: "E".repeat(32),
        dataGeneration: initial.dataGeneration + 1,
        coverageTileUrl: `/api/map-selections/${"E".repeat(32)}/coverage-tiles/${initial.dataGeneration + 1}/{z}/{x}/{y}.pbf`,
      };
      let posts = 0;
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        if (String(input) === "/api/map-selections" && init?.method === "POST") {
          posts++;
          return Promise.resolve(
            json(
              posts === 1 ? initial : posts === 2 ? { ...initial, id: "D".repeat(32) } : updated,
            ),
          );
        }
        if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
        throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
      });
      const view = render(
        <MapPage
          config={config}
          preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
        />,
      );
      await screen.findByRole("checkbox", { name: /Show Running/ });
      await waitFor(() => expect(screen.getByRole("button", { name: "Coverage" })).toBeEnabled());
      vi.useFakeTimers();
      fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
      const map = mapInstances.at(-1)!;
      const sourceAdds = map.addSource.mock.calls.length;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(interval);
      });
      expect(posts).toBe(2);
      expect(map.addSource.mock.calls.length).toBe(sourceAdds);
      expect(globalThis.fetch).toHaveBeenCalledWith(
        `/api/map-selections/${"D".repeat(32)}`,
        expect.objectContaining({ method: "DELETE" }),
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(interval);
      });
      expect(posts).toBe(3);
      expect(map.addSource).toHaveBeenCalledWith(
        "private-workout-coverage",
        expect.objectContaining({ tiles: [privateTemplate(updated.coverageTileUrl)] }),
      );
      expect(mapInstances).toHaveLength(1);
      fireEvent.click(screen.getByRole("button", { name: "Routes" }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(posts).toBe(3);
      view.unmount();
      vi.useRealTimers();
    },
  );

  test("pauses Coverage polling in a hidden tab and resumes without foreground loading", async () => {
    let visibility: DocumentVisibilityState = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    const pending: MapSelection = {
      ...selection,
      workouts: selection.workouts.map((workout) => ({
        ...workout,
        coverageReadiness: {
          mapDataStatus: "ready",
          processingStatus: "queued",
          resultStatus: "none",
        },
      })),
    };
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST") {
        posts++;
        return Promise.resolve(json(pending));
      }
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    const view = render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() => expect(screen.getByRole("button", { name: "Coverage" })).toBeEnabled());
    visibility = "hidden";
    vi.useFakeTimers();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Coverage" }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(posts).toBe(1);
    visibility = "visible";
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(posts).toBe(2);
    expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument();
    await act(async () => {
      view.unmount();
    });
    vi.useRealTimers();
  });

  test("explains unavailable map data and failed Coverage updates on disabled rows", async () => {
    const unavailableSelection: MapSelection = {
      ...selection,
      workouts: [
        {
          ...selection.workouts[0],
          coverageReadiness: {
            mapDataStatus: "unavailable",
            processingStatus: "unprocessed",
            resultStatus: "none",
          },
        },
        {
          ...selection.workouts[1],
          coverageReadiness: {
            mapDataStatus: "ready",
            processingStatus: "failed",
            resultStatus: "none",
          },
        },
      ],
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(unavailableSelection));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
    const posts = vi
      .mocked(globalThis.fetch)
      .mock.calls.filter(
        ([input, init]) => String(input) === "/api/map-selections" && init?.method === "POST",
      ).length;
    vi.useFakeTimers();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(
      vi
        .mocked(globalThis.fetch)
        .mock.calls.filter(
          ([input, init]) => String(input) === "/api/map-selections" && init?.method === "POST",
        ),
    ).toHaveLength(posts);
    vi.useRealTimers();
  });

  test("renders every Coverage status with its production indicator and tooltip", async () => {
    const readinessStates: MapSelectionWorkout["coverageReadiness"][] = [
      { mapDataStatus: "ready", processingStatus: "current", resultStatus: "current" },
      { mapDataStatus: "ready", processingStatus: "stale", resultStatus: "stale" },
      { mapDataStatus: "pending", processingStatus: "unprocessed", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "queued", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "running", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "failed", resultStatus: "none" },
      { mapDataStatus: "unavailable", processingStatus: "unprocessed", resultStatus: "none" },
      { mapDataStatus: "ready", processingStatus: "unprocessed", resultStatus: "none" },
    ];
    const previewSelection: MapSelection = {
      ...selection,
      workouts: Array.from({ length: readinessStates.length }, (_, index) => ({
        ...selection.workouts[0],
        id: String.fromCharCode(68 + index).repeat(32),
        startedAt: `2026-08-${String(8 - index).padStart(2, "0")}T12:00:00Z`,
        localStartDate: `2026-08-${String(8 - index).padStart(2, "0")}`,
        coverageReadiness: readinessStates[index],
      })),
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json(previewSelection));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(await screen.findAllByRole("checkbox", { name: /Show Running/ })).toHaveLength(
      readinessStates.length,
    );
    const coverageMode = screen.getByRole("button", { name: "Coverage" });
    await waitFor(() => expect(coverageMode).toBeEnabled());
    fireEvent.click(coverageMode);

    expect(document.querySelectorAll(".coverage-route-status--current")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--stale")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--pending")).toHaveLength(4);
    expect(document.querySelectorAll(".coverage-route-status--failed")).toHaveLength(1);
    expect(document.querySelectorAll(".coverage-route-status--unavailable")).toHaveLength(1);
    expect(document.querySelector(".coverage-route-status--current")).toHaveTextContent("✓");
    expect(document.querySelector(".coverage-route-status--failed")).toHaveTextContent("!");
    const unavailableBadge = document.querySelector(".coverage-route-status--unavailable")!;
    const unavailableIcon = unavailableBadge.querySelector("svg")!;
    expect(unavailableBadge).not.toHaveTextContent("×");
    expect(unavailableBadge.querySelector('span[aria-hidden="true"]')).not.toBeInTheDocument();
    expect(unavailableIcon).toHaveAttribute("viewBox", "0 0 16 16");
    expect(unavailableIcon).toHaveAttribute("aria-hidden", "true");
    expect(unavailableIcon).toHaveAttribute("focusable", "false");
    expect(unavailableIcon.querySelector("circle")).toHaveAttribute("cx", "8");
    expect(unavailableIcon.querySelector("circle")).toHaveAttribute("cy", "8");
    expect(unavailableIcon.querySelector("circle")).toHaveAttribute("r", "8");
    expect(unavailableIcon.querySelector("path")).toHaveAttribute("d", "M5 5L11 11M11 5L5 11");
    expect(unavailableIcon.querySelector("path")).toHaveAttribute("stroke-width", "1.5");
    expect(unavailableIcon.querySelector("path")).toHaveAttribute("stroke-linecap", "round");
    expect(document.querySelector(".coverage-route-status--unprocessed")).not.toBeInTheDocument();
    const unprocessed = screen.getByRole("button", { name: /Coverage pending.*8\/01\/2026/ });
    expect(unprocessed).toBeDisabled();
    expect(unprocessed.querySelector(".coverage-route-status--pending")).toBeInTheDocument();
    for (const indicator of document.querySelectorAll(".coverage-route-status--pending"))
      expect(indicator.querySelector('[aria-hidden="true"]')).toBeEmptyDOMElement();
    expect(screen.getByText("Waiting for map data")).toHaveAttribute("role", "tooltip");
    expect(screen.getAllByText("Waiting for coverage update")).toHaveLength(3);
    for (const tooltip of screen.getAllByText("Waiting for coverage update"))
      expect(tooltip).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Coverage update failed")).toHaveAttribute("role", "tooltip");
    expect(screen.getByText("Map data not available")).toHaveAttribute("role", "tooltip");
    for (const tooltip of document.querySelectorAll(".tooltip-content--portal"))
      expect(tooltip.parentElement).toBe(document.body);

    fireEvent.click(screen.getByRole("button", { name: /Coverage current.*Running/ }));
    expect(new URL(window.location.href).searchParams.get("workoutId")).toBe("D".repeat(32));
  });

  test("maps all 54 Coverage readiness combinations to circular status kinds", async () => {
    const readinessStates = (["ready", "pending", "unavailable"] as const).flatMap(
      (mapDataStatus) =>
        (["unprocessed", "queued", "running", "current", "failed", "stale"] as const).flatMap(
          (processingStatus) =>
            (["none", "current", "stale"] as const).map((resultStatus) => ({
              mapDataStatus,
              processingStatus,
              resultStatus,
            })),
        ),
    );
    const workouts = readinessStates.map((coverageReadiness, index) => ({
      ...selection.workouts[0],
      id: (256 + index).toString(16).padStart(32, "0").toUpperCase(),
      coverageReadiness,
    }));
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return Promise.resolve(json({ ...selection, workouts }));
      if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(await screen.findAllByRole("checkbox", { name: /Show Running/ })).toHaveLength(54);
    const coverageMode = screen.getByRole("button", { name: "Coverage" });
    await waitFor(() => expect(coverageMode).toBeEnabled());
    fireEvent.click(coverageMode);

    for (const workout of workouts) {
      const { resultStatus, mapDataStatus, processingStatus } = workout.coverageReadiness;
      const expectedKind =
        resultStatus !== "none"
          ? resultStatus
          : mapDataStatus === "unavailable"
            ? "unavailable"
            : mapDataStatus === "ready" && processingStatus === "failed"
              ? "failed"
              : "pending";
      const row = document.querySelector(`[data-workout-id="${workout.id}"]`)!;
      expect(row.querySelectorAll(".coverage-route-status")).toHaveLength(1);
      expect(row.querySelector(".coverage-route-status")).toHaveClass(
        `coverage-route-status--${expectedKind}`,
      );
      const trigger = row.querySelector(".tooltip-trigger");
      expect(trigger !== null).toBe(resultStatus === "none");
      if (!trigger) continue;
      const tooltip = document.getElementById(trigger.getAttribute("aria-describedby")!)!;
      expect(tooltip.parentElement).toBe(document.body);
      if (tooltip.textContent === "Waiting for coverage update")
        expect(expectedKind).toBe("pending");
    }
  });

  test("shows delayed exact road coverage stats after one transient failure without exposing a raw focused route", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const focusedSelection = { ...selection, focusedWorkoutId: selection.workouts[0].id };
    const entityID = "E".repeat(32);
    let statsAttempts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(focusedSelection));
      if (
        path ===
        `/api/map-selections/${selection.id}/coverage/path/${entityID}/stats?generation=${selection.dataGeneration}`
      ) {
        statsAttempts++;
        if (statsAttempts === 1)
          return Promise.resolve(json({ title: "Service Unavailable" }, 503));
        return Promise.resolve(
          json({
            entityId: entityID,
            entityKind: "path",
            name: null,
            localityName: "Yosemite National Park",
            regionId: "geofabrik:norcal",
            regionName: "Northern California",
            broadClass: "cycleway",
            rangeWorkoutCount: 3,
            rangeFirstDate: "2025-10-25",
            rangeFirstWorkoutId: selection.workouts[0].id,
            rangeLatestDate: "2026-08-05",
            rangeLatestWorkoutId: selection.workouts[0].id,
            allTimeWorkoutCount: 8,
            allTimeFirstDate: "2024-04-02",
            allTimeFirstWorkoutId: selection.workouts[1].id,
            allTimeLatestDate: "2026-08-05",
            allTimeLatestWorkoutId: selection.workouts[0].id,
            bounds: selection.bounds,
          }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    const diagnosticConfig = { ...config, features: { coverageMatcherDiagnostics: true } };
    const view = render(
      <MapPage
        config={diagnosticConfig}
        preferences={productionPreferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
        persistedFocusedWorkoutId={selection.workouts[0].id}
        onFocusedWorkoutChange={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    const map = mapInstances.at(-1)!;
    map.queryRenderedFeatures.mockImplementation(
      (_point: unknown, options: { layers?: string[] }) =>
        options.layers?.some((layer) => layer.includes("coverage"))
          ? [
              {
                properties: {
                  entityId: "D".repeat(32),
                  entityKind: "park",
                  sourceEntityKind: "park",
                  sourceEntityId: "B".repeat(32),
                  name: "Yosemite National Park",
                },
              },
              {
                properties: {
                  entityId: entityID,
                  entityKind: "path",
                  sourceEntityKind: "path",
                  sourceEntityId: "A".repeat(32),
                  name: null,
                  localityName: "Yosemite National Park",
                },
              },
            ]
          : [],
    );
    vi.useFakeTimers();
    act(() =>
      map.handlers.get("mousemove")?.({
        point: { x: 50, y: 50 },
        lngLat: { lng: -105.2, lat: 40 },
      }),
    );
    expect(map.queryRenderedFeatures).toHaveBeenCalledWith(
      [
        [47, 47],
        [53, 53],
      ],
      { layers: ["private-workout-coverage-parks", "private-workout-coverage"] },
    );
    act(() => vi.advanceTimersByTime(749));
    expect(popupInstances).toHaveLength(0);
    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(popupInstances).toHaveLength(0);
    await act(async () => {
      vi.advanceTimersByTime(250);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(popupInstances).toHaveLength(1);
    expect(statsAttempts).toBe(2);
    expect(popupInstances[0].content).toHaveTextContent(
      "Bike pathYosemite National ParkWorkouts3Earliest visit10/25/2025First visit ever4/2/2024Most recent visit8/5/2026Last visit ever8/5/2026",
    );
    expect(map.setLayoutProperty).toHaveBeenCalledWith(
      "private-workout-routes",
      "visibility",
      "none",
    );
    expect(map.getLayer("coverage-diagnostic-raw-route")).toBeUndefined();
    vi.useRealTimers();
    view.rerender(
      <MapPage
        config={diagnosticConfig}
        preferences={{ ...productionPreferences, coverageDiagnosticsEnabled: true }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
        persistedFocusedWorkoutId={selection.workouts[0].id}
        onFocusedWorkoutChange={vi.fn()}
      />,
    );
    await waitFor(() => expect(popupInstances[0].remove).toHaveBeenCalled());
  });

  test.each([503, 422, "incomplete"] as const)(
    "does not cache failed bulk coverage (%s), then caches a complete retry",
    async (failure) => {
      const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
      let coverageRequests = 0;
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        const path = String(input);
        const method = init?.method ?? "GET";
        if (path === "/api/map-selections" && method === "POST")
          return Promise.resolve(json(selection));
        if (path.includes("/coverage/paths?")) {
          coverageRequests++;
          expect(new URL(path, "https://test").searchParams.get("bulk")).toBe("true");
          return Promise.resolve(
            coverageRequests > 1
              ? json({
                  pagination: { page: 1, pageSize: 50000, totalItems: 0, totalPages: 0 },
                  items: [],
                })
              : failure === "incomplete"
                ? json({
                    pagination: { page: 1, pageSize: 100, totalItems: 101, totalPages: 2 },
                    items: [],
                  })
                : json({ title: "Failed" }, failure),
          );
        }
        if (method === "DELETE") return Promise.resolve(json(undefined, 204));
        throw new Error(`Unexpected request ${method} ${path}`);
      });
      const cacheChanged = vi.fn();
      function CachedCoverageMap() {
        const [cache, setCache] = useState<RoadCoverageCache>();
        return (
          <MapPage
            config={config}
            preferences={productionPreferences}
            csrfToken="csrf-map"
            dateRange="last30Days"
            onDateRangeSelected={vi.fn()}
            persistedFocusedWorkoutId={selection.workouts[0].id}
            onFocusedWorkoutChange={vi.fn()}
            roadCoverageCache={cache}
            onRoadCoverageCacheChange={(next) => {
              cacheChanged(next);
              setCache(next);
            }}
          />
        );
      }
      render(<CachedCoverageMap />);
      await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
      await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
      const dialog = await screen.findByRole("dialog", { name: "Road Coverage" });
      expect(dialog).toHaveTextContent(
        failure === 422
          ? "Road coverage exceeds the bulk limit of 50,000 entities or 64 MiB. Choose a smaller date range."
          : "Road coverage could not be loaded.",
      );
      expect(cacheChanged).not.toHaveBeenCalled();
      expect(within(dialog).queryByRole("table")).not.toBeInTheDocument();
      expect(within(dialog).queryByRole("searchbox")).not.toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Retry" }));
      await waitFor(() => expect(coverageRequests).toBe(2));
      await waitFor(() => expect(cacheChanged).toHaveBeenCalledTimes(1));
      expect(cacheChanged).toHaveBeenCalledWith({
        key: JSON.stringify([
          selection.range.startDate,
          selection.range.endDate,
          selection.dataGeneration,
          selection.coverageDisplayPolicy,
          selection.coverageDisplayRevision,
        ]),
        loadedAt: expect.any(Number),
        entities: [],
      });
      await userEvent.click(within(dialog).getByRole("button", { name: "Close Road Coverage" }));
      expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
      expect(await screen.findByRole("table")).toBeVisible();
      expect(coverageRequests).toBe(2);
    },
  );

  test("refreshes a stale map selection and resumes Road Coverage loading after a 404", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    let selectionRequests = 0,
      coverageRequests = 0;
    const coverageEntity = {
      entityId: "E".repeat(32),
      entityKind: "park",
      name: "Example Park",
      localityName: "Test City",
      regionId: "geofabrik:norcal",
      regionName: "Northern California",
      broadClass: "park",
      rangeWorkoutCount: 3,
      rangeFirstDate: "2026-07-12",
      rangeFirstWorkoutId: selection.workouts[1].id,
      rangeLatestDate: "2026-08-05",
      rangeLatestWorkoutId: selection.workouts[0].id,
      allTimeWorkoutCount: 7,
      allTimeFirstDate: "2026-01-01",
      allTimeFirstWorkoutId: selection.workouts[1].id,
      allTimeLatestDate: "2026-08-05",
      allTimeLatestWorkoutId: selection.workouts[0].id,
      bounds: selection.bounds,
    } as const;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input),
        method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        selectionRequests++;
        return Promise.resolve(
          json(
            selectionRequests === 1
              ? selection
              : { ...selection, id: "8".repeat(32), dataGeneration: selection.dataGeneration + 1 },
          ),
        );
      }
      if (path.includes("/coverage/paths?")) {
        coverageRequests++;
        return Promise.resolve(
          coverageRequests === 1
            ? json({ title: "Not Found" }, 404)
            : json({
                pagination: { page: 1, pageSize: 50000, totalItems: 1, totalPages: 1 },
                items: [coverageEntity],
              }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={productionPreferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(selectionRequests).toBeGreaterThanOrEqual(2);
    expect(coverageRequests).toBe(2);
    expect(screen.queryByText("Road coverage could not be loaded.")).not.toBeInTheDocument();
  });

  test("refreshes Road Coverage when an updating entity disappears", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    const staleEntity = {
      entityId: "E".repeat(32),
      entityKind: "path",
      name: null,
      localityName: "Mountain View",
      regionId: "geofabrik:norcal",
      regionName: "Northern California",
      broadClass: "footway",
      rangeWorkoutCount: 25,
      rangeFirstDate: "2025-04-03",
      rangeFirstWorkoutId: selection.workouts[1].id,
      rangeLatestDate: "2026-08-05",
      rangeLatestWorkoutId: selection.workouts[0].id,
      allTimeWorkoutCount: 27,
      allTimeFirstDate: "2025-04-03",
      allTimeFirstWorkoutId: selection.workouts[1].id,
      allTimeLatestDate: "2026-08-05",
      allTimeLatestWorkoutId: selection.workouts[0].id,
      bounds: selection.bounds,
    } as const;
    const replacement = {
      ...staleEntity,
      entityId: "D".repeat(32),
      name: "Replacement Path",
    } as const;
    let listRequests = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input),
        method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.includes("/coverage/paths?")) {
        listRequests++;
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 50000, totalItems: 1, totalPages: 1 },
            items: listRequests === 1 ? [staleEntity] : [replacement],
          }),
        );
      }
      if (path.includes(`/coverage/path/${staleEntity.entityId}`))
        return Promise.resolve(json({ title: "Not Found" }, 404));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={productionPreferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }));
    await userEvent.click(await screen.findByRole("button", { name: "Pedestrian path" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("changed or expired");
    expect(listRequests).toBe(1);
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await userEvent.click(await screen.findByRole("button", { name: "Coverage by road..." }));
    expect(await screen.findByRole("button", { name: "Replacement Path" })).toBeVisible();
    expect(listRequests).toBe(2);
    expect(
      screen.queryByText("That road coverage could not be shown on the map."),
    ).not.toBeInTheDocument();
  });

  test("restores the cached Road Coverage page and resets it for search", async () => {
    const productionPreferences = {
      ...preferences,
      coverageDiagnosticsEnabled: false,
      pageSize: 1,
    };
    const entity = (id: string, name: string, count: number) =>
      ({
        entityId: id.repeat(32),
        entityKind: "path",
        name,
        localityName: "Test City",
        regionId: "geofabrik:norcal",
        regionName: "Northern California",
        broadClass: "road",
        rangeWorkoutCount: count,
        rangeFirstDate: "2026-07-12",
        rangeFirstWorkoutId: selection.workouts[1].id,
        rangeLatestDate: "2026-08-05",
        rangeLatestWorkoutId: selection.workouts[0].id,
        allTimeWorkoutCount: count,
        allTimeFirstDate: "2026-07-12",
        allTimeFirstWorkoutId: selection.workouts[1].id,
        allTimeLatestDate: "2026-08-05",
        allTimeLatestWorkoutId: selection.workouts[0].id,
        bounds: selection.bounds,
      }) as const;
    const high = entity("E", "High Road", 2),
      low = entity("F", "Low Road", 1);
    let coverageRequests = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input),
        method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        return Promise.resolve(
          json(
            body.dateRangeEnum === "last7Days"
              ? {
                  ...selection,
                  id: "9".repeat(32),
                  range: { startDate: "2026-07-30", endDate: "2026-08-05" },
                }
              : selection,
          ),
        );
      }
      if (path.includes("/coverage/paths?")) {
        coverageRequests++;
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 50000, totalItems: 2, totalPages: 1 },
            items: [high, low],
          }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function CachedCoverageMap({ range }: { range: DateRangePreference }) {
      const [cache, setCache] = useState<RoadCoverageCache>();
      return (
        <MapPage
          config={config}
          preferences={productionPreferences}
          csrfToken="csrf-map"
          dateRange={range}
          onDateRangeSelected={vi.fn()}
          roadCoverageCache={cache}
          onRoadCoverageCacheChange={setCache}
        />
      );
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
    expect(coverageRequests).toBe(1);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } });
    expect(screen.getByRole("searchbox")).toHaveValue("");
    await waitFor(() => expect(screen.getByText("Page 1 of 2")).toBeVisible(), { timeout: 1000 });
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Page 2 of 2")).toBeVisible();
    view.rerender(<CachedCoverageMap range="last7Days" />);
    await waitFor(() => expect(screen.getByText("Page 1 of 2")).toBeVisible());
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(
      screen.getByRole("checkbox", { name: "Show unnamed roads and paths" }),
    ).not.toBeChecked();
    await waitFor(() => expect(coverageRequests).toBe(2));
  });

  test("uses a two-row loading body before locking Road Coverage to the loaded result height", async () => {
    const productionPreferences = { ...preferences, coverageDiagnosticsEnabled: false };
    let resolveCoverage!: (response: Response) => void;
    const deferredCoverage = new Promise<Response>((resolve) => {
      resolveCoverage = resolve;
    });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.includes("/coverage/paths?")) return deferredCoverage;
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={productionPreferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
        persistedFocusedWorkoutId={selection.workouts[0].id}
        onFocusedWorkoutChange={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Coverage by road..." }));
    const dialog = await screen.findByRole("dialog", { name: "Road Coverage" });
    const frame = dialog.querySelector<HTMLElement>(".road-coverage-table-wrap")!;
    expect(frame.style.getPropertyValue("--road-coverage-row-slots")).toBe("2");
    expect(
      await within(dialog).findByText("Loading coverage...", {}, { timeout: 1500 }),
    ).toHaveAttribute("role", "status");
    resolveCoverage(
      json({
        pagination: { page: 1, pageSize: 50000, totalItems: 1, totalPages: 1 },
        items: [
          {
            entityId: "E".repeat(32),
            entityKind: "park",
            name: "Example Park",
            localityName: "Test City",
            regionId: "geofabrik:norcal",
            regionName: "Northern California",
            broadClass: "park",
            rangeWorkoutCount: 3,
            rangeFirstDate: "2026-07-12",
            rangeFirstWorkoutId: selection.workouts[1].id,
            rangeLatestDate: "2026-08-05",
            rangeLatestWorkoutId: selection.workouts[0].id,
            allTimeWorkoutCount: 7,
            allTimeFirstDate: "2026-01-01",
            allTimeFirstWorkoutId: selection.workouts[1].id,
            allTimeLatestDate: "2026-08-05",
            allTimeLatestWorkoutId: selection.workouts[0].id,
            bounds: selection.bounds,
          },
        ],
      }),
    );
    expect(await within(dialog).findByRole("button", { name: "Example Park" })).toBeVisible();
    expect(frame.style.getPropertyValue("--road-coverage-row-slots")).toBe("1");
    expect(within(dialog).queryByRole("button", { name: "Fit" })).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Close Road Coverage" }));
    await userEvent.click(screen.getByRole("button", { name: "Fit" }));
    expect(screen.queryByRole("dialog", { name: "Road Coverage" })).not.toBeInTheDocument();
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.fitBounds).toHaveBeenCalledWith(
        [
          [-105.3, 39.9],
          [-105.2, 40.1],
        ],
        { padding: 48, duration: 350 },
      ),
    );
    expect(map.getCanvas()).toHaveFocus();
  });

  test("keeps a family override visit-local and exposes the accessible mobile sheet", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
    expect(mapInstances.at(-1)?.setStyle).toHaveBeenLastCalledWith(
      "https://tiles.example.test/road-dark.json",
      expect.objectContaining({ transformStyle: expect.any(Function) }),
    );
    const transform = mapInstances.at(-1)?.setStyle.mock.calls.at(-1)?.[1].transformStyle;
    const transformed = transform(
      {
        version: 8,
        sources: { "private-workout-routes": { type: "vector", tiles: ["private"] } },
        layers: [{ id: "private-workout-routes", type: "line" }],
      },
      { version: 8, sources: { base: { type: "vector" } }, layers: [{ id: "base", type: "line" }] },
    );
    expect(transformed.sources).toHaveProperty("private-workout-routes");
    expect(transformed.layers.map((layer: { id: string }) => layer.id)).toEqual([
      "base",
      "private-workout-routes",
    ]);
    const trigger = screen.getByRole("button", { name: "Routes and controls" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("dialog", { name: "Routes and controls" })).toBeVisible();
    expect(screen.getByRole("dialog", { name: "Routes and controls" })).toContainElement(
      screen.getByRole("button", { name: "Select base map" }),
    );
  });

  test.each([
    ["2023-04-01", "2022-03-01", "2023-04-01"],
    ["2022-02-15", "2022-03-01", "2022-03-01"],
    ["2022-03-01", "2022-03-01", "2022-03-01"],
    ["2023-04-01", "", "2023-04-01"],
    ["", "2022-03-01", "2022-03-01"],
    ["", "", ""],
  ])("completes the Map range only on start blur: %s / %s -> %s", async (start, end, expected) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="2022-02-01/2022-03-01"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: /^Custom/ }));
    const startDate = await screen.findByLabelText("Start date");
    const endDate = screen.getByLabelText("End date");
    fireEvent.change(endDate, { target: { value: end } });
    await user.click(startDate);
    fireEvent.change(startDate, { target: { value: start } });
    expect(startDate).toHaveFocus();
    expect(endDate).toHaveValue(end);
    await user.tab();
    expect(endDate).toHaveFocus();
    expect(endDate).toHaveValue(expected);
  });

  test("formats an explicit date range consistently in the trigger", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="2026-03-06/2026-03-20"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(await screen.findByRole("button", { name: "Select date range" })).toHaveTextContent(
      "Mar 6, 2026 to Mar 20, 2026",
    );
  });

  test("dismisses a date range save warning without changing the applied map range", async () => {
    const posted: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        return Promise.resolve(
          json({
            ...selection,
            workouts:
              body.dateRangeEnum === "last7Days" ? [selection.workouts[0]] : selection.workouts,
          }),
        );
      }
      if (path === "/api/me/preferences" && method === "PATCH")
        return Promise.reject(new Error("preference save failed"));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function Harness() {
      const [range, setRange] = useState<DateRangePreference>("last30Days");
      return (
        <MapPage
          config={config}
          preferences={preferences}
          csrfToken="csrf-map"
          dateRange={range}
          onDateRangeSelected={setRange}
        />
      );
    }
    render(<Harness />);
    await screen.findByRole("checkbox", { name: /Show Hiking/ });
    await userEvent.click(screen.getByRole("button", { name: "Select date range" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    const warning = await screen.findByText(
      "Your default date range was not saved. This map will continue using your selection.",
    );
    await waitFor(() =>
      expect(screen.queryByRole("checkbox", { name: /Show Hiking/ })).not.toBeInTheDocument(),
    );
    const map = mapInstances.at(-1)!;
    const source = map.getSource("private-workout-routes");
    await userEvent.click(screen.getByRole("button", { name: "Dismiss date range warning" }));
    expect(warning).not.toBeInTheDocument();
    expect(document.querySelector(".map-banner")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select date range" })).toHaveTextContent(
      "Last 7 days",
    );
    expect(screen.getByRole("checkbox", { name: /Show Running/ })).toBeChecked();
    expect(screen.queryByRole("checkbox", { name: /Show Hiking/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fit routes" })).toBeEnabled();
    expect(map.getSource("private-workout-routes")).toBe(source);
    expect(posted).toEqual([
      { dateRangeEnum: "last30Days", tz: "America/Denver" },
      { dateRangeEnum: "last7Days", tz: "America/Denver" },
    ]);
  });

  test("replaces the private selection when a workout route is filtered", async () => {
    const posted: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        const filtered = Array.isArray(body.workoutIds)
          ? selection.workouts.filter((workout) => body.workoutIds.includes(workout.id))
          : selection.workouts;
        const id = filtered.length === 1 ? "D".repeat(32) : selection.id;
        return Promise.resolve(
          json({
            ...selection,
            id,
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
            workouts: filtered,
          }),
        );
      }
      if (path === `/api/workouts/${selection.workouts[1].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox", { name: /Running/ }));
    await waitFor(() =>
      expect(posted).toContainEqual({
        dateRangeEnum: "last30Days",
        tz: "America/Denver",
        workoutIds: [selection.workouts[1].id],
      }),
    );
    expect(mapInstances).toHaveLength(1);
    await waitFor(() =>
      expect(mapInstances[0].removeSource).toHaveBeenCalledWith("private-workout-routes"),
    );
    expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-routes", {
      type: "vector",
      tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "D".repeat(32)))],
    });
    await waitFor(() =>
      expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-route-endpoints", {
        type: "geojson",
        data: rawRouteEndpoints,
      }),
    );
    expect(mapInstances[0].remove).not.toHaveBeenCalled();
  });

  test("fits and highlights a requested route without fitting the merged checked selection", async () => {
    const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
    const requested = selection.workouts[0];
    const previouslySelected = selection.workouts[1];
    history.replaceState({}, "", `/map?workoutId=${requested.id}`);
    const posted: Array<{ workoutIds?: string[] }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        const workouts =
          body.workoutIds === undefined
            ? selection.workouts
            : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function ControlledMap() {
      const [workoutIds, setWorkoutIds] = useState<string[] | undefined>([previouslySelected.id]);
      const [available, setAvailable] = useState(selection.workouts);
      return (
        <MapPage
          config={config}
          preferences={preferences}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          persistedWorkoutIds={workoutIds}
          onWorkoutSelectionChange={setWorkoutIds}
          persistedAvailableWorkouts={available}
          onAvailableWorkoutsChange={setAvailable}
        />
      );
    }
    render(<ControlledMap />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(posted).toContainEqual(
        expect.objectContaining({ workoutIds: [requested.id, previouslySelected.id].sort() }),
      ),
    );
    await waitFor(() =>
      expect(map.fitBounds).toHaveBeenLastCalledWith(
        [
          [requested.bounds.minimumLongitude, requested.bounds.minimumLatitude],
          [requested.bounds.maximumLongitude, requested.bounds.maximumLatitude],
        ],
        { padding: 48, duration: 350 },
      ),
    );
    expect(map.fitBounds).toHaveBeenCalledTimes(1);
    const highlightLayer = map.addLayer.mock.calls.find(
      (call: any[]) => call[0].id === "private-workout-route-hover",
    )?.[0];
    expect(highlightLayer.filter).toEqual(["==", ["get", "workoutId"], requested.id]);
    expect(highlightLayer.paint["line-color"]).toBe("#c026ff");
    expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
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
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        const workouts =
          body.workoutIds === undefined
            ? selection.workouts
            : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, bounds: requested.bounds, workouts }));
      }
      if (path === `/api/workouts/${requested.id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    function ControlledRemount() {
      const [workoutIds, setWorkoutIds] = useState<string[] | undefined>([requested.id]);
      const [available, setAvailable] = useState<typeof selection.workouts>([]);
      const [focused, setFocused] = useState<string | undefined>(requested.id);
      return (
        <MapPage
          config={config}
          preferences={preferences}
          csrfToken="csrf-map"
          dateRange="last30Days"
          onDateRangeSelected={vi.fn()}
          persistedWorkoutIds={workoutIds}
          onWorkoutSelectionChange={setWorkoutIds}
          persistedAvailableWorkouts={available}
          onAvailableWorkoutsChange={setAvailable}
          persistedFocusedWorkoutId={focused}
          onFocusedWorkoutChange={setFocused}
        />
      );
    }
    render(<ControlledRemount />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).not.toBeChecked();
    expect(posted[0].workoutIds).toBeUndefined();
    expect(posted.at(-1)?.workoutIds).toEqual([requested.id]);
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.fitBounds).toHaveBeenCalledWith(
        [
          [requested.bounds.minimumLongitude, requested.bounds.minimumLatitude],
          [requested.bounds.maximumLongitude, requested.bounds.maximumLatitude],
        ],
        { padding: 48, duration: 350 },
      ),
    );
    expect(map.fitBounds).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", inline: "nearest" });
  });

  test("delays list highlighting and clears it immediately when the route is hidden", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        const workouts =
          body.workoutIds === undefined
            ? selection.workouts
            : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(json({ ...selection, workouts }));
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
    const mixedConfig = {
      ...config,
      baseMaps: {
        ...baseMaps,
        workoutTypeMappings: baseMaps.workoutTypeMappings.map((mapping) =>
          mapping.normalizedTypeKey === "hiking" ? { ...mapping, familyId: "road" } : mapping,
        ),
      },
    };
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={mixedConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const checkbox = await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    await waitFor(() =>
      expect(map.fitBounds).toHaveBeenCalledWith(
        [
          [-105.3, 39.8],
          [-105.1, 40.1],
        ],
        { padding: 48, duration: 350 },
      ),
    );
    const fitsBeforeToggle = map.fitBounds.mock.calls.length;
    const stylesBeforeToggle = map.setStyle.mock.calls.length;
    const user = userEvent.setup();
    await user.click(checkbox);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeToggle);
    await user.click(checkbox);
    expect(map.fitBounds).toHaveBeenCalledTimes(fitsBeforeToggle);
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeToggle);
    expect(
      screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li"),
    ).not.toHaveClass("is-focused");
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    expect(map.fitBounds).toHaveBeenLastCalledWith(
      [
        [-105.3, 39.9],
        [-105.2, 40.1],
      ],
      { padding: 48, duration: 350 },
    );
    expect(map.setStyle).toHaveBeenLastCalledWith(
      "https://tiles.example.test/outdoor-dark.json",
      expect.objectContaining({ transformStyle: expect.any(Function) }),
    );
    await waitFor(() => expect(map.getCanvas()).toHaveFocus());
    expect(screen.getByRole("application", { name: "Workout route map" })).toHaveAttribute(
      "aria-keyshortcuts",
      "Space",
    );
    expect(map.getLayer("private-workout-routes").paint["line-opacity-transition"]).toEqual({
      duration: 400,
      delay: 0,
    });
    expect(
      map.getLayer("private-workout-route-markers").paint["circle-opacity-transition"],
    ).toEqual({ duration: 400, delay: 0 });
    map.setPaintProperty.mockClear();
    map.setLayoutProperty.mockClear();
    map.removeLayer.mockClear();
    map.removeSource.mockClear();
    fireEvent.keyDown(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "private-workout-routes",
        "line-opacity",
        0,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-markers",
      "circle-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-route-hover",
      "line-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-route-marker-hover",
      "circle-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-route-start",
      "circle-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "private-workout-route-finish",
      "text-opacity",
      0,
    );
    expect(map.setPaintProperty).not.toHaveBeenCalledWith(
      "coverage-diagnostic-direction",
      "icon-opacity",
      0,
    );
    fireEvent.keyUp(window, { key: " ", code: "Space" });
    await waitFor(() =>
      expect(map.setPaintProperty).toHaveBeenCalledWith(
        "private-workout-routes",
        "line-opacity",
        0.82,
      ),
    );
    expect(map.setPaintProperty).toHaveBeenCalledWith(
      "private-workout-route-markers",
      "circle-opacity",
      0.9,
    );
    for (const layer of [
      "private-workout-routes",
      "private-workout-route-markers",
      "private-workout-route-hover",
      "private-workout-route-marker-hover",
    ]) {
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
    expect(
      screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li"),
    ).not.toHaveClass("is-focused");
  });

  test("replaces a stale empty source when a route is re-enabled", async () => {
    const posted: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        posted.push(body);
        const workouts =
          body.workoutIds === undefined
            ? selection.workouts
            : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        const id =
          workouts.length === 0
            ? "E".repeat(32)
            : workouts.length === 1
              ? "F".repeat(32)
              : selection.id;
        return Promise.resolve(
          json({
            ...selection,
            id,
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
            bounds: workouts.length ? workouts[0].bounds : null,
            workouts,
          }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup();
    const map = mapInstances.at(-1)!;
    await user.click(screen.getByRole("checkbox", { name: "Select all workout routes" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Fit routes" })).toBeDisabled());
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    await waitFor(() =>
      expect(posted).toContainEqual({
        dateRangeEnum: "last30Days",
        tz: "America/Denver",
        workoutIds: [selection.workouts[0].id],
        focusedWorkoutId: selection.workouts[0].id,
      }),
    );
    await waitFor(() => expect(map.removeSource).toHaveBeenCalledWith("private-workout-routes"));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
      type: "vector",
      tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "F".repeat(32)))],
    });
    expect(mapInstances).toHaveLength(1);
    expect(map.remove).not.toHaveBeenCalled();
  });

  test("uses the route-aligned bulk checkbox to toggle all routes", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const method = init?.method ?? "GET";
      if (String(input) === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        const workouts =
          body.workoutIds === undefined
            ? selection.workouts
            : selection.workouts.filter((workout) => body.workoutIds.includes(workout.id));
        return Promise.resolve(
          json({ ...selection, bounds: workouts.length ? selection.bounds : null, workouts }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${String(input)}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const user = userEvent.setup();
    const bulk = await screen.findByRole("checkbox", { name: "Select all workout routes" });
    const map = mapInstances.at(-1)!;
    const stylesBeforeVisibilityChanges = map.setStyle.mock.calls.length;
    expect(bulk).toBeChecked();
    expect(bulk.closest(".map-route-toolbar")).toBeInTheDocument();
    expect(bulk.closest(".map-route-toggle")).toBeInTheDocument();
    expect(screen.queryByText("All routes")).not.toBeInTheDocument();

    await user.click(bulk);
    await waitFor(() =>
      expect(
        screen
          .getAllByRole("checkbox", { name: /Show / })
          .every((checkbox) => !(checkbox as HTMLInputElement).checked),
      ).toBe(true),
    );
    expect(bulk).not.toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);

    await user.click(bulk);
    await waitFor(() =>
      expect(
        screen
          .getAllByRole("checkbox", { name: /Show / })
          .every((checkbox) => (checkbox as HTMLInputElement).checked),
      ).toBe(true),
    );
    expect(bulk).toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);

    await user.click(screen.getByRole("checkbox", { name: /Show Running/ }));
    expect(bulk).not.toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);
    await user.click(bulk);
    expect(
      screen
        .getAllByRole("checkbox", { name: /Show / })
        .every((checkbox) => (checkbox as HTMLInputElement).checked),
    ).toBe(true);
    expect(bulk).toBeChecked();
    expect(map.setStyle).toHaveBeenCalledTimes(stylesBeforeVisibilityChanges);
  });

  test("delays the two-line route popup and synchronizes the workout highlight", async () => {
    const requests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      requests.push(`${method} ${path}`);
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.endsWith("/route/points") && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    map.queryRenderedFeatures.mockReturnValue([
      { properties: { workoutId: selection.workouts[0].id, sortOrder: 1 } },
    ]);
    vi.useFakeTimers();
    act(() =>
      map.handlers.get("mousemove")?.({
        point: { x: 50, y: 50 },
        lngLat: { lng: -105.2, lat: 40 },
      }),
    );
    const row = screen.getByRole("button", { name: /Running.*8\/08\/2026/ }).closest("li");
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(249));
    act(() =>
      map.handlers.get("mousemove")?.({
        point: { x: 51, y: 51 },
        lngLat: { lng: -105.2, lat: 40 },
      }),
    );
    expect(row).not.toHaveClass("is-hovered");
    act(() => vi.advanceTimersByTime(1));
    expect(row).toHaveClass("is-hovered");
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
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
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        selectionCount++;
        return Promise.resolve(
          json({
            ...selection,
            routeTileUrl: `/api/map-selections/${selection.id}/route-tiles/${selectionCount}/{z}/{x}/{y}.pbf`,
          }),
        );
      }
      if (path.endsWith("/route/points") && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
      const path = String(input);
      const method = init?.method ?? "GET";
      requests.push(`${method} ${path}`);
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.endsWith("/route/points") && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={sameFamilyConfig}
        preferences={{ ...preferences, coverageDiagnosticsEnabled: false }}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    const routeSource = map.getSource("private-workout-routes");
    const routeLayer = map.getLayer("private-workout-routes");
    map.fitBounds.mockClear();
    map.removeSource.mockClear();
    map.removeLayer.mockClear();
    map.setStyle.mockClear();
    for (const [name, workout] of [
      [/Running.*8\/08\/2026/, selection.workouts[0]],
      [/Hiking.*8\/01\/2026/, selection.workouts[1]],
    ] as const) {
      await userEvent.click(screen.getByRole("button", { name }));
      await waitFor(() => expect(map.getLayer("coverage-diagnostic-direction")).toBeTruthy());
      expect(
        map.getLayer("coverage-diagnostic-direction").paint["icon-opacity-transition"],
      ).toEqual({ duration: 0, delay: 0 });
      expect(map.fitBounds).toHaveBeenLastCalledWith(
        [
          [workout.bounds.minimumLongitude, workout.bounds.minimumLatitude],
          [workout.bounds.maximumLongitude, workout.bounds.maximumLatitude],
        ],
        { padding: 48, duration: 350 },
      );
      expect(map.setFilter).toHaveBeenCalledWith("private-workout-route-hover", [
        "==",
        ["get", "workoutId"],
        workout.id,
      ]);
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
      const path = String(input);
      const method = init?.method ?? "GET";
      requests.push(`${method} ${path}`);
      if (path === "/api/map-selections" && method === "POST")
        return Promise.resolve(json(selection));
      if (path.endsWith("/route/points") && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    const hikingCheckbox = await screen.findByRole("checkbox", { name: /Show Hiking/ });
    const map = mapInstances.at(-1)!;
    map.fitBounds.mockClear();
    scrollIntoView.mockClear();
    map.queryRenderedFeatures.mockReturnValue([
      { properties: { workoutId: selection.workouts[1].id, sortOrder: 2 } },
    ]);
    act(() => map.handlers.get("click")?.({ point: { x: 80, y: 60 } }));
    const row = screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")!;
    await waitFor(() => expect(row).toHaveClass("is-hovered", "is-focused"));
    expect(hikingCheckbox).toBeChecked();
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", inline: "nearest" });
    expect(map.fitBounds).toHaveBeenCalledWith(
      [
        [
          selection.workouts[1].bounds.minimumLongitude,
          selection.workouts[1].bounds.minimumLatitude,
        ],
        [
          selection.workouts[1].bounds.maximumLongitude,
          selection.workouts[1].bounds.maximumLatitude,
        ],
      ],
      { padding: 48, duration: 350 },
    );
    await waitFor(() =>
      expect(requests).toContain(`GET /api/workouts/${selection.workouts[1].id}/route/points`),
    );
    expect(map.getLayer("coverage-diagnostic-direction")).toBeTruthy();
    expect(map.getLayer("private-workout-route-start")).toBeTruthy();
    expect(map.getLayer("private-workout-route-finish")).toBeTruthy();
    expect(map.moveLayer.mock.calls.at(-1)).toEqual(["private-workout-route-start"]);
  });

  test("shows safe empty and error states without contacting a map provider", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(json({ ...selection, workouts: [], bounds: null }));
    const view = render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(await screen.findByText("No workout routes in this range.")).toBeInTheDocument();
    expect(mapInstances).toHaveLength(1);
    mapInstances[0].handlers.get("load")?.();
    expect(mapInstances[0].fitBounds).toHaveBeenCalledWith(
      [
        [-125, 24],
        [-66.5, 49.5],
      ],
      { padding: 48, duration: 0 },
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
    view.unmount();

    fetchMock.mockRejectedValueOnce(new Error("private provider detail"));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(
      await screen.findByText("Routes could not be prepared for this map."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss route preparation error" }));
    expect(
      screen.queryByText("Routes could not be prepared for this map."),
    ).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private provider detail");
  });

  test.each([false, true])(
    "dismisses route progress without cancelling preparation and shows the next request (previous pending: %s)",
    async (previousPending) => {
      const requests: Array<{
        signal: AbortSignal;
        resolve: (response: Response) => void;
      }> = [];
      vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
        if (String(input) === "/api/map-selections" && init?.method === "POST")
          return new Promise<Response>((resolve) => {
            requests.push({ signal: init.signal as AbortSignal, resolve });
          });
        if (init?.method === "DELETE") return Promise.resolve(json(undefined, 204));
        throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
      });
      const props = {
        config,
        preferences: { ...preferences, coverageDiagnosticsEnabled: false },
        csrfToken: "csrf-map",
        onDateRangeSelected: vi.fn(),
      };
      const view = render(<MapPage {...props} dateRange="last30Days" />);
      await userEvent.click(screen.getByRole("button", { name: "Dismiss route update message" }));
      expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument();
      expect(document.querySelector(".map-banner")).not.toBeInTheDocument();
      expect(requests).toHaveLength(1);
      expect(requests[0].signal.aborted).toBe(false);
      expect(screen.getByRole("button", { name: "Fit routes" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Coverage" })).toBeDisabled();
      if (!previousPending) {
        await act(async () => requests[0].resolve(json(selection)));
        expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeChecked();
        expect(screen.getByRole("button", { name: "Fit routes" })).toBeEnabled();
        expect(screen.getByRole("button", { name: "Coverage" })).toBeEnabled();
        expect(requests[0].signal.aborted).toBe(false);
        expect(mapInstances.at(-1)!.getSource("private-workout-routes")).toBeTruthy();
      }

      view.rerender(<MapPage {...props} dateRange="last7Days" />);
      expect(screen.getByText("Updating routes...")).toBeInTheDocument();
      expect(requests).toHaveLength(2);
      await userEvent.click(screen.getByRole("button", { name: "Dismiss route update message" }));
      expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument();
      expect(requests[1].signal.aborted).toBe(false);
      await act(async () => requests[1].resolve(json(selection)));
      expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeChecked();
      expect(screen.getByRole("button", { name: "Fit routes" })).toBeEnabled();
      expect(screen.getByRole("button", { name: "Coverage" })).toBeEnabled();
      expect(mapInstances.at(-1)!.getSource("private-workout-routes")).toBeTruthy();
      expect(requests[1].signal.aborted).toBe(false);
      expect(document.querySelector(".map-banner")).not.toBeInTheDocument();
    },
  );

  test("shows errors after dismissed progress and dismisses only the visible warning in priority order", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    let rejectSelection!: (error: Error) => void;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (String(input) === "/api/map-selections" && init?.method === "POST")
        return new Promise<Response>((_resolve, reject) => {
          rejectSelection = reject;
        });
      throw new Error(`Unexpected request ${init?.method ?? "GET"} ${input}`);
    });
    const props = { config, preferences, csrfToken: "csrf-map", onDateRangeSelected: vi.fn() };
    const view = render(<MapPage {...props} dateRange="last30Days" />);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss route update message" }));
    await act(async () => rejectSelection(new Error("private provider detail")));
    expect(await screen.findByText("Routes could not be prepared for this map.")).toBeVisible();
    act(() => mapInstances.at(-1)!.handlers.get("error")?.());
    expect(screen.getByText(/The public base map could not be loaded/)).toBeVisible();
    expect(
      screen.queryByText("Routes could not be prepared for this map."),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss base map warning" }));
    expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();
    expect(screen.getByText("Routes could not be prepared for this map.")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss route preparation error" }));
    expect(document.querySelector(".map-banner")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private provider detail");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    view.rerender(<MapPage {...props} dateRange="last7Days" />);
    expect(screen.getByText("Updating routes...")).toBeVisible();
    await act(async () => rejectSelection(new Error("another private detail")));
    expect(await screen.findByText("Routes could not be prepared for this map.")).toBeVisible();
    const requestsBeforeDismiss = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Dismiss route preparation error" }));
    expect(document.querySelector(".map-banner")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(requestsBeforeDismiss);
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
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    expect(screen.getByText("Updating routes...")).toBeInTheDocument();
    expect(
      await screen.findByRole("checkbox", { name: /Show Running/ }, { timeout: 4000 }),
    ).toBeInTheDocument();
    expect(attempts).toBe(3);
    expect(
      screen.queryByText("Routes could not be prepared for this map."),
    ).not.toBeInTheDocument();
  });

  test("centers an empty initial map on an available browser location", async () => {
    Object.defineProperty(navigator, "geolocation", {
      configurable: true,
      value: {
        getCurrentPosition: vi.fn((success: PositionCallback) =>
          success({ coords: { longitude: -104.99, latitude: 39.74 } } as GeolocationPosition),
        ),
      },
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      json({ ...selection, workouts: [], bounds: null }),
    );
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances.at(-1)!;
    vi.useFakeTimers();
    act(() => map.handlers.get("error")?.());
    expect(
      screen.getByText(
        "The public base map could not be loaded. Your private routes remain available. Retrying in 5 seconds...",
      ),
    ).toBeInTheDocument();
    expect(map.setStyle).toHaveBeenCalledWith(expect.objectContaining({ version: 8 }));
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
      type: "vector",
      tiles: [privateTemplate(selection.routeTileUrl)],
    });
    mapBehavior.emitSetStyleLoad = true;
    act(() => vi.advanceTimersByTime(4999));
    expect(map.setStyle).not.toHaveBeenLastCalledWith(
      "https://tiles.example.test/road-dark.json",
      expect.anything(),
    );
    act(() => vi.advanceTimersByTime(1));
    expect(map.setStyle).toHaveBeenLastCalledWith(
      "https://tiles.example.test/road-dark.json",
      expect.objectContaining({ transformStyle: expect.any(Function) }),
    );
    expect(screen.queryByText(/The public base map could not be loaded/)).not.toBeInTheDocument();
  });

  test("clears a pending base map retry when the provider style loads before the timer", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
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
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const tileURL = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
    act(() =>
      mapInstances.at(-1)?.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { status: 401, url: tileURL },
      }),
    );
    expect(expired).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  test("refreshes an unavailable current route capability and ignores stale tile errors", async () => {
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        posts++;
        const id = posts === 1 ? selection.id : "D".repeat(32);
        return Promise.resolve(
          json({
            ...selection,
            id,
            expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
          }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances[0];
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", expect.anything()),
    );
    const currentTile = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { status: 404, url: currentTile },
      }),
    );
    await waitFor(() => expect(posts).toBe(2));
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "D".repeat(32)))],
      }),
    );
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { status: 404, url: currentTile },
      }),
    );
    await new Promise((resolve) => setTimeout(resolve, 5));
    expect(posts).toBe(2);
  });

  test("does not refit a deep-linked route when its capability refreshes", async () => {
    const requested = selection.workouts[0];
    history.replaceState({}, "", `/map?workoutId=${requested.id}`);
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        posts++;
        const id = posts === 1 ? selection.id : posts === 2 ? "D".repeat(32) : "E".repeat(32);
        return Promise.resolve(
          json({
            ...selection,
            id,
            workouts: [requested],
            bounds: requested.bounds,
            expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
          }),
        );
      }
      if (path === `/api/workouts/${requested.id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const map = mapInstances[0];
    await waitFor(() => expect(posts).toBe(2));
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
    map.jumpTo({ center: [-104.5, 39.5], zoom: 13 });
    map.fitBounds.mockClear();
    const currentTile = `${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32)).replace("{z}/{x}/{y}", "12/654/1583")}`;
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { status: 404, url: currentTile },
      }),
    );
    await waitFor(() => expect(posts).toBe(3));
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "E".repeat(32)))],
      }),
    );
    expect(map.fitBounds).not.toHaveBeenCalled();
  });

  test("keeps recreated private route layers hidden when a capability refreshes in Coverage mode", async () => {
    const enabledConfig: PublicConfig = {
      ...config,
      features: { coverageMatcherDiagnostics: true },
    };
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        posts++;
        const id = posts === 1 ? selection.id : "D".repeat(32);
        return Promise.resolve(
          json({
            ...selection,
            id,
            expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
          }),
        );
      }
      if (
        path === `/api/workouts/${selection.workouts[0].id}/coverage-diagnostic-runs` &&
        method === "POST"
      )
        return Promise.resolve(json(diagnostic, 201));
      if (path === `/api/workouts/${selection.workouts[0].id}/route/points` && method === "GET")
        return Promise.resolve(json(rawRoutePoints));
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={enabledConfig}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/08\/2026/ }));
    const coverage = screen.getByRole("button", { name: "Coverage" });
    await waitFor(() => expect(coverage).toBeEnabled());
    await userEvent.click(coverage);
    await screen.findByRole("region", { name: "Coverage diagnostic review" });
    const map = mapInstances[0];
    const routeLayers = [
      "private-workout-routes",
      "private-workout-route-markers",
      "private-workout-route-hover",
      "private-workout-route-marker-hover",
    ];
    const visibility = (id: string) =>
      (map.layers.get(id) as { layout?: { visibility?: string } } | undefined)?.layout
        ?.visibility ?? "visible";
    await waitFor(() =>
      expect(routeLayers.map(visibility)).toEqual(["none", "none", "none", "none"]),
    );
    await waitFor(() => expect(map.fitBounds).toHaveBeenCalled());
    map.fitBounds.mockClear();
    const currentTile = `${window.location.origin}${selection.routeTileUrl.replace("{z}/{x}/{y}", "12/654/1583")}`;
    act(() =>
      map.handlers.get("error")?.({
        sourceId: "private-workout-routes",
        error: { status: 404, url: currentTile },
      }),
    );
    await waitFor(() => expect(posts).toBe(2));
    await waitFor(() =>
      expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "D".repeat(32)))],
      }),
    );
    expect(routeLayers.map(visibility)).toEqual(["none", "none", "none", "none"]);
    for (const layer of routeLayers)
      expect(map.setLayoutProperty).toHaveBeenCalledWith(layer, "visibility", "none");
    expect(map.fitBounds).not.toHaveBeenCalled();
    expect(coverage).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("region", { name: "Coverage diagnostic review" })).toBeInTheDocument();
  });

  test("refreshes an expired map selection capability on resume", async () => {
    let posts = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/map-selections" && method === "POST") {
        posts++;
        const id = posts === 1 ? selection.id : "E".repeat(32);
        return Promise.resolve(
          json({
            ...selection,
            id,
            expiresAt:
              posts === 1
                ? "2000-01-01T00:00:00Z"
                : new Date(Date.now() + 30 * 60 * 1000).toISOString(),
            routeTileUrl: selection.routeTileUrl.replace(selection.id, id),
          }),
        );
      }
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await waitFor(() => expect(posts).toBe(2));
    await waitFor(() =>
      expect(mapInstances[0].addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl.replace(selection.id, "E".repeat(32)))],
      }),
    );
  });

  test("installs private routes when an initial style event is missed", async () => {
    mapBehavior.emitInitialStyleLoad = false;
    mapBehavior.emitSetStyleLoad = false;
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    expect(map.addSource).not.toHaveBeenCalled();
    mapBehavior.styleLoaded = true;
    map.handlers.get("load")?.();
    expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", {
      type: "vector",
      tiles: [privateTemplate(selection.routeTileUrl)],
    });
  });

  test("installs a refresh selection after style.load while base tiles are still pending", async () => {
    mapBehavior.styleLoaded = false;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
    render(
      <MapPage
        config={config}
        preferences={preferences}
        csrfToken="csrf-map"
        dateRange="last30Days"
        onDateRangeSelected={vi.fn()}
      />,
    );
    await screen.findByRole("checkbox", { name: /Show Running/ });
    await waitFor(() =>
      expect(mapInstances.at(-1)?.addSource).toHaveBeenCalledWith("private-workout-routes", {
        type: "vector",
        tiles: [privateTemplate(selection.routeTileUrl)],
      }),
    );
  });
});
