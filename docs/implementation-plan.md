# Implementation Plan

This plan delivers vertical, testable product slices. It intentionally avoids a file-by-file task inventory. Each milestone should leave `main` deployable and preserve the contracts established in the product, functional, architecture, and ADR documents.

Every UI slice starts with the [UI style guide](ui-style-guide.md) and the closest
existing composition. Include visual and behavioral consistency in implementation
and review rather than treating it as a later styling pass.

## Milestone Sequence Update (11/5/2026)

**Stats And New Coverage** is the new Milestone 9. Administration/operations,
tablet UI/UX and MVP verification/release readiness move to **10, 11 and 12**.
**GitHub Project Publication** is the new **Milestone 13**, and production
deployment/scaling/security moves to **14**. Publication includes GitHub Actions
CI, public release artifacts and human-facing installation/usage documentation.
Forward references throughout the documentation use this sequence. Historical
decision dates, measured results and completed M1-M8 acceptance remain unchanged.
M9 Stats/New Coverage is complete and accepted; M10's first admin authentication/
workspace slice is deployed, with remaining administration in progress. Public
publication remains M13 work. See the [foundation checkpoint](development-progress.md#milestone-10-foundation).
See the [admin UI specification](admin-ui-spec.md),
[M9 closeout](development-progress.md#milestone-9-closeout)
and retained [initial implementation record](development-progress.md#stats-and-new-coverage-initial-implementation).

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

## Canonical Display Identity (030)

[ADR 0013](decisions/013-canonical-coverage-display-identity.md) is accepted and
**deployed** behavior, including the click-highlight follow-up. Current source
combines compact/canonical display in **030** and ingest status/reprocessing/phases
in **029**, with **001-028 unchanged**. Final source/runtime support and
schema/minimum runtime are **30**, baseline **27**, ledger **0-30**. See the
[history mapping](database-migrations.md#current-source-layout) and
[pre-cutover actual status](database-migrations.md#historical-deployment-checkpoints).
The authorized live reconciliation is pending here. M8 is complete with explicit user
approval and the final qualified live browser gate **PASS**; see
[authoritative closeout](development-progress.md#milestone-8-closeout).

The following are enduring release requirements, not a new deployment receipt.
Protected actual-backup migration/preservation, actual-role readiness and matching
database/image rollback proof, retention and completed deployment evidence are
recorded in the private canonical migration handoff under **pre-squash historical
numbering** ([mapping](database-migrations.md#current-source-layout)). These are
not fresh squash proofs. The earlier **521 UI tests**
and focused **219 MapPage tests** receipts remain historical within their original
scope, not a new full-suite pass or a substitute for database/browser proof.

- Verify the generally applicable display key, pinned Unicode policy/UUIDv8 and
  collision rejection, unnamed nonmerging, mixed park/path presentation and source
  provenance with synthetic fixtures only. Cover all historical membership,
  history-only fallback, distinct-workout union, date ties and actual gap-preserving
  geometry consistently across list/stats/detail/focus/all four tile layers.
- Verify preparation and cache policy/revision guards, eight soft unreferenced
  contexts plus active pins, deletion, rollback, deferred all-input invalidation,
  one revision/generation advance per account/transaction, both READ COMMITTED
  commit orders and stale Repeatable Read `40001`. Complete full source/race,
  database-backed readiness and desktop/mobile browser verification separately.
- Monitor workspace, `/tmp`, NFS and application-PG filesystems/inodes regularly.
  Keep dumps/archives in approved protected storage with the private operator
  handoff, not on constrained workspace storage. Rehearse
  native migration, preservation, actual-role readiness and matching database/image
  rollback on a protected actual-backup restore before any live mutation.
- Retain fresh protected backups and old images, drain incompatible runtimes and
  coordinate schema/minimum runtime 30 with compatible API/general-worker/Coverage-
  worker and UI. Record cutover completion and read-only deployed validation;
  historical policy deployment is complete, but squash cutover remains pending.
  Preserve TLS/migration/Martin
  flags/identity, replica policy, auto-sync, admin-password debug, resources,
  concurrency, timeouts and retries.
- Warm/rebuild canonical contexts from retained evidence for validation. This is
  Coverage read-model recomputation, **not new route jobs**: no OSM derivation,
  matcher or path-policy bump, and **no OSM regeneration/download/rematch needed**.
  Do not start imports, provider checks or OSM work to adopt the display policy.

See [deployed progress](development-progress.md#canonical-display-identity-030),
[architecture](architecture.md#canonical-display-identity-requires-30),
[functional contract](functional-spec.md#canonical-display-identity-030),
[click-highlight contract](functional-spec.md#click-highlight-follow-up)
and [migration requirements](database-migrations.md#canonical-coverage-display-030).

## Coverage Display Optimization (030)

The deployed optimization retains all Road Coverage filtering, sorting and
pagination in the browser. One bounded bulk request replaces the repeated page
fetches and duplicate full-result counts. Migration 030 precomputes compact
matched-evidence partials, independent regional context and park bounds; live
eligibility/metadata joins and entity/workout deduplication precede summarization
for arbitrary date ranges. Dirty-key maintenance batches replacement work at
transaction commit, including underlying evidence and segment updates.

## Ingest Reprocessing Follow-Up (029)

The approved follow-up covers implementation and coordinated deployment of
[Ingest Reprocessing](ingest-reprocessing.md), not design-only exploration.
Its compatible API/worker/UI behavior is deployed; current source combines this
contract in **029**, with final schema/minimum runtime **30**, baseline **27**.
The approved live reconciliation remains pending here. The requirements below
describe source adoption, not instructions to downgrade an unreconciled live
runtime. Earlier backend-42/UI-43 rollout/browser receipts remain historical,
not current release proof. M8 acceptance and the
final live browser pass are recorded in
[closeout](development-progress.md#milestone-8-closeout); capacity is not certified.
The separate all-popups **Dismiss** requirement remains outside this ingest
design scope and is not accepted by its historical deployment receipt.

- Default new bounded jobs to metadata skipping within inclusive export-file
  dates; retain existing incremental, ownership-epoch and account/source rules.
- Add the bounded-only unchecked **Reprocess all files in this date range**
  checkbox below both date inputs and above Start sync, with the exact hover/focus
  tooltip in the contract. `IngestCreate.reprocessAllFiles` is an optional boolean
  only with both valid ordered dates; even false presence is invalid incrementally.
- Persist explicit false/true on new bounded parents/children. Keep absent flags
  on existing bounded jobs as force, without another contract marker, parameter
  backfill or frozen-manifest rewrite. Preserve queued historical compatibility.
- Use API coalescing v2 to separate force, dates, sorted sources/generations and
  fresh roots/retry parent/source-child ancestry, with existing sorted source-row
  locks and active uniqueness, not a new lock.
- Always check valid same-file/metadata ancestor-path success before force on
  retry; exclude siblings/other branches. Forced retries process failed, unreached
  or changed files despite unrelated older inventory. Non-forced retries retain
  valid inventory skips, including original intentional skips.
- Protect private per-file `observed_identity`/`ownership_blocked_epoch` receipts
  stamped from the admitted commit capability and exact frozen manifest. Ancestor
  proof must survive later inventory-pointer/blocker changes; legacy NULL receipts
  require sufficient current inventory/manifest proof or a safe extra download.
  Retain immutable terminal checkpoints and no-double-count recovery.
- Preserve workout deduplication and route identity; force is not guaranteed
  Coverage rematching. Keep metadata fingerprints distinct from parsed-byte local
  SHA-256. Pinned rclone iCloud 1.75.2 has no remote hashes/free MD5; unchanged
  ID/size/mtime overwrites can go undetected without explicit reprocessing.
- Verify omitted/false/true inputs, invalid presence/type/dates, isolated coalescing,
  both retry policies, durable/legacy proof, stale blockers, frozen recovery,
  counter restoration, identity preservation and checkbox hover/keyboard access
  with synthetic January 2025 inputs. Preserve 029 outcomes: an unsuccessful
  zero-created/zero-updated run stays Failed even with zero failed files.
- Take backups, use the complete native source sequence through 030 for compatible
  histories (separate approved reconciliation for the historical live ledger), and
  coordinate compatible API/general-worker/Coverage-worker images and matching UI.
  Final source support/schema/minimum runtime become 30, marker stays 27. Verify history,
  role readiness and queued-job compatibility; do not claim live success from
  disposable tests or attempt an incompatible image-only rollback.

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
| Accepted 9/13/2026; amended 9/14/2026 | ADR 010 | Six fixed date-range buckets, paired aggregate/focus palettes, legend labels, and backend tile semantics are concrete; distribution and visual checks validate the contract. |

ADRs 0001 through 0012 are accepted. Future proposed records must have their
stated acceptance evidence recorded and status changed before dependent
application work begins.

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

### Completion evidence (8/5/2026)

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
- Implement incremental and bounded metadata skipping, with explicit bounded reprocessing under the 029 follow-up contract.
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
- Incremental and new bounded sync skip unchanged successful metadata; explicit bounded reprocessing opts into the old force behavior, subject to valid ancestor-success skips on retry.
- An unsuccessful ingest yields partially_succeeded only with created or updated workouts under migration 029; reads/skips/unchanged workouts do not qualify.
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

### Completion evidence (8/6/2026)

These are historical observations **before explicit ingest reprocessing**. In particular, the bounded
one-day reprocessing observation retains the original force default; it does not
describe the new default or become a synthetic replacement observation.

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

### Progress (8/7/2026)

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

### Status (10/11/2026)

Complete under the user-approved scope in
[Acceptance Decisions](#acceptance-decisions-10112026). Core functionality is
accepted; remaining administration, tablet, benchmark and production work is
assigned to Milestones 10-12 and 14. Public distribution is Milestone 13.
Automatic region addition remains indefinitely
deferred. Scope completion does not turn unexecuted checks or historical budget
misses into passing evidence or certify production capacity/security.

### Outcome

Users can see and tabulate visited roads, trails, and other paths with accurate distinct-workout counts.

### Vertical slice

- Complete the toolchain and storage spike and accept ADR 0008 before creating the OSM schema or promoting a regional extract. ADR 0008 is accepted with canonical multi-region promotion, executable overlap/replacement fixtures, and indexed xdev candidate-query evidence; the operator-run resumable updater is implemented.
- Provision the public-data-only `osm` database on the production PostgreSQL server for shared development and production reads.
- Bootstrap a selective, reference-complete eligible-path and municipal-boundary import from configured Geofabrik extract `norcal`, using an ephemeral downloaded PBF. The pinned importer image, failure-safe resumable generation lifecycle, partition preparation/promotion, and retryable storage cleanup are implemented and exercised through derivation 20/generation 60. Administrator status/refresh API and job integration move to Milestone 10 (renumbered after the 10/4/2026 scope decision); they are not unfinished Milestone 7 operator execution.
- Import authoritative locality boundaries and derive deterministic locality-scoped logical paths above matching segments.
- Add offline IANA timezone boundaries. Completed with a pinned, checksummed,
  versioned `timezone-boundary-builder` import, direct route-start lookup,
  offset-safe nearest-workout inference, and restartable ingest/deletion backfill.
- Implement active-region Coverage detection and raw-route-only unavailable behavior outside promoted regions. Automatic provider-catalog region downloads and their bounded, globally coalesced update orchestration are indefinitely deferred by the 10/4/2026 scope decision in ADR 0008. Operators add regions only after conscious selection and storage preparation; promotion still triggers reconciliation.
- Chain successful promotions to account/region coverage updates using desired/applied generation watermarks; refresh every intersecting routed workout after an existing region changes.
- Decouple coverage from ingest completion: persist route revision/readiness and transactionally enqueue or coalesce one unclaimed account/region `coverage_update` parent with a `coverage_update_route` child per eligible stale workout revision, then make the workout and raw route available while matching remains pending.
- Add queued, running, current, failed, and stale per-workout coverage state with target/applied route revision, matcher version, OSM generation vector, timestamps, bounded progress, and safe failure categories. Preserve prior valid coverage until a replacement commits.
- Run coverage in a dedicated deployment that claims only `coverage_update_route` children; keep general-worker capacity reserved for connection checks, deletion, and ingest rather than relying on non-preemptive queue priority. Claim fairly across active account/region parents rather than draining one large parent first.
- Process exactly one workout revision per child under one bounded OSM snapshot and no open application write transaction. Revalidate revision, matcher version, and OSM generations in a short fenced persistence transaction; make every route result an independent checkpoint so a failed child cannot stop or roll back siblings.
- Derive coverage-parent status from route children: all successful is `succeeded`, mixed success and failure/cancellation is `partially_succeeded`, all failed is `failed`, and all cancelled is `cancelled`. Treat no-evidence as successful and a stale target as a successful `superseded` child that cannot replace current coverage.
- Persist parent route counters for total, processed, succeeded, failed, cancelled, and superseded. Show those counters and authorized per-workout child status, attempts, duration, and safe failure category in the Data Sync Run detail without exposing coordinates.
- Allow a failed or partially successful coverage parent to retry only failed/cancelled eligible routes using their current route revisions and matcher/OSM target; never rerun successful or superseded routes. Reuse the linear ingest retry contract: the original parent is First, each retry links to its immediate predecessor and root, only the latest parent may be retried, and Run detail navigates every parent by First, Second, ... latest ordinal. Link each retried route child to the corresponding unsuccessful child in the preceding parent. Do not advance the fully applied account/region watermark on partial failure or automatically hot-loop the failed target; retry only by user request or after desired work/OSM state advances.
- Add PostgreSQL-coordinated global, per-account, and optional per-region matcher slots. Default to one active matcher route globally across production and diagnostics and one active production route per account, independent of worker or API replica count.
- Give coverage workers dedicated least-privilege application credentials, explicit small data pools, reserved claim/heartbeat control capacity, and bounded pool-acquisition, statement, lock, query, and route-child timeouts. Dedicated OSM reader credentials for production Coverage and diagnostics belong to the final production/security milestone, Milestone 14; xdev temporarily shares the updater credential without relaxing application-database isolation.
- Implement bounded candidate generation and sequence-aware HMM/Viterbi matching using retained point quality, topology, timing, and reliable heading evidence.
- Derive clipped positive-length segment traversals from decoded transitions; point projections alone create neither attribution nor rendered coverage.
- Tune candidate, emission, transition, gap, and confidence thresholds against representative routes and accept ADR 0009 with concrete rules, tie-breaking, and matcher versioning. Accepted on 9/11/2026 with matcher rules `coverage-experimental-v1` and path policy `coverage-path-policy-experimental-v82`.
- Implement the accepted ADR 010 six-bucket Coverage rendering contract and retain aggregate/visual validation evidence.
- Copy matched segment and logical-path identity, geometry, name, locality, class, and version into the application database.
- Persist one match per workout and physical segment by dissolving overlapping or contiguous traversal spans regardless of direction, retaining disjoint spans as one `MultiLineString`, and computing unique covered length without filling gaps.
- Enforce one workout/logical-path attribution using the earliest positive-length member-segment traversal.
- Implement logical-path daily rollups plus all-time counts and date-only first/latest extrema.
- Implement Coverage vector tiles, paired amber/magenta fixed buckets, delayed exact hover details, and a dual-palette legend.
- Preserve route-level matcher review behind the per-user **Enable coverage diagnostics** preference in a **Diagnostics** preference section. When enabled, Coverage synchronously runs and displays diagnostics for only the selected route; when disabled, Coverage displays durable aggregate coverage and statistics for all checked routes. Keep the existing bounded request timeout, local gate, and fast `429 Retry-After` behavior; additionally require cluster-wide matcher admission so API replicas cannot multiply OSM load. Do not introduce diagnostic jobs or polling in this milestone.
- Implement searchable, sortable, paginated Road Coverage in a modal dialog that preserves the mounted map and supports entity/workout navigation.
- Serve production Coverage tiles and statistics only from durable application data. Poll bounded processing state rather than invoking matching interactively, refresh private capabilities after committed generation changes, and identify omitted pending, failed, or unavailable checked routes when presenting partial coverage.
- Set separate CPU/memory requests and limits for UI, API, Martin, general-worker, Coverage-worker, and OSM/operator pods. The starting envelope is requests **25m CPU / 64Mi memory**, limits **250m CPU / 256Mi memory**, including chart hook containers. Keep shared matcher concurrency at one. Retain privacy-safe queue-age, matcher/route/admission timing, timeout/outcome, lease, retry, stale-rejection, pool-wait, and tile-latency instruments (see `coverage-metrics.md`); record a representative resource-capped backlog/interactive-latency run before raising concurrency. Full dashboards/alerts belong to Milestone 10, typical/stress benchmarking to Milestone 12, and final production sizing/hardening to Milestone 14.
- Complete the operator-run OSM status, refresh/rebuild, and copied-segment reconciliation lifecycle. The remaining administrator status/refresh API and job/status contract belong to Milestone 10, not Milestone 7.

### Acceptance

- Repeated traversal and multiple matched segments of one logical path in one workout contribute exactly one count.
- Repeated or jittering traversal of one physical segment produces one match with no duplicated covered length; disjoint visited spans remain separate `MultiLineString` components.
- Equally named roads in different localities produce separate rows; compatible same-name segments within one locality produce one row.
- Unnamed paths retain locality and stable identity and use type-specific fallback names: Unnamed road, Bike path, Pedestrian path, Trail, or Unnamed path.
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

### Progress (9/12/2026)

- Extracted the complete route-level matcher pipeline from the synchronous
  diagnostic API into `internal/coverage/routepipeline`. Sampling, bounded
  overlapping windows, v82 cross-window repairs, exact clipping, invalid-geometry
  filtering, generation provenance, unavailable-region lookup, movement-mode
  inference, and diagnostic limits now form one side-effect-free implementation
  reusable by diagnostics and the subsequently delivered `coverage_update_route` workers.
- The diagnostic API retains request admission, timeout, OSM snapshot lifecycle,
  immutable run persistence, labels, response conversion, and summary logging.
  The manual evaluator shares the canonical window size and movement-mode policy.
- Focused and full Go tests plus `go vet ./...` passed after the extraction. Durable
  production job schema, matcher admission, route-child execution, copied coverage
  persistence, rollups, APIs, and UI were subsequent work at that historical checkpoint
  and are now delivered.
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
  Coverage UI were subsequent work at that checkpoint and are now delivered.
- Cumulative migration 18 adds workout-local attribution dates, transactionally maintained
  account/path daily and all-time distinct-workout rollups, immutable map-selection
  range/subset metadata, and a capability-fenced selected-path aggregate read.
  Complete date ranges use daily rollups; explicit checked-workout subsets use
  direct attribution membership. Map generations now advance once on successful
  applied/no-evidence completion rather than on queued, running, failed, stale, or
  cancellation state transitions. Fixed rendering buckets and Coverage MVT
  remained blocked on ADR 010 acceptance at that checkpoint and are now delivered.
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
  migrations cannot collide with its inputs. Migration 020 aligns facility identities
  across explorer reads. On 10/6/2026, the development-only Coverage metadata grants
  were consolidated into migration 015; the separate 021 file was removed after
  compatibility checks and coordinated xdev bookkeeping repair. Schema support
  ended at 020 with minimum runtime 18 at that historical checkpoint. The next
  ordinal was 021; M8 now uses it for the distinct iCloud forward migration,
  with schema/minimum runtime 21, not a restored Coverage/tile migration.
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
  limit. The current 027 baseline aligns diagnostics with production at 50,000
  original / 12,000 sampled points through shared pipeline limits, without
  changing request budgets or deployment policy. Live reconciliation then
  persisted path, segment, and park attribution.
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

### Closeout Scope (10/4/2026)

- Core OSM generation, matching, durable persistence, reconciliation, Coverage
  rendering, Road Coverage, facility/state-park identities, and selective retry
  are implemented. Row-level readiness icons and disabled-row reasons satisfy
  partial-coverage omission reporting; no separate omission summary is required.
- Ten-second no-result readiness polling is complemented by bounded background
  generation checks for displayed current/stale results. Private capabilities are
  replaced on committed data-generation changes even if readiness labels are unchanged.
- Automatic region addition is indefinitely deferred; `osm.autoAddRegions` stays
  false. Missing promoted data leaves raw routes available and result-less Coverage
  disabled. Dedicated OSM reader credentials are deferred to Milestone 14 before
  production deployment; application role isolation remains required in xdev.
- All application/OSM components start with the same 25m/64Mi requests and
  250m/256Mi limits. These are provisional dev settings, not measured production
  capacity; importer stages and concurrent API password hashing especially need
  resource-capped validation. PostgreSQL resources/PVCs are not changed here.
- Small Coverage lifecycle and tile measurements plus existing DB-pool metrics
  are the Milestone 7 observability scope. Matcher concurrency remains one;
  dashboards/alerting move to Milestone 10, profile benchmarking to Milestone 12,
  and final production sizing/scale tuning to Milestone 14.
- The CoverageRunner end-to-end integration matrix passes ten cases and
  three race repetitions and runs in its own CI job. Month/year backend reads
  are recorded; earlier host full-length matcher correctness is separate from
  current profile receipts, whose matcher-smoke fields are null. Resource-capped
  backlogs, theme/device/color-vision checks, real-device/native-zoom checks and
  overlapping general-worker responsiveness were closeout gates at the earlier
  checkpoint. The 10/11/2026 acceptance decisions below carry them forward;
  unexecuted checks remain unmeasured rather than passing evidence. See
  `coverage-acceptance.md` and its reports.
  The administrator status/refresh API and job/status contract are
  formally deferred by the 10/4/2026 decision, now Milestone 10; the completed operator
  lifecycle and automatic copied-segment reconciliation remain in Milestone 7.
- Representative per-user acceptance uses `coverage-acceptance.md`: typical
  365/year with 329 routed workouts, stress 730/year with 511 routed workouts,
  requested activity/distance/duration mixtures and no repeated daily type.
  Both fixtures and backend reads are verified; the stress year Coverage tile
  misses the provisional budget. The retained runs contain no warm serial phase
  or latency retry. Full profile-specific acceptance remains unmeasured and is
  carried forward by the explicit decisions below, not claimed as a pass.

### Acceptance Decisions (10/11/2026)

These user-approved decisions supersede the corresponding closeout gates above
and complete Milestone 7's agreed scope. Unexecuted checks remain unmeasured,
not passing evidence. The future milestone assignments below are required work,
not removed requirements or a production-readiness claim.

- **Resource-capped backlog: accepted for Milestone 7.** The current worker
  correctness evidence and conservative settings are sufficient for this milestone.
  Move the representative 48/96-route backlog and CPU/memory/throttling/peak-resource
  measurements to Milestone 12. Matcher concurrency remains one; this decision
  does not certify throughput, spare capacity or production sizing.
- **Mixed-workload responsiveness: accepted for Milestone 7.** Dedicated Coverage
  execution and shared matcher admission controls are sufficient for this milestone.
  Move sustained Summary/Routes/Coverage/API latency and general-worker
  ingest/check/delete responsiveness during matching, including competing
  diagnostics, to Milestone 12. No overlapping-load responsiveness pass is claimed.
- **Profile benchmarking and visual matrix: accepted for Milestone 7.** Move
  typical/stress benchmarking and profile-specific theme/color-vision visual
  review to MVP verification, now Milestone 12. The stress year tile and viewport
  budget misses and unmeasured initial-map render timing remain recorded, not
  converted to passes. Tablet UI/UX development and its device-specific visual
  review belong to Milestone 11. iPad-sized devices and larger are the supported mobile target;
  iPhone-sized UI/UX development is explicitly out of scope.
- **Real-device and native-zoom checks: accepted for Milestone 7.** Move supported
  iPad device, rotation, keyboard and browser-zoom validation to Milestone 11,
  and desktop native-zoom regressions to Milestone 12. The existing UI is accepted
  for this milestone without claiming that the unexecuted matrix passed.
- **Production scaling and security hardening: deferred to Milestone 14.** The
  final milestone owns production deployment, capacity/scaling, final resource
  and retention settings, least-privilege OSM reader credentials, and production
  security controls. Earlier milestones retain application authorization,
  account isolation and privacy regression requirements; this is not permission
  to weaken existing boundaries or treat xdev exceptions as production-ready.

## Milestone 8: iCloud/Rclone Source

### Status

**Complete.** Explicit user approval and the confirmed final qualified live browser
gate **PASS** close all three M8 closeout items; no further owner decision is
pending. iCloud ingest and independent available-map Coverage recovery are
verified. Current source targets **001-030**, schema/minimum runtime **30**, baseline
**27**, ledger **0-30**; the [authorized live reconciliation](database-migrations.md#historical-deployment-checkpoints)
is pending here, without reopening M8. The authoritative
[Milestone 8 closeout](development-progress.md#milestone-8-closeout) records the
deployed behavior, image lineage, browser/cleanup boundaries and private evidence
retention. Historical results below remain scoped receipts, not new full-suite,
capacity, load or failover passes. M10 administration, OSM administration and full
monitoring/runbooks remain future work; M7/M12 benchmark exclusions are unchanged.

### Approved Source Adoption (Requires 26)

The implementation/deployment-status paragraph below is a historical checkpoint
**before explicit ingest reprocessing**. The [current status](#status) and
[030 source release](#canonical-display-identity-030) supersede its version/status
statements, not its recorded verification boundaries.

Latest migration code is **26**. The approved identity/adoption feature requires
schema and minimum runtime **26**, with migrations 024-026 and coordinated API,
general-worker and Coverage-worker images. Integration is still in progress;
this documentation update is not verification of a completed integration or a
runtime-26 release. Current deployment remains **23** until coordinated rollout.
[Development Progress](development-progress.md) owns live-version, deployment and
operational evidence. Historical status/results below retain their original
schema, scope and failures; they do not establish adoption or live-cloud acceptance.

The new approved contract supersedes the earlier source-scoped provider-ID rule:

- Enforce uniqueness by account, provider namespace and opaque provider ID. Local
  and iCloud transports share `health-auto-export`. Same-source unchanged content
  records a match; changed content updates the same UUID.
- Skip another live owner's identity without duplicate creation, payload changes
  or ownership theft. Live means `deleted_at IS NULL`, not enabled/connected.
  A single deleted-source orphan can be adopted under the same UUID, preserving
  historical source/file/job provenance and applying authoritative incoming
  header/aggregate/route content. Source deletion is a soft tombstone, not a NULL
  workout source; API source type remains immutable. No workout wipe is needed.
- Fail migration on legacy duplicate provider identities and fail ambiguous
  resolution without silent merges or arbitrary UUID selection. Pending/completed
  workout deletion tombstones prevent resurrection. ID-less fallback stays
  conservative and source-local, without automatic cross-source adoption.
- Retain successful checksums for all-blocked and mixed files. Any
  `skipped_live_owner` records `ownership_blocked_epoch` captured before ownership
  decisions. Same-epoch incremental jobs skip unchanged metadata without
  reread/download; only a live-to-deleted source transition advances the account
  epoch. Adoption, cookie refresh and auth/config generations must not advance it.
- A fresh incremental job reprocesses stale ownership-blocked files despite
  identical metadata/bytes. Same-job manifest actions remain frozen, terminal
  file checkpoints never reopen and recovery counters/events do not double-count.
  Deletion starts no immediate sync; the next auto-sync or a new manual job is
  needed. Before explicit reprocessing, bounded manual jobs already forced eligible files to
  reprocess; new bounded defaults now use metadata skipping, with force opt-in.
- Every successful source-switch adoption records a durable Coverage request in
  the same file transaction, including unchanged content/route. Post-commit ingest
  repair or leased reconciliation consumes the outbox, with event/lease/input
  fencing; no fake hash or source/workout-to-Coverage lock inversion is allowed.
  Missing map data retains a deferred obligation, not a false match result.
- Count `adopted` as Updated and `skipped_live_owner` as Unchanged in immediate and
  recovered progress, preserving explicit owner-private events for the difference.
  Keep 500m Coverage CPU, two execution lanes, existing admission caps and
  6m/10m/15m timeouts; this feature does not increase capacity or certify cloud smoke.

Implementation interfaces are in
[`024_provider_workout_identity.sql`](../api/migrations/sql/024_provider_workout_identity.sql),
[`025_owner_aware_source_inventory.sql`](../api/migrations/sql/025_owner_aware_source_inventory.sql)
and [`026_adopted_workout_coverage.sql`](../api/migrations/sql/026_adopted_workout_coverage.sql).
The worker uses `app.resolve_ingest_workout_identity`,
`app.read_ingest_candidate_actions`, `app.record_successful_source_object` and
`app.record_adopted_workout_coverage_refresh`; `replayAdoptedCoverage` consumes
committed requests from ingest repair or leased reconciliation. Ownership epochs
advance only for successful source deletion, never adoption or credential refresh.
Versioned fences preserve admitted transaction authority past wall-clock expiry
without allowing an expired lease to acquire new write authority.

Verification gates, not new passing receipts:

- Fresh/repeat/upgrade schema-26 migration, runtime readiness/ACL/RLS and fail-closed
  legacy-conflict checks, preserving old UUIDs and append-only history.
- Same-source unchanged/changed, local-to-iCloud and reverse adoption,
  disabled/disconnected live-owner skips, fallback isolation and pending/completed
  deletion suppression, with no provider IDs or raw payloads in diagnostics.
- Same-epoch no-download, deletion-only epoch changes, pre-decision captured-epoch
  races, identical-metadata reread, all-blocked/mixed files, frozen recovery
  manifests, terminal checkpoints and no duplicate immediate/recovered counts.
- Changed/unchanged adoption outbox atomicity, rollback, crash replay, newer-event
  protection, missing-map deferral and normal Coverage lease/admission fencing.
  The [Local A / Cloud B worked example](icloud-source-setup.md#worked-source-switch-example)
  is the expected behavior, not a real-account smoke receipt.

Keep real accounts, workout counts, IDs, dates/GPS, private keys and private URLs
out of Git. Synthetic test cases do not certify upstream authentication, live
iCloud, deployment or production acceptance. No new application test run, image
publication or live operation is claimed by this documentation update.

### Status (10/13/2026)

This is a historical checkpoint; [current M8 status](#status) supersedes its
pending acceptance, unchecked README and deployment statements, not its receipts.

**Ready for owner review.** Implementation and synthetic verification are ready:
ordinary verification, scoped race tests, actual disposable PostGIS M8 suites,
local worker-image/native checks and synthetic built-UI browser flows pass within
the scope recorded below. Owner acceptance is pending; M8 is not closed and its
README completion row stays unchecked. No live-cloud or production validation,
image publication or redeployment is claimed or authorized by this progress note.
The 10/12 verification below remains historical schema-21 evidence. At this
checkpoint, code supported schema 22/minimum runtime 21 and the deployed
function-only repair retained schema/Goose 20/minimum runtime 18.
As of 10/13/2026 (`America/Los_Angeles`), the
owner-requested repair and normal failed-only retries are verified complete: all
affected routes have current durable matches and no failed/queued repair work
remains. Private identity and counts are omitted. M8 image builds, publication and
deployment have not started in this phase; no release tag is selected. Owner
review and explicit image/rollout authorization remain required. The implemented
[OSM reader-only feature](osm-read-replicas.md) is off by default. Live headless
DNS/TCP and a separate direct-backend TLS/authentication client are verified in
the telemetry guide; in-pod TLS/authentication is not certified, and reader
activation/rollout remain pending.

### Accepted Decisions (10/11/2026)

The source-scoped identity decision here is historical and superseded by
[Approved Source Adoption](#approved-source-adoption-requires-26). These decisions
are **before explicit ingest reprocessing**: its bounded-force default is superseded by the opt-in policy
in [Ingest Reprocessing](ingest-reprocessing.md). The observations and original
verification boundaries remain historical evidence, not new verification.

- Use a job-scoped native rclone 1.75.2 CLI, not a daemon/RC service or embedded
  library. Pin release archives and verify separate amd64/arm64 SHA-256 values.
- Use one application flat metadata discovery per sync. Incremental skipping
  compares remote ID, size and modification time, accepting that a same-metadata
  overwrite is undetectable; bounded Manual Sync forces matching dates. Native
  stat/download may list internally, so no one-listing/one-HTTP guarantee exists.
- Require a completed external session: closed version-1 path, write-only Apple
  ID/obscured password/full cookies/trust token, optional non-secret client ID.
  Fix native backend/service to `iclouddrive`/`drive`; reject arbitrary profiles,
  flags, descriptions and intermediate `_auth_session`. At this M8 checkpoint,
  source CRUD UI and in-app 2FA were out of scope; owners submitted whitelisted
   fields through Swagger/REST. The owner-profile **Data Sources** UI delivered at
   Helm 167 supersedes the CRUD-UI exclusion, not the external authentication/2FA
   boundary; it remains separate from the [M10 admin workspace](admin-ui-spec.md).
- Persist only refreshed cookies with first-writer-wins generation/lease fencing;
  keep independently encrypted snapshots immutable. Publish refreshed cookies and
  successful connection-check status atomically.
- Add immutable forward migration `021_icloud_sources.sql`, schema/minimum runtime
  21, fencing runtime-20 claims and pre-upgrade scheduler leases. Preserve M7's
  historical schema-20/minimum-18 evidence and the tile consolidation history.
- Retain the default 24-hour auto-sync cadence (optionally 12 hours), independent
  of UI polling. At this historical checkpoint, separate local/iCloud sources
  retained separate deduplication identities; that identity policy is superseded
  by the approved contract requiring 26. Do not silently merge or purge workouts.

### Outcome

An owner can configure externally authenticated rclone/iCloud access and use the same sync workflow as local/NFS.

### Vertical slice

- Implement strict whitelisting of required rclone iCloud fields.
- Generate private job-scoped rclone config files.
- Execute rclone without exposing secrets in arguments or logs.
- Discover by safe relative metadata and stage one remote file per slot.
- Persist refreshed cookies into current encrypted source configuration.
- Distinguish known trust/auth/explicit PCS approval failures from transient upstream failure; only the former disconnect a source during ingest. Explicit checks retain the asynchronous failed-check contract.
- Add source-specific safe diagnostics and reauthentication instructions.
- Persist refreshed cookies only when the source generation still matches the job snapshot; discard stale refreshes after source update or deletion.
- Remove private configs, backups, endpoint caches and staged exports before terminal outcomes and on startup scavenging. A local cleanup failure blocks that attempt's terminal writes and that worker's new claims/readiness until confirmed; encryption/refresh write errors preserve recoverable work. Pod-local gates/scavenging are not distributed cleanup, lost-volume guarantees or physical erasure.
- Keep local/NFS behavior as the supported fallback.

### Implementation Present

- Typed discriminated OpenAPI requests/public responses, closed canonical config
  validation, credential-preserving PATCH, account isolation and safe redaction.
- Linux native adapter with credential-free argv, private environment/config,
  bounded processes/output/retries, flat metadata validation, pre/post download
  checks, atomic staging and cleanup. Bounds: 64 MiB files, 100000 entries,
  10000 eligible exports, 32 MiB stdout, 60-second metadata, five-minute download.
- Worker source-check/ingest integration, local-parser parity, metadata checkpoints,
  cookie-only encrypted refresh, safe failure classification and forward-migration
  functions for typed snapshots, atomic check completion, refresh and runtime fencing.
- Synthetic suites for adapter processes/metadata/cleanup, schema/PATCH/redaction,
  API lifecycle, worker lifecycle, migration CAS/scheduler/lease races and readiness
  faults. Passing results and their boundaries are recorded below, separately
  from wider database-suite baseline failures.
- [Owner setup and recovery guide](icloud-source-setup.md) and
  [contributor verification commands](../CONTRIBUTING.md#icloud-source-verification).

### Verification Results (10/12/2026)

| Verification | Recorded result | Boundary |
|---|---|---|
| Final `make verify` | Exit 0; all-package Go tests, Go vet, generation, formatting, typecheck, Go/UI builds, Helm checks and 303 UI tests pass | Ordinary suite with database URLs unset; database-backed skips are not PostgreSQL passes |
| Final scoped race suite | Exit 0 for `./internal/sourceconfig ./internal/rclonesource ./internal/database ./api ./worker ./worker/cmd/worker` | Database URLs unset; ordinary unit/race coverage |
| Disposable PostgreSQL 18.6 / PostGIS 3.6.4 M8 suites | 40 worker M8 tests across normal/race receipts; 138 internal full-race tests; 32 M8 SQL/readiness/race tests; four API integration passes | Actual database-backed receipts in `m8-verify/run2`; worker M8 runs have no M8 exclusions/skips, not a blanket wider database-suite pass |
| Migration/readiness | Fresh 001-021, repeat migration, v1 upgrade and 020-to-021 SQL paths pass; API, worker and Coverage runtime roles unready at 20 and ready at 21 | Isolated disposable database, not deployed upgrade acceptance |
| Worker image/native | Full Buildah build passes; all seven commands compile; amd64 release checksum matches, native version is 1.75.2; Alpine UID 65532 execution passes 41 top-level tests plus 60 subtests without failures/skips | Retained locally as `localhost/workouts-worker:m8-verification`; no publication/redeployment or arm64 execution |
| Private staging bind root | Actual UID/GID `0:65532`, mode-`2770` bind root; `ScavengeStaging` creates private child `65532:65532`, mode `2700` (setgid plus `0700` access); synthetic `Source.New` version-only setup/cleanup passes | Not full worker startup, read-only-root-filesystem/seccomp verification or a cloud call |
| Connected browser MCP | Built UI assets with synthetic API fulfillment in new isolated contexts at 1366x900 and 768x1024 pass friendly cloud label, connected default selection, disabled expired-cloud checkbox, exact two-ID cloud/local POST excluding expired source, and task navigation; zero page errors | Synthetic frontend verification; zero real-cloud calls or deployment changes |

The final status receipts are `/tmp/opencode/m8-verify-final.status` and
`/tmp/opencode/m8-local-verify-final.status`, both `0`. Temporary receipts and the
ignored `deploy/internal/browser-checks/m8-synthetic-check.js` browser harness are local artifacts,
not committed acceptance artifacts or durable backup evidence. This repository
summary records their scope without private output or payloads.

After the four API integration passes, a new recovery-copy unit assertion failed
on the wording `external` versus `externally`. Production recovery copy was fixed;
the final API unit/race checks pass. PostgreSQL integration was **not rerun after
that message-only fix**; database semantics did not change. Earlier database
receipts must not be represented as a fresh post-copy-fix run.

### Legacy Coverage Repair (10/13/2026)

- Added forward-only `022_legacy_coverage_bootstrap.sql` for routes excluded from
  reconciliation because they lacked coverage state. The repair uses the real
  canonical digest, concurrent-ingest/lease fences and 50,001-point overflow
  readers; it preserves function signatures, owners and ACLs without new grants
  or helper fields. Campaign wakeup preserves live cursors and newer desired work.
- Recorded disposable PostGIS verification covers 27 test cases in each mode:
  full migration to schema 22/minimum runtime 21 and function-only schema-20
  compatibility. Fresh schema-22 readiness passes; deployed old-runtime readiness
  remains preserved. These receipts do not replace the 10/12 M8 test counts or
  establish a fresh whole-suite/application-image pass.
- The deployed repair applied only the marked five-function block and separately
  woke only the operator-selected account. Schema/Goose remains 20/minimum runtime
  18; no M8 source DDL, source ingestion, import/delete/purge or OSM rebuild was used.
  Map-data readiness is resolved, but first-pass matching included timeouts.
- Explicit owner-API failed-only retries are **verified complete**, with all
  affected routes current and no remaining failed/queued repair work. The supplied
  final verification receipt reports successful completion (status `0`); private
  account identifiers, health counts and route/job inventory remain outside Git.
  Existing per-child timeout-retry policy grants the first timeout retry six
  minutes and the second ten with the configured three-minute base; successful children
  are skipped. Resources, base timeout and global/account/region limits `2/2/2`
  remain physically unchanged. This normal runtime-derived parent/child retry
  behavior is not a concurrent-capacity benchmark or a reader-feature deployment.

See the [repair runbook](coverage-legacy-route-repair.md) for the compatibility
boundary, private receipt retention and deployment gate. External source profiles
remain unregistered; host keyring/rclone configuration was not read for this repair.

### OSM Reader Policy (10/13/2026)

- The revised chart contract retains global `osm.useReadReplicasOnly: false`
  solely as a boolean connection policy. Readers use complete `{name, key}`
  references: `worker.osmDatabaseSecret`, `coverageWorker.osmDatabaseSecret` and
  `api.coverageDiagnostics.databaseSecret`, each defaulting to name
  `workouts-explorer-database` and key `osmReaderDatabaseUrl`, under either policy
  value. Each is independent of application-database references and the other
  readers, even with shared default names. True enforces strict standby
  validation; false uses normal driver behavior with the same selected URL and
  may reach the primary if an operator-chosen all-node URL permits it. The boolean
  never chooses a Secret or overrides an endpoint. The closed schema rejects
  removed component key-only properties without migration aliases; historical
  source mappings and previously removed global-key cleanup are in the operator guide.
- OSM migration/timezone-import hooks independently use
  `osm.writerDatabaseSecret: {name: workouts-explorer-database, key: osmDatabaseUrl}`
  and `osm.writerDatabaseTLSSecret: workouts-explorer-database-tls`. Their role
  must be authorized for migrations/imports, not a read-only runtime login.
  Operator Jobs keep writer environment selectors `OSM_DATABASE_SECRET`,
  `OSM_DATABASE_KEY` and `OSM_DATABASE_TLS_SECRET` with those same defaults;
  they are independent of Helm reader references and never inferred from private
  Helm values. Application-database selectors remain unrelated and unchanged.
- Existing values require operator-only conversion of all three old component
  key-only overrides: explicitly set the new reader name and key, then remove the
  old property. Previously borrowed application Secret names are no longer
  inherited. Deliberately replace overrides retaining a writer member with a
  reader member. Verify reader entries privately and provision only if absent;
  shared Secrets can retain application, reader and writer members, or dedicated
  OSM reader roles can use separate Secrets with existing reader TLS properties.
  Preserve connection-pool settings and reader profiles. Use a complete freshly
  reviewed values file, not stale `--reuse-values`; agents must not read or edit
  private local values.
  See [the migration guidance](osm-read-replicas.md#existing-values-and-minimal-example).
- Configuration propagates `OSM_USE_READ_REPLICAS_ONLY`. Strict boolean/schema
  checks reject invalid values and the reader-only/auto-add conflict; auto-add
  stays false. New connections resolve/copy/shuffle all headless A/AAAA results,
  preserve verified TLS/fallback order, and require standby role at connection
  and pool checkout without primary fallback. Held transactions cannot reroute
  or prevent mid-transaction promotion. Safe connect/acquire telemetry remains;
  unnamed role probes avoid raw role-check traces and named prepared statements.
- Coverage preflights before claim when no replica is usable. Missing/lower heads
  wait under five-second snapshot attempts/two-second polling, with renewing
  claim/slot heartbeats; genuinely newer heads supersede only after no target is
  behind. The match budget starts after a valid snapshot. Waiting holds matcher
  admission and can block ready matches; readiness/reconciliation can lag too.
- Recorded focused config, protocol/TLS/privacy fixtures and
  disposable PostGIS lag/wait integration checks pass. Simulated replica heads
  are not streaming-replication, live DNS/authentication or capacity proof.
  Connection shuffling does not guarantee per-job balance, and the flag does not
  increase worker threads, replicas or matcher limits. No unexecuted benchmark
  is marked passed; newly evaluated capacity starts at one before a separately
  approved increase to two.
- Revised Helm/schema verification must cover default/custom complete component
  reader references independently of application Secrets, each other and both
  policy values, enabled/disabled diagnostics, unchanged separate writer
  name/key/TLS overrides and removed-property rejection without aliases.
  Earlier key-only chart receipts do not establish this replacement contract;
  this documentation-only update does not run chart/runtime tests.
- The exact headless DNS forwarding artifact and reachability proof remain owned
  by [Telemetry and monitoring](telemetry-and-monitoring.md#private-dns-and-reachability).
  Live DNS/TCP and a separate direct-backend authentication/verified-TLS client
  pass within that guide's scope, not in-pod TLS/authentication or mTLS proof.
  Reader activation/rollout remain pending at this checkpoint.
  Shared Ansible HA provisioning is unchanged. The reader-only routing feature
  added no migration at this checkpoint: code schema/minimum was 22/21 and
  deployed compatibility was 20/18. Review
  README and coordinate appropriate API/worker images before any authorized
  migration/rollout. M8 image builds/deployment have not started in this phase.

### Wider Database Exceptions

Three wider database failures were also reproduced on archived user HEAD
`a82377244a1df0ee78efbd49676e08ca4dfbedd3` in an isolated schema-20 baseline:

| Test | Reproduced failure |
|---|---|
| Migration `TestCoverageRouteFailureIsolationMatcherSlotsAndSelectiveRetry` | RLS fixture writes to job logs lack account context |
| API `TestJobRetryLimitAndSourceLineageIntegration` | Returns 200 where the test expects 503 |
| Local worker `TestRunnerManualIngestPostgreSQL/golden_import_and_unchanged_reimport` | Route-summary failure |

Unrelated code remains unchanged. These failures are recorded baseline exceptions,
not passing checks or reasons to describe the entire database integration suite
as green. M7 historical measurements and acceptance decisions remain unchanged.

### Owner Review And Boundaries

The pending-acceptance, unchecked README, image/schema-22 and reader-rollout
statements below record the earlier review phase. They are superseded by
[current M8 status](#status), not current instructions or outstanding decisions;
their original verification and hardening exclusions remain unchanged.

- Owner acceptance and explicit milestone closeout remain pending; keep README
  milestone 8 unchecked until authorized. Synthetic implementation verification
  is ready, not an outstanding implementation blocker.
- An optional owner-operated real-cloud smoke test outside CI requires separate
  authorization. Apple authentication/approval and production behavior have not
  been validated by synthetic receipts.
- The owner-account repair and failed-job retries are verified; this satisfies
  that prerequisite, not M8 acceptance or image/rollout authorization. M8 image
  builds, publication and redeployment have not started in this phase and require
  README review, coordinated schema-22 API/worker images, a selected release tag
  and explicit authorization. The 10/12 local verification image is not a
  deployed M8 release; the 10/13 SQL-only repair did not build or deploy application images.
- Arm64 execution, read-only-root-filesystem/seccomp behavior and full worker
  startup were not verified. The pod-local cleanup/readiness gate is not a
  distributed cross-pod cleanup or lost-volume guarantee. Permanent permission,
  read-only filesystem or hardware faults are not automatically repaired; no
  immediate physical-erasure or production-hardening claim is made.

### Acceptance

- Rclone source produces the same normalized ingest behavior as local source.
- Updated cookies persist without changing an active job's snapshot.
- Trust expiry sets connection-failed and prevents later ingest.
- Apple ID, source password, cookies and trust token never appear in logs, argv/process listings, Swagger responses or telemetry; public config has no masked secret placeholders.

### Verification focus

- Fake rclone process and output fixtures
- Timeout, cancellation, partial download, and token-expiry cases
- Credential redaction across API and worker
- Optional real integration run outside CI when upstream ADP support works

## Milestone 9: Stats And New Coverage

**Complete and accepted 11/12/2026 at Helm 129.** Owner review closes the Stats/New
Coverage slice and its navigation/UX follow-ups; see the authoritative
[M9 closeout](development-progress.md#milestone-9-closeout). Current UI verification
has 907 passing tests, deployed Chromium dark/WebKit light checks and retained-tab
resource/performance evidence. M10 is next planned; the full tablet-device matrix,
capacity/release verification and public publication remain M11-M13 work.

**Initial implementation deployed 11/5/2026 at Helm 82; review was pending then.**
Backend APIs and retained-history reads were implemented and verified before the
UI. All 755 UI tests pass; native API/history and cold-context index-plan checks,
catalog mutation checks and independent actual-backup-restore parity pass.
Live Chromium/WebKit cover both rollups, count details, n/a, search and tab state.
That initial checkpoint was not final milestone acceptance; see the
[implementation record](development-progress.md#stats-and-new-coverage-initial-implementation).

**Review follow-up deployed 11/6/2026 at Helm 83.** Shared typography and Map-style
Month/Year controls, the centered two-thirds table, equal spacing and delayed
header help replace the initial layout. Counts and dialog links use standard
pointer/hover/date formatting; exact identity and in-range workout jumps preserve
the main range and report page. Both pagers retain rows/height/scroll, and expired
data scopes recover from transient renewal failures without remounting. The
[review record](development-progress.md#stats-review-navigation-and-refresh)
retains that checkpoint's verification; out-of-range previous dates are not clickable.

### Outcome

Users can explore statistics and trends through an extensible **Stats** tab
between **Map** and **Data Sync**. The first report, **New Coverage**, distinguishes
first-ever visits from first visits or returns after more than one calendar year.
"Reports" is the navigation term for current tables and future tables/charts.

### Vertical slice

- Add Stats to the primary tab order: Summary, Map, Stats, Data Sync. Reuse the
  Map-style left panel with the shared Date range picker first, then **Reports**
  and selectable report titles. Use the existing magenta selected border plus
  accessible selected-state semantics. New Coverage is initially selected.
- Keep the active report and per-report controls in the parent UI frame so tab
  switches preserve them. New Coverage defaults to **Show new visits by** Month,
  with Year as the other radio option. Share date selection, shortcuts, recent
  custom ranges and saved range behavior with Summary and Map.
- Show every month/year intersecting the inclusive selected range, including zero
  buckets and partial boundary buckets. Match Workout log styling, sortable
  columns and the rows-per-page preference; default to date bucket descending.
- Count named canonical Road Coverage identities using all eligible account
  history for first/predecessor determination, then filter qualifying visits by
  the selected workout-local dates. Reuse one full-selected-range canonical
  context across buckets and details, independently of Map's checked workouts.
- **New visits (all time)** counts first-ever visits. **New visits (past year)**
  counts those same first visits plus returns strictly after the previous visit's
  one-calendar-year anniversary. Clamp February 29 anniversaries to February 28.
  Later repeat visits neither qualify nor replace a qualifying event's date.
- Make each nonzero count's entire cell an accessible drill-down target. Reuse
  Road Coverage dialog/search/table primitives with only the specified name,
  place and event-date columns; add Previous visit for the past-year variant,
  with grayed-out **n/a** for first-ever visits, matching missing Distance in the
  Summary Workout log. Default New visit descending, hide the
  unnamed toggle, narrow the dialog, shrink short tables and support Escape.
- Add bounded account-scoped summary/detail API reads from retained canonical
  visit evidence, generation/context guards and invalidation after data changes.
  Stats must work without mounting Map or fetching tiles or geometry.
- Keep the report registry extensible for future workout, route and Coverage
  tables/charts; only New Coverage is delivered in this milestone.

Detailed behavior and counting examples are in the
[functional specification](functional-spec.md#feature-explore-stats-and-new-coverage);
the [technical design](architecture.md#stats-reports-and-new-coverage-m9)
defines history ordering, context scope, query boundaries and client state.

### Acceptance

- Tab switching restores the active report and Month/Year choice; changing the
  shared range preserves the report choice and resets affected pages.
- 3/25/2024 through 10/1/2026 shows March 2024 through October 2026, or
  2024/2025/2026, while counting only visits on inclusive selected dates.
- First-ever visits enter both columns; a repeat at exactly the anniversary does
  not qualify, and a later visit qualifies only when its immediate predecessor
  is more than a calendar year earlier. Out-of-range history still disqualifies.
- Canonical merged members count once per workout/identity; named education
  identities follow Road Coverage, and unnamed/otherwise excluded entities do not
  enter either column. Each nonzero count agrees with the complete detail total.
- Dialog dates identify qualifying events, not a later visit or all-time latest
  date. Search, sorting, preference-sized paging, keyboard cell activation,
  short-dialog sizing, Escape dismissal and focus restoration work in both themes.
- Foreign/stale scopes and late responses cannot expose another account's data
  or install counts/details from an obsolete range or generation.

### Verification focus

- First-ever/repeat/return histories, multiple workouts on one date, leap-day and
  exact-anniversary boundaries, partial buckets, empty and all-zero ranges
- Independent event/count oracle over all-history deduplicated canonical visits;
  history before range, same-context cross-bucket consistency and count/detail parity
- Account isolation, logical deletion, changed metadata/dates, retained stale
  applied results, successful replacement and failed-rematch preservation
- API validation, bounded pagination/payloads, stale generations and cache races
- Desktop/tablet tab/sidebar/radio/table/dialog journeys, keyboard/focus,
  light/dark themes, preference page sizes and narrow/short dialog layout

## Milestone 10: Administration And Operational Completion

### Outcome

After installation, the administrator can manage accounts, prepare and maintain
OSM regions, communicate service announcements and monitor/recover operations
through a dedicated **`/admin`** workspace. The existing login screen authenticates
reserved username **`admin`** with the bootstrap administrator's password, without
granting access to any normal user's private data.

**Scope revision accepted 11/15/2026:** the original no-admin-UI assumption was
made before the number and complexity of required administrative tasks became
clear. Manual REST/Swagger and CLI use remains available for automation/recovery,
but is no longer the normal administration acceptance path. The
[admin UI specification](admin-ui-spec.md) owns the detailed feature contract.

### Vertical slice

- Build four primary tabs, **Accounts, OSM Maps, Operations, Settings**, with
  Stats-style left navigation for second-level categories and the existing shared
  UI compositions, themes, retained state and scroll/focus behavior.
- Extend shared login and session handling for canonical `admin` reservation,
  server-authorized role routing and independent owner/admin cookie sessions.
  Provide an explicit audited transition for an existing differently named
  bootstrap administrator; fail on unresolved canonical collisions, without
  converting an owner or resetting credentials during startup.
- Complete user/invitation directories, invitation creation/resend/revoke and
  delivery recovery, plus suspend/reactivate and session revocation. Password
  recovery stays self-service; the admin reset-email shortcut was dropped on
  11/17/2026. Do not expose private user data or impersonation.
- Implement asynchronous account deletion that immediately denies access, cancels
  and drains account jobs before capturing purge targets, and durably retries
  failures without re-enabling the account.
- Implement all-user in-app announcements, expiration, independent acknowledgement,
  retraction and sanitized administrative audit/history views.
- Complete the administrator OSM API and durable task/status contract moved from
  Milestone 7: provider-catalog selection, initial download/preparation, refresh,
  stage/generation history, resume/retry, storage inspection/unblock, explicit
  candidate abort, cleanup recovery and safe reconciliation/readiness summaries.
  Reuse the existing updater/checkpoints/promotion lifecycle and global locks.
- Add a separate internal OSM job-controller Deployment with a dedicated
  namespace-scoped ServiceAccount/Role for approved updater Jobs. Keep Kubernetes
  Job-launching authority out of the public API and existing workers; specify and
  verify template/admission restrictions, restart-safe reconciliation and owned-only
  cleanup. Job creation RBAC alone cannot constrain an embedded pod specification.
- Build Operations overview, administrative tasks/events/logs, incidents,
  announcements, audit and contextual recovery guidance. Do not widen owner job
  APIs into a private cross-account task browser.
- Limit Settings to independent administrator console preferences/access and a
  curated **read-only** effective-configuration view. Helm/GitOps/Secrets remain
  authoritative for deployment settings; no SMTP/base-map/key/resource/policy
  editors, shadow overrides or write-back into infrastructure configuration.
- Add all required OTel metrics, traces, dashboards, and suggested Alertmanager rules.
- Add source freshness, expired credentials, stuck job, cleanup failure, OSM failure, API latency, and tile latency alerts.
- Complete Helm resources, network policies where supported, service accounts, Secrets, probes, PodDisruptionBudget decisions, and emptyDir limits.
- Document operator prerequisites for the final production milestone. Dedicated
  OSM reader credentials, enforced production network boundaries and final
  production security/scaling validation belong to Milestone 14, not this
  administration slice.
- Add backup/restore and migration runbooks.
- Add Argo CD multi-source example documentation for app chart plus `homelab-apps` values.
- Keep release-oriented Helm installation guidance, including initial base-map
   selection and MapTiler keys, in M13. The owner-profile **Data Sources**
   configuration UI is delivered separately at Helm 167 and grants no administrator authority.

### Delivery sequence

The first authentication/workspace slice is deployed at Helm 153: shared login,
reserved identity/adoption, independent sessions, four-tab/left-nav shell, initial
read-only directory/invitation sending and effective configuration. Remaining
steps below are not claimed complete; see [feature status](admin-ui-spec.md#implementation-status).

The Helm-155/156 extension adds forward schema/minimum runtime 32, durable login
metadata, local registration dates, expressly bounded account usage and real
invitation resend/revoke. It includes a protected pre-migration backup, drained
worker window and all affected runtime rollouts. Helm 159 adds audited disable/enable,
cookie/bearer revocation and ordinary job cancellation without automatic revival;
verified disabled credentials receive fixed sign-in feedback below the fields.
The explicitly requested account-access fold into 002 preserves schema/minimum
runtime 32/32 and ledger 0-32 after actual-backup rehearsal. Helm 173 subsequently
delivers durable account deletion, full private purge and fresh re-registration
through schema/minimum runtime 34/34, with Deleting/pending in-place polling.
Helm 174 subsequently folds lifecycle/source-check foundations into canonical
001-032 with schema/minimum runtime 32/32; Helm 179 delivers conditional session
revocation. The admin-triggered recovery shortcut is dropped in favor of existing
self-service recovery. Full administration detail and OSM/Operations delivery
still follow this sequence.

1. Synchronize admin OpenAPI/schema/auth contracts, reserve `admin`, provide the
   identity transition and build shared-login routing plus the isolated admin shell.
2. Deliver complete Accounts journeys, including invitation delivery, suspension,
   session revocation and interruption-safe account purge. Recovery is self-service.
3. Deliver the OSM controller and full region preparation/recovery lifecycle using
   the existing updater, with real Kubernetes permission/template verification.
4. Complete Operations tasks, announcements, audit, monitoring gaps and runbooks;
   deliver Settings without infrastructure configuration drift.
5. Verify complete admin journeys, privacy boundaries, Helm/GitOps behavior and
   failure recovery before marking M10 complete.

### Acceptance

- The shared login sends `admin` to `/admin`; normal users cannot enter admin APIs
  and administrators cannot enter owner APIs, data, logs, reports or private tiles.
- All specified post-installation admin workflows are available through the four
  tabs and their left navigation, without manual API/CLI steps for ordinary use.
- Owner/admin sessions and caches coexist independently; reserved-name collisions,
  expiry, sign-out and password recovery/change preserve role separation.
- Region preparation/refresh/recovery survives browser/controller/Job interruption,
  coalesces duplicate requests and preserves active data after candidate failure.
- Kubernetes privileges are isolated to the controller/approved updater roles;
  Settings cannot mutate or shadow Helm/GitOps-managed configuration.
- Announcement expiration and retraction remove user-visible messages.
- Account deletion immediately disables access and eventually purges private records.
- Argo CD blocks rollout on failed migration.
- Dashboards and alerts distinguish product incidents from source-user action such as expired iCloud trust.

### Verification focus

- Shared-login role routing, canonical reservation/identity transition and dual-session CSRF tests
- Admin privacy, cache isolation, item/log/artifact/export/report/tile denial tests
- Invitation races/delivery failure, suspension/reactivation, session revocation and self-service recovery
- Account purge completeness, active-work draining and interrupted retry
- Real controller/updater RBAC/admission, duplicate Job/restart handling and CLI contention
- OSM checkpoints, storage clearance, version fences, promotion/reconciliation and cleanup recovery
- Four-tab/left-nav light/dark UI continuity, keyboard and iPad-class containment
- Settings ownership, password revocation and upgrade/GitOps no-drift checks
- Helm install/upgrade/rollback rendering
- Backup restore followed by migration
- Alert rule tests from synthetic metrics

## Milestone 11: Tablet-Focused UI/UX

### Outcome

The product is practical to use on iPad-sized tablets and larger, in portrait
and landscape, without compromising the established desktop experience.
iPhone-sized layouts and phone-focused development are explicitly out of scope.
This milestone precedes MVP verification and release readiness, now Milestone 12.

### Vertical slice

- Choose and document iPad-class reference devices and viewports; do not add
  iPhone-sized requirements or spend development effort on phone layouts.
- Review navigation, authentication/preferences dialogs, Summary, Stats, Data Sync,
  Map, New Coverage details and Road Coverage as complete tablet user journeys,
  not just CSS reflow.
- Adapt dense tables, row expansion, filters, date controls, modal sizing and
  scroll ownership for portrait/landscape tablet use.
- Make Map controls and sheets usable by touch, preserve map space, and provide
  touch-accessible alternatives to hover-only details and selection interactions.
- Verify on-screen/external keyboard behavior, focus management, browser zoom,
  rotation and safe-area handling without losing selection or map state.
- Review light/dark themes, contrast, color-vision distinctions and touch targets
  on the supported tablet layouts while preserving desktop behavior.

### Acceptance

- Core user journeys work on the documented iPad-class devices in both orientations.
- Controls, dialogs and tables remain reachable without unintended nested scrolling
  or obscured content; the Map remains a usable primary surface.
- Touch users can access essential details and actions without a mouse hover.
- Rotation and keyboard/zoom changes preserve current selections and private scope.
- Tablet light/dark and accessibility reviews reference actual captured evidence.
- No iPhone-sized implementation or acceptance matrix is required.

### Verification focus

- Real iPad-class device and tablet browser checks
- Portrait/landscape, touch, keyboard, zoom and safe-area scenarios
- Tablet theme/contrast/color-vision review and desktop regressions
- Summary/Map/Stats/Data Sync/dialog navigation and state-preservation journeys

## Milestone 12: MVP Verification And Release Readiness

### Outcome

The feature-complete MVP has reproducible benchmark, functional, privacy and
recovery evidence and coordinated release artifacts. Production deployment,
scaling and environment-specific security hardening remain Milestone 14; public
GitHub publication follows this verification milestone as Milestone 13.

### Vertical slice

- Run the complete historical NFS import with production-like limits.
- Complete the carried-forward typical/stress profile benchmarks, resource-capped
  48/96-route backlogs, resource peaks, mixed-workload responsiveness and
  profile-specific visual review. Preserve earlier failures and unmeasured fields
  until new bound evidence is collected; retain conservative matcher concurrency
  until capacity measurements justify a change.
- Define and commit a reproducible benchmark profile covering the 2022-present 5.4 GB source archive, normalized record counts, synthetic multi-account load, pod resources, request mix, cache state, and concurrent worker activity.
- Tune indexes, SQL, rollups, matching, and tiles from measured traces.
- Validate responsiveness while ingest and OSM work run.
- Run cross-account security tests across every private resource and tile route.
- Run dependency, container, OpenAPI, and migration compatibility checks.
- Exercise failed source, worker crash, database restart, deletion failure, SMTP failure, and OSM failure runbooks.
- Record benchmark resource envelopes and tuning recommendations; final production
  resources, scale limits and retention decisions belong to Milestone 14.
- Reconcile all documentation and prepare the verified release candidate and
  coordinated semver artifact inputs for Milestone 13's public publication.

### Acceptance

- Ordinary API queries meet 500 ms p95 under the documented benchmark profile.
- Summary is usable within two seconds and initial Map within three seconds under the same profile.
- Historical ingest completes without exhausting configured temporary storage.
- No critical or high security finding remains unaddressed.
- Restore and migration procedures recover a representative backup.
- Every functional-spec acceptance criterion is mapped to an automated or documented verification.

### Verification focus

- End-to-end user and admin journeys
- Desktop native-zoom regressions and profile-specific theme/color-vision review
- Multi-year import and map benchmark
- Security boundary and log/telemetry leak audit
- Failure injection and recovery
- Release artifact and GitOps provenance

## Milestone 13: GitHub Project Publication

### Outcome

A public user can discover the GitHub project, understand and install version
**1.0.0**, obtain its images and Helm chart without private-registry access, and
operate it using published administrator and user documentation. **GitHub Actions**
provides the public CI and release pipelines. This milestone distributes the
verified product; deployment to the owner's production host remains Milestone 14.

### Vertical slice

- Prepare the public GitHub repository and its release metadata, license/notices,
  contribution/support/security guidance, issue templates and project links.
  Keep public installation examples portable and based on synthetic data.
- Bump root **VERSION to 1.0.0** during release preparation and synchronize Helm
  `version`/`appVersion`, published component metadata and the `v1.0.0` Git tag.
  Describe supported Kubernetes, Helm, PostgreSQL/PostGIS and CPU architectures.
- Add **GitHub Actions** workflows under `.github/workflows/` for pull-request
  validation and trusted-branch/release publication. Reuse the repository's Go,
  UI, OpenAPI, database integration, image and chart checks rather than relying
  on private Gitea runners or Harbor for the public installation path.
- Build and publish public **GHCR OCI images** for UI, API, shared worker and the
  OSM operator. Publish release-version, immutable commit/digest references and
  a latest-stable alias, with coordinated source/revision metadata, checksums,
  SBOMs and build provenance. Build/test supported architecture manifests and
  verify anonymous image pulls; PR workflows validate without publication rights.
- Create the initial public **GitHub Release v1.0.0**, with release notes, the
  matching chart package, artifact checksums/provenance links, supported-version
  information, installation/upgrade links and known limitations. Publish a
  consistent completed artifact set before marking it the latest stable release.
- Add README badges backed by real **GitHub Actions build and test** results,
  plus useful release, license, documentation and chart links. Keep the README
  focused on a tested quick start and product discovery.
- Build a human-facing **Docusaurus** site from Markdown/MDX and publish static
  HTML to **GitHub Pages through GitHub Actions**. Organize separate administrator
  and user guides, with search/navigation, versioned release documentation and
  generated or verified API/Helm references. Check links, site base paths and
  snippets in CI; publish curated guides rather than development receipts.
- Cover prerequisites; bundled/BYO database installation; SMTP, keys/Secrets,
  administrator bootstrap and invitations; sources/iCloud/NFS and initial OSM
  setup; Summary/Map/Stats/Data Sync; preferences, backups, upgrades, troubleshooting
  and uninstall/data-retention behavior. Clearly identify operator actions and
  external services that cannot be supplied by chart defaults.
- Add an optional **standard standalone PostgreSQL Helm dependency**, enabled by
  default and disableable for **BYO PostgreSQL**. Select and pin a maintained
  single-server chart with a compatible, publicly available PostgreSQL/PostGIS
  image; lock dependencies. Support separate application and OSM databases/roles
  on that instance and explicit external connection/Secret settings when disabled.
- Integrate database readiness, role provisioning, migrations and bootstrap for
  clean installs and upgrades. A pre-install hook must not wait for an ordinary
  dependency server that Helm has not yet created. Preserve least-privilege roles,
  required extensions/collations, persistent credentials/storage and existing BYO
  TLS/Secret behavior; test failure, retry and retained-data upgrade paths.
- Publish the **same versioned Helm package** through both an HTTPS **legacy
  chart repository** (`index.yaml` plus packages, hosted with GitHub Pages) and
  an **OCI chart repository in GHCR**. Document both `helm repo add/install` and
  `helm install oci://...` flows; preserve earlier packages and index entries.
- Default public chart repositories to GHCR images for the newest compatible
  stable release. Pin each released chart to its matching version/digests rather
  than letting an unchanged chart drift with `latest`; retain explicit image,
  registry, tag and digest overrides. Document latest-stable aliases separately.
- Smoke-test fresh installation from both **published** chart channels, using
  bundled and BYO PostgreSQL on a clean cluster with no private registry/runners
  or homelab configuration. Exercise signin/bootstrap, import, Map/Coverage/Stats,
  upgrades, failed migrations and documented recovery/uninstall behavior.

See the [public installation contract](functional-spec.md#feature-install-and-use-the-public-release)
and [publication architecture](architecture.md#github-publication-and-public-installation-planned-m13).

### Acceptance

- VERSION, chart/app versions, Git tag, GitHub Release and all component image
  references agree on 1.0.0; public artifact pulls and both chart installation
  instructions work without publisher credentials.
- GitHub Actions reports real build/test results; release jobs publish only after
  required checks pass, and docs/Helm/image artifacts identify the same source.
- Both chart channels distribute the identical package; defaults use published
  GHCR images and upgrading cannot silently change a released image digest.
- Bundled PostgreSQL and BYO modes pass clean-install and upgrade tests with
  actual extensions, collation, role separation, durable data and migration gates.
- User/admin documentation is readable on GitHub Pages, version-aligned and
  sufficient for a new operator to follow the tested installation/first-use path.
- No chart defaults or release examples depend on private Harbor, Gitea, cluster
  hostnames, development credentials or owner-specific data/configuration.

### Verification focus

- GitHub Actions PR/release permissions, version/tag consistency and rerun behavior
- Public GHCR pulls, supported image manifests, SBOM/provenance and release assets
- Helm lint/schema/render/package/index and OCI pull checks, package digest parity
- Actual clean installs/upgrades for bundled/BYO databases, hook ordering, failed
  initialization, extensions/collation, credential retention and PVC lifecycle
- Docusaurus build, versioned/base-path links, README badges and executable examples
- Published-artifact first-use journeys and installation/upgrade/recovery guidance

## Milestone 14: Production Deployment, Scaling And Security Hardening

### Outcome

The validated MVP is deployed directly to the production host cluster with measured
capacity, least-privilege credentials, enforced isolation and tested recovery.
This is the final planned MVP milestone, not an extension of Milestone 7.

### Vertical slice

- Prepare production-only application values, Secrets, database and storage;
  preserve private development/production data separation while sharing only
  public OSM data. Coordinate shared infrastructure through its owning automation.
- Provision distinct least-privilege OSM reader credentials/client certificates
  for Coverage and API diagnostics, separate from updater write access. Verify
  generation, segment, topology and locality permissions and revoke mutation access.
- Validate enforced API-only Martin networking, service-account permissions,
  application database roles/RLS and private-resource authorization in production.
  Do not carry accepted xdev namespace-sync/network-policy exceptions into production.
- Review TLS verification, certificate renewal, Secret/key rotation, ingress
  exposure and deployment/dependency security findings; never enable plaintext
  fallback or broaden private-data access to simplify rollout.
- Measure production-like multi-account traffic, import/matching overlap and
  resource/storage/database pressure. Set final requests/limits, pool budgets,
  replicas, matcher admission and supported scale from evidence rather than
  extrapolating single-user fixture results.
- Finalize retention, staging/PVC capacity, backup schedules and recovery
  objectives; test restore, migration, rollout/rollback and failure recovery.
- Verify production telemetry origin scope, actionable alerts and alert delivery,
  and publish the production operating/security/scale runbooks and release.

### Acceptance

- No shared updater credential or development-only isolation exception remains
  in the production application deployment.
- Production cross-account, private-tile and least-privilege negative tests pass;
  critical/high security findings are resolved before exposure.
- The documented production load meets agreed latency/resource limits without
  exhausting database connections, temporary storage or matcher admission capacity.
- Scaling changes preserve isolation, single-flight maintenance and bounded work.
- Certificate/key rotation, alert delivery, backup restore and rollback are verified.
- Production deployment and operating limits are reproducible and documented.

### Verification focus

- Production permissions, TLS/network boundaries and Secret rotation
- Multi-account capacity, scaling, database/resource budgets and saturation
- Operational alert delivery, retention, backup/restore and rollout recovery
- Production release and infrastructure-ownership provenance

## Post-MVP Sequence

Post-MVP work begins only after separate product and authorization design. Likely order:

1. User-facing source and admin management UI
2. OIDC/Keycloak authentication provider
3. Additional source adapters and normalization mappings
4. Sharing grants with explicit temporal and geospatial exclusions
5. Side-by-side statistics and route-overlap comparison
6. Optional awards, route planning, native clients, or richer elevation analysis

No MVP schema or API should imply that sharing rules have already been designed.
