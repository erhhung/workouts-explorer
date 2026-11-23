import * as Dialog from "./Dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { ApiError, api, type Session } from "./api";
import { CoverageCellValue } from "./CoverageTableCell";
import { useTabActive } from "./RetainedTab";
import { formatDateOnly } from "./date";
import { dateTimeFormatter, numberFormatter } from "./formatters";

export type DirectoryEntry = {
  id: string;
  kind: "user" | "invitation";
  username: string | null;
  fullName: string | null;
  email: string;
  registeredAt: string | null;
  activeSessionExpiresAt: string | null;
  state: "active" | "invited" | "disabled" | "deleting" | "deleted" | "expired" | "revoked";
};
type Directory = { items: DirectoryEntry[] };
type Column = "username" | "fullName" | "email" | "registeredAt" | "state";
const COLUMNS: Array<[Column, string]> = [
  ["username", "Username"],
  ["fullName", "Name"],
  ["email", "Email"],
  ["registeredAt", "Registered"],
  ["state", "Status"],
];
const STATES: Record<DirectoryEntry["state"], string> = {
  active: "Active",
  invited: "Invited",
  disabled: "Disabled",
  deleting: "Deleting",
  deleted: "Deleted",
  expired: "Expired",
  revoked: "Revoked",
};
const DIRECTORY_KEY = ["admin-directory"];
const DELETION_BUSY = "https://workouts-explorer.invalid/problems/account-deletion-busy";

function deletionIsBusy(error: unknown) {
  return error instanceof ApiError && error.status === 503 && error.problem.type === DELETION_BUSY;
}

