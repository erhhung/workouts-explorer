# ADR 0008: OSM Import And Segment Identity

## Status

Proposed - requires an OSM toolchain spike before Milestone 7 schema and
bootstrap work.

## Context

Coverage matching requires roads, trails, cycleways, and other eligible paths
from OpenStreetMap. The OSM database must preserve node, way, relation, tag, and
version provenance while deriving query-efficient path segments. Refreshes must
reconcile copied matched segments in the application database without making
existing coverage unavailable after a failed import.

ADR 0001 already selects a separate PostgreSQL/PostGIS OSM database, copied
matched geometry, manual regional refresh, and bounded public fallback. It does
not select the importer, derivation tools, schema, or stable segment identity.

## Proposed Decision Process

Evaluate importer and derivation toolchains against a representative California
extract and a small refresh diff. The spike must prove:

- retention of source node, way, relation, tags, versions, and timestamps needed
  for diagnostics;
- derivation of all required named and unnamed path classes;
- deterministic splitting at intersections and relevant topology changes;
- PostGIS indexes and nearest-candidate query plans;
- repeatable regional bootstrap and refresh;
- stable reconciliation of unchanged segments;
- detection of changed, split, merged, and deleted segments; and
- licensing, maintenance, image, and operational fit for the homelab.

### Segment identity constraints

The accepted design must distinguish public OSM identity from derived segment
identity. A segment record retains source-way provenance, source version, a
derivation-version identifier, ordered endpoint/topology identity, geometry, path
classification, name, and display tags.

Derived identifiers must be deterministic for an unchanged importer and
derivation version. Geometry hashes alone are insufficient because harmless
coordinate edits should not masquerade as unrelated provenance, while a new
derivation algorithm must not silently reuse old identity.

### Refresh and promotion constraints

- Build or update public data without destroying the last usable dataset.
- Promote a refresh only after schema, count, geometry, and replica-readiness
  checks succeed.
- Reconcile copied application segments after successful promotion.
- Rematch only routes affected by changed segment regions or identities.
- Preserve old copied segment data until dependent private attribution is safely
  reconciled.
- Keep OSM diagnostics free of private route or account details.

### Regional and fallback constraints

The initial regional dataset is California. Missing route regions trigger one
bounded regional retrieval/cache operation, never one public request per point.
The accepted toolchain must define loaded-region metadata and overlap behavior
between extracts and fallback cache entries.

## Alternatives To Evaluate

- `osm2pgsql` with a custom flexible output and derivation pipeline;
- `imposm` or another maintained PostGIS importer;
- preprocessing with `osmium` followed by explicit SQL/PostGIS derivation; and
- a custom minimal importer only if maintained tools cannot preserve required
  hierarchy and refresh behavior.

A routing engine remains outside this ADR because ADR 0001 selects nearest-path
matching for the MVP.

## Acceptance Evidence

Change this ADR to Accepted only after recording the selected versions, schemas,
derivation rules, stable segment algorithm, measured import/query behavior,
refresh failure behavior, rejected alternatives, and operational commands. The
spike must include named roads, unnamed trails, relations, parallel paths, path
splits, deleted ways, and overlapping regional data.
