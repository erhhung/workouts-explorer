# ADR 0006: UI Foundation

## Status

Accepted

## Context

ADR 0001 selects React, TypeScript, Vite, TanStack Query, TanStack Table, and
MapLibre but deliberately leaves styling and component implementation open.
Milestone 2 introduces public authentication screens, dark-default theming, the
authenticated shell, dialogs, an avatar menu, and responsive behavior. Later
milestones add dense desktop tables, compact expandable mobile rows, map overlays,
and a mobile bottom sheet.

The functional specification requires keyboard and touch parity, distinguishable
focus/hover/selected/warning/error states, a theme restored before primary paint,
and readable light and dark map controls. The product also has a two-second Summary
and three-second initial Map usability target. Styling must not obscure semantic
HTML, couple domain state to a widget library, or impose a generic visual system on
the map and data-dense views.

The current UI is an executable skeleton rather than a representative product UI.
It has a single panel, product-owned CSS, a dark palette, responsive padding, focus
styling, and reduced-motion handling. It has no login form, menu, dialog, sheet,
table, MapLibre dependency, bundled font, light theme, or pre-paint theme bootstrap.
The evaluation therefore combines the product requirements, documented library
behavior, package metadata, and a disposable bundle composition rather than
treating the skeleton as sufficient visual evidence.

## Decision

### Styling and primitives

Use product-owned, mobile-first CSS and a deliberately narrow set of unstyled
Radix Primitives:

- `@radix-ui/react-dialog` for ordinary modal dialogs and the mobile sheet;
- `@radix-ui/react-alert-dialog` for destructive confirmations;
- `@radix-ui/react-dropdown-menu` for the avatar and workout action menus;
- `@radix-ui/react-popover` for anchored, non-modal controls such as the desktop
  date-range chooser; and
- `@radix-ui/react-tooltip` only for supplemental hover/focus explanations and
  aggregate breakdowns that contain no action available only through the tooltip.

Import the individual packages, not the `radix-ui` aggregate package. Do not adopt
Radix Themes, a CSS-in-JS runtime, Tailwind CSS, or a comprehensive styled component
system. A new primitive package requires a concrete interaction that native HTML
and the approved primitives cannot implement safely, an accessibility review, and
an incremental production-bundle measurement.

Radix is behavior, not the product component API. Small product-owned wrappers set
required titles/descriptions, class names, animation hooks, and defaults, while
feature state and API data remain outside those wrappers. Use native `button`,
`a`, `form`, `label`, `input`, `select`, `fieldset`, and `table` elements wherever
their behavior is sufficient. Do not replace native controls merely for visual
uniformity. Avatar images use the API's proxied Gravatar response or a generated
fallback; UI CSS and components never cause a browser-direct Gravatar request.

Apply the primitives to the evaluated surfaces as follows:

| Surface | Foundation policy |
|---|---|
| Login and account forms | Semantic native form controls, visible labels, field-level errors, an error summary when submission fails, and product CSS; no primitive is needed. |
| Avatar and row action menus | Radix Dropdown Menu for roving focus, arrow keys, typeahead, collision handling, dismissal, and return focus. Use menus only for actions, not site navigation. |
| Dialog and confirmation | Radix Dialog with a required title and explicit close control; Radix Alert Dialog for deletion, with initial focus on the least destructive action. |
| Mobile sheet | A product `Sheet` presentation of Radix Dialog, bottom-anchored below `48rem`. It has modal focus containment, an explicit close button, safe-area padding, and no drag-only operation. It is not a second state model or a separate sheet dependency. |
| Dense table | TanStack Table supplies models only. Desktop renders a semantic table with scoped headers and `aria-sort`; mobile renders a three-column list/row presentation from the same model with a real expansion button and `aria-expanded`/`aria-controls`. Do not use ARIA grid unless spreadsheet-style cell navigation becomes a requirement. |
| MapLibre | Keep controls and attribution as labelled DOM controls above the canvas. Map tokens feed a small theme adapter that reapplies paint/layout properties after a theme change because canvas layers do not inherit CSS variables. Map hover information must also be reachable through selection/filter or the corresponding Summary/Path Coverage data; the canvas is not the sole representation of essential values. |

