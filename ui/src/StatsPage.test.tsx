import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import type { DateRangePreference, NewCoverageBucket, Preferences, StatsReportScope } from "./api";
import { DEFAULT_STATS_STATE, StatsPage, statsBucketLabel, type StatsMapTarget } from "./StatsPage";

const preferences: Preferences = {
  theme: "dark",
  units: "imperial",
  timezone: "America/Denver",
  firstWeekday: "monday",
  clockFormat: "12h",
  workoutColumns: ["date", "distance"],
  pageSize: 25,
  customDateRangeLimit: 3,
  recentCustomDateRanges: ["2025-01-01/2025-12-31"],
  initialized: true,
  coverageDiagnosticsEnabled: false,
  dateRange: "last30Days",
};
const initialRange = "2024-03-25/2026-11-30";
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });

function mockReports(
  intercept?: (url: URL, init?: RequestInit) => Response | Promise<Response> | undefined,
) {
  const scopes = new Map<string, StatsReportScope>();
  let sequence = 0;
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "https://example.test");
    const override = intercept?.(url, init);
    if (override) return override;
    if (url.pathname === "/api/me/preferences") {
      const { dateRange } = JSON.parse(String(init?.body));
      return json({ ...preferences, dateRange, recentCustomDateRanges: [dateRange] });
    }
    if (url.pathname === "/api/stats/report-scopes") {
      const body = JSON.parse(String(init?.body));
      const id = String(++sequence).padStart(32, "0");
      const scope: StatsReportScope = {
        id,
        dataGeneration: sequence,
        expiresAt: new Date(Date.now() + 1800000).toISOString(),
        range: {
          startDate: body.startDate ?? "2025-01-01",
          endDate: body.endDate ?? "2025-12-31",
          timezone: preferences.timezone,
        },
        coverageDisplayRevision: 1,
        coverageDisplayPolicy: "coverage-display-identity-v1",
      };
      scopes.set(id, scope);
      return json(scope, 201);
    }
    if (init?.method === "DELETE") return new Response(null, { status: 204 });
    const scope = scopes.get(url.pathname.split("/")[4]);
    if (!scope) throw new Error(`Unexpected Stats request ${url}`);
    const group = url.searchParams.get("groupBy") ?? "month";
    const page = Number(url.searchParams.get("page") ?? 1);
    const size = Number(url.searchParams.get("pageSize") ?? 25);
    if (url.pathname.endsWith("/visits")) {
      const metric = url.searchParams.get("metric");
      const visits = [
        {
          entityId: "A".repeat(32),
          entityKind: "path",
          name: "Return Road",
          place: "Fixture Town",
          newVisit: "2026-11-10",
          previousVisit: "2024-11-09",
          newVisitWorkoutId: "C".repeat(32),
          previousVisitWorkoutId: "D".repeat(32),
        },
        {
          entityId: "B".repeat(32),
          entityKind: "park",
          name: "First Park",
          place: "Fixture County",
          newVisit: "2026-11-03",
          previousVisit: null,
          newVisitWorkoutId: "E".repeat(32),
          previousVisitWorkoutId: null,
        },
      ].filter((visit) => metric !== "all_time" || visit.previousVisit === null);
      const search = url.searchParams.get("search")?.toLowerCase() ?? "";
      const filtered = visits.filter((visit) =>
        `${visit.name} ${visit.place}`.toLowerCase().includes(search),
      );
      return json({
        scope,
        groupBy: group,
        bucketStart: url.searchParams.get("bucketStart"),
        metric,
        totalVisits: visits.length,
        items: filtered.slice((page - 1) * size, page * size),
        pagination: {
          page,
          pageSize: size,
          totalItems: filtered.length,
          totalPages: Math.ceil(filtered.length / size),
        },
      });
    }
    const items: NewCoverageBucket[] = [];
    const first = new Date(
      `${scope.range.startDate.slice(0, group === "month" ? 7 : 4)}${group === "month" ? "-01" : "-01-01"}T00:00:00Z`,
    );
    while (first.toISOString().slice(0, 10) <= scope.range.endDate) {
      const bucketStart = first.toISOString().slice(0, 10);
      const nonzero = bucketStart === (group === "month" ? "2026-11-01" : "2026-01-01");
      items.unshift({ bucketStart, allTime: nonzero ? 1 : 0, pastYear: nonzero ? 2 : 0 });
      if (group === "month") first.setUTCMonth(first.getUTCMonth() + 1);
      else first.setUTCFullYear(first.getUTCFullYear() + 1);
    }
    return json({
      scope,
      groupBy: group,
      items: items.slice((page - 1) * size, page * size),
      pagination: {
        page,
        pageSize: size,
        totalItems: items.length,
        totalPages: Math.ceil(items.length / size),
      },
    });
  });
  return { fetch, scopes };
}

