import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import * as Dialog from "./Dialog";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { api, type BaseMapsConfig, type Session } from "./api";
import { DialogTableViewport } from "./DialogTableViewport";
import { OSMRegionBoundaryDialog } from "./OSMRegionBoundaryDialog";
import { OSMRegionProgressDialog } from "./OSMRegionProgressDialog";
import { PageNavigation } from "./PageNavigation";
import { useTabActive } from "./RetainedTab";
import { useKeyboardShortcut } from "./keyboardShortcuts";
import { Tooltip, dismissActivePortalTooltip } from "./Tooltip";
import {
  REGION_COLUMNS,
  catalogPending,
  canViewRegionProgress,
  canOpenStatusProgress,
  exactBytes,
  elapsedTotal,
  osmBytes,
  regionDate,
  regionExactTime,
  regionStatus,
  regionValue,
  updateActive,
  updateStatus,
  type RegionUpdate,
  type CatalogRefresh,
  type OSMCatalogs,
  type OSMRegion,
  type RegionSort,
} from "./osmRegions";
export type { CatalogRefresh, OSMCatalogs, OSMRegion } from "./osmRegions";
export { osmBytes } from "./osmRegions";

const KEY = ["admin-osm-regions"];
const NUMERIC = [
  "pbfModifiedAt",
  "pbfBytes",
  "downloadedAt",
  "generation",
  "databaseBytes",
  "workoutCount",
] as const;
function useNameColumnWidth(table: RefObject<HTMLTableElement | null>) {
  useLayoutEffect(() => {
    const element = table.current;
    if (!element) return;
    const update = () => {
      const section = element.closest<HTMLElement>(".osm-regions-section"),
        parent = section?.parentElement,
        rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
      let width = element.getBoundingClientRect().width - 7 * rem;
      if (parent) {
        const style = getComputedStyle(parent);
        const available =
          parent.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
        const inset = section!.getBoundingClientRect().width - element.parentElement!.clientWidth;
        width = Math.max(64 * rem, Math.min(available, 80 * rem) - inset);
        element.style.width = `${width + 7 * rem}px`;
        const columns = element.querySelectorAll("col");
        [26.6, 14, 14, 14.4, 11, 15].forEach((track, index) => {
          if (index > 0)
            columns[index].style.width =
              `${((track / 107) * 94 * width) / 100 - (index === 2 ? rem : 0)}px`;
        });
      }
      if (width <= 0) return;
      const actions = element
        .querySelector("th.workout-actions-heading")!
        .getBoundingClientRect().width;
      const unit = (width * 0.94) / 107;
      const previousStatus = width - 95 * unit - actions;
      // Transfer a tenth of both original tracks; Status remains the flexible
      // last track so the fixed ellipsis boundary cannot develop spare space.
      element.style.setProperty(
        "--osm-regions-name-width",
        `${25 * unit + 1.6 * unit + 0.1 * previousStatus}px`,
      );
    };
    update();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(update) : undefined;
    observer?.observe(element);
    if (element.closest(".osm-regions-section")?.parentElement)
      observer?.observe(element.closest(".osm-regions-section")!.parentElement!);
    window.addEventListener("resize", update);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", update);
    };
  }, [table]);
}
type Numeric = (typeof NUMERIC)[number];
function useExtents(regions: OSMRegion[], table: RefObject<HTMLTableElement | null>) {
  const candidates = useMemo(
    () =>
      Object.fromEntries(
        NUMERIC.map((field) => [
          field,
          [...new Set(regions.map((region) => regionValue(region, field)))],
        ]),
      ) as Record<Numeric, string[]>,
    [regions],
  );
  const fallback = useMemo(
    () =>
      Object.fromEntries(
        NUMERIC.map((field) => [
          field,
          candidates[field].reduce((a, b) => (b.length > a.length ? b : a), "n/a"),
        ]),
      ) as Record<Numeric, string>,
    [candidates],
  );
  const [measured, setMeasured] = useState(fallback);
  useLayoutEffect(() => {
    let alive = true;
    const measure = () => {
      if (!alive || !table.current) return;
      const style = getComputedStyle(
        table.current.querySelector(".workout-value-content") ?? table.current,
      );
      const probe = document.createElement("span");
      probe.setAttribute("aria-hidden", "true");
      Object.assign(probe.style, {
        position: "fixed",
        visibility: "hidden",
        left: "-10000px",
        whiteSpace: "pre",
        font: style.font,
        fontVariantNumeric: "tabular-nums",
        letterSpacing: style.letterSpacing,
      });
      document.body.append(probe);
      const next = { ...fallback };
      for (const field of NUMERIC) {
        let widest = 0;
        for (const value of candidates[field]) {
          probe.textContent = value;
          const width = probe.getBoundingClientRect().width;
          if (width > widest) {
            widest = width;
            next[field] = value;
          }
        }
      }
      probe.remove();
      setMeasured((old) => (NUMERIC.every((field) => old[field] === next[field]) ? old : next));
    };
    measure();
    void document.fonts?.ready.then(measure);
    document.fonts?.addEventListener("loadingdone", measure);
    return () => {
      alive = false;
      document.fonts?.removeEventListener("loadingdone", measure);
    };
  }, [candidates, fallback, table]);
  return measured;
}

