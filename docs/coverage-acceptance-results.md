# Coverage Acceptance Results

## 10/5/2026

Stable single-user serial/Go viewport evidence comes from
[typical-results.json](../deploy/acceptance/results/typical-results.json) and
[stress-results.json](../deploy/acceptance/results/stress-results.json).
Authoritative corrected browser evidence comes from
[typical-viewport-browser-results.json](../deploy/acceptance/results/typical-viewport-browser-results.json)
and [stress-viewport-browser-results.json](../deploy/acceptance/results/stress-viewport-browser-results.json).
The [profile guide](coverage-acceptance.md) defines the generation contract.
Dates and receipt times use the IANA zone `America/Los_Angeles`; date-only
headings omit a zone suffix. Go tables round to whole milliseconds; browser
tables retain one decimal. Raw artifacts keep their original timestamps,
precision and failures. Corrected browser-only runs do not replace Go results.

## Conclusions

| Measurement | Typical | Stress |
| --- | --- | --- |
| Seed and retained fixture validation | PASS | PASS |
| Serial baseline, 20 endpoint cases | 20/20 budgets PASS | 19/20 PASS; year Coverage tile **708ms FAIL** |
| Go viewport functional checks | 1400/1400 HTTP 200 | 1400/1400 HTTP 200 |
| Go request / whole-viewport budgets | 4/8 request budgets fail; 8/8 fetch budgets pass | 6/8 request and 8/8 fetch budgets fail |
| Corrected browser capture / comparison | Both true; 70/70 HTTP 200 over h2 | Both true; 70/70 HTTP 200 over h2 |
| Current matcher smoke / capped backlog | Not run | Not run |
| Fixture namespace and forward cleanup | PASS | PASS |

Serial and viewport budgets are separate assessments. Typical's root serial
status is `PASS`, not an overall viewport acceptance pass. Stress's root serial
failure count is **1**; `viewport_targets` records **14 failed budget checks**
across eight cases, versus typical's **4**. Both have complete serial endpoints
and viewport evidence. No Go latency retries or warm serial phase were run,
and no additional fixture users or resource increases were introduced. Browser
recapture corrected the observation harness; it is not a retry-until-green
performance claim. All browser statuses remain `RECORDED_NO_ACCEPTANCE_THRESHOLD`.

The **500ms tile request budget is provisional**, not a written product SLO.
The **3000ms whole-viewport fetch budget is proposed**, not a product render
target. Both Summary targets pass; initial-map render remains `NOT_MEASURED`.

## Provenance And Runtime

These are immutable captured-run receipts. The 10/6/2026 development-only
consolidation subsequently moved Coverage job metadata grants into migration 015
and returned current application schema/Goose support to 20/minimum 18. Future
fixture image pins use the compatible revision-97 API/worker binaries; this does
not change the recorded schema 21, images, namespaces or timings below.

Go runs and corrected browser fixtures use revision-**94** pinned application images, evidence schema
**3**, profile version **1**, year **2026**, seed **7104**, **5s GPS cadence** and
**1609.344 meters/mile**. Actual `generation_parameters` snapshots share manifest
SHA-256 `1081959ad31aa04bdc9b36357e569383819a12baaf0e51db0273e3df10bdce7a`.
Exact Go image references are in `images`; browser deployment specifications
are in `fixture_verification.deployment_specs`.

Stable Go archive receipt times below are on **10/5/2026** in Los Angeles;
the corrected browser runs have separate namespace bindings listed later:

| Profile | Namespace UID | Created | Collector Observation | Cleanup Verified |
| --- | --- | --- | --- | --- |
| Typical | `4c88715b-bd08-4c2e-a4fd-6a405f4e4c46` | 11:48AM PDT | 12:00PM PDT | 12:08PM PDT |
| Stress | `54cdf971-fe99-429a-bb0f-4563fa8f391d` | 12:08PM PDT | 12:23PM PDT | 12:29PM PDT |

PostgreSQL **18.6 (Debian 18.6-1.pgdg13+2)**, PostGIS **3.6.4**, application
schema **21**, OSM schema **9**, and **3120 edges / 80 ways** are recorded.
Each benchmark database has one fixture account and one database account.
API/Martin application requests/limits remain **25m CPU / 64Mi** and
**250m / 256Mi**; scratch database limits are **1 CPU / 1Gi**. Martin's pool is
**4**, its cache is disabled, and API upstream timeout is **5s**. These are
configured limits, not measured resource peaks or proven saturation.

