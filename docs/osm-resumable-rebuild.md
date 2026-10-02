# Resumable OSM Rebuild Contract

OSM rebuilds run on storage-constrained PostgreSQL infrastructure where a full
regional build can take many hours. Losing completed database work after a late
failure is not acceptable.

## Terminology

- A **stage** is an ordered, durable pipeline phase such as `derive`,
  `clip-candidates`, or `identity-propagate`. A stage has one row in
  `osm_catalog.generation_stages`, one SQL/version fence, and a state of
  `running`, `completed`, or `failed`.
- A **batch**, also called a **unit**, is one invocation of work inside a stage.
  Before each database-mutating batch, the controller runs storage preflight.
  The batch commits its output, cursor, processed-row count, and batch count in
  one transaction. A stage remains `running` while it has more batches.
- A **batched stage** may execute many batches with the same stage name. Resume
  reloads its last committed cursor and starts the next batch; only the current
  uncommitted batch can be lost.
- An **atomic stage** consists of one naturally bounded batch. It still receives
  a preflight and durable terminal checkpoint, but has no intermediate cursor.
- Start/completion log banners bracket batches, not whole stages. Repeated
  banners with the same quoted name are successive batches in that durable
  stage. Naturally short setup, terminal, or index batches may complete in less
  than the 10-15 minute sizing target.

Completed stage fences are immutable for a retained generation. If the SQL for
a completed stage changes, a later resume rejects the new image rather than mix
outputs produced by different implementations. Therefore a generation must
finish on the image that produced its completed fences, or resume from that
image's immutable digest. A newly checkpointed implementation applies only to a
new generation.

## Performance Contract

- Every database-mutating phase after `osm2pgsql` is durable and resumable.
- Every storage-preflighted batch logs its starting PVC capacity/free space and,
  before its completion banner, the in-batch free-space low-water mark plus a
  fresh post-transaction/post-GC sample for post-run capacity analysis.
- One database-wide advisory lock makes the updater the sole owner of build and
  resumability resources. After acquiring it, startup terminates stale
  `workouts-osm-update` backends from older Jobs and verifies that their
  table/row/advisory locks were released. A live modern updater or a conflicting
  lock owned by another application fails startup; unrelated services are never
  terminated.
- Batch sizing targets 10-15 minutes on the production reference cluster and
  uses keyset cursors, not offsets. A unit must not be deliberately sized to run
  longer than 15 minutes without an internal checkpoint.
- Recovery may lose only the current uncommitted unit, so a restart, reconnect,
  cancellation, or failure loses no more than 15 minutes of completed database
  work under the reference load.
- A pod restart, PostgreSQL reconnect, operator cancellation, or stage failure
  resumes after the last committed cursor without rebuilding prior phases.
- Completed phases are never rerun unless their input/version/checksum fence
  changes. A checksum change is fail-closed: partial output from different stage
  implementations is never mixed, so the operator must abort that candidate
  and start a new generation.
- Validation is always rerun before promotion.
- Active canonical generation data remains untouched until atomic promotion.
- Build schemas are retained after any post-import failure and removed only by
  explicit abort or successful post-promotion GC.
- PostgreSQL PVCs and Longhorn snapshots are never changed without explicit
  operator authorization.
- Before every resumable unit starts, the updater checks free space on the
  PostgreSQL primary filesystem. The unit may start only when free space is at
  least 10% of the PVC's usable capacity. A 30 GiB PVC therefore requires at
  least 3 GiB free at unit start.
- Insufficient starting space is an operator-blocked condition, not a transient
  stage failure. The updater aborts before executing unit SQL, records the
  measured free/capacity bytes and threshold, returns a bounded operator-visible
  error, and does not retry automatically.
- An operator must reclaim or authorize storage, verify the primary again, and
  explicitly clear the block before the generation can resume.
- Free space may fall below 10% while a unit is running. The unit is allowed to
  finish because temporary data and transaction-local files may be reclaimed at
  commit or rollback. Before the next unit begins, cleanup/GC completes and the
  10% preflight is evaluated again.

## Durable Phase Model

`osm_catalog.generation_stages` records phase state. Batch-capable phases also
store a monotonic cursor and processed-row counters. The intended sequence is:

