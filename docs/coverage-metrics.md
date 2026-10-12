# Coverage Metrics

**Identity/SLO status updated 10/9/2026** (`America/Los_Angeles`): Go origin
aliases and four API/tile SLOs are live-verified at Helm revision **104**. All four
source/mirrored objectives and **12 app groups / 76 rules** are healthy. API inputs
and a read-only smoke sample populated both API objectives; both tile objectives
remain **No data**. This is not two-week compliance or tile forwarding proof.
Coverage completion/freshness SLOs remain future work.

**Historical live audit: 10/5/2026.** Final snapshot: **10/5/2026 9:27:21 PM PDT**
(`America/Los_Angeles`), Helm revision **96**. Five application producers and both
sidecar self identities deliver metrics to the gateway and host Prometheus.
No monitoring resources are installed by this document. The dated xdev checks
below remain historical evidence, not a new rollout receipt.

For the complete component inventory, live collection verification, operational
versus historical dashboard models and implemented/future Pyrra SLOs, see
[Telemetry and monitoring](telemetry-and-monitoring.md).

Coverage uses the existing shared OTel provider and OTLP export configuration,
with instrumentation scope `github.com/erhhung/workouts-explorer/worker/coverage`.
Concurrency remains one route per runner. All durations are seconds; counters
use `{event}`. No metric attributes contain job, route, account, worker or region
identifiers, coordinates, raw errors or failure summaries.

Schema 21 grants the Coverage role SELECT on job `created_at`, `attempt` and
`retry_of_job_id` only, retaining existing account RLS and denying broad table or
private-column access. Runtime readiness requires these grants. Loading reads
metadata directly with account, running-status, worker and lease-token fences;
there is no runtime privilege fallback. Timeout-backoff observations continue to
use the existing database function.

## Export And Resources

Helm `otel.endpoint` and common Go configuration now default to
`grpcs://otel-gateway-collector.observability.svc.cluster.local:4317`.
An unset `OTEL_EXPORTER_OTLP_ENDPOINT` uses this default; an explicitly empty
value disables provider/runtime setup for API, general worker and Coverage worker.
Metrics export every 30 seconds; nonempty Go setup also enables traces. There is
no Go application Prometheus scrape endpoint or new application ServiceMonitor.
The chart still has no ServiceMonitor/PodMonitor; the 10/5 audit found xdev lacked
those CRDs. This is not a missing-Pyrra-CRD claim: xdev SLO custom-resource sync is
now enabled as described in the telemetry guide.

Go gRPC TLS uses `x509.SystemCertPool`, respects the mounted CA selected by
`SSL_CERT_FILE`, verifies SAN/hostname and requires TLS 1.2 or newer. Invalid trust,
hostname or a plaintext server causes export failure, never plaintext fallback.
HTTP/HTTPS export remains available as an explicit override, including the local
acceptance endpoint `http://acceptance-telemetry:4318`. The previous
`https://otlp.fourteeners.local:4318` path is historical, not the current default.

The five long-running components have live-verified collection paths. UI and Martin
now have digest-pinned OTel Contrib **0.156.0** sidecars: the UI nginx receiver
reads `http://127.0.0.1:8083/nginx_status`, and the tiles Prometheus receiver reads
Martin **1.15.0** at `http://127.0.0.1:3000/_/metrics`. Martin native metrics are
enabled in the standard image without a metrics switch; `/metrics` was the wrong
probe. Each sidecar separately exports its own collector process telemetry from
loopback port 8888. No status route is publicly served or exposed through the API
proxy; collection needs no additional Prometheus scrape or ingress/policy opening.
An empty Helm endpoint omits both sidecars; `otel.collector.enabled=false` disables
sidecars independently of Go export. No PostgreSQL/PVC change is required, and
application/OSM database server telemetry additions remain deferred.

