import * as Dialog from "./Dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { ApiError, api, type DataSync, type Session, type Source, type SourceType } from "./api";
import { CoverageCellValue } from "./CoverageTableCell";
import { PreferenceSelect } from "./PreferenceSelect";
import { Tooltip } from "./Tooltip";
import { autoSyncIntervalLabel } from "./formatters";

const TYPES: Array<{ value: SourceType; label: string }> = [
  { value: "health-auto-export-icloud", label: "Health Auto Export — iCloud Drive" },
  { value: "health-auto-export-local", label: "Health Auto Export — Server Path" },
];
const DEFAULT_NAMES: Record<SourceType, string> = {
  "health-auto-export-icloud": "Health Auto Export iCloud Drive",
  "health-auto-export-local": "Health Auto Export Server Path",
};
const STATUS: Record<Source["status"], string> = {
  connected: "Connected",
  "checking-connection": "Connecting",
  "connection-failed": "Connection failed",
};
const RETAINED = "existing value retained unless provided";
type SourceList = { items: Source[] };
type Draft = {
  key: number;
  mode: "new" | "edit";
  source?: Source;
  type: SourceType;
  displayName: string;
  autoSyncEnabled: boolean;
  path: string;
  appleId: string;
  password: string;
  cookies: string;
  trustToken: string;
};
type InputKey = "displayName" | "path" | "appleId" | "password" | "cookies" | "trustToken";
type Failure = { title: string; message: string; field?: InputKey };
type Column = "displayName" | "type" | "autoSyncEnabled" | "status";
const COLUMNS: Array<[Column, string]> = [
  ["displayName", "Name"],
  ["type", "Type"],
  ["autoSyncEnabled", "Auto Sync"],
  ["status", "Status"],
];

function connectionMessage(source: Source) {
  switch (source.statusCode) {
    case "source-auth-expired":
    case "source-auth-failed":
      return "iCloud authentication failed or expired. Update the values from a connected rclone session and try again.";
    case "source-adp-approval-required":
      return "Approve iCloud access outside the app, then update the values from rclone config show.";
    case "source-config-invalid":
      return "Check the source path and the values copied from rclone config show.";
    case "connection-check-canceled":
      return "The connection check was canceled. Try saving the source again.";
    case "source-check-timeout":
      return "The source did not respond in time. Check its connection settings and try again.";
    default:
      return source.type === "health-auto-export-local"
        ? "The source directory is not accessible. Check the path and worker access permissions."
        : "The iCloud Drive folder could not be reached. Check its path and connection settings.";
  }
}

