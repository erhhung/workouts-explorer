# OSM Import Spike

Milestone 7 uses a full-refresh, selective import. The source PBF is filtered
with Osmium to `highway=*` ways, administrative boundary relations, and their
required references before `osm2pgsql` flexible output loads a generation-specific
candidate schema.

The current assets implement the measured NorCal import and graph derivation.
ADR 009 accepts matcher rules `coverage-experimental-v1`, adaptive sampling,
and path policy `coverage-path-policy-experimental-v82` as the production baseline.

Importer version 3 and derivation version 16 retain bounded named local and
national-park polygons during generation construction and attach stable park
metadata to fully contained path segments. Local parks require an authoritative
named municipality and keep municipality-scoped IDs. National parks may cover
segments without a municipality; their IDs are scoped by provider region and OSM
source identity, and their accepted area range is 1 km2 through 100,000 km2.
Connectivity scope is the selected named park when present, otherwise municipality,
otherwise county, otherwise provider region. Both `admin_level=8` municipalities
and `admin_level=6` counties are retained; the smallest covering polygon wins so a
municipality takes precedence inside its containing county. Physical segments share an identity only when exact
graph endpoints connect, normalized names match (with one null-name sentinel),
and both belong to the same road/path class. Cycleways and footways are both
paths; unnamed segments cannot bridge that boundary. Named segments merge across
graph-connected road/path transitions and branches because the name supplies
semantic identity. Named road segments also gain bounded proximity edges within
one scope/name: 15 m generally, or 50 m when both are explicitly one-way, so
divided carriageways remain one attribution without broadly joining nearby roads. Unnamed cross-source
segments merge at simple two-segment continuations. At a larger unnamed branch,
the only two segments of one exact broad class also continue across source ways;
other cross-source branches remain separate. Segments from one OSM way remain continuous.
Park scope prevents contained geometry from bridging outside, and municipality
scope keeps cities separate. Every final identity is derived from its physical-
segment component, so park/outside, road/path, named/unnamed, and disconnected-
component boundaries remain explicit even when incoming logical IDs were shared.
For non-road geometry, a municipality-attributed clipping piece no longer than 25 m
is absorbed into county scope when the immediately preceding and following pieces
of the same source segment belong to that same county and have the same name state
and exact broad class. Park-attributed pieces are never absorbed.

Named educational grounds are retained from `amenity=school|college|university`
and named `landuse=education` polygons. Only formally unnamed roads/paths receive
education tags and education-area identity scope; named campus geometry retains
its own OSM name and normal scope. Matcher copies expose the education name as the
display/path name fallback without overwriting canonical OSM `name` tags.
Park polygons are build-only and are not copied into canonical storage.
Application reconciliation converts matching segment metadata into once-per-
workout park visits. Driveway and parking geometry remains matchable and tile-
visible but is omitted from path statistics.

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
the required importer/derivation versions, then restart OSM-dependent services and run coverage
reconciliation. Publishing images or applying the hotpatch alone does not add
national parks to an existing OSM generation.

For recovery from the failed derivation-v6 generation-9 import, use this
storage-constrained sequence:

1. Stop coverage workers, diagnostics that hold OSM snapshots, and all OSM update
   jobs. Confirm the application database still contains copied segments, matches,
   attributions, and rollups. Verify generation 9 is still `building` and
   `osm_build_9` is absent, then manually mark that catalog row `failed` with a
   bounded operator failure summary. Do not recreate or resume generation 9.
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
   using existing segment/API signatures and repopulates dependent application
   rows. Compare copied counts and representative entities before restoring normal
   scheduling.

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
segment edges. For unnamed groups, branch-node incidence prunes cross-source edges
when more than two eligible segments meet. Named edges remain intact. Parent-
compressed connected components contain only participating segments;
the final identities are applied by writing one unlogged replacement segment table
instead of creating millions of old/new tuple versions in place. The indexed source
is dropped when attribution commits, then `prepare-partitions.sql` converts the
compact replacement to logged storage, adds fixed region/generation provenance,
replaces keys, builds the canonical indexes, and gives the three promoted tables
generation-qualified names. `validate.sql` then records exact preparation,
source-version, logical-path, geometry, locality, park-kind, national-park area,
remaining connected-split, component-count, and storage gates.
`promote.sql` atomically detaches the prior region leaves, moves and attaches the
prepared leaves, advances catalog state, and queues detached storage for GC.
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
operator-initiated and region-specific. The example Job has an 8 GiB bounded
`emptyDir`, at least 4 GiB memory, and no retries. Render its placeholders and
submit it manually:

