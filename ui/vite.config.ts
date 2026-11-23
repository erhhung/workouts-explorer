import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [
    {
      name: "osm-rebuild-contract",
      enforce: "pre",
      resolveId(source) {
        if (source === "virtual:osm-rebuild-contract") return "\0osm-rebuild-contract";
      },
      load(id) {
        if (id !== "\0osm-rebuild-contract") return;
        const path = fileURLToPath(new URL("../docs/osm-resumable-rebuild.md", import.meta.url));
        this.addWatchFile(path);
        return `export default ${JSON.stringify(readFileSync(path, "utf8"))};`;
      },
    },
    react(),
  ],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/swagger": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test-setup.ts",
  },
});
