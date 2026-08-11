# Contributing to Workouts Explorer

This guide covers local development, database verification, and packaging for
contributors to Workouts Explorer.

## Development

The repository is a coordinated monorepo with a React/Vite SPA in `ui/`, a Go
API in `api/`, a separate Go worker in `worker/`, shared Go infrastructure in
`internal/`, and a namespace-agnostic chart in `helm/`. The design-first API
contract is `api/openapi.yaml`; `api/generated/` is generated and checked for
drift.

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
then limited to loopback or the development Mailpit service on port 1025 with no
credentials. `PUBLIC_URL` is an HTTPS origin with no path, query, or fragment;
only explicit local development may use HTTP, and then only on loopback.
`PASSWORD_MINIMUM_LENGTH` accepts 12 through 64, `PAGE_SIZE_MAXIMUM` accepts 25
through 1000, `SESSION_LIFETIME` accepts 5 minutes through 24 hours, and
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

## Database Verification

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

The migration command holds a PostgreSQL advisory lock and is idempotent. API
and worker never migrate on startup. Kubernetes receives these URLs from an
existing Secret selected by Helm values; the chart never creates credentials.
Schema 9 requires the PostGIS extension to be installed by a database
administrator before migration. Migrations fail unless `workouts_api`,
`workouts_worker`, and `workouts_tiles` already exist as non-superuser
`NOBYPASSRLS` login roles and `workouts_security_owner` exists as a non-login,
non-bypass function owner. Job status, attempts, leases,
cancellation, and parent derivation are changed through the `app.*_job`
state-machine functions rather than direct runtime-role updates.

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
authentication to match `pgTileserv.databaseSecret`.

For a development container without a local container runtime, the same topology
can run in a dedicated vCluster. Publish the three images and run the test with:

```sh
make publish-dev-images
make vcluster-test
```

The image registry defaults to `CI_REGISTRY_PATH` when
`WORKOUTS_TEST_IMAGE_REGISTRY` is unset. Both commands default to one
`dev-YYYYMMDD` tag based on the local date. Repeated builds on the same day
overwrite that tag, and the vCluster test forces a rollout so Kubernetes repulls
the updated images. Publication deletes older `dev-*` Harbor tags for these three
repositories while preserving commit-SHA and release tags. Set
`WORKOUTS_TEST_IMAGE_TAG` only when an intentionally different tag is needed.

The test uses kubeconfig context `xdev` by default. It installs Mailpit in
namespace `mailpit`, persistent PostgreSQL in namespace `postgresql`, and the
application in namespace `workouts-explorer`. PostgreSQL requests a 5 GiB
Longhorn volume that is retained across normal redeploys. The test also creates
development-only rate-limit and bootstrap Secrets and bootstraps an `xdev-admin`
identity so invite and recovery mail can be inspected without external delivery.
Context `xtest` targets the separate test vCluster and can be selected with
`VCLUSTER_CONTEXT=xtest`. The database uses trust authentication strictly for
isolated vCluster testing. Application database URLs use
`postgresql.postgresql.svc.cluster.local`, and development SMTP uses
`mailpit.mailpit.svc.cluster.local:1025`.

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

The Helm release enables the nginx Ingress at
`workouts.<context>.fourteeners.local`, so the defaults produce
`workouts.xdev.fourteeners.local` and `workouts.xtest.fourteeners.local`.
Override `VCLUSTER_HOST` only when the vCluster DNS convention differs. The test
runs the migration Job, waits for the UI, API, worker, and Certificate, probes
services inside the cluster, and verifies the external HTTPS UI, runtime config,
Swagger, and Mailpit routes. The development release enables
`https://workouts.<context>.fourteeners.local/mailpit/`. Mailpit is configured
with that web root, and ingress-nginx reaches its cross-namespace Service through
a same-namespace `ExternalName` proxy Service. In a vCluster, the harness points
that proxy to the host-synced Mailpit Service name because host ingress-nginx
cannot resolve virtual-cluster DNS. This path and proxy are disabled by default,
so production renders neither when using real SMTP.
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

## Packaging

`make helm` validates and renders the chart with ingress disabled. Each
Dockerfile produces a non-root OCI image through Buildah. Run `make images` to
build all three images with the eight-character Git commit tag and the semver in
`VERSION`. The API image also contains `/app/provision-roles`, `/app/migrate`,
and `/app/bootstrap-admin` for ordered Argo CD/Helm hook Jobs. CI uses the same
Buildah target and coordinated tags.
Before either `make images` or `make publish-dev-images` builds, the workflow
removes older local tags for the three Workouts Explorer images and runs
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
