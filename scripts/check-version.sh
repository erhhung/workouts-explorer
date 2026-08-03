#!/bin/sh
set -eu

version="$(tr -d '\r\n' < VERSION)"
chart_version="$(helm show chart helm | awk '$1 == "version:" {print $2}')"
app_version="$(helm show chart helm | awk '$1 == "appVersion:" {gsub(/"/, "", $2); print $2}')"

if [ "$version" != "$chart_version" ] || [ "$version" != "$app_version" ]; then
  printf '%s\n' "VERSION ($version), chart version ($chart_version), and appVersion ($app_version) must match" >&2
  exit 1
fi
