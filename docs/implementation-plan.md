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
- When verification needs a temporary Docker container and the Docker daemon is unavailable, try a disposable daemonless Buildah container with `buildah from` and `buildah run` before marking the check unavailable; clean up the working container and temporary artifacts afterward.

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
| Before private vector-tile deployment | ADR 0012 | Martin function publication, API proxying, least-privilege database access, and cluster-only routing are accepted. |
| Before Milestone 6 Map implementation | ADR 0011 | Theme-aware style families, workout-type defaults, fallback and override behavior, provider access, and private-overlay restoration are accepted. |
| Before Milestone 7 OSM schema/bootstrap | ADR 0008 | The measured importer, derivation schema, segment identity, refresh, and promotion design are accepted. |
| Before Milestone 7 matching acceptance | ADR 0009 | Curated fixtures establish concrete matching thresholds, quality rules, tie-breaking, and rule versioning. |
| Accepted 2026-09-13; amended 2026-09-14 | ADR 010 | Six fixed date-range buckets, paired aggregate/focus palettes, legend labels, and backend tile semantics are concrete; distribution and visual checks validate the contract. |

ADRs 0001 through 0007 and ADRs 0011 through 0012 are accepted. ADRs 0008 through 0010 remain
Proposed until their stated acceptance evidence is recorded. A proposed record's
status must change before dependent application work begins.

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
- Add the discriminated OpenAPI config schema for the initial
  `health-auto-export-local` type; add iCloud to the union in Milestone 8.
- Implement local/NFS source path validation and high-priority connection check.
- Implement source statuses, source update generation, tombstone deletion, and current-config replacement.
- Implement the PostgreSQL worker claim, lease, heartbeat, and terminal-cleanup lifecycle.
- Implement parent ingest and source-child jobs for a selected local source.
- Create independent encrypted snapshots for connection checks and source-child
  jobs, then clear them atomically on every terminal outcome.
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
- Source updates do not alter active snapshots, and no terminal job retains one.

### Verification focus

- Golden parser tests for all supplied samples
- Duplicate route timestamp preservation
- Transaction rollback for malformed files/workouts
- Unit normalization and suspicious-unit warnings
- Source secret encryption and response redaction
- ADR 0007 envelope tampering, key-version, rotation, and source-generation race tests
- Account-scoped SQL tests

### Completion evidence (2026-08-05)

- Schema 1-to-5 migration, upgrade, RLS, API, worker, parser, and UI suites pass against PostgreSQL 18.
- The `xdev` deployment imported all three supplied NFS fixtures into five workouts, three workout types, and 788 route points.
- An unchanged deployed reimport produced five `matched_unchanged` events and retained five workouts.
- Encrypted source configuration contains no plaintext path, and terminal jobs retain neither snapshots nor leases.
- Responsive Summary behavior is covered by desktop table, mobile expansion, date-range, sorting, pagination, empty, and error-state tests.

## Milestone 4: Durable Data Sync Workflow

### Outcome

Manual and scheduled Data Sync are reliable, bounded, observable, cancellable, and diagnosable.

### Vertical slice

- Expand `/api/ingest` to selected source sets and parent/child aggregation.
- Implement incremental and bounded reprocessing semantics.
- Coalesce equivalent active jobs by normalized parameters.
- Implement per-account, per-worker, and PostgreSQL-coordinated global file limits.
- Expand snapshot cancellation, lease-recovery, and startup-scavenging behavior
  for multi-source workflows.
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

### Completion evidence (2026-08-06)

- Schema 1-to-6 migration, upgrade, tenant isolation, job-state, scheduler, file-slot, recovery, API, worker, and UI suites pass against PostgreSQL 18.
- The `xdev` deployment completed a two-source parent sync, reused an equivalent active request, and persisted independent source-child progress and diagnostics.
- A repeat incremental sync skipped all 34 unchanged files, while a bounded one-day sync deliberately reprocessed its matching export.
- A database-leased scheduled sync created one parent with two source children, completed as a silent no-data success, and advanced the next cadence boundary.
- Queued cancellation cleared its child work, and retry created linked parent and child jobs using current source configuration.
- Owner Data Sync APIs and UI expose safe history, progress, files, events, logs, freshness, notifications, cancellation, and retry without source configuration or lease data.

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

