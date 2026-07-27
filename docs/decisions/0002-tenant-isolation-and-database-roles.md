# ADR 0002: Tenant Isolation And Database Roles

## Status

Accepted

## Context

Workouts Explorer stores sensitive health, location, source, job, and diagnostic
data for multiple private accounts. Application code must enforce account scope,
but a missing SQL predicate must not silently expose another account's data.

The API and worker use pooled PostgreSQL connections. The worker also needs to
discover and claim work across accounts before processing each job within one
account. Administrators manage account lifecycle and shared infrastructure but
must not gain access to private account data.

The database therefore needs defense-in-depth tenant isolation without granting
ordinary runtime roles unrestricted row-level-security bypass.

## Decision

### Authorization layers

Application-layer authorization remains mandatory. The authenticated identity
determines the account and role before a repository or query is called; account
identity is never accepted from an untrusted request parameter.

PostgreSQL row-level security (RLS) additionally protects account-owned private
tables. RLS is selective rather than universal: global identity, invitation,
administrative audit, and safe queue-control data use explicit grants and
application authorization where tenant policies do not fit their purpose.

### Account-owned tables

Private tables carry an explicit non-null `account_id` wherever practical,
including private source, job diagnostic, workout, route, map-selection,
coverage, notification, and preference data. This deliberate denormalization
makes policy scope visible and avoids relying on long join chains for isolation.

Tables protected by tenant policies use `FORCE ROW LEVEL SECURITY` so their
owners do not accidentally bypass policies during ordinary access. Migration
ownership remains separate from runtime access.

### Tenant context

Tenant context is set only within a database transaction using transaction-local
settings. Policies read the current account from that context and reject access
when it is missing or malformed.

Repositories that access RLS-protected data run inside a transaction that:

1. derives the account from trusted authentication or claimed-job state;
2. sets the transaction-local account context;
3. performs all scoped statements; and
4. commits or rolls back before returning the pooled connection.

Session-level tenant settings are prohibited because they can leak between
users through a connection pool.

### Runtime and ownership roles

Use separate least-privilege roles for:

- schema migration and object ownership;
- API runtime access;
- worker runtime access;
- private tile functions;
- OSM import and maintenance; and
- OSM read access.

API and worker runtime roles are `NOBYPASSRLS` and do not own protected tables.
The migration role is not used by a long-running application process. The tile
role is introduced with the map milestone and may execute only approved tile
functions or views.

Infrastructure provisioning creates databases, login roles, and credentials.
Goose migrations create application schemas, tables, policies, approved
functions, and grants within the authority provisioned to the migration role.

### Cross-account orchestration

Cross-account operations use narrowly scoped database functions rather than a
general `BYPASSRLS` runtime role. Examples include claiming the next eligible
job and evaluating scheduled-work candidates.

Such functions:

- are `SECURITY DEFINER` only when required;
- have a fixed safe `search_path`;
- are owned by a non-login owner;
- validate all arguments;
- return only safe orchestration metadata;
- grant execution only to the required runtime role; and
- are covered by privilege and cross-account tests.

After a worker claims a job, it starts an account-scoped transaction before
reading or changing private job parameters or domain data.

### Administrator boundary

Administrative identities do not receive a database role that can browse
private tenant tables. Administrative APIs use narrowly scoped queries over
account lifecycle and sanitized operational records. Account deletion invokes
explicit orchestration and purge operations rather than granting an
administrator general private-data access.

### Verification

Database integration tests must cover:

- absent, invalid, and foreign tenant context;
- reads, inserts, updates, and deletes across two accounts;
- pooled-connection reuse after commit and rollback;
- API and worker role grants;
- cross-account queue functions and their returned fields;
- administrator denial from private tables;
- tile-function account isolation when introduced; and
- migration ownership without runtime ownership.

Application-level account-isolation tests remain required for every private
resource route even when the underlying table has RLS.

## Alternatives Considered

### Application authorization without RLS

Explicit account predicates are still required, but relying on them alone makes
one omitted predicate a potential cross-account disclosure. The sensitivity of
health and route data justifies database defense in depth.

### Universal RLS

Applying tenant policies to every identity, invitation, administration, and
queue-control table would make legitimate global workflows difficult to reason
about and encourage broad bypass roles. Selective RLS keeps the strongest
boundary around private account data.

### Worker role with `BYPASSRLS`

A bypass role would simplify queue scans but would leave worker query mistakes
unprotected throughout ingest and deletion. Narrow orchestration functions keep
cross-account authority small and reviewable.

### Session-level account settings

Session settings are simple but unsafe with pooled connections. A missed reset
could run a later request under the previous account.

## Consequences

### Positive

- Missing account predicates are less likely to expose private data.
- API and worker use the same transaction-scoped tenant model.
- Cross-account authority is explicit and auditable.
- Administrators remain operationally capable without private-data access.
- The schema exposes tenant ownership directly for tests, indexes, and purges.

### Negative

- Scoped reads require transaction boundaries even for simple queries.
- Explicit `account_id` columns add storage and consistency constraints.
- Queue claiming and scheduled orchestration need carefully secured functions.
- Local and CI database setup must provision and test several roles.
- RLS policy changes become security-sensitive migrations.

## Conditions That Would Trigger Reconsideration

Reconsider this strategy if PostgreSQL limitations make transaction-scoped RLS
incompatible with required PostGIS or tile workloads, if audited policy
complexity creates greater risk than it removes, or if a future sharing model
requires authorization rules that cannot be represented safely by account
scope. Any replacement must retain application authorization and equivalent
cross-account isolation tests.
