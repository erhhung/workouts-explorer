import {
  focusManager,
  onlineManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  type DataSync as DataSyncModel,
  type IngestActivity,
  type JobDetail,
  type JobEventList,
  type JobFileList,
  type JobList,
  type JobProgress,
  type Preferences,
} from "./api";
import { DataSync } from "./DataSync";

const SOURCE_A = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const SOURCE_B = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";
const SOURCE_C = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC";
const JOB_ID = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDD";
const progress: JobProgress = {
  current: 2,
  total: 4,
  filesDiscovered: 4,
  filesSkipped: 0,
  filesSucceeded: 1,
  filesFailed: 1,
  workoutsCreated: 3,
  workoutsUpdated: 2,
  workoutsUnchanged: 1,
  workoutsRejected: 1,
};
const preferences: Preferences = {
  theme: "dark",
  units: "metric",
  timezone: "UTC",
  firstWeekday: "monday",
  clockFormat: "24h",
  workoutColumns: ["date", "type", "duration"],
  pageSize: 25,
  coverageDiagnosticsEnabled: false,
  initialized: true,
};
const snapshot: DataSyncModel = {
  schedule: {
    enabled: true,
    sourceCount: 1,
    cadence: "Daily at 02:00",
    cadenceSeconds: 86400,
    staleDays: 3,
    nextRunAt: "2026-08-07T02:00:00Z",
  },
  sources: [
    {
      id: SOURCE_A,
      displayName: "Phone exports",
      type: "health-auto-export-local",
      status: "connected",
      autoSyncEnabled: true,
      freshness: { lastSyncSucceededAt: "2026-08-06T01:00:00Z", lastNewExportDate: "2026-08-05" },
    },
    {
      id: SOURCE_B,
      displayName: "Watch archive",
      type: "health-auto-export-local",
      status: "connected",
      autoSyncEnabled: false,
      freshness: {},
    },
    {
      id: SOURCE_C,
      displayName: "Old folder",
      type: "health-auto-export-local",
      status: "connection-failed",
      autoSyncEnabled: true,
      freshness: { staleSince: "2026-08-01" },
    },
  ],
  notifications: [
    {
      id: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
      type: "source-stale",
      severity: "warning",
      state: "remind",
      subjectType: "source",
      sourceId: SOURCE_C,
      title: "Export is stale",
      message: "No new export has arrived.",
      createdAt: "2026-08-02T00:00:00Z",
      updatedAt: "2026-08-02T00:00:00Z",
      remindAt: "2026-08-08T00:00:00Z",
    },
  ],
  notificationsTruncated: false,
};
const detail: JobDetail = {
  id: JOB_ID,
  trigger: "manual",
  status: "partially_succeeded",
  attempt: 0,
  progress,
  children: [],
  results: { filesSucceeded: 1, filesFailed: 1, workoutsCreated: 3 },
  retriedByJobIds: [],
  cancelRequested: false,
  createdAt: "2026-08-06T12:00:00Z",
  updatedAt: "2026-08-06T12:05:00Z",
  terminalAt: "2026-08-06T12:05:00Z",
};

const coverageFailure: JobDetail = {
  ...detail,
  operation: "coverage_update",
  trigger: "system",
  status: "failed",
  failureCode: "coverage-osm-unavailable",
  failureSummary: "Coverage map data could not be processed.",
  routeStats: {
    total: 2,
    processed: 2,
    running: 0,
    succeeded: 0,
    failed: 2,
    cancelled: 0,
    superseded: 0,
  },
  children: ["A", "B"].map((letter) => ({
    ...detail,
    id: letter.repeat(32),
    parentJobId: JOB_ID,
    operation: "coverage_update" as const,
    status: "failed" as const,
  })),
};

const eligibleCoverageRetry = {
  canRetry: true,
  reason: "eligible" as const,
  eligibleRouteCount: 2,
  unsuccessfulRouteCount: 2,
  currentRouteCount: 0,
  currentCoverageJobIds: [],
};

const zeroProgress: JobProgress = {
  current: 0,
  total: 0,
  filesDiscovered: 0,
  filesSkipped: 0,
  filesSucceeded: 0,
  filesFailed: 0,
  workoutsCreated: 0,
  workoutsUpdated: 0,
  workoutsUnchanged: 0,
  workoutsRejected: 0,
};
const activityLabels: Record<IngestActivity["phase"], string> = {
  resuming: "Resuming incomplete sync job.",
  preparing: "Preparing to read workout files.",
  discovering: "Looking for workout files that need to be processed.",
  importing: "Parsing workout files and importing new and changed workouts.",
  analyzing_imported_routes: "Analyzing map data for imported routes.",
  analyzing_existing_routes: "Analyzing map data for existing routes.",
  updating_time_zones: "Determining and updating workout time zones.",
  cleanup: "Cleaning up temporary files.",
};

function sourceRun(overrides: Partial<JobDetail> = {}): JobDetail {
  return {
    ...detail,
    status: "running",
    progress: zeroProgress,
    results: undefined,
    terminalAt: undefined,
    source: {
      sourceId: SOURCE_A,
      generation: 1,
      displayName: "Activity source",
      sourceType: "health-auto-export-local",
    },
    ...overrides,
  };
}

async function advanceSyncTimers(milliseconds: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(milliseconds);
  });
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

function renderDataSync(
  selectedJobId?: string,
  navigate = vi.fn(),
  pollingIntervalSeconds = 30,
  userPreferences = preferences,
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const component = (jobId = selectedJobId) => (
    <QueryClientProvider client={client}>
      <DataSync
        csrfToken="csrf-data-sync"
        preferences={userPreferences}
        pollingIntervalSeconds={pollingIntervalSeconds}
        selectedJobId={jobId}
        navigate={navigate}
      />
    </QueryClientProvider>
  );
  const view = render(component());
  return {
    ...view,
    client,
    navigate,
    rerenderJob: (jobId?: string) => view.rerender(component(jobId)),
  };
}

function baseFetch(
  handler?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined,
) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const path = String(input);
    const handled = handler?.(path, init);
    if (handled) return Promise.resolve(handled);
    if (path === "/api/data-sync") return Promise.resolve(json(snapshot));
    if (path === "/api/jobs?page=1&pageSize=25")
      return Promise.resolve(
        json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }),
      );
    if (path === `/api/jobs/${JOB_ID}`) return Promise.resolve(json(detail));
    throw new Error(`Unhandled fetch: ${init?.method ?? "GET"} ${path}`);
  });
}

function artifactPage(
  kind: "files" | "events" | "logs",
  page: number,
  count = 25,
  totalPages = 3,
  jobId = JOB_ID,
  pageSize = 25,
): JobFileList | JobEventList {
  const pagination = {
    page,
    pageSize,
    totalItems:
      totalPages === 0 ? 0 : (totalPages - 1) * pageSize + (page === totalPages ? count : 5),
    totalPages,
  };
  if (kind === "files")
    return {
      pagination,
      items: Array.from({ length: count }, (_, index) => ({
        id: `${jobId}-${page}-${index}`,
        jobId,
        basename: `files-page-${page}-row-${index}.json`,
        source: {
          sourceId: SOURCE_A,
          generation: 1,
          displayName: "Synthetic exports",
          sourceType: "health-auto-export-local",
        },
        state: "succeeded",
        sizeBytes: 1024,
        createdAt: "2026-08-06T12:00:00Z",
        updatedAt: "2026-08-06T12:05:00Z",
      })),
    };
  return {
    pagination,
    items: Array.from({ length: count }, (_, index) => ({
      id: (page - 1) * pageSize + index + 1,
      jobId,
      severity: "info" as const,
      code: "synthetic-artifact",
      message: `${kind}-page-${page}-row-${index}`,
      fields: {},
      createdAt: "2026-08-06T12:05:00Z",
    })),
  };
}

