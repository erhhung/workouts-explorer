# Architecture

## Context

Workouts Explorer is a small, self-hosted, multi-account application for importing workout records and GPS tracks, displaying workout summaries, and calculating visited path coverage from public map data.

The expected deployment has up to 10 accounts and 10 concurrent users, but each account may contain several years of route points and thousands of workouts. Ingest, path matching, OSM maintenance, and tile generation must not make the interactive API unresponsive.

The system handles sensitive health and location data. Account isolation, credential protection, authenticated private map tiles, safe diagnostics, and deletion behavior are first-class architectural constraints.

## Components

### Owned components

The `workouts-explorer` repository contains three independently deployable components:

| Component | Project folder | Image | Responsibility |
|---|---|---|---|
| UI | `ui/` | `workouts-ui` | React SPA, responsive Summary and Map views, Swagger-independent user workflows |
| API | `api/` | `workouts-api` | REST API, authentication, authorization, queries, commands, notifications, private tile proxy, static/public runtime config |
| Worker | `worker/` | `workouts-worker` | Durable jobs, source access, ingest, deletion, matching, aggregation, OSM maintenance, connection checks |

API and worker are separate Go binaries and Kubernetes Deployments. They may share Go domain packages and generated OpenAPI types from the same repository, but they have independent processes, resource limits, health checks, and scaling.

### Supporting components

| Component | Responsibility |
|---|---|
| Application PostgreSQL/PostGIS | Accounts, sessions, sources, jobs, workouts, private routes, copied segments, attribution, rollups, notifications |
| OSM PostgreSQL/PostGIS | Imported OSM hierarchy, regional boundaries, derived path segments, public fallback cache |
| OSM read replica | Optional matching reads without adding load to OSM import/refresh primary |
| `pg_tileserv` | Generates dense private route and coverage vector tiles from the application database |
| Public base-map provider | Provides unauthenticated browser base-map tiles |
| NFS archive | Immediately usable source archive for Health Auto Export JSON |
| Rclone/iCloud Drive | Preferred remote source transport when upstream ADP support permits |
| SMTP server | Sends invitations and password resets using iCloud SMTP initially |
| OTel Collector | Receives API and worker traces and metrics |
| Prometheus/Alertmanager | Evaluates operational metrics and sends operator alerts |

### High-level flow

```text
Browser
  | HTTPS
  v
UI SPA -----> API ----------------------> Application PostgreSQL/PostGIS
               |                                      ^
               | authenticated MVT proxy              |
               v                                      |
          pg_tileserv --------------------------------+

API --enqueue durable jobs--> Application PostgreSQL
                                  |
                                  v
                               Worker
                              /   |   \
                         NFS/rclone |  OSM PostgreSQL/PostGIS
                                    |
                                    +--> copied matched segments and rollups

API/Worker --> OTel Collector --> Prometheus/Alertmanager
API        --> SMTP
Browser    --> public base-map tiles
```

## Component Responsibilities

### UI

- Loads public runtime presentation settings from `/api/config`.
- Authenticates through the API and keeps bearer tokens out of persistent browser storage.
- Uses the HTTP-only session cookie and session-bound CSRF token for browser mutations.
- Resolves user interactions into explicit API requests.
- Polls active jobs and notifications at the configured interval, default 30 seconds.
- Optimistically removes UI-created deletions while accepting eventual consistency for other sessions.
- Uses MapLibre for public base maps and authenticated private vector layers.
- Never receives source credentials, database topology, or internal `pg_tileserv` URLs.

### API

- Implements the design-first OpenAPI contract under `/api`.
- Serves the public OpenAPI document and Swagger UI.
- Creates and revokes local sessions.
- Enforces administrator and data-owner role separation.
- Enforces account scope before every private read, write, export, log, and tile operation.
- Performs synchronous schema and authorization validation only; external connection checks and data processing are jobs.
- Stores encrypted current source configuration and creates encrypted job-scoped snapshots.
- Enqueues durable jobs and coalesces equivalent queued or running jobs.
- Serves Summary, workout, provenance, coverage, preference, job, and notification queries.
- Authenticates private tile requests and proxies them to internal `pg_tileserv`.
- Redacts errors and diagnostics before returning them.

