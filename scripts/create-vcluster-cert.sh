#!/usr/bin/env bash
set -euo pipefail

vcluster_name="${1:-dev}"

HOST_KUBE_CONTEXT=homelab
INGRESS_NAME=workouts-explorer-ingress
HOMELAB_DOMAIN=fourteeners.local
WORKOUTS_UI_SUBDOMAIN=workouts

kubectl --context ${HOST_KUBE_CONTEXT} \
  create --dry-run=server -o yaml -f - <<EOF | \
  yq '.metadata |= {"name":.name}'

# below is technically not a valid Certificate resource, but Kyverno
# "standard-tls-certificate" MutatingPolicy in the host cluster will
# add the missing specs to create a valid Certificate for our domain

apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${INGRESS_NAME}
spec:
  dnsNames:
    - ${WORKOUTS_UI_SUBDOMAIN}.x${vcluster_name}.${HOMELAB_DOMAIN}
  secretName: ${INGRESS_NAME}-tls
  duration: 2160h
  renewBefore: 168h
EOF
