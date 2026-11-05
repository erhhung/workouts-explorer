# ADR 0015: Route Continuity And Preserved Observations

Status: Accepted; source 009 and the corresponding focused-route UI are deployed.

## Context

The timestamp-cadence segmentation rule can miss a distant GPS jump. In
particular, after a temporal split its running baseline is empty, so the first
connection in that component can span an implausibly large distance. Drawing
that connection invents travel through unobserved space. Deleting the workout or
its original samples would unnecessarily discard valid sections and audit data.

## Decision

Use the existing Coverage spatial boundary: consecutive observations more than
**200 metres** apart do not establish a continuous line. This is an upper bound
on an inferred connection, not an activity-speed classification. A long sampling
gap can represent missing observations rather than an invalid endpoint.

Keep original GPS rows, export content hashes, timestamps, provider aggregates
and workout identity intact. Apply the policy when the fenced ingest summary
writer builds derived route geometry, before projected chunks and zoom-dependent
LODs are generated. Independently apply the same spatial limit to the original
points used by the browser's focused route and direction markers.

Spatial splitting is a second pass over existing temporal components. It never
resets their cadence calculation, reconnects an existing time gap, or joins
nonadjacent valid samples across an outlier. New singleton fragments do not form
lines and are not fabricated into stationary dots; repeated-coordinate runs
remain valid stationary components. A route with no drawable component has NULL
derived geometry while retaining its observations and workout.

Coverage already partitions observations at its 200-metre spatial boundary, with
additional temporal/backward-time rules. Matching, sampling, supplemental repair,
minimum traversal, canonical counts and matching-contract versions are unchanged.
The policy does not attribute the gap or substitute a straight line between valid
sections. Historical matching proof must be checked before deciding whether an
affected workout needs a Coverage rerun; an unstarted workout with no evidence
must not be relabeled current.

## Existing Data

Migration 009 installs the policy without a global historical scan or rewrite.
An explicitly authorized, account-scoped operator scan identifies connections
still present in stored geometry, retaining a bounded manifest and preimages.
Rehearse the derived-geometry update, guard against changed source data, then
apply the selected repair atomically. Existing chunk-maintenance triggers rebuild
both exact and LOD projections. The normal account generation advance invalidates
old map capabilities and cached responses; generations must not be reset.

Verification compares retained edge multisets, not just the absence of long
lines. All non-gap edges must remain byte-identical. Original point-row hashes,
workout metadata and relevant Coverage evidence/proofs are checked independently.
Matching results that already obey the existing hard-gap policy are preserved
rather than recomputed or deleted. Detailed manifests, actual location/date
examples, screenshots and recovery material remain private.

## Compatibility And Verification

009 keeps minimum runtime 31 and existing writer/MVT signatures. Supported shipped
runtime-31 and runtime 31 readiness was tested against schema 31, in addition to
the new runtime 31 body/ACL checks. No worker image, provider reimport or OSM
migration is required. Historical 001-031 SQL remains unchanged.

Tests cover first-pair jumps, isolated outliers, stationary runs, unchanged time
components, idempotence, original-observation preservation and the real fenced
ingest writer. Authorized live checks cover focused browser geometry and actual
private tiles at selected gap-only locations. A tile containing independently
observed geometry is not required to be empty merely because it also contains a
gap midpoint. These checks do not certify map-rendering latency or repair every
other GPS-quality problem.
