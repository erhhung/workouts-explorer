import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DataSourcesDialog } from "./DataSourcesDialog";
import type { Session, Source } from "./api";

const session: Session = {
  id: "A".repeat(32),
  csrfToken: "owner-csrf",
  expiresAt: "2099-01-01T00:00:00Z",
  identity: { id: "B".repeat(32), username: "owner", fullName: "Owner", role: "user" },
};
const existing: Source = {
  id: "C".repeat(32),
  displayName: "Existing iCloud",
  type: "health-auto-export-icloud",
  autoSyncEnabled: false,
  status: "connected",
  generation: 7,
  config: { version: 1, path: "Health/Exports", clientId: "preserved-provider-identity" },
};
const retained = "existing value retained unless provided";
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

function setup(
  items: Source[] = [],
  handler?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined,
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  client.setQueryData(["session"], session);
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    const response = handler?.(path, init);
    if (response) return response;
    if (path === "/api/sources" && (!init?.method || init.method === "GET")) return json({ items });
    if (path === "/api/data-sync")
      return json({ schedule: { cadenceSeconds: 86400 }, sources: [], notifications: [] });
    throw new Error(`Unexpected ${init?.method ?? "GET"} ${path}`);
  });
  const onClose = vi.fn(),
    onCloseFocus = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <DataSourcesDialog session={session} onClose={onClose} onCloseFocus={onCloseFocus} />
    </QueryClientProvider>,
  );
  return { client, fetch, onClose, ...view };
}

async function newSource() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Add a source" }));
  return user;
}
async function credentials() {
  for (const [label, value] of [
    ["Path", "Health/Exports"],
    ["Apple ID", "synthetic@example.test"],
    ["Password", "synthetic-obscured-password"],
    ["Cookies", "synthetic-cookie"],
    ["Trust token", "synthetic-trust-token"],
  ])
    fireEvent.change(screen.getByLabelText(label, { exact: true }), { target: { value } });
}

test("empty table, type ordering, defaults, limits and Cancel clear the editor", async () => {
  setup();
  await screen.findByText("No data sources configured.");
  expect(screen.getByRole("dialog", { name: "Data Sources" })).toBeInTheDocument();
  expect(screen.getAllByRole("columnheader").map((el) => el.textContent)).toEqual([
    "Name▲",
    "Type▲ ▼",
    "Auto Sync▲ ▼",
    "Status▲ ▼",
    "Actions",
  ]);
  const user = await newSource();
  expect(screen.getByLabelText("Name")).toHaveValue("Health Auto Export iCloud Drive");
  expect(screen.getByLabelText("Name")).toHaveAttribute("maxlength", "200");
  expect(screen.getByLabelText("Path")).toHaveAttribute("maxlength", "250");
  expect(screen.getByLabelText("Cookies")).toHaveAttribute("maxlength", "4000");
  expect(screen.getByLabelText("Cookies").tagName).toBe("TEXTAREA");
  expect(screen.getByLabelText("Cookies")).toHaveAttribute("rows", "3");
  expect(screen.getByLabelText("Trust token")).toHaveAttribute("maxlength", "250");
  expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
  expect(screen.getByLabelText("Password")).toHaveAttribute("autocomplete", "new-password");
  expect(screen.getByLabelText("Apple ID")).toHaveAttribute("autocomplete", "off");
  expect(screen.getByLabelText("Password")).toHaveAttribute("name", "rclone-obscured-password");
  expect(screen.getByLabelText("Apple ID")).toHaveAttribute("name", "rclone-apple-id");
  for (const label of ["Apple ID", "Password"]) {
    const input = screen.getByLabelText(label);
    for (const attribute of ["data-1p-ignore", "data-lpignore", "data-bwignore"])
      expect(input).toHaveAttribute(attribute, "true");
  }
  for (const label of ["Path", "Apple ID", "Password", "Cookies", "Trust token"]) {
    const control = screen.getByLabelText(label, { exact: true });
    expect(control).not.toHaveAttribute("title");
    const help = document.getElementById(control.getAttribute("aria-describedby")!);
    expect(help).toHaveAttribute("role", "tooltip");
    expect(help).toHaveClass("tooltip-content--portal");
  }
  await user.click(screen.getByRole("combobox", { name: "Type" }));
  expect(screen.getAllByRole("option").map((el) => el.textContent)).toEqual([
    expect.stringContaining("Health Auto Export — iCloud Drive"),
    expect.stringContaining("Health Auto Export — Server Path"),
  ]);
  await user.click(screen.getByRole("option", { name: "Health Auto Export — Server Path" }));
  expect(screen.getByLabelText("Name")).toHaveValue("Health Auto Export Server Path");
  expect(screen.queryByLabelText("Apple ID")).not.toBeInTheDocument();
  const localHelp = document.getElementById(
    screen.getByLabelText("Path").getAttribute("aria-describedby")!,
  );
  expect(localHelp).toHaveTextContent("Path in ingest worker container containing");
  expect(localHelp?.textContent).toBe(
    "Path in ingest worker container containing\nHealthAutoExport-YYYY-MM-DD.json files",
  );
  expect(localHelp).toHaveClass("tooltip-content--multiline");
  fireEvent.change(screen.getByLabelText("Path"), { target: { value: "/data/workouts" } });
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByLabelText("Path")).not.toBeInTheDocument();
  await newSource();
  expect(screen.getByLabelText("Path")).toHaveValue("");
  expect(screen.getByLabelText("Name")).toHaveValue("Health Auto Export iCloud Drive");
});

