import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ADMIN_SESSION_EXPIRED_EVENT, ApiError, api, type PublicConfig, type Session } from "./api";
import { ContentBackground } from "./ContentBackground";
import { AdminUsers } from "./AdminUsers";
import { AdminOSMRegions } from "./AdminOSMRegions";
import { AdminOSMProcessingSettings } from "./AdminOSMProcessingSettings";
import { AdminOSMStorage } from "./AdminOSMStorage";
import { FutureReleasePlaceholder } from "./FutureReleasePlaceholder";
import { RetainedTab, useTabActive } from "./RetainedTab";
import { useKeyboardShortcut } from "./keyboardShortcuts";

const TABS = [
  {
    id: "accounts",
    title: "Accounts",
    sections: [["users", "Users"]],
  },
  {
    id: "osm",
    title: "OSM Maps",
    sections: [
      ["regions", "Regions"],
      ["storage", "Storage usage"],
      ["history", "Update history"],
    ],
  },
  {
    id: "operations",
    title: "Operations",
    sections: [
      ["overview", "Overview"],
      ["tasks", "System tasks"],
      ["incidents", "Incidents"],
      ["announcements", "Announcements"],
      ["audit", "Audit"],
      ["recovery", "Recovery"],
    ],
  },
  {
    id: "settings",
    title: "Settings",
    sections: [
      ["preferences", "Console preferences"],
      ["osm-processing", "OSM Processing"],
      ["access", "Administrator access"],
      ["configuration", "Effective configuration"],
    ],
  },
] as const;
type Navigate = (path: string, replace?: boolean) => void;
type AdminConfig = {
  passwordMinimumLength: number;
  pageSizeMaximum: number;
  sessionLifetimeSeconds: number;
  pollingIntervalSeconds: number;
  mapFamilies: string[];
};

export function AdminRoot(props: {
  path: string;
  navigate: Navigate;
  login: ReactNode;
  brand: ReactNode;
}) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false, refetchOnWindowFocus: false },
          mutations: { retry: false },
        },
      }),
  );
  return (
    <QueryClientProvider client={client}>
      <AdminApplication {...props} />
    </QueryClientProvider>
  );
}

