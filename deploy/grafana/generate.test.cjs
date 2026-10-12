const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { test } = require("node:test");
const { artifacts, operational, historical } = require("./generate.cjs");

// Independent audited family inventory: 35 normal Go instruments, plus native/self.
const goFamilies = [
  "http_server_request_duration_seconds",
  "http_server_request_body_size_bytes",
  "http_server_response_body_size_bytes",
  "workouts_api_tile_duration_seconds",
  "coverage_queue_age_seconds",
  "coverage_matcher_duration_seconds",
  "coverage_route_duration_seconds",
  "coverage_admission_wait_seconds",
  "coverage_route_outcomes_total",
  "coverage_control_calls_total",
  "coverage_retry_attempts_total",
  "coverage_stale_rejections_total",
  "db_client_operation_duration_seconds",
  "db_client_operation_errors_total",
  "pgxpool_acquires_total",
  "pgxpool_acquire_duration_nanoseconds_total",
  "pgxpool_acquired_connections",
  "pgxpool_canceled_acquires_total",
  "pgxpool_constructing_connections",
  "pgxpool_empty_acquire_total",
  "pgxpool_idle_connections",
  "pgxpool_max_connections",
  "pgxpool_max_idle_destroys_total",
  "pgxpool_max_lifetime_destroys_total",
  "pgxpool_new_connections_total",
  "pgxpool_total_connections",
  "pgxpool_empty_acquire_wait_time_nanoseconds_total",
  "go_memory_used_bytes",
  "go_memory_limit_bytes",
  "go_memory_allocated_bytes_total",
  "go_memory_allocations_total",
  "go_memory_gc_goal_bytes",
  "go_goroutine_count",
  "go_processor_limit",
  "go_config_gogc_percent",
];
const histogramFamilies = new Set([
  "http_server_request_duration_seconds",
  "http_server_request_body_size_bytes",
  "http_server_response_body_size_bytes",
  "workouts_api_tile_duration_seconds",
  "coverage_queue_age_seconds",
  "coverage_matcher_duration_seconds",
  "coverage_route_duration_seconds",
  "coverage_admission_wait_seconds",
  "db_client_operation_duration_seconds",
  "martin_http_requests_duration_seconds",
]);
const nativeFamilies = [
  "nginx_connections_accepted_total",
  "nginx_connections_handled_total",
  "nginx_connections_current",
  "nginx_requests_total",
  "martin_http_requests_total",
  "martin_http_requests_duration_seconds",
];
const selfFamilies = [
  "otelcol_process_cpu_seconds_total",
  "otelcol_process_memory_rss",
  "otelcol_process_runtime_heap_alloc_bytes",
  "otelcol_process_runtime_alloc_bytes_total",
  "otelcol_receiver_accepted_metric_points_total",
  "otelcol_receiver_refused_metric_points_total",
  "otelcol_receiver_failed_metric_points_total",
  "otelcol_exporter_sent_metric_points_total",
  "otelcol_exporter_queue_size",
  "otelcol_exporter_queue_capacity",
  // Explicitly unobserved candidates in the audited documentation, not live zeros.
  "otelcol_exporter_send_failed_metric_points_total",
  "otelcol_exporter_enqueue_failed_metric_points_total",
];
const allowlist = new Set(
  [...goFamilies, ...nativeFamilies, ...selfFamilies].flatMap((family) =>
    histogramFamilies.has(family)
      ? ["bucket", "sum", "count"].map((suffix) => `${family}_${suffix}`)
      : [family],
  ),
);
const datasource = { type: "prometheus", uid: "prometheus" };
const dashboards = [operational(), historical()];
const allPanels = (dashboard) =>
  dashboard.panels.flatMap((panel) => [panel, ...allPanels({ panels: panel.panels || [] })]);
const queryPanels = (dashboard) => allPanels(dashboard).filter((panel) => panel.targets);
const note = (dashboard, title) =>
  dashboard.panels.at(-1).panels.find((panel) => panel.title === title);
const expressions = (dashboard) =>
  queryPanels(dashboard).flatMap((panel) => panel.targets.map((target) => target.expr));
