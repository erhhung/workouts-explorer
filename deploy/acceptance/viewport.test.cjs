require("./profile-fixture.cjs");
const test = require("node:test");
const assert = require("node:assert/strict");
const e = require("./evidence.cjs");
const profile = require("./profile.cjs");
const observation = require("../../ui/scripts/coverage-viewport-observation.cjs");

function viewportFixture(ns) {
  const binding = {
    ...profile.binding,
    schema_version: e.SCHEMA,
    namespace: e.NAMESPACE,
    namespace_uid: ns.metadata.uid,
  };
  const base = Date.now() - 100000;
  const stamp = (offset) => new Date(base + offset).toISOString();
  const tiles = [];
  for (let y = 8189; y <= 8193; y++) for (let x = 8189; x <= 8195; x++) tiles.push({ z: 14, x, y });
  const latency = (samples) => ({
    samples,
    p50_ms: samples ? 1 : 0,
    p95_ms: samples ? 1 : 0,
    max_ms: samples ? 1 : 0,
  });
  const result = {
    ...binding,
    started_at: stamp(0),
    recorded_at: stamp(90000),
    namespace_created_at: ns.metadata.creationTimestamp,
    status: "PASS",
    scope: "year",
    account_count: 1,
    configured_concurrency: [1, 6, 16, 35],
    batches_per_case: 5,
    targets_ms: { request_p95_provisional: 500, viewport_fetch_p95_proposed: 3000 },
    tile_plan: {
      css_width: 3095,
      css_height: 1973,
      tile_size: 512,
      center_lng: 0.0195,
      center_lat: 0.0195,
      zoom: 14,
      bearing: 0,
      pitch: 0,
      projection: "mercator",
      dpr_multiplies_css: false,
      count: 35,
      tiles,
    },
    content_checks: {
      status: "PASS",
      grid_size: 40,
      named_paths_sql_oracle: 80,
      routed_workouts_sql_oracle: profile.routed,
      per_tile_SQL_cardinality: true,
    },
    failure_totals: {},
    attempts: 1400,
    successes: 1400,
    errors: 0,
    timeouts: 0,
    failed_cases: 0,
    request_failure_totals: {},
    state_start: { data_generation: 1, active_matching_children: 0, matcher_slots: 0 },
    state_end: { data_generation: 1, active_matching_children: 0, matcher_slots: 0 },
    cases: ["route", "coverage"].flatMap((source) =>
      [1, 6, 16, 35].map((concurrency, c) => ({
        ...binding,
        name: `year_${source}_c${concurrency}`,
        scope: "year",
        source,
        concurrency,
        status: "PASS",
        started_at: stamp((source === "route" ? c : c + 4) * 10000),
        recorded_at: stamp((source === "route" ? c : c + 4) * 10000 + 5000),
        attempts: 175,
        count_tiles: 175,
        successes: 175,
        errors: 0,
        timeouts: 0,
        bytes: 1750,
        feature_counts: 5,
        feature_bearing_tiles: 5,
        empty_tiles: 170,
        failure_totals: {},
        statuses: { 200: 175 },
        protocols: { "HTTP/1.1": 175 },
        peak_offered_active_requests: concurrency,
        peak_http1_connections: concurrency,
        overall: latency(175),
        feature_bearing: latency(5),
        empty: latency(170),
        viewport_completion: {
          samples: 5,
          p50_ms: Math.ceil(35 / concurrency),
          p95_ms: Math.ceil(35 / concurrency),
          max_ms: Math.ceil(35 / concurrency),
        },
        batches: Array.from({ length: 5 }, (_, index) => ({
          batch: index + 1,
          status: "PASS",
          failure_class: "",
          started_at: stamp((source === "route" ? c : c + 4) * 10000 + index * 1000),
          finished_at: stamp((source === "route" ? c : c + 4) * 10000 + (index + 1) * 1000),
          viewport_completion_ms: Math.ceil(35 / concurrency),
          peak_offered_active_requests: concurrency,
          peak_http1_connections: concurrency,
          data_generation: 1,
          generation_start: 1,
          generation_end: 1,
          active_children_start: 0,
          active_children_end: 0,
          matcher_slots_start: 0,
          matcher_slots_end: 0,
          samples: tiles.map((coord, i) => ({
            source,
            coord,
            scheduled_at: stamp((source === "route" ? c : c + 4) * 10000 + index * 1000),
            dispatched_at: stamp(
              (source === "route" ? c : c + 4) * 10000 + index * 1000 + Math.floor(i / concurrency),
            ),
            body_complete_at: stamp(
              (source === "route" ? c : c + 4) * 10000 +
                index * 1000 +
                Math.floor(i / concurrency) +
                1,
            ),
            request_ms: 1,
            queue_ms: Math.floor(i / concurrency),
            scheduled_to_body_ms: Math.floor(i / concurrency) + 1,
            bytes: 10,
            decoded_features: i === 0 ? 1 : 0,
            status: 200,
            protocol: "HTTP/1.1",
            error_class: "",
          })),
        })),
      })),
    ),
  };
  result.preflight = structuredClone([result.cases[0].batches[0], result.cases[4].batches[0]]);
  return result;
}

