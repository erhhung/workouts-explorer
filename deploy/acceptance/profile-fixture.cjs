// Unit-test manifest only. Never write or replace the shared production manifest.
const fs = require("node:fs");
const path = require("node:path");

const defaults = {
  schema_version: 1,
  year: 2026,
  seed: 7104,
  gps_sample_seconds: 5,
  distance_meters_per_mile: 1609.344,
  other_duration_seconds: 1800,
  other_types: [
    ["climbing", "Climbing"],
    ["paddle_sports", "Paddle Sports"],
    ["skating_sports", "Skating Sports"],
  ].map(([key, label]) => ({ key, label })),
  profiles: {},
};

for (const [name, perDay, counts, percentages, miles, durations] of [
  ["typical", 1, [219, 73, 37, 36], [60, 20, 10, 10], [8, 8, 20, 0], [7200, 7200, 7200, 1800]],
  [
    "stress",
    2,
    [292, 146, 73, 219],
    [40, 20, 10, 30],
    [13.1, 10, 40, 0],
    [9000, 10800, 14400, 1800],
  ],
])
  defaults.profiles[name] = {
    workouts_per_day: perDay,
    total_workouts: 365 * perDay,
    activities: ["running", "walking", "cycling", "other"].map((key, i) => ({
      key,
      label: ["Outdoor Run", "Outdoor Walk", "Outdoor Cycle", "Other"][i],
      count: counts[i],
      percentage: percentages[i],
      routed: i < 3,
      distance_miles: miles[i],
      duration_seconds: durations[i],
    })),
  };

const typeKeys = {
  "Outdoor Run": "outdoor-run-27e9c638ed73cbf21bee19a1d4db970f",
  "Outdoor Walk": "outdoor-walk-d382cfff87408643a86b7a842d85134a",
  "Outdoor Cycle": "outdoor-cycle-b6c2eef4fa14bc8baf203afa98e2c72e",
  Climbing: "climbing-df7da510a6fe1ab64a57e49687b9b1d2",
  "Paddle Sports": "paddle-sports-3d6e19d8f2d58789c6c23febefa2301d",
  "Skating Sports": "skating-sports-7422cc76b1829e98bc46bdc5b48a665e",
};
const bytes = Buffer.from(
  process.env.MOCK_PROFILE_MANIFEST_JSON ?? JSON.stringify(defaults) + "\n",
);
const manifest = JSON.parse(bytes);
const original = fs.readFileSync;

fs.readFileSync = function (file, ...args) {
  if (path.resolve(String(file)) === path.join(__dirname, "profiles.json"))
    return args[0] === "utf8" ? bytes.toString() : bytes;
  return original.call(this, file, ...args);
};

module.exports = { manifest, bytes, typeKeys };
