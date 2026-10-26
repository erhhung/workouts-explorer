# Application Database Migrations

The deployed migration sequence is **001-032**. Released/applied 032 includes coverage
display precomputations and targeted entity statistics. **Source-only 033** adds
canonical display identity and is **not deployed**. Live schema and minimum runtime
are **32**, retaining `migration_baseline=27`, with
contiguous Goose ledger **0-32**. Applied **001-032 are immutable**. The standalone
native migration and coordinated backend-42/UI-43 deployment are accepted.
Scripted deployed-browser verification passed; owner acceptance/milestone and
capacity remain separate.
The preceding ingest contract remains defined by 031. See
[current release status](development-progress.md#coverage-display-optimization).
The baseline release receipts below are historical, **prior to 031/032**.
Source support/schema/minimum runtime for the pending coordinated release are
**33**, baseline **27**; see [033 gates](#canonical-coverage-display-033-source-only).

## Directory Layout

Application migration runtime code, SQL sources and tests are separated:

```text
api/migrations/
    embed.go       Runtime embedded-files wrapper
    history.go     Runtime history validation
    sql/           Active numbered Goose SQL migrations
    tests/         Migration contract and integration tests
```

Physical SQL paths use `api/migrations/sql/NNN_name.sql`; migration tests run with
`go test ./api/migrations/tests` (or `go test ./api/migrations/...` to include the
runtime package and all subpackages). `make migration-test` invokes
`scripts/test-migrations.sh`, which runs the native migration command twice for
idempotence before executing the moved tests.

The runtime Go import path remains
`github.com/erhhung/workouts-explorer/api/migrations`. Its `migrations.Files`
wrapper preserves the embedded virtual filesystem's root-level SQL basenames:
`migrations.Files.ReadFile("028_ingest_coverage_pipeline.sql")` still works without
a `sql/` prefix, and Goose still reads migrations from `"."`. Moving the physical
files does not require runtime callers to change their imports or embedded read
paths. The separate OSM migration layout is unchanged.

## Canonical Baseline

The active Goose sequence is contiguous. Migrations **001-026** establish the
foundation; `027_coverage_recovery_baseline.sql` is an ordinary static migration
from schema 26 to **27**. It adds routed counters, the cause-aware retry policy,
runtime-capability compatibility, shared diagnostic limits and retry/Superseded
presentation. It has one final assignment to schema 27/minimum runtime 26 and
sets `migration_baseline = 27`.

027 contains no historical-version dispatch, intermediate schema versions,
ledger rewrite, generated legacy payloads or archive loader. Production embedding
includes active SQL from `api/migrations/sql/` only. Tests obtain isolated feature
checkpoints through semantic sections of that same SQL, not a second versioned
migration history.

The deployed Coverage contract uses **001-028**.
`028_ingest_coverage_pipeline.sql` folds together the pipeline, deferred-generation
locking fix, strict eligible-cohort construction and mixed-failure automatic retry
policy. Schema and minimum runtime are **28**, `migration_baseline` remains **27**,
and that release's `SupportedSchemaVersion=28`. Goose's version-0 sentinel is bookkeeping,
not a migration file. Internal serial row IDs need not be consecutive; migration
versions must be.

**The folded-028 contract is deployed in `dev-20261020`, Helm revision 29**, with
schema/Goose/minimum runtime 28 and marker 27. The separately approved exact
actual-profile repair and protected actual-backup rehearsal are complete.
The same numeric version does not establish contract or feature compatibility.
Ordinary static 027 and migrations 001-027 stay unchanged. Folding this
028 contract was an explicit user decision, not permission to edit applied SQL as
a routine upgrade mechanism.

## Ingest Outcomes And File Filtering

Migration 029 advanced the active sequence ordinarily to **001-029**.
`029_ingest_partial_success.sql` adds file-aware ingest-parent outcomes and
server-side failed-file pagination. Source `SupportedSchemaVersion=29` requires
this migration before new binaries start. Minimum runtime remains **28**, and
`migration_baseline` remains **27**; existing runtime-28 readers retain their
three-argument file-list ABI.

When a failed source child has successfully processed files or unchanged files
that were successfully skipped, its terminal ingest parent is
`partially_succeeded`, even if no whole source child succeeded. Durable successful
file records cover progress snapshots that lag committed imports. Failed source
children remain failed and retryable; all-cancelled runs remain cancelled.
Notifications distinguish file-processing failures from source-level failures,
without promising that a bounded retry processes only failed filenames.

The optional `failuresOnly` file-list filter applies before counts and pagination,
preserves account scope and safe file fields, and does not modify files or jobs.
Existing terminal jobs and persisted notifications are **not rewritten** by 029.
Apply it with the native migration entry point, not by editing applied 028.

## Workout-Based Ingest Outcomes

Migration 030 advanced the sequence to **001-030**. The applied 029 SQL remains
immutable; `030_ingest_workout_outcomes.sql` supersedes its file-based success
criterion. An unsuccessful ingest is partially successful only when
`workouts_created > 0 OR workouts_updated > 0`. Successful file reads, skips and
unchanged workouts alone do not qualify. Successful empty runs remain completed,
all-cancelled runs remain cancelled, and Coverage/deletion outcomes are unchanged.

The same rule projects historical ingest results in job detail, latest-job cards,
notifications and history filters before counting/pagination. A historical stored
failure with imported or updated workouts displays `partially_succeeded` without
rewriting its execution record, notification row, timestamps or retry lineage.
Source-child display follows the same rule while its stored failure stays
retryable. New terminal parents use the workout-based rule directly.

Source runtime is **30**, minimum runtime remains **28**, and baseline marker
remains **27**. Apply native migration 030 before deploying the new API.

## Explicit Ingest Reprocessing (031)

`031_ingest_reprocessing.sql` advances the active sequence to **001-031**, schema
and minimum runtime **31**, retaining `migration_baseline=27`. Source support is
31. Apply it through the native `/app/migrate` entry point; applied **001-030 stay
immutable**, with no ledger repair or replay of applied SQL.

New bounded API jobs persist explicit boolean `reprocessAllFiles`, default false;
incremental jobs cannot carry the field. Existing bounded jobs with the field
absent retain their original force policy. Do not rewrite historical parameters,
coalescing keys, queued jobs or frozen candidate metadata, or add another new/old
contract marker. The existing historical parameter compatibility is retained.

031 adds private nullable `source_files.observed_identity` and
`ownership_blocked_epoch` success receipts and guarded commit-time stamping from
the admitted ingest-write capability and frozen manifest. Existing terminal-file
and manifest immutability remain authoritative. Retry reads valid same-file,
same-metadata success on its source-child ancestor path before applying force;
siblings and unrelated branches do not qualify. New receipts remain usable even
when later jobs move the latest inventory success pointer. Legacy NULL receipts
require sufficient current inventory/checksum/manifest proof or may cause a safe
extra download. No invented backfill or universal unsupported-history skip claim
is made. Normal non-forced retries can still use ordinary valid inventory skips.

The new API v2 coalescing identity separates effective force, date/source-generation
scope and root/retry ancestry using existing sorted source-row locks and active
uniqueness, not another lock. Private receipts do not expand the public file ABI.
Schema 030's workout-based outcomes are unchanged: unsuccessful runs with zero
created/updated workouts display **Failed** even with zero failed files; source
discovery/authentication can fail without a failed file. Successful empty and
all-cancelled runs retain their existing outcomes.

Take fresh protected backups and retain matching old images. Drain/stop
incompatible API/general-worker/Coverage-worker runtimes, apply native 031, then
deploy coordinated compatible API/worker images and matching UI. Minimum runtime
31 fences older execution; do not mix old API/worker images with this contract.
Verify contiguous history, marker, role readiness, grants and queued historical
job compatibility before resuming. Rollback requires the matching database backup
and old images together; 031 is forward-only, not an image-only rollback. This
section defines rollout requirements, not a live-release receipt.
See [Ingest Reprocessing](ingest-reprocessing.md) for the full API and retry rules.

## Coverage Display Precomputations (032)

[032_coverage_display_precomputations.sql](../api/migrations/sql/032_coverage_display_precomputations.sql) is an ordinary forward migration after
031. Applied **001-032 remain immutable**; use native `/app/migrate`, with no ledger
rewrite or replay. Its final metadata assignment is schema **32** and minimum
runtime **32**; the baseline marker stays **27**. The targeted reader and indexes
shipped in this **released/applied 032**, not a 033 or a rewrite of deployed history.
Accepted private receipts verify compatible runtime support and actual API/worker
role readiness, not merely numeric schema compatibility.

The migration adds three normalized compact projections:

- `workout_path_display_partials` retains per-workout/display-entity/source-path
  qualification buckets, education identity/name partials and numeric bounds.
- `workout_path_display_context` retains regional-park display context independently
  of path-statistics exclusions.
- `workout_park_display_bounds` retains matched-evidence bounds independently of
  path qualification and live park visit attribution.

All three and the transaction-scoped `coverage_display_dirty` queue are owned by
the security owner with forced RLS. Direct access and helper execution are revoked
from public and API/worker/tile roles. The replaced list reader keeps its shipped
signature, ownership and ACLs. No year/month materializations or date-specific
response caches are added; custom ranges use the same projections.

The migration locks workouts, match evidence and copied segments in
`SHARE ROW EXCLUSIVE` mode and backfills all existing match evidence in one batch,
including currently ineligible workouts. Live joins retain visibility, local
dates, routed/applied-result eligibility and path/park/region metadata. The reader
deduplicates qualified path partials by display entity/workout before counting,
preserving range/all-time counts, dates, navigation tie-breaks and bounds.

Runtime evidence writes and relevant segment updates/deletes enqueue old/new
workout or segment-source keys plus one account key per transaction. The queue has
no source foreign keys so deletion tombstones survive until flush. The initially
deferred account callback serializes on a no-value-change generation-row update,
then expands segment keys to affected workouts **after commit-time serialization**
in a separate command. This avoids missing concurrently committed matches; stale
Repeatable Read serialization or deadlock failures propagate. One set-based rebuild
per dirty account refreshes affected workouts and drains the queue. Evidence and
projections commit atomically, with no partial applied replacement on failure and
no new generation-increment rule. Logical deletions are excluded by live joins
before physical cleanup; failed rematching retains the prior applied coverage.

### Targeted Entity Statistics

032 also adds
`app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)` for
`GET /api/map-selections/{mapSelectionId}/coverage/{entityKind}/{entityId}/stats`.
It returns the same 21 ordered metadata fields as the list/detail readers,
serialized as `RoadCoverageEntity`, with raw bounds but no geometry or expanded
fit bounds. The API uses a read-only transaction. The STABLE security-definer
function validates active account/session, selection expiry and current
generation before facts in the same statement snapshot; missing scope/entity
returns no row and the API returns 404. It never invokes the full-list/detail
functions, scans segment geometry or runs maintenance. Existing click-highlight
geometry/detail behavior is unchanged.

Each compact fact source filters kind and ID before aggregation. New nonunique
btree indexes use `(account_id, entity_id, workout_id)` on path partials and
context, and `(account_id, park_id, workout_id)` on park bounds; park attribution
already has the corresponding entity-first access path. Range/all-time
eligibility, live metadata/name qualification, education visit deduplication,
count/date extrema and earliest-start/UUID navigation ties remain unchanged.
The range includes all eligible account workouts in the selected dates, not just
the checked/focused subset.

Park bounds retain an independent canonical-text-geometry availability marker.
Stats preserve detail's requirement for at least one canonical park match from an
eligible **in-range** workout, independently of which workout supplies the park
visit attribution. Out-of-range canonical evidence alone cannot authorize stats.
Raw bounds still include every UUID-cast park tag, including noncanonical tags in
mixed evidence; availability does not shrink them to canonical geometry bounds.

The security owner owns the new function; only `workouts_api` receives runtime
EXECUTE, with no PUBLIC, general-worker, Coverage-worker or tile grant and no
direct fact-table grants. Its fixed `search_path` is `pg_catalog, app`; both list
and stats readers set `jit=off`. Runtime readiness pins exact body bytes for all
six display functions, not normalized SQL. It also checks signatures, ordered
results/defaults, language/volatility/security settings, ownership/ACLs, table
columns/constraints, forced RLS/policies, index definitions/validity and exact
maintenance-trigger events/deferral. A missing index, altered setting/body or
broadened privilege fails readiness even at schema 32.

Contract and disposable-database checks live in
[stats contract tests](../api/migrations/tests/coverage_entity_stats_test.go),
[stats parity/security tests](../api/migrations/tests/coverage_entity_stats_integration_test.go),
[projection maintenance/upgrade tests](../api/migrations/tests/coverage_display_precomputations_integration_test.go),
[exact readiness contract tests](../internal/database/coverage_display_precomputations_readiness_test.go), and
[catalog mutation tests](../internal/database/coverage_display_precomputations_readiness_integration_test.go).
Recorded verification belongs in
[Development Progress](development-progress.md#coverage-display-optimization),
with separate source, deployment and passed scripted deployed-browser evidence
boundaries; none permits replaying applied migrations.

### Release Requirements

Before applying 032, retain a protected pre-migration backup and matching images,
drain incompatible runtimes, and coordinate compatible API, general-worker and
Coverage-worker images and UI. Verify clean creation, 031-to-032 upgrade, backfill parity,
batched maintenance, concurrent segment invalidation, rollback on maintenance
failure, role isolation and unchanged date-range semantics. These are release
requirements, not test or deployment receipts. Goose Down deliberately raises
SQLSTATE `55000`: **032 is forward-only**. Rollback requires restoring the matching
pre-migration database backup and images, not an image-only downgrade.

The accepted release applied 032 through a standalone restricted native migration
Job while incompatible runtimes were paused, then rolled backend revision **42**
and UI revision **43** with full values and `--no-hooks`. Paused original-column
hash checks preserved all **82 original business tables**, prior ledger entries
and unrelated relations/roles/functions. Actual API/general-worker/Coverage-worker
readiness passed at schema/minimum **32**, marker **27**, ledger **0-32**. Full
protected pre-32 database/role backups and matching old image exports remain
retained. Scripted deployed-browser verification passed; owner acceptance/milestone
and capacity remain separate. No image-only rollback, provider check or new
ingest/matching operation is implied by this release.

## Canonical Coverage Display (033, Source Only)

[033_canonical_coverage_display.sql](../api/migrations/sql/033_canonical_coverage_display.sql)
is the next ordinary forward migration, **not applied live**. It advances schema
and minimum runtime to **33**, retaining `migration_baseline=27`; its expected
post-migration ledger is **0-33**. Applied **001-032 are immutable**, including
032's shipped stats reader. Use native `/app/migrate`, never replay/modify applied
SQL or rewrite the ledger. Live backend-42/UI-43 remains schema/minimum **32** until
explicit MAIN cutover completes.

[ADR 0013](decisions/013-canonical-coverage-display-identity.md) defines policy
`coverage-display-identity-v1`: region plus normalized actual display name/place,
not source kind/class/component or hidden jurisdiction IDs. PostgreSQL **18 UTF8**
with built-in **`pg_catalog.pg_c_utf8`** is required for pinned NFC/casefold and
explicit Unicode White_Space behavior. UUIDv8 uses the versioned, length-prefixed
UTF-8 tuple and SHA-256; stored normalized keys block inconsistent hash collisions.
Unnamed sources remain kind/UUID-distinct. This policy is general, not data-specific.

Six forced-RLS security-owner tables retain account revisions, scoped contexts,
source members, distinct canonical/workout visits, indexed entities and a
transaction invalidation queue. A nullable composite-account FK binds a selection
to its ready context. The API prepares/reuses contexts in its existing Repeatable
Read selection transaction, not during read-only hover/detail or tiles. Scope is
account/generation/revision/dates/policy/locale. GC retains eight reusable
unreferenced scopes plus valid actively pinned contexts; obsolete or older-than-
one-day unreferenced scopes are removed. Build/rollback and account deletion must
be safe without exposing incomplete contexts.

All historically eligible sources participate in context-resolved membership,
including historical-only sources using fallback context/region. Distinct-workout
union precedes both range/all-time summaries; list/stats require an in-range visit.
Mixed named park/path groups share canonical IDs and counts, with park-preferred
presentation only. List, indexed point stats, stored whole-group detail, focus
and all four tile layers share that context and preserve actual source geometry
and provenance. Selection JSON requires display policy/revision; compatible UI
caches/guards must use them.

033 retains compact-fact maintenance and post-barrier segment expansion. All
reader inputs, including metadata, dates, deletion, route/applied eligibility and
region labels, queue deferred invalidation. Revision and public generation advance
once per account/transaction, not per row; no early ingest/provider generation
lock or eager rebuild of every date context. Verify both READ COMMITTED commit
orders, stale Repeatable Read `40001`, savepoints, rollback and deleted-account
queue cleanup. Runtime readiness checks exact 033 bodies and
ownership/ACL/RLS/index/trigger contracts, not numeric version alone.

Migration seeds revisions and advances public generation once per account to
invalidate old source-ID capabilities/caches. Original evidence, GPS, manifests,
jobs, matching states/rules and generation vectors remain unchanged. **No OSM
derivation/matcher/path-policy bump, OSM regeneration/download or route rematching
is needed.** Coverage recomputation means canonical context warm/rebuild from
existing evidence, not new route jobs.

Before MAIN cutover: complete source verification; rehearse native 032-to-033
migration, role readiness, preservation and full rollback on a protected restore
of an actual backup using the actual migration role; retain fresh backups and
matching old images. Stop/drain incompatible API/general-worker/Coverage-worker
runtimes and coordinate compatible images/UI, including tile SQL consumers.
Preserve migration/TLS/OSM replica/Martin flags and identity, source auto-sync,
admin-password debug, resources, concurrency, timeouts and retry budgets. Keep
all dumps/archives under `~/nfs-share/.temp/`, linked from the matching private
`deploy/internal` release bundle; monitor workspace, `/tmp`, NFS and application-PG
filesystems and inodes regularly. No live mutation is authorized by this document.
033 Down raises `55000`: rollback needs the matching database restore **and** old
images, never image-only downgrade. Full 033 verification, rehearsal and deployment
receipts are separate gates in [Development Progress](development-progress.md#canonical-display-identity-033-source-only).

## Native Upgrade Entry Point

Use the API image's **`/app/migrate`** command, or its source equivalent
`go run ./api/cmd/migrate`. API and worker processes never migrate on startup.
The command holds its PostgreSQL session advisory lock, runs the read-only
`migrations.ValidateHistory` check, then invokes ordinary Goose Up.

Validation requires a consistent metadata row and exact applied ledger prefix.
Canonical schema 27 and later require marker 27. Empty databases and supported
foundation prefixes may advance normally. Gapped, duplicate, unapplied, unknown
future or metadata-inconsistent histories fail before changes. Validation does
not delete, stamp, rename or otherwise repair ledger rows, and it does not enable
`AllowMissing`.

An already-applied canonical 027 is not replayed: canonical 27 applies new 028.
Fresh creation executes the same active sequence. Each migration's DDL/data changes
and Goose version recording are transactional; failure rolls back that migration.
An existing old-contract schema 28 is a special case: **native Goose does not
replay 028**, so Goose Up alone cannot install the new contract. Forward-only
migrations are not a substitute for a backup restore during rollback.

## Deferred Account Generation (Folded Into 028)

The folded lock fix changes timing, not invalidation policy or retry allowance:

- Four existing, same-name generation triggers become constraint triggers with
  `DEFERRABLE INITIALLY DEFERRED`: workout headers and route summaries retain
  their INSERT/UPDATE/DELETE events; workout types retain UPDATE OF `type_key`
  and `provider_label`; Coverage retains UPDATE OF `processing_finished_at` with the
  existing WHEN predicate requiring `current` and a changed finish timestamp.
- Account generation seeding stays immediate. The counter advances once per
  qualifying row event, not per GPS point or once per transaction. No invalidation
  is skipped, and Coverage queued/running/failed/deleted states do not gain new
  invalidation coverage beyond migration 017's current-finish scope.
- Deferred events normally run at commit; `SET CONSTRAINTS ... IMMEDIATE` can
  flush them earlier. Production does not issue that early flush. Commit tails
  still serialize on the account counter; deferral is not a capacity guarantee.
- `app.fence_ingest_job` loses only the generation-row `FOR UPDATE NOWAIT`.
  Its nonlocking existence check and missing-generation SQLSTATE `55000` remain,
  as do identity advisory locks, source/job locks, lease, ownership epoch,
  runtime-capability checks and account/workout KEY SHARE fencing.
- The rewrite matches exactly one structural locking statement, tolerating case,
  whitespace and comment differences. Zero or duplicate anchors fail closed and
  roll back the migration, rather than accepting an unrelated statement.
- `app.lock_account_data_generation()` keeps its historic ABI but becomes a
  nonlocking MVCC read. The API reads generation and selected data from the same
  Repeatable Read snapshot. A concurrent commit can make the response already
  stale; existing current-generation validation rejects that capability rather
  than pairing old selected data with a newer generation. Account/workout FKs,
  including deferred focused membership, stay enforced without a generation-first
  lock cycle.
- Data and generation commit or roll back atomically. An injected deferred-trigger
  failure leaves no partial file/checkpoint, Coverage evidence, state or progress.
  There is no new worker transaction retry or deadline allowance.

New runtime-28 readiness checks require the private cohort-builder contract,
exact deferred triggers/scopes, nonlocking fence/helper, function security and
grants. The old ten-argument `apply_ingest_coverage_batch` and six-argument
`defer_ingest_coverage_batch` ABIs are intentionally absent. **Old runtime-28
readiness must fail against new 028**; numeric minimum runtime 28 is not a
behavioral compatibility fence. There is no rolling reuse of old runtime-28
API/ingest/Coverage images. Historic map-selection helper ABI preservation does
not imply whole-runtime compatibility.

## Eligible Cohorts And Retry Policy

During active ingest, a new normal parent seals with exactly saved **B** newly
eligible matching targets, default 25, across regions and source siblings of the
ingest root. Missing-map, already-current/live, unchanged authoritative failed or
cancelled targets and deduplicated obligations do not count. Private prepared
metadata persists across files/crashes, with root/sibling scope and saved B; it
does not create a placeholder Coverage parent. Full parent and children seal
atomically. A final partial cohort is permitted only after the visible ingest root
and every source sibling are terminal and due preparation is exhausted, through
leased account-reconciliation recovery, not active source EOF.

Mixed-region parents are explicitly allowed. Optional `coverage.regionIds` is the
actual child-region union, including retries; `regionId`/`targetGeneration` remain
representative anchors, never complete matching scope. Per-route generation
vectors, claim fencing and admission are unchanged. Preparation uses bounded
metadata and one route's coordinates at a time. A trusted fingerprint of **all**
catalog regions/configuration/boundaries/active heads must agree in the same
Repeatable Read ROI lookup snapshot and before seal. Mismatch invalidates prepared
readiness and requires fresh ROI, never a substituted newer generation vector.
This is not a cross-database latest-state atomicity claim.

After cooldown and authority validation, at least one timeout/verified-transient
eligible failed child permits an automatic successor carrying **all authoritative
failed siblings**, including permanent, unknown and null-code failures, with valid
descriptors. Successful siblings never rerun; cancelled siblings are not revived.
All-permanent/unknown failed parents stop automatically. The default one additional
automatic attempt (0-20) is shared durably per root; manual retry does not reset it.
Latest-only authority, retry isolation and unchanged-failed-target suppression
remain enforced. See [Job State Machine](job-state-machine.md#committed-route-pipeline-requires-28).

## Existing Old-Contract 28 Repair

An old-contract schema 28 has the same numeric version as the folded-028 release,
but an incompatible contract. It requires a **new private, guarded offline repair**,
not a public compatibility migration, archived active fixture or ledger rewrite.
Execution requires explicit operator approval and a fresh protected backup.
Before application, rehearse repair and rollback on a protected restore of an
**actual application backup**, using the **actual migration role**, released source
and catalog-fingerprint proof. Synthetic archived-old-28 checks are not that
protected actual-backup rehearsal. For the current release, the separately approved
exact actual-profile rehearsal and live repair both passed; the following
preservation/rollback requirements still apply to any separately authorized repair.

Pause **all old API, ingest and Coverage runtimes** before repair and replacement;
pause tiles too if DDL affects their application constraints. Verify preservation
and actual new-role readiness before resuming only compatible new images. Preserve
Goose ledger **0-28**, including IDs and timestamps, without ledger DML/rewrite.
Do not reset data, matcher roots, retry budgets, sources or credentials. Retain old
artifacts and backups. Rollback to incompatible old images requires restoring
the **old database contract and images**, not an image-only rollback. Private
operator evidence owns SQL, guards, paths and receipts; public docs define only
these requirements.

## Rollout And Recovery

- Test clean creation, canonical upgrades, native CLI idempotence, invalid-history
  rejection, role boundaries and transaction rollback.
- Rehearse against a protected restored application backup using the actual
  migration role. Verify raw data, job history, retry budgets, ownership and ACLs.
- Retain a fresh backup and old images before maintenance. Preserve resource,
  credential and source settings unless the release explicitly changes them.
- Drain and stop incompatible runtimes even when numeric minimum runtime is
  unchanged. Old runtime-27 and old-contract runtime-28 images are not rollback
  targets for new 028 without restoring their database contract first.
- Verify metadata, marker, contiguous ledger, role readiness and public/UI health
  before ending maintenance. Do not create real imports merely to test deployment.
- The folded-028 repair did not add a migration or public old-contract archive.
  New feature migrations, including 029, are ordinary forward upgrades, not repair
  shortcuts. Chart semantic changes remain frozen
  and have not started; documentation is not rollout authorization.

Backups with noncanonical bookkeeping require a separately reviewed operator
recovery procedure before the ordinary native command. That preparation and its
private audit evidence are not executable compatibility history in 027. Never
silently rewrite a restored ledger or drop application data to make it pass.
Dataset-specific receipts, identifiers, timings and backup locations belong in
private operational records, not this design contract.

## Actual-Profile Repair Receipt (10/20/2026)

The approved repair pins the exact actual-old function catalogue
`378818f0f03a0436c4dcdd3b9c7bc01b258127a2862d60fa14a077caf2b65779`
and canonical-new post-catalogue
`0452c00d2b2ae111dfcd59eb89075c9c62b4711464886a602d8a1499aa54a6c4`.
Offline review qualified 104 formatting-different definitions with all 1,322
literal occurrences byte-equal. The approved delta additionally reinstalls 103
otherwise unchanged definitions from fixed canonical-new text, not inherited
body rewriting. This is a frozen-corpus qualification, **not a generic SQL lexer
or universal semantic-equivalence claim**, and no normalized hash is admitted.

All 198 retained function OIDs survived. All seven actual historical table
differences remain: the backup relation's `postgres` owner/ACL, two extra park
checks, and five differently grouped AND CHECK catalogues. Original constraints
are retained verbatim, not simplified or replaced by a canonical table catalogue.

The protected actual-backup rehearsal and live repair used the real authenticated
non-superuser migration login with its existing security-owner SET capability.
Explicit operator/profile approvals, exact preconditions and one SQL transaction
guarded application. Full before/after original-tuple hashes across **82 relations
(81 app plus ledger)** preserved every old field without exclusions, including
exact ledger 0-28 IDs/timestamps. The separate post-startup reconciliation
timestamp exception is not an exclusion from this repair proof.

Native migration runs confirm **applied 028, no replay**. New runtime structural
readiness passes and old same-numeric-28 readiness fails. Current images must not
be replaced by old runtime-28 images against this repaired database. Protected
local exports retain the final whole-database backup, password-free roles and old
OCI images after authorized old-artifact registry deletion; rollback requires
restoring the **whole old database and matching old images** together. These local
exports are not an off-machine backup. Exact execution/approval receipts and
recovery locations remain in private operator records.
