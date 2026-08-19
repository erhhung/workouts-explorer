# ADR 0008: OSM Import, Segment, And Logical Path Identity

## Status

Proposed - requires an OSM toolchain spike before Milestone 7 schema and
bootstrap work.

## Context

Coverage matching requires roads, trails, cycleways, and other eligible paths
from OpenStreetMap. The OSM database must preserve node, way, relation, tag, and
version provenance while deriving query-efficient path segments. Matching
segments are often arbitrary user-facing units because one named road can contain
many OSM ways and derived segments or span several cities. Coverage statistics
instead need logical path identities that group compatible segments of the same
named road or path within one authoritative locality. Refreshes must reconcile
copied matched segments and logical paths in the application database without
making existing coverage unavailable after a failed import.

ADR 0001 already selects a separate PostgreSQL/PostGIS OSM database, copied
matched geometry, and manual regional refresh. The OSM
database is named `osm`, runs on the production PostgreSQL server, and is shared
by development and production so large public datasets are not duplicated. It
does not contain private workout or account data. ADR 0001 does not select the
importer, derivation tools, schema, or stable segment identity.

## Confirmed Product And Operational Constraints

- OSM administrative boundary relations define municipal city/town locality for
  the MVP; postal-city boundaries and a separate government-boundary dataset are
  out of scope.
- Current OSM object provenance is sufficient: type, ID, current version,
  timestamp, tags, ordered way-node references, and ordered relation members and
  roles. Full edit history, contributor identity, and changeset history are not
  required.
- Long-lived OSM data and indexes must fit within 20 GiB. The spike must measure
  importer scratch space and WAL separately and must not assume that two complete
  regional generations fit concurrently.
- Configuration is an ordered list of provider-qualified named extracts, not
  coordinates, radii, or dynamically cached regions. The initial list contains
  Geofabrik extract ID `norcal` (Northern California).
- Product and operational terminology calls these named datasets regions. The
  worker configuration contains `osm.regions`, `osm.dataProviders`,
  `osm.autoAddRegions`, and `osm.maxAutoDownloadBytes`; provider source metadata
  may continue to use the upstream term extract internally.
- When automatic addition is enabled, an ingest outside promoted regions resolves
  the smallest containing region from the ordered provider catalogs. A region is
  eligible only when its advertised PBF size does not exceed the byte ceiling.
  Resolution enqueues or reuses one active region-update job and never sends the
  route envelope or points to the provider.
- The updater resolves configured IDs through Geofabrik's versioned index,
  downloads each current PBF into ephemeral local scratch space, records its
  source URL, checksum, header timestamp, and replication metadata, and removes
  it after successful or failed processing. No persistent raw OSM archive or NFS
  storage is required.
- `northern_california` is the equivalent Pyrosm downloader name, but Pyrosm's
  dotted Python catalog path is not an external configuration contract. The
  application uses Geofabrik's stable provider and extract IDs directly and does
  not add Pyrosm solely for downloading.

## Proposed Decision Process

Evaluate importer and derivation toolchains against the configured Northern
California extract and a subsequent full refresh. The spike must prove:

- retention of source node, way, relation, tags, versions, and timestamps needed
  for diagnostics;
- derivation of all required named and unnamed path classes;
- import of authoritative administrative locality boundaries with stable source
  identity;
- deterministic splitting at intersections, relevant topology changes, and
  locality boundaries;
- deterministic grouping of compatible same-name segments within one locality;
- separate stable identities for unnamed paths without locality-wide collapse;
- PostGIS indexes and nearest-candidate query plans;
- a reference-complete eligible-path and municipal-boundary extract that does not
  retain unrelated objects solely because they occur in the source extract;
- repeatable named-extract bootstrap and full refresh;
- stable reconciliation of unchanged segments;
- detection of changed, split, merged, and deleted segments; and
- licensing, maintenance, image, and operational fit for the homelab.
- measured promoted database, index, importer scratch, temporary, and WAL sizes
  against the under-20-GiB long-lived storage constraint. A preliminary
  reference-complete NorCal derivative containing `highway=*` ways and
  administrative boundary relations is 190,143,416 bytes, with 21,690,263
  nodes, 2,021,280 ways, and 435 relations; final PostgreSQL sizing still requires
  the representative import.

The initial NorCal spike on 2026-08-12 selected Osmium 1.19.0 and osm2pgsql
2.3.1 for further evaluation. The 648,847,614-byte source produced a
190,143,549-byte selective PBF with 21,690,263 nodes, 2,021,280 ways, and 435
relations. Every highway way-node reference was complete; clipped regional
boundary relations reported 40 missing nodes, 2,574 missing ways, and 4 missing
relations, so incomplete boundaries are recorded and skipped rather than treated
as authoritative locality geometry.

The full flexible-output import completed in 5 minutes 25 seconds and produced
2,018,520 valid highway geometries with complete current version, timestamp, tag,
and ordered node lineage. Raw ways plus their default geometry index occupied
1,174,331,392 bytes. Adding the required geography expression index brought the
database to 1,368,501,395 bytes. A representative 50-meter Sunnyvale candidate
query improved from a 13.7-second parallel scan to a 15.6-millisecond geography
GiST index scan. These are spike measurements, not yet acceptance of segment or
logical-path derivation.

