import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import type { ConsistencyReport, ConsistencyGrouping, Preferences } from "./api";
import { DEFAULT_STATS_STATE, StatsPage, type StatsViewState } from "./StatsPage";
import { RetainedTab } from "./RetainedTab";

const preferences: Preferences = {
  theme: "dark",
  units: "imperial",
  timezone: "America/Denver",
  firstWeekday: "monday",
  clockFormat: "12h",
  workoutColumns: ["date", "duration"],
  pageSize: 25,
  customDateRangeLimit: 3,
  recentCustomDateRanges: [],
  coverageDiagnosticsEnabled: false,
  initialized: true,
};
function report(group: ConsistencyGrouping = "week"): ConsistencyReport {
  return {
    range: { startDate: "2024-01-01", endDate: "2026-12-31", timezone: preferences.timezone },
    groupBy: group,
    firstWeekday: "monday",
    undatedWorkoutCount: 0,
    buckets: Array.from({ length: group === "week" ? 156 : 36 }, (_, index) => {
      const date = new Date("2024-01-01T00:00:00Z");
      if (group === "week") date.setUTCDate(1 + index * 7);
      else date.setUTCMonth(index);
      return {
        bucketStart: date.toISOString().slice(0, 10),
        workoutCount: 3,
        activeDays: group === "week" ? index % 8 : (index % 29) + 1,
        duration: index === 2 ? "10000000" : "3600",
        distance: { value: String(10 + (index % 5)), unit: "km" },
        calories: { value: "500", unit: "kcal" },
        longestDuration: "1800",
        longestDistance: { value: "5", unit: "km" },
        highestCalories: { value: "250", unit: "kcal" },
      };
    }),
  };
}
function setup(intercept?: (url: URL) => Promise<Response> | Response | undefined) {
  const requests: URL[] = [];
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = new URL(String(input), "https://test");
    requests.push(url);
    const custom = intercept?.(url);
    if (custom) return Promise.resolve(custom);
    if (url.pathname === "/api/stats/consistency")
      return Promise.resolve(
        Response.json(report((url.searchParams.get("groupBy") ?? "week") as ConsistencyGrouping)),
      );
    if (url.pathname === "/api/stats/report-scopes")
      return Promise.resolve(
        Response.json(
          {
            id: "A".repeat(32),
            dataGeneration: 1,
            coverageDisplayRevision: 1,
            coverageDisplayPolicy: "test",
            expiresAt: new Date(Date.now() + 3600000).toISOString(),
            range: report().range,
          },
          { status: 201 },
        ),
      );
    if (url.pathname.endsWith("/new-coverage"))
      return Promise.resolve(
        Response.json({
          scope: {
            id: "A".repeat(32),
            dataGeneration: 1,
            coverageDisplayRevision: 1,
            coverageDisplayPolicy: "test",
            expiresAt: new Date(Date.now() + 3600000).toISOString(),
            range: report().range,
          },
          groupBy: "month",
          items: [],
          pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
        }),
      );
    if (url.pathname.includes("/report-scopes/"))
      return Promise.resolve(new Response(null, { status: 204 }));
    throw new Error("Unexpected " + url.pathname);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity, refetchOnWindowFocus: false } },
  });
  function Harness() {
    const [state, setState] = useState<StatsViewState>(DEFAULT_STATS_STATE);
    const [active, setActive] = useState(true);
    const [range, setRange] = useState<"allTime" | "last7Days">("allTime");
    return (
      <QueryClientProvider client={client}>
        <button onClick={() => setActive(!active)}>
          {active ? "Leave Stats" : "Return to Stats"}
        </button>
        <button onClick={() => setRange("last7Days")}>Change range</button>
        <RetainedTab id="/stats" active={active}>
          <StatsPage
            accountKey="fixture-session"
            preferences={preferences}
            csrfToken="test"
            dateRange={range}
            onDateRangeSelected={() => undefined}
            onRecentDateRangesSaved={() => undefined}
            state={state}
            onStateChange={setState}
            pollingIntervalSeconds={30}
          />
        </RetainedTab>
      </QueryClientProvider>
    );
  }
  const view = render(<Harness />);
  return { ...view, requests, fetch, client, user: userEvent.setup() };
}