1. Download and verify source.
2. Filter tags and check references.
3. Import with osm2pgsql. Completion creates the first durable resume boundary.
4. Postprocess boundaries and attribution polygons.
5. Compact derivation, checkpointed by source-way keysets for shared-node
   discovery and segment construction, followed by one index per unit.
6. Clip candidate discovery, checkpointed by source segment UUID keyset.
7. Clip replacement construction, checkpointed by candidate segment keyset.
8. Clip application, checkpointed delete/insert batches.
9. Clip logical-path finalization.
10. Park attribution, checkpointed segment keysets.
11. Education attribution, checkpointed segment keysets.
12. Locality-sliver absorption.
13. Identity-segment staging, checkpointed by source segment keyset.
14. Identity edge construction, checkpointed by edge orientation and segment keyset.
15. Named-road proximity edges, checkpointed by source segment keyset.
16. Branch filtering and component seeding.
17. Component propagation, checkpointed after each keyset batch and convergence transition.
18. Component-aware directional-label sliver reconciliation.
19. Storage-bounded segment rewrite, checkpointed by segment keyset into the
    replacement table before one metadata swap.
20. Partition preparation.
21. Validation and promotion.
22. Retired-generation and build-schema GC.

Every batch is idempotent through primary keys or conflict handling. Cursor state
is advanced in the same transaction as batch output. Resume verifies the build
schema, source SHA-256, source timestamp, tool versions, importer version,
derivation version, region, and all completed predecessor phases.

## Full Rebuild Batch Inventory

The following list is the execution order for a complete regional rebuild. A
name with a slash is `stage/phase`; the stage is the durable catalog row and the
phase is stored in its cursor. `Repeated` means the controller invokes the same
batch name until its keyset is exhausted. The final invocation of a repeated
phase usually reads an empty keyset, advances to the next phase or completes the
stage, and may therefore finish much faster than its data-bearing batches.

The first invocation of a newly started multi-phase stage can appear in older
logs as only the stage name because its empty cursor has not recorded the default
phase yet. The logical phase names below are authoritative for interpreting the
cursor and future phase-qualified logs.

### Source preparation and import

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 1 | `download` | Once | Download and verify the source PBF in updater scratch. This is logged as a batch but precedes the durable database resume boundary. |
| 2 | `tags-filter` | Once | Run `osmium tags-filter` and write the filtered PBF. |
| 3 | `check-refs` | Once | Validate references in the filtered PBF. |
| 4 | `osm2pgsql` | Once | Import the filtered PBF. Completion is the first durable resume boundary. |
| 5 | `postprocess` | Once | Build boundary, locality, park, and education import structures. |

### Compact derivation

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 6 | `derive/init` | Once | Create durable node-usage, shared-node, physical-segment, and logical-path work tables. |
| 7 | `derive/node-counts` | Repeated, 150,000 source ways | Count graph-node usage by source-way keyset. The empty terminal batch advances to shared-node materialization. |
| 8 | `derive/shared-nodes` | Once | Materialize and analyze nodes used by more than one way. |
| 9 | `derive/segments` | Repeated, 100,000 source ways | Construct physical segments and incrementally aggregate logical paths. The empty terminal batch advances to index creation. |
| 10 | `derive/path-primary` | Once | Add the physical-segment primary key. |
| 11 | `derive/source-unique` | Once | Add the source-way/piece uniqueness constraint. |
| 12 | `derive/geography-index` | Once | Build the segment geography GiST index. |
| 13 | `derive/start-index` | Once | Build the start graph-node index. |
| 14 | `derive/end-index` | Once | Build the end graph-node index. |
| 15 | `derive/logical-index` | Once | Build the logical-path lookup index. |
| 16 | `derive/locality-index` | Once | Build the locality lookup index. |
| 17 | `derive/logical-locality-index` | Once | Build the logical-path locality/name/class index. |
| 18 | `derive/finalize` | Once | Analyze derived tables, drop node derivation work tables, and complete the stage. |

