# ADR 0007: Source Configuration Encryption

## Status

Accepted

Accepted on 8/4/2026 before Milestone 3 source configuration persistence.
Amended for M8 decisions accepted on 10/11/2026. Implementation and synthetic
verification are ready for owner review as of 10/12/2026; this is not milestone
closeout, live-cloud acceptance or production validation.

## Context

Source configurations contain local paths and remote credentials. Current source
configuration and job-scoped snapshots must be encrypted with a
Kubernetes-provided master key that is not stored in PostgreSQL. Secrets must be
replaceable, clearable at terminal job outcomes, and eventually rotatable.

Rclone may refresh mutable cookies. Those values may update current source
configuration only when the source generation still matches the generation used
to create the job snapshot.

## Decision

### Envelope format

Use only Go's `crypto/aes`, `crypto/cipher`, and `crypto/rand` packages. Each
document gets a random 32-byte data-encryption key (DEK). AES-256-GCM encrypts
the canonical configuration with the DEK, and a second AES-256-GCM operation
encrypts the DEK with the selected 32-byte master key. Each operation gets an
independent 12-byte nonce from `crypto/rand.Reader`; nonces are never derived or
reused. No custom cryptographic primitive is permitted.

The UTF-8 JSON envelope has exactly these fields and rejects unknown fields:

```json
{
  "version": 1,
  "algorithm": "AES-256-GCM+AES-256-GCM",
  "keyId": "2026-08-primary",
  "wrappedKeyNonce": "base64url without padding",
  "wrappedKey": "base64url without padding",
  "payloadNonce": "base64url without padding",
  "ciphertext": "base64url without padding"
}
```

Version 1 accepts only the named algorithm, a key ID matching
`^[a-z0-9][a-z0-9._-]{0,63}$`, 12-byte decoded nonces, a 48-byte wrapped DEK,
and at most 64 KiB of decoded ciphertext. Unsupported versions, algorithms,
lengths, encodings, duplicate fields, and trailing JSON fail closed. The
configuration plaintext is a closed, versioned adapter object encoded with
Go's `encoding/json`; adapter code uses typed structures rather than arbitrary
maps, and decoding rejects unknown fields and trailing data.

Both AEAD operations bind immutable context as UTF-8 additional authenticated
data (AAD). UUIDs are canonical lowercase dashed strings and fields are joined
with a single LF byte:

```text
workouts-explorer/source-config-envelope/v1/dek\n<PURPOSE>\n<ACCOUNT-UUID>\n<RECORD-UUID>
workouts-explorer/source-config-envelope/v1/payload\n<PURPOSE>\n<ACCOUNT-UUID>\n<RECORD-UUID>
```

`PURPOSE` is exactly `source-config` with the source ID as `RECORD-UUID`, or
`job-config-snapshot` with the job ID as `RECORD-UUID`. The domain prefix,
purpose, account ID, and record ID prevent cross-account, cross-record, and
source-to-snapshot substitution. Snapshot creation decrypts the current source
document and independently encrypts the same canonical configuration with a
fresh DEK; it never copies source envelope bytes.

### Key management

- A Kubernetes Secret mounts one `keyring.json` file read-only into API and
  worker pods. It has a required `activeKeyId` and a `keys` object whose key IDs
  map to unpadded base64url-encoded 32-byte keys. The active ID must exist in
  `keys`; duplicate IDs, unknown fields, invalid IDs, invalid encodings, wrong
  lengths, and files larger than 16 KiB prevent process startup.
- The active key and explicitly retained decryption-only predecessor keys are
  present in `keys`. Every configured key may decrypt, but only `activeKeyId`
  encrypts new envelopes.
- Master keys never enter PostgreSQL, logs, telemetry, command arguments, or
  public configuration.
- New writes use the active key identifier.
- Rotation follows `docs/source-encryption-key-rotation.md`. The resumable
  command decrypts only each wrapped DEK and encrypts that DEK with the active
  key; configuration plaintext is not decrypted during rewrap.
