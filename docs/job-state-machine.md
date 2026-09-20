# Job State Machine

This document defines the durable job contract used by the API, worker, and
database schema. It elaborates the job architecture without changing the
observable behavior in `functional-spec.md`.

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
source. A coverage parent is created with one child per eligible stale workout
route at the target account/region work revision, matcher version, and OSM
generation. Either parent's status is derived from child statuses:

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
independent of administrator identity. Account coverage updates are single-flight
by account and region. Account deletion and source deletion use their own
lifecycle identity as the coalescing scope.

The link from an OSM update to a coverage parent does not use `parent_job_id`;
that relationship remains desired-state chaining rather than cross-owner job
hierarchy. Region and account/region context retain desired and applied OSM
generation watermarks plus a monotonic desired work revision that also advances
when route input changes under the same OSM generation. If either desired target
advances while coverage work is active, parent terminalization queues a successor
for the newer target.

## Coverage Route Work And Admission

`coverage_update` is one account-owned, single-flight parent per account/region
target. Its safe parameters identify the region, target OSM generation, desired
work revision, matcher versions, and traversal policy. Ingest advances desired
state and coalesces this parent in the same transaction that makes new route input
eligible for derived processing, then terminates independently of coverage
completion.

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
`coverage-route-timeout`; other retry reasons reset it. The worker applies
`min(configured timeout * 2^count, 10 minutes)`, allowing transiently slow routes
more time without raising the cost bound for sibling routes or initial attempts.

Synchronous coverage diagnostics do not create jobs. They retain their bounded
HTTP timeout, process-local gate, and fast `429 Retry-After` response, and also
acquire cluster-wide matcher admission for the requesting account and route's OSM
regions. Diagnostic admission is tied to the request lifetime and recovered after
abandonment. Diagnostics do not interrupt a running production route.

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
