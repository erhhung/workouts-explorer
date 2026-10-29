# ADR 0013: Canonical coverage display identity

- Status: Accepted; implemented and deployed
- Date: 10/24/2026
- Owners: Product and engineering

## Context

OSM logical-path connectivity, source-way lineage and park attribution remain
essential provenance, but are not sufficient user-facing row identities. Separate
source components can resolve to the same displayed name and place. Counting or
highlighting just one component gives inconsistent table, hover and map results.
This is a generally applicable read-model policy for every account, not a patch
for particular names, routes or data. No owner data is needed to specify it.

The policy and coordinated API/UI behavior are **implemented and deployed**;
current source combines them with compact display facts and targeted stats in
[030_coverage_display.sql](../../api/migrations/sql/030_coverage_display.sql).
Final source/runtime `SupportedSchemaVersion=30`, schema/minimum runtime **30**,
baseline **27** and ledger **0-30** define the source contract, not a completed live
rewrite. See the [history mapping](../database-migrations.md#source-history-mapping)
and [pre-cutover actual status](../database-migrations.md#pre-cutover-live-status).
The original ADR date records the policy decision, not a deployment receipt.
See [current release status](../development-progress.md#current-status)
and [Milestone 8 closeout](../development-progress.md#milestone-8-closeout) for
current verification and acceptance boundaries.

## Decision

### Named Identity And Normalization

Policy `coverage-display-identity-v1` keys named groups by **region scope +
normalized actual DISPLAY name + normalized actual DISPLAY place**. Use the
resolved user-facing name and locality/context, not direction-stripped OSM names.
When there is no locality, the region fallback is represented by the region scope
and empty locality. Region IDs separate regions; hidden municipality/county IDs,
source kind, broad class, connectivity component, source UUID, generation,
viewport, date range and checked/focused subset are not additional named keys.
Account isolation is enforced independently; account ID is not a hash input.

Normalization is pinned to **PostgreSQL 18, UTF8, built-in
`pg_catalog.pg_c_utf8`**: NFC, Unicode casefold under that collation, then NFC
again. Collapse explicit Unicode `White_Space` runs to ASCII space and trim that
space: U+0009-000D, U+0020, U+0085, U+00A0, U+1680, U+2000-200A, U+2028-2029,
U+202F, U+205F and U+3000. Do not substitute session-locale lowercasing, browser
locale rules or regex locale-dependent space classes. The PG18 policy/runtime
guard is deliberate; a different Unicode implementation needs explicit review,
not silently different identities under v1.

Preserve directions, accents, punctuation and suffixes. For synthetic examples,
`Example Trail East` and `Example Trail West` remain distinct, as do
`Example-Trail` and `Example Trail`; NFC-equivalent accent encodings may agree,
but accents are not removed. Names blank after normalization retain a
`source_kind/source_id` discriminator. Fallback labels such as **Unnamed road**,
**Bike path** and **Trail** are never merge keys.

The deterministic UUIDv8 hashes a versioned namespace tuple:
`workouts-explorer/coverage-display`, policy version, region, normalized name,
normalized locality, discriminator (empty for named groups). Prefix **each**
UTF-8 component with its four-byte network-order octet length; SHA-256 supplies
the first 128 bits, with explicit UUID version-8 and RFC variant bits. Retain
normalized key fields beside UUIDs. Inconsistent keys sharing a UUID within the
account's retained contexts abort preparation with `23505`, never silently merge.

### Membership, Visits And Geometry

Named paths and parks with the same resolved key form **one group**, including
disconnected components and mixed broad classes. Deterministic representative
kind/class/name/place is presentation only: prefer a park member when present,
then deterministic C-collated metadata/source-ID ordering. Preserve each member's
source kind and UUID for geometry qualification and hit prioritization.

Membership is resolved for a date context, not stored as a permanent
source-to-canonical assignment. Source names and base locality use all eligible
history through compact facts in 030. In-range state/national-park context overrides path locality;
region uses the in-range evidence minimum when available. Context-only evidence
still participates even when its segment does not qualify for path statistics.

Resolve **all historically eligible sources**, not only in-range sources.
Historical context/region fallback applies to a source with no in-range visit.
If that source resolves to a group's key, include its entire eligible history.
Union distinct workout IDs across all members **before** either range or all-time
summaries; one workout visiting multiple members counts once. Return list/stats
only for groups with an actual in-range visit. First/latest navigation preserves
date order, earliest start on that date, then UUID, including latest-date ties.
The same resolved key has the same UUID across ranges; a changed display context
can legitimately move a source to another group.

Checked/focused workouts affect rendered geometry only, not membership, metadata,
counts or buckets. Detail highlights the union of actual eligible in-range
matched pieces across the whole group; tiles render checked pieces and focus
renders focused pieces. Preserve real disconnections and gaps. Never invent a
connecting line, simplify away evidence, or claim unvisited geometry.

### Context Read Model And Invalidation

Private security-owner tables with forced RLS retain revisions, contexts, members,
deduplicated visits, entities and transaction invalidations. Context scope is
**account + public generation + display revision + inclusive dates + policy +
label locale** (`en` in v1). The API calls VOLATILE SECURITY DEFINER
`app.prepare_coverage_display_context(account,session,selection,generation)` in
the existing Repeatable Read selection transaction. It validates the capability,
atomically builds/reuses a ready context and binds the selection through a
composite-account FK. No half-built context is readable. Read-only hover/detail
and tile requests never allocate or rebuild contexts.

Preparation garbage-collects obsolete or older-than-one-day unreferenced scopes
and keeps at most **eight reusable unreferenced contexts** per account. This is a
soft limit: valid actively referenced contexts are pinned and may exceed eight.
Account deletion cascades contexts and children safely; stale selection binding
cleanup nulls only the context column, not account identity. Build failure,
savepoint rollback and invalidation failure remain atomic.

Invalidate every read-model input: match evidence and segment projections,
path/park names, locality and classes, park attribution, workout dates/start,
logical/physical deletion, route presence, applied-result eligibility and region
catalog labels, including context-only evidence. Timestamp-only/no-input changes
need not invalidate. Coalesce **one display revision and one public generation
advance per affected account/transaction**, using deferred flush, not eager
context rebuilding. Keep generation-before-revision lock order and 030's
post-serialization segment-key expansion. Do not take an account generation lock
at the beginning of ingest/provider work. Both READ COMMITTED commit orders must
see correct evidence; stale Repeatable Read must propagate `40001` rather than
publish stale projections. Deletion tombstones survive until flush without
resurrecting deleted accounts. Existing budgets/retries are unchanged.

### API, UI And Tiles

Selection JSON requires `coverageDisplayPolicy` and `coverageDisplayRevision`
from preparation. Bulk/table, hover and detail cache validity and late-response
guards include both fields. Existing client-side search, unnamed filtering,
sorting and paging remain; server search applies **after grouping**.

List and stats retain their ordered metadata ABI. Stats becomes an authorized
indexed canonical lookup, without full-account aggregation or geometry. The API
detail endpoint keeps its six arguments/response but calls the replaced stored
`app.map_selection_coverage_entity_detail`, with transaction-local `jit=off`.
That reader gates whole-group geometry on canonical metadata in one statement;
there is no second source-only API geometry implementation.

All four `coverage_mvt` layers (`coverage`, `coverage_parks`, `coverage_focus`,
`coverage_parks_focus`) and `map_selection_coverage_focus` use the same ready
context for canonical `entityId`, representative `entityKind`, metadata, exact
range count and `countBucket`. Preserve `sourceEntityId`/`sourceEntityKind` for hit
priority and source geometry; a path feature can represent a canonical park
group. Never hash tile-local labels. Account/session/expiry/current-generation
authorization remains mandatory; Martin identity, flags and private proxy remain
unchanged. Earlier click-highlight responsiveness/cancellation/cache improvements
are deployed as part of this coordinated release, not a separate release contract.

## Consequences And Release Gates

030 advances schema/minimum runtime to **30**, retaining baseline **27**. Native
migration seeds display revisions and advances public generation once per account
to invalidate source-ID selections. It does not rewrite workouts, GPS, manifests,
match evidence, job history, matcher states/rules/generation vectors or OSM data.
Runtime readiness validates exact new function bodies, ownership, ACLs, forced
RLS, indexes and triggers, not just the schema number.

This is **not** an OSM derivation, matcher or path-policy bump. **No OSM
regeneration, download or route rematching is needed.** Coverage "recomputation"
for this release means warming/rebuilding canonical read-model contexts from
existing evidence during selection preparation, not creating route jobs.

Source verification, protected actual-backup restore/migration/rollback rehearsal,
fresh backups and matching old images, and coordinated runtime migration/cutover
are release safety requirements. The policy is deployed under historical numbering;
the separately authorized source-squash reconciliation is still pending here.
030 is forward-only; rollback restores the
matching database and images together. Do not change resource/concurrency/retry
budgets, source auto-sync, admin-password debug, TLS/OSM replica policy or Martin
flags/identity. Keep all dumps/archives on the approved NFS temporary archive tree,
linked from the matching private `deploy/internal` bundle; monitor workspace,
`/tmp`, NFS and application-PG filesystem space/inodes regularly. Public examples
and test evidence must be synthetic; private operational identifiers and data do
not belong here. See [migration requirements](../database-migrations.md#canonical-coverage-display-030),
[current release status](../development-progress.md#current-status) and
[Milestone 8 closeout](../development-progress.md#milestone-8-closeout).
Historical source-only test checkpoints do not establish the current release's
browser or milestone acceptance outcome.
