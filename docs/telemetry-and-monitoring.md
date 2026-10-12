# Telemetry And Monitoring

**SLO implementation and pipeline status updated 10/10/2026** (`America/Los_Angeles`).
The normal Helm chart implements four opt-in app SLOs. After xdev recreation,
Helm revision **3** is live-verified: all four source and mirrored objectives and their generated
**12 groups / 76 rules** are healthy, with advancing evaluations. Go origin labels
and all seven producer/self identities are verified. Authenticated owner smoke
checks returned nonempty route/Coverage tiles and Coverage-focus geometry. All
four SLO recording outputs are present. This is not evidence of two-week
compliance, target attainment, full-load acceptance or end-to-end alert delivery.

**Historical live audit: 10/5/2026.** Final snapshot: **10/5/2026 9:27:21 PM PDT**
(`America/Los_Angeles`), Helm revision **96**. All five application producers and
both collector self identities deliver metrics to the gateway and host Prometheus.
This inventory distinguishes source instrumentation, measured collection, durable
statistics and proposed work. Delivery/readiness is not application availability.
This documentation does not deploy collectors, monitors, dashboards, recording rules or SLOs, change
cluster configuration, or increase concurrency. Coverage, ingestion, OSM and
browser/map SLOs remain future work, not part of the four implemented objectives.

## Component Coverage

**All five long-running components have live-verified metrics delivery;
not all have owned lifecycle instrumentation.** API and both workers export OTLP;
UI and Martin use native collection through sidecars. Prometheus scrapes the
gateway, not the applications themselves.
Kubernetes infrastructure metrics cover containers but do not replace application
request, queue, matching, ingest or data-quality metrics.

| Component | Application Metrics Today | Collection | Main Gaps |
| --- | --- | --- | --- |
| API, `workouts-api` | HTTP, runtime, database operations/pools, private tile forwarding | TLS OTLP/gRPC to homelab gateway; gateway ServiceMonitor | Active requests, diagnostic admission/matching, auth/SMTP/notification outcomes, end-user operation/render latency |
| General worker, `workouts-worker` | Runtime, database operations/pools, HTTP health probes | Same OTLP path | No owned job/ingest/scheduler/file/deletion/freshness instruments |
| Coverage worker, `workouts-coverage-worker` | Eight Coverage instruments plus shared runtime/DB/health metrics | Same OTLP path | Backlog/oldest-job/slot gauges, reconciliation, complete durable completion/freshness statistics |
| UI/nginx, `workouts-ui` | Native nginx requests, accepted/handled connections and current connections by state | Contrib 0.156.0 nginx receiver at `127.0.0.1:8083/nginx_status` -> OTLP sidecar; delivery verified | Per-route/status/latency/cache and browser performance/errors |
| Martin tiles, `workouts-tiles` | Martin 1.15.0 native HTTP request counter and seconds histogram at `/_/metrics` | Contrib 0.156.0 Prometheus receiver at `127.0.0.1:3000` -> OTLP sidecar; delivery verified | Cache/pool-specific telemetry; API forwarding errors remain independently observable |
| OSM update/identity-evaluation Jobs | No active OTel provider or metrics endpoint | Logs, catalog/checkpoint/status records and Kubernetes Job state | Download/stage/batch durations, outcomes, freshness and durable progress exporter |
| Bootstrap/migration/role/timezone Jobs | No metrics provider/endpoint | Exit status, hook logs, Kubernetes Job state | Durable outcomes and short-lived-job export/flush strategy |
| Application PostgreSQL in xdev | No server exporter deployed by this chart | Application client metrics; host container/storage metrics | Application-DB server metrics/exporter explicitly deferred |
| OSM PostgreSQL HA in homelab | External PostgreSQL exporter ServiceMonitor exists | `postgresql/postgresql-postgresql`, port `metrics` | Server metric coverage/custom SQL queries are infrastructure-owned, not application instrumentation |

UI `/metrics` may be served by SPA fallback; HTML with HTTP 200 is not a
Prometheus endpoint. nginx's text `stub_status` endpoint is loopback-only, not
Prometheus exposition. Martin 1.15.0's standard image has native metrics enabled
without a metrics switch; the endpoint is **`/_/metrics`, not `/metrics`**.
The earlier help/config inspection and `/metrics` 404 probe were insufficient
and the earlier conclusion that Martin lacked metrics was false. This is not a
promise about custom Martin builds with the compile-time metrics feature removed.
The sidecar scrapes inside the pod, so no additional port-3000 NetworkPolicy
permission or public/API proxy route is needed.

