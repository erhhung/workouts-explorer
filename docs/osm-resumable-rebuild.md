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
an observational `validate` stage changes after validation completed, native
resume may reset and rerun that entire stage under the corrected checks. Running
validation cannot change fences mid-scan, and all geometry-producing stages stay
immutable. Facility-crossing checks exclude only lines wholly within a
`1e-9`-degree boundary buffer (less than a fifth of a millimeter), avoiding
floating-point false crossings without ignoring real inside/outside portions.

For geometry-producing stages, if the SQL for
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
- Operator Jobs immediately continue when fresh post-GC metrics show at least
  10% free. Only low post-batch readings enter a five-second polling loop, bounded
  by `OSM_STORAGE_SETTLE_TIMEOUT` (default maximum `90s`); polling stops as soon as
  space recovers. Kubelet volume values can remain cached across fresh Prometheus
  scrapes, so this gives temporary-file cleanup time to reach those values
  without imposing a fixed per-batch wait. Cancellation interrupts polling. A
  persistent low reading remains low for the next unit's normal starting-space
  check, and a recorded block still requires an explicit operator unblock.
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
6. Clip locality/park and unnamed education-boundary candidate discovery,
   checkpointed by source segment UUID keyset.
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

The final column is also shown as task help in the progress dialog. Keep each
description in plain language and no more than two sentences; technical batch
limits and recovery details belong in the surrounding specifications.

### Source preparation and import

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 1 | `prepare-source` | Once; maintenance checked again on resume | Check the required tools and prepare a workspace, including any leftover cleanup. Reuse completed database upkeep when the installed map data has not changed. |
| 2 | `download` | Once | Download the region's map file and check that it arrived intact. Record its size and the date of the map data. |
| 3 | `tags-filter` | Once | Keep the roads, paths and area details needed for coverage processing. Remove unrelated map features to reduce the remaining work. |
| 4 | `check-refs` | Once | Check that the filtered map file includes the points and features its roads and boundaries refer to. |
| 5 | `osm2pgsql` | Once | Load the filtered map data into the database. After this step, processing can resume without repeating the import. |
| 6 | `postprocess` | Once | Prepare city boundaries, parks and school grounds so roads and paths can be assigned to the right areas. |

### Compact derivation

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 7 | `derive/init` | Once | Prepare to divide roads and paths into smaller sections and identify how they connect. |
| 8 | `derive/node-counts` | Repeated, 150,000 source ways | Count how many roads and paths use each map point to find shared connections. |
| 9 | `derive/shared-nodes` | Once | Identify points shared by several roads or paths so junctions can be recognized correctly. |
| 10 | `derive/segments` | Repeated, 100,000 source ways | Divide roads and paths into sections between junctions. Group related sections as parts of the same road or path. |
| 11 | `derive/path-primary` | Once | Give each road or path section a unique reference so it can be found reliably. |
| 12 | `derive/source-unique` | Once | Check that each section of an original road or path is stored only once. |
| 13 | `derive/geography-index` | Once | Speed up searches for roads and paths near a location. |
| 14 | `derive/start-index` | Once | Speed up finding sections that begin at a particular map point. |
| 15 | `derive/end-index` | Once | Speed up finding sections that end at a particular map point. |
| 16 | `derive/logical-index` | Once | Speed up finding all sections belonging to the same road or path. |
| 17 | `derive/locality-index` | Once | Speed up finding roads and paths within a city or other local area. |
| 18 | `derive/logical-locality-index` | Once | Speed up searches by area, road or path name, and type. |
| 19 | `derive/finalize` | Once | Finish preparing the road and path sections and remove temporary data that is no longer needed. |

