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
if [ "$(grep -c 'name: LOG_FORMAT' "$rendered")" -lt 3 ] ||
   ! grep -q 'LOG_FORMAT: "json"' "$rendered" ||
   helm template workouts-explorer helm --set-string logging.format=console >/dev/null 2>&1; then
  printf '%s\n' 'Global application log format configuration is incomplete' >&2
  exit 1
fi
if ! grep -q 'name: workouts-explorer-tiles' "$rendered" ||
   ! grep -q 'type: ClusterIP' "$rendered" ||
   ! grep -q 'image: ghcr.io/maplibre/martin:1.15.0@sha256:' "$rendered" ||
   ! grep -q 'TILE_SERVER_URL:' "$rendered" ||
   ! grep -q 'source_id_format: "{schema}.{function}"' "$rendered" ||
   ! grep -q 'cache: disable' "$rendered" ||
   ! grep -q 'pool_size: 4' "$rendered" ||
   ! grep -q 'key: tilesDatabaseUrl' "$rendered"; then
  printf '%s\n' 'Internal Martin deployment, service, or credential wiring is incomplete' >&2
  exit 1
fi
for setting in 'MAP_TILE_CONCURRENCY: "4"' 'MAP_TILE_QUEUE_SIZE: "64"' \
  'MAP_TILE_CACHE_BYTES: "33554432"' 'MAP_TILE_CACHE_ENTRIES: "512"' \
  'MAP_TILE_CACHE_TTL: "30s"' 'MAP_TILE_QUEUE_TIMEOUT: "1s"' 'MAP_TILE_REQUEST_TIMEOUT: "6s"'; do
  if ! grep -Fq "$setting" "$rendered"; then
    printf '%s\n' "Private API tile configuration is missing $setting" >&2
    exit 1
  fi
done
helm template workouts-explorer helm \
  --set api.mapTiles.concurrency=1,api.mapTiles.queueSize=0,api.mapTiles.cacheBytes=0,api.mapTiles.cacheEntries=1 \
  --set-string api.mapTiles.cacheTtl=5m,api.mapTiles.queueTimeout=100ms,api.mapTiles.requestTimeout=5s >"$changed"
for setting in 'MAP_TILE_CONCURRENCY: "1"' 'MAP_TILE_QUEUE_SIZE: "0"' \
  'MAP_TILE_CACHE_BYTES: "0"' 'MAP_TILE_CACHE_ENTRIES: "1"' \
  'MAP_TILE_CACHE_TTL: "5m"' 'MAP_TILE_QUEUE_TIMEOUT: "100ms"' 'MAP_TILE_REQUEST_TIMEOUT: "5s"'; do
  if ! grep -Fq "$setting" "$changed"; then
    printf '%s\n' "Private API tile override did not round-trip: $setting" >&2
    exit 1
  fi
done
checksum="$(awk '$1 == "checksum/config:" {print $2}' "$rendered")"
changed_checksum="$(awk '$1 == "checksum/config:" {print $2}' "$changed")"
if [ -z "$checksum" ] || [ "$checksum" = "$changed_checksum" ]; then
  printf '%s\n' 'Private tile settings do not alter the API rollout checksum' >&2
  exit 1
fi
for setting in concurrency=0 concurrency=17 queueSize=-1 queueSize=257 \
  cacheBytes=-1 cacheBytes=134217729 cacheEntries=0 cacheEntries=4097 \
  cacheTtl=999ms cacheTtl=301s cacheTtl=6m queueTimeout=99ms queueTimeout=3001ms \
  queueTimeout=4s requestTimeout=4s requestTimeout=16s unknown=1; do
  if helm template workouts-explorer helm --set "api.mapTiles.$setting" >/dev/null 2>&1; then
    printf '%s\n' "Private API tile schema accepted invalid setting $setting" >&2
    exit 1
  fi
done
if ! grep -q 'kind: NetworkPolicy' "$rendered" ||
   ! grep -q 'app.kubernetes.io/component: tiles' "$rendered" ||
   ! grep -q 'app.kubernetes.io/component: api' "$rendered"; then
  printf '%s\n' 'Martin is not restricted to API ingress' >&2
  exit 1
