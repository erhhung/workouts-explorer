#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: osm-update-operator.sh

  Interactively inspect and operate one OSM
  region through short-lived Kubernetes job

Required environment:

  KUBE_CONTEXT            Explicit kubectl context
  OSM_UPDATE_IMAGE        Published workouts-osm image
  OSM_REGION_ID           Catalog region, e.g., geofabrik:norcal

Optional environment:

  OSM_NAMESPACE           Defaults to workouts-explorer
  OSM_IMAGE_PULL_SECRET   Defaults to workouts-harbor
  OSM_DATABASE_SECRET     Defaults to workouts-explorer-database
  OSM_DATABASE_KEY        Defaults to osmDatabaseUrl
  OSM_DATABASE_TLS_SECRET Defaults to workouts-explorer-database-tls
  OSM_MAX_DOWNLOAD_BYTES  Defaults to 1073741824
  OSM_OPERATOR            Defaults to local user name
  PROMETHEUS_KUBE_CONTEXT defaults to homelab
EOF
}

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  usage
  exit 0
fi
if [[ $# -ne 0 ]]; then
  usage >&2
  exit 2
fi

for command in kubectl envsubst jq curl; do
  command -v "$command" >/dev/null || { printf 'required command not found: %s\n' "$command" >&2; exit 1; }
done

: "${KUBE_CONTEXT:?KUBE_CONTEXT is required}"
: "${OSM_UPDATE_IMAGE:?OSM_UPDATE_IMAGE is required}"
: "${OSM_REGION_ID:?OSM_REGION_ID is required}"

OSM_NAMESPACE=${OSM_NAMESPACE:-workouts-explorer}
PROMETHEUS_KUBE_CONTEXT=${PROMETHEUS_KUBE_CONTEXT:-homelab}
OSM_IMAGE_PULL_SECRET=${OSM_IMAGE_PULL_SECRET:-workouts-harbor}
OSM_DATABASE_SECRET=${OSM_DATABASE_SECRET:-workouts-explorer-database}
OSM_DATABASE_KEY=${OSM_DATABASE_KEY:-osmDatabaseUrl}
OSM_DATABASE_TLS_SECRET=${OSM_DATABASE_TLS_SECRET:-workouts-explorer-database-tls}
OSM_MAX_DOWNLOAD_BYTES=${OSM_MAX_DOWNLOAD_BYTES:-1073741824}
OSM_OPERATOR=${OSM_OPERATOR:-${USER:-unknown}}

template=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/osm/manual-update-job.yaml
OSM_PROMETHEUS_SERVICE_IP=$(kubectl --context "$PROMETHEUS_KUBE_CONTEXT" -n monitoring \
  get service prometheus-stack-prometheus -o jsonpath='{.spec.clusterIP}')
if [[ -z "$OSM_PROMETHEUS_SERVICE_IP" || "$OSM_PROMETHEUS_SERVICE_IP" == "None" ]]; then
  printf 'Unable to discover the Prometheus Service ClusterIP from context %s.\n' "$PROMETHEUS_KUBE_CONTEXT" >&2
  exit 1
fi

PYGMENT_STYLE=${PYGMENT_STYLE:-gruvbox-dark}

# <lexer> [style]
colorize() {
  # lexer: yaml|json|bash...
  # style: see Pygments docs
  if command -v pygmentize &> /dev/null; then
    local lexer=$1 style=${2:-$PYGMENT_STYLE}
    pygmentize -f terminal256 -l "$lexer" -O style="$style"
  else
    cat
  fi
}

printf '\nOSM update operator context:\n'
{
printf '  KUBE_CONTEXT="%s"\n' "$KUBE_CONTEXT"
printf '  OSM_UPDATE_IMAGE="%s"\n' "$OSM_UPDATE_IMAGE"
printf '  OSM_REGION_ID="%s"\n' "$OSM_REGION_ID"
printf '  PROMETHEUS_SERVICE_IP="%s" ("%s" context)\n' "$OSM_PROMETHEUS_SERVICE_IP" "$PROMETHEUS_KUBE_CONTEXT"
} | colorize bash gruvbox-dark

export OSM_UPDATE_IMAGE OSM_REGION_ID OSM_IMAGE_PULL_SECRET OSM_DATABASE_SECRET
export OSM_DATABASE_KEY OSM_DATABASE_TLS_SECRET OSM_MAX_DOWNLOAD_BYTES OSM_OPERATOR
export OSM_PROMETHEUS_SERVICE_IP

job_name=""
job_persistent=false

cleanup_operator_job() {
  if [[ -n "$job_name" && "$job_persistent" != true ]]; then
    kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" delete job "$job_name" --wait=false >/dev/null 2>&1 || true
  fi
}

trap cleanup_operator_job EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

launch_job() {
  local action=$1 generation=${2:-} note=${3:-} reason=${4:-}
  local suffix
  case "$action" in
    start)
      OSM_RESOURCE_REQUEST_CPU=2
      OSM_RESOURCE_REQUEST_MEMORY=3Gi
      OSM_RESOURCE_REQUEST_EPHEMERAL_STORAGE=2Gi
      OSM_RESOURCE_LIMIT_CPU=4
      OSM_RESOURCE_LIMIT_MEMORY=8Gi
      OSM_RESOURCE_LIMIT_EPHEMERAL_STORAGE=8Gi
      ;;
    resume)
      OSM_RESOURCE_REQUEST_CPU=100m
      OSM_RESOURCE_REQUEST_MEMORY=256Mi
      OSM_RESOURCE_REQUEST_EPHEMERAL_STORAGE=64Mi
      OSM_RESOURCE_LIMIT_CPU=1
      OSM_RESOURCE_LIMIT_MEMORY=1Gi
      OSM_RESOURCE_LIMIT_EPHEMERAL_STORAGE=1Gi
      ;;
    status|unblock|abort)
      OSM_RESOURCE_REQUEST_CPU=10m
      OSM_RESOURCE_REQUEST_MEMORY=64Mi
      OSM_RESOURCE_REQUEST_EPHEMERAL_STORAGE=16Mi
      OSM_RESOURCE_LIMIT_CPU=250m
      OSM_RESOURCE_LIMIT_MEMORY=128Mi
      OSM_RESOURCE_LIMIT_EPHEMERAL_STORAGE=256Mi
      ;;
    *)
      printf 'Unsupported OSM update action: %s\n' "$action" >&2
      return 1
      ;;
  esac
  suffix="operator-${action}-$(date +%Y%m%d%H%M%S)"
  suffix=${suffix,,}

  export OSM_JOB_SUFFIX=$suffix OSM_UPDATE_ACTION=$action OSM_GENERATION_ID=$generation
  export OSM_RESUME_GENERATION_ID=$generation OSM_OPERATOR_NOTE=$note OSM_ABORT_REASON=$reason
  export OSM_RESOURCE_REQUEST_CPU OSM_RESOURCE_REQUEST_MEMORY OSM_RESOURCE_REQUEST_EPHEMERAL_STORAGE
  export OSM_RESOURCE_LIMIT_CPU OSM_RESOURCE_LIMIT_MEMORY OSM_RESOURCE_LIMIT_EPHEMERAL_STORAGE
  envsubst < "$template" | kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" apply -f - >/dev/null
  job_name="osm-update-${suffix}"
}

