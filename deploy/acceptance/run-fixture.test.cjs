const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

function run(action, extra = {}) {
  return spawnSync(
    process.execPath,
    [
      "--require",
      path.join(__dirname, "fixture-driver.test-preload.cjs"),
      path.join(__dirname, "run-fixture.cjs"),
      action,
    ],
    {
      env: {
        ...process.env,
        COVERAGE_ACCEPTANCE_DISPOSABLE: "true",
        COVERAGE_ACCEPTANCE_PROFILE: "typical",
        COVERAGE_ACCEPTANCE_PHASE: "warm",
        ...extra,
      },
      encoding: "utf8",
    },
  );
}

test("fixture driver supplies profile paths and preserves Go source/workload measurements for both profiles", () => {
  for (const name of ["typical", "stress"])
    for (const action of ["seed", "queue", "general-worker", "reads", "diagnostics"]) {
      const result = run(action, { COVERAGE_ACCEPTANCE_PROFILE: name });
      assert.equal(result.status, 0, result.stderr);
      const rows = result.stdout
        .split("\n")
        .filter((s) => s.startsWith("{"))
        .map((s) => JSON.parse(s));
      const producer = rows.find((r) => r.producer_env),
        written = rows.find((r) => r.written);
      assert.equal(producer.producer_env.COVERAGE_ACCEPTANCE_PROFILE, name);
      assert.equal(producer.producer_env.COVERAGE_ACCEPTANCE_PROFILE_VERSION, "1");
      assert.match(
        producer.producer_env.COVERAGE_ACCEPTANCE_RESULTS,
        new RegExp(`/coverage-${name}-`),
      );
      assert.ok(producer.producer_args.includes("-json"));
      assert.equal(written.value.schema_version, 3);
      assert.equal(written.value.profile, name);
      assert.equal(written.value.namespace_uid, "mock-fixture-uid");
      assert.equal(
        written.value.profile_manifest_sha256,
        producer.producer_env.COVERAGE_ACCEPTANCE_PROFILE_MANIFEST_SHA256,
      );
      if (action === "diagnostics") assert.deepEqual(written.value.statuses, { 200: 1, 429: 2 });
      else {
        assert.deepEqual(written.value.source_cases, ["from-Go"]);
        assert.equal(written.value.workload_parents, 7);
      }
      if (action === "seed") assert.equal(written.value.seed_seconds, 12.5);
    }
});

test("fixture driver cannot launder wrong bindings from Go into valid evidence", () => {
  for (const [field, value] of [
    ["profile", "stress"],
    ["profile_version", 2],
    ["profile_manifest_sha256", "bad"],
    ["schema_version", 2],
    ["namespace_uid", "old"],
  ]) {
    const result = run("reads", { MOCK_BAD_FIELD: field, MOCK_BAD_VALUE: JSON.stringify(value) });
    assert.notEqual(result.status, 0);
    assert.ok(!result.stdout.includes('"written"'));
  }
});

test("fixture driver rejects namespace replacement before recording output", () => {
  const result = run("seed", { MOCK_NAMESPACE_CHANGED: "true" });
  assert.notEqual(result.status, 0);
  assert.ok(!result.stdout.includes('"written"'));
  assert.match(result.stderr, /another namespace/);
});

test("matcher smoke preserves structured type results and status without hiding failures", () => {
  for (const [status, exit, expectedExit, expectedStatus] of [
    ["PASS", "0", 0, "PASS"],
    ["FAIL", "0", 1, "FAIL"],
    ["FAIL", "1", 1, "FAIL"],
    ["PASS", "1", 1, "FAIL"],
  ]) {
    const result = run("matcher-smoke", {
      COVERAGE_ACCEPTANCE_PROFILE: "stress",
      MOCK_ARTIFACT_STATUS: status,
      MOCK_PRODUCER_EXIT: exit,
    });
    assert.equal(result.status, expectedExit, result.stderr);
    const rows = result.stdout
      .split("\n")
      .filter((s) => s.startsWith("{"))
      .map((s) => JSON.parse(s));
    const producer = rows.find((r) => r.producer_env),
      raw = rows.find((r) => r.producer_artifact).producer_artifact,
      written = rows.find((r) => r.written);
    assert.ok(producer.producer_args.includes("^TestCoverageAcceptanceMatcherSmoke$"));
    assert.match(written.written, /coverage-stress-matcher-smoke\.json$/);
    assert.equal(written.value.status, expectedStatus);
    assert.deepEqual(written.value.results, raw.results);
    assert.deepEqual(
      written.value.results.map((r) => r.activity),
      ["Outdoor Run", "Outdoor Walk", "Outdoor Cycle"],
    );
    assert.deepEqual(
      written.value.results.map((r) => r.bucket),
      ["running", "walking", "cycling"],
    );
    assert.deepEqual(
      written.value.results.map((r) => r.type_key),
      [
        "outdoor-run-27e9c638ed73cbf21bee19a1d4db970f",
        "outdoor-walk-d382cfff87408643a86b7a842d85134a",
        "outdoor-cycle-b6c2eef4fa14bc8baf203afa98e2c72e",
      ],
    );
    assert.ok(written.value.results.every((r) => !Object.hasOwn(r, "type")));
    assert.ok(
      written.value.results.every(
        (r) =>
          r.point_count === 17 &&
          r.input_duration_seconds > 0 &&
          r.distance_meters > 0 &&
          r.outcome === "applied",
      ),
    );
    assert.equal(written.value.profile_manifest_sha256, raw.profile_manifest_sha256);
    assert.equal(written.value.namespace_uid, raw.namespace_uid);
    assert.equal(written.value.started_at, raw.started_at);
    assert.equal(written.value.recorded_at, raw.recorded_at);
  }
});