```sh
export OSM_UPDATE_IMAGE=registry.example/library/workouts-osm:0.1.0
export OSM_IMAGE_PULL_SECRET=workouts-registry
export OSM_JOB_SUFFIX=norcal-$(date +%Y%m%d%H%M)
export OSM_DATABASE_SECRET=workouts-explorer-database
export OSM_DATABASE_KEY=osmDatabaseUrl
export OSM_DATABASE_TLS_SECRET=workouts-explorer-database-tls
export OSM_REGION_ID=geofabrik:norcal
export OSM_MAX_DOWNLOAD_BYTES=1073741824
envsubst < osm/manual-update-job.yaml | kubectl apply -n workouts-explorer -f -
```

One `building`/`validating` generation per region is enforced globally by the
catalog's partial unique index. The command transactionally allocates its ID and
`osm_build_<id>` schema, records source bytes, SHA-256, header timestamp, and
exact tool versions, then runs reference-complete filtering, reference checking,
flex import, postprocessing, derivation, clipping, partition preparation, and
validation. Only a passing validation report moves to `validating` and invokes
the schema-4 promotion function.

Before promotion, every failure stores a whitespace-normalized summary bounded
to 512 bytes, drops the candidate schema, and removes both PBFs without changing
the active generation. After promotion, detached leaves and the residual build
schema are processed through `storage_gc`. A GC failure is reported but never
fails the active generation; `failed` and `queued` GC records are retried at the
start and end of a later invocation.

The mutating PostgreSQL stages (`postprocess`, `derive`, `clip`,
`attribute-parks`, and `prepare-partitions`) each run as one transaction. A
recognized transient connection failure retries only the current PostgreSQL
stage, in the same generation and build schema, up to five total attempts with
5, 15, 30, and 60 second delays. Read-only validation uses the same retry policy
without a wrapping transaction. Syntax, constraint, and validation failures do
not retry. Retry diagnostics are bounded and redact PostgreSQL URLs. Command
output is streamed as statements complete, and a one-minute heartbeat identifies
the active stage, attempt, and elapsed time while a long statement is still
running. Attribution also labels its major phases and reports row counts while
connected components converge. PostgreSQL does not expose an in-progress row
count for one atomic `UPDATE`, so those statements use heartbeats until their
final affected-row count becomes visible.

This is process-local recovery. Exhaustion of all five attempts fails the
generation, drops its build schema, and removes its PBFs. A process or pod crash
cannot run that cleanup, but its generation-scoped PBFs are still lost with the
`emptyDir`; an operator must mark the stranded generation failed and remove its
build schema. In either case, the next invocation must reserve a new generation
and download/rebuild. Restartable recovery would require durable scratch storage
and an explicit stage journal. Applying this retry change as an in-place
operational patch also needs enough PostgreSQL space for the long transaction's
temporary data and WAL; rollback and retry can extend WAL retention and increase
peak storage.

`derive-compact.sql` builds the physical graph in bounded source-way batches to
avoid global sort, temporary-file, and shared-memory spikes. `clip-localities.sql`
rewrites only municipal-boundary candidates at ordered source-line fractions.
Pieces that cannot be covered by one locality within 1 cm retain matchable
geometry but abstain from municipal attribution.

## Identity rule evaluation

`/app/osm-identity-eval` evaluates candidate attribution rules without reserving,
promoting, or retiring an OSM generation. By default it copies active canonical
segments for Cupertino, Mountain View, Sunnyvale, and Saratoga, plus complete
source ways named by the checked-in regression corpus, into a uniquely named
unlogged scratch schema, preserves their existing park/locality attribution, runs
the exact production identity SQL, and drops the schema when finished. It reports
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
