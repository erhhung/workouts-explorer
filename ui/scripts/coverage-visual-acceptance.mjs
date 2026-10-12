import { chromium } from "@playwright/test";
import { createHash } from "node:crypto";
import { execFileSync, spawn } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import profile from "../../deploy/acceptance/profile.cjs";
import evidence from "../../deploy/acceptance/evidence.cjs";

const origin =
  process.env.COVERAGE_ACCEPTANCE_UI_URL || "https://acceptance.workouts.xdev.fourteeners.local";
const allowedOrigins = new Set(["https://acceptance.workouts.xdev.fourteeners.local"]);
if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true" || !allowedOrigins.has(origin)) {
  throw new Error("Refusing: explicit disposable guard and exact synthetic fixture URL required.");
}
const namespace = profile.requireProfile();
const artifactRoot = profile.artifact("visual");
const artifactDir = resolve(
  process.env.COVERAGE_ACCEPTANCE_ARTIFACT_DIR || `${artifactRoot}/${namespace.metadata.uid}`,
);
if (!artifactDir.startsWith(`${artifactRoot}/`)) {
  throw new Error(
    "Artifacts must be under the selected profile visual directory in /tmp/opencode/.",
  );
}
const bearer = createHash("sha256")
  .update("FAKE disposable synthetic coverage acceptance bearer; never a live credential")
  .digest("base64url");
const hash = (data) => createHash("sha256").update(data).digest("hex");
const resultsPath = profile.results("visual");
const binding = {
  ...profile.binding,
  schema_version: evidence.SCHEMA,
  namespace: evidence.NAMESPACE,
  namespace_uid: namespace.metadata.uid,
};
// Reviews are explicit checksum-bound observations, never inferred from successful capture.
if (process.argv[2] === "--record-review") {
  const results = evidence.assertBinding(
    JSON.parse(await readFile(resultsPath, "utf8")),
    namespace,
  );
  const reviews = JSON.parse(await readFile(process.argv[3], "utf8"));
  for (const review of reviews) {
    evidence.assertBinding(review, namespace);
    const artifact = results.artifacts.find(
      (item) => item.sha256 === review.sha256 && item.path.endsWith(`/${review.name}.png`),
    );
    if (
      !artifact ||
      !resolve(artifact.path).startsWith(`${artifactDir}/`) ||
      hash(await readFile(artifact.path)) !== review.sha256 ||
      !review.notes
    )
      throw new Error(
        "Review must name an existing checksum-verified synthetic screenshot and observations.",
      );
    evidence.assertBinding(artifact, namespace);
    artifact.review = "REVIEWED_WITH_OBSERVATIONS";
    artifact.reviewedAt = new Date().toISOString();
    artifact.reviewNotes = review.notes;
    const testCase = results.cases.find((item) => item.name === review.name);
    if (testCase) testCase.review = artifact.review;
  }
  results.counts.pendingVisualReviews = results.artifacts.filter(
    (item) => item.review === "PENDING_MANUAL_REVIEW",
  ).length;
  results.counts.reviewedScreenshots =
    results.artifacts.length - results.counts.pendingVisualReviews;
  if (profile.requireProfile().metadata.uid !== namespace.metadata.uid)
    throw new Error("namespace changed during review");
  await writeFile(resultsPath, `${JSON.stringify(results, null, 2)}\n`);
  console.log(
    JSON.stringify({ recordedReviews: reviews.length, status: results.status, ...results.counts }),
  );
  process.exit(0);
}
const k = (...args) =>
  execFileSync("kubectl", ["--context", "xdev", "-n", "workouts-acceptance", ...args], {
    encoding: "utf8",
    timeout: 30000,
  });