### Progress (2026-08-07)

- Added the owner-only chronological workout provenance contract and responsive
  Summary action/dialog flow, including normalized compact or dashed UUID input,
  historical source context, safe warning details, stale-workout refresh, and
  cross-account/API/UI coverage.
- Added versioned normalized points downloads in provider sequence with duplicate
  timestamp preservation, canonical-unit field names, every retained accuracy
  value, private no-store attachment responses, short safe filenames, and a
  route-aware buffered Summary export action.
- Added schema 7 route summaries with fenced worker replacement and historical
  backfill, persisted bounds and elevation derivation, and standard contextual
  GeoJSON downloads that use 3D coordinates only for complete-altitude routes
  and otherwise emit a consistent 2D LineString.
- Added the schema 8 individual-deletion foundation: immediate API-only logical
  hiding, persistent identity tombstones, runtime-gated claiming, exact-target
  fenced purge, reimport suppression, safe failure notifications, durable job
  detail, and optimistic Summary confirmation with exact rollback on enqueue
  failure. Extended the same job model to immutable explicit-range target sets,
  case-sensitive `DELETE` confirmation, atomic multi-target purge/progress, and
  terminal retries that reuse only the originally captured pending targets.

## Milestone 6: Raw Route Map

### Outcome

Users can explore selected raw workout routes efficiently on desktop and mobile.

### Vertical slice

- Implement session-scoped map selections, extent calculation, expiration, and account authorization.
- Import route geometry into private vector-tile functions.
- Split persisted route geometry when a positive point-to-point timestamp gap is at least three times the running average for its current segment, so paused and resumed workouts do not render false connecting lines.
- Emit zero-length route components as point features in private vector tiles so repeated resumed coordinates remain visible and hoverable instead of disappearing from line rendering.
- Deploy cluster-internal Martin as `workouts-explorer-tiles` with least-privilege database access and function-only publication.
- Implement the authenticated API tile proxy and private cache policy.
- Add account data-generation cache busting.
- Replace the singleton base-map settings with validated, extensible style-family runtime configuration, paired light and dark variants, structured attribution, provider resource origins, a fallback family, and provider-label workout mappings.
- Seed MapTiler Outdoor, customized MapTiler Streets, and Stadia Alidade Smooth families, with Alidade Smooth as the default fallback.
- Build the Map view with public base-map styles, visible active-style attribution, automatic workout-type selection, and a current-visit manual selector.
- Synchronize the active style variant with the application theme and restore private sources and layers after style replacement without resetting map state.
- Constrain browser provider access through generated content-security policy origins and document that browser provider credentials are public and provider-restricted.
- Implement date synchronization, workout filtering, Routes mode, type colors, oldest-to-newest order, topmost hover, and purple full-route highlight.
- Implement desktop controls and mobile bottom sheet.
- Keep one MapLibre instance mounted while immutable route selections change; update the vector source in place and preserve private sources/layers through public style transforms.
- Use one compact workout list for visibility, per-route fitting, automatic workout-family selection, synchronized hover highlighting, and delayed route detail popups.
- Keep empty maps interactive with browser-location centering and a contiguous-US fallback.
- Wire Show on map from the workout action menu.

### Acceptance

- A user cannot request another account's map selection or tile.
- The map becomes visible within the target three seconds on representative history.
- Pan and zoom remain interactive during ingest.
- Hovering an overlap selects the newest topmost route.
- Deletion followed by redraw does not show a cached deleted route.
- A single mapped workout type selects its configured family, while mixed, unmapped, and empty route selections use the configured Smooth fallback.
- Light and dark theme changes switch variants without losing the viewport, filters, selection, ordering, hover behavior, or private overlay.
- Manual style selection offers every configured family for the current Map visit on desktop and mobile, then returns to automatic selection on the next visit.
- Active-provider attribution remains visible and current after manual selection and theme changes.
- Route visibility changes do not blank or recreate the map, and clicking a route fits it without hiding other checked routes.
- Sustained map hover highlights the full route and matching list row and shows compact type, distance, time-range, and duration details.
- Paused routes with anomalous timestamp gaps render as separate line segments while retaining their original ordered points and summary bounds.

### Verification focus

