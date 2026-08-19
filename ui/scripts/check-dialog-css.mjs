import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");

if (!/\.dialog-actions\s*\{[^}]*gap:\s*var\(--space-4\)[^}]*padding:\s*var\(--space-4\)/.test(css)) {
  throw new Error("Dialog action gap must match the default vertical action-bar spacing");
}
if (!/\.range-dialog-action\s*\{\s*flex:\s*0 0 6\.5rem;\s*\}/.test(css)) {
  throw new Error("Date-range dialog actions must use one fixed width");
}
const deletionActions = css.match(/\.single-deletion-dialog\s*>\s*\.dialog-actions\s*\{([^}]*)\}/)?.[1] ?? "";
if (!deletionActions.includes("margin-inline") || deletionActions.includes("padding")) {
  throw new Error("Deletion dialogs must inherit the shared action-bar padding and gap");
}
if (!/\.single-deletion-dialog\s*>\s*\.dialog-actions\s*>\s*button\s*\{\s*flex:\s*0 0 6\.5rem;\s*\}/.test(css)) {
  throw new Error("Deletion dialog actions must remain equal width");
}

console.log("Dialog action spacing and widths verified");