const year = profile.manifest.year;
const ranges = { month: `${year}-09-01/${year}-09-30`, year: `${year}-01-01/${year}-12-31` };
const readinessOverlay = process.env.COVERAGE_ACCEPTANCE_READINESS_OVERLAY === "true";
const viewports = { desktop: { width: 1440, height: 900 }, mobile: { width: 390, height: 844 } };
const visions = ["none", "protanopia", "deuteranopia", "tritanopia", "achromatopsia"];
const labels = [
  "1 workout",
  "2 workouts",
  "3-5 workouts",
  "6-10 workouts",
  "11-25 workouts",
  "26+ workouts",
];
const result = {
  ...binding,
  started_at: new Date().toISOString(),
  status: "RUNNING",
  scope:
    "Disposable public synthetic fixture only; real HTTP, MapLibre and MVT; no production evidence.",
  fixture: {
    origin,
    owner: "Synthetic Acceptance Owner",
    roads: "40x40 grid",
    yearWorkouts: profile.total,
    yearRouteRows: profile.routed,
    readinessOverlay,
  },
  instrumentation:
    "Test-only DOM React fiber hook traversal to existing MapCanvas ref; no product globals or source changes.",
  targets: { initialUsableMapMs: 3000, coverageUsableFromActionMs: 3000 },
  timingMethod:
    "Navigation performance clock through nonzero rendered features, loaded sources, settled map and two animation frames; coverage timing also includes UI mode activation. RAF gaps and long tasks are observations, not fabricated threshold passes.",
  limitations: [
    "Single sequential run; warm server/database caches are not flushed.",
    "Remote shared Chromium software rendering/cluster scheduling affects timings.",
    "Color-vision screenshots require human review; capture is not a perceptual accessibility pass.",
    "200% zoom uses half-sized CSS viewport at deviceScaleFactor=2, equivalent layout/reflow, not browser toolbar zoom.",
  ],
  cases: [],
  artifacts: [],
  warnings: [],
};
const sqlOracles = {};
for (const [name, range] of Object.entries(ranges)) {
  const [start, end] = range.split("/");
  const sql = `BEGIN READ ONLY;
    WITH owner AS (SELECT u.account_id FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id WHERE p.username='acceptance-fixture'),
    fixture AS (SELECT w.*,EXISTS(SELECT 1 FROM app.workout_route_points r WHERE r.account_id=w.account_id AND r.workout_id=w.id) routed
      FROM app.workouts w JOIN owner USING(account_id) WHERE local_start_date BETWEEN '${start}' AND '${end}'),
    paths AS (SELECT 'path' kind, logical_path_id id, count(DISTINCT workout_id)::int n
      FROM app.workout_path_attributions JOIN owner USING(account_id) WHERE visit_date BETWEEN '${start}' AND '${end}' GROUP BY logical_path_id),
    parks AS (SELECT 'park' kind, park_id id, count(DISTINCT workout_id)::int n
      FROM app.workout_park_attributions JOIN owner USING(account_id) WHERE visit_date BETWEEN '${start}' AND '${end}' GROUP BY park_id),
    entities AS (SELECT * FROM paths UNION ALL SELECT * FROM parks)
    SELECT json_build_object('fixture_accounts',(SELECT count(*) FROM owner),'workouts',(SELECT count(*) FROM fixture),
      'routed_workouts',(SELECT count(*) FROM fixture WHERE routed),'unrouted_workouts',(SELECT count(*) FROM fixture WHERE NOT routed),
      'counts',COALESCE((SELECT json_object_agg(kind || ':' || upper(replace(id::text,'-','')),n) FROM entities),'{}'::json),
      'buckets',(SELECT json_object_agg(bucket,n) FROM (SELECT app.coverage_count_bucket(n::bigint) bucket,count(*) n FROM entities GROUP BY 1) b));
    ROLLBACK;`;
  const raw = k(
    "exec",
    "deployment/acceptance-postgres",
    "--",
    "psql",
    "-U",
    "postgres",
    "-d",
    "acceptance_benchmark",
    "-At",
    "-v",
    "ON_ERROR_STOP=1",
    "-c",
    sql,
  );
  sqlOracles[name] = JSON.parse(raw.split("\n").find((line) => line.startsWith("{")));
  if (sqlOracles[name].fixture_accounts !== 1)
    throw new Error("expected one fixture owner account");
  sqlOracles[name].sqlSha256 = hash(sql);
}
result.sqlOracle = Object.fromEntries(
  Object.entries(sqlOracles).map(([name, oracle]) => [
    name,
    {
      workouts: oracle.workouts,
      routeRows: oracle.routed_workouts,
      unroutedWorkouts: oracle.unrouted_workouts,
      entities: Object.keys(oracle.counts).length,
      buckets: oracle.buckets,
      sqlSha256: oracle.sqlSha256,
      countsSha256: hash(JSON.stringify(oracle.counts)),
      method: "Read-only SQL distinct workout counts from fixture path/park attributions.",
    },
  ]),
);
if (
  sqlOracles.year.workouts !== profile.total ||
  sqlOracles.year.routed_workouts !== profile.routed
)
  throw new Error("SQL fixture counts do not match selected profile");
result.fixture.monthWorkouts = sqlOracles.month.workouts;
result.fixture.monthRouteRows = sqlOracles.month.routed_workouts;

