const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const e = require("./evidence.cjs");
const profile = require("./profile.cjs");

if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true")
  throw Error("disposable switch required");
const action = process.argv[2];
const cases = {
  seed: "Seed",
  queue: "Backlog",
  diagnostics: "Diagnostics",
  "general-worker": "GeneralWorker",
  "matcher-smoke": "MatcherSmoke",
  validate: "ValidateFixture",
  reads: "Reads",
  viewport: "Viewport",
};
if (!cases[action]) throw Error("unknown fixture action");
const ns = profile.requireProfile();
const phase = process.env.COVERAGE_ACCEPTANCE_PHASE || "warm";
const backlog = process.env.COVERAGE_ACCEPTANCE_BACKLOG || "48";
if (action === "reads" && !/^(baseline|backlog48|backlog96|warm)$/.test(phase))
  throw Error("invalid phase");
if (action === "queue" && !/^(48|96)$/.test(backlog)) throw Error("invalid backlog");
const resultFile = profile.artifact(
  `${action === "reads" ? phase : action === "queue" ? `queue${backlog}` : action === "seed" ? "profile" : action}.json`,
);

const start = new Date();
if (action === "viewport" && fs.existsSync(resultFile)) {
  const previous = JSON.parse(fs.readFileSync(resultFile, "utf8"));
  if (previous.namespace !== e.NAMESPACE || !/^[a-zA-Z0-9_-]+$/.test(previous.namespace_uid || ""))
    throw Error("cannot archive an unbound previous viewport artifact");
  fs.copyFileSync(
    resultFile,
    profile.artifact(`viewport-${previous.namespace_uid}-${start.getTime()}-previous.json`),
    fs.constants.COPYFILE_EXCL,
  );
}
let output = "",
  pending = "",
  testStarted = null,
  testFinished = null;
const root = path.resolve(__dirname, "../..");
const child = spawn(
  "go",
  [
    "test",
    "./worker",
    "-run",
    `^TestCoverageAcceptance${cases[action]}$`,
    "-count=1",
    "-json",
    "-timeout=45m",
  ],
  {
    cwd: root,
    detached: true,
    env: {
      ...process.env,
      COVERAGE_ACCEPTANCE_PROFILE: profile.name,
      COVERAGE_ACCEPTANCE_PROFILE_VERSION: String(profile.binding.profile_version),
      COVERAGE_ACCEPTANCE_PROFILE_MANIFEST_SHA256: profile.binding.profile_manifest_sha256,
      COVERAGE_ACCEPTANCE_RESULTS: resultFile,
      COVERAGE_ACCEPTANCE_ACTION: action,
      COVERAGE_ACCEPTANCE_NAMESPACE_UID: ns.metadata.uid,
    },
    stdio: ["ignore", "pipe", "pipe"],
  },
);
let stopping = false;

for (const signal of ["SIGINT", "SIGTERM"])
  process.on(signal, () => {
    stopping = true;
    try {
      process.kill(-child.pid, signal);
    } catch {}
  });

child.stdout.on("data", (data) => {
  pending += data;
  let newline;
  while ((newline = pending.indexOf("\n")) >= 0) {
    const line = pending.slice(0, newline);
    pending = pending.slice(newline + 1);
    const event = JSON.parse(line);
    if (event.Output) {
      output += event.Output;
      process.stdout.write(event.Output);
    }
    if (event.Test === `TestCoverageAcceptance${cases[action]}`) {
      if (event.Action === "run") testStarted = event.Time;
      if (["pass", "fail", "skip"].includes(event.Action)) testFinished = event.Time;
    }
  }
});
child.stderr.on("data", (data) => process.stderr.write(data));

