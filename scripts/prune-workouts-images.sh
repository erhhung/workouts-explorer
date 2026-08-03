#!/usr/bin/env bash
set -euo pipefail

for component in api worker ui; do
  while IFS= read -r image; do
    case "$image" in
      workouts-${component}:*|*/workouts-${component}:*)
        buildah rmi "$image"
        ;;
    esac
  done < <(buildah images --format '{{.Name}}:{{.Tag}}')
done

buildah rmi --prune
