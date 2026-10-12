// Deploy JSON and Helm JSON mirrors are generated from this single source.
const fs = require("node:fs");
const path = require("node:path");

const datasource = { type: "prometheus", uid: "prometheus" };
// Empty alternatives retain legacy Thanos series without resource labels.
const goLabels =
  'service_namespace=~"${service_namespace:regex}|",deployment_environment=~"${environment:regex}|"';
const scopes = {
  api: `job="otel-gateway-collector",exported_job=~"workouts-api|\${service_namespace:regex}/workouts-api",${goLabels}`,
  go: `job="otel-gateway-collector",exported_job=~"(\${service_namespace:regex}/)?workouts-(api|worker|coverage-worker)",${goLabels}`,
  coverage: `job="otel-gateway-collector",exported_job=~"workouts-coverage-worker|\${service_namespace:regex}/workouts-coverage-worker",${goLabels}`,
  ui: 'job="otel-gateway-collector",exported_job="${service_namespace}/workouts-ui",service_name="workouts-ui",service_namespace="$service_namespace",deployment_environment="$environment"',
  martin:
    'job="otel-gateway-collector",exported_job="${service_namespace}/workouts-tiles",service_name="workouts-tiles",service_namespace="$service_namespace",deployment_environment="$environment"',
  self: 'job="otel-gateway-collector",exported_job=~"${service_namespace:regex}/workouts-(ui|tiles)-collector",service_name=~"workouts-(ui|tiles)-collector",service_namespace="$service_namespace",deployment_environment="$environment"',
};
const apiRoutes = 'http_route=~"/api/.*"';
const martinTiles = 'method="GET",martin_route="/{source_ids}/{z}/{x}/{y}"';
const martinSuccess = `${martinTiles},status=~"200|204"`;
const poolGroups = "exported_job,db_client_connection_pool_name";
const dbGroups = "exported_job,pgx_operation_type";
const selfGroups = "service_name,service_instance_id";
const tileBoundary =
  "Three independent populations: API GET handler includes auth/validation and response handling; API forwarding starts before Martin and ends after body processing/write, excluding auth/capability renewal; Martin native measures successful backend GET tiles (200 or 204), excluding proxy and browser render. Never subtract or add p95s. 500 ms is a provisional performance guide, not an accepted SLO.";
const coverageCaveat =
  "Source-defined sparse Coverage instruments; all eight were absent in the 10/5/2026 six-hour audit. N/A is not zero or an empty backlog. Outcomes observe confirmed committed terminal transitions, not an exactly-once ledger; crashes, ambiguous commits and DB-only reconciliation are omitted. no_evidence can be valid; superseded/cancelled are not automatically failures.";
const timingsCaveat =
  "Queue age is creation-to-first-claim (not oldest backlog age); matcher measures Evaluate only; route measures postclaim attempts including cleanup, not durable completion; admission measures the polling loop. Histograms have no measurement dimensions. Sparse observations preserve gaps.";
const goScopeNote =
  "Namespace and Environment filter newly labeled Go API/workers as well as native nginx, Martin and collector self metrics. Legacy bare exported_job history retained in Thanos lacks namespace/environment labels and remains unscoped: its environment is unknowable from the series; only earlier xdev deployed metrics are known.";

function metric(name, scope, extra = "") {
  return `${name}{${scopes[scope]}${extra ? `,${extra}` : ""}}`;
}

function aggregate(expr, groups = "") {
  return groups ? `sum by (${groups}) (${expr})` : `sum(${expr})`;
}

function counter(name, scope, groups = "", extra = "", window = "$__rate_interval", fn = "rate") {
  return aggregate(`${fn}(${metric(name, scope, extra)}[${window}])`, groups);
}

function gauge(name, scope, groups = "", extra = "") {
  return aggregate(metric(name, scope, extra), groups);
}

// Filter the denominator with matching grouping, rather than inventing zero/perfect health.
function ratio(numerator, denominator, scale = 1) {
  return `${scale === 1 ? "" : `${scale} * `}(${numerator}) / (${denominator} > 0)`;
}

function mean(family, scope, groups = "", extra = "", window, fn) {
  return ratio(
    counter(`${family}_sum`, scope, groups, extra, window, fn),
    counter(`${family}_count`, scope, groups, extra, window, fn),
  );
}

function quantile(family, scope, groups = "", extra = "", window, fn) {
  const buckets = counter(
    `${family}_bucket`,
    scope,
    ["le", groups].filter(Boolean).join(","),
    extra,
    window,
    fn,
  );
  const observations = counter(`${family}_count`, scope, groups, extra, window, fn);
  return `histogram_quantile(0.95, ${buckets}) and on (${groups}) (${observations} > 0)`;
}

function poolMean(duration, count, window, fn) {
  return ratio(
    `(${counter(duration, "go", poolGroups, "", window, fn)}) / 1e9`,
    counter(count, "go", poolGroups, "", window, fn),
  );
}

function query(expr, legendFormat) {
  return { expr, legendFormat };
}

function variable(name, label, definition, value) {
  return {
    name,
    label,
    type: "query",
    datasource,
    definition,
    query: { query: definition, refId: `variable-${name}` },
    current: { selected: true, text: value, value },
    options: [{ selected: true, text: value, value }],
    multi: false,
    includeAll: false,
    refresh: 1,
    sort: 1,
    hide: 0,
    skipUrlSync: false,
    regex: "",
  };
}

