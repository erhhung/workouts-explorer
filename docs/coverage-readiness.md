# Coverage Readiness

Coverage readiness has three independent dimensions:

```json
{
  "mapDataStatus": "ready",
  "processingStatus": "failed",
  "resultStatus": "current"
}
```

The statuses answer different questions:

- `mapDataStatus`: Can the matcher access all required OSM data?
- `processingStatus`: What is happening with the desired Coverage computation?
- `resultStatus`: Is there a previously applied result, and does it match the
  current route, matcher policy, and OSM generations?

Keeping these dimensions separate allows the application to distinguish a route
that has never been processed from a failed refresh whose prior result remains
usable.

## Status Enums

### `mapDataStatus`

```text
pending
unavailable
ready
```

### `processingStatus`

```text
unprocessed
queued
running
current
failed
stale
```

### `resultStatus`

```text
none
current
stale
```

## Map Data Readiness

`mapDataStatus` describes whether the OSM prerequisite for matching is
available. It does not describe whether a Coverage job has been queued or run.

| Status | Meaning | Expected behavior |
| --- | --- | --- |
| `pending` | Required map data has not been resolved yet, or a known provider region does not yet have an active OSM generation. | Retain the raw route, report that Coverage is pending, and poll on the bounded processing interval when progress is expected. |
| `unavailable` | Required map data cannot currently be supplied. Examples include no provider region, automatic region addition being disabled, or an eligible region exceeding its download limit. | Retain the raw route, omit it from current Coverage, explain why map data is unavailable, and do not poll indefinitely. |
| `ready` | Every required provider region has an active generation that can be used by the matcher. | Interpret `processingStatus` and `resultStatus` to decide whether to render Coverage or wait for processing. |

The internal database readiness state `unresolved` maps to public
`mapDataStatus: pending`. `unresolved` is a short-lived internal transition and
does not need to be part of the public contract.

## Coverage Processing Readiness

`processingStatus` describes the desired Coverage computation for the current
route input, matcher contract, and OSM generation vector.

| Status | Meaning | Expected behavior |
| --- | --- | --- |
| `unprocessed` | Map data is ready, but no matching job has started for the desired target. | Show that Coverage has not been processed. Queue work when eligible. |
| `queued` | A Coverage route job is waiting for a worker and matcher admission. | Present as pending and poll on the bounded interval. |
| `running` | A worker is actively matching the route. | Present as pending and poll on the bounded interval. |
| `current` | The applied result matches the current route revision, matcher and sampling versions, path policy, and OSM generation vector. | Render the applied result normally. |
| `failed` | The latest attempt for the desired target ended unsuccessfully. | Stop ordinary pending polling, report the safe failure category, and offer or await an eligible retry. A prior result may remain renderable as stale. |
| `stale` | The desired target differs from the applied target and no current queued or running job represents it. A completed child may also become stale when its target was superseded before persistence. | Preserve any prior applied result as stale and queue or reconcile the desired target when eligible. |

Queued and running are separate durable processing states, but both are
presented to the user as pending work.

## Applied Result Status

`resultStatus` describes the last successfully applied Coverage computation. It
does not mean that the computation necessarily produced matched geometry.

| Status | Meaning | Expected behavior |
| --- | --- | --- |
| `none` | No Coverage result has ever been applied for this workout. | There is no prior Coverage to render. |
| `current` | The applied result matches the desired route revision, matcher contract, and OSM generations. | Render normally. |
| `stale` | An applied result exists, but it does not match the desired target. | Keep rendering the prior result where the product contract permits it and clearly identify it as outdated. |

A successful no-evidence computation is an applied result. It therefore has
`resultStatus: current` even though it produces no matched segment geometry.

The application can derive `resultStatus` by comparing existing applied metadata
with the desired route revision, matcher contract, and OSM generation vector. It
must not derive result status from processing status alone:

| Applied result exists | Applied target matches desired target | Result status |
| --- | --- | --- |
| No | Not applicable | `none` |
| Yes | Yes | `current` |
| Yes | No | `stale` |

For example, a redundant recomputation may fail without invalidating an existing
result that still matches the desired target. That state is valid:

```json
{
  "mapDataStatus": "ready",
  "processingStatus": "failed",
  "resultStatus": "current"
}
```

## What Stale Means

Stale does not mean failed, corrupt, or unusable. It means that a successfully
applied result exists, but the desired Coverage target has changed. Causes
include:

- a new route-input revision;
- a newer active OSM generation;
- a matcher, sampling, or path-policy version change;
- cancellation of replacement work after an older result was applied; or
- a child finishing after its immutable target was superseded.

The prior result remains a valid record of the older target. It must not be
silently presented as current, but a failed or in-progress replacement must not
delete it. Atomic replacement promotes a new result to current only after the
new computation passes all revision, policy, and generation fences.

## Pending, Failed, and Unavailable

Pending, failed, and unavailable all mean that the desired current Coverage
result cannot be rendered yet, but they require different user and system
behavior.

