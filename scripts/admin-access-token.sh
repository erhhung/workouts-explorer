#!/usr/bin/env bash
set -euo pipefail

vcluster_name="${1:-dev}"

HOMELAB_DOMAIN=fourteeners.local
VCLUSTER_KUBE_CONTEXT=x${vcluster_name}
WORKOUTS_NAMESPACE=workouts-explorer
WORKOUTS_API_URL=https://workouts.x${vcluster_name}.$HOMELAB_DOMAIN/api

admin_username="$(
  kubectl --context "$VCLUSTER_KUBE_CONTEXT" -n "$WORKOUTS_NAMESPACE" \
    get secret workouts-explorer-bootstrap-admin \
    -o jsonpath='{.data.username}' | base64 -d
)"
admin_password="$(
  kubectl --context "$VCLUSTER_KUBE_CONTEXT" -n "$WORKOUTS_NAMESPACE" \
    get secret workouts-explorer-bootstrap-admin \
    -o jsonpath='{.data.password}' | base64 -d
)"

curl --fail --silent \
    -H 'Content-Type: application/json' \
    --data "$(jq -n \
      --arg username "$admin_username" \
      --arg password "$admin_password" \
      '{
         username: $username,
         password: $password
       }')" \
    "$WORKOUTS_API_URL/session-tokens" | \
  jq -r '.accessToken'