- Tile-function account isolation
- Direct Martin network exposure check
- Browser map interaction and mobile layout tests
- Tile cache-generation invalidation
- Representative multi-year rendering benchmark
- Style-family schema, URL, attribution, mapping, fallback, and duplicate-ID validation
- Provider-label normalization and single-, mixed-, unmapped-, and empty-selection resolution
- Theme synchronization, current-visit override reset, attribution switching, and private-overlay survival
- Helm rendering and content-security policy checks for configured provider resource origins

## Milestone 7: OSM Path Coverage

### Outcome

Users can see and tabulate visited roads, trails, and other paths with accurate distinct-workout counts.

### Vertical slice

- Complete the toolchain and storage spike and accept ADR 0008 before creating the OSM schema or promoting a regional extract. ADR 0008 is accepted with canonical multi-region promotion, executable overlap/replacement fixtures, and indexed xdev candidate-query evidence; external update orchestration remains next.
- Provision the public-data-only `osm` database on the production PostgreSQL server for shared development and production reads.
- Bootstrap a selective, reference-complete eligible-path and municipal-boundary import from configured Geofabrik extract `norcal`, using an ephemeral downloaded PBF. The manual schema-4 updater, pinned importer image, failure-safe generation lifecycle, partition preparation/promotion, and retryable storage cleanup are implemented; administrator API/job integration and the next live refresh remain pending.
- Import authoritative locality boundaries and derive deterministic locality-scoped logical paths above matching segments.
- Add offline IANA timezone boundaries. Completed with a pinned, checksummed,
  versioned `timezone-boundary-builder` import, direct route-start lookup,
  offset-safe nearest-workout inference, and restartable ingest/deletion backfill.
- Implement named-region coverage detection, bounded provider-catalog auto-addition, globally coalesced region updates, and raw-route-only pending/unavailable behavior outside promoted regions.
- Chain successful promotions to account/region coverage updates using desired/applied generation watermarks; refresh every intersecting routed workout after an existing region changes.
- Decouple coverage from ingest completion: persist route revision/readiness and transactionally enqueue or coalesce one unclaimed account/region `coverage_update` parent with a `coverage_update_route` child per eligible stale workout revision, then make the workout and raw route available while matching remains pending.
- Add queued, running, current, failed, and stale per-workout coverage state with target/applied route revision, matcher version, OSM generation vector, timestamps, bounded progress, and safe failure categories. Preserve prior valid coverage until a replacement commits.
- Run coverage in a dedicated deployment that claims only `coverage_update_route` children; keep general-worker capacity reserved for connection checks, deletion, and ingest rather than relying on non-preemptive queue priority. Claim fairly across active account/region parents rather than draining one large parent first.
- Process exactly one workout revision per child under one bounded OSM snapshot and no open application write transaction. Revalidate revision, matcher version, and OSM generations in a short fenced persistence transaction; make every route result an independent checkpoint so a failed child cannot stop or roll back siblings.
- Derive coverage-parent status from route children: all successful is `succeeded`, mixed success and failure/cancellation is `partially_succeeded`, all failed is `failed`, and all cancelled is `cancelled`. Treat no-evidence as successful and a stale target as a successful `superseded` child that cannot replace current coverage.
- Persist parent route counters for total, processed, succeeded, failed, cancelled, and superseded. Show those counters and authorized per-workout child status, attempts, duration, and safe failure category in the Data Sync Run detail without exposing coordinates.
- Allow a failed or partially successful coverage parent to retry only failed/cancelled eligible routes using their current route revisions and matcher/OSM target; never rerun successful or superseded routes. Reuse the linear ingest retry contract: the original parent is First, each retry links to its immediate predecessor and root, only the latest parent may be retried, and Run detail navigates every parent by First, Second, ... latest ordinal. Link each retried route child to the corresponding unsuccessful child in the preceding parent. Do not advance the fully applied account/region watermark on partial failure or automatically hot-loop the failed target; retry only by user request or after desired work/OSM state advances.
- Add PostgreSQL-coordinated global, per-account, and optional per-region matcher slots. Default to one active matcher route globally across production and diagnostics and one active production route per account, independent of worker or API replica count.
- Give coverage workers dedicated least-privilege application/OSM credentials, explicit small data pools, reserved claim/heartbeat control capacity, and bounded pool-acquisition, statement, lock, query, and route-child timeouts.
- Implement bounded candidate generation and sequence-aware HMM/Viterbi matching using retained point quality, topology, timing, and reliable heading evidence.
- Derive clipped positive-length segment traversals from decoded transitions; point projections alone create neither attribution nor rendered coverage.
- Tune candidate, emission, transition, gap, and confidence thresholds against representative routes and accept ADR 0009 with concrete rules, tie-breaking, and matcher versioning. Accepted on 2026-09-11 with matcher rules `coverage-experimental-v1` and path policy `coverage-path-policy-experimental-v82`.
- Implement the accepted ADR 010 six-bucket Coverage rendering contract and retain aggregate/visual validation evidence.
- Copy matched segment and logical-path identity, geometry, name, locality, class, and version into the application database.
- Persist one match per workout and physical segment by dissolving overlapping or contiguous traversal spans regardless of direction, retaining disjoint spans as one `MultiLineString`, and computing unique covered length without filling gaps.
- Enforce one workout/logical-path attribution using the earliest positive-length member-segment traversal.
- Implement logical-path daily rollups plus all-time counts and date-only first/latest extrema.
- Implement Coverage vector tiles, paired amber/magenta fixed buckets, delayed exact hover details, and a dual-palette legend.
- Preserve route-level matcher review behind the per-user **Enable coverage diagnostics** preference in a **Diagnostics** preference section. When enabled, Coverage synchronously runs and displays diagnostics for only the selected route; when disabled, Coverage displays durable aggregate coverage and statistics for all checked routes. Keep the existing bounded request timeout, local gate, and fast `429 Retry-After` behavior; additionally require cluster-wide matcher admission so API replicas cannot multiply OSM load. Do not introduce diagnostic jobs or polling in this milestone.
- Implement searchable, sortable, paginated Road Coverage in a modal dialog that preserves the mounted map and supports entity/workout navigation.
- Serve production Coverage tiles and statistics only from durable application data. Poll bounded processing state rather than invoking matching interactively, refresh private capabilities after committed generation changes, and identify omitted pending, failed, or unavailable checked routes when presenting partial coverage.
- Set separate CPU/memory requests and limits for API, Martin, general-worker, and coverage-worker pods. Measure queue age, matcher duration, slot and pool contention, query timeouts, leases, retries, stale-result rejection, and interactive API/tile latency before raising matcher concurrency above one.
- Implement manual OSM status/refresh and copied-segment reconciliation.

