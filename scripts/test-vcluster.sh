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

if ! kubectl config get-contexts "$context" >/dev/null 2>&1; then
  printf 'kubectl context %s does not exist\n' "$context" >&2
  exit 1
fi

kubectl --context "$context" apply -f deploy/dev/postgres.yaml
kubectl --context "$context" -n "$database_namespace" rollout status deployment/workouts-postgres --timeout="$timeout"

kubectl --context "$context" create namespace "$app_namespace" --dry-run=client -o yaml | kubectl --context "$context" apply -f -
database_host="postgres.${database_namespace}.svc.cluster.local"
kubectl --context "$context" -n "$app_namespace" create secret generic workouts-explorer-database \
  --from-literal=migrationDatabaseUrl="postgresql://workouts_migration@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=apiDatabaseUrl="postgresql://workouts_api@${database_host}:5432/workouts?sslmode=disable" \
  --from-literal=workerDatabaseUrl="postgresql://workouts_worker@${database_host}:5432/workouts?sslmode=disable" \
  --dry-run=client -o yaml | kubectl --context "$context" apply -f -

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
  --set ingress.enabled=true
  --set-string ingress.className=nginx
  --set-string "ingress.host=${host}"
  --set-string "ingress.certificateSecretName=${certificate_secret}"
  --wait
  --wait-for-jobs
  --timeout "$timeout"
)

if [[ -n "${WORKOUTS_TEST_IMAGE_PULL_SECRET:-}" ]]; then
  helm_args+=(--set-string "image.pullSecrets[0].name=$WORKOUTS_TEST_IMAGE_PULL_SECRET")
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

kubectl --context "$context" -n "$app_namespace" get pods
