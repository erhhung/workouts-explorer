const fs = require("node:fs");
const profile = require("./profile.cjs");

const SCHEMA = 3;
const NAMESPACE = "workouts-acceptance";
const endpoints = ["month", "year"].flatMap((p) =>
  [
    "summary",
    "workouts_page1",
    "workouts_page2",
    "selection",
    "paths_page1",
    "paths_page2",
    "path_detail",
    "focus",
    "route_tile",
    "coverage_tile",
  ].map((e) => `${p}_${e}`),
);

function assertBinding(value, namespace) {
  profile.assertProfile(value);
  if (
    value.schema_version !== SCHEMA ||
    value.namespace !== NAMESPACE ||
    value.namespace_uid !== namespace.metadata.uid
  ) {
    throw Error("unbound, wrong-schema, or previous-namespace artifact");
  }
  if (value.provenance?.namespace_uid && value.provenance.namespace_uid !== namespace.metadata.uid)
    throw Error("provenance belongs to another namespace");
  for (const field of ["started_at", "recorded_at", "created_start", "created_end"]) {
    if (value[field]) {
      if (!Number.isFinite(Date.parse(value[field]))) throw Error("invalid artifact timestamp");
      if (Date.parse(value[field]) < Date.parse(namespace.metadata.creationTimestamp))
        throw Error("artifact predates namespace");
    }
  }
  return value;
}

function fresh(namespace) {
  return {
    ...profile.binding,
    schema_version: SCHEMA,
    namespace: NAMESPACE,
    namespace_uid: namespace.metadata.uid,
    namespace_created_at: namespace.metadata.creationTimestamp,
    evidence: {
      ...profile.binding,
      schema_version: SCHEMA,
      namespace: NAMESPACE,
      namespace_uid: namespace.metadata.uid,
      scope: "Disposable public synthetic fixtures, not a full production archive benchmark.",
      generation_parameters: structuredClone(profile.manifest),
      provenance: {
        ...profile.binding,
        kind: "current_namespace",
        namespace_uid: namespace.metadata.uid,
        namespace_created_at: namespace.metadata.creationTimestamp,
      },
      seed: null,
      phases: {},
      matcher: null,
      matcher_smoke: null,
      general_worker: null,
      diagnostics_429: null,
      viewport: null,
      viewport_browser: null,
      targets_ms: { api_p95: 500, summary: 2000, initial_map_render: 3000 },
      limitations: [],
    },
    snapshots: {},
    measurements: {},
  };
}

function loadArchive(file, namespace) {
  if (!fs.existsSync(file)) return fresh(namespace);
  const value = assertBinding(JSON.parse(fs.readFileSync(file, "utf8")), namespace);
  if (!value.evidence || !value.snapshots || !value.measurements)
    throw Error("invalid archive shape");
  assertBinding(value.evidence, namespace);
  for (const phase of Object.values(value.evidence.phases || {})) {
    assertBinding(phase, namespace);
  }
  for (const snapshot of Object.values(value.snapshots)) {
    assertBinding(snapshot, namespace);
  }
  for (const measurement of Object.values(value.measurements.queue_windows || {}))
    assertBinding(measurement, namespace);
  for (const key of ["seed", "general_worker", "diagnostics_429", "matcher_smoke", "viewport"])
    if (value.evidence[key]) assertBinding(value.evidence[key], namespace);
  if (value.evidence.viewport) assertMeasurementBinding(value.evidence.viewport, namespace);
  if (value.evidence.viewport_browser)
    assertMeasurementBinding(value.evidence.viewport_browser, namespace);
  if (value.evidence.current_observation)
    assertBinding(value.evidence.current_observation, namespace);
  return value;
}

function loadMeasurement(file, namespace) {
  if (!fs.existsSync(file)) return null;
  return assertMeasurementBinding(JSON.parse(fs.readFileSync(file, "utf8")), namespace);
}

