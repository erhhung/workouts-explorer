# Application Database Migrations

## Matcher-Owned Coverage Display Classification

The v84 feature folds nullable `display_class` and its enum constraint into016,
and the private immutable `coverage_match_visible(text,text,jsonb)` helper plus
its consumers into030/031. The persistence writer joins immutable parent policy
metadata: v84 payloads require a recognized classification, whereas legacy jobs
ignore that field and store NULL. Existing display readers use the matcher-owned
decision for current evidence and one frozen compatibility branch for legacy
evidence. Raw geometry, lease/generation/input/cancellation fences and ABI stay
unchanged. Runtime readiness checks the column/constraint, effective fenced
persistence body, helper ownership/ACL/attributes and all changed reader bodies.

An existing APP33 installation uses the rehearsed guarded nullable-column/body
adoption rather than replaying ledger entries. Preserve existing table/function
OIDs, original columns/constraints, ownership, ACL/RLS, schema33/minimum32/baseline27
and Goose0-33. Add/validate the nullable constraint without rewriting old match
decisions, and create the helper under the existing security owner with no public
or runtime-role grant. This does not relabel old evidence or bulk-fill
classifications. New policy results arrive only through versioned matching jobs.

Importer v4's public residential scratch and compiled context tags need no new
OSM ledger entry: the existing generation's importer version, source hash and
stage fences identify that contract. Importer-v3 resumes keep their original
style/stages and derivation21; fresh builds collect context from their PBF. A
new context/processing contract requires a new importer/context version, separately
from matcher policy changes and physical derivation changes.

## Named Private Neighborhood Roads In Coverage Display

Canonical030 and031 retain the matcher's named private neighborhood-road
exception when `service=driveway`. The correction applies to display-fact
precomputation, exact entity detail, focused geometry and both canonical/tile-local
MVT definitions. Existing parking exclusions, private capabilities, date/workout
scope, grouping identities and count semantics remain intact. Matcher policy and
durable traversal evidence are unchanged.

Existing APP33 installations require a rehearsed adoption of the four effective
function bodies; ordinary Goose Up does not replay already-applied030/031.
Guard exact function preimages and preserve OIDs, owners, ACLs, attributes/settings,
namespace ACL, schema33/minimum32/baseline27 and every Goose ledger row. Refresh
only affected derived display facts through the existing deferred invalidation
protocol, including already-present facts with incomplete geometry bounds. Preserve
raw routes, match geometry/lengths and applied Coverage authority, and invalidate
account/display generations normally so existing UI caches renew.

Shared `internal/coverage/testdata/service-road-policy.json` vectors exercise
foot/bicycle matching and native PostgreSQL display precomputations, lists,
statistics, detail, focus and tiles. Changes to either policy implementation must
test both, as required by the functional spec and ADR0009. Source-prefix and
runtime exact-body fingerprints include this approved correction.

## Cleaned Route Statistics

Canonical011 introduces private `workout_route_statistics_v2(uuid,uuid)`, consuming
existing display/export membership and workout-time-valid positive legs. Gaps,
omitted samples and backward time contribute nothing; duplicate timestamps keep
continuity without zero-time distance/elevation. Full and final partial unit
intervals use the accumulated observed distance/time, normalizing partial paces.
The cached `pace_distance_meters`/`pace_elapsed_seconds` pair supplies the same
distance-weighted mean; a validated cache constraint brackets it with both unit
ranges. Stationary tails are retained, including exact-unit endings. Altitude/gain
use observed components without cross-gap jumps, and envelopes cover the existing
display geometry. Reported totals and immutable GPS remain preserved.

The original twelve-column statistics helper remains an ABI-compatible wrapper.
The existing worker-facing `replace_workout_split_summary(uuid)` keeps its ABI,
owner, grants and live-ingest authorization; it updates only changed derived
cache fields and treats an already-correct route as a successful no-op. The new
helper has no direct runtime-role grant. No display/matcher policy changes are
made, and the existing three display/gap function hashes remain unchanged.

Live adoption is rehearsed from the actual function preimage, preserving OIDs,
ACLs/settings, namespace ACL and schema33/32 Goose metadata. Historical correction
uses one route per transaction, lifecycle/workout/cache row locks, bounded waits
and changed-only updates, without disabling RLS/triggers. The 2026-11-19 scoped
receipt confirms raw points, displayed geometry, provider metrics and Coverage
input revision are unchanged. Source-prefix snapshots include the approved011 fold.

