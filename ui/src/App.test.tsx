import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode } from "react";
import { describe, expect, test, vi } from "vitest";
import indexHtml from "../index.html?raw";
import { App } from "./App";
import { SESSION_EXPIRED_EVENT, api } from "./api";
import { applyTheme } from "./theme";

const session = {
  id: "11111111111111111111111111111111",
  expiresAt: "2026-08-03T15:00:00Z",
  csrfToken: "ccccccccccccccccccccccccccccccccccccccccccc",
  identity: { id: "22222222222222222222222222222222", username: "trailrunner", fullName: "Avery Stone", role: "user" },
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
  dateRange: "last30Days",
};
const emptySummary = { range: { startDate: "2026-07-07", endDate: "2026-08-05", timezone: "America/Denver" }, totals: { count: 0, duration: "0", distance: { value: "0", unit: "km" }, energy: { value: "0", unit: "kcal" } }, byType: [] };
const emptyWorkouts = { range: emptySummary.range, pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] };
const publicConfig = {
  productName: "Workouts Explorer",
  pollingIntervalSeconds: 30,
  mapFitPaddingPixels: 48,
  passwordMinimumLength: 12,
  pageSizeMaximum: 100,
};
const dataSync = {
  schedule: { enabled: true, sourceCount: 0, cadence: null, cadenceSeconds: 86400, staleDays: 3 },
  sources: [], notifications: [], notificationsTruncated: false,
};
const emptyJobs = { pagination: { page: 1, pageSize: 25, totalItems: 0, totalPages: 0 }, items: [] };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" } });
}

function routeFetch(handler?: (path: string, method: string, init?: RequestInit) => Response | Promise<Response>) {
  return vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const path = typeof input === "string" ? input : input instanceof URL ? input.pathname : input.url;
    const method = init?.method ?? "GET";
    if (handler) {
      const response = handler(path, method, init);
      if (response) return Promise.resolve(response);
    }
    if (path === "/api/config" && method === "GET") return Promise.resolve(json(publicConfig));
    if (path.startsWith("/api/summary?") && method === "GET") return Promise.resolve(json(emptySummary));
    if (path.startsWith("/api/workouts?") && method === "GET") return Promise.resolve(json(emptyWorkouts));
    if (path === "/api/session" && method === "GET") return Promise.resolve(json({ type: "about:blank", title: "Unauthorized", status: 401, requestId: "test" }, 401));
    throw new Error(`Unhandled fetch: ${method} ${path}`);
  });
}

function authenticatedFetch(handler?: (path: string, method: string, init?: RequestInit) => Response | Promise<Response>) {
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
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { ...render(<QueryClientProvider client={client}><App /></QueryClientProvider>), client };
}

