# ADR 0011: Theme-Aware Base-Map Styles

## Status

Accepted

## Context

The Raw Route Map overlays sensitive, authenticated workout geometry on a public
base map. Different public styles emphasize different workout contexts: outdoor
styles expose terrain and trails, street styles emphasize the road network, and
visually quiet styles keep recorded tracks prominent. A single installation-wide
tile URL cannot offer these choices or follow the application's light and dark
themes.

The initial useful choices are MapTiler Outdoor, customized MapTiler Streets,
and Stadia Alidade Smooth. Additional providers and styles must be possible
without adding product code. Public style changes must not disturb the camera,
workout filter, selected route, route order, hover behavior, or authenticated
private overlay.

Workout types originate as provider labels rather than a fixed product enum.
Visible selections may contain one type, several types with the same preferred
style, conflicting preferences, unmapped types, or no routed workouts. The
automatic rule must be deterministic and understandable. A user must also be
able to choose another base map without turning an incidental choice into a
permanent preference.

## Decision

Represent a selectable base map as a style family with:

- a stable configuration ID and user-facing label;
- one light and one dark MapLibre style URL;
- structured provider attribution; and
- an allowlist of public resource origins needed for style documents, tiles,
  glyphs, sprites, and images.

The initial families are Outdoor, Streets, and Alidade Smooth. Alidade Smooth is
the initial configured fallback. Installations may add families or choose a
different available fallback through runtime configuration.

Workout-type defaults map readable provider labels to style-family IDs. The API
normalizes configured labels using the same Unicode normalization and case
folding as workout ingest. Configuration does not use account-specific workout
type UUIDs, mutable display spelling, or opaque hashed keys.

Automatic selection follows these rules:

1. Consider visible workouts that have routes.
2. If every represented workout type maps to the same configured family, select
   that family.
3. If mappings conflict, any represented type is unmapped, no routed workouts
   are visible, or selection is otherwise indeterminate, select the configured
   fallback.
4. Choose the selected family's light or dark variant from the current
   application theme.

The user may select any configured family from desktop Map controls or the
mobile bottom sheet. A manual selection overrides automatic family selection
for the current Map visit only. It continues to follow application theme
changes. Leaving and reopening Map clears the override and reapplies the
automatic rules.

Changing a family or theme replaces the public MapLibre style while preserving
product-owned map state. After the new style loads, the UI reinstalls the
authenticated route or coverage sources and layers, including ordering and
selection styling. Public styles and provider requests never receive private
tile credentials.

The runtime contract validates unique family IDs, both theme variants, HTTPS
resource URLs, structured attribution, mapped family references, and fallback
membership. Attribution is rendered as text and validated links rather than raw
configured HTML, and it remains visible for the active family on desktop and
mobile.

Public provider credentials embedded in browser style URLs are public by
definition. MapTiler keys must use allowed-origin restrictions, minimum
privileges, quotas, monitoring, and rotation. Stadia domain authentication is
preferred where available. The UI content security policy allows only the
configured public provider origins required by the active style resources.

## Consequences

- Provider choice can match route context without coupling product code to a
  fixed workout taxonomy.
- Light and dark maps remain visually consistent with the application theme.
- Mixed or unknown workout selections have a stable, quiet fallback rather than
  changing style according to route ordering or counts.
- A current-visit override gives the user control without suppressing future
  workout-aware defaults or requiring a new persisted preference.
- Every family must supply and maintain two compatible style variants,
  attribution, and complete resource-origin metadata.
- Style replacement requires explicit restoration of private MapLibre layers
  and browser tests that guard map state.
- Self-hosted operators must manage public provider access policy and understand
  that third-party map requests disclose client network metadata and viewed map
  extents to the provider.

## Alternatives Considered

### Independent light and dark choices

Listing each theme variant separately would allow mismatched UI and map themes
and duplicate workout mappings. Grouping variants into families keeps the user
choice stable while the theme changes.

### One global base-map style

A singleton URL is simpler but cannot support route-appropriate defaults or
user choice and does not model provider-specific attribution and resource
origins.

### Persist the manual choice per user

A persisted override would silently suppress workout-type defaults across later
visits and devices and would need removal semantics when configuration changes.
The current-visit override meets the user-control requirement without adding a
preference migration.

### Choose the most common or newest workout type

These rules make the base map change as filters, counts, or ordering change and
privilege one type in a mixed selection. Falling back to Alidade Smooth is
deterministic and keeps the private tracks visually prominent.

### Map defaults by stored workout-type UUID or hashed key

UUIDs are account-specific and hashed keys are difficult for operators to
discover and maintain. Readable provider labels are suitable configuration when
normalized by the same canonical algorithm as ingest.

### Proxy all public map resources through the API

Same-origin proxying could conceal provider topology from the browser but would
make the application a bandwidth-heavy public tile cache and would not make
browser-use provider credentials inherently secret. Direct provider requests
remain the default boundary established by ADR 0003; private workout tiles
continue to use the authenticated API proxy.