## Optional OSM Metrics And Build Allocation Snapshots

Application033 captures the deployment's metrics capability with each new updater
execution. It can lower the global monitoring preference to disabled, but cannot
enable a policy the administrator disabled. Existing execution hashes retain their
captured policy. API settings expose the effective capability and reject attempts
to enable monitoring without it. The live adoption changes only the two existing
policy/event function bodies, preserving their identities, owners, permissions and
settings; it does not change the schema33/32 watermark or Goose ledger.

OSM011 adds nullable retained-build byte/timestamp snapshots. They are measured
from physical relation allocation after committed task/batch scratch cleanup,
independently of Prometheus. The adoption adds only those two columns to the
existing generation table and retains source11/minimum10 and the ledger.

Fresh application032 installation excludes the already capability-aware
`require_active_api_account` lifecycle function from generic purge-trigger
wrapping, including when an earlier source-timer table already uses that guard.
The canonical guard body and its approved hash remain unchanged.

## Independent Daily Source Sync

Canonical006 adds nullable `sources.auto_sync_time`, the private per-source timer
table and owner-scoped time accessors. Existing sources keep their interval
schedule until the owner saves a daily time. The public021 scheduler freezes due
source identities in its existing account lease, reads and validates only that
set, and advances those timers by preferred-zone calendar days. Legacy interval
wakes cannot include timed sources. Lease/version fences, source-generation
checks, job coalescing, snapshot encryption and lifecycle guards are retained.
Time and preferred-zone edits serialize through a settings-only advisory lock.

The additive live adoption is rehearsed against actual deployed function bodies,
preserving existing function identities/owners/permissions/settings, source data,
schema ACL and Goose ledger. Source/schema remains33, minimum runtime32 and
baseline27; no migration-number change is required. API/UI images include the
matching contract, and runtime readiness checks the new helpers, column and trigger.

## Promoted-Region Eligibility And Administrator Counts

The targeted-lock refinement keeps APP33 and folds its helpers into APP015/028.
Shared account locks protect lifecycle/ownership; only Coverage admission takes its
private advisory mutex. Related route jobs and destination parents replace global
history locking, with NOWAIT acquisition preventing job/state lock-order inversions.
Metadata rebinds that unexpectedly need admission retry rather than upgrading locks.
APP033 adds a separate four-column administrator progress function, preserving the
existing three-column counts function. Missing-map counts require current negative
selection proofs; waiting/stale readiness is not classified as confirmed missing.
The live patch preserves all original function OIDs, ACL/settings, schema ACL and
Goose rows, and is rehearsed from the actual live function definitions.

Coverage's canonical region-ID checks in APP013/015/016/017/023/028 accept the
OSM11 hierarchical identifier format, including `geofabrik:us/nevada`. The provider
portion is at most 64 characters and the region portion at most 192. Existing
generation-vector, ownership, lease and admission bounds are retained. The live
APP33 correction replaces only that regex in five existing function bodies and
five CHECK constraints, with identity/permission and ledger preservation guards;
it does not introduce a migration or relabel existing regions. The public test
fixture covers the complete 554-region catalog and cached legacy IDs.

The administrator-managed region workflow removes the static region allowlist.
OSM001 no longer defines the region configured flag. OSM010 uses version-2 catalog
and selection fingerprints, active-region names, and active presence independent
of deployment settings. The live OSM11 adoption renames the cached names column,
replaces the refresh body while preserving its identity/owner/ACL/settings,
recreates only the two affected region triggers and removes the flag. It refreshes
the derived catalog revision without changing map data or promotion numbering.

APP033 owns the administrator-only region workout aggregates and private spatial
membership index. Memberships follow globally unique workout IDs through cascading
FKs, including account purge, and update when stored routes or cached boundaries
change. The count reader checks a live administrator session and returns aggregates
only; application roles cannot read membership rows directly. Totals retain
workouts from deleted sources. Progress requires current input/provenance and the
active build identity, not a regional number or successful job counter.

Both patches are rehearsed against the previously deployed app33/OSM11 definitions
and applied under their existing owners. Original ledger rows/timestamps, schema
watermarks, function identities/permissions and OSM data remain intact. No new
migration is introduced. Current receipts are in `promoted-region-coverage`;
earlier fingerprint-v1/configured-region descriptions below retain historical scope.