test("create waits for connection success, sends owner CSRF and clears the editor", async () => {
  const created: Source = {
    ...existing,
    generation: 1,
    autoSyncEnabled: true,
    status: "checking-connection",
  };
  let items: Source[] = [];
  const { fetch, client } = setup([], (path, init) => {
    if (path === "/api/sources" && init?.method === "POST") {
      items = [created];
      return json(created, 201);
    }
    if (path === "/api/sources") return json({ items });
    if (path === `/api/sources/${created.id}`) {
      items = [{ ...created, status: "connected" }];
      return json(items[0]);
    }
    return undefined;
  });
  const user = await newSource();
  await credentials();
  const automated = await screen.findByRole("checkbox", {
    name: "Enable automated sync (once a day)",
  });
  expect(automated).not.toBeChecked();
  await user.click(automated);
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(screen.getByRole("button", { name: "Checking..." })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "New source" })).not.toBeInTheDocument(),
  );
  const [, init] = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
  expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
  expect(JSON.parse(String(init?.body))).toMatchObject({
    type: existing.type,
    autoSyncEnabled: true,
    config: {
      version: 1,
      path: "Health/Exports",
      appleId: "synthetic@example.test",
      password: "synthetic-obscured-password",
      cookies: "synthetic-cookie",
      trustToken: "synthetic-trust-token",
    },
  });
  expect(screen.getByRole("cell", { name: "Connected" })).toBeInTheDocument();
  expect(
    JSON.stringify(client.getQueryData(["sources", session.id, session.identity.id])),
  ).not.toContain("synthetic-cookie");
});

