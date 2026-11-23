import * as Dialog from "./Dialog";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { GeoJSONSource, Map as LibreMap } from "maplibre-gl";
import type { MultiPolygon, Polygon } from "geojson";
import "maplibre-gl/dist/maplibre-gl.css";
import { api, type BaseMapsConfig } from "./api";
import type { OSMRegion } from "./osmRegions";

export type RegionBoundary = { id: string; geometry: Polygon | MultiPolygon };
export function boundaryBounds(
  geometry: Polygon | MultiPolygon,
): [[number, number], [number, number]] {
  const positions =
    geometry.type === "Polygon" ? geometry.coordinates.flat() : geometry.coordinates.flat(2);
  const longitudes = positions
    .map(([longitude]) => ((((longitude + 180) % 360) + 360) % 360) - 180)
    .sort((a, b) => a - b);
  if (!longitudes.length) throw Error("Empty region boundary");
  let gap = -1,
    start = 0;
  for (let i = 0; i < longitudes.length; i++) {
    const distance =
      longitudes[(i + 1) % longitudes.length] +
      (i + 1 === longitudes.length ? 360 : 0) -
      longitudes[i];
    if (distance > gap) {
      gap = distance;
      start = (i + 1) % longitudes.length;
    }
  }
  const west = longitudes[start],
    east = longitudes[(start + longitudes.length - 1) % longitudes.length] + (start ? 360 : 0);
  let south = 90,
    north = -90;
  for (const [, latitude] of positions) {
    south = Math.min(south, latitude);
    north = Math.max(north, latitude);
  }
  return [
    [west, Math.max(-85.051129, south)],
    [east, Math.min(85.051129, north)],
  ];
}

