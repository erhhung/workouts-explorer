# Coverage Acceptance

This is the canonical specification for the **typical** and **stress**
testing profiles. Each simulates **one user account for a complete 365-day year**.
Multiple-user concurrency is a separate workload, not several users combined
into one account's map selection.

The machine-readable source of generation parameters is
`deploy/acceptance/profiles.json`. Select a profile using
`COVERAGE_ACCEPTANCE_PROFILE=typical|stress`; the default is `typical`.

## Typical Profile

Exactly one workout each day: **365/year**, approximately 90% with GPS routes.

| Activity | Target Share | Annual Count | Distance Per Workout | Duration Per Workout |
| --- | ---: | ---: | ---: | ---: |
| Outdoor Run | 60% | 219 | 8 miles | 2 hours |
| Outdoor Walk | 20% | 73 | 8 miles | 2 hours |
| Outdoor Cycle | 10% | 37 | 20 miles | 2 hours |
| Random other types, without routes | 10% | 36 | None | 30 minutes |

Both 10% quotas produce 36.5 workouts. Largest-remainder rounding assigns the
tied extra workout to Outdoor Cycle: **329 routed / 36 route-less**, approximately
90.14% routed. The distribution is quota-based, not independent random draws.
The 30-minute route-less duration is an adjustable fixture assumption, not a
user-specified requirement.

## Stress Profile

Exactly two workouts each day: **730/year**, exactly 70% with GPS routes.

| Activity | Target Share | Annual Count | Distance Per Workout | Duration Per Workout |
| --- | ---: | ---: | ---: | ---: |
| Outdoor Run | 40% | 292 | 13.1 miles | 2.5 hours |
| Outdoor Walk | 20% | 146 | 10 miles | 3 hours |
| Outdoor Cycle | 10% | 73 | 40 miles | 4 hours |
| Random other types, without routes | 30% | 219 | None | 30 minutes |

There are **511 routed / 219 route-less** workouts. The two daily sessions never
repeat a workout type and never overlap. Pair a route-less workout with a routed
workout on 219 days. The remaining **146 days (40%) necessarily have two routed
workouts of distinct types**. This is the minimum possible with 511 routed
workouts in 365 days; the generator does not silently reduce the requested ratio.

## Editable Generation Parameters

Edit `deploy/acceptance/profiles.json`, then regenerate in a fresh disposable
namespace. Validators check invariants rather than hardcoding the default
distances, durations, seed or quotas.

| JSON Parameter | Default | Meaning And Constraints |
| --- | --- | --- |
| `schema_version` | `1` | Supported manifest format. Do not bump for parameter-only edits; the raw-file hash identifies each definition. |
| `year` | `2026` | Non-leap calendar year, 1900-2100. Populate every date; preserve 365 days. |
| `seed` | `7104` | Signed 32-bit integer controlling deterministic schedule shuffling, route generation and random route-less type selection. |
| `gps_sample_seconds` | `5` | Integer 1-30. Extra observations at turns/reversals prevent corner-cutting. |
| `distance_meters_per_mile` | `1609.344` | Exact unit conversion, fixed rather than tunable. |
| `other_duration_seconds` | `1800` | Route-less duration, 1-25,200 seconds; update both profiles' `other.duration_seconds` to match. |
| `other_types` | Climbing, Paddle Sports, Skating Sports | The default random catalog contains only these three app activities. Editable nonempty catalog; unique lowercase ASCII scheduling keys and nonblank labels. |
| `profiles.typical.workouts_per_day` | `1` | Defines typical; keep one session per date. |
| `profiles.typical.total_workouts` | `365` | Must equal 365 times workouts/day. |
| `profiles.stress.workouts_per_day` | `2` | Defines stress; keep two sessions per date. |
| `profiles.stress.total_workouts` | `730` | Must equal 365 times workouts/day. |
| `activities[].key` | `running`, `walking`, `cycling`, `other` | Keep these ordered internal scheduling buckets, not stored app type keys. |
| `activities[].label` | Outdoor Run, Outdoor Walk, Outdoor Cycle, Other | App activity labels. The synthetic records derive their actual hashed type keys with the ingestion normalizer. |
| `activities[].percentage` | As in the tables | Finite 0-100; all four shares sum to 100. |
| `activities[].count` | As in the tables | Explicit integer quota, 0-365; quotas sum to the profile total. Each differs from the fractional target by at most one workout. |
| `activities[].routed` | True for the first three; false for other | Separates GPS-bearing workouts from route-less workouts. |
| `activities[].distance_miles` | As in the tables | Positive finite mileage for routed activities; zero for other. |
| `activities[].duration_seconds` | As below | Positive integer, up to 25,200 seconds, fitting the fixed daily session slots. |

