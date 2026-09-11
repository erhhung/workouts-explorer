# ADR 0012: Martin for private vector tiles

- Status: Accepted
- Date: 2026-09-03
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
