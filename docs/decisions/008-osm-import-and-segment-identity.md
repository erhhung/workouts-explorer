# ADR 0008: OSM Import, Segment, And Logical Path Identity

## Status

Accepted. The canonical multi-region promotion slice described here is
implemented. External download, build, validation policy, job orchestration, and
application reconciliation remain the next delivery slice.

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

## Decision Process

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

Through derivation version 18, named road identity removes one leading spelled-out cardinal direction from the
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
user-facing attribution and Path Coverage rows. Named logical paths begin with an
authoritative municipal city/town locality identity, normalized path name, and a
broad compatible path class. Locality identity uses imported administrative
provenance rather than postal-city or display text alone. Thus `El Camino Real, Mountain View` and `El Camino
Real, Sunnyvale` are distinct logical paths even when their source geometry is
part of one continuous road.

Coverage identity primarily follows OSM graph connectivity within an attribution
scope. That scope is a named educational ground for eligible unnamed geometry,
otherwise a named park for non-road geometry, an authoritative municipality or
county, or finally the provider region. Compatible connected segments may share
an identity across source-way and path-class transitions, while disconnected
components and scope or name boundaries remain separate. Named roads also use
bounded proximity connections to join nearby divided carriageways and topology
gaps as described by derivation versions 13 and 14 below.

Derivation version 6 applies a second, connectivity-bounded identity pass after
park attribution. Its scope is the stable selected park ID when present, otherwise
municipal locality ID, otherwise provider region ID. Within a scope, exact shared
graph nodes merge existing logical IDs with the same normalized name; all null
names use one unnamed key. Broad class, highway, and source lineage are ignored
only across those exact connections. Park scope wins over locality, preventing
park-contained geometry from bridging to outside geometry, while municipality
scope prevents cross-city merges. National-park behavior from version 5 is thus
preserved and generalized, including same-name class transitions across municipal
boundaries inside one national park.

When no authoritative municipality contains a named segment, logical identity is
scoped by provider region ID, normalized path name, and broad path class instead
of the former global `outside` bucket. This prevents disconnected same-name roads
in different extracts from collapsing into one path while retaining useful
regional aggregation. Derivation version 2 introduces this fallback; municipality-
scoped logical IDs remain stable.

Segment geometry crossing a locality boundary is split deterministically at that
boundary before logical-path assignment. The spike must define behavior for
boundary roads, disputed or overlapping boundaries, unincorporated areas, missing
locality data, name aliases, route relations, and name or boundary changes.

Unnamed paths remain eligible and display as N/A. Initial identity retains
deterministic source and topology lineage, then version 6 merges those IDs only
when exact graph endpoints connect under the same attribution scope. Disconnected
IDs are not newly merged. Existing legacy IDs can already contain disconnected
geometry; version 6 deliberately does not detect or split those members.

Decoded positive-length segment traversals remain the source evidence for rendering
geometry. Durable evidence has one row per workout and physical segment: overlapping
or contiguous traversal spans are dissolved regardless of direction, while truly
disjoint spans remain separate components of one `MultiLineString`. Dissolution
must not fill an untraversed gap. A workout contributes at most once to the
containing logical path, using its earliest accepted member-segment traversal.
Only traversed geometry from selected workouts renders as visited; every emitted
span uses the logical path count and bucket.

Named park polygons are generation-local derivation input. Qualifying
`leisure=park` areas from 500 m² through 25 km² and qualifying
`leisure=nature_reserve` or `boundary=protected_area` areas from 1,000 m² through
10 km² may attribute a fully contained locality-clipped segment. State parks use
case-insensitive `protection_title=State Park` or `park:type=state_park`.
Authoritative national-park tagging is `boundary=national_park`,
`protected_area=national_park`, or case-insensitive
`protection_title=National Park`; these named areas qualify from 1 km² through
100,000 km², including when `protect_class=2`. Unrelated `protect_class=2`,
invalid polygons, unnamed polygons, and material partial overlaps abstain. Local
parks still require an authoritative named municipality. State and national parks
take precedence in overlap selection because they supply regional context; ties
then prefer smaller area, relation before way, and source ID.

Park attribution is additive for named trails and roads and never replaces
physical segment identity. It supplies the highest-priority connectivity scope
for both named and unnamed logical identity. Stable local park identity combines municipal locality
relation, OSM source type, and OSM source ID. Stable state and national park
identity uses a distinct namespace and combines provider region, OSM source type,
and OSM source ID, making one park one entity across municipalities. This allows one workout to
contribute once to both a named logical path and the containing park visit. A
A state- or national-park-attributed road or path uses the park name as its
application display context ahead of municipality or county context. Local parks
are never used for that fallback.
User-facing reads assign an unnamed segment inside a qualifying local park only
to the park. Educational-ground-attributed unnamed roads and paths similarly
roll up to one application entity per `education_id`, regardless of connectivity
component or path class. Named campus roads and paths retain their normal path
identity. State- and national-park-attributed unnamed roads and paths retain their
individual application identities in addition to the park visit.