- A predecessor key is removed only after verification shows no live envelope
  references it and backup-retention implications are documented.

### Configuration lifecycle

- Validate and canonicalize adapter configuration before encryption. The entire
  adapter `config`, including a local path and non-secret adapter fields, is one
  encrypted document.
- Store only source ID, account ID, display name, immutable type,
  `autoSyncEnabled`, lifecycle status, generation, safe status metadata, and
  timestamps outside the encrypted document.
- Responses reconstruct only allowlisted non-secret fields; secrets are always
  write-only.
- PATCH merges against decrypted current configuration in memory, preserving
  omitted write-only secrets and rejecting required-secret clearing.
- Every connection-check and source-child job receives an independent encrypted
  snapshot. Source generation capture, snapshot creation, and job insertion
  occur in one transaction while the source revision is locked.
- Terminal success, failure, or cancellation deletes the complete snapshot row
  in the same transaction as the terminal transition. Queued cancellation does
  the same. A cleanup failure prevents the terminal transition and remains
  retryable; maintenance may finish cleanup for an already-terminal legacy or
  crash-inconsistent row.
- Decrypted values exist only in process memory and required private job-scoped
  files.

### Mutable credential updates

Generation is a positive signed 64-bit integer initialized to 1. A successful
owner mutation that changes canonical adapter configuration increments it once.
Metadata-only changes, connection status changes, and envelope rewraps do not.
A mutable credential refresh is an adapter-configuration change and increments
generation once. Overflow rejects the mutation.

The worker writes refreshed mutable credentials only when all of source ID,
non-deleted lifecycle, captured generation, running job ID, worker identity, and
lease token still match. The source and job rows are locked in one transaction.
Two refreshes from one generation are deliberately first-writer-wins: the first
increments generation and later writes are discarded. A stale, updated,
deleted, cancelled, or lease-lost source rejects the write without making the
job fail solely because its refresh became obsolete. Refresh never mutates the
job snapshot.

Connection-check status updates use the same captured-generation and lease
fence. A stale check may finish its job but cannot publish status for newer
configuration. `checking-connection` and `connected` sources permit refresh;
`connection-failed` permits only a connection-check result, not ingest refresh.

M8 implements cookie-only refresh: native changes to identity, password, trust
token or client ID cannot be promoted. Successful check completion publishes its
cookie refresh and `connected` atomically, avoiding a generation increment that
invalidates the check's own status fence. Refresh encryption or persistence errors
retain recoverable work rather than clearing a snapshot or falsely succeeding.

The closed `health-auto-export-icloud` version-1 config requires path, write-only
Apple ID, reversibly obscured password, full cookie string and trust token, with
optional non-secret client identity. Public config exposes only version/path/
optional client identity, never Apple ID or masked credential placeholders.
PATCH retains omitted credentials and rejects null/empty required values; a
canonical path/client identity change also increments generation and queues a
check. [The setup guide](../icloud-source-setup.md) records exact schema limits.

Immutable forward migration `021_icloud_sources.sql` supplies typed snapshot,
cookie-CAS, atomic check/status and source-failure functions and raises current
schema/minimum runtime to 21. Version-20 claims and scheduler leases acquired
before upgrade are fenced. This is independent of the earlier development-only
Coverage/tile consolidation; historical schema-20/minimum-18 receipts remain valid
for their dated runs.

## Alternatives Considered

### Encrypt individual secret fields

Field-level encryption can expose configuration shape and creates adapter-specific
crypto plumbing. A versioned document envelope is simpler while public fields
remain queryable in normal columns.

### One master key directly encrypts every document

Direct encryption is simpler but makes rotation require decrypting and
reencrypting all configuration payloads. Wrapped per-record data keys permit
safer rewrapping.

### Store source secrets only as Kubernetes Secrets

Per-user dynamic source CRUD does not map cleanly to deployment-managed Secret
objects and would grant the application Kubernetes write authority.

