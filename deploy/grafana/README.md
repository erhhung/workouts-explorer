# Workouts Explorer Grafana Dashboards

Exactly two classic dashboards, generated from `generate.cjs`:

**Dashboard documentation updated 10/6/2026.** Final Helm migration and browser
verification are pending; the earlier live query checks below remain dated evidence.

| Title (literal slash, not a folder) | Stable UID | Helm-managed ConfigMap |
| --- | --- | --- |
| Workouts Explorer / Operational | `workouts-explorer-operational` | `workouts-explorer-operational-dashboard` |
| Workouts Explorer / Historical | `workouts-explorer-historical` | `workouts-explorer-historical-dashboard` |

The builder is the single source for dashboard JSON. The application Helm chart
owns the ConfigMap wrappers in **xdev**, namespace **`workouts-explorer`**. vCluster
syncs them to the homelab host cluster for Grafana discovery; they are no longer
manually managed in homelab `monitoring`. Standalone ConfigMap YAML files have
been removed. Generation creates no CRDs, datasource, provider,
recording rules, SLOs or cluster deployments and changes no application packages.

## Artifact Contract

`artifacts()` returns four paths relative to `deploy/grafana/`:

```text
operational.json
historical.json
../../helm/dashboards/operational.json
../../helm/dashboards/historical.json
```

The CLI writes both deploy JSON files and **byte-identical Helm mirrors**, creating
`helm/dashboards/` if needed. Chart templates consume the mirrors with
`.Files.Get "dashboards/operational.json"` and
`.Files.Get "dashboards/historical.json"`. Do not hand-edit the mirrors or maintain
a second builder. The chart/main agent owns wrapper metadata and resource lifecycle;
this generator does not produce ConfigMaps or implement chart deployment.

## Generate And Validate

Run from the repository root with Node's built-in test runner; no dependencies:

```sh
node deploy/grafana/generate.cjs
node deploy/grafana/generate.cjs --check
node --test deploy/grafana/generate.test.cjs
npm --prefix ui run format
npm --prefix ui run format:check
```

The existing formatter uses Prettier **3.9.9** and scans repository JS/TS/CSS;
it excludes generated JSON/YAML and byte-sensitive acceptance artifacts. Do not
use it to rewrite unrelated concurrent source edits. `--check` is read-only,
compares all four generated files byte-for-byte, and exits nonzero on missing or
stale files, including missing/drifted Helm mirrors. It does not create directories
or write files. Change the builder, regenerate, and run checks instead of editing
JSON. Generation is deterministic and never connects to Kubernetes. Tests preserve
the exact definitions/order of all **92 existing query targets** across 48 metric
panels and validate recursive collapsed-row IDs and layout.

## Helm Deployment

Regenerate the deploy JSON and Helm mirrors, then update the application release
from the repository root using the existing local values file:

```sh
node deploy/grafana/generate.cjs
node deploy/grafana/generate.cjs --check
node --test deploy/grafana/generate.test.cjs
helm --kube-context xdev upgrade workouts-dev ./helm \
  --namespace workouts-explorer --values helm/values-xdev.local.yaml --wait
```

This is an operator deployment command. Generation and local tests never install
or update cluster resources. Revision **98** deployed both ConfigMaps through
`workouts-dev` on **10/6/2026**; the two original manually managed homelab
ConfigMaps and the temporary sync probe were removed after the synced copies
and Grafana reloads were confirmed.
The chart's `grafanaDashboards.enabled` controls both ConfigMaps. Test releases
must disable them to avoid replacing dashboards with the same stable UIDs.