### Acceptance

- Repeated traversal and multiple matched segments of one logical path in one workout contribute exactly one count.
- Repeated or jittering traversal of one physical segment produces one match with no duplicated covered length; disjoint visited spans remain separate `MultiLineString` components.
- Equally named roads in different localities produce separate rows; compatible same-name segments within one locality produce one row.
- Unnamed paths appear as N/A.
- Unmatched points remain visible in Routes but create no false coverage.
- Month/year coverage remains interactive at the target scale.
- OSM refresh failure leaves existing copied coverage usable.
- Ingest completion and raw-route availability do not wait for coverage matching.
- A coverage backlog cannot consume general-worker execution capacity or exceed configured database-coordinated matcher slots.
- Each route becomes visible atomically after matching; stale, failed, cancelled, or timed-out work cannot replace prior valid coverage, block sibling routes, or falsely advance the fully applied watermark.
- Mixed route success and failure produces a partially successful coverage parent with accurate route totals and child outcomes in Data Sync Run detail.
- Partial Coverage identifies checked routes omitted because processing is pending, failed, or unavailable.

### Verification focus

- Curated match/no-match route fixtures
- ADR 0008 importer/refresh spike and stable segment reconciliation fixtures
- Locality-boundary splits, same-name cross-city roads, duplicate locality names, and unnamed-path identity fixtures
- Parallel-road and poor-accuracy cases
- Straight intersections, genuine turns, isolated cross-street excursions, grade-separated crossings, and stationary intersection jitter
- ADR 0009 labeled evaluation metrics and deterministic rematch tests
- Segment, locality, and logical-path identity/version reconciliation
- Named-extract resolution, ephemeral download failure, overlap deduplication, and outside-region behavior
- Coverage rollup equivalence to source attribution
- High-density vector-tile benchmark
- ADR 010 distribution analysis and light/dark/mobile visual-regression checks
- Ingest completion with a long coverage backlog and immediate Routes availability
- Dedicated worker-kind claim isolation while connection checks, deletion, and ingest execute
- Global, per-account, and per-region matcher admission races across multiple coverage workers and API replicas
- Fair route-child claiming, cancellation, timeout, selective retry, lease recovery, and sibling progress preservation
- Coverage-parent status derivation and total/processed/succeeded/failed/cancelled/superseded counter races for every child-terminal combination
- Per-route failure isolation, continued sibling execution, no-evidence success, superseded-result handling, and authorized Run-detail disclosure
- Selective coverage retry across multiple ordinals, non-forking latest-only creation, parent first-to-latest navigation, and corresponding route-child lineage
- Stale route-revision, matcher-version, and OSM-generation rejection plus desired-watermark successor coalescing
- Synchronous diagnostic success when capacity is available and bounded `429 Retry-After` behavior when it is occupied
- Summary, Routes, Coverage tile, and ordinary API latency under a representative matching backlog
- Pool and PostgreSQL connection limits, reserved heartbeat capacity, pod resource limits, and saturation metrics