| Condition | Transient? | Poll? | Retry or corrective action |
| --- | --- | --- | --- |
| `mapDataStatus: pending` | Yes, when region discovery or activation is progressing. | Yes, on the bounded interval. | The OSM region lifecycle must complete. |
| `processingStatus: queued` | Yes. | Yes, on the bounded interval. | Wait for worker and matcher capacity. |
| `processingStatus: running` | Yes. | Yes, on the bounded interval. | Wait for the active route job. |
| `processingStatus: failed` | No for that attempt. | No indefinite pending poll. | Retry the eligible failed route or wait for desired work to advance. |
| `mapDataStatus: unavailable` | No until configuration or provider data changes. | No indefinite pending poll. | Enable or add the region, select a supported provider, or change the configured size limit. |

Pending must not be used as a synonym for failed or unavailable:

- Pending promises that progress is expected and justifies polling.
- Failed records a terminal processing outcome and requires retry semantics.
- Unavailable records a missing map-data prerequisite and requires a
  configuration or provider-data change.

Both pending and failed processing may have `resultStatus: current` or
`resultStatus: stale`. A current applied result remains fully usable despite the
separate processing attempt. A stale applied result remains available as outdated
Coverage while the desired result is pending or failed. If `resultStatus` is
`none`, the workout is omitted from rendered Coverage and included in the
corresponding pending, failed, or unavailable omission summary.

## Representative Combinations

| Map data | Processing | Result | Interpretation |
| --- | --- | --- | --- |
| `ready` | `unprocessed` | `none` | Matching has not started and no prior result exists. |
| `ready` | `queued` | `none` | Initial matching is pending. |
| `ready` | `queued` | `stale` | Replacement is pending while prior Coverage remains available. |
| `ready` | `running` | `stale` | Replacement is running while prior Coverage remains available. |
| `ready` | `failed` | `none` | Initial matching failed; no Coverage can be rendered. |
| `ready` | `failed` | `current` | A processing attempt failed, but the applied result still matches the desired target. |
| `ready` | `failed` | `stale` | Refresh failed; render prior Coverage as outdated. |
| `ready` | `current` | `current` | Desired Coverage is fully current. |
| `ready` | `stale` | `stale` | Prior Coverage exists and reconciliation is required. |
| `pending` | `unprocessed` | `none` | Required OSM data is expected but not active yet. |
| `unavailable` | `unprocessed` | `none` | Required OSM data cannot currently be supplied. |

## Map Presentation

Routes mode identifies each workout with its workout-type color. Production
Coverage mode replaces that color with an accessible status indicator:

| Status | Indicator |
| --- | --- |
| Current result | Success check |
| Stale result | Amber clock |
| Pending work | Activity ring |
| Failed processing | Exclamation mark |
| Unavailable map data | X |
| Unprocessed | Dash |

The icon and accessible label carry meaning independently of color. A workout
with `resultStatus: none` remains checkable but its non-checkbox row action is
de-emphasized and disabled in production Coverage mode because there is no result
to focus. After the standard tooltip delay, hovering or focusing that disabled
row action explains the reason with concise text such as **Map data not
available**, **Waiting for map data**, **Waiting for coverage update**, or
**Coverage update failed**. Current and stale results remain focusable.

The application polls every ten seconds while a checked workout has no applied
result and is in a transient state. Polling stops for failed or unavailable
workouts and stops entirely when every checked workout has a current or stale
result. Readiness polling replaces the private map capability only when returned
statuses change, so an unchanged poll does not flash loading state or reinstall
map layers.

### Facility And Park Tooltips

Map tiles, focused overlays, Road Coverage rows, and hover details use the same
entity identity and distinct-workout visit counts. Unnamed education-attributed
paths are grouped under the facility's education ID and name for schools,
colleges, universities, and other education facilities. A facility name inherited
by a path does not make it an originally named road. Originally named campus
paths retain their own logical path ID, name, and tooltip; education attribution
is applied only to originally unnamed paths by the OSM derivation.

Park-attributed coverage uses the park ID and name for local parks, nature
reserves, protected areas, state parks, and national parks. Named roads and paths
within parks retain their individual identity and tooltip. State and national
parks also retain individually browsable unnamed paths, using the park name as
their locality context. Hovering these paths or facilities must resolve the ID
emitted in the map tile rather than an incompatible underlying path ID.

Checkboxes control visibility only. Checking a workout does not focus or fit it;
the non-checkbox portion of the row performs focus and fit. Unchecking the
focused workout clears focus.

## Temporary Focus Performance

Temporary Coverage focus is a core interaction and must not recreate aggregate
Coverage tiles or fan out into viewport-dependent focus tile requests. After the
route-list hover delay, the client requests one complete focused-workout GeoJSON
overlay, caches it by map-selection generation and workout, and replaces one
persistent MapLibre GeoJSON source with `setData()`. The switch is atomic: orange
and yellow aggregate Coverage remains installed, camera and base map do not
change, and focused geometry must not appear progressively by tile. Returning to
persistent focus reuses its cached overlay without a server round trip. Stale
responses are fenced by selection ID and generation.