### Locality clipping and area attribution

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 20 | `clip-candidates` | Repeated, 200,000 segment UUIDs | Find sections that cross city or other area boundaries, including parks and school grounds. |
| 21 | `clip-replacements` | Repeated, 7,500 candidate UUIDs | Split sections where they cross area boundaries so each piece can be assigned to the right area. Keep the original order and connections intact. |
| 22 | `clip-apply` | Repeated, 18,000 source UUIDs | Insert the newly split sections and remove the originals without leaving duplicates or gaps. |
| 23 | `clip-residual` | Repeated, 10,000 segment UUIDs | Correct sections that were assigned to an area even though they lie outside it. |
| 24 | `clip-finalize` | Once | Update the road and path groups to include the new pieces and remove temporary work data. |
| 25 | `attribute-parks-tags` | Repeated, 750,000 segment UUIDs | Mark sections within parks and keep their park names and details. |
| 26 | `attribute-education` | Repeated, 750,000 segment UUIDs | Mark sections within school and university grounds and keep the relevant area details. |
| 27 | `attribute-slivers/discover` | Repeated, 500,000 segment UUIDs | Find very short sections whose area assignments disagree with their matching neighbors. These can result from small differences in map boundaries. |
| 28 | `attribute-slivers/apply` | Repeated, 500,000 sliver UUIDs | Correct inconsistent area assignments on very short sections when their matching neighbors agree. |

### Identity graph construction

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 29 | `identity-segments` | Repeated, 500,000 segment UUIDs | Prepare the names, types, directions and connection points needed to decide which sections belong to the same road or path. |
| 30 | `identity-edges/orientation-1` | Repeated, 500,000 left-segment UUIDs | Find sections connected at their starting points. |
| 31 | `identity-edges/orientation-2` | Repeated, 500,000 left-segment UUIDs | Find connections from one section's start to another's end. Remove temporary processing data that is no longer needed. |
| 32 | `identity-edges/orientation-4` | Repeated, 500,000 left-segment UUIDs | Find sections connected at their ending points. |
| 33 | `identity-edges/finalize` | Once | Finish preparing the connections between sections so they can be searched efficiently. |
| 34 | `identity-proximity/roads` | Repeated, 500,000 identity-segment UUIDs | Prepare named sections for checks that link nearby parts of the same road or path. |
| 35 | `identity-proximity/candidates` | Repeated, 500,000 named-road UUIDs | Find nearby sections that may belong to the same road and choose the best matches. |
| 36 | `identity-proximity/edges` | Repeated, 500,000 winner UUIDs | Connect the selected nearby sections and remove temporary comparison data. |

### Components and propagation

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 37 | `identity-components/branch-nodes` | Once | Find junctions where more than two sections meet. |
| 38 | `identity-components/branch-classes` | Once | Identify pairs of sections that continue the same kind of road or path through a junction. |
| 39 | `identity-components/filter-edges` | Repeated, 750,000 edge keys | Remove misleading connections between unnamed roads at junctions while preserving clear continuations. |
| 40 | `identity-components/components-left` | Repeated, 2,000,000 edge keys | Collect the first section from each connection to start forming road and path groups. |
| 41 | `identity-components/components-right` | Repeated, 2,000,000 edge keys | Collect the second section from each connection so both sides are included in the road and path groups. |
| 42 | `identity-components/component-index` | Once | Prepare the groups so related sections can be joined quickly. Remove temporary junction and nearby-road data. |
| 43 | `identity-propagate/compress` | Repeated, 1,000,000 components | Simplify the links within each road or path group. This may repeat as related groups are combined. |
| 44 | `identity-propagate/hook` | Repeated, 100,000 roots | Combine groups that are connected parts of the same road or path. Repeat the grouping checks until no more groups need joining. |
| 45 | `identity-propagate/assign` | Repeated, 500,000 components | Give all sections in each completed group the same road or path reference. |
| 46 | `identity-label-overrides/init` | Once | Prepare to check road and path names within each area. |
| 47 | `identity-label-overrides/labels` | Repeated, 250,000 segment UUIDs | Collect the names and total lengths of connected sections within each area. |
| 48 | `identity-label-overrides/neighbors` | Once | Merge a short section of 25 meters or less into a single matching neighboring road or path. Use matching names, location and connections to avoid joining unrelated sections. |

`identity-propagate/compress` and `identity-propagate/hook` form a convergence
loop, so their order can repeat as:

```text
compress pass -> hook pass -> compress pass -> hook pass -> ... -> assign
```

The cursor's `iteration`, `changed`, and `last_segment_id` fields show progress
within that loop. Phase-specific batch sizes are chosen independently because a
hook update is substantially more expensive than a compression scan.

### Rewrite, validation, and promotion

