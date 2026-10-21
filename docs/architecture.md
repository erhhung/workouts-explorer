# Architecture

## Context

Workouts Explorer is a small, self-hosted, multi-account application for importing workout records and GPS tracks, displaying workout summaries, and calculating visited path coverage from public map data.

The expected deployment has up to 10 accounts and 10 concurrent users, but each account may contain several years of route points and thousands of workouts. Ingest, path matching, OSM maintenance, and tile generation must not make the interactive API unresponsive.

The system handles sensitive health and location data. Account isolation, credential protection, authenticated private map tiles, safe diagnostics, and deletion behavior are first-class architectural constraints.

This document describes implemented structure unless a feature is explicitly
marked planned, optional or deferred. Capacity targets are design requirements,
not proof of multi-user load acceptance. Dated operational measurements belong in
the focused acceptance and telemetry guides rather than this component overview.

The provider-identity/source-adoption foundation requires minimum runtime 26
(migrations 024-026). The approved active sequence remains **001-028**, with **no
active 029**. Ordinary static 027 advances 26 to 27 without a legacy dispatcher or
archive replay; 001-027 remain unchanged. New 028 folds deferred-generation locking,
strict eligible cohorts and mixed-failure automatic retry into the pipeline.
Source `SupportedSchemaVersion`, schema and minimum runtime remain **28**, marker
**27**. The folded contract, exact parking repair and UI follow-up are **deployed
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
[Development Progress](development-progress.md) owns operational evidence; this
contract is not owner iCloud acceptance or final yearly recovery closure.

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
| Rclone/iCloud Drive | M8 native 1.75.2 job-scoped transport implemented and deployed; owner ingest/recovery verified, milestone acceptance separate; local/NFS remains the fallback |
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
| `source_files` | Safe discovery identity, source-relative name, size, modification metadata, checksum, processing state |
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

The unique workout/physical-segment constraint collapses repeated passes and GPS-jitter spans into one segment match. Persistence dissolves overlapping or contiguous spans regardless of direction, computes length from the dissolved geometry, and stores disjoint visited spans as separate components of one `MultiLineString`; it never fills gaps between them. The unique workout/logical-path constraint ensures matching multiple member segments and outbound/inbound traversal count once. Segment evidence therefore retains only clipped, positive-length geometry actually traversed by selected workouts. Every emitted span receives the containing logical path's count and fixed bucket; unvisited spans and members are not emitted.

Public graph derivation batches source ways to bound PostgreSQL temporary and shared-memory use. Physical segments split at shared OSM nodes and locality boundaries. Locality assignment requires geometry coverage within 1 cm; ambiguous complex pieces retain public geometry without a false locality attribution. Derivation v20 preserves directional labels in named identity groups: East, West and plain road names remain separate even when search normalization strips a leading cardinal direction. A road component of at most 25 m may join exactly one neighboring component with the same base name and locality; ambiguous fragments remain separate. See [ADR 0008](decisions/008-osm-import-and-segment-identity.md) for the versioned identity rules.

Daily rollups support named periods and arbitrary explicit ranges without maintaining separate mutable materializations for each shortcut.

Selections containing all workouts in a date range use daily rollups. Arbitrary workout subsets query the underlying unique workout/path attribution joined to the selected workout IDs, because an account/day rollup cannot preserve subset membership. Tile functions choose the rollup or attribution path from the authorized map selection and join selected segment-match evidence to avoid rendering unvisited portions of a logical path. First/latest visit fields always come from all-time date-only extrema.

#### Deferred Generation (Folded 028, Undeployed)

The account generation is an invalidation counter, not a long-file mutex. In new
028, the four existing header, route-summary, type-update and Coverage-current
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

The old pipeline is live in `dev-20261018`, Helm revision 28, schema/minimum 28,
marker 27. The following **new folded-028 contract is undeployed**. Historical
deployment verification does not attest this behavior; see
[Development Progress](development-progress.md).
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
a time with one efficient route query, not per-point queries. A trusted fingerprint
of **all catalog regions, configuration, boundaries and active heads** must match
in the same Repeatable Read snapshot as ROI lookup and again before seal. A
mismatch invalidates prepared readiness and requires fresh ROI; changing only the
saved generation vector cannot bless an old result. Separate APP/OSM transactions
do not establish cross-database latest-state atomicity.
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

New folded-028 automation requires at least one authoritative timeout/verified-
transient failed child, then carries **all authoritative failed siblings** with
valid descriptors into the latest-only chained successor, including permanent,
unknown and null-code failures. Successes never rerun and cancellations are not
automatically revived. All-permanent/unknown parents stop. The default one
additional attempt (range 0-20) is durable per root across descendants/restarts;
manual retry never resets it, and normal failed-target scans cannot create fresh
roots to bypass it. This new mixed policy is undeployed, not a reinterpretation
of historical retry receipts.

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

OSM promotion emits an immutable monotonic public event in the same transaction
that activates a generation. Each application database observes only the event
head and matcher-contract version, raises its own reconciliation revision, and
seeds every active account without exposing account enumeration to runtime code.
Coverage workers lease one account at a time, keyset-page routed workouts, resolve
the current region/generation vector, and enqueue only stale targets. Cursors
advance after each route; expired leases resume at the last committed cursor.
Event-driven scans are supplemented by a daily full-account scan so provider
boundary changes or retained-event gaps cannot strand historical routes.

