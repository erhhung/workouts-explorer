function isTerminalTileEvent(event, sourceID) {
  return Boolean(
    event?.type === "sourcedata" &&
    event.sourceId === sourceID &&
    event.coord?.canonical &&
    event.isSourceLoaded === true,
  );
}

function assessBrowserCapture(item, pendingRequests, target) {
  const expectedTiles = [];
  for (let y = 8189; y <= 8193; y++)
    for (let x = 8189; x <= 8195; x++) expectedTiles.push(`14/${x}/${y}`);
  const uniqueTiles = [...new Set(item.requests.map((row) => row.tile_id))].sort();
  const missingTiles = expectedTiles.filter((id) => !uniqueTiles.includes(id));
  const extraTiles = uniqueTiles.filter((id) => !expectedTiles.includes(id));
  const exactFrame =
    Math.abs(item.canvas_css?.width - target.width) <= 1 &&
    Math.abs(item.canvas_css?.height - target.height) <= 1 &&
    Array.isArray(item.camera?.center) &&
    item.camera.center.length === 2 &&
    item.camera.center.every((n, i) => Math.abs(n - target.center[i]) < 0.000001) &&
    Math.abs(item.camera.zoom - target.zoom) < 0.000001 &&
    Math.abs(item.camera.bearing) < 0.000001 &&
    Math.abs(item.camera.pitch) < 0.000001;
  const captureSuccess = !(
    item.errors.length ||
    pendingRequests ||
    !item.requests.length ||
    !item.source_loaded ||
    item.source_events.some((event) => event.type === "error") ||
    item.requests.some(
      (row) =>
        row.status !== 200 || row.failure || row.body_error || !Number.isFinite(row.duration_ms),
    )
  );
  const reasons = [];
  if (!exactFrame || item.frame_changes.length) reasons.push("unstable_frame");
  if (
    missingTiles.length ||
    extraTiles.length ||
    item.requests.length !== 35 ||
    item.source_events.some((event) => event.tile_id && !expectedTiles.includes(event.tile_id))
  )
    reasons.push("wrong_or_duplicate_tile_set");
  if (item.interference.length) reasons.push("other_capability_or_unscoped_source_interference");
  if (
    pendingRequests ||
    typeof item.source_loaded !== "boolean" ||
    item.requests.some(
      (row) =>
        !Number.isInteger(row.status) ||
        !Number.isFinite(row.started_ms) ||
        !Number.isFinite(row.finished_ms) ||
        !Number.isFinite(row.duration_ms) ||
        row.duration_ms < 0 ||
        row.finished_ms < row.started_ms ||
        !Number.isInteger(row.bytes) ||
        row.bytes < 0,
    )
  )
    reasons.push("incomplete_request_records");
  const comparable = reasons.length === 0;
  return {
    pending_requests: pendingRequests,
    unique_tile_ids: uniqueTiles,
    missing_planned_tiles: missingTiles,
    extra_observed_tiles: extraTiles,
    total_bytes: item.requests.reduce((sum, row) => sum + (row.bytes || 0), 0),
    http_completion_ms:
      item.requests.length && item.requests.every((row) => Number.isFinite(row.finished_ms))
        ? Math.max(...item.requests.map((row) => row.finished_ms))
        : null,
    capture_success: captureSuccess,
    comparison_reasons: reasons,
    comparison_valid: comparable,
    status: !captureSuccess
      ? "FAIL"
      : comparable
        ? "RECORDED_NO_ACCEPTANCE_THRESHOLD"
        : "NOT_COMPARABLE",
  };
}

module.exports = { isTerminalTileEvent, assessBrowserCapture };