function AdminApplication({
  path,
  navigate,
  login,
  brand,
}: {
  path: string;
  navigate: Navigate;
  login: ReactNode;
  brand: ReactNode;
}) {
  const client = useQueryClient();
  const session = useQuery({
    queryKey: ["admin-session"],
    queryFn: async ({ signal }) => {
      try {
        const value = await api<Session>("/api/admin/session", { signal });
        if (value.identity.role !== "administrator") throw new ApiError(403);
        return value;
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
  });
  const config = useQuery({
    queryKey: ["public-config"],
    queryFn: ({ signal }) => api<PublicConfig>("/api/config", { signal }),
  });
  useEffect(() => {
    const original = session.data;
    if (!original) return;
    const controller = new AbortController();
    let checking = false;
    let timer: number;
    const current = () =>
      !controller.signal.aborted && client.getQueryData(["admin-session"]) === original;
    const confirm = async () => {
      if (!current() || checking) return;
      checking = true;
      try {
        const fresh = await api<Session>("/api/admin/session", { signal: controller.signal });
        if (!current()) return;
        if (fresh.identity.role !== "administrator") throw new ApiError(401);
        if (fresh.id !== original.id || fresh.identity.id !== original.identity.id) {
          await client.cancelQueries({
            predicate: (query) =>
              query.queryKey[0] !== "public-config" && query.queryKey[0] !== "admin-session",
          });
          if (!current()) return;
          client.removeQueries({
            predicate: (query) =>
              query.queryKey[0] !== "public-config" && query.queryKey[0] !== "admin-session",
          });
        }
        if (
          fresh.id !== original.id ||
          fresh.expiresAt !== original.expiresAt ||
          fresh.csrfToken !== original.csrfToken ||
          fresh.identity.id !== original.identity.id
        )
          client.setQueryData(["admin-session"], fresh);
      } catch (error) {
        if (!(error instanceof ApiError) || error.status !== 401 || !current()) return;
        await client.cancelQueries({
          predicate: (query) =>
            query.queryKey[0] !== "public-config" && query.queryKey[0] !== "admin-session",
        });
        if (!current()) return;
        client.removeQueries({
          predicate: (query) =>
            query.queryKey[0] !== "public-config" && query.queryKey[0] !== "admin-session",
        });
        client.setQueryData(["admin-session"], null);
        navigate(
          `/login?returnTo=${encodeURIComponent(window.location.pathname + window.location.search)}`,
          true,
        );
      } finally {
        checking = false;
      }
    };
    const deadline = () => {
      window.clearTimeout(timer);
      const remaining = Date.parse(original.expiresAt) - Date.now();
      if (!Number.isFinite(remaining) || remaining <= 0) {
        void confirm();
        timer = window.setTimeout(deadline, 10_000);
      } else timer = window.setTimeout(deadline, Math.min(remaining, 2_147_483_647));
    };
    const event = () => {
      void confirm();
    };
    const visibility = () => {
      if (document.visibilityState === "visible") event();
    };
    deadline();
    window.addEventListener(ADMIN_SESSION_EXPIRED_EVENT, event);
    window.addEventListener("focus", event);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
      window.removeEventListener(ADMIN_SESSION_EXPIRED_EVENT, event);
      window.removeEventListener("focus", event);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [client, navigate, session.data]);
  useEffect(() => {
    if (session.data && path.split("?")[0] === "/login") {
      const candidate = new URLSearchParams(path.split("?")[1] ?? "").get("returnTo") ?? "/admin";
      navigate(
        candidate.startsWith("/admin/") || candidate === "/admin" ? candidate : "/admin",
        true,
      );
    }
  }, [session.data, path, navigate]);
  if (session.isPending || config.isPending)
    return (
      <main className="center-state" aria-busy="true">
        <p role="status">Opening administration...</p>
      </main>
    );
  if ((!session.data && session.isError) || config.isError)
    return (
      <main className="center-state">
        <h1>Administration is unavailable.</h1>
        <p role="alert">We couldn't check your administrator session or application settings.</p>
        <button
          onClick={() => {
            void session.refetch();
            void config.refetch();
          }}
        >
          Try again
        </button>
        <a href="/login?switchAccount=1">Sign in</a>
      </main>
    );
  if (!session.data) return login;
  return (
    <AdminShell
      key={`${session.data.id}:${session.data.identity.id}`}
      path={path}
      session={session.data}
      config={config.data!}
      navigate={navigate}
      brand={brand}
    />
  );
}

function AdminShell({
  path,
  session,
  config,
  navigate,
  brand,
}: {
  path: string;
  session: Session;
  config: PublicConfig;
  navigate: Navigate;
  brand: ReactNode;
}) {
  const client = useQueryClient();
  const segments = path.split("?")[0].split("/");
  const tab = TABS.find((value) => value.id === segments[2]) ?? TABS[0];
  const shell = useRef<HTMLDivElement>(null);
  const shortcutFocus = useRef(false);
  const signoutLock = useRef(false);
  const [remembered, setRemembered] = useState<Record<string, string>>({});
  const selectedSection =
    tab.sections.find(([id]) => id === segments[3])?.[0] ?? tab.sections[0][0];
  useEffect(() => {
    setRemembered((old) =>
      old[tab.id] === selectedSection ? old : { ...old, [tab.id]: selectedSection },
    );
  }, [tab.id, selectedSection]);
  useKeyboardShortcut("tab", shell, (shortcut) => {
    if (shortcut.kind !== "tab") return false;
    const target = TABS[["/", "/map", "/stats", "/data-sync"].indexOf(shortcut.path)];
    if (!target) return false;
    if (target.id === tab.id) return true;
    shortcutFocus.current = true;
    navigate(`/admin/${target.id}/${remembered[target.id] ?? target.sections[0][0]}`);
    return true;
  });
  useLayoutEffect(() => {
    if (!shortcutFocus.current) return;
    shortcutFocus.current = false;
    shell.current
      ?.querySelector<HTMLElement>("[data-app-tab]:not([hidden]) [data-app-content]")
      ?.focus({ preventScroll: true });
  }, [tab.id]);
  useEffect(() => {
    if (tab.id === "accounts" && ["invitations", "deletions"].includes(segments[3]))
      navigate("/admin/accounts/users", true);
  }, [tab.id, segments[3], navigate]);
  const signout = useMutation({
    mutationFn: () => api<void>("/api/admin/session", { method: "DELETE" }, session.csrfToken),
    onSuccess: async () => {
      await client.cancelQueries();
      client.clear();
      client.setQueryData(["admin-session"], null);
      navigate("/login?returnTo=%2Fadmin", true);
    },
    onSettled: () => {
      signoutLock.current = false;
    },
  });
  return (
    <div className="shell admin-shell" ref={shell}>
      <header className="app-header">
        <a
          className="wordmark"
          href="/admin"
          onClick={(event) => {
            event.preventDefault();
            navigate("/admin");
          }}
        >
          {brand}
        </a>
        <nav aria-label="Administration">
          {TABS.map((item) => (
            <a
              key={item.id}
              href={`/admin/${item.id}/${remembered[item.id] ?? item.sections[0][0]}`}
              aria-current={tab.id === item.id ? "page" : undefined}
              onClick={(event) => {
                event.preventDefault();
                navigate(`/admin/${item.id}/${remembered[item.id] ?? item.sections[0][0]}`);
              }}
            >
              {item.title}
            </a>
          ))}
        </nav>
        <button
          className="secondary admin-signout"
          aria-busy={signout.isPending}
          onClick={() => {
            if (!signoutLock.current) {
              signoutLock.current = true;
              signout.mutate();
            }
          }}
        >
          Sign out
        </button>
      </header>
      {TABS.map((item) => {
        const selected =
          item.sections.find(
            ([id]) => id === (item.id === tab.id ? segments[3] : remembered[item.id]),
          ) ?? item.sections[0];
        const tableCaption =
          (item.id === "accounts" && selected[0] === "users") ||
          (item.id === "osm" && selected[0] === "regions");
        const planned =
          !tableCaption &&
          !(item.id === "osm" && selected[0] === "storage") &&
          !(
            item.id === "settings" &&
            ["osm-processing", "configuration", "access"].includes(selected[0])
          );
        return (
          <RetainedTab key={item.id} id={`/admin/${item.id}`} active={item.id === tab.id}>
            <main
              className={`stats-page admin-page${tableCaption ? " admin-page--table-caption" : ""}`}
            >
              <aside className="stats-sidebar" data-app-scroll>
                <nav className="stats-report-nav" aria-label={`${item.title} sections`}>
                  <h2>{item.title}</h2>
                  {item.sections.map(([id, title]) => (
                    <button
                      type="button"
                      key={id}
                      aria-current={id === selected[0] ? "page" : undefined}
                      onClick={() => {
                        setRemembered((old) => ({ ...old, [item.id]: id }));
                        navigate(`/admin/${item.id}/${id}`);
                      }}
                    >
                      {title}
                    </button>
                  ))}
                </nav>
              </aside>
              <div className="stats-report-viewport content-background">
                <ContentBackground />
                <section
                  className={`stats-report admin-content${planned ? " admin-content--planned" : ""}${item.id === "osm" && selected[0] === "storage" ? " admin-content--storage" : ""}`}
                  data-app-content
                  data-app-scroll
                  tabIndex={-1}
                  aria-labelledby={
                    planned || (item.id === "osm" && selected[0] === "storage")
                      ? undefined
                      : `admin-heading-${item.id}`
                  }
                  aria-label={
                    planned || (item.id === "osm" && selected[0] === "storage")
                      ? selected[1]
                      : undefined
                  }
                >
                  {!tableCaption &&
                    !planned &&
                    !(item.id === "osm" && selected[0] === "storage") && (
                      <header className="stats-heading">
                        <p className="eyebrow">Administration</p>
                        <h1 id={`admin-heading-${item.id}`}>{selected[1]}</h1>
                      </header>
                    )}
                  {signout.isError && (
                    <p role="alert" className="error-summary">
                      Sign out failed. Try again.
                    </p>
                  )}
                  {item.id === "osm" && (
                    <RetainedTab
                      id="/admin/osm/regions"
                      active={tab.id === "osm" && selected[0] === "regions"}
                    >
                      <AdminOSMRegions
                        baseMaps={config.baseMaps}
                        polling={config.pollingIntervalSeconds}
                        session={session}
                        headingId={`admin-heading-${item.id}`}
                      />
                    </RetainedTab>
                  )}
                  {item.id === "osm" && selected[0] === "regions" ? null : item.id === "accounts" &&
                    selected[0] === "users" ? (
                    <AdminUsers
                      polling={config.pollingIntervalSeconds}
                      session={session}
                      headingId={`admin-heading-${item.id}`}
                    />
                  ) : item.id === "osm" && selected[0] === "storage" ? (
                    <AdminOSMStorage polling={config.pollingIntervalSeconds} />
                  ) : item.id === "settings" && selected[0] === "osm-processing" ? (
                    <AdminOSMProcessingSettings session={session} />
                  ) : item.id === "settings" && selected[0] === "configuration" ? (
                    <EffectiveConfig />
                  ) : item.id === "settings" && selected[0] === "access" ? (
                    <div className="sync-card">
                      <h2>Administrator identity</h2>
                      <p>@{session.identity.username}</p>
                      <a href="/forgot-password">Recover administrator password</a>
                      <p className="muted">
                        Password changes and session management are planned in this milestone.
                      </p>
                    </div>
                  ) : (
                    <FutureReleasePlaceholder />
                  )}
                </section>
              </div>
            </main>
          </RetainedTab>
        );
      })}
    </div>
  );
}

function EffectiveConfig() {
  const active = useTabActive();
  const query = useQuery({
    queryKey: ["admin-effective-config"],
    queryFn: ({ signal }) => api<AdminConfig>("/api/admin/config", { signal }),
    enabled: active,
  });
  return (
    <div className="sync-card admin-section">
      <h2>Managed by Helm/GitOps</h2>
      <p className="muted">Effective deployment settings are read-only.</p>
      {query.data && (
        <dl className="admin-config">
          {[
            ["Minimum password length", query.data.passwordMinimumLength],
            ["Maximum page size", query.data.pageSizeMaximum],
            ["Session lifetime (seconds)", query.data.sessionLifetimeSeconds],
            ["Polling interval (seconds)", query.data.pollingIntervalSeconds],
            ["Base-map families", query.data.mapFamilies.join(", ")],
          ].map(([name, value]) => (
            <div key={name}>
              <dt>{name}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
      {query.isPending && (
        <p className="content-state-message content-state-message--centered" role="status">
          Loading effective configuration...
        </p>
      )}
      {query.isError && (
        <div role="alert">
          <p className="content-state-message content-state-message--centered">
            Effective configuration is unavailable.
          </p>
          <button className="secondary" onClick={() => void query.refetch()}>
            Retry
          </button>
        </div>
      )}
    </div>
  );
}
