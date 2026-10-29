# ADR 0014: Metadata-driven coverage reconciliation

- Status: Implemented and deployed at Helm revision 62; initialization and live unchanged-cycle acceptance PASS
- Owners: Product and engineering
- Source contracts: APP feature in 029, complete runtime 30; separate OSM schema/minimum runtime 10

## Motivation

Coverage reconciliation previously revisited historical routes to discover that
nothing needed matching. Loading full GPS histories, recomputing canonical route
hashes and resolving region boundaries for unchanged routes spends database CPU,
I/O and worker time even when **no Coverage job is needed**. Daily full-account
scans repeat that cost without a change to route inputs, map data or matcher
policy. An interrupted ingest's prepared tail also must not justify an ordinary
history scan after recovery.

The solution is durable metadata that proves when route selection and successful
matching remain valid. It is not a slower scan, a larger timeout, a concurrency
increase, or a cache whose expiry silently creates work. Necessary updates still
run, and missing proof remains unknown rather than being guessed from current
state. The policy applies to every account, not a special dataset or incident.

## Decision

There are exactly four ordinary reconciliation cases:

1. **Nothing relevant changed.** Valid selection and successful applied-target
   proof, or a valid pending/unavailable selection proof, suppress route work.
   Do not load GPS, resolve regions, write unchanged state or create a job.
2. **Route input changed or is new.** Exact committed input dirtiness or missing
   input proof authorizes a bounded canonical snapshot/hash and region lookup for
   that route. Enqueue only a genuinely eligible target; no-route and missing-map
   outcomes do not require a matching job.
3. **OSM catalog or active generations changed.** Start a metadata-filtered
   campaign. Rebind selected generation heads without GPS only when exact input,
   selection fingerprint and selection policy still prove the selected IDs.
   Otherwise resolve the full route again. Only affected effective targets match.
4. **The matching descriptor changed.** Reuse valid region-selection proof but
   compare the complete new descriptor against actual successful applied proof.
   Queue eligible changed targets, including minimum-traversal changes. Never
   relabel an old successful result as having run under the new descriptor.

Interrupted-ingest recovery is a durable queue obligation, not a fifth ordinary
invalidation case or permission to scan history. Existing failed/cancelled/live
target guards, distinct adoption authority and retry lineage remain authoritative
in all four cases.

### Proof Tiers And Work Control

These names refer to actual schema objects, not new public readiness states:

| Tier | Actual storage | Meaning |
|---|---|---|
| Catalog observation | OSM `osm_catalog.route_region_catalog_state`; accepted APP `app.coverage_reconciliation_state` | Monotonic revision, full and selection fingerprints, active heads and configured regions; APP also records selection policy and current complete matching descriptor |
| Route-selection proof | `app.coverage_route_selection_proofs` | Exact content/source, canonical route revision/SHA, point mutation version, observed catalog proof, readiness/reason, selected IDs and observed generation vector |
| Successful applied-match proof | `app.coverage_applied_match_proofs`, backed by immutable `app.coverage_job_contexts` and `app.coverage_route_job_contexts` | Actual successful `applied` or `no_evidence` result for the canonical route, normalized generations and producer descriptor |

Work control is separate: `app.coverage_route_changes` tracks
`mutation_version > resolved_version`; `app.coverage_reconciliation_wakes` stores
exact-account transaction-coalesced wakes; `app.coverage_reconciliation_accounts`
owns campaign revisions, cursor, lease, `scan_required` and `queue_retry_at`.
Existing `app.ingest_coverage_handoffs`, `app.ingest_coverage_builders` and
`app.ingest_coverage_prepared` retain committed origins and bounded cohort state.
Preparation, readiness, enqueue and campaign completion are **not** applied proof.

New APP proof/change/wake tables have owner-only forced RLS and no direct runtime
grants. Fenced helpers owned by `workouts_security_owner` expose only authorized
metadata. `workouts_worker` uses its exact live source-job capability;
`workouts_coverage_worker` uses its exact account reconciliation lease/revision.
Neither API nor tiles gains access to private proof or raw route data.

### Cheap Steady State

The reconciler observes the catalog head on its default **30-second idle poll**,
also paced during back-to-back work pages, and checks the durable work claim hint.
`ReadRouteRegionCatalog` reads the OSM singleton rather than provider geometries.
`app.read_coverage_catalog_observation()` returns the accepted floor, including
the indexed maximum durable route-proof catalog revision. An identical
`app.observe_coverage_catalog(...)` returns before locks, DML or account
enumeration. An empty claim does not update an account merely to record a poll.
In settled unchanged state there are no GPS reads/hashes, ROI queries, redundant
jobs or unchanged reconciliation DML.