function SourceActions({
  source,
  enabled,
  onEdit,
  onDelete,
}: {
  source: Source;
  enabled: boolean;
  onEdit: (source: Source, trigger: HTMLButtonElement | null) => void;
  onDelete: (source: Source, trigger: HTMLButtonElement | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!enabled) setOpen(false);
  }, [enabled]);
  return (
    <DropdownMenu.Root open={open && enabled} onOpenChange={setOpen}>
      <DropdownMenu.Trigger
        ref={trigger}
        className="workout-action-trigger"
        aria-label={`Actions for ${source.displayName}`}
        disabled={!enabled}
      >
        <span aria-hidden="true">...</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content workout-action-menu dialog-menu-content"
          align="end"
          sideOffset={6}
        >
          <DropdownMenu.Item onSelect={() => onEdit(source, trigger.current)}>
            Edit source
          </DropdownMenu.Item>
          <DropdownMenu.Separator />
          <DropdownMenu.Item
            className="danger-item"
            onSelect={() => onDelete(source, trigger.current)}
          >
            Delete source
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function DataSourcesDialog({
  session,
  onClose,
  onCloseFocus,
}: {
  session: Session;
  onClose: () => void;
  onCloseFocus: () => void;
}) {
  const client = useQueryClient();
  const id = useId();
  const key = ["sources", session.id, session.identity.id];
  const alive = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const lock = useRef(false);
  const sequence = useRef(0);
  const [busy, setBusy] = useState(false);
  const [draft, setDraft] = useState<Draft>();
  const [failure, setFailure] = useState<Failure>();
  const errorRef = useRef<HTMLDivElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  const editorTrigger = useRef<HTMLButtonElement | null>(null);
  const deleteTrigger = useRef<HTMLButtonElement | null>(null);
  const deleteCancel = useRef<HTMLButtonElement>(null);
  const [deleteTarget, setDeleteTarget] = useState<Source>();
  const [deleteError, setDeleteError] = useState("");
  const [sort, setSort] = useState<{ column: Column; ascending: boolean }>({
    column: "displayName",
    ascending: true,
  });
  const list = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api<SourceList>("/api/sources", { signal }),
    retry: false,
    refetchInterval: busy ? false : 30_000,
  });
  const schedule = useQuery({
    queryKey: ["data-sync"],
    queryFn: ({ signal }) => api<DataSync>("/api/data-sync", { signal }),
    enabled: Boolean(draft),
    retry: false,
  });
  const cadence = schedule.data
    ? autoSyncIntervalLabel(schedule.data.schedule.cadenceSeconds).toLowerCase()
    : schedule.isError
      ? "interval unavailable"
      : "loading interval...";
  const current = () => {
    const cached = client.getQueryData<Session | null>(["session"]);
    return (
      alive.current &&
      cached?.id === session.id &&
      cached.identity.id === session.identity.id &&
      cached.identity.role === "user"
    );
  };
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      controller.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (draft) nameRef.current?.focus();
  }, [draft?.key]);
  useEffect(() => {
    if (failure) errorRef.current?.focus();
  }, [failure]);

  function refresh() {
    if (!current()) return;
    void client.invalidateQueries({ queryKey: key, exact: true });
    void client.invalidateQueries({ queryKey: ["data-sync"] });
    void client.invalidateQueries({ queryKey: ["jobs"] });
  }
  async function accept(source: Source) {
    await client.cancelQueries({ queryKey: key, exact: true });
    if (!current()) return;
    client.setQueryData<SourceList>(key, (old) => ({
      items: [...(old?.items ?? []).filter((row) => row.id !== source.id), source],
    }));
  }
  function edit(source?: Source, trigger: HTMLButtonElement | null = addRef.current) {
    if (lock.current || !current()) return;
    editorTrigger.current = trigger;
    setFailure(undefined);
    setDraft({
      key: ++sequence.current,
      mode: source ? "edit" : "new",
      source,
      type: source?.type ?? TYPES[0].value,
      displayName: source?.displayName ?? DEFAULT_NAMES[TYPES[0].value],
      autoSyncEnabled: source?.autoSyncEnabled ?? false,
      path: source?.config.path ?? "",
      appleId: "",
      password: "",
      cookies: "",
      trustToken: "",
    });
  }
  function finishEditor() {
    setDraft(undefined);
    setFailure(undefined);
    requestAnimationFrame(() => {
      if (!alive.current) return;
      (editorTrigger.current?.isConnected ? editorTrigger.current : addRef.current)?.focus();
    });
  }
  function update(field: InputKey, value: string) {
    setDraft((old) => (old ? { ...old, [field]: value } : old));
    setFailure(undefined);
  }
  function validate(value: Draft): Failure | undefined {
    const invalid = (field: InputKey, message: string) => ({
      title: "Source could not be saved",
      message,
      field,
    });
    if (!value.displayName.trim() || [...value.displayName.trim()].length > 200)
      return invalid("displayName", "Enter a name containing 1–200 characters.");
    if (!value.path || value.path.length > 250)
      return invalid("path", "Enter a source path containing 1–250 characters.");
    if (value.type === "health-auto-export-icloud") {
      for (const [field, label, limit] of [
        ["appleId", "Apple ID", 320],
        ["password", "Password", 4096],
        ["cookies", "Cookies", 4000],
        ["trustToken", "Trust token", 250],
      ] as const) {
        if ((!value.source && !value[field]) || value[field].length > limit)
          return invalid(field, `Enter ${label} using no more than ${limit} characters.`);
      }
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft || lock.current || !current()) return;
    const invalid = validate(draft);
    if (invalid) {
      setFailure(invalid);
      return;
    }
    lock.current = true;
    setBusy(true);
    setFailure(undefined);
    const request = new AbortController();
    controller.current = request;
    let expired = false;
    const timer = setTimeout(() => {
      expired = true;
      request.abort();
    }, 30_000);
    let checking = false;
    try {
      const config: Record<string, string | number> = { version: 1, path: draft.path };
      if (draft.type === "health-auto-export-icloud")
        for (const field of ["appleId", "password", "cookies", "trustToken"] as const)
          if (draft[field]) config[field] = draft[field];
      const saved = await api<Source>(
        draft.source ? `/api/sources/${draft.source.id}?checkConnection=true` : "/api/sources",
        {
          method: draft.source ? "PATCH" : "POST",
          body: JSON.stringify({
            displayName: draft.displayName.trim(),
            autoSyncEnabled: draft.autoSyncEnabled,
            ...(draft.source ? {} : { type: draft.type }),
            config,
          }),
          signal: request.signal,
        },
        session.csrfToken,
      );
      if (!current() || request.signal.aborted) return;
      checking = true;
      // Adopt the persisted identity before polling: a failed check must never
      // turn another Save into a duplicate POST or retain submitted credentials.
      setDraft((old) =>
        old?.key === draft.key
          ? { ...old, source: saved, appleId: "", password: "", cookies: "", trustToken: "" }
          : old,
      );
      await accept(saved);
      let result = saved;
      while (result.status === "checking-connection") {
        await new Promise<void>((resolve, reject) => {
          const done = () => {
            request.signal.removeEventListener("abort", abort);
            resolve();
          };
          const timeout = setTimeout(done, 500);
          const abort = () => {
            clearTimeout(timeout);
            reject(new DOMException("Aborted", "AbortError"));
          };
          if (request.signal.aborted) abort();
          else request.signal.addEventListener("abort", abort, { once: true });
        });
        result = await api<Source>(`/api/sources/${saved.id}`, { signal: request.signal });
        if (!current() || request.signal.aborted) return;
        if (result.generation !== saved.generation) throw new Error("source-generation-changed");
      }
      await accept(result);
      if (!current() || request.signal.aborted) return;
      if (result.status === "connected") finishEditor();
      else setFailure({ title: "Connection check failed", message: connectionMessage(result) });
    } catch (error) {
      if (!current()) return;
      setFailure({
        title: checking || expired ? "Connection check failed" : "Source could not be saved",
        message: expired
          ? "The connection check did not finish within 30 seconds. Refresh the sources list or try again."
          : error instanceof Error && error.message === "source-generation-changed"
            ? "This source changed during the connection check. Edit its latest configuration and try again."
            : error instanceof ApiError && error.status === 409
              ? "That source name is already in use. Choose a different name."
              : error instanceof ApiError && error.status === 400
                ? "Check the source name, path and configuration values before saving again."
                : error instanceof ApiError && error.status === 404
                  ? "This source is no longer available. Refresh the sources list."
                  : "The source service is unavailable. Please try again later.",
      });
    } finally {
      clearTimeout(timer);
      lock.current = false;
      if (alive.current) setBusy(false);
      refresh();
    }
  }

  async function remove() {
    if (!deleteTarget || lock.current || !current()) return;
    const target = deleteTarget;
    lock.current = true;
    setBusy(true);
    setDeleteError("");
    const request = new AbortController();
    controller.current = request;
    const timer = setTimeout(() => request.abort(), 30_000);
    try {
      try {
        await api<void>(
          `/api/sources/${target.id}`,
          { method: "DELETE", signal: request.signal },
          session.csrfToken,
        );
      } catch (error) {
        if (!(error instanceof ApiError && error.status === 404)) throw error;
      }
      await client.cancelQueries({ queryKey: key, exact: true });
      if (!current() || request.signal.aborted) return;
      client.setQueryData<SourceList>(key, (old) => ({
        items: (old?.items ?? []).filter((row) => row.id !== target.id),
      }));
      if (draft?.source?.id === target.id) {
        setDraft(undefined);
        setFailure(undefined);
      }
      setDeleteTarget(undefined);
    } catch {
      if (current()) setDeleteError("The source could not be deleted. Please try again.");
    } finally {
      clearTimeout(timer);
      lock.current = false;
      if (alive.current) setBusy(false);
      refresh();
    }
  }

  function field(name: InputKey, label: string, maxLength: number, help?: string, secret = false) {
    if (!draft) return null;
    const fieldId = `${id}-${name}`;
    const fullWidth = name === "cookies" || name === "trustToken" || name === "path";
    const credential = name === "appleId" || name === "password";
    const controlProps = {
      id: fieldId,
      name:
        name === "appleId"
          ? "rclone-apple-id"
          : name === "password"
            ? "rclone-obscured-password"
            : name,
      className: name === "displayName" ? undefined : "source-config-input",
      value: draft[name],
      maxLength,
      autoComplete: name === "password" ? "new-password" : "off",
      "data-1p-ignore": credential ? "true" : undefined,
      "data-lpignore": credential ? "true" : undefined,
      "data-bwignore": credential ? "true" : undefined,
      spellCheck: false,
      required: !secret || !draft.source,
      disabled: busy,
      placeholder: secret && draft.source ? RETAINED : undefined,
      "aria-describedby": help ? `${fieldId}-help` : undefined,
      "aria-invalid": failure?.field === name || undefined,
      onChange: (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
        update(name, event.target.value),
    };
    const input =
      name === "cookies" ? (
        <textarea {...controlProps} rows={3} />
      ) : (
        <input
          {...controlProps}
          ref={name === "displayName" ? nameRef : undefined}
          type={name === "password" ? "password" : "text"}
        />
      );
    return (
      <div className={fullWidth ? "field field--wide" : "field"} key={name}>
        <label htmlFor={fieldId}>{label}</label>
        {help ? (
          <Tooltip
            id={`${fieldId}-help`}
            content={help}
            portal
            focusable={false}
            keyboardFocusOnly
            dismissOnPointerDown
            dismissOnPointerLeave
            followPointer
            multiline={help.includes("\n")}
            className="source-input-tooltip"
          >
            {input}
          </Tooltip>
        ) : (
          input
        )}
      </div>
    );
  }
  const items = [...(list.data?.items ?? [])].sort((a, b) => {
    const text = (source: Source) =>
      sort.column === "type"
        ? (TYPES.find((type) => type.value === source.type)?.label ?? source.type)
        : sort.column === "status"
          ? STATUS[source.status]
          : String(source[sort.column]);
    return (
      (sort.ascending ? 1 : -1) *
        text(a).localeCompare(text(b), "en-US", { sensitivity: "base" }) || a.id.localeCompare(b.id)
    );
  });
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && !lock.current) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content compact-dialog data-sources-dialog"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onCloseFocus();
          }}
        >
          <header className="dialog-heading preferences-header">
            <Dialog.Title>Data Sources</Dialog.Title>
            <Dialog.Close className="icon-button" aria-label="Close Data Sources" disabled={busy}>
              &times;
            </Dialog.Close>
          </header>
          <form
            className="preferences-form"
            name="rclone-source-configuration"
            autoComplete="off"
            onSubmit={save}
            noValidate
          >
            <div className="preferences-body">
              <section aria-labelledby={`${id}-sources`}>
                <div className="section-line">
                  <h3 id={`${id}-sources`}>Sources</h3>
                  <button
                    ref={addRef}
                    type="button"
                    className="text-action workout-clear-filters"
                    disabled={busy}
                    onClick={() => edit()}
                  >
                    Add a source
                  </button>
                </div>
                <div className="workout-table-wrap admin-users-wrap" aria-busy={list.isFetching}>
                  <table className="workout-table data-sources-table">
                    <colgroup>
                      <col style={{ width: "31.73%" }} />
                      <col style={{ width: "32.4%" }} />
                      <col style={{ width: "19.67%" }} />
                      <col style={{ width: "16.2%" }} />
                      <col className="workout-actions-col" />
                    </colgroup>
                    <thead>
                      <tr>
                        {COLUMNS.map(([column, label]) => (
                          <th
                            key={column}
                            scope="col"
                            aria-sort={
                              sort.column === column
                                ? sort.ascending
                                  ? "ascending"
                                  : "descending"
                                : "none"
                            }
                          >
                            <button
                              type="button"
                              onClick={() =>
                                setSort((old) => ({
                                  column,
                                  ascending: old.column === column ? !old.ascending : true,
                                }))
                              }
                            >
                              <span className="column-label">{label}</span>
                              <span className="sort-indicator" aria-hidden="true">
                                {sort.column === column ? (sort.ascending ? "▲" : "▼") : "▲ ▼"}
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
                      {items.map((source) => (
                        <tr key={source.id}>
                          <td>
                            <CoverageCellValue text={source.displayName} />
                          </td>
                          <td>
                            <CoverageCellValue
                              text={
                                TYPES.find((type) => type.value === source.type)?.label ??
                                source.type
                              }
                            />
                          </td>
                          <td>{source.autoSyncEnabled ? "Enabled" : "Disabled"}</td>
                          <td>
                            <span className={`sync-status-text source-status--${source.status}`}>
                              {STATUS[source.status]}
                            </span>
                          </td>
                          <td className="workout-actions-cell">
                            <SourceActions
                              source={source}
                              enabled={!busy && !list.isError}
                              onEdit={edit}
                              onDelete={(source, trigger) => {
                                deleteTrigger.current = trigger;
                                setDeleteError("");
                                setDeleteTarget(source);
                              }}
                            />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {!items.length && (
                    <p className="admin-users-message" role="status">
                      {list.isPending
                        ? "Loading sources..."
                        : list.isError
                          ? "Sources are unavailable."
                          : "No data sources configured."}
                    </p>
                  )}
                </div>
                {list.isError && (
                  <div className="summary-query-error" role="alert">
                    <span>Sources could not be refreshed.</span>
                    <button type="button" className="secondary" onClick={() => void list.refetch()}>
                      Retry
                    </button>
                  </div>
                )}
              </section>
              {draft && (
                <section className="data-source-editor" aria-labelledby={`${id}-editor`}>
                  <div className="section-line">
                    <h3 id={`${id}-editor`}>
                      {draft.mode === "new" ? "New source" : "Edit source"}
                    </h3>
                  </div>
                  <div className="settings-grid">
                    <PreferenceSelect
                      id={`${id}-type`}
                      name="sourceType"
                      label="Type"
                      defaultValue={draft.type}
                      value={draft.type}
                      options={TYPES}
                      disabled={busy || Boolean(draft.source)}
                      onValueChange={(type) =>
                        setDraft((old) =>
                          old && !old.source
                            ? {
                                ...old,
                                type: type as SourceType,
                                displayName:
                                  !old.displayName || old.displayName === DEFAULT_NAMES[old.type]
                                    ? DEFAULT_NAMES[type as SourceType]
                                    : old.displayName,
                                path: "",
                                appleId: "",
                                password: "",
                                cookies: "",
                                trustToken: "",
                              }
                            : old,
                        )
                      }
                    />
                    {field("displayName", "Name", 200)}
                    {field(
                      "path",
                      "Path",
                      250,
                      draft.type === "health-auto-export-icloud"
                        ? "Path on iCloud Drive containing HealthAutoExport-YYYY-MM-DD.json files"
                        : "Path in ingest worker container containing\nHealthAutoExport-YYYY-MM-DD.json files",
                    )}
                    {draft.type === "health-auto-export-icloud" && (
                      <>
                        {field(
                          "appleId",
                          "Apple ID",
                          320,
                          "Value of `apple_id` field from output of `rclone config show`",
                          true,
                        )}
                        {field(
                          "password",
                          "Password",
                          4096,
                          "Value of `password` field from output of `rclone config show`",
                          true,
                        )}
                        {field(
                          "cookies",
                          "Cookies",
                          4000,
                          "Value of `cookies` field from output of `rclone config show`",
                          true,
                        )}
                        {field(
                          "trustToken",
                          "Trust token",
                          250,
                          "Value of `trust_token` field from output of `rclone config show`",
                          true,
                        )}
                      </>
                    )}
                    <div className="field">
                      <label className="checkbox-option" htmlFor={`${id}-auto-sync`}>
                        <input
                          id={`${id}-auto-sync`}
                          type="checkbox"
                          name="autoSyncEnabled"
                          checked={draft.autoSyncEnabled}
                          disabled={busy}
                          onChange={(event) => {
                            const checked = event.target.checked;
                            setDraft((old) => (old ? { ...old, autoSyncEnabled: checked } : old));
                            setFailure(undefined);
                          }}
                        />
                        <span>Enable automated sync ({cadence})</span>
                      </label>
                      {schedule.isError && (
                        <button
                          type="button"
                          className="text-action"
                          onClick={() => void schedule.refetch()}
                        >
                          Retry interval
                        </button>
                      )}
                    </div>
                  </div>
                  {failure && (
                    <div ref={errorRef} className="error-summary" role="alert" tabIndex={-1}>
                      <strong>{failure.title}</strong>
                      <span>{failure.message}</span>
                    </div>
                  )}
                </section>
              )}
            </div>
            {draft && (
              <footer className="dialog-actions">
                <button
                  type="button"
                  className="secondary preferences-action"
                  disabled={busy}
                  onClick={finishEditor}
                >
                  Cancel
                </button>
                <button className="primary preferences-action" disabled={busy} aria-busy={busy}>
                  {busy ? "Checking..." : "Save"}
                </button>
              </footer>
            )}
          </form>
          <Dialog.Root
            open={Boolean(deleteTarget)}
            onOpenChange={(open) => {
              if (!open && !busy) setDeleteTarget(undefined);
            }}
          >
            <Dialog.Portal>
              <Dialog.Overlay className="dialog-overlay dialog-overlay--nested" />
              <Dialog.Content
                className="dialog-content compact-dialog deletion-dialog single-deletion-dialog"
                onOpenAutoFocus={(event) => {
                  event.preventDefault();
                  deleteCancel.current?.focus();
                }}
                onCloseAutoFocus={(event) => {
                  event.preventDefault();
                  if (alive.current)
                    (deleteTrigger.current?.isConnected
                      ? deleteTrigger.current
                      : addRef.current
                    )?.focus();
                }}
              >
                <div className="dialog-heading">
                  <Dialog.Title>Delete source</Dialog.Title>
                </div>
                <Dialog.Description className="deletion-confirmation-message">
                  Delete <strong>{deleteTarget?.displayName}</strong>? Its connection credentials
                  will be deleted and its active sync jobs canceled. Imported workouts will be
                  retained.
                </Dialog.Description>
                {deleteError && (
                  <p className="error-summary" role="alert">
                    {deleteError}
                  </p>
                )}
                <div className="dialog-actions">
                  <Dialog.Close ref={deleteCancel} className="secondary" disabled={busy}>
                    Cancel
                  </Dialog.Close>
                  <button
                    type="button"
                    className="danger-action"
                    disabled={busy}
                    onClick={() => void remove()}
                  >
                    Delete
                  </button>
                </div>
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
