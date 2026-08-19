#!/usr/bin/env bash
set -euo pipefail

context="${VCLUSTER_CONTEXT:-xdev}"
app_namespace="${VCLUSTER_APP_NAMESPACE:-workouts-explorer}"
database_namespace="postgresql"
release="${VCLUSTER_RELEASE:-workouts-dev}"
registry="${WORKOUTS_TEST_IMAGE_REGISTRY:-${CI_REGISTRY_PATH:-}}"
tag="${WORKOUTS_TEST_IMAGE_TAG:-dev-$(date +%Y%m%d)}"
host="${VCLUSTER_HOST:-workouts.${context}.fourteeners.local}"
timeout="${VCLUSTER_TIMEOUT:-12m}"
vcluster_name="${context#x}"
certificate_secret="workouts-explorer-ingress-tls-x-${app_namespace}-x-${vcluster_name}"
: "${registry:?WORKOUTS_TEST_IMAGE_REGISTRY or CI_REGISTRY_PATH is required}"
: "${SMTP_HOST:?SMTP_HOST is required}"
: "${SMTP_PORT:?SMTP_PORT is required}"
: "${SMTP_USER:?SMTP_USER is required}"
: "${SMTP_PASSWORD:?SMTP_PASSWORD is required}"
smtp_from_address="${SMTP_FROM_ADDRESS:-workouts@fourteeners.local}"
osm_database_url="${OSM_DATABASE_URL:-}"
if [[ -z "$osm_database_url" && -f /tmp/prod-pg-url ]]; then
  IFS= read -r osm_line </tmp/prod-pg-url
  osm_database_url="${osm_line#osmDatabaseUrl: }"
fi
case "$osm_database_url" in
  postgresql://*'/osm?'*) ;;
  *) printf '%s\n' 'OSM_DATABASE_URL or /tmp/prod-pg-url must contain the osm database URL' >&2; exit 1 ;;
esac

if ! kubectl config get-contexts "$context" >/dev/null 2>&1; then
  printf 'kubectl context %s does not exist\n' "$context" >&2
  exit 1
fi

kubectl --context "$context" apply -f deploy/dev/postgres.yaml
kubectl --context "$context" -n "$database_namespace" rollout status statefulset/postgresql --timeout="$timeout"
# PVCs initialized by the former custom bootstrap user may not have a postgres role.
if ! kubectl --context "$context" -n "$database_namespace" exec statefulset/postgresql -- \
  psql -U postgres -d workouts -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null 2>&1; then
  kubectl --context "$context" -n "$database_namespace" exec statefulset/postgresql -- \
    psql -U workouts_migration -d workouts -v ON_ERROR_STOP=1 -c \
    'CREATE ROLE postgres LOGIN SUPERUSER CREATEDB CREATEROLE;'
fi
kubectl --context "$context" -n "$database_namespace" exec -i statefulset/postgresql -- \
  psql -U postgres -d workouts -v ON_ERROR_STOP=1 <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_migration') THEN
    CREATE ROLE workouts_migration LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_api') THEN
    CREATE ROLE workouts_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_worker') THEN
    CREATE ROLE workouts_worker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_tiles') THEN
    CREATE ROLE workouts_tiles LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_security_owner') THEN
    CREATE ROLE workouts_security_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
END
$$;
ALTER ROLE workouts_migration LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
ALTER ROLE workouts_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
ALTER ROLE workouts_worker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
ALTER ROLE workouts_tiles LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE workouts_security_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE EXTENSION IF NOT EXISTS postgis;
ALTER DATABASE workouts OWNER TO workouts_migration;
GRANT CONNECT, TEMPORARY ON DATABASE workouts TO workouts_migration;
GRANT USAGE, CREATE ON SCHEMA public TO workouts_migration;
GRANT workouts_security_owner TO workouts_migration WITH ADMIN OPTION;
SQL

kubectl --context "$context" create namespace "$app_namespace" --dry-run=client -o yaml | kubectl --context "$context" apply -f -
kubectl --context "$context" -n "$app_namespace" get secret workouts-explorer-database-tls >/dev/null
database_host="postgresql.${database_namespace}.svc.cluster.local"
kubectl --context "$context" -n "$app_namespace" create secret generic workouts-explorer-database \
  --from-literal=migrationDatabaseUrl="postgresql://workouts_migration@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=apiDatabaseUrl="postgresql://workouts_api@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=workerDatabaseUrl="postgresql://workouts_worker@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=tilesDatabaseUrl="postgresql://workouts_tiles@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=osmDatabaseUrl="$osm_database_url" \
  --dry-run=client -o yaml | kubectl --context "$context" apply -f -
rate_limit_key="$(openssl rand -base64 32 | tr -d '=\n')"
kubectl --context "$context" -n "$app_namespace" create secret generic workouts-explorer-security \
  --from-literal=rateLimitKey="$rate_limit_key" \
  --from-literal=smtpPassword="$SMTP_PASSWORD" \
  --dry-run=client -o yaml | kubectl --context "$context" apply -f -
if ! kubectl --context "$context" -n "$app_namespace" get secret workouts-explorer-source-encryption >/dev/null 2>&1; then
  source_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
  source_keyring="{\"activeKeyId\":\"xdev-primary-v1\",\"keys\":{\"xdev-primary-v1\":\"${source_key}\"}}"
  kubectl --context "$context" -n "$app_namespace" create secret generic workouts-explorer-source-encryption \
    --from-literal=keyring.json="$source_keyring"
  unset source_key source_keyring
