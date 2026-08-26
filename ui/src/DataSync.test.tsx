import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { type DataSync as DataSyncModel, type JobDetail, type JobProgress, type Preferences } from "./api";
import { DataSync } from "./DataSync";

const SOURCE_A = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const SOURCE_B = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";
const SOURCE_C = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC";
const JOB_ID = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDD";
const progress: JobProgress = {
  current: 2, total: 4, filesDiscovered: 4, filesSkipped: 0, filesSucceeded: 1, filesFailed: 1,
  workoutsCreated: 3, workoutsUpdated: 2, workoutsUnchanged: 1, workoutsRejected: 1,
};
const preferences: Preferences = {
  theme: "dark", units: "metric", timezone: "UTC", firstWeekday: "monday", clockFormat: "24h",
  workoutColumns: ["date", "type", "duration"], pageSize: 25, initialized: true,
};
const snapshot: DataSyncModel = {
  schedule: { enabled: true, sourceCount: 1, cadence: "Daily at 02:00", cadenceSeconds: 86400, staleDays: 3, nextRunAt: "2026-08-07T02:00:00Z" },
  sources: [
    { id: SOURCE_A, displayName: "Phone exports", type: "health-auto-export-local", status: "connected", autoSyncEnabled: true, freshness: { lastSyncSucceededAt: "2026-08-06T01:00:00Z", lastNewExportDate: "2026-08-05" } },
    { id: SOURCE_B, displayName: "Watch archive", type: "health-auto-export-local", status: "connected", autoSyncEnabled: false, freshness: {} },
    { id: SOURCE_C, displayName: "Old folder", type: "health-auto-export-local", status: "connection-failed", autoSyncEnabled: true, freshness: { staleSince: "2026-08-01" } },
  ],
  notifications: [{ id: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", type: "source-stale", severity: "warning", state: "remind", subjectType: "source", sourceId: SOURCE_C, title: "Export is stale", message: "No new export has arrived.", createdAt: "2026-08-02T00:00:00Z", updatedAt: "2026-08-02T00:00:00Z", remindAt: "2026-08-08T00:00:00Z" }],
  notificationsTruncated: false,
};
const detail: JobDetail = {
  id: JOB_ID, trigger: "manual", status: "partially_succeeded", attempt: 0, progress, children: [], results: { filesSucceeded: 1, filesFailed: 1, workoutsCreated: 3 },
  retriedByJobIds: [], cancelRequested: false, createdAt: "2026-08-06T12:00:00Z", updatedAt: "2026-08-06T12:05:00Z", terminalAt: "2026-08-06T12:05:00Z",
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" } });
}

function renderDataSync(selectedJobId?: string, navigate = vi.fn(), pollingIntervalSeconds = 30, userPreferences = preferences) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const component = (jobId = selectedJobId) => <QueryClientProvider client={client}><DataSync csrfToken="csrf-data-sync" preferences={userPreferences} pollingIntervalSeconds={pollingIntervalSeconds} selectedJobId={jobId} navigate={navigate} /></QueryClientProvider>;
  const view = render(component());
  return { ...view, client, navigate, rerenderJob: (jobId?: string) => view.rerender(component(jobId)) };
}

function baseFetch(handler?: (path: string, init?: RequestInit) => Response | undefined) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const path = String(input);
    const handled = handler?.(path, init);
    if (handled) return Promise.resolve(handled);
    if (path === "/api/data-sync") return Promise.resolve(json(snapshot));
    if (path === "/api/jobs?page=1&pageSize=25") return Promise.resolve(json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }));
    if (path === `/api/jobs/${JOB_ID}`) return Promise.resolve(json(detail));
    throw new Error(`Unhandled fetch: ${init?.method ?? "GET"} ${path}`);
  });
}