test("viewport driver selects the Go case and preserves structured status independently of budgets", () => {
  for (const name of ["typical", "stress"])
    for (const [status, exit, expectedExit, expectedStatus] of [
      ["PASS", "0", 0, "PASS"],
      ["FAIL", "0", 1, "FAIL"],
      ["FAIL", "1", 1, "FAIL"],
      ["PASS", "1", 1, "FAIL"],
    ]) {
      const result = run("viewport", {
        COVERAGE_ACCEPTANCE_PROFILE: name,
        MOCK_ARTIFACT_STATUS: status,
        MOCK_PRODUCER_EXIT: exit,
      });
      assert.equal(result.status, expectedExit, result.stderr);
      const rows = result.stdout
        .split("\n")
        .filter((s) => s.startsWith("{"))
        .map((s) => JSON.parse(s));
      const producer = rows.find((r) => r.producer_env),
        raw = rows.find((r) => r.producer_artifact).producer_artifact,
        written = rows.find((r) => r.written);
      assert.ok(producer.producer_args.includes("^TestCoverageAcceptanceViewport$"));
      assert.equal(
        producer.producer_env.COVERAGE_ACCEPTANCE_RESULTS,
        `/tmp/opencode/coverage-${name}-viewport.json`,
      );
      assert.equal(written.value.status, expectedStatus);
      assert.equal(written.value.producer_exit_code, Number(exit));
      assert.deepEqual(written.value.cases, raw.cases);
      assert.deepEqual(written.value.failure_totals, raw.failure_totals);
      assert.equal(written.value.assessment.target_failure_count, 0);
      assert.equal(written.value.assessment.initial_map_target, "NOT_MEASURED");
    }
});

test("viewport driver rejects incomplete success, missing artifacts and nested stale bindings", () => {
  for (const extra of [
    { MOCK_VIEWPORT_MISSING_CASES: "true" },
    { MOCK_MISSING_ARTIFACT: "true" },
    { MOCK_STALE_ARTIFACT: "true" },
  ]) {
    const result = run("viewport", extra);
    assert.equal(result.status, 1, result.stderr);
    const written = result.stdout
      .split("\n")
      .filter((s) => s.startsWith("{"))
      .map((s) => JSON.parse(s))
      .find((r) => r.written);
    assert.equal(written.value.status, "FAIL");
    assert.equal(written.value.assessment.status, "INCOMPLETE");
  }
  for (const [field, value] of [
    ["schema_version", 2],
    ["namespace_uid", "old"],
    ["profile_manifest_sha256", "bad"],
  ]) {
    const result = run("viewport", {
      MOCK_VIEWPORT_BAD_CASE_FIELD: field,
      MOCK_BAD_VALUE: JSON.stringify(value),
    });
    assert.equal(result.status, 1);
    assert.ok(!result.stdout.includes('"written"'));
  }
});

test("viewport driver preserves previous namespace bytes before capturing a new UID", () => {
  const result = run("viewport", { MOCK_PREVIOUS_NAMESPACE_UID: "old-namespace-uid" });
  assert.equal(result.status, 0, result.stderr);
  const rows = result.stdout
    .split("\n")
    .filter((s) => s.startsWith("{"))
    .map((s) => JSON.parse(s));
  const copies = rows.filter((r) => r.copied).map((r) => r.copied);
  assert.equal(copies.length, 2);
  assert.equal(copies[0].namespace_uid, "old-namespace-uid");
  assert.match(copies[0].to, /viewport-old-namespace-uid-\d+-previous\.json$/);
  assert.equal(copies[1].namespace_uid, "mock-fixture-uid");
  assert.match(copies[1].to, /viewport-mock-fixture-uid-\d+-captured\.json$/);
  assert.equal(rows.find((r) => r.written).value.namespace_uid, "mock-fixture-uid");
});
