import * as Dialog from "./Dialog";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { DialogTableViewport } from "./DialogTableViewport";
import { useOSMProgressFollow } from "./osmProgressFollow";
import { dateTimeFormatter } from "./formatters";
import { Tooltip } from "./Tooltip";
import { osmTaskDescriptions } from "./osmTaskDescriptions";
import type { RegionUpdate, RegionUpdateStep } from "./osmRegions";
import { elapsedTotal, updateElapsedLabel } from "./osmRegions";

const deletionDescriptions: Record<string, string> = {
  "detach-region":
    "Stop using this region's processed map data and identify the data to remove. Keep its processing history.",
  "reclaim-storage":
    "Remove the region's processed data and leftovers from unfinished processing jobs to free database space.",
  "verify-cleanup":
    "Check that the regional map data is removed and its database space reclaimed. Mark the region Available for download again.",
};

export function OSMTaskIcon({ state }: { state: string }) {
  if (state === "pending") return <span className="osm-update-icon-slot" aria-hidden="true" />;
  const running = state === "running",
    done = state === "completed",
    failed = state === "failed";
  return (
    <span className="osm-update-icon-slot">
      <span
        aria-label={state}
        className={`coverage-route-status coverage-route-status--${running ? "pending" : done ? "current" : failed ? "failed" : "unavailable"}`}
      >
        {!running && (
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <circle
              cx="8"
              cy="8"
              r="8"
              fill={
                done
                  ? "var(--status-success)"
                  : failed
                    ? "var(--status-error)"
                    : "var(--status-neutral)"
              }
            />
            <path
              d={
                done
                  ? "M4.5 8L7 10.5L11.5 5.5"
                  : state === "paused"
                    ? "M6 4.5V11.5M10 4.5V11.5"
                    : "M5 5L11 11M11 5L5 11"
              }
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        )}
      </span>
    </span>
  );
}
const timestamp = (value: string | null) =>
  value
    ? `${dateTimeFormatter("en-US", { year: "numeric", month: "numeric", day: "numeric" }).format(new Date(value))} ${dateTimeFormatter("en-US", { hour: "numeric", minute: "2-digit", second: "2-digit", hour12: true }).format(new Date(value))}`
    : undefined;
function Time({
  value,
  elapsed,
  finished = false,
}: {
  value: string | null;
  elapsed?: number | null;
  finished?: boolean;
}) {
  if (!value) return <span className="workout-table-na">n/a</span>;
  const time = <time dateTime={value}>{timestamp(value)}</time>;
  const duration = elapsedTotal(elapsed);
  if (!finished) return time;
  return (
    <Tooltip portal content={duration ?? "Elapsed duration was not recorded for this run."}>
      {time}
    </Tooltip>
  );
}
function Step({ step, state }: { step: RegionUpdateStep; state: string }) {
  return (
    <span className="osm-update-order">
      <span className="workout-value-lane" data-width-sample="58">
        <span className="workout-value-content">{step.order}</span>
      </span>
      <OSMTaskIcon state={state} />
    </span>
  );
}

function FailureMessage({ message, task }: { message: string; task: string }) {
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle");
  const prefix = `${task} failed: `;
  const detail = (message.startsWith(prefix) ? message.slice(prefix.length) : message).replace(
    "Review the updater logs for this run.",
    "Review the updater pod logs for this job.",
  );
  const download = detail.match(
    /^(Failed to download )(https:\/\/download\.geofabrik\.de\/[^\s|]+\.pbf)( \| .+)$/,
  );
  if (!download) return detail;
  const [, downloadPrefix, url, reason] = download;
  const feedback =
    copied === "copied"
      ? "Download URL copied"
      : copied === "failed"
        ? "Could not copy. Select the URL to copy it manually."
        : "Copy download URL";
  return (
    <p className="osm-update-download-error">
      {downloadPrefix}
      <Tooltip portal focusable={false} content={feedback}>
        <button
          type="button"
          className="osm-update-download-url"
          aria-label="Copy download URL"
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(url);
              setCopied("copied");
            } catch {
              setCopied("failed");
            }
          }}
        >
          {url}
        </button>
      </Tooltip>
      {reason}
      <span className="visually-hidden" aria-live="polite">
        {copied === "idle" ? "" : feedback}
      </span>
    </p>
  );
}

