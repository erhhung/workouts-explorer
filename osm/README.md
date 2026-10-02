# OSM Import Spike

Milestone 7 uses a full-refresh, selective import. The source PBF is filtered
with Osmium to `highway=*` ways, administrative boundary relations, and their
required references before `osm2pgsql` flexible output loads a generation-specific
candidate schema.

The current assets implement the measured NorCal import and graph derivation.
ADR 009 accepts matcher rules `coverage-experimental-v1`, adaptive sampling,
and path policy `coverage-path-policy-experimental-v82` as the production baseline.

Importer version 3 and derivation version 18 retain bounded named local, state,
and national-park polygons during generation construction and attach stable park
metadata to fully contained path segments. Local parks require an authoritative
named municipality and keep municipality-scoped IDs. State and national parks may
cover segments without a municipality; their IDs are scoped by provider region
and OSM source identity. Roads and paths inside those regional parks use the park
name as their application display context instead of municipality or county.
Connectivity scope is the selected named park when present, otherwise municipality,
otherwise county, otherwise provider region. Both `admin_level=8` municipalities
and `admin_level=6` counties are retained; the smallest covering polygon wins so
a municipality takes precedence inside its containing county. Physical segments
share an identity only when exact graph endpoints connect, normalized names match
(with one null-name sentinel), and both belong to the same road/path class.
Cycleways and footways are both paths; unnamed segments cannot bridge that
boundary. Named segments merge across graph-connected road/path transitions
and branches because the name supplies semantic identity. Named road segments
also gain bounded proximity edges within one scope/name: 15 m generally, or 50 m
when both are explicitly one-way, so divided carriageways remain one attribution
without broadly joining nearby roads. Unnamed cross-source segments merge at
simple two-segment continuations. At a larger unnamed branch, the only two
segments of one exact broad class also continue across source ways; other
cross-source branches remain separate. Segments from one OSM way remain continuous.
Park scope prevents contained geometry from bridging outside, and municipality
scope keeps cities separate. Every final identity is derived from its physical-
segment component, so park/outside, road/path, named/unnamed, and disconnected-
component boundaries remain explicit even when incoming logical IDs were shared.
For non-road geometry, a municipality-attributed clipping piece no longer than
25 m is absorbed into county scope when the immediately preceding and following
pieces of the same source segment belong to that same county and have the same
name state and exact broad class. Park-attributed pieces are never absorbed.

Named educational grounds are retained from `amenity=school|college|university`
and named `landuse=education` polygons. Only formally unnamed roads/paths receive
education tags and education-area identity scope; named campus geometry retains
its own OSM name and normal scope. Matcher copies expose the education name as
the display/path name fallback without overwriting canonical OSM `name` tags.
Park polygons are build-only and are not copied into canonical storage.
Application reconciliation converts matching segment metadata into
once-per-workout park visits. Driveway and parking geometry remains
matchable and tile-visible but is omitted from path statistics.

## Storage-Constrained Rebuild

The deployed OSM database does not have capacity for another approximately 5 GB
generation beside the current tables. When importer or derivation changes require
a full rebuild, do not use the normal build-then-promote flow on that environment.
First stop coverage matching, diagnostics, and OSM update work; preserve the
application database's copied coverage; detach and drop the current generation's
canonical data partitions plus any leftover build schemas; then import and
validate the replacement generation in place before restarting OSM-dependent
services and reconciliation. Preserve `osm_catalog` migrations, region
configuration, and provider-catalog tables; this is a data-generation rebuild,
not a catalog-schema reset.
Treat the interval as planned OSM matching downtime. Confirm available storage
before import and do not assume old tables can be deleted only after the new
generation is populated.

For the original national-park rollout on an unpatched xdev application schema
19 database, generate and review the in-place patch before publishing its images:

```sh
./scripts/xdev-national-park-coverage-hotpatch.sh > /tmp/national-park-hotpatch.sql
psql "$MIGRATION_DATABASE_URL" -v ON_ERROR_STOP=1 -f /tmp/national-park-hotpatch.sql
```

The script is intentionally not run by builds or migrations. It preserves the
19/18 schema/runtime metadata, relaxes only park-locality storage, and replaces
the affected schema-18/19 functions. Stop coverage writes while applying it.
After image publication, perform the storage-constrained replacement above with
the required importer/derivation versions, then restart OSM-dependent services
and run coverage reconciliation. Publishing images or applying the hotpatch alone
does not add national parks to an existing OSM generation.

For recovery from the failed derivation-v6 generation-9 import, use this
storage-constrained sequence:

