const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const profile = require("./profile.cjs");
const e = require("./evidence.cjs");

if (process.env.COVERAGE_ACCEPTANCE_DISPOSABLE !== "true")
  throw Error("explicit disposable switch required");
const namespace = profile.requireProfile();
const base = "http://127.0.0.1:18081";
const token = crypto
  .createHash("sha256")
  .update("FAKE disposable synthetic coverage acceptance bearer; never a live credential")
  .digest("base64url");
const root = path.join(profile.artifact("exports"), namespace.metadata.uid);
fs.mkdirSync(root, { recursive: true });
const artifacts = [];

function save(name, data) {
  fs.writeFileSync(path.join(root, name), data);
  artifacts.push({
    ...profile.binding,
    schema_version: e.SCHEMA,
    namespace: e.NAMESPACE,
    namespace_uid: namespace.metadata.uid,
    name,
    bytes: data.length,
    sha256: crypto.createHash("sha256").update(data).digest("hex"),
  });
}

async function request(route, body) {
  const response = await fetch(base + route, {
    method: body ? "POST" : "GET",
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  if (!response.ok) throw Error(`synthetic export HTTP ${response.status}`);
  return Buffer.from(await response.arrayBuffer());
}

(async () => {
  for (const [name, startDate, endDate] of [
    ["month", `${profile.manifest.year}-09-01`, `${profile.manifest.year}-09-30`],
    ["year", `${profile.manifest.year}-01-01`, `${profile.manifest.year}-12-31`],
  ]) {
    const data = await request("/api/map-selections", { startDate, endDate });
    const selection = JSON.parse(data);
    save(`${name}-selection.json`, data);
    const prefix = `/api/map-selections/${selection.id}`;
    save(
      `${name}-paths.json`,
      await request(`${prefix}/coverage/paths?generation=${selection.dataGeneration}&pageSize=100`),
    );
    for (const kind of ["route", "coverage"]) {
      save(
        `${name}-${kind}.pbf`,
        await request(`${prefix}/${kind}-tiles/${selection.dataGeneration}/14/8192/8191.pbf`),
      );
    }
    if (profile.requireProfile().metadata.uid !== namespace.metadata.uid)
      throw Error("namespace changed during export");
    fs.writeFileSync(
      path.join(root, "binding.json"),
      JSON.stringify(
        {
          ...profile.binding,
          schema_version: e.SCHEMA,
          namespace: e.NAMESPACE,
          namespace_uid: namespace.metadata.uid,
          recorded_at: new Date().toISOString(),
          artifacts,
        },
        null,
        2,
      ) + "\n",
    );
  }
  console.log(
    "Exported real synthetic API selections, lists, and Martin MVT responses; no bearer credentials saved.",
  );
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
