import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { type Preferences, type Workout } from "./api";
import { Summary, formatAggregateDuration, formatDuration } from "./Summary";

const preferences: Preferences = {
  theme: "dark",
  units: "imperial",
  timezone: "America/Denver",
  firstWeekday: "monday",
  clockFormat: "12h",
  workoutColumns: ["date", "type", "distance", "duration"],
  pageSize: 25,
  dateRange: "last30Days",
};
const range = { startDate: "2026-07-07", endDate: "2026-08-05", timezone: "America/Denver" };
const totals = { count: 2, duration: "3900.5", distance: { value: "10.5", unit: "km" }, energy: { value: "450.25", unit: "kcal" } };
const running = { id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", key: "running", displayName: "Running" };
const summary = { range, totals, byType: [{ type: running, totals }] };
const workout: Workout = {
  id: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
  sourceId: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
  type: running,
  startedAt: "2026-08-05T13:30:00Z",
  endedAt: "2026-08-05T14:35:00Z",
  duration: "3900.5",
  localStartDate: "2026-08-05",
  displayTimezone: "America/Denver",
  originalStartOffsetMinutes: -360,
  originalEndOffsetMinutes: -360,
  timezone: "America/Denver",
  indoor: false,
  location: "Boulder",
  distance: { value: "10.5", unit: "km" },
  pace: { value: "5.25", unit: "min/km" },
  calories: { value: "450.25", unit: "kcal" },
  heartRate: null,
  elevation: { value: "100", unit: "m" },
  routePointCount: 42,
  routeAvailable: true,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" } });
}

function workoutPage(page = 1, items = [workout], pageSize = 25) {
  return { range, pagination: { page, pageSize, totalItems: 26, totalPages: 2 }, items };
}

function renderSummary(overrides: Partial<Preferences> = {}, onDateRangeSaved = vi.fn(), onShowOnMap?: (workoutId: string) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  let current = { ...preferences, ...overrides };
  const view = (next: Preferences) => <QueryClientProvider client={client}><Summary preferences={next} csrfToken="csrf-summary" onShowOnMap={onShowOnMap} onDateRangeSaved={onDateRangeSaved} /></QueryClientProvider>;
  const result = render(view(current));
  return {
    ...result,
    onDateRangeSaved,
    rerenderPreferences(next: Partial<Preferences>) { current = { ...current, ...next }; result.rerender(view(current)); },
  };
}

describe("Summary", () => {
  test("starts with the date picker, omits the former hero, and uses one summary inset token", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    const { container } = renderSummary();
    const main = container.querySelector("main.summary-page")!;
    expect(main.querySelector("h1, .eyebrow")).toBeNull();
    expect(screen.queryByText("Movement, in measure.")).not.toBeInTheDocument();
    expect(main.querySelector("header.summary-heading")?.querySelector(":scope > *")).toBe(screen.getByRole("button", { name: "Select date range" }));
    await screen.findByText("Range totals");
    expect(screen.queryByRole("button", { name: "Delete workouts in this range" })).not.toBeInTheDocument();
    expect(main).toHaveStyle("--summary-inset: var(--space-5)");
    expect(main.querySelector("header.summary-heading")?.nextElementSibling).toHaveClass("aggregate-section");
    expect(main.querySelector(".aggregate-section")?.nextElementSibling).toHaveClass("workouts-section");
  });

  test("formats resolved dates without commas and shows the current PDT range-zone badge", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-15T12:00:00Z"));
    const pacificSummary = { ...summary, range: { startDate: "2026-01-01", endDate: "2026-12-31", timezone: "America/Los_Angeles" } };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(pacificSummary)) : Promise.resolve(json(workoutPage())));
    renderSummary();
    await vi.runAllTimersAsync();
    expect(screen.getByText("Jan 1 2026 through Dec 31 2026")).toBeInTheDocument();
    const metadata = screen.getByText("Jan 1 2026 through Dec 31 2026").closest(".range-metadata")!;
    const badge = within(metadata as HTMLElement).getByLabelText("America/Los_Angeles (UTC-7:00)");
    expect(badge).toHaveTextContent("PDT");
    expect(document.getElementById(badge.getAttribute("aria-describedby")!)).toHaveTextContent("America/Los_Angeles (UTC-7:00)");
    vi.useRealTimers();
  });

  test("describes workout totals as sessions with sensible singular wording", async () => {
    let singular = false;
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      if (String(input).startsWith("/api/summary?")) return Promise.resolve(json(summary));
      return Promise.resolve(json(singular
        ? { ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 } }
        : workoutPage()));
    });
    const view = renderSummary();
    expect(await screen.findByText("26 sessions / Page 1 of 2")).toBeInTheDocument();
    singular = true;
    view.rerenderPreferences({ dateRange: "thisMonth" });
    expect(await screen.findByText("1 session / Page 1 of 1")).toBeInTheDocument();
  });

  test("rounds duration displays to the nearest whole minute", () => {
    expect(formatDuration("29.9")).toBe("0m");
    expect(formatDuration("30")).toBe("1m");
    expect(formatDuration("65.25")).toBe("1m");
    expect(formatDuration("3599.9")).toBe("1h 00m");
    expect(formatDuration("3900.5")).toBe("1h 05m");
  });

  test("rolls aggregate duration into unbounded days", () => {
    expect(formatAggregateDuration("86399")).toBe("1d 0h 00m");
    expect(formatAggregateDuration("405780")).toBe("4d 16h 43m");
    expect(formatAggregateDuration(String((123 * 86400) + (7 * 3600) + (5 * 60)))).toBe("123d 7h 05m");
  });

  test("rounds aggregate distance at 100 displayed units without changing workout rows", async () => {
    const longTotals = { ...totals, distance: { value: "442.232", unit: "km" } };
    const longSummary = { ...summary, totals: longTotals, byType: [{ type: running, totals: longTotals }] };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(longSummary)) : Promise.resolve(json(workoutPage())));
    renderSummary();
    const distanceCard = await screen.findByRole("button", { name: /Distance.*By workout type/ });
    expect(distanceCard).toHaveTextContent("275 mi");
    await userEvent.click(distanceCard);
    expect(document.getElementById(distanceCard.getAttribute("aria-controls")!)).toHaveTextContent("275 mi");
    expect(await within(screen.getByRole("table")).findByText("6.52 mi")).toBeInTheDocument();
  });

  test("queries both resources with the stored shortcut, timezone, paging, sort, and AbortSignals", async () => {
    const requests: Array<{ path: string; signal?: AbortSignal | null }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      requests.push({ path, signal: init?.signal });
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage()));
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary({ dateRange: "thisMonth" });
    expect(await screen.findByText("Range totals")).toBeInTheDocument();
    await screen.findAllByText("Running");
    const summaryRequest = requests.find(({ path }) => path.startsWith("/api/summary?"))!;
    const workoutRequest = requests.find(({ path }) => path.startsWith("/api/workouts?"))!;
    const summaryParams = new URL(summaryRequest.path, "https://test").searchParams;
    const workoutParams = new URL(workoutRequest.path, "https://test").searchParams;
    expect(Object.fromEntries(summaryParams)).toEqual({ dateRangeEnum: "thisMonth", tz: "America/Denver" });
    expect(Object.fromEntries(workoutParams)).toEqual({ dateRangeEnum: "thisMonth", tz: "America/Denver", page: "1", pageSize: "25", sort: "date:desc" });
    expect(summaryRequest.signal).toBeInstanceOf(AbortSignal);
    expect(workoutRequest.signal).toBeInstanceOf(AbortSignal);
  });

  test("renders weekday, exact local date, IANA abbreviation, tooltip, and 12/24-hour time", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    const view = renderSummary();
    let table = await screen.findByRole("table");
    let date = within(within(table).getAllByRole("row")[1]).getByText("Wed", { selector: ".weekday-badge" }).closest<HTMLElement>(".workout-date")!;
    expect(date).toHaveTextContent("WedAug 5, 2026, 7:30 AMMDT");
    const badge = within(date).getByLabelText("America/Denver (UTC-6:00)");
    expect(badge).toHaveTextContent("MDT");
    expect(badge).not.toHaveAttribute("title");
    expect(badge).toHaveAttribute("tabindex", "0");
    expect(document.getElementById(badge.getAttribute("aria-describedby")!)).toHaveAttribute("role", "tooltip");
    expect(date.querySelector(".tooltip-trigger")?.textContent).toBe("MDT");

    view.rerenderPreferences({ clockFormat: "24h" });
    table = await screen.findByRole("table");
    date = within(within(table).getAllByRole("row")[1]).getByText("Wed", { selector: ".weekday-badge" }).closest<HTMLElement>(".workout-date")!;
    expect(date).toHaveTextContent("Aug 5, 2026, 07:30");
    expect(date).not.toHaveTextContent("AM");
  });

  test("never infers a preference zone and rejects a workout IANA zone whose offset mismatches", async () => {
    const offsetOnly = { ...workout, id: "11111111111111111111111111111111", timezone: null };
    const invalidZone = { ...workout, id: "22222222222222222222222222222222", timezone: "Also/Invalid", originalStartOffsetMinutes: -420 };
    const mismatchedZone = { ...workout, id: "66666666666666666666666666666666", timezone: "America/Los_Angeles", originalStartOffsetMinutes: -360 };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json(summary))
      : Promise.resolve(json(workoutPage(1, [offsetOnly, invalidZone, mismatchedZone]))));
    renderSummary();
    const rows = within(await screen.findByRole("table")).getAllByRole("row").slice(1);

    const offsetOnlyBadge = rows[0].querySelector<HTMLElement>(".timezone-badge .tooltip-trigger")!;
    expect(offsetOnlyBadge).toHaveTextContent("UTC-6");
    expect(offsetOnlyBadge).not.toHaveAttribute("title");
    expect(rows[0]).not.toHaveTextContent("MDT");

    const offsetBadge = rows[1].querySelector<HTMLElement>(".timezone-badge .tooltip-trigger")!;
    expect(offsetBadge).toHaveTextContent("UTC-7");
    expect(offsetBadge).not.toHaveAttribute("title");
    expect(rows[1]).not.toHaveTextContent("America/Denver");
    expect(rows[1].querySelector(".tooltip-trigger")?.textContent).toBe("UTC-7");

    const mismatchBadge = rows[2].querySelector<HTMLElement>(".timezone-badge .tooltip-trigger")!;
    expect(mismatchBadge).toHaveTextContent("UTC-6");
    expect(mismatchBadge).not.toHaveAttribute("title");
    expect(rows[2]).toHaveTextContent("7:30 AM");
    expect(rows[2]).not.toHaveTextContent("PDT");
  });

  test("derives DST and half-hour zone badges at each workout instant", async () => {
    const winter = { ...workout, id: "33333333333333333333333333333333", startedAt: "2026-01-15T20:00:00Z", timezone: "America/Los_Angeles", originalStartOffsetMinutes: -480 };
    const summer = { ...workout, id: "44444444444444444444444444444444", timezone: "America/Los_Angeles", originalStartOffsetMinutes: -420 };
    const halfHour = { ...workout, id: "55555555555555555555555555555555", timezone: "Asia/Kolkata", originalStartOffsetMinutes: 330 };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json(summary))
      : Promise.resolve(json(workoutPage(1, [winter, summer, halfHour]))));
    renderSummary();
    const badges = Array.from((await screen.findByRole("table")).querySelectorAll<HTMLElement>("tbody .timezone-badge .tooltip-trigger"));
    expect(badges.map((badge) => badge.textContent)).toEqual(["PST", "PDT", "GMT+5:30"]);
    expect(badges.map((badge) => document.getElementById(badge.getAttribute("aria-describedby")!)?.textContent)).toEqual([
      "America/Los_Angeles (UTC-8:00)",
      "America/Los_Angeles (UTC-7:00)",
      "Asia/Kolkata (UTC+5:30)",
    ]);
  });

  test("shows rounded duration tooltips in totals, breakdowns, desktop, and mobile rows", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    renderSummary();
    const total = await screen.findByRole("button", { name: /Duration.*By workout type/ });
    expect(total).toHaveTextContent("1h 05m");
    expect(total).not.toHaveAttribute("title");
    const totalTooltip = document.getElementById(total.getAttribute("aria-describedby")!)!;
    expect(totalTooltip).toHaveAttribute("role", "tooltip");
    expect(totalTooltip).toHaveTextContent("1h 05m 01s");
    fireEvent.click(total);
    const breakdown = document.getElementById(total.getAttribute("aria-controls")!)!;
    const byTypeDuration = breakdown.querySelector<HTMLElement>(".duration-value")!;
    const byTypeTrigger = byTypeDuration.querySelector<HTMLElement>(".tooltip-trigger")!;
    expect(byTypeTrigger).not.toHaveAttribute("title");
    expect(byTypeTrigger).toHaveAttribute("tabindex", "0");
    expect(byTypeTrigger).toHaveAccessibleName("Duration 1h 05m; rounded to nearest second 1h 05m 01s");
    expect(document.getElementById(byTypeTrigger.getAttribute("aria-describedby")!)).toHaveAttribute("role", "tooltip");

    const rowDuration = (await screen.findByRole("table")).querySelector<HTMLElement>("tbody .duration-value")!;
    expect(rowDuration).toHaveTextContent("1h 05m");
    expect(rowDuration.querySelector(".tooltip-trigger")).not.toHaveAttribute("title");
    const mobileRow = document.querySelector<HTMLButtonElement>(".mobile-workout-summary > button:first-child")!;
    expect(mobileRow).toHaveAttribute("title", "1h 05m 01s");
    expect(mobileRow.querySelector(".duration-value")).toHaveTextContent("1h 05m");
    expect(mobileRow.querySelector("[tabindex]")).toBeNull();
    fireEvent.click(mobileRow);
    const details = document.getElementById(mobileRow.getAttribute("aria-controls")!)!;
    const detailDuration = within(details).getByText("Duration").closest("div")!;
    expect(detailDuration).toHaveTextContent("Duration1h 05m");
    const detailTrigger = detailDuration.querySelector<HTMLElement>(".duration-value .tooltip-trigger")!;
    expect(detailTrigger).toHaveAttribute("tabindex", "0");
    expect(document.getElementById(detailTrigger.getAttribute("aria-describedby")!)).toHaveTextContent("1h 05m 01s");
  });

  test("persists shortcut and validated custom selections with CSRF while retaining a failed selection", async () => {
    const patches: Array<{ body: unknown; csrf: string | null }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage()));
      if (path === "/api/me/preferences" && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body));
        patches.push({ body, csrf: new Headers(init.headers).get("X-CSRF-Token") });
        return Promise.resolve(body.dateRange === "last7Days" ? json({ title: "Unavailable" }, 503) : json({ ...preferences, dateRange: body.dateRange }));
      }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Select date range" }));
    expect(screen.queryByText("Quick ranges")).not.toBeInTheDocument();
    expect(within(screen.getByRole("menuitem", { name: /Last 30 days/ })).getByLabelText("selected")).toHaveTextContent("✓");
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    expect(await screen.findByRole("status")).toHaveTextContent("continue using your selection");
    expect(screen.getByRole("button", { name: "Select date range" })).toHaveTextContent("Last 7 days");

    await user.click(screen.getByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: /^Custom/ }));
    const startDate = await screen.findByLabelText("Start date");
    const endDate = screen.getByLabelText("End date");
    await user.type(startDate, "2026-08-05");
    await user.tab();
    expect(endDate).toHaveValue("2026-08-05");
    await user.clear(endDate);
    await user.type(endDate, "2026-08-01");
    await user.click(screen.getByRole("button", { name: "Apply range" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("on or before");
    await waitFor(() => expect(alert).toHaveFocus());
    expect(screen.getByLabelText("Start date")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("End date")).toHaveAccessibleDescription("Start date must be on or before end date.");
    await user.clear(screen.getByLabelText("End date"));
    await user.type(screen.getByLabelText("End date"), "2026-08-06");
    await user.click(screen.getByRole("button", { name: "Apply range" }));
    await waitFor(() => expect(patches).toHaveLength(2));
    expect(patches).toEqual([
      { body: { dateRange: "last7Days" }, csrf: "csrf-summary" },
      { body: { dateRange: "2026-08-05/2026-08-06" }, csrf: "csrf-summary" },
    ]);
    await waitFor(() => {
      const customRead = vi.mocked(fetch).mock.calls.map(([input]) => String(input)).find((path) => path.includes("startDate=2026-08-05"));
      expect(customRead).toContain("endDate=2026-08-06");
      expect(customRead).not.toContain("tz=");
    });
  });

  test("does not replace an existing custom-range end date when the start date loses focus", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json(summary))
      : Promise.resolve(json(workoutPage())));
    renderSummary({ dateRange: "2026-08-01/2026-08-31" });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: /^Custom/ }));
    const startDate = await screen.findByLabelText("Start date");
    const endDate = screen.getByLabelText("End date");
    await user.clear(startDate);
    await user.type(startDate, "2026-08-05");
    await user.tab();
    expect(endDate).toHaveValue("2026-08-31");
  });

  test("renders precise converted aggregate cards and exposes type breakdowns by pointer and keyboard state", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    renderSummary();
    const cards = await screen.findAllByText("By workout type");
    expect(cards).toHaveLength(4);
    const distanceCard = screen.getByRole("button", { name: /Distance.*By workout type/ });
    expect(distanceCard).toHaveTextContent("6.52 mi");
    const durationCard = screen.getByRole("button", { name: /Duration.*By workout type/ });
    expect(durationCard).toHaveTextContent("1h 05m");
    expect(durationCard).not.toHaveAttribute("title");
    expect(document.getElementById(durationCard.getAttribute("aria-describedby")!)).toHaveTextContent("1h 05m 01s");
    expect(distanceCard).toHaveAttribute("aria-expanded", "false");
    const user = userEvent.setup();
    await user.hover(distanceCard);
    expect(distanceCard).toHaveAttribute("aria-expanded", "true");
    const breakdown = document.getElementById(distanceCard.getAttribute("aria-controls")!);
    expect(breakdown).toHaveAttribute("aria-hidden", "false");
    expect(breakdown).toHaveTextContent("Running");
    expect(breakdown).toHaveTextContent("6.52 mi");
    await user.unhover(distanceCard);
    expect(distanceCard).toHaveAttribute("aria-expanded", "false");
    distanceCard.focus();
    await waitFor(() => expect(distanceCard).toHaveAttribute("aria-expanded", "true"));
    await user.keyboard("{Escape}");
    expect(distanceCard).toHaveAttribute("aria-expanded", "false");
    expect(breakdown).toHaveAttribute("aria-hidden", "true");
    fireEvent.pointerEnter(distanceCard, { pointerType: "touch" });
    expect(distanceCard).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(distanceCard);
    expect(distanceCard).toHaveAttribute("aria-expanded", "true");
  });

  test("rounds calories everywhere while omitting kcal only from individual workout values", async () => {
    const calorieTotals = { ...totals, energy: { value: "450.75", unit: "kcal" } };
    const calorieSummary = { ...summary, totals: calorieTotals, byType: [{ type: running, totals: calorieTotals }] };
    const calorieWorkout = { ...workout, calories: { value: "450.75", unit: "kcal" } };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json(calorieSummary))
      : Promise.resolve(json(workoutPage(1, [calorieWorkout]))));
    renderSummary({ workoutColumns: ["date", "calories"] });

    const table = await screen.findByRole("table");
    const calorieCell = within(table).getAllByRole("row")[1].querySelector<HTMLElement>(".workout-cell--calories")!;
    expect(calorieCell).toHaveTextContent("451");
    expect(calorieCell).not.toHaveTextContent("kcal");

    const energyCard = screen.getByRole("button", { name: /Energy.*By workout type/ });
    expect(energyCard).toHaveTextContent("451 kcal");
    fireEvent.click(energyCard);
    expect(document.getElementById(energyCard.getAttribute("aria-controls")!)).toHaveTextContent("Running451 kcal");

    const mobileRow = document.querySelector<HTMLButtonElement>(".mobile-workout-summary > button:first-child")!;
    fireEvent.click(mobileRow);
    const calorieDetail = within(document.getElementById(mobileRow.getAttribute("aria-controls")!)!).getByText("Calories").closest("div")!;
    expect(calorieDetail).toHaveTextContent("Calories451");
    expect(calorieDetail).not.toHaveTextContent("kcal");
  });

  test("uses canonical preference columns and sends one server sort while resetting the page", async () => {
    const workoutRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) { workoutRequests.push(path); return Promise.resolve(json(workoutPage())); }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary({ workoutColumns: ["duration", "date", "calories"] });
    let table = await screen.findByRole("table");
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual(["Date▼", "Duration▲ ▼", "Calories▲ ▼", "Actions"]);
    await userEvent.click(within(table).getByRole("button", { name: /Duration/ }));
    await waitFor(() => expect(workoutRequests.some((path) => new URL(path, "https://test").searchParams.get("sort") === "duration:asc")).toBe(true));
    table = await screen.findByRole("table");
    expect(within(table).getByText("Duration").closest("th")).toHaveAttribute("aria-sort", "ascending");
  });

  test("uses exact sort glyphs and keeps fixed weighted columns stable across sorting", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    renderSummary({ workoutColumns: ["calories", "duration", "date", "type"] });
    const table = await screen.findByRole("table");
    const headers = within(table).getAllByRole("columnheader");
    const indicators = headers.map((header) => header.querySelector<HTMLElement>(".sort-indicator")?.textContent);
    const widths = Array.from(table.querySelectorAll<HTMLTableColElement>("col"), (column) => column.style.width);

    expect(headers.map((header) => header.textContent)).toEqual(["Date▼", "Type▲ ▼", "Duration▲ ▼", "Calories▲ ▼", "Actions"]);
    expect(indicators).toEqual(["▼", "▲ ▼", "▲ ▼", "▲ ▼", undefined]);
    expect(headers.map((header) => header.getAttribute("aria-sort"))).toEqual(["descending", "none", "none", "none", null]);
    expect(widths).toEqual(["37.037%", "25.9259%", "18.5185%", "18.5186%", ""]);
    expect(widths.slice(0, -1).reduce((total, width) => total + Number.parseFloat(width), 0)).toBe(100);

    await userEvent.click(within(table).getByRole("button", { name: "Type" }));
    await waitFor(() => expect(headers[1]).toHaveAttribute("aria-sort", "ascending"));
    expect(headers.map((header) => header.querySelector<HTMLElement>(".sort-indicator")?.textContent)).toEqual(["▲ ▼", "▲", "▲ ▼", "▲ ▼", undefined]);
    expect(Array.from(table.querySelectorAll<HTMLTableColElement>("col"), (column) => column.style.width)).toEqual(widths);
  });

  test("retains the same full-opacity table during a deferred sort and swaps rows on response", async () => {
    const walking = { id: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", key: "walking", displayName: "Walking" };
    const secondWorkout = { ...workout, id: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", sourceId: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", type: walking };
    let resolveSort!: (response: Response) => void;
    const deferredSort = new Promise<Response>((resolve) => { resolveSort = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const sort = new URL(path, "https://test").searchParams.get("sort");
      return sort === "type:asc" ? deferredSort : Promise.resolve(json(workoutPage(1, [workout, secondWorkout])));
    });
    renderSummary({ workoutColumns: ["date", "type"] });
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(screen.queryByText(/^Sorted by/)).not.toBeInTheDocument();

    await userEvent.click(within(table).getByRole("button", { name: "Type" }));
    expect(screen.getByRole("table")).toBe(table);
    expect(within(table).getAllByRole("row").slice(1)).toEqual(rows);
    expect(table.closest(".workout-results")).toHaveAttribute("aria-busy", "true");
    expect(table.closest(".workout-results")).not.toHaveClass("is-refetching");
    expect(screen.queryByText("Loading workouts...")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Sorting by Type ascending...");

    resolveSort(json(workoutPage(1, [secondWorkout, workout])));
    await waitFor(() => expect(Array.from(table.querySelectorAll("tbody .workout-cell--type"), (cell) => cell.textContent)).toEqual(["Walking", "Running"]));
    expect(table.closest(".workout-results")).toHaveAttribute("aria-busy", "false");
    expect(await screen.findByRole("status")).toHaveTextContent("Sorted by Type ascending.");
  });

  test("retains page 2 only while a new sort resets to a deferred page 1", async () => {
    let resolveSort!: (response: Response) => void;
    const deferredSort = new Promise<Response>((resolve) => { resolveSort = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const params = new URL(path, "https://test").searchParams;
      if (params.get("sort") === "type:asc") return deferredSort;
      return Promise.resolve(json(workoutPage(Number(params.get("page")))));
    });
    renderSummary({ workoutColumns: ["date", "type"] });
    await userEvent.click(await screen.findByRole("button", { name: "Next" }));
    const pageTwoTable = await screen.findByRole("table");
    const pageTwoRows = within(pageTwoTable).getAllByRole("row").slice(1);
    expect(screen.getByText("Page 2 of 2", { selector: ".pagination span" })).toBeInTheDocument();

    await userEvent.click(within(pageTwoTable).getByRole("button", { name: "Type" }));
    expect(screen.getByRole("table")).toBe(pageTwoTable);
    expect(within(pageTwoTable).getAllByRole("row").slice(1)).toEqual(pageTwoRows);
    expect(screen.getByText("Page 2 of 2", { selector: ".pagination span" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Sorting by Type ascending...");
    expect(screen.queryByText("Loading workouts...")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();

    resolveSort(json(workoutPage(1)));
    expect(await screen.findByText("Page 1 of 2", { selector: ".pagination span" })).toBeInTheDocument();
  });

  test("updates live sort progress for rapid selections and announces only the active completion", async () => {
    let resolveAscending!: (response: Response) => void;
    let resolveDescending!: (response: Response) => void;
    let ascendingSignal: AbortSignal | null | undefined;
    const ascending = new Promise<Response>((resolve) => { resolveAscending = resolve; });
    const descending = new Promise<Response>((resolve) => { resolveDescending = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const requestedSort = new URL(path, "https://test").searchParams.get("sort");
      if (requestedSort === "type:asc") { ascendingSignal = init?.signal; return ascending; }
      if (requestedSort === "type:desc") return descending;
      return Promise.resolve(json(workoutPage()));
    });
    renderSummary({ workoutColumns: ["date", "type"] });
    const table = await screen.findByRole("table");
    const typeSort = within(table).getByRole("button", { name: "Type" });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    await userEvent.click(typeSort);
    expect(screen.getByRole("status")).toHaveTextContent("Sorting by Type ascending...");
    await userEvent.click(typeSort);
    expect(screen.getByRole("status")).toHaveTextContent("Sorting by Type descending...");
    expect(ascendingSignal?.aborted).toBe(true);

    resolveAscending(json(workoutPage()));
    expect(screen.getByRole("status")).toHaveTextContent("Sorting by Type descending...");
    resolveDescending(json(workoutPage()));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Sorted by Type descending."));
    expect(screen.queryByText("Sorted by Type ascending.")).not.toBeInTheDocument();
  });

  test("does not retain workout rows when the date range changes", async () => {
    let resolveRange!: (response: Response) => void;
    const deferredRange = new Promise<Response>((resolve) => { resolveRange = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path === "/api/me/preferences" && init?.method === "PATCH") return Promise.resolve(json({ ...preferences, dateRange: "last7Days" }));
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.includes("dateRangeEnum=last7Days")) return deferredRange;
      return Promise.resolve(json(workoutPage()));
    });
    renderSummary();
    const user = userEvent.setup();
    await screen.findByRole("table");
    await user.click(screen.getByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText("Loading workouts...")).toHaveAttribute("role", "status");
    expect(screen.queryByText("No workouts in this range.")).not.toBeInTheDocument();
    resolveRange(json(workoutPage()));
    expect(await screen.findByRole("table")).toBeInTheDocument();
  });

  test("uses server pagination and mobile rows expose expansion semantics and details", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const requestedPage = new URL(path, "https://test").searchParams.get("page");
      return Promise.resolve(json(workoutPage(Number(requestedPage))));
    });
    renderSummary();
    const next = await screen.findByRole("button", { name: "Next" });
    await userEvent.click(next);
    expect(await screen.findByText("Page 2 of 2", { selector: ".pagination span" })).toBeInTheDocument();
    const mobileButton = document.querySelector<HTMLButtonElement>(".mobile-workout-summary > button:first-child")!;
    const mobileCard = mobileButton.closest(".mobile-workout")!;
    expect(mobileButton).toHaveAttribute("aria-expanded", "false");
    expect(mobileCard).not.toHaveClass("is-expanded");
    const details = document.getElementById(mobileButton.getAttribute("aria-controls")!);
    expect(details).toHaveAttribute("hidden");
    await userEvent.click(mobileButton);
    expect(mobileButton).toHaveAttribute("aria-expanded", "true");
    expect(mobileCard).toHaveClass("is-expanded");
    expect(details).not.toHaveAttribute("hidden");
    expect(details).toHaveTextContent("Heart rateUnavailable");
    expect(details).toHaveTextContent("Local start");
    expect(details).toHaveTextContent("Local end");
    expect(details).toHaveTextContent("8:35 AM");
    expect(within(details!).getAllByLabelText("America/Denver (UTC-6:00)")).toHaveLength(2);
  });

  test("opens full chronological provenance from desktop and mobile action positions", async () => {
    const provenanceRequests: Array<{ path: string; signal?: AbortSignal | null }> = [];
    const provenance = {
      workoutId: workout.id,
      items: [
        { id: "11111111111111111111111111111111", kind: "created", jobId: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1", sourceId: workout.sourceId, sourceName: "Archived NFS", sourceType: "health-auto-export-local", sourceFile: "2026-08-05.json", warnings: [], importedAt: "2026-08-05T15:00:00Z" },
        { id: "22222222222222222222222222222222", kind: "updated", jobId: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2", sourceId: workout.sourceId, sourceName: "Archived NFS", sourceType: "health-auto-export-local", sourceFile: "2026-08-05.json", warnings: [{ code: "invalid_optional_route_value", field: "route_speed", routePoint: 4 }], importedAt: "2026-08-06T15:00:00Z" },
        { id: "33333333333333333333333333333333", kind: "matched_unchanged", jobId: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA3", sourceId: workout.sourceId, sourceName: "Archived NFS", sourceType: "health-auto-export-local", sourceFile: "2026-08-05.json", warnings: [], importedAt: "2026-08-07T15:00:00Z" },
      ],
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage()));
      if (path.includes("/provenance")) { provenanceRequests.push({ path, signal: init?.signal }); return Promise.resolve(json(provenance)); }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const triggers = await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" });
    expect(triggers).toHaveLength(2);
    expect(triggers[0].closest("td")).toHaveClass("workout-actions-cell");
    expect(triggers[1].parentElement).toHaveClass("mobile-workout-summary");
    expect(triggers[1].closest(".mobile-workout")?.querySelector(".mobile-workout-details")?.contains(triggers[1])).toBe(false);

    await user.click(triggers[0]);
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["View provenance", "Export GeoJSON", "Export points", "Delete workout"]);
    await user.click(screen.getByRole("menuitem", { name: "View provenance" }));
    const dialog = await screen.findByRole("dialog", { name: "Workout provenance" });
    expect(within(dialog).getByText("Running on 2026-08-05. Full import history, oldest first.")).toBeInTheDocument();
    expect(within(dialog).getByText("Workout provenance").closest(".provenance-dialog-header")).toBeInTheDocument();
    expect(dialog.querySelector(".provenance-timeline")?.closest(".provenance-dialog-body")).toBeInTheDocument();
    expect(within(dialog).getAllByRole("listitem", { hidden: false }).filter((item) => item.parentElement?.classList.contains("provenance-timeline")).map((item) => item.querySelector(".provenance-event-heading strong")?.textContent)).toEqual(["Imported", "Updated", "Matched unchanged"]);
    expect(dialog).toHaveTextContent("Archived NFS");
    expect(dialog).toHaveTextContent("2026-08-05.json");
    expect(dialog).toHaveTextContent("Source typehealth-auto-export-local");
    expect(dialog).toHaveTextContent("Job IDAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1");
    expect(dialog).toHaveTextContent("invalid optional route value: route speed, point 5");
    expect(provenanceRequests).toHaveLength(1);
    expect(provenanceRequests[0].path).toBe(`/api/workouts/${workout.id}/provenance`);
    expect(provenanceRequests[0].signal).toBeInstanceOf(AbortSignal);

    await user.click(within(dialog).getByRole("button", { name: "Close Workout provenance" }));
    await waitFor(() => expect(triggers[0]).toHaveFocus());
  });

  test("keeps provenance errors private and closes stale workouts while refreshing Summary", async () => {
    let provenanceAttempts = 0;
    let workoutReads = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) { workoutReads += 1; return Promise.resolve(json(workoutPage())); }
      if (path.includes("/provenance")) {
        provenanceAttempts += 1;
        return Promise.resolve(json({ title: provenanceAttempts === 1 ? "private database detail" : "Not Found" }, provenanceAttempts === 1 ? 503 : 404));
      }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const trigger = (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0];
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "View provenance" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Provenance is unavailable");
    expect(alert).not.toHaveTextContent("private database detail");
    await user.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Workout provenance" })).not.toBeInTheDocument());
    await waitFor(() => expect(workoutReads).toBeGreaterThan(1));
    expect(trigger).toHaveFocus();
  });

  test("puts Show on map first for routed workouts and passes the compact workout ID", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    const showOnMap = vi.fn();
    renderSummary({}, vi.fn(), showOnMap);
    const user = userEvent.setup();
    const trigger = (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0];
    await user.click(trigger);
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["Show on map", "View provenance", "Export GeoJSON", "Export points", "Delete workout"]);
    await user.click(screen.getByRole("menuitem", { name: "Show on map" }));
    expect(showOnMap).toHaveBeenCalledWith(workout.id);
  });

  test("buffers normalized points before starting the server-provided download", async () => {
    const createObjectURL = vi.fn((_blob: Blob) => "blob:workout-points");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    let clicked: { download: string; href: string } | undefined;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked = { download: this.download, href: this.href };
    });
    const exportRequests: Array<{ path: string; accept: string | null; credentials?: RequestCredentials }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage()));
      if (path.endsWith("/route") || path.endsWith("/route/points")) {
        exportRequests.push({ path, accept: new Headers(init?.headers).get("Accept"), credentials: init?.credentials });
        const geoJSON = path.endsWith("/route");
        return Promise.resolve(new Response(JSON.stringify({ schemaVersion: 1, workoutId: workout.id, workoutType: "Running", startedAt: workout.startedAt, endedAt: workout.endedAt, points: [] }), {
          status: 200,
          headers: { "Content-Type": geoJSON ? "application/geo+json" : "application/json", "Content-Disposition": `attachment; filename="2026-08-05-running.${geoJSON ? "geojson" : "json"}"` },
        }));
      }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const trigger = (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0];
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Export GeoJSON" }));
    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(clicked).toEqual({ download: "2026-08-05-running.geojson", href: "blob:workout-points" });
    expect(exportRequests[0]).toEqual({ path: `/api/workouts/${workout.id}/route`, accept: "application/geo+json, application/problem+json", credentials: "same-origin" });

    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Export points" }));
    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(2));
    expect(createObjectURL.mock.calls[1][0]).toBeInstanceOf(Blob);
    expect(clicked).toEqual({ download: "2026-08-05-running.json", href: "blob:workout-points" });
    expect(revokeObjectURL).toHaveBeenCalledTimes(2);
    expect(document.querySelector('a[download="2026-08-05-running.json"]')).toBeNull();
    expect(exportRequests[1]).toEqual({ path: `/api/workouts/${workout.id}/route/points`, accept: "application/json, application/problem+json", credentials: "same-origin" });
    expect(screen.queryByText("Workout points could not be exported. Try again.")).not.toBeInTheDocument();
  });

  test("hides route exports without points and never creates a partial failed download", async () => {
    const createObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    const unrouted = { ...workout, id: "99999999999999999999999999999999", routeAvailable: false, routePointCount: 0 };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage(1, [workout, unrouted])));
      if (path.endsWith("/route/points")) return Promise.resolve(json({ title: "private export failure" }, 503));
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const triggers = await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" });
    await user.click(triggers[1]);
    expect(screen.getByRole("menuitem", { name: "View provenance" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Export GeoJSON" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Export points" })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(triggers[0]);
    await user.click(screen.getByRole("menuitem", { name: "Export points" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Workout points could not be exported. Try again.");
    expect(alert).not.toHaveTextContent("private export failure");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  test("confirms and optimistically hides an individual workout before durable deletion is accepted", async () => {
    let resolveDelete!: (response: Response) => void;
    const deferredDelete = new Promise<Response>((resolve) => { resolveDelete = resolve; });
    let deleted = false;
    let deletionRequest: { path: string; method?: string; csrf: string | null; body: BodyInit | null | undefined } | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(deleted ? { ...summary, totals: { count: 1, duration: "0", distance: { value: "0", unit: "km" }, energy: { value: "0", unit: "kcal" } }, byType: [] } : summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(deleted
        ? { ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }
        : { ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 } }));
      if (path === `/api/workouts/${workout.id}`) {
        deletionRequest = { path, method: init?.method, csrf: new Headers(init?.headers).get("X-CSRF-Token"), body: init?.body };
        return deferredDelete;
      }
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const trigger = (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0];
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Delete workout" }));
    const dialog = screen.getByRole("dialog", { name: "Delete workout?" });
    expect(dialog).toHaveTextContent("Delete Running on Aug 5 2026?");
    expect(dialog).toHaveTextContent("This workout will be hidden from view immediately. All associated data, including route, import history, and derived data, will be purged by a background task. This cannot be undone. Are you sure?");
    expect(dialog).toHaveClass("single-deletion-dialog");
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus());
    expect(within(dialog).getAllByRole("button").map((button) => button.textContent)).toEqual(["Cancel", "Yes"]);
    await user.click(within(dialog).getByRole("button", { name: "Yes" }));
    expect(deletionRequest).toEqual({ path: `/api/workouts/${workout.id}`, method: "DELETE", csrf: "csrf-summary", body: undefined });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Actions for Running on 2026-08-05" })).not.toBeInTheDocument());
    expect(document.querySelectorAll(".aggregate-card strong")[0]).toHaveTextContent("1");
    expect(document.querySelectorAll(".aggregate-card strong")[1]).toHaveTextContent("0m");

    deleted = true;
    resolveDelete(json({ jobId: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", status: "queued", reused: false, targetCount: 1 }, 202));
    await waitFor(() => expect(document.getElementById("workouts-heading")).toHaveFocus());
    expect(screen.queryByText("Deletion queued. The workout is hidden now.")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "View deletion job" })).not.toBeInTheDocument();
  });

  test("rolls back an individual deletion when durable enqueue fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json({ ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 } }));
      if (path === `/api/workouts/${workout.id}`) return Promise.resolve(json({ title: "private deletion detail" }, 503));
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary();
    const user = userEvent.setup();
    const trigger = (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0];
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Delete workout" }));
    await user.click(screen.getByRole("button", { name: "Yes" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Nothing was changed");
    expect(alert).not.toHaveTextContent("private deletion detail");
    expect(alert).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "Delete workout?" })).toBeInTheDocument();
    expect(document.querySelectorAll(".aggregate-card strong")[0]).toHaveTextContent("2");
    expect(document.querySelectorAll('.workout-action-trigger[aria-label="Actions for Running on 2026-08-05"]')).toHaveLength(2);
  });

  test("requires exact DELETE before optimistically hiding an explicit range", async () => {
    let resolveDelete!: (response: Response) => void;
    const deferredDelete = new Promise<Response>((resolve) => { resolveDelete = resolve; });
    let deleted = false;
    let request: { path: string; method?: string; csrf: string | null; body: BodyInit | null | undefined } | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(deleted ? { ...summary, totals: { count: 0, duration: "0", distance: { value: "0", unit: "km" }, energy: { value: "0", unit: "kcal" } }, byType: [] } : summary));
      if (path === "/api/workouts?startDate=2026-08-01&endDate=2026-08-31") {
        request = { path, method: init?.method, csrf: new Headers(init?.headers).get("X-CSRF-Token"), body: init?.body };
        return deferredDelete;
      }
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(deleted
        ? { ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }
        : { ...workoutPage(), pagination: { page: 1, pageSize: 25, totalItems: 2, totalPages: 1 } }));
      throw new Error(`Unhandled ${path}`);
    });
    renderSummary({ dateRange: "2026-08-01/2026-08-31" });
    const user = userEvent.setup();
    const open = await screen.findByRole("button", { name: "Delete workouts in this range" });
    expect(open.querySelector("svg")).toBeInTheDocument();
    expect(open).not.toHaveTextContent("Delete workouts in this range");
    await user.click(open);
    const dialog = screen.getByRole("dialog", { name: "Delete workouts in this range?" });
    expect(dialog).toHaveClass("range-deletion-dialog", "single-deletion-dialog");
    expect(dialog).toHaveTextContent("Delete all workouts from Aug 1 2026 through Aug 31 2026, inclusive?");
    expect(dialog).toHaveTextContent("These workouts will be hidden from view immediately. All associated data, including routes, import history, and derived data, will be purged by a background task. This cannot be undone.");
    expect(within(dialog).queryByText("Confirmation")).not.toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus());
    const confirmation = within(dialog).getByRole("textbox", { name: "Type DELETE to confirm." });
    const submit = within(dialog).getByRole("button", { name: "Yes" });
    await user.type(confirmation, "delete");
    expect(submit).toBeDisabled();
    await user.clear(confirmation);
    await user.type(confirmation, "DELETE");
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() => expect(request).toEqual({ path: "/api/workouts?startDate=2026-08-01&endDate=2026-08-31", method: "DELETE", csrf: "csrf-summary", body: undefined }));
    await waitFor(() => expect(document.querySelectorAll(".workout-action-trigger")).toHaveLength(0));
    expect(document.querySelectorAll(".aggregate-card strong")[0]).toHaveTextContent("0");

    deleted = true;
    resolveDelete(json({ jobId: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", status: "queued", reused: false, targetCount: 2 }, 202));
    await waitFor(() => expect(document.getElementById("workouts-heading")).toHaveFocus());
    expect(screen.queryByText("Deletion queued for 2 workouts. They are hidden now.")).not.toBeInTheDocument();
  });

  test("keeps summary and workout error/empty states independent", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json({ title: "private detail" }, 503));
      return Promise.resolve(json({ range, pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] }));
    });
    renderSummary();
    expect(await screen.findByRole("alert")).toHaveTextContent("Range totals are unavailable");
    expect(screen.getByText("No workouts in this range.")).toBeInTheDocument();
    expect(screen.queryByText("private detail")).not.toBeInTheDocument();
  });

  test("serializes rapid range writes and applies only the latest response with CSRF", async () => {
    let resolveFirst!: (response: Response) => void;
    const firstResponse = new Promise<Response>((resolve) => { resolveFirst = resolve; });
    const patches: Array<{ dateRange: string; csrf: string | null }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) return Promise.resolve(json(workoutPage()));
      if (path === "/api/me/preferences" && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)) as { dateRange: string };
        patches.push({ dateRange: body.dateRange, csrf: new Headers(init.headers).get("X-CSRF-Token") });
        if (patches.length === 1) return firstResponse;
        return Promise.resolve(json({ ...preferences, units: "metric", dateRange: body.dateRange }));
      }
      throw new Error(`Unhandled ${path}`);
    });
    const { onDateRangeSaved } = renderSummary();
    const user = userEvent.setup();
    const trigger = await screen.findByRole("button", { name: "Select date range" });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Last month" }));
    expect(trigger).toHaveTextContent("Last month");
    expect(patches).toEqual([{ dateRange: "last7Days", csrf: "csrf-summary" }]);
    resolveFirst(json({ ...preferences, dateRange: "last7Days" }));
    await waitFor(() => expect(patches).toHaveLength(2));
    expect(patches[1]).toEqual({ dateRange: "lastMonth", csrf: "csrf-summary" });
    await waitFor(() => expect(onDateRangeSaved).toHaveBeenCalledWith("lastMonth"));
    expect(onDateRangeSaved).toHaveBeenCalledTimes(1);
  });

  test("syncs an externally changed date preference and refetches when first weekday changes", async () => {
    const summaryRequests: string[] = [];
    const workoutRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) { summaryRequests.push(path); return Promise.resolve(json(summary)); }
      if (path.startsWith("/api/workouts?")) { workoutRequests.push(path); return Promise.resolve(json(workoutPage())); }
      throw new Error(`Unhandled ${path}`);
    });
    const view = renderSummary();
    const trigger = await screen.findByRole("button", { name: "Select date range" });
    await waitFor(() => expect(summaryRequests).toHaveLength(1));
    view.rerenderPreferences({ dateRange: "thisYear" });
    await waitFor(() => expect(trigger).toHaveTextContent("This year"));
    await waitFor(() => expect(summaryRequests.some((path) => path.includes("dateRangeEnum=thisYear"))).toBe(true));
    const summaryCount = summaryRequests.length;
    const workoutCount = workoutRequests.length;
    view.rerenderPreferences({ firstWeekday: "sunday" });
    await waitFor(() => expect(summaryRequests.length).toBe(summaryCount + 1));
    expect(workoutRequests.length).toBe(workoutCount + 1);
  });

  test("resets server pagination immediately when page size changes", async () => {
    const workoutRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const params = new URL(path, "https://test").searchParams;
      workoutRequests.push(path);
      return Promise.resolve(json(workoutPage(Number(params.get("page")), [workout], Number(params.get("pageSize")))));
    });
    const view = renderSummary();
    await userEvent.click(await screen.findByRole("button", { name: "Next" }));
    await screen.findByText("Page 2 of 2", { selector: ".pagination span" });
    view.rerenderPreferences({ pageSize: 50 });
    await waitFor(() => expect(workoutRequests.some((path) => {
      const params = new URL(path, "https://test").searchParams;
      return params.get("page") === "1" && params.get("pageSize") === "50";
    })).toBe(true));
    const sizeChangeRequests = workoutRequests.filter((path) => new URL(path, "https://test").searchParams.get("pageSize") === "50");
    expect(sizeChangeRequests.every((path) => new URL(path, "https://test").searchParams.get("page") === "1")).toBe(true);
  });

  test("filters stale and duplicate columns and falls back to safe mobile-equivalent columns", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?") ? Promise.resolve(json(summary)) : Promise.resolve(json(workoutPage())));
    const view = renderSummary({ workoutColumns: ["calories", "stale", "date", "date"] as unknown as Preferences["workoutColumns"] });
    let table = await screen.findByRole("table");
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual(["Date▼", "Calories▲ ▼", "Actions"]);
    view.rerenderPreferences({ workoutColumns: ["stale"] as unknown as Preferences["workoutColumns"] });
    table = await screen.findByRole("table");
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual(["Date▼", "Type▲ ▼", "Duration▲ ▼", "Actions"]);
  });

  test("keeps a failed viewed range when page size changes", async () => {
    const workoutRequests: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      if (path.startsWith("/api/workouts?")) { workoutRequests.push(path); return Promise.resolve(json(workoutPage())); }
      if (path === "/api/me/preferences" && init?.method === "PATCH") return Promise.resolve(json({ title: "Unavailable" }, 503));
      throw new Error(`Unhandled ${path}`);
    });
    const view = renderSummary();
    const user = userEvent.setup();
    const trigger = await screen.findByRole("button", { name: "Select date range" });
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    await screen.findByRole("status");
    view.rerenderPreferences({ pageSize: 50 });
    expect(trigger).toHaveTextContent("Last 7 days");
    await waitFor(() => expect(workoutRequests.some((path) => {
      const params = new URL(path, "https://test").searchParams;
      return params.get("dateRangeEnum") === "last7Days" && params.get("pageSize") === "50";
    })).toBe(true));
  });

  test("clamps an out-of-range response and resets a nonempty empty page without empty-range copy", async () => {
    const requestedPages: number[] = [];
    let secondPageVisits = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const page = Number(new URL(path, "https://test").searchParams.get("page"));
      requestedPages.push(page);
      if (page === 3) return Promise.resolve(json({ range, pagination: { page: 3, pageSize: 25, totalItems: 26, totalPages: 2 }, items: [] }));
      if (page === 2) {
        secondPageVisits += 1;
        if (secondPageVisits === 1) return Promise.resolve(json({ range, pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 3 }, items: [workout] }));
        return Promise.resolve(json({ range, pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 2 }, items: [workout] }));
      }
      return Promise.resolve(json({ range, pagination: { page: 1, pageSize: 25, totalItems: 26, totalPages: 3 }, items: [workout] }));
    });
    renderSummary();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Next" }));
    await screen.findByText("Page 2 of 3", { selector: ".pagination span" });
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.queryByText("No workouts in this range.")).not.toBeInTheDocument();
    expect(await screen.findByText("Page 2 of 2", { selector: ".pagination span" })).toBeInTheDocument();
    expect(requestedPages).toEqual([1, 2, 3, 2]);
  });

  test("resets an empty page that still reports workouts", async () => {
    const requestedPages: number[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.startsWith("/api/summary?")) return Promise.resolve(json(summary));
      const page = Number(new URL(path, "https://test").searchParams.get("page"));
      requestedPages.push(page);
      if (page === 2) return Promise.resolve(json({ range, pagination: { page: 2, pageSize: 25, totalItems: 26, totalPages: 3 }, items: [] }));
      return Promise.resolve(json({ range, pagination: { page: 1, pageSize: 25, totalItems: 26, totalPages: 3 }, items: [workout] }));
    });
    renderSummary();
    await userEvent.click(await screen.findByRole("button", { name: "Next" }));
    await waitFor(() => expect(requestedPages).toEqual([1, 2, 1]));
    expect(screen.queryByText("No workouts in this range.")).not.toBeInTheDocument();
    expect(await screen.findByText("Page 1 of 3", { selector: ".pagination span" })).toBeInTheDocument();
  });

  test("renders a large scrollable type breakdown", async () => {
    const byType = Array.from({ length: 40 }, (_, index) => ({
      type: { id: String(index).padStart(32, "A"), key: `type-${index}`, displayName: `Workout type ${index + 1}` },
      totals: { ...totals, count: index + 1 },
    }));
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json({ ...summary, byType }))
      : Promise.resolve(json(workoutPage())));
    renderSummary();
    const card = await screen.findByRole("button", { name: /Workouts.*By workout type/ });
    fireEvent.click(card);
    const panel = document.getElementById(card.getAttribute("aria-controls")!);
    expect(panel).toHaveAttribute("aria-hidden", "false");
    expect(within(panel!).getAllByText(/Workout type \d+/)).toHaveLength(40);
    expect(panel).toHaveClass("aggregate-breakdown");
  });

  test("keeps known local date when time context is unavailable and wraps long mobile copy", async () => {
    const longType = `Ultramarathon-${"technical-ridgeline-".repeat(12)}`;
    const longLocation = `Remote trailhead ${"without-breaks".repeat(20)}`;
    const partialWorkout = {
      ...workout,
      type: { ...running, displayName: longType },
      timezone: null,
      displayTimezone: null,
      originalStartOffsetMinutes: null,
      originalEndOffsetMinutes: null,
      location: longLocation,
    };
    vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).startsWith("/api/summary?")
      ? Promise.resolve(json(summary))
      : Promise.resolve(json(workoutPage(1, [partialWorkout]))));
    renderSummary();
    await screen.findAllByText(longType);
    const mobileButton = document.querySelector<HTMLButtonElement>(".mobile-workout-summary > button:first-child")!;
    expect(mobileButton).toHaveTextContent("2026");
    expect(mobileButton).toHaveTextContent("time unavailable");
    expect(within(mobileButton).getByLabelText("Recorded offset/timezone unavailable")).toHaveTextContent("TZ unavailable");
    expect(mobileButton.querySelector(".workout-date")).toHaveClass("unavailable");
    expect(within(mobileButton).getByText(longType)).toBeInTheDocument();
    fireEvent.click(mobileButton);
    const details = document.getElementById(mobileButton.getAttribute("aria-controls")!);
    expect(details).toHaveTextContent(longLocation);
    expect(details).toHaveTextContent("Local end---UnavailableTZ unavailable");
    expect(within(details!).getAllByText("Unavailable", { selector: ".unavailable" }).length).toBeGreaterThan(0);
  });
});
