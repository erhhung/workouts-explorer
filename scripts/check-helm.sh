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
if grep -q '/mailpit' "$rendered" || grep -q 'kind: ExternalName' "$rendered"; then
  printf '%s\n' 'Default production rendering unexpectedly exposes Mailpit' >&2
  exit 1
fi
if [ "$(grep -c 'mountPath: /var/run/secrets/workouts-source' "$rendered")" -ne 2 ] ||
   ! grep -q 'SOURCE_KEYRING_FILE:' "$rendered" ||
   ! grep -q 'LOCAL_SOURCE_ROOTS:' "$rendered"; then
  printf '%s\n' 'API and worker source encryption configuration is incomplete' >&2
  exit 1
fi
helm template workouts-explorer helm \
  --set sources.nfs.enabled=true \
  --set-string sources.nfs.server=qnap.fourteeners.local \
  --set-string sources.nfs.path=/k8s_data/datasets/workouts/samples \
  --set-string sources.nfs.mountPath=/data/workouts/samples >"$changed"
if ! grep -q 'supplementalGroups: \[1000\]' "$changed" ||
   ! grep -q 'server: qnap.fourteeners.local' "$changed" ||
   ! grep -q 'path: /k8s_data/datasets/workouts/samples' "$changed" ||
   ! grep -q 'mountPath: /data/workouts/samples' "$changed"; then
  printf '%s\n' 'Read-only worker NFS source mount is incomplete' >&2
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
  --set-string ingress.host=workouts.xdev.fourteeners.local \
  --set ingress.mailpit.enabled=true >"$rendered"
if ! grep -q 'path: /mailpit' "$rendered" ||
   ! grep -q 'type: ExternalName' "$rendered" ||
   ! grep -q 'externalName: mailpit.mailpit.svc.cluster.local' "$rendered"; then
  printf '%s\n' 'Development Mailpit proxy path or ExternalName Service is missing' >&2
  exit 1
fi
helm template workouts-explorer helm \
  --set ingress.enabled=true \
  --set-string ingress.className=nginx \
  --set-string ingress.host=workouts.xdev.fourteeners.local \
  --set-string ingress.tlsSecretName=virtual-ingress-tls \
  --set-string ingress.certificateSecretName=translated-host-certificate >"$rendered"
if ! grep -q 'kind: Certificate' "$rendered" ||
   ! grep -q 'secretName: virtual-ingress-tls' "$rendered" ||
   ! grep -q 'secretName: translated-host-certificate' "$rendered"; then
  printf '%s\n' 'Ingress and Certificate Secret targets are not independently rendered' >&2
  exit 1
fi

helm template workouts-explorer helm --set api.bootstrap.enabled=true >"$rendered"
if ! grep -q 'name: BOOTSTRAP_DATABASE_URL' "$rendered"; then
  printf '%s\n' 'Bootstrap Job does not use its one-shot database credential' >&2
  exit 1
fi
if ! grep -q 'helm.sh/hook: post-install,post-upgrade' "$rendered" ||
   ! grep -q -- '--password-minimum=12' "$rendered"; then
  printf '%s\n' 'Bootstrap Job lacks controlled upgrade execution or password policy' >&2
  exit 1
fi

helm template workouts-explorer helm --set api.roleProvisioning.enabled=true >"$rendered"
if ! grep -q 'name: ROLE_PROVISIONING_DATABASE_URL' "$rendered" ||
   ! grep -q 'argocd.argoproj.io/sync-wave: "-3"' "$rendered" ||
   ! grep -q 'helm.sh/hook-weight: "-3"' "$rendered"; then
  printf '%s\n' 'Role provisioning Job is not independently credentialed before migration' >&2
  exit 1
fi
