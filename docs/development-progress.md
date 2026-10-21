# Development Progress

This document tracks technical project status separately from the public
[README](../README.md). Milestone 7 remains complete; Milestone 8 remains
unchecked pending owner acceptance. Historical M7 measurements stay in
[Coverage Acceptance Results](coverage-acceptance-results.md) and are not
reinterpreted here. Documentation dates use `America/Los_Angeles` and M/D/YYYY.

## Current Status

| Area | Status |
|---|---|
| M8 source implementation | Implemented and deployed; owner iCloud ingest and available-map Coverage recovery verified; explicit milestone acceptance remains separate |
| Committed-route Coverage pipeline | Folded-028 contract deployed in `dev-20261020`, Helm revision 29; exact eligible cohorts and mixed-failure automatic recovery active |
| Concurrent coverage and reader coordination | Deployed and focused verification complete; one coverage pod runs two jobs with shared least-active reader assignment |
| Deployed application database | Contiguous schema/Goose 28, minimum runtime 28, migration baseline marker 27 |
| New folded-028 contract | Active 001-028, no active 029; source support/schema/minimum 28, marker 27; generation locking, strict eligible cohorts and mixed automatic policy deployed |
| Old-contract 28 repair | Approved exact actual-profile repair rehearsed on a protected actual backup, then applied under the real non-superuser migration login; preservation and new readiness verified |
| UI follow-up | Deployed with the current pinned UI image; earlier source-only and test-count checkpoints are historical |
| Historical yearly recovery | Available-map Coverage independently settled; final measurements retained privately; owner milestone acceptance remains separate |
| Chart semantics | Frozen; all 23 release-manifest chart files unchanged; only Helm README template/generated documentation updated here |
| Legacy route repair | Authorized repair and failed-only retries complete; all affected routes have current durable matches, with no failed/queued repair work remaining |
| Reader policy | Strict replica-only enforcement is active; the chart default remains false |
| Release | `dev-20261020`, Helm revision 32; UI-only Run detail pagination fix deployed, with API/worker/OSM pins and protected pods unchanged; all five deployments healthy |
| Summary aggregates | Measured-distance totals/averages include route-less types; Workouts pairs the total with a right-aligned GPS-route count |
| Latest performance comparison | Corrected graph statistics resolved the observed regression under fixed inputs and matching contracts; measurements remain private, not capacity certification |

Runtime deployment and the earlier authorized legacy-account repair are complete.
This does not close M8: owner acceptance remains separate from release and
available-map historical recovery verification. The owner recovered expired iCloud access
through the API before the historical ingest. Host `rclone.conf` was not read.

The [10/20 release checkpoint](#folded-028-production-release-10202026) supersedes
earlier undeployed, pending-repair and old-live-contract wording. Dated checkpoints
below retain their original evidence boundaries. The
[independent recovery closure](#independent-yearly-recovery-closure-10202026)
is complete for available map data; source success alone was not used as proof.

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
sibling scope. Missing-map, current/live/unchanged authoritative failed/cancelled
targets and deduplication do not count. Private prepared metadata persists across
files/crashes with saved B and scope, with no placeholder parent; parent and B
children seal atomically. Mixed-region parents are explicit: optional
`coverage.regionIds` is the actual union, including retries; `regionId` and
`targetGeneration` are representative anchors only. Per-route vectors, claims and
admission stay unchanged. A final partial seals only via account reconciliation
after the visible root/all source siblings are terminal and due preparation is
exhausted, never active EOF. Failed/cancelled roots can owe legitimate committed-
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
or proof that the new contract is deployed.

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
  cancelled predecessors and inherited capacity, never fresh top-up routes. A
  three-child retry under B=25 remains separate from a fresh 25-route normal root.
- Durable receipts, full-input canonical CAS and lease/ownership fences protect
  replay. APP transactions close before OSM work; metadata-only cohorts retain
  only one route's compact coordinates at a time. Raw readiness covers 0-250,000
  points; the unchanged 50,000-original-point matcher ceiling produces a typed
  permanent Coverage failure rather than a source-ingest failure. No-GPS adoption
  acknowledges without a matching job.
- Account recovery drains committed terminal failed/cancelled source obligations,
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
M8's checkbox remains pending explicit owner acceptance. Deployment verification
is complete. At that deployment checkpoint, the next owner-operated yearly batch
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
related guides describes the earlier stage, not this completed schema-23 release.

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

## Pending Acceptance

- M8 remains unchecked until the owner explicitly closes it. Completed deployment,
  focused verification and account work do not substitute for that acceptance.
- Owner-operated iCloud ingest and available-map Coverage recovery are verified;
  they do not automatically close M8. Host rclone configuration has not been read.
- The folded-028 release and UI follow-up are deployed at revision 29. Protected
  actual-backup rehearsal, approved repair and post-rollout verification passed.
  Available-map yearly recovery and final measurements are now independently
  verified; dataset metrics stay private and do not close owner acceptance.
- Audit end-to-end telemetry delivery and broader backlog, replay-lag and
  interactive-latency behavior before claiming capacity or recommending increased
  concurrency. The follow-up's resource observations are not a capacity
  certification. Physical promotion/failover remains a separate boundary.

Migration 023 raised minimum runtime to 23; the old schema-20 binary is not a
rollback target for the upgraded database. Further deployment or capacity changes
require their own authorization.

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

This release checkpoint predates the independent yearly closure appended below.
Its pending-closure statements retain that earlier evidence boundary, not current
recovery status.

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
terminal targets. No application jobs were cancelled or database/deployment
changes made by that watcher shutdown.

The live release remains **`dev-20261020`, Helm revision 29**, with all four
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
declare the next year ready or certify M11 capacity. M8 remains unchecked pending
explicit owner acceptance. This documentation update changes no runtime or frozen
chart/source files and authorizes no new operations.