Both Go archive root `matcher_smoke` values are **null**. Previous-namespace matcher and
warm artifacts were rejected by the collector. Earlier host matcher and naming
correctness/race checks were verified separately; their six-route receipts and
point counts are **not measurements of these new namespaces**. No current HMM
throughput, matcher backlog or repeated smoke result is claimed.

## Fixture And Safety

| Activity | Typical Count | Stress Count | Typical Miles / Seconds Each | Stress Miles / Seconds Each |
| --- | ---: | ---: | ---: | ---: |
| Outdoor Run | 219 | 292 | 8 / 7200 | 13.1 / 9000 |
| Outdoor Walk | 73 | 146 | 8 / 7200 | 10 / 10800 |
| Outdoor Cycle | 37 | 73 | 20 / 7200 | 40 / 14400 |
| Climbing | 14 | 74 | None / 1800 | None / 1800 |
| Paddle Sports | 15 | 74 | None / 1800 | None / 1800 |
| Skating Sports | 7 | 71 | None / 1800 | None / 1800 |

Outdoor records have `is_indoor=false`; all three route-less types retain
**unknown indoor status (SQL NULL)**, null distance and no fabricated GPS,
route summary, Coverage child or evidence. Share parameters remain typical
**60/20/10/10%**, stress **40/20/10/30%**, for the three routed activities / other.

Stable Go fixture facts (browser-only reseed timings are separate receipts):

| Observed Fact | Typical | Stress |
| --- | ---: | ---: |
| Total / routed / route-less workouts | 365 / 329 / 36 | 730 / 511 / 219 |
| Calendar dates / sessions each date | 365 / 1 | 365 / 2 |
| Annual traveled miles / duration seconds | 3076 / 2433600 | 8205.2 / 5650200 |
| Seed and retained GPS points | 480443 | 1068624 |
| Seed and retained segment matches | 19938 | 51257 |
| Retained path copies | 2724 | 2957 |
| `map_data_ready` / processing `current` | 329 / 329 | 511 / 511 |
| Seed-only / driver seconds (rounded) | 92.017 / 93.074 | 346.140 / 348.581 |
| September / year total workouts | 30 / 365 | 60 / 730 |
| September / year routed workouts | 26 / 329 | 42 / 511 |
| September / year logical paths | 62 / 80 | 76 / 80 |

All 365 dates are populated. Full GPS geometry is unchanged after reads and
browser observation. Seed writes exercise actual ingest fences, Coverage claims
and persistence with **precomputed matching ground truth, not HMM for every
workout**. Driver wall time includes build/startup. Current routed Coverage and
genuine no-route source facts are independently validated.

Fresh validators and independent SQL after viewport/browser reads pass: zero
duplicate-type days, overlapping pairs, geography mismatches over one meter,
active/failed jobs and matcher slots. Full point sequence/cadence/speed/bounds,
source distance/duration and geometry invariants are validated. Viewport MVT
decoding and per-tile SQL cardinality checks pass for feature-bearing and empty
tiles, not merely nonempty response bodies.

The four recorded migration/API/worker/Coverage roles are neither superuser nor
`BYPASSRLS`; OSM reader mutation privileges remain false. Diagnostics preference
is false. No PVC, live writes or PBF import was used. Matching children, other
jobs and matcher slots are zero at viewport boundaries, with unchanged data
generation: this is a read-concurrency test, not a matcher-overlap benchmark.

## Read Latency

Each profile has one serial baseline: **20 cases x 30 sequential samples = 600
successful logical reads**, **540 HTTP 200 + 60 HTTP 201**. Both selection cases
return 201; the other 18 return 200. All content checks pass. Across both profiles
there are **1200** serial reads; old warm-window results are not included.

On **10/5/2026**, typical baseline ran **11:53AM-11:54AM PDT** and stress
**12:16PM-12:17PM PDT**. Rounded p95 milliseconds:

| Endpoint | Typical Baseline | Stress Baseline |
| --- | ---: | ---: |
| `month_summary` | 52 | 33 |
| `month_workouts_page1` | 28 | 34 |
| `month_workouts_page2` | 30 | 22 |
| `month_selection` | 34 | 59 |
| `month_paths_page1` | 117 | 253 |
| `month_paths_page2` | 116 | 262 |
| `month_path_detail` | 76 | 125 |
| `month_focus` | 31 | 27 |
| `month_route_tile` | 37 | 32 |
| `month_coverage_tile` | 42 | 75 |
| `year_summary` | 28 | 35 |
| `year_workouts_page1` | 37 | 54 |
| `year_workouts_page2` | 22 | 40 |
| `year_selection` | 94 | 133 |
| `year_paths_page1` | 186 | 396 |
| `year_paths_page2` | 170 | 367 |
| `year_path_detail` | 102 | 251 |
| `year_focus` | 40 | 64 |
| `year_route_tile` | 122 | 222 |
| `year_coverage_tile` | 232 | **708 FAIL** |

