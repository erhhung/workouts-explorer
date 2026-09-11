# ADR 0009: Coverage Matching Rules

## Status

Proposed - requires representative route experiments during Milestone 7.

## Context

The MVP matches each workout to nearby public path segments using the ordered
route sequence rather than choosing an independent nearest segment for every
point. It must avoid false coverage on parallel roads, intersection cross streets,
and poor-accuracy points while retaining the unmodified raw route. Decoded
positive-length segment traversals provide visited geometry; counting occurs at
the containing locality-scoped logical path. Each workout may contribute at most
once to one logical path, using its earliest accepted member-segment timestamp.

The correct distance threshold and quality rules depend on actual Health Auto
Export routes, route-point accuracy, path density, and the segment model selected
by ADR 0008. Choosing constants before those inputs exist would turn guesses into
persistent coverage data.

## Proposed Decision Process

Build a curated, privacy-safe evaluation set containing:

- clear on-path routes across roads, trails, and cycleways;
- parallel roads, frontage roads, divided roads, bridges, and crossings;
- straight intersection crossings, genuine turns, isolated cross-street point
  excursions, and stationary intersection jitter;
- switchbacks and nearby trail branches;
- sparse samples and GPS drift;
- indoor or route-poor workouts;
- points with good, poor, missing, and suspicious accuracy; and
- deliberate no-match cases outside any reasonable threshold.

Label expected segment matches and no-matches independently of the algorithm.
Evaluate candidate distance thresholds and quality rules with false-positive,
false-negative, unmatched, and ambiguous-match rates. Record results by path
class and quality band rather than selecting one aggregate score only.

## Proposed Rule Constraints

The accepted matcher must:

- query all eligible public path classes within a bounded candidate distance;
- retain multiple nearby candidates at intersections rather than reducing each
  point to its nearest segment before sequence decoding;
- use a point's retained quality information without altering the raw route;
- reject implausible candidates rather than always selecting the nearest path;
- decode connected transitions across the ordered route and use observations
  after an intersection to distinguish continuation from a genuine turn;
- permit same-road continuity as weak evidence, never as a hard rule that can
  suppress a motion-supported turn;
- split matching at unsupported temporal, spatial, or network gaps rather than
  routing through unobserved streets;
- attribute and render only positive-length segment portions traversed by decoded
  transitions, not proximity-only candidates or every member segment of a
  logical path;
- use deterministic tie-breaking;
- retain the matching-rule and map-data versions for diagnostics;
- persist one workout/physical-segment match after dissolving overlapping or
  contiguous accepted traversal spans on that segment, preserving genuinely
  disjoint spans as separate components of one `MultiLineString` and never
  filling an untraversed gap;
- upsert a unique workout/logical-path pair with the earliest accepted
  member-segment timestamp;
- permit safe rematching after rule or OSM changes; and
- expose aggregate quality metrics without coordinates or private geometry.

A global maximum distance may be combined with an accuracy-derived candidate
radius and stricter quality- or class-sensitive rules if the evaluation
demonstrates a clear improvement. Every operator-configurable threshold must
represent a legitimate deployment policy; correct-by-construction matching rules
remain versioned code.

## Proposed Sequence Model

Use an offline hidden Markov model with Viterbi decoding over directed physical
segment candidates. Each candidate records the segment, projected position,
direction, lateral distance, local tangent, and logical-path/locality metadata.
Logical paths are attribution metadata, not matcher states, so equally named but
disconnected roads cannot become connected through naming alone.

Emission evidence includes point-to-segment distance normalized by effective
horizontal uncertainty. Reliable course or adjacent-point bearing may contribute
local-tangent evidence, but heading is suppressed for stationary or low-motion
points and when course accuracy is poor. Missing telemetry remains unknown rather
than becoming zero.

Transition evidence compares connected network distance with observed
displacement and elapsed time, respects direction and grade-separated topology,
and rejects implausible detours, reversals, and speed. A small continuity prior
may favor the prior segment or logical path when evidence is otherwise close.
Several post-intersection points must be able to revise the apparent match at the
intersection, which is why greedy previous-road selection is insufficient.

For every accepted transition, clip the first and last segments to their projected
positions and retain each positively traversed segment span. Point projections
remain diagnostics; they do not independently create coverage. Before durable
persistence, group spans by workout and physical segment, discard traversal
direction as a storage identity, and dissolve overlapping or contiguous intervals.
Store the result as one `MultiLineString` even when it has one component. Repeated
passes and GPS-jitter reversals therefore do not create duplicate match rows or
inflate covered length. Disjoint intervals remain separate components, and the
system must not bridge their untraversed gap. The coverage map unions these stored
geometries across selected workouts while joining logical-path counts and all-time
dates. Therefore two disjoint visited spans of `El Camino Real, Sunnyvale` render
as two disjoint geometries with the same popup statistics, and the unvisited middle
does not render.

Dissolution operates on normalized intervals along the canonical physical segment,
then clips the canonical line from those merged intervals. This avoids treating
opposite directions as different geometry or using a proximity-based spatial union
that could close a real gap. Covered length is computed from the resulting unique
geometry, not by summing the pre-dissolution traversal lengths.