## Consequences And Threat Model

- Every configuration read/write includes envelope processing and error paths.
- Envelope metadata and key identifiers become a persistent compatibility
  contract.
- Rotation requires an explicit operational command and migration tests.
- Logical clearing cannot remove ciphertext already retained in PostgreSQL WAL
  or infrastructure backups.
- Tests must cover tampering, wrong purpose/record identity, old keys, partial
  PATCH, snapshot cleanup, compare-and-swap races, and redaction.
- Database-only compromise reveals envelope metadata and ciphertext but not
  plaintext without a mounted key. Compromise of an API or worker pod can expose
  mounted keys and plaintext being processed by that pod; pod hardening,
  least-privilege service accounts, key rotation, and incident response bound
  but cannot eliminate that risk.
- Backups and WAL retain encrypted values and therefore require retained key
  material for restoration. Backup encryption, access control, retention, and
  destruction are operator responsibilities.
- Crash diagnostics must never include envelopes, key IDs, adapter config,
  private paths, payloads, or decrypted values. Errors use stable safe codes.
- M8 rclone configuration files are an accepted plaintext exception: one profile
  per mode-0600 file in a random job directory with `0700` access (possibly inherited
  setgid) beneath the worker-owned
  `WORKER_STAGING_ROOT/private` child. A UID-0/fsGroup-writable emptyDir outer root
  is not the adapter's private root. Staging is per pod, never shared with other
  independent worker processes; Linux `openat2` and `/proc/self/fd` pin access.
  Native 1.75.2 runs noninteractively with credential-free argv, stripped
  `RCLONE*` environment and private HOME/XDG/cache/temp paths. Config backups,
  endpoint files and downloads join the cleanup scope.
- A local attempt's cleanup failure blocks its terminal writes and that worker's
  new claims/readiness until cleanup is confirmed. Bounded retries and startup
  scavenging are worker/pod-local, not distributed cross-pod cleanup or a
  lost-volume guarantee. Permission, read-only filesystem or hardware faults are
  not automatically repaired. Eventual per-pod volume disposal is a recovery
  boundary, not evidence of immediate physical erasure.
- An unknown historical key or authentication failure affects only the envelope
  operation: API reads fail with a generic internal problem, mutations fail with
  a generic service-unavailable problem when encryption is unavailable, and a
  worker fails the job with `source_config_unavailable`. Connection failures may
  set a generation-fenced safe source status. Readiness validates key-ring
  structure and active-key availability, not every stored envelope.

## Acceptance Evidence

The construction uses the standard library's reviewed AES-GCM implementation
and operating-system CSPRNG through `crypto/rand`; it requires no third-party or
custom cryptography. The exact envelope fields, limits, encoding, nonce rules,
and AAD bytes are fixed above. The lifecycle was walked against source update,
connection-check, concurrent refresh, lease loss, deletion, queued/running
cancellation, retry, terminal cleanup, key rotation, and backup-restore races.

`docs/source-encryption-key-rotation.md` records staged rollout, resumable
rewrap, verification, rollback, predecessor retirement, and restore behavior.
The consequences section records the required database, pod, backup, crash,
and temporary-file threat model and residual risks. This evidence authorizes
Milestone 3 implementation but does not replace its tamper, rotation, race,
redaction, and cleanup tests.

M8's [10/12/2026 verification summary](../implementation-plan.md#verification-results-10122026)
records actual disposable PostGIS CAS/status/runtime-fence/readiness checks,
synthetic cleanup/refresh/race tests and non-root native/private-root checks.
The bind-root private child retains setgid as mode `2700` with `0700` access.
These scoped results are ready for owner review, not a wider database-suite pass,
distributed cleanup guarantee or production/security validation.

## Conditions That Would Trigger Reconsideration

Introduce a new envelope version rather than changing version 1 if AES-GCM is no
longer approved, a managed KMS becomes available, the 64 KiB configuration bound
is insufficient, or adapter configuration must be shared across trust domains.