fi
bootstrap_rotate_existing=false
if ! kubectl --context "$context" -n "$app_namespace" get secret workouts-explorer-bootstrap-admin >/dev/null 2>&1; then
  bootstrap_password="$(openssl rand -base64 30 | tr -d '\n')"
  kubectl --context "$context" -n "$app_namespace" create secret generic workouts-explorer-bootstrap-admin \
    --from-literal=username=xdev-admin \
    --from-literal=email=xdev-admin@example.test \
    --from-literal=password="$bootstrap_password"
  unset bootstrap_password
  bootstrap_rotate_existing=true
fi

# Reset stale pull backoff/progress conditions and repull mutable dev tags before
# Helm waits on an existing release. This is a no-op for the first installation.
if kubectl --context "$context" -n "$app_namespace" get deployment/workouts-explorer-ui >/dev/null 2>&1; then
  kubectl --context "$context" -n "$app_namespace" rollout restart \
    deployment/workouts-explorer-ui \
    deployment/workouts-explorer-api \
    deployment/workouts-explorer-worker
fi

helm_args=(
  upgrade --install "$release" helm
  --kube-context "$context"
  --namespace "$app_namespace"
  --set-string "image.registry=$registry"
  --set-string "image.tag=$tag"
  --set image.pullPolicy=Always
  --set-string "api.publicUrl=https://${host}"
  --set api.localDevelopment=false
  --set-string "api.smtp.address=${SMTP_HOST}:${SMTP_PORT}"
  --set-string "api.smtp.fromAddress=${smtp_from_address}"
  --set-string "api.smtp.username=${SMTP_USER}"
  --set api.smtp.allowInsecureLocal=false
  --set api.bootstrap.enabled=true
  --set "api.bootstrap.rotateExistingPassword=${bootstrap_rotate_existing}"
  --set ingress.enabled=true
  --set-string ingress.className=nginx
  --set-string "ingress.host=${host}"
  --set-string "ingress.certificateSecretName=${certificate_secret}"
  --set ingress.mailpit.enabled=false
  --set sources.nfs.enabled=true
  --set-string sources.nfs.server=qnap.fourteeners.local
  --set-string sources.nfs.path=/k8s_data/datasets/workouts
  --set-string sources.nfs.mountPath=/data/workouts
  --set-string sources.localRoots[0]=/data/workouts
  --set sources.nfs.supplementalGroup=1000
  --wait
  --wait-for-jobs
  --timeout "$timeout"
)

if [[ -n "${WORKOUTS_TEST_IMAGE_PULL_SECRET:-}" ]]; then
  helm_args+=(--set-string "image.pullSecrets[0].name=$WORKOUTS_TEST_IMAGE_PULL_SECRET")
fi
if [[ -n "${WORKOUTS_TEST_VALUES_FILE:-}" ]]; then
  if [[ ! -f "$WORKOUTS_TEST_VALUES_FILE" ]]; then
    printf 'WORKOUTS_TEST_VALUES_FILE does not exist: %s\n' "$WORKOUTS_TEST_VALUES_FILE" >&2
    exit 1
  fi
  helm_args+=(-f "$WORKOUTS_TEST_VALUES_FILE")
fi

helm "${helm_args[@]}"

kubectl --context "$context" -n "$app_namespace" rollout status deployment/workouts-explorer-ui --timeout="$timeout"
kubectl --context "$context" -n "$app_namespace" rollout status deployment/workouts-explorer-api --timeout="$timeout"
kubectl --context "$context" -n "$app_namespace" rollout status deployment/workouts-explorer-worker --timeout="$timeout"
kubectl --context "$context" -n "$app_namespace" wait \
  --for=condition=Ready certificate/workouts-explorer-ingress --timeout="$timeout"

kubectl --context "$context" -n "$app_namespace" run workouts-smoke \
  --image=docker.io/curlimages/curl:8.17.0 \
  --restart=Never \
  --rm \
  --attach \
  --quiet \
  --command -- sh -eu -c '
    curl --fail --silent --show-error http://workouts-explorer-ui/health/live >/dev/null
    curl --fail --silent --show-error http://workouts-explorer-ui/health/ready >/dev/null
    curl --fail --silent --show-error http://workouts-explorer-api/health/live >/dev/null
    curl --fail --silent --show-error http://workouts-explorer-api/health/ready >/dev/null
    curl --fail --silent --show-error http://workouts-explorer-api/api/config >/dev/null
    curl --fail --silent --show-error http://workouts-explorer-api/swagger >/dev/null
  '

curl --fail --silent --show-error --retry 60 --retry-all-errors --retry-delay 5 "https://${host}/" >/dev/null
curl --fail --silent --show-error --retry 60 --retry-all-errors --retry-delay 5 "https://${host}/api/config" >/dev/null
curl --fail --silent --show-error --retry 60 --retry-all-errors --retry-delay 5 "https://${host}/swagger" >/dev/null

kubectl --context "$context" -n "$database_namespace" delete \
  deployment/workouts-postgres service/postgres --ignore-not-found
kubectl --context "$context" -n "$app_namespace" get pods
unset osm_database_url osm_line
