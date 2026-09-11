# Experimental Coverage Matcher Evaluation

## Status

Experimental evidence for ADR 0009. ADR 0009 remains **Proposed**. Production
Coverage state remains untouched. An owner-only, feature-gated diagnostic API
can persist bounded matcher evidence and labels; there is no diagnostic UI.

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
overlapping 128-point windows, one repeatable-read OSM snapshot, exact clipped
source geometry, and a final route revision/digest recheck. It stores no route
points, candidate clouds, OSM tags or names, free text, or private request input.
`PATCH /api/coverage-diagnostic-runs/{id}/labels` stores only fixed overall and
evidence label enums. Both endpoints are owner-only and cookie mutations require
CSRF. Disabled endpoints return 404 before authentication or either database.

For xdev enablement, set `api.coverageDiagnostics.enabled=true`; the temporary
Helm default reads the existing `osmDatabaseUrl` secret key. Before broader
rollout, provision a dedicated OSM matcher reader credential and change
`api.coverageDiagnostics.osmDatabaseKey` to that dedicated key.

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
`ExperimentalRules`. These values are test hypotheses, not accepted production
thresholds.

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

Command on 2026-08-31:

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

### Aggregate-only testuser evaluation

With explicit owner approval, the evaluator ran against testuser routes on xdev.
No route coordinates, timestamps, workout/account IDs, labels, names, or source
metadata were logged or retained in the report.

| Metric | First recent-foot run | Stratified run |
| --- | ---: | ---: |
| Workouts | 20 foot | 28 foot, 2 bicycle |
| Original observations | 190,177 | 239,400 |
| Sampled observations | 50,508 | 67,529 |
| Sampling reduction | 73.4% | 71.8% |
| Matched | 37,239 | 48,647 |
| Ambiguous | 1,754 | 2,679 |
| Unmatched | 8,222 | 11,900 |
| Rejected | 3,293 | 4,303 |
| Traversals | 1,061 | 1,496 |
| Positive portions | 38,652 | 51,286 |
| Unique public physical segments | 1,559 | 2,315 |
| Matcher errors | 0 | 0 |
| Total runtime | 103.4 s | 212.4 s |

The stratified run accepted 76.0% of sampled observations as matched or
ambiguous, with 17.6% unmatched and 6.4% rejected. Mean total runtime was about
7.1 seconds per workout and matching consumed about 3.1 milliseconds per sampled
observation. These are operational measurements only: without independent labels,
they cannot establish false-positive or false-negative accuracy.

## Known Gaps

- The adapter-to-decoder bridge is implemented, but persisted traversal geometry
  still requires clipping source polylines rather than retaining only meter
  intervals. Geographic projection tolerance also needs a larger fixture corpus.
- Altitude, turn restrictions, and OSM conditional restrictions are not represented.
  Experimental versioned foot, bicycle, and conservative shared-public access and
  direction policy is implemented, but still requires representative evaluation.
- Path policy v2 suppresses pedestrian-only candidates for foot and bicycle observations when
  a drivable centerline is within 5 meters. Label analysis found 146 Unexpected v2
  portions across 23 public segments: 11 sidewalks and 12 crossings. Sixteen were
  within 5 meters of a road and had leaked back through topology transitions; the
  remaining seven were 7.1-10.5 meters from centerlines. Policy v3 excludes tagged
  sidewalks/crossings from candidate and transition graphs while retaining untagged
  park/trail paths. Representative sidewalk-heavy diagnostics must now validate v4.
- A labeled 4,343-point Outdoor Walk produced 1,299 sampled observations under v3,
  with only 489 matched, 34 ambiguous, 738 unmatched, and 38 rejected. The missing
  spans corresponded to sidewalk traces beside road centerlines outside the
  accuracy-derived 7.5-meter radius. Policy v4 raises only foot/bicycle candidate
  search to a 12-meter floor while keeping generic park-path suppression at 5 meters.
- The first v4 rerun produced identical counts because candidate truncation happened
  before policy filtering. Policy v5 queries up to 64 raw candidates per observation,
  removes road accessories/ineligible states, and passes at most 16 eligible states
  to the decoder.
- Whole-route public-road distance analysis found 808 observations 12-20 meters
  from the nearest centerline. Policy v6 uses a 20-meter foot/bicycle road-search
  floor and 128 raw candidates while retaining the 5-meter generic-path suppression
  threshold and 16-state HMM bound.
- Runningwood Circle and Cuernavaca Circulo are OSM residential/service streets
  tagged `access=private`. v6 fetched but rejected them, leaving counts unchanged.
  Policy v7 permits private drivable street centerlines as attribution targets for
  owner foot/bicycle traces while continuing to reject private pedestrian paths,
  driveways, and parking aisles.
- On the labeled 2025-09-24 Outdoor Walk, v7 increased matched observations from
  489 to 658, reduced unmatched observations from 738 to 569, reduced no-candidate
  splits from 746 to 575, and increased positive portions from 538 to 708. This
  confirms private-road rejection was material while leaving additional gaps for
  review.
- The 2025-10-29 Outdoor Run showed that v7 still discarded road candidates after
  fetching them: its 1.3-meter reported accuracy produced a 7.5-meter decoder
  radius, while sidewalk traces were commonly 9-13 meters from the road centerline.
  Policy v8 scores foot/bicycle road candidates relative to an estimated road edge
  plus sidewalk setback. It prefers explicit width and otherwise estimates width
  from lane and roadside-facility tags using validated deployment assumptions.
- The 2025-10-28 Outdoor Walk retained 359 unmatched observations under v8.
  Heatherstone Way and Dale Avenue had sustained 15-40 meter trace displacement
  despite reported 1.3-3 meter accuracy, while mapped sidewalk candidates remained
  close and parallel. At Americana/El Camino/Sylvan, excluded pedestrian topology
  forced a road-graph detour over El Camino. Policy v9 evaluates parallel mapped
  sidewalks as road-attribution support and permits non-attributable pedestrian
  transition edges; representative reruns must measure both recovery and false
  cross-street attribution.
- The final v10 refinement limits direction-only drift tolerance to road candidates
  within 20 meters; only explicit parallel sidewalk support can promote a road from
  the 20-40 meter supplemental corridor. It also penalizes transient logical-path
  switches and immediate physical-segment direction reversals. On the target walk,
  the preceding candidate behavior increased Heatherstone from 170.6 to 362.0
  meters and Dale from 430.2 to 542.2 meters, removed West El Camino evidence, and
  reduced East El Camino from 425.6 to 130.9 meters before zero-net cleanup.
  The remaining intersection artifact was an exact 12.3-meter reverse/forward pair
  on one El Camino physical segment inside a single network transition; v10 cancels
  such zero-net pairs from finalized traversal evidence under v12 while retaining
  their routing distance, including when the pair spans adjacent sampled transitions.
  The deployed v12 rerun retained 362.0 meters on Heatherstone, 542.2 meters on
  Dale, 210.0 meters on The Americana, and 465.6 meters on Sylvan; it removed all
  West El Camino evidence and the identified 24.7-meter East El Camino inverse
  pair. The remaining 98.2 meters of East El Camino occurs in a separate return
  traversal rather than the Americana-to-Sylvan transition.
- v13 addresses three owner-reviewed v12 artifacts: contiguous reverse fragments
  are coalesced before zero-net cancellation; sidewalk-to-road support must fit the
  candidate carriageway's estimated lateral envelope plus 5 meters; and turn-point
  directional support considers two sampled observations on either side. These
  target the unexpected service segment, the Dale-to-Continental turn gap, and the
  unsupported Americana carriageway switch respectively.
- v14 addresses the remaining Dale-to-Continental centerline gap by adding at most
  15 meters of accepted projection envelope per endpoint to the hard transition
  feasibility bound. It does not reduce transition cost and therefore only avoids
  premature graph rejection of a drifted or corner-cutting pedestrian turn.
  The deployed rerun joins Dale and Continental at their exact shared OSM node,
  emits no evidence for reviewed service segment
  `2E304D5BE098E31C0A70A8298B376CC0`, and retains only the west/raw-route-side
  Americana carriageway before El Camino. It produced 785 matched, 296 ambiguous,
  301 unmatched, and 33 rejected observations. The 2025-10-29 regression retained
  2,234 matched and 436 ambiguous observations with 32 unmatched and 21 rejected.
- v15 targets the two disconnected El Camino stubs at Americana/Sylvan. OSM already
  contains a direct Sylvan continuation from the west Americana endpoint, but v14
  selected two transverse El Camino states around a transition-only crosswalk. v15
  promotes road candidates parallel to the closest tagged crossing and suppresses
  transverse road candidates while an observation is on that crossing.