function Value({
  region,
  field,
  extent,
}: {
  region: OSMRegion;
  field: RegionSort | "generation";
  extent?: string;
}) {
  const text = regionValue(region, field),
    unavailable = text === "n/a";
  const help =
    field === "workoutCount" && region.generation != null
      ? `${region.coverageRouteCount ?? 0} coverage routes`
      : field === "pbfModifiedAt"
        ? regionExactTime(region.pbfModifiedAt)
        : field === "pbfBytes"
          ? exactBytes(region.pbfBytes)
          : field === "downloadedAt"
            ? exactBytes(region.downloadedBytes)
            : field === "databaseBytes"
              ? exactBytes(region.databaseBytes)
              : field === "generation" && region.promotedAt
                ? [
                    regionDate(region.promotedAt, true),
                    elapsedTotal(region.generationElapsedSeconds),
                  ]
                    .filter(Boolean)
                    .join(" | ")
                : undefined;
  const content = <span className={unavailable ? "workout-table-na" : undefined}>{text}</span>;
  const value = help ? (
    <Tooltip
      content={help ?? ""}
      enabled={Boolean(help)}
      portal
      focusable={false}
      className={`osm-regions-value-tooltip${field === "generation" || field === "workoutCount" ? " osm-regions-generation-tooltip" : ""}`}
    >
      {content}
    </Tooltip>
  ) : (
    content
  );
  return extent === undefined ? (
    value
  ) : (
    <span
      className={`workout-value-lane${field === "generation" || field === "workoutCount" ? " osm-regions-generation-value" : ""}`}
      data-width-sample={extent}
    >
      <span className="workout-value-content">{value}</span>
    </span>
  );
}

