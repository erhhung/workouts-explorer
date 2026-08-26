import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiError, type BaseMapsConfig, type MapSelection, type Preferences, type PublicConfig } from "./api";
import MapPage, { absoluteRouteTileTemplate, formatRoutePopupDetails, formatRoutePopupDistance, requestedWorkoutIds, resolveBaseFamily, routeColor, routeColors, selectionRequest, sortMapWorkouts } from "./MapPage";

const mapInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const popupInstances = vi.hoisted(() => [] as Array<Record<string, any>>);
const mapBehavior = vi.hoisted(() => ({ emitInitialStyleLoad: true, emitSetStyleLoad: true, styleLoaded: true }));
const originalGeolocation = Object.getOwnPropertyDescriptor(navigator, "geolocation");

vi.mock("maplibre-gl", () => ({
  default: (() => {
    class MockMap {
      layers = new Map<string, unknown>();
      sources = new Map<string, unknown>();
      handlers = new Map<string, (...args: never[]) => void>();
      addLayer = vi.fn((layer: { id: string }) => { this.layers.set(layer.id, layer); });
      addSource = vi.fn((id: string, source: object) => { this.sources.set(id, { ...source, setTiles: vi.fn() }); });
      removeLayer = vi.fn((id: string) => { this.layers.delete(id); });
      removeSource = vi.fn((id: string) => { this.sources.delete(id); });
      getLayer = vi.fn((id: string) => this.layers.get(id));
      getSource = vi.fn((id: string) => this.sources.get(id));
      fitBounds = vi.fn();
      setFilter = vi.fn();
      setPaintProperty = vi.fn();
      jumpTo = vi.fn();
      setStyle = vi.fn((style: unknown) => { if (typeof style !== "string") mapBehavior.styleLoaded = true; if (typeof style !== "string" || mapBehavior.emitSetStyleLoad) this.handlers.get("style.load")?.(); });
      getStyle = vi.fn(() => ({}));
      isStyleLoaded = vi.fn(() => mapBehavior.styleLoaded);
      queryRenderedFeatures = vi.fn(() => []);
      getCanvas = vi.fn(() => document.createElement("canvas"));
      addControl = vi.fn();
      remove = vi.fn();
      on = vi.fn((event: string, layerOrHandler: string | ((...args: never[]) => void), handler?: (...args: never[]) => void) => {
        const callback = typeof layerOrHandler === "function" ? layerOrHandler : handler!;
        const previous = this.handlers.get(event);
        this.handlers.set(event, previous ? ((...args: never[]) => { previous(...args); callback(...args); }) : callback);
        if (event === "style.load" && mapBehavior.emitInitialStyleLoad) callback();
      });
      off = vi.fn();
      constructor(public options: unknown) { mapInstances.push(this); }
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
const config: PublicConfig = { productName: "Workouts Explorer", pollingIntervalSeconds: 30, mapFitPaddingPixels: 48, passwordMinimumLength: 12, pageSizeMaximum: 100, baseMaps };
const preferences: Preferences = { theme: "dark", units: "metric", timezone: "America/Denver", firstWeekday: "monday", clockFormat: "24h", workoutColumns: ["date", "type"], pageSize: 25, initialized: true, dateRange: "last30Days" };
const selection: MapSelection = {
  id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", expiresAt: "2026-08-08T13:00:00Z", dataGeneration: 7,
  range: { startDate: "2026-07-10", endDate: "2026-08-08" },
  bounds: { minimumLongitude: -105.3, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40.1 },
  workouts: [
    { id: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", type: { id: "11111111111111111111111111111111", key: "running", name: "Running" }, startedAt: "2026-08-08T12:00:00Z", endedAt: "2026-08-08T13:45:00Z", duration: "6300", localStartDate: "2026-08-08", partialRoute: false, bounds: { minimumLongitude: -105.3, minimumLatitude: 39.9, maximumLongitude: -105.2, maximumLatitude: 40.1 }, distance: { value: "8.25", unit: "km" }, pace: { value: "5", unit: "min/km" }, calories: { value: "500", unit: "kcal" }, heartRate: { value: "120", unit: "count/min" }, elevationGain: { value: "100", unit: "m" } },
    { id: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" }, startedAt: "2026-08-01T12:00:00Z", endedAt: "2026-08-01T13:00:00Z", duration: "3600", localStartDate: "2026-08-01", partialRoute: true, bounds: { minimumLongitude: -105.2, minimumLatitude: 39.8, maximumLongitude: -105.1, maximumLatitude: 40 }, distance: null, pace: null, calories: { value: "300", unit: "kcal" }, heartRate: { value: "100", unit: "count/min" }, elevationGain: { value: "250", unit: "m" } },
  ],
  routeTileUrl: "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/route-tiles/7/{z}/{x}/{y}.pbf",
};

beforeEach(() => { mapInstances.splice(0); popupInstances.splice(0); mapBehavior.emitInitialStyleLoad = true; mapBehavior.emitSetStyleLoad = true; mapBehavior.styleLoaded = true; });
afterEach(() => {
  vi.useRealTimers();
  if (originalGeolocation) Object.defineProperty(navigator, "geolocation", originalGeolocation);
  else Object.defineProperty(navigator, "geolocation", { configurable: true, value: undefined });
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
  });
});

describe("MapPage", () => {
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
    expect(screen.getByLabelText("Active map attribution for Outdoor")).toHaveTextContent("Outdoor mapMap data");
    expect(screen.getByRole("link", { name: "Map data" })).toHaveAttribute("href", "https://attribution.example.test");
    expect(requests[0]).toEqual({ path: "/api/map-selections", method: "POST", body: { dateRangeEnum: "last30Days", tz: "America/Denver" }, csrf: "csrf-map" });
    await waitFor(() => expect(mapInstances.length).toBeGreaterThan(0));
    const map = mapInstances.at(-1)!;
    await waitFor(() => expect(map.addSource).toHaveBeenCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl}`] }));
    expect(map.addLayer.mock.calls.some((call: any[]) => call[0]["source-layer"] === "routes" && call[0].layout["line-sort-key"][1] === "sort_order")).toBe(true);
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
      if (method === "DELETE") return Promise.resolve(json(undefined, 204));
      throw new Error(`Unexpected request ${method} ${path}`);
    });
    render(<MapPage config={config} preferences={preferences} csrfToken="csrf-map" dateRange="last30Days" onDateRangeSelected={vi.fn()} />);
    await screen.findByRole("checkbox", { name: /Show Running/ });
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox", { name: /Running/ }));
    await waitFor(() => expect(posted).toContainEqual({ dateRangeEnum: "last30Days", tz: "America/Denver", workoutIds: [selection.workouts[1].id] }));
    expect(mapInstances).toHaveLength(1);
    const source = mapInstances[0].sources.get("private-workout-routes");
    await waitFor(() => expect(source.setTiles).toHaveBeenCalledWith([`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "D".repeat(32))}`]));
    expect(mapInstances[0].remove).not.toHaveBeenCalled();
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
    await user.click(screen.getByRole("button", { name: "None" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Fit routes" })).toBeDisabled());
    await user.click(screen.getByRole("button", { name: /Running.*8\/08\/2026/ }));
    await waitFor(() => expect(posted).toContainEqual({ dateRangeEnum: "last30Days", tz: "America/Denver", workoutIds: [selection.workouts[0].id] }));
    await waitFor(() => expect(map.removeSource).toHaveBeenCalledWith("private-workout-routes"));
    expect(map.addSource).toHaveBeenLastCalledWith("private-workout-routes", { type: "vector", tiles: [`${window.location.origin}${selection.routeTileUrl.replace(selection.id, "F".repeat(32))}`] });
    expect(mapInstances).toHaveLength(1);
    expect(map.remove).not.toHaveBeenCalled();
  });

  test("delays the two-line route popup and synchronizes the workout highlight", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json(selection));
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
    act(() => vi.advanceTimersByTime(499));
    expect(popupInstances).toHaveLength(0);
    act(() => vi.advanceTimersByTime(1));
    expect(popupInstances).toHaveLength(1);
    expect(popupInstances[0].content).toHaveTextContent("Running8.25 km");
    expect(popupInstances[0].content.children).toHaveLength(4);
    expect(popupInstances[0].content).toHaveTextContent("8/08/20266:00 - 7:45");
    vi.useRealTimers();
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
