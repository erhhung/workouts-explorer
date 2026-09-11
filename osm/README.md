# OSM Import Spike

Milestone 7 uses a full-refresh, selective import. The source PBF is filtered
with Osmium to `highway=*` ways, administrative boundary relations, and their
required references before `osm2pgsql` flexible output loads a generation-specific
candidate schema.

The current assets implement the measured NorCal import and graph derivation.
Matcher thresholds remain pending ADR 0009 experiments.

Pinned tools:

- Osmium 1.19.0
- osm2pgsql 2.3.1

The candidate schema name must match `osm_build_<generation>` and is supplied
in `OSM_BUILD_SCHEMA`. The manual updater owns the complete import and promotion
lifecycle.

`postprocess.sql` adds the meter-based geography index required by matching and
derives OSM administrative-level-8 municipal locality polygons. After clipping,
`prepare-partitions.sql` adds fixed region/generation provenance, replaces keys,
builds the canonical indexes, and gives the three promoted tables
generation-qualified names. `validate.sql` then records exact preparation,
source-version, logical-path, geometry, locality, and storage gates.
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

`derive-compact.sql` builds the physical graph in bounded source-way batches to
avoid global sort, temporary-file, and shared-memory spikes. `clip-localities.sql`
rewrites only municipal-boundary candidates at ordered source-line fractions.
Pieces that cannot be covered by one locality within 1 cm retain matchable
geometry but abstain from municipal attribution.

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
