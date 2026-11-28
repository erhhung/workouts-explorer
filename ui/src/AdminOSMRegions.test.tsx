import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  AdminOSMRegions,
  type OSMCatalogs,
  type OSMRegion,
  type CatalogRefresh,
} from "./AdminOSMRegions";
import { boundaryBounds } from "./OSMRegionBoundaryDialog";
import { regionDate, regionExactTime } from "./osmRegions";
import type { RegionUpdate } from "./osmRegions";
import type { BaseMapsConfig, Session } from "./api";
import { RetainedTab } from "./RetainedTab";

const maps = vi.hoisted(() => ({
  instances: [] as Array<{
    options: { container: HTMLElement; style: string };
    canvas: HTMLCanvasElement;
    layers: { id: string; type: string; paint?: Record<string, unknown> }[];
    sources: Map<string, { data: unknown; setData: ReturnType<typeof vi.fn> }>;
    fitBounds: ReturnType<typeof vi.fn>;
    remove: ReturnType<typeof vi.fn>;
    setStyle: ReturnType<typeof vi.fn>;
  }>,
}));
vi.mock("maplibre-gl", () => ({
  default: {
    Map: class {
      options: { container: HTMLElement; style: string };
      canvas = document.createElement("canvas");
      layers = [
        { id: "ground", type: "fill" },
        { id: "labels", type: "symbol" },
      ];
      sources = new Map<string, { data: unknown; setData: ReturnType<typeof vi.fn> }>();
      handlers = new Map<string, () => void>();
      constructor(options: { container: HTMLElement; style: string }) {
        this.options = options;
        this.canvas.className = "maplibregl-canvas";
        options.container.append(this.canvas);
        maps.instances.push(this);
      }
      on = vi.fn((type: string, handler: () => void) => {
        this.handlers.set(type, handler);
        if (type === "style.load") queueMicrotask(handler);
        return this;
      });
      getStyle = () => ({ layers: this.layers });
      getSource = (id: string) => this.sources.get(id);
      addSource = (id: string, spec: { data: unknown }) => {
        const source = {
          data: spec.data,
          setData: vi.fn((data: unknown) => {
            source.data = data;
          }),
        };
        this.sources.set(id, source);
      };
      addLayer = (layer: { id: string; type: string }, before?: string) => {
        const index = this.layers.findIndex((item) => item.id === before);
        this.layers.splice(index < 0 ? this.layers.length : index, 0, layer);
      };
      fitBounds = vi.fn();
      resize = vi.fn();
      stop = vi.fn();
      setStyle = vi.fn(() => {
        this.sources.clear();
        this.layers = [
          { id: "ground", type: "fill" },
          { id: "labels", type: "symbol" },
        ];
        queueMicrotask(() => this.handlers.get("style.load")?.());
      });
      remove = vi.fn(() => {
        this.canvas.remove();
        this.handlers.clear();
      });
    },
  },
}));

const session: Session = {
  id: "A".repeat(32),
  csrfToken: "admin-catalog-csrf",
  expiresAt: "2099-01-01T00:00:00Z",
  identity: {
    id: "B".repeat(32),
    username: "admin",
    fullName: "Administrator",
    role: "administrator",
  },
};
const baseMaps: BaseMapsConfig = {
  families: [
    {
      id: "streets",
      label: "Streets",
      styles: {
        light: "https://map.test/streets-light.json",
        dark: "https://map.test/streets-dark.json",
      },
      attribution: { text: "MapTiler", links: [] },
      resourceOrigins: ["https://map.test"],
    },
  ],
  fallbackFamilyId: "streets",
  workoutTypeMappings: [],
};
const row: OSMRegion = {
  id: "geofabrik:norcal",
  provider: "geofabrik",
  providerId: "us/california/norcal",
  parentId: "us/california",
  name: "Northern California",
  workoutCount: 0,
  coverageRouteCount: 0,
  available: true,
  areaM2: 100,
  pbfBytes: 1024 ** 3,
  pbfModifiedAt: "2026-11-18T00:00:00Z",
  metadataCheckedAt: "2026-11-18T00:00:00Z",
  metadataAttemptedAt: "2026-11-18T00:00:00Z",
  metadataErrorCode: null,
  generation: 62,
  downloadedAt: "2026-11-01T12:00:00Z",
  downloadedBytes: 900 * 1024 ** 2,
  sourceTimestamp: "2026-11-01T00:00:00Z",
  promotedAt: "2026-11-02T12:00:00Z",
  databaseBytes: 2 * 1024 ** 3,
  databaseMeasuredAt: "2026-11-18T00:00:00Z",
  processedDatabaseBytes: 2 * 1024 ** 3,
};
const available: OSMRegion = {
  ...row,
  id: "geofabrik:us/district-of-columbia",
  providerId: "us/district-of-columbia",
  name: "US / District of Columbia",
  workoutCount: 0,
  coverageRouteCount: 0,
  generation: null,
  pbfBytes: null,
  pbfModifiedAt: null,
  downloadedAt: null,
  downloadedBytes: null,
  promotedAt: null,
  databaseBytes: null,
};
const initial: OSMCatalogs = {
  refreshEnabled: true,
  providers: [
    {
      provider: "geofabrik",
      state: "succeeded",
      refreshedAt: "2026-11-18T00:00:00Z",
      attemptedAt: "2026-11-18T00:00:00Z",
      regions: 2,
      metadataFailed: 0,
      failureCode: null,
    },
  ],
  regions: [available, row],
  refresh: null,
};
const job: CatalogRefresh = {
  id: "C".repeat(32),
  state: "running",
  providers: ["geofabrik"],
  createdAt: "2026-11-18T00:00:00Z",
  startedAt: "2026-11-18T00:00:00Z",
  finishedAt: null,
  failureCode: null,
  progress: { phase: "metadata", provider: "geofabrik", total: 12, completed: 3, failed: 1 },
};
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const geometry = {
  type: "Polygon",
  coordinates: [
    [
      [-124, 37],
      [-122, 37],
      [-122, 40],
      [-124, 37],
    ],
  ],
};

function setup(
  value: OSMCatalogs = initial,
  post?: (init: RequestInit) => Response | Promise<Response>,
  showAllForTest = true,
  currentSession = session,
) {
  let snapshot = structuredClone(value),
    boundaryFailure = false;
  let updateDetail: RegionUpdate | undefined;
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (path === "/api/admin/osm/regions") return Promise.resolve(json(snapshot));
    if (path.startsWith("/api/admin/osm/region-boundary?"))
      return Promise.resolve(
        boundaryFailure
          ? json({ detail: "private-boundary-error" }, 503)
          : json({ id: new URL(path, "https://app.test").searchParams.get("regionId"), geometry }),
      );
    if (path === "/api/admin/osm/catalog-refreshes" && post)
      return Promise.resolve(post(init ?? {}));
    if (path.startsWith("/api/admin/osm/region-updates") && post && init?.method === "POST")
      return Promise.resolve(post(init));
    if (path.startsWith("/api/admin/osm/region-updates/") && updateDetail)
      return Promise.resolve(json(updateDetail));
    throw Error(`Unexpected API ${path}`);
  });
  vi.stubGlobal("fetch", fetch);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } },
  });
  client.setQueryData(["admin-session"], currentSession);
  const tree = (active: boolean) => (
    <QueryClientProvider client={client}>
      <RetainedTab id="/admin/osm/regions" active={active}>
        <AdminOSMRegions
          session={currentSession}
          baseMaps={baseMaps}
          polling={30}
          headingId="region-heading"
        />
      </RetainedTab>
    </QueryClientProvider>
  );
  const view = render(tree(true));
  if (showAllForTest) fireEvent.click(screen.getByRole("checkbox", { name: "Show all regions" }));
  return {
    client,
    fetch,
    unmount: view.unmount,
    setUpdate: (value: RegionUpdate) => {
      updateDetail = value;
    },
    set: (value: OSMCatalogs) => {
      snapshot = structuredClone(value);
    },
    active: (value: boolean) => view.rerender(tree(value)),
    failBoundary: (value: boolean) => {
      boundaryFailure = value;
    },
  };
}
afterEach(() => {
  cleanup();
  sessionStorage.removeItem("workouts:admin:region-updates-disabled-notice");
  maps.instances.length = 0;
  document.documentElement.dataset.theme = "dark";
  vi.unstubAllGlobals();
});
const tableRows = () => screen.getByRole("table").querySelectorAll("tbody tr:not([hidden])");