test("edit locks type, shows exact write-only placeholders, omits unchanged credentials and retains Auto Sync", async () => {
  const { fetch } = setup([existing], (path, init) =>
    path.includes("checkConnection=true") ? json({ ...existing, generation: 8 }) : undefined,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  expect(screen.getByRole("combobox", { name: "Type" })).toBeDisabled();
  expect(screen.getAllByPlaceholderText(retained)).toHaveLength(4);
  for (const label of ["Apple ID", "Password", "Cookies", "Trust token"])
    expect(screen.getByLabelText(label, { exact: true })).toHaveValue("");
  expect(screen.getByLabelText("Path")).toHaveValue("Health/Exports");
  expect(
    await screen.findByRole("checkbox", { name: "Enable automated sync (once a day)" }),
  ).not.toBeChecked();
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Edit source" })).not.toBeInTheDocument(),
  );
  const [path, init] = fetch.mock.calls.find(([, init]) => init?.method === "PATCH")!;
  expect(path).toBe(`/api/sources/${existing.id}?checkConnection=true`);
  expect(JSON.parse(String(init?.body))).toEqual({
    displayName: existing.displayName,
    autoSyncEnabled: false,
    config: { version: 1, path: existing.config.path },
  });
  expect(screen.getByRole("cell", { name: "Disabled" })).toBeInTheDocument();
});

test("failed check keeps a retryable identity, clears credentials and never renders server detail", async () => {
  let saved = false;
  const { fetch } = setup([], (path, init) => {
    if (path === "/api/sources" && init?.method === "POST") {
      saved = true;
      return json({ ...existing, generation: 1, status: "checking-connection" }, 201);
    }
    if (path.includes("checkConnection=true")) return json({ ...existing, generation: 2 });
    if (path === `/api/sources/${existing.id}`)
      return json({
        ...existing,
        generation: 1,
        status: "connection-failed",
        statusCode: "source-auth-expired",
        statusSummary: "raw-private-cookie",
      });
    if (path === "/api/sources") return json({ items: saved ? [existing] : [] });
    return undefined;
  });
  const user = await newSource();
  await credentials();
  await user.click(screen.getByRole("button", { name: "Save" }));
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("Connection check failed");
  expect(alert).toHaveTextContent("authentication failed or expired");
  expect(alert).not.toHaveTextContent("raw-private-cookie");
  expect(screen.getByLabelText("Password")).toHaveValue("");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "New source" })).not.toBeInTheDocument(),
  );
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "PATCH")).toHaveLength(1);
});