Default duration values are typical `7200/7200/7200/1800` and stress
`9000/10800/14400/1800` for running/walking/cycling/other. When changing shares,
also change explicit quotas and keep their sum correct. The stress generator
schedules at most one `other` bucket per day and rejects impossible mixtures;
no category can exceed 365 annual occurrences. Each profile needs at least one
routed workout. The three-type matcher smoke additionally needs positive quotas
for all three routed types.

For example, changing stress Outdoor Run to 12 miles over 2.25 hours requires only
changing that activity's `distance_miles` to `12` and `duration_seconds` to `8100`.
Changing the schedule seed or GPS cadence likewise does not require code edits.
Combined distance/duration/cadence must remain within the production input,
sampling, speed and precision limits; the generator rejects violations rather
than shortening the requested route.

### Fixed Synthetic Geometry

These are the current spatial recipe and generation rules, not hidden additional
profile quotas:

- An invented 40x40 grid near longitude/latitude (0,0), spacing 0.001 degrees,
  coordinates 0 through 0.039, 1,600 graph nodes, 3,120 physical edges and 80 named
  roads. Canonical generation 1 belongs to `fixture:dense`.
- `deploy/acceptance/graph.sql` defines the graph; `basemap.cjs` defines its public
  visual context. The Go grid helpers check the same topology. Changing topology
  requires coordinated edits to those files, not merely a profile JSON edit.
- Use canonical PostGIS geography lengths, connected rectangular loops and
  connectors, and a partial final edge to achieve the requested physical mileage.
  Repeated laps are legitimate traveled distance, not duplicated Coverage evidence.
- Use 3m horizontal GPS accuracy, constant simulated speed, regular observations
  plus turning vertices, SQL-microsecond timestamps and bounds covering all points.
- In `America/Los_Angeles`, sessions start at 12:00AM PST or 1:00AM PDT;
  stress's second session starts at 7:00AM PST or 8:00AM PDT, depending on the date.
  These fixed UTC slots (08:00 and 15:00) are defined in `acceptanceSchedule` in
  `worker/coverage_acceptance_profile_test.go`; durations up to seven hours fit
  without overlaps or crossing the local date.
- Outdoor activities have `is_indoor=false`. Route-less Climbing, Paddle Sports
  and Skating Sports retain unknown indoor status as SQL NULL; missing GPS does
  not imply indoor. They have no GPS points, route summary, fabricated distance,
  Coverage child or applied evidence.
- Baseline routed workouts all have applied Coverage. GPS percentages do not
  mean percentages of pending/failed processing. Readiness faults are separate
  controlled tests.

Generation proceeds one route at a time, using bulk point writes. Workouts and
precomputed segment evidence pass through actual ingest leases/write fences,
Coverage claims and `app.persist_coverage_route`. Seeding does not claim to run
the HMM for every workout. The bounded smoke and resource-capped backlog execute
actual matching separately.

App-facing names also standardize the indoor counterparts as **Indoor Run,
Indoor Walk and Indoor Cycle**. Qualified provider aliases such as Outdoor
Cycling and Indoor Biking display and persist as Cycle. In real ingestion,
original provider-derived type keys, fallback fingerprints and content hashes
remain unchanged; canonical display names must not duplicate ID-less workouts
or undo deletion suppression. Existing stored aliases are canonicalized on API
read without a destructive identity migration. The annual profiles themselves
contain only the three outdoor activities and the route-less catalog above.

## Views And Validation

Summary and workout lists count **365/730 total workouts**. Maps and route lists
contain only **329/511 routed workouts** under the default definitions. September
has 30/60 total workouts; routed totals depend on the deterministic schedule.
SQL oracles are scoped to the fixture owner, not every database account.

Validation checks all calendar/type quotas, duplicate daily types, session
overlap, source distance/duration, point sequence/cadence/speed/bounds, route
geography within one meter of the target, digests, route-less isolation and
covered-geometry subsets. Repeated segment visits are dissolved; distinct-workout
counts must not increase with lap count.