1. Stop coverage workers, diagnostics that hold OSM snapshots, and all OSM update
   jobs. Confirm the application database still contains copied segments, matches,
   attributions, and rollups. Verify generation 9 is still `building` and
   `osm_build_9` is absent, then manually mark that catalog row `failed` with
   a bounded operator failure summary. Do not recreate or resume generation 9.
2. Publish the importer-3/derivation-6 updater containing transactional stage
   retries and the compatible worker image. No application migration or API
   signature change is required.
3. Confirm migrations through OSM catalog migration 008 are present. Do not drop
   `osm_catalog`, region/provider rows, promotion events, or timezone data. Verify
   enough free space for one complete build plus temporary and WAL growth.
4. Run one manual regional update for `geofabrik:norcal`. It must reserve generation
   10 and report importer/derivation `3/6`; abort if another generation number or
   version is reserved. Monitor edge/component table sizes, PostgreSQL temporary
   bytes, and WAL growth during `attribute-parks`.
5. Require validation to show zero provenance, source, logical-path, geometry,
   park, endpoint-index, and `remainingConnectedAttributionSplits` failures. Review
   `attributionScopeRebasedSegments`, `attributionScopeRebasedLogicalIds`,
   `attributionIdentityLogicalIdEdges`, `attributionIdentityAffectedLogicalIds`,
   `attributionIdentityMergedComponents`, total paths, segment length, schema bytes,
   and representative Yosemite and Stevens Creek/Sleeper Park identities before
   promotion. The database promotion wrapper independently rejects a nonzero split.
6. After generation 10 is active and storage GC completes, restart OSM readers and
   coverage reconciliation. Reconciliation copies generation-10 merged logical IDs
   using existing segment/API signatures and repopulates dependent application rows.
   Compare copied counts and representative entities before restoring normal scheduling.

The interval between retiring generation 8 and promoting generation 10 is planned
OSM matching downtime. Keep a database backup or volume snapshot as rollback
protection because storage constraints remove the online generation-8 rollback.

Pinned tools:

- Osmium 1.19.0
- osm2pgsql 2.3.1

The candidate schema name must match `osm_build_<generation>` and is supplied
in `OSM_BUILD_SCHEMA`. The manual updater owns the complete import and promotion
lifecycle.

`postprocess.sql` adds the meter-based geography index required by matching and
derives OSM administrative-level-8 municipal locality polygons. Park attribution
uses four index-driven endpoint joins to stage deduplicated same-road/path-class
segment edges. For unnamed groups, branch-node incidence prunes cross-source
edges when more than two eligible segments meet. Named edges remain intact.
Parent-compressed connected components contain only participating segments;
the final identities are applied by writing one logged, checkpointed replacement
segment table instead of creating millions of old/new tuple versions in place.
The indexed source is dropped when attribution commits, then
`prepare-partitions.sql` adds fixed region/generation provenance,
replaces keys, builds the canonical indexes, and gives the three promoted tables
generation-qualified names. `validate.sql` then records exact preparation,
source-version, logical-path, geometry, locality, park-kind, national-park
area, remaining connected-split, component-count, and storage gates. The updater
then calls `osm_catalog.promote_region_generation(...)` directly to atomically
detach the prior region leaves, move and attach the prepared leaves, advance
catalog state, and queue detached storage for GC.
Promotion never performs a full-table `INSERT SELECT`.

## Manual regional update

Build the dedicated amd64 importer image with `make images`, or directly with:

```sh
buildah build --file worker/osm-update.Dockerfile --tag workouts-osm:local .
```

The command contract is:

- `OSM_MIGRATION_DATABASE_URL`: catalog owner/migration PostgreSQL URL. It is
  passed to libpq as `PGDATABASE`, never as a subprocess argument.
- `OSM_REGION_ID`: configured catalog ID, for example `geofabrik:norcal`.
- `OSM_MAX_DOWNLOAD_BYTES`: positive hard limit for the source response stream.
- `OSM_UPDATE_SCRATCH`: writable directory for generation-scoped source and
  filtered PBFs.
- `OSM_PIPELINE_ROOT`: directory containing the checked-in Lua and SQL assets.

The image defaults the last two paths to `/scratch` and `/app/osm`. It is based
on the amd64 digest of `iboates/osm2pgsql:2.3.1`, copies `osmium` from the amd64
digest of `iboates/osmium:1.19.0`, and installs the PostgreSQL 18 client and CA
certificates. Startup refuses any reported Osmium or osm2pgsql version other
than 1.19.0 and 2.3.1. It runs as UID/GID 65532 and supports a read-only root
filesystem with only `/scratch` writable.