### Progress (2026-09-12)

- Extracted the complete route-level matcher pipeline from the synchronous
  diagnostic API into `internal/coverage/routepipeline`. Sampling, bounded
  overlapping windows, v82 cross-window repairs, exact clipping, invalid-geometry
  filtering, generation provenance, unavailable-region lookup, movement-mode
  inference, and diagnostic limits now form one side-effect-free implementation
  reusable by diagnostics and future `coverage_update_route` workers.
- The diagnostic API retains request admission, timeout, OSM snapshot lifecycle,
  immutable run persistence, labels, response conversion, and summary logging.
  The manual evaluator shares the canonical window size and movement-mode policy.
- Focused and full Go tests plus `go vet ./...` pass after the extraction. Durable
  production job schema, matcher admission, route-child execution, copied coverage
  persistence, rollups, APIs, and UI remain next.
- Migration 16 adds unclaimed `coverage_update` parents, independently leased
  `coverage_update_route` children, immutable route/matcher/OSM targets,
  per-workout queued/running/current/failed/stale state, exact parent route
  counters, fair cross-parent claiming, failure-isolated completion, and selective
  non-forking retries linked at both parent and route-child levels.
- Migration 16 also adds one shared PostgreSQL-coordinated matcher admission pool
  for production and synchronous diagnostics, conservatively defaulted to one
  global, account, and region slot. Coverage parent cancellation now cancels
  queued children, marks running children cooperatively, and preserves the mature
  ingest/source lock order. At the migration-16 checkpoint, production matching
  and copied coverage persistence were not yet connected to these durable jobs.
- Migration 17 adds account-scoped copied logical paths and canonical segments,
  dissolved workout/segment matches, and unique workout/path attributions. A
  fenced persistence function atomically replaces one workout's coverage and
  terminalizes its route child; workers can no longer mark successful coverage
  current through the lower-level completion function. Valid no-evidence results
  atomically remove prior coverage, while failed or stale work preserves it.
- New ingest writes append ready workouts to coalesced coverage parents. The
  dedicated `/app/coverage-worker` deployment claims only route children, loads
  canonical route input through a lease-fenced reader, verifies its digest and OSM
  generation vector, shares matcher admission with synchronous diagnostics, runs
  v82, dissolves repeated traversal intervals, copies bounded OSM metadata, and
  persists each workout independently. At the migration-17 checkpoint, aggregate
  rollups, production tiles/stats, Data Sync coverage details, and historical-route
  backfill remained next.
- OSM migration 7 adds a transactionally emitted promotion-event outbox and seeds
  events for already-active regions. Application migration 18 adds independent
  event/matcher-contract observation, cross-account campaign seeding behind
  security-definer functions, account leases, keyset cursors, route-input repair,
  current region resolution, bounded historical backfill, retry delay, and daily
  safety scans. The dedicated coverage worker runs reconciliation alongside route
  matching without direct account enumeration.
- Production matcher snapshots now constrain candidate and incident-edge lookup
  to the child's exact OSM generation vector and remain open through application
  persistence. Inactive targets complete as superseded, and queued children are
  replaced when either route revision or generation vector changes. Aggregate
  rollups, production tiles/stats, Data Sync coverage details, and the production
  Coverage UI remain next.
