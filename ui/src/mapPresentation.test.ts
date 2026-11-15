import type { Map as MapLibreMap } from "maplibre-gl";
import { describe, expect, test, vi } from "vitest";
import {
  orderedMapLayerIds,
  syncMapLayerPresentation,
  type MapLayerGroups,
} from "./mapPresentation";

const groups: MapLayerGroups = {
  background: ["routes", "coverage"],
  focused: ["focus", "diagnostic", "hit"],
  directions: ["arrows"],
  endpoints: ["finish", "start"],
  attention: ["blink"],
};
type Layer = { id: string; type: string; "source-layer"?: string; paint?: Record<string, unknown> };
function mapWith(layers: Layer[]) {
  let order = layers.map((layer) => layer.id);
  const byId = new Map(layers.map((layer) => [layer.id, layer]));
  const map = {
    getLayersOrder: () => order,
    getLayer: (id: string) => byId.get(id),
    getPaintProperty: (id: string, property: string) => byId.get(id)?.paint?.[property],
    setPaintProperty: vi.fn((id: string, property: string, value: unknown) => {
      const layer = byId.get(id)!;
      (layer.paint ??= {})[property] = value;
    }),
    moveLayer: vi.fn((id: string, before?: string) => {
      order = order.filter((candidate) => candidate !== id);
      order.splice(before === undefined ? order.length : order.indexOf(before), 0, id);
    }),
  };
  return { map, reconcile: () => syncMapLayerPresentation(map as unknown as MapLibreMap, groups) };
}

describe("map presentation", () => {
  test("keeps arrows directly above focused paths, provider symbols above arrows, and blink topmost", () => {
    const layers = [
      { id: "terrain", type: "hillshade" },
      { id: "road-name", type: "symbol" },
      { id: "road-line", type: "line" },
      { id: "shield", type: "symbol" },
      { id: "blink", type: "line" },
      { id: "start", type: "circle" },
      { id: "focus", type: "line" },
      { id: "coverage", type: "line" },
      { id: "arrows", type: "symbol" },
      { id: "routes", type: "line" },
      { id: "finish", type: "symbol" },
      { id: "diagnostic", type: "line" },
      { id: "hit", type: "line" },
    ];
    const { map, reconcile } = mapWith(layers);
    const originalOrder = map.getLayersOrder();
    reconcile();
    expect(map.getLayersOrder()).toEqual([
      "terrain",
      "road-line",
      "routes",
      "coverage",
      "focus",
      "diagnostic",
      "hit",
      "arrows",
      "road-name",
      "shield",
      "finish",
      "start",
      "blink",
    ]);
    expect(originalOrder).toEqual(layers.map((layer) => layer.id));
    map.moveLayer.mockClear();
    reconcile();
    expect(map.moveLayer).not.toHaveBeenCalled();
    expect(map.setPaintProperty).not.toHaveBeenCalled();
  });

  test("restores ordering after asynchronous focus recreation without moving attention below it", () => {
    const { map, reconcile } = mapWith([
      { id: "terrain", type: "background" },
      { id: "road-name", type: "symbol" },
      { id: "start", type: "circle" },
      { id: "blink", type: "line" },
      { id: "focus", type: "line" },
    ]);
    reconcile();
    expect(map.getLayersOrder()).toEqual(["terrain", "focus", "road-name", "start", "blink"]);
  });

  test.each(["road_label", "transportation_name"])(
    "makes %s labels and shields fully opaque while preserving unrelated provider styling",
    (sourceLayer) => {
      const { map, reconcile } = mapWith([
        {
          id: "road-name",
          type: "symbol",
          "source-layer": sourceLayer,
          paint: { "text-opacity": 0.7 },
        },
        {
          id: "shield",
          type: "symbol",
          "source-layer": sourceLayer,
          paint: { "icon-opacity": 0.8 },
        },
        { id: "place", type: "symbol", "source-layer": "place", paint: { "text-opacity": 0.6 } },
        { id: "arrows", type: "symbol", paint: { "icon-opacity": 0.95 } },
      ]);
      reconcile();
      expect(map.getPaintProperty("road-name", "text-opacity")).toBe(1);
      expect(map.getPaintProperty("shield", "icon-opacity")).toBe(1);
      expect(map.getPaintProperty("place", "text-opacity")).toBe(0.6);
      expect(map.getPaintProperty("arrows", "icon-opacity")).toBe(0.95);
      map.setPaintProperty.mockClear();
      reconcile();
      expect(map.setPaintProperty).not.toHaveBeenCalled();
    },
  );

  test("style transformation and live reconciliation use the same order with absent optional layers", () => {
    const layers = [
      { id: "terrain", type: "background" },
      { id: "road-name", type: "symbol" },
      { id: "focus", type: "line" },
      { id: "start", type: "circle" },
      { id: "arrows", type: "symbol" },
    ];
    const { map, reconcile } = mapWith(layers);
    reconcile();
    expect(orderedMapLayerIds(layers, groups)).toEqual(map.getLayersOrder());
    expect(map.getLayersOrder()).toHaveLength(layers.length);
  });
});