function wrapper({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

describe("public authentication", () => {
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
    expect(await screen.findByLabelText("Username")).toHaveAttribute("placeholder", "Username or E-mail");
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
  });

  test("successful login sends the exact body and navigates into the restored shell", async () => {
    let posted: Record<string, string> | undefined;
    routeFetch((path, method, init) => {
      if (path === "/api/session" && method === "POST") { posted = JSON.parse(String(init?.body)); return json(session, 201); }
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

  test("failed login stays public, uses safe copy, and focuses the summary", async () => {
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    routeFetch((path, method) => path === "/api/session" && method === "POST" ? json({ type: "about:blank", title: "Unauthorized", detail: "secret backend detail", status: 401, requestId: "test" }, 401) : undefined as never);
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
      if (path === "/api/password-reset-requests" && method === "POST") { requestBody = JSON.parse(String(init?.body)); return json({ status: "accepted" }, 202); }
      return undefined as never;
    });
    renderApp("/forgot-password");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username or E-mail"), "possibly-missing@example.test");
    await user.click(screen.getByRole("button", { name: "Send reset link" }));
    expect(await screen.findByRole("status")).toHaveTextContent("If an account matches");
    expect(requestBody).toEqual({ username: "possibly-missing@example.test" });
  });

  test("validates an invitation, registers exact fields, and maps API field errors", async () => {
    const token = "iiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiii";
    let registrationBody: unknown;
    routeFetch((path, method, init) => {
      if (path === `/api/invitations/${token}`) return json({ email: "invitee@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST") {
        registrationBody = JSON.parse(String(init?.body));
        return json({ type: "about:blank", title: "Invalid request", status: 400, requestId: "test", errors: [{ field: "username", code: "duplicate", message: "Choose another username." }] }, 400);
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
    expect(await screen.findByText("Choose another username.")).toHaveAttribute("id", "username-error");
    expect(screen.getByLabelText("Username")).toHaveAccessibleDescription("Choose another username.");
    expect(registrationBody).toEqual({ token, username: "Avery", fullName: "Avery Stone", password: "long enough password", passwordConfirmation: "long enough password" });
  });

  test("successful invitation registration returns to sign in", async () => {
    const token = "sssssssssssssssssssssssssssssssssssssssssss";
    routeFetch((path, method) => {
      if (path === `/api/invitations/${token}`) return json({ email: "new@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST") return json({ ...profile, email: "new@example.test" }, 201);
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
      if (path === `/api/invitations/${token}`) return json({ email: "minimum@example.test", expiresAt: "2026-08-10T12:00:00Z" });
      if (path === "/api/registrations" && method === "POST") { registrationAttempted = true; return json(profile, 201); }
      return undefined as never;
    });
    renderApp(`/invitations/${token}`);
    const password = await screen.findByLabelText("Password");
    expect(password).toHaveAttribute("minlength", "16");
    expect(screen.getByText("16-128 characters. Spaces and symbols are welcome.")).toBeInTheDocument();
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
      if (path === "/api/password-resets" && method === "POST") { body = JSON.parse(String(init?.body)); return new Response(null, { status: 204 }); }
      return undefined as never;
    });
    renderApp(`/reset-password?token=${token}`);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("New password"), "a newly secure password");
    await user.type(screen.getByLabelText("Confirm new password"), "a newly secure password");
    await user.click(screen.getByRole("button", { name: "Reset password" }));
    expect(await screen.findByRole("heading", { name: "Your password is reset." })).toBeInTheDocument();
    expect(body).toEqual({ token, password: "a newly secure password", passwordConfirmation: "a newly secure password" });
  });

  test("uses a different configured password minimum for reset validation", async () => {
    const token = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv";
    let resetAttempted = false;
    routeFetch((path, method) => {
      if (path === "/api/config") return json({ ...publicConfig, passwordMinimumLength: 20 });
      if (path === "/api/password-resets" && method === "POST") { resetAttempted = true; return new Response(null, { status: 204 }); }
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
    routeFetch((path) => path === "/api/config" ? json({ type: "about:blank", title: "Database details", detail: "private failure", status: 503, requestId: "test" }, 503) : undefined as never);
    renderApp("/login");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Application settings could not be loaded");
    expect(alert).not.toHaveTextContent("private failure");
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });
});

describe("authenticated shell", () => {
  test("an active session visiting login returns to the authenticated shell", async () => {
    authenticatedFetch();
    renderApp("/login");
    expect(await screen.findByRole("button", { name: "Select date range" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });

  test("loads session first, restores server theme, and renders Summary", async () => {
    let resolveSession!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { resolveSession = resolve; });
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

  test("loads a Data Sync deep link and the wordmark returns to Summary", async () => {
    const jobId = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD";
    authenticatedFetch((path) => path === `/api/jobs/${jobId}` ? json({
      id: jobId, trigger: "manual", status: "succeeded", attempt: 0,
      progress: { current: 0, total: 0, filesDiscovered: 0, filesSkipped: 0, filesSucceeded: 0, filesFailed: 0, workoutsCreated: 0, workoutsUpdated: 0, workoutsUnchanged: 0, workoutsRejected: 0 },
      children: [], retriedByJobIds: [], cancelRequested: false,
      createdAt: "2026-08-06T12:00:00Z", updatedAt: "2026-08-06T12:00:00Z", terminalAt: "2026-08-06T12:00:00Z",
    }) : undefined as never);
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
        return json({ id: upper, trigger: "manual", status: "succeeded", attempt: 0, progress: { current: 0, total: 0, filesDiscovered: 0, filesSkipped: 0, filesSucceeded: 0, filesFailed: 0, workoutsCreated: 0, workoutsUpdated: 0, workoutsUnchanged: 0, workoutsRejected: 0 }, children: [], retriedByJobIds: [], cancelRequested: false, createdAt: "2026-08-06T12:00:00Z", updatedAt: "2026-08-06T12:00:00Z" });
      }
      return undefined as never;
    });
    renderApp(`/data-sync/jobs/${lower}`);
    expect(await screen.findByText("Run detail")).toBeInTheDocument();
    expect(location.pathname).toBe(`/data-sync/jobs/${upper}`);
    expect(paths).toEqual([`/api/jobs/${upper}`]);
  });

  test("coalesces simultaneous protected 401s into one clean login transition", async () => {
    let resolveSummary!: (response: Response) => void;
    let resolveWorkouts!: (response: Response) => void;
    const summaryResponse = new Promise<Response>((resolve) => { resolveSummary = resolve; });
    const workoutsResponse = new Promise<Response>((resolve) => { resolveWorkouts = resolve; });
    authenticatedFetch((path) => {
      if (path.startsWith("/api/summary?")) return summaryResponse;
      if (path.startsWith("/api/workouts?")) return workoutsResponse;
      return undefined as never;
    });
    renderApp();
    await screen.findByRole("button", { name: "Select date range" });
    const replace = vi.spyOn(history, "replaceState");
    const unauthorized = json({ title: "Unauthorized", detail: "private expiry detail", status: 401 }, 401);
    resolveSummary(unauthorized);
    resolveWorkouts(json({ title: "Unauthorized", detail: "different private detail", status: 401 }, 401));
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(location.pathname).toBe("/login");
    expect(replace).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Retry")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private expiry detail");
  });

  test("cancels a deferred session refetch before a protected 401 can clear the shell", async () => {
    let sessionRequests = 0;
    let resolveRefetch!: (response: Response) => void;
    const deferredSession = new Promise<Response>((resolve) => { resolveRefetch = resolve; });
    authenticatedFetch((path, method) => {
      if (path === "/api/session" && method === "GET") {
        sessionRequests += 1;
        return sessionRequests === 1 ? json(session) : deferredSession;
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
      if (path === "/api/session" && method === "POST") { loginPosted = true; return json(session, 201); }
      if (path.startsWith("/api/protected-expiry")) { protectedFailures += 1; return json({ title: "Unauthorized" }, 401); }
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
        mutations.push({ path, body: JSON.parse(String(init?.body)), csrf: new Headers(init?.headers).get("X-CSRF-Token") });
        if (path === "/api/me") return json({ ...profile, fullName: "Avery Summit" });
        if (path === "/api/me/preferences") return json({ ...preferences, theme: "light", units: "metric", pageSize: 50 });
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    const menuButton = await screen.findByRole("button", { name: /Open account menu/ });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const dialog = await screen.findByRole("dialog", { name: "Preferences" });
    expect(screen.getByLabelText("Username", { selector: "input[readonly]" })).toHaveValue("trailrunner");
    expect(screen.getByLabelText("E-mail", { selector: "input[readonly]" })).toHaveValue("avery@example.test");
    await user.clear(screen.getByLabelText("Full name"));
    await user.type(screen.getByLabelText("Full name"), "Avery Summit");
    await user.selectOptions(screen.getByLabelText("Theme"), "light");
    await user.selectOptions(screen.getByLabelText("Units"), "metric");
    expect([...screen.getByLabelText("Workouts per page").querySelectorAll("option")].map((option) => option.value)).toEqual(["25", "50", "75", "100"]);
    await user.selectOptions(screen.getByLabelText("Workouts per page"), "50");
    await user.click(screen.getByRole("checkbox", { name: "Distance" }));
    await user.click(screen.getByRole("checkbox", { name: "Pace" }));
    await user.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(mutations).toHaveLength(2);
    expect(mutations[0]).toEqual({ path: "/api/me", body: { fullName: "Avery Summit" }, csrf: session.csrfToken });
    expect(mutations[1].csrf).toBe(session.csrfToken);
    expect(mutations[1].body).toMatchObject({ theme: "light", units: "metric", timezone: "America/Denver", firstWeekday: "monday", clockFormat: "12h", workoutColumns: ["date", "type", "duration", "pace"], pageSize: 50 });
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
    expect(header).toContainElement(screen.getByText("Set how your routes, dates, and profile appear."));
    expect(header).toContainElement(screen.getByRole("button", { name: "Close Preferences" }));
    expect(body).toContainElement(screen.getByRole("group", { name: "Display" }));
    expect(body).not.toContainElement(screen.getByRole("button", { name: "Save preferences" }));
    expect(footer).toContainElement(screen.getByRole("button", { name: "Cancel" }));
    expect(footer).toContainElement(screen.getByRole("button", { name: "Save preferences" }));
  });

  test("uses the browser time zone while the stored preference is still UTC", async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      locale: "en-US",
      calendar: "gregory",
      numberingSystem: "latn",
      timeZone: "America/Los_Angeles",
    });
    authenticatedFetch((path, method) => {
      if (path === "/api/me/preferences" && method === "GET") return json({ ...preferences, timezone: "UTC" });
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const timeZone = await screen.findByLabelText("Time zone");
    expect(timeZone).toHaveValue("America/Los_Angeles");
    expect(timeZone.tagName).toBe("SELECT");
    expect(screen.queryByLabelText("IANA timezone")).not.toBeInTheDocument();
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
    expect(options.find((option) => option.value === "America/Los_Angeles")?.textContent).toMatch(/^America\/Los_Angeles \(UTC -\d{1,2}:\d{2}\)$/);
  });

  test("limits page-size choices to the configured maximum", async () => {
    let patchAttempted = false;
    authenticatedFetch((path, method) => {
      if (path === "/api/config") return json({ ...publicConfig, pageSizeMaximum: 40 });
      if (method === "PATCH") { patchAttempted = true; return json(preferences); }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Preferences" }));
    const pageSize = await screen.findByLabelText("Workouts per page");
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
        return attempts === 1 ? json({ type: "about:blank", title: "Forbidden", status: 403, requestId: "test" }, 403) : new Response(null, { status: 204 });
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
    authenticatedFetch((path, method) => path === "/api/session" && method === "DELETE"
      ? json({ title: "Unauthorized", detail: "expired signout detail" }, 401)
      : undefined as never);
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
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["Preferences", "Switch to light theme", "About Workouts Explorer", "Sign out"]);
  });

  test("theme bootstrap is synchronous and menu toggle persists through the API and local cache", async () => {
    expect(indexHtml.indexOf("workouts-explorer.theme")).toBeLessThan(indexHtml.indexOf("/src/main.tsx"));
    expect(indexHtml).not.toContain("prefers-color-scheme");
    applyTheme("light");
    expect(localStorage.getItem("workouts-explorer.theme")).toBe("light");
    applyTheme("dark");
    let patch: { body: unknown; csrf: string | null } | undefined;
    authenticatedFetch((path, method, init) => {
      if (path === "/api/me/preferences" && method === "PATCH") {
        patch = { body: JSON.parse(String(init?.body)), csrf: new Headers(init?.headers).get("X-CSRF-Token") };
        return json({ ...preferences, theme: "light" });
      }
      return undefined as never;
    });
    renderApp();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Open account menu/ }));
    await user.click(screen.getByRole("menuitem", { name: "Switch to light theme" }));
    await waitFor(() => expect(patch).toEqual({ body: { theme: "light" }, csrf: session.csrfToken }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("workouts-explorer.theme")).toBe("light");
  });

  test("avatar and source artifacts never expose a browser-direct Gravatar URL", async () => {
    authenticatedFetch();
    const view = renderApp();
    const avatar = await screen.findByRole("button", { name: /Open account menu/ });
    expect(avatar.querySelector("img")).toHaveAttribute("src", "/api/me/avatar");
    expect(view.container.innerHTML).not.toMatch(/gravatar/i);
    expect(JSON.stringify(profile)).not.toMatch(/gravatar/i);
  });
});

test("applyTheme updates DOM color scheme without needing a provider", () => {
  render(<span />, { wrapper });
  applyTheme("light");
  expect(document.documentElement.style.colorScheme).toBe("light");
});