function builder(kind) {
  const historical = kind === "historical";
  const dashboard = {
    id: null,
    uid: `workouts-explorer-${kind}`,
    title: `Workouts Explorer / ${historical ? "Historical" : "Operational"}`,
    description: `AUTOGENERATED by deploy/grafana/generate.cjs. Based on the 10/5/2026 live metric audit; no synthetic profile or private product data. ${goScopeNote}`,
    schemaVersion: 41,
    version: 1,
    editable: true,
    timezone: "America/Los_Angeles",
    tags: ["workouts-explorer", kind, "telemetry"],
    time: { from: historical ? "now-7d" : "now-1h", to: "now" },
    refresh: historical ? "5m" : "30s",
    timepicker: {
      refresh_intervals: ["30s", "1m", "5m", "15m", "1h"],
      time_options: historical ? ["1d", "2d", "7d", "14d", "30d"] : ["15m", "1h", "6h", "1d"],
    },
    annotations: { list: [] },
    links: [],
    templating: {
      list: [
        variable(
          "service_namespace",
          "Namespace",
          'label_values(nginx_requests_total{job="otel-gateway-collector",service_name="workouts-ui"}, service_namespace)',
          "workouts-explorer",
        ),
        variable(
          "environment",
          "Environment",
          `label_values(${metric("nginx_requests_total", "ui").replace(',deployment_environment="$environment"', "")}, deployment_environment)`,
          "xdev",
        ),
      ],
    },
    panels: [],
  };
  let y = 0;
  let id = 0;
  let slot = 0;
  let rowStart = 0;
  let columns = 3;
  let panelHeight = 8;
  const notes = [];
  function text(title, bullets) {
    notes.push({ title, content: bullets.map((bullet) => `- ${bullet}`).join("\n") });
  }
  function row(title, layout = {}) {
    y = Math.max(y, rowStart + Math.ceil(slot / columns) * panelHeight);
    dashboard.panels.push({
      id: ++id,
      type: "row",
      title,
      collapsed: false,
      panels: [],
      gridPos: { x: 0, y, w: 24, h: 1 },
    });
    y += 1;
    rowStart = y;
    slot = 0;
    columns = layout.columns ?? 3;
    panelHeight = layout.height ?? 8;
  }
  function panel(title, description, unit, queries, options = {}) {
    const type = options.stat ? "stat" : "timeseries";
    const defaults = {
      unit,
      noValue: "N/A",
      decimals: options.decimals ?? 2,
      color: { mode: "palette-classic" },
      mappings: [{ type: "special", options: { match: "nan", result: { text: "N/A" } } }],
      thresholds: {
        mode: "absolute",
        steps: [
          { color: "green", value: null },
          ...(options.latency ? [{ color: "orange", value: 0.5 }] : []),
        ],
      },
    };
    if (type === "timeseries") {
      defaults.custom = {
        drawStyle: "line",
        lineWidth: 2,
        fillOpacity: 7,
        showPoints: "auto",
        pointSize: 4,
        spanNulls: false,
        insertNulls: false,
        stacking: { mode: "none", group: "A" },
        axisLabel: options.latency ? "Seconds; 500 ms provisional guide, not SLO" : "",
        axisPlacement: "auto",
        scaleDistribution: { type: "linear" },
        hideFrom: { legend: false, tooltip: false, viz: false },
        thresholdsStyle: { mode: options.latency ? "line" : "off" },
      };
    }
    dashboard.panels.push({
      id: ++id,
      title,
      description,
      type,
      datasource,
      gridPos: {
        x: Math.floor(((slot % columns) * 24) / columns),
        y: rowStart + Math.floor(slot / columns) * panelHeight,
        w:
          Math.floor((((slot % columns) + 1) * 24) / columns) -
          Math.floor(((slot % columns) * 24) / columns),
        h: panelHeight,
      },
      interval: options.daily ? "1d" : "30s",
      maxDataPoints: options.daily ? 30 : 600,
      fieldConfig: { defaults, overrides: options.overrides || [] },
      options: options.stat
        ? {
            reduceOptions: { values: false, calcs: ["lastNotNull"], fields: "" },
            orientation: "auto",
            textMode: "auto",
            text: { titleSize: 14, valueSize: options.singleValue ? 36 : 18 },
            colorMode: "none",
            graphMode: "none",
            justifyMode: "auto",
          }
        : {
            legend: { displayMode: "table", placement: "bottom", calcs: ["lastNotNull"] },
            tooltip: { mode: "multi", sort: "desc" },
          },
      targets: queries.map((target, index) => ({
        ...target,
        refId: String.fromCharCode(65 + index),
        datasource,
        editorMode: "code",
        instant: Boolean(options.stat),
        range: !options.stat,
        format: "time_series",
        ...(options.daily ? { interval: "1d", intervalFactor: 1 } : {}),
      })),
    });
    slot += 1;
    y = Math.max(y, rowStart + Math.ceil(slot / columns) * panelHeight);
  }
  const buttons = ["operational", "historical"].map((view) => {
    const active = view === kind;
    const label = view === "operational" ? "Operational" : "Historical";
    const href = `/d/workouts-explorer-${view}/workouts-explorer-${view}?\${service_namespace:queryparam}&amp;\${environment:queryparam}`;
    const style = `display:inline-flex;align-items:center;justify-content:center;height:32px;box-sizing:border-box;padding:0 12px;border:1px solid currentColor;border-radius:4px;color:inherit;text-decoration:none;font-weight:${active ? "600" : "400"};background:${active ? "rgba(128,128,128,0.18)" : "transparent"};`;
    return `<a target="_self" href="${href}"${active ? ' aria-current="page"' : ""} style="${style}">${label}</a>`;
  });
  // Explicit same-tab anchors request full navigation, including the current view.
  dashboard.panels.push({
    id: ++id,
    type: "text",
    title: "",
    description:
      "Open Operational or Historical in the same tab, retaining Namespace and Environment selections. Each view uses its default time range.",
    transparent: true,
    gridPos: { x: 0, y: 0, w: 24, h: 2 },
    options: {
      mode: "html",
      content: `<div role="navigation" aria-label="Workouts Explorer dashboards" style="display:flex;align-items:center;gap:8px;height:32px;">${buttons.join("")}</div>`,
    },
  });
  y = 2;
  text("Scope and interpretation", [
    goScopeNote,
    "The gateway pod is not application origin. Collector self CPU/RSS and local scrape health do not represent application or Job health.",
    "Missing or sparse observations display N/A with gaps, not invented zeros. Worker HTTP measures probes, not ingest traffic.",
    "Dates use America/Los_Angeles. These views contain no accepted SLO, error budget, inventory exporter or private account/health data.",
  ]);
  text("Data availability: 30 days", [
    "Live retention updated 10/6/2026: Thanos raw samples are retained for 2 days.",
    "Five-minute downsampled data is retained for 14 days; hourly data for 30 days.",
    "Retention is an availability ceiling, not a guarantee of complete stored blocks or short-window latency detail. Verify datasource routing and query warnings when interpreting older data.",
    "Historical defaults to 7 days, with time-picker suggestions through 30 days. Longer manual selections may contain little or no retained data; no automatic range clamp is applied.",
    "Daily plots use rolling 24-hour increases evaluated at one-day steps, not exact Los Angeles calendar-day ledgers. Missing days remain gaps; a 14-day sample window does not guarantee completeness.",
  ]);
  function finish() {
    row("Notes and data availability");
    const notesRow = dashboard.panels.at(-1);
    notesRow.collapsed = true;
    notesRow.panels = notes.map(({ title, content }) => {
      const note = {
        id: ++id,
        type: "text",
        title,
        description: content,
        gridPos: { x: 0, y, w: 24, h: 6 },
        options: { mode: "markdown", content },
      };
      y += 6;
      return note;
    });
    return dashboard;
  }
  return { row, panel, text, finish };
}