For xdev, `deploy/dev/otel-dns.yaml` forwards only `observability.svc.cluster.local`
on CoreDNS port **1053** to homelab DNS **`10.43.0.10`**. UI and all three Go pods
resolve the exact gateway FQDN to **`10.43.84.114`**, the homelab Service IP.
A direct Service-port-forward handshake passed public-CA trust and exact-hostname
validation with TLS 1.3/ALPN `h2`; actual running Go endpoint/CA settings and
delivery confirm the private gRPC path. Both custom server keys, the custom import
and six-record `NodeHosts` entry are present and existing fourteeners resolution
works; there is no prior snapshot proving byte-for-byte restoration. Any approved
DNS change must use a **merge patch**, not server-side apply of a partial
ConfigMap; unrelated DNS keys were removed by an earlier apply and restored.
The exact operator command and preservation precautions are in
[Private DNS and reachability](telemetry-and-monitoring.md#private-dns-and-reachability).

### Producer Identity

The **10/5 audit's** Go query contract was `job="otel-gateway-collector"` with
bare `exported_job="workouts-coverage-worker"` (or API/general-worker service name).
The host monitor drops `instance`/`endpoint` and other scrape labels; its `pod`
identifies the gateway, not the producing worker. Go origin preservation is now
implemented in `internal/telemetry/metrics.go`: the exporter projects only
`service_name`, `service_namespace`, `deployment_environment` and
`service_instance_id` onto metric points. Helm sets standard
`OTEL_RESOURCE_ATTRIBUTES`, including downward-API pod UID, for all three Go
services. The new host identity is namespace/service, e.g.
`workouts-explorer/workouts-coverage-worker`; revision **104** live checks verified
all three Go services' origin aliases and matching producer pod UIDs.
Retained bare Go history remains only in Grafana's compatibility regex, not the
four app SLI selectors. Its namespace/environment cannot be recovered from the
series; no historical backfill is performed.

For sidecars, `metrics/native` resources use `service.name=workouts-ui` or
`workouts-tiles`; `metrics/self` resources use `workouts-ui-collector` or
`workouts-tiles-collector`. Processors project the stable datapoint labels
`service_name`, `service_namespace`, `service_instance_id` (**pod UID**) and
`deployment_environment`. The live namespace is **`workouts-explorer`** and
environment **`xdev`** (chart default remains `development`). Gateway conversion
supplies namespace/service `job`, which host scraping retains as `exported_job`,
not the receiver's local scrape job:

| Path | Actual Host `exported_job` |
| --- | --- |
| UI native | `workouts-explorer/workouts-ui` |
| Martin native | `workouts-explorer/workouts-tiles` |
| UI self | `workouts-explorer/workouts-ui-collector` |
| Tiles self | `workouts-explorer/workouts-tiles-collector` |

At the 10/5 audit, Go values were unprefixed and lacked the projected aliases;
that historical observation does not describe the new exporter contract.
All actual native/self points retained
the four aliases, with one producer pod UID per identity observed, not broad
multi-replica validation. Generated `target_info` instead uses resource key
`deployment_environment_name` and exported identity, not the four point aliases;
exclude it from point-label checks and never select it with `service_name`.
Martin `endpoint` becomes `martin_route` before host `endpoint` label removal;
`method`/`status` retain native names. Exact namespace/environment selectors apply
to new labeled Go points too, not legacy unlabeled history. Do not interpret
collector self-runtime/local scrape `up` as worker/Job readiness or completion.

### Resource Envelopes

UI, API, Martin, general worker, Coverage worker, OSM operator Jobs, and chart
hooks start with requests of 25m CPU/64Mi memory and limits of 250m CPU/256Mi
memory. Each component keeps an independent resource block for later tuning.
The two new collectors each have their **own** 25m/64Mi request and 250m/256Mi
limit, with `GOMEMLIMIT=192MiB`; UI/tiles pod totals now include their sidecar.
Collector limits are not evidence of nginx/Martin/worker peak usage.
Latest sidecar RSS is UI **158,969,856 bytes** and tiles **194,285,568 bytes**
(approximately 152/185 MiB), below 256Mi but above the 64Mi request. All seven
containers have zero restarts at the snapshot. Peak memory, OOM safety and
load-sizing remain unverified; Go heap/`GOMEMLIMIT` is not total RSS. No resource
envelope is changed by this update.
The operator's existing action-specific ephemeral-storage bounds remain separate.
These limits are provisional: imports, timezone loading, diagnostic matching, and
concurrent password hashing must be checked under load rather than inferred from
idle usage. Keep global/account/region matcher slots at one.

The pre-rollout xdev idle sample on 10/4/2026 was API 1m/18Mi, Coverage worker
2m/12Mi, Martin 1m/13Mi, UI 1m/4Mi, and general worker 3m/8Mi, read through host
cluster metrics because the vCluster Metrics API is unavailable. No Coverage
children were queued or running. This is an idle baseline, not a backlog benchmark
or evidence that importer peaks fit 256Mi.

## Instruments

The eight owned Coverage instruments are part of the **35-name union across the
three Go services** (nine application-owned and 26 shared/library instruments),
not 35 names per component or the total for the five-component/native/self path.
Martin's two native HTTP families, nginx's four instruments and sidecar self
metrics are separate. Shared Go runtime metrics do not imply collector CPU/uptime
metrics exist on the application Go processes or deferred Jobs.

| Instrument | Meaning | Attributes |
| --- | --- | --- |
| `coverage.queue.age` | Job `created_at` to claim completion, observed during account-scoped loading only when `attempt=1`; clock skew clamps to zero | None |
| `coverage.matcher.duration` | Actual `routepipeline.Evaluate` call, including unsuccessful calls; excludes admission, snapshot setup, generation verification, preparation, persistence and cleanup | None |
| `coverage.route.duration` | Every observed postclaim attempt, including invalid claims, loading, admission, matching, persistence and cleanup; not a durable-outcome latency | None |
| `coverage.admission.wait` | Full admission loop until acquisition, error or interruption | None |
| `coverage.route.outcomes` | Confirmed committed terminal transitions, including persistence and detached failure/supersede/cancel helpers | `outcome`: `applied`, `no_evidence`, `superseded`, `failed`, `cancelled`; `failure`: `none`, `timeout`, `limit`, `invalid`, `route_read`, `persist`, `osm`, `other` |
| `coverage.control.calls` | Admission decisions, heartbeat results, release results and rollback errors | `operation`: `admission`, `heartbeat`, `release`, `rollback`; `result`: `acquired`, `denied`, `renewed`, `lost`, `released`, `not_found`, `error`, `timeout`, `cancelled` |
| `coverage.retry.attempts` | Attempts observed during loading: same-job recovery (`attempt>1`), explicit retry (`retry_of_job_id` present), timeout backoff (`timeout_retry_count>0`) | `kind`: `recovery`, `retry`, `timeout_backoff` |
| `coverage.stale.rejections` | Confirmed ownership-fenced rejection of terminal transitions; never counted as durable completion | `operation`: `persist`, `fail`, `supersede`, `cancel` |

Expected Prometheus families for these owned instruments:

| Family | Type / Unit |
| --- | --- |
| `coverage_queue_age_seconds` | Histogram, seconds |
| `coverage_matcher_duration_seconds` | Histogram, seconds |
| `coverage_route_duration_seconds` | Histogram, seconds |
| `coverage_admission_wait_seconds` | Histogram, seconds |
| `coverage_route_outcomes_total` | Counter, events |
| `coverage_control_calls_total` | Counter, events |
| `coverage_retry_attempts_total` | Counter, events |
| `coverage_stale_rejections_total` | Counter, events |

Histograms expose `_bucket`, `_sum`, `_count` with boundaries
`.01,.1,.5,1,5,10,30,60,180,300,600,1800,3600,+Inf` seconds. They have no
measurement attributes; the counter labels are bounded as listed above.
Synchronous instruments are sparse: no measurement means no series, **not zero**.
All eight remained absent in the latest six-hour query; do not fill absent outcome
counts/latency with zero or infer an empty queue. These remain source-defined
families/boundaries, not live-verified owned Coverage names/bucket spellings. Do
not manufacture production work to populate them during a read-only audit.

Failure categories are allowlisted, with unknown failure codes mapped to `other`.
Successful outcomes always use `failure=none`. Outcome counters are emitted only
after successful commits and affirmative boolean results, or a non-NULL
persistence outcome. Failed or ambiguous commits emit no durable outcome.
Stale persistence results do not trigger a second failure transition.

API forwarding exports `workouts.api.tile.duration` in seconds from immediately
before the Martin request through the response-body write. Attributes are limited
to `tile_kind` (`route`/`coverage`), `outcome` (`success`, `upstream_error`,
`invalid_response`, `body_error`, `too_large`, `write_error`), and the bounded API
response status. It excludes authentication/capability validation and is not
client-perceived rendering latency. Existing HTTP server metrics complement it.

Both worker pools and the API diagnostic OSM pool use the privacy-safe shared
`otelpgx` instrumentation. Pool metrics include `pgxpool.acquire_duration`,
`pgxpool.empty_acquire_wait_time`, `pgxpool.canceled_acquires`, and connection
counts under fixed application/pool names. Acquire/wait durations are cumulative
nanoseconds, not per-request percentile histograms. Database operation durations
and errors remain available without SQL, parameters, or connection details.

Heartbeat control transactions, claims and boolean control transactions are
bounded to five seconds. Cleanup rollback uses a fresh, bounded context rather
than a cancelled route context. Release failures are counted and logged; a
missing slot is counted separately and may reflect normal terminal cleanup.
Heartbeat `renewed` means the renewal transaction committed, even if that same
transaction observed a cancellation request. `lost` means the database rejected
renewal, not that a database error proved ownership loss.
Heartbeat control errors cancel the route operation; they do not themselves emit
a terminal outcome. Any subsequent failure/cancellation outcome still requires
the database-fenced terminal transition to commit successfully.

## Limitations And Followups

- Queue age is creation-to-first-claim, not per-recovery waiting time. It is not
  observed if claim validation or metadata loading fails, or the process crashes
  before the observation. Explicit retry jobs have their own creation time.
- Retry kinds overlap: an explicit timeout retry can also be a recovered claim.
  Do not sum kinds as a count of distinct jobs. Retries that fail before loading
  metadata are not observed.
- Admission `denied` counts polling decisions, not distinct waiting routes. The
  database boolean does not distinguish global/account/region saturation from
  an invalid/stale claim. Admission errors leave existing lease recovery behavior
  unchanged; they do not imply a durable failed route.
- Metrics cover this worker only. Database-only reconciliation/cancellation,
  crashes, unobserved lease expiry and ambiguous commits are not reconstructed;
  this is operational telemetry, not an exactly-once durable ledger.
- No backlog, oldest-waiting-job or slot-occupancy gauges are added. Matcher
  histograms omit attempts that never reach `Evaluate`; route histograms include
  interrupted and stale attempts and should not be equated to completed work.
- Minimal API diagnostics telemetry (admission decisions/wait, matcher duration,
  bounded failure categories and release errors) remains a separate followup.
  CoverageRunner end-to-end validation is implemented; it does not reconstruct
  missing operational observations or provide continuous historical monitoring.

### Deferred Jobs And Databases

OSM update/identity-evaluation, bootstrap, application/OSM migration,
role-provisioning and timezone download/import Jobs have no active OTel provider
or endpoint today. Basic Go runtime setup is the first **deferred** candidate for
their Go commands, with bounded final metric flush/shutdown so a 30s interval
does not miss the whole run. Later candidates are stage/total durations, outcomes,
stale-result observations where applicable, durable last-result freshness and
final-flush duration/failures. No new Runner API, Job instruments, family names
or Job SLO commitments are introduced. Logs, durable state and Kubernetes Job
status remain their evidence; collector process data is never parent-Job telemetry.
Application/OSM DB server exporter work is also deferred. Optional infrastructure
panels use existing external sources, not invented application SQL server metrics.

## Operational Verification (10/5/2026)

The finalized report `/tmp/opencode/workouts-metrics-verification.md` records
the **10/5/2026 9:27:21 PM PDT** snapshot. `workouts-dev` revision **96** is
deployed, with ready pod/container counts 1/1/1/2/2 and all seven containers at
zero restarts. Gateway and host agree on the following source inventory, excluding
host-generated recording-rule series:

| Service Resource Identity | Families | Series |
| --- | ---: | ---: |
| `workouts-api` | 26 | 1,152 |
| `workouts-worker` | 24 | 212 |
| `workouts-coverage-worker` | 24 | 212 |
| `workouts-ui` | 5 | 8 |
| `workouts-tiles` | 8 | 66 |
| `workouts-ui-collector` | 35 | 270 |
| `workouts-tiles-collector` | 31 | 233 |

Total **2,153 source series**: 1,650 across five application identities and 503
self series. UI's four native families have seven native series plus generated
`target_info`; Martin has two HTTP families, five local scrape-health families
and `target_info`. Self counts include their generated `target_info`. Shared
families repeat across identities; these counts must not be summed as distinct
instrument names or equated to traffic counts.

Live corrections: Martin's histogram is **`martin_http_requests_duration_seconds`**,
not the fixture's `martin_http_request_duration_secs`. Its live route is
`/{source_ids}/{z}/{x}/{y}`, `method="GET"`, with successful native status
**200 or 204**. Host bucket labels include `1.0`, `5.0`, `10.0`, not the source
integer spellings. nginx families are `nginx_connections_accepted_total`,
`nginx_connections_handled_total`, `nginx_connections_current` (states active,
reading, waiting, writing) and `nginx_requests_total`, without duplicated units.

The six actual basic collector process families are
`otelcol_process_cpu_seconds_total`, **`otelcol_process_memory_rss`**,
**`otelcol_process_runtime_alloc_bytes_total`**, `otelcol_process_runtime_heap_alloc_bytes`,
`otelcol_process_runtime_total_sys_memory_bytes`, **`otelcol_process_uptime_total`**.
RSS is bytes despite no `_bytes` suffix, uptime is seconds despite no `_seconds`
suffix, and the allocation name differs from the source `total_alloc_bytes`.
The complete 31-family shared self inventory and four UI-only additions, units/
semantics, corrected five-component PromQL and observed bucket spellings are in
[Telemetry and monitoring](telemetry-and-monitoring.md).

Over 75 seconds, allocated bytes increased on API/general/Coverage workers by
770,848 / 1,342,936 / 1,420,640; nginx requests increased 32, Martin health/metrics
requests 23, and UI/tiles collector CPU 0.10/0.22 seconds. This is fresh source
data, not merely refreshed scrape timestamps. Receiver accepted and exporter
sent point rates were positive; receiver failed/refused counters were actual
zeros, queues 0/10. `otelcol_exporter_send_failed_metric_points_total` and
`otelcol_exporter_enqueue_failed_metric_points_total` were **absent**, not zero.
Exporter identity is `otlp_grpc`. Forwarded local `up` belongs to Martin/self
scrapes; nginx has no Prometheus `up`, and gateway scrape `up=1` is separate.

All three Go processes have seven live runtime families, not an assumed larger
baseline; finite `go_memory_limit_bytes` is absent. Coverage's eight owned families
remain absent, and worker DB errors are absent; shared runtime export health
cannot establish job progress/backlog. Current pool capacities are API main 6,
diagnostic 1, general-worker app/OSM **6/6**, Coverage app/OSM **4/3**; the older
general-worker 10/10 observation is historical, not the current setting.

**Application health is distinct:** the final API owned tile snapshot includes
111 route/upstream-error observations with status 502, alongside route/success=85
and coverage/success=20. Transport/TLS/delivery health does not imply private-tile
success or diagnose these errors. Existing 502/503 populations need a separate
authorized investigation; retained six-hour requests may predate REV96.

The audit used reads/public-certificate verification and safe pod-local UI/Martin
health/static/status/metrics probes only. It created no authenticated/private
workload, synthetic OTLP call, Job, retry, import, region operation or SQL query;
application traffic grew independently. No cluster resources/configuration were
changed, and all audit-owned forwards stopped. Deployment collection validation
does not validate deferred Jobs, DB exporters, full historical completeness,
external browser journeys or SLOs, nor install dashboards/CI rules/Pyrra resources.

Source checks: `internal/telemetry/grpc_test.go` asserts metrics/traces for all
three Go service names, baseline runtime metrics and finite-memory-limit behavior,
CA/SAN verification and failure without plaintext fallback. The HTTP regression
test remains in `internal/telemetry/telemetry_test.go`. `tests/helm/collector_test.go`
checks rendered config/security/private status access; opt-in
`TestCollectorNativeAndSelfExport` in `tests/helm/collector_runtime_test.go` uses the
pinned vendor binary to assert separate native/self resources, stable point labels,
nginx states and preserved Martin counters/histograms. Its synthetic endpoints
are not live Martin/nginx samples or proof of final Prometheus translation.

```sh
go test ./internal/telemetry ./internal/config
go test ./tests/helm -run 'TestCollector|TestNginxStatusIsLoopbackOnly' -v
OTELCOL_CONTRIB_BINARY=/path/to/otelcol-contrib-0.156.0 go test ./tests/helm -run 'TestCollectorConfiguration|TestCollectorNativeAndSelfExport' -v
```

The last command is opt-in and requires the actual pinned binary. A skipped
test is not export evidence. [README](../README.md) and the acceptance documents
describe measured synthetic profiles, not production telemetry/SLO validation.
The earlier source-only update passed telemetry/config and Helm rendering/schema/
private-status tests; its vendor runtime test skipped because
`OTELCOL_CONTRIB_BINARY` was unset. The later supplied live audit establishes
actual delivery and names independently, not by treating that skipped fixture
as evidence. This documentation edit performs no new cluster check or change.

## Acceptance Still Required

Before increasing concurrency, record a representative resource-capped Coverage
backlog alongside Summary, Routes, Coverage tiles, focus overlays and ordinary API
requests. Include long routes and competing diagnostics. Capture matcher/route
latency, queue/admission wait, pool contention, timeouts, heartbeat failures,
retries/stale rejection, interactive API/tile p95, CPU throttling, peak working set,
and OOM/restart counts. Verify worker restart/reclaim, cancellation, and stale
targets with controlled fixtures rather than mutating live workouts implicitly.
The 10/11/2026 closeout decisions move these capped-load and typical/stress
profile checks to Milestone 11. Tablet UI/UX and device-specific visual work
belong to Milestone 10. The remaining measurements are required before capacity
claims or concurrency increases, not passing evidence inferred from scope acceptance.

The two synthetic profiles and worker integration matrix are specified in
[Coverage acceptance](coverage-acceptance.md); measured profile results are in
[Coverage acceptance results](coverage-acceptance-results.md). They do not certify
archive-scale capacity or claim unexecuted profile-specific browser/backlog checks.

Broader dashboard/alert coverage and comprehensive diagnostic/fleet telemetry move
to Milestone 9; the four implemented API/tile chart SLOs do not provide those
Coverage signals. Typical/stress profiling belongs to Milestone 11; final
production resource sizing, broader scaling and security hardening belong to
the final Milestone 12. Neither idle samples nor exported instruments alone
satisfy performance acceptance.

## Historical Xdev Verification (10/4/2026)

The schema-21 observations below describe the original run. On 10/6/2026, the
approved development-only consolidation moved its three column grants into
migration 015 and returned current schema/Goose support to 20, minimum runtime
18. Compatible binaries rolled out first; the obsolete version record and schema
number were then repaired under the migration lock without revoking permissions
or modifying workout data. Fresh/upgrade/downgrade integration tests verify the
consolidated history. This is not permission to rewrite other deployed histories.

This initial configuration/idle check predates the subsequently executed
typical/stress workload in [Coverage acceptance](coverage-acceptance.md). The
later 10/4 Go audit and finalized 10/5 REV96 live audit are separately dated
in [Telemetry and monitoring](telemetry-and-monitoring.md); none of the following
revision/readiness/sample statements is a new live rollout verification.

- Helm revision 92 is deployed. All five application Deployments are ready with
  the exact requested envelope; their current pods have zero restarts. API
  `/health/ready` returns `{"status":"ok"}`. Existing-data OSM migration and
  timezone hooks also completed under the same limits; no regional rebuild ran.
- Application schema/Goose version is 21, minimum runtime remains 18. The three
  metadata grants are present without broad Coverage-worker job-table SELECT.
  Real disposable PostgreSQL 18.6/PostGIS 3.6.4 migration tests pass, including
  grants/RLS, Down/Up, readiness, legacy upgrade, and exact facility-read restoration.
- Enabling OTLP exposed an SDK resource-schema mismatch (1.41 versus a pinned
  1.39). Setup now adds stable service-name attributes without replacing the SDK
  schema. A real HTTP-exporter regression test verifies metrics and trace exports.
  The initial failed rollout retained the prior ready API/worker pods; the fixed
  images rolled out successfully before revision 92 completed.
- At 10/4/2026 3:44AM PDT (`America/Los_Angeles`) the collector scrape is healthy (`up=1`, 30-second interval),
  receiver failures/refusals and Tempo export failures have not increased since
  startup, and all three Go services export runtime metrics plus all 13 pgxpool
  instruments for each of their two pools. The diagnostic OSM pool has its fixed
  name and maximum of one connection; no diagnostic work was invoked.
- Prometheus uses `job=otel-gateway-collector` and `exported_job` for the application
  service; `db_client_connection_pool_name` distinguishes pools. Actual cumulative
  wait instruments are `pgxpool_acquire_duration_nanoseconds_total` and
  `pgxpool_empty_acquire_wait_time_nanoseconds_total`. The collector's `pod` label
  is not the application pod. Recent samples and increasing counters verify export.
- Go unit tests/vet, targeted race tests, 179 UI Vitest tests, typecheck/build, and
  Helm/resource-rendering checks passed at that verification. The ordinary UI test
  wrapper then failed its Data Sync source/artifact spacing assertion. Broader
  database-backed API retry/map-session and worker-ingest fixtures also had failures;
  this is historical evidence, not a current test-suite status or a green full
  integration suite.
- No live Coverage children were queued/running or explicitly triggered during
  acceptance verification. Queue/matcher/lease/outcome instruments are validated
  through the in-memory exporter tests; their live workload distribution and the
  resource-capped backlog/interactive benchmark remain unverified.
