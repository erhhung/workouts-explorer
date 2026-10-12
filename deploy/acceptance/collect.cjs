const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const e = require("./evidence.cjs");
const profile = require("./profile.cjs");

if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true")
  throw Error("explicit disposable switch required");
const namespace = profile.requireProfile();
const target = process.env.COVERAGE_ACCEPTANCE_COLLECT_OUTPUT || profile.results("acceptance");
const archive =
  process.env.COVERAGE_ACCEPTANCE_ARCHIVE ||
  profile.artifact(`acceptance-${namespace.metadata.uid}-raw.json`);

for (const file of [archive, target]) {
  const resolved = path.resolve(file);
  if (
    resolved !== profile.results("acceptance") &&
    !resolved.startsWith(`/tmp/opencode/coverage-${profile.name}-`)
  )
    throw Error("output outside profile-owned paths");
}

const k = (...args) =>
  execFileSync("kubectl", ["--context", "xdev", "-n", e.NAMESPACE, ...args], {
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
  });

// Never initialize state from committed compact evidence or unbound temp files.
const state = e.loadArchive(archive, namespace),
  result = state.evidence;
result.generation_parameters = structuredClone(profile.manifest);
const phase = process.argv[2] || "snapshot";
if (!/^[a-z][a-z0-9_-]*$/.test(phase)) throw Error("invalid snapshot phase");

const psql = (sql, db = "acceptance_benchmark") =>
  JSON.parse(
    k(
      "exec",
      "deployment/acceptance-postgres",
      "--",
      "psql",
      "-U",
      "postgres",
      "-d",
      db,
      "-At",
      "-v",
      "ON_ERROR_STOP=1",
      "-c",
      sql,
    ),
  );

const snapshot = {
  ...profile.binding,
  schema_version: e.SCHEMA,
  namespace: e.NAMESPACE,
  namespace_uid: namespace.metadata.uid,
  recorded_at: new Date().toISOString(),
  pods: {},
  metrics: {},
  host_metrics: {},
};

for (const p of JSON.parse(k("get", "pods", "-o", "json")).items) {
  if (p.status.phase !== "Running") continue;
  const name = p.metadata.labels["app.kubernetes.io/component"] || p.metadata.labels.app;
  if (!name) continue;
  snapshot.pods[name] = {
    ready: p.status.containerStatuses?.every((c) => c.ready),
    restarts: p.status.containerStatuses?.reduce((n, c) => n + c.restartCount, 0),
    termination_reasons: p.status.containerStatuses
      ?.map((c) => c.lastState?.terminated?.reason)
      .filter(Boolean),
    images: p.status.containerStatuses?.map((c) => c.imageID),
    resources: p.spec.containers.map((c) => c.resources),
  };
}

