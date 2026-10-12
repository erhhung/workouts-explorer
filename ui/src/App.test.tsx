import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import maplibregl from "maplibre-gl";
import { type ReactNode } from "react";
import { describe, expect, test, vi } from "vitest";
import indexHtml from "../index.html?raw";
import { App, protectedReturnPath } from "./App";
import { SESSION_EXPIRED_EVENT, api } from "./api";
import { applyTheme } from "./theme";

const session = {
  id: "11111111111111111111111111111111",
  expiresAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
  csrfToken: "ccccccccccccccccccccccccccccccccccccccccccc",
  identity: {
    id: "22222222222222222222222222222222",
    username: "trailrunner",
    fullName: "Avery Stone",
    role: "user",
  },
};
const profile = {
  id: "22222222222222222222222222222222",
  username: "trailrunner",
  email: "avery@example.test",
  fullName: "Avery Stone",
  avatarUrl: "/api/me/avatar",
};
const preferences = {
  theme: "dark",
  units: "imperial",
  timezone: "America/Denver",
  firstWeekday: "monday",
  clockFormat: "12h",
  workoutColumns: ["date", "type", "distance", "duration"],
  pageSize: 25,
  initialized: true,
  coverageDiagnosticsEnabled: false,
  dateRange: "last30Days",
};
const emptySummary = {
  range: { startDate: "2026-07-07", endDate: "2026-08-05", timezone: "America/Denver" },
  totals: {
    count: 0,
    duration: "0",
    distance: { value: "0", unit: "km" },
    energy: { value: "0", unit: "kcal" },
    routeCount: 0,
    routedDistance: { value: "0", unit: "km" },
  },
  byType: [],
};
const emptyWorkouts = {
  range: emptySummary.range,
  pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
  items: [],
};
const oneWorkout = {
  id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  sourceId: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
  type: { id: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", key: "running", displayName: "Running" },
  startedAt: "2026-08-05T12:00:00Z",
  endedAt: "2026-08-05T13:00:00Z",
  duration: "3600",
  localStartDate: "2026-08-05",
  displayTimezone: "America/Denver",
  originalStartOffsetMinutes: -360,
  originalEndOffsetMinutes: -360,
  timezone: "America/Denver",
  indoor: false,
  location: null,
  distance: { value: "10", unit: "km" },
  pace: { value: "6", unit: "min/km" },
  splitPaces: { kilometer: null, mile: null },
  calories: { value: "500", unit: "kcal" },
  activeCalories: { value: "400", unit: "kcal" },
  heartRate: { value: "120", unit: "count/min" },
  maximumHeartRate: { value: "160", unit: "count/min" },
  elevationGain: { value: "100", unit: "m" },
  minimumElevation: null,
  maximumElevation: null,
  routePointCount: 2,
  routeAvailable: true,
};
const workoutsWithOne = {
  ...emptyWorkouts,
  pagination: { ...emptyWorkouts.pagination, totalItems: 1, totalPages: 1 },
  items: [oneWorkout],
};
const publicConfig = {
  productName: "Workouts Explorer",
  pollingIntervalSeconds: 30,
  mapFitPaddingPixels: 48,
  baseMaps: { families: [], fallbackFamilyId: "", workoutTypeMappings: [] },
  passwordMinimumLength: 12,
  pageSizeMaximum: 100,
  features: { coverageMatcherDiagnostics: false },
};
const deletionMapWorkouts = [
  oneWorkout,
  {
    ...oneWorkout,
    id: "E".repeat(32),
    localStartDate: "2026-08-01",
    type: { ...oneWorkout.type, key: "hiking", displayName: "Hiking" },
  },
].map((workout) => ({
  ...workout,
  type: { ...workout.type, name: workout.type.displayName },
  partialRoute: false,
  bounds: {
    minimumLongitude: -105.3,
    maximumLongitude: -105.1,
    minimumLatitude: 39.8,
    maximumLatitude: 40.1,
  },
  coverageReadiness: {
    mapDataStatus: "ready",
    processingStatus: "current",
    resultStatus: "current",
  },
}));
const dataSync = {
  schedule: { enabled: true, sourceCount: 0, cadence: null, cadenceSeconds: 86400, staleDays: 3 },
  sources: [],
  notifications: [],
  notificationsTruncated: false,
};
const emptyJobs = {
  pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 },
  items: [],
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

function routeFetch(
  handler?: (path: string, method: string, init?: RequestInit) => Response | Promise<Response>,
) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const path =
      typeof input === "string" ? input : input instanceof URL ? input.pathname : input.url;
    const method = init?.method ?? "GET";
    if (handler) {
      const response = handler(path, method, init);
      if (response) return Promise.resolve(response);
    }
    if (path === "/api/config" && method === "GET") return Promise.resolve(json(publicConfig));
    if (path.startsWith("/api/summary?") && method === "GET")
      return Promise.resolve(json(emptySummary));
    if (path.startsWith("/api/workouts?") && method === "GET")
      return Promise.resolve(json(emptyWorkouts));
    if (path === "/api/session" && method === "GET")
      return Promise.resolve(
        json({ type: "about:blank", title: "Unauthorized", status: 401, requestId: "test" }, 401),
      );
    throw new Error(`Unhandled fetch: ${method} ${path}`);
  });
}

function authenticatedFetch(
  handler?: (path: string, method: string, init?: RequestInit) => Response | Promise<Response>,
) {
  return routeFetch((path, method, init) => {
    if (handler) {
      const response = handler(path, method, init);
      if (response) return response;
    }
    if (path === "/api/config" && method === "GET") return json(publicConfig);
    if (path === "/api/session" && method === "GET") return json(session);
    if (path === "/api/me" && method === "GET") return json(profile);
    if (path === "/api/me/preferences" && method === "GET") return json(preferences);
    if (path.startsWith("/api/summary?") && method === "GET") return json(emptySummary);
    if (path.startsWith("/api/workouts?") && method === "GET") return json(emptyWorkouts);
    if (path === "/api/data-sync" && method === "GET") return json(dataSync);
    if (path.startsWith("/api/jobs?") && method === "GET") return json(emptyJobs);
    throw new Error(`Unhandled authenticated fetch: ${method} ${path}`);
  });
}

function renderApp(path = "/") {
  history.replaceState({}, "", path);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return {
    ...render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    ),
    client,
  };
}

