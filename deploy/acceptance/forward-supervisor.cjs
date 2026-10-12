const fs = require("node:fs");
const path = require("node:path");
const { spawn, execFileSync } = require("node:child_process");

const directory = "/tmp/opencode";
const pidFile = path.join(directory, "coverage-acceptance-forward.pid");
const lockFile = path.join(directory, "coverage-acceptance-forward.lock");

function lock(pid = pidFile, lock = lockFile) {
  const fd = fs.openSync(lock, "wx", 0o600);
  try {
    const p = fs.openSync(pid, "wx", 0o600);
    fs.writeFileSync(p, String(process.pid) + "\n");
    fs.closeSync(p);
  } catch (error) {
    fs.closeSync(fd);
    fs.unlinkSync(lock);
    throw error;
  }
  return () => {
    fs.closeSync(fd);
    fs.unlinkSync(pid);
    fs.unlinkSync(lock);
  };
}

const alive = (pid) => {
  try {
    return fs.readFileSync(`/proc/${pid}/stat`, "utf8").split(") ")[1][0] !== "Z";
  } catch {
    return false;
  }
};

async function stop() {
  if (!fs.existsSync(pidFile)) {
    if (fs.existsSync(lockFile)) throw Error("stale supervisor lock; operator inspection required");
    return;
  }
  const pid = Number(fs.readFileSync(pidFile, "utf8").trim());
  if (!Number.isInteger(pid) || pid <= 1) throw Error("invalid supervisor PID");
  if (!alive(pid)) throw Error("stale PID file; refusing to guess ownership");
  const command = fs.readFileSync(`/proc/${pid}/cmdline`, "utf8");
  if (!command.includes(path.resolve(__filename)))
    throw Error("PID belongs to an unmanaged process; refusing to signal it");
  process.kill(pid, "SIGTERM");
  for (let i = 0; i < 150; i++) {
    if (!fs.existsSync(pidFile) && !fs.existsSync(lockFile) && !alive(pid)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw Error("supervisor did not confirm child shutdown");
}

function start(options = {}) {
  const release = lock(options.pidFile || pidFile, options.lockFile || lockFile);
  let stopping = false;
  const children = new Map(),
    timers = new Set();
  const signals = options.signals || process,
    launch = options.spawn || spawn;
  const namespace =
    options.namespace ||
    (() => {
      const ns = JSON.parse(
        execFileSync(
          "kubectl",
          ["--context", "xdev", "get", "namespace", "workouts-acceptance", "-o", "json"],
          { encoding: "utf8" },
        ),
      );
      if (ns.metadata.labels["workouts-disposable"] !== "true") throw Error("not disposable");
      return ns.metadata.uid;
    });
  let uid;
  try {
    uid = namespace();
  } catch (error) {
    release();
    throw error;
  }

  function finish() {
    if (stopping && !children.size) {
      release();
      signals.exitCode = 0;
    }
  }

  function shutdown() {
    if (stopping) return;
    stopping = true;
    for (const t of timers) clearTimeout(t);
    timers.clear();
    for (const c of children.values()) c.kill("SIGTERM");
    finish();
  }

  for (const signal of ["SIGINT", "SIGTERM"]) signals.on(signal, shutdown);

  function forward(service, ports) {
    if (stopping) return;
    try {
      if (namespace() !== uid) {
        shutdown();
        return;
      }
    } catch {
      shutdown();
      return;
    }
    const child = launch(
      "kubectl",
      [
        "--context",
        "xdev",
        "-n",
        "workouts-acceptance",
        "port-forward",
        `service/${service}`,
        ports,
      ],
      { stdio: "inherit" },
    );
    children.set(service, child);
    child.on("error", shutdown);
    child.on("close", () => {
      children.delete(service);
      if (stopping) {
        finish();
        return;
      }
      const t = setTimeout(() => {
        timers.delete(t);
        forward(service, ports);
      }, 1000);
      timers.add(t);
    });
  }

  for (const [service, port] of [
    ["acceptance-postgres", "15432:5432"],
    ["workouts-explorer-api", "18081:80"],
    ["workouts-explorer-tiles", "18083:3000"],
    ["acceptance-telemetry", "18089:8889"],
    ["workouts-explorer-ui", "18080:80"],
  ])
    forward(service, port);
}

module.exports = { lock, start };

if (require.main === module) {
  if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true")
    throw Error("disposable switch required");
  if (process.argv[2] === "stop")
    stop().catch((error) => {
      console.error(error.message);
      process.exitCode = 1;
    });
  else start();
}
