require("./profile-fixture.cjs");
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const childProcess = require("node:child_process");
const profile = require("./profile.cjs");
const e = require("./evidence.cjs");

test("collector defaults to the selected acceptance result without reading the compact report", (t) => {
  const env = { ...process.env };
  const argv = [...process.argv];
  t.after(() => {
    process.env = env;
    process.argv = argv;
  });
  process.env.COVERAGE_ACCEPTANCE_DISPOSABLE = "true";
  delete process.env.COVERAGE_ACCEPTANCE_COLLECT_OUTPUT;
  delete process.env.COVERAGE_ACCEPTANCE_ARCHIVE;
  process.argv[2] = "snapshot";
  const ns = { metadata: { uid: "collector-test-uid", creationTimestamp: "2026-10-04T00:00:00Z" } };
  const archive = profile.artifact(`acceptance-${ns.metadata.uid}-raw.json`);
  const writes = new Map();
  t.mock.method(profile, "requireProfile", () => ns);
  t.mock.method(e, "loadArchive", (file) => {
    assert.equal(file, archive);
    return e.fresh(ns);
  });
  t.mock.method(e, "loadMeasurement", () => null);
  t.mock.method(childProcess, "execFileSync", (command, args) => {
    assert.equal(command, "kubectl");
    return JSON.stringify(args.includes("pods") ? { items: [] } : {});
  });
  t.mock.method(fs, "writeFileSync", (file, data) => writes.set(file, JSON.parse(data)));
  const read = fs.readFileSync;
  t.mock.method(fs, "readFileSync", (file, ...args) => {
    assert.notEqual(path.resolve(file), profile.results("acceptance"));
    return read(file, ...args);
  });
  t.mock.method(console, "log", () => {});
  require("./collect.cjs");
  const target = path.join(__dirname, "results", `${profile.name}-results.json`);
  assert.deepEqual([...writes.keys()], [archive, target]);
  assert.equal(writes.get(target).profile, profile.name);
  assert.equal(writes.get(target).namespace_uid, ns.metadata.uid);
  assert.equal(writes.get(target).status, "NOT_MEASURED");
  assert.deepEqual(writes.get(target).phases, {});
});