function unitOverride(pattern, unit) {
  return {
    matcher: { id: "byRegexp", options: pattern },
    properties: [
      { id: "unit", value: unit },
      { id: "custom.axisPlacement", value: "right" },
      { id: "custom.axisLabel", value: "" },
      { id: "thresholds", value: { mode: "absolute", steps: [{ color: "green", value: null }] } },
      { id: "custom.thresholdsStyle", value: { mode: "off" } },
    ],
  };
}

function boundaries(window, fn) {
  return [
    query(
      quantile(
        "http_server_request_duration_seconds",
        "api",
        "",
        `${apiRoutes},http_request_method="GET"`,
        window,
        fn,
      ),
      "API handler GET p95",
    ),
    query(
      quantile("workouts_api_tile_duration_seconds", "api", "tile_kind", "", window, fn),
      "API forwarding {{tile_kind}} p95",
    ),
    query(
      quantile("martin_http_requests_duration_seconds", "martin", "", martinSuccess, window, fn),
      "Martin successful tile GET p95",
    ),
  ];
}

function operational() {
  const { row, panel, text, finish } = builder("operational");
  row("1 / API and UI traffic");
  panel(
    "API request rate by status",
    "API route templates only; excludes health probes. Observed HTTP attempts are not logical operation availability.",
    "reqps",
    [
      query(
        counter(
          "http_server_request_duration_seconds_count",
          "api",
          "http_response_status_code",
          apiRoutes,
        ),
        "HTTP {{http_response_status_code}}",
      ),
    ],
  );
  panel(
    "API 5xx percentage and GET latency (p95)",
    "5xx fraction is an observed attempt ratio, not an accepted availability SLO. No traffic or absent 5xx stays N/A. GET latency includes fast errors; routes are templates. 500 ms is provisional.",
    "s",
    [
      query(
        quantile(
          "http_server_request_duration_seconds",
          "api",
          "http_route",
          `${apiRoutes},http_request_method="GET"`,
        ),
        "GET {{http_route}} p95",
      ),
      query(
        ratio(
          counter(
            "http_server_request_duration_seconds_count",
            "api",
            "",
            `${apiRoutes},http_response_status_code=~"5.."`,
          ),
          counter("http_server_request_duration_seconds_count", "api", "", apiRoutes),
          100,
        ),
        "5xx percentage",
      ),
    ],
    { latency: true, overrides: [unitOverride("5xx percentage", "percent")] },
  );
  panel(
    "API mean body size",
    "Bytes actually read/written, including measured zero-size bodies. Sum/count gives mean bytes per observed HTTP request, not bandwidth.",
    "bytes",
    [
      query(mean("http_server_request_body_size_bytes", "api", "", apiRoutes), "Request mean"),
      query(mean("http_server_response_body_size_bytes", "api", "", apiRoutes), "Response mean"),
    ],
  );
  panel(
    "UI request rate",
    "Native stub_status requests include status/probe traffic. No route/status/latency or browser-error instrumentation exists.",
    "reqps",
    [query(counter("nginx_requests_total", "ui"), "UI requests including probes")],
  );
  panel(
    "UI connections by state",
    "active is TOTAL; reading/writing/waiting are its breakdown. Lines are not stacked or summed. Connections are not requests or users.",
    "short",
    [query(gauge("nginx_connections_current", "ui", "state"), "{{state}}")],
  );
  panel(
    "UI connection rate",
    "Accepted minus handled is a resource-limit connection signal, not HTTP errors. Real zero is retained; absent series are not filled.",
    "cps",
    [
      query(counter("nginx_connections_accepted_total", "ui"), "Accepted"),
      query(counter("nginx_connections_handled_total", "ui"), "Handled"),
      query(
        `(${counter("nginx_connections_accepted_total", "ui")}) - (${counter("nginx_connections_handled_total", "ui")})`,
        "Accepted minus handled",
      ),
    ],
  );

  row("2 / Tiles");
  panel(
    "API tile-forwarding outcome rate",
    "Write errors can have HTTP 200; use outcome, not just 5xx. Kinds route/coverage are bounded; no capability IDs or concrete coordinates.",
    "ops",
    [
      query(
        counter("workouts_api_tile_duration_seconds_count", "api", "tile_kind,outcome"),
        "{{tile_kind}} / {{outcome}}",
      ),
    ],
  );
  panel("API and Martin latency (p95)", tileBoundary, "s", boundaries(), {
    latency: true,
  });
  panel(
    "Martin successful tile request rate",
    "Native GET route template only; both 200 and valid-empty 204 are success. This is not the API forwarding population.",
    "reqps",
    [
      query(
        counter("martin_http_requests_total", "martin", "status", martinSuccess),
        "Native {{status}}",
      ),
    ],
  );
  panel(
    "Martin tile 5xx request rate",
    "No native 5xx combination was observed in the final audit; absent is N/A, not zero. Local scrape up is not whole tile availability.",
    "reqps",
    [
      query(
        counter("martin_http_requests_total", "martin", "status", `${martinTiles},status=~"5.."`),
        "Native {{status}}",
      ),
    ],
  );

  row("3 / Coverage");
  panel("Coverage timing (p95)", `${timingsCaveat} ${coverageCaveat}`, "s", [
    ...["queue_age", "matcher_duration", "route_duration", "admission_wait"].map((name) =>
      query(
        quantile(`coverage_${name}_seconds`, "coverage", "", "", "15m"),
        name.replaceAll("_", " "),
      ),
    ),
  ]);
  panel(
    "Coverage outcomes",
    coverageCaveat,
    "short",
    [
      query(
        counter(
          "coverage_route_outcomes_total",
          "coverage",
          "outcome,failure",
          "",
          "$__range",
          "increase",
        ),
        "{{outcome}} / {{failure}}",
      ),
    ],
    { stat: true },
  );
  panel(
    "Coverage control rate",
    "Admission denied counts polls, not distinct queued routes; lost renewals are rejected renewals, not necessarily incidents. Separate operation/result labels, never sum into failures.",
    "ops",
    [
      query(
        counter("coverage_control_calls_total", "coverage", "operation,result", "", "15m"),
        "{{operation}} / {{result}}",
      ),
    ],
  );
  panel(
    "Coverage retry and stale-rejection rate",
    "Retry kinds overlap and omit attempts failing before metadata load; do not sum as distinct jobs. Stale rejections are ownership-fenced terminal rejections, not completion.",
    "ops",
    [
      query(
        counter("coverage_retry_attempts_total", "coverage", "kind", "", "15m"),
        "Retry {{kind}}",
      ),
      query(
        counter("coverage_stale_rejections_total", "coverage", "operation", "", "15m"),
        "Stale {{operation}}",
      ),
    ],
  );

  row("4 / Database clients and pools");
  panel(
    "Database client operation rate",
    "Client operations, not server QPS or jobs; operations from both pools merge within each service. No pool dimension exists on DB duration/errors.",
    "ops",
    [
      query(
        counter("db_client_operation_duration_seconds_count", "go", dbGroups),
        "{{exported_job}} / {{pgx_operation_type}}",
      ),
    ],
  );
  panel(
    "Database client mean duration",
    "Sum/count mean only. Current seconds buckets overwhelmingly cover (0,5]; subsecond DB p95 is not meaningful and is intentionally omitted.",
    "s",
    [
      query(
        mean("db_client_operation_duration_seconds", "go", dbGroups),
        "{{exported_job}} / {{pgx_operation_type}} mean",
      ),
    ],
  );
  panel(
    "Database client error rate",
    "Errors exclude no-row results; batch callbacks can yield multiple observations. Not failed-job counts. Worker counters were absent in the audit, not measured zero.",
    "ops",
    [
      query(
        counter("db_client_operation_errors_total", "go", dbGroups),
        "{{exported_job}} / {{pgx_operation_type}}",
      ),
    ],
  );
  panel(
    "Pool connections and capacity",
    "Fixed pool names distinguish app/OSM/diagnostic pools. Total includes constructing + acquired + idle; independent lines are not stacked. Configured max is not an SLO.",
    "short",
    [
      ...["acquired", "idle", "constructing", "total", "max"].map((state) =>
        query(
          gauge(`pgxpool_${state}_connections`, "go", poolGroups),
          `${state} {{db_client_connection_pool_name}}`,
        ),
      ),
    ],
  );
  panel(
    "Pool contention and connection churn",
    "Empty acquires are successful acquires that waited; cancellations are not failed jobs. Destroy/open counters describe connection lifecycle.",
    "ops",
    [
      ...[
        "acquires",
        "empty_acquire",
        "canceled_acquires",
        "new_connections",
        "max_idle_destroys",
        "max_lifetime_destroys",
      ].map((name) =>
        query(
          counter(`pgxpool_${name}_total`, "go", poolGroups),
          `${name} {{db_client_connection_pool_name}}`,
        ),
      ),
    ],
  );
  panel(
    "Pool mean acquisition and wait time",
    "Duration counters are nanoseconds, converted with /1e9 before division. Means use successful acquires / successful empty acquires respectively; no histogram or pool-specific p95 exists.",
    "s",
    [
      query(
        poolMean("pgxpool_acquire_duration_nanoseconds_total", "pgxpool_acquires_total"),
        "Acquire {{db_client_connection_pool_name}}",
      ),
      query(
        poolMean(
          "pgxpool_empty_acquire_wait_time_nanoseconds_total",
          "pgxpool_empty_acquire_total",
        ),
        "Empty wait {{db_client_connection_pool_name}}",
      ),
    ],
  );

  row("5 / Go runtime and collectors");
  panel(
    "Go runtime memory",
    `Go stack/other memory and GC goal are not RSS/container working set. Finite runtime memory limit is optional and was absent in the audit. ${goScopeNote}`,
    "bytes",
    [
      query(
        gauge("go_memory_used_bytes", "go", "exported_job,go_memory_type"),
        "{{exported_job}} / {{go_memory_type}}",
      ),
      query(gauge("go_memory_gc_goal_bytes", "go", "exported_job"), "{{exported_job}} GC goal"),
      query(
        gauge("go_memory_limit_bytes", "go", "exported_job"),
        "{{exported_job}} finite runtime limit",
      ),
    ],
  );
  panel(
    "Go goroutines",
    "Runtime activity on API/general/Coverage workers is not evidence of owned job progress. No app CPU, uptime or GC pause instrument is installed.",
    "short",
    [query(gauge("go_goroutine_count", "go", "exported_job"), "{{exported_job}}")],
  );
  panel(
    "Go allocation rate",
    "Cumulative heap bytes and allocation counts are differentiated before aggregation. Positive allocation rates indicate producer activity, not completion or CPU usage.",
    "Bps",
    [
      query(
        counter("go_memory_allocated_bytes_total", "go", "exported_job"),
        "Bytes {{exported_job}}",
      ),
      query(
        counter("go_memory_allocations_total", "go", "exported_job"),
        "Allocations {{exported_job}}",
      ),
    ],
    { overrides: [unitOverride("^Allocations ", "ops")] },
  );
  panel(
    "Collector CPU usage",
    "Only UI/tiles sidecar collector processes, not nginx/Rust/Go app CPU. CPU-seconds rate gives used cores. Preserve origin service_instance_id, never gateway pod.",
    "cores",
    [
      query(
        counter("otelcol_process_cpu_seconds_total", "self", selfGroups),
        "{{service_name}} / {{service_instance_id}}",
      ),
    ],
  );
  panel(
    "Collector memory and allocation rate",
    "RSS has no _bytes suffix but is bytes; heap is not RSS. Allocation rate uses the audited runtime_alloc_bytes_total name, not the vendor fixture name.",
    "bytes",
    [
      query(
        gauge("otelcol_process_memory_rss", "self", selfGroups),
        "RSS {{service_name}} / {{service_instance_id}}",
      ),
      query(
        gauge("otelcol_process_runtime_heap_alloc_bytes", "self", selfGroups),
        "Heap {{service_name}} / {{service_instance_id}}",
      ),
      query(
        counter("otelcol_process_runtime_alloc_bytes_total", "self", selfGroups),
        "Allocation B/s {{service_name}} / {{service_instance_id}}",
      ),
    ],
    { overrides: [unitOverride("^Allocation B/s ", "Bps")] },
  );
  panel(
    "Collector metric flow",
    "Accepted means handoff, not delivery; sent counts successful point sends, not HTTP requests/jobs. Native and self pipelines share exporter otlp_grpc; counts need not match end to end.",
    "ops",
    [
      query(
        counter("otelcol_receiver_accepted_metric_points_total", "self", "service_name,receiver"),
        "Accepted {{service_name}} / {{receiver}}",
      ),
      query(
        counter(
          "otelcol_exporter_sent_metric_points_total",
          "self",
          "service_name,exporter",
          'exporter="otlp_grpc"',
        ),
        "Sent {{service_name}} / {{exporter}}",
      ),
    ],
  );
  panel(
    "Collector metric errors",
    "Receiver refused/failed are separate point signals, not distinct loss. Exporter send_failed/enqueue_failed are UNOBSERVED CANDIDATES, absent at source/host in the audit, not zero. Send attempts may retry; failed self-export can hide its own warnings. Pair with host probes/logs/gateway evidence.",
    "ops",
    [
      ...["refused", "failed"].map((name) =>
        query(
          counter(`otelcol_receiver_${name}_metric_points_total`, "self", "service_name,receiver"),
          `${name} {{service_name}} / {{receiver}}`,
        ),
      ),
      ...["send", "enqueue"].map((name) =>
        query(
          counter(
            `otelcol_exporter_${name}_failed_metric_points_total`,
            "self",
            "service_name,exporter",
            'exporter="otlp_grpc"',
          ),
          `Unobserved candidate ${name} {{service_name}}`,
        ),
      ),
    ],
  );
  panel(
    "Collector exporter queue",
    "Current exporter sending-queue size/capacity for otlp_grpc in configured queue-sizer units, not application backlog or a count of lost telemetry points. Keep per-origin identity. Queue size/capacity were actually 0/10 in the audit, not fabricated defaults.",
    "short",
    [
      query(
        gauge(
          "otelcol_exporter_queue_size",
          "self",
          `${selfGroups},exporter`,
          'exporter="otlp_grpc"',
        ),
        "Size {{service_name}} / {{service_instance_id}}",
      ),
      query(
        gauge(
          "otelcol_exporter_queue_capacity",
          "self",
          `${selfGroups},exporter`,
          'exporter="otlp_grpc"',
        ),
        "Capacity {{service_name}} / {{service_instance_id}}",
      ),
    ],
  );
  text("History limitations", [
    "No backlog/oldest-job/slot state exporter, owned ingest/scheduler/file outcomes, nginx latency/status/browser metrics, DB server exporter, OSM/hook Job metrics or authorized SQL inventory reader exists.",
    "No accepted two-week SLO, Pyrra resources, burn rates or error budgets are installed. The 500 ms latency guide is provisional, not an availability commitment.",
    "Host infrastructure mapping is not inferred from the gateway pod. Coverage timings and terminal outcomes are sparse observations, not queue state or an exactly-once ledger.",
    "No accounts, health values, coordinates, private capability IDs, SQL or raw errors are queried. Only bounded route templates and fixed pool names are used; scope variables are not access control.",
  ]);
  return finish();
}

