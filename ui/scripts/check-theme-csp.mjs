import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

const html = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const nginx = readFileSync(new URL("../nginx.conf", import.meta.url), "utf8");
const scripts = [...html.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)].filter((match) => !/\bsrc\s*=/.test(match[1]));

if (scripts.length !== 1) {
  throw new Error(`Expected exactly one inline script, found ${scripts.length}`);
}

const hash = `sha256-${createHash("sha256").update(scripts[0][2]).digest("base64")}`;
if (!nginx.includes(`'${hash}'`)) {
  throw new Error(`nginx.conf CSP is missing the exact theme bootstrap hash '${hash}'`);
}

for (const directive of ["default-src 'none'", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "font-src 'self'", "img-src 'self' data:", "connect-src 'self'", "base-uri 'none'", "frame-ancestors 'none'", "form-action 'self'"]) {
  if (!nginx.includes(directive)) throw new Error(`nginx.conf CSP is missing: ${directive}`);
}

console.log(`Theme bootstrap CSP verified: ${hash}`);