The old **24-hour full scan has ceased to be a work trigger**. The retained
`scanInterval` option/function argument does not authorize daily work: completion
sets `next_scan_at` to infinity only when the completed revision covers the current
desired revision. Completing campaign N must not park a newer N+1 campaign; a newer
desired revision or due exact queue obligation remains actionable. This poll is
unrelated to the UI's **10-second Running-job detail** refresh or its Coverage
processing polling.

Near-idle is not zero database queries. Coverage executors still perform small
claim/preflight queries; health/readiness and connection-role checks also remain.
The target is removal of application-attributed unnecessary CPU and history-sized
work, not elimination of legitimate database control traffic.

### OSM Catalog Proof

Separate OSM migration
[010_route_region_catalog_state.sql](../../internal/osm/migrations/010_route_region_catalog_state.sql)
stores `osm_catalog.regions.boundary_sha256` from SHA-256 of NDR EWKB and
`geographic_area_m2` from `ST_Area(boundary::geography)` as generated columns.
Boundary hashing and geographic area calculation occur at **catalog write time**,
not once per GPS point. ROI ordering uses the stored area and deterministic ID
tie-break; catalog sync suppresses unchanged upserts.

The full `fingerprint` preserves the legacy `route-region-catalog-v1` encoding:
all catalog region IDs, configured flags, boundary hashes and active generation
IDs, in deterministic order. The `selection_fingerprint` covers **all** catalog
IDs, configuration, boundaries and configured-active **presence**, but excludes
replacement generation **numbers**. Thus an already-active region's replacement
can preserve selection while its new generation changes the effective match
target. First activation, removal or configuration changes cannot take that shortcut.

Covering only previously selected regions is unsafe. A smaller competing region
can become preferred; even an unconfigured catalog polygon can change whether a
point has a provider at all. Both active configured selection and the all-catalog
point-presence query are covered. Display names are retained separately in head
metadata; a name-only head revision need not create a matching campaign.

Statement triggers serialize relevant region/generation writes against the head
and refresh it transactionally. Equal derived state does not increment revision
or issue an unchanged head UPDATE; progress/timestamp/storage-only updates are
excluded. Rollback publishes nothing. This is a monotonic transactional revision,
not a sequence or promotion-event-only substitute for boundary/config changes.

### Invalidation Matrix

| Input or event | Proof/action | GPS/ROI and job behavior |
|---|---|---|
| Unchanged route, catalog, descriptor and settled queues | Reuse selection and successful applied proof, or exact negative/pending proof | No GPS/ROI, DML or job |
| Route content/source/ownership change, canonical revision/SHA change | Revalidate captured content/source/ownership epoch and exact route identity | Changed/unknown input requires canonical proof; only eligible changed targets enqueue |
| Point INSERT/UPDATE/DELETE, including telemetry edits with identical count/bounds | Statement transition hooks dirty exact old/new account/workout keys and increment mutation version | Recompute canonical input for dirty route; count/bounds equality is never proof |
| Region added/deleted, boundary/configuration changed, configured-active presence changed, including competing/unselected/unconfigured polygons | Selection proof invalid; metadata campaign filters before page limiting | Full-route ROI for invalid selections; no unconditional rematch of every route |
| Replacement generation number only; identical selection proof/policy | Rebind exact selected IDs to current active heads; preserve route identity | No GPS/ROI for rebind; matching only if selected effective target changed |
| Generation changed only outside selected IDs | Effective selected vector remains equal | No GPS/ROI or matching job for already-proven target |
| Rules, sampling, path policy, minimum traversal or production contract changed | Compare complete descriptor and successful applied provenance | Reuse valid selection without GPS; eligible changed matcher target enqueues |
| Ingest handoff, dirty route or terminal root/sibling wake | Exact-account queue recovery independent of campaign eligibility | Drain only due obligations; no ordinary follow-up history scan |
| Replica behind revision/event floor, conflicting same-revision full fingerprint, lost lease or input CAS | Reject/defer and retain durable work/progress | No destructive readiness, cursor advance or stale result |
| No retained trustworthy proof at initial adoption | Bounded initial bootstrap of unknown proofs only | Honest canonical snapshot/ROI cost once; never fabricate old negative state |

