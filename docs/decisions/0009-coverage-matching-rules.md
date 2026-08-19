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
- retain unique workout/segment match evidence and upsert a unique
  workout/logical-path pair with the earliest accepted member-segment timestamp;
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
positions and retain each positively traversed segment portion. Point projections
remain diagnostics; they do not independently create coverage. The coverage map
unions selected workouts' traversed portions by physical segment while joining
logical-path counts and all-time dates. Therefore two disjoint visited portions of
`El Camino Real, Sunnyvale` render as two disjoint geometries with the same popup
statistics, and the unvisited middle does not render.

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
- changing candidate iteration order does not change the result; and
- duplicate stationary observations do not add traversed segments.