### Design tokens and CSS organization

Define semantic custom properties on `:root` and override their values under
`html[data-theme="light"]` and `html[data-theme="dark"]`. Dark is the fallback.
Token groups cover at least:

- canvas/surface/elevated surface, primary/muted text, border, focus, selection,
  link, info, warning, error, and disabled states;
- workout route categories, selected-route highlight, coverage buckets, map
  control surfaces, and map-overlay text;
- font families, weights, fluid display sizes, body/table sizes, and tabular
  numeral behavior;
- a four-pixel spacing scale, control sizes, radii, borders, shadows, and layer
  order; and
- motion duration and easing.

Components consume semantic variables rather than literal theme colors. CSS files
may be organized by tokens, base/reset, layout, and component/feature styles, but
selectors remain shallow and locally named. MapLibre's required base CSS is loaded
with the Map route and product CSS overrides its controls through the same semantic
tokens.

### Font

Bundle the normal Latin variable WOFF2 for **Atkinson Hyperlegible Next**, weights
200 through 800, with its SIL Open Font License 1.1 text. The evaluated Fontsource
artifact is `@fontsource-variable/atkinson-hyperlegible-next` 5.3.0; its normal
Latin file is 33,996 bytes and its metadata identifies upstream Google Fonts and
OFL-1.1. Self-host the font in the UI build and copy the npm package's license
into the OCI image under `/usr/share/licenses`; the license is not a public UI
asset or duplicated source file. Use `font-display: swap`, and do not request
Google Fonts or another font host from the browser. Use the font for the wordmark,
controls, prose, and tables, with `font-variant-numeric: tabular-nums` on metrics,
dates, and table columns. Do not bundle italic or Latin Extended until product
content requires it.

### Theme bootstrap

Place a small classic inline script in `index.html` before stylesheets and the Vite
module entry. It synchronously:

1. reads `workouts-explorer.theme` from `localStorage` inside `try/catch`;
2. accepts only `light` or `dark`, defaulting to `dark`;
3. sets `document.documentElement.dataset.theme` and `style.colorScheme`; and
4. updates the `theme-color` meta element.

The inline script is static and must be allowed by an exact CSP hash, not
`unsafe-inline`. The server-stored preference remains authoritative after
authentication: applying a fetched or changed preference updates the DOM and the
non-sensitive local cache synchronously. Failure or denial of storage falls back
to dark without blocking rendering. Theme is a display hint only; no identity,
token, or private data may be stored with it. Do not use the operating-system
preference because the product's specified default is dark and its preference enum
contains only light and dark.

### Responsive policy

Use mobile-first CSS with two shared, content-driven breakpoints:

| Name | CSS boundary | Behavior |
|---|---|---|
| Compact | below `48rem` (768 px at the default root size) | Omit the wordmark, use mobile rows, overlay the avatar on Map, and put view/filter controls in the bottom sheet. |
| Standard | `min-width: 48rem` | Show the wordmark, desktop control row, and semantic dense table when the available content width supports its selected columns. |
| Wide | `min-width: 64rem` (1024 px at the default root size) | Allow the complete header/control arrangement and wider preferred-column table without increasing base control density. |

Prefer container queries for feature-local adaptations and `clamp()` for fluid
spacing/type. Do not add device-specific breakpoints or branch domain state on
`window.innerWidth`. Support a 320 px CSS viewport, 200% text zoom/reflow, display
cutouts via `env(safe-area-inset-*)`, and `100dvh` rather than fixed `100vh` for
mobile map and sheet sizing. Interactive targets are at least 44 by 44 CSS pixels
on compact layouts; dense desktop row height may be smaller if each control still
meets WCAG 2.2's 24 by 24 CSS-pixel minimum or spacing exception.

### Accessibility acceptance

Target WCAG 2.2 AA. Every interface milestone must include:

- semantic and accessible-name tests with Testing Library and `jest-dom`, including
  labelled form errors and status/alert announcements;
- automated `axe-core` checks in a real Chromium flow through
  `@axe-core/playwright` for login, each open menu/dialog/sheet, Summary table and
  expanded mobile row, and Map controls in both themes;