### Locality clipping and area attribution

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 19 | `clip-candidates` | Repeated, 200,000 segment UUIDs | Discover segments intersecting locality boundaries. |
| 20 | `clip-replacements` | Repeated, 7,500 candidate UUIDs | Construct clipped replacement pieces and locality/logical-path identities. |
| 21 | `clip-apply` | Repeated, 18,000 source UUIDs | Atomically delete original segments and insert all replacement pieces. |
| 22 | `clip-residual` | Repeated, 10,000 segment UUIDs | Correct residual outside-locality assignments. |
| 23 | `clip-finalize` | Once | Rebuild logical-path aggregates and remove clipping work tables. |
| 24 | `attribute-parks-tags` | Repeated, 750,000 segment UUIDs | Apply park attribution tags and park-scoped identities. |
| 25 | `attribute-education` | Repeated, 750,000 segment UUIDs | Apply educational-ground attribution and identities. |
| 26 | `attribute-slivers/discover` | Repeated, 500,000 segment UUIDs | Discover short locality slivers between matching county-scoped neighbors. |
| 27 | `attribute-slivers/apply` | Repeated, 500,000 sliver UUIDs | Apply replacement locality assignments, record statistics, and complete the stage. |

### Identity graph construction

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 28 | `identity-segments` | Repeated, 500,000 segment UUIDs | Materialize attribution group, endpoint, class, and direction metadata. |
| 29 | `identity-edges/orientation-1` | Repeated, 500,000 left-segment UUIDs | Build start/start endpoint edges. |
| 30 | `identity-edges/orientation-2` | Repeated, 500,000 left-segment UUIDs | Build start/end endpoint edges, then reclaim candidate-only import/index structures. |
| 31 | `identity-edges/orientation-4` | Repeated, 500,000 left-segment UUIDs | Build end/end endpoint edges. Orientation 3 is omitted because canonical segment ordering makes it the symmetric duplicate of orientation 2. |
| 32 | `identity-edges/finalize` | Once | Analyze the edge table and complete the stage. |
| 33 | `identity-proximity/roads` | Repeated, 500,000 identity-segment UUIDs | Materialize named road segments and their geometry. |
| 34 | `identity-proximity/candidates` | Repeated, 500,000 named-road UUIDs | Find bounded nearby road candidates and retain deterministic winners. |
| 35 | `identity-proximity/edges` | Repeated, 500,000 winner UUIDs | Insert proximity edges and remove the winner work table. |

### Components and propagation

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 36 | `identity-components/branch-nodes` | Once | Materialize graph nodes with more than two incident identity segments. |
| 37 | `identity-components/branch-classes` | Once | Materialize two-segment class continuations at branch nodes. |
| 38 | `identity-components/filter-edges` | Repeated, 750,000 edge keys | Delete unnamed cross-way branch edges that do not preserve a class continuation. |
| 39 | `identity-components/components-left` | Repeated, 2,000,000 edge keys | Seed component rows from left edge members. |
| 40 | `identity-components/components-right` | Repeated, 2,000,000 edge keys | Seed component rows from right edge members. |
| 41 | `identity-components/component-index` | Once | Build the parent index, analyze component/edge tables, reclaim branch/proximity staging, and complete the stage. |
| 42 | `identity-propagate/compress` | Repeated, 1,000,000 components | Compress parent pointers. A full keyspace pass can repeat at a higher iteration when changes remain. |
| 43 | `identity-propagate/hook` | Repeated, 100,000 roots | Apply globally materialized component hooks. A changed pass returns to `compress`; a no-change pass advances to assignment. |
| 44 | `identity-propagate/assign` | Repeated, 500,000 components | Assign merged logical-path UUIDs. The empty terminal batch writes propagation statistics, drops hook staging, and completes the stage. |
| 45 | `identity-label-overrides/init` | Once | Create component-label and component-override work tables. |
| 46 | `identity-label-overrides/labels` | Repeated, 250,000 segment UUIDs | Aggregate exact OSM labels and lengths by propagated component and locality. |
| 47 | `identity-label-overrides/neighbors` | Once | Absorb a component of at most 25 m only when graph endpoints identify exactly one alternate component with the same base name and locality. |

`identity-propagate/compress` and `identity-propagate/hook` form a convergence
loop, so their order can repeat as:

```text
compress pass -> hook pass -> compress pass -> hook pass -> ... -> assign
```

The cursor's `iteration`, `changed`, and `last_segment_id` fields show progress
within that loop. Phase-specific batch sizes are chosen independently because a
hook update is substantially more expensive than a compression scan.