- Cumulative migration 18 adds workout-local attribution dates, transactionally maintained
  account/path daily and all-time distinct-workout rollups, immutable map-selection
  range/subset metadata, and a capability-fenced selected-path aggregate read.
  Complete date ranges use daily rollups; explicit checked-workout subsets use
  direct attribution membership. Map generations now advance once on successful
  applied/no-evidence completion rather than on queued, running, failed, stale, or
  cancellation state transitions. Fixed rendering buckets and Coverage MVT remain
  remained blocked on ADR 010 acceptance at that checkpoint.
- Coverage jobs are now owner-visible through the existing Jobs API and Data Sync
  page. History supports a Coverage update filter and system trigger; detail shows
  safe account/region context, route totals, per-workout outcomes and duration,
  events/logs, cancellation, and selective failed/cancelled-route retry. Coverage
  parent retry chains use one-based First/Second/... ordinals and expose every
  attempt for direct navigation without exposing route coordinates or digests.
- Numbered API migrations, OSM migrations, and ADR filenames use uniform
  three-digit prefixes. Reconciliation, rollups, and Coverage MVT are cumulative migration 017;
  migration 018 defines park-attributed unnamed segments as exclusively park-owned,
  with the final explorer filters installed by migration 019. Migration 013 uses
  positional arguments when persisting diagnostics so columns added by later
  migrations cannot collide with its inputs. The next application migration ordinal is 020.
- ADR 010 is accepted with date-range workout buckets `1`, `2`, `3-5`, `6-10`,
  `11-25`, and `26+`, exact tile counts, paired amber and magenta sequential
  palettes, and backend-authoritative assignment. Cumulative migration 017 adds the
  capability-fenced aggregate Coverage MVT contract, and map selections expose separate immutable
  route and Coverage tile URLs.
- OSM importer/derivation versions 3/5 add generation-local bounded local and
  national-park polygon import. Local attribution remains municipality-scoped;
  national attribution is provider-region/source-scoped, spans municipalities,
  and supports segments without a municipality. Derivation 5 gives connected
  same-name national-park segments one logical path across broad-class changes,
  while retaining separate IDs for disconnected components. Application migration 018
  persists nullable-locality regional park visits and rollups. Migration 019
  retains individual unnamed state- and national-park paths and uses the regional
  park as road/path display context ahead of municipality or county. Driveway and parking geometry retains current
  matching and tile behavior but is filtered from user-facing path statistics/history.
- Cumulative migration 013 persists the owner diagnostics preference. With it disabled,
  Coverage uses aggregate checked-workout MVT and the accepted legend/path stats;
  with it enabled, Coverage retains selected-route matcher review. Cumulative
  migration 015 creates readiness/job state and moves matcher execution to the least-privilege
  `workouts_coverage_worker` role while the general worker retains enqueue-only
  authority.
- The xdev `dev-20260913` rollout exercised the storage-constrained rebuild: it
  preserved the OSM catalog and 419 timezone geometries, retired generation 3,
  and promoted validated importer/derivation 2/3 generation 4 with 4,483,073
  segments and 83,137 park-attributed segments. The application database was
  bridged in place from the shipped 22/21 migration history to cumulative 18/17
  without changing workout or diagnostic row counts. Deployment probes also
  hardened the manual update Job's pull/TLS settings, completed the dedicated
  role's metadata and account-context grants, and raised only trusted production
  route input capacity to 50,000 points while retaining the public diagnostics
  limit. Live reconciliation then persisted path, segment, and park attribution.
- Migration 019 separates checked-route geometry from date-range statistics and
  focused-route coverage. Production Coverage hides raw GPS routes, renders
  checked coverage with an amber sequence, overlays focused attribution with a
  magenta sequence, and exposes delayed exact visit details. The Road Coverage
  dialog provides server-side search, sorting, pagination, workout navigation,
  and entity navigation with 500-meter minimum fit bounds and a white blink.
- The original national-park rollout required the schema-19 xdev hotpatch. The
  connected-path derivation requires no application schema hotpatch or function
  signature change: publish OSM/worker images as `dev-20260913`, retire active
  generation 7 for storage, rebuild generation 8 with importer/derivation 3/5,
  and let coverage reconciliation repopulate copied paths, segments, and matches.