### Worker

- Claims durable jobs from PostgreSQL according to priority.
- Heartbeats claims and permits recovery after worker interruption.
- Decrypts source configuration only for the duration of an authorized job.
- Discovers candidate files without downloading the whole range.
- Stages and processes one file per available ingest slot.
- Parses Health Auto Export incrementally and writes workout transactions.
- Removes staged files and clears encrypted snapshots at every terminal outcome.
- Updates derived timezone, elevation, attribution, and aggregate data.
- Runs source checks, deletion, OSM import, and OSM refresh work.
- Emits safe structured logs, job events, OTel metrics, and traces.

### `pg_tileserv`

- Connects only to the application database using a least-privilege database role.
- Can execute only approved private tile functions or views.
- Is reachable only inside the cluster.
- Does not implement end-user authentication itself.
- Receives only API-authorized account and selection parameters.

## Data Model

All application-owned IDs are UUIDv7 stored as native PostgreSQL `uuid`. API serialization uses compact uppercase UUIDs.

### Identity and account

| Entity | Purpose |
|---|---|
| `users` | Immutable username/email, mutable full name, account lifecycle |
| `administrators` | Separate bootstrap administrative identities with no workout ownership |
| `accounts` | Tenant boundary; one personal account per data user in the MVP |
| `account_memberships` | One owner membership now; preserves a future sharing-compatible boundary without implementing sharing |
| `auth_identities` | Local password identity now, OIDC identity later |
| `sessions` | Opaque revocable sessions, absolute expiration, CSRF secret, hashed bearer verifier |
| `invitations` | Expiring single-use signup tokens stored as hashed verifiers |
| `password_resets` | Expiring single-use reset tokens stored as hashed verifiers |
| `user_preferences` | Theme, units, timezone, week start, clock, table, and date preferences |

Administrator and data-owner logins remain different identities even when controlled by one person.

### Sources and ingest

| Entity | Purpose |
|---|---|
| `sources` | Current account-owned source, display name, immutable type, auto-sync flag, health status, encrypted current config |
| `source_tombstones` or tombstone columns | Minimal deleted source identity retained for provenance; not exposed by CRUD APIs |
| `source_files` | Safe discovery identity, source-relative name, size, modification metadata, checksum, processing state |
| `jobs` | Parent/child hierarchy, type, priority, status, progress, retry/cancel links, safe parameters |
| `job_config_snapshots` | Encrypted source configuration copied for one job and cleared at terminal state |
| `job_events` | Structured progress and safe diagnostic events |
| `job_logs` | Bounded, owner-authorized, security-redacted diagnostic output |

The source row stores only the current configuration. Updating it does not retain persistent credential revisions. A running job uses its own encrypted snapshot and is unaffected by later source updates. Retrying snapshots current validated source configuration.

### Workouts and route data

| Entity | Purpose |
|---|---|
| `workout_types` | Normalized identity and provider-derived display label |
| `workouts` | Source, provider ID, type, timestamps, duration, normalized aggregates, indoor state, lifecycle |
| `workout_route_points` | Provider sequence, timestamp, 3D position, speed, course, four accuracy fields, quality flags |
| `workout_routes` | Derived line geometry, bounds, point count, completeness, elevation summary |
| `workout_import_events` | Chronological created, updated, and matched_unchanged provenance |
| `workout_deletion_targets` | Immutable workout IDs captured by an asynchronous deletion request |

The primary deduplication identity is source plus provider workout ID. A provider-specific fingerprint is a fallback only when no stable ID exists.

Changed source content updates the current workout transactionally and rebuilds only affected derived data. Deletion first hides a workout, then physically removes its current record and detailed import events after derived cleanup. A later bounded reimport creates a new application workout ID for the same provider ID. Ingest and deletion for the same workout are serialized so deletion-pending records cannot be updated concurrently.

### Time