describe("DataSync", () => {
  describe("ingest activity", () => {
    test.each(
      Object.entries(activityLabels).flatMap(([phase, label]) =>
        [0, 1, 100].map((files) => [phase as IngestActivity["phase"], label, files] as const),
      ),
    )("shows %s (%s) after results for %i files", async (phase, label, files) => {
      const child = sourceRun({
        id: "F".repeat(32),
        parentJobId: JOB_ID,
        activity: { phase, current: 0, updatedAt: detail.updatedAt },
        progress: {
          ...zeroProgress,
          current: files,
          total: files,
          filesSucceeded: files,
        },
      });
      baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}`
          ? json({
              ...child,
              id: JOB_ID,
              source: undefined,
              activity: undefined,
              children: [child],
            })
          : undefined,
      );
      renderDataSync(JOB_ID);
      const messages = await screen.findAllByText(label);
      expect(messages).toHaveLength(2);
      for (const message of messages) {
        expect(message).toHaveAttribute("role", "status");
        expect(message).toHaveAttribute("aria-live", "polite");
        const result = message.parentElement!.previousElementSibling!;
        expect(result).toHaveClass(files === 0 ? "sync-no-data" : "result-counts");
        expect(result).toHaveTextContent(
          files === 0 ? "No files were processed." : "Files Processed",
        );
        expect(result.previousElementSibling).toHaveClass("sync-progress");
      }
      const region = screen.getByRole("region", { name: "Selected run" });
      expect(region).not.toHaveAttribute("aria-live");
      expect(region.querySelector(".job-detail-card")).not.toHaveAttribute("aria-live");
      expect(within(region).queryByText(/Sync job completed/)).not.toBeInTheDocument();
    });

    test.each([
      [undefined, undefined, undefined],
      [null, 12, undefined],
      [0, undefined, "Routes checked: 0"],
      [3, null, "Routes checked: 3"],
      [0, 0, "Routes checked: 0 of 0"],
      [2, 7, "Routes checked: 2 of 7"],
    ])(
      "renders actual route counts %s / %s without a live counter",
      async (current, total, copy) => {
        baseFetch((path) =>
          path === `/api/jobs/${JOB_ID}`
            ? json({
                ...sourceRun(),
                activity: {
                  phase: "analyzing_existing_routes",
                  current,
                  total,
                  updatedAt: detail.updatedAt,
                },
              })
            : undefined,
        );
        renderDataSync(JOB_ID);
        const message = await screen.findByText(activityLabels.analyzing_existing_routes);
        expect(message).not.toHaveTextContent("Routes checked");
        if (copy) {
          const count = screen.getByText(copy);
          expect(count.closest('[aria-live], [role="status"]')).toBeNull();
        } else expect(screen.queryByText(/Routes checked/)).not.toBeInTheDocument();
        expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
      },
    );

    test.each(["importing", "updating_time_zones", "cleanup"] as const)(
      "does not reinterpret %s counts as route or time zone counters",
      async (phase) => {
        baseFetch((path) =>
          path === `/api/jobs/${JOB_ID}`
            ? json(
                sourceRun({
                  activity: { phase, current: 3, total: 9, updatedAt: detail.updatedAt },
                }),
              )
            : undefined,
        );
        renderDataSync(JOB_ID);
        await screen.findByText(activityLabels[phase]);
        expect(screen.queryByText(/Routes checked|3 of 9/)).not.toBeInTheDocument();
      },
    );

    test("shows per-source current phases at the root and after each source's results", async () => {
      const children = [
        sourceRun({ id: SOURCE_A, activity: { phase: "importing", updatedAt: detail.updatedAt } }),
        sourceRun({
          id: SOURCE_B,
          source: { ...sourceRun().source!, sourceId: SOURCE_B, displayName: "Other source" },
          activity: { phase: "updating_time_zones", updatedAt: detail.updatedAt },
        }),
      ];
      baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}`
          ? json(sourceRun({ source: undefined, children }))
          : undefined,
      );
      renderDataSync(JOB_ID);
      await screen.findByText(`Activity source: ${activityLabels.importing}`);
      expect(screen.getByText(`Other source: ${activityLabels.updating_time_zones}`)).toBeVisible();
      const sources = document.querySelectorAll<HTMLElement>(".source-run");
      expect(within(sources[0]).getByText(activityLabels.importing)).toBeVisible();
      expect(within(sources[1]).getByText(activityLabels.updating_time_zones)).toBeVisible();
    });

    test.each([
      [0, 0, 0, "Sync job completed, but no new data were imported."],
      [100, 0, 0, "Sync job completed, but no new data were imported."],
      [1, 1, 0, "Sync job completed successfully."],
      [1, 0, 1, "Sync job completed successfully."],
    ] as const)(
      "uses terminal status for success with %i files, %i created, %i updated",
      async (files, created, updated, copy) => {
        let run = sourceRun({
          progress: {
            ...zeroProgress,
            current: files,
            total: files,
            filesSucceeded: files,
            workoutsCreated: created,
            workoutsUpdated: updated,
          },
          activity: { phase: "cleanup", updatedAt: detail.updatedAt },
        });
        baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(run) : undefined));
        const { client } = renderDataSync(JOB_ID);
        await screen.findByText(activityLabels.cleanup);
        expect(screen.queryByText(/Sync job completed/)).not.toBeInTheDocument();
        run = { ...run, status: "succeeded", results: run.progress };
        await act(async () => client.refetchQueries({ queryKey: ["job", JOB_ID] }));
        expect(await screen.findByText(copy)).toBeVisible();
        expect(screen.queryByText(activityLabels.cleanup)).not.toBeInTheDocument();
        expect(screen.queryByText("No new data.")).not.toBeInTheDocument();
        expect(screen.queryByText("No files were processed.")).not.toBeInTheDocument();
        expect(screen.getAllByText(/Sync job completed/)).toHaveLength(1);
      },
    );

    test.each(["failed", "cancelled", "partially_succeeded"] as const)(
      "never presents stale cleanup or success for %s",
      async (status) => {
        baseFetch((path) =>
          path === `/api/jobs/${JOB_ID}`
            ? json(
                sourceRun({
                  status,
                  progress: { ...progress, current: 100, total: 100 },
                  activity: { phase: "cleanup", updatedAt: detail.updatedAt },
                  failureSummary: "A file could not be imported.",
                }),
              )
            : undefined,
        );
        renderDataSync(JOB_ID);
        await screen.findByText("A file could not be imported.");
        expect(screen.queryByText(activityLabels.cleanup)).not.toBeInTheDocument();
        expect(screen.queryByText(/Sync job completed/)).not.toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Retry run" })).toBeEnabled();
      },
    );

    test("prioritizes cancellation and queued status over persisted phases without guessing historical phases", async () => {
      const run = sourceRun({
        progress: { ...progress, current: 100, total: 100 },
        activity: {
          phase: "analyzing_imported_routes",
          current: 1,
          total: 2,
          updatedAt: detail.updatedAt,
        },
      });
      baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}` ? json({ ...run, cancelRequested: true }) : undefined,
      );
      const { client } = renderDataSync(JOB_ID);
      await screen.findByText("Cancelling...", { selector: '[role="status"]' });
      expect(screen.queryByText(activityLabels.analyzing_imported_routes)).not.toBeInTheDocument();
      expect(screen.queryByText(/Routes checked/)).not.toBeInTheDocument();
      act(() => client.setQueryData(["job", JOB_ID], { ...run, status: "queued" }));
      expect(await screen.findByText("Waiting for sync job to start.")).toBeVisible();
      act(() => client.setQueryData(["job", JOB_ID], { ...run, activity: undefined }));
      await waitFor(() =>
        expect(screen.queryByText("Waiting for sync job to start.")).not.toBeInTheDocument(),
      );
      for (const label of Object.values(activityLabels))
        expect(screen.queryByText(label)).not.toBeInTheDocument();
      expect(screen.queryByText(/Sync job completed/)).not.toBeInTheDocument();
    });
  });

  describe("detail polling and phase history", () => {
    test("polls running details every 10 seconds but history and snapshot every 30 seconds", async () => {
      vi.useFakeTimers();
      const fetchMock = baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}` ? json(sourceRun()) : undefined,
      );
      const view = renderDataSync(JOB_ID);
      const reads = (path: string) =>
        fetchMock.mock.calls.filter(([input]) => input === path).length;
      try {
        await advanceSyncTimers(0);
        expect(reads(`/api/jobs/${JOB_ID}`)).toBe(1);
        await advanceSyncTimers(9_999);
        expect(reads(`/api/jobs/${JOB_ID}`)).toBe(1);
        await advanceSyncTimers(2);
        expect(reads(`/api/jobs/${JOB_ID}`)).toBe(2);
        await advanceSyncTimers(10_000);
        expect(reads(`/api/jobs/${JOB_ID}`)).toBe(3);
        expect(reads("/api/jobs?page=1&pageSize=25")).toBe(1);
        expect(reads("/api/data-sync")).toBe(1);
        await advanceSyncTimers(10_000);
        expect(reads(`/api/jobs/${JOB_ID}`)).toBe(4);
        expect(reads("/api/jobs?page=1&pageSize=25")).toBe(2);
        expect(reads("/api/data-sync")).toBe(2);
        expect(view.navigate).not.toHaveBeenCalled();
      } finally {
        view.unmount();
        vi.useRealTimers();
      }
    });

    test("switches queued/running intervals in place and stops terminal ingest polling", async () => {
      vi.useFakeTimers();
      let status: JobDetail["status"] = "queued";
      let detailReads = 0;
      baseFetch((path) => {
        if (path !== `/api/jobs/${JOB_ID}`) return undefined;
        detailReads += 1;
        return json(sourceRun({ status }));
      });
      const view = renderDataSync(JOB_ID);
      try {
        await advanceSyncTimers(0);
        const region = screen.getByRole("region", { name: "Selected run" });
        status = "running";
        await advanceSyncTimers(29_999);
        expect(detailReads).toBe(1);
        await advanceSyncTimers(2);
        expect(detailReads).toBe(2);
        expect(within(region).getByText("Running", { selector: ".sync-status" })).toBeVisible();
        status = "queued";
        await advanceSyncTimers(10_001);
        expect(detailReads).toBe(3);
        expect(within(region).getByText("Waiting for sync job to start.")).toBeVisible();
        status = "running";
        await advanceSyncTimers(29_997);
        expect(detailReads).toBe(3);
        await advanceSyncTimers(4);
        expect(detailReads).toBe(4);
        status = "succeeded";
        await advanceSyncTimers(10_001);
        expect(detailReads).toBe(5);
        expect(
          within(region).getByText("Sync job completed, but no new data were imported."),
        ).toBeVisible();
        await advanceSyncTimers(60_000);
        expect(detailReads).toBe(5);
        expect(screen.getByRole("region", { name: "Selected run" })).toBe(region);
        expect(view.navigate).not.toHaveBeenCalled();
      } finally {
        view.unmount();
        vi.useRealTimers();
      }
    });

    test("pauses hidden-tab polls, refetches on focus, and cleans up after navigation and unmount", async () => {
      vi.useFakeTimers();
      focusManager.setFocused(undefined);
      const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
      const nextJobId = "F".repeat(32);
      const fetchMock = baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}` || path === `/api/jobs/${nextJobId}`
          ? json(sourceRun({ id: path.split("/").at(-1)! }))
          : undefined,
      );
      const view = renderDataSync(JOB_ID);
      const reads = (jobId: string) =>
        fetchMock.mock.calls.filter(([path]) => path === `/api/jobs/${jobId}`).length;
      try {
        await advanceSyncTimers(0);
        visibility.mockReturnValue("hidden");
        fireEvent(document, new Event("visibilitychange", { bubbles: true }));
        const initialReads = fetchMock.mock.calls.length;
        await advanceSyncTimers(30_001);
        expect(fetchMock.mock.calls).toHaveLength(initialReads);
        visibility.mockReturnValue("visible");
        fireEvent(document, new Event("visibilitychange", { bubbles: true }));
        await advanceSyncTimers(1);
        expect(reads(JOB_ID)).toBe(2);
        view.rerenderJob(nextJobId);
        await advanceSyncTimers(1);
        expect(reads(nextJobId)).toBe(1);
        await advanceSyncTimers(10_001);
        expect(reads(JOB_ID)).toBe(2);
        expect(reads(nextJobId)).toBe(2);
        view.unmount();
        const unmountedReads = fetchMock.mock.calls.length;
        await advanceSyncTimers(60_000);
        expect(fetchMock.mock.calls).toHaveLength(unmountedReads);
      } finally {
        view.unmount();
        visibility.mockRestore();
        focusManager.setFocused(undefined);
        vi.useRealTimers();
      }
    });

    test("retains the 30 second terminal Coverage eligibility poll", async () => {
      vi.useFakeTimers();
      let reads = 0;
      baseFetch((path) => {
        if (path !== `/api/jobs/${JOB_ID}`) return undefined;
        reads += 1;
        return json({ ...coverageFailure, retryEligibility: eligibleCoverageRetry });
      });
      const view = renderDataSync(JOB_ID);
      try {
        await advanceSyncTimers(0);
        await advanceSyncTimers(29_999);
        expect(reads).toBe(1);
        await advanceSyncTimers(2);
        expect(reads).toBe(2);
        expect(screen.getByRole("button", { name: "Retry run" })).toBeEnabled();
      } finally {
        view.unmount();
        vi.useRealTimers();
      }
    });

    test("aborts an in-flight detail poll on navigation without applying its late phases", async () => {
      vi.useFakeTimers();
      let signal: AbortSignal | null | undefined;
      let resolvePoll!: (response: Response) => void;
      let reads = 0;
      const nextJobId = "F".repeat(32);
      baseFetch((path, init) => {
        if (path === `/api/jobs/${nextJobId}`)
          return json(sourceRun({ id: nextJobId, status: "queued" }));
        if (path !== `/api/jobs/${JOB_ID}`) return undefined;
        if (++reads === 1) return json(sourceRun());
        signal = init?.signal;
        return new Promise<Response>((resolve) => {
          resolvePoll = resolve;
        });
      });
      const view = renderDataSync(JOB_ID);
      try {
        await advanceSyncTimers(0);
        await advanceSyncTimers(10_000);
        expect(signal?.aborted).toBe(false);
        view.rerenderJob(nextJobId);
        await advanceSyncTimers(1);
        expect(signal?.aborted).toBe(true);
        await act(async () =>
          resolvePoll(
            json(
              sourceRun({
                activity: { phase: "cleanup", updatedAt: detail.updatedAt },
              }),
            ),
          ),
        );
        await advanceSyncTimers(1);
        expect(screen.getByText("Waiting for sync job to start.")).toBeVisible();
        expect(screen.queryByText(activityLabels.cleanup)).not.toBeInTheDocument();
        await advanceSyncTimers(10_001);
        expect(reads).toBe(2);
        expect(view.navigate).not.toHaveBeenCalled();
      } finally {
        view.unmount();
        vi.useRealTimers();
      }
    });

    test("does not refresh Events from cached activity after a detail 404 or refetch it on focus", async () => {
      vi.useFakeTimers();
      let missing = false;
      let detailReads = 0;
      const fetchMock = baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}`) {
          detailReads += 1;
          return missing
            ? json({ title: "Not found" }, 404)
            : json(
                sourceRun({
                  activity: { phase: "preparing", updatedAt: detail.updatedAt },
                }),
              );
        }
        if (path.includes("/events?")) return json(artifactPage("events", 1));
        return undefined;
      });
      const view = renderDataSync(JOB_ID);
      const eventReads = () =>
        fetchMock.mock.calls.filter(([path]) => String(path).includes("/events?")).length;
      try {
        await advanceSyncTimers(0);
        fireEvent.click(screen.getByRole("button", { name: /^Events/ }));
        await advanceSyncTimers(1);
        expect(eventReads()).toBe(1);
        missing = true;
        await advanceSyncTimers(10_001);
        expect(detailReads).toBe(2);
        expect(eventReads()).toBe(1);
        expect(view.navigate).toHaveBeenCalledWith("/data-sync", true);
        act(() => {
          focusManager.setFocused(false);
          focusManager.setFocused(true);
        });
        await advanceSyncTimers(30_000);
        expect(detailReads).toBe(2);
        expect(eventReads()).toBe(1);
      } finally {
        view.unmount();
        focusManager.setFocused(undefined);
        vi.useRealTimers();
      }
    });

    test.each([false, true])(
      "refreshes open Events through fast phases and terminal catchup (root: %s), never Logs",
      async (root) => {
        vi.useFakeTimers();
        let run = sourceRun({ activity: { phase: "preparing", updatedAt: detail.updatedAt } });
        let unavailable = false;
        const phases: IngestActivity["phase"][] = ["preparing"];
        const fetchMock = baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}`) {
            if (unavailable) return json({ title: "Unavailable" }, 503);
            return json(
              root
                ? {
                    ...run,
                    activity: undefined,
                    source: undefined,
                    children: [{ ...run, id: SOURCE_A }],
                  }
                : run,
            );
          }
          if (path.includes(`/${JOB_ID}/events?`)) {
            const events = artifactPage("events", 1, phases.length, 1) as JobEventList;
            events.items = phases.map((phase, index) => ({
              ...events.items[index],
              code: `ingest-${phase}`,
              message: `Recorded phase: ${activityLabels[phase]}`,
            }));
            return json(events);
          }
          for (const kind of ["files", "logs"] as const)
            if (path.includes(`/${JOB_ID}/${kind}?`)) return json(artifactPage(kind, 1));
          return undefined;
        });
        const view = renderDataSync(JOB_ID);
        const reads = (kind: string) =>
          fetchMock.mock.calls.filter(([path]) => String(path).includes(`/${kind}?`)).length;
        try {
          await advanceSyncTimers(0);
          for (const name of ["Events", "Logs", "Files"]) {
            fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${name}`) }));
            await advanceSyncTimers(1);
          }
          const before = { events: reads("events"), files: reads("files"), logs: reads("logs") };
          phases.push("discovering", "importing", "analyzing_imported_routes");
          run = {
            ...run,
            activity: {
              phase: "analyzing_imported_routes",
              current: 0,
              updatedAt: detail.updatedAt,
            },
          };
          await advanceSyncTimers(10_001);
          for (const phase of phases)
            expect(
              screen.getByText(`info: Recorded phase: ${activityLabels[phase]}`),
            ).toBeVisible();
          expect(reads("events")).toBe(before.events + 1);
          expect(reads("files")).toBe(before.files + 1);
          expect(reads("logs")).toBe(before.logs);
          expect(
            screen.queryByText(activityLabels.importing, { selector: '[role="status"]' }),
          ).not.toBeInTheDocument();
          unavailable = true;
          await advanceSyncTimers(18_000);
          expect(reads("events")).toBe(before.events + 1);
          expect(reads("files")).toBe(before.files + 1);
          unavailable = false;
          phases.push("analyzing_existing_routes", "updating_time_zones", "cleanup");
          run = {
            ...run,
            status: "succeeded",
            activity: { phase: "cleanup", updatedAt: detail.updatedAt },
          };
          await advanceSyncTimers(10_001);
          expect(screen.getByText(`info: Recorded phase: ${activityLabels.cleanup}`)).toBeVisible();
          expect(
            screen.queryByText(activityLabels.cleanup, { selector: '[role="status"]' }),
          ).not.toBeInTheDocument();
          expect(reads("events")).toBe(before.events + 2);
          expect(reads("files")).toBe(before.files + 2);
          expect(reads("logs")).toBe(before.logs);
          fireEvent.click(screen.getByRole("button", { name: /^Events/ }));
          await act(async () => view.client.refetchQueries({ queryKey: ["job", JOB_ID] }));
          await advanceSyncTimers(1);
          expect(reads("events")).toBe(before.events + 2);
          expect(
            view.client.getQueryState(["job-artifacts", JOB_ID, "events", 1])?.isInvalidated,
          ).toBe(true);
          fireEvent.click(screen.getByRole("button", { name: /^Events/ }));
          await advanceSyncTimers(1);
          expect(reads("events")).toBe(before.events + 3);
          await advanceSyncTimers(30_000);
          expect(reads("events")).toBe(before.events + 3);
        } finally {
          view.unmount();
          vi.useRealTimers();
        }
      },
    );
  });

  test.each([
    ["manual", "running"],
    ["scheduled", "running"],
    ["manual", "succeeded"],
    ["scheduled", "succeeded"],
  ] as const)(
    "renders routed counters for %s %s parent and source runs",
    async (trigger, status) => {
      const counters = {
        ...progress,
        workoutsCreated: 516,
        workoutsCreatedWithRoute: 491,
        workoutsUpdated: 33,
        workoutsUpdatedWithRoute: 31,
      };
      const run: JobDetail = {
        ...detail,
        trigger,
        status,
        progress: counters,
        results: status === "running" ? undefined : counters,
        children: [
          {
            ...detail,
            id: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
            trigger,
            status,
            progress: counters,
            results: status === "running" ? undefined : counters,
            source: {
              sourceId: SOURCE_A,
              displayName: "Counter source",
              generation: 1,
              sourceType: "health-auto-export-local",
            },
          },
        ],
      };
      baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(run) : undefined));
      const { client } = renderDataSync(JOB_ID);
      await screen.findAllByText("Workouts Imported");
      for (let update = 0; update < 2; update += 1) {
        for (const [label, count, routes] of [
          ["Workouts Imported", "516", "491"],
          ["Workouts Updated", "33", "31"],
        ]) {
          const labels = screen.getAllByText(label);
          expect(labels).toHaveLength(2);
          for (const node of labels) {
            const value = node.nextElementSibling!;
            expect(value.textContent).toBe(`${count} (${routes} routes)`);
            expect(value).toHaveClass("sync-workout-count");
            expect(value.firstElementChild).toHaveTextContent(count);
            expect(value.lastElementChild?.tagName).toBe("SPAN");
            expect(value.lastElementChild).toHaveClass("sync-route-count");
            expect(value.lastElementChild).toHaveTextContent(`(${routes} routes)`);
          }
        }
        if (update === 0) act(() => client.setQueryData(["job", JOB_ID], structuredClone(run)));
      }
    },
  );

  test.each([undefined, null, 0])(
    "distinguishes unknown route counts from genuine zero (%s)",
    async (routes) => {
      baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}`
          ? json({
              ...detail,
              results: {
                filesSucceeded: 1,
                workoutsCreated: 516,
                workoutsUpdated: 33,
                workoutsCreatedWithRoute: routes,
                workoutsUpdatedWithRoute: routes,
              },
            })
          : undefined,
      );
      renderDataSync(JOB_ID);
      const imported = await screen.findByText("Workouts Imported");
      const updated = screen.getByText("Workouts Updated");
      expect(imported.nextElementSibling).toHaveTextContent(
        routes === 0 ? /^516 \(0 routes\)$/ : /^516$/,
      );
      expect(updated.nextElementSibling).toHaveTextContent(
        routes === 0 ? /^33 \(0 routes\)$/ : /^33$/,
      );
      for (const label of [imported, updated]) {
        const suffix = label.nextElementSibling?.querySelector(".sync-route-count");
        if (routes === 0) expect(suffix).toHaveTextContent("(0 routes)");
        else expect(suffix).toBeNull();
      }
    },
  );

  test("aligns retrieval states with the first Manual and Automated fields", () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise(() => undefined));
    renderDataSync();
    expect(screen.getByText("Retrieving sources...")).toHaveClass("sync-retrieval-state");
    expect(screen.getByText("Retrieving schedule...")).toHaveClass("sync-retrieval-state");
    expect(screen.queryByText("Loading sources...")).not.toBeInTheDocument();
    expect(screen.queryByText("Loading schedule...")).not.toBeInTheDocument();
  });

  test("defaults once to connected automatic sources and posts the exact incremental body with CSRF", async () => {
    let request: { body: unknown; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path === "/api/ingest" && init?.method === "POST") {
        request = {
          body: JSON.parse(String(init.body)),
          csrf: new Headers(init.headers).get("X-CSRF-Token"),
        };
        return json({ jobId: JOB_ID, status: "queued", reused: true }, 202);
      }
      return undefined;
    });
    const { navigate } = renderDataSync();
    expect(await screen.findByRole("checkbox", { name: /Phone exports/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Watch archive/ })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Old folder/ })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() =>
      expect(request).toEqual({ body: { sourceIds: [SOURCE_A] }, csrf: "csrf-data-sync" }),
    );
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
    expect(await screen.findByText("Existing run selected.")).toBeInTheDocument();
  });

  test("shows an unchecked reprocess option only between bounded date inputs and submit", async () => {
    baseFetch();
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    const name = "Reprocess all files in this date range";
    expect(screen.queryByRole("checkbox", { name })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Sources" })).toBeInTheDocument();
    await user.click(
      within(screen.getByRole("group", { name: "Scope" })).getByRole("radio", {
        name: "Specific date range Import data in inclusive date range.",
      }),
    );
    const checkbox = screen.getByRole("checkbox", { name });
    expect(checkbox).not.toBeChecked();
    expect(checkbox).toHaveAttribute("type", "checkbox");
    expect(checkbox).toHaveAttribute("name", "reprocessAllFiles");
    const form = checkbox.closest("form")!;
    expect([
      ...form.querySelectorAll(
        'input[type="date"], input[name="reprocessAllFiles"], button[type="submit"]',
      ),
    ]).toEqual([
      screen.getByLabelText("Start date"),
      screen.getByLabelText("End date"),
      checkbox,
      screen.getByRole("button", { name: "Start sync" }),
    ]);
    await user.click(screen.getByRole("radio", { name: /New data only/ }));
    expect(screen.queryByRole("checkbox", { name })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Start date")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("End date")).not.toBeInTheDocument();
  });

  test.each([true, false])(
    "submits explicit reprocessAllFiles %s for a bounded range after toggling",
    async (checked) => {
      let request: { body: unknown; csrf: string | null } | undefined;
      baseFetch((path, init) => {
        if (path !== "/api/ingest") return undefined;
        request = {
          body: JSON.parse(String(init?.body)),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        };
        return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
      });
      renderDataSync();
      const user = userEvent.setup();
      await screen.findByRole("checkbox", { name: /Phone exports/ });
      await user.click(screen.getByRole("checkbox", { name: /Watch archive/ }));
      await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
      fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-08-01" } });
      fireEvent.change(screen.getByLabelText("End date"), { target: { value: "2026-08-07" } });
      const checkbox = screen.getByRole("checkbox", {
        name: "Reprocess all files in this date range",
      });
      await user.click(checkbox);
      expect(checkbox).toBeChecked();
      if (!checked) await user.click(checkbox);
      await user.click(screen.getByRole("button", { name: "Start sync" }));
      await waitFor(() =>
        expect(request).toEqual({
          body: {
            sourceIds: [SOURCE_A, SOURCE_B],
            startDate: "2026-08-01",
            endDate: "2026-08-07",
            reprocessAllFiles: checked,
          },
          csrf: "csrf-data-sync",
        }),
      );
    },
  );

  test("clears reprocessing on range-mode changes and omits it from incremental requests", async () => {
    let body: unknown;
    baseFetch((path, init) => {
      if (path !== "/api/ingest") return undefined;
      body = JSON.parse(String(init?.body));
      return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
    });
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    const bounded = screen.getByRole("radio", { name: /Specific date range/ });
    const incremental = screen.getByRole("radio", { name: /New data only/ });
    const name = "Reprocess all files in this date range";
    await user.click(bounded);
    await user.click(screen.getByRole("checkbox", { name }));
    expect(screen.getByRole("checkbox", { name })).toBeChecked();
    await user.click(incremental);
    expect(screen.queryByRole("checkbox", { name })).not.toBeInTheDocument();
    await user.click(bounded);
    expect(screen.getByRole("checkbox", { name })).not.toBeChecked();
    await user.click(screen.getByRole("checkbox", { name }));
    await user.click(incremental);
    await user.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() => expect(body).toEqual({ sourceIds: [SOURCE_A] }));
  });

  test("associates reprocess help with the native checkbox and preserves one Tab stop and Space toggling", async () => {
    baseFetch();
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
    const name = "Reprocess all files in this date range";
    const checkbox = screen.getByRole("checkbox", { name });
    const label = screen.getByText(name).closest("label")!;
    const wrapper = label.parentElement!;
    const help = document.getElementById(checkbox.getAttribute("aria-describedby")!)!;
    const content =
      "Download and process every eligible file in this date range, including previously successful files. This may be slow and transfer substantial data.";
    expect(checkbox).toHaveAccessibleDescription(content);
    expect(help).toHaveAttribute("role", "tooltip");
    expect(help).toHaveClass("tooltip-content");
    expect(help).toHaveTextContent(content);
    expect(wrapper).toHaveClass("tooltip", "manual-reprocess");
    expect(label).toHaveClass("tooltip-trigger");
    expect(label.nextElementSibling).toBe(help);
    expect(label).not.toHaveAttribute("tabindex");
    expect(wrapper).not.toHaveAttribute("tabindex");
    expect(checkbox).not.toHaveAttribute("tabindex");
    expect(within(wrapper).queryByRole("button")).not.toBeInTheDocument();
    await user.hover(checkbox);
    expect(checkbox.closest(".manual-reprocess")).toBe(wrapper);
    await user.unhover(checkbox);
    await user.hover(label);
    expect(checkbox).toHaveAccessibleDescription(content);
    await user.unhover(label);
    await user.click(screen.getByLabelText("End date"));
    await user.tab();
    expect(checkbox).toHaveFocus();
    expect(checkbox).toHaveAccessibleDescription(content);
    await user.keyboard(" ");
    expect(checkbox).toBeChecked();
    await user.keyboard(" ");
    expect(checkbox).not.toBeChecked();
    await user.tab();
    expect(screen.getByRole("button", { name: "Start sync" })).toHaveFocus();
  });

  test.each([
    ["", "", "Enter both inclusive dates."],
    ["2026-08-07", "", "Enter both inclusive dates."],
    ["2026-08-07", "2026-08-06", "Start date must be on or before end date."],
  ])("still validates dates with reprocessing enabled: %s / %s", async (start, end, message) => {
    const fetchMock = baseFetch();
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
    await user.click(
      screen.getByRole("checkbox", { name: "Reprocess all files in this date range" }),
    );
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: start } });
    fireEvent.change(screen.getByLabelText("End date"), { target: { value: end } });
    await user.click(screen.getByRole("button", { name: "Start sync" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(message);
    await waitFor(() => expect(alert).toHaveFocus());
    expect(screen.getByLabelText("Start date")).toHaveAccessibleDescription(message);
    expect(screen.getByLabelText("End date")).toHaveAttribute("aria-invalid", "true");
    expect(fetchMock.mock.calls.filter(([path]) => path === "/api/ingest")).toHaveLength(0);
  });

  test("fills an empty end date only when the start date loses focus and submits that day", async () => {
    let body: unknown;
    baseFetch((path, init) => {
      if (path !== "/api/ingest") return undefined;
      body = JSON.parse(String(init?.body));
      return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
    });
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
    const start = screen.getByLabelText("Start date");
    const end = screen.getByLabelText("End date");
    await user.type(start, "2026-08-05");
    expect(start).toHaveFocus();
    expect(end).toHaveValue("");
    fireEvent.change(start, { target: { value: "2026-08-06" } });
    expect(end).toHaveValue("");
    await user.tab();
    expect(end).toHaveFocus();
    expect(end).toHaveValue("2026-08-06");
    await user.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() =>
      expect(body).toEqual({
        sourceIds: [SOURCE_A],
        startDate: "2026-08-06",
        endDate: "2026-08-06",
        reprocessAllFiles: false,
      }),
    );
  });

  test.each([
    ["2023-04-01", "2022-03-01", "2023-04-01"],
    ["2022-02-15", "2022-03-01", "2022-03-01"],
    ["2022-03-01", "2022-03-01", "2022-03-01"],
    ["2023-04-01", "", "2023-04-01"],
    ["", "2022-03-01", "2022-03-01"],
    ["", "", ""],
  ])(
    "completes the DataSync range only on start blur: %s / %s -> %s",
    async (value, endValue, expected) => {
      baseFetch();
      renderDataSync();
      const user = userEvent.setup();
      await screen.findByRole("checkbox", { name: /Phone exports/ });
      await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
      const start = screen.getByLabelText("Start date");
      const end = screen.getByLabelText("End date");
      fireEvent.change(start, { target: { value: "2022-02-01" } });
      fireEvent.change(end, { target: { value: endValue } });
      await user.click(start);
      fireEvent.change(start, { target: { value } });
      expect(start).toHaveFocus();
      expect(end).toHaveValue(endValue);
      await user.tab();
      expect(end).toHaveFocus();
      expect(end).toHaveValue(expected);
    },
  );

  test("leaves the end date empty when the start date is empty on blur", async () => {
    baseFetch();
    renderDataSync();
    const user = userEvent.setup();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await user.click(screen.getByRole("radio", { name: /Specific date range/ }));
    await user.click(screen.getByLabelText("Start date"));
    await user.tab();
    expect(screen.getByLabelText("End date")).toHaveValue("");
  });

  test("validates bounded dates and sends only contract fields", async () => {
    let body: unknown;
    baseFetch((path, init) => {
      if (path === "/api/ingest") {
        body = JSON.parse(String(init?.body));
        return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
      }
      return undefined;
    });
    renderDataSync();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("radio", { name: /Specific date range/ }));
    await userEvent.type(screen.getByLabelText("Start date"), "2026-02-28");
    await userEvent.click(screen.getByLabelText("End date"));
    await userEvent.clear(screen.getByLabelText("End date"));
    await userEvent.type(screen.getByLabelText("End date"), "2026-02-30");
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("both inclusive dates");
    await waitFor(() => expect(alert).toHaveFocus());
    expect(screen.getByLabelText("End date")).toHaveAccessibleDescription(
      "Enter both inclusive dates.",
    );
    expect(body).toBeUndefined();
    await userEvent.clear(screen.getByLabelText("Start date"));
    await userEvent.type(screen.getByLabelText("Start date"), "2026-08-07");
    await userEvent.clear(screen.getByLabelText("End date"));
    await userEvent.type(screen.getByLabelText("End date"), "2026-08-07");
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() =>
      expect(body).toEqual({
        sourceIds: [SOURCE_A],
        startDate: "2026-08-07",
        endDate: "2026-08-07",
        reprocessAllFiles: false,
      }),
    );
  });

  test.each([
    ["queued", false, "Manual sync job has been queued."],
    ["running", false, "Sync started."],
    ["queued", true, "Existing run selected."],
    ["running", true, "Existing run selected."],
  ] as const)(
    "keeps submission feedback continuous until %s detail loads (reused: %s)",
    async (status, reused, confirmation) => {
      let resolvePost!: (response: Response) => void;
      let resolveDetail!: (response: Response) => void;
      const postResponse = new Promise<Response>((resolve) => {
        resolvePost = resolve;
      });
      const detailResponse = new Promise<Response>((resolve) => {
        resolveDetail = resolve;
      });
      let posts = 0;
      let detailReads = 0;
      let body: unknown;
      baseFetch((path, init) => {
        if (path === "/api/ingest") {
          posts += 1;
          body = JSON.parse(String(init?.body));
          return postResponse;
        }
        if (path === `/api/jobs/${JOB_ID}`) {
          detailReads += 1;
          return detailResponse;
        }
        return undefined;
      });
      const view = renderDataSync();
      await screen.findByRole("checkbox", { name: /Phone exports/ });
      await userEvent.click(screen.getByRole("radio", { name: /Specific date range/ }));
      await userEvent.type(screen.getByLabelText("Start date"), "2022-01-01");
      await userEvent.click(screen.getByLabelText("End date"));
      await userEvent.clear(screen.getByLabelText("End date"));
      await userEvent.type(screen.getByLabelText("End date"), "2022-12-31");
      await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
      const button = screen.getByRole("button", { name: "Starting sync..." });
      const form = button.closest("form")!;
      const feedback = screen.getByRole("status", { name: "Sync feedback" });
      expect(button).toBeDisabled();
      const reprocess = screen.getByRole("checkbox", {
        name: "Reprocess all files in this date range",
      });
      expect(reprocess).toBeDisabled();
      await userEvent.click(reprocess);
      expect(reprocess).not.toBeChecked();
      expect(form).toHaveAttribute("aria-busy", "true");
      expect(feedback).toHaveClass("sync-feedback");
      expect(feedback).not.toHaveClass("visually-hidden");
      expect(feedback).toHaveTextContent("Starting sync...");
      await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
      expect(feedback).toBeEmptyDOMElement();
      expect(button).toBeDisabled();
      expect(form).toHaveAttribute("aria-busy", "true");
      fireEvent.submit(form);
      expect(posts).toBe(1);
      expect(body).toEqual({
        sourceIds: [SOURCE_A],
        startDate: "2022-01-01",
        endDate: "2022-12-31",
        reprocessAllFiles: false,
      });

      await act(async () => resolvePost(json({ jobId: JOB_ID, status, reused }, 202)));
      await waitFor(() => expect(feedback).toHaveTextContent(confirmation));
      expect(within(feedback).getByRole("button", { name: "Dismiss" })).toBeEnabled();
      expect(view.navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
      view.rerenderJob(JOB_ID);
      expect(screen.getByText("Loading run detail...")).toBeVisible();
      expect(button).toBeDisabled();
      expect(reprocess).toBeDisabled();
      expect(form).toHaveAttribute("aria-busy", "true");
      fireEvent.submit(form);
      await userEvent.click(button);
      expect(posts).toBe(1);

      await act(async () => resolveDetail(json({ ...detail, status, terminalAt: undefined })));
      const region = screen.getByRole("region", { name: "Selected run" });
      expect(
        await within(region).findByText(status === "queued" ? "Queued" : "Running", {
          selector: ".sync-status",
        }),
      ).toBeVisible();
      await waitFor(() => expect(screen.getByRole("button", { name: "Start sync" })).toBeEnabled());
      expect(reprocess).toBeEnabled();
      expect(form).toHaveAttribute("aria-busy", "false");
      expect(feedback).toHaveTextContent(confirmation);
      expect(detailReads).toBe(1);
      expect(posts).toBe(1);
    },
  );

  test("dismisses only the accepted confirmation through detail handoff and polling until a fresh submission", async () => {
    let resolveDetail!: (response: Response) => void;
    const detailResponse = new Promise<Response>((resolve) => {
      resolveDetail = resolve;
    });
    let detailReads = 0;
    const fetchMock = baseFetch((path) => {
      if (path === "/api/ingest")
        return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
      if (path === `/api/jobs/${JOB_ID}`) {
        detailReads += 1;
        return detailReads === 1
          ? detailResponse
          : json({ ...detail, status: "queued", terminalAt: undefined });
      }
      return undefined;
    });
    const view = renderDataSync();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    await within(feedback).findByText("Manual sync job has been queued.");
    await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
    expect(feedback).toBeEmptyDOMElement();
    expect(feedback).toHaveClass("visually-hidden");
    const starting = screen.getByRole("button", { name: "Starting sync..." });
    expect(starting).toBeDisabled();
    fireEvent.submit(starting.closest("form")!);
    view.rerenderJob(JOB_ID);
    await act(async () =>
      resolveDetail(json({ ...detail, status: "queued", terminalAt: undefined })),
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Start sync" })).toBeEnabled());
    await act(async () => {
      await view.client.refetchQueries({ queryKey: ["data-sync"] });
      await view.client.refetchQueries({ queryKey: ["jobs"] });
      await view.client.refetchQueries({ queryKey: ["job", JOB_ID] });
    });
    expect(feedback).toBeEmptyDOMElement();
    expect(screen.getByText("Export is stale")).toBeVisible();
    expect(screen.getByRole("region", { name: "Selected run" })).toBeVisible();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await within(feedback).findByText("Manual sync job has been queued.");
    expect(within(feedback).getByRole("button", { name: "Dismiss" })).toBeEnabled();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(2);
  });

  test("does not replace submission feedback when the previously selected run finishes", async () => {
    const previousJobId = "F".repeat(32);
    let resolvePost!: (response: Response) => void;
    let resolveDetail!: (response: Response) => void;
    const postResponse = new Promise<Response>((resolve) => {
      resolvePost = resolve;
    });
    const detailResponse = new Promise<Response>((resolve) => {
      resolveDetail = resolve;
    });
    baseFetch((path) => {
      if (path === "/api/ingest") return postResponse;
      if (path === `/api/jobs/${JOB_ID}`) return detailResponse;
      if (path === `/api/jobs/${previousJobId}`)
        return json({ ...detail, id: previousJobId, status: "running", terminalAt: undefined });
      return undefined;
    });
    const view = renderDataSync(previousJobId);
    await screen.findByText("Running", { selector: ".sync-status" });
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    act(() => {
      view.client.setQueryData(["job", previousJobId], {
        ...detail,
        id: previousJobId,
        status: "succeeded",
      });
    });
    await screen.findByText("Completed", { selector: ".sync-status" });
    expect(feedback).toHaveTextContent("Starting sync...");
    expect(screen.getByRole("button", { name: "Starting sync..." })).toBeDisabled();
    await act(async () =>
      resolvePost(json({ jobId: JOB_ID, status: "queued", reused: false }, 202)),
    );
    await waitFor(() => expect(feedback).toHaveTextContent("Manual sync job has been queued."));
    view.rerenderJob(JOB_ID);
    expect(screen.getByRole("button", { name: "Starting sync..." })).toBeDisabled();
    await act(async () =>
      resolveDetail(json({ ...detail, status: "queued", terminalAt: undefined })),
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Start sync" })).toBeEnabled());
    expect(feedback).toHaveTextContent("Manual sync job has been queued.");
  });

  test("preserves acceptance feedback when the initial run detail request fails", async () => {
    let resolveDetail!: (response: Response) => void;
    const detailResponse = new Promise<Response>((resolve) => {
      resolveDetail = resolve;
    });
    let posts = 0;
    baseFetch((path) => {
      if (path === "/api/ingest") {
        posts += 1;
        return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
      }
      if (path === `/api/jobs/${JOB_ID}`) return detailResponse;
      return undefined;
    });
    const view = renderDataSync();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    await waitFor(() => expect(feedback).toHaveTextContent("Manual sync job has been queued."));
    view.rerenderJob(JOB_ID);
    expect(screen.getByRole("button", { name: "Starting sync..." })).toBeDisabled();
    await act(async () => resolveDetail(json({ title: "Service Unavailable" }, 503)));
    expect(await screen.findByText("Run detail is unavailable.")).toBeVisible();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start sync" })).toBeEnabled());
    expect(feedback).toHaveTextContent("Manual sync job has been queued.");
    expect(
      screen.queryByText("The sync could not be started. Please try again."),
    ).not.toBeInTheDocument();
    await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
    expect(feedback).toBeEmptyDOMElement();
    expect(screen.getByText("Run detail is unavailable.")).toBeVisible();
    expect(screen.getByText("Export is stale")).toBeVisible();
    await act(async () => {
      await view.client.refetchQueries({ queryKey: ["data-sync"] });
      await view.client.refetchQueries({ queryKey: ["jobs"] });
    });
    expect(feedback).toBeEmptyDOMElement();
    expect(screen.getByText("Run detail is unavailable.")).toBeVisible();
    expect(posts).toBe(1);
  });

  test("clears starting feedback and allows retry after a rejected submission", async () => {
    let posts = 0;
    baseFetch((path) => {
      if (path !== "/api/ingest") return undefined;
      posts += 1;
      return posts === 1
        ? json({ title: "Service Unavailable" }, 503)
        : json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
    });
    renderDataSync();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const error = await screen.findByText("The sync could not be started. Please try again.");
    await waitFor(() => expect(error).toHaveFocus());
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    expect(feedback).toBeEmptyDOMElement();
    expect(feedback).toHaveClass("visually-hidden");
    expect(within(feedback).queryByRole("button", { name: "Dismiss" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start sync" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() => expect(feedback).toHaveTextContent("Manual sync job has been queued."));
    expect(feedback).toHaveClass("sync-feedback");
    expect(
      screen.queryByText("The sync could not be started. Please try again."),
    ).not.toBeInTheDocument();
    expect(posts).toBe(2);
  });

  test("labels iCloud sources and excludes expired access from mixed-source sync", async () => {
    let body: unknown;
    baseFetch((path, init) => {
      if (path === "/api/data-sync")
        return json({
          ...snapshot,
          sources: snapshot.sources.map((source) =>
            source.id === SOURCE_A || source.id === SOURCE_C
              ? { ...source, type: "health-auto-export-icloud" }
              : source,
          ),
        });
      if (path === "/api/ingest") {
        body = JSON.parse(String(init?.body));
        return json({ jobId: JOB_ID, status: "queued", reused: false }, 202);
      }
      return undefined;
    });
    renderDataSync();
    const cloud = await screen.findByRole("checkbox", { name: /Phone exports/ });
    expect(cloud).toHaveAccessibleName(/Health Auto Export — iCloud Drive/);
    expect(cloud).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Old folder/ })).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox", { name: /Watch archive/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() => expect(body).toEqual({ sourceIds: [SOURCE_A, SOURCE_B] }));
  });

  test("renders persisted partial progress and does not fetch logs before disclosure", async () => {
    const fetchMock = baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`
        ? json({
            pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
            items: [
              {
                id: 1,
                jobId: JOB_ID,
                severity: "info",
                code: "import-finished",
                message: "Import finished safely",
                fields: { files: 2 },
                createdAt: "2026-08-06T12:05:00Z",
              },
            ],
          })
        : undefined,
    );
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(await within(region).findByText("Partially succeeded")).toBeInTheDocument();
    expect(within(region).getByRole("progressbar")).toHaveAttribute("value", "2");
    expect(within(region).getByText("2 of 4 files")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/logs?"))).toBe(false);
    await userEvent.click(within(region).getByRole("button", { name: /Logs/ }));
    expect(await within(region).findByText(/Import finished safely/)).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(
        ([input]) => String(input) === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`,
      ),
    ).toBe(true);
  });

  test.each([
    ["manual", 4, 0],
    ["manual", 0, 4],
    ["scheduled", 4, 0],
    ["scheduled", 0, 4],
  ] as const)(
    "renders API-projected historical %s partial success with %i imported and %i updated workouts",
    async (trigger, workoutsCreated, workoutsUpdated) => {
      const counters = { ...progress, workoutsCreated, workoutsUpdated };
      const run: JobDetail = {
        ...detail,
        operation: "data_sync",
        trigger,
        status: "failed",
        displayStatus: "partially_succeeded",
        progress: counters,
        results: counters,
        createdAt: "2024-03-01T12:00:00Z",
        updatedAt: "2024-03-01T12:05:00Z",
        terminalAt: "2024-03-01T12:05:00Z",
      };
      run.children = [
        {
          ...run,
          id: "F".repeat(32),
          parentJobId: JOB_ID,
          source: {
            sourceId: SOURCE_A,
            generation: 1,
            displayName: "Historical exports",
            sourceType: "health-auto-export-local",
          },
          children: [],
        },
      ];
      const warning = {
        ...snapshot.notifications[0],
        type: "ingest-partially-succeeded",
        severity: "warning" as const,
        state: "unresolved" as const,
        subjectType: "job" as const,
        sourceId: undefined,
        jobId: JOB_ID,
        title: "Sync partially succeeded",
        message: "The sync partially succeeded. Some workouts were saved, but some files failed.",
      };
      const retryJobId = "1".repeat(32);
      let retry: { body: string; csrf: string | null } | undefined;
      baseFetch((path, init) => {
        if (path === `/api/jobs/${JOB_ID}`) return json(run);
        if (path === "/api/data-sync") return json({ ...snapshot, notifications: [warning] });
        if (path === "/api/jobs?page=1&pageSize=25")
          return json({
            pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
            items: [run],
          });
        if (path === `/api/jobs/${JOB_ID}/retry`) {
          retry = {
            body: String(init?.body),
            csrf: new Headers(init?.headers).get("X-CSRF-Token"),
          };
          return json({ jobId: retryJobId, status: "queued", reused: false }, 202);
        }
        return undefined;
      });
      const { client, navigate } = renderDataSync(JOB_ID);
      const region = await screen.findByRole("region", { name: "Selected run" });
      const parentBadge = await within(region).findByText("Partially succeeded", {
        selector: ".job-detail-card > .sync-card-heading .sync-status",
      });
      expect(parentBadge).toHaveClass("sync-status--partially_succeeded");
      const child = region.querySelector<HTMLElement>(".source-run")!;
      expect(within(child).getByText("Partially succeeded")).toHaveClass(
        "sync-status--partially_succeeded",
      );
      expect(region.querySelector(".sync-status--failed, .sync-status--succeeded")).toBeNull();
      expect(within(region).getByText(trigger === "manual" ? "Manual" : "Scheduled")).toBeVisible();
      for (const label of ["Workouts Imported", "Workouts Updated"]) {
        const count = label === "Workouts Imported" ? workoutsCreated : workoutsUpdated;
        for (const node of within(region).getAllByText(label)) {
          expect(node.nextElementSibling).toHaveTextContent(String(count));
        }
      }
      await screen.findAllByText("Partially succeeded", { selector: ".sync-status-text" });
      for (const selector of [".sync-history-table", ".sync-history-cards"]) {
        const container = document.querySelector(selector)!;
        expect(container.querySelector(".sync-status-text--partially_succeeded")).toHaveTextContent(
          "Partially succeeded",
        );
        expect(
          container.querySelector(".sync-status-text--failed, .sync-status-text--succeeded"),
        ).toBeNull();
      }
      const message = await screen.findByText(warning.message);
      expect(message.closest("article")).toHaveClass("sync-notification--warning");
      expect(message.closest("article")).not.toHaveClass("sync-notification--error");
      expect(
        screen.queryByText("Completed", { selector: ".sync-status, .sync-status-text" }),
      ).toBeNull();
      expect(client.getQueryData<JobDetail>(["job", JOB_ID])?.status).toBe("failed");
      const retryButton = within(region).getByRole("button", { name: "Retry run" });
      expect(retryButton).toBeEnabled();
      await userEvent.click(retryButton);
      await waitFor(() => expect(retry).toEqual({ body: "{}", csrf: "csrf-data-sync" }));
      await waitFor(() => expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${retryJobId}`));
    },
  );

  test.each([
    ["failed", "failed", 0, 0, "Failed"],
    ["succeeded", "succeeded", 0, 0, "Completed"],
    ["failed", undefined, 4, 0, "Failed"],
    ["failed", undefined, 0, 4, "Failed"],
  ] as const)(
    "keeps stored %s with display status %s, %i imported and %i updated workouts labeled %s",
    async (status, displayStatus, workoutsCreated, workoutsUpdated, label) => {
      const counters: JobProgress = {
        current: 0,
        total: 0,
        filesDiscovered: 0,
        filesSkipped: 0,
        filesSucceeded: 0,
        filesFailed: 0,
        workoutsCreated,
        workoutsUpdated,
        workoutsUnchanged: 0,
        workoutsRejected: 0,
      };
      const run: JobDetail = {
        ...detail,
        status,
        displayStatus,
        progress: counters,
        results: counters,
      };
      run.children = [{ ...run, id: "F".repeat(32), parentJobId: JOB_ID, children: [] }];
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}`) return json(run);
        if (path === "/api/jobs?page=1&pageSize=25")
          return json({
            pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
            items: [run],
          });
        return undefined;
      });
      renderDataSync(JOB_ID);
      const region = await screen.findByRole("region", { name: "Selected run" });
      const badges = await within(region).findAllByText(label, { selector: ".sync-status" });
      expect(badges).toHaveLength(2);
      for (const badge of badges) expect(badge).toHaveClass(`sync-status--${status}`);
      await screen.findAllByText(label, { selector: ".sync-status-text" });
      for (const selector of [".sync-history-table", ".sync-history-cards"]) {
        expect(
          document.querySelector(`${selector} .sync-status-text--${status}`),
        ).toHaveTextContent(label);
      }
      expect(document.querySelector(".sync-status--partially_succeeded")).toBeNull();
      expect(document.querySelector(".sync-status-text--partially_succeeded")).toBeNull();
      if (status === "failed") {
        expect(within(region).getByRole("button", { name: "Retry run" })).toBeEnabled();
        expect(
          document.querySelector(".sync-status--succeeded, .sync-status-text--succeeded"),
        ).toBeNull();
      } else {
        expect(
          within(region).getAllByText("Sync job completed, but no new data were imported."),
        ).toHaveLength(2);
        expect(within(region).queryByRole("button", { name: "Retry run" })).toBeNull();
      }
    },
  );

  test.each([false, true])(
    "dismisses retry acceptance feedback (reused: %s) without repeating the command",
    async (reused) => {
      const retryId = "F".repeat(32);
      let retries = 0;
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}/retry`) {
          retries++;
          return json({ jobId: retryId, status: "queued", reused }, 202);
        }
        return undefined;
      });
      const { client, navigate } = renderDataSync(JOB_ID);
      const retry = await screen.findByRole("button", { name: "Retry run" });
      await userEvent.click(retry);
      const feedback = screen.getByRole("status", { name: "Sync feedback" });
      const message = reused ? "Existing retry selected." : "Retry accepted.";
      await waitFor(() => expect(feedback).toHaveTextContent(message));
      expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${retryId}`);
      await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
      expect(feedback).toBeEmptyDOMElement();
      await act(async () => {
        await client.refetchQueries({ queryKey: ["data-sync"] });
        await client.refetchQueries({ queryKey: ["jobs"] });
        await client.refetchQueries({ queryKey: ["job", JOB_ID] });
      });
      expect(feedback).toBeEmptyDOMElement();
      expect(retries).toBe(1);
      await waitFor(() => expect(retry).toBeEnabled());
      await userEvent.click(retry);
      await waitFor(() => expect(feedback).toHaveTextContent(message));
      expect(within(feedback).getByRole("button", { name: "Dismiss" })).toBeEnabled();
      expect(retries).toBe(2);
    },
  );

  test.each([
    [
      "retry",
      409,
      "The retry was not started. Review the refreshed run details for retry availability.",
    ],
    ["retry", 503, "The retry could not be started."],
    ["cancellation", 409, "The run changed before cancellation could be requested."],
    ["cancellation", 503, "Cancellation could not be requested."],
  ] as const)("dismisses %s %i action feedback locally", async (command, status, message) => {
    let posts = 0;
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}` && command === "cancellation")
        return json({ ...detail, status: "running", terminalAt: undefined });
      if (path === `/api/jobs/${JOB_ID}/${command}`) {
        posts++;
        return json({ title: "Request failed" }, status);
      }
      return undefined;
    });
    const { client } = renderDataSync(JOB_ID);
    await userEvent.click(
      await screen.findByRole("button", {
        name: command === "retry" ? "Retry run" : "Cancel run",
      }),
    );
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    await waitFor(() => expect(feedback).toHaveTextContent(message));
    await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
    expect(feedback).toBeEmptyDOMElement();
    await act(async () => client.refetchQueries({ queryKey: ["job", JOB_ID] }));
    expect(feedback).toBeEmptyDOMElement();
    expect(posts).toBe(1);
  });

  test("dismisses completion feedback without hiding the completed run or restoring the message on polling", async () => {
    let completed = false;
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}`
        ? json({ ...detail, status: completed ? "succeeded" : "running" })
        : undefined,
    );
    const { client } = renderDataSync(JOB_ID);
    await screen.findByRole("button", { name: "Cancel run" });
    completed = true;
    await act(async () => client.refetchQueries({ queryKey: ["job", JOB_ID] }));
    const feedback = screen.getByRole("status", { name: "Sync feedback" });
    await waitFor(() => expect(feedback).toHaveTextContent("Run completed."));
    await userEvent.click(within(feedback).getByRole("button", { name: "Dismiss" }));
    expect(feedback).toBeEmptyDOMElement();
    expect(
      within(screen.getByRole("region", { name: "Selected run" })).getByText("Completed", {
        selector: ".sync-status",
      }),
    ).toBeVisible();
    await act(async () => client.refetchQueries({ queryKey: ["job", JOB_ID] }));
    expect(feedback).toBeEmptyDOMElement();
  });

  test("dismisses notification-dismissal errors without dismissing or retrying the notification", async () => {
    let posts = 0;
    baseFetch((path) => {
      if (path.endsWith("/dismissal")) {
        posts++;
        return json({ title: "Service Unavailable" }, 503);
      }
      return undefined;
    });
    renderDataSync();
    const notice = (await screen.findByText("Export is stale")).closest("article")!;
    await userEvent.click(within(notice).getByRole("button", { name: "Dismiss" }));
    const error = await screen.findByRole("alert");
    expect(error).toHaveTextContent("The notification could not be dismissed.");
    await userEvent.click(within(error).getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("Export is stale")).toBeVisible();
    expect(posts).toBe(1);
    await userEvent.click(within(notice).getByRole("button", { name: "Dismiss" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The notification could not be dismissed.",
    );
    expect(posts).toBe(2);
  });

  test("dismisses reminders with an empty body and server pagination stays authoritative", async () => {
    let dismissal: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path.includes("page=2&pageSize=25"))
        return json({
          pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 2 },
          items: [],
        });
      if (path.endsWith("/dismissal")) {
        dismissal = {
          body: String(init?.body),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        };
        return json({ ...snapshot.notifications[0], state: "dismissed" });
      }
      if (path === "/api/jobs?page=1&pageSize=25")
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 26, totalPages: 2 },
          items: [
            {
              id: JOB_ID,
              trigger: "manual",
              status: "partially_succeeded",
              progress,
              createdAt: detail.createdAt,
              updatedAt: detail.updatedAt,
            },
          ],
        });
      return undefined;
    });
    renderDataSync();
    expect(await screen.findByText("Export is stale")).toBeInTheDocument();
    expect(await screen.findAllByText("Partially succeeded")).toHaveLength(2);
    expect(screen.queryByText("Partially completed")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(dismissal).toEqual({ body: "{}", csrf: "csrf-data-sync" }));
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("No tasks match these filters.")).toBeInTheDocument();
  });

  test("retains task rows, table geometry, and scroll position while a new page loads", async () => {
    const pageTwoJobId = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF";
    let resolvePageTwo!: (response: Response) => void;
    const deferredPageTwo = new Promise<Response>((resolve) => {
      resolvePageTwo = resolve;
    });
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path === "/api/data-sync") return Promise.resolve(json(snapshot));
      if (path === "/api/jobs?page=1&pageSize=25")
        return Promise.resolve(
          json({
            pagination: { page: 1, pageSize: 25, totalItems: 26, totalPages: 2 },
            items: [
              {
                id: JOB_ID,
                trigger: "manual",
                status: "partially_succeeded",
                progress,
                createdAt: detail.createdAt,
                updatedAt: detail.updatedAt,
              },
            ],
          }),
        );
      if (path === "/api/jobs?page=2&pageSize=25") return deferredPageTwo;
      throw new Error(`Unhandled fetch: ${path}`);
    });
    renderDataSync();
    const next = await screen.findByRole("button", { name: "Next" });
    const table = document.querySelector<HTMLTableElement>(".sync-history-table table")!;
    const rows = within(table).getAllByRole("row").slice(1);
    const results = table.closest<HTMLElement>(".sync-history-results")!;
    vi.spyOn(results, "getBoundingClientRect").mockReturnValue({
      x: 0,
      y: 0,
      width: 900,
      height: 420,
      top: 0,
      right: 900,
      bottom: 420,
      left: 0,
      toJSON: () => ({}),
    });
    const scrollTo = vi.spyOn(window, "scrollTo");
    await userEvent.click(next);
    expect(document.querySelector(".sync-history-table table")).toBe(table);
    expect(within(table).getAllByRole("row").slice(1)).toEqual(rows);
    expect(results).toHaveAttribute("aria-busy", "true");
    expect(results).toHaveStyle({ minHeight: "420px" });
    expect(screen.queryByText("Loading task history...")).not.toBeInTheDocument();
    resolvePageTwo(
      json({
        pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 2 },
        items: [
          {
            id: pageTwoJobId,
            trigger: "automatic",
            status: "succeeded",
            progress,
            createdAt: "2026-08-07T12:00:00Z",
            updatedAt: "2026-08-07T12:05:00Z",
          },
        ],
      }),
    );
    expect(
      await screen.findByText("Page 2 of 2", { selector: ".pagination span" }),
    ).toBeInTheDocument();
    await waitFor(() => expect(within(table).getByText("Completed")).toBeVisible());
    await waitFor(() => expect(scrollTo).toHaveBeenCalledWith(0, 0));
  });

  test("keeps embedded notifications authoritative, marks truncation statically, and links only job notifications", async () => {
    const navigate = vi.fn();
    const jobNotification = {
      ...snapshot.notifications[0],
      id: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
      subjectType: "job" as const,
      sourceId: undefined,
      jobId: JOB_ID,
      title: "Workout deletion failed",
      message: "The workout could not be deleted, but you can retry the task.",
    };
    baseFetch((path) =>
      path === "/api/data-sync"
        ? json({
            ...snapshot,
            notifications: [{ ...snapshot.notifications[0], jobId: JOB_ID }, jobNotification],
            notificationsTruncated: true,
          })
        : undefined,
    );
    renderDataSync(undefined, navigate);
    const stale = await screen.findByText("Export is stale");
    expect(
      screen.getByText("The workout could not be deleted, but you can retry the task."),
    ).toBeInTheDocument();
    expect(stale.closest("article")).not.toHaveAttribute("role");
    expect(screen.getByText("More notifications are available.")).not.toHaveAttribute("role");
    expect(screen.getAllByRole("button", { name: "View detail" })).toHaveLength(1);
    expect(
      vi.mocked(fetch).mock.calls.some(([input]) => String(input).startsWith("/api/notifications")),
    ).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "View detail" }));
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
  });

  test("expands source failure diagnostics without navigating to an internal check job", async () => {
    const failedSource = {
      ...snapshot.sources[2],
      type: "health-auto-export-icloud",
      statusCode: "source-auth-expired",
      statusSummary: "Source authentication expired; reauthentication is required",
      checkedAt: "2026-10-16T23:04:46Z",
    };
    const failedSnapshot = {
      ...snapshot,
      sources: [failedSource],
      notifications: [
        {
          ...snapshot.notifications[0],
          type: "source-connection-failed",
          severity: "error",
          state: "unresolved",
          jobId: JOB_ID,
          title: "Source connection failed",
          message: "The source connection could not be verified.",
        },
      ],
    };
    const fetchMock = baseFetch((path) =>
      path === "/api/data-sync" ? json(failedSnapshot) : undefined,
    );
    const { client, navigate } = renderDataSync();
    const button = await screen.findByRole("button", { name: "View detail" });
    const banner = button.closest("article")!;
    expect(within(banner).queryByText(failedSource.statusSummary)).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /Old folder/ })).toHaveAccessibleName(
      /Source authentication expired/,
    );
    expect(button).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    const diagnostics = document.getElementById(button.getAttribute("aria-controls")!)!;
    expect(within(diagnostics).getByText(failedSource.statusSummary)).toBeVisible();
    expect(within(diagnostics).getByText("source-auth-expired")).toBeVisible();
    expect(within(diagnostics).getByText(/Last checked:/)).not.toHaveTextContent("Never");
    expect(within(diagnostics).getByText(/Reconnect with Rclone/)).toBeVisible();
    expect(navigate).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls.some(([input]) => String(input) === `/api/jobs/${JOB_ID}`)).toBe(
      false,
    );
    expect(screen.queryByText("Loading run detail...")).not.toBeInTheDocument();
    expect(screen.queryByText("Run detail is unavailable.")).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Selected run" })).not.toBeInTheDocument();

    act(() => {
      client.setQueryData(["data-sync"], {
        ...failedSnapshot,
        sources: [
          {
            ...failedSource,
            status: "checking-connection",
            statusCode: undefined,
            statusSummary: undefined,
            checkedAt: undefined,
          },
        ],
      });
    });
    expect(await within(diagnostics).findByText(/Checking connection/)).toBeVisible();
    expect(within(diagnostics).queryByText("source-auth-expired")).not.toBeInTheDocument();
    expect(within(diagnostics).queryByText(/Reconnect with Rclone/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Hide detail" }));
    expect(document.getElementById(button.getAttribute("aria-controls")!)).toBeNull();
  });

  test("handles a removed source notification without following its retained job reference", async () => {
    baseFetch((path) =>
      path === "/api/data-sync"
        ? json({
            ...snapshot,
            sources: [],
            notifications: [
              {
                ...snapshot.notifications[0],
                type: "source-connection-failed",
                jobId: JOB_ID,
              },
            ],
          })
        : undefined,
    );
    const { navigate } = renderDataSync();
    await userEvent.click(await screen.findByRole("button", { name: "View detail" }));
    expect(screen.getByText("This source is no longer available.")).toBeVisible();
    expect(navigate).not.toHaveBeenCalled();
  });

  test("clears an unavailable run selection and hides its panel without retrying", async () => {
    vi.useFakeTimers();
    let snapshotReads = 0;
    let detailReads = 0;
    baseFetch((path) => {
      if (path === "/api/data-sync") {
        snapshotReads += 1;
        return json(snapshot);
      }
      if (path === `/api/jobs/${JOB_ID}`) {
        detailReads += 1;
        return json({ title: "Not Found", detail: "job was not found" }, 404);
      }
      return undefined;
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(view.navigate).toHaveBeenCalledWith("/data-sync", true);
      expect(screen.queryByRole("region", { name: "Selected run" })).not.toBeInTheDocument();
      expect(screen.queryByText("Run detail is unavailable.")).not.toBeInTheDocument();
      expect(detailReads).toBe(1);
      await act(async () => {
        focusManager.setFocused(false);
        focusManager.setFocused(true);
        onlineManager.setOnline(false);
        onlineManager.setOnline(true);
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(detailReads).toBe(1);
      expect(snapshotReads).toBeGreaterThan(1);
      expect(view.navigate).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("region", { name: "Selected run" })).not.toBeInTheDocument();
      expect(screen.queryByText("Loading run detail...")).not.toBeInTheDocument();
    } finally {
      view.unmount();
      focusManager.setFocused(undefined);
      onlineManager.setOnline(true);
      vi.useRealTimers();
    }
  });

  test("hides an unavailable automatic run fallback when no run was selected", async () => {
    baseFetch((path) => {
      if (path === "/api/data-sync") return json({ ...snapshot, latestJob: detail });
      if (path === `/api/jobs/${JOB_ID}`) return json({ title: "Not Found" }, 404);
      return undefined;
    });
    const { client, navigate } = renderDataSync();
    await waitFor(() => expect(client.getQueryState(["job", JOB_ID])?.status).toBe("error"));
    expect(screen.queryByRole("region", { name: "Selected run" })).not.toBeInTheDocument();
    expect(screen.queryByText("Run detail is unavailable.")).not.toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
  });

  test("continues polling an active run after transient failures exhaust request retries", async () => {
    vi.useFakeTimers();
    let detailReads = 0;
    baseFetch((path) => {
      if (path !== `/api/jobs/${JOB_ID}`) return undefined;
      detailReads += 1;
      if (detailReads > 1 && detailReads <= 5) {
        return json({ title: "Service Unavailable" }, 503);
      }
      return json({
        ...detail,
        status: detailReads === 1 ? "running" : "succeeded",
      });
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      const region = screen.getByRole("region", { name: "Selected run" });
      expect(within(region).getByText("Running")).toBeVisible();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(12000);
      });
      expect(detailReads).toBe(6);
      expect(within(region).getByText("Completed")).toBeVisible();
      expect(within(region).queryByText("Run detail is unavailable.")).not.toBeInTheDocument();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(detailReads).toBe(6);
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });

  test("shows status-aware zero progress, labels creation as queued, and omits the API attempt", async () => {
    const zero = {
      ...progress,
      current: 0,
      total: 0,
      filesDiscovered: 0,
      filesSucceeded: 0,
      filesFailed: 0,
      workoutsCreated: 0,
      workoutsUpdated: 0,
      workoutsUnchanged: 0,
      workoutsRejected: 0,
    };
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}`
        ? json({ ...detail, status: "succeeded", attempt: 4, progress: zero, results: {} })
        : undefined,
    );
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(
      await within(region).findByText("Sync job completed, but no new data were imported."),
    ).toBeInTheDocument();
    expect(within(region).queryByRole("progressbar")).not.toBeInTheDocument();
    expect(within(region).queryByText(/Discovering files/)).not.toBeInTheDocument();
    const queued = [...region.querySelectorAll(".job-metadata dt")]
      .find((node) => node.textContent === "Queued")!
      .closest("div")!;
    expect(queued.querySelector("dd")).not.toHaveTextContent("Never");
    expect(within(region).queryByText("Attempt")).not.toBeInTheDocument();
    expect(within(region).queryByText(/Retry of job/)).not.toBeInTheDocument();
    expect(region.querySelector(".job-actions")).not.toBeInTheDocument();
    expect(region).not.toHaveFocus();
  });

  test("distinguishes original queue time from a previously started Coverage parent waiting again", async () => {
    const waiting = {
      ...detail,
      operation: "coverage_update" as const,
      trigger: "system" as const,
      status: "queued" as const,
      startedAt: "2026-08-06T12:01:00Z",
      updatedAt: "2026-08-06T12:10:00Z",
      routeStats: {
        total: 3,
        processed: 1,
        running: 0,
        succeeded: 1,
        failed: 0,
        cancelled: 0,
        superseded: 0,
      },
    };
    baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(waiting) : undefined));
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Waiting since");
    const queued = [...region.querySelectorAll(".job-metadata dt")]
      .find((node) => node.textContent === "Queued")!
      .closest("div")!;
    const started = within(region).getByText("First started").closest("div")!;
    const waitingSince = within(region).getByText("Waiting since").closest("div")!;
    expect(queued.querySelector("dd")).not.toHaveTextContent("Never");
    expect(started.querySelector("dd")).not.toHaveTextContent("Never");
    expect(waitingSince.querySelector("dd")).not.toHaveTextContent("Never");
    expect(waitingSince.querySelector("dd")?.textContent).not.toBe(
      queued.querySelector("dd")?.textContent,
    );
  });

  test.each([
    [1, "First", ""],
    [2, "Second", "first"],
    [5, "Fifth", "fourth"],
  ] as const)(
    "renders retry ordinal %i with root and previous-job navigation",
    async (retryOrdinal, ordinalLabel, previousLabel) => {
      const rootJobId = "ABCDEF1234567890ABCDEF1234567890";
      const previousJobId = "1234567890ABCDEF1234567890ABCDEF";
      const navigate = vi.fn();
      baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}`
          ? json({
              ...detail,
              retryRootJobId: rootJobId,
              retryOrdinal,
              retryOfJobId: retryOrdinal > 1 ? previousJobId : rootJobId,
              latestRetryJobId:
                retryOrdinal > 1 && retryOrdinal < 5
                  ? "FEDCBA0987654321FEDCBA0987654321"
                  : undefined,
              latestRetryOrdinal: retryOrdinal > 1 && retryOrdinal < 5 ? 5 : undefined,
            })
          : undefined,
      );
      renderDataSync(JOB_ID, navigate);
      const region = await screen.findByRole("region", { name: "Selected run" });
      if (retryOrdinal === 1) {
        expect(within(region).queryByText("Attempt")).not.toBeInTheDocument();
        expect(within(region).queryByText(/Retry of job|Retry by job/)).not.toBeInTheDocument();
        return;
      }
      const link = await within(region).findByRole("link", { name: rootJobId.slice(0, 8) });
      expect(link).toHaveAttribute("href", `/data-sync/jobs/${rootJobId}`);
      expect(link.closest("dt")).toHaveTextContent(`Retry of job ${rootJobId.slice(0, 8)}`);
      expect(link.closest("div")).toHaveTextContent(ordinalLabel);
      expect(link.closest("div")?.querySelector("dd")).toHaveClass("retry-ordinal");
      expect(within(region).queryByText(/Retry by job/)).not.toBeInTheDocument();
      if (retryOrdinal > 1) {
        const previous = within(region).getByRole("link", { name: previousLabel });
        expect(previous).toHaveAttribute("href", `/data-sync/jobs/${previousJobId}`);
        expect(previous).toHaveTextContent(previousLabel);
        expect(previous).not.toHaveTextContent("view");
        expect(previous.closest("small")).toHaveClass("retry-previous");
        expect(previous.closest("small")).toHaveTextContent(`| view ${previousLabel}`);
        await userEvent.click(previous);
        expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${previousJobId}`);
      } else {
        expect(within(region).queryByRole("link", { name: /view/ })).not.toBeInTheDocument();
      }
      await userEvent.click(link);
      expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${rootJobId}`);
      expect(within(region).queryByText("Attempt")).not.toBeInTheDocument();
    },
  );

  test("renders coverage route outcomes, counters, and ingest-style retry navigation", async () => {
    const first = "11111111111111111111111111111111";
    const third = "33333333333333333333333333333333";
    const coverageJob: JobDetail = {
      ...detail,
      operation: "coverage_update",
      trigger: "system",
      retryOrdinal: 4,
      retryRootJobId: first,
      retryOfJobId: third,
      routeStats: {
        total: 3,
        processed: 3,
        running: 0,
        succeeded: 1,
        failed: 1,
        cancelled: 0,
        superseded: 1,
      },
      coverage: {
        regionId: "geofabrik:test",
        targetOsmGeneration: 2,
        targetWorkRevision: 4,
        rulesVersion: "coverage-experimental-v1",
        samplingVersion: "coverage-sampling-experimental-v1",
        pathPolicyVersion: "coverage-path-policy-experimental-v82",
      },
      children: [
        {
          ...detail,
          id: "44444444444444444444444444444444",
          operation: "coverage_update",
          trigger: "system",
          status: "succeeded",
          results: undefined,
          routeStats: undefined,
          children: [],
          coverageRoute: {
            workoutId: "55555555555555555555555555555555",
            startedAt: "2026-08-05T08:00:00Z",
            localStartDate: "2026-08-05",
            workoutType: "Outdoor Run",
            resultOutcome: "applied",
            durationMilliseconds: 1234,
            targetGenerations: [
              { regionId: "geofabrik:north", generation: 7, latest: true },
              { regionId: "geofabrik:north", generation: 2, latest: false },
              { regionId: "geofabrik:south", generation: 9, latest: false },
            ],
          },
        },
      ],
    };
    baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(coverageJob) : undefined));
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(
      await within(region).findByRole("heading", { name: "Coverage update" }),
    ).toBeInTheDocument();
    expect(within(region).getByText("Routes Processed").closest("div")).toHaveTextContent("3");
    expect(within(region).getByRole("heading", { name: "Route runs" })).toBeInTheDocument();
    expect(within(region).getByText("Outdoor Run")).toBeInTheDocument();
    const routeRun = region.querySelector(".coverage-route-run")!;
    const routeDate = new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeZone: "UTC",
    }).format(new Date("2026-08-05T00:00:00Z"));
    expect(routeRun.querySelector(".coverage-route-title")).toHaveTextContent(
      `Outdoor Run (${routeDate})`,
    );
    const generations = within(routeRun as HTMLElement).getByRole("table", {
      name: "OSM generations",
    });
    expect(
      [...generations.querySelectorAll("tr")].map((row) =>
        [...row.querySelectorAll("td")].map((cell) => cell.textContent),
      ),
    ).toEqual([
      ["geofabrik:north", "7"],
      ["geofabrik:north", "2"],
      ["geofabrik:south", "9"],
    ]);
    expect(within(generations).getByText("7")).toHaveClass("is-latest");
    expect(within(generations).getByText("2")).not.toHaveClass("is-latest");
    expect(within(region).getByText(/applied \/ 1,234 ms/)).toBeInTheDocument();
    expect(within(region).queryByText("Attempt")).not.toBeInTheDocument();
    expect(within(region).getByRole("link", { name: first.slice(0, 8) })).toHaveAttribute(
      "href",
      `/data-sync/jobs/${first}`,
    );
    expect(within(region).getByText("Fourth")).toBeVisible();
    expect(within(region).getByRole("link", { name: "third" })).toHaveAttribute(
      "href",
      `/data-sync/jobs/${third}`,
    );
    expect(within(region).queryByRole("button", { name: "Files" })).not.toBeInTheDocument();
  });

  test.each([
    {
      regionIds: ["geofabrik:north", "geofabrik:south", "geofabrik:north"],
      label: "geofabrik:north, geofabrik:south",
    },
    { regionIds: ["geofabrik:north"], label: "geofabrik:north" },
    { regionIds: undefined, label: "geofabrik:anchor" },
    { regionIds: [], label: "geofabrik:anchor" },
  ])(
    "renders Coverage region scope with older API fallback: $label",
    async ({ regionIds, label }) => {
      const coverageJob: JobDetail = {
        ...detail,
        operation: "coverage_update",
        trigger: "system",
        coverage: {
          regionId: "geofabrik:anchor",
          regionIds,
          targetOsmGeneration: 1,
          targetWorkRevision: 1,
          rulesVersion: "coverage-experimental-v1",
          samplingVersion: "coverage-sampling-experimental-v1",
          pathPolicyVersion: "coverage-path-policy-experimental-v82",
        },
        children: [],
      };
      baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(coverageJob) : undefined));
      renderDataSync(JOB_ID);
      const region = await screen.findByRole("region", { name: "Selected run" });
      const scope = await within(region).findByText("Regions");
      expect(scope.closest("div")?.querySelector("dd")).toHaveTextContent(label);
      if (regionIds?.length) {
        expect(within(region).queryByText("geofabrik:anchor")).not.toBeInTheDocument();
      }
    },
  );

  test("distinguishes matching Coverage parents from parents waiting for the matcher", async () => {
    const matching = {
      ...detail,
      id: "1".repeat(32),
      operation: "coverage_update" as const,
      trigger: "system" as const,
      status: "running" as const,
      routeStats: {
        total: 3,
        processed: 1,
        running: 1,
        succeeded: 1,
        failed: 0,
        cancelled: 0,
        superseded: 0,
      },
    };
    const waiting = {
      ...matching,
      id: "2".repeat(32),
      status: "queued" as const,
      routeStats: { ...matching.routeStats, total: 2, processed: 0, running: 0, succeeded: 0 },
    };
    let historyReads = 0;
    baseFetch((path) => {
      if (path !== "/api/jobs?page=1&pageSize=25") return undefined;
      historyReads++;
      return json({
        pagination: { page: 1, pageSize: 25, totalItems: 2, totalPages: 1 },
        items:
          historyReads === 1
            ? [matching, waiting]
            : [
                {
                  ...matching,
                  status: "queued" as const,
                  routeStats: { ...matching.routeStats, running: 0 },
                },
                {
                  ...waiting,
                  status: "running" as const,
                  routeStats: { ...waiting.routeStats, running: 1 },
                },
              ],
      });
    });
    renderDataSync(undefined, vi.fn(), 1);
    const history = screen.getByRole("region", { name: "Recent activity" });
    await within(history).findAllByText("1 of 3 routes");
    const table = history.querySelector(".sync-history-table") as HTMLElement;
    const matchingRow = within(table).getByText("1 of 3 routes").closest("tr")!;
    const waitingRow = within(table).getByText("0 of 2 routes").closest("tr")!;
    expect(within(matchingRow).getByText("Running")).toBeVisible();
    expect(within(waitingRow).getByText("Queued")).toBeVisible();
    await waitFor(() => expect(historyReads).toBeGreaterThanOrEqual(2), { timeout: 2000 });
    expect(within(matchingRow).getByText("Queued")).toBeVisible();
    expect(within(waitingRow).getByText("Running")).toBeVisible();
  });

  test("renders result counts in the approved DOM order with derived values and rejected help", async () => {
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}` ? json({ ...detail, results: undefined }) : undefined,
    );
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Files Processed");
    const counts = region.querySelector(".result-counts")!;
    expect([...counts.querySelectorAll("dt")].map((node) => node.textContent)).toEqual([
      "Files Processed",
      "Workouts Imported",
      "Workouts Unchanged",
      "Files Failed",
      "Workouts Updated",
      "Workouts Rejected",
    ]);
    expect([...counts.querySelectorAll("dd")].map((node) => node.textContent)).toEqual([
      "2",
      "3",
      "1",
      "1",
      "2",
      "1",
    ]);
    const rejected = within(counts as HTMLElement)
      .getByText("Workouts Rejected")
      .closest("div")!
      .querySelector("dd")!;
    expect(rejected).toHaveAccessibleDescription(
      "Workout records skipped while their source file continued processing.",
    );
    expect(rejected).not.toHaveAttribute("title");
  });

  test("renders source identity and type on one heading row with its status at the right", async () => {
    const child: JobDetail = {
      ...detail,
      id: "22222222222222222222222222222222",
      status: "succeeded",
      source: {
        sourceId: SOURCE_A,
        generation: 1,
        displayName: "Phone exports",
        sourceType: "health-auto-export-local",
      },
      children: [],
    };
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}` ? json({ ...detail, children: [child] }) : undefined,
    );
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Source runs");
    const heading = region.querySelector(".source-run-heading")!;
    expect(heading).toHaveTextContent("Phone exports (Health Auto Export — Server Path)");
    expect(heading.querySelector("strong")).toHaveTextContent("Phone exports");
    expect(heading.lastElementChild).toHaveClass("sync-status", "sync-status--succeeded");
    expect(heading.lastElementChild).toHaveTextContent("Completed");
    expect(heading.lastElementChild).toHaveStyle({ position: "relative", top: ".2rem" });
    expect(region.querySelector(".source-run .sync-progress")).toHaveStyle({
      margin: ".7rem 0 -.3rem 0",
    });
  });

  test("uses filled semantic detail statuses and explicit schedule states", async () => {
    const statuses = ["succeeded", "failed", "partially_succeeded", "cancelled"] as const;
    const children = statuses.map((status, index) => ({
      ...detail,
      id: String(index + 3).repeat(32),
      status,
      children: [],
    }));
    baseFetch((path) => {
      if (path === "/api/data-sync")
        return json({ ...snapshot, schedule: { ...snapshot.schedule, enabled: false } });
      if (path === `/api/jobs/${JOB_ID}`) return json({ ...detail, status: "succeeded", children });
      return undefined;
    });
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Source runs");
    for (const status of statuses)
      expect(region.querySelector(`.sync-status--${status}`)).toBeInTheDocument();
    expect(screen.getByText("Disabled", { selector: ".schedule-state" })).toHaveClass(
      "schedule-state--disabled",
    );
    expect(within(region).getByText("Canceled")).toHaveClass("sync-status--cancelled");
  });

  test("resets lazy artifacts when the selected job changes", async () => {
    const nextJob = "11111111111111111111111111111111";
    const logRequests: string[] = [];
    baseFetch((path) => {
      if (path === `/api/jobs/${nextJob}`)
        return json({ ...detail, id: nextJob, status: "failed" });
      if (path.includes("/logs?")) {
        logRequests.push(path);
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
          items: [],
        });
      }
      return undefined;
    });
    const view = renderDataSync(JOB_ID);
    const firstRegion = await screen.findByRole("region", { name: "Selected run" });
    await within(firstRegion).findByText("Partially succeeded");
    await userEvent.click(within(firstRegion).getByRole("button", { name: /Logs/ }));
    await within(firstRegion).findByText("No logs recorded.");
    view.rerenderJob(nextJob);
    const nextRegion = screen.getByRole("region", { name: "Selected run" });
    await within(nextRegion).findByText("Failed");
    expect(within(nextRegion).getByRole("button", { name: /Logs/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(logRequests).toEqual([`/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`]);
  });

  describe("Files failure filter", () => {
    function failedPage(page: number, count = 25, totalPages = 2): JobFileList {
      const data = artifactPage("files", page, count, totalPages) as JobFileList;
      return {
        ...data,
        items: data.items.map((file) => ({
          ...file,
          basename: `failed-${file.basename}`,
          state: "failed",
          failureSummary: "Invalid export",
        })),
      };
    }

    test.each(["manual", "scheduled"] as const)(
      "filters %s files on the server, resets page one, and keeps filtered pagination and caches separate",
      async (trigger) => {
        let resolveFiltered!: (response: Response) => void;
        const filtered = new Promise<Response>((resolve) => {
          resolveFiltered = resolve;
        });
        const fetchMock = baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}`)
            return json({ ...detail, trigger, progress: { ...progress, filesFailed: 30 } });
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`)
            return filtered;
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25&failuresOnly=true`)
            return json(failedPage(2, 5));
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`)
            return json(artifactPage("files", 2));
          if (path === `/api/jobs/${JOB_ID}/events?page=1&pageSize=25`)
            return json(artifactPage("events", 1));
          if (path === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`)
            return json(artifactPage("logs", 1));
          return undefined;
        });
        const { client } = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        await screen.findByText("files-page-1-row-0.json");
        const checkbox = screen.getByRole("checkbox", { name: "Show failures only" });
        const nav = screen.getByRole("navigation", { name: "Files pages" });
        const panel = nav.parentElement!;
        const results = panel.querySelector<HTMLElement>(".artifact-results")!;
        vi.spyOn(results, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 900, 500));
        expect(nav).toContainElement(checkbox);
        expect(checkbox).toBeEnabled();
        expect(checkbox).not.toBeChecked();
        const next = within(nav).getByRole("button", { name: "Next" });
        const previous = within(nav).getByRole("button", { name: "Previous" });
        await userEvent.click(next);
        await screen.findByText("files-page-2-row-0.json");
        await userEvent.click(checkbox);
        expect(checkbox).toBeChecked();
        expect(panel).toHaveAttribute("aria-busy", "true");
        expect(within(panel).getByText("Loading files...")).toBeVisible();
        expect(within(panel).queryByRole("listitem")).not.toBeInTheDocument();
        expect(within(panel).queryByText("No failed files recorded.")).not.toBeInTheDocument();
        expect(results).toHaveStyle({ minHeight: "500px" });
        expect(within(nav).getByRole("button", { name: "Next" })).toBe(next);
        expect(next).toBeDisabled();
        expect(previous).toBeDisabled();
        await act(async () => resolveFiltered(json(failedPage(1))));
        await screen.findByText("failed-files-page-1-row-0.json");
        expect(nav).toHaveTextContent("Page 1 of 2");
        expect(within(panel).getAllByRole("listitem")).toHaveLength(25);
        expect(previous).toBeDisabled();
        await userEvent.click(next);
        await screen.findByText("failed-files-page-2-row-0.json");
        expect(nav).toHaveTextContent("Page 2 of 2");
        expect(within(panel).getAllByRole("listitem")).toHaveLength(5);
        expect(next).toBeDisabled();
        expect(checkbox).toBeChecked();
        expect(client.getQueryData(["job-artifacts", JOB_ID, "files", 1])).toEqual(
          artifactPage("files", 1),
        );
        expect(client.getQueryData(["job-artifacts", JOB_ID, "files", 1, "failures-only"])).toEqual(
          failedPage(1),
        );
        await userEvent.click(checkbox);
        await screen.findByText("files-page-1-row-0.json");
        expect(nav).toHaveTextContent("Page 1 of 3");
        expect(checkbox).not.toBeChecked();
        expect(screen.queryByText("failed-files-page-2-row-0.json")).not.toBeInTheDocument();
        for (const label of ["Events", "Logs"]) {
          await userEvent.click(screen.getByRole("button", { name: new RegExp(`^${label}`) }));
          const records = screen.getByRole("navigation", { name: `${label} pages` }).parentElement!;
          expect(within(records).queryByRole("checkbox")).not.toBeInTheDocument();
        }
        expect(
          fetchMock.mock.calls.filter(([path]) => String(path).includes("failuresOnly")),
        ).toHaveLength(2);
        expect(
          fetchMock.mock.calls.some(([path]) => String(path).includes("failuresOnly=false")),
        ).toBe(false);
      },
    );

    test.each([
      ["empty run", "partially_succeeded", 0, 0],
      ["completed successful run", "succeeded", 1, 1],
      ["source connection failure with no files", "failed", 0, 0],
    ] as const)(
      "disables the unchecked filter for a %s",
      async (_label, status, count, totalPages) => {
        const fetchMock = baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}`)
            return json({
              ...detail,
              status,
              progress: { ...progress, filesFailed: 0 },
              results: {},
              failureSummary: status === "failed" ? "Source connection failed" : undefined,
            });
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1, count, totalPages));
          return undefined;
        });
        renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        await waitFor(() =>
          expect(
            screen.getByRole("navigation", { name: "Files pages" }).parentElement,
          ).toHaveAttribute("aria-busy", "false"),
        );
        const checkbox = screen.getByRole("checkbox", { name: "Show failures only" });
        expect(checkbox).toBeDisabled();
        expect(checkbox).not.toBeChecked();
        expect(screen.getByRole("navigation", { name: "Files pages" })).toContainElement(checkbox);
        await userEvent.click(checkbox);
        expect(fetchMock.mock.calls.some(([path]) => String(path).includes("failuresOnly"))).toBe(
          false,
        );
      },
    );

    test("enables the filter when a running run's global failed count updates, not from page rows or terminal status", async () => {
      let run: JobDetail = {
        ...detail,
        status: "running",
        progress: { ...progress, filesFailed: 0 },
        results: undefined,
      };
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}`) return json(run);
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1, 1, 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`)
          return json(failedPage(1, 1, 1));
        return undefined;
      });
      const { client } = renderDataSync(JOB_ID);
      await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
      await screen.findByText("files-page-1-row-0.json");
      const checkbox = screen.getByRole("checkbox", { name: "Show failures only" });
      expect(checkbox).toBeDisabled();
      run = { ...run, progress: { ...run.progress, filesFailed: 1 } };
      await act(async () => {
        await client.refetchQueries({ queryKey: ["job", JOB_ID] });
      });
      await waitFor(() => expect(checkbox).toBeEnabled());
      expect(checkbox).not.toBeChecked();
      await userEvent.click(checkbox);
      await screen.findByText("failed-files-page-1-row-0.json");
      expect(checkbox).toBeChecked();
    });

    test("refreshes loaded failure pagination as a running run grows from 25 to 26 failures", async () => {
      let run: JobDetail = {
        ...detail,
        status: "running",
        progress: { ...progress, filesFailed: 25 },
        results: undefined,
      };
      const fetchMock = baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}`) return json(run);
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`) {
          const data = failedPage(1, 25, run.progress.filesFailed === 25 ? 1 : 2);
          data.pagination.totalItems = run.progress.filesFailed;
          return json(data);
        }
        if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25&failuresOnly=true`)
          return json(failedPage(2, 1));
        if (path === `/api/jobs/${JOB_ID}/events?page=1&pageSize=25`)
          return json(artifactPage("events", 1));
        if (path === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`)
          return json(artifactPage("logs", 1));
        return undefined;
      });
      const { client } = renderDataSync(JOB_ID);
      for (const label of ["Files", "Events", "Logs"]) {
        await userEvent.click(await screen.findByRole("button", { name: new RegExp(`^${label}`) }));
      }
      await screen.findByText("files-page-1-row-0.json");
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      await screen.findByText("failed-files-page-1-row-0.json");
      const nav = screen.getByRole("navigation", { name: "Files pages" });
      const next = within(nav).getByRole("button", { name: "Next" });
      expect(nav).toHaveTextContent("Page 1 of 1");
      expect(next).toBeDisabled();
      await waitFor(() => expect(nav.parentElement).toHaveAttribute("aria-busy", "false"));
      const readsBefore = fetchMock.mock.calls.filter(([path]) =>
        String(path).includes("/files?"),
      ).length;

      run = { ...run, progress: { ...run.progress, filesFailed: 26 } };
      await act(async () => {
        await client.refetchQueries({ queryKey: ["job", JOB_ID] });
      });
      await waitFor(() => expect(next).toBeEnabled());
      expect(nav).toHaveTextContent("Page 1 of 2");
      expect(
        client.getQueryData<JobFileList>(["job-artifacts", JOB_ID, "files", 1, "failures-only"])
          ?.pagination.totalItems,
      ).toBe(26);
      expect(client.getQueryState(["job-artifacts", JOB_ID, "files", 1])?.isInvalidated).toBe(true);
      await userEvent.click(next);
      await screen.findByText("failed-files-page-2-row-0.json");
      expect(nav).toHaveTextContent("Page 2 of 2");
      expect(within(nav.parentElement!).getAllByRole("listitem")).toHaveLength(1);
      expect(screen.getByRole("checkbox", { name: "Show failures only" })).toBeChecked();
      expect(
        fetchMock.mock.calls
          .filter(([path]) => String(path).includes("/files?"))
          .slice(readsBefore, readsBefore + 2),
      ).toEqual([
        [`/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`, expect.anything()],
        [`/api/jobs/${JOB_ID}/files?page=2&pageSize=25&failuresOnly=true`, expect.anything()],
      ]);
      for (const kind of ["events", "logs"]) {
        expect(
          fetchMock.mock.calls.filter(([path]) => String(path).includes(`/${kind}?`)),
        ).toHaveLength(1);
      }
    });

    test.each([false, true])(
      "refreshes files on terminal transition with unchanged failed count (filtered: %s)",
      async (filtered) => {
        let run: JobDetail = {
          ...detail,
          status: "running",
          progress: { ...progress, filesFailed: 1 },
          results: undefined,
        };
        const filesUrl = `/api/jobs/${JOB_ID}/files?page=1&pageSize=25${filtered ? "&failuresOnly=true" : ""}`;
        const fetchMock = baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}`) return json(run);
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25` || path === filesUrl) {
            const data =
              filtered && path === filesUrl ? failedPage(1, 1, 1) : artifactPage("files", 1, 1, 1);
            data.items[0] = {
              ...data.items[0],
              basename: run.status === "running" ? "in-progress-export.json" : "final-export.json",
            };
            return json(data);
          }
          return undefined;
        });
        const { client } = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        await screen.findByText("in-progress-export.json");
        if (filtered) {
          await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
          await waitFor(() =>
            expect(
              screen.getByRole("navigation", { name: "Files pages" }).parentElement,
            ).toHaveAttribute("aria-busy", "false"),
          );
        }
        await waitFor(() =>
          expect(
            screen.getByRole("navigation", { name: "Files pages" }).parentElement,
          ).toHaveAttribute("aria-busy", "false"),
        );
        const readsBefore = fetchMock.mock.calls.filter(([path]) => path === filesUrl).length;
        run = { ...run, status: "partially_succeeded" };
        await act(async () => {
          await client.refetchQueries({ queryKey: ["job", JOB_ID] });
        });
        await screen.findByText("final-export.json");
        expect(fetchMock.mock.calls.filter(([path]) => path === filesUrl)).toHaveLength(
          readsBefore + 1,
        );
        await act(async () => {
          await client.refetchQueries({ queryKey: ["job", JOB_ID] });
        });
        await waitFor(() =>
          expect(fetchMock.mock.calls.filter(([path]) => path === filesUrl)).toHaveLength(
            readsBefore + 2,
          ),
        );
      },
    );

    test.each([false, true])(
      "refreshes the open file page with successful polls, not failed polls or closed panels (filtered: %s)",
      async (filtered) => {
        vi.useFakeTimers();
        let run: JobDetail = {
          ...detail,
          status: "running",
          progress: { ...progress, filesFailed: 30 },
          results: undefined,
        };
        let unavailable = false;
        const fetchMock = baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}`)
            return unavailable ? json({ title: "Unavailable" }, 503) : json(run);
          if (path.startsWith(`/api/jobs/${JOB_ID}/files?`)) {
            const url = new URL(path, "http://localhost");
            const page = Number(url.searchParams.get("page"));
            if (url.searchParams.has("failuresOnly")) {
              const data = failedPage(page, page === 1 ? 25 : 5);
              data.pagination.totalItems = 30;
              return json(data);
            }
            return json(artifactPage("files", page));
          }
          if (path.endsWith("/events?page=1&pageSize=25")) return json(artifactPage("events", 1));
          if (path.endsWith("/logs?page=1&pageSize=25")) return json(artifactPage("logs", 1));
          return undefined;
        });
        const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
        const view = renderDataSync(JOB_ID, vi.fn(), 1);
        const advance = async (milliseconds: number) => {
          await act(async () => {
            await vi.advanceTimersByTimeAsync(milliseconds);
          });
        };
        try {
          await advance(0);
          for (const name of ["Files", "Events", "Logs"]) {
            fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${name}`) }));
            await advance(0);
          }
          await advance(0);
          if (filtered) {
            fireEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
            await advance(0);
          }
          const nav = screen.getByRole("navigation", { name: "Files pages" });
          fireEvent.click(within(nav).getByRole("button", { name: "Next" }));
          await advance(20);
          const pageLabel = `Page 2 of ${filtered ? 2 : 3}`;
          expect(nav).toHaveTextContent(pageLabel);
          scroll.mockClear();
          const fileReads = () =>
            fetchMock.mock.calls.filter(([path]) => String(path).includes("/files?")).length;
          const previousReads = fileReads();
          run = {
            ...run,
            progress: { ...run.progress, filesSucceeded: run.progress.filesSucceeded + 1 },
          };
          await advance(1000);
          expect(fileReads()).toBe(previousReads + 1);
          expect(screen.getByText("Files Processed").nextElementSibling).toHaveTextContent(
            String(run.progress.filesSucceeded + run.progress.filesFailed),
          );
          expect(screen.getByRole("navigation", { name: "Files pages" })).toBe(nav);
          expect(nav).toHaveTextContent(pageLabel);
          const filter = screen.getByRole("checkbox", { name: "Show failures only" });
          if (filtered) expect(filter).toBeChecked();
          else expect(filter).not.toBeChecked();
          expect(scroll).not.toHaveBeenCalled();
          // Identical detail responses must still refresh Files, without a request loop.
          await advance(1000);
          expect(fileReads()).toBe(previousReads + 2);
          await advance(50);
          expect(fileReads()).toBe(previousReads + 2);
          unavailable = true;
          await advance(8000);
          expect(fileReads()).toBe(previousReads + 2);
          expect(nav).toHaveTextContent(pageLabel);
          expect(scroll).not.toHaveBeenCalled();
          for (const kind of ["events", "logs"])
            expect(
              fetchMock.mock.calls.filter(([path]) => String(path).includes(`/${kind}?`)),
            ).toHaveLength(1);
          fireEvent.click(screen.getByRole("button", { name: /^Files/ }));
          unavailable = false;
          await advance(10000);
          expect(fileReads()).toBe(previousReads + 2);
        } finally {
          view.unmount();
          vi.useRealTimers();
        }
      },
    );

    test("does not retain unfiltered rows or false empty results after a filtered request fails, and retries the filter", async () => {
      let resolveFiltered!: (response: Response) => void;
      let resolveUnfiltered!: (response: Response) => void;
      const unfiltered = new Promise<Response>((resolve) => {
        resolveUnfiltered = resolve;
      });
      const filtered = new Promise<Response>((resolve) => {
        resolveFiltered = resolve;
      });
      let reads = 0;
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) return unfiltered;
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`)
          return ++reads === 1 ? filtered : json(failedPage(1, 0, 0));
        return undefined;
      });
      renderDataSync(JOB_ID);
      await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
      await screen.findByText("files-page-1-row-0.json");
      await userEvent.click(
        within(screen.getByRole("navigation", { name: "Files pages" })).getByRole("button", {
          name: "Next",
        }),
      );
      expect(screen.getByText("files-page-1-row-0.json")).toBeVisible();
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      expect(screen.getByText("Loading files...")).toBeVisible();
      expect(screen.queryByText("files-page-1-row-0.json")).not.toBeInTheDocument();
      await act(async () => resolveFiltered(json({ title: "Service Unavailable" }, 503)));
      await screen.findByText("Files are unavailable.");
      await act(async () => resolveUnfiltered(json(artifactPage("files", 2))));
      expect(screen.queryByText("files-page-1-row-0.json")).not.toBeInTheDocument();
      expect(screen.queryByText("files-page-2-row-0.json")).not.toBeInTheDocument();
      expect(screen.queryByText(/No (failed )?files recorded/)).not.toBeInTheDocument();
      expect(screen.getByRole("checkbox", { name: "Show failures only" })).toBeChecked();
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      await screen.findByText("No failed files recorded.");
      expect(reads).toBe(2);
    });

    test("resets on close and run switch, and discards late responses from another filter or run", async () => {
      const nextJob = "1".repeat(32);
      let resolveOld!: (response: Response) => void;
      let resolveOther!: (response: Response) => void;
      let oldSignal: AbortSignal | null | undefined;
      const old = new Promise<Response>((resolve) => {
        resolveOld = resolve;
      });
      const other = new Promise<Response>((resolve) => {
        resolveOther = resolve;
      });
      let filteredReads = 0;
      const fetchMock = baseFetch((path, init) => {
        if (path === `/api/jobs/${nextJob}`) return json({ ...detail, id: nextJob });
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`) {
          if (++filteredReads === 1) {
            oldSignal = init?.signal;
            return old;
          }
          return json(failedPage(1, 1, 1));
        }
        if (path === `/api/jobs/${nextJob}/files?page=1&pageSize=25`) return other;
        return undefined;
      });
      const view = renderDataSync(JOB_ID);
      const trigger = await screen.findByRole("button", { name: /^Files/ });
      await userEvent.click(trigger);
      await screen.findByText("files-page-1-row-0.json");
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      await screen.findByText("files-page-1-row-0.json");
      expect(oldSignal?.aborted).toBe(true);
      await act(async () => resolveOld(json(failedPage(1))));
      expect(screen.queryByText("failed-files-page-1-row-0.json")).not.toBeInTheDocument();
      expect(view.client.getQueryData(["job-artifacts", JOB_ID, "files", 1])).toEqual(
        artifactPage("files", 1),
      );
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      await screen.findByText("failed-files-page-1-row-0.json");
      fireEvent.keyDown(trigger.closest(".artifact-disclosure")!, { key: "Escape" });
      await userEvent.click(trigger);
      await screen.findByText("files-page-1-row-0.json");
      expect(screen.getByRole("checkbox", { name: "Show failures only" })).not.toBeChecked();
      await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
      await screen.findByText("failed-files-page-1-row-0.json");
      view.rerenderJob(nextJob);
      const nextTrigger = await screen.findByRole("button", { name: /^Files/ });
      expect(nextTrigger).toHaveAttribute("aria-expanded", "false");
      await userEvent.click(nextTrigger);
      expect(screen.getByRole("checkbox", { name: "Show failures only" })).not.toBeChecked();
      expect(screen.getByText("Loading files...")).toBeVisible();
      expect(screen.queryByText("failed-files-page-1-row-0.json")).not.toBeInTheDocument();
      await act(async () => resolveOther(json(artifactPage("files", 1, 1, 1, nextJob))));
      await screen.findByText("files-page-1-row-0.json");
      expect(
        fetchMock.mock.calls.some(
          ([path]) =>
            String(path).startsWith(`/api/jobs/${nextJob}/files?`) &&
            String(path).includes("failuresOnly"),
        ),
      ).toBe(false);
    });
  });

  describe("artifact pagination", () => {
    let height: number;
    let frames: Map<number, FrameRequestCallback>;

    beforeEach(() => {
      height = 500;
      frames = new Map();
      const originalRect = HTMLElement.prototype.getBoundingClientRect;
      vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
        this: HTMLElement,
      ) {
        return this.classList.contains("artifact-results") ||
          this.classList.contains("artifact-list")
          ? new DOMRect(0, 0, 900, height)
          : originalRect.call(this);
      });
      vi.stubGlobal("scrollX", 37);
      vi.stubGlobal("scrollY", 900);
      vi.spyOn(window, "scrollTo");
      vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
        const id = frames.size + 1;
        frames.set(id, callback);
        return id;
      });
      // Keep cancelled callbacks available to exercise their own stale-work guards.
      vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => undefined);
    });

    afterEach(() => vi.unstubAllGlobals());

    test.each([
      ["full", "cached"],
      ["full", "uncached"],
      ["cached partial", "cached"],
      ["cached partial", "uncached"],
    ] as const)(
      "releases taller filtered rows when returning from a %s failure page to %s unfiltered rows",
      async (lastPage, normalCache) => {
        let resolveLast!: (response: Response) => void;
        let resolveNormal!: (response: Response) => void;
        const last = new Promise<Response>((resolve) => {
          resolveLast = resolve;
        });
        const normal = new Promise<Response>((resolve) => {
          resolveNormal = resolve;
        });
        const failed = (page: number, count = 25): JobFileList => {
          const data = artifactPage("files", page, count, 2) as JobFileList;
          return {
            ...data,
            items: data.items.map((file) => ({
              ...file,
              state: "failed",
              failureSummary: "Source file is unavailable",
            })),
          };
        };
        let normalReads = 0;
        let lastReads = 0;
        baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return ++normalReads === 1 ? json(artifactPage("files", 1)) : normal;
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25&failuresOnly=true`)
            return json(failed(1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25&failuresOnly=true`) {
            lastReads++;
            return lastPage === "full" ? json(failed(2)) : last;
          }
          return undefined;
        });
        vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
          this: HTMLElement,
        ) {
          if (this.classList.contains("artifact-list")) {
            const rowHeight = this.querySelector(".failure-copy") ? 36 : 20;
            return new DOMRect(0, 0, 900, this.children.length * rowHeight);
          }
          if (this.classList.contains("artifact-results")) {
            const natural =
              this.querySelector(".artifact-list")?.getBoundingClientRect().height ?? 0;
            return new DOMRect(0, 0, 900, Math.max(natural, parseFloat(this.style.minHeight) || 0));
          }
          return new DOMRect();
        });
        const { client } = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        await screen.findByText("files-page-1-row-0.json");
        const nav = screen.getByRole("navigation", { name: "Files pages" });
        const results = nav.parentElement!.querySelector<HTMLElement>(".artifact-results")!;
        const checkbox = screen.getByRole("checkbox", { name: "Show failures only" });
        await userEvent.click(checkbox);
        await waitFor(() => expect(results.querySelectorAll(".failure-copy")).toHaveLength(25));
        expect(results.getBoundingClientRect().height).toBe(900);
        if (lastPage === "cached partial") {
          act(() => {
            client.setQueryData(
              ["job-artifacts", JOB_ID, "files", 2, "failures-only"],
              failed(2, 4),
            );
          });
        }
        await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
        await waitFor(() => expect(lastReads).toBe(1));
        await screen.findByText(
          lastPage === "full" ? "files-page-2-row-0.json" : "files-page-1-row-0.json",
        );
        expect(results.querySelectorAll("li")).toHaveLength(25);
        expect(results.style.minHeight).toBe("900px");
        if (normalCache === "uncached") {
          act(() =>
            client.removeQueries({ queryKey: ["job-artifacts", JOB_ID, "files", 1], exact: true }),
          );
        }
        await userEvent.click(checkbox);
        await waitFor(() => expect(normalReads).toBe(2));
        if (normalCache === "cached") {
          expect(results.querySelectorAll("li")).toHaveLength(25);
          expect(results.querySelectorAll(".failure-copy")).toHaveLength(0);
          expect(results.getBoundingClientRect().height).toBe(500);
        } else {
          expect(results.style.minHeight).toBe("900px");
        }
        await act(async () => resolveNormal(json(artifactPage("files", 1))));
        await waitFor(() => expect(results.closest("[aria-busy='true']")).toBeNull());
        expect(nav).toHaveTextContent("Page 1 of 3");
        expect(checkbox).not.toBeChecked();
        expect(results.querySelectorAll("li")).toHaveLength(25);
        expect(results.style.minHeight).toBe("500px");
        expect(results.getBoundingClientRect().height).toBe(500);
        await act(async () => resolveLast(json(failed(2, 4))));
        expect(results.getBoundingClientRect().height).toBe(500);
      },
    );

    test.each(["files", "events", "logs"] as const)(
      "keeps %s rows and navigation mounted, reserves full-page height, and releases only the loaded last partial page",
      async (kind) => {
        const label = kind[0].toUpperCase() + kind.slice(1);
        let resolveFirst!: (response: Response) => void;
        let resolveSecond!: (response: Response) => void;
        let resolveThird!: (response: Response) => void;
        let resolveBack!: (response: Response) => void;
        const first = new Promise<Response>((resolve) => {
          resolveFirst = resolve;
        });
        const second = new Promise<Response>((resolve) => {
          resolveSecond = resolve;
        });
        const third = new Promise<Response>((resolve) => {
          resolveThird = resolve;
        });
        const back = new Promise<Response>((resolve) => {
          resolveBack = resolve;
        });
        const signals: AbortSignal[] = [];
        let secondReads = 0;
        baseFetch((path, init) => {
          if (!path.startsWith(`/api/jobs/${JOB_ID}/${kind}?`)) return undefined;
          expect(init?.signal).toBeInstanceOf(AbortSignal);
          signals.push(init!.signal!);
          if (path.endsWith("page=1&pageSize=25")) return first;
          if (path.endsWith("page=2&pageSize=25")) return ++secondReads === 1 ? second : back;
          if (path.endsWith("page=3&pageSize=25")) return third;
          throw new Error(`Unexpected artifact URL: ${path}`);
        });
        renderDataSync(JOB_ID);
        const trigger = await screen.findByRole("button", { name: new RegExp(`^${label}`) });
        await userEvent.click(trigger);
        const disclosure = trigger.closest<HTMLElement>(".artifact-disclosure")!;
        const loading = within(disclosure).getByText(`Loading ${kind}...`);
        const results = disclosure.querySelector<HTMLElement>(".artifact-results")!;
        expect(results).toContainElement(loading);
        expect(results.tagName).toBe("DIV");
        expect(within(disclosure).queryByRole("list")).not.toBeInTheDocument();
        expect(window.requestAnimationFrame).not.toHaveBeenCalled();

        await act(async () => resolveFirst(json(artifactPage(kind, 1))));
        const list = await within(disclosure).findByRole("list");
        const rows = within(list).getAllByRole("listitem");
        expect(rows).toHaveLength(25);
        const nav = within(disclosure).getByRole("navigation", { name: `${label} pages` });
        expect(results).toContainElement(list);
        expect(results).not.toContainElement(nav);
        expect(results.nextElementSibling).toBe(nav);
        const next = within(nav).getByRole("button", { name: "Next" });
        const previous = within(nav).getByRole("button", { name: "Previous" });
        await userEvent.click(next);
        expect(within(disclosure).getByRole("list")).toBe(list);
        expect(within(list).getAllByRole("listitem")).toEqual(rows);
        expect(within(disclosure).getByRole("navigation")).toBe(nav);
        expect(within(nav).getByRole("button", { name: "Next" })).toBe(next);
        expect(within(nav).getByRole("button", { name: "Previous" })).toBe(previous);
        expect(results.closest("[aria-busy='true']")).not.toBeNull();
        expect(results.style.minHeight).toBe("500px");
        expect(nav).toHaveTextContent("Page 1 of 3");
        expect(next).toBeDisabled();
        expect(previous).toBeDisabled();
        expect(within(disclosure).queryByText(`Loading ${kind}...`)).not.toBeInTheDocument();
        expect(signals).toHaveLength(2);
        expect(signals[1].aborted).toBe(false);
        expect(window.scrollTo).not.toHaveBeenCalled();
        expect(window.requestAnimationFrame).not.toHaveBeenCalled();

        height = 420;
        await act(async () => resolveSecond(json(artifactPage(kind, 2))));
        await waitFor(() => expect(nav).toHaveTextContent("Page 2 of 3"));
        expect(within(disclosure).getByRole("list")).toBe(list);
        expect(list).toHaveTextContent(`${kind}-page-2-row-0`);
        expect(list).not.toHaveTextContent(`${kind}-page-1-row-0`);
        expect(results.style.minHeight).toBe("500px");
        expect(results.closest("[aria-busy='true']")).toBeNull();
        await waitFor(() => expect(frames.size).toBe(1));
        expect(window.scrollTo).not.toHaveBeenCalled();
        act(() => frames.get(1)!(0));
        expect(window.scrollTo).toHaveBeenLastCalledWith(37, 900);

        const secondRows = within(list).getAllByRole("listitem");
        await userEvent.click(next);
        expect(within(list).getAllByRole("listitem")).toEqual(secondRows);
        expect(nav).toHaveTextContent("Page 2 of 3");
        expect(next).toBeDisabled();
        expect(previous).toBeDisabled();
        expect(results.style.minHeight).toBe("500px");
        expect(frames.size).toBe(1);
        height = 120;
        await act(async () => resolveThird(json(artifactPage(kind, 3, 5))));
        await waitFor(() => expect(nav).toHaveTextContent("Page 3 of 3"));
        expect(within(list).getAllByRole("listitem")).toHaveLength(5);
        expect(results.style.minHeight).toBe("");
        expect(next).toBeDisabled();
        expect(previous).toBeEnabled();
        await waitFor(() => expect(frames.size).toBe(2));
        act(() => frames.get(2)!(0));

        // Cached full rows restore the frame before their background request settles.
        await userEvent.click(previous);
        await waitFor(() => expect(secondReads).toBe(2));
        expect(within(disclosure).getByRole("list")).toBe(list);
        expect(within(disclosure).getByRole("navigation")).toBe(nav);
        expect(previous).toBeDisabled();
        expect(next).toBeDisabled();
        expect(results.style.minHeight).toBe("500px");
        expect(list).toHaveTextContent(`${kind}-page-2-row-0`);
        expect(results.closest("[aria-busy='true']")).not.toBeNull();
        await waitFor(() => expect(frames.size).toBe(3));
        act(() => frames.get(3)!(0));
        expect(window.scrollTo).toHaveBeenCalledTimes(3);
        expect(window.scrollTo).toHaveBeenLastCalledWith(37, 900);
        height = 420;
        await act(async () => resolveBack(json(artifactPage(kind, 2))));
        await waitFor(() => expect(next).toBeEnabled());
        expect(nav).toHaveTextContent("Page 2 of 3");
        expect(results.style.minHeight).toBe("500px");
        await waitFor(() => expect(frames.size).toBe(4));
        act(() => frames.get(4)!(0));
        expect(window.scrollTo).toHaveBeenCalledTimes(4);
        expect(window.scrollTo).toHaveBeenLastCalledWith(37, 900);
      },
    );

    test.each(["immediate", "background"] as const)(
      "anchors cached full rows with %s resolution and reanchors after a background fetch",
      async (resolution) => {
        let resolveSecond!: (response: Response) => void;
        const second = new Promise<Response>((resolve) => {
          resolveSecond = resolve;
        });
        let reads = 0;
        baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) {
            reads += 1;
            return resolution === "immediate" ? json(artifactPage("files", 2)) : second;
          }
          return undefined;
        });
        const { client } = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        const nav = await screen.findByRole("navigation", { name: "Files pages" });
        const list = nav.parentElement!.querySelector("ul")!;
        const results = list.closest<HTMLElement>(".artifact-results")!;
        act(() =>
          client.setQueryData(["job-artifacts", JOB_ID, "files", 2], artifactPage("files", 2)),
        );
        await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
        await waitFor(() => expect(reads).toBe(1));
        if (resolution === "background") {
          expect(within(nav).getByRole("button", { name: "Next" })).toBeDisabled();
          expect(within(nav).getByRole("button", { name: "Previous" })).toBeDisabled();
          expect(client.getQueryState(["job-artifacts", JOB_ID, "files", 2])?.fetchStatus).toBe(
            "fetching",
          );
          expect(list).toHaveTextContent("files-page-2-row-0.json");
          expect(results.style.minHeight).toBe("500px");
          await waitFor(() => expect(frames.size).toBe(1));
          expect(window.scrollTo).not.toHaveBeenCalled();
          act(() => frames.get(1)!(0));
          expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 900);
          await act(async () => resolveSecond(json(artifactPage("files", 2))));
          await waitFor(() => expect(frames.size).toBe(2));
        }
        await waitFor(() =>
          expect(within(nav).getByRole("button", { name: "Next" })).toBeEnabled(),
        );
        expect(frames.size).toBeGreaterThanOrEqual(1);
        expect(nav).toHaveTextContent("Page 2 of 3");
        expect(nav.parentElement!.querySelector("ul")).toBe(list);
        expect(results.style.minHeight).toBe("500px");
        expect(screen.queryByText("Loading files...")).not.toBeInTheDocument();
        act(() => frames.get(frames.size)!(0));
        expect(window.scrollTo).toHaveBeenCalledTimes(resolution === "background" ? 2 : 1);
        expect(window.scrollTo).toHaveBeenLastCalledWith(37, 900);
      },
    );

    test("anchors the pager immediately when returning from a cached last partial page to cached full rows, then reanchors fresh growth", async () => {
      let resolveFull!: (response: Response) => void;
      const full = new Promise<Response>((resolve) => {
        resolveFull = resolve;
      });
      let fullReads = 0;
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return ++fullReads === 1 ? json(artifactPage("files", 1, 25, 2)) : full;
        if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`)
          return json(artifactPage("files", 2, 5, 2));
        return undefined;
      });
      const { client } = renderDataSync(JOB_ID);
      await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
      const nav = await screen.findByRole("navigation", { name: "Files pages" });
      const list = nav.parentElement!.querySelector("ul")!;
      const results = list.closest<HTMLElement>(".artifact-results")!;
      const next = within(nav).getByRole("button", { name: "Next" });
      const previous = within(nav).getByRole("button", { name: "Previous" });
      await userEvent.click(next);
      await waitFor(() => expect(nav).toHaveTextContent("Page 2 of 2"));
      expect(within(list).getAllByRole("listitem")).toHaveLength(5);
      expect(results.style.minHeight).toBe("");
      await waitFor(() => expect(frames.size).toBe(1));
      act(() => frames.get(1)!(0));
      vi.mocked(window.scrollTo).mockClear();

      height = 100;
      let scrollY = 900;
      let fullGrowth = 1500;
      const partialDocumentTop = 795 + scrollY;
      vi.mocked(window.scrollTo).mockImplementation((_x, y) => {
        scrollY = y!;
        vi.stubGlobal("scrollY", scrollY);
      });
      // Unlike JSDOM's zero-size geometry, this pager moves with content and scrolling.
      vi.spyOn(nav, "getBoundingClientRect").mockImplementation(() => {
        const growth = list.textContent?.includes("files-page-1-row-0") ? fullGrowth : 0;
        return new DOMRect(0, partialDocumentTop + growth - scrollY, 900, 40);
      });
      expect(nav.getBoundingClientRect().top).toBe(795);
      await userEvent.click(previous);
      await waitFor(() => expect(fullReads).toBe(2));
      expect(client.getQueryState(["job-artifacts", JOB_ID, "files", 1])?.fetchStatus).toBe(
        "fetching",
      );
      expect(nav).toHaveTextContent("Page 1 of 2");
      expect(nav.parentElement!.querySelector("ul")).toBe(list);
      expect(within(list).getAllByRole("listitem")).toHaveLength(25);
      expect(results.style.minHeight).toBe("500px");
      expect(results.closest("[aria-busy='true']")).not.toBeNull();
      expect(next).toBeDisabled();
      expect(previous).toBeDisabled();
      expect(nav.getBoundingClientRect().top).toBe(2295);
      await waitFor(() => expect(frames.size).toBe(2));
      act(() => frames.get(2)!(0));
      expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 2400);
      expect(scrollY).toBe(2400);
      expect(nav.getBoundingClientRect().top).toBe(795);
      expect(client.getQueryState(["job-artifacts", JOB_ID, "files", 1])?.fetchStatus).toBe(
        "fetching",
      );

      height = 700;
      fullGrowth += 200;
      await act(async () => resolveFull(json(artifactPage("files", 1, 25, 2))));
      await waitFor(() => expect(next).toBeEnabled());
      expect(results.style.minHeight).toBe("700px");
      expect(nav.getBoundingClientRect().top).toBe(995);
      await waitFor(() => expect(frames.size).toBe(3));
      act(() => frames.get(3)!(0));
      expect(window.scrollTo).toHaveBeenCalledTimes(2);
      expect(window.scrollTo).toHaveBeenLastCalledWith(37, 2600);
      expect(scrollY).toBe(2600);
      expect(nav.getBoundingClientRect().top).toBe(795);
      expect(window.scrollTo).not.toHaveBeenCalledWith(37, 900);
      expect(results.closest("[aria-busy='true']")).toBeNull();
    });

    test.each([
      [false, false],
      [false, true],
      [true, false],
      [true, true],
    ])(
      "retains visible rows on cached partial Next (filtered: %s, refetch fails: %s)",
      async (filtered, fails) => {
        let resolvePartial!: (response: Response) => void;
        let resolveRetry!: (response: Response) => void;
        const partial = new Promise<Response>((resolve) => {
          resolvePartial = resolve;
        });
        const retry = new Promise<Response>((resolve) => {
          resolveRetry = resolve;
        });
        const filePage = (page: number, count = 25): JobFileList => {
          const data = artifactPage("files", page, count, 2) as JobFileList;
          return filtered
            ? {
                ...data,
                items: data.items.map((file) => ({
                  ...file,
                  state: "failed",
                  failureSummary: "Invalid export",
                })),
              }
            : data;
        };
        const suffix = filtered ? "&failuresOnly=true" : "";
        let partialReads = 0;
        baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1, 25, 2));
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25${suffix}`)
            return json(filePage(1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25${suffix}`)
            return ++partialReads === 1
              ? json(filePage(2, 4))
              : partialReads === 2
                ? partial
                : retry;
          return undefined;
        });
        const { client } = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        await screen.findByText("files-page-1-row-0.json");
        if (filtered) {
          await userEvent.click(screen.getByRole("checkbox", { name: "Show failures only" }));
          await waitFor(() =>
            expect(screen.getByText("files-page-1-row-0.json").closest("li")).toHaveTextContent(
              "Invalid export",
            ),
          );
        }
        const nav = await screen.findByRole("navigation", { name: "Files pages" });
        const results = nav.parentElement!.querySelector<HTMLElement>(".artifact-results")!;
        const next = within(nav).getByRole("button", { name: "Next" });
        const previous = within(nav).getByRole("button", { name: "Previous" });
        await userEvent.click(next);
        await waitFor(() => expect(nav).toHaveTextContent("Page 2 of 2"));
        expect(results.style.minHeight).toBe("");
        await userEvent.click(previous);
        await waitFor(() => expect(next).toBeEnabled());
        expect(nav).toHaveTextContent("Page 1 of 2");
        expect(results.style.minHeight).toBe("500px");
        act(() => frames.get(frames.size)!(0));
        const frameCount = frames.size;
        const scrollCount = vi.mocked(window.scrollTo).mock.calls.length;
        const list = within(results).getByRole("list");
        const rows = within(list).getAllByRole("listitem");
        height = 420;
        await userEvent.click(next);
        await waitFor(() => expect(partialReads).toBe(2));
        const key = ["job-artifacts", JOB_ID, "files", 2, ...(filtered ? ["failures-only"] : [])];
        expect(client.getQueryState(key)?.fetchStatus).toBe("fetching");
        expect(nav).toHaveTextContent("Page 1 of 2");
        expect(within(results).getByRole("list")).toBe(list);
        expect(within(list).getAllByRole("listitem")).toEqual(rows);
        expect(results.style.minHeight).toBe("500px");
        expect(next).toBeDisabled();
        expect(previous).toBeDisabled();
        expect(frames.size).toBe(frameCount);
        expect(window.scrollTo).toHaveBeenCalledTimes(scrollCount);
        if (fails) {
          await act(async () => resolvePartial(json({ title: "Service Unavailable" }, 503)));
          const error = await within(results).findByRole("alert");
          expect(error).toHaveTextContent("Files are unavailable.");
          expect(within(results).getByRole("list")).toBe(list);
          expect(within(list).getAllByRole("listitem")).toEqual(rows);
          expect(next).toBeDisabled();
          expect(previous).toBeDisabled();
          await userEvent.click(within(error).getByRole("button", { name: "Try again" }));
          await waitFor(() => expect(partialReads).toBe(3));
          expect(within(list).getAllByRole("listitem")).toEqual(rows);
        }
        height = 100;
        await act(async () => (fails ? resolveRetry : resolvePartial)(json(filePage(2, 4))));
        await waitFor(() => expect(previous).toBeEnabled());
        expect(nav).toHaveTextContent("Page 2 of 2");
        expect(within(results).getByRole("list")).toBe(list);
        expect(within(list).getAllByRole("listitem")).toHaveLength(4);
        expect(within(results).queryByRole("alert")).not.toBeInTheDocument();
        expect(results.style.minHeight).toBe("");
        await waitFor(() => expect(frames.size).toBe(frameCount + 1));
        act(() => frames.get(frames.size)!(0));
        expect(window.scrollTo).toHaveBeenCalledTimes(scrollCount + 1);
        expect(window.scrollTo).toHaveBeenLastCalledWith(37, 900);
      },
    );

    test("keeps the measured error frame and restores the requested page after Try again succeeds", async () => {
      let resolveSecond!: (response: Response) => void;
      let resolveRetry!: (response: Response) => void;
      const second = new Promise<Response>((resolve) => {
        resolveSecond = resolve;
      });
      const retry = new Promise<Response>((resolve) => {
        resolveRetry = resolve;
      });
      let reads = 0;
      const fetchMock = baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`)
          return ++reads === 1 ? second : retry;
        if (path === `/api/jobs/${JOB_ID}/files?page=3&pageSize=25`)
          return json(artifactPage("files", 3, 5));
        return undefined;
      });
      renderDataSync(JOB_ID);
      const trigger = await screen.findByRole("button", { name: /^Files/ });
      await userEvent.click(trigger);
      const nav = await screen.findByRole("navigation", { name: "Files pages" });
      const disclosure = trigger.closest<HTMLElement>(".artifact-disclosure")!;
      const results = disclosure.querySelector<HTMLElement>(".artifact-results")!;
      await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
      await act(async () => resolveSecond(json({ title: "Service Unavailable" }, 503)));
      const error = await within(disclosure).findByRole("alert");
      expect(error).toHaveTextContent("Files are unavailable.");
      expect(results).toContainElement(error);
      expect(results).toHaveTextContent("files-page-1-row-0.json");
      expect(nav).toHaveTextContent("Page 1 of 3");
      const next = within(nav).getByRole("button", { name: "Next" });
      const previous = within(nav).getByRole("button", { name: "Previous" });
      expect(next).toBeDisabled();
      expect(previous).toBeDisabled();
      await userEvent.click(next);
      await userEvent.click(previous);
      expect(reads).toBe(1);
      expect(
        fetchMock.mock.calls.filter(([path]) => String(path).includes("/files?")),
      ).toHaveLength(2);
      // TanStack drops placeholder data on error; the reserved frame must not depend on it.
      expect(results.style.minHeight).toBe("500px");
      expect(window.requestAnimationFrame).not.toHaveBeenCalled();
      expect(window.scrollTo).not.toHaveBeenCalled();
      await userEvent.click(within(error).getByRole("button", { name: "Try again" }));
      expect(results.style.minHeight).toBe("500px");
      expect(window.requestAnimationFrame).not.toHaveBeenCalled();
      height = 420;
      await act(async () => resolveRetry(json(artifactPage("files", 2))));
      await within(disclosure).findByText("files-page-2-row-0.json");
      expect(within(disclosure).queryByRole("alert")).not.toBeInTheDocument();
      expect(results.style.minHeight).toBe("500px");
      expect(within(disclosure).getByRole("navigation")).toHaveTextContent("Page 2 of 3");
      expect(next).toBeEnabled();
      expect(previous).toBeEnabled();
      await waitFor(() => expect(frames.size).toBe(1));
      act(() => frames.get(1)!(0));
      expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 900);
      expect(reads).toBe(2);
      await userEvent.click(next);
      await waitFor(() => expect(nav).toHaveTextContent("Page 3 of 3"));
      expect(results).toHaveTextContent("files-page-3-row-0.json");
      expect(
        fetchMock.mock.calls.filter(([path]) => String(path).endsWith("/files?page=3&pageSize=25")),
      ).toHaveLength(1);
    });

    describe("scroll intent", () => {
      let view: ReturnType<typeof renderDataSync>;
      let resolveSecond: (response: Response) => void;
      let nav: HTMLElement;
      let results: HTMLElement;
      let next: HTMLElement;
      let previous: HTMLElement;

      beforeEach(async () => {
        const second = new Promise<Response>((resolve) => {
          resolveSecond = resolve;
        });
        baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) return second;
          if (path === `/api/jobs/${JOB_ID}/files?page=3&pageSize=25`)
            return json(artifactPage("files", 3, 5));
          return undefined;
        });
        vi.spyOn(window, "addEventListener");
        vi.spyOn(window, "removeEventListener");
        vi.mocked(window.scrollTo).mockImplementation((_x, y) => {
          vi.stubGlobal("scrollY", y!);
        });
        view = renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        nav = await screen.findByRole("navigation", { name: "Files pages" });
        results = nav.parentElement!.querySelector<HTMLElement>(".artifact-results")!;
        next = within(nav).getByRole("button", { name: "Next" });
        previous = within(nav).getByRole("button", { name: "Previous" });
      });

      test.each(["wheel", "touchmove", "PageDown"] as const)(
        "%s prevents both cached-full restores without blocking height updates or the next navigation",
        async (intent) => {
          act(() =>
            view.client.setQueryData(
              ["job-artifacts", JOB_ID, "files", 2],
              artifactPage("files", 2),
            ),
          );
          await userEvent.click(next);
          await waitFor(() => expect(frames.size).toBe(1));
          expect(nav).toHaveTextContent("Page 2 of 3");
          expect(results).toHaveTextContent("files-page-2-row-0.json");
          expect(results.style.minHeight).toBe("500px");
          expect(
            view.client.getQueryState(["job-artifacts", JOB_ID, "files", 2])?.fetchStatus,
          ).toBe("fetching");
          vi.stubGlobal("scrollY", 1300);
          if (intent === "PageDown") fireEvent.keyDown(window, { key: intent });
          else fireEvent(window, new Event(intent));
          act(() => frames.get(1)!(0));
          expect(window.scrollTo).not.toHaveBeenCalled();
          expect(window.scrollY).toBe(1300);

          height = 700;
          await act(async () => resolveSecond(json(artifactPage("files", 2))));
          await waitFor(() => expect(next).toBeEnabled());
          expect(results.style.minHeight).toBe("700px");
          await waitFor(() => expect(frames.size).toBe(2));
          act(() => frames.get(2)!(0));
          expect(window.scrollTo).not.toHaveBeenCalled();
          expect(window.scrollY).toBe(1300);

          await userEvent.click(next);
          await waitFor(() => expect(nav).toHaveTextContent("Page 3 of 3"));
          expect(results.style.minHeight).toBe("");
          await waitFor(() => expect(frames.size).toBe(3));
          act(() => frames.get(3)!(0));
          expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 1300);
        },
      );

      test.each(["full", "final partial"] as const)(
        "user scroll intent suppresses an uncached %s anchor but still applies its final height",
        async (size) => {
          await userEvent.click(next);
          expect(nav).toHaveTextContent("Page 1 of 3");
          expect(results).toHaveTextContent("files-page-1-row-0.json");
          expect(results.style.minHeight).toBe("500px");
          expect(frames.size).toBe(0);
          vi.stubGlobal("scrollY", 1300);
          fireEvent(window, new Event(size === "full" ? "wheel" : "touchmove"));
          height = size === "full" ? 700 : 100;
          await act(async () =>
            resolveSecond(
              json(artifactPage("files", 2, size === "full" ? 25 : 5, size === "full" ? 3 : 2)),
            ),
          );
          await waitFor(() => expect(previous).toBeEnabled());
          expect(results.style.minHeight).toBe(size === "full" ? "700px" : "");
          expect(results).toHaveTextContent("files-page-2-row-0.json");
          await waitFor(() => expect(frames.size).toBe(1));
          act(() => frames.get(1)!(0));
          expect(window.scrollTo).not.toHaveBeenCalled();
          expect(window.scrollY).toBe(1300);
        },
      );

      test.each([
        "input",
        "select",
        "textarea",
        "contenteditable",
        "prevented key",
        "button Space",
        "native scroll",
      ] as const)("%s does not cancel a valid pending restore", async (interaction) => {
        await userEvent.click(next);
        vi.stubGlobal("scrollY", 1300);
        if (interaction === "native scroll") {
          fireEvent.scroll(window);
        } else if (interaction === "prevented key") {
          const event = new KeyboardEvent("keydown", {
            key: "PageDown",
            bubbles: true,
            cancelable: true,
          });
          event.preventDefault();
          fireEvent(window, event);
          expect(event.defaultPrevented).toBe(true);
        } else {
          const control = render(
            interaction === "input" ? (
              <input aria-label="Scroll intent input" />
            ) : interaction === "select" ? (
              <select aria-label="Scroll intent select">
                <option>One</option>
              </select>
            ) : interaction === "textarea" ? (
              <textarea aria-label="Scroll intent textarea" />
            ) : interaction === "contenteditable" ? (
              <div contentEditable suppressContentEditableWarning>
                Editable text
              </div>
            ) : (
              <button type="button">
                <span>Keyboard action</span>
              </button>
            ),
          );
          const target = control.container.firstElementChild as HTMLElement;
          // JSDOM does not implement the browser's isContentEditable property.
          if (interaction === "contenteditable")
            Object.defineProperty(target, "isContentEditable", { value: true });
          fireEvent.keyDown(interaction === "button Space" ? target.firstElementChild! : target, {
            key: interaction === "button Space" ? " " : "ArrowDown",
          });
        }
        await act(async () => resolveSecond(json(artifactPage("files", 2))));
        await waitFor(() => expect(next).toBeEnabled());
        await waitFor(() => expect(frames.size).toBe(1));
        act(() => frames.get(1)!(0));
        expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 900);
        expect(window.scrollY).toBe(900);
      });

      test.each(["close", "unmount"] as const)(
        "%s removes every scroll-intent listener and cancels the queued restore",
        async (action) => {
          const handlers = vi
            .mocked(window.addEventListener)
            .mock.calls.filter(([type]) => ["wheel", "touchmove", "keydown"].includes(type));
          expect(handlers.map(([type]) => type).sort()).toEqual(["keydown", "touchmove", "wheel"]);
          expect(
            vi.mocked(window.addEventListener).mock.calls.some(([type]) => type === "scroll"),
          ).toBe(false);
          act(() =>
            view.client.setQueryData(
              ["job-artifacts", JOB_ID, "files", 2],
              artifactPage("files", 2),
            ),
          );
          await userEvent.click(next);
          await waitFor(() => expect(frames.size).toBe(1));
          if (action === "close")
            await userEvent.click(screen.getByRole("button", { name: /^Files/ }));
          else view.unmount();
          for (const [type, handler] of handlers)
            expect(window.removeEventListener).toHaveBeenCalledWith(type, handler);
          expect(window.cancelAnimationFrame).toHaveBeenCalledWith(1);
          act(() => frames.get(1)!(0));
          await act(async () => resolveSecond(json(artifactPage("files", 2))));
          expect(window.scrollTo).not.toHaveBeenCalled();
        },
      );

      test("allows truthful navigation after a cached current-page background error", async () => {
        act(() =>
          view.client.setQueryData(["job-artifacts", JOB_ID, "files", 2], artifactPage("files", 2)),
        );
        await userEvent.click(next);
        await act(async () => resolveSecond(json({ title: "Service Unavailable" }, 503)));
        await within(results).findByRole("alert");
        expect(nav).toHaveTextContent("Page 2 of 3");
        expect(results).toHaveTextContent("files-page-2-row-0.json");
        expect(next).toBeEnabled();
        expect(previous).toBeEnabled();
        await userEvent.click(next);
        await waitFor(() => expect(nav).toHaveTextContent("Page 3 of 3"));
        expect(results).toHaveTextContent("files-page-3-row-0.json");
        expect(next).toBeDisabled();
        expect(previous).toBeEnabled();
      });
    });

    test.each([
      ["short non-final", 2, 3, 5, 25, "500px"],
      ["full final", 2, 2, 25, 25, "500px"],
      ["server-sized full final", 2, 2, 5, 5, "500px"],
      ["server-sized partial final", 2, 2, 5, 10, ""],
      ["empty zero-page", 2, 0, 0, 25, ""],
    ] as const)(
      "uses response pagination to decide whether a loaded %s page releases height",
      async (_name, page, totalPages, count, pageSize, minHeight) => {
        let resolveSecond!: (response: Response) => void;
        const second = new Promise<Response>((resolve) => {
          resolveSecond = resolve;
        });
        baseFetch((path) => {
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) return second;
          return undefined;
        });
        renderDataSync(JOB_ID);
        await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
        const nav = await screen.findByRole("navigation", { name: "Files pages" });
        const panel = nav.parentElement!;
        const results = panel.querySelector<HTMLElement>(".artifact-results")!;
        await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
        expect(results.style.minHeight).toBe("500px");
        height = 120;
        await act(async () =>
          resolveSecond(json(artifactPage("files", page, count, totalPages, JOB_ID, pageSize))),
        );
        await waitFor(() => expect(results.closest("[aria-busy='true']")).toBeNull());
        expect(results.style.minHeight).toBe(minHeight);
        if (count === 0) expect(within(results).getByText("No files recorded.")).toBeVisible();
        else expect(within(results).getAllByRole("listitem")).toHaveLength(count);
      },
    );

    test("does not consume pending scroll for a response whose page differs from the requested target", async () => {
      let resolveSecond!: (response: Response) => void;
      const second = new Promise<Response>((resolve) => {
        resolveSecond = resolve;
      });
      baseFetch((path) => {
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) return second;
        return undefined;
      });
      const { client } = renderDataSync(JOB_ID);
      await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
      const nav = await screen.findByRole("navigation", { name: "Files pages" });
      await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
      await act(async () => resolveSecond(json(artifactPage("files", 1))));
      await waitFor(() =>
        expect(client.getQueryState(["job-artifacts", JOB_ID, "files", 2])?.fetchStatus).toBe(
          "idle",
        ),
      );
      await waitFor(() => expect(nav.closest("[aria-busy='true']")).toBeNull());
      expect(window.requestAnimationFrame).not.toHaveBeenCalled();
      expect(window.scrollTo).not.toHaveBeenCalled();
      act(() =>
        client.setQueryData(["job-artifacts", JOB_ID, "files", 2], artifactPage("files", 2)),
      );
      await waitFor(() => expect(frames.size).toBe(1));
      act(() => frames.get(1)!(0));
      expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 900);
    });

    test.each(["close", "escape", "switch job", "unmount"] as const)(
      "clears pending scroll on %s even if the fetch resolves late",
      async (action) => {
        const nextJob = "1".repeat(32);
        let signal: AbortSignal | undefined;
        let resolveSecond!: (response: Response) => void;
        const second = new Promise<Response>((resolve) => {
          resolveSecond = resolve;
        });
        baseFetch((path, init) => {
          if (path === `/api/jobs/${nextJob}`) return json({ ...detail, id: nextJob });
          if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
            return json(artifactPage("files", 1));
          if (path === `/api/jobs/${JOB_ID}/files?page=2&pageSize=25`) {
            signal = init?.signal ?? undefined;
            return second.then((response) => response.clone());
          }
          return undefined;
        });
        const view = renderDataSync(JOB_ID);
        const trigger = await screen.findByRole("button", { name: /^Files/ });
        await userEvent.click(trigger);
        const nav = await screen.findByRole("navigation", { name: "Files pages" });
        await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
        expect(signal).toBeInstanceOf(AbortSignal);
        if (action === "close") await userEvent.click(trigger);
        if (action === "escape") fireEvent.keyDown(trigger, { key: "Escape" });
        if (action === "switch job") {
          view.rerenderJob(nextJob);
          await waitFor(() => expect(trigger).not.toBeInTheDocument());
        }
        if (action === "unmount") view.unmount();
        if (action === "switch job" || action === "unmount") expect(signal?.aborted).toBe(true);
        await act(async () => resolveSecond(json(artifactPage("files", 2))));
        expect(window.requestAnimationFrame).not.toHaveBeenCalled();
        expect(window.scrollTo).not.toHaveBeenCalled();
        if (action === "close" || action === "escape") {
          expect(trigger).toHaveAttribute("aria-expanded", "false");
          await userEvent.click(trigger);
          await screen.findByText("files-page-2-row-0.json");
          expect(window.requestAnimationFrame).not.toHaveBeenCalled();
          expect(window.scrollTo).not.toHaveBeenCalled();
        }
      },
    );

    test.each(["close", "switch job", "unmount", "new page"] as const)(
      "cancels a scheduled frame and rejects its stale callback after %s",
      async (action) => {
        const nextJob = "1".repeat(32);
        baseFetch((path) => {
          if (path === `/api/jobs/${nextJob}`) return json({ ...detail, id: nextJob });
          for (const page of [1, 2, 3]) {
            if (path === `/api/jobs/${JOB_ID}/files?page=${page}&pageSize=25`)
              return json(artifactPage("files", page, page === 3 ? 5 : 25));
          }
          return undefined;
        });
        const view = renderDataSync(JOB_ID);
        const trigger = await screen.findByRole("button", { name: /^Files/ });
        await userEvent.click(trigger);
        const nav = await screen.findByRole("navigation", { name: "Files pages" });
        await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
        await waitFor(() => expect(frames.size).toBe(1));
        const staleCallback = frames.get(1)!;
        if (action === "close") await userEvent.click(trigger);
        if (action === "switch job") {
          view.rerenderJob(nextJob);
          await waitFor(() => expect(trigger).not.toBeInTheDocument());
        }
        if (action === "unmount") view.unmount();
        if (action === "new page") {
          await userEvent.click(within(nav).getByRole("button", { name: "Next" }));
          await waitFor(() => expect(frames.size).toBe(2));
        }
        expect(window.cancelAnimationFrame).toHaveBeenCalledWith(1);
        act(() => staleCallback(0));
        expect(window.scrollTo).not.toHaveBeenCalled();
        if (action === "new page") {
          act(() => frames.get(2)!(0));
          expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(37, 900);
        }
        if (action === "close") {
          await userEvent.click(trigger);
          await screen.findByText("files-page-2-row-0.json");
          act(() => staleCallback(0));
          expect(window.scrollTo).not.toHaveBeenCalled();
        }
      },
    );

    test("never borrows placeholder rows or height from another artifact kind or selected job", async () => {
      const nextJob = "1".repeat(32);
      let resolveEvents!: (response: Response) => void;
      let resolveOtherFiles!: (response: Response) => void;
      const events = new Promise<Response>((resolve) => {
        resolveEvents = resolve;
      });
      const otherFiles = new Promise<Response>((resolve) => {
        resolveOtherFiles = resolve;
      });
      baseFetch((path) => {
        if (path === `/api/jobs/${nextJob}`) return json({ ...detail, id: nextJob });
        if (path === `/api/jobs/${JOB_ID}/files?page=1&pageSize=25`)
          return json(artifactPage("files", 1));
        if (path === `/api/jobs/${JOB_ID}/events?page=1&pageSize=25`) return events;
        if (path === `/api/jobs/${nextJob}/files?page=1&pageSize=25`) return otherFiles;
        return undefined;
      });
      const view = renderDataSync(JOB_ID);
      await userEvent.click(await screen.findByRole("button", { name: /^Files/ }));
      await screen.findByText("files-page-1-row-0.json");
      const eventsTrigger = screen.getByRole("button", { name: /^Events/ });
      await userEvent.click(eventsTrigger);
      const eventsDisclosure = eventsTrigger.closest<HTMLElement>(".artifact-disclosure")!;
      expect(within(eventsDisclosure).getByText("Loading events...")).toBeVisible();
      expect(within(eventsDisclosure).queryByRole("list")).not.toBeInTheDocument();
      expect(
        eventsDisclosure.querySelector<HTMLElement>(".artifact-results")?.style.minHeight,
      ).toBe("");
      await act(async () => resolveEvents(json(artifactPage("events", 1))));
      await within(eventsDisclosure).findByText("info: events-page-1-row-0");
      view.rerenderJob(nextJob);
      await waitFor(() => expect(eventsTrigger).not.toBeInTheDocument());
      const filesTrigger = await screen.findByRole("button", { name: /^Files/ });
      expect(filesTrigger).toHaveAttribute("aria-expanded", "false");
      await userEvent.click(filesTrigger);
      const filesDisclosure = filesTrigger.closest<HTMLElement>(".artifact-disclosure")!;
      expect(within(filesDisclosure).getByText("Loading files...")).toBeVisible();
      expect(within(filesDisclosure).queryByRole("list")).not.toBeInTheDocument();
      expect(filesDisclosure.querySelector<HTMLElement>(".artifact-results")?.style.minHeight).toBe(
        "",
      );
      await act(async () => resolveOtherFiles(json(artifactPage("files", 1, 5, 1, nextJob))));
      await within(filesDisclosure).findByText("files-page-1-row-0.json");
      expect(window.requestAnimationFrame).not.toHaveBeenCalled();
      expect(window.scrollTo).not.toHaveBeenCalled();
    });
  });

  test("preserves manual source selection when the snapshot refreshes", async () => {
    let snapshotCalls = 0;
    baseFetch((path) => {
      if (path === "/api/data-sync") {
        snapshotCalls += 1;
        return json(
          snapshotCalls === 1
            ? snapshot
            : {
                ...snapshot,
                sources: snapshot.sources.map((source) => ({
                  ...source,
                  autoSyncEnabled: source.id === SOURCE_B,
                })),
              },
        );
      }
      return undefined;
    });
    const { client } = renderDataSync();
    const first = await screen.findByRole("checkbox", { name: /Phone exports/ });
    const second = screen.getByRole("checkbox", { name: /Watch archive/ });
    await userEvent.click(first);
    await userEvent.click(second);
    await client.refetchQueries({ queryKey: ["data-sync"] });
    await waitFor(() => expect(snapshotCalls).toBe(2));
    expect(first).not.toBeChecked();
    expect(second).toBeChecked();
  });

  test("uses exact freshness fields and Never copy", async () => {
    baseFetch();
    renderDataSync();
    const schedule = await screen.findByRole("heading", { name: "Schedule & freshness" });
    const card = schedule.closest("section")!;
    expect(await within(card).findAllByText("Last successful sync")).toHaveLength(3);
    expect(within(card).getAllByText("Latest export date")).toHaveLength(3);
    expect(within(card).getAllByText("Never").length).toBeGreaterThan(0);
    expect(within(card).queryByText("Last sync", { exact: true })).not.toBeInTheDocument();
  });

  test("starts with the operational cards and renders exact schedule labels, values, and help", async () => {
    baseFetch((path) =>
      path === "/api/data-sync"
        ? json({
            ...snapshot,
            schedule: { ...snapshot.schedule, cadence: null, cadenceSeconds: 43200, staleDays: 1 },
          })
        : undefined,
    );
    renderDataSync();
    const main = await screen.findByRole("main");
    expect(main.querySelector("h1, .data-sync-heading, .eyebrow")).toBeNull();
    expect(screen.queryByText("Operations")).not.toBeInTheDocument();
    expect(screen.getByText("Manual", { selector: ".card-kicker" })).toBeInTheDocument();
    expect(screen.getByText("Automated", { selector: ".card-kicker" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Start a sync" })).toBeInTheDocument();
    await screen.findByRole("button", { name: "About Interval" });
    const card = screen.getByRole("heading", { name: "Schedule & freshness" }).closest("section")!;
    const expected = [
      ["Interval", "Automatically run a sync job on this interval"],
      ["Sources enabled", "Number of sources with automated sync enabled"],
      [
        "Stale after",
        "Show a warning after this period if no new workouts have been imported by auto-sync",
      ],
      ["Next run", "Local date/time when the next sync job will run"],
    ];
    for (const [label, content] of expected) {
      const trigger = within(card).getByRole("button", { name: `About ${label}` });
      expect(trigger).toHaveAccessibleDescription(content);
      expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveAttribute(
        "role",
        "tooltip",
      );
      expect(trigger).not.toHaveAttribute("title");
      expect(trigger).toHaveAttribute("aria-expanded", "false");
    }
    expect(within(card).getByText("Interval").closest("div")).toHaveTextContent("Twice a day");
    expect(within(card).getByText("Sources enabled").closest("div")).toHaveTextContent("1");
    expect(within(card).getByText("Stale after").closest("div")).toHaveTextContent("1 day");
    expect(within(card).queryByText("Cadence")).not.toBeInTheDocument();
    expect(within(card).queryByText("Automatic sources")).not.toBeInTheDocument();
    expect(within(card).queryByText("Last queued")).not.toBeInTheDocument();
  });

  test("pins schedule help on click and dismisses it by Escape, second click, blur, or outside press", async () => {
    baseFetch();
    renderDataSync();
    const trigger = await screen.findByRole("button", { name: "About Interval" });
    const tip = trigger.parentElement!;

    await userEvent.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(tip).toHaveClass("is-open");
    await userEvent.keyboard("{Escape}");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).toHaveFocus();
    expect(tip).not.toHaveClass("is-open");
    expect(tip).toHaveClass("is-dismissed");

    await userEvent.click(trigger);
    await userEvent.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(tip).toHaveClass("is-dismissed");

    await userEvent.click(trigger);
    await userEvent.tab();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(tip).not.toHaveClass("is-open");
    expect(tip).not.toHaveClass("is-dismissed");

    await userEvent.click(trigger);
    await userEvent.pointer({ keys: "[MouseLeft]", target: document.body });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(tip).not.toHaveClass("is-open");
  });

  test.each([
    [
      "summer PDT",
      "2026-07-15T19:30:00Z",
      "America/Los_Angeles",
      "12h" as const,
      "PDT",
      "America/Los_Angeles (UTC-7:00)",
    ],
    [
      "winter PST",
      "2026-01-15T20:30:00Z",
      "America/Los_Angeles",
      "24h" as const,
      "PST",
      "America/Los_Angeles (UTC-8:00)",
    ],
    [
      "half-hour offset",
      "2026-08-07T02:00:00Z",
      "Asia/Kolkata",
      "24h" as const,
      "GMT+5:30",
      "Asia/Kolkata (UTC+5:30)",
    ],
  ])(
    "formats the Next run instant with its %s badge",
    async (_case, nextRunAt, timezone, clockFormat, badgeText, description) => {
      baseFetch((path) =>
        path === "/api/data-sync"
          ? json({ ...snapshot, schedule: { ...snapshot.schedule, nextRunAt } })
          : undefined,
      );
      renderDataSync(undefined, vi.fn(), 30, { ...preferences, timezone, clockFormat });
      const badge = await screen.findByLabelText(description);
      const card = badge.closest("section")!;
      const expectedDateTime = new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: timezone,
        hour12: clockFormat === "12h",
      }).format(new Date(nextRunAt));
      expect(badge).toHaveTextContent(badgeText);
      expect(badge).toHaveAccessibleDescription(description);
      expect(badge).not.toHaveAttribute("title");
      expect(
        within(card).getByText("Next run").closest("div")?.querySelector("dd .next-run-value"),
      ).toHaveTextContent(expectedDateTime);
    },
  );

  test("formats fractional-hour intervals exactly and handles absent or invalid Next run zones truthfully", async () => {
    baseFetch((path) =>
      path === "/api/data-sync"
        ? json({
            ...snapshot,
            schedule: {
              ...snapshot.schedule,
              cadence: "Ignored server cadence",
              cadenceSeconds: 4500,
              nextRunAt: undefined,
            },
          })
        : undefined,
    );
    const view = renderDataSync();
    await screen.findByText("Interval");
    const card = screen.getByRole("heading", { name: "Schedule & freshness" }).closest("section")!;
    expect(within(card).getByText("Interval").closest("div")).toHaveTextContent("Every 1.25 hours");
    expect(within(card).getByText("Next run").closest("div")).toHaveTextContent("Never");
    expect(within(card).queryByText("Ignored server cadence")).not.toBeInTheDocument();
    expect(within(card).queryByText("TZ unavailable")).not.toBeInTheDocument();
    view.unmount();

    vi.mocked(fetch).mockRestore();
    baseFetch((path) => (path === "/api/data-sync" ? json(snapshot) : undefined));
    renderDataSync(undefined, vi.fn(), 30, { ...preferences, timezone: "Not/AZone" });
    await screen.findByLabelText("Not/AZone unavailable; displayed in UTC (UTC+0:00)");
    const invalidCard = screen
      .getByRole("heading", { name: "Schedule & freshness" })
      .closest("section")!;
    expect(
      within(invalidCard).getByLabelText("Not/AZone unavailable; displayed in UTC (UTC+0:00)"),
    ).toHaveTextContent("UTC");
    expect(within(invalidCard).getByText("Next run").closest("div")).not.toHaveTextContent("Never");
  });

  test("polls the snapshot while idle and stops detail polling at terminal cancel-requested state", async () => {
    vi.useFakeTimers();
    let snapshots = 0;
    let details = 0;
    let historyRequests = 0;
    const running = {
      ...detail,
      status: "running" as const,
      terminalAt: undefined,
      cancelRequested: false,
    };
    baseFetch((path) => {
      if (path === "/api/data-sync") {
        snapshots += 1;
        return json(
          snapshots === 1
            ? snapshot
            : {
                ...snapshot,
                activeJob: {
                  ...running,
                  children: undefined,
                  attempt: undefined,
                  retriedByJobIds: undefined,
                  cancelRequested: undefined,
                },
              },
        );
      }
      if (path.startsWith("/api/jobs?")) {
        historyRequests += 1;
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
          items: [],
        });
      }
      if (path === `/api/jobs/${JOB_ID}`) {
        details += 1;
        return json(
          details === 1 ? running : { ...detail, status: "cancelled", cancelRequested: true },
        );
      }
      return undefined;
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(details).toBe(1);
    expect(snapshots).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(details).toBe(2);
    expect(snapshots).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(details).toBe(2);
    expect(snapshots).toBeGreaterThanOrEqual(4);
    expect(historyRequests).toBeGreaterThanOrEqual(2);
    view.unmount();
    vi.useRealTimers();
  });

  test("refreshes task history with final deletion status and results after detail polling completes", async () => {
    const runningProgress = { ...progress, current: 0, total: 1 };
    const completedProgress = { ...progress, current: 1, total: 1 };
    let detailReads = 0;
    let historyReads = 0;
    baseFetch((path) => {
      if (path === "/api/data-sync") return json(snapshot);
      if (path === `/api/jobs/${JOB_ID}`) {
        detailReads += 1;
        return json({
          ...detail,
          operation: "workout_deletion",
          status: detailReads === 1 ? "running" : "succeeded",
          progress: detailReads === 1 ? runningProgress : completedProgress,
        });
      }
      if (path === "/api/jobs?page=1&pageSize=25") {
        historyReads += 1;
        const completed = historyReads > 1;
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
          items: [
            {
              id: JOB_ID,
              operation: "workout_deletion",
              trigger: "manual",
              status: completed ? "succeeded" : "running",
              progress: completed ? completedProgress : runningProgress,
              createdAt: detail.createdAt,
              updatedAt: detail.updatedAt,
            },
          ],
        });
      }
      return undefined;
    });
    renderDataSync(JOB_ID, vi.fn(), 0.2);
    const table = await waitFor(() => {
      const element = document.querySelector(".sync-history-table");
      expect(element).not.toBeNull();
      return element as HTMLElement;
    });
    expect(await within(table).findByText("Running")).toBeInTheDocument();
    expect(within(table).getByText("0 of 1 deleted")).toBeInTheDocument();
    await waitFor(() => expect(within(table).getByText("Completed")).toBeInTheDocument());
    expect(within(table).getByText("1 of 1 deleted")).toBeInTheDocument();
    expect(historyReads).toBeGreaterThanOrEqual(2);
  });

  test("cancellation posts an empty body and refetches snapshot, history, and detail", async () => {
    const running = {
      ...detail,
      status: "running" as const,
      terminalAt: undefined,
      cancelRequested: false,
    };
    let snapshotReads = 0;
    let historyReads = 0;
    let detailReads = 0;
    let cancellation: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path === "/api/data-sync") {
        snapshotReads += 1;
        return json(snapshot);
      }
      if (path.startsWith("/api/jobs?")) {
        historyReads += 1;
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
          items: [],
        });
      }
      if (path === `/api/jobs/${JOB_ID}`) {
        detailReads += 1;
        return json(running);
      }
      if (path.endsWith("/cancellation")) {
        cancellation = {
          body: String(init?.body),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        };
        return json({ ...running, cancelRequested: true });
      }
      return undefined;
    });
    renderDataSync(JOB_ID);
    const cancelButton = await screen.findByRole("button", { name: "Cancel run" });
    const before = [snapshotReads, historyReads, detailReads];
    expect(screen.queryByRole("button", { name: "Retry run" })).not.toBeInTheDocument();
    await userEvent.click(cancelButton);
    await waitFor(() => expect(cancellation).toEqual({ body: "{}", csrf: "csrf-data-sync" }));
    await waitFor(() => {
      expect(snapshotReads).toBeGreaterThan(before[0]);
      expect(historyReads).toBeGreaterThan(before[1]);
      expect(detailReads).toBeGreaterThan(before[2]);
    });
  });

  test("handles retry 409 with safe copy and refetches operation state", async () => {
    let detailReads = 0;
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}`) {
        detailReads += 1;
        return json(detail);
      }
      if (path.endsWith("/retry"))
        return json({ title: "Worker lease secret", detail: "private detail" }, 409);
      return undefined;
    });
    renderDataSync(JOB_ID);
    expect(await screen.findByRole("button", { name: "Retry run" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry run" }));
    expect(
      await screen.findByText(
        "The retry was not started. Review the refreshed run details for retry availability.",
      ),
    ).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("Worker lease secret");
    expect(detailReads).toBeGreaterThan(1);
  });

  test.each(["failed", "partially_succeeded"] as const)(
    "shows a green Superseded parent for resolved %s history, retaining failed route badges",
    async (status) => {
      const currentJobs = ["E".repeat(32), "F".repeat(32)];
      const resolved: JobDetail = {
        ...coverageFailure,
        status,
        displayStatus: "superseded",
        retryEligibility: {
          ...eligibleCoverageRetry,
          canRetry: false,
          reason: "already_current",
          eligibleRouteCount: 0,
          currentRouteCount: 2,
          currentCoverageJobIds: currentJobs,
        },
      };
      const fetchMock = baseFetch((path) =>
        path === `/api/jobs/${JOB_ID}` ? json(resolved) : undefined,
      );
      const { navigate, client } = renderDataSync(JOB_ID);
      const region = await screen.findByRole("region", { name: "Selected run" });
      const badge = await within(region).findByText("Superseded", { selector: ".sync-status" });
      expect(badge).toHaveClass("sync-status--superseded");
      expect(
        within(region).getAllByText("Failed", { selector: ".source-run .sync-status" }),
      ).toHaveLength(2);
      expect(within(region).getByText("Historical run issue:")).toBeVisible();
      const retry = within(region).getByRole("button", { name: "Retry run" });
      expect(retry).toBeDisabled();
      expect(retry).toHaveAccessibleDescription(/No action is needed/);
      expect(within(region).getByRole("note", { name: "Retry availability" })).toHaveTextContent(
        "No retry needed.",
      );
      await userEvent.click(retry);
      expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith("/retry"))).toBe(false);
      expect(client.getQueryData<JobDetail>(["job", JOB_ID])?.status).toBe(status);
      expect(within(region).getByText(/not retry ancestry/)).toBeVisible();
      const links = currentJobs.map((id) =>
        within(region).getByRole("link", { name: `View current Coverage run ${id.slice(0, 8)}` }),
      );
      expect(links.map((link) => link.getAttribute("href"))).toEqual(
        currentJobs.map((id) => `/data-sync/jobs/${id}`),
      );
      await userEvent.click(links[1]);
      expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${currentJobs[1]}`);
    },
  );

  test("keeps mixed current and retryable failures actionable without a Superseded parent", async () => {
    const mixed: JobDetail = {
      ...coverageFailure,
      status: "partially_succeeded",
      displayStatus: "partially_succeeded",
      retryEligibility: {
        ...eligibleCoverageRetry,
        eligibleRouteCount: 1,
        currentRouteCount: 1,
        currentCoverageJobIds: ["E".repeat(32)],
      },
    };
    baseFetch((path) => (path === `/api/jobs/${JOB_ID}` ? json(mixed) : undefined));
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(
      await within(region).findByText("Partially succeeded", { selector: ".sync-status" }),
    ).toBeVisible();
    expect(within(region).getByRole("button", { name: "Retry run" })).toBeEnabled();
    expect(within(region).queryByText("Superseded")).not.toBeInTheDocument();
    expect(
      within(region).queryByRole("note", { name: "Retry availability" }),
    ).not.toBeInTheDocument();
    expect(within(region).getByRole("link", { name: /View current Coverage run/ })).toBeVisible();
  });

  test.each([
    ["no_eligible_routes", /no current failed or cancelled route targets eligible/],
    ["not_latest", /Open the latest attempt/],
    ["retry_limit", /reached its retry limit/],
  ] as const)("explains blocked coverage retry: %s", async (reason, copy) => {
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}`
        ? json({
            ...coverageFailure,
            retryEligibility: {
              ...eligibleCoverageRetry,
              canRetry: false,
              reason,
              eligibleRouteCount: 0,
            },
          })
        : undefined,
    );
    renderDataSync(JOB_ID);
    const retry = await screen.findByRole("button", { name: "Retry run" });
    expect(retry).toBeDisabled();
    expect(retry).toHaveAccessibleDescription(copy);
    expect(screen.queryByText("Superseded")).not.toBeInTheDocument();
  });

  test("does not show a retry warning for a normally active run", async () => {
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}`
        ? json({
            ...coverageFailure,
            status: "running",
            retryEligibility: { ...eligibleCoverageRetry, canRetry: false, reason: "not_terminal" },
          })
        : undefined,
    );
    renderDataSync(JOB_ID);
    expect(await screen.findByRole("button", { name: "Cancel run" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Retry run" })).not.toBeInTheDocument();
    expect(screen.queryByRole("note", { name: "Retry availability" })).not.toBeInTheDocument();
  });

  test("refetches a resolved race into Superseded instead of offering another retry", async () => {
    let resolved = false;
    let posts = 0;
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}`)
        return json({
          ...coverageFailure,
          displayStatus: resolved ? "superseded" : "failed",
          retryEligibility: resolved
            ? {
                ...eligibleCoverageRetry,
                canRetry: false,
                reason: "already_current",
                eligibleRouteCount: 0,
                currentRouteCount: 2,
              }
            : eligibleCoverageRetry,
        });
      if (path.endsWith("/retry")) {
        posts += 1;
        resolved = true;
        return json(
          {
            type: "https://workouts-explorer.invalid/problems/coverage-retry/already-current",
            title: "Private SQL title",
            detail: "private SQL detail",
          },
          409,
        );
      }
      return undefined;
    });
    renderDataSync(JOB_ID);
    await userEvent.click(await screen.findByRole("button", { name: "Retry run" }));
    expect(await screen.findByText("Superseded", { selector: ".sync-status" })).toBeVisible();
    await waitFor(() =>
      expect(screen.getByRole("status", { name: "Sync feedback" })).toHaveTextContent(
        "No action is needed.",
      ),
    );
    expect(screen.getByRole("button", { name: "Retry run" })).toBeDisabled();
    expect(document.body).not.toHaveTextContent("Private SQL title");
    expect(document.body).not.toHaveTextContent("private SQL detail");
    expect(posts).toBe(1);
  });

  test("explains transient busy conflicts while keeping genuinely eligible retry available", async () => {
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}`)
        return json({ ...coverageFailure, retryEligibility: eligibleCoverageRetry });
      if (path.endsWith("/retry"))
        return json(
          {
            type: "https://workouts-explorer.invalid/problems/coverage-retry/busy",
            detail: "private lock SQL",
          },
          409,
        );
      return undefined;
    });
    renderDataSync(JOB_ID);
    await userEvent.click(await screen.findByRole("button", { name: "Retry run" }));
    expect(
      await screen.findByText(
        "Coverage work is being updated. Wait a moment, then try Retry run again.",
      ),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "Retry run" })).toBeEnabled();
    expect(document.body).not.toHaveTextContent("private lock SQL");
    expect(screen.queryByText("Superseded")).not.toBeInTheDocument();
  });

  test("refreshes terminal Coverage projections when newer work changes current authority", async () => {
    vi.useFakeTimers();
    let phase: "failed" | "resolved" | "changed" = "failed";
    let detailReads = 0;
    baseFetch((path) => {
      if (path !== `/api/jobs/${JOB_ID}`) return undefined;
      detailReads += 1;
      return json({
        ...coverageFailure,
        displayStatus: phase === "resolved" ? "superseded" : "failed",
        retryEligibility:
          phase === "failed"
            ? eligibleCoverageRetry
            : {
                ...eligibleCoverageRetry,
                canRetry: false,
                eligibleRouteCount: 0,
                reason: phase === "resolved" ? "already_current" : "no_eligible_routes",
                currentRouteCount: phase === "resolved" ? 2 : 0,
              },
      });
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      const region = screen.getByRole("region", { name: "Selected run" });
      expect(within(region).getByRole("button", { name: "Retry run" })).toBeEnabled();
      phase = "resolved";
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1000);
      });
      expect(detailReads).toBe(2);
      expect(view.client.getQueryData<JobDetail>(["job", JOB_ID])?.displayStatus).toBe(
        "superseded",
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(within(region).getByText("Superseded", { selector: ".sync-status" })).toBeVisible();
      expect(within(region).getByRole("button", { name: "Retry run" })).toBeDisabled();
      phase = "changed";
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1000);
      });
      expect(detailReads).toBe(3);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(
        within(region).queryByText("Superseded", { selector: ".sync-status" }),
      ).not.toBeInTheDocument();
      expect(within(region).getByRole("note", { name: "Retry availability" })).toHaveTextContent(
        "no current failed or cancelled route targets eligible",
      );
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });

  test("keeps an authoritative retry block if detail refresh fails, until fresh metadata resolves it", async () => {
    vi.useFakeTimers();
    let rejected = false;
    let refreshRecovered = false;
    let posts = 0;
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}`) {
        if (rejected && !refreshRecovered) return json({ title: "Service Unavailable" }, 503);
        return json({ ...coverageFailure, retryEligibility: eligibleCoverageRetry });
      }
      if (path.endsWith("/retry")) {
        rejected = true;
        posts += 1;
        return json(
          {
            type: "https://workouts-explorer.invalid/problems/coverage-retry/no-eligible-routes",
            detail: "private raw database error",
          },
          409,
        );
      }
      return undefined;
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      fireEvent.click(screen.getByRole("button", { name: "Retry run" }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(9000);
      });
      const button = screen.getByRole("button", { name: "Retry run" });
      expect(button).toBeDisabled();
      expect(button).toHaveAccessibleDescription(
        /no current failed or cancelled route targets eligible/,
      );
      expect(screen.getByText("Run detail is unavailable.")).toBeVisible();
      expect(document.body).not.toHaveTextContent("private raw database error");
      fireEvent.click(button);
      expect(posts).toBe(1);
      refreshRecovered = true;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(screen.getByRole("button", { name: "Retry run" })).toBeEnabled();
      expect(screen.queryByRole("note", { name: "Retry availability" })).not.toBeInTheDocument();
      expect(screen.getByRole("status", { name: "Sync feedback" })).toHaveTextContent(
        "Retry is now available.",
      );
      expect(posts).toBe(1);
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });

  test("does not clear a typed retry block when a refresh lacks eligibility metadata", async () => {
    let rejected = false;
    baseFetch((path) => {
      if (path === `/api/jobs/${JOB_ID}`)
        return json({
          ...coverageFailure,
          ...(rejected ? {} : { retryEligibility: eligibleCoverageRetry }),
        });
      if (path.endsWith("/retry")) {
        rejected = true;
        return json(
          { type: "https://workouts-explorer.invalid/problems/coverage-retry/no-eligible-routes" },
          409,
        );
      }
      return undefined;
    });
    const view = renderDataSync(JOB_ID);
    await userEvent.click(await screen.findByRole("button", { name: "Retry run" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry run" })).toBeDisabled());
    expect(screen.getByRole("button", { name: "Retry run" })).toHaveAccessibleDescription(
      /no current failed or cancelled route targets eligible/,
    );
    expect(view.client.getQueryData<JobDetail>(["job", JOB_ID])?.retryEligibility).toBeUndefined();
    act(() => {
      view.client.setQueryData(["job", JOB_ID], {
        ...coverageFailure,
        displayStatus: "superseded",
        retryEligibility: {
          ...eligibleCoverageRetry,
          canRetry: false,
          reason: "already_current",
          eligibleRouteCount: 0,
          currentRouteCount: 2,
        },
      });
    });
    expect(await screen.findByText("Superseded", { selector: ".sync-status" })).toBeVisible();
    await waitFor(() =>
      expect(screen.getByRole("note", { name: "Retry availability" })).toHaveTextContent(
        "No action is needed.",
      ),
    );
  });

  test.each(["failed", "superseded"] as const)(
    "refreshes mutable %s history and totals even when its filtered page becomes empty",
    async (filter) => {
      vi.useFakeTimers();
      let resolved = filter === "superseded";
      const requests: string[] = [];
      baseFetch((path) => {
        if (!path.startsWith("/api/jobs?")) return undefined;
        requests.push(path);
        const status = new URL(path, "http://test").searchParams.get("status");
        const displayStatus = resolved ? "superseded" : "failed";
        const items =
          !status || status === displayStatus ? [{ ...coverageFailure, displayStatus }] : [];
        return json({
          pagination: {
            page: 1,
            pageSize: 25,
            totalItems: items.length,
            totalPages: items.length ? 1 : 0,
          },
          items,
        });
      });
      const view = renderDataSync(undefined, vi.fn(), 1);
      try {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(0);
        });
        fireEvent.keyDown(screen.getByRole("button", { name: "Filter by status" }), {
          key: "ArrowDown",
          code: "ArrowDown",
        });
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1);
        });
        fireEvent.click(
          screen.getByRole("menuitemradio", {
            name: filter === "failed" ? "Failed" : "Superseded",
          }),
        );
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1);
        });
        const selector = `.sync-status-text--${filter}`;
        expect(document.querySelectorAll(selector)).toHaveLength(2);
        resolved = !resolved;
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1001);
        });
        expect(document.querySelectorAll(selector)).toHaveLength(0);
        expect(
          view.client.getQueryData<JobList>(["jobs", 1, "", filter])?.pagination.totalItems,
        ).toBe(0);
        resolved = !resolved;
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1001);
        });
        expect(document.querySelectorAll(selector)).toHaveLength(2);
        expect(
          view.client.getQueryData<JobList>(["jobs", 1, "", filter])?.pagination.totalItems,
        ).toBe(1);
        expect(
          requests.filter((path) => path.endsWith(`status=${filter}`)).length,
        ).toBeGreaterThanOrEqual(3);
      } finally {
        view.unmount();
        vi.useRealTimers();
      }
    },
  );

  test("uses derived Superseded status in desktop/mobile history and its server-side filter", async () => {
    const requests: string[] = [];
    baseFetch((path) => {
      if (!path.startsWith("/api/jobs?")) return undefined;
      requests.push(path);
      return json({
        pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        items: [
          {
            ...coverageFailure,
            displayStatus: "superseded",
          },
        ],
      });
    });
    renderDataSync();
    await screen.findAllByText("Superseded");
    for (const selector of [".sync-history-table", ".sync-history-cards"]) {
      const container = document.querySelector(selector)!;
      expect(container.querySelector(".sync-status-text--superseded")).toHaveTextContent(
        "Superseded",
      );
      expect(container.querySelector(".sync-status-text--failed")).toBeNull();
    }
    await userEvent.click(screen.getByRole("button", { name: "Filter by status" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Superseded" }));
    await waitFor(() =>
      expect(requests).toContain("/api/jobs?page=1&pageSize=25&status=superseded"),
    );
    expect(screen.getByRole("button", { name: "Filter by status" })).toHaveTextContent(
      "Superseded",
    );
  });

  test("renders deletion targets without sync artifacts and retries the captured set", async () => {
    const deletionDetail: JobDetail = {
      ...detail,
      operation: "workout_deletion",
      status: "failed",
      progress: { ...progress, current: 0, total: 3 },
      failureCode: "workout-delete-failed",
      failureSummary: "The workout deletion could not be completed.",
    };
    let retry: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path === `/api/jobs/${JOB_ID}`) return json(deletionDetail);
      if (path.endsWith("/retry")) {
        retry = { body: String(init?.body), csrf: new Headers(init?.headers).get("X-CSRF-Token") };
        return json(
          { jobId: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", status: "queued", reused: false },
          202,
        );
      }
      return undefined;
    });
    renderDataSync(JOB_ID);
    expect(await screen.findByRole("heading", { name: "Workout deletion" })).toBeInTheDocument();
    expect(screen.getByText("Targets").closest("div")).toHaveTextContent("3");
    expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Run records")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry deletion" }));
    await waitFor(() => expect(retry).toEqual({ body: "{}", csrf: "csrf-data-sync" }));
  });

  test("links a superseded deletion to its latest retry and removes the stale retry action", async () => {
    const latestRetryJobId = "B8EDF88F1D12472AB2AF82EB3F958A07";
    const navigate = vi.fn();
    baseFetch((path) =>
      path === `/api/jobs/${JOB_ID}`
        ? json({
            ...detail,
            operation: "workout_deletion",
            status: "failed",
            latestRetryJobId,
            latestRetryOrdinal: 2,
            retriedByJobIds: [latestRetryJobId],
          })
        : undefined,
    );
    renderDataSync(JOB_ID, navigate);
    const region = await screen.findByRole("region", { name: "Selected run" });
    const link = await within(region).findByRole("link", { name: latestRetryJobId.slice(0, 8) });
    expect(link.closest("dt")).toHaveTextContent(`Retry by job ${latestRetryJobId.slice(0, 8)}`);
    expect(link.closest("div")?.querySelector("dd")).toHaveClass("visually-hidden");
    expect(link.closest("div")).not.toHaveTextContent("Second");
    expect(
      within(region).queryByRole("button", { name: "Retry deletion" }),
    ).not.toBeInTheDocument();
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${latestRetryJobId}`);
  });

  test("uses operation-aware history headings and plain status text on desktop and mobile", async () => {
    baseFetch((path) =>
      path === "/api/jobs?page=1&pageSize=25"
        ? json({
            pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
            items: [
              {
                id: JOB_ID,
                trigger: "manual",
                status: "cancelled",
                progress,
                createdAt: detail.createdAt,
                startedAt: detail.createdAt,
                updatedAt: detail.updatedAt,
              },
            ],
          })
        : undefined,
    );
    renderDataSync();
    expect(await screen.findByRole("heading", { name: "Recent activity" })).toBeInTheDocument();
    await screen.findAllByText("Canceled");
    expect(screen.getByText("Task history", { selector: ".card-kicker" })).toBeInTheDocument();
    const table = document.querySelector(".sync-history-table")!;
    expect(
      within(table as HTMLElement).getByRole("columnheader", { name: "Started" }),
    ).toBeInTheDocument();
    expect(table.querySelector("tbody .sync-status")).toBeNull();
    expect(table.querySelector("tbody .sync-status-text--cancelled")).toHaveTextContent("Canceled");
    expect(Array.from(table.querySelectorAll("col")).map((column) => column.className)).toEqual([
      "history-started-column",
      "history-operation-column",
      "history-results-column",
      "history-status-column",
      "history-action-column",
    ]);
    const cards = document.querySelector(".sync-history-cards")!;
    expect(cards.querySelector(".sync-status-text--cancelled")).toHaveTextContent("Canceled");
    expect(screen.getByText("Enabled", { selector: ".schedule-state" })).toHaveClass(
      "schedule-state--enabled",
    );
  });

  test("keeps dismissed deletion failures discoverable and retryable through task history", async () => {
    const navigate = vi.fn();
    const deletionDetail: JobDetail = {
      ...detail,
      operation: "workout_deletion",
      status: "failed",
      progress: { ...progress, current: 0, total: 1 },
      failureCode: "workout-delete-failed",
      failureSummary: "The workout deletion could not be completed.",
    };
    baseFetch((path) => {
      if (path === "/api/data-sync") return json({ ...snapshot, notifications: [] });
      if (path === "/api/jobs?page=1&pageSize=25")
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
          items: [
            {
              id: JOB_ID,
              operation: "workout_deletion",
              trigger: "manual",
              status: "failed",
              progress: deletionDetail.progress,
              createdAt: detail.createdAt,
              updatedAt: detail.updatedAt,
            },
          ],
        });
      if (path === `/api/jobs/${JOB_ID}`) return json(deletionDetail);
      return undefined;
    });
    const view = renderDataSync(undefined, navigate);
    expect(await screen.findAllByText("Workout deletion")).toHaveLength(2);
    expect(screen.getAllByText("0 of 1 deleted")).toHaveLength(2);
    await userEvent.click(
      within(document.querySelector(".sync-history-table") as HTMLElement).getByRole("button", {
        name: "View detail",
      }),
    );
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
    view.rerenderJob(JOB_ID);
    expect(await screen.findByRole("button", { name: "Retry deletion" })).toBeInTheDocument();
  });

  test("filters history with Radix radio menus, exact queries, Escape, and page reset", async () => {
    const requests: string[] = [];
    baseFetch((path) => {
      if (!path.startsWith("/api/jobs?")) return undefined;
      requests.push(path);
      const page = Number(new URL(path, "http://test").searchParams.get("page"));
      return json({
        pagination: { page, pageSize: 25, totalItems: 26, totalPages: 2 },
        items: [
          {
            id: JOB_ID,
            trigger: "manual",
            status: "failed",
            progress,
            createdAt: detail.createdAt,
            updatedAt: detail.updatedAt,
          },
        ],
      });
    });
    renderDataSync();
    const statusTrigger = await screen.findByRole("button", { name: "Filter by status" });
    const operationTrigger = screen.getByRole("button", { name: "Filter by operation" });
    expect(document.querySelector(".history-filters select")).toBeNull();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(statusTrigger).toHaveTextContent("StatusAll statusesv");
    expect(operationTrigger).toHaveTextContent("OperationAll operationsv");
    expect(document.querySelectorAll(".history-filter-trigger")[0]).toBe(operationTrigger);

    await userEvent.click(await screen.findByRole("button", { name: "Next" }));
    await waitFor(() => expect(requests).toContain("/api/jobs?page=2&pageSize=25"));
    await userEvent.click(statusTrigger);
    expect(await screen.findByRole("menuitemradio", { name: "All statuses" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menuitemradio", { name: /All statuses/ })).not.toBeInTheDocument();
    expect(statusTrigger).toHaveFocus();

    await userEvent.click(statusTrigger);
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Failed" }));
    await waitFor(() => expect(requests).toContain("/api/jobs?page=1&pageSize=25&status=failed"));
    expect(statusTrigger).toHaveTextContent("StatusFailedv");

    await userEvent.click(operationTrigger);
    await screen.findByRole("menuitemradio", { name: "All operations" });
    await userEvent.click(screen.getByRole("menuitemradio", { name: "Automated sync" }));
    await waitFor(() =>
      expect(requests).toContain(
        "/api/jobs?page=1&pageSize=25&operation=automated_sync&status=failed",
      ),
    );
    expect(operationTrigger).toHaveTextContent("OperationAutomated syncv");
  });

  test("aborts an in-flight snapshot when the page unmounts", async () => {
    let snapshotSignal: AbortSignal | null | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/data-sync") {
        snapshotSignal = init?.signal;
        return new Promise(() => undefined);
      }
      if (path.startsWith("/api/jobs?"))
        return Promise.resolve(
          json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }),
        );
      throw new Error(`Unhandled fetch: ${path}`);
    });
    const view = renderDataSync();
    await waitFor(() => expect(snapshotSignal).toBeInstanceOf(AbortSignal));
    expect(snapshotSignal?.aborted).toBe(false);
    view.unmount();
    expect(snapshotSignal?.aborted).toBe(true);
  });
});