The supplied homelab Grafana **13.1.3** stateful
`prometheus-stack-grafana` in `monitoring` has automatic discovery for the string
label `grafana_dashboard='1'`, with `NAMESPACE=ALL`. The Helm wrappers retain that
string label and `app.kubernetes.io/part-of: workouts-explorer`, use the release
namespace `workouts-explorer`, and set **`vcluster.loft.sh/force-sync: 'true'`** so
vCluster copies these unmounted ConfigMaps to the host. A live sync probe on
**10/6/2026** confirmed the copied label; final checks also verify Helm ownership,
host copies and removal of the old sources. The existing sidecar
writes dashboard files to `/tmp/dashboards`; its existing file provider imports
them into the default/root folder. No folder annotation is supplied or invented.
The slash in each exact title is literal title text, not a folder instruction.
Stable UIDs and JSON filenames allow updates without creating additional
dashboards. The files use classic schema version 41 and modern field config with
only row, text, stat and timeseries panels. Existing sidecar discovery/provider
reload handles updates; no Grafana restart, new provider or datasource is required.

The updated build was verified through Grafana API and an authenticated browser
on **10/6/2026**. Search returns exactly two dashboards with their original
titles/UIDs, source/API JSON matches, and `editable: true`. Namespace/Environment
resolve correctly; Notes are initially collapsed; both dashboards enter edit mode.
The earlier 92-target query sweep passed, and tests prove all 92 definitions remain
unchanged. This update does not claim a new query-performance acceptance result.
The generator itself never deploys resources.

Open the dashboards:

- [Workouts Explorer / Operational](https://grafana.fourteeners.local/d/workouts-explorer-operational/workouts-explorer-operational)
- [Workouts Explorer / Historical](https://grafana.fourteeners.local/d/workouts-explorer-historical/workouts-explorer-historical)

## Editing And Navigation

Both JSON dashboards set **`editable: true`**; the previous read-only label is
removed. Users with Grafana edit permissions can edit and export, or use **Save As**
where their folder/create permissions allow.
The existing file provider still has **`allowUiUpdates: false`**, so saving edits
back to the same provisioned dashboard is blocked. Unsaved in-browser edits are
not a source change; provisioning can replace them. Durable updates belong in the
builder. There is no per-dashboard JSON `allowUiUpdates` field, and this work does
not change provider-wide policy or Grafana permissions.

The native variable toolbar uses the short labels **Namespace** and **Environment**
and Grafana's native controls; JSON cannot restyle or insert navigation into that
toolbar or the time picker. Header dashboard links are removed. Instead, a
transparent HTML Text panel at the top uses flex alignment, an 8px gap and two
**32px-high** anchors,
with no custom JavaScript, global CSS or HTML-sanitization changes. The current view
has a subtle active treatment. Grafana strips the supplied ARIA group/current
attributes; the rendered navigation remains native named links, not an ARIA tab
widget. This is a separate compact
navigation row below the native toolbar, intended to match its control height,
not a claim that all controls share the first toolbar. Actual native-control
heights and visual alignment remain pending browser measurement.

Both current-view and other-view links use full `/d/<uid>/<slug>` URLs with explicit
`target="_self"`, rather than Grafana dashboard-link SPA navigation. They retain
Namespace and Environment through Grafana's `:queryparam` interpolation and an
HTML-escaped ampersand. They do not retain date ranges: each view loads its own
default. The intended fresh-document navigation avoids retained SPA scroll position;
actual scroll reset, HTML sanitization and toolbar alignment still need live browser
verification by the main agent.

Metrics start immediately below the compact navigation. All informational Text
panels live inside one collapsed bottom row, **Notes and data availability**, with
three bullet-based sections: **Scope and interpretation**, **Data availability:
30 days**, and **History limitations**. Metric rows stay expanded, and useful
technical caveats remain in panel-description tooltips.

## Datasource And Scope

Every query and query variable uses existing datasource **UID `prometheus`**,
type `prometheus`, routed to the **TLS Thanos query frontend on port 9090**.
This directory does not provision/change its URL, TLS, authentication, or
30-second scrape interval. Panels use 30s min intervals and `$__rate_interval`
for operational rates. Coverage timings/control rates use 15m for sparse events.
Operational defaults to `now-1h`, refresh `30s`; Historical defaults to `now-7d`,
refresh `5m`. Both display dates in **America/Los_Angeles**.

Queries follow the actual 10/5/2026 audit in
[`docs/telemetry-and-monitoring.md`](../../docs/telemetry-and-monitoring.md),
especially **Five-Component Panel Examples**, not a synthetic vendor fixture,
acceptance profile or exported CSV. No new inventory counters are invented.

- Every metric selector uses `job="otel-gateway-collector"`.
- Go series use bare `exported_job="workouts-api"`, `workouts-worker`, or
  `workouts-coverage-worker`. They have no native/self namespace/environment
  point aliases. **The selectors do not filter Go panels**; the collapsed scope
  notes explain this distinction. Go origin preservation remains an instrumentation gap.
- Native/self selectors use namespace-prefixed `exported_job`, `service_name`,
  `service_namespace` and `deployment_environment`. The two single-select query
  variables default explicitly to `workouts-explorer` and `xdev`, with no All
  option. Changing namespace changes the exported-job prefix as well.
- Collector replica legends use `service_instance_id`, not the gateway's `pod`.
  Collector CPU/RSS is not nginx, Martin, API, worker or Job runtime.
- Martin uses **`martin_http_requests_duration_seconds`**, native `method`/`status`,
  and preserved `martin_route="/{source_ids}/{z}/{x}/{y}"`. Successful tiles include
  **200 and 204**. The singular fixture histogram name is not used.
- nginx uses `nginx_requests_total` including probes. `active` is total current
  connections; reading/writing/waiting are its breakdown, never stacked together.

## Operational Interpretation

Panel titles use a component-first convention: API, UI, Martin, Coverage, Database,
Pool, Go or Collector, followed by the measurement. Live activity uses **rate**;
historical Stat totals use the dashboard's selected range without repeating that
phrase in each title. Daily totals retain **(daily)** to distinguish their window.
Percentages are named percentages, percentile panels use **p95**, and means are
explicit. Words such as observed, sampled and live do not add useful distinctions
to titles; measurement boundaries and incomplete-history caveats belong in
descriptions or the collapsed Notes section.

Historical's API/UI traffic section uses five panels across one four-grid-unit
row, half the usual panel height. Three panels remain Stats and two remain daily
trend charts; compact layout does not change their queries or data shape.

Five expanded metric groups, **28 query panels**, plus compact navigation and the
collapsed notes row:

1. API and UI traffic: status rates, observed 5xx fraction, GET p95, mean body
   bytes, nginx requests/current states/accepted-handled gap.
2. Private tiles: forwarding outcomes, three independent p95 boundaries,
   successful native 200/204 requests and native 5xx observations.
3. Coverage: four sparse timing distributions, observed terminal transitions,
   bounded control decisions, overlapping retry kinds and stale fences.
4. DB client operations and pools: operations, mean duration, errors, connection
   states/capacity, acquire/churn rates and mean acquisition/empty-pool wait.
5. Go and separate collectors: memory/goroutines/allocations, collector CPU/RSS,
   point handoff/sends/refusals/failures and exporter queue state/capacity.

The combined timing panel does **not** subtract p95s: full API handler, API
forwarding and Martin native backend latency have different boundaries and
populations; none is browser render latency. Histograms aggregate bucket
rates/increases before quantile calculation. The 500ms guide is provisional, not
an accepted SLO. HTTP 5xx fraction is observed attempt reliability, not logical
operation availability. Write errors can have HTTP 200 and remain visible by
tile outcome. No accepted percent target/two-week SLO, burn rate or error budget
is fabricated, and no Pyrra resources are installed.

Missing and NaN results display **N/A**, with unconnected gaps. There is no
`or vector(0)` or NaN/denominator clamp. All mean/percent denominators filter
`> 0` with matching grouping. Genuine measured zeros remain zeros. Coverage's
eight owned instruments, worker DB errors, finite Go memory limits, and candidate
exporter send/enqueue failures were absent in the audit, **not zero**.

Single-value Stats (API requests, API 5xx percentage and UI requests) use fixed
36px values. Multi-value Stats keep 18px values; all Stat labels stay at 14px,
avoiding automatic enlargement when a grouped panel contains only N/A. Time-series panels use their
own compact empty-state/legend typography; font size does not indicate a different
kind of missing data. Both preserve missing values rather than inventing zeros.

Pool duration counters are nanoseconds and divide by `1e9` to show seconds;
successful empty-pool wait divides by successful empty acquires. These are means,
not pool p95. DB operation buckets are too coarse for meaningful subsecond p95,
so only sum/count means are shown; DB operation errors/durations lack pool labels.

Coverage queue age is first-claim age, not oldest backlog; matcher times Evaluate
only; route times postclaim attempts, not durable completions. Controls count
polling/renewal decisions, retries overlap, and stale fences are not outcomes.
Terminal outcomes observe confirmed commits, not an exactly-once ledger;
no-evidence can be valid and supersession/cancellation are not automatic failures.
Point delivery is separate from application health and cannot prove job progress.
Candidate exporter failures are labeled unobserved; failed self-export can hide
its own telemetry, so external host probes/logs/gateway evidence still matter.

## Historical Limits

Four expanded metric groups, **20 query panels**, plus compact navigation and
collapsed notes: observed
API/UI traffic, tile outcomes/distributions, Coverage events, and DB/client/runtime
history. Stat counters use **instant `increase(counter[$__range])`** and reflect
the entire selected window. Window percentiles increase buckets over the entire
window before aggregating: never sum or average daily/per-replica p95 values.

Daily count plots use **`increase(counter[1d])`**, applied before grouping, with
**1d minimum interval and evaluation step**, at most 30 data points. They show
rolling observed event estimates, not rates, raw cumulative heights, exact local
calendar days, unique workouts or SQL ledger facts. Daily pool means divide
duration/event increases; sampled gauge charts are explicitly state histories,
not event counts or exact peaks. A 14d sample window remains subject to gaps.

In the earlier **10/6/2026** live query check, daily plots were empty at aligned
one-day steps because matching retained data did not cover those evaluation
timestamps. Selected-range stats
and shorter diagnostic steps returned data. This is not a parser failure and
must not be replaced with zero. Historical selected-range p95 queries also
returned histogram-monotonicity repair warnings for some populations; inspect
query warnings and treat repaired percentiles cautiously rather than as exact
ledger facts or approved SLO compliance.

Live Thanos retention updated **10/6/2026**: **2d raw / 14d five-minute / 30d hourly**.
These are verified compactor settings, not Prometheus local retention, which
remains **6h**. Longer Thanos history improves dashboard availability but does not
fix Pyrra's two-week recording-rule evaluation against short-history local
Prometheus; see the [SLO preconditions](../../docs/telemetry-and-monitoring.md#preconditions).
The historical time picker offers 30d as a typical month-length view, while the
default remains 7d. The previous 20-day stop/guard is removed. Retention information
is in the collapsed availability notes, not a hard-stop banner or query clamp.
Longer manually selected ranges may contain little or no retained data. Retention
is a ceiling, not a promise of complete blocks, exact calendar-month statistics or
short-window latency resolution. There are no year graphs or completeness claims.

No authorized SQL aggregate datasource/exporter exists for inventory, exact daily
durable results, ingest/file/scheduler/freshness, coverage backlog/slots, OSM/hook
Job outcomes or app database server metrics. These are explained as unavailable,
not populated with proposal metric names or invented zeros. No account variable,
health values, user IDs, private capability IDs, paths, coordinates, raw errors,
SQL text or product measurements appear. Route templates and fixed pool names
are the only route/pool dimensions; variables are scope selectors, not access
control. Synthetic acceptance evidence is never dashboard data.