- Store workout instants as `timestamptz`.
- Preserve original start and end UTC offsets.
- Store the workout's local start date for calendar filtering.
- Store a nullable inferred IANA timezone name and its derivation source at workout level, not at each route point.
- Derive timezone from a route point and offline timezone boundaries where possible.
- Otherwise use the chronologically nearest GPS-derived workout in either direction with the same UTC offset.
- When no IANA name can be inferred, leave the IANA field null and derive a display label such as `UTC-08:00` from the preserved offset.
- Recompute affected inferred timezones after ingest or deletion.

### OSM and coverage

The OSM database retains standard public-map hierarchy and derived matching paths. Exact importer schema is an implementation decision constrained by preserving node, way, relation, tag, and version provenance.

The application database intentionally copies only matched segment data required for private queries and rendering:

| Entity | Purpose |
|---|---|
| `path_segments` | OSM identity/version, 2D geometry, name, path classification, display tags |
| `workout_segment_attributions` | Unique workout/segment pair and earliest matched timestamp |
| `account_segment_daily_rollups` | Count and visit extrema keyed by account, segment, and workout-local start date |
| `account_segment_all_time` | All-time first/latest visit and distinct workout count |
| `account_data_generations` | Cache-busting generation for private tile URLs after mutations |

The unique workout/segment constraint ensures outbound and inbound traversal counts once.

Daily rollups support named periods and arbitrary explicit ranges without maintaining separate mutable materializations for each shortcut.

Selections containing all workouts in a date range use daily rollups. Arbitrary workout subsets query the underlying unique workout/segment attribution joined to the selected workout IDs, because an account/day rollup cannot preserve subset membership. Tile functions choose the rollup or attribution path from the authorized map selection.

### Notifications and announcements

| Entity | Purpose |
|---|---|
| `notifications` | Per-user severity, server-owned display state, typed subject reference, message, expiration |
| `announcements` | Admin-created all-user title, message, severity, expiration, retraction audit |

Reminder notifications retain enough typed condition data to evaluate persisted state at the next signin without an external call.

## Jobs And Concurrency

### Queue

PostgreSQL is the durable queue. Workers claim rows using locking semantics equivalent to `FOR UPDATE SKIP LOCKED`, record leases and heartbeats, and recover abandoned claims.

Priority order:

1. Source connection checks and deletion
2. Manual ingest
3. Scheduled ingest
4. OSM bootstrap and refresh

OSM maintenance is single-flight. High-priority work bypasses ingest-file semaphores and remains intentionally unpooled because it is rare. Duplicate active checks and deletions are coalesced.

### Ingest hierarchy

Every ingest request is a parent job with one child per selected source. Files are processing records beneath a source child, not a third job level.

Parent status derives from children:

- `succeeded`: every child succeeded, including no-data results.
- `partially_succeeded`: at least one child succeeded and another failed or was cancelled.
- `cancelled`: no child succeeded and every child was cancelled.
- `failed`: no child succeeded and at least one child failed, including a mix of failed and cancelled children.

### Ingest-file concurrency

Defaults are configurable operator limits:

- Two files per account
- Two files per worker
- Four files globally

Global coordination is stored in PostgreSQL so additional workers cannot exceed the global limit.

Each slot owns one file through discovery selection, optional download, parse, transaction, and cleanup before claiming another file.

### Source snapshot lifecycle

1. API copies current encrypted source parameters into the child job.
2. Worker decrypts the snapshot only when executing the job.
3. Source updates do not alter active snapshots.
4. Source deletion cancels jobs using its snapshots.
5. Terminal success, failure, or cancellation clears the encrypted snapshot.
6. Retry uses current validated source parameters.
7. Stale-job maintenance clears snapshots left by abnormal termination after reaching a terminal state.

If an adapter refreshes mutable credentials such as rclone cookies, the worker writes them back only with a compare-and-swap on the source generation captured by the job. If the source was updated or deleted, refreshed values from the older snapshot are discarded rather than overwriting current credentials.

PostgreSQL WAL and infrastructure backups may retain older encrypted values according to operator retention. Logical clearing does not promise immediate physical erasure from backups.

## Ingest Pipeline

### Discovery and staging

