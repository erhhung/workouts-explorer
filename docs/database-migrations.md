# Application Database Migrations

## Canonical Baseline

The active Goose sequence is contiguous. Migrations **001-026** establish the
foundation; `027_coverage_recovery_baseline.sql` is an ordinary static migration
from schema 26 to **27**. It adds routed counters, the cause-aware retry policy,
runtime-capability compatibility, shared diagnostic limits and retry/Superseded
presentation. It has one final assignment to schema 27/minimum runtime 26 and
sets `migration_baseline = 27`.

027 contains no historical-version dispatch, intermediate schema versions,
ledger rewrite, generated legacy payloads or archive loader. Production embedding
includes active root SQL only. Tests obtain isolated feature checkpoints through
semantic sections of that same SQL, not a second versioned migration history.

The user-approved active sequence remains **001-028**, with **no active 029**.
`028_ingest_coverage_pipeline.sql` folds together the pipeline, deferred-generation
locking fix, strict eligible-cohort construction and mixed-failure automatic retry
policy. Schema and minimum runtime are **28**, `migration_baseline` remains **27**,
and source `SupportedSchemaVersion=28`. Goose's version-0 sentinel is bookkeeping,
not a migration file. Internal serial row IDs need not be consecutive; migration
versions must be.

**The folded-028 contract is deployed in `dev-20261020`, Helm revision 29**, with
schema/Goose/minimum runtime 28 and marker 27. The separately approved exact
actual-profile repair and protected actual-backup rehearsal are complete.
The same numeric version does not establish contract or feature compatibility.
Ordinary static 027 and migrations 001-027 stay unchanged. Folding this
028 contract was an explicit user decision, not permission to edit applied SQL as
a routine upgrade mechanism.

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

An old-contract schema 28 has the same numeric version as current source, but an
incompatible contract. It requires a **new private, guarded offline repair**,
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
- Keep active source at 001-028 for this decision. Do not add a public old-contract
  archive or active 029 as a repair shortcut. Chart semantic changes remain frozen
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
