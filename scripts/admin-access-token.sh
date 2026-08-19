#!/usr/bin/env bash
set -euo pipefail

vcluster_name="${1:-dev}"

HOMELAB_DOMAIN=fourteeners.local
VCLUSTER_KUBE_CONTEXT=x${vcluster_name}
WORKOUTS_NAMESPACE=workouts-explorer
ADMIN_SECRET_NAME=workouts-explorer-bootstrap-admin
WORKOUTS_API_URL=https://workouts.x${vcluster_name}.$HOMELAB_DOMAIN/api

kubectl="kubectl --context $VCLUSTER_KUBE_CONTEXT -n $WORKOUTS_NAMESPACE"

$kubectl get secret $ADMIN_SECRET_NAME &> /dev/null || {
  echo >&2 "$ADMIN_SECRET_NAME Secret not found!"
  exit 1
}
admin_username="$(
  $kubectl get secret $ADMIN_SECRET_NAME \
    -o jsonpath='{.data.username}' | base64 -d
)"
admin_password="$(
  $kubectl get secret $ADMIN_SECRET_NAME \
    -o jsonpath='{.data.password}' | base64 -d
)"

jo username="$admin_username" \
   password="$admin_password" | \
  curl --fail --silent \
    -H 'Content-Type: application/json' \
    --data-binary @- \
    "$WORKOUTS_API_URL/session-tokens" | \
  jq -r '.accessToken'

unset admin_password