## Alternatives To Evaluate

### One fixed nearest-segment distance

This is simple and remains the baseline. It may be sufficient if representative
routes show acceptable ambiguity.

### Accuracy-adjusted candidate distance

Accuracy can inform acceptance, but allowing poor accuracy to expand the search
may increase false positives. The spike must test both rejection and expansion
strategies.

### Greedy heading and continuity checks

Course, adjacent points, segment direction, and previous-road preference may
disambiguate some intersections. Greedy decisions cannot use later observations
to correct an isolated cross-street jump and can suppress genuine turns, so this
remains a measured baseline rather than the preferred design.

### Sequence-aware HMM/Viterbi matching

Multiple candidates plus connected transition scoring directly model the
intersection concern while allowing genuine turns. It is the preferred design if
the curated corpus confirms acceptable performance and bounded network-search
cost.

### Dedicated map-matching engine

ADR 0001 rejects this for the MVP. It is reconsidered only if the nearest-path
approach cannot meet the curated acceptance threshold.

## Acceptance Evidence

An experimental `coverage-experimental-v1` decoder, bounded canonical OSM
adapter, synthetic corpus, and NorCal public-coordinate smoke evaluation are
implemented and recorded in `docs/coverage-matcher-evaluation.md`. The synthetic
corpus currently reports 1.000 expected-segment precision and recall, zero false
cross-street attributions, and zero missed expected turns. At NorCal scale, the
provenance-preserving 50-meter candidate query measured 255.753 ms cold and
7.892 ms warm; endpoint expansion uses the dedicated start/end graph-node indexes.

This evidence does not yet accept the ADR. The current bridge deliberately treats
physical edges as bidirectional, retains meter intervals rather than clipped
source polylines, and has not been labeled across representative private route
classes and quality bands. Access/direction policy, curved-geometry clipping,
representative error rates, and rematch behavior remain acceptance blockers.

Implementation decisions selected for the next evaluation round are:

- use testuser xdev routes for tuning while retaining and reporting only aggregate
  metrics and public segment identities, never route coordinates;
- apply workout-aware foot and bicycle access/direction policy, with a conservative
  shared-public fallback for other workout types;
- default the minimum persisted traversed portion to 5 meters, expose it as a
  validated deployment setting, and retain its effective value in matcher
  diagnostics/version provenance;
- permit partial Coverage when at least one checked workout is processed, identify
  omitted pending/unavailable workouts, and poll readiness every 10 seconds while
  Map remains open and at least one selected workout is pending;
  and
- scope named paths outside authoritative municipalities by provider region rather
  than one global outside identity.

Foot and bicycle coverage use street-level rather than sidewalk-level attribution. For each
observation, when an eligible drivable road centerline is within 5 meters, nearby
pedestrian-only candidates (footways, sidewalks, pedestrian ways, steps, corridors,
cycleways, and non-motor paths) are suppressed. Those paths remain eligible
when no such road is within 5 meters, preserving park and trail coverage. Raw route
rendering remains unchanged and continues to show the traveled side of the street.
Candidate-level nearby-road preference was introduced in v2. Labeled v2 evidence
showed that tagged sidewalks and crossings could still re-enter decoded traversals
as shortest-path transition edges: all 23 unique Unexpected segments were
`footway=sidewalk` or `footway=crossing`, including 16 within 5 meters of a road.
`coverage-path-policy-experimental-v3` therefore excludes explicitly tagged road
accessories from both candidates and transition graphs for walking/running and
cycling. Untagged park/trail footways remain eligible. v1/v2 diagnostics that choose
sidewalks or crossings should be labeled Unexpected and the overall route Incorrect
when the substitution is material.

Review of a sidewalk-heavy v3 route showed a second effect: after removing road
accessories, 57% of sampled observations were unmatched because good GPS accuracy
reduced the normal candidate radius to about 7.5 meters, while adjacent OSM road
centerlines were commonly 7.1-10.5 meters from sidewalk traces. Policy v4 therefore
uses a 12-meter minimum candidate search radius for foot and bicycle observations.
The general path-to-road suppression threshold remains 5 meters, so untagged park
or trail paths 5-12 meters from a street remain eligible and normally win by
distance; tagged sidewalks/crossings remain excluded entirely.

The first v4 diagnostic retained v3's 738 unmatched observations because the OSM
adapter truncated to the closest 16 physical candidates before policy filtering;
dense sidewalk/crossing pieces consumed those slots and the road centerline was
never returned. Policy v5 overfetches up to 64 raw candidates, applies movement
policy, then retains the closest 16 eligible states for HMM decoding. Decoder and
graph bounds are unchanged.

The first v5 run remained unchanged because the sidewalk-heavy route had substantial
spans 12-20 meters from the nearest OSM road centerline. Policy v6 raises the
supplemental foot/bicycle road search floor to 20 meters and overfetches 128 raw
candidates before retaining 16 eligible states. Generic park/trail paths are still
not suppressed unless a road is within the original 5-meter suppression threshold.