1. The adapter enumerates metadata matching incremental or explicit-range rules.
2. Incremental sync selects only new or changed source objects.
3. Bounded Manual Sync selects all matching objects, even previously processed ones.
4. A worker slot downloads at most one remote file into `/tmp/<user-id>/<job-id>/`.
5. Partial downloads use a non-final filename and are atomically promoted when complete.
6. Local/NFS adapters may read directly when staging is unnecessary.
7. File processing writes transactional workout results and source-file state.
8. Cleanup removes the staged file before the slot claims another.

### Parsing and normalization

- Parse `/data/workouts` without depending on JSON key order.
- Preserve provider duration independently from rounded timestamp differences.
- Prefer explicit provider aggregates over sums of incomplete sample arrays.
- Retain ordered route points and equal timestamps.
- Preserve optional values as unknown rather than inventing zeroes.
- Record recoverable adapter warnings for suspicious units and quality values.
- Reject invalid file JSON without leaving a falsely complete file result.

### Derived processing

- Build 3D raw route geometry.
- Derive bounds and elevation statistics.
- Resolve or update workout timezone.
- Ensure required OSM path data exists for route envelopes.
- Match points to nearest candidate segments using quality data and an implementation-tested threshold.
- Upsert unique workout/segment attribution.
- Rebuild affected daily and all-time rollups.
- Advance the account data generation used by private tile URLs.

## External Interfaces

### REST API

- Base path: `/api`
- Specification: OpenAPI 3.0.3, design-first
- Documentation: public `/swagger`
- Schema: public `/api/openapi.yaml`
- Errors: RFC 9457 Problem Details
- IDs: tolerant UUID input, compact uppercase output
- Pagination: page number and configurable page size
- Sorting: allowlisted multi-column syntax
- Async commands: `202 Accepted` plus job location
- Source request validation: discriminated adapter-specific schemas

The complete route inventory and observable rules are in `functional-spec.md` and the OpenAPI document to be created during implementation.

### Private vector tiles

The API owns the public private-tile route. A browser never calls `pg_tileserv` directly.

1. Browser requests a route or coverage tile using a session-scoped map selection.
2. API authenticates the session and verifies account ownership.
3. API supplies approved selection and account context to internal `pg_tileserv`.
4. `pg_tileserv` queries copied segment geometry and private attribution in the application database.
5. API returns the tile with private caching headers.

Tile URLs include an account data generation so a redraw after ingest or deletion does not reuse stale private data.

### Public map and OSM data

- Browser base-map tile URL and attribution are safe runtime configuration.
- Regional road/path hierarchy begins with a California extract.
- Missing regions use bounded public Overpass retrieval and caching, never one request per coordinate.
- Public endpoint limits, user-agent requirements, and attribution must be respected.
- Manual OSM refresh replaces or updates regional data and reconciles copied matched segments.

### Rclone

- The user runs rclone's external interactive 2FA setup.
- Source CRUD accepts only whitelisted fields needed for the iCloud adapter.
- Rclone's reversibly obscured password is re-encrypted by the application.
- The worker persists updated cookies into current encrypted source configuration.
- Cookie persistence requires the current source generation to match the job snapshot generation.
- Expired trust tokens set connection-failed and require external reconnection plus source update.
- The active upstream ADP issue makes NFS/local ingest the immediate MVP path.

### SMTP

- Initial provider: `smtp.mail.me.com` using STARTTLS and an app-specific password.
- Email is used only for invitations and password resets in the MVP.
- Admin announcements remain in-app.

## Authentication And Authorization

### Authentication

- Local passwords use Argon2id.
- Session, invitation, and reset tokens are cryptographically random, contain at least 128 bits of entropy, and are stored only as one-way verifiers.
- Signup and reset token consumption is atomic and single-use.
- Public authentication and recovery routes use rate limits and enumeration-resistant responses.
- Browser sessions use Secure, HTTP-only, SameSite cookies.
- API clients may use the opaque session token as a bearer token.
- Cookie mutations require a session-bound CSRF token.
- Session lifetime is globally configured, default two hours absolute.
- Password reset revokes all existing sessions.
- OIDC remains behind a future authentication-provider boundary.

### Authorization

