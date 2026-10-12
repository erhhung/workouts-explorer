#!/usr/bin/env bash
set -euo pipefail

[[ "${COVERAGE_ACCEPTANCE_DISPOSABLE:-}" == true ]] || {
  printf '%s\n' 'COVERAGE_ACCEPTANCE_DISPOSABLE=true is required.' >&2
  exit 1
}
for name in MIGRATION_DATABASE_URL API_DATABASE_URL WORKER_DATABASE_URL COVERAGE_WORKER_DATABASE_URL OSM_MIGRATION_DATABASE_URL COVERAGE_WORKER_OSM_DATABASE_URL; do
  value="${!name:-}"
  database='(coverage_test|acceptance_test)'
  [[ "$name" != *OSM* ]] || database='(coverage_test_osm|acceptance_test_osm)'
  if [[ ! "$value" =~ ^postgresql://[^@/?#[:space:]]+@(127\.0\.0\.1:(5432|15432)|localhost:(5432|15432)|acceptance-postgres\.workouts-acceptance\.svc\.cluster\.local:5432)/${database}(\?sslmode=(disable|allow|prefer|require|verify-ca|verify-full))?$ ]]; then
    printf 'Refusing non-disposable %s\n' "$name" >&2
    exit 1
  fi
done

# Check the entire set before either migration can open a connection.
app_database="${MIGRATION_DATABASE_URL%%\?*}"
app_database="${app_database##*/}"
endpoint="${MIGRATION_DATABASE_URL#*@}"
endpoint="${endpoint%%/*}"
for name in MIGRATION_DATABASE_URL API_DATABASE_URL WORKER_DATABASE_URL COVERAGE_WORKER_DATABASE_URL OSM_MIGRATION_DATABASE_URL COVERAGE_WORKER_OSM_DATABASE_URL; do
  value="${!name}"
  actual_database="${value%%\?*}"
  actual_database="${actual_database##*/}"
  actual_endpoint="${value#*@}"
  actual_endpoint="${actual_endpoint%%/*}"
  expected_database="$app_database"
  [[ "$name" != *OSM* ]] || expected_database="${app_database}_osm"
  if [[ "$actual_database" != "$expected_database" || "$actual_endpoint" != "$endpoint" ]]; then
    printf 'Refusing mismatched disposable %s\n' "$name" >&2
    exit 1
  fi
done

go run ./api/cmd/migrate
go run ./worker/cmd/osm-migrate
psql "$OSM_MIGRATION_DATABASE_URL" -v ON_ERROR_STOP=1 -c '
  GRANT USAGE ON SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;
  GRANT SELECT ON ALL TABLES IN SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;'
go test -race ./worker -run '^TestCoverageRunnerRunOnce.*Integration$' -count=3 -timeout=15m