- keyboard tests for logical tab order, visible and unobscured focus, menu arrow
  keys/typeahead/Escape, dialog focus containment and return, sheet close, sorting,
  expansion, and all actions otherwise exposed by hover or touch;
- measured contrast of at least 4.5:1 for normal text, 3:1 for large text and
  meaningful controls/graphics, plus high-contrast/forced-colors inspection; and
- manual checks at 320 px, the two breakpoint boundaries, 200% zoom, coarse pointer,
  reduced motion, keyboard only, and at least one current screen-reader/browser
  combination before accepting each major surface.

Light and dark visual regression captures must include focus, selected, warning,
error, disabled, route highlight, coverage buckets, MapLibre controls, and public
attribution. Color cannot be the only state cue. Automated checks are a gate, not
a substitute for keyboard, screen-reader, reflow, and map-equivalence review.

### Motion

Motion communicates hierarchy and state but is never required to understand or
complete an action. Use only opacity and transform for entry/exit where practical:

- 120 ms for menu, tooltip, and small control feedback;
- 180 ms for dialog/backdrop transitions; and
- at most 240 ms for the mobile sheet.

Do not animate table layout, large background decoration continuously, or map
camera movement merely for polish. Under `prefers-reduced-motion: reduce`, set
product transition/animation durations to zero, remove parallax or decorative
motion, and use MapLibre `jumpTo`/instant fit instead of `flyTo` or eased camera
movement. Loading indicators may remain non-motion text or a static progress state.

## Evaluation Evidence

### Alternatives

| Approach | Login/menu/dialog/sheet | Dense table and MapLibre | Bundle and maintenance | Result |
|---|---|---|---|---|
| Product-owned CSS only | Excellent for forms and visuals, but CSS cannot provide menu roving focus/typeahead or robust modal focus, inertness, dismissal, and return focus. Native `dialog` helps dialogs but does not solve action menus or a consistently tested sheet. | Maximum table/map control and no framework runtime. | No dependency bytes, but the application would own accessibility-sensitive interaction code and browser edge cases. | Rejected: the byte saving does not justify custom menu and modal infrastructure. |
| Product-owned CSS plus headless primitives | Native forms stay simple; Radix supplies only the difficult menu, modal, anchored overlay, and tooltip behavior. Dialog can be presented as a sheet without another state model. | Leaves semantic table markup and all MapLibre styling/control placement product-owned. | Modular packages and no shipped library theme/CSS. Narrow wrappers contain upgrades. | Accepted with the exact Radix package allowlist above. |
| Utility-first CSS | Tailwind 4.3.3 has no browser styling runtime and emits only detected utilities; responsive/state variants are capable of all evaluated layouts. It would still need accessible headless behavior. | Capable, but long dynamic class sets around dense rows and map overlays obscure the semantic token vocabulary, while MapLibre paint expressions still require a separate adapter. | Build dependency and generated CSS are reasonable; the main cost is two styling vocabularies and less readable state-heavy markup, not bundle size. | Rejected: it adds little leverage for this small, visually bespoke UI over custom properties and product CSS. |
| Comprehensive styled system | Material UI 9.2.0 covers all common controls and explicitly supports React 19. | Strong table/form breadth, but Material defaults and CSS-in-JS theming are a poor fit for a distinctive dense map surface and duplicate the TanStack/product token decisions. | Largest measured representative increment, Emotion runtime, and broad component API ownership. | Rejected: speed on common screens does not offset runtime, styling override, and visual-system coupling. |

React Aria Components 1.20.0 was the other concrete maintained headless candidate.
Its package metadata permits React 19, its state model and internationalized input
coverage are strong, and it ships no default styles. It was not selected because
the representative composition was larger and its broader component/state API
would overlap more native form and TanStack responsibilities than the narrowly
approved Radix parts. Reconsider it if complex accessible date, locale, select, or
collection widgets become a dominant requirement.

