#!/bin/sh
set -eu

rendered="$(mktemp)"
changed="$(mktemp)"
trap 'rm -f "$rendered" "$changed"' EXIT
helm lint helm
helm template workouts-explorer helm >"$rendered"
if ! grep -q 'checksum/config:' "$rendered"; then
  printf '%s\n' 'API Deployment is missing the ConfigMap checksum rollout annotation' >&2
  exit 1
fi
helm template workouts-explorer helm --set api.publicConfig.pollingIntervalSeconds=31 >"$changed"
checksum="$(awk '$1 == "checksum/config:" {print $2}' "$rendered")"
changed_checksum="$(awk '$1 == "checksum/config:" {print $2}' "$changed")"
if [ -z "$checksum" ] || [ "$checksum" = "$changed_checksum" ]; then
  printf '%s\n' 'API ConfigMap changes do not alter the Deployment rollout checksum' >&2
  exit 1
fi
helm template workouts-explorer helm \
  --set ingress.enabled=true \
  --set-string ingress.className=nginx \
  --set-string ingress.host=workouts.xdev.fourteeners.local >"$rendered"
if ! grep -q 'kind: Certificate' "$rendered" ||
   ! grep -q 'secretName: workouts-explorer-ingress-tls' "$rendered"; then
  printf '%s\n' 'Ingress rendering is missing its Certificate or fixed TLS Secret' >&2
  exit 1
fi
