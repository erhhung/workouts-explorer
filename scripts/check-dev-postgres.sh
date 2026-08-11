#!/bin/sh
set -eu

manifest=deploy/dev/postgres.yaml
harness=scripts/test-vcluster.sh

require_manifest() {
  if ! grep -Eq "$1" "$manifest"; then
    printf '%s\n' "$2" >&2
    exit 1
  fi
}

require_harness() {
  if ! grep -Eq "$1" "$harness"; then
    printf '%s\n' "$2" >&2
    exit 1
  fi
}

require_manifest '^kind: StatefulSet$' 'Development PostgreSQL must use a StatefulSet'
require_manifest '^  name: postgresql$' 'Development PostgreSQL resources must be named postgresql'
require_manifest '^  clusterIP: None$' 'Development PostgreSQL governing Service must be headless'
require_manifest '^  publishNotReadyAddresses: true$' 'Development PostgreSQL DNS must remain stable during pod restarts'
require_manifest '^  replicas: 1$' 'Development PostgreSQL StatefulSet must have one replica'
require_manifest '^  serviceName: postgresql$' 'Development PostgreSQL StatefulSet has the wrong governing Service'
require_manifest '^        app\.kubernetes\.io/name: postgresql$' 'Development PostgreSQL pods must use the postgresql label'
require_manifest '^          image: postgis/postgis:18-3.6-alpine$' 'Development PostgreSQL must use the pinned PostgreSQL 18/PostGIS 3.6 image'
require_manifest '^            - name: POSTGRES_USER$' 'Development PostgreSQL must declare POSTGRES_USER'
require_manifest '^              value: postgres$' 'Development PostgreSQL must initialize with the postgres superuser'
require_manifest '^              command: \[pg_isready, -U, postgres, -d, workouts\]$' 'Development PostgreSQL probes must use postgres'
require_manifest '^    CREATE ROLE workouts_migration LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;$' 'Migration role must be an explicit non-superuser'
require_manifest '^    CREATE EXTENSION IF NOT EXISTS postgis;$' 'Development PostgreSQL must install PostGIS before application migrations'
require_manifest '^    CREATE ROLE workouts_tiles LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;$' 'Tile role must be an explicit least-privilege login'
require_manifest '^    ALTER DATABASE workouts OWNER TO workouts_migration;$' 'Migration role must own the workouts database'
require_manifest '^    GRANT workouts_security_owner TO workouts_migration WITH ADMIN OPTION;$' 'Migration role needs admin-option security-owner membership'
require_manifest '^        name: data$' 'Development PostgreSQL volume claim template must be named data'
require_manifest '^          - ReadWriteOnce$' 'Development PostgreSQL PVC must use ReadWriteOnce'
require_manifest '^        storageClassName: longhorn$' 'Development PostgreSQL PVC must use Longhorn'
require_manifest '^            storage: 5Gi$' 'Development PostgreSQL PVC must request 5Gi'
require_manifest '^              mountPath: /var/lib/postgresql$' 'PostgreSQL 18 data must be mounted at /var/lib/postgresql'

if grep -Eq '^kind: Deployment$|emptyDir:' "$manifest"; then
  printf '%s\n' 'Development PostgreSQL must not use a Deployment or emptyDir' >&2
  exit 1
fi
if [ "$(grep -Ec '^              command: \[pg_isready, -U, postgres, -d, workouts\]$' "$manifest")" -ne 2 ]; then
  printf '%s\n' 'Both development PostgreSQL probes must use postgres' >&2
  exit 1
fi
if grep -Eq '^              value: workouts_migration$' "$manifest"; then
  printf '%s\n' 'workouts_migration must not be the PostgreSQL bootstrap superuser' >&2
  exit 1
fi

require_harness 'rollout status statefulset/postgresql' 'vCluster harness must wait for the PostgreSQL StatefulSet'
require_harness 'exec -i statefulset/postgresql' 'vCluster role provisioning must execute against the PostgreSQL StatefulSet'
require_harness 'psql -U postgres -d workouts' 'vCluster role provisioning must run as postgres'
require_harness 'ALTER ROLE workouts_migration LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS' 'vCluster role provisioning must harden the migration role'
require_harness 'GRANT workouts_security_owner TO workouts_migration WITH ADMIN OPTION' 'vCluster role provisioning must grant admin-option membership'
require_harness 'deployment/workouts-postgres service/postgres --ignore-not-found' 'vCluster harness must safely remove legacy resources'
require_harness '^database_host="postgresql\.\$\{database_namespace\}\.svc\.cluster\.local"$' 'vCluster harness uses the wrong PostgreSQL host'

delete_line=$(grep -n 'deployment/workouts-postgres service/postgres --ignore-not-found' "$harness" | cut -d: -f1)
pod_list_line=$(grep -n 'get pods$' "$harness" | cut -d: -f1)
for marker in 'helm "${helm_args\[@\]}"' 'rollout status deployment/workouts-explorer-worker' 'certificate/workouts-explorer-ingress' 'run workouts-smoke' '"https://\${host}/mailpit/"'; do
  marker_line=$(grep -n "$marker" "$harness" | cut -d: -f1)
  if [ "$delete_line" -le "$marker_line" ]; then
    printf '%s\n' 'Legacy PostgreSQL resources must only be removed after Helm, rollout, certificate, and smoke checks' >&2
    exit 1
  fi
done
if [ "$pod_list_line" -ne $((delete_line + 1)) ]; then
  printf '%s\n' 'Legacy PostgreSQL cleanup must immediately precede the final pod listing' >&2
  exit 1
fi

if grep -Eq 'persistentvolumeclaim|pvc' "$harness"; then
  printf '%s\n' 'vCluster harness must never delete PostgreSQL PVCs' >&2
  exit 1
fi