function assertMeasurementBinding(value, namespace) {
  assertBinding(value, namespace);
  const visit = (item) => {
    if (!item || typeof item !== "object") return;
    // Generation parameters have their own manifest schema, not an evidence binding.
    for (const [key, child] of Object.entries(item)) {
      if (key === "generation_parameters") continue;
      if (child && typeof child === "object") {
        if (key === "provenance") {
          if (Object.hasOwn(child, "profile")) profile.assertProfile(child);
          if (child.namespace_uid !== namespace.metadata.uid)
            throw Error("provenance belongs to another namespace");
        } else if (
          [
            "schema_version",
            "profile",
            "profile_version",
            "profile_manifest_sha256",
            "namespace",
          ].some((field) => Object.hasOwn(child, field))
        )
          assertBinding(child, namespace);
        else if (child.namespace_uid && child.namespace_uid !== namespace.metadata.uid)
          throw Error("nested record belongs to another namespace");
        visit(child);
      }
    }
  };
  visit(value);
  if (value.tile_plan || value.configured_concurrency || value.frame)
    for (const item of value.cases || []) assertBinding(item, namespace);
  return value;
}

function assess(phases) {
  let failures = 0,
    missing = 0;
  for (const p of Object.values(phases)) {
    const absent = endpoints.filter((name) => !p.timings?.[name]);
    p.missing_endpoints = absent;
    p.p95_target_failures = 0;
    for (const [name, t] of Object.entries(p.timings || {})) {
      const budget = Number.isFinite(t.p95_ms) && t.p95_ms <= 500;
      const functional =
        t.samples === 30 &&
        (t.statuses?.[200] || 0) + (t.statuses?.[201] || 0) === 30 &&
        Object.entries(t.statuses || {}).every(([s]) => ["200", "201", "404"].includes(s));
      t.target = budget && functional ? "PASS" : "FAIL";
      if (!budget || !functional) {
        p.p95_target_failures++;
        failures++;
      }
      if (name.endsWith("_tile") && !(p.content_checks?.tile_min_bytes?.[name] > 0))
        absent.push(name + ":empty_or_unverified_tile");
    }
    if (p.content_checks?.status !== "PASS") absent.push("content_checks");
    p.status = absent.length ? "INCOMPLETE" : p.p95_target_failures ? "FAIL" : "PASS";
    p.missing_endpoints = absent;
    missing += absent.length;
  }
  const values = Object.values(phases);
  return {
    status: !values.length
      ? "NOT_MEASURED"
      : values.some((p) => p.status === "INCOMPLETE")
        ? "INCOMPLETE"
        : failures
          ? "FAIL"
          : "PASS",
    target_failure_count: failures,
    missing_endpoint_count: missing,
    summary_target: !values.length
      ? "NOT_MEASURED"
      : values.every((p) =>
            ["month_summary", "year_summary"].every(
              (k) => Number.isFinite(p.timings?.[k]?.max_ms) && p.timings[k].max_ms <= 2000,
            ),
          )
        ? "PASS"
        : "FAIL",
    initial_map_target: "NOT_MEASURED",
  };
}