export function OSMRegionProgressDialog({
  jobId,
  onClose,
  onReturnFocus,
}: {
  jobId: string | undefined;
  onClose: () => void;
  onReturnFocus: () => void;
}) {
  const close = useRef<HTMLButtonElement>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const query = useQuery({
    queryKey: ["admin-osm-region-update", jobId],
    queryFn: ({ signal }) =>
      api<RegionUpdate>(`/api/admin/osm/region-updates/${jobId}`, { signal }),
    enabled: Boolean(jobId),
    refetchInterval: jobId ? 10000 : false,
    refetchIntervalInBackground: false,
  });
  const job = query.data,
    stopped = job && ["failed", "canceled", "paused"].includes(job.state);
  const stepCount = job?.steps?.length;
  const currentOrder =
    job?.state === "succeeded"
      ? stepCount || job.currentOrder || (job.operation === "delete" ? 3 : 58)
      : (job?.currentOrder ?? 0);
  const step = job?.steps?.find((step) => step.order === currentOrder);
  useOSMProgressFollow(scroll, jobId, currentOrder, stepCount, job?.state);
  const compact = stepCount !== undefined && stepCount <= 20;
  const legacyPreparation = stepCount === 57 && job?.steps?.[0]?.name === "download";
  const [now, setNow] = useState(Date.now);
  const ticking = Boolean(
    jobId &&
    (job?.executionSince || job?.elapsedSince) &&
    ["downloading", "processing", "deleting", "pausing", "canceling"].includes(job.state),
  );
  useEffect(() => {
    setNow(Date.now());
    if (!ticking) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [ticking, job?.elapsedSince, job?.executionSince]);
  const duration =
    currentOrder && job && job.state !== "waiting" ? updateElapsedLabel(job, now) : undefined;
  const queued = job?.state === "waiting" || (!currentOrder && !stopped);
  const waiting =
    job?.state === "waiting"
      ? job.startedAt
        ? "Waiting to resume processing"
        : "Waiting for updater capacity"
      : "Waiting to start";
  const visibleStep = step ?? job?.steps?.[0];
  const icon = queued
    ? "running"
    : !currentOrder && stopped
      ? job.state
      : (visibleStep?.state ?? "running");
  const progress = queued
    ? waiting
    : !currentOrder
      ? "No processing step started"
      : `${currentOrder} of ${stepCount ?? 58} \u2014 ${step?.name ?? "updating"}${step && step.repeated && step.batches > 1 && step.batch > 0 ? ` \u2014 batch ${step.batch} of ${step.batches}` : ""}`;
  return (
    <Dialog.Root
      open={Boolean(jobId)}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className={`dialog-content osm-update-progress-dialog${compact ? " osm-update-progress-dialog--compact" : ""}`}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            scroll.current?.focus({ preventScroll: true });
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onReturnFocus();
          }}
        >
          <div className="dialog-heading">
            <Dialog.Title>
              {job
                ? `${job.operation === "delete" ? (job.state === "succeeded" ? "Deleted" : "Deleting") : job.state === "succeeded" ? "Processed" : job.state === "waiting" && !job.startedAt ? "Downloading" : job.phase} ${job.regionId}`
                : "Region update progress"}
            </Dialog.Title>
            <Dialog.Description className="visually-hidden">
              Ordered updater tasks, batches and local timestamps.
            </Dialog.Description>
            <Dialog.Close ref={close} className="icon-button" aria-label="Close region progress">
              &times;
            </Dialog.Close>
          </div>
          <div className="osm-update-status-row">
            <div className="osm-update-current" role="status">
              <OSMTaskIcon state={icon} />
              <span>
                {progress}
                {duration && (
                  <span aria-live="off">
                    {" \u2014 "}
                    {duration}
                  </span>
                )}
              </span>
            </div>
            {job && job.operation !== "delete" && job.monitorDatabaseStorage !== undefined && (
              <p className="osm-update-storage-policy">
                Database storage monitoring{"\u00a0\u00a0"}
                <span
                  className={`eyebrow osm-update-policy-value osm-update-policy-value--${job.monitorDatabaseStorage ? "enabled" : "disabled"}`}
                >
                  {job.monitorDatabaseStorage ? "ENABLED" : "DISABLED"}
                </span>
              </p>
            )}
          </div>
          {stopped && (
            <div className="error-summary" role="alert">
              <FailureMessage
                key={job.error}
                task={job.currentOrder ? (visibleStep?.name ?? "") : "startup"}
                message={
                  job.error ??
                  (job.state === "paused" ? "This update is paused." : "The update has stopped.")
                }
              />
            </div>
          )}
          {query.isError && (
            <p className="osm-update-read-error" role="alert">
              Progress could not be refreshed.{" "}
              <button className="text-action" onClick={() => void query.refetch()}>
                Retry
              </button>
            </p>
          )}
          <DialogTableViewport
            stickyHeader
            scrollRef={scroll}
            scrollLabel="Region update steps"
            className="osm-update-progress-table-wrap"
            rowSlots={Math.min(stepCount ?? 20, 20)}
            rowClassName="osm-update-task-row"
            rowSample={<span className="osm-update-task-sample">Sample task</span>}
            busy={query.isFetching}
            message={query.isPending ? "Loading updater progress..." : undefined}
          >
            <table className="workout-table osm-update-progress-table">
              <colgroup>
                <col style={{ width: "10%" }} />
                <col style={{ width: "32%" }} />
                <col style={{ width: "14%" }} />
                <col style={{ width: "22%" }} />
                <col style={{ width: "22%" }} />
              </colgroup>
              <thead>
                <tr>
                  {["Step", "Task", "Batch", "Started", "Finished"].map((name) => (
                    <th key={name} scope="col">
                      {name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {job?.steps?.map((step) => (
                  <tr key={step.order} className="osm-update-task-row" data-task-order={step.order}>
                    <td>
                      <Step step={step} state={step.state} />
                    </td>
                    <td>
                      <Tooltip
                        portal
                        multiline
                        enabled={Boolean(
                          osmTaskDescriptions[step.name] ?? deletionDescriptions[step.name],
                        )}
                        content={
                          legacyPreparation && step.order === 1
                            ? "This earlier job records setup and downloading together. New jobs show preparation and downloading as separate steps."
                            : (osmTaskDescriptions[step.name] ??
                              deletionDescriptions[step.name] ??
                              "")
                        }
                      >
                        <span>{step.name}</span>
                      </Tooltip>
                    </td>
                    <td>
                      {step.repeated && step.batches > 1 && step.batch > 0 ? (
                        `${step.batch} of ${step.batches}`
                      ) : (
                        <span className="workout-table-na">n/a</span>
                      )}
                    </td>
                    <td>
                      <Time value={step.startedAt} />
                    </td>
                    <td>
                      <Time value={step.finishedAt} elapsed={step.elapsedTotal} finished />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </DialogTableViewport>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
