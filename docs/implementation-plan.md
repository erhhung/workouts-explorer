# Implementation Plan

This plan delivers vertical, testable product slices. It intentionally avoids a file-by-file task inventory. Each milestone should leave `main` deployable and preserve the contracts established in the product, functional, architecture, and ADR documents.

## Delivery Principles

- Keep the OpenAPI document ahead of or synchronized with implementation.
- Implement one complete user outcome at a time across UI, API, worker, database, tests, telemetry, and deployment.
- Use the supplied Health Auto Export samples as fixtures, but add synthetic fixtures for privacy, invalid input, and edge cases.
- Do not log sample payloads or coordinates in CI output.
- Apply every schema change through Goose from the first table onward.
- Keep API and worker separately runnable and resource-isolated from the first executable release.
- Keep account scope explicit in repositories, queries, jobs, tiles, tests, and telemetry.
- Prefer PostgreSQL and PostGIS capabilities before introducing another stateful dependency.
- Deploy immutable image SHA tags even before formal releases.

## Definition Of Done

A milestone is complete when:

- Observable behavior and failure cases match `functional-spec.md`.
- OpenAPI requests, responses, examples, security, and errors are current.
- Generated API artifacts have no uncommitted drift.
- Unit, integration, contract, migration, and applicable browser tests pass in CI.
- New asynchronous work has retry, cancellation, cleanup, metrics, and safe diagnostics.
- New private data has account-isolation tests.
- New schema can migrate from the previous milestone and create from empty.
- Helm and runtime configuration changes are documented.
- No source secrets, health values, or GPS coordinates appear in logs or traces.

## Architecture Decision Milestones

Architecture decisions are accepted at the last responsible moment, before the
schema, public contract, or component boundary that depends on them. A Proposed
ADR records the decision scope and required evidence; it does not authorize
implementation that depends on an unresolved choice.

| Decision milestone | Required records | Exit condition |
|---|---|---|
| Before Milestone 1 implementation | ADR 0002 and ADR 0003 | Tenant isolation, database-role ownership, service packaging, and same-origin routing are accepted. |
| Before the Milestone 1 authentication contract is frozen | ADR 0004 | Browser-cookie, API-bearer, CSRF, and mixed-credential behavior are accepted and represented in OpenAPI. |
| Before Milestone 2 authentication implementation | ADR 0005 | Identity canonicalization, password/token policy, distributed throttling, SMTP failure behavior, and administrator bootstrap are accepted. |
| Before Milestone 2 interface implementation | ADR 0006 | The focused UI spike records the selected styling primitives, theme bootstrap, font, responsive conventions, and accessibility checks. |
| Before Milestone 3 source persistence | ADR 0007 | The reviewed envelope format, key lifecycle, snapshot encryption, and credential compare-and-swap behavior are accepted. |
| Before Milestone 7 OSM schema/bootstrap | ADR 0008 | The measured importer, derivation schema, segment identity, refresh, and promotion design are accepted. |
| Before Milestone 7 matching acceptance | ADR 0009 | Curated fixtures establish concrete matching thresholds, quality rules, tie-breaking, and rule versioning. |
| Before Milestone 7 Coverage rendering acceptance | ADR 0010 | Historical distributions establish concrete fixed count buckets, colors, legend labels, and tile semantics. |

ADRs 0001 through 0006 are accepted. ADRs 0007 through 0010 remain Proposed
until their stated acceptance evidence is recorded. A proposed record's status
must change before dependent application work begins.

## Milestone 1: Executable Skeleton

### Outcome

The repository builds, tests, packages, and runs the UI, API, worker, database migrations, and Helm chart without implementing workout features.

### Vertical slice

