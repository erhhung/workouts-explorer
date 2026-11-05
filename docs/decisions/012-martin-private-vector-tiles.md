# ADR 0012: Martin for private vector tiles

- Status: Accepted
- Date: 9/3/2026
- Owners: Product and engineering

## Context

Private route and coverage tiles are generated in PostgreSQL/PostGIS by
security-definer functions and exposed only through an authenticated API proxy.
The original runtime used `pg_tileserv`, whose release cadence and maintenance
activity no longer match the desired long-term dependency posture. Martin is an
actively maintained MapLibre project with PostGIS function sources, broader tile
formats and tooling, and a performance-oriented Rust implementation.

The browser must not reach the tile server directly. The tile runtime must not
receive application credentials or direct table privileges, and expiring map
selection scope must be revalidated inside PostgreSQL for every tile request.

## Decision

Replace `pg_tileserv` with Martin 1.15.0, pinned by OCI digest, in a
cluster-internal Deployment and Service named `workouts-explorer-tiles`.

- Martin connects as the least-privilege `workouts_tiles` login role.
- Automatic table publication is disabled. Function discovery is restricted to
  the `app` schema and uses `{schema}.{function}` source IDs.
- Tile functions use Martin's `(z integer,x integer,y integer,query_params json)`
  contract. They reject missing, additional, malformed, or invalid scope values
  before reading private data.
- Martin's in-memory tile cache and web UI are disabled. The authenticated API
  remains the only caller and returns `private, no-store`.
- A NetworkPolicy permits inbound Martin traffic only from API pods.
- Runtime configuration uses vendor-neutral `TILE_SERVER_URL` and `tileServer`
  names so future implementation changes do not affect the public contract.

This decision supersedes the `pg_tileserv` portions of ADR 0001 and ADR 0003.

### Spatial Filtering And Transient Recovery

Migration 014 filters parent routes against the tile bounding box before the
zero-length component branch expands geometry with `ST_Dump`. Both branches must
reject spatially irrelevant routes; otherwise a viewport burst can repeatedly
expand an entire selection, queue behind Martin's connection pool and exceed the
API's five-second forwarding deadline. Session/account/generation validation,
function ownership, ACLs and MVT contents remain unchanged. The fix was folded
into the JSON tile-contract migration by explicit development-only approval;
there was no separate tile migration 021. At that consolidation checkpoint the
latest schema was 20, minimum runtime 18; dated tile measurements retain that
historical context.

M8 decisions accepted on 10/11/2026 introduce the distinct immutable forward
migration `021_icloud_sources.sql`, with current schema/minimum runtime 21. This
adds iCloud source/credential and worker/scheduler fencing contracts, not another
tile fix or a reversal of the tile consolidation. M8 implementation and synthetic
verification are ready for owner review as of 10/12/2026; see the
[scoped results](../implementation-plan.md#verification-results-10122026).
Owner acceptance was pending at that checkpoint, with no live-cloud or production
validation claim. [M8 closeout](../development-progress.md#milestone-8-closeout)
subsequently closed acceptance; historical tile measurements remain unchanged.

The historical browser path coalesced private-tile 502/503/504 failures into at most two
source reloads, delayed one and two seconds. Recovery uses `setTiles` in place,
preserving layer state without recreating the capability or public base style.
Superseded URLs, canceled requests and disposed maps do not trigger recovery;
401 and current-capability 404 retain their authentication/expiry behavior.
Successful tiles do not reset the retry budget and create an endless retry loop.
This source-wide recovery path is superseded by the bounded per-tile scheduler
in the [031 follow-up](#rendering-and-delivery-031).

### Tile-Local Follow-Up (031)

All six source optimizations now cover Routes and Coverage: private projected
chunks, local clipping/LOD, Coverage evidence deduplication, independent focus,
bounded UI caches and bounded API delivery/cache/coalescing. This is new forward
**031**, not historical pre-squash 031 ingest reprocessing; 001-030 stay unchanged.
The historical 031 cutover at Helm 65 used schema/minimum **31**, baseline **27**,
ledger **0-31**. This foundation and the revision-64 UI/SVG fix remain deployed;
current status is the consolidated 031 contract below.

Martin's function ABI, exactly four opaque JSON scope parameters, independent MVT
authorization, role identity and disabled cache remain unchanged. API-only
five-argument `authorize_map_tile` returns session/selection expiry and is checked
before payload lookup and again after waiting, including hits. The API's bounded
in-process payload cache is not a cached authorization decision or browser cache;
all responses, including validation errors, remain `private, no-store` with
`Vary: Cookie, Authorization`.

The [architecture](../architecture.md#tile-local-rendering-031) owns current geometry,
focus, admission, memory and configuration limits. Provisioned tile-login defaults
bound SQL to **4s statement / 250ms lock / 5s idle transaction**, with RLS on;
fresh tile-login settings were verified after Martin's controlled zero-to-one
restart. Its **1.15.0 pin, pool of four and cache off** remain unchanged. HTTP
cancellation alone is not proof of immediate SQL cancellation.
[Controlled migration](../database-migrations.md#tile-local-geometry-031) completed
after actual-data rehearsal with coordinated API/shared-worker cutover and no
old-30 write window. Binary-only rollback remains unsupported; the tested
pre-writer checkpoint is no longer eligible after writers resumed. M8 stays
closed; authenticated tile smoke and viewport capacity are not claimed in this
deployment record, nor a new browser or M9 capacity receipt.

### Rendering And Delivery (031)

Current live is **Helm 67 / APP schema 31/minimum 31 / OSM 10/minimum 10**, baseline
**27**, ledger **0-31**, high-water **56**. New `raw_route_mvt` and
`coverage_mvt` preserve four-scope authorization and public URLs; 001-031,
legacy functions and the literal ingest fence 31 are untouched. Shipped worker-31
and legacy API readiness support rolling image fallback, not database rollback.
Martin discovered v2 sources by restart **before API-31 startup**, preserving
**1.15.0 / pool 4 / cache off**; no new worker images or increased server budgets.

The [architecture](../architecture.md#tile-rendering-and-delivery-031) owns
whole-component raw LOD, Coverage display-precision deduplication, public MapLibre
protocol scheduling and four-encoder streaming gzip. Initial browser queue **60s**
is distinct from the **15s service/retry budget starting at first dispatch**.
Source-wide `setTiles` recovery and opaque MapLibre internals are not used.
Final authorization follows encoder admission; all responses retain private/no-store
and `Vary: Cookie, Authorization, Accept-Encoding`. Real empty PBF remains valid,
but failures are not fabricated empties. `areTilesLoaded()` is not completeness proof.
The [canonical authorized desktop/4K run](../coverage-acceptance-results.md#tile-delivery-status-031)
completed all required views without observed delivery errors. Dense high-zoom raw
completion remained slow; wire savings are not compression-latency, usable-render
or M9 capacity acceptance. No resource, request-rate, timeout or body-limit increase
is justified by that bounded result.

## Consequences

### Positive

- The tile runtime follows an actively maintained MapLibre project.
- Function-only publication and the existing database role retain least privilege.
- Martin's additional source formats and tooling are available for future needs.
- The public API and MapLibre tile URLs remain unchanged.

### Negative

- PostgreSQL tile functions must accept and validate Martin's JSON query argument.
- The internal tile URL shape changes and requires API proxy coordination.
- Martin image and configuration updates require compatibility and private-tile
  regression testing.