snapshot.database =
  psql(`WITH owner AS (SELECT u.account_id FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id WHERE p.username='acceptance-fixture'),
  fixture AS (SELECT w.*,t.type_key,t.provider_label AS type_provider_label,EXISTS(SELECT 1 FROM app.workout_route_points r WHERE r.account_id=w.account_id AND r.workout_id=w.id) routed,
    (SELECT CASE a.unit WHEN 'mi' THEN a.value WHEN 'km' THEN a.value/1.609344 WHEN 'm' THEN a.value/1609.344 END
      FROM app.workout_aggregates a WHERE a.account_id=w.account_id AND a.workout_id=w.id AND a.metric='distance') distance_miles
    FROM app.workouts w JOIN owner USING(account_id) JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id)
  SELECT jsonb_build_object(
  'fixture_accounts',(SELECT count(*) FROM owner),'database_accounts',(SELECT count(*) FROM app.accounts),
  'workouts',(SELECT count(*) FROM fixture),'routed_workouts',(SELECT count(*) FROM fixture WHERE routed),'unrouted_workouts',(SELECT count(*) FROM fixture WHERE NOT routed),
  'types',(SELECT jsonb_object_agg(type_key,data) FROM (SELECT type_key,jsonb_build_object('provider_label',min(type_provider_label),'count',count(*),'routed',count(*) FILTER(WHERE routed),'unrouted',count(*) FILTER(WHERE NOT routed),
    'duration_seconds',sum(provider_duration),'distance_miles',sum(distance_miles)) data FROM fixture GROUP BY type_key) s),
  'daily_counts',(SELECT jsonb_object_agg(local_start_date,n) FROM (SELECT local_start_date,count(*) n FROM fixture GROUP BY 1) s),
  'duration_counts',(SELECT jsonb_object_agg(provider_duration,n) FROM (SELECT provider_duration,count(*) n FROM fixture GROUP BY 1) s),
  'distance_miles_counts',(SELECT jsonb_object_agg(distance_miles,n) FROM (SELECT distance_miles,count(*) n FROM fixture WHERE distance_miles IS NOT NULL GROUP BY 1) s),
  'duration_seconds',(SELECT sum(provider_duration) FROM fixture),'distance_miles',(SELECT sum(distance_miles) FROM fixture),
  'route_points',(SELECT count(*) FROM app.workout_route_points JOIN owner USING(account_id)),
  'segment_matches',(SELECT count(*) FROM app.workout_segment_matches JOIN owner USING(account_id)),
  'path_copies',(SELECT count(*) FROM app.path_segments JOIN owner USING(account_id)),
  'month_counts',(SELECT jsonb_object_agg(month_key,n) FROM (SELECT to_char(local_start_date,'YYYY-MM') AS month_key,count(*) n FROM fixture GROUP BY 1) s),
  'readiness',(SELECT jsonb_object_agg(readiness_state,n) FROM (SELECT readiness_state,count(*) n FROM app.workout_coverage_states JOIN owner USING(account_id) GROUP BY 1) s),
  'processing',(SELECT jsonb_object_agg(processing_state,n) FROM (SELECT processing_state,count(*) n FROM app.workout_coverage_states JOIN owner USING(account_id) GROUP BY 1) s),
  'matcher_slots',(SELECT count(*) FROM app.matcher_slots),'schema',(SELECT schema_version FROM app.schema_metadata),
  'postgres',current_setting('server_version'),'postgis',postgis_lib_version(),
  'diagnostics_preference',(SELECT coverage_diagnostics_enabled FROM app.preferences preference JOIN app.users owner ON owner.account_id=preference.account_id
    JOIN app.authentication_principals principal ON principal.id=owner.principal_id WHERE principal.username='acceptance-fixture'))`);
snapshot.osm = psql(
  `SELECT jsonb_build_object('schema_version',(SELECT schema_version FROM osm_catalog.schema_metadata),
  'canonical_edges',(SELECT count(*) FROM osm_canonical.path_segments),'canonical_ways',(SELECT count(*) FROM osm_canonical.ways),
  'reader_can_mutate_tables',has_table_privilege('workouts_coverage_worker','osm_canonical.path_segments','INSERT,UPDATE,DELETE'),
  'reader_can_execute_mutable_functions',EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname IN ('osm_catalog','osm_active') AND p.proname NOT IN ('promotion_event_head','read_promotion_events','timezone_at')
      AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')))`,
  "osm",
);

try {
  const text = execFileSync("curl", ["-fsS", "http://127.0.0.1:18089/metrics"], {
    encoding: "utf8",
  });
  for (const line of text.split("\n")) {
    const m = line.match(/^([a-zA-Z_:][a-zA-Z_0-9:]*)(?:\{(.*)\})? ([0-9.eE+-]+)$/);
    if (!m || m[1].endsWith("_bucket") || !/^(pgxpool_|coverage_)/.test(m[1])) continue;
    const pool =
      m[2]?.match(/db_client_connection_pool_name="([a-zA-Z0-9-]+)"/)?.[1] ||
      m[2]?.match(/service_name="([a-zA-Z0-9-]+)"/)?.[1] ||
      "aggregate";
    const key = `${pool}/${m[1]}`;
    snapshot.metrics[key] = (snapshot.metrics[key] || 0) + Number(m[3]);
  }
} catch {
  snapshot.metrics_unavailable = true;
}

