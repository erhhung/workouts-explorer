# Source Encryption Key Rotation

This runbook rotates the master keys defined by ADR 0007. Key-ring files and
commands must be handled through the deployment secret mechanism; never place
key material in shell arguments, logs, Git, PostgreSQL, or temporary files.

## Preconditions

1. Back up PostgreSQL and the current key ring through the operator's protected
   secret-backup process. Record which backup generations require each key.
2. Verify API and worker health, no active prior rotation, and enough retained
   keys to decrypt every live source and job-snapshot envelope.
3. Generate a 32-byte key with a cryptographically secure generator. Assign a
   non-secret unique key ID matching ADR 0007; store the key as unpadded base64url
   in the protected Kubernetes Secret source.

## Staged Rotation

1. Add the new key to `keys` on every API and worker replica while leaving the
   old `activeKeyId`. Roll out and verify all replicas can load the expanded ring.
2. Change `activeKeyId` to the new ID, retain every predecessor, and roll out all
   replicas. New source documents and snapshots now use the new key.
3. Run the administrative rewrap command with its key ring supplied by mounted
   file. The command processes bounded batches, locks one envelope at a time,
   decrypts its DEK with the referenced predecessor, re-encrypts the same DEK
   with the active key and fresh nonce/AAD, and conditionally updates only the
   unchanged envelope. It checkpoints by table and UUID and is safe to restart.
4. Repeat until verification reports zero live source or job-snapshot envelopes
   referencing keys other than the active ID. Concurrent writes, deletes, and
   terminal cleanup may cause conditional misses; rerunning resolves them.
5. Exercise an API source read and a worker connection check before considering
   rewrap complete. Keep predecessor keys through the documented backup-retention
   window.

## Rollback

Before predecessor removal, restore the previous `activeKeyId` while retaining
both keys and roll out all replicas. Envelopes already written with the new key
remain readable. Do not remove the new key during rollback. Diagnose and rerun
rewrap after correcting the cause.

## Predecessor Retirement

Remove a predecessor only after all of the following are true:

- the live-envelope verification query reports no reference to its ID;
- every API and worker replica uses a ring that includes the current active key;
- retained backups that may reference the predecessor have expired or the
  predecessor remains recoverable in the protected backup-key archive; and
- a representative restore has confirmed that the backup and required key set
  can be used together.

Roll out the reduced ring and verify readiness. Destruction of retired key
material follows the operator's secret-retention policy, not application logic.

## Recovery

An unknown key ID is not repaired by changing envelope metadata. Restore the
matching key from the protected key archive, deploy it as a decryption-only
predecessor, verify authenticated decryption, then resume rewrap. If the key is
irrecoverable, affected encrypted configuration is irrecoverable; disable the
source safely and require the owner to submit new configuration. Never emit the
envelope or guessed plaintext while diagnosing recovery.