fi
if grep -Eq 'path: .*workouts-explorer-tiles|type: (NodePort|LoadBalancer)' "$rendered"; then
  printf '%s\n' 'Martin must never receive public ingress or an external Service type' >&2
  exit 1
fi
if grep -q 'prepare-smtp-password' "$rendered" ||
   ! grep -q 'mountPath: /var/run/secrets/workouts-smtp' "$rendered" ||
   ! grep -q 'defaultMode: 0440' "$rendered"; then
  printf '%s\n' 'SMTP password must be mounted directly with group-read-only permissions' >&2
  exit 1
fi
if ! grep -q 'name: workouts-explorer-ui-nginx' "$rendered" ||
   ! grep -Fq 'https://api.maptiler.com' "$rendered" ||
   ! grep -q 'mountPath: /etc/nginx/conf.d/default.conf' "$rendered"; then
  printf '%s\n' 'UI provider CSP configuration is not mounted from validated map origins' >&2
  exit 1
fi
if grep -q 'kind: ExternalName' "$rendered"; then
  printf '%s\n' 'Production rendering unexpectedly contains an ExternalName proxy' >&2
  exit 1
fi
if [ "$(grep -c 'mountPath: /var/run/secrets/workouts-source' "$rendered")" -ne 2 ] ||
   ! grep -q 'SOURCE_KEYRING_FILE:' "$rendered" ||
   ! grep -q 'LOCAL_SOURCE_ROOTS:' "$rendered"; then
  printf '%s\n' 'API and worker source encryption configuration is incomplete' >&2
  exit 1
fi
if ! grep -q 'name: SSL_CERT_FILE' "$rendered" ||
   ! grep -q 'mountPath: /var/run/secrets/workouts-trust' "$rendered" ||
   ! grep -q 'key: ca.crt' "$rendered"; then
  printf '%s\n' 'API private CA trust configuration is incomplete' >&2
  exit 1
fi
if ! grep -q 'name: WORKER_FILE_CONCURRENCY' "$rendered" ||
   ! grep -q 'name: ACCOUNT_FILE_CONCURRENCY' "$rendered" ||
   ! grep -q 'name: GLOBAL_FILE_CONCURRENCY' "$rendered" ||
   ! grep -q 'name: AUTO_SYNC_INTERVAL' "$rendered" ||
   ! grep -q 'name: AUTO_SYNC_POLL_INTERVAL' "$rendered" ||
   ! grep -q 'name: AUTO_SYNC_STALE_DAYS' "$rendered" ||
   ! grep -q 'name: SCHEDULER_LEASE_DURATION' "$rendered" ||
   ! grep -q 'name: WORKER_STAGING_ROOT' "$rendered" ||
   ! grep -q 'name: OSM_AUTO_ADD_REGIONS' "$rendered" ||
   ! grep -q 'name: OSM_MAX_AUTO_DOWNLOAD_BYTES' "$rendered" ||
   ! grep -q 'name: OSM_DATA_PROVIDERS_JSON' "$rendered" ||
   ! grep -q 'name: OSM_REGIONS_JSON' "$rendered" ||
   ! grep -q 'name: OSM_DATABASE_URL' "$rendered" ||
   ! grep -q 'key: osmDatabaseUrl' "$rendered" ||
   ! grep -q 'mountPath: /tls/prod-db' "$rendered" ||
   ! grep -q 'secretName: workouts-explorer-database-tls' "$rendered" ||
   ! grep -Fq '[\"geofabrik:norcal\"]' "$rendered" ||
   ! grep -q 'mountPath: /var/lib/workouts/staging' "$rendered" ||
   ! grep -q 'sizeLimit: 2Gi' "$rendered"; then
  printf '%s\n' 'Worker concurrency or bounded staging configuration is incomplete' >&2
  exit 1
fi
if ! grep -q 'COVERAGE_DEAD_END_ENDPOINT_ALLOWANCE_METERS:' "$rendered" ||
   ! grep -q 'value: "3"' "$rendered"; then
  printf '%s\n' 'Coverage dead-end endpoint allowance configuration is incomplete' >&2
  exit 1
fi
if ! grep -q 'COVERAGE_DIAGNOSTICS_TIMEOUT: "120s"' "$rendered"; then
  printf '%s\n' 'Coverage diagnostic timeout must allow large focused routes to finish' >&2
  exit 1
