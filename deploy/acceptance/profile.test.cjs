const { manifest, bytes, typeKeys } = require("./profile-fixture.cjs");
const test = require("node:test");
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const childProcess = require("node:child_process");
const path = require("node:path");
const profile = require("./profile.cjs");

test("shared manifest supplies counts, raw-byte hash, paths and default profile", () => {
  assert.equal(profile.name, process.env.COVERAGE_ACCEPTANCE_PROFILE || "typical");
  assert.equal(profile.total, manifest.profiles[profile.name].total_workouts);
  assert.equal(profile.routed, profile.name === "typical" ? 329 : 511);
  assert.equal(
    profile.binding.profile_manifest_sha256,
    crypto.createHash("sha256").update(bytes).digest("hex"),
  );
  assert.match(profile.artifact("seed.json"), new RegExp(`/coverage-${profile.name}-seed.json$`));
  assert.equal(
    profile.results("acceptance"),
    path.join(__dirname, "results", `${profile.name}-results.json`),
  );
  assert.match(
    profile.results("visual"),
    new RegExp(`/coverage-visual-${profile.name}-results.json$`),
  );
});

test("literal default specifications validate independently of the mocked file model", () => {
  const defaults = {
    schema_version: 1,
    year: 2026,
    seed: 7104,
    gps_sample_seconds: 5,
    distance_meters_per_mile: 1609.344,
    other_duration_seconds: 1800,
    other_types: [
      { key: "climbing", label: "Climbing" },
      { key: "paddle_sports", label: "Paddle Sports" },
      { key: "skating_sports", label: "Skating Sports" },
    ],
    profiles: {
      typical: {
        workouts_per_day: 1,
        total_workouts: 365,
        activities: [
          {
            key: "running",
            label: "Outdoor Run",
            percentage: 60,
            count: 219,
            routed: true,
            distance_miles: 8,
            duration_seconds: 7200,
          },
          {
            key: "walking",
            label: "Outdoor Walk",
            percentage: 20,
            count: 73,
            routed: true,
            distance_miles: 8,
            duration_seconds: 7200,
          },
          {
            key: "cycling",
            label: "Outdoor Cycle",
            percentage: 10,
            count: 37,
            routed: true,
            distance_miles: 20,
            duration_seconds: 7200,
          },
          {
            key: "other",
            label: "Other",
            percentage: 10,
            count: 36,
            routed: false,
            distance_miles: 0,
            duration_seconds: 1800,
          },
        ],
      },
      stress: {
        workouts_per_day: 2,
        total_workouts: 730,
        activities: [
          {
            key: "running",
            label: "Outdoor Run",
            percentage: 40,
            count: 292,
            routed: true,
            distance_miles: 13.1,
            duration_seconds: 9000,
          },
          {
            key: "walking",
            label: "Outdoor Walk",
            percentage: 20,
            count: 146,
            routed: true,
            distance_miles: 10,
            duration_seconds: 10800,
          },
          {
            key: "cycling",
            label: "Outdoor Cycle",
            percentage: 10,
            count: 73,
            routed: true,
            distance_miles: 40,
            duration_seconds: 14400,
          },
          {
            key: "other",
            label: "Other",
            percentage: 30,
            count: 219,
            routed: false,
            distance_miles: 0,
            duration_seconds: 1800,
          },
        ],
      },
    },
  };
  assert.equal(profile.validate(defaults), defaults);
  assert.deepEqual(manifest, defaults);
});

test("default fixture has exactly six provider names with official canonical type keys", () => {
  const expected = [
    ["Outdoor Run", "outdoor-run-27e9c638ed73cbf21bee19a1d4db970f"],
    ["Outdoor Walk", "outdoor-walk-d382cfff87408643a86b7a842d85134a"],
    ["Outdoor Cycle", "outdoor-cycle-b6c2eef4fa14bc8baf203afa98e2c72e"],
    ["Climbing", "climbing-df7da510a6fe1ab64a57e49687b9b1d2"],
    ["Paddle Sports", "paddle-sports-3d6e19d8f2d58789c6c23febefa2301d"],
    ["Skating Sports", "skating-sports-7422cc76b1829e98bc46bdc5b48a665e"],
  ];
  assert.deepEqual(typeKeys, Object.fromEntries(expected));
  assert.deepEqual(
    manifest.other_types.map((a) => a.key),
    ["climbing", "paddle_sports", "skating_sports"],
  );
  for (const p of Object.values(manifest.profiles)) {
    assert.deepEqual(
      [...p.activities.slice(0, 3), ...manifest.other_types].map((a) => a.label),
      expected.map(([label]) => label),
    );
  }
  for (const [label, key] of expected) {
    const canonical = label.trim().normalize("NFKC").toLowerCase().normalize("NFKC");
    assert.equal(
      key,
      canonical.replaceAll(" ", "-") +
        "-" +
        crypto.createHash("sha256").update(canonical).digest("hex").slice(0, 32),
    );
  }
});