function historical() {
  const { row, panel, text, finish } = builder("historical");
  const selected = (name, scope, groups = "", extra = "") =>
    counter(name, scope, groups, extra, "$__range", "increase");
  const daily = (name, scope, groups = "", extra = "") =>
    counter(name, scope, groups, extra, "1d", "increase");
  const stat = { stat: true };
  const singleStat = { stat: true, singleValue: true };
  const day = { daily: true };
  const estimates =
    "Selected-range increase estimates observed events with reset handling, not exact durable/calendar counts. Missing data is N/A. Retained data may be incomplete.";
  const dailyNote =
    "Rolling 1d increase per source series before grouping, evaluated at 1d steps (min interval 1d); not rates, cumulative-counter heights or exact timezone calendar-day ledgers. Missing days preserve gaps.";
  row("1 / API and UI traffic", { columns: 5, height: 4 });
  panel(
    "API requests",
    estimates,
    "short",
    [
      query(
        selected("http_server_request_duration_seconds_count", "api", "", apiRoutes),
        "API requests",
      ),
    ],
    singleStat,
  );
  panel(
    "API 5xx percentage",
    `${estimates} Not availability compliance or an accepted SLO; numerator is observed 5xx attempts. Zero traffic/absent numerator remains N/A. No fake perfect health.`,
    "percent",
    [
      query(
        ratio(
          selected(
            "http_server_request_duration_seconds_count",
            "api",
            "",
            `${apiRoutes},http_response_status_code=~"5.."`,
          ),
          selected("http_server_request_duration_seconds_count", "api", "", apiRoutes),
          100,
        ),
        "5xx percentage",
      ),
    ],
    singleStat,
  );
  panel(
    "UI requests",
    `${estimates} Native nginx includes internal status/probe traffic, not unique users or browser journeys.`,
    "short",
    [query(selected("nginx_requests_total", "ui"), "UI requests including probes")],
    singleStat,
  );
  panel(
    "API requests by status (daily)",
    dailyNote,
    "short",
    [
      query(
        daily(
          "http_server_request_duration_seconds_count",
          "api",
          "http_response_status_code",
          apiRoutes,
        ),
        "HTTP {{http_response_status_code}}",
      ),
    ],
    day,
  );
  panel(
    "UI requests and connection gap (daily)",
    `${dailyNote} Connections are not requests. Accepted-minus-handled is a resource-limit signal, not HTTP failure count; actual zero is valid.`,
    "short",
    [
      query(daily("nginx_requests_total", "ui"), "Requests including probes"),
      query(
        `(${daily("nginx_connections_accepted_total", "ui")}) - (${daily("nginx_connections_handled_total", "ui")})`,
        "Accepted minus handled connections",
      ),
    ],
    day,
  );

  row("2 / Tiles");
  panel(
    "API tile-forwarding outcomes",
    `${estimates} Write-error can be HTTP 200; retain outcome and kind. These are forwarding observations, not distinct logical tiles.`,
    "short",
    [
      query(
        selected("workouts_api_tile_duration_seconds_count", "api", "tile_kind,outcome"),
        "{{tile_kind}} / {{outcome}}",
      ),
    ],
    stat,
  );
  panel(
    "API and Martin latency (p95)",
    `${tileBoundary} Buckets are increased per series over the entire selected range, then aggregated BEFORE quantile; never average daily/per-pod p95s into a window p95. Downsampling may reduce detail.`,
    "s",
    boundaries("$__range", "increase"),
    stat,
  );
  panel(
    "API tile-forwarding outcomes (daily)",
    `${dailyNote} API forwarding is not full handler or browser render latency.`,
    "short",
    [
      query(
        daily("workouts_api_tile_duration_seconds_count", "api", "tile_kind,outcome"),
        "{{tile_kind}} / {{outcome}}",
      ),
    ],
    day,
  );
  panel(
    "Martin tile requests by status (daily)",
    `${dailyNote} Exact native GET template; 200 and 204 are successes. Other statuses remain independent. No observed 5xx series is not zero.`,
    "short",
    [
      query(
        daily("martin_http_requests_total", "martin", "status", martinTiles),
        "Native {{status}}",
      ),
    ],
    day,
  );
  panel(
    "API and Martin requests within 500ms",
    `${tileBoundary} Bucket/count fractions describe selected-range distributions, NOT SLO compliance, two-week commitments, burn rates or error budgets. No accepted targets. Denominators are filtered >0.`,
    "percent",
    [
      query(
        ratio(
          selected(
            "http_server_request_duration_seconds_bucket",
            "api",
            "",
            `${apiRoutes},http_request_method="GET",le="0.5"`,
          ),
          selected(
            "http_server_request_duration_seconds_count",
            "api",
            "",
            `${apiRoutes},http_request_method="GET"`,
          ),
          100,
        ),
        "API handler GET <=500ms",
      ),
      query(
        ratio(
          selected("workouts_api_tile_duration_seconds_bucket", "api", "tile_kind", 'le="0.5"'),
          selected("workouts_api_tile_duration_seconds_count", "api", "tile_kind"),
          100,
        ),
        "API forwarding {{tile_kind}} <=500ms",
      ),
      query(
        ratio(
          selected(
            "martin_http_requests_duration_seconds_bucket",
            "martin",
            "",
            `${martinSuccess},le="0.5"`,
          ),
          selected("martin_http_requests_duration_seconds_count", "martin", "", martinSuccess),
          100,
        ),
        "Martin successful GET <=500ms",
      ),
    ],
    stat,
  );

  row("3 / Coverage");
  panel(
    "Coverage outcomes",
    `${estimates} ${coverageCaveat}`,
    "short",
    [
      query(
        selected("coverage_route_outcomes_total", "coverage", "outcome,failure"),
        "{{outcome}} / {{failure}}",
      ),
    ],
    stat,
  );
  panel(
    "Coverage outcomes (daily)",
    `${dailyNote} ${coverageCaveat}`,
    "short",
    [
      query(
        daily("coverage_route_outcomes_total", "coverage", "outcome,failure"),
        "{{outcome}} / {{failure}}",
      ),
    ],
    day,
  );
  panel(
    "Coverage timing (p95)",
    `${timingsCaveat} ${coverageCaveat} Whole-window buckets are aggregated before quantile, not averages of daily p95.`,
    "s",
    [
      ...["queue_age", "matcher_duration", "route_duration", "admission_wait"].map((name) =>
        query(
          quantile(`coverage_${name}_seconds`, "coverage", "", "", "$__range", "increase"),
          name.replaceAll("_", " "),
        ),
      ),
    ],
    stat,
  );
  panel(
    "Coverage controls (daily)",
    `${dailyNote} Denials count admission polls, not waiting routes; lost renewal need not be an incident. Operations/results are bounded and not summed into distinct failures.`,
    "short",
    [
      query(
        daily("coverage_control_calls_total", "coverage", "operation,result"),
        "{{operation}} / {{result}}",
      ),
    ],
    day,
  );
  panel(
    "Coverage retries and stale rejections (daily)",
    `${dailyNote} Retry kinds overlap; do not sum as distinct jobs. Stale rejection is not durable completion.`,
    "short",
    [
      query(daily("coverage_retry_attempts_total", "coverage", "kind"), "Retry {{kind}}"),
      query(
        daily("coverage_stale_rejections_total", "coverage", "operation"),
        "Stale {{operation}}",
      ),
    ],
    day,
  );

  row("4 / Database clients and runtime");
  panel(
    "Database client operations and errors",
    `${estimates} DB operations merge both pools within each service; errors exclude no-row results and batch observations can repeat. Not server QPS or failed-job counts.`,
    "short",
    [
      query(
        selected("db_client_operation_duration_seconds_count", "go", dbGroups),
        "Operations {{exported_job}} / {{pgx_operation_type}}",
      ),
      query(
        selected("db_client_operation_errors_total", "go", dbGroups),
        "Errors {{exported_job}} / {{pgx_operation_type}}",
      ),
    ],
    stat,
  );
  panel(
    "Database client mean duration",
    "Whole-window sum/count supports mean. Current coarse seconds buckets make subsecond DB p95 meaningless; no percentile is invented. No pool label on DB operations.",
    "s",
    [
      query(
        mean("db_client_operation_duration_seconds", "go", dbGroups, "", "$__range", "increase"),
        "{{exported_job}} / {{pgx_operation_type}} mean",
      ),
    ],
    stat,
  );
  panel(
    "Pool mean acquisition and wait time (daily)",
    "Rolling 1d duration increases / event increases, 1d evaluation steps. Nanoseconds converted with /1e9. Means are not additive or pool p95; empty wait divides by successful empty acquires only.",
    "s",
    [
      query(
        poolMean(
          "pgxpool_acquire_duration_nanoseconds_total",
          "pgxpool_acquires_total",
          "1d",
          "increase",
        ),
        "Acquire {{db_client_connection_pool_name}}",
      ),
      query(
        poolMean(
          "pgxpool_empty_acquire_wait_time_nanoseconds_total",
          "pgxpool_empty_acquire_total",
          "1d",
          "increase",
        ),
        "Empty wait {{db_client_connection_pool_name}}",
      ),
    ],
    day,
  );
  panel(
    "Go runtime memory and goroutines",
    `Sampled state within the selected retained range, not daily event counts or exact peaks. Go runtime-managed memory is not RSS; no app CPU instrument exists. ${goScopeNote}`,
    "bytes",
    [
      query(
        gauge("go_memory_used_bytes", "go", "exported_job,go_memory_type"),
        "Memory {{exported_job}} / {{go_memory_type}}",
      ),
      query(gauge("go_goroutine_count", "go", "exported_job"), "Goroutines {{exported_job}}"),
    ],
    { overrides: [unitOverride("^Goroutines ", "short")] },
  );
  panel(
    "Collector CPU, memory and queue",
    "Only native sidecar collectors, not application processes. CPU-seconds rates give cores, RSS is bytes, queue is current telemetry queue entries. Sampled/downsampled state, not exact historical peaks or application availability.",
    "bytes",
    [
      query(
        gauge("otelcol_process_memory_rss", "self", selfGroups),
        "RSS {{service_name}} / {{service_instance_id}}",
      ),
      query(
        counter("otelcol_process_cpu_seconds_total", "self", selfGroups),
        "CPU {{service_name}} / {{service_instance_id}}",
      ),
      query(
        gauge(
          "otelcol_exporter_queue_size",
          "self",
          `${selfGroups},exporter`,
          'exporter="otlp_grpc"',
        ),
        "Queue {{service_name}} / {{service_instance_id}}",
      ),
    ],
    { overrides: [unitOverride("^CPU ", "cores"), unitOverride("^Queue ", "short")] },
  );
  text("History limitations", [
    "Selected-range stats estimate retained telemetry observations, not a product/SQL ledger. Whole-window percentiles aggregate buckets before quantile; daily or per-replica p95s cannot be added or averaged into a window p95.",
    "No fleet accounts/workouts/imported-file inventory, per-user health/activity graphs, coverage freshness/backlog, exact durable daily outcomes or OSM/hook Job history exists without an authorized aggregate exporter and event definitions.",
    "No synthetic acceptance CSV/profile is used. There are no year graphs or accepted two-week SLO/compliance/error-budget claims.",
    "Coverage outcome counters record transitions reported by workers, not a durable job ledger. Crashes, missed exports, ambiguous commits and database-only reconciliation can leave observations incomplete. Missing Coverage remains N/A, not an empty queue; retry kinds overlap and control denials count polls.",
    "No private IDs, health values, paths, coordinates, raw errors or SQL are queried. Route templates and fixed pool names remain bounded; scope selections do not authorize access to private data.",
  ]);
  return finish();
}

function artifacts() {
  const files = {};
  for (const [kind, dashboard] of [
    ["operational", operational()],
    ["historical", historical()],
  ]) {
    const json = `${JSON.stringify(dashboard, null, 2)}\n`;
    files[`${kind}.json`] = json;
    files[`../../helm/dashboards/${kind}.json`] = json;
  }
  return files;
}

function main(args) {
  if (args.length > 1 || (args.length && args[0] !== "--check")) {
    throw new Error("Usage: node deploy/grafana/generate.cjs [--check]");
  }
  let stale = false;
  for (const [name, content] of Object.entries(artifacts())) {
    const filename = path.join(__dirname, name);
    const displayName = path.relative(path.resolve(__dirname, "../.."), filename);
    if (args[0] === "--check") {
      if (!fs.existsSync(filename) || fs.readFileSync(filename, "utf8") !== content) {
        console.error(`Stale generated artifact: ${displayName}`);
        stale = true;
      }
    } else {
      fs.mkdirSync(path.dirname(filename), { recursive: true });
      fs.writeFileSync(filename, content);
      console.log(`Generated ${displayName}`);
    }
  }
  if (stale) process.exitCode = 1;
}

module.exports = { artifacts, operational, historical };
if (require.main === module) main(process.argv.slice(2));