fi
if ! grep -q 'component: osm-migration' "$rendered" ||
   ! grep -q 'name: OSM_MIGRATION_DATABASE_URL' "$rendered" ||
   ! grep -q 'command: \[/app/osm-migrate\]' "$rendered"; then
  printf '%s\n' 'OSM migration job configuration is incomplete' >&2
  exit 1
fi
helm template workouts-explorer helm \
  --set osm.autoAddRegions=true \
  --set osm.maxAutoDownloadBytes=2000000000 \
  --set-string 'osm.dataProviders[0]=geofabrik' \
  --set-string 'osm.regions[0]=geofabrik:new-york' >"$changed"
if ! grep -q 'name: OSM_AUTO_ADD_REGIONS' "$changed" ||
   ! grep -q 'value: "true"' "$changed" ||
   ! grep -q 'value: "2000000000"' "$changed" ||
   ! grep -Fq '[\"geofabrik:new-york\"]' "$changed"; then
  printf '%s\n' 'Worker OSM configuration rendering is incomplete' >&2
  exit 1
fi
if helm template workouts-explorer helm --set osm.maxAutoDownloadBytes=0 >/dev/null 2>&1 ||
   helm template workouts-explorer helm --set-string 'osm.regions[0]=norcal' >/dev/null 2>&1 ||
   helm template workouts-explorer helm --set-string 'osm.dataProviders[0]=geofabrik:norcal' >/dev/null 2>&1; then
  printf '%s\n' 'Worker OSM schema accepted invalid settings' >&2
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
if helm template workouts-explorer helm --set-string 'api.publicConfig.baseMaps.styleFamilies[0].resourceOrigins[0]=https://maps.example.invalid;script-src *' >/dev/null 2>&1; then
  printf '%s\n' 'Base-map resource origin validation accepted CSP directive injection' >&2
  exit 1
fi
if helm template workouts-explorer helm --set-string worker.schedulerLeaseDuration=30s >/dev/null 2>&1; then
  printf '%s\n' 'Worker scheduler lease coherence check accepted a lease equal to the poll interval' >&2
  exit 1
fi
for duration in 901s 999s 3600s 16m 1h; do
  if helm template workouts-explorer helm --set-string worker.schedulerLeaseDuration="$duration" >/dev/null 2>&1; then
    printf '%s\n' "Worker scheduler lease bound accepted $duration over 15 minutes" >&2
    exit 1
  fi
done
for duration in 900s 15m; do
  if ! helm template workouts-explorer helm --set-string worker.schedulerLeaseDuration="$duration" >/dev/null 2>&1; then
    printf '%s\n' "Worker scheduler lease bound rejected valid duration $duration" >&2
    exit 1
  fi
done

helm template workouts-explorer helm \
  --set ingress.enabled=true \
  --set-string ingress.className=nginx \
  --set-string ingress.host=workouts.xdev.fourteeners.local \
  --set-string ingress.additionalHosts[0]=workouts.erhhungyuan.com \
  --set-string ingress.tlsSecretName=virtual-ingress-tls \
  --set-string ingress.certificateSecretName=translated-host-certificate >"$rendered"
if ! grep -q 'kind: Certificate' "$rendered" ||
   ! grep -q 'secretName: virtual-ingress-tls' "$rendered" ||
   ! grep -q 'secretName: translated-host-certificate' "$rendered" ||
   ! grep -q 'host: workouts.erhhungyuan.com' "$rendered"; then
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
helm template workouts-explorer helm --set api.bootstrap.enabled=true \
  --set api.bootstrap.rotateExistingPassword=true >"$rendered"
if ! grep -q -- '--rotate-existing-password' "$rendered"; then
  printf '%s\n' 'Explicit bootstrap password rotation is not rendered' >&2
  exit 1
fi

helm template workouts-explorer helm --set api.roleProvisioning.enabled=true >"$rendered"
if ! grep -q 'name: ROLE_PROVISIONING_DATABASE_URL' "$rendered" ||
   ! grep -q 'argocd.argoproj.io/sync-wave: "-3"' "$rendered" ||
   ! grep -q 'helm.sh/hook-weight: "-3"' "$rendered"; then
  printf '%s\n' 'Role provisioning Job is not independently credentialed before migration' >&2
  exit 1
fi