Stress's exact failing p95 is **708.005ms**, with 30/30 HTTP 200 responses.
First calls are cold-ish, not flushed-cache tests; localhost forwarding and
uncontrolled shared-host load are included. HTTP success does not waive a budget
failure, and serial tile latency is not browser fetch completion or rendering.

## Request Concurrency

Serial reads have one account/client and at most **one request in flight**.
Viewport runs retain the same single owner but vary a separate request-load axis:
**C=1/6/16/35** offered requests. Route and Coverage sources run separately,
matching the UI's one-active-source model; no multi-user capacity test was run.

The Go HTTP/1.1 model uses the actual **3095 x 1973 CSS-pixel** canvas, **512px
tiles**, Mercator, zoom **14**, center **(0.0195, 0.0195)**, zero bearing/pitch,
and MapLibre distance-to-tile-center ordering. Its **35 tiles** cover
`x=8189..8195`, `y=8189..8193`. DPR does not multiply the CSS viewport area.

Each profile measures **8 cases x 5 batches x 35 tiles = 1400 requests**, plus
**70 separate preflight requests** (35 per source). All are HTTP 200, with zero
Go request errors/timeouts. That is **2070 serial + measured + preflight requests
per profile**, excluding browser and capability setup traffic. Each case has
**175 measured requests**: **45 feature-bearing / 130 empty**, or **9 / 26 tiles
per batch**. The 40x40 graph does not fill all 35 viewport tiles densely.

Achieved offered-active and HTTP/1.1 connection peaks equal configured C in all
cases. Offered-active includes transport queue; connection peak spans acquisition
through body completion. Neither proves actual API, Martin or DB concurrency.
Sequential preflight warms tiles; database/OS caches are not flushed. No
application retry or selection refresh was used in the Go viewport run.

## Viewport Burst

All columns below are rounded p95 milliseconds. **Request** is dispatch to body
completion; it excludes waiting for a client concurrency slot. **Viewport** is
batch start through the final body completion, including client queueing: the
whole batch critical path, **not a sum of request p95s**. Positive/empty subgroup
p95s include validated successes. Budgets use overall request p95 and whole
viewport p95; subgroup p95s expose empty-tile dilution but do not override status.

Typical measured **11:54AM-11:55AM PDT on 10/5/2026**:

| Source | C | Peak Offered / Connections | Request | Positive | Empty | Viewport | Request / Fetch Budget |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Route | 1 | 1/1 | 74 | 92 | 55 | 1741 | PASS/PASS |
| Route | 6 | 6/6 | 401 | 503 | 365 | 1761 | PASS/PASS |
| Route | 16 | 16/16 | 951 | 978 | 911 | 1947 | FAIL/PASS |
| Route | 35 | 35/35 | 1846 | 1735 | 1882 | 2105 | FAIL/PASS |
| Coverage | 1 | 1/1 | 112 | 170 | 50 | 1704 | PASS/PASS |
| Coverage | 6 | 6/6 | 487 | 722 | 217 | 1268 | PASS/PASS |
| Coverage | 16 | 16/16 | 707 | 905 | 690 | 1181 | FAIL/PASS |
| Coverage | 35 | 35/35 | 1163 | 1307 | 1099 | 1336 | FAIL/PASS |

Stress measured **12:18PM-12:20PM PDT on 10/5/2026**:

| Source | C | Peak Offered / Connections | Request | Positive | Empty | Viewport | Request / Fetch Budget |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Route | 1 | 1/1 | 185 | 217 | 119 | 3833 | PASS/FAIL |
| Route | 6 | 6/6 | 990 | 1244 | 766 | 3738 | FAIL/FAIL |
| Route | 16 | 16/16 | 1939 | 2042 | 1798 | 3743 | FAIL/FAIL |
| Route | 35 | 35/35 | 3557 | 2751 | 3605 | 3949 | FAIL/FAIL |
| Coverage | 1 | 1/1 | 303 | 617 | 82 | 3504 | PASS/FAIL |
| Coverage | 6 | 6/6 | 1358 | 3056 | 497 | 3364 | FAIL/FAIL |
| Coverage | 16 | 16/16 | 2198 | 2691 | 1951 | 3268 | FAIL/FAIL |
| Coverage | 35 | 35/35 | 3272 | 3129 | 3320 | 3452 | FAIL/FAIL |

