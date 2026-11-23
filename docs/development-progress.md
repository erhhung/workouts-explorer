# Development Progress

This document tracks technical project status separately from the public
[README](../README.md). Milestones 1-9 are complete; the authoritative
[M9 closeout](#milestone-9-closeout) records owner acceptance of Stats/New Coverage
and the navigation/UX follow-ups. The [M8 closeout](#milestone-8-closeout) retains
its approval and final live browser pass.
Historical M7 measurements stay in
[Coverage Acceptance Results](coverage-acceptance-results.md) and are not
reinterpreted here. Documentation dates use `America/Los_Angeles` and M/D/YYYY.

## Current Status

| Area | Status |
|---|---|
| M8 source implementation | Complete and accepted; iCloud ingest, available-map Coverage recovery and final live browser gate verified; see [closeout](#milestone-8-closeout) |
| Milestone plan | M9 complete; M10 foundation deployed, full administration in progress; shared login/reserved admin, isolated sessions and four-tab shell with initial Accounts/Settings workflows; M11-M14 retain scope; see [foundation](#milestone-10-foundation) |
| Committed-route Coverage pipeline | Folded-028 contract introduced at Helm revision 29 remains deployed; exact eligible cohorts and mixed-failure automatic recovery active |
| Metadata-driven Coverage reconciliation | Deployed at Helm 62; initialization complete; selected real PostgreSQL matrix and three live unchanged cycles PASS with no GPS/ROI, new jobs or changed rows |
| Concurrent coverage and reader coordination | Deployed and focused verification complete; one coverage pod runs two jobs with shared least-active reader assignment |
| Application database | Source/schema33, minimum runtime32, baseline27, ledger0-33; consolidated region management in033 and display currentness in030; retained ledger prefix/timestamps, original function identities and private data preserved |
| Historical database checkpoints | Superseded; detailed historical source/catalog receipts are private and do not define current compatibility |
| OSM database | Source/schema11, minimum10; durable regional generation high-watermarks and statistics receipts; healthy preparation skips installed map-data scans and retained promotion numbers/source hashes remain intact |
| Park/education boundary correction | OSM NorCal generation 18 (build ID 62), derivation 21 active; both account campaigns complete; the three former graph-layer failures are recovered with current NorCal generation-18 evidence (build ID 62) |
| Source layout | Account access/purge foundation002, source-check lane003; continuity009/010, pipeline028, status029, display/Stats/currentness030, tiles031, admin metadata/fences032, consolidated OSM region management033 |
| Old-contract 28 repair | Approved exact actual-profile repair rehearsed on a protected actual backup, then applied under the real non-superuser migration login; preservation and new readiness verified |
| UI follow-up | Owner Data Sources dialog delivers source table, add/edit/delete, typed settings, write-only retention placeholders and bounded connection-check feedback; Preferences fixes retained |
| Historical yearly recovery | Available-map Coverage independently settled; final measurements retained privately; M8 accepted without a capacity claim |
| Chart semantics | Helm194 adds a separate limited OSM controller and approved updater template, default dispatch concurrency1; existing credentials/configuration and Martin preserved |
| Legacy route repair | Authorized repair and failed-only retries complete; all affected routes have current durable matches, with no failed/queued repair work remaining |
| Reader policy | Strict replica-only enforcement is active; the chart default remains false |
| Release | Helm232, current dev-20261120 pins; sentence-case Coverage route outcomes in Data Sync; Helm231 hover behavior, Helm230 column widths and Helm228 eligibility/count behavior retained; OSM11/10, application33/32 |
| Coverage parking-repair recovery | v83 deployed; two complete private replays succeeded with 222 prepared segments and no recovery-excluded GPS points; requested production retry is still queued, successful-v82 reuse and redundant policy-only queue cleanup remain open |
| Ingest-phase progress | Deployed behavior retained in source 029; actual worker phases and durable Events, source-aware detail and faster Running polling; see [follow-up](#ingest-phase-progress-follow-up-029) |
| Click-highlight follow-up | Deployed behavior retained in source 030; full-group geometry, cancellation/cache guards and camera/tile-ready highlighting active; earlier test counts retain their original scope |
| Canonical display identity | Policy coverage-display-identity-v1 retained; qualified M8 acceptance remains historical |
| Summary aggregates | Measured-distance totals/averages include route-less types; Workouts pairs the total with a right-aligned GPS-route count |
| Latest performance comparison | Canonical authorized desktop/4K run completed all required views without observed delivery errors; wire-size benefit observed, no latency-speedup or capacity certification; dense raw completion remains slow |
| Early hard-gap matcher correction | Implemented, verified and deployed; selective failed-target recovery verified privately; existing bounds and successful Coverage preserved |
| Coverage display optimization | Compact foundation and canonical identity combined in source 030; deployed hover/cache/overlay behavior unchanged; M8 accepted, capacity not certified |
| Tile-local map optimization | All six 031 optimizations remain deployed; consolidated 031 LOD/optimized SQL, scheduling and gzip delivery live at 67; scoped completeness verified, not fast dense raw rendering |
| Raw route continuity | 009 enforces the existing Coverage spatial boundary during ingest-derived geometry and focused display; authorized existing-data scan/repair verified with source observations and valid evidence preserved |

Runtime deployment, the earlier authorized legacy repair and M8 acceptance are
complete. Explicit user approval and the final qualified live browser pass close
the milestone; no further owner decision is needed. Expired iCloud access was
recovered through the API before historical ingest. Host `rclone.conf` was not
read.

Helm232 capitalizes the first letter of each displayed Coverage route outcome in
Data Sync's Route runs list, replacing underscore separators with spaces. Existing
Processing/No result fallbacks and elapsed-millisecond formatting are retained.
The UI-only change is built and deployed directly for review. Protected evidence:
`coverage-route-status-capitalization`.

Helm231 shows the Workouts value tooltip only when the region has a generation.
Without one, the value renders without a tooltip wrapper and inherits the row's
hand cursor. This UI-only change is built and deployed directly for review,
preserving the requested column widths. Protected evidence: `regions-workouts-tooltip`.

Helm230 narrows Workouts by 1rem to 9rem and gives that 1rem to Status. The table
width and all other tracks retain their Helm229 dimensions. The UI-only image is
published under the current tag and deployed directly for user review, with no
additional verification requested. Protected evidence: `regions-column-rebalance`.

Helm229 adjusts only the Regions table layout: Workouts is 4rem wider, Status is
2rem narrower and PBF size is 1rem narrower. Other column widths are retained;
the overall table is 1rem wider than Helm228. The UI image is published under the
existing current tag and its rollout completes. Further post-deployment verification
is skipped at the user's request for direct review and possible further tweaks.
Protected evidence: `regions-column-widths`.

Helm228 removes the static region list from Helm, runtime configuration, environment
and catalog bootstrap. The OSM region configured flag is removed; every promoted
active region is eligible for Coverage. Version-2 catalog fingerprints invalidate
old negative readiness proofs when active-region membership changes. The live
app33/OSM11 patch is folded into the existing schema definitions, without new
migration IDs or ledger changes. Actual OSM ownership is discovered rather than
assumed; original function identities/permissions, generation numbering and map
data are preserved. The coordinated rollout restores workers/controller after
adoption and leaves six deployments healthy.

Regions adds **Workouts** between Database usage and Status, with a 6rem track and
no reduction in the existing column widths. Its total counts intersecting routed
workouts across all accounts, including workouts retained after source deletion;
the minimum-2em styled hover target reports **n coverage routes** using current
route-input proofs and the latest regional generation. A private, cascading spatial
membership index avoids redoing all route/boundary intersections during polling.
Native checks cover both accounts, multi-region routes, generation reset/progress,
route/boundary changes, retained-source workouts and access restrictions. All 88
focused UI tests pass; candidate/deployed Chromium dark and WebKit light compare
desktop/mobile column widths directly with Helm227. Final API migration packaging
includes the retained-source correction; unchanged runtime/UI artifacts and each
image's source provenance are recorded. Harbor current tags match deployed pins.

The live Israel aggregate initially reports 60 workouts and zero latest-generation
Coverage results. All 7,665 observations of the requested 5/24/2023 run are inside
the installed Israel boundary; no private coordinates leave the application
database for that check. The new reconciliation revision is active and its stale
negative proof is actionable. Ordinary backfill is asynchronous and has encountered
transient NOWAIT row contention; the requested run has not yet been observed queued.
Protected evidence: `promoted-region-coverage`.

Helm227 corrects Data Sync Coverage generation labels without restyling the existing
region/number table. Retained per-build promotion numbers are projected through the
catalog cache; NorCal build 62 displays regional generation **18**, including old
jobs. Deleted/retired regions retain their recorded number; missing metadata displays
unavailable rather than an internal ID or a substituted current generation. Build
provenance and freshness comparisons remain unchanged. All 241 Data Sync tests,
focused native app33/OSM11 label/history checks, Go tests/vet and generated-source
checks pass. Chromium dark and WebKit light verify candidate/deployed desktop and
mobile presentation. Six deployments are healthy; Harbor current tags match the
deployed digest pins, with schemas, ledgers, configuration, Martin and active updater
identities preserved. Protected evidence: `coverage-generation-labels`.

The production v83 review at 11/22/2026 6:48 PM records 162 applied jobs, one
no-evidence success, 35 transient failures, two OSM-unavailable failures and ten
parameter rejections from the initial rollout. The requested route remains queued;
these aggregate successes do not confirm that specific recovery end to end.

Helm226 deploys bounded window recovery for typed local matcher limits. It retries
smaller windows before excluding an irreducible observation and never joins across
an excluded gap. Two complete private replays produced 222 prepared segments with
zero recovery-excluded observations. Legacy v82 jobs retain their original policy.
The requested production retry is queued; private replay success is not recorded as
an applied production result. The policy name remains experimental until that
confirmation; the intended stable name is `coverage-path-policy-v83`, with historical
identifiers retained and naming compatibility required before activation. The
recovery-only descriptor change also caused an unwanted broad scan. That scan is
temporarily deferred with its checkpoint preserved; successful-v82 compatibility
and narrowly scoped redundant-queue cleanup are not complete.

The same rollout includes job/regional-generation/ruleset batch banners, complete
download destination logging, waiting-only prestart status, current-task following
that preserves manual scroll during stable polling, and modal-aware `/` search
focus. Native app33/OSM11 checks and candidate/deployed light/dark browsers pass.
The pre-follow-adjustment full UI suite passed 1,134 tests; 157 focused tests passed
after its ResizeObserver correction. Helm225's waiting command was canceled after
healthy worker rollout; direct binary verification and successful Helm226 activation
confirmed the deployed images.

Helm224 retains its checkpoint: locality sliver containment, failed-candidate
cancellation and the Regions A shortcut. Four retained-candidate attribution errors
were traced to county reassignment without geometry containment; corrected producer
SQL excludes those invalid assignments while validation stays strict. Incompatible
completed checkpoints require a fresh job, with a safe cancellation action now
available. All 1,119 UI tests and native PostGIS checks pass; six deployments are healthy.
At that checkpoint the requested private Coverage run was diagnosed as a
parking-repair rewrite loop; the subsequent bounded recovery is recorded at Helm226.

Helm223 retains its checkpoint: prefix-free OSM task errors, retained
validation-gate causes and configurable six-hour completed/failed Job retention.
Terminal retained pods do not hold processing slots; cleanup retries have unique
names. Generic errors say Review the updater pod logs for this job. Existing UI
history normalizes the old prefix/instruction. Helm/Go/native checks and 93 focused
UI tests pass; both themes and deployed assets are verified. Six deployments are healthy.

Helm222 retains its checkpoint: concise download failures and copyable URL.
Download errors use `Failed to download <URL> | <reason>` without redundant task
prefixes. The inline URL copies on click or keyboard activation, with shared tooltip,
accessible success/failure feedback and mobile wrapping. Focused UI/Go and native
controller persistence checks pass; six deployments are healthy.

Helm221 retains its checkpoint: OSM executor/UI final-task completion and
shared current/table task icons. Final validation no longer completes task 58
before publication, cleanup, statistics and local scratch removal finish. Promoted
maintenance retries reopen the final task, including historical 57-step requests.
All 1,109 UI tests and disposable native protocol/maintenance checks pass; light/dark
candidate/deployed browsers verify 24 states each. Six deployments are healthy.

Helm220 retains its checkpoint: UI-only transform-free settled dialogs.
Painted border samples now match their theme color on all four edges in the
verified Chromium/WebKit matrix, including map scrolling. This corrects Helm219's
coordinate-only check, which missed compositor blurring despite integral bounds.
All 1,101 UI tests pass; schemas, configuration and non-UI pods are preserved.

Helm219 retains its checkpoint: UI-only shared dialog pixel alignment.
All modal entry points use the shared primitive, including nested confirmations,
content-sized forms and the mobile Map sheet. Table frames retain only their
internal residual correction and remeasure after the dialog moves. Light/dark
browser checks cover normal/high-DPI rendering, resizing and focus/scroll behavior;
all 1,100 UI tests pass and six deployments are healthy.

Helm218 retains its checkpoint: UI-only admin sidebar caption alignment.
Accounts/Users and OSM Maps/Regions align exactly; Operations/Settings match their
content-heading insets. Light/dark checks cover desktop/tablet/mobile pane layouts,
with the shared owner Stats sidebar rules untouched. All six deployments are healthy.

Helm217 retains its checkpoint: UI-only monitoring-indicator alignment,
raised by 0.25rem. Candidate/deployed light/dark checks verify the computed offset,
table alignment and responsive fit. Schemas, configuration and non-UI pods are preserved.
The [route markers and displayed-geometry fitting](#route-markers-and-displayed-geometry-fitting) retains its Helm216 checkpoint.
The [monitoring indicator typography](#monitoring-indicator-typography) retains its Helm215 checkpoint.
The [execution policy, inline indicator and OSM source consolidation](#execution-policy-inline-indicator-and-osm-source-consolidation) retains its Helm214 checkpoint.
The [OSM storage-monitoring policy and adaptive safeguards](#osm-storage-monitoring-policy-and-adaptive-safeguards) retains its Helm213 checkpoint; its immutable-per-request policy is superseded by per-execution capture.
The [plain-language OSM step help](#plain-language-osm-step-help) retains its Helm212 checkpoint.
The [metrics-inclusive durations and historical reconciliation](#metrics-inclusive-durations-and-historical-reconciliation) retains its Helm211 checkpoint.
The [short progress and completed deletion history](#short-progress-and-completed-deletion-history) retains its Helm208 checkpoint.
The [consolidated region management and Coverage continuity](#consolidated-region-management-and-coverage-continuity) retains its Helm207 checkpoint.
The [processed title and Coverage retention clarification](#processed-title-and-coverage-retention-clarification) retains its earlier checkpoint; its stale-on-removal description is superseded by the continuity contract.
The [progress and typed user confirmation](#progress-and-typed-user-confirmation) retains its Helm205 checkpoint.
The [region deletion and recovered timing](#region-deletion-and-recovered-timing) retains its Helm204 checkpoint.
The [Finished elapsed help and historical recovery](#finished-elapsed-help-and-historical-recovery) retains its earlier recovery checkpoint.
The [Region-local generations and build identity](#region-local-generations-and-build-identity) retains its Helm199 checkpoint.
The [Successful elapsed totals and final UI help](#successful-elapsed-totals-and-final-ui-help) retains its Helm198 checkpoint.
The [Region progress actions and OOM diagnosis](#region-progress-actions-and-oom-diagnosis) retains its Helm196 checkpoint.
The [American vocabulary and updater help](#american-vocabulary-and-updater-help) retains its Helm195 checkpoint.
The [Managed region update workflows](#managed-region-update-workflows) retain their Helm194 checkpoint.
The [Regions live-progress tooltip dismissal](#regions-live-progress-tooltip-dismissal) retains its Helm193 checkpoint.
The [Regions minor words, track transfer and join limit](#regions-minor-words-track-transfer-and-join-limit) retain their Helm-192 checkpoint.
The [Regions columns, title casing and map polish](#regions-columns-title-casing-and-map-polish) retain their Helm-191 checkpoint.
The [Regions behavior and presentation follow-up](#regions-behavior-and-presentation-follow-up) retains its Helm-190 checkpoint.
The [Regions table and retained boundary map](#regions-table-and-retained-boundary-map) retain their Helm-189 checkpoint.
The [Road Coverage cell-target correction](#road-coverage-cell-targets-and-checkbox-alignment) retains its Helm-188 checkpoint.
The [inline checkbox edge correction](#symmetric-inline-checkbox-edges) retains its Helm-187 checkpoint.
The [shared header hover-edge correction](#shared-table-header-hover-edges) retains its Helm-186 checkpoint.
The [shared painted table outline](#shared-sticky-table-viewport-and-painted-outline) retains its Helm-185 checkpoint.
The [shared table-header standard](#shared-table-header-and-sort-styling) retains its Helm-184 checkpoint.
The [Road Coverage sticky-header correction](#road-coverage-sticky-header-top-border) retains its Helm-183 checkpoint.
The [border-height correction](#road-coverage-border-height-correction) retains its Helm-182 checkpoint.
The [OSM provider catalog refresh](#osm-provider-catalog-refresh) retains its Helm-181 checkpoint.
The [Users menu/recovery-scope update](#users-menu-order-and-recovery-scope) retains its Helm-180 checkpoint.
The [Accounts/source follow-ups](#accounts-and-source-follow-ups-through-helm-179) retain their Helm-179 checkpoint.
The [account-deletion update](#account-deletion-and-in-place-users-refresh) retains its historical Helm-173 checkpoint.
The [account/source-default update](#account-and-source-defaults) retains Helm 172.
The [Data Sources field-hint and automation update](#data-sources-field-hints-and-automation) retains Helm 171.
The [Data Sources dialog-polish update](#data-sources-dialog-polish) retains Helm 170.
The [Data Sources column-width update](#data-sources-column-widths) retains Helm 169.
The [owner Data Sources update](#owner-data-sources) retains Helm 167-168.
The [Preferences Diagnostics-help update](#preferences-diagnostics-help) retains Helm 166.
The [Preferences Diagnostics-width update](#preferences-diagnostics-width) retains Helm 165.
The [Preferences dialog-height update](#preferences-dialog-height) retains Helm 164.
The [Preferences dialog-spacing update](#preferences-dialog-spacing) retains Helm 163.
The [Enable user dialog-copy update](#enable-user-dialog-copy) retains Helm 161-162.
The [shared error-copy update](#shared-error-copy) retains Helm 160.
The [account disable/enable update](#admin-account-disable-and-enable) retains Helm 159.
The [disabled-action/usage-dialog update](#admin-disabled-actions-and-usage-dialog)
retains Helm 158. The [invitation/date/usage update](#admin-invitations-dates-and-usage)
retains Helm 155-157. The [admin Users review](#admin-users-review) retains Helm 154.
The [M10 foundation](#milestone-10-foundation) retains Helm 153.
The [placement-aware menu update](#placement-aware-row-menus) retains Helm 152.
The [star and global-focus update](#smaller-stars-and-global-surface-focus-policy)
retains its Helm-151 checkpoint. The [job/detail and chart-track update](#stable-job-details-and-linked-charts)
retains its Helm-150 checkpoint. The [cursor/period/history update](#cursor-help-current-periods-and-history-actions)
retains its Helm-149 checkpoint. The [fixed workspace update](#fixed-navigation-and-content-scrolling)
retains its Helm-148 checkpoint. The [tight-scale/table-message update](#tight-scales-and-table-messages)
retains its Helm-147 checkpoint. The [baseline correction](#consistency-zero-baseline) retains
its Helm-146 checkpoint. The [spacing refinements](#consistency-chart-spacing) retain
their Helm-145 checkpoint. The [chart refinements](#consistency-chart-refinements) retain
their Helm-144 checkpoint. The [Consistency report](#consistency-report) retains its
Helm-142/143 checkpoints. The
[map symbol priority update](#map-text-and-symbol-priority) retains its Helm-141
checkpoint. The [disabled-control and menu refinements](#disabled-dropdowns-and-row-menu-refinements)
retain their Helm-139/140 checkpoints. The [Range totals refresh fade](#range-totals-refresh-fade)
retains its Helm-137/138 checkpoints. The [heading-link hit-area correction](#workout-log-popup-link-hit-areas)
retains its Helm-135/136 checkpoints. The [popup refinements](#workout-log-popup-and-link-hover-refinements)
retain their Helm-133/134 checkpoints. The [height correction](#workout-log-table-height-correction)
retains its Helm-132 checkpoint. The [filtering follow-up](#workout-log-column-filters) retains
its Helm-130/131 checkpoints. The [modifier-hover follow-up](#standalone-modifier-hover)
retains its Helm-129 checkpoint. The [window-restore follow-up](#aggregate-window-restore-recovery) retains
its Helm-128 checkpoint. The [aggregate preview follow-up](#shift-held-aggregate-previews)
retains its Helm-127 checkpoint. The [retained workspace follow-up](#retained-tab-workspaces)
retains its Helm-126 checkpoint. The [map presentation/focus follow-up](#map-layer-presentation-and-preferences-focus)
retains its Helm-123/124 checkpoints. The [dialog paging follow-up](#search-dialog-arrow-paging)
retains its Helm-122 checkpoint. The [focus-modality follow-up](#tab-only-focus-navigation) retains its
Helm-121 checkpoint. The [keyboard/camera follow-up](#application-shortcuts-and-preserved-map-viewport)
retains its Helm-120 checkpoint. The [empty/search continuity follow-up](#centered-empty-states-and-search-continuity)
retains its Helm-119 checkpoint. The [frozen dialog follow-up](#frozen-table-dialog-capacity)
retains its Helm-118 checkpoint. The [focus visibility follow-up](#map-and-stats-focus-visibility)
retains its Helm-117 checkpoint. The [aggregate focus follow-up](#range-totals-keyboard-focus)
retains its Helm-116 checkpoint. The [paging performance follow-up](#workout-log-paging-performance)
retains its Helm-115 checkpoint. The [shared background follow-up](#shared-tab-content-background)
retains its Helm-114 checkpoint. The [GPS recovery follow-up](#bounded-gps-recovery-filtering)
retains its Helm-113 checkpoint. The [raw display/export follow-up](#shared-raw-route-display-and-exports)
retains its Helm-112 checkpoint. The [Operation filter follow-up](#operation-order-and-raw-route-anomaly-investigation)
retains its Helm-110 investigation checkpoint. The [compact pager follow-up](#compact-steady-pagination)
retains its Helm-108/109 checkpoints. The [shared pagination follow-up](#shared-one-click-pagination) retains
its Helm-107 checkpoint. The [dialog sizing follow-up](#road-coverage-content-sized-dialog)
retains its Helm-106 checkpoint. The [graph-regression closure](#coverage-graph-expansion-regression-closure)
retains its Helm-104/105 worker checkpoint. The [All time/navigation follow-up](#all-time-and-range-aware-job-route-navigation)
retains its Helm-103 API/UI checkpoint. The [SVG finish-marker follow-up](#svg-raw-route-finish-marker)
retains its Helm-102 checkpoint. The [offline rebuild completion](#offline-osm-rebuild-and-coverage-reconciliation)
retains its Helm-101 operational checkpoint. OSM NorCal generation 18 (build ID 62) is promoted; all-account
Coverage campaigns and queued work are terminal, with three processing-limit
exceptions explicitly retained. The [boundary-attribution correction](#park-and-education-boundary-attribution-correction)
retains its initial updater-publication checkpoint. The
[all-value fitting rollout](#shared-fitting-for-all-range-totals-values) retains
the unchanged UI's Helm-93 checkpoint. The [Map-state/Duration rollout](#map-workspace-retention-and-duration-fitting)
retains its Helm-92 checkpoint. The [direct-camera rollout](#direct-identity-camera-flow)
retains its Helm-91 checkpoint. The [deletion-copy rollout](#workout-deletion-confirmation-copy-and-audit)
retains its Helm-90 checkpoint. The [shared badge-size rollout](#shared-badge-size-verification)
retains its Helm-89 checkpoint. The [badge-text alignment rollout](#shared-date-badge-text-alignment)
retains its Helm-88 checkpoint. The [Summary/shared-dialog rollout](#summary-and-shared-dialog-ui-rollout)
retains its Helm-86 checkpoint. The [width/grouping UI rollout](#stats-width-and-grouping-ui-rollout)
retains its Helm-85 checkpoint. The [heading-margin UI rollout](#stats-heading-margin-ui-rollout)
retains its Helm-84 checkpoint. The [Stats review follow-up](#stats-review-navigation-and-refresh)
retains its Helm-83 checkpoint. The [initial Stats implementation](#stats-and-new-coverage-initial-implementation)
retains its Helm-82 checkpoint. The [empty-result follow-up](#empty-coverage-results)
retains its Helm-80 checkpoint. The [031 delivery record](#tile-rendering-and-delivery-031)
retains its Helm-67 benchmark scope. The [031 release record](#tile-local-map-optimization-031) retains its
Helm-65 checkpoint and limits. The [metadata release record](#metadata-driven-coverage-reconciliation)
retains revision-61/62 evidence; the [029 follow-up](#ingest-phase-progress-follow-up-029)
retains its phase contract and historical revision-60 receipt.
[M8 closeout](#milestone-8-closeout) retains its revision-57 acceptance and
browser PASS unchanged. The [10/20 release checkpoint](#folded-028-production-release-10202026)
superseded earlier undeployed, pending-repair and old-live-contract wording at
that stage. Dated checkpoints below retain their original evidence boundaries.
The [independent recovery closure](#independent-yearly-recovery-closure-10202026)
is complete for available map data; source success alone was not used as proof.

## Locality Sliver Containment And Regions Shortcut

Helm224 fixes the producer behind the four material locality residuals in retained
attempt75. Sliver attribution previously assumed an admin-level8 municipality was
geometrically contained in the neighboring admin-level6 county. Four short trail
pieces (2.38m to 18.14m) were reassigned entirely outside that county. Each had a
valid original municipality. The producer now requires outside-county length no
greater than the unchanged 0.01m validation tolerance before absorbing a sliver.
It preserves the original scope otherwise. No validation metric, geometry or
identity rule is weakened, and valid nested slivers/road/length exclusions remain.

Read-only indexed, bounded checks on the retained candidate verified all four old
invalid reassignments would be excluded and the valid original municipalities
retained. No production candidate data was patched. Native PostGIS fixtures run
the actual discovery/apply SQL across checkpoint boundaries and the unchanged
locality/park/education validation predicates. Existing completed geometry fences
remain immutable; completed validation can still restart under its existing rules.

Ordinary Resume does not rewind completed geometry/identity work. Changed producer
SQL records a clear incompatible-checkpoint reason requiring a fresh processing
job. Failed resumable, unpromoted requests now expose the existing audited Cancel
processing action so candidate cleanup can precede Clear job status and a fresh
Download/Reprocess without deleting promoted regional data. Post-promotion
maintenance cannot use this action.

The Regions checkbox declares `aria-keyshortcuts="A"`; the shared shortcut registry
handles A/Shift+A, resets paging like a pointer toggle, suppresses repeated toggles
and preserves text editing, modifier, menu, modal and inactive-section ownership.
All 1,119 UI tests, production/type/CSP/CSS/format checks and Go updater/controller
tests/vet pass. Candidate/deployed Chromium dark and WebKit light verify desktop/
mobile shortcut guards, recovery messaging and the real cancellation command path
using synthetic API fixtures. Only updater/UI images and the approved updater
template checksum change; API/worker/coverage/Martin pods, schemas/ledgers, secrets,
TTL21600, active execution identities and Harbor current tag consistency are preserved.
Six deployments are healthy. Protected evidence is under
`deploy-internal/osm-locality-residuals`; its disposable fixture pod/forward are removed.

### Requested Coverage Failure Diagnosis

The requested private Outdoor Run was inspected without changing its route or
Coverage state. Its retained target uses Northern California attempt62; 7,333
original points are below the route-point bound. The old log index was absent, so
a read-only replay used the production snapshot/matcher with normal global/account/
region/backend diagnostic admission and strict standby placement. The replay hit
the 524,288-byte parking-repair retained-state bound. It repeatedly accepts local
rewrites at one cursor after rejecting simpler pumping alternatives, growing the
traversal until the memory guard stops it. This is a matcher rewrite-loop issue,
not the updater's locality containment defect. A private input/graph reproducer
and trace are retained under `deploy-internal/coverage-processing-limit` outside
the repository. No matcher policy or cap changes are deployed, no partial Coverage
is accepted, and this Coverage failure remains unresolved. Diagnostic leases,
forwards and copied connection credentials are removed; source matcher hashes are
verified unchanged.

## Task Error Causes, Pod Logs And Job Retention

Helm223 removes the redundant task-failed prefix from controller summaries,
including SQL/detail, OOM, eviction and generic container exits. Structured logs
retain task identity. Generic instructions now read **Review the updater pod logs
for this job.** The dialog also normalizes the old prefix/instruction in retained
history without rewriting its durable event records; download URL copying remains.

The latest Israel/Palestine attempt75 generic exit was investigated in archived
`fluent-bit-*` logs. Its actual final exception at 2026-11-22T19:20:46.397Z was
`OSM validation gate: materialLocalityResiduals is 4, expected 0`. The candidate
was not promoted. SQL validation had committed, but the later Go validation gate
did not retain its rejection reason. The updater now records that specific cause
using the existing stage-failure path while preserving committed SQL checkpoints
and the resumable candidate. The controller can present that cause instead of
only an exit code. The underlying locality residuals are not altered by this release.

`osm.regionUpdates.ttlSecondsAfterFinished` defaults to **21600**. The Helm schema
and runtime validate nonnegative integral seconds; `0` permits immediate TTL GC.
The approved template applies it to updater, cancellation-cleanup and deletion
Jobs. Terminal Jobs with TTL are left to Kubernetes for reclamation. Capacity and
finalization require all request pods to be Succeeded/Failed, not physically gone;
Pending/Running/Unknown pods remain a barrier. Cleanup retries use distinct names
so retained failed Jobs cannot be accidentally adopted as the next attempt. Older
Jobs without TTL preserve the previous UID-fenced deletion lifecycle, and a Job
already reporting its outcome is not recreated if immediate TTL GC wins the UID
capture race. Forced stopping of stale active executions remains available.

Go updater/controller tests, vet, all Helm tests and 93 focused UI tests pass.
Helm tests cover default/zero/custom retention and reject negative/fractional/
out-of-range/string values. Disposable application33/OSM11 native checks cover
retained terminal Jobs, processing capacity release, unique cleanup retry names,
legacy cleanup, specific download/validation error persistence and unchanged
committed validation checkpoints. Its test pod/forward are removed. Candidate and
deployed Chromium dark/WebKit light each verify ten desktop/mobile error states,
historical prefix/instruction normalization and continued URL clipboard copying.

The deployed approved Job template and Helm values both contain TTL21600. Six
deployments are healthy; schemas/ledgers, secrets, API/Martin pods, other worker/
coverage binaries, rclone and active execution identities/policies are preserved.
Both origins serve reviewed assets and current Harbor tags match deployed pins.
Verified log fields/index/namespace and kubectl/archive instructions are documented
in the region update guide. Protected archive evidence and release receipts remain
under `deploy-internal/osm-task-errors` outside the repository.

## Download Failure URLs And Clipboard Copy

Helm222 formats region-download errors once at the downloader boundary as
`Failed to download <URL> | <reason>`. The updater preserves this message rather
than adding a source-PBF prefix; the controller recognizes the URL-specific download
message and does not prepend the task name. Network failures retain their underlying
error identity without repeating the HTTP client's request URL. Generic SQL and
container/OOM summaries keep their existing task context and bounds.

The progress dialog renders the URL as an inline copy action using the established
theme, focus and tooltip patterns. Click or Enter copies only the URL, never
navigates, announces success/failure and supports retry if clipboard access fails.
The URL remains selectable, copy feedback resets when the error changes or the
dialog reopens, and long URLs wrap within the mobile error panel.

Ninety focused UI tests, updater/controller Go tests and vet, production/type/CSP/
CSS/format checks pass. A disposable native controller fixture verifies the exact
URL/pipe message persists through owned pod termination and job finalization while
retaining OOM precedence and capability/UID/capacity fences; its pod/forward are
removed. Candidate/deployed Chromium dark and WebKit light each verify desktop/
mobile click/keyboard copy, denied-clipboard retry, feedback reset, wrapping and
stable dialog geometry with synthetic API fixtures.

Only the OSM updater, controller binary in the shared worker image and UI assets
change. Other worker/coverage binaries and rclone are verified unchanged. Six
deployments are healthy; schemas/ledgers, credentials, API/Martin pods and active
updater execution identities/policies are preserved. Both origins serve reviewed
assets and Harbor current tags match deployed digest references. Protected release
receipts are under `deploy-internal/osm-download-errors` outside the repository.

## OSM Final-Task Completion And Icon Agreement

Helm221 corrects the Monaco observation: processing attempt 72 genuinely downloaded,
imported and validated a fresh 704,884-byte extract in about 18.4 seconds, then spent
another 3m23s in post-publication cleanup/statistics maintenance. Validation SQL had
incorrectly completed the final task before that maintenance, and the current-task
icon independently represented aggregate job state rather than its table row.

Final validation reports now leave the final task Running with no Finished time.
Completion is reported only after publication, retired-data cleanup, statistics and
local scratch removal succeed. Failure/pause stays visible through the normal
executor outcome path. Post-promotion retries and cancellation cleanup reopen the
final task and settle it only after successful maintenance. Historical 57-step
requests retain their original numbering through executor translation.

Current-task and table icons now use one task-state projection, including completed
tasks between batches, terminal outcomes and queued work. Never-started queued
processing/deletion reserves the waiting spinner in both places; queued resume
retains matching task history. Timing, counters, monitoring policy and dialog/table
geometry are preserved.

The minutes in finalization are not additional Monaco derivation: PostgreSQL17.6
recursively analyzes installed partitions when refreshing shared partitioned-table
statistics. That server has no parent-only ANALYZE syntax. This release retains
the existing statistics policy and makes its active work accurately visible.

All 1,109 UI tests, Go updater/controller tests, vet, production/type/CSP/CSS/format
checks pass. Disposable application33/OSM11 native checks verify new/legacy final
Running/no-Finished boundaries, post-publication failure/reopen/success, execution
duration fencing and existing statistics recovery. The owned test pod/forward are
removed. Candidate/deployed Chromium dark and WebKit light each verify 24 states
covering finalization, task-boundary completion, stop/cancel, queued resume and
download/deletion behavior, including desktop/mobile views and matching icons.
Six deployments are healthy; only UI, approved updater image/template and controller
template checksum change. Schemas/ledgers, credentials, API/worker/coverage/Martin
pods and running execution identities/policies are preserved. Both origins serve
reviewed assets and Harbor current tags match deployed pins. A transient first
post-rollout asset 504 cleared on retry. Protected release receipts are under
`deploy-internal/osm-final-task` outside the repository.

## Transform-Free Settled Dialog Borders

Helm220 corrects the follow-up shown in the user's partial boundary-dialog capture:
the horizontal stroke was spread across two dim rows while the vertical stroke
occupied one bright column. Helm219 could produce integral DOM bounds while its
centering transform and compensating translation still blurred the compositor's
rasterized layer. Checking coordinates alone did not establish crisp paint.

After entry animation, shared centered dialogs now use device-pixel `left`/`top`
layout coordinates and no persistent transform or translation. Natural dimensions,
responsive centering, nested modal behavior and the non-centered mobile Map sheet
remain intact. Table frames retain their independent residual correction and
remeasure after parent positioning changes.

Pixel regression checks sample three positions along every edge at six viewport
sizes, at the beginning/middle/end of map scrolling. Candidate Chromium dark and
WebKit light pass at DPR1/2; deployed DPR1 checks repeat all 18 states per browser.
All sampled strokes match the computed border color exactly. Retained Helm219
samples at 1441x1001 differ by up to 51 RGB levels in Chromium and 44 in WebKit;
the same samples now differ by zero. Broader candidate/deployed checks preserve
the 18-state modal, table, focus-return, content-growth and responsive matrix.

All 1,101 UI tests, production/type/CSP/CSS/format checks and deployment asset/hash
checks pass. A transient ingress 504 during the first post-rollout asset check
cleared on verification retry; all six deployments are healthy and both origins
serve the reviewed assets. Runtime schemas/ledgers, configuration, non-UI pod UIDs
and Harbor current tag-to-digest consistency are preserved. Protected baseline,
pixel-comparison and release receipts are under `deploy-internal/dialog-border-paint`.

## Shared Dialog Pixel Alignment

Helm219 initially aligns every dialog's outer position to device pixels through the shared
`Dialog.Content` primitive, preserving Radix portals, focus handling, `asChild`
forms and dismissal. Alignment waits for entry animations and updates after size,
font, viewport or display-resolution changes. Existing transforms and responsive
dimensions are preserved; corrections do not accumulate across measurements.

`DialogTableViewport` no longer positions the ancestor dialog. It corrects only
its own remaining internal offset and remeasures after the shared dialog reports
a position adjustment, so parent and child corrections cannot compete. Its centered
dialog translation mechanism is superseded by Helm220's transform-free positioning. A source
check prevents application dialogs from bypassing the shared primitive.

All 1,100 UI tests and the production/type/CSP/CSS/format checks pass. Candidate
Chromium dark and WebKit light checks at DPR1/2 each cover 18 states: boundary and
progress dialogs at odd/even desktop, tablet and mobile dimensions, an explicitly
fractional internal table layout, confirmation/form dialogs, Preferences, About,
content-sized source editing, nested source deletion and custom date ranges.
Deployed Chromium dark/WebKit light repeat those checks against published assets
with synthetic API fixtures. Focus return, keyboard scrolling and modal layering
remain intact. All six deployments are healthy; configuration, schemas/ledgers,
non-UI pods and current Harbor tag-to-digest consistency are verified. Protected
receipts remain under `deploy-internal/dialog-pixel-alignment` outside the repository.

## Route Markers And Displayed-Geometry Fitting

Helm216 updates API/UI only. The ENABLED state and round route-start marker share
the same bright green token. The centered red finish square and its outline scale
by 1.25 on each side within the existing image canvas; the start-marker radius is
unchanged. Indicator tracking, non-breaking spacing and disabled gray remain intact.

Map selection now projects Routes-mode bounds from the stored displayed segmented
line rather than unfiltered imported extrema. Display-rejected isolated points
cannot expand a route fit; Coverage keeps its independently applied geometry bounds.
Raw observations and imported summary metadata are not rewritten. The investigated
user-provided example has one isolated older observation already excluded from its
drawn line, explaining the prior northward fit discrepancy. Sensitive identifiers,
coordinates and diagnostic evidence remain protected outside the repository.

The API/native regression verifies authenticated owner-scoped 2D/3D visible bounds,
their aggregate and unchanged imported metadata. The 365 focused Map/Regions tests,
generated/format/vet/Helm checks and candidate/deployed light/dark browser checks
pass. Marker-preview evidence confirms the exact side/stroke scaling and matching
green. All six deployments are healthy; schema/ledger, credentials, worker/OSM
controller and Martin are preserved. The owned verification database/forward are
removed; current Harbor tags match deployed pins.

## Monitoring Indicator Typography

Helm215 changes only the UI. ENABLED/DISABLED keep the shared all-caps font and
semantic colors while using a local `0.04em` tracking override. Two U+00A0 characters
separate monitoring from the state; shared heading spacing is unchanged.
Sixty-two Regions component tests and candidate/deployed Chromium dark/WebKit
light checks confirm exact text characters, computed tracking, table alignment,
responsive containment and retained start/resume/history behavior. All six
deployments are healthy; schemas, configuration and non-UI pods remain unchanged.

## Execution Policy, Inline Indicator And OSM Source Consolidation

Helm214 captures the current global storage-monitoring default at the first
authenticated read for each updater execution capability. Starts and paused/failed
resumes reload the default without resetting checkpoint identity or task history.
Repeated reads and later default changes cannot mutate a running execution;
completed history retains its final execution's value. Existing running policies
are protected during adoption and policy application is audited in job events.

The progress indicator now shares the task/elapsed status row and aligns to the
table's right edge. Its regular label omits the colon; ENABLED/DISABLED reuse the
heading's all-caps typography, green or n/a-gray respectively. Narrow viewports
wrap cleanly without overflow or losing table alignment. Settings help and the
operator contract describe the new start/resume semantics.

OSM region management is consolidated in `011_region_management.sql`, mode0644,
with source/schema11, minimum10 and ledger0-11. Catalog refresh metadata, request
binding, deletion receipts, generation high-watermarks and statistics receipts
remain intact. Fresh and adopted function/security contracts match; retained
ledger timestamps, all function identities/owners/grants/settings, trigger modes,
region/generation data and numbering are preserved. Application schema/ledger33
and minimum32 stay unchanged; obsolete higher OSM migration references are removed.

The Go suite, 96 focused UI tests, native first-read/running/repeated-read/pause/
failed-resume/history checks and fresh/adopted rehearsals pass. Candidate/deployed
Chromium dark and WebKit light verify exact text, colors, heading font, desktop
row/right-edge alignment and responsive containment. All six services are healthy;
credentials, Martin and existing updater identities are preserved. Current Harbor
tags match deployed pins. The owned native verification pod/forward are removed;
protected preimages and receipts remain outside Git.

## OSM Storage-Monitoring Policy And Adaptive Safeguards

Helm213 implements Settings > OSM Processing > **Monitor available database
storage**, persisted in the application database and enabled by default. Revision-
fenced saves require administrator authentication/CSRF and create sanitized audit
records. New region requests atomically capture the policy and settings revision;
database enforcement makes them immutable. Coalescing, retries, pause/resume and
later default changes retain the original request policy. Progress shows its
saved Enabled/Disabled value; deletion does not use monitoring.

Monitoring off skips all updater Prometheus storage requests/retries, background
sampling, post-batch freshness/settle waits and storage-triggered vacuuming.
Writer-primary checks and exclusive ownership remain independent. Temporary
object deletion, checkpoints, limits, required indexes/statistics, validation,
publication and final cleanup remain intact. No blanket final vacuum is added
for temporary grouping data that has already been removed.

Enabled monitoring accepts recent valid readings with at least 15% headroom,
avoiding mandatory 30/60-second scrape barriers after healthy tiny batches. Low-
headroom paths retain freshness checks, second observations and bounded settling;
missing/stale/invalid required metrics still fail closed. The authoritative
[work-unit skip matrix](osm-region-updates.md#storage-monitoring-policy) documents
each work unit and the disabled-policy behavior.

The additive canonical033 adoption is rehearsed against the prior installed
catalog and preserves schema/ledger33, minimum32, all existing function identities,
owners/grants/settings and original job policies. The OSM runtime contract is retained. Native
checks cover authorization/CSRF, strict inputs, revision conflicts/idempotent saves,
audit counts, new versus existing policy snapshots, trigger immutability and the
capability response. Unit checks prove monitoring-off retains primary validation
and required cleanup/statistics, and healthy versus low/invalid metric behavior.

The Go suite, 94 focused UI tests, candidate/deployed Chromium dark/WebKit light
and desktop/tablet/mobile policy/settings checks pass. All six services are healthy;
credentials, Martin and running updater identities are preserved. Current Harbor
tags match deployed pins. The native verification pod and forward are removed;
protected schema/function preimages and release receipts remain outside Git.

## Plain-Language OSM Step Help

Helm212 updates only the UI. All 58 updater descriptions are reviewed and rewritten
as concise, human-readable action/purpose summaries, with no more than two
sentences each. Database/algorithm jargon is removed from the tooltip column;
technical frequencies and surrounding specifications remain available to operators.
Three deletion descriptions and the older combined setup/download explanation
follow the same rule. The document remains the source of truth for current task help.

Verification covers the complete description inventory, sentence limits and jargon
checks, 61 focused UI tests, the production build and Go/document task-order
contract. Candidate/deployed Chromium dark and WebKit light inspect all 62 help
entries and mobile placement. All six services are healthy, unchanged schemas/
ledger and non-UI pods are preserved, and current Harbor tags match deployed pins.

## Metrics-Inclusive Durations And Historical Reconciliation

Helm211 corrects task and job duration semantics. A task's Finished timestamp is
the end of its last batch's metrics settling, and its tooltip is the active
first-to-final span. The job total independently covers all active execution,
including metrics waits, publication and maintenance. Paused/queued gaps are
excluded. Revisited task spans can overlap and must not be summed into a job total.

Checkpoint activity is reported before a cancellable metrics wait, but the task
remains Running until its settled boundary is reported. Interruption captures the
time actually spent without losing committed work. Controller finalization and
duration reconciliation are atomic with terminal-state/capacity publication.
Legacy executors are reconciled from their retained next-start/stop boundaries.

All seven completed/terminal records and 175 started task rows are repaired from
retained event boundaries and protected execution evidence, with preimages and
row/event guards. The historical Svalbard stop/container-start receipts close its
old recovery gaps without counting overnight/pause time. Monaco derive/segments
finishes at 11:25:50 AM, spans 90.754 seconds including metrics settling, and its
job total is 1h18m28s. The previous partial SQL-time counters are superseded.

Native tests cover ninety-second repeated work with tiny SQL increments, no
premature Finished value during settling, timestamp/span consistency, pause/queue
exclusion, idempotent reports and old/new task inventories. Fresh/adopted function
contracts match with schema/ledger33, existing identities and grants preserved.
Candidate/deployed Chromium dark and WebKit light review every repaired Finished
tooltip and timestamp. All six deployments are healthy; current Harbor tags match
deployed pins. UI/Martin and credentials remain intact; the disposable test pod is
removed. Detailed preimages and receipts remain protected outside Git.

### Queued Progress, Preparation And Ownership

Helm209 enabled View job progress from both menu and status before a queued job
has started. New 58-step requests separate prepare-source from download, with
operation-appropriate titles and the first step/waiting spinner. Earlier 57-step
requests retain compatible numbering. Healthy source preparation checks a durable
statistics receipt instead of rescanning installed regions. New generations or
interrupted maintenance still require repair; removal preserves valid surviving
statistics. The running clock includes active between-batch overhead.

Helm210 adds stable Deployment ownership for updater/cleanup Jobs, with only a
name-scoped Deployment GET and no owner-delete privilege. Real API admission
under the controller service account verifies the owner reference. Pod/ReplicaSet
rollouts do not replace the owner identity. The earlier native verification pod
was also removed after its checks completed.

## Short Progress And Completed Deletion History

Helm208 updates only the UI under the current `dev-20261120` tag. Progress lists
with twenty steps or fewer fit the actual header/rows; longer processing lists
retain the established twenty-row viewport. Successful deletion switches the
dialog title to **Deleted <region ID>**, both during completion polling and when
history is reopened from Available. Focus, horizontal scrolling and recorded
totals are preserved.

Verification passes 57 Regions component tests and candidate/deployed Chromium
dark/WebKit light checks. Browser evidence uses the actual Virgin Islands deletion
history and its **0.39s total**. One-, three- and twenty-step lists fit without
unused vertical space or a clipped final row; twenty-one- and fifty-seven-step
lists retain prior sizing. Desktop/tablet/mobile bounds, open completion and
Available-history reopening pass. All six deployments are healthy; application
schema/ledger33, minimum32, OSM11, configurations and non-UI pods remain intact.
Harbor's current tags match the deployed pins; protected receipts remain outside Git.

## Consolidated Region Management And Coverage Continuity

Helm207 deploys the consolidated application source and the revised region
deletion/retained-Coverage contract. Canonical033 now owns public catalog refresh,
durable region processing, successful-work timing and deletion. Canonical030 owns
display currentness. Application source/schema33, minimum32, baseline27 and
ledger0-33 match a fresh installation. Removed source files and obsolete higher
migration references are absent from the repository.

The adoption was rehearsed under the non-superuser migration role. Live cutover
uses protected schema/ledger/function preimages, quiesced application writers and
matching binaries. It preserves the retained ledger prefix and terminal timestamp,
all pre-existing function OIDs/owners/ACLs/settings, business data, credentials and
Martin. The resulting function contract exactly matches fresh source. Native
validation and the deployed migration binary accept the consolidated ledger.

Existing matched geometry and metadata stay current and usable after shared
regional source removal. The account-scoped display accessor separates saved
result freshness from prerequisites for a new calculation. Map routes remain
enabled with the current icon and no missing-map warning. Changed route/matching
identity or a newer accepted regional generation still makes prior Coverage stale;
new routes without applied Coverage remain unavailable while map data is absent.

OSM11 adds durable regional high-watermarks and immutable promotion numbers,
backfilled without renumbering existing promotions. Retirement/deletion does not
reset the counter. A fresh successful promotion increments it atomically; failed
processing does not consume a number. Native physical-deletion/reconciliation
checks preserve copied Coverage, cover historical results without auxiliary proof,
reject cross-account/policy/route changes, and confirm that generation2 with an
older timestamp still supersedes generation1. Existing-generation backfill is
also verified with an eighteen-promotion fixture.

Delete region uses the requested three logical lines and processing-job wording;
cleanup help and human-facing operator logs avoid generic processing terminology.
The Go suite, 52 Regions and 303 Map tests, fresh/adopted catalog comparison,
generated/format/vet/Helm checks and candidate/deployed Chromium dark/WebKit light
checks pass. All six deployments are healthy; current Harbor tags match all four
deployed application image references. Protected receipts remain outside Git.

## Processed Title And Coverage Retention Clarification

Helm206 updates only the UI under the current `dev-20261120` tag. Successful
processing jobs now show **Processed <region ID>**, both when opened from Ready
and when an already-open dialog refreshes at completion. The recorded total and
step-table focus remain intact. Active/non-successful and deletion titles retain
their existing meanings. All six deployments are healthy; API, worker, Coverage
worker, OSM controller and Martin pods, schema/ledger and configurations are preserved.

The region guide explains that unfinished builds are temporary import/reprocess
artifacts. Region deletion retains workouts, GPS routes and application-side
Coverage snapshots. Deployed map tiles use saved Coverage chunks/matches and do
not read the deleted OSM region; missing-map reconciliation retains applied
results as stale rather than purging them. New matching needs suitable active map
data. This is a documented existing contract, not a new retention-policy change.

Verification passes 52 Regions component tests, production build, generated/
format/vet/Helm checks and candidate/deployed Chromium dark/WebKit light checks.
Browser checks cover both an actual completed job and the live
Processing-to-Processed transition with recorded total and table focus preserved.
Deployed schema/function inspection confirms cached geometry/metadata storage and
retained-result rendering without reading private workout data or deleting a region.

## Progress And Typed User Confirmation

Helm205 publishes only API/UI changes under the current `dev-20261120` tag. All six
deployments are healthy; the then-current application contract, OSM11/minimum10 and the original
ledger/configuration remain unchanged. Worker, Coverage worker, OSM controller and
Martin pods are preserved.

- Generation reuses the shared right-aligned numeric lane and provides a minimum
  2em styled-help target without making the entire cell a help cursor.
- Progress names its first column Step and initially focuses its scrollable list.
  Native Up/Down/Home/End scroll only the list, retaining the underlying workspace
  position and the normal close-focus handoff.
- Completed repeated steps show their stored batch counts instead of suppressing
  them outside Running. Svalbard's actual 57-step history verifies all retained
  repeated-batch values.
- The current-task line adds live successful-work elapsed time and switches to the
  recorded total after completion. The read-only API projects the active batch
  start after the last completion/stop/resume boundary, excluding old attempts and
  pause/queue gaps. No database migration or new batch timestamp storage is added.
- Delete user? retains its warning and reuses the workout-range confirmation
  field and Cancel/Yes actions. Only exact uppercase DELETE enables Yes; dismissal
  or selecting another target clears confirmation. Existing CSRF, duplicate-submit,
  contention-retry and inactive-session guards remain covered.

Verification passes the Go suite, 93 focused UI tests, native administrator/API
elapsed-window projection checks and candidate/deployed Chromium dark/WebKit light
checks. Browser checks cover the actual completed Svalbard job, two-digit versus
single-digit alignment, minimum hover area, native keyboard scrolling, ticking
elapsed time, exact typed confirmation and desktop/tablet/mobile containment.
Protected evidence stays outside the repository; the owned native fixture and
forward are removed, and Harbor's current tags match deployed references.

## Region Deletion And Recovered Timing

Helm204 deploys the administrator region-deletion workflow and checkpoint timing
recovery, retaining the `dev-20261120` tag with matching Harbor/deployment digests.
All six deployments are healthy. Application the then-current application contract and OSM
schema13/minimum10 preserve their earlier ledger rows, capability RPC identity and
active NorCal, Virgin Islands and Svalbard data.

Svalbard generation1, build66 now has **1h 17m 34s total** in its promotion tooltip.
The recovery uses the saved job accumulator, successful batch audit boundaries,
the committed task27 checkpoint and surviving later task counters. It excludes
failed attempts and pause/queue gaps. Checkpoint reconciliation now adds a safely
bounded lost batch to prior totals instead of clearing the accumulated job time;
ambiguous intervals remain unknown.

Delete region names the target in the shared confirmation and shows red Deleting
through queued/running cleanup and retries. The approved executor detaches active
partitions, drops the region's tables/indexes/TOAST/build schemas and records a
verified receipt. The controller independently confirms absent resources and
stopped pods before publishing Available. Prior checkpoints cannot resume deleted
data. Provider boundaries, job/promotion history and neighboring regions remain
intact. Completion respects Show all regions and returns dialog focus to the
Regions heading if the deleted row becomes hidden.

Verification covers the Go suite, 50 Regions UI tests, duration/document contracts,
native administrator authorization/CSRF/coalescing and stale-resume rejection,
capability-fenced executor reports, controller dispatch/retry/stop confirmation,
and the exact previous-to-updated application/OSM migration upgrade. Interrupted
native cleanup resumed successfully and reclaimed **34,799,616 bytes** of physical
database storage while retaining a neighboring region and promotion history.
Chromium dark and WebKit light pass against candidate and deployed assets at
desktop/tablet/mobile sizes, including Svalbard's actual recovered duration data.
Disposable PostgreSQL and migration Jobs are removed; protected receipts remain
outside the repository. Developer workflow details live in
[Administrator Region Updates](osm-region-updates.md).

## Finished Elapsed Help And Historical Recovery

**11/20/2026, Helm200:** The visible legacy runs predated timing counters and had
null totals, so Finished values rendered without their tooltip wrapper. Finished
timestamps now always expose the shared help cursor/tooltip; missing timing gets
an explicit unavailable explanation instead of silently becoming plain text.
Known successful-batch totals retain the `15m 24s total` format. Started and n/a
values do not acquire spurious elapsed help.

Retained progress audit events provide paired starts and successful completions
for the older runs. A guarded recovery sums those pairs across all batches,
excludes interrupted/failed attempts and resume idle gaps, handles the initial
source-preparation start, and includes successful promotion/cleanup. Only null
historical counters are filled; timestamps/history are preserved and a recovery
receipt is appended. Unrun legacy steps are safely initialized to zero for later
resume accounting. No wall-clock task-span estimates or rounded console-duration
guesses are used.

The actual Virgin Islands API response now has totals for all57 Finished rows;
`identity-propagate/compress` shows **20m 30s total**. Both live Chromium dark and
WebKit light compare every rendered tooltip against that real response and verify
the help cursor/hover display. Focused UI tests cover the requested 15m24s format,
missing legacy timing, and plain Started/n/a values. The UI-only rollout preserves
backend/controller/Martin pods, the then-current application contract, and current image-tag pins.
Protected audit preimages/recovery guards and real-data screenshots are retained
under `osm-finished-elapsed-help` outside the repository.

## Region-Local Generations And Build Identity

**11/20/2026, Helm199:** Generation means the sequential successful-promotion
number within one region, starting at1. A global build ID identifies an attempt
and is never a generation number. Failed/canceled attempts do not advance regional
generations. Virgin Islands displays generation1 with build65; NorCal displays
generation18 with build62, based on retained promotion history.

Public catalog `generation` is regional; `buildId` retains the immutable internal
key for schemas, checkpoints, matching and duration lookup. Public job/operator
status also names build IDs explicitly. Attempt logs use build ID only, then
successful promotion/completion logs pair values as **generation 18; build 62**.
Operator commands use `--build`; old `--generation`/environment spellings remain
documented compatibility aliases for build IDs. Historical numeric notes are
corrected with regional numbers and original build IDs; unpromoted/synthetic
attempt references are labeled builds instead of assigning fabricated versions.

Verification includes read-only live catalog checks across both promoted regions
and failed history, promotion-log unit coverage, Go/generated/format/vet/Helm checks,
45 focused UI tests, and live Chromium dark/WebKit light checks. A guarded public
projection refresh updates existing cached metadata without changing build IDs or
map data. the then-current application contract and OSM11 remain unchanged, all six deployments are
healthy, and four pinned references match current Harbor `dev-20261120` tags.
Protected mapping, source/image and browser evidence is retained under
`osm-regional-generations` outside the repository.

## Successful Elapsed Totals And Final UI Help

**11/20/2026, Helm198:** Failed and Canceled menus keep Show boundaries first,
View job progress second, a divider, then Resume processing/Clear job status or
Download region/Clear job status. Ready reprocesses; Paused resumes/cancels.
Clear dismisses only the latest terminal overlay, restoring Available or Ready
while retaining data/audit history. Status action colors are preserved in both
themes, with the shared accent hover.

The Virgin Islands failure was a task4 importer connection routed to a read-only
standby. The adapter now preserves each host/port and primary-only session
selection for libpq tools. A live standby-first transactional temporary-DDL probe
passes. The original request was resumed with the corrected approved image and
completed all57 tasks, promoting Virgin Islands generation 1 (build ID 65); the earlier NorCal data is retained.

Task Finished help and Generation help use accumulated successful execution time,
not wall-clock gaps: **2m 40s total** and **November 8, 2026 | 16h 24m 23s total**.
The consolidated region-management source adds nullable counters plus execution-scoped sequence deduplication.
Successful batch/promotion/cleanup increments survive pause/failure/resume without
counting idle time or failed attempts. No per-batch start/end records are added.
Unknown historical totals stay omitted. The Generation lookup matches promoted
generation history independently of newer failed/cleared attempts. Source/schema
are35, minimum32, baseline27; the original0-34 ledger prefix and public RPC OID are
preserved. Secrets and Martin remain intact.

Summary aggregate hints now use two lines: Click to view averages; press and hold
shift / key to hover over averages by workout type, substituting totals in both
places when appropriate. Existing preview/Shift behavior is retained.

Verification covers Go/generated/format/vet/Helm checks, 45 focused OSM UI tests,
the existing Summary hint test, fresh the then-current application contract/native elapsed deduplication and
resume tests, and live Chromium dark/WebKit light checks of all status colors,
menu ordering, both elapsed tooltips, focus and responsive dialog layouts.
Four immutable application pins match current Harbor `dev-20261120` tags; all six
deployments are healthy. Protected backups, source/image proofs and screenshots
are retained under `osm-elapsed-totals` outside the repository.

## Region Progress Actions And OOM Diagnosis

**11/20/2026, Helm196:** Eligible Downloading, Processing, Canceled, Paused, Failed
and Ready status cells now open View progress, sharing menu availability and the
standard full-cell action. Ready requires retained managed run history. Dismissal
returns focus to the originating status/menu action. The current-task line uses
**20 of 57 — clip-replacements — batch 25 of 95**.

The Svalbard/Jan Mayen run started its container at 3:25:07 AM PST but did not emit
its first task event until 3:28:36. Download completed in about two seconds; the
kernel then killed `osmium tags-filter` and the updater together when their memory
cgroup hit **1Gi**. A bounded, database-free reproduction using the same source
URL/filter command succeeded with **1,101,084 KiB** peak resident filter memory.
The updater now reserves **2Gi**, capped at **4Gi**, independently of PostgreSQL
storage preflight.

Fresh source preparation reports task activity after exclusive writer acquisition
and before cleanup/statistics replay, closing the silent maintenance gap. Both
active-table and open-dialog polling remain ten seconds. The controller captures
UID-owned pod task/OOM/exit/limit evidence before Job deletion and preserves it
through terminal finalization instead of replacing it with a generic storage
suggestion. Raw termination messages are not copied into the UI.

The original failed request retains its precise OOM cause and original timestamps.
Its sole empty pre-import candidate schema was removed under the shared updater
lock with exact request/generation guards; active NorCal generation 18 (build ID 62) is untouched.
Verification includes Go/generated/format/vet/Helm checks, 35 focused UI tests,
production assets, native PostgreSQL OOM-evidence persistence and live Chromium
dark/WebKit light status-click/focus/responsive passes. API and Martin pod UIDs,
Secrets, the then-current application contract and the existing OSM11 contract remain unchanged.
Worker/controller, UI and OSM use new immutable pins under the current
`dev-20261120` tag. Protected diagnosis, reproduction and deployment evidence is
retained under `osm-progress-diagnostics` outside the repository.

## American Vocabulary And Updater Help

**11/20/2026, Helm195:** All four application images now use `dev-20261120` with
matching immutable deployment pins. Superseded application tags/artifacts and
their attached accessories were removed from Harbor after live/retained image
reference checks; unrelated repositories are unaffected. Six deployments are
healthy, Secrets/configuration and the Martin pod are preserved.

Source, API fields/states, metrics and database contracts use American
`canceled`/`canceling`. A quiet-writer backup and transactional reconciliation
preserve all original Goose rows/timestamps, function ownership/settings/ACLs and
trigger modes. Application source/schema/minimum remain **34/34/32**, baseline27,
the pre-fold ledger, with no permanent spelling-only migration. Native synthetic-data preservation
and all 289 fresh/converted function contracts match.

New Coverage omits future-empty month/year buckets, including custom ranges,
before totals/pagination. Actual future workouts retain their bucket even when
they have no Coverage visits. Current periods resolve in the report time zone.
Native PostgreSQL regression coverage includes both groupings and pagination.

Progress titles now read **Downloading geofabrik:monaco** / **Processing
geofabrik:monaco**, and the X shares the boundary dialog's hover border. All 57
task names expose styled help with the question-mark cursor and keyboard-focus
support. The UI imports the rebuild inventory's **Work and completion boundary**
cells directly from Markdown; there is no separate maintained description list.

Verification includes Go tests/generated/format/vet/Helm checks, 24 focused UI
tests and a production build. Live Chromium dark/WebKit light compare every
tooltip with its document cell, check help cursor/focus and confirm responsive
dialog behavior. Both ingress origins serve the reviewed asset hashes. Protected
backups, catalog/ledger proofs, image/SBOM manifests, screenshots and Harbor cleanup
receipts are retained under `dev-20261120-american` outside the repository.

## Managed Region Update Workflows

**Deployed at Helm194.** Download/Reprocess now enqueue a coalesced audited region
request. The dedicated controller dispatches approved UID-fenced Kubernetes Jobs
with default concurrency1, configurable through Helm. The stronger shared OSM
writer lock is retained; legitimate managed waiters use a distinct PostgreSQL
application name so stale-updater recovery does not terminate them.

Regions exposes Waiting, Downloading and Processing distinctly, with Pausing,
Paused, Canceling, Canceled and Failed states and matching eligible menu actions.
Pause stops the current unit and retains durable post-import work. Resume requeues
behind active work with the pinned original image. Cancel explicitly discards
only the request-bound unfinished candidate. Capacity is held until actual pod
termination and, for Cancel, confirmed cleanup; interrupted cleanup gets an owned
cleanup execution. Atomic promotion is not reversible by cancellation. Delete
region remains a separate disabled future workflow.

View progress polls every ten seconds, shows all 57 inventory tasks in order
without sorting/search/paging and uses a 20-row scroll frame. The current line
shows task order/name and known repeated-batch progress. Started/Finished are local
logical-task timestamps; every Order cell reserves its icon position. Completed,
running, failed, canceled and paused states use the shared circular status art.
Stopped outcomes retain framed status/error detail and audit history.

Application033/OSM11 migrations pass on isolated PostgreSQL/PostGIS. Native
tests verify admission/CSRF/role separation, ordered cancellation history,
single capacity, paused resume queueing, ambiguous-create adoption, stale-token
rejection, owned-only deletion, cleanup gating and preservation of active data
during real candidate abort. All Go/generated/format/vet/Helm checks, **1,045 UI
tests**, and candidate/deployed Chromium dark/WebKit light workflows pass.
Kubernetes smoke checks verify the published updater's pinned osmium1.19.0 and
osm2pgsql2.3.1 plus its SQL/Lua inputs.

Live API admission/coalescing/cancellation retains all 57 tasks. A writer-lock-
gated live check launches two real owned Kubernetes executions through pause,
resume and cancellation without allocating a generation or downloading a PBF.
Both temporary verification admin sessions are revoked, promoted NorCal generation 18 (build ID 62)
is unchanged, and no heavy public rebuild is initiated by acceptance testing.

Backups include a fully read application archive and bounded OSM schema/catalog
preimages. Two limited app runtime credentials and a primary-aware updater URL
are added without rotating old credentials. Only the new controller receives
Job/pod discovery permissions; Secret browsing, exec/log, RBAC/Deployment/PVC
mutation are denied. Updater Pods carry no Kubernetes token or owner keyring.
All six deployments are healthy; Martin and existing configuration values remain
unchanged. Protected build/backup/native/browser/RBAC/live receipts are under
`deploy-internal/osm-region-updates`. See [the workflow guide](osm-region-updates.md).

## Regions Live-Progress Tooltip Dismissal

**Deployed at Helm 193.** The refresh tooltip no longer retains focus from the
button removed when the action becomes a status label. Portal help validates its
focused child, dismisses on outside pointerdown/window blur/Escape and does not
reopen after content updates. Regions also uses the existing keyboard-only focus,
pointerdown and pointerleave dismissal controls for this action. Keyboard help
and the shared single-tooltip owner remain supported.

The owner declined a heavier boundary stroke, so the outline stays **5px** with
round joins and `line-round-limit: 0`; geometry is not smoothed or altered. The
same retained canvas/context and square map layout are preserved.

All **1,042 UI tests**, 38 focused tooltip/Regions cases, CSS/format checks and
production build pass. Candidate and deployed Chromium dark/WebKit light checks
reproduce clicking Refresh, the live **270 of 554 regions cataloged** help, pointer
exit, counter changes, outside click, Escape, blur and hover-owner transfer. They
also verify that job completion updates the original row in place and restores
Refresh catalogs without page reload. All five deployments are healthy; only UI
rolls, existing backend/Martin pods and configuration/database versions remain
unchanged, and Harbor's current UI tag matches its deployed pin. Protected
receipts are under `deploy-internal/osm-regions-tooltip`.

## Regions Minor Words, Track Transfer And Join Limit

**Deployed at Helm 192.** Generic region title casing now covers mit/et/y/with/
de/da/la/includes and the intact, case-insensitive **a.k.a.** abbreviation. Initial
words retain their capital; parenthetical descriptions do not make includes or
a.k.a. an initial word. Tests cover uppercase input, first-word exceptions,
parentheses and abbreviation punctuation alongside the existing Unicode cases.

Downloaded and Status each transfer exactly 10% of their prior widths to Name.
The flexible last data track still reaches the fixed, padded ellipsis boundary.
An inline measured width accounts for the fixed action track across responsive
sizes; the action header, rather than WebKit's zero-width col rectangle, provides
the physical measurement. Baseline-relative Chromium/WebKit pixel checks verify
both reductions, their combined transfer, all other column widths and the fixed
action boundary at desktop/tablet/mobile widths. The 80rem Regions limit remains.

Boundary outlines retain `line-join: round` and now specify `line-round-limit: 0`.
MapLibre defaults the latter to 1.05 and otherwise substitutes a miter at shallow
turns (approximately interior angles above 145 degrees). Disabling that shortcut
keeps obtuse joins in the round-rendering path; a 5px stroke can still make the
rounding visually subtle near a straight segment. Canvas/context retention and
the line layer's other geometry/paint properties are unchanged.

Backend and focused Regions checks, generated/format/vet, production build and
candidate/deployed dark/light geometry checks pass. Native/read-only API inspection
confirms cached shapes and 554 normalized regions; temporary admin verification
sessions are revoked. All five deployments are healthy, Martin/configuration and
database versions remain unchanged, and current Harbor tags match deployed pins.
Protected receipts are under `deploy-internal/osm-regions-word-track`.

## Regions Columns, Title Casing And Map Polish

**Deployed at Helm 191.** Database usage's relative column allocation is reduced
25% and transferred to Name. Status consumes remaining width up to the fixed
shared ellipsis column, so its hover track ends at the trigger's padding rather
than leaving spare action-column space. Regions' whole aligned section is capped
at **80rem**, independently of Workout log's unchanged 90rem. Boundary lines now
join at rounded corners without changing their 5px width, opacity or layer order.

Region display formatting is generic Unicode word-level title casing, not
Województwo/kraj or per-region name matching. Common minor words stay lowercase
except at segment starts, known acronyms/internal capitals are preserved and
hyphenated components capitalize. Tests cover **Polynésie Française (French
Polynesia)**, **Île-de-France**, **Côte d'Ivoire**, Polish/Czech labels and English
prepositions. Native API/cache authorization and projection checks pass with the
new generic formatting. Live inspection confirms French Polynesia and the
localized labels among 554 regions; its temporary admin session is revoked.

Candidate/deployed Chromium dark and WebKit light geometry checks pass at
desktop/tablet/mobile sizes, including the baseline-relative allocations, 80rem
limit and exact Status/action padded boundary. Focused Regions tests include the
round-join layer contract. Backend tests, generated/format/vet and production
build pass. The final frontend-only additions reuse backend images only after
byte-verifying their source inventories remain unchanged; both build cohorts
and the combined receipt are retained under `deploy-internal/osm-regions-columns`.

No schema/configuration changes or region-data lifecycle commands are introduced.
All five deployments are healthy and existing tag pins are consistent. Martin
and the 18 configuration tuples remain unchanged.

## Regions Behavior And Presentation Follow-Up

**Deployed at Helm 190.** The Regions search explicitly permits navigation while
unfocused, restoring admin 1-4 and arrow paging without weakening focused-editor,
menu, modal, modifier or composition guards elsewhere. Show all regions defaults
unchecked and the viewport always reserves 25 slots, including the current
single-prepared-region view.

Regions now owns its `osm-regions-*` CSS hooks instead of inheriting coverage
search/footer/checkbox selectors. Identical declarations share selector groups.
Only the Regions box omits Road Coverage's +1px offset, moving it up one pixel;
the Road Coverage rule remains intact. Ellipsis controls align to the right so
their top/right/bottom cell insets match. Plain unavailable values without help
have no tooltip wrapper and inherit the clickable row's hand cursor.

The retained Streets canvas is square at its existing width. The dialog scrolls
on short screens with a sticky close heading; canvas/context reuse still passes.
Polish Województwo and Czech/Slovak kraj labels now title-case localized words and
hyphenated components without changing region IDs. Provider-supplied already-
pretty labels, including District of Columbia, remain valid.

An accepted/pending refresh shows yellow, non-clickable **Refreshing catalogs**
with help cursor and **completed of total regions cataloged** progress. Partial
metadata help uses an em dash instead of a semicolon. No automatic refresh or
new region lifecycle action is introduced.

Go/generated/format/vet checks, all **1,040 UI tests**, 83 focused Regions/admin/
shortcut tests and candidate/deployed Chromium dark/WebKit light checks pass.
Pixel checks cover checkbox position, square canvas, ellipsis insets, cursors,
header labels and narrow/desktop containment; functional checks retain exact
canvas/context identity through region/theme changes. Real API inspection finds
554 regions, valid cached boundaries and corrected localized labels; temporary
verification admin sessions are revoked. The image-only rollout preserves
configuration/database versions and Martin; all five deployments are healthy.
Protected evidence is under `deploy-internal/osm-regions-followup`.

## Regions Table And Retained Boundary Map

**Deployed at Helm 189 after the owner approved the current Helm-188 baseline.**
Regions now uses the Users H2/text-action composition, Workout log maximum width
and Road Coverage search/viewport/footer. Columns are Name, Date, PBF size,
Downloaded, Generation, Database usage and Status, plus the standard action menu.
The table keeps 25 stable row slots across compatible paging/search/sort/polling,
does not shrink on a short last page and defaults Show all regions to checked.
Date/size extents are left-anchored and their values right-aligned, with the
requested local dates, exact tooltips and muted `n/a` values. Numeric/date sort
defaults descend; Name starts ascending; Generation is not sortable. Provider
HTML breaks and raw path-slug names are normalized without changing IDs.

Show boundaries works from a row, native keyboard name action or menu. The modal
uses the theme's Streets style, a magenta 25%-opacity fill and 5px outline beneath
base labels/symbols, and fits the boundary when opened. One map, canvas and WebGL
context survive dismissals, sequential region examinations and theme changes;
the DOM node is parked while the normal modal portal is absent, and only actual
workspace/session teardown disposes it. Cached geometry is fetched per region
through an administrator-only endpoint, not returned in bulk lists or fetched
from the provider on dialog open.

The owner explicitly chose **UI and boundaries first**. Download/Reprocess/
View progress/Delete entries are correctly selected and disabled pending their
backend lifecycle slice. No fake jobs, reimports or map-data deletion are wired.
Existing shared full-cell actions and checkbox/header styling from 188 remain
the composition baseline. A narrow raw-route-ref snapshot fixes the existing
typecheck error without changing diagnostic route behavior; the CSS contract
check now recognizes the shared padded action primitive.

Go/generated/format/vet checks, all **1,036 UI tests**, native PostgreSQL/PostGIS
boundary authorization/projection checks and actual Streets-map Chromium dark/
WebKit light checks pass. Browser checks verify exact tooltips, untruncated
headers, numeric extents, fixed paging height, menu independence, focus, and
desktop/tablet/mobile containment. They retain and compare the exact canvas and
WebGL context across successive dialogs/themes. Real admin API verification
finds **554** regions, humanized District of Columbia and Trøndelag labels,
valid cached polygons for three regions, and no geometry in bulk responses.
Its temporary admin verification session is revoked afterward.

The image-only rollout preserves all 18 configuration tuples, database versions
and Martin's pod. No catalog refresh, PBF download, reprocessing or deletion is
needed to populate boundaries: the worker refreshes the public projection at
startup. Protected receipts are under `deploy-internal/osm-regions-ui`. One
post-build formatting adjustment is recorded with byte-identical TypeScript-
emitted JavaScript; frozen build inputs and deployed asset hashes remain pinned.

## Road Coverage Cell Targets And Checkbox Alignment

**Deployed 11/19/2026, Helm 188.** Move only Road Coverage's unnamed-filter box
down one pixel with relative positioning; its label and footer layout stay fixed.
Replace text-height Road Coverage actions with the existing New visits full-cell
pattern, generalized as `.table-action-cell`/`.table-cell-link` and shared by both.
Action cells transfer their padding to the full-width/height native button and
show a hand cursor throughout. Preserve Road Coverage's 42px row height, date
eligibility, single keyboard stops, hover behavior and inset focus rings.

All 332 Map/Stats tests and the production build pass. Candidate/live Chromium
dark/WebKit light checks compare settled before/after text ranges and prove a
one-pixel box movement with zero label displacement. Trusted clicks at top-left
and bottom-right padded cell edges close the dialog through the existing name/
workout actions exactly once; entity detail caching remains effective. Out-of-range
dates keep plain, non-pointer cells. Only UI rolls; four other pods, 18 configuration
tuples and database contracts remain unchanged. Both origins serve verified
assets, Harbor's existing tag matches the deployed UI pin and all five deployments
are healthy. Protected evidence is under `deploy-internal/road-coverage-cell-targets`.

## Symmetric Inline Checkbox Edges

**Deployed 11/19/2026, Helm 187.** Live inspection confirms Road Coverage's unnamed
filter checkbox fits within its footer and has no CSS shadow. Native browser
appearance at a fractional layout position produces the bevel/uneven edge effect.
Reuse `.inline-checkbox` for this filter and the existing Data Sync artifact-failure
filter: flat theme-token outline/fill, no bevel/shadow, nonshrinking size and a
checked indicator. Preserve native input/label/Space behavior, Tab focus, muted
disabled states and native forced-colors fallback. The guide records this shared
inline-filter control composition.

All 62 focused Map/Data Sync tests and the production build pass. Candidate/live
Chromium dark/WebKit light pixel checks pass at device scales 1 and 2, comparing
the checked/unchecked top and bottom stroke colors and rejecting paint/shadow
outside the box. Keyboard, label, disabled and forced-colors checks pass too.
Only UI rolls; four other pods, 18 configuration tuples and database contracts
remain unchanged. Both origins serve verified assets, Harbor's existing tag matches
the deployed UI pin and all five deployments are healthy. Protected before/after
evidence is retained under `deploy-internal/coverage-checkbox-edges`.

## Shared Table Header Hover Edges

**Deployed 11/19/2026, Helm 186.** Live reference/dialog measurements find three
pixels above sticky-dialog hover fills versus one in Workout log/New Coverage.
No header/button shadow is present: fractional centered-dialog transforms blend
the hover boundaries. Remove the redundant sticky offset/scroll padding, make
the common one-pixel header-cell inset explicit, and align both centered table
dialogs and their sticky viewport to device pixels after entry animation/fonts.
The shared stationary outline remains independent of the scrolling paint layer.

The production build and all 429 Map/Summary/Stats tests pass. Candidate and live
Chromium dark/WebKit light checks compare ordinary and dialog header hover gaps,
check one pixel above/below, and inspect screenshot pixels for sharp hover/base
color boundaries. Shared sort/hover/focus/background checks pass across user/admin
tables. Candidate border-paint checks and Chromium high-DPI checks also pass; an
additional post-rollout high-DPI/border rerun was blocked by browser-server startup
timeouts after the main live hover checks had passed. Both origins' asset hashes
and all five healthy deployments are verified. Only UI rolls, preserving four
other pods, 18 configuration tuples and both database contracts. Harbor initially
returned 503, recovered, and its existing tag now matches the deployed UI pin.
Protected reference, pixel, build and rollout receipts are retained under
`deploy-internal/table-header-hover-edges`.

## Shared Sticky Table Viewport And Painted Outline

**Deployed 11/18/2026, Helm 185.** The owner again observed Road Coverage's top
border missing despite correct geometry after 184's opaque table surfaces. Live
Playwright pixel inspection reproduced about 69% of the expected one-pixel stroke
after desktop scrolling: geometry-only checks had missed paint-layer occlusion.

Centralize scrolling-table chrome in `DialogTableViewport`'s `stickyHeader` mode.
Road Coverage and New visits both opt into the same scroll-container, inset,
sticky-header and stationary-outline rules. Remove their separate header/wrapper
style blocks. The shared frame paints a single outline above the scrolling layer,
while a transparent layout border preserves measurement and pointer behavior.
The guide requires reuse of this primitive rather than feature-specific chrome.
Normal table/header surfaces and orange-text-only sorting remain unchanged.

All 429 Map/Summary/Stats tests and the production build pass. Candidate/live
Chromium dark and WebKit light pixel checks pass at device scales 1 and 2 for
Road Coverage and New visits, before/after vertical and horizontal scrolling on
desktop/narrow viewports. They compare original theme pixels with diagnostic
border-color renders and measure full stroke coverage, not just bounding boxes.
The existing user/admin table surface, sort, hover/focus and containment browser
matrix also passes. Only UI rolls; four other pods and 18 configuration tuples
remain unchanged, both origins serve verified assets, Harbor's current tag matches
the deployed pin and all five deployments are healthy. Protected evidence,
including the failing 184 pixel observations, is retained under
`deploy-internal/road-coverage-painted-border`.

## Shared Table Header And Sort Styling

**Deployed 11/18/2026, Helm 184.** All shared tables use the same opaque base
surface for headers and normal rows. Road Coverage and New visits retain sticky
behavior/border clearance without a raised header band. Active ascending and
descending columns use orange labels/direction indicators only, with no persistent
sort-state fill. Active labels remain orange during hover; ordinary shared hover
feedback and keyboard focus outlines remain intact. These are explicit standards
in `docs/ui-style-guide.md` for user and admin interfaces.

All 1,025 UI tests and the production build pass. Candidate/deployed Chromium
dark/WebKit light checks verify Road Coverage at four viewport widths, Workout
log, New Coverage report, New visits dialog, owner Data Sources, Data Sync history,
admin Users and admin Regions. They compare actual header/row surfaces, reject
sorted-cell background fills in both directions, and preserve hover/focus and
existing sticky-border/frozen-height behavior. Only UI rolls; four other pods,
18 configuration tuples and both database contracts remain unchanged. Both
origins serve verified assets, Harbor's current tag matches the deployed UI pin
and all five deployments are healthy. Protected evidence is retained under
`deploy-internal/table-header-standard`.

## Road Coverage Sticky Header Top Border

**Deployed 11/18/2026, Helm 183.** The owner confirmed the bottom border after
182 but still observed the top border covered. The outer-frame correction alone
did not address Road Coverage's zero-offset sticky header. Match the New visits
prior art's one-pixel sticky top inset so the header clears the scrollport border.

Five focused Road Coverage tests, production build and candidate/deployed
Chromium dark/WebKit light checks pass. Browser checks now explicitly verify the
sticky header's top clearance at rest and after vertical/horizontal scrolling,
alongside the prior outer-frame, frozen-filter and viewport checks. Only UI rolls;
four other pods, all 18 configuration tuples and database contracts stay intact.
Both origins serve verified assets, the existing Harbor tag matches the pin and
all five deployments are healthy. Evidence is retained under
`deploy-internal/road-coverage-sticky-border`.

## Road Coverage Border Height Correction

**Deployed 11/18/2026, Helm 182.** Give table dialogs a one-pixel vertical paint
inset so the outer clipping frame does not trim the scrollport's top/bottom
border strokes. Include the frame padding in measured reserved height, empty-state
header/bottom offsets and the viewport cap. Retain frozen search/filter/paging
capacity and viewport containment; Summary's non-dialog frame remains unchanged.

All 429 Map/Summary/Stats UI tests, production build, candidate/deployed Chromium
dark/WebKit light checks pass. Browser measurements prove border clearance at
1920/1440/1280/390 widths, stable filtered/empty-result height, and the same fix
against the New visits dialog prior art. Only UI rolls; four other pods, all 18
configuration tuples and both database contracts remain unchanged. Both origins
serve the verified assets and all five deployments are healthy. Protected
evidence is retained under `deploy-internal/road-coverage-border-height`.

## OSM Provider Catalog Refresh

**Deployed 11/18/2026, Helm 181.** Regions now lists configured providers' cached
catalogs with current provider size/file date and installed-generation download/
database measurements. **Refresh provider catalogs** accepts one coalesced,
audited admin job. The independent worker lane saves each index first, then
enriches metadata with HEAD-only requests, bounded by
`osm.catalogRefresh.metadataConcurrency` (default5, range1-32), independent of
ingest slots. Browser navigation does not cancel accepted work; active polling,
retained row nodes/search/sort and safe partial results are verified.

The restricted OSM runtime login cannot update/promote generations, delete
regions or create schemas. Primary selection uses three certificate-valid node
Service DNS endpoints and `target_session_attrs=read-write`, with exact-name
vcluster DNS forwarding. Existing credentials, DNS entries and app configuration
values remain intact. This adds no worker Kubernetes privileges. Source33/OSM11
migrations are additive; active map NorCal generation 18 (build ID 62), source SHA and downloaded bytes
are unchanged. Completed-download timestamps and processed-size snapshots are
recorded going forward, without inventing old timestamps. Current public storage
projections refresh every five minutes without provider HTTP.

Go tests, generated/format/vet/Helm checks, all **1,025 UI tests**, native
PostgreSQL/PostGIS role/CSRF/coalescing/recovery/storage checks and candidate/live
Chromium dark/WebKit light desktop/tablet/narrow checks pass. Native concurrency
reaches exactly five; failures preserve last-known metadata and a replaced worker
reclaims its expired lease. Application backup and bounded public OSM catalog/
schema preimages pass full archive reading and SHA receipt retention.

A real administrator API refresh discovers **554** Geofabrik regions and finishes
in **332 seconds** with **511 successful metadata observations and 43 unavailable**,
reported as partial success. Every region is processed; unavailable values stay
cached/Unknown rather than zero. Duplicate requests coalesce. The active norcal
NorCal generation 18 (build ID 62) retains its 655,461,159 downloaded bytes and reports **5,392,506,880**
active database bytes; its historical download-completion date remains Unknown.
The temporary verification administrator session is revoked after inspection.

All five deployments are healthy, Martin's pod is unchanged, and Harbor's existing
tags match deployed pins. Protected build, backup, native, browser, DNS/role and
rollout receipts are under `deploy-internal/osm-provider-catalogs`. The normal
local container store had I/O errors, so this release uses an isolated build store.
Extract preparation/promotion UI and the remaining OSM/Operations workflows are
still pending. See the [catalog refresh guide](osm-catalog-refresh.md).

## Users Menu Order And Recovery Scope

**Deployed 11/17/2026, Helm 180.** Registered-user menu order is **View usage**,
conditional **Revoke sessions**, **Disable user / Enable user**, then the existing
divider and **Delete user**. Session availability, expiry, authorization and
revocation behavior retain the Helm-179 contract.

The owner explicitly drops the planned administrator-triggered password reset
email. Self-service **Forgot password?** remains the recovery path. Product,
functional, admin, architecture and milestone documents reflect that decision.

All 30 administrator UI tests, production build and candidate/deployed Chromium
dark/WebKit light checks pass, including exact menu order and desktop/tablet/
narrow containment. Only UI rolls; API, workers, Martin, schema/minimum 32/32 and
18 configuration tuples remain intact. All five deployments are healthy and
Harbor's existing tag matches the deployed UI pin. Protected rollout/browser
receipts remain under `deploy-internal/admin-session-menu-order`.

## Accounts And Source Follow-Ups Through Helm 179

**Deployed 11/17/2026, Helm 179.** Accounts now offers **Revoke sessions** only when
an active user has an unrevoked, unexpired browser or bearer credential. This is
usable-token state, not online presence. The directory returns only the latest
valid expiry. Refresh it on menu open and hide the action until authoritative
availability returns; an expiry timer also removes it from an already-open menu.
The CSRF-protected administrator command serializes with sign-in, revokes all
valid sessions and audits atomically, without changing password/account state,
canceling jobs or blocking on an ingest account lock. Repeating after revocation
is a no-op; future sign-in is permitted. Another owner/admin session is unaffected.

The rollout changes only API/UI pins, retains schema/minimum **32/32** and all
18 configuration tuples, and verifies all five deployments healthy. Existing
`dev-20261111` Harbor tags match deployed references. Go tests, generated/format/vet
checks, 30 administrator UI tests, native PostgreSQL session/CSRF/isolation/audit/
sign-in-serialization checks and candidate/deployed Chromium dark/WebKit light
browser checks pass. The browser matrix includes hidden sessionless/expired/
invitation actions, refresh races, live expiry, failure feedback, focus return
and desktop/tablet/narrow containment. Protected evidence is retained under
`deploy-internal/admin-revoke-sessions`.

Earlier follow-ups retain their own evidence:

- Helm 174 reconciles the separately rehearsed lifecycle/source-check fold into
  canonical 001-032. Function catalogs match fresh source; structural catalogs
  match the unchanged live before-image. Original Goose 0-32 rows/timestamps,
  sequence high-water, relation identities and private application data survive.
- Helm 175 deploys the requested account-deletion confirmation copy.
- Helm 176 defaults Manual sync only to the sole Connected source, independently
  of automated sync. Multiple Connected sources start unselected; manual choices
  survive refresh/tab return. All 1,007 UI tests at that checkpoint pass.
- Helm 177 adds bounded acceptance-lock retries and keyboard-accessible clickable
  user rows for usage. Investigation confirms two three-second PostgreSQL account
  lock timeouts, not stale admin authentication. Read-only verification of the
  completed live @foo purge finds zero rows across 79 account-scoped tables,
  no identity/session/reset/invitation/staging residuals and empty private roots;
  the retained task reports 166,537 purged rows, 53 workouts, 24 routes and one source.
- Helm 178 removes the separate Account deletions navigation/placeholder. Old
  deletion-category links redirect to Users; deletion/progress remain row actions.

Richer lifecycle history/detail remains pending. Administrator-triggered password
recovery was dropped on 11/17/2026; self-service Forgot password is implemented.
At this historical checkpoint OSM Maps and Operations were navigation placeholders;
later Helm181 adds Regions catalog refresh, not the remaining M10 workflows.

## Account Deletion And In-Place Users Refresh

**Deployed 11/17/2026, Helm 173.** The owner's complete private-purge/fresh-
registration expectations are accepted as the implementation goal. **Delete user**
now opens the shared irreversible confirmation for the selected username, focusing
Cancel first and guarding duplicate/late submissions. Acceptance revokes sessions,
sets the account Deleting, blocks API work admitted just before that transition,
cancels queued work and requests cooperative cancellation of running work.

Users immediately retains the accepted row as yellow **Deleting**. Active polling
uses configured cadence and accelerates to at most **10 seconds** while deleting.
Compatible pending refreshes retain existing row nodes and sort/scroll; authoritative
completion removes only the target row. Inactive views pause. **View deletion**
shows sanitized phase/counts and retry-needed state; Retry nudges the same task.
Closing progress never cancels an accepted purge, and reactivation is rejected.

The admin-owned task survives all owner-job deletion. A checked dependency-ordered
inventory covers **78 purge relations**, including the new private staging receipt
scope and indirect retry attempts. Batches remove at most 5000 ordinary rows, retain
monotonic progress across interruption and verify no account rows remain before
releasing identity. Sources/credentials/tombstones, source inventory/checksums,
workouts/raw points/routes, provenance, Coverage/display/diagnostics/tiles/LOD,
private jobs/logs/events/progress/retries/candidates, schedules/notifications/
preferences/report/map state and capabilities are purged. Identity/session/reset/
invitation state is removed; old username/email can register new UUIDs and empty
activity/defaults. Only sanitized admin task/audit IDs, counts and timestamps remain.
The API no longer retains identity-keyed avatar memory cache entries.

Foreign keys remain enforced. An unforgeable backend/transaction capability gates
the precise purge-only trigger path; normal guards stay pinned outside it. API
write triggers serialize with lifecycle state. Worker receipts are registered before
execution and retained through private cleanup; terminal jobs alone are insufficient.
Same-pod/root startup scavenging or verified disposal/replacement of the exact
chart-owned emptyDir UID acknowledges abandoned receipts. Live/unverified roots
block purge. The worker's namespace-only Role permits **get pods**, not Secret
reads or pod mutation; live authorization checks confirm that restriction.

the earlier account-deletion addition raises schema/minimum runtime to **34/34**, retaining baseline **27** and every
original **0-33** Goose row/timestamp before appending 34. Old writers were drained
before the fence. Up passed under the restricted migration login on a complete
SHA-256-verified restored live backup. An isolated purge of the restored @foo's
**35 workouts / 15 routes** verified every private scope zero and unchanged other-
account counts. **Live @foo remains active and untouched** for the owner's test.
External Local/iCloud export archives, public OSM, backups/WAL and physical lost-
volume media remain within their documented operator/infrastructure boundaries.

Verification: Go suite, generated/format/vet and chart checks pass; all **1000 UI
tests** pass. Native synthetic cases exercise active ingest and staging drain,
coalescing/authorization/CSRF, no re-enable, injected failure with committed-batch
resume, zero scoped leftovers and same-name/email re-registration with fresh IDs.
Disable/enable and exact readiness/grant regressions pass. Candidate/deployed
Chromium dark/WebKit light verify confirmation, warning color, real accelerated
poll timing, stable in-place nodes, sanitized progress and authoritative target-only
removal at desktop/iPad sizes. Browser private APIs are synthetic. All five
deployments are healthy; binaries and both origins' asset hashes match the reviewed
artifacts; Martin, 18 configuration tuples and canonical tags are preserved.
Protected migration, backup/restore/purge and matching rollback receipts remain
private; deployment does not execute a live deletion test for the user.

## Account And Source Defaults

**Deployed 11/17/2026, Helm 172.** New local/iCloud sources start with automated
sync **unchecked/false**; existing stored true/false values are retained in Edit.
Explicit opt-in still submits true. The Data Sources table shortens only the
pending label to **Connecting**; Connected/Connection failed and API enum values
retain their prior meaning.

Registration explicitly creates new personal preferences with columns **Date,
Type, Duration, Distance, Calories** in that order. The first UI initialization
persists that server-provided selection. Existing preferences are untouched.
This is an application creation default, not a schema/history rewrite: the
historical DDL default remains historical, and no migration/backfill runs.

API tests and the native PostgreSQL account lifecycle pass, including concurrent
registration, the five-column uninitialized defaults, initialization and later
custom preferences. Source/App checks pass (119 focused cases, with the corrected
async header assertion rerun independently), as do build, generated, formatting
and vet checks. Candidate/deployed Chromium dark/WebKit light use synthetic APIs
to verify exact initialization payload/header order, default-false sources for
both types, explicit enable, saved flags and the Connecting status, retaining the
prior field/modal/CRUD/focus matrix. Both origins serve verified assets and all
five deployments are healthy. Only API/UI roll; both workers, Martin, all 18
configuration tuples, canonical image tags and the exact 33/32 database metadata/
ledger are preserved. No real owner account/source was created for acceptance.

## Data Sources Field Hints And Automation

**Deployed 11/17/2026, Helm 171.** Path spans the full form for both types and both
New/Edit modes. Server Path's styled tooltip has two exact lines: **Path in ingest
worker container containing** / **HealthAutoExport-YYYY-MM-DD.json files**.
Password joins the existing fixed-width configuration font; entered text remains
at its prior **16px** size. Retention placeholders alone are proportional italic
and read **existing value retained unless provided**, fitting half-width Apple ID
and Password. A scoped real italic face is bundled without changing other text.

Apple ID/Password use Rclone-specific field names, form autocomplete suppression,
Password's `new-password` token and 1Password/LastPass/Bitwarden ignore hints. Browser
extensions can override these hints; no hidden decoys or focus/read-only tricks
are introduced. Stored credentials remain write-only and blank edits retain them.

The last field is **Enable automated sync (<interval>)**, after Path for Server
Path or Trust token for iCloud. It reuses the Data Sync Automated card's cadence
formatter and schedule response, lowercased. New sources default enabled; Edit
loads the stored flag and POST/PATCH send the actual checked value. Canceling
discards a changed checkbox. Once-a-day/twice-a-day labels and disabling new local/
iCloud sources or enabling an existing source are verified.

All **252 focused Source/Data Sync/formatter tests**, formatting and build pass.
Candidate/deployed Chromium dark/WebKit light verify full-width local/iCloud paths,
the two-line tooltip, autofill attributes, real italic placeholder loading/fit,
unchanged entered font size, checkbox label/order/payload and cancellation at four
desktop/iPad sizes, retaining existing CRUD/check/retry, dimming and focus checks.
Browser private APIs are synthetic. Both public origins serve all verified assets,
including the scoped italic font. All five deployments are healthy; only UI rolls,
preserving four other pods, 18 configuration tuples, canonical tags and schema/
minimum runtime 33/32. Earlier full-suite receipts retain their original scope.

## Data Sources Dialog Polish

**Deployed 11/17/2026, Helm 170.** Data Sources now has an explicit, isolated
content-size exception: empty/one/two-source views have only standard bottom
insets, while Add/Edit grows to its visible fields. The viewport cap retains
fixed header/actions and body scrolling. Preferences keeps its unchanged fixed
height, and all other dialog sizing contracts remain unchanged.

The iCloud editor has full-width Path, paired Apple ID/Password, a full-width
three-visible-line Cookies textarea and full-width single-line Trust token.
The final control/body and footer actions have matching **20px** divider insets
without reserved blank space. Field help uses the shared styled cursor-follow
tooltip, with no native `title` attributes. Write-only retention, limits and
opaque credential submission are retained.

Delete source uses a transparent nested overlay so the already-dimmed base page
does not darken again. Its exact prompt is **Delete <source-name>? Its connection
credentials will be deleted and its active sync jobs canceled. Imported workouts
will be retained.** Name loses 5% of its prior width and Auto Sync receives it:
**31.73% / 32.4% / 19.67% / 16.2%**, with Type/Status/actions otherwise unchanged.

All **116 focused App/source tests**, production build and formatting pass.
Candidate/deployed Chromium dark/WebKit light checks verify every field tooltip,
paired/full-width geometry, actual three-line textarea content height, equal
divider gaps, one-layer dimming, exact copy, empty/one/two-source and editor heights,
viewport scrolling, unchanged Preferences height, and create/check/fail/retry/delete/
CSRF/focus behavior at desktop/iPad sizes. Browser private APIs are synthetic.
Both public origins serve the verified hashes. All five deployments are healthy;
only UI rolls, preserving four other pods, 18 configuration tuples, canonical tags
and schema/minimum runtime 33/32. Prior full-suite receipts retain their scope.

## Data Sources Column Widths

**Deployed 11/17/2026, Helm 169.** Type, Auto Sync and Status each lose 10% of
their prior relative width, transferred to Name: **33.4% / 32.4% / 18% / 16.2%**,
with the fixed actions column retained. Standard default source names now fit
without ellipsis at the reviewed desktop size. Nine source tests, formatting and
build pass; candidate/deployed Chromium dark/WebKit light confirm the allocation,
readable Auto Sync headers, overflow behavior and source workflows at four sizes.
Only UI rolls; four other pods, 18 configuration tuples, schema/minimum runtime
33/32 and canonical tags remain preserved. Both origins serve the reviewed assets
and all five deployments are healthy. The 994-test full-suite receipt retains its
original scope.

## Owner Data Sources

**Deployed 11/16/2026, Helm 167-168.** Normal users have **Data Sources** directly
below Preferences in the account menu. The Preferences-style dialog retains compact
insets, tall containment, a fixed header/action footer and a scrolling body. Its
unpaged **Sources** table reuses admin Users styling, sort headers, ellipsis menus
and empty feedback, with Name, Type, Auto Sync and Status columns. Add/Edit shows
the conditional typed editor; Delete names the source in a compact confirmation
and preserves imported workouts through the existing tombstone/cancellation API.

Type labels/default names, field limits, monospace fields and styled tooltips match
the owner contract. Type locks after persistence. Apple ID/password/cookies/trust
token are never loaded from stored credentials. Edit placeholders read exactly
**the existing value is retained unless a new value is provided**; empty fields
are omitted from PATCH, retaining secrets and optional provider client identity.
Accepted credentials are cleared from draft state. New sources enable Auto Sync;
edits preserve its stored flag. Cancel clears/hides the editor.

Save requests a fresh generation-specific check, waits at most 30 seconds and
closes the editor only on that generation's success. Failures show **Connection
check failed** and safe actionable copy. A persisted new source retries with PATCH,
not a duplicate POST. Explicit `checkConnection=true` rechecks unchanged config;
ordinary metadata-only REST updates retain their previous no-check behavior.
Failed checks retain the submitted server configuration, as the source API already
specifies. Session/identity-scoped caches and admission/late-response guards prevent
stale work from restoring cleared or replacement-owner state, even under the same
advertised session ID. Portaled source row menus use the shared dialog-control layer.

The worker supervises an independent check-only serial runner with its own sibling
private staging root. A 25-second operation deadline produces safe terminal failure
and ordinary snapshot cleanup; an active ingest does not occupy this lane. Additive
`033_source_check_lane.sql` installs a restricted versioned claim helper. Source/
schema advance to **33**, minimum runtime stays **32**, baseline stays **27**, and
Goose appends only **33**. The original **0-32** rows/timestamps and every existing
function remain unchanged. This is distinct from the earlier account-access fold
into 002. A complete SHA-256-verified live backup was restored and migrated with
the exact deployed binary under the restricted migration login before live Up
through the chart-derived Job. The live migration and four-image rollout passed.

Verification: **994 UI tests**, production build, formatting, generated and vet
checks pass; the Go suite passes. Native PostgreSQL verifies local/iCloud source
lifecycle, explicit rechecks without resetting Auto Sync, private claim privileges,
readiness/overgrant rejection, checks while an ingest is running, deadline failure,
cancellation/lease/generation fencing and snapshot cleanup. Chromium dark/WebKit
light verify candidate/deployed empty/add/edit/fail/retry/delete workflows, exact
placeholders, tooltips, safe error copy, owner CSRF and focus at desktop/iPad sizes,
including full Auto Sync headings and contained horizontal table scrolling.
Browser private APIs are synthetic; no real owner source or Apple session was
created/edited/deleted for acceptance. Both public origins serve the verified UI
hashes. All five deployments are healthy; Martin and configuration are preserved.
Helm 168 adds the final principal-aware cache fence without changing API/worker
pods or the 033 database contract. Protected backup, migration, preimage, browser
and rollback receipts remain private.

## Preferences Diagnostics Help

**Deployed 11/16/2026, Helm 166.** The Diagnostics checkbox keeps its ordinary
field width while its explanation spans both grid columns with the normal field
hint gap: **Show the matcher review tool for the focused route instead of aggregate
coverage.** All 107 App tests, formatting and build pass. Candidate/deployed
Chromium dark/WebKit light checks verify the text, full-width hint, narrower
clickable frame and keyboard interaction at four desktop/tablet sizes. Both
origins serve verified assets; all five deployments are healthy. Four non-UI
pods, 18 configuration tuples, canonical tags and schema/minimum runtime 32/32
remain preserved.

## Preferences Diagnostics Width

**Deployed 11/16/2026, Helm 165.** Enable coverage diagnostics uses one ordinary
settings-grid column, matching Username and Full name instead of spanning both
columns. The label's complete padded frame remains clickable and keyboard Space
toggles its checkbox. Existing responsive grid, tall dialog and compact spacing
remain intact. All 107 App tests, formatting and production build pass. Candidate
and deployed Chromium dark/WebKit light checks compare the three field widths
and exercise frame-edge clicks, keyboard toggling and Save/Cancel at four
desktop/tablet sizes. Both public origins serve the verified assets; all five
deployments are healthy. Four non-UI pods, 18 configuration tuples, canonical
tags and schema/minimum runtime 32/32 remain preserved.

## Preferences Dialog Height

**Deployed 11/16/2026, Helm 164.** Preferences uses the exact requested
`height: min(64.5rem, calc(100dvh - 1rem))`, retaining shared compact insets and
the fixed header/footer. Production build and formatting pass. Candidate/deployed
Chromium dark and WebKit light verify the computed height at 1440x1200, 1440x1000,
1024x768 and 768x1024, including the tall-screen cap, viewport containment, body
scrolling where needed, 20px insets and Save/Cancel/close/Escape behavior. Browser
APIs are synthetic. Both public origins serve the verified UI asset hashes; all
five deployments are healthy. Four other pods, configuration tuples, canonical
tags and schema/minimum runtime 32/32 remain intact. Prior test-suite receipts
retain their original scope.

## Preferences Dialog Spacing

**Deployed 11/16/2026, Helm 163.** User Preferences now reuses `compact-dialog`
and its shared `--dialog-inset`: the header, scrolling body and Cancel/Save footer
have the same **20px** insets as Custom date range and the established confirmation
dialogs. Footer button gaps also reuse that inset; button widths remain unchanged.
The redundant **Set how your routes, dates, and profile appear.** subheading is
removed, with the dialog title retained and no dangling description reference.
The fixed header/footer, independently scrolling body, safe-area footer treatment,
saved preferences and content-focus return remain intact.

All 107 App tests, Prettier and the production build pass. Candidate and deployed
Chromium dark/WebKit light checks compare computed Preferences spacing with a
real Custom date range dialog at **1440x1000, 1024x768 and 768x1024**. Header/body
padding, footer insets and button gap match; body scrolling leaves header/actions
fixed, content fits horizontally, and Save/Cancel/close/Escape retain state, CSRF
and focus behavior. Review uses synthetic private APIs. Both public origins serve
the verified UI asset hashes. All five deployments are healthy; four other pods,
18 configuration tuples, canonical tags and schema/minimum runtime 32/32 are preserved.

## Enable User Dialog Copy

**Deployed 11/16/2026, Helm 162**, correcting the first-word typo from Helm 161.
The Enable user confirmation uses the owner's corrected wording, with the selected
username interpolated in bold:

> Enable @user? The user will be able to sign in again and automatic sync will
> resume on schedule. Revoked sessions and canceled jobs will not be restored.

All 15 administrator component tests, Prettier and the production build pass.
Candidate and deployed Chromium dark/WebKit light review verifies the exact text,
desktop/tablet containment, confirmation focus and existing command behavior with
synthetic private APIs. Both public origins serve the verified UI assets. Four
other pods, all 18 configuration tuples, canonical tags and schema/minimum runtime
32/32 are preserved; all five deployments remain healthy.

## Shared Error Copy

**Deployed 11/16/2026, Helm 160.** The shared `ErrorSummary` heading now reads
**Well, this is a dead end... 🙁** across sign-in, registration, recovery/reset and
Preferences. The accepted shorter messages are:

- **Sign-in failed. Check your details and try again.**
- **Create account failed. Review the fields and try again.**
- **Request failed. Please try again later.**
- **Choose one of the available page sizes.**
- **Choose how many date ranges to remember (1-5).**

The credential-verified **Your account has been disabled.** detail and existing
validation, safe-error mapping and alert markup remain intact. This is a copy-only
UI rollout. All 107 App tests, Prettier checks and the production build pass.
Candidate and deployed assets pass targeted Chromium dark/WebKit light review of
sign-in, disabled-account, recovery and registration summaries at desktop/tablet
sizes, using synthetic API responses. Both public origins serve the verified asset
hashes. Four non-UI pods, all 18 configuration tuples, canonical image tags and
schema/minimum runtime 32/32 are preserved; all five deployments are healthy.

## Admin Account Disable And Enable

**Deployed 11/16/2026, Helm 159.** Accounts / Users now implements **Disable user**
and **Enable user** through the shared compact confirmation composition. Both
name the selected username and focus Cancel first. Requests use administrator
CSRF, serialize submission, retain failures in place and preserve selected identity
and focus across refreshes. Deleting accounts cannot be enabled.

Disable atomically denies authentication, revokes cookie/bearer sessions,
invalidates outstanding scheduler admission and cancels queued account jobs or
requests cooperative cancellation of running work. Workers retain their ordinary
cleanup/terminalization duties. Workouts, routes, source configuration/credentials,
preferences and valid Coverage remain retained. Enable requires a new sign-in and
resumes normal next-due scheduling without reviving sessions or canceled jobs,
regardless of unused automatic-retry allowance. The UI uses existing API-401
confirmation/redirect handling, without adding session polling.

Only verified owner credentials disclose suspension. The login summary below
Username/Password says **Your account has been disabled.**; wrong credentials
remain generic. Sign-in and suspension serialize at session issuance.

The owner explicitly requested folding the proposed 033 into
`002_account_lifecycle.sql`. The active sequence remains **001-032**, runtime and
minimum **32/32**, baseline **27**, with the exact **0-32** ledger unchanged.
The private guarded upgrade was rehearsed on a complete restored live backup
under `workouts_migration`; helper bodies/ownership/ACLs match fresh source and
existing function definitions, data counts/relations and principal hashes remain
unchanged. A truncated exec-stream backup was rejected before use; the accepted
archive matches the completed remote file's SHA-256 and passes a full read/restore.
Recovery evidence and the protected backup remain private. Ordinary Goose does
not replay 002; this is the explicitly approved development-only consolidation.

Verification: Go suite, generated/format/vet checks and UI production build pass;
all **985 UI tests** pass. Native restricted-role cases cover cookie/bearer
revocation, authorization/CSRF, incorrect versus correct disabled credentials,
queued cancellation, running ingest/Coverage cancellation completion, retained
data, reactivation without revival, deletion conflict and sign-in serialization.
The native administrator-password regression matrix also passes with its race
injections before the new issuance fence. Candidate and deployed assets pass
Chromium dark/WebKit light review at desktop/iPad-class sizes, using synthetic
private API responses. Both public origins serve the verified asset hashes; all
five deployments are healthy. No real owner account was disabled for acceptance.
At this historical checkpoint, full account purge, standalone session controls,
OSM and Operations acceptance remained M10 work. Later checkpoints deliver purge
and session revocation; admin-triggered recovery is dropped in favor of self-service.

## Admin Disabled Actions And Usage Dialog

**11/15/2026, Helm 158:** Delete user remains non-operative and disabled; its
danger class no longer overrides the shared gray disabled treatment. Disabled
menu items retain pointer hit-testing and show the blocked circle-slash cursor,
while Radix prevents selection. Shared unavailable filter/select/input/slider
controls use that same cursor. Enabled danger actions retain their red treatment.

The usage title is **2rem**. Its close button now exactly reuses the Provenance
dialog's icon-button and multiplication-sign glyph, replacing the baseline-offset
SVG. Escape, the X and return focus keep their existing behavior.

**176 focused UI tests**, production build and UI artifact checks pass. Chromium
dark and WebKit light candidate/deployed-assets checks verify gray colors,
not-allowed cursor, actual hit-testing and no-op clicks, shared disabled controls,
32px title, identical Provenance close-button styles and desktop/iPad containment.
Only UI rolls; all five deployments are healthy, four other pods and all 18
configuration tuples are preserved. Source/schema/minimum runtime stays
**32/32/32**, both strict-TLS origins serve reviewed assets and canonical
**dev-20261111** tags match pins. Protected receipts retain the evidence.

## Admin Invitations Dates And Usage

**11/15/2026, Helm 155-157:** Users now uses **80%** of Workout log width and a
sortable **Registered** column between Email and Status, formatted without time in
the browser's local time zone. **View usage** is first for registered users and
opens an X-only compact dialog showing local last login, workout/route counts and
latest local workout date. It is an explicitly authorized bounded projection,
not private workout/route/source access or owner-capability issuance.

Invite title and error gaps are equal above/below. Successful sending closes the
dialog without a message. Real Resend rotates the token, grants a fresh full
seven-day period and sends email; normal-style Revoke removes the row, invalidates
tokens and permits a later new invitation. Revoke has no danger styling/divider;
registered Delete remains a danger item below a divider, disabled pending its
backend. The placeholder menu note is removed.

The SMTP path already existed; two recent sends failed because Mailpit kept
serving a certificate that expired November 8 despite its renewed Secret. A
Mailpit restart loaded the renewed certificate; verification remained strict and
no SMTP/infrastructure setting was weakened or changed through the app.

Forward migration **032** adds immutable-registration read access, persistent
last-login tracking, narrow invitation updates and the active-admin-fenced usage
function. It restores logical tenant/session scope on success/error, preserves
RLS, and exposes no private rows. The ingest phase writer's literal runtime fence
advances to 32; the existing 001-031 history is unchanged. Source/schema/minimum
runtime is **32/32/32**, baseline **27**, ledger **0-32**.

Running work drained before the API/general/Coverage quiet window. A protected
**1,642,617,013-byte** custom-format database backup was created and its TOC/hash
verified before migration. The migration ran under the configured migration role;
private-table counts and relation identities plus principal credential/identity
hashes matched afterward. The temporary migration Job was removed. API, both
worker processes and UI rolled at 155; Martin and all 18 app configuration tuples
were preserved. All five deployments are healthy.

Live checks caught an empty-body Resend POST rejected by JSON middleware. Helm
**156** supplies/requires `{}` and changes API/UI only, preserving both workers and
Martin. The final real SMTP flow acknowledged create, resend and re-invite, with
durable delivered states, fresh expiry, old-token revocation and row removal
verified; owned verification invitations and sessions were revoked. Mailpit inbox
contents were not read through its separately authenticated UI/API.

Helm **157** is an API-only correction: successful SMTP DATA acknowledgement
remains success if QUIT subsequently fails, or if recording that accepted delivery
fails. The latter logs a sanitized persistence error instead of falsely telling
the user that email was unsent. Dedicated transport/recording regression tests
pass; live create/resend/re-invite acknowledges three real SMTP messages again.
Both workers, UI and Martin remain on the same pods at this follow-up.

Verification includes the **980-test UI baseline**, Go suite, native32 lifecycle/
readiness/privacy suites and final focused resend/JSON retests. The final focused
UI retest disables Node 26's conflicting experimental Web Storage implementation;
this is a harness setting, not a product change. Chromium dark/WebKit light pass
candidate and deployed-assets checks at desktop/iPad sizes, including midnight
localization, exact 1152px desktop width, title/error gaps, usage fields and normal
revoke menus. Actual strict-TLS API usage/privacy and SMTP acknowledgements also
pass. Backup/native/browser/image/deployment receipts remain protected. Full M10
account disable/delete, administration details and OSM/Operations remain pending.

## Admin Users Review

**11/15/2026, Helm 154:** admin tabs now support shared guarded **1/2/3/4**
shortcuts and focus handoff. Sign out uses scoped compact padding and pale-red
danger hover, retains its label/appearance while pending, and prevents duplicate
requests without the previous Signing out flash.

Accounts / Users reuses Workout log at **60% width** (864px on the reviewed
desktop), with its uppercase heading, right-aligned Invite a user link, sortable
headers, 53px rows, shared hover/truncation help and ellipsis menus. No page-level
heading pair, search, pager or separate Invitations category remains. Old category
links resolve to Users. The compact invitation dialog validates email, has a
non-bold prompt and Cancel/Send, and immediately inserts a successful persisted
**Invited** row. Bold status treatments use green Active, yellow Invited and red
disabled/deletion/expiry/revocation states. Lifecycle row commands are visibly
unavailable until their remaining M10 backend work is complete.

The new bounded unpaged directory endpoint combines identity/lifecycle data with
unaccepted invitation metadata in one statement. Registration replaces the
invitation without duplicating the email; expired/revoked invitations are correctly
classified. It excludes token/verifier/private data and fails above its bound rather
than truncating. Existing paginated user APIs remain available for automation.

**978 UI tests**, API/native restricted-role directory/registration/privacy tests,
typecheck/build/CSP/CSS/generated/format checks pass. Chromium dark and WebKit light
verify candidate and deployed assets at desktop/iPad sizes, including exact width,
row height, shortcuts and guards, dialog validation/focus, persistent fixture rows,
status colors and stable pending sign-out. Actual strict-TLS admin directory reads
and owner-profile denial also pass; the verification session was revoked.

Only API/UI roll, preserving three other pods and all 18 configuration tuples.
All five deployments are healthy; schema/minimum runtime stays **31/31**. Both
origins serve reviewed assets and canonical **dev-20261111** tags match pins.
Owned native test resources were cleaned and the rehearsal cluster stopped.
Protected browser/source/image/deployment receipts retain the evidence. Full M10
remains in progress; see [feature status](admin-ui-spec.md#implementation-status).

## Milestone 10 Foundation

**11/15/2026, Helm 153:** first usable admin slice deployed. Existing login routes
the reserved **admin** identity to **/admin**; separate cookies, CSRF/session
checks, expiry events and query clients isolate it from the owner explorer.
The four tabs and Stats-style category navigation work with deep links and retained
selection. Initial workflows are a searchable/paginated user identity directory,
invitation sending and sanitized read-only effective configuration. Other categories
say they are planned; M10 is in progress, not complete.

The live bootstrap administrator had a different username. An explicit short-lived
bootstrap adoption Job verified existing credentials, preserved principal ID,
password hash and email, revoked old admin sessions and recorded audit. Only the
bootstrap Secret's username changed to **admin**; its UID/password/email were
preserved. A first attempt rejected a projected symlink before any mutation; the
successful execution copied it to a private regular file. After DB adoption, a
failed stdin-file Secret patch was reconciled by verifying the preserved identity
and applying a resource-version-guarded file patch. The owned Job was removed.

Only API/UI image references changed in the subsequent rollout, preserving all
18 post-transition configuration tuples and the three worker/Coverage/Martin pods.
All five deployments are healthy; schema/minimum runtime remains **31/31** with
no new migration. API's bootstrap executable is updated alongside its runtime;
migration/role-provisioning binaries and unrelated images remain unchanged.

Verification: **974 UI tests**, build/typecheck/CSP/CSS/generated/format checks and
native restricted-role M10, existing account lifecycle and development debug-auth
regression suites pass. Owned native DB/proxy resources were removed and the
local rehearsal cluster stopped. Chromium dark and WebKit light verify candidate
and deployed assets at desktop/iPad sizes with synthetic private APIs. Separately,
actual strict-TLS bootstrap login, admin session/directory/config reads, owner
profile denial and verification-session revocation pass. Both public origins serve
the reviewed assets. Protected receipts retain source/image/browser/identity and
rollout proofs; no private identity data is recorded here.

Next: invitation history/resend/revoke and account controls, then durable purge,
the OSM controller/lifecycle and Operations; remaining Settings preferences/access
controls follow the [delivery plan](implementation-plan.md#milestone-10-administration-and-operational-completion).
See [feature status](admin-ui-spec.md#implementation-status) for precise boundaries.

## Milestone 10 Admin UI Decision

**11/15/2026:** accepted a dedicated **/admin** workspace after the account, OSM
and operational task inventory outgrew the original Swagger/CLI-only assumption.
The [admin UI specification](admin-ui-spec.md) now owns detailed requirements and
the [M10 implementation plan](implementation-plan.md#milestone-10-administration-and-operational-completion)
defines delivery and acceptance. This design-only checkpoint preceded the
[foundation rollout](#milestone-10-foundation); Helm 152 was the runtime status
when the design was accepted. It does not claim the remaining M10 work complete.

- Four tabs: **Accounts, OSM Maps, Operations, Settings**. Each uses Stats-style
  left navigation for second-level categories and existing shared UI behavior.
- Reuse the existing login screen with reserved canonical **admin** and the
  bootstrap administrator's current password. Define audited existing-identity
  transition, independent owner/admin sessions and strict private-data denial.
- Accounts includes invitation/user lifecycle, suspension/reactivation, session
  revocation/recovery email and durable cancellation/drain/purge workflows.
- OSM Maps reuses the existing updater and checkpoints; a separate internal
  controller launches approved Kubernetes Jobs under bounded RBAC/template rules.
- Operations includes system tasks, incidents, announcements, audit and recovery.
  Settings contains admin preferences/access and read-only effective configuration;
  Helm/GitOps/Secrets have no competing UI editor or override layer.
- Initial base-map/MapTiler and other Helm installation guidance remains M13.
  Owner **Data Sources** is planned in the normal profile menu and separately
  scheduled; it grants no administrator access to private sources or credentials.

## Placement-Aware Row Menus

**Helm 152** keeps shared row-menu translation at **-1px/5px** below the trigger
and uses **-1px/-5px** when Radix collision handling places it above. Matching
top/bottom clearance prevents the visual shift from clipping the viewport edge.
Chromium dark and WebKit light verify both placements in Summary and Task history,
roughly 11px trigger/menu gaps within fractional-pixel rounding, unchanged horizontal
alignment, viewport containment and normal dismissal. Build and CSS/CSP checks
pass; the existing 967-test baseline is not a newly rerun full-suite claim.

Only UI rolls. All five deployments are healthy, four other pods and all 18
configuration tuples are preserved, both strict-TLS origins serve reviewed assets,
and canonical **dev-20261111** tags match deployed pins. Protected receipts retain
the four-placement matrix and build/deployment proofs.

## Smaller Stars And Global Surface Focus Policy

**Helm 151** fixes monthly calendar-award stars at **22px**, recalculating equal
top/left/right margins from each bar's actual width (10px in the current 42px bar).
The app-wide keyboard focus rule now targets native/ARIA controls and intentional
interactive row/value targets rather than every focusable node. Large panel/card,
section/content and dialog containers keep real programmatic focus without a huge
outline. The standalone Run detail outline rule is removed, and the global
container/control distinction is documented in the UI style guide.

**246 focused tests**, type/build and CSS/CSP checks pass. Chromium dark and
WebKit light verify exact star geometry, context-menu View detail landing with
actual focus/no panel outline, preserved 3px keyboard rings on a child control,
and no outlines on unclassified large native/ARIA container types. This is a
focused follow-up to the 967-test full-suite baseline, not a new full-suite claim.

Only UI rolls. All five deployments are healthy, four other pods and all 18
configuration tuples are unchanged, both strict-TLS origins serve reviewed assets,
and canonical **dev-20261111** tags match deployed pins. API/schema and job/chart
data are unchanged. Protected receipts retain geometry/focus and rollout proofs.

## Stable Job Details And Linked Charts

**Helm 150** prepares fresh authoritative history-menu choices before revealing
the popup; stale cached commands and transient loading menus never appear first.
Pending preparation is cancellable and a completed old read cannot reopen it.
Prepared choices remain stable for that opening, with existing command/CSRF rules.

Run detail retains its accepted full-size content while another target loads,
including stale-cache handoffs. The old card is inert, then updates in place after
a fresh complete response. Old artifacts pause during cross-run handoffs, while
same-job poll errors preserve manual-only Logs behavior. A selection intent lands
the rendered detail at its content owner's top plus the panel gap when its top is
not already visible, with native clamping on short content and no poll/tab hijack.

Consistency keeps variable-width Y axes/grid origins, but compensates bar/date
tracks to a common origin. A shared native-scroll coordinator locks every chart's
period columns during dragging, wheel/programmatic and keyboard movement, without
per-pixel report state. Grouping resets to the latest period; hidden sections
rejoin the current offset. Maxima titles now use **per workout per week/month**.
Monthly active days alone awards a calendar-perfect month with a gold star using
two-thirds bar width and equal one-sixth top/side margins, including leap/century
calendar rules. Current partial and weekly periods are not awarded.

All **967 UI tests** pass, including prepared-menu, retained-card, shared-scroll,
axis-padding and calendar-award regressions, plus existing artifact polling and
request isolation. Type/build, CSS/CSP and formatting checks pass. Chromium dark
and WebKit light measure aligned bar/label X positions and scroll offsets despite
different axis widths, then verify shared drag/scroll, narrow layouts and 28px stars
inside 42px bars with 7px margins. Deferred 20-to-60-to-5-route selections retain
their old panel height until admitted, then land at the expected scroller offset.
Menu preparation is held deliberately to prove no popup appears prematurely.

Only UI rolls. All five deployments are healthy, four other pods and all 18
configuration tuples are unchanged, both strict-TLS origins serve reviewed assets,
and canonical **dev-20261111** tags match deployed pins. API/schema and workout
data are unchanged. Protected receipts retain deferred-load, alignment/star,
continuity/landing and build/deployment proofs.

## Cursor Help, Current Periods And History Actions

**Helm 149** completes three owner refinements. Consistency opts into the shared
tooltip's pointer-following mode: standard initial delay, the Summary hint's
**0.65rem** offset, coalesced position updates, no auto-hide, and immediate context/
hover cleanup. Keyboard focus retains element anchoring. Summary's existing
two-second mode-hint dismissal is unchanged.

The Consistency API resolves range and cutoff from the same request instant,
then bounds source dates and calendar generation to today in the resolved time
zone. Current partial periods remain; future periods do not. The original shared
range remains in response metadata, entirely future ranges return no buckets, and
All time cannot bypass the date cutoff for future-dated workouts. No migration or
historical workout rewrite is required.

Task history rows/cards now support direct detail selection and keyboard entry.
Right-aligned ellipsis controls reuse Workout log sizing, offsets and focus rules;
table rows measure the same **53px**. A narrow action column redistributes saved
space across the other columns. Menus lazily query the chosen parent's details and
share Run detail's action policy, including disabled/suppressed retry and deletion
rules. Immutable command targets, CSRF, duplicate-command fencing, cache refresh,
and inactive/unmounted navigation guards protect asynchronous transitions. Menu
events cannot activate the enclosing row.

All **960 UI tests** pass, including eleven history-action cases, pointer-follow/
cleanup tests and the prior scope/scroll suite. Go tests/vet, generated bindings,
format/type/build and CSS/CSP checks pass. Disposable PostgreSQL verifies local-day
and month/week boundaries across time zones, original-range retention, future-only
results, and All time future-row exclusion; owned resources are cleaned afterward.
Chromium dark and WebKit light verify actual cursor tracking and persistence,
This year ending at the current week/month, exact row sizing, lazy command reads,
correct-job Cancel/Retry and CSRF, and responsive menu containment with synthetic
private APIs. No actual-account browser E2E or new capacity claim is made.

Only API/UI roll. All five deployments are healthy; three other pods and all 18
configuration tuples are unchanged. Both strict-TLS origins serve reviewed assets,
the API binary matches its frozen build, and canonical **dev-20261111** tags match
deployed pins. Source/schema/minimum runtime remain **31/31/31**, baseline **27**,
ledger **0-31**. Protected receipts retain native tests, pointer/period/row geometry,
mutations, frozen builds and rollout proofs.

## Fixed Navigation And Content Scrolling

**Helm 148** bounds the authenticated shell to the viewport. Header/navigation
stays in place while Summary and Data Sync scroll their retained workspaces.
Stats independently scrolls its report under fixed sidebar controls, with a fixed
background surface and capped stacked controls on narrow/short screens. Map follows
the available workspace row, retaining its renderer, camera and list behavior.

Scroll surfaces are explicitly owned. RetainedTab remembers active content and
sidebar positions independently; hidden events cannot overwrite them. The shared
capture/restore helper retargets Summary filtering/paging, Stats tables and Data
Sync history/artifacts from document coordinates to their nearest content owner,
retaining the standalone window fallback. Partial-page anchor arithmetic uses the
current owner offset, and disconnected owners cannot scroll another context.
Task history bottom padding is **20px**, matching the outer section gap while
retaining device safe-area accommodation.

All **947 UI tests** pass, including new owner-isolation/restoration tests and the
existing pagination, focus and resource-retention suite. Type/build and CSS/CSP
checks pass. Chromium dark and WebKit light exercise real MapLibre/provider styles
with synthetic private data: window scroll remains zero, the header/sidebar bounds
stay fixed, content scroll restores across tabs, Summary/history/Files paging stays
anchored, and the Map canvas fills its remaining viewport without camera changes.
Short and narrow layouts remain contained. Measured history bottom spacing is
20px within native fractional-scroll rounding, equal to its upper panel gap.

Only UI rolls. All five deployments are healthy; four unrelated pods and all 18
configuration tuples are unchanged. Both strict-TLS origins serve reviewed assets
and canonical **dev-20261111** tags match deployed pins. Protected receipts retain
document/content geometry, camera/scroll checks and build/deployment evidence.

## Tight Scales And Table Messages

**Helm 147** reduces the shared date-slot gap from 16px to **12px**, including
measurement/virtualization arithmetic. Active days keeps its calendar bounds but
shows only **2/4/6** weekly labels/lines and **7/14/21/28** monthly labels/lines;
unlabeled top lines at 7/8 or 31 are not drawn.

Other charts choose four equal intervals whose highest non-outlier value reaches
**75%-100%** of the labeled maximum. Coarse steps and the former extra padding no
longer inflate headroom. Round numeric/time steps are preferred only within that
bound; finer second/sub-unit labels prevent zero or duplicate tick text for small
values. Outlier screening/caps and exact zero baselines remain intact. Parameterized
tests cover small, large, fractional and boundary peaks across all six metrics.

The shared centered-table-message token is **1.1rem**, 10% larger than the prior
dialog message font. Road Coverage and New visits loading/empty states inherit it
through the shared viewport, and report/loading-row messages use the same token.
This changes typography without changing frozen table capacity.

**76 focused tests**, type/build, CSS/CSP and formatting checks pass. Chromium dark
and WebKit light verify both period views: exact slot gaps, active-day lines,
quarter-position metric lines, normal peak heights, and shared zero baselines.
Actual New visits/report loading states and Road Coverage message-style probes
all compute **17.6px**. The 940-test full-suite baseline is not a new rerun claim.
Only UI rolls: all five services are healthy, four other pods and all 18
configuration tuples remain unchanged, both strict-TLS origins serve the reviewed
assets, and canonical **dev-20261111** tags match deployed pins. Protected receipts
retain the scale/geometry/font matrix and rollout evidence.

## Consistency Zero Baseline

**Helm 146** corrects a visual defect reported for small weekly Distance bars.
The generic `.tooltip` rule occurred later than the equally specific bar wrapper
rule and restored `inline-flex`. Short bars then participated in a text line box:
the prior live renderer reproduces a **10px** downward shift below the zero line.
Scoping the chart wrapper rule under its bar target restores block layout with
higher specificity; sums, values and scale calculations are unchanged.

Chromium dark and WebKit light measure the numeric zero line, target, wrapper and
painted fill for zero, tiny, ordinary, missing and capped values in all seven charts
and both period groupings. Every corrected bottom matches zero within subpixel
tolerance. The old failing baseline is retained with the corrected geometry in
protected review storage. Build and CSS/CSP checks pass; this CSS-only fix uses
real-renderer geometry rather than claiming a new component/full-suite run.

Only UI rolls. All five deployments are healthy, four other pods and all 18
configuration tuples are preserved, both strict-TLS origins serve the reviewed
assets, and canonical **dev-20261111** tags match deployed pins. API/schema and
workout data are unchanged.

## Consistency Chart Spacing

**Helm 145** removes the overflow-cap caption and all space reserved for it.
Chromium dark and WebKit light measure the chart-to-panel bottom inset against
the actual panel padding, matching the side padding in every section. The tooltip
separator becomes one heavy dash with a regular/nonbreaking space pair on each
side; raw accessible text verifies that spacing rather than collapsed rendering.
Top corners become **3px**, and the gradient starts at **70%** opacity, ending at
100%. Existing colors, caps, latest-period defaults, dragging and keyboard
exploration remain intact.

**14 focused tests**, type/build and CSS/CSP checks pass. Both browser/theme suites
verify geometry, separator text, computed gradient/radius and existing timeline
behavior, including narrow containment. The 940-test full-suite baseline is not a
new rerun claim. Only UI rolls; all five deployments are healthy, four other pods
and all 18 configuration tuples are preserved, both strict-TLS origins serve the
reviewed assets, and canonical **dev-20261111** tags match deployed pins. Protected
receipts retain computed styles, insets and build/deployment proofs.

## Consistency Chart Refinements

**Helm 144** implements the nine owner-requested chart refinements. Panel-title
dividers are removed and disclosure markers enlarged. Bars use **#bc067a**, rounded
top corners and a 75%-to-100% bottom/top opacity gradient; overflow caps use
**#d70d0d**. Week/month bars share a common measured date-label slot and width.
Weekly labels are unpadded **M/D/YY**; tooltip dates expand to unpadded four-digit
years or full month names, separated from values by the requested heavy rule.

Every chart initializes at its latest period and resets to the right edge when
accepted grouping changes. Font/viewport measurements finish before releasing
initial placement, including sections opened after being hidden. Later tab/report
returns preserve manual panning. Native scrollbars are hidden in both styling
families; a hand cursor and captured pointer drag scroll the timeline without
selecting text or revealing stale help. Cancellation/blur releases the gesture;
touch retains vertical scrolling/pinch zoom and keyboard Home/End/arrows remain.

**75 focused tests** pass, including new latest-position, grouping-reset,
drag/blur and retained-pan regressions. Type/build, CSS/CSP and formatting checks
pass. The prior 940-test full-suite baseline is not a new rerun claim. Candidate
Chromium dark and WebKit light verify all seven latest-position defaults, equal
42px week/month bars, no native scrollbar gutters, exact colors/gradient/radii,
hover text, drag behavior, scroll retention, overflow caps and narrow containment.

Only UI rolls. All five deployments are healthy; four other pods and all 18
configuration tuples are unchanged. Both strict-TLS origins serve reviewed assets
and canonical **dev-20261111** tags match deployed pins. API/schema and workout
data are unchanged; detailed browser/style/geometry and image/deploy receipts
remain in protected review storage.

## Consistency Report

**Helm 142** implements the post-M9 Consistency report and API. **Helm 143** keeps
browser/modifier/composition ownership in the period controls. Consistency is
first/default above New Coverage, with Week/Month grouping, guarded W/M shortcuts,
seven open-by-default disclosure panels and the shared open/close-all link style.
New Coverage retains M/Y, its state, paging and Map handoffs, and prepares a
Coverage scope only when explicitly selected.

The seven charts show active days, duration/distance/calorie totals and their
per-workout maxima. Sections share Summary's maximum content width and Data Sync
panel composition; chart height follows the responsive Range totals card token.
Fixed day axes and four nonzero metric ticks use Workout log formatting/units.
Robust typical-value scales show extreme overflow as red caps while keeping full
tooltip values. Chronological date labels, native horizontal scrolling, bounded
bar windows and roving keyboard exploration preserve long timelines without DOM
growth proportional to the full archive. Hidden tabs/reports pause reads/help;
scope fences, compatible continuity and Retry retain existing privacy/UX rules.

`GET /api/stats/consistency` uses one repeatable-read owner snapshot for exact
aggregates and zero-filled local-date calendar buckets. Partial edges are range
filtered before grouping; active days deduplicate same-date workouts. Missing
metrics are unavailable, empty totals zero, and undated All time workouts are
reported separately. It does not create map/Coverage capabilities or change
schema. Extreme response ranges receive bounded 422 errors rather than truncation.

Verification includes **940 passing UI tests**, Go tests/vet, generated bindings,
type/build, CSS/CSP and formatting checks. Fresh disposable PostgreSQL tests prove
exact sums/maxima, weekly/monthly boundaries, preferred weekday, DST-local dates,
partial/empty buckets, missing units, undated-workout treatment, owner isolation
and response limits. The owned database/proxy were removed and the cluster stopped.
Final keyboard refinements pass **39 targeted tests** against that full-suite
baseline. Candidate Chromium dark/WebKit light review verifies seven 1440px panels
and 204px chart viewports against 136px Summary cards, required ticks, bar widths,
red caps, delayed help, final-period scrolling, report/tab retention, preferred
units and narrow containment using synthetic private APIs. No actual-account E2E
or new capacity-certification claim is made.

All five deployments are healthy. The initial release rolls only API/UI and
preserves three unrelated pods; the keyboard follow-up rolls only UI and preserves
four. All 18 configuration tuples stay unchanged. Both strict-TLS origins serve
reviewed assets; the API binary matches its frozen build; canonical
**dev-20261111** tags match deployed pins with no review aliases. Source/schema/
minimum runtime remain **31/31/31**, baseline **27**, ledger **0-31**. Protected
receipts retain native tests, browser geometry, frozen builds and rollout proofs.

## Map Text And Symbol Priority

**Helm 141** updates the shared style-transform/live-reconciliation policy after
owner review: provider geometry, background paths, focused paths, directional
markers, provider text/symbols, endpoints, then the temporary blink (bottom-to-top).
Directions now directly follow focused geometry; existing provider symbol order,
label styling/visibility, path/marker opacity and endpoint/blink priority remain
intact. Reconciliation remains a no-op for an already-correct stack.

All **308 focused map tests** pass, including the revised diagnostic-transition
and shared ordering expectations. Type/build, Map CSS and CSP checks pass.
Chromium dark and WebKit light use real MapLibre/provider styles with synthetic
private data to verify all three configured families, Routes, focused production
Coverage, diagnostics, style reload, Space, endpoint order and topmost blink.
Each browser records nine layer snapshots; camera state remains unchanged across
style/mode transitions and narrow containment still passes. This does not claim
actual-account browser E2E or a new full UI-suite run.

Only UI rolls. All five deployments are healthy; four other pods and all 18
configuration tuples are preserved. Both strict-TLS origins serve the reviewed
assets and canonical **dev-20261111** tags match deployed pins. API/schema and
workout data are unchanged. Detailed renderer snapshots, source/image proofs and
deployment receipts remain in protected review storage.

## Disabled Dropdowns And Row Menu Refinements

**Helm 139** first moves the Workout log ellipsis popup down 3px, preserving
keyboard dismissal/focus return and viewport collision handling. **Helm 140**
adds another 2px down and 1px left: total translation is 5px down/1px left, with
corresponding edge clearance. Chromium dark and WebKit light verify both ordinary
and above-trigger placement, including the exact horizontal shift.

Helm 140 also shares disabled-chevron colors across range, account, preference
and native-select dropdown styles. Native, ARIA and headless-widget disabled states
mute their indicators and use a default cursor; enabled colors restore unchanged.
All six numeric Workout log filters now mute their minimum/maximum slider labels
alongside the existing disabled controls and current value. Both browser/theme
checks exercise disable/enable/disable, the three real dropdown trigger families,
and native select styling. Type/build, CSS/CSP, formatting and asset checks pass.
No new full-suite run is claimed for these focused visual refinements.

Both releases roll only UI. All five deployments are healthy, four other pods and
all 18 configuration tuples are preserved, and both strict-TLS origins serve the
reviewed assets. Canonical **dev-20261111** tags match deployed pins. Protected
receipts retain the state-color matrix, exact menu geometry and deployment proofs.

## Range Totals Refresh Fade

**Current: Helm 138**, after owner clarification. Range totals starts at **30%
opacity** and reaches full opacity over **500ms** using `steps(10, end)`: one
seven-percentage-point increase every **50ms**. Chromium dark and WebKit light
verify the declared duration/step function and quantized, monotonic intermediate
frame samples on a real tab return with a controlled synthetic Summary response.
Reduced motion still sets duration to zero. Build, CSS/CSP and deployed-asset
checks pass; this is a UI-only rollout with all five services healthy, four other
pods and all 18 configuration tuples preserved, and canonical tags matching pins.

Initially deployed **11/12/2026 at Helm 137** after owner review of the existing Summary
refresh effect. Its former 120ms transition from 58% opacity becomes **360ms with
ease-in-out timing from 88% opacity**, so the cards begin only slightly dimmed and
settle more gently. The opacity state is scoped to the aggregate grid. Existing
refresh ownership, retained-tab behavior and interactions are unchanged, and the
reduced-motion preference makes the transition immediate.

Chromium dark and WebKit light verify an actual Data Sync-to-Summary return with
a held synthetic Summary response: busy opacity settles at 88%, response release
produces continuous monotonically increasing intermediate opacity samples, and
the final opacity is 100%. Both browsers report a 0.36s ease-in-out transition and
0s duration after enabling reduced motion. Build, CSS/CSP and deployed-asset checks
pass. This CSS-only follow-up does not rerun or claim a new full component suite.

Only UI rolls. All five deployments are healthy; four unrelated pods and all 18
configuration tuples are preserved. Both strict-TLS origins serve the reviewed
assets and canonical **dev-20261111** tags match deployed pins. Protected receipts
retain timing and frame samples; API/schema and workout data are unchanged.

## Workout Log Popup Link Hit Areas

Deployed **11/12/2026 at Helm 135** after owner review. All/None and Enable/Disable
buttons now have a **3rem by 1rem** click target with right-aligned text on the
title's unchanged baseline. Their tooltip wrappers use block layout and match
the button bounds exactly, removing the inherited help-cursor area around them.

**Helm 136** removes the separate Clear all filters hover-underline rule. Candidate
Chromium dark and WebKit light verify no decoration, a pointer cursor, successful
clearing and the unchanged disabled state. Build, CSS/CSP and deployed-asset checks
pass; this follow-up is CSS-only and preserves all other rollout settings.

Prior live browser measurements show a 21px tooltip wrapper around the old 17px
button, with a help cursor above/below its clickable area. Corrected Chromium dark
and WebKit light measure **48px by 16px** for each button and both wrappers. Text
position and baseline offsets match the prior UI for every label. Edge hit tests
reach the button with a pointer cursor, surrounding points do not use the help
cursor, and hover-leave dismissal remains intact. Production build and CSS/CSP
checks pass. This CSS-only change uses real-browser geometry verification; the
prior 128 focused tests and historical 926-test full-suite baseline are not new
rerun claims.

Only UI rolls. All five deployments are healthy; four unrelated pods and all 18
configuration tuples remain unchanged. Both strict-TLS origins serve the reviewed
assets, and canonical **dev-20261111** tags match deployed pins. Protected review
receipts retain prior and corrected geometry for all four labels in both themes.

## Workout Log Popup And Link Hover Refinements

Deployed **11/12/2026**, following owner review:

- **Helm 133** moves each Workout log column popup 0.7rem down/right, makes weekday
  and equal-width workout-type checkbox columns at least 16rem wide, and presents
  inclusive comparisons using the single-character mathematical symbols. The
  native HTML range input retains its keyboard/touch/snapping behavior while CSS
  gives it an unfilled neutral track and a rounded rectangular handle.
- **Helm 134** removes underlining from the All/None and Enable/Disable links and
  corrects their sticky help. Popup autofocus no longer counts as mouse-independent
  keyboard intent. Pointer exit dismisses the help even when the button keeps
  focus; a genuine keyboard-only focus still provides help. Other tooltip surfaces
  keep their existing default focus/hover-overlap behavior.
- **128 focused tests** pass across Summary, column filters and shared tooltips.
  The new autofocus/hover regression fails on the prior behavior and passes after
  correction, including both link labels and retained keyboard focus. The existing
  926-test full-suite baseline is historical, not a newly rerun full-suite claim.
  Production build, CSS/CSP, type/format and documentation checks pass.
- Candidate Chromium dark and WebKit light verify exact popup translation, equal
  minimum-width columns, native slider keyboard behavior with no filled-progress
  styling, and all four link hover/leave cases without an intervening click. Full,
  partial and zero-result table capacity, explicit Apply/Cancel, failures/retry and
  narrow containment continue to pass with synthetic private APIs.

Both rollouts change only UI. All five deployments are healthy; four unrelated
pods and all 18 configuration tuples are preserved. Canonical **dev-20261111** tags
match deployed pins, both strict-TLS origins serve reviewed assets, and application
schema/minimum runtime remain **31/31** with no API or data changes. Detailed
browser, image-publication and deployment receipts remain in protected review
storage.

## Workout Log Table Height Correction

Owner review identified excess blank space below a full 25-row Workout log page.
The hidden measurement cell's padding rule had equal specificity to a later generic
table-cell rule, so the cascade measured **69px** per row instead of the real
**53px**. Twenty-five slots consequently reserved **400px** of excess height.
The correction scopes measurement padding under `.workout-table`, retaining the
normal action-cell padding and existing frozen-capacity behavior.

Chromium reproduces the old mismatch. Corrected Chromium dark and WebKit light
verify matching 53px sample/actual rows and a **1374px** frame around the **1371px** natural table
(borders/last-row allowance only). Full, partial and zero-result filtering retains
that correctly sized frame; the existing interaction suite also passes in both
themes and narrow layouts. Production build, CSS/CSP and documentation checks
pass. The existing 926-test UI baseline is retained; this CSS-only follow-up uses
real-browser geometry checks rather than rerunning unrelated component tests.

Deployed **11/12/2026 at Helm 132**, rolling only UI. All five deployments are
healthy; the four other pods and all 18 configuration tuples are unchanged. Both
strict-TLS origins serve the reviewed assets, and canonical **dev-20261111** tags
match deployed pins. No API, schema or workout-data changes are required. Protected
review receipts retain the failing baseline and corrected geometry measurements.

## Workout Log Column Filters

Implemented and deployed **11/12/2026** as a post-M9 Summary extension.

- All eight Workout log columns have long-press filters, sharing the Map type
  picker's 500ms duration and transactional Apply/Cancel composition. Only one
  popup owns focus; Space/Arrow Down opens it, while click/Enter retains sorting.
  Outside/Escape/tab exit discards drafts, including superseded-popup focus races.
- Weekday choices follow First weekday. Alphabetical type choices come from the
  complete unfiltered range and use at most eight rows per column. Numeric filters
  have the specified operator order, preferred units, snap intervals, caps and
  20rem desktop slider area. Enable/Disable remains a draft until Apply.
- API filters intersect under account RLS before count/sort/pagination, with bound
  parameters and strict input validation. Unfiltered type choices, maxima and
  count remain available independently of the current filter/page. Exact pace
  seconds-per-km/mi comparisons cross-multiply before division; native tests prove
  equal boundaries for all four operators in both unit systems.
- Applied filters have themed blue Font Awesome indicators. Clear all filters
  replaces the redundant heading count and preserves sort. Paging shows filtered
  sessions; Range totals remains range-wide. Compatible updates retain rows at
  full opacity, freeze the unfiltered table capacity and fence old actions. Empty
  results retain headers, centered feedback and pager placement; errors retain
  compatible rows read-only with Retry. Narrow controls reuse the existing cards.
- Breakdown rows now use the same subtle themed hover mixture as Workout log rows.

Verification: **926 UI tests** pass; the final focused Summary/filter suite has
**112 passing tests**. Go tests, vet, generated bindings, formatting, CSS/CSP,
type/build and documentation checks pass. A fresh disposable PostgreSQL schema
verifies owner reads, every column, combined/empty filters, exact comparisons and
unfiltered metadata. Candidate Chromium dark and WebKit light exercise desktop
and narrow layouts with synthetic private APIs, held responses and real browser
focus/layout behavior. These checks do not claim actual-account browser E2E or
capacity certification. The owned test database/proxy were removed and the native
cluster stopped afterward.

**Helm 130** introduced the feature; **Helm 131** adds exact displayed-unit pace
boundaries. Both roll only API/UI. All five deployments are healthy, the other
three pods and all **18 configuration tuples** are preserved, the deployed API
binary matches the reviewed build, and both strict-TLS origins serve the reviewed
assets. Canonical **dev-20261111** tags match deployed pins, with no review aliases.
Application source/schema/minimum runtime remain **31/31/31**, baseline **27**,
ledger **0-31**; no migration or historical workout rewrite is needed. Detailed
browser/build/publication/deployment receipts remain in protected review storage.

## Milestone 9 Closeout

**Complete and accepted 11/12/2026**, following owner review of Stats/New Coverage
and the navigation/UX follow-ups. The owner confirmed that tab switching and the
New visits date/name handoff to Map are quick and practical, and requested M9
closure. The [README milestone table](../README.md#development-progress) now marks
Milestone 9 complete. M10 administration/operational completion is next planned.

### Accepted Scope

- Stats sits between Map and Data Sync, with the shared date picker and an
  extensible Reports sidebar. New Coverage supports monthly/yearly grouping,
  inclusive partial/zero buckets, sorting and preference-sized paging.
- First-ever and past-year return counts use all eligible canonical visit history,
  named-identity deduplication and calendar-year anniversary rules, including
  leap-day boundaries. Nonzero counts agree with their complete visit details.
- Searchable New visits dialogs show qualifying/predecessor dates and first-visit
  n/a correctly. Name and in-range date actions focus the intended Map entity or
  workout without changing the shared range; out-of-range predecessors stay plain
  text. Returning preserves report/grouping/page/sort and applicable dialog state.
- Owned, expiring scopes, account isolation, stale/late response fences and bounded
  renewal preserve private data and action validity across context changes.
- Retained tab workspaces, guarded application shortcuts, Tab-only focus, steady
  pagination/dialogs, Map camera/source reuse and Preferences content-focus return
  make repeated Stats/dialog/Map journeys practical. Related Summary hover and
  modifier refinements have also completed owner review.

### Verification And Deployment

The [initial implementation](#stats-and-new-coverage-initial-implementation) and
[Stats review](#stats-review-navigation-and-refresh) retain the native PostgreSQL,
API, catalog/readiness and independent actual-backup-restore evidence: anniversary
and history boundaries, deduplication, count/detail/workout-target parity,
foreign/stale scopes and the 394-query oracle across two ranges. Their original
test counts, timings and deployment revisions remain historical checkpoints.

The accepted [UI follow-up](#standalone-modifier-hover) has **907 passing UI tests**,
successful type/build/CSS/CSP/format checks and candidate/deployed Chromium dark
and WebKit light checks. The [retained workspace record](#retained-tab-workspaces)
adds six-return, 500-synthetic-workout comparisons and verifies renderer/source/
camera reuse, paused inactive polling, active-content focus and session cleanup.
These scoped measurements support the accepted interactive workflow; they do not
replace M12 typical/stress/large-history or mixed-workload capacity benchmarks.

Acceptance is at **Helm 129**, with all five deployments healthy and reviewed
assets verified on both strict-TLS origins. Canonical **dev-20261111** tags match
the deployed pins. Application source/schema/minimum runtime remain **31/31/31**,
baseline **27**, ledger **0-31**. This closeout records acceptance of deployed work;
it requires no application, database or deployment change.

M10 administration, the full M11 tablet-device/accessibility matrix, M12 release
verification/capacity work, M13 public publication and M14 production hardening
remain planned under their existing scopes. Future reports/charts remain
extensions of the registry rather than missing M9 acceptance work.

## Stats Milestone Plan Update (11/5/2026)

The owner inserted **Stats And New Coverage** as the new **Milestone 9**. The
previous administration/operations, tablet UI/UX and MVP verification are now
**M10, M11 and M12**; production hardening is **M14** after the subsequent M13
GitHub publication insertion below. Historical
acceptance dates/results stay unchanged; milestone references use the current
sequence. In particular, capacity benchmarking belongs to M12, not the new Stats
milestone or M10 administration.

The [milestone list](../README.md#development-progress),
[implementation plan](implementation-plan.md#milestone-9-stats-and-new-coverage),
[product brief](product-brief.md#stats-and-reports-milestone-9),
[functional specification](functional-spec.md#feature-explore-stats-and-new-coverage)
and [technical specification](architecture.md#stats-reports-and-new-coverage-m9)
record the feature. Stats sits between Map and Data Sync, with the shared date
picker above an extensible Reports sidebar and parent-owned report selection.
New Coverage is the initial monthly/yearly report, counting named canonical
identities with all-history first/predecessor evaluation before range filtering.
Past-year counts include first-ever visits and returns strictly after a
calendar-year anniversary; first-ever Previous visit shows grayed-out **n/a**,
matching unavailable Summary Distance. Whole-cell nonzero actions open compact,
searchable visit-detail dialogs with true qualifying event dates.

This is a documentation/planning update. Stats implementation and verification
remain future work; the current deployed application remains Helm 80.

## GitHub Publication Plan Update (11/5/2026)

The owner inserted **GitHub Project Publication** as **Milestone 13**, moving
production deployment/scaling/security from 13 to **14**. M9-M12 retain their
Stats, administration, tablet and verification assignments. The
[milestone plan](implementation-plan.md#milestone-13-github-project-publication),
[product brief](product-brief.md#public-distribution-planned-milestone-13),
[functional contract](functional-spec.md#feature-install-and-use-the-public-release)
and [technical design](architecture.md#github-publication-and-public-installation-planned-m13)
record the public installation path.

M13 prepares VERSION 1.0.0 and an initial public GitHub Release; explicitly uses
**GitHub Actions** for tests, OCI image builds/GHCR publishing, chart publication
and GitHub Pages deployment; adds real README badges; and publishes Docusaurus
Markdown/MDX administrator/user documentation. One Helm package is distributed
through both a legacy HTTPS chart repository and GHCR OCI, with public stable
image defaults and an optional standard standalone PostgreSQL dependency plus
tested BYO mode. PostGIS/collation compatibility, dependency/migration startup
ordering, durable data/credentials and independent public installs are acceptance
requirements. Owner production deployment and measured hardening remain M14.

This is planned work: VERSION and runtime/chart/workflow files are not changed by
this documentation update, and Helm 80 remains the current deployed application.

## Standalone Modifier Hover

Helm **129** removes anticipatory Alt/Command dismissal from Summary previews.
Pressing or releasing Alt, Command/Meta or Ctrl alone now preserves a hovered
card. Actual focus/visibility loss, window-focus restoration and live pointer
boundary reconciliation continue to dismiss obsolete previews; missing-leave,
cached-enter and lost-Shift-keyup recovery remain intact. Modified Tab is still
not treated as fresh card keyboard-navigation intent.

All **907 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Candidate and deployed Chromium dark/WebKit light checks verify modifier-only
hover preservation, actual focus-loss dismissal, Shift interaction, single
ownership and the three lost-event recovery cases. Both origins serve reviewed
assets; all five deployments are healthy. Only UI changes under canonical
**dev-20261111**; four unrelated pods and all 18 configuration tuples remain
unchanged.

## Aggregate Window Restore Recovery

Helm **128** corrects a stale mouse preview after OS app switching. A controlled
browser regression reproduces the Helm-127 failure by suppressing card leave
events and moving onto a real Workout log row: row hover activates while the
old preview remains open. Explicit blur/visibility tests alone did not exercise
that missing-boundary-event path.

While a mouse preview is open, capture-phase document pointer/mouse movement now
checks the actual original/Shift-expanded bounds and observed Shift state.
Moving elsewhere closes the obsolete preview even if leave or Shift keyup was
lost, including a cached restore-enter event. Alt/Meta initiation and window-focus
return dismiss old ownership without consuming OS shortcuts; modified Tab no
longer resets the restored-focus guard. Genuine keyboard-only previews keep their
focus-based behavior, and the single-owner, Shift-click and contrast contracts
remain intact.

All **906 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Candidate and deployed Chromium dark/WebKit light checks repeat the original
interaction suite plus missing-leave, cached-enter, lost-keyup, actual Workout
log hover and OS-modifier/focus-return regressions. These use synthetic private
data and deliberate event-loss simulation, not desktop OS automation. Both
origins serve reviewed assets and all five deployments are healthy. Only UI
changes under canonical **dev-20261111**; four unrelated pods and all 18
configuration tuples remain unchanged.

## Shift-Held Aggregate Previews

Helm **127** deploys Summary's extended Range totals/averages card interaction.
Ordinary hover still closes at the original button boundary. While Shift is held,
the visible breakdown extends that boundary; rows receive a subtle theme-aware
gray hover background. Clicking a row or expanded padding delegates to the same
totals/averages toggle exactly once and retains the original button's focus.
Releasing Shift after using the expanded area closes it when the pointer is
outside the original card; release over the original card restores normal hover.
Keyboard-only Shift+Tab retains the existing one-stop-per-card navigation.

One grid-local owner arbitrates keyboard focus and mouse hover, without rerendering
the Workout log for preview changes. Superseded blur cannot close a newer owner.
Window blur, hidden document, outside focus, Escape, resize and retained-tab exit
clear the preview, held modifier and pending hints. Restored button focus alone
cannot reopen a window-dismissed preview; fresh pointer/Tab intent can.

All **898 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Candidate and deployed Chromium dark/WebKit light checks cover all four cards,
original/expanded bounds, row/padding clicks, mixed mouse/keyboard ownership,
focus/visibility loss, retained tab return and narrow keyboard scrolling. The
browser fixture uses synthetic private data; headless window switching uses an
explicit blur signal when the platform supplies none. Measured hover contrast is
at least **10.29:1** for type labels and **5.26:1** for muted values across both
themes. Both origins serve reviewed assets, all five deployments are healthy,
and four unrelated pods plus all 18 configuration tuples remain unchanged. The
UI uses the existing canonical **dev-20261111** tag.

## Retained Tab Workspaces

Helm **126** deploys the UI-only retained-workspace follow-up. Previously the
Shell's conditional tab tree unmounted each old page: leaving Map disposed its
MapLibre renderer, canvas, workers, sources and loaded resources, so each return
created another renderer and capability despite retaining selected view state.

Visited tabs now remain mounted within the session-keyed frame; unvisited tabs
stay lazy. Inactive trees are hidden/inert and avoid unrelated reconciliation.
Map keeps the same renderer, canvas, focused source, camera and valid caches;
the dense route list is reused, and unchanged dimensions no longer invoke
MapLibre resize's buffer reallocation/camera events. Inactive application polling,
provider fallback retries, Space handling, hover and blink work pause. Reveal
handles real resizing and capability expiry, while unchanged background
validation does not replace sources or refetch points/tiles. Date/time-zone and
deletion boundaries fence obsolete private pixels; late requests cannot restore
them. Explicit target navigation, readiness fallback, active-content/Preferences
focus and the diagnostic entry contract remain covered. Signout/session or
identity replacement disposes all retained workspaces and the renderer.

An identical synthetic **500-workout** fixture with real MapLibre/provider styles
measures six returns per engine through Stats, Summary and Data Sync. Candidate
median return times improve from **2679 to 571 ms** in Chromium dark (**79%**) and
**2273 to 1125 ms** in WebKit light (**51%**). These are controlled headless-browser
measurements, not actual-account end-to-end latency or capacity certification.
Identity/source/camera checks verify reuse rather than relying on timing alone.

All **885 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Candidate and deployed Chromium dark/WebKit light checks verify retained tab DOM,
renderer/source identity, cached geometry/tiles, paused polling, resize/reveal,
Stats grouping/page preservation, Preferences/Space focus, narrow containment and
signout cleanup. Both origins serve the reviewed assets; all five deployments
are healthy. Only the UI image changes under canonical **dev-20261111**; four
unrelated pods and all 18 configuration tuples remain unchanged.

## Map Layer Presentation And Preferences Focus

Helm **123** rebuilds API, shared worker, UI and OSM operator images from the
reviewed source using the requested **dev-20261111** tag. Helm **124** publishes
the Preferences focus follow-up under that same canonical tag and rolls only UI;
the rebuilt API/worker/OSM references and Martin remain intact. All five
deployments are healthy, both origins serve the reviewed assets, and all 18
configuration tuples are preserved. Application schema/minimum runtime remain
31/31 with the retained migration ledger; no migration replay or OSM rebuild is
part of this release.

Normal path lines and stationary dots use **90% per-layer opacity** in Routes,
production Coverage and Diagnostic states. The shared presentation policy orders
provider geometry, background paths, focused paths, existing provider symbols,
direction arrows and endpoints, then the temporary white selection blink. Road
names/shields retain full paint opacity; the blink remains a full-strength
100%/0% attention exception. Invisible diagnostic hit targets remain invisible.
Style transformations and private-layer recreation share the same ordering;
ordinary camera frames do not reorder already-correct layers. The functional
specification's [layer table](functional-spec.md#map-layer-stack-and-opacity)
records applicable modes, states and opacity.

Changing the diagnostic preference in an existing focused Coverage visit now
transitions directly to/from diagnostic review without refitting or losing the
mode, including Stats-origin visits. Every Preferences close path returns focus
to the active tab's content area; Map uses its canvas through the existing
readiness handoff. The handoff runs after modal close-focus handling, so Space
controls the map instead of reopening the profile menu.

All **877 UI tests**, type/build/CSS/CSP, formatting and documentation checks
pass. The full Go suite, vet, generated-source, version and UI-artifact checks
also pass for the all-image rebuild. Candidate and deployed Chromium dark and
WebKit light checks use synthetic private API data with real MapLibre/provider
styles across all three families, style reloads, Space visibility, diagnostic
transitions, camera retention and mobile containment. They exercise Save, Cancel,
close button, Escape and outside dismissal on all four tabs, including immediate
Space behavior on the Map canvas. Harbor cleanup removes **15 non-current
artifacts** from exactly the four application repositories, preserving the four
current images and their attached metadata; only dev-20261111 tags remain.

## Search Dialog Arrow Paging

Helm **122** deploys the UI-only paging exception for searchable modal tables.
Left/Right and shifted five-page arrows target the active dialog when its search
field is displayed but unfocused. Focused text entry/native editing widgets keep
their arrow keys. The exception bypasses only the visible-field paging gate;
modal/background ownership, unavailable/busy clamps, modifier/IME guards and the
separate number/letter visibility rule remain intact. Arrow paging does not
activate focus-ring mode; Tab/Shift+Tab still owns that transition.

All **865 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Chromium dark and WebKit light candidate/deployed checks exercise Road Coverage
and New visits paging from headers/pagers/checkboxes with visible unfocused
search, native caret/selection arrows with focused search, five-page clamps,
held-response extra-press fencing, background pager protection, other shortcut
suppression and Tab-only ring behavior. Four unrelated pods and all 18
configuration tuples are preserved; both origins serve reviewed assets and all
five deployments are healthy.

## Tab-Only Focus Navigation

Helm **121** deploys the UI-only focus-modality refinement. Only Tab/Shift+Tab
enters keyboard-navigation highlighting. The shared shortcut dispatcher no longer
activates ring mode, and the modality tracker ignores arrows/Home/End and other
non-Tab commands. Commands preserve an already-active Tab mode; pointer use exits
it. Content landmarks and Map canvas handoffs retain actual focus but never draw
a large-area outline. Smaller controls, including Files headings, retain normal
focus rings only when the Tab-initiated mode is active. Existing paging/focus/
scroll ownership and shortcut behavior remain intact.

All **865 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Chromium dark and WebKit light candidate/deployed checks verify number/Map/group
shortcuts without ring activation, Files single/five-page arrows in pointer mode,
normal Files/header focus after Tab, prior-mode persistence during shortcuts,
pointer reset, and outline-free large content/canvas targets even in Tab mode.
The style guide and functional spec record the generic rule. Four unrelated pods
and all 18 configuration tuples are preserved; both origins serve reviewed
assets and all five deployments are healthy.

## Application Shortcuts And Preserved Map Viewport

Helm **120** deploys a shared document-level keyboard router for invisible
application-tab, table-page, Map mode/focused-fit and Stats grouping commands.
The functional specification owns the key map. Displayed text-entry surfaces
block the commands even when unfocused; native widgets, menu typeahead, modals,
modifiers and IME composition retain ownership. Pager routing chooses exactly
one deepest/remembered/primary region and reuses the button path's clamp and
pending fences. Tab navigation hands focus to programmatic content landmarks or
the ready Map canvas without adding sequential stops or visual shortcut hints.

Routes/Coverage switches now preserve center, zoom, bearing and pitch, including
when a workout is focused. Mode changes and diagnostic-result arrival no longer
create fit requests. Explicit route clicks, fit controls and focused-only `F`
retain their current-mode bounds semantics. This supersedes the historical
mode-transition auto-fit checkpoint below; initial entry/range fits retain their
feature contracts.

All **864 UI tests**, type/build/CSS/CSP, formatting and documentation checks pass.
Native Chromium dark and WebKit light candidate/deployed checks exercise the
requested keys, focus from ordinary controls, tabbing, editing/caret safety,
visible unfocused search/date fields, modal/menu guards, page clamping/busy
fences and independent Task history/Files/Events ownership. Actual MapLibre camera
getters confirm unchanged custom pan/zoom state across both mode transitions;
unit tests distinguish focused raw/Coverage bounds from aggregate selection
bounds. No shortcut legends or label changes are rendered. Four unrelated pods
and all 18 configuration tuples are preserved; both origins serve reviewed
assets and all five deployments are healthy.

## Centered Empty States And Search Continuity

Helm **119** deploys `DialogTableViewport`, separating reserved height from data
rows. Road Coverage and New visits show accepted zero-result messages in a
slightly larger, centered viewport layer below the header; their actual result
bodies contain no rows. The message remains centered after horizontal scrolling.
Nonempty partial results reuse positional rows and hide unused slots. Measured
header/row layout sizes ignore dialog entry transforms, and resize writes are
scheduled outside observer delivery to keep reopened geometry stable.

New visits no longer treats incremental search as an incompatible dataset.
Within the same scope/group/bucket/metric, old rows remain mounted at full opacity
while a response is pending, then values update in place. Pending row actions are
guarded without native-disabled text flashing; action nodes retain record keys.
Owner/date/bucket/metric boundaries, scope proof checks and late-response fences
remain intact. The UI style guide explicitly covers search continuity and
centered, header-only zero-result states alongside the generic frozen-size rule.

All **829 UI tests**, type/build/CSS/CSP and formatting checks pass. Native
Chromium dark and WebKit light candidate/deployed checks use short and multi-page
synthetic sets, including a narrow layout. They verify empty bodies/header-only
tables, larger text centered on both axes before/after horizontal scrolling,
unchanged dialog/input/table/footer rectangles, zero row removals while a search
response is deliberately held, retained row/cell identity after admission,
hidden surplus rows and retry/cache restoration. Both origins serve reviewed
assets; four unrelated pods and all 18 configuration tuples are preserved, and
all five deployments are healthy.

## Frozen Table-Dialog Capacity

Helm **118** deploys shared unfiltered sizing for Road Coverage and New visits.
`dialogTable` clamps dialog paging/capacity to the preference and 25-row feature
limit. Road Coverage includes unnamed membership when reserving its rows, even
with that display filter off. New visits uses the unfiltered bucket count rather
than filtered pagination totals. Partial, empty and loading results retain blank
row space; pager caption width is reserved from unfiltered pages to prevent
footer rewrapping. Cached filter/page restoration starts from full membership,
so clearing a restored search cannot enlarge the dialog. Initial reservations
are held once editing/interaction begins before bulk membership is known.
Search resets only internal vertical scroll, and visit retry feedback floats
without changing layout. The style guide records this as a generic rule for
every dialog containing a table, irrespective of contents.

Shared sortable-header focus now uses the same inset, cell-wide treatment on
Summary, Stats and dialog tables; this supersedes the preceding Summary-specific
outset reference while retaining the other focus improvements.

All **827 UI tests**, affected-suite rechecks, type/build/CSS/CSP and formatting
checks pass. Chromium dark and WebKit light compare dialog/input/table/footer
rectangles through narrowed/cleared/empty searches, unnamed toggles, partial
pages, road-name dismissal/blink/reopen, cached state restoration, delayed
responses and retry recovery, with short and multi-page synthetic datasets and
narrow viewports. A separate early-edit case verifies no contraction while the
initial bulk response is pending. Header checks confirm inside-cell focus paint
on all four shared table compositions. Four unrelated pods and all 18
configuration tuples are preserved; both origins serve reviewed assets and all
five deployments are healthy.

## Map And Stats Focus Visibility

Helm **117** deploys the UI-only focus visibility fixes. Route action/help rings
and Stats header rings now paint inside their hit targets, with route content
clear of the ring. Joined Map/Stats mode controls retain rounded clipping while
showing inset focus on either segment; selected fills use contrasting themed ink.
System Highlight remains authoritative in forced-colors mode. Narrow Stats
frames use their available width; the desktop composition and Summary reference
header treatment remain intact.

All **822 UI tests**, type/build/CSS/CSP and formatting checks pass. Native
Chromium dark and WebKit light candidate/deployed checks reproduce the old
clipping, then verify painted pixels on every edge, selected/unselected focus
contrast, first/last/unavailable route controls, Stats and visit-dialog headers,
narrow reports and the Map sheet. Focus does not activate selection, keyboard
sorting still works, and pointer focus does not acquire keyboard rings. Four
unrelated pods and all 18 configuration tuples are preserved; both origins serve
reviewed assets and all five deployments are healthy.

## Range Totals Keyboard Focus

Helm **116** deploys the UI-only single-stop card focus fix. Duration's per-type
precision tooltip had been a focusable descendant of its preview. Forward Tab
entered it, blurring the owning button and hiding the newly focused target;
backward traversal encountered the still-closed preview and skipped that stop.
Duration preview hints are now passive styled help. Closed previews are inert,
containers stay outside sequential focus, and overflow can be scrolled from the
focused card without another stop. Escape and totals/averages activation retain
their contracts; individual Workout log duration help remains keyboard-focusable.

All **822 UI tests**, type/build/CSS/CSP and formatting checks pass. A browser
focus-event trace reproduces the original forward-only focus loss, then verifies
the exact four-card order in both directions. Chromium dark and WebKit light
candidate/deployed checks cover desktop and narrow overflowing previews,
keyboard scrolling, focus retention, Escape and average-mode activation. Four
unrelated pods and all 18 configuration tuples are preserved, both origins
serve reviewed assets, and all five deployments are healthy.

## Workout Log Paging Performance

Helm **115** deploys a UI-only rendering optimization. Read-only RLS-scoped SQL
profiles and request timing directed investigation toward browser work. The
former path repeatedly constructed Intl formatters, rendered collapsed mobile
details and rebuilt the full time-zone choice list even with Preferences closed.
The shared bounded formatter cache, lazy secondary content/options and reused
desktop page cells remove that work. Menu roots stay keyed to workout identity;
API payload, per-page requests and existing freshness behavior remain intact.

All **820 UI tests**, type/build/CSS/CSP and formatting checks pass. Regressions
cover DST/quarter-hour offsets, locale/clock/precision distinctions, invalid
zones, bounded eviction, lazy details, deferred time-zone enumeration and menu
identity when rows are replaced. Sequential unthrottled synthetic benchmarks
use identical varied 25-row/all-column records and verify identical rendered
values: median click-to-DOM improves from **1,628 to 174 ms** in Chromium and
**1,662 to 472 ms** in WebKit. Warm-page Intl constructor counts fall from roughly
five thousand to zero. Measurements describe controlled fixtures and the review
browser environment. Deployed dark/light checks confirm the same zero-build
hot path, one request per page and working mobile expansion/collapse. Four
unrelated pods and all 18 configuration tuples are preserved; both origins
serve reviewed assets and all five deployments are healthy.

## Shared Tab Content Background

Helm **114** deploys the UI-only shared background composition. Summary, Stats,
Data Sync and the area beneath MapLibre now reuse `ContentBackground` and one
semantic background rule for the dim grid, theme gradient and stepped corner
contours. Map's loading state uses the same treatment. Decoration is clipped,
noninteractive and hidden from assistive technology; report foreground surfaces
remain above it. Sidebar/card surfaces and Map popup stacking retain their
established behavior. The UI style guide explicitly requires this consistent
composition and links its implementation.

All **813 UI tests**, a focused Stats recheck, type/build/CSS/CSP/format checks
pass. Chromium dark and WebKit light compare all four computed backgrounds and
decorative geometry at desktop/mobile sizes, verify decoration does not alter
report-table pixels, preserve Stats paging across tab switches, exercise the
range picker, and check Map host containment/stacking. Both deployed origins serve
reviewed assets; four unrelated pods and all 18 configuration tuples are
preserved, and all five deployments are healthy.

## Bounded GPS Recovery Filtering

Helm **113** deploys `raw-route-display-v3` in API and shared workers. The
endpoint-only speed rule could leave slower fragments between bursts or during
GPS recovery. The shared display/export helper now expands a confirmed
impossible-speed, degraded-accuracy episode only when a bounded lookahead finds
sustained reliable recovery. It preserves original temporal/spatial boundaries
and never reconnects across excluded samples. Points export remains schema 2,
with the refined policy identifier and the same explicit components.

Full Go suite/vet and generated-source checks pass. Fresh native schema and
export/RLS/geometry tests cover recovery across recording gaps, preserved valid
running after a spike, fast movement without a seed, poor accuracy alone,
unconfirmed recovery, lookahead exhaustion, cycling and stationary controls.
The original reported routes match an independent membership oracle in native
replay and production, including exact source evidence, stored geometry and all
tile detail levels. Original point/workout counts, Coverage fingerprint/match
count and migration metadata/ledger are preserved. A fully restored route backup
and operator rehearsal precede the derived rebuild; 001-031 remains the complete
source inventory, schema/minimum 31/31 and baseline 27.

Chromium dark and WebKit light candidate/deployed checks confirm export download
compatibility and MapLibre loading with synthetic fixtures. UI and Martin pods
are unchanged, all five deployments are healthy, both origins serve the existing
reviewed UI assets, and all 18 configuration tuples are preserved. Private route
diagnostics and backup artifacts remain outside the repository.

## Shared Raw-Route Display And Exports

Helm **112** deploys `raw-route-display-v2` across API, shared workers and UI.
Folded 010 adds the owner-RLS display membership helper, shared by ingest-derived
geometry and both route exports. The conservative positive-time walk/run speed
rule excludes suspect endpoints as well as connecting edges. Original samples
remain immutable evidence. Surviving singleton fragments do not create dots;
explicit components prevent bridging removed samples in focused lines, endpoints,
arrows, GeoJSON and points downloads. Exported bounds/count/elevation describe
retained samples; version-2 points include original sequence IDs and components.

The existing 001-031 inventory, schema/minimum 31/31 and baseline 27 remain.
Guarded operator DDL and derived repair were rehearsed; the production route
backup was fully restored and verified before rollout. An initial oversized
diagnostic report aborted the rebuild before commit and left Helm 111 failed.
The retry uses bounded counts/hashes, a short catalog transaction and an atomic
derived backfill. All stored routes were checked and changed display geometries
were rebuilt. Exact source samples for the reported routes, global source
point/workout counts, Coverage-state fingerprint and match count, and migration
metadata/ledger are preserved. The six reported routes agree exactly with the
independent retained-membership oracle, stored geometry, full-detail chunks and
every LOD. Historical receipts retain their original scope.

All **813 UI tests**, full Go suite/vet, generated-source, type/build/CSS/format
checks pass. Native PostgreSQL tests cover exports and stored geometry, endpoint
removal without reconnecting gaps, evidence retention, owner isolation, cycling
and stationary controls, complete rejection, and elevation across components.
Chromium dark and WebKit light candidate/deployed checks exercise both export
menu downloads and the real MapLibre map with synthetic filtered fixtures.
All five deployments are healthy, both origins serve reviewed assets, Martin is
unchanged, and all 18 configuration tuples are preserved.

## Operation Order And Raw-Route Anomaly Investigation

Helm **110** deploys the requested Operation menu order: All operations, Manual
sync, Automated sync, Coverage update, Workout deletion. All **220 Data Sync
tests**, type/build and format checks pass. Chromium dark and WebKit light verify
the candidate and deployed menu ordering, Home/End/arrow/Enter selection, Escape
focus return and unchanged filter query/page-reset semantics. Both origins serve
reviewed assets; unrelated pods and all 18 configuration tuples are preserved.

A separate read-only investigation confirms that continuous sequences of
implausibly fast GPS fixes can survive the existing time-gap and per-edge spatial
cutoffs. A walking/running-only speed-break candidate reproduces the stored
baseline geometry and removes the confirmed high-speed connections without
inventing replacement links or changing source samples. Synthetic controls
retain walking, running and fast sprinting; other movement classes need their
own policy. This candidate remains **offline research, not deployed behavior**;
production raw geometry, imported facts and Coverage evidence are unchanged.
Private captures and measurements remain outside the repository.

## Compact Steady Pagination

Helm **109** further reduces the centered glyph footprint by roughly one tenth,
retaining compact button dimensions, stroke weight and first/last edge contact.
The 11 focused pager tests, type/build/CSS/format checks and light/dark candidate
and deployed browser measurements pass. The revision-108 behavior checks below
retain their original scope.

Helm **108** deploys the UI-only pager refinement. All six controls now share the
compact Show workout types Cancel/Apply height and retain their width/height
ratio. Smaller rounded SVGs use finer strokes; first/last bars and chevrons touch
without overlapping. Standard theme surfaces and foregrounds replace the solid
neutral enabled fill, and disabled symbols match table `n/a` gray.

Pending page loads and same-page background refresh retain displayed boundary
states, colors and cursors. Extra presses are ignored, with an immediate-click
fence before the owner can render pending state. Boundary availability updates
with rendered results; unavailable failed results retain their recovery guard.

All **812 UI tests**, type/build/CSS/format checks pass, including immediate
repeat presses, unchanged same-page refresh states, deferred direct Workout log
jumps and existing paging/recovery contracts. Chromium dark and WebKit light
candidate/deployed checks cover all eight pagers, compact popup height equality, stroke
geometry, shared colors, clamping, narrow containment and native disabled-attribute
observation during delayed navigation and same-page refresh. Deployment preserves
four unrelated pods and all 18 configuration tuples; all five deployments are
healthy and both origins serve the exact reviewed assets.

## Shared One-Click Pagination

Helm **107** deploys the UI-only shared `PageNavigation` composition across all
eight paging locations: Workout log, Road Coverage, Stats report and New visits,
Task history, Files, Events and Logs. First/last, clamped five-page jumps and
single-page steps use equal SVG-only buttons, accessible names and no tooltips.
Buttons preserve the former text-button height, use the requested width/height
ratio, thick rounded strokes and enabled/disabled treatments in both themes.
Narrow footers wrap controls without shrinking targets or clipping actions.

All **809 UI tests**, type/build/CSS/format checks pass, including a deferred
Workout log jump that requests only the destination while retaining rows and
disabling competing actions. Chromium dark and WebKit light verify candidate
and deployed assets with synthetic 58-page fixtures at all eight locations:
repeated jumps, clamped boundaries, first/last availability, keyboard activation,
SVG geometry, equal dimensions, cursors/colors, Files filter retention and narrow
Workout log/Road Coverage containment. Both origins serve the exact reviewed
assets; all five deployments are healthy, four unrelated pods and all 18
configuration tuples are preserved.

## Road Coverage Content-Sized Dialog

Helm **106** deploys the UI-only sizing follow-up. Road Coverage now uses the
table's natural height, following Stats New visits, rather than a fixed row-height
estimate and JavaScript viewport row cap. Full 25-row pages fit without vertical
overflow on a sufficiently tall viewport. Short and filtered results shrink;
hidden, actual-height row slots keep the pager stationary on partial last pages.
The rows-per-page preference, cached filters, focus return and small-viewport
table scrolling remain intact.

All **800 UI tests**, type/build/CSS/format checks pass. Chromium dark and WebKit
light measured candidate and deployed assets with synthetic fixtures: 25 rows,
seven/one/empty results, cached reopen, stable partial pages, resized desktop and
mobile containment, unnamed visibility, Escape and Map-canvas focus return.
A separate ten-row preference check passes. Both origins serve the exact reviewed
assets; all five deployments are healthy, four unrelated pods and all 18
configuration tuples are preserved.

## Coverage Graph Expansion Regression Closure

**Worker fixes deployed at Helm 104/105; all three authorized failures recovered.**
Read-only, single-connection standby replays captured the exact failing windows
and reproduced every old graph-layer error offline. Finer generation-62
segmentation exposed two defects in the former 64-round guard: corrections to
already-loaded shortest-distance labels repeatedly queried the same adjacency,
while a valid short-piece chain could require more discovery hops despite fitting
the actual node/edge and distance budgets. Private input and topology captures
remain outside the repository; public regressions are independently synthetic.

The graph bridge now caches adjacency (including empty leaves) and settles loaded
edges with a deterministic distance-priority queue before fetching new nodes.
Each node is fetched once, bounding cache and query work by the existing node
budget. The independent hop ceiling is removed; **2,048 nodes, 4,096 edges**, the
distance radius, bounded query responses, cancellation checkpoints, sampled-point
and evidence/output limits, snapshot and caller deadlines remain enforced. No
arbitrary pruning, prefix matching, weakened acceptance threshold, policy-version
change or global rematch is introduced. A complete-input replay through the
normal route pipeline, clipping and preparation succeeds for every affected route.

Two synthetic regressions fail on the old implementation and pass on the fix:
competing long/short paths through a short-piece chain, and a bounded deep chain.
An independent fixed-point shortest-distance oracle checks randomized graph
variants and both response orders. Tests also cover one lookup per node, reachable
nodes discovered only after distance improvements, distance/node/reply bounds,
cached-CPU cancellation, disconnected/foreign endpoints and invalid weights.

Production retry validation exposed a separate finalization interaction: a
heartbeat could wait on its own writer's job-row lock during a slow COMMIT, then
cancel that commit at the control timeout. The third route recovered through
normal lease recovery; Helm 105 prevents repetition by cooperatively stopping
and draining renewal control before the final fenced transaction. It does not
cancel the operation or extend the existing lease/deadline. All exit paths join
the heartbeat safely and repeated shutdown is idempotent. A native PostgreSQL
six-second deferred-COMMIT test fails on worker 104 and passes on the coordinated
implementation. Native lease renewal/loss, stale ownership, cancellation, newer
target/revision, atomic replacement and rollback tests pass. Full Go tests, vet
and worker/Coverage race tests pass.

Only the three authoritative failed targets were retried with the native
constructor. All now have succeeded jobs, **current/matched generation-62 proofs**,
unchanged input hashes/revisions and matching policy/sampling descriptors. Stored
segment counts equal the read-only replay's prepared results and geometry is
valid/current. Independent daily/all-time path and park rollup oracles have zero
mismatches. No Coverage jobs or matcher slots remain active. Historical failed
attempts remain auditable and are linked to successful successors; no successful
sibling was rerun. The per-account persistence-admission safeguard remains one.

Both rollouts preserve all **18 configuration tuples**, API/UI/Martin pods,
OSM/Martin images, normal resources and APP **31/31** / OSM **10/10**. No schema
change, Goose replay, OSM rebuild, provider fetch or raw-workout mutation is needed.
Only canonical Harbor tags are used. Private receipts, scoped traces and native
replay evidence are retained in `coverage-graph-layer-regression`.

## All Time And Range-Aware Job Route Navigation

**Deployed 11/9/2026 at Helm 103.** The shared picker places **All time** after Last
year and before the Custom divider on Summary, Map and Stats. It is a persisted
shortcut, not an MRU custom range. Workout lists, extents, Summary totals and route
capability capture omit date predicates in this mode. Captured scopes and reusable
canonical contexts carry an immutable unfiltered flag; detail/focus and Coverage
tile readers bypass dates while retaining normal authorization, checked membership
and deletion rules. A complete All time capability is not truncated to the bounded
range limit. Display metadata starts at the earliest owner workout and ends at
the later of local today or the latest owner workout; empty/unknown-date fallback
and future dates are covered. Summary metadata/totals use one RR snapshot.

Every available Route runs reference is a theme-text-to-orange anchor. A shared
workout navigation action keeps an encompassing current range, otherwise selects
the first covering preset in picker order and persists it. A changed selection
uses the normal page/sort/catalog/checked/focus reset path, then requested-ID
navigation fits/checks/centers the target after fresh capability data arrives.
Renderable Coverage is preferred and missing Coverage falls back to raw Routes.
Unknown local dates choose All time rather than guessing a bounded membership.
Stats uses the preserve-range policy; out-of-range Previous visit dates remain
plain noninteractive text. Map/Stats reselecting their current range no longer
clears a catalog/scope without scheduling a reload; Summary save-retry behavior
is retained.

All **800 UI tests**, full Go tests, vet, generated-source checks, type checks,
CSS/CSP guards, formatting and build pass. Native PostgreSQL checks include owner
isolation, old/future/undated workouts, selected-zone today floors, empty-account
metadata, **5,003 actual routed rows**, raw MVT inclusion of undated/old routes,
and a deliberately narrow display span whose unfiltered context/detail/report
still retains out-of-span history. Native canonical/tile authorization and geometry
regressions pass. An old production-schema-only clone rehearses the guarded
upgrade; genuine restricted API/worker/Coverage-worker roles pass full readiness
and the catalog-mutation matrix, including unfiltered-flag/default corruption.

Candidate and deployed Chromium dark/WebKit light fixtures verify all picker
positions, All time request bodies, link hover/cursor, out-of-range expansion and
persistence, Summary page/sort resets, in-range preservation, actual centered-list
scroll calls, raw fallback, unchanged MRU ranges and noninteractive Previous visit.
Narrow-layout checks cover Summary, job references and the Map controls sheet.
These browser API fixtures are synthetic; no production preference is changed.

The live guarded SQL preserves **31/31**, baseline **27**, ledger **0-31**, source
facts and preferences. Applied Goose migrations are not replayed. API, shared
worker/Coverage worker and UI roll; OSM/Martin image references and the Martin pod
are unchanged, and all **18 configuration tuples** remain intact. Exact UI assets
match both strict-TLS origins; all five deployments and all three application
database health probes pass. Canonical Harbor tags only. Private receipts:
`all-time-navigation`.

## SVG Raw-Route Finish Marker

**Deployed 11/9/2026 at Helm 102.** The remaining red finish-square font glyph is
replaced with a self-contained SVG rectangle registered as a MapLibre image.
It no longer requires provider glyph fonts. Theme outlines select a cached SVG
image; style restoration re-registers it, and late image loads cannot modify an
unmounted map. Endpoint ownership, overlap rules, fading and layer ordering stay
in the existing MapLibre flow. Route status badges remain full SVG; the start
marker, pending spinner and direction arrows were already non-text geometry.

All **784 UI tests**, type checking, CSS/CSP/format/build checks pass. Candidate
and deployed Chromium dark/WebKit light fixtures verify the existing badges and
an installed finish image with no text field, preserved opacity transition and
successful style restoration. The browser fixtures use synthetic routes and real
MapLibre rendering. Exact assets match both strict-TLS origins; only UI rolls,
the other four pods and **18 configuration tuples** are unchanged. Private
receipts: `svg-route-finish-marker`.

## Offline OSM Rebuild And Coverage Reconciliation

**Completed with explicit route-limit exceptions on 11/9/2026, Helm 101.**
Owner-authorized offline rebuild used an external custom-format OSM backup,
fully decoded and restored on an owned isolated PostGIS server. Restored schema,
generation/storage metadata, data counts and valid indexes were verified before
retiring the old generation and reclaiming only its canonical data partitions.
Application Coverage evidence, region/provider catalog, time zones and function
definitions were preserved. Separate owner-authorized LiteLLM body expiration,
compaction and Longhorn filesystem trimming restored shared PostgreSQL headroom;
no PVC expansion or unrelated database cleanup was performed. Valid Longhorn
snapshots were preserved and can continue pinning backend storage despite free
filesystem space.

The native rebuild retained NorCal generation 18 (build ID 62), importer **3**, derivation **21**
through resumable checkpoints. A primary-pinned, verify-full TLS operator Job
avoided query-splitting proxy read-after-write lag. Consumed staging structures
were reclaimed only after their last consumer committed. During graph propagation,
deferring the mutable parent index permitted HOT updates; it was rebuilt and
verified valid after propagation. Geometry SQL fences stayed unchanged.

Initial validation rejected apparent park crossings caused by interpolated
coordinates straddling an exactly shared boundary at floating-point precision.
All flagged lines were wholly boundary-aligned within a sub-millimeter envelope.
The corrected observational check exempts only that envelope; PostGIS regression
fixtures still reject real shallow crossings. Completed validation may restart
fully with corrected checks; running validation and geometry-stage fences remain
immutable. The full Go suite, including the disposable boundary/fence integration,
passed. Native validation then reran from initialization, passed every zero-valued
gate and promoted NorCal generation 18 (build ID 62). Canonical indexes and native OSM readiness passed
on the primary and both standbys; APP **31/31** and OSM **10/10** remain unchanged.

Both active accounts' reconciliation campaigns finished. The queue drained with
**1,652 generation-62 current results**. Seven concurrent path-rollup persistence
collisions recovered through the native retry constructor after per-account
matcher admission was reduced to one. Daily/all-time path rollups match an
independent aggregation of their attribution facts with zero mismatches. Three
route updates still hit the graph-layer processing guard after one isolated
native retry each; they preserve prior evidence and remain explicitly failed/stale.
Previously unavailable routes remain unavailable rather than being mislabeled as
updated. Permanent rollup concurrency and dense-graph handling remain follow-ups.
The reported park visit's copied NorCal generation-18 evidence (build ID 62) contains the correctly
attributed inside road piece and an independently unassigned outside piece.

All five services are restored. Temporary Coverage surge replicas/resources are
removed; the per-account admission safeguard remains one pending the concurrency
fix, while global/region/reader limits are unchanged. All **18 configuration
tuples** are preserved, both strict-TLS UI/API origins respond, and only the
canonical Harbor tag is used. Added storage-settling waits were disabled during
the final rebuild; independent logs/catalog/storage observations continued at
five-minute intervals and Coverage observations at three-minute intervals. Owned
monitors, completed importer Job, verification browser servers and local rehearsal
server are stopped/removed after archiving their receipts. Backups and audits are
retained under the protected `deploy-internal` bundles. No commits were made.

## SVG Coverage Route Status Badges

**Deployed 11/9/2026 at Helm 100.** Current Coverage checkmarks, stale clocks and
failed exclamation marks now share the gray-X badge's full-SVG composition. Each
circle and mark uses one coordinate system and theme tokens. Clock hands meet at
the exact circle center; checkmarks and failure marks have explicit path geometry.
Font-glyph nudges and CSS pseudo-element clock hands are removed. All static
route-status marks are non-text; the existing CSS pending spinner remains
non-glyph-based with its reduced-motion behavior intact. Accessible state labels,
tooltips, route focus availability, colors and outer badge dimensions remain.

All **783 UI tests**, type/CSS/CSP/format/build checks pass. Candidate and deployed
Chromium dark/WebKit light checks cover repeated current/stale badges, failed and
unavailable states, and desktop/intermediate/mobile-sheet layouts. Each SVG fills
the badge box with its exact geometric center matching the wrapper center; no
font marks or pseudo-element hands remain. Screenshots use synthetic account data
and actual MapLibre rendering. Only UI rolls; all six non-UI pods and **18
configuration tuples** are preserved while the Coverage refresh continues.
Private receipts: `svg-route-status`.

## Map And Stats Feedback Overlays

**Deployed 11/9/2026 at Helm 99.** Upper Map status feedback is centered on the
canvas and the measured vertical midpoint of the Checked routes/Road coverage
card. Exact-identity feedback mirrors that anchor from the bottom. Card/stage
resize and font completion refresh the measurement; a temporary absent card
retains the last measurement. Existing warning dismissal and exact-detail
Cancel/Retry behavior remain shared.

Report recovery warnings now use the same banner surface, fixed near the lower
viewport and horizontally centered on the report region. They stay visible on
long/scrolled reports without occupying table/pager layout space. With existing
compatible results, a warning is revealed only if it persists for half a second;
initial preparation failures remain immediately actionable. Background failures
retain the mounted rows and Retry path instead of flashing zero/empty results.

All **783 UI tests**, type checking, CSS/CSP guards, formatting and build pass.
Chromium dark and WebKit light verify candidate and deployed assets with synthetic
API data and real MapLibre rendering at wide, intermediate and narrow viewports:
measured upper alignment, mirrored lower anchor, canvas bounds, fixed report
placement during scrolling, unchanged table/pager coordinates, quiet short
failures and successful recovery. No production account state is changed by
these browser fixtures. Exact assets match at both strict-TLS origins; only UI
rolls and all **18 configuration tuples** remain unchanged. Coverage processing
continues throughout. Private receipts: `status-feedback-alignment`.

## Park And Education Boundary Attribution Correction

**Updater published 11/7/2026 at Helm 94; existing-data repair pending.**
Whole-segment containment previously abstained when a physical segment crossed a
park or education boundary, losing attribution for the portion inside. The
existing resumable clipping stages now split at locality/park boundaries and,
for formally unnamed geometry, education boundaries before area attribution.
Inside/outside pieces retain source geometry, original graph endpoints and
adjacent-piece connectivity. Source-ordered edge positions retain repeated
crossings of the same boundary location. Park precedence and named-campus-road
behavior remain intact.

Derivation **21** adds bounded promotion gates for material park/education
crossings, including untagged segments. Real PostGIS regression tests execute the
checked-in clipping, attribution and validation SQL with durable restart cursors:
ordinary/named park crossings, unnamed school crossings, named-school exclusions,
holes, overlapping parks, coincident park/school areas, tangencies, locality
crossings and retraced geometry. They check geometry/length conservation,
connectivity, outside exclusion and detection of deliberately reintroduced
unsplit segments. Missing/nonzero boundary metrics prevent promotion. The full
`go test ./...` suite passes with the disposable clipping integration enabled;
other integration suites retain their explicit opt-in requirements. An isolated
replay of the reported public source geometry also verifies exact inside/outside
attribution using the actual SQL; private details remain outside the repository.

The canonical updater image is published with an SPDX attachment and the existing
runtime/toolchain preserved. A strict-TLS, read-only Kubernetes status Job checks
the pulled immutable image and exact binary/pipeline bytes, then is removed.
Helm **94** changes only the updater reference: all five application pods, their
rendered specs and **18 configuration tuples** remain unchanged. Application
schema/minimum runtime **31/31** and OSM **10/10** are unchanged.

The active OSM generation and copied workout evidence remain unchanged.
Existing results require a new regional build/promotion followed by normal
Coverage reconciliation. Primary storage is below the updater's 10% starting
threshold, so no rebuild, storage resize, active-generation removal or Coverage
repair is started. Storage capacity/remediation requires an owner decision;
publication is not a completed data repair. Private source, replay, image and
deployment receipts are retained in `area-attribution-fix` under `deploy-internal`.

## Shared Fitting For All Range Totals Values

**Deployed 11/6/2026 at Helm 93.** Every headline value uses the same
`FittedAggregateValue` implementation: total workouts, With routes, Duration,
Distance and Energy. Each measures its own available line/grid-column width and
reduces only its own font if needed, bounded by its normal CSS size. Normal-sized
values remain unaffected by overflowing neighbors. Routed counts retain their
right alignment and accessible label. The Duration-only opt-in is removed rather
than copying measurement code into other cards.

All **781 UI tests**, type checking, CSS/CSP guards, build and formatting pass.
Deployed-asset Chromium/WebKit light/dark checks exercise deliberately oversized
values, a short routed count beside a large total, and two overflowing paired
counts. All five independently fit without ellipsis, select different fractional
sizes and preserve the normal size when already fitting. Resize checks cover
adjacent/wide/narrow viewports without page overflow; shorter replacement values
restore CSS sizing. Fixtures are synthetic and verification changes no user data.

Only UI rolls; the other four pods, **18 configuration tuples**, schema/minimum
runtime **31/31** and canonical tag are preserved. Exact deployed assets are
verified at both strict-TLS origins after rollout settling; all five deployments
are healthy. Private receipts are retained in `all-aggregate-values-fit`.

## Map Workspace Retention And Duration Fitting

**Deployed 11/6/2026 at Helm 92.** Stats identity handoffs no longer clear the
focused route, checked set, catalog or valid Road Coverage cache. The signed-in
frame also retains manual base-map override and the last automatic family,
restoring them for the handoff before metadata arrives. Automatic still follows
the focused workout and remains sticky after focus is cleared. Normal Map visits
retain their visit-local override contract; visit-date actions intentionally
focus/add their actual workout rather than resetting an explicit checked set.
The shared exact-detail/single-fit/blink camera flow remains intact.

Summary's Duration value uses a dedicated fitted line without widening any cards.
It measures shaped text at the CSS maximum and chooses the largest fitting
fractional size before paint, remeasuring on value/width/resize/font-load changes.
Only the overflowing line shrinks; shorter values restore CSS sizing. Actual
fractional content width avoids integer rounding that could leave a clipped glyph.

All **780 UI tests**, type checking, CSS/CSP guards, build and formatting pass.
Regressions cover focused/checked state through the actual Shell handoff, sticky
automatic/manual families, existing visit-local behavior and fractional fitting/
restoration. Deployed-asset real MapLibre Chromium/WebKit checks cover focused
and unfocused Automatic/Streets, manual Streets, manual Smooth with a focused
route, and Road Coverage parity. The correct style is used from the first style
request, and checked subsets/focus survive every identity jump.

The synthetic long Duration line fits at **28.57px** versus its normal **30.72px**
near a compact desktop width, then increases by a fraction for a nearby width.
On a wide layout only **0.08-0.09px** reduction is needed from the normal 48px.
Increasing it further would overflow; other cards remain at their normal sizes,
and the shorter average value restores CSS sizing. These are scoped rendering
checks, not a capacity measurement.

Only UI rolls; all five deployments are healthy, both strict-TLS origins serve
the exact assets, and the other four pods/**18 configuration tuples** remain
unchanged. Schema/minimum runtime **31/31** and canonical tags are preserved.
Private source/build/browser receipts are retained in `map-state-and-duration-fit`.

## Direct Identity Camera Flow

**Deployed 11/6/2026 at Helm 91.** Stats identity navigation no longer starts a
default/geolocation or all-routes fit before exact geometry. The signed-in frame
keeps the actual last camera in memory and restores center/zoom/bearing/pitch
when Map is recreated. With no prior camera, wait for detail and initialize at
its exact padded bounds, with no visible world view or second camera fit.

Stats and Road Coverage both invoke the same existing detail/blink implementation
and one linear exact-identity fit rather than a zoom-out flight arc. Fitted-key
tracking is separate from layer/blink restoration, so style events cannot refit
the same identity. Existing capability authorization, cache/abort rules, minimum
extent, padding and readiness-based blink timing remain unchanged. Normal range
and workout-focus fits retain their prior contracts; memory resets with the
private Shell at confirmed session/principal replacement.

All **773 UI tests**, type checking, CSS/CSP guards, build and formatting pass.
Regressions cover saved camera, move-end/disposal capture, cold initialization,
single fits, repeated/style-replayed intents and existing Map behavior. Real
MapLibre Chromium/WebKit checks on deployed assets use controlled delayed detail
responses: cold first zoom is about **15.49**, with no world frame/post-init fit;
warm starts at the retained **zoom 12**, performs exactly one fit and never dips
below that zoom. Road Coverage has identical fit options, one fit and no interim
movement or flight arc. Data/style fixtures are synthetic; these checks make no
backend latency or production-capacity claim.

Only UI rolls; the other four pods, all **18 configuration tuples**, schema/minimum
runtime **31/31** and canonical tag remain unchanged. Exact assets are verified at
both strict-TLS origins after rollout settling; all five deployments are healthy.
Private source/build/browser receipts are retained in `stats-direct-camera` under
`deploy-internal`. Owned isolated browser servers are closed after verification.

## Workout Deletion Confirmation Copy And Audit

**Deployed 11/6/2026 at Helm 90.** Both Summary deletion dialogs use the owner's
approved two-line confirmation with one explicit line break, singular/plural
first sentences and the scoped cleanup/stat-recalculation explanation ending
**This cannot be undone!** Confirmation guards and pending/focus behavior remain.

The deployed purge function matches source (body MD5
`6221f5453cbd6e74b5d329aac518e60d`). A read-only catalog/source audit confirms
explicit provenance deletion followed by workout deletion and cascades for raw
points/routes/chunks, metrics, Coverage facts/attributions/diagnostics/processing
contexts and compact per-workout display facts. Shared aggregates are updated.
Operational jobs/logs, source-file bookkeeping and resurrection-suppression
tombstones remain. Old canonical contexts are invalidated immediately but persist
until later cache GC; original exports and backups are outside the purge.
This corrects the old specification's inaccurate re-import acceptance claim.

All **161 App/Summary tests**, type checking, CSS/CSP guards, build and formatting
pass. Deployed-asset Chromium/WebKit light/dark checks verify the exact two-line
messages, Cancel focus and range DELETE guard without submitting any deletion.
Only UI rolls; the other four pods, **18 configuration tuples**, schema/minimum
runtime **31/31** and canonical tag remain unchanged. Both strict-TLS origins
serve exact assets; private receipts are in `workout-delete-copy`.

## Shared Badge Size Verification

**Deployed 11/6/2026 at Helm 89.** The owner's shared badge font change from
0.66rem to **0.6rem** is now deployed, retaining the shared text offset and other
UI edits. Investigation found no Summary-range-specific override: the previous
live UI still used 0.66rem, while the current source already gave the range,
Workout log weekday and Workout log time-zone badges identical metrics.
No extra local sizing override was introduced.

Deployed-asset Chromium/WebKit checks confirm all three locations use **9.6px**
text, **11.52px** line-height, the same weight/padding, and **17.3125px** badge
height at default root size, in dark/light themes. All **81 Summary/Tooltip
tests**, type checking, CSS guards, build and formatting pass. Only UI rolls;
the other four pods, **18 configuration tuples**, schema/minimum runtime **31/31**
and canonical tag remain unchanged. Both strict-TLS origins serve the exact
assets. Private receipts are in `range-badge-size-fix` under `deploy-internal`.

## Shared Date Badge Text Alignment

**Deployed 11/6/2026 at Helm 88.** A common `.date-badge-text` span/style shifts
weekday/time-zone lettering down **1px** without moving or resizing its badge.
Summary's weekday renderer and shared ZoneBadge use the same rule, including
Data Sync. Original padding, line-height, borders and backgrounds remain intact.
Browser measurement rejected asymmetric padding because its baseline contribution
also moved the outer badge; only the text receives the final relative offset.

All **301 Summary/Tooltip/Data Sync tests**, type checking, CSS/CSP guards, build
and formatting pass. Deployed-asset Chromium/WebKit checks measure **1px** text
movement and **0px** changes to width, height or outer position for Summary weekday,
Summary time zone and Data Sync time zone badges, in dark/light themes. They also
repeat the full menu cursor sweep and range-delete icon/confirmation check.

Only UI rolls; the other four pods, **18 configuration tuples**, schema/minimum
runtime **31/31** and canonical tag remain unchanged. Both strict-TLS origins
serve the exact assets, and all five deployments are healthy. Private receipts
are retained in `badge-text-alignment` under `deploy-internal`.

## Dropdown Cursor And Range Delete Icon

**Deployed 11/6/2026 at Helm 87.** Shared action/radio/checkbox menu items and
styled preference options use hand cursors across their padded rows and child
labels/checkmarks. Disabled variants retain non-clickable cursors and pointer
guards; headings/separators remain noninteractive. The style guide explicitly
covers items inside dropdown panels rather than just their triggers.

The Summary range-delete action uses Font Awesome Free's solid trash-can SVG
inline, inheriting its existing color and retaining its hit target, accessible
label and confirmation. Shared styled help replaces its native title tooltip;
the icon attribution is maintained in the project-root [ATTRIBUTIONS](../ATTRIBUTIONS),
covering Font Awesome Free SVG icons generally without individual icon notices.
There is no external icon/font request or new icon-package dependency.

The **381 App/Data Sync/Summary tests** pass; App/Summary's **161 tests** pass again
after the icon change. Type/build/format and CSS guards pass. Chromium/WebKit
verify avatar, date/base-map, row-context, history filter and styled preference
items plus disabled variants and the confirmation. Its final content is retained
by Helm 88; private build receipts are in `dropdown-hand-cursors`.

## UI Style Guide Consolidation

[The UI style guide](ui-style-guide.md) consolidates cross-cutting design and
interaction reminders from the functional/technical specifications, UI/map ADRs,
readiness and job/source contracts, and the recent review fixes. It introduces no
CSS values or new visual system. Existing feature-specific requirements and
historical decisions remain in place; contributor, architecture, product and
implementation guidance link to the guide. `AGENTS.md` makes reading it and
comparing the closest prior art part of future UI implementation/restyling.

## Summary And Shared Dialog UI Rollout

**Deployed 11/6/2026 at Helm 86.** Summary provenance/deletion dialogs and the
shared Summary/Map/Stats custom-range dialog use Road Coverage's compact inset
and consistent footer spacing. Close buttons use its orange hover border.
Provenance timestamps have Workout log date emphasis and minute precision,
respecting time zone/clock preferences. A common scrollport and sticky header
keep the date's right edge flush with the close button, including long histories.

All **771 UI tests**, type checking, CSS/CSP guards, build and formatting pass.
Chromium/WebKit light/dark checks measure consistent **20px** content/action,
above/below-divider, button-gap and outer insets. Dates match the table font and
weight, omit seconds, and align with **0px** right-edge difference. Long history
keeps its header fixed; all three tabs' custom-range dialogs and narrow layouts
retain their controls. Deployed-asset browser checks use synthetic API data and
perform no user-data mutations.

Only UI rolls; the other four pods, all **18 configuration tuples**, schema/minimum
runtime **31/31** and the canonical tag remain unchanged. Both strict-TLS origins
serve the reviewed assets and all five deployments are healthy. Private receipts
are retained in `summary-dialog-polish` under `deploy-internal`.

## Stats Width And Grouping UI Rollout

**Deployed 11/6/2026 at Helm 85.** The New Coverage table is now half Summary's
Workout log width, with Month/Year widened from 32% to **38%**. Report controls
retain their section spacing, while pagination inherits the shared margin rather
than an extra grid gap. Month/Year requests retain compatible old rows and their
correct header/labels, then update positional rows atomically and shrink/grow to
the accepted result. Account/range/deletion boundaries still clear private data;
late superseded grouping responses cannot replace the active result.

All **118 focused App/Stats tests**, type checking, CSS guards, build and formatting
pass. Deployed-asset Chromium/WebKit checks with controlled delayed responses
measure **0.5** width ratio, **38%** date-column share, and the same **20px**
table-to-button gap as Summary. Both grouping directions retain the first row's
actual DOM node, contain no empty frames and resize only when the replacement
data is ready. Existing report/dialog paging keeps **0px** button/height movement.
Dark/light and narrow viewport checks retain the previous heading/label tweaks.

Only UI rolls; the other four pods, **18 configuration tuples**, schema/minimum
runtime **31/31** and canonical tag are preserved. Both strict-TLS origins serve
the exact reviewed assets. Private receipts are in `stats-half-width` under
`deploy-internal`; browser data is synthetic and verification changes no user data.

## Stats Heading Margin UI Rollout

**Deployed 11/6/2026 at Helm 84.** Restrict the shared zero-margin reset to
Summary's `.section-line h2`, keeping the shared Reports/Summary rule for
typography only. The owner's `.stats-report-nav h2` margin remains
`-0.35rem 0 0.4rem 0`, along with the other UI edits, including Show new visits by.
Live Chrome checks in dark/light themes measure **-5.6px / 0 / 6.4px / 0** at a
16px root size; Summary headings retain zero margins. All **115 focused App/Stats
tests**, type checking, CSS guards, production build and formatting pass.

The UI-only rollout preserves the other four pods and all **18 configuration
tuples**, keeps schema/minimum runtime **31/31** unchanged and serves exact assets
from both strict-TLS origins. The canonical UI tag matches the deployed digest;
all five deployments are healthy. Private build/browser receipts are retained in
`stats-heading-margin-fix` under `deploy-internal`.

## Stats Review, Navigation And Refresh

**Deployed 11/6/2026 at Helm 83.** The review follow-up aligns Reports with Summary
section typography and hand cursors, removes redundant explanatory copy, reuses
Map's segmented Month/Year controls and centers the title and table. Shared
Summary width tokens set the table to two-thirds width, capped by the available
content; controls align with its left edge. The three vertical gaps use one
token. The past-year header has the exact requested delayed two-line styled help.
Count/name/date actions use normal text, orange-on-hover and hand cursors, without
underlines or cell-level backgrounds. Dialogs have the smaller clamped heading,
an intact top border, Road Coverage date formatting and no redundant visit caption.

Both tables now keep compatible rows mounted at full opacity until replacement
pages arrive and reserve `min(totalItems,pageSize)` slots, including a short last
page. They retain page-button positions and document/table scrolling; small total
or filtered results can still shrink. Account/range/group/deletion-context changes
remain isolated from this continuity state.

Names switch to Map production Coverage and use the existing exact identity fit/
blink flow. In-range New visit/Previous visit links focus the actual workout,
select it and scroll it to the list center when possible. Returning restores the
active report/page/sort. **Out-of-range previous dates are plain text**: no path
expands or overrides the main date range. Explicit Stats navigation suppresses
diagnostics only for that Map visit, without changing the saved preference.

The detail API now returns qualifying/predecessor workout IDs. The new API-only
`app.stats_visit_workouts` helper uses authorized full-key history probes after
page LIMIT/OFFSET and deterministic same-date start-instant/ID ties. It belongs
to source 030; source/schema/minimum runtime stay **31/31/31**, baseline **27**,
ledger **0-31**. Its guarded adoption preserves **18 existing business/ledger
inventories**, adds no jobs and changes no existing data rows or function contracts.

Idle behavior is repaired separately from authentication: visible refresh runs at
the configured 10-60-second cadence (normally 30 seconds), while report scopes
last at most 30 minutes and are clipped to the session deadline. Expiry/404
renews the scope; transient preparation failures now retry with bounded backoff
and on the normal visible/focus/online recovery path. A failed renewal no longer
leaves Stats unable to recover until remount. Last-validated rows remain visible
but stale actions are disabled until fresh authorization/data arrives.

Verification:

- **767 UI tests**, Go suite/vet, generated-code checks, type checking, production
  build, formatting and CSP/dialog/map CSS checks pass. Tests cover delayed-page
  continuity/spacers, header help, date formatting, one-shot Map intents, all
  three navigation actions, unchanged range and parent report/page restoration.
- Native PostgreSQL scenarios validate exact new/predecessor workout IDs,
  same-date ties, out-of-range metadata and foreign-session denial. The expanded
  catalog mutation matrix and fresh migration through 031 pass, including actual
  restricted API/general/Coverage-worker readiness.
- The protected actual-backup-restore oracle matches both rollups, every nonzero
  detail page and every qualifying/predecessor workout target across two ranges:
  **394 queries**, maximum observed read approximately **119 ms** in that restore.
- Chromium/WebKit, light/dark desktop previews measure exact **2/3** width and
  **20px/20px/20px** gaps at default scale. Frame sampling sees no empty table
  frames during either pager and **0px** page-button/table-height movement on
  short final pages. Tablet/narrow previews have no page overflow; the tooltip
  delay, exact two lines, cursors, text-only hover and out-of-range dates pass.
- Both engines pass against deployed assets/APIs and real basemaps: exact road
  blinking, new/previous workout focus/check/centered-list scrolling, unchanged
  range, report-page restoration and plain out-of-range previous dates. An owned
  report scope is deliberately expired while authentication remains valid and
  one preparation response is made transiently unavailable; Stats recovers in
  place, without blank rows, logout or a tab switch. Saved preferences stay intact.

All five deployments are healthy; both strict-TLS origins serve the exact UI
assets. Canonical Harbor tags match deployed immutable references; all **18
configuration tuples** and Martin are preserved. Verification jobs, owned browser
servers/sessions/scopes and the local test server/proxy are cleaned up while
protected backups/clones remain retained. Private receipts/screenshots live in
`stats-review-031` under `deploy-internal`. At this checkpoint, these scoped checks
did not close M9 without owner review or certify the M11 device matrix/production
capacity. Subsequent owner acceptance is recorded in [M9 closeout](#milestone-9-closeout).

## Stats And New Coverage Initial Implementation

**Deployed 11/5/2026 at Helm 82; owner review was pending at this checkpoint.**
Subsequent acceptance is recorded in [M9 closeout](#milestone-9-closeout). Backend implementation
and native database/API verification preceded the UI. Stats sits between Map and
Data Sync; its Map-style sidebar has the shared Summary/Map/Stats date picker,
Reports navigation and the existing magenta selected border. Report selection,
Month/Year grouping, sorting and paging live in the parent frame across tabs.

New Coverage counts named canonical identities using all-history first/previous
visits before inclusive date filtering. It fills every intersecting month/year,
including zero and partial boundary buckets. First-ever visits enter both
metrics; later past-year visits require a gap strictly longer than the previous
visit's calendar-year anniversary, with February 29 clamped to February 28.
Nonzero cells are whole-cell actions opening compact, searchable, sortable
qualifying-event dialogs. Previous visit uses the Summary table's grayed-out
**n/a** for first visits; Escape restores focus to the invoking action.

Four Stats API routes prepare/release an expiring account/session-owned report
scope and read paged bucket/detail results. Source 030 adds one private forced-RLS
scope table and six narrowly granted helpers, reusing canonical precomputed visit
history. Active scopes pin contexts independently of Map; reads use generation/
policy validation and the same read-only RR snapshot. No Map capability, tile,
geometry, source-provider request or matching job is needed for Stats.

**Cold-context follow-up:** the first live read gate found a planner failure
absent on the restored database and warm contexts. For a new context estimated as
one row, PostgreSQL moved canonical equality into nested-loop join filters and
repeatedly scanned the context's identity set, exhausting the read deadline.
The corrected event helper scans history once, materializes qualifying events
and uses a full-key correlated metadata index probe. A native plan regression
checks the canonical index condition. Three independently verified, never-built
live contexts subsequently returned **200**: preparation **2.0-4.7 seconds**,
summary reads **39-87 ms**. These are scoped observations, not capacity acceptance.

Verification:

- **755 UI tests** pass, including actual Shell tab order/persistence, shared date
  picker regressions, whole-cell actions, n/a, dialog search/sort/focus, stale and
  late response fences, bounded renewal and hidden-tab polling. Type checking,
  production build, formatting, CSP and existing CSS guards pass.
- Native PostgreSQL API scenarios cover first/repeat/return histories, leap-day/
  exact-anniversary rules, out-of-range predecessors, park/education/source-member
  deduplication, zero buckets, pagination/search and count/detail parity. Foreign
  sessions/accounts, expiry/revocation, context GC pins, failed replacements and
  changed generations are verified; the new cold-plan regression passes.
- The restricted-role catalog mutation matrix passes for the new private table
  and helpers. Fresh source migration and API/general/Coverage-worker readiness
  preserve source/schema/minimum runtime **31/31/31**, baseline **27**, ledger
  **0-31**; no source migration 032 is introduced.
- An independent calendar-history oracle on a protected full actual-application
  restore matches both rollups and every nonzero drill-down over two ranges:
  **394 summary/detail queries**, with maximum read approximately **99 ms** after
  the cold-plan fix. Names, coordinates and account identifiers stay private.
- Chromium and WebKit synthetic previews cover light/dark desktop, tablet and
  narrow viewports without page overflow; both engines also pass live deployed-backend
  count/detail, first-visit/previous-date/n-a, search, compact sizing, Escape/focus,
  tab restoration and tablet checks. These are not the full M11 device matrix.

Operations:

- Retained a protected **1.65 GB** application backup and restored it before the
  guarded adoption. Existing function owners/ACLs and **18 business/ledger table
  inventories** are preserved in both the initial adoption and function follow-up;
  existing data rows, generations/revisions, jobs and saved preferences are unchanged.
- Helm 81 delivered API, both shared-worker consumers and UI. Helm 82 rolls only
  API/general-worker/Coverage-worker for the plan/readiness change, preserving the
  UI and Martin pods/images. All **18 configuration UID/resourceVersion/content
  tuples** and both strict-TLS origins are preserved; all five deployments are healthy.
- Canonical Harbor `dev-20261102` tags match deployed digests. OSM content is
  unchanged. Owned verification jobs/browser servers/sessions/scopes are cleaned
  up, with receipts and actual-data screenshots retained only in protected storage.

The current private bundles are `milestone9-stats-031` and its final
`milestone9-stats-cold-plan-031` follow-up under `deploy-internal`. This initial
implementation was ready for owner review, not itself a declaration of M9
acceptance; the later [closeout](#milestone-9-closeout) records acceptance.

## Empty Coverage Results

**Deployed 11/5/2026 at Helm 80.** Source 030 persists the applied display result
as `matched` or `no_matches`, with null reserved for unapplied state. The map API
exposes `no_matches` explicitly. Production Coverage disables empty-result row
actions, clears misleading persistent focus, uses the neutral gray circle/X and
shows **No map segments matched** through the shared portal tooltip. Raw Routes
remain selectable, and a requested empty Coverage workout falls back to raw mode.
No empty focus fetch or camera fit is started. A completed empty evaluation remains
successful/current for durable job and reconciliation semantics, avoiding endless
retry solely because it found no matches.

Applied-proof changes enqueue the shared deferred display coordinator even when
a first empty job writes no match rows. Classification and bounds update together;
replacement with nonempty evidence restores `matched`, while failed replacement
retains prior evidence and classification. Genuine worker/PostGIS integration
tests verify first empty completion, no redundant work, empty-to-matched-to-empty
transitions, shared-copy retention, and persistence-failure rollback. The compact
projection parity and restricted-role catalog-mutation tests also pass. All 741
UI tests pass, including both empty legacy and explicit-result behavior and all
72 readiness combinations with and without renderable geometry. The Go suite,
generated-code checks, type checking, formatting, CSS guards and build pass.

A guarded classification-only update was rehearsed on the protected actual-backup
restore and then applied to existing results. Fourteen original table inventories,
including original state fields, raw/Coverage geometry, source data, job state,
generations, revisions and ledger, pass preservation checks; function owners/ACLs
are retained. Source inventory stays **001-031**, schema/minimum runtime **31/31**,
baseline 27 and ledger 0-31. No ingest, provider, OSM or production matching job
is started by the repair.

Live Chromium and WebKit verify every affected route against the actual API/UI
and real map resources: persisted status, gray X, disabled action, exact visible
tooltip, no empty focus fetch/fit, and continued raw-route selection. Saved
preferences remain unchanged; temporary sessions/selections and the verification
Job are cleaned up. Both strict-TLS origins serve the published assets, all five
deployments are healthy, all 18 configurations are preserved, and Martin is
unchanged. The existing canonical image tag is reused without review aliases.

## Mode-Specific Route Bounds

**Deployed 11/5/2026 at Helm 79.** Raw GPS bounds remain separate from four
nullable Coverage extent fields on the applied-result state. The existing
deferred display coordinator maintains these from compact path/park evidence,
including name qualification and evidence removal, without rescanning GPS or
changing matching. The API returns per-workout and checked-selection Coverage
bounds. Explicit/restored focus, focused production-mode changes and Fit routes
use the appropriate mode's box; missing Coverage bounds never fall back to raw.
The [functional specification](functional-spec.md#mode-specific-route-fitting)
records the camera and empty/stale-result behavior.

The schema addition is folded into source 030. The current source inventory and
supported/schema/minimum runtime remain **001-031 / 31/31/31**, baseline 27 and
ledger 0-31. A protected full application backup was restored privately before
the guarded operator update was applied to every account. Original state fields,
raw routes, matching evidence, display facts, jobs, sources, preferences,
generations, revisions and ledger values passed preservation checks; function
owners/ACLs are retained. All-account stored bounds agree with an independent
full matching-geometry oracle, including null extents for empty evidence. No
import, provider access, OSM update or rematching is triggered.

All 734 UI tests, `go test ./...`, generated-code checks, type checking,
formatting, CSS guards and the production build pass. Targeted actual
PostgreSQL/PostGIS tests cover deferred projection parity, metadata changes,
restricted-role readiness/mutations and API selection isolation/membership.
The membership fixture explicitly supplies drawable components instead of
depending on old synthetic GPS jumps exceeding the spatial continuity limit.
Live Chromium and WebKit verify the owner-reported route with real basemap and
private resources: distinct boxes, padded viewport containment, row/card/union
fits, both mode transitions, and no capability renewal or saved preference writes.
Owned browser sessions/selections and the verification Job are cleaned up.

Only API, both shared-worker consumers and UI roll; Martin remains unchanged.
Both strict-TLS origins serve the exact published assets, all five deployments
are healthy, and all 18 configuration fingerprints are preserved. Harbor's
existing canonical tag is reused without dated/review aliases. These are scoped
functional and preservation checks, not a new capacity or latency certification.

## Route Checkbox Tooltip Focus Restoration

**Harbor cleanup 11/4/2026:** all four application repositories retain their
current release image and its current SBOM/provenance artifacts. Twenty-one
obsolete artifacts and two extra aliases are removed. `dev-20261102` is the only
remaining image tag in each repository, matching the current deployed references;
the UI's additional `dev-20261104` alias is removed. Helm revision 77 and all
running digests remain unchanged and all five deployments are healthy. Registry
garbage collection is not triggered by this scoped artifact/tag cleanup.

**Deployed 11/4/2026 at Helm 77.** Pointer Apply/Cancel returns focus to the
bulk checkbox without reopening its help. This checkbox opts into keyboard-only
focus activation; hover remains available, pointer leave dismisses it, and
keyboard Apply restores the keyboard help. Other tooltip callers retain their
existing focus behavior. Three regression tests reproduce both mouse failures
before the fix and retain keyboard return behavior.

All 302 focused Map/checkbox/tooltip tests, type checking, CSS guards, formatting
and the production UI build pass. Chromium and WebKit candidate checks verify
both viewport sizes against the live backend with real map resources. Live
desktop/mobile Chromium repeats mouse Apply/Cancel, delayed no-reappearance,
mouse movement, hover re-entry/leave and keyboard Apply checks against the
deployed image. Saved preferences remain unchanged and owned verification
capabilities and the temporary session are cleaned up. Only UI rolls; all five
deployments are healthy, both strict-TLS origins serve the published bytes, and
all 18 configuration fingerprints and four non-UI pod identities are preserved.

## Route Checkbox Tooltip Offset

**Deployed 11/4/2026 at Helm 76.** The revised three-line checkbox help text is
preserved, and only that portal tooltip moves up 2px. Type checking, formatting,
28 focused checkbox/tooltip tests and the production UI build pass. Live
desktop/mobile Playwright checks verify the exact -2px placement, three rendered
lines, unchanged picker anchoring and real basemap rendering without resource
stubs or saved preference changes. Only UI rolls; all five deployments remain
healthy, all 18 configuration fingerprints and four non-UI pod identities are
preserved, and both strict-TLS origins serve the exact published assets.

## Route Picker Timing, Alignment And Help

**Deployed 11/4/2026 at Helm 75.** The type-picker hold threshold is 500ms, and
the panel's top edge aligns with the scrolling routes list while retaining the
full frame's left edge and width. Other dropdown offsets remain 12px. The native
checkbox title is replaced with the shared portal tooltip, using 16px text and
the requested three-line pointer/keyboard guidance. The actual checkbox links to
the description; no extra tab stop is introduced, and help hides while the picker
is open.

All 725 UI tests, type checking, CSS guards, formatting and the UI build pass.
Chromium and WebKit candidate checks verify both desktop and mobile geometry,
the three rendered tooltip lines and real basemap rendering. Authenticated live
Chromium repeats those checks against the deployed image without resource stubs
or saved preference changes. The rollout changes only the UI image, preserves all
18 configuration fingerprints and four other pod identities, and leaves all
five deployments healthy. Both strict-TLS origins serve the exact published
assets; owned verification capabilities and the temporary session are cleaned up.

## Dropdown And Keyboard Focus Polish

**Deployed 11/4/2026 at Helm 74.** Only the UI image changes. The existing global
tag and all other image pins, resource/scheduling settings, database schema and
18 configuration fingerprints are preserved. API, general-worker,
Coverage-worker and Martin pod identities and restart counts remain unchanged.

Focus rings follow keyboard navigation rather than pointer focus. Preferences
uses accessible styled selects aligned with the trigger's left/top edges and
width. Summary/Map date menus and the Map base-map menu match their controls and
open 12px below them. The one-second workout-type picker matches the full route
frame, uses equal 12px action insets/gap, and stays in the mobile dialog's
accessible subtree. Data Sync Operation/Status panels share the full filter-row
anchor and width; the control gap matches the surrounding card insets.

The 724-test UI suite, type checking, CSS checks, formatting and production build
pass. Chromium and WebKit candidate checks pass at desktop and mobile sizes with
the live backend and real basemap resources. Authenticated Chromium checks repeat
the geometry, keyboard focus and map-rendering verification against the actual
deployment without UI, worker or tile stubs. Both strict-TLS origins serve the
exact published UI bytes. Verification leaves saved preferences unchanged and
removes its owned map capability and temporary session.

## Custom Date Ranges And Workout Type Selection

**Deployed 11/2/2026 at Helm 73 using `dev-20261102`.** The account lifecycle
foundation includes the bounded recent custom date range list and its 1-5
preference (default 3). The source/schema/minimum runtime remains 31/31/31,
baseline 27, ledger 0-31. A guarded, rehearsed update installs the same preference
columns on the existing database and seeds an existing explicit default range
without changing prior preference values, account generations or functions.

Summary and Map share the persistent unique MRU list below **Custom...**; an
explicit range keeps its checkmark on **Custom...**. The Map bulk checkbox retains
normal all/none behavior and opens **Show workout types** after a 1.5-second hold.
Its dropdown aligns with the checkbox and uses the other dropdowns' 8px gap.
Draft type selections apply atomically; compact Cancel/Apply actions, outside
click and Escape retain normal dropdown behavior.

Fresh native installation through 031, account persistence/concurrent update
tests, restricted API/worker/Coverage-worker readiness, the Go suite and vet,
the UI build and generated contract checks pass. The 718-test UI suite and
desktop/mobile browser proof cover the unchanged UI source included in this
release. Both strict-TLS live origins serve the exact published UI bytes. All
five deployments are healthy and all 18 configuration fingerprints are
preserved; only four intended deployment images change.

Authenticated live desktop/mobile UI checks pass for the history menus, 1-5
preference choices and the type dropdown's unchanged checkbox state, exact
left-edge/8px anchoring and compact actions. This is a DOM/control check with
public provider resources and private tile bodies stubbed, not a new map-rendering
performance claim. Existing preferences remain unchanged; the temporary owned
session, map capability and verification Job are cleaned up.

## Consolidated Source And Tooltip Scroll Ownership

**Deployed at Helm 72 using `dev-20261101`.** Raw spatial continuity is defined in
009 and used by 010; the complete tile pipeline is `031_tile_rendering.sql`.
Current startup accepts only schema/history 31 and its exact object/privilege
profile. The fresh native installation and guarded PostgreSQL geometry/auth/count
tests passed. A protected-data rehearsal verified catalog reconciliation and exact
checkpoint recovery before the coordinated live cutover.

The live function catalog matches the fresh reference. Business table identities,
account generations and retained ledger rows were unchanged; sequence high-water
was preserved. The completed route repair was not repeated. Matching API, worker,
Coverage-worker and UI consumers are healthy. Martin restarted with only current
canonical sources while retaining its image, pool/cache and resource settings.
All 18 configuration fingerprints and application sizing settings are unchanged.
Both strict-HTTPS origins match the frozen UI distribution.

Portaled tooltips now have one active owner. Captured scroll rechecks the element
under the remembered pointer, so disabled rows cannot retain stale hover bubbles.
Scroll/resize handlers cannot reactivate a superseded tooltip; keyboard-focus and
same-trigger hover overlap remain supported. The actual authorized wheel-scroll
reproduction changed from accumulating stale bubbles to at most one, with none
remaining after leaving the list. This is separate from latency/capacity acceptance.

Full **708 UI tests**, including **271 MapPage** and **14 Tooltip** tests, and the
full Go suite passed. Current images and their provenance remain under the new
tag; explicitly requested registry cleanup removed non-current artifacts and old
tag aliases after successful deployment. Historical image rollback availability
must not be assumed after that cleanup. No broad registry garbage collection was
triggered. Detailed operational evidence stays private.

## Raw Route Continuity And Overflow Tooltips

The current raw-route policy belongs to 009/010 and is deployed through the
normal fenced ingest summary writer. Original observations, file hashes and
Coverage matching versions are retained. [ADR 0015](decisions/015-route-continuity-and-preserved-observations.md)
explains why spatial splitting follows, rather than resets, temporal components.

An explicitly approved account-scoped scan found timestamp-baseline blind spots.
The selected repair was rehearsed against private derived data, then applied in
an atomic guarded transaction with geometry preimages retained. Original point
row hashes and workout metadata stayed unchanged. Independent edge-multiset
comparison retained every non-gap edge; exact and LOD chunk projections matched
their builders. An account-wide derived-geometry verification found no remaining
connections over the spatial limit. Already proven Coverage evidence/states and
unstarted/no-evidence states were preserved; no provider reimport or unnecessary
Coverage campaign was started. Detailed account/date/location manifests remain
private. Other accounts are not claimed as historically backfilled.

The focused raw renderer applies the same spatial limit to its original-point
response. Authenticated browser checks on the reported cases verified loaded,
visible focus sources without oversized connections. Separate actual authorized
tile probes checked eligible gap-only locations; a location also containing valid
observed geometry was not falsely required to be empty. These are continuity
checks, not latency/capacity certification. Runtime providers/styles and isolated
software-browser limitations are retained in private evidence.

Road Coverage values remain inside padded cells. Styled full-text tooltips appear
only for truncated values, use a single line, and paint above the modal dialog.
An actual authenticated before/candidate/deployed hover reproduced the previous
stacking failure and verified the correction at desktop and narrow widths.
Temporary verification sessions/selections and the native operator Job were
cleaned up. All 18 configuration identities/content fingerprints and application
resources remain unchanged; both strict-HTTPS origins match the frozen UI assets.

Source checks passed **706 UI tests**, including **271 MapPage tests**, full Go
tests, vet, CSS guards and production typecheck/build. Isolated real PostgreSQL
tests covered pure splitting, source preservation, idempotence, stationary runs,
private ACLs and the real fenced ingest writer. Historical milestone acceptance
and raw-rendering performance limitations remain separate.

## Tile Rendering And Delivery (031)

The complete current tile contract is consolidated in 031. Historical Helm-67
delivery measurements retain their checkpoint scope; completeness was observed,
but dense high-zoom raw completion was not a latency acceptance. Those results do
not define an alternate runtime or migration profile. OSM remains independent.

The [architecture](architecture.md#tile-rendering-and-delivery-031) records
whole-component LOD bands, cold/generic-plan bounded Coverage probes, display-precision
deduplication, the public-API browser protocol scheduler, and four-encoder streaming
gzip with final authorization after admission. Canonical counts and exact detail/
focus remain unchanged. Synthetic compression alone reduced **373,507 to 49,879
bytes (86.6%)**; this is not a final live latency measurement.

**Recorded source verification:** **701 UI tests**, including **267 MapPage and
46 scheduler tests**; **9/9 native MapLibre 5.24 / Chromium 151 cases**, including
40 delayed tiles whose admission exceeds 15s. Full Go tests, API/config races,
**24 guarded authorization cases plus eight blocked-encoder invalidations**,
vet, CSS and TypeScript/build checks passed. Generated consistency remains stable;
there are no new public OpenAPI property changes after the 65 contract. Guarded
031 SQL verification covers loops, fresh/upgrade/rollback, auth/ACL and cold plans.
Incomplete actual-clone Coverage authorization/extracted-query observations are
not a complete endpoint benchmark or an additional accepted test pass.

**Sanitized live finding:** the earlier authorized pre-fix normal/4K viewport reproduction
exposed server admission/SQL failures and exhausted the old source-wide retry
budget. 66 removed the observed HTTP errors in that run but exposed premature
15s browser **queue** expiry. 67 separates bounded queue wait from service time
and adds bounded compression. The final canonical run reached every target camera
and loaded every required tile at settlement, with no observed HTTP/non-abort
transport errors, client deadlines or required errored tiles. Coverage 4K early/
settled observations were complete; **dense high-zoom raw client completion was
still slow, not a latency pass**. Server time and browser time are not fully
decomposed; the remaining observation is not evidence of slow SQL. Existing CPU/
memory evidence did not justify raising resource caps. Account/location/date/ID
details, timings and screenshots stay in private ignored storage.

The run was not repeated until green, with no cache flushing or manual scope/source
reload. Ordinary background Coverage renewal retained identical context. Expected
mode/zoom aborts are not delivery errors; unavailable bodies remain unknown, not
empty. Gzip reduced estimated wire transfer, with header overhead not isolated;
matched high-zoom responses retained decoded bytes and feature/layer counts. Neither
compression-latency improvement nor raw viewport p95 improvement is established.
Scheduling stalls extended nominal observation windows, precluding exact-window
or time-to-usable-render acceptance; software/remote-browser and CPU causes remain
unisolated. Owned selections/session were cleaned up and preferences/account/source
generations preserved. See the [scoped final result](coverage-acceptance-results.md#tile-delivery-status-031).

M8 remains closed; this is not M12 capacity acceptance or a universal production
SLO. Dense raw client completion remains the next investigation, not a demonstrated
need for more server resources or changed request-rate/timeout/body limits. No
benchmark/test rerun, database call or operational execution occurs in this
documentation task.

## Tile-Local Map Optimization (031)

**Historical Helm-65 checkpoint; the foundation remains deployed.** Current
schema/delivery status is recorded in the [031 follow-up](#tile-rendering-and-delivery-031).

**All six optimizations implemented and deployed for both maps at Helm 65.**
The user-approved forward-only [031](database-migrations.md#tile-local-geometry-031) raises source
`SupportedSchemaVersion`, schema and minimum runtime to **31**, retaining baseline
**27** and ledger **0-31**. Historical **001-030 are byte-identical**. At that checkpoint live was
**Helm 65 / APP 31/minimum 31 / OSM 10/minimum 10**, retaining the revision-64 UI
fix and unchanged SVG. Historical
pre-squash `031_ingest_reprocessing` is unrelated to this new tile migration.

The [architecture](architecture.md#tile-local-rendering-031) records projected private
chunks, local clipping/LOD, source-scoped Coverage deduplication, independent exact
focus, bounded UI geometry caches, and API payload cache/coalescing/admission.
Canonical whole-range counts, original GPS/evidence and exact Coverage-focus
GeoJSON are unchanged. Default occupied delivery slots remain four with a separate
six-body-job ceiling even when configured concurrency allows 16. New private Helm
settings and tile-role SQL defaults are installed; Martin caching stays off.

**Recorded local verification**, using guarded synthetic loopback PostgreSQL
**18.6 / PostGIS 3.6.4**, passed native fresh and 30-to-31 migrations, transactional
rollback/forward-only history, partial/reversed/overlapping evidence, gaps/dots,
duplicate workout separateness, buffers/LOD, canonical-context invalidation and
runtime ingest fences. Catalog readiness verification recorded **792 cases**.
Actual tile-role `pg_sleep` and lock-wait checks verify the **4s SQL statement /
250ms lock** bounds; HTTP abort is not proof of instantaneous SQL cancellation.

Existing API/config full race checks and new cache-authorization integration
checks passed, including blocked-transport/slow-writer body-slot lifetime tests.
Final-source verification passed **651 UI tests**, including **263 MapPage tests**,
the CSS guards, production build, full-repository Go tests and vet, generated
OpenAPI consistency, Helm tests and chart checks. The build retains its chunk-size
warning. OpenAPI focus metadata now describes the actual canonical/source
properties, and signal cancellation reaches the tile-service lifecycle. Database
integration checks ran separately against explicit disposable endpoints; passing
the ordinary Go suite does not mean every opt-in operational fixture executed.

**Approved deployment receipt:** native migration under the actual role took
**50.77s**, Job **67.65s**, bounded by **90s statement / 500ms lock**. Existing Goose
tuples were preserved and 031 appended at row **55**, high-water **55**, without
reset. Native full readiness passed under the actual API/general-worker/Coverage-worker
roles before writers started and after rollout. Fresh actual tile login verified
`row_security=on`, **4s statement / 250ms lock / 5s idle transaction** limits.
Martin's controlled zero-to-one restart refreshed connections while preserving
its **1.15.0 pin, pool 4 and cache off**.

Three new API/UI/shared-worker images under the original `dev-20261020` tag serve
four consumers. All five application Deployments returned to their original
**one replica**, healthy with **zero restarts** at verification. Separate OSM pins,
settings and pod identities were unchanged. Of **18 configuration specifications**,
**17 are unchanged**; the API ConfigMap retains its UID and adds only the seven
tile settings. The UI checksum changed intentionally through the shared API
template hash; actual nginx ConfigMap bytes are unchanged. Runtime packaging
permissions were corrected without changing dependency versions or asset bytes.

Authoritative business-data fingerprints across **95 tables / 305 buckets** were
unchanged; raw GPS was **not dumped, scanned or hashed** by this proof. Actual-data
native Up rehearsals took **40.866s / 45.053s**. The bounded consistent read-only
snapshot excluded raw points and was **not a full backup**. Guarded pre-writer
catalog-checkpoint restoration passed twice at approximately **152s**, restoring
the exact old catalog without `CASCADE` and preserving sequence high-water.
**No recovery ran live; resumed writers make that pre-start checkpoint ineligible
now.** It is not an ongoing rollback guarantee. Actual dataset and storage metrics
remain private; [migration recovery limits](database-migrations.md#tile-local-geometry-031) apply.

Both strict-HTTPS origins served HTML and all **eight frozen assets** with matching
hashes. No owner UI login, session, selection or provider was created for this
verification. **Authenticated tile smoke and viewport capacity are not claimed in
this deployment record.** Actual signed-in browser validation, parallel bursts,
p95, initial usable render and focus latency remain separate gates, not missing
implemented optimizations. The [synthetic comparison](coverage-acceptance-results.md#tile-local-source-verification-031)
still proves cardinality, not endpoint p95 or a speed multiplier. M8 remains
closed with its original acceptance/browser receipt; no new M8 pass or M12 capacity
certification is implied. This documentation closeout performs no operational
execution and does not rerun the recorded tests.

## Metadata-Driven Coverage Reconciliation

**Implemented and deployed at Helm revision 62; initialization and live
unchanged-cycle acceptance PASS.**
[ADR 0014](decisions/014-metadata-driven-coverage-reconciliation.md) records the
design and motivation. This is an additional behavior change, not just the
source-history mapping; deployment verification is not steady-idle acceptance.

APP **029** now includes independent selection and successful applied-match proof,
exact point-mutation tracking, transaction-coalesced account wakes and queue-only
recovery that preserves campaign cursors. Metadata filtering precedes GPS; valid
selection permits generation-only rebinding without GPS, while real selection/input
changes still receive bounded resolution. Matching identity includes immutable
rules, sampling, path policy, minimum traversal and production contract. Only
actual successful `applied`/`no_evidence` persistence stamps applied proof; exact
retained successful history can initialize it, never invent an old negative
catalog baseline. Unknown initial selection proof may cost one bounded bootstrap.

The default **30-second idle catalog-head/claim poll** does no GPS/ROI or unchanged
observation DML. The former **24-hour scan no longer triggers work**. Executor
claim/preflight/health queries remain; near-idle is not zero database traffic.
Neither this cadence nor its acceptance target is the UI's 10-second Running-job
poll. Necessary queued bootstrap/input/catalog/matcher updates must drain before
assessing application-attributed idle CPU; no arbitrary five-minute guarantee is
claimed. Failed/canceled targets keep existing retry/cancellation authority.

Separate OSM **010** adds transactional singleton catalog-head proof and generated
boundary SHA/area at catalog-write time. The full fingerprint remains legacy
compatible; selection fingerprint covers all catalog IDs/configuration/boundaries
and configured-active presence but not replacement generation numbers. Unchanged
catalog/head updates are suppressed. Replica event/revision floors and one
Repeatable Read snapshot reject stale observations without strict-policy fallback.
See [migration contract](database-migrations.md#metadata-driven-coverage-reconciliation-029-and-osm-010).

**Verification:** the selected real PostgreSQL matrix passed: baseline run
**25 top-level / 152 subtests**, then review run **26 top-level / 45 subtests**.
These are separate, overlapping run scopes, not counts to sum as unique cases.
The campaign-counter correction additionally passed **14 top-level / 29 subtests**
in its own overlapping PostgreSQL run.
Full **`go test ./...` PASS** is also recorded. Isolated verification used
PostgreSQL **18.6**. Actual OSM native Up on PG17 and primary/two-standby **10/10**
verification passed, but do not establish a full PG17 matrix or completion of all
historical races. This documentation task does not rerun those tests.

The approved review fixes preserve a due N+1 campaign when N completes, fence
`selectionPolicy`, and defer only typed route-local ROI failures for five minutes
under mutation/input CAS, with a 30-second default ROI budget. Other routes keep
progressing; systemic/cancellation/lag failures are not swallowed. Readiness checks
the exact primary-key/index contracts of all four private tables. See
[failure semantics](decisions/014-metadata-driven-coverage-reconciliation.md#failure-semantics-and-readiness).

**Initial cutover:** Helm **61**, APP schema/minimum **30/30**, baseline **27**, ledger
**0-30**, retaining original ledger rows and Goose row-ID high-water **54**;
OSM schema/minimum **10/10** on primary and two standbys. Native API/shared-worker/OSM
images use the original tag; UI/Martin are unchanged, all **18 configuration
specifications** are preserved. No manual application jobs or provider operations
were started. The higher-numbered APP history cutover is recorded separately in
the private operator bundle, not implemented as runtime compatibility branches.

**Current correction:** Helm **62** allocates genuine campaigns above every
global/account revision, avoiding a legacy completed counter that could otherwise
mask new initialization work. A separately recorded one-time metadata update
schedules only accounts with missing initial proofs; healthy accounts are not
reset. Schemas, ledger, existing application data and configuration specifications
remain unchanged. Its selected PostgreSQL regression scope is recorded above.

**Preservation boundary:** before restarting writers, recorded checks verified
original context/region projections, roles, sequence states and old-table counters,
including zero forbidden APP old-table DML in the owned transaction. This is
bounded metadata/projection proof, not a full raw-data backup or hash, nor a blanket
data-loss guarantee. Fallback image pins were retained at deployment; older Harbor
artifact deletions mean that receipt cannot establish current registry-wide
rollback availability. Reverify actual artifacts before relying on rollback.

**Initialization and bounded unchanged-cycle acceptance PASS.** Canonical
metadata, checked before and after the probe, proves every eligible route has
valid current selection proof. All campaigns are complete and unclaimed, with
no active application jobs, dirty keys, pending handoffs, prepared work or wakes.
The existing initializer completed without a duplicate pass.

An authorized temporary native operator invoked the exact deployed
`NewCoverageReconciler(...).RunOnce` three times with fresh reconcilers, actual
restricted worker credentials and unchanged production options. Each returned
`worked=false`; only the expected catalog/control operations were traced, with
no GPS snapshots, canonical hashes, ROI, coverage-context or job-creation queries.
Row/xmin fingerprints and settled APP/OSM table counters show no row changes.
Connection/standby checks below pgx tracing are not included in the traced query
count, and shared statistics/CPU are not attributed exclusively to the operator.
No forced campaign or optional all-route-query timing is claimed. See the
[repeat procedure](decisions/014-metadata-driven-coverage-reconciliation.md#repeatable-unchanged-cycle-verification).

Services, pods/images, configurations and schemas remained unchanged; the native
probe, staged TLS files and probe connections were removed. This verifies bounded
unchanged-state behavior, not map scaling or production capacity. Existing
M8 and historical 34/60/57 receipts remain untouched. Private user counts, workout
dates/IDs, GPS, job IDs and incident measurements/timings are not published. No
code or live operations are performed by this documentation change.

## Ingest Phase Progress Follow-Up (029)

**Source-30 cutover completed at revision 61; revision-60 evidence below is historical.**
[029 ingest status reporting](database-migrations.md#ingest-phase-progress-029)
combines phase progress with outcomes, filtering and reprocessing; **030** combines
compact and canonical display. Final source/runtime `SupportedSchemaVersion=30`,
schema/minimum runtime **30**, baseline **27**, ledger **0-30**; **001-028 unchanged**.
See the [source history mapping](database-migrations.md#current-source-layout).

Data Sync shows actual worker activity, not a phase guessed from elapsed time or
file counters. Approved technical keys and user-facing text are:

- `resuming`: "Resuming incomplete sync job."
- `preparing`: "Preparing to read workout files."
- `discovering`: "Looking for workout files that need to be processed."
- `importing`: "Parsing workout files and importing new and changed workouts."
- `analyzing_imported_routes`: "Analyzing map data for imported routes."
- `analyzing_existing_routes`: "Analyzing map data for existing routes."
- `updating_time_zones`: "Determining and updating workout time zones."
- `cleanup`: "Cleaning up temporary files."

Route-analysis phases show **Routes checked** from actual acknowledged checks;
when the denominator is unknown, only the current count appears, not a fabricated
total or percentage. File-counter completion is not job completion, and cleanup
does not imply success. Required persisted phase Events preserve fast steps
between polls independently of optional diagnostic limits. Detail uses the latest
current-attempt phase by event identity, with no guessed or fabricated history for
legacy jobs. Source-aware phases appear in source detail and at the root, with
separate source labels when multiple sources are active rather than an invented
single root phase. Cancellation and terminal status take precedence.

Running detail polls every **10 seconds** at the default setting (or faster if
configured); queued detail and history retain the default **30-second** cadence.
Terminal ingest stops interval polling, and background detail polling pauses.
After a successful detail refresh, the open Files page and, when phase activity
exists, Events refresh without resetting the current page; failed detail requests
do not trigger those refreshes. Separate responses are not an atomic snapshot.
Logs remain manual-refresh only.

## Canonical Display Identity (030)

Named display identity uses region + normalized actual DISPLAY name/place,
excluding kind/class/components and internal jurisdiction IDs. PostgreSQL 18 UTF8
`pg_c_utf8` pins NFC/casefold and explicit White_Space normalization; directions,
accents and punctuation survive. Length-prefixed versioned SHA-256 UUIDv8 keys
retain normalized fields for collision rejection; unnamed sources remain
kind/UUID-distinct. Mixed named park/path members share one canonical group with
park-preferred presentation and retained source provenance.

Date-context membership resolves all historically eligible sources, including
history-only fallback. Distinct-workout union precedes range/all-time summaries;
only in-range-visited groups are returned. List, point stats, stored whole-group
detail, focus and all four tile layers share ready context IDs/metadata/counts
and actual matched geometry, never invented bridges. Selection JSON and all UI
cache guards carry policy/revision. Server contexts are scoped by account,
generation, revision, dates, policy and locale, with eight soft unreferenced
contexts plus active pins. All source inputs defer/coalesce revision and public
generation once per account/transaction, preserving rollback/deletion and both
READ COMMITTED commit orders / Repeatable Read serialization rejection.

The click-highlight follow-up below is deployed with this release. Its **521 UI
tests** receipt and the later focused **219 MapPage tests** receipt are historical
checkpoints, not new full-suite passes. The focused run covered display
policy/revision cache invalidation and mixed-source hit provenance; it does not
by itself establish database-backed or deployed-browser proof.

This is not an OSM derivation/matcher/path-policy bump: **no OSM regeneration,
download or route rematching is needed**. Coverage recomputation here means
canonical read-model context warm/rebuild from existing evidence, not new route
jobs. See [architecture](architecture.md#canonical-display-identity-requires-30),
[functional contract](functional-spec.md#canonical-display-identity-030),
[migration requirements](database-migrations.md#canonical-coverage-display-030)
and [release requirements](implementation-plan.md#canonical-display-identity-030).

## Click-Highlight Follow-Up

Latest [MapPage source](../ui/src/MapPage.tsx) closes Road Coverage and focuses the
mounted canvas immediately on entity-click intent. Uncached detail shows transient
loading/**Cancel** feedback in a bottom map banner, visible on desktop and mobile
at **z-index 7**, below tooltips at **8**. Errors and the **30-second** request
timeout show map-level **Retry / Close**, with manual Retry and no hidden detail
retry. Exact geometry and expanded fit bounds arrive before any fit; the existing
**350ms** camera animation and immediate white blink are retained. The existing
**three-second** highlight countdown waits for camera/tiles readiness, retaining
the bounded fallback.

The existing detail endpoint in [the map API](../api/maps.go) retains its response
and six query arguments. Current source calls the stored
`app.map_selection_coverage_entity_detail`, retained in source 030 with canonical
whole-group geometry, rather than the earlier API-local source-only query.
Metadata gates geometry through the correlated `LATERAL`/`OFFSET 0` barrier in
one statement snapshot; denied metadata cannot start geometry work. The
transaction remains read-only with critical `SET LOCAL jit=off`, not a global
setting change. This stored-reader contract belongs to combined source 030.

The independent successful-detail LRU cache is limited to **8 entries / 8 MiB of
UTF-8 serialized JSON**, with a fixed **five-minute TTL from load** and no hit
extension. This is serialized accounting, not a heap guarantee; oversized detail
is displayed without caching. No bulk geometry prefetch is added. Latest-request
and selection/generation/range, deletion, subset and mode/diagnostics/refresh
guards isolate results, now including display policy/revision. Context changes
clear detail state/cache; stale work,
abort, Cancel and unmount cannot surface late UI errors, cache late responses or
trigger recovery. The same installed geometry avoids redundant source `setData`;
style/source replacement reinstalls it. Reopening Road Coverage cancels pending
detail rather than allowing completion behind the dialog. Late 200, 503 or 404
responses cannot fit, cache, invalidate the bulk-table cache or automatically
refresh the capability after reopen. Already-completed detail cache entries and
highlights are preserved. A current 404 invalidates detail/table caches and leaves
visible Retry feedback. Manual Retry refreshes the capability;
the user reopens the table and chooses again, without replaying the stale click.
The separate hover-stats cache and bulk-table cache/client-side paging are unchanged.

At the earlier click-only checkpoint, focused verification passed **203 MapPage
tests**, covering the click flow, cache bounds/TTL, context/cancellation and reopen guards,
and highlight source lifecycle. API regression coverage includes query/geometry
parity and authorization checks. Final `make verify` and full uncached Go race
checks passed, with all **521 UI tests** passing. This is not a fresh combined-source receipt.
Separate restricted-role PostgreSQL integration runs passed query/geometry/fit parity and denied-query
execution checks under race detection; database-gated skips in broad suites are
not counted as integration passes. Real MapLibre checks covered immediate dismissal,
pointer-accessible loading/Cancel/Retry/Close feedback, cache reuse, style replay,
and late-response isolation on desktop and narrow layouts. These earlier receipts
retain their original scope; current deployment and final browser acceptance are
recorded separately in [M8 closeout](#milestone-8-closeout). No private data or
operational measurements are included. See the
[functional contract](functional-spec.md#click-highlight-follow-up)
and [architecture](architecture.md#click-highlight-follow-up).

## Coverage Display Optimization

Full protected pre-32 database/role backups and matching old image exports remain
retained. **Image-only rollback is unsupported**: recovery requires the matching
pre-32 database restore and old images together. Technical implementation is now
deployed; scripted deployed-browser verification passed. Owner acceptance/milestone
and capacity remain separate. Private owner data and actual performance
measurements stay private.

Scripted verification used real deployed responses, not mocked browser data. One
bulk request loaded the complete result; returning to the cached dialog restored
the same page without additional HTTP requests. Actual hover popups used targeted
stats without geometry, and cached revisits needed no further stats request.
Desktop and two mobile layouts passed inset alignment within one-pixel tolerance,
with zero observed alignment delta, and popup stacking above the legend,
navigation and attribution. Strict-HTTPS entry checks passed, and loaded asset
bytes matched the final image. Logout invalidated the verification session while
existing principals, passwords and sessions remained unchanged. These are scripted
deployed-browser results, not explicit owner milestone acceptance or capacity
certification.

### Deployed Read Contract

The list reader uses normalized compact workout/path/entity qualification
partials, independent regional-park context and independent park bounds. It
deduplicates display-entity/workout visits before counting and retains live
metadata, visibility, date and applied-result joins. One dataset serves named and
custom ranges and all-time history, not separate year/month materializations.
Logical deletion is visible before physical cleanup, and failed rematching retains
the prior applied result.

Transaction-local dirty keys coalesce evidence changes and old/new segment-source
invalidation. The deferred flush serializes per account before expanding segment
keys to workouts, then rebuilds affected projections once as a set. Evidence and
projections commit atomically; maintenance/serialization failures propagate rather
than publishing partial replacements. Combined source 030 coalesces display
revision and public generation once per affected account/transaction.

The API and map client use one bounded `bulk=true` load with one coverage-function
evaluation, limited to **50,000 entities / 64 MiB uncompressed JSON including
metadata**. Overflow returns 422 `bulk`/`limit`, with no partial results or partial
cache. Client-side search, unnamed filtering, sorting/tie-breaks, preference-sized
paging and the five-minute range/generation cache remain unchanged. Default API
pagination is retained. The [architecture](architecture.md#coverage-display-precomputations-requires-30)
and [migration requirements](database-migrations.md#coverage-display-precomputations-030)
define the deployed contract and verification boundaries, not live measurements.

Hover now calls the targeted
`GET /api/map-selections/{mapSelectionId}/coverage/{entityKind}/{entityId}/stats`
reader and receives `RoadCoverageEntity` without geometry or expanded fit bounds.
Click highlights still fetch full detail. Compact facts are restricted by entity
before aggregation, with entity-first indexes inside account scope. Candidate
workout IDs also bound the live eligibility work, without dropping independent
context-only or bounds-only contributors. Live
eligibility, education deduplication, counts/dates/navigation ties and the detail
reader's canonical park-geometry availability are preserved. The API-only
security-definer reader has a fixed search path and `jit=off`; structural readiness
checks exact body fingerprints, catalog definitions, ownership and ACLs rather
than trusting the schema number alone. The compact list reader also disables JIT.

The deployed timing follow-up starts an uncached lookup after a brief stable
hover, while retaining the full visibility dwell and bounded server-503 retry. A separate
canvas-local **128-entry LRU / five-minute** cache includes selection ID,
generation, resolved dates, entity kind and ID; cache hits still dwell and do not
extend TTL. Policy/revision also scope cache identity. Context changes clear
cached stats and abort stale lookups. Entity changes, canvas exit, window blur and
a hidden document cancel popup visibility; an already-started same-context lookup
can finish into the cache. Returning reuses cached or in-flight stats without a
duplicate request. Inactive hovers cannot retry, recover or restore a popup;
unmount aborts lookups, clears the cache and removes all listeners. Late
results/errors cannot affect a new context. The shell's complete-result bulk cache
remains unchanged.

Shared overlay insets are **0.75rem on mobile layouts / 1rem on desktop**, with
MapLibre inner control margins zero. Legend width reserves the zoom column/gutter.
Passive canvas-level tooltips stack above legend/navigation/attribution without
host stacking-context traps and use `pointer-events: none`. Existing map sizing,
sidebar scrolling and bulk-dialog layout are preserved.

### Historical Verification Receipts

The subsequent source checkpoint includes **164 MapPage tests**, with all **482
UI tests** passing. The combined projection/stats database race run passes
**three repetitions**, with full **21-field** metadata parity against original
list/detail readers, excluding only geometry and expanded fit bounds. Coverage
includes live metadata/eligibility and date edits, education deduplication,
canonical/mixed park evidence, subset independence and authorization boundaries.
UI regressions cover dwell, bounded retry, LRU/TTL, context invalidation,
cancellation/late responses, canvas-leave/blur/visibility cleanup and geometry-only
click highlighting. Actual query-plan checks show unrelated account history does
not expand target eligibility. Real MapLibre browser checks confirm responsive
control alignment, popup stacking and geometry-free hover requests. These are
synthetic source-verification results, not owner-data measurements or capacity
acceptance; deployment is established separately by the accepted rollout receipts.

Relevant source checks are
[MapPage tests](../ui/src/MapPage.test.tsx),
[map CSS guard](../ui/scripts/check-map-css.mjs),
[stats contract tests](../api/migrations/tests/coverage_entity_stats_test.go),
[stats database integration tests](../api/migrations/tests/coverage_entity_stats_integration_test.go),
[API integration tests](../api/maps_integration_test.go), and
[catalog readiness mutation tests](../internal/database/coverage_display_precomputations_readiness_integration_test.go).
These public contracts apply to all users' data; account-specific names, dates,
counts, timings and operational receipts remain private.

Migration sources now live in `api/migrations/sql/` and their contract/integration
tests in `api/migrations/tests/`. The runtime package and embedded SQL basenames
are unchanged. Native and physical-SQL migration commands, historical migration
paths and idempotent reruns pass after the move. The accepted backend-42/UI-43
rollout deployed the new runtime and migration. Its build/full-verification receipt
predates this docs-only update; no build verification was rerun or receipt retaken.

## Early Hard-Gap Release Work

The approved matcher correction partitions the once-sampled route into continuous
legs **before** window/candidate/graph work, not in a new ingest stage. Original
and sampled sequences remain unchanged. Each leg retains primary 128-point and
typed graph-exhaustion 64/32 windows, with leg-local seam/turn/tangent scopes.
One route-level `Result` retains global order/provenance, global clipping and
`Prepare` union/deduplication; minimum traversal remains per decoded chunk, never
aggregate acceptance of subthreshold chunks. Existing strict hard-gap thresholds
remain unchanged. See the [design and verification](coverage-matcher-evaluation.md#early-hard-gap-partitioning-design).

Current-source synthetic, normal and race verification passed, including the
existing 204-case matcher golden and 27 full-result/Prepare continuous-route
goldens against the pre-fix implementation. Revision 41 deployed only API and
shared-worker images, and selective failed-target recovery was verified. This is
not a new performance/capacity benchmark; historical measurements retain their
original scope. Real-case inputs, measurements and operational receipts remain
private. Existing resources, bounds, concurrency, deadlines and 6m/10m/15m timeout
policy remain unchanged.

Path policy v82, sampling v1, rules v1 and `coverage-production-v1` remain fixed
because this restores intended hard-gap handling. No database/minimum-runtime
change is required; the ingest contract now in 029 is independent, not introduced
or deployed by this matcher work. There is no global cache buster or recompute.
Previously successful disjoint routes are not universally output-equivalent:
leg-local window alignment can change evidence. Existing `acceptedCoverage`
remains authoritative unless explicitly rerun.

Only after the new deployment is verified may the authorized failed-route retry
use the direct API against the latest parent, requiring exactly one authoritative
failed target at submission. Otherwise stop; do not broaden the retry or rerun
successes/ingestion. Automatic allowance and the shared root budget, including
manual retries, are unchanged and never reset. This docs-only work performs no
live infrastructure, credential/provider reads, replay, deployment or retry.

## Deferred Generation Fix (Source Only)

Historical pre-release checkpoint, superseded by the 10/20 release below. Its
undeployed/pending statements describe that checkpoint, not the current release.

**All new behavior is undeployed.** The user decision keeps active **001-028**,
with **no active 029**. New `028_ingest_coverage_pipeline.sql` folds together the
generation-lock fix, strict eligible cohorts and mixed-failure automatic policy.
`SupportedSchemaVersion`, schema and minimum runtime are **28**, marker **27**.
Live remains **`dev-20261018`, Helm revision 28, schema/Goose/minimum runtime 28,
marker 27**, with the **old contract**, not the folded behavior. Ordinary static
027 and 001-027 stay unchanged. The earlier separate-forward-migration and
old-runtime compatibility claims are withdrawn, not rollout instructions.

The four same-name generation constraint triggers are initially deferred with
their exact events/WHEN scope, including 017's changed-current-finish-only
Coverage rule. Account seeding stays immediate; every qualifying increment remains.
The early generation fence lock is removed while identity/source/job/lease/epoch/
runtime/KEY SHARE guards remain. The historic selection helper reads nonlocking
in the same Repeatable Read snapshot as selected data; stale validation remains.
Data, counter increments, checkpoints and evidence/progress commit or roll back
together; no cache invalidation is lost. Commit tails still serialize. See the
[migration contract](database-migrations.md#deferred-account-generation-folded-into-028).

New active-ingest normal parents seal **exactly saved B new eligible matching
targets**, default 25 (1-1000), globally across regions within ingest-root/source-
sibling scope. Missing-map, current/live/unchanged authoritative failed/canceled
targets and deduplication do not count. Private prepared metadata persists across
files/crashes with saved B and scope, with no placeholder parent; parent and B
children seal atomically. Mixed-region parents are explicit: optional
`coverage.regionIds` is the actual union, including retries; `regionId` and
`targetGeneration` are representative anchors only. Per-route vectors, claims and
admission stay unchanged. A final partial seals only via account reconciliation
after the visible root/all source siblings are terminal and due preparation is
exhausted, never active EOF. Failed/canceled roots can owe legitimate committed-
data work; normal metadata/deduplication preserves cancellation footprints.

Preparation retains bounded metadata and one coordinate route at a time. ROI
uses one efficient query per route, not per point. A trusted fingerprint of all
catalog regions/configuration/boundaries/active heads must match in the same
Repeatable Read ROI lookup and before seal; mismatch invalidates for fresh ROI,
not a newly substituted vector. No cross-database latest-state atomicity is
claimed. The runner remains serial per source job, cleaning before/after the file
pipeline. Resources, semantic limits and deadlines do not increase.

At least one timeout/verified-transient eligible FAILED child enables a new
latest-only automatic attempt carrying **all authoritative FAILED siblings** with
valid descriptors, including permanent/unknown/null codes. Successes do not rerun,
cancellations are not revived, and all-permanent/unknown parents stop. The
additional automatic allowance stays default **1**, range **0-20**, durable per
root across descendants/manual retries. Normal unchanged failed-target scans
cannot create fresh roots to bypass it. This new mixed policy is not live.

Earlier synthetic PostgreSQL/PostGIS lock/commit/snapshot checks and the recorded
44/44 isolated Coverage integrations are historical, scoped evidence, **not a
fresh final folded-contract or repair pass**. The wider DB suite was not wholly
green: unrelated scheduler/stale-target and API lineage/prepared-statement fixture
failures are not claimed fixed. Synthetic archived-old-28 guarded repair checks
were completed **before final proof/source**; the new-source/catalog-proof redo is
pending. No protected restore of an **actual application backup** under the actual
migration role has passed rehearsal for this new repair.

The supplied read-only matcher-core probe establishes typed parking-repair
state-byte exhaustion, not point-limit overflow. A generic stored processing-limit
code and a generic lifecycle `failed` Job Log are consistent with that cause.
This is not an actual probe job, persisted diagnostic or APP persistence receipt;
exact dataset byte/point accounting stays in the existing ignored operator log.
No matcher policy or semantic limit is changed.

Native Goose validates history read-only but **does not replay applied 028**.
Old-contract 28 therefore needs a new private guarded offline repair with explicit
operator approval, fresh protected backup and actual-backup rehearsal. Pause all
old API/ingest/Coverage runtimes and tiles if DDL affects their app constraints.
Old apply-10/defer-6 ABIs are intentionally absent: old runtime-28 readiness fails;
new runtime-28 readiness requires private builders. Numeric version 28 does not
permit rolling reuse of old images. Preserve ledger 0-28 IDs/timestamps without
DML/rewrite, data, matcher roots/budgets, sources and credentials. Retain old
artifacts/backups; incompatible-image rollback needs the old DB contract and
images, not images alone. Private operator evidence owns repair SQL and receipts;
no public old-contract archive or active compatibility fixture is added.

Policy remains `coverage-production-v1`, **500m CPU / 256 MiB**, **two lanes**,
admission **2/2/2**, **one per reader**, **6m/10m/15m**; no worker transaction retry
or deadline allowance is added. The prior **346-test UI follow-up remains
undeployed**. Chart semantic work remains **frozen/not started**. No live apply,
deploy, source/provider call, import or manual retry is performed by this docs
update. Dataset-specific probes and counters stay private; source completion is
not settled Coverage, final yearly metrics or owner acceptance.

## Pipeline Deployment (10/18/2026)

This section records the **old deployed contract**. Its scan-threshold, source-EOF
flush and region-grouping behavior is historical, not the new folded-028 design
or proof that the new contract is deployed. Its pending owner-acceptance wording
is superseded by [M8 closeout](#milestone-8-closeout).

The completed historical ingest and available-map Coverage proof satisfy the
pipeline feature gate. **`dev-20261018` is live at Helm revision 28**, schema/Goose
**28**, minimum runtime **28**, marker **27**. Deployment completed **10/18/2026,
11:41:16 PM PDT** (`2026-10-19T06:41:16.445Z`); genuine read-only postdeployment
verification completed at 11:44:01 PM PDT (`2026-10-19T06:44:01.695Z`). No further
owner import was needed to unlock the pipeline.

All five deployments have desired/updated/Ready/Available replica counts of one.
API, worker, UI and operator OSM images were rebuilt and published under the
requested tag; exact configured references and runtime image identities passed
verification. The standalone Martin public image and full specification are
unchanged; its pod restarted when the paused deployment resumed. The operator OSM
image remains Job-only, not an extra normal deployment.

The native migration succeeded and applied **only 028**, without replaying 027.
Read-only history proof confirms applied ledger rows 0-28 and retained IDs/timestamps
for 0-27. Preservation checks cover original-column projections/full-column hashes
across all 78 existing app tables, with source credentials, job payloads/history
and consumed retry budgets unchanged. Actual API, general-worker and Coverage-worker
database login sessions passed the frozen runtime-readiness contract through real
pods; both public origins passed strict TLS and health checks. Verification made
no application/database writes, source checks or cloud-authentication probes and
started no imports. Stored source state remains connected with auto-sync disabled;
this is not fresh proof that Apple authentication remains valid.

Protected last-known-good backups and old image pins remain retained. Runtime-27
images cannot be rolled back alone against minimum-runtime-28 data; rollback
requires the coordinated protected database/image recovery procedure.

- At this release, active migration source was **001-028**. Ordinary static 027
  advances 26 to 27; it has no legacy dispatch or archived-fixture dependency. New 028 implements
  the pipeline and requires schema/minimum runtime **28**, retaining
  `migration_baseline=27`. Earlier staged 028 work is retired history.
- The native CLI holds its advisory lock, runs read-only `ValidateHistory`, then
  Goose Up. Canonical 27 advances through 028 without replaying 027. Unknown
  histories fail closed for manual private preparation; normal protected backups
  and coordinated rollback boundaries remain required.
- `worker.coverageBatchSize` / `WORKER_COVERAGE_BATCH_SIZE` defaults to 25 and
  validates 1-1000. Postcommit/file-cleanup handoff gates the outstanding normal
  and adopted union, flushes the final partial cohort and keeps files atomic.
  A synthetic compatible 63-route file yields 25/25/13; missing maps, deduplication
  and region/matcher grouping can produce smaller parents.
- SQL counts all children, including terminal/superseded rows, against each
  parent's creation-time capacity. Only compatible queued normal roots coalesce;
  new configuration affects new parents. Retries preserve only eligible failed/
  canceled predecessors and inherited capacity, never fresh top-up routes. A
  three-child retry under B=25 remains separate from a fresh 25-route normal root.
- Durable receipts, full-input canonical CAS and lease/ownership fences protect
  replay. APP transactions close before OSM work; metadata-only cohorts retain
  only one route's compact coordinates at a time. Raw readiness covers 0-250,000
  points; the unchanged 50,000-original-point matcher ceiling produces a typed
  permanent Coverage failure rather than a source-ingest failure. No-GPS adoption
  acknowledges without a matching job.
- Account recovery drains committed terminal failed/canceled source obligations,
  protecting active underfull cohorts. Ingest cancellation does not erase those
  obligations or cancel existing Coverage jobs. Ordinary unchanged failed targets
  cannot reset retry budgets; only distinct adoption authorizes force refresh.
  Typed transient readiness errors persist one-hour deferral; every error branch
  must qualify within a 256-node walk, otherwise the error surfaces safely.

Recorded source verification, not rerun by this documentation change, includes
actual disposable PostgreSQL/PostGIS fresh/canonical-27 upgrades, runtime-role
readiness, concurrent enqueue transactions, all eight manual/automatic
retry-isolation cases, CAS/cancellation/lock races, full-digest parity through
250,000 points, and source failure/recovery. Worker race integration includes
oversized normal/adopted inputs, empty adoption and later ordinary reconciliation;
the 25-route mixed-size fixture measured 76.9 MiB peak Go heap, not a process-RSS
or multi-user capacity certificate. Full 250,000-point parsing/snapshot proof is
separate from actual 50,001-point source ingestion; the larger per-row source
stress attempt did not finish within its test budget.

Policy remains `coverage-production-v1`, Coverage 500m CPU / 256 MiB, two lanes,
admission 2/2/2, one per reader and unchanged 6m/10m/15m/automatic retry policy.
No live source configuration or jobs change as part of documentation preparation.
At this checkpoint, M8 still awaited explicit owner acceptance; its later
[closeout](#milestone-8-closeout) supersedes that status. Deployment verification
was complete. At that deployment checkpoint, the next owner-operated yearly batch
had not started; an inclusive January 1-December 31 file range with auto-sync
disabled was available. Later batch diagnosis remains private, not final acceptance.
Dataset-specific counts, storage and timings stay in the ignored internal backfill log.

## Completed Work

- M8 adds encrypted iCloud source configuration, asynchronous connection checks,
  incremental remote imports and safe expired-access recovery. Setup and recovery
  use owner-authenticated Swagger/REST with externally completed Apple sign-in.
  Local/NFS remains the fallback; source CRUD UI and in-app Apple 2FA are outside
  the MVP. See [iCloud Source Setup](icloud-source-setup.md).
- The authorized legacy repair and normal failed-only retries finished for all
  affected routes within the active public NorCal extract. The earlier five-function
  compatibility patch preserved schema 20/minimum runtime 18 and did not apply M8
  source migrations, import/delete workouts or rebuild OSM. The subsequent runtime
  release upgraded the database to schema 23. See
  [Legacy Coverage Repair](coverage-legacy-route-repair.md).
- Coverage execution defaults to two per-process jobs through
  `coverageWorker.jobConcurrency`. Shared application-database admission still
  controls cluster-wide limits; local concurrency does not override them.
- Migration `023_coordinated_coverage_readers.sql` adds a shared reader-assignment
  ledger to matcher slots, least-active eligible-reader selection, lease/owner
  fencing and a default per-reader limit of one. It preserves existing
  global/account/region limits. API diagnostics and coverage workers share the
  coordinator rather than relying on connection shuffling to balance jobs.
- Reader placement uses dedicated pinned job connections, bounded discovery
  probes and a short discovery cache. Role and target-generation checks determine
  eligibility; a cached observation alone is not evidence of current readiness or
  successful failover. The completed study below demonstrates overlap, not a
  CPU/RSS capacity certification.

## Source Failure Diagnostics (10/16/2026)

- A source-failure notification retained an internal connection-check job ID.
  **View detail** incorrectly opened that ID as a sync run, whose public detail
  endpoint intentionally excludes internal checks. The UI now expands current
  source status, safe error code/summary and last-check time inside the banner.
- The Data Sync snapshot includes optional `statusCode` and `statusSummary`,
  without source configuration or credentials. Failed sources also show their
  summary in the source selector. Unavailable run links do not automatically
  retry on focus/reconnect; active runs still recover from transient failures.
- Verification passed the UI build, all 309 UI tests, API tests, and the actual
  owner-isolation/diagnostics integration test under the race detector against
  disposable PostgreSQL 18/PostGIS 3.6. Synthetic browser checks confirmed the
  disclosure works without navigation or job-detail requests.
- API/UI-only rollout completed at Helm revision 20 using new digest pins under
  the existing `dev-20261015` tag. Live readiness, diagnostics OpenAPI and served
  UI assets passed verification over strict TLS. All five deployments are healthy;
  worker, Coverage-worker and tiles pod identities/specifications are unchanged.
- This rollout skipped Helm hooks, needed no schema migration, and did not
  update source configuration/credentials or trigger a source check or ingest.
  Existing failed-source diagnostics were not cleared or retried. Previous image
  artifacts were retained for rollback; no registry cleanup was performed.
- Revision 21 adds a UI-only follow-up: a retained `/data-sync/jobs/...` URL from
  the old source-check notification link is replaced with `/data-sync` when run
  detail returns 404, and the unavailable run panel is hidden. Actual active/latest
  run fallback and transient-error recovery remain intact. All 313 UI tests pass;
  synthetic browser checks cover the obsolete link, source diagnostics and refresh
  after URL cleanup. API, workers, tiles, schema and source credentials are unchanged.
- Revision 22 (10/17/2026) adds visible, accessible manual-sync feedback. The
  button remains pending through POST acceptance and initial run-detail retrieval;
  queued/started/reused confirmation is visible immediately. Detail retrieval
  failure does not misreport an accepted submission as failed. Previous-run
  polling cannot replace a pending submission's announcement. All 320 UI tests,
  production build, formatting and synthetic browser checks passed. This rollout
  changed only the UI image, without source operations or API/worker restarts.
- [Credential recovery instructions](icloud-source-setup.md#recovering-expired-authentication)
  explain external reconnect followed by a credential PATCH. The deployment does
  not substitute for owner authentication recovery or iCloud acceptance.

## Coverage Recovery Release (10/17/2026)

This receipt uses **pre-squash historical numbering**
([mapping](database-migrations.md#current-source-layout)); its earlier 027-030
recovery numbering was already consolidated into baseline 027, not today's 029/030.

At this historical checkpoint, migrations 027-030 and runtime 30 deployed at revision 23, retaining minimum
runtime 26. The release adds durable routed ingest counters, cause-aware
configurable automatic retries and runtime-capability compatibility. Existing
failed historical jobs were not automatically enrolled; authorized selective
recovery is being tracked separately.

- Route counters snapshot route presence in import events, recover committed-file
  progress and preserve unknown history rather than claiming zero.
- `coverageWorker.autoRetryCount` defaults to 1 additional attempt, accepts 0-20,
  and retains the existing timeout-only 6/10/15-minute policy. Only timeouts or
  verified transient database failures qualify. Deterministic limits and unknown
  causes remain manual-only; mixed parents preserve manual retry access.
- Retry origins/budgets are durable and ordinary reconciliation cannot bypass
  exhaustion by enqueueing an unchanged failed target as a fresh root. Explicit
  manual cancellation/retry semantics remain supported.
- Graph expansion limits now have a typed error, preventing them from being
  mislabeled as temporary map unavailability. Safe worker diagnostics expose
  only bounded stage, SQLSTATE and validated limit metadata, not raw errors.
- Full ordinary Go tests, 327 UI tests/build/formatting and actual disposable
  PostgreSQL/PostGIS counter, policy, race and runtime-readiness suites passed.
- This recovery release did not include pipelining. The implemented source and
  current deployment evidence are recorded in Pipeline Deployment.
- Diagnostics and production now share 12,000 sampled / 50,000 original-point
  bounds, OpenAPI/storage validation and the same adaptive matcher. Typed graph
  exhaustion retries 128-point windows at 64/32 without pruning observations or
  nodes; accepted-window accounting and continuous seam repair are verified.
  Diagnostic HTTP deadlines remain distinct and explicitly inconclusive.
- Four prior point/graph-bound inputs completed admitted read-only matching,
  generation verification and preparation under unchanged resources. These were
  functional core probes, not persisted recovery or HTTP/dual-lane certification.
- A restored private application backup exposed whitespace-sensitive stored-function
  anchors in unshipped 028. Bounded case/whitespace matching and exact-one checks
  passed compact-definition regressions before republishing the migrator.
- The live migration initially waited behind a long-idle reader. The reader was
  closed only after explicit owner approval and exact session revalidation. A
  fresh final backup preceded the successful upgrade; all original columns and
  row counts across 76 app tables were preserved. No raw-route or source-credential
  changes occurred. Migration lock/statement timeouts now bound that maintenance
  job, and the runtime rollout preserved existing resource/admission settings.

## Retry Presentation And Contiguous Baseline (10/18/2026)

The retry-presentation changes deployed in consolidated migration 027 at
revision 24, schema/runtime 27, minimum runtime 26 and baseline marker 27. The
active Goose history at that release was exactly 001-027. Current source uses
ordinary static 027 plus folded 028, not the historical conversion dispatcher
or archived development fixtures. The design contracts are in the
functional specification, architecture, job-state machine and
[migration baseline document](database-migrations.md), not this tracker.

- Coverage details expose advisory retry eligibility and owned current-Coverage
  links separately from retry ancestry. Typed conflict responses use safe,
  actionable copy, with no arbitrary database messages.
- Resolved failed/partial parents have a green derived Superseded presentation;
  stored execution outcomes and individual route badges remain unchanged.
- Display-status filtering, counts and pagination are consistent. Ordinary pages
  evaluate only page-relevant proofs; affected failure filters classify before
  counting. Empty filtered pages and terminal details continue refreshing.
- Blocking conflicts survive failed or metadata-incomplete refreshes until fresh
  eligibility resolves them. Transient busy conflicts remain retryable.
- Disposable PostgreSQL tests cover current-result proof, current-parent links,
  constructor authority, owner isolation, latest/depth guards, filter pagination,
  bounded proof calls and schema readiness. Synthetic browser checks confirm
  green parent/failed-child presentation and navigation at three viewport widths.
- No live jobs, ancestry, source profiles or stored results were changed by this
  presentation work. No additional Coverage retry was submitted.
- Native fresh installation and legacy checkpoint conversion passed, including
  an actual restored application backup and rollback injection. The live native
  conversion preserved all 78 existing app table row counts/original columns and
  ledger rows 0-26 while replacing legacy ledger 27-30 with canonical 27.
- All affected images were rebuilt and the old backends were stopped before the
  metadata version conversion. New API/ingest/Coverage role readiness, tile
  contracts, exact ledger/marker, public strict TLS and served Superseded styles
  are verified. The legacy owner-scoped read path is verified without creating a
  login session; authenticated owner HTTP was not synthesized or claimed.
- Pre-baseline backups and old image artifacts remain retained. Old runtime-30
  images require a database restore before rollback; automatic Helm rollback
  against the rebased database is forbidden.

## Policy-Preserving Matcher Performance Recovery (10/18/2026)

- Profiles localized allocation-heavy graph rebuilding in parking repair and
  repeated geography-index work in candidate reads. A synthetic cycle and a
  separately captured structural growth pattern motivated bounded exact
  continuation detection, not larger matching limits.
- An initial unsupported-middle acceptance rule was a policy change. Its brief
  revision-25 binary rollout produced no actual jobs or diagnostics before a
  same-schema return to revision-24 images, recorded as Helm revision 26. That
  rule is absent from the final revision-27 release.
- V1 raw-support predicates, margins, ordering and supported-middle replacement
  behavior remain. Exact state/suffix certificates require deterministic
  continuation; ambiguous choices remain unchanged under a strict work limit.
  Filtered graph views avoid whole-graph reconstruction. Explicit limits fail
  without publishing partial Coverage. The contract stays `coverage-production-v1`.
- Candidate batching caches an exact geography superset with an 8,192-row bound
  and sentinel fallback to the original query. Real PostGIS oracle tests preserve
  all 28 fields, float bits, ordering, precedence and overflow behavior.
- Unit/race/vet, 204 unchanged goldens, 203 terminating policy-oracle cases and
  fuzz regressions pass. Actual read-only routes and one control passed full
  evaluation/preparation; the control preserved counts/evidence/prepared output
  exactly. This is not universal route equivalence or multi-account capacity proof.
- Main deployed new API/worker binaries only, with schema, UI/tiles, OSM writer,
  resource/admission configuration and execution budgets unchanged. Authorized
  selective production recovery then settled successfully, without increasing
  automatic retry allowance or rerunning ingestion. Dataset-specific outcomes,
  timing/headroom and diagnostic availability exceptions remain internal.
- Final backfill metrics precede the configurable pipeline, now deployed at
  revision 28 with default batch size 25. No pipeline schema
  or runtime behavior was included in this historical performance release.

## Initial M8 Deployment

- Release `dev-20261013` was deployed at Helm revision 14, with schema/Goose 23 and
  minimum runtime 23. One coverage pod runs `N=2` with strict reader-only mode on.
- Revision 13 initially failed migration 023 because an exact-text matcher did
  not recognize restored function formatting. Migrations 001-022 had committed
  in a separate transaction, leaving schema 22 and only two of five application
  pods Ready temporarily. The whole migration sequence was not atomically rolled
  back.
- The corrected regex path and API image completed the upgrade. Restored compact
  22-to-23 verification recorded 97 checks, including 63 readiness checks. Final
  readiness showed all five application pods Ready; the failed migration Job and
  its two failed pods were removed after successful verification.
- Global/account/region admission limits remain `2/2/2`, with per-reader limit
  `1`. The deployment did not implicitly increase those limits.

## Configuration Status

The deployed route timeout policy is six minutes initially, ten minutes on the
first timeout-only retry, and fifteen
minutes on the second and later retries. `coverageWorker.routeTimeout` controls
only the initial budget; the first retry uses `min(max(initial, 10 minutes),
15 minutes)` and later retries use fifteen minutes. This preserves a longer
initial budget on the first retry, while runtime validation still restricts the
initial setting to five seconds through ten minutes. Only timeout failures
increment the retry count; all other retry reasons reset it. The default lease
is now a seven-minute renewable ownership TTL, not a match timeout, with unchanged
twenty-second heartbeats and the strict initial-timeout-plus-ten-seconds cleanup
margin. This policy deployed with `dev-20261014` at revision 15 and remains in
subsequent releases. The historical `dev-20261013` study used the earlier
three-/six-/ten-minute policy; its measurements remain in private operational records.

`osm.useReadReplicasOnly` controls connection policy, not Secret selection. Its
chart default remains false; this release runs with true. False retains the
chosen reader URL and normal driver behavior; it neither forces primary routing
nor switches to a writer credential. Readers use independent complete
`{name, key}` references:

| Component | Reference | Default name / key |
|---|---|---|
| API diagnostics, when enabled | `api.coverageDiagnostics.databaseSecret` | `workouts-explorer-database` / `osmReaderDatabaseUrl` |
| General worker | `worker.osmDatabaseSecret` | `workouts-explorer-database` / `osmReaderDatabaseUrl` |
| Coverage worker | `coverageWorker.osmDatabaseSecret` | `workouts-explorer-database` / `osmReaderDatabaseUrl` |
| Application migrations | `api.migration.databaseSecret` | `workouts-explorer-database` / `migrationDatabaseUrl` |
| Administrator bootstrap | `api.bootstrap.databaseSecret` | `workouts-explorer-database` / `migrationDatabaseUrl` |
| Optional role provisioning | `api.roleProvisioning.databaseSecret` | `workouts-explorer-database` / `migrationDatabaseUrl` |

Bootstrap remains optional and defaults to disabled. Role provisioning also
defaults to disabled, so no role-provisioning Job or reference to a separate
missing provisioning Secret is rendered by default. When enabled, provisioning
requires `CREATEROLE`; the shared migration URL is adequate only if its actual
role has that privilege. Ordinary migration/runtime credentials must not be
assumed privileged. These are selector names, not credential values.

OSM writer hooks use separate `osm.writerDatabaseSecret` and
`osm.writerDatabaseTLSSecret` selectors; operator Jobs retain independently
supplied writer selectors. See [OSM Read Replicas](osm-read-replicas.md) for policy
and connection boundaries, and [chart documentation](../helm/README.md) for chart
reference material. Older pending-rollout and schema-22/minimum-21 wording in
related guides describes earlier stages, not the current source-30 contract or
the separately reported pre-cutover actual state.

## Recorded Verification

These are recorded scoped results, not tests rerun for this documentation change.
Historical results retain their original boundaries.

| Scope | Recorded result and exclusions |
|---|---|
| M8 ordinary checks, 10/12/2026 | `make verify` passed Go tests/vet, generation/format, typecheck, Go/UI builds, Helm checks and 303 UI tests; database-backed skips are not integration passes |
| M8 scoped race and disposable PostGIS, 10/12/2026 | Scoped race checks and actual M8 PostgreSQL 18.6/PostGIS 3.6.4 integration passed within the linked schema-21 scope; the wider database suite was not wholly green |
| M8 local image and synthetic browser, 10/12/2026 | Local amd64 native/non-root checks and synthetic desktop/tablet flows passed; no cloud calls, publication or deployment; arm64 execution and full worker startup were not verified |
| Legacy repair, 10/13/2026 | Disposable schema-22 and schema-20 compatibility checks plus authorized repair/retry verification completed; not a fresh whole-suite or concurrency benchmark pass |
| Earlier reader-policy checks | Scoped configuration/protocol/TLS fixtures and disposable lag/wait checks were recorded; headless DNS/TCP and a separate direct-backend TLS/authentication client were verified, not in-pod TLS/authentication or mTLS |
| Final focused schema-23 normal/race and actual PostGIS checks | Final summary: 598 passing leaf-test executions, zero failures and zero skips; 35 binaries compiled, including 26 tested package binaries, with no compile failures |
| Restored compact 22-to-23 migration/readiness | Corrected upgrade path: 97 checks, including 63 readiness checks; final deployed schema/Goose and minimum runtime are 23 |
| Authorized live concurrency study | Completed with successful final outcomes and retained timeout attempts; observed overlap and standby routing, not serial speedup or CPU/RSS capacity certification |

The [detailed M8 results](implementation-plan.md#verification-results-10122026)
retain the original counts, baseline database failures and recovery-copy-fix
chronology. PostgreSQL integration was not rerun after that message-only fix.
Earlier receipts must not be relabeled as schema-23 verification, physical
replication/failover proof, live-cloud acceptance or production validation.

## Live Concurrency Study

The authorized study completed through normal parent/child retries. Final success
does not erase earlier timeout costs. Case counts, attempt durations and polling
measurements remain in private operational records.

Distinct reader assignments and simultaneous standby queries established observed
overlap, not speedup against a serial baseline. CPU/RSS metrics were unavailable;
routing and query overlap do not certify CPU capacity, memory headroom, failover
behavior or production scaling. No admission-limit increase is justified by this
study alone.

## Telemetry Status

The earlier exporter TLS errors reflected the gateway leaf that expired on
10/12/2026. The external gateway rolled on 10/14/2026 at 12:42 AM
(`America/Los_Angeles`) with a refreshed leaf valid until 11/8/2026. Client trust
was correct for all five application pods, and the TLS handshake was verified
at 1:02 AM. End-to-end telemetry delivery was not audited; TLS recovery is not
proof of ingestion, dashboard completeness or retroactive CPU/RSS evidence.

## Telemetry-Enabled Follow-Up

The 10/14/2026 follow-up ran on `dev-20261014`, Helm revision 15, with the new
6m/10m/15m timeout schedule and a seven-minute renewable lease. Schema 23,
one Coverage pod, two execution lanes, resource limits and admission caps were
unchanged during the measurement. Samples were selected randomly within point-count
strata, excluding prior samples; this was not a uniform draw. Case measurements
remain in private operational records.

- Observed execution overlap is not serial speedup, especially across different
  samples or studies with different retry costs. Retry escalation was not exercised.
- Distinct standby reservations and long-lived snapshots established reader overlap,
  not physical promotion or failover behavior.
- Route-minus-matcher time includes loading, initialization, preparation,
  persistence and cleanup; it is not an exact copy-stage timer.
- Throttled CFS-period fraction is not CPU time lost. Cumulative allocation volume
  is not resident memory or exact live heap.
- Exported attempt counts were reconciled with the job ledger. Missing histogram
  series were inferred zero only after verifying idle state, not observed as zero.
- Direct collector exports and pod metrics do not establish Prometheus scraping,
  Thanos storage or complete trace ingestion. Thanos access was unavailable; exact
  Go heap and per-stage query histograms were not available.

Raw inputs and account preferences were unchanged. The temporary verification
session was revoked and no study jobs or matcher reservations remained. These
results support the configured concurrency of two for the studied workload, but
do not justify higher concurrency or certify a large import backlog. Investigate the
geometry-copy query path and worker CPU throttling before increasing parallelism.

## OSM Planner Statistics Refresh

The authorized 10/14/2026 maintenance refreshed join/filter-column statistics on
`osm_canonical.ways`, `osm_canonical.localities`, `osm_canonical.path_segments`
and `osm_catalog.generations`. Recursive canonical-table analysis populated both
parent and physical-partition provenance statistics; both replicas replayed past
the refresh watermark and exposed the new statistics. This was targeted analysis,
not a refresh of every geometry/payload-column statistic. Initial full-column and
short-budget attempts did not complete; independently verified targeted commits
and replica replay establish the successful refresh.

- The generation-filter estimate increased from 113 rows to approximately
  4.5 million rows, consistent with the active segment partition's catalog size.
- Retained segment-reference sets were checked on each replica using direct,
  verified-TLS connections, read-only transactions and normal planner settings.
  Scoped logical-path aggregation completed without disabling bitmap scans or
  writing temporary blocks. Case measurements remain private; cache and reader
  storage differences prevent interpreting these query diagnostics as controlled
  end-to-end route throughput measurements.
- The unchanged production copy query still exceeded its eight-second diagnostic
  limit on both replicas. Its plan retains broad active-segment aggregation;
  refreshing statistics alone does not establish that this bottleneck is fixed.
- The scoped SQL remains a diagnostic alternative, not deployed application
  behavior. No indexes, schema, global planner settings, imports, application
  records or worker resource settings were changed. Worker CPU remains 250m;
  there was no Helm upgrade for this comparison.
- Preparation adds provenance columns after earlier analysis stages. At this
  maintenance-only point, durable lifecycle refresh and a tested bounded copy
  query remained follow-up work; the rollout below implements both.

## Optimization Rollout Comparison

The authorized 10/14/2026 rollout published `dev-20261015` and upgraded the release
from revision 15 to 16. All five deployments are Ready/Available, all three normal
hooks succeeded, and schema/Goose/minimum runtime remain 23. Coverage CPU is now
500m, verified in both pod configuration and cgroup quota, with unchanged 256 MiB
memory, one pod, two execution lanes, replica-only enforcement, admission caps and
6m/10m/15m timeout policy. Other resources, credentials and source configuration
were preserved. No real iCloud access, date-range ingest or regional OSM import
was performed.

- `CopySegments` now aggregates active logical-path names only for requested
  paths. Synthetic PostGIS equivalence tests compare every old/new output column,
  including overlapping regions, retired generations, education/null fallbacks,
  repeated paths, ordinals and bounds.
- OSM updater statistics maintenance runs after startup storage GC and after
  committed promotion/GC, outside the partition-swap transaction. Startup replay
  also covers unchanged sources and resumed runs. Failure after promotion cannot
  fail or drop the active generation. Targeted recursive analysis has transaction-
  local ten-minute statement and five-second lock limits, a thirty-minute context
  cap and explicit root/leaf maintenance-privilege checks. This simple replay
  contract intentionally repeats analysis rather than adding another checkpoint.
- Verification passed: full ordinary Go suite, UI tests, Helm checks, non-root
  image checks, focused race tests, and disposable database tests for query
  equivalence, statistics, privileges, locks, cancellation and lifecycle failures.
  Environment-gated tests skipped by the ordinary suite are not integration passes.
  The stale 250m chart-test expectation was corrected to the 500m default before
  the successful verification/publication retry.

Canonical inputs, region/generation vectors and matching contracts were held fixed
without resampling. Stored match evidence was unchanged, but end-to-end performance
regressed despite reduced preparation overhead. Case counts, timings and resource
measurements remain in private operational records.

SQL, statistics and CPU changed together; reader cache, node and shared-storage
conditions were uncontrolled. Retry costs and a monitoring interruption also
prevented a clean throughput comparison. Recovered strict-TLS telemetry used
fifteen-second query steps and one-minute CPU/CFS rates, distinct from earlier
direct-monitor sampling. Throttled-period fractions are not CPU time lost.

Final verification confirmed current results, unchanged canonical inputs,
equivalent stored evidence and cleanup of jobs, reservations and observer sessions.
This correctness pass was not a capacity or first-attempt completion guarantee.
The subsequent graph-statistics diagnosis and repair are recorded below.

New or changed canonical route data from successful ingestion participates in
automatic readiness/reconciliation and coverage scheduling when required map data
is available. Coverage remains asynchronous relative to ingest completion. An
unchanged reimport through the same source identity does not force already-current
routes to rematch. A newly created source has a separate deduplication scope and
can create additional records for overlapping imports; see the source-replacement
warning in [iCloud Source Setup](icloud-source-setup.md#replacing-an-existing-source).
The six-minute budget and this test do not guarantee first-pass success for a
subsequent iCloud date-range ingest.

## Graph Statistics Regression Repair

The 10/15/2026 follow-up identified a concrete planning regression in the earlier
partial statistics refresh. The matching candidate and incident-edge SQL text
had not changed. Canonical parents gained accurate generation/region statistics,
but inherited `start_graph_node_id` and `end_graph_node_id` statistics were absent.
This mixed state caused severe overestimation of graph-node matches: a 128-node
incident query estimated roughly 2.9 million intermediate rows and selected a
full-segment-table join. The graph-node indexes existed; the missing selectivity
statistics made PostgreSQL choose poorly despite them.

Targeted analysis of both endpoint columns repaired parent and leaf statistics
on the primary, and both replicas replayed and exposed the correction. The
same-replica plan inspection then showed no full segment scan, with estimated
plan cost dropping from approximately 1.49 million to 6,847. These are planner
cost units, not elapsed times. The public samples were not identical across that
maintenance comparison, so no paired microbenchmark speedup is claimed.

Historical container pressure also increased during revision 16. These observations
cannot distinguish external contention from inefficient-query work; measurements
remain in private operational records.
CPU-limit/throttling series for these database containers were unavailable, not
observed zero. The investigation did not change database resource policies,
global planner settings, schema, indexes or public OSM data.

The corrective implementation adds both graph endpoints to the updater's existing
targeted recursive analysis. Regression tests require populated, non-null endpoint
statistics on inherited parents and every synthetic active leaf, alongside the
provenance statistics. Ordinary Go/UI/Helm and image checks passed, as did focused
race and disposable database lifecycle/statistics tests. `dev-20261015-r1` deployed
at revision 17, preserving 500m CPU, 256 MiB memory, two execution lanes, strict
reader-only placement, admission caps and the six-minute initial timeout. The
scoped copy query is retained; rolling back application images alone would not
undo database statistics and would reintroduce the earlier preparation overhead.

The acceptance comparison held canonical inputs, generation vectors and matching
contracts fixed, without resampling or ingestion. Corrected statistics resolved
the observed regression while preserving stored evidence. CPU remained 500m;
no optimizer hints or matching-algorithm changes were deployed for the repair.
Case counts and performance measurements remain in private operational records.

The corrected monitor had no interruption. Final verification waited for settled
telemetry and confirmed current results, unchanged canonical data, equivalent
stored evidence, healthy services and cleaned-up jobs, slots and observer sessions.
No source configuration change, iCloud access or ingest was triggered.

Shared infrastructure conditions remain observational. This comparison does not
certify arbitrary backlog capacity or guarantee future first-attempt completion.

## Map Defaults And Tag Refresh

The 10/15/2026 chart review replaced the inert placeholder family with complete
MapTiler `outdoor` and `streets` defaults, including both appearance URLs,
attribution text, every attribution link and exact resource origins. Public
examples use `YOUR_MAPTILER_KEY`, never an installation's real browser key.
`Hiking -> outdoor` and `Running -> streets` are active default mappings, not
commented examples. The chart README is regenerated from its retained template,
and rendering tests cover the complete families and active mappings. Provider
matching is documented as normalized original workout identity, distinct from
canonical UI labels and with a limited qualified-activity alias fallback.

A read-only review confirmed the installation's existing mappings cover its
currently routed provider identities. Installation-specific mapping arrays,
families, fallback, credentials and other live values were preserved; private
type inventories are not recorded here. The source-replacement caveat in the
iCloud setup guide now explicitly documents source-scoped deduplication and
retained workouts after source deletion.

All four components were rebuilt and republished with the requested
`dev-20261015` tag, and revision 18 deployed successfully with all five services
healthy and normal hooks successful. The verified digests are identical to the
fixed `dev-20261015-r1` build; Coverage remains at 500m CPU, two lanes and a
six-minute initial budget, with schema/minimum runtime 23 unchanged. Historical
revision-16 references to `dev-20261015` describe the earlier, now-replaced tag
contents, not this fixed release. Use revision and digest receipts for identity.

After rollout verification, the separately authorized Harbor cleanup removed
older artifacts and extra aliases from `workouts-api`, `workouts-worker`,
`workouts-osm` and `workouts-ui`. Each repository was verified to contain exactly
one artifact and only the `dev-20261015` tag. Fourteen older artifacts and seven
extra aliases were removed, including temporary preservation tags. No registry
garbage collection was triggered. Deleted digests are no longer available for
rollback from Harbor; Helm history alone does not retain container images.

Ordinary Go, UI, Helm, documentation and non-root artifact checks passed before
publication. No source was created, converted or deleted, no real iCloud operation
or ingest was triggered, and no Git commit was created on the owner's behalf.

## Provider Identity And Source Adoption

Historical rollout checkpoint. Its pending live-cloud acceptance statement is
superseded by [M8 closeout](#milestone-8-closeout); its verification scope is unchanged.

The authorized 10/15/2026 implementation and data migration deployed migrations
024-026 with schema/Goose/minimum runtime 26 at Helm revision 19. All four images
were rebuilt as `dev-20261015`, verified remotely and rolled out together. All five
services and all three application runtime-role readiness checks pass. Coverage
retains 500m CPU, two lanes, strict replica-only placement, existing admission caps
and the six-/ten-/fifteen-minute timeout policy. The matching algorithm and OSM
queries are unchanged; no public OSM data/import or resource policy was changed.

- Provider-backed workouts are unique per account, `health-auto-export` provider
  namespace and opaque provider ID, shared by local/iCloud transports. Same-source
  content follows the existing unchanged/update rules. A different live owner
  blocks duplicate creation and ownership theft; a single deleted-source owner
  permits adoption of the existing UUID and authoritative incoming content.
- Existing provider identities were conflict-free before migration, so no merge,
  deletion or ownership reassignment was necessary. Namespace metadata and the
  global uniqueness constraint now cover the existing records. Deletion targets
  also retain provider namespace so deliberate deletion is not bypassed by a new
  source. ID-less fallback matching remains conservative and source-local.
- Historical import events retain their original source/file/job identity while
  referencing stable workout/account identity rather than current ownership.
  The authenticated provenance API uses a narrowly granted reader so deleted
  sources do not hide old history. API/OpenAPI and desktop/mobile UI contracts
  distinguish `adopted` and `skipped_live_owner`; progress counts them as Updated
  and Unchanged respectively, not as proof of identical payload bytes.
- Successful file bytes/checksums and first-seen freshness are preserved. A file
  with any live-owner block records the account ownership epoch captured before
  decisions. It skips unchanged metadata while that epoch remains current, but
  a successful source deletion makes it eligible for the next fresh incremental
  job even when metadata/bytes match. Ordinary unblocked files remain skippable.
  Cookie refresh, disabled sync and connection failures do not release ownership.
  Frozen same-job actions and terminal checkpoints are never reopened or recounted.
- Adoption records a durable Coverage obligation in the same file transaction,
  including unchanged content, without taking Coverage state/parent locks. After
  commit, ingest repair or leased reconciliation enqueues matching without faking
  the route digest. Event fencing, due filtering, lease renewal and bounded replay
  prevent stale consumers, duplicate running targets and deferred-prefix starvation.

Verification includes ordinary Go/UI/Helm/generated-code checks, focused race
suites, actual disposable PostgreSQL/PostGIS upgrade/adoption/inventory/provenance
tests and failure cases for privileges, source/workout deletion, contention,
captured epochs, replay and legacy writes. Already-admitted file transactions may
finish after wall-clock lease expiry while their locks preserve authority; expired
leases still cannot acquire a fresh fence. Synthetic compact-function upgrade
tests preserve routine owners/ACLs and reject missing or duplicate rewrite anchors.

A protected preliminary application backup was restored in an isolated loopback
database and migrated through 23 -> 24 -> 25 -> 26. This caught a formatting-sensitive
stored-function anchor before any live schema write; the corrected migration then
passed against the real restored data with owners/ACLs preserved and actual
runtime-readiness checks. During live maintenance, API/ingest/Coverage writers were
paused, a final protected backup was taken, and row-count/aggregate-hash snapshots
covered every pre-existing application table except intentionally changed schema
metadata. All snapshots matched after excluding only newly introduced metadata
columns. Existing IDs, source ownership, credentials, import history, routes,
metrics, coverage results and preferences were preserved. The migration used the
actual configured migration identity; final read-only probes verified runtime 26
and role readiness after the coordinated rollout restored the services.

The owner-requested latest-only Harbor state was restored after successful rollout:
each `workouts-*` repository has one artifact and only `dev-20261015`. Old schema-23
binaries must not be rolled back against schema 26; the private database backup is
retained separately, and Helm rollback does not reverse this forward-only migration.
No registry garbage collection was triggered.

Relevant design contracts are updated in Architecture, Functional Specification,
Job State Machine, Implementation Plan and iCloud Source Setup, including the
worked Local A / Cloud B incremental-skipping example. No real iCloud profile was
created or accessed, no ingest was manually triggered, and the local source was
not removed. The owner can perform the source switch without wiping workouts;
deliberately deleting workouts first would preserve their deletion-suppression
intent across the replacement source. Live-cloud owner acceptance remains pending.

## Milestone 8 Closeout

The revision-57 acceptance receipt below uses **pre-squash historical numbering**
([mapping](database-migrations.md#current-source-layout)); it remains **PASS**,
not a fresh source-30 pass or current cutover receipt. Actual release status is
tracked in [Current Status](#current-status).

The final qualified receipt and canonical migration handoff are retained privately.
Earlier incomplete popup probe attempts were placement limitations, not UI
defects; their original evidence remains private and is not rewritten by the
final pass. Protected actual-backup migration/preservation, role-readiness and
matching database/image rollback proof are recorded in that handoff. Historical
test receipts retain their original source/scope: this closeout claims neither a
new full-suite pass nor capacity, load or failover certification. No application
builds or tests were rerun for this documentation change.

The deployed follow-ups start stats lookup before popup visibility and preserve
same-context cached/in-flight results after pointer exit. Account switching keeps
UI data isolated, Manual Sync spacing remains usable on narrow layouts, and
committed file counters publish before post-file Coverage draining. Run-detail
polling refreshes the open Files page without resetting its position or treating
separate responses as an atomic snapshot. Direction-arrow density follows screen
space, with final-camera, current-source and tile-readiness guards preventing stale
arrows from flashing during programmatic moves.

Historical M7 measurements and the separate M12 capacity boundary are unchanged;
this closeout adds no capacity certification. M10 administration,
OSM administration, full monitoring and operational runbooks remain future work;
M8 completion does not close those milestones or production hardening. Rollback
requires matching database and image recovery, never an incompatible image-only
downgrade. Further deployment or capacity changes require their own authorization.

Keep secrets, account identities, health inventories, workout/job IDs, GPS, source
payloads, private URLs and raw operational output out of progress records. The
descriptions above are sanitized summaries of separately authorized operations,
not authorization to inspect keyrings or host rclone configuration or to make
further cloud, database or deployment changes.

## Exact Parking Repair Checkpoint (10/19/2026)

Historical source-only checkpoint; the 10/20 release below supersedes its live
status, not its original test/probe boundaries.

This appended checkpoint supersedes earlier pending matcher diagnosis and local
UI test-count wording as current source status, without rewriting historical
release or verification records. **Nothing in this checkpoint is a deployment.**

- The new production matcher change is confined to
  `internal/coverage/matcher.go`: exact unsigned-varint state keys plus an exact
  same-cursor period-one/two positive-push certificate. V1/V2 implementation labels
  are not new matcher-policy versions; `coverage-production-v1` remains unchanged.
- Smaller keys reduce equal-work synthetic key payload but do not resolve
  unbounded insertion by themselves. The certificate rejects only a provably
  nonterminating closing candidate and still tries later ends. Terminating
  old-policy oracle results and all unchanged goldens agree. Longer periods and
  unproven/ambiguous cases retain the existing guards, not a universal termination
  guarantee. See [the evaluation checkpoint](coverage-matcher-evaluation.md#exact-parking-repair-follow-up-10192026).
- Bounds remain 512 KiB key payload, 4,096 states/accepted replacements per
  traversal, 16,384 portions/suffix nodes and an 8 MiB charged cache. The bounded
  optional descriptor measures 2,624 bytes and is charged 4,096 bytes within that
  budget; it yields to mandatory caches. No states are discarded to fit, no
  resource/deadline increases are made, and errors/cancellation publish no partial
  replacement.
- Scoped normal/race coverage tests, 204 unchanged goldens, the 256-seed old-policy
  corpus (203 terminating, 47 with repairs), 512 independent certificate witnesses
  and four 30-second fuzz runs totaling 559,058 executions pass. The 192 certified
  / 320 declined witness split tests agreement with the independent **certificate**
  reference, not complete detection of nontermination.
- The final full **58-worker integration rerun remains pending** for the combined
  current source. Earlier integration receipts retain their original source/scope.
  The separately recorded **366 UI tests are green**, with UI source unchanged by
  this matcher work; that does not deploy the local UI follow-up. Final benchmark
  receipt publication remains with its owner.

Active migration source stays **001-028**, with no active 029. Source supported
schema, schema and minimum runtime stay **28**, baseline marker **27**. No 025/028
migration rewrite or SQL version change is part of this matcher follow-up. Live
remains **`dev-20261018`, Helm revision 28**, schema/Goose/minimum 28 and marker 27,
with the **old contract**. New folded-028 locking, strict eligible cohorts,
mixed-failure automatic policy, matcher follow-up and local UI work remain
**UNDEPLOYED**. Preserve all existing data, history and consumed retry budgets.

The private old-contract SQL repair bundle is staged/ready for the next operator
step, not live-applied. Protected actual-backup rehearsal under the actual
migration role, final combined verification, new images and separately authorized
coordinated rollout remain pending. Native Goose does not replay applied 028;
numeric version equality does not make old images compatible with the new
contract. Earlier synthetic checks are not an actual-backup rehearsal. Chart
semantics remain frozen/not started. Coverage remains 500m CPU / 256 MiB, two
lanes, admission 2/2/2, one per reader and unchanged 6m/10m/15m budgets.

User-owned probe evidence and all dataset-specific measurements remain private.
Diagnostic guards describe processing work, not a claim of excessive route mileage.
No live apply, deployment, source operation, SQL write, job/retry submission or Git
commit is performed by this documentation update. Source completion remains
distinct from settled yearly Coverage and explicit M8 owner acceptance.

## Folded-028 Production Release (10/20/2026)

This historical release checkpoint predates the independent yearly closure below
and [M8 closeout](#milestone-8-closeout). Its pending-recovery/acceptance and live
version statements retain that earlier evidence boundary, not current status.

**`dev-20261020` is deployed at Helm revision 29.** Schema/Goose/minimum runtime
remain **28**, marker **27**, with active source **001-028** and **no active 029**.
The authorized rollout receipt is dated **10/20/2026, 1:54:53 AM PDT**
(`2026-10-20T08:54:53.992Z`). All four application/operator images were rebuilt
and digest-pinned. All five application deployments are Ready/Available; the OSM
operator image remains Job-only and Martin's image/specification is unchanged.
Generation locking, strict exact-B eligible cohorts, mixed-failure automatic
policy, exact parking repair and the UI follow-up are **deployed**, not pending.

The approved repair first passed a protected restore of an actual application
backup, using the real non-superuser migration login and its existing security-
owner SET capability. After quiescence and a final protected backup, the explicit
guarded SQL repair committed transactionally. It admitted only the reviewed exact
actual-old profile, not a generic normalized SQL profile. Native Goose then
confirmed applied 028 without replay. New API/ingest/Coverage readiness passed;
same-number old runtime-28 images fail the new structural contract.

All original tuple columns across 81 app relations plus the Goose ledger were
hashed before/after repair with **no data-field exclusions**. Exact ledger IDs
and timestamps, source ciphertexts, history and spent retry budgets were retained.
All seven actual historical table-metadata differences and 198 retained function
OIDs were preserved. The formatting qualification covers only the frozen reviewed
corpus and exact literal bytes, not general SQL-lexer semantic equivalence.
Post-startup derived reconciliation timestamps have a separately approved narrow
exception recorded privately; it does not weaken the pre-startup preservation
proof or exclude any raw data.

The released matcher uses exact unsigned-varint keys and exact positive-growth
period-one/two certificates (`GrowthPeriod` 1/2), with source SHA-256
`e8935386435aa107a1c36abf7c6792dd5bc923678ef4bf4ba4c023c14677b447`.
Policy remains `coverage-production-v1`, with unchanged semantic limits. Real
production automatic recovery has succeeded and published authoritative current
Coverage for the mixed-failure successor cohort. This is persisted recovery
evidence, not just a read-only probe;
it is not yet independent closure of the entire historical year.

Coverage remains **500m CPU / 256 MiB**, **two lanes**, admission **2/2/2**, one
per reader, **6m/10m/15m**, B=**25**, and **one additional automatic attempt**
shared per root across manual/automatic descendants. The source worker remains
**250m / 256 MiB**. All other resources/infrastructure, chart semantics and
defaults are unchanged. Strict TLS passed for both public origins. Original-DNS
hostname-verified TLS 1.3 and physical standby reader evidence passed; the backend
does **not** request client certificates, so **mTLS is not claimed**. Stored source
state remains connected with auto-sync disabled; no source check, provider call,
new ingest or source-configuration change was used for this release verification.

The separately authorized Harbor cleanup is complete in only the four app image
repositories: four old tags and eight old artifacts deleted, with twelve HTTP 200
responses. Four new release roots and four SBOM accessories remain, with only
`dev-20261020` tagged. No global registry garbage collection or blob deletion was
run; physical blob-space reclamation is not claimed. Protected persistent local
rollback exports retain the final whole-database dump, password-free roles and all
four old OCI images. They are not an off-machine backup. **Rollback requires a
full old-database restore plus matching old OCI images, never old images alone.**

Private operator records own dataset counts, timings, storage, paths and recovery
receipts. Final yearly metrics remain **TODO: independent closure**, not inferred
from ingest completion or historical failed-child counts. M8 acceptance remains
with the owner. This documentation update performs no runtime/source/registry
operation and does not authorize another retry or capacity change.

## Independent Yearly Recovery Closure (10/20/2026)

Historical recovery checkpoint. Its live-version and pending owner-acceptance
statements are superseded by [M8 closeout](#milestone-8-closeout), without changing
the original measurements, preservation proof or capacity exclusions.

**Historical recovery is complete for available map data**, independently verified
after all selected automatic/manual recovery chains succeeded and their results
became authoritative current Coverage. No unresolved selected work, active app
jobs or unexpired matcher slots remained. Historical failed/partial execution
outcomes remain unchanged; resolved parents display derived green Superseded,
not rewritten success. Missing-map routes remain unavailable, not failed or
implicitly matched.

The independent collector verified a sampled idle interval exceeding its required
duration across the physical OSM nodes. Only exact SQL signatures from hash-verified
released source identified known background catalog/ROI queries, which were
reported separately. Unknown queries still blocked closure. This is sampled
`pg_stat_activity` evidence, not a continuous query-event audit. Closure is
independent of the recovery executor's self-receipt; Main subsequently stopped
only its own operator watcher after checking accepted journal admissions and
terminal targets. No application jobs were canceled or database/deployment
changes made by that watcher shutdown.

At this checkpoint, the live release was **`dev-20261020`, Helm revision 29**, with all four
release images digest-pinned, all five deployments healthy, schema/Goose/minimum
runtime **28**, marker **27**, and active **001-028**, no **029**. Fresh read-only
runtime-contract checks under each application role passed using operator
`SET LOCAL ROLE`; these are not fresh authenticated runtime-login tests. Prior
genuine strict-TLS transport evidence and fresh workload TLS metadata remain
valid, including original-DNS physical standby TLS 1.3; the backend does not
request client certificates and **mTLS is not claimed**.

Final source counts, exact wall-time bounds, storage/headroom and receipt identities
are appended only to private operator records. Those spans include development,
repair, rollout, queueing, waits and retries, not clean throughput. Existing raw
preservation proofs and the narrowly approved derived reconciliation timestamp
exception remain unchanged. No ingest was rerun, no source check/provider call was
made, and stored source state remains connected with auto-sync disabled.

The completed app-repository Harbor cleanup and protected local old-database/OCI
rollback exports remain as recorded above. No global registry GC, blob-space
reclamation, resource increase or storage expansion is claimed. The owner should
review current headroom before another yearly batch; this closure does **not**
declare the next year ready or certify M12 capacity. M8 still awaited explicit owner
acceptance at this checkpoint; its later [closeout](#milestone-8-closeout) is current.
This documentation update changes no runtime or frozen
chart/source files and authorizes no new operations.