### Rewrite, validation, and promotion

| Order | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 48 | `identity-rewrite` | Repeated, 250,000 segment UUIDs | Write final segment identities and incrementally build replacement logical-path aggregates. The empty terminal batch validates completeness, swaps replacement tables, and removes identity staging. |
| 49 | `prepare-partitions` | Once | Add provenance, constraints, and canonical indexes to candidate partition tables. |
| 50 | `validate/init` | Once | Reset validation metrics, bind prepared partition views, and record static counts/provenance gates. |
| 51 | `validate/ways` | Once | Validate way geometry/lineage completeness and source length. |
| 52 | `validate/segments` | Once | Validate segment geometry, source versions, orphan identities, and segment length. |
| 53 | `validate/logical-paths` | Repeated, 50,000 logical-path UUIDs | Compare stored logical-path aggregates with indexed segment aggregates. |
| 54 | `validate/locality-residuals` | Repeated, 100,000 segment UUIDs | Check material geometry outside assigned localities. |
| 55 | `validate/parks` | Once | Validate park areas, tags, and material park residuals. |
| 56 | `validate/education` | Once | Validate education tags and material education residuals. |
| 57 | `validate/finalize` | Once | Assemble the single validation JSON report, remove validation views, and complete the stage. All validation phases are intentionally rerun before a later promotion retry. |

After the final logged batch, the controller performs three orchestration steps
that are not batch SQL files and therefore do not have batch banners:

1. Mark the candidate generation `validating` with its validation report.
2. Atomically promote the candidate and retire the previous active generation.
3. Process detached retired-generation storage and residual build-schema GC.

The most reliable high-level progress indicator is the current stage's position
in this inventory. Within a multi-phase stage, use `cursor.phase`; within a
repeated phase, use the keyset cursor and `batch_count`. Propagation is the one
non-linear section because convergence can require multiple compress/hook
iterations before assignment.

## Production Batch Sizing

Generation 52 provides the reference timings for the 30 GiB two-replica
PostgreSQL cluster. Initial batch sizes are deliberately conservative and must be
tuned toward 10-15 minutes. Reduce a batch before production whenever an observed
run exceeds 15 minutes; a naturally bounded phase that completes in less than 10
minutes does not need artificial delay or enlargement:

| Operation | Generation-52 observation | Initial resumable unit |
| --- | --- | --- |
| Compact derivation | generation 55 node batches took 19-35m at 400,000 ways; first 400,000-way segment batch exhausted over 7 GiB of temporary space | 150,000 ways for node counts; 100,000 ways for segments; one index per unit |
| Clip candidate discovery | 4,455,314 segments in 3h35m | 200,000 segment UUIDs |
| Clip replacement construction | 42,048 candidates in 59m | 7,500 candidate UUIDs |
| Clip replacement insertion | generation 60 processed 35,000 rows in 21m45s and the remaining 7,079 rows in 5m25s | 18,000 replacement UUIDs |
| Outside-locality correction | generation 60 processed its first 100,000 segments in 1h46m58s | 10,000 segment UUIDs |
| Park attribution | 103,454 updates in 54m after full spatial scan | 750,000 source segment UUIDs |
| Education attribution | 109,492 updates in 37m after full spatial scan | 750,000 source segment UUIDs |
| Connected edge orientation | each orientation completed in 2-5m | one orientation plus 500,000 source segments if needed |
| Connected start/end edge orientation | generation 56 whole-table orientation took 38m and left 4% free | 500,000 left-segment UUIDs; omit symmetric duplicate end/start orientation |
| Named-road proximity | 1,551,186 named roads and 719,056 edges in 21m | 500,000 left-segment UUIDs |
| Branch filtering/setup | approximately 4m | one durable phase |
| Branch filtering/component setup | generation 60 filter batches at 250,000 rows took at most 3m42s; 500,000-row left/right population batches took at most 2m35s | 750,000 edge-filter rows; 2,000,000 left/right population rows; drop branch/proximity tables before propagation |
| Component propagation | generation 60 400,000-root hook batches had a 20m median and 51m34s maximum; 1,000,000-row compression scans ranged from 1s to 12m57s | 100,000 hook roots; 1,000,000 compression rows; 500,000 assignment rows; vacuum only when the in-batch PVC low-water sample is below 15% free |
| Final component assignment | 3,798,669 rows in 12m | one durable phase |
| Segment rewrite | generation 56 reached 9% free after 2.25m of 4.5m replacement rows | 250,000 segment UUIDs using indexed nested probes; drop completed edge graph and component parent index before rewrite; retain canonical ways through promotion |
| Validation | generation 56 monolithic validation took 1h34m48s | separate ways/segments/park/education units; 50,000 logical paths and 100,000 locality residual segments per batch; always rerun all groups before promotion |