No scheduled or release-hook Helm Job is installed. Updates are deliberately
operator-initiated and region-specific. Here, "operator" means the human with
Kubernetes and storage authority, normally the administrator running this
procedure. The example Job has an 8 GiB bounded `emptyDir`, at least 4 GiB
memory, and no retries.

Prefer the interactive operator CLI. It launches a short-lived status Job,
displays the current stage, cursor, failure, and storage block, and offers only
actions valid for that state. Long-running `start` and `resume` Jobs remain in
the namespace for monitoring; short `status`, `unblock`, and `abort` Jobs are
removed after their output is captured. A new `start` Job reserves import
capacity: 2 CPU, 3 GiB memory, and 2 GiB ephemeral storage. A `resume` Job is
valid only after the durable `osm2pgsql` checkpoint, so its database-heavy
stages request only 100m CPU, 256 MiB memory, and 64 MiB ephemeral storage.
Short operator Jobs request 10m CPU, 64 MiB memory, and 16 MiB ephemeral storage.
All profiles retain higher limits where useful, and status inspection remains
schedulable while PostgreSQL performs the rebuild work.

```sh
export KUBE_CONTEXT=xdev
export OSM_UPDATE_IMAGE=harbor.fourteeners.local/library/workouts-osm:dev-20260925
export OSM_REGION_ID=geofabrik:norcal

make osm-update
```

The CLI defaults the namespace, Secret names, image pull Secret (`workouts-harbor`),
database key, and one-GiB download limit to the values shown in
`osm/manual-update-job.yaml`. Override `OSM_NAMESPACE`,
`OSM_IMAGE_PULL_SECRET`, `OSM_DATABASE_SECRET`, `OSM_DATABASE_KEY`,
`OSM_DATABASE_TLS_SECRET`, or `OSM_MAX_DOWNLOAD_BYTES` when the deployment uses
different values. It does not need a database URL on the workstation because
the probe and action commands read the existing Kubernetes Secret inside their
Jobs.

The Job checks for `/app/osm-operator-v1` before invoking the updater. Images
built before the interactive action protocol therefore fail the status probe
instead of accidentally starting a rebuild.

Immediately before creating a long-running `start` or `resume` Job, the CLI
deletes prior Jobs labeled `app.kubernetes.io/component=osm-update` and waits for
their pods to terminate. Completed, failed, and superseded updater Jobs therefore
do not accumulate, and a replacement is not launched alongside an older Job.

For scripted/manual rendering, export all placeholders in
`osm/manual-update-job.yaml`, including `OSM_UPDATE_ACTION` and command-specific
values, then pass it through `envsubst` as before.

### Operator monitoring procedure

Treat an OSM rebuild and its application reconciliation as one supervised
operation. Do not start the Job and leave an unbounded `kubectl wait` running.

Do not expand PostgreSQL PVCs without prior operator authorization. If free
space becomes low during a rebuild, first inspect the affected Longhorn volumes
for old snapshots and report their reclaimable storage. Suggest deleting
unneeded old snapshots to free space, but do not delete snapshots or resize PVCs
without explicit authorization. If authorized cleanup cannot provide sufficient
headroom, pause or stop the rebuild safely and request a storage decision rather
than expanding a claim unilaterally.

Before every resumable unit, verify free space on the PostgreSQL primary
filesystem. Require at least 10% of usable PVC capacity at unit start (3 GiB for
a 30 GiB PVC). If the threshold is not met, the updater must stop before running
unit SQL, log and notify the measured capacity/free-space reason, mark the unit
operator-blocked, and disable automatic retry. Free space may temporarily fall
below the threshold during a running unit, but cleanup must finish and the
preflight must pass again before the next unit. Resume requires explicit operator
clearance after manual intervention; clearance does not waive the threshold.

1. During the first minute after creating the `osm-update` Job, use either a
   `kubectl wait` bounded to at most one minute or follow the Job logs for at
   most one minute. Confirm that the pod reaches Running/Ready, reports the
   expected Osmium and osm2pgsql versions, and reserves exactly one new `building`
   generation with the expected importer and derivation versions.

   ```sh
   kubectl --context "$KUBE_CONTEXT" wait --for=condition=Ready pod \
     -l job-name="$OSM_JOB_NAME" --timeout=1m -n workouts-explorer
   timeout --signal=INT 1m kubectl --context "$KUBE_CONTEXT" logs \
     -f "job/$OSM_JOB_NAME" --since=1m --timestamps -n workouts-explorer
   ```