The version-6 implementation first scope-rebases the bounded park-attributed
subset so a legacy locality ID cannot remain shared across a park boundary. It
does not materialize all approximately nine million segment endpoints. Four index-driven start/end join orientations insert only
deduplicated cross-logical-ID edges. Component state contains only logical IDs on
those edges and uses deterministic minimum-member hooking with parent compression.
The merged UUID namespace includes stable scope, encoded name/unnamed key, and
minimum member logical ID. Beyond the park rebase, only participating segment
groups are updated; all logical paths are then reaggregated because an old ID may
retain members in another scope. Validation must report zero remaining same-key
connected splits and records park-rebase, cross-ID edge, affected-ID, and merged-component counts. Cross-scope and cross-name
connections are valid. No disconnected-merge validation is claimed.

Derivation version 7 supersedes that component contract. Incoming logical IDs are
not connectivity vertices because one can already contain disconnected geometry.
Instead, physical segments are vertices; exact shared graph nodes form edges only
when attribution scope, normalized name or unnamed sentinel, and road/path class
all match. Cycleways and footways belong to the path class. The deterministic
component UUID uses the minimum physical segment ID.
This splits disconnected legacy members and prevents road identities from using
path-class segments as transitive bridges while preserving source-lineage merging
inside one graph-connected road/path-class component.

Derivation version 8 adds branch continuity. For one scope/name/road-path key,
segments from different source ways connect only where exactly two eligible
segments meet. At nodes with three or more eligible segments, cross-source edges
are removed while same-source edges remain. This preserves a source way through a
junction, still joins source-way transitions such as Stevens Creek Trail into
Sleeper Park, and prevents one branching unnamed sidewalk network from becoming a
town-wide identity. Derivation records its remaining required-edge split count for
promotion validation, avoiding a second full endpoint-join validation pass.

Derivation version 9 restricts that branch-edge pruning to unnamed groups. A
normalized name provides semantic identity across a branch, so named same-scope,
same-road/path-class segments retain every exact graph edge regardless of source
way. Unnamed groups continue to use the source-way/simple-continuation rule.

Derivation version 10 additionally retains a cross-source unnamed edge at a branch
when the edge joins the only two incident segments of one exact broad class. This
uses OSM's cycleway/footway/trail classification to identify the through pair while
leaving different-class spurs and ambiguous three-way same-class branches split.
Derivation version 11 retains named valid county (`admin_level=6`) polygons as a
fallback below municipality (`admin_level=8`) and above provider region. The
smallest covering polygon wins. A non-road municipality clipping island no longer
than 25 m is absorbed into county scope when both adjacent pieces from the same
source segment belong to that county with the same name state and exact broad
class. This is a clipping-artifact correction, not general permission to cross
scope; roads, parks, endpoints, and longer municipal pieces remain unchanged.

Derivation version 12 corrects the initial covering-polygon selector to order by
administrative specificity (`admin_level=8` before `6`), then polygon area and
relation ID. Numeric OSM relation IDs never decide municipality-versus-county
precedence.

Derivation version 13 treats a normalized name as sufficient to bridge exact graph
road/path transitions. It also adds spatial edges between same-scope, same-name
road segments within 15 m, extended to 50 m only when both carry an explicit
one-way tag. This models divided carriageways without applying proximity grouping
to unnamed roads or arbitrary two-way roads.

Derivation version 14 keeps one nearest named-road proximity edge per scope/name
and source-way pair, including a same-source pair when clipping or OSM topology
left it disconnected. This bounds graph storage without dropping short approaches.

Derivation version 15 limits park identity scope to non-road geometry. Road
segments may retain park tags for context, but identity grouping uses their
municipality/county scope so a park crossing does not split a named street.

Derivation version 16 retains named educational-ground polygons for schools,
colleges, and universities. Only canonical segments with no formal normalized name
receive education tags/scope; named campus roads and paths are unchanged. Matcher
copy coalesces the education name only at the application boundary, preserving
canonical source provenance and avoiding an application schema migration.
Derivation version 17 recognizes explicitly tagged state parks as regional parks,
gives state and national parks precedence over overlapping local park polygons,
and exposes either regional park as application display context for all contained
roads and paths without changing road logical identity scope.
Derivation version 18 computes named-road graph components across locality
boundaries before deriving locality-scoped IDs. A road that follows or repeatedly
crosses a municipal boundary therefore remains one identity per municipality,
without allowing identities themselves to span municipalities.