- Establish `ui/`, `api/`, `worker/`, `helm/`, root `VERSION`, and shared contract conventions.
- Add the initial OpenAPI 3.0.3 document with public config, Swagger, health, ADR 0004 signin/session schemas, documented placeholder behavior, and RFC 9457 schemas.
- Generate Go API interfaces and request/response types.
- Serve the SPA and safe runtime config using ADR 0003's same-origin production and local-development topology.
- Connect API and worker to PostgreSQL with distinct roles.
- Apply ADR 0002's role ownership and row-level-security foundations before private account-owned tables are established.
- Add the first Goose migration and migration command.
- Specify the complete job status/transition, hierarchy, lease, cancellation, retry, and coalescing model, then add only its PostgreSQL table foundations without processing domain jobs.
- Emit baseline OTel resource, HTTP, process, and database metrics/traces.
- Add structured logging and request/job correlation IDs.
- Build all three images and a namespace-agnostic Helm chart.
- Add a migration PreSync Job template.

### Acceptance

- `/swagger`, `/api/openapi.yaml`, `/api/config`, `/health/live`, and `/health/ready` behave as documented.
- API and worker start independently.
- A migration can create an empty database and rerun without change.
- CI detects stale generated OpenAPI artifacts.
- Images receive commit-SHA and `VERSION` tags.
- Helm renders with ingress disabled by default.
- ADRs 0002, 0003, and 0004 are reflected consistently in migrations, OpenAPI, local topology, containers, and Helm templates.

### Verification focus

- OpenAPI lint and schema validation
- Clean and repeat migration tests
- Container non-root and health behavior
- No secrets in public config or Swagger examples
- Helm template and values-schema tests
- Database role, RLS policy, missing-context, cross-account, and pooled-connection tests

## Milestone 2: Secure Account Lifecycle

### Outcome

An administrator can invite a user, the user can register and sign in, and all later features have a tested tenant boundary.

### Vertical slice

- Apply ADR 0005 to identity migrations and public authentication handlers.
- Apply ADR 0006's accepted UI foundation to public and authenticated interface components.
- Bootstrap the separate administrator identity from Secret-backed configuration.
- Implement invitations, SMTP delivery, signup, unique username/email, and personal account creation.
- Implement Argon2id credentials, signin, HTTP-only cookie, bearer session token, CSRF, signout, and absolute session expiry.
- Implement forgot-password and reset-password with session revocation.
- Implement `/api/session`, `/api/me`, mutable full name, preferences, and proxied Gravatar.
- Establish role and account authorization middleware.
- Add public dark-default login/signup/reset UI and authenticated shell.
- Add the desktop wordmark, About dialog, avatar menu, theme switching, and mobile avatar placement.

### Acceptance

- No public account can be created without an invitation.
- Administrator cannot access data-owner endpoints.
- Data owner cannot access administrator endpoints.
- Cookie mutation without CSRF fails; bearer mutation succeeds without CSRF.
- Password reset invalidates prior sessions.
- Theme, units, timezone, week start, clock, and profile preferences persist.
- Gravatar is fetched through the API rather than directly by the browser.

### Verification focus

- Cross-role and cross-account authorization matrix
- Token expiry, replay, revocation, and generic error behavior
- SMTP failure without account enumeration
- Password-hash parameter tests
- Public endpoint secret scan

## Milestone 3: First End-To-End Workout Import

### Outcome

An owner configures a local/NFS Health Auto Export source through Swagger, imports a file, and sees normalized workouts in Summary.

### Vertical slice

- Accept ADR 0007 before source configuration is persisted.
- Implement envelope encryption and source type-agnostic CRUD.
- Add discriminated OpenAPI config schemas for the two initial source types.
- Implement local/NFS source path validation and high-priority connection check.
- Implement source statuses, source update generation, tombstone deletion, and current-config replacement.
- Implement the PostgreSQL worker claim, lease, heartbeat, and terminal-cleanup lifecycle.
- Implement parent ingest and source-child jobs for a selected local source.
- Implement discovery records and file-at-a-time processing.
- Parse the supplied workout fixtures into normalized workout, type, aggregate, route-point, file, and provenance records.
- Implement source/provider ID upsert and created/updated/matched_unchanged events.
- Implement the first `/api/workouts`, `/api/workout-types`, `/api/summary`, and pagination/sorting behavior.
- Build the first responsive Summary cards and workout table.