After the final start/end identity orientation, the pipeline drops candidate-only
indexes and import intermediates whose last consumers have completed. This
includes old path/way lookup indexes, raw park/education import tables, the
start-node identity index, and the pre-identity logical-path aggregate. Canonical
indexes and logical-path aggregates are rebuilt by rewrite and partition
preparation. Generation 56 required this cleanup manually because it reached the
stage using the earlier whole-orientation implementation; future generations run
the same cleanup as a normal checkpointed identity-edge transition.

Batch controllers record `cursor`, `rows_processed`, `batch_count`, and
`checkpointed_at` in `osm_catalog.generation_stages`. The cursor is advanced in
the same commit as output rows. A retry repeats at most the uncommitted batch.
Batch size is a maximum, not a target minimum; spatially expensive batches may be
reduced automatically based on the previous batch duration.

The generation-52 `base/pgsql_tmp` failure occurred on the PostgreSQL primary
PVC, not in the updater pod's scratch volume. Updater scratch requires room for
the source and filtered PBF plus tool overhead; server-side batching controls
PostgreSQL temporary storage. PVC expansion remains prohibited without explicit
operator authorization.

## Failure Semantics

- Before the osm2pgsql checkpoint, pod-local source files make the run
  non-resumable; cleanup marks the generation failed and drops its schema.
- After the osm2pgsql checkpoint, failure keeps the generation in `building`,
  records the failed phase/batch, and retains the schema.
- `osm-update resume --generation <id>` is the only way to continue retained
  work. `OSM_RESUME_GENERATION_ID` remains an environment default for the flag.
  A new generation cannot be reserved while a resumable build exists for the
  region.
- Explicit abort marks the generation failed and drops its build schema;
  ordinary errors never do this automatically after the durable boundary.
- A storage-preflight block preserves the current stage cursor and build schema,
  performs no batch mutation, consumes no retry attempt, and remains blocked
  until recorded operator approval. Repeated polling may report the block but
  must not execute the unit or clear it merely because free space later rises.

## Primary Storage Preflight

The check measures the filesystem that contains PostgreSQL `PGDATA` and
`base/pgsql_tmp`, not updater-pod scratch and not logical database size. Its
provider is fail-closed: if primary identity, PVC capacity, or filesystem free
bytes cannot be determined, the unit does not start.

Ordinary PostgreSQL SQL cannot provide filesystem free blocks. Functions such as
`pg_database_size`, `pg_tablespace_size`, and `pg_stat_file` report logical
object/file sizes, not statvfs capacity and availability. The updater must not be
granted Kubernetes `pods/exec` merely to run `df` in a PostgreSQL container.

The production provider uses the existing monitoring stack:

1. Verify the database connection is writable with `pg_is_in_recovery()=false`
   and capture `inet_server_addr()` as the authoritative primary backend.
2. Resolve that backend to the PostgreSQL pod/PVC using read-only Kubernetes
   discovery metadata or the `pg_replication_is_replica` Prometheus series. The
   production claim name is derived from `data-{{POD}}`.
3. Query Prometheus for `kubelet_volume_stats_available_bytes` and
   `kubelet_volume_stats_capacity_bytes` for that PVC.
4. Require exactly one current sample for each metric, reject negative or
   impossible values, and reject samples older than two scrape intervals.
5. Compute the threshold from measured capacity bytes; do not assume the Helm
   request or nominal PVC size equals usable filesystem capacity.