The full NorCal derivation completed on 2026-08-12 after replacing global
node/cut materialization with 20,000-way batches. Global staging was rejected
because 24,557,767 node occurrences plus overlapping indexes exhausted the
PostgreSQL volume, and a single global window sort exhausted temporary storage.
The accepted storage-bounded process stages only 2,703,177 shared node IDs,
streams source vertices from each way, and rewrites only municipal-boundary
candidates.

Derivation version 1 produced 4,461,751 valid physical segments and 3,033,810
logical paths. Source length of 571,542,372.850141 meters was conserved within
0.00024 meters after boundary splitting. Every segment has a unique deterministic
identity, positive valid geometry, connected adjacent boundary-piece graph nodes,
and a logical-path row. Forty-nine geometrically complex pieces could not be
covered by one municipality within 1 cm and therefore deterministically abstain
from municipal attribution while remaining available for matching.

Named road identity removes one leading spelled-out cardinal direction from the
OSM display name before grouping; the original display name remains on each
physical segment. This groups `East El Camino Real` and `West El Camino Real` as
`El Camino Real, Sunnyvale`, which contains 343 physical segments and 12,134.2
meters, while Mountain View and Santa Clara remain separate locality-scoped
paths. The rule applies only to road-class ways and does not strip embedded or
abbreviated direction text.

The complete database occupies approximately 4.47 GB. A 50-meter candidate query
uses the geography GiST index and measured 59 ms cold and 9.5 ms warm at the
Sunnyvale representative point. Unbounded geometry KNN is intentionally not the
matcher query contract; route points use bounded, batch-oriented candidate
generation.

### Segment identity constraints

The accepted design must distinguish public OSM identity from derived segment
identity. A segment record retains source-way provenance, source version, a
derivation-version identifier, ordered endpoint/topology identity, geometry, path
classification, name, and display tags.

Derived identifiers must be deterministic for an unchanged importer and
derivation version. Geometry hashes alone are insufficient because harmless
coordinate edits should not masquerade as unrelated provenance, while a new
derivation algorithm must not silently reuse old identity.

### Logical path and locality constraints

The accepted design must retain a second identity above matching segments for
user-facing attribution and Path Coverage rows. Named logical paths are scoped by
an authoritative municipal city/town locality identity, normalized path name,
and a broad compatible path class. Locality identity uses imported administrative
provenance rather than postal-city or display text alone. Thus `El Camino Real, Mountain View` and `El Camino
Real, Sunnyvale` are distinct logical paths even when their source geometry is
part of one continuous road.

Segment geometry crossing a locality boundary is split deterministically at that
boundary before logical-path assignment. The spike must define behavior for
boundary roads, disputed or overlapping boundaries, unincorporated areas, missing
locality data, name aliases, route relations, and name or boundary changes.

Unnamed paths must remain eligible and display as N/A, but every unnamed segment
in one locality must not collapse into a single logical path. Their identity uses
deterministic source and topology lineage within the locality. The spike must
measure whether connected-component derivation or retained source-way/relation
lineage provides the most stable useful grouping.

Decoded positive-length segment traversals remain the evidence and rendering
geometry. A workout contributes at most once to the containing logical path,
using its earliest accepted member-segment traversal. Only traversed portions
from selected workouts render as visited; every emitted portion uses the logical
path count and bucket.

### Refresh and promotion constraints

- Build or update public data without destroying the last usable dataset.
- Promote a refresh only after schema, count, geometry, and replica-readiness
  checks succeed.
- Reconcile copied application segments and logical paths after successful promotion.
- Rematch only routes affected by changed segment regions or identities.
- Preserve old copied segment data until dependent private attribution is safely
  reconciled.
- Keep OSM diagnostics free of private route or account details.

### Regional and fallback constraints

Configured named regions define the initial loaded region set. Overlapping regions
deduplicate source objects by OSM type and ID and retain source-region provenance.
A route outside every promoted region remains visible in Routes mode but reports
coverage pending when automatic addition has queued an eligible named region, or
unavailable when automatic addition is disabled, no provider region contains it,
or the smallest region exceeds the byte ceiling. The worker never performs public
OSM or Overpass lookups per point.

Refresh downloads complete current PBFs and rebuilds selective candidate tables
containing eligible paths, required node and relation lineage, municipal
boundaries, derived segments, and logical paths. Buildings, POIs, addresses, and
unrelated map-rendering features are not retained. Candidate validation and
promotion must leave the previous active generation usable on download, import,
validation, or promotion failure.

Region updates are globally single-flight by stable provider-qualified region ID,
independent of which administrator or ingest detects the need. A successful first
load raises a desired coverage-generation watermark for accounts with unmatched
routed workouts in the region. A successful refresh raises that watermark for
every account with routed workouts intersecting the region, including workouts
that already have coverage. One account/region coverage-update job reconciles all
such workouts and records the applied OSM generation and matcher version.

Coverage coalescing must not lose a newer generation requested while an older job
is queued or running. Completion queues a successor when the desired generation
advanced. Individual workouts may be skipped only when their applied OSM
generation and matcher version already equal the job targets.

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
splits, deleted ways, overlapping regional data, same-name roads crossing city
boundaries, boundary roads, unincorporated paths, aliases, and renamed localities.
