#!/usr/bin/env bash
set -euo pipefail

registry="${CI_REGISTRY:?CI_REGISTRY is required}"
registry_path="${CI_REGISTRY_PATH:?CI_REGISTRY_PATH is required}"
registry_user="${CI_REGISTRY_USER:?CI_REGISTRY_USER is required}"
registry_password="${CI_REGISTRY_PASSWORD:?CI_REGISTRY_PASSWORD is required}"
tag="${WORKOUTS_TEST_IMAGE_TAG:-dev-$(date +%Y%m%d)}"

registry_path="${registry_path%/}"
project="${registry_path#*/}"
if [[ "$project" == "$registry_path" || "$project" == */* ]]; then
  printf 'CI_REGISTRY_PATH must contain one Harbor project: %s\n' "$registry_path" >&2
  exit 1
fi

cleanup_stale_dev_tags() {
  local component repository page payload artifact_count stale_tag
  local -a page_tags=() stale_tags=()

  for component in api worker ui; do
    repository="workouts-${component}"
    stale_tags=()
    page=1
    while true; do
      payload="$(curl --fail --silent --show-error \
        --user "${registry_user}:${registry_password}" \
        "${registry%/}/api/v2.0/projects/${project}/repositories/${repository}/artifacts?with_tag=true&page=${page}&page_size=100")"
      artifact_count="$(jq 'length' <<<"$payload")"
      mapfile -t page_tags < <(jq --arg keep "$tag" -r \
        '.[] | .tags[]? | .name | select(startswith("dev-") and . != $keep)' <<<"$payload")
      stale_tags+=("${page_tags[@]}")
      if (( artifact_count < 100 )); then
        break
      fi
      ((page += 1))
    done

    for stale_tag in "${stale_tags[@]}"; do
      printf 'Deleting stale Harbor tag %s/%s:%s\n' "$project" "$repository" "$stale_tag"
      curl --fail --silent --show-error --request DELETE \
        --user "${registry_user}:${registry_password}" \
        "${registry%/}/api/v2.0/projects/${project}/repositories/${repository}/artifacts/${stale_tag}/tags/${stale_tag}"
    done
  done
}

./scripts/prune-workouts-images.sh

printf '%s' "$registry_password" | buildah login "$registry" \
  --username "$registry_user" --password-stdin

cleanup_stale_dev_tags

for component in api worker ui; do
  image="${registry_path}/workouts-${component}:${tag}"
  buildah build --file "${component}/Dockerfile" --tag "$image" .
  buildah push "$image"
done

printf 'Published workouts API, worker, and UI images with tag %s\n' "$tag"