- Administrator and data-owner roles are separate identities.
- Administrators manage users, invitations, announcements, and OSM jobs only.
- Administrators cannot access workout records, routes, source configuration, private tiles, user notifications, or user job logs.
- Data owners can access only their personal account.
- Account identity is derived from the authenticated session, never trusted from client parameters.
- Temporary map selections and tiles are session- and account-scoped.
- Source and job ownership is rechecked for every item route.

Application-layer authorization is mandatory. ADR 0002 adds selective, forced PostgreSQL row-level security for private account-owned tables, transaction-local tenant context, and narrowly scoped cross-account orchestration functions as database defense in depth.

## Deployment Model

### Repository and release names

- Repository: `workouts-explorer`
- Product version: root `VERSION`
- Component folders: `ui/`, `api/`, `worker/`
- Images: `workouts-ui`, `workouts-api`, `workouts-worker`
- Helm chart: `helm/`, chart name `workouts-explorer`
- Runtime namespace: `workouts-explorer`, set by Argo CD rather than hardcoded in the chart

### CI and images

Gitea Actions performs:

1. Formatting, linting, unit, contract, migration, and UI tests.
2. OpenAPI generation-drift validation.
3. Container builds.
4. Security and dependency checks as configured.
5. Push to Harbor using both an immutable eight-character commit SHA and a mutable semver from `VERSION`.

Helm `version` and `appVersion` follow `VERSION` for the coordinated release.

### GitOps

- The application repository contains the chart but no live Argo CD Application.
- `homelab-apps/workouts-explorer/` contains the live Application and values.
- Environment values pin immutable image SHA tags.
- Chart defaults set `ingress.enabled: false`.
- Hostnames, image registries, resources, Secret names, storage limits, and ingress settings are deployment values.
- There is one primary main-branch environment rather than dev/test/prod duplication.

### Database migrations

- Goose owns ordered application and app-derived schema migrations.
- API and worker do not migrate on startup.
- An Argo CD PreSync migration Job runs the API image's migration command.
- Migrations acquire a PostgreSQL advisory lock.
- Deployment stops if migration fails.
- Migrations are immutable after merge; fixes are forward migrations.
- Expand-and-contract changes preserve compatibility during rollout.
- CI validates clean creation and supported upgrade paths.
- Destructive migrations require an operator backup note.

### Runtime configuration

Secret runtime configuration is mounted into API and worker. The configuration should expose only actual operator policy or environment values, not correct-by-construction domain logic.

Appropriate settings include:

- Public URL and listening ports
- Session lifetime and pagination maximum
- SMTP, PostgreSQL, OTel, and master-key secrets
- Sync cadence and stale-data threshold
- File concurrency and staging roots
- Map provider, fit padding, and presentation defaults
- Elevation-visible workout types
- Worker polling and UI polling interval

Timezone inference order, deletion semantics, attribution uniqueness, and cleanup guarantees are coded rules rather than configuration.

## Observability

### Traces

- Trace API commands through job creation.
- Link worker job traces to the originating request without requiring one long-lived span.
- Trace database, source transport, parsing phase, OSM lookup, matching, rollup, and tile proxy latency.
- Never attach coordinates, route geometry, health values, or credentials as attributes.

### Metrics

Minimum metrics include:

- Request rate, latency, status, and active requests
- Database pool utilization and query latency
- Job counts by type, priority, state, age, attempt, and duration
- Source freshness and connection state
- Files discovered, processed, failed, staged, and cleaned
- Workouts created, updated, unchanged, and rejected
- Route point and path match counts, unmatched ratio, and matching latency
- OSM import/refresh age and failures
- Private tile latency and error rate
- Notification and SMTP delivery outcomes

### Logs

- Runtime logs are structured JSON.
- Central operator logs and owner-visible job logs are separate concerns.
- Owner-visible diagnostics are bounded and security-redacted.
- Redaction covers authorization values, passwords, cookies, tokens, database credentials, full private paths, coordinates, route geometry, source payloads, and health values.
- Safe request and job IDs correlate API, worker, and OTel data.

## Failure Handling

### API and database

