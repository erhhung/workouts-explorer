# ADR 0001: Technology Stack

## Status

Accepted

## Context

Workouts Explorer needs a responsive browser interface, a secure REST API, durable asynchronous ingest and geospatial processing, and efficient storage and querying of private route data.

The expected deployment is a small homelab Kubernetes cluster with an existing PostgreSQL cluster, OTel Collector, Prometheus stack, Harbor registry, Gitea Actions, and Argo CD. The architecture must avoid unnecessary infrastructure while isolating interactive API load from worker load.

The application handles several years of source JSON and potentially millions of route points per account. It needs spatial indexes, nearest-segment matching, daily coverage rollups, and dense vector tiles. It does not need internet-scale search, a general event-stream platform, or independent product teams deploying many services.

## Decision

### Application structure

Use one repository with three owned components:

- `ui/`: React and TypeScript SPA
- `api/`: Go REST API server
- `worker/`: Go asynchronous worker

API and worker are separate binaries, images, and Kubernetes Deployments. They share domain and generated contract packages within the repository where appropriate.

### Frontend

Use:

- React with TypeScript
- Vite for development and production builds
- TanStack Query for server state
- TanStack Table for Summary and coverage tables
- MapLibre GL JS for interactive maps and vector tiles

The styling/component implementation remains open until a focused UI spike. The product must support dark and light themes and responsive desktop/mobile layouts without depending on a particular CSS framework in this ADR.

### Backend

Use Go for API and worker services with:

- Chi as the small `net/http`-compatible router
- `pgx` for PostgreSQL connectivity
- SQL-first data access, with `sqlc` where generated typed queries improve safety
- Standard Go concurrency and context cancellation
- OpenTelemetry Go SDK for traces and metrics

Avoid a large application framework or ORM. Spatial and aggregate queries should remain explicit SQL because PostGIS behavior is central to the product.

### API contract

Use OpenAPI 3.0.3 as a design-first source of truth:

- Hand-maintained `api/openapi.yaml`
- Generated Go request/response types and route interfaces
- Runtime request validation
- CI response-contract validation
- Public Swagger UI
- RFC 9457 Problem Details

Use `oapi-codegen` and its supported validation middleware for Go contract generation and request validation. The exact dependency version is dependency management, not a new architecture decision.

### Data storage

Use PostgreSQL with PostGIS.

- One application database stores identities, sources, jobs, workouts, private routes, copied path segments, attribution, rollups, and notifications.
- One separate OSM database stores public-map hierarchy and derived matching paths.
- Optional OSM read replicas serve matching reads.
- Native PostgreSQL UUID stores UUIDv7 application IDs.

Do not use OpenSearch for current query requirements. Do not use Valkey as a job queue or cache until measurements establish a need.

### Jobs

Use the application PostgreSQL database as the durable job queue.

- Parent/child ingest hierarchy
- Row locking equivalent to `FOR UPDATE SKIP LOCKED`
- Leases and heartbeats
- Database-coordinated global ingest concurrency
- Structured progress and retry history

API and worker do not communicate synchronously for job execution.

### Geospatial rendering

Use:

- PostGIS for route, path, matching, and coverage queries
- A separate OSM PostgreSQL/PostGIS database
- Copied matched segment geometry in the application database
- `pg_tileserv` for dense private vector tiles
- MapLibre GL JS for browser rendering
- A configurable public base-map tile provider

Keep `pg_tileserv` cluster-internal and proxy all private tiles through authenticated API routes.

### Database migrations

Use Goose for ordered SQL migrations and exceptional Go data migrations.

- Migrations run as an Argo CD PreSync Job.
- API and worker never migrate at startup.
- Migration execution uses a PostgreSQL advisory lock.
- CI validates clean creation and upgrade paths.

### Build and deployment

Use:

- Gitea Actions for CI and image builds
- Harbor for OCI images
- Helm chart under `helm/`
- Argo CD Application and live values in the separate `homelab-apps` repository
- Immutable eight-character commit-SHA image tags for deployment
- Root `VERSION` semver for coordinated release, mutable image aliases, chart version, and appVersion

## Alternatives Considered

### TypeScript/Node.js backend

Fastify with TypeScript would reduce initial learning cost and could support the expected scale. It was not selected because Go better fits streaming ingest, bounded concurrency, small container/runtime overhead, and the owner's learning goal. The React UI remains TypeScript regardless.

### Next.js full-stack application