## Regenerate And Measure

Prerequisites: Go, Node/npm, PostgreSQL client, kubectl access to `xdev`, and Helm
3 for the fixture's post-renderer. Set `COVERAGE_ACCEPTANCE_HELM` to your Helm 3
binary if the default `helm` is another version. See `deploy/acceptance/README.md`
for deployment-specific requirements. Do not use live database URLs.

**Before editing a definition**, clean up an existing fixture with its current
profile. Its namespace, ConfigMap and protected database marker bind the current
definition hash; changing the file in place cannot relabel a seeded dataset.

```bash
export COVERAGE_ACCEPTANCE_DISPOSABLE=true
export COVERAGE_ACCEPTANCE_PROFILE=typical  # or stress, matching the existing stack
COVERAGE_ACCEPTANCE_CLEANUP_CONFIRMED=yes scripts/coverage-acceptance.sh cleanup
```

Edit the shared JSON, then provision and forward the selected profile:

```bash
export COVERAGE_ACCEPTANCE_DISPOSABLE=true
export COVERAGE_ACCEPTANCE_PROFILE=typical  # select stress for the second run
scripts/coverage-acceptance.sh provision
scripts/coverage-acceptance.sh forward
```

Keep forwarding alive. In a second shell select the same profile:

```bash
export COVERAGE_ACCEPTANCE_DISPOSABLE=true
export COVERAGE_ACCEPTANCE_PROFILE=typical
eval "$(scripts/coverage-acceptance.sh env)"
scripts/coverage-acceptance.sh seed
scripts/coverage-acceptance.sh validate
COVERAGE_ACCEPTANCE_PHASE=baseline scripts/coverage-acceptance.sh reads
scripts/coverage-acceptance.sh matcher-smoke
scripts/coverage-acceptance.sh collect
```

`reads` measures twenty month/year endpoint cases with thirty samples each. It
retains evidence and returns nonzero for missed budgets; still run `collect`.
One deliberate repeat uses `COVERAGE_ACCEPTANCE_PHASE=warm`, retaining baseline
and repeat separately rather than retrying until a favorable result appears.

