# ADR 0005: Public Authentication And Email Delivery

## Status

Accepted

## Context

Invitation, signup, signin, and password recovery are public operations. They
must resist account enumeration, token replay, brute force, resource exhaustion,
and SMTP failure. The deployment may run multiple API replicas, while ADR 0001
intentionally avoids Valkey and external message-broker infrastructure.

The architecture assigns SMTP to the API. Invitation delivery must leave
retryable state after SMTP failure, and password recovery must not reveal whether
an account exists or whether delivery succeeded. ADR 0002 requires narrowly
scoped access to global security data, and ADR 0004 fixes signin credential
transport, configured-origin, cookie, CSRF, and generic-error behavior.

## Decision

### Canonical identities

Username and email display values are the submitted values after trimming only
the Unicode 15.0 `White_Space` property from both ends. The application rejects
invalid UTF-8. It does not silently trim or map any interior character.

Username canonicalization version 1 is, in this order:

1. trim surrounding Unicode 15.0 `White_Space`;
2. apply Unicode 15.0 NFKC;
3. apply Unicode 15.0 full, locale-independent default case folding; and
4. apply Unicode 15.0 NFKC again.

The canonical username must contain 3 through 32 Unicode scalar values and no
more than 128 UTF-8 bytes. Each scalar must be a Unicode letter, decimal digit,
or combining mark, or one of `.`, `_`, and `-`; the first scalar must be a
letter or decimal digit. The display value is validated by canonicalizing it,
but remains otherwise unchanged. Usernames cannot contain `@`, so signin can
unambiguously distinguish username input from email input. Examples of intended
collisions include `Straße`/`STRASSE`, full-width/ASCII letters, and
precomposed/decomposed equivalent text.

Email accepts one address only, with no display name, comments, quoted local
part, domain literal, or obsolete syntax. The local part is an ASCII dot-atom,
is at most 64 bytes, and is lowercased for canonical comparison. The domain uses
Unicode 15.0 and the `golang.org/x/net/idna` profile composed from
`MapForLookup`, `Transitional(false)`, `BidiRule`, and
`VerifyDNSLength(true)`. It is converted to lowercase ASCII A-labels and must
satisfy DNS label rules. The resulting canonical addr-spec is at most 254 bytes.
This deliberately treats the local part case-insensitively, as required by the
functional specification, and does not claim SMTPUTF8 local-part support.

PostgreSQL stores display values separately from non-null canonical values. A
single global authentication-principal table covers both administrator and data
owner identities and has these independently unique representations:

```sql
canonical_username text COLLATE "C" NOT NULL,
canonical_email    text COLLATE "C" NOT NULL,
canonicalization_version smallint NOT NULL,
UNIQUE (canonical_username),
UNIQUE (canonical_email)
```

The bytewise deterministic `C` collation makes the B-tree constraints enforce
the application's already normalized representation without depending on the
database's locale or nondeterministic ICU collation. The table is global
identity data, not an account-owned private table under RLS; following ADR 0002,
runtime roles receive only the specific lookup and mutation privileges they
need. A Unicode or IDNA table upgrade requires a reviewed data migration that
recomputes every canonical value and aborts on a collision. Runtime code never
recanonicalizes persisted identities opportunistically.

### Password profile

Passwords are valid UTF-8 containing 12 through 128 Unicode scalar values and at
most 512 UTF-8 bytes. The minimum defaults to 12 and is the only configurable
password-content rule; operators may choose 12 through 64. The scalar and byte
maxima are fixed denial-of-service bounds. Passwords allow spaces and symbols,
have no composition rule, and are never trimmed, normalized, or case-mapped.
The confirmation must match the exact UTF-8 bytes. NUL is rejected to keep
password handling consistent across tooling.

Hash passwords with Argon2id version 19 using:

- memory `m=65536` KiB;
- iterations `t=3`;
- parallelism `p=1`;
- a fresh random 16-byte salt; and
- a 32-byte derived key.

Store a strict PHC-format encoding containing the algorithm, version,
parameters, salt, and derived key. Parsing rejects unknown algorithms, duplicate
parameters, unsupported versions, and values outside implementation safety
bounds before allocating memory. Verification uses constant-time derived-key
comparison. A successful signin rehashes in the same transaction when the
stored profile differs from the current profile; a failed signin never rehashes.

Each API replica admits at most two concurrent Argon2 operations. It performs
rate-limit checks before entering this gate. A request that cannot enter within
250 milliseconds returns the same sanitized `503` class used for temporary
authentication-service failure; it does not fall back to a weaker hash.

