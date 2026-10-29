# Application Database Migrations

The current **source** migration sequence is **001-030**. Final source/runtime
`SupportedSchemaVersion=30`, schema **30** and minimum runtime **30** retain
`migration_baseline=27` and a contiguous Goose ledger **0-30**. Migrations
**001-028 are unchanged**. The approved source contract is now installed at Helm
revision **61**: APP schema/minimum runtime **30/30**, baseline **27**, ledger
**0-30**, retaining original ledger rows and Goose row-ID high-water **54**.
Separate OSM schema/minimum runtime **10/10** is verified on the primary and two
standbys. Current Helm revision **62** retains those schema/history contracts and
corrects observer campaign allocation. Deployment, initial proof validation and
bounded live unchanged-cycle acceptance are verified.

## Source History Mapping

Old numbers in public release reports are **pre-squash historical numbering**;
use this mapping rather than treating them as current migrations or fresh passes.

| Current source migration | Folded pre-squash historical numbering | Contract |
|---|---|---|
| `001`-`028` | `001`-`028` | Unchanged foundation and committed-route pipeline |
| [029_ingest_status_reporting.sql](../api/migrations/sql/029_ingest_status_reporting.sql) | `029_ingest_partial_success.sql` + `030_ingest_workout_outcomes.sql` + `031_ingest_reprocessing.sql` + `034_ingest_phase_progress.sql` | Final workout-based outcomes, file filtering, explicit reprocessing and durable activity phases |
| [030_coverage_display.sql](../api/migrations/sql/030_coverage_display.sql) | `032_coverage_display_precomputations.sql` + `033_canonical_coverage_display.sql` | Compact display facts, targeted stats and canonical display identity |

Both display sections below belong to **030**, with distinct precomputation and
canonical-identity anchors. Ingest outcomes, reprocessing and phase progress all
belong to **029**. There is no ongoing source schema/runtime 31-34 compatibility
contract. Private historical receipts, Git/image history and earlier test results
remain immutable; the squash changes numbering, not approved functionality.