The canonical source sequence is **001-033**, with **SupportedSchemaVersion=33**,
minimum runtime **32**, recovery baseline **27**, and ledger **0-33**. The raw-route
spatial policy belongs to [009](#raw-route-continuity-009), and the complete private
tile pipeline belongs to [031](#tile-rendering-031). There is one current runtime
and function contract; no alternate migration profile or tile-function alias is
shipped. The controlled catalog/history reconciliation is validated against a
fresh source installation and retains monotonic sequence high-water values.

Native migration tools must embed this exact complete source sequence. UI-only
upgrades keep hooks disabled. Historical operator checkpoints remain private and
are not additional migration compatibility contracts.

## Consolidated Region Management

[033_osm_region_management.sql](../api/migrations/sql/033_osm_region_management.sql)
owns catalog refresh, durable administrator region-processing requests, ordered
tasks/events, successful-work elapsed counters and deletion. It retains the
capability-fenced executor RPC, restricted controller/executor roles and limited
administrator admission/control grants. Its active-region constraint also covers
deletion; cleanup has three tasks rather than the processing inventory.

The same canonical source owns persisted OSM processing defaults and per-execution
storage-monitoring snapshots. Existing running executions retain their original
policy. At the first authenticated updater read for each new capability, the RPC
captures the current global value/revision under a row lock and binds it to that
execution hash. Repeated reads cannot change it; the next start/resume refreshes
it. Completed requests retain their final execution's value. A capture trigger
rejects policy edits outside this binding operation.
Install these additive catalog changes through a protected rehearsed adoption on
an already-applied source sequence; the maximum source/schema stays33.

[030_coverage_display.sql](../api/migrations/sql/030_coverage_display.sql) owns the
account-scoped display-currentness accessor. Unchanged applied Coverage remains
current when regional source data is removed; changed route/matching identity or
a newer accepted regional generation still invalidates it. New calculations keep
their separate map-data prerequisite.

Fresh installations use source/schema33, minimum runtime32, baseline27 and ledger
0-33. Existing installations require a protected, rehearsed catalog/ledger fold:
preserve original ledger0-32 and the original terminal ledger row/timestamp,
business data, function OIDs/owners/settings/ACLs, triggers and sequences. Install
the display accessor from canonical030, reconcile the ledger/schema marker and
deploy matching images in a quiet-writer window. Ordinary Goose Up does not replay
already-applied source files. Keep preimages and rollback receipts outside Git.

OSM migrations retain request-bound cleanup receipts and add an explicit regional
generation high-watermark in canonical011. Successful promotion allocates the next number
atomically. Retirement/deletion retains both the counter and promotion history;
failed processing consumes no regional generation number.

## American Cancellation Vocabulary

Canonical source uses canceled/canceling throughout SQL, JSON, jobs and counters.
The earlier protected vocabulary reconciliation retained function identities,
owners, grants, trigger modes and private data. It requires no separate permanent
spelling-only migration in the canonical sequence.

## Raw Route Continuity (009)

009 defines the private spatial splitter. 010 invokes it after the unchanged
timestamp-cadence builder. The normal fenced ingest writer therefore creates no
line across consecutive observations more than 200 metres apart. Original GPS
observations, provider hashes, workout identity and aggregates remain intact.
Singleton fragments do not invent lines or dots; stationary runs remain valid.
See [ADR 0015](decisions/015-route-continuity-and-preserved-observations.md).

Historical repair is a separate explicitly authorized account-scoped operation,
using retained derived-geometry preimages, source-row guards and an atomic update.
Existing triggers rebuild exact and LOD chunks. Account generations advance
normally and are never reset. Already valid Coverage evidence and unstarted
states remain unchanged because matching already observes this spatial limit.

The current source consolidation does not repeat that repair. Catalog and ledger
reconciliation preserves business tables, sequences, generations and completed
repair results, and is rehearsed together with guarded checkpoint recovery.

## Current Source Layout

| Source | Contract |
|---|---|
| 001-008 | Account, job, source, ingest and deletion foundations |
| [002](../api/migrations/sql/002_account_lifecycle.sql) | Account access, durable account purge, private cleanup receipts and transaction-bound purge capabilities |
| [003](../api/migrations/sql/003_sources_and_job_snapshots.sql) | Source/job snapshots and runtime-versioned interactive source-check claim |
| [009](../api/migrations/sql/009_raw_route_map.sql) and [010](../api/migrations/sql/010_segment_route_geometry.sql) | Raw route capabilities, spatial continuity and timestamp segmentation |
| 011-027 | Splits, time zones, Coverage, provider identity and recovery baseline |
| [028](../api/migrations/sql/028_ingest_coverage_pipeline.sql) | Committed-route pipeline and bounded eligible cohorts |
| [029](../api/migrations/sql/029_ingest_status_reporting.sql) | Ingest outcomes, file filtering, reprocessing, activity and metadata reconciliation |
| [030](../api/migrations/sql/030_coverage_display.sql) | Canonical display identity/currentness, compact counters, exact detail, independent Coverage bounds and Stats report scopes/history reads |
| [031](../api/migrations/sql/031_tile_rendering.sql) | Exact/LOD storage, authorization and optimized current MVT functions |
| [032](../api/migrations/sql/032_admin_accounts.sql) | Registration/login metadata, invitation lifecycle permissions, bounded administrator usage and late-dependent purge grants/trigger guards/API write fences |
| [033](../api/migrations/sql/033_osm_region_management.sql) | Public catalog refresh, durable region processing/timing/deletion, restricted roles and capability-fenced executor RPC |

The approved route foundations and tile file are consolidated source. Other
foundation definitions retain their established behavior. Current readiness is
catalog-only, validates exact function/privilege contracts and rejects an
unrecognized schema/history rather than implementing migration compatibility.

## Managed Region Updates (033 / OSM 011)

Application 033 adds administrator-owned region requests, 57 ordered logical
task rows and retained events. The API has column-limited admission/control
grants, the controller has public control/projection grants, and the executor
can only invoke its execution-capability-fenced RPC. It has no private/control
table SELECT or UPDATE grant. Runtime readiness verifies the new objects and
separation. Source/schema becomes34 while minimum32/baseline27 stay intact.

OSM 011 adds the request UUID binding on candidate builds and public stage
SELECT for controller recovery. Cleanup can identify only its own unfinished
candidate; existing active data and old generations are unchanged. OSM source is
12, minimum10 retained. No existing stage SQL/fence changes are required, so
paused work can keep its pinned updater image and committed checkpoints.

Back up the application and the public OSM catalog/schema preimages, provision
the two limited runtime credentials, apply OSM011/app033 with the migration
credentials, then deploy the controller and approved updater digest. The Job
template/controller enablement is Helm-owned. See
[the managed-update guide](osm-region-updates.md).

## OSM Provider Catalog Refresh (033 / OSM 011)

Application 033 adds the administrator-owned refresh queue/public projections and
advances source/schema to 33, retaining minimum32, baseline27 and original 0-32
ledger rows. Readiness verifies new objects and API/worker grant separation.
These are public OSM/admin scopes, not new account-private purge tables.

OSM 011 retains minimum10 while adding catalog observation/availability, HEAD
size/date outcomes, hierarchical provider IDs, actual download-completion times
and a processed region-size snapshot. Existing generations are not reimported or
promoted. A narrow size function sums the three canonical region partitions with
indexes/TOAST. A trigger records download completion from the existing downloaded-
bytes/checksum update too, without inventing dates for historical records.

Provision `scripts/osm-catalog-role.sql` on the OSM cluster before 011 and supply
its own runtime password through the configured Secret. OSM 011 grants only
region INSERT/UPDATE, public metadata SELECT and the bounded size function.
Migration credentials stay outside runtime connections. Preserve application
backups and OSM schema/catalog preimages; these migrations do not reimport maps.

## Lifecycle And Source-Check Fold (Helm 174)

The approved lifecycle/source consolidation places account deletion in002, source-check claims in003, and dependent administrator installation in032. Region management is consolidated in033.
runtime fences and readiness checks use 32.

A full protected live backup was restored and the exact reconciliation rehearsed
under the migration role. The quiet-writer live reconciliation changes only the
three version-gated function bodies, schema/minimum metadata and superseded Goose
33/34 rows. Original Goose 0-32 rows/timestamps, sequence high-water, application
data and relation identities remain intact. Function definitions match a fresh
001-032 installation; table/constraint/policy and trigger catalogs match the live
before-image. Pre-existing structural differences from fresh are not repaired.

Helm 174 rolled API/general/Coverage workers with hooks disabled, preserving UI,
Martin and deployment settings. The folded migrator accepts the history and normal
Up is a no-op. Protected before-images, verified backup, rehearsal and rollout
receipts remain the recovery authority. Historical installation and
rollback descriptions below are not the current deployment contract.

## Account Access Fold (002)

The owner's explicit 11/16/2026 development-only consolidation folds the proposed
`033_account_access.sql` into `002_account_lifecycle.sql`; that proposed account
migration is not shipped. The later independent source-check 033 was subsequently
folded into 003 at Helm 174 and does not reverse this account-access fold.
002 adds account state `disabled`, narrowly privileged access/issuance functions
and a job-insertion lifecycle fence. Its suspension command reuses the source and
Coverage cancellation engines introduced later; those PL/pgSQL calls resolve
when invoked against the complete schema. This fold's deployment checkpoint was
schema/minimum runtime 32/32 and ledger 0-32; historical source-check 033 later
advanced the schema/ledger before the Helm-174 consolidation restored 32/32.

An existing installation does not replay applied 002. The live catalog upgrade
was independently backed up, restored and rehearsed under the migration role,
then applied in a quiet writer window. New helper bodies, owners and grants match
fresh source; comparisons normalize role names rather than database-specific role
OIDs. The exact Goose rows/timestamps, schema metadata, existing functions,
private-data counts/relations and identity hashes remain unchanged. New runtime
readiness rejects a pre-fold catalog even when it has the same numeric version.
See [deployment evidence](development-progress.md#admin-account-disable-and-enable).

Keep the protected pre-upgrade backup, catalog/preimage evidence and matching
old image pins for rollback. A generic Goose Up/Down or ordinary Helm rollback
does not reverse this approved source fold. Do not reconstruct an old account
constraint over newly disabled rows; recover the matching database/images through
the retained procedure. This exception does not authorize rewriting other applied
migrations or historical test receipts.

## Account Deletion Foundation

The historical account-deletion checkpoint is consolidated into002 and032. The runtime provides sanitized admin-owned deletion tasks, private execution
receipts and transaction-bound purge capabilities, plus narrowly granted functions.
Runtime roles never receive raw purge-table DELETE/capability-table access.

Only a fixed, lease-validated task can create a full purge capability. Original
account-table trigger bodies gain an exact capability guard so append-only checks
and maintenance callbacks are skipped only during that transaction; their prior
behavior remains pinned otherwise. Readiness verifies the guarded bodies, grants,
inventory and API write triggers. Foreign keys are never disabled. Batches commit
monotonic progress and verify all private scopes before releasing identity keys.
The ingest-phase fence uses the matching supported runtime.

Migration Up was rehearsed under the actual migration role on a complete restored
live backup; an isolated purge of restored @foo verified zero private scopes and
unchanged other-account counts. That does not delete the live account. Native
synthetic cases additionally cover active ingest/staging drain, interruption and
same-name/email fresh registration. Matching backup/images/receipts remain private.

Rollback is not an old-image-only operation because minimum34 excludes old writers.
Keep services quiescent and recover a matching database/runtime pair through the
protected procedure, accounting for deletion work accepted after the backup. Never
rewrite Goose rows or silently resurrect purged account data as a deployment fix.

## Source Check Lane (003)

The compatibility/rollback notes in this subsection describe the historical
Helm-167 checkpoint only. The earlier source-check and account-deletion additions were folded into their foundations; use the
current 001-032 contract and retained reconciliation recovery receipts.

003 owns `app.claim_next_source_check(text,uuid,interval,integer)`, owned by
`workouts_security_owner` and executable only by `workouts_worker`. The helper
checks the runtime fence and uses the existing check-only admission path; the
legacy unversioned entry point remains disabled. Readiness verifies its exact body,
signature, ownership and ACLs. No existing function ABI or private data row changes.

This additive migration advances schema/runtime source to 33, keeps minimum runtime
32 and appends only Goose version 33. The original 0-32 ledger rows/timestamps are
preserved. Old runtime32 remains compatible with the prior claim paths; the literal
ingest-phase writer fence remains valid against minimum32 and is not rewritten.
Live Up was rehearsed using the deployed migration binary and restricted migration
login on a complete restored live backup, then run through a chart-derived Job.
Both migration and application rollout are verified at Helm 167.

For an application rollback, retain the source-check catalog and roll matching old
runtime32 images with migration hooks disabled; an old embedded migrator does not
own the newer ledger. Match Harbor's current tags to the restored pins. Do not
rewrite original Goose rows or run an old migration hook against the new ledger.
Any schema rollback requires an explicit proper Goose/recovery procedure using
the retained protected backup and migration receipts.

## Custom Date Ranges (002)

### All Time Scope Contract (017, 030, 031)

All time is an explicit query mode rather than a finite custom interval. The
consolidated selection foundation includes `map_selections.all_time`, and canonical
contexts/report scopes include the same non-null, default-false flag. Context
uniqueness and authorization distinguish bounded and unfiltered modes, even when
display bounds coincide. The report preparation helper accepts a trailing boolean
defaulting to false, retaining eight-argument bounded callers; the private tile
scope returns the captured flag. Runtime checks protect those signatures, defaults,
physical columns, constraints, ACLs and exact helper bodies.

Existing schema-31 installations use the rehearsed, hash-guarded operator update
before the matching API/worker rollout. It adds the three flag columns, extends the
context key and replaces the affected readers/preparation helpers atomically;
the two changed helper signatures are recreated with their restricted ownership
and grants. It does not rerun applied Goose files, alter the schema high-water or
ledger, mutate preferences, rebuild matching evidence or start jobs. Retain the
schema backup and old-schema rehearsal/readiness receipts with the deployment.

### Recent Custom Ranges

The account lifecycle foundation defines `custom_date_range_limit` (1-5, default
3) and the bounded `recent_custom_date_ranges` array on the forced-RLS
preferences row. Date-range and limit updates hold the preferences row lock, so
history promotion, deduplication and truncation commit atomically with the patch.
The history is read-only in the API contract; clients apply a range through
`dateRange`. New accounts start with an empty list. Existing installations receive
the same columns and constraints through a guarded operator update that seeds an
existing explicit default range account by account without bypassing RLS. This
keeps schema/minimum runtime 31, ledger 0-31, route geometry, tile functions and
account data generations intact.

## Coverage Route Bounds (030)

The same foundation persists `applied_result_status` (`matched`/`no_matches`, null
before application), constrained to agree with the nullable Coverage box. Applied
proof changes enqueue the existing deferred projection even for a first empty
result with no match writes. Existing applied results receive a guarded
classification-only backfill: empty boxes become `no_matches`, nonempty boxes
become `matched`; original state fields and geometry remain intact. No extra
source migration is introduced and schema/minimum runtime stay **31/31**.

The display foundation defines four nullable `coverage_*` longitude/latitude
columns on `app.workout_coverage_states`, with an all-null-or-valid-box constraint.
Raw GPS bounds remain on `app.workout_routes`. The existing serialized deferred
display rebuild combines compact path/park evidence bounds and updates only the
four derived fields; path-name qualification changes enqueue affected workouts
through that same coordinator. Removing the final displayable match clears the
Coverage bounds. Ordinary failed rematching preserves prior applied evidence
and its bounds.

Fresh installations backfill these fields with the other display projections in
030. Existing schema-31 installations require the separately rehearsed guarded
operator update: add the exact columns/constraint, replace the two changed
functions while preserving owners/ACLs, and backfill every account from retained
compact evidence. Already-applied Goose migrations are not replayed. Preserve
schema/minimum runtime **31/31**, ledger **0-31**, generations, revisions, original
GPS, raw bounds, matching evidence, jobs, and credentials. The source inventory
remains **001-031**; no additional migration or alternate function ABI is shipped.
Verify restricted-role readiness and coordinate API/shared-worker/UI images with
the revised source and API contract. Retain private preimages and preservation
proofs; the operation does not enqueue imports, OSM work, or rematching.

## Stats Reports (030)

The display foundation also defines `app.stats_report_scopes`: an account/session-
owned, expiring, forced-RLS table referencing the canonical context by composite
account/context key. Its private owner policy and complete table/constraint/index
contract are checked without giving runtime logins direct fact-table privileges.
Five API-only entry functions prepare, read or release a scope, return qualifying
Coverage events and resolve the qualifying/predecessor workout IDs for returned
page rows; two authorization helpers remain private. The shared
canonical preparer accepts report scopes without creating Map capabilities and
protects live report pins during context garbage collection.

New Coverage reuses the canonical deduplicated all-history visits. The event
reader computes the immediate predecessor before inclusive range filtering,
materializes qualifying events and fetches display metadata by a complete
account/context/canonical index probe. This preserves performance when fresh
context statistics underestimate its size; no new monthly/yearly rollup table,
matching job, OSM operation or provider access is needed.

The initial M9 adoption is folded into 030; source **001-031**, schema/minimum
runtime **31/31**, baseline **27** and ledger **0-31** remain unchanged. Existing
installations receive separately rehearsed, guarded operator SQL: add the private
scope table/helpers, replace the shared preparation body preserving owners/ACLs,
then apply the cold-plan event-body correction. Do not replay applied Goose files.
The review follow-up adds only `app.stats_visit_workouts`, an API-only, scoped
full-index-key reader. It selects the earliest workout on the qualifying date
and latest on the predecessor date with deterministic start-instant/ID ties;
the API invokes it after pagination. Previous dates outside the report range
remain noninteractive in the UI. No table, ledger or data backfill is needed.
Retain the protected full backup, exact function preimages, preservation receipts
and matched image references; verify restricted API/general/Coverage-worker
readiness against the revised exact catalog. The operation changes no existing
business rows, evidence, route geometry, generations, revisions or job outcomes.

See [Stats architecture](architecture.md#stats-reports-and-new-coverage-m9) and
[the deployment record](development-progress.md#stats-and-new-coverage-initial-implementation).

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

## Historical Deployment Checkpoints

Historical deployment and recovery measurements retain their original scope in
private records. They are not current source histories or runtime profiles.
Use the current source layout and exact current catalog contract for deployment.

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
source-30 sequence finished at schema/minimum runtime **30/30**, baseline **27**;
intermediate sections are not separate supported runtime contracts.

The final partial-success criterion is imported or updated workouts, not successful
file reads or skips. Durable records cover progress snapshots that lag committed
imports. Failed source children remain failed and retryable; all-canceled runs
remain canceled.
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
all-canceled runs remain canceled, and Coverage/deletion outcomes are unchanged.

The same rule projects historical ingest results in job detail, latest-job cards,
notifications and history filters before counting/pagination. A historical stored
failure with imported or updated workouts displays `partially_succeeded` without
rewriting its execution record, notification row, timestamps or retry lineage.
Source-child display follows the same rule while its stored failure stays
retryable. New terminal parents use the workout-based rule directly.

## Explicit Ingest Reprocessing (029)

`029_ingest_status_reporting.sql` includes explicit ingest reprocessing, retaining
`migration_baseline=27`. Its combined 029/030 release used support/schema/minimum
runtime **30**; current source schema is **31/minimum runtime 31**. Use native `/app/migrate` for
compatible histories; the completed revision-61 historical reconciliation was
not ordinary Goose Up or permission to replay applied SQL.

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
all-canceled runs retain their existing outcomes.

Take fresh protected backups and retain matching old images. Drain/stop
incompatible API/general-worker/Coverage-worker runtimes, apply the full source sequence, then
deploy coordinated compatible API/worker images and matching UI. Minimum runtime
31 now fences older execution; do not mix old API/worker images with this contract.
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
before its metadata assignment/Down section. At that release APP source/runtime stayed
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
Verification using pre-squash historical numbering ([mapping](#current-source-layout)) belongs in
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
and the 030 contract's four tile layers share that context and preserve actual source geometry
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
data rewrite or invented historical activity. Source runtime **31** requires
the combined structural readiness contract, including 031's guarded phase-writer
runtime-fence advance. The former additive phase-only
rollback is not the rollback contract for combined, forward-only 029/030; retain
matching database backups and images together.

## Tile-Local Geometry (031)

[031_tile_rendering.sql](../api/migrations/sql/031_tile_rendering.sql)
is new and forward-only; **001-030 remain byte-unchanged**. Source/runtime/minimum
are **31**, baseline stays **27**, and native fresh/30-to-31 history is **0-31**.
The user-approved 031 cutover **deployed at Helm 65**, APP **31/minimum 31** and
OSM **10/minimum 10**. Native Up preserved old ledger tuples and appended 031 at
row **55**, advancing high-water to **55** without reset. This is not historical
pre-squash 031 ingest reprocessing or authorization for another maintenance run.

The migration locks **both `workout_routes` and `workout_segment_matches` in
`SHARE ROW EXCLUSIVE` mode for the entire transactional backfill**. It installs
forced-RLS owner-only EPSG:3857 raw/evidence chunk tables, cascading source FKs,
AFTER-write maintenance, spatial and covering lookup indexes, and replaces both
MVT bodies. Chunks have at most 256 vertices; original GPS, match evidence,
canonical contexts/counts and exact focus/detail readers remain authoritative.
API-only five-argument `authorize_map_tile` returns an expiry; Martin functions
retain their four opaque JSON scope parameters and independent authorization.
The guarded phase-writer patch advances its literal runtime fence to 31 without
rewriting historical SQL. See the [render contract](architecture.md#tile-local-rendering-031).

Runtime readiness validates exact bodies, security/ACL/RLS, tables, FK cascades,
indexes and maintenance triggers, including ingest fences, not just metadata 31.
API and **both shared-worker consumers** were coordinated with SQL maintenance/
backfill, with incompatible writers stopped and **no old-30 write window**.
Actual native full readiness passed under all three runtime roles before writers
started and again after rollout. Native migration under the actual migration role
took **50.77s**; the Job took **67.65s**, with a **90s statement / 500ms lock**
budget. These maintenance budgets are distinct from runtime tile limits.

The tile login defaults in role provisioning and **fresh** development bootstrap
are `row_security=on`, `statement_timeout=4s`, `lock_timeout=250ms` and
`idle_in_transaction_session_timeout=5s`. Existing connections do not inherit role
changes. Fresh actual tile-login settings were verified effective after Martin's
controlled zero-to-one restart, preserving **1.15.0 / pool 4 / cache off**.
These are login defaults, not function-local timeouts or migration backfill
budgets. HTTP abort cannot prove immediate PostgreSQL cancellation;
actual-role `pg_sleep` and lock-wait integration checks verify the SQL bounds.

Actual-data rehearsals under the native migration path completed Up in **40.866s**
and **45.053s**. The bounded consistent read-only snapshot **excluded raw GPS
points and was not a full database backup**. Before writers started, guarded
catalog-checkpoint restoration passed twice at approximately **152s**, restoring
the exact old catalog without `CASCADE` and preserving sequence high-water.
No recovery was executed live. **Writers have resumed, so that pre-start checkpoint
is no longer eligible for rollback**; it is not an ongoing recovery guarantee.

Authoritative business-data fingerprints across **95 tables / 305 buckets** were
unchanged. Raw GPS was **not dumped, scanned or hashed** for that proof; it does
not establish full raw-data backup/hash coverage. Actual dataset/chunk/storage
measurements remain private. Down still raises `55000`; **binary-only rollback is
unsupported**, and recovery needs a separately validated matching database/image
plan. Do not infer that a new full backup exists. Large recovery artifacts belong
on approved NFS with capacity/inode checks, not casually in the workspace or
`/tmp`; further operations require separate approval.

Local guarded PostgreSQL **18.6 / PostGIS 3.6.4** verification passed native fresh
creation and 30-to-31 upgrade, backfill, transaction rollback/forward-only history,
geometry/security and catalog-readiness cases. Those source receipts remain
distinct from the actual-data rehearsal and deployment evidence above; none is
capacity acceptance. Authenticated tile smoke and viewport capacity are not
claimed in this deployment record. See
[verification scope](development-progress.md#tile-local-map-optimization-031).

## Tile Rendering (031)

[031_tile_rendering.sql](../api/migrations/sql/031_tile_rendering.sql) contains the
complete private rendering pipeline: exact projected raw/match chunks,
whole-component raw LODs, transactional maintenance, scoped authorization and the
optimized canonical MVT functions. It publishes schema/minimum runtime **31/31**
with baseline **27** and ledger **0-31**. Down is forward-only.

Only `app.raw_route_mvt` and `app.coverage_mvt` are published for current tiles.
The private scope helper and exact body/ACL checks belong to the same current
contract. Startup requires the exact current schema/history and object profile;
there is no alternate tile implementation or migration compatibility dispatch.

The source is validated by native fresh installation and guarded PostgreSQL
fixtures. Existing installations use an explicitly authorized, checkpointed
catalog/history consolidation rather than replaying table backfills. Rehearsal
checks full function equality with fresh source, preserved table identities and
generations, retained ledger rows, sequence high-water and checkpoint recovery.
The already repaired route data is not rebuilt or reprocessed during consolidation.

Deploy matching API/shared-worker/UI images and restart Martin to discover only
the current functions, retaining its pool/cache/resource settings. Resume workers
only after current-role readiness and data-preservation checks pass. Historical
performance checkpoints remain private evidence, not fresh capacity acceptance.

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
canceled targets and deduplicated obligations do not count. Private prepared
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
descriptors. Successful siblings never rerun; canceled siblings are not revived.
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
