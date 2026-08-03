#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?MIGRATION_DATABASE_URL is required}"
: "${API_DATABASE_URL:?API_DATABASE_URL is required}"
: "${WORKER_DATABASE_URL:?WORKER_DATABASE_URL is required}"

go run ./api/cmd/migrate
go run ./api/cmd/migrate
go test ./api/migrations -count=1