2. In that same startup check, inspect `osm_catalog.generations`,
   `pg_stat_activity`, and PostgreSQL blockers. A previous execution must not
   leave a stale `building`/`validating` generation, session, transaction, or
   database lock that prevents the new execution from starting normally. Resolve
   stale state deliberately before continuing; never launch a second updater to
   work around it.
3. Monitor the long rebuild using repeated waits or log-following intervals of at
   most ten minutes. After every interval, verify expected stage progress in the
   logs and catalog, inspect active PostgreSQL work and blockers, and check primary
   storage plus replication health. A heartbeat without expected database work,
   an unchanged stage beyond its normal duration, or shrinking storage headroom
   requires investigation before another interval.

   ```sh
   timeout --signal=INT 10m kubectl --context "$KUBE_CONTEXT" logs \
     -f "job/$OSM_JOB_NAME" --since=1m --timestamps -n workouts-explorer
   ```

4. After the Job completes, independently verify the promoted generation,
   validation report, importer/derivation versions, canonical row counts, target
   attribution, and absence of residual build schemas. Confirm storage GC removed
   retired-generation data, remove the prior-generation operational backup only
   after the new generation is verified, and ensure PostgreSQL storage and
   replication are healthy before starting application reconciliation.
5. If Coverage reconciliation is required, maintain one rolling page of active
   Coverage route jobs; the current operator page size is 25. Use repeated waits
   of at most five minutes and inspect parent plus route-child status after every
   interval. At each check, selectively retry the latest **Failed** or
   **Partially completed** parents first, count the queued/running route jobs
   created by those retries, then enqueue new routes until exactly one page (25
   jobs at the current setting) is queued or running. This keeps workers utilized
   while bounding the active backlog. Do not wait for the final job in the prior
   window to finish before topping the window back up.

   ```sh
   timeout --signal=INT 5m kubectl --context "$KUBE_CONTEXT" logs \
     -f deployment/workouts-explorer-coverage-worker --since=1m \
     --timestamps -n workouts-explorer
   ```
6. Retry only the latest parent in each lineage with status **Failed** or
   **Partially completed** through the normal selective retry mechanism; an older
   unsuccessful parent with a newer retry is historical and must not be retried
   again. Recheck retries in at-most-five-minute intervals, refill the active
   window after accounting for them, and continue until every eligible route has
   updated successfully or its supported retries are exhausted. Only then declare
   reconciliation complete.

Record generation IDs, Job IDs, validation counts, retries, backup cleanup, and
final storage/replication health in the operator report.

### Resuming a failed database stage

After `osm2pgsql` completes, every stage transition is recorded in
`osm_catalog.generation_stages`. A later failure keeps the generation in
`building`, retains its build schema, and marks only the failed stage. Do not
start a new generation. Resume with an image containing the same fenced SQL as
the retained stage, inspect the retained cursor, and recreate the update Job with
an explicit resume command. If stage SQL must change, its checksum fence rejects
mixing old and new batch output; explicitly abort that generation and start a
new rebuild instead.

```sh
/app/osm-update status --generation <generation-id>
/app/osm-update resume --generation <generation-id>
```

The operator discovers the Prometheus Service ClusterIP from the `homelab`
context and injects it as a host alias for the unauthenticated internal endpoint
at `https://prometheus-stack-prometheus.monitoring.svc.cluster.local:9090`.
`SSL_CERT_FILE` points to the already mounted homelab CA. The manual Job defaults
`OSM_PROMETHEUS_PVC_TEMPLATE` to `data-{{POD}}` and
`OSM_PROMETHEUS_MAX_SAMPLE_AGE` to two minutes. The updater queries
`pg_replication_is_replica` to map the SQL backend address to the current primary
pod, substitutes that pod into the PVC template, then independently queries raw
`kubelet_volume_stats_available_bytes` and
`kubelet_volume_stats_capacity_bytes` samples for the derived claim. It rejects
missing, stale, or duplicate mappings instead of aggregating them.
DNS, connection, timeout, HTTP 429, HTTP 5xx, and an empty primary-address
mapping are retried four times after 5, 10, 30, and 60 seconds. Ambiguous or
invalid mappings, missing or stale storage metrics, primary changes, and
insufficient storage are not retried.

Updater logs display timestamps using the `America/Los_Angeles` timezone, and
bracket each executable batch with searchable start/completion banners containing
the generation ID, derivation version, durable stage name, and batch elapsed time.