Radix package metadata checked on 2026-08-03 reports React 19-compatible peer
ranges for Dialog 1.1.23, Alert Dialog 1.1.23, Dropdown Menu 2.1.24, Popover 1.1.23,
and Tooltip 1.2.16. Radix documentation states that Dialog traps focus, announces
title/description, closes on Escape, and returns focus, while Dropdown Menu follows
the WAI-ARIA Menu Button pattern with roving focus, typeahead, arrow keys, Escape,
and collision-aware placement. Product tests remain required because a primitive
cannot supply labels, contrast, application focus order, or correct composition.

### Bundle evidence and budgets

The current checked-in Vite production output contains 228,123 bytes of JavaScript
and 1,600 bytes of CSS, or 70,906 and 862 bytes respectively when gzip-compressed.
It includes React, React DOM, and TanStack Query, but not the chosen primitives,
TanStack Table, MapLibre, or a font.

A disposable spike outside the repository used esbuild 0.25.8, minified ESM,
React/React DOM 19.1.0, and concrete renderable compositions. These are comparative
transfer indicators, not a prediction of Vite chunk boundaries:

| Composition | Minified JS | Gzip JS | Increment over spike baseline |
|---|---:|---:|---:|
| React render baseline | 186,446 B | 58,348 B | - |
| Five approved Radix primitives | 294,887 B | 94,404 B | 108,441 B minified / 36,056 B gzip |
| React Aria dialog/menu/tooltip equivalent | 359,045 B | 113,273 B | 172,599 B minified / 54,925 B gzip |
| MUI dialog/menu/tooltip/button equivalent | 391,747 B | 127,951 B | 205,301 B minified / 69,603 B gzip |

The same spike measured a minimal TanStack React Table 8.21.3 composition at about
12.9 KiB gzip above its React baseline. A named `Map` import from MapLibre GL JS
6.1.0 measured 242,495 bytes gzip plus 10,135 bytes gzip for MapLibre CSS. MapLibre
must therefore be route-split and loaded only when Map is requested; prefetching is
allowed only after primary Summary content is usable. The font adds 33,996 bytes
for a first uncached Latin-normal request.

Conservatively loading all approved primitives in the shell, the font, and up to
15 KiB gzip of product CSS puts the expected pre-table initial transfer near 156
KiB gzip versus the current approximately 70 KiB JavaScript plus 1 KiB CSS. Summary
adds roughly 13 KiB gzip for TanStack Table. Opening Map adds an independently
cached approximately 253 KiB gzip MapLibre JS/CSS route chunk, before product map
code. Actual Vite output must be recorded when those surfaces exist.

Set budgets of 175 KiB gzip for initial executable JavaScript, 20 KiB gzip for
product CSS excluding MapLibre CSS, and 300 KiB gzip for the lazy Map route's
MapLibre plus product JavaScript. Font files are reported separately. CI should
report compressed assets and fail only after a reviewed baseline/budget mechanism
is added; a budget increase requires measured user-value justification in the
change that crosses it.

## Consequences

### Positive

- Product CSS retains full control over the visual language, density, themes,
  responsive layout, and map integration.
- Maintained primitives own the most error-prone menu, modal, focus, dismissal,
  portal, and positioning behavior.
- Native forms and semantic tables remain inspectable and testable without a
  comprehensive component abstraction.
- The font and theme bootstrap make first paint private, deterministic, and
  independent of external font services.
- Explicit route splitting keeps MapLibre out of authentication and Summary's
  initial transfer.

### Negative

- The product owns token design, component CSS, visual regressions, and the wrapper
  layer around primitives.
- Radix still adds an estimated 36 KiB gzip when all approved primitives share one
  chunk, and the bundled font adds about 34 KiB on first load.
- A dialog-presented sheet does not provide native drag physics; this is deliberate
  to preserve keyboard parity and avoid another dependency.
- MapLibre's canvas requires explicit theme synchronization and a non-map path to
  essential information.

## Conditions That Would Trigger Reconsideration

Reconsider this decision if Radix drops React support or fails the required
assistive-technology tests, custom component CSS becomes a sustained delivery
bottleneck, complex internationalized widgets dominate the interface, the measured
initial bundle exceeds its budget despite route splitting, or a native platform
primitive reaches the browser support and accessibility quality needed to remove a
dependency. Visual preference alone is not a reason to replace the behavior layer;
the product can change its tokens and CSS without changing this architecture.
