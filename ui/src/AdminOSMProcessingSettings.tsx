import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { ApiError, api, type Session } from "./api";
import { useTabActive } from "./RetainedTab";

type Settings = {
  monitorDatabaseStorage: boolean;
  storageMetricsConfigured?: boolean;
  revision: number;
  updatedAt: string;
};
const key = ["admin-osm-processing-settings"] as const;

export function AdminOSMProcessingSettings({ session }: { session: Session }) {
  const active = useTabActive(),
    client = useQueryClient(),
    identity = useRef(session.id);
  identity.current = session.id;
  const [draft, setDraft] = useState<boolean>();
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api<Settings>("/api/admin/osm/processing-settings", { signal }),
    enabled: active,
  });
  const save = useMutation({
    mutationFn: ({
      monitor,
      revision,
    }: {
      monitor: boolean;
      revision: number;
      sessionID: string;
    }) =>
      api<Settings>("/api/admin/osm/processing-settings", {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrfToken },
        body: JSON.stringify({ monitorDatabaseStorage: monitor, revision }),
      }),
    onSuccess: (value, request) => {
      if (
        identity.current !== request.sessionID ||
        client.getQueryData<Session | null>(["admin-session"])?.id !== request.sessionID
      )
        return;
      client.setQueryData(key, value);
      setDraft(undefined);
    },
    onError: (error, request) => {
      if (client.getQueryData<Session | null>(["admin-session"])?.id !== request.sessionID) return;
      if (error instanceof ApiError && error.status === 409) void query.refetch();
    },
  });
  const configured = query.data?.storageMetricsConfigured !== false,
    value = configured && (draft ?? query.data?.monitorDatabaseStorage ?? true),
    dirty = query.data && value !== query.data.monitorDatabaseStorage;
  return (
    <div className="sync-card admin-section">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!query.data || !configured || !dirty || save.isPending) return;
          save.mutate({ monitor: value, revision: query.data.revision, sessionID: session.id });
        }}
      >
        <div className="field">
          <label className="checkbox-option">
            <input
              type="checkbox"
              checked={value}
              disabled={!query.data || !configured || save.isPending}
              onChange={(event) => {
                setDraft(event.target.checked);
                save.reset();
              }}
            />
            <span>Monitor available database storage</span>
          </label>
        </div>
        {query.data && !configured && (
          <p className="muted">Storage metrics are disabled or not configured in Helm.</p>
        )}
        <p className="muted">
          Check free database space during region processing. Turn this off to skip storage probes,
          metrics-settle waits and storage-triggered vacuuming; cleanup and required processing
          checks still run.
        </p>
        <p className="muted">
          Applies whenever a download or reprocessing job starts or resumes. Running jobs keep their
          current setting until the next start or resume.
        </p>
        {query.isPending && (
          <p className="content-state-message content-state-message--centered" role="status">
            Loading OSM processing settings...
          </p>
        )}
        {query.isError && (
          <div role="alert">
            <p className="content-state-message content-state-message--centered">
              OSM processing settings are unavailable.
            </p>
            <button type="button" className="secondary" onClick={() => void query.refetch()}>
              Retry
            </button>
          </div>
        )}
        {save.isError && (
          <p role="alert" className="error-summary">
            {save.error instanceof ApiError && save.error.status === 409
              ? "Settings changed elsewhere. Review the latest value and save again."
              : "Could not save OSM processing settings. Try again."}
          </p>
        )}
        <div className="dialog-actions">
          <button
            type="submit"
            className="primary preferences-action"
            disabled={!configured || !dirty || save.isPending}
            aria-busy={save.isPending}
          >
            {save.isPending ? "Saving..." : "Save"}
          </button>
        </div>
        {save.isSuccess && <p role="status">OSM processing settings saved.</p>}
      </form>
    </div>
  );
}