function assessViewport(value, expectedNamespace) {
  const result = {
    status: "NOT_MEASURED",
    request_p95_target: "NOT_MEASURED",
    viewport_fetch_target: "NOT_MEASURED",
    initial_map_target: "NOT_MEASURED",
    target_failure_count: 0,
    missing_evidence: [],
    cases: {},
    budgets_ms: { request_p95_provisional: 500, viewport_fetch_p95_proposed: 3000 },
    budget_scope:
      "Provisional request and proposed viewport-fetch budgets, not a product render SLO.",
  };
  if (value === null || value === undefined) return result;
  const missing = result.missing_evidence;
  const require = (condition, field) => {
    if (!condition) missing.push(field);
  };
  const nonnegative = (n) => Number.isFinite(n) && n >= 0;
  const count = (n) => Number.isInteger(n) && n >= 0;
  const namespace = expectedNamespace || {
    metadata: { uid: value.namespace_uid, creationTimestamp: value.namespace_created_at },
  };
  const bound = (item) => {
    try {
      assertBinding(item, namespace);
      return Boolean(item.namespace_uid);
    } catch {
      return false;
    }
  };
  const timestamp = (v) =>
    typeof v === "string" &&
    /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(v) &&
    Number.isFinite(Date.parse(v));
  const close = (a, b, tolerance = 0.001) =>
    Number.isFinite(a) && Number.isFinite(b) && Math.abs(a - b) <= tolerance;
  const histogram = (actual, expected) =>
    actual &&
    typeof actual === "object" &&
    !Array.isArray(actual) &&
    Object.keys(actual).length === Object.keys(expected).length &&
    Object.entries(expected).every(([key, n]) => actual[key] === n);
  const increment = (map, key, n = 1) => {
    map[key] = (map[key] || 0) + n;
  };
  const percentiles = (values) => {
    const sorted = values.filter(nonnegative).sort((a, b) => a - b);
    return {
      samples: sorted.length,
      p50_ms: sorted.length ? sorted[Math.ceil(sorted.length * 0.5) - 1] : 0,
      p95_ms: sorted.length ? sorted[Math.ceil(sorted.length * 0.95) - 1] : 0,
      max_ms: sorted.length ? sorted.at(-1) : 0,
    };
  };
  const latency = (actual, values) => {
    const derived = percentiles(values);
    return (
      actual?.samples === values.length &&
      derived.samples === values.length &&
      ["p50_ms", "p95_ms", "max_ms"].every((key) => close(actual[key], derived[key]))
    );
  };
  const windowStart = Math.max(
    Date.parse(value.started_at),
    Date.parse(namespace.metadata.creationTimestamp) || -Infinity,
  );
  const windowEnd = Date.parse(value.recorded_at);
  require(bound(value), "binding");
  require(timestamp(value.started_at) &&
    timestamp(value.recorded_at) &&
    Date.parse(value.recorded_at) >= Date.parse(value.started_at), "capture_window");
  require(value.scope === "year" && value.account_count === 1, "single_account_year_scope");
  require(JSON.stringify(value.configured_concurrency) === "[1,6,16,35]", "configured_concurrency");
  const batches = value.batches_per_case;
  require(Number.isInteger(batches) && batches >= 1 && batches <= 10, "batches_per_case");
  require(value.targets_ms?.request_p95_provisional === 500 &&
    value.targets_ms?.viewport_fetch_p95_proposed === 3000, "targets_ms");
  const plan = value.tile_plan;
  require(plan?.css_width === 3095 &&
    plan.css_height === 1973 &&
    plan.tile_size === 512 &&
    plan.center_lng === 0.0195 &&
    plan.center_lat === 0.0195 &&
    plan.zoom === 14 &&
    plan.bearing === 0 &&
    plan.pitch === 0 &&
    plan.projection === "mercator" &&
    plan.dpr_multiplies_css === false, "tile_plan.frame");
  const tileID = (t) => t && `${t.z}/${t.x}/${t.y}`;
  const expectedTiles = new Set();
  for (let y = 8189; y <= 8193; y++)
    for (let x = 8189; x <= 8195; x++) expectedTiles.add(`14/${x}/${y}`);
  const completeTiles = (items, coord) =>
    Array.isArray(items) &&
    items.length === 35 &&
    new Set(items.map(coord)).size === 35 &&
    items.every((item) => expectedTiles.has(coord(item)));
  require(plan?.count === 35 && completeTiles(plan?.tiles, tileID), "tile_plan.tiles");
  require(value.content_checks?.status === "PASS" &&
    value.content_checks.grid_size === 40 &&
    value.content_checks.named_paths_sql_oracle === 80 &&
    value.content_checks.routed_workouts_sql_oracle === profile.routed &&
    value.content_checks.per_tile_SQL_cardinality === true, "content_checks");
  require(Array.isArray(value.preflight) &&
    value.preflight.length === 2 &&
    ["route", "coverage"].every(
      (source) =>
        value.preflight.filter(
          (batch) =>
            completeTiles(batch?.samples, (sample) => tileID(sample?.coord)) &&
            batch.samples.every((sample) => sample.source === source),
        ).length === 1,
    ), "preflight");
  require(value.state_start?.data_generation > 0 &&
    value.state_end?.data_generation === value.state_start.data_generation &&
    [
      value.state_start?.active_matching_children,
      value.state_end?.active_matching_children,
      value.state_start?.matcher_slots,
      value.state_end?.matcher_slots,
    ].every((n) => n === 0), "immutable_state");
  const expectedCases = ["route", "coverage"].flatMap((source) =>
    [1, 6, 16, 35].map((concurrency) => ({
      source,
      concurrency,
      name: `year_${source}_c${concurrency}`,
    })),
  );
  require(Array.isArray(value.cases) && value.cases.length === 8, "cases.count");
  let requestFailures = 0,
    fetchFailures = 0,
    functionalFailures =
      value.status !== "PASS" ||
      Object.values(value.failure_totals || {}).some((n) => n > 0) ||
      value.errors !== 0 ||
      (Array.isArray(value.preflight) &&
        value.preflight.some(
          (batch) =>
            !batch ||
            batch.status !== "PASS" ||
            batch.failure_class ||
            !Array.isArray(batch.samples) ||
            !batch.samples.some((sample) => sample?.decoded_features > 0) ||
            batch.samples.some(
              (sample) =>
                sample?.status !== 200 ||
                sample.protocol !== "HTTP/1.1" ||
                sample.error_class !== "",
            ),
        ));
  const totals = { attempts: 0, successes: 0, errors: 0, timeouts: 0, failed_cases: 0 },
    rootFailures = Object.create(null);
  for (const expected of expectedCases) {
    const matches = Array.isArray(value.cases)
      ? value.cases.filter((item) => item?.name === expected.name)
      : [];
    if (matches.length !== 1) {
      missing.push(`${expected.name}:missing_or_duplicate_case`);
      continue;
    }
    const item = matches[0],
      prefix = `${expected.name}:`,
      initialMissing = missing.length;
    require(bound(item), prefix + "binding");
    require(timestamp(item.started_at) &&
      timestamp(item.recorded_at) &&
      Date.parse(item.started_at) >= windowStart &&
      Date.parse(item.recorded_at) <= windowEnd &&
      Date.parse(item.recorded_at) >= Date.parse(item.started_at), prefix + "capture_window");
    require(item.scope === "year" &&
      item.source === expected.source &&
      item.concurrency === expected.concurrency, prefix + "scope_source_concurrency");
    require(Array.isArray(item.batches) &&
      item.batches.length === batches &&
      item.batches.length > 0, prefix + "batches");
    let sampleCount = 0,
      successes = 0,
      bytes = 0,
      features = 0,
      featureTiles = 0,
      emptyTiles = 0,
      errors = 0,
      timeouts = 0,
      peakActive = 0,
      peakConnected = 0,
      sampleFailures = false;
    const statuses = Object.create(null),
      protocols = Object.create(null),
      failures = Object.create(null),
      overall = [],
      featureLatencies = [],
      emptyLatencies = [],
      completion = [];
    const seenBatches = new Set();
    for (const batch of Array.isArray(item.batches) ? item.batches : []) {
      if (!batch || typeof batch !== "object") {
        missing.push(prefix + "batch_fields");
        continue;
      }
      require(Number.isInteger(batch.batch) &&
        batch.batch >= 1 &&
        batch.batch <= batches &&
        !seenBatches.has(batch.batch), prefix + "batch_index");
      seenBatches.add(batch.batch);
      require(completeTiles(batch.samples, (sample) => tileID(sample?.coord)), prefix +
        `batch${batch.batch}.tiles`);
      require(nonnegative(batch.viewport_completion_ms), prefix + `batch${batch.batch}.completion`);
      require(timestamp(batch.started_at) &&
        timestamp(batch.finished_at) &&
        Date.parse(batch.started_at) >= Date.parse(item.started_at) &&
        Date.parse(batch.finished_at) <= Date.parse(item.recorded_at) &&
        Date.parse(batch.finished_at) >= Date.parse(batch.started_at), prefix +
        `batch${batch.batch}.window`);
      peakActive = Math.max(peakActive, batch.peak_offered_active_requests);
      peakConnected = Math.max(peakConnected, batch.peak_http1_connections);
      if (batch.failure_class) increment(failures, batch.failure_class);
      require(count(batch.peak_offered_active_requests) &&
        batch.peak_offered_active_requests > 0 &&
        batch.peak_offered_active_requests <= expected.concurrency &&
        count(batch.peak_http1_connections) &&
        batch.peak_http1_connections > 0 &&
        batch.peak_http1_connections <= batch.peak_offered_active_requests, prefix +
        `batch${batch.batch}.concurrency`);
      if (
        batch.status !== "PASS" ||
        batch.failure_class ||
        !(batch.data_generation > 0) ||
        batch.generation_start !== batch.data_generation ||
        batch.generation_end !== batch.data_generation ||
        batch.active_children_start !== 0 ||
        batch.active_children_end !== 0 ||
        batch.matcher_slots_start !== 0 ||
        batch.matcher_slots_end !== 0
      )
        sampleFailures = true;
      const batchDurations = [],
        bodyTimes = [];
      for (const sample of Array.isArray(batch.samples) ? batch.samples : []) {
        sampleCount++;
        if (!sample || typeof sample !== "object") {
          missing.push(prefix + "sample_fields");
          sampleFailures = true;
          continue;
        }
        require(sample.source === expected.source &&
          nonnegative(sample.request_ms) &&
          nonnegative(sample.queue_ms) &&
          nonnegative(sample.scheduled_to_body_ms) &&
          count(sample.bytes) &&
          count(sample.decoded_features) &&
          count(sample.status) &&
          sample.status <= 599 &&
          typeof sample.protocol === "string" &&
          typeof sample.error_class === "string", prefix + `batch${batch.batch}.sample_fields`);
        const scheduled = Date.parse(sample.scheduled_at),
          dispatched = Date.parse(sample.dispatched_at),
          completed = Date.parse(sample.body_complete_at);
        require([sample.scheduled_at, sample.dispatched_at, sample.body_complete_at].every(
          timestamp,
        ) &&
          scheduled >= windowStart &&
          completed <= windowEnd &&
          close(scheduled, Date.parse(batch.started_at), 1) &&
          dispatched >= scheduled &&
          completed >= dispatched &&
          completed <= Date.parse(batch.finished_at) &&
          close(sample.queue_ms, dispatched - scheduled, 1) &&
          close(sample.request_ms, completed - dispatched, 1) &&
          close(sample.scheduled_to_body_ms, completed - scheduled, 1) &&
          close(sample.queue_ms + sample.request_ms, sample.scheduled_to_body_ms), prefix +
          `batch${batch.batch}.sample_timing`);
        increment(statuses, sample.status);
        increment(protocols, sample.protocol);
        overall.push(sample.request_ms);
        batchDurations.push(sample.scheduled_to_body_ms);
        bodyTimes.push(completed);
        const ok = sample.error_class === "";
        if (!ok) {
          errors++;
          increment(failures, sample.error_class);
          if (sample.error_class === "timeout") timeouts++;
        }
        if (!ok || sample.status !== 200 || sample.protocol !== "HTTP/1.1") sampleFailures = true;
        bytes += sample.bytes || 0;
        if (ok) {
          successes++;
          features += sample.decoded_features || 0;
          if (sample.decoded_features > 0) {
            featureTiles++;
            featureLatencies.push(sample.request_ms);
          } else {
            emptyTiles++;
            emptyLatencies.push(sample.request_ms);
          }
        }
      }
      if (batchDurations.length) {
        const criticalPath = Math.max(...batchDurations);
        completion.push(criticalPath);
        require(close(batch.viewport_completion_ms, criticalPath) &&
          close(
            batch.viewport_completion_ms,
            Math.max(...bodyTimes) - Date.parse(batch.started_at),
            1,
          ), prefix + `batch${batch.batch}.critical_path`);
      }
    }
    const attempts = 35 * batches;
    require(item.attempts === attempts &&
      item.count_tiles === attempts &&
      sampleCount === attempts, prefix + "attempts");
    if (sampleCount !== attempts) increment(failures, "missing_attempts");
    require(item.successes === successes &&
      item.errors === errors &&
      item.timeouts === timeouts &&
      item.bytes === bytes &&
      item.feature_counts === features &&
      item.feature_bearing_tiles === featureTiles &&
      item.empty_tiles === emptyTiles, prefix + "aggregate_counts");
    require(histogram(item.statuses, statuses) &&
      histogram(item.protocols, protocols) &&
      histogram(item.failure_totals, failures), prefix + "histograms");
    require(item.peak_offered_active_requests === peakActive &&
      item.peak_http1_connections === peakConnected &&
      peakActive <= expected.concurrency &&
      peakConnected <= peakActive, prefix + "aggregate_peaks");
    require(latency(item.overall, overall) &&
      latency(item.viewport_completion, completion) &&
      latency(item.feature_bearing, featureLatencies) &&
      latency(item.empty, emptyLatencies), prefix + "latencies");
    require(featureTiles > 0 && features > 0 && bytes > 0, prefix + "feature_content");
    const requestPass =
      percentiles(overall).samples === sampleCount &&
      percentiles(overall).p95_ms <= 500 &&
      sampleCount > 0;
    const fetchPass =
      percentiles(completion).samples === batches &&
      percentiles(completion).p95_ms <= 3000 &&
      sampleCount > 0;
    if (!requestPass) requestFailures++;
    if (!fetchPass) fetchFailures++;
    const functional =
      item.status === "PASS" &&
      !sampleFailures &&
      item.errors === 0 &&
      item.timeouts === 0 &&
      !Object.values(item.failure_totals || {}).some((n) => n > 0);
    if (!functional) functionalFailures = true;
    totals.attempts += sampleCount;
    totals.successes += successes;
    totals.errors += errors;
    totals.timeouts += timeouts;
    if (item.status !== "PASS") totals.failed_cases++;
    for (const [key, n] of Object.entries(failures)) increment(rootFailures, key, n);
    result.cases[expected.name] = {
      status:
        missing.length > initialMissing
          ? "INCOMPLETE"
          : functional && requestPass && fetchPass
            ? "PASS"
            : "FAIL",
      request_p95_target: requestPass ? "PASS" : "FAIL",
      viewport_fetch_target: fetchPass ? "PASS" : "FAIL",
    };
  }
  require(Object.entries(totals).every(
    ([key, n]) => count(value[key]) && value[key] === n,
  ), "root_aggregate_counts");
  require(histogram(value.request_failure_totals, rootFailures), "root_request_failure_totals");
  result.target_failure_count = requestFailures + fetchFailures;
  result.status = missing.length
    ? "INCOMPLETE"
    : functionalFailures || result.target_failure_count
      ? "FAIL"
      : "PASS";
  result.request_p95_target = missing.length ? "INCOMPLETE" : requestFailures ? "FAIL" : "PASS";
  result.viewport_fetch_target = missing.length ? "INCOMPLETE" : fetchFailures ? "FAIL" : "PASS";
  return result;
}