### Selection Reuse And Successful Matching

`read_coverage_reconciliation_routes` applies actionability metadata filters before
page limiting, raw snapshots or hashing. Current/live/unchanged authoritative
failed/cancelled targets and valid negative/pending proof are not ordinary work.
`read_coverage_selection_metadata` can return an input-valid raw proof that is
newer than global acceptance or whose selection catalog changed. It must first
be checked against a catalog fenced by both its own and the accepted revision
floor. Presence of that raw proof alone does not authorize reuse.

`rebindCoverageSelection` requires nonempty readiness, an equal nonempty selection
fingerprint, equal `route-selection-v1:auto-add=true|false` policy, and every
selected ID still active. Only generation numbers for those exact IDs change;
the canonical route proof and readiness do not. A full-fingerprint mismatch is
therefore not by itself a reason to reread GPS. A selection mismatch is.

The immutable matching descriptor has exactly five fields: `rules`, `sampling`,
`pathPolicy`, `minimumTraversalMeters`, `productionMatcherContract`. The effective
target additionally includes canonical route revision/SHA and the normalized
region-generation vector. Parent contexts capture the actual producer descriptor;
an in-flight job is never rewritten to match later settings. The runner checks
`app.coverage_production_descriptor_current` before raw GPS/matching. A prepared
builder may adopt the accepted minimum traversal before job construction only
when rules/sampling/path/production contract agree; incompatible producer
contracts fail rather than publish old work after a newer campaign.

Only actual successful `applied`/`no_evidence` persistence stamps applied proof.
Historical initialization may reuse an exact retained successful shipped-V1
immutable job context, including minimum traversal and all target fields. A
cleared current pointer requires an exact successful processing-finish marker;
conflicting retained descriptors are rejected, not guessed. A current-looking
state, enqueue, preparation or later mutable catalog is not successful provenance.

### Unknown And Negative Proof

`pending`/`region_not_active` and `unavailable`/`no_provider_region` selection
results are useful durable proofs, including policy-converted results when auto-add
is disabled. Preserve their catalog fingerprint, selection fingerprint and policy
even with an empty generation vector. They remain reusable while exact input and
selection proof hold; a real selection change makes them actionable again.

Old negative states have no retrospectively known catalog baseline. Do not stamp
today's catalog on them or infer historical absence from an empty selected vector.
The initial campaign may incur one bounded pass of unknown routes, obtaining
honest full-input hashes/ROI proofs, even when no job results. Exact existing
successful job history can avoid unnecessary matching, but cannot manufacture
negative region-selection provenance. Subsequent unchanged polls do not repeat
this bootstrap.

### Queue Recovery, Transactions And Races

Handoff insertion, point mutations and ingest terminal transitions persist exact
account wakes. Keys are `(account_id, transaction_id)`, coalesced per transaction,
not a shared account-row mutex or an ordering comparison of transaction IDs.
Claimers consume only committed wakes; an older concurrent transaction remains
discoverable when it commits. Point transition triggers cover old and new UPDATE
keys without full-route hashing or an account-generation lock in the file
transaction. Proof writers take their short catalog SHARE/NOWAIT fence only in
post-file preparation.

`app.claim_coverage_reconciliation_work` returns `scan_required` alongside the
leased revision/cursor. Queue-only claims replay due handoffs/builders and exact
dirty routes, then release the lease without completing/resetting the campaign
cursor. Queue failure likewise preserves campaign progress. Terminal-root recovery
seals a partial cohort only after the visible root **and all source siblings**
are terminal and due preparation is exhausted. A crashed active source first
undergoes ordinary lease recovery. EOF checks immutable input proof, not global
catalog freshness, so it does not cause redundant ordinary reconciliation.

Application writes CAS-check current lease/revision/runtime, account/source/event,
ownership/content, point mutation version, canonical route revision/SHA, accepted
catalog and full matching descriptor. Apply and proof storage are atomic, with a
final authority check. Crashes replay durable obligations idempotently; campaign
cursors advance only after committed route handling. New committed wakes bypass
prior queue/campaign backoff without erasing a saved campaign cursor. Existing
bounded contention retry, typed readiness deferral and reconciliation failure
backoff remain, not a hot loop. None is an alternate route-job retry mechanism:
failed/cancelled identical targets retain 027/028 authority, cancellation footprints
and consumed automatic/manual retry budgets.

### Failure Semantics And Readiness