wait_for_job() {
  local name=$1 attempts=${2:-90} status
  for ((attempt=0; attempt<attempts; attempt++)); do
    status=$(kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" get job "$name" -o json)
    if [[ $(jq -r '.status.succeeded // 0' <<<"$status") -gt 0 ]]; then
      return 0
    fi
    if [[ $(jq -r '.status.failed // 0' <<<"$status") -gt 0 ]]; then
      return 1
    fi
    sleep 1
  done
  return 1
}

run_short_job() {
  local action=$1 generation=${2:-} note=${3:-} reason=${4:-} logs result=0
  launch_job "$action" "$generation" "$note" "$reason"
  if ! wait_for_job "$job_name"; then
    result=1
  fi
  logs=$(kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" logs "job/$job_name" 2>&1 || true)
  kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" delete job "$job_name" --wait=false >/dev/null 2>&1 || true
  job_name=""
  printf '%s' "$logs"
  return "$result"
}

cleanup_prior_update_jobs() {
  local jobs
  jobs=$(kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" get jobs \
    -l app.kubernetes.io/component=osm-update -o name)
  if [[ -z "$jobs" ]]; then
    return
  fi
  printf '\nRemoving prior OSM update Job before launching replacement:\n'
  printf '  %s\n' $jobs
  # Word splitting is intentional: kubectl emits one resource name per line.
  kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" delete $jobs --wait=true >/dev/null
}

probe_status() {
  local output
  if ! output=$(run_short_job status); then
    printf 'Unable to inspect OSM state:\n%s\n' "$output" >&2
    return 1
  fi
  if ! jq -e 'type == "object" and has("found")' >/dev/null <<<"$output"; then
    printf 'Status Job returned invalid output:\n%s\n' "$output" >&2
    return 1
  fi
  printf '%s' "$output"
}

prometheus_query() {
  local query=$1 output delay
  for delay in 0 5 10; do
    if ((delay > 0)); then
      sleep "$delay"
    fi
    if output=$(curl -GsS --fail --max-time 15 \
      --resolve "prometheus-stack-prometheus.monitoring.svc.cluster.local:9090:${OSM_PROMETHEUS_SERVICE_IP}" \
        'https://prometheus-stack-prometheus.monitoring.svc.cluster.local:9090/api/v1/query' \
      --data-urlencode "query=${query}" 2>/dev/null); then
      if jq -e '.status == "success" and .data.resultType == "vector"' >/dev/null <<<"$output"; then
        printf '%s' "$output"
        return 0
      fi
    fi
  done
  return 1
}

postgres_primary_storage() {
  local primary_result primary_count namespace pod node pvc available_result capacity_result
  local available capacity available_gib capacity_gib percent

  primary_result=$(prometheus_query 'pg_replication_is_replica') || return 1
  primary_count=$(jq '[.data.result[] | select((.value[1] | tonumber) == 0)] | length' <<< "$primary_result")
  [[ "$primary_count" == 1 ]] || return 1

  namespace=$(jq -r '.data.result[] | select((.value[1] | tonumber) == 0) | .metric.namespace' <<< "$primary_result")
  pod=$(jq -r '.data.result[] | select((.value[1] | tonumber) == 0) | .metric.pod' <<< "$primary_result")
  [[ -n "$namespace" && "$namespace" != null && -n "$pod" && "$pod" != null ]] || return 1

  node=$(kubectl --context "$PROMETHEUS_KUBE_CONTEXT" -n "$namespace" \
    get pod "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null) || return 1
  pvc="data-${pod}"
  available_result=$(prometheus_query "kubelet_volume_stats_available_bytes{namespace=\"${namespace}\",persistentvolumeclaim=\"${pvc}\"}") || return 1
  [[ $(jq '.data.result | length' <<< "$available_result") == 1 ]] || return 1
  capacity_result=$(prometheus_query "kubelet_volume_stats_capacity_bytes{namespace=\"${namespace}\",persistentvolumeclaim=\"${pvc}\"}") || return 1
  [[ $(jq '.data.result | length' <<< "$capacity_result") == 1 ]] || return 1

  available=$(jq -r '.data.result[0].value[1] | tonumber | floor' <<< "$available_result")
   capacity=$(jq -r '.data.result[0].value[1] | tonumber | floor' <<<  "$capacity_result")
  ((capacity > 0 && available >= 0 && available <= capacity)) || return 1

  available_gib=$(jq -nr --argjson bytes "$available" '$bytes / 1073741824 * 100 | round / 100')
   capacity_gib=$(jq -nr --argjson bytes  "$capacity" '$bytes / 1073741824 * 100 | round / 100')
  percent=$(jq -nr --argjson available "$available" --argjson capacity "$capacity" '$available * 100 / $capacity | floor')
  printf '\nPostgreSQL primary storage:\n'
  {
  printf '  Pod: %s (%s)\n' "$pod" "$node"
  printf '  PVC: %s\n' "$pvc"
  printf '  Capacity: %s GiB\n' "$capacity_gib"
  printf '  Free: %s GiB (%s%%)\n' "$available_gib" "$percent"
  } | colorize yaml github-dark
}

choose() {
  local prompt=$1 answer
  shift
  printf '\n%s\n' "$prompt" >&2
  {
  for option in "$@"; do
    printf '  %s\n' "$option"
  done
  } | colorize cry native >&2
  read -r -p '> ' answer </dev/tty
  printf '%s' "$answer"
}

while true; do
  status=$(probe_status) || exit 1
  if [[ $(jq -r '.found' <<<"$status") != true ]]; then
    printf '\nNo incomplete OSM generation exists for %s.\n' "$OSM_REGION_ID"
    printf 'The next generation will be %s for derivation version %s.\n' \
      "$(jq -r '.nextGenerationId' <<<"$status")" "$(jq -r '.derivationVersion' <<<"$status")"
    answer=$(choose 'Choose an action:' '1) Start a new generation' '2) Refresh status' '0) Exit')
    case "$answer" in
      1)
        cleanup_prior_update_jobs
        launch_job start
        job_persistent=true
        printf '\nStarted job "%s"\nin namespace "%s" under context "%s".\n' "$job_name" "$OSM_NAMESPACE" "$KUBE_CONTEXT"
        exit 0
        ;;
      2) continue ;;
      0) exit 0 ;;
      *) printf 'Invalid choice.\n' >&2 ;;
    esac
    continue
  fi

  generation=$(jq -r '.generationId' <<<"$status")
  state=$(jq -r '.state' <<<"$status")
  stage_state=$(jq -r '.currentStage.state // "none"' <<<"$status")
  active_job_names=$(kubectl --context "$KUBE_CONTEXT" -n "$OSM_NAMESPACE" get jobs \
    -l app.kubernetes.io/component=osm-update -o json |
    jq -r '.items[] | select((.status.active // 0) > 0) | .metadata.name')
  controller_running=false
  if [[ -n "$active_job_names" ]]; then
    controller_running=true
  fi
  printf '\nCurrent OSM generation status:\n'
  jq '{generationId, regionId, state, schemaName, failure, currentStage,
    block: (if .block != null and .block.clearedAt == null then .block else null end)} |
    with_entries(select(.value != null))' <<<"$status"
  if ! postgres_primary_storage; then
    printf '\nPostgreSQL primary storage: unavailable\n'
  fi
  blocked=$(jq -r '.block != null and .block.clearedAt == null' <<<"$status")
  block_reason=$(jq -r '.block.reasonCode // "unknown"' <<<"$status")

  if [[ "$state" == "evaluating" ]]; then
    answer=$(choose 'An identity evaluation cannot be resumed:' '1) Abort and remove its scratch schema' '2) Refresh status' '0) Exit')
  elif [[ "$blocked" == true ]]; then
    if [[ "$block_reason" == "insufficient_space" ]]; then
      unblock_note_prompt='Describe the completed storage remediation: '
      answer=$(choose 'The generation is storage-capacity-blocked:' '1) Unblock after storage remediation' '2) Abort the generation' '3) Refresh status' '0) Exit')
    else
      unblock_note_prompt='Describe how the preflight failure was resolved: '
      answer=$(choose "The generation is preflight-blocked (${block_reason}):" '1) Unblock after resolving the preflight failure' '2) Abort the generation' '3) Refresh status' '0) Exit')
    fi
  elif [[ "$state" == "building" && "$stage_state" == "running" && "$controller_running" == true ]]; then
    answer=$(choose "The generation is actively running as Job \"${active_job_names//$'\n'/, }\":" '1) Abort the generation' '2) Refresh status' '0) Exit')
  else
    answer=$(choose 'Choose a valid follow-up:' '1) Resume the generation' '2) Abort the generation' '3) Refresh status' '0) Exit')
  fi

  if [[ "$state" == "evaluating" ]]; then
    case "$answer" in
      1) action=abort ;;
      2) continue ;;
      0) exit 0 ;;
      *) printf 'Invalid choice.\n' >&2; continue ;;
    esac
  elif [[ "$blocked" == true ]]; then
    case "$answer" in
      1) action=unblock ;;
      2) action=abort ;;
      3) continue ;;
      0) exit 0 ;;
      *) printf 'Invalid choice.\n' >&2; continue ;;
    esac
  elif [[ "$state" == "building" && "$stage_state" == "running" && "$controller_running" == true ]]; then
    case "$answer" in
      1) action=abort ;;
      2) continue ;;
      0) exit 0 ;;
      *) printf 'Invalid choice.\n' >&2; continue ;;
    esac
  else
    case "$answer" in
      1) action=resume ;;
      2) action=abort ;;
      3) continue ;;
      0) exit 0 ;;
      *) printf 'Invalid choice.\n' >&2; continue ;;
    esac
  fi

  case "$action" in
    resume)
      cleanup_prior_update_jobs
      launch_job resume "$generation"
      job_persistent=true
      printf '\nStarted "%s". The controller\nwill rerun storage preflight and continue from its durable cursor.\n' "$job_name"
      exit 0
      ;;
    unblock)
      read -r -p $'\n'"$unblock_note_prompt" note
      if [[ -z ${note// } ]]; then
        printf 'A remediation note is required.\n' >&2
        continue
      fi
      output=$(run_short_job unblock "$generation" "$note") || { printf '%s\n' "$output" >&2; exit 1; }
      printf '%s\n' "$output"
      printf '\nThe generation is unblocked. Choose Resume on the next prompt to run a fresh preflight.\n'
      ;;
    abort)
      read -r -p $'\nReason for permanently aborting this generation: ' reason
      if [[ -z ${reason// } ]]; then
        printf 'An abort reason is required.\n' >&2
        continue
      fi
      read -r -p "Type generation number \"$generation\" to confirm: " confirmation
      if [[ "$confirmation" != "$generation" ]]; then
        printf 'Abort cancelled.\n'
        continue
      fi
      cleanup_prior_update_jobs
      output=$(run_short_job abort "$generation" "" "$reason") || { printf '%s\n' "$output" >&2; exit 1; }
      printf '%s\n' "$output"
      ;;
  esac
done