function Harness({
  pageSize = 25,
  onShowOnMap,
  startingRange = initialRange,
}: {
  pageSize?: number;
  onShowOnMap?: (target: StatsMapTarget) => void;
  startingRange?: DateRangePreference;
}) {
  const [range, setRange] = useState<DateRangePreference>(startingRange);
  const [state, setState] = useState(DEFAULT_STATS_STATE);
  const [visible, setVisible] = useState(true);
  const [recent, setRecent] = useState(preferences.recentCustomDateRanges);
  return (
    <>
      <button onClick={() => setVisible((old) => !old)}>
        {visible ? "Switch to Map" : "Return to Stats"}
      </button>
      <button onClick={() => setRange("2025-01-01/2025-12-31")}>External range change</button>
      {visible && (
        <StatsPage
          accountKey="account-session"
          preferences={{ ...preferences, pageSize, recentCustomDateRanges: recent }}
          csrfToken="csrf"
          dateRange={range}
          onDateRangeSelected={setRange}
          onRecentDateRangesSaved={setRecent}
          state={state}
          onStateChange={setState}
          pollingIntervalSeconds={30}
          onShowOnMap={onShowOnMap}
        />
      )}
    </>
  );
}

describe("Stats New Coverage", () => {
  test("keyboard grouping shortcuts use the existing report transition and leave text entry alone", async () => {
    mockReports();
    render(<Harness />);
    await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" });
    fireEvent.keyDown(document, { key: "Y" });
    expect(screen.getByRole("button", { name: "Year", pressed: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await screen.findByRole("button", { name: "New visits (past year) in 2026: 2" });
    fireEvent.keyDown(document, { key: "m" });
    expect(screen.getByRole("button", { name: "Month", pressed: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" });
    const text = document.createElement("textarea");
    document.body.append(text);
    fireEvent.keyDown(document, { key: "y" });
    expect(screen.getByRole("button", { name: "Month", pressed: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    text.remove();
  });

  test.each([200, 503])(
    "replaces search cells in place and ignores a late obsolete search (%i)",
    async (status) => {
      const releases = new Map<string, (response: Response) => void>();
      const { scopes } = mockReports((url) => {
        const search = url.searchParams.get("search");
        if (url.pathname.endsWith("/visits") && (search === "Old" || search === "New"))
          return new Promise<Response>((resolve) => releases.set(search, resolve));
      });
      render(<Harness onShowOnMap={vi.fn()} />);
      const user = userEvent.setup();
      await user.click(
        await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" }),
      );
      const dialog = await screen.findByRole("dialog"),
        input = within(dialog).getByRole("searchbox");
      await within(dialog).findByText("Return Road");
      const row = dialog.querySelector(".stats-visits-table tbody tr"),
        cell = row?.firstElementChild;
      fireEvent.change(input, { target: { value: "Old" } });
      await waitFor(() => expect(releases.has("Old")).toBe(true));
      expect(row).toBeInTheDocument();
      expect(within(dialog).getByText("Return Road")).toBeVisible();
      expect(within(dialog).queryByText("Loading visits...")).not.toBeInTheDocument();
      fireEvent.change(input, { target: { value: "New" } });
      await waitFor(() => expect(releases.has("New")).toBe(true));
      const response = (name: string, id: string) =>
        json({
          scope: [...scopes.values()][0],
          groupBy: "month",
          bucketStart: "2026-11-01",
          metric: "past_year",
          totalVisits: 2,
          items: [
            {
              entityId: id,
              entityKind: "path",
              name,
              place: "Fixture City",
              newVisit: "2026-11-10",
              previousVisit: null,
              newVisitWorkoutId: "C".repeat(32),
              previousVisitWorkoutId: null,
            },
          ],
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        });
      await act(async () => releases.get("New")!(response("New road", "9".repeat(32))));
      await within(dialog).findByText("New road");
      expect(dialog.querySelector(".stats-visits-table tbody tr")).toBe(row);
      expect(row?.firstElementChild).toBe(cell);
      expect(dialog.querySelectorAll(".stats-visits-table tbody tr[hidden]")).toHaveLength(1);
      await act(async () =>
        releases.get("Old")!(
          status === 200
            ? response("Old road", "F".repeat(32))
            : json({ title: "Service unavailable" }, 503),
        ),
      );
      expect(within(dialog).getByText("New road")).toBeVisible();
      expect(within(dialog).queryByText("Old road")).not.toBeInTheDocument();
      expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      expect(input).toHaveValue("New");
    },
  );

  test("freezes visit capacity from totalVisits through search, empty results and deferred replacement", async () => {
    const visits = Array.from({ length: 60 }, (_, i) => ({
      entityId: (i + 1).toString(16).padStart(32, "0").toUpperCase(),
      entityKind: "path",
      name: i === 0 ? "Only Target" : "Fixture Road " + i,
      place: "Fixture City",
      newVisit: "2026-11-10",
      previousVisit: null,
      newVisitWorkoutId: "C".repeat(32),
      previousVisitWorkoutId: null,
    }));
    let resolveSearch: ((response: Response) => void) | undefined;
    const { scopes, fetch } = mockReports((url) => {
      const scope = scopes.get(url.pathname.split("/")[4]);
      if (url.pathname.endsWith("/new-coverage"))
        return json({
          scope,
          groupBy: "month",
          items: [{ bucketStart: "2026-11-01", allTime: 60, pastYear: 60 }],
          pagination: { page: 1, pageSize: 100, totalItems: 1, totalPages: 1 },
        });
      if (url.pathname.endsWith("/visits")) {
        const search = url.searchParams.get("search") ?? "",
          page = Number(url.searchParams.get("page")),
          size = Number(url.searchParams.get("pageSize"));
        const filtered = visits.filter((v) => v.name.includes(search)),
          body = {
            scope,
            groupBy: "month",
            bucketStart: "2026-11-01",
            metric: url.searchParams.get("metric"),
            totalVisits: 60,
            items: filtered.slice((page - 1) * size, page * size),
            pagination: {
              page,
              pageSize: size,
              totalItems: filtered.length,
              totalPages: Math.ceil(filtered.length / size),
            },
          };
        if (search === "Only")
          return new Promise<Response>((resolve) => {
            resolveSearch = resolve;
          }).then(() => json(body));
        return json(body);
      }
    });
    const showOnMap = vi.fn();
    render(<Harness pageSize={100} onShowOnMap={showOnMap} />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "New visits (past year) in November 2026: 60" }),
    );
    const dialog = await screen.findByRole("dialog"),
      table = dialog.querySelector(".stats-visits-table")!;
    await within(dialog).findByText("Only Target");
    expect(table.querySelectorAll("tbody tr:not(.stats-page-spacer)")).toHaveLength(25);
    await user.click(within(dialog).getByRole("button", { name: "Last page" }));
    await within(dialog).findByText("Page 3 of 3");
    expect(table.querySelectorAll("tbody tr[hidden]")).toHaveLength(15);
    const firstRow = table.querySelector("tbody tr"),
      firstCell = firstRow?.firstElementChild;
    const oldAction = within(dialog).getByRole("button", { name: "Fixture Road 50" });
    fireEvent.change(within(dialog).getByRole("searchbox"), { target: { value: "Only" } });
    await waitFor(() => expect(resolveSearch).toBeDefined());
    expect(table.querySelector("tbody tr")).toBe(firstRow);
    expect(firstCell).toBeInTheDocument();
    expect(table.querySelectorAll("tbody tr[hidden]")).toHaveLength(15);
    expect(within(dialog).queryByText("Loading visits...")).not.toBeInTheDocument();
    expect(oldAction).toHaveAttribute("aria-disabled", "true");
    expect(oldAction).not.toBeDisabled();
    await user.click(oldAction);
    expect(showOnMap).not.toHaveBeenCalled();
    expect(within(dialog).getByRole("navigation")).toHaveTextContent("Page 3 of 3");
    expect(within(dialog).getByRole("button", { name: "Previous" })).toBeEnabled();
    await act(async () => resolveSearch!(json({})));
    await within(dialog).findByText("Only Target");
    expect(table.querySelector("tbody tr")).toBe(firstRow);
    expect(firstRow?.firstElementChild).toBe(firstCell);
    expect(table.querySelectorAll("tbody tr[hidden]")).toHaveLength(24);
    expect(oldAction).not.toBeInTheDocument();
    expect(dialog.querySelector(".pagination-position")).toHaveAttribute(
      "data-width-sample",
      "Page 3 of 3",
    );
    fireEvent.change(within(dialog).getByRole("searchbox"), { target: { value: "absent" } });
    await within(dialog).findByText("No visits match this search.");
    expect(table.querySelectorAll("tbody tr")).toHaveLength(0);
    expect(table.querySelectorAll("thead tr")).toHaveLength(1);
    expect(within(dialog).getByText("No visits match this search.").closest("table")).toBeNull();
    fireEvent.change(within(dialog).getByRole("searchbox"), { target: { value: "" } });
    await within(dialog).findByText("Only Target");
    expect(table.querySelector(".stats-page-spacer")).toBeNull();
    expect(table.querySelectorAll("tbody tr[hidden]")).toHaveLength(0);
    expect(
      fetch.mock.calls
        .filter(([url]) => String(url).includes("/visits?"))
        .every(
          ([url]) => new URL(String(url), "https://test").searchParams.get("pageSize") === "25",
        ),
    ).toBe(true);
  });

  test("uses report navigation, Month default, chronological buckets and whole nonzero cell actions", async () => {
    const { fetch } = mockReports();
    render(<Harness />);
    expect(screen.getByRole("navigation", { name: "Reports" })).toContainElement(
      screen.getByRole("button", { name: "New Coverage" }),
    );
    expect(screen.getByRole("button", { name: "New Coverage" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      within(screen.getByRole("group", { name: "Show new visits by" })).getByRole("button", {
        name: "Month",
      }),
    ).toHaveAttribute("aria-pressed", "true");
    const action = await screen.findByRole("button", {
      name: "New visits (all time) in November 2026: 1",
    });
    expect(action.parentElement).toHaveClass("stats-count-cell");
    expect(screen.getByRole("columnheader", { name: /Month/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    const zeroRow = screen.getByText("October 2026").closest("tr")!;
    expect(within(zeroRow).getAllByText("0")).toHaveLength(2);
    expect(within(zeroRow).queryByRole("button")).not.toBeInTheDocument();
    const requests = fetch.mock.calls.map(([url]) => String(url));
    expect(
      requests.some((url) => /map-selections|tile|geometry|coverage-diagnostic/.test(url)),
    ).toBe(false);
    expect(
      requests.some((url) => url.includes("sort=bucket%3Adesc") && url.includes("pageSize=25")),
    ).toBe(true);
  });

  test("shows qualifying dates and the exact muted n/a presentation, searches, and restores focus on Escape", async () => {
    mockReports();
    render(<Harness />);
    const user = userEvent.setup();
    const action = await screen.findByRole("button", {
      name: "New visits (past year) in November 2026: 2",
    });
    action.focus();
    await user.keyboard("{Enter}");
    const dialog = screen.getByRole("dialog", { name: "New visits (past year) in November 2026" });
    await within(dialog).findByText("First Park");
    expect(within(dialog).getByText("Nov 3, 2026")).toBeInTheDocument();
    expect(within(dialog).getByText("Nov 9, 2024")).toBeInTheDocument();
    expect(within(dialog).getByText("n/a")).toHaveClass("workout-table-na");
    expect(within(dialog).getByRole("columnheader", { name: /New visit/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(dialog).toHaveClass("stats-visits-dialog--returns");
    await user.type(within(dialog).getByRole("searchbox"), "return");
    await waitFor(() => {
      expect(within(dialog).queryByText("First Park")).not.toBeInTheDocument();
      expect(within(dialog).getByText("Return Road")).toBeInTheDocument();
    });
    expect(within(dialog).queryByText(/\d+ of \d+ visits/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText("First Park")).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(action).toHaveFocus());
  });

  test("all-time details contain only three columns and Year uses the matching dialog title", async () => {
    mockReports();
    render(<Harness />);
    const user = userEvent.setup();
    await screen.findByText("November 2026");
    await user.click(
      within(screen.getByRole("group", { name: "Show new visits by" })).getByRole("button", {
        name: "Year",
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "New visits (all time) in 2026: 1" }),
    );
    const dialog = screen.getByRole("dialog", { name: "New visits (all time) in 2026" });
    await within(dialog).findByText("First Park");
    expect(within(dialog).getAllByRole("columnheader")).toHaveLength(3);
    expect(within(dialog).queryByText("Previous visit")).not.toBeInTheDocument();
    expect(dialog).not.toHaveClass("stats-visits-dialog--returns");
    expect(within(dialog).getByText("Nov 3, 2026")).toBeInTheDocument();
  });

  test("keeps parent-owned report controls and paging across tab unmounts without changing workout sort", async () => {
    const { fetch } = mockReports();
    render(<Harness />);
    const user = userEvent.setup();
    await screen.findByText("November 2026");
    await user.click(
      within(screen.getByRole("columnheader", { name: /New visits \(all time\)/ })).getByRole(
        "button",
      ),
    );
    await user.click(
      within(screen.getByRole("navigation", { name: "New Coverage pages" })).getByRole("button", {
        name: "Next",
      }),
    );
    await screen.findByText("Page 2 of 2");
    await user.click(screen.getByRole("button", { name: "Switch to Map" }));
    await user.click(screen.getByRole("button", { name: "Return to Stats" }));
    await screen.findByText("Page 2 of 2");
    expect(screen.getByRole("columnheader", { name: /New visits \(all time\)/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await user.click(
      within(screen.getByRole("group", { name: "Show new visits by" })).getByRole("button", {
        name: "Year",
      }),
    );
    await screen.findByText("2026", { selector: "td" });
    await user.click(screen.getByRole("button", { name: "Switch to Map" }));
    await user.click(screen.getByRole("button", { name: "Return to Stats" }));
    expect(
      within(screen.getByRole("group", { name: "Show new visits by" })).getByRole("button", {
        name: "Year",
      }),
    ).toHaveAttribute("aria-pressed", "true");
    await screen.findByText("2026", { selector: "td" });
    expect(
      fetch.mock.calls.some(
        ([url]) => String(url).includes("page=2") && String(url).includes("sort=allTime%3Adesc"),
      ),
    ).toBe(true);
  });

  test("reuses the date picker, recent ranges and saved inclusive selection", async () => {
    const { fetch } = mockReports();
    render(<Harness />);
    const user = userEvent.setup();
    await screen.findByText("November 2026");
    await user.click(screen.getByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: "Jan 1, 2025 to Dec 31, 2025" }));
    await screen.findByText("December 2025");
    expect(screen.queryByText("November 2026")).not.toBeInTheDocument();
    expect(
      fetch.mock.calls.some(
        ([url, init]) =>
          url === "/api/me/preferences" &&
          JSON.parse(String(init?.body)).dateRange === "2025-01-01/2025-12-31",
      ),
    ).toBe(true);
    const create = fetch.mock.calls.filter(([url]) => url === "/api/stats/report-scopes").at(-1)!;
    expect(JSON.parse(String(create[1]?.body))).toEqual({
      startDate: "2025-01-01",
      endDate: "2025-12-31",
    });
  });

  test("late summary responses cannot restore an obsolete date range", async () => {
    let resolveOld!: (value: Response) => void;
    let delayed = false;
    const { scopes } = mockReports((url) => {
      if (url.pathname.endsWith("/new-coverage") && !delayed) {
        delayed = true;
        return new Promise<Response>((resolve) => {
          resolveOld = resolve;
        });
      }
    });
    render(<Harness />);
    await waitFor(() => expect(resolveOld).toBeDefined());
    fireEvent.click(screen.getByRole("button", { name: "External range change" }));
    await screen.findByText("December 2025");
    await act(async () =>
      resolveOld(
        json({
          scope: scopes.values().next().value,
          groupBy: "month",
          items: [{ bucketStart: "2026-11-01", allTime: 999, pastYear: 999 }],
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        }),
      ),
    );
    expect(screen.queryByText("November 2026")).not.toBeInTheDocument();
    expect(screen.queryByText("999")).not.toBeInTheDocument();
  });

  test("late detail responses cannot reopen a dismissed dialog", async () => {
    let resolveDetails!: (value: Response) => void;
    const { scopes } = mockReports((url) => {
      if (url.pathname.endsWith("/visits"))
        return new Promise<Response>((resolve) => {
          resolveDetails = resolve;
        });
    });
    render(<Harness />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" }),
    );
    await waitFor(() => expect(resolveDetails).toBeDefined());
    await user.keyboard("{Escape}");
    await act(async () =>
      resolveDetails(
        json({
          scope: scopes.values().next().value,
          totalVisits: 2,
          items: [
            {
              entityId: "F".repeat(32),
              name: "Obsolete detail",
              place: "Fixture",
              newVisit: "2026-11-01",
              previousVisit: null,
            },
          ],
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        }),
      ),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByText("Obsolete detail")).not.toBeInTheDocument();
  });

  test("stale scopes renew with bounded retries and no automatic dialog reopen", async () => {
    let stale = true;
    const { fetch } = mockReports((url) => {
      if (url.pathname.endsWith("/new-coverage") && stale) {
        stale = false;
        return json({ title: "Not Found" }, 404);
      }
    });
    render(<Harness />);
    await screen.findByText("November 2026");
    expect(fetch.mock.calls.filter(([url]) => url === "/api/stats/report-scopes")).toHaveLength(2);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("preparation failures show a retryable error rather than zero counts", async () => {
    let fail = true;
    mockReports((url) => {
      if (url.pathname === "/api/stats/report-scopes" && fail)
        return json({ title: "Unavailable" }, 503);
    });
    render(<Harness />);
    await screen.findByRole("alert");
    expect(screen.queryByText("0")).not.toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("November 2026");
  });

  test("keeps brief refresh failures quiet and persistent feedback outside the table flow", async () => {
    let fail = false;
    mockReports((url) => {
      if (fail && url.pathname.endsWith("/new-coverage")) return json({}, 503);
    });
    render(<Harness />);
    const row = (await screen.findByText("November 2026")).closest("tr");
    const table = document.querySelector(".stats-table")!;
    vi.useFakeTimers();
    try {
      fail = true;
      await act(async () => fireEvent.focus(window));
      await act(async () => vi.advanceTimersByTimeAsync(200));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      fail = false;
      await act(async () => fireEvent.focus(window));
      await act(async () => vi.advanceTimersByTimeAsync(500));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      fail = true;
      await act(async () => fireEvent.focus(window));
      await act(async () => vi.advanceTimersByTimeAsync(499));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      await act(async () => vi.advanceTimersByTimeAsync(1));
      const feedback = screen.getByRole("alert");
      expect(feedback).toHaveClass("map-banner", "stats-report-feedback");
      expect(feedback.closest(".stats-table-frame")).toBeNull();
      expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
      expect(document.querySelector(".stats-table")).toBe(table);
      expect(screen.getByText("November 2026").closest("tr")).toBe(row);
      fail = false;
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "Retry" })));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(screen.getByText("November 2026").closest("tr")).toBe(row);
    } finally {
      vi.useRealTimers();
    }
  });

  test("rejects response scope mismatches", async () => {
    mockReports((url) => {
      if (url.pathname.endsWith("/new-coverage"))
        return json({
          scope: { id: "foreign" },
          items: [],
          pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
        });
    });
    render(<Harness />);
    await screen.findByRole("alert");
    expect(screen.queryByText("November 2026")).not.toBeInTheDocument();
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  test("bucket labels preserve calendar month and four-digit year", () => {
    expect(statsBucketLabel("2026-11-01", "month")).toBe("November 2026");
    expect(statsBucketLabel("2024-01-01", "year")).toBe("2024");
    expect(statsBucketLabel("0001-03-01", "month")).toBe("March 0001");
  });

  test("replaces the explanatory copy with the exact styled two-line header help", async () => {
    mockReports();
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
      () => new DOMRect(100, 100, 200, 40),
    );
    render(<Harness />);
    await screen.findByText("November 2026");
    expect(
      screen.queryByText("First visits and returns to named roads, paths and parks."),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(
        "Past-year visits include first visits and returns after more than one calendar year.",
      ),
    ).not.toBeInTheDocument();
    const header = screen.getByRole("columnheader", { name: "New visits (past year)" });
    fireEvent.pointerEnter(header.querySelector(".tooltip-trigger")!, {
      clientX: 150,
      clientY: 110,
    });
    expect(document.querySelector(".tooltip-content--portal.is-open")?.textContent).toBe(
      "Includes first visits and return\nvisits after more than one year",
    );
    expect(header.querySelector("[title]")).toBeNull();
  });

  test("keeps old report rows and page controls mounted until the next page arrives, then reserves the short final page", async () => {
    let resolvePage!: (response: Response) => void;
    const { scopes } = mockReports((url) => {
      if (url.pathname.endsWith("/new-coverage") && url.searchParams.get("page") === "2")
        return new Promise<Response>((resolve) => {
          resolvePage = resolve;
        });
    });
    render(<Harness />);
    const old = await screen.findByText("November 2026");
    const pages = screen.getByRole("navigation", { name: "New Coverage pages" });
    fireEvent.click(within(pages).getByRole("button", { name: "Next" }));
    await waitFor(() => expect(resolvePage).toBeDefined());
    expect(old).toBeInTheDocument();
    expect(within(pages).getByRole("button", { name: "Next" })).toBeEnabled();
    expect(document.querySelector(".stats-table .stats-state-row")).toBeNull();
    await act(async () =>
      resolvePage(
        json({
          scope: [...scopes.values()][0],
          groupBy: "month",
          items: Array.from({ length: 8 }, (_, index) => ({
            bucketStart: `2024-${String(10 - index).padStart(2, "0")}-01`,
            allTime: 0,
            pastYear: 0,
          })),
          pagination: { page: 2, pageSize: 25, totalItems: 33, totalPages: 2 },
        }),
      ),
    );
    await screen.findByText("March 2024");
    expect(screen.getByRole("navigation", { name: "New Coverage pages" })).toBe(pages);
    expect(document.querySelector(".stats-table .stats-page-spacer td")).toHaveStyle({
      height: "51rem",
    });
    expect(screen.getByText("Page 2 of 2")).toBeInTheDocument();
  });

  test.each(["year", "month"] as const)(
    "replaces grouping with %s in place only after its data arrives",
    async (group) => {
      let delay = false;
      let resolveGroup!: (response: Response) => void;
      const { scopes, fetch } = mockReports((url) => {
        if (
          delay &&
          url.pathname.endsWith("/new-coverage") &&
          url.searchParams.get("groupBy") === group
        )
          return new Promise<Response>((resolve) => {
            resolveGroup = resolve;
          });
      });
      render(<Harness />);
      const user = userEvent.setup();
      await screen.findByText("November 2026");
      const controls = screen.getByRole("group", { name: "Show new visits by" });
      if (group === "month") {
        await user.click(within(controls).getByRole("button", { name: "Year" }));
        await screen.findByText("2026", { selector: "td" });
      }
      const table = document.querySelector(".stats-table")!;
      const firstRow = table.querySelector("tbody tr")!;
      const previousLabel = group === "year" ? "November 2026" : "2026";
      delay = true;
      await user.click(
        within(controls).getByRole("button", { name: group === "year" ? "Year" : "Month" }),
      );
      await waitFor(() => expect(resolveGroup).toBeDefined());
      expect(firstRow).toBeInTheDocument();
      expect(within(firstRow as HTMLElement).getByText(previousLabel)).toBeInTheDocument();
      expect(table.querySelector(".stats-state-row")).toBeNull();
      expect(
        screen.getByRole("columnheader", { name: group === "year" ? "Month" : "Year" }),
      ).toBeInTheDocument();
      const items =
        group === "year"
          ? [2026, 2025, 2024].map((year) => ({
              bucketStart: `${year}-01-01`,
              allTime: 0,
              pastYear: 0,
            }))
          : Array.from({ length: 25 }, (_, index) => {
              const date = new Date(Date.UTC(2026, 10 - index, 1));
              return { bucketStart: date.toISOString().slice(0, 10), allTime: 0, pastYear: 0 };
            });
      await act(async () =>
        resolveGroup(
          json({
            scope: [...scopes.values()][0],
            groupBy: group,
            items,
            pagination: {
              page: 1,
              pageSize: 25,
              totalItems: group === "year" ? 3 : 33,
              totalPages: group === "year" ? 1 : 2,
            },
          }),
        ),
      );
      await screen.findByText(group === "year" ? "2026" : "November 2026", { selector: "td" });
      expect(table.querySelector("tbody tr")).toBe(firstRow);
      expect(
        table.querySelectorAll("tbody tr:not(.stats-page-spacer):not(.stats-state-row)"),
      ).toHaveLength(group === "year" ? 3 : 25);
      expect(table.querySelector(".stats-page-spacer")).toBeNull();
      expect(
        screen.getByRole("columnheader", { name: group === "year" ? "Year" : "Month" }),
      ).toBeInTheDocument();
      expect(fetch.mock.calls.filter(([url]) => url === "/api/stats/report-scopes")).toHaveLength(
        1,
      );
    },
  );

  test("ignores a late Year response after the user switches back to Month", async () => {
    let resolveYear!: (response: Response) => void;
    const { scopes } = mockReports((url) => {
      if (url.pathname.endsWith("/new-coverage") && url.searchParams.get("groupBy") === "year")
        return new Promise<Response>((resolve) => {
          resolveYear = resolve;
        });
    });
    render(<Harness />);
    const user = userEvent.setup();
    await screen.findByText("November 2026");
    const controls = screen.getByRole("group", { name: "Show new visits by" });
    await user.click(within(controls).getByRole("button", { name: "Year" }));
    await waitFor(() => expect(resolveYear).toBeDefined());
    await user.click(within(controls).getByRole("button", { name: "Month" }));
    await waitFor(() =>
      expect(document.querySelector(".stats-table-wrap")).toHaveAttribute("aria-busy", "false"),
    );
    await act(async () =>
      resolveYear(
        json({
          scope: [...scopes.values()][0],
          groupBy: "year",
          items: [{ bucketStart: "2026-01-01", allTime: 999, pastYear: 999 }],
          pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
        }),
      ),
    );
    expect(screen.getByText("November 2026")).toBeInTheDocument();
    expect(screen.queryByText("999")).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Month" })).toBeInTheDocument();
  });

  test("keeps visit rows visible during dialog page navigation", async () => {
    let resolvePage!: (response: Response) => void;
    const { scopes } = mockReports((url) => {
      if (url.pathname.endsWith("/visits") && url.searchParams.get("page") === "2")
        return new Promise<Response>((resolve) => {
          resolvePage = resolve;
        });
    });
    render(<Harness pageSize={1} />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" }),
    );
    const dialog = screen.getByRole("dialog");
    const old = await within(dialog).findByText("Return Road");
    const pages = within(dialog).getByRole("navigation", { name: "New visit pages" });
    await user.click(within(pages).getByRole("button", { name: "Next" }));
    expect(old).toBeInTheDocument();
    expect(within(dialog).queryByText("Loading visits...")).not.toBeInTheDocument();
    expect(within(pages).getByRole("button", { name: "Next" })).toBeEnabled();
    await act(async () =>
      resolvePage(
        json({
          scope: [...scopes.values()][0],
          totalVisits: 2,
          items: [
            {
              entityId: "B".repeat(32),
              entityKind: "park",
              name: "First Park",
              place: "Fixture County",
              newVisit: "2026-11-03",
              previousVisit: null,
              newVisitWorkoutId: "E".repeat(32),
              previousVisitWorkoutId: null,
            },
          ],
          pagination: { page: 2, pageSize: 1, totalItems: 2, totalPages: 2 },
        }),
      ),
    );
    await within(dialog).findByText("First Park");
    expect(within(dialog).getByRole("navigation", { name: "New visit pages" })).toBe(pages);
    expect(within(dialog).queryByText(/of \d+ visits/)).not.toBeInTheDocument();
  });

  test.each(["name", "new", "previous"] as const)(
    "navigates an in-range %s to the matching Map target",
    async (action) => {
      mockReports();
      const onShowOnMap = vi.fn();
      render(<Harness onShowOnMap={onShowOnMap} />);
      const user = userEvent.setup();
      await user.click(
        await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" }),
      );
      const dialog = screen.getByRole("dialog");
      await within(dialog).findByText("Return Road");
      await user.click(
        within(dialog).getByRole("button", {
          name:
            action === "name" ? "Return Road" : action === "new" ? "Nov 10, 2026" : "Nov 9, 2024",
        }),
      );
      expect(onShowOnMap).toHaveBeenCalledWith(
        action === "name"
          ? { kind: "entity", entityId: "A".repeat(32), entityKind: "path", name: "Return Road" }
          : { kind: "workout", workoutId: (action === "new" ? "C" : "D").repeat(32) },
      );
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    },
  );

  test("keeps an out-of-range previous date plain despite having its workout ID", async () => {
    mockReports();
    const onShowOnMap = vi.fn();
    render(<Harness startingRange="2026-11-01/2026-11-30" onShowOnMap={onShowOnMap} />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "New visits (past year) in November 2026: 2" }),
    );
    const dialog = screen.getByRole("dialog");
    const date = await within(dialog).findByText("Nov 9, 2024");
    expect(date.closest("button")).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "Nov 9, 2024" })).not.toBeInTheDocument();
    await user.click(date);
    expect(onShowOnMap).not.toHaveBeenCalled();
    expect(dialog).toBeInTheDocument();
  });

  test("recovers idle scope expiry and failed renewal without a tab switch or erased rows", async () => {
    vi.useFakeTimers();
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    let preparations = 0,
      unavailable = false;
    mockReports((url, init) => {
      if (url.pathname === "/api/stats/report-scopes" && init?.method === "POST") {
        preparations++;
        if (unavailable) return json({ title: "Service Unavailable" }, 503);
      }
    });
    render(<Harness />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const old = screen.getByText("November 2026");
    visibility.mockReturnValue("hidden");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31 * 60000);
    });
    unavailable = true;
    visibility.mockReturnValue("visible");
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    for (let step = 0; step < 4; step++)
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1100);
      });
    expect(old).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("could not be refreshed");
    expect(preparations).toBe(4);
    unavailable = false;
    await act(async () => {
      window.dispatchEvent(new Event("online"));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "New visits (all time) in November 2026: 1" }),
    ).toBeEnabled();
    expect(preparations).toBe(5);
  });

  test("stops automatic renewal after repeated stale snapshots", async () => {
    const { fetch } = mockReports((url) =>
      url.pathname.endsWith("/new-coverage") ? json({ title: "Not Found" }, 404) : undefined,
    );
    render(<Harness />);
    await waitFor(
      () => expect(screen.getByRole("alert")).toHaveTextContent("Coverage is changing"),
      { timeout: 3000 },
    );
    expect(fetch.mock.calls.filter(([url]) => url === "/api/stats/report-scopes")).toHaveLength(4);
  });

  test("validates active reports in the background and pauses in hidden tabs", async () => {
    vi.useFakeTimers();
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const { fetch } = mockReports();
    render(<Harness />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const reads = () =>
      fetch.mock.calls.filter(([url]) => String(url).includes("/new-coverage?")).length;
    expect(reads()).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30000);
    });
    expect(reads()).toBe(2);
    visibility.mockReturnValue("hidden");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(reads()).toBe(2);
    visibility.mockReturnValue("visible");
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(reads()).toBe(3);
  });
});
