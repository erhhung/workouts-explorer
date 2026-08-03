#!/bin/sh
set -eu

for artifact in ui/tsconfig.app.tsbuildinfo ui/tsconfig.node.tsbuildinfo ui/vite.config.js ui/vite.config.d.ts; do
  if [ -e "$artifact" ]; then
    printf '%s\n' "TypeScript build emitted source artifact: $artifact" >&2
    exit 1
  fi
done
