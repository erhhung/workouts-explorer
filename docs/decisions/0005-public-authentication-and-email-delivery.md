# ADR 0005: Public Authentication And Email Delivery

## Status

Proposed

This ADR must be accepted before Milestone 2 public authentication endpoints are
implemented.

## Context

Invitation, signup, signin, and password recovery are public operations. They
must resist account enumeration, token replay, brute force, and SMTP failure.
The deployment may run multiple API replicas, while ADR 0001 intentionally
avoids Valkey and external message-broker infrastructure.

The architecture currently assigns SMTP to the API. Invitation delivery must
leave retryable state after SMTP failure, and password recovery must not reveal
whether an account exists or whether delivery succeeded.

## Proposed Decision

### Canonical identities

Preserve the user's submitted username and email for display while storing
separate canonical lookup values. Canonicalization trims surrounding whitespace
and applies documented Unicode normalization and case folding. Database unique
indexes enforce global uniqueness on the canonical values.

The implementation spike must choose and test the exact PostgreSQL representation
before the identity migration; application-only lowercase checks are not
sufficient.

### Password and token profile

- Hash passwords with Argon2id using versioned parameters selected from a
  deployment-resource benchmark and rehash after signin when parameters age.
- Keep password policy operator-configurable only where policy legitimately
  varies; validation behavior remains documented in OpenAPI.
- Generate invitation and reset tokens with at least 128 bits of entropy.
- Store only one-way token verifiers.
- Consume signup and reset tokens atomically with the state change they
  authorize.
- Store token purpose, issuance, expiry, use, revocation, and replacement state.

Exact Argon2id parameters, invitation lifetime, reset lifetime, and password
limits must be filled into this ADR before acceptance.

### Distributed throttling

Use PostgreSQL-backed throttling for public authentication. Rate-limit records
use bounded, privacy-safe keys derived from operation, canonical identity input,
and trusted client network information. Responses remain generic regardless of
whether an identity exists.

The design must specify trusted proxy handling, windows, limits, cleanup, and
behavior during database failure. Raw passwords, tokens, and full untrusted
request bodies are never rate-limit keys or diagnostics.

### Email delivery

Persist invitation or recovery state before attempting SMTP. The API performs a
bounded SMTP attempt after commit and records a sanitized delivery outcome.

- Invitation delivery failure leaves an explicit retryable invitation state.
- Resending an invitation issues a new token and revokes the previous token.
- Recovery behavior never confirms whether the account exists. If SMTP failure
  is reported as a safe temporary error, equivalent requests for missing
  identities remain indistinguishable in response class, body, and practical
  timing.
- SMTP credentials, recipient-bearing protocol transcripts, and provider errors
  do not enter owner-visible logs or telemetry.

Automatic durable email retries are not required initially. Administrative
resend is the retry mechanism for invitations. A future durable outbox may be
introduced if measured SMTP reliability or operational needs justify it.

### Bootstrap administrator

Provision the first administrator through an explicit one-shot bootstrap command
using Secret-backed input. Normal API or worker startup does not reconcile or
reset administrator credentials. The command is idempotent only when the
configured identity already matches; credential rotation uses an explicit
administrative command and audit.

## Alternatives Considered

### In-memory rate limiting

In-memory limits disappear on restart and do not coordinate API replicas. They
may supplement but cannot be the primary public-auth control.

### Valkey-backed throttling

Valkey would coordinate limits but adds stateful infrastructure that ADR 0001
rejects without measured need. PostgreSQL is sufficient at the expected scale.

### Email outbox processed by the worker

A durable outbox offers automatic retries but would require production job
execution during Milestone 2, ahead of the planned worker lifecycle in
Milestone 3. The initial bounded API delivery plus persisted retry state is
smaller and matches the architecture. This should be reconsidered if reliable
automatic delivery becomes a requirement.

### Reconcile bootstrap credentials at every startup

Continuous reconciliation could silently reset a changed password and makes a
long-lived plaintext bootstrap secret more dangerous. An explicit command is
safer and auditable.

## Consequences

- Public-auth throttling adds PostgreSQL writes and retention cleanup.
- Identity canonicalization becomes an irreversible schema contract requiring
  Unicode and collision fixtures.
- SMTP delivery may require explicit resend after transient failures.
- Bootstrap is an operator action rather than an automatic startup side effect.
- Security tests must cover enumeration, replay, concurrency, expiry, rate-limit
  coordination, SMTP failure, and sanitized diagnostics.

## Acceptance Evidence

Before acceptance, benchmark Argon2id on representative pod resources, choose
canonicalization and database-index behavior, set token/password policy values,
define rate-limit windows and trusted-proxy rules, and test the SMTP failure and
resend lifecycle against the functional specification.