module.exports = { viewportFixture };

if (require.main === module) {
  const ns = { metadata: { uid: "viewport-test-uid", creationTimestamp: "2026-10-04T00:00:00Z" } };
  test("viewport pilot uses its own sample counts and never implies serial or render PASS", () => {
    const raw = viewportFixture(ns),
      saved = structuredClone(raw);
    const assessment = e.assessViewport(raw);
    assert.equal(assessment.status, "PASS");
    assert.equal(assessment.initial_map_target, "NOT_MEASURED");
    assert.equal(e.assess({}).status, "NOT_MEASURED");
    assert.deepEqual(raw, saved);
    assert.equal(e.assessViewport(null).status, "NOT_MEASURED");
  });
  test("missing cases, tiles, batches, metadata and content evidence cannot PASS", () => {
    for (const mutate of [
      (v) => {
        v.cases = [];
      },
      (v) => {
        v.cases.pop();
      },
      (v) => {
        v.cases[0] = v.cases[1];
      },
      (v) => {
        v.tile_plan.tiles = [];
      },
      (v) => {
        v.tile_plan.tiles[0] = v.tile_plan.tiles[1];
      },
      (v) => {
        v.cases[0].batches = [];
      },
      (v) => {
        v.cases[0].batches[0].samples = [];
      },
      (v) => {
        v.cases[0].batches[0].samples.pop();
      },
      (v) => {
        v.account_count = 2;
      },
      (v) => {
        v.tile_plan.css_width = 3431;
      },
      (v) => {
        v.content_checks.status = "FAIL";
      },
      (v) => {
        v.preflight = [];
      },
      (v) => {
        delete v.state_start;
      },
      (v) => {
        delete v.cases[0].schema_version;
      },
      (v) => {
        delete v.cases[0].profile_manifest_sha256;
      },
      (v) => {
        v.cases[0].namespace_uid = "old";
      },
      (v) => {
        v.cases[0].source = "coverage";
      },
      (v) => {
        v.cases[0].batches[0].viewport_completion_ms = null;
      },
      (v) => {
        v.cases[0].overall.p95_ms = null;
      },
    ]) {
      const value = viewportFixture(ns);
      mutate(value);
      const assessment = e.assessViewport(value);
      assert.equal(assessment.status, "INCOMPLETE", JSON.stringify(assessment));
      assert.ok(assessment.missing_evidence.length);
    }
  });
  test("structured Go failures survive even when all timing budgets pass", () => {
    for (const mutate of [
      (v) => {
        v.status = "FAIL";
      },
      (v) => {
        v.failure_totals.UNSTABLE = 1;
      },
      (v) => {
        v.cases[0].status = "FAIL";
      },
      (v) => {
        v.cases[0].batches[0].failure_class = "UNSTABLE";
      },
      (v) => {
        v.cases[0].batches[0].generation_end = 2;
      },
      (v) => {
        v.cases[0].batches[0].samples[0].status = 502;
      },
    ]) {
      const value = viewportFixture(ns);
      mutate(value);
      const assessment = e.assessViewport(value);
      assert.notEqual(assessment.status, "PASS");
      assert.equal(assessment.target_failure_count, 0);
      assert.equal(value.cases[0].overall.p95_ms, 1);
    }
  });
  test("forged request and critical-path budget summaries do not replace raw timings", () => {
    const value = viewportFixture(ns);
    value.cases[0].overall.p95_ms = 501;
    value.cases[0].overall.max_ms = 501;
    value.cases[1].viewport_completion.p95_ms = 3001;
    value.cases[1].viewport_completion.max_ms = 3001;
    const assessment = e.assessViewport(value);
    assert.equal(assessment.status, "INCOMPLETE");
    assert.equal(assessment.target_failure_count, 0);
    assert.ok(assessment.missing_evidence.some((field) => field.endsWith(":latencies")));
    assert.equal(assessment.initial_map_target, "NOT_MEASURED");
  });

  test("raw counters, histograms, peaks, timestamps and nearest-rank summaries cannot be forged", () => {
    for (const [name, mutate] of [
      [
        "integer root errors",
        (v) => {
          v.errors = 99;
        },
      ],
      [
        "HTTP histogram",
        (v) => {
          v.cases[0].statuses = { 502: 175 };
        },
      ],
      [
        "protocol histogram",
        (v) => {
          v.cases[0].protocols = { h2: 175 };
        },
      ],
      [
        "offered peak",
        (v) => {
          v.cases[0].peak_offered_active_requests = 999;
        },
      ],
      [
        "connection peak",
        (v) => {
          v.cases[0].peak_http1_connections = 999;
        },
      ],
      [
        "failure histogram",
        (v) => {
          v.cases[0].failure_totals = { timeout: 99 };
        },
      ],
      [
        "root failure histogram",
        (v) => {
          v.request_failure_totals = { timeout: 99 };
        },
      ],
      [
        "hidden request",
        (v) => {
          for (const b of v.cases[0].batches) for (const s of b.samples) s.request_ms = 9000;
        },
      ],
      [
        "hidden critical path",
        (v) => {
          for (const b of v.cases[0].batches)
            for (const s of b.samples) s.scheduled_to_body_ms = 18000;
        },
      ],
      [
        "old sample date",
        (v) => {
          v.cases[0].batches[0].samples[0].scheduled_at = "1900-01-01T00:00:00Z";
        },
      ],
      [
        "out of batch",
        (v) => {
          v.cases[0].batches[0].samples[0].body_complete_at = v.recorded_at;
        },
      ],
      [
        "wrong queue",
        (v) => {
          v.cases[0].batches[0].samples[0].queue_ms = 99;
        },
      ],
      [
        "wrong request date delta",
        (v) => {
          v.cases[0].batches[0].samples[0].request_ms = 99;
        },
      ],
      [
        "wrong completed date delta",
        (v) => {
          v.cases[0].batches[0].samples[0].scheduled_to_body_ms = 99;
        },
      ],
      [
        "feature percentile",
        (v) => {
          v.cases[0].feature_bearing.p95_ms = 99;
        },
      ],
      [
        "empty percentile",
        (v) => {
          v.cases[0].empty.p50_ms = 99;
        },
      ],
      [
        "batch critical path",
        (v) => {
          v.cases[0].batches[0].viewport_completion_ms = 99;
        },
      ],
    ]) {
      const value = viewportFixture(ns);
      mutate(value);
      const result = e.assessViewport(value, ns);
      assert.equal(result.status, "INCOMPLETE", name);
      assert.ok(result.missing_evidence.length, name);
      if (name === "hidden request" || name === "hidden critical path")
        assert.ok(result.target_failure_count > 0, name);
    }
    const value = viewportFixture(ns);
    for (const b of value.cases[0].batches)
      for (const s of b.samples) {
        s.request_ms = 9000;
        s.scheduled_to_body_ms = s.queue_ms + 9000;
      }
    value.cases[0].overall = { samples: 175, p50_ms: 1, p95_ms: 1, max_ms: 1 };
    value.cases[0].viewport_completion = { samples: 5, p50_ms: 1, p95_ms: 1, max_ms: 1 };
    assert.equal(e.assessViewport(value, ns).target_failure_count, 2);
  });

  test("a consistent raw request-budget failure stays FAIL without becoming incomplete", () => {
    const value = viewportFixture(ns),
      item = value.cases[3];
    for (const batch of item.batches) {
      batch.viewport_completion_ms = 501;
      for (const sample of batch.samples) {
        sample.request_ms = 501;
        sample.scheduled_to_body_ms = 501;
        sample.body_complete_at = new Date(Date.parse(sample.dispatched_at) + 501).toISOString();
      }
    }
    for (const key of ["overall", "feature_bearing", "empty", "viewport_completion"])
      Object.assign(item[key], { p50_ms: 501, p95_ms: 501, max_ms: 501 });
    item.status = value.status = "FAIL";
    value.failed_cases = 1;
    value.failure_totals[item.name] = 1;
    const result = e.assessViewport(value, ns);
    assert.equal(result.status, "FAIL");
    assert.deepEqual(result.missing_evidence, []);
    assert.equal(result.target_failure_count, 1);
    assert.equal(result.request_p95_target, "FAIL");
    assert.equal(result.viewport_fetch_target, "PASS");
  });

  test("JavaScript date truncation tolerates sub-millisecond Go timestamps, not fabricated durations", () => {
    const value = viewportFixture(ns);
    const sample = value.cases[0].batches[0].samples[0];
    sample.dispatched_at = sample.dispatched_at.replace("Z", "123456Z");
    sample.body_complete_at = sample.body_complete_at.replace("Z", "987654Z");
    sample.queue_ms = 0.123456;
    sample.request_ms = 1.864198;
    sample.scheduled_to_body_ms = 1.987654;
    // Change a non-percentile sample; the overall p50/p95/max now includes its max.
    value.cases[0].overall.max_ms = sample.request_ms;
    value.cases[0].feature_bearing.max_ms = sample.request_ms;
    value.cases[0].feature_bearing.p95_ms = sample.request_ms;
    assert.equal(e.assessViewport(value, ns).status, "PASS");
    sample.request_ms += 2;
    assert.equal(e.assessViewport(value, ns).status, "INCOMPLETE");
  });

  test("browser source replacement drops old cache, preserves layer order and observes transient interference", () => {
    const fs = require("node:fs"),
      path = require("node:path"),
      vm = require("node:vm");
    const script = fs.readFileSync(
      path.join(__dirname, "../../ui/scripts/coverage-viewport-acceptance.mjs"),
      "utf8",
    );
    // Execute the actual page.evaluate callback without running its CLI/cluster setup.
    const start = script.indexOf("({ sourceID, template, target, prefix }) => {");
    const end = script.indexOf("\n        },\n        {", start);
    assert.ok(start > 0 && end > start, "source replacement callback must remain identifiable");
    const sourceID = "private-workout-routes",
      template = "https://fixture.invalid/api/map-selections/FRESH/route-tiles/1/{z}/{x}/{y}.pbf";
    const target = {
      width: 3095,
      height: 1973,
      center: [0.0195, 0.0195],
      zoom: 14,
      bearing: 0,
      pitch: 0,
    };
    const layers = [
      { id: "before", type: "background" },
      { id: "route-a", source: sourceID, type: "line" },
      { id: "between", type: "background" },
      { id: "route-b", source: sourceID, type: "circle" },
      { id: "after", type: "background" },
    ];
    const style = {
      sources: {
        [sourceID]: {
          type: "vector",
          tiles: ["old-capability/{z}/{x}/{y}"],
          minzoom: 0,
          maxzoom: 22,
          metadata: { preserve: true },
        },
      },
      layers: structuredClone(layers),
    };
    const calls = [],
      handlers = new Map();
    let oldCache = ["15/16384/16382"],
      zoom = 14;
    const map = {
      getStyle: () => structuredClone(style),
      getCenter: () => ({ lng: 0.0195, lat: 0.0195 }),
      getZoom: () => zoom,
      getBearing: () => 0,
      getPitch: () => 0,
      getCanvas: () => ({ getBoundingClientRect: () => ({ width: 3095, height: 1973 }) }),
      getLayer: (id) => style.layers.find((layer) => layer.id === id),
      removeLayer: (id) => {
        calls.push("removeLayer");
        style.layers = style.layers.filter((layer) => layer.id !== id);
      },
      removeSource: (id) => {
        calls.push("removeSource");
        delete style.sources[id];
        oldCache = [];
      },
      addSource: (id, source) => {
        calls.push("addSource");
        style.sources[id] = structuredClone(source);
      },
      addLayer: (layer, before) => {
        const i = before ? style.layers.findIndex((row) => row.id === before) : style.layers.length;
        style.layers.splice(i, 0, structuredClone(layer));
      },
      on: (event, listener) => {
        calls.push("listen");
        if (!handlers.has(event)) handlers.set(event, []);
        handlers.get(event).push(listener);
      },
      jumpTo: () => {
        throw Error("camera must settle before capture");
      },
      setTiles: () => {
        throw Error("old cache must not be reloaded");
      },
    };
    const probe = {
      getMap: () => map,
      isTerminalTileEvent: vm.runInNewContext(`(${observation.isTerminalTileEvent.toString()})`),
    };
    const callback = vm.runInNewContext(`(${script.slice(start, end)}\n})`, {
      window: { __viewportAcceptance: probe },
      performance: { now: () => 0 },
      structuredClone,
      URL,
      location: { origin: "https://fixture.invalid" },
    });
    callback({ sourceID, template, target, prefix: "/api/map-selections/fresh/route-tiles/" });
    assert.deepEqual(oldCache, []);
    assert.ok(calls.indexOf("removeSource") < calls.indexOf("listen"));
    assert.deepEqual(style.layers, layers);
    assert.deepEqual(style.sources[sourceID], {
      type: "vector",
      tiles: [template],
      minzoom: 0,
      maxzoom: 22,
      metadata: { preserve: true },
    });
    probe.listener({
      type: "sourcedata",
      sourceId: sourceID,
      sourceDataType: "metadata",
      isSourceLoaded: true,
    });
    assert.equal(probe.sourceLoadedMs, null);
    probe.listener({
      type: "sourcedata",
      sourceId: "other-source",
      coord: { canonical: { z: 14, x: 8192, y: 8191 } },
      isSourceLoaded: true,
    });
    assert.equal(probe.sourceLoadedMs, null);
    probe.listener({
      type: "sourcedata",
      sourceId: sourceID,
      coord: { canonical: { z: 14, x: 8192, y: 8191 } },
      isSourceLoaded: true,
    });
    assert.equal(probe.sourceLoadedMs, 0, "terminal tile events need no sourceDataType");
    zoom = 15;
    for (const listener of handlers.get("move")) listener();
    zoom = 14;
    for (const listener of handlers.get("render")) listener();
    assert.equal(
      probe.frameChanges.length,
      1,
      "returning to the final camera must not hide a transient change",
    );
    probe.listener({
      type: "sourcedata",
      sourceId: sourceID,
      source: { tiles: ["old-capability"] },
    });
    probe.errorListener({
      sourceId: sourceID,
      error: {
        message: "old request",
        url: "https://fixture.invalid/api/map-selections/OLD/route-tiles/1/15/1/1.pbf",
      },
    });
    probe.errorListener({ error: { message: "no URL available" } });
    assert.equal(probe.interference.length, 3);
    assert.equal(probe.events.filter((event) => event.type === "error").length, 0);
    probe.errorListener({
      error: { message: "fresh request", url: template.replace("{z}/{x}/{y}", "14/8192/8191") },
    });
    assert.equal(probe.events.filter((event) => event.type === "error").length, 1);
  });

  test("terminal source completion requires the desired source and a loaded canonical tile, not metadata", () => {
    const sourceID = "private-workout-routes";
    const terminal = {
      type: "sourcedata",
      sourceId: sourceID,
      coord: { canonical: { z: 14, x: 8192, y: 8191 } },
      isSourceLoaded: true,
    };
    assert.equal(observation.isTerminalTileEvent(terminal, sourceID), true);
    for (const event of [
      { ...terminal, coord: undefined, sourceDataType: "metadata" },
      { ...terminal, coord: undefined, sourceDataType: "content" },
      { ...terminal, sourceId: "private-workout-coverage" },
      { ...terminal, type: "sourcedataloading" },
      { ...terminal, isSourceLoaded: false },
      null,
    ])
      assert.equal(observation.isTerminalTileEvent(event, sourceID), false);
  });

  test("successful browser HTTP is distinct from a comparable fixed-frame tile capture", () => {
    const classify = (item, pending, target) =>
      Object.assign(item, observation.assessBrowserCapture(item, pending.size, target));
    const target = { width: 3095, height: 1973, center: [0.0195, 0.0195], zoom: 14 };
    const rows = viewportFixture(ns).tile_plan.tiles.map((tile) => ({
      tile_id: `${tile.z}/${tile.x}/${tile.y}`,
      status: 200,
      bytes: 0,
      started_ms: 1,
      finished_ms: 2,
      duration_ms: 1,
    }));
    const base = {
      requests: rows,
      unique_tile_ids: rows.map((row) => row.tile_id),
      canvas_css: { width: 3095, height: 1973 },
      camera: { center: target.center, zoom: 14, bearing: 0, pitch: 0 },
      errors: [],
      interference: [],
      source_events: [],
      frame_changes: [],
      source_loaded: true,
    };
    for (const mutate of [
      (item) => {
        item.requests[0].tile_id = item.unique_tile_ids[0] = "15/16384/16382";
      },
      (item) => {
        item.frame_changes.push({ zoom: 15 });
      },
      (item) => {
        item.interference.push({ kind: "other_capability_request" });
      },
      (item) => {
        item.requests.pop();
      },
      (item) => {
        item.requests[0].tile_id = "13/4096/4095";
      },
      (item) => {
        item.requests[0].finished_ms = undefined;
      },
    ]) {
      const item = structuredClone(base);
      mutate(item);
      classify(item, { size: 0 }, target);
      assert.equal(item.capture_success, true);
      assert.equal(item.comparison_valid, false);
      assert.equal(item.status, "NOT_COMPARABLE");
    }
    const valid = structuredClone(base);
    classify(valid, { size: 0 }, target);
    assert.equal(valid.comparison_valid, true);
    assert.equal(valid.status, "RECORDED_NO_ACCEPTANCE_THRESHOLD");
    const failed = structuredClone(base);
    failed.requests[0].status = 502;
    classify(failed, { size: 0 }, target);
    assert.equal(failed.capture_success, false);
    assert.equal(failed.comparison_valid, true);
    assert.deepEqual(failed.comparison_reasons, []);
    assert.equal(failed.status, "FAIL");
  });

  test("viewport-only collection rebuilds current evidence without importing old serial reports", (t) => {
    const fs = require("node:fs"),
      childProcess = require("node:child_process");
    const env = { ...process.env },
      argv = [...process.argv];
    t.after(() => {
      process.env = env;
      process.argv = argv;
      delete require.cache[require.resolve("./collect.cjs")];
    });
    process.env.COVERAGE_ACCEPTANCE_DISPOSABLE = "true";
    delete process.env.COVERAGE_ACCEPTANCE_COLLECT_OUTPUT;
    delete process.env.COVERAGE_ACCEPTANCE_ARCHIVE;
    process.argv[2] = "viewport";
    const raw = viewportFixture(ns),
      writes = new Map();
    t.mock.method(profile, "requireProfile", () => ns);
    t.mock.method(e, "loadArchive", (file) => {
      assert.equal(file, profile.artifact(`acceptance-${ns.metadata.uid}-raw.json`));
      return e.fresh(ns);
    });
    t.mock.method(e, "loadMeasurement", (file) =>
      file === profile.artifact("viewport.json") ? raw : null,
    );
    t.mock.method(childProcess, "execFileSync", (command, args) => {
      assert.equal(command, "kubectl");
      return JSON.stringify(args.includes("pods") ? { items: [] } : {});
    });
    t.mock.method(fs, "writeFileSync", (file, data) => writes.set(file, JSON.parse(data)));
    t.mock.method(console, "log", () => {});
    require("./collect.cjs");
    const result = writes.get(profile.results("acceptance"));
    assert.equal(result.status, "NOT_MEASURED");
    assert.equal(result.serial_read_targets.status, "NOT_MEASURED");
    assert.equal(result.viewport_targets.status, "PASS");
    assert.equal(result.initial_map_target, "NOT_MEASURED");
    assert.deepEqual(result.phases, {});
    assert.deepEqual(result.viewport, raw);
    assert.deepEqual(
      [...writes.keys()],
      [profile.artifact(`acceptance-${ns.metadata.uid}-raw.json`), profile.results("acceptance")],
    );
  });
}
