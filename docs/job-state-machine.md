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
| `osm_bootstrap` | Administrator | Standalone | 20 |
| `osm_refresh` | Administrator | Standalone | 20 |

Account deletion records sanitized administrative progress and never transfers
private job diagnostics to administrator ownership. Parent ingest jobs are not
claimed by workers; their state is derived transactionally from their children.

Priority values are persisted so later job kinds can be ordered without a
schema change. Within one priority, workers prefer the oldest eligible job and
use UUID as a deterministic tie-breaker.

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
source. Its status is derived from child statuses:

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
- Retried source children snapshot the current validated source configuration.
- Immutable deletion targets are copied from the failed deletion job.
- Retry history remains navigable in both directions.

Only terminal failed or cancelled work may be retried, and authorization is
rechecked when the retry is requested.

## Active-Job Coalescing

Commands compute a versioned canonical parameter representation and a SHA-256
coalescing key. Parameters contain only safe identifiers and normalized values,
never source credentials or private payload data.

At most one `queued` or `running` job may exist for an ownership scope, kind,
and coalescing key. Cancellation intent does not make a job inactive; equivalent
requests return the existing job until it is terminal. The database unique
constraint is authoritative so concurrent API replicas cannot enqueue duplicate
work.

OSM bootstrap and refresh share a single-flight scope even though they are
different kinds. Account deletion and source deletion use their own lifecycle
identity as the coalescing scope.

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
records. Encrypted source snapshots are separate records introduced with source
ingest and are cleared at every terminal outcome.

## Invariants And Verification

- A child has exactly one parent and the same account ownership as that parent.
- Only parent kinds may have children; only matching source-child kinds may use
  an ingest parent.
- `partially_succeeded` is valid only for a parent.
- Terminal jobs have no live lease and cannot transition again.
- Running claimed work has complete lease fields.
- A stale lease token cannot heartbeat, commit domain work, or finish a job.
- Account-private parameters, events, and logs follow ADR 0002 tenant scope.
- Administrative job responses never expose private account diagnostics.
- Coalescing remains correct under concurrent transactions.
- Parent derivation covers every combination of child terminal outcomes.

Migration and integration tests exercise every legal transition, reject illegal
transitions, simulate lease expiry and stale-worker fencing, race equivalent job
creation, and verify parent status derivation exhaustively.