### Acceptance

- Creating a source returns checking-connection and asynchronously becomes connected.
- One sample import creates the expected workout count and provider IDs.
- Reimporting an unchanged file creates no duplicate workouts.
- Changed workout content updates in place and appends provenance.
- Provider aggregates win over incomplete sample sums.
- Mobile rows show date/timezone, type, duration, and expandable details.
- Cross-account workout and source access is denied.

### Verification focus

- Golden parser tests for all supplied samples
- Duplicate route timestamp preservation
- Transaction rollback for malformed files/workouts
- Unit normalization and suspicious-unit warnings
- Source secret encryption and response redaction
- ADR 0007 envelope tampering, key-version, rotation, and source-generation race tests
- Account-scoped SQL tests

## Milestone 4: Durable Data Sync Workflow

### Outcome

Manual and scheduled Data Sync are reliable, bounded, observable, cancellable, and diagnosable.

### Vertical slice

- Expand `/api/ingest` to selected source sets and parent/child aggregation.
- Implement incremental and bounded reprocessing semantics.
- Coalesce equivalent active jobs by normalized parameters.
- Implement per-account, per-worker, and PostgreSQL-coordinated global file limits.
- Add job-scoped encrypted source snapshots and terminal clearing.
- Add staging paths, partial download handling, cleanup, and startup scavenging.
- Add scheduled all-account ingest for auto-sync sources.
- Add source freshness and three-day no-data warning behavior.
- Implement cancellation and failed-child retry with current source config.
- Implement Data Sync status, files, jobs, notifications, and redacted log APIs.
- Build Manual Sync, source selection, date-range mode, Data Sync menu, banners, progress polling, retry, and log viewer.

### Acceptance

- A three-source account never processes more than two files concurrently by default.
- A bounded range does not stage all files at once.
- Incremental sync skips unchanged files; bounded Manual Sync reprocesses them.
- One failed source yields partially_succeeded when another succeeds.
- Parent retry includes only failed children.
- Source update does not affect an active job snapshot.
- Source deletion cancels affected jobs and clears snapshots.
- Scheduled success is silent; Manual Sync completion notifies.

### Verification focus

- Worker-kill and lease-recovery tests
- Concurrency tests with multiple worker processes
- Staging traversal and cleanup tests
- Snapshot clearing on every terminal path
- Notification remind state across sessions
- Diagnostic redaction tests using deliberately hostile upstream errors

## Milestone 5: Workout Detail, Provenance, Export, And Deletion

### Outcome

Users can inspect where a workout came from, export normalized route data, and delete private data safely.

### Vertical slice

- Implement full chronological provenance query and dialog.
- Implement normalized points and standard 3D GeoJSON route responses.
- Add download content types and short filenames.
- Derive route bounds, minimum/maximum altitude, and elevation gain.
- Add the ordered three-dot workout action menu.
- Implement individual and explicit-range deletion commands.
- Capture immutable deletion targets, logical hiding, physical cleanup, sanitized audit, and retry.
- Purge detailed workout provenance with workout deletion.
- Add Delete failed notifications and owner-visible diagnostics.

### Acceptance

- GeoJSON contains one LineString with contextual properties and uses 3D coordinates when route altitude is available.
- Points export includes every source point in provider order and all accuracy values.
- Provenance includes created, updated, and unchanged import events.
- Individual deletion disappears optimistically from the initiating UI.
- Range deletion accepts explicit dates only.
- Failed deletion keeps targets hidden and retries the original fixed set.

### Verification focus

- GeoJSON schema and GIS compatibility fixtures
- Compact UUID path/input normalization
- Content-Disposition filename tests
- Delete/retry race tests with concurrent ingest
- Provenance and location-data purge verification