The updater receives a dedicated read-only metrics endpoint/configuration and,
if Kubernetes discovery is used, only `get/list/watch` access to the relevant
pods, endpoints, and PVC metadata. It receives no pod exec, mutation, Longhorn,
or secret-listing permission. Metrics endpoint failure, ambiguous primary/PVC
mapping, duplicate series, or stale samples blocks the unit exactly like low
space. Tests use an injected `StoragePreflight` provider rather than accessing a
real cluster.

The structured event contains at least:

```text
region_id
generation_id
stage
batch_count
cursor
postgres_primary
pvc_name
capacity_bytes
free_bytes
required_free_bytes
blocked_at
```

The catalog stores the block separately from stage failure so the UI/operator
can distinguish **Waiting for storage intervention** from a failed SQL batch.
Operator clearance records who approved the resume, when, and an optional note.
Clearance authorizes another preflight; it never bypasses the 10% threshold.
Use `osm-update status --generation <id>` to inspect the cursor and block,
`osm-update unblock --generation <id> --approved-by <operator>` to record
clearance, and `osm-update abort --generation <id> --reason <reason>` only to
permanently fail the generation and remove its build schema.

### Interactive operator CLI

Set the Kubernetes context, published updater image, and target catalog region
explicitly before invoking the operator CLI:

```sh
export KUBE_CONTEXT=xdev
export OSM_UPDATE_IMAGE=harbor.fourteeners.local/library/workouts-osm:dev-20260925
export OSM_REGION_ID=geofabrik:norcal
make osm-update
```

All three variables are required. The script prints their effective values
before contacting Kubernetes so the operator can verify the target cluster,
image, and region. It exits before creating a Job when any value is absent.

Human operators should normally use this CLI rather than constructing container
commands. It probes state through a temporary Kubernetes Job, displays the
current stage and block reason, and launches the selected follow-up Job with the
correct generation and audit fields. After storage remediation, choose
`unblock`, provide a note, then choose `resume`; these remain separate so
approval never implicitly executes database work.

Before launching a long-running `start` or `resume` replacement, the CLI deletes
all prior Jobs labeled `app.kubernetes.io/component=osm-update` in the selected
namespace and waits for their owned pods to disappear. This includes completed
and failed historical Jobs and prevents updater Jobs from accumulating or
running concurrently. Temporary probe/action Jobs are already deleted after
their output is captured.

## Automatic Region Preparation

Automatic provider-region download and map-data preparation invokes this same
updater and catalog contract. It must not use a separate one-shot import path.
Queued region preparation reports pending while a resumable generation exists,
and retries that generation from its latest committed phase or batch.

## Online Availability And Storage

`osm_active` contains views, not stored copies. The views resolve data from the
currently active generation partition for every configured region. A rebuild
uses an isolated `osm_build_<generation>` schema while the active region
partition remains readable until atomic promotion.

Normal rebuild availability is therefore:

- API, UI, Martin tiles, and the general worker remain online.
- Existing route and Coverage reads continue from the application database.
- OSM-backed diagnostics and matchers continue reading the old active generation.
- The Coverage worker may be paused or throttled to reduce PostgreSQL contention,
  but stopping it is an operational choice rather than a correctness requirement.
- Routes requiring a region with no active generation remain pending until that
  region's first generation promotes.

Taking all application services offline is not part of the normal rebuild
procedure. Promotion and active-view switching are metadata transactions.

Storage-conserving does not mean deleting active data while a candidate builds.
The primary temporarily stores both the old active region partition and the new
build schema. The pipeline limits additional amplification by using bounded
staging tables, avoiding mass in-place rewrites, minimizing WAL-producing logged
copies, attaching final tables as partitions without copying rows, and queuing
the retired region partition for GC immediately after successful promotion.
Capacity planning must still allow the active region plus one candidate region,
indexes, bounded staging, PostgreSQL WAL, and bounded temporary files. A rebuild
must stop before exhausting that budget; PVC expansion and snapshot deletion
require explicit operator authorization.

Canonical parents are partitioned by `region_id`. Each region has its own active
generation and physical `ways`, `localities`, and `path_segments` leaf tables.
Generation IDs are globally unique catalog IDs, not a database-wide version that
forces all regions to advance together. One region may remain on an older active
generation while another builds or promotes a newer generation. `osm_active`
combines all active regional partitions and applies deterministic overlap
precedence for duplicate OSM objects.