The v6 trace remained unchanged because Runningwood Circle and Cuernavaca Circulo
are tagged `access=private`; foot/bicycle policy fetched their centerlines and then
rejected them. Policy v7 treats non-motorway drivable street centerlines as valid
coverage attribution targets for foot/bicycle traces even when access or the
mode-specific tag is private/no. Private pedestrian paths and private service
driveways/parking aisles remain ineligible.

Review of the 2025-10-29 Outdoor Run exposed that the supplemental 20-meter query
radius was undone by the decoder's accuracy-derived 7.5-meter acceptance radius.
Policy v8 retains raw point-to-centerline distance but computes a separate lateral
road-attribution offset for foot and bicycle road candidates. Explicit `width=*`
is preferred; otherwise carriageway width is estimated from `lanes=*` using a
configurable 3-meter motor-lane default and tagged bicycle and parking lanes. A
configurable 3-meter road-edge-to-sidewalk-center setback is then added. Only the
remaining distance beyond that envelope contributes to candidate acceptance and
emission cost, and the original 20-meter centerline query remains a hard cap.
Non-road and shared-public candidates retain unadjusted geometric distance.

Review of the 2025-10-28 Outdoor Walk identified two remaining failure modes.
First, a watch trace reported 1.3-3 meter accuracy while drifting 15-40 meters
from Heatherstone Way and Dale Avenue, but remained close and parallel to mapped
`footway=sidewalk` geometry. Policy v9 searches a bounded 40-meter corridor and
uses only a nearby, parallel, explicitly tagged sidewalk as supporting emission
evidence for an eligible road; the sidewalk never becomes a Coverage candidate.
Second, removing all sidewalk/crossing topology forced a divided-road transition
from The Americana to Sylvan Avenue to detour along El Camino Real. v9 restores
eligible road accessories as transition-only graph edges. Their length participates
in network plausibility, but their geometry is never emitted as Coverage evidence.
Policy v10 narrows v9's fallback after live comparison: road candidates beyond 20
meters require explicit parallel sidewalk support, while direction-supported
misreported-accuracy tolerance remains bounded to 5 meters inside the 20-meter
road corridor. A logical-path switch prior and same-segment U-turn penalty suppress
short cross-street excursions without preventing one supported genuine turn.
Policy v12 retains exact adjacent forward/reverse traversal of the same physical interval
in network-distance validation but canceled from emitted Coverage as a zero-net
topology backtrack. Separate sampled out-and-back transitions are not canceled.
Policy v13 first coalesces contiguous same-direction portions, allowing the same
zero-net rule to remove a split 17.8-meter service-road excursion. Sidewalk support
is limited to carriageways whose inferred road-edge envelope is consistent with the
sidewalk separation, preventing one side of a divided road from supporting the
other. Directional tolerance uses a five-observation window so both sides of a
sharp turn can remain candidates while the instantaneous heading changes.
Policy v14 adds accepted road-envelope offsets, capped at 15 meters per endpoint,
to the hard network-transition gate. The HMM transition cost remains based on the
full centerline route discrepancy, so GPS corner cutting can remain connected
without becoming cost-free or permitting unbounded topology jumps.
Policy v15 uses a nearby tagged pedestrian crossing as directional evidence. Road
candidates parallel to the crossing are promoted as street continuations, while
transverse road candidates are suppressed for observations on that crossing. The
crossing remains transition-only and never becomes Coverage geometry.
Policy v16 limits transverse suppression to one-way primary/secondary carriageways,
retaining the divided-road correction without fragmenting ordinary street-crossing
traversals. Crossing-aligned continuation promotion remains road-class agnostic.
Policy v17 closes an eligibility fallthrough that admitted `service=parking_aisle`,
driveways, drive-throughs, and emergency-access ways as ordinary foot/bicycle
candidates. It also raises direction-supported drift tolerance from 5 to 7 meters,
still bounded to the 20-meter road corridor, to preserve a predominant road when a
short parallel accessory lies directly beneath an overconfident GPS trace.
Policy v18 bridges adjacent decoded traversals only when they resume the exact same
directed physical segment within 15 meters and four sampled observations. Temporal
and spatial splits prohibit bridging. This preserves predominant road continuity
across a short no-candidate gap without inferring a connection between roads.
Policy v19 adds a low-weight raw-distance term inside the road-edge emission plateau
to distinguish competing carriageways on the same logical road. It also permits a
bounded 30-meter bridge across connected physical segments of the same logical path
within eight observations, and cancels exact nested inverse excursions across
adjacent traversal boundaries. These rules preserve road continuity without joining
different logical roads or crossing temporal/spatial splits.
Policy v20 increases the secondary raw-distance weight to 0.01 per squared meter
after the lower weight did not disambiguate the Grant fork. It also permits up to
16 sampled observations for the unchanged 30-meter same-logical-path graph bridge;
distance and hard-split bounds remain authoritative.
Policy v21 exempts named pedestrian paths and bridges from the generic 5-meter
road-nearby suppression rule. Tagged sidewalks/crossings remain non-attributable,
and unnamed generic paths remain suppressed. This preserves independent trail
continuity where a named bicycle/foot bridge meets a road.
Policy v22 raises the logical-path switch cost from 0.75 to 2 only for named or
bridge independent pedestrian paths. A sustained road-to-trail turn pays once,
while a one-observation Trail-to-road-to-Trail excursion pays twice. Other path
groups retain the existing switch cost.
Policy v23 may replace a middle traversal of at most 10 meters when the preceding
and following traversals share one logical path, its graph connector is at most 250
meters, and that connector is no more than 1.5 times accumulated raw distance plus
20 meters. At most 64 observations may be spanned, and temporal/spatial splits
prohibit replacement. This recovers sustained trail travel around a short road stub
without relying on proximity alone.
Policy v24 applies the same bounded A-to-B-to-A replacement inside one decoded
traversal. A middle portion of at most 10 meters may be replaced only by a graph
connector of at most 250 meters wholly on the surrounding logical path. This covers
cases where transition-only topology kept the decoded traversal connected even
though emitted evidence contained a short road stub and disconnected trail restart.
Policy v25 computes all same-logical-path repair connectors on a graph filtered to
that logical path. This prevents a shorter road/sidewalk route from defeating a
candidate repair merely because the resulting connector correctly fails the
same-path validation.
Policy v26 handles the traversal-boundary form `A -> short B | A`: the short suffix
may be replaced and the traversals merged only when the resumed path is identical
and its constrained connector satisfies the same 10/250-meter limits. This covers a
network split immediately after the short excursion without affecting `A -> B | C`.
Policy v27 permits a direction change at the projected position on one physical
segment rather than requiring travel to its endpoint before entering the reverse
directed edge. The existing U-turn cost remains. This models pedestrian turnarounds
and prevents intersection loops introduced solely to reverse direction.
Policy v28 extends same-logical-path boundary repair to connectors up to 250 meters
when graph and accumulated raw distance agree bidirectionally: graph distance must
fall between `raw/1.5 - 20` and `raw*1.5 + 20` meters. At most 64 observations may
be bridged, and temporal/spatial splits remain absolute blockers.
Policy v29 fills a same-logical-path gap between adjacent portions inside one
already accepted traversal using the path-filtered graph, capped at 250 meters.
Because hard splits cannot exist inside a decoded traversal, this covers evidence
gaps hidden by transition-only topology without relaxing cross-traversal inference.
Policy v30 runs intra-traversal same-path gap filling after all short-excursion
replacement passes. A repair that removes a middle road stub can therefore expose
and immediately fill the surrounding path gap under the existing v29 constraints.
Policy v31 carries independent named/bridge path classification into the graph. For
adjacent traversal boundaries on such a path, the 250-meter/64-observation and hard
split bounds remain, but raw-distance agreement is not required because self-looping
path geometry can concentrate accumulated travel before the split. Roads and unnamed
paths retain bidirectional raw-distance agreement.
Policy v32 adds a coarse HMM continuity class for foot/bicycle matching. Eligible
cycleways, paths, and footways share `path`; drivable streets use `road`. A class
change costs 2, so a sustained trail-to-road turn pays once while a short
path-to-road-to-path excursion pays twice. Logical-path scoring remains independent.
Policy v33 carries that class onto graph edges and evidence. Within one accepted
traversal, a middle run of another class totaling at most 75 meters may be replaced
by a connector of at most 250 meters entirely within the surrounding class. This
preserves path continuity across logical-path splits without widening path distance.
Policy v34 removes the class-level evidence rewrite after it altered unrelated
segments without removing the target road. It instead raises the HMM path/road class
switch cost from 2 to 8. A sustained class transition can amortize the one-time cost,
while a short path-to-road-to-path excursion pays 16.
Policy v35 restricts class-level evidence repair to three traversal boundaries:
`path | short uniform non-path | path`. The middle traversal is capped at 75 meters,
the path-only connector at 500 meters and 64 observations, graph/raw distance must
agree bidirectionally, and hard splits prohibit replacement. No in-traversal class
rewrite is performed.
Policy v36 generalizes the same-logical-path in-traversal repair from one 10-meter
middle portion to a contiguous middle run totaling at most 30 meters. The surrounding
logical path must be identical and the replacement remains constrained to that path
with the existing 250-meter cap.
Policy v37 raises the matching cross-traversal same-logical-path excursion cap from
10 to 30 meters. Path-only routing, 250-meter/64-observation bounds, bidirectional
raw-distance agreement, and hard-split blockers remain unchanged.
Policy v38 generalizes the boundary-suffix shape from one middle portion to a
contiguous suffix totaling at most 30 meters. The following traversal must resume
the same logical path, and replacement remains constrained to that path under the
existing 250-meter and hard-split bounds.
Policy v39 preserves inverse portions when merging a repaired boundary. It coalesces
only contiguous same-direction portions, preventing a legitimate out-and-back on
the surrounding logical path from being erased after an unrelated suffix repair.
Policy v40 may emit a transition-only connector already selected by an accepted
foot or bicycle HMM transition when the connector is bounded by attributable spans,
consists entirely of accessible `footway=crossing` or `footway=traffic_island`
edges, and totals at most 10 meters. The selected transition remains subject to the
existing topology, direction, distance, speed, and raw-route plausibility checks;
v40 performs no second shortest-path inference. Sidewalks, generic links, denied
accessories, unbounded connectors, and over-limit connector runs remain
non-attributable and unrendered.
Policy v41 adds a configurable 3-meter attribution allowance for foot and bicycle
road candidates projected within 1 meter of a degree-one drivable-road endpoint.
The allowance models the wider turning bulb and its surrounding sidewalk without
globally widening ordinary road attribution. It is applied only after graph
expansion establishes road degree; candidates on pedestrian paths, in the middle
of a segment, or at road nodes with two or more incident drivable segments retain
their existing offsets.
Policy v42 permits an accessible `service=driveway` run to become attributable
continuity evidence only when the HMM-selected transition directly bridges `path`
and `road` evidence, the complete run is at most 40 meters, and every directed
driveway edge is within 30 degrees of the raw transition heading. Driveways remain
ineligible as observation candidates. Road-to-road and path-to-path shortcuts,
unbounded or mixed transition-only runs, stationary transitions, over-limit runs,
misaligned edges, and driveways with denied mode/access tags remain excluded.
Policy v43 retains v42's heading requirement for substantive driveway spans but
allows one perpendicular road-adjacent driveway edge of at most 10 meters. This
models the short lateral join from a sidewalk or path position to the attributed
road centerline without allowing a longer misaligned driveway to become coverage.
Policy v44 evaluates substantive driveway support over the 16 sampled observations
on either side of the selected transition. It requires raw points within 10 meters
of both the first and last fifth of each directed driveway and at least 60 percent
directed progress aligned within 30 degrees. This recognizes a driveway traversed
before the candidate state switches at a nearby turn without accepting a connector
based only on distant endpoints.
Policy v45 applies the same raw-polyline and geometry safeguards when adjacent
decoded path and road traversals were separated by a non-hard network or
no-candidate split. It inserts only the driveway route selected between their exact
directed boundaries, requires the gap to span at most 32 sampled observations and
40 meters of network distance, and refuses temporal/spatial hard splits. This
preserves driveways traversed over several observations without relaxing HMM
transition-distance limits.
Policy v46 allows that post-decode repair to reverse direction at the same physical
road or path boundary before entering the driveway route. It evaluates both
directed representations of the exact boundary position and chooses the shortest
qualifying connector, while normal HMM transitions retain their original strict
direction semantics.
Policy v47 treats `access=private` on pedestrian paths as permission-limited rather
than prohibited for foot and bicycle matching. Such paths remain subject to normal
distance, heading, topology, speed, and continuity scoring. Explicit `access=no`,
`foot=no`, or `bicycle=no` remains a hard exclusion. Private sidewalks and
crossings may support graph transitions but retain their road-accessory candidate
and emission restrictions.
Policy v48 emits an HMM-selected transition-only run of sidewalks, crossings, and
traffic islands when it is bounded by attributable evidence, at least one boundary
is a pedestrian path, and the run totals at most 25 meters. Every accessory span
longer than 10 meters requires directed raw-polyline support; at least one span in
the run must be raw-supported. Accessories remain unavailable as standalone
candidates, generic links remain excluded, and the existing crossing-only 10-meter
rule remains unchanged.
Policy v49 preserves the HMM route when an accessory run lacks emission evidence,
stripping only that run instead of rejecting the transition. It also applies the
same 25-meter rule across adjacent decoded traversals separated by a non-hard gap,
using the exact boundary route and allowing only boundary-segment tails plus
accessories. The raw-support threshold treats spans through 12 meters as short
companions, while every longer span requires direct support and every emitted run
requires at least one supported span.
Policy v50 supplies the candidate continuity classes when an HMM state lies exactly
at an accessory-run boundary. A zero-length boundary portion therefore still
qualifies as attributable path/road evidence, fixing endpoint-only suppression
without relaxing accessory type, length, or raw-support requirements.
Policy v51 replaces a decoded road excursion with a direct accessory-only route
between the same exact selected boundaries when the excursion is at most 50 meters,
the replacement is at most 25 meters and less than half as long, at least 10 meters
are saved, and the raw polyline continuously follows the replacement corridor.
This repair does not make accessories observation candidates or alter point
classifications, and it preserves the selected excursion whenever the shorter route
lacks continuous directed raw support.
Policy v52 completes an already accepted, non-accessory pedestrian path to either
physical endpoint when the omitted tail is at most 20 meters and raw motion reaches
within 5 meters of that endpoint with parallel movement. It may additionally
absorb one directly connected, raw-supported eligible path of at most 5 meters,
allowing a sub-sampling-length connector to contribute to the accepted traversal.
Completion stops at the next graph node, never follows a long adjacent sidewalk,
and leaves over-limit or unsupported tails clipped.
Policy v53 evaluates endpoint and short-neighbor support over 16 sampled
observations before and after the decoded traversal boundary. Geometry caps remain
unchanged; the wider temporal context only allows raw motion immediately assigned
to a neighboring road state to prove that the selected path reached its endpoint.
Policy v54 clamps that context at the nearest temporal or spatial hard split, so
endpoint evidence from a paused, relocated, or resumed route cannot complete a
path on the other side of the discontinuity.
Policy v55 validates endpoint completion using material approach to or departure
from the endpoint within the 5-meter corridor instead of comparing raw heading to
the straight chord between a path's endpoints. This supports curved path tails
whose local tangent differs from the full-segment chord while still requiring at
least 1 meter of endpoint-distance change over a 2-meter raw movement.
Policy v56 requires the selected path span itself to be at least 5 meters before
endpoint completion. This prevents a tiny jitter fragment from borrowing endpoint
evidence elsewhere in the same traversal window while preserving completion for a
substantive selected span.
Policy v57 adds four independently guarded refinements. Endpoint proximity subtracts
reported horizontal accuracy before applying the 5-meter corridor. A named
`access=private` service driveway is treated as an HOA neighborhood road, while
unnamed driveways remain excluded. Raw-supported accessory shortcuts may use road
boundaries. Finally, a short excursion containing a service road or road link may
be replaced by a road-only route that either is materially shorter and raw-supported
or strictly continues one boundary's logical road within a 20-meter detour budget;
the selected middle must not already have that continuity. Road-only graphs are
compiled once per repair to keep evaluation bounded.
Policy v58 removes a symmetric multi-edge road out-and-back of at most 100 meters
one way when its exact inverse portions are consecutive and the outbound road chain
lacks directed raw support within 15 meters. A raw-supported cul-de-sac remains;
the rule only removes an HMM branch excursion whose geometry was introduced by
permissive nearby road candidates.
Policy v59 narrows that support test to the exact turnaround point represented by
the center inverse pair. A palindrome is retained when raw observations, after
horizontal-accuracy adjustment, approach within 15 meters of that point. This
preserves genuine cul-de-sac and out-and-back traversals whose side-of-road trace
does not continuously support every centerline edge, while still removing a branch
whose dead end was never approached.
Policy v60 permits parking aisles only as raw-gated foot/bicycle continuity
evidence, never observation candidates. A parking-only road-to-road graph run may
total at most 200 meters; each aisle must have at least 5 meters and 50 percent of
its directed geometry supported within a 20-meter accuracy-adjusted raw corridor.
Only that supported interval is emitted. A fully reached road endpoint may add at
most one adjacent supported aisle and stops at the aisle's next graph node. Mixed,
path-boundary, over-limit, unsupported, or `access=no` aisle routes remain excluded.
Policy v61 repairs an ordinary-road turn when decoded cancellation leaves a short
middle road portion provably disconnected from one boundary. The replacement must
be a road-only graph route of at most 15 meters that strictly continues either
boundary's logical road, stays within the existing 20-meter detour budget, and has
raw-supported incoming and outgoing boundary roads. Longer replacements retain the
existing directed raw-progress requirement. This handles centerline offsets through
wide intersections without treating connected road detours as shortcuts.
Policy v62 removes a single physical road segment emitted immediately in both
directions during a turn when the surrounding portions belong to different logical
roads and connect directly at the graph node. The one-way stub is capped at 30
meters, must belong to one of the boundary roads, and is retained when the raw trace
supports at least 60 percent of the segment in both directions within 15 meters and
45 degrees. This distinguishes an actual short out-and-back from a sidewalk rounding
an outer corner near the stub endpoint.
Policy v63 replaces a bounded overlapping road reversal at a road-to-path turn
with the graph-connected path route when every non-boundary path edge belongs to
the resumed logical path and has ordered raw endpoint support within 15 meters.
The same policy permits a fully reached road node to emit one raw-supported
driveway out-and-back of at most 40 meters. For mapped driveways shorter than GPS
sampling resolution, ordered travel from the road node at least 5 meters beyond
the mapped endpoint is sufficient; one same-logical-road edge of at most 5 meters
may connect the completed road portion to that driveway. Expansion is non-recursive.
Policy v64 excludes a parking-aisle connector when an eligible non-transition road
directly connects the same graph-node pair in either direction. Parking support is
currently evaluated against a canonical segment's endpoint chord, so a curved aisle
loop and the predominant direct road cannot be distinguished safely when they share
both endpoints. The ordinary road is preferred until matcher topology carries the
full source polyline for raw-support projection.
Policy v65 generalizes the v64 shadow test from one direct road edge to a bounded
ordinary-road graph path. A parking aisle is excluded when either direction between
its endpoint nodes has a strictly shorter route composed only of eligible,
non-transition road edges. This handles predominant roads split at intermediate
graph nodes without suppressing an aisle whose only alternative is longer.
Policy v66 adds three bounded road invariants. A fully reached road node may emit
one eligible non-transition dead-end road from 20 through 150 meters out-and-back only
when both directed edges have raw progress or ordered endpoint support within 15
meters. An immediate unsupported side-road reversal of at most 30 meters is removed
when the surrounding portions continue the same logical road. Finally, disconnected
road portions may insert an ordinary-road graph route of at most 40 meters; routes
up to 20 meters rely on supported boundary roads as intersection-centerline
continuity, while longer routes require directed or ordered endpoint support. A
disconnected middle road excursion of at most 40 meters may be replaced by a
raw-supported alternative that saves at least 5 meters. Side-road reversals may
also use ordered travel past the mapped endpoint as support, preserving genuine
visits whose centerlines are shorter than the raw excursion.
Policy v67 repairs an incorrect traversal suffix after a fully reached road endpoint.
Up to three suffix portions may be replaced by one outgoing eligible segment of the
same logical road, between 20 and 150 meters, only when the replacement saves at
least 10 meters and none of the removed portions belongs to that logical road. Raw
observations must encounter the replacement endpoints in order, stay within a
15-meter accuracy-adjusted endpoint corridor, and accumulate between 70 and 150
percent of the segment's mapped length between those encounters. This supports a
curved road whose endpoint chord is not a useful raw projection without accepting
an arbitrary same-endpoint detour.
Policy v68 replaces a bounded unsupported parallel road run with a connected
road/parking alternative. The selected middle contains at most three portions and
totals at most 120 meters. The alternative is computed after excluding those physical segments, may use only
ordinary road and parking-aisle edges, may be at most 20 meters longer, and every
edge must have directed or ordered endpoint support within a 30-meter road corridor.
The selected middle may not already contain parking-aisle evidence, preventing this
repair from swapping one supported parking route for another. The replacement or
one boundary must contain exactly one physical parking aisle. Parking portions retain
the v60 20-meter, 5-meter, and 50-percent interval gates.
Policy v69 permits a parking-only run to connect one road boundary to one path
boundary, while path-to-path runs remain excluded. The 200-meter run cap and each
aisle's 5-meter/50-percent support requirement remain. A mapped parking entry no
longer than 10 meters may use ordered travel at least 5 meters past its directed
endpoint within the 20-meter corridor when clamped projection cannot establish 5
meters of progress; longer aisle portions retain the standard interval requirement.
Policy v70 extends an already selected driveway out-and-back through one connected
same-source-way driveway segment when the total chain is at most 150 meters and both
directions have raw progress, path-distance, or ordered endpoint support within 30
meters. It also removes a cross-traversal network prefix of at least three portions
when at most 20 percent has directed support and raw travel resumes on a supported
segment within 200 meters. A short resume subdivision may rely on its immediately
supported same-logical-road continuation. Same-logical-road internal excursions may
be replaced by a raw-supported route saving at least 10 meters. Incomplete boundary
turns may add up to 40 meters of ordinary centerline continuity without internal raw
support, and unsupported road stubs up to 30 meters may be removed at traversal heads.
Policy v71 disables the v70 matcher repairs after deployed corpus evaluation showed
that traversal-boundary and head heuristics removed supported portions from unrelated
routes. Matcher behavior returns to v69 while retaining v70 as immutable diagnostic
provenance. The September 1, 17, and 20 cases require shape-aware or globally
windowed evidence rather than local traversal-prefix inference.
Policy v72 restores only the within-traversal incomplete-road-turn repair and adds an
ordered, additive out-and-back chain repair. A chain may contain at most three new
physical segments and 150 meters in one direction, must have reverse directed edges,
and must encounter every graph endpoint in outbound-then-return observation order
within 15 meters after accuracy. Ordinary roads may branch from a connected road
junction. An excluded driveway may extend an already selected driveway reversal only
on the same source way. Competing supported first branches reject the repair. The v70
traversal-boundary, traversal-head, and unsupported-prefix deletion rules remain
disabled.
Policy v73 disables the v72 repairs after deployed evaluation added 16 unrelated
physical segments and removed one accepted segment from the October 8 route; the
September 17 diagnostic also exceeded its timeout. Matcher behavior again equals
v69/v71. The v72 diagnostic rows remain immutable rejected evidence.
Policy v74 enables only the normal-road branch subset of v72's additive repair. The
selected route must continue through the same non-empty logical road at the junction.
The branch must contain two or three ordinary-road segments, each at least 20 meters,
total at least 60 and at most 150 meters, and remain on one non-empty logical path and
one OSM source way. Every outbound and reverse endpoint must occur in raw observation
order within the accuracy-adjusted 15-meter corridor, and competing first branches
reject the repair. Driveway-chain and road-turn repairs remain disabled.
Policy v75 repeats the ordered out-and-back chain repair after driveway inference so
an inferred driveway anchor can extend through one connected same-source-way segment.
The continuation must retain reverse directed travel, remain at most 150 meters, be
absent from accepted evidence, and have ordered outbound-return endpoint support.
v75 also permits one incomplete road turn across a soft traversal boundary only when
the selected stub is disconnected on entry but connected on exit, all three selected
logical roads differ, and exactly one direct ordinary-road edge on the incomplete
boundary's logical path reaches the following road. The completed boundary remainder
plus connector is capped at 40 meters and may be at most 30 meters longer than the
removed stub. Empty intervening traversals may be crossed only within the existing
128-observation and hard-split bounds.
Policy v76 removes a selected parking-aisle portion when it appears immediately
between two directly connected portions of the same non-empty logical road and OSM
source way, both the parking aisle and continuing road depart from the same graph
node, and raw observations strongly support the road continuation. The cleanup runs
after all additive repairs, does not apply across different logical roads/source ways,
requires at least 40 meters of selected parking-aisle travel so short intersection
connectors remain intact, and does not infer new coverage.
Policy v77 replaces a soft-boundary detour with one direct same-road segment only
when the boundary portions share a non-empty logical path and source way, the detour
contains three to six ordinary-road portions totaling at most 100 meters, at most 20
percent of that detour is raw-supported, exactly one direct connector exists, the
connector saves at least 20 meters, and raw observations support it. v77 also removes
an unsupported single-segment road out-and-back between connected portions of the
same through road when the branch is an unnamed service-road connector terminating
at a graph dead end; supported
turnarounds remain intact. Dead-end support requires directionally aligned raw travel;
merely crossing the endpoint or segment perpendicularly does not preserve the stub.
Policy v78 removes one unsupported transition-only driveway out-and-back between
directly connected portions of the same through road. The driveway must be one
physical segment and 12 to 30 meters in one direction; either direction is retained
when raw observations support travel within 45 degrees and the accuracy-adjusted
15-meter corridor. A perpendicular trace that merely passes the junction does not
support the driveway. Shorter junction-touch driveways and multi-segment driveway
chains are not affected.
It also removes a full ordinary-road portion of at most 20 meters when that portion
is isolated from both neighboring road portions, no temporal or spatial hard split
separates them, and raw travel does not support the portion within the same directional
corridor. Directionally supported isolated portions and longer partial roads remain.
Policy v79 permits the raw-supported same-road excursion repair to use one or two
replacement subdivisions. Every replacement must remain on the surrounding road's
non-empty logical path and OSM source way, the replaced excursion must contain at
least three ordinary-road portions, the direct route must save at least 20 meters and
be at least one-third shorter, and each replacement subdivision must have directional
raw support. This handles roads split at closely spaced intersection nodes without
allowing a route through an unrelated parallel way.
Policy v80 completes two adjacent, already selected ordinary-road portions to their
exact shared graph node when each already contributes at least 20 meters, they belong
to distinct logical paths, raw observations approach the shared node within the
accuracy-adjusted 15-meter corridor, and together they omit at most 30 meters. It does
not search for or insert a connector; it only restores clipped tails and heads at a
known connected road turn.
Policy v81 recognizes clipped directed starts and ends independently for forward and
reverse portions when performing the bounded cross-window turn completion. It also
permits one or two short, mostly unsupported road tangents to be replaced by a
directionally raw-supported road route of two or three portions up to 400 meters,
provided its graph length agrees with raw distance within 25 percent plus 20 meters.
When the full boundary tangent shares the before-anchor's logical road and reaches a
road dead end with raw turnaround evidence, the tangent is retained outbound and
reverse before the alternate continuation. Non-road path/cycleway continuations do
not prevent that road-dead-end classification.
Policy v82 extends unsupported single-road reversal cleanup to ordinary residential
road dead ends. Directionally aligned raw travel retains the segment; raw turnaround
evidence at an ordinary road's terminal endpoint also retains it. A perpendicular
trace that continues through the junction without reaching the endpoint removes the
out-and-back. Service-road connectors keep the stricter directional-only requirement.

