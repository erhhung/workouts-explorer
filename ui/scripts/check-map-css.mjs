import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const mapHost = /\.map-stage\s*>\s*\.map-canvas\.maplibregl-map\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";

for (const declaration of ["position: absolute", "inset: 0", "width: 100%", "height: 100%"])
  if (!mapHost.includes(declaration)) throw new Error(`Map host must declare ${declaration} with greater specificity than MapLibre`);

if (!/\.map-page\s*\{[^}]*height:\s*calc\(100dvh\s*-\s*4\.75rem\)[^}]*min-height:\s*0[^}]*overflow:\s*hidden/.test(css))
  throw new Error("Desktop Map must stay viewport-bound so the route sidebar scrolls independently");
if (!/\.map-stage\s*\{[^}]*height:\s*100%[^}]*min-height:\s*0/.test(css))
  throw new Error("Desktop map stage must fill the viewport-bound Map row");
if (!/\.primary:disabled,\s*\.secondary:disabled\s*\{[^}]*background:\s*var\(--surface-soft\)[^}]*cursor:\s*not-allowed/.test(css))
  throw new Error("Shared disabled buttons must use the neutral destructive-confirmation treatment");
if (/\.coverage-button/.test(css)) throw new Error("Unimplemented Coverage controls must not ship in Map CSS");
if (!/\.map-filter-actions button\s*\{[^}]*width:\s*3\.75rem/.test(css)) throw new Error("Map All and None actions must have equal fixed widths");
if (!/\.map-route-tooltip\s*\{[^}]*width:\s*max-content[^}]*grid-template-columns:\s*max-content max-content/.test(css)) throw new Error("Map route popups must auto-size two aligned detail columns");
if (!/\.map-canvas \.maplibregl-ctrl-group button\s*\{[^}]*border-radius:\s*50%[^}]*color:\s*var\(--accent\)/.test(css)) throw new Error("Map zoom controls must remain circular and use the accent color");
if (!/\.maplibregl-ctrl-group button:not\(:disabled\):hover[^}]*background-color:\s*var\(--map-control\)\s*!important/.test(css)) throw new Error("Map zoom controls must override MapLibre's lazy hover background");
if (/\.map-route-list li:hover[^}]*#c026ff/.test(css)) throw new Error("Map route highlighting must use the delayed interaction state rather than immediate CSS hover");
if (!/\.avatar-trigger:hover\s*\{[^}]*background:\s*transparent/.test(css)) throw new Error("Account menu trigger must retain its background on hover");
if (!/@media \(min-width:\s*48rem\)[\s\S]*\.app-header\s*\{[^}]*padding-inline:\s*max\(var\(--space-3\),\s*env\(safe-area-inset-left\)\)\s*max\(var\(--space-3\),\s*env\(safe-area-inset-right\)\)/.test(css)) throw new Error("Desktop header edge spacing must match its vertical spacing");
if (/\.app-header\s*\{[^}]*padding-inline:\s*var\(--space-(?:8|12)\)/.test(css)) throw new Error("Wide layouts must not override the header's equal edge spacing");
if (!/\.maplibregl-ctrl-zoom-out::before\s*\{[^}]*translateY\(-3px\)/.test(css) && !/\.maplibregl-ctrl-zoom-out::before\s*\{[^}]*\}/.test(css.split("transform: translateY(-3px)")[1] ?? "")) throw new Error("Map zoom symbols must retain their optical vertical offset");
if (!/\.mark\s*\{[^}]*contain:\s*paint[^}]*translateZ\(0\)/.test(css)) throw new Error("Header mark must keep a stable compositor layer over the WebGL map");

console.log("MapLibre host dimensions verified");