With **only five batches**, the retained viewport p95 equals the maximum; these
are coarse pilot observations, not a stable SLO estimate. Stress Coverage C=1
passes overall request p95 at 303ms even though positive-tile p95 is 617ms.
More parallel requests generally raise individual latency here; they do not
establish proportionate capacity gains. No failure is discarded or waived.

## Corrected Browser Observation

The two standalone browser reports linked at the top are authoritative for
browser results. Each binds a fresh single-account fixture and records one
remote Chromium capture per source, with **`capture_success: true` and
`comparison_valid: true`** at root and case level. All receipt times below are
on **10/5/2026** in Los Angeles:

| Profile | Browser Namespace UID | Capture Window | Cleanup Verified |
| --- | --- | --- | --- |
| Typical | `26960c93-ce11-4dc3-8bf9-20f171c76b48` | 1:55:05PM-1:55:33PM PDT | 1:57:40PM PDT |
| Stress | `a5810e7a-714c-412e-a156-56759058f43d` | 2:05:13PM-2:05:52PM PDT | 2:08:00PM PDT |

All four sources match **exactly the planned 35 z14 tiles**, with **35 peak
in-flight requests**, **3095 x 1973 CSS pixels**, **512px tiles**, DPR **1**, and
a stable camera/frame. No missing, extra or fallback tiles, frame changes,
old-capability/source interference, pending requests or capture errors are
recorded. All **140 tile requests return HTTP 200 over h2**, confirmed by CDP;
browser-facing HTTP/2 does not establish protocol on every upstream hop.

The corrected per-source observations, in milliseconds:

| Profile / Source | Requests / Peak | HTTP Completion | Source Loaded | Idle | Longest Request |
| --- | ---: | ---: | ---: | ---: | ---: |
| Typical Route | 35 / 35 | 2958.9 | 3375.8 | 3379.0 | 2642.0 |
| Typical Coverage | 35 / 35 | 2015.5 | 2494.9 | 2511.8 | 1619.6 |
| Stress Route | 35 / 35 | 4977.5 | 5448.5 | 5450.9 | 4635.5 |
| Stress Coverage | 35 / 35 | 4434.8 | 4535.1 | 4539.6 | 4092.2 |

HTTP completion, source-loaded events and idle are distinct observations, not
render usability. Longest request is the maximum among that capture's 35
requests, **not p99**. There is **one run per source/profile**, not a distribution
of viewport trials or a viewport percentile. Each case retains status
**`RECORDED_NO_ACCEPTANCE_THRESHOLD`**: successful capture/comparison is not
performance acceptance, and no numeric browser budget was applied. Initial-map
render remains `NOT_MEASURED`; the proposed Go 3s fetch budget is not a browser
or written initial-map product target.

Corrected browser and Go runs share profile parameters and planned tile
coordinates, but differ in session, namespace, time, source capability and
cache state. Go uses HTTP/1.1; the browser uses h2. This is **not a controlled
protocol comparison** and establishes no HTTP/2 speed benefit. Backend/OS caches
were not flushed; shared remote Chromium scheduling/software rendering also
limits interpretation. No serial reads or Go viewport cases were rerun in the
browser-only fixtures; their export receipts confirm the original evidence
was preserved.

The older embedded `viewport_browser` captures in the Go archives are
**superseded, noncomparable diagnostics only**. Their camera/source interference
and experimental errors motivated corrected recapture. They remain untouched
in raw JSON, but are not the current browser table, comparable capacity data
or evidence of timeout failures in the corrected all-200 captures.

## Investigation And Remediation

The serial/Go chain is client -> port-forward -> API auth/capability -> Martin
-> app-db/PostGIS -> response. Browser additionally involves ingress/UI proxy,
source scheduling, decode and rendering. **Component ranking is a source-based
hypothesis, not measured per-hop attribution**; resource peaks remain null.

| Component | Evidence And Hypothesis | Next Measurement |
| --- | --- | --- |
| app-db | Priority query suspect: annual joins/counts, geometry aggregation, clipping and MVT encoding; 1 CPU limit. No measured proof it dominates total delay. | Correlated nested query plans, CPU, buffers/spills, lock waits and query duration. |
| Martin | Pool of 4 versus up to 35 offered requests; queueing and 250m CPU limit may contribute. HTTP connection count is not DB pool occupancy. | Pool wait/occupancy, SQL duration, transport/compression and throttling. |
| API | Capability checks and proxying; 5s upstream timeout is configured, but corrected browser tile requests all return 200. No current timeout failure or dominant API cost is established. | Auth/capability time, proxy wait, cancellation propagation and correlated upstream spans. |
| UI/browser/ingress | Corrected source/frame/tile comparisons pass; loading/idle still does not prove rendered usability. Capture and protocol differences prevent direct timing attribution. | Retain source isolation and exact tile/protocol capture; measure fetch/decode/render separately. |
| OSM | Read tiles use copied app-db evidence, not fresh OSM matching. No matcher backlog was running. | Shared scratch-instance interference if observed; OSM upgrades are not demonstrated tile fixes. |

