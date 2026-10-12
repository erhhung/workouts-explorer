#!/usr/bin/env bash
set -euo pipefail

root="$(dirname "$(dirname "$(realpath "$0")")")"
context=xdev
namespace=workouts-acceptance
host=acceptance-postgres.workouts-acceptance.svc.cluster.local
password=acceptance-only
action="${1:-help}"

[[ "${COVERAGE_ACCEPTANCE_DISPOSABLE:-}" == true ]] || {
  printf '%s\n' 'Refusing: COVERAGE_ACCEPTANCE_DISPOSABLE=true is required.' >&2
  exit 1
}
# Never accept ambient live URLs, even when a caller requests the disposable switch.
for name in \
  DATABASE_URL PRODUCTION_DATABASE_URL \
  PRODUCTION_OSM_DATABASE_URL \
  MIGRATION_DATABASE_URL \
  WORKER_DATABASE_URL \
  COVERAGE_WORKER_DATABASE_URL \
  API_DATABASE_URL \
  API_OSM_DATABASE_URL \
  OSM_DATABASE_URL \
  OSM_MIGRATION_DATABASE_URL \
  COVERAGE_WORKER_OSM_DATABASE_URL \
  COVERAGE_ACCEPTANCE_DATABASE_URL \
  COVERAGE_ACCEPTANCE_OSM_DATABASE_URL; do
  value="${!name:-}"
  [[ -z "$value" || "$value" =~ ^postgresql://workouts_[a-z_]+:acceptance-only@(127\.0\.0\.1:15432|acceptance-postgres\.workouts-acceptance\.svc\.cluster\.local:5432)/(acceptance_(test|benchmark|test_osm)|osm)\?sslmode=disable$ ]] || {
    printf 'Refusing non-disposable %s\n' "$name" >&2
    exit 1
  }
done

COVERAGE_ACCEPTANCE_PROFILE="$(node "$root/deploy/acceptance/profile.cjs" name)"
export COVERAGE_ACCEPTANCE_PROFILE

k() { kubectl --context "$context" -n "$namespace" "$@"; }

fixture_test() {
  COVERAGE_ACCEPTANCE_DATABASE_URL="postgresql://workouts_migration:$password@127.0.0.1:15432/acceptance_benchmark?sslmode=disable" \
  COVERAGE_ACCEPTANCE_OSM_DATABASE_URL="postgresql://workouts_migration:$password@127.0.0.1:15432/osm?sslmode=disable" \
  COVERAGE_ACCEPTANCE_API_URL=http://127.0.0.1:18081 \
  node "$root/deploy/acceptance/run-fixture.cjs" "$1"
}

pause_workers() {
  [[ "$(k get namespace "$namespace" -o jsonpath='{.metadata.labels.workouts-disposable}')" == true ]] || exit 1

  general_replicas="$(k get deployment/workouts-explorer-worker -o jsonpath='{.spec.replicas}')"
  coverage_replicas="$(k get deployment/workouts-explorer-coverage-worker -o jsonpath='{.spec.replicas}')"

  restore_workers() {
    k scale deployment/workouts-explorer-worker --replicas="$general_replicas"
    k scale deployment/workouts-explorer-coverage-worker --replicas="$coverage_replicas"
  }
  trap restore_workers EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  k scale deployment/workouts-explorer-coverage-worker deployment/workouts-explorer-worker --replicas=0

  # A successful scale is not a terminated consumer. Wait for every old pod,
  # including terminating pods, before creating or claiming fixture leases.
  for component in worker coverage-worker; do
    for ((attempt=0;attempt<120;attempt++)); do
      pods="$(k get pods -l "app.kubernetes.io/instance=workouts-acceptance,app.kubernetes.io/component=$component" -o name)"
      [[ -n "$pods" ]] || break
      sleep 1
    done
    [[ -z "$pods" ]] || {
      printf 'Timed out stopping %s\n' "$component" >&2
      exit 1
    }
  done
}

case "$action" in
  forward|seed|validate|reads|viewport|queue|diagnostics|general-worker|matcher-smoke|collect)
    node "$root/deploy/acceptance/profile.cjs" require ;;
