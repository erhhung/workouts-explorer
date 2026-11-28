import { useQuery } from "@tanstack/react-query";
import { useId, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { api } from "./api";
import { useTabActive } from "./RetainedTab";
import { dateTimeFormatter } from "./formatters";
import { Tooltip } from "./Tooltip";
import { formatEnglishDate, type DateFormat } from "./date";
import { useDateFormat } from "./DateFormat";

export interface StorageNode {
  pod: string;
  namespace: string;
  role: "primary" | "replica";
  pvcs: string[];
  capacityBytes: number;
  requestedCapacityBytes?: number | null;
  availableBytes: number;
  usedBytes: number;
  applicationBytes: number | null;
  sampledAt: string;
  issue?: string;
}
export interface StorageUsage {
  configured: boolean;
  checkedAt: string;
  databases: string[];
  nodes: StorageNode[];
}
const GiB = 2 ** 30;
const number = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
export const storageGiB = (bytes: number) => `${number.format(bytes / GiB)} GiB`;
export function storageMeasurementTime(value: string, order?: DateFormat) {
  const date = new Date(value);
  const day = formatEnglishDate(
    date,
    {
      year: "numeric",
      month: "numeric",
      day: "numeric",
    },
    order,
  );
  const time = dateTimeFormatter("en-US", {
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
    hour12: true,
  }).format(date);
  return `${day} at ${time}`;
}
export function storageTicks(capacity: number) {
  const maximum = capacity / GiB,
    target = Math.max(1, maximum / 10),
    power = 10 ** Math.floor(Math.log10(target));
  const step = Math.max(
    1,
    [1, 2, 5, 10]
      .map((value) => value * power)
      .reduce((best, value) => (Math.abs(value - target) < Math.abs(best - target) ? value : best)),
  );
  return Array.from({ length: Math.min(101, Math.floor(maximum / step) + 1) }, (_, i) => i * step);
}

function UsageBar({ node, active }: { node: StorageNode; active: boolean }) {
  const dateFormat = useDateFormat();
  const track = useRef<HTMLDivElement>(null),
    label = useRef<HTMLSpanElement>(null);
  const [inside, setInside] = useState(false);
  const fraction = Math.max(0, Math.min(1, node.usedBytes / node.capacityBytes));
  const requested =
    node.requestedCapacityBytes != null && node.requestedCapacityBytes > 0
      ? node.requestedCapacityBytes
      : null;
  const capacityLabel = requested ?? node.capacityBytes;
  const capacityHelp =
    requested != null
      ? `Requested PVC size: ${storageGiB(requested)}. Usable filesystem capacity: ${storageGiB(node.capacityBytes)}. Percentages use usable filesystem capacity.`
      : `Requested PVC size is unavailable. Showing usable filesystem capacity: ${storageGiB(node.capacityBytes)}.`;
  useLayoutEffect(() => {
    if (!active) return;
    let disposed = false;
    const measure = () => {
      if (disposed || !track.current || !label.current) return;
      const width = track.current.getBoundingClientRect().width;
      setInside(width > 0 && width * fraction >= label.current.getBoundingClientRect().width);
    };
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    if (track.current) observer?.observe(track.current);
    if (label.current) observer?.observe(label.current);
    window.addEventListener("resize", measure);
    document.fonts?.addEventListener("loadingdone", measure);
    void document.fonts?.ready.then(measure);
    measure();
    return () => {
      disposed = true;
      observer?.disconnect();
      window.removeEventListener("resize", measure);
      document.fonts?.removeEventListener("loadingdone", measure);
    };
  }, [active, fraction, node.usedBytes]);
  const ticks = storageTicks(capacityLabel);
  return (
    <div
      className="osm-storage-bar-chart"
      role="img"
      aria-label={`${storageGiB(node.usedBytes)} used of ${storageGiB(node.capacityBytes)} usable; ${storageGiB(node.availableBytes)} available${requested != null ? `; requested PVC size ${storageGiB(requested)}` : ""}`}
    >
      <div className="osm-storage-bar-area">
        <div className="osm-storage-bar-meta">
          <p className="osm-storage-measurement">
            <span className="osm-storage-foreground">
              {node.pvcs.length ? node.pvcs.join(", ") : "Filesystem"}
            </span>
            {" — measured on "}
            <time dateTime={node.sampledAt}>
              {storageMeasurementTime(node.sampledAt, dateFormat)}
            </time>
          </p>
          <p className="osm-storage-available">
            <span className="osm-storage-foreground">{storageGiB(node.availableBytes)}</span>{" "}
            available (
            <strong className="osm-storage-foreground">
              {number.format((node.availableBytes / node.capacityBytes) * 100)}%
            </strong>
            )
          </p>
        </div>
        <div className="osm-storage-bar-track" ref={track}>
          <div
            className="osm-storage-bar-fill"
            style={{
              width: `${fraction * 100}%`,
              backgroundSize: `${fraction > 0 ? 100 / fraction : 100}% 100%`,
            }}
          />
          <span
            ref={label}
            className={`osm-storage-bar-value${inside ? " is-inside" : ""}`}
            style={{ left: `${fraction * 100}%` }}
          >
            {storageGiB(node.usedBytes)} ({number.format(fraction * 100)}%)
          </span>
        </div>
        <Tooltip className="osm-storage-capacity" content={capacityHelp}>
          <strong>{storageGiB(capacityLabel)}</strong>
        </Tooltip>
        <div className="osm-storage-axis" aria-hidden="true">
          {ticks.map((value) => (
            <span key={value} style={{ left: `${((value * GiB) / capacityLabel) * 100}%` }}>
              {value} GiB
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

function UsagePie({ node }: { node: StorageNode }) {
  if (node.applicationBytes == null)
    return (
      <div
        className="osm-storage-pie-unavailable content-state-message content-state-message--centered"
        role="status"
      >
        {node.issue ?? "Database storage breakdown is unavailable."}
      </div>
    );
  const application =
    node.usedBytes > 0
      ? Math.max(0, Math.min(100, (node.applicationBytes / node.usedBytes) * 100))
      : 0;
  const other = node.usedBytes > 0 ? 100 - application : 0;
  return (
    <div className="osm-storage-pie-chart">
      <div
        className="osm-storage-pie"
        role="img"
        aria-label={`Used disk space: Workouts Explorer ${number.format(application)}%; Other applications ${number.format(other)}%`}
        style={{ "--storage-application-share": `${application}%` } as CSSProperties}
      />
      <ul className="osm-storage-legend">
        <li>
          <span className="osm-storage-key osm-storage-key--app" aria-hidden="true" />
          <span>
            Workouts Explorer<strong>{number.format(application)}%</strong>
          </span>
        </li>
        <li>
          <span className="osm-storage-key osm-storage-key--other" aria-hidden="true" />
          <span>
            Other applications<strong>{number.format(other)}%</strong>
          </span>
        </li>
      </ul>
    </div>
  );
}

function StorageSection({ node, active }: { node: StorageNode; active: boolean }) {
  const [expanded, setExpanded] = useState<boolean>();
  const open = expanded ?? node.role === "primary",
    id = useId();
  return (
    <section
      className="sync-card consistency-section osm-storage-section"
      aria-labelledby={`${id}-heading`}
    >
      <div className="sync-card-heading">
        <h2 id={`${id}-heading`}>
          <button
            type="button"
            className="consistency-disclosure"
            aria-expanded={open}
            aria-controls={`${id}-body`}
            onClick={() => setExpanded(!open)}
          >
            <span>
              {node.role === "primary" ? "Read/write primary" : "Read-only replica"}: {node.pod}
            </span>
            <svg viewBox="0 0 24 24" aria-hidden="true" className={open ? "is-open" : ""}>
              <path d="m5 9 7 7 7-7" fill="none" stroke="currentColor" strokeWidth="2" />
            </svg>
          </button>
        </h2>
      </div>
      <div className="consistency-section-body" id={`${id}-body`} hidden={!open} inert={!open}>
        <div className="osm-storage-charts">
          <UsageBar node={node} active={active && open} />
          <UsagePie node={node} />
        </div>
      </div>
    </section>
  );
}

export function AdminOSMStorage({ polling }: { polling: number }) {
  const active = useTabActive();
  const root = useRef<HTMLDivElement>(null),
    barMeasure = useRef<HTMLSpanElement>(null);
  const query = useQuery({
    queryKey: ["admin-osm-storage-usage"],
    queryFn: ({ signal }) => api<StorageUsage>("/api/admin/osm/storage-usage", { signal }),
    enabled: active,
    refetchInterval: active ? Math.max(10, polling) * 1000 : false,
    refetchIntervalInBackground: false,
  });
  useLayoutEffect(() => {
    if (!active || !query.data?.configured) return;
    let disposed = false;
    const measure = () => {
      if (disposed || !root.current || !barMeasure.current) return;
      // Match Consistency's measured date slot and its 75%-width bars.
      const width = Math.ceil(barMeasure.current.getBoundingClientRect().width);
      if (width) root.current.style.setProperty("--storage-bar-height", `${width * 0.75}px`);
    };
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    if (barMeasure.current) observer?.observe(barMeasure.current);
    document.fonts?.addEventListener("loadingdone", measure);
    void document.fonts?.ready.then(measure);
    measure();
    return () => {
      disposed = true;
      observer?.disconnect();
      document.fonts?.removeEventListener("loadingdone", measure);
    };
  }, [active, query.data?.configured]);
  if (query.data?.configured === false)
    return (
      <div className="osm-storage-unavailable">
        <div className="sync-feedback future-release-placeholder content-state-message" role="note">
          Storage usage information not available because
          <br />
          Prometheus metrics querying is not configured.
        </div>
      </div>
    );
  return (
    <div className="osm-storage-page" ref={root} aria-busy={query.isFetching}>
      <span
        className="consistency-date-measure osm-storage-bar-measure"
        ref={barMeasure}
        aria-hidden="true"
      >
        <span>May 0000</span>
        <span>12/31/00</span>
      </span>
      <header className="stats-heading">
        <h1>Storage Usage</h1>
      </header>
      {query.isPending && (
        <p className="content-state-message content-state-message--centered" role="status">
          Loading storage usage...
        </p>
      )}
      {query.isError && (
        <div className="map-banner stats-report-feedback" role="alert">
          <span>
            Storage usage could not be refreshed.{" "}
            {query.data ? "Showing the last available measurements." : "Please try again."}
          </span>
          <button className="secondary" onClick={() => void query.refetch()}>
            Retry
          </button>
        </div>
      )}
      {query.data && (
        <>
          <div className="consistency-sections">
            {query.data.nodes.map((node) => (
              <StorageSection key={`${node.namespace}/${node.pod}`} node={node} active={active} />
            ))}
          </div>
          <p className="osm-storage-footnote">
            <span className="osm-storage-foreground">Workouts Explorer</span> includes OSM database
            data and indexes, including partial builds;{" "}
            <span className="osm-storage-foreground">Other applications</span> includes other
            databases and shared PostgreSQL storage, such as WAL, temporary files and overhead.
          </p>
        </>
      )}
    </div>
  );
}
