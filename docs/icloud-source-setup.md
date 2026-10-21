# iCloud Source Setup

## Scope And Status

Milestone 8 source implementation is deployed; owner-operated iCloud ingest and
available-map Coverage recovery are verified. M8 remains unchecked pending
explicit owner acceptance, separate from those operational results. The
[initial verification](implementation-plan.md#verification-results-10122026)
retains its dated synthetic/disposable-database scope. The schema-28 committed-route
pipeline's **folded contract is live in `dev-20261020`, Helm revision 29**, with
schema/Goose/minimum runtime **28** and `migration_baseline=27`. The rollout receipt
is dated **10/20/2026 at 1:54:53 AM PDT** (`2026-10-20T08:54:53.992Z`). All five
deployments are Ready/Available and new runtime-role readiness passed. Release
verification did not probe iCloud authentication, change source configuration or
start an import. Stored source state remains connected with auto-sync disabled.

Historical recovery has independent closure for available map data, including
authorized automatic/manual retries. Final metrics stay
in private operator records; missing-map routes remain unavailable. This does not
close M8 owner acceptance, certify capacity or authorize another yearly import.
See [independent closure](development-progress.md#independent-yearly-recovery-closure-10202026).

New folded-028 behavior (generation locking, strict eligible cohorts and mixed
automatic retries), exact parking repair and the UI follow-up are **deployed**.
Active source remains **001-028**, no
active 029, with `SupportedSchemaVersion=28`, schema/minimum 28 and marker 27.
The same version number does not make old and new runtime contracts compatible.

The approved provider identity/source adoption behavior below **requires schema
and runtime 26** through migrations 024-026. Runtime publication,
live-version and Coverage evidence are recorded in
[Development Progress](development-progress.md); deployment verification is not
M8 owner acceptance or a fresh iCloud-authentication test. The earlier schema-20 compatibility repair
is historical and documented in the [repair runbook](coverage-legacy-route-repair.md).
Host keyring/rclone configuration has not been read; this documentation work
does not create profiles, change live source configuration or start imports.

The 10/11/2026 decisions select metadata-based incremental skipping and a
job-scoped native **rclone 1.75.2** CLI, not a daemon, RC service or embedded
rclone library. Local/NFS remains the supported fallback. Source management is
owner-authenticated Swagger/REST only; source CRUD UI and in-app Apple 2FA are
outside the MVP.

## Replacing An Existing Source

These user-approved rules apply to schema/runtime 26 and later:

- Provider-backed identity is unique within an account by provider namespace and
  opaque provider ID. Local and iCloud transports share `health-auto-export`;
  creating another source does not create a new provider namespace.
- Same mapped source: unchanged content skips raw replacement and changed content
  updates the existing workout. Another **live** owner blocks duplicate creation,
  payload updates and ownership theft. Live means not deleted, even when disabled,
  disconnected, checking or connection-failed.
- A single matching workout owned by a deleted source is adopted under the same
  UUID. The incoming payload is authoritative, including changed header,
  aggregates and route points. Earlier source/file/job provenance stays intact.
  Ambiguous identities or legacy duplicates fail closed without silent merges.
- Workout deletion tombstones prevent resurrection through a replacement source.
  ID-less fallback fingerprints stay conservative and source-local; they are not
  automatically adopted across sources or matched by date/GPS similarity.

Source deletion disables synchronization and clears active credentials, but
retains workouts and a soft source tombstone (`deleted_at`), not a NULL workout
source. The source-update API still cannot change type. Use a new source for the
new transport; there is **no need to wipe existing workouts** to switch sources.
Creating that source alone does not adopt anything: a later eligible ingest does.

Any successful adoption records `adopted` and a durable Coverage refresh request
in the same file transaction, even when content/route is unchanged. Only after
commit do ingest repair or leased reconciliation consume the outbox and request
routed Coverage; no content hash is faked. Missing map data can defer matching.
Aggregate progress counts adoption as Updated and live-owner skips as Unchanged,
with explicit owner-private events distinguishing the reasons. See the
[architecture](architecture.md#provider-identity-and-source-adoption-requires-26)
and [recovery contract](job-state-machine.md#ingest-ownership-and-file-recovery-requires-26).

### Worked Source-Switch Example

This is a synthetic example, not real account or cloud activity:

1. Local A owns a provider-backed workout and is live. Cloud B processes a file
   containing that identity, records `skipped_live_owner` and does not duplicate
   or modify A's workout. B keeps the successful file checksum and the ownership
   epoch captured before the decision, even if the file also imported other data.
2. Further B incremental jobs at the same epoch skip unchanged metadata without
   downloading that file again. Turning A's auto-sync off or disconnecting it
   does not make it an orphan.
3. The owner deletes A. Its live-to-deleted transition increments the account
   ownership epoch; workouts/history remain. This does **not** immediately start
   B sync, reset credentials or reopen completed checkpoints.
4. The next **fresh** B auto-sync or new manual incremental job sees the stale
   blocked epoch and rereads/downloads the file despite identical metadata and
   bytes. It adopts the same workout UUID, keeps historical provenance, applies
   B's authoritative payload and durably requests Coverage even if unchanged.
5. A later B incremental sync skips the unchanged file normally. Adoption itself
   does not bump the epoch. If other live owners still block workouts in a mixed
   file, B records those blockers at the captured epoch and skips further rereads
   until a later source deletion.

An already-running job keeps its frozen manifest actions across recovery, and
completed file checkpoints never reopen or double-count results. Start a new job
or wait for auto-sync to reconsider ownership. Bounded Manual Sync already forces
all eligible files in its inclusive range to reprocess. Cookie refresh and
auth/config generations are separate from ownership and do not invalidate these
ownership skips.

## Authenticate Externally

1. On a trusted machine, use rclone 1.75.2's interactive configuration workflow
   with a deliberately selected private config file. Keep its directory `0700`
   and file `0600`; do not use a repository path or a shared configuration.
2. Configure an `iclouddrive` backend with the `drive` service. Complete Apple
   sign-in, 2FA and any trusted-device approval needed for iCloud web access or
   Advanced Data Protection (ADP). Confirm access to the intended export folder
   outside Workouts Explorer before creating the source.
3. Privately inspect only the intended profile yourself and map the fields below
   into an authenticated owner request. The host profile alias is arbitrary and
   is not an application source field. Do not send a complete `rclone.conf`.
4. Retain or securely remove your external setup file according to your own
   credential-retention policy. Worker cleanup does not remove that host file.

Never paste Apple IDs, passwords, cookies, trust tokens, config files or raw
rclone output into chat, issues, screenshots, logs or this repository. Neither
an agent nor the application should read or automatically upload your host
`rclone.conf`. Disable request-body logging in clients/proxies used for setup;
avoid credentials in command arguments, shell history and copied diagnostics.
Use the authenticated HTTPS owner interface, not an administrator identity.

The pinned backend's public source, rather than a generic option listing, is
the authority for this integration: `newICloudClient` requires `trust_token` for an
already configured session. Native cookies may be optional during interactive
authentication, but the application requires a ready, externally authenticated
session with cookies. `_auth_session` is intermediate 2FA state and is cleared
after setup; it is not a reusable application credential. ADP support depends on
Apple's approval/session state and upstream behavior, not merely the version
number. This guide does not recommend disabling ADP to make imports work.

References: [rclone iCloud Drive](https://rclone.org/iclouddrive/) and
[pinned backend source](https://github.com/rclone/rclone/tree/v1.75.2/backend/iclouddrive).

## Accepted Configuration

Create with `POST /api/sources`, using `type: health-auto-export-icloud`.
The `config` object is closed and case-sensitive. Required create fields are
`version`, `path`, `appleId`, `password`, `cookies` and `trustToken`.
Only `clientId` is optional.

| API field | External profile field | Contract |
|---|---|---|
| `version` | None | Exactly integer `1` |
| `path` | Chosen export folder | Nonempty relative POSIX directory, at most 4096 UTF-8 bytes; example `Auto Export/Example` |
| `appleId` | `apple_id` | Nonempty, at most 320 UTF-8 bytes; secret, write-only |
| `password` | `password` | Rclone-obscured value, at most 4096 bytes; secret, write-only |
| `cookies` | `cookies` | Complete nonempty cookie string, at most 32768 UTF-8 bytes; secret, write-only |
| `trustToken` | `trust_token` | Nonempty, at most 4096 UTF-8 bytes; secret, write-only |
| `clientId` | `client_id` | Non-secret; at most 128 ASCII characters matching `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`, or omitted/empty for native identity |

These are application byte limits, in addition to OpenAPI string-length
validation. Values must be valid UTF-8 without leading/trailing whitespace,
control/format characters, Unicode line/paragraph separators or replacement
characters. The input config is limited to 64 KiB; canonical JSON, including
escaping, must fit 65520 bytes so its GCM tag fits the 64 KiB ciphertext limit.

`path` must already be canonical: no absolute path, `~` prefix, colon,
backslash, empty component, `.` or `..`. It is relative to iCloud Drive, not a
host mount or a `remote:folder` expression. Only direct child files named
`HealthAutoExport-YYYY-MM-DD.json` with valid dates are eligible; discovery is
not recursive.

This source `path` and the `health-auto-export-icloud` JSON parameters are
unrelated to OSM database selectors. Global
[`osm.useReadReplicasOnly`](osm-read-replicas.md) defaults to false and controls
strict standby-role policy only; it never chooses a Secret or overrides an
endpoint. The general worker, Coverage worker and enabled API diagnostics select
`worker.osmDatabaseSecret`, `coverageWorker.osmDatabaseSecret` and
`api.coverageDiagnostics.databaseSecret`, respectively, as complete `{name, key}`
references. Each defaults to name `workouts-explorer-database` and key
`osmReaderDatabaseUrl`, independently of application-database references, the
other readers and policy. False keeps the
selected URL and normal driver behavior; an operator-selected all-node URL may
reach the primary if its session attributes allow it. OSM writer hooks instead
use independent `osm.writerDatabaseSecret` and `osm.writerDatabaseTLSSecret`;
operator Jobs retain independently supplied writer environment selectors.
Source configuration, rclone transport, application-database selectors and
file/matcher concurrency are unchanged. Backend load still needs monitoring;
do not compensate for replica lag by changing source paths, threads or replicas.
Reader rollout does not require inspecting external profiles, keyrings or host
`rclone.conf`.

The password must be canonical unpadded URL-safe base64 decoding to at least
17 bytes (rclone's 16-byte IV plus obscured payload). A plaintext password or a
made-up placeholder is rejected. Rclone obscuring is reversible, **not
encryption**; treat the obscured value as a credential. The application encrypts
the entire canonical config, including path and non-secret client identity.

Copy the **full cookie string**, not a selected handful of cookies. The native
transport additionally validates cookie syntax, nonempty cookie values and the
safe single-line INI representation. Do not submit `description`,
`_auth_session`, profile/remote names, arbitrary flags or native `type`/`service`
inside `config`. Those fields are unsupported; the worker fixes native
`type=iclouddrive`, `service=drive` and its own one-profile alias.

### Request Shape

This is a placeholder-only illustration, **not a runnable or validator-valid
credential fixture**. In particular, `<RCLONE_OBSCURED_PASSWORD>` is intentionally
not a valid obscured password. Substitute actual values privately only in the
owner's authenticated Swagger/REST request, never in this document or chat.

```json
{
  "displayName": "Example iCloud archive",
  "type": "health-auto-export-icloud",
  "autoSyncEnabled": false,
  "config": {
    "version": 1,
    "path": "Auto Export/Example",
    "appleId": "<APPLE_ID>",
    "password": "<RCLONE_OBSCURED_PASSWORD>",
    "cookies": "<COMPLETE_COOKIE_STRING>",
    "trustToken": "<TRUST_TOKEN>"
  }
}
```

Start with auto-sync disabled. Creation returns `checking-connection` and
queues a high-priority asynchronous check; it does not authenticate interactively.
Review source/job status through the owner API or Data Sync. A successful check
sets `connected`; a failed check sets `connection-failed` with a safe notification
and blocks ingest until a later successful check.

Source responses expose only `version`, `path` and optional `clientId` in
`config`. They return neither Apple ID nor credential values nor masked `***`
substitutes. The API schema is `api/openapi.yaml`; canonicalization and merge
rules are in `internal/sourceconfig/icloud.go`.

### Updating And Reconnecting

`PATCH /api/sources/{sourceId}` preserves every omitted credential. Required
fields cannot be `null` or empty; `clientId` may be reset with `""`, not `null`.
Unknown/duplicate fields, wrong casing and trailing JSON are rejected. A path
change can therefore retain all credentials:

```json
{"config":{"path":"Auto Export/Example Updated"}}
```

A canonical configuration change, including a non-secret path/client identity
change, increments generation, sets `checking-connection` and queues a new
check. Display-name/auto-sync-only changes and identical canonical config do not
create a new generation/check. The source type cannot change.

For expired trust, failed authentication or explicit missing PCS/device approval,
repeat external setup and privately PATCH the resulting ready-session values.
The worker cannot request a 2FA code or approve a device. Wait for `connected`
before retrying ingest. Do not attach raw native stderr when asking for help;
use only the safe source/job category.

#### Recovering Expired Authentication

Do **not** delete the application source. Running `rclone config reconnect example-icloud:`
(`example-icloud` is a placeholder for your selected external profile) updates
the external Rclone profile only; Workouts Explorer never rereads that
profile. After completing 2FA and verifying external access, privately submit the
fresh complete cookie string and trust token to the existing source:

```http
PATCH /api/sources/{sourceId}
Content-Type: application/json
Authorization: Bearer <OWNER_SESSION_TOKEN>

{
  "config": {
    "cookies": "<FRESH_COMPLETE_COOKIE_STRING>",
    "trustToken": "<FRESH_TRUST_TOKEN>"
  }
}
```

These placeholders are not usable credentials. Copy external `cookies` to
`cookies` and `trust_token` to `trustToken`. Include `appleId`, the
Rclone-obscured `password`, or `clientId` if those also changed; omitted fields
are preserved. Do not paste credentials into chat, logs or documentation.

A changed config automatically queues verification, regardless of auto-sync.
The PATCH returns a source in `checking-connection`, not a check-job ID. Poll
`GET /api/sources/{sourceId}` until `connected` or `connection-failed`, inspecting
`statusCode`, `statusSummary` and `checkedAt`. Data Sync also exposes these safe
diagnostics; a source-failure banner's **View detail** expands current source
status instead of opening an internal connection check as a sync run.

There is currently no standalone force-recheck endpoint. An identical config
PATCH, toggling auto-sync or renaming the source does not queue another check;
internal connection checks also cannot use the public run-retry endpoint.

An authentication failure leaves `autoSyncEnabled` unchanged but makes the
source ineligible for ingestion. Once the new check succeeds, a source with
auto-sync still enabled becomes eligible for the next scheduled sync without
another toggle. This does not immediately retry a failed ingest or guarantee an
immediate scheduled run. For immediate import, use Manual Sync or
`POST /api/ingest` with `{"sourceIds":["<SOURCE_ID>"]}` after it connects. An
eligible failed ingest can alternatively be retried through
`POST /api/jobs/{jobId}/retry`; the retry snapshots current credentials.

#### Session Lifetime

The [pinned Rclone v1.75.2 documentation](https://github.com/rclone/rclone/blob/v1.75.2/docs/content/iclouddrive.md#configuration)
describes a 30-day trust-token lifetime. Treat this as upstream guidance, not a
guaranteed lifetime or an application expiry countdown. The backend can renew
cookies noninteractively while trust remains valid, and the app persists those
cookie updates safely. It does not renew trust by performing interactive 2FA.

Consequently, auto-sync can stop when Apple requires renewed trust or device
approval. The app reports `connection-failed` / `source-auth-expired` when a
check or ingest detects expiration; there is no proactive expiry timer. Repeat
external reconnect and the credential PATCH above when this happens. Enabling
auto-sync is not a guarantee of indefinitely unattended iCloud authentication.

## Sync Behavior And Limits

- Auto-sync defaults to `AUTO_SYNC_INTERVAL=24h`; an operator may choose `12h`.
  This is distinct from Data Sync UI polling, normally 30 seconds.
- Each sync performs one application-level flat metadata discovery. Native stat
  and download operations may internally list again or make several requests;
  this is not a guarantee of one listing or one HTTP request to Apple.
- Incremental sync skips successful objects whose remote ID, size and modification
  time are unchanged in the same source inventory, unless an ownership-blocked
  epoch is stale (requires 26). Successful checksums are retained, not cleared to
  force retry. It does not hash/download every remote file. An overwrite preserving
  all three metadata values remains undetectable absent forced reprocessing.
  Bounded Manual Sync forces all eligible files in the inclusive date range to
  download and reprocess.
- Discovery is bounded to 100000 entries, 10000 eligible exports and 32 MiB
  metadata stdout. Eligible files require ID, size and valid modification time;
  ambiguous aliases or unsafe metadata fail closed.
- One file per available slot is downloaded, with a 64 MiB per-file hard limit.
  Metadata commands have a 60-second deadline; each download, including its
  pre/post metadata checks, has a five-minute deadline. Native retries are
  bounded (`--retries 2`, `--low-level-retries 2`, one-second retry sleep).
- Parsing and normalization reuse the local pipeline. With the contract requiring
  26, provider-backed local/iCloud identities deduplicate or adopt according to
  [source ownership](#replacing-an-existing-source), not transport-specific IDs.
- Retain the 500m Coverage CPU, two execution lanes, existing admission caps and
  6m/10m/15m route timeout policy. Adoption does not authorize capacity changes or
  certify a real-cloud smoke test; live policy evidence stays in Development Progress.

### Committed-Route Coverage Handoff

The **deployed folded-028** contract uses `worker.coverageBatchSize` /
`WORKER_COVERAGE_BATCH_SIZE`, default 25, range 1-1000. After each file commits and
transport/file-slot cleanup finishes, the source worker prepares normal and
adopted obligations. The runner downloads/processes files serially within a source
job, with cleanup before and after the file pipeline. Files remain atomic.

During active ingest, a normal parent requires **exactly B new eligible matching
targets**, globally across regions within its ingest-root/source-sibling scope.
Missing maps, current/live/unchanged authoritative failed/cancelled targets,
non-GPS inputs and deduplication do not count. Durable private prepared metadata
saves B and root/sibling scope across files/crashes; no placeholder Coverage
parent exists. Full parent and B children seal atomically. For 63 eligible routes
at B=25, 25/25 may seal during ingest, but 13 wait for terminal-root recovery.
Mixed regions are explicitly allowed: optional `coverage.regionIds` reports the
actual child-region union, including retries; `regionId`/`targetGeneration` remain
representative anchors, not complete scope. Per-route vectors/claim/admission stay
unchanged. Missing maps cannot shrink an active-ingest normal parent below B.

SQL counts all children, including terminal/superseded ones, against the parent's
saved capacity. New configuration affects new builders, not existing cohorts.
Retries preserve their chain and include only eligible unsuccessful predecessors,
never fresh top-up routes. For example, a three-route retry with inherited capacity
25 stays separate from a fresh 25-route normal root. Ordinary unchanged imports
cannot reset a failed target's retry budget; distinct adoption alone authorizes
fresh Coverage for otherwise identical content.

Durable receipts survive source failure and cancellation. Only leased account
reconciliation after the **visible ingest root and all source siblings are
terminal and due preparation is exhausted** may seal a final partial cohort,
never active EOF. Failed/cancelled roots may owe legitimate derived work for
committed data without reopening ingest. Active underfull cohorts stay protected;
normal metadata changes/deduplication do not resurrect cancelled route targets or
erase their cancellation footprint. Cancelling ingest does not cancel separate
Coverage jobs. Bounded metadata and one route's compact coordinates at a time
keep APP transactions outside OSM work, with one efficient ROI query per route,
not per point. A trusted fingerprint of **all catalog regions/configuration/
boundaries/active heads** must match in the same Repeatable Read ROI snapshot
and before seal. Mismatch invalidates readiness for fresh ROI, never substitutes
new heads into an old vector. This is not cross-database latest-state atomicity.
Fresh prepare/seal checks lease, source/account,
event, ownership epoch, content and the full canonical raw digest (0-250,000 points).
No-GPS adoption acknowledges without a job. Matching still has its 50,000-original-
point limit; overflow is a typed permanent Coverage failure, not failed ingestion.
Verified typed transient readiness errors durably defer one hour; any permanent,
unknown or cancelled error branch surfaces safely rather than silently deferring.
Policy remains `coverage-production-v1`, 500m / 256 MiB, two lanes, admission 2/2/2,
one per reader, and the same timeout/retry budgets. This is not capacity proof.

The folded lock fix defers four same-name generation constraint triggers with
their exact prior events/WHEN scopes, retaining 017's changed-current-finish-only
Coverage invalidation and immediate account seeding. It removes the early
generation-row fence lock and makes the historic selection helper a nonlocking
same-Repeatable-Read read. Source/epoch/lease and other guards remain intact;
all qualifying counter increments and data commit or roll back together.

New automation requires at least one timeout/verified-transient eligible failed
child, then carries **all authoritative failed siblings** with valid descriptors,
including permanent/unknown/null-code failures, into the next latest-only attempt.
Successes never rerun; cancellations are not automatically revived. All-permanent/
unknown parents stop. The additional automatic count defaults to 1 (0-20), durable
per root; manual retry never resets it and normal failed-target scans cannot
create fresh roots to bypass it. This new policy has not been deployed.

Deployment verification is complete. For an owner-operated next yearly backfill, leave
`autoSyncEnabled=false`, select an inclusive January 1 through December 31 file
range, and review ingestion and Coverage separately before the next batch. A
completed prior batch satisfies the historical readiness gate; no extra import
is needed merely to unlock the pipeline. The next batch was not started at the
deployment checkpoint; later dataset status stays private. This guide submits no
jobs. M8 owner acceptance remains separate.

Known trust/authentication/explicit PCS approval failures during ingest set a
generation-fenced `connection-failed` status, notify the owner and block future
ingest. Transient upstream/network failures fail the job with safe diagnostics
without misdiagnosing authentication or disconnecting an otherwise connected
source. A transient failure during an explicit connection **check** may still
set `connection-failed` under the existing asynchronous check contract.

## Worker Security And Recovery

The runtime contract uses
`WORKER_STAGING_ROOT/private/ingest-<32 lowercase hex digits>/`. The worker-owned
`private` child is needed because a Kubernetes emptyDir mount root may be owned
by UID 0 and writable through `fsGroup`; the adapter requires its own root to be
worker-owned and not group/other-writable. Job directories have `0700` access
permissions (setgid may be inherited); configuration/staged export files are
`0600`. Use per-pod staging, **never a
shared root for independent worker processes** whose scavengers could delete
another worker's active files. The default outer root is
`/var/lib/workouts/staging`.

The actual non-root Alpine bind-root check used UID/GID `0:65532` and mode
`2770` for the outer root. `ScavengeStaging` created the private child as
`65532:65532`, mode `2700`: inherited setgid plus `0700` access permissions.
Synthetic `Source.New` exercised only native version checking and cleaned its
files; this was not full worker startup or an authenticated cloud operation.

The Linux adapter requires `openat2` and `/proc/self/fd` for descriptor-pinned,
no-symlink filesystem access. Each job writes exactly one private native profile;
credentials do not enter argv. Execution is noninteractive with no inherited
stdin, bounded output/deadlines and process-group cancellation. Inherited
`RCLONE*` variables are stripped and HOME, XDG, cache and temporary paths are
job-private. Config backups and native endpoint caches are part of the cleanup
scope. Raw output, profile contents and backend object IDs are not diagnostics.

Only validated refreshed **cookies** can update current source configuration.
The worker independently encrypts that update and uses a source-generation,
job/worker/lease-fenced compare-and-swap. Competing refreshes are first-writer-wins;
owner updates, deletion, cancellation or lease loss make older refreshes harmless
no-ops. Job snapshots stay independently encrypted and immutable. A successful
connection check atomically publishes cookie refresh and `connected`, avoiding a
generation change that would strand the source in `checking-connection`.

Refresh encryption or persistence errors leave the job recoverable rather than
discarding its snapshot or falsely reporting success. A local attempt's
private-file cleanup failure blocks that attempt's terminal writes and that
worker's new claims/readiness until cleanup is confirmed. The gate and startup
scavenging are worker/pod-local, not a distributed cross-pod cleanup or lost-volume
guarantee. Cleanup attempts are bounded: permission, read-only filesystem or
hardware failures cannot be magically repaired. Eventual disposal of per-pod
storage is a recovery boundary, not proof of immediate physical erasure. Logical
database cleanup also does not erase encrypted values already in WAL/backups.

The worker Dockerfile packages the native binary from the pinned 1.75.2 release
archive and checks architecture-specific SHA-256 for both amd64 and arm64. The
full local Buildah worker-image build passed, including all seven commands;
amd64 checksum/version and native execution as UID 65532 inside Alpine were
verified. The local image is `localhost/workouts-worker:m8-verification`; it was
not published or deployed. Arm64 execution, read-only-root-filesystem/seccomp
behavior and full worker startup were not verified by these checks.

## Schema And Verification

Active migrations are **001-028**. Migration 021 introduced iCloud, 022 legacy-route
bootstrap, 023 coordinated readers, and 024-026 provider identity, owner-aware
inventory and adoption. Ordinary static 027 advances 26 to 27; it has no legacy
dispatch or archived-fixture replay. New `028_ingest_coverage_pipeline.sql`
folds the lock fix, strict cohorts and mixed automatic policy into schema/minimum
runtime **28**, retaining `migration_baseline=27` and source support **28**.
The native CLI holds its advisory lock, performs read-only `ValidateHistory`,
then Goose Up. Canonical 27 applies new 028 without replaying 027; native Goose
does not replay applied 028. Existing old-contract 28 needs a **new private guarded
offline repair**, explicit approval and protected actual-backup restore rehearsal
under the actual migration role, with new-source/catalog proof. Pause all old
API/ingest/Coverage runtimes, plus tiles if DDL affects their app constraints.
Old apply-10/defer-6 ABIs are intentionally absent; old runtime-28 readiness fails
and new readiness requires private builders. No rolling reuse of old images is
supported. Preserve ledger 0-28 IDs/timestamps without DML/rewrite, data, matcher
roots/budgets, sources and credentials. Retain old artifacts/backups; incompatible
old-image rollback requires a full old-database restore plus matching old OCI
images. The approved exact actual-profile repair passed protected actual-backup
rehearsal and live application under the real non-superuser migration login;
new readiness and full original-tuple/ledger preservation passed.
Unknown histories fail closed for private preparation, not automatic conversion.
No public old-contract archive/active fixture is added; chart semantic changes
remain frozen/not started. [Development Progress](development-progress.md)
owns live status and publication evidence. Version-20 workers
cannot claim new source work; scheduler leases obtained before upgrade are also fenced
from reading/enqueuing sources or advancing schedules. Historical M7
schema-20/minimum-18 measurements remain historical evidence.
The deployed compatibility patch replaced only migration 022's marked five SQL
functions, preserving schema/Goose 20/minimum runtime 18 and all owners/ACLs; it
did not apply migration 021 or register iCloud sources. The recorded 10/12 results
below tested schema 21 and must not be relabeled as schema-22 verification.

### Verified Results (10/12/2026)

| Scope | Result and boundary |
|---|---|
| Ordinary `make verify` | Pass: Go tests/vet, generation/format, typecheck, Go/UI builds, Helm checks and 303 UI tests; database-backed cases skip when URLs are unset |
| Scoped Go race suite | Pass for sourceconfig, rclonesource, database, API, worker and worker command with database URLs unset; not a PostgreSQL integration receipt |
| Disposable PostgreSQL 18.6 / PostGIS 3.6.4 | M8 worker normal/race, internal full-race, SQL/readiness/race and API integration pass; fresh/repeat/upgrade migrations and runtime-role readiness verified |
| Local worker image/native checks | Full Buildah build; non-root amd64 native version/checksum and 41 top-level tests plus 60 subtests pass without failures/skips; private bind-root check passes |
| Synthetic built-UI browser | Isolated contexts at 1366x900 and 768x1024 pass cloud label, default selection, disabled expired source, exact cloud/local submission and task navigation; zero page errors or real-cloud calls |

See [detailed results and baseline exceptions](implementation-plan.md#verification-results-10122026)
for exact integration counts and the recovery-message fix chronology. Three wider
database failures reproduce on archived schema-20 HEAD; the whole database suite
is not green. The browser used built UI assets with synthetic API fulfillment,
not a deployment or live backend. Temporary receipts are not durable backup
evidence; this documentation records only their scoped summary.

See [contributor verification](../CONTRIBUTING.md#icloud-source-verification)
for commands and disposable-database guards. Owner acceptance remains pending.
An optional owner-operated real-cloud smoke test outside CI and any image
publication/redeployment require explicit authorization; they are not outstanding
implementation blockers or claims of production validation. The separate
owner-account repair/retry verification gate is now satisfied, but does not itself
authorize M8 builds or rollout. The linked reader/telemetry guides record verified
headless DNS/TCP and a separate direct-backend TLS/authentication client, not
in-pod TLS/authentication, mTLS, live-cloud or production acceptance. Reader
activation at that earlier checkpoint was not established by those receipts;
later deployment evidence belongs in Development Progress.
