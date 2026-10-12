import { chromium } from "@playwright/test";
import { createHash } from "node:crypto";
import { execFileSync, spawn } from "node:child_process";
import { writeFile } from "node:fs/promises";
import profile from "../../deploy/acceptance/profile.cjs";
import evidence from "../../deploy/acceptance/evidence.cjs";
import observation from "./coverage-viewport-observation.cjs";

const origin =
  process.env.COVERAGE_ACCEPTANCE_UI_URL || "https://acceptance.workouts.xdev.fourteeners.local";
if (
  process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true" ||
  origin !== "https://acceptance.workouts.xdev.fourteeners.local"
)
  throw Error("explicit disposable switch and exact synthetic fixture origin required");
const namespace = profile.requireProfile();
const started = new Date();
const binding = {
  ...profile.binding,
  schema_version: evidence.SCHEMA,
  namespace: evidence.NAMESPACE,
  namespace_uid: namespace.metadata.uid,
};
const output = profile.artifact("viewport-browser.json");
const receipt = profile.artifact(
  `viewport-browser-${namespace.metadata.uid}-${started.getTime()}.json`,
);
const bearer = createHash("sha256")
  .update("FAKE disposable synthetic coverage acceptance bearer; never a live credential")
  .digest("base64url");
const target = {
  width: 3095,
  height: 1973,
  tile_size: 512,
  center: [0.0195, 0.0195],
  zoom: 14,
  bearing: 0,
  pitch: 0,
};
const result = {
  ...binding,
  started_at: started.toISOString(),
  status: "RUNNING",
  account_count: 1,
  scope:
    "Single disposable public synthetic account; real MapLibre source-loader observation, not product render acceptance.",
  frame: target,
  generation_parameters: structuredClone(profile.manifest),
  provenance: {
    ...profile.binding,
    kind: "captured_browser_source_loader",
    namespace_uid: namespace.metadata.uid,
  },
  initial_map_target: "NOT_MEASURED",
  cases: [],
  errors: [],
  limitations: [
    "One cold source capability per mode; server, database and OS caches are not flushed.",
    "Test-only React ref traversal obtains the existing map; public MapLibre methods replace the source and preserve its layers without changing product code.",
    "Source load and idle observations are not time-to-usable-render; no 3-second product render pass is inferred.",
    "Remote shared Chromium scheduling/software rendering and a single run are not a production benchmark.",
  ],
};

function installProbe() {
  const getMap = () => {
    const element = document.querySelector(".map-canvas");
    let fiber = element?.[Object.keys(element).find((key) => key.startsWith("__reactFiber$"))];
    for (; fiber; fiber = fiber.return)
      for (let hook = fiber.memoizedState, i = 0; hook && i < 160; hook = hook.next, i++) {
        const map = hook.memoizedState?.current;
        if (map?.getCanvas && map?.getStyle && map?.jumpTo) return map;
      }
  };
  window.__viewportAcceptance = { getMap };
}

async function quiet(page) {
  await page.waitForFunction(
    () => {
      const map = window.__viewportAcceptance.getMap();
      return map?.loaded() && map.areTilesLoaded() && !map.isMoving();
    },
    null,
    { timeout: 60000 },
  );
  await page.evaluate(
    () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
  );
}

let browser, forward, context, browserCDP, pageCDP;
const cdpRequests = new Map(),
  cdpSessions = new Map(),
  cdpTargets = new Set();