// This traverses only the mounted canvas ancestors, never relying on minified component names.
function installProbe() {
  const getMap = () => {
    const el = document.querySelector(".map-canvas");
    let fiber = el?.[Object.keys(el).find((key) => key.startsWith("__reactFiber$"))];
    for (; fiber; fiber = fiber.return) {
      for (let hook = fiber.memoizedState, i = 0; hook && i < 160; hook = hook.next, i++) {
        const value = hook.memoizedState?.current;
        if (value?.queryRenderedFeatures && value?.getCanvas && value?.getStyle) return value;
      }
    }
  };
  const longTasks = [];
  new PerformanceObserver((list) =>
    longTasks.push(
      ...list
        .getEntries()
        .map((entry) => ({ startMs: entry.startTime, durationMs: entry.duration })),
    ),
  ).observe({ type: "longtask", buffered: true });
  const features = (map, ids) => {
    const layers = ids.filter((id) => map.getLayer(id));
    return layers.length ? map.queryRenderedFeatures({ layers }) : [];
  };
  const state = () => {
    const map = getMap();
    if (!map) return null;
    const style = map.getStyle();
    if (!style?.layers) return null;
    const baseLayers = style.layers
      .filter((layer) => !layer.id.startsWith("private-") && layer.type !== "background")
      .map((layer) => layer.id);
    return {
      loaded: map.loaded(),
      tilesLoaded: map.areTilesLoaded(),
      moving: map.isMoving(),
      routeFeatures: features(map, ["private-workout-routes"]).length,
      coverage: features(map, ["private-workout-coverage", "private-workout-coverage-parks"]).map(
        (feature) => feature.properties,
      ),
      focusFeatures: features(map, [
        "private-workout-coverage-focus",
        "private-workout-coverage-parks-focus",
      ]).length,
      baseFeatures: baseLayers.length ? features(map, baseLayers).length : 0,
      baseLayers,
      aggregateOpacity: map.getLayer("private-workout-coverage")
        ? map.getPaintProperty("private-workout-coverage", "line-opacity")
        : null,
      focusOpacity: map.getLayer("private-workout-coverage-focus")
        ? map.getPaintProperty("private-workout-coverage-focus", "line-opacity")
        : null,
      focusColor: map.getLayer("private-workout-coverage-focus")
        ? map.getPaintProperty("private-workout-coverage-focus", "line-color")
        : null,
      canvasMounted: map.getCanvas().isConnected,
      canvasFocused: document.activeElement === map.getCanvas(),
      timeMs: performance.now(),
      longTasks: [...longTasks],
    };
  };
  window.__coverageAcceptance = { getMap, state, longTasks };
}

async function ready(page, mode) {
  await page.waitForFunction(
    (mode) => {
      const state = window.__coverageAcceptance?.state();
      return (
        state?.loaded &&
        state.tilesLoaded &&
        !state.moving &&
        (mode === "coverage" ? state.coverage.length > 0 : state.routeFeatures > 0)
      );
    },
    mode,
    { timeout: 30000 },
  );
  return page.evaluate(async () => {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    return window.__coverageAcceptance.state();
  });
}