Apply, proof recording and prepared rebinding fence `selectionPolicy` against the
accepted catalog policy, not just the selection fingerprint. Campaign completion
parks only a revision that covers the current desired revision; a concurrent newer
campaign stays due. Queue-only completion still cannot complete a campaign.

A genuine new campaign allocates its revision above both the global counter and
every account's desired, active and completed counters. Earlier per-account
bootstrap work can legitimately have advanced an account beyond the global
counter; allocating only `global + 1` can otherwise make new work appear already
complete. The maximum-account lookup occurs only for a changed observation, never
for an identical idle poll. An already accepted observation is not falsified to
restart initialization: a separately recorded, one-time metadata operation can
schedule only accounts whose initial selection proofs remain missing.

Ordinary route ROI has a **30-second** default budget, capped by a quarter of the
lease. Only typed route-local ROI deadline/invalid-data failures receive a
**five-minute route-only deferral** through
`app.defer_coverage_reconciliation_route`. Its lease/input/mutation-version CAS
prevents an old lookup from delaying newly mutated input. The route remains dirty
and pending while other routes continue, so a poison route cannot starve the
account. Cancellation, lease loss, replica lag, permission/transport/systemic or
unknown failures remain cycle-fatal, not disguised as route-local deferrals.
This does not create successful proof or authorize a matching-job retry; ingest's
existing typed readiness deferral remains a separate contract.

Readiness verifies the exact primary keys and valid, ready, live, immediate unique
btree backing indexes of all four private tables: `(account_id, workout_id)` for
route changes, selection proofs and applied proofs, and
`(account_id, transaction_id)` for wakes. Mere table/column presence is insufficient.

### Replica Metadata Fences

Catalog head, promotion head and any ROI lookup share a read-only Repeatable Read
snapshot on one physical OSM backend. Both minimum promotion event and catalog
revision must be visible, including the maximum durable APP route-proof revision.
APP acceptance rejects older revisions/events and rejects an equal revision with
a different full fingerprint (`40001`), rechecking the proof floor under lock.
A lagged snapshot is discarded for reacquisition; it cannot overwrite newer
proof, invalidate a prepared target or advance a campaign cursor.

Strict replica-only policy remains strict: no primary/writer fallback, no
unfenced legacy catalog path and no cross-database latest-state atomicity claim.
Readiness checks the actual APP helper/ACL/RLS/trigger/index/body contracts and OSM
10 generated columns/head/triggers, not just numeric version labels.

## Delivery And Acceptance

The new implementation lives in the `BEGIN PRECISE NEAR-IDLE COVERAGE
RECONCILIATION` section of
[APP 029](../../api/migrations/sql/029_ingest_status_reporting.sql),
[worker metadata](../../worker/coverage_metadata.go) and
[reconciler](../../worker/coverage_reconciler.go). It is an additional behavior
change, not merely the existing source-history renumbering. Full APP runtime
and deployed schema/minimum runtime are **30/30**, baseline **27**, ledger **0-30**.
The separately recorded operator cutover completed at Helm **61**, retaining the
original ledger rows through 30 and Goose row-ID high-water **54**. This was not
an ordinary Goose downgrade or a runtime compatibility branch. Separate native
OSM 010 Up completed on the actual PostgreSQL **17** installation; schema/minimum
runtime **10/10** was verified on the primary and both standbys.

Revision **62** corrects campaign allocation for ahead-of-global account counters
and schedules the missing initial proof campaign. It replaces the observer and
updates reconciliation metadata only; schemas, Goose history and unrelated
application data remain unchanged. Healthy accounts are not restarted.

Native API, shared-worker and OSM images were deployed under the original tag;
UI/Martin stayed unchanged and all **18 configuration specifications** were
preserved. No manual application jobs or provider operations were started.
Private pre-restart checks, with prior writers stopped, verified retained context
and region projections, roles, sequence states and the recorded old-table DML
counters, including zero forbidden APP old-table DML in the owned transaction.
This bounded metadata/preservation proof is not a full raw-data backup/hash or
a universal data-loss guarantee. Fallback image pins were retained at deployment;
that receipt does not establish present registry-wide rollback availability after
older artifact deletions. Existing M8 and historical 34/60/57 receipts are unchanged.