### Tokens and lifetimes

Invitation and password-reset tokens are independent random 32-byte values
(256 bits), encoded as 43-character unpadded base64url. PostgreSQL stores only a
SHA-256 verifier because these are uniformly random high-entropy secrets. Token
comparison is constant time after indexed verifier lookup.

- An invitation expires exactly 7 days after issuance.
- A password reset expires exactly 30 minutes after issuance.
- Issuance and expiry are stored as `timestamptz` instants based on the database
  transaction time; expiry is exclusive (`now() < expires_at`).
- Issuing a replacement revokes the preceding live token in the same
  transaction. There is at most one live invitation per invitation and one live
  reset per principal.
- Signup and reset lock the token state and consume it in the same transaction
  as account creation or password replacement. Concurrent use, revocation,
  replacement, and expiry therefore permit at most one successful outcome.
- Reset completion revokes all of the principal's sessions as required by ADR
  0004. Token values never enter logs, telemetry, audits, or API responses other
  than the email link for which they were created.

Persist token purpose, issuance, expiry, use, revocation, replacement, and
sanitized delivery state. Token lifetime and entropy are fixed domain rules, not
operator policy.

### PostgreSQL-backed throttling

Every syntactically valid public-auth attempt consumes all applicable counters
before identity lookup, Argon2, token mutation, or SMTP. PostgreSQL increments
the counters atomically and returns one decision. Concurrent replicas therefore
share the limits. A denied attempt still increments every applicable counter so
one key cannot be probed through another.

Counters use UTC-aligned fixed windows and the following exact limits:

| Operation class | Network-prefix limit | Subject limit |
|---|---:|---:|
| Browser and bearer signin, shared | 30 per 10 minutes | 10 per canonical username or email per 10 minutes |
| Invitation signup | 20 per hour | 10 per invitation-token verifier per hour |
| Forgot-password request | 10 per hour | 3 per canonical username or email per hour |
| Password-reset completion | 20 per hour | 10 per reset-token verifier per hour |

The network subject is IPv4 `/24` or IPv6 `/64`, not a full client address.
Subject material is encoded with its operation and key kind and stored only as
`HMAC-SHA-256(rate_limit_key, encoded_subject)`. The HMAC input is
length-delimited and domain-separated. The Secret-backed rate-limit key is at
least 32 random bytes and is not stored in PostgreSQL. Raw addresses, canonical
identity input, passwords, tokens, and request bodies are not persisted in rate
limits or diagnostics. Rotating the HMAC key resets active counters and is an
explicit incident operation, not routine rotation.

An exceeded limit returns a generic `429` Problem Details response and a
`Retry-After` rounded up to the end of the rejected fixed window. The response
does not vary with identity or token existence. If the rate-limit transaction
cannot complete, every operation fails closed with the same generic `503`
response before credential verification, state mutation, or SMTP. Readiness and
normal database failure policy remain as defined elsewhere.

Rate-limit rows contain `(operation, key_kind, key_digest, window_start,
window_end, count)` and a unique constraint on the first four fields. Each API
replica runs cleanup at startup and every 10 minutes; a PostgreSQL advisory lock
elects one cleaner, which deletes at most 5,000 rows per pass in batches of 1,000
where `window_end < now() - interval '24 hours'`. Cleanup uses a two-second
statement timeout. Cleanup failure emits only a counter and sanitized warning,
does not bypass throttling, and is retried at the next interval. The API runtime
role accesses rows only through fixed-search-path, narrowly granted functions
owned by a non-login owner, consistent with ADR 0002.

### Client network and trusted proxies

The default trusted-proxy CIDR list is empty. The socket peer from
`RemoteAddr` is authoritative unless it belongs to an explicitly configured
trusted CIDR. Only then may `X-Forwarded-For` affect client attribution:

1. parse at most 20 comma-separated, bare IP addresses from a header of at most
   1,024 bytes and append the socket peer;
2. walk right to left, discarding trusted proxies; and
3. select the first untrusted address, or the leftmost address if every address
   is trusted.

An untrusted peer's forwarding headers are ignored. A trusted peer's malformed
or over-limit header is rejected with `400` rather than silently merging clients
under the proxy address. `Forwarded`, `X-Real-IP`, forwarded host, and forwarded
scheme are ignored. These rules affect rate-limit metadata only: ADR 0004's
origin checks, public-origin configuration, and production cookie security never
derive from forwarding headers. Configuration must list ingress/gateway source
CIDRs, never all cluster or private networks merely for convenience.