async function screenshot(page, name, cdp, vision = "none") {
  await cdp.send("Emulation.setEmulatedVisionDeficiency", { type: vision });
  await page.evaluate(
    () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
  );
  const path = resolve(artifactDir, `${name}.png`);
  const viewport = await page.evaluate(() => ({
    width: innerWidth,
    height: innerHeight,
    devicePixelRatio,
  }));
  const buffer = await page.screenshot({ path, fullPage: false, scale: "css", timeout: 15000 });
  const dimensions = { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
  if (dimensions.width !== viewport.width || dimensions.height !== viewport.height)
    throw new Error(`Screenshot viewport mismatch: ${JSON.stringify({ viewport, dimensions })}`);
  const artifact = {
    ...binding,
    path,
    sha256: hash(buffer),
    bytes: buffer.length,
    dimensions,
    viewport,
    vision,
    review: "PENDING_MANUAL_REVIEW",
  };
  result.artifacts.push(artifact);
  return artifact;
}

async function controls(page, open) {
  const trigger = page.getByRole("button", { name: "Routes and controls", exact: true });
  if (!(await trigger.isVisible())) return;
  const sheet = page.locator("#mobile-map-sheet");
  if (open && !(await sheet.isVisible())) await trigger.click();
  if (!open && (await sheet.isVisible()))
    await page.getByRole("button", { name: "Close Routes and controls" }).click();
}

async function overflow(page) {
  return page.evaluate(() => {
    const rect = document.querySelector(".map-canvas").getBoundingClientRect();
    const dialog = document.querySelector('[role="dialog"]');
    const d = dialog?.getBoundingClientRect();
    return {
      viewport: innerWidth,
      document: document.documentElement.scrollWidth,
      pageOverflow: document.documentElement.scrollWidth > innerWidth + 1,
      canvas: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
      canvasInViewport:
        rect.x >= -1 && rect.right <= innerWidth + 1 && rect.width > 0 && rect.height > 0,
      dialogInViewport:
        !d || (d.x >= -1 && d.right <= innerWidth + 1 && d.y >= -1 && d.bottom <= innerHeight + 1),
      scrollContainers: [
        ...document.querySelectorAll(".road-coverage-table-wrap,.map-filter-options"),
      ]
        .filter((el) => el.getBoundingClientRect().width > 0)
        .map((el) => ({
          className: el.className,
          width: el.clientWidth,
          scrollWidth: el.scrollWidth,
        })),
    };
  });
}

function check(testCase, name, passed, details) {
  testCase.checks.push({ name, status: passed ? "PASS" : "FAIL", details });
}

async function attempt(testCase, name, fn) {
  try {
    await fn();
  } catch (error) {
    check(testCase, name, false, error.message);
  }
}

async function measureInteraction(page) {
  return page.evaluate(async () => {
    const { getMap, longTasks } = window.__coverageAcceptance;
    const map = getMap(),
      gaps = [];
    const start = performance.now();
    let previous;
    await new Promise((resolve) => {
      function frame(time) {
        if (previous !== undefined) gaps.push(time - previous);
        previous = time;
        if (time - start < 2200) requestAnimationFrame(frame);
        else resolve();
      }
      requestAnimationFrame(frame);
      const center = map.getCenter();
      map.easeTo({ center: [center.lng + 0.001, center.lat + 0.001], duration: 700 });
      setTimeout(() => map.easeTo({ zoom: map.getZoom() + 0.3, duration: 700 }), 800);
    });
    const sorted = [...gaps].sort((a, b) => a - b);
    return {
      durationMs: performance.now() - start,
      frames: gaps.length,
      gapsMs: gaps,
      p50GapMs: sorted[Math.floor(sorted.length * 0.5)],
      p95GapMs: sorted[Math.floor(sorted.length * 0.95)],
      maxGapMs: Math.max(...gaps),
      gapsOver50Ms: gaps.filter((gap) => gap > 50).length,
      longTasks: longTasks.filter((entry) => entry.startMs >= start),
      status: "RECORDED_NO_ACCEPTANCE_THRESHOLD",
    };
  });
}

let forward, browser;
await mkdir(artifactDir, { recursive: true });
try {
  let endpoint = process.env.COVERAGE_ACCEPTANCE_PLAYWRIGHT_WS;
  if (endpoint && !/^ws:\/\/(127\.0\.0\.1|localhost):19123\/$/.test(endpoint))
    throw new Error("Refusing non-allowlisted browser endpoint.");
  if (!endpoint) {
    forward = spawn(
      "kubectl",
      ["--context", "homelab", "-n", "playwright", "port-forward", "svc/playwright", "19123:3000"],
      { stdio: ["ignore", "pipe", "pipe"] },
    );
    await new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("Browser port-forward readiness timeout")),
        20000,
      );
      forward.on("error", reject);
      forward.on("exit", (code) => {
        clearTimeout(timer);
        reject(new Error(`Browser port-forward exited ${code}`));
      });
      forward.stdout.on("data", (data) => {
        if (data.toString().includes("Forwarding from")) {
          clearTimeout(timer);
          resolve();
        }
      });
      forward.stderr.on("data", (data) =>
        result.warnings.push(`Browser forward: ${data.toString().trim()}`),
      );
    });
    endpoint = "ws://127.0.0.1:19123/";
  }
  browser = await chromium.connect(endpoint, { timeout: 30000 });
  result.browser = { version: browser.version(), client: "1.62.0", remote: true };
  for (const [rangeName, range] of Object.entries(ranges))
    for (const theme of ["light", "dark"])
      for (const [viewportName, viewport] of Object.entries(viewports)) {
        const name = `${rangeName}-${theme}-${viewportName}`;
        const testCase = {
          name,
          kind: "functional",
          range,
          theme,
          viewport,
          checks: [],
          timings: {},
        };
        result.cases.push(testCase);
        const context = await browser.newContext({ viewport, colorScheme: theme });
        const blocked = [],
          httpErrors = [],
          tiles = [],
          pageErrors = [];
        const bodyTasks = [];
        let selection;
        await context.route("**/*", async (route) => {
          const request = route.request(),
            url = new URL(request.url());
          if (!["http:", "https:"].includes(url.protocol)) return route.continue();
          if (url.origin !== origin) {
            blocked.push(url.origin + url.pathname);
            return route.abort("blockedbyclient");
          }
          const headers = { ...request.headers() };
          if (url.pathname.startsWith("/api/")) headers.authorization = `Bearer ${bearer}`;
          await route.continue({ headers });
        });
        await context.addInitScript(installProbe);
        const page = await context.newPage();
        page.setDefaultTimeout(10000);
        page.on("pageerror", (error) => pageErrors.push(error.message));
        page.on("response", (response) => {
          const url = new URL(response.url());
          if (response.status() >= 400)
            httpErrors.push({ path: url.pathname, status: response.status() });
          if (url.pathname === "/api/map-selections" && response.status() === 201)
            bodyTasks.push(
              response
                .json()
                .then((body) => {
                  selection = body;
                })
                .catch(() => {}),
            );
          if (url.pathname.includes("/coverage-tiles/") && response.status() === 200)
            bodyTasks.push(
              response
                .body()
                .then((buffer) =>
                  tiles.push({
                    bytes: buffer.length,
                    sha256: hash(buffer),
                    contentType: response.headers()["content-type"],
                  }),
                )
                .catch(() => {}),
            );
        });
        const cdp = await context.newCDPSession(page);
        try {
          await page.goto(`${origin}/map`, { waitUntil: "domcontentloaded" });
          await page
            .getByRole("button", { name: "Open account menu for Synthetic Acceptance Owner" })
            .waitFor();
          const patch = await page.evaluate(
            async ({ bearer, range, theme }) => {
              const session = await fetch("/api/session", {
                headers: { Authorization: `Bearer ${bearer}` },
              });
              const response = await fetch("/api/me/preferences", {
                method: "PATCH",
                headers: { Authorization: `Bearer ${bearer}`, "Content-Type": "application/json" },
                body: JSON.stringify({
                  theme,
                  dateRange: range,
                  coverageDiagnosticsEnabled: false,
                }),
              });
              return {
                sessionStatus: session.status,
                status: response.status,
                preferences: await response.json(),
              };
            },
            { bearer, range, theme },
          );
          check(
            testCase,
            "authenticated session and preferences PATCH before measured load",
            patch.sessionStatus === 200 &&
              patch.status === 200 &&
              patch.preferences.theme === theme &&
              patch.preferences.dateRange === range &&
              patch.preferences.coverageDiagnosticsEnabled === false,
            patch,
          );
          tiles.length = 0;
          await page.reload({ waitUntil: "domcontentloaded" });
          const routes = await ready(page, "routes");
          testCase.timings.initialUsableMapMs = routes.timeMs;
          testCase.timings.initialLongTasks = routes.longTasks;
          check(testCase, "initial usable rendered map within 3 seconds", routes.timeMs <= 3000, {
            ms: routes.timeMs,
            routeFeatures: routes.routeFeatures,
            baseFeatures: routes.baseFeatures,
          });
          await controls(page, true);
          const coverageButton = page
            .getByRole("button", { name: "Coverage", exact: true })
            .filter({ visible: true });
          const activated = await page.evaluate(() => performance.now());
          await coverageButton.click();
          await controls(page, false);
          const coverage = await ready(page, "coverage");
          testCase.timings.coverageUsableFromNavigationMs = coverage.timeMs;
          testCase.timings.coverageUsableFromActionMs = coverage.timeMs - activated;
          check(
            testCase,
            "coverage usable rendered map within 3 seconds of mode action",
            coverage.timeMs - activated <= 3000,
            {
              ms: coverage.timeMs - activated,
              includes:
                "Playwright click, UI update, mobile sheet dismissal, settled tiles and two animation frames",
            },
          );
          await Promise.all(bodyTasks);
          const unique = [
            ...new Map(
              coverage.coverage.map((properties) => [
                `${properties.entityKind}:${properties.entityId}`,
                properties,
              ]),
            ).values(),
          ];
          const oracle = sqlOracles[rangeName];
          check(
            testCase,
            "selection routed fixture size and range",
            selection?.workouts.length === oracle.routed_workouts &&
              selection?.range.startDate === range.split("/")[0] &&
              selection?.range.endDate === range.split("/")[1],
            {
              totalWorkouts: oracle.workouts,
              expectedRouteRows: oracle.routed_workouts,
              routeRows: selection?.workouts.length,
              range: selection?.range,
            },
          );
          check(
            testCase,
            "real loaded MVT with nonzero rendered coverage features",
            tiles.some((tile) => tile.bytes > 0) &&
              unique.length > 0 &&
              coverage.loaded &&
              coverage.tilesLoaded,
            {
              uniqueEntities: unique.length,
              tiles: tiles.length,
              nonemptyTiles: tiles.filter((tile) => tile.bytes > 0).length,
              tileBytes: tiles.reduce((sum, tile) => sum + tile.bytes, 0),
              tileChecksumsSha256: hash(JSON.stringify(tiles)),
              contentTypes: [...new Set(tiles.map((tile) => tile.contentType))],
            },
          );
          const mismatches = unique.filter((properties) => {
            const count = oracle.counts[`${properties.entityKind}:${properties.entityId}`];
            const bucket =
              count === 1
                ? 1
                : count === 2
                  ? 2
                  : count <= 5
                    ? 3
                    : count <= 10
                      ? 4
                      : count <= 25
                        ? 5
                        : 6;
            return count !== properties.rangeWorkoutCount || bucket !== properties.countBucket;
          });
          check(
            testCase,
            "rendered MVT exact counts and buckets match independent SQL",
            unique.length > 0 && mismatches.length === 0,
            {
              checked: unique.length,
              mismatches,
              observedBuckets: [...new Set(unique.map((feature) => feature.countBucket))].sort(),
            },
          );
          check(
            testCase,
            "six exact legend labels",
            JSON.stringify(await page.locator(".coverage-legend > div").allTextContents()) ===
              JSON.stringify(labels),
            labels,
          );
          await attempt(
            testCase,
            "workout readiness inspection, focus and mobile sheet dismissal",
            async () => {
              await controls(page, true);
              const rows = await page
                .locator(".map-workout-list:visible li")
                .evaluateAll((elements) =>
                  elements.map((element) => ({
                    id: element.dataset.workoutId,
                    label: element.querySelector(".visually-hidden")?.textContent,
                    disabled: element.querySelector(".map-route-focus").disabled,
                  })),
                );
              const expectedLabel = (readiness) =>
                readiness.resultStatus === "current"
                  ? "Coverage current"
                  : readiness.resultStatus === "stale"
                    ? "Coverage stale"
                    : readiness.mapDataStatus === "unavailable"
                      ? "Coverage unavailable"
                      : readiness.mapDataStatus === "pending" ||
                          ["queued", "running"].includes(readiness.processingStatus)
                        ? "Coverage pending"
                        : readiness.processingStatus === "failed"
                          ? "Coverage failed"
                          : "Coverage unprocessed";
              const mismatches = rows.filter((row) => {
                const workout = selection.workouts.find((workout) => workout.id === row.id);
                return (
                  !workout ||
                  row.disabled !== (workout.coverageReadiness.resultStatus === "none") ||
                  row.label !== expectedLabel(workout.coverageReadiness)
                );
              });
              check(
                testCase,
                "row readiness and missing-data disabled focus",
                rows.length === oracle.routed_workouts &&
                  (!readinessOverlay || rows.some((row) => row.disabled)) &&
                  !mismatches.length,
                {
                  rows: rows.length,
                  expectedRouteRows: oracle.routed_workouts,
                  readinessOverlay,
                  disabled: rows.filter((row) => row.disabled).length,
                  statuses: [...new Set(rows.map((row) => row.label))],
                  mismatches: mismatches.slice(0, 10),
                },
              );
              await screenshot(page, `${name}-readiness`, cdp);
              const focusRow = page
                .locator(".map-workout-list:visible .map-route-focus:not(:disabled)")
                .first();
              const focusStarted = await page.evaluate(() => performance.now());
              await focusRow.click();
              try {
                await controls(page, false);
              } finally {
                testCase.timings.focusAndDismissMs = await page.evaluate(
                  (start) => performance.now() - start,
                  focusStarted,
                );
              }
              check(testCase, "workout focus and mobile sheet dismissal completed", true, {
                ms: testCase.timings.focusAndDismissMs,
              });
            },
          );
          await attempt(
            testCase,
            "focused magenta overlay and Space aggregate-only hiding",
            async () => {
              await page.waitForFunction(
                () => window.__coverageAcceptance.state()?.focusFeatures > 0,
                null,
                { timeout: 15000 },
              );
              const before = await ready(page, "coverage");
              check(
                testCase,
                "focused magenta overlay",
                before.focusFeatures > 0 && JSON.stringify(before.focusColor).includes("#ff008c"),
                { features: before.focusFeatures, colorExpression: before.focusColor },
              );
              await screenshot(page, `${name}-focused`, cdp);
              await page.locator(".maplibregl-canvas").focus();
              await page.keyboard.down("Space");
              await page.waitForFunction(
                () => window.__coverageAcceptance.state()?.aggregateOpacity === 0,
              );
              const hidden = await page.evaluate(() => window.__coverageAcceptance.state());
              await screenshot(page, `${name}-space`, cdp);
              await page.keyboard.up("Space");
              await page.waitForFunction(
                () => window.__coverageAcceptance.state()?.aggregateOpacity > 0,
              );
              check(
                testCase,
                "Space hides aggregate only and preserves focused overlay/base context",
                hidden.aggregateOpacity === 0 &&
                  hidden.focusOpacity > 0 &&
                  hidden.focusFeatures > 0 &&
                  hidden.baseFeatures > 0 &&
                  hidden.canvasMounted,
                {
                  aggregateOpacity: hidden.aggregateOpacity,
                  focusOpacity: hidden.focusOpacity,
                  focusFeatures: hidden.focusFeatures,
                  baseFeatures: hidden.baseFeatures,
                },
              );
            },
          );
          await attempt(testCase, "hover exact SQL count", async () => {
            const points = await page.evaluate(() => {
              const map = window.__coverageAcceptance.getMap(),
                rect = map.getCanvas().getBoundingClientRect();
              const features = map.queryRenderedFeatures({ layers: ["private-workout-coverage"] });
              const points = [];
              for (const feature of features) {
                const lines =
                  feature.geometry.type === "LineString"
                    ? [feature.geometry.coordinates]
                    : feature.geometry.coordinates;
                for (const line of lines)
                  for (let i = 1; i < line.length; i++) {
                    const p = map.project([
                      (line[i - 1][0] + line[i][0]) / 2,
                      (line[i - 1][1] + line[i][1]) / 2,
                    ]);
                    const x = p.x + rect.x,
                      y = p.y + rect.y;
                    if (
                      p.x > 20 &&
                      p.y > 20 &&
                      p.x < rect.width - 20 &&
                      p.y < rect.height - 20 &&
                      document.elementFromPoint(x, y) === map.getCanvas()
                    )
                      points.push({ x, y });
                  }
              }
              return points.slice(0, 10);
            });
            let detail;
            for (const point of points) {
              const responsePromise = page
                .waitForResponse(
                  (response) =>
                    /\/coverage\/(path|park)\//.test(response.url()) && response.status() === 200,
                  { timeout: 2500 },
                )
                .catch(() => null);
              await page.mouse.move(point.x, point.y);
              const response = await responsePromise;
              if (response) {
                detail = await response.json();
                break;
              }
            }
            if (!detail)
              throw new Error(`No hover detail after ${points.length} real mouse locations`);
            await page.locator(".map-coverage-tooltip").waitFor();
            const texts = await page
              .locator(".map-coverage-tooltip")
              .evaluate((element) => [...element.children].map((child) => child.textContent));
            const expected = oracle.counts[`${detail.entityKind}:${detail.entityId}`];
            check(
              testCase,
              "hover exact SQL count",
              detail.rangeWorkoutCount === expected &&
                texts[texts.indexOf("Workouts") + 1] === expected.toLocaleString("en-US"),
              { expected, responseCount: detail.rangeWorkoutCount, popup: texts },
            );
            await screenshot(page, `${name}-hover`, cdp);
            await page.mouse.move(0, 0);
          });
          await attempt(
            testCase,
            "RoadCoverageDialog keyboard and Escape return to mounted canvas",
            async () => {
              const open = page.getByRole("button", { name: "Coverage by road..." });
              await page.evaluate(() => {
                window.__coverageAcceptance.dialogMap = window.__coverageAcceptance.getMap();
              });
              await open.focus();
              await page.keyboard.press("Enter");
              await page
                .locator('.road-coverage-table-wrap[aria-busy="false"]')
                .waitFor({ timeout: 20000 });
              const layout = await overflow(page);
              await screenshot(page, `${name}-road-dialog`, cdp);
              check(
                testCase,
                "RoadCoverageDialog contained overflow",
                !layout.pageOverflow && layout.dialogInViewport,
                layout,
              );
              await page.keyboard.press("Tab");
              const focusInside = await page
                .getByRole("dialog", { name: "Road Coverage" })
                .evaluate((dialog) => dialog.contains(document.activeElement));
              await page.keyboard.press("Escape");
              await page
                .getByRole("dialog", { name: "Road Coverage" })
                .waitFor({ state: "hidden" });
              await page.waitForFunction(() => window.__coverageAcceptance.state()?.canvasFocused);
              const state = await page.evaluate(() => window.__coverageAcceptance.state());
              const sameMap = await page.evaluate(
                () =>
                  window.__coverageAcceptance.dialogMap === window.__coverageAcceptance.getMap(),
              );
              check(
                testCase,
                "RoadCoverageDialog keyboard and Escape return to mounted canvas",
                focusInside &&
                  sameMap &&
                  state.canvasFocused &&
                  state.canvasMounted &&
                  state.baseFeatures > 0,
                {
                  focusInside,
                  sameMap,
                  canvasFocused: state.canvasFocused,
                  baseFeatures: state.baseFeatures,
                },
              );
            },
          );
          const layout = await overflow(page);
          check(
            testCase,
            "desktop/mobile no page overflow and base context survives",
            !layout.pageOverflow &&
              layout.canvasInViewport &&
              (await page.evaluate(() => window.__coverageAcceptance.state())).baseFeatures > 0,
            layout,
          );
          testCase.timings.panZoom = await measureInteraction(page);
          await ready(page, "coverage");
          for (const vision of visions) {
            const visualCase = {
              name: `${name}-${vision === "none" ? "normal" : vision}`,
              kind: "vision",
              checks: [],
            };
            result.cases.push(visualCase);
            await attempt(
              visualCase,
              "CDP color vision emulation and synthetic screenshot",
              async () => {
                const artifact = await screenshot(page, visualCase.name, cdp, vision);
                const state = await page.evaluate(() => window.__coverageAcceptance.state());
                check(
                  visualCase,
                  "CDP color vision emulation and synthetic screenshot",
                  state.coverage.length > 0 && state.focusFeatures > 0 && state.baseFeatures > 0,
                  {
                    artifact: artifact.path,
                    sha256: artifact.sha256,
                    coverageFeatures: state.coverage.length,
                    focusFeatures: state.focusFeatures,
                    baseFeatures: state.baseFeatures,
                  },
                );
                visualCase.review = "PENDING_MANUAL_REVIEW";
              },
            );
          }
          await cdp.send("Emulation.setEmulatedVisionDeficiency", { type: "none" });
          if (viewportName === "desktop")
            for (const [layoutName, size, scale] of [
              ["reflow-320", { width: 320, height: 844 }, 1],
              ["zoom-200", { width: 720, height: 450 }, 2],
            ]) {
              const layoutCase = {
                name: `${rangeName}-${theme}-${layoutName}`,
                kind: "layout",
                checks: [],
              };
              result.cases.push(layoutCase);
              await attempt(layoutCase, "reflow map, controls and dialog", async () => {
                await page.setViewportSize(size);
                await cdp.send("Emulation.setDeviceMetricsOverride", {
                  width: size.width,
                  height: size.height,
                  deviceScaleFactor: scale,
                  mobile: false,
                });
                await page.evaluate(() => window.__coverageAcceptance.getMap().resize());
                await ready(page, "coverage");
                const layout = await overflow(page);
                const state = await page.evaluate(() => window.__coverageAcceptance.state());
                check(
                  layoutCase,
                  "map reflow and base context",
                  layout.viewport === size.width &&
                    !layout.pageOverflow &&
                    layout.canvasInViewport &&
                    state.baseFeatures > 0 &&
                    state.focusFeatures > 0,
                  {
                    layout,
                    baseFeatures: state.baseFeatures,
                    focusFeatures: state.focusFeatures,
                    scale,
                  },
                );
                await screenshot(page, layoutCase.name, cdp);
                await controls(page, true);
                const sheetLayout = await overflow(page);
                check(
                  layoutCase,
                  "controls contained in viewport",
                  !sheetLayout.pageOverflow && sheetLayout.dialogInViewport,
                  sheetLayout,
                );
                await screenshot(page, `${layoutCase.name}-controls`, cdp);
                await controls(page, false);
                await page.getByRole("button", { name: "Coverage by road..." }).click();
                await page
                  .locator('.road-coverage-table-wrap[aria-busy="false"]')
                  .waitFor({ timeout: 20000 });
                const dialogLayout = await overflow(page);
                check(
                  layoutCase,
                  "road dialog contained reflow",
                  !dialogLayout.pageOverflow && dialogLayout.dialogInViewport,
                  dialogLayout,
                );
                await screenshot(page, `${layoutCase.name}-dialog`, cdp);
                await page.keyboard.press("Escape");
              });
              await cdp.send("Emulation.clearDeviceMetricsOverride");
              await page.setViewportSize(viewport);
              await page.evaluate(() => window.__coverageAcceptance.getMap().resize());
            }
        } catch (error) {
          check(testCase, "case completed", false, error.message);
          await screenshot(page, `${name}-failure`, cdp).catch(() => {});
        } finally {
          await Promise.all(bodyTasks);
          check(
            testCase,
            "fixture-only network and no page exceptions",
            blocked.length === 0 && pageErrors.length === 0,
            { blocked, pageErrors },
          );
          testCase.httpErrors = httpErrors;
          if (httpErrors.length)
            result.warnings.push(`${name}: ${httpErrors.length} HTTP errors; see case metadata.`);
          await context.close();
          for (const item of result.cases)
            item.status = item.checks.some((entry) => entry.status === "FAIL") ? "FAIL" : "PASS";
          console.log(
            `${name}: ${testCase.status}; initial=${testCase.timings.initialUsableMapMs?.toFixed(1) ?? "unmeasured"}ms; screenshots=${result.artifacts.length}`,
          );
        }
      }
} catch (error) {
  result.fatalError = error.message;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (forward) {
    forward.kill("SIGTERM");
    await new Promise((resolve) => {
      if (forward.exitCode !== null || forward.signalCode !== null) resolve();
      else {
        forward.once("exit", resolve);
        setTimeout(resolve, 5000).unref();
      }
    });
  }
  result.recorded_at = new Date().toISOString();
  result.counts = {
    cases: result.cases.length,
    passed: result.cases.filter((item) => item.status === "PASS").length,
    failed: result.cases.filter((item) => item.status === "FAIL").length,
    checksPassed: result.cases
      .flatMap((item) => item.checks)
      .filter((item) => item.status === "PASS").length,
    checksFailed: result.cases
      .flatMap((item) => item.checks)
      .filter((item) => item.status === "FAIL").length,
    screenshots: result.artifacts.length,
    pendingVisualReviews: result.artifacts.filter((item) => item.review === "PENDING_MANUAL_REVIEW")
      .length,
  };
  result.status =
    result.fatalError || result.counts.failed || result.counts.cases !== 56
      ? "FAIL"
      : "PENDING_MANUAL_REVIEW";
  if (profile.requireProfile().metadata.uid !== namespace.metadata.uid)
    throw new Error("namespace changed during visual capture");
  await writeFile(resultsPath, `${JSON.stringify(result, null, 2)}\n`);
  console.log(
    JSON.stringify({
      status: result.status,
      ...result.counts,
      fatalError: result.fatalError,
      artifacts: artifactDir,
    }),
  );
  process.exitCode = result.status === "FAIL" ? 1 : 0;
}
