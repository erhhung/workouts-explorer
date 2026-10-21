# OSM Read Replicas

## Scope And Status

Status: 10/13/2026 (`America/Los_Angeles`). Strict replica-only OSM reads are
implemented in the current code and Helm chart, but are **off by default**.
Recorded configuration, protocol/TLS/privacy fixtures and disposable
PostGIS lag/wait integration checks pass within the boundaries below. Live
headless DNS and in-pod TCP checks pass for all three nodes; a separate client
verified direct authentication, hostname-validated TLS and strict standby
selection. Reader Secret-entry verification, activation and rollout remain pending.
These probes do not certify in-pod TLS/authentication.
The backends did not request client certificates: no direct-backend mTLS claim
is made. Final operational proof belongs in
[Telemetry and monitoring](telemetry-and-monitoring.md#private-dns-and-reachability),
not an assumed deployment pass here.

The separately authorized [legacy-route repair](coverage-legacy-route-repair.md)
and normal selective retries are verified: all affected routes have current
durable matches, with no remaining failed or queued repair work. That result does
not prove replica routing or concurrent matcher capacity. M8 image builds,
publication and deployment have not started in this phase; owner acceptance and
an explicitly authorized image/rollout remain separate gates. Private values,
source keyrings, external profiles and host `rclone.conf` must not be read for
documentation or selector verification.
Shared PostgreSQL HA provisioning belongs to the owner's Ansible automation and
is not modified by this application feature.

## Configuration And Secret Selection

The current chart contract separates connection policy from Secret selection. These
are defaults, not instructions to enable the feature:

```yaml
osm:
  useReadReplicasOnly: false
  writerDatabaseSecret:
    name: workouts-explorer-database
    key: osmDatabaseUrl
  writerDatabaseTLSSecret: workouts-explorer-database-tls
  autoAddRegions: false
worker:
  osmDatabaseSecret:
    name: workouts-explorer-database
    key: osmReaderDatabaseUrl
coverageWorker:
  osmDatabaseSecret:
    name: workouts-explorer-database
    key: osmReaderDatabaseUrl
api:
  coverageDiagnostics:
    databaseSecret:
      name: workouts-explorer-database
      key: osmReaderDatabaseUrl
```

`osm.useReadReplicasOnly` is a global **boolean connection policy only**, not a
per-worker or per-account flag, never a Secret selector or endpoint override. Helm
passes it as `OSM_USE_READ_REPLICAS_ONLY` to all three reader Deployments. Runtime
configuration parses a boolean, defaults an unset/empty value to false, and
rejects malformed values without echoing their contents. API diagnostics select
an OSM URL and mount their database TLS Secret only when diagnostics are enabled.

| Reader | Complete Secret reference | OSM URL environment variable | Default name | Default key |
|---|---|---|---|---|
| General worker | `worker.osmDatabaseSecret` (`name`, `key`) | `OSM_DATABASE_URL` | `workouts-explorer-database` | `osmReaderDatabaseUrl` |
| Coverage worker | `coverageWorker.osmDatabaseSecret` (`name`, `key`) | `COVERAGE_WORKER_OSM_DATABASE_URL` | `workouts-explorer-database` | `osmReaderDatabaseUrl` |
| API diagnostics, if enabled | `api.coverageDiagnostics.databaseSecret` (`name`, `key`) | `API_OSM_DATABASE_URL` | `workouts-explorer-database` | `osmReaderDatabaseUrl` |

Each reader **always selects its own name and key**, independently of its
application-database `databaseSecret` and the other readers, regardless of the
boolean. Sharing default names does not create inheritance: changing an
application Secret or one reader reference does not change any other reference.
Both `name` and `key` must be nonempty; a key alone is not a complete reference.
True enforces the strict standby-role policy described below. False uses normal pgx
driver behavior with the same selected URL: a reader URL can still be selected,
and an operator-chosen all-node URL can connect to the primary if its own
`target_session_attrs` permits it. False does not force primary routing, switch to
a writer key or erase a URL's standby requirement. Component TLS mounts remain
selected by `worker.databaseTLSSecret`, `coverageWorker.databaseTLSSecret` and
`api.coverageDiagnostics.databaseTLSSecret`, respectively.

The chart references existing Secrets; it does not create credentials. Secret
member names such as `osmReaderDatabaseUrl`, `osmDatabaseUrl` or an
installation-specific `osmDbUrl` are aliases for stored URL values, not database
roles or policy switches. The URL determines the actual login and connection
parameters. An existing shared Secret can retain application, reader and writer
URL members together; separating references does not require splitting Secrets.
A dedicated OSM reader role can instead have its URL in a separate Secret,
selected independently by each reader with its existing reader TLS property.
Retain application and writer members and verify that the selected reader member
is present in each selected reader Secret (once if shared). If it already
exists, do not recreate or overwrite it merely to follow this guide. Never read
or publish credential values to check selector names.

### Writer Selection

OSM migration and timezone-import hooks use only
`osm.writerDatabaseSecret.name` and `osm.writerDatabaseSecret.key` for
`OSM_MIGRATION_DATABASE_URL`, mounting `osm.writerDatabaseTLSSecret` at
`/tls/prod-db`. Their defaults are `workouts-explorer-database`, `osmDatabaseUrl`
and `workouts-explorer-database-tls`. They no longer borrow the general worker's
OSM key, Secret name or TLS selector. The selected writer URL must use a role
authorized for OSM migrations/imports; a standby-only URL or read-only login
cannot serve that purpose. The three long-running OSM connections are readers,
not a route for writer work. Read-only transactions/routing alone do not prove
least-privilege login permissions.

The operator CLI keeps its existing environment names and defaults:

| Operator environment | Writer selector | Default |
|---|---|---|
| `OSM_DATABASE_SECRET` | Writer Secret name | `workouts-explorer-database` |
| `OSM_DATABASE_KEY` | Writer URL member | `osmDatabaseUrl` |
| `OSM_DATABASE_TLS_SECRET` | Writer TLS Secret | `workouts-explorer-database-tls` |

These are **writer selectors**, including the database options described by
`scripts/osm-update-operator.sh --help`. Operator update/import/identity Jobs
retain their separately supplied writer connection and receive no reader-policy
flag. Operator environment selectors are independent of Helm reader references
and are not automatically synchronized with Helm writer settings either. Supply
non-default writer selectors explicitly; the CLI must not parse private Helm
values to infer them. Application-database URL selectors, including
`worker.databaseSecret.key`, are unrelated and unchanged, as are source snapshots,
transport and file concurrency.

### Existing Values And Minimal Example

The former component key-only properties are **removed** from the closed schema,
which rejects them rather than silently ignoring them. There are no migration
aliases or automatic conversions. The operator must privately convert any local
overrides using this historical source mapping:

| Removed source property | Former borrowed Secret name | Current complete reference |
|---|---|---|
| `worker.osmDatabaseKey` | `worker.databaseSecret.name` | `worker.osmDatabaseSecret.name` and `.key` |
| `coverageWorker.osmDatabaseKey` | `coverageWorker.databaseSecret.name` | `coverageWorker.osmDatabaseSecret.name` and `.key` |
| `api.coverageDiagnostics.osmDatabaseKey` | `api.databaseSecret.name` | `api.coverageDiagnostics.databaseSecret.name` and `.key` |

For each reader, explicitly set the intended Secret name and reader URL key,
then remove its old source property. To retain a previously selected location,
copy the formerly borrowed name into the new reference; it is no longer borrowed
at render time. An explicit API diagnostics override still naming a writer member
must be deliberately changed to a reader member, as must equivalent worker
overrides. Defaults cannot correct retained explicit values. Set separate writer
name/key/TLS selectors explicitly if their historical worker-derived values
differed from the defaults; the existing writer contract is unchanged.

Historical migration note: `osm.readerDatabaseKey` was already removed from the
unshipped global-key design, has no compatibility alias and is rejected by the
closed schema. The deployed v12 release has no such property from this feature,
but the operator must remove it if present in a locally prepared values file.

Use a complete, freshly reviewed values file rather than `--reuse-values`, which
can retain a stale removed property or old component overrides. Verify the
reader entry already exists, or provision it privately if absent, without
changing application or writer members. Preserve existing connection-pool
settings and the intended reader profile during this selector-only conversion.
Agents must not inspect or edit ignored local values, keyrings or credential
values to perform these operator checks.

This minimal values fragment demonstrates strict policy plus all three reader
selectors. The reader selectors are intentionally redundant with their defaults;
it is not a complete deployment values file and contains no URLs or credentials:

```yaml
osm:
  useReadReplicasOnly: true
  autoAddRegions: false
worker:
  osmDatabaseSecret:
    name: workouts-explorer-database
    key: osmReaderDatabaseUrl
coverageWorker:
  osmDatabaseSecret:
    name: workouts-explorer-database
    key: osmReaderDatabaseUrl
api:
  coverageDiagnostics:
    databaseSecret:
      name: workouts-explorer-database
      key: osmReaderDatabaseUrl
```

Replica-only mode requires `osm.autoAddRegions: false`. Helm rejects true/true;
`config.LoadOSM` and Coverage-worker configuration reject
`OSM_USE_READ_REPLICAS_ONLY=true` with `OSM_AUTO_ADD_REGIONS=true` as unsupported.
API diagnostics do not implement auto-add. Automatic region addition remains
deferred, and manual public-data writer Jobs stay independent.

## Headless DNS And Verified TLS

The intended direct OSM endpoint is
`postgresql-postgresql-headless.postgresql.svc.cluster.local:5432`, in
`postgresql`, **not** `monitoring`. The supplied infrastructure observation has
all three Ready data nodes (`role: data`) behind the headless Service, including
the primary. Kubernetes readiness and that label do not identify a standby.
Discover the complete current A/AAAA set dynamically; do not pin pod IPs or
assume two fixed replica endpoints survive promotion/replacement.

The existing database certificate's wildcard parent SAN
`*.postgresql.svc.cluster.local` covers the headless Service hostname. A pod FQDN
such as `<pod>.postgresql-postgresql-headless.postgresql.svc.cluster.local` adds
another label and is **not** covered by that wildcard. Keep the original headless
hostname as TLS `ServerName` while connecting to resolved addresses. Do not
replace it with an IP/pod hostname, disable verification or use a plaintext
fallback to work around a SAN failure. Verify the actual CA chain, hostname and
client certificate privately; certificate renewal/lifetime is operational state,
not a fixed expiration date to record here.

Placeholder-only reader URI shape for the selected private Kubernetes Secret key:

```text
postgresql://<OSM_READER_ROLE>:<URL_ENCODED_PASSWORD>@postgresql-postgresql-headless.postgresql.svc.cluster.local:5432/osm?sslmode=verify-full&sslrootcert=/tls/prod-db/ca.crt&sslcert=/tls/prod-db/tls.crt&sslkey=/tls/prod-db/tls.key&connect_timeout=5&target_session_attrs=standby
```

Substitute credentials only through the approved private Secret workflow, never
in Helm values, committed manifests, argv, shell history or public receipts.
Retain the TLS/authentication parameters appropriate for the existing role; the
illustration is not a credential or proof that password/client-certificate
authentication succeeds directly. Direct PostgreSQL `pg_hba.conf` and role
authentication may differ from Pgpool, so an existing Pgpool URL is not sufficient
probe evidence. Reader-only routing also does not provision a least-privilege
login: production OSM reader permissions remain a separate security requirement.

### Virtual-Cluster Forwarding Boundary

The app-owned [development DNS artifact](../deploy/dev/otel-dns.yaml) specifies
only `postgresql-postgresql-headless.postgresql.svc.cluster.local:1053`, forwarded
to host DNS `10.43.0.10`. Do not forward the whole `postgresql.svc.cluster.local`
zone: the virtual cluster's private application database uses that namespace too.
Preserve existing `observability.svc.cluster.local` and `fourteeners.local`
forwards. Merge the additional entry into the existing `coredns-custom` data;
do not apply a partial ConfigMap as a replacement for other owned entries.

DNS implementation, probing and final evidence are owned by the reachability
section linked above. This documentation change does not edit/apply that artifact
or the shared infrastructure. DNS caching can delay endpoint changes; dynamic
discovery is not a promise of instantaneous failover.

## Connection Policy

`database.OpenOSMReader` first uses `pgxpool.ParseConfig`, preserving the supplied
URL's authentication, CA/client certificate settings and ordered host/TLS fallback
configuration. The strict policy then:

- Copies and shuffles **all** resolved A/AAAA candidates for each new physical
  connection, without mutating the resolver-owned slice or TLS settings. It
  resolves again for a new connection; pooled connection reuse does not rediscover
  DNS for every job.
- Requires `pg_catalog.pg_is_in_recovery()` to return true in `ValidateConnect`.
  The flag overrides URL `target_session_attrs`, including `primary` and
  `prefer-standby`; there is no client-side primary fallback. Adding `standby` to
  the URI expresses intent but is not the enforcement mechanism.
- Rechecks the role in `PrepareConn` on pool checkout. A promoted pooled connection
  is destroyed and acquisition retries within pgxpool's bounds; a failed role
  query rejects acquisition. Session `default_transaction_read_only` is set to
  `on`; matcher snapshots remain repeatable-read/read-only.
- Bounds DNS/role probes to five seconds under the caller's context and supplies
  five-second connect/ping defaults when absent. Caller deadlines bound an entire
  acquisition; multiple address attempts are not one guaranteed five-second
  total connection time.

An acquired connection or held transaction stays on the same physical backend,
even if it is promoted. The policy checks connection/checkout boundaries; it
cannot physically prevent promotion during a transaction or reroute in-flight
SQL. The next checkout rejects that now-primary backend. Standby replay conflicts
may delay or cancel a snapshot; a replica snapshot does not lock the remote
primary against promotion.

Role probes use unnamed extended-protocol `ExecParams`, avoiding named prepared
statement churn and query-tracer exposure of raw role-check errors. Connect and
pool-acquire traces retain safe operational outcomes/cancellation/SQLSTATE while
hiding connection metadata. Caller-side `RedactOSMConnectionError` masks driver
connection errors narrowly, preserving ordinary workload SQL diagnostics rather
than treating every SQL failure as a routing error. Do not publish raw driver
errors, role/authentication details or private URLs while probing.

## Coverage Lag And Admission

Replica-only Coverage runs a five-second OSM reader preflight **before claiming**
a route child. With no usable standby, the cycle reports a safe failure and leaves
queued work unclaimed instead of draining the queue into terminal failures.

After a claim and matcher-slot admission, snapshot initialization has separate
five-second attempts with two-second polling between attempts. Missing or lower
target-region heads are `ErrMatcherReplicaNotReady`, not proof of a genuinely
newer generation. Heads are checked before decoding full metadata, so lagging
incomplete metadata cannot falsely terminalize the child. If any target is missing
or behind, a mixed ahead/behind vector waits too. Once every target is visible,
a genuinely newer active head produces normal supersession rather than stale
coverage persistence. Numeric rollback detection is unsupported; reconciliation
must establish authoritative targets after deliberately promoting an older ID.

The claim and slot heartbeats remain alive while waiting. There is no matcher
execution budget for catch-up: the current source default six-minute initial
match budget starts only after a valid target snapshot is obtained.
`coverageWorker.routeTimeout` configures only the initial budget (runtime range
five seconds through ten minutes). Timeout-only selective retries use
`min(max(initial, 10 minutes), 15 minutes)` on the first retry and fifteen minutes
on the second and later retries, never doubling six minutes to twelve or shortening
a longer initial budget on the first retry. Other retry reasons reset the count.
The default seven-minute lease is a renewable ownership TTL, not the match budget;
twenty-second heartbeats support retries longer than the TTL. The initial budget
plus ten seconds must remain strictly below the lease duration. This source policy
is not yet deployed; earlier repair and release receipts retain their original
three-/six-/ten-minute policy. Cancellation, lease loss and shutdown still interrupt
waiting; authentication, permission and caught-up invalid-metadata failures are
not silently treated as replication lag.

Rejected lagging snapshots discard their physical connection before releasing
it, so the next attempt can discover another caught-up standby instead of
repeatedly reusing the same lagging idle connection. Reconciliation independently
fences readiness against the application's observed promotion head and resolves
that head and region generations within one replica snapshot. A stale reader
cannot advance the campaign cursor or publish obsolete readiness; the existing
campaign retry remains in place.

**Waiting retains the matcher slot.** With an existing global limit of two, even
a few waiters can occupy all admission capacity and block ready matches or API
diagnostics. Reconciliation and System readiness can also lag public-data
promotion because their reads use replicas. A Ready pod does not prove that its
OSM snapshot has the target generations. API diagnostics retain their bounded
synchronous timeout/admission contract; they do not gain the durable runner's
catch-up wait policy.

## Capacity And Authorized Rollout

This flag does not increase worker threads, Deployment replicas or matcher-slot
limits. Shuffling distributes **new connections**, not individual jobs, and does
not guarantee equal backend use or two concurrent jobs on different replicas.
Existing pools can reuse one backend. UI responsiveness, yearly source history,
or roughly thousand-workout imports do not establish reader-backend capacity;
replicas can still overload while source ingestion and reconciliation add reads.

The repair operation leaves its existing global/account/region limits at `2/2/2`,
resources and three-minute base timeout physically unchanged. Chart defaults
remain conservative; do not infer a new capacity recommendation from that receipt.
For a newly evaluated reader rollout, monitor at capacity one before an explicitly
approved increase to two, and never raise an existing maximum of two implicitly.
Measure backend/pool load, lag, waiting slots and interactive latency together;
there is no executed concurrent-capacity benchmark pass for this feature.

1. Review the current [README status](../README.md) and obtain explicit permission
   for image publication and rollout. The completed repair gate alone does not
   authorize either; M8 acceptance stays unchecked until the owner closes it.
2. Select appropriate coordinated API and worker images containing the reader
   implementation and schema-22 runtime support. Both components must support
   minimum runtime 21 before normal Goose runs migrations 021 and 022. The old
   schema-20 binary is not a valid rollback target afterward. The reader flag
   itself adds **no database migration**, and schema/minimum 22/21 stays unchanged.
3. Privately verify each selected reader Secret entry is present (provision it only
   if absent), convert local key-only overrides to explicit name/key references
   and remove obsolete properties as described above. Preserve application and
   writer members, TLS mounts, deliberately selected reader Secret names,
   resources and current concurrency settings.
   Confirm the exact-zone DNS merge and direct role/authentication/TLS probes
   from the application network before enabling `osm.useReadReplicasOnly`.
4. Only after the explicit rollout authorization, enable the global flag with
   auto-add false. Verify all reader selectors and writer-hook separation, queued
   behavior without a standby, target-generation catch-up and ordinary owner-visible
   outcomes. Keep credentials, accounts, workout/job IDs, health counts, source
   titles, GPS, payloads and raw operational output out of Git-eligible evidence.
5. Monitor conservatively before separately authorizing capacity changes. To
   disable strict enforcement, restore the flag to false under the normal rollout
   procedure. Reader Secret selection stays unchanged; primary routing still
   depends on the selected URL. This is not a schema downgrade or HA change.

## Verification Boundaries

Recorded focused checks cover strict flag/default/conflict parsing; full candidate
DNS discovery; verified TLS and unchanged fallback order; primary rejection and
promoted checkout; held-transaction affinity; safe
role/connection telemetry; and invalid role results. Disposable PostGIS fixtures
cover lower/missing heads, catch-up beyond the match budget and initial lease,
renewed claim/slot heartbeats, newer-head supersession, preclaim unavailability,
acquisition races, bounded snapshot initialization and interrupted waits.

The complete-reference contract requires Helm/schema checks for all three default
reader name/key pairs, independently customized reader and application references
under both boolean values, diagnostics enabled/disabled, unchanged independent
writer name/key/TLS overrides and rejection of removed properties without aliases.
Earlier chart receipts for key-only designs do not verify this replacement
contract; documentation-only checks do not rerun chart or runtime tests.

Those integration fixtures simulate replica heads in isolated databases; they do
not prove physical streaming replication, live promotion/DNS/authentication,
balanced jobs, two-reader throughput or production capacity. Skipped integration
tests are not passes. The historical schema-21 M8 results remain distinct from
schema-22 repair checks and these reader-policy checks. See
[contributor verification](../CONTRIBUTING.md#osm-reader-verification) for focused
commands; no app build, live account tool or infrastructure mutation is needed for
documentation verification.