- API rejects invalid commands before job creation.
- Equivalent active jobs return the existing job.
- Database transactions prevent partial source/workout state.
- Readiness reflects ability to serve traffic, not unrelated source or OSM health.

### Worker interruption

- Leases and heartbeats make abandoned work reclaimable.
- File and workout transaction boundaries prevent falsely complete results.
- Startup cleanup removes abandoned temporary files.
- Terminal cleanup clears source snapshots.

### Source failure

- Source check failure blocks future ingest and creates an owner notification.
- Active jobs continue with their immutable snapshot when a source is updated.
- Source deletion cancels jobs and clears snapshots.
- Stale-source reminders reappear at next signin only if persisted source state still fails.

### Ingest failure

- One source failure does not block siblings.
- Parent status communicates partial success.
- Retry reruns only failed source children using current source config.
- Scheduled success is silent; manual success and all failures notify according to policy.

### Deletion failure

- Targets are logically hidden as soon as deletion is accepted.
- Physical cleanup failure never makes them visible again.
- Retry uses the original immutable target IDs.
- Owner receives a Delete failed notification and redacted logs.

### Account deletion

- Account deletion atomically disables authentication and prevents new jobs.
- Queued and running account jobs are cancelled before purge enumeration.
- Purge waits for terminal jobs or expired leases, clears snapshots and staging, then captures the complete private-data target scope.
- Purge cannot run concurrently with a job that may still commit account data.
- Failure leaves the account disabled and resumes from durable deletion state.

### OSM failure

- OSM bootstrap/refresh is single-flight and lowest priority.
- Refresh failure does not make API readiness fail.
- Existing copied segment data remains usable until a successful reconciliation.

## Security Boundaries

### Public boundary

Public endpoints are limited to Swagger assets, the non-secret OpenAPI document, safe runtime UI config, signin/signup/reset flows, and cluster health according to ingress policy.

### Browser boundary

- Do not persist bearer tokens in local storage.
- Treat all browser input, map selection IDs, sort fields, filenames, and UUIDs as untrusted.
- Proxy Gravatar to avoid disclosing browser IP and email hash.
- Keep private tile responses out of shared caches.

### Account boundary

- Scope all private tables and queries by account.
- Derive account from session, not request bodies.
- Test cross-account denial for every private resource class.
- Preserve the same isolation in tile functions and logs.

### Worker boundary

- Worker has source and OSM permissions the API does not need.
- Decrypted credentials live only in process memory except for a private job-scoped rclone config file required by the subprocess.
- Rclone config files use mode `0600`, contain only one source, live in the job staging directory, and are removed on success, failure, cancellation, and worker-startup scavenging.
- Temporary files use private permissions and job-scoped paths.
- Rclone subprocess arguments and logs must not contain plaintext secrets.

### Database boundary

- Use separate least-privilege roles for API, worker, migration, OSM import, OSM read, and tile service.
- Application and OSM databases remain separate.
- `pg_tileserv` can access only approved application schemas/functions.
- Source encryption key is not stored in PostgreSQL.

### Operational boundary

- Kubernetes Secrets supply credentials and encryption keys.
- TLS terminates at trusted ingress or gateway infrastructure.
- PostgreSQL volume and backup encryption are operator requirements.
- Logs, backups, and WAL have explicit retention and access controls.

## Deferred Decisions

- Session-cookie, bearer-token, and CSRF details proposed in ADR 0004
- Public-authentication policy and SMTP failure handling proposed in ADR 0005
- CSS/component styling approach and bundled default font pending the ADR 0006 spike
- Source envelope-encryption format and key lifecycle proposed in ADR 0007
- Exact OSM importer and segment derivation toolchain pending the ADR 0008 spike
- Tested nearest-segment distance and route quality rules pending ADR 0009 experiments
- Coverage bucket boundaries pending ADR 0010 historical-data analysis
- Retention limits for job history and owner-visible diagnostic logs
- Public base-map provider for externally exposed installations
- Additional adapters and provider-normalization rules
- OIDC identity linking and account migration
- Sharing grants, temporal/geofence filters, comparison authorization, and overlap rendering