- v16 narrows transverse suppression to one-way primary/secondary carriageways
  after v15 increased fragmentation on the 2025-10-29 regression route. The aligned
  continuation promotion remains available at all tagged crossings.
  The deployed v16 walk rerun contains neither reported El Camino physical segment
  and emits the connected source-way chain `550541672 -> 707878800 -> 8935940 ->
  417071555` from west Americana through the direct Sylvan continuation. It yielded
  775 matched, 306 ambiguous, 301 unmatched, and 33 rejected observations. The
  2025-10-29 regression retained 2,214 matched and 456 ambiguous observations with
  32 unmatched and 21 rejected, equal in accepted-observation total to v14.
- v17 targets a northbound Wright Avenue discontinuity where an 8.3-meter
  `service=parking_aisle` candidate sat under the GPS trace while Wright remained
  16.6-20.0 meters away. The service-way exclusion now matches the documented v7
  intent, and the heading-required drift allowance increases from 5 to 7 meters so
  the two-lane Wright envelope reaches 20.5 meters without widening the query.
- v18 fills the residual 11-meter Wright gap only when adjacent traversals resume
  the identical directed physical segment within four samples. This follows removal
  of the two parking-service candidates and cannot bridge temporal/spatial gaps or
  different physical roads.
  The deployed rerun emits no evidence for physical segment
  `85B205D190F4F1F88998F0F4C47FA599` and replaces the two separated intervals on
  Wright physical segment `B96E0D23047BEC13122D5D36429F2098` with one continuous
  37.8-meter portion. Traversal count drops from 33 to 31 with observation statuses
  unchanged. The v18 October 28 regression remains unchanged from v17.
- v19 targets the 2025-10-27 Grant/Fremont review. Competing Grant carriageways
  previously tied inside a zero-cost road envelope despite the expected branch being
  0-2 meters from the trace and the selected branch growing to 4-15 meters away.
  A 0.002-per-square-meter secondary cost preserves the envelope while breaking that
  tie. Same-logical-path graph bridges address the 11.9-meter Grant merge and roughly
  28-meter Fremont gap, while traversal-boundary inverse cancellation addresses the
  Belleville/Fremont nested zero-net excursion.
- The v19 live rerun removed the Fremont nested excursion and completed the
  11.9-meter Grant merge, but retained the wrong Grant fork and 27.8-meter Fremont
  gap. Policy v20 strengthens the raw-distance tie-break from 0.002 to 0.01 and
  raises only the bridge observation-count bound from 8 to 16 while retaining the
  30-meter same-logical-road limit.
- On the 2025-10-27 route, v20 suppressed the named Stevens Creek Trail candidates
  where its bridge met Heatherstone/Dale because a road centerline was within 5
  meters. The decoder selected a 5.6-meter road stub, then resumed the trail at an
  interior projection. Policy v21 retains named or `bridge=yes` independent path
  candidates near roads while preserving generic unnamed-path suppression.
- The v21 rerun retained the Dale stub because the road's 0.5-5 meter proximity
  overcame the trail's 4-6 meter emission and the 0.75 switch cost. Policy v22 uses
  a switch cost of 2 only for named/bridge independent paths, penalizing the short
  Trail-to-road-to-Trail excursion twice while charging a genuine turn once.
- v23 addresses the network split that isolates that excursion. The surrounding
  Stevens Creek Trail traversals require roughly 155 meters of trail graph distance,
  while the raw route covers 136.8 meters and the intervening Dale stub is 5.6
  meters. A bounded same-path replacement uses graph/raw-distance agreement rather
  than a location-specific segment rule; the unrelated Rainbow service segment does
  not have matching surrounding logical paths and is therefore unaffected.
- The v23 rerun remained unchanged because transition-only topology kept the Dale
  stub and both Stevens Creek Trail portions inside one decoded traversal. Policy
  v24 applies the same short-excursion replacement within a traversal, requiring a
  connector wholly on the surrounding logical path and capped at 250 meters.
- The v24 rerun remained unchanged because unconstrained shortest-path routing again
  selected the Dale/sidewalk shortcut, which then failed same-path validation.
  Policy v25 runs repair routing on the surrounding logical path's subgraph so the
  intended bounded trail connector can be evaluated directly.
- The v25 rerun remained unchanged because the Dale stub is the suffix of one
  traversal and the restarted trail begins the next. Policy v26 handles this
  boundary shape while requiring identical surrounding logical paths; the Rainbow
  segment remains ineligible for replacement because its next path differs.
- Four v20-v26 artifacts at Homestead/South Mary formed a graph-enforced U-turn
  loop: Homestead extension, Mary connector, and two crossing segments. Policy v27
  allows a same-physical-segment direction reversal at the projected point, retaining
  the U-turn cost but avoiding an endpoint/intersection loop not present in raw data.
  The deployed v27 rerun emits none of the four reviewed physical segments. It
  improves October 29 from 2,307/301 matched/ambiguous and 33 rejected to
  2,340/289 and 12 rejected, with unmatched fixed at 82. October 27 improves to
  2,103 matched with the trail repair retained; October 28 improves to 930 matched
  and 18 rejected while preserving Rainbow segment
  `1E92DDE708C34F99830F0C4A88525A6E` unchanged at 8.4 meters.
- v27's direct U-turn model removed the Homestead loop but changed the Stevens Creek
  Trail traversal boundary, losing v26's complete 284-meter connector. Policy v28
  directly bridges adjacent same-path traversals when a path-only graph connector
  and accumulated raw distance agree in both directions. This admits the roughly
  155-meter trail connector for 136.8 meters of raw travel but rejects a short graph
  shortcut across a materially longer off-path detour.
- The v28 rerun remained disconnected because both trail portions were adjacent
  inside one accepted traversal rather than at a boundary. Policy v29 fills such an
  intra-traversal same-path gap on the path-filtered graph, still capped at 250
  meters, while retaining the stricter raw-distance check for traversal boundaries.
- The v29 rerun remained disconnected because same-path gap filling ran before the
  Dale-stub replacement made the two trail portions adjacent. Policy v30 orders gap
  filling after all excursion replacements without changing any acceptance bound.
- The v30 rerun remained disconnected because the actual boundary compares a
  155-meter path connector against one short sample transition; travel accumulated
  on earlier projections of the self-looping bridge geometry. Policy v31 permits the
  bounded connector only for graph segments classified as named/bridge independent
  paths. Road and unnamed-path boundary repairs still require raw-distance agreement.
- On the 2025-10-24 run, two connected unnamed `highway=cycleway` segments from OSM
  source way 195656378 received different logical-path IDs around trail junctions.
  A short interval selected closer West Dana Street despite viable trail candidates
  for 68 of 77 raw observations. Policy v32 adds path-versus-road continuity scoring
  without changing distance acceptance or logical-path identity.
- The v32 rerun retained West Dana because the path-road interval remained inside
  one accepted traversal. Policy v33 applies a bounded class-level excursion repair:
  the roughly 60-meter road run may be replaced only by a path-only graph connector
  capped at 250 meters, while sustained class changes remain untouched.
- The v33 rerun retained West Dana while removing 12 unrelated evidence segments,
  so its class-level postprocessor was rejected. Policy v34 removes that rewrite and
  raises only the HMM class-switch cost from 2 to 8, preserving candidate and
  evidence geometry while making a brief path-road-path excursion pay twice.
- The v34 rerun retained West Dana, indicating the road interval spans a standalone
  traversal. Policy v35 permits only a `path | short road | path` boundary repair
  with a uniform middle class, path-only graph connector, bidirectional raw-distance
  agreement, and strict 75/500-meter and 64-observation bounds.
- On the 2025-10-25 walk, v35 selected a 21.9-meter two-portion Vallco traversal
  between two Wolfe traversals on the same logical path. Policy v36's in-traversal
  generalization did not apply because the Vallco interval is its own traversal.
  Policy v37 raises the existing same-logical cross-traversal middle cap to 30 meters
  while retaining Wolfe-only routing and raw-distance/hard-split safeguards.
- The v37 rerun retained Vallco because both portions are a suffix of the preceding
  Wolfe traversal rather than a standalone middle traversal. Policy v38 generalizes
  the existing `A -> short B | A` repair to a contiguous suffix totaling at most 30
  meters, still requiring an A-only graph connector.
- The v38 Wolfe repair succeeded but its post-merge inverse cancellation removed a
  legitimate Rainbow Drive out-and-back physical segment on the 2025-10-28 walk.
  Policy v39 coalesces only same-direction portions after this boundary repair,
  preserving reversals while retaining the Vallco replacement.