export function OSMRegionBoundaryDialog({
  region,
  baseMaps,
  onClose,
  onReturnFocus,
}: {
  region: OSMRegion | undefined;
  baseMaps: BaseMapsConfig;
  onClose: () => void;
  onReturnFocus: () => void;
}) {
  const parking = useRef<HTMLDivElement>(null),
    host = useRef<HTMLDivElement | null>(null),
    container = useRef<HTMLDivElement | null>(null),
    close = useRef<HTMLButtonElement>(null);
  const map = useRef<LibreMap | null>(null),
    creating = useRef(false),
    lifetime = useRef(0),
    alive = useRef(true),
    styleReady = useRef(false);
  const hostObserver = useRef<ResizeObserver | null>(null),
    hostFrame = useRef<number | undefined>(undefined);
  const selected = useRef(region);
  selected.current = region;
  const [theme, setTheme] = useState<"light" | "dark">(() =>
    document.documentElement.dataset.theme === "light" ? "light" : "dark",
  );
  const [mapError, setMapError] = useState(false),
    [displayed, setDisplayed] = useState<string>(),
    [loaded, setLoaded] = useState(false),
    [retry, setRetry] = useState(0);
  const family = baseMaps.families.find(
    (item) => item.id.toLowerCase() === "streets" || item.label.toLowerCase().includes("streets"),
  );
  const style = family?.styles[theme],
    latestStyle = useRef(style);
  latestStyle.current = style;
  const installedStyle = useRef<string | undefined>(undefined),
    installedGeometry = useRef<Polygon | MultiPolygon | undefined>(undefined),
    fitRequest = useRef(0),
    fittedRequest = useRef(-1);
  const boundary = useQuery({
    queryKey: ["admin-osm-region-boundary", region?.id],
    queryFn: ({ signal }) =>
      api<RegionBoundary>(
        `/api/admin/osm/region-boundary?${new URLSearchParams({ regionId: region!.id })}`,
        { signal },
      ),
    enabled: Boolean(region),
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
  const data = useRef(boundary.data);
  data.current = boundary.data;
  const sync = useRef<() => void>(() => {});
  sync.current = () => {
    const instance = map.current,
      geometry = data.current;
    if (
      !instance ||
      !styleReady.current ||
      !selected.current ||
      !host.current ||
      geometry?.id !== selected.current.id
    )
      return;
    const feature = { type: "Feature" as const, properties: {}, geometry: geometry.geometry };
    const source = instance.getSource("admin-region-boundary") as GeoJSONSource | undefined;
    if (!source) {
      instance.addSource("admin-region-boundary", { type: "geojson", data: feature });
      const before = instance.getStyle().layers?.find((layer) => layer.type === "symbol")?.id;
      instance.addLayer(
        {
          id: "admin-region-boundary-fill",
          type: "fill",
          source: "admin-region-boundary",
          paint: { "fill-color": "#ff00ff", "fill-opacity": 0.25 },
        },
        before,
      );
      instance.addLayer(
        {
          id: "admin-region-boundary-line",
          type: "line",
          source: "admin-region-boundary",
          layout: { "line-join": "round", "line-round-limit": 0 },
          paint: { "line-color": "#ff00ff", "line-width": 5 },
        },
        before,
      );
    } else if (installedGeometry.current !== geometry.geometry) source.setData(feature);
    installedGeometry.current = geometry.geometry;
    instance.resize();
    if (fittedRequest.current !== fitRequest.current) {
      instance.fitBounds(boundaryBounds(geometry.geometry), { padding: 40, duration: 0 });
      fittedRequest.current = fitRequest.current;
    }
    setDisplayed(geometry.id);
    setLoaded(true);
    setMapError(false);
  };
  const attachHost = useCallback((node: HTMLDivElement | null) => {
    host.current = node;
    hostObserver.current?.disconnect();
    hostObserver.current = null;
    if (hostFrame.current !== undefined) cancelAnimationFrame(hostFrame.current);
    hostFrame.current = undefined;
    // Keep the same WebGL canvas alive in a hidden parking node while Radix
    // unmounts its modal portal. Reopening only reparents/resizes that map.
    const destination = node ?? parking.current;
    if (destination && container.current && container.current.parentNode !== destination)
      destination.append(container.current);
    if (node) {
      const schedule = () => {
        if (hostFrame.current !== undefined) cancelAnimationFrame(hostFrame.current);
        hostFrame.current = requestAnimationFrame(() => {
          hostFrame.current = undefined;
          sync.current();
        });
      };
      if (typeof ResizeObserver !== "undefined") {
        hostObserver.current = new ResizeObserver(schedule);
        hostObserver.current.observe(node);
      }
      schedule();
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      lifetime.current++;
      creating.current = false;
      hostObserver.current?.disconnect();
      if (hostFrame.current !== undefined) cancelAnimationFrame(hostFrame.current);
      map.current?.remove();
      map.current = null;
      container.current?.remove();
      container.current = null;
    };
  }, []);
  useEffect(() => {
    const observer = new MutationObserver(() =>
      setTheme(document.documentElement.dataset.theme === "light" ? "light" : "dark"),
    );
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!region || !family || map.current || creating.current) return;
    creating.current = true;
    const generation = lifetime.current;
    void import("maplibre-gl")
      .then(({ default: library }) => {
        if (!alive.current || generation !== lifetime.current) return;
        const element = document.createElement("div");
        element.className = "osm-regions-boundary-map";
        container.current = element;
        (host.current ?? parking.current)?.append(element);
        const instance = new library.Map({
          container: element,
          style: latestStyle.current!,
          center: [0, 0],
          zoom: 1,
          attributionControl: false,
        });
        map.current = instance;
        installedStyle.current = latestStyle.current;
        instance.on("style.load", () => {
          if (!alive.current) return;
          styleReady.current = true;
          sync.current();
        });
        instance.on("error", () => {
          if (alive.current && !styleReady.current && selected.current) setMapError(true);
        });
        creating.current = false;
      })
      .catch(() => {
        creating.current = false;
        if (alive.current) setMapError(true);
      });
  }, [Boolean(region), family, retry]);
  useLayoutEffect(() => {
    if (!region) {
      map.current?.stop();
      return;
    }
    fitRequest.current++;
    sync.current();
  }, [region?.id]);
  useEffect(() => {
    sync.current();
  }, [boundary.data]);
  useEffect(() => {
    if (map.current && style && (installedStyle.current !== style || retry > 0)) {
      styleReady.current = false;
      setLoaded(false);
      installedStyle.current = style;
      map.current.setStyle(style, { diff: false });
    }
  }, [style, retry]);
  const visible = loaded && displayed === region?.id && !mapError;
  return (
    <>
      <div ref={parking} hidden inert aria-hidden="true" className="osm-regions-map-parking" />
      <Dialog.Root
        open={Boolean(region)}
        onOpenChange={(open) => {
          if (!open) onClose();
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content osm-regions-boundary-dialog"
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              close.current?.focus();
            }}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              onReturnFocus();
            }}
          >
            <div className="dialog-heading">
              <Dialog.Title>{region?.name}</Dialog.Title>
              <Dialog.Description className="visually-hidden">
                Region boundaries on the Streets map.
              </Dialog.Description>
              <Dialog.Close
                ref={close}
                className="icon-button"
                aria-label="Close region boundaries"
              >
                &times;
              </Dialog.Close>
            </div>
            <div className="osm-regions-boundary-body">
              <div
                ref={attachHost}
                className="osm-regions-boundary-host"
                style={{ visibility: visible ? "visible" : "hidden" }}
                role="region"
                aria-label={`${region?.name ?? "Region"} boundary map`}
              />
              {!visible && !boundary.isError && !mapError && family && (
                <p className="osm-regions-boundary-state" role="status">
                  Loading region map...
                </p>
              )}
              {(boundary.isError || mapError || !family) && (
                <div className="osm-regions-boundary-state" role="alert">
                  <p>
                    {!family
                      ? "The Streets base map is not configured."
                      : "Region boundaries could not be loaded."}
                  </p>
                  {family && (
                    <button
                      className="secondary"
                      onClick={() =>
                        boundary.isError ? void boundary.refetch() : setRetry((old) => old + 1)
                      }
                    >
                      Retry
                    </button>
                  )}
                </div>
              )}
              {family && (
                <div className="map-attribution">
                  <span>{family.attribution.text}</span>
                  {family.attribution.links.map((link) => (
                    <a key={link.url} href={link.url} target="_blank" rel="noreferrer">
                      {link.label}
                    </a>
                  ))}
                </div>
              )}
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </>
  );
}
