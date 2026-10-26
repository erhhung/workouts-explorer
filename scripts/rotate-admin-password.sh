#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

usage() {
  printf '%s\n' 'Usage: rotate-admin-password.sh --context CONTEXT [--namespace NAMESPACE] [--release RELEASE] [--chart PATH] [--timeout SECONDSs] (--dry-run | --yes)' \
    'Defaults: namespace=workouts-explorer release=workouts-dev chart=<repo>/helm timeout=300s' \
    'Validate only: scripts/rotate-admin-password.sh --context xdev --dry-run' \
    'Rotate:        scripts/rotate-admin-password.sh --context xdev --yes' \
    'Never prints the password. No release changes or API restart. Minimum changes require a normal API rollout.'
}

die() {
  printf 'Error: %s\n' "$1" >&2
  exit 1
}

context=''
namespace=workouts-explorer
release=workouts-dev
timeout=300s
dry_run=0
yes=0
chart="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../helm" && pwd)"

while (($#)); do
  case "$1" in
    --context | --namespace | --release | --chart | --timeout)
      (($# >= 2)) && [[ -n $2 && $2 != --* ]] || die "Missing value for $1"
      case "$1" in
        --context)
          context=$2
          ;;
        --namespace)
          namespace=$2
          ;;
        --release)
          release=$2
          ;;
        --chart)
          chart=$2
          ;;
        --timeout)
          timeout=$2
          ;;
      esac
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    --yes)
      yes=1
      shift
      ;;
    --help | -h)
      usage
      exit 0
      ;;
    *)
      die 'Unknown option; use --help'
      ;;
  esac
done

[[ -n $context ]] || die '--context is required'
((dry_run || yes)) || die 'Actual rotation requires --yes; use --dry-run to validate only'
[[ $timeout =~ ^[1-9][0-9]{0,5}s$ ]] || die '--timeout must be positive whole seconds, e.g. 300s (at most 999999s)'

for dependency in helm kubectl jq openssl; do
  command -v "$dependency" > /dev/null || die "Missing dependency: $dependency"
done

work=$(mktemp -d "${TMPDIR:-/tmp}/rotate-admin.XXXXXXXXXX")
changed=0
confirmed=0
job=not-created

# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() {
  local status=$?
  trap - EXIT
  if ((changed && !confirmed)); then
    printf '%s\n' 'Secret updated; database rotation not confirmed. Inspect this job before retrying.' "Job: $job" >&2
  fi
  rm -rf -- "$work"
  exit "$status"
}

trap 'cleanup' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 0700 "$work"
k=(
  kubectl
  --context "$context"
  --namespace "$namespace"
  --request-timeout=10s
)
h=(
  helm
  --kube-context "$context"
  --namespace "$namespace"
)

"${h[@]}" status "$release" -o json > "$work/status" 2> "$work/error" ||
  die 'Cannot read installed release'

jq -e --arg release "$release" --arg ns "$namespace" \
  '.name == $release and .namespace == $ns and .info.status == "deployed"' \
  "$work/status" > /dev/null 2> "$work/error" ||
  die 'Release must be deployed in the requested namespace'

"${h[@]}" get values "$release" --all -o json > "$work/values.json" 2> "$work/error" ||
  die 'Cannot read installed values'

minimum=$(
  jq -er '
  def nonempty: type == "string" and length > 0;
  select(type == "object") | select(.api.passwordMinimumLength | type == "number" and floor == . and . >= 12 and . <= 64) |
  select([.api.image, .image.tag, .api.bootstrap.secretName, .api.bootstrap.usernameKey,
    .api.bootstrap.emailKey, .api.bootstrap.passwordKey, .api.bootstrap.databaseSecret.name,
    .api.bootstrap.databaseSecret.key] | all(nonempty)) |
  select(.image.registry | type == "string") |
  select(.api.bootstrap.passwordKey != .api.bootstrap.usernameKey and .api.bootstrap.passwordKey != .api.bootstrap.emailKey) |
  .api.passwordMinimumLength' "$work/values.json" 2> "$work/error"
) || die 'Invalid installed values or password minimum (must be an integer 12..64)'

