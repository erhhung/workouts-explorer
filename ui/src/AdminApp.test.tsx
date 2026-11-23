import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { App } from "./App";
import { AdminUsers, validInvitationEmail } from "./AdminUsers";
import { ADMIN_SESSION_EXPIRED_EVENT, SESSION_EXPIRED_EVENT, api } from "./api";

const admin = {
  id: "A".repeat(32),
  expiresAt: new Date(Date.now() + 3600000).toISOString(),
  csrfToken: "admin-csrf",
  identity: {
    id: "B".repeat(32),
    username: "admin",
    fullName: "Administrator",
    role: "administrator",
  },
};
const owner = {
  ...admin,
  id: "C".repeat(32),
  identity: { ...admin.identity, id: "D".repeat(32), username: "owner", role: "user" },
};
const config = {
  productName: "Workouts Explorer",
  pollingIntervalSeconds: 30,
  mapFitPaddingPixels: 48,
  passwordMinimumLength: 12,
  pageSizeMaximum: 100,
  features: { coverageMatcherDiagnostics: false },
  baseMaps: { families: [], fallbackFamilyId: "", workoutTypeMappings: [] },
};
const users = {
  items: [
    {
      id: "C".repeat(32),
      kind: "user",
      accountId: "D".repeat(32),
      username: "trailrunner",
      fullName: "Avery Stone",
      email: "avery@example.test",
      registeredAt: "2025-10-16T12:00:00Z",
      activeSessionExpiresAt: null,
      state: "active",
    },
  ],
  pagination: { page: 1, pageSize: 25, totalItems: 1, totalPages: 1 },
};
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function setup(
  path = "/admin",
  handler?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined,
) {
  window.history.replaceState(null, "", path);
  const requests = vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const target = String(input);
    const overridden = handler?.(target, init);
    if (overridden) return Promise.resolve(overridden);
    if (target === "/api/config") return Promise.resolve(json(config));
    if (target === "/api/admin/session") return Promise.resolve(json(admin));
    if (target === "/api/admin/directory") return Promise.resolve(json(users));
    if (target === "/api/admin/config")
      return Promise.resolve(
        json({
          passwordMinimumLength: 12,
          pageSizeMaximum: 100,
          sessionLifetimeSeconds: 21600,
          pollingIntervalSeconds: 30,
          mapFamilies: ["Outdoor", "Streets"],
        }),
      );
    if (target === "/api/session") return Promise.resolve(json({ title: "Unauthorized" }, 401));
    throw Error(`Unexpected request ${target}`);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
  );
  return { requests, client };
}