esac

case "$action" in
  provision)
    ! k get namespace "$namespace" >/dev/null 2>&1 || {
      printf '%s\n' 'Namespace already exists; refusing reinitialization.' >&2
      exit 1
    }
    kubectl --context "$context" apply -f "$root/deploy/acceptance/postgres.yaml"
    binding="$(node "$root/deploy/acceptance/profile.cjs" binding)"
    k annotate namespace "$namespace" "workouts-acceptance/profile=$binding"
    node "$root/deploy/acceptance/profile.cjs" configmap | k create -f -
    k rollout status deployment/acceptance-postgres --timeout=5m

    k exec -i deployment/acceptance-postgres -- psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE workouts_migration LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD 'acceptance-only';
CREATE DATABASE acceptance_benchmark OWNER workouts_migration;
CREATE DATABASE acceptance_test OWNER workouts_migration;
CREATE DATABASE osm OWNER workouts_migration;
CREATE DATABASE acceptance_test_osm OWNER workouts_migration;
SQL
    uid="$(k get namespace "$namespace" -o jsonpath='{.metadata.uid}')"
    k exec -i deployment/acceptance-postgres -- psql -U postgres -d acceptance_benchmark -v ON_ERROR_STOP=1 -v "binding=$binding" -v "uid=$uid" <<'SQL'
CREATE TABLE public.acceptance_profile (singleton boolean PRIMARY KEY CHECK (singleton), binding jsonb NOT NULL);
INSERT INTO public.acceptance_profile VALUES (true, :'binding'::jsonb || jsonb_build_object('namespace_uid', :'uid'));
REVOKE ALL ON public.acceptance_profile FROM PUBLIC;
SQL
    k exec -i deployment/acceptance-postgres -- psql -U postgres -d acceptance_benchmark -v ON_ERROR_STOP=1 < "$root/scripts/postgresql-init.sql"
    k exec -i deployment/acceptance-postgres -- psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
