require("./profile-fixture.cjs");
const fs = require("node:fs");
const childProcess = require("node:child_process");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");
const profile = require("./profile.cjs");
const e = require("./evidence.cjs");
const { bytes, typeKeys } = require("./profile-fixture.cjs");

const ns = {
  metadata: {
    name: e.NAMESPACE,
    uid: "mock-fixture-uid",
    creationTimestamp: "2026-10-04T00:00:00Z",
    labels: { "workouts-disposable": "true" },
    annotations: { "workouts-acceptance/profile": JSON.stringify(profile.binding) },
  },
};
let namespaceReads = 0;

childProcess.execFileSync = (command, args) => {
  if (command !== "kubectl") throw Error("unexpected external command");
  if (args.includes("namespace")) {
    namespaceReads++;
    return JSON.stringify(
      process.env.MOCK_NAMESPACE_CHANGED && namespaceReads > 1
        ? { metadata: { ...ns.metadata, uid: "replacement" } }
        : ns,
    );
  }
  if (args.includes("configmap"))
    return JSON.stringify({
      data: { "binding.json": JSON.stringify(profile.binding), "manifest.json": bytes.toString() },
    });
  return JSON.stringify({ ...profile.binding, namespace_uid: ns.metadata.uid });
};

const files = new Map();
if (process.env.MOCK_PREVIOUS_NAMESPACE_UID)
  files.set(
    profile.artifact("viewport.json"),
    JSON.stringify(
      require("./viewport.test.cjs").viewportFixture({
        metadata: { ...ns.metadata, uid: process.env.MOCK_PREVIOUS_NAMESPACE_UID },
      }),
    ),
  );
const read = fs.readFileSync;
fs.readFileSync = (file, ...args) => (files.has(file) ? files.get(file) : read(file, ...args));
fs.existsSync = (file) => files.has(file);
fs.statSync = () => ({ mtimeMs: process.env.MOCK_STALE_ARTIFACT ? 0 : Date.now() });
fs.writeFileSync = (file, data) => {
  files.set(file, data);
  process.stdout.write(JSON.stringify({ written: file, value: JSON.parse(data) }) + "\n");
};
fs.copyFileSync = (from, to) => {
  if (!files.has(from) || files.has(to)) throw Error("invalid mock artifact copy");
  files.set(to, files.get(from));
  process.stdout.write(
    JSON.stringify({
      copied: { from, to, namespace_uid: JSON.parse(files.get(from)).namespace_uid },
    }) + "\n",
  );
};

childProcess.spawn = (command, args, options) => {
  if (command !== "go") throw Error("unexpected fixture producer");
  const env = options.env;
  process.stdout.write(JSON.stringify({ producer_env: env, producer_args: args }) + "\n");
  const child = new EventEmitter();
  child.pid = 999999;
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  process.nextTick(() => {
    let artifact = {
      ...e.fresh(ns).evidence,
      source_cases: ["from-Go"],
      workload_parents: 7,
      duration_seconds: 12.5,
      started_at: new Date().toISOString(),
      recorded_at: new Date().toISOString(),
      created_start: new Date().toISOString(),
      created_end: new Date().toISOString(),
    };
    if (env.COVERAGE_ACCEPTANCE_ACTION === "viewport") {
      artifact = require("./viewport.test.cjs").viewportFixture(ns);
      artifact.status = process.env.MOCK_ARTIFACT_STATUS || "PASS";
      if (process.env.MOCK_VIEWPORT_MISSING_CASES) artifact.cases = [];
      if (process.env.MOCK_VIEWPORT_BAD_CASE_FIELD)
        artifact.cases[0][process.env.MOCK_VIEWPORT_BAD_CASE_FIELD] = JSON.parse(
          process.env.MOCK_BAD_VALUE,
        );
    }
    if (env.COVERAGE_ACCEPTANCE_ACTION === "matcher-smoke") {
      artifact.status = process.env.MOCK_ARTIFACT_STATUS || "PASS";
      artifact.results = profile.selected.activities.slice(0, 3).map((a) => ({
        activity: a.label,
        type_key: typeKeys[a.label],
        bucket: a.key,
        workout_id: `mock-${a.key}`,
        job_id: `mock-job-${a.key}`,
        point_count: 17,
        input_duration_seconds: a.duration_seconds,
        distance_meters: a.distance_miles * profile.manifest.distance_meters_per_mile,
        status: "succeeded",
        outcome: "applied",
        matcher_ms: 12.5,
      }));
    }
    if (process.env.MOCK_BAD_FIELD)
      artifact[process.env.MOCK_BAD_FIELD] = JSON.parse(process.env.MOCK_BAD_VALUE);
    if (!process.env.MOCK_MISSING_ARTIFACT)
      files.set(env.COVERAGE_ACCEPTANCE_RESULTS, JSON.stringify(artifact));
    process.stdout.write(JSON.stringify({ producer_artifact: artifact }) + "\n");
    const test = args[3].replace(/^\^|\$$/g, "");
    for (const event of [
      { Test: test, Action: "run", Time: new Date().toISOString() },
      { Output: "competing diagnostic outcomes=map[200:1 429:2]\n" },
      { Test: test, Action: "pass", Time: new Date().toISOString() },
    ])
      child.stdout.write(JSON.stringify(event) + "\n");
    child.stdout.end();
    child.emit("close", Number(process.env.MOCK_PRODUCER_EXIT || 0), null);
  });
  return child;
};