- The deployed v39 rerun emits neither Vallco segment
  `60F84F22666AFB9AF9877FADE5D55E1D` nor `ABFF284273DF05D64A8389E09E4DE960`.
  Wolfe is endpoint-contiguous through `3A5815D6513A7EED8AA054DAB1ABA312`,
  connector segments `9988FB37BAC3BE3E69C5355C28FB7331` and
  `85D20530C4BB95BC4D23CC705BEFAC99`, and
  `12444568B55639A02452AE0F50566AA5`. October 28 retains both directions of
  legitimate Rainbow segment `BC285C32AFBA8EF3F67621A79DDCABBB` totaling 95.4
  meters and the reviewed 8.4-meter service segment. October 27 remains at 2,104
  matched/296 ambiguous/113 unmatched/4 rejected with the complete 284-meter trail
  repair, while October 29 remains at 2,342/287/82/12 with all reviewed artifacts
  absent.
- The deployed v40 rerun promotes only two short crossing edges already selected
  by accepted transitions on the 2025-10-25 walk:
  `4B2F41496770DFCA2AF62C2A52BD0AC3` at 4.096 meters between
  `C3E24779452B4503EDB017209ECB899A` and
  `738DD54A8348825E3D3D49A32A2B98EC`, and
  `5C1C541966A69095D94D5D7134E63DF5` at 4.495 meters between
  `6994E19C4C6FD051BBFA525C152FC0BD` and
  `E64A8980F87DB39EF3D58F4E4F1297AE`. Point classifications and traversal count
  remain `449/54/110/5` and 12; portions increase from 91 to 93 and unique
  segments from 64 to 66. The complete evidence rows for the reviewed October 27,
  28, and 29 routes are unchanged from v39. October 27 retains the complete
  283.962-meter Stevens Creek Trail segment, October 28 retains both Rainbow
  directions totaling 95.365 meters and the 8.433-meter reviewed service segment,
  and all reviewed October 29 artifacts remain absent.
- The 2025-10-16 walk exposes a remaining dead-end turning-bulb threshold cliff.
  Viterbi selects both directions of the southern Madrone Avenue cul-de-sac,
  `F69974F6E88F08818694F8DE4936A93C` and
  `B2A38F95041DDB9E433E5BB410EA5405`, between the cited through segments
  `B41415DDBA2F692106B0D8D3A7D4B5FA` and
  `D608B4BEF6F4D2A03F234C50B11F8EAD`. At raw sequence 1605, however, the
  terminal-segment projection is 14.01 meters from the centerline. Its 6-meter
  road-edge offset leaves an 8.01-meter effective distance, 0.51 meters beyond
  the 7.5-meter candidate radius. That single `no_candidate` observation splits
  the outbound and return traversals, after which traversal-boundary backtrack
  cleanup cancels their inverse cul-de-sac spans. The nearby northern analog,
  `5C703ECC4400727D615244AD36F62C1C` and
  `6310C291039A83C4FC3849A4BB5F524D`, reaches only a 12.95-meter centerline
  distance and remains inside the same envelope, so its out-and-back survives.
  A future rule should model extra attribution width only at a degree-one road
  endpoint or preserve a raw-supported dead-end U-turn across a very short
  no-candidate gap; globally widening road attribution or disabling inverse
  cleanup would be less controlled.
- The deployed v41 policy adds 3 meters of attribution allowance only within
  1 meter of a degree-one drivable-road endpoint. On the 2025-10-16 walk it
  restores the southern cul-de-sac as full forward and reverse spans on
  `F69974F6E88F08818694F8DE4936A93C` (9.447 meters) and
  `B2A38F95041DDB9E433E5BB410EA5405` (22.797 meters). It also restores one
  additional raw-supported Madrone Avenue dead-end branch comprising
  `8CE40613AC5BC4024C109B2067312C26`, `FB52020FB3871B8CE11D50D82C272BE7`,
  and `629324C93CF5808FDBB5FFFF2639BC17`. Counts move from
  `851/204/44/6` to `854/203/42/6`; traversals fall from 20 to 18 as the two
  isolated no-candidate U-turn splits close, portions rise from 208 to 218, and
  unique segments rise from 128 to 133. The 2025-10-25 geometry is unchanged
  from v40. October 27 and 29 retain byte-equivalent traversal geometry, while
  October 28 retains the same physical segment set and extends one terminal span
  by 0.509 meters with a fully overlapping 0.855-meter reverse fragment. All
  reviewed October 27-29 artifacts remain absent, including the rejected trail,
  Rainbow/service behavior, and October 29 false positives.
- Two remaining 2025-10-16 path-to-road discontinuities are caused by explicit
  driveway exclusion rather than failed graph search. OSM path
  `EFC2948F0260ECFD821746803D265963` shares its north endpoint directly with
  Madrone Avenue, but its south endpoint reaches the junction of Ferndale
  segments `156733EEBCCDE102CB8230BDC6D42E23` and
  `2EB4D92696AD2831AF380954C56E2882` only through driveway segments
  `0D870DB64FAEA99088284879502540C0` (26.812 meters) and
  `411C6FAF7A8A6CF5415387C45891BD22` (7.205 meters). Likewise, paths
  `2AAF63DC5EF5697DF7E9FD9AD5CE97C0` and
  `1FF5B2128F1B82D732C8C078B54A0FBB` share a north endpoint directly with
  Manzanita Avenue but reach `DE5554CF246468F7C938C21AA8DB22CA` only through
  driveways `2FFE5CF5897FDA7B6ECC4BB749F8791A` (15.922 meters) and
  `9F2BF4FDB8BA96AD9A6BAEB535728A66` (6.958 meters). The raw route continuously
  follows both connector corridors, and none of the four driveway ways has a
  denied-access tag. Current foot/bicycle policy nevertheless excludes
  `service=driveway` from both candidate and transition eligibility, so the HMM
  cannot connect either side. Making narrowly qualified driveways transition-only
  could preserve decoding continuity without attributing or rendering private
  driveway coverage; rendering a continuous line would be a separate product
  decision because it would promote those driveway geometries into coverage.
- The deployed v46 policy promotes both raw-supported driveway corridors as
  ambiguous evidence in both directions. The Ferndale connector adds
  `0D870DB64FAEA99088284879502540C0` at 26.812 meters and its 7.205-meter
  road-centerline join `411C6FAF7A8A6CF5415387C45891BD22`; the Manzanita
  connector adds `2FFE5CF5897FDA7B6ECC4BB749F8791A` at 15.922 meters and
  `9F2BF4FDB8BA96AD9A6BAEB535728A66` at 6.958 meters. Relative to v41, the
  2025-10-16 run changes only by those four physical segments, increases portions
  from 218 to 234 and unique segments from 133 to 137, and reduces traversals from
  18 to 14 by joining the four path/road boundaries. Point classifications remain
  `852/205/42/6`. The reviewed October 25, 27, 28, and 29 routes retain exactly the
  same physical segment sets as v41 and all previously reviewed outcomes remain
  intact.
- The 2025-10-14 Outdoor Run has two unrelated gap causes. Between Prospect Road
  segments `02850E6319EC182912B3E5B68FB76435` and
  `2FD47CFB72B53E1A52E0D28DD609DFB6`, raw sequences 5685-5693 advance at
  approximately 17.1-17.4 meters per second. That exceeds the 15-meter-per-second
  transition cap and creates repeated `network_gap` splits, omitting eligible
  6.308-meter Prospect connector `EC88D5990A5FBB1CFD1D820E68885AEA` and
  adjacent segment tails. The coordinates and one-second timestamps independently
  imply that speed, so continuity repair should not bridge it unless source timing
  is proven incorrect. The gap from `0C46D333B1D56B812104658706BAE86F`
  to `68F536674454CDCC350254460BA28C30` instead follows mapped private pedestrian
  geometry: paths `7B64415515241C945215B69A96B0D595` and
  `902D744B7552C0283E21917C1A383571`, plus private sidewalk/crossing links near
  Seven Springs Parkway. All carry `access=private`, so foot policy excludes them,
  while nearby private residential roads remain candidate-eligible for road-edge
  attribution. Counting strongly raw-supported private pedestrian paths would
  require an explicit access-policy decision rather than a continuity repair.
