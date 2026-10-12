require("./profile-fixture.cjs");
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const e = require("./evidence.cjs");
const profile = require("./profile.cjs");

const ns = { metadata: { uid: "current-namespace", creationTimestamp: "2026-10-04T19:11:56Z" } };

test("fresh archive never initializes from committed compact JSON", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-evidence-"));
  try {
    fs.writeFileSync(
      path.join(dir, "compact.json"),
      JSON.stringify({ phases: { fake: {} }, seed: { seed_seconds: 12.5 } }),
    );
    const s = e.loadArchive(path.join(dir, "absent-archive.json"), ns);
    assert.deepEqual(s.snapshots, {});
    assert.deepEqual(s.evidence.phases, {});
    assert.equal(s.evidence.seed, null);
    assert.equal(s.evidence.diagnostics_429, null);
    assert.equal(s.evidence.viewport, null);
    assert.deepEqual(s.evidence.generation_parameters, profile.manifest);
    assert.equal(e.assess(s.evidence.phases).status, "NOT_MEASURED");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("archive and temp measurements reject old UID, missing UID and wrong schema", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-evidence-"));
  const file = path.join(dir, "artifact.json");
  try {
    for (const value of [
      { ...e.fresh(ns), namespace_uid: "old" },
      { ...e.fresh(ns), schema_version: 2 },
      {},
    ]) {
      fs.writeFileSync(file, JSON.stringify(value));
      assert.throws(() => e.loadArchive(file, ns));
      assert.throws(() => e.loadMeasurement(file, ns));
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("matching UID cannot admit a measurement older than namespace creation", () => {
  assert.throws(
    () => e.assertBinding({ ...e.fresh(ns), started_at: "2026-10-03T00:00:00Z" }, ns),
    /predates/,
  );
});

test("outer archive UID cannot conceal another namespace evidence or snapshot", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-nested-"));
  const file = path.join(dir, "archive");
  try {
    const s = e.fresh(ns);
    s.evidence.namespace_uid = "old";
    fs.writeFileSync(file, JSON.stringify(s));
    assert.throws(() => e.loadArchive(file, ns));
    s.evidence.namespace_uid = ns.metadata.uid;
    s.snapshots.fake = { ...e.fresh(ns), namespace_uid: "old" };
    fs.writeFileSync(file, JSON.stringify(s));
    assert.throws(() => e.loadArchive(file, ns));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("skipped endpoint, empty tile and missing cardinality evidence cannot PASS", () => {
  const phase = { timings: {}, content_checks: { status: "PASS", tile_min_bytes: {} } };
  for (const name of e.endpoints) {
    phase.timings[name] = { samples: 30, p95_ms: 1, max_ms: 2, statuses: { 200: 30 } };
    if (name.endsWith("_tile")) phase.content_checks.tile_min_bytes[name] = 1;
  }
  assert.equal(e.assess({ warm: phase }).status, "PASS");
  delete phase.timings.year_path_detail;
  assert.equal(e.assess({ warm: phase }).status, "INCOMPLETE");
  phase.timings.year_path_detail = { samples: 30, p95_ms: 1, max_ms: 2, statuses: { 200: 30 } };
  phase.content_checks.tile_min_bytes.year_route_tile = 0;
  assert.equal(e.assess({ warm: phase }).status, "INCOMPLETE");
  phase.content_checks.tile_min_bytes.year_route_tile = 1;
  phase.content_checks.status = "FAIL";
  assert.equal(e.assess({ warm: phase }).status, "INCOMPLETE");
});

test("target failures survive serialization", () => {
  const phase = {
    content_checks: {
      status: "PASS",
      tile_min_bytes: Object.fromEntries(
        e.endpoints.filter((n) => n.endsWith("_tile")).map((n) => [n, 1]),
      ),
    },
    timings: Object.fromEntries(
      e.endpoints.map((name) => [
        name,
        {
          samples: 30,
          p95_ms: name === "year_selection" ? 704 : 1,
          max_ms: 705,
          statuses: { 200: 30 },
        },
      ]),
    ),
  };
  assert.equal(e.assess({ backlog96: phase }).status, "FAIL");
  assert.equal(phase.timings.year_selection.target, "FAIL");
  assert.equal(
    JSON.parse(e.render({ phases: { backlog96: phase } })).phases.backlog96.timings.year_selection
      .p95_ms,
    704,
  );
});

test("profile name, version and manifest hash bind all nested evidence", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-profiles-"));
  const file = path.join(dir, "archive");
  try {
    for (const [field, value] of [
      ["profile", profile.name === "typical" ? "stress" : "typical"],
      ["profile_version", 2],
      ["profile_manifest_sha256", "0".repeat(64)],
    ]) {
      assert.throws(() => e.assertBinding({ ...e.fresh(ns), [field]: value }, ns), /profile/);
      for (const place of ["evidence", "phase", "snapshot", "queue", "seed"]) {
        const s = e.fresh(ns),
          bad = { ...structuredClone(s.evidence), [field]: value };
        if (place === "evidence") s.evidence = bad;
        if (place === "phase") s.evidence.phases.warm = bad;
        if (place === "snapshot") s.snapshots.fake = bad;
        if (place === "queue") s.measurements.queue_windows = { backlog48: bad };
        if (place === "seed") s.evidence.seed = bad;
        fs.writeFileSync(file, JSON.stringify(s));
        assert.throws(() => e.loadArchive(file, ns), /profile/);
      }
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("complete timings cannot bypass missing content checks", () => {
  const phase = {
    timings: Object.fromEntries(
      e.endpoints.map((name) => [
        name,
        { samples: 30, p95_ms: 1, max_ms: 2, statuses: { 200: 30 } },
      ]),
    ),
  };
  assert.equal(e.assess({ warm: phase }).status, "INCOMPLETE");
});

test("nested measurement bindings cannot be laundered by a valid outer record", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-viewport-binding-"));
  const file = path.join(dir, "measurement.json");
  try {
    for (const [field, value] of [
      ["profile", "other"],
      ["profile_version", 2],
      ["profile_manifest_sha256", "bad"],
      ["schema_version", 2],
      ["namespace_uid", "old"],
    ]) {
      const outer = e.fresh(ns).evidence;
      outer.cases = [{ ...e.fresh(ns).evidence, [field]: value }];
      fs.writeFileSync(file, JSON.stringify(outer));
      assert.throws(() => e.loadMeasurement(file, ns));
      const archive = e.fresh(ns);
      archive.evidence.viewport = outer;
      fs.writeFileSync(file, JSON.stringify(archive));
      assert.throws(() => e.loadArchive(file, ns));
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
