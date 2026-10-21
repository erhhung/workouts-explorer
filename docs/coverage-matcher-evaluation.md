# Experimental Coverage Matcher Evaluation

## Status

Acceptance evidence for ADR 0009, accepted on 9/11/2026 with matcher rules
`coverage-experimental-v1`, adaptive sampling, and path policy
`coverage-path-policy-experimental-v82`. Production Coverage persistence remains
the next delivery slice. The owner-only diagnostic API and UI persist bounded
matcher evidence and labels and remain available for route-level review.

## Scope

`internal/coverage` is a deterministic, side-effect-free evaluation package. It
uses local-meter Cartesian coordinates and caller-supplied segment candidates.
The independent `internal/osm` matcher adapter supplies bounded candidate,
topology, generation, and clipped-geometry reads from canonical OSM storage.
`MatchOSM` bridges geographic observations and
physical OSM candidates into the local-meter directed evaluation graph. Fixtures
contain no real or private coordinates.

The adapter opens a PostgreSQL `REPEATABLE READ, READ ONLY` snapshot so candidate
geometry, incident topology, and generation provenance cannot change during one
matching run. Candidate lookup accepts at most 256 observations and 256 results
per observation, caps each caller-supplied radius at 250 meters, uses geographic
`ST_DWithin`, and detects cardinality overflow with a max+1 query. Incident reads
accept at most 256 graph nodes and 4,096 returned edges. Invalid numeric input,
array shape, and limits are rejected before SQL. Overflow errors identify only
the operation, limit, and observation ordinal, never coordinates.

Candidate SQL reads canonical tables directly and reproduces active source-way
version and segment-identity overlap precedence rather than losing provenance
through `osm_active`. Results retain region, generation, source-way, derivation,
and segment identities plus projected position and local tangent. Incident edges
use indexed start/end graph-node columns. Transactions are read-only and the
adapter performs no geometry or observation persistence.

## Owner diagnostic API

The API is disabled by default. Set `COVERAGE_DIAGNOSTICS_ENABLED=true` together
with `API_OSM_DATABASE_URL`; optional `COVERAGE_DIAGNOSTICS_TIMEOUT` defaults to
`120s` and is bounded to `5s` through `120s`, while
`COVERAGE_DIAGNOSTICS_CONCURRENCY` defaults to `1` and is bounded to `1` through
`4`. The optional OSM pool is opened only when enabled and its maximum connection
count equals diagnostic concurrency. API readiness deliberately remains based on
the application database only.

`POST /api/workouts/{id}/coverage-diagnostic-runs` accepts only a required empty
JSON object. It uses a repeatable-read account snapshot, adaptive sampling,
overlapping 128-point windows with bounded 64/32-point recovery for typed graph
node/edge exhaustion, one repeatable-read OSM snapshot, exact clipped
source geometry, and a final route revision/digest recheck. It stores no route
points, candidate clouds, OSM tags or names, free text, or private request input.
`PATCH /api/coverage-diagnostic-runs/{id}/labels` stores only fixed overall and
evidence label enums. Both endpoints are owner-only and cookie mutations require
CSRF. Disabled endpoints return 404 before authentication or either database.

### Shared Matching Limits And Window Recovery