- The deployed v47 policy treats private pedestrian access as permission-limited
  rather than prohibited while preserving explicit `no` restrictions. On the
  2025-10-14 run, private park footway `902D744B7552C0283E21917C1A383571`
  is emitted for its complete 34.385 meters in each direction, and
  `7B64415515241C945215B69A96B0D595` is emitted across the directly supported
  spans. Private sidewalk and crossing members remain transition-only. Counts move
  from `1333/161/269/36` to `1387/154/223/35`; traversals fall from 27 to 24,
  portions rise from 205 to 213, and unique segments rise from 139 to 144. The
  route replaces several nearby private-road projections with ten directly
  supported path, crossing, and continuity segments while removing five prior
  road projections. The reviewed October 25, 27, 28, and 29 physical segment sets
  are unchanged from v46. The October 16 walk likewise shifts from nearby road
  projections onto eight directly traversed private-path/continuity segments while
  removing three prior projections, reducing unmatched points from 42 to 1.
- The remaining visible gaps around private park path
  `902D744B7552C0283E21917C1A383571` are hidden accessory geometry, not graph
  discontinuities. Its outbound connection to
  `128C24244B96D1937C4C51E16BCB0F01` uses private sidewalk segments
  `77ADD591DCF9125D82434F89C2705188` (11.405 meters) and
  `62064A775D748F3EAFAF3E6FCBB3CF10` (8.501 meters). Its return connection from
  `2279B7A883770B82CA91E4254DFDE963` uses private crossing
  `460FDDAF20E88A4A48E315FDCD47340C` (8.675 meters) followed by the same
  11.405-meter sidewalk in reverse. v47 permits these private accessories in the
  transition graph, so the selected HMM route is continuous, but evidence emission
  suppresses sidewalks and allows only all-crossing/traffic-island runs totaling
  at most 10 meters. A future continuity exception could emit a raw-supported
  accessory run of approximately 20 meters when it directly connects an
  attributable pedestrian path, without globally making sidewalks candidates.
- The deployed v50 policy emits both reviewed park accessory runs as ambiguous
  evidence. Outbound, `902D744B7552C0283E21917C1A383571` connects through
  `77ADD591DCF9125D82434F89C2705188` (11.405-meter sidewalk) and
  `62064A775D748F3EAFAF3E6FCBB3CF10` (8.501-meter sidewalk) to
  `128C24244B96D1937C4C51E16BCB0F01`. On return,
  `2279B7A883770B82CA91E4254DFDE963` connects through
  `460FDDAF20E88A4A48E315FDCD47340C` (8.675-meter crossing) and the same
  11.405-meter sidewalk in reverse to `902D744B7552C0283E21917C1A383571`.
  Relative to v47, point classifications and traversal count remain
  `1387/154/223/35` and 24; portions rise from 213 to 221 and unique segments
  from 144 to 151 through seven raw-supported accessory connectors. The reviewed
  October 16, 25, 27, 28, and 29 routes retain exactly the same physical segment
  sets as v47; October 16 has only evidence-class and portion-coalescing changes.
- One v50 park traversal still exposes a candidate-state detour. The direct OSM
  route from crossing `1EB6E14E4CF4131B8D56E0FF8A4243BD` to sidewalk
  `0CE76301BDEF68E639F206DEB7DAAD22` is crossing
  `7C7ED19BEBABAED990BBDEB0D6797B98` in reverse, totaling 5.992 meters. Raw
  sequences 194-210 travel north over that connector and pass within 1.8 meters
  of its midpoint. Because crossings and sidewalks remain transition-only, none
  can serve as an observation state; road-edge attribution instead selects private
  road `0DF729B8B0B635C78F334BC8FF43B4E9`, road
  `B1B9E95A7D0847F7F0F53F09A8B5E845`, and crossing
  `35D97F8A81D9458778FD5EAEB5EC382C`, producing a 23.151-meter west/north/east
  detour. Accessory emission exposes that selected route but does not replace it
  with the shorter raw-supported route. A future repair should compare short
  decoded road excursions against a direct accessory-only graph route and replace
  the excursion only when the direct route is strongly raw-supported,
  substantially shorter, and bounded by the same exact physical endpoints.
- The deployed v51 policy replaces that detour with direct crossing
  `7C7ED19BEBABAED990BBDEB0D6797B98` in reverse for 5.992 meters. It removes
  `0DF729B8B0B635C78F334BC8FF43B4E9`,
  `B1B9E95A7D0847F7F0F53F09A8B5E845`, and
  `35D97F8A81D9458778FD5EAEB5EC382C`; these are the only physical segment-set
  differences from v50 on the 2025-10-14 run. Point classifications and traversal
  count remain `1387/154/223/35` and 24, while portions fall from 221 to 219 and
  unique segments from 151 to 149. The previously accepted park sidewalk/crossing
  connectors remain present. The reviewed October 16, 25, 27, 28, and 29 physical
  segment sets and counts are unchanged from v50.
- The 2025-10-10 Outdoor Run exposes raw-supported path endpoint truncation that
  is distinct from v51's detour replacement. Private footway
  `2F2347EAA6D4352C9A86E9AC96FAEF4D` is 58.118 meters long, but v51 emits only
  source fractions 0.050683-0.708674 in reverse for 38.095 meters. Approximately
  2.9 meters at its south end and 16.9 meters at its north end are omitted even
  though raw points pass within 0.41 and 1.40 meters of those endpoints. The north
  endpoint continues into private sidewalk `8050D4C2D84B8285E1BE7FB2A4E21D09`;
  the south endpoint continues into 9.745-meter private sidewalk
  `32BA01CBCA6957404CBBD7E65039E062`. Those sidewalks remain transition-only,
  while nearby private-road candidates take over before the footway endpoint, so
  candidate-to-candidate clipping does not emit the residual path tails. Private
  footway `9D14BE001810095994587F856A5BECED` similarly emits source fraction
  0-0.878986, omitting its final 2.284 meters despite a raw point within 2.98
  meters before the route continues into the private sidewalk network. Physical
  segment `7B64415515241C945215B69A96B0D595` is already fully covered in later
  forward and reverse spans, but its directly connected 4.253-meter private
  footway `35D9CD08EEE50786537DB5A73293940F` receives no sampled candidate and is
  shorter than the standalone 5-meter traversal threshold. A future completion
  rule should extend a positively traversed pedestrian path to a graph endpoint
  only when the omitted tail is short and continuously raw-supported, and may
  absorb one directly connected raw-supported eligible path below the sampling
  threshold without extending beyond its next sharp turn or graph node.
- The v53 endpoint-window policy completes all three reviewed 2025-10-10 cases. Private
  path `2F2347EAA6D4352C9A86E9AC96FAEF4D` expands from 38.095 meters to its
  complete 58.118 meters, `9D14BE001810095994587F856A5BECED` expands from
  16.589 meters to its complete 18.873 meters, and directly connected private
  path `35D9CD08EEE50786537DB5A73293940F` is added for its full 4.253 meters.
  Point classifications and traversal count remain `707/93/177/25` and 19;
  portions rise from 147 to 148 and unique segments from 85 to 86. Additional
  traversal-row extensions on the reviewed October 14 and 29 routes affect only
  overlapping forward/reverse evidence on physical paths whose union was already
  complete, so dissolved durable geometry is unchanged. The reviewed October 14,
  16, 25, 27, 28, and 29 physical segment sets remain unchanged from v51 except
  for the intended 4.253-meter `35D9CD08...` addition.
- The deployed v54 policy clamps endpoint-window evidence at temporal and spatial
  hard splits. Its complete evidence rows are identical to v53 for the reviewed
  October 10, 14, 16, 25, 27, 28, and 29 routes, preserving the intended endpoint
  completions while preventing a relocated continuation from proving them.
- Hiking workout types map to the same foot movement mode as walking and running,
  so path completion applies without a separate activity rule. On the 2025-10-07
  hike, v54 emitted only source fraction 0-0.919983 of curved footway
  `F44B7824BF60F20E7B83D3820D162884` (70.698 of 75.933 meters). The raw route
  passes within 1.28 meters of the physical endpoint, but its local eastward tail
  differs from the straight full-segment endpoint chord used by v54's heading
  check. The deployed v56 policy instead requires material endpoint approach or
  departure and a selected span of at least 5 meters; it emits the complete 75.933
  meters without allowing sub-threshold jitter spans to borrow endpoint evidence.
  The endpoint joins mapped north/south sidewalks, but South Blaney Avenue segment
  `B9C1DDC1202507373BA1FD36FF46A138` remains topologically separate by 5.629
  meters with no OSM segment across that gap, so coverage correctly stops at the
  physical footway endpoint. Point classifications, traversal count, portions,
  and unique-segment count remain `1569/76/96/1`, 18, 127, and 46.
