# Ingest Reprocessing

## Scope And Status

This is the approved public contract for bounded ingest and retry behavior, now
included in **029_ingest_status_reporting.sql**, for local/NFS and iCloud sources.
Final source/runtime support, schema and minimum runtime are **30**, baseline **27**,
ledger **0-30**. See the [history mapping](database-migrations.md#current-source-layout)
and [pre-cutover actual status](database-migrations.md#historical-deployment-checkpoints).
The behavior is deployed under historical numbering; live source-squash
reconciliation is authorized but pending here.
Milestone acceptance is governed only by
[Milestone 8 Closeout](development-progress.md#milestone-8-closeout), not this
contract or a deployment receipt. Capacity certification remains separate.

[030 canonical display identity](decisions/013-canonical-coverage-display-identity.md)
combines with compact display projections without changing this ingest
contract, job behavior, generation policy or matcher policy. Its Coverage
recomputation is canonical context warming/rebuild from existing evidence, not
reprocessing files, OSM regeneration/download or route rematching; no provider
check is needed for context warming.
Historical observations elsewhere predate explicit reprocessing; they are not
synthetic replacement observations or proof of this contract.

The separate requirement that all popups include **Dismiss** belongs to
the UI release, not this ingest design; browser closeout is tracked at the
authoritative acceptance link. No source configuration, credentials, live import
or private workout data is needed to document or verify the contract.

## Default And Explicit Reprocessing

New bounded runs use the same metadata-based skip policy as incremental runs,
restricted to the inclusive export-file date range. Unchanged successful files
are skipped without reread/download when their ownership-blocked epoch is absent
or current. New, failed, changed and stale ownership-blocked files are processed.
The date comes from the eligible export filename, not the workout timestamp.
Eligibility, source/account scope and date bounds are never bypassed by force.

The old behavior, downloading and processing every eligible file in a range, is
available for a **new** run only through `reprocessAllFiles: true`. Existing
persisted bounded jobs whose flag is absent retain their original force policy;
this compatibility rule does not change the default for new API requests.

File reprocessing is not workout duplication or forced Coverage rematching.
Provider identity, source ownership, deletion suppression, normalized-content
deduplication and route identity still apply. Identical content keeps the same
workout and route; changed content follows ordinary transactional update rules.
Unchanged reimports do not guarantee new Coverage jobs or reset failed-target
retry budgets. The distinct proven source-adoption Coverage obligation remains
unchanged; `reprocessAllFiles` is not a Coverage-force option.

## Manual Sync Control

In **Specific date range** mode only, show the unchecked-by-default checkbox
**Reprocess all files in this date range**, below both Start date and End date
inputs and above **Start sync**. Do not expose it in **New data only** mode or
send its field for incremental requests. Switching back to incremental clears
the selection; a new form starts unchecked.

Hovering the checkbox or label, or focusing the checkbox with the keyboard,
reveals an associated, accessible tooltip with this exact text:

> Download and process every eligible file in this date range, including previously successful files. This may be slow and transfer substantial data.

The UI sends an explicit boolean for bounded submissions. The warning describes
a fresh forced run, not a promise to redownload successes on each retry or after
same-job lease recovery.

## Public API

`POST /api/ingest` uses the existing closed `IngestCreate` request in
[`api/openapi.yaml`](../api/openapi.yaml). Its fields are `sourceIds`, optional
`startDate`, optional `endDate`, and optional boolean `reprocessAllFiles`.
`sourceIds` remains a unique list of 1-100 source IDs, authorized and validated
under the owning account. Source availability and connection checks are unchanged.
There is no new public mode, contract-version field or receipt field.

| Dates and flag in a new request | Meaning |
|---|---|
| Both dates omitted; flag omitted | Incremental metadata-based processing |
| Both valid ordered dates; flag omitted | Bounded metadata-based processing; normalize flag to `false` |
| Both valid ordered dates; flag `false` | Same bounded default |
| Both valid ordered dates; flag `true` | Bounded explicit reprocessing |
| Either date missing; flag present, even `false` | Reject; the field is bounded-only |
| Missing one date, invalid date, reversed range, or non-boolean flag including `null` | Reject |

Dates use `YYYY-MM-DD`, must be real calendar dates, and allow start equal to end.
The presence restriction applies even to `false`; it is not enough for the value
to be falsey. Validation errors use existing Problem Details responses. Accepted
requests retain `202 Accepted`, `Location` and `IngestAccepted` fields `jobId`,
`status` and `reused`.

These are **synthetic January 2025 inputs**, not real source or workout records.
The source ID is illustrative and must refer to a connected owned source to run:

```json
{
  "sourceIds": ["0123456789ABCDEF0123456789ABCDEF"],
  "startDate": "2025-01-01",
  "endDate": "2025-01-31"
}
```

This uses the bounded default, just like adding `"reprocessAllFiles": false`.
To request the old all-eligible-files behavior explicitly:

```json
{
  "sourceIds": ["0123456789ABCDEF0123456789ABCDEF"],
  "startDate": "2025-01-01",
  "endDate": "2025-01-31",
  "reprocessAllFiles": true
}
```

## Persistence And Active-Job Coalescing

New bounded parent and source-child parameters persist an **explicit boolean**,
including `false` when omitted from the incoming request. Existing `mode`, dates,
parent `sourceIds`, and child `sourceId`/`generation` retain their meanings.
Incremental parameters carry no reprocessing field. An absent flag in an existing
bounded job still means force; retries inherit that effective policy, while new
retry jobs persist it explicitly. Do not introduce another new/old contract marker
or rewrite historical parameters to distinguish the two defaults. Existing
historical parameter compatibility remains in place, not generalized to new input.

New API enqueue uses `coalescing_version=2` and the v2 canonical key. Within the
account-owned scope it distinguishes mode, inclusive dates, effective force,
sorted source IDs and their current generations, a fresh root versus a retry,
the retry parent predecessor, and sorted source-to-child predecessor links.
Omitted and explicit-false new bounded requests coalesce; false and true do not.
Equivalent active requests reuse the queued/running parent, including while
cancellation is pending. Retries of unrelated roots, sibling retry paths and
other branches must not coalesce just because dates and source IDs match.

Use the existing sorted source-row locks and authoritative active-job uniqueness
constraint; no additional lock or coordination service is introduced. Preserve
the existing conflict guard for incompatible earlier active ingest work. Do not
rewrite existing coalescing keys, retry ancestry or queued historical jobs.

## Retry And Same-Job Recovery

Retry still creates a new parent and eligible failed/cancelled source children,
with current validated source configuration and immediate-predecessor links.
It retains the original range and effective reprocessing policy, not a file-list
filter derived from the Files disclosure. Latest-attempt authorization, ordinal
limits, non-forking creation and account/source fencing remain unchanged.

Before applying force or ordinary inventory skips, **every retry** checks for a
valid successful processing receipt for the same source-relative file and current
observed metadata in its own source-child ancestor path. Only predecessor edges
on that path count, not siblings, descendants, other retry branches or unrelated
jobs. Matching successful evidence skips the file even when the initial run was
forced. A stale ownership-blocked epoch invalidates that evidence.

| Current file evidence at retry discovery | Forced retry | Non-forced retry |
|---|---|---|
| Valid same-metadata success on its own ancestor path | Skip | Skip |
| No valid ancestor success: failed, never reached, or changed metadata | Process, even if an older unrelated source-inventory success exists | Apply normal source-inventory policy; otherwise process |
| Valid unchanged source inventory, but no valid ancestor success | Process | May skip |
| Stale ownership-blocked success | Process | Process unless other valid evidence satisfies the normal policy |

In particular, a normal retry may skip files that its original run intentionally
skipped using valid source inventory. It is **not** a strict chain-only-download
policy. A forced retry must not mistake old unrelated inventory for successful
processing by its forced run. Metadata changes or failures are not rescued by an
older success with different metadata.

Retry discovery is distinct from recovering the **same job** after interruption.
Once recorded, its candidate set, metadata and `process`/`skip` actions stay frozen.
Recovery validates the same manifest, restores its stored decisions and terminal
file checkpoints, and restores committed event-derived counters without double
counting. It never changes a frozen `process` to `skip`, reopens a terminal file,
or reevaluates the old manifest after a source deletion. A new job is needed for
new ownership or metadata decisions.

## Durable Success Evidence

Migration 029 adds private nullable `observed_identity` and
`ownership_blocked_epoch` receipt columns to `app.source_files`. A guarded
`processing -> succeeded` transition stamps them in the file commit transaction,
using the admitted transaction's ingest-write capability and captured ownership
epoch plus the exact frozen manifest candidate. The real processed-byte
`checksum_sha256` remains required for reusable success. A live-owner skip records
the pre-decision epoch; mixed and all-blocked successful files retain that evidence.

Callers cannot supply or overwrite receipts. Existing terminal-file immutability,
manifest immutability and account/source/job/lease/runtime/cancellation guards
remain authoritative. Receipt identity and blocker evidence stay on the successful
file even when a later job moves the source inventory's `successful_job_id` pointer
or records a different blocker. An ancestor's durable proof therefore does not
depend on that latest inventory pointer. These columns are private implementation
evidence, not new fields in job/file API responses.

Historical receipts remain `NULL`, meaning unknown rather than unblocked success.
Reuse of a legacy ancestor success requires sufficient current inventory and
frozen-manifest proof: matching successful job/checksum, file metadata/identity,
and absent or current blocker evidence. If the inventory has moved or that proof
is unavailable, a safe additional download/process may be necessary. Do not
backfill invented evidence, rewrite frozen metadata, or promise that every
unchanged successful file from unsupported history always skips. Existing queued
historical jobs remain readable with their original effective policy.

Only a committed source live-to-deleted transition advances the account ownership
epoch. Adoption, disabling auto-sync, connection changes and credential refresh
do not. Stale blocked evidence is reconsidered by a fresh eligible job; source
deletion itself starts no sync. Force never permits stealing a live owner's
workout, crossing accounts, resurrecting a deletion or bypassing ambiguity checks.

## Metadata Is Not Content Integrity

iCloud skipping compares source-scoped remote object ID, size and modification
time. The `v2:rclone:` observed identity is a SHA-256 **of metadata**, including the
source namespace, not a SHA-256 of remote content. Local/NFS observed identity
retains its existing size, modification and filesystem-identity metadata rules.
No full remote download or content hash is performed merely to decide a skip.

The pinned [rclone iCloud v1.75.2 backend](https://github.com/rclone/rclone/blob/v1.75.2/backend/iclouddrive/iclouddrive.go)
advertises `hash.None` in `Hashes()` and returns `hash.ErrUnsupported` from object
`Hash()`. There is no free remote MD5 available through this backend. Files that
are actually processed receive a local SHA-256 while parsing their bytes. That
does not establish byte equality with a skipped remote object or provide an
independent remote MD5 integrity guarantee.

An overwrite preserving the same remote ID, size and modification time can go
undetected by default. Explicit reprocessing reads current bytes; it is the opt-in
way to revisit such files, not a promise that metadata detects all overwrites.

## Outcomes And Rollout

029's workout-based outcome rule is unchanged. An unsuccessful ingest with
zero created and zero updated workouts displays **Failed**, even when its failed
file count is zero: source discovery, authentication or other source-level work
can fail before a file fails. There is no new `failuresCount` API field; public
file failures use `JobProgress.filesFailed`, alongside `workoutsCreated` and
`workoutsUpdated`. Success-only empty runs remain successful, all-cancelled runs
remain cancelled, and unsuccessful runs qualify for partial success only through
created/updated workouts. Historical execution records and notifications are not
rewritten to achieve the display projection.

Ingest reprocessing and phase progress are included in migration **029**. The
final source contract requires schema/minimum runtime **30**, baseline **27**;
live reconciliation is pending here. Use the complete active **001-030** inventory
in `api/migrations/sql/`, not an ingest-only migration subset. See
[current migration requirements](database-migrations.md). The 030 read model
does not require new ingest/route jobs or provider checks for context warming.

For compatible source histories, the native command advances through **001-030**.
Source support, schema and minimum runtime become **30**; `migration_baseline`
remains **27**. The historical live ledger needs separate approved reconciliation,
not ordinary Goose Up. Take protected backups and retain matching old images,
stop/drain incompatible API and worker runtimes, apply the complete source contract,
and bring up coordinated compatible API/general-worker/Coverage-worker images
with the matching UI. Verify history, readiness and grants before resuming work.
No old/new API-worker rolling mixture is supported. An incompatible-image rollback
requires the matching database backup, not an image-only rollback or Goose Down.
See [database migrations](database-migrations.md#explicit-ingest-reprocessing-029).

Release verification must cover omitted/false/true bounded requests, incremental
presence rejection, coalescing isolation, forced and normal retry paths, durable
receipt independence from later inventory, conservative legacy fallback, stale
blockers, immutable frozen-manifest recovery, checkpoint/counter restoration and
workout/route deduplication. UI checks cover checkbox placement/default/visibility,
hover and keyboard-focus tooltip, and exact submitted fields. Disposable fixtures
and synthetic January 2025 ranges do not certify live iCloud or deployment.