## Milestone 6: Raw Route Map

### Outcome

Users can explore selected raw workout routes efficiently on desktop and mobile.

### Vertical slice

- Implement session-scoped map selections, extent calculation, expiration, and account authorization.
- Import route geometry into private vector-tile functions.
- Deploy cluster-internal `pg_tileserv` with least-privilege database access.
- Implement the authenticated API tile proxy and private cache policy.
- Add account data-generation cache busting.
- Build the Map view with public base tiles and attribution.
- Implement date synchronization, workout filtering, Routes mode, type colors, oldest-to-newest order, topmost hover, and purple full-route highlight.
- Implement desktop controls and mobile bottom sheet.
- Wire Show on map from the workout action menu.

### Acceptance

- A user cannot request another account's map selection or tile.
- The map becomes visible within the target three seconds on representative history.
- Pan and zoom remain interactive during ingest.
- Hovering an overlap selects the newest topmost route.
- Deletion followed by redraw does not show a cached deleted route.

### Verification focus

- Tile-function account isolation
- Direct `pg_tileserv` network exposure check
- Browser map interaction and mobile layout tests
- Tile cache-generation invalidation
- Representative multi-year rendering benchmark

## Milestone 7: OSM Path Coverage

### Outcome

Users can see and tabulate visited roads, trails, and other paths with accurate distinct-workout counts.

### Vertical slice

- Complete the toolchain spike and accept ADR 0008 before creating the OSM schema or importing the California extract.
- Bootstrap California road/path data into the separate OSM PostGIS database.
- Add offline IANA timezone boundaries.
- Implement loaded-region detection and bounded Overpass fallback caching.
- Implement nearest-segment matching using retained point quality.
- Tune the matching threshold against representative routes and accept ADR 0009 with concrete quality rules, tie-breaking, and matcher versioning.
- Choose fixed coverage bucket boundaries from historical distribution and accept ADR 0010 before accepting Coverage rendering.
- Copy matched segment identity, geometry, name, class, and version into the application database.
- Enforce one workout/segment attribution with earliest match.
- Implement daily and all-time account coverage rollups.
- Implement Coverage vector tiles, fixed blue buckets, hover properties, and legend.
- Implement sortable, paginated Path Coverage.
- Implement manual OSM status/refresh and copied-segment reconciliation.

### Acceptance

- Repeated traversal of one segment in one workout contributes exactly one count.
- Unnamed paths appear as N/A.
- Unmatched points remain visible in Routes but create no false coverage.
- Month/year coverage remains interactive at the target scale.
- OSM refresh failure leaves existing copied coverage usable.

### Verification focus

- Curated match/no-match route fixtures
- ADR 0008 importer/refresh spike and stable segment reconciliation fixtures
- Parallel-road and poor-accuracy cases
- ADR 0009 labeled evaluation metrics and deterministic rematch tests
- Segment identity/version reconciliation
- Overpass throttling and cache behavior
- Coverage rollup equivalence to source attribution
- High-density vector-tile benchmark
- ADR 0010 distribution analysis and light/dark/mobile visual-regression checks

## Milestone 8: iCloud/Rclone Source

### Outcome

An owner can configure externally authenticated rclone/iCloud access and use the same sync workflow as local/NFS.

### Vertical slice

- Implement strict whitelisting of required rclone iCloud fields.
- Generate private job-scoped rclone config files.
- Execute rclone without exposing secrets in arguments or logs.
- Discover by safe relative metadata and stage one remote file per slot.
- Persist refreshed cookies into current encrypted source configuration.
- Detect trust-token expiry and ADP/upstream failures.
- Add source-specific safe diagnostics and reauthentication instructions.
- Persist refreshed cookies only when the source generation still matches the job snapshot; discard stale refreshes after source update or deletion.
- Remove job-scoped rclone config files on success, failure, cancellation, and startup scavenging.
- Keep local/NFS behavior as the supported fallback.