- The 2025-10-08 Outdoor Run exposes three policy distinctions. At South Bernardo
  and El Camino Real, the raw route follows the northeast sidewalk and turns south,
  but HMM road attribution selects unnamed 8.341-meter service spur
  `6F18D7647EFA8CD6C04E7169B11E4C4E`. The direct road-centerline chain between
  `47C243900AFF0CBE77627A4DA83C17FD` and
  `7E2B8223A8E2B473426F6524C82B353D` is El Camino segments
  `DACEF372E16B38C96533C850F776BE03` (22.898 meters) and
  `F6E42B17C1FB816AB94E16734B3E4D45` (18.266 meters). At Dana and Moorpark, the
  selected route reaches the same endpoint through crossings and traffic island
  geometry followed by `BF30B924128FF8AF0A24DEFF63C2448B` and
  `F8E90447B6DD0A4702FE82C3F6077ADE`; the raw-supported direct route from
  `E898C025D6D223B10293B03DD3B511A8` instead uses crossings
  `8CA01B20E1EAF81444B2BC279DA64D9C` and
  `C8D594B629C4BBBA7C38B404CAA29FEF`, totaling 10.482 meters. v51 shortcut repair
  does not consider either replacement because its boundaries were intentionally
  restricted to path/accessory evidence rather than road-to-road transitions.
  At Whisman, `B45AD5C2B02C8B5E2E0FD639BD3CD7D3` and
  `6AB9CAD178CA7853A40AB4645A387ADF` share a direct 33.523-meter North Whisman
  connector, `974B881BC8C56FBDD4093549890BC8B7`. The raw route lies on the west
  sidewalk, closer to link `EB74A1F5537447CB65B932322FE785AB`, so nearest-road
  scoring selects the 46.503-meter C866/crossing/EB74 route instead. A future road
  continuity repair should compare exact-boundary alternatives using raw corridor,
  path-name continuity, and substantial distance savings instead of treating every
  nearby road/link centerline equally.
- On that same run, designated cycleway `B8ACE62E82B00BF063054A30579DBBE9`
  emits source fraction 0-0.36536 in reverse, or 9.051 of 24.177 meters. Its T-node
  is 5.95 meters from the nearest raw point whose reported horizontal accuracy is
  1.31 meters, narrowly outside v56's fixed 5-meter endpoint corridor. The node
  joins mapped sidewalks; Glenborough Drive remains a separate road centerline
  about 15.8 meters away. An accuracy-aware completion corridor could finish the
  cycleway without inventing a direct road connection.
- Georgetown and Cameron are explicit access-tagging cases. The 63.465-meter
  Georgetown chain between path `A684A036F179542C85F96B27009A6F32` and service
  road `DCEF198AC95A5524E3EC884AEAEF2E67` consists of named, private
  `service=driveway` segments and exceeds the 40-meter driveway connector cap.
  Cameron Drive is likewise represented by named private driveway segments
  `3E543FCB7F69CC456A38FF66E6755204` and
  `EDE2E276631BB05E7EB1D30DFE1BFE7E`, so it is excluded while adjacent unqualified
  private service roads remain eligible. A controlled policy may treat named
  private driveways as HOA neighborhood roads while keeping unnamed residential
  driveways excluded. `DCEF198A...` itself is not off limits: v56 covers source
  fraction 0-0.868 in reverse plus an overlapping forward span, or 30.163 unique
  meters of its 34.750-meter geometry. `8C83E68A19B8AF22CA7B2090B3349E0C`
  is Kasra Drive, not Cameron Drive, and is selected for its complete 28.200 meters.
- The deployed v57 policy addresses those distinctions. Accuracy-aware endpoint
  support completes designated cycleway `B8ACE62E82B00BF063054A30579DBBE9`
  from 9.051 to its full 24.177 meters. At Dana/Moorpark, direct crossings
  `8CA01B20E1EAF81444B2BC279DA64D9C` and
  `C8D594B629C4BBBA7C38B404CAA29FEF` replace
  `BF30B924128FF8AF0A24DEFF63C2448B` and
  `F8E90447B6DD0A4702FE82C3F6077ADE`. At Whisman, direct North Whisman segment
  `974B881BC8C56FBDD4093549890BC8B7` replaces
  `C8666F89C0DD3A2509BA76104DEE535F` and
  `EB74A1F5537447CB65B932322FE785AB`. At El Camino, road-continuity segments
  `DACEF372E16B38C96533C850F776BE03` and
  `F6E42B17C1FB816AB94E16734B3E4D45` replace unnamed service stub
  `6F18D7647EFA8CD6C04E7169B11E4C4E` and the wrong Bernardo approach.
- Named private HOA ways tagged `service=driveway` become normal road candidates
  in v57. The complete Georgetown chain `38705AA8...`, `A574A60E...`,
  `9627D709...`, and `6D160592...` now reaches the endpoint of `DCEF198A...`;
  Cameron segments `3E543FCB...` and `EDE2E276...` are traversed in both
  directions. `DCEF198A...` itself is no longer selected because the newly
  eligible named streets and adjacent paths provide better observation states,
  not because unqualified service roads became prohibited. On the full 2025-10-08
  run, counts move from `2122/369/368/76` to `2237/390/253/55`; traversals fall
  from 49 to 47, portions rise from 366 to 405, and unique segments rise from 307
  to 333 as additional directly traversed named HOA streets replace nearby proxies.
  The reviewed October 25, 27, 28, and 29 physical segment sets are unchanged.
  October 14 replaces one unnamed service loop with direct Seven Springs Lane;
  October 16 removes two unnamed service spurs while preserving both reviewed
  cul-de-sacs and all four driveway connectors. October 7 and 10 endpoint
  completions remain unchanged.
- The first v57 rerun also exposed one unsupported named-road branch at Georgetown:
  `38979C07882D38B5FA8F39F4E0ECEB0F` and
  `426AAF1C813CA1E2D01D2AD33E6D8702` led to terminal segment
  `19D9712B625021831BCE2BEE40F43BBC` and immediately reversed over the exact same
  three segments. The raw route passes the branch junction but never approaches
  its dead end; the nearest point is 19.54 meters away. v59 removes this exact
  multi-edge palindrome because its turnaround lacks raw support within 15 meters,
  reducing v57's 405 portions and 333 unique segments to 399 and 330 without
  changing point classifications or traversal count. The complete Georgetown
  through chain remains. Genuine 10/16 cul-de-sacs, Rainbow's reversal, the
  complete Stevens Creek Trail, and all reviewed October 25-29 outcomes retain
  their v57 evidence.
- The 2025-10-08 office/industrial park cluster after North Whisman segment
  `8B6EF01BD5E4F505C2B6051717836B90` is fragmented by intentional
  `service=parking_aisle` exclusion. `8B6EF...` is already complete at 19.304
  meters; the raw route turns east into parking geometry beginning with aisle
  `634A119BA59612E478C0FF75ED11E37C`. Unqualified service segment
  `9AB0F0CB4B43445B91AEE55D41CDB39A` is 74.492 meters, but raw points only touch
  its north endpoint within 1.65 meters before turning away, so the emitted 2.233
  meters are appropriate. Segment `19741218F48D9A670D36D383DCFF2F82`
  is complete at 9.330 meters. Its graph connection to
  `C7360977C220A35ADE4C714C225958A7` uses 84.170-meter parking aisle
  `21657D4DBA31794BA93A63A62DD0368E`; v59 excludes that edge, while `C736...`
  itself emits 54.424 of 56.853 meters. The omitted endpoint is 18.65 meters from
  the nearest raw point, so endpoint completion would overfill it. Segment
  `B61C45FA74F2854A77F1C239DD0CAC6F` is already complete at 23.905 meters in
  reverse; its 0.701-meter forward span is overlapping turnaround jitter. The raw
  route reaches its dead end within 1.80 meters and then continues east on excluded
  56.939-meter parking aisle `D75EEB21FF35B37B6183D1DB3B51E641`.
  Restoring visual continuity here would require an explicit parking-aisle policy,
  preferably limited to strongly raw-supported aisle runs between eligible roads;
  extending the adjacent eligible roads would be geometrically incorrect.