For every batch covered by PostgreSQL storage preflight, the updater logs the
primary PVC capacity and free space immediately after the start banner. It
samples storage while the batch runs, then waits for a fresh post-transaction
Prometheus sample and logs the observed pre-cleanup low-water mark and post-GC
free space immediately before the completion banner. The node in parentheses is
the Kubernetes node exporting the PVC metric:

```text
PVC: data-postgresql-postgresql-0 (k8s4) | capacity: 29.94 GiB | free: 7.29 GiB (24%)
PVC: data-postgresql-postgresql-0 (k8s4) | pre-GC: 2.38 GiB | post-GC: 7.03 GiB (23%)
```

Propagation uses phase-specific batch sizes because hook updates are much more
expensive than compression scans: 100,000 hook roots, 1,000,000 compression
rows, and 500,000 assignment rows. The controller runs component-table vacuum
maintenance after a propagation batch only when that batch's sampled PVC
low-water observation is below 15% free; at 15% or more it skips vacuum overhead.
Propagation cursors also persist the next batch ordinal and the pass total. The
start and completion banners render `(N of M)`, and the ordinal resets to 1 each
time convergence begins a new compression or hook pass.

If status reports an active storage block, reclaim or explicitly authorize
storage first, then record approval and resume separately:

```sh
/app/osm-update unblock --generation <generation-id> \
  --approved-by "$USER" --note "snapshot cleanup completed"
/app/osm-update resume --generation <generation-id>
```

Normally, do not run those container commands directly. After resolving an
insufficient-storage incident, rerun `make osm-update`, choose
**Unblock**, enter a remediation note, then choose **Resume** on the next prompt.
`unblock` records who approved the intervention and what changed. `resume`
launches the retained build and immediately performs a new storage preflight;
it still refuses to execute SQL if the claim remains below 10% free.

`unblock` records operator approval but does not execute SQL. `resume` starts the
retained controller, which then runs a fresh preflight and continues from the
committed cursor. During a healthy run the controller advances directly from one
committed batch to the next; `resume` is only needed after the process exits.
Clearance never bypasses preflight. To permanently discard retained work, use
the separately locked destructive command:

```sh
/app/osm-update abort --generation <generation-id> \
  --reason "operator-approved rebuild replacement"
```

Resume verifies the generation is still building, the schema exists, pinned tool
versions and importer/derivation versions match, and `osm2pgsql` has a completed
checkpoint. It skips completed stages and reruns the first incomplete or failed
stage. Validation is always rerun before promotion. Download, filtering, and
`osm2pgsql` remain non-resumable because their source files are pod-local; a
failure before the `osm2pgsql` checkpoint is cleaned up and requires a new
generation. Operator cancellation after that checkpoint is resumable and must not
drop the build schema.

Before catalog cleanup, reservation, or resume, the updater acquires one
database-wide advisory lock. The lock permits only one updater across all
regions. Once it owns that lock, it terminates stale PostgreSQL backends whose
application name is `workouts-osm-update`, then verifies that no conflicting
table or row locks remain on `osm_build_*`, `generation_stages`, or
`generation_storage_blocks`. It never terminates unrelated API, tile, migration,
or HA sessions; a conflicting foreign lock fails startup for operator review.

The coarse stage ledger is supplemented by restart-safe keyset batch cursors for
long clip, attribution, graph, and rewrite phases. The normative performance and
failure contract is `docs/osm-resumable-rebuild.md`. Batches target 10-15 minutes
on the production reference cluster and may not be deliberately sized above 15
minutes without an internal durable checkpoint, limiting recovery loss to the
current uncommitted batch. Automatic region preparation uses the same updater
and resume contract.

A **stage** is the durable catalog phase and SQL fence. A **batch** (or **unit**)
is one cursor-advancing transaction within that stage. Log banners bracket
batches, so a batched stage can emit the same banner name multiple times before
its stage row becomes `completed`.

One `building`/`validating` generation per region is enforced globally by the
catalog's partial unique index. The command transactionally allocates its ID and
`osm_build_<id>` schema, records source bytes, SHA-256, header timestamp, and
exact tool versions, then runs reference-complete filtering, reference checking,
flex import, postprocessing, derivation, clipping, partition preparation, and
validation. Only a passing validation report moves to `validating` and invokes
the schema-4 promotion function.