The normative requirements are the
[functional matching contract](functional-spec.md#matching-bounds-and-diagnostic-parity)
and [shared architectural contract](architecture.md#shared-matching-contract).

Diagnostics and production share `routepipeline.OriginalPointLimit` (50,000 raw
route points) and `routepipeline.DefaultLimits()` (12,000 sampled observations),
with unchanged limits of 10,000 evidence portions, 4,096 segments, 256 generations and 64 unavailable
regions. Migration 030 widens the diagnostic count checks to match; schema 30
retains minimum runtime 26. The raw-point guard runs before matcher admission.
Equal input, OSM snapshot, movement mode, sampling, matching rules and road
geometry settings produce the same matching counts and evidence in both
adapters. This is matching-limit parity, not equal execution budgets: the
synchronous HTTP diagnostic still has its existing maximum 120-second timeout,
and a timeout is inconclusive, not a production semantic matching failure.

Failed window attempts retain the same deadline/snapshot and contribute no
observations, traversals or evidence. Accepted windows advance with one-point
overlap, and supplemental boundary repairs use continuous supporting traversals.
Exhaustion at the minimum window remains a failure. Nodes are not arbitrarily
discarded to fit a bound. Real-route core validation does not attest the HTTP
envelope, APP persistence or production two-lane capacity; long-route UI
attestation would need an asynchronous worker-based execution path.

Diagnostic minimum-traversal and road-geometry configuration remain supported
what-if controls. `minimumTraversalMeters` is reported in each run; road-geometry
overrides are service configuration, not attested by the response metadata.
Do not interpret a custom-parameter diagnostic as production parity merely
because its version labels and limits match. Compare its effective settings with
the production job's minimum traversal and `coverage.DefaultRoadGeometryRules()`.

For xdev enablement, set `api.coverageDiagnostics.enabled=true`. The current Helm
default selects `api.coverageDiagnostics.databaseSecret.name` as
`workouts-explorer-database` and `.key` as `osmReaderDatabaseUrl`, independently
of `api.databaseSecret` and both worker reader references. An existing shared
Secret can retain application, reader and writer URL keys. A dedicated OSM
matcher reader role can instead use a separate Secret through this illustrative,
non-credential fragment:

```yaml
api:
  coverageDiagnostics:
    enabled: true
    databaseSecret:
      name: example-osm-reader
      key: osmReaderDatabaseUrl
    databaseTLSSecret: workouts-explorer-database-tls
```

The selected URL must authenticate as the intended reader role; a Secret name or
key does not assign database permissions. Keep the existing
`api.coverageDiagnostics.databaseTLSSecret` property, selecting the TLS material
appropriate for that reader. The same independent full-reference contract applies
to `worker.osmDatabaseSecret` and `coverageWorker.osmDatabaseSecret`, both with
the same default name/key and their existing component `databaseTLSSecret`
properties. `osm.useReadReplicasOnly` remains a default-off standby-role policy,
not a Secret selector or endpoint override. For operator-only conversion of old
local key-only overrides, see
[the migration guidance](osm-read-replicas.md#existing-values-and-minimal-example).

## Manual real-route evaluator

`worker/cmd/coverage-evaluate` is an operator-run, read-only evidence tool. It is
not invoked by the worker and does not enable Coverage, persist matcher output,
or expose an API or UI. Run `/app/coverage-evaluate` from the worker image with:

- `MIGRATION_DATABASE_URL`: application database connection able to read the
  account and workout tables;
- `OSM_DATABASE_URL`: canonical OSM database connection;
- `COVERAGE_EVALUATION_ACCOUNT`: one exact canonical username or canonical
  email; resolution must identify exactly one user account;
- `COVERAGE_EVALUATION_LIMIT`: optional routed-workout limit, default `20`,
  bounded from `1` through `500`; and
- `COVERAGE_MIN_TRAVERSAL_METERS`: optional traversal threshold in meters,
  default `5`, bounded from `0.1` through `100`.

Non-deleted workouts with route summaries are selected in stable workout
chronology. Route points are read in sequence order one workout at a time. The
workout type key and provider label select experimental `foot`, `bicycle`, or
conservative `shared_public` policy, but neither source value is retained in the
report. The complete route is adaptively sampled before matching. Sampled points
are then processed in windows of at most 128 observations; adjacent windows
share one observation so the boundary transition is retained without counting
the observation twice.

Each non-empty workout has one bounded two-minute context and one read-only,
repeatable-read OSM matcher snapshot shared by all of its windows. The snapshot
is always closed. Candidate, graph-node, graph-edge, and expansion-layer bounds
remain those of the experimental OSM bridge; a bound or database failure fails
that workout rather than silently truncating it.

Standard output contains exactly one final JSON report. It contains only fixed
rules/config values, workout counts by safe outcome/mode, original and sampled
observation counts, matched/ambiguous/unmatched/rejected counts, topology totals
and maxima, traversal and portion counts, the count of unique public physical
segments, allowlisted error-category counts, and aggregate durations. Standard
error diagnostics contain only a safe category. The tool never emits account,
principal, workout, route, or segment identifiers; coordinates; timestamps;
source labels; names; or arbitrary database error text.

The bridge queries candidates in batches, converts geographic observations and
projections to a route-local tangent plane, constructs forward/reverse directed
states with physical segment lengths and local tangents, and expands incident
topology only within the experimental network-distance bound. Expansion has hard
node, edge, layer, candidate, and database-query limits and fails rather than
silently truncating the graph.

The package defines observations, directed segments, candidates, a graph,
decoded observations/traversals/results, evaluation labels/metrics, and an
explicit rules version. `coverage-experimental-v1` centralizes all constants in
`ExperimentalRules`. ADR 0009 accepts these values as the production baseline;
future changes require a new version and diagnostic regression review.

## Semantics

- Candidate acceptance is bounded by both the global distance cap and a clamped
  accuracy-derived radius. Unknown accuracy receives conservative finite
  uncertainty; invalid, duplicate, unknown-segment, and out-of-range candidates
  are removed before decoding.
- Viterbi decoding scores point-to-projection distance normalized by effective
  horizontal accuracy. Course contributes angular evidence only when supplied,
  reliable, and supported by local movement.
- Transitions use directed connected-network shortest distance and compare it
  with observed displacement and elapsed time. Searches and detours are bounded;
  logical path names and locality metadata never create graph connectivity.
- Candidate states, graph edges, equal-cost shortest paths, and equal-cost
  Viterbi paths use stable lexical tie-breakers. Candidate input order therefore
  cannot affect output.
- No-candidate, temporal, spatial, and unavailable-network transitions split a
  trace explicitly. Matching restarts after a split and never emits a connector
  across it.
- A decoded chunk is rejected when it has no minimum positive movement, excessive
  mean cost, or unresolved ambiguity across most observations.
- Coverage portions come only from positive-length directed network transitions.
  First and last segments are clipped to candidate positions. A projection or
  stationary observation alone never creates a traversal.

## Synthetic Corpus

The table-driven tests cover:

1. straight travel through an intersection without cross-street attribution;
2. a motion-supported true turn;
3. a single-point cross-street excursion;
4. a geometrically crossing but topologically separate grade crossing;
5. divided and parallel roads;
6. stationary jitter;
7. long temporal and spatial gaps;
8. a sparse trace;
9. same-name, disconnected paths in different localities;
10. a deliberate no-match outside candidate bounds;
11. determinism after seeded candidate shuffling; and
12. duplicate stationary observations.

Additional focused tests verify optional heading evidence, supplied-candidate
bounds, and the proximity-only no-traversal invariant.

## Measured Result

Command on 8/31/2026:

```text
go test ./internal/coverage/... -v
```

One local run over the 12 table fixtures reported:

| Metric | Result |
| --- | ---: |
| Expected-segment precision | 1.000 |
| Expected-segment recall | 1.000 |
| False cross-street attributions | 0 |
| Missed expected turns | 0 |
| Unmatched observations | 2 |
| Ambiguous accepted observations | 2 |
| Rejected observations | 8 |
| Matcher runtime accumulated in fixtures | 169.968 microseconds |

The complete package test completed in 0.002 seconds. Runtime is measured around
`Match` calls only and varies by host and run; it is included to exercise the
metric, not as a performance guarantee. Precision and recall are set-based per
fixture over expected directed segment IDs. Cross-street labels and expected
ordered turns are evaluated separately.

### NorCal adapter evidence

Read-only xdev measurements on the active 2,020,900-way, 4,467,193-segment
NorCal generation used the public ADR 0008 benchmark coordinate; no workout
coordinates were read:

| Operation | Result |
| --- | ---: |
| 50 m provenance-preserving candidates | 30 |
| Candidate query, cold | 255.753 ms |
| Candidate query, warm | 7.892 ms |
| Incident query, cold including seed lookup | 27.811 ms |
| Incident query, warm including seed lookup | 9.987 ms |
| Start-node index size | 113 MB |
| End-node index size | 117 MB |

Plans use `canonical_path_segments_geography_gist_g1`,
`path_segments_g1_start_graph_node_id_idx`, and
`path_segments_g1_end_graph_node_id_idx`. A live read-only snapshot integration
including generation SHA, candidate projection/provenance, and incident-edge
decoding completed in 0.97 seconds.

A synthetic two-observation trace generated along an actual public candidate's
projected tangent exercised the complete adapter-to-decoder bridge: 4 candidates,
311 graph nodes, 478 physical edges, 21 distance-bounded expansion layers, and one
accepted traversal with 2 positive portions at total cost 0.0550.

The manual `coverage-evaluate` command reads one routed workout at a time, applies
versioned adaptive 5-meter/5-second sampling, and evaluates overlapping windows of
at most 128 observations under one two-minute read-only OSM snapshot per workout.
Its JSON report contains only aggregate outcome/mode counts, observation statuses,
candidate and graph bounds, traversal/portion counts, unique public segment count,
safe error categories, and durations. It emits no account/workout IDs, route
coordinates, timestamps, labels, path names, source labels, or arbitrary database
errors. Configuration is `MIGRATION_DATABASE_URL`, `OSM_DATABASE_URL`,
`COVERAGE_EVALUATION_ACCOUNT`, optional `COVERAGE_EVALUATION_LIMIT` (1..500,
default 20), and `COVERAGE_MIN_TRAVERSAL_METERS` (0.1..100, default 5).

### Private Operational Evaluation

Owner-approved route evaluations are retained in private operational records.
Aggregate matching outcomes and runtime alone cannot establish false-positive or
false-negative accuracy without independent labels. Public synthetic fixtures and
public-coordinate adapter measurements above retain their separate scopes.

## Known Gaps

- The diagnostic adapter clips canonical source polylines and persists valid
  traversal geometry. Geographic projection tolerance should continue to be
  evaluated as routes from additional devices and regions become available.
- Altitude, turn restrictions, and OSM conditional restrictions are not represented.
  The accepted versioned foot, bicycle, and conservative shared-public access and
  direction policy remains subject to future diagnostic regression review.
- The labeled corpus is finite and is not representative of all devices, regions,
  dense urban grids, switchbacks or tunnels. Broader diagnostic coverage remains
  useful rather than an acceptance blocker.
- Ambiguity is reported from near-equal dynamic-programming state costs. It is
  not a calibrated posterior probability.
- Runtime evidence now includes synthetic graphs, the public-coordinate NorCal
  smoke evaluation, aggregate route evaluation, and repeated deployed diagnostics.
  Dense downtown, switchback, and tunnel cases still require representative
  measurement.
- Diagnostics persist map-data/rules provenance, immutable rematch runs, labels,
  and exact segment evidence. Durable production matches, logical-path counting,
  aggregation, and Coverage rendering remain the next implementation phase.

### Historical Policy Refinements

These are generalized algorithm notes from the experimental policy review, not
replacement synthetic evidence or new measured results. Owner-specific route
analysis and regression measurements remain in private operational records.
Current production semantics remain governed by the versioned matching contract.

- v2-v7 refine candidate eligibility: accessory exclusion applies to both candidate
  and transition graphs; eligibility filtering precedes decoder truncation. The
  accuracy-derived radius can be 7.5 meters; the historical foot/bicycle search
  floor progressed through 12 to 20 meters, with
  raw candidate bounds progressing through 64 to 128 and at most 16 HMM states.
  Generic-path suppression remains 5 meters. Private drivable streets can support
  road attribution without making driveways or parking aisles ordinary candidates.
- v8-v14 distinguish reported accuracy from road-edge attribution. Width comes
  from explicit tags or validated lane/roadside assumptions. Direction-only drift
  support is limited to 20 meters; the 20-40 meter supplemental corridor requires
  explicit parallel sidewalk support. Transition-only pedestrian topology may
  connect roads without becoming attributable evidence. Coalesce contiguous
  reverse fragments before zero-net cancellation, retain routing distance, require
  sidewalk support within the carriageway envelope plus 5 meters, and evaluate
  turn direction using two sampled observations on either side. The transition
  feasibility allowance is at most 15 meters per accepted projection endpoint;
  it does not reduce transition cost.
- v15-v18 promote crossing-aligned continuation candidates, with transverse
  suppression narrowed to one-way primary/secondary carriageways. Parking-service
  exclusions remain explicit. Heading-required drift allowance increases from
  5 to 7 meters. A short gap may close only when the identical directed physical
  segment resumes within four samples, never across hard splits or different roads.
- v19-v22 break equal road-envelope costs with raw distance, strengthening the
  secondary cost from 0.002 to 0.01 per square meter. Same-logical-road bridges
  retain the 30-meter cap while the observation bound increases from 8 to 16.
  Named or bridge independent paths survive nearby-road suppression and use a
  switch cost of 2 rather than 0.75 to discourage brief road excursions.
- v23-v31 repair short excursions only on the surrounding logical path's graph.
  Repairs cover internal portions, suffixes and traversal boundaries; path-only
  connectors are capped at 250 meters and gap filling follows excursion repair.
  Boundary repairs normally require bidirectional graph/raw-distance agreement;
  the bounded named/bridge-path exception does not extend to roads or unnamed
  paths. A same-physical-segment U-turn may reverse at its projected point while
  retaining U-turn cost, without inventing an endpoint loop.
- v32-v39 distinguish path/road class continuity from logical-path identity. The
  v33 class-level postprocessor was rejected; v34 restores evidence-preserving HMM
  scoring with class-switch cost 8. Boundary class repair requires a uniform short
  middle road run, path-only routing, bidirectional raw support and 75/500-meter
  and 64-observation bounds. Same-logical-path middle/suffix replacement is capped
  at 30 meters. Post-repair coalescing merges only same-direction portions so
  legitimate out-and-back travel survives.
- v40-v47 promote only selected, bounded continuity geometry. Degree-one road
  endpoints receive 3 meters of attribution allowance within 1 meter of the node.
  Raw-supported driveway corridors may contribute ambiguous connector evidence.
  Private pedestrian access is permission-limited rather than prohibited; explicit
  `no` restrictions remain enforced and private accessories remain transition-only.
  Violations of the 15-meter-per-second transition cap must not be bridged without
  correcting proven source timing.
- v50-v54 allow bounded raw-supported accessory continuity and exact-boundary
  shortcut replacement only with substantial savings and strong raw support.
  Path endpoint completion requires continuous support for omitted tails and may
  absorb a directly connected eligible path below the standalone 5-meter threshold,
  without passing its next sharp turn or graph node. Temporal/spatial hard splits
  clamp endpoint windows; a relocated continuation cannot prove completion.
- v56-v59 use material endpoint approach/departure and at least 5 meters of selected
  span, rather than a full-segment endpoint chord. Walking, running and hiking
  share foot movement mode. Accuracy-aware completion must
  not invent a connection across a topological gap. Road continuity alternatives
  require exact boundaries, raw corridor support, path-name continuity and
  substantial savings. Named private neighborhood driveways may be road candidates;
  unnamed residential driveways remain excluded. Unsupported multi-edge dead-end
  palindromes require turnaround support within 15 meters.
- v60-v65 admit only strongly raw-supported parking runs between eligible roads,
  clipping evidence to supported progress rather than extending adjacent roads.
  Turn-spur repair requires a connected raw-bounded replacement. Single-edge turn
  reversals need directed support for both legs, not endpoint proximity alone.
  Parking-loop shadow checks use source geometry rather than coincident endpoint
  chords, including when a shorter predominant route contains multiple segments.
- v66-v69 preserve fully reached residential turnarounds, remove unsupported
  side-road prefixes and repair turns with connected alternatives. Same-road tail
  replacement requires ordered endpoint and raw path-distance support. Parallel
  parking/alley choices and short connectors require actual directed travel, not
  a fabricated road/path jump.
- v70 and v72 broad boundary/head inference were rejected after unrelated corpus
  changes; v71 and v73 restore the prior accepted behavior. Narrow v74 inference
  requires a two-or-three-segment ordinary-road branch on one logical path/source
  way, totaling 60-150 meters, with ordered out-and-back endpoint support and
  through travel on that same road. Competing first branches remain disqualifying.
- v75-v79 constrain boundary completion to a selected driveway's source way and
  connected raw-supported road turns. Parking-shadow removal requires surrounding
  directly connected subdivisions on one logical path/source way and a selected
  parking run of at least 40 meters. Service/driveway reversal retention requires
  directional support; a perpendicular crossing alone is insufficient. Same-way
  road continuation must remain distinct from a perpendicular service excursion.
- v80-v82 complete adjacent directed road portions to their exact shared node only
  when both already contribute at least 20 meters and raw observations approach
  that node. Across evaluator windows, one bounded centered rematch may only extend
  the same physical IDs/directions with containing intervals and at most 30 added
  meters. Long supported connections preserve genuine dead-end out-and-back travel.
  Ordinary-road dead-end cleanup applies only to full physical-segment reversals;
  endpoint-supported residential turnarounds and supported partial reversals remain.

Regression review must cover displaced sidewalk traces, parallel carriageways,
path/road boundaries, private-access distinctions, unsupported service/parking
excursions, genuine turnarounds, curved endpoints, graph disconnections and window
seams. Compare exact physical sets, directions, source intervals and multiplicity
under fixed source/rules versions. Functional recovery is not HTTP-envelope,
persisted-result, universal-equivalence or concurrent-capacity certification.

## Exact Parking Repair Follow-Up (10/19/2026)

Historical source-only checkpoint. The appended 10/20 deployment receipt
supersedes its undeployed/live-status wording, not the recorded test/probe scope.

This source-only checkpoint supplements the historical evaluation above. The new
production change is confined to `internal/coverage/matcher.go`; it is **not
deployed** and changes neither SQL versions nor `coverage-production-v1`.
Implementation labels **V1** (varint keys) and **V2** (varints plus the same-cursor
push proof) below are not matcher-policy versions. Live remains `dev-20261018`,
Helm revision 28, with the old database contract. New folded-028 locking,
eligible-cohort and mixed-retry behavior remains undeployed; active migration
source remains 001-028, schema/supported/minimum runtime 28 and marker 27.

### Exact Keys And Equal-Work Synthetic Measurement

State keys encode the cursor, portion count and ordered exact portion IDs using
canonical unsigned, self-delimiting varints. IDs intern every portion field,
including exact floating-point bits, signed zero and NaN payloads. The resulting
keys are owning strings, not probabilistic hashes; no hash-collision assumption
stands in for equality. IDs are not reused during a traversal.

For equal synthetic work of 500 keys, each containing 508 portions:

| Synthetic fixture | Fixed-width key bytes | Varint key bytes | Key-payload reduction |
|---|---:|---:|---:|
| Benchmark fixture | 1,024,000 | 442,500 | About 56.8% |
| Separate implementation fixture | 1,024,000 | 446,000 | About 56.4% |

These are different fixtures with different ID distributions, not inconsistent
measurements of one fixture. They measure serialized key payload, not total
allocation, live heap or process RSS. The equal-work benchmark uses a test-only
allowance to retain all keys for comparison; production retains its 512 KiB cap.
Smaller keys alone cannot terminate a rewrite that keeps inserting portions at
the same cursor. They can merely postpone the existing guard.

### Same-Cursor Push Certificate

V2 adds a sufficient exact certificate for positive-insertion head cycles of
period one or two. This complements the existing full-state and advancing-cursor
suffix certificates; it does not change raw-support predicates, margins, routing
choices, candidate order or supported-middle replacement policy.

At a fixed cursor, write the complete current vector as `L + H + X`, where `L`
is the full prefix, `H` is the five-portion head read by the bounded old-policy
candidate search, and `X` is the **entire** old tail. For the old policy's first
accepted candidate, independently normalize the local replacement to `R` using
the real merge operation. Certification requires the actual whole replacement
to equal `L + R + X`, field for field and float bit for float bit. A matching head
or tail anchor alone is insufficient.

The proof additionally requires:

- Unique opposite-direction selection across the full source graph, including
  map-selected aliases, so replay is deterministic without sorting old choices.
- Uninterrupted old-policy first-accepted-candidate execution at the same cursor;
  a skipped closing candidate or cursor advance clears continuation/push history.
- An exact five-portion head closure after one or two phases, with a positive
  insertion in at least one phase, rather than approximate geometry recurrence.
- Already normalized prefix and entire tail, preservation of the prefix/local
  boundary, and every phase's replay barriers against every carried tail and
  inserted block. Barriers call the real merge predicate, including its rewind
  and NaN comparison semantics, not an endpoint-distance approximation.

Only the candidate closing the certified nonterminating continuation is rejected.
Later candidate ends at that cursor are still tried; the certificate does not
discard the traversal or declare the route too long. Periods above two,
direction ambiguity and any failed proof obligation retain the existing bounded
behavior. This is not a complete nontermination detector. Terminating old-policy
oracle cases and unchanged goldens retain their complete results; no equality
with a nonterminating old run is claimed.

### Unchanged Bounds And Atomicity

Tracking retains the existing 512 KiB active-cursor key-payload cap, 4,096 states
and accepted replacements per traversal, 16,384 interned portions, 16,384 suffix
nodes and 8 MiB charged cache budget. No retained states are discarded to make a
run fit. Temporary encoding/map overhead remains additional to these logical
bounds; they are not a process-memory ceiling.

One optional inline push descriptor measures 2,624 bytes on the verified build
and is conservatively charged 4,096 bytes to that same cache budget. It retains
bounded head identities and boundary anchors, not a growing tail vector. History
can decline admission or be evicted to make room for mandatory portion, suffix
and continuation entries. This does not evict mandatory state or raise a bound.
Cancellation checks cover proof scans and key encoding. Replacement results
publish atomically only after all traversals complete; cancellation or exhaustion
returns an error without a partially modified result.

### Recorded Verification Boundary

These are supplied scoped receipts, not tests rerun by this documentation edit:

- `go test -count=1 ./internal/coverage/...` and the corresponding race suite pass.
- All 204 unchanged goldens pass. The 256-seed old-policy corpus includes 203
  terminating oracle cases, 47 with repairs; their complete results agree.
- The independent property corpus has 512 witnesses: 192 certified and 320
  declined. Agreement is **if and only if the independent certificate reference
  accepts**, not if and only if old-policy execution is nonterminating.
- Four fuzz targets pass at 30 seconds each, totaling 559,058 executions. Later-end
  escape, atomic cancellation, repeated original occurrences, ambiguous choices
  and optional-cache admission/eviction receive focused coverage.

The final full 58-worker integration rerun against the combined current source is
**pending**; older passes are not relabeled as that rerun. Final benchmark receipt
publication belongs to its verification owner. No user-owned probe inputs, dates,
counts, geometry, private paths or operational receipts are published here.

## Exact Parking Repair Deployment (10/20/2026)

The varint state keys and exact positive-growth period-one/two certificates
(`GrowthPeriod` 1/2) are **deployed in `dev-20261020`, Helm revision 29**. Released
matcher source SHA-256 is
`e8935386435aa107a1c36abf7c6792dd5bc923678ef4bf4ba4c023c14677b447`.
Policy remains `coverage-production-v1`; all default semantic limits,
500m CPU / 256 MiB, two lanes, admission limits and 6m/10m/15m budgets are unchanged.
This is not a new policy version or a generic termination/equivalence claim.

Production automatic retry of a mixed-failure cohort succeeded, with authoritative
current persisted results. This supersedes the earlier read-only-
probe-only evidence boundary for that recovery, not the failed historical
attempts. It does not establish independent completion of the entire year,
arbitrary-backlog throughput, or capacity certification. Dataset counts,
timings and private receipts remain in private operator records.

The folded schema-28 structural contract, UI follow-up, protected actual-backup
rehearsal and approved live repair are also complete; see
[current release evidence](development-progress.md#folded-028-production-release-10202026).
The historical 58-worker-rerun and benchmark-publication statements above remain
their dated verification checkpoints, not invented fresh full-suite passes.

### Independent Recovery Closure (10/20/2026)

Subsequent independent verification closes the historical year's available-map
recovery, including the deployed automatic successor and separately authorized
manual chains. All selected latest chains succeeded with authoritative current
results; original failed/partial outcomes remain historical and their resolved
parent presentation is derived Superseded. The earlier deployment checkpoint's
pending yearly boundary is superseded by
[independent closure](development-progress.md#independent-yearly-recovery-closure-10202026),
not by treating a core probe as persistence proof. Exact frozen-source SQL
signatures distinguish known background ROI reads from blocking matcher queries;
unknown queries still block the sampled physical-reader idle gate. Dataset
counts, dates, timings, storage and receipt identities remain private. This is
neither a clean-throughput benchmark nor M8 acceptance or M11 capacity certification.