image=$(
  jq -er '
    (.image.registry | if length > 0 then . + "/" else "" end) + .api.image + ":" + .image.tag +
    ((.image.digests[.api.image] // "") | if length > 0 then "@" + . else "" end)
  ' "$work/values.json" 2> "$work/error"
) || die 'Invalid installed image'

render() {
  "${h[@]}" template "$release" "$chart" --values "$work/values.json" \
    --show-only templates/bootstrap-job.yaml \
    --set api.bootstrap.enabled=true \
    --set api.bootstrap.rotateExistingPassword=true \
    --set "api.passwordMinimumLength=$minimum" \
    > "$work/render.yaml" 2> "$work/error" ||
    die 'Bootstrap hook render failed'

  "${k[@]}" create --dry-run=client --validate=false -f "$work/render.yaml" -o json \
    > "$work/render.json" 2> "$work/error" ||
    die 'Bootstrap hook JSON conversion failed'

  jq -es --arg release "$release" --arg ns "$namespace" --arg image "$image" --arg min "$minimum" \
    --slurpfile installed "$work/values.json" '
    def only($keys): (keys - $keys | length) == 0;
    $installed[0].api.bootstrap as $b |
    select(length == 1) | .[0] |
    select(.apiVersion == "batch/v1" and .kind == "Job") |
    select(only(["apiVersion","kind","metadata","spec"])) |
    select(.metadata.namespace == null or .metadata.namespace == $ns) |
    select(.metadata | only(["name","namespace","labels","annotations"])) |
    select(.metadata.labels["app.kubernetes.io/instance"] == $release) |
    select(.spec | only(["backoffLimit","template"])) |
    select(.spec.backoffLimit == 1) |
    select(.spec.template | only(["metadata","spec"])) |
    select(.spec.template.metadata | only(["labels","annotations"])) |
    select((.spec.template.metadata.annotations // {}) == {}) |
    select(.spec.template.metadata.labels["app.kubernetes.io/instance"] == $release and
      .spec.template.metadata.labels["app.kubernetes.io/component"] == "bootstrap-admin") |
    .spec.template.spec as $p |
    select($p | only(["restartPolicy","securityContext","imagePullSecrets","initContainers","containers","volumes"])) |
    select($p.restartPolicy == "Never" and ($p.containers | length) == 1 and ($p.initContainers | length) == 1) |
    $p.containers[0] as $c | $p.initContainers[0] as $i |
    select([$c,$i] | all(only(["name","image","command","args","securityContext","resources","volumeMounts","env"]))) |
    select($c.name == "bootstrap-admin" and $c.image == $image and $i.image == $image) |
    select($c.command == ["/app/bootstrap-admin"] and $c.args == ["--username=$(BOOTSTRAP_USERNAME)",
      "--email=$(BOOTSTRAP_EMAIL)","--password-file=/private/password","--password-minimum=" + $min,"--rotate-existing-password"]) |
    select($i.name == "prepare-password" and $i.command == ["/bin/sh","-ec",
      "cp /secret/password /private/password && chmod 0400 /private/password && chown 65532:65532 /private/password"] and
      ($i.args // []) == [] and ($i.env // []) == []) |
    select($c.env == [
      {name:"LOG_FORMAT",value:$installed[0].logging.format},
      {name:"BOOTSTRAP_DATABASE_URL",valueFrom:{secretKeyRef:$b.databaseSecret}},
      {name:"BOOTSTRAP_USERNAME",valueFrom:{secretKeyRef:{name:$b.secretName,key:$b.usernameKey}}},
      {name:"BOOTSTRAP_EMAIL",valueFrom:{secretKeyRef:{name:$b.secretName,key:$b.emailKey}}}]) |
    select($c.volumeMounts == [{name:"private",mountPath:"/private",readOnly:true}]) |
    select($i.volumeMounts == [{name:"source",mountPath:"/secret",readOnly:true},{name:"private",mountPath:"/private"}]) |
    select($p.volumes == [{name:"private",emptyDir:{medium:"Memory"}},
      {name:"source",secret:{secretName:$b.secretName,defaultMode:256,items:[{key:$b.passwordKey,path:"password"}]}}])
    ' "$work/render.json" > "$work/job.json" 2> "$work/error" ||
    die 'Rendered manifest must be exactly the native bootstrap Job with installed references'
}

render

fullname=$(
  jq -er '.metadata.name | capture("^(?<prefix>.+)-bootstrap-admin-[0-9]+$").prefix' \
    "$work/job.json" 2> "$work/error"
) || die 'Invalid bootstrap Job name'

"${k[@]}" get configmap "$fullname-api" -o json > "$work/config.json" 2> "$work/error" ||
  die 'Cannot read current API ConfigMap'

current_minimum=$(
  jq -er '
    .data.PASSWORD_MINIMUM_LENGTH | select(type == "string") |
    select(test("^[1-9][0-9]$")) | tonumber | select(. >= 12 and . <= 64)
  ' "$work/config.json" 2> "$work/error"
) || die 'Invalid or missing API ConfigMap password minimum (12..64)'

# The live ConfigMap may be ahead of Helm values; never weaken either minimum.
if ((current_minimum > minimum)); then
  minimum=$current_minimum
  render
fi

"${k[@]}" get deployment "$fullname-api" -o json > "$work/api.json" 2> "$work/error" ||
  die 'Cannot read installed API Deployment'

jq -e --arg image "$image" '[.spec.template.spec.containers[] | select(.name == "api") | .image] == [$image]' \
  "$work/api.json" > /dev/null 2> "$work/error" ||
  die 'Hook image differs from installed API image; reconcile the release first'

secret=$(
  jq -er '.spec.template.spec.volumes[] | select(.name == "source") | .secret.secretName' \
    "$work/job.json"
)
key=$(
  jq -er '.spec.template.spec.volumes[] | select(.name == "source") | .secret.items[] | select(.path == "password") | .key' \
    "$work/job.json"
)

"${k[@]}" get secret "$secret" -o json > "$work/secret.json" 2> "$work/error" ||
  die 'Cannot read bootstrap Secret'

jq -e --slurpfile v "$work/values.json" --arg secret "$secret" --arg key "$key" '
  $v[0].api.bootstrap as $b | .kind == "Secret" and .metadata.name == $secret and
  (.metadata.uid | type == "string" and length > 0) and
  (.metadata.resourceVersion | type == "string" and length > 0) and .immutable != true and
  ([.data[$key],.data[$b.usernameKey],.data[$b.emailKey]] | all(type == "string" and length > 0))' \
  "$work/secret.json" > /dev/null 2> "$work/error" ||
  die 'Bootstrap Secret is missing required data/UID/resourceVersion or is immutable'

db_secret=$(jq -er '.api.bootstrap.databaseSecret.name' "$work/values.json")
"${k[@]}" get secret "$db_secret" -o json > "$work/db.json" 2> "$work/error" ||
  die 'Cannot read bootstrap database Secret'

jq -e --slurpfile v "$work/values.json" '.data[$v[0].api.bootstrap.databaseSecret.key] | type == "string" and length > 0' \
  "$work/db.json" > /dev/null 2> "$work/error" ||
  die 'Bootstrap database Secret key is missing'

suffix=${work##*.}
job="${fullname:0:32}-rotate-admin-${suffix,,}"

jq --arg name "$job" --arg ns "$namespace" --argjson seconds "${timeout%s}" '
  .metadata.name = $name | .metadata.namespace = $ns |
  .metadata.annotations |= with_entries(select((.key | startswith("helm.sh/") or startswith("argocd.argoproj.io/")) | not)) |
  .spec.activeDeadlineSeconds = $seconds | .spec.ttlSecondsAfterFinished = 3600' \
  "$work/job.json" > "$work/run.json" 2> "$work/error" ||
  die 'Cannot prepare rotation Job'

if ((dry_run)); then
  printf 'Dry-run validated: Secret %s; native bootstrap Job; no changes.\n' "$secret"
  exit 0
fi

password=$(openssl rand -hex 32 2> "$work/error") || die 'Secure password generation failed'
[[ $password =~ ^[0-9a-f]{64}$ ]] || die 'Secure password generator returned invalid output'
printf '%s' "$password" > "$work/password"
unset password

openssl dgst -sha256 "$work/password" > "$work/checksum" 2> "$work/error" ||
  die 'Password checksum generation failed'

IFS= read -r checksum < "$work/checksum" || die 'Cannot read password checksum'
checksum=${checksum##* }
[[ $checksum =~ ^[0-9a-f]{64}$ ]] || die 'Invalid password checksum'
printf '%s' "$checksum" > "$work/expected-sha256"
unset checksum

# Bind the copied bytes to this rotation, even if the Secret volume is stale or overwritten.
jq --rawfile sha "$work/expected-sha256" '
  .spec.template.spec.initContainers[0].command = ["/bin/sh","-ec",
    "cp /secret/password /private/password && actual=$(sha256sum /private/password) && [ \"${actual%% *}\" = \"$EXPECTED_PASSWORD_SHA256\" ] && chmod 0400 /private/password && chown 65532:65532 /private/password"] |
  .spec.template.spec.initContainers[0].env = [{name:"EXPECTED_PASSWORD_SHA256",value:$sha}]' \
  "$work/run.json" > "$work/guarded.json" 2> "$work/error" ||
  die 'Cannot prepare password copy guard'

# Read password bytes from disk, not argv; resourceVersion protects other Secret keys.
jq --arg key "$key" --rawfile pw "$work/password" '
  [{op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
   {op:"replace",path:("/data/" + ($key | gsub("~";"~0") | gsub("/";"~1"))),value:($pw | @base64)}]' \
  "$work/secret.json" > "$work/patch.json" 2> "$work/error" ||
  die 'Cannot prepare Secret patch'

# A transport error can occur after the server commits the patch; never assume rollback is safe.
changed=1
"${k[@]}" patch secret "$secret" --type=json --patch-file "$work/patch.json" -o json \
  > "$work/patched.json" 2> "$work/error" ||
  die 'Secret patch not confirmed (possible resourceVersion conflict); inspect Secret before retrying'

jq -e --arg key "$key" --rawfile pw "$work/password" --slurpfile before "$work/secret.json" '
  .kind == "Secret" and .metadata.name == $before[0].metadata.name and
  .metadata.namespace == $before[0].metadata.namespace and .metadata.uid == $before[0].metadata.uid and
  (.metadata.resourceVersion | type == "string" and length > 0) and
  .metadata.resourceVersion != $before[0].metadata.resourceVersion and
  .data == ($before[0].data | .[$key] = ($pw | @base64))' \
  "$work/patched.json" > /dev/null 2> "$work/error" ||
  die 'Secret patch response is inconsistent; rotation not confirmed'

printf 'Secret updated: %s\nJob: %s\n' "$secret" "$job"
"${k[@]}" create -f "$work/guarded.json" > "$work/create-result" 2> "$work/error" ||
  die 'Rotation Job creation failed'

deadline=$((SECONDS + ${timeout%s}))
while ((SECONDS < deadline)); do
  "${k[@]}" get job "$job" -o json > "$work/progress.json" 2> "$work/error" ||
    die 'Cannot read rotation Job status'

  if jq -e 'any(.status.conditions[]?; (.type == "Failed" or .type == "FailureTarget") and .status == "True")' "$work/progress.json" > /dev/null 2> "$work/error"; then
    die 'Native bootstrap Job failed'
  fi

  if jq -e 'any(.status.conditions[]?; .type == "Complete" and .status == "True")' "$work/progress.json" > /dev/null 2> "$work/error"; then
    "${k[@]}" get secret "$secret" -o json > "$work/final-secret.json" 2> "$work/error" ||
      die 'Cannot confirm bootstrap Secret after Job completion'

    jq -e --slurpfile patched "$work/patched.json" '
      .kind == "Secret" and .metadata.name == $patched[0].metadata.name and
      .metadata.namespace == $patched[0].metadata.namespace and .metadata.uid == $patched[0].metadata.uid and
      .metadata.resourceVersion == $patched[0].metadata.resourceVersion and .data == $patched[0].data' \
      "$work/final-secret.json" > /dev/null 2> "$work/error" ||
      die 'Bootstrap Secret changed after patch; rotation not confirmed'

    confirmed=1
    printf 'Database rotation confirmed by successful native Job: %s\n' "$job"
    exit 0
  fi

  sleep 1
done

die 'Timed out waiting for native bootstrap Job completion'
