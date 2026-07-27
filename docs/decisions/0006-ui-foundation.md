# ADR 0006: UI Foundation

## Status

Proposed - requires a focused UI spike before Milestone 2 interface work.

## Context

ADR 0001 selects React, TypeScript, Vite, TanStack Query, TanStack Table, and
MapLibre but deliberately leaves styling and component implementation open.
Milestone 2 introduces public authentication screens, dark-default theming, the
authenticated shell, dialogs, an avatar menu, and responsive behavior. A styling
choice made during those screens would otherwise become an accidental platform
decision.

The later Summary and Map views need dense desktop tables, compact mobile rows,
bottom sheets, map overlays, strong light/dark contrast, keyboard access, and a
bundled default font.

## Proposed Decision Process

Run a small, disposable spike that implements:

- the login form and validation states;
- the authenticated desktop header and avatar menu;
- a mobile header and bottom sheet;
- a representative dense table row and expandable mobile row;
- a map control overlay in light and dark themes; and
- a dialog, notification banner, focus states, and reduced-motion behavior.

Compare a minimal custom CSS approach with at most two suitable component or
primitive libraries. The spike evaluates shipped JavaScript and CSS, accessibility,
theme control, responsive ergonomics, MapLibre integration, maintainability, and
visual distinctiveness.

## Proposed Decision Constraints

The accepted choice must:

- use semantic HTML and accessible primitives;
- define product-owned CSS variables for color, typography, spacing, elevation,
  motion, and map/table states;
- restore the persisted theme before primary UI paint;
- bundle the default font or use a deliberately selected privacy-safe source;
- support dark and light themes without duplicating component trees;
- support keyboard, touch, focus, hover, selected, warning, and error states;
- avoid browser-direct Gravatar requests;
- work at documented desktop and mobile breakpoints; and
- avoid tying domain state to a component library's internal state model.

TanStack Query owns server state, and TanStack Table owns table models regardless
of the styling choice. MapLibre remains the map renderer.

## Alternatives To Evaluate

### Product-owned CSS with accessible headless primitives

This provides strong visual control and moderate behavior reuse. It is the
provisional preference if the spike shows acceptable maintenance cost.

### Product-owned CSS without a primitive library

This minimizes dependencies but makes dialogs, menus, focus management, and
bottom sheets security- and accessibility-sensitive custom work.

### Comprehensive styled component system

This accelerates common screens but may produce a generic visual language,
increase bundle size, and make dense map/table layouts harder to control.

### Utility-first CSS

This can improve implementation speed but must demonstrate readable component
code, stable theme tokens, and no conflict with dynamic map styling.

## Acceptance Evidence

Record the selected approach, rejected spike alternatives, bundled font, theme
bootstrap mechanism, breakpoint policy, accessibility checks, and measured
production bundle impact. Change this ADR to Accepted before building Milestone
2 screens; do not merge the disposable spike as application architecture.
