# Legacy Coverage Route Repair

## Scope And Status

This runbook covers legacy routed workouts without coverage state. It is a
derived-data repair, not source ingestion, workout import/delete/purge or an OSM
rebuild. Choose only the owner account explicitly authorized for repair; do not
enumerate or wake unrelated accounts. Keep account names/IDs, workout/job IDs,
coordinates, account inventory, health counts and detailed workout dates out of
Git-eligible receipts. Use the authenticated owner interface for private job review.

As of 10/13/2026 (`America/Los_Angeles`), the deployed five-function repair preserves
schema/Goose version 20 and minimum runtime 18, with all function owners/ACLs intact. Map-data readiness
is resolved for the selected repair scope. First-pass matching included timeouts;
normal failed-only retries through the owner Jobs API are now **verified complete**.
All affected routes have current durable matches, with no remaining failed or
queued repair work. The supplied final verification receipt reports successful
completion (status `0`); private account identity, health counts and route/job
inventory are intentionally omitted. This is matching evidence, not just readiness, and does
not establish replica routing or concurrent-capacity acceptance.

The repair/retry verification gate is satisfied. M8 image builds, publication and
deployment have not started in this phase; no release tag is selected, and they
still require owner review and explicit authorization. M8 remains unchecked
pending owner acceptance. No external source profiles were registered, and no
host keyring or rclone config was
read. Earlier local image checks are historical, not new builds for this repair.

## Cause And Repair Contract

The reconciler's inner join to `workout_coverage_states` excluded routed workouts
with no state row. An account scan could therefore appear complete while those
routes remained unprocessed. The API mapped missing state to pending, leaving a
spinner without work queued to resolve it.

Forward-only [migration 022](../api/migrations/022_legacy_coverage_bootstrap.sql)
replaces five functions: `read_coverage_reconciliation_routes`,
`read_coverage_reconciliation_route`, `repair_coverage_reconciliation_input`,
`read_coverage_route` and `advance_coverage_reconciliation` in `app`.

- LEFT JOIN reads include missing state with revision `0` and NULL digest as a
  read-only sentinel. Neither value is persisted as initialized route state.
- Unchanged Go code computes the canonical route-input SHA-256. SQL independently
  verifies the current ordered points before initializing revision `1`; it never
  substitutes a geometry hash, fake digest or truncated-prefix digest.
- Account/lease/workout/state fences, NOWAIT lower-level locks, a bounded unique-key
  wait and clock-based checks before and after mutation prevent concurrent ingest
  or lease expiry from committing invalid state. Expired mutations roll back.
- SQL readers return up to 50,001 points, matching Go's 50,000-point acceptance
  bound plus one overflow sentinel. Oversized routes fail closed before prefix
  hashing or matching can corrupt derived results.
- Wakeup raises desired work without resetting a live lease's revision/cursor.
  An older scan's completion schedules newer desired work immediately rather than
  postponing it until the next periodic scan.
- Signatures, security-definer owners, account authorization and ACLs remain intact;
  no grants, helper fields or direct runtime access to private tables are added.

## Operator Procedure

1. Confirm explicit authorization for the selected owner account and separately
   for retrying its failed jobs. Record current schema/minimum runtime, Goose
   version, readiness and the five function owners/ACLs in a private receipt.
2. Snapshot only the five SQL function definitions plus their owner/ACL metadata
   using the authorized migration/operator role. Store files with mode `0600`
   under a private directory outside the repository; include no user table data.
   Temporary `/tmp/opencode` artifacts are not durable archives or backup evidence.
   Retain approved durable private copies of restore definitions and sanitized
   verification receipts before relying on them for rollback or recovery.
3. For the specifically approved schema-20 compatibility repair, apply **only**
   the five replacements between `BEGIN SCHEMA 20 COMPATIBLE FUNCTION PATCH` and
   `END SCHEMA 20 COMPATIBLE FUNCTION PATCH`. Exclude the migration's campaign
   INSERT, schema metadata UPDATE and Down block. Do not run Goose to 22, apply
   M8 source DDL, edit shipped migrations or manually advance Goose bookkeeping.
4. Recheck all five owners/ACLs, old-runtime readiness and unchanged schema/Goose
   20/minimum runtime 18. Separately wake only the authorized account's campaign,
   preserving any live lease/cursor. Do not use the full migration's eligible-account
   wakeup as a substitute for this account-scoped operational action.
5. Let normal reconciliation initialize canonical input, resolve readiness against
   active configured public regions and enqueue ordinary coverage jobs. Inspect
   readiness separately from processing/result status; raw routes stay private
   and no raw geometry or private metadata belongs in public diagnostics.
6. Explicitly retry eligible failed/partially successful coverage parents through
   the owner Data Sync Retry action or `POST /api/jobs/{jobId}/retry`. Follow the
   latest-only, non-forking retry chain; do not manually rewrite child states or
   retry successful children. Superseded/cancelled targets retain normal eligibility
   rules. Observe each new attempt until terminal outcomes and durable results
   are verified; pending retries do not satisfy the M8 deployment gate.

The recorded operation keeps the three-minute configured base route timeout,
resources and global/account/region matcher limits at `2/2/2` physically unchanged.
Existing per-child timeout-retry policy gives timeout `RetryCount=1` six minutes
and the second timeout retry ten.
Other failure retries do not automatically receive this escalation. This is normal
selective retry, not a global timeout increase or automatic retry loop.
The existing runtime derives each route-child budget from its timeout-retry
lineage; retry parent/child behavior does not rewrite Helm or account/region limits.

## Reader-Only Follow-Up

[Strict OSM reader routing](osm-read-replicas.md) is separately implemented and
off by default. The completed repair used normal matching/retry behavior; it is
not a deployed reader-feature or two-backend load-balancing test. Live headless
DNS and direct-backend TLS/authentication probes remain pending at this checkpoint.
Do not enable the flag, add worker replicas/threads, change shared HA provisioning
or increase matcher limits merely because repair completed. A newly evaluated
reader rollout starts with monitored capacity one before an explicitly approved
increase to two; the repair's existing maximum of two is not a benchmark pass.

Before an authorized M8 rollout, review README and select coordinated API/worker
images supporting schema 22/minimum runtime 21. Normal Goose then installs 021
and 022; the reader feature itself adds no migration. Private reader Secret keys
must not replace writer keys used by OSM hooks, timezone imports or operator Jobs.

## Verification And Recovery

The recorded 10/13 disposable PostGIS verification covers 27 test cases in each
mode: full migration to schema 22/minimum runtime 21 and the marked function-only
patch on schema 20. Fresh schema-22 readiness passes; deployed old-runtime
readiness remains preserved. These scoped receipts are distinct from the
[10/12 schema-21 M8 results](implementation-plan.md#verification-results-10122026)
and do not establish a new whole-suite, image or live-cloud pass.

The version-controlled full migration is the durable forward repair. When a later
M8 rollout is authorized, normal Goose migration through 021 and 022 installs
schema 22/minimum runtime 21 and wakes active accounts with missing state, without
resetting live cursors. Current code supports schema 22; runtime 20 is not a valid
rollback target after the M8 minimum-runtime fence. Migration 022 rejects Down.

Before restoring a database or saved function definitions, check its schema and
minimum runtime against the intended binary. A restored schema-20 snapshot may
need the approved compatibility block again; a schema-22 restore must retain the
forward repair and minimum-runtime-compatible code. Never lower metadata or undo
owner/ACL protections to make readiness pass. Retain final retry outcomes privately;
update public progress only after verification, without publishing account counts.