ALTER ROLE workouts_api PASSWORD 'acceptance-only';
ALTER ROLE workouts_worker PASSWORD 'acceptance-only';
ALTER ROLE workouts_coverage_worker PASSWORD 'acceptance-only';
ALTER ROLE workouts_tiles PASSWORD 'acceptance-only';
GRANT workouts_security_owner TO workouts_migration WITH ADMIN OPTION;
SQL
    for db in acceptance_benchmark acceptance_test osm acceptance_test_osm; do
      k exec deployment/acceptance-postgres -- psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -c 'CREATE EXTENSION IF NOT EXISTS postgis; CREATE EXTENSION IF NOT EXISTS pg_stat_statements; GRANT USAGE,CREATE ON SCHEMA public TO workouts_migration;'
    done

    k create secret generic workouts-explorer-database \
      --from-literal="migrationDatabaseUrl=postgresql://workouts_migration:$password@$host:5432/acceptance_benchmark?sslmode=disable" \
      --from-literal="apiDatabaseUrl=postgresql://workouts_api:$password@$host:5432/acceptance_benchmark?sslmode=disable" \
      --from-literal="workerDatabaseUrl=postgresql://workouts_worker:$password@$host:5432/acceptance_benchmark?sslmode=disable" \
      --from-literal="coverageWorkerDatabaseUrl=postgresql://workouts_coverage_worker:$password@$host:5432/acceptance_benchmark?sslmode=disable" \
      --from-literal="tilesDatabaseUrl=postgresql://workouts_tiles:$password@$host:5432/acceptance_benchmark?sslmode=disable" \
      --from-literal="osmDatabaseUrl=postgresql://workouts_migration:$password@$host:5432/osm?sslmode=disable" \
      --from-literal="osmReaderDatabaseUrl=postgresql://workouts_coverage_worker:$password@$host:5432/osm?sslmode=disable"

    k create secret generic workouts-explorer-security --from-literal=rateLimitKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
    k create secret generic workouts-explorer-source-encryption --from-literal='keyring.json={"activeKeyId":"acceptance","keys":{"acceptance":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}'
    # No TLS is used on this emptyDir fixture. The mount is required by the chart.
    k create secret generic workouts-explorer-database-tls --from-file=ca.crt=/etc/ssl/certs/ca-certificates.crt
    node "$root/deploy/acceptance/basemap.cjs" | k apply --server-side -f -
    node "$root/deploy/acceptance/source.cjs" | k apply -f -
    k apply -f "$root/deploy/acceptance/basemap.yaml"
    k apply -f "$root/deploy/acceptance/telemetry.yaml"

    # The runtime readiness contract requires a database literally named osm.
    # Migrate and populate that scratch DB before waiting for runtime readiness.
    k run acceptance-osm-init --restart=Never --image=harbor.fourteeners.local/library/workouts-worker:dev-20261001@sha256:937c20cec9821868ce45c246470d178d5950e15858e733cca4804d809e415131 \
      --override-type=strategic --overrides='{"spec":{"containers":[{"name":"acceptance-osm-init","resources":{"requests":{"cpu":"25m","memory":"64Mi"},"limits":{"cpu":"250m","memory":"256Mi"}}}]}}' \
      --env="OSM_MIGRATION_DATABASE_URL=postgresql://workouts_migration:$password@$host:5432/osm?sslmode=disable" --command -- /app/osm-migrate
    k wait --for=jsonpath='{.status.phase}'=Succeeded pod/acceptance-osm-init --timeout=5m
    k exec -i deployment/acceptance-postgres -- psql -U workouts_migration -d osm -v ON_ERROR_STOP=1 < "$root/deploy/acceptance/graph.sql"

    "${COVERAGE_ACCEPTANCE_HELM:-helm}" upgrade --install workouts-acceptance "$root/helm" \
      --kube-context "$context" --namespace "$namespace" -f "$root/deploy/acceptance/values.yaml" \
      --post-renderer "$root/deploy/acceptance/post-render.cjs" --wait --wait-for-jobs --timeout 10m

    # Separate item-5 databases receive the same compiled migrations, never fixture jobs.
    for spec in \
      'test-app /app/migrate MIGRATION_DATABASE_URL acceptance_test api' \
      'test-osm /app/osm-migrate OSM_MIGRATION_DATABASE_URL acceptance_test_osm worker'; do
      read -r name command env db image <<<"$spec"

      digest=295e9338d2b02f56b32df74c11303b873d2da14c2984b85b5fa42df8bf4e3274
      [[ "$image" != worker ]] || digest=937c20cec9821868ce45c246470d178d5950e15858e733cca4804d809e415131

      k run "acceptance-$name" --restart=Never --image="harbor.fourteeners.local/library/workouts-$image:dev-20261001@sha256:$digest" \
        --override-type=strategic --overrides="{\"spec\":{\"containers\":[{\"name\":\"acceptance-$name\",\"resources\":{\"requests\":{\"cpu\":\"25m\",\"memory\":\"64Mi\"},\"limits\":{\"cpu\":\"250m\",\"memory\":\"256Mi\"}}}]}}" \
        --env="$env=postgresql://workouts_migration:$password@$host:5432/$db?sslmode=disable" --command -- "$command"
      k wait --for=jsonpath='{.status.phase}'=Succeeded "pod/acceptance-$name" --timeout=5m
    done
    k exec -i deployment/acceptance-postgres -- psql -U postgres -d acceptance_test_osm -v ON_ERROR_STOP=1 < "$root/deploy/acceptance/reader-grants.sql"
    ;;
  forward)
    [[ "$(k get namespace "$namespace" -o jsonpath='{.metadata.labels.workouts-disposable}')" == true ]] || exit 1
    exec node "$root/deploy/acceptance/forward-supervisor.cjs"
    ;;
  env)
    printf '%s\n' 'export COVERAGE_ACCEPTANCE_DISPOSABLE=true' 'export COVERAGE_ACCEPTANCE_API_URL=http://127.0.0.1:18081' 'export COVERAGE_ACCEPTANCE_UI_URL=http://localhost:18080'
    printf 'export COVERAGE_ACCEPTANCE_PROFILE=%s\n' "$COVERAGE_ACCEPTANCE_PROFILE"
    for spec in 'MIGRATION_DATABASE_URL migration acceptance_test' 'WORKER_DATABASE_URL worker acceptance_test' 'COVERAGE_WORKER_DATABASE_URL coverage_worker acceptance_test' 'API_DATABASE_URL api acceptance_test' 'TILES_DATABASE_URL tiles acceptance_test' 'OSM_MIGRATION_DATABASE_URL migration acceptance_test_osm' 'COVERAGE_WORKER_OSM_DATABASE_URL coverage_worker acceptance_test_osm' 'COVERAGE_ACCEPTANCE_DATABASE_URL migration acceptance_benchmark' 'COVERAGE_ACCEPTANCE_OSM_DATABASE_URL migration osm'; do
      read -r name role db <<<"$spec"
      printf 'export %s="postgresql://workouts_%s:%s@127.0.0.1:15432/%s?sslmode=disable"\n' "$name" "$role" "$password" "$db"
    done
    ;;
  token)
    node -e 'const c=require("node:crypto");console.log(c.createHash("sha256").update("FAKE disposable synthetic coverage acceptance bearer; never a live credential").digest("base64url"))'
    ;;
  seed)
    pause_workers
    # Fresh post-rendered deployments intentionally start at zero. Restore the
    # general worker after seed, keeping Coverage paused for baseline reads.
    general_replicas=1
    fixture_test seed
    ;;
  validate)
    fixture_test validate
    ;;
  reads)
    phase="${COVERAGE_ACCEPTANCE_PHASE:-warm}"
    [[ "$phase" =~ ^(baseline|backlog48|backlog96|warm)$ ]] || exit 1
    COVERAGE_ACCEPTANCE_PHASE="$phase" fixture_test reads
    ;;
  queue)
    [[ "${COVERAGE_ACCEPTANCE_BACKLOG:-48}" =~ ^(48|96)$ ]] || exit 1
    pause_workers
    general_replicas=1
    coverage_replicas=1
    COVERAGE_ACCEPTANCE_BACKLOG="${COVERAGE_ACCEPTANCE_BACKLOG:-48}" fixture_test queue
    ;;
  diagnostics)
    fixture_test diagnostics
    ;;
  viewport)
    fixture_test viewport
    ;;
  general-worker)
    fixture_test general-worker
    ;;
  matcher-smoke)
    pause_workers
    fixture_test matcher-smoke
    ;;
  collect)
    node "$root/deploy/acceptance/collect.cjs" "${COVERAGE_ACCEPTANCE_PHASE:-final}"
    ;;
  cleanup)
    [[ "${COVERAGE_ACCEPTANCE_CLEANUP_CONFIRMED:-}" == yes ]] || {
      printf '%s\n' 'Cleanup requires COVERAGE_ACCEPTANCE_CLEANUP_CONFIRMED=yes.' >&2
      exit 1
    }
    label="$(k get namespace "$namespace" --ignore-not-found -o jsonpath='{.metadata.labels.workouts-disposable}')"
    [[ -z "$label" || "$label" == true ]] || exit 1
    [[ -z "$label" ]] || node "$root/deploy/acceptance/profile.cjs" require --namespace-only
    node "$root/deploy/acceptance/forward-supervisor.cjs" stop
    [[ -z "$label" ]] || k delete namespace "$namespace"
    ;;
  *) printf '%s\n' 'Usage: COVERAGE_ACCEPTANCE_DISPOSABLE=true scripts/coverage-acceptance.sh provision|forward|env|token|seed|validate|reads|viewport|queue|diagnostics|general-worker|matcher-smoke|collect|cleanup (Helm 3 required)' ;;
esac
