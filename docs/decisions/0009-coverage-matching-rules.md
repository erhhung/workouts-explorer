# ADR 0009: Coverage Matching Rules

## Status

Proposed - requires representative route experiments during Milestone 7.

## Context

The MVP attributes each workout to nearby public path segments using
nearest-segment matching. It must avoid false coverage on parallel roads and
poor-accuracy points while retaining the unmodified raw route. Each workout may
contribute at most once to one segment, using its earliest matching timestamp.

The correct distance threshold and quality rules depend on actual Health Auto
Export routes, route-point accuracy, path density, and the segment model selected
by ADR 0008. Choosing constants before those inputs exist would turn guesses into
persistent coverage data.

## Proposed Decision Process

Build a curated, privacy-safe evaluation set containing:

- clear on-path routes across roads, trails, and cycleways;
- parallel roads, frontage roads, divided roads, bridges, and crossings;
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
- use a point's retained quality information without altering the raw route;
- reject implausible candidates rather than always selecting the nearest path;
- use deterministic tie-breaking;
- retain the matching-rule and map-data versions for diagnostics;
- upsert a unique workout/segment pair with the earliest matched timestamp;
- permit safe rematching after rule or OSM changes; and
- expose aggregate quality metrics without coordinates or private geometry.

A global maximum distance may be combined with stricter quality- or
class-sensitive rules if the evaluation demonstrates a clear improvement. Every
operator-configurable threshold must represent a legitimate deployment policy;
correct-by-construction matching rules remain versioned code.

## Alternatives To Evaluate

### One fixed nearest-segment distance

This is simple and remains the baseline. It may be sufficient if representative
routes show acceptable ambiguity.

### Accuracy-adjusted candidate distance

Accuracy can inform acceptance, but allowing poor accuracy to expand the search
may increase false positives. The spike must test both rejection and expansion
strategies.

### Heading and continuity checks

Course, adjacent points, and segment direction may disambiguate parallel paths
without introducing a full routing engine. Added complexity must show measured
benefit.

### Dedicated map-matching engine

ADR 0001 rejects this for the MVP. It is reconsidered only if the nearest-path
approach cannot meet the curated acceptance threshold.

## Acceptance Evidence

Before acceptance, record the fixture composition, labeling method, candidate
rules, chosen thresholds, tie-breakers, measured errors, known limitations,
matching-rule versioning, and rematch trigger. The accepted ADR must contain
concrete values rather than delegating them to unspecified runtime configuration.
