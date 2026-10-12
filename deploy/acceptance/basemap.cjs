// Entirely public, invented geometry near (0, 0); no provider or private exports.
const features = [];

for (let i = 0; i < 40; i++) {
  for (const vertical of [false, true]) {
    const coordinates = vertical
      ? [
          [i * 0.001, 0],
          [i * 0.001, 0.039],
        ]
      : [
          [0, i * 0.001],
          [0.039, i * 0.001],
        ];
    features.push({
      type: "Feature",
      properties: { kind: "road", name: `${vertical ? "Avenue" : "Street"} ${i + 1}` },
      geometry: { type: "LineString", coordinates },
    });
  }
  if (i < 39)
    for (let j = 0; j < 39; j++) {
      const x = i * 0.001,
        y = j * 0.001,
        d = 0.00015,
        s = 0.00085;
      features.push({
        type: "Feature",
        properties: {
          kind: (i + j) % 9 === 0 ? "park" : "block",
          name: `Synthetic Block ${i + 1}-${j + 1}`,
        },
        geometry: {
          type: "Polygon",
          coordinates: [
            [
              [x + d, y + d],
              [x + s, y + d],
              [x + s, y + s],
              [x + d, y + s],
              [x + d, y + d],
            ],
          ],
        },
      });
    }
}

const style = (dark) => ({
  version: 8,
  name: "Public Synthetic Acceptance City",
  center: [0.0195, 0.0195],
  zoom: 14,
  sources: {
    city: {
      type: "geojson",
      data: "/synthetic/city.geojson",
      attribution: "Public synthetic geometry; not OSM",
    },
  },
  layers: [
    {
      id: "ground",
      type: "background",
      paint: { "background-color": dark ? "#252c31" : "#efece3" },
    },
    {
      id: "blocks",
      type: "fill",
      source: "city",
      filter: ["==", "kind", "block"],
      paint: { "fill-color": dark ? "#34414a" : "#ded9cc" },
    },
    {
      id: "parks",
      type: "fill",
      source: "city",
      filter: ["==", "kind", "park"],
      paint: { "fill-color": dark ? "#344f43" : "#c5d9bc" },
    },
    {
      id: "roads-casing",
      type: "line",
      source: "city",
      filter: ["==", "kind", "road"],
      paint: { "line-color": dark ? "#56616b" : "#c6bfae", "line-width": 8 },
    },
    {
      id: "roads",
      type: "line",
      source: "city",
      filter: ["==", "kind", "road"],
      paint: { "line-color": dark ? "#899099" : "#ffffff", "line-width": 5 },
    },
  ],
});

process.stdout.write(
  JSON.stringify({
    apiVersion: "v1",
    kind: "ConfigMap",
    metadata: { name: "acceptance-basemap", namespace: "workouts-acceptance" },
    data: {
      "city.geojson": JSON.stringify({ type: "FeatureCollection", features }),
      "light.json": JSON.stringify(style(false)),
      "dark.json": JSON.stringify(style(true)),
      "default.conf":
        'server { listen 8080; root /data; location / { add_header Access-Control-Allow-Origin "*"; } }',
    },
  }),
);