Migration 022 includes routed workouts lacking coverage state in reconciliation
instead of silently completing a scan that excludes them. A read-only revision-0/
NULL-digest sentinel lets unchanged Go code compute the canonical SHA-256; fenced
SQL verifies current points before initializing revision 1. NOWAIT lower-level
locks and pre/post-write clock-based lease checks protect concurrent ingest.
Both point readers return at most 50,001 points so Go rejects overflow beyond
50,000 rather than hashing or matching a truncated prefix. Campaign wakeup keeps
live leases/cursors intact, and completion of an older revision does not delay
newer desired work. See the [legacy-route repair runbook](coverage-legacy-route-repair.md)
for the narrowly scoped schema-20 compatibility procedure and selective retry.

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

The normal observation window contains 128 points. Only typed graph-node/edge
exhaustion invokes bounded retries at the same start with 64, then 32 points.
The next accepted window starts at the preceding accepted end minus one, and
normal size 128 is tried again. Record starts/ordinals and observations/evidence
only for accepted windows. Supplemental turn/tangent rematches also use bounded
fallback while retaining their continuity and supporting-traversal checks.

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
TLS/authentication or mTLS; reader activation/rollout remain pending, and scoped
fixture passes do not establish concurrent capacity.

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

### Discovery and staging

1. The adapter enumerates metadata matching incremental or explicit-range rules.
2. Incremental sync selects new or changed objects and ownership-blocked objects whose captured epoch is stale (requires 26).
3. Bounded Manual Sync selects all matching objects, even previously processed ones.
4. The M8 remote transport stages at most one file per slot in a random job directory with `0700` access under `WORKER_STAGING_ROOT/private`, with mode-0600 files; the outer root defaults to `/var/lib/workouts/staging`. The old `/tmp/<user-id>/<job-id>/` layout is not the contract.
5. Remote downloads use a non-final filename and are atomically promoted after bounded download and pre/post metadata validation.
6. Local/NFS adapters may read directly when staging is unnecessary.
7. File processing writes transactional workout results and source-file state.
8. Cleanup removes the staged file before the slot claims another.

iCloud incremental discovery uses remote ID, size and modification time, not a
remote content hash. A same-metadata content overwrite is undetectable; bounded
Manual Sync forces matching dates to reprocess. One application discovery per
sync does not imply one native listing or HTTP request: backend stat/download
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
auto-sync or a new manual job is needed. Bounded Manual Sync already forces
eligible files to reprocess. Successful bytes are not invalidated or deleted to
implement ownership retry.

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
- Persist raw map geometry as a multi-line route, beginning a new segment when a positive timestamp delta reaches three times the running positive-delta average for the current segment; duplicate and backward timestamps remain ordered but do not affect that baseline.
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
3. API supplies approved selection and account context to internal Martin.
4. Martin invokes the approved security-definer tile function, which revalidates scope before querying copied segment geometry and private attribution.
5. API returns the tile with private caching headers.

Tile URLs include an account data generation so a redraw after ingest or deletion does not reuse stale private data.

With new folded 028, `app.lock_account_data_generation()` retains its historic name and
ABI but performs a nonlocking MVCC read in the API's existing Repeatable Read
transaction. Selected data and generation must come from that **same snapshot**.
A concurrent writer can commit before selection creation returns, making the new
capability already stale. Existing current-generation validation rejects it;
the API must not bless old selected data with a newly read generation. Removing
the generation-first lock eliminates its account/workout FK cycle, including the
deferred focused-membership FK, without weakening those constraints. This helper's
ABI does not make old runtime-28 images compatible with the new cohort contract;
the new behavior remains undeployed.

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
- Map routes derive small green direction triangles from cached segmented raw routes. In Routes mode they follow whichever visible route is magenta, preferring the delayed-hover route over the focused route; in Coverage they follow the focused diagnostic raw route. Marker spacing is measured in projected screen pixels. Arrows hide immediately during camera movement, regenerate for the final viewport, and reappear only after the current GeoJSON worker update and source tiles are ready. Superseded route/camera/style updates cannot reveal stale arrows; route lines and start/finish markers do not wait for this handoff. Each bearing is averaged from three GPS points before and after the marker so loops remain readable without depending on physical-distance spacing.
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

Milestone 8 source implementation is deployed and owner ingest/available-map
Coverage recovery are verified; explicit milestone acceptance remains pending.
The [initial verification](implementation-plan.md#verification-results-10122026)
retains its synthetic/disposable scope. The implemented schema-28 pipeline is
live in `dev-20261018`, Helm revision 28, schema/minimum runtime 28, marker 27.
See [iCloud setup](icloud-source-setup.md)
for private owner setup and [Development Progress](development-progress.md) for
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

The 10/19/2026 source-only matcher follow-up extends
[Matching Performance And Termination](#matching-performance-and-termination).
The new production change is confined to `internal/coverage/matcher.go` and is
**undeployed**. It retains `coverage-production-v1`; implementation labels V1/V2
do not denote policy versions. No SQL version or migration rewrite is involved.
Active source remains 001-028, supported/schema/minimum runtime 28, marker 27.
Live remains `dev-20261018`, Helm revision 28, on the old contract; new folded-028
locking/cohort/retry behavior remains undeployed as described in Context.

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
stays private. The final combined worker rerun and protected actual-backup rehearsal
remain pending; a staged private repair bundle is not deployment authorization.