child.on("close", (code, signal) => {
  if (stopping || signal) {
    process.exitCode = 1;
    return;
  }
  const end = new Date();
  if (profile.requireProfile().metadata.uid !== ns.metadata.uid)
    throw Error("namespace changed during fixture run");
  const binding = {
    ...profile.binding,
    schema_version: e.SCHEMA,
    namespace: e.NAMESPACE,
    namespace_uid: ns.metadata.uid,
    started_at: testStarted || start.toISOString(),
    recorded_at: testFinished || end.toISOString(),
    driver_started_at: start.toISOString(),
    driver_finished_at: end.toISOString(),
    window_kind:
      testStarted && testFinished
        ? "Go test case, including fixture setup and controlled slot hold"
        : "driver wall window; case timestamps unavailable",
    provenance: { ...profile.binding, kind: "captured_fenced_run", namespace_uid: ns.metadata.uid },
    status: code === 0 ? "PASS" : "FAIL",
  };
  if (action === "diagnostics") {
    const match = output.match(/competing diagnostic outcomes=map\[([^\]]*)\]/);
    const counts = match
      ? Object.fromEntries(
          match[1]
            .trim()
            .split(/\s+/)
            .filter(Boolean)
            .map((pair) => {
              const [status, count] = pair.split(":");
              return [status, Number(count)];
            }),
        )
      : null;
    fs.writeFileSync(
      profile.artifact("diagnostics.json"),
      JSON.stringify(
        {
          ...binding,
          statuses: counts,
          requests: counts ? Object.values(counts).reduce((a, b) => a + b, 0) : null,
          global_matcher_limit: 1,
          controlled_slot_hold_seconds: 3,
        },
        null,
        2,
      ) + "\n",
    );
  } else if (action === "seed") {
    let measured = {};
    if (fs.existsSync(resultFile) && fs.statSync(resultFile).mtimeMs >= start.getTime())
      measured = e.loadMeasurement(resultFile, ns);
    else if (code === 0)
      throw Error("seed succeeded without a fresh profile-bound fixture artifact");
    fs.writeFileSync(
      profile.artifact("seed.json"),
      JSON.stringify(
        {
          ...measured,
          ...binding,
          seed_seconds: measured.duration_seconds ?? null,
          driver_elapsed_seconds: (end - start) / 1000,
          note: "Seed-only duration is reported only from the Go fixture; driver wall time includes build/startup.",
        },
        null,
        2,
      ) + "\n",
    );
  } else if (["queue", "general-worker", "reads", "matcher-smoke", "viewport"].includes(action)) {
    if (fs.existsSync(resultFile) && fs.statSync(resultFile).mtimeMs >= start.getTime()) {
      const measured = e.loadMeasurement(resultFile, ns);
      if (action === "matcher-smoke" && measured.status === "FAIL") code = code || 1;
      if (action === "viewport") {
        const producerExit = code;
        const assessment = e.assessViewport(measured);
        if (assessment.status !== "PASS") code = code || 1;
        measured.assessment = assessment;
        measured.producer_exit_code = producerExit;
      }
      fs.writeFileSync(
        resultFile,
        JSON.stringify(
          {
            ...measured,
            ...binding,
            status:
              action === "viewport"
                ? code === 0
                  ? measured.assessment.status
                  : measured.status === "PASS" || !measured.status
                    ? "FAIL"
                    : measured.status
                : code === 0
                  ? (measured.status ?? binding.status)
                  : "FAIL",
            started_at: measured.started_at || binding.started_at,
            recorded_at: measured.recorded_at || binding.recorded_at,
          },
          null,
          2,
        ) + "\n",
      );
    } else if (action === "viewport") {
      const producerExit = code;
      code = code || 1;
      const failed = {
        ...binding,
        status: "FAIL",
        producer_exit_code: producerExit,
        cases: [],
        errors: [
          {
            kind: "missing_fresh_artifact",
            message: "Go fixture did not emit a fresh profile-bound viewport measurement",
          },
        ],
      };
      failed.assessment = e.assessViewport(failed);
      fs.writeFileSync(resultFile, JSON.stringify(failed, null, 2) + "\n");
    } else if (code === 0) {
      throw Error("fixture succeeded without a fresh profile-bound measurement");
    }
  }

  if (action === "viewport" && fs.existsSync(resultFile)) {
    const receipt = profile.artifact(
      `viewport-${ns.metadata.uid}-${start.getTime()}-captured.json`,
    );
    fs.copyFileSync(resultFile, receipt, fs.constants.COPYFILE_EXCL);
    console.log(
      JSON.stringify({
        action,
        status: e.loadMeasurement(resultFile, ns).status,
        artifact: resultFile,
        receipt,
      }),
    );
  }

  process.exitCode = code || 0;
});