Dense route input uses deterministic adaptive sampling before candidate lookup:
retain endpoints, temporal/spatial gap boundaries, material accuracy-band changes,
and significant turns, plus a point after either 5 meters of movement or 5 seconds.
The sampling policy is versioned independently from matcher rules.

Before acceptance, record the fixture composition, labeling method, candidate
rules, chosen thresholds, tie-breakers, measured errors, known limitations,
matching-rule versioning, and rematch trigger. The accepted ADR must contain
concrete values rather than delegating them to unspecified runtime configuration.

Evidence must report false cross-street attribution and missed-turn rates
separately. Required fixtures include straight intersection travel, true turns,
single-point cross-street excursions, grade-separated crossings, divided and
parallel roads, stationary intersection pauses, long gaps, sparse traces, and
same-name roads crossing locality boundaries.

The following invariants do not depend on tuned thresholds:

- sharing an intersection vertex never attributes an untraversed cross street;
- every attributed segment has positive traversed length in a decoded transition;
- every rendered coverage geometry comes from decoded traversed portions;
- trace gaps never render connector geometry;
- logical-path attribution never causes unvisited member geometry to render;
- when network alternatives connect the same anchors, route-level directional raw
  support is required: an unsupported perpendicular excursion cannot displace a
  directionally supported continuation on the same logical path;
- changing candidate iteration order does not change the result; and
- duplicate stationary observations do not add traversed segments.