describe("administrator OSM Regions", () => {
  test("disabled notice is above the heading and stays dismissed for this login, including reload", async () => {
    const disabled = { ...initial, updatesEnabled: false };
    const user = userEvent.setup();
    const first = setup(disabled);
    const message = await screen.findByText(
      "Region updates have been disabled in this installation.",
    );
    const notice = message.closest("article")!;
    expect(notice).toHaveClass("sync-notification", "sync-notification--warning");
    expect(
      notice.compareDocumentPosition(screen.getByRole("heading", { name: "Regions" })) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    await user.click(
      screen.getByRole("button", { name: "Dismiss region updates disabled notice" }),
    );
    expect(
      screen.queryByText("Region updates have been disabled in this installation."),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Regions" })).toHaveFocus();
    first.active(false);
    first.active(true);
    expect(
      screen.queryByText("Region updates have been disabled in this installation."),
    ).not.toBeInTheDocument();
    first.unmount();
    const reloaded = setup(disabled);
    await screen.findByText(row.name);
    expect(
      screen.queryByText("Region updates have been disabled in this installation."),
    ).not.toBeInTheDocument();
    reloaded.unmount();
    setup(disabled, undefined, true, { ...session, id: "D".repeat(32) });
    await screen.findByText("Region updates have been disabled in this installation.");
  });

  test("notice returns when updates are enabled and disabled again in the same login", async () => {
    const disabled = { ...initial, updatesEnabled: false };
    const { client, set } = setup(disabled);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "Dismiss region updates disabled notice" }),
    );
    set({ ...initial, updatesEnabled: true });
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    await waitFor(() =>
      expect(sessionStorage.getItem("workouts:admin:region-updates-disabled-notice")).toBeNull(),
    );
    set(disabled);
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    await screen.findByText("Region updates have been disabled in this installation.");
  });

  test("A toggles Show all regions without intercepting search, modifiers, menus or dialogs", async () => {
    const user = userEvent.setup();
    setup(initial, undefined, false);
    const all = await screen.findByRole("checkbox", { name: "Show all regions" });
    await screen.findByRole("button", { name: row.name });
    expect(all).toHaveAttribute("aria-keyshortcuts", "A");
    await user.keyboard("a");
    await waitFor(() => expect(all).toBeChecked());
    fireEvent.keyDown(document, { key: "a", repeat: true });
    expect(all).toBeChecked();
    await user.keyboard("A");
    expect(all).not.toBeChecked();
    const search = screen.getByRole("searchbox");
    await user.click(search);
    await user.keyboard("a");
    expect(search).toHaveValue("a");
    expect(all).not.toBeChecked();
    await user.clear(search);
    all.focus();
    await user.keyboard("{Control>}a{/Control}{Meta>}a{/Meta}");
    expect(all).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    await user.keyboard("a");
    expect(all).not.toBeChecked();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: row.name }));
    await screen.findByRole("dialog");
    await user.keyboard("a");
    expect(all).not.toBeChecked();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await user.keyboard("a");
    expect(all).toBeChecked();
  });
  test.each(["download", "validate/finalize", "startup"])(
    "historical %s failures omit the redundant task prefix",
    async (task) => {
      const user = userEvent.setup(),
        update = updateFixture("failed");
      update.currentOrder = task === "startup" ? 0 : task === "download" ? 2 : 57;
      if (update.currentOrder) update.steps![update.currentOrder - 1].name = task;
      const detail =
        task === "download"
          ? "Failed to download https://download.geofabrik.de/monaco-latest.osm.pbf | unexpected HTTP status 503"
          : "Updater container exited with code 1. Review the updater pod logs for this job.";
      update.error = `${task} failed: ${detail.replace("Review the updater pod logs for this job.", "Review the updater logs for this run.")}`;
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Failed", update }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Failed`,
        }),
      );
      const alert = within(await screen.findByRole("dialog")).getByRole("alert");
      expect(alert).toHaveTextContent(detail);
      expect(alert).not.toHaveTextContent(`${task} failed:`);
      if (task === "download")
        expect(
          within(alert).getByRole("button", { name: "Copy download URL" }),
        ).toBeInTheDocument();
    },
  );
  test.each([false, true])(
    "download URL copies on activation and reports clipboard failure=%s",
    async (failure) => {
      const user = userEvent.setup(),
        writeText = vi.spyOn(navigator.clipboard, "writeText");
      if (failure) writeText.mockRejectedValue(new Error("clipboard denied"));
      else writeText.mockResolvedValue();
      const url =
        "https://download.geofabrik.de/europe/norway/svalbard-janmayen/svalbard-janmayen-latest.osm.pbf";
      const error = `Failed to download ${url} | unexpected HTTP status 503`;
      const update = { ...updateFixture("failed"), currentOrder: 2, error };
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Failed", update }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Failed`,
        }),
      );
      const dialog = await screen.findByRole("dialog"),
        alert = within(dialog).getByRole("alert"),
        copy = within(alert).getByRole("button", { name: "Copy download URL" });
      expect(alert).toHaveTextContent(error);
      expect(copy).toHaveTextContent(url);
      expect(copy).not.toHaveAttribute("href");
      const before = location.href;
      copy.focus();
      await user.keyboard("{Enter}");
      expect(writeText).toHaveBeenCalledExactlyOnceWith(url);
      expect(location.href).toBe(before);
      await waitFor(() =>
        expect(alert.querySelector('[aria-live="polite"]')).toHaveTextContent(
          failure ? "Could not copy" : "Download URL copied",
        ),
      );
      await act(() =>
        view.client.setQueryData(["admin-osm-region-update", update.id], {
          ...update,
          error: "A different task failed.",
        }),
      );
      await waitFor(() =>
        expect(
          within(alert).queryByRole("button", { name: "Copy download URL" }),
        ).not.toBeInTheDocument(),
      );
      expect(alert).not.toHaveTextContent("Download URL copied");
    },
  );
  test.each([true, false])(
    "storage monitoring indicator shares the task row and shows uppercase state %s",
    async (enabled) => {
      const user = userEvent.setup(),
        update = { ...updateFixture(), monitorDatabaseStorage: enabled };
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Processing", update }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Processing`,
        }),
      );
      const dialog = await screen.findByRole("dialog"),
        status = within(dialog).getByRole("status"),
        indicator = dialog.querySelector(".osm-update-storage-policy")!;
      expect(status.parentElement).toBe(indicator.parentElement);
      const rows = within(indicator as HTMLElement).getAllByRole("row");
      expect(rows).toHaveLength(2);
      expect(within(rows[0]).getByRole("rowheader")).toHaveTextContent(
        "Database storage monitoring",
      );
      expect(within(rows[1]).getByRole("rowheader")).toHaveTextContent("Database storage used");
      expect(indicator).not.toHaveTextContent(":");
      expect(
        within(indicator as HTMLElement).getByText(enabled ? "ENABLED" : "DISABLED"),
      ).toHaveClass("eyebrow", `osm-update-policy-value--${enabled ? "enabled" : "disabled"}`);
    },
  );
  const updateFixture = (state: RegionUpdate["state"] = "processing"): RegionUpdate => ({
    id: "E".repeat(32),
    regionId: available.id,
    state,
    phase: "Processing",
    operation: "download",
    buildId: 77,
    resumable: true,
    promoted: false,
    currentOrder: 20,
    createdAt: "2026-11-19T18:00:00Z",
    startedAt: "2026-11-19T18:00:00Z",
    finishedAt: null,
    error: null,
    steps: Array.from({ length: 57 }, (_, index) => ({
      order: index + 1,
      name: index === 19 ? "clip-replacements" : `task-${index + 1}`,
      repeated: index === 19,
      state: index < 19 ? "completed" : index === 19 ? "running" : "pending",
      batch: index === 19 ? 25 : 0,
      batches: index === 19 ? 95 : 0,
      startedAt: index === 0 ? "2026-11-18T02:30:00Z" : index < 20 ? "2026-11-20T02:30:00Z" : null,
      finishedAt: index === 0 ? "2026-11-20T02:32:40Z" : index < 19 ? "2026-11-20T02:31:00Z" : null,
      elapsedTotal: index === 0 ? 160 : index < 19 ? 60 : null,
    })),
  });
  test("progress storage usage follows measured build snapshots and becomes normal only after successful promotion", async () => {
    const user = userEvent.setup(),
      update = { ...updateFixture(), regionId: row.id, monitorDatabaseStorage: false };
    const running: OSMRegion = {
      ...row,
      status: "Processing",
      buildId: 62,
      update,
      buildDatabaseId: 77,
      buildDatabaseBytes: Math.round(245.6 * 1024 ** 2),
      buildDatabaseMeasuredAt: "2026-11-24T01:00:00Z",
    };
    const view = setup({ ...initial, updatesEnabled: true, regions: [running] });
    view.setUpdate(update);
    await user.click(
      await screen.findByRole("button", { name: `View job progress for ${row.name}: Processing` }),
    );
    const dialog = await screen.findByRole("dialog"),
      summary = within(dialog).getByRole("table", { name: "Database storage summary" });
    expect(within(summary).getByText("245.6 MiB")).toHaveClass("osm-update-storage-used--pending");
    view.set({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...running, buildDatabaseBytes: 512 * 1024 ** 2 }],
    });
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    expect(await within(summary).findByText("512.0 MiB")).toHaveClass(
      "osm-update-storage-used--pending",
    );
    const promoted = { ...update, promoted: true };
    view.setUpdate(promoted);
    view.set({
      ...initial,
      updatesEnabled: true,
      regions: [
        { ...running, buildId: 77, generation: 19, databaseBytes: 3 * 1024 ** 3, update: promoted },
      ],
    });
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
      await view.client.invalidateQueries({ queryKey: ["admin-osm-region-update", update.id] });
    });
    expect(await within(summary).findByText("3.0 GiB")).toHaveClass(
      "osm-update-storage-used--pending",
    );
    view.setUpdate({ ...promoted, state: "succeeded" });
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: ["admin-osm-region-update", update.id] });
    });
    await waitFor(() =>
      expect(within(summary).getByText("3.0 GiB")).not.toHaveClass(
        "osm-update-storage-used--pending",
      ),
    );
    const used = within(summary).getByText("3.0 GiB");
    expect(used.tagName).toBe("STRONG");
    expect(
      document.getElementById(used.closest(".tooltip-trigger")!.getAttribute("aria-describedby")!),
    ).toHaveTextContent("3,221,225,472 bytes");
    view.set({
      ...initial,
      updatesEnabled: true,
      regions: [
        {
          ...running,
          buildId: 99,
          databaseBytes: 8 * 1024 ** 3,
          update: { ...promoted, id: "F".repeat(32) },
        },
      ],
    });
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    await waitFor(() => expect(within(summary).queryByText("3.0 GiB")).not.toBeInTheDocument());
    expect(within(summary).getByText("n/a")).toBeInTheDocument();
  });

  test.each(["first build", "different build", "unmeasured"])(
    "progress storage avoids unrelated or unknown allocation: %s",
    async (mode) => {
      const user = userEvent.setup(),
        update = { ...updateFixture(), monitorDatabaseStorage: false };
      const region: OSMRegion = {
        ...available,
        status: "Processing",
        update,
        buildDatabaseId: 77,
        buildDatabaseBytes: Math.round(245.6 * 1024 ** 2),
        buildDatabaseMeasuredAt: "2026-11-24T01:00:00Z",
      };
      if (mode === "different build") region.buildDatabaseId = 78;
      if (mode === "unmeasured") region.buildDatabaseMeasuredAt = null;
      const view = setup({ ...initial, updatesEnabled: true, regions: [region] });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Processing`,
        }),
      );
      const summary = within(await screen.findByRole("dialog")).getByRole("table", {
        name: "Database storage summary",
      });
      if (mode === "first build")
        expect(within(summary).getByText("245.6 MiB")).toHaveClass(
          "osm-update-storage-used--pending",
        );
      else expect(within(summary).getByText("n/a")).toBeInTheDocument();
    },
  );
  test.each([true, false])(
    "shows measured retained build bytes with independent tooltip targets (active generation: %s)",
    async (active) => {
      const bytes = Math.round(245.6 * 1024 ** 2);
      setup({
        ...initial,
        regions: [
          {
            ...row,
            generation: active ? 18 : null,
            databaseBytes: active ? 5 * 1024 ** 3 : null,
            status: "Processing",
            update: { ...updateFixture(), regionId: row.id },
            buildDatabaseId: 77,
            buildDatabaseBytes: bytes,
            buildDatabaseMeasuredAt: "2026-11-24T01:00:00Z",
          },
        ],
      });
      const build = await screen.findByText("245.6 MiB");
      expect(build).toHaveClass("osm-regions-build-usage");
      const buildHelp = document.getElementById(
        build.closest(".tooltip-trigger")!.getAttribute("aria-describedby")!,
      )!;
      expect(buildHelp).toHaveTextContent("257,530,266 bytes");
      const cell = build.closest("td")!;
      if (active) {
        expect(cell).toHaveTextContent("5.0 GiB + 245.6 MiB");
        const current = within(cell).getByText("5.0 GiB");
        expect(current).not.toHaveClass("osm-regions-build-usage");
        expect(
          document.getElementById(
            current.closest(".tooltip-trigger")!.getAttribute("aria-describedby")!,
          )!,
        ).toHaveTextContent("5,368,709,120 bytes");
      } else {
        expect(cell).toHaveTextContent(/^245.6 MiB$/);
      }
    },
  );

  test.each(["promoted", "different build", "no measurement", "canceled"])(
    "does not show stale temporary usage: %s",
    async (reason) => {
      const region: OSMRegion = {
        ...row,
        status: "Processing",
        update: { ...updateFixture(), regionId: row.id },
        buildDatabaseId: 77,
        buildDatabaseBytes: Math.round(245.6 * 1024 ** 2),
        buildDatabaseMeasuredAt: "2026-11-24T01:00:00Z",
      };
      if (reason === "promoted") region.update!.promoted = true;
      if (reason === "different build") region.buildDatabaseId = 76;
      if (reason === "no measurement") region.buildDatabaseMeasuredAt = null;
      if (reason === "canceled") region.status = "Canceled";
      setup({ ...initial, regions: [region] });
      await screen.findByText(region.name);
      expect(screen.queryByText("245.6 MiB")).not.toBeInTheDocument();
    },
  );

  test("download admits with CSRF and updates the same row to Waiting", async () => {
    const user = userEvent.setup(),
      waiting = {
        ...updateFixture("waiting"),
        phase: "Downloading" as const,
        currentOrder: 0,
        startedAt: null,
        resumable: false,
        buildId: null,
      };
    const snapshot = { ...initial, updatesEnabled: true, regions: [available] };
    const view = setup(snapshot, () => {
      view.set({ ...snapshot, regions: [{ ...available, status: "Waiting", update: waiting }] });
      return json(waiting, 202);
    });
    const row = (await screen.findByText(available.name)).closest("tr");
    await user.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Download region" }));
    await screen.findByText("Waiting", { selector: ".osm-regions-status" });
    expect(screen.getByText(available.name).closest("tr")).toBe(row);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const call = view.fetch.mock.calls.find(([path]) => path === "/api/admin/osm/region-updates");
    expect(call?.[1]?.body).toBe(JSON.stringify({ regionId: available.id, action: "download" }));
    expect(new Headers(call?.[1]?.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
    await user.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
    expect(screen.getByRole("menuitem", { name: "Cancel download" })).not.toHaveAttribute(
      "data-disabled",
    );
    expect(screen.getByRole("menuitem", { name: "View job progress" })).not.toHaveAttribute(
      "data-disabled",
    );
  });
  test("progress presents the ordered inventory, repeated batch and reserved icon positions", async () => {
    const user = userEvent.setup(),
      update = updateFixture(),
      view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Processing", update }],
      });
    view.setUpdate(update);
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    expect(screen.getByRole("menuitem", { name: "Pause processing" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Cancel processing" })).toBeInTheDocument();
    const progressAction = screen.getByRole("menuitem", { name: "View job progress" });
    expect(progressAction.previousElementSibling).toHaveTextContent("View boundaries");
    expect(progressAction.nextElementSibling).toHaveAttribute("role", "separator");
    expect(progressAction.nextElementSibling?.nextElementSibling).toHaveTextContent(
      "Pause processing",
    );
    await user.click(progressAction);
    const dialog = await screen.findByRole("dialog", { name: `Processing ${available.id}` });
    expect(
      await within(dialog).findByText("20 of 57 \u2014 clip-replacements \u2014 batch 25 of 95"),
    ).toBeInTheDocument();
    expect(
      within(dialog)
        .getAllByRole("columnheader")
        .map((el) => el.textContent),
    ).toEqual(["Step", "Task", "Batch", "Started", "Finished"]);
    expect(within(dialog).getByRole("region", { name: "Region update steps" })).toHaveFocus();
    expect(within(dialog).queryByRole("button", { name: "Task" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("navigation")).not.toBeInTheDocument();
    const rows = within(dialog)
      .getByRole("table", { name: "Region update tasks" })
      .querySelectorAll("tbody tr");
    expect(rows).toHaveLength(57);
    expect(rows[19]).toHaveTextContent("25 of 95");
    expect(rows[20].querySelector(".osm-update-icon-slot")).toBeEmptyDOMElement();
    expect(rows[0].querySelector(".osm-update-icon-slot svg")).not.toBeNull();
    expect(rows[19].querySelector(".coverage-route-status--pending")).not.toBeNull();
    const finishedTime = rows[0].querySelector("td:last-child time")!;
    const finishedTrigger = finishedTime.closest(".tooltip-trigger")!;
    expect(
      document.getElementById(finishedTrigger.getAttribute("aria-describedby")!),
    ).toHaveTextContent("2m 40s total");
    expect(rows[19].querySelector("td:last-child .tooltip-trigger")).toBeNull();
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    const stopped = {
      ...update,
      state: "canceled" as const,
      error: "This update was canceled.",
      steps: update.steps!.map((step) =>
        step.order === 20 ? { ...step, state: "canceled" as const } : step,
      ),
    };
    await act(async () => {
      view.client.setQueryData(["admin-osm-region-update", update.id], stopped);
    });
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("This update was canceled.");
    expect(rows[19].querySelector(".coverage-route-status--unavailable")).not.toBeNull();
  });
  test.each<[number, number, string]>([
    [37, 3, "37 coverage routes | 3 missing additional regions"],
    [37, 0, "37 coverage routes"],
    [40, 3, "40 coverage routes"],
  ])(
    "Workouts help distinguishes missing maps: current=%s missing=%s",
    async (current, missing, expected) => {
      setup({
        ...initial,
        regions: [
          {
            ...row,
            workoutCount: 40,
            coverageRouteCount: current,
            missingRegionRouteCount: missing,
          },
        ],
      });
      const cell = (await screen.findByText(row.name)).closest("tr")!.querySelectorAll("td")[6];
      const trigger = cell.querySelector(".tooltip-trigger")!;
      expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveTextContent(
        expected,
      );
    },
  );
  test("Generation help combines promotion date and full matched job elapsed time", async () => {
    const ready = {
      ...row,
      generation: 1,
      buildId: 65,
      promotedAt: "2026-11-08T12:00:00Z",
      generationElapsedSeconds: 59063,
    };
    setup({ ...initial, regions: [ready] });
    const tableRow = (await screen.findByText(ready.name)).closest("tr")!;
    expect(tableRow.querySelectorAll("td")[4]).toHaveTextContent("1");
    expect(tableRow.querySelectorAll("td")[4].querySelector(".workout-value-lane")).toHaveAttribute(
      "data-width-sample",
    );
    expect(
      tableRow.querySelectorAll("td")[4].querySelector(".osm-regions-generation-value"),
    ).toBeInTheDocument();
    const trigger = tableRow.querySelectorAll("td")[4].querySelector(".tooltip-trigger")!;
    expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveTextContent(
      "November 8, 2026 | 16h 24m 23s total",
    );
  });
  test("Workouts shows the all-account total and latest-generation Coverage progress", async () => {
    const view = setup({
      ...initial,
      regions: [{ ...row, workoutCount: 24, coverageRouteCount: 0 }],
    });
    const tableRow = (await screen.findByText(row.name)).closest("tr")!;
    const cell = tableRow.querySelectorAll("td")[6];
    expect(cell).toHaveTextContent("24");
    expect(cell.querySelector(".osm-regions-generation-value")).toBeInTheDocument();
    const trigger = cell.querySelector(".tooltip-trigger")!;
    expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveTextContent(
      "0 coverage routes",
    );
    await act(async () => {
      view.set({
        ...initial,
        regions: [{ ...row, workoutCount: 24, coverageRouteCount: 7 }],
      });
      await view.client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    expect(cell).toHaveTextContent("24");
    await waitFor(() =>
      expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveTextContent(
        "7 coverage routes",
      ),
    );
  });
  test("completed repeated steps retain their batch counts and the job line shows its total", async () => {
    const user = userEvent.setup();
    const update = {
      ...updateFixture("succeeded"),
      currentOrder: 57,
      elapsedTotal: 5155,
      steps: updateFixture().steps!.map((step) => ({
        ...step,
        state: "completed" as const,
        batch: step.order === 20 ? 12 : step.batch,
        batches: step.order === 20 ? 12 : step.batches,
      })),
    };
    update.steps[56].name = "validate/finalize";
    const view = setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, generation: 1, status: "Ready", update }],
    });
    view.setUpdate(update);
    await user.click(
      await screen.findByRole("button", { name: `View job progress for ${available.name}: Ready` }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("heading", { name: `Processed ${available.id}` }),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("status")).toHaveTextContent(
      "57 of 57 \u2014 validate/finalize \u2014 1h 25m 55s total",
    );
    expect(dialog.querySelector('[data-task-order="20"] td:nth-child(3)')).toHaveTextContent(
      "12 of 12",
    );
    expect(dialog.querySelector('[data-task-order="1"] td:nth-child(3)')).toHaveTextContent("n/a");
  });
  test.each(["pending", "running", "completed", "paused", "failed", "canceled"] as const)(
    "the current task icon matches its %s table row rather than the overall job state",
    async (state) => {
      const user = userEvent.setup(),
        update = updateFixture("processing");
      update.steps![19].state = state;
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Processing", update }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Processing`,
        }),
      );
      const dialog = await screen.findByRole("dialog"),
        current = within(dialog).getByRole("status");
      expect(current.querySelector(".osm-update-icon-slot")!.innerHTML).toBe(
        dialog.querySelector('[data-task-order="20"] .osm-update-icon-slot')!.innerHTML,
      );
    },
  );
  test.each([57, 58])(
    "final task %s stays running through maintenance, then both icons complete together",
    async (count) => {
      const user = userEvent.setup(),
        update = updateFixture("processing");
      if (count === 58) update.steps!.push({ ...update.steps![56], order: 58 });
      update.currentOrder = count;
      update.promoted = true;
      update.steps = update.steps!.map((step) => ({ ...step, state: "completed" }));
      update.steps[count - 1] = {
        ...update.steps[count - 1],
        name: "validate/finalize",
        state: "running",
        finishedAt: null,
      };
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Processing", update }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Processing`,
        }),
      );
      const dialog = await screen.findByRole("dialog"),
        row = dialog.querySelector(`[data-task-order="${count}"]`)!;
      expect(
        within(within(dialog).getByRole("status")).getByLabelText("running"),
      ).toBeInTheDocument();
      expect(within(row as HTMLElement).getByLabelText("running")).toBeInTheDocument();
      expect(row.querySelector("td:last-child time")).toBeNull();
      await act(() =>
        view.client.setQueryData(["admin-osm-region-update", update.id], {
          ...update,
          steps: update.steps!.map((step) => ({
            ...step,
            state: "completed",
            finishedAt: "2026-11-22T10:06:16Z",
          })),
        }),
      );
      expect(
        await within(within(dialog).getByRole("status")).findByLabelText("completed"),
      ).toBeInTheDocument();
      expect(within(row as HTMLElement).getByLabelText("completed")).toBeInTheDocument();
      expect(row.querySelector("td:last-child time")).toHaveAttribute(
        "datetime",
        "2026-11-22T10:06:16Z",
      );
      await act(() =>
        view.client.setQueryData(["admin-osm-region-update", update.id], {
          ...update,
          state: "succeeded",
          steps: update.steps!.map((step) => ({
            ...step,
            state: "completed",
            finishedAt: "2026-11-22T10:06:16Z",
          })),
        }),
      );
      await waitFor(() =>
        expect(within(dialog).getByRole("heading")).toHaveTextContent("Processed"),
      );
    },
  );
  test("running elapsed time advances from the active batch and freezes during a pause", async () => {
    const now = Date.parse("2026-11-21T10:00:00Z");
    const clock = vi.spyOn(Date, "now").mockReturnValue(now);
    const user = userEvent.setup();
    const update = {
      ...updateFixture(),
      currentOrder: 21,
      elapsedTotal: 2000,
      elapsedSince: new Date(now - 64000).toISOString(),
    };
    update.steps![20] = {
      ...update.steps![20],
      name: "clip-apply",
      state: "running",
      repeated: true,
      batch: 2,
      batches: 24,
    };
    const view = setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Processing", update }],
    });
    view.setUpdate(update);
    await user.click(
      await screen.findByRole("button", {
        name: `View job progress for ${available.name}: Processing`,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("status")).toHaveTextContent(
      "21 of 57 \u2014 clip-apply \u2014 batch 2 of 24 \u2014 34m 24s elapsed",
    );
    clock.mockReturnValue(now + 2000);
    await waitFor(
      () => expect(within(dialog).getByRole("status")).toHaveTextContent("34m 26s elapsed"),
      { timeout: 2000 },
    );
    await act(() =>
      view.client.setQueryData(["admin-osm-region-update", update.id], {
        ...update,
        state: "paused",
      }),
    );
    await waitFor(() =>
      expect(within(dialog).getByRole("status")).toHaveTextContent("33m 20s elapsed"),
    );
    clock.mockReturnValue(now + 3600000);
    expect(within(dialog).getByRole("status")).toHaveTextContent("33m 20s elapsed");
  });
  test("Finished help sums recorded batches and explains unavailable legacy timing", async () => {
    const user = userEvent.setup(),
      job = updateFixture("processing");
    job.steps![6].elapsedTotal = 924;
    job.steps![0].elapsedTotal = null;
    const view = setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Processing", update: job }],
    });
    view.setUpdate(job);
    await user.click(
      await screen.findByRole("button", {
        name: `View job progress for ${available.name}: Processing`,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: `Processing ${available.id}` });
    const total = dialog.querySelector('[data-task-order="7"] td:last-child .tooltip-trigger')!;
    expect(document.getElementById(total.getAttribute("aria-describedby")!)).toHaveTextContent(
      "15m 24s total",
    );
    const unknown = dialog.querySelector('[data-task-order="1"] td:last-child .tooltip-trigger')!;
    expect(document.getElementById(unknown.getAttribute("aria-describedby")!)).toHaveTextContent(
      "Elapsed duration was not recorded for this run.",
    );
    expect(
      dialog.querySelector('[data-task-order="1"] td:nth-child(4) .tooltip-trigger'),
    ).toBeNull();
    expect(
      dialog.querySelector('[data-task-order="20"] td:last-child .tooltip-trigger'),
    ).toBeNull();
  });
  test.each([
    ["Downloading", "downloading"],
    ["Processing", "processing"],
    ["Canceled", "canceled"],
    ["Paused", "paused"],
    ["Failed", "failed"],
    ["Ready", "succeeded"],
    ["Stale", "succeeded"],
    ["Waiting", "waiting"],
    ["Pausing", "pausing"],
    ["Canceling", "canceling"],
  ] as const)(
    "%s status opens the retained progress dialog and restores focus",
    async (status, state) => {
      const user = userEvent.setup(),
        update = updateFixture(state);
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status, update }],
      });
      view.setUpdate(update);
      const trigger = await screen.findByRole("button", {
        name: `View job progress for ${available.name}: ${status}`,
      });
      expect(trigger.closest("td")).toHaveClass("table-action-cell");
      await user.click(trigger);
      const dialog = await screen.findByRole("dialog", {
        name: `${state === "succeeded" ? "Processed" : "Processing"} ${available.id}`,
      });
      expect(screen.getAllByRole("dialog")).toHaveLength(1);
      await user.click(within(dialog).getByRole("button", { name: "Close region progress" }));
      await waitFor(() => expect(trigger).toHaveFocus());
    },
  );
  test.each(["Ready", "Stale", "Waiting", "Pausing", "Canceling", "Available"] as const)(
    "%s has no status action without an eligible history",
    async (status) => {
      setup({ ...initial, updatesEnabled: true, regions: [{ ...available, status }] });
      await screen.findByText(available.name);
      expect(
        screen.queryByRole("button", {
          name: `View job progress for ${available.name}: ${status}`,
        }),
      ).not.toBeInTheDocument();
    },
  );
  test.each([false, true])(
    "Stale explains reprocessing with the exact tooltip (history=%s)",
    async (history) => {
      setup({
        ...initial,
        updatesEnabled: true,
        regions: [
          {
            ...available,
            generation: 1,
            status: "Stale",
            update: history ? updateFixture("succeeded") : undefined,
          },
        ],
      });
      const status = history
        ? await screen.findByRole("button", {
            name: `View job progress for ${available.name}: Stale`,
          })
        : await screen.findByText("Stale");
      expect(status).toHaveClass("osm-regions-status--stale");
      expect(status).toHaveAccessibleDescription("Updated matcher policies require reprocessing");
      expect(status).not.toHaveAttribute("title");
      expect(document.getElementById(status.getAttribute("aria-describedby")!)).toHaveTextContent(
        /^Updated matcher policies require reprocessing$/,
      );
      await userEvent.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
      expect(screen.getByRole("menuitem", { name: "Reprocess region" })).toBeEnabled();
      expect(screen.queryByRole("menuitem", { name: "Download region" })).not.toBeInTheDocument();
    },
  );
  test("clearing a failed overlay preserves the underlying Stale state while the catalog refresh is pending", async () => {
    const failed = { ...updateFixture("failed"), regionId: row.id };
    let afterClear = false;
    let finishRefresh: ((response: Response) => void) | undefined;
    const view = setup(
      {
        ...initial,
        updatesEnabled: true,
        regions: [{ ...row, status: "Failed", reprocessingRequired: true, update: failed }],
      },
      () => {
        afterClear = true;
        return json({ ...failed, cleared: true });
      },
    );
    const original = view.fetch.getMockImplementation()!;
    view.fetch.mockImplementation((path: string, init?: RequestInit) =>
      path === "/api/admin/osm/regions" && afterClear
        ? new Promise<Response>((resolve) => {
            finishRefresh = resolve;
          })
        : original(path, init),
    );
    await screen.findByText("Failed", { selector: ".osm-regions-status" });
    await userEvent.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Clear job status" }));
    await waitFor(() =>
      expect(view.client.getQueryData<OSMCatalogs>(["admin-osm-regions"])?.regions[0].status).toBe(
        "Stale",
      ),
    );
    expect(
      screen.queryByText("Ready", { selector: ".osm-regions-status" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(finishRefresh).toBeDefined());
    await act(async () =>
      finishRefresh!(
        json({
          ...initial,
          updatesEnabled: true,
          regions: [{ ...row, status: "Stale", reprocessingRequired: true }],
        }),
      ),
    );
    expect(
      await screen.findByText("Stale", { selector: ".osm-regions-status" }),
    ).toHaveAccessibleDescription("Updated matcher policies require reprocessing");
  });

  test("queued resume shows queue status above the paused task history", async () => {
    const user = userEvent.setup(),
      job = updateFixture("waiting");
    job.steps![19].state = "paused";
    const view = setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Waiting", update: job }],
    });
    view.setUpdate(job);
    await user.click(
      await screen.findByRole("button", {
        name: `View job progress for ${available.name}: Waiting`,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: `Processing ${available.id}` });
    expect(within(dialog).getByRole("status")).toHaveTextContent("Waiting to resume processing");
    expect(within(dialog).getByRole("status")).toHaveTextContent(/^Waiting to resume processing$/);
    expect(
      within(within(dialog).getByRole("status")).getByLabelText("running"),
    ).toBeInTheDocument();
    expect(
      dialog.querySelector('[data-task-order="20"] .coverage-route-status--unavailable'),
    ).not.toBeNull();
  });
  test.each(["download", "delete"] as const)(
    "queued %s opens progress before it has started",
    async (operation) => {
      const user = userEvent.setup();
      const deletion = operation === "delete";
      const job: RegionUpdate = {
        ...updateFixture("waiting"),
        operation,
        startedAt: null,
        currentOrder: 0,
        phase: "Downloading",
        steps: deletion
          ? ["detach-region", "reclaim-storage", "verify-cleanup"].map((name, i) => ({
              order: i + 1,
              name,
              repeated: false,
              state: "pending",
              batch: 0,
              batches: 0,
              startedAt: null,
              finishedAt: null,
            }))
          : [
              {
                order: 1,
                name: "prepare-source",
                repeated: false,
                state: "pending",
                batch: 0,
                batches: 0,
                startedAt: null,
                finishedAt: null,
              },
              ...updateFixture().steps!.map((s) => ({
                ...s,
                order: s.order + 1,
                name: s.order === 1 ? "download" : s.name,
                state: "pending" as const,
                startedAt: null,
                finishedAt: null,
              })),
            ],
      };
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, update: job, status: "Waiting" }],
      });
      view.setUpdate(job);
      const status = await screen.findByRole("button", {
        name: `View job progress for ${available.name}: Waiting`,
      });
      await user.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
      const menu = screen.getByRole("menuitem", { name: "View job progress" });
      expect(menu).not.toHaveAttribute("data-disabled");
      await user.click(menu);
      let dialog = await screen.findByRole("dialog", {
        name: `${deletion ? "Deleting" : "Downloading"} ${available.id}`,
      });
      expect(within(dialog).getByRole("status")).toHaveTextContent(
        /^Waiting for updater capacity$/,
      );
      expect(
        within(within(dialog).getByRole("status")).getByLabelText("running"),
      ).toBeInTheDocument();
      expect(
        dialog.querySelector('[data-task-order="1"] .osm-update-icon-slot'),
      ).toBeEmptyDOMElement();
      await user.click(within(dialog).getByRole("button", { name: "Close region progress" }));
      await user.click(status);
      dialog = await screen.findByRole("dialog", {
        name: `${deletion ? "Deleting" : "Downloading"} ${available.id}`,
      });
      await act(() =>
        view.client.setQueryData(["admin-osm-region-update", job.id], {
          ...job,
          state: deletion ? "deleting" : "downloading",
          startedAt: "2026-11-21T10:00:00Z",
          currentOrder: 1,
        }),
      );
      await waitFor(() =>
        expect(within(dialog).getByRole("status")).not.toHaveTextContent("Waiting"),
      );
    },
  );

  test("active execution elapsed ticks between short completed batches", async () => {
    const now = Date.parse("2026-11-21T10:00:00Z"),
      clock = vi.spyOn(Date, "now").mockReturnValue(now),
      user = userEvent.setup();
    const job = {
      ...updateFixture(),
      elapsedTotal: 188,
      elapsedSince: null,
      executionElapsed: 0,
      executionSince: new Date(now - 2000000).toISOString(),
    };
    job.steps![19].state = "completed";
    const view = setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Processing", update: job }],
    });
    view.setUpdate(job);
    await user.click(
      await screen.findByRole("button", {
        name: `View job progress for ${available.name}: Processing`,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("status")).toHaveTextContent("33m 20s elapsed");
    clock.mockReturnValue(now + 2000);
    await waitFor(
      () => expect(within(dialog).getByRole("status")).toHaveTextContent("33m 22s elapsed"),
      { timeout: 2000 },
    );
  });
  test.each([
    ["Ready", "succeeded", ["Reprocess region"]],
    ["Paused", "paused", ["Resume processing", "Cancel processing"]],
    ["Failed", "failed", ["Resume processing", "Clear job status"]],
    ["Canceled", "canceled", ["Download region", "Clear job status"]],
  ] as const)("%s exposes its requested status actions", async (status, state, actions) => {
    const user = userEvent.setup(),
      update = { ...updateFixture(state), resumable: false };
    setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status, generation: status === "Ready" ? 62 : null, update }],
    });
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    const operations = screen
      .getAllByRole("menuitem")
      .map((item) => item.textContent)
      .filter(
        (label) => !["View boundaries", "View job progress", "Delete region"].includes(label ?? ""),
      );
    expect(operations).toEqual(actions);
    const items = screen.getAllByRole("menuitem");
    expect(items.slice(0, 2).map((item) => item.textContent)).toEqual([
      "View boundaries",
      "View job progress",
    ]);
    expect(items[1].nextElementSibling).toHaveAttribute("role", "separator");
  });
  test.each([
    ["Failed", "failed", null],
    ["Canceled", "canceled", null],
    ["Failed", "failed", 62],
    ["Canceled", "canceled", 62],
  ] as const)(
    "clears %s with prior generation %s back to its baseline",
    async (status, state, generation) => {
      const user = userEvent.setup(),
        update = updateFixture(state),
        snapshot = {
          ...initial,
          updatesEnabled: true,
          regions: [{ ...available, status, generation, update }],
        };
      const baseline = generation === null ? ("Available" as const) : ("Ready" as const);
      const view = setup(snapshot, () => {
        view.set({ ...snapshot, regions: [{ ...available, generation, status: baseline }] });
        return json({ ...update, cleared: true }, 202);
      });
      await user.click(
        await screen.findByRole("button", { name: `Actions for ${available.name}` }),
      );
      await user.click(screen.getByRole("menuitem", { name: "Clear job status" }));
      await screen.findByText(baseline, { selector: ".osm-regions-status" });
      const call = view.fetch.mock.calls.find(([path]) => path.endsWith("/commands"));
      expect(call?.[1]?.body).toBe('{"action":"clear"}');
      expect(new Headers(call?.[1]?.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
      expect(
        screen.queryByRole("button", {
          name: `View job progress for ${available.name}: ${baseline}`,
        }),
      ).not.toBeInTheDocument();
    },
  );
  test("a failed pre-import run offers Resume processing and requeues", async () => {
    const user = userEvent.setup(),
      failed = { ...updateFixture("failed"), resumable: false },
      snapshot = {
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, status: "Failed" as const, update: failed }],
      };
    const waiting = { ...failed, state: "waiting" as const };
    const view = setup(snapshot, () => {
      view.set({ ...snapshot, regions: [{ ...available, status: "Waiting", update: waiting }] });
      return json(waiting, 202);
    });
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Resume processing" }));
    await screen.findByText("Waiting", { selector: ".osm-regions-status" });
    expect(view.fetch.mock.calls.find(([path]) => path.endsWith("/commands"))?.[1]?.body).toBe(
      '{"action":"resume"}',
    );
  });
  test("a failed resumable candidate can be canceled before a fresh region update", async () => {
    const user = userEvent.setup(),
      update = { ...updateFixture("failed"), resumable: true, promoted: false };
    const canceling = { ...update, state: "canceling" as const, error: null };
    const snapshot = {
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Failed" as const, update }],
    };
    const view = setup(snapshot, () => {
      view.setUpdate(canceling);
      view.set({
        ...snapshot,
        regions: [{ ...available, status: "Canceling", update: canceling }],
      });
      return json(canceling, 202);
    });
    view.setUpdate(update);
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Cancel processing" }));
    expect(view.fetch.mock.calls.find(([path]) => path.endsWith("/commands"))?.[1]?.body).toBe(
      '{"action":"cancel"}',
    );
    await screen.findByText("Canceling", { selector: ".osm-regions-status" });
  });
  test("failed post-promotion maintenance does not offer candidate cancellation", async () => {
    const user = userEvent.setup(),
      update = { ...updateFixture("failed"), resumable: true, promoted: true };
    setup({
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Failed", update }],
    });
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    expect(screen.queryByRole("menuitem", { name: "Cancel processing" })).not.toBeInTheDocument();
  });
  test("paused work can resume into Waiting and stopped runs retain View job progress", async () => {
    const user = userEvent.setup(),
      paused = { ...updateFixture("paused"), error: "This update is paused." },
      waiting = { ...paused, state: "waiting" as const, error: null };
    const snapshot = {
      ...initial,
      updatesEnabled: true,
      regions: [{ ...available, status: "Paused" as const, update: paused }],
    };
    const view = setup(snapshot, () => {
      view.set({ ...snapshot, regions: [{ ...available, status: "Waiting", update: waiting }] });
      return json(waiting, 202);
    });
    await user.click(await screen.findByRole("button", { name: `Actions for ${available.name}` }));
    expect(screen.getByRole("menuitem", { name: "View job progress" })).toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Resume processing" }));
    await screen.findByText("Waiting", { selector: ".osm-regions-status" });
    const call = view.fetch.mock.calls.find(([path]) => path.endsWith("/commands"));
    expect(call?.[1]?.body).toBe('{"action":"resume"}');
  });
  test("arrow paging works outside persistent search and leaves focused editing alone", async () => {
    const regions = Array.from({ length: 60 }, (_, i) => ({
      ...available,
      id: `geo:${i}`,
      name: `Region ${String(i).padStart(2, "0")}`,
    }));
    setup({ ...initial, regions });
    await screen.findByText("Region 00");
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(tableRows()[0]).toHaveTextContent("Region 25");
    const search = screen.getByRole("searchbox");
    search.focus();
    fireEvent.keyDown(search, { key: "ArrowLeft" });
    expect(tableRows()[0]).toHaveTextContent("Region 25");
    search.blur();
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(tableRows()[0]).toHaveTextContent("Region 00");
  });
  test("missing values without help have no tooltip trigger or help cursor hook", async () => {
    setup();
    await screen.findByText(available.name);
    const cells = within(screen.getByText(available.name).closest("tr")!).getAllByRole("cell");
    for (const index of [1, 2, 3, 4, 5])
      expect(cells[index].querySelector(".tooltip-trigger")).toBeNull();
    expect(document.querySelector(".osm-regions-section [class*=coverage]")).toBeNull();
  });
  test("refresh is a non-clickable status label with cataloged progress and an em dash for partial results", async () => {
    const view = setup({
      ...initial,
      refresh: { ...job, progress: { ...job.progress, completed: 123, total: 554 } },
    });
    await screen.findByText(row.name);
    const label = screen.getByText("Refreshing catalogs");
    expect(label.tagName).toBe("SPAN");
    expect(label).toHaveClass("osm-regions-refreshing");
    expect(screen.queryByRole("button", { name: "Refresh catalogs" })).not.toBeInTheDocument();
    const help = label.closest(".tooltip-trigger")!.getAttribute("aria-describedby")!;
    expect(document.getElementById(help)).toHaveTextContent("123 of 554 regions cataloged");
    view.set({
      ...initial,
      refresh: { ...job, state: "partially_succeeded", progress: { ...job.progress, failed: 4 } },
    });
    await act(async () => {
      await view.client.refetchQueries({ queryKey: ["admin-osm-regions"] });
    });
    const button = await screen.findByRole("button", { name: "Refresh catalogs" }),
      id = button.closest(".tooltip-trigger")!.getAttribute("aria-describedby")!;
    expect(document.getElementById(id)).toHaveTextContent(
      "4 metadata lookups unavailable — cached values retained",
    );
  });
  test("uses the Users heading/link and requested columns without extra provider panels", async () => {
    setup(initial, undefined, false);
    await screen.findByText(row.name);
    expect(screen.getByRole("heading", { name: "Regions" }).tagName).toBe("H2");
    expect(screen.queryByText("Administration")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh catalogs" })).toHaveClass("text-action");
    expect(
      screen
        .getAllByRole("columnheader")
        .map((cell) => cell.textContent?.replace(/[▲▼]/g, "").trim()),
    ).toEqual([
      "Name",
      "Date",
      "PBF size",
      "Downloaded",
      "Generation",
      "Database usage",
      "Workouts",
      "Status",
      "Actions",
    ]);
    expect(screen.getByRole("columnheader", { name: /Generation/ })).not.toHaveAttribute(
      "aria-sort",
    );
    expect(screen.queryByRole("button", { name: "Generation" })).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox", { name: "Search regions" })).toBeVisible();
    expect(screen.getByRole("checkbox", { name: "Show all regions" })).not.toBeChecked();
    expect(tableRows()).toHaveLength(1);
    expect(document.querySelector(".dialog-table-frame")).toHaveAttribute(
      "style",
      "--dialog-table-rows: 25;",
    );
    expect(screen.queryByText("Catalog refreshed")).not.toBeInTheDocument();
  });

  test("formats dates/sizes with shared numeric lanes and exact tooltips", async () => {
    const user = userEvent.setup();
    setup();
    const prepared = (await screen.findByText(row.name)).closest("tr")!,
      cells = within(prepared).getAllByRole("cell");
    expect(cells[1]).toHaveTextContent(regionDate(row.pbfModifiedAt));
    expect(cells[2]).toHaveTextContent("1 GiB");
    expect(cells[3]).toHaveTextContent(regionDate(row.downloadedAt));
    expect(cells[4]).toHaveTextContent("62");
    expect(cells[5]).toHaveTextContent("2.0 GiB");
    for (const index of [1, 2, 3, 5])
      expect(cells[index].querySelector(".workout-value-lane[data-width-sample]")).not.toBeNull();
    const help = (index: number) =>
      document.getElementById(
        cells[index].querySelector(".tooltip-trigger")!.getAttribute("aria-describedby")!,
      )!;
    await user.hover(cells[1].querySelector(".tooltip-trigger")!);
    expect(help(1)).toHaveTextContent(regionExactTime(row.pbfModifiedAt)!);
    await user.unhover(cells[1].querySelector(".tooltip-trigger")!);
    await user.hover(cells[2].querySelector(".tooltip-trigger")!);
    expect(help(2)).toHaveTextContent("1,073,741,824 bytes");
    await user.unhover(cells[2].querySelector(".tooltip-trigger")!);
    await user.hover(cells[3].querySelector(".tooltip-trigger")!);
    expect(help(3)).toHaveTextContent("943,718,400 bytes");
    await user.unhover(cells[3].querySelector(".tooltip-trigger")!);
    await user.hover(cells[4].querySelector(".tooltip-trigger")!);
    expect(help(4)).toHaveTextContent(regionDate(row.promotedAt, true));
    await user.unhover(cells[4].querySelector(".tooltip-trigger")!);
    await user.hover(cells[5].querySelector(".tooltip-trigger")!);
    expect(help(5)).toHaveTextContent(/^2,147,483,648 bytes$/);
  });

  test("uses muted n/a for unavailable values, including unknown legacy download dates", async () => {
    setup({ ...initial, regions: [available, { ...row, downloadedAt: null }] });
    await screen.findByText(row.name);
    const absent = screen.getByText(available.name).closest("tr")!;
    expect(within(absent).getAllByText("n/a")).toHaveLength(5);
    expect([...absent.querySelectorAll(".workout-table-na")]).toHaveLength(5);
    expect(screen.queryByText("Unknown")).not.toBeInTheDocument();
    expect(screen.queryByText("0 B")).not.toBeInTheDocument();
    expect(within(screen.getByText(row.name).closest("tr")!).getByText("n/a")).toHaveClass(
      "workout-table-na",
    );
  });

  test("starts Name ascending and uses descending defaults for dates and size columns", async () => {
    const user = userEvent.setup();
    setup({
      ...initial,
      regions: [
        {
          ...row,
          id: "geo:a",
          name: "Alpha",
          pbfBytes: 1,
          databaseBytes: 1,
          pbfModifiedAt: "2026-01-01Z",
          downloadedAt: "2026-01-01Z",
        },
        {
          ...row,
          id: "geo:z",
          name: "Zulu",
          pbfBytes: 2,
          databaseBytes: 2,
          pbfModifiedAt: "2026-11-01Z",
          downloadedAt: "2026-11-01Z",
        },
        available,
      ],
    });
    await screen.findByText("Alpha");
    expect(tableRows()[0]).toHaveTextContent("Alpha");
    for (const label of ["Date", "PBF size", "Downloaded", "Database usage"]) {
      await user.click(screen.getByRole("button", { name: label }));
      expect(tableRows()[0]).toHaveTextContent("Zulu");
      expect(screen.getByRole("columnheader", { name: new RegExp(label) })).toHaveAttribute(
        "aria-sort",
        "descending",
      );
    }
    await user.click(screen.getByRole("button", { name: "Database usage" }));
    expect(tableRows()[0]).toHaveTextContent("Alpha");
    expect(tableRows()[2]).toHaveTextContent(available.name);
  });

  test("keeps 25 page slots and replaces row contents in place on short final pages/search", async () => {
    const user = userEvent.setup();
    const regions = Array.from({ length: 56 }, (_, i) => ({
      ...available,
      id: `geofabrik:r${i}`,
      name: `Region ${String(i).padStart(2, "0")}`,
    }));
    setup({ ...initial, regions });
    await screen.findByText("Region 00");
    const first = tableRows()[0],
      frame = document.querySelector(".dialog-table-frame")!;
    expect(tableRows()).toHaveLength(25);
    expect(frame).toHaveAttribute("style", "--dialog-table-rows: 25;");
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(tableRows()[0]).toBe(first);
    expect(first).toHaveTextContent("Region 25");
    await user.click(screen.getByRole("button", { name: "Last page" }));
    expect(tableRows()).toHaveLength(6);
    expect(tableRows()[0]).toBe(first);
    expect(first).toHaveTextContent("Region 50");
    expect(document.querySelectorAll(".osm-regions-table tbody tr")).toHaveLength(25);
    expect(frame).toHaveAttribute("style", "--dialog-table-rows: 25;");
    await user.type(screen.getByRole("searchbox"), "r55");
    expect(tableRows()).toHaveLength(1);
    expect(tableRows()[0]).toBe(first);
    expect(first).toHaveTextContent("Region 55");
    await user.clear(screen.getByRole("searchbox"));
    await user.type(screen.getByRole("searchbox"), "missing");
    expect(tableRows()).toHaveLength(0);
    expect(screen.getByText("No regions match this search.")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Name/ })).toBeInTheDocument();
  });

  test("Show all regions hides only Available and preserves status meanings", async () => {
    const user = userEvent.setup();
    const statuses = [
      "Ready",
      "Downloading",
      "Processing",
      "Failed",
      "Deleting",
      "Available",
    ] as const;
    setup({
      ...initial,
      regions: statuses.map((status) => ({
        ...available,
        id: `geo:${status}`,
        name: `Region ${status}`,
        status,
      })),
    });
    await screen.findByText("Region Ready");
    await user.click(screen.getByRole("checkbox", { name: "Show all regions" }));
    expect(tableRows()).toHaveLength(5);
    expect(screen.queryByText("Region Available")).not.toBeInTheDocument();
    for (const status of statuses.slice(0, 5))
      expect(within(screen.getByRole("cell", { name: status })).getByText(status)).toHaveClass(
        `osm-regions-status--${status.toLowerCase()}`,
      );
  });

  test.each(["Ready", "Available", "Processing", "Paused", "Failed", "Canceled"] as const)(
    "disabled updates preserve read-only actions for %s",
    async (status) => {
      const update =
        status === "Available"
          ? undefined
          : {
              ...updateFixture(
                status === "Ready" ? "succeeded" : (status.toLowerCase() as RegionUpdate["state"]),
              ),
              regionId: row.id,
            };
      const region: OSMRegion = { ...row, status, update };
      const { setUpdate, fetch } = setup({ ...initial, updatesEnabled: false, regions: [region] });
      if (update) setUpdate(update);
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: `Actions for ${row.name}` }));
      expect(screen.getByRole("menuitem", { name: "View boundaries" })).not.toHaveAttribute(
        "data-disabled",
      );
      for (const item of screen.getAllByRole("menuitem"))
        if (!["View boundaries", "View job progress"].includes(item.textContent!))
          expect(item).toHaveAttribute("data-disabled");
      if (update) {
        const progress = screen.getByRole("menuitem", { name: "View job progress" });
        expect(progress).not.toHaveAttribute("data-disabled");
        await user.click(progress);
        await screen.findByRole("dialog");
      }
      expect(fetch.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    },
  );

  test("disabling updates closes an open deletion confirmation", async () => {
    const user = userEvent.setup(),
      fixture = { ...initial, updatesEnabled: true, regions: [row] };
    const { client, set, fetch } = setup(fixture);
    await user.click(await screen.findByRole("button", { name: `Actions for ${row.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Delete region" }));
    await screen.findByRole("dialog", { name: "Delete region" });
    set({ ...fixture, updatesEnabled: false });
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["admin-osm-regions"] });
    });
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Delete region" })).not.toBeInTheDocument(),
    );
    expect(fetch.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });

  test("context menus expose working boundaries and disabled lifecycle entries without row activation", async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText(row.name);
    await user.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "View boundaries",
      "Reprocess region",
      "Delete region",
    ]);
    const divider = screen.getByRole("menuitem", {
      name: "Reprocess region",
    }).previousElementSibling!;
    expect(divider).toHaveAttribute("role", "separator");
    expect(divider.previousElementSibling).toHaveTextContent("View boundaries");
    expect(screen.getByRole("menuitem", { name: "Reprocess region" })).toHaveAttribute(
      "data-disabled",
    );
    expect(screen.getByRole("menuitem", { name: "Delete region" })).toHaveClass("danger-item");
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
    expect(screen.getByRole("menuitem", { name: "Download region" })).toHaveAttribute(
      "data-disabled",
    );
    expect(screen.queryByRole("menuitem", { name: "Delete region" })).not.toBeInTheDocument();
  });

  test("region deletion confirms its scope, shows Deleting progress and returns to Available", async () => {
    const user = userEvent.setup();
    const deletion: RegionUpdate = {
      ...updateFixture("deleting"),
      regionId: row.id,
      operation: "delete",
      currentOrder: 0,
      resumable: false,
      buildId: null,
      startedAt: null,
      steps: ["detach-region", "reclaim-storage", "verify-cleanup"].map((name, index) => ({
        order: index + 1,
        name,
        repeated: false,
        state: "pending",
        batch: 0,
        batches: 0,
        startedAt: null,
        finishedAt: null,
      })),
    };
    const view = setup(
      { ...initial, updatesEnabled: true },
      (init) => {
        expect(JSON.parse(String(init.body))).toEqual({ regionId: row.id, action: "delete" });
        view.set({
          ...initial,
          updatesEnabled: true,
          regions: [{ ...row, update: deletion, status: "Deleting" }],
        });
        return json(deletion, 202);
      },
      false,
    );
    view.setUpdate(deletion);
    await screen.findByText(row.name);
    await user.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Delete region" }));
    const dialog = screen.getByRole("dialog", { name: "Delete region" });
    expect(dialog).toHaveTextContent(row.id);
    expect(dialog.querySelectorAll(".deletion-confirmation-message br")).toHaveLength(2);
    expect(dialog).toHaveTextContent(
      "This will permanently delete processed map data as well as data from any unfinished processing job to reclaim database storage; this region will then become available for download again.",
    );
    expect(dialog).toHaveTextContent(
      "Workout routes in this region that have already updated their coverage will continue to display unaffected.",
    );
    expect(dialog).not.toHaveTextContent(/build/i);
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(view.fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Delete region" }));
    await user.click(
      within(screen.getByRole("dialog", { name: "Delete region" })).getByRole("button", {
        name: "Delete",
      }),
    );
    const status = await screen.findByRole("button", {
      name: `View job progress for ${row.name}: Deleting`,
    });
    expect(status).toHaveClass("osm-regions-status--deleting");
    await user.click(status);
    const progress = await screen.findByRole("dialog", { name: `Deleting ${row.id}` });
    expect(within(progress).getByText("reclaim-storage")).toBeInTheDocument();
    expect(progress.querySelectorAll("tbody tr[data-task-order]")).toHaveLength(3);
    expect(progress).toHaveClass("osm-update-progress-dialog--compact");
    await user.click(within(progress).getByRole("button", { name: "Close region progress" }));
    await user.click(screen.getByRole("button", { name: `Actions for ${row.name}` }));
    expect(screen.getByRole("menuitem", { name: "Delete region" })).toHaveAttribute(
      "data-disabled",
    );
    expect(screen.queryByRole("menuitem", { name: "Cancel processing" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Download region" })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.click(status);
    const complete = {
      ...row,
      generation: null,
      buildId: null,
      promotedAt: null,
      downloadedAt: null,
      downloadedBytes: null,
      databaseBytes: null,
      status: "Available" as const,
      update: { ...deletion, state: "succeeded" as const },
    };
    view.setUpdate(complete.update);
    view.set({ ...initial, updatesEnabled: true, regions: [complete] });
    await act(() => view.client.invalidateQueries({ queryKey: ["admin-osm-regions"] }));
    await act(() =>
      view.client.setQueryData(["admin-osm-region-update", deletion.id], complete.update),
    );
    await waitFor(() =>
      expect(document.querySelector(".osm-regions-row:not([hidden])")).toBeNull(),
    );
    await user.click(
      within(await screen.findByRole("dialog", { name: `Deleted ${row.id}` })).getByRole("button", {
        name: "Close region progress",
      }),
    );
    await waitFor(() => expect(screen.getByRole("heading", { name: "Regions" })).toHaveFocus());
    await user.click(screen.getByRole("checkbox", { name: "Show all regions" }));
    expect(await screen.findByRole("cell", { name: "Available" })).toBeInTheDocument();
    expect(within(tableRows()[0] as HTMLElement).getAllByText("n/a").length).toBeGreaterThan(1);
    await user.click(
      screen.getByRole("button", { name: `View job progress for ${row.name}: Available` }),
    );
    expect(await screen.findByRole("dialog", { name: `Deleted ${row.id}` })).toHaveClass(
      "osm-update-progress-dialog--compact",
    );
  });

  test.each([1, 3, 20, 21, 57])(
    "reserves only %i steps up to the existing twenty-row progress viewport",
    async (count) => {
      const user = userEvent.setup();
      const update = { ...updateFixture(), steps: updateFixture().steps!.slice(0, count) };
      const view = setup({
        ...initial,
        updatesEnabled: true,
        regions: [{ ...available, update, status: "Processing" }],
      });
      view.setUpdate(update);
      await user.click(
        await screen.findByRole("button", {
          name: `View job progress for ${available.name}: Processing`,
        }),
      );
      const dialog = await screen.findByRole("dialog", { name: `Processing ${available.id}` });
      expect(dialog.classList.contains("osm-update-progress-dialog--compact")).toBe(count <= 20);
      expect(
        (dialog.querySelector(".dialog-table-frame") as HTMLElement).style.getPropertyValue(
          "--dialog-table-rows",
        ),
      ).toBe(String(Math.min(count, 20)));
      expect(dialog.querySelectorAll("tr[data-task-order]")).toHaveLength(count);
    },
  );

  test.each(["Downloading", "Processing"] as const)(
    "offers View job progress for %s without enabling an unimplemented workflow",
    async (status) => {
      const user = userEvent.setup();
      setup({ ...initial, regions: [{ ...available, status }] });
      await screen.findByText(available.name);
      await user.click(screen.getByRole("button", { name: `Actions for ${available.name}` }));
      expect(screen.getByRole("menuitem", { name: "View job progress" })).toHaveAttribute(
        "data-disabled",
      );
    },
  );

  test("reuses the exact map and canvas through dismissals and switches region source/fit", async () => {
    const user = userEvent.setup();
    const view = setup();
    const name = await screen.findByRole("button", { name: row.name });
    await user.click(name);
    await waitFor(() => expect(maps.instances).toHaveLength(1));
    const instance = maps.instances[0],
      canvas = instance.canvas;
    await waitFor(() => expect(instance.fitBounds).toHaveBeenCalledTimes(1));
    expect(instance.options.style).toBe(baseMaps.families[0].styles.dark);
    expect(instance.layers.map((layer) => layer.id)).toEqual([
      "ground",
      "admin-region-boundary-fill",
      "admin-region-boundary-line",
      "labels",
    ]);
    expect(instance.layers[1].paint).toEqual({ "fill-color": "#ff00ff", "fill-opacity": 0.25 });
    expect(instance.layers[2].paint).toEqual({ "line-color": "#ff00ff", "line-width": 5 });
    expect(instance.layers[2]).toMatchObject({
      layout: { "line-join": "round", "line-round-limit": 0 },
    });
    await user.click(screen.getByRole("button", { name: "Close region boundaries" }));
    await waitFor(() => expect(name).toHaveFocus());
    expect(instance.remove).not.toHaveBeenCalled();
    expect(canvas.isConnected).toBe(true);
    expect(canvas.closest(".osm-regions-map-parking")).not.toBeNull();
    await user.click(screen.getByRole("button", { name: available.name }));
    await waitFor(() => expect(instance.fitBounds).toHaveBeenCalledTimes(2));
    expect(maps.instances).toHaveLength(1);
    expect(document.querySelector("[role=dialog] canvas")).toBe(canvas);
    await user.keyboard("{Escape}");
    await screen.findByRole("button", { name: row.name });
    await user.click(name);
    await waitFor(() => expect(instance.fitBounds).toHaveBeenCalledTimes(3));
    expect(maps.instances).toHaveLength(1);
    expect(
      view.fetch.mock.calls.filter(([path]) => path.startsWith("/api/admin/osm/region-boundary?")),
    ).toHaveLength(2);
    view.unmount();
    expect(instance.remove).toHaveBeenCalledTimes(1);
  });

  test("changes theme on the retained map without recreating the canvas or resetting its camera", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByRole("button", { name: row.name }));
    await waitFor(() => expect(maps.instances[0]?.fitBounds).toHaveBeenCalledTimes(1));
    const instance = maps.instances[0];
    act(() => {
      document.documentElement.dataset.theme = "light";
    });
    await waitFor(() =>
      expect(instance.setStyle).toHaveBeenCalledWith(baseMaps.families[0].styles.light, {
        diff: false,
      }),
    );
    await waitFor(() => expect(instance.sources.has("admin-region-boundary")).toBe(true));
    expect(instance.fitBounds).toHaveBeenCalledTimes(1);
    expect(instance.remove).not.toHaveBeenCalled();
    expect(maps.instances).toHaveLength(1);
  });

  test("row clicking and native keyboard activation show boundaries and restore the name action", async () => {
    const user = userEvent.setup();
    setup();
    const name = await screen.findByRole("button", { name: row.name });
    await user.click(name.closest("tr")!.children[6]);
    expect(await screen.findByRole("dialog", { name: row.name })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(name).toHaveFocus());
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: row.name })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(name).toHaveFocus());
    await user.keyboard(" ");
    expect(await screen.findByRole("dialog", { name: row.name })).toBeInTheDocument();
  });

  test("boundary failures show safe retry feedback and do not leak diagnostics", async () => {
    const user = userEvent.setup();
    const view = setup();
    view.failBoundary(true);
    await user.click(await screen.findByRole("button", { name: row.name }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Region boundaries could not be loaded",
    );
    expect(screen.getByRole("alert")).not.toHaveTextContent("private-boundary-error");
    view.failBoundary(false);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(maps.instances[0]?.fitBounds).toHaveBeenCalledTimes(1));
  });

  test("coalesces local submissions with admin CSRF while keeping displayed rows", async () => {
    const user = userEvent.setup();
    let release!: (response: Response) => void;
    const view = setup(
      initial,
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    );
    const original = (await screen.findByText(row.name)).closest("tr");
    const trigger = screen.getByRole("button", { name: "Refresh catalogs" });
    await user.dblClick(trigger);
    const commands = view.fetch.mock.calls.filter(
      ([path]) => path === "/api/admin/osm/catalog-refreshes",
    );
    expect(commands).toHaveLength(1);
    expect(commands[0][1]?.body).toBe("{}");
    expect(new Headers(commands[0][1]?.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
    view.set({ ...initial, refresh: job });
    await act(async () => release(json(job, 202)));
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Refreshing catalogs"),
    );
    expect(screen.getByText(row.name).closest("tr")).toBe(original);
    expect(screen.queryByRole("button", { name: "Refresh catalogs" })).not.toBeInTheDocument();
  });

  test("retains filters through compatible refreshes and tab changes; late dialogs close offscreen", async () => {
    const user = userEvent.setup();
    const view = setup();
    await screen.findByText(row.name);
    await user.type(screen.getByRole("searchbox"), "norcal");
    await user.click(screen.getByRole("checkbox", { name: "Show all regions" }));
    view.active(false);
    view.active(true);
    expect(screen.getByRole("searchbox")).toHaveValue("norcal");
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: row.name }));
    await screen.findByRole("dialog");
    view.active(false);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    view.active(true);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("disables refresh when configured off and renders safe read/request failures", async () => {
    const user = userEvent.setup();
    const view = setup({ ...initial, refreshEnabled: false });
    await screen.findByText(row.name);
    expect(screen.getByRole("button", { name: "Refresh catalogs" })).toBeDisabled();
    view.fetch.mockImplementationOnce(() => Promise.resolve(json({ detail: "private-sql" }, 503)));
    await act(async () => {
      await view.client.refetchQueries({ queryKey: ["admin-osm-regions"] });
    });
    expect(await screen.findByRole("alert")).not.toHaveTextContent("private-sql");
    expect(screen.getByText(row.name)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });
});

test("boundary fitting handles multipolygons and chooses the short antimeridian extent", () => {
  expect(
    boundaryBounds({
      type: "Polygon",
      coordinates: [
        [
          [179, -10],
          [-179, -10],
          [-179, 10],
          [179, -10],
        ],
      ],
    }),
  ).toEqual([
    [179, -10],
    [181, 10],
  ]);
  expect(
    boundaryBounds({
      type: "MultiPolygon",
      coordinates: [
        [
          [
            [10, 20],
            [12, 20],
            [12, 22],
            [10, 20],
          ],
        ],
        [
          [
            [11, 19],
            [13, 19],
            [13, 21],
            [11, 19],
          ],
        ],
      ],
    }),
  ).toEqual([
    [10, 19],
    [13, 22],
  ]);
});
