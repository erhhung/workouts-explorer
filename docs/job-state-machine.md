# Job State Machine

This document defines the durable job contract used by the API, worker, and
database schema. It elaborates the job architecture without changing the
observable behavior in `functional-spec.md`.

Active migrations remain **001-028**, with **no active 029**. New 028 folds the
generation-lock fix, strict eligible cohorts and mixed-failure automatic policy
into the pipeline. Source `SupportedSchemaVersion`, schema and minimum runtime
are **28**, marker **27**. The folded contract is **deployed in `dev-20261020`,
Helm revision 29**, schema/Goose/minimum 28, marker 27;
[Development Progress](development-progress.md) owns receipts.

Ordinary static 027 and 001-027 stay unchanged. The native CLI validates history
read-only before Goose Up; canonical 27 applies new 028 without replaying 027.
Native Goose does not replay an applied 028. Old-contract 28 needs a new private
guarded offline repair, explicit operator approval and protected actual-backup
restore rehearsal under the actual migration role with new source/catalog proof.
Pause all old API/ingest/Coverage runtimes, and tiles if DDL affects their app
constraints. Old apply-10/defer-6 ABIs are intentionally absent: old runtime-28
readiness fails and new runtime-28 readiness requires private builders. Numeric
28 does not fence feature behavior or permit rolling reuse of old images. Keep
ledger 0-28 IDs/timestamps without DML/rewrite, data, retry roots/budgets, sources
and credentials; retain backups/images. Incompatible-image rollback requires the
full old database restore plus matching old OCI images. The approved exact
actual-profile repair passed protected actual-backup rehearsal and live
application under the real non-superuser migration login; new readiness and
full old-tuple/ledger preservation passed. See [migration recovery](database-migrations.md).

## Job Kinds And Ownership

| Kind | Ownership | Hierarchy | Default priority |
|---|---|---|---:|
| `source_connection_check` | Account | Standalone | 100 |
| `workout_deletion` | Account | Standalone | 100 |
| `account_deletion` | Administrator | Standalone | 100 |
| `manual_ingest` | Account | Parent | 80 |
| `manual_ingest_source` | Account | Child | 80 |
| `scheduled_ingest` | Account | Parent | 60 |
| `scheduled_ingest_source` | Account | Child | 60 |
| `osm_region_update` | Administrator | Standalone | 20 |
| `coverage_update` | Account | Parent | 20 |
| `coverage_update_route` | Account | Child | 20 |

Account deletion records sanitized administrative progress and never transfers
private job diagnostics to administrator ownership. Parent ingest and coverage
jobs are not claimed by workers; their state is derived transactionally from
their children.

Priority values are persisted so later job kinds can be ordered without a
schema change. Within one priority, general workers prefer the oldest eligible
job and use UUID as a deterministic tie-breaker. Coverage-route claims first
choose the least-recently-served active parent, then its oldest eligible child.

## Statuses

| Status | Terminal | Meaning |
|---|---|---|
| `queued` | No | Eligible for a worker claim or waiting for children |
| `running` | No | Claimed under a live lease or has at least one active child |
| `succeeded` | Yes | Completed successfully, including a no-data result |
| `partially_succeeded` | Yes | Parent only: at least one child succeeded and another failed or was cancelled |
| `failed` | Yes | Work failed, or no parent child succeeded and at least one failed |
| `cancelled` | Yes | Cancelled before completion, with no successful parent child |

Cancellation intent is stored separately as `cancel_requested_at` and
`cancel_requested_by`. It is not a status because a running job remains owned by
its lease holder until it reaches a safe cancellation boundary.

## Standalone And Child Transitions

Allowed status transitions are:

```text
queued  -> running
queued  -> cancelled
running -> queued
running -> succeeded
running -> failed
running -> cancelled
```

`running -> queued` occurs only when an expired lease is recovered. Recovery
clears the old lease, records a safe event, and leaves the job eligible for a new
claim. A status never leaves a terminal state.