describe("administrator workspace", () => {
  test("OSM processing stores the monitoring default and retains it across tabs", async () => {
    const user = userEvent.setup();
    let settings = { monitorDatabaseStorage: true, revision: 1, updatedAt: "2026-11-21T00:00:00Z" };
    const { requests } = setup("/admin/settings/osm-processing", (path, init) => {
      if (path !== "/api/admin/osm/processing-settings") return;
      if (init?.method === "PATCH") {
        expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(admin.csrfToken);
        expect(JSON.parse(String(init.body))).toEqual({
          monitorDatabaseStorage: false,
          revision: 1,
        });
        settings = { ...settings, monitorDatabaseStorage: false, revision: 2 };
        return json(settings);
      }
      return json(settings);
    });
    const checkbox = await screen.findByRole("checkbox", {
      name: "Monitor available database storage",
    });
    await waitFor(() => expect(checkbox).toBeEnabled());
    expect(checkbox).toBeChecked();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.click(checkbox);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("OSM processing settings saved.");
    expect(checkbox).not.toBeChecked();
    const nav = screen.getByRole("navigation", { name: "Administration" });
    await user.click(within(nav).getByRole("link", { name: "Accounts" }));
    await user.click(within(nav).getByRole("link", { name: "Settings" }));
    expect(screen.getByRole("heading", { level: 1, name: "OSM Processing" })).toBeInTheDocument();
    expect(
      screen.getByRole("checkbox", { name: "Monitor available database storage" }),
    ).not.toBeChecked();
    expect(requests.mock.calls.filter(([, init]) => init?.method === "PATCH")).toHaveLength(1);
  });
  test("OSM settings conflict reloads the revision without silently discarding the requested value", async () => {
    const user = userEvent.setup();
    let revision = 1,
      attempts = 0;
    setup("/admin/settings/osm-processing", (path, init) => {
      if (path !== "/api/admin/osm/processing-settings") return;
      if (init?.method === "PATCH") {
        attempts++;
        if (attempts === 1) {
          revision = 2;
          return json({ title: "Conflict" }, 409);
        }
        expect(JSON.parse(String(init.body))).toEqual({
          monitorDatabaseStorage: false,
          revision: 2,
        });
        return json({
          monitorDatabaseStorage: false,
          revision: 3,
          updatedAt: "2026-11-21T00:00:00Z",
        });
      }
      return json({ monitorDatabaseStorage: true, revision, updatedAt: "2026-11-21T00:00:00Z" });
    });
    const checkbox = await screen.findByRole("checkbox", {
      name: "Monitor available database storage",
    });
    await waitFor(() => expect(checkbox).toBeEnabled());
    await user.click(checkbox);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Settings changed elsewhere. Review the latest value and save again.");
    expect(checkbox).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("OSM processing settings saved.");
  });
  test("OSM settings failed saves preserve the draft and allow retry", async () => {
    const user = userEvent.setup();
    setup("/admin/settings/osm-processing", (path, init) =>
      path === "/api/admin/osm/processing-settings"
        ? init?.method === "PATCH"
          ? json({ title: "Unavailable" }, 503)
          : json({ monitorDatabaseStorage: true, revision: 1, updatedAt: "2026-11-21T00:00:00Z" })
        : undefined,
    );
    const checkbox = await screen.findByRole("checkbox", {
      name: "Monitor available database storage",
    });
    await waitFor(() => expect(checkbox).toBeEnabled());
    await user.click(checkbox);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Could not save OSM processing settings. Try again.");
    expect(checkbox).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });
  test("isolates private queries and retains left-nav selection across tabs", async () => {
    const user = userEvent.setup();
    const { requests } = setup("/admin/accounts/deletions");
    expect(await screen.findByRole("cell", { name: "trailrunner" })).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/admin/accounts/users"));
    expect(
      within(screen.getByRole("navigation", { name: "Accounts sections" }))
        .getAllByRole("button")
        .map((button) => button.textContent),
    ).toEqual(["Users"]);
    expect(screen.queryByText("Account deletions")).not.toBeInTheDocument();
    expect(screen.queryByText(/This section is planned/)).not.toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Administration" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Accounts", "OSM Maps", "Operations", "Settings"]);
    await user.click(within(nav).getByRole("link", { name: "Settings" }));
    await user.click(screen.getByRole("button", { name: "Effective configuration" }));
    expect(await screen.findByText("Outdoor, Streets")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await user.click(within(nav).getByRole("link", { name: "Accounts" }));
    expect(await screen.findByRole("cell", { name: "trailrunner" })).toBeInTheDocument();
    await user.click(within(nav).getByRole("link", { name: "Settings" }));
    expect(
      screen.getByRole("heading", { level: 1, name: "Effective configuration" }),
    ).toBeInTheDocument();
    expect(
      requests.mock.calls.every(
        ([url]) => String(url) === "/api/config" || String(url).startsWith("/api/admin/"),
      ),
    ).toBe(true);
  });

  test("shared login routes authenticated admin away from an owner return path", async () => {
    const user = userEvent.setup();
    setup("/login?returnTo=%2Fmap", (path, init) =>
      path === "/api/session" && init?.method === "POST" ? json(admin, 201) : undefined,
    );
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "synthetic admin password");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("navigation", { name: "Administration" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/admin");
  });

  test("owner credentials entered on an admin deep link cannot mount administration", async () => {
    const user = userEvent.setup();
    const { requests } = setup("/login?returnTo=%2Fadmin", (path, init) => {
      if (path === "/api/admin/session") return json({ title: "Unauthorized" }, 401);
      if (path === "/api/session" && init?.method === "POST") return json(owner, 201);
      if (path === "/api/session") return json(owner);
      if (path.startsWith("/api/me")) return json({ title: "Unavailable" }, 503);
      return undefined;
    });
    await user.type(await screen.findByLabelText("Username"), "owner");
    await user.type(screen.getByLabelText("Password"), "synthetic owner password");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(window.location.pathname).toBe("/"));
    expect(screen.queryByRole("navigation", { name: "Administration" })).not.toBeInTheDocument();
    expect(
      requests.mock.calls.some(([url]) => String(url).startsWith("/api/admin/directory")),
    ).toBe(false);
  });

  test("invites use the administrator CSRF and distinguish unconfirmed delivery", async () => {
    const user = userEvent.setup();
    let fail = false;
    let invited = false;
    const { requests } = setup("/admin/accounts/users", (path, init) => {
      if (path === "/api/admin/directory")
        return json({
          items: [
            ...users.items,
            ...(invited
              ? [
                  {
                    id: "E".repeat(32),
                    kind: "invitation",
                    username: null,
                    fullName: null,
                    email: "invitee@example.test",
                    state: "invited",
                  },
                ]
              : []),
          ],
        });
      if (path === "/api/admin/invitations" && init?.method === "POST") {
        if (fail) return json({ title: "Unavailable" }, 503);
        invited = true;
        return json({ id: "E".repeat(32), email: "invitee@example.test" }, 201);
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "Invite a user" }));
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    await user.type(screen.getByLabelText("Enter e-mail address"), "invalid");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    await user.clear(screen.getByLabelText("Enter e-mail address"));
    await user.type(screen.getByLabelText("Enter e-mail address"), "invitee@example.test");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("cell", { name: "invitee@example.test" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Invited" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const call = requests.mock.calls.find(([url]) => url === "/api/admin/invitations")!;
    expect(new Headers(call[1]?.headers).get("X-CSRF-Token")).toBe("admin-csrf");
    fail = true;
    await user.click(screen.getByRole("button", { name: "Invite a user" }));
    await user.type(screen.getByLabelText("Enter e-mail address"), "another@example.test");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Invitation email could not be sent",
    );
  });

  test("signout clears only administrator state and uses its session endpoint", async () => {
    const user = userEvent.setup();
    let signedOut = false;
    const { requests } = setup("/admin", (path, init) => {
      if (path === "/api/admin/session" && init?.method === "DELETE") {
        signedOut = true;
        return new Response(null, { status: 204 });
      }
      if (path === "/api/admin/session" && signedOut) return json({ title: "Unauthorized" }, 401);
      return undefined;
    });
    await screen.findByRole("cell", { name: "trailrunner" });
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("heading", { name: "Find your way home." })).toBeInTheDocument();
    expect(
      requests.mock.calls.some(
        ([url, init]) => url === "/api/session" && init?.method === "DELETE",
      ),
    ).toBe(false);
  });

  test("confirms admin expiry independently and ignores owner expiry events", async () => {
    let expired = false;
    setup("/admin", (path) =>
      path === "/api/admin/session" && expired ? json({ title: "Unauthorized" }, 401) : undefined,
    );
    await screen.findByRole("cell", { name: "trailrunner" });
    act(() => window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT)));
    expect(screen.getByRole("navigation", { name: "Administration" })).toBeInTheDocument();
    expired = true;
    act(() => window.dispatchEvent(new Event(ADMIN_SESSION_EXPIRED_EVENT)));
    expect(await screen.findByRole("heading", { name: "Find your way home." })).toBeInTheDocument();
    expect(screen.queryByText("avery@example.test")).not.toBeInTheDocument();
  });

  test("admin API 401 events do not expire owners or recursively confirm sessions", async () => {
    const ownerExpired = vi.fn(),
      adminExpired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, ownerExpired);
    window.addEventListener(ADMIN_SESSION_EXPIRED_EVENT, adminExpired);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ title: "Unauthorized" }, 401));
    await expect(api("/api/admin/users")).rejects.toBeInstanceOf(Error);
    expect(adminExpired).toHaveBeenCalledOnce();
    expect(ownerExpired).not.toHaveBeenCalled();
    await expect(api("/api/admin/session")).rejects.toBeInstanceOf(Error);
    expect(adminExpired).toHaveBeenCalledOnce();
    window.removeEventListener(SESSION_EXPIRED_EVENT, ownerExpired);
    window.removeEventListener(ADMIN_SESSION_EXPIRED_EVENT, adminExpired);
  });

  test("sorts the unpaged Users table and shows shared status/actions", async () => {
    const user = userEvent.setup();
    setup("/admin", (path) =>
      path === "/api/admin/directory"
        ? json({
            items: [
              ...users.items,
              {
                ...users.items[0],
                id: "E".repeat(32),
                username: "alpine",
                state: "disabled",
                email: "alpine@example.test",
              },
              {
                id: "F".repeat(32),
                kind: "invitation",
                username: null,
                fullName: null,
                email: "invited@example.test",
                state: "invited",
              },
            ],
          })
        : undefined,
    );
    await screen.findByRole("cell", { name: "alpine" });
    const table = screen.getByRole("table");
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("alpine");
    await user.click(screen.getByRole("button", { name: "Username" }));
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("trailrunner");
    expect(screen.getByRole("cell", { name: "Disabled" }).firstChild).toHaveClass(
      "admin-user-status--disabled",
    );
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "User pages" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Invitations" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Actions for alpine" }));
    expect(screen.getByRole("menuitem", { name: "Enable user" })).not.toHaveAttribute(
      "data-disabled",
    );
    expect(screen.getByRole("menuitem", { name: "Delete user" })).not.toHaveAttribute(
      "data-disabled",
    );
  });

  test("admin 1-4 shortcuts respect dialogs, editing, modifiers and native menus", async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByRole("cell", { name: "trailrunner" });
    for (const [key, label] of [
      ["2", "OSM Maps"],
      ["3", "Operations"],
      ["4", "Settings"],
      ["1", "Accounts"],
    ]) {
      fireEvent.keyDown(document.body, { key });
      expect(
        within(screen.getByRole("navigation", { name: "Administration" })).getByRole("link", {
          name: label,
        }),
      ).toHaveAttribute("aria-current", "page");
      expect(document.activeElement).toHaveAttribute("data-app-content");
      if (label === "OSM Maps") {
        const search = screen.getByRole("searchbox");
        search.focus();
        fireEvent.keyDown(search, { key: "3" });
        expect(window.location.pathname).toContain("/osm/");
        screen.getByRole("heading", { name: "Regions" }).focus();
      }
    }
    fireEvent.keyDown(document.body, { key: "4", ctrlKey: true });
    expect(window.location.pathname).toContain("/accounts/");
    await user.click(screen.getByRole("button", { name: "Invite a user" }));
    fireEvent.keyDown(screen.getByLabelText("Enter e-mail address"), { key: "4" });
    fireEvent.keyDown(screen.getByRole("button", { name: "Cancel" }), { key: "2" });
    expect(window.location.pathname).toContain("/accounts/");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Actions for trailrunner" }));
    fireEvent.keyDown(document.body, { key: "4" });
    expect(window.location.pathname).toContain("/accounts/");
  });

  test("Delete user confirms the target, uses admin CSRF and retains the row as Deleting", async () => {
    const user = userEvent.setup();
    let deleting = false;
    let release!: () => void;
    const { requests } = setup("/admin", (path, init) => {
      if (path === "/api/admin/directory")
        return json({ items: [{ ...users.items[0], state: deleting ? "deleting" : "active" }] });
      if (init?.method === "DELETE" && path.startsWith("/api/admin/users/"))
        return new Promise<Response>((resolve) => {
          release = () => {
            deleting = true;
            resolve(json({ jobId: "D".repeat(32), phase: "draining", rowsPurged: 0 }, 202));
          };
        });
      return undefined;
    });
    const cell = await screen.findByRole("cell", { name: "trailrunner" }),
      row = cell.closest("tr");
    await user.click(screen.getByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete user" }));
    const dialog = screen.getByRole("dialog", { name: "Delete user?" });
    expect(dialog).toHaveTextContent("@trailrunner");
    expect(dialog).toHaveTextContent("This cannot be undone");
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    const confirmation = within(dialog).getByRole("textbox", { name: "Type DELETE to confirm." });
    expect(confirmation).toHaveAttribute("placeholder", "DELETE");
    const yes = within(dialog).getByRole("button", { name: "Yes" });
    expect(yes).toBeDisabled();
    for (const value of ["delete", "Delete", " DELETE "]) {
      await user.clear(confirmation);
      await user.type(confirmation, value);
      expect(yes).toBeDisabled();
    }
    await user.clear(confirmation);
    await user.type(confirmation, "DELETE");
    expect(yes).toBeEnabled();
    await user.click(within(dialog).getByRole("button", { name: "Yes" }));
    await user.click(within(dialog).getByRole("button", { name: "Yes" }));
    expect(requests.mock.calls.filter(([, init]) => init?.method === "DELETE")).toHaveLength(1);
    await act(async () => release());
    await screen.findByText("Deleting", { selector: ".admin-user-status" });
    expect(screen.getByRole("cell", { name: "trailrunner" }).closest("tr")).toBe(row);
    expect(screen.getByText("Deleting", { selector: ".admin-user-status" })).toHaveClass(
      "admin-user-status--deleting",
    );
    const [, init] = requests.mock.calls.find(([, init]) => init?.method === "DELETE")!;
    expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe(admin.csrfToken);
  });

  test("canceling a typed user deletion clears confirmation before reopening", async () => {
    const user = userEvent.setup();
    const { requests } = setup("/admin");
    const open = async () => {
      await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
      await user.click(screen.getByRole("menuitem", { name: "Delete user" }));
      return screen.getByRole("dialog", { name: "Delete user?" });
    };
    let dialog = await open();
    await user.type(
      within(dialog).getByRole("textbox", { name: "Type DELETE to confirm." }),
      "DELETE",
    );
    expect(within(dialog).getByRole("button", { name: "Yes" })).toBeEnabled();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    dialog = await open();
    expect(within(dialog).getByRole("textbox", { name: "Type DELETE to confirm." })).toHaveValue(
      "",
    );
    expect(within(dialog).getByRole("button", { name: "Yes" })).toBeDisabled();
    expect(requests.mock.calls.filter(([, init]) => init?.method === "DELETE")).toHaveLength(0);
  });

  test("compatible deletion polls keep rows in place and remove them only after an authoritative refresh", async () => {
    let release!: (response: Response) => void;
    let refreshing = false;
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["admin-session"], admin);
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      refreshing
        ? new Promise<Response>((resolve) => {
            release = resolve;
          })
        : json({ items: [{ ...users.items[0], state: "deleting" }] }),
    );
    render(
      <QueryClientProvider client={client}>
        <AdminUsers
          session={{ ...admin, identity: { ...admin.identity, role: "administrator" } }}
          polling={30}
          headingId="users-heading"
        />
      </QueryClientProvider>,
    );
    const cell = await screen.findByRole("cell", { name: "trailrunner" }),
      row = cell.closest("tr");
    refreshing = true;
    let poll!: Promise<void>;
    act(() => {
      poll = client.refetchQueries({ queryKey: ["admin-directory"], exact: true });
    });
    await waitFor(() => expect(release).toBeTypeOf("function"));
    expect(screen.getByRole("cell", { name: "trailrunner" }).closest("tr")).toBe(row);
    expect(screen.queryByText("Loading users...")).not.toBeInTheDocument();
    await act(async () => {
      release(json({ items: [] }));
      await poll;
    });
    await waitFor(() =>
      expect(screen.queryByRole("cell", { name: "trailrunner" })).not.toBeInTheDocument(),
    );
    expect(screen.getByText("No users yet.")).toBeInTheDocument();
  });

  test("deletion failures keep the selected account and inactive late acknowledgements do not reopen dialogs", async () => {
    const user = userEvent.setup();
    let release!: (response: Response) => void;
    setup("/admin", (path, init) =>
      init?.method === "DELETE" && path.startsWith("/api/admin/users/")
        ? new Promise<Response>((resolve) => {
            release = resolve;
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete user" }));
    await user.type(screen.getByRole("textbox", { name: "Type DELETE to confirm." }), "DELETE");
    await user.click(screen.getByRole("button", { name: "Yes" }));
    await act(async () =>
      release(json({ title: "Unavailable", detail: "private diagnostics" }, 503)),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be started");
    expect(screen.getByRole("alert")).not.toHaveTextContent("private diagnostics");
    await user.click(screen.getByRole("button", { name: "Yes" }));
    act(() => {
      history.pushState(null, "", "/admin/osm/regions");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await act(async () =>
      release(json({ jobId: "D".repeat(32), phase: "draining", rowsPurged: 0 }, 202)),
    );
    expect(location.pathname).toBe("/admin/osm/regions");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("retries deletion lock contention in the same pending confirmation", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    let release!: (response: Response) => void;
    setup("/admin", (path, init) => {
      if (path.startsWith("/api/admin/users/") && init?.method === "DELETE") {
        attempts++;
        if (attempts === 1)
          return json(
            { type: "https://workouts-explorer.invalid/problems/account-deletion-busy" },
            503,
          );
        return new Promise<Response>((resolve) => {
          release = resolve;
        });
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete user" }));
    const dialog = screen.getByRole("dialog", { name: "Delete user?" });
    await user.type(
      within(dialog).getByRole("textbox", { name: "Type DELETE to confirm." }),
      "DELETE",
    );
    await user.click(within(dialog).getByRole("button", { name: "Yes" }));
    expect(await within(dialog).findByRole("status")).toHaveTextContent("retry automatically");
    expect(within(dialog).getByRole("button", { name: "Yes" })).toBeDisabled();
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => expect(attempts).toBe(2), { timeout: 3000 });
    await act(async () =>
      release(json({ jobId: "D".repeat(32), phase: "draining", rowsPurged: 0 }, 202)),
    );
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(attempts).toBe(2);
  });

  test("stops a scheduled deletion retry after leaving administration", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    setup("/admin", (path, init) => {
      if (path.startsWith("/api/admin/users/") && init?.method === "DELETE") {
        attempts++;
        return json(
          { type: "https://workouts-explorer.invalid/problems/account-deletion-busy" },
          503,
        );
      }
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete user" }));
    await user.type(screen.getByRole("textbox", { name: "Type DELETE to confirm." }), "DELETE");
    await user.click(screen.getByRole("button", { name: "Yes" }));
    await screen.findByText(/Deletion will retry automatically/);
    act(() => {
      history.pushState(null, "", "/admin/osm/regions");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await act(async () => new Promise((resolve) => setTimeout(resolve, 1100)));
    expect(attempts).toBe(1);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("disables and enables the selected user with confirmation and administrator CSRF", async () => {
    const user = userEvent.setup();
    let enabled = true;
    let release!: () => void;
    const { requests } = setup("/admin", (path, init) => {
      if (path === "/api/admin/directory")
        return json({ items: [{ ...users.items[0], state: enabled ? "active" : "disabled" }] });
      if (path.endsWith("/access") && init?.method === "PUT")
        return new Promise<Response>((resolve) => {
          release = () => {
            enabled = JSON.parse(String(init.body)).enabled;
            resolve(json({ enabled }));
          };
        });
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Disable user" }));
    let dialog = screen.getByRole("dialog", { name: "Disable user" });
    expect(dialog).toHaveTextContent("@trailrunner");
    expect(dialog).toHaveTextContent("data and settings are retained");
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await user.click(within(dialog).getByRole("button", { name: "Disable" }));
    await user.click(within(dialog).getByRole("button", { name: "Disable" }));
    expect(requests.mock.calls.filter(([path]) => String(path).endsWith("/access"))).toHaveLength(
      1,
    );
    expect(screen.getByText("Active", { selector: ".admin-user-status" })).toBeInTheDocument();
    await act(async () => release());
    expect(await screen.findByRole("cell", { name: "Disabled" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Actions for trailrunner" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Enable user" }));
    dialog = screen.getByRole("dialog", { name: "Enable user" });
    expect(dialog).toHaveTextContent(
      "Enable @trailrunner? The user will be able to sign in again and automatic sync will resume on schedule. Revoked sessions and canceled jobs will not be restored.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Enable" }));
    await act(async () => release());
    expect(await screen.findByRole("cell", { name: "Active" })).toBeInTheDocument();
    const calls = requests.mock.calls.filter(([path]) => String(path).endsWith("/access"));
    expect(calls.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([
      { enabled: false },
      { enabled: true },
    ]);
    for (const [, init] of calls)
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe(admin.csrfToken);
  });

  test("access conflicts preserve the row and a late command cannot reopen an inactive dialog", async () => {
    const user = userEvent.setup();
    let release!: (response: Response) => void;
    setup("/admin", (path) =>
      path.endsWith("/access")
        ? new Promise<Response>((resolve) => {
            release = resolve;
          })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await user.click(screen.getByRole("menuitem", { name: "Disable user" }));
    await user.click(screen.getByRole("button", { name: "Disable" }));
    await act(async () => release(json({ title: "Conflict" }, 409)));
    expect(await screen.findByRole("alert")).toHaveTextContent("account is being deleted");
    expect(screen.getByText("Active", { selector: ".admin-user-status" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Disable" }));
    act(() => {
      history.pushState(null, "", "/admin/osm/regions");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await act(async () => release(json({ enabled: false })));
    expect(location.pathname).toBe("/admin/osm/regions");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test.each([
    ["active", null],
    ["active", "2025-01-01T00:00:00Z"],
    ["disabled", "2099-01-01T00:00:00Z"],
    ["deleting", "2099-01-01T00:00:00Z"],
  ])("hides Revoke sessions for %s with expiry %s", async (state, activeSessionExpiresAt) => {
    const user = userEvent.setup();
    const { requests } = setup("/admin", (path) =>
      path === "/api/admin/directory"
        ? json({ items: [{ ...users.items[0], state, activeSessionExpiresAt }] })
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "Actions for trailrunner" }));
    await waitFor(() =>
      expect(requests.mock.calls.filter(([path]) => path === "/api/admin/directory")).toHaveLength(
        2,
      ),
    );
    expect(screen.queryByRole("menuitem", { name: "Revoke sessions" })).not.toBeInTheDocument();
  });

  test("refreshes session availability on menu open and revokes with administrator CSRF", async () => {
    const user = userEvent.setup();
    let activeSessionExpiresAt: string | null = null;
    let release!: () => void;
    const { requests } = setup("/admin", (path, init) => {
      if (path === "/api/admin/directory")
        return json({ items: [{ ...users.items[0], activeSessionExpiresAt }] });
      if (path.endsWith("/sessions") && init?.method === "DELETE")
        return new Promise<Response>((resolve) => {
          release = () => {
            activeSessionExpiresAt = null;
            resolve(new Response(null, { status: 204 }));
          };
        });
      return undefined;
    });
    const trigger = await screen.findByRole("button", { name: "Actions for trailrunner" });
    activeSessionExpiresAt = new Date(Date.now() + 3600000).toISOString();
    await user.click(trigger);
    const revoke = await screen.findByRole("menuitem", { name: "Revoke sessions" });
    expect(revoke).not.toHaveClass("danger-item");
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "View usage",
      "Revoke sessions",
      "Disable user",
      "Delete user",
    ]);
    await user.click(revoke);
    expect(trigger).toBeDisabled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const calls = requests.mock.calls.filter(([path]) => String(path).endsWith("/sessions"));
    expect(calls).toHaveLength(1);
    expect(calls[0][1]?.method).toBe("DELETE");
    expect(new Headers(calls[0][1]?.headers).get("X-CSRF-Token")).toBe(admin.csrfToken);
    await act(async () => release());
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(screen.getByText("Active", { selector: ".admin-user-status" })).toBeInTheDocument();
    await user.click(trigger);
    await screen.findByRole("menuitem", { name: "View usage" });
    expect(screen.queryByRole("menuitem", { name: "Revoke sessions" })).not.toBeInTheDocument();
  });

  test("hides sessions revoked elsewhere when the menu refreshes", async () => {
    const user = userEvent.setup();
    let expires: string | null = "2099-01-01T00:00:00Z";
    let release!: (response: Response) => void;
    let reads = 0;
    setup("/admin", (path) => {
      if (path !== "/api/admin/directory") return undefined;
      if (++reads === 1)
        return json({ items: [{ ...users.items[0], activeSessionExpiresAt: expires }] });
      return new Promise<Response>((resolve) => {
        release = resolve;
      });
    });
    const trigger = await screen.findByRole("button", { name: "Actions for trailrunner" });
    expires = null;
    await user.click(trigger);
    expect(screen.queryByRole("menuitem", { name: "Revoke sessions" })).not.toBeInTheDocument();
    await act(async () =>
      release(json({ items: [{ ...users.items[0], activeSessionExpiresAt: expires }] })),
    );
    expect(screen.queryByRole("menuitem", { name: "Revoke sessions" })).not.toBeInTheDocument();
  });

  test("removes Revoke sessions when the last session expires with the menu open", async () => {
    const user = userEvent.setup();
    let expires: string | null = null;
    setup("/admin", (path) =>
      path === "/api/admin/directory"
        ? json({ items: [{ ...users.items[0], activeSessionExpiresAt: expires }] })
        : undefined,
    );
    const trigger = await screen.findByRole("button", { name: "Actions for trailrunner" });
    expires = new Date(Date.now() + 1000).toISOString();
    await user.click(trigger);
    expect(await screen.findByRole("menuitem", { name: "Revoke sessions" })).toBeInTheDocument();
    await waitFor(
      () =>
        expect(screen.queryByRole("menuitem", { name: "Revoke sessions" })).not.toBeInTheDocument(),
      { timeout: 2500 },
    );
    expect(screen.getByRole("menuitem", { name: "View usage" })).toBeInTheDocument();
  });

  test("failed session revocation keeps the user and hides raw diagnostics", async () => {
    const user = userEvent.setup();
    setup("/admin", (path, init) => {
      if (path === "/api/admin/directory")
        return json({
          items: [{ ...users.items[0], activeSessionExpiresAt: "2099-01-01T00:00:00Z" }],
        });
      if (path.endsWith("/sessions") && init?.method === "DELETE")
        return json({ detail: "private-sql-canary" }, 503);
      return undefined;
    });
    const trigger = await screen.findByRole("button", { name: "Actions for trailrunner" });
    await user.click(trigger);
    await user.click(await screen.findByRole("menuitem", { name: "Revoke sessions" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "User sessions could not be revoked",
    );
    expect(screen.getByRole("alert")).not.toHaveTextContent("private-sql-canary");
    await waitFor(() => expect(trigger).toHaveFocus());
    await user.click(trigger);
    expect(await screen.findByRole("menuitem", { name: "Revoke sessions" })).toBeInTheDocument();
  });

  test("signout retains its label and appearance while preventing duplicate requests", async () => {
    const user = userEvent.setup();
    let release!: (response: Response) => void;
    const { requests } = setup("/admin", (path, init) =>
      path === "/api/admin/session" && init?.method === "DELETE"
        ? new Promise<Response>((resolve) => {
            release = resolve;
          })
        : undefined,
    );
    await screen.findByRole("cell", { name: "trailrunner" });
    const button = screen.getByRole("button", { name: "Sign out" });
    await user.click(button);
    await user.click(button);
    expect(button).toHaveTextContent(/^Sign out$/);
    expect(button).not.toBeDisabled();
    expect(
      requests.mock.calls.filter(
        ([url, init]) => url === "/api/admin/session" && init?.method === "DELETE",
      ),
    ).toHaveLength(1);
    await act(async () => release(new Response(null, { status: 204 })));
  });

  test("invitation email validation rejects malformed addresses", () => {
    for (const email of [
      "",
      "a",
      "a@",
      "a..b@example.test",
      ".a@example.test",
      "a@%65xample.test",
      "a@example.test/path",
      "a@-example.test",
      "a b@example.test",
    ])
      expect(validInvitationEmail(email)).toBe(false);
    expect(validInvitationEmail(" invitee+tag@example.test ")).toBe(true);
  });

  test("Registered dates and View usage expose only the requested summary", async () => {
    const user = userEvent.setup();
    setup("/admin", (path) =>
      path.includes("/usage?")
        ? json({
            fullName: "Avery Stone",
            lastLoginAt: "2026-10-20T20:25:00Z",
            workoutCount: 245,
            routeCount: 145,
            lastWorkoutDate: "2026-10-18",
          })
        : undefined,
    );
    await screen.findByRole("cell", { name: "trailrunner" });
    expect(screen.getByRole("cell", { name: "Oct 16, 2025" })).toBeInTheDocument();
    expect(
      screen
        .getAllByRole("columnheader")
        .map((cell) => cell.textContent?.replace(/[\u25b2\u25bc ]/g, "")),
    ).toEqual(["Username", "Name", "Email", "Registered", "Status", "Actions"]);
    const trigger = screen.getByRole("button", { name: "Actions for trailrunner" });
    await user.click(trigger);
    expect(screen.getAllByRole("menuitem")[0]).toHaveTextContent("View usage");
    expect(screen.getByRole("menuitem", { name: "Delete user" })).toHaveClass("danger-item");
    expect(screen.getByRole("menuitem", { name: "Delete user" })).not.toHaveAttribute(
      "aria-disabled",
    );
    expect(
      screen.getByRole("menuitem", { name: "Delete user" }).previousElementSibling,
    ).toHaveAttribute("role", "separator");
    await user.click(screen.getByRole("menuitem", { name: "View usage" }));
    const dialog = await screen.findByRole("dialog", { name: "Usage by Avery Stone" });
    expect(await within(dialog).findByText("245 (145 routes)")).toBeInTheDocument();
    expect(within(dialog).getByText("Oct 18, 2026")).toBeInTheDocument();
    expect(within(dialog).getByText(/Oct 20, 2026, 1:25 PM/)).toBeInTheDocument();
    expect(within(dialog).getAllByRole("button")).toHaveLength(1);
    expect(within(dialog).getByRole("button", { name: "Close usage" })).toHaveTextContent("\u00d7");
    await user.click(within(dialog).getByRole("button", { name: "Close usage" }));
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  test("user row cells and the keyboard username action open usage and restore focus", async () => {
    const user = userEvent.setup();
    const { requests } = setup("/admin", (path) =>
      path.includes("/usage?")
        ? json({
            fullName: "Avery Stone",
            lastLoginAt: null,
            workoutCount: 245,
            routeCount: 145,
            lastWorkoutDate: null,
          })
        : undefined,
    );
    const row = (await screen.findByRole("cell", { name: "trailrunner" })).closest("tr")!;
    const username = within(row).getByRole("button", { name: "trailrunner" });
    expect(row).toHaveClass("admin-user-row");
    for (const cell of [
      row,
      within(row).getByText("Avery Stone"),
      within(row).getByText("Active"),
    ]) {
      await user.click(cell);
      const dialog = await screen.findByRole("dialog", { name: "Usage by Avery Stone" });
      expect(await within(dialog).findByText("245 (145 routes)")).toBeInTheDocument();
      await user.click(within(dialog).getByRole("button", { name: "Close usage" }));
      await waitFor(() => expect(username).toHaveFocus());
    }
    for (const key of ["{Enter}", " "]) {
      await user.keyboard(key);
      const dialog = await screen.findByRole("dialog", { name: "Usage by Avery Stone" });
      await user.click(within(dialog).getByRole("button", { name: "Close usage" }));
      await waitFor(() => expect(username).toHaveFocus());
    }
    await user.click(within(row).getByRole("button", { name: "Actions for trailrunner" }));
    expect(screen.getByRole("menuitem", { name: "View usage" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(requests.mock.calls.filter(([path]) => String(path).includes("/usage?"))).toHaveLength(
      5,
    );
  });

  test("invitation rows do not open user usage", async () => {
    const user = userEvent.setup();
    const { requests } = setup("/admin", (path) =>
      path === "/api/admin/directory"
        ? json({
            items: [{ ...users.items[0], kind: "invitation", username: null, fullName: null }],
          })
        : undefined,
    );
    const row = (await screen.findByRole("cell", { name: "avery@example.test" })).closest("tr")!;
    expect(row).not.toHaveClass("admin-user-row");
    await user.click(row);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(requests.mock.calls.some(([path]) => String(path).includes("/usage?"))).toBe(false);
  });

  test("resends and revokes invitations without danger styling, and permits reinviting", async () => {
    const user = userEvent.setup();
    const id = "E".repeat(32);
    let present = true;
    const { requests } = setup("/admin", (path, init) => {
      if (path === "/api/admin/directory")
        return json({
          items: [
            ...users.items,
            ...(present
              ? [
                  {
                    id,
                    kind: "invitation",
                    username: null,
                    fullName: null,
                    email: "invited@example.test",
                    registeredAt: null,
                    state: "expired",
                  },
                ]
              : []),
          ],
        });
      if (path === `/api/admin/invitations/${id}/resend`)
        return json({ id, email: "invited@example.test" });
      if (path === `/api/admin/invitations/${id}` && init?.method === "DELETE") {
        present = false;
        return new Response(null, { status: 204 });
      }
      if (path === "/api/admin/invitations" && init?.method === "POST") {
        present = true;
        return json({ id, email: "invited@example.test" }, 201);
      }
      return undefined;
    });
    await user.click(
      await screen.findByRole("button", { name: "Actions for invited@example.test" }),
    );
    expect(screen.getByRole("menuitem", { name: "Revoke invitation" })).not.toHaveClass(
      "danger-item",
    );
    expect(screen.queryByRole("separator")).not.toBeInTheDocument();
    expect(screen.queryByText("Account actions are planned next.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Resend invitation" }));
    await waitFor(() =>
      expect(
        requests.mock.calls.some(
          ([path, init]) => path === `/api/admin/invitations/${id}/resend` && init?.body === "{}",
        ),
      ).toBe(true),
    );
    await user.click(
      await screen.findByRole("button", { name: "Actions for invited@example.test" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "Revoke invitation" }));
    await waitFor(() =>
      expect(screen.queryByRole("cell", { name: "invited@example.test" })).not.toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Invite a user" }));
    await user.type(screen.getByLabelText("Enter e-mail address"), "invited@example.test");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("cell", { name: "invited@example.test" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
