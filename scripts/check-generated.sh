#!/bin/sh
set -eu

temporary="$(mktemp)"
trap 'rm -f "$temporary"' EXIT
cp api/generated/openapi.gen.go "$temporary"
go generate ./api
if ! cmp -s "$temporary" api/generated/openapi.gen.go; then
  cp "$temporary" api/generated/openapi.gen.go
  printf '%s\n' 'generated OpenAPI artifacts are stale; run make generate' >&2
  exit 1
fi