function RegionActions({
  region,
  enabled,
  onBoundaries,
  updatesEnabled,
  onCommand,
  onProgress,
}: {
  region: OSMRegion;
  enabled: boolean;
  onBoundaries: () => void;
  updatesEnabled: boolean;
  onCommand: (
    action: "download" | "reprocess" | "pause" | "resume" | "cancel" | "clear" | "delete",
  ) => void;
  onProgress: () => void;
}) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!enabled) setOpen(false);
  }, [enabled]);
  const status = regionStatus(region),
    running = status === "Downloading" || status === "Processing";
  const update = region.update,
    active = updateActive(update),
    stopping = status === "Pausing" || status === "Canceling";
  const downloaded =
    region.downloadedBytes !== null || region.downloadedAt !== null || region.generation !== null;
  return (
    <DropdownMenu.Root open={open && enabled} onOpenChange={setOpen}>
      <DropdownMenu.Trigger
        className="workout-action-trigger"
        aria-label={`Actions for ${region.name}`}
        disabled={!enabled}
      >
        <span aria-hidden="true">...</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content workout-action-menu"
          align="end"
          sideOffset={6}
        >
          <DropdownMenu.Item onSelect={onBoundaries}>Show boundaries</DropdownMenu.Item>
          {(running || canViewRegionProgress(region)) && (
            <>
              <DropdownMenu.Item
                disabled={!updatesEnabled || !canViewRegionProgress(region)}
                onSelect={onProgress}
              >
                View job progress
              </DropdownMenu.Item>
              <DropdownMenu.Separator />
            </>
          )}
          {status === "Ready" && !canViewRegionProgress(region) && <DropdownMenu.Separator />}
          {!active && status !== "Deleting" && status !== "Paused" && status !== "Failed" && (
            <DropdownMenu.Item
              disabled={!updatesEnabled || !region.available}
              onSelect={() => onCommand(status === "Ready" ? "reprocess" : "download")}
            >
              {status === "Ready" ? "Reprocess region" : "Download region"}
            </DropdownMenu.Item>
          )}
          {update && (status === "Paused" || status === "Failed") && (
            <DropdownMenu.Item disabled={!updatesEnabled} onSelect={() => onCommand("resume")}>
              Resume processing
            </DropdownMenu.Item>
          )}
          {active && status === "Processing" && (
            <DropdownMenu.Item
              disabled={!updatesEnabled || Boolean(update?.promoted)}
              onSelect={() => onCommand("pause")}
            >
              Pause processing
            </DropdownMenu.Item>
          )}
          {((active && status !== "Deleting") ||
            status === "Paused" ||
            (status === "Failed" && update?.resumable && !update.promoted)) && (
            <DropdownMenu.Item
              disabled={!updatesEnabled || stopping || Boolean(update?.promoted)}
              onSelect={() => onCommand("cancel")}
            >
              {status === "Paused" || update?.phase === "Processing"
                ? "Cancel processing"
                : "Cancel download"}
            </DropdownMenu.Item>
          )}
          {update && (status === "Failed" || status === "Canceled") && (
            <DropdownMenu.Item disabled={!updatesEnabled} onSelect={() => onCommand("clear")}>
              Clear job status
            </DropdownMenu.Item>
          )}
          {(downloaded || status === "Paused" || status === "Failed" || status === "Deleting") && (
            <>
              <DropdownMenu.Separator />
              <DropdownMenu.Item
                disabled={!updatesEnabled || active || status === "Deleting" || running || stopping}
                className="danger-item"
                onSelect={() => onCommand("delete")}
              >
                Delete region
              </DropdownMenu.Item>
            </>
          )}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function AdminOSMRegions({
  session,
  polling,
  headingId,
  baseMaps,
}: {
  session: Session;
  polling: number;
  headingId: string;
  baseMaps: BaseMapsConfig;
}) {
  const active = useTabActive(),
    activeRef = useRef(active);
  activeRef.current = active;
  const client = useQueryClient(),
    submitLock = useRef(false),
    table = useRef<HTMLTableElement>(null),
    scroll = useRef<HTMLDivElement>(null),
    heading = useRef<HTMLHeadingElement>(null);
  const [search, setSearch] = useState(""),
    [showAll, setShowAll] = useState(false),
    [page, setPage] = useState(1);
  useKeyboardShortcut("osm-regions", table, (shortcut) => {
    if (shortcut.kind !== "osm-regions" || !activeRef.current) return false;
    setShowAll((current) => !current);
    setPage(1);
    return true;
  });
  const [sort, setSort] = useState<{ field: RegionSort; direction: "asc" | "desc" }>({
    field: "name",
    direction: "asc",
  });
  const [selected, setSelected] = useState<OSMRegion>();
  const [deleteTarget, setDeleteTarget] = useState<OSMRegion>();
  const deleteCancel = useRef<HTMLButtonElement>(null),
    deleteFocus = useRef<HTMLButtonElement | null>(null);
  const [progressId, setProgressId] = useState<string>();
  const progressFocus = useRef<HTMLButtonElement | null>(null);
  const commandLock = useRef(false),
    commandFocus = useRef<HTMLButtonElement | null>(null);
  const lastSelected = useRef<string | undefined>(undefined);
  const query = useQuery({
    queryKey: KEY,
    queryFn: ({ signal }) => api<OSMCatalogs>("/api/admin/osm/regions", { signal }),
    enabled: active,
    refetchInterval: (query) =>
      active
        ? Math.min(
            polling * 1000,
            catalogPending(query.state.data?.refresh)
              ? 5000
              : query.state.data?.regions.some((region) => updateActive(region.update))
                ? 10000
                : Infinity,
          )
        : false,
    refetchIntervalInBackground: false,
  });
  const refresh = useMutation({
    mutationFn: () =>
      api<CatalogRefresh>(
        "/api/admin/osm/catalog-refreshes",
        { method: "POST", body: "{}" },
        session.csrfToken,
      ),
    onSuccess: async (job) => {
      await client.cancelQueries({ queryKey: KEY, exact: true });
      if (client.getQueryData<Session>(["admin-session"])?.id !== session.id) return;
      client.setQueryData<OSMCatalogs>(KEY, (old) => (old ? { ...old, refresh: job } : old));
      void client.invalidateQueries({ queryKey: KEY, exact: true });
    },
    onSettled: () => {
      submitLock.current = false;
    },
  });
  const command = useMutation({
    mutationFn: ({
      region,
      action,
    }: {
      region: OSMRegion;
      action: "download" | "reprocess" | "pause" | "resume" | "cancel" | "clear" | "delete";
    }) =>
      api<RegionUpdate>(
        action === "download" || action === "reprocess" || action === "delete"
          ? "/api/admin/osm/region-updates"
          : `/api/admin/osm/region-updates/${region.update!.id}/commands`,
        {
          method: "POST",
          body: JSON.stringify(
            action === "download" || action === "reprocess" || action === "delete"
              ? { regionId: region.id, action }
              : { action },
          ),
        },
        session.csrfToken,
      ),
    onSuccess: async (update) => {
      setDeleteTarget(undefined);
      await client.cancelQueries({ queryKey: KEY, exact: true });
      if (client.getQueryData<Session>(["admin-session"])?.id !== session.id) return;
      client.setQueryData<OSMCatalogs>(KEY, (old) =>
        old
          ? {
              ...old,
              regions: old.regions.map((region) =>
                region.id === update.regionId
                  ? {
                      ...region,
                      update: update.cleared ? undefined : update,
                      status: update.cleared
                        ? region.generation === null
                          ? "Available"
                          : "Ready"
                        : updateStatus(update),
                    }
                  : region,
              ),
            }
          : old,
      );
      void client.invalidateQueries({ queryKey: KEY, exact: true });
      void client.invalidateQueries({ queryKey: ["admin-osm-region-update", update.id] });
    },
    onSettled: () => {
      commandLock.current = false;
    },
  });
  useEffect(() => {
    if (!command.isPending && commandFocus.current) {
      if (activeRef.current)
        (commandFocus.current.isConnected && !commandFocus.current.closest("[hidden]")
          ? commandFocus.current
          : heading.current
        )?.focus();
      commandFocus.current = null;
    }
  }, [command.isPending]);
  useEffect(() => {
    if (!active) {
      setSelected(undefined);
      setProgressId(undefined);
      setDeleteTarget(undefined);
    }
  }, [active]);
  const regions = query.data?.regions ?? EMPTY;
  const rows = useMemo(
    () =>
      regions
        .filter(
          (region) =>
            (showAll || regionStatus(region) !== "Available") &&
            `${region.name} ${region.providerId} ${region.id}`
              .toLowerCase()
              .includes(search.trim().toLowerCase()),
        )
        .sort((a, b) => {
          const av = sort.field === "status" ? regionStatus(a) : a[sort.field],
            bv = sort.field === "status" ? regionStatus(b) : b[sort.field];
          if (av === null || bv === null)
            return av === bv ? a.id.localeCompare(b.id) : av === null ? 1 : -1;
          const result =
            sort.field === "pbfModifiedAt" || sort.field === "downloadedAt"
              ? Date.parse(String(av)) - Date.parse(String(bv))
              : typeof av === "number" && typeof bv === "number"
                ? av - bv
                : String(av).localeCompare(String(bv), undefined, { sensitivity: "base" });
          return result * (sort.direction === "asc" ? 1 : -1) || a.id.localeCompare(b.id);
        }),
    [regions, showAll, search, sort],
  );
  const pages = Math.ceil(rows.length / 25),
    currentPage = Math.max(1, Math.min(page, Math.max(1, pages))),
    visible = rows.slice((currentPage - 1) * 25, currentPage * 25);
  const slots = 25;
  const pool = useRef<OSMRegion[]>([]);
  if (visible.length) pool.current = visible.concat(pool.current.slice(visible.length));
  const samples = useExtents(regions, table);
  useNameColumnWidth(table);
  useEffect(() => {
    if (page !== currentPage) setPage(currentPage);
  }, [page, currentPage]);
  useLayoutEffect(() => {
    if (scroll.current) scroll.current.scrollTop = 0;
  }, [search, showAll, sort, currentPage]);
  const enabled = active && !query.isError && !command.isPending;
  const showBoundaries = (region: OSMRegion) => {
    if (!enabled) return;
    dismissActivePortalTooltip();
    lastSelected.current = region.id;
    setSelected(region);
  };
  const returnFocus = () => {
    if (!activeRef.current) return;
    const trigger = [
      ...(table.current?.querySelectorAll<HTMLButtonElement>("[data-region-boundary-trigger]") ??
        []),
    ].find(
      (button) => button.dataset.regionId === lastSelected.current && !button.closest("[hidden]"),
    );
    (trigger ?? heading.current)?.focus({ preventScroll: true });
  };
  const showProgress = (region: OSMRegion, trigger?: HTMLButtonElement) => {
    if (!enabled || !query.data?.updatesEnabled || !canViewRegionProgress(region)) return;
    dismissActivePortalTooltip();
    lastSelected.current = region.id;
    progressFocus.current =
      trigger ??
      [...(table.current?.querySelectorAll<HTMLTableRowElement>("tr[data-region-id]") ?? [])]
        .find((row) => row.dataset.regionId === region.id)
        ?.querySelector<HTMLButtonElement>(".workout-action-trigger") ??
      null;
    setProgressId(region.update!.id);
  };
  const returnProgressFocus = () => {
    if (!activeRef.current) return;
    if (progressFocus.current?.isConnected && !progressFocus.current.closest("[hidden]"))
      progressFocus.current.focus({ preventScroll: true });
    else returnFocus();
  };
  const job = query.data?.refresh,
    busy = catalogPending(job);
  const refreshing = refresh.isPending || busy;
  const refreshTotal = job?.progress.total || regions.length;
  const refreshHelp = refreshing
    ? `${busy ? (job?.progress.completed ?? 0) : 0} of ${refreshTotal} regions cataloged`
    : job?.state === "partially_succeeded"
      ? `${job.progress.failed ?? 0} metadata lookups unavailable — cached values retained`
      : "Refresh configured provider catalogs";
  return (
    <section
      className="workouts-section osm-regions-section"
      aria-labelledby={headingId}
      data-paging-region
    >
      <div className="section-line">
        <h2 ref={heading} id={headingId} tabIndex={-1}>
          Regions
        </h2>
        <Tooltip
          content={refreshHelp}
          portal
          focusable={false}
          keyboardFocusOnly
          dismissOnPointerDown
          dismissOnPointerLeave
        >
          {refreshing ? (
            <span className="text-action osm-regions-refreshing" role="status">
              Refreshing catalogs
            </span>
          ) : (
            <button
              type="button"
              className="text-action workout-clear-filters"
              disabled={!enabled || !query.data?.refreshEnabled || refresh.isPending || busy}
              aria-busy={refresh.isPending || busy}
              onClick={() => {
                if (submitLock.current) return;
                submitLock.current = true;
                refresh.mutate();
              }}
            >
              Refresh catalogs
            </button>
          )}
        </Tooltip>
      </div>
      <div className="osm-regions-search">
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
        <label className="visually-hidden" htmlFor="admin-region-search">
          Search regions
        </label>
        <input
          id="admin-region-search"
          type="search"
          data-unfocused-shortcuts
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            setPage(1);
          }}
          placeholder="Search regions"
        />
      </div>
      <div className="osm-regions-viewport">
        <DialogTableViewport
          stickyHeader
          scrollRef={scroll}
          className="workout-table-wrap osm-regions-table-wrap"
          rowSlots={slots}
          rowClassName="workout-row-measure"
          rowSample={<span className="workout-row-sample">Sample region</span>}
          busy={query.isFetching}
          message={
            query.isPending
              ? "Loading regions..."
              : !query.data && query.isError
                ? "Regions are unavailable."
                : !rows.length
                  ? "No regions match this search."
                  : undefined
          }
        >
          <table ref={table} className="workout-table osm-regions-table">
            <colgroup>
              {[26.6, 14, 14, 14.4, 11, 15].map((width, i) => (
                <col
                  key={i}
                  style={{
                    width:
                      i === 0
                        ? `var(--osm-regions-name-width, ${(width / 107) * 94}%)`
                        : `${(width / 107) * 94}%`,
                  }}
                />
              ))}
              <col style={{ width: "9rem" }} />
              <col />
              <col className="workout-actions-col" />
            </colgroup>
            <thead>
              <tr>
                {REGION_COLUMNS.map((column) => (
                  <th
                    key={column.field}
                    scope="col"
                    aria-sort={
                      column.initial
                        ? sort.field === column.field
                          ? sort.direction === "asc"
                            ? "ascending"
                            : "descending"
                          : "none"
                        : undefined
                    }
                  >
                    {column.initial ? (
                      <button
                        type="button"
                        onClick={() => {
                          setSort((old) => ({
                            field: column.field as RegionSort,
                            direction:
                              old.field === column.field
                                ? old.direction === "asc"
                                  ? "desc"
                                  : "asc"
                                : column.initial!,
                          }));
                          setPage(1);
                        }}
                      >
                        <span className="column-label">{column.label}</span>
                        <span className="sort-indicator" aria-hidden="true">
                          {sort.field === column.field ? (
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
                    ) : (
                      <span className="osm-regions-heading-label">{column.label}</span>
                    )}
                  </th>
                ))}
                <th className="workout-actions-heading" scope="col">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {visible.length > 0 &&
                pool.current.map((region, index) => (
                  <tr
                    key={index}
                    hidden={index >= visible.length}
                    inert={index >= visible.length}
                    className={enabled ? "osm-regions-row" : undefined}
                    data-region-id={region.id}
                    onClick={(event) => {
                      if (
                        event.defaultPrevented ||
                        (event.target instanceof Element &&
                          event.target.closest("button,a,[role='menuitem']"))
                      )
                        return;
                      showBoundaries(region);
                    }}
                  >
                    <td className="table-action-cell">
                      <button
                        type="button"
                        className="table-cell-link"
                        data-region-boundary-trigger
                        data-region-id={region.id}
                        disabled={!enabled}
                        aria-haspopup="dialog"
                        aria-description="Show boundaries"
                        onClick={() => showBoundaries(region)}
                      >
                        <Tooltip
                          content={region.id}
                          portal
                          focusable={false}
                          dismissOnPointerDown
                          className="osm-regions-name-tooltip"
                        >
                          <span className="osm-regions-name-value">{region.name}</span>
                        </Tooltip>
                      </button>
                    </td>
                    <td>
                      <Value region={region} field="pbfModifiedAt" extent={samples.pbfModifiedAt} />
                    </td>
                    <td>
                      <Value region={region} field="pbfBytes" extent={samples.pbfBytes} />
                    </td>
                    <td>
                      <Value region={region} field="downloadedAt" extent={samples.downloadedAt} />
                    </td>
                    <td>
                      <Value region={region} field="generation" extent={samples.generation} />
                    </td>
                    <td>
                      <Value region={region} field="databaseBytes" extent={samples.databaseBytes} />
                    </td>
                    <td>
                      <Value region={region} field="workoutCount" extent={samples.workoutCount} />
                    </td>
                    <td
                      className={
                        query.data?.updatesEnabled && canOpenStatusProgress(region)
                          ? "table-action-cell"
                          : undefined
                      }
                    >
                      {query.data?.updatesEnabled && canOpenStatusProgress(region) ? (
                        <button
                          type="button"
                          className={`table-cell-link osm-regions-status osm-regions-status--${regionStatus(region).toLowerCase()}`}
                          disabled={!enabled}
                          aria-haspopup="dialog"
                          aria-label={`View job progress for ${region.name}: ${regionStatus(region)}`}
                          onClick={(event) => showProgress(region, event.currentTarget)}
                        >
                          {regionStatus(region)}
                        </button>
                      ) : (
                        <span
                          className={`osm-regions-status osm-regions-status--${regionStatus(region).toLowerCase()}`}
                        >
                          {regionStatus(region)}
                        </span>
                      )}
                    </td>
                    <td className="workout-actions-cell osm-regions-actions-cell">
                      <RegionActions
                        key={region.id}
                        region={region}
                        enabled={enabled}
                        onBoundaries={() => showBoundaries(region)}
                        updatesEnabled={Boolean(query.data?.updatesEnabled)}
                        onProgress={() => showProgress(region)}
                        onCommand={(action) => {
                          if (commandLock.current) return;
                          if (action === "delete") {
                            deleteFocus.current =
                              [
                                ...(table.current?.querySelectorAll<HTMLTableRowElement>(
                                  "tr[data-region-id]",
                                ) ?? []),
                              ]
                                .find((row) => row.dataset.regionId === region.id)
                                ?.querySelector<HTMLButtonElement>(".workout-action-trigger") ??
                              null;
                            command.reset();
                            setDeleteTarget(region);
                            return;
                          }
                          commandLock.current = true;
                          commandFocus.current =
                            [
                              ...(table.current?.querySelectorAll<HTMLTableRowElement>(
                                "tr[data-region-id]",
                              ) ?? []),
                            ]
                              .find((row) => row.dataset.regionId === region.id)
                              ?.querySelector<HTMLButtonElement>(".workout-action-trigger") ?? null;
                          command.mutate({ region, action });
                        }}
                      />
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </DialogTableViewport>
      </div>
      <footer className="osm-regions-footer">
        <label className="osm-regions-show-all">
          <input
            className="inline-checkbox osm-regions-show-all-box"
            type="checkbox"
            aria-keyshortcuts="A"
            checked={showAll}
            onChange={(event) => {
              setShowAll(event.target.checked);
              setPage(1);
            }}
          />
          Show all regions
        </label>
        <PageNavigation
          value={{ page: currentPage, totalPages: pages }}
          reserveTotalPages={Math.ceil(regions.length / 25)}
          label="Region pages"
          busy={query.isPending}
          unavailable={!query.data}
          onPage={setPage}
        />
      </footer>
      {(refresh.isError || query.isError) && (
        <div className="error-summary" role="alert">
          {refresh.isError
            ? "Catalog refresh could not be started. Try again."
            : "Regions could not be refreshed."}
          {query.isError && (
            <button className="text-action" onClick={() => void query.refetch()}>
              Retry
            </button>
          )}
        </div>
      )}
      {command.isError && (
        <div className="error-summary" role="alert">
          The region update command could not be accepted. Refresh the region and try again.
        </div>
      )}
      <OSMRegionProgressDialog
        jobId={progressId}
        onClose={() => setProgressId(undefined)}
        onReturnFocus={returnProgressFocus}
      />
      <Dialog.Root
        open={Boolean(deleteTarget) && active}
        onOpenChange={(open) => {
          if (!open && !command.isPending) setDeleteTarget(undefined);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content compact-dialog deletion-dialog single-deletion-dialog"
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              deleteCancel.current?.focus();
            }}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (activeRef.current)
                (deleteFocus.current?.isConnected ? deleteFocus.current : heading.current)?.focus();
            }}
            onEscapeKeyDown={(event) => {
              if (command.isPending) event.preventDefault();
            }}
            onPointerDownOutside={(event) => {
              if (command.isPending) event.preventDefault();
            }}
          >
            <div className="dialog-heading">
              <Dialog.Title>Delete region</Dialog.Title>
            </div>
            <Dialog.Description className="deletion-confirmation-message">
              Delete <strong>{deleteTarget?.name}</strong> ({deleteTarget?.id})?
              <br />
              This will permanently delete processed map data as well as data from any unfinished
              processing job to reclaim database storage; this region will then become available for
              download again.
              <br />
              Workout routes in this region that have already updated their coverage will continue
              to display unaffected.
            </Dialog.Description>
            {command.isError && (
              <p className="error-summary" role="alert">
                Region deletion could not be started. Refresh the region and try again.
              </p>
            )}
            <div className="dialog-actions">
              <Dialog.Close ref={deleteCancel} className="secondary" disabled={command.isPending}>
                Cancel
              </Dialog.Close>
              <button
                type="button"
                className="danger-action"
                disabled={command.isPending}
                aria-busy={command.isPending}
                onClick={() => {
                  const region = regions.find((region) => region.id === deleteTarget?.id);
                  if (!region || commandLock.current || updateActive(region.update)) return;
                  commandLock.current = true;
                  command.mutate({ region, action: "delete" });
                }}
              >
                Delete
              </button>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <OSMRegionBoundaryDialog
        region={selected}
        baseMaps={baseMaps}
        onClose={() => setSelected(undefined)}
        onReturnFocus={returnFocus}
      />
    </section>
  );
}
const EMPTY: OSMRegion[] = [];