- OSM derivation 6 supersedes the national-park-only component pass. After park
  attribution it merges graph-connected existing logical IDs by park, municipality,
  or provider-region scope and normalized name, using one key for all unnamed
  segments. Park-attributed IDs are scope-rebased first so a legacy locality ID
  cannot span the park boundary. Four indexed endpoint joins stage only cross-ID edges; connected
  components contain only participating logical IDs. Broad class and source
  lineage are ignored only across exact graph connections. Validation reports
  edge, affected-ID, and component counts and promotion rejects any remaining
  connected split. Existing disconnected members of one legacy ID are not split.
- OSM derivation 7 corrects derivation 6's over-grouping. Component vertices are
  physical segments rather than incoming logical IDs, and road/path class is part of
  the scope/name key. Connected same-road/path-class named or unnamed segments merge across
  source lineage (including cycleway/footway transitions), while road/path boundaries and every disconnected component
  receive independent deterministic identities even when a legacy ID was shared.
- OSM derivation 8 prevents connected unnamed pedestrian networks from collapsing
  across town. Cross-source edges survive only at simple two-segment continuation
  nodes; branch junctions preserve source-way boundaries while one source way
  remains continuous. Validation reads the exact pruned-edge split count recorded
  during derivation instead of rescanning four endpoint orientations.
- OSM derivation 9 limits branch pruning to unnamed groups. A normalized name is
  sufficient semantic identity at a branch, so graph-connected same-name segments
  merge across source ways even where three or more eligible segments meet. This
  reunifies named roads such as Cupertino's three-way Meteor Drive junction while
  retaining derivation 8's protection for unnamed branching networks.
- OSM derivation 10 preserves an unambiguous exact-class continuation at unnamed
  branches: when exactly two cycleway, footway, trail, or other broad-class
  segments meet, their cross-source edge survives while different-class spurs stay
  separate. This reconnects the Cupertino Mary Avenue/Homestead cycleway without
  reopening town-wide pedestrian-network merging.
- Derivation 11 retains county (`admin_level=6`) polygons alongside municipalities
  (`admin_level=8`), uses the smallest covering polygon, and scopes geometry outside
  municipalities to county before provider region. It also absorbs non-road
  municipality clipping islands up to 25 m when matching pieces of the same county
  immediately precede and follow on one source segment. This removes Parker Ranch
  Trail's 4.15 m Saratoga end cap while labeling real county pockets such as Monta
  Vista as Santa Clara County.
- Derivation 12 fixes overlapping administrative assignment by ordering covering
  polygons by admin level before area and relation ID. Municipalities therefore
  always beat their containing county; this is regression-checked against
  Heatherstone Way and Yorkshire Way in Mountain View.
- Derivation 13 lets normalized names bridge exact road/path graph transitions and
  adds named-road proximity edges within one scope/name: 15 m for ordinary gaps,
  or 50 m only when both segments are explicitly one-way. This merges Yorkshire
  Way's road/cycleway continuation and divided roads such as Grant Road and
  Foothill Expressway while preserving all unnamed class/branch protections.
- Derivation 14 compacts named-road proximity to one nearest edge per scope/name
  and source-way pair, including same-source disconnected pieces. This preserves
  Grant Road's short approach while bounding full-region graph storage.
- Derivation 15 applies park identity scope only to non-road geometry. Roads keep
  municipality/county scope while crossing parks, preventing short park-covered
  pieces from splitting named streets such as Franklin Avenue.
- Derivation 16 imports named school/college/university/education grounds and
  attributes only formally unnamed roads/paths to the smallest containing grounds
  polygon. Education ID defines identity scope and matcher copy exposes the campus
  name through existing name fields, so no application schema migration is needed.
- The storage-constrained derivation-6 rollout retires active generation 8 only
  after coverage/diagnostic/update workers are stopped and copied application
  coverage is preserved, removes its detached canonical leaves and stale build
  schemas, then builds and validates generation 9 with importer/derivation 3/6.
  Restarting reconciliation copies the merged IDs through existing signatures.
- Unrestricted diagnostic OSM snapshots treat both null and empty generation
  arrays as unbounded. Production snapshots continue to fence explicit region
  generations; diagnostics no longer filter every candidate when pgx encodes an
  unset Go slice as SQL NULL.

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