test("editable parameters and fractional or zero quotas regenerate parsed counts and artifact bindings", () => {
  const edited = structuredClone(manifest);
  Object.assign(edited, {
    year: 2025,
    seed: 42,
    gps_sample_seconds: 10,
    other_duration_seconds: 777,
    other_types: [
      { key: "trail_mobility", label: " Mobility practice " },
      { key: "stretch-2", label: "Stretching" },
    ],
  });
  for (const [name, counts] of [
    ["typical", [146, 110, 0, 109]],
    ["stress", [300, 180, 100, 150]],
  ]) {
    edited.profiles[name].activities.forEach((a, i) =>
      Object.assign(a, {
        count: counts[i],
        percentage: (counts[i] / edited.profiles[name].total_workouts) * 100,
        label: `Edited ${a.key}`,
        distance_miles: i < 3 ? [1.25, 2.75, 7.5][i] : 0,
        duration_seconds: i < 3 ? [300, 720, 1200][i] : 777,
      }),
    );
  }
  assert.equal(profile.validate(edited), edited);
  const raw = JSON.stringify(edited) + "\n";
  for (const name of ["typical", "stress"]) {
    const run = childProcess.spawnSync(
      process.execPath,
      [
        "--require",
        path.join(__dirname, "profile-fixture.cjs"),
        "-e",
        'const p=require("./deploy/acceptance/profile.cjs"); const e=require("./deploy/acceptance/evidence.cjs"); console.log(JSON.stringify({manifest:p.manifest,selected:p.selected,total:p.total,routed:p.routed,binding:p.binding,fresh:e.fresh({metadata:{uid:"unit-uid",creationTimestamp:"2025-01-01T00:00:00Z"}})}));',
      ],
      {
        cwd: path.resolve(__dirname, "../.."),
        env: { ...process.env, COVERAGE_ACCEPTANCE_PROFILE: name, MOCK_PROFILE_MANIFEST_JSON: raw },
        encoding: "utf8",
      },
    );
    assert.equal(run.status, 0, run.stderr);
    const parsed = JSON.parse(run.stdout);
    assert.deepEqual(parsed.manifest, edited);
    assert.deepEqual(parsed.selected, edited.profiles[name]);
    assert.equal(parsed.total, edited.profiles[name].total_workouts);
    assert.equal(
      parsed.routed,
      edited.profiles[name].activities.filter((a) => a.routed).reduce((n, a) => n + a.count, 0),
    );
    assert.equal(parsed.binding.profile, name);
    assert.equal(parsed.binding.profile_version, 1);
    assert.equal(
      parsed.binding.profile_manifest_sha256,
      crypto.createHash("sha256").update(raw).digest("hex"),
    );
    assert.notEqual(
      parsed.binding.profile_manifest_sha256,
      profile.binding.profile_manifest_sha256,
    );
    assert.equal(
      parsed.fresh.evidence.profile_manifest_sha256,
      parsed.binding.profile_manifest_sha256,
    );
    assert.equal(parsed.fresh.evidence.seed, null);
    assert.deepEqual(parsed.fresh.evidence.phases, {});
    assert.throws(() => profile.assertProfile(parsed.binding), /manifest hash mismatch/);
  }
});

