# Contributing to Workouts Explorer

This guide covers local development, database verification, and packaging for
contributors to Workouts Explorer.

## Development

The repository is a coordinated monorepo with a React/Vite SPA in `ui/`, a Go
API in `api/`, a separate Go worker in `worker/`, shared Go infrastructure in
`internal/`, and a namespace-agnostic chart in `helm/`. The design-first API
contract is `api/openapi.yaml`; `api/generated/` is generated and checked for
drift.

Keep `helm/` limited to the chart, values, templates and packaged assets. Go
chart-rendering and deployment-manifest tests live in `tests/helm/`; run them with
`go test ./tests/helm`. They are also included by `go test ./...` and `make verify`.

Committed code and documentation should describe Workouts Explorer and the
infrastructure interfaces it needs, not unrelated applications or their resource
inventories. Shared infrastructure provisioning belongs to its owning automation;
keep only app-specific configuration, integration requirements and examples here.

Install UI dependencies and run the normal local verification:

```sh
npm ci --prefix ui
make verify
```

The API and worker intentionally require distinct role URLs. They connect
lazily, remain live during a database outage, and report unready until the
first migration is present:

```sh
API_DATABASE_URL="$API_ROLE_URL" go run ./api/cmd/api
WORKER_DATABASE_URL="$WORKER_ROLE_URL" go run ./worker/cmd/worker
npm --prefix ui run dev
```

Vite proxies `/api` and `/swagger` to the API at `localhost:8080`. The public UI
configuration is an allowlist and never includes database or cluster-internal
settings. OTel export is disabled when `OTEL_EXPORTER_OTLP_ENDPOINT` is empty.
Application logs default to JSON. Set `LOG_FORMAT=text` directly, or
`logging.format: text` in Helm, to use plain text consistently for direct and
package-level `slog` calls.
Public base-map templates must use HTTP(S), cannot contain user information, and
reject loopback, private, `.local`, and `.internal` hosts. Local development may
set `ALLOW_LOCAL_BASE_MAP=true` only while `PUBLIC_URL` is itself local.

Swagger UI is pinned through the Go module and served entirely beneath
`/swagger`; it does not fetch scripts or styles from a CDN. API request bodies
are limited to 1 MiB before OpenAPI validation.

The API requires `RATE_LIMIT_KEY` as unpadded base64 for at least 32 random
bytes, authenticated STARTTLS SMTP settings, and a private regular file selected
by `SMTP_PASSWORD_FILE`. Only explicit local development may set
`LOCAL_DEVELOPMENT=true` and `SMTP_ALLOW_INSECURE_LOCAL=true`; plaintext SMTP is
then limited to loopback on port 1025 with no
credentials. `PUBLIC_URL` is an HTTPS origin with no path, query, or fragment;
only explicit local development may use HTTP, and then only on loopback.
`BROWSER_SIGNIN_ORIGINS` optionally contains up to 16 comma-separated origins,
must include `PUBLIC_URL`, and permits the same deployment to accept browser
sign-in from explicit alternate hosts. Cookies remain host-only and are not
shared between those origins.
`PASSWORD_MINIMUM_LENGTH` accepts 12 through 64, `PAGE_SIZE_MAXIMUM` accepts 25
through 1000, and `SESSION_LIFETIME` accepts 5 minutes through 24 hours and
defaults to 6 hours. The browser session has a fixed expiry and no refresh token.
`TRUSTED_PROXY_CIDRS` defaults to empty. Invitation and reset
links use `PUBLIC_URL`; authentication never infers its origin from forwarding
headers.

Normal API and worker startup never create an administrator. After migration,
run the one-shot command with a private password file whose exact bytes meet
the password policy:

```sh
BOOTSTRAP_DATABASE_URL="$MIGRATION_OR_ADMIN_ROLE_URL" go run ./api/cmd/bootstrap-admin -- \
  --username administrator --email administrator@example.com \
  --password-file /run/secrets/bootstrap-password --password-minimum 12
```

