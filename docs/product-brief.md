# Product Brief

## Problem

Fitness platforms record individual workouts and GPS routes, but they do not make it easy to explore a lifetime of workouts as geographic coverage. A person who intentionally explores new streets, trails, and paths cannot readily answer:

- Where have I worked out during a selected period?
- Which roads and paths have I covered, and how often?
- Which nearby paths have I not visited?
- How has my activity changed across days, weeks, months, and years?

Workouts Explorer imports workout records and GPS tracks, normalizes them independently of the source provider, and presents both raw routes and path-level coverage on an interactive map.

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
3. The user selects Routes or Coverage.
4. Routes are colored by workout type; hovering highlights the topmost, most recent workout route.
5. Coverage uses fixed blue count buckets so frequently visited paths do not flatten the rest of the scale.
6. The user filters the map to any subset of workouts.
7. The user inspects segment visit counts, first and latest visits, and the Path Coverage table.

### Keep data synchronized

1. The user creates a source through the authenticated REST API.
2. A high-priority connection check validates the source asynchronously.
3. Scheduled sync discovers and imports new or changed files from all auto-sync sources.
4. The user can run Manual Sync for a selected set of sources and an optional explicit date range.
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
- `health-auto-export-icloud` source using an externally configured rclone session
- Per-source `autoSyncEnabled` behavior
- High-priority asynchronous connection checks
- Scheduled incremental sync of new or changed files
- Manual incremental or bounded historical sync
- Parent ingest jobs with one child per selected source
- Server-side deduplication of equivalent active jobs
- File-at-a-time staging and guaranteed cleanup
- Idempotent workout upsert using source and provider workout ID
- Complete, sanitized import provenance
- Retry, cancellation, polling, notifications, and redacted diagnostic logs

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

- Responsive desktop and mobile map
- Raw route and path-coverage modes named Routes and Coverage
- Workout subset filtering
- Automatic extent fitting with configurable padding
- Route colors by workout type and bright-purple hover highlighting
- Newest route rendered on top
- Nearest-path matching against all relevant public map paths
- One attribution per workout and path segment using the earliest match
- Fixed blue count buckets for coverage
- Authenticated private vector tiles
- Path Coverage table with sortable statistics
- Public or privately cached base-map tiles

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
- Each path segment counts a workout at most once, even when traversed repeatedly in that workout.
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