test("manifest accepts inclusive bounds, calendar-century rules and catalog/label edges", () => {
  for (const [year, seed, cadence, duration] of [
    [1900, -2147483648, 1, 1],
    [2100, 2147483647, 30, 25200],
  ]) {
    const copy = structuredClone(manifest);
    Object.assign(copy, {
      year,
      seed,
      gps_sample_seconds: cadence,
      other_duration_seconds: duration,
    });
    copy.other_types = [
      { key: "a", label: "\u0085 Climbing \u0085" },
      { key: "b" + "_".repeat(511), label: "\uFEFF" },
    ];
    for (const p of Object.values(copy.profiles))
      for (const a of p.activities) a.duration_seconds = duration;
    assert.equal(profile.validate(copy), copy);
  }
  const rounded = structuredClone(manifest);
  rounded.profiles.typical.activities[0].percentage += 100 / 365;
  rounded.profiles.typical.activities[1].percentage -= 100 / 365;
  assert.equal(profile.validate(rounded), rounded);
  const zeroQuotas = structuredClone(manifest);
  for (const [name, counts] of [
    ["typical", [365, 0, 0, 0]],
    ["stress", [365, 365, 0, 0]],
  ]) {
    zeroQuotas.profiles[name].activities.forEach((a, i) =>
      Object.assign(a, {
        count: counts[i],
        percentage: (counts[i] / zeroQuotas.profiles[name].total_workouts) * 100,
      }),
    );
  }
  assert.equal(profile.validate(zeroQuotas), zeroQuotas);
});

test("manifest rejects invalid format, calendar, seed, cadence and shared duration", () => {
  for (const [field, values] of [
    ["schema_version", [0, 2, "1"]],
    ["year", [1899, 2101, 2024, 2000, 2025.5, "2025"]],
    ["seed", [-2147483649, 2147483648, 42.5, "42", NaN, Infinity]],
    ["gps_sample_seconds", [0, 31, 1.5, "10"]],
    ["distance_meters_per_mile", [1609, 1609.345, "1609.344", NaN, Infinity]],
    ["other_duration_seconds", [0, 25201, 777.5, "777"]],
  ])
    for (const value of values) {
      const copy = structuredClone(manifest);
      copy[field] = value;
      assert.throws(
        () => profile.validate(copy),
        /invalid acceptance profile manifest/,
        `${field}=${value}`,
      );
    }
  for (const value of [null, {}, []])
    assert.throws(() => profile.validate(value), /invalid acceptance profile manifest/);
});

test("manifest rejects missing, duplicate, reserved or invalid other types and labels", () => {
  for (const catalog of [
    undefined,
    null,
    [],
    {},
    [null],
    [
      { key: "climbing", label: "Climbing" },
      { key: "climbing", label: "Duplicate" },
    ],
    ...[
      "running",
      "walking",
      "cycling",
      "other",
      "Climbing",
      "0climbing",
      "climbing.name",
      "climbing\n",
      "climbing\r",
      "climbing\0",
      "climb\u00e9",
      "a".repeat(513),
      "",
    ].map((key) => [{ key, label: "Climbing" }]),
    ...[undefined, null, "", " \t\n", "\u0085\u00a0", "Climbing\0", 7].map((label) => [
      { key: "climbing", label },
    ]),
  ]) {
    const copy = structuredClone(manifest);
    copy.other_types = catalog;
    assert.throws(() => profile.validate(copy), /catalog|other type/);
  }
});

test("manifest rejects invalid profile identities, ordered activities, quotas and route fields", () => {
  const changes = [
    (m) => (m.profiles.legacy = m.profiles.typical),
    (m) => delete m.profiles.stress,
    (m) => delete m.profiles.typical,
    (m) => (m.profiles.typical = null),
    (m) => (m.profiles.typical.workouts_per_day = 2),
    (m) => (m.profiles.stress.workouts_per_day = 1),
    (m) => (m.profiles.typical.total_workouts = 730),
    (m) => (m.profiles.stress.total_workouts = 365),
    (m) => m.profiles.typical.activities.pop(),
    (m) => m.profiles.typical.activities.push(m.profiles.typical.activities[0]),
    (m) => m.profiles.typical.activities.reverse(),
    (m) => (m.profiles.typical.activities[0] = null),
    (m) => m.profiles.typical.activities[0].count++,
    (m) => {
      const a = m.profiles.typical.activities;
      a[0].count += 2;
      a[1].count -= 2;
    },
    (m) => {
      m.profiles.typical.activities.forEach((a, i) =>
        Object.assign(a, { count: i === 3 ? 365 : 0, percentage: i === 3 ? 100 : 0 }),
      );
    },
    (m) => {
      const a = m.profiles.typical.activities;
      a[0].percentage += 1e-6;
    },
    (m) => (m.profiles.typical.activities[0].key = "other"),
  ];
  for (const [field, values] of [
    ["label", [undefined, "", " \t\n", "\u0085", "Outdoor Run\0", 7]],
    ["count", [-1, 366, 1.5, "219"]],
    ["percentage", [-1, 101, NaN, Infinity, -Infinity, "60"]],
    ["routed", [false, "true", 1]],
    ["distance_miles", [-1, 0, NaN, Infinity, Number.MAX_VALUE, "8"]],
    ["duration_seconds", [0, 25201, 1.5, "7200"]],
  ])
    for (const value of values)
      changes.push((m) => (m.profiles.typical.activities[0][field] = value));
  for (const change of [
    (a) => (a.routed = true),
    (a) => (a.distance_miles = 1),
    (a) => a.duration_seconds++,
    (a) => (a.duration_seconds = 0),
  ]) {
    changes.push((m) => change(m.profiles.typical.activities[3]));
  }
  for (const change of changes) {
    const copy = structuredClone(manifest);
    change(copy);
    assert.throws(() => profile.validate(copy), /manifest|profile/);
  }
});