Derivation version 20 preserves the case/whitespace-normalized original OSM
display label in the named attribution-group key. `East`, `West`, and plain road
labels therefore form independent graph/proximity components even though the
persisted search normalization may share one direction-stripped base name. After
component propagation, a dedicated checkpointed stage examines road components
of at most 25 meters. A short component is absorbed only when its graph endpoints
identify exactly one different component with the same base name and locality;
ambiguous fragments remain independent. This removes municipal-boundary/name
transition slivers without recombining substantial directional roads. The final
logical-path UUID continues to include the propagated component and locality,
so no identity spans municipalities.
Driveway, parking-aisle, and parking-area geometry remains eligible for matching
and Coverage rendering under the accepted matcher policy, but those ordinary
logical paths are excluded from user-facing path statistics and history. Their
park visit remains eligible when the segment is fully inside a qualifying park.

### Refresh and promotion constraints

- Build or update public data without destroying the last usable dataset.
- Promote a refresh only after schema, count, geometry, and replica-readiness
  checks succeed.
- Reconcile copied application segments and logical paths after successful promotion.
- Rematch only routes affected by changed segment regions or identities.
- Preserve old copied segment data until dependent private attribution is safely
  reconciled.
- Keep OSM diagnostics free of private route or account details.

### Canonical multi-region promotion

Generation schemas are mutable only during build and preparation. After clipping,
preparation adds fixed region and generation provenance, replaces candidate keys,
validates partition-bound constraints, builds indexes matching the canonical
parents, and renames `ways`, `localities`, and `path_segments` with their
generation ID. Canonical contribution tables are `LIST (region_id)` partitioned
parents. Promotion moves those prepared tables into `osm_canonical`, detaches the
old leaves for that region, and attaches the new leaves; it never performs a
full-table `INSERT SELECT`. A transaction-scoped advisory lock serializes
promotion per stable region ID. The invoker-security function verifies catalog
and schema identity, prepared relation names, validated provenance constraints,
required indexes, nonempty source and segment sets, segment/source-way and
derivation versions, logical-path equality, and exact JSON preparation gates.
Leaf replacement and catalog retirement/activation occur in one transaction, so
readers see either the old or new complete region contribution.

Stable `osm_active` views preserve the original generation-table reader columns.
For overlapping regions, ways select the highest OSM way version; localities
select the highest relation version. Equal source versions use highest generation
ID and then lexicographically highest region ID as deterministic tie-breaks.
Segments are eligible only when their source way and source version won way
selection, and duplicate segment UUIDs use the same generation/region precedence.
`logical_paths` is aggregated from that deduplicated segment view rather than
copied from any individual region.

Schema 4 preflights the expected deployed schema-3 shape: nonempty canonical
heaps require exactly one active region, and every canonical tuple must match its
region and generation. The migration renames those heaps into generation leaves,
creates partitioned parents and indexes, and attaches the leaves without copying
tuples. A fresh empty database follows the same migration without requiring an
active generation. `region_storage` records each attached generation and its
three leaves. Replacement detaches rather than drops prior leaves, while
`storage_gc` queues those leaves and the now-unneeded build schema for bounded,
out-of-transaction cleanup. This foundation does not yet add worker GC execution.
Downgrade must not copy data and explicitly refuses once a schema-4 replacement
has queued retired leaves; the legacy schema cannot safely recover after GC.

### Regional and fallback constraints

Configured named regions define the initial loaded region set. Overlapping regions
deduplicate source objects by OSM type and ID and retain source-region provenance.
A route outside every promoted region remains visible in Routes mode but reports
coverage pending when automatic addition has queued an eligible named region, or
unavailable when automatic addition is disabled, no provider region contains it,
or the smallest region exceeds the byte ceiling. The worker never performs public
OSM or Overpass lookups per point.
Automatic named-region addition is deferred from the current implementation. Until
it is implemented, a no-evidence diagnostic reports cataloged regions covering route
points that lack an active configured generation; the UI presents those regions as
unavailable and retains the raw route.

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

The selected Osmium and osm2pgsql versions, measured import/query/storage results,
derivation and stable identity rules, locality behavior, and failure-safe refresh
model are recorded above. Migration contract tests cover partition conversion,
preparation gates, overlap precedence, source-version filtering, exact segment
deduplication, per-region replacement, GC state, rollback, downgrade refusal,
and delegation through the thin promotion script. The executable PostGIS fixture
attaches overlapping regions, verifies source-version and exact-segment
deduplication, replaces one region without hiding the other, forces a promotion
rollback, and checks queued GC metadata. On the 2,020,900-way,
4,467,193-segment xdev generation,
the canonical 50-meter candidate plan starts from the geography GiST index and
measured 225 ms cold and 7.3 ms warm. The manual regional updater now implements
the external lifecycle with digest-pinned Osmium 1.19.0 and osm2pgsql 2.3.1,
transactional single-flight generation reservation, bounded download provenance,
an injectable validation-gated pipeline, schema-4 promotion, and retryable
post-promotion storage GC. It intentionally remains an operator-run command;
automatic API/job and UI contracts are deferred.