function selectors(expr) {
  // Quoted labels may contain braces (route templates and Grafana variables).
  return [...expr.matchAll(/([a-zA-Z_:][a-zA-Z0-9_:]*)\{((?:[^"{}]|"(?:\\.|[^"\\])*")*)\}/g)];
}

test("exactly two dashboards and four deterministic generated artifacts", () => {
  assert.equal(goFamilies.length, 35);
  const files = artifacts();
  assert.deepEqual(Object.keys(files).sort(), [
    "../../helm/dashboards/historical.json",
    "../../helm/dashboards/operational.json",
    "historical.json",
    "operational.json",
  ]);
  assert.deepEqual(fs.readdirSync(__dirname).sort(), [
    "README.md",
    "generate.cjs",
    "generate.test.cjs",
    "historical.json",
    "operational.json",
  ]);
  assert.deepEqual(artifacts(), files);
  assert.deepEqual(
    dashboards.map((dashboard) => dashboard.title),
    ["Workouts Explorer / Operational", "Workouts Explorer / Historical"],
  );
  assert.deepEqual(
    dashboards.map((dashboard) => dashboard.uid),
    ["workouts-explorer-operational", "workouts-explorer-historical"],
  );
  for (const dashboard of dashboards) {
    assert.ok(dashboard.uid.length <= 40);
    assert.equal(dashboard.id, null);
    assert.equal(dashboard.schemaVersion, 41);
    assert.equal(dashboard.timezone, "America/Los_Angeles");
    assert.match(dashboard.description, /AUTOGENERATED/);
    assert.equal(dashboard.editable, true);
    assert.doesNotMatch(JSON.stringify(dashboard), /allowUiUpdates/);
    assert.deepEqual(dashboard.annotations.list, []);
  }
});

test("Helm mirrors are byte-identical dashboard JSON; standalone ConfigMaps are no longer generated", () => {
  const files = artifacts();
  for (const kind of ["operational", "historical"]) {
    const json = files[`../../helm/dashboards/${kind}.json`];
    assert.equal(json, files[`${kind}.json`]);
    assert.deepEqual(
      JSON.parse(json),
      dashboards.find((dashboard) => dashboard.uid.endsWith(kind)),
    );
    assert.equal(fs.existsSync(path.join(__dirname, `${kind}-configmap.yaml`)), false);
  }
});

test("all 92 metric expressions retain their deployed definitions and order except Go identity scopes", () => {
  // Receipts from revision 100, before the title/layout-only changes.
  const hashes = [
    "0ca16340ed1eb9883651f2f5aa94e05e4f56c3ec3ef77cfe0d7f09b17cacd387",
    "56f964c12e8e1205b3e40e708732e09a4f2b277e6cf39528f94b7953afff1d1f",
  ];
  assert.equal(dashboards.flatMap(expressions).length, 92);
  dashboards.forEach((dashboard, index) => {
    assert.equal(expressions(dashboard).length, index === 0 ? 58 : 34);
    // Normalize only the intended scope changes against the original query receipts.
    const originalExpressions = expressions(dashboard).map((expr) =>
      expr
        .replaceAll(
          'exported_job=~"workouts-api|${service_namespace:regex}/workouts-api"',
          'exported_job="workouts-api"',
        )
        .replaceAll(
          'exported_job=~"(${service_namespace:regex}/)?workouts-(api|worker|coverage-worker)"',
          'exported_job=~"workouts-(api|worker|coverage-worker)"',
        )
        .replaceAll(
          'exported_job=~"workouts-coverage-worker|${service_namespace:regex}/workouts-coverage-worker"',
          'exported_job="workouts-coverage-worker"',
        )
        .replaceAll(
          ',service_namespace=~"${service_namespace:regex}|",deployment_environment=~"${environment:regex}|"',
          "",
        ),
    );
    assert.equal(
      createHash("sha256").update(JSON.stringify(originalExpressions)).digest("hex"),
      hashes[index],
    );
  });
});

test("all panel presentation remains unchanged apart from Go identity descriptions and notes", () => {
  const hashes = [
    "3a21ff67d46044cec62f59640f3903054595f4a5ab7ae26ea024ffe5c3e9cfb7",
    "d69f3e208913210e7b50591145257ba5a34ee247d31bb0f04d84beb6e696ac00",
  ];
  dashboards.forEach((dashboard, index) => {
    const panels = allPanels(dashboard).map(({ description, panels, ...panel }) => ({
      ...panel,
      ...(panel.targets ? { targets: panel.targets.map(({ expr, ...target }) => target) } : {}),
      ...(panel.options?.mode === "markdown" ? { options: { ...panel.options, content: "" } } : {}),
    }));
    assert.equal(createHash("sha256").update(JSON.stringify(panels)).digest("hex"), hashes[index]);
  });
});

test("compact transparent navigation has same-tab links for both views and retains variable selections", () => {
  for (const dashboard of dashboards) {
    assert.deepEqual(dashboard.links, []);
    const navigation = dashboard.panels[0];
    assert.equal(navigation.type, "text");
    assert.equal(navigation.title, "");
    assert.equal(navigation.transparent, true);
    assert.deepEqual(navigation.gridPos, { x: 0, y: 0, w: 24, h: 2 });
    assert.equal(navigation.options.mode, "html");
    const html = navigation.options.content;
    assert.match(html, /display:flex;align-items:center;gap:8px;height:32px;/);
    assert.match(html, /role="navigation" aria-label="Workouts Explorer dashboards"/);
    const anchors = [...html.matchAll(/<a ([^>]+)>([^<]+)<\/a>/g)];
    assert.deepEqual(
      anchors.map((anchor) => anchor[2]),
      ["Operational", "Historical"],
    );
    for (const [index, anchor] of anchors.entries()) {
      const view = index === 0 ? "operational" : "historical";
      assert.match(anchor[1], /target="_self"/);
      assert.ok(
        anchor[1].includes(
          `href="/d/workouts-explorer-${view}/workouts-explorer-${view}?\${service_namespace:queryparam}&amp;\${environment:queryparam}"`,
        ),
      );
      assert.match(
        anchor[1],
        /display:inline-flex;align-items:center;justify-content:center;height:32px;box-sizing:border-box;/,
      );
      assert.match(anchor[1], /border:1px solid currentColor;.*color:inherit;/);
      if (dashboard.uid.endsWith(view)) {
        assert.match(anchor[1], /aria-current="page"/);
        assert.match(anchor[1], /font-weight:600;background:rgba\(128,128,128,0.18\)/);
      } else {
        assert.doesNotMatch(anchor[1], /aria-current/);
        assert.match(anchor[1], /font-weight:400;background:transparent/);
      }
    }
    assert.doesNotMatch(
      html,
      /<script|<style|onclick|javascript:|target="_blank"|from=|to=|#|scroll/i,
    );
  }
});

test("namespace/environment variables are query-based, single-select, explicitly defaulted", () => {
  for (const dashboard of dashboards) {
    assert.deepEqual(
      dashboard.templating.list.map((variable) => variable.name),
      ["service_namespace", "environment"],
    );
    assert.deepEqual(
      dashboard.templating.list.map((variable) => variable.label),
      ["Namespace", "Environment"],
    );
    for (const [index, variable] of dashboard.templating.list.entries()) {
      const value = index === 0 ? "workouts-explorer" : "xdev";
      assert.equal(variable.type, "query");
      assert.deepEqual(variable.datasource, datasource);
      assert.equal(variable.includeAll, false);
      assert.equal(variable.multi, false);
      assert.deepEqual(variable.current, { selected: true, text: value, value });
      assert.deepEqual(variable.options, [{ selected: true, text: value, value }]);
      assert.equal(variable.query.query, variable.definition);
      assert.match(variable.definition, /label_values\(nginx_requests_total\{/);
      assert.match(variable.definition, /job="otel-gateway-collector"/);
    }
    const env = dashboard.templating.list[1];
    assert.match(env.definition, /exported_job="\$\{service_namespace\}\/workouts-ui"/);
    assert.match(env.definition, /service_namespace="\$service_namespace"/);
    assert.doesNotMatch(env.definition, /deployment_environment="\$environment"/);
  }
});

test("purposeful group counts, valid unique IDs and non-overlapping 24-column grids", () => {
  for (const [index, dashboard] of dashboards.entries()) {
    assert.equal(queryPanels(dashboard).length, index === 0 ? 28 : 20);
    assert.equal(
      dashboard.panels.filter((panel) => panel.type === "row").length,
      index === 0 ? 6 : 5,
    );
    const panels = allPanels(dashboard);
    const notes = dashboard.panels.at(-1);
    assert.equal(notes.title, "Notes and data availability");
    assert.equal(notes.collapsed, true);
    assert.deepEqual(
      notes.panels.map((panel) => panel.title),
      ["Scope and interpretation", "Data availability: 30 days", "History limitations"],
    );
    assert.ok(
      notes.panels.every(
        (panel) =>
          panel.type === "text" &&
          !panel.targets &&
          panel.gridPos.y >= notes.gridPos.y + notes.gridPos.h,
      ),
    );
    assert.equal(dashboard.panels.filter((panel) => panel.type === "text").length, 1);
    assert.equal(dashboard.panels[1].type, "row");
    assert.equal(dashboard.panels[1].gridPos.y, 2);
    const ids = new Set();
    let row;
    for (const panel of panels) {
      assert.ok(["row", "text", "stat", "timeseries"].includes(panel.type));
      assert.ok(Number.isInteger(panel.id) && panel.id > 0 && !ids.has(panel.id));
      ids.add(panel.id);
      const { x, y, w, h } = panel.gridPos;
      assert.ok([x, y, w, h].every(Number.isInteger));
      assert.ok(x >= 0 && y >= 0 && w > 0 && h > 0 && x + w <= 24);
      assert.ok(panel.title.trim() === panel.title);
      if (panel !== dashboard.panels[0]) assert.ok(panel.title);
      if (panel.type === "row") {
        row = panel;
        assert.equal(panel.collapsed, panel === notes);
        if (panel !== notes) assert.deepEqual(panel.panels, []);
        assert.ok(y >= 2);
      } else {
        assert.ok(panel.description.length > 30);
        if (panel.targets) assert.ok(row && y >= row.gridPos.y + row.gridPos.h);
      }
    }
    for (let a = 0; a < panels.length; a += 1) {
      for (let b = a + 1; b < panels.length; b += 1) {
        const first = panels[a].gridPos;
        const second = panels[b].gridPos;
        const overlap =
          first.x < second.x + second.w &&
          first.x + first.w > second.x &&
          first.y < second.y + second.h &&
          first.y + first.h > second.y;
        assert.equal(overlap, false, `${panels[a].title} overlaps ${panels[b].title}`);
      }
    }
  }
});

test("all query and text metadata, datasource references and modern field config", () => {
  for (const dashboard of dashboards) {
    for (const panel of allPanels(dashboard)) {
      if (panel.type === "text") {
        assert.equal(panel.options.mode, panel === dashboard.panels[0] ? "html" : "markdown");
        if (panel !== dashboard.panels[0]) {
          assert.equal(panel.options.content, panel.description);
          assert.ok(panel.options.content.split("\n").every((line) => line.startsWith("- ")));
          assert.doesNotMatch(panel.options.content, /\b(WARNING|STOP|MUST)\b/);
        }
        assert.equal(panel.targets, undefined);
      } else if (panel.targets) {
        assert.deepEqual(panel.datasource, datasource);
        assert.equal(panel.fieldConfig.defaults.noValue, "N/A");
        assert.ok(panel.fieldConfig.defaults.unit);
        assert.deepEqual(panel.fieldConfig.defaults.mappings, [
          { type: "special", options: { match: "nan", result: { text: "N/A" } } },
        ]);
        assert.equal(
          new Set(panel.targets.map((target) => target.refId)).size,
          panel.targets.length,
        );
        for (const target of panel.targets) {
          assert.deepEqual(target.datasource, datasource);
          assert.equal(target.editorMode, "code");
          assert.equal(target.instant, panel.type === "stat");
          assert.equal(target.range, panel.type !== "stat");
          assert.ok(target.expr && target.legendFormat);
          assert.equal(target.format, "time_series");
        }
        if (panel.type === "timeseries") {
          assert.equal(panel.fieldConfig.defaults.custom.spanNulls, false);
          assert.equal(panel.fieldConfig.defaults.custom.insertNulls, false);
          assert.equal(panel.fieldConfig.defaults.custom.stacking.mode, "none");
        } else if (panel.type === "stat") {
          const singleValue = ["API requests", "API 5xx percentage", "UI requests"].includes(
            panel.title,
          );
          assert.deepEqual(panel.options.text, { titleSize: 14, valueSize: singleValue ? 36 : 18 });
          assert.equal(panel.options.textSize, undefined);
        }
      }
    }
  }
});

test("Coverage row titles stay concise while ledger qualifications live in collapsed notes", () => {
  for (const dashboard of dashboards) {
    assert.ok(
      dashboard.panels.some((panel) => panel.type === "row" && panel.title === "3 / Coverage"),
    );
    assert.ok(dashboard.panels.every((panel) => !panel.title.includes("not durable ledger")));
  }
  assert.match(
    note(historical(), "History limitations").options.content,
    /outcome counters.*not a durable job ledger/s,
  );
});

test("component-first titles use consistent measurements and reserve caveats for descriptions", () => {
  const expectedRows = ["1 / API and UI traffic", "2 / Tiles", "3 / Coverage"];
  for (const dashboard of dashboards) {
    assert.deepEqual(
      dashboard.panels
        .filter((panel) => panel.type === "row")
        .slice(0, 3)
        .map((panel) => panel.title),
      expectedRows,
    );
    for (const panel of allPanels(dashboard)) {
      assert.doesNotMatch(
        panel.title,
        /\b(observed|observations|sampled|live|candidate|boundaries)\b/i,
      );
      assert.doesNotMatch(panel.title, /\bDB\b|selected-range|in selected range/);
    }
    assert.ok(queryPanels(dashboard).some((panel) => panel.title === "Coverage outcomes"));
    assert.ok(
      queryPanels(dashboard).some((panel) => panel.title.startsWith("API and Martin latency (p95")),
    );
  }
});

test("Historical traffic uses a single compact five-panel row", () => {
  const dashboard = dashboards[1];
  const traffic = dashboard.panels.slice(2, 7);
  assert.equal(traffic.length, 5);
  assert.deepEqual(
    traffic.map((panel) => panel.title),
    [
      "API requests",
      "API 5xx percentage",
      "UI requests",
      "API requests by status (daily)",
      "UI requests and connection gap (daily)",
    ],
  );
  assert.deepEqual(
    traffic.map((panel) => panel.type),
    ["stat", "stat", "stat", "timeseries", "timeseries"],
  );
  assert.ok(traffic.every((panel) => panel.gridPos.y === 3 && panel.gridPos.h === 4));
  assert.equal(traffic[0].gridPos.x, 0);
  assert.equal(traffic.at(-1).gridPos.x + traffic.at(-1).gridPos.w, 24);
  assert.equal(dashboard.panels[7].gridPos.y, 7);
});

test("single-value Stats use 36px without enlarging grouped or multi-query Stats", () => {
  for (const dashboard of dashboards) {
    const large = queryPanels(dashboard).filter(
      (panel) => panel.type === "stat" && panel.options.text.valueSize === 36,
    );
    assert.deepEqual(
      large.map((panel) => panel.title),
      dashboard.uid.endsWith("historical")
        ? ["API requests", "API 5xx percentage", "UI requests"]
        : [],
    );
    for (const panel of large) {
      assert.equal(panel.targets.length, 1);
      assert.doesNotMatch(panel.targets[0].expr, /\bsum by\b/);
    }
  }
});

test("every metric selector belongs to the independent audited allowlist and gateway job", () => {
  for (const dashboard of dashboards) {
    for (const expr of expressions(dashboard)) {
      const matches = selectors(expr);
      assert.ok(matches.length > 0, expr);
      for (const [, name, labels] of matches) {
        assert.ok(allowlist.has(name), `Unaudited metric ${name}`);
        assert.match(labels, /(?:^|,)job="otel-gateway-collector"(?:,|$)/);
        assert.doesNotMatch(
          labels,
          /(?:^|,)(?:pod|namespace|instance|endpoint|account_id|job_id|workout_id|region_id|generation_id|server_address)=/,
        );
      }
    }
  }
});

test("Go scopes filter new identities and retain explicitly unscoped legacy history; native/self stay scoped", () => {
  for (const dashboard of dashboards) {
    const scopeNote = note(dashboard, "Scope and interpretation").options.content;
    assert.match(scopeNote, /Namespace and Environment filter newly labeled Go API\/workers/);
    assert.match(scopeNote, /Legacy bare exported_job history.*Thanos.*remains unscoped/);
    assert.match(scopeNote, /environment is unknowable.*only earlier xdev deployed metrics/);
    assert.match(dashboard.description, /filter newly labeled Go.*Legacy.*unscoped/);
    for (const panel of queryPanels(dashboard).filter((panel) =>
      panel.title.startsWith("Go runtime"),
    )) {
      assert.match(panel.description, /filter newly labeled Go.*Legacy.*unscoped/);
    }
    for (const expr of expressions(dashboard)) {
      for (const [, name, labels] of selectors(expr)) {
        if (/^(nginx_|martin_|otelcol_)/.test(name)) {
          assert.match(labels, /exported_job=~?"\$\{service_namespace(?::regex)?\}\//);
          assert.match(labels, /service_name=~?"workouts-/);
          assert.match(labels, /service_namespace="\$service_namespace"/);
          assert.match(labels, /deployment_environment="\$environment"/);
          assert.doesNotMatch(labels, /deployment_environment_name/);
        } else {
          const services = /^(http_server_|workouts_api_)/.test(name)
            ? ["workouts-api"]
            : name.startsWith("coverage_")
              ? ["workouts-coverage-worker"]
              : ["workouts-api", "workouts-worker", "workouts-coverage-worker"];
          assert.ok(labels.includes('service_namespace=~"${service_namespace:regex}|"'));
          assert.ok(labels.includes('deployment_environment=~"${environment:regex}|"'));
          assert.ok(labels.includes("${service_namespace:regex}/"));
          assert.doesNotMatch(
            labels,
            /workouts-explorer|xdev|\bservice_name\b|service_instance_id/,
          );
          const matchers = [
            ...labels.matchAll(
              /(?:^|,)(job|exported_job|service_namespace|deployment_environment)(=~|=)"([^"]*)"/g,
            ),
          ];
          assert.equal(matchers.length, 4);
          for (const [namespace, environment] of [
            ["other.namespace", "stage"],
            ["another-namespace", "prod.us"],
          ]) {
            const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
            const matches = (series) =>
              matchers.every(([, label, operator, value]) => {
                const pattern = value
                  .replaceAll("${service_namespace:regex}", escape(namespace))
                  .replaceAll("${environment:regex}", escape(environment));
                const actual = series[label] || "";
                return operator === "=~"
                  ? new RegExp(`^(?:${pattern})$`).test(actual)
                  : actual === pattern;
              });
            const legacy = (service) => ({ job: "otel-gateway-collector", exported_job: service });
            const labeled = (service) => ({
              ...legacy(`${namespace}/${service}`),
              service_namespace: namespace,
              deployment_environment: environment,
            });
            for (const service of ["workouts-api", "workouts-worker", "workouts-coverage-worker"]) {
              const allowed = services.includes(service);
              assert.equal(matches(legacy(service)), allowed, `${name}: legacy ${service}`);
              assert.equal(matches(labeled(service)), allowed, `${name}: labeled ${service}`);
              assert.equal(
                matches({ ...legacy(service), service_namespace: "", deployment_environment: "" }),
                allowed,
              );
              assert.equal(matches({ ...labeled(service), service_namespace: "wrong" }), false);
              assert.equal(
                matches({ ...labeled(service), deployment_environment: "wrong" }),
                false,
              );
              assert.equal(
                matches({ ...labeled(service), exported_job: `wrong/${service}` }),
                false,
              );
              assert.equal(matches({ ...legacy(service), deployment_environment: "wrong" }), false);
              assert.equal(matches({ ...legacy(service), service_namespace: "wrong" }), false);
            }
            assert.equal(matches(legacy("workouts-ui")), false);
            assert.equal(matches(labeled("workouts-ui")), false);
            assert.equal(matches({ ...labeled(services[0]), job: "wrong" }), false);
          }
        }
      }
    }
  }
});

test("counter reset handling precedes all aggregation; no raw cumulative heights", () => {
  for (const dashboard of dashboards) {
    for (const expr of expressions(dashboard)) {
      assert.doesNotMatch(expr, /(?:rate|increase)\(\s*(?:sum|avg)\b/);
      for (const match of selectors(expr)) {
        const name = match[1];
        if (/_total$|_(bucket|sum|count)$/.test(name) && name !== "go_goroutine_count") {
          assert.match(
            expr.slice(0, match.index),
            /(?:rate|increase)\($/,
            `Raw counter ${name}: ${expr}`,
          );
          assert.match(
            expr.slice(match.index + match[0].length),
            /^\[(?:\$__rate_interval|\$__range|15m|1d)\]\)/,
          );
        }
      }
    }
  }
});

test("histogram quantiles aggregate buckets first, filter no-observation denominators and never average p95", () => {
  for (const dashboard of dashboards) {
    for (const expr of expressions(dashboard).filter((expr) =>
      expr.includes("histogram_quantile"),
    )) {
      assert.match(
        expr,
        /^histogram_quantile\(0\.95, sum by \(le(?:,[a-z_,]+)?\) \((?:rate|increase)\(/,
      );
      assert.match(expr, /_bucket\{/);
      assert.match(
        expr,
        /and on \([^)]*\) \(sum(?: by \([^)]*\))?\((?:rate|increase)\(|and on \([^)]*\) \(sum by \([^)]*\) \((?:rate|increase)\(/,
      );
      assert.match(expr, /_count\{/);
      assert.match(expr, /> 0\)$/);
      assert.doesNotMatch(expr, /db_client_operation|avg|\)\s*[-+]\s*histogram_quantile/);
    }
  }
});

test("fractions and means have filtered, aligned denominators; no synthetic zero/NaN clamps", () => {
  for (const dashboard of dashboards) {
    for (const expr of expressions(dashboard)) {
      assert.doesNotMatch(
        expr,
        /\bor\b|vector\(|clamp|\bNaN\b|absent\(|==\s*bool|>\s*bool|\bbool\b/i,
      );
      if (expr.includes(" / ") && !expr.includes(" / 1e9")) assert.match(expr, /> 0\)$/);
      if (expr.includes(" / ") && !expr.includes("histogram_quantile")) {
        assert.match(expr, /> 0\)$/);
        const groupings = [...expr.matchAll(/sum by \(([^)]*)\)/g)].map((match) => match[1]);
        assert.ok(
          groupings.length === 0 || groupings.every((grouping) => grouping === groupings[0]),
          expr,
        );
        const terms = selectors(expr);
        assert.equal(terms.length, 2);
        const numeratorLabels = terms[0][2]
          .replace(/,le="[^"]+"/, "")
          .replace(',http_response_status_code=~"5.."', "");
        assert.equal(numeratorLabels, terms[1][2], `Mismatched ratio population: ${expr}`);
      }
    }
  }
});

test("pool duration units, empty-wait denominator and DB operation scope are correct", () => {
  for (const dashboard of dashboards) {
    for (const panel of queryPanels(dashboard)) {
      for (const target of panel.targets) {
        if (/pgxpool_.*nanoseconds_total/.test(target.expr)) {
          assert.equal(panel.fieldConfig.defaults.unit, "s");
          assert.match(target.expr, /\/ 1e9/);
          assert.match(target.expr, /> 0\)$/);
          if (target.expr.includes("empty_acquire_wait_time")) {
            assert.match(target.expr, /(?:rate|increase)\(pgxpool_empty_acquire_total\{/);
            assert.doesNotMatch(target.expr, /pgxpool_acquires_total/);
          } else assert.match(target.expr, /(?:rate|increase)\(pgxpool_acquires_total\{/);
        }
        if (target.expr.includes("db_client_operation"))
          assert.doesNotMatch(target.expr, /db_client_connection_pool_name|histogram_quantile/);
      }
    }
  }
});

test("native spellings, success status domains, template routes and unstacked nginx totals", () => {
  const all = dashboards.flatMap(expressions).join("\n");
  assert.doesNotMatch(
    all,
    /martin_http_request_duration|nginx_connections_current_connections|otelcol_process_memory_rss_bytes|runtime_total_alloc_bytes/,
  );
  assert.match(all, /martin_http_requests_duration_seconds_bucket/);
  assert.match(all, /martin_route="\/\{source_ids\}\/\{z\}\/\{x\}\/\{y\}"/);
  assert.match(all, /status=~"200\|204"/);
  for (const dashboard of dashboards) {
    for (const panel of queryPanels(dashboard)) {
      if (panel.targets.some((target) => target.expr.includes("nginx_connections_current"))) {
        assert.equal(panel.fieldConfig.defaults.custom.stacking.mode, "none");
        assert.match(panel.description, /active is TOTAL.*breakdown/);
        assert.match(panel.targets[0].expr, /sum by \(state\)/);
      }
      for (const target of panel.targets) {
        for (const [, name, labels] of selectors(target.expr)) {
          if (name.startsWith("martin_")) {
            assert.match(
              labels,
              /method="GET",martin_route="\/\{source_ids\}\/\{z\}\/\{x\}\/\{y\}"/,
            );
            assert.doesNotMatch(
              labels,
              /http_request_method|http_response_status_code|http_route=/,
            );
          }
        }
      }
    }
  }
});

test("operational defaults, sparse semantics, latency guide and explicit missing-control explanations", () => {
  const dashboard = dashboards[0];
  assert.deepEqual(dashboard.time, { from: "now-1h", to: "now" });
  assert.equal(dashboard.refresh, "30s");
  for (const panel of queryPanels(dashboard)) assert.equal(panel.interval, "30s");
  const coverage = queryPanels(dashboard).find((panel) => panel.title === "Coverage timing (p95)");
  assert.equal(coverage.targets.length, 4);
  for (const target of coverage.targets) assert.match(target.expr, /\[15m\]/);
  assert.match(coverage.description, /absent.*N\/A is not zero/s);
  assert.match(coverage.description, /not oldest backlog.*Evaluate/s);
  const latency = queryPanels(dashboard).find(
    (panel) => panel.title === "API and Martin latency (p95)",
  );
  assert.equal(latency.targets.length, 3);
  assert.match(latency.description, /Never subtract or add p95s.*not an accepted SLO/);
  assert.match(latency.fieldConfig.defaults.custom.axisLabel, /500 ms provisional guide, not SLO/);
  assert.deepEqual(latency.fieldConfig.defaults.thresholds.steps[1], {
    color: "orange",
    value: 0.5,
  });
  const mixed = queryPanels(dashboard).find(
    (panel) => panel.title === "API 5xx percentage and GET latency (p95)",
  );
  const percentage = mixed.fieldConfig.overrides[0];
  assert.equal(percentage.matcher.options, "5xx percentage");
  assert.deepEqual(percentage.properties.find((property) => property.id === "thresholds").value, {
    mode: "absolute",
    steps: [{ color: "green", value: null }],
  });
  assert.deepEqual(
    percentage.properties.find((property) => property.id === "custom.thresholdsStyle").value,
    { mode: "off" },
  );
  const candidates = queryPanels(dashboard).find(
    (panel) => panel.title === "Collector metric errors",
  );
  assert.match(candidates.description, /UNOBSERVED CANDIDATES.*not zero.*hide its own warnings/);
  const footer = note(dashboard, "History limitations").options.content;
  assert.match(footer, /No backlog.*SQL inventory.*No accepted two-week SLO.*No accounts/s);
});

test("historical stats use whole selected windows; daily counters use 1d increases with 1d evaluation steps", () => {
  const dashboard = dashboards[1];
  assert.deepEqual(dashboard.time, { from: "now-7d", to: "now" });
  assert.equal(dashboard.refresh, "5m");
  assert.deepEqual(dashboard.timepicker.time_options, ["1d", "2d", "7d", "14d", "30d"]);
  const availability = note(dashboard, "Data availability: 30 days").options.content;
  assert.match(
    availability,
    /10\/6\/2026.*raw.*2 days.*Five-minute.*14 days.*hourly.*30 days.*not a guarantee.*no automatic range clamp.*rolling 24-hour.*one-day steps/s,
  );
  assert.doesNotMatch(availability, /20d|20 days|10d|shorten|STOP|MUST/);
  for (const panel of queryPanels(dashboard)) {
    if (panel.type === "stat") {
      for (const target of panel.targets) {
        assert.equal(target.instant, true);
        assert.match(target.expr, /increase\(/);
        assert.match(target.expr, /\[\$__range\]/);
        assert.doesNotMatch(target.expr, /\brate\(|\[1d\]|\[14d\]/);
      }
    } else if (panel.title.endsWith("(daily)")) {
      assert.equal(panel.interval, "1d");
      assert.equal(panel.maxDataPoints, 30);
      for (const target of panel.targets) {
        assert.equal(target.instant, false);
        assert.equal(target.range, true);
        assert.equal(target.interval, "1d");
        assert.equal(target.intervalFactor, 1);
        assert.match(target.expr, /increase\(/);
        assert.match(target.expr, /\[1d\]/);
        assert.doesNotMatch(target.expr, /\brate\(|\$__range|\$__rate_interval/);
      }
    } else assert.match(panel.description, /[Ss]ampled/);
  }
  assert.match(
    note(dashboard, "History limitations").options.content,
    /No fleet accounts.*No synthetic.*no year graphs/s,
  );
});

test("committed generated files are current and CLI check succeeds without writing", () => {
  for (const [name, content] of Object.entries(artifacts())) {
    assert.equal(fs.readFileSync(path.join(__dirname, name), "utf8"), content, name);
  }
  const result = spawnSync(process.execPath, [path.join(__dirname, "generate.cjs"), "--check"], {
    encoding: "utf8",
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "");
});

test("CLI detects missing/stale artifacts read-only, rejects invalid arguments and regenerates deterministically", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "opencode", "workouts-grafana-"));
  try {
    const output = path.join(directory, "deploy", "grafana");
    fs.mkdirSync(output, { recursive: true });
    const script = path.join(output, "generate.cjs");
    fs.copyFileSync(path.join(__dirname, "generate.cjs"), script);
    const run = (...args) => spawnSync(process.execPath, [script, ...args], { encoding: "utf8" });
    assert.equal(run("--check").status, 1);
    assert.deepEqual(fs.readdirSync(output), ["generate.cjs"]);
    assert.equal(fs.existsSync(path.join(directory, "helm")), false);
    assert.equal(run("--invalid").status, 1);
    assert.equal(run().status, 0);
    assert.equal(run("--check").status, 0);
    const filename = path.join(output, "operational.json");
    fs.writeFileSync(filename, "stale\n");
    const result = run("--check");
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Stale generated artifact.*operational.json/);
    assert.equal(fs.readFileSync(filename, "utf8"), "stale\n");
    assert.equal(run().status, 0);
    const mirror = path.join(directory, "helm", "dashboards", "historical.json");
    fs.writeFileSync(mirror, "stale mirror\n");
    assert.equal(run("--check").status, 1);
    assert.equal(fs.readFileSync(mirror, "utf8"), "stale mirror\n");
    assert.equal(run().status, 0);
    for (const [name, content] of Object.entries(artifacts()))
      assert.equal(fs.readFileSync(path.join(output, name), "utf8"), content);
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});
