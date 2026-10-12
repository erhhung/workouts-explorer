const workouts = Array.from({ length: 16 }, (_, i) => ({
  id: `public-synthetic-indoor-${i}`,
  name: "Indoor Walk",
  start: `2026-09-15 ${String(i).padStart(2, "0")}:00:00 +0000`,
  end: `2026-09-15 ${String(i).padStart(2, "0")}:02:00 +0000`,
  duration: 120,
  // Public invented route outside the fixture provider boundary: the general
  // worker can exercise real ingest/delete without adding matcher children.
  route: Array.from({ length: 17 }, (_, j) => ({
    timestamp: `2026-09-15 ${String(i).padStart(2, "0")}:${String(Math.floor((j * 7) / 60)).padStart(2, "0")}:${String((j * 7) % 60).padStart(2, "0")} +0000`,
    latitude: 0.08 + i * 0.0001,
    longitude: 0.08 + j * 0.0001,
    horizontalAccuracy: 3,
  })),
}));

process.stdout.write(
  JSON.stringify({
    apiVersion: "v1",
    kind: "ConfigMap",
    metadata: { name: "acceptance-source", namespace: "workouts-acceptance" },
    data: { "HealthAutoExport-2026-09-15.json": JSON.stringify({ data: { workouts } }) },
  }),
);