### Email delivery

Invitation or recovery state commits before SMTP starts. SMTP uses authenticated
STARTTLS with certificate and hostname verification; plaintext fallback is
prohibited. One attempt has a 10-second total deadline covering DNS, connect,
STARTTLS, authentication, envelope, message transfer, and response. Socket
deadlines enforce cancellation even when a library call does not accept a
context. Each replica permits two SMTP attempts concurrently and keeps at most
32 recovery deliveries in memory. There is no retry inside one attempt.

Invitation creation and resend wait for that one bounded attempt so the
administrator receives either success or a sanitized `503` Problem Details
response stating that the invitation remains retryable. Forgot-password always
returns the same `202 Accepted` status and body after persistence and a bounded
queue-admission decision, whether the principal exists, the queue is full, or
later delivery succeeds. It never waits for SMTP. Missing identities perform the
same rate-limit and bounded database path but create no reset. Queue-full
recovery is recorded as delivery failure for an existing reset before the
generic response. Two in-process delivery workers discard a queued item older
than 30 seconds and record failure rather than starting an already delayed
attempt.

Delivery state is one of `pending`, `delivered`, `failed`, or `unknown` with a
sanitized category such as `timeout`, `tls`, `authentication`, `rejected`,
`queue_full`, or `interrupted`; provider text and SMTP transcripts are never
stored. If SMTP succeeds but recording the outcome fails, state remains
`pending`/`unknown`, never falsely `delivered`. API shutdown cancels bounded
attempts and marks work `unknown` where possible. Startup marks stale `pending`
attempts `unknown`. Both `failed` and `unknown` are retryable invitation states.

Administrative resend locks the invitation, rejects accepted or revoked
invitations, revokes the prior token, creates a new seven-day token in `pending`
state, and commits before SMTP. Thus an old link fails as soon as resend commits,
including when delivery of the replacement fails. Concurrent resend, acceptance,
and revocation serialize on the invitation and only one state transition wins.
Recovery has no administrative resend; a user repeats forgot-password, which
atomically supersedes any prior live reset.

Automatic durable email retries are not required initially. A future durable
outbox may be introduced if measured SMTP reliability or operational needs
justify it.

### Bootstrap administrator

The API image provides an explicit one-shot `bootstrap-admin` command. Normal
API and worker startup never creates, reconciles, enables, or resets an
administrator. The command requires the normal API database URL, `--username`,
`--email`, and `--password-file`; the password is never accepted as a command-line
argument or environment value. The mounted Secret file must be a regular file,
must not be group/world accessible, and is read as exact bytes with no newline
trimming under the fixed password limits.

The command canonicalizes and validates all input, hashes the password using the
current profile, obtains a transaction-scoped advisory lock, and then behaves as
follows:

- with no administrator, atomically creates the separate administrator
  principal and a sanitized bootstrap audit and exits zero;
- with exactly one active administrator whose canonical username, canonical
  email, and verified password all match, performs no write and exits zero;
- with any identity or credential mismatch, a disabled administrator, or more
  than one administrator, performs no write and exits nonzero with a generic
  diagnostic; and
- on validation, database, or hashing failure, performs no partial creation and
  exits nonzero.

The command never prints identity values, password material, or a hash. It does
not rotate credentials or re-enable accounts. Rotation is a separate explicit
operator action with its own sanitized audit and is not implemented as bootstrap
idempotence. The bootstrap Job and its Secret are removed after success; keeping
plaintext bootstrap credentials in a long-lived Deployment is prohibited.

## Alternatives Considered

### In-memory rate limiting

In-memory limits disappear on restart and do not coordinate API replicas. They
may supplement but cannot be the primary public-auth control.

### Valkey-backed throttling

Valkey would coordinate limits but adds stateful infrastructure that ADR 0001
rejects without measured need. PostgreSQL is sufficient at the expected scale.

### Email outbox processed by the worker

A durable outbox offers automatic retries but would require production job
execution during Milestone 2, ahead of the planned worker lifecycle in Milestone
3. The bounded API delivery plus persisted retry state is smaller and matches
the architecture. This should be reconsidered if reliable automatic delivery
becomes a requirement.

### Reconcile bootstrap credentials at every startup

Continuous reconciliation could silently reset a changed password and makes a
long-lived plaintext bootstrap secret more dangerous. An explicit command is
safer and auditable.

## Consequences

- Canonical values and their Unicode/IDNA version become an irreversible schema
  contract with collision fixtures and migration review on table upgrades.