| Step | Batch name | Frequency | Work and completion boundary |
| ---: | --- | --- | --- |
| 49 | `identity-rewrite` | Repeated, 250,000 segment UUIDs | Save the final road and path groups and check that every section is included. Replace the temporary results only after those checks pass. |
| 50 | `prepare-partitions` | Once | Prepare the processed map data for safe use, including checks that it belongs to the correct region and original map version. |
| 51 | `validate/init` | Once | Prepare the final checks and record the totals expected for this region. |
| 52 | `validate/ways` | Once | Check that the original roads and paths have valid shapes, complete source details and reasonable lengths. |
| 53 | `validate/segments` | Once | Check that each section has a valid shape, source version and road or path group. Also check section lengths for inconsistencies. |
| 54 | `validate/logical-paths` | Repeated, 50,000 logical-path UUIDs | Check that each road or path group's shape and total length agree with its individual sections. |
| 55 | `validate/locality-residuals` | Repeated, 100,000 segment UUIDs | Check for sections that extend outside their assigned city or other local area. |
| 56 | `validate/parks` | Once | Check that park assignments and names agree with the map boundaries. |
| 57 | `validate/education` | Once | Check that school and university ground assignments agree with the map boundaries. |
| 58 | `validate/finalize` | Once | Review the final checks and make the region available for coverage processing if they pass. Remove old and temporary map data and finish the remaining maintenance. |

After the final logged batch, the controller performs three orchestration steps
that are not batch SQL files and therefore do not have batch banners:

1. Mark the candidate build `validating` with its validation report.
2. Atomically promote the candidate and retire the previous active generation.
3. Process detached retired-generation storage and residual build-schema GC.

The most reliable high-level progress indicator is the current stage's position
in this inventory. Within a multi-phase stage, use `cursor.phase`; within a
repeated phase, use the keyset cursor and `batch_count`. Propagation is the one
non-linear section because convergence can require multiple compress/hook
iterations before assignment.

## Production Batch Sizing

build ID 52 provides the reference timings for the 30 GiB two-replica
PostgreSQL cluster. Initial batch sizes are deliberately conservative and must be
tuned toward 10-15 minutes. Reduce a batch before production whenever an observed
run exceeds 15 minutes; a naturally bounded phase that completes in less than 10
minutes does not need artificial delay or enlargement:

| Operation | Build-52 observation (not promoted) | Initial resumable unit |
| --- | --- | --- |
| Compact derivation | build ID 55 node batches took 19-35m at 400,000 ways; first 400,000-way segment batch exhausted over 7 GiB of temporary space | 150,000 ways for node counts; 100,000 ways for segments; one index per unit |
| Clip candidate discovery | 4,455,314 segments in 3h35m | 200,000 segment UUIDs |
| Clip replacement construction | 42,048 candidates in 59m | 7,500 candidate UUIDs |
| Clip replacement insertion | NorCal generation 17 (build ID 60) processed 35,000 rows in 21m45s and the remaining 7,079 rows in 5m25s | 18,000 replacement UUIDs |
| Outside-locality correction | NorCal generation 17 (build ID 60) processed its first 100,000 segments in 1h46m58s | 10,000 segment UUIDs |
| Park attribution | 103,454 updates in 54m after full spatial scan | 750,000 source segment UUIDs |
| Education attribution | 109,492 updates in 37m after full spatial scan | 750,000 source segment UUIDs |
| Connected edge orientation | each orientation completed in 2-5m | one orientation plus 500,000 source segments if needed |
| Connected start/end edge orientation | NorCal generation 16 (build ID 56) whole-table orientation took 38m and left 4% free | 500,000 left-segment UUIDs; omit symmetric duplicate end/start orientation |
| Named-road proximity | 1,551,186 named roads and 719,056 edges in 21m | 500,000 left-segment UUIDs |
| Branch filtering/setup | approximately 4m | one durable phase |
| Branch filtering/component setup | NorCal generation 17 (build ID 60) filter batches at 250,000 rows took at most 3m42s; 500,000-row left/right population batches took at most 2m35s | 750,000 edge-filter rows; 2,000,000 left/right population rows; drop branch/proximity tables before propagation |
| Component propagation | NorCal generation 17 (build ID 60) 400,000-root hook batches had a 20m median and 51m34s maximum; 1,000,000-row compression scans ranged from 1s to 12m57s | 100,000 hook roots; 1,000,000 compression rows; 500,000 assignment rows; vacuum only when the in-batch PVC low-water sample is below 15% free |
| Final component assignment | 3,798,669 rows in 12m | one durable phase |
| Segment rewrite | NorCal generation 16 (build ID 56) reached 9% free after 2.25m of 4.5m replacement rows | 250,000 segment UUIDs using indexed nested probes; drop completed edge graph and component parent index before rewrite; retain canonical ways through promotion |
| Validation | NorCal generation 16 (build ID 56) monolithic validation took 1h34m48s | separate ways/segments/park/education units; 50,000 logical paths and 100,000 locality residual segments per batch; always rerun all groups before promotion |

