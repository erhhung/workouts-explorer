# Product Brief

## Problem

Fitness platforms record individual workouts and GPS routes, but they do not make it easy to explore a lifetime of workouts as geographic coverage. A person who intentionally explores new streets, trails, and paths cannot readily answer:

- Where have I worked out during a selected period?
- Which roads and paths have I covered, and how often?
- Which nearby paths have I not visited?
- How has my activity changed across days, weeks, months, and years?

Workouts Explorer imports workout records and GPS tracks, normalizes them independently of the source provider, and presents both raw routes and path-level coverage on an interactive map.

Current source ends at **030**, with final source/runtime support and
schema/minimum runtime **30**, baseline **27**, ledger **0-30**. Combined **029**
contains ingest status/reprocessing/phases; **030** contains compact/canonical
display. See the [history mapping](database-migrations.md#source-history-mapping)
and [pre-cutover actual status](database-migrations.md#pre-cutover-live-status).
The approved live reconciliation is pending; source preparation is not deployment.
Context warming uses retained evidence with no job, generation-policy or
matcher-policy change; it needs no provider check, new import, OSM
regeneration/download or route rematch. M8 acceptance is governed only by
[Milestone 8 Closeout](development-progress.md#milestone-8-closeout), not by this
brief or historical verification receipts. Benchmark/capacity targets below
remain separate and are not certified by deployment or milestone closeout.

## Intended Users

The initial user is the product owner, who has Apple Fitness workout history exported by Health Auto Export. The self-hosted product must nevertheless support multiple independently invited users from the MVP onward.

Expected deployment scale:

- Up to 10 private user accounts
- Approximately 10 concurrent users
- One or more data sources per account
- Approximately one workout per account per day
- Multiple years and thousands of workouts per account
- Several gigabytes of archived source JSON per account

Each user owns one personal workout account. Users cannot access another account's fitness data, GPS routes, source configuration, job logs, or notifications. A separate administrator identity manages account lifecycle and shared infrastructure without access to private workout data.

## Primary User Journeys

### Explore workout history

1. The user signs in.
2. The product restores the user's last date-range preference.
3. The user selects an explicit range or a shortcut such as Last 30 days.
4. The Summary view shows aggregate statistics and a sortable, paginated workout table.
5. Hovering an aggregate shows values grouped by workout type.
6. The user opens a workout for details, provenance, export, deletion, or map navigation.

### Explore routes and path coverage

1. The user switches to Map.
2. The map fits the routes in the selected period.
3. The base map uses a workout-appropriate style and follows the light or dark application theme.
4. The user can select any configured base-map style for the current Map visit.
5. The user selects Routes or Coverage.
6. Routes are colored by workout type; hovering highlights the topmost, most recent workout route.
7. Long pause gaps split a workout into separate map segments instead of drawing false connections between distant resumed points.
8. Coverage uses fixed amber count buckets, with a matching magenta sequence for the focused workout, so frequently visited paths do not flatten the rest of the scale.
9. The user filters the map to any subset of workouts with a bulk checkbox above the workout checkboxes.
10. The user opens Road Coverage over the map without unloading it, then searches, sorts, and pages through locality-scoped road, path, and park counts and visit dates.
11. The user can return directly from a Road Coverage name or visit date to the entity or workout on the map.

### Keep data synchronized

1. The user creates a source through the authenticated REST API.
2. A high-priority connection check validates the source asynchronously.
3. Scheduled sync discovers and imports new or changed files from all auto-sync sources.
4. The user can run Manual Sync for selected sources and an optional inclusive date range. Bounded runs skip unchanged successful files by default; an unchecked bounded-only option explicitly requests reprocessing.
5. The Data Sync menu reports source freshness, active work, warnings, and failures.
6. Manual completion and failure notifications link to job details and security-redacted diagnostics.

### Administer the self-hosted installation

1. A bootstrap administrator signs in through a separate administrative identity.
2. The administrator invites users by email.
3. Invitees choose a unique username, full display name, and password.
4. The administrator can revoke invitations, delete accounts, publish in-app announcements, and trigger OSM maintenance.
5. The administrator cannot view private workouts, routes, source credentials, or user ingest logs.

## MVP Scope

### Accounts and security

- Multiple invited user accounts with no public registration
- Separate bootstrap administrator and data-owner identities
- Local username/password authentication
- Signin by username or email
- Email invitation and forgot-password flows through SMTP
- Revocable two-hour sessions by default
- Secure browser cookies, bearer session tokens for API clients, and CSRF protection
- Account isolation for all fitness, source, job, notification, and route data
- Envelope encryption of complete source adapter configuration using a Kubernetes-provided master key

### Data sources and ingestion

- Type-agnostic source CRUD through authenticated REST APIs
- `health-auto-export-local` source for local or NFS-mounted archives
- Deployed `health-auto-export-icloud` source using an externally authenticated ready session and a job-scoped native rclone 1.75.2 CLI
- Per-source `autoSyncEnabled` behavior
- High-priority asynchronous connection checks
- Scheduled incremental sync of new or changed files, default daily with an operator-configurable cadence independent of UI polling
- Manual incremental or bounded historical sync, with metadata skipping by default and opt-in **Reprocess all files in this date range**
- Parent ingest jobs with one child per selected source
- Server-side deduplication of equivalent active jobs
- File-at-a-time private staging with cleanup-gated terminal transitions, bounded retries and pod-scoped crash recovery
- Idempotent workout upsert using source and provider workout ID
- Complete, sanitized import provenance
- Retry, cancellation, polling, notifications, and redacted diagnostic logs

The 10/11/2026 M8 decisions accept remote ID/size/modification-time incremental
skipping rather than downloading unchanged files to hash them. A content overwrite
preserving all metadata can go undetected; new bounded Manual Sync uses the same
metadata skip policy within the selected dates. The bounded-only checkbox below
the date inputs and above Start sync defaults unchecked; explicit selection
requests the former forced behavior. Rclone iCloud v1.75.2 exposes no remote
hashes/free MD5; processed files receive local SHA-256 while parsing, not as proof
of skipped remote bytes. Sync performs one application discovery, not necessarily one
native listing or HTTP request. iCloud uses externally completed Apple 2FA/approval,
full cookies and a trust token; no host config is automatically read/uploaded.
Only version, path and optional client identity are returned, never Apple ID or
masked credentials. See [iCloud setup](icloud-source-setup.md).

Provider-backed identity is account-wide across local/iCloud transports under
the shared provider namespace. Reprocessing preserves workout/route identity,
live-owner blockers, eligible deleted-source adoption and deletion suppression;
it does not promise forced Coverage. ID-less fallback remains source-local.
Retries skip valid unchanged successes on their own source-child ancestor path
even after force; ordinary non-forced retries may also use valid source inventory.
See [Ingest Reprocessing](ingest-reprocessing.md) for the API, legacy bounded-job
compatibility and source-029 contract, with final runtime 30 and marker 27.
The all-popups **Dismiss** requirement belongs to the UI release, outside the
ingest design; current browser/acceptance status is tracked only in
[Milestone 8 Closeout](development-progress.md#milestone-8-closeout).

At the historical checkpoint **before explicit ingest reprocessing**, M8 implementation and synthetic
verification were ready for owner review as of 10/12/2026; its completion checkbox
was unchecked pending owner acceptance. [Verification results](implementation-plan.md#verification-results-10122026)
distinguish passing M8 PostGIS suites from ordinary checks with database skips
and three reproduced baseline database failures. No live-cloud or production
validation was claimed by those receipts. Their authorization boundaries are
historical, not a renewed approval requirement for the already-authorized M8
closeout after its browser gate.

### Workout data

- Generic workout model with normalized workout types and source provenance
- Provider-supplied aggregate metrics in canonical units
- Original GPS points with sequence, timestamp, coordinates, altitude, speed, course, and accuracy fields
- Workout-local timestamps with original UTC offset
- IANA timezone inference from route location, nearest definitive workout, or UTC-offset fallback
- Derived elevation minimum, maximum, and gain for all usable routes
- Individual and date-range deletion with asynchronous derived-data cleanup

### Summary

- Shared explicit or shortcut date range
- Aggregate workout count, duration, distance, energy, and applicable metrics
- Aggregate hover breakdown by workout type
- Sortable and paginated workout table
- Per-user columns and page size
- Desktop table and three-column expandable mobile rows
- Per-workout action menu

### Map and coverage

- Responsive desktop and iPad-sized tablet map; tablet-focused UI/UX is Milestone 10, with iPhone-sized development explicitly out of scope
- Raw route and path-coverage modes named Routes and Coverage
- Workout subset filtering
- Automatic extent fitting with configurable padding
- Route colors by workout type and bright-purple hover highlighting
- Newest route rendered on top
- Nearest-path matching against all relevant public map paths
- Deterministic OSM-derived segments for matching and visited geometry
- One dissolved traversed geometry per workout and physical segment, preserving disjoint visited spans without filling gaps
- Locality-scoped logical roads and paths for distinct-workout attribution
- One attribution per workout and logical path using the earliest accepted segment match
- Paired fixed amber aggregate and magenta focused count buckets for coverage
- Authenticated private vector tiles
- Searchable, sortable, paginated Road Coverage dialog with direct entity and workout map navigation
- Extensible public base-map style families with paired light and dark variants
- Workout-type style defaults, a Smooth fallback, and a current-visit style selector
- Provider-specific visible attribution and direct or privately cached public base-map resources

### API and operations

- OpenAPI 3.0.3 design-first REST API under `/api`
- Public Swagger UI at `/swagger`
- RFC 9457 Problem Details errors
- Runtime request validation and CI response validation
- UUIDv7 internal identifiers with compact uppercase API output
- Page-number pagination and multi-column sorting
- PostgreSQL schema versioning and data migrations
- OpenTelemetry metrics and traces
- In-app warnings plus Alertmanager-compatible metrics
- Kubernetes deployment through Helm and Argo CD

## Non-Goals

The following are excluded from the MVP but may be designed later:

- Workout-data sharing between users
- Side-by-side account statistics
- Route-overlap comparisons
- Temporal and geofenced sharing grants
- Admin UI
- Source-management UI or in-app iCloud 2FA setup
- OIDC and Keycloak login
- Additional fitness-provider adapters
- Native mobile applications
- Live workout recording
- Workout editing other than deletion
- Awards, goals, streaks, route planning, and social features
- Continuously replicated global OSM data

The following are architectural non-goals at the expected scale:

- Commercial SaaS scale or thousands of users
- Hosting raster map tiles
- OpenSearch for ordinary workout queries
- Valkey or an external message broker for job coordination
- Unnecessary microservice decomposition beyond UI, API, and worker

## Success Criteria

- A new invited user can sign up, sign in, configure a source through Swagger, and complete a Manual Sync.
- Reprocessing the same source files does not create duplicate workouts.
- A bounded historical ingest can process several years of files without staging the whole range at once.
- Summary counts and totals match normalized provider aggregates for representative fixtures.
- Raw routes and path coverage are visible for selected date ranges and workout subsets.
- Each locality-scoped logical path counts a workout at most once, even when several member segments are traversed repeatedly in that workout.
- Fully contained state- and national-park roads and paths retain their individual identities and use the park as City/Region context, including outside municipal boundaries.
- Account isolation tests prevent cross-account access to records, tiles, jobs, logs, exports, and notifications.
- Source secrets, GPS coordinates, and health values do not appear in logs or telemetry.
- Ordinary API queries complete within 500 ms at p95 under a documented representative homelab benchmark.
- Summary becomes usable within 2 seconds and the initial map within 3 seconds under that benchmark.
- Pan and zoom remain interactive while ingest or OSM jobs run.
- Failed sync and deletion jobs produce actionable notifications and redacted diagnostics.
- API, worker, and database upgrades preserve existing imported data through versioned migrations.

The release benchmark must record dataset size, normalized route-point count, account count, concurrent request mix, pod resources, active worker load, cache state, and measurement window so these targets remain reproducible.

## Constraints

- The product runs in a homelab Kubernetes cluster with outbound internet access.
- There is one primary deployment environment based on the `main` branch.
- Gitea Actions runs tests and builds images.
- Images are pushed to Harbor as `workouts-ui`, `workouts-api`, and `workouts-worker`.
- Images receive an immutable eight-character commit-SHA tag and a mutable root-`VERSION` semver tag.
- Argo CD deploys immutable SHA tags using values from `homelab-apps/workouts-explorer/values.yaml`.
- The application Helm chart lives under `helm/` and is namespace-agnostic.
- PostgreSQL/PostGIS, SMTP, OTel Collector, Prometheus, and Alertmanager already exist in the homelab.
- OSM hierarchy and derived paths live in a separate PostgreSQL/PostGIS database.
- Raw Health Auto Export files remain in iCloud Drive or an NFS archive and are not retained by the application.
- Rclone iCloud support is experimental, needs periodic interactive reauthentication, and may be blocked by upstream ADP issues.
- Private route data must never be exposed through unauthenticated vector-tile endpoints.

## Open Questions

- What date range should a newly registered user see before a last-used preference exists?
- What nearest-path distance threshold gives the best results across representative routes?
- Which exact OSM import and road/path derivation tools best preserve the required node, way, and relation hierarchy?
- Which fixed coverage bucket boundaries best distinguish common and rarely visited paths?
- How stable are Health Auto Export workout UUIDs across regenerated exports and format versions?
- What additional normalized workout types and aggregates emerge from a broader historical sample?
