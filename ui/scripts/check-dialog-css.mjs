import { readFileSync, readdirSync } from "node:fs";

const css = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8").replace(
  /\s+/g,
  " ",
);

for (const name of readdirSync(new URL("../src/", import.meta.url))) {
  if (!name.endsWith(".tsx") || name.includes(".test.") || name === "Dialog.tsx") continue;
  const source = readFileSync(new URL(`../src/${name}`, import.meta.url), "utf8");
  if (source.includes('from "@radix-ui/react-dialog"')) {
    throw new Error(`${name} must use the shared pixel-aligned Dialog primitives`);
  }
}

if (
  !/\.dialog-content\[data-dialog-pixel-positioned\]\s*\{[^}]*transform:\s*none;[^}]*translate:\s*none;/.test(
    css,
  )
) {
  throw new Error("Settled dialogs must use layout positioning without compositor translations");
}

if (
  !/\.dialog-actions\s*\{[^}]*gap:\s*var\(--space-4\)[^}]*padding:\s*var\(--space-4\)/.test(css)
) {
  throw new Error("Dialog action gap must match the default vertical action-bar spacing");
}
if (!/\.range-dialog-action\s*\{\s*flex:\s*0 0 6\.5rem;\s*\}/.test(css)) {
  throw new Error("Date-range dialog actions must use one fixed width");
}
const deletionActions =
  css.match(/\.single-deletion-dialog\s*>\s*\.dialog-actions\s*\{([^}]*)\}/)?.[1] ?? "";
if (!deletionActions.includes("margin-inline") || deletionActions.includes("padding")) {
  throw new Error("Deletion dialogs must inherit the shared action-bar padding and gap");
}

if (
  !/\.compact-dialog\s*\{[^}]*--dialog-inset:\s*var\(--space-5\)/.test(css) ||
  !/\.compact-dialog\s+\.dialog-actions\s*\{[^}]*gap:\s*var\(--dialog-inset\)[^}]*padding:\s*var\(--dialog-inset\)/.test(
    css,
  )
) {
  throw new Error("Summary and custom-range dialog actions must share the Road Coverage inset");
}
if (
  !/\.single-deletion-dialog\s*>\s*\.dialog-actions\s*>\s*button\s*\{\s*flex:\s*0 0 6\.5rem;\s*\}/.test(
    css,
  )
) {
  throw new Error("Deletion dialog actions must remain equal width");
}

console.log("Dialog action spacing and widths verified");