const cdpObserver = {
  status: "NOT_CAPTURED",
  errors: [],
  target_types: {},
  flatten: true,
  wait_for_debugger: false,
};
let cdpMessageID = 0;
function networkEvent(method, event, session) {
  const key = `${session}:${event.requestId}`;
  if (method === "Network.requestWillBeSent") {
    const url = new URL(event.request.url);
    if (
      url.origin !== origin ||
      !/^\/api\/map-selections\/[^/]+\/(route|coverage)-tiles\//.test(url.pathname)
    )
      return;
    cdpRequests.set(key, {
      path: url.pathname,
      started_at: event.timestamp,
      target_type: cdpSessions.get(session) || "page",
    });
  } else {
    const row = cdpRequests.get(key);
    if (!row) return;
    if (method === "Network.responseReceived")
      Object.assign(row, {
        protocol: event.response.protocol,
        status: event.response.status,
        connection_id: event.response.connectionId,
        response_at: event.timestamp,
        from_disk_cache: event.response.fromDiskCache,
        from_service_worker: event.response.fromServiceWorker,
      });
    if (method === "Network.loadingFinished")
      Object.assign(row, {
        finished_at: event.timestamp,
        encoded_data_length: event.encodedDataLength,
      });
    if (method === "Network.loadingFailed")
      Object.assign(row, { finished_at: event.timestamp, error: event.errorText });
  }
}
try {
  const sql =
    "SELECT count(*) FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id WHERE p.username='acceptance-fixture'";
  const accounts = Number(
    execFileSync(
      "kubectl",
      [
        "--context",
        "xdev",
        "-n",
        evidence.NAMESPACE,
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
      ],
      { encoding: "utf8", timeout: 30000 },
    ).trim(),
  );
  if (accounts !== 1) throw Error("expected exactly one synthetic fixture account");
  let endpoint = process.env.COVERAGE_ACCEPTANCE_PLAYWRIGHT_WS;
  if (endpoint && !/^ws:\/\/(127\.0\.0\.1|localhost):19123\/$/.test(endpoint))
    throw Error("non-allowlisted browser endpoint");
  if (!endpoint) {
    forward = spawn(
      "kubectl",
      ["--context", "homelab", "-n", "playwright", "port-forward", "svc/playwright", "19123:3000"],
      { stdio: ["ignore", "pipe", "pipe"] },
    );
    await new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(Error("browser port-forward readiness timeout")),
        20000,
      );
      const fail = (error) => {
        clearTimeout(timer);
        reject(error);
      };
      forward.once("error", fail);
      forward.once("exit", (code) => fail(Error(`browser port-forward exited ${code}`)));
      forward.stdout.on("data", (data) => {
        if (data.toString().includes("Forwarding from")) {
          clearTimeout(timer);
          resolve();
        }
      });
      forward.stderr.on("data", (data) => process.stderr.write(data));
    });
    endpoint = "ws://127.0.0.1:19123/";
  }
  browser = await chromium.connect(endpoint, { timeout: 30000 });
  result.browser = { version: browser.version(), client: "1.62.0", remote: true };
  context = await browser.newContext({
    viewport: { width: 3431, height: 2049 },
    deviceScaleFactor: 1,
  });
  await context.route("**/*", async (route) => {
    const request = route.request(),
      url = new URL(request.url());
    if (!["http:", "https:"].includes(url.protocol)) return route.continue();
    if (url.origin !== origin) {
      result.errors.push({ kind: "blocked_origin", origin: url.origin, path: url.pathname });
      return route.abort("blockedbyclient");
    }
    // Never attach the fixture bearer to a third-party origin or static asset.
    const headers = { ...request.headers() };
    if (url.pathname.startsWith("/api/")) headers.authorization = `Bearer ${bearer}`;
    return route.continue({ headers });
  });
  await context.addInitScript({
    content: `(${installProbe.toString()})(); window.__viewportAcceptance.isTerminalTileEvent = (${observation.isTerminalTileEvent.toString()});`,
  });
  const page = await context.newPage();
  const privateRequests = new Set();
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      url.origin === origin &&
      /^\/api\/map-selections\/[^/]+\/(route|coverage)-tiles\//.test(url.pathname)
    )
      privateRequests.add(request);
  });
  page.on("requestfinished", (request) => privateRequests.delete(request));
  page.on("requestfailed", (request) => privateRequests.delete(request));
  try {
    pageCDP = await context.newCDPSession(page);
    const { targetInfo: ownTarget } = await pageCDP.send("Target.getTargetInfo");
    if (!ownTarget.browserContextId)
      throw Error("CDP cannot identify the runner-owned browser context");
    browserCDP = await browser.newBrowserCDPSession();
    const observeTarget = async (targetInfo) => {
      if (
        targetInfo.browserContextId !== ownTarget.browserContextId ||
        !["worker", "shared_worker", "service_worker"].includes(targetInfo.type) ||
        cdpTargets.has(targetInfo.targetId)
      )
        return;
      cdpTargets.add(targetInfo.targetId);
      try {
        // Playwright's public CDPSession cannot route flattened session commands.
        // A second, nonflattened observer attaches only our context's workers.
        const { sessionId } = await browserCDP.send("Target.attachToTarget", {
          targetId: targetInfo.targetId,
          flatten: false,
        });
        cdpSessions.set(sessionId, targetInfo.type);
        cdpObserver.target_types[targetInfo.type] =
          (cdpObserver.target_types[targetInfo.type] || 0) + 1;
        await browserCDP.send("Target.sendMessageToTarget", {
          sessionId,
          message: JSON.stringify({ id: ++cdpMessageID, method: "Network.enable", params: {} }),
        });
      } catch (error) {
        cdpObserver.errors.push(error.message);
      }
    };
    browserCDP.on("Target.targetCreated", ({ targetInfo }) => {
      void observeTarget(targetInfo);
    });
    browserCDP.on("Target.attachedToTarget", ({ targetInfo }) => {
      void observeTarget(targetInfo);
    });
    browserCDP.on("Target.receivedMessageFromTarget", ({ sessionId, message }) => {
      if (!cdpSessions.has(sessionId)) return;
      const event = JSON.parse(message);
      if (event.error) cdpObserver.errors.push(event.error.message);
      if (event.method?.startsWith("Network.")) networkEvent(event.method, event.params, sessionId);
    });
    await browserCDP.send("Target.setDiscoverTargets", { discover: true });
    await browserCDP.send("Target.setAutoAttach", {
      autoAttach: true,
      waitForDebuggerOnStart: false,
      flatten: true,
    });
    await pageCDP.send("Network.enable");
    for (const method of [
      "Network.requestWillBeSent",
      "Network.responseReceived",
      "Network.loadingFinished",
      "Network.loadingFailed",
    ])
      pageCDP.on(method, (event) => networkEvent(method, event, "page"));
    const { targetInfos } = await browserCDP.send("Target.getTargets");
    await Promise.all(targetInfos.map(observeTarget));
  } catch (error) {
    cdpObserver.errors.push(error.message);
  }
  page.on("pageerror", (error) =>
    result.errors.push({ kind: "pageerror", message: error.message }),
  );
  await page.goto(`${origin}/map`, { waitUntil: "domcontentloaded" });
  await page
    .getByRole("button", { name: "Open account menu for Synthetic Acceptance Owner" })
    .waitFor();
  const range = `${profile.manifest.year}-01-01/${profile.manifest.year}-12-31`;
  const patch = await page.evaluate(
    async ({ range }) => {
      const response = await fetch("/api/me/preferences", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ dateRange: range, coverageDiagnosticsEnabled: false }),
      });
      return { status: response.status, body: await response.json() };
    },
    { range },
  );
  if (
    patch.status !== 200 ||
    patch.body.dateRange !== range ||
    patch.body.coverageDiagnosticsEnabled !== false
  )
    throw Error("fixture preference setup failed");
  await page.reload({ waitUntil: "domcontentloaded" });
  await quiet(page);
  await page.getByRole("button", { name: "Routes", exact: true }).filter({ visible: true }).click();

  // Sidebar/header dimensions are observed, not assumed from the page viewport.
  for (let attempt = 0; attempt < 6; attempt++) {
    const rect = await page.evaluate(() => {
      const rect = window.__viewportAcceptance.getMap().getCanvas().getBoundingClientRect();
      return { width: rect.width, height: rect.height };
    });
    if (Math.abs(rect.width - target.width) <= 1 && Math.abs(rect.height - target.height) <= 1)
      break;
    const size = page.viewportSize();
    await page.setViewportSize({
      width: Math.round(size.width + target.width - rect.width),
      height: Math.round(size.height + target.height - rect.height),
    });
    await page.evaluate(() => window.__viewportAcceptance.getMap().resize());
  }
  result.actual_canvas_css = await page.evaluate(() => {
    const rect = window.__viewportAcceptance.getMap().getCanvas().getBoundingClientRect();
    return {
      width: rect.width,
      height: rect.height,
      device_pixel_ratio: devicePixelRatio,
      page_width: innerWidth,
      page_height: innerHeight,
    };
  });
  if (
    Math.abs(result.actual_canvas_css.width - target.width) > 1 ||
    Math.abs(result.actual_canvas_css.height - target.height) > 1
  )
    throw Error("canvas CSS dimensions do not match required frame");

  for (const mode of ["route", "coverage"]) {
    const item = {
      ...binding,
      source: mode,
      period: "year",
      started_at: new Date().toISOString(),
      status: "RUNNING",
      requests: [],
      source_events: [],
      interference: [],
      errors: [],
    };
    result.cases.push(item);
    const bodyTasks = [],
      pending = new Map();
    let selectionID, captureStarted, prefix;
    const sourceID = mode === "route" ? "private-workout-routes" : "private-workout-coverage";
    await page.evaluate(() => {
      const probe = window.__viewportAcceptance;
      probe.events = [];
      probe.sourceLoadedMs = null;
      probe.idleMs = null;
      probe.listener = null;
    });
    const requestStarted = (request) => {
      const url = new URL(request.url());
      if (!captureStarted || url.origin !== origin) return;
      if (!url.pathname.toLowerCase().startsWith(prefix)) {
        if (/^\/api\/map-selections\/[^/]+\/(route|coverage)-tiles\//.test(url.pathname))
          item.interference.push({
            kind: "other_capability_request",
            path: url.pathname,
            started_ms: performance.now() - captureStarted,
          });
        return;
      }
      const tile = url.pathname.match(/\/(\d+)\/(\d+)\/(\d+)\.pbf$/);
      const row = {
        tile_id: tile ? tile.slice(1).join("/") : null,
        path: url.pathname,
        started_ms: performance.now() - captureStarted,
      };
      item.requests.push(row);
      pending.set(request, row);
      item.peak_in_flight = Math.max(item.peak_in_flight || 0, pending.size);
    };
    const responseReceived = (response) => {
      const row = pending.get(response.request());
      if (!row) return;
      row.status = response.status();
      row.response_ms = performance.now() - captureStarted;
      bodyTasks.push(
        response
          .body()
          .then((body) => {
            row.bytes = body.length;
            row.sha256 = createHash("sha256").update(body).digest("hex");
          })
          .catch((error) => {
            row.body_error = error.message;
          }),
      );
    };
    const finished = (request) => {
      const row = pending.get(request);
      if (!row) return;
      row.finished_ms = performance.now() - captureStarted;
      row.duration_ms = row.finished_ms - row.started_ms;
      row.failure = request.failure()?.errorText ?? null;
      pending.delete(request);
    };
    page.on("request", requestStarted);
    page.on("response", responseReceived);
    page.on("requestfinished", finished);
    page.on("requestfailed", finished);
    try {
      if (mode === "coverage")
        await page
          .getByRole("button", { name: "Coverage", exact: true })
          .filter({ visible: true })
          .click();
      await quiet(page);
      await page.evaluate(
        (target) =>
          window.__viewportAcceptance.getMap().jumpTo({
            center: target.center,
            zoom: target.zoom,
            bearing: target.bearing,
            pitch: target.pitch,
          }),
        target,
      );
      await quiet(page);
      const drainDeadline = Date.now() + 60000;
      while (privateRequests.size && Date.now() < drainDeadline)
        await new Promise((resolve) => setTimeout(resolve, 25));
      if (privateRequests.size)
        throw Error("previous private requests did not terminate before capture");
      const selection = await page.evaluate(
        async ({ year }) => {
          const response = await fetch("/api/map-selections", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ startDate: `${year}-01-01`, endDate: `${year}-12-31` }),
          });
          return { status: response.status, body: await response.json() };
        },
        { year: profile.manifest.year },
      );
      if (selection.status !== 201 || selection.body.workouts.length !== profile.routed)
        throw Error("fresh year selection failed fixture cardinality check");
      selectionID = selection.body.id;
      item.selection = {
        id: selectionID,
        generation: selection.body.dataGeneration,
        routed_workouts: selection.body.workouts.length,
        range: selection.body.range,
      };
      const template = new URL(
        mode === "route" ? selection.body.routeTileUrl : selection.body.coverageTileUrl,
        origin,
      );
      if (template.origin !== origin) throw Error("source tile template escaped fixture origin");
      prefix = `/api/map-selections/${selectionID}/${mode}-tiles/`.toLowerCase();
      captureStarted = performance.now();
      await page.evaluate(
        ({ sourceID, template, target, prefix }) => {
          const map = window.__viewportAcceptance.getMap();
          const probe = window.__viewportAcceptance;
          const style = map.getStyle();
          const source = structuredClone(style.sources[sourceID]);
          const layers = style.layers
            .filter((layer) => layer.source === sourceID)
            .map((layer) => structuredClone(layer));
          if (source?.type !== "vector" || !layers.length)
            throw Error("expected mounted vector source and layers");
          // Destroy the old SourceCache before listening: setTiles reloads cached
          // offscreen zoom levels as well as the current camera's ideal tiles.
          for (const layer of [...layers].reverse()) map.removeLayer(layer.id);
          map.removeSource(sourceID);
          probe.events = [];
          probe.started = performance.now();
          probe.sourceLoadedMs = null;
          probe.idleMs = null;
          probe.frameChanges = [];
          probe.interference = [];
          probe.frameListener = () => {
            const center = map.getCenter(),
              rect = map.getCanvas().getBoundingClientRect();
            if (
              Math.abs(rect.width - target.width) > 1 ||
              Math.abs(rect.height - target.height) > 1 ||
              Math.abs(center.lng - target.center[0]) > 0.000001 ||
              Math.abs(center.lat - target.center[1]) > 0.000001 ||
              Math.abs(map.getZoom() - target.zoom) > 0.000001 ||
              Math.abs(map.getBearing()) > 0.000001 ||
              Math.abs(map.getPitch()) > 0.000001
            )
              probe.frameChanges.push({
                time_ms: performance.now() - probe.started,
                center: [center.lng, center.lat],
                zoom: map.getZoom(),
                bearing: map.getBearing(),
                pitch: map.getPitch(),
                width: rect.width,
                height: rect.height,
              });
            const source = map.getStyle()?.sources[sourceID];
            if (source && (source.tiles?.length !== 1 || source.tiles[0] !== template))
              probe.interference.push({
                type: "source_template_changed",
                time_ms: performance.now() - probe.started,
              });
          };
          probe.listener = (event) => {
            if (event.sourceId !== sourceID) return;
            if (
              event.source?.tiles &&
              (event.source.tiles.length !== 1 || event.source.tiles[0] !== template)
            )
              probe.interference.push({
                type: "old_source_event",
                time_ms: performance.now() - probe.started,
              });
            const tile = event.coord?.canonical;
            probe.events.push({
              type: event.type,
              source_data_type: event.sourceDataType,
              time_ms: performance.now() - probe.started,
              tile_id: tile ? `${tile.z}/${tile.x}/${tile.y}` : null,
              source_loaded: event.isSourceLoaded ?? null,
            });
            if (probe.isTerminalTileEvent(event, sourceID))
              probe.sourceLoadedMs = performance.now() - probe.started;
          };
          probe.idleListener = () => {
            probe.idleMs = performance.now() - probe.started;
          };
          probe.errorListener = (event) => {
            const url = event.error?.url || event.url || event.tile?.request?.url;
            const scoped =
              typeof url === "string" &&
              new URL(url, location.origin).pathname.toLowerCase().startsWith(prefix);
            if (scoped)
              probe.events.push({
                type: "error",
                time_ms: performance.now() - probe.started,
                message: event.error?.message,
                path: new URL(url, location.origin).pathname,
              });
            else
              probe.interference.push({
                type: "unscoped_source_error",
                source_id: event.sourceId ?? null,
                url: url ?? null,
                message: event.error?.message ?? null,
              });
          };
          map.on("sourcedataloading", probe.listener);
          map.on("sourcedata", probe.listener);
          map.on("idle", probe.idleListener);
          map.on("error", probe.errorListener);
          for (const event of ["render", "move", "resize", "sourcedata"])
            map.on(event, probe.frameListener);
          probe.frameListener();
          delete source.url;
          source.tiles = [template];
          map.addSource(sourceID, source);
          for (const layer of layers) {
            const index = style.layers.findIndex((original) => original.id === layer.id);
            const following = style.layers
              .slice(index + 1)
              .find((original) => map.getLayer(original.id));
            map.addLayer(layer, following?.id);
          }
        },
        {
          sourceID,
          template: template.href.replace(/%7B/g, "{").replace(/%7D/g, "}"),
          target,
          prefix,
        },
      );
      await page.waitForFunction(
        (sourceID) => {
          const probe = window.__viewportAcceptance,
            map = probe.getMap();
          return (
            probe.sourceLoadedMs !== null &&
            probe.idleMs !== null &&
            map.isSourceLoaded(sourceID) &&
            !map.isMoving()
          );
        },
        sourceID,
        { timeout: 60000 },
      );
      await quiet(page);
    } catch (error) {
      item.errors.push({ kind: "capture", message: error.message });
    } finally {
      const observed = await page.evaluate((sourceID) => {
        const probe = window.__viewportAcceptance,
          map = probe.getMap();
        if (probe.listener) {
          map.off("sourcedataloading", probe.listener);
          map.off("sourcedata", probe.listener);
          map.off("idle", probe.idleListener);
          map.off("error", probe.errorListener);
          for (const event of ["render", "move", "resize", "sourcedata"])
            map.off(event, probe.frameListener);
        }
        const center = map.getCenter(),
          rect = map.getCanvas().getBoundingClientRect();
        return {
          source_events: probe.events || [],
          source_loaded_ms: probe.sourceLoadedMs ?? null,
          idle_ms: probe.idleMs ?? null,
          source_loaded: Boolean(map.getSource(sourceID)) && map.isSourceLoaded(sourceID),
          camera: {
            center: [center.lng, center.lat],
            zoom: map.getZoom(),
            bearing: map.getBearing(),
            pitch: map.getPitch(),
          },
          canvas_css: { width: rect.width, height: rect.height },
          frame_changes: probe.frameChanges || [],
          source_interference: probe.interference || [],
        };
      }, sourceID);
      Object.assign(item, observed);
      item.interference.push(...item.source_interference);
      await Promise.all(bodyTasks);
      Object.assign(item, observation.assessBrowserCapture(item, pending.size, target));
      const cdpRows = [...cdpRequests.values()].filter((row) =>
        row.path.toLowerCase().startsWith(prefix || "!"),
      );
      const protocolCounts = {};
      for (const row of cdpRows)
        protocolCounts[row.protocol || "NOT_CAPTURED"] =
          (protocolCounts[row.protocol || "NOT_CAPTURED"] || 0) + 1;
      for (const row of item.requests) {
        const observed = cdpRows.find((record) => record.path === row.path);
        row.cdp_protocol = observed?.protocol ?? null;
        row.cdp_connection_id = observed?.connection_id ?? null;
      }
      item.cdp = {
        status:
          item.requests.length && item.requests.every((row) => row.cdp_protocol)
            ? "RECORDED"
            : "NOT_CAPTURED",
        protocol_counts: protocolCounts,
        requests: cdpRows,
        scope:
          "Actual CDP Network observations from this page and its context's worker targets; no protocol inferred from URL or configuration.",
      };
      item.recorded_at = new Date().toISOString();
      page.off("request", requestStarted);
      page.off("response", responseReceived);
      page.off("requestfinished", finished);
      page.off("requestfailed", finished);
      if (selectionID)
        await page
          .evaluate(async (id) => {
            await fetch(`/api/map-selections/${encodeURIComponent(id)}`, { method: "DELETE" });
          }, selectionID)
          .catch(() => {});
    }
  }
} catch (error) {
  result.errors.push({ kind: "fatal", message: error.message });
} finally {
  result.cdp_observer = {
    ...cdpObserver,
    status:
      result.cases.length === 2 && result.cases.every((item) => item.cdp?.status === "RECORDED")
        ? "RECORDED"
        : "NOT_CAPTURED",
  };
  if (browserCDP) await browserCDP.detach().catch(() => {});
  if (pageCDP) await pageCDP.detach().catch(() => {});
  if (context) await context.close().catch(() => {});
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
  result.status =
    result.errors.length ||
    result.cases.length !== 2 ||
    result.cases.some((item) => item.status === "FAIL")
      ? "FAIL"
      : result.cases.some((item) => !item.comparison_valid)
        ? "NOT_COMPARABLE"
        : "RECORDED_NO_ACCEPTANCE_THRESHOLD";
  result.capture_success =
    result.errors.length === 0 &&
    result.cases.length === 2 &&
    result.cases.every((item) => item.capture_success);
  result.comparison_valid =
    result.cases.length === 2 && result.cases.every((item) => item.comparison_valid);
  if (profile.requireProfile().metadata.uid !== namespace.metadata.uid)
    throw Error("namespace changed during browser capture");
  const data = `${JSON.stringify(result, null, 2)}\n`;
  await writeFile(receipt, data, { flag: "wx" });
  await writeFile(output, data);
  console.log(
    JSON.stringify({
      status: result.status,
      artifact: output,
      receipt,
      initial_map_target: result.initial_map_target,
    }),
  );
  process.exitCode = result.capture_success && result.comparison_valid ? 0 : 1;
}
