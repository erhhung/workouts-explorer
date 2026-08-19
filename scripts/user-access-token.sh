#!/usr/bin/env bash
set -euo pipefail

vcluster_name="${1:-dev}"

HOMELAB_DOMAIN=fourteeners.local
WORKOUTS_API_URL="https://workouts.x${vcluster_name}.${HOMELAB_DOMAIN}/api"

[ -t 0 ] || {
  echo >&2 'Interactive TTY required for login.'
  exit 1
}

read -r -p $'\nUsername: ' username
read -r -s -p 'Password: ' password
printf '\n' >&2

[ "$username" ] && \
[ "$password" ] || {
  echo >&2 'Username and password are required.'
  exit 1
}

[ -t 1 ] && echo >&2
jo username="$username" \
   password="$password" | \
  curl --fail --silent \
      -H 'Content-Type: application/json' \
      --data-binary @- \
      "$WORKOUTS_API_URL/session-tokens" | \
  jq -r '.accessToken' || {
    echo >&2 'Invalid username or password!'
    exit 1
  }