test("CLI rejects unknown names before accessing Kubernetes", () => {
  const run = childProcess.spawnSync(
    process.execPath,
    [
      "--require",
      path.join(__dirname, "profile-fixture.cjs"),
      path.join(__dirname, "profile.cjs"),
      "name",
    ],
    {
      env: { ...process.env, COVERAGE_ACCEPTANCE_PROFILE: "legacy" },
      encoding: "utf8",
    },
  );
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /must be typical or stress/);
});

test("requireProfile checks namespace, ConfigMap, database binding and UID without mutating", (t) => {
  const disposable = process.env.COVERAGE_ACCEPTANCE_DISPOSABLE;
  process.env.COVERAGE_ACCEPTANCE_DISPOSABLE = "true";
  t.after(() => {
    if (disposable === undefined) delete process.env.COVERAGE_ACCEPTANCE_DISPOSABLE;
    else process.env.COVERAGE_ACCEPTANCE_DISPOSABLE = disposable;
  });
  const ns = {
    metadata: {
      name: "workouts-acceptance",
      uid: "fixture-uid",
      labels: { "workouts-disposable": "true" },
      annotations: { "workouts-acceptance/profile": JSON.stringify(profile.binding) },
    },
  };
  const cm = {
    data: { "binding.json": JSON.stringify(profile.binding), "manifest.json": bytes.toString() },
  };
  let namespace = ns,
    configmap = cm,
    db = { ...profile.binding, namespace_uid: ns.metadata.uid };
  const calls = [];
  t.mock.method(childProcess, "execFileSync", (command, args) => {
    assert.equal(command, "kubectl");
    calls.push(args);
    if (args.includes("namespace")) return JSON.stringify(namespace);
    if (args.includes("configmap")) return JSON.stringify(configmap);
    assert.ok(args.includes("psql"));
    assert.ok(args.at(-1).startsWith("SELECT binding"));
    return JSON.stringify(db);
  });
  assert.equal(profile.requireProfile().metadata.uid, ns.metadata.uid);
  for (const [key, value] of [
    ["profile", profile.name === "typical" ? "stress" : "typical"],
    ["profile_version", 2],
    ["profile_manifest_sha256", "0".repeat(64)],
  ]) {
    db = { ...profile.binding, namespace_uid: ns.metadata.uid, [key]: value };
    assert.throws(() => profile.requireProfile(), /profile/);
    db = { ...profile.binding, namespace_uid: ns.metadata.uid };
    namespace = structuredClone(ns);
    namespace.metadata.annotations["workouts-acceptance/profile"] = JSON.stringify({
      ...profile.binding,
      [key]: value,
    });
    assert.throws(() => profile.requireProfile(), /profile/);
    namespace = ns;
    configmap = {
      data: { ...cm.data, "binding.json": JSON.stringify({ ...profile.binding, [key]: value }) },
    };
    assert.throws(() => profile.requireProfile(), /profile/);
    configmap = cm;
  }
  db.namespace_uid = "old";
  assert.throws(() => profile.requireProfile(), /another namespace/);
  assert.equal(profile.requireProfile({ database: false }).metadata.uid, ns.metadata.uid);
  configmap = { data: { ...cm.data, "manifest.json": cm.data["manifest.json"] + " " } };
  assert.throws(() => profile.requireProfile(), /manifest differs/);
  configmap = cm;
  namespace = structuredClone(ns);
  namespace.metadata.labels["workouts-disposable"] = "false";
  assert.throws(() => profile.requireProfile(), /disposable/);
  assert.ok(
    calls.every((args) => !args.some((a) => ["apply", "create", "delete", "scale"].includes(a))),
  );
});
