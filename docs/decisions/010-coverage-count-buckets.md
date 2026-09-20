# ADR 010: Coverage Count Buckets

## Status

Accepted on 2026-09-13 and amended on 2026-09-14. Production Coverage uses the fixed six-class
approximately logarithmic contract defined below. Historical distribution and
visual checks validate the accepted contract and inform a future ADR revision;
they do not block its initial implementation.

## Context

Coverage uses fixed buckets based on distinct workouts attributed to a
locality-scoped logical path. All visited member segments of that path use the
same count bucket, while unvisited member segments are not rendered. A dynamic
scale would let a few heavily visited paths flatten visual differences among
rarely visited paths, while arbitrary fixed boundaries may waste colors or hide
meaningful variation.

The same boundaries drive vector-tile properties, the MapLibre style, legend
labels, hover interpretation, screenshots, and browser tests. They are a product
interpretation of historical data rather than an operator tuning knob.

## Decision

Color represents the number of distinct current workouts in the selected date range
that are attributed to a locality-scoped logical path. It does not use the
path's all-time count unless the current selection itself represents all owner
history. The accepted v1 boundaries and legend labels are:

| Bucket | Distinct date-range workouts | Legend label |
| ---: | ---: | --- |
| 1 | 1 | `1 workout` |
| 2 | 2 | `2 workouts` |
| 3 | 3-5 | `3-5 workouts` |
| 4 | 6-10 | `6-10 workouts` |
| 5 | 11-25 | `11-25 workouts` |
| 6 | 26 or more | `26+ workouts` |

Checked routes control which covered geometry is visible but do not change this
range-wide count. The focused route overlays its exact attributed geometry using
the corresponding bucket from a separate palette.

The tile contract emits exact `range_workout_count` and integer
`count_bucket` properties. `count_bucket` is 1 through 6 for rendered coverage;
zero-count paths are absent rather than assigned bucket 0. Tile SQL is the
authoritative bucket implementation. Browser styling and legends consume the
emitted bucket and must not independently recalculate boundaries.

The accepted non-focused colors, from bucket 1 through 6, are `#d95d0b`,
`#ed7d0c`, `#f59e0b`, `#f7b928`, `#f9d64a`, and `#fff176`. The accepted focused
colors are `#ff008c`, `#ff5fb4`, `#ff8bc8`, `#ffaad2`, `#ffc9e1`, and `#ffd8f0`.
The first five focused colors are luminance-matched to their corresponding
aggregate colors. Bucket 6 is intentionally about 10% darker than that match so
the lightest hot pink remains visibly distinct from white.
Both sequences become brighter with increasing count and are shown together in
each legend entry.

## Rule Constraints

- Count means distinct workouts per logical path, never point matches, matched segments, or traversals.
- Zero-count paths are not rendered as covered.
- Boundaries are fixed and shared by all accounts and date ranges.
- The highest bucket is open-ended.
- Non-focused colors form an amber-orange-yellow sequence; focused colors form
  a purple-magenta-pink sequence with comparable luminance.
- Hover details always show the exact count even though color is bucketed.
- Bucket assignment is deterministic in tile SQL, not duplicated in browser code.
- Changing accepted boundaries requires a new decision and visual-regression
  update, but does not require rebuilding attribution.

## Alternatives Considered

### Linear fixed buckets

Easy to explain but spends too many colors on high counts and too few on the
common one-to-five range.

### Approximately logarithmic fixed buckets

Preserves low-count distinctions and tolerates high-count outliers. This is the
accepted approach.

### Quantiles per request or account

Quantiles use every color but make the same color mean different counts across
accounts and periods. They conflict with a stable legend and are rejected.

### Continuous color interpolation

A continuous scale is vulnerable to outlier flattening and is rejected by the
product requirement for fixed buckets.

## Validation

Validation is intentionally practical:

- Count how many logical paths land in each bucket for full history and common
  7-day, 30-day, month, and year selections. Empty or overwhelmingly dominant
  buckets indicate that a future boundary revision may be useful.
- Confirm very frequently visited paths stay in the open-ended final bucket and
  do not change the colors assigned to less-visited paths.
- Confirm counts 1, 2, 3, 4, and 5 remain understandable through exact hover
  counts even where some values share bucket 3.
- Review the map and paired legend on desktop and mobile, in light and dark
  themes, and with common color-vision simulations. Luminance and line prominence
  must distinguish classes within each palette without relying on hue alone.
- Review dense urban selections to ensure six line classes remain legible and do
  not obscure route interaction or base-map context.

Distribution summaries and screenshots must contain only aggregate or synthetic
values. No route coordinates, path names, workout dates, or account identifiers
belong in committed validation artifacts.