const hostPods = JSON.parse(
  execFileSync(
    "kubectl",
    [
      "--context",
      "homelab",
      "-n",
      "vcluster-dev",
      "get",
      "pods",
      "-l",
      "vcluster.loft.sh/namespace=workouts-acceptance",
      "-o",
      "json",
    ],
    { encoding: "utf8" },
  ),
).items;
for (const p of hostPods) {
  if (p.status.phase !== "Running") continue;
  const name = p.metadata.labels["app.kubernetes.io/component"] || p.metadata.labels.app;
  if (!name) continue;
  const selector = `{namespace="vcluster-dev",pod="${p.metadata.name}",container!="",container!="POD"}`;
  snapshot.host_metrics[name] = {};
  for (const [key, query] of Object.entries({
    peak_working_set_bytes: `max(max_over_time(container_memory_working_set_bytes${selector}[15m]))`,
    peak_cpu_cores_1m: `max(max_over_time(rate(container_cpu_usage_seconds_total${selector}[1m])[15m:15s]))`,
    throttled_period_ratio_15m: `sum(increase(container_cpu_cfs_throttled_periods_total${selector}[15m]))/sum(increase(container_cpu_cfs_periods_total${selector}[15m]))`,
  })) {
    try {
      const reply = JSON.parse(
        execFileSync(
          "curl",
          [
            "-ksS",
            "--get",
            "--data-urlencode",
            `query=${query}`,
            "https://127.0.0.1:19090/api/v1/query",
          ],
          { encoding: "utf8" },
        ),
      );
      const raw = reply.data?.result?.[0]?.value?.[1];
      const number = raw === undefined ? NaN : Number(raw);
      snapshot.host_metrics[name][key] = Number.isFinite(number) ? number : null;
    } catch {
      snapshot.host_metrics[name][key] = null;
    }
  }
}

state.snapshots[`${profile.name}-${phase}`] = snapshot;
const inputs = {
  seed: "coverage-seed.json",
  general_worker: "coverage-general-worker.json",
  diagnostics_429: "coverage-diagnostics.json",
  matcher_smoke: "coverage-matcher-smoke.json",
  viewport: "coverage-viewport.json",
  viewport_browser: "coverage-viewport-browser.json",
};
for (const name of ["baseline", "backlog48", "backlog96", "warm"])
  inputs[`phase:${name}`] = `coverage-${name}.json`;
for (const count of [48, 96]) inputs[`queue:${count}`] = `coverage-queue${count}.json`;
result.rejected_measurements = {};
for (const [key, file] of Object.entries(inputs)) {
  let value;
  try {
    value = e.loadMeasurement(profile.artifact(file.slice("coverage-".length)), namespace);
  } catch (error) {
    console.warn(`Ignored ${file}: ${error.message}`);
    result.rejected_measurements[key] = {
      artifact: profile.artifact(file.slice("coverage-".length)),
      error: error.message,
    };
    if (["viewport", "viewport_browser"].includes(key)) result[key] = null;
    continue;
  }
  if (!value) continue;
  if (key.startsWith("phase:")) {
    const name = key.slice(6);
    result.phases[name] = { ...value, finished_at: value.recorded_at };
  } else if (key.startsWith("queue:")) {
    (state.measurements.queue_windows ||= {})[`backlog${key.slice(6)}`] = value;
  } else {
    result[key] = value;
    if (key === "diagnostics_429")
      result.diagnostics_note =
        "Only captured UID-bound probe counts are reported; earlier uncaptured console outcomes are not aggregated.";
  }
}

const stamp = (v) => {
  if (
    !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(v) ||
    !Number.isFinite(Date.parse(v))
  )
    throw Error("invalid DB cohort timestamp");
  return v;
};

const windows = Object.entries(state.measurements.queue_windows || {})
  .filter(([name]) => /^backlog(48|96)$/.test(name))
  .map(
    ([name, w]) =>
      `('${name}','${stamp(w.created_start)}'::timestamptz,'${stamp(w.created_end)}'::timestamptz)`,
  );
