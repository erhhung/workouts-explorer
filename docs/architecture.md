# Architecture

## Context

Workouts Explorer is a small, self-hosted, multi-account application for importing workout records and GPS tracks, displaying workout summaries, and calculating visited path coverage from public map data.

The expected deployment has up to 10 accounts and 10 concurrent users, but each account may contain several years of route points and thousands of workouts. Ingest, path matching, OSM maintenance, and tile generation must not make the interactive API unresponsive.

The system handles sensitive health and location data. Account isolation, credential protection, authenticated private map tiles, safe diagnostics, and deletion behavior are first-class architectural constraints.

This document describes implemented structure unless a feature is explicitly
marked planned, optional or deferred. Capacity targets are design requirements,
not proof of multi-user load acceptance. Dated operational measurements belong in
the focused acceptance and telemetry guides rather than this component overview.

The 030 canonical display contract introduced schema/minimum runtime **30**,
baseline **27**. [ADR 0013](decisions/013-canonical-coverage-display-identity.md)
defines the general display policy, not an OSM derivation/matcher/path-policy
change. No OSM regeneration or route rematching is needed. The compact-fact reader
descriptions below explain the foundation; the canonical section defines the final
combined read contract. See [migration 030](database-migrations.md#canonical-coverage-display-030).

The following folded-028 release evidence predates explicit ingest reprocessing.
The provider-identity/source-adoption foundation requires minimum runtime 26
(migrations 024-026). At that checkpoint the active sequence was **001-028**, with
no active 029. Ordinary static 027 advances 26 to 27 without a legacy dispatcher or
archive replay; 001-027 remain unchanged. New 028 folds deferred-generation locking,
strict eligible cohorts and mixed-failure automatic retry into the pipeline.
At that checkpoint, `SupportedSchemaVersion`, schema and minimum runtime were
**28**, marker **27**. The folded contract, exact parking repair and UI follow-up are **deployed
in `dev-20261020`, Helm revision 29**, schema/Goose/minimum runtime 28, marker 27.

The [migration design](database-migrations.md) defines read-only history validation,
native upgrades and recovery. Canonical 27 applies new 028; native Goose does not
replay an already-applied 028. Old-contract 28 therefore needs a new private,
guarded offline repair with explicit operator approval, protected actual-backup
restore rehearsal using the actual migration role and new-source/catalog proof.
Pause all old API/ingest/Coverage runtimes, and tiles if affected by DDL constraints.
Old apply-10/defer-6 ABIs are intentionally absent: old runtime-28 readiness fails,
new runtime-28 readiness requires private builders. Numeric version 28 is not a
feature/compatibility fence; rolling reuse of old images is not supported.
Preserve ledger 0-28 IDs/timestamps without DML/rewrite and all data, retry roots/
budgets, sources and credentials. Retain backups/artifacts; incompatible-image
rollback requires the full old database restore plus matching old OCI images.
The approved exact actual-profile repair passed protected actual-backup rehearsal
and production application under the real non-superuser migration login. All old
tuple fields and ledger IDs/timestamps were preserved before runtime startup;
new structural readiness passed. Generic SQL normalization is not an admission
rule. Chart semantic changes remain frozen; all release-manifest chart files and
runtime resource/admission limits are unchanged.
[Development Progress](development-progress.md#current-status) owns current evidence;
this historical contract is not itself a milestone or yearly recovery receipt.
See [Milestone 8 closeout](development-progress.md#milestone-8-closeout).

## Components

### Owned components

The `workouts-explorer` repository contains three independently deployable components:

| Component | Project folder | Image | Responsibility |
|---|---|---|---|
| UI | `ui/` | `workouts-ui` | React SPA, responsive Summary and Map views, Swagger-independent user workflows |
| API | `api/` | `workouts-api` | REST API, authentication, authorization, queries, commands, notifications, private tile proxy, static/public runtime config |
| Worker | `worker/` | `workouts-worker` | General job runner and separate Coverage runner: source checks, ingest, workout deletion, matching, reconciliation and aggregates |

API and worker are separate Go binaries and Kubernetes Deployments. They may share Go domain packages and generated OpenAPI types from the same repository, but they have independent processes, resource limits, health checks, and scaling.

General and Coverage workers are separate Deployments from the same worker image.
The additional operator-only `workouts-osm` image is built from
`worker/osm-update.Dockerfile` and runs public-data update/identity-evaluation
commands in short-lived Jobs; it is not the general job runner.

### Supporting components

| Component | Responsibility |
|---|---|
| Application PostgreSQL/PostGIS | Accounts, sessions, sources, jobs, workouts, private routes, copied segments, attribution, rollups, notifications |
| Shared `osm` PostgreSQL/PostGIS | Selective hierarchy, municipal boundaries, and derived paths from configured named extracts on the production PostgreSQL server; contains no private account data |
| OSM read replicas | Optional strict standby-only OSM reads via all-node headless DNS; off by default, independently gated from writer Jobs and capacity increases |
| Martin (`workouts-explorer-tiles`) | Generates dense private route and coverage vector tiles from the application database |
| Public base-map providers | Provide unauthenticated browser style documents, tiles, glyphs, sprites, and images |
| NFS archive | Immediately usable source archive for Health Auto Export JSON |
| Rclone/iCloud Drive | M8 native 1.75.2 job-scoped transport implemented and deployed; ingest/recovery verified; [milestone closeout](development-progress.md#milestone-8-closeout) owns acceptance status; local/NFS remains the fallback |
| SMTP server | Sends invitations and password resets using iCloud SMTP initially |
| OTel gateway and UI/tiles sidecars | Gateway receives Go metrics/traces and sidecar metrics; Contrib 0.156.0 sidecars collect native nginx/Martin and separate collector self-telemetry |
| Prometheus, Thanos Ruler, Pyrra and Alertmanager | Collect/store metrics, generate and evaluate app SLO rules, and route alerts; shared provisioning is infrastructure-owned |

### High-level flow

```text
Browser
  | HTTPS
  v
UI SPA -----> API ----------------------> Application PostgreSQL/PostGIS
               |                                      ^
               | authenticated MVT proxy              |
               v                                      |
          Martin -------------------------------------+

API --enqueue durable jobs--> Application PostgreSQL
                                  |
                                  v
                               Worker
                              /   |   \
                     NFS/local/iCloud | shared `osm` PostgreSQL/PostGIS
                                    |
                                    +--> copied matched segments and rollups

API/General worker/Coverage worker --TLS OTLP/gRPC--> OTel gateway
UI nginx status/Martin native metrics --> local sidecars --TLS OTLP/gRPC--> OTel gateway
Sidecar self-metrics --> separate local self pipelines  --TLS OTLP/gRPC--> OTel gateway
OTel gateway --> Prometheus/Thanos --> Grafana/Alertmanager
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
- Uses MapLibre for selectable, theme-aware public base-map styles and authenticated private vector layers.
- Reinstalls product-owned private sources and layers after a public style change without resetting current map state.
- Never receives source credentials, database topology, or the internal tile-server URL.
- Exposes nginx `stub_status` only on `127.0.0.1:8083/nginx_status` for its local native receiver; no public status/metrics route or API proxy is added.

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
- Authenticates private tile requests and proxies them to internal Martin.
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
- The general runner executes source checks, source-child ingest and workout deletion. The dedicated Coverage runner executes route matching and reconciliation. OSM import/refresh uses the operator Job workflow described below, not general-runner dispatch.
- Emits safe structured logs, job events, OTel metrics, and traces.

### Martin (`workouts-explorer-tiles`)

- Connects only to the application database using a least-privilege database role.
- Auto-publishes only approved tile functions from the `app` schema and publishes no tables.
- Is reachable only inside the cluster.
- Does not implement end-user authentication itself.
- Receives API-authorized account and selection parameters as one validated JSON function argument.
- Disables its internal tile cache because private selections expire and API responses are `private, no-store`.
- Martin 1.15.0's standard image enables native HTTP metrics at `/_/metrics` without a metrics switch; its local sidecar scrapes port 3000 without opening another network-policy path. `/metrics` is not this endpoint.

## Data Model

API-created application entity IDs use UUIDv7 stored as native PostgreSQL `uuid`;
database-generated internal IDs may use random UUIDs. Copied public segment/path
identities retain the deterministic OSM derivation identity rather than receiving
fresh UUIDv7 IDs. API serialization uses compact uppercase UUIDs.

### Identity and account

| Entity | Purpose |
|---|---|
| `authentication_principals` | Role, immutable username/email identity, password hash, mutable full name and disabled state |
| `users` | Data-owner principal subtype and unique personal-account association |
| `administrators` | Administrative principal subtype with no workout ownership |
| `accounts` | Tenant boundary; one personal account per data user in the MVP |
| `sessions` | Opaque revocable cookie or bearer sessions, absolute expiration, hashed credential verifier and cookie-session CSRF token |
| `invitations` | Expiring single-use signup tokens stored as hashed verifiers |
| `password_resets` | Expiring single-use reset tokens stored as hashed verifiers |
| `preferences` | Account-owned theme, units, timezone, week start, clock, table, date and diagnostics preferences |

Administrator and data-owner logins remain different identities even when controlled by one person.
Separate membership/sharing and external authentication-identity tables are
future designs, not current schema objects.

### Sources and ingest

| Entity | Purpose |
|---|---|
| `sources` | Current account-owned source, display name, immutable type, auto-sync flag, health status, encrypted current config |
| `sources.deleted_at` | Tombstoned source identity retained for provenance; configuration is cleared and CRUD excludes deleted rows |
| `source_files` | Safe discovery identity, source-relative name, size, modification metadata, checksum, processing state and private immutable success receipts (029) |
| `source_objects` | Source-local successful object inventory, retained checksum and optional ownership-blocked epoch |
| `accounts.workout_ownership_epoch` | Account-wide invalidation epoch advanced only by a source's live-to-deleted transition |
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
| `workout_import_events` | Chronological created, updated, matched_unchanged, adopted and skipped_live_owner provenance |
| `workout_deletion_targets` | Immutable deletion targets and identity tombstones preventing reimport resurrection |
| `adopted_workout_coverage_refreshes` | Durable source-switch Coverage obligation, recorded with adoption and consumed after commit |

#### Provider identity and source adoption (requires 26)

Provider-backed identity is globally unique within an account by `(account_id,
provider_namespace, provider_id)`. Both `health-auto-export-local` and
`health-auto-export-icloud` map to `health-auto-export`; transports are not
different providers. Provider IDs are opaque, not inferred from dates or routes.
`app.resolve_ingest_workout_identity` resolves identity under the versioned
`app.fence_ingest_job` transaction/worker/lease capability before writes:

- Same mapped source: unchanged normalized content records `matched_unchanged`
  without replacing raw data; changed content updates the existing UUID and
  affected derived data.
- Another live source: record `skipped_live_owner` without duplicating, stealing
  or updating the workout. Live means `deleted_at IS NULL`, even if disabled,
  disconnected, checking or connection-failed.
- Exactly one match owned by a deleted source: adopt the same UUID, set its current
  source/file to the incoming source, and treat the incoming payload as
  authoritative. Changed content replaces header, aggregates and route points;
  identical content need not rewrite raw data. Append `adopted` while retaining
  earlier source/file/job provenance unchanged.
- Conflicting migration identities or ambiguous resolution fail closed, without
  choosing a winner, silently merging UUIDs or exposing provider IDs in errors.
- ID-less fallback fingerprints remain conservative and source-local, with no
  automatic cross-source adoption or fuzzy date/GPS matching.

A deleted source is a retained soft tombstone, not a NULL `workouts.source_id`.
Source type remains immutable through the API; switching transport uses another
source and eligible adoption, not source conversion or a workout-history wipe.
Workout deletion first hides the row, then removes current data and detailed
events after derived cleanup. Pending and completed deletion identity tombstones
suppress resurrection, including provider-backed imports through replacement
sources; fallback suppression remains source-local. Ingest and deletion are
serialized so deletion-pending records cannot be adopted or updated.

Every successful adoption records `app.record_adopted_workout_coverage_refresh`
in the same file transaction as ownership, authoritative content, import event,
successful checksum and inventory. This durable outbox is consumed only after
commit by ingest repair or leased Coverage reconciliation, outside ingest's
source/workout lock order. Routed workouts request new Coverage even when their
content and route digest are unchanged; hashes are never fabricated to force
work. Event-fenced consumption cannot consume a newer adoption, and missing map
data retains a deferred obligation rather than falsely reporting a match.
No-route/deleted workouts need no match. See the
[job contract](job-state-machine.md#ingest-ownership-and-file-recovery-requires-26)
and [source-switch example](icloud-source-setup.md#worked-source-switch-example).

### Time

- Store workout instants as `timestamptz`.
- Preserve original start and end UTC offsets.
- Store the workout's local start date for calendar filtering.
- Store a nullable inferred IANA timezone name and its derivation source at workout level, not at each route point.
- Derive timezone from a route point and offline timezone boundaries where possible.
- Otherwise use the chronologically nearest GPS-derived workout in either direction with the same UTC offset.
- When no IANA name can be inferred, leave the IANA field null and derive a display label such as `UTC-08:00` from the preserved offset.
- Recompute affected inferred timezones after ingest or deletion.

The route's first provider-sequenced coordinate is the definitive lookup point.
`route_boundary` derivations record the promoted boundary release; only those
direct results can seed `nearest_route_boundary` derivations. Nearest lookup is
account-scoped, requires the same recorded start offset, and uses timestamp then
workout ID as deterministic tie-breakers. Inferred rows retain the source workout
ID so deletion and changed-route reconciliation can replace stale derivations.
An IANA result is discarded when its historical offset at workout start conflicts
with the provider's preserved offset.

### OSM and coverage

The OSM database retains selective public-map provenance and derived matching
paths. ADR 0008's toolchain is implemented: Osmium 1.19.0 filtering/reference
checks, osm2pgsql 2.3.1 flex output using `osm/import.lua`, and checked-in batched
PostGIS derivation, validation and promotion SQL. The Go updater in
`internal/osm/` coordinates that pipeline; importer version is 3 and derivation
version is 20. Node/way/relation lineage, tags, current versions and timestamps
are retained as required by the selective contract, not a full OSM history copy.

The public-data model separates deterministic matching segments from logical paths. Segments are split at intersections, relevant topology changes, and locality boundaries and retain exact OSM provenance. Named logical paths group connected segments by case/whitespace-normalized original name, with bounded proximity joining for roads; connected named groups may cross road/path classes. Components are computed across locality boundaries before final identities are scoped by authoritative municipality/county, or provider region when no locality exists. Locality identity includes stable OSM administrative provenance rather than postal-city or display text alone, so equally named localities cannot collide. Unnamed paths use scoped source/topology lineage and conservative branch rules instead of collapsing every unnamed segment into one group.

The application database intentionally copies only matched segment and logical-path data required for private queries and rendering:

| Entity | Purpose |
|---|---|
| `coverage_paths` | Stable logical-path identity, normalized/display name, broad class, and authoritative locality identity/display name |
| `path_segments` | Logical-path membership, OSM identity/version, 2D geometry, path classification, and display tags |
| `workout_segment_matches` | One row per workout/physical segment with dissolved traversed `MultiLineString`, unique covered length, and earliest traversal timestamp |
| `workout_path_attributions` | Unique workout/logical-path attribution and earliest positive-length member-segment traversal |
| `account_path_daily_rollups` | Distinct-workout count keyed by account, logical path, and workout-local start date |
| `account_path_all_time` | All-time first/latest visit dates and distinct-workout count by logical path |
| `account_data_generations` | Cache-busting generation for private tile URLs after mutations |
| `workout_path_display_partials` | Compact workout/display-entity/source-path qualification partials and numeric bounds |
| `workout_path_display_context` | Independent workout/display-entity state/national-park context |
| `workout_park_display_bounds` | Independent workout/park matched-evidence bounds |
| `coverage_display_dirty` | Transaction-local account, workout and segment-source keys for deferred display maintenance |

The unique workout/physical-segment constraint collapses repeated passes and GPS-jitter spans into one segment match. Persistence dissolves overlapping or contiguous spans regardless of direction, computes length from the dissolved geometry, and stores disjoint visited spans as separate components of one `MultiLineString`; it never fills gaps between them. The unique workout/logical-path constraint ensures matching multiple member segments and outbound/inbound traversal count once. Segment evidence therefore retains only clipped, positive-length geometry actually traversed by selected workouts. Every emitted span receives the containing logical path's count and fixed bucket; unvisited spans and members are not emitted.

Public graph derivation batches source ways to bound PostgreSQL temporary and shared-memory use. Physical segments split at shared OSM nodes and locality boundaries. Locality assignment requires geometry coverage within 1 cm; ambiguous complex pieces retain public geometry without a false locality attribution. Derivation v20 preserves directional labels in named identity groups: East, West and plain road names remain separate even when search normalization strips a leading cardinal direction. A road component of at most 25 m may join exactly one neighboring component with the same base name and locality; ambiguous fragments remain separate. See [ADR 0008](decisions/008-osm-import-and-segment-identity.md) for the versioned identity rules.

Daily rollups support named periods and arbitrary explicit ranges without maintaining separate mutable materializations for each shortcut.

Selections containing all workouts in a date range use daily rollups. Arbitrary workout subsets query the underlying unique workout/path attribution joined to the selected workout IDs, because an account/day rollup cannot preserve subset membership. Tile functions choose the rollup or attribution path from the authorized map selection and join selected segment-match evidence to avoid rendering unvisited portions of a logical path. First/latest visit fields always come from all-time date-only extrema.

#### Coverage Display Precomputations (Requires 30)

This section and Targeted Coverage Hover Statistics describe the compact-fact
foundation in **030**; [canonical display identity](#canonical-display-identity-requires-30)
defines the final aggregation and cache scope in the same combined migration.

Road Coverage list reads use normalized, compact evidence projections rather than
rescanning segment geometry and tags for every request. These supplement the tile
rollups above; they are not cached final responses or separate year/month datasets.
One normalized dataset serves named periods, all-time history and every custom date range.

Path partials are keyed by account, workout, display entity, source logical path
and `qualifies_without_name`. Education-ground identities can combine several
source paths without losing the live source-path name qualification. Driveways,
parking aisles and parking areas are excluded from path partials. Regional-park
context and park bounds are computed independently from matched evidence, so a
segment excluded from path statistics can still supply context or park bounds.

`app.map_selection_coverage_entities` retains selection/session/account/generation
authorization and joins workout visibility, routed/applied-result eligibility,
local start date, path names/classes/localities, park attributions and park/region
metadata live. A logical deletion therefore disappears from fresh reads without
waiting for physical evidence cleanup. Failed, cancelled or timed-out rematches
do not discard the old applied result. Metadata or date edits are not frozen in
the projections. Qualified source-path/bucket rows are deduplicated to one
display-entity/workout visit **before** range and all-time counts and extrema;
latest-date navigation still chooses the earliest start/ID on a tied date.

Evidence INSERT/UPDATE/DELETE and relevant copied-segment UPDATE/DELETE triggers
only enqueue deduplicated dirty keys. An initially deferred constraint trigger
flushes once per affected account at normal commit: it serializes through an
actual no-value-change update of the account generation row, then expands segment
source keys into affected workouts in a separate command **after** that barrier.
At READ COMMITTED this sees matches committed while waiting; Repeatable Read
conflicts must propagate rather than publish a stale projection. Both old and new
source keys are retained for updates. One set-based evidence scan rebuilds all
three projections for the dirty workouts and drains the queue. Evidence,
projections and existing generation changes commit or roll back atomically;
maintenance errors cannot leave a partially replaced applied result. The barrier
does not itself increment the public generation or change invalidation policy.
These tables/helpers are security-owner-only with forced RLS and no direct
API/worker/tile grants. See [migration 030](database-migrations.md#coverage-display-precomputations-030).

The dialog makes one `bulk=true` request to
`GET /api/map-selections/{mapSelectionId}/coverage/paths` per uncached load. The API
evaluates the coverage function once, reads at most 50,001 rows to detect overflow,
and buffers a complete response bounded to **50,000 entities and 64 MiB of
uncompressed JSON including metadata** before sending success. Either limit returns
422 Problem Details with field `bulk`, code `limit`, and no partial results. Bulk
metadata is page 1, page size 50,000, total equal to item count, and one total page
(zero when empty). Valid page/pageSize values do not slice bulk results; server
search and sort still apply. Default API pagination remains capped at 100 rows.

Client-side displayed-text search, unnamed visibility, sorting/tie-breaks and
preference-sized paging are unchanged. The shell's five-minute complete-result
cache remains keyed by resolved date range and account data generation. The client
validates complete-result metadata and never caches a failed, aborted, incomplete
or oversized load; 422 asks the user to choose a smaller date range. There is no
fallback page loop that could cache a partial result.

#### Targeted Coverage Hover Statistics

`GET /api/map-selections/{mapSelectionId}/coverage/{entityKind}/{entityId}/stats?generation=...`
returns one `RoadCoverageEntity`, including identity, counts, visit dates,
navigation workout IDs and raw bounds, but **no geometry or expanded fit bounds**.
The API uses a read-only transaction and
`app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)`;
it does not call the full list or geometry-detail reader. The server adds no dwell
delay. Click-to-fit/blink highlights continue using the existing detail endpoint
and its geometry/fit bounds.

The STABLE security-definer function validates active account, session, selection
expiry and current generation before reading facts, within the same statement
snapshot. Each compact fact source is restricted to the requested kind and ID
before aggregation. A union of target partials, context, attribution and bounds
first identifies candidate workouts; only those candidates receive live eligibility
checks, including independent context-only and bounds-only contributors.
Account/entity/workout indexes cover path partials and context;
account/park/workout indexes cover park bounds and the existing park attribution
lookup. Range statistics still cover **all eligible workouts in the inclusive
selection date range**, independent of checked/focused workouts; all-time statistics
cover the account's eligible history. Live eligibility, source-name qualification,
education visit deduplication, context, date extrema and earliest-start/ID
navigation ties match the existing readers.

Park availability also retains the detail reader's range-only **canonical-text
geometry** gate: at least one eligible in-range canonical park match must exist.
Numeric bounds still include UUID-cast noncanonical evidence when mixed with that
canonical evidence. A list row alone does not prove detail/stats availability.
Unavailable entities or foreign, expired or stale selections return 404.

Only the API runtime role can execute the new reader; underlying facts and
maintenance helpers remain private to the security owner. The stats reader fixes
`search_path=pg_catalog, app`; both compact list/stats readers set `jit=off`.
Readiness checks exact function-body fingerprints and catalog structure, including
signatures/results/defaults, security/ownership/ACLs, settings, projection tables,
RLS, indexes and maintenance triggers. Numeric schema 30 alone is not readiness
proof. These additions and final canonical readers are combined in
[030 source SQL](../api/migrations/sql/030_coverage_display.sql).
Future feature changes require a forward migration, not another implicit squash.

The UI-only hover timing follow-up retains the **750ms visibility dwell**, including
cache hits, but starts an uncached stats lookup after **250ms of stable hover**.
Lookup and the remaining dwell run concurrently. Its separate in-memory hover
cache holds at most **128 LRU entries for five minutes from response load**, keyed
by selection ID, generation, display policy/revision, resolved start/end dates,
entity kind and ID. It is not the shell's complete-table cache. Successful results
are cached immediately, even if the pointer leaves before the popup appears;
returning to an entity reuses its cached result or existing in-flight lookup.
Entity changes, canvas mouseleave, window blur and a hidden document cancel popup
visibility, not useful in-flight lookups. They suppress retries and selection
recovery for inactive hovers. Selection/generation/policy/revision/range changes
abort obsolete lookups, remove popups and clear cached stats. Late successes/errors
cannot affect a replacement context. Unmount aborts all lookups, clears the cache
and unregisters all listeners. Failed/aborted lookups are not cached. The existing
client retry for a server 503 remains **one retry after 250ms**, with no new server
delay or retry budget. This timing follow-up is deployed UI-only; no database
migration or backend rollout was required.

#### Canonical Display Identity (Requires 30)

Policy `coverage-display-identity-v1` groups by **region + normalized actual display
name + normalized actual display place**. Hidden jurisdiction IDs, source kind,
broad class, connectivity components and source UUIDs are not named-group keys.
Names/localities use NFC/casefold/NFC pinned to PostgreSQL 18 UTF8 built-in
`pg_c_utf8`, then explicit Unicode White_Space collapse/trim. Directions, accents,
punctuation and suffixes remain significant; blank/unknown names retain source
kind/UUID discrimination instead of merging fallback labels. The versioned,
length-prefixed UTF-8 namespace tuple hashes with SHA-256 to UUIDv8 with explicit
variant bits. Stored normalized key fields detect inconsistent UUID collisions
and abort preparation. [ADR 0013](decisions/013-canonical-coverage-display-identity.md)
specifies the exact tuple and whitespace code points.

Membership is date-context-resolved, not a permanent source mapping. Resolve all
historically eligible sources, retaining compact source-name/base-locality evidence,
in-range park context and region precedence. Use historical context/region fallback
for history-only sources; if their resolved key agrees, include their full visit
history. Deduplicate the union of workouts across every member before range and
all-time counts/extrema. List/stats expose only groups with an in-range visit;
latest-date ties still select earliest start then UUID. Checked/focused subsets
change geometry, not metadata or membership. Mixed named park/path members form
one group, with deterministic park-preferred representative kind/class for
presentation only. Geometry unions actual matched pieces, preserving gaps.

Six private, security-owner/forced-RLS tables store `coverage_display_revisions`,
`coverage_display_contexts`, `coverage_display_members`, `coverage_display_visits`,
`coverage_display_entities` and `coverage_display_invalidations`. Contexts are
unique by account, generation, revision, inclusive dates, policy and label locale
(`en`). The API's existing Repeatable Read selection transaction invokes VOLATILE
SECURITY DEFINER `app.prepare_coverage_display_context`, builds/reuses the complete
ready context and binds it with a composite-account FK. Preparation returns
context ID, display revision and policy; JSON requires `coverageDisplayPolicy`
and `coverageDisplayRevision`. Read-only hover/detail and tiles never rebuild.

Preparation cleans obsolete/older-than-one-day unreferenced contexts and retains
eight reusable unreferenced scopes per account, a soft limit excluding valid
actively pinned selections. Account cascades and context-column-only FK cleanup
preserve account identity; failed builds, savepoints and maintenance roll back
atomically without exposing half-built contexts.

All source inputs invalidate contexts: matches/segment projections, path/park
metadata, attribution, workout dates/start/deletion, route presence/applied
eligibility and region labels, including context-only evidence. Deferred
account/transaction coalescing advances both display revision and public generation
once, without rebuilding every range on write or taking early ingest/provider
generation locks. Preserve generation-before-revision order, compact-fact post-barrier
segment expansion, correct evidence under both READ COMMITTED commit orders and
Repeatable Read `40001` propagation. Deleted-account tombstones drain safely.

List reads indexed ready entities with search after grouping; point stats is an
authorized canonical index lookup without geometry or full-account aggregation.
The replaced stored detail reader gates whole-group in-range geometry on metadata;
the API calls that same six-argument reader with transaction-local `jit=off`.
The 030 contract's four MVT layers and focus GeoJSON use this context's canonical IDs, metadata,
counts and buckets while retaining source IDs/kinds for hit priority and actual
geometry. A source path feature may have canonical `entityKind=park`; representative
kind never filters membership. No tile-local text hashing or invented bridges.

Bulk, hover and detail caches and stale-response guards add policy/revision to
their existing scope; local search/sort/filter/paging and click improvements remain.
Runtime readiness pins exact 030 bodies and ownership/ACL/RLS/index/trigger
contracts. This is a generally applicable application read-model policy, not an
OSM derivation, matcher or path-policy change. Coverage recomputation here means
context warm/rebuild from retained evidence, **not new route jobs**. This contract
introduced schema/minimum runtime **30**; 031 retains the policy but replaces the
tile path as described [below](#tile-local-rendering-031). Actual deployment status and
verification boundaries are in [current status](development-progress.md#current-status).

#### Click-Highlight Follow-Up

The following behavior is already deployed and retained by source migration 030.
The implementation is in [MapPage](../ui/src/MapPage.tsx) and
[the map API](../api/maps.go). Earlier source and browser receipts retain their
checkpoint scope; [Milestone 8 closeout](development-progress.md#milestone-8-closeout)
owns the current browser and acceptance outcome.

An entity-name click closes Road Coverage and returns focus to the mounted map
canvas immediately on intent, without waiting for detail. An uncached request
shows transient loading feedback with **Cancel** in a bottom map banner, visible
on desktop and mobile at **z-index 7**, below passive tooltips at **8**. Failure or
the **30-second** request timeout leaves map-level **Retry / Close** feedback,
not a reopened error dialog. Detail retries are manual, with no hidden retry.
Exact geometry and its expanded fit bounds must be available before fitting;
table/raw bounds do not cause a speculative camera move. The existing
**350ms** fit animation and white blink are retained: blinking starts immediately
when geometry is available, and the **three-second** duration starts only after
camera movement and requested tiles are ready, with the existing bounded fallback.

The existing
`GET /api/map-selections/{mapSelectionId}/coverage/{entityKind}/{entityId}?generation=...`
endpoint and response shape remain unchanged. The deployed API calls
`app.map_selection_coverage_entity_detail` with the existing six arguments
(account, session, selection, generation, kind and canonical ID). Migration 030
replaces that stored reader with metadata-gated whole-group geometry in one
statement snapshot, retaining the correlated `LATERAL`/`OFFSET 0` barrier and
minimum-500-meter fit pipeline. The earlier API-local source-only geometry copy
is superseded. Denied metadata cannot initiate geometry work. The transaction
remains read-only with explicit `SET LOCAL jit=off`, not a global setting change.
This 030 reader contract remains unchanged in source 031; the source-30 live
cutover completed at revision 61, separately from the approved 031 rollout at revision 65.

A separate canvas-visit detail cache stores only successful, still-current
responses, keyed by selection ID, generation, resolved dates, display policy and
revision, entity kind and canonical ID.
It evicts least-recently-used entries to retain at most **8 entries / 8 MiB of
UTF-8 serialized JSON**, with a fixed **five-minute TTL from load**; hits do not
extend expiry. Oversized details can be displayed without caching. This bounds
serialized payload accounting, not JavaScript heap or process memory. There is no
bulk geometry prefetch. The separate hover-stats cache and shell complete-result
bulk cache, including client-side paging, remain unchanged.

Latest-request sequence and selection/generation/range, deletion, workout subset,
mode/diagnostics and selection-refresh context guards prevent superseded work from
changing UI, camera, caches or recovery state. Context changes clear detail state
and cache; cancellation, abort and unmount stop pending work without surfacing
stale errors or caching late successes. Reusing the same geometry/source skips
redundant GeoJSON `setData`; style/source replacement forces installation again.
Reopening Road Coverage cancels pending detail work rather than allowing it to
complete behind the dialog. Late 200, 503 or 404 responses cannot fit, cache,
invalidate the bulk-table cache or automatically refresh the capability after
reopen. Already-completed detail cache entries and highlights are preserved.
A current 404 invalidates detail and complete-table caches and shows visible
Retry feedback. Only that manual Retry refreshes the capability; the user then
reopens Road Coverage and chooses an entity again, without automatic replay of
the stale click.

#### Map Overlay Layout

Map cards and the navigation-control container share `--map-overlay-inset`:
**0.75rem on mobile layouts, 1rem on desktop**. MapLibre's inner control margin is
zero, avoiding a doubled inset. The Road coverage legend width reserves the zoom
button column and its gutter. Existing viewport-bound map and independently
scrolling sidebar behavior remain unchanged.

Route and Coverage tooltips remain canvas-level popups above the legend,
navigation controls and attribution. The stage/canvas host must not create a
stacking context that traps them below sibling overlays. Popup root and content
use `pointer-events: none`, so these passive tooltips do not interrupt hover or
intercept map/control interaction. The
[map CSS guard](../ui/scripts/check-map-css.mjs) protects these layout rules.

#### Deferred Generation (Introduced By Deployed Folded 028)

The account generation is an invalidation counter, not a long-file mutex. Folded
028 introduced the following timing contract, deployed at the 10/20/2026 checkpoint.
030 adds the display invalidation/coalescing rules described above. In 028, the
four existing header, route-summary, type-update and Coverage-current
generation triggers retain their names, events, UPDATE OF columns and WHEN scope,
but become `DEFERRABLE INITIALLY DEFERRED` constraint triggers. Account seeding
remains immediate. Each qualifying row event increments the counter; GPS points
are not individual generation events. Coverage invalidation remains migration
017's changed-current-finish rule, not all Coverage-state changes.

Normal production transactions defer these increments to commit. PostgreSQL can
flush them early with `SET CONSTRAINTS ... IMMEDIATE`, but production does not.
Data and generation become visible atomically, and a deferred-trigger failure
rolls back file checkpoints or Coverage evidence/state/progress with the data.
Commit tails still serialize on the counter; this removes long-file contention,
not every possible wait, and does not certify capacity.

The ingest fence retains a nonlocking generation-row existence check and missing
row `55000`, removing only its explicit generation `FOR UPDATE NOWAIT`. Identity
advisory locks, source/job and account/workout KEY SHARE locks, lease, ownership
epoch and runtime-capability checks are unchanged. No worker transaction retry,
deadline increase or matching-policy change compensates for contention.

### Independent Raw And Coverage Bounds

Raw route extents are computed during ingest and stored on `workout_routes`.
The four nullable `coverage_*` extent columns on `workout_coverage_states` are a
separate derived projection of displayable applied path/park evidence. Migration
030 maintains them in the existing account-serialized deferred display rebuild,
using compact evidence boxes rather than scanning GPS or clipping tiles. Relevant
path-name changes enter the same dirty-workout protocol. Empty evidence clears
the Coverage box; retained stale evidence retains its box.

Map selection responses expose raw `bounds` and nullable `coverageBounds` both
per workout and for the checked selection. The browser uses the current mode's
box for explicit/restored focus and Fit routes, and refits a persistent focus on
Routes/production-Coverage transitions. It never substitutes the raw box for a
missing Coverage box or waits for geometry fetching to determine camera bounds.
Canonical range-context filtering can omit some precomputed evidence pieces;
the stored Coverage box conservatively contains the displayed focused geometry.

The same deferred projection persists `applied_result_status` as `matched` or
`no_matches` (null before any applied result). Applied-proof changes enqueue a
workout even if the evaluation writes no matches, covering the first empty result
in every producer path. The guarded projection update changes classification and
bounds together and does not recursively requeue itself. Empty results remain
successfully completed/current for job and reconciliation purposes, while the
map API exposes the explicit `no_matches` result status. Production Coverage
disables their row focus/hover action, uses the neutral X icon and the **No map
segments matched** tooltip. Raw-mode access and diagnostic review remain separate.

### Notifications and announcements

| Entity | Purpose |
|---|---|
| `notifications` | Per-user severity, server-owned display state, typed subject reference, message, expiration |
| Announcements (planned) | Future admin-created all-user title, message, severity, expiration and retraction audit; no current `app.announcements` table |

Typed notification subjects support owner-visible job outcomes. The complete
signin-reminder and announcement policy remains planned rather than a claim
that every notification lifecycle is implemented.

## Jobs And Concurrency

### Queue

PostgreSQL is the durable queue. Workers claim rows using locking semantics equivalent to `FOR UPDATE SKIP LOCKED`, record leases and heartbeats, and recover abandoned claims.

Priority order:

1. Source connection checks and deletion
2. Manual ingest
3. Scheduled ingest
4. Coverage updates (dedicated runner; operator OSM Jobs are outside this queue)

Source checks and deletions bypass ingest-file semaphores. Duplicate active checks
and deletions are coalesced. The separate OSM updater uses a database-wide advisory
lock and durable region/generation checkpoints; its operator Jobs are not rows
executed by the general application job runner. Queue priority for future automatic
OSM orchestration is design intent.

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
The source-job runner downloads/processes files serially with cleanup before and
after the file pipeline; slot ceilings do not imply parallel downloads within a job.

### Coverage processing concurrency

Coverage is asynchronous derived work: ingestion never waits for matching to
finish. In schema 28, the file transaction records durable normal/adopted handoff
origins; readiness and enqueue happen after commit and file cleanup. This bounded
handoff can take time on the source worker but does not hold the file transaction
or slot. Workouts and raw routes remain available while Coverage is pending.

A `coverage_update` is a bounded, unclaimed account-owned parent, which may mix
regions under new ingest cohort construction, with one
`coverage_update_route` child per eligible stale workout revision. A dedicated
coverage-worker deployment claims only route children. The general worker retains
independent capacity for source checks, deletion, and ingest; low queue priority
alone is insufficient because a running job cannot be preempted. Every child is an
independent route checkpoint, and claim ordering is fair across active
parents so one large account cannot monopolize capacity.

One route match uses one bounded, read-only OSM snapshot. No application write
transaction remains open during candidate lookup or decoding. A short fenced
application transaction then rechecks the route revision, matcher version, and OSM
generation vector before replacing that route's copied segments, segment matches,
logical-path attributions, and affected rollups. Stale work cannot commit. Prior
valid coverage remains visible until its replacement commits, and a newer desired
OSM generation or route-work revision produces a successor after the current
target terminates. A failed route child does not stop its siblings.

#### Committed-Route Handoff (Requires 28)

The following contract was introduced by **deployed folded 028** at the
10/20/2026 checkpoint. The preceding `dev-20261018`, Helm revision 28 receipt
describes the old same-numeric-28 contract, not this behavior. See
[current status](development-progress.md#current-status) for later releases.
`worker.coverageBatchSize` /
`WORKER_COVERAGE_BATCH_SIZE` defaults to 25 and accepts 1-1000. After transport and
file-slot cleanup, the source worker prepares normal and adopted obligations.
During active ingest, **B newly eligible matching targets** are required globally
across regions within the ingest-root/source-sibling scope. Missing-map,
already-current/live, unchanged authoritative failed/cancelled targets and
deduplicated obligations do not count. The private durable builder saves B and
root/sibling scope across files/crashes, with bounded metadata and no placeholder
Coverage parent. It atomically seals the full B children and parent. With 63
eligible routes at B=25, active ingest may seal 25/25; the remaining 13 wait for
terminal-root account recovery, not source EOF.

Mixed regions are explicitly allowed. Optional `coverage.regionIds` is the actual
child-region union, including retries. Existing `regionId`/`targetGeneration`
are representative anchors, not complete matching scope. Each route retains its
complete generation vector and existing claim/admission checks; no region grouping
may shrink an active-ingest normal cohort.

`ingest_coverage_handoffs` retains import origins and acknowledgement/job links;
`adopted_coverage_handoff_receipts` retains source-job provenance independently
of the ephemeral adoption force queue. Metadata-only batch reads retain no route
buffers. Each protected STABLE route snapshot supplies content, ownership epoch,
the full canonical digest and compact coordinates for just one route at a time.
APP transactions close before OSM resolution. ROI readiness resolves one route at
a time with one efficient route query, not per-point queries. The full catalog
fingerprint covers **all catalog regions, configuration, boundaries and active
heads** in the same Repeatable Read snapshot as ROI lookup and again before seal.
The built source-029 metadata refinement permits generation-only rebinding when
exact input, selection fingerprint and policy still prove the same selected IDs;
a selection change requires fresh ROI. Substituting heads without that proof is
never valid. See [metadata-driven reconciliation](#metadata-driven-reconciliation-requires-29).
Separate APP/OSM transactions do not establish cross-database latest-state atomicity.
A short atomic prepare/seal/defer transaction
revalidates lease/runtime, account/source/event, ownership epoch, content and full
route digest. Account-first, ordered parent/child NOWAIT locks precede workout/state
locks; contention retries reread snapshots rather than applying stale readiness.

Hashing/readiness covers the raw parser domain **0-250,000 points**, including a
real empty-input digest, never a truncated prefix. Ordinary future-generation
reconciliation uses the same full-input compact snapshot. Matching keeps the
**50,000-original-point** ceiling; overflow raises a typed permanent Coverage
failure, not an ingest failure. No-GPS adoption retires its obligation without
creating a matching job. Only metadata/CAS/readiness survives across route reads;
there is no B-times-route-coordinate workset or new resource allowance.

SQL retains saved builder B as `parent_batch_size` when sealing and counts **all**
children, including terminal/superseded rows. Later configuration affects new
builders, not saved cohorts; no underfull Coverage parent is filled incrementally.
Retry parents inherit capacity but contain only eligible unsuccessful predecessors,
never fresh top-up work. For example, three failures of a 25-route root produce a
three-child retry while a fresh 25-route batch uses a normal root. Normal imports
preserve identical-current/live/failed/cancelled target guards and consumed retry
budgets; only a distinct proven adoption event authorizes force refresh.

Leased account reconciliation recovers committed obligations from succeeded,
failed or cancelled roots; crashed claims first follow normal lease recovery.
A final partial cohort seals **only after the visible ingest root and all source
siblings are terminal and due preparation is exhausted**, never active EOF.
Active underfull cohorts remain protected. Failed/cancelled roots can legitimately
owe derived work for committed data without reopening ingest. Normal metadata
updates/deduplication do not resurrect cancelled matching targets; cancellation
footprints remain authoritative. Cancelling ingestion does not retract
committed derived-work obligations or cancel separate Coverage jobs. Verified typed
transient readiness failures persist a one-hour deferral without acknowledgement;
permanent/unknown/cancelled leaves surface safely. Classification checks every
joined/wrapped branch with a 256-node bound, not a convenient incidental deadline.
Normal missing-map handoffs acknowledge readiness without a child; adopted missing
maps retain the force obligation. Policy remains `coverage-production-v1`, 500m /
256 MiB, two lanes, admission 2/2/2 and one per reader; no multi-user capacity
certification follows from pipeline tests.

PostgreSQL coordinates matcher admission across all replicas. Operator-configurable
limits include global, per-account, and optional per-region slots; the conservative
default is one active matcher route globally across production and diagnostics,
and one active production route per account. Synchronous API diagnostics keep
their request/response, timeout, local gate, and fast `429 Retry-After` behavior,
but must also acquire the cluster-wide matcher capacity so
additional API replicas cannot multiply OSM load. Diagnostics are not durable jobs
and do not preempt an active route; infrequent review retries at a route checkpoint.

Coverage workers use a dedicated least-privilege application credential,
small explicit data pools, and reserved application control capacity for claims,
heartbeats, cancellation checks, and terminal transitions. Pool acquisition,
statements, locks, individual queries, and route-child matching have
explicit timeouts. API, Martin, general-worker, and coverage-worker pods have
separate CPU/memory requests and limits.

Dedicated least-privilege OSM reader credentials for Coverage and API diagnostics
remain a pre-production requirement in Milestone 12. xdev currently permits
the explicit development exception of sharing the updater's OSM credential; a separate pool
or read-only transaction is not equivalent to a read-only database login.
The global reader flag controls strict standby-role validation only; component
selectors choose the Secret keys independently. Neither setting provisions or
proves least-privilege login permissions.
See [OSM read replicas](osm-read-replicas.md) for selection and rollout boundaries.

The application credential is the dedicated `workouts_coverage_worker` login.
It can claim, heartbeat, read through fenced functions, persist/fail/supersede a
coverage child, and run leased reconciliation. It cannot read raw workouts or
route points directly. The general `workouts_worker` retains transactional
coverage enqueue authority after ingest but cannot claim or persist coverage.

Coverage processing records queued, running, current, failed, and stale state with
target/applied route revision, matcher version, OSM generations, timestamps,
bounded progress, and a safe failure category. Parent results aggregate routes
total, processed, succeeded, failed, cancelled, and superseded. Mixed success and
failure produces `partially_succeeded`; failed targets do not advance the fully
applied account/region watermark or automatically hot-loop. Production tiles and
statistics read only durable application data and never invoke the matcher or query OSM.
Retry creates a new non-forking coverage parent from only the preceding attempt's
eligible failed or cancelled route children. Parent and corresponding route-child
lineage reuse the Data Sync retry root and ordinals, allowing Run detail to
navigate First, Second, and every later attempt through the latest without
rerunning successful or superseded routes.

Automation introduced by deployed folded 028 requires at least one authoritative
timeout/verified-transient failed child, then carries **all authoritative failed siblings** with
valid descriptors into the latest-only chained successor, including permanent,
unknown and null-code failures. Successes never rerun and cancellations are not
automatically revived. All-permanent/unknown parents stop. The default one
additional attempt (range 0-20) is durable per root across descendants/restarts;
manual retry never resets it, and normal failed-target scans cannot create fresh
roots to bypass it. This mixed policy is deployed; earlier retry receipts retain
their historical policy scope.

Coverage execution state and owner-facing display state are separate contracts.
For a stored failed/partially successful parent whose unsuccessful routes all
have proven current Coverage, the API supplies `displayStatus = superseded` and
the UI renders a green Superseded parent/history label. Children retain their
recorded outcomes. A read-only, active-account-scoped API helper reuses the
private retry-target predicate and proves current results through the present
authoritative child/context; it returns safe eligibility counts and bounded
current-parent links, not new retry edges. These links can span independent
chains and changed input/generations without fabricating retry ancestry.

The owner-history helper applies derived status before affected filters, totals
and pagination; unrelated statuses and unfiltered pages avoid full-history proof
scans. Missing/foreign/incomplete proof cannot turn a failure green. GET metadata
is advisory, and POST retains constructor locking and revalidation. The UI polls
terminal Coverage projections and history totals, including empty filtered pages,
and retains authoritative retry blocks across failed refreshes. See the
[job presentation contract](job-state-machine.md#current-retry-eligibility-and-superseded-presentation).

Implemented event metrics include first-claim queue age, matcher/admission/attempt
duration, committed terminal outcomes, control/lease results, retries and
stale-result rejection, alongside shared pool and API/tile metrics. Slot occupancy,
backlog/oldest age, reconciliation/deadline freshness and complete lifecycle/timeout
statistics remain instrumentation gaps, not existing gauges. Matcher concurrency
increases above one only after representative measurements show adequate
shared-database and active-user headroom.

#### Metadata-Driven Reconciliation (Requires 29)

**Implementation BUILT; real PostgreSQL and deployment verification PENDING.**
[ADR 0014](decisions/014-metadata-driven-coverage-reconciliation.md) specifies the
motivation, four ordinary cases, invalidation matrix and acceptance boundary.
Unchanged historical routes must not consume full-GPS/hash/ROI work merely to
discover that no job is needed. This is new source-029 behavior, not a deployment
claim or a reinterpretation of historical 022/028 repair receipts.

The default **30-second idle poll** reads the OSM singleton catalog head and APP
claim metadata. Identical observations return before locks/DML/account enumeration.
Completed campaigns no longer become due after 24 hours; daily scanning has ceased
to be a work trigger. Executor claim/preflight and health queries still run, so
near-idle CPU does not mean zero database queries. This cadence is independent of
the UI's 10-second Running-job refresh.

OSM 010 computes generated boundary SHA/area at catalog write time and maintains
transactional revision, legacy-compatible full fingerprint, selection fingerprint
and head metadata. Selection proof covers all region IDs/configuration/boundaries
and configured-active presence, including competing and unconfigured polygons,
but not replacement generation numbers. Unchanged derived heads suppress UPDATEs.
APP stores independent `app.coverage_route_selection_proofs` and successful
`app.coverage_applied_match_proofs`; immutable job contexts capture rules, sampling,
path policy, minimum traversal and production matcher contract. Preparation or
enqueue cannot stamp successful applied provenance.

Campaigns keyset-page metadata-actionable routes before any GPS read. Valid
selection proof permits exact-ID generation rebinding without GPS; a changed
selection or unknown input requires the bounded canonical snapshot/ROI path.
Pending/unavailable proof is retained honestly. Initial unknown history may need
one bounded bootstrap; old negative catalog baselines are never invented.

Point transition hooks capture exact old/new keys, including telemetry changes
with equal count/bounds, in `app.coverage_route_changes` without hashing or an
account-generation lock in the file transaction. Transaction-coalesced
`app.coverage_reconciliation_wakes` target exact accounts. Queue-only recovery
drains handoffs, terminal-root tails and dirty keys without an ordinary follow-up
scan or changing a saved campaign cursor. Retry/cancellation authority is unchanged.
Lease/input/descriptor CAS and monotonic catalog/event floors reject stale results;
head and ROI use one replica Repeatable Read snapshot with no strict-policy primary
fallback. See [metadata fences](osm-read-replicas.md#metadata-fences-osm-010).

#### Shared Matching Contract

Production Coverage and API diagnostics use one shared matcher and limit policy,
implemented by `routepipeline.OriginalPointLimit` and
`routepipeline.DefaultLimits()`. Storage validation and API response schemas must
agree with these limits; diagnostics must not retain a smaller matching ceiling.

| Bound | Limit | Scope |
|---|---:|---|
| Original GPS points supplied to matching | 50,000 | One route, before sampling |
| Sampled GPS points | **12,000** | One route, after sampling |
| Graph nodes | 2,048 | One matching window |
| Graph edges | 4,096 | One matching window |
| Evidence portions, including dissolved preparation portions | 10,000 | One route |
| Unique matched segments | 4,096 | One route |
| OSM generation entries | 256 | One route |
| Reported unavailable regions | 64 | One route |

Bounds are inclusive. An original/sampled-point overflow is an explicit
processing failure, never permission to hash or match a truncated prefix. These
are matching bounds, not raw-ingest retention limits; ingestion and raw-route
availability remain independent of derived Coverage success.

The normal observation window contains 128 points. The approved early hard-gap
correction below scopes these windows to continuous legs rather than letting a
whole-route window span disjoint clusters before decoder splitting. Only typed
graph-node/edge exhaustion invokes bounded retries at the same start with 64,
then 32 points.
The next accepted window starts at the preceding accepted end minus one, and
normal size 128 is tried again. Record starts/ordinals and observations/evidence
only for accepted windows. Supplemental turn/tangent rematches also use bounded
fallback while retaining their continuity and supporting-traversal checks.

**Implemented and deployed in revision 41:** sample
the complete route once without changing original or sampled sequences, then
partition adjacent sampled points before any window, candidate or graph work.
Sampling and partitioning share `coverage.GeographicGapReason`. Existing hard
gaps are elapsed time greater than 30 seconds,
distance greater than 200 meters, or backward time; threshold equality is not
itself a gap. Missing timestamps disable only temporal detection for that pair,
not spatial detection. Valid remote pause/resume legs remain matchable without
inventing Coverage for the travel between them. This is existing matcher-stage
ordering work, not ingest preprocessing.

Primary/adaptive windows and supplemental seam/turn/tangent scopes stay within
each leg; overlap and repair never cross a hard gap. One route-level `Result`
retains global offsets, order, split accounting and provenance. Clipping and
`Prepare` union/deduplication remain global, preserving disjoint visited spans and
unique physical-segment accounting across legs. Minimum-traversal acceptance
remains per decoded chunk: summing separate subthreshold chunks cannot make
them acceptable. Point/output bounds remain per route, not multiplied per leg.

Path policy `coverage-path-policy-experimental-v82`, sampling
`coverage-sampling-experimental-v1`, rules `coverage-experimental-v1` and
`coverage-production-v1` remain unchanged: this corrects intended split handling.
No database migration, minimum-runtime change, global cache buster or recomputation
is introduced; the ingest contract now in 029 is independent. This is not a
universal output-equivalence claim for previously successful disjoint routes:
per-leg window alignment can change decoding/evidence. Existing `acceptedCoverage`
remains authoritative unless explicitly rerun. Retry lineage, automatic allowance
and manual/automatic shared root budgets are unchanged. The narrowly authorized
postdeployment failed-only retry and completed verification are described in the
[evaluation design](coverage-matcher-evaluation.md#early-hard-gap-partitioning-design),
with owner-specific operational evidence kept private.

Every attempt reuses the same snapshot, admitted capacity and deadline; graph
node/edge and expansion-layer bounds remain unchanged. No arbitrary node pruning,
deadline renewal or implicit parallelism is permitted. Cancellation, database
errors, other processing bounds and exhausted minimum-size windows propagate as
errors, not silently successful partial matching. No APP transaction spans these
OSM matching attempts.

For the same raw input, OSM snapshot, movement mode, sampling, matching rules and
road geometry, both adapters must produce equivalent matching evidence; production
then dissolves that evidence into durable per-segment geometry. A diagnostic with
deliberate what-if parameters is not an attestation of a different production
configuration. The synchronous diagnostic HTTP deadline is an independent
operational limit: expiration is inconclusive, not a production-matching failure.
Point/window-policy changes do not authorize larger CPU, memory, concurrency or
execution-time budgets. See the
[functional acceptance contract](functional-spec.md#matching-bounds-and-diagnostic-parity)
and [matcher evaluation design](coverage-matcher-evaluation.md#shared-matching-limits-and-window-recovery).

#### Matching Performance And Termination

Parking repair preserves its established acceptance predicates and ordering.
Bounded exact state tracking rejects only certified nonterminating continuation:
full-state recurrence at the same cursor or an equal unread suffix behind a
merge-sealed prefix. Both certificates require deterministic opposite-boundary
selection and uninterrupted execution. Rejection continues to later candidates.
Ambiguous graphs retain the original unsorted choices, with an accepted-work
bound rather than an inferred cycle. Exhaustion/cancellation returns an error,
not successful partial replacement.

Exact portion interning avoids retaining repeated graph metadata in every state
key. Repair tracking is local and bounded at 4,096 accepted replacements/states
per traversal, 512 KiB active-cursor key payload, 16,384 interned portions/suffix
nodes and an 8 MiB charged cache budget. Temporary encoder/map overhead is
additional; these are not a hard process-memory measurement. Filtered routing
views share immutable metadata/adjacency and apply exclusions consistently to
endpoint fast paths, expansion, class/logical filters and connector searches.

Parking state-byte exhaustion is a typed `ParkingRepairLimitError` even when
both point counts are below their ceilings. A generic stored
`coverage-route-too-large` code alone cannot identify the exhausted bound;
generic lifecycle `failed` Job Logs do not contradict it. A read-only core probe
is not an actual job, persisted diagnostic or APP persistence proof. Dataset
inputs/counters belong only in private operator evidence; the matcher policy and
semantic limits remain unchanged.

Candidate reads may batch spatial discovery into one geography superset and
cache segment geography for exact per-observation tests. Materialization is
bounded to 8,192 eligible segments plus an overflow sentinel. Overflow selects
the original per-observation query within the same statement/snapshot, never a
truncated prefix. Zipped provenance scopes, global way/segment precedence,
spheroidal distances, tie ordering and per-observation max+1 reporting remain
unchanged. The row bound does not bound all index scan work or bytes; wide and
discontinuous batches can retain baseline cost plus the preliminary search.
No canonical data, indexes, statistics or server settings are changed by this
read-path optimization.

#### Matcher Snapshots And Reader Policy

Production matcher snapshots are restricted to the route child's complete target
generation vector for candidate and graph expansion. The repeatable-read OSM
snapshot remains open through the short application persistence transaction. On
the primary, a conflicting region promotion waits for that snapshot's locks; a
standby snapshot does not lock the remote primary and can encounter replay
conflicts. In normal mode, an inactive target supersedes the child rather than
failing it. Replica-only mode distinguishes missing/lower heads from a genuinely
newer active head: lag waits for catch-up, while a newer head supersedes once no
target is missing/behind. Reconciliation then schedules the current target.

The reader policy is implemented but defaults to `osm.useReadReplicasOnly: false`.
General-worker, Coverage-worker and enabled API-diagnostic OSM pools always use
`worker.osmDatabaseSecret`, `coverageWorker.osmDatabaseSecret` and
`api.coverageDiagnostics.databaseSecret`, respectively, as complete `{name, key}`
references. All three default to name `workouts-explorer-database` and key
`osmReaderDatabaseUrl` for either boolean value. Each is independent of its
application-database Secret and the other readers, even when defaults share a
name. Existing shared Secrets can retain application, reader and writer keys;
a dedicated reader role can instead use a separate Secret with the existing
component reader TLS selector. True enforces standby-only reads; false uses
normal driver behavior with that same URL and can reach the primary through an
operator-selected all-node URL if its session attributes permit it. The boolean
never selects a Secret or
overrides an endpoint. Former component key-only properties are removed and
rejected by the closed schema without migration aliases; historical cleanup is
documented in the [operator migration guide](osm-read-replicas.md#existing-values-and-minimal-example).

OSM migration/timezone-import hooks instead select
`osm.writerDatabaseSecret: {name: workouts-explorer-database, key: osmDatabaseUrl}`
and `osm.writerDatabaseTLSSecret: workouts-explorer-database-tls`. These writer
settings are independent of the general worker's reader and application-database
selectors. The writer URL must select a role authorized for OSM migrations/imports;
the runtime readers must not be used for writer work. Secret member names are
URL aliases, not database roles. Operator Jobs keep `OSM_DATABASE_SECRET`,
`OSM_DATABASE_KEY` and `OSM_DATABASE_TLS_SECRET` with the same writer defaults;
their environment selectors are independent of Helm and never inferred by
parsing private values. Auto-add must remain false under strict policy. The
feature adds no schema migration or concurrency increase. Existing values need
explicit component-override review and removed-key cleanup with a complete fresh
file, not `--reuse-values`; see the linked operator guide.

New physical connections resolve/copy/shuffle the complete headless A/AAAA set,
retain the original verified TLS hostname and URL fallback order, and require
`pg_is_in_recovery()` at connect and checkout. Promoted pooled connections are
rejected; held transactions cannot move backends or prevent mid-transaction
promotion. A read-only session default adds defense in depth, not a failover lock.
Coverage preflights a reader before claiming, then uses five-second snapshot
attempts/two-second polls with renewing heartbeats. The match budget starts only
after a valid target snapshot, but admission remains held during catch-up, so a
few lag waiters can block all matcher slots. Pool distribution is per new
connection, not balanced per job. At the 10/13/2026 (`America/Los_Angeles`)
checkpoint, live headless DNS/TCP and a separate direct-backend TLS/authentication
client are verified in the linked telemetry guide. They do not certify in-pod
TLS/authentication or mTLS; reader activation/rollout were still pending at that
checkpoint. See [current status](development-progress.md#current-status) for the
subsequent active reader policy; scoped fixture passes do not establish capacity.

OSM importer version 3 retains bounded named local, state, and national-park polygons only
inside the generation build schema. The current derivation version 20 attaches stable park
metadata to fully contained segments before canonical partitions are prepared;
park polygons themselves are not promoted. Local park identity remains scoped by
authoritative municipality and OSM source. State and national park identity is
instead scoped by provider region and OSM source, so one regional park remains
one entity across municipal boundaries and may attribute segments without a
municipality.
For unnamed geometry, connectivity scope is the selected educational ground,
otherwise a named park for non-road geometry, otherwise the authoritative
municipality/county, otherwise the provider region. Named groups use the preserved
case/whitespace-normalized original name across the provider region without a
road/path class boundary, then receive locality-scoped final identities. Exact
shared OSM graph nodes connect compatible physical segments. Named road geometry also
connects within 15 m, extended to 50 m only for two explicitly one-way segments,
to represent divided carriageways. For unnamed groups, different source ways merge only when exactly
two eligible segments meet. At a larger unnamed branch, exactly two segments of
one exact broad class also continue across source ways; other cross-source branches
remain separate. One source way remains continuous through branches.
Imported attribution boundaries include `admin_level=8` municipalities and
`admin_level=6` counties. The smallest covering polygon wins, giving municipality
scope priority and county scope to otherwise unincorporated geometry; provider
region is the final fallback. Short non-road municipality islands up to 25 m are
absorbed into county scope only when both adjacent pieces of the same clipped
source segment belong to that county with matching name state and exact broad
class; parks are exempt.
Named school, college, university, and generic education-ground polygons are also
retained. Education scope applies only to unnamed segments, outranks park scope
for those segments, and is exposed by matcher copy as a display-name fallback;
canonical source names remain unchanged.
For unnamed non-road geometry, park scope takes precedence over locality, so
contained geometry cannot bridge to outside geometry. Named final identities
remain locality-scoped even though graph components are computed across boundaries.
The derivation stages deduplicated physical-segment edges and their participating segments, not
all segment endpoints. Isolated segments become singleton components. Computing
from physical segments rather than incoming logical IDs also splits legacy
disconnected geometry, prevents unnamed road components from bridging unnamed path components,
and prevents an unnamed branching pedestrian network from becoming one identity.
Application persistence derives a separate once-per-workout park attribution
from copied segment tags. State- and national-park roads and paths retain their
individual identities and use the attributed park as display context ahead of
municipality or county context. Unnamed local-park geometry remains represented
only by its park entity in user-facing reads.
Other path identity and map geometry remain unchanged, while driveway and
parking logical paths are omitted from user-facing statistics/history and remain
visible in Coverage tiles.

### Source snapshot lifecycle

1. API locks the current source revision and independently encrypts its canonical
   configuration for the connection-check or source-child job in the same
   transaction as job creation.
2. Worker decrypts the snapshot only when executing the job.
3. Source updates do not alter active snapshots.
4. Source deletion cancels jobs using its snapshots. Queued snapshots are deleted
   immediately; running snapshots remain until cooperative terminal cleanup.
5. Terminal success, failure, or cancellation deletes the complete encrypted
   snapshot row in the same transaction as the terminal transition.
6. Retry uses current validated source parameters.
7. Stale-job maintenance clears snapshots left by abnormal termination after reaching a terminal state.

The M8 rclone adapter writes only validated refreshed cookies back with a
source-generation and job/worker/lease-fenced compare-and-swap. Competing refreshes
are first-writer-wins; source update/deletion, cancellation or lease loss discards
obsolete writes without failing the job solely for a stale refresh. Snapshots
remain immutable. Connection-check completion atomically publishes refreshed
cookies and `connected`, so its own generation increment cannot strand a source
in `checking-connection`. Refresh encryption/persistence failures retain recoverable work
rather than falsely terminalizing it. Local/NFS ingest does not refresh credentials.

PostgreSQL WAL and infrastructure backups may retain older encrypted values according to operator retention. Logical clearing does not promise immediate physical erasure from backups.

## Ingest Pipeline

### Ingest activity and refresh

Ingest file counters measure discovered, skipped, succeeded and failed files,
not completion of every ingest task. After files, source-scoped map-readiness
analysis, account-wide time zone updates and cleanup may still be running.
Manual and Automated sync publish their actual current activity after counters
and results, including zero-import runs. Map analysis prepares later Coverage
processing; it does not execute or wait for route-to-street matching.

The general worker records phase entry before the corresponding work and real
route-check progress through the fenced `app.record_ingest_phase` function
included in 029. Required snapshots share the append-only `job_events` journal
but bypass optional diagnostic limits. A partial `(job_id, id DESC)` index supports
the latest current-attempt lookup. `JobDetail.activity` belongs to source children;
root presentation derives from active children rather than whichever sibling
last wrote an event. The detail GET uses an account-scoped read-only repeatable-read
transaction. Old jobs with no phase evidence retain absent activity.

Selected Running detail polling uses `min(10 seconds, configured polling interval)`.
Queued detail, Task history and the Data Sync summary retain the configured
interval, default **30 seconds**. Background detail polling pauses, and terminal
ingest detail stops interval polling. Existing terminal Coverage eligibility
refresh remains at the configured interval. These UI refresh intervals are
independent of worker claim polling and source auto-sync cadence.

A successful detail update refreshes the open Files page and, for phase-bearing
ingest details, the open Events page, including terminal catch-up. Closed pages
are marked stale without fetching; page/filter/scroll state is preserved. Logs
remain manual-refresh only. Failed detail requests do not trigger artifact
refreshes, and separate responses are not an atomic multi-endpoint snapshot.
Short phases may complete between polls, but remain inspectable in Events; do not
artificially delay work or replay completed phases as current activity.

See the [exact phase messages](functional-spec.md#ingest-activity-messages) and
[activity state semantics](job-state-machine.md#ingest-activity-phases-requires-29).
User-facing time zone terminology uses two words; internal identifiers need not
be renamed. Release evidence is recorded in the [029 follow-up](development-progress.md#ingest-phase-progress-follow-up-029).

### Discovery and staging

1. The adapter enumerates metadata matching incremental or explicit-range rules.
2. Incremental sync selects new or changed objects and ownership-blocked objects whose captured epoch is stale (requires 26).
3. New bounded Manual Sync applies the same metadata-skip policy within inclusive export-file dates; only explicit `reprocessAllFiles: true` processes all eligible files in a fresh run. Existing bounded jobs with no persisted flag retain their original force policy.
4. The M8 remote transport stages at most one file per slot in a random job directory with `0700` access under `WORKER_STAGING_ROOT/private`, with mode-0600 files; the outer root defaults to `/var/lib/workouts/staging`. The old `/tmp/<user-id>/<job-id>/` layout is not the contract.
5. Remote downloads use a non-final filename and are atomically promoted after bounded download and pre/post metadata validation.
6. Local/NFS adapters may read directly when staging is unnecessary.
7. File processing writes transactional workout results and source-file state.
8. Cleanup removes the staged file before the slot claims another.

iCloud incremental discovery uses remote ID, size and modification time, not a
remote content hash. A same-metadata content overwrite can go undetected; explicit
bounded reprocessing reads current bytes. Rclone iCloud v1.75.2 advertises
`hash.None` and returns `hash.ErrUnsupported`: no free remote MD5 is available.
The `v2:rclone:` SHA-256 hashes source-scoped metadata, not file content. Actual
processing computes local content SHA-256 while parsing; skipping never downloads
all remote bytes for a hash and does not prove their integrity. One application
discovery per sync does not imply one native listing or HTTP request: backend stat/download
can list internally. Flat discovery is bounded to 100000 entries, 10000 eligible
exports and 32 MiB stdout; files are capped at 64 MiB, metadata operations at
60 seconds and downloads including checks at five minutes. Native retries are
bounded. Auto-sync defaults to 24 hours and may be configured to 12 hours;
Data Sync UI polling is a separate interval.

With migration 025, `app.record_successful_source_object` retains the real
successful checksum even if any workout in a file was skipped for a live owner.
It records `ownership_blocked_epoch` from the ingest fence captured before
ownership decisions, never from a later epoch read. This applies to both
all-blocked and mixed files. Unchanged metadata plus a successful checksum skips
without reread/download when the blocker is absent or its epoch is current.
Only a committed source live-to-deleted transition increments the account epoch;
adoption, cookie refresh and auth/config generation changes do not.

On the next **fresh** incremental job, `app.read_ingest_candidate_actions`
reselects stale blocked files despite identical metadata and bytes, allowing a
deleted owner's workout to be adopted. If blockers remain, success records the
new captured epoch. Same-job manifest actions stay frozen, completed file
checkpoints never reopen, and recovery restores committed counters/events without
double counting. Source deletion does not enqueue an immediate sync: the next
auto-sync or a new manual job is needed. New bounded defaults use the same
owner-aware policy; explicit reprocessing is the opt-in force path. Successful
bytes are not invalidated or deleted to implement ownership retry.

### Reprocessing, Retry And Receipts (Requires 29)

`IngestCreate` accepts optional boolean `reprocessAllFiles` only with both valid
ordered dates, including when `false`. New bounded requests default to false and
persist an explicit boolean on parent and child parameters; incremental requests
and parameters omit it. Existing bounded parameters with an absent flag still
mean force. No new/old contract marker, historical parameter rewrite or frozen
manifest rewrite is needed.

New API enqueue uses coalescing v2 over sorted source IDs/generations, mode,
dates, effective force and root/retry ancestry, including each source-child
predecessor. Omitted/false bounded requests coalesce, but force, retries of
unrelated roots and distinct retry paths remain separate. Existing sorted
source-row locks and active-job uniqueness provide coordination; no new lock is added.

At fresh retry discovery, valid successful processing of the same file metadata
on the current source-child ancestor path skips regardless of original force.
Sibling/other-branch/unrelated success is not ancestor evidence. A forced retry
processes failed, unreached or changed files without being defeated by older
unrelated successful inventory. A non-forced retry additionally permits ordinary
valid inventory skips, including files its original run intentionally skipped.

Private `source_files.observed_identity` and `ownership_blocked_epoch` receipts
are stamped at successful file commit from the admitted ingest-write capability,
pre-decision ownership epoch and exact frozen manifest. Terminal-file and manifest
immutability protect them. An ancestor receipt survives movement of the latest
inventory success pointer. Historical NULL receipts require sufficient current
inventory/checksum/manifest proof or may need a safe extra download, never an
invented universal skip guarantee. Frozen same-job actions, terminal checkpoints,
counter restoration, date/ownership/account fences and workout/route identity
remain intact. Explicit file force does not guarantee Coverage rematching. See
the [complete contract](ingest-reprocessing.md).

### Parsing and normalization

- Provider reference: [Health Auto Export workout JSON format](https://help.healthyapps.dev/en/health-auto-export/export-format/workouts/), including versioned workout fields, aggregate units, and route-point semantics.
- Parse `/data/workouts` without depending on JSON key order.
- Preserve provider duration independently from rounded timestamp differences.
- Prefer explicit provider aggregates over sums of incomplete sample arrays.
- Retain ordered route points and equal timestamps.
- Preserve optional values as unknown rather than inventing zeroes.
- Record recoverable adapter warnings for suspicious units and quality values.
- Reject invalid file JSON without leaving a falsely complete file result.

### Derived processing

- Build 3D raw route geometry.
- Persist raw map geometry as a multi-line route, beginning a new segment when a positive timestamp delta reaches three times the running positive-delta average for the current segment; duplicate and backward timestamps remain ordered but do not affect that baseline. source 009 then splits connections longer than Coverage's 200-metre spatial limit without resetting that cadence baseline or reconnecting earlier components. Original observations are retained; projected chunks/LODs and focused browser geometry honor the same spatial boundary. See [route continuity](decisions/015-route-continuity-and-preserved-observations.md).
- Encode zero-length multi-line components as route-attributed point features alongside line features; MapLibre renders them as route-colored circles with the same selection and hover semantics.
- Derive bounds and elevation statistics.
- Resolve or update workout timezone.
- Record durable route-work obligations with the file, then resolve OSM readiness and enqueue bounded compatible Coverage parents after commit and file cleanup; source completion does not wait for matching.
- In the dedicated coverage worker, generate bounded nearby segment candidates and Viterbi-decode sequence-aware connected traversals using distance, quality, topology, timing, and reliable heading evidence.
- Atomically dissolve decoded spans into one unique workout/physical-segment match geometry, upsert unique workout/logical-path attribution, rebuild affected logical-path daily and all-time rollups, and advance the account data generation used by private tile URLs.

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

The implemented route contract is in `api/openapi.yaml`; product behavior and
explicitly planned features are described in `functional-spec.md`.

### Private vector tiles

The API owns the public private-tile route. A browser never calls Martin directly.

1. Browser requests a route or coverage tile using a session-scoped map selection.
2. API authenticates the session and verifies account ownership.
3. API authorizes before cache lookup/coalescing and again after fetch and encoder admission, including cache hits; no database transaction spans either wait.
4. On a miss, internal Martin invokes the approved security-definer tile function, independently revalidating scope before querying private render chunks.
5. API returns the tile with `Cache-Control: private, no-store` and `Vary: Cookie, Authorization, Accept-Encoding`, including validation errors.

Tile URLs include an account data generation so a redraw after ingest or deletion does not reuse stale private data.

Introduced by deployed folded 028, `app.lock_account_data_generation()` retains its
historic name and ABI but performs a nonlocking MVCC read in the API's existing Repeatable Read
transaction. Selected data and generation must come from that **same snapshot**.
A concurrent writer can commit before selection creation returns, making the new
capability already stale. Existing current-generation validation rejects it;
the API must not bless old selected data with a newly read generation. Removing
the generation-first lock eliminates its account/workout FK cycle, including the
deferred focused-membership FK, without weakening those constraints. This helper's
ABI does not make old runtime-28 images compatible with the folded cohort contract.

### Tile-Local Rendering (031)

All six optimizations are implemented and **deployed at Helm 65 for both Routes
and Coverage**, not capacity-certified: projected chunks, local clipping/LOD,
Coverage deduplication, independent focus, bounded UI reuse, and bounded API
cache/coalescing/admission. [031 SQL](../api/migrations/sql/031_tile_rendering.sql)
and [tile delivery](../api/tile_rendering.go) define the contract.

This is the retained **031 foundation/legacy-function contract**. The deployed
[031 follow-up](#tile-rendering-and-delivery-031) supersedes its active raw
LOD, Coverage deduplication and browser delivery path without editing 031.

- Private security-owner/forced-RLS `raw_route_tile_chunks` and
  `coverage_tile_chunks` retain EPSG:3857 pieces of at most **256 vertices**.
  AFTER-write maintenance and cascading source FKs keep chunks transactional with
  raw routes and actual partial match evidence. GiST geometry indexes and covering
  selection/order and workout-metadata indexes bound local probes. No GPS,
  matching, canonical identity or visit-count policy changes.
- Both MVT functions retain `(z,x,y,query_params json)` and exactly four opaque
  account/session/selection/generation parameters. Independent database authorization
  remains mandatory. API-only `authorize_map_tile(uuid,uuid,uuid,bigint,boolean)`
  returns the earlier session/selection expiry, validates current generation and
  Coverage context, and caches no authorization decision.
- Select only indexed projected chunks intersecting the tile envelope expanded by
  **64/4096** of its width, then clip locally. Below zoom **15**, simplify stable
  pieces **before clipping** with topology preservation at **0.25 CSS pixels**
  for a 512px tile; with MVT quantization the error stays below **0.5 CSS pixels**.
  At zoom 15 and above use full piece geometry. Endpoints, short components, gaps
  and stationary raw-route dots remain; raw workouts never deduplicate across IDs.
- Coverage exact-EWKB deduplication is scoped by canonical ID **and source kind/ID**
  before local union, with a singleton fast path. It never substitutes full OSM
  geometry for actual partial evidence. Canonical metadata/counts still cover the
  whole inclusive date range; checked workouts affect tile geometry only.
  `coverage_mvt` now emits only `coverage` and `coverage_parks`, not duplicate
  focused unions. Independent exact Coverage-focus GeoJSON is unchanged.
- Raw focus uses dedicated GeoJSON line/dot layers from the existing segmented
  raw-point request, sharing its data with start/finish and direction markers.
  Focus-only changes do not renew background selections. Focus line/arrow readiness
  is independent of background tiles; stable filters/paint writes avoid repaint
  loops, and pending tiles during a user pan do not erase the current line or ready arrows.
  Focused Coverage hover fallback preserves canonical IDs and source kind.
- Separate raw and Coverage focus LRUs each hold **16 entries / 8 MiB serialized
  JSON**, with fixed **five-minute TTLs** from load, not access. Generation/context,
  deletion, cancellation and latest-request guards prevent stale geometry reuse,
  old 404 capability refresh, or old geometry impersonating a new owner. These are
  not JS-heap bounds, the 8-entry entity-detail cache, or the hover/table caches.

The API uses a dedicated non-redirecting, uncompressed transport and accepts only
complete validated positive or legitimate empty MVT responses up to **5 MiB**.
Its per-process LRU defaults to **32 MiB / 512 entries / 30s**, expiry-capped by
the authorized capability. Coalescing keys are typed account/session/selection/
generation/kind/z/x/y tuples; no UUIDs enter metric labels or new logs.
Authorization runs before lookup and after waiting, never from cache.

Default admission is **4 occupied render/delivery slots**, **64 FIFO queued
distinct jobs**, **256 admitted HTTP requests** and **64 waiters per job**.
Queue/request/upstream budgets are **1s / 6s / 5s**. The last waiter leaving removes
queued work or cancels active transport; the occupied slot remains until transport
return **and the last response writer releases it**. Cache-hit bodies are pinned
and charged by allocated capacity, not length, so eviction cannot hide retained
payload. Conservative default payload accounting is **52 MiB + 4 bytes retained**,
or **60 MiB + 4 bytes during growth**, excluding runtime overhead/RSS. An independent
**six-body-job ceiling** applies even though configured concurrency accepts 1-16.
Limits are per API process, not a global database quota; Martin caching stays off.

Seven private `api.mapTiles` settings map to API-only environment variables:

| Helm key under `api.mapTiles` | Environment variable | Type / default | Accepted chart bounds |
| --- | --- | --- | --- |
| `concurrency` | `MAP_TILE_CONCURRENCY` | integer / `4` | 1-16; effective body-job ceiling 6 |
| `queueSize` | `MAP_TILE_QUEUE_SIZE` | integer / `64` | 0-256; zero disables waiting |
| `cacheBytes` | `MAP_TILE_CACHE_BYTES` | integer / `33554432` | 0-134217728; zero disables caching |
| `cacheEntries` | `MAP_TILE_CACHE_ENTRIES` | integer / `512` | 1-4096 |
| `cacheTtl` | `MAP_TILE_CACHE_TTL` | string / `30s` | whole seconds 1-300 or minutes 1-5 |
| `queueTimeout` | `MAP_TILE_QUEUE_TIMEOUT` | string / `1s` | whole milliseconds 100-3000 or seconds 1-3 |
| `requestTimeout` | `MAP_TILE_REQUEST_TIMEOUT` | string / `6s` | whole seconds 5-15 |

The [schema](../helm/values.schema.json), [runtime validation](../internal/config/tile_rendering.go)
and [installed-values reference](../helm/README.md#private-tile-budgets) define these
bounds; runtime requires queue timeout below request timeout. API ConfigMap
checksums roll API pods on changes. Settings are not public browser config.
[Role defaults and controlled rollout](database-migrations.md#tile-local-geometry-031)
provide the SQL execution bound; HTTP abort is not proof of immediate PostgreSQL
cancellation. Fresh Martin connections inherited the verified role defaults during
the approved cutover; its 1.15.0 pin, pool of four and disabled cache were preserved.
[Source and deployment evidence](development-progress.md#tile-local-map-optimization-031)
and [synthetic cardinality results](coverage-acceptance-results.md#tile-local-source-verification-031)
retain their checkpoint scope. The [current release](development-progress.md#tile-rendering-and-delivery-031)
keeps final live viewport/render/focus acceptance separate from implementation.

### Tile Rendering And Delivery (031)

**Deployed at Helm 67, schema 31/minimum 31.** The canonical authorized desktop/4K
run completed all required views without observed delivery errors; dense high-zoom
raw client completion remained slow, not a usable-render or capacity acceptance.
The API calls private `app.raw_route_mvt` and `app.coverage_mvt`; public URLs,
four opaque scope parameters, independent MVT authorization and API-only
`authorize_map_tile` stay unchanged. All 031 functions and the literal ingest fence
31 remain untouched: shipped worker 31 and legacy API readiness pass. This is
concrete rolling-image fallback support, not database rollback or version dispatch.

- [031 SQL](../api/migrations/sql/031_tile_rendering.sql) precomputes complete
  projected raw components **before chunking**, eliminating artificial chunk-end
  constraints on simplification. Bands **10/12/14** use the finer band between
  checkpoints. `ST_Simplify(..., true)` preserves collapsed short lines; if positive
  original length would become zero, retain the whole original component, including
  closed moving loops. Never turn movement into dots. At **z >= 15**, use original
  exact 031 raw chunks. Workout identity, genuine gaps and stationary dots remain.
  New forced-RLS owner-only LOD storage is backfilled and maintained transactionally.
- Coverage eligibility uses bounded probes **once per tile-local workout**, tested
  with cold/generic estimates. Deduplicate at **MVT display precision before union**,
  isolated by canonical ID and source kind/ID. Actual partial evidence, whole-range
  counts, canonical metadata and exact detail/focus geometry remain unchanged.
  `coverage_mvt` sets function-local **`work_mem=32MB`**: not a global setting or
  total-query memory bound; multiple operations and four concurrent slots multiply use.
- [PrivateTiles](../ui/src/privateTiles.ts), its [tests](../ui/src/privateTiles.test.ts)
  and [MapPage integration](../ui/src/MapPage.tsx) use public MapLibre `addProtocol`
  main/worker handling, with same-origin allowlisted URLs and cookie credentials,
  never tokens or opaque private MapLibre internals. Per map: **4 active fetches,
  128 jobs, 64 waiters/job**, separate transferable `ArrayBuffer` ownership per
  consumer. Initial queue wait is bounded to **60s**; a separate **15s service/retry
  budget starts at first dispatch**, not enqueue. At most **three attempts**,
  jittered **250/750ms** backoffs and `Retry-After` capped at **1s**; sleepers release
  the active slot and rejoin the tail. Generation/style/unmount cancellation drains
  stale jobs; one event-loop turn before refill admits worker abort messages.
- There is no source-wide `setTiles` retry budget in this path. A scope's 401 or
  404 callback runs once and stops dispatch; browser-internal 404 handling rejects
  as **410 before MapLibre can treat 404 as empty**, without changing the public
  API's 404 contract. Validated PBF 200 and legitimate 204/empty responses remain
  valid cacheable payloads; errors are never converted into successful empty tiles.
- [API compression](../api/tile_compression.go) streams negotiated **gzip BestSpeed**
  for successful bodies **>= 1,024 bytes**, respecting encoding q-values/refusals.
  Errors, small bodies and empty responses stay uncompressed. The raw cache and
  **5 MiB uncompressed limit** remain unchanged; no full compressed-body copy.
  **Four process-wide reusable encoders** bound cache-hit compression too, including
  the 256-admitted-request case. No encoder allocation before admission, no DB
  transaction while waiting, and final authorization **after encoder admission**.
  Denial preserves the authorization Problem response; encoder close precedes body
  lease release. [Delivery code](../api/tile_metrics.go) retains private/no-store and
  `Vary: Cookie, Authorization, Accept-Encoding` on all responses.

API limits remain **4 occupied slots / 64 queued jobs / 1s queue / 6s HTTP / 5s
upstream / 4s SQL**; browser budgets do not enlarge them. No CPU/memory, queue,
timeout, resource or Martin pool increase accompanies this delivery change.
`areTilesLoaded()` can count failed tiles as loaded: it is not completeness proof.
See the [scoped final result and remaining client investigation](coverage-acceptance-results.md#tile-delivery-status-031);
wire-size reduction does not establish latency improvement or a need for more resources.

### Public map and OSM data

- Public runtime configuration defines an ordered, extensible set of base-map style families. Each family has a stable ID, user-facing label, light and dark MapLibre style URLs, structured attribution, and the provider resource origins required by browser policy.
- The initial families are MapTiler Outdoor, customized MapTiler Streets, and Stadia Alidade Smooth. The configured fallback is Alidade Smooth unless an installation explicitly chooses another available family.
- Workout-type defaults map readable provider labels to family IDs. Labels are normalized with the same Unicode normalization and case folding used by ingest rather than matched by mutable display spelling.
- Automatic selection uses a mapped family only when every visible routed workout type resolves to that family. Mixed mappings, unmapped types, no visible routed workouts, and indeterminate selections use the configured fallback.
- A manual family choice lasts for the current Map visit. Theme changes choose the corresponding light or dark variant without clearing that choice; leaving Map returns selection to automatic mode.
- MapLibre style replacement preserves the camera and application selection state, then reinstalls private route or coverage sources and layers. Public style documents never carry private tile authorization.
- Attribution is structured text and validated HTTPS links rather than operator-provided HTML. It changes with the active family and remains visible on desktop and mobile.
- The browser may request approved public style resources directly. Public provider credentials delivered to the browser are not secrets and require provider-side origin restrictions, minimum privileges, quotas, monitoring, and rotation; Stadia domain authentication is preferred where available.
- The UI content security policy permits only configured provider resource origins needed for style documents, tiles, glyphs, sprites, and images.
- The UI permits runtime inline CSS because MapLibre positions controls and popups through element styles; scripts remain self-hosted and hash-restricted, and configured attribution never accepts operator HTML.
- Regional road/path hierarchy comes from an ordered configuration of provider-qualified named extracts, initially Geofabrik `norcal`.
- Runtime configuration names these datasets regions: `osm.regions` is the explicit initial list, `osm.dataProviders` is the ordered automatic-discovery catalog list, `osm.autoAddRegions` controls discovery, and `osm.maxAutoDownloadBytes` bounds each automatically selected PBF.
- Development and production use the same public-data-only `osm` database on the production PostgreSQL server; private workout matching results remain isolated in each environment's application database.
- The updater resolves extract metadata through Geofabrik's versioned index, downloads current PBF files into ephemeral local scratch space, validates and records source metadata, and removes the files after processing.
- `scripts/osm-update-operator.sh` is the implemented interactive entry point for a new or existing provider-qualified region. It launches `osm/manual-update-job.yaml` with the `workouts-osm` image and supports start, status, resume, unblock and abort. The Go updater filters/imports public data, runs checked-in resumable SQL stages, validates and atomically promotes a generation. Resume checks tool/version/checksum fences; operator-blocked builds do not retry without an explicit unblock. See [ADR 0008](decisions/008-osm-import-and-segment-identity.md) and the [resumable rebuild contract](osm-resumable-rebuild.md).
- Selective import retains eligible paths, required node/relation lineage, municipal boundaries, and derived matching data while omitting buildings, POIs, addresses, and unrelated rendering features.
- Routes outside promoted regions remain raw-route-only while coverage is unavailable. Any future automatic-addition implementation must resolve a containing named provider region from locally cached public catalog polygons, record only its public region ID in the maintenance queue, and never send route coordinates to the provider; no automatic download is currently queued.
- Automatic region addition remains deferred. A no-evidence diagnostic resolves its route against the local provider catalog and reports each required but inactive region so the UI can present coverage as unavailable rather than as a successful zero-match review.
- The unavailable-region fallback samples at most 256 evenly distributed route points, including both endpoints. The diagnostic response lists stable provider-qualified region IDs and display names; the Coverage UI shows its alert/retry state and suppresses normal review controls while preserving the raw route.
- Diagnostic clipping discards non-positive or degenerate LineStrings before persistence, logs their count, and recomputes portion/segment totals and generation provenance from valid evidence. One unusable OSM substring cannot abort an otherwise valid diagnostic run.
- Map routes derive small green direction triangles from cached segmented raw routes. In Routes mode they follow whichever visible route is magenta, preferring the delayed-hover route over the focused route; explicit focus cancels an old hover or its pending timer. In Coverage they follow the focused diagnostic raw route. Marker spacing is measured in projected screen pixels; the bearing window starts three samples before/after a marker and expands within the same route component until it spans at least four screen pixels, rather than discarding dense GPS samples. Cached markers include zoom, bearing, pitch, viewport size and projection identity. Ready arrows remain visible and reuse their geographic positions during an actual user drag. Programmatic camera movements, including pan-only route/road fits, hide the arrows immediately; zoom, rotation and pitch also invalidate them. After movement settles, wait for a final-camera render and the current direction-worker/focus-source update, independently of background private/base tiles in source 031. Render-time readiness checks do not repeatedly write unchanged paint properties. Superseded route/camera/style updates cannot reveal stale arrows; route lines and start/finish markers do not wait for this handoff.
- Clicking a visible route feature in Routes mode immediately focuses and fits that workout, ensures it is checked, and issues a one-time centered scroll for its route-list row. Clicking a route-list row performs the same focus/fit selection without automatic list scrolling.
- Summary's Show on map handoff checks the fresh capability's target readiness before fitting. If production Coverage is active but the target has no coverage result, it switches to raw Routes and retains focus; current/stale usable coverage keeps Coverage active. This one-time handoff does not force mode changes on ordinary tab reopening or capability refreshes. Cached readiness cannot clear the requested focus while capability creation is pending.
- Accepted Summary deletions, including an already-unavailable response, notify the shared shell. It prunes the Map catalog, checked IDs and deleted focus, invalidates cached Coverage views, and advances a collection revision without changing the date range or refetching the complete workout collection. Late capability/overlay results cannot restore deleted entries. An external deletion that makes an explicit capability return 404 triggers one authoritative range-catalog recovery and retains only the still-available checked IDs, preserving explicit none rather than selecting all.
- Map capability creation validates optional focus against the actual selected rows before commit, returning 404 rather than a retryable 503 for unavailable focus. Fresh-login and deep-link recovery also covers all-range selections: it clears unavailable focus/history/overlays and reuses one unfocused catalog capability without broadening an explicit subset or changing preferences. Genuine 503 errors retain bounded retries.
- In Routes mode, start/finish markers use the same cached raw-route owner as the direction arrows: delayed hover takes precedence over persistent focus, and both endpoint markers are moved above the magenta route and direction-marker layers.
- Map's Space-key isolation follows the [three-mode layer visibility contract](functional-spec.md#space-key-layer-visibility). Hidden layers fade out and back in over 400ms without source teardown; directional markers use that transition only in diagnostic Coverage, with zero-duration opacity changes in Routes mode.
- Before a route's first match commits, the application stores a canonical route-input digest and monotonic revision plus map-data readiness. A route is `map_data_ready` when every point resolves to the deterministic smallest configured active provider region; readiness records region generation provenance, not successful matching or a covered-path claim. Separate processing and result states distinguish queued/running/failed work from durable current/stale results.
- Region updates are globally single-flight by provider-qualified region ID. Promotion advances a region generation and raises account/region coverage watermarks; initial loads target missing coverage, while refreshes target every intersecting routed workout so existing attribution is reconciled to the latest OSM generation.
- Coverage updates are account-owned bounded parents; new ingest cohorts may mix regions, with per-route generation vectors authoritative and retry parents isolated from fresh work. Desired and applied OSM-generation watermarks ensure a promotion racing active work produces a successor rather than losing rematch work.
- Public endpoint limits, user-agent requirements, and attribution must be respected.
- Manual OSM refresh replaces or updates regional data and reconciles copied matched segments.

### Rclone

Milestone 8 source implementation is deployed and ingest/available-map Coverage
recovery are verified. [Milestone 8 closeout](development-progress.md#milestone-8-closeout)
owns current acceptance and browser-verification status.
The [initial verification](implementation-plan.md#verification-results-10122026)
retains its synthetic/disposable scope. The `dev-20261018`, Helm revision 28,
schema/minimum runtime 28, marker 27 receipt is a historical pipeline checkpoint.
See [iCloud setup](icloud-source-setup.md)
for private setup and [current status](development-progress.md#current-status) for
actual live-version evidence and verification boundaries.

- Native rclone 1.75.2 runs once per bounded operation with a job-private profile,
  not as a daemon/RC service. Packaging checks pinned amd64/arm64 release archive
  SHA-256 values. External interactive setup supplies a ready session.
- The closed version-1 `health-auto-export-icloud` config requires `path`,
  write-only `appleId`, rclone-obscured `password`, full `cookies` string and
  `trustToken`; non-secret `clientId` is optional. Public config contains only
  `version`, `path` and optional `clientId`, never Apple ID or masked secrets.
- PATCH retains omitted secrets, rejects null/empty required values, and rechecks
  canonical config changes, including non-secret path/client identity changes.
  Native `type=iclouddrive` and `service=drive` are fixed. Host remote aliases,
  `description`, `_auth_session` and arbitrary flags are unsupported.
- Reversible password obscuring is not encryption. The application encrypts the
  complete config and independently encrypts immutable job snapshots under ADR 0007.
- Native credentials reside only in the private mode-0600 profile, never argv.
  The process has no interactive stdin, strips inherited `RCLONE*` variables,
  uses private HOME/XDG/cache/temp paths, and has bounded output, timeouts and
  process-group cancellation. Native endpoint files/config backups are cleaned too.
- Known expired trust, authentication failure or explicit missing PCS/device
  approval during ingest sets a fenced `connection-failed` status and notification
  that blocks future ingest. Transient upstream failure fails ingest without
  disconnecting a connected source or diagnosing auth from an HTTP status alone.
  Explicit checks retain their existing failed-check status contract, including
  transient check failures.
- ADP/Apple approval and periodic external reauthentication remain upstream
  constraints. The application never reads/uploads host `rclone.conf` automatically
  and does not provide source CRUD UI or in-app 2FA. Local/NFS remains the fallback.

Provider-backed local/iCloud imports share the
[account-wide identity/adoption contract](#provider-identity-and-source-adoption-requires-26).
Only ID-less fallback identity remains source-local. Changing transports does not
purge history, merge ambiguous records or bypass a live owner.

### SMTP

- Initial provider: `smtp.mail.me.com` using STARTTLS and an app-specific password.
- Email is used only for invitations and password resets in the MVP.
- Planned admin announcements remain in-app rather than adding email delivery.

## Authentication And Authorization

### Authentication

- Local passwords use Argon2id.
- Session, invitation, and reset tokens are cryptographically random, contain at least 128 bits of entropy, and are stored only as one-way verifiers.
- Signup and reset token consumption is atomic and single-use.
- Public authentication and recovery routes use rate limits and enumeration-resistant responses.
- Browser sessions use Secure, HTTP-only, SameSite cookies.
- API clients may use the opaque session token as a bearer token.
- Cookie mutations require a session-bound CSRF token.
- Session lifetime is globally configured, default six hours absolute (`SESSION_LIFETIME`); it is not extended by route clicks.
- A private 401 is a signal to confirm the cookie session, not proof of expiration. In response to that signal, the UI clears private state only after a confirmed session 401; canceled/unavailable authentication lookups return 503 rather than invalidating credentials. A valid-cookie refresh of a login return URL resumes the protected route instead of falling through to Summary.
- Navigation, including clicking the already-active Map tab, must not disable session confirmation. Background session-query 401s also confirm the current cookie before clearing cached private state. Deadline checks rearm after an unchanged valid response or transient failure, retrying every ten seconds once the advertised deadline has elapsed; confirmed logout preserves the current protected return path. A newer login or unmounted application remains protected from late checks.
- Password reset revokes all existing sessions.
- OIDC remains behind a future authentication-provider boundary.

### Authorization

- Administrator and data-owner roles are separate identities.
- Administrators can create invitations. User-management completion, announcements, account purge and administrator OSM status/refresh remain planned; none grants access to private workout data.
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
- Runtime namespace: operator-selected Helm release namespace (normally `workouts-explorer`); templates do not require an Argo CD deployment

### CI and images

Gitea Actions performs:

1. Formatting, linting, unit, contract, migration, and UI tests.
2. OpenAPI generation-drift validation.
3. Container builds.
4. Security and dependency checks as configured.
5. Push to Harbor using both an immutable eight-character commit SHA and a mutable semver from `VERSION`.

Helm `version` and `appVersion` follow `VERSION` for the coordinated release.

### Deployment Ownership

- The application repository contains the chart but no live Argo CD Application.
- The current xdev release is installed/upgraded directly with Helm. Shared infrastructure is provisioned by its owning Ansible automation; this repository does not assert the existence of an external live Argo CD Application.
- Argo CD is supported by chart hook annotations but is not required. A future live Application and environment values belong to deployment automation, not this application repository.
- Environment values can pin immutable image SHA tags or digests; mutable development tags require deliberate rollout handling.
- Chart defaults set `ingress.enabled: false`.
- Hostnames, image registries, resources, Secret names, storage limits, and ingress settings are deployment values.
- xdev is development/test. Eventual production deploys directly to the host cluster. Separate environments keep private application databases and use explicit telemetry environment labels; they may share the public OSM database.

### Database migrations

- Goose owns ordered application and app-derived schema migrations.
- API and worker do not migrate on startup.
- A Helm pre-install/pre-upgrade migration Job runs the API image's migration command; equivalent Argo CD PreSync annotations support GitOps deployments.
- Migrations acquire a PostgreSQL advisory lock.
- Deployment stops if migration fails.
- Shipped migrations are normally immutable and fixes use forward migrations. Explicitly approved development-only consolidation is an exception and requires a coordinated runtime/bookkeeping repair; it is not a general production procedure.
- Expand-and-contract changes preserve compatibility during rollout.
- CI validates clean creation and supported upgrade paths.
- Destructive migrations require an operator backup note.

M8 introduced immutable forward migration `021_icloud_sources.sql`, which requires
minimum runtime 21 and fences runtime-20 worker claims and pre-upgrade scheduler
leases from new source work. Forward-only `022_legacy_coverage_bootstrap.sql`
introduced schema 22/minimum runtime 21; migration 023 introduced coordinated
Coverage readers. Adoption **requires minimum runtime 26**:
`024_provider_workout_identity.sql`, `025_owner_aware_source_inventory.sql`
and `026_adopted_workout_coverage.sql` jointly require minimum runtime 26.
Intermediate schema 24/25 is not an independently usable rollout target. Only
source deletion advances the ownership epoch; adoption does not.
The active sequence continues with ordinary
`027_coverage_recovery_baseline.sql` and forward
`028_ingest_coverage_pipeline.sql`. Runtime readiness requires marker 27 as well
as the complete role/function contract; pipeline minimum runtime is 28. Normal
upgrades never rewrite the ledger. Validation, upgrades, recovery and rollback
are defined in [Application Database Migrations](database-migrations.md).
[Development Progress](development-progress.md) owns live-version evidence.

Historical repair evidence follows, not a current deployment claim.
The 10/13/2026 deployed compatibility repair replaced only its marked five-function
block, preserving owners/ACLs and schema/Goose version 20/minimum runtime 18,
without M8 source DDL or an application build. The full future migration provides
the durable repair after restore/upgrade. Historical schema-20 M7 and schema-21
M8 receipts remain unchanged. The authorized repair and normal failed-only retries
are now verified with all affected routes current and no remaining failed/queued
repair work; account identifiers and counts remain private. Those historical
schema-20/21/22 receipts do not verify migrations 024-026, coordinated runtime-26
images, source adoption rollout or live-cloud acceptance.

### Runtime configuration

Secret references supply API/worker environment variables and mounted keyring,
CA/client TLS and other secret files. The configuration should expose only actual
operator policy or environment values, not correct-by-construction domain logic.

Appropriate settings include:

- Public URL and listening ports
- Session lifetime and pagination maximum
- SMTP, PostgreSQL, OTel, and master-key secrets
- Sync cadence and stale-data threshold
- File concurrency and staging roots
- Base-map style families and variants, structured attribution, provider resource origins, workout-type family defaults, fallback family, fit padding, and presentation defaults
- Elevation-visible workout types
- Worker polling and UI polling interval
- Global OSM reader-only boolean policy, independent per-component reader name/key references and separate writer name/key/TLS selectors, without changing matcher capacity

Timezone inference order, deletion semantics, attribution uniqueness, and cleanup guarantees are coded rules rather than configuration.

## Observability

The implemented component/metric inventory, dated collection evidence, dashboard
models, SLO definitions and remaining gaps are in
[Telemetry and monitoring](telemetry-and-monitoring.md). This section describes
the collection architecture, not a fixed live pod/IP/series-count snapshot.
The broader minimum-metric list remains intended scope, not a claim of complete
instrumentation or validated multi-user capacity.

### Collection And Identity

API, general worker and Coverage worker export Go runtime/HTTP/database/pool
metrics through the OTel SDK. API adds private-tile forwarding metrics and the
Coverage worker adds eight owned instruments. UI and Martin use Contrib 0.156.0
sidecars for native metrics and separate collector self-telemetry. Collector
CPU/memory/uptime belongs to the sidecar, not nginx, Martin or another app process.

All seven producer/self identities preserve datapoint aliases `service_name`,
`service_namespace`, `service_instance_id` and `deployment_environment`. The Go
exporter projects these bounded aliases from standard resource attributes; the
sidecars do the same in their pipelines. Namespace/environment are chart values
and the instance is the originating pod UID. Current Go Prometheus job identities
are namespace-qualified too. Grafana retains compatibility with older unqualified
Go history, but the app SLOs use the explicit origin aliases rather than gateway
pod identity. `target_info` remains a resource-label exception, not a substitute
for datapoint origin or successful application work.

The default exporter endpoint is
`grpcs://otel-gateway-collector.observability.svc.cluster.local:4317`, with CA and
hostname verification and no plaintext fallback. Explicit HTTP/HTTPS overrides
remain for fixtures; an empty endpoint disables the Go instrumentation and Helm
sidecars. Sidecar trust mounts contain public CA material, not database credentials
or private keys. Gateway scraping is infrastructure-owned; the app chart does not
add an application ServiceMonitor/PodMonitor or public metrics proxy.

Virtual-cluster DNS needs a narrow host-gateway forwarding rule while preserving
other custom forwards. See the
[reachability notes](telemetry-and-monitoring.md#private-dns-and-reachability).
Service IPs, live series counts, pool occupancies and memory samples are mutable
operational evidence, not architectural constants or capacity guarantees.

The Helm-owned Operational and Historical Grafana dashboards are provisioned
from the chart. Four opt-in Pyrra SLO resources define scoped API GET availability
and latency, tile forwarding success and successful-forward latency. The host
Pyrra controller generates labeled rule ConfigMaps, and Thanos Ruler evaluates
them with ABORT partial-response semantics. Namespace-sync deployments preserve
names; xdev enables the chart's generated-ConfigMap backsync guard. Only the
namespace badge is propagated to the dashboard. Healthy evaluation and collection
do not establish full-window compliance, target attainment or alert delivery.

### Traces

The intended trace coverage includes the following; this is not an inventory of
completed spans across every worker or operator stage.

- Trace API commands through job creation.
- Link worker job traces to the originating request without requiring one long-lived span.
- Trace database, source transport, parsing phase, OSM lookup, matching, rollup, and tile proxy latency.
- Never attach coordinates, route geometry, health values, or credentials as attributes.

### Metrics

Intended minimum metrics include (several remain unimplemented):

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

General-worker job/ingest/scheduler/freshness lifecycle instruments, Coverage state/
reconciliation gauges, active requests, diagnostic-specific matching and browser
signals remain gaps. Native nginx request counts are not request latency/status
histograms; native Martin HTTP timing is not API authorization or browser render
latency. All proposed SLOs require eligibility, missing-data, retention and
full-window rule-evaluation validation. The four opt-in API/tile objectives
described above are implemented and installed in xdev; additional lifecycle,
matching and browser SLOs remain proposals. Installation alone does not establish
an accepted full-window compliance or alert-delivery result.

### Deferred Jobs And Databases

OSM update/identity-evaluation and bootstrap, migration, role-provisioning and
timezone hooks are rare short-lived Jobs without an active provider or metrics
endpoint. Basic Go runtime only, with bounded final metric flush/shutdown in the
Go commands, is the first deferred candidate. Stage/total durations, outcomes,
stale-result observations and durable result freshness are later candidates, not
implemented Job instruments or a new Runner API. Collector self-runtime and local
scrape `up` must not be attributed to a parent Job. Logs, durable records and
Kubernetes Job status remain current evidence; no Job SLO is promised.

Application/OSM database server exporter additions are also deferred. Client
`otelpgx` metrics are not PostgreSQL server metrics. Existing external cluster
exporters may support optional infrastructure dashboard panels, but this chart
does not add a server exporter, private SQL aggregate datasource or broader DB
permissions. Deployment collection verification is separate from all this work.

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
- Source deletion cancels queued jobs and clears their snapshots immediately;
  running lease holders clear snapshots at their safe cancellation boundary.
- Stale-source notifications use persisted condition evaluation and reminder timing. Reappearance specifically at the next signin remains planned; current signin does not run that lifecycle.

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

The following is the accepted lifecycle target, not a claim of a completed
general-worker account-purge implementation. Current runner dispatch is limited
to source checks, source-child ingest and workout deletion.

- Account deletion atomically disables authentication and prevents new jobs.
- Queued and running account jobs are cancelled before purge enumeration.
- Purge waits for terminal jobs or expired leases, clears snapshots and staging, then captures the complete private-data target scope.
- Purge cannot run concurrently with a job that may still commit account data.
- Failure leaves the account disabled and resumes from durable deletion state.

### OSM failure

- OSM bootstrap/refresh uses the separate single-flight operator Job workflow; automatic application-queue orchestration remains deferred.
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
- Source configuration is decrypted in process memory; the M8 native subprocess additionally requires one private job-scoped config file with one profile.
- Rclone configs/staged files use mode `0600` in job directories with `0700` access permissions (setgid may be inherited). The worker-owned `WORKER_STAGING_ROOT/private` child accommodates a UID-0/fsGroup-writable emptyDir outer mount. Linux `openat2` and `/proc/self/fd` pin no-symlink filesystem access.
- Staging is per pod, never shared with independent worker processes. Cleanup includes config backups, endpoint caches and partial exports; a local attempt's failed cleanup blocks its terminal writes and that worker's new claims/readiness until confirmed. The gate/scavenging is pod-local, not a distributed cross-pod or lost-volume guarantee. Bounded retries cannot repair read-only storage, permissions or hardware and do not promise physical erasure.
- Actual Alpine/non-root checks verified a UID/GID `0:65532`, mode-`2770` bind root and a worker-owned `65532:65532`, mode-`2700` private child (inherited setgid plus `0700` access). Synthetic constructor/version cleanup passed. This does not verify full worker startup, arm64 execution, read-only-root-filesystem/seccomp behavior or production deployment.
- Temporary files use private permissions and job-scoped paths.
- Rclone subprocess arguments and logs must not contain plaintext secrets.

### Database boundary

- Application API, general worker, Coverage worker, migration and tile service use separate roles. Dedicated OSM read/import credential separation remains required before production; the explicit xdev development exception is described above.
- Application and OSM databases remain separate.
- Martin connects as `workouts_tiles`, can execute only approved application tile functions, and has no private-table `SELECT` privileges.
- Source encryption key is not stored in PostgreSQL.

### Operational boundary

- Kubernetes Secrets supply credentials and encryption keys.
- TLS terminates at trusted ingress or gateway infrastructure.
- PostgreSQL volume and backup encryption are operator requirements.
- Logs, backups, and WAL have explicit retention and access controls.

## Deferred Decisions

- Automatic region addition/download orchestration, dedicated read-only OSM credential provisioning and administrator OSM status/refresh workflows; the manual importer/derivation/operator toolchain is implemented, not pending the ADR 0008 spike
- Future matcher-policy revisions beyond the accepted ADR 0009 v82 baseline
- Future Coverage bucket revisions beyond the accepted ADR 010 six-class baseline
- Retention limits for job history and owner-visible diagnostic logs
- Installation-specific public provider selection, origin restrictions, quotas and key rotation beyond the implemented configurable family catalog
- Additional adapters and provider-normalization rules
- OIDC identity linking and account migration
- Sharing grants, temporal/geofence filters, comparison authorization, and overlap rendering

## Exact Parking Continuation Addendum

The matcher follow-up first recorded at the 10/19/2026 source-only checkpoint extends
[Matching Performance And Termination](#matching-performance-and-termination).
The production change is confined to `internal/coverage/matcher.go` and was
subsequently **deployed with the folded-028 release on 10/20/2026**.
It retains `coverage-production-v1`; implementation labels V1/V2
do not denote policy versions. No SQL version or migration rewrite is involved.
At the source-only checkpoint, the active source was 001-028, supported/schema/
minimum runtime 28, marker 27, while live `dev-20261018`, Helm revision 28 still
used the old contract. Those versions are historical, not current release status.

Exact state keys use canonical unsigned varints for cursor, portion count and
ordered interned IDs. Interning compares every portion field and all float bits,
including signed zero and NaN payloads. Keys own their strings; neither portion
identity nor key equality relies on a collision-prone digest. Compact encoding
reduces payload, but cannot itself stop a growing same-cursor rewrite.

The additional certificate handles deterministic same-cursor head cycles of period
one or two with positive insertion. It requires unique opposite-direction choices
throughout the full source graph, not just the local route. Old unsorted choices,
acceptance predicates, margins and first-accepted-candidate ordering stay intact.
For a full vector `L + H + X`, the five-portion head `H` must undergo exact local
normalization to `R`, and the actual complete replacement must equal `L + R + X`.
The entire old prefix and tail are checked, with all metadata and float bits;
matching only a head or boundary anchor cannot certify growth.

The prefix and tail must already be normalized. The prefix/local boundary and
**all** replay boundaries between each phase and every carried tail/inserted block
must be sealed under the actual merge predicate, including rewind and NaN
semantics. Exact five-portion head closure plus positive insertion in at least one
phase then proves a nonterminating continuation. History is valid only across
uninterrupted old-policy execution at that cursor and clears after skips or cursor
advance. Reject only the closing candidate and continue to later ends. Failed
proof obligations, ambiguous directions and periods above two fall back to existing
certificates and work guards; this is not complete nontermination detection.

The unchanged bounds are 512 KiB active-cursor key payload, 4,096 states and
accepted replacements per traversal, 16,384 interned portions, 16,384 suffix nodes
and an 8 MiB charged cache. A single optional descriptor has a measured build size
of 2,624 bytes and a conservative 4,096-byte charge within that budget. It retains
fixed head identities/anchors, not whole growing vectors; admission may decline
or history may be evicted for mandatory cache entries. Mandatory state is not
discarded to fit. Encoder/map overhead remains additional, not certified process
memory. Context checks and complete-only atomic publication remain required.

Termination certificates preserve terminating old-policy results rather than
introducing a new route-acceptance policy. The recorded golden/oracle checks are
finite evidence, not universal equivalence within arbitrary runtime budgets.
Independent property-reference agreement tests certification, not an if-and-only-if
semantic termination oracle. Processing-limit failures remain typed diagnostic
guards, not proof of excessive route mileage. See
[the evaluation checkpoint](coverage-matcher-evaluation.md#exact-parking-repair-follow-up-10192026)
for synthetic measurements and verification scope; user-owned operational evidence
stays private. At the 10/19/2026 checkpoint, the final combined worker rerun and
protected actual-backup rehearsal remained pending; a staged private repair bundle
was not deployment authorization. Subsequent folded-028 release evidence is separate;
see [current status](development-progress.md#current-status).