export function validInvitationEmail(raw: string) {
  const email = raw.trim();
  if (email.length > 254 || email.split("@").length !== 2) return false;
  const [local, domain] = email.split("@");
  if (
    local.length > 64 ||
    !/^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(?:\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$/.test(local)
  )
    return false;
  if (!domain || /[\s/:?#\\\[\]@%\u0000-\u001f\u007f]/.test(domain) || domain.endsWith("."))
    return false;
  try {
    const hostname = new URL(`https://${domain}`).hostname;
    return hostname
      .split(".")
      .every((label) => label.length <= 63 && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(label));
  } catch {
    return false;
  }
}

function UserActions({
  entry,
  enabled,
  onUsage,
  onInvitation,
  onAccess,
  onDelete,
  onDeletion,
  onRevokeSessions,
  onRefresh,
}: {
  entry: DirectoryEntry;
  enabled: boolean;
  onUsage: (entry: DirectoryEntry, trigger: HTMLButtonElement | null) => void;
  onInvitation: (entry: DirectoryEntry, action: "resend" | "revoke") => void;
  onAccess: (entry: DirectoryEntry, trigger: HTMLButtonElement | null) => void;
  onDelete: (entry: DirectoryEntry, trigger: HTMLButtonElement | null) => void;
  onDeletion: (entry: DirectoryEntry, trigger: HTMLButtonElement | null) => void;
  onRevokeSessions: (entry: DirectoryEntry, trigger: HTMLButtonElement | null) => void;
  onRefresh: () => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  const [checkingSessions, setCheckingSessions] = useState(false);
  const [sessionClock, setSessionClock] = useState(0);
  const menuRequest = useRef(0);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!enabled) setOpen(false);
  }, [enabled]);
  useEffect(() => {
    if (!open) return;
    const remaining = Date.parse(entry.activeSessionExpiresAt ?? "") - Date.now();
    if (!(remaining > 0)) return;
    const timer = window.setTimeout(
      () => setSessionClock((old) => old + 1),
      Math.min(remaining + 1, 2_147_483_647),
    );
    return () => window.clearTimeout(timer);
  }, [open, entry.activeSessionExpiresAt, sessionClock]);
  return (
    <DropdownMenu.Root
      open={open && enabled}
      onOpenChange={(next) => {
        const request = ++menuRequest.current;
        setOpen(next);
        if (!next || entry.kind !== "user") return;
        setCheckingSessions(true);
        const done = () => {
          if (menuRequest.current === request) setCheckingSessions(false);
        };
        void onRefresh().then(done, done);
      }}
    >
      <DropdownMenu.Trigger
        ref={trigger}
        className="workout-action-trigger"
        aria-label={`Actions for ${entry.username ?? entry.email}`}
        disabled={!enabled}
      >
        <span aria-hidden="true">...</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content workout-action-menu admin-user-actions"
          align="end"
          sideOffset={6}
          collisionPadding={{ left: 1, top: 5, bottom: 5 }}
        >
          <DropdownMenu.Item
            disabled={entry.kind !== "user"}
            onSelect={() => onUsage(entry, trigger.current)}
          >
            View usage
          </DropdownMenu.Item>
          {entry.kind === "invitation" ? (
            <>
              <DropdownMenu.Item onSelect={() => onInvitation(entry, "resend")}>
                Resend invitation
              </DropdownMenu.Item>
              <DropdownMenu.Item onSelect={() => onInvitation(entry, "revoke")}>
                Revoke invitation
              </DropdownMenu.Item>
            </>
          ) : (
            <>
              {!checkingSessions &&
                entry.state === "active" &&
                Date.parse(entry.activeSessionExpiresAt ?? "") > Date.now() && (
                  <DropdownMenu.Item onSelect={() => onRevokeSessions(entry, trigger.current)}>
                    Revoke sessions
                  </DropdownMenu.Item>
                )}
              <DropdownMenu.Item
                disabled={entry.state !== "active" && entry.state !== "disabled"}
                onSelect={() => onAccess(entry, trigger.current)}
              >
                {entry.state === "disabled" ? "Enable user" : "Disable user"}
              </DropdownMenu.Item>
              {entry.state === "deleting" && (
                <DropdownMenu.Item onSelect={() => onDeletion(entry, trigger.current)}>
                  View deletion
                </DropdownMenu.Item>
              )}
              <DropdownMenu.Separator />
              <DropdownMenu.Item
                className="danger-item"
                disabled={entry.state !== "active" && entry.state !== "disabled"}
                onSelect={() => onDelete(entry, trigger.current)}
              >
                Delete user
              </DropdownMenu.Item>
            </>
          )}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function AdminUsers({
  polling,
  session,
  headingId,
}: {
  polling: number;
  session: Session;
  headingId: string;
}) {
  const active = useTabActive();
  const activeRef = useRef(active);
  activeRef.current = active;
  const client = useQueryClient();
  const [sort, setSort] = useState<{ field: Column; direction: "asc" | "desc" }>({
    field: "username",
    direction: "asc",
  });
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [usageTarget, setUsageTarget] = useState<{
    entry: DirectoryEntry;
    trigger: HTMLButtonElement | null;
  }>();
  const [actionError, setActionError] = useState("");
  const invitationLock = useRef(false);
  const revokeSessionsLock = useRef(false);
  const revokeSessionsFocus = useRef<HTMLButtonElement | null>(null);
  const [accessTarget, setAccessTarget] = useState<{
    entry: DirectoryEntry;
    enabled: boolean;
    trigger: HTMLButtonElement | null;
  }>();
  const accessFocus = useRef<HTMLButtonElement | null>(null);
  const accessCancel = useRef<HTMLButtonElement | null>(null);
  const accessLock = useRef(false);
  const [deleteTarget, setDeleteTarget] = useState<DirectoryEntry>();
  const [deleteConfirmation, setDeleteConfirmation] = useState("");
  const [deletionTarget, setDeletionTarget] = useState<DirectoryEntry>();
  const deleteFocus = useRef<HTMLButtonElement | null>(null);
  const deleteCancel = useRef<HTMLButtonElement>(null);
  const deleteLock = useRef(false);
  const deletionRetryAllowed = useRef(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const sendLock = useRef(false);
  const directory = useQuery({
    queryKey: DIRECTORY_KEY,
    queryFn: ({ signal }) => api<Directory>("/api/admin/directory", { signal }),
    enabled: active,
    refetchInterval: (query) =>
      active
        ? query.state.data?.items.some((row) => row.state === "deleting")
          ? Math.min(10_000, Math.max(1, polling) * 1000)
          : Math.max(1, polling) * 1000
        : false,
  });
  const currentSession = () => {
    const current = client.getQueryData<Session | null>(["admin-session"]);
    return current?.id === session.id && current.identity.id === session.identity.id;
  };
  type Deletion = {
    jobId: string;
    phase: "draining" | "purging" | "completed";
    rowsPurged: number;
    workoutsTotal?: number;
    routesTotal?: number;
    sourcesTotal?: number;
    lastErrorCode?: string;
  };
  const deletion = useQuery({
    queryKey: ["admin-account-deletion", session.id, deletionTarget?.id],
    queryFn: ({ signal }) =>
      api<Deletion>(`/api/admin/users/${deletionTarget!.id}/deletion`, { signal }),
    enabled: active && Boolean(deletionTarget),
    refetchInterval: (query) =>
      active && deletionTarget && query.state.data?.phase !== "completed"
        ? Math.min(10_000, Math.max(1, polling) * 1000)
        : false,
  });
  useEffect(() => {
    if (deletion.data?.phase === "completed" && currentSession())
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
  }, [deletion.data?.phase]);
  const remove = useMutation({
    mutationFn: (entry: DirectoryEntry) => {
      if (!activeRef.current || !currentSession() || !deletionRetryAllowed.current)
        throw new Error("Administration is inactive");
      return api<Deletion>(`/api/admin/users/${entry.id}`, { method: "DELETE" }, session.csrfToken);
    },
    retry: (failures, error) =>
      failures < 14 && activeRef.current && currentSession() && deletionIsBusy(error),
    retryDelay: 1000,
    onSuccess: async (_saved, entry) => {
      await client.cancelQueries({ queryKey: DIRECTORY_KEY, exact: true });
      if (!currentSession()) return;
      client.setQueryData<Directory>(DIRECTORY_KEY, (old) => ({
        items: (old?.items ?? []).map((row) =>
          row.kind === "user" && row.id === entry.id
            ? { ...row, state: "deleting", activeSessionExpiresAt: null }
            : row,
        ),
      }));
      setDeleteTarget(undefined);
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
      void client.invalidateQueries({ queryKey: ["admin-account-deletion", session.id, entry.id] });
    },
    onError: () => {
      if (currentSession()) void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onSettled: () => {
      deleteLock.current = false;
    },
  });
  const access = useMutation({
    mutationFn: ({ entry, enabled }: { entry: DirectoryEntry; enabled: boolean }) =>
      api<{ enabled: boolean }>(
        `/api/admin/users/${entry.id}/access`,
        { method: "PUT", body: JSON.stringify({ enabled }) },
        session.csrfToken,
      ),
    onSuccess: async (saved, { entry }) => {
      await client.cancelQueries({ queryKey: DIRECTORY_KEY, exact: true });
      if (!currentSession()) return;
      client.setQueryData<Directory>(DIRECTORY_KEY, (old) => ({
        items: (old?.items ?? []).map((row) =>
          row.kind === "user" && row.id === entry.id
            ? { ...row, state: saved.enabled ? "active" : "disabled", activeSessionExpiresAt: null }
            : row,
        ),
      }));
      setAccessTarget(undefined);
      setDeleteTarget(undefined);
      setDeletionTarget(undefined);
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onError: () => {
      if (currentSession()) void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onSettled: () => {
      accessLock.current = false;
    },
  });
  const revokeSessions = useMutation({
    mutationFn: (entry: DirectoryEntry) =>
      api<void>(`/api/admin/users/${entry.id}/sessions`, { method: "DELETE" }, session.csrfToken),
    onSuccess: async (_saved, entry) => {
      await client.cancelQueries({ queryKey: DIRECTORY_KEY, exact: true });
      if (!currentSession()) return;
      client.setQueryData<Directory>(DIRECTORY_KEY, (old) => ({
        items: (old?.items ?? []).map((row) =>
          row.kind === "user" && row.id === entry.id
            ? { ...row, activeSessionExpiresAt: null }
            : row,
        ),
      }));
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onError: () => {
      if (!currentSession()) return;
      setActionError("User sessions could not be revoked. Refresh and try again.");
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onSettled: () => {
      revokeSessionsLock.current = false;
    },
  });
  useEffect(() => {
    if (revokeSessions.isPending || !revokeSessionsFocus.current) return;
    if (active && currentSession()) revokeSessionsFocus.current.focus();
    revokeSessionsFocus.current = null;
  }, [revokeSessions.isPending, active]);
  const invite = useMutation({
    mutationFn: (address: string) =>
      api<{ id: string; email: string }>(
        "/api/admin/invitations",
        { method: "POST", body: JSON.stringify({ email: address }) },
        session.csrfToken,
      ),
    onSuccess: async (saved) => {
      await client.cancelQueries({ queryKey: DIRECTORY_KEY, exact: true });
      if (!currentSession()) return;
      client.setQueryData<Directory>(DIRECTORY_KEY, (old) => ({
        items: [
          ...(old?.items ?? []).filter((entry) => entry.id !== saved.id),
          {
            id: saved.id,
            kind: "invitation",
            username: null,
            fullName: null,
            email: saved.email,
            registeredAt: null,
            activeSessionExpiresAt: null,
            state: "invited",
          },
        ],
      }));
      setOpen(false);
      setEmail("");
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onError: () => {
      if (currentSession()) void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onSettled: () => {
      sendLock.current = false;
    },
  });
  const invitationAction = useMutation({
    mutationFn: async ({
      entry,
      action,
    }: {
      entry: DirectoryEntry;
      action: "resend" | "revoke";
    }) => {
      if (action === "revoke") {
        await api<void>(
          `/api/admin/invitations/${entry.id}`,
          { method: "DELETE" },
          session.csrfToken,
        );
        return;
      }
      await api(
        `/api/admin/invitations/${entry.id}/resend`,
        { method: "POST", body: JSON.stringify({}) },
        session.csrfToken,
      );
    },
    onSuccess: async (_, { entry, action }) => {
      await client.cancelQueries({ queryKey: DIRECTORY_KEY, exact: true });
      if (!currentSession()) return;
      client.setQueryData<Directory>(DIRECTORY_KEY, (old) => ({
        items: (old?.items ?? [])
          .filter((row) => action !== "revoke" || row.id !== entry.id)
          .map((row) => (row.id === entry.id ? { ...row, state: "invited" } : row)),
      }));
      if (action === "revoke" && activeRef.current)
        document.getElementById(headingId)?.focus({ preventScroll: true });
      void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
    },
    onError: (_, { action }) => {
      if (currentSession()) {
        setActionError(
          action === "resend"
            ? "Invitation email could not be sent. Check SMTP delivery and try again."
            : "The invitation could not be revoked. Refresh and try again.",
        );
        void client.invalidateQueries({ queryKey: DIRECTORY_KEY, exact: true });
      }
    },
    onSettled: () => {
      invitationLock.current = false;
    },
  });
  useEffect(() => {
    if (!active) {
      deletionRetryAllowed.current = false;
      setOpen(false);
      setUsageTarget(undefined);
      setAccessTarget(undefined);
      setDeleteTarget(undefined);
      setDeletionTarget(undefined);
    }
  }, [active]);
  const items = [...(directory.data?.items ?? [])].sort((left, right) => {
    const a = left[sort.field],
      b = right[sort.field];
    if (a == null || b == null)
      return a === b ? left.email.localeCompare(right.email) : a === null ? 1 : -1;
    return (
      (sort.field === "registeredAt"
        ? Date.parse(a) - Date.parse(b)
        : a.localeCompare(b, undefined, { sensitivity: "base" })) *
        (sort.direction === "asc" ? 1 : -1) ||
      left.email.localeCompare(right.email) ||
      left.id.localeCompare(right.id)
    );
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    if (!validInvitationEmail(email) || sendLock.current) return;
    sendLock.current = true;
    invite.mutate(email.trim());
  }
  const actionsEnabled =
    active &&
    !directory.isError &&
    !invitationAction.isPending &&
    !access.isPending &&
    !revokeSessions.isPending &&
    !remove.isPending;
  return (
    <section className="workouts-section admin-users-section" aria-labelledby={headingId}>
      <div className="section-line">
        <h2 id={headingId} tabIndex={-1}>
          Users
        </h2>
        <button
          ref={trigger}
          type="button"
          className="text-action workout-clear-filters"
          onClick={() => {
            setEmail("");
            invite.reset();
            setOpen(true);
          }}
        >
          Invite a user
        </button>
      </div>
      <div className="workout-table-wrap admin-users-wrap" aria-busy={directory.isFetching}>
        <table className="workout-table admin-users">
          <colgroup>
            <col style={{ width: "19%" }} />
            <col style={{ width: "21%" }} />
            <col style={{ width: "28%" }} />
            <col style={{ width: "19%" }} />
            <col style={{ width: "13%" }} />
            <col className="workout-actions-col" />
          </colgroup>
          <thead>
            <tr>
              {COLUMNS.map(([field, label]) => (
                <th
                  key={field}
                  scope="col"
                  aria-sort={
                    sort.field === field
                      ? sort.direction === "asc"
                        ? "ascending"
                        : "descending"
                      : "none"
                  }
                >
                  <button
                    type="button"
                    onClick={() =>
                      setSort((old) => ({
                        field,
                        direction:
                          old.field === field
                            ? old.direction === "asc"
                              ? "desc"
                              : "asc"
                            : field === "registeredAt"
                              ? "desc"
                              : "asc",
                      }))
                    }
                  >
                    <span className="column-label">{label}</span>
                    <span className="sort-indicator" aria-hidden="true">
                      {sort.field === field ? (
                        sort.direction === "asc" ? (
                          <>&#9650;</>
                        ) : (
                          <>&#9660;</>
                        )
                      ) : (
                        <>&#9650; &#9660;</>
                      )}
                    </span>
                  </button>
                </th>
              ))}
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((entry) => (
              <tr
                key={`${entry.kind}:${entry.id}`}
                className={entry.kind === "user" && actionsEnabled ? "admin-user-row" : undefined}
                onClick={(event) => {
                  if (
                    entry.kind !== "user" ||
                    !actionsEnabled ||
                    event.defaultPrevented ||
                    (event.target instanceof Element &&
                      event.target.closest("button,a,[role='menuitem']"))
                  )
                    return;
                  setUsageTarget({
                    entry,
                    trigger: event.currentTarget.querySelector<HTMLButtonElement>(
                      ".admin-user-usage-trigger",
                    ),
                  });
                }}
              >
                <td>
                  {entry.username ? (
                    <button
                      type="button"
                      className="admin-user-usage-trigger"
                      aria-description={`View usage for @${entry.username}`}
                      aria-haspopup="dialog"
                      disabled={!actionsEnabled}
                      onClick={(event) => setUsageTarget({ entry, trigger: event.currentTarget })}
                    >
                      <CoverageCellValue text={entry.username} />
                    </button>
                  ) : (
                    <span className="workout-table-na">n/a</span>
                  )}
                </td>
                <td>
                  {entry.fullName ? (
                    <CoverageCellValue text={entry.fullName} />
                  ) : (
                    <span className="workout-table-na">n/a</span>
                  )}
                </td>
                <td>
                  <CoverageCellValue text={entry.email} />
                </td>
                <td>
                  {entry.registeredAt ? (
                    dateTimeFormatter("en-US", {
                      month: "short",
                      day: "numeric",
                      year: "numeric",
                    }).format(new Date(entry.registeredAt))
                  ) : (
                    <span className="workout-table-na">n/a</span>
                  )}
                </td>
                <td>
                  <span
                    className={`sync-status-text admin-user-status admin-user-status--${entry.state}`}
                  >
                    {STATES[entry.state]}
                  </span>
                </td>
                <td className="workout-actions-cell">
                  <UserActions
                    entry={entry}
                    enabled={actionsEnabled}
                    onUsage={(selected, returnFocus) =>
                      setUsageTarget({ entry: selected, trigger: returnFocus })
                    }
                    onInvitation={(selected, action) => {
                      if (invitationLock.current) return;
                      invitationLock.current = true;
                      setActionError("");
                      invitationAction.mutate({ entry: selected, action });
                    }}
                    onAccess={(entry, trigger) => {
                      access.reset();
                      accessFocus.current = trigger;
                      setAccessTarget({ entry, trigger, enabled: entry.state === "disabled" });
                    }}
                    onDelete={(entry, trigger) => {
                      remove.reset();
                      setDeleteConfirmation("");
                      deleteFocus.current = trigger;
                      setDeleteTarget(entry);
                    }}
                    onDeletion={(entry, trigger) => {
                      deleteFocus.current = trigger;
                      setDeletionTarget(entry);
                    }}
                    onRefresh={() => directory.refetch()}
                    onRevokeSessions={(entry, trigger) => {
                      if (revokeSessionsLock.current) return;
                      revokeSessionsLock.current = true;
                      revokeSessionsFocus.current = trigger;
                      setActionError("");
                      revokeSessions.mutate(entry);
                    }}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!items.length && (
          <p className="admin-users-message" role="status">
            {directory.isPending
              ? "Loading users..."
              : directory.isError
                ? "Users are unavailable."
                : "No users yet."}
          </p>
        )}
      </div>
      {actionError && (
        <p className="error-summary" role="alert">
          {actionError}
        </p>
      )}
      {directory.isError && (
        <div className="summary-query-error" role="alert">
          <span>Users could not be refreshed.</span>
          <button className="secondary" onClick={() => void directory.refetch()}>
            Retry
          </button>
        </div>
      )}
      <Dialog.Root
        open={open && active}
        onOpenChange={(next) => {
          if (!invite.isPending) setOpen(next);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            asChild
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (activeRef.current && trigger.current?.isConnected) trigger.current.focus();
            }}
          >
            <form
              className="dialog-content compact-dialog deletion-dialog single-deletion-dialog range-deletion-dialog admin-invite-dialog"
              onSubmit={submit}
              noValidate
            >
              <div className="dialog-heading">
                <Dialog.Title>Invite a user</Dialog.Title>
              </div>
              <div className="field range-confirmation">
                <label htmlFor="admin-invitation-email">Enter e-mail address</label>
                <input
                  id="admin-invitation-email"
                  type="email"
                  autoComplete="email"
                  required
                  maxLength={254}
                  value={email}
                  disabled={invite.isPending}
                  onChange={(event) => {
                    setEmail(event.target.value);
                    invite.reset();
                  }}
                />
              </div>
              {invite.isError && (
                <p className="error-summary" role="alert">
                  {invite.error instanceof ApiError && invite.error.status === 409
                    ? "This email already has a user or pending invitation."
                    : "Invitation email could not be sent. Check SMTP delivery, or close this dialog and use Resend invitation."}
                </p>
              )}
              <div className="dialog-actions">
                <Dialog.Close type="button" className="secondary" disabled={invite.isPending}>
                  Cancel
                </Dialog.Close>
                <button
                  type="submit"
                  className="primary"
                  disabled={invite.isPending || !validInvitationEmail(email)}
                >
                  Send
                </button>
              </div>
            </form>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <UsageDialog target={usageTarget} active={active} onClose={() => setUsageTarget(undefined)} />
      <Dialog.Root
        open={Boolean(deleteTarget) && active}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) {
            setDeleteTarget(undefined);
            setDeleteConfirmation("");
          }
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content compact-dialog deletion-dialog single-deletion-dialog"
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              deleteCancel.current?.focus();
            }}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (activeRef.current)
                (deleteFocus.current?.isConnected
                  ? deleteFocus.current
                  : document.getElementById(headingId)
                )?.focus();
            }}
          >
            <div className="dialog-heading">
              <Dialog.Title>Delete user?</Dialog.Title>
            </div>
            <Dialog.Description className="deletion-confirmation-message">
              Delete <strong>@{deleteTarget?.username}</strong>? The user will be signed out
              immediately, and running jobs will be canceled while a background task permanently
              deletes all private data. This cannot be undone.
            </Dialog.Description>
            <div className="field range-confirmation">
              <label htmlFor="user-delete-confirmation">
                Type <strong>DELETE</strong> to confirm.
              </label>
              <input
                id="user-delete-confirmation"
                value={deleteConfirmation}
                placeholder="DELETE"
                autoComplete="off"
                disabled={remove.isPending}
                onChange={(event) => setDeleteConfirmation(event.target.value)}
              />
            </div>
            {remove.isError && (
              <p className="error-summary" role="alert">
                {deletionIsBusy(remove.error)
                  ? "The account is still busy finishing a data update. Try again shortly; no page reload is needed."
                  : "Account deletion could not be started. Refresh the users list and try again."}
              </p>
            )}
            {remove.isPending && remove.failureCount > 0 && (
              <p role="status">
                Waiting for a data update to finish. Deletion will retry automatically.
              </p>
            )}
            <div className="dialog-actions">
              <Dialog.Close ref={deleteCancel} className="secondary" disabled={remove.isPending}>
                Cancel
              </Dialog.Close>
              <button
                type="button"
                className="danger-action"
                disabled={remove.isPending || deleteConfirmation !== "DELETE"}
                aria-busy={remove.isPending}
                onClick={() => {
                  if (!deleteTarget || deleteLock.current || deleteConfirmation !== "DELETE")
                    return;
                  deleteLock.current = true;
                  deletionRetryAllowed.current = true;
                  remove.mutate(deleteTarget);
                }}
              >
                Yes
              </button>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <Dialog.Root
        open={Boolean(deletionTarget) && active}
        onOpenChange={(open) => {
          if (!open) setDeletionTarget(undefined);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content compact-dialog"
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (activeRef.current)
                (deleteFocus.current?.isConnected
                  ? deleteFocus.current
                  : document.getElementById(headingId)
                )?.focus();
            }}
          >
            <div className="dialog-heading">
              <Dialog.Title>Account deletion</Dialog.Title>
              <Dialog.Close className="icon-button" aria-label="Close deletion progress">
                &times;
              </Dialog.Close>
            </div>
            <Dialog.Description>
              Deletion of <strong>@{deletionTarget?.username}</strong> cannot be undone.
            </Dialog.Description>
            {deletion.isError ? (
              <p role="alert">Deletion progress is unavailable.</p>
            ) : (
              <dl className="admin-usage-fields">
                <div>
                  <dt>Status</dt>
                  <dd>
                    {deletion.data?.phase === "completed"
                      ? "Completed"
                      : deletion.data?.phase === "purging"
                        ? "Purging private data"
                        : "Canceling jobs and cleaning up"}
                  </dd>
                </div>
                <div>
                  <dt>Rows removed</dt>
                  <dd>
                    {deletion.data
                      ? numberFormatter().format(deletion.data.rowsPurged)
                      : "Unavailable"}
                  </dd>
                </div>
              </dl>
            )}
            {deletion.data?.lastErrorCode && (
              <p className="error-summary" role="status">
                Deletion is waiting to resume. The account remains inaccessible.
              </p>
            )}
            {deletion.data?.phase !== "completed" && (
              <div className="dialog-actions">
                <Dialog.Close className="secondary">Close</Dialog.Close>
                <button
                  type="button"
                  className="primary"
                  disabled={remove.isPending}
                  onClick={() => {
                    if (deletionTarget && !deleteLock.current) {
                      deleteLock.current = true;
                      deletionRetryAllowed.current = true;
                      remove.mutate(deletionTarget);
                    }
                  }}
                >
                  Retry
                </button>
              </div>
            )}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <Dialog.Root
        open={Boolean(accessTarget) && active}
        onOpenChange={(open) => {
          if (!open && !access.isPending) setAccessTarget(undefined);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content
            className="dialog-content compact-dialog deletion-dialog single-deletion-dialog"
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              accessCancel.current?.focus();
            }}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (activeRef.current && accessFocus.current?.isConnected)
                accessFocus.current.focus();
            }}
          >
            <div className="dialog-heading">
              <Dialog.Title>{accessTarget?.enabled ? "Enable user" : "Disable user"}</Dialog.Title>
            </div>
            <Dialog.Description className="deletion-confirmation-message">
              {accessTarget?.enabled ? (
                <>
                  Enable <strong>@{accessTarget.entry.username}</strong>? The user will be able to
                  sign in again and automatic sync will resume on schedule. Revoked sessions and
                  canceled jobs will not be restored.
                </>
              ) : (
                <>
                  Disable <strong>@{accessTarget?.entry.username}</strong>? The user will be signed
                  out, automatic sync will stop, and queued or running jobs will be canceled. Their
                  data and settings are retained.
                </>
              )}
            </Dialog.Description>
            {access.isError && (
              <p className="error-summary" role="alert">
                {access.error instanceof ApiError && access.error.status === 409
                  ? "This account is being deleted and its access cannot be changed."
                  : "Account access could not be changed. Refresh the users list and try again."}
              </p>
            )}
            <div className="dialog-actions">
              <Dialog.Close ref={accessCancel} className="secondary" disabled={access.isPending}>
                Cancel
              </Dialog.Close>
              <button
                type="button"
                className={accessTarget?.enabled ? "primary" : "danger-action"}
                disabled={access.isPending}
                aria-busy={access.isPending}
                onClick={() => {
                  if (!accessTarget || accessLock.current) return;
                  accessLock.current = true;
                  access.mutate(accessTarget);
                }}
              >
                {accessTarget?.enabled ? "Enable" : "Disable"}
              </button>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </section>
  );
}

type Usage = {
  fullName: string;
  lastLoginAt: string | null;
  workoutCount: number;
  routeCount: number;
  lastWorkoutDate: string | null;
};
function UsageDialog({
  target,
  active,
  onClose,
}: {
  target?: { entry: DirectoryEntry; trigger: HTMLButtonElement | null };
  active: boolean;
  onClose: () => void;
}) {
  const returnFocus = useRef<HTMLButtonElement | null>(null);
  if (target) returnFocus.current = target.trigger;
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const query = useQuery({
    queryKey: ["admin-usage", target?.entry.id, timezone],
    queryFn: ({ signal }) =>
      api<Usage>(
        `/api/admin/users/${target!.entry.id}/usage?${new URLSearchParams({ timezone })}`,
        { signal },
      ),
    enabled: Boolean(target) && active,
    staleTime: 0,
    gcTime: 0,
  });
  const data = query.data;
  return (
    <Dialog.Root
      open={Boolean(target) && active}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content compact-dialog admin-usage-dialog"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (active && returnFocus.current?.isConnected) returnFocus.current.focus();
          }}
        >
          <div className="dialog-heading">
            <Dialog.Title>
              Usage by {data?.fullName ?? target?.entry.fullName ?? target?.entry.username}
            </Dialog.Title>
            <Dialog.Close className="icon-button" aria-label="Close usage">
              &times;
            </Dialog.Close>
          </div>
          {query.isError ? (
            <p className="error-summary" role="alert">
              Usage could not be loaded. Close this dialog and try again.
            </p>
          ) : (
            <dl className="admin-config admin-usage-fields" aria-busy={query.isFetching}>
              <div>
                <dt>Last login</dt>
                <dd>
                  {data
                    ? data.lastLoginAt
                      ? dateTimeFormatter("en-US", {
                          month: "short",
                          day: "numeric",
                          year: "numeric",
                          hour: "numeric",
                          minute: "2-digit",
                          hour12: true,
                          timeZone: timezone,
                        }).format(new Date(data.lastLoginAt))
                      : "n/a"
                    : "Loading..."}
                </dd>
              </div>
              <div>
                <dt>Workouts</dt>
                <dd>
                  {data
                    ? `${numberFormatter("en-US").format(data.workoutCount)} (${numberFormatter("en-US").format(data.routeCount)} ${data.routeCount === 1 ? "route" : "routes"})`
                    : "Loading..."}
                </dd>
              </div>
              <div>
                <dt>Last workout</dt>
                <dd>
                  {data
                    ? data.lastWorkoutDate
                      ? formatDateOnly(data.lastWorkoutDate)
                      : "n/a"
                    : "Loading..."}
                </dd>
              </div>
            </dl>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