After the final start/end identity orientation, the pipeline drops candidate-only
indexes and import intermediates whose last consumers have completed. This
includes old path/way lookup indexes, raw park/education import tables, the
start-node identity index, and the pre-identity logical-path aggregate. Canonical
indexes and logical-path aggregates are rebuilt by rewrite and partition
preparation. NorCal generation 16 (build ID 56) required this cleanup manually because it reached the
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

The administrator controller distinguishes **Pause** from **Cancel**. Pause uses
the retained-work semantics below and requeues a compatible Resume behind other
work. Cancel explicitly aborts only the request-bound unfinished candidate after
stopping; Canceling persists through cleanup and does not affect active data.
The standalone CLI still retains post-import work on ordinary cancellation;
managed discard is authorized only by the authenticated admin request. See
[Administrator Region Updates](osm-region-updates.md).

- Before the osm2pgsql checkpoint, pod-local source files make the run
  non-resumable; cleanup marks the generation failed and drops its schema.
- After the osm2pgsql checkpoint, failure keeps the generation in `building`,
  records the failed phase/batch, and retains the schema.
- `osm-update resume --build <build-id>` is the only way to continue retained
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
Use `osm-update status --build <build-id>` to inspect the cursor and block,
`osm-update unblock --build <build-id> --approved-by <operator>` to record
clearance, and `osm-update abort --build <build-id> --reason <reason>` only to
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

Today, human operators should use this CLI rather than constructing container
commands. The accepted [M10 admin UI](admin-ui-spec.md#osm-maps) will make routine
post-installation operations available through OSM Maps while retaining the CLI
for operator recovery. It reuses this lifecycle rather than introducing a second
importer. The CLI probes state through a temporary Kubernetes Job, displays the
current stage and block reason, and launches the selected follow-up Job with the
correct generation and audit fields. After storage remediation, choose
`unblock`, provide a note, then choose `resume`; these remain separate so
approval never implicitly executes database work.

The operator's writer endpoint must keep every connection and checkpoint read on
the PostgreSQL primary. A query-splitting proxy can return a stale just-written
stage or acquire the updater advisory lock on a standby. Use a direct-primary
endpoint or verified Job-local primary routing, retaining `sslmode=verify-full`
and a hostname covered by the server certificate. Re-discover and verify the
primary before replacement/resume Jobs after a failover. Operator writer routing
is independent of the application's reader selectors and replica-only policy.

Before launching a long-running `start` or `resume` replacement, the CLI deletes
all prior Jobs labeled `app.kubernetes.io/component=osm-update` in the selected
namespace and waits for their owned pods to disappear. This includes completed
and failed historical Jobs and prevents updater Jobs from accumulating or
running concurrently. Temporary probe/action Jobs are already deleted after
their output is captured.

The planned [internal OSM job controller](admin-ui-spec.md#dedicated-osm-job-controller)
must use durable task/Job reconciliation and delete only its own executions; the
CLI's broad historical updater-Job cleanup is not its cleanup algorithm. Both
paths must honor the same catalog ownership and database-wide lock so an operator
cannot unknowingly race a controller execution. Controller and Kubernetes RBAC/
template changes are M10 implementation work, not present behavior claimed here.

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
Generation numbers are sequential successful-promotion numbers per region, starting
at1. Only a successful promotion advances a region's generation. A global build ID
is a separate opaque identifier allocated to a build attempt, including failed or
canceled attempts. Logs pair them as `generation <number>; build <id>` once promoted.
Legacy physical names such as `osm_catalog.generations.id`, `generation_id`,
`osm_build_<id>` and the deprecated CLI `--generation` alias carry build IDs, not
regional generation numbers; use `--build <build-id>` in operator commands.
One region may remain on its current generation while another successfully
promotes its next generation. `osm_active`
combines all active regional partitions and applies deterministic overlap
precedence for duplicate OSM objects.
