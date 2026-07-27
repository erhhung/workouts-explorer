# ADR 0007: Source Configuration Encryption

## Status

Proposed

This ADR must be accepted before Milestone 3 source configuration is persisted.

## Context

Source configurations contain local paths and remote credentials. Current source
configuration and job-scoped snapshots must be encrypted with a
Kubernetes-provided master key that is not stored in PostgreSQL. Secrets must be
replaceable, clearable at terminal job outcomes, and eventually rotatable.

Rclone may refresh mutable cookies. Those values may update current source
configuration only when the source generation still matches the generation used
to create the job snapshot.

## Proposed Decision

### Envelope format

Encrypt each sensitive configuration document with a fresh random data-encryption
key using an authenticated encryption algorithm from the Go standard or extended
cryptography ecosystem. Encrypt that data key with the active master key.

Persist a versioned envelope containing only:

- format version;
- master-key identifier;
- wrapped data key;
- algorithm identifier where needed for migration;
- nonces; and
- authenticated ciphertext.

Bind immutable record purpose and identity as additional authenticated data so
ciphertext cannot be moved between source and job-snapshot records.

The exact AEAD and key-wrapping construction must be selected through a focused
cryptographic implementation review before this ADR is accepted. Custom
cryptographic primitives are prohibited.

### Key management

- Kubernetes Secrets mount one active master key and any explicitly retained
  decryption-only predecessor keys into API and worker pods.
- Master keys never enter PostgreSQL, logs, telemetry, command arguments, or
  public configuration.
- New writes use the active key identifier.
- Rotation is an explicit, resumable administrative operation that rewraps data
  keys without exposing plaintext configuration outside process memory.
- A predecessor key is removed only after verification shows no live envelope
  references it and backup-retention implications are documented.

### Configuration lifecycle

- Validate and canonicalize adapter configuration before encryption.
- Store public source fields separately from one encrypted adapter document.
- Responses reconstruct only allowlisted non-secret fields; secrets are always
  write-only.
- PATCH merges against decrypted current configuration in memory, preserving
  omitted write-only secrets and rejecting required-secret clearing.
- Each child job receives an independent encrypted snapshot.
- Terminal success, failure, or cancellation clears snapshot ciphertext.
- Decrypted values exist only in process memory and required private job-scoped
  files.

### Mutable credential updates

The worker writes refreshed mutable credentials only with a compare-and-swap on
the source identifier, active lifecycle state, and generation captured by the
job. A stale, updated, or deleted source rejects the write without making the
job fail solely because its refresh became obsolete.

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

## Consequences

- Every configuration read/write includes envelope processing and error paths.
- Envelope metadata and key identifiers become a persistent compatibility
  contract.
- Rotation requires an explicit operational command and migration tests.
- Logical clearing cannot remove ciphertext already retained in PostgreSQL WAL
  or infrastructure backups.
- Tests must cover tampering, wrong purpose/record identity, old keys, partial
  PATCH, snapshot cleanup, compare-and-swap races, and redaction.

## Acceptance Evidence

Before acceptance, select reviewed Go cryptography APIs, define envelope bytes
and additional authenticated data exactly, write a key-rotation runbook, and
threat-model database compromise, pod compromise, backups, crash diagnostics,
and rclone temporary files.
