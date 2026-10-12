const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const childProcess = require("node:child_process");

const manifestPath = path.join(__dirname, "profiles.json");
const bytes = fs.readFileSync(manifestPath);
const manifest = JSON.parse(bytes);
const name = process.env.COVERAGE_ACCEPTANCE_PROFILE || "typical";

function validate(value) {
  if (
    value?.schema_version !== 1 ||
    !Number.isInteger(value.year) ||
    value.year < 1900 ||
    value.year > 2100 ||
    (Date.UTC(value.year + 1, 0, 1) - Date.UTC(value.year, 0, 1)) / 86400000 !== 365 ||
    !Number.isInteger(value.seed) ||
    value.seed < -2147483648 ||
    value.seed > 2147483647 ||
    !Number.isInteger(value.gps_sample_seconds) ||
    value.gps_sample_seconds < 1 ||
    value.gps_sample_seconds > 30 ||
    value.distance_meters_per_mile !== 1609.344 ||
    !Number.isInteger(value.other_duration_seconds) ||
    value.other_duration_seconds < 1 ||
    value.other_duration_seconds > 25200 ||
    Object.keys(value.profiles || {})
      .sort()
      .join(",") !== "stress,typical"
  )
    throw Error("invalid acceptance profile manifest");
  const mainKeys = ["running", "walking", "cycling", "other"];
  const keys = new Set(mainKeys);
  // Go trims Unicode White_Space, which differs from JavaScript's trim().
  const validLabel = (label) =>
    typeof label === "string" && /[^\p{White_Space}]/u.test(label) && !label.includes("\0");
  if (!Array.isArray(value.other_types) || !value.other_types.length)
    throw Error("missing other-type catalog");
  for (const a of value.other_types) {
    if (
      typeof a?.key !== "string" ||
      !/^[a-z][a-z0-9_-]{0,511}(?![\s\S])/.test(a.key) ||
      keys.has(a.key) ||
      !validLabel(a.label)
    )
      throw Error("invalid, duplicate, or reserved other type");
    keys.add(a.key);
  }
  for (const [name, perDay] of [
    ["typical", 1],
    ["stress", 2],
  ]) {
    const p = value.profiles[name];
    if (
      p?.workouts_per_day !== perDay ||
      p.total_workouts !== 365 * perDay ||
      !Array.isArray(p.activities) ||
      p.activities.length !== mainKeys.length
    )
      throw Error("invalid profile calendar or activities");
    for (const [i, a] of p.activities.entries()) {
      if (
        a?.key !== mainKeys[i] ||
        !validLabel(a.label) ||
        !Number.isInteger(a.count) ||
        a.count < 0 ||
        a.count > 365 ||
        a.routed !== i < 3 ||
        !Number.isFinite(a.percentage) ||
        a.percentage < 0 ||
        a.percentage > 100 ||
        Math.abs(a.count - (p.total_workouts * a.percentage) / 100) > 1 + 1e-9 ||
        !Number.isFinite(a.distance_miles) ||
        !Number.isFinite(a.distance_miles * value.distance_meters_per_mile) ||
        (a.routed && a.distance_miles <= 0) ||
        (!a.routed &&
          (a.distance_miles !== 0 || a.duration_seconds !== value.other_duration_seconds)) ||
        !Number.isInteger(a.duration_seconds) ||
        a.duration_seconds < 1 ||
        a.duration_seconds > 25200
      )
        throw Error("invalid profile activity fields");
    }
    const total = p.activities.reduce((n, a) => n + a.count, 0);
    if (
      total !== p.total_workouts ||
      Math.abs(p.activities.reduce((n, a) => n + a.percentage, 0) - 100) > 1e-9 ||
      !p.activities.some((a) => a.routed && a.count > 0)
    )
      throw Error("invalid profile counts");
  }

  return value;
}

validate(manifest);
if (!Object.hasOwn(manifest.profiles, name))
  throw Error("COVERAGE_ACCEPTANCE_PROFILE must be typical or stress");
const selected = manifest.profiles[name];
const total = selected.activities.reduce((n, a) => n + a.count, 0);
const routed = selected.activities.filter((a) => a.routed).reduce((n, a) => n + a.count, 0);
const binding = Object.freeze({
  profile: name,
  profile_version: manifest.schema_version,
  profile_manifest_sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
});

const artifact = (suffix) => `/tmp/opencode/coverage-${name}-${suffix}`;

const results = (kind) =>
  kind === "acceptance"
    ? path.join(__dirname, `results/${name}-results.json`)
    : path.resolve(__dirname, `../../docs/coverage-${kind}-${name}-results.json`);

function assertProfile(value) {
  if (Object.entries(binding).some(([key, expected]) => value?.[key] !== expected)) {
    throw Error(
      "profile name, version, or manifest hash mismatch; cleanup and reprovision for another profile",
    );
  }
  return value;
}

function assertNamespace(ns) {
  if (
    ns.metadata?.name !== "workouts-acceptance" ||
    ns.metadata?.labels?.["workouts-disposable"] !== "true"
  )
    throw Error("not the disposable acceptance namespace");
  let marker;
  try {
    marker = JSON.parse(ns.metadata.annotations?.["workouts-acceptance/profile"]);
  } catch {}
  assertProfile(marker);
  return ns;
}

function kubectl(...args) {
  return childProcess.execFileSync(
    "kubectl",
    ["--context", "xdev", "-n", "workouts-acceptance", ...args],
    { encoding: "utf8", timeout: 30000 },
  );
}

function requireProfile({ database = true } = {}) {
  if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true")
    throw Error("explicit disposable switch required");
  const ns = assertNamespace(
    JSON.parse(kubectl("get", "namespace", "workouts-acceptance", "-o", "json")),
  );
  const marker = JSON.parse(kubectl("get", "configmap", "acceptance-profile", "-o", "json")).data;
  assertProfile(JSON.parse(marker["binding.json"]));
  if (marker["manifest.json"] !== bytes.toString())
    throw Error("namespace profile manifest differs");
  if (database) {
    const dbMarker = JSON.parse(
      kubectl(
        "exec",
        "deployment/acceptance-postgres",
        "--",
        "psql",
        "-U",
        "postgres",
        "-d",
        "acceptance_benchmark",
        "-At",
        "-v",
        "ON_ERROR_STOP=1",
        "-c",
        "SELECT binding FROM public.acceptance_profile WHERE singleton",
      ),
    );
    assertProfile(dbMarker);
    if (dbMarker.namespace_uid !== ns.metadata.uid)
      throw Error("profile DB marker belongs to another namespace");
  }
  return ns;
}

module.exports = {
  manifestPath,
  manifest,
  name,
  selected,
  total,
  routed,
  binding,
  artifact,
  results,
  validate,
  assertProfile,
  assertNamespace,
  requireProfile,
};

if (require.main === module) {
  const action = process.argv[2];
  if (action === "name") console.log(name);
  else if (action === "binding") console.log(JSON.stringify(binding));
  else if (action === "configmap")
    console.log(
      JSON.stringify({
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { name: "acceptance-profile", namespace: "workouts-acceptance" },
        data: { "binding.json": JSON.stringify(binding), "manifest.json": bytes.toString() },
      }),
    );
  else if (action === "require")
    requireProfile({ database: process.argv[3] !== "--namespace-only" });
  else throw Error("unknown profile action");
}