function render(value) {
  const lines = ["{"];
  const entries = Object.entries(value);
  entries.forEach(([key, item], i) => {
    const comma = i + 1 < entries.length ? "," : "";
    if (key !== "phases") {
      lines.push(`  ${JSON.stringify(key)}: ${JSON.stringify(item)}${comma}`);
      return;
    }
    lines.push('  "phases": {');
    const phases = Object.entries(item);
    phases.forEach(([name, { timings, ...metadata }], j) => {
      lines.push(`    ${JSON.stringify(name)}: {`);
      Object.entries(metadata).forEach(([k, v]) =>
        lines.push(`      ${JSON.stringify(k)}: ${JSON.stringify(v)},`),
      );
      lines.push('      "timings": {');
      const rows = Object.entries(timings || {});
      rows.forEach(([k, v], n) =>
        lines.push(
          `        ${JSON.stringify(k)}: ${JSON.stringify(v)}${n + 1 < rows.length ? "," : ""}`,
        ),
      );
      lines.push("      }", `    }${j + 1 < phases.length ? "," : ""}`);
    });
    lines.push(`  }${comma}`);
  });
  lines.push("}");
  return lines.join("\n") + "\n";
}

module.exports = {
  SCHEMA,
  NAMESPACE,
  endpoints,
  assertBinding,
  fresh,
  loadArchive,
  loadMeasurement,
  assertMeasurementBinding,
  assess,
  assessViewport,
  render,
};