- The deployed v60 policy implements that raw-gated aisle evidence. On the
  2025-10-08 run it adds six parking aisles and no other physical segments:
  `10B06D215E803C38AF8A8FBB0B50FE85` (10.811 meters),
  `634A119BA59612E478C0FF75ED11E37C` (10.773 meters),
  `CB838472D24909CA902D80147633B33B` (36.727 meters),
  `21657D4DBA31794BA93A63A62DD0368E` (67.788 supported meters of 84.170),
  `D75EEB21FF35B37B6183D1DB3B51E641` (56.939 meters), and
  `2F904D071D3A24CF6BB3AF2269F0D24C` (112.319 meters). The first and last
  portions of `21657D4D...` remain clipped to raw-supported progress rather than
  claiming its unsupported tail; `C7360977...` remains at 54.424 of 56.853
  meters. `9AB0F0CB...`, `19741218...`, and `B61C45FA...` retain their v59
  lengths. Point classifications remain `2237/390/253/55`; traversals fall from
  47 to 45 by joining supported gaps, portions rise from 399 to 405, and unique
  segments rise from 330 to 336. The reviewed October 16 and 25-29 routes retain
  identical v59 physical segment sets and counts.
- Policy v61 addresses the 2025-10-06 Fremont/Bernardo left turn. v60 emitted
  `0A0202A1E82CD6918EA1B7E522DC36C0 -> A7BF828957A447EDEFFAF14C80172A8D
  -> 321170F48A12135EEA08E7059FCCC4A6`, even though `A7BF...` is the Bernardo
  segment south of the intersection and the raw route turns north. The decoded
  portions had a provable graph-node break after cancellation. v61 replaces that
  seven-meter spur with the raw-bounded 10.960-meter final Fremont subdivision
  `6D989EA158A1D2E1A7458177FAE8AFF5`, yielding connected normal travel into
  `321170F48A12135EEA08E7059FCCC4A6`. Point classifications, traversal count,
  portion count, and unique-segment count are unchanged; the physical set is an
  exact one-for-one replacement. The deployed v61 reruns of October 8, 16, 25,
  and 27-29 retain the v60 physical segment sets and diagnostic counts.
- Policy v62 trims the 2025-10-08 Whisman/East Dana turn artifact
  `13E6F16A38721A5AF8C3892516E8AEA9`. v61 emitted its full 12.610 meters in reverse
  and immediately forward between directly connected Whisman and Dana portions.
  The raw sidewalk trace approached the segment's outer endpoint to 0.39 meters
  while rounding the corner but supported only the forward road direction. v62
  requires directed raw support for both legs before retaining a single-edge turn
  out-and-back; endpoint proximity alone no longer preserves it. The deployed
  result keeps point classifications and 45 traversals unchanged, removes the two
  inverse portions (405 to 403), and reduces unique segments from 336 to 335. The
  reviewed October 6, 16, 25, and 27-29 routes retain their exact v61 physical
  segment sets and diagnostic counts.
- Policy v63 restores the 2025-10-09 Heatherstone/Stevens Creek Trail invariant
  previously established by v26. The overlapping Dale Avenue reversal
  `E2961AFE9D461BAA6A5A3ADC0F93010C` is replaced by trail segments
  `6B8AB48259B02CD59B33981F18A0B605`, `DD7CC5AB3E2CFFADF738D7ACC6A0E72E`,
  `8D7ED232C8D16B1EC24203AEC40917DB`, and the missing leading interval of bridge
  `1D3F989027F8E20AB43842E0E56114CA`. On Bonita Avenue, directly followed tagged
  driveways `3C8FF02959FD1AEFE855A88FBFB7F41A` and
  `C4A775AFB7700CF30CFDFED062DEE31E` are emitted out-and-back; the latter includes
  the 3.943-meter Bonita subdivision `3C87E537F307EF4FC10B18A002228190`.
  Untaken driveway `20E06EFB1B6F19FD02560D81E16CFE2A` remains absent. Classifications
  remain `1806/207/286/11`; traversals fall from 28 to 27, portions rise from 240
  to 246, and unique physical segments rise from 154 to 156. October 6, 8, 25,
  and 29 retain exact v62 physical sets and counts. Raw-supported dead-end
  driveways `26F2C63ECF897CDC1D3B55309CF6E4DC`,
  `1EF5ABB5144D3AE0ED825CB1E9CEDFD6`, and
  `58422ED472F09F86100906855E1557CA` add one physical segment and two portions
  respectively to October 16, 27, and 28 without changing classifications or
  traversal counts.
- Policy v64 removes two false parking-aisle loops from the 2025-10-10 Outdoor Run.
  `A53F6A80B29A368E31BB5568BCA776C6` shares both graph nodes with predominant
  Seven Springs segment `88E0D5E2BFAC316F2FFD8283CEA8875A`, while
  `23D1228F03B90575FA0A48BFEDAA04F1` shares both nodes with
  `2508C227612A269C4E62E96930D1D7D8`. The raw route follows the direct residential
  roads. The aisle loops appeared supported only because their endpoint chords are
  identical to those direct roads even though their source geometries bend away.
  Classifications remain `707/93/177/25` and traversals remain 19; portions fall
  from 150 to 148 and unique physical segments from 88 to 86. The parking-heavy
  October 8 route and the v63 October 9 path/driveway result retain exact physical
  sets and diagnostic counts.
- Policy v65 applies the same conservative shadow rule to the 2025-10-14 Outdoor
  Run, where parking aisle `A5BEE9B70CB8C27438D6CF8F7062ADFD` spans the same
  endpoints as the shorter two-segment Seven Springs path
  `5023D3BE7C048B302E15697D52C614EB -> 1E717523FAB78B4E563766CBF9DDA26C`.
  The raw route follows those predominant road segments, not the curved aisle loop.
  Classifications remain `1387/154/223/35` and traversals remain 24; portions fall
  from 220 to 219 and unique physical segments from 150 to 149. The v64 October 10
  result and the parking-heavy October 8 result retain exact physical sets and counts.
- Policy v66 corrects three 2025-09-11 Outdoor Run cases. Villa Nueva Court
  `A27D25EAF0FCD2F4DD8447540B0D753D` is emitted for its complete 99.017 meters
  outbound and inbound at the fully reached Villa Nueva Way junction. Unsupported
  7.512-meter South Drive reversal `E45DC4DD2BAA9BF35243A7E5DF43CF37`
  is removed from smooth Grant Road travel. At Grant/Phyllis/Martens, the outbound
  right turn inserts `F6EC93ABE5EEF6C92677D0072D41208F` and
  `76ED892BA3EFBF798F0F4FE8CF8E6203` between `FE75...` and `DBBD...`; the return
  turn replaces disconnected `76ED... -> AD40BA9D473FB5C69720BE47932D40FE`
  with `C3BB8A6CAB0CCF04888FAD19228056AC` between `3BD...` and `012F...`.
  Classifications remain `2059/274/158/25` and traversals remain 25; portions rise
  from 279 to 280 and unique physical segments from 199 to 200. The final physical
  delta is exactly those three additions and the two removals above. The same turn
  correction removes `76ED...` from the equivalent October 9 return route.
  Unsupported non-dead-end side-road prefixes `A3EAFBD70BD943B3BBF53DEE6627A671`
  and `FFC6E713AD755CDDDA18773B4D870DB2` are also removed from October 16 and 27;
  October 6, 8, 10, 14, 25, 28, and 29 retain their v65 physical segment sets.
- Policy v67 restores the complete 127.544-meter Arboleda Drive segment
  `930ED654C26CB6EE258E93D7E326E86F` on the 2025-09-10 Outdoor Run. After
  `8C04976C5B771307063F159A1608D473`, v66 selected a 154.8-meter cycleway/road
  traversal tail even though the raw route follows `930ED...` completely in reverse
  west along Arboleda and north around its bend to Cuesta segment
  `9C90DD8ADBCEC33439068C097EDD7F59`. v67 replaces that suffix with the one
  same-logical-road segment using ordered endpoint and raw path-distance support.
  Classifications remain `2575/351/89/10` and traversals remain 32; portions fall
  from 367 to 365 and unique physical segments from 221 to 219. The exact physical
  delta adds `930ED...` and removes incorrect suffix segments
  `67608678D8CECCABED6946DA69B8F928`, `EFD4DDDC21BA34323D20348AE1B9AB38`,
  and `5D195D75671EF45D78B27E6086619AF3`. The v66 September 11 result and all
  reviewed October 8-10, 14, 16, and 25-29 results retain exact physical sets and
  diagnostic counts.
- Policy v68 corrects the 2025-09-09 1.56-mile Outdoor Walk parallel-route choice.
  The raw route follows parking aisle `DDB1D3F4585617346FF3620B2DCB9602` in both
  directions, not Knickerbocker segment `E3315CC17A84869EACBAE3DA357C7CA1` and
  northern connectors `A843D76EC053A10A67F0C843EFA9AD32` and
  `5FCD4EC00F0E446AC3C1D9F65C60C62E`. v68 keeps graph continuity from
  `147F9795B949C775257A15187BF09A94` through alley segments
  `5EE0613583C75895509EC031F4DA5FFB`, `73B79F03792AE8A4563B5FB757CB554A`,
  and `CF49B475B5E0D88832497516A986E25E` to `DDB1...`, in reverse order on return.
  Classifications remain `362/55/109/9`, traversals remain 5, portions rise from
  58 to 59, and unique physical segments remain 27. The physical delta removes
  `E331...`, `A843...`, and `5FCD...` and adds only the three alley connectors;
  `DDB1...` was already present and gains its reverse traversal. The complete v67
  September 10, v66 September 11, and parking-heavy October 8 results retain exact
  physical sets and diagnostic counts.