if (windows.length)
  result.matcher =
    psql(`WITH windows(cohort,start_at,end_at) AS(VALUES ${windows.join(",")}),selected AS(
 SELECT j.*,c.duration_milliseconds,c.timeout_retry_count,c.result_outcome,w.cohort FROM app.jobs j
 JOIN app.coverage_route_job_contexts c ON c.job_id=j.id JOIN windows w ON j.created_at BETWEEN w.start_at AND w.end_at WHERE j.kind='coverage_update_route')
 SELECT jsonb_object_agg(cohort,data) FROM(SELECT cohort,jsonb_build_object('children',count(*),'succeeded',count(*) FILTER(WHERE status='succeeded'),
 'applied',count(*) FILTER(WHERE result_outcome='applied'),'failed',count(*) FILTER(WHERE status='failed'),'cancelled',count(*) FILTER(WHERE status='cancelled'),
 'timeouts',count(*) FILTER(WHERE failure_code='coverage-route-timeout'),'timeout_retries',sum(timeout_retry_count),
 'reclaimed_attempts',sum(greatest(attempt-1,0)),'retry_children',count(*) FILTER(WHERE retry_of_job_id IS NOT NULL),
 'duration_ms_p50',percentile_cont(.5) WITHIN GROUP(ORDER BY duration_milliseconds),'duration_ms_p95',percentile_cont(.95) WITHIN GROUP(ORDER BY duration_milliseconds),
 'duration_ms_max',max(duration_milliseconds),'queue_age_seconds_p50',percentile_cont(.5) WITHIN GROUP(ORDER BY extract(epoch FROM started_at-created_at)),
 'queue_age_seconds_p95',percentile_cont(.95) WITHIN GROUP(ORDER BY extract(epoch FROM started_at-created_at)),'queue_age_seconds_max',max(extract(epoch FROM started_at-created_at)),
 'created_start',min(created_at),'created_end',max(created_at),'execution_start',min(started_at),'execution_end',max(terminal_at),
 'completion_span_seconds',extract(epoch FROM max(terminal_at)-min(started_at)),
 'throughput_routes_per_second',count(*)/NULLIF(extract(epoch FROM max(terminal_at)-min(started_at)),0)) data FROM selected GROUP BY cohort)s`);

result.current_observation = {
  ...profile.binding,
  schema_version: e.SCHEMA,
  namespace: e.NAMESPACE,
  namespace_uid: namespace.metadata.uid,
  recorded_at: snapshot.recorded_at,
  fixture: snapshot.database,
  osm: snapshot.osm,
};
result.images = Object.fromEntries(
  Object.entries(snapshot.pods).map(([name, p]) => [name, p.images[0]]),
);
result.pods ||= {};
result.resource_measurement ||= {
  windows: {},
  memory: "Sampled 15-minute working-set peaks",
  cpu: "Peak 1-minute rate",
};
for (const [name, p] of Object.entries(snapshot.pods)) {
  const recorded = Object.entries(state.snapshots).filter(
    ([, s]) => s.namespace_uid === namespace.metadata.uid,
  );
  const values = recorded.map(([, s]) => s.host_metrics?.[name]).filter(Boolean);
  const max = (key) => {
    const numbers = values.map((v) => v[key]).filter(Number.isFinite);
    return numbers.length ? Math.max(...numbers) : null;
  };
  const bytes = max("peak_working_set_bytes");
  result.pods[name] = {
    ready: p.ready,
    restarts: p.restarts,
    oom_killed: p.termination_reasons.includes("OOMKilled"),
    peak_working_set_mib: bytes === null ? null : Math.round((bytes / 1048576) * 1000) / 1000,
    peak_cpu_cores: max("peak_cpu_cores_1m"),
    throttled_period_ratio: max("throttled_period_ratio_15m"),
  };
  for (const [key, s] of recorded) result.resource_measurement.windows[key] = s.recorded_at;
}

result.serial_read_targets = e.assess(result.phases);
Object.assign(result, result.serial_read_targets);
result.viewport_targets = e.assessViewport(result.viewport);
if (result.rejected_measurements.viewport) {
  result.viewport_targets.status = "INCOMPLETE";
  result.viewport_targets.missing_evidence.push("viewport_artifact_binding");
}
if (profile.requireProfile().metadata.uid !== namespace.metadata.uid)
  throw Error("namespace changed during collection");
fs.writeFileSync(archive, JSON.stringify(state, null, 2) + "\n");
fs.writeFileSync(target, e.render(result));
console.log(
  JSON.stringify({
    status: result.status,
    target_failures: result.target_failure_count,
    namespace_uid: namespace.metadata.uid,
    seed_measured: result.seed !== null,
    diagnostics_measured: result.diagnostics_429 !== null,
    viewport_measured: result.viewport !== null,
    viewport_status: result.viewport_targets.status,
    artifact: target,
  }),
);