The deployed [metadata-driven reconciliation](#metadata-driven-coverage-reconciliation-029-and-osm-010)
is an additional source-029 feature, not functionality claimed by those historical
receipts or merely by renumbering. Initial proof validation and bounded live
unchanged-cycle verification are complete.

## Live Cutover (Revision 61)

The separate operator-recorded APP cutover completed at Helm **61**, with native
API/shared-worker/OSM images under the original tag. UI/Martin stayed unchanged;
all **18 configuration specifications** were preserved. APP schema/minimum runtime
is **30/30**, baseline **27**, ledger **0-30**; original retained ledger rows and
Goose row-ID high-water **54** were verified. Native OSM 010 Up completed on actual
PG17 and schema/minimum **10/10** was verified on primary and two standbys. This
was not an ordinary Goose downgrade or a new runtime compatibility branch.

Private checks before prior writers restarted verified recorded context/region
projections, roles, sequence states and old-table counters, including zero
forbidden APP old-table DML in the owned transaction. This bounded preservation
proof does not establish a full raw-data backup/hash or universal absence of data
loss. No manual application jobs or provider operations were started. Fallback
image pins were retained at deployment, not certified as globally retrievable
now: older registry artifact deletions require fresh reachability checks before
relying on rollback. Matching database/image recovery remains required.

The selected genuine PostgreSQL matrix and full `go test ./...` passed; isolated
tests used PG18.6. Actual PG17 OSM native Up/standby verification is not a full PG17
test matrix or completion of every historical race. See
[revision-61 verification](development-progress.md#metadata-driven-coverage-reconciliation).
Revision **62** replaces only the catalog observer and makes a one-time metadata
update for accounts missing initial selection proof. Genuine campaigns allocate
above all global/account counters, preventing a legacy account's completed
revision from masking new work. No schema, table or Goose-history change occurs;
healthy accounts and unrelated application data remain unchanged. Initial proof
validation subsequently completed. Three fresh production reconciliation cycles
returned no work, with no GPS/ROI, new jobs or changed rows; this bounded idle
acceptance is separate from readiness and does not certify map-scaling capacity.
See the [repeat procedure](decisions/014-metadata-driven-coverage-reconciliation.md#repeatable-unchanged-cycle-verification).
This documentation closeout performs no cluster, deployment or database work.

## Pre-Cutover Live Status

**Historical checkpoint, superseded by [revision 61](#live-cutover-revision-61).**
The pre-cutover deployment was Helm revision **60**, schema **34**, minimum runtime
**33**, baseline **27**, ledger **0-34**, in **pre-squash historical numbering**
([mapping](#source-history-mapping)). Only API, shared worker and UI pins changed
at that checkpoint; OSM and Martin were unchanged. This is not deployed schema 30.
See [current release status](development-progress.md#current-status) and the
[029 phase follow-up](development-progress.md#ingest-phase-progress-follow-up-029).
[Milestone 8 closeout](development-progress.md#milestone-8-closeout) retains its
revision-57 browser **PASS**; it is not reopened or a capacity certification.

That higher-numbered catalog/ledger required a separate approved operator cutover,
not ordinary Goose Up, replay of applied SQL or improvised ledger edits. Its
34/60 receipt and the revision-57 M8 receipt remain mapped history, not relabeled
source-30 passes. Current preservation and verification scope is recorded above.

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

The historical folded-028 Coverage release used **001-028**.
`028_ingest_coverage_pipeline.sql` folds together the pipeline, deferred-generation
locking fix, strict eligible-cohort construction and mixed-failure automatic retry
policy. At that checkpoint schema and minimum runtime were **28**,
`migration_baseline` remained **27**, and `SupportedSchemaVersion=28`.
Goose's version-0 sentinel is bookkeeping,
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

`029_ingest_status_reporting.sql` combines ingest status reporting, file filtering,
workout-based outcomes, explicit reprocessing and phase progress. The complete
source sequence finishes at schema/minimum runtime **30/30**, baseline **27**;
intermediate sections are not separate supported runtime contracts.

The final partial-success criterion is imported or updated workouts, not successful
file reads or skips. Durable records cover progress snapshots that lag committed
imports. Failed source children remain failed and retryable; all-cancelled runs
remain cancelled.
Notifications distinguish file-processing failures from source-level failures,
without promising that a bounded retry processes only failed filenames.

The optional `failuresOnly` file-list filter applies before counts and pagination,
preserves account scope and safe file fields, and does not modify files or jobs.
Existing terminal jobs and persisted notifications are **not rewritten** by 029.
Apply it with the native migration entry point, not by editing applied 028.

## Workout-Based Ingest Outcomes

Migration **029** contains the final workout-based success criterion, with no
intermediate file-based contract. An unsuccessful ingest is partially successful
only when
`workouts_created > 0 OR workouts_updated > 0`. Successful file reads, skips and
unchanged workouts alone do not qualify. Successful empty runs remain completed,
all-cancelled runs remain cancelled, and Coverage/deletion outcomes are unchanged.

The same rule projects historical ingest results in job detail, latest-job cards,
notifications and history filters before counting/pagination. A historical stored
failure with imported or updated workouts displays `partially_succeeded` without
rewriting its execution record, notification row, timestamps or retry lineage.
Source-child display follows the same rule while its stored failure stays
retryable. New terminal parents use the workout-based rule directly.

Final source runtime and minimum runtime are **30**, and baseline marker remains
**27**. Use the complete source sequence before deploying compatible binaries.

## Explicit Ingest Reprocessing (029)

`029_ingest_status_reporting.sql` includes explicit ingest reprocessing, retaining
`migration_baseline=27`. Final source support/schema/minimum runtime are **30**.
Use native `/app/migrate` for compatible source histories; the separately approved
live reconciliation is not ordinary Goose Up or permission to replay applied SQL.

New bounded API jobs persist explicit boolean `reprocessAllFiles`, default false;
incremental jobs cannot carry the field. Existing bounded jobs with the field
absent retain their original force policy. Do not rewrite historical parameters,
coalescing keys, queued jobs or frozen candidate metadata, or add another new/old
contract marker. The existing historical parameter compatibility is retained.

029 adds private nullable `source_files.observed_identity` and
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
029's workout-based outcomes are unchanged: unsuccessful runs with zero
created/updated workouts display **Failed** even with zero failed files; source
discovery/authentication can fail without a failed file. Successful empty and
all-cancelled runs retain their existing outcomes.

Take fresh protected backups and retain matching old images. Drain/stop
incompatible API/general-worker/Coverage-worker runtimes, apply the full source sequence, then
deploy coordinated compatible API/worker images and matching UI. Minimum runtime
30 fences older execution; do not mix old API/worker images with this contract.
Verify contiguous history, marker, role readiness, grants and queued historical
job compatibility before resuming. Rollback requires the matching database backup
and old images together; 029 is forward-only, not an image-only rollback. This
section defines rollout requirements, not a live-release receipt.
See [Ingest Reprocessing](ingest-reprocessing.md) for the full API and retry rules.

## Metadata-Driven Coverage Reconciliation (029 And OSM 010)

**Deployed at Helm 62; initialization and live unchanged-cycle acceptance PASS.**
[ADR 0014](decisions/014-metadata-driven-coverage-reconciliation.md) documents why
unchanged routes must not require full-history GPS/hash/ROI scans or redundant jobs.
The feature is the `BEGIN PRECISE NEAR-IDLE COVERAGE RECONCILIATION` section of
[029_ingest_status_reporting.sql](../api/migrations/sql/029_ingest_status_reporting.sql),
before its metadata assignment/Down section. Complete APP source/runtime stays
**30**, schema/minimum runtime **30/30**, baseline **27**, ledger **0-30**;
001-028 and combined 030 are not changed by this feature. Section placement is
not a separately supported intermediate runtime.

029 adds owner-only forced-RLS `app.coverage_route_changes`,
`app.coverage_route_selection_proofs`, `app.coverage_applied_match_proofs` and
`app.coverage_reconciliation_wakes`; accepted catalog fields on
`app.coverage_reconciliation_state`; queue/scan controls on
`app.coverage_reconciliation_accounts`; and the immutable complete matching
descriptor on `app.coverage_job_contexts`. Transition hooks capture point changes
including old/new UPDATE keys with equal count/bounds; terminal/handoff hooks wake
exact accounts without an account-generation lock in file transactions.

The callable contract includes fenced `app.read_coverage_catalog_observation`,
`app.read_coverage_selection_metadata`, `app.observe_coverage_catalog`,
`app.claim_coverage_reconciliation_work` (with `scan_required`),
`app.apply_coverage_selection`, `app.ingest_coverage_route_needs_proof` and
`app.coverage_production_descriptor_current`. Runtime access is explicit helper
execution only, not direct private-table access. Readiness composes table/RLS/ACL,
function signatures/attributes/native body fingerprints, dirty indexes,
mutation/terminal hooks and baseline patch fences into full APP readiness.

Historical applied-proof initialization requires exact retained successful
immutable shipped-V1 job history, including minimum traversal and the actual
generation/input target. Cleared pointers require a matching successful finish
marker; conflicting descriptors are rejected. **No historical negative selection
baseline is inferred.** Unknown selection proof has a bounded one-time bootstrap
cost; valid pending/unavailable proof is then reusable. Ordinary identical
observations do no DML, and 24-hour age no longer triggers a scan. Queue-only
recovery preserves campaign cursor/completion and existing retry/cancel authority.

OSM uses a **separate**
[010_route_region_catalog_state.sql](../internal/osm/migrations/010_route_region_catalog_state.sql),
raising OSM schema/minimum runtime to **10/10**. It adds generated region boundary
SHA/area and `osm_catalog.route_region_catalog_state` with transactional revision,
legacy-compatible full fingerprint, selection fingerprint, active heads and
configured metadata. Catalog-write triggers suppress equal derived head UPDATEs;
unchanged catalog upserts are also suppressed. Selection covers all catalog
IDs/configuration/boundaries/configured-active presence, excluding replacement
generation numbers. This permits proof-based generation rebinding without GPS,
not unfenced substitution of heads or per-point area/hash computation.

OSM 010 and APP source-30 readers/runtimes were coordinated at revision 61;
metadata/event floors must be observed in one replica Repeatable Read snapshot with no strict-policy
primary fallback. The selected genuine PostgreSQL matrix and deployed readiness
passed within the [revision-61 scope](#live-cutover-revision-61), not as a claim
that all historical races passed. APP is **30/minimum 30**, OSM **10/minimum 10**.
The prior higher-numbered APP history was reconciled by the separately recorded
operator cutover, not ordinary Goose downgrade or runtime compatibility branches.
These forward-only contracts do not promise image-only rollback or current global
registry retention. The [delivery record](development-progress.md#metadata-driven-coverage-reconciliation)
includes completed proof initialization and measured unchanged-cycle acceptance;
map-scaling and capacity acceptance remain separate.

## Coverage Display Precomputations (030)

[030_coverage_display.sql](../api/migrations/sql/030_coverage_display.sql) follows
029 and combines compact projections, targeted statistics and canonical identity.
Its final metadata assignment is schema **30** and minimum runtime **30**; the
baseline marker stays **27**. Use native `/app/migrate` for compatible histories;
the [completed operator cutover](#live-cutover-revision-61) was separate.
Actual API/worker role readiness must establish the final combined contract,
not merely numeric schema compatibility or historical receipts.

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

030 also adds
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
broadened privilege fails readiness even at schema 30.

Contract and disposable-database checks live in
[stats contract tests](../api/migrations/tests/coverage_entity_stats_test.go),
[stats parity/security tests](../api/migrations/tests/coverage_entity_stats_integration_test.go),
[projection maintenance/upgrade tests](../api/migrations/tests/coverage_display_precomputations_integration_test.go),
[exact readiness contract tests](../internal/database/coverage_display_precomputations_readiness_test.go), and
[catalog mutation tests](../internal/database/coverage_display_precomputations_readiness_integration_test.go).
Verification using pre-squash historical numbering ([mapping](#source-history-mapping)) belongs in
[Development Progress](development-progress.md#coverage-display-optimization),
with separate source, deployment and passed scripted deployed-browser evidence
boundaries; none permits replaying applied migrations.

### Release Requirements

Before applying 030, retain a protected pre-migration backup and matching images,
drain incompatible runtimes, and coordinate compatible API, general-worker and
Coverage-worker images and UI. Verify clean creation, 028-to-030 upgrade, backfill parity,
batched maintenance, concurrent segment invalidation, rollback on maintenance
failure, role isolation and unchanged date-range semantics. These are release
requirements, not test or deployment receipts. Goose Down deliberately raises
SQLSTATE `55000`: **030 is forward-only**. Rollback requires restoring the matching
pre-migration database backup and images, not an image-only downgrade.

The **pre-squash historical numbering** ([mapping](#source-history-mapping))
**032/backend-42/UI-43 checkpoint** applied 032 through a standalone
restricted native migration Job while incompatible runtimes were paused, then rolled backend revision **42**
and UI revision **43** with full values and `--no-hooks`. Paused original-column
hash checks preserved all **82 original business tables**, prior ledger entries
and unrelated relations/roles/functions. Actual API/general-worker/Coverage-worker
readiness passed at schema/minimum **32**, marker **27**, ledger **0-32**. Full
protected pre-32 database/role backups and matching old image exports remain
retained. Scripted deployed-browser verification passed at that checkpoint, not
as current release acceptance. See [Milestone 8 closeout](development-progress.md#milestone-8-closeout).
No capacity certification, image-only rollback, provider check or new
ingest/matching operation is implied by this release.

## Canonical Coverage Display (030)

[030_coverage_display.sql](../api/migrations/sql/030_coverage_display.sql)
includes canonical identity and the compact-fact/stats foundation. Final source
schema and minimum runtime are **30**, retaining `migration_baseline=27` and ledger
**0-30**. The behavior's historical receipts remain unchanged; the source contract
is now installed at **revision 61**. Use native `/app/migrate` only with compatible
histories; see the separate [completed live cutover](#live-cutover-revision-61).

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

030 retains compact-fact maintenance and post-barrier segment expansion. All
reader inputs, including metadata, dates, deletion, route/applied eligibility and
region labels, queue deferred invalidation. Revision and public generation advance
once per account/transaction, not per row; no early ingest/provider generation
lock or eager rebuild of every date context. Verify both READ COMMITTED commit
orders, stale Repeatable Read `40001`, savepoints, rollback and deleted-account
queue cleanup. Runtime readiness checks exact 030 bodies and
ownership/ACL/RLS/index/trigger contracts, not numeric version alone.

Migration seeds revisions and advances public generation once per account to
invalidate old source-ID capabilities/caches. Original evidence, GPS, manifests,
jobs, matching states/rules and generation vectors remain unchanged. **No OSM
derivation/matcher/path-policy bump, OSM regeneration/download or route rematching
is needed.** Coverage recomputation means canonical context warm/rebuild from
existing evidence, not new route jobs.

The 030 rollout contract requires source verification; native 028-to-030
migration, role readiness, preservation and full rollback rehearsal on a protected
restore of an actual backup using the actual migration role; fresh backups and
matching old images. Stop/drain incompatible API/general-worker/Coverage-worker
runtimes and coordinate compatible images/UI, including tile SQL consumers.
Preserve migration/TLS/OSM replica/Martin flags and identity, source auto-sync,
admin-password debug, resources, concurrency, timeouts and retry budgets. Keep
all dumps/archives under `~/nfs-share/.temp/`, linked from the matching private
`deploy/internal` release bundle; monitor workspace, `/tmp`, NFS and application-PG
filesystems and inodes regularly. No live mutation is authorized by this document.
030 Down raises `55000`: rollback needs the matching database restore **and** old
images, never image-only downgrade. These are safety requirements, not completed
source-squash cutover assertions or new execution authorization. Verification, rehearsal and
deployment receipts retain their distinct scopes in
[current status](development-progress.md#current-status); browser and milestone
acceptance belongs in [Milestone 8 closeout](development-progress.md#milestone-8-closeout).

## Ingest Phase Progress (029)

[029_ingest_status_reporting.sql](../api/migrations/sql/029_ingest_status_reporting.sql)
includes the fenced, worker-only
`app.record_ingest_phase` function and a partial `app.job_events(job_id, id DESC)`
index for `code LIKE 'ingest-phase-%'`. Required phase writes use the append-only
journal independently of optional diagnostic-event limits. There is no backfill,
data rewrite or invented historical activity. Final source runtime **30** requires
the combined structural readiness contract. The former additive phase-only
rollback is not the rollback contract for combined, forward-only 029/030; retain
matching database backups and images together.

The revision-60 native migration completed in **73.09ms** using **pre-squash
historical numbering** ([mapping](#source-history-mapping)): 034 installed schema
**34/minimum 33**, baseline **27**, ledger **0-34**. That is an earlier release
receipt, not a squashed-source pass or cutover claim. See the
[follow-up closeout](development-progress.md#ingest-phase-progress-follow-up-029)
for unchanged behavior and recorded validation. No operations run in this docs task.

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
protected actual-backup rehearsal. For the historical folded-028 release, the
separately approved exact actual-profile rehearsal and live repair both passed; the following
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
