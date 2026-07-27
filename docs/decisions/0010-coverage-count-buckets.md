# ADR 0010: Coverage Count Buckets

## Status

Proposed - requires historical coverage distribution analysis during Milestone
7 and must be accepted before Coverage rendering is accepted.

## Context

Coverage uses fixed blue buckets based on distinct workout counts. A dynamic
scale would let a few heavily visited paths flatten visual differences among
rarely visited paths, while arbitrary fixed boundaries may waste colors or hide
meaningful variation.

The same boundaries must drive vector-tile properties, the MapLibre style,
legend labels, hover interpretation, screenshots, and browser tests. They are a
product interpretation of historical data rather than an operator tuning knob.

## Proposed Decision Process

After representative historical matching is available, calculate the
distribution of all-time and selected-period distinct workout counts across:

- the full owner history;
- common 7-day, 30-day, month, and year selections;
- urban roads and lower-frequency trails; and
- synthetic additional accounts that do not expose private values in artifacts.

Evaluate candidate bucket sets using percentile occupancy, visual distinction
for counts one through five, stability under outliers, and legend readability on
desktop and mobile. Keep the number of rendered classes small enough for a clear
legend and efficient vector styling.

## Proposed Rule Constraints

- Count means distinct workouts, never point matches or traversals.
- Zero-count paths are not rendered as covered.
- Boundaries are fixed and shared by all accounts and date ranges.
- The highest bucket is open-ended.
- Colors form a sequential blue scale with accessible contrast in both themes.
- Hover details always show the exact count even though color is bucketed.
- Bucket assignment is deterministic in tile SQL or a shared contract, not
  duplicated independently in browser code.
- Changing accepted boundaries requires a new decision and visual-regression
  update, but does not require rebuilding attribution.

## Alternatives To Evaluate

### Linear fixed buckets

Easy to explain but likely to spend too many colors on high counts and too few on
the common one-to-five range.

### Approximately logarithmic fixed buckets

Likely to preserve low-count distinctions and tolerate high-count outliers. This
is the provisional preference to test against historical data.

### Quantiles per request or account

Quantiles use every color but make the same color mean different counts across
accounts and periods. They conflict with a stable legend and are rejected.

### Continuous color interpolation

A continuous scale is vulnerable to outlier flattening and is rejected by the
product requirement for fixed buckets.

## Acceptance Evidence

Before acceptance, record anonymized distribution summaries, candidate boundary
occupancy, the selected concrete boundaries and colors, exact legend labels,
tile bucket semantics, and visual checks for light theme, dark theme, mobile,
color-vision deficiencies, and high-density maps.