The command is idempotent only when the sole active administrator's canonical
username, email, and verified password all match. Helm can run the same command
from an existing Secret with `api.bootstrap.enabled=true`; it uses the separately
selected `api.bootstrap.databaseKey`, not the long-running API credential. Helm
runs it after install or a controlled upgrade migration, and the command never
reconciles or rotates credentials. Argo CD reruns the idempotent verification
while bootstrap remains enabled. Because the chart does not own operator
Secrets, disable bootstrap and delete the external bootstrap Secret after the
first successful run.

Milestone 2 exposes invitation creation and one-time registration only.
Invitation listing, resend, and revocation remain sequenced for Milestone 9.

## Code Formatting

Run `make format` to apply `gofmt` to Go and the pinned Prettier to JS/TS/React
and CSS source. Run `make format-check` (or the check-only alias `make fmt`)
before submitting changes; `make verify` includes this check.

See [`docs/source-code-formatting.md`](docs/source-code-formatting.md) for the
configuration, source scope, UI-only commands, and generated/data exclusions.
The unified guide also covers [SQL formatting](docs/source-code-formatting.md#sql)
and [documentation dates](docs/source-code-formatting.md#documentation-dates).

## Database Verification

SQL formatting conventions, Goose directive requirements, dollar-quote tags,
and the repository policy for using `pg_format` are documented in
[the SQL section of the formatting guide](docs/source-code-formatting.md#sql).
Apply it to application migrations, OSM migrations, and psql pipeline scripts.

Infrastructure installs PostGIS and creates the migration, API, worker, and tile
login roles. For local verification, `compose.yaml` starts PostgreSQL 18 with
PostGIS 3.6, restricts host trust to the loopback-published port, and initializes
separate passwordless development roles; it is not a production configuration.

```sh
make compose-up
export MIGRATION_DATABASE_URL=postgresql://workouts_migration@127.0.0.1:54329/workouts
export API_DATABASE_URL=postgresql://workouts_api@127.0.0.1:54329/workouts
export WORKER_DATABASE_URL=postgresql://workouts_worker@127.0.0.1:54329/workouts
export TILE_DATABASE_URL=postgresql://workouts_tiles@127.0.0.1:54329/workouts
make migration-test
make compose-down
```

When a verification requires a temporary Docker container but the Docker daemon
is unavailable, try a disposable Buildah container before skipping the check.
Buildah is daemonless: create a working container with `buildah from`, copy or
mount the required test artifacts, and use `buildah run` to start the service and
execute the verification in the same container invocation. This is particularly
useful for PostgreSQL/PostGIS migration tests because the database can listen on
its container-local Unix socket. Remove the working container and temporary test
artifacts after the run. Do not place credentials in the image, command output,
or repository.

The migration command holds a PostgreSQL advisory lock and is idempotent. API
and worker never migrate on startup. Kubernetes receives these URLs from an
existing Secret selected by Helm values; the chart never creates credentials.
The user-approved active API sequence is **001-028**, with **no active 029**.
Ordinary static 027 and 001-027 stay unchanged; new 028 folds the lock fix, strict
eligible cohorts and mixed-failure automatic policy into the pipeline. Source
`SupportedSchemaVersion`, schema and minimum runtime are **28**, marker **27**.
The folded contract, exact parking repair and UI follow-up are **deployed in
`dev-20261020`, Helm revision 29**, schema/Goose/minimum runtime 28, marker 27.
The native command validates history read-only, then runs Goose Up; canonical 27
applies new 028 without replaying 027. Native Goose **does not replay an applied
028**, and matching numeric versions do not prove runtime compatibility.

Old-contract 28 needs a new **private guarded offline repair**, explicit operator
approval, fresh protected backup and rehearsal on a protected restore of an
actual application backup using the actual migration role, new source and catalog
proof. Pause all old API/ingest/Coverage runtimes, plus tiles if DDL affects their
application constraints. Old apply-10/defer-6 ABIs are intentionally absent; old
runtime-28 readiness fails and new runtime-28 readiness requires private builders.
Do not claim a rolling DB-only upgrade or reuse old images. Preserve ledger 0-28
IDs/timestamps without DML/rewrite, application data, retry roots/budgets, sources
and credentials. Retain old images/backups; rollback needs the old DB contract
and images, not images alone. The current release's separately approved exact
actual-profile repair passed protected actual-backup rehearsal and live
transactional application under the real non-superuser migration login with its
existing security-owner SET capability. Full old-tuple/ledger preservation and
new readiness passed; native Goose confirmed applied 028 without replay. This
frozen-corpus qualification is not generic SQL-lexer equivalence or permission to
weaken exact repair guards. Old artifacts were deleted only after verified local
rollback exports; incompatible-image rollback requires the whole old database
restore plus matching old OCI images. Final yearly closure and M8 owner acceptance
are separate from deployment. All release-manifest source/chart files stay frozen;
for this documentation-only update, edit only the Helm README template and
regenerate its README, not values or runtime chart files.
See the [migration and recovery contract](docs/database-migrations.md) before deployment.
Schema 9 requires the PostGIS extension to be installed by a database
administrator before migration. Migrations fail unless `workouts_api`,
`workouts_worker`, and `workouts_tiles` already exist as non-superuser
`NOBYPASSRLS` login roles and `workouts_security_owner` exists as a non-login,
non-bypass function owner. Job status, attempts, leases,
cancellation, and parent derivation are changed through the `app.*_job`
  state-machine functions rather than direct runtime-role updates.

For folded-028 verification, distinguish deferred commit-time invalidation from job
retries. Four same-name triggers keep their exact events/WHEN scopes and
per-qualifying-event increments; the account seed remains immediate. PostgreSQL
can flush them early with `SET CONSTRAINTS`, although production does not. Ingest
keeps its missing-generation `55000` and all identity/source/job/lease/epoch/runtime
and KEY SHARE fences. Map generation and selected data must use one Repeatable
Read snapshot through the historic, now nonlocking helper; current-generation
validation rejects a concurrently stale response. Commit failures roll back
data/checkpoints/evidence/progress with the generation. Commit tails still
serialize; no worker transaction retry or deadline budget is added.

Active ingest seals a normal parent only with saved B (default 25, range 1-1000)
**new eligible matching targets** globally across regions within its ingest-root/
source-sibling scope. Missing maps, current/live/unchanged failed/cancelled targets
and deduplication do not count. Durable prepared metadata survives files/crashes;
there is no placeholder parent before atomic full-B sealing. Only account recovery
after the visible root and all source siblings are terminal and due preparation is
exhausted may seal a final partial cohort, not active EOF. Keep metadata bounded,
one coordinate route at a time, and require the full trusted catalog fingerprint
in the ROI Repeatable Read lookup and before seal; mismatch requires fresh ROI.
Mixed-region parent anchors are representative; optional `coverage.regionIds`
reports the actual union, including retries. Per-route vectors/admission stay intact.

One timeout/verified-transient eligible failed child enables carrying **all
authoritative failed siblings**, including permanent/unknown/null-code failures,
into the next automatic attempt if every failed descriptor remains valid. Do not
rerun successes, revive cancellations or fork latest-only ancestry. All-permanent
parents stop. The durable per-root automatic allowance defaults to one additional
attempt (0-20); manual retries and normal reconciliation never reset it.

Earlier isolated lock/commit/snapshot and Coverage tests are scoped synthetic
evidence, not a fresh pass of the final folded-028 contract, repair or broad DB
suite. Unrelated scheduler/stale-target and broader API fixture failures remain
documented in
[Development Progress](docs/development-progress.md#deferred-generation-fix-source-only).
Never use deployed databases or private source profiles to rerun fixture-writing
tests. Documentation work needs no live credentials, cloud calls, migration,
image publication or cluster commands. Chart semantics remain frozen/not started;
edit only values comments and the README template, then regenerate with pinned
`helm-docs` as described in [the chart README](helm/README.md#regenerate-documentation).

For an existing Milestone 1 database, infrastructure must provision the new
function-owner role before migration 00002. Use a temporary external credential
that has `CREATEROLE`; normal migration, API, and worker credentials must not
have that attribute:

```sh
ROLE_PROVISIONING_DATABASE_URL="$ROLE_PROVISIONER_URL" \
  go run ./api/cmd/provision-roles
```

The fixed-purpose command creates or verifies `workouts_security_owner` as
`NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS` and
`workouts_tiles` as a login role with the same capability restrictions, then
grants only the owner role to `workouts_migration`. It rejects an unsafe existing
role rather than repairing it. Helm's equivalent
`api.roleProvisioning.enabled=true` Job uses a separate external Secret and runs
before migration on install/upgrade and Argo CD PreSync. Enable it for initial
provisioning and upgrades that introduce a role, including M1-to-M2 and M5-to-M6,
then disable it and remove the external CREATEROLE credential Secret. The
operator remains responsible for configuring the tile role's database
authentication to match `tileServer.databaseSecret`.

For a development container without a local container runtime, the same topology
can run in a dedicated vCluster. Publish the four images and run the test with:

```sh
make publish-dev-images
make vcluster-test
```

The vCluster test requires `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, and
`SMTP_PASSWORD`. Set `SMTP_FROM_ADDRESS` when the authenticated username is not a
valid sender address; otherwise xdev defaults to `workouts@fourteeners.local`.
It also requires `workouts-explorer-database-tls` in the application namespace
and either `OSM_DATABASE_URL` or `/tmp/prod-pg-url` containing the shared OSM
connection URL.

xdev intentionally keeps `workouts-explorer-bootstrap-admin` so
`scripts/admin-access-token.sh dev` can authenticate the durable `xdev-admin`
identity. The harness creates that Secret only when absent, uses a generated
password, and explicitly rotates a matching existing xdev administrator during
Secret regeneration. This is a development-only exception to the production
rule that removes one-shot bootstrap credentials after initialization.

The image registry defaults to `CI_REGISTRY_PATH` when
`WORKOUTS_TEST_IMAGE_REGISTRY` is unset. Both commands default to one
`dev-YYYYMMDD` tag based on the local date. Repeated builds on the same day
overwrite that tag, and the vCluster test forces a rollout so Kubernetes repulls
the updated images. See [Development image publication](#development-image-publication)
for component selection, explicit tags, and Harbor cleanup behavior.

The test uses kubeconfig context `xdev` by default. Persistent PostgreSQL runs in
namespace `postgresql`, and the application runs in namespace
`workouts-explorer`. Use the externally provisioned authenticated STARTTLS service
at `smtp.fourteeners.local`, configured by `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, and
`SMTP_PASSWORD`. PostgreSQL requests a 5 GiB
Longhorn volume that is retained across normal redeploys. The test also creates
development-only rate-limit and bootstrap Secrets and bootstraps an `xdev-admin`
identity for recovery verification.
Context `xtest` targets the separate test vCluster and can be selected with
`VCLUSTER_CONTEXT=xtest`. The database uses trust authentication strictly for
isolated vCluster testing. Application database URLs use
`postgresql.postgresql.svc.cluster.local`. The SMTP password belongs only in the
`workouts-explorer-security` Secret; never put it in Helm values or checked-in
manifests.

**WARNING: The following fresh-reset procedure permanently destroys all
development database data.** Stop and delete the StatefulSet before deleting
its PVC, then recreate PostgreSQL by rerunning the vCluster test:

```sh
context=xdev
kubectl --context "$context" -n postgresql delete statefulset postgresql
kubectl --context "$context" -n postgresql delete pvc data-postgresql-0
VCLUSTER_CONTEXT="$context" make vcluster-test
```

Set `context` to the intended vCluster before running the destructive procedure.
Normal `make vcluster-test` runs never delete the StatefulSet PVC.

The Helm release enables the nginx Ingress at `ingress.host` and every explicit
`ingress.additionalHosts` entry. The test defaults produce
`workouts.xdev.fourteeners.local` and `workouts.xtest.fourteeners.local`.
Override `VCLUSTER_HOST` only when the vCluster DNS convention differs. The test
runs the migration Job, waits for the UI, API, worker, and Certificate, probes
services inside the cluster, and verifies the external HTTPS UI, runtime config,
and Swagger routes. Invitation and recovery mail use the configured external SMTP service.
`VCLUSTER_APP_NAMESPACE` and `VCLUSTER_RELEASE` remain available for
intentionally isolated application releases. Helm and rollout waits default to
12 minutes because the NFS-backed homelab Harbor registry can take up to 10
minutes when all pods pull concurrently; override `VCLUSTER_TIMEOUT` only when
needed.

`scripts/create-vcluster-cert.sh dev` and
`scripts/create-vcluster-cert.sh test` show the effective cert-manager resources
for the two vClusters after host Kyverno mutation. The chart's Certificate
template mirrors that policy: `StepClusterIssuer/step-issuer`, ECDSA P-256,
90-day duration and 7-day renewal window. `ingress.tlsSecretName` is the Secret
referenced by the virtual or ordinary Ingress and defaults to
`workouts-explorer-ingress-tls`; `ingress.certificateSecretName` is the
Certificate target and defaults to the same value. Both resources must be
enabled for vCluster sync-to-host. In the host namespace, vCluster rewrites
the Ingress reference to
`workouts-explorer-ingress-tls-x-<namespace>-x-<vcluster>` but does not rewrite
a synced Certificate's `spec.secretName`; the xdev harness overrides only
`certificateSecretName` with that translated host target while leaving the
virtual Ingress `tlsSecretName` unchanged. For a
private image project, create a pull Secret in `workouts-explorer` and set
`WORKOUTS_TEST_IMAGE_PULL_SECRET` to its name.

## iCloud Source Verification

Read [iCloud Source Setup](docs/icloud-source-setup.md) before working on M8.
Implementation and synthetic verification are ready for owner review; owner
acceptance and the milestone completion checkbox remain pending. Do not use real
Apple credentials or host rclone configs for automated tests. Helpers use
synthetic processes/exports; no cloud or deployment command is needed.

From the repository root, run the focused synthetic suites without inherited
application database URLs:

```sh
go test ./internal/sourceconfig ./internal/rclonesource
env -u API_DATABASE_URL -u WORKER_DATABASE_URL -u MIGRATION_DATABASE_URL \
  go test ./api ./worker -run 'TestICloud|TestManifestIdentityOptional|TestRunnerTransportOptions' -count=1
```

The adapter's `TestActualRcloneVersion` skips if no native binary is installed;
when installed, it must be 1.75.2. That check uses `--config /dev/null version`,
not cloud access or host configuration. Do not interpret skipped database or
native checks as passing integration/packaging evidence.

For the migration suite, provision standard application roles and an **empty,
disposable loopback database** named `workouts_icloud_test_*`. Its admin URL must
be able to `SET ROLE` to the test roles. Set `SCRATCH_ADMIN_URL` privately to that
database only; never reuse a deployed or personal-data database. The suite checks
the empty-database precondition, creates PostGIS by default and exercises the
complete 020-to-021 upgrade, CAS, status, runtime-20 and pre-upgrade scheduler
lease fences. It intentionally does not fall back to application database URLs.

```sh
ICLOUD_TEST_DISPOSABLE=1 ICLOUD_TEST_DATABASE_URL="$SCRATCH_ADMIN_URL" \
  go test ./api/migrations -run '^TestICloudMigrationIntegration$' -count=1
ICLOUD_TEST_DISPOSABLE=1 ICLOUD_READINESS_TEST_DATABASE_URL="$SCRATCH_ADMIN_URL" \
  go test ./internal/database -run '^TestICloudReadinessIntegration$' -count=1
```

Readiness requires a **fully migrated schema-21** scratch database and uses
transaction-local rolled-back faults. It may use the full migration suite's
resulting database. For a separate limited source-foundations run, use another
empty scratch database and add `ICLOUD_TEST_SOURCE_FOUNDATIONS_ONLY=1` to the
migration command: this runs 001-006 plus 021 without PostGIS, not the complete
upgrade or full-runtime readiness contract.

API and worker lifecycle integration tests use the existing role-URL convention,
not the disposable-flag guard. Explicitly point **every** URL below at a fully
migrated disposable database with its corresponding role before running them;
they write fixtures and worker tests configure file-slot limits.

```sh
API_DATABASE_URL="$SCRATCH_API_ROLE_URL" MIGRATION_DATABASE_URL="$SCRATCH_MIGRATION_ROLE_URL" \
  go test ./api -run '^TestICloudSourceLifecycleIntegration$' -count=1
WORKER_DATABASE_URL="$SCRATCH_WORKER_ROLE_URL" MIGRATION_DATABASE_URL="$SCRATCH_MIGRATION_ROLE_URL" \
  go test ./worker -run '^TestICloudWorkerPostgreSQL$' -count=1
```

### Recorded Verification (10/12/2026)

| Scope | Result |
|---|---|
| Final `make verify` | Exit 0, including Go tests/vet, generation/format, typecheck, Go/UI builds, Helm and 303 UI tests; database-backed cases skip when URLs are unset |
| Scoped race suite | Exit 0 for sourceconfig, rclonesource, database, API, worker and worker command with database URLs unset |
| Disposable PostGIS M8 | Actual PostgreSQL 18.6 / PostGIS 3.6.4 worker normal/race, internal full-race, SQL/readiness/race and API integration receipts pass; fresh/repeat/upgrade and runtime-role readiness checked |
| Local image/native/browser | Full worker Buildah build, amd64 native non-root checks, private bind-root check and synthetic built-UI desktop/tablet flows pass within their recorded boundaries |

The [detailed M8 results](docs/implementation-plan.md#verification-results-10122026)
record exact counts, the message-only recovery-copy fix chronology and three
wider database failures reproduced on isolated archived schema-20 HEAD. The
ordinary suite's database skips are not integration passes; the wider database
suite is not wholly green. PostgreSQL was not rerun after the recovery-message
fix; final API unit/race checks were. Temporary receipts and the ignored browser
harness are local artifacts, not durable backup evidence.

Cleanup claim/readiness gating and scavenging are worker/pod-local: a local
failure blocks that attempt's terminal writes and that worker's new claims/readiness until
confirmed cleanup. They do not guarantee distributed cleanup, lost-volume
recovery or immediate physical erasure under permanent filesystem faults.
Arm64 execution, read-only-root-filesystem/seccomp behavior and full worker
startup were not verified. Owner acceptance remains pending; optional
owner-operated cloud smoke tests outside CI and image publication/redeployment
need explicit authorization, not further implementation work. No production
validation is claimed.

## OSM Reader Verification

Read [OSM Read Replicas](docs/osm-read-replicas.md) for the global default-off
boolean policy, independent complete reader references
(`api.coverageDiagnostics.databaseSecret`, `worker.osmDatabaseSecret` and
`coverageWorker.osmDatabaseSecret`, each defaulting to name
`workouts-explorer-database` and key `osmReaderDatabaseUrl`), separate writer
name/key/TLS selectors, headless TLS and
lag/admission behavior. Documentation checks do not require an app build, private
values/keyring/profile reads, live account tools or cluster changes.

The boolean never selects a Secret or overrides an endpoint. Each reader reference
is independent of application-database references and the other readers, even
when default names coincide. Removed key-only properties have no compatibility
aliases and must fail schema validation; operators privately convert local
overrides to explicit name/key references using the guide's historical mapping
and a complete fresh values file, not `--reuse-values`. Shared Secrets can retain
application, reader and writer keys; dedicated reader roles can instead use
separate Secrets with existing reader TLS selectors. Operator CLI database
environment options select writers independently of Helm reader references,
without parsing private values. Documentation work must not read or edit the
private local values file.

Focused synthetic/configuration/chart checks use public source and fixtures:

```sh
go test ./internal/config -run 'TestOSMUseReadReplicasOnly|TestOSMReadReplicasRejectAutoAddRegions|TestLoadOSMDoesNotRequireCommonConfiguration' -count=1
go test ./internal/database -run '^TestOSMReader' -count=1
go test ./internal/osm -run '^TestClassifyMatcherReplicaGenerations$' -count=1
go test ./tests/helm -run '^TestHelmOSMReadReplica' -count=1
env -u COVERAGE_ACCEPTANCE_DISPOSABLE \
  go test ./worker -run '^TestCoverageReplica|^TestRecoverableCoverageReaderErrors$' -count=1
```

Helm rendering tests require the Helm binary; a skip is not rendering evidence.
Worker database-backed cases skip without the disposable guard. To run those
integration cases, explicitly provision isolated, fully migrated application/OSM
databases and roles under the existing [Coverage acceptance guards](docs/coverage-acceptance.md).
The fixture requires `COVERAGE_ACCEPTANCE_DISPOSABLE=true` and all six role URLs:
`MIGRATION_DATABASE_URL`, `API_DATABASE_URL`, `WORKER_DATABASE_URL`,
`COVERAGE_WORKER_DATABASE_URL`, `OSM_MIGRATION_DATABASE_URL` and
`COVERAGE_WORKER_OSM_DATABASE_URL`. There are no URL fallbacks; the OSM reader must
be read-only. Never point these fixture-writing tests at a deployed database.
Run `go test ./worker -run '^TestCoverageReplica.*Integration$' -count=1` only
after those guards are privately satisfied. They simulate generation lag, not a
physical HA cluster or concurrent-capacity benchmark. Live DNS/TLS/authentication
probes and any image publication/rollout need separate authorization.

## Packaging

### Development image publication

`scripts/publish-dev-images.sh` accepts zero or more component names from `api`,
`ui`, `worker`, and `osm`. With no component arguments, it builds and pushes all
four images using the default local-date tag `dev-YYYYMMDD`:

```sh
./scripts/publish-dev-images.sh
```

Pass one or more space-separated component names to publish only those images:

```sh
./scripts/publish-dev-images.sh osm
./scripts/publish-dev-images.sh api worker
./scripts/publish-dev-images.sh api ui worker osm
```

`make publish-dev-images` invokes the script with no component arguments, so it
always publishes all four images. It then runs the UI artifact check. Use the
script directly for a component subset; component names are not Make arguments.

Set `WORKOUTS_IMAGE_TAG` to build and push a specific tag instead of the
default date-derived tag:

```sh
WORKOUTS_IMAGE_TAG=feature-map-fix \
  ./scripts/publish-dev-images.sh api ui

WORKOUTS_IMAGE_TAG=release-candidate-2 make publish-dev-images
```

Use the same variable to replace an older tag that already exists in Harbor.
This is useful when a running Job or deployment deliberately references a stable
development tag rather than today's `dev-YYYYMMDD` tag:

```sh
WORKOUTS_IMAGE_TAG=dev-20260925 \
  ./scripts/publish-dev-images.sh osm
```

The push moves that Harbor tag to the newly built image. Workloads using a
mutable tag must have `imagePullPolicy: Always` and must be recreated or rolled
out before they pull the replacement digest; existing pods continue running
their original digest.

Before building, publication removes older `dev-*` Harbor tags from each
selected component repository while preserving commit-SHA and release tags. The
value of `WORKOUTS_IMAGE_TAG` is the one tag retained. Consequently,
republishing an older tag such as `dev-20260925` deletes other `dev-*` tags,
including newer date tags, from the selected repositories. It does not clean
repositories for components omitted from the command.

`make helm` validates and renders the chart with ingress disabled. Each
Dockerfile produces a non-root OCI image through Buildah. Run `make images` to
build all four images with the eight-character Git commit tag and the semver in
`VERSION`. The API image also contains `/app/provision-roles`, `/app/migrate`,
and `/app/bootstrap-admin` for ordered Argo CD/Helm hook Jobs. CI uses the same
Buildah target and coordinated tags.
Before either `make images` or `make publish-dev-images` builds, the workflow
removes older local tags for the four Workouts Explorer images and runs
`buildah rmi --prune`. This keeps Buildah's VFS layer store within the dev
container's inode and storage limits without touching unrelated local images.
Trusted `main` pushes publish both tags to Harbor using the
`CI_REGISTRY`, `CI_REGISTRY_PATH`, `CI_REGISTRY_USER`, and
`CI_REGISTRY_PASSWORD` environment variables. Authenticate manually without
printing the password using:

```sh
printf '%s' "$CI_REGISTRY_PASSWORD" | buildah login "$CI_REGISTRY" \
  --username "$CI_REGISTRY_USER" --password-stdin
```

Pull requests build but never authenticate or push.