A queued cancellation atomically becomes `cancelled`. A running cancellation
sets cancellation intent; the lease holder performs cleanup and then records
`cancelled`. Completion may win a race with cancellation only if the domain
transaction committed before cancellation was observed. Every terminal path
records one terminal timestamp and performs required snapshot/staging cleanup.

## Parent Derivation

An ingest parent is created in the same transaction as one child per selected
source. A Coverage parent is created atomically with its eligible route children;
new normal ingest cohorts may mix regions and use each child's complete target
vector. Private prepared metadata is not a parent job. Either parent's status is
derived from child statuses:

- `queued` when every child is queued;
- `running` when any child is running, or when queued and terminal children are
  mixed;
- `succeeded` when every child succeeded;
- `partially_succeeded` when at least one child succeeded and another failed or
  was cancelled;
- `cancelled` when no child succeeded and every child was cancelled; and
- `failed` when no child succeeded and at least one child failed, including a
  mix of failed and cancelled children.

Parent progress is the aggregate of persisted child counters. Parent terminal
state and notification creation occur in the same transaction that makes the
last child terminal. Parent cancellation immediately cancels queued children and
sets cancellation intent on running children. Completed child work is not
rolled back.

For coverage, a successful no-evidence match is still a succeeded child. A child
whose target was superseded before commit also terminates successfully with a
`superseded` result because it did not fail matching; desired-state advancement
ensures a successor parent covers the new revision. One failed route does not
cancel siblings or prevent another route child from being claimed. A mixture of
succeeded and failed or cancelled routes therefore makes the coverage parent
`partially_succeeded`; all failed routes make it `failed`.

## Claim, Lease, And Fencing

Workers claim eligible standalone or child rows with locking equivalent to
`FOR UPDATE SKIP LOCKED`. A successful claim atomically:

1. changes `queued` to `running`;
2. increments `attempt`;
3. assigns a random lease token and worker identity;
4. records lease acquisition and expiry timestamps; and
5. appends a safe claimed event.

Heartbeats extend a lease only when job ID, worker identity, lease token, and
current `running` status all match. Domain commits and terminal transitions use
the same fencing predicate so an expired worker cannot commit after another
worker recovers the job.

Lease recovery is allowed after expiry plus a configured safety interval. It
clears claim fields and returns the job to `queued`; it does not create a new job
or reset `attempt`. Attempts count executions of one durable job, including
recovery after worker interruption.

## Ingest Ownership And File Recovery (Requires 26)

The approved source-adoption contract requires schema/minimum runtime **26**
through migrations 024-026. Live-version and
operational evidence belong in [Development Progress](development-progress.md),
not this contract or historical M8 test receipts.