The selected genuine PostgreSQL matrix and full `go test ./...` passed, as recorded
in [delivery status](../development-progress.md#metadata-driven-coverage-reconciliation).
Isolated tests used PostgreSQL **18.6**; actual PG17 OSM native Up and standby
verification do not constitute a full PG17 test matrix or all historical race
coverage. **Initial proof validation and live unchanged-cycle acceptance PASS.**
Canonical metadata checks cover every eligible route in every active account,
not merely the number of proof rows. Initialization drained; desired/completed
campaign revisions agree, leases are released, and no dirty, handoff, prepared,
wake or active application-job obligations remain. Three fresh one-shot cycles
returned `worked=false` without GPS/ROI or job-creation queries. Metadata row/xmin
fingerprints and settled table counters remained unchanged. Low CPU alone was
not used as proof of completion.
This documentation closeout performs no code, database or cluster operations.

Acceptance must combine logical proof with application-attributed CPU/query
observations: settled unchanged routes cause no canonical GPS/hash or ROI calls,
no redundant jobs and no unchanged reconciliation DML, including beyond the former
24-hour interval. Separate necessary bootstrap, dirty-input work, real OSM/matcher
campaigns, executor/health control queries and unrelated database tenants from
idle reconciler cost. Near-idle CPU is the target after required work drains,
not an arbitrary five-minute guarantee while necessary updates are queued.

The selected matrix covers metadata, readiness, route mutation, queue recovery,
matching and review fixes within its recorded scope. It is not blanket completion
of every historical concurrency/retry/cancel scenario. Unit/static or skipped
integration tests are not database or live proof.
Private operational evidence retains account/workout/job identifiers, user counts,
workout dates, GPS and incident measurements/timings; none belongs in this ADR.

### Repeatable Unchanged-Cycle Verification

The production entry point is
[`worker.NewCoverageReconciler(...).RunOnce(ctx)`](../../worker/coverage_reconciler.go),
not a separate approximation of the SQL. An authorized private operator can invoke
it without a service restart, new application job, image build or public endpoint.
The verified operator source and sealed receipts are retained privately with the
revision-62 release; this does not introduce a supported public administration API.

1. Check canonical proof validity for all eligible active-account routes and verify
   no outstanding campaigns, leases, dirty routes, handoffs, prepared cohorts,
   wakes or application jobs. Accepted catalog policy and matching descriptor must
   agree with the operator's actual production configuration.
2. Use the exact deployed source and normal restricted worker pools/configuration,
   including TLS, read-only OSM snapshots and strict standby guards. Keep privileged
   before/after audits separate. Do not copy an executable into a read-only worker
   filesystem or expose credentials in arguments, logs or saved configuration.
3. Trace allowlisted operations after connection warmup. Construct a fresh
   reconciler for each cycle so its in-memory observation timer cannot skip the
   catalog check. Preserve normal APP autocommit behavior and impose short SQL and
   whole-probe deadlines; abort the idle test if real work becomes claimable.
4. Expect three APP operations: accepted catalog floor, catalog observation and
   empty work claim. Expect an OSM read-only transaction with the tiny catalog
   metadata read and rollback. These six pgx-traced operations exclude lower-level
   connection/checkout standby checks; they are not all wire-level SQL. Outer
   `SELECT` tags also do not establish whether a called function changed rows.
5. Compare before/after row identities, values and `xmin`, corroborated by settled
   insert/update/delete counters and health/CPU observations. Empty-queue cleanup
   can execute a DML statement affecting zero rows; acceptance requires no changed
   rows, GPS/hash/ROI work, new jobs or invalidation, not zero SQL statements.
6. Remove the temporary operator and any staged database TLS files, close its
   connections, and verify that service identities, images, configuration and
   readiness remain unchanged.

The live verification used this procedure for three successful fresh cycles.
Shared/asynchronous statistics and CPU observations are corroborating evidence,
not exclusive query attribution. An optional all-route actionable-count query
hit its short timeout and was omitted without retry; no zero-count claim is made
for that query. The measured result is an ordinary idle cycle, not a forced
all-route campaign. Do not change catalog fingerprints, policy, campaign revisions
or timestamps merely to make a test perform work. Test genuine invalidation cases
on disposable synthetic databases instead.

## Consequences

- Steady-state cost depends on small head/queue metadata, not historical GPS size.
- Genuine selection changes still cost bounded full-route resolution; generation
  replacement and matcher-only changes can reuse trustworthy selection proof.
- Persisted proof adds schema/security/readiness obligations and an honest one-time
  bootstrap cost for unknown history; it does not justify invented provenance.
- Event/dirty coverage replaces periodic safety scans, so mutation hooks, monotonic
  catalog fences and queue recovery are correctness requirements, not optional
  performance hints.
