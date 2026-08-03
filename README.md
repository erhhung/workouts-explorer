# Workouts Explorer

Workouts Explorer is a self-hosted web app for exploring a lifetime of fitness
activities through workout summaries, recorded GPS routes, and interactive maps.
It helps you see where you have exercised, understand how your activity changes
over time, discover which roads and paths you visit most often, and identify
nearby places you have yet to explore.

The app is intended for anyone who records fitness activities, although the
initial implementation supports only Apple Watch workouts exported from Apple
Fitness.

## Milestone 1 Development

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
lazily, remain live during a database outage, and report unready until the first
migration is present:

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

## Database Verification

Infrastructure creates the migration, API, and worker login roles. For local
verification, `compose.yaml` starts PostgreSQL with host trust restricted to the
loopback-published port and initializes separate passwordless development roles;
it is not a production configuration.

```sh
make compose-up
export MIGRATION_DATABASE_URL=postgresql://workouts_migration@127.0.0.1:54329/workouts
export API_DATABASE_URL=postgresql://workouts_api@127.0.0.1:54329/workouts
export WORKER_DATABASE_URL=postgresql://workouts_worker@127.0.0.1:54329/workouts
make migration-test
make compose-down
```

The migration command holds a PostgreSQL advisory lock and is idempotent. API
and worker never migrate on startup. Kubernetes receives these URLs from an
existing Secret selected by Helm values; the chart never creates credentials.
The first migration fails unless `workouts_api` and `workouts_worker` already
exist as non-superuser `NOBYPASSRLS` login roles. Job status, attempts, leases,
cancellation, and parent derivation are changed through the `app.*_job`
state-machine functions rather than direct runtime-role updates.

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

The test uses kubeconfig context `xdev` by default. Context `xtest` targets the
separate test vCluster and can be selected with `VCLUSTER_CONTEXT=xtest`. It
installs ephemeral PostgreSQL in namespace `postgresql` and the application in
namespace `workouts-explorer`. The database uses `emptyDir` and trust
authentication strictly for isolated vCluster testing. Application database URLs
use the cross-namespace service name
`postgres.postgresql.svc.cluster.local`.

The Helm release enables the nginx Ingress at
`workouts.<context>.fourteeners.local`, so the defaults produce
`workouts.xdev.fourteeners.local` and `workouts.xtest.fourteeners.local`.
Override `VCLUSTER_HOST` only when the vCluster DNS convention differs. The test
runs the migration Job, waits for the UI, API, worker, and Certificate, probes
services inside the cluster, and verifies the external HTTPS UI, runtime config,
and Swagger routes. `VCLUSTER_APP_NAMESPACE` and `VCLUSTER_RELEASE` remain
available for intentionally isolated application releases. Helm and rollout
waits default to 12 minutes because the NFS-backed homelab Harbor registry can
take up to 10 minutes when all pods pull concurrently; override
`VCLUSTER_TIMEOUT` only when needed.

`scripts/create-vcluster-cert.sh dev` and
`scripts/create-vcluster-cert.sh test` show the effective cert-manager resources
for the two vClusters after host Kyverno mutation. The chart's Certificate
template mirrors that policy: `StepClusterIssuer/step-issuer`, ECDSA P-256,
90-day duration, 7-day renewal window, and Secret
`workouts-explorer-ingress-tls`. Both the Certificate and Ingress must be enabled
for vCluster sync-to-host. The virtual Ingress always references that exact
Secret name. In the host namespace, vCluster rewrites the Ingress reference to
`workouts-explorer-ingress-tls-x-<namespace>-x-<vcluster>` but does not rewrite a
synced Certificate's `spec.secretName`; the test harness supplies the translated
Certificate target explicitly so cert-manager and the host Ingress agree. For a
private image project, create a pull Secret in `workouts-explorer` and set
`WORKOUTS_TEST_IMAGE_PULL_SECRET` to its name.

## Packaging

`make helm` validates and renders the chart with ingress disabled. Each
Dockerfile produces a non-root OCI image through Buildah. Run `make images` to
build all three images with the eight-character Git commit tag and the semver in
`VERSION`. The API image also contains `/app/migrate` for the Argo CD `PreSync`
migration Job. CI uses the same Buildah target and coordinated tags.
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