- Policy v69 connects South Blaney segment `A353347AE109BBABB285E2C224B0CD85`
  to paved footway `DCB940D2B53F751E56844CF4EFD8C340` on the 2025-09-05 Outdoor
  Run through raw-supported parking aisles `D6B7917B875B80674CAF86C6466C1B32`
  (6.158 meters), `0683435155B6D918FF5C818DA55DF6FA` (8.804 meters), and
  `4609780B70777316D2B4437ED742A102` (52.171 meters). The first short connector
  uses directed travel past its mapped endpoint; the main aisle retains normal
  interval support. The two decoded traversals merge without inventing a road/path
  jump. Classifications remain `413/47/4/1`; traversals fall from 7 to 6, portions
  rise from 55 to 58, and unique physical segments from 44 to 47. The v68 September
  9 parallel-route correction and parking-heavy October 8 result retain exact
  physical sets and diagnostic counts.
- Policy v70 addresses September 1, 17, and 20 route boundaries. On September 1,
  selected driveway `A5713CED98286FD4093913350233A6EB` now continues through
  same-way segment `F3820DF4556FCA5ECBED6F7BBE14AB5B` for its remaining 69.538
  meters outbound and inbound. After `3F35421191D5740C8DD6B1FB97B6A512`,
  the unsupported perpendicular network prefix through
  `25BE904E93B8D5834837FB08C2EC9A65` is removed and supported travel resumes at
  `91DFF90FD7C749949B21A67B0AA29563`.
- On September 17, service-road tangent `81390AB8DAFB33A460F0501ED737082C`
  and its rectangular continuation are replaced by direct 39.337-meter Homestead
  segment `42BC22738F117728B597EFD021E89D9F` between `D6BA...` and `1CFE...`.
- On September 20, `F7B210B4BED1A41F8CA5DD0308AD27D9` is removed from a
  traversal head, the unsupported prefix beginning with
  `2D72C14EF29246F3E567C422FE89F57B` is removed until supported Morningside travel,
  and partial `3BD537EA9E9FED1619AF8F7F2E3C6850` is completed through
  `C3BB8A6CAB0CCF04888FAD19228056AC` into
  `012F54488236C6BD09CFAF7AD4AF2F0C`; `AD40BA9D473FB5C69720BE47932D40FE`
  is absent.
- The initial deployed v70 corpus run was rejected: although the three target routes
  improved, local traversal-prefix and head rules removed 26 physical segments from
  the accepted October 8 route and 10 unrelated physical segments from September 17.
  Policy v71 therefore restores v69 matcher behavior. The v70 diagnostic rows remain
  available as evidence, but v70 is not an accepted matcher policy.
- Policy v72 limits the September 20 turn correction to a within-traversal connected
  road alternative and adds ordered out-and-back chain completion. The chain rule is
  additive: it requires every outbound and reverse endpoint in observation order,
  rejects competing first branches, and permits excluded driveway continuation only
  along an already selected driveway's source way. The broad v70 boundary and head
  deletion rules remain disabled. Deployed corpus results are required before v72 is
  accepted.
- The deployed v72 audit was rejected. It corrected Thorsen Court and the
  `3BD... -> C3BB... -> 012F...` turn on September 20, but added 16 unrelated
  physical segments and removed one accepted segment from October 8; September 17
  exceeded the diagnostic timeout. It also could not extend `8D0E...` through
  `7380...` because the selected directions fell in separate 128-point windows.
  Policy v73 restores v69/v71 behavior while retaining v72 as diagnostic evidence.
  The deployed v73 October 8 control exactly matches v71 at
  `2237/390/253/55`, 45 traversals, 403 portions, and 335 unique physical
  segments, with zero physical-set differences.
- Policy v74 isolates the Thorsen Court evidence shape from rejected v72 additions:
  a two-or-three-segment ordinary-road branch on one logical path and source way,
  totaling 60-150 meters, taken out-and-back while the selected route continues
  through the same logical road at the junction. The deployed September 20 result
  adds only `28E590CC573B51CEAC2B115CE7D06F02` and
  `57D469D40B44EACB7E49FE535242C6C5`, each forward and reverse. Observation
  classifications remain `3401/451/217/15`, traversals remain 38, portions rise
  from 442 to 446, and unique physical segments rise from 295 to 297. September 9
  and October 8, 16, 25, 27, 28, and 29 retain exact v71 physical sets; the October
  8 counts remain `2237/390/253/55`, 45 traversals, 403 portions, and 335 unique
  segments.
- Policy v75 targets two remaining September 20 boundaries after the final driveway
  inference stage. Selected driveway `8D0E2B7FF34E8CA49A2A884921934247`
  now continues through same-source-way segment
  `7380024D482970918FBEE82140B1E2B3` for 110.718 meters outbound and
  reverse. The right turn completes `3BD537EA9E9FED1619AF8F7F2E3C6850`
  from 22.635 to 37.973 meters, adds 16.068-meter continuation
  `C3BB8A6CAB0CCF04888FAD19228056AC`, and enters
  `012F54488236C6BD09CFAF7AD4AF2F0C`; tangent stub
  `AD40BA9D473FB5C69720BE47932D40FE` is absent. Thorsen Court remains
  present in both directions. Classifications remain `3401/451/217/15`;
  traversals fall from 38 to 37, portions rise from 446 to 448, and unique
  physical segments from 297 to 298. September 9 and October 8, 16, 25, 27,
  28, and 29 retain exact v74 physical sets. October 8 remains
  `2237/390/253/55`, 45 traversals, 403 portions, and 335 unique segments.
- Policy v76 targets the September 17 Condor Way sequence where parking aisle
  `61A2E776F3E77744C9FA8217746A8363` departs from the same node as the raw-supported
  continuation `BAFEB430074A0E1A9BAA5C0E24094DFC` before the route reaches cycleway
  `DB96D9B909F504733D489DC7A096C725`. The parking aisle is removed only when the
  surrounding road portions are directly connected subdivisions of one logical path
  and source way and the selected parking run is at least 40 meters. The deployed
  result removes only `61A2E776F3E77744C9FA8217746A8363`; Condor Way segment
  `BAFEB430074A0E1A9BAA5C0E24094DFC` remains fully selected at 81.984 meters and
  cycleway `DB96D9B909F504733D489DC7A096C725` remains selected at 11.405 meters.
  Classifications remain `2874/337/338/24`, traversals remain 39, portions fall from
  358 to 357, and unique physical segments fall from 282 to 281. September 9 and 20
  and October 8, 16, 25, 27, 28, and 29 retain exact v75 physical sets.
- Policy v77 targets two remaining September 17 shapes. It replaces the service-road
  rectangle from `D6BA62DB8BE229A7794247EF93009125` to
  `1CFEFB42F1179AF80CCC100E1FCCEA1E` with raw-supported 39.337-meter Homestead
  segment `42BC22738F117728B597EFD021E89D9F`, and removes unsupported 20.555-meter
  service-road out-and-back `EA60CB5177518BA148A9511155D35802`. The latter requires
  directionally aligned raw support; its perpendicular raw crossing does not retain
  it. The deployed result adds only `42BC...` and removes `8139...`, `3A96...`,
  `B7B0...`, `B465...`, `B6A8...`, and `EA60...`. Classifications remain
  `2874/337/338/24`, traversals remain 39, portions fall from 357 to 351, and unique
  physical segments fall from 281 to 276. September 9 and 20 and October 8, 16, 25,
  27, 28, and 29 retain exact v76 physical sets.
