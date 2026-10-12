#!/usr/bin/env node
// Scratch-only proxies, source fixture mounts, and pre-seed worker pause.
let input = "";

process.stdin.on("data", (chunk) => (input += chunk));
process.stdin.on("end", () => {
  const sourceChecksum = require("node:crypto")
    .createHash("sha256")
    .update(require("node:fs").readFileSync(require("node:path").join(__dirname, "source.cjs")))
    .digest("hex");
  input = input.replace(
    "        location / {",
    `        location /api/ { proxy_pass http://workouts-explorer-api; }
        location /swagger { proxy_pass http://workouts-explorer-api; }
        location /synthetic/ { proxy_pass http://acceptance-basemap/; }
        location / {`,
  );

  // Seed before allowing the real production matcher to consume the fixture queue.
  if (process.env.COVERAGE_ACCEPTANCE_PAUSE_MATCHER !== "false") {
    for (const component of ["worker", "coverage-worker"]) {
      input = input.replace(
        `replicas: 1\n  selector:\n    matchLabels: {app.kubernetes.io/instance: workouts-acceptance, app.kubernetes.io/component: ${component}}`,
        `replicas: 0\n  selector:\n    matchLabels: {app.kubernetes.io/instance: workouts-acceptance, app.kubernetes.io/component: ${component}}`,
      );
    }
  }

  input = input
    .split("---\n")
    .map((section) => {
      // Runtime matchers get a read-only OSM role; migration hooks keep the
      // explicit migration credential, including Helm 3's separately rendered hooks.
      if (section.includes("kind: Deployment\n"))
        section = section.replaceAll("key: osmDatabaseUrl", "key: osmReaderDatabaseUrl");
      if (!section.includes("name: workouts-explorer-worker\n")) return section;
      return section
        .replace(
          "      labels: {app.kubernetes.io/instance: workouts-acceptance, app.kubernetes.io/component: worker}",
          `      labels: {app.kubernetes.io/instance: workouts-acceptance, app.kubernetes.io/component: worker}
      annotations: {acceptance-source-checksum: "${sourceChecksum}"}`,
        )
        .replace(
          "          volumeMounts:\n",
          "          volumeMounts:\n            - {name: acceptance-source, mountPath: /tmp/acceptance-source, readOnly: true}\n",
        )
        .replace(
          "      containers:\n",
          `      initContainers:
        - name: public-synthetic-source
          image: busybox:1.37
          command: [sh, -ec, 'cp /fixture/HealthAutoExport-2026-09-15.json /source/HealthAutoExport-2026-09-15.json']
          resources:
            requests: {cpu: 25m, memory: 64Mi}
            limits: {cpu: 250m, memory: 256Mi}
          volumeMounts:
            - {name: acceptance-source-input, mountPath: /fixture, readOnly: true}
            - {name: acceptance-source, mountPath: /source}
      containers:
`,
        )
        .replace(
          "      volumes:\n",
          "      volumes:\n        - {name: acceptance-source, emptyDir: {sizeLimit: 1Mi}}\n        - name: acceptance-source-input\n          configMap: {name: acceptance-source}\n",
        );
    })
    .join("---\n");

  process.stdout.write(input);
});