function wrapper({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

describe("public authentication", () => {
  test("accepts only internal protected return locations", () => {
    expect(protectedReturnPath("/login?returnTo=%2Fmap%3FworkoutId%3DAAAA")).toBe(
      "/map?workoutId=AAAA",
    );
    expect(protectedReturnPath("/data-sync/jobs/ABC")).toBe("/data-sync/jobs/ABC");
    expect(protectedReturnPath("/login?returnTo=https%3A%2F%2Fevil.example")).toBe("/");
    expect(protectedReturnPath("/login?returnTo=%2F%2Fevil.example")).toBe("/");
  });

  test("does not expire a session for normalized public endpoint 401s", async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    routeFetch(() => json({ title: "Unauthorized" }, 401));
    const requests: Array<[string, RequestInit?]> = [
      ["/api/config?fresh=1"],
      ["/api/session?fresh=1"],
      ["/api/session", { method: "post" }],
      ["/api/session-tokens?source=cli", { method: "post" }],
      ["/api/invitations/token-value?fresh=1"],
      ["/api/registrations", { method: "POST" }],
      ["/api/password-reset-requests", { method: "POST" }],
      ["/api/password-resets", { method: "POST" }],
    ];
    await Promise.all(requests.map(([path, init]) => api(path, init).catch(() => undefined)));
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  test("renders the exact unauthenticated login labels and placeholder", async () => {
    const fetchMock = routeFetch();
    renderApp("/login");
    expect(await screen.findByLabelText("Username")).toHaveAttribute(
      "placeholder",
      "Username or E-mail",
    );
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
  });

  test("successful login sends the exact body and navigates into the restored shell", async () => {
    let posted: Record<string, string> | undefined;
    routeFetch((path, method, init) => {
      if (path === "/api/session" && method === "POST") {
        posted = JSON.parse(String(init?.body));
        return json(session, 201);
      }
      if (path === "/api/me") return json(profile);
      if (path === "/api/me/preferences") return json(preferences);
      return undefined as never;
    });
    renderApp("/login");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "avery@example.test");
    await user.type(screen.getByLabelText("Password"), "correct horse battery");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(location.pathname).toBe("/");
    expect(posted).toEqual({ username: "avery@example.test", password: "correct horse battery" });
  });

  test("returns to the selected Map workout after reauthentication", async () => {
    const workoutID = "A".repeat(32);
    const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
    routeFetch((path, method, init) => {
      if (path === "/api/session" && method === "POST") return json(session, 201);
      if (path === "/api/me") return json(profile);
      if (path === "/api/me/preferences") return json(preferences);
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        bodies.push(body);
        return json(
          {
            id: "B".repeat(32),
            expiresAt: session.expiresAt,
            dataGeneration: 1,
            range: emptySummary.range,
            bounds: null,
            focusedWorkoutId: body.focusedWorkoutId ?? null,
            workouts: deletionMapWorkouts,
            routeTileUrl: `/api/map-selections/${"B".repeat(32)}/route-tiles/1/{z}/{x}/{y}.pbf`,
          },
          201,
        );
      }
      if (path.endsWith("/route/points")) return json({ points: [] });
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp(`/login?returnTo=${encodeURIComponent(`/map?workoutId=${workoutID}`)}`);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "trailrunner");
    await user.type(screen.getByLabelText("Password"), "correct horse battery");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() =>
      expect(location.pathname + location.search).toBe(`/map?workoutId=${workoutID}`),
    );
    expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
    await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
    expect(
      screen.queryByText("Routes could not be prepared for this map."),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Running.*8\/05\/2026/ }).closest("li")).toHaveClass(
      "is-focused",
    );
    expect(bodies).toEqual([
      { dateRangeEnum: "last30Days", tz: "America/Denver", focusedWorkoutId: workoutID },
    ]);
  });

  test.each(["ordinary", "deleted", "foreign", "out-of-range", "empty", "persistent"])(
    "fresh login loads actual map routes or a bounded safe result for %s focus",
    async (scenario) => {
      const unavailableID = "F".repeat(32);
      const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
      routeFetch((path, method, init) => {
        if (path === "/api/session" && method === "POST") return json(session, 201);
        if (path === "/api/me") return json(profile);
        if (path === "/api/me/preferences") return json(preferences);
        if (path === "/api/map-selections" && method === "POST") {
          const body = JSON.parse(String(init?.body));
          bodies.push(body);
          const workouts = scenario === "empty" ? [] : deletionMapWorkouts;
          // Match API membership validation, including the previously hidden focus FK failure.
          if (
            scenario === "persistent" ||
            (body.focusedWorkoutId &&
              !workouts.some((workout) => workout.id === body.focusedWorkoutId))
          )
            return json({ title: "Not Found", detail: "private ownership or FK failure" }, 404);
          return json(
            {
              id: "B".repeat(32),
              expiresAt: session.expiresAt,
              dataGeneration: 1,
              range: emptySummary.range,
              bounds: null,
              focusedWorkoutId: null,
              workouts,
              routeTileUrl: `/api/map-selections/${"B".repeat(32)}/route-tiles/1/{z}/{x}/{y}.pbf`,
            },
            201,
          );
        }
        if (path.startsWith("/api/map-selections/") && method === "DELETE")
          return new Response(null, { status: 204 });
        return undefined as never;
      });
      const returnTo = scenario === "ordinary" ? "/map" : `/map?workoutId=${unavailableID}`;
      const { client } = renderApp(`/login?returnTo=${encodeURIComponent(returnTo)}`);
      expect(
        client
          .getQueryCache()
          .getAll()
          .every((query) => query.state.data === undefined),
      ).toBe(true);
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("Username"), "trailrunner");
      await user.type(screen.getByLabelText("Password"), "correct horse battery");
      await user.click(screen.getByRole("button", { name: "Sign in" }));
      if (scenario === "persistent")
        expect(await screen.findByText("Routes could not be prepared for this map.")).toBeVisible();
      else if (scenario === "empty")
        expect(await screen.findByText("No workout routes in this range.")).toBeVisible();
      else {
        expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeChecked();
        expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
      }
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      expect(bodies).toEqual(
        scenario === "ordinary"
          ? [{ dateRangeEnum: "last30Days", tz: "America/Denver" }]
          : [
              {
                dateRangeEnum: "last30Days",
                tz: "America/Denver",
                focusedWorkoutId: unavailableID,
              },
              { dateRangeEnum: "last30Days", tz: "America/Denver" },
            ],
      );
      expect(document.body).not.toHaveTextContent("private ownership or FK failure");
      if (scenario !== "persistent") {
        expect(location.pathname + location.search).toBe("/map");
        expect(
          screen.queryByText("Routes could not be prepared for this map."),
        ).not.toBeInTheDocument();
        await user.click(screen.getByRole("link", { name: "Summary" }));
        await user.click(screen.getByRole("link", { name: "Map" }));
        await waitFor(() => expect(bodies).toHaveLength(scenario === "ordinary" ? 2 : 3));
        await waitFor(() =>
          expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument(),
        );
        expect(bodies.at(-1)).toEqual({ dateRangeEnum: "last30Days", tz: "America/Denver" });
      }
    },
  );

  test("failed login stays public, uses safe copy, and focuses the summary", async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    routeFetch((path, method) =>
      path === "/api/session" && method === "POST"
        ? json(
            {
              type: "about:blank",
              title: "Unauthorized",
              detail: "secret backend detail",
              status: 401,
              requestId: "test",
            },
            401,
          )
        : (undefined as never),
    );
    renderApp("/login");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "unknown");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(alert).toHaveFocus());
    expect(alert).toHaveTextContent("We couldn't sign you in");
    expect(alert).not.toHaveTextContent("secret backend detail");
    expect(location.pathname).toBe("/login");
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  test("forgot recovery sends username and always presents generic accepted copy", async () => {
    let requestBody: unknown;
    routeFetch((path, method, init) => {
      if (path === "/api/password-reset-requests" && method === "POST") {
        requestBody = JSON.parse(String(init?.body));
        return json({ status: "accepted" }, 202);
      }
      return undefined as never;
    });
    renderApp("/forgot-password");
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Username or E-mail"),
      "possibly-missing@example.test",
    );
    await user.click(screen.getByRole("button", { name: "Send reset link" }));
    expect(await screen.findByRole("status")).toHaveTextContent("If an account matches");
    expect(requestBody).toEqual({ username: "possibly-missing@example.test" });
  });

  test("validates an invitation, registers exact fields, and maps API field errors", async () => {
    const token = "iiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiii";
    let registrationBody: unknown;
    routeFetch((path, method, init) => {
      if (path === `/api/invitations/${token}`)
        return json({ email: "invitee@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST") {
        registrationBody = JSON.parse(String(init?.body));
        return json(
          {
            type: "about:blank",
            title: "Invalid request",
            status: 400,
            requestId: "test",
            errors: [{ field: "username", code: "duplicate", message: "Choose another username." }],
          },
          400,
        );
      }
      return undefined as never;
    });
    renderApp(`/invitations/${token}`);
    expect(await screen.findByDisplayValue("invitee@example.test")).toHaveAttribute("readonly");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Username"), "Avery");
    await user.type(screen.getByLabelText("Full name"), "Avery Stone");
    await user.type(screen.getByLabelText("Password"), "long enough password");
    await user.type(screen.getByLabelText("Confirm password"), "long enough password");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(await screen.findByText("Choose another username.")).toHaveAttribute(
      "id",
      "username-error",
    );
    expect(screen.getByLabelText("Username")).toHaveAccessibleDescription(
      "Choose another username.",
    );
    expect(registrationBody).toEqual({
      token,
      username: "Avery",
      fullName: "Avery Stone",
      password: "long enough password",
      passwordConfirmation: "long enough password",
    });
  });

  test("successful invitation registration returns to sign in", async () => {
    const token = "sssssssssssssssssssssssssssssssssssssssssss";
    routeFetch((path, method) => {
      if (path === `/api/invitations/${token}`)
        return json({ email: "new@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST")
        return json({ ...profile, email: "new@example.test" }, 201);
      return undefined as never;
    });
    renderApp(`/invitations/${token}`);
    await screen.findByDisplayValue("new@example.test");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Username"), "newrunner");
    await user.type(screen.getByLabelText("Full name"), "New Runner");
    await user.type(screen.getByLabelText("Password"), "long enough password");
    await user.type(screen.getByLabelText("Confirm password"), "long enough password");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    await waitFor(() => expect(location.pathname).toBe("/login"));
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });

  test("uses a configured registration password minimum in attributes, copy, and validation", async () => {
    const token = "mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm";
    let registrationAttempted = false;
    routeFetch((path, method) => {
      if (path === "/api/config") return json({ ...publicConfig, passwordMinimumLength: 16 });
      if (path === `/api/invitations/${token}`)
        return json({ email: "minimum@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST") {
        registrationAttempted = true;
        return json(profile, 201);
      }
      return undefined as never;
    });
    renderApp(`/invitations/${token}`);
    const password = await screen.findByLabelText("Password");
    expect(password).toHaveAttribute("minlength", "16");
    expect(
      screen.getByText("16-128 characters. Spaces and symbols are welcome."),
    ).toBeInTheDocument();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Username"), "minimum");
    await user.type(screen.getByLabelText("Full name"), "Minimum Test");
    await user.type(password, "fifteen-characters");
    await user.type(screen.getByLabelText("Confirm password"), "fifteen-characters");
    await user.clear(password);
    await user.type(password, "only-fifteen-xx");
    await user.clear(screen.getByLabelText("Confirm password"));
    await user.type(screen.getByLabelText("Confirm password"), "only-fifteen-xx");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(await screen.findByText("Use at least 16 characters.")).toBeInTheDocument();
    expect(registrationAttempted).toBe(false);
  });

  test("completes password reset with token and confirmation", async () => {
    const token = "rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr";
    let body: unknown;
    routeFetch((path, method, init) => {
      if (path === "/api/password-resets" && method === "POST") {
        body = JSON.parse(String(init?.body));
        return new Response(null, { status: 204 });
      }
      return undefined as never;
    });
    renderApp(`/reset-password?token=${token}`);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("New password"), "a newly secure password");
    await user.type(screen.getByLabelText("Confirm new password"), "a newly secure password");
    await user.click(screen.getByRole("button", { name: "Reset password" }));
    expect(
      await screen.findByRole("heading", { name: "Your password is reset." }),
    ).toBeInTheDocument();
    expect(body).toEqual({
      token,
      password: "a newly secure password",
      passwordConfirmation: "a newly secure password",
    });
  });

  test("uses a different configured password minimum for reset validation", async () => {
    const token = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv";
    let resetAttempted = false;
    routeFetch((path, method) => {
      if (path === "/api/config") return json({ ...publicConfig, passwordMinimumLength: 20 });
      if (path === "/api/password-resets" && method === "POST") {
        resetAttempted = true;
        return new Response(null, { status: 204 });
      }
      return undefined as never;
    });
    renderApp(`/reset-password?token=${token}`);
    const password = await screen.findByLabelText("New password");
    expect(password).toHaveAttribute("minlength", "20");
    expect(screen.getByText("Use 20-128 characters.")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.type(password, "short but matching");
    await user.type(screen.getByLabelText("Confirm new password"), "short but matching");
    await user.click(screen.getByRole("button", { name: "Reset password" }));
    expect(await screen.findByText("Use at least 20 characters.")).toBeInTheDocument();
    expect(resetAttempted).toBe(false);
  });

  test("fails closed with safe copy when public configuration is unavailable", async () => {
    routeFetch((path) =>
      path === "/api/config"
        ? json(
            {
              type: "about:blank",
              title: "Database details",
              detail: "private failure",
              status: 503,
              requestId: "test",
            },
            503,
          )
        : (undefined as never),
    );
    renderApp("/login");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Application settings could not be loaded");
    expect(alert).not.toHaveTextContent("private failure");
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });
});

describe("authenticated shell", () => {
  test.each(["same principal", "different principal", "different principal with same session ID"])(
    "installs a confirmed replacement for %s and resets private Shell state",
    async (replacement) => {
      const confirmed = {
        ...session,
        id:
          replacement === "different principal with same session ID" ? session.id : "F".repeat(32),
        csrfToken: "new-session-csrf",
        identity:
          replacement === "same principal"
            ? session.identity
            : {
                ...session.identity,
                id: "E".repeat(32),
                fullName: "New Explorer",
              },
      };
      let checks = 0;
      let profileGets = 0;
      let replaced = false;
      let resolveProfile!: (response: Response) => void;
      const selectionBodies: Array<{
        dateRangeEnum?: string;
        workoutIds?: string[];
        csrf: string | null;
      }> = [];
      const fetchMock = authenticatedFetch((path, method, init) => {
        if (path === "/api/session" && method === "GET") {
          if (++checks === 1) return json(session);
          replaced = true;
          return json(confirmed);
        }
        if (path === "/api/me" && method === "GET" && ++profileGets > 1)
          return new Promise((resolve) => {
            resolveProfile = resolve;
          });
        if (path === "/api/map-selections" && method === "POST") {
          const body = JSON.parse(String(init?.body));
          selectionBodies.push({ ...body, csrf: new Headers(init?.headers).get("X-CSRF-Token") });
          return json({
            id: (replaced ? "D" : "B").repeat(32),
            expiresAt: session.expiresAt,
            dataGeneration: 1,
            range: emptySummary.range,
            bounds: null,
            focusedWorkoutId: null,
            workouts: deletionMapWorkouts.filter(
              (workout) => body.workoutIds === undefined || body.workoutIds.includes(workout.id),
            ),
            routeTileUrl: "/tiles/{z}/{x}/{y}",
          });
        }
        if (path.startsWith("/api/map-selections/") && method === "DELETE")
          return new Response(null, { status: 204 });
        if (path === "/api/me/preferences" && method === "PATCH")
          return json({ ...preferences, ...JSON.parse(String(init?.body)) });
        return undefined as never;
      });
      const { client } = renderApp("/map?q=keep");
      const user = userEvent.setup();
      await screen.findByRole("checkbox", { name: /Show Running/ });
      await user.click(screen.getByRole("button", { name: "Select date range" }));
      await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
      await user.click(screen.getByRole("checkbox", { name: /Show Hiking/ }));
      await waitFor(() => expect(selectionBodies.at(-1)?.workoutIds).toEqual([oneWorkout.id]));
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      const oldRunning = screen.getByRole("checkbox", { name: /Show Running/ });
      client.setQueryData(["private-sentinel"], "old-account-data");
      const publicSettings = client.getQueryData(["public-config"]);
      await act(async () => {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      });
      await screen.findByText("Restoring your route preferences...");
      expect(screen.queryByRole("button", { name: /Open account menu/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("checkbox", { name: /Show Running/ })).not.toBeInTheDocument();
      expect(client.getQueryData(["private-sentinel"])).toBeUndefined();
      expect(client.getQueryData(["public-config"])).toBe(publicSettings);
      expect(client.getQueryData(["session"])).toEqual(confirmed);
      await act(async () => {
        resolveProfile(
          json({ ...profile, id: confirmed.identity.id, fullName: confirmed.identity.fullName }),
        );
      });
      expect(
        await screen.findByRole("button", {
          name: `Open account menu for ${confirmed.identity.fullName}`,
        }),
      ).toBeInTheDocument();
      expect(await screen.findByRole("checkbox", { name: /Show Running/ })).not.toBe(oldRunning);
      expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      expect(selectionBodies.at(-1)).toEqual({
        dateRangeEnum: "last30Days",
        tz: "America/Denver",
        csrf: confirmed.csrfToken,
      });
      expect(profileGets).toBe(2);
      expect(location.pathname + location.search).toBe("/map?q=keep");
      expect(
        fetchMock.mock.calls.some(
          ([, init]) => init?.method === "POST" && String(init.body).includes("password"),
        ),
      ).toBe(false);
      expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
    },
  );

  test.each([401, 200])(
    "a pending old-session check returning %s cannot replace a later successful login",
    async (status) => {
      let checks = 0;
      let resolveCheck!: (response: Response) => void;
      const newSession = {
        ...session,
        id: "F".repeat(32),
        identity: { ...session.identity, id: "E".repeat(32) },
      };
      authenticatedFetch((path, method) => {
        if (path === "/api/session" && method === "GET" && ++checks > 1)
          return new Promise((resolve) => {
            resolveCheck = resolve;
          });
        if (path === "/api/session" && method === "POST") return json(newSession, 201);
        if (path === "/api/session" && method === "DELETE")
          return new Response(null, { status: 204 });
        return undefined as never;
      });
      const { client } = renderApp();
      await screen.findByRole("button", { name: /Open account menu/ });
      act(() => {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      });
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /Open account menu/ }));
      await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
      await user.type(await screen.findByLabelText("Username"), "new-user");
      await user.type(screen.getByLabelText("Password"), "test password");
      await user.click(screen.getByRole("button", { name: "Sign in" }));
      await screen.findByRole("button", { name: "Select date range" });
      client.setQueryData(["private-sentinel"], true);
      await act(async () => {
        resolveCheck(json(status === 401 ? { title: "Unauthorized" } : session, status));
      });
      expect(client.getQueryData(["session"])).toEqual(newSession);
      expect(client.getQueryData(["private-sentinel"])).toBe(true);
      expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
    },
  );

  test("only a confirmed 401 clears private cache and retains the original protected query string", async () => {
    let checks = 0;
    authenticatedFetch((path) => {
      if (path === "/api/session" && ++checks > 1) return json({ title: "Unauthorized" }, 401);
      if (path === "/api/forbidden") return json({ title: "Forbidden" }, 403);
      return undefined as never;
    });
    const { client } = renderApp("/data-sync?q=keep");
    await screen.findByRole("heading", { name: "Start a sync" });
    client.setQueryData(["private-sentinel"], true);
    await expect(api("/api/forbidden")).rejects.toMatchObject({ status: 403 });
    expect(checks).toBe(1);
    expect(client.getQueryData(["private-sentinel"])).toBe(true);
    act(() => {
      window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    });
    await screen.findByRole("button", { name: "Sign in" });
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/data-sync?q=keep");
    expect(client.getQueryData(["private-sentinel"])).toBeUndefined();
    expect(client.getQueryData(["session"])).toBeNull();
  });

  test("a replacement check rechecks the cached session after cancellation", async () => {
    let checks = 0;
    const confirmed = { ...session, id: "F".repeat(32), csrfToken: "stale-confirmed-csrf" };
    const current = { ...session, id: "D".repeat(32), csrfToken: "current-csrf" };
    authenticatedFetch((path) =>
      path === "/api/session" && ++checks > 1 ? json(confirmed) : (undefined as never),
    );
    const { client } = renderApp();
    await screen.findByRole("button", { name: "Select date range" });
    let finishCancellation!: () => void;
    const cancel = vi.spyOn(client, "cancelQueries").mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finishCancellation = resolve;
        }),
    );
    act(() => {
      window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    });
    await waitFor(() => expect(cancel).toHaveBeenCalledTimes(1));
    act(() => {
      client.setQueryData(["session"], current);
    });
    client.setQueryData(["private-sentinel"], "new-account-data");
    await act(async () => {
      finishCancellation();
    });
    expect(client.getQueryData(["session"])).toEqual(current);
    expect(client.getQueryData(["private-sentinel"])).toBe("new-account-data");
  });

  test("a background session query 503 retains the existing authenticated Shell", async () => {
    let checks = 0;
    authenticatedFetch((path) =>
      path === "/api/session" && ++checks > 1
        ? json({ title: "Unavailable" }, 503)
        : (undefined as never),
    );
    const { client } = renderApp();
    const menu = await screen.findByRole("button", { name: /Open account menu/ });
    const original = client.getQueryData(["session"]);
    client.setQueryData(["private-sentinel"], true);
    await act(async () => {
      await client.refetchQueries({ queryKey: ["session"] });
    });
    expect(screen.getByRole("button", { name: /Open account menu/ })).toBe(menu);
    expect(client.getQueryData(["session"])).toBe(original);
    expect(client.getQueryData(["private-sentinel"])).toBe(true);
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });

  test("a background session query 401 confirms expiry and clears the cached Shell", async () => {
    let checks = 0;
    authenticatedFetch((path) =>
      path === "/api/session" && ++checks > 1
        ? json({ title: "Unauthorized" }, 401)
        : (undefined as never),
    );
    const { client } = renderApp("/data-sync?q=keep");
    await screen.findByRole("heading", { name: "Start a sync" });
    client.setQueryData(["private-sentinel"], true);
    await act(async () => {
      await client.refetchQueries({ queryKey: ["session"] });
    });
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(client.getQueryData(["private-sentinel"])).toBeUndefined();
    expect(client.getQueryData(["session"])).toBeNull();
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/data-sync?q=keep");
    expect(checks).toBe(3);
  });

  test.each([200, 503])(
    "a background session query 401 retains private state when confirmation returns %s",
    async (status) => {
      let checks = 0;
      authenticatedFetch((path) => {
        if (path !== "/api/session") return undefined as never;
        checks++;
        if (checks === 2) return json({ title: "Unauthorized" }, 401);
        if (checks > 2 && status === 503) return json({ title: "Unavailable" }, 503);
        return json(session);
      });
      const { client } = renderApp("/data-sync");
      const heading = await screen.findByRole("heading", { name: "Start a sync" });
      const original = client.getQueryData(["session"]);
      client.setQueryData(["private-sentinel"], true);
      await act(async () => {
        await client.refetchQueries({ queryKey: ["session"] });
      });
      expect(checks).toBe(3);
      expect(screen.getByRole("heading", { name: "Start a sync" })).toBe(heading);
      expect(client.getQueryData(["session"])).toBe(original);
      expect(client.getQueryData(["private-sentinel"])).toBe(true);
      expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
    },
  );

  test("clicking the active Map tab cannot disable automatic logout", async () => {
    let checks = 0;
    authenticatedFetch((path, method) => {
      if (path === "/api/session" && method === "GET" && ++checks > 1)
        return json({ title: "Unauthorized" }, 401);
      if (path === "/api/expired") return json({ title: "Unauthorized" }, 401);
      if (path === "/api/map-selections" && method === "POST")
        return json({
          id: "B".repeat(32),
          expiresAt: session.expiresAt,
          dataGeneration: 1,
          range: emptySummary.range,
          bounds: null,
          focusedWorkoutId: null,
          workouts: deletionMapWorkouts,
          routeTileUrl: "/tiles/{z}/{x}/{y}",
        });
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    const { client } = renderApp("/map");
    await screen.findByRole("checkbox", { name: /Show Running/ });
    client.setQueryData(["private-sentinel"], true);
    await userEvent.click(screen.getByRole("link", { name: "Map" }));
    await act(async () => {
      await expect(api("/api/expired")).rejects.toMatchObject({ status: 401 });
    });
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(client.getQueryData(["private-sentinel"])).toBeUndefined();
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/map");
    expect(checks).toBe(2);
  });

  test.each([200, 503, "network"])(
    "coalesces transient 401 signals and preserves Map for session check %s",
    async (status) => {
      let checks = 0;
      let resolveCheck!: (response: Response) => void;
      let rejectCheck!: (error: Error) => void;
      const pending = new Promise<Response>((resolve, reject) => {
        resolveCheck = resolve;
        rejectCheck = reject;
      });
      const fetchMock = authenticatedFetch((path, method) => {
        if (path === "/api/session" && method === "GET")
          return ++checks === 1 ? json(session) : pending;
        if (path === "/api/transient") return json({ title: "Unauthorized" }, 401);
        if (path === "/api/map-selections" && method === "POST")
          return json({
            id: "B".repeat(32),
            expiresAt: session.expiresAt,
            dataGeneration: 1,
            range: emptySummary.range,
            bounds: null,
            focusedWorkoutId: null,
            workouts: deletionMapWorkouts,
            routeTileUrl: "/tiles/{z}/{x}/{y}",
          });
        if (path.startsWith("/api/map-selections/") && method === "DELETE")
          return new Response(null, { status: 204 });
        return undefined as never;
      });
      const { client } = renderApp("/map?q=keep");
      const running = await screen.findByRole("checkbox", { name: /Show Running/ });
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      const cachedSession = client.getQueryData(["session"]);
      client.setQueryData(["private-sentinel"], { retained: true });
      const remove = vi.spyOn(client, "removeQueries");
      const before = fetchMock.mock.calls.length;
      await act(async () => {
        await Promise.all(
          [api("/api/transient"), api("/api/transient")].map((request) =>
            expect(request).rejects.toMatchObject({ status: 401 }),
          ),
        );
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      });
      expect(checks).toBe(2);
      await act(async () => {
        if (status === "network") rejectCheck(new TypeError("Network unavailable"));
        else
          resolveCheck(json(status === 200 ? session : { title: "Unavailable" }, Number(status)));
      });
      expect(screen.getByRole("checkbox", { name: /Show Running/ })).toBe(running);
      expect(running).toBeChecked();
      expect(location.pathname + location.search).toBe("/map?q=keep");
      expect(client.getQueryData(["session"])).toBe(cachedSession);
      expect(client.getQueryData(["private-sentinel"])).toEqual({ retained: true });
      expect(remove).not.toHaveBeenCalled();
      expect(
        fetchMock.mock.calls
          .slice(before)
          .map(([input]) => String(input))
          .sort(),
      ).toEqual(["/api/session", "/api/transient", "/api/transient"]);
      await act(async () => {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      });
      expect(checks).toBe(3);
    },
  );

  test("authenticated login reload renders the completed Map catalog and replaces the return URL", async () => {
    authenticatedFetch((path, method) => {
      if (path === "/api/map-selections" && method === "POST")
        return json({
          id: "B".repeat(32),
          expiresAt: session.expiresAt,
          dataGeneration: 1,
          range: emptySummary.range,
          bounds: null,
          focusedWorkoutId: null,
          workouts: deletionMapWorkouts,
          routeTileUrl: "/tiles/{z}/{x}/{y}",
        });
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp(`/login?returnTo=${encodeURIComponent("/map?q=keep")}`);
    expect(await screen.findByRole("checkbox", { name: /Show Running/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Show Hiking/ })).toBeChecked();
    await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
    expect(location.pathname + location.search).toBe("/map?q=keep");
    expect(screen.getByRole("link", { name: "Map" })).toHaveAttribute("aria-current", "page");
  });

  test("authenticated login rejects an external return URL", async () => {
    authenticatedFetch();
    renderApp("/login?returnTo=https%3A%2F%2Fevil.example");
    await screen.findByRole("button", { name: "Select date range" });
    expect(location.pathname + location.search).toBe("/");
  });

  test.each(["/map", "/login"])(
    "initial session 503 shows retry rather than login at %s",
    async (path) => {
      let unavailable = true;
      authenticatedFetch((url) =>
        url === "/api/session" && unavailable
          ? json({ title: "Unavailable" }, 503)
          : (undefined as never),
      );
      renderApp(path);
      expect(await screen.findByRole("alert")).toHaveTextContent("temporarily unavailable");
      expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
      unavailable = false;
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      await screen.findByRole("link", { name: "Summary" });
    },
  );

  test.each(["identity", "unmount"])(
    "ignores pending session 401 after %s changes",
    async (change) => {
      let checks = 0;
      let resolveCheck!: (response: Response) => void;
      authenticatedFetch((path) =>
        path === "/api/session" && ++checks > 1
          ? new Promise((resolve) => {
              resolveCheck = resolve;
            })
          : (undefined as never),
      );
      const { client, unmount } = renderApp("/data-sync");
      await screen.findByRole("heading", { name: "Start a sync" });
      act(() => {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
      });
      if (change === "identity")
        act(() => {
          client.setQueryData(["session"], {
            ...session,
            id: "F".repeat(32),
            identity: { ...session.identity, id: "E".repeat(32) },
          });
        });
      if (change === "unmount") unmount();
      client.setQueryData(["private-sentinel"], true);
      await act(async () => {
        resolveCheck(json({ title: "Unauthorized" }, 401));
      });
      expect(client.getQueryData(["private-sentinel"])).toBe(true);
      expect(client.getQueryData(["session"])).toBeTruthy();
      expect(location.pathname).not.toBe("/login");
    },
  );

  test("navigation cannot discard confirmed expiry and preserves the current return path", async () => {
    let checks = 0;
    let resolveCheck!: (response: Response) => void;
    authenticatedFetch((path) =>
      path === "/api/session" && ++checks > 1
        ? new Promise((resolve) => {
            resolveCheck = resolve;
          })
        : (undefined as never),
    );
    const { client } = renderApp("/data-sync");
    await screen.findByRole("heading", { name: "Start a sync" });
    act(() => {
      window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    });
    await userEvent.click(screen.getByRole("link", { name: "Summary" }));
    client.setQueryData(["private-sentinel"], true);
    await act(async () => {
      resolveCheck(json({ title: "Unauthorized" }, 401));
    });
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(client.getQueryData(["private-sentinel"])).toBeUndefined();
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/");
  });

  test.each([
    ["all", 202],
    ["explicit", 202],
    ["none", 202],
    ["hidden", 202],
    ["explicit", 404],
    ["explicit", 503],
    ["initial", 202],
    ["range", 202],
    ["rangeChanged", 202],
    ["rangeNewImport", 202],
  ])(
    "reconciles %s map state only after deletion response %s, including tab-change races",
    async (mode, status) => {
      const bodies: Array<{ workoutIds?: string[]; focusedWorkoutId?: string }> = [];
      let resolveDelete!: (response: Response) => void;
      let unavailable = false;
      let workoutFetches = 0;
      const rangeDeletion = String(mode).startsWith("range");
      authenticatedFetch((path, method, init) => {
        if (rangeDeletion && path === "/api/me/preferences" && method === "GET")
          return json({ ...preferences, dateRange: "2026-08-05/2026-08-05" });
        if (path === "/api/me/preferences" && method === "PATCH")
          return json({ ...preferences, ...JSON.parse(String(init?.body)) });
        if (path.startsWith("/api/workouts?") && method === "GET") {
          workoutFetches++;
          return json(workoutsWithOne);
        }
        if (
          (path === `/api/workouts/${oneWorkout.id}` ||
            path === "/api/workouts?startDate=2026-08-05&endDate=2026-08-05") &&
          method === "DELETE"
        )
          return new Promise((resolve) => {
            resolveDelete = resolve;
          });
        if (path === "/api/map-selections" && method === "POST") {
          const body = JSON.parse(String(init?.body));
          bodies.push(body);
          const workouts = deletionMapWorkouts.filter(
            (workout) =>
              (rangeDeletion || !unavailable || workout.id !== oneWorkout.id) &&
              (body.workoutIds === undefined || body.workoutIds.includes(workout.id)),
          );
          if (mode === "rangeNewImport" && unavailable)
            workouts.push({
              ...deletionMapWorkouts[0],
              id: "F".repeat(32),
              type: { ...deletionMapWorkouts[0].type, name: "New import" },
            });
          return json({
            id: "B".repeat(32),
            expiresAt: new Date(Date.now() + 1800000).toISOString(),
            dataGeneration: unavailable ? 2 : 1,
            range: emptySummary.range,
            bounds: null,
            workouts,
            routeTileUrl: "/tiles/{z}/{x}/{y}",
          });
        }
        if (path.startsWith("/api/map-selections/") && method === "DELETE")
          return new Response(null, { status: 204 });
        if (path.endsWith("/route/points")) return json({ points: [] });
        return undefined as never;
      });
      const user = userEvent.setup();
      renderApp(mode === "initial" ? "/" : "/map");
      if (mode !== "initial") {
        await screen.findByRole("button", { name: /Running.*8\/05\/2026/ });
        if (mode === "explicit") {
          await user.click(screen.getByRole("checkbox", { name: /Hiking/ }));
          await user.click(screen.getByRole("button", { name: /Running.*8\/05\/2026/ }));
        } else if (mode === "none")
          await user.click(screen.getByRole("checkbox", { name: "Select all workout routes" }));
        else if (mode === "hidden")
          await user.click(screen.getByRole("checkbox", { name: /Running/ }));
        await user.click(screen.getByRole("link", { name: "Summary" }));
      }
      if (rangeDeletion) {
        await user.click(
          await screen.findByRole("button", { name: "Delete workouts in this range" }),
        );
        await user.type(screen.getByRole("textbox", { name: "Type DELETE to confirm." }), "DELETE");
      } else {
        await user.click(
          (await screen.findAllByRole("button", { name: "Actions for Running on 2026-08-05" }))[0],
        );
        await user.click(screen.getByRole("menuitem", { name: "Delete workout" }));
      }
      await user.click(screen.getByRole("button", { name: "Yes" }));
      fireEvent.click(screen.getByRole("link", { name: "Map", hidden: true }));
      await screen.findByRole("button", { name: "Routes" });
      await waitFor(() => expect(bodies.length).toBeGreaterThan(0));
      if (mode === "rangeChanged") {
        await user.click(screen.getByRole("button", { name: "Select date range" }));
        await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
        await waitFor(() => expect(bodies.at(-1)).toHaveProperty("dateRangeEnum", "last7Days"));
      }
      const before = bodies.length;
      const getsBefore = workoutFetches;
      unavailable = status !== 503;
      await act(async () => {
        resolveDelete(
          json(
            status === 202
              ? { jobId: "D".repeat(32), targetCount: 1, status: "queued" }
              : { title: "Unavailable" },
            Number(status),
          ),
        );
      });
      if (status === 503) {
        expect(screen.getByRole("button", { name: /Running.*8\/05\/2026/ })).toBeInTheDocument();
        expect(bodies.at(-1)?.workoutIds).toEqual([oneWorkout.id]);
      } else {
        await waitFor(() =>
          expect(
            screen.queryByRole("button", { name: /Running.*8\/05\/2026/ }),
          ).not.toBeInTheDocument(),
        );
        await waitFor(() => expect(bodies.length).toBeGreaterThan(before));
        const last = bodies.at(-1)!;
        expect(last.focusedWorkoutId).toBeUndefined();
        expect(last.workoutIds).toEqual(
          mode === "explicit" || mode === "none"
            ? []
            : mode === "hidden"
              ? ["E".repeat(32)]
              : undefined,
        );
        expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ })).toBeInTheDocument();
        if (mode === "rangeNewImport") {
          expect(
            await screen.findByRole("button", { name: /New import.*8\/05\/2026/ }),
          ).toBeInTheDocument();
          expect(screen.getByRole("checkbox", { name: /New import/ })).toBeChecked();
        }
      }
      expect(workoutFetches).toBe(getsBefore);
    },
  );

  test("an active session visiting login returns to the authenticated shell", async () => {
    authenticatedFetch();
    renderApp("/login");
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });

  test("loads session first, restores server theme, and renders Summary", async () => {
    let resolveSession!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => {
      resolveSession = resolve;
    });
    routeFetch((path) => {
      if (path === "/api/session") return pending;
      if (path === "/api/me") return json(profile);
      if (path === "/api/me/preferences") return json({ ...preferences, theme: "light" });
      return undefined as never;
    });
    renderApp("/");
    expect(await screen.findByText("Opening your explorer...")).toBeInTheDocument();
    resolveSession(json(session));
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    expect(screen.getByRole("navigation", { name: "Primary" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Open account menu/ })).toBeInTheDocument();
  });

  test("keeps Summary route-aware and leaves Data Sync navigation in the primary menu", async () => {
    const fetchMock = authenticatedFetch();
    renderApp("/");
    const summaryLink = await screen.findByRole("link", { name: "Summary" });
    expect(summaryLink).toHaveAttribute("aria-current", "page");
    expect(fetchMock.mock.calls.some(([input]) => String(input) === "/api/data-sync")).toBe(false);
    await userEvent.click(screen.getByRole("link", { name: "Data Sync" }));
    expect(await screen.findByRole("heading", { name: "Start a sync" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Data Sync" })).not.toBeInTheDocument();
    expect(location.pathname).toBe("/data-sync");
    expect(screen.getByRole("link", { name: "Data Sync" })).toHaveAttribute("aria-current", "page");

    await userEvent.click(screen.getByRole("button", { name: /Open account menu/ }));
    const menuItems = screen.getAllByRole("menuitem");
    expect(menuItems.map((item) => item.textContent)).not.toContain("Data Sync");
    expect(menuItems[0]).toHaveTextContent("Preferences");
  });

  test("preserves workout sorting across tabs, resets it with the range, and carries that range into Map", async () => {
    let selectionBody: unknown;
    authenticatedFetch((path, method, init) => {
      if (path.startsWith("/api/workouts?") && method === "GET") return json(workoutsWithOne);
      if (path === "/api/me/preferences" && method === "PATCH")
        return json({ ...preferences, dateRange: "last7Days" });
      if (path === "/api/map-selections" && method === "POST") {
        selectionBody = JSON.parse(String(init?.body));
        return json({
          id: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
          expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
          dataGeneration: 1,
          range: { startDate: "2026-08-03", endDate: "2026-08-09" },
          bounds: null,
          workouts: [],
          routeTileUrl: "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/routes/{z}/{x}/{y}",
        });
      }
      if (path === "/api/map-selections/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    const typeHeader = (await screen.findByRole("button", { name: "Type" })).closest("th")!;
    await user.click(screen.getByRole("button", { name: "Type" }));
    await waitFor(() => expect(typeHeader).toHaveAttribute("aria-sort", "ascending"));
    await user.click(screen.getByRole("link", { name: "Data Sync" }));
    await screen.findByRole("heading", { name: "Start a sync" });
    await user.click(screen.getByRole("link", { name: "Summary" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Type" }).closest("th")).toHaveAttribute(
        "aria-sort",
        "ascending",
      ),
    );
    await user.click(await screen.findByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Date" }).closest("th")).toHaveAttribute(
        "aria-sort",
        "descending",
      ),
    );
    await user.click(screen.getByRole("link", { name: "Map" }));
    expect(await screen.findByText("No workout routes in this range.")).toBeInTheDocument();
    expect(location.pathname).toBe("/map");
    expect(screen.getByRole("link", { name: "Map" })).toHaveAttribute("aria-current", "page");
    expect(selectionBody).toEqual({ dateRangeEnum: "last7Days", tz: "America/Denver" });
  });

  test("propagates the diagnostic feature flag to Map while still requiring a focused route", async () => {
    const routeWorkout = {
      id: "D".repeat(32),
      type: { id: "1".repeat(32), key: "running", name: "Running" },
      startedAt: "2026-08-05T12:00:00Z",
      endedAt: "2026-08-05T13:00:00Z",
      duration: "3600",
      localStartDate: "2026-08-05",
      partialRoute: false,
      bounds: {
        minimumLongitude: -105.3,
        minimumLatitude: 39.9,
        maximumLongitude: -105.2,
        maximumLatitude: 40.1,
      },
      distance: null,
      pace: null,
      calories: null,
      heartRate: null,
      elevationGain: null,
      coverageReadiness: {
        mapDataStatus: "ready",
        processingStatus: "unprocessed",
        resultStatus: "none",
      },
    };
    authenticatedFetch((path, method) => {
      if (path === "/api/config" && method === "GET")
        return json({ ...publicConfig, features: { coverageMatcherDiagnostics: true } });
      if (path === "/api/me/preferences" && method === "GET")
        return json({ ...preferences, coverageDiagnosticsEnabled: true });
      if (path === "/api/map-selections" && method === "POST")
        return json({
          id: "A".repeat(32),
          expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
          dataGeneration: 1,
          range: { startDate: "2026-07-07", endDate: "2026-08-05" },
          bounds: routeWorkout.bounds,
          workouts: [routeWorkout],
          routeTileUrl: `/api/map-selections/${"A".repeat(32)}/routes/{z}/{x}/{y}`,
          coverageTileUrl: `/api/map-selections/${"A".repeat(32)}/coverage/{z}/{x}/{y}`,
        });
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp("/map");
    const coverage = await screen.findByRole("button", { name: "Coverage" });
    expect(coverage).toBeDisabled();
    await userEvent.click(await screen.findByRole("button", { name: /Running.*8\/05\/2026/ }));
    await waitFor(() => expect(coverage).toBeEnabled());
    await userEvent.click(coverage);
    expect(coverage).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("link", { name: "Summary" }));
    await userEvent.click(screen.getByRole("link", { name: "Map" }));
    expect(await screen.findByRole("button", { name: "Routes" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  test("keeps every route checked when first opening Map through Show on map", async () => {
    const routeWorkouts = [
      {
        id: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
        type: { id: "11111111111111111111111111111111", key: "running", name: "Running" },
        startedAt: "2026-08-05T12:00:00Z",
        endedAt: "2026-08-05T13:00:00Z",
        duration: "3600",
        localStartDate: "2026-08-05",
        partialRoute: false,
        bounds: {
          minimumLongitude: -105.3,
          minimumLatitude: 39.9,
          maximumLongitude: -105.2,
          maximumLatitude: 40.1,
        },
        distance: null,
        pace: null,
        calories: null,
        heartRate: null,
        elevationGain: null,
        coverageReadiness: {
          mapDataStatus: "pending",
          processingStatus: "unprocessed",
          resultStatus: "none",
        },
      },
      {
        id: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
        type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" },
        startedAt: "2026-08-01T12:00:00Z",
        endedAt: "2026-08-01T13:00:00Z",
        duration: "3600",
        localStartDate: "2026-08-01",
        partialRoute: false,
        bounds: {
          minimumLongitude: -105.2,
          minimumLatitude: 39.8,
          maximumLongitude: -105.1,
          maximumLatitude: 40,
        },
        distance: null,
        pace: null,
        calories: null,
        heartRate: null,
        elevationGain: null,
        coverageReadiness: {
          mapDataStatus: "pending",
          processingStatus: "unprocessed",
          resultStatus: "none",
        },
      },
    ];
    const summaryWorkout = {
      ...oneWorkout,
      id: routeWorkouts[1].id,
      type: { id: routeWorkouts[1].type.id, key: "hiking", displayName: "Hiking" },
      startedAt: routeWorkouts[1].startedAt,
      endedAt: routeWorkouts[1].endedAt,
      localStartDate: routeWorkouts[1].localStartDate,
    };
    const selectionBodies: Array<{ workoutIds?: string[] }> = [];
    authenticatedFetch((path, method, init) => {
      if (path.startsWith("/api/workouts?") && method === "GET")
        return json({ ...workoutsWithOne, items: [summaryWorkout] });
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body));
        selectionBodies.push(body);
        return json({
          id: "A".repeat(32),
          expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
          dataGeneration: 1,
          range: emptySummary.range,
          bounds: routeWorkouts[1].bounds,
          workouts: routeWorkouts,
          routeTileUrl: `/api/map-selections/${"A".repeat(32)}/routes/{z}/{x}/{y}`,
        });
      }
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(
      (await screen.findAllByRole("button", { name: "Actions for Hiking on 2026-08-01" }))[0],
    );
    await user.click(screen.getByRole("menuitem", { name: "Show on map" }));
    expect(
      await screen.findByRole("checkbox", { name: "Show Running from 8/05/2026" }),
    ).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Show Hiking from 8/01/2026" })).toBeChecked();
    expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
    expect(selectionBodies.at(-1)?.workoutIds).toBeUndefined();
    await user.click(screen.getByRole("link", { name: "Summary" }));
    await user.click(screen.getByRole("link", { name: "Map" }));
    expect(
      await screen.findByRole("checkbox", { name: "Show Running from 8/05/2026" }),
    ).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Show Hiking from 8/01/2026" })).toBeChecked();
    expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
  });

  test("preserves selected map routes across tabs and sorting until the date range changes", async () => {
    const routeWorkouts = [
      {
        id: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
        type: { id: "11111111111111111111111111111111", key: "running", name: "Running" },
        startedAt: "2026-08-05T12:00:00Z",
        endedAt: "2026-08-05T13:00:00Z",
        duration: "3600",
        localStartDate: "2026-08-05",
        partialRoute: false,
        bounds: {
          minimumLongitude: -105.3,
          minimumLatitude: 39.9,
          maximumLongitude: -105.2,
          maximumLatitude: 40.1,
        },
        distance: null,
        pace: null,
        calories: null,
        heartRate: null,
        elevationGain: null,
        coverageReadiness: {
          mapDataStatus: "ready",
          processingStatus: "current",
          resultStatus: "current",
        },
      },
      {
        id: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
        type: { id: "22222222222222222222222222222222", key: "hiking", name: "Hiking" },
        startedAt: "2026-08-01T12:00:00Z",
        endedAt: "2026-08-01T13:00:00Z",
        duration: "3600",
        localStartDate: "2026-08-01",
        partialRoute: false,
        bounds: {
          minimumLongitude: -105.2,
          minimumLatitude: 39.8,
          maximumLongitude: -105.1,
          maximumLatitude: 40,
        },
        distance: null,
        pace: null,
        calories: null,
        heartRate: null,
        elevationGain: null,
        coverageReadiness: {
          mapDataStatus: "ready",
          processingStatus: "current",
          resultStatus: "current",
        },
      },
    ];
    const summaryWorkout = {
      ...oneWorkout,
      id: routeWorkouts[1].id,
      type: { id: routeWorkouts[1].type.id, key: "hiking", displayName: "Hiking" },
      startedAt: routeWorkouts[1].startedAt,
      endedAt: routeWorkouts[1].endedAt,
      localStartDate: routeWorkouts[1].localStartDate,
    };
    const summaryWorkouts = { ...workoutsWithOne, items: [summaryWorkout] };
    const selectionBodies: Array<{ workoutIds?: string[]; dateRangeEnum?: string }> = [];
    authenticatedFetch((path, method, init) => {
      if (path.startsWith("/api/workouts?") && method === "GET") return json(summaryWorkouts);
      if (path === "/api/me/preferences" && method === "PATCH")
        return json({ ...preferences, dateRange: "last7Days" });
      if (path === "/api/map-selections" && method === "POST") {
        const body = JSON.parse(String(init?.body)) as {
          workoutIds?: string[];
          dateRangeEnum?: string;
        };
        selectionBodies.push(body);
        const selected =
          body.workoutIds === undefined
            ? routeWorkouts
            : routeWorkouts.filter((workout) => body.workoutIds?.includes(workout.id));
        const id = selectionBodies.length.toString(16).toUpperCase().padStart(32, "A");
        return json({
          id,
          expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
          dataGeneration: 1,
          range: { startDate: "2026-07-07", endDate: "2026-08-05" },
          bounds: null,
          workouts: selected,
          routeTileUrl: `/api/map-selections/${id}/routes/{z}/{x}/{y}`,
          coverageTileUrl: `/api/map-selections/${id}/coverage/{z}/{x}/{y}`,
        });
      }
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      return undefined as never;
    });
    renderApp("/map");
    const user = userEvent.setup();
    const hiking = await screen.findByRole("checkbox", { name: "Show Hiking from 8/01/2026" });
    await user.click(hiking);
    await waitFor(() => expect(selectionBodies.at(-1)?.workoutIds).toEqual([routeWorkouts[0].id]));
    await user.click(screen.getByRole("button", { name: "Coverage" }));
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    await user.click(screen.getByRole("link", { name: "Summary" }));
    await user.click(await screen.findByRole("button", { name: "Duration" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Duration" }).closest("th")).toHaveAttribute(
        "aria-sort",
        "descending",
      ),
    );
    await user.click(screen.getByRole("link", { name: "Map" }));

    expect(
      await screen.findByRole("checkbox", { name: "Show Running from 8/05/2026" }),
    ).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Show Hiking from 8/01/2026" })).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await waitFor(() => expect(selectionBodies.at(-1)?.workoutIds).toEqual([routeWorkouts[0].id]));

    await user.click(screen.getByRole("link", { name: "Summary" }));
    await user.click(
      (await screen.findAllByRole("button", { name: "Actions for Hiking on 2026-08-01" }))[0],
    );
    await user.click(screen.getByRole("menuitem", { name: "Show on map" }));
    expect(
      await screen.findByRole("checkbox", { name: "Show Running from 8/05/2026" }),
    ).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Show Hiking from 8/01/2026" })).toBeChecked();
    await waitFor(() =>
      expect(selectionBodies.at(-1)?.workoutIds).toEqual(
        routeWorkouts.map((workout) => workout.id),
      ),
    );
    expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );

    await user.click(screen.getByRole("link", { name: "Summary" }));
    await screen.findByRole("button", { name: "Duration" });
    await user.click(screen.getByRole("link", { name: "Map" }));
    expect(
      await screen.findByRole("checkbox", { name: "Show Hiking from 8/01/2026" }),
    ).toBeChecked();
    expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
    await waitFor(() =>
      expect(selectionBodies.at(-1)?.workoutIds).toEqual(
        routeWorkouts.map((workout) => workout.id),
      ),
    );

    await user.click(screen.getByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    await waitFor(() =>
      expect(selectionBodies.at(-1)).toMatchObject({ dateRangeEnum: "last7Days" }),
    );
    expect(selectionBodies.at(-1)?.workoutIds).toBeUndefined();
    expect(
      await screen.findByRole("checkbox", { name: "Show Hiking from 8/01/2026" }),
    ).toBeChecked();

    await user.click(screen.getByRole("link", { name: "Summary" }));
    await user.click(
      (await screen.findAllByRole("button", { name: "Actions for Hiking on 2026-08-01" }))[0],
    );
    await user.click(screen.getByRole("menuitem", { name: "Show on map" }));
    await waitFor(() =>
      expect(selectionBodies).toContainEqual({ dateRangeEnum: "last7Days", tz: "America/Denver" }),
    );
    await waitFor(() =>
      expect(selectionBodies.at(-1)).toMatchObject({ dateRangeEnum: "last7Days" }),
    );
    expect(selectionBodies.at(-1)?.workoutIds).toBeUndefined();
    expect(screen.getByRole("checkbox", { name: "Show Running from 8/05/2026" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Show Hiking from 8/01/2026" })).toBeChecked();
    expect(screen.getByRole("button", { name: /Hiking.*8\/01\/2026/ }).closest("li")).toHaveClass(
      "is-hovered",
      "is-focused",
    );
  });

  test.each(["none", "current", "stale"] as const)(
    "paged Show on map uses %s coverage readiness without changing scope or sort",
    async (resultStatus) => {
      const maps: Array<Record<string, any>> = [];
      class MockMap {
        layers = new Map<string, any>();
        sources = new Map<string, any>();
        handlers = new Map<string, () => void>();
        canvas = Object.assign(document.createElement("canvas"), { tabIndex: 0 });
        fitBounds = vi.fn();
        addControl = vi.fn();
        setFilter = vi.fn();
        jumpTo = vi.fn();
        moveLayer = vi.fn();
        remove = vi.fn();
        off = vi.fn();
        hasImage = () => false;
        addImage = vi.fn();
        isStyleLoaded = () => true;
        isMoving = () => false;
        areTilesLoaded = () => true;
        isSourceLoaded = () => true;
        queryRenderedFeatures = () => [];
        getCanvas = () => this.canvas;
        project = ([x, y]: number[]) => ({ x, y });
        getStyle = () => ({ layers: [] });
        setStyle = vi.fn(() => this.handlers.get("style.load")?.());
        addSource = (id: string, source: object) =>
          this.sources.set(id, {
            ...source,
            setTiles: vi.fn(),
            setData: vi.fn(() => Promise.resolve()),
          });
        getSource = (id: string) => this.sources.get(id);
        removeSource = (id: string) => this.sources.delete(id);
        addLayer = (layer: { id: string }) => this.layers.set(layer.id, layer);
        getLayer = (id: string) => this.layers.get(id);
        removeLayer = (id: string) => this.layers.delete(id);
        setPaintProperty = (id: string, key: string, value: unknown) => {
          const layer = this.layers.get(id);
          if (layer) layer.paint = { ...layer.paint, [key]: value };
        };
        setLayoutProperty = (id: string, key: string, value: unknown) => {
          const layer = this.layers.get(id);
          if (layer) layer.layout = { ...layer.layout, [key]: value };
        };
        on = (event: string, handler: (() => void) | string, callback?: () => void) => {
          const listener = typeof handler === "function" ? handler : callback!;
          this.handlers.set(event, listener);
          if (event === "style.load") listener();
        };
        constructor(options: { container: HTMLElement }) {
          options.container.append(this.canvas);
          maps.push(this);
        }
      }
      vi.spyOn(maplibregl, "Map").mockImplementation(function (options) {
        return new MockMap(options as { container: HTMLElement }) as never;
      });
      const target = {
        ...deletionMapWorkouts[0],
        id: "F".repeat(32),
        type: { id: "D".repeat(32), key: "cycling", name: "Cycling" },
        localStartDate: "2026-07-20",
        startedAt: "2026-07-20T12:00:00Z",
        endedAt: "2026-07-20T12:10:00Z",
        duration: "600",
        bounds: {
          minimumLongitude: -104,
          minimumLatitude: 38,
          maximumLongitude: -103,
          maximumLatitude: 39,
        },
        coverageReadiness: {
          mapDataStatus: resultStatus === "stale" ? "unavailable" : "ready",
          processingStatus: resultStatus === "current" ? "current" : "failed",
          resultStatus,
        },
      };
      const routeWorkouts = [...deletionMapWorkouts, target];
      const olderRows = Array.from({ length: 25 }, (_, index) => ({
        ...oneWorkout,
        id: (index + 1).toString(16).toUpperCase().padStart(32, "0"),
        duration: String(3600 + index),
      }));
      const bodies: Array<Record<string, any>> = [];
      const workoutQueries: URLSearchParams[] = [];
      const fetchMock = authenticatedFetch((path, method, init) => {
        if (path === "/api/config")
          return json({
            ...publicConfig,
            features: { coverageMatcherDiagnostics: true },
            baseMaps: {
              families: [
                {
                  id: "road",
                  label: "Road",
                  styles: {
                    light: "https://tiles.example.test/light.json",
                    dark: "https://tiles.example.test/dark.json",
                  },
                  attribution: { text: "Map", links: [] },
                  resourceOrigins: ["https://tiles.example.test"],
                },
              ],
              fallbackFamilyId: "road",
              workoutTypeMappings: [],
            },
          });
        if (path.startsWith("/api/workouts?") && method === "GET") {
          const query = new URL(path, "https://test").searchParams;
          workoutQueries.push(query);
          const page = Number(query.get("page"));
          return json({
            ...workoutsWithOne,
            pagination: { page, pageSize: 25, totalItems: 26, totalPages: 2 },
            items:
              page === 2
                ? [
                    {
                      ...oneWorkout,
                      ...target,
                      type: { ...target.type, displayName: target.type.name },
                    },
                  ]
                : olderRows,
          });
        }
        if (path === "/api/map-selections" && method === "POST") {
          const body = JSON.parse(String(init?.body));
          bodies.push(body);
          const id = bodies.length.toString(16).toUpperCase().padStart(32, "B");
          return json({
            id,
            expiresAt: session.expiresAt,
            dataGeneration: 1,
            range: emptySummary.range,
            bounds: deletionMapWorkouts[0].bounds,
            focusedWorkoutId: body.focusedWorkoutId ?? null,
            workouts: routeWorkouts.filter(
              ({ id }) => body.workoutIds === undefined || body.workoutIds.includes(id),
            ),
            routeTileUrl: `/api/map-selections/${id}/route-tiles/1/{z}/{x}/{y}.pbf`,
            coverageTileUrl: `/api/map-selections/${id}/coverage-tiles/1/{z}/{x}/{y}.pbf`,
          });
        }
        if (path.startsWith("/api/map-selections/") && method === "DELETE")
          return new Response(null, { status: 204 });
        if (path.endsWith("/route/points")) return json({ points: [] });
        return undefined as never;
      });
      renderApp("/map");
      const user = userEvent.setup();
      await user.click(await screen.findByRole("checkbox", { name: /Show Cycling/ }));
      const retainedIds = deletionMapWorkouts.map(({ id }) => id);
      await waitFor(() => expect(bodies.at(-1)?.workoutIds).toEqual(retainedIds));
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      const beforeMode = bodies.length;
      await user.click(screen.getByRole("button", { name: "Coverage" }));
      expect(bodies).toHaveLength(beforeMode);
      await user.click(screen.getByRole("link", { name: "Summary" }));
      await user.click(await screen.findByRole("button", { name: "Duration" }));
      await user.click(screen.getByRole("button", { name: "Next" }));
      await screen.findByText("Page 2 of 2", { selector: ".pagination span" });
      const beforeShow = bodies.length;
      await user.click(
        screen.getAllByRole("button", { name: "Actions for Cycling on 2026-07-20" })[0],
      );
      await user.click(screen.getByRole("menuitem", { name: "Show on map" }));
      const chosenMode = resultStatus === "none" ? "Routes" : "Coverage";
      await waitFor(() =>
        expect(screen.getByRole("button", { name: chosenMode })).toHaveAttribute(
          "aria-pressed",
          "true",
        ),
      );
      const map = maps.at(-1)!;
      await waitFor(() => expect(map.fitBounds).toHaveBeenCalledTimes(1));
      expect(map.fitBounds).toHaveBeenLastCalledWith(
        [
          [-104, 38],
          [-103, 39],
        ],
        { padding: 48, duration: 350 },
      );
      expect(map.getLayer("private-workout-routes").layout.visibility).toBe(
        resultStatus === "none" ? "visible" : "none",
      );
      for (const name of [/Show Running/, /Show Hiking/, /Show Cycling/])
        expect(screen.getByRole("checkbox", { name })).toBeChecked();
      expect(
        screen.getByRole("button", { name: /Cycling.*7\/20\/2026/ }).closest("li"),
      ).toHaveClass("is-focused");
      expect(location.pathname + location.search).toBe(`/map?workoutId=${target.id}`);
      expect(bodies.slice(beforeShow)).toEqual([
        {
          dateRangeEnum: "last30Days",
          tz: "America/Denver",
          workoutIds: [...retainedIds, target.id],
          focusedWorkoutId: target.id,
        },
      ]);
      await user.click(screen.getByRole("button", { name: "Coverage" }));
      expect(bodies).toHaveLength(beforeShow + 1);
      await user.click(screen.getByRole("link", { name: "Summary" }));
      await screen.findByText("Page 2 of 2", { selector: ".pagination span" });
      expect(screen.getByRole("button", { name: "Duration" }).closest("th")).toHaveAttribute(
        "aria-sort",
        "descending",
      );
      await user.click(screen.getByRole("link", { name: "Map" }));
      await waitFor(() => expect(screen.queryByText("Updating routes...")).not.toBeInTheDocument());
      expect(location.pathname + location.search).toBe("/map");
      expect(screen.getByRole("button", { name: "Coverage" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(bodies.at(-1)).toMatchObject({
        dateRangeEnum: "last30Days",
        workoutIds: [...retainedIds, target.id],
      });
      expect(workoutQueries.at(-1)?.get("sort")).toBe("duration:desc");
      expect(
        fetchMock.mock.calls
          .filter(([, init]) => init?.method === "POST")
          .every(([input]) => String(input) === "/api/map-selections"),
      ).toBe(true);
      expect(
        fetchMock.mock.calls
          .filter(([, init]) => init?.method === "DELETE")
          .every(([input]) => String(input).startsWith("/api/map-selections/")),
      ).toBe(true);
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PATCH")).toBe(false);
    },
  );

  test("preserves the Summary workout page across tabs and resets it for a new date range", async () => {
    authenticatedFetch((path, method) => {
      if (path.startsWith("/api/workouts?") && method === "GET") {
        const page = Number(new URL(path, "https://test").searchParams.get("page"));
        return json({
          ...workoutsWithOne,
          pagination: { page, pageSize: 25, totalItems: 51, totalPages: 3 },
        });
      }
      if (path === "/api/map-selections" && method === "POST")
        return json({
          id: "A".repeat(32),
          expiresAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
          dataGeneration: 1,
          range: emptySummary.range,
          bounds: null,
          workouts: [],
          routeTileUrl: `/api/map-selections/${"A".repeat(32)}/routes/{z}/{x}/{y}`,
        });
      if (path.startsWith("/api/map-selections/") && method === "DELETE")
        return new Response(null, { status: 204 });
      if (path === "/api/me/preferences" && method === "PATCH")
        return json({ ...preferences, dateRange: "last7Days" });
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    expect(
      await screen.findByText("Page 1 of 3", { selector: ".pagination span" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(
      await screen.findByText("Page 2 of 3", { selector: ".pagination span" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Map" }));
    await screen.findByRole("group", { name: "Map mode" });
    await user.click(screen.getByRole("link", { name: "Summary" }));
    expect(
      await screen.findByText("Page 2 of 3", { selector: ".pagination span" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Select date range" }));
    await user.click(screen.getByRole("menuitem", { name: "Last 7 days" }));
    expect(
      await screen.findByText("Page 1 of 3", { selector: ".pagination span" }),
    ).toBeInTheDocument();
  });

  test("loads a Data Sync deep link and the wordmark returns to Summary", async () => {
    const jobId = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD";
    authenticatedFetch((path) =>
      path === `/api/jobs/${jobId}`
        ? json({
            id: jobId,
            trigger: "manual",
            status: "succeeded",
            attempt: 0,
            progress: {
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
            },
            children: [],
            retriedByJobIds: [],
            cancelRequested: false,
            createdAt: "2026-08-06T12:00:00Z",
            updatedAt: "2026-08-06T12:00:00Z",
            terminalAt: "2026-08-06T12:00:00Z",
          })
        : (undefined as never),
    );
    renderApp(`/data-sync/jobs/${jobId}`);
    expect(await screen.findByText("Run detail")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Workouts Explorer" }));
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(location.pathname).toBe("/");
  });

  test("canonicalizes lowercase compact job IDs before API use", async () => {
    const upper = "ABCDEFABCDEFABCDEFABCDEFABCDEFAB";
    const lower = upper.toLowerCase();
    const paths: string[] = [];
    authenticatedFetch((path) => {
      if (path === `/api/jobs/${upper}`) {
        paths.push(path);
        return json({
          id: upper,
          trigger: "manual",
          status: "succeeded",
          attempt: 0,
          progress: {
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
          },
          children: [],
          retriedByJobIds: [],
          cancelRequested: false,
          createdAt: "2026-08-06T12:00:00Z",
          updatedAt: "2026-08-06T12:00:00Z",
        });
      }
      return undefined as never;
    });
    renderApp(`/data-sync/jobs/${lower}`);
    expect(await screen.findByText("Run detail")).toBeInTheDocument();
    expect(location.pathname).toBe(`/data-sync/jobs/${upper}`);
    expect(paths).toEqual([`/api/jobs/${upper}`]);
  });

  test("coalesces simultaneous protected 401s into one clean login transition", async () => {
    let sessionRequests = 0;
    let resolveSummary!: (response: Response) => void;
    let resolveWorkouts!: (response: Response) => void;
    const summaryResponse = new Promise<Response>((resolve) => {
      resolveSummary = resolve;
    });
    const workoutsResponse = new Promise<Response>((resolve) => {
      resolveWorkouts = resolve;
    });
    authenticatedFetch((path) => {
      if (path === "/api/session")
        return ++sessionRequests === 1 ? json(session) : json({ title: "Unauthorized" }, 401);
      if (path.startsWith("/api/summary?")) return summaryResponse;
      if (path.startsWith("/api/workouts?")) return workoutsResponse;
      return undefined as never;
    });
    renderApp();
    await screen.findByRole("button", { name: "Select date range" });
    const replace = vi.spyOn(history, "replaceState");
    const unauthorized = json(
      { title: "Unauthorized", detail: "private expiry detail", status: 401 },
      401,
    );
    resolveSummary(unauthorized);
    resolveWorkouts(
      json({ title: "Unauthorized", detail: "different private detail", status: 401 }, 401),
    );
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(location.pathname).toBe("/login");
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/");
    expect(replace).toHaveBeenCalledTimes(1);
    expect(sessionRequests).toBe(2);
    expect(screen.queryByText("Retry")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private expiry detail");
  });

  test("expires an advertised browser session deadline without waiting for another API request", async () => {
    let sessionRequests = 0;
    authenticatedFetch((path, method) =>
      path === "/api/session" && method === "GET"
        ? ++sessionRequests === 1
          ? json({ ...session, expiresAt: "2000-01-01T00:00:00Z" })
          : json({ title: "Unauthorized" }, 401)
        : (undefined as never),
    );
    renderApp("/data-sync");
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(location.pathname).toBe("/login");
    expect(new URLSearchParams(location.search).get("returnTo")).toBe("/data-sync");
    expect(sessionRequests).toBe(2);
  });

  test.each([200, 503, "network"])(
    "rechecks an elapsed session deadline after confirmation %s without another user action",
    async (status) => {
      vi.useFakeTimers();
      try {
        let checks = 0;
        const expiring = { ...session, expiresAt: new Date(Date.now() + 5000).toISOString() };
        authenticatedFetch((path, method) => {
          if (path !== "/api/session" || method !== "GET") return undefined as never;
          if (++checks === 1) return json(expiring);
          if (checks === 2) {
            if (status === "network") return Promise.reject(new TypeError("Network unavailable"));
            return json(status === 200 ? expiring : { title: "Unavailable" }, Number(status));
          }
          return json({ title: "Unauthorized" }, 401);
        });
        const app = renderApp("/data-sync?q=keep");
        for (let i = 0; i < 10 && !screen.queryByRole("heading", { name: "Start a sync" }); i++)
          await act(async () => {
            await vi.advanceTimersByTimeAsync(100);
          });
        expect(screen.getByRole("heading", { name: "Start a sync" })).toBeInTheDocument();
        app.client.setQueryData(["private-sentinel"], true);
        await act(async () => {
          await vi.advanceTimersByTimeAsync(Date.parse(expiring.expiresAt) - Date.now());
        });
        expect(checks).toBe(2);
        expect(screen.getByRole("heading", { name: "Start a sync" })).toBeInTheDocument();
        expect(app.client.getQueryData(["private-sentinel"])).toBe(true);
        await act(async () => {
          await vi.advanceTimersByTimeAsync(10000);
        });
        expect(checks).toBe(3);
        expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
        expect(app.client.getQueryData(["private-sentinel"])).toBeUndefined();
        expect(new URLSearchParams(location.search).get("returnTo")).toBe("/data-sync?q=keep");
        app.unmount();
      } finally {
        vi.useRealTimers();
      }
    },
  );

  test("cancels a deferred session refetch before a protected 401 can clear the shell", async () => {
    let sessionRequests = 0;
    let resolveRefetch!: (response: Response) => void;
    const deferredSession = new Promise<Response>((resolve) => {
      resolveRefetch = resolve;
    });
    authenticatedFetch((path, method) => {
      if (path === "/api/session" && method === "GET") {
        sessionRequests += 1;
        return sessionRequests === 1
          ? json(session)
          : sessionRequests === 2
            ? deferredSession
            : json({ title: "Unauthorized" }, 401);
      }
      if (path === "/api/me?expired=1") return json({ title: "Unauthorized" }, 401);
      return undefined as never;
    });
    const { client } = renderApp();
    await screen.findByRole("button", { name: "Select date range" });
    const refetch = client.refetchQueries({ queryKey: ["session"] });
    await waitFor(() => expect(sessionRequests).toBe(2));
    await expect(api("/api/me?expired=1")).rejects.toMatchObject({ status: 401 });
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    resolveRefetch(json(session));
    await refetch;
    await Promise.resolve();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Select date range" })).not.toBeInTheDocument();
  });

  test("re-arms expiration after login and handles a second protected 401", async () => {
    let loginPosted = false;
    let protectedFailures = 0;
    authenticatedFetch((path, method) => {
      if (path === "/api/session" && method === "GET" && protectedFailures)
        return json({ title: "Unauthorized" }, 401);
      if (path === "/api/session" && method === "POST") {
        loginPosted = true;
        return json(session, 201);
      }
      if (path.startsWith("/api/protected-expiry")) {
        protectedFailures += 1;
        return json({ title: "Unauthorized" }, 401);
      }
      return undefined as never;
    });
    renderApp();
    await screen.findByRole("button", { name: "Select date range" });
    await expect(api("/api/protected-expiry/one")).rejects.toMatchObject({ status: 401 });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "trailrunner");
    await user.type(screen.getByLabelText("Password"), "correct horse battery");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByRole("button", { name: "Select date range" });
    await expect(api("/api/protected-expiry/two")).rejects.toMatchObject({ status: 401 });
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(loginPosted).toBe(true);
    expect(protectedFailures).toBe(2);
  });

  test("saves profile and every preference with the session CSRF token", async () => {
    const mutations: Array<{ path: string; body: unknown; csrf: string | null }> = [];
    authenticatedFetch((path, method, init) => {
      if (method === "PATCH") {
        mutations.push({
          path,
          body: JSON.parse(String(init?.body)),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        });
        if (path === "/api/me") return json({ ...profile, fullName: "Avery Summit" });
        if (path === "/api/me/preferences")
          return json({ ...preferences, theme: "light", units: "metric", pageSize: 50 });
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    const menuButton = await screen.findByRole("button", { name: /Open account menu/ });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const dialog = await screen.findByRole("dialog", { name: "Preferences" });
    expect(screen.getByLabelText("Username", { selector: "input[readonly]" })).toHaveValue(
      "trailrunner",
    );
    expect(screen.getByLabelText("E-mail", { selector: "input[readonly]" })).toHaveValue(
      "avery@example.test",
    );
    for (const label of [
      "Theme",
      "Units",
      "Time zone",
      "First weekday",
      "Clock format",
      "Table rows per page",
    ]) {
      expect(screen.getByLabelText(label).parentElement).toHaveClass("select-control");
    }
    await user.clear(screen.getByLabelText("Full name"));
    await user.type(screen.getByLabelText("Full name"), "Avery Summit");
    await user.selectOptions(screen.getByLabelText("Theme"), "light");
    await user.selectOptions(screen.getByLabelText("Units"), "metric");
    expect(
      [...screen.getByLabelText("Table rows per page").querySelectorAll("option")].map(
        (option) => option.value,
      ),
    ).toEqual(["25", "50", "75", "100"]);
    await user.selectOptions(screen.getByLabelText("Table rows per page"), "50");
    await user.click(screen.getByRole("checkbox", { name: "Distance" }));
    await user.click(screen.getByRole("checkbox", { name: "Pace" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(mutations).toHaveLength(2);
    expect(mutations[0]).toEqual({
      path: "/api/me",
      body: { fullName: "Avery Summit" },
      csrf: session.csrfToken,
    });
    expect(mutations[1].csrf).toBe(session.csrfToken);
    expect(mutations[1].body).toMatchObject({
      theme: "light",
      units: "metric",
      timezone: "America/Denver",
      firstWeekday: "monday",
      clockFormat: "12h",
      workoutColumns: ["date", "type", "duration", "pace"],
      pageSize: 50,
    });
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  test("keeps preference header and actions outside the scrolling body", async () => {
    authenticatedFetch();
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const dialog = await screen.findByRole("dialog", { name: "Preferences" });
    const header = dialog.querySelector(".preferences-header");
    const body = dialog.querySelector(".preferences-body");
    const footer = dialog.querySelector(".dialog-actions");
    expect(header).toContainElement(
      screen.getByText("Set how your routes, dates, and profile appear."),
    );
    expect(header).toContainElement(screen.getByRole("button", { name: "Close Preferences" }));
    expect(body).toContainElement(screen.getByRole("group", { name: "Display" }));
    expect(body).not.toContainElement(screen.getByRole("button", { name: "Save" }));
    const cancel = screen.getByRole("button", { name: "Cancel" });
    const save = screen.getByRole("button", { name: "Save" });
    expect(footer).toContainElement(cancel);
    expect(footer).toContainElement(save);
    expect(cancel).toHaveClass("preferences-action");
    expect(save).toHaveClass("preferences-action");
  });

  test("persists every displayed default with the browser time zone before rendering a new account", async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      locale: "en-US",
      calendar: "gregory",
      numberingSystem: "latn",
      timeZone: "America/Los_Angeles",
    });
    let initialization: { body: unknown; csrf: string | null } | undefined;
    authenticatedFetch((path, method, init) => {
      if (path === "/api/me/preferences" && method === "GET")
        return json({ ...preferences, timezone: "UTC", initialized: false });
      if (path === "/api/me/preferences?initializeOnly=true" && method === "PATCH") {
        initialization = {
          body: JSON.parse(String(init?.body)),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        };
        return json({ ...preferences, timezone: "America/Los_Angeles", initialized: true });
      }
      return undefined as never;
    });
    renderApp();
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(initialization).toEqual({
      body: {
        theme: "dark",
        units: "imperial",
        timezone: "America/Los_Angeles",
        firstWeekday: "monday",
        clockFormat: "12h",
        workoutColumns: ["date", "type", "distance", "duration"],
        pageSize: 25,
        coverageDiagnosticsEnabled: false,
      },
      csrf: session.csrfToken,
    });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const timeZone = await screen.findByLabelText("Time zone");
    expect(timeZone).toHaveValue("America/Los_Angeles");
    expect(timeZone.tagName).toBe("SELECT");
    expect(screen.queryByLabelText("IANA timezone")).not.toBeInTheDocument();
  });

  test("preserves an intentionally initialized UTC time zone", async () => {
    const fetchMock = authenticatedFetch((path, method) => {
      if (path === "/api/me/preferences" && method === "GET")
        return json({ ...preferences, timezone: "UTC", initialized: true });
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    expect(await screen.findByLabelText("Time zone")).toHaveValue("UTC");
    expect(
      fetchMock.mock.calls.some(
        ([input, init]) => String(input).includes("initializeOnly") && init?.method === "PATCH",
      ),
    ).toBe(false);
  });

  test("orders time zones west-to-east and labels them with UTC offsets", async () => {
    authenticatedFetch();
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const options = [...(await screen.findByLabelText("Time zone")).querySelectorAll("option")];
    const values = options.map((option) => option.value);
    expect(values.indexOf("America/Los_Angeles")).toBeLessThan(values.indexOf("America/Denver"));
    expect(values.indexOf("America/Denver")).toBeLessThan(values.indexOf("America/Chicago"));
    expect(values.indexOf("America/Chicago")).toBeLessThan(values.indexOf("America/New_York"));
    expect(options.find((option) => option.value === "America/Los_Angeles")?.textContent).toMatch(
      /^America\/Los_Angeles \(UTC -\d{1,2}:\d{2}\)$/,
    );
  });

  test("limits page-size choices to the configured maximum", async () => {
    let patchAttempted = false;
    authenticatedFetch((path, method) => {
      if (path === "/api/config") return json({ ...publicConfig, pageSizeMaximum: 40 });
      if (method === "PATCH") {
        patchAttempted = true;
        return json(preferences);
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const pageSize = await screen.findByLabelText("Table rows per page");
    expect(pageSize.tagName).toBe("SELECT");
    expect([...pageSize.querySelectorAll("option")].map((option) => option.value)).toEqual(["25"]);
    expect(patchAttempted).toBe(false);
  });

  test("signout sends CSRF and only clears local session after server success", async () => {
    let deleteHeaders: Headers | undefined;
    let attempts = 0;
    authenticatedFetch((path, method, init) => {
      if (path === "/api/session" && method === "DELETE") {
        attempts += 1;
        deleteHeaders = new Headers(init?.headers);
        return attempts === 1
          ? json({ type: "about:blank", title: "Forbidden", status: 403, requestId: "test" }, 403)
          : new Response(null, { status: 204 });
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    const menuButton = await screen.findByRole("button", { name: /Open account menu/ });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("session is still active");
    expect(location.pathname).toBe("/");
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
    await waitFor(() => expect(location.pathname).toBe("/login"));
    expect(deleteHeaders?.get("X-CSRF-Token")).toBe(session.csrfToken);
  });

  test("treats a signout DELETE 401 as session expiration", async () => {
    let sessionRequests = 0;
    authenticatedFetch((path, method) =>
      path === "/api/session" && method === "GET"
        ? ++sessionRequests === 1
          ? json(session)
          : json({ title: "Unauthorized" }, 401)
        : path === "/api/session" && method === "DELETE"
          ? json({ title: "Unauthorized", detail: "expired signout detail" }, 401)
          : (undefined as never),
    );
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(location.pathname).toBe("/login");
    expect(document.body).not.toHaveTextContent("expired signout detail");
  });

  test("menu keyboard navigation opens About and Escape returns focus", async () => {
    authenticatedFetch();
    renderApp();
    const user = userEvent.setup();
    const trigger = await screen.findByRole("button", { name: /Open account menu/ });
    trigger.focus();
    await user.keyboard("{ArrowDown}");
    expect(await screen.findByRole("menu")).toBeVisible();
    await user.keyboard("abo{Enter}");
    expect(await screen.findByRole("dialog", { name: "About Workouts Explorer" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Close About Workouts Explorer" })).toBeVisible();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  test("keeps the account menu concise and uses the shared dropdown chevron", async () => {
    authenticatedFetch();
    renderApp();
    const user = userEvent.setup();
    const trigger = await screen.findByRole("button", { name: /Open account menu/ });
    expect(trigger.querySelector(".menu-chevron")).toHaveTextContent("v");
    await user.click(trigger);
    const menu = await screen.findByRole("menu");
    expect(menu).toHaveTextContent("@trailrunner");
    expect(menu).not.toHaveTextContent("Avery Stone");
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Preferences",
      "Switch to light theme",
      "About Workouts Explorer",
      "Sign out",
    ]);
  });

  test("theme bootstrap is synchronous and menu toggle persists through the API and local cache", async () => {
    expect(indexHtml.indexOf("workouts-explorer.theme")).toBeLessThan(
      indexHtml.indexOf("/src/main.tsx"),
    );
    expect(indexHtml).not.toContain("prefers-color-scheme");
    applyTheme("light");
    expect(localStorage.getItem("workouts-explorer.theme")).toBe("light");
    applyTheme("dark");
    let patch: { body: unknown; csrf: string | null } | undefined;
    authenticatedFetch((path, method, init) => {
      if (path === "/api/me/preferences" && method === "PATCH") {
        patch = {
          body: JSON.parse(String(init?.body)),
          csrf: new Headers(init?.headers).get("X-CSRF-Token"),
        };
        return json({ ...preferences, theme: "light" });
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Switch to light theme" }));
    await waitFor(() =>
      expect(patch).toEqual({ body: { theme: "light" }, csrf: session.csrfToken }),
    );
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("workouts-explorer.theme")).toBe("light");
  });

  test("avatar and source artifacts never expose a browser-direct Gravatar URL", async () => {
    authenticatedFetch();
    const view = renderApp();
    const avatar = await screen.findByRole("button", { name: /Open account menu/ });
    expect(avatar.querySelector("img")).toHaveAttribute("src", "/api/me/avatar?v=2");
    expect(view.container.innerHTML).not.toMatch(/gravatar/i);
    expect(JSON.stringify(profile)).not.toMatch(/gravatar/i);
  });
});

test("applyTheme updates DOM color scheme without needing a provider", () => {
  render(<span />, { wrapper });
  applyTheme("light");
  expect(document.documentElement.style.colorScheme).toBe("light");
});