`app.fence_ingest_job(job_id, claiming_worker, current_lease_token,
runtime_version)` establishes the source/job/lease and captures the account's
`workout_ownership_epoch` before per-workout ownership decisions.
`app.resolve_ingest_workout_identity` returns the existing/proposed UUID, prior
content digest and `created`, `existing`, `adopted`, `skipped_live_owner` or
`suppressed` outcome. Provider identity is account plus `health-auto-export`
namespace plus opaque provider ID, shared by local/iCloud transports. Same-source
content compares normally; another non-deleted owner blocks writes even when
disabled/disconnected. A single deleted-source match can transfer current
source/file ownership under the same UUID, with authoritative incoming content
and append-only historical provenance. Ambiguity fails closed; deletion
tombstones suppress resurrection. ID-less fallback remains source-local and is
not auto-adopted. See [identity rules](architecture.md#provider-identity-and-source-adoption-requires-26).

The source remains a soft-tombstoned row, not a NULL workout source; source type
is API-immutable. The account ownership epoch advances **only** on a committed
live-to-deleted source transition, not adoption, auth/config generation, cookie
refresh, disabling auto-sync or connection-status change.

File inventory and recovery follow these invariants:

- `app.record_successful_source_object` runs after all import events in the file
  write transaction. Retain `successful_checksum`; if any workout was
  `skipped_live_owner`, record `ownership_blocked_epoch` from the pre-decision
  fence, not a later account epoch. Mixed files retain both successful writes and
  blocked evidence; an all-blocked file can still succeed.
- For a fresh incremental job, `app.read_ingest_candidate_actions(job, worker,
  lease, candidates)` skips unchanged successful metadata when the blocker is
  absent or its epoch is current, without reread/download. A stale blocked epoch
  selects `process` even for identical metadata and bytes. Reprocessing may adopt
  an eligible orphan or record remaining blockers at the new captured epoch.
- `app.record_ingest_file_manifest` freezes the candidate set, exact metadata and
  actions for that job. Recovery restores those actions instead of reevaluating
  them after deletion, while still rejecting metadata/set mismatch. A completed
  file checkpoint never reopens, and committed event-derived counters and already
  counted skips are restored without double counting.
- Source deletion cancels only work using that source and does not enqueue sync
  for another. Ownership reconsideration needs the next auto-sync or a **new**
  manual job, not recovery of an old frozen job. Bounded Manual Sync already
  processes every eligible file regardless of successful checksum.
- `adopted` contributes to Updated and `skipped_live_owner` to Unchanged in both
  immediate and recovered totals. The distinct owner-private import event remains
  authoritative; Unchanged here does not imply equal payloads. No new public job
  status is introduced.

Every successful source-switch adoption calls
`app.record_adopted_workout_coverage_refresh` in the same file transaction as
ownership/content/event and successful inventory. It writes the durable
`app.adopted_workout_coverage_refreshes` outbox, never takes Coverage state locks
or enqueues matching inside identity resolution. An outbox failure rolls back
the file rather than losing the obligation.

In schema 28, unified postcommit `replayIngestCoverage` drains normal and adopted
obligations through the fenced batch/snapshot/apply interfaces described below.
The existing adoption interfaces retain their protected ABI; they are not an
ordinary-import force-refresh bypass. A successful adoption requests fresh routed Coverage even for an
unchanged canonical digest; no fake hash/revision change forces work. Event-token
comparison prevents an old consumer from clearing a newer adoption. Missing map
data retains a deferred request; deleted/no-route workouts clear without matching.
Recovery can discover committed outbox work independently of the original ingest
finishing. This does not reopen terminal ingest files/jobs or count their results
again. Normal route-child fencing and matcher admission still apply.

### Generation Lock Timing (Folded 028, Undeployed)

The file fence removes only the generation-row `FOR UPDATE NOWAIT`; the
nonlocking existence check still fails with `55000` if the row is missing.
Identity advisory locks, source/job locks, account/workout KEY SHARE, lease,
ownership epoch and runtime-capability fences are preserved. Four existing
generation triggers become same-name `DEFERRABLE INITIALLY DEFERRED` constraint
triggers, keeping exactly their existing events, UPDATE OF columns and WHEN
predicates. Account seeding remains immediate; each qualifying event still
increments, not each GPS point or just once per transaction. Coverage scope
remains migration 017's changed `current` finish only, not queued/failed changes.

Production does not flush constraints early, although PostgreSQL permits
`SET CONSTRAINTS ... IMMEDIATE`. Commit-time data and generation are atomic;
injected trigger failure rolls back file/checkpoint and Coverage evidence,
state/counters/progress together. Commit tails can still serialize. This adds no
worker transaction retry, deadline allowance or additional automatic job retry.
Map selection uses the historic `lock_account_data_generation()` ABI as a
nonlocking read of the same Repeatable Read snapshot as selected data. Concurrent
commit can make its response already stale; existing current-generation checks
reject it without mixing old data and a new generation or holding a generation
lock before account/workout/focused-membership FKs.

### Committed-Route Pipeline (Requires 28)

- The file transaction records created/updated routed import origins in
  `ingest_coverage_handoffs` and adoption origins/force obligations with durable
  `adopted_coverage_handoff_receipts`. Acknowledgements retain provenance and actual
  Coverage links after consumption; replay does not recount file results.
- The source runner downloads/processes files serially within its job, with
  cleanup before and after the file pipeline. Postcommit transport/slot cleanup
  precedes handoff; pipeline work never overlaps an open file transaction.
  `worker.coverageBatchSize` / `WORKER_COVERAGE_BATCH_SIZE` defaults to B=25,
  validates 1-1000, and requires **B new eligible matching targets** during active
  ingest, globally across regions in the ingest-root/source-sibling scope.
  Normal/adopted obligations share this cohort. Missing maps, current/live,
  unchanged authoritative failed/cancelled targets and deduplication do not count.
  A 63-eligible-route file may seal 25/25 during ingest; 13 await terminal recovery.
- Metadata-only `read_ingest_coverage_batch` precedes one-route-at-a-time
  `read_ingest_coverage_route_snapshot`. Protected STABLE snapshots bind event,
  content, ownership epoch, complete canonical digest and compact coordinates.
  APP transactions close before OSM work; only metadata/CAS/readiness is retained
  across routes in bounded private prepared rows, never an unbounded route payload
  or metadata collection. ROI lookup uses one efficient query per route, not per
  point. The raw domain is 0-250,000 points, including the canonical empty
  digest, not a truncated matcher prefix. Ordinary reconciliation uses the same
  full-domain compact snapshot. Matching above 50,000 original points returns a
  typed permanent Coverage failure, never a source-ingest failure.
- A trusted fingerprint covers **all catalog regions, configuration, boundaries
  and active heads**, in the same Repeatable Read snapshot as ROI lookup and
  revalidated before seal. Mismatch invalidates readiness for fresh ROI; it must
  not substitute newer generation heads into an old lookup. Separate APP/OSM
  snapshots are not a cross-database latest-state atomicity guarantee.
- Prepare/seal atomically rechecks current source/account,
  job/parent cancellation, lease/runtime, event, epoch, content and full digest.
  Account-first, sorted active-parent/child NOWAIT locks precede workout/state
  locks. Recognized contention retries are bounded to three full attempts with
  fresh metadata/OSM reads and backoff; stale readiness is not acknowledged.
- Private prepared cohort metadata persists across files/crashes, with saved B
  and root/source-sibling scope. No placeholder Coverage parent exists before B.
  `seal_ingest_coverage_cohort` creates the full-B parent and children atomically.
  SQL retains saved B as `parent_batch_size` and counts every child, including
  terminal/superseded children. Configuration changes do not resize saved cohorts.
  Unavailable normal work acknowledges without a matching job, not as failed
  children of a hidden parent, and cannot shrink an active-ingest parent below B.
- Mixed regions are explicitly allowed. Optional `coverage.regionIds` contains
  the actual child-region union, including retries. `regionId`/`targetGeneration`
  remain representative anchors, not complete matching scope. Each child retains
  its full generation vector; per-route claim fencing and admission are unchanged.
- Retry construction copies only eligible failed/cancelled predecessors and
  inherits capacity and lineage. Spare retry capacity never accepts normal work:
  three eligible failures under B=25 stay a three-route retry; 25 fresh routes
  use a normal root. Unchanged normal/current/failed work cannot reset budgets.
  Only a distinct proven adoption event can force otherwise identical work.
- Leased account reconciliation recovers committed obligations from succeeded,
  failed or cancelled ingest roots without reopening them. A **final partial**
  seals only after the visible ingest root and **all source siblings are terminal**
  and due preparation is exhausted, never at active EOF. Crashed active claims
  first follow normal lease recovery; active underfull/deferred cohorts stay
  protected. Committed-data obligations from failed/cancelled roots are legitimate;
  normal metadata updates/deduplication never resurrect cancelled route targets
  or erase cancellation footprints. Existing Coverage jobs need their own manual
  cancellation; stopping ingest does not erase derived obligations.
- Only typed verified transient readiness errors persist a one-hour deferral
  without acknowledgement. The classifier examines all wrapped/joined branches
  with a 256-node bound; permanent/unknown/cancelled branches defeat incidental
  deadline/network evidence and surface safely. No-GPS adoption acknowledges
  without a job. Normal missing maps acknowledge readiness without matching;
  adopted missing maps retain a deferred force request.

The pipeline preserves `coverage-production-v1`, 500m CPU / 256 MiB, two lanes,
admission 2/2/2, one slot per reader and existing timeout/automatic retry budgets.
These invariants and synthetic concurrent tests are not capacity certification.

## Retry

User-requested retry creates a new job linked by `retry_of_job_id`; it never
returns a terminal row to `queued`.

- Retrying an ingest parent creates a new parent containing only failed or
  cancelled source children that support retry.
- Retrying a failed or partially successful coverage parent creates a new parent
  containing only failed or cancelled workout routes that remain eligible. Each
  new child captures the route's current revision and current matcher/OSM target
  rather than replaying stale immutable matcher input.
- Retried source children snapshot the current validated source configuration.
- Immutable deletion targets are copied from the failed deletion job.
- Retry history remains navigable in both directions.

Only terminal failed or cancelled work, or a partially successful parent, may be
retried, and authorization is rechecked when the retry is requested.

Retry lineage is linear and capped by the existing maximum ordinal. A retry may
be created only from the latest parent in a lineage, preventing forks. The
original parent is ordinal 1 (`First`); each selective retry points
`retry_of_job_id` to its immediate predecessor, retains the same retry-root ID,
and receives the next ordinal (`Second`, `Third`, and so on). Parent Run detail
exposes the complete ordered lineage from first through latest and permits direct
navigation by ordinal, with the current parent identified.

Each retried coverage-route child points to the corresponding failed or cancelled
child from the preceding parent and inherits its new parent's retry root and
ordinal. Successful and superseded route children have no child in a selective
retry. This preserves per-workout attempt history while parent navigation remains
the primary first/second/.../latest Run-detail control.

### Current Retry Eligibility And Superseded Presentation

Stored execution state describes the attempt that actually ran. A Coverage
parent stored as `failed` or `partially_succeeded` has the derived display status
`superseded` only when all historically failed/cancelled route children have
proven current Coverage. The parent/history label is green **Superseded**; each
child retains its actual Failed/Canceled badge. No status, result, progress,
timestamp or retry edge is rewritten to achieve that presentation.

Current proof uses the authoritative succeeded child and its parent/route
context, current route revision/digest, applied generations/versions, current
matcher contract and visible non-deleting workout/source. A replacement may
have a different input/generation/policy from the historical attempt. Its parent
may still be running or partially successful because other routes have not
finished. Missing or mismatched proof, or a replacement route still queued,
running or failed, does not permit green Superseded presentation. Cancelled or
successful parent execution states are not relabeled by this rule.

One current target per route does not mean one historical chain forever.
Different target descriptors may create independent chains; a multi-route parent
may have its routes completed in separate newer parents. Current-Coverage links
are advisory supersession navigation, not `retry_of_job_id` ancestry. Neither a
fork nor a false linear relationship may be introduced just to hide old failures.

The read-only eligibility contract derives the active owner account, reuses the
private authoritative target predicate and returns route counts plus bounded
owned current-parent links. It disables unsupported retry with a safe reason;
proven all-current history takes precedence over latest/ordinal presentation
warnings. This metadata is not authorization: POST still locks and recomputes
current eligibility before creating an attempt. Conflict types distinguish
already-current, no-eligible-target, newer-attempt, retry-limit and temporary
contention cases without exposing database messages.

History filters operate on derived display status before totals and pagination.
Unfiltered and unaffected-status requests count/page first and evaluate proof
only for relevant page rows; failed/partial/superseded filters classify their
relevant candidate set first. History, including empty filtered pages, and terminal
Coverage details refresh because unrelated work can change eligibility,
Superseded presentation and totals without changing the historical row. A known
blocking conflict cannot be cleared by a failed refresh
that leaves stale cached eligibility; busy contention remains retryable.

### Cause-Aware Automatic Coverage Retry

`coverageWorker.autoRetryCount` /
`COVERAGE_WORKER_AUTO_RETRY_COUNT` defaults to one **additional** automatic attempt
per logical retry root; valid values are 0-20 and zero disables automation.
Increasing the value permits additional attempts up to the new total without
resetting the durable used counter. Lowering it does not cancel already-created
jobs. The existing overall retry ordinal limit remains in force.

- At least one authoritative failed child must have a timeout or explicitly
  verified transient database-boundary cause to trigger automatic retry. Generic
  map/persistence, unknown/null, deterministic limits, input, authentication,
  permissions, schema/configuration and TLS failures are not themselves transient.
- With that trigger, the new chained attempt carries **all authoritative FAILED
  siblings**, including permanent, unknown and null-code failures, provided all
  failed descriptors remain valid. It must not retry only the transient subset
  and strand other failures behind a non-latest parent. Successful/superseded
  siblings never rerun; cancelled siblings are not automatically revived.
  A parent with only permanent/unknown failures stops automatically. Latest-only
  authority and non-forking construction remain required for every descriptor.
- A 30-second cooldown, parent/target revalidation and durable root budget prevent
  duplicate creation and unbounded descendant retries. Manual retry is separate
  and remains authorized after automatic exhaustion, including cancellation;
  manual creation does not reset the root's automatic budget.
  One additional automatic attempt is a root-wide budget, not renewed on its
  automatic successor: a failed automatic second attempt cannot create an
  automatic third with the default count of one.
- Historical terminal failures are not enrolled by migration. Only new failure
  transitions enter the policy. Ordinary reconciliation cannot silently create
  fresh roots for identical failed/cancelled targets; genuine desired-work changes
  and fresh adoption events remain distinct work.
- Automatic retries use the same timeout-counter logic as manual retry. Only a
  timeout predecessor advances the 6/10/15-minute budget; other causes do not.

Routed ingest counters (schema 27+) snapshot route presence with the committed
import event. Created routes contribute to Imported; updated and adopted routes
contribute to Updated, including unchanged adoption. Unchanged/live-owner skips
do not increment either. Recovery derives these counters from committed events;
unknown legacy evidence remains nullable. The UI displays a known count as, for
example, `12 (10 routes)`, and does not invent a route count for unknown history.

## Active-Job Coalescing

Commands compute a versioned canonical parameter representation and a SHA-256
coalescing key. Parameters contain only safe identifiers and normalized values,
never source credentials or private payload data.

At most one `queued` or `running` job may exist for an ownership scope, kind,
and coalescing key. Cancellation intent does not make a job inactive; equivalent
requests return the existing job until it is terminal. The database unique
constraint is authoritative so concurrent API replicas cannot enqueue duplicate
work.

OSM region updates are globally single-flight by provider-qualified region ID,
independent of administrator identity. New ingest Coverage uses durable private
builders across regions in an ingest-root/source-sibling scope, then seals a new
normal parent atomically; retry parents stay isolated from fresh work. Account
deletion and source deletion use their own
lifecycle identity as the coalescing scope.

The link from an OSM update to a coverage parent does not use `parent_job_id`;
that relationship remains desired-state chaining rather than cross-owner job
hierarchy. Region and account/region context retain desired and applied OSM
generation watermarks plus a monotonic desired work revision that also advances
when route input changes under the same OSM generation. If either desired target
advances while coverage work is active, parent terminalization queues a successor
for the newer target.

## Coverage Route Work And Admission

`coverage_update` is an account-owned, capacity-bounded parent. New ingest cohorts
may mix regions; safe `regionId`/`targetGeneration` parameters are representative
anchors and optional `coverage.regionIds` reports the actual child-region union,
including retries. Matching scope always comes from the child's full vector, not
the representative anchor. Schema-28 ingest records obligations in the file
transaction, then prepares and seals normal parents after commit and cleanup.
Ingest terminates independently of Coverage completion.

The parent creates one `coverage_update_route` child per eligible stale workout.
Child parameters identify the workout, route revision and digest, matcher
versions, traversal policy, and complete target OSM generation vector. Only the
dedicated coverage-worker deployment claims route children. Each claim matches and
commits exactly one route, so progress is durable at every route boundary and a
worker crash or route failure cannot discard sibling results. Coverage claim order
is fair across active account/region parents rather than draining one large parent
before another can advance. Cancellation and lease checks occur between matcher
windows as well as before persistence.

Before expensive work, a coverage-route lease holder acquires PostgreSQL-coordinated
matcher slots. Limits are global, per account, and optionally per OSM region; the
default permits one matcher route globally across production and diagnostics and
one active production route for an account. Slot ownership is fenced by route
child job ID, worker identity, and lease token and is released on commit,
cancellation, failure, lease recovery, or expiry cleanup. Worker
replicas cannot raise effective concurrency above the database limits.
Coverage route claim and terminal transitions append owner-safe Events and Logs
transactionally. Parent detail aggregates those child records, so applied,
no-evidence, failed, and superseded outcomes remain observable without exposing
route coordinates, digests, or internal database errors.
Each route child persists a timeout-retry count. A selective retry increments it
only when that route's immediately prior child failed with
`coverage-route-timeout`; other retry reasons reset it. The current source policy
defaults to six minutes initially, ten minutes on the first timeout-only retry,
and fifteen minutes on the second and later retries. `routeTimeout` configures
only the initial budget: count 0 uses `min(base, 15 minutes)`, count 1 uses
`min(max(base, 10 minutes), 15 minutes)`, and count 2 or higher uses fifteen
minutes. Retry floors are fixed, not exponential; a longer initial budget is not
shortened by the first retry (for example, a twelve-minute helper input stays
twelve minutes). Runtime configuration still permits initial budgets only from
five seconds through ten minutes and requires `base + 10 seconds < lease`.
The default seven-minute lease is a renewable ownership TTL, not a match timeout;
unchanged twenty-second heartbeats allow ten-/fifteen-minute matches to outlast
that TTL. Adoption retains the 500m Coverage CPU, two execution lanes, existing
admission caps and 6m/10m/15m policy; it authorizes no capacity increase. Recorded
earlier runs retain their original three-/six-/ten-minute budgets. See
[Development Progress](development-progress.md) for deployed policy evidence;
neither that evidence nor this contract certifies a live iCloud smoke test.

Synchronous coverage diagnostics do not create jobs. They retain their bounded
HTTP timeout, process-local gate, and fast `429 Retry-After` response, and also
acquire cluster-wide matcher admission for the requesting account and route's OSM
regions. Diagnostic admission is tied to the request lifetime and recovered after
abandonment. Diagnostics do not interrupt a running production route.

### Matching Limits And Adaptive Recovery

Each route child and diagnostic uses the same matching policy: at most 50,000
original GPS points and **12,000 sampled points per route**, inclusive. These
processing limits do not restrict raw-ingest storage, and import completion does
not certify Coverage completion. Overflow is explicit, not silent truncation.
The complete bounds are defined in the
[shared matching contract](architecture.md#shared-matching-contract).

Within one claimed route, typed graph-node/edge exhaustion may subdivide a normal
128-point window to 64, then 32 points. This is in-attempt recovery: it neither
creates another job nor consumes an automatic retry ordinal, renews the matching
deadline, releases/reacquires admission, or increases graph bounds. Accepted
windows overlap by one point and failed larger attempts publish no counts or
evidence. Supplemental seam matching preserves continuous supporting traversals.

If the minimum window still fails, the child reports a processing-limit failure
and does not commit an incomplete replacement. It is not a temporary map outage
and is not automatically retryable merely because it was mislabeled previously.
Existing valid Coverage remains available until a valid replacement commits.

Diagnostic HTTP expiration remains an inconclusive request outcome, not a verdict
that production would exhaust its separate worker budget. Completed diagnostics
attest matching only under the same effective input, generations and parameters;
they do not attest APP persistence or capacity under concurrent production load.

A route result commits only when the live route-child lease, target route revision,
matcher version, and target OSM generation vector still match. A mismatch records
or preserves stale state, produces a `superseded` child result, and cannot replace
current coverage. A terminal child updates parent counters transactionally:
routes total, processed, succeeded, failed, cancelled, and superseded. The parent
also retains bounded safe failure categories; route coordinates never appear in
job parameters, events, summaries, or logs.

`routes_processed` counts succeeded, failed, and superseded children; routes
cancelled before completion remain separate. `routes_succeeded` includes valid
no-evidence results but excludes superseded work. The parent Run detail may list
each child by workout ID plus the already-authorized workout date/type summary,
status, attempt, duration, and safe failure category, but never raw route points or
coordinates.

A parent advances the applied account/region OSM generation, desired work
revision, and matcher version only when every route child applied successfully,
none was superseded, and desired state still equals the parent target. A parent
with superseded, failed, or cancelled children records its attempted target but
does not advance the fully applied watermark. Failed-target state suppresses
automatic hot-loop retries; a user retry or a newer desired work/OSM target creates
the next parent. If desired state advanced during execution, terminalization
atomically queues a successor even when every child for the older target otherwise
succeeded.

## Durable Fields

The foundational job record supports:

- UUIDv7 identity and optional parent identity;
- account or administrative ownership, with exactly one ownership mode;
- kind, priority, status, and immutable safe parameters;
- versioned coalescing key;
- attempt and progress counters;
- cancellation requester and timestamp;
- worker identity, lease token, claim, heartbeat, and expiry timestamps;
- originating request and trace correlation identifiers;
- retry and cancellation lineage;
- creation, start, terminal, and update timestamps; and
- a bounded safe failure code and summary.

Detailed safe events and owner-authorized diagnostic logs are separate append-only
records. Encrypted source snapshots are separate records for connection checks
and source ingest. Source generation capture, independent snapshot encryption,
and job creation are one transaction. A snapshot is immutable and can be read
only by the matching running lease holder in an account-scoped transaction. Its
complete row is deleted in the same transaction as every terminal outcome.

## Invariants And Verification

- A child has exactly one parent and the same account ownership as that parent.
- Only parent kinds may have children; source-child kinds use the corresponding
  ingest parent and coverage-route children use a coverage parent.
- `partially_succeeded` is valid only for a parent.
- Terminal jobs have no live lease and cannot transition again.
- Running claimed work has complete lease fields.
- A stale lease token cannot heartbeat, commit domain work, or finish a job.
- A terminal connection-check or source-child job retains no config snapshot.
- Account-private parameters, events, and logs follow ADR 0002 tenant scope.
- Administrative job responses never expose private account diagnostics.
- Coalescing remains correct under concurrent transactions.
- Parent derivation covers every combination of child terminal outcomes.
- General workers never claim coverage-route work, coverage workers claim only
  coverage-route children, and no worker claims a coverage parent.
- Multiple worker and API replicas cannot exceed global, account, or region matcher
  slot limits.
- Coverage child completion preserves parent progress, fair cross-parent claiming,
  lease fencing, and coalescing identity when sibling routes fail.
- A stale route revision, matcher version, or OSM generation cannot replace current
  coverage.
- Coverage parent status and route counters remain correct for every combination
  of succeeded, failed, cancelled, and superseded children.
- Coverage retry lineages are linear, ordinal navigation reaches every parent from
  first through latest, and each retry contains only the preceding parent's
  eligible failed or cancelled route children.

Migration and integration tests exercise every legal transition, reject illegal
transitions, simulate lease expiry and stale-worker fencing, race equivalent job
creation, and verify parent status derivation exhaustively.