A full-stack React framework could reduce the number of initial projects, but it would couple UI serving, API behavior, and background processing. It is less aligned with independently resource-limited API and worker services and offers little benefit for this authenticated SPA.

### Independent microservices

Separate ingest, matching, OSM import, notification, and tile orchestration services would permit independent scaling. They were rejected because the expected deployment is small, one team owns the product, and distributed consistency and operations would outweigh the benefit.

### Valkey or a message broker

A dedicated queue could offer high-throughput messaging. It was rejected because PostgreSQL already provides durability, locking, visibility, transactions, and sufficient throughput for the workload. Adding a queue would create a second source of truth for job state.

### OpenSearch

OpenSearch could provide text and analytics queries. It was rejected because workout lists, filters, aggregates, and path coverage are naturally served by indexed PostgreSQL and PostGIS queries at the intended scale.

### MongoDB or document storage

Document storage could retain provider payloads conveniently. It was rejected because the product needs relational account isolation, transactional upserts, versioned normalized schemas, spatial joins, and SQL aggregates. Raw provider files already remain in the source archive.

### Cross-database FDW for tiles

Joining application attribution to the OSM database through FDW would avoid copied geometry. It was rejected because private tile latency and reliability would depend on cross-database joins. Copying only matched segment geometry into the application database is simpler and faster.

### Dedicated map-matching engine

Valhalla or another standalone routing-aware matcher could infer paths between sparse points. It was not selected for the MVP because an in-process, bounded HMM/Viterbi matcher over the imported segment graph preserves deployment and privacy simplicity. A dedicated engine remains a reconsideration option if measured accuracy or network-search performance is inadequate.

### Continuous OSM replication

Minute-level replication would keep public-map data fresh. It was rejected because established paths change slowly for this use case. Manual regional refresh is adequate and operationally cheaper.

### Atlas or application-startup migrations

Atlas offers declarative schema management, and startup migration can simplify small deployments. Goose was selected because it supports explicit SQL plus targeted Go data migration while keeping schema changes reviewable. Startup migration was rejected to avoid API/worker races and to let Argo CD stop rollout on migration failure.

## Consequences

### Positive

- Interactive API and heavy worker load are independently resource-controlled.
- PostgreSQL/PostGIS provides one transactional foundation for private data, jobs, spatial queries, and aggregates.
- Go supports efficient streaming and explicit concurrency with a small operational footprint.
- The stack reuses existing homelab services.
- Design-first OpenAPI gives the UI, Swagger, tests, and Go handlers one durable contract.
- Private vector tiles can remain dense and performant without exposing `pg_tileserv`.
- The modular monorepo avoids distributed-system overhead while preserving clear components.

### Negative

- The owner must learn Go and its SQL/code-generation tooling.
- PostgreSQL job-queue semantics, leases, and fairness are application responsibilities.
- Copied OSM segment geometry requires reconciliation after OSM refresh.
- `pg_tileserv` needs an authenticated proxy and least-privilege SQL functions.
- The separate OSM database increases bootstrap and backup complexity.
- A design-first API requires CI discipline to keep generated code synchronized.
- One main environment makes migration and release tests especially important.

### Neutral or managed tradeoffs

- Source JSON is not retained, so reprocessing depends on the owner's source archive.
- Nearest-segment matching is intentionally less sophisticated than route inference.
- Mutable semver image tags exist for convenience, but deployment always pins immutable SHA tags.
- Rclone iCloud reliability is an external constraint; the local/NFS adapter prevents it from blocking MVP development.

## Conditions That Would Trigger Reconsideration

Reconsider parts of this stack if measured evidence shows one or more of the following:

- PostgreSQL job contention or throughput materially delays interactive queries despite worker isolation.
- Private vector-tile generation cannot meet the map latency target with indexes, rollups, and copied geometry.
- Nearest-segment matching produces unacceptable false matches or gaps across representative routes.
- Account or data volume grows far beyond the stated 10-user homelab target.
- Full-text or exploratory analytics requirements cannot be served reasonably by PostgreSQL.
- Multiple independent teams require separate release ownership for worker capabilities.
- OSM changes need near-real-time replication rather than occasional manual refresh.
- A supported HealthKit or other provider integration requires a native mobile component.
- The selected OpenAPI tooling cannot reliably validate or generate the agreed contract.
- Go maintenance cost outweighs its ingest and operational benefits after real implementation experience.