describe("Consistency report", () => {
  function mockChartLayout() {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains("consistency-chart-scroll") ? 600 : 0;
    });
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains("consistency-chart-scroll")
        ? Number.parseFloat(
            (this.querySelector(".consistency-chart-canvas") as HTMLElement)?.style.width || "600",
          )
        : 0;
    });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
      this: HTMLElement,
    ) {
      return new DOMRect(0, 0, this.classList.contains("consistency-date-measure") ? 64 : 0, 0);
    });
  }
  function pointer(element: Element, type: string, x: number) {
    const event = new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      button: 0,
      buttons: type === "pointerup" ? 0 : 1,
      clientX: x,
      clientY: 100,
    });
    Object.defineProperties(event, { pointerId: { value: 1 }, pointerType: { value: "mouse" } });
    fireEvent(element, event);
  }
  test("starts every chart at the final period and returns there after changing grouping", async () => {
    mockChartLayout();
    const { user } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    await waitFor(() => {
      for (const node of document.querySelectorAll<HTMLElement>(".consistency-chart-scroll"))
        expect(node.scrollLeft).toBe(node.scrollWidth - node.clientWidth);
    });
    const pane = document.querySelector<HTMLElement>(".consistency-chart-scroll")!;
    pane.scrollLeft = 500;
    fireEvent.scroll(pane);
    await user.click(screen.getByRole("button", { name: "Month" }));
    await screen.findByRole("group", { name: "Active days per month" });
    await waitFor(() => {
      for (const node of document.querySelectorAll<HTMLElement>(".consistency-chart-scroll"))
        expect(node.scrollLeft).toBe(node.scrollWidth - node.clientWidth);
    });
  });
  test("locks horizontal positions across charts and awards only perfect-month active-day bars", async () => {
    mockChartLayout();
    const { user } = setup((url) => {
      if (url.pathname !== "/api/stats/consistency") return;
      const result = report((url.searchParams.get("groupBy") ?? "week") as ConsistencyGrouping);
      if (result.groupBy === "month") {
        result.buckets[0].activeDays = 31;
        result.buckets[1].activeDays = 29;
        result.buckets[2].activeDays = 30;
        result.buckets[3].activeDays = 30;
      }
      return Response.json(result);
    });
    await screen.findByRole("group", { name: "Active days per week" });
    const panes = [...document.querySelectorAll<HTMLElement>(".consistency-chart-scroll")];
    panes[2].scrollLeft = 500;
    fireEvent.scroll(panes[2]);
    await waitFor(() => expect(panes.every((pane) => pane.scrollLeft === 500)).toBe(true));
    expect(document.querySelector(".consistency-perfect-star")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Month" }));
    await screen.findByRole("group", { name: "Active days per month" });
    const monthPanes = [...document.querySelectorAll<HTMLElement>(".consistency-chart-scroll")];
    monthPanes[0].scrollLeft = 0;
    fireEvent.scroll(monthPanes[0]);
    await waitFor(() =>
      expect(document.querySelectorAll(".consistency-perfect-star")).toHaveLength(3),
    );
    expect(
      document.querySelectorAll(".consistency-section:not(:first-child) .consistency-perfect-star"),
    ).toHaveLength(0);
  });
  test("drags the timeline, cancels on blur and retains a user's pan across tab returns", async () => {
    mockChartLayout();
    const { user } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    const pane = document.querySelector<HTMLElement>(".consistency-chart-scroll")!;
    await waitFor(() => expect(pane.scrollLeft).toBeGreaterThan(0));
    const end = pane.scrollLeft;
    pointer(pane, "pointerdown", 200);
    pointer(pane, "pointermove", 280);
    expect(pane.scrollLeft).toBe(end - 80);
    expect(pane).toHaveClass("is-dragging");
    pointer(pane, "pointerup", 280);
    expect(pane).not.toHaveClass("is-dragging");
    pointer(pane, "pointerdown", 200);
    pointer(pane, "pointermove", 240);
    fireEvent(window, new Event("blur"));
    expect(pane).not.toHaveClass("is-dragging");
    const saved = pane.scrollLeft;
    await user.click(screen.getByRole("button", { name: "Leave Stats" }));
    await user.click(screen.getByRole("button", { name: "Return to Stats" }));
    expect(pane.scrollLeft).toBe(saved);
  });
  test("is the first/default report and opens seven sections without preparing Coverage", async () => {
    const { requests } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    const links = within(screen.getByRole("navigation", { name: "Reports" })).getAllByRole(
      "button",
    );
    expect(links.map((link) => link.textContent)).toEqual(["Consistency", "New coverage"]);
    expect(links[0]).toHaveAttribute("aria-current", "page");
    expect(screen.getAllByRole("button", { expanded: true })).toHaveLength(7);
    expect(screen.getByRole("button", { name: "Week", pressed: true })).toBeInTheDocument();
    expect(requests.filter((url) => url.pathname === "/api/stats/report-scopes")).toHaveLength(0);
    expect(document.querySelectorAll(".consistency-chart")).toHaveLength(7);
    expect(document.querySelectorAll(".consistency-bar-slot").length).toBeLessThan(100);
    expect(
      [
        ...document
          .querySelectorAll(".consistency-chart")[0]
          .querySelectorAll(".consistency-axis-plot > span"),
      ].map((node) => node.textContent),
    ).toEqual(["2", "4", "6"]);
    expect(
      document.querySelector(".consistency-chart [data-consistency-index='155']"),
    ).toBeInTheDocument();
  });
  test("collapses independently, uses Open all only when all closed, and retains chart nodes", async () => {
    const { user } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    const chart = document.querySelector(".consistency-chart");
    await user.click(screen.getByRole("button", { name: "Active days per week" }));
    expect(chart).not.toBeVisible();
    expect(screen.getByRole("button", { name: "Close all sections" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close all sections" }));
    expect(
      within(document.querySelector(".consistency-report") as HTMLElement).getAllByRole("button", {
        expanded: false,
      }),
    ).toHaveLength(7);
    await user.click(screen.getByRole("button", { name: "Open all sections" }));
    expect(chart).toBeVisible();
    expect(document.querySelector(".consistency-chart")).toBe(chart);
  });
  test("W/M switch grouping, preserve collapsed sections and obey editing/modifier guards", async () => {
    const { user, requests } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    await user.click(screen.getByRole("button", { name: "Distance covered per week" }));
    fireEvent.keyDown(document, { key: "M" });
    await screen.findByRole("group", { name: "Active days per month" });
    expect(screen.getByRole("button", { name: "Month", pressed: true })).toBeInTheDocument();
    const monthButton = screen.getByRole("button", { name: "Month", pressed: true });
    const browserBack = new KeyboardEvent("keydown", {
      key: "ArrowLeft",
      altKey: true,
      bubbles: true,
      cancelable: true,
    });
    fireEvent(monthButton, browserBack);
    expect(browserBack.defaultPrevented).toBe(false);
    expect(monthButton).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Distance covered per month" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(
      [
        ...document
          .querySelectorAll(".consistency-chart")[0]
          .querySelectorAll(".consistency-axis-plot > span"),
      ].map((node) => node.textContent),
    ).toEqual(["7", "14", "21", "28"]);
    const count = requests.length;
    fireEvent.keyDown(document, { key: "w", ctrlKey: true });
    expect(requests).toHaveLength(count);
    const input = document.createElement("input");
    document.body.append(input);
    input.focus();
    fireEvent.keyDown(input, { key: "w" });
    expect(screen.getByRole("button", { name: "Month", pressed: true })).toBeInTheDocument();
    input.remove();
    fireEvent.keyDown(document, { key: "w" });
    await screen.findByRole("group", { name: "Active days per week" });
  });
  test("retains prior grouping at full opacity until a compatible replacement arrives", async () => {
    let release!: (response: Response) => void;
    const { user } = setup((url) =>
      url.searchParams.get("groupBy") === "month"
        ? new Promise<Response>((resolve) => (release = resolve))
        : undefined,
    );
    await screen.findByRole("group", { name: "Active days per week" });
    const chart = document.querySelector(".consistency-chart"),
      bar = document.querySelector(".consistency-bar-slot");
    await user.click(screen.getByRole("button", { name: "Month" }));
    expect(screen.getByRole("group", { name: "Active days per week" })).toBeInTheDocument();
    expect(document.querySelector(".consistency-chart")).toBe(chart);
    expect(document.querySelector(".consistency-bar-slot")).toBe(bar);
    await act(async () => release(Response.json(report("month"))));
    await screen.findByRole("group", { name: "Active days per month" });
  });
  test("clears incompatible range pixels and ignores an aborted old response", async () => {
    let release!: (response: Response) => void;
    const { user } = setup((url) =>
      url.searchParams.get("dateRangeEnum") === "last7Days"
        ? new Promise<Response>((resolve) => (release = resolve))
        : undefined,
    );
    await screen.findByRole("group", { name: "Active days per week" });
    await user.click(screen.getByRole("button", { name: "Change range" }));
    expect(document.querySelectorAll(".consistency-bar-slot")).toHaveLength(0);
    expect(screen.getAllByText("Loading chart...")).toHaveLength(7);
    await act(async () => release(Response.json(report())));
    await screen.findByRole("group", { name: "Active days per week" });
  });
  test("uses one keyboard stop per chart and can explore the complete virtual timeline", async () => {
    setup();
    const chart = await screen.findByRole("group", { name: "Active days per week" });
    const first = within(chart).getAllByRole("img")[0];
    first.focus();
    fireEvent.keyDown(first, { key: "End" });
    await waitFor(() =>
      expect(document.activeElement?.closest("[data-consistency-index]")).toHaveAttribute(
        "data-consistency-index",
        "155",
      ),
    );
    expect(chart.querySelectorAll(".consistency-bar-key[tabindex='0']")).toHaveLength(1);
    expect(chart.querySelectorAll(".consistency-bar-slot").length).toBeLessThan(20);
  });
  test("preserves report selection, grouping and collapse state across retained tab returns", async () => {
    const { user } = setup();
    await screen.findByRole("group", { name: "Active days per week" });
    await user.click(screen.getByRole("button", { name: "Month" }));
    await screen.findByRole("group", { name: "Active days per month" });
    await user.click(screen.getByRole("button", { name: "Active days per month" }));
    await user.click(screen.getByRole("button", { name: "Leave Stats" }));
    await user.click(screen.getByRole("button", { name: "Return to Stats" }));
    expect(screen.getByRole("button", { name: "Month", pressed: true })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Active days per month" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });
});