test("a newer generation cannot satisfy a previous Save", async () => {
  setup([existing], (path, init) =>
    init?.method === "PATCH"
      ? json({ ...existing, generation: 8, status: "checking-connection" })
      : path === `/api/sources/${existing.id}`
        ? json({ ...existing, generation: 9 })
        : undefined,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("changed during the connection check");
  expect(screen.getByRole("heading", { name: "Edit source" })).toBeInTheDocument();
});

test("timeout releases pending controls and reports bounded connection failure", async () => {
  setup([existing], (path, init) =>
    init?.method === "PATCH" || path === `/api/sources/${existing.id}`
      ? json({ ...existing, generation: 8, status: "checking-connection" })
      : undefined,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  vi.useFakeTimers();
  fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(screen.getByRole("alert")).toHaveTextContent("within 30 seconds");
  expect(screen.getByRole("button", { name: "Save" })).not.toBeDisabled();
});

test("deletion requires confirmation, retains failed rows and uses owner CSRF", async () => {
  let fail = true,
    deleted = false;
  const { fetch } = setup([existing], (path, init) => {
    if (init?.method === "DELETE") {
      if (fail) return json({ title: "private-error" }, 503);
      deleted = true;
      return new Response(null, { status: 204 });
    }
    if (path === "/api/sources") return json({ items: deleted ? [] : [existing] });
    return undefined;
  });
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Delete source" }));
  const dialog = screen.getByRole("dialog", { name: "Delete source" });
  expect(dialog).toHaveTextContent(
    "Delete Existing iCloud? Its connection credentials will be deleted and its active sync jobs cancelled. Imported workouts will be retained.",
  );
  expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  expect(await within(dialog).findByRole("alert")).toHaveTextContent("could not be deleted");
  expect(document.querySelector(".data-sources-table tbody")).toHaveTextContent(
    existing.displayName,
  );
  fail = false;
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  await screen.findByText("No data sources configured.");
  const [, init] = fetch.mock.calls.find(([, init]) => init?.method === "DELETE")!;
  expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
});

test("a late save after unmount cannot repopulate a cleared session cache", async () => {
  let release!: (response: Response) => void;
  const { client, unmount } = setup([existing], (_path, init) =>
    init?.method === "PATCH"
      ? new Promise<Response>((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  unmount();
  client.clear();
  await act(async () => release(json({ ...existing, generation: 8 })));
  expect(client.getQueryData(["sources", session.id, session.identity.id])).toBeUndefined();
});

test.each(["health-auto-export-local", "health-auto-export-icloud"] as const)(
  "new %s source defaults to automated sync disabled",
  async (type) => {
    let saved: Source | undefined;
    const { fetch } = setup([], (path, init) => {
      if (path === "/api/sources" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        saved = {
          ...existing,
          type,
          generation: 1,
          autoSyncEnabled: body.autoSyncEnabled,
          displayName: body.displayName,
        };
        return json(saved, 201);
      }
      if (path === "/api/sources") return json({ items: saved ? [saved] : [] });
      return undefined;
    });
    const user = await newSource();
    if (type === "health-auto-export-local") {
      await user.click(screen.getByRole("combobox", { name: "Type" }));
      await user.click(screen.getByRole("option", { name: "Health Auto Export — Server Path" }));
      fireEvent.change(screen.getByLabelText("Path"), { target: { value: "/data/workouts" } });
    } else await credentials();
    const checkbox = await screen.findByRole("checkbox", {
      name: "Enable automated sync (once a day)",
    });
    expect(checkbox).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(screen.queryByRole("heading", { name: "New source" })).not.toBeInTheDocument(),
    );
    const [, init] = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(JSON.parse(String(init?.body)).autoSyncEnabled).toBe(false);
    expect(screen.getByRole("cell", { name: "Disabled" })).toBeInTheDocument();
  },
);

test("edit loads its disabled flag, uses the shared twice-a-day label, and PATCH can enable sync", async () => {
  let source = existing;
  const { fetch } = setup([existing], (path, init) => {
    if (path === "/api/data-sync")
      return json({ schedule: { cadenceSeconds: 43200 }, sources: [], notifications: [] });
    if (init?.method === "PATCH") {
      source = {
        ...existing,
        generation: 8,
        autoSyncEnabled: JSON.parse(String(init.body)).autoSyncEnabled,
      };
      return json(source);
    }
    if (path === "/api/sources") return json({ items: [source] });
    return undefined;
  });
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  const checkbox = await screen.findByRole("checkbox", {
    name: "Enable automated sync (twice a day)",
  });
  expect(checkbox).not.toBeChecked();
  await user.click(checkbox);
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Edit source" })).not.toBeInTheDocument(),
  );
  const [, init] = fetch.mock.calls.find(([, init]) => init?.method === "PATCH")!;
  expect(JSON.parse(String(init?.body)).autoSyncEnabled).toBe(true);
  expect(screen.getByRole("cell", { name: "Enabled" })).toBeInTheDocument();
});

test("a late save cannot write into a replacement principal even with the same session ID", async () => {
  let release!: (response: Response) => void;
  const { client } = setup([existing], (_path, init) =>
    init?.method === "PATCH"
      ? new Promise<Response>((resolve) => {
          release = resolve;
        })
      : undefined,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Actions for Existing iCloud" }));
  await user.click(screen.getByRole("menuitem", { name: "Edit source" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  client.setQueryData(["session"], {
    ...session,
    identity: { ...session.identity, id: "D".repeat(32) },
  });
  client.setQueryData(["sources", session.id, session.identity.id], { items: [] });
  await act(async () => release(json({ ...existing, generation: 8 })));
  expect(client.getQueryData(["sources", session.id, session.identity.id])).toEqual({ items: [] });
  expect(client.getQueryData(["sources", session.id, "D".repeat(32)])).toBeUndefined();
});