No ServiceMonitor/PodMonitor exists in this application's Helm chart. The 10/5
xdev audit found neither resource type installed; that is not a claim that the
Pyrra CRD is missing. xdev now has the SLO custom-resource sync described
[below](#pyrra-application-slos). Future scrape resources must
be supported/synchronized explicitly or created against host-cluster Services;
do not assume a virtual monitor is automatically discovered by homelab.

## Current Collection

```text
API + general worker + Coverage worker
  -> TLS OTLP/gRPC, grpcs://otel-gateway-collector.observability.svc.cluster.local:4317
UI nginx status -> local nginx receiver ---------+
Martin /_/metrics -> local Prometheus receiver --+-> sidecar native pipeline -> same gateway
Sidecar :8888/metrics -> local self receiver ----+-> sidecar self pipeline -> same gateway
  (workouts-ui-collector / workouts-tiles-collector, not application runtime)

All producers
  -> homelab observability/otel-gateway (Collector 0.156.0)
  -> Prometheus exposition, :8889/metrics
  -> ServiceMonitor observability/otel-gateway-collector, every 30s
  -> homelab Prometheus -> Thanos -> Grafana / Pyrra

Gateway internal telemetry :8888/metrics
  -> ServiceMonitor observability/otel-gateway-monitoring-collector
```

`otel.endpoint` and the common Go configuration default to
`grpcs://otel-gateway-collector.observability.svc.cluster.local:4317`.
An unset `OTEL_EXPORTER_OTLP_ENDPOINT` uses that default; an explicitly empty
value disables provider/runtime setup on the three Go services. An empty Helm
endpoint also omits both sidecars and their ConfigMaps. Nonempty Go setup
initializes metrics **and traces**, with a 30-second metric export interval.
`otel.collector.enabled=false` disables sidecars independently of Go export.
Native/self receiver intervals are also 30s, with 5s scrape timeouts and a 5s
batch timeout. Sidecars collect metrics only. No Go application `/metrics`
handler is installed. Traces go from the gateway to Tempo; application logs are
not exported through this OTel setup.

Go gRPC TLS explicitly uses `x509.SystemCertPool`, including the mounted CA
selected by `SSL_CERT_FILE`, requires TLS 1.2 or newer, and verifies the server's
SAN/hostname. It does not fall back to plaintext or insecure verification on
failure, even if exporter insecure environment variables are set. Explicit
`grpc://` is allowed by the Go parser only for loopback tests. HTTP/HTTPS
compatibility remains for explicit overrides, including the local acceptance
collector at `http://acceptance-telemetry:4318`; it is not the default transport.
Sidecars use an explicit public `ca.crt` projection, not database credentials or
private keys, and run the digest-pinned Contrib 0.156.0 image.

### Private DNS And Reachability

The xdev CoreDNS custom server in `deploy/dev/otel-dns.yaml` listens
on port **1053** and forwards only `observability.svc.cluster.local` to homelab
DNS **`10.43.0.10`**. The live audit confirms both `workouts-otel.server` and
`fourteeners.local.server`, the main CoreDNS custom-server import, and the
`NodeHosts` entry with six records. Existing fourteeners resolution works. No
prior snapshot establishes byte-for-byte restoration of those entries.
UI and all three Go pods resolve the exact gateway FQDN to **`10.43.84.114`**,
matching the homelab Service IP. A direct Service port-forward handshake with the
application public CA and exact hostname passed trust/SAN validation, negotiating
TLS 1.3 and ALPN `h2`. Running Go endpoint/CA settings and delivered metrics
confirm the private gRPC path; no public status route or API proxy is involved.
The inspected gateway certificate expires **10/12/2026 5:34:21 AM PDT**; this
handshake snapshot does not certify future rotation or uninterrupted delivery.

For an operator-approved DNS update, merge just this key into the existing
ConfigMap; this documentation does not run the command:

```sh
kubectl --context=xdev patch cm coredns-custom -n kube-system --type=merge --patch-file deploy/dev/otel-dns.yaml
```

Do **not** server-side apply this partial ConfigMap: field ownership/removal can
drop unrelated custom DNS keys. The earlier apply removed other entries, which
were restored; inspect the full ConfigMap before and after any merge patch.

Gateway metric processors are memory limiter, filter, Kubernetes attributes and
batch. Prometheus exporter expiration is ten minutes. The existing filter targets
the older `http_server_duration_milliseconds` name; it does **not** filter this
application's current HTTP duration or probe metrics.

Homelab monitors carry `release: prometheus-stack`, matching the Prometheus
monitor selector. The application gateway monitor scrapes port `prometheus`;
the internal monitor scrapes port `monitoring`. Internal collector health and
accepted/refused/export-failed counters should be dashboarded separately from
application traffic; exact internal metric names depend on collector version.

### Labels And Identity

- At direct gateway exposition, `job` identifies the application. Homelab scrape
  sets `job="otel-gateway-collector"` and preserves the application under
  **`exported_job`**. New Go resource identity includes namespace, so all producers
  use namespace/service identity; the 10/5 Go audit used bare service names.
  For native/self sidecar data, resource processors overwrite receiver scrape
  identity with `service.name=workouts-ui|workouts-tiles` or
  `workouts-ui-collector|workouts-tiles-collector`. Gateway resource-to-telemetry
  conversion derives namespace/service `job` from that resource, not the local
  scrape jobs `martin`/`collector`; host scraping preserves it as `exported_job`.
  The chart/exporter contract is **namespace-prefixed** for Go too, live-verified
  with origin aliases at revision **104**:

| Producer | Host `exported_job` With Current Identity |
| --- | --- |
| API | `workouts-explorer/workouts-api` |
| General worker | `workouts-explorer/workouts-worker` |
| Coverage worker | `workouts-explorer/workouts-coverage-worker` |
| UI native | `workouts-explorer/workouts-ui` |
| Martin native | `workouts-explorer/workouts-tiles` |
| UI self | `workouts-explorer/workouts-ui-collector` |
| Tiles self | `workouts-explorer/workouts-tiles-collector` |

- Metric relabeling drops `namespace`, `service`, `instance`, `container`,
  `endpoint` and `otel_scope_schema_url`. The remaining `pod` is the **collector
  pod**, not the originating application pod. Do not use it for app replicas.
- Scope identity remains `otel_scope_name`/`otel_scope_version`. SDK attributes
  translate to underscore labels: `http.route` -> `http_route`,
  `http.response.status_code` -> `http_response_status_code`,
  `db.client.connection.pool.name` -> `db_client_connection_pool_name`.
- Both sidecars and the Go metric exporter project an allowlist onto **datapoints**:
  `service_name`, `service_namespace`, `service_instance_id` (pod UID),
  `deployment_environment`.
  Resources are `service.name`, `service.namespace`, `service.instance.id` and
  `deployment.environment.name`; chart values select namespace/environment.
  Chart environment defaults to `development`; the audited override is **`xdev`**,
  with **`service_namespace="workouts-explorer"`**. At the 10/5 audit, all actual
  native/self metric points retained these labels and the producer pod UID after
  host relabeling.
  Only one producer instance per identity was observed; this is not broad
  multi-replica validation. Host `exported_instance` is also present, but the
  gateway `pod` must not be used as originating application identity.
- Martin's native `endpoint` is copied to **`martin_route`** and removed by the
  local receiver before the gateway. This retains the template route despite the
  homelab monitor's `endpoint` label drop; `method` and `status` remain native
  labels, not Go HTTP semantic-convention names.
- Go setup reads standard `OTEL_RESOURCE_ATTRIBUTES`; Helm supplies
  `service.namespace` and `deployment.environment.name` from chart values, and
  `service.instance.id` from the downward-API `POD_UID`.
  `internal/telemetry/metrics.go` projects only
  the four aliases at export for shared/library and application metric points,
  on both HTTP and gRPC paths. The revision **104** rollout verified those aliases
  and originating Go pod UIDs; the 10/5 Go series lacked them. Distinct producer
  counters must not collide or overwrite. Do not enable blanket resource-to-point
  copying or use the gateway pod as origin identity.
- Retained bare Go `exported_job` history is preserved only by Grafana's legacy
  regex alternatives, not by SLI selectors. Its namespace/environment cannot be
  recovered from those series. New labels are not historical backfill; do not
  attribute unlabeled history to a selected namespace/environment or relabel it
  into the new scoped SLOs.

Generated **`target_info` is an exception**, not an application/native datapoint:
it carries resource key `deployment_environment_name="xdev"` and exported scrape
identity, not the four projected point aliases. Exclude it from datapoint-label
assertions; do not select it with `service_name` or `deployment_environment`.

OTLP applications and these sidecars do not need their own ServiceMonitor. No
additional Prometheus scrape or monitoring CRD is required for this path.
Avoid collecting the same instruments through both OTLP and direct scraping.
Short-lived Jobs need explicit final flush/shutdown or a durable state reader;
scraping may miss their entire lifetime. Do not add Pushgateway by default.

## Two Dashboard Models

| Model | Questions | Sources | Semantics |
| --- | --- | --- | --- |
| Live operational | Is traffic healthy? Is work progressing? Are pools/CPU/storage saturated? | OTel event metrics, continuously refreshed state gauges, Kubernetes/exporter metrics | Seconds/minutes, rates, percentiles, current gauges, timestamps, errors and burn rates |
| Aggregated/historical | How much work completed daily? How did reliability/capacity change? How many records/results exist? | Retained Prometheus/Thanos series plus authorized durable SQL aggregates | Hours/days/weeks; distinguish events over time, current inventory and exact ledger facts |

Historical is not a different wire protocol. Prometheus counters/histograms
retain operational history; SQL stores authoritative product/job history. They
answer different questions and must not be presented as interchangeable.

- `increase(counter[1d])` estimates observed events, handling resets; it does not
  reconstruct unexported events, ambiguous commits or missed short-lived jobs.
- An inventory gauge can decrease on deletion and must not be treated as an
  ever-created counter. Its history is sampled state, not every intervening event.
- Reingesting the same workout is not another new workout. Distinguish created,
  updated, unchanged, attempts, retries and distinct durable results.
- Histogram p95 is not additive. Aggregate buckets before quantile calculation;
  do not average per-pod/day p95 values into a fleet/month p95.
- Exact dates, completeness, attribution and corrected/deleted data require SQL
  semantics. Current Coverage explorer counts eligible workouts/matches; it is
  not simply a read of every daily rollup table.

### Historical Retention

Current homelab retention, verified **10/6/2026**: Prometheus local retention
remains **6h**; Thanos compactor retention is **2d raw / 14d five-minute / 30d hourly**.
Pyrra's frontend queries the TLS Thanos query frontend and links Grafana datasource ID
`prometheus`. Verify actual stored blocks and Grafana datasource routing before
promising that a particular historical window is complete.

This currently does **not** provide a year of time-series history. Longer
historical dashboard periods require a deliberate retention/storage decision;
SQL may retain longer facts, but their pruning/deletion semantics also need policy.
Downsampled data cannot promise raw short-window latency resolution.
The 30d Thanos ceiling improves historical dashboard availability, but does not
create missing two-week application history. The verified standalone Thanos
Ruler pipeline now evaluates Pyrra rules against Thanos rather than 6h local
Prometheus history; the [SLO preconditions](#preconditions) still apply.

## Implemented Metric Inventory

Pinned Go dependencies: OTel SDK/exporters 1.44.0, `otelhttp` 0.69.0, runtime
instrumentation 0.66.0, `otelpgx` 0.11.1. The tables describe these versions,
not every instrument named in semantic-convention definitions.

The **three Go services** have **35 normal instrument names** in their union:
nine owned by the application and 26 shared/library instruments. This is not
35 per service or the total for all five components. Martin adds two native HTTP
families, nginx adds four native instruments, and the two sidecars add collector
self-telemetry independently. Registration is not evidence that every name or
label combination has appeared in production.

Prometheus histogram families have `_bucket`, `_sum` and `_count` series;
monotonic counters usually add `_total`. Tables show explicit SDK units where
declared and interpreted display units for counts/connections. `otelpgx` declares
no SDK unit for operation errors or eleven non-duration pool instruments; its
two duration counters declare `ns`. Runtime goroutine count declares
`{goroutine}`. Separate Prometheus metadata unit fields may be empty. Runtime/pool observable
up/down counters translate to Prometheus gauges.

### API HTTP

API traffic and worker **health probes only** use scope
`go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`.

| OTel Name | Prometheus Family | Type / Unit | Recorded Value |
| --- | --- | --- | --- |
| `http.server.request.duration` | `http_server_request_duration_seconds` | Histogram, seconds | Instrumented handler duration |
| `http.server.request.body.size` | `http_server_request_body_size_bytes` | Histogram, bytes | Bytes actually read, including zero |
| `http.server.response.body.size` | `http_server_response_body_size_bytes` | Histogram, bytes | Bytes written, including zero |

Dimensions are method, scheme, server address/port, protocol, status and route
pattern where known. Live route values are templates, including
`/api/map-selections/{mapSelectionId}/coverage-tiles/{generation}/{z}/{x}/{y}.pbf`.
They are not concrete IDs/coordinates. Some unmatched/early failures may lack
`http_route`. Health metrics remain emitted even when successful probe logs are
suppressed. **No active-request metric is implemented.**

Duration buckets in seconds:
`.005,.01,.025,.05,.075,.1,.25,.5,.75,1,2.5,5,7.5,10,+Inf`.
Body-size histograms currently use SDK default buckets.

### API Tiles

Scope `github.com/erhhung/workouts-explorer/api`:

| OTel Name | Prometheus Family | Type / Unit | Dimensions |
| --- | --- | --- | --- |
| `workouts.api.tile.duration` | `workouts_api_tile_duration_seconds` | Histogram, seconds | `tile_kind`, `outcome`, `http_response_status_code` |

Kinds: `route`, `coverage`. Outcomes: `success`, `upstream_error`,
`invalid_response`, `body_error`, `too_large`, `write_error`. Status is 200 for
success/write-error, 502 for the other failures; upstream 204 becomes API 200.
A write failure can therefore occur with status 200: use `outcome`, not only 5xx.
Timing starts just before Martin forwarding and ends after body processing/write.
Authentication, validation and capability renewal are outside this boundary;
it is neither full logical tile availability nor browser-render latency.
Buckets: `.01,.05,.1,.25,.5,1,2.5,5,10,+Inf` seconds.

### Coverage Worker

Scope `github.com/erhhung/workouts-explorer/worker/coverage`:

| OTel Name | Expected Prometheus Family | Type / Unit | Meaning |
| --- | --- | --- | --- |
| `coverage.queue.age` | `coverage_queue_age_seconds` | Histogram, seconds | Creation-to-first-claim, during fenced metadata load; excludes recovered attempts; clock skew clamps to zero |
| `coverage.matcher.duration` | `coverage_matcher_duration_seconds` | Histogram, seconds | Actual Evaluate call, including failed calls; not snapshot/preparation/persistence |
| `coverage.route.duration` | `coverage_route_duration_seconds` | Histogram, seconds | Every observed postclaim attempt, including admission/cleanup |
| `coverage.admission.wait` | `coverage_admission_wait_seconds` | Histogram, seconds | Admission loop until acquire, error or interruption |
| `coverage.route.outcomes` | `coverage_route_outcomes_total` | Counter, events | Confirmed committed terminal transitions |
| `coverage.control.calls` | `coverage_control_calls_total` | Counter, events | Admission/heartbeat/release results and rollback errors |
| `coverage.retry.attempts` | `coverage_retry_attempts_total` | Counter, events | Recovery, explicit retry lineage and timeout-backoff observations |
| `coverage.stale.rejections` | `coverage_stale_rejections_total` | Counter, events | Ownership-fenced terminal rejections |

The four timing histograms have no measurement dimensions and boundaries
`.01,.1,.5,1,5,10,30,60,180,300,600,1800,3600,+Inf` seconds.
Counter dimension domains are bounded:

| Counter | Dimensions / Values |
| --- | --- |
| Outcomes | `outcome=applied\|no_evidence\|superseded\|failed\|cancelled`; `failure=none\|timeout\|limit\|invalid\|route_read\|persist\|osm\|other` |
| Controls | `operation=admission\|heartbeat\|release\|rollback`; `result=acquired\|denied\|renewed\|lost\|released\|not_found\|error\|timeout\|cancelled` |
| Retry attempts | `kind=recovery\|retry\|timeout_backoff` |
| Stale rejections | `operation=persist\|fail\|supersede\|cancel` |

Do not add account/job/workout/worker/region IDs or raw errors as dimensions.
Important limitations: retry kinds overlap; denial counts polling decisions;
lost renewal is a rejected renewal, not necessarily an incident; no-evidence
can be a valid success; supersession/cancellation are not automatically failures.
Outcome observations are not an exactly-once ledger and omit crashes, ambiguous
commits and database-only reconciliation. Queue age is not oldest backlog age.
For precise details see [Coverage metrics](coverage-metrics.md).

### Shared Database

Scope `github.com/exaring/otelpgx`:

| OTel Name | Prometheus Family | Type / Unit | Dimensions |
| --- | --- | --- | --- |
| `db.client.operation.duration` | `db_client_operation_duration_seconds` | Histogram, seconds | `db_system_name=postgresql`, `pgx_operation_type` |
| `db.client.operation.errors` | `db_client_operation_errors_total` | Counter, count | Same |

Operation types: `query`, `copy`, `batch`, `connect`, `prepare`, `acquire`.
Operations from both pools merge within a service; there is **no pool dimension**
on these two instruments. Errors exclude no-row results; batch callbacks can
produce multiple observations, so the error counter is not failed-job count.

**Current bucket gap:** default duration boundaries are
`0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000,+Inf`, in **seconds**.
Live queries fall overwhelmingly in `(0,5]`; a subsecond p95 estimate is not
meaningful. Add explicit SDK Views/buckets before database-latency SLOs or
subsecond percentile dashboards. `_sum/_count` still supports mean duration.

### Shared Pools

All thirteen instruments use fixed `db_client_connection_pool_name` plus
`db_system_name=postgresql`. Pool statistics refresh at most every five seconds,
then export at the normal thirty-second interval.

| OTel Name | Prometheus Name | Type / Unit | Meaning |
| --- | --- | --- | --- |
| `pgxpool.acquires` | `pgxpool_acquires_total` | Counter, count | Successful acquires |
| `pgxpool.acquire_duration` | `pgxpool_acquire_duration_nanoseconds_total` | Counter, ns | Cumulative successful acquire duration |
| `pgxpool.acquired_connections` | `pgxpool_acquired_connections` | Gauge, connections | In use |
| `pgxpool.canceled_acquires` | `pgxpool_canceled_acquires_total` | Counter, count | Context-cancelled acquisitions |
| `pgxpool.constructing_connections` | `pgxpool_constructing_connections` | Gauge, connections | Being constructed |
| `pgxpool.empty_acquire` | `pgxpool_empty_acquire_total` | Counter, count | Successful acquisitions that waited on an empty pool |
| `pgxpool.idle_connections` | `pgxpool_idle_connections` | Gauge, connections | Idle |
| `pgxpool.max_connections` | `pgxpool_max_connections` | Gauge, connections | Configured capacity |
| `pgxpool.max_idle_destroys` | `pgxpool_max_idle_destroys_total` | Counter, count | Idle-age destroys |
| `pgxpool.max_lifetime_destroys` | `pgxpool_max_lifetime_destroys_total` | Counter, count | Lifetime destroys |
| `pgxpool.new_connections` | `pgxpool_new_connections_total` | Counter, count | Opened connections |
| `pgxpool.total_connections` | `pgxpool_total_connections` | Gauge, connections | Constructing + acquired + idle |
| `pgxpool.empty_acquire_wait_time` | `pgxpool_empty_acquire_wait_time_nanoseconds_total` | Counter, ns | Total wait for successful empty-pool acquires |

Live pool capacities at the 10/5/2026 snapshot:

| Pool Name | Maximum Connections |
| --- | ---: |
| `workouts-api` | 6 |
| `workouts-api-coverage-diagnostics` | 1 |
| `workouts-worker` | 6 |
| `workouts-worker-osm` | 6 |
| `workouts-coverage-worker` | 4 |
| `workouts-coverage-worker-osm` | 3 |

The older 10/4/2026 observation recorded general-worker app/OSM capacities of
10/10; it is not the current capacity. These are dated measurements, not an SLO
or immutable deployment contract.
Acquire/wait totals are not histograms and cannot provide pool-specific p95.

### Shared Runtime

Scope `go.opentelemetry.io/contrib/instrumentation/runtime`:

| OTel Name | Prometheus Name | Type / Unit | Meaning |
| --- | --- | --- | --- |
| `go.memory.used` | `go_memory_used_bytes` | Gauge, bytes | Runtime-managed memory, `go_memory_type=stack\|other` |
| `go.memory.limit` | `go_memory_limit_bytes` | Gauge, bytes | Only emitted with a finite Go runtime memory limit |
| `go.memory.allocated` | `go_memory_allocated_bytes_total` | Counter, bytes | Cumulative heap allocations |
| `go.memory.allocations` | `go_memory_allocations_total` | Counter, allocations | Allocation count |
| `go.memory.gc.goal` | `go_memory_gc_goal_bytes` | Gauge, bytes | Heap GC goal |
| `go.goroutine.count` | `go_goroutine_count` | Gauge, count | Live goroutines |
| `go.processor.limit` | `go_processor_limit` | Gauge, threads | GOMAXPROCS |
| `go.config.gogc` | `go_config_gogc_percent` | Gauge, percent | GOGC setting |

Runtime refresh minimum is fifteen seconds. Go memory is not container RSS or
working set. Kubernetes memory limits do not by themselves emit `go.memory.limit`.
No process CPU, uptime, active request or GC-pause histogram is installed here.
The pinned dependency offers deprecated runtime instruments behind an unset
feature flag and scheduler instruments behind an unattached producer; neither
is part of the configured normal inventory.

### UI Native nginx

The pinned Contrib **0.156.0** nginx receiver reads native `stub_status`, not a
custom application exporter. Its source schema declares plain units `connections`
and `requests` (not `{connections}`). The following host families are **live-verified**:

| OTel Name | Actual Prometheus Family | Type / Unit | Meaning |
| --- | --- | --- | --- |
| `nginx.connections_accepted` | `nginx_connections_accepted_total` | Counter, connections | Cumulative accepted client connections |
| `nginx.connections_handled` | `nginx_connections_handled_total` | Counter, connections | Cumulative handled connections; fewer than accepted can indicate resource limits |
| `nginx.connections_current` | `nginx_connections_current` | Gauge, connections | `state=active\|reading\|writing\|waiting` |
| `nginx.requests` | `nginx_requests_total` | Counter, requests | Cumulative HTTP requests |

There is no doubled connection-unit suffix on `nginx_connections_current`.
The four families yield seven native series: three counters and four state
gauges; generated `target_info` adds one series/family, making 5 families/8 series.
Final accepted/handled totals were both 1,030, requests 1,325, and current
connections active=6, reading=0, waiting=5, writing=1. `active` is the
total, while reading/writing/waiting are its breakdown: do not sum all four states.
Connections are not requests or users; keepalives can carry several requests.
Counts include internal status/probe traffic and are not per-route/status counters
or latency histograms. Browser latency/errors and cache telemetry remain gaps.

### Martin Native HTTP

Martin **1.15.0** exposes these two native families at `/_/metrics` without an
extra metrics option:

| Native Prometheus Family | Actual Gateway/Host Family | Type / Unit | Native Labels |
| --- | --- | --- | --- |
| `martin_http_requests_total` | `martin_http_requests_total` | Counter, requests | `endpoint`, `method`, `status` |
| `martin_http_requests_duration_seconds` | `martin_http_requests_duration_seconds` | Histogram, seconds | Same; `_bucket`, `_sum`, `_count` |

At the gateway/host, select **`martin_route`**, not `endpoint`, alongside `method`
and `status`. The live tile-route template is **`/{source_ids}/{z}/{x}/{y}`**.
The earlier source-only guide incorrectly named the histogram
`martin_http_request_duration_secs`; the vendor fixture's synthetic name is not
the deployed Martin family. Martin's source masks unmatched routes as `UNKNOWN`;
the audited native combinations were GET `/health` 200 (793), `/_/metrics` 200
(133), and the tile template 200 (140) / 204 (75). Native **2xx includes 204**
for valid empty tiles; current successful tile selectors can use `status=~"200|204"`.
API forwarding converts
upstream 204 to API 200, so native and proxy status populations differ. Native
timing does not include API authorization, proxy/body handling or browser render.
This adds independent backend HTTP visibility, not cache/pool/server-SQL metrics.

The actual histogram host `le` spellings are
`0.005,0.01,0.025,0.05,0.1,0.25,0.5,1.0,2.5,5.0,10.0,+Inf` seconds.
Source integer bounds `1`, `5`, `10` become `1.0`, `5.0`, `10.0` at the host.
Four native combinations produce 60 HTTP series; five receiver scrape-health
gauges (`up`, `scrape_duration_seconds`, `scrape_samples_post_metric_relabeling`,
`scrape_samples_scraped`, `scrape_series_added`) plus generated `target_info`
make **8 families/66 series**, not eight Martin-owned instruments. Display units
follow names/HELP; explicit native/host metadata unit fields were empty.

### Collector Self-Telemetry

Each sidecar scrapes its own loopback `127.0.0.1:8888/metrics` through
`prometheus/self`. `metrics/native` uses resource identity `workouts-ui` or
`workouts-tiles`; **`metrics/self` uses `workouts-ui-collector` or
`workouts-tiles-collector`**. Both apply memory limiter, their respective resource
processor, stable identity transform and batch. Self data describes the collector's
Go process, not nginx, Rust/Martin, the API, workers, or a parent Job process.

The six basic Go/process self metrics below are **live-verified**. Source names
and final host names differ; do not infer final suffixes from the vendor fixture
or a broad metadata catalog containing other collectors/historical sources.

| Source Name | Actual Host Family | Interpretation |
| --- | --- | --- |
| `otelcol_process_cpu_seconds` | `otelcol_process_cpu_seconds_total` | Cumulative collector process CPU seconds; rate gives used CPU cores |
| `otelcol_process_memory_rss` | `otelcol_process_memory_rss` | Gauge, collector process resident bytes; no `_bytes` suffix |
| `otelcol_process_runtime_heap_alloc_bytes` | `otelcol_process_runtime_heap_alloc_bytes` | Collector live Go heap bytes |
| `otelcol_process_runtime_total_alloc_bytes` | `otelcol_process_runtime_alloc_bytes_total` | Collector cumulative Go allocations, monotonic counter |
| `otelcol_process_runtime_total_sys_memory_bytes` | `otelcol_process_runtime_total_sys_memory_bytes` | Collector runtime-obtained memory, not container working set |
| `otelcol_process_uptime` | `otelcol_process_uptime_total` | Collector monotonic uptime seconds, not application uptime; no `_seconds` suffix |

Complete shared live self inventory: **31 families on tiles**, with the same 31
plus four UI-only families below making **35 on UI**. Histogram families have
bucket/sum/count series. Units are not inferred from default bucket sets;
re-scraped self metadata may be empty, including internal-duration units.

| Actual Host Family | Type |
| --- | --- |
| `otelcol_exporter_in_flight_requests` | Gauge |
| `otelcol_exporter_queue_batch_send_size` | Histogram |
| `otelcol_exporter_queue_batch_send_size_bytes` | Histogram |
| `otelcol_exporter_queue_capacity` | Gauge |
| `otelcol_exporter_queue_size` | Gauge |
| `otelcol_exporter_sent_metric_points_total` | Counter |
| `otelcol_process_cpu_seconds_total` | Counter |
| `otelcol_process_memory_rss` | Gauge |
| `otelcol_process_runtime_alloc_bytes_total` | Counter |
| `otelcol_process_runtime_heap_alloc_bytes` | Gauge |
| `otelcol_process_runtime_total_sys_memory_bytes` | Gauge |
| `otelcol_process_uptime_total` | Counter |
| `otelcol_processor_batch_batch_send_size` | Histogram |
| `otelcol_processor_batch_batch_send_size_bytes` | Histogram |
| `otelcol_processor_batch_metadata_cardinality` | Gauge |
| `otelcol_processor_batch_timeout_trigger_send_total` | Counter |
| `otelcol_processor_incoming_items_total` | Counter |
| `otelcol_processor_internal_duration` | Histogram |
| `otelcol_processor_memory_limiter_accepted_metric_points_total` | Counter |
| `otelcol_processor_outgoing_items_total` | Counter |
| `otelcol_receiver_accepted_metric_points_total` | Counter |
| `otelcol_receiver_failed_metric_points_total` | Counter |
| `otelcol_receiver_refused_metric_points_total` | Counter |
| `promhttp_metric_handler_errors_total` | Counter |
| `rpc_client_call_duration` | Histogram |
| `scrape_duration_seconds` | Gauge |
| `scrape_samples_post_metric_relabeling` | Gauge |
| `scrape_samples_scraped` | Gauge |
| `scrape_series_added` | Gauge |
| `target_info` | Generated gauge; resource-label exception |
| `up` | Forwarded local scrape-health gauge |

| UI-Only Additional Host Family | Type |
| --- | --- |
| `http_client_request_body_size` | Histogram |
| `http_client_request_duration` | Histogram |
| `otelcol_scraper_errored_metric_points_total` | Counter |
| `otelcol_scraper_scraped_metric_points_total` | Counter |

Self labels include receiver/exporter/processor/scraper identities, transport,
data type/signal, RPC/HTTP dimensions and the four stable point aliases. Actual
receiver accepted/refused/failed counters and exporter sent counters count
**telemetry points**, not HTTP requests, failed workouts or distinct lost jobs.
Accepted is handoff to the pipeline, not proof of delivery; send-failed attempts may later
retry successfully, while enqueue-failed signals inability to enter the queue.
Refused/failed counters were present with actual zeros for `nginx`,
`prometheus/martin` and both `prometheus/self` receivers. Refused/failed semantics
still depend on receiver-helper feature gates; do not sum them into distinct loss.
`otelcol_exporter_send_failed_metric_points_total` and
`otelcol_exporter_enqueue_failed_metric_points_total` were **absent**, not measured
zero, at both sidecar source and host. The actual exporter is `otlp_grpc`, queue
size was 0 and capacity 10 on each sidecar. Self export shares the native exporter,
so exporter failure can hide its own warning metrics: pair it with pod/probe/log
and gateway observations.

The Martin receiver's forwarded `up` describes **local scrape success**, not
whole tile availability or Kubernetes readiness. A gateway `up=1` only proves
Prometheus scraped the gateway. Collector port 13133 probes are collector health,
not nginx/Martin readiness. Neither `up` nor collector self-runtime represents
successful completion of an OSM, bootstrap, migration, provisioning or timezone Job.
The nginx receiver emits **no Prometheus `up`**. Live forwarded `up=1` belongs
to Martin native or either self identity; use the correct `exported_job` to
distinguish those gauges from gateway scrape health.

## Operational Verification (10/5/2026)

Evidence is the finalized 403-line report
`/tmp/opencode/workouts-metrics-verification.md`, final snapshot
**10/5/2026 9:27:21 PM PDT**, delivered at 9:31 PM PDT. Release `workouts-dev`
is deployed at **revision 96**. API/general-worker/Coverage-worker pods are
1/1 ready, UI/tiles are 2/2 ready with sidecars; all seven containers have zero
restarts. Martin 1.15.0 and Contrib 0.156.0 images are digest-pinned. Inspected
startup logs show ready collectors without structured warnings/errors; the final
10-minute window shows no Go/sidecar TLS or telemetry-export failures.

Gateway and host agree on the following **source-family/series** inventory:

| Service Resource Identity | Observed Families | Series | Population |
| --- | ---: | ---: | --- |
| `workouts-api` | 26 | 1,152 | Shared baseline plus DB errors and owned API tile histogram |
| `workouts-worker` | 24 | 212 | Shared runtime/DB/pools and probe HTTP |
| `workouts-coverage-worker` | 24 | 212 | Shared baseline; no owned Coverage family |
| `workouts-ui` | 5 | 8 | Four nginx families / seven native series plus generated `target_info` |
| `workouts-tiles` | 8 | 66 | Two Martin HTTP families, five local scrape-health families, generated `target_info` |
| `workouts-ui-collector` | 35 | 270 | Self process/pipeline/scrape families including generated `target_info` |
| `workouts-tiles-collector` | 31 | 233 | Self process/pipeline/scrape families including generated `target_info` |

Five application identities total **1,650 series**, self identities add **503**,
for **2,153 source series**. Host-generated `count:up1` recording-rule series are
excluded. Family counts are not traffic counts, and each histogram is one family
but many series. New independent application traffic changed the API population
after an initial idle snapshot; an intermediate gateway/host mismatch was scrape
lag, not a persistent delivery discrepancy. Do not sum component family counts
as a union of distinct instrument names.

The common **24 live Go families** are the three HTTP histograms, seven runtime
families (the runtime table except `go_memory_limit_bytes`), DB operation duration
and all thirteen pgxpool families listed above. API additionally has
`db_client_operation_errors_total` and `workouts_api_tile_duration_seconds`.
Worker DB-error counters, finite `go_memory_limit_bytes` on all three Go services,
and all eight owned Coverage families were absent in the queried windows; no
owned Coverage family appeared in the retained six hours. Absence is **not zero**
and establishes neither queue/backlog state nor missing source instrumentation.
Collector CPU/uptime is not application Go CPU/uptime.

All actual native/self points passed the four stable-label checks. Excluding
generated `target_info`, there were UI 7, Martin 65, UI self 269 and tiles self
232 datapoint series, one producer instance per identity. Go points lacked those
projected aliases in this dated audit. This does not prove broader multi-environment/replica safety
or a source-enforced privacy/cardinality allowlist for every future dimension.

### Freshness And Delivery

Changing direct-gateway source counters over **75 seconds**, spanning more than
two export intervals, establish fresh producer data with no observed resets:

| Producer / Counter | Increase |
| --- | ---: |
| API `go_memory_allocated_bytes_total` | 770,848 bytes |
| General worker `go_memory_allocated_bytes_total` | 1,342,936 bytes |
| Coverage worker `go_memory_allocated_bytes_total` | 1,420,640 bytes |
| UI `nginx_requests_total` | 32 requests |
| Martin `martin_http_requests_total`, health/metrics combinations | 23 requests |
| UI self `otelcol_process_cpu_seconds_total` | 0.10 CPU seconds |
| Tiles self `otelcol_process_cpu_seconds_total` | 0.22 CPU seconds |

Selected host samples for all seven identities were approximately 10.217 seconds
old at 10/5/2026 9:25:35 PM PDT. Sample age proves scrape freshness; the changing
producer counters prove more than timestamp refresh of stale gateway data.
Runtime exports can progress while owned job events remain absent.

| Self Identity / Receiver | Accepted Points | Refused Points | Failed Points |
| --- | ---: | ---: | ---: |
| UI / `nginx` | 917 | 0 | 0 |
| UI / `prometheus/self` | 6,104 | 0 | 0 |
| Tiles / `prometheus/martin` | 1,185 | 0 | 0 |
| Tiles / `prometheus/self` | 5,584 | 0 | 0 |

Exporter `otlp_grpc` sent UI 7,021 and tiles 6,769 points; accepted/sent rates
and collector CPU/allocation increases were positive. Queue size/capacity was
0/10. Receiver failures/refusals are observed zeros, unlike absent exporter
send/enqueue-failure families. Gateway gRPC accepted points increased by 3,602
and spans by 641 over the comparison; these global totals include other producers
and cannot be attributed exclusively to Workouts Explorer. Both gateway scrape
targets were up with empty last-error fields.

### Resources And Application Outcomes

Final collector process samples:

| Self Identity | CPU Seconds | RSS Bytes | Heap Bytes | Allocated Bytes Total | Runtime System Bytes | Uptime Seconds |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| UI collector | 6.62 | 158,969,856 | 26,487,720 | 196,916,864 | 48,863,496 | 3,924.325 |
| Tiles collector | 9.99 | 194,285,568 | 26,630,184 | 202,385,656 | 45,074,712 | 3,926.570 |

RSS is approximately 152/185 MiB, materially above the 64Mi requests and below
the unchanged 256Mi limits; each collector retains 25m CPU/64Mi requests,
250m/256Mi limits and `GOMEMLIMIT=192MiB`. This snapshot is not peak RSS or a
load-sizing/OOM guarantee. No restart was observed; do not silently raise limits
or equate the Go heap/memory limit with total resident/container memory.

API tile observations were route/success/200=85, **route/upstream_error/502=111**,
and coverage/success/200=20. Existing API 502/503 populations warrant a separate
authorized application investigation; transport/TLS/gateway health does not imply
successful private tile forwarding or explain those errors. The retained six-hour
HTTP window can include pre-rollout requests and must not all be attributed to
REV96. Native successful Martin 200/204 observations are a different population.

The audit used Kubernetes/Helm reads, container DNS/config/environment/log reads,
public-certificate TLS verification, metrics reads and host Prometheus queries.
Safe probes covered pod-local UI health/static/status and Martin health/metrics,
not external browser/public-edge health. No authenticated API/private tile request,
synthetic OTLP/gRPC application call, Job, import, Coverage retry, OSM operation,
SQL query or database server-metrics work was created. Application traffic grew
independently. No credentials/private keys/private URLs/raw errors were printed,
and no cluster or repository change was made by the audit; all its forwards stopped.
Negative TLS fault injection, binary-to-source attestation, trace attribution,
historical completeness and accepted SLOs were not established.

Earlier source checks passed telemetry/config and Helm rendering/schema/private-
status tests, while the opt-in vendor export test skipped because its binary
variable was unset. That skipped fixture remains no evidence; **the subsequent
live audit**, not the source test, establishes the actual families and delivery.

### Historical Live Audit (10/4/2026)

Observed on 10/4/2026 at approximately 10:14-10:20PM PDT
(`America/Los_Angeles`), the gateway scrape was healthy (`up=1`, no
scrape error). Prometheus and exporter agreed on 1,694 application series.

| Service | Observed Families | Series | Main Observations |
| --- | ---: | ---: | --- |
| API | 26 | 1,270 | HTTP, seven runtime, fifteen DB/pool, tile family |
| General worker | 24 | 212 | Probe HTTP, seven runtime, fourteen DB/pool |
| Coverage worker | 24 | 212 | Same shared families; no recent owned Coverage family |

This historical **Go-only** union has 26 of the 35 implemented instrument families.
It does not count the newly added Martin/nginx/self path. `go.memory.limit`
and all eight Coverage families were absent in the queried six hours; database
error counters were absent on both workers. Absence is **not numeric zero**, does
not establish job/queue state at that audit, and does not prove missing instrumentation.
No jobs or API work were triggered to force metrics into existence.

Samples were roughly eleven seconds old; allocation counters increased over two
minutes for all services, showing fresh exports rather than frozen scrape data.
Pool callbacks emit legitimate zero counters. Unused synchronous instruments do
not appear until measured. The OTel gateway expiration also makes absent-series
interpretation different from resetting a counter.

Observed metric dimensions contain route templates and fixed pool names, not
actual accounts/jobs/workouts/generations/coordinates, SQL or raw errors. HTTP
`server_address` is derived from request Host; its low observed cardinality is
not an enforced allowlist. Review it before public access or scale-out.

## Historical SQL Statistics

These stores exist, but **no production aggregate exporter/Grafana SQL read path
is implemented**. The current owner-facing APIs and acceptance scripts are not
a fleet-monitoring endpoint.

| Store | Useful Facts | Dashboard Caveats |
| --- | --- | --- |
| `app.jobs`, progress/events/logs and route contexts | Queue/lease state, attempts/retries, timestamps, durable outcomes and durations | Top-level parents and child work are different populations; owner UI hides some predecessors and derives presentation states |
| `app.source_files`, `app.workout_import_events`, `app.job_progress` | Discovery/file outcomes; created/updated/unchanged facts | Current rejected-workout progress producer passes literal zero; not a verified rejection metric |
| Workouts, aggregates and route summaries | Current inventory, routed share, per-type volume | Counts decrease with deletion; distance/time/energy/heart-rate are product/health values, not generic telemetry |
| Coverage states/matches/attributions and path/park rollups | Current/stale/failed/unavailable coverage, visit/segment inventory | Exact explorer range semantics come from current eligible-workout queries, not all rollups alone |
| Diagnostic runs/evidence | Matching quality, point/segment/traversal counts and duration | Private diagnostic data; publish only authorized bounded aggregates |
| OSM generations/stages/storage blocks | Active generation, checkpoints, validation, blockers and update history | Operationally useful timestamps/ages; avoid unbounded generation/region labels |

Proposed state collection: a dedicated authorized aggregate reader, bounded
queries, timeouts, freshness timestamps and scrape success. Give its role only
approved aggregate function/view access, not broad private-table SELECT or an
API user's credentials. Account RLS/forced-RLS must be deliberately handled;
`SECURITY DEFINER` alone is not proof of safe fleet aggregation.

For exact historical daily statistics, use committed facts with explicit event
definitions and retention/cursors, not repeated counts of a mutable table cast
as counters. Suggested fleet aggregates: current accounts/workouts/routed share,
queue state/oldest age, daily durable job outcomes, ingestion created/updated/
unchanged, processing latency, Coverage freshness and OSM update history.

Do not export owner IDs, names, emails, source paths, coordinates, route geometry,
heart rate, energy or individual workout measurements as Prometheus dimensions.
Account-specific statistics require authenticated owner-scoped API access or a
separately authorized datasource; a Grafana account variable is not authorization.
Define small-cohort privacy and deletion/retention policy before exposing health
or personal activity aggregates in shared operator dashboards.

## Dashboards

**Dashboard configuration updated 10/6/2026.** Two auto-provisioned dashboards
are available in homelab Grafana:
**Workouts Explorer / Operational** and **Workouts Explorer / Historical**.
The application Helm chart now owns their ConfigMaps in xdev namespace
`workouts-explorer`, with `grafana_dashboard: "1"` and
`vcluster.loft.sh/force-sync: 'true'` for unmounted ConfigMaps. vCluster copies
them to the homelab host, where the existing Grafana sidecar's `NAMESPACE=ALL`
discovery imports them into the default/root folder with the same stable UIDs.
A live sync probe on 10/6/2026 confirmed the copied dashboard label. The main
agent's Helm deployment and removal of the two original manually managed homelab
ConfigMaps are pending final verification; no Grafana restart is required.
Source, regeneration and xdev Helm deployment commands, links and interpretation
caveats are in [the dashboard guide](../deploy/grafana/README.md). The earlier
build's live provisioning and all 92 target queries were verified on 10/6/2026;
that evidence does not verify the updated layout or final Helm migration. Missing
data remains N/A, and query definitions, metric scope and refresh defaults are unchanged.

- **Editing:** JSON uses `editable: true` and removes the read-only label. Users
  with suitable Grafana permissions can experiment, export or use Save As, but
  the unchanged global file provider's `allowUiUpdates: false` blocks saving to
  the existing provisioned UID. There is no per-dashboard JSON provider-policy
  override; durable updates belong in the generator.
- **Navigation:** Namespace and Environment remain native variable selectors.
  Separate compact top-row links use 32px-high anchors and `target="_self"`,
  retaining those selections while loading each view's default time range to
  avoid retained SPA scroll. JSON cannot place/style these links in Grafana's
  native toolbar; actual control heights, alignment and scroll reset await
  browser verification. All informational bullet sections are in the collapsed
  bottom **Notes and data availability** row; metric rows remain expanded.
- **History:** Historical defaults to 7d and offers 30d, matching the current
  Thanos retention ceiling without a hard stop or query clamp. Retention is not
  proof of complete blocks, exact calendar-month totals or year-long history.

The capability breakdown below includes proposed panels still awaiting producers,
not a claim that every listed signal exists in the deployed dashboards.

| Dashboard | Live Panels | Historical Panels | Availability |
| --- | --- | --- | --- |
| Service Overview | Five-component traffic/runtime/pools as applicable, readiness, CPU/memory/restarts, telemetry delivery | Daily traffic/errors, percentile bucket trends, changes/deploy annotations | REV96 delivery audit is historical; REV104 Go origin identity verified; external probes missing |
| API And Tiles | Endpoint RPS/status/p95/bytes, tile outcomes, diagnostic pool saturation | Request mix, tile errors/bytes/latency, renewal frequency | Tile and HTTP exist; logical renew/render and diagnostic-specific signals missing |
| UI And Martin Native | nginx connections/request rate/accepted-handled gap; Martin route/method/status RPS/p95; separate collector health | Native traffic/connection trends and collector resource/export failures | Native/self families and host labels verified; no nginx request latency histogram |
| Ingestion | Job queue/oldest age, workers/file slots, phase durations, source freshness, staging/cleanup | Files and created/updated/unchanged/rejected, attempts/outcomes, scheduling history | Mostly durable facts/logs; add actual producers and state reader |
| Coverage | Matcher/admission/route timings, pool contention, outcomes, backlog/slots/reconciliation | Applied/no-evidence/failed/stale trends, retries, processing delay, freshness | Eight event instruments exist; state/reconciliation/complete historical statistics missing |
| OSM And Storage | Optional cluster-infrastructure primary/PVC/locks/WAL panels; future stage/checkpoint state | Future promotion/stage/outcome history and infrastructure capacity trend | Existing external infrastructure sources only; Job and application/OSM DB telemetry additions deferred |
| Data Inventory | Current anonymous aggregate counts/routed share | Daily inventory and authorized import/visit facts | Proposed aggregate read model/exporter, not an existing metric family |
| SLOs | Error-budget remaining and short/long burn rates | Window compliance and incidents | REV104: four objectives and 76 app rules healthy; API smoke inputs verified; tile inputs absent; window completeness unverified |

### Dated Five-Component Panel Examples (10/5/2026)

Examples use the live-verified family/label spellings, except source-defined owned
Coverage families and unobserved exporter-failure candidates, explicitly marked.
The audit executed API GET rate/p95, native nginx rate/states/connection gap,
Martin rate/p95, Go allocation/memory and self CPU/RSS/allocation/accepted/sent
checks against the TLS host Prometheus API. Coverage matcher returned an empty
vector; sparse API groups returned NaN, never rewritten as zero. This validates
selected observations, not installed dashboards, every candidate query or SLO rules.
`$environment` and `$service_namespace` are selected Grafana variables matching
the audited **`xdev`** / **`workouts-explorer`** values, not instructions to query
every environment together. These are preserved 10/5 audit examples, not current
Go query recommendations: their bare Go `exported_job` selectors omit new labeled
series. For new Go metrics use namespace-prefixed jobs and exact origin aliases,
as in the SLI selectors below. Only Grafana's compatibility regex includes old
bare Go history, whose environment/namespace is unknown from the series.
The explicit `exported_job` values in these examples target the audited
`workouts-explorer` namespace; changing the namespace variable also requires
changing that job prefix. Scope names identify nginx receiver versus Prometheus
receiver data; a broad metric metadata catalog alone is not service-specific evidence.

**API HTTP:** select route templates and Go method/status labels, not Martin's
labels. These examples include all negotiated HTTP versions; HTTP/2 is not a
second metric family. Add a protocol filter only after verifying its emitted label.

```promql
# Successful API GET rate (2xx); omit worker probes and API health checks.
sum(rate(http_server_request_duration_seconds_count{
  job="otel-gateway-collector",exported_job="workouts-api",http_route=~"/api/.*",
  http_request_method="GET",http_response_status_code=~"2.."
}[5m]))

# API p95, seconds; route templates are the bounded grouping dimension.
histogram_quantile(0.95,
  sum by (le,http_route) (rate(http_server_request_duration_seconds_bucket{
    job="otel-gateway-collector",exported_job="workouts-api",
    http_route=~"/api/.*",http_request_method="GET"
  }[5m])))

# API 5xx attempt rate; a fast error still contributes to the p95 above.
sum(rate(http_server_request_duration_seconds_count{
  exported_job="workouts-api",http_route=~"/api/.*",http_response_status_code=~"5.."
}[5m]))

# Mean successful pool acquisition, seconds; this is not p95.
sum by (db_client_connection_pool_name) (rate(pgxpool_acquire_duration_nanoseconds_total{
  exported_job="workouts-api"
}[5m])) / 1e9
/
sum by (db_client_connection_pool_name) (rate(pgxpool_acquires_total{
  exported_job="workouts-api"
}[5m]))
```

**General worker:** shared Go runtime and pgxpool metrics exist; probe HTTP is
not ingest traffic. No owned ingest/job/scheduler/freshness lifecycle instruments
are implemented, and these panels cannot infer completed work or queue length.

```promql
# Live goroutines, retaining service identity.
sum by (exported_job) (go_goroutine_count{exported_job="workouts-worker"})

# Runtime-managed memory, not process RSS/container working set.
sum by (exported_job,go_memory_type) (go_memory_used_bytes{
  exported_job="workouts-worker"
})

# Executed freshness check: positive allocation increases on all three Go services.
# Dated audit: these legacy series lacked environment/namespace/UID aliases.
sum by (exported_job) (increase(go_memory_allocated_bytes_total{
  job="otel-gateway-collector",exported_job=~"workouts-(api|worker|coverage-worker)"
}[2m]))

# Pool occupancy; fixed names distinguish app and OSM pools.
sum by (db_client_connection_pool_name) (pgxpool_acquired_connections{
  exported_job="workouts-worker"
})
/
sum by (db_client_connection_pool_name) (pgxpool_max_connections{
  exported_job="workouts-worker"
})

# Cancelled pool acquires are not failed-job counts.
sum by (db_client_connection_pool_name) (rate(pgxpool_canceled_acquires_total{
  exported_job="workouts-worker"
}[5m]))
```

**Coverage worker:** the eight owned instruments are sparse and remained absent
over the latest queried six hours. Empty results are not zero latency/outcomes or proof
of an empty backlog. Outcome counts are observed committed transitions, not an
exactly-once ledger; matcher p95 excludes attempts that never enter Evaluate.

```promql
histogram_quantile(0.95,
  sum by (le) (rate(coverage_matcher_duration_seconds_bucket{
    exported_job="workouts-coverage-worker"
  }[15m])))

histogram_quantile(0.95,
  sum by (le) (rate(coverage_admission_wait_seconds_bucket{
    exported_job="workouts-coverage-worker"
  }[15m])))

sum by (outcome,failure) (increase(coverage_route_outcomes_total{
  exported_job="workouts-coverage-worker"
}[1h]))

sum by (db_client_connection_pool_name) (pgxpool_acquired_connections{
  exported_job="workouts-coverage-worker"
})
```

**UI nginx:** live names, scoped to the selected environment/namespace. Gauge
states and request rate returned data; the accepted-minus-handled rate gap was
an actual numeric **0**, unlike an absent series. It is a connection signal,
not an HTTP error ratio.

```promql
sum by (state) (nginx_connections_current{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-ui",
  service_name="workouts-ui",deployment_environment="$environment",
  service_namespace="$service_namespace"
})

sum(rate(nginx_requests_total{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-ui",
  service_name="workouts-ui",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

sum(rate(nginx_connections_accepted_total{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-ui",
  service_name="workouts-ui",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))
-
sum(rate(nginx_connections_handled_total{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-ui",
  service_name="workouts-ui",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))
```

**Martin native:** live family/template values. Use native `method`/`status` and
preserved `martin_route`; observed successful tile statuses are **200 and 204**.
The audit's final successful-tile p95 estimate was approximately 0.98093s;
API route-tile p95 was approximately 0.91775s, across a different population and
timing boundary. These are window estimates, not commitments or browser latency.

```promql
sum by (martin_route,status) (rate(martin_http_requests_total{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-tiles",
  service_name="workouts-tiles",deployment_environment="$environment",
  service_namespace="$service_namespace",method="GET",status=~"200|204",
  martin_route="/{source_ids}/{z}/{x}/{y}"
}[5m]))

histogram_quantile(0.95,
  sum by (le,martin_route) (rate(martin_http_requests_duration_seconds_bucket{
    job="otel-gateway-collector",exported_job="workouts-explorer/workouts-tiles",
    service_name="workouts-tiles",deployment_environment="$environment",
    service_namespace="$service_namespace",method="GET",status=~"200|204",
    martin_route="/{source_ids}/{z}/{x}/{y}"
  }[5m])))

# No Martin 5xx combination was observed in the final snapshot; absence is not zero.
sum by (martin_route) (rate(martin_http_requests_total{
  job="otel-gateway-collector",exported_job="workouts-explorer/workouts-tiles",
  service_name="workouts-tiles",deployment_environment="$environment",
  service_namespace="$service_namespace",method="GET",status=~"5.."
}[5m]))
```

### Sidecar Health Panels

These live names describe only the collector process and telemetry pipeline.
The two exporter-failure selectors at the end are **unobserved candidates**, not
live zero counters. A positive accepted/sent rate is evidence
of point flow, not an application-availability SLI; native/self pipelines both
contribute points and are not necessarily a one-to-one end-to-end count.

```promql
# Collector CPU cores, not UI/Martin CPU.
sum by (service_name) (rate(otelcol_process_cpu_seconds_total{
  job="otel-gateway-collector",exported_job=~"workouts-explorer/workouts-(ui|tiles)-collector",
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

# Collector RSS, retain replica identity rather than using the gateway pod label.
otelcol_process_memory_rss{
  job="otel-gateway-collector",exported_job=~"workouts-explorer/workouts-(ui|tiles)-collector",
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}

# Executed allocation-increase check; actual host name omits the source "total" token.
sum by (service_name) (increase(otelcol_process_runtime_alloc_bytes_total{
  job="otel-gateway-collector",exported_job=~"workouts-explorer/workouts-(ui|tiles)-collector",
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[2m]))

# Current collector uptime in seconds; this family has no "_seconds" suffix.
otelcol_process_uptime_total{
  job="otel-gateway-collector",exported_job=~"workouts-explorer/workouts-(ui|tiles)-collector",
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}

# Receiver point handoff; separate nginx, prometheus/martin and prometheus/self.
sum by (service_name,receiver) (rate(otelcol_receiver_accepted_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

# Actual refused-point counters were zero; inspect internal failures separately.
sum by (service_name,receiver) (rate(otelcol_receiver_refused_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

# Actual internal-failure counters were zero; do not add to refused as distinct loss.
sum by (service_name,receiver) (rate(otelcol_receiver_failed_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

# Successful sends: positive live rates, exporter="otlp_grpc".
sum by (service_name,exporter) (rate(otelcol_exporter_sent_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace",exporter="otlp_grpc"
}[5m]))

# Unobserved candidate: absent at sidecar source/host, not numeric zero.
sum by (service_name,exporter) (rate(otelcol_exporter_send_failed_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))

# Unobserved candidate: absent at sidecar source/host, not numeric zero.
sum by (service_name,exporter) (rate(otelcol_exporter_enqueue_failed_metric_points_total{
  service_name=~"workouts-(ui|tiles)-collector",deployment_environment="$environment",
  service_namespace="$service_namespace"
}[5m]))
```

Zero denominators/no traffic should display no-data, not synthetic perfect health.
Show freshness/export loss alongside charts. Host kubelet/cAdvisor/kube-state
metrics provide CPU, working set, quota throttling, restarts/OOM and PVC data;
use host vCluster pod mapping rather than the collector's pod label.

## Pyrra Application SLOs

**Implementation/pipeline status: 10/10/2026** (`America/Los_Angeles`).
`helm/templates/pyrra-slos.yaml` creates four ordinary Helm-owned
`pyrra.dev/v1alpha1` ServiceLevelObjectives when `pyrraSLOs.enabled=true`.
The chart default is **false**; `helm/values-xdev.local.yaml` enables them for
normal xdev deployment. Acceptance releases keep them disabled. Enabling them
requires a nonempty `otel.endpoint` and a supported Pyrra CRD/controller path.
The initial app rollout completed at Helm revisions **103/104** before xdev
recreation. The recreated release verified all four source/mirrored SLOs,
generated files, and advancing healthy Ruler rules.

xdev now uses namespace sync with `"*": "xdev-*"`. Virtual namespace
`workouts-explorer` maps to **`xdev-workouts-explorer`**; all four SLO resource
names match exactly between virtual and host clusters. PostgreSQL maps to
`xdev-postgresql`, with a fresh claim and restored database. Namespace mappings
are creation-time configuration owned by the infrastructure automation.

xdev vCluster custom-resource sync for
**`servicelevelobjectives.pyrra.dev/v1alpha1` is already enabled**. Source SLOs
in virtual namespace `workouts-explorer` sync to the mapped host namespace
without name rewriting. The host Pyrra controller runs **ConfigMap mode** and
copies the unprefixed root metadata label **`pyrra-rule: "true"`** to generated
rule ConfigMaps. The standalone Thanos Ruler watcher discovers those ConfigMaps
across namespaces, reloads rules and evaluates against Thanos Query. The four app
objectives generate **12 app groups / 76 rules**. All app groups use ABORT and
have demonstrated advancing, healthy evaluations. An empty `groups: []` default
ConfigMap is not evidence of an empty Ruler: Pyrra rules load through the
additional rule-file path. Infrastructure configuration and migration tooling
belong to external Ansible roles, not this application's chart or repository.
See [Operational note](#operational-note) for the relevant safety boundaries.
This documentation performs no infrastructure changes.

### Dashboard Labels

The chart does not set custom `pyrra.dev/*` labels. Pyrra supplies the automatic
namespace badge; in xdev it is **`namespace=xdev-workouts-explorer`**, identifying
both the environment and application without repeating the objective name or
adding multiple badges that widen the table. Standard Helm/Kubernetes labels and
the `pyrra-rule` discovery label remain.

Playwright verified all four rows show only that namespace badge. At a 1440px
viewport the table is 1288px wide, with Availability, Budget and Alerts fully
visible; the former five-badge layout was 2058px wide. All 12 app groups / 76
rules remain healthy and advancing after regeneration.

This does not remove origin labels from the underlying telemetry. Every raw SLI
selector still matches `service_name`, `service_namespace` and
`deployment_environment` explicitly. Targets, windows, populations and the
backsync guard are unchanged.

Removing the former propagated `application`, `environment`, `name` and
`service_namespace` metadata labels changes generated recording/alert labelsets,
not resource names. Older series remain in retained history; they are not
relabeled. External SLO selectors or alert routing that relied on those metadata
labels must use the objective identity and namespace instead.

### SLO Name Translation

The current namespace-sync deployment preserves namespaced SLO names. The
following describes the **previous single-host-namespace deployment**, not the
current placement or dashboard titles.

In vCluster **0.36.0** single-host-namespace mode, custom-resource names use the
short-name translator rather than preserving the virtual name. Multiple virtual
namespaces share a host namespace, so translation incorporates both the source
namespace and vCluster identity to avoid naming collisions. It is not a long-name
truncation fallback: even short custom-resource names are hashed.

The verified translator computes `v` followed by the first 13 characters of
the lowercase base36-encoded SHA256 digest of
`<virtual-name>-x-<virtual-namespace>-x-<vcluster-name>`. For this app,
`workouts-explorer-api-availability` in virtual namespace `workouts-explorer`
and vCluster `dev` maps to host name `vvirq2ysdlfye6`.
See the pinned [vCluster translator](https://github.com/loft-sh/vcluster/blob/v0.36.0/pkg/util/translate/single_namespace.go).

There is no supported per-resource name template or naming strategy for to-host
custom-resource sync in the verified version. Patches are not a supported way to
change `metadata.name` or `metadata.namespace`; from-host name mappings do not
rename virtual-owned mirrors. Enabling namespace sync changes placement across
all synced resources and is not an app-only cosmetic fix.

Stock Pyrra **0.10.2** uses the host name as its dashboard title and has no
display-name override. The earlier chart used a `pyrra.dev/name` badge as a
readability workaround, not a title override. Namespace sync now preserves the
actual titles, and the custom badges have been removed in favor of namespace-only
identification.

Do not manually rename mirrored/generated objects. A naming or namespace
migration replaces Kubernetes identities and can split SLO recording history,
change alert fingerprints or briefly duplicate evaluation. Coordinate any such
migration through the owning automation.

### Generated Rule Backsync

Pyrra 0.10.2 copies SLO annotations to generated rule ConfigMaps. Under namespace
sync, host-generated ConfigMaps are otherwise imported into the virtual cluster.
The recreation exposed a lifecycle conflict involving copied vCluster tracking
annotations: generated rule ConfigMaps were imported and deleted, so healthy
SLO resources did not imply loaded app rules.

The chart's opt-in **`pyrraSLOs.skipBacksync`** emits
**`vcluster.loft.sh/skip-backsync: "true"`** on the SLOs. Pyrra propagates that
host-only backsync guard to its generated ConfigMaps, while normal source-SLO
to-host synchronization continues. The chart default is **false** for direct
deployments; xdev's local values enable it. Do not substitute a general skip-sync
annotation or manually manage generated ConfigMaps.

Revision 2 verified all four generated ConfigMaps retain their UIDs, the three
previously missing ConfigMaps remain present, and all 12 groups / 76 rules load
and advance with ABORT. Previously imported virtual copies were not manually
removed; a complete vCluster cold-restart lifecycle has not been tested. Shared
Pyrra/Thanos service configuration was not changed.

### Post-Recreation Verification

The full database/role restore matched all **76 table counts**, schema **20**,
ownership, effective/table/column/default privileges, forced RLS/policies,
memberships, sequences and application function definitions. Both saved encrypted
source configurations decrypt with the preserved keyring. Original backup files
remain unchanged; private recovery receipts are outside Git.

The app's five pods/seven containers are Ready without restarts. Both ingress
hosts and the unsuffixed Certificate Secret are restored. OTel receives current
samples for all seven identities with the original namespace/environment labels
and new originating pod UIDs. NFS is mounted read-only and listable. Both
provisioned Grafana dashboard UIDs are available.

Administrator authentication and expected denial of private user endpoints were
verified separately. With the supplied workout-user account, bounded smoke
checks verified private profile/source/workout reads, a temporary map selection,
nonempty route and Coverage MVT responses, Coverage path reads and Coverage-focus
geometry. The temporary map selection was deleted and session revoked. No test
credentials, coordinates or private response bodies were persisted in receipts.

The initial interactive map burst logged short-lived 502/503 responses followed
by successful tile requests. Their timing is consistent with request cancellation
when the UI replaces tile sources, but the API does not retain the underlying
error needed to prove that cause. Restored route tables were already autoanalyzed.
The bounded owner smoke had no unexpected statuses. These observations do not
declare the interactive experience error-free or the latency objectives met.

Completed historical OSM Jobs and old Pods/ReplicaSets/events were not replayed;
declarative workloads and controller-generated resources were recreated instead.

Application objectives use `pyrra.dev/v1alpha1`, `window: 2w`, percent-string
`target` and `partial_response_strategy: abort`. API availability is a ratio of
eligible 5xx attempts to eligible total attempts; API latency uses the within-
500ms histogram bucket over the same total population. Select this application's
seconds-based `http_server_request_duration_seconds_*` families and
`http_response_status_code` label, not legacy millisecond families/status labels.

Grafana's provisioned Tempo trace-to-metrics queries currently use legacy
`http_server_duration_milliseconds_*` names and `http_method`/`http_status_code`
labels. Those links need service-specific current names, seconds conversion and
label mappings before they work for this application; standalone PromQL below
does not repair the provisioned correlations.

Use monotonic counters/counts as SLI inputs, not already computed rates or a
percentile gauge. Latency objectives use the fraction of observations meeting
a bucket threshold. Outcome counters cannot measure a deadline for work that
never finishes, and superseded/cancelled/no-evidence need explicit policy.

### Operational Note

The Ansible-owned watcher must reload the **same pod's** Ruler listener, not a
readiness-gated/load-balanced Service. Service reloads can create a startup cycle:
the watcher waits for reload, while the Service excludes the pod until both
containers are ready. Use bounded retries for listener startup, keep certificate-
valid hostname/SAN verification, trusted CA and authentication enabled, and do
not mount a server private key into the watcher. Watcher readiness alone is not
reload proof: verify HTTP success, loaded app rule contents and advancing healthy
evaluations on every Ruler replica.

Treat **`pyrra-rule: "true"`** as an operator-controlled trust boundary: only
trusted, SLO-controller-generated rule ConfigMaps may carry it. The watcher
selects labels, not owners; reject unrelated selected ConfigMaps, unexpected URL
data keys and folder-override annotations. Do not manually manage generated rule
ConfigMaps. RBAC, reload credentials/rotation, watcher configuration and shared
monitoring lifecycle remain external Ansible responsibilities.

### Implemented Objectives

All four use **`window: 2w`** and `partial_response_strategy: abort` by default.
These are chart policy targets, not proof of production compliance or complete
two-week data. Targets are configurable under `pyrraSLOs.targets`.

| Objective | SLI Population / Policy | Target Over 2w | Chart Target Key |
| --- | --- | --- | --- |
| API backend availability | 5xx errors / eligible GET attempts on `/api/.*`; total includes only 2xx/3xx/5xx | 99.9% | `apiAvailability` |
| API interactive latency | Eligible GET attempts within 500ms / the same eligible GET population | 95% | `apiLatency` |
| Tile forwarding success | Nonsuccess outcomes / all observed forwarding attempts, including write failures | 99.9% | `tileSuccess` |
| Tile forwarding latency | Successful forwards within 1s / successful forwards only | 99% | `tileLatency` |

API policy excludes probes, non-GET operations, all 4xx (including 429 and
capability-renewal 404), and failures without a matching `/api/.*` route template.
A fast eligible 5xx meets the latency bound but fails availability: latency is
not a combined successful-and-fast objective. Tile policy starts immediately
before Martin forwarding and ends after body processing/write; pre-forward
authentication, validation and capability failures are excluded. A write failure
can have status 200 and still fails forwarding success. Neither tile objective
measures whole-user tile availability, browser fetch completion or map rendering.

### Future Objectives

These are not implemented by `pyrraSLOs.enabled`:

| Objective | Proposed SLI | Policy Still Needed | Readiness |
| --- | --- | --- | --- |
| Coverage completion/freshness | Eligible desired targets applied/no-evidence before a deadline, including expired queued/running targets | Choose deadline and target after load validation | Needs durable deadline/cohort measurement; attempt-duration histograms are insufficient |
| Ingestion/source freshness | Eligible ingest/source checks complete successfully within policy deadline | Set by manual/scheduled service expectations | Missing lifecycle/freshness producers and authoritative cohort denominator |
| OSM update reliability | Future durable stage/outcome/freshness observations for rare operator requests | Deferred; no Job SLO promise | Jobs have no active provider/final-flush implementation; manual region addition is intentional |
| End-user usable map | Successful interactions/initial render within 3s | Existing product target; choose compliance fraction | No production browser or synthetic user-journey metric |

### Implemented SLI Expressions

All numerator/denominator selectors use exact **`service_name="workouts-api"`**,
**`service_namespace`** and **`deployment_environment`** origin labels from chart
values. The xdev examples below match the template; they intentionally exclude
unlabeled legacy history and do not depend on `exported_job`. `service_instance_id`
keeps producer counters distinct before fleet aggregation. Inputs are raw counters
and histogram buckets, not precomputed rates or p95. `abort` applies to Thanos
Ruler evaluation; Prometheus ignores that field.

```promql
# Availability error/total inputs.
http_server_request_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",http_route=~"/api/.*",http_request_method="GET",http_response_status_code=~"5.."}
http_server_request_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",http_route=~"/api/.*",http_request_method="GET",http_response_status_code=~"2..|3..|5.."}

# API latency success/total inputs (500ms bucket, seconds).
http_server_request_duration_seconds_bucket{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",http_route=~"/api/.*",http_request_method="GET",http_response_status_code=~"2..|3..|5..",le="0.5"}
http_server_request_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",http_route=~"/api/.*",http_request_method="GET",http_response_status_code=~"2..|3..|5.."}

# Forwarding success error/total inputs (outcome, not only HTTP status).
workouts_api_tile_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",outcome!="success"}
workouts_api_tile_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev"}

# Forwarding latency success/total inputs (successful forwards only).
workouts_api_tile_duration_seconds_bucket{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",outcome="success",le="1.0"}
workouts_api_tile_duration_seconds_count{service_name="workouts-api",service_namespace="workouts-explorer",deployment_environment="xdev",outcome="success"}
```

### Alerts And No-Data Interpretation

The chart enables Pyrra's **absent-input** and **burn-rate** alerts. Missing inputs
are alertable, not numeric zero. Present counters with zero denominator increase
mean idle/no traffic, not proof of health; do not fill ratios with perfect health.
An absent error series alone is not proof that no errors occurred.

The installed CRD **does not support a severity override**. The chart uses Pyrra's
default alert severities only and exposes **no `alertSeverity` option**. Alertmanager
routing must match the actual generated alerts; do not document an unsupported
`spec.alerting` severity field as configurable.

**Operator caveat:** Pyrra **0.10.2**'s detail UI can show **100%** and **Total 1**
for empty data. That display is a UI bug, not evidence of one successful request,
valid input coverage or SLO compliance. Check raw selector results, denominator
increase, freshness and missing-input alerts before interpreting it. This work
does not promise an application-owned Pyrra fork or UI fix.

### Preconditions

- Verify traffic eligibility, counter reset/replica identity, missing/zero series,
  histogram bucket labels and numerator/denominator alignment. Do not silently
  replace a missing denominator with zero or declare idle services healthy.
- Pair sparse success/outcome counters with external availability, telemetry
  freshness and backlog/deadline alerts. Gauge saturation/oldest age are alerts,
  not automatically valid error/total event ratios.
- Verify the implemented eligibility/exclusion policies against real traffic;
  preserve genuine platform errors within those populations. A recoverable
  internal attempt is not always a failed user action.
- Make rule evaluation cover the **full window**. The shared Thanos Ruler pipeline
  is verified, replacing short-history local Prometheus evaluation for Pyrra
  outputs. Retention/downsampling and a healthy rule load do not prove complete
  newly labeled application history. No historical backfill is performed.
- The rollout verified virtual/translated host SLOs, generated labeled ConfigMaps,
  mounted-file contents, successful reload and all 12 advancing healthy app groups.
  Runtime/API datapoints retain the four origin aliases; instance IDs match the
  three originating xdev pod UIDs, not the gateway. API's exact 0.5s bucket exists.
  Tile inputs were absent after rollout because no authenticated map forwarding
  had occurred; their absent alerts are expected during that startup state, not
  proof of a forwarding failure. Verify tile inputs and the 1.0s bucket after
  normal map traffic. End-to-end alert delivery is not verified.
- New scoped history began with the rollout on **10/9/2026 around 5:43 PM PDT**.
  Initial API counters did not advance between retained samples, yielding zero
  increase/NaN availability; missing tile inputs yielded no availability. These
  are no-data states, not a two-week compliance result. A public `/api/config`
  GET was used as a read-only smoke check, not production load evidence.
- Final checks at **5:55 PM PDT on 10/9/2026**, Helm revision **104**, confirmed
  successful reload, all **12 app groups / 76 app rules healthy**, and all four
  friendly name labels retained in source, mirrors, ConfigMaps and loaded rules.
  The smoke read produced positive API observations: both API objectives recorded
  `pyrra_availability=1`. Both tile objectives still had no availability series.
  The Pyrra dashboard shows the four objectives and tile **No data**. This tiny
  validation sample is not a product load test or two-week compliance evidence.

## Recommended Work

| Priority | Addition | Why |
| --- | --- | --- |
| First | Tune DB histogram buckets; monitor delivered Go/native/self metrics and resource headroom | Go origin aliases and sidecar delivery/labels verified; peak capacity and application errors remain separate gaps |
| First | General-worker job/phase/outcome and file/workout counters; queue/oldest/slot state collector | Biggest operational blind spot; observed events plus durable state |
| First | Coverage reconciliation, backlog, deadlines, route quality and diagnostic admission/matching/release | Supports actual readiness/freshness SLOs instead of successful-attempt bias |
| Deferred | Basic Go runtime setup for rare OSM/operator/chart Jobs; later stage/outcome/freshness measurements and durable reader | Low-frequency work needs explicit final-flush design; not required for the five-Deployment metrics path |
| Next | Durable fleet statistics reader and authorized historical datasource | Exact daily facts/inventory without private health-data metric labels |
| Next | Browser Web Vitals/map-usable timing and redacted error reporting or synthetic journeys | Existing acceptance scripts are not live user monitoring |
| Before Claiming Compliance | App-specific rule/input/alert proof, full-window completeness and load evidence | Shared pipeline is healthy and four chart SLOs exist in source; metrics presence alone does not establish compliance |

Infrastructure exporters already used for PostgreSQL/storage remain separate
from app-owned metrics. Application/OSM database server telemetry additions are
explicitly deferred; no PostgreSQL exporter or aggregate SQL server metric is
implemented by these sidecars. Optional cluster-infrastructure dashboard panels
must identify their external datasource and verified coverage. Add scope-limited
measurements, bounded labels and tests first; avoid one exporter querying private
tables with superuser access.

### Deferred Job Telemetry

OSM update/identity-evaluation Jobs and bootstrap, application/OSM migration,
role-provisioning and timezone download/import hooks are rare, short-lived
operations. They have **no active telemetry provider or metrics endpoint today**;
endpoint environment variables alone do not initialize one. A collector's Go
runtime or forwarded scrape `up` must never be presented as the Job's parent
process metrics, completion, stale-result rejection or success rate.

The first deferred instrumentation candidate is **basic Go runtime only** in
the relevant Go commands, with explicit bounded provider shutdown/final metric
flush. No new Runner API or Job lifecycle instrumentation is implemented here.
Later candidates are bounded stage/total durations, terminal outcomes, stale-result
observations where meaningful, last-success/result freshness from durable state,
and final-flush duration/failure visibility. These are future measurements, not
existing family names. Scrapes/30s periodic export can miss the whole Job lifetime;
flush cannot recover crashes or ambiguous commits, so exact history requires a
durable reader. Keep Kubernetes Job status/logs as current evidence and do not
promise a sparse Job percentage SLO. Deployment metrics validation does not
validate these deferred Jobs, application DB telemetry, or OSM DB telemetry.

### Privacy Boundary

Application-owned metric dimensions are bounded and omit personal identifiers.
This does not prove blanket trace/log redaction: default HTTP spans include actual
paths/client metadata, and the DB tracer can record raw exception text despite
SQL/parameter attributes being disabled. Review/drop those sensitive trace fields
before broad dashboard/Tempo access. Monitoring health/workout data must respect
owner access, retention and deletion policy.

## Sources And Verification

Source anchors: `internal/telemetry/telemetry.go`, `internal/telemetry/metrics.go`,
`internal/telemetry/metrics_test.go`, `internal/telemetry/grpc_test.go`,
`internal/telemetry/telemetry_test.go`, `internal/config/config.go`,
`internal/database/database.go`,
`worker/coverage_metrics.go`, `worker/coverage_runner.go`, `api/maps.go`,
`api/coverage_diagnostics.go`, worker/OSM command entrypoints, Helm deployment,
Service, NetworkPolicy and config templates, `helm/templates/collector-configmap.yaml`,
`helm/templates/_collector.tpl`, `tests/helm/collector_test.go`,
`tests/helm/collector_runtime_test.go`, `ui/nginx.conf`, `helm/values.yaml`,
`helm/templates/pyrra-slos.yaml`, `tests/helm/pyrra_test.go`,
`deploy/dev/otel-dns.yaml`, and the pinned Go module sources.
Architecture's minimum-metric list is intended scope, not implemented inventory.

The pinned [nginx receiver schema](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.156.0/receiver/nginxreceiver/metadata.yaml)
defines the four native instruments/units/states; the pinned
[Prometheus name translator](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.156.0/pkg/translator/prometheus/normalize_name.go)
explains expected classic suffixes, not live gateway output. Martin's
[1.15.0 server source](https://github.com/maplibre/martin/blob/martin-v1.15.0/martin/src/srv/server.rs)
registers `/_/metrics` and masks unmatched route patterns. Root
[README](../README.md) covers product/acceptance evidence, not live metrics
verification; [contributor guidance](../CONTRIBUTING.md) covers local setup.
The Collector's pinned [process schema](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.156.0/service/metadata.yaml)
defines self process types/units (including monotonic uptime and total allocations);
[receiver helper](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.156.0/receiver/receiverhelper/metadata.yaml)
and [exporter helper](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.156.0/exporter/exporterhelper/metadata.yaml)
schemas define point counters and feature-gated semantics. Those sources alone
did not establish final host translation; the subsequent live audit supplies the
actual family/label spellings in this guide.

Source/export checks, runnable without cluster changes:

```sh
go test ./internal/telemetry ./internal/config
go test ./tests/helm -run 'TestPyrraSLO' -v
go test ./tests/helm -run 'TestCollector|TestNginxStatusIsLoopbackOnly' -v
# Optional: use an already available pinned vendor binary, not a custom proxy.
OTELCOL_CONTRIB_BINARY=/path/to/otelcol-contrib-0.156.0 go test ./tests/helm -run 'TestCollectorConfiguration|TestCollectorNativeAndSelfExport' -v
```

The opt-in vendor test asserts native and self OTLP names, service resource/
stable point identity, nginx states, and Martin histogram/counter/label preservation
across HTTP/HTTPS/gRPC/gRPC-TLS fixtures. It does **not** assert final host Prometheus
name translation or live nginx/Martin data. Go TLS tests cover all three service
names, runtime baseline with/without a finite memory limit, trusted CA/SAN success,
wrong-host/untrusted-CA/plaintext rejection, and explicit empty disable behavior.
The Pyrra tests cover opt-in rendering, targets/window, origin-scoped selectors,
exclusions, labels and schema validation; Go identity tests cover exporter aliases.
These commands do not verify the app rollout, live SLO inputs/evaluation/alerts,
deferred Jobs, DB exporters or installed dashboards.

Historical read-only live checks: homelab Collector/ServiceMonitors, Prometheus targets,
family metadata/labels/buckets, Pyrra objectives/generated rules, retention and
Thanos/Pyrra arguments; deployed Martin help and the **insufficient `/metrics` probe**.
The 10/5 source correction did not relabel that old probe as a successful
`/_/metrics` check. The later finalized **REV96 live audit** at
`/tmp/opencode/workouts-metrics-verification.md` independently verified the
correct endpoint and all seven producer identities; its observations are captured
in the operational section so the temporary report is not the only record. No jobs,
private workout reads, API mutations, application deploys or monitoring changes
were used to force samples. Audit-owned forwards were removed.

Related documents: [Coverage metrics](coverage-metrics.md),
[acceptance profiles](coverage-acceptance.md), and
[acceptance results](coverage-acceptance-results.md). Acceptance snapshots are
test evidence, not live scrape endpoints or production dashboard history.