Before the durable `osm2pgsql` checkpoint, failure stores a bounded summary and
drops the candidate schema. After that checkpoint, ordinary stage failures and
storage blocks retain the candidate for explicit resume or abort. After
promotion, detached leaves and the residual build
schema are processed through `storage_gc`. A GC failure is reported but never
fails the active generation; `failed` and `queued` GC records are retried at the
start and end of a later invocation.

Every mutating PostgreSQL unit commits its output and stage checkpoint in one
transaction. The updater never blindly retries a batch after a connection loss,
because the commit result may be ambiguous; a later `resume` reloads the durable
cursor and either advances or safely repeats the uncommitted unit. Read-only
validation may retry recognized transient connections up to five attempts with
5, 15, 30, and 60 second delays. Syntax, constraint, and validation failures do
not retry. Diagnostics are bounded, redact PostgreSQL URLs, stream command output,
and emit one-minute heartbeats for long-running units.

A pod crash before `osm2pgsql` remains non-resumable because its PBFs are stored
in `emptyDir`. After the durable import checkpoint, PBF loss is harmless: all
remaining work and cursors are in PostgreSQL, so the operator recreates the Job
with `resume` rather than failing the generation or deleting its build schema.

`derive-batched.sql` builds the physical graph in durable source-way batches to
avoid global sort, temporary-file, and shared-memory spikes. The checkpointed
`clip-localities-*.sql` stages rewrite only municipal-boundary candidates at
ordered source-line fractions.
Pieces that cannot be covered by one locality within 1 cm retain matchable
geometry but abstain from municipal attribution.

## Identity rule evaluation

`/app/osm-identity-eval` evaluates candidate attribution rules without promoting
or retiring canonical data. It reserves an `evaluating` catalog scratch generation,
which does not consume the region's single production `building` slot. By default
it copies active canonical segments for Cupertino, Mountain View, Sunnyvale, and
Saratoga, plus complete source ways named by the checked-in regression corpus,
into a uniquely named logged scratch schema, preserves their existing park/locality
attribution, runs the exact checkpointed production identity stages with storage
preflight before scratch reservation, every setup mutation, and every stage unit.
A normal run drops its schema before marking the scratch generation failed-by-design.
A crashed evaluator or failed schema cleanup remains `evaluating`, can be inspected
by generation ID, and can be aborted without blocking a production rebuild.
`--keep-schema` deliberately leaves that recoverable `evaluating` record. It reports
baseline versus candidate component counts, the largest candidate components, and
the checked-in Stevens Creek, Meteor Drive, and Cupertino branch regressions.

When `MIGRATION_DATABASE_URL` is provided, the evaluator also maps existing
application physical-segment matches through candidate IDs and reports aggregate
entity/workout-attribution splits and merges. It never writes application data or
creates Coverage jobs. Use `--without-application-projection` for an OSM-only run,
`--keep-schema` for manual inspection, or `--full-region` with an explicitly large
`--max-segments` value for an operator-only regional smoke test.

Render `osm/manual-identity-eval-job.yaml` with an explicit temporary evaluator
image, secret keys, locality list, and segment limit. Evaluator stdout is one JSON
report; stage progress goes to stderr. Any failed known regression or retained
required-edge split makes the command exit nonzero after writing the report.

## Timezone boundaries

Workout timezone resolution uses the `timezone-boundary-builder` combined
GeoJSON release, licensed under the Open Database License (ODbL) 1.0. The Helm
chart pins the release URL and SHA-256 digest. A pre-deployment job downloads
that immutable artifact, streams its polygons into versioned OSM catalog
tables, validates them with PostGIS, and atomically promotes the completed
dataset. Workout ingestion and backfill then query only the local OSM database;
they do not call a geocoding or timezone service.

The default artifact is release `2026c` `timezones.geojson.zip`. To import a
previously downloaded artifact manually:

```sh
OSM_MIGRATION_DATABASE_URL=postgresql://... \
TIMEZONE_BOUNDARY_ARCHIVE=/data/timezones.geojson.zip \
TIMEZONE_BOUNDARY_RELEASE=2026c \
TIMEZONE_BOUNDARY_SOURCE_URL=https://github.com/evansiroky/timezone-boundary-builder/releases/download/2026c/timezones.geojson.zip \
TIMEZONE_BOUNDARY_SHA256=7d3f0c5a33b6acd891335c0ad5ba767736b6914cb1a1d68c71921c17ce358948 \
go run ./worker/cmd/timezone-import
```

Promotion is idempotent for an already-active release and digest. Geometry from
the previously active release is removed only after the replacement is active;
its provenance row remains retired in the catalog.