- Two simultaneous password hashes consume 128 MiB of working memory; admission
  control is required on every replica.
- Public-auth throttling adds small PostgreSQL writes and retention cleanup.
- Fixed windows can allow a boundary burst of up to twice a listed limit; the
  paired subject and network limits are accepted at the expected scale.
- Recovery delivery is bounded but not durable across process failure; users can
  request a new reset without learning the earlier outcome.
- SMTP delivery may require explicit administrative invitation resend.
- Bootstrap is an operator action rather than an automatic startup side effect.
- Security tests must cover canonical collisions, hash parsing and rehash,
  enumeration, replay, concurrency, expiry, rate-limit coordination and database
  failure, trusted-proxy parsing, SMTP timeout/failure, sanitized diagnostics,
  and bootstrap mismatch behavior.

## Acceptance Evidence

### Argon2id benchmark

The benchmark ran on 2026-08-03 with Go 1.26.5 and
`golang.org/x/crypto/argon2` (Argon2 version 19) on Linux/amd64, a 13th Gen Intel
Core i9-13900HK virtualized host. The host cgroup exposed a two-CPU quota and 10
GiB memory ceiling. The representative API pod assumption is `1 CPU / 512 MiB`,
with `GOMAXPROCS=1`, Argon2 `p=1`, and at most two hashes concurrently. Fifteen
timed hashes followed three warmups for each profile, using a 64-byte password,
16-byte salt, and 32-byte output:

| Argon2id profile | Median | Worst/p95 of 15 | Mean throughput |
|---|---:|---:|---:|
| `m=32768 KiB, t=2, p=1` | 33.1 ms | 38.1 ms | 29.86/s |
| `m=65536 KiB, t=2, p=1` | 65.9 ms | 74.4 ms | 14.88/s |
| `m=65536 KiB, t=3, p=1` | 98.8 ms | 105.6 ms | 10.18/s |
| `m=98304 KiB, t=2, p=1` | 105.8 ms | 117.5 ms | 9.32/s |
| `m=131072 KiB, t=2, p=1` | 144.8 ms | 167.2 ms | 6.84/s |

For the selected profile, 30 operations in two concurrent streams completed in
3.110 seconds: 207.3 ms mean wall time per stream operation and 9.65/s aggregate.
The two-hash gate bounds Argon2 working memory at 128 MiB, leaving 384 MiB of the
assumed pod limit for the Go runtime, HTTP handling, SMTP, and database pool. The
selected profile adds a third pass over 64 MiB at essentially the same measured
latency as the 96 MiB/two-pass candidate while retaining more pod headroom.

### Canonical and state review

The canonical representation is deterministic bytewise PostgreSQL text rather
than locale-dependent `lower()` or nondeterministic collation. Review fixtures
must include ASCII case, `Straße`/`STRASSE`, full-width ASCII, composed/decomposed
marks, surrounding Unicode whitespace, invalid UTF-8, IDNA A-label/U-label
equivalence, and every resulting unique-constraint collision before migrations
merge.

The delivery lifecycle was walked against the functional-spec failure and
acceptance cases:

| Scenario | Committed result | Observable result |
|---|---|---|
| New invitation, SMTP success | live token, `delivered` | administrator success |
| New invitation, SMTP failure/timeout | live token, `failed`/`unknown` | sanitized failure; invitation retryable |
| Resend, SMTP failure | old token revoked; new live token retryable | old link fails; sanitized failure |
| Resend races acceptance or revocation | row lock commits exactly one transition | only the winning state/token is usable |
| Forgot-password for present or absent identity | reset persisted only when present | identical immediate `202` body and class |
| Recovery SMTP failure or queue interruption | `failed`/`unknown` when a reset exists | no changed public response; user may request again |
| Signup/reset token used concurrently | one transaction consumes token | at most one account creation/password change |

This evidence resolves every prerequisite identified when the ADR was Proposed.
Implementation acceptance still requires automated versions of these fixtures
and failure-injection cases; accepting this record authorizes that implementation
but does not substitute for Milestone 2 tests.

## Conditions That Would Trigger Reconsideration

Rebenchmark the password profile before changing API pod CPU architecture or
memory below 512 MiB, or when p95 verification materially exceeds 500
milliseconds under representative concurrent traffic. Reconsider the fixed
rate-limit design if measured boundary abuse or PostgreSQL write load is
material. Introduce a durable outbox if SMTP interruption or manual invitation
resend becomes operationally unacceptable. Unicode/IDNA version changes require
an explicit collision-safe migration, not an in-place behavior change.