The current reader uses **one account/client and one in-flight HTTP request**,
not a parallel tile load. Endpoint cases and their thirty samples are sequential,
including full response reads and validation; tile cases repeat
`z=14, x=8192, y=8191`. It is closed-loop with no configured arrival rate.
Typical/stress describe the account's data, not concurrency. Parallel viewport
tile bursts, multiple accounts and saturation need separate measured load
parameters; no multi-user capacity follows from these serial timings. See the
[results report](coverage-acceptance-results.md#request-concurrency) for actual
load and interpretation limits.

### Viewport Tile Loading

Use the separate `viewport` action for a single-account parallel load:

```bash
VIEWPORT_BATCHES=5 scripts/coverage-acceptance.sh viewport
scripts/coverage-acceptance.sh collect
```

The modeled map canvas is **3095 x 1973 CSS pixels**, Mercator, center
`(0.0195, 0.0195)`, zoom 14, zero bearing/pitch, and 512-CSS-pixel vector tiles.
MapLibre 5.24's covering algorithm requires **35 distinct tiles** here,
`x=8189..8195`, `y=8189..8193`; alignment/fractional zoom can change the count.
DPR affects the drawing buffer, not this private-vector tile demand. Multiple
layers sharing one source do not multiply requests; route and Coverage modes
are measured separately, not as two simultaneous source batches.

For each source, test maximum offered concurrency **1, 6, 16 and 35**, five
viewport batches per level: **1400 measured requests per profile**, plus 70
unmeasured probes. `VIEWPORT_BATCHES` accepts 1-10; five yields only a coarse
viewport p95, not stable SLO evidence. Validate all MVTs, including legitimate
empty tiles, and separately report feature-bearing/empty latency. Only nine of
the 35 tiles contain fixture features; do not present this as a viewport-wide
dense city.

Record offered and HTTP/1 connection peaks, errors, body-complete request
latency and the entire batch critical path. Client peaks do not prove API/Martin/
DB concurrency. Keep 500ms request and 3000ms viewport-fetch budgets explicitly
provisional/proposed; fetch completion is not the product's usable-render target.

To observe the actual browser's source scheduling and HTTP protocol:

```bash
COVERAGE_ACCEPTANCE_UI_URL=https://acceptance.workouts.xdev.fourteeners.local \
  npm --prefix ui run test:coverage-viewport
```

This sizes the real `.map-canvas`, settles the fixed camera, drains prior loads,
replaces the measured source/cache with a fresh capability, and records actual
URLs, peaks, HTTP/CDP protocol, source completion and interference. Successful
capture and structural comparability are separate; neither is browser render
acceptance. Do not infer protocol from HTTPS or compare timings as a controlled
HTTP/1-versus-HTTP/2 experiment across different runs/namespaces.

The matcher smoke exercises one full-length Outdoor Run, Outdoor Walk and Outdoor Cycle route
through host `CoverageRunner.RunOnce` with a read-only OSM connection. Host
correctness timing is not resource-capped matching throughput. To measure a
backlog on the deployed Coverage worker, use:

```bash
COVERAGE_ACCEPTANCE_BACKLOG=48 scripts/coverage-acceptance.sh queue
COVERAGE_ACCEPTANCE_PHASE=backlog48 scripts/coverage-acceptance.sh reads
scripts/coverage-acceptance.sh collect
```

Alternatively use 96 and `backlog96`. Waves revise existing eligible workouts
across the year without changing their type, date, mileage or duration. Require
enough eligible routes and inspect actual overlap windows; do not invent extra
monthly workouts or shorten routes to make the backlog finish faster.

Browser measurements use real MapLibre/MVT, the profile-specific SQL oracle,
light/dark desktop/tablet and color-vision simulations. The 10/11/2026 scope
decision carries profile benchmarking to Milestone 11 and tablet-focused UI/UX
to Milestone 10. Adapt the existing visual harness to the approved iPad-class
targets before tablet acceptance; legacy narrow-screen captures do not imply
iPhone support or authorize phone-focused development:

```bash
COVERAGE_ACCEPTANCE_UI_URL=https://acceptance.workouts.xdev.fourteeners.local \
  npm --prefix ui run test:coverage-visual
```

Capture success is not a perceptual accessibility pass. Reviews must name actual
checksum-verified screenshots. Cleanup before switching profiles, editing
parameters or finishing the run, using the cleanup command above.

## Reports And Bindings

Only these profiles have result paths:

| Profile | Backend Report | Browser Report, When Executed |
| --- | --- | --- |
| Typical | `deploy/acceptance/results/typical-results.json` | `docs/coverage-visual-typical-results.json` |
| Stress | `deploy/acceptance/results/stress-results.json` | `docs/coverage-visual-stress-results.json` |

The corrected source-loader observations are stored separately in
`deploy/acceptance/results/typical-viewport-browser-results.json` and
`deploy/acceptance/results/stress-viewport-browser-results.json`, with their own
namespace/definition bindings. They are not the unexecuted full visual matrix.

Records include profile name, format version, raw manifest SHA-256 and namespace
UID. Captures/exports use `/tmp/opencode/coverage-{profile}-*/{namespace_uid}/`;
intermediate measurements use profile-specific paths. A fresh namespace must
not silently load another namespace's archive or samples. Reports record the
parameters actually used, not whatever is later edited into the manifest.

The [human-readable results report](coverage-acceptance-results.md) contains the
complete measured tables, provenance, validation and execution limitations.
Typical baseline reads pass; stress retains a year Coverage tile budget miss.
The retained runs contain no warm serial phase or latency retry. The tile budget
is provisional, not an accepted tile-specific product target. Earlier host matcher
correctness is separate from the current profile receipts, whose matcher-smoke
fields are null. Browser acceptance and resource-capped backlogs remain unmeasured,
with profile benchmarking carried to Milestone 11 by the closeout scope decision.

## Worker Correctness Suite

Independently of profile size, `worker/coverage_runner_integration_test.go`
exercises the real worker against separate application and read-only OSM
databases. Ten cases cover claim/snapshot/copies, heartbeat, atomic replacement,
no-evidence removal, generation fencing, heartbeat/persistence lease loss,
newer target/revision, rollback and cancellation. The guarded wrapper passes
three race repetitions and runs in the dedicated CI job:

```bash
make coverage-worker-test
```

Administrator OSM status/refresh API work belongs to Milestone 9, not these
Milestone 7 acceptance profiles.
