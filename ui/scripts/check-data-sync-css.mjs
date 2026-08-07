import { readFileSync } from "node:fs";

const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const checks = [
  ["dark focus token", styles.includes("--focus: #8f744d")],
  ["light focus token", styles.includes("--focus: #6f7f77")],
  ["750ms hover/focus-visible delay", /\.tooltip-trigger:hover \+ \.tooltip-content, \.tooltip-trigger:focus-visible \+ \.tooltip-content[^}]+transition-delay: 750ms/.test(styles)],
  ["immediate pinned tooltip", /\.info-tip\.is-open > \.tooltip-content[^}]+transition-delay: 0s/.test(styles)],
  ["Data Sync overflow", /\.data-sync-page\s*\{[^}]+overflow: visible/.test(styles)],
  ["narrow schedule layout", styles.includes("@media (max-width: 30rem)")],
  ["viewport tooltip width", styles.includes("max-width: min(18rem, calc(100vw - 4rem))")],
  ["compact help marker", /\.schedule-details dt\s*\{[^}]+gap: 0\.05rem/.test(styles) && /\.info-tip > button > span\s*\{[^}]+width: 0\.85rem[^}]+height: 0\.85rem/.test(styles)],
  ["notification panel spacing", /\.sync-notification\s*\{[^}]+margin-bottom: var\(--space-5\)/.test(styles)],
  ["filled semantic statuses", ["succeeded", "failed", "partially_succeeded", "cancelled", "running"].every((status) => new RegExp(`\\.sync-status--[^}]*${status}|\\.sync-status--${status}`).test(styles)) && styles.includes("background: var(--status-success)") && styles.includes("background: var(--status-error)")],
  ["schedule state fills", styles.includes(".schedule-state--enabled") && styles.includes(".schedule-state--disabled")],
  ["forced color statuses", /\.sync-status, \.schedule-state\s*\{[^}]+forced-color-adjust: auto/.test(styles)],
  ["three-column results", /\.result-counts\s*\{[^}]+grid-template-columns: repeat\(3, minmax\(0, 1fr\)\)/.test(styles)],
  ["panel-inset result spacing", /\.result-counts\s*\{[^}]+margin-block: var\(--space-4\)[^}]+padding-block: var\(--space-4\)/.test(styles) && !/\.source-run \.result-counts\s*\{/.test(styles)],
  ["shared progress spacing", /\.sync-progress\s*\{[^}]+margin-block: var\(--space-4\)/.test(styles) && !/\.source-run \.sync-progress\s*\{/.test(styles)],
  ["centered source heading", /\.source-run\s*\{[^}]+padding-block: var\(--space-4\)/.test(styles) && /\.source-run-heading\s*\{[^}]+align-items: center/.test(styles) && /\.source-run-heading > div\s*\{[^}]+align-items: center/.test(styles)],
  ["compressed source and artifact spacing", /\.source-runs\s*\{[^}]+margin-top: var\(--space-6\)[^}]+margin-bottom: -2rem/.test(styles) && /\.artifact-group\s*\{[^}]+margin-top: var\(--space-6\)/.test(styles)],
  ["compact history rows", /\.sync-history-table td\s*\{[^}]+height: 2\.75rem[^}]+padding-block: 0/.test(styles)],
  ["tracked history headers", /\.sync-history-table th\s*\{[^}]+letter-spacing: 0\.06em[^}]+text-transform: uppercase/.test(styles)],
  ["compact orange filter trigger", /\.history-filter-trigger\.range-trigger\s*\{[^}]+min-height: 2\.75rem/.test(styles) && /\.range-trigger > span:last-child\s*\{[^}]+color: var\(--accent\)/.test(styles) && styles.includes("--radius: 0.3rem")],
];

const failed = checks.filter(([, passed]) => !passed).map(([name]) => name);
if (failed.length) throw new Error(`Data Sync CSS checks failed: ${failed.join(", ")}`);
console.log("Data Sync tooltip and focus CSS verified");
