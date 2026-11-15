import type { Map as MapLibreMap } from "maplibre-gl";

export const MAP_PATH_OPACITY = 0.9;

export type MapLayerGroups = {
  background: readonly string[];
  focused: readonly string[];
  directions: readonly string[];
  endpoints: readonly string[];
  attention: readonly string[];
};
type Layer = { id: string; type: string };

export function orderedMapLayerIds(layers: readonly Layer[], groups: MapLayerGroups) {
  const privateIds = new Set(Object.values(groups).flat());
  const existing = new Set(layers.map((layer) => layer.id));
  const base = layers.filter((layer) => !privateIds.has(layer.id));
  const present = (ids: readonly string[]) => ids.filter((id) => existing.has(id));
  return [
    ...base.filter((layer) => layer.type !== "symbol").map((layer) => layer.id),
    ...present(groups.background),
    ...present(groups.focused),
    ...present(groups.directions),
    ...base.filter((layer) => layer.type === "symbol").map((layer) => layer.id),
    ...present(groups.endpoints),
    ...present(groups.attention),
  ];
}

export function syncMapLayerPresentation(map: MapLibreMap, groups: MapLayerGroups) {
  const current = [...map.getLayersOrder()];
  const privateIds = new Set(Object.values(groups).flat());
  const layers = current.map((id) => ({ id, type: map.getLayer(id)?.type ?? "unknown" }));
  for (const layer of layers) {
    if (layer.type !== "symbol" || privateIds.has(layer.id)) continue;
    const source = map.getLayer(layer.id) as { sourceLayer?: string; "source-layer"?: string };
    const roadLabel = ["road_label", "transportation_name"].includes(
      source.sourceLayer ?? source["source-layer"] ?? "",
    );
    if (!roadLabel) continue;
    for (const property of ["text-opacity", "icon-opacity"]) {
      const value = map.getPaintProperty(layer.id, property);
      if (value !== undefined && value !== 1) map.setPaintProperty(layer.id, property, 1);
    }
  }
  const desired = orderedMapLayerIds(layers, groups);
  // Reconcile only out-of-order layers; ordinary camera frames do no style work.
  for (let index = desired.length - 1; index >= 0; index--) {
    const id = desired[index],
      before = desired[index + 1],
      at = current.indexOf(id);
    if (current[at + 1] === before) continue;
    if (before === undefined) map.moveLayer(id);
    else map.moveLayer(id, before);
    current.splice(at, 1);
    current.splice(before === undefined ? current.length : current.indexOf(before), 0, id);
  }
}
