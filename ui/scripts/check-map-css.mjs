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
if (!/\.coverage-diagnostic-card\s*\{[^}]*position:\s*absolute[^}]*z-index:\s*6[^}]*max-height:[^}]*overflow-y:\s*auto/.test(css))
  throw new Error("Coverage diagnostic review must float and scroll within the map viewport");
if (/\.coverage-diagnostic-card\s*\{[^}]*border-top:/.test(css) ||
    !/\.coverage-diagnostic-card\s*\{[^}]*border:\s*1px solid var\(--border-strong\)/.test(css))
  throw new Error("Coverage diagnostic review must use the standard popup border on every edge");
if (!/\.coverage-diagnostic-actions\s*\{[^}]*grid-template-columns:\s*repeat\(2,\s*4\.25rem\)[^}]*gap:\s*var\(--space-3\)/.test(css) ||
    !/\.coverage-diagnostic-counts\s*\{[^}]*gap:\s*var\(--space-3\)/.test(css))
  throw new Error("Coverage diagnostic actions and stat boxes must use equal widths and shared gutters");
if (/\.coverage-diagnostic-save\s*\{[^}]*min-height:/.test(css))
  throw new Error("Idle diagnostic save status must not reserve vertical space");
if (!/\.coverage-diagnostic-preparing\s*\{[^}]*min-height:\s*12rem[^}]*overflow:\s*hidden/.test(css) ||
    !/\.coverage-diagnostic-preparing::before\s*\{[^}]*repeating-linear-gradient\(135deg[^}]*animation:\s*coverage-diagnostic-drift 1\.6s linear infinite/.test(css) ||
    !/@media \(prefers-reduced-motion:\s*reduce\)\s*\{\s*\.coverage-diagnostic-preparing::before\s*\{\s*animation:\s*none/.test(css))
  throw new Error("Coverage diagnostic preparation must animate a subdued diagonal field with reduced-motion fallback");
if (!/\.coverage-diagnostic-labels button\[aria-pressed="true"\]\s*\{[^}]*background:\s*#19c7c9/.test(css) ||
    !/\.coverage-diagnostic-labels button:focus-visible/.test(css))
  throw new Error("Coverage diagnostic labels must expose visible pressed and keyboard focus states");
if (!/\.coverage-diagnostic-fit:hover,\s*\.coverage-diagnostic-rerun:not\(:disabled\):hover\s*\{[^}]*border-color:\s*var\(--accent\)/.test(css) ||
    !/\.coverage-diagnostic-copy\s*\{[^}]*cursor:\s*pointer/.test(css))
  throw new Error("Coverage diagnostic actions must expose accent hover borders and pointer cursors");
if (!/\.map-mode-controls\s*\{[^}]*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/.test(css) ||
    !/\.map-mode-controls\s*\{[^}]*width:\s*calc\(100%\s*-\s*1\.65rem\)[^}]*margin-left:\s*1\.65rem/.test(css) ||
    !/\.map-mode-controls\s*\{[^}]*overflow:\s*hidden[^}]*border-radius:\s*var\(--radius-sm\)/.test(css) ||
    !/\.map-mode-controls button \+ button\s*\{[^}]*border-left:\s*1px solid var\(--border-strong\)/.test(css))
  throw new Error("Map mode controls must be equal joined segments inside one rounded frame");
if (!/\.map-route-toolbar\s*\{[^}]*grid-template-columns:\s*2\.3rem minmax\(0,\s*1fr\)[^}]*border:\s*1px solid transparent/.test(css) ||
    !/\.map-route-list li\s*\{[^}]*grid-template-columns:\s*2\.3rem minmax\(0,\s*1fr\)/.test(css))
  throw new Error("Bulk and individual route checkboxes must share the same horizontal track");
if (!/\.map-route-tooltip\s*\{[^}]*width:\s*max-content[^}]*grid-template-columns:\s*max-content max-content/.test(css)) throw new Error("Map route popups must auto-size two aligned detail columns");
if (!/\.map-canvas \.maplibregl-ctrl-group button\s*\{[^}]*border-radius:\s*50%[^}]*color:\s*var\(--accent\)/.test(css)) throw new Error("Map zoom controls must remain circular and use the accent color");
if (!/\.map-canvas \.maplibregl-canvas:focus-visible\s*\{[^}]*outline:\s*0/.test(css)) throw new Error("The focused map canvas must not draw a border over diagnostic geometry");
if (!/\.maplibregl-ctrl-group button:not\(:disabled\):hover[^}]*background-color:\s*var\(--map-control\)\s*!important/.test(css)) throw new Error("Map zoom controls must override MapLibre's lazy hover background");
if (/\.map-route-list li:hover[^}]*#c026ff/.test(css)) throw new Error("Map route highlighting must use the delayed interaction state rather than immediate CSS hover");
if (!/\.avatar-trigger:hover\s*\{[^}]*background:\s*transparent/.test(css)) throw new Error("Account menu trigger must retain its background on hover");
if (!/@media \(min-width:\s*48rem\)[\s\S]*\.app-header\s*\{[^}]*padding-inline:\s*max\(var\(--space-3\),\s*env\(safe-area-inset-left\)\)\s*max\(var\(--space-3\),\s*env\(safe-area-inset-right\)\)/.test(css)) throw new Error("Desktop header edge spacing must match its vertical spacing");
if (/\.app-header\s*\{[^}]*padding-inline:\s*var\(--space-(?:8|12)\)/.test(css)) throw new Error("Wide layouts must not override the header's equal edge spacing");
if (!/\.maplibregl-ctrl-zoom-out::before\s*\{[^}]*translateY\(-3px\)/.test(css) && !/\.maplibregl-ctrl-zoom-out::before\s*\{[^}]*\}/.test(css.split("transform: translateY(-3px)")[1] ?? "")) throw new Error("Map zoom symbols must retain their optical vertical offset");
if (!/\.mark\s*\{[^}]*contain:\s*paint[^}]*translateZ\(0\)/.test(css)) throw new Error("Header mark must keep a stable compositor layer over the WebGL map");

console.log("MapLibre host dimensions verified");
