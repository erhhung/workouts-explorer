const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { EventEmitter } = require("node:events");
const { lock, start } = require("./forward-supervisor.cjs");

test("INT and TERM wait for all child close events before releasing supervisor references", async () => {
  for (const signal of ["SIGINT", "SIGTERM"]) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-signals-"));
    const pid = path.join(dir, "pid"),
      mutex = path.join(dir, "lock"),
      signals = new EventEmitter(),
      children = [];
    try {
      start({
        pidFile: pid,
        lockFile: mutex,
        signals,
        namespace: () => "mock-disposable-uid",
        spawn: () => {
          const child = new EventEmitter();
          child.kill = () => {
            child.stopped = true;
          };
          children.push(child);
          return child;
        },
      });
      signals.emit(signal);
      assert.equal(children.length, 5);
      assert.ok(children.every((c) => c.stopped));
      assert.ok(fs.existsSync(pid));
      for (const child of children.slice(0, 4)) child.emit("close");
      assert.ok(fs.existsSync(pid));
      children[4].emit("close");
      assert.equal(fs.existsSync(pid), false);
      assert.equal(fs.existsSync(mutex), false);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("supervisor refuses a second owner without overwriting PID references", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-lock-"));
  const pid = path.join(dir, "pid"),
    mutex = path.join(dir, "lock");
  try {
    const release = lock(pid, mutex),
      original = fs.readFileSync(pid, "utf8");
    assert.throws(() => lock(pid, mutex));
    assert.equal(fs.readFileSync(pid, "utf8"), original);
    release();
    fs.writeFileSync(pid, "existing-owner\n");
    assert.throws(() => lock(pid, mutex));
    assert.equal(fs.readFileSync(pid, "utf8"), "existing-owner\n");
    assert.equal(fs.existsSync(mutex), false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("queue waits for both worker pod sets and restores replicas after producer failure", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-lifecycle-"));
  const log = path.join(dir, "events");
  try {
    fs.writeFileSync(
      path.join(dir, "kubectl"),
      `#!/bin/sh
printf '%s\\n' "$*" >> "$EVENT_LOG"
case "$*" in
 *metadata.labels.workouts-disposable*) printf true ;;
 *spec.replicas*) printf 1 ;;
 *"get pods"*) case "$*" in
   *"component=worker "*) marker="$TEST_ROOT/general" ;;
   *) marker="$TEST_ROOT/coverage" ;;
 esac
 if [ ! -f "$marker" ]; then touch "$marker"; printf 'pod/still-terminating'; fi ;;
esac
`,
      { mode: 0o755 },
    );
    fs.writeFileSync(path.join(dir, "sleep"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    fs.writeFileSync(
      path.join(dir, "node"),
      '#!/bin/sh\ncase "$1" in *profile.cjs) case "$2" in name) printf typical;; require) printf "profile-guard\\n" >> "$EVENT_LOG"; exit "${PROFILE_GUARD_EXIT:-0}";; esac;; *) printf "producer\\n" >> "$EVENT_LOG"; exit 17;; esac\n',
      { mode: 0o755 },
    );
    const env = {
      ...process.env,
      PATH: dir + ":" + process.env.PATH,
      EVENT_LOG: log,
      TEST_ROOT: dir,
      COVERAGE_ACCEPTANCE_DISPOSABLE: "true",
    };
    for (const key of Object.keys(env)) if (key.endsWith("DATABASE_URL")) delete env[key];
    const script = path.resolve(__dirname, "../../scripts/coverage-acceptance.sh");
    const run = spawnSync("bash", [script, "queue"], { env, encoding: "utf8" });
    assert.equal(run.status, 17, run.stderr);
    const events = fs.readFileSync(log, "utf8").split("\n"),
      producer = events.indexOf("producer");
    assert.equal(events.slice(0, producer).filter((v) => v.includes("get pods")).length, 4);
    assert.ok(
      events
        .slice(producer + 1)
        .some((v) => v.includes("scale deployment/workouts-explorer-worker --replicas=1")),
    );
    assert.ok(
      events
        .slice(producer + 1)
        .some((v) => v.includes("scale deployment/workouts-explorer-coverage-worker --replicas=1")),
    );
    fs.writeFileSync(log, "");
    const refused = spawnSync("bash", [script, "queue"], {
      env: { ...env, PROFILE_GUARD_EXIT: "19" },
      encoding: "utf8",
    });
    assert.equal(refused.status, 19);
    assert.equal(fs.readFileSync(log, "utf8"), "profile-guard\n");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("cleanup stops supervisor before deletion and also stops when namespace is absent", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "acceptance-cleanup-"));
  const log = path.join(dir, "events");
  try {
    fs.writeFileSync(
      path.join(dir, "kubectl"),
      '#!/bin/sh\nprintf "%s\\n" "$*" >> "$EVENT_LOG"\ncase "$*" in *"get namespace"*) printf "%s" "$TEST_LABEL";; esac\n',
      { mode: 0o755 },
    );
    fs.writeFileSync(
      path.join(dir, "node"),
      '#!/bin/sh\ncase "$1" in *profile.cjs) case "$2" in name) printf typical;; require) printf "profile-guard\\n" >> "$EVENT_LOG"; exit "${PROFILE_GUARD_EXIT:-0}";; esac;; *) printf "stop-supervisor\\n" >> "$EVENT_LOG";; esac\n',
      { mode: 0o755 },
    );
    for (const label of ["true", ""]) {
      fs.writeFileSync(log, "");
      const env = {
        ...process.env,
        PATH: dir + ":" + process.env.PATH,
        EVENT_LOG: log,
        TEST_LABEL: label,
        COVERAGE_ACCEPTANCE_DISPOSABLE: "true",
        COVERAGE_ACCEPTANCE_CLEANUP_CONFIRMED: "yes",
      };
      for (const key of Object.keys(env)) if (key.endsWith("DATABASE_URL")) delete env[key];
      const run = spawnSync(
        "bash",
        [path.resolve(__dirname, "../../scripts/coverage-acceptance.sh"), "cleanup"],
        { env, encoding: "utf8" },
      );
      assert.equal(run.status, 0, run.stderr);
      const events = fs.readFileSync(log, "utf8").split("\n");
      assert.ok(events.includes("stop-supervisor"));
      const deleted = events.findIndex((v) => v.includes("delete namespace"));
      if (label) assert.ok(deleted > events.indexOf("stop-supervisor"));
      else assert.equal(deleted, -1);
      if (label) {
        fs.writeFileSync(log, "");
        const refused = spawnSync(
          "bash",
          [path.resolve(__dirname, "../../scripts/coverage-acceptance.sh"), "cleanup"],
          { env: { ...env, PROFILE_GUARD_EXIT: "19" }, encoding: "utf8" },
        );
        assert.equal(refused.status, 19);
        assert.ok(!fs.readFileSync(log, "utf8").includes("stop-supervisor"));
        assert.ok(!fs.readFileSync(log, "utf8").includes("delete namespace"));
      }
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