- Policy v78 targets the June 10 perpendicular driveway
  `99638F38F5FEC8A43A66C53831F0C6A2`, selected out-and-back between connected Crist
  Drive subdivisions while the raw trace continues north/south on the opposite side
  of the road. Directional support is required to retain a single driveway reversal.
  The two-segment `766885EB8FD62A1E2B049C9B68ED1F19` ->
  `3EDB50374A43E210C0F61C2B40044D0A` chain remains unchanged. The same policy removes
  isolated, unsupported 13.859-meter Fremont segment
  `95A09FAE64313ACE4E93A2EFEA49F2AE`. The deployed June 10 result removes exactly
  `9963...` and `95A0...`; classifications remain `2011/241/84/13`, traversals remain
  21, portions fall from 286 to 283, and unique physical segments fall from 219 to
  217. September 9, 17, and 20 and October 8, 16, 25, 27, 28, and 29 retain exact
  v77 physical sets.
  Partial 23.142-meter Grant segment `7FE971836B1E2B6366E740784C851D32`
  remains unchanged; replacing that parking-area attribution is optional and requires
  separate corpus-safe evidence beyond the already selected `BDE6...`/`D2FD...`
  driveway out-and-back.
- Policy v79 targets the July 24 South Mary Avenue excursion between
  `01226D6079FAD556EA467C7DBB790763` and
  `3B4FB19C8D268D8DD7DCACF2DE1969D3`. The direct same-way continuation consists of
  `F65A276758537625ECA89BC7A05C69DB` and
  `95ED8DE6F818850ED58B066F6C30275D`, totaling 17.601 meters. It replaces the
  perpendicular service-road excursion containing `364785F1767E2A3A4DEA88229C868E28`
  and `F5A07E6B16B1EF2B3817B9282BC6FF7A`. The deployed result adds only the two
  South Mary subdivisions and removes the five service portions `3647...`, `4AC8...`,
  `87A8...`, `C6A6...`, and `F5A0...`. Classifications remain
  `1862/348/332/19`, traversals remain 27, portions fall from 311 to 308, and unique
  physical segments fall from 263 to 260. June 10, September 9, 17, and 20, and
  October 8, 16, 25, 27, 28, and 29 retain exact v78 physical sets.
- Policy v80 targets the April 28 Victor Way to Castro Street right turn. The selected
  Victor portion currently stops about 7 meters before the shared graph node and the
  selected Castro portion begins about 8 meters after it. v80 completes both existing
  directed portions to their exact common node only when each already contributes at
  least 20 meters and raw observations approach that node. Because the pair straddles
  two 128-point evaluator windows, one bounded centered rematch may replace only the
  same two physical IDs and directions with containing source intervals totaling no
  more than 30 added meters. The deployed result extends Victor forward from 72.019
  to 79.726 meters and Castro reverse from 75.144 to its full 83.349 meters, meeting
  at their exact common node. Classifications remain `1693/246/131/18`, traversals
  remain 30, and portion/unique-segment counts remain `277/191`. June 10, July 24,
  September 9, 17, and 20, and October 8, 16, 25, 27, 28, and 29 retain exact v79
  evidence tuples, including direction, source intervals, and multiplicity.
- Policy v81 targets three July 18 continuity gaps: Maranta Avenue
  `A4A084228F985D21D03C9EFD4BF2EA9A` into Knickerbocker
  `726BD12806B244EF6715F499ECB3BC57`, Princeton Drive
  `79A8B0747B120D4331D4E2A4AE588438` into Rubis Drive
  `49DDCCBB054488F4A1C559D2C54039C3`, and the missing raw-supported Sunnymount route
  between Princeton and Dawn Drive `F742D6132F59045C5AE3EEF01849AFC9`. Deployed
  v81 completes Maranta forward from 79.975 to 95.513 meters and Knickerbocker
  forward from 164.293 to 180.195 meters at their common node; it also completes
  Princeton forward from 118.910 to 129.782 meters and Rubis reverse from 208.435 to
  228.985 meters at their common node. The long gap becomes
  `F742... -> BCB451... reverse -> BCB451... forward -> 457350... -> 6C2C98... ->
  79A8...`, retaining the raw-supported 38.176-meter Dawn dead-end out-and-back and
  adding full 289.853-meter and 86.925-meter Sunnymount portions.
  Classifications remain `1833/171/150/32`, traversals remain 29, portions rise from
  214 to 217, and unique physical segments rise from 132 to 134. April 28, June 10,
  July 24, September 9, 17, and 20, and October 8, 16, 25, 27, 28, and 29 retain exact
  v80 evidence tuples, including direction, source intervals, and multiplicity.
- Policy v82 removes the July 18 Georgetown Court reversal
  `9543CAE049808B0B2E5695C2F2052E82`. The 25.811-meter court runs perpendicular to
  the north/south raw route and the trace never reaches its terminal endpoint. The
  ordinary-road dead-end rule applies only to full physical-segment reversals and
  retains endpoint-supported residential turnarounds such as the accepted September
  20 control. The deployed result removes only the forward/reverse `9543...` portions;
  classifications remain `1833/171/150/32`, traversals remain 29, portions fall from
  217 to 215, and unique physical segments fall from 134 to 133. The reviewed v81
  corpus remains unchanged, including the formerly sensitive June 10 and October 8
  partial reversals.
- On the 2025-10-25 walk, v35 emitted a 21.9-meter two-portion Vallco Parkway
  excursion between two Wolfe Road portions of the same logical path. Policy v36
  permits the existing same-logical-path repair to replace a contiguous middle run
  totaling at most 30 meters using only the surrounding path's graph.
- The deployed v35 rerun removes West Dana physical segment
  `18768EF68032302A20D1CB87EB373F18` and emits the endpoint-connected cycleway chain
  `43D898E60A784D54ADDF308DE4802B10 -> 15ACD006A7F4C7DB49F66F18200FB536
  -> E5BBF676F0D7AAE6DB2E339B30BCC322`, all from OSM source way 195656378. The 15
  observations from the removed road traversal become rejected, leaving unmatched
  count unchanged. October 27 retains the complete 284-meter Stevens Creek Trail
  repair, October 29 retains zero reviewed Homestead/Mary artifacts, and October 28
  retains the Rainbow segment unchanged; its two removed unique segments were
  pedestrian crossing artifacts at the reviewed Americana/El Camino transition.
- The deployed v31 rerun restores a single complete 284-meter traversal of
  `1D3F989027F8E20AB43842E0E56114CA`, connected from `6B8AB48259B02CD59B33981F18A0B605`
  through both short trail connectors and onward to the next Stevens Creek Trail
  segment; the Dale stub remains absent. October 27 retains 2,103 matched, 297
  ambiguous, 113 unmatched, and 4 rejected observations. October 28/29 totals are
  unchanged from v27, the Rainbow segment remains 8.4 meters, and all four reviewed
  Homestead/South Mary artifacts remain absent.
- The deployed v26 rerun emits no evidence for Dale segment
  `E2961AFE9D461BAA6A5A3ADC0F93010C`. It connects Heatherstone through trail
  segments `6B8AB48259B02CD59B33981F18A0B605`, the two short trail connectors, and
  `1D3F989027F8E20AB43842E0E56114CA`, then covers the full 284-meter physical trail
  segment into the continuing trail. Observation counts remain unchanged while one
  traversal, four portions, and one unique segment are removed. October 28 remains
  byte-for-byte equivalent in counts and retains the reviewed Rainbow service
  segment at 8.4 meters; October 29 retains its accepted total while shifting three
  observations from ambiguous to matched.
- The deployed v20 rerun removes Grant segments `EB13E19A239D4E523FF8F40867DC3511`
  and `69AA7FA95C8F8E3C7C3FA7733D190165`, selecting the connected direct branch
  `B98FAA453A74A450013C0830E83EC527 -> 0E7FE5B55B5384AD8B31C4D2685AB66D
  -> 3A5DC362FB079F525AB522505AD4BDB9`. It completes the 47.9-meter Grant merge,
  removes both Belleville/Fremont nested-excursion IDs, and fills the Fremont gap
  from `C5DD429B7AD3DB7C674E98E04C4E992B` through the shared endpoint into
  `0A0202A1E82CD6918EA1B7E522DC36C0`. The run yielded 2,100 matched, 300 ambiguous,
  113 unmatched, and 4 rejected observations. October 28 and 29 regressions shift
  substantial ambiguity to matched status while retaining comparable accepted totals.
- The corpus is intentionally small and synthetic. It does not tune thresholds
  across path classes, sampling devices, accuracy bands, switchbacks, tunnels,
  dense urban grids, or representative Health Auto Export traces.
- Ambiguity is reported from near-equal dynamic-programming state costs. It is
  not a calibrated posterior probability.
- Runtime uses small synthetic graphs plus one public-coordinate NorCal smoke
  evaluation. Candidate cardinality, projection accuracy, and bounded routing
  cost across dense downtown, trail, switchback, tunnel, and poor-accuracy cases
  still require representative measurement.
- Map-data/rules version persistence, idempotent rematching, segment evidence,
  logical-path counting, and rendering remain intentionally unimplemented.