### Acceptance

- Rclone source produces the same normalized ingest behavior as local source.
- Updated cookies persist without changing an active job's snapshot.
- Trust expiry sets connection-failed and prevents later ingest.
- Source password, cookies, and trust token never appear in logs, process listings, Swagger responses, or telemetry.

### Verification focus

- Fake rclone process and output fixtures
- Timeout, cancellation, partial download, and token-expiry cases
- Credential redaction across API and worker
- Optional real integration run outside CI when upstream ADP support works

## Milestone 9: Administration And Operational Completion

### Outcome

The installation is operable through Swagger, Argo CD, telemetry, and documented runbooks without an admin UI.

### Vertical slice

- Complete admin user list, invitation resend/revoke, and asynchronous account deletion that cancels and drains account jobs before capturing purge targets.
- Implement all-user in-app announcements, expiration, independent acknowledgement, and retraction.
- Complete OSM status and refresh diagnostics.
- Add all required OTel metrics, traces, dashboards, and suggested Alertmanager rules.
- Add source freshness, expired credentials, stuck job, cleanup failure, OSM failure, API latency, and tile latency alerts.
- Complete Helm resources, network policies where supported, service accounts, Secrets, probes, PodDisruptionBudget decisions, and emptyDir limits.
- Add backup/restore and migration runbooks.
- Add Argo CD multi-source example documentation for app chart plus `homelab-apps` values.

### Acceptance

- Admin can operate every MVP lifecycle feature through Swagger without seeing private data.
- Announcement expiration and retraction remove user-visible messages.
- Account deletion immediately disables access and eventually purges private records.
- Argo CD blocks rollout on failed migration.
- Dashboards and alerts distinguish product incidents from source-user action such as expired iCloud trust.

### Verification focus

- Admin privacy and log-access tests
- Account purge completeness
- Helm install/upgrade/rollback rendering
- Backup restore followed by migration
- Alert rule tests from synthetic metrics

## Milestone 10: MVP Hardening And Release

### Outcome

The documented MVP is secure, performant, recoverable, and ready for sustained personal use.

### Vertical slice

- Run the complete historical NFS import with production-like limits.
- Define and commit a reproducible benchmark profile covering the 2022-present 5.4 GB source archive, normalized record counts, synthetic multi-account load, pod resources, request mix, cache state, and concurrent worker activity.
- Tune indexes, SQL, rollups, matching, and tiles from measured traces.
- Validate responsiveness while ingest and OSM work run.
- Run cross-account security tests across every private resource and tile route.
- Run dependency, container, OpenAPI, and migration compatibility checks.
- Exercise failed source, worker crash, database restart, deletion failure, SMTP failure, and OSM failure runbooks.
- Finalize resource limits and retention from measured operation; matching distance and coverage buckets are already resolved in Milestone 7.
- Reconcile all documentation and publish a coordinated semver release.

### Acceptance

- Ordinary API queries meet 500 ms p95 under the documented benchmark profile.
- Summary is usable within two seconds and initial Map within three seconds under the same profile.
- Historical ingest completes without exhausting configured temporary storage.
- No critical or high security finding remains unaddressed.
- Restore and migration procedures recover a representative backup.
- Every functional-spec acceptance criterion is mapped to an automated or documented verification.

### Verification focus

- End-to-end user and admin journeys
- Multi-year import and map benchmark
- Security boundary and log/telemetry leak audit
- Failure injection and recovery
- Release artifact and GitOps provenance

## Post-MVP Sequence

Post-MVP work begins only after separate product and authorization design. Likely order:

1. User-facing source and admin management UI
2. OIDC/Keycloak authentication provider
3. Additional source adapters and normalization mappings
4. Sharing grants with explicit temporal and geospatial exclusions
5. Side-by-side statistics and route-overlap comparison
6. Optional awards, route planning, native clients, or richer elevation analysis

No MVP schema or API should imply that sharing rules have already been designed.
