# UI Style Guide

## Golden Rule

**New UI compositions must maintain visual and behavioral consistency with
previously implemented elements and adapt to the selected light/dark theme.
Prefer prior art whenever implementing something similar.**

Before designing a new table, dialog, dropdown or control, find its closest
existing counterpart. Reuse its components, semantic styles and interactions;
do not build a parallel visual language for a new tab.

This is a reminder for implementation and review, not a stylesheet specification.
It prescribes no CSS values, dimensions, colors or timing constants. Obtain those
from the shared implementation and the applicable feature contract. An old
screenshot, a historical rollout or a known inconsistency is not a new precedent.

The [functional specification](functional-spec.md) owns feature-specific labels,
availability and behavior. [Architecture](architecture.md) and the
[UI foundation decision](decisions/006-ui-foundation.md) own implementation
boundaries. This guide consolidates their reusable presentation principles;
feature requirements and accepted exceptions remain in their original documents.

## Start With Existing Compositions

| Need | Prior art to inspect and reuse |
|---|---|
| Section headings, aggregates, workout tables, row menus and provenance | [Summary](../ui/src/Summary.tsx) |
| Dense searchable dialogs, clickable identity/date cells, focus and fit | Road Coverage in [MapPage](../ui/src/MapPage.tsx) |
| Map path opacity and provider/private overlay stacking | [mapPresentation](../ui/src/mapPresentation.ts) and the [layer contract](functional-spec.md#map-layer-stack-and-opacity) |
| Report sidebar, grouped table replacement and table-to-Map navigation | [StatsPage](../ui/src/StatsPage.tsx) |
| Session-owned tab retention, visibility and scroll ownership | [RetainedTab](../ui/src/RetainedTab.tsx) and the [workspace contract](functional-spec.md#feature-retain-tab-workspaces) |
| Shared first/last, single-step and multi-page navigation | [PageNavigation](../ui/src/PageNavigation.tsx) |
| Guarded invisible keyboard accelerators and table ownership | [keyboardShortcuts](../ui/src/keyboardShortcuts.ts) and the [shortcut contract](functional-spec.md#feature-keyboard-shortcuts) |
| Consistent tab content backgrounds and clipped decorative atmosphere | [ContentBackground](../ui/src/ContentBackground.tsx) and the shared content-background rule in the [product stylesheet](../ui/src/styles.css) |
| Task filters, disclosures, progress and notifications | [DataSync](../ui/src/DataSync.tsx) |
| Shared period selection and custom-range editing | [DateRangePicker](../ui/src/DateRangePicker.tsx), [CustomDateRangeDialog](../ui/src/CustomDateRangeDialog.tsx), [RecentDateRanges](../ui/src/RecentDateRanges.tsx) |
| Styled selection controls and menus | [PreferenceSelect](../ui/src/PreferenceSelect.tsx) and existing action/filter menus |
| Delayed help and truncated table values | [Tooltip](../ui/src/Tooltip.tsx), [CoverageTableCell](../ui/src/CoverageTableCell.tsx) |
| Independently fitted aggregate value lines | [FittedAggregateValue](../ui/src/FittedAggregateValue.tsx), used for every Range totals value and both paired counts |
| Frozen table viewport and centered empty/loading feedback | [DialogTableViewport](../ui/src/DialogTableViewport.tsx) |
| Date display, shared typography, spacing, states and themes | [Date helpers](../ui/src/date.ts), [shared Intl formatters](../ui/src/formatters.ts), [Summary formatters](../ui/src/Summary.tsx), [product stylesheet](../ui/src/styles.css) |

Reuse the composition, not just its appearance. Focus return, loading behavior,
keyboard controls, scrolling and state retention are part of the component.
Prefer a small shared component or semantic rule over copying a substantial
block of markup/CSS. Keep product state separate from headless widget internals.

## Theme, Typography And Visual Hierarchy

- Use the bundled application font and established heading, body, table and
  numeric treatments. A sidebar section heading should look like the equivalent
  section heading elsewhere; report titles should use the existing title hierarchy.
- Consume shared semantic theme and spacing tokens. Avoid isolated hardcoded
  colors, font stacks, offsets or dimensions copied from a single screenshot.
- Every tab's content area uses the same shared background composition: the dim
  grid, theme-aware gradient and stepped corner contours. Reuse `ContentBackground`
  with the shared `content-background` surface instead of creating tab-specific
  variants. This also applies beneath the Map canvas and in its loading state.
  Preserve intentional sidebar/card surfaces, and keep decorative layers clipped,
  noninteractive and hidden from assistive technology. Verify consistency in both
  themes and at desktop/mobile sizes.
- Check both themes for text, muted/unavailable values, surfaces, borders, hover,
  focus, selection, warnings, errors and disabled states. Preserve the pre-paint
  theme behavior; new content must not flash the opposite theme while loading.
- Keep status meaning independent of color through icons, text and accessible
  names. Do not reuse a success treatment for pending, unavailable or failed data.
- Draw small circular status marks and their circles in one SVG coordinate
  system, following the Coverage route badges. Avoid font glyphs or separately
  positioned CSS hands whose alignment depends on baseline or rasterization.
- Keep metric/date alignment, numeric typography, precision and missing-value
  presentation consistent. Unavailable is not zero; use the established muted
  unavailable treatment, and do not fabricate a date or measurement.
- For an overflowing headline value, prefer fitting just that line to its
  available width over widening the composition or shrinking every card's text.
  Use the normal CSS font size as a ceiling, reduce only as much as needed, and
  restore it when space/value/font loading changes permit. Do not introduce coarse
  font-size steps or let measurement itself cause a visible flash.
- Respect the user's units, clock format and time zone. Use existing formatters
  for the same kind of date or timestamp; extra precision is not automatically
  better. UI display, API dates and documentation date conventions are distinct.
- Reuse bounded formatter configurations instead of constructing internationalized
  formatters for every cell/render. Resolve each date and offset for its actual
  instant, including DST transitions. Formatter caches contain configuration,
  not private row values or per-zone offsets assumed constant across dates.
- Prefer concise labels and contextual help to redundant subtitles, notices and
  captions. Preserve useful domain distinctions rather than exposing internal IDs,
  provider keys or implementation terminology as user-facing labels.

## Clickability, Hover And Keyboard Focus

- All actionable dropdown triggers/panel items, buttons, links, sortable headers and clickable
  rows/cells use a hand cursor over their entire activation area. Text-entry
  fields retain their editing cursor; unavailable controls must not falsely
  advertise an available action. Shared paging follows the
  [no-flash pending-state contract](functional-spec.md#feature-navigate-table-pages).
- Make the padded target clickable, not just a small number or a short label.
  Use a real button for an action and a real link for navigation, with an
  accessible name and a single intentional keyboard stop.
- Match existing hover borders, text colors, row highlights and selected states.
  Table actions follow Road Coverage: normal text until the pointer enters the
  cell, then accent text, without an extra cell background or default underline.
  Preserve the normal row-level highlight.
- Close buttons use the existing dialog hover-border treatment. Hover must not
  change control size, move adjacent content or substitute for a selected state.
- Keyboard navigation must expose a visible, unobscured focus indicator. Preserve
  the application's input-modality behavior instead of showing keyboard rings
  after ordinary pointer clicks or pointer-initiated text editing.
- Only Tab/Shift+Tab initiates keyboard-navigation focus highlighting. Application
  shortcuts, arrow keys and other key commands preserve the current mode rather
  than enabling it; pointer interaction exits that mode. Programmatic content-area
  and canvas handoffs retain actual focus without outlining the entire large
  surface. Smaller actionable controls still show their normal rings after Tab.
- Inside scrolling or rounded/clipped frames, keep the focus ring within the
  target's available surface and clear of its content. Do not remove deliberate
  clipping or scroll ownership to expose an outward ring. Segmented choices need
  visible focus on both selected and unselected options, with contrast against
  their actual fill; selection alone is not a focus indicator. Respect the
  system focus palette in forced-colors mode.
- Shared sortable-table headers use the same cell-wide inset focus treatment
  across tabs and dialogs, including tables whose frames would permit an
  outward outline. Keep the indicator inside the cell borders.
- Distinguish visibility, selection, focus and activation. For example, a route
  checkbox changes visibility; it must not silently focus, fit or change the map.
- Essential actions cannot depend on mouse hover or a long press alone. Preserve
  the equivalent keyboard/touch route and its accessible selected/expanded state.
- Route application accelerators through the shared guarded keyboard dispatcher.
  Preserve text-entry visibility, native widget/menu keys, modal ownership and
  committed state. Follow the feature's focus handoff and discovery contract;
  invisible accelerators do not imply extra visual legends or sequential stops.
- A searchable table dialog can page with arrow shortcuts while its search field
  is visible but unfocused. Focused text entry keeps caret/selection keys; paging
  affects only the active dialog. Keep the separate number/letter shortcut guard
  for displayed editing surfaces and preserve Tab-only focus-ring activation.
- Passive card previews have one intentional keyboard stop on the owning card.
  Preview-only hints must not insert focusable descendants into content that
  closes when the card blurs. Keep closed previews inert, and allow overflowing
  content to be scrolled from the focused card without introducing a second stop.
- Related previews share one owner across mouse and keyboard input; superseded
  focus/blur events must not reopen or dismiss another preview. Temporary modifier
  holds extend only the intended pointer region, reset on focus/visibility loss,
  and preserve ordinary keyboard navigation. Expanded click areas delegate to
  the owning action without duplicate activation or extra Tab stops. Row hover
  emphasis reuses theme colors and keeps both labels and muted values readable.
- Hover state must recover when OS window switching omits leave or modifier-keyup
  events. Reconcile the live pointer against the allowed region rather than
  relying on a cached hover chain, and preserve genuine keyboard-only previews.
  OS/browser switching shortcuts must remain unconsumed and must not be mistaken
  for fresh focus-navigation intent. Standalone modifiers do not imply focus loss
  or dismiss an otherwise valid hover preview.

## Tooltips And Overflow

- **Never use native HTML tooltips.** Use the application's styled tooltip
  component and its shared hover delay; do not add a duplicate `title` tooltip.
  Preserve explicit feature-specific hover/lookup timing where specified rather
  than inventing another independent delay.
- Make supplemental help available through the supported keyboard-focus path
  without adding redundant tab stops. Essential instructions/actions must remain
  available without a tooltip.
- Respect active interaction: pressing a control dismisses or suppresses its
  pending help where established, and opening its menu must not reveal a delayed
  tooltip over that menu. Cancel pending reveals on dismissal or context changes.
- Clip long table values to their padded column width with an ellipsis. Show the
  full value only when it is actually truncated, using the shared styled help.
  Recheck after resizing, font loading or text changes without replacing focus.
- Portal tooltip content above the relevant dialog/scrolling surface. Avoid
  clipping and arbitrary competing stacking rules. Passive map help must not
  intercept pointers or cover controls/attribution needed for interaction.
- Keep one active tooltip owner. Scroll, resize, blur or an anchor moving away
  must remove obsolete help; a superseded tooltip cannot reopen itself.

## Tables, Sorting And Pagination

- Begin with an existing semantic table, scoped headers, accessible sort state
  and the standard sort indicators. Use documented initial directions, stable
  tie-breaks and the established handling of unavailable values.
- Respect the rows-per-page preference and its configured maximum for report and
  dialog tables. Use the shared page controls, alignment, button widths and
  table-to-pagination spacing instead of a feature-specific extra gap.
- **Keep the current rows mounted during compatible paging, sorting and incremental search.**
  Fetch first, then replace cell values in place; do not erase/repopulate the body,
  flash loading rows or unnecessarily dim already displayed content. After an
  accepted partial result, hide surplus row slots rather than rebuilding the
  visible rows. Block obsolete row actions while the replacement is pending.
- An accepted zero-result table keeps its header but no result rows. Show its
  empty message as slightly larger, muted text centered horizontally and vertically
  in the visible reserved results area below the header, outside the table body.
  Keep it centered when the table scrolls horizontally; do not use a first-row
  message or fake result rows to maintain height.
- Reserve a full page of row space when the result spans multiple pages. Previous
  and Next must stay in the same position on the last partial page too. A short
  unfiltered result can establish a shorter initial frame. Preserve document and
  internal table scroll during replacement.
- **Freeze the size of every dialog containing a table from its complete
  unfiltered result set, irrespective of table contents.** Include items reachable
  through display filters that are off by default, within the active data scope,
  rows-per-page preference and feature's visible-row limit. Search, secondary
  filters, partial pages, loading replacements and no-match results retain that
  row capacity using blank space rather than shrinking/recentering the dialog or
  moving the search field. Reserve pagination-label space too, so changing counts
  cannot rewrap the footer and move the dialog. Restored filters/pages must size
  from the full unfiltered set, never from the smaller restored result; clearing
  a search then reveals rows without enlarging the frame. Viewport changes can
  adapt containment to keep controls reachable.
- Grouping changes follow the same no-flash principle. Retain the old rows with
  their correct header/labels until the new grouping is ready, then update them
  atomically. A shorter grouping may shrink after arrival; do not retain obsolete
  labels or force its body to remain as tall as the longer grouping.
- Block ambiguous old actions while replacement data is pending. Shared paging
  retains its displayed boundary states and ignores repeat activation during
  loading and background refresh. Fence late responses so rapid paging, sorting
  or grouping cannot install an older result.
- Continuity is not permission to retain another account's data or an obsolete
  date, bucket, metric or dataset scope. Clear private state at those boundaries.
  Incremental search within the same authenticated dataset is a compatible update:
  retain the prior rows read-only until the latest response is ready. Distinguish
  initial loading, refresh, empty results and errors; failure is not a zero count.
- Keep report selection, table sort and page in the owning frame when tab
  switching or drill-downs must preserve them. Charts do not need table page
  state. Clamp/reset pages only according to the relevant feature contract.
- When reusing positional page cells, keep action menus keyed to the actual
  record identity: an open menu must not silently retarget a replacement row.
  Build expensive closed-dialog choices only when opened and collapsed secondary
  detail content only when expanded. Calculate shared column-width samples once
  per table render rather than once per cell.

## Dialogs And Confirmation Flows

- Use the established modal primitives, required title/description, overlay,
  viewport containment and explicit dismissal path. Do not create bespoke focus
  traps or a different close-button treatment for one feature.
- Match Road Coverage's compact content insets for comparable Summary/custom-range
  dialogs. Keep outer content padding, spacing above/below footer dividers,
  bottom/right action insets and gaps between peer buttons visually consistent.
- Keep headers and controls usable with long content. Assign scroll ownership
  deliberately, avoid unwanted nested scrolling, and prevent sticky headers from
  obscuring borders or focused content.
- Size a dialog to its columns/content, not to an unrelated full-width table.
  A short unfiltered set can establish less height initially. Apply the generic
  frozen table-dialog rule during filtering, paging and restored-state review.
- Align related content to shared edges. For example, a right-aligned provenance
  timestamp should end at the close button's right edge even with a scrollbar,
  and should reuse the Workout log date's formatting and emphasis.
- Escape and close/cancel controls dismiss as specified and restore focus to the
  invoking control or an appropriate surviving fallback. Closing a modal should
  reveal the existing underlying view, not recreate it or lose its state.
- Preferences returns focus to the active tab's content area on every close path,
  including Save, Cancel, close button, Escape and outside dismissal. Map uses its
  canvas as that content target, with the existing readiness handoff when needed.
  Run this after modal close-focus handling so the profile trigger cannot reclaim
  focus and receive the next Space press.
- Destructive confirmation names the actual target/scope and initially focuses
  the least destructive action. Preserve required confirmation text, pending
  guards, error handling and acceptance semantics while restyling it.

## Dropdowns, Pickers And Forms

- The hand cursor applies to clickable **items inside the panel**, not just its
  trigger. This includes avatar/profile actions, date shortcuts and recent ranges,
  base-map choices, Workout log row context actions, task-history filter choices
  and styled select options. Cover each item's whole padded row and its child
  labels/checkmarks. Disabled items, labels and separators must not imply clickability.
- Reuse shared date pickers, styled selects, action menus and segmented choices.
  The same control should not acquire a different appearance or behavior on a
  newly added tab.
- Match the established trigger width, aligned edges, opening anchor and spacing.
  Long panels scroll within the available viewport rather than drifting from the
  requested anchor. Respect feature-specific anchors such as a shared filter row.
- Preserve keyboard arrows/typeahead, selection indicators, Escape/outside-click
  dismissal and return focus. Use action menus for actions, not primary navigation.
- Keep checkbox/label activation regions coherent. Multi-select drafts use the
  established Apply/Cancel behavior; dismissing must not commit an unfinished draft.
- Use visible labels, fieldsets/legends, field errors and accessible error
  summaries. Keep submission and date validation behavior when replacing controls.

## State, Navigation And Background Refresh

- Keep visited tab workspaces mounted in the signed-in frame. Hide and make
  inactive content inert without disposing expensive renderers or rebuilding
  unchanged tables. Unvisited tabs stay lazy. Pause inactive polling and global
  interactions, close transient portals, and restore the active content's focus
  and scroll. Retention is private to the current session/identity; it does not
  bypass data-scope expiry, invalidation or fresh action eligibility.
- Preserve the shared date range and intended tab/report state unless an explicit
  feature contract permits changing them. Coverage job route links may use the
  shared range-aware Map action to select the first encompassing preset. Stats
  drill-downs preserve the report range; out-of-range Previous visit dates remain
  noninteractive even though range-aware navigation is available elsewhere.
- Reuse the existing Map focus, fit, exact-geometry blink and list-scroll flows.
  A report view must not mount a map, fetch geometry or run matching merely to
  display a table. Theme/style changes preserve applicable camera/layer state.
- All entry points to the same identity highlight share one camera/detail/blink
  flow. Restore the previous owner-scoped camera when returning to Map and avoid
  fallback, all-routes or flight-arc detours while an explicit target is pending.
  Fit the authoritative target once; a cold map can initialize at its exact bounds
  rather than display an unrelated world view first. Keep camera memory private
  to the signed-in frame instead of persistent browser storage.
- An identity blink is temporary emphasis, not a reset of the Map workspace.
  Retain the base-map override/last automatic family, checked routes and focused
  workout through report handoffs. A visit-date action may intentionally focus
  its target workout; an identity action must not clear the existing focus.
- Background polling should not reset pages, filters, disclosures, focus or
  scroll. Keep manual refresh behavior where specified, and pause hidden-view
  work according to the owning feature's policy.
- Transient refresh feedback must not shift the report table or pager. Prefer the
  shared floating Map-banner treatment, keep persistent recovery actions usable,
  and suppress brief recovery warnings when compatible results remain visible.
  Center Map feedback on the canvas and align its upper anchor to the measured
  Coverage legend center; mirror that anchor for exact-identity feedback below.
- An expired data scope is distinct from expired authentication. Recover from
  stale scopes/transient failures in place, with bounded retries and visible
  retry paths; do not require a tab switch to unstick the view.
- Preserve last-validated compatible results during recovery, but disable stale
  actions until fresh authorization/data is accepted. Cancel obsolete requests;
  late results must not reopen dismissed UI or restore deleted items.
- Notifications and failures have an explicit dismissal/recovery path. Present
  trustworthy status and user-safe errors, not raw private/internal diagnostics.

## Responsive Behavior, Motion And Review

- Follow the current [responsive scope](functional-spec.md#feature-use-the-responsive-interface)
  rather than inferring new device support from an old ADR or a narrow screenshot.
  Preserve semantic controls and touch/keyboard alternatives, readable contrast,
  visible attribution and reachable dialog actions at supported sizes and zoom.
- Use purposeful, reduced-motion-aware transitions. Do not animate table layout
  or introduce decorative movement that causes flashing, obscures data or is
  required to understand a state. Preserve feature-defined feedback such as Map
  highlighting without inventing new timing values here.
- Keep shared typography separate from feature-specific layout. A later shared
  heading rule must not accidentally reset a local margin. Check computed styles,
  source order, specificity, inherited tokens and theme overrides before adding
  another override or an `!important` workaround.
- Review beside the existing counterpart in both themes. Test pointer, keyboard,
  focus return, disabled/error/empty states, long names and long dialog histories.
- Exercise deliberately delayed and out-of-order responses. Check for populated
  frames during paging/grouping, stable page-control positions and sensible
  shrinkage after a shorter result arrives.
- Run the applicable component/CSS/CSP/type/build checks from the
  [verification guidance](source-code-formatting.md#verification). Use real
  browsers for actual spacing, hover delay, clipping, scrolling and font loading;
  DOM-only tests and a successful screenshot capture do not prove those qualities.

## Design Sources

This guide draws reusable rules from the functional specification's preferences,
Summary, Stats, row actions, Road Coverage, Data Sync and responsive-interface
contracts; the architecture's UI, overlay and report-state sections; and the
current review fixes recorded in [Development Progress](development-progress.md).
More specialized rules remain in their owning documents:

- [UI Foundation](decisions/006-ui-foundation.md): primitives, themes, typography,
  semantics, reduced motion and accessibility responsibilities.
- [Theme-Aware Base-Map Styles](decisions/011-theme-aware-base-map-styles.md) and
  [Coverage Count Buckets](decisions/010-coverage-count-buckets.md): map-specific
  theme/state rules and accepted visual semantics.
- [Coverage Readiness](coverage-readiness.md) and
  [Canonical Display Identity](decisions/013-canonical-coverage-display-identity.md):
  trustworthy status, disabled actions and consistent identity across surfaces.
- [Job State Machine](job-state-machine.md), [Ingest Reprocessing](ingest-reprocessing.md)
  and [iCloud Setup](icloud-source-setup.md): progress, retry, dismissal and help
  must describe the actual operation rather than imply a different side effect.
- [Coverage Acceptance](coverage-acceptance.md): browser evidence and the distinction
  between successful capture and a completed visual/accessibility review.