describe("DataSync", () => {
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
        request = { body: JSON.parse(String(init.body)), csrf: new Headers(init.headers).get("X-CSRF-Token") };
        return json({ jobId: JOB_ID, status: "queued", reused: true }, 202);
      }
      return undefined;
    });
    const { navigate } = renderDataSync();
    expect(await screen.findByRole("checkbox", { name: /Phone exports/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Watch archive/ })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Old folder/ })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() => expect(request).toEqual({ body: { sourceIds: [SOURCE_A] }, csrf: "csrf-data-sync" }));
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
    expect(await screen.findByText("Existing run selected.")).toBeInTheDocument();
  });

  test("validates bounded dates and sends only contract fields", async () => {
    let body: unknown;
    baseFetch((path, init) => {
      if (path === "/api/ingest") { body = JSON.parse(String(init?.body)); return json({ jobId: JOB_ID, status: "queued", reused: false }, 202); }
      return undefined;
    });
    renderDataSync();
    await screen.findByRole("checkbox", { name: /Phone exports/ });
    await userEvent.click(screen.getByRole("radio", { name: /Specific date range/ }));
    await userEvent.type(screen.getByLabelText("Start date"), "2026-02-28");
    await userEvent.type(screen.getByLabelText("End date"), "2026-02-30");
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("both inclusive dates");
    await waitFor(() => expect(alert).toHaveFocus());
    expect(screen.getByLabelText("End date")).toHaveAccessibleDescription("Enter both inclusive dates.");
    expect(body).toBeUndefined();
    await userEvent.clear(screen.getByLabelText("Start date"));
    await userEvent.type(screen.getByLabelText("Start date"), "2026-08-07");
    await userEvent.clear(screen.getByLabelText("End date"));
    await userEvent.type(screen.getByLabelText("End date"), "2026-08-07");
    await userEvent.click(screen.getByRole("button", { name: "Start sync" }));
    await waitFor(() => expect(body).toEqual({ sourceIds: [SOURCE_A], startDate: "2026-08-07", endDate: "2026-08-07" }));
  });

  test("renders persisted partial progress and does not fetch logs before disclosure", async () => {
    const fetchMock = baseFetch((path) => path === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`
      ? json({ pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 }, items: [{ id: 1, jobId: JOB_ID, severity: "info", code: "import-finished", message: "Import finished safely", fields: { files: 2 }, createdAt: "2026-08-06T12:05:00Z" }] })
      : undefined);
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(await within(region).findByText("Partially completed")).toBeInTheDocument();
    expect(within(region).getByRole("progressbar")).toHaveAttribute("value", "2");
    expect(within(region).getByText("2 of 4 files")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/logs?"))).toBe(false);
    await userEvent.click(within(region).getByRole("button", { name: /Logs/ }));
    expect(await within(region).findByText(/Import finished safely/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input) === `/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`)).toBe(true);
  });

  test("dismisses reminders with an empty body and server pagination stays authoritative", async () => {
    let dismissal: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path.includes("page=2&pageSize=25")) return json({ pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 2 }, items: [] });
      if (path.endsWith("/dismissal")) { dismissal = { body: String(init?.body), csrf: new Headers(init?.headers).get("X-CSRF-Token") }; return json({ ...snapshot.notifications[0], state: "dismissed" }); }
      if (path === "/api/jobs?page=1&pageSize=25") return json({ pagination: { page: 1, pageSize: 25, totalItems: 26, totalPages: 2 }, items: [{ id: JOB_ID, trigger: "manual", status: "partially_succeeded", progress, createdAt: detail.createdAt, updatedAt: detail.updatedAt }] });
      return undefined;
    });
    renderDataSync();
    expect(await screen.findByText("Export is stale")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(dismissal).toEqual({ body: "{}", csrf: "csrf-data-sync" }));
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("No tasks match these filters.")).toBeInTheDocument();
  });

  test("keeps embedded notifications authoritative, marks truncation statically, and links only job notifications", async () => {
    const navigate = vi.fn();
    const jobNotification = { ...snapshot.notifications[0], id: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", subjectType: "job" as const, sourceId: undefined, jobId: JOB_ID, title: "Workout deletion failed", message: "The workout could not be deleted, but you can retry the task." };
    baseFetch((path) => path === "/api/data-sync" ? json({ ...snapshot, notifications: [...snapshot.notifications, jobNotification], notificationsTruncated: true }) : undefined);
    renderDataSync(undefined, navigate);
    const stale = await screen.findByText("Export is stale");
    expect(screen.getByText("The workout could not be deleted, but you can retry the task.")).toBeInTheDocument();
    expect(stale.closest("article")).not.toHaveAttribute("role");
    expect(screen.getByText("More notifications are available.")).not.toHaveAttribute("role");
    expect(screen.getAllByRole("button", { name: "View detail" })).toHaveLength(1);
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).startsWith("/api/notifications"))).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "View detail" }));
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${JOB_ID}`);
  });

  test("shows status-aware zero progress, labels creation as queued, and omits the API attempt", async () => {
    const zero = { ...progress, current: 0, total: 0, filesDiscovered: 0, filesSucceeded: 0, filesFailed: 0, workoutsCreated: 0, workoutsUpdated: 0, workoutsUnchanged: 0, workoutsRejected: 0 };
    baseFetch((path) => path === `/api/jobs/${JOB_ID}` ? json({ ...detail, status: "succeeded", attempt: 4, progress: zero, results: {} }) : undefined);
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    expect(await within(region).findByText("No new data.")).toBeInTheDocument();
    expect(within(region).queryByRole("progressbar")).not.toBeInTheDocument();
    expect(within(region).queryByText(/Discovering files/)).not.toBeInTheDocument();
    const queued = within(region).getByText("Queued").closest("div")!;
    expect(queued.querySelector("dd")).not.toHaveTextContent("Never");
    expect(within(region).queryByText("Attempt")).not.toBeInTheDocument();
    expect(within(region).queryByText(/Retry of job/)).not.toBeInTheDocument();
    expect(region.querySelector(".job-actions")).not.toBeInTheDocument();
    expect(region).not.toHaveFocus();
  });

  test.each([[1, "First", ""], [2, "Second", "first"], [5, "Fifth", "fourth"]] as const)("renders retry ordinal %i with root and previous-job navigation", async (retryOrdinal, ordinalLabel, previousLabel) => {
    const rootJobId = "ABCDEF1234567890ABCDEF1234567890";
    const previousJobId = "1234567890ABCDEF1234567890ABCDEF";
    const navigate = vi.fn();
    baseFetch((path) => path === `/api/jobs/${JOB_ID}` ? json({
      ...detail,
      retryRootJobId: rootJobId,
      retryOrdinal,
      retryOfJobId: retryOrdinal > 1 ? previousJobId : rootJobId,
      latestRetryJobId: retryOrdinal < 5 ? "FEDCBA0987654321FEDCBA0987654321" : undefined,
      latestRetryOrdinal: retryOrdinal < 5 ? 5 : undefined,
    }) : undefined);
    renderDataSync(JOB_ID, navigate);
    const region = await screen.findByRole("region", { name: "Selected run" });
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
  });

  test("renders result counts in the approved DOM order with derived values and rejected help", async () => {
    baseFetch((path) => path === `/api/jobs/${JOB_ID}` ? json({ ...detail, results: undefined }) : undefined);
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Files Processed");
    const counts = region.querySelector(".result-counts")!;
    expect([...counts.querySelectorAll("dt")].map((node) => node.textContent)).toEqual([
      "Files Processed", "Workouts Imported", "Workouts Unchanged", "Files Failed", "Workouts Updated", "Workouts Rejected",
    ]);
    expect([...counts.querySelectorAll("dd")].map((node) => node.textContent)).toEqual(["2", "3", "1", "1", "2", "1"]);
    const rejected = within(counts as HTMLElement).getByText("Workouts Rejected").closest("div")!.querySelector("dd")!;
    expect(rejected).toHaveAccessibleDescription("Workout records skipped while their source file continued processing.");
    expect(rejected).not.toHaveAttribute("title");
  });

  test("renders source identity and type on one heading row with its status at the right", async () => {
    const child: JobDetail = {
      ...detail,
      id: "22222222222222222222222222222222",
      status: "succeeded",
      source: { sourceId: SOURCE_A, generation: 1, displayName: "Phone exports", sourceType: "health-auto-export-local" },
      children: [],
    };
    baseFetch((path) => path === `/api/jobs/${JOB_ID}` ? json({ ...detail, children: [child] }) : undefined);
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Source runs");
    const heading = region.querySelector(".source-run-heading")!;
    expect(heading).toHaveTextContent("Phone exports (Health Auto Export)");
    expect(heading.querySelector("strong")).toHaveTextContent("Phone exports");
    expect(heading.lastElementChild).toHaveClass("sync-status", "sync-status--succeeded");
    expect(heading.lastElementChild).toHaveTextContent("Completed");
    expect(heading.lastElementChild).toHaveStyle({ position: "relative", top: ".2rem" });
    expect(region.querySelector(".source-run .sync-progress")).toHaveStyle({ margin: ".7rem 0 -.3rem 0" });
  });

  test("uses filled semantic detail statuses and explicit schedule states", async () => {
    const statuses = ["succeeded", "failed", "partially_succeeded", "cancelled"] as const;
    const children = statuses.map((status, index) => ({ ...detail, id: String(index + 3).repeat(32), status, children: [] }));
    baseFetch((path) => {
      if (path === "/api/data-sync") return json({ ...snapshot, schedule: { ...snapshot.schedule, enabled: false } });
      if (path === `/api/jobs/${JOB_ID}`) return json({ ...detail, status: "succeeded", children });
      return undefined;
    });
    renderDataSync(JOB_ID);
    const region = await screen.findByRole("region", { name: "Selected run" });
    await within(region).findByText("Source runs");
    for (const status of statuses) expect(region.querySelector(`.sync-status--${status}`)).toBeInTheDocument();
    expect(screen.getByText("Disabled", { selector: ".schedule-state" })).toHaveClass("schedule-state--disabled");
    expect(within(region).getByText("Canceled")).toHaveClass("sync-status--cancelled");
  });

  test("resets lazy artifacts when the selected job changes", async () => {
    const nextJob = "11111111111111111111111111111111";
    const logRequests: string[] = [];
    baseFetch((path) => {
      if (path === `/api/jobs/${nextJob}`) return json({ ...detail, id: nextJob, status: "failed" });
      if (path.includes("/logs?")) { logRequests.push(path); return json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }); }
      return undefined;
    });
    const view = renderDataSync(JOB_ID);
    const firstRegion = await screen.findByRole("region", { name: "Selected run" });
    await within(firstRegion).findByText("Partially completed");
    await userEvent.click(within(firstRegion).getByRole("button", { name: /Logs/ }));
    await within(firstRegion).findByText("No logs recorded.");
    view.rerenderJob(nextJob);
    const nextRegion = screen.getByRole("region", { name: "Selected run" });
    await within(nextRegion).findByText("Failed");
    expect(within(nextRegion).getByRole("button", { name: /Logs/ })).toHaveAttribute("aria-expanded", "false");
    expect(logRequests).toEqual([`/api/jobs/${JOB_ID}/logs?page=1&pageSize=25`]);
  });

  test("preserves manual source selection when the snapshot refreshes", async () => {
    let snapshotCalls = 0;
    baseFetch((path) => {
      if (path === "/api/data-sync") {
        snapshotCalls += 1;
        return json(snapshotCalls === 1 ? snapshot : { ...snapshot, sources: snapshot.sources.map((source) => ({ ...source, autoSyncEnabled: source.id === SOURCE_B })) });
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
    baseFetch((path) => path === "/api/data-sync" ? json({
      ...snapshot,
      schedule: { ...snapshot.schedule, cadence: null, cadenceSeconds: 43200, staleDays: 1 },
    }) : undefined);
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
      ["Stale after", "Show a warning after this period if no new workouts have been imported by auto-sync"],
      ["Next run", "Local date/time when the next sync job will run"],
    ];
    for (const [label, content] of expected) {
      const trigger = within(card).getByRole("button", { name: `About ${label}` });
      expect(trigger).toHaveAccessibleDescription(content);
      expect(document.getElementById(trigger.getAttribute("aria-describedby")!)).toHaveAttribute("role", "tooltip");
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
    ["summer PDT", "2026-07-15T19:30:00Z", "America/Los_Angeles", "12h" as const, "PDT", "America/Los_Angeles (UTC-7:00)"],
    ["winter PST", "2026-01-15T20:30:00Z", "America/Los_Angeles", "24h" as const, "PST", "America/Los_Angeles (UTC-8:00)"],
    ["half-hour offset", "2026-08-07T02:00:00Z", "Asia/Kolkata", "24h" as const, "GMT+5:30", "Asia/Kolkata (UTC+5:30)"],
  ])("formats the Next run instant with its %s badge", async (_case, nextRunAt, timezone, clockFormat, badgeText, description) => {
    baseFetch((path) => path === "/api/data-sync" ? json({ ...snapshot, schedule: { ...snapshot.schedule, nextRunAt } }) : undefined);
    renderDataSync(undefined, vi.fn(), 30, { ...preferences, timezone, clockFormat });
    const badge = await screen.findByLabelText(description);
    const card = badge.closest("section")!;
    const expectedDateTime = new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium", timeStyle: "short", timeZone: timezone, hour12: clockFormat === "12h",
    }).format(new Date(nextRunAt));
    expect(badge).toHaveTextContent(badgeText);
    expect(badge).toHaveAccessibleDescription(description);
    expect(badge).not.toHaveAttribute("title");
    expect(within(card).getByText("Next run").closest("div")?.querySelector("dd .next-run-value")).toHaveTextContent(expectedDateTime);
  });

  test("formats fractional-hour intervals exactly and handles absent or invalid Next run zones truthfully", async () => {
    baseFetch((path) => path === "/api/data-sync" ? json({
      ...snapshot,
      schedule: { ...snapshot.schedule, cadence: "Ignored server cadence", cadenceSeconds: 4500, nextRunAt: undefined },
    }) : undefined);
    const view = renderDataSync();
    await screen.findByText("Interval");
    const card = screen.getByRole("heading", { name: "Schedule & freshness" }).closest("section")!;
    expect(within(card).getByText("Interval").closest("div")).toHaveTextContent("Every 1.25 hours");
    expect(within(card).getByText("Next run").closest("div")).toHaveTextContent("Never");
    expect(within(card).queryByText("Ignored server cadence")).not.toBeInTheDocument();
    expect(within(card).queryByText("TZ unavailable")).not.toBeInTheDocument();
    view.unmount();

    vi.mocked(fetch).mockRestore();
    baseFetch((path) => path === "/api/data-sync" ? json(snapshot) : undefined);
    renderDataSync(undefined, vi.fn(), 30, { ...preferences, timezone: "Not/AZone" });
    await screen.findByLabelText("Not/AZone unavailable; displayed in UTC (UTC+0:00)");
    const invalidCard = screen.getByRole("heading", { name: "Schedule & freshness" }).closest("section")!;
    expect(within(invalidCard).getByLabelText("Not/AZone unavailable; displayed in UTC (UTC+0:00)")).toHaveTextContent("UTC");
    expect(within(invalidCard).getByText("Next run").closest("div")).not.toHaveTextContent("Never");
  });

  test("polls the snapshot while idle and stops detail polling at terminal cancel-requested state", async () => {
    vi.useFakeTimers();
    let snapshots = 0;
    let details = 0;
    let historyRequests = 0;
    const running = { ...detail, status: "running" as const, terminalAt: undefined, cancelRequested: false };
    baseFetch((path) => {
      if (path === "/api/data-sync") { snapshots += 1; return json(snapshots === 1 ? snapshot : { ...snapshot, activeJob: { ...running, children: undefined, attempt: undefined, retriedByJobIds: undefined, cancelRequested: undefined } }); }
      if (path.startsWith("/api/jobs?")) { historyRequests += 1; return json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }); }
      if (path === `/api/jobs/${JOB_ID}`) { details += 1; return json(details === 1 ? running : { ...detail, status: "cancelled", cancelRequested: true }); }
      return undefined;
    });
    const view = renderDataSync(JOB_ID, vi.fn(), 1);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(details).toBe(1);
    expect(snapshots).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(details).toBe(2);
    expect(snapshots).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
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
        return json({ ...detail, operation: "workout_deletion", status: detailReads === 1 ? "running" : "succeeded", progress: detailReads === 1 ? runningProgress : completedProgress });
      }
      if (path === "/api/jobs?page=1&pageSize=25") {
        historyReads += 1;
        const completed = historyReads > 1;
        return json({
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
          items: [{ id: JOB_ID, operation: "workout_deletion", trigger: "manual", status: completed ? "succeeded" : "running", progress: completed ? completedProgress : runningProgress, createdAt: detail.createdAt, updatedAt: detail.updatedAt }],
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
    expect(historyReads).toBe(2);
  });

  test("cancellation posts an empty body and refetches snapshot, history, and detail", async () => {
    const running = { ...detail, status: "running" as const, terminalAt: undefined, cancelRequested: false };
    let snapshotReads = 0;
    let historyReads = 0;
    let detailReads = 0;
    let cancellation: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path === "/api/data-sync") { snapshotReads += 1; return json(snapshot); }
      if (path.startsWith("/api/jobs?")) { historyReads += 1; return json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }); }
      if (path === `/api/jobs/${JOB_ID}`) { detailReads += 1; return json(running); }
      if (path.endsWith("/cancellation")) { cancellation = { body: String(init?.body), csrf: new Headers(init?.headers).get("X-CSRF-Token") }; return json({ ...running, cancelRequested: true }); }
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
      if (path === `/api/jobs/${JOB_ID}`) { detailReads += 1; return json(detail); }
      if (path.endsWith("/retry")) return json({ title: "Worker lease secret", detail: "private detail" }, 409);
      return undefined;
    });
    renderDataSync(JOB_ID);
    expect(await screen.findByRole("button", { name: "Retry run" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry run" }));
    expect(await screen.findByText("The run changed before retry could be started.")).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("Worker lease secret");
    expect(detailReads).toBeGreaterThan(1);
  });

  test("renders deletion targets without sync artifacts and retries the captured set", async () => {
    const deletionDetail: JobDetail = {
      ...detail, operation: "workout_deletion", status: "failed", progress: { ...progress, current: 0, total: 3 },
      failureCode: "workout-delete-failed", failureSummary: "The workout deletion could not be completed.",
    };
    let retry: { body: string; csrf: string | null } | undefined;
    baseFetch((path, init) => {
      if (path === `/api/jobs/${JOB_ID}`) return json(deletionDetail);
      if (path.endsWith("/retry")) {
        retry = { body: String(init?.body), csrf: new Headers(init?.headers).get("X-CSRF-Token") };
        return json({ jobId: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", status: "queued", reused: false }, 202);
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
    baseFetch((path) => path === `/api/jobs/${JOB_ID}` ? json({
      ...detail,
      operation: "workout_deletion",
      status: "failed",
      latestRetryJobId,
      latestRetryOrdinal: 2,
      retriedByJobIds: [latestRetryJobId],
    }) : undefined);
    renderDataSync(JOB_ID, navigate);
    const region = await screen.findByRole("region", { name: "Selected run" });
    const link = await within(region).findByRole("link", { name: latestRetryJobId.slice(0, 8) });
    expect(link.closest("dt")).toHaveTextContent(`Retry by job ${latestRetryJobId.slice(0, 8)}`);
    expect(link.closest("div")?.querySelector("dd")).toHaveClass("visually-hidden");
    expect(link.closest("div")).not.toHaveTextContent("Second");
    expect(within(region).queryByRole("button", { name: "Retry deletion" })).not.toBeInTheDocument();
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith(`/data-sync/jobs/${latestRetryJobId}`);
  });

  test("uses operation-aware history headings and plain desktop status while retaining mobile badges", async () => {
    baseFetch((path) => path === "/api/jobs?page=1&pageSize=25" ? json({
      pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
      items: [{ id: JOB_ID, trigger: "manual", status: "cancelled", progress, createdAt: detail.createdAt, startedAt: detail.createdAt, updatedAt: detail.updatedAt }],
    }) : undefined);
    renderDataSync();
    expect(await screen.findByRole("heading", { name: "Recent activity" })).toBeInTheDocument();
    await screen.findAllByText("Canceled");
    expect(screen.getByText("Task history", { selector: ".card-kicker" })).toBeInTheDocument();
    const table = document.querySelector(".sync-history-table")!;
    expect(within(table as HTMLElement).getByRole("columnheader", { name: "Started" })).toBeInTheDocument();
    expect(table.querySelector("tbody .sync-status")).toBeNull();
    expect(table.querySelector("tbody .sync-status-text--cancelled")).toHaveTextContent("Canceled");
    const cards = document.querySelector(".sync-history-cards")!;
    expect(cards.querySelector(".sync-status--cancelled")).toHaveTextContent("Canceled");
    expect(screen.getByText("Enabled", { selector: ".schedule-state" })).toHaveClass("schedule-state--enabled");
  });

  test("keeps dismissed deletion failures discoverable and retryable through task history", async () => {
    const navigate = vi.fn();
    const deletionDetail: JobDetail = {
      ...detail, operation: "workout_deletion", status: "failed", progress: { ...progress, current: 0, total: 1 },
      failureCode: "workout-delete-failed", failureSummary: "The workout deletion could not be completed.",
    };
    baseFetch((path) => {
      if (path === "/api/data-sync") return json({ ...snapshot, notifications: [] });
      if (path === "/api/jobs?page=1&pageSize=25") return json({
        pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        items: [{ id: JOB_ID, operation: "workout_deletion", trigger: "manual", status: "failed", progress: deletionDetail.progress, createdAt: detail.createdAt, updatedAt: detail.updatedAt }],
      });
      if (path === `/api/jobs/${JOB_ID}`) return json(deletionDetail);
      return undefined;
    });
    const view = renderDataSync(undefined, navigate);
    expect(await screen.findAllByText("Workout deletion")).toHaveLength(2);
    expect(screen.getAllByText("0 of 1 deleted")).toHaveLength(2);
    await userEvent.click(within(document.querySelector(".sync-history-table") as HTMLElement).getByRole("button", { name: "View detail" }));
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
        items: [{ id: JOB_ID, trigger: "manual", status: "failed", progress, createdAt: detail.createdAt, updatedAt: detail.updatedAt }],
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
    expect(await screen.findByRole("menuitemradio", { name: "All statuses" })).toHaveAttribute("aria-checked", "true");
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
    await waitFor(() => expect(requests).toContain("/api/jobs?page=1&pageSize=25&operation=automated_sync&status=failed"));
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
      if (path.startsWith("/api/jobs?")) return Promise.resolve(json({ pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }));
      throw new Error(`Unhandled fetch: ${path}`);
    });
    const view = renderDataSync();
    await waitFor(() => expect(snapshotSignal).toBeInstanceOf(AbortSignal));
    expect(snapshotSignal?.aborted).toBe(false);
    view.unmount();
    expect(snapshotSignal?.aborted).toBe(true);
  });
});