Recommended experiments, not changes made by this report:

1. Preserve the corrected source/frame isolation and CDP capture for repeated
   identical-tile browser trials; measure usability separately from load/idle.
2. Correlate API/Martin/DB spans and pool waits with `EXPLAIN (ANALYZE, BUFFERS)`
   or nested plans. Do not subtract endpoint p95s to invent component percentages.
3. Optimize demonstrated hotspots with owner/date-scoped indexes, preaggregation
   or caching, preserving exact counts, generation invalidation and isolation.
4. Vary one resource/pool change at a time against this same profile/tile set.
   More CPU, memory/work_mem, disk or network helps only its measured bottleneck;
   increasing Martin's pool alone may increase contention on the 1-CPU database.
5. Extend batch counts, capture utilization/throughput and actual server
   concurrency, then add separate accounts as a new load axis. Current pilot
   data cannot predict many-user capacity or justify a linear hardware upgrade.

## Cleanup And Limits

All four Go/browser `operational_state` receipts record cleanup **PASS**, namespace
deletion, stopped supervisor, absent PID/lock and closed ports
**15432/18081/18080/18083/18089/19123**. Final jobs, failures and matcher slots
are zero. On **10/5/2026**, Go cleanup was verified **12:08PM PDT typical /
12:29PM PDT stress**; corrected browser cleanup **1:57:40PM PDT typical /
2:08:00PM PDT stress**. No fixture is claimed retained for inspection.

Go archive roots `matcher`, `matcher_smoke`, `general_worker` and `diagnostics_429`
are null. No capped 48/96-job backlog, additional users, full browser/accessibility
matrix or product initial-render acceptance was run. CPU/memory/throttling peaks
are unavailable because host Prometheus was not forwarded; configured limits
and ready/no-OOM snapshots do not prove spare capacity. Superseded diagnostic
browser errors remain in the original archives; corrected standalone captures
are error-free without claiming render acceptance. Only this human report
changes; all four JSON archives and raw timestamps remain untouched. No
screenshots, CI pipelines or workloads were run for this documentation refresh.

## Milestone 7 Acceptance Decisions (10/11/2026)

These are user-approved scope decisions, not new benchmark results. The captured
measurements, failures, null fields and raw JSON artifacts above remain unchanged.
Milestone 7 is complete under this revised scope; remaining validation belongs
to the future milestones listed below, not an inferred benchmark or production pass.

- **Resource-capped backlog: accepted for Milestone 7.** Carry the unexecuted
  representative 48/96-route backlog and CPU/memory/throttling/peak-resource
  measurements to Milestone 11. Retain matcher concurrency at one. This closes
  the milestone item without asserting measured throughput or spare capacity.
- **Mixed-workload responsiveness: accepted for Milestone 7.** Carry the unmeasured
  overlapping Coverage/general-worker/API and competing-diagnostic workload
  validation to Milestone 11. Worker separation and admission controls are
  implemented; sustained interactive responsiveness under that load is not proved.
- **Profile benchmarking and visual matrix: accepted for Milestone 7.** Carry
  typical/stress benchmarks and profile-specific theme/color-vision review to
  Milestone 11 (the former Milestone 10 verification/release scope). Preserve
  the recorded stress year tile/viewport budget failures and missing initial-map
  render measurements. The newly inserted Milestone 10 owns tablet-focused UI/UX
  and device-specific visual
  review on iPad-sized devices and larger; iPhone-sized development is excluded.
- **Real-device and native-zoom checks: accepted for Milestone 7.** Carry iPad
  device/rotation/keyboard/browser-zoom validation to Milestone 10 and desktop
  native-zoom regression checks to Milestone 11. No unexecuted device matrix is
  relabeled as passed.
- **Production scaling and security hardening: deferred to Milestone 12.** The
  final milestone owns production deployment, scale/resource/retention decisions,
  dedicated OSM reader credentials and environment-specific security hardening.
  The development evidence above does not establish production readiness.
