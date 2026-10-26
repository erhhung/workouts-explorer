# Workouts Explorer Helm Chart

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

Install Workouts Explorer on Kubernetes to browse workout routes on a map, synchronize workout files, and track route coverage. Supply the databases, images, and credentials for your installation; this chart runs the application around them.

## Components

- **UI:** serves the browser application.
- **API:** handles accounts, source settings, workout data, and authenticated tile access.
- **Ingestion worker:** synchronizes sources and imports workout files.
- **Coverage worker:** matches routes against OpenStreetMap data, separately from file ingestion.
- **Martin tile server:** serves private vector tiles internally through the API.
- **Jobs and monitoring:** database migration hooks, timezone import, optional role provisioning and administrator bootstrap, optional telemetry sidecars, Grafana dashboard ConfigMaps, and Pyrra objectives.

## Before Installing

1. Provide reachable external PostgreSQL/PostGIS application and OSM databases. Create the databases, install the required extensions, and provision the migration/runtime identities and their credentials. Prepare the coverage-worker, tile, and security-owner roles and required migration membership through database-owner automation, or enable `api.roleProvisioning.enabled` with a suitably privileged connection, including `CREATEROLE`. This optional job requires the `workouts_migration` role to exist and does not set login passwords or replace your credential provisioning.
2. Prepare the OSM catalog and imported map data for the configured providers and regions outside this chart. Ensure OSM readers can see the required catalog generations and timezone data before relying on matching. The enabled-by-default timezone hook downloads and imports the pinned boundary archive; it needs outbound HTTPS and OSM writer access. If you disable it, supply compatible timezone data yourself.
3. Publish or select compatible application images and configure `image.registry`, `image.tag`, optional `image.digests`, and any existing image-pull Secrets. Martin and collector images use their own complete references.
4. Provision the existing Secrets described below in the release namespace before running Helm, including Secrets needed by pre-install hooks. Set your browser URL, SMTP server, and usable base-map styles. The default style families are complete MapTiler Outdoor/Streets examples with attribution links and resource origins; replace `YOUR_MAPTILER_KEY` in all four style URLs with your browser-restricted public key and follow the provider's licensing requirements. The mail and application domains remain placeholders.
5. Choose browser access and monitoring. Built-in ingress is disabled by default; when enabled, it creates both an Ingress and a Certificate and requires an ingress controller, cert-manager, and the `StepClusterIssuer` named `step-issuer`. Keep `ingress.tlsSecretName` and `ingress.certificateSecretName` aligned. Configure a reachable TLS OTLP endpoint or set `otel.endpoint: ""` to disable export and sidecars. Dashboard ConfigMaps need an existing Grafana importer; Pyrra resources need its CRD/controller and an existing rule evaluator.

The chart does **not** create application/OSM databases, connection or application credential Secrets, OSM map imports, PostgreSQL high availability, DNS infrastructure, an NFS server, or monitoring backends. Its ingress Certificate can ask the existing certificate controller to populate the selected ingress TLS Secret. Resource names are fixed as `workouts-explorer` rather than derived from the release name; use a separate namespace for each installation.

## Workout-Type Map Styles

`api.publicConfig.baseMaps.workoutTypeMappings` selects a style family; it does not rename or reclassify workouts. Set `providerLabel` to the original exported workout name. The application derives a stable key from its Unicode-normalized, case-folded, outer-trimmed identity, which remains unchanged when the UI canonicalizes display names. For example, `Outdoor Cycling` can display as `Outdoor Cycle` while still matching an `Outdoor Cycling` mapping.

Exact provider identity takes precedence. Qualified indoor/outdoor run, walk and cycle aliases have a limited display-label fallback only when the matching aliases select one unambiguous family. Unmapped or conflicting selections use `fallbackFamilyId`; a valid manual map-family selection takes precedence over automatic matching. These mappings are global, not per account or source.

The active chart defaults map `Hiking` to `outdoor` and `Running` to `streets`, with `streets` as the fallback. Supply additional provider labels and families for your installation; existing installation-specific arrays replace these example defaults.

## Credentials And Writes

Values contain **Secret names and keys**, not passwords or connection URLs. A referenced key contains the connection URL or other credential in an existing Secret. References may share a Secret object, but use separate least-privilege database identities for each task. All references resolve in the release namespace.

| Values reference | Access or contents |
|------------------|--------------------|
| `api.databaseSecret.{name,key}` | Runtime application database access for the API; not migration or role-management privileges. |
| `worker.databaseSecret.{name,key}` | Runtime application database access for source synchronization and file ingestion. |
| `coverageWorker.databaseSecret.{name,key}` | Runtime application database access for coverage jobs and their leases/reservations. |
| `tileServer.databaseSecret.{name,key}` | Restricted application database access for Martin tile queries. |
| `api.migration.databaseSecret.{name,key}` | Application schema migration credentials with the required ownership/DDL privileges. |
| `api.roleProvisioning.databaseSecret.{name,key}` | Optional role-management credentials, including `CREATEROLE`; disabled by default. The default migration-key reference does not mean ordinary migration credentials can create roles. |
| `api.bootstrap.databaseSecret.{name,key}` | Application database credentials permitted to bootstrap an administrator; distinct from the administrator account Secret. |
| `osm.writerDatabaseSecret.{name,key}` | OSM schema migration and timezone import writes, not runtime reader access. |
| `worker.osmDatabaseSecret.{name,key}` | Read-only OSM catalog access for the ingestion worker. |
| `coverageWorker.osmDatabaseSecret.{name,key}` | Read-only OSM matching access for coverage workers. |
| `api.coverageDiagnostics.databaseSecret.{name,key}` | Read-only OSM access for API diagnostics, only when enabled. |

Also provide `api.securitySecret` with the private rate-limit key and SMTP password, and `sources.keyringSecret` with the source-configuration encryption keyring shared by API and ingestion workers. Preserve old decryption keys during keyring rotation. Source-account credentials belong in encrypted account source configuration, not chart values. Optional administrator bootstrap uses `api.bootstrap.secretName` and its username/email/password keys; it is disabled by default and does not rotate an existing password unless explicitly requested.

For database TLS, `worker.databaseTLSSecret`, `coverageWorker.databaseTLSSecret`, `api.coverageDiagnostics.databaseTLSSecret`, and `osm.writerDatabaseTLSSecret` mount existing Secrets at `/tls/prod-db`. Connection URLs must refer to the certificate paths actually mounted for their component. Supply client certificates and private keys only where your database authentication requires them; direct backend access does not itself require mutual TLS. API trust uses `api.trustedCASecret`; collector sidecars mount only a public CA bundle and use `otel.trustedCASecret`, falling back to the entire API CA reference when its name is empty. Preserve database hostname verification and the configured TLS server names.

Application and OSM migrations run as pre-install/pre-upgrade hooks, before the new workloads start; optional role provisioning runs first and timezone import follows migrations. Optional administrator bootstrap is a Helm post-install/post-upgrade hook (an Argo CD PreSync hook). These jobs write to your supplied databases. Review backups and database privileges before upgrading; Helm rollback does not undo completed database migrations.

The approved active application sequence is **001-028**, with **no active 029**. Folded 028 implements generation locking, strict eligible cohorts and mixed-failure automatic retries at schema/minimum runtime **28**, marker **27**, source `SupportedSchemaVersion=28`. This contract, exact parking repair and UI follow-up are **deployed in `dev-20261020`, Helm revision 29**, schema/Goose/minimum 28 and marker 27. All four release images are digest-pinned and all five application deployments are Ready/Available. Equal numeric versions do not prove feature/runtime compatibility. Chart semantics remain **frozen**; all 23 release-manifest chart files, including `values.yaml`, are unchanged. This update edits only this README template and its generated README, not values comments, hooks or runtime settings.

The frozen-chart statements above describe that historical release. The subsequent administrator-password debugging setting documented below adds a default-off opt-in; it does not change existing defaults or imply a deployment.

Native Goose does not replay applied 028. An existing old-contract 28 needs a **private guarded offline repair**, explicit operator approval, a fresh protected backup and rehearsal on a protected restore of an **actual application backup** using the actual migration role and released-source/catalog proof. For this release, the approved exact actual-profile rehearsal and transactional live repair passed under the real non-superuser migration login with its existing security-owner SET capability. Full original-tuple hashes and exact ledger 0-28 IDs/timestamps were preserved without data-field exclusions before startup; new readiness passed. This is a frozen-corpus qualification, not generic SQL-lexer equivalence. Old apply-10/defer-6 ABIs are intentionally absent: old runtime-28 readiness fails and new runtime-28 readiness requires private builders. There is **no rolling reuse of old runtime-28 images**. Preserve data, matcher roots/budgets, sources and credentials. Verified protected local rollback exports retain the final whole-database dump, password-free roles and four old OCI images after authorized old-artifact cleanup. Incompatible-image rollback requires a **full old-database restore and matching old OCI images**, never images alone. No global registry GC or blob deletion occurred; physical blob-space reclamation is not claimed. See [Application Database Migrations](../docs/database-migrations.md) and [current release evidence](../docs/development-progress.md#folded-028-production-release-10202026); private repair SQL, backup locations and dataset metrics are not published here.

## Administrator Password Debugging

**WARNING: `api.allowAdminPassword` grants administrator access to ALL user accounts using the administrator password. Enable judiciously ONLY for development/debugging, NEVER in production.** It defaults to `false` and renders `ALLOW_ADMIN_PASSWORD: "false"` in the API ConfigMap. It is independent of `api.localDevelopment`, including for HTTPS debugging; do not weaken origin, TLS, or other authentication controls.

For a generic, render-only example of the explicit opt-in (not a live deployment command):

```sh
helm template debug-example ./helm --set api.allowAdminPassword=true
```

Sign in with an existing active non-administrator user's username or email and either that user's own password or the current password of the sole enabled administrator in the database. The resulting session has the target user's full privileges, including access to private data and user-authorized writes, not administrator privileges. This deliberately bypasses the normal separation of administrator and private user data.

`api.bootstrap.enabled` controls only the bootstrap job. An existing enabled administrator and its Argon2id password hash remain in the database when that job is disabled. The fallback verifies that current database hash; it does not mount the bootstrap Secret in the API or use a plaintext configuration password. An old bootstrap Secret password that no longer matches after rotation does not work. With zero or more than one enabled administrator, fallback is denied. Supplied passwords must meet the current password policy, including `api.passwordMinimumLength`, before either hash is checked when the flag is enabled; an older administrator password below the current minimum is not exempt.

Fallback sign-ins retain the standard distributed network/subject limits and add a shared global limit of 10 attempts per 10 minutes. When enabled, failed own-password attempts that reach fallback consume this budget regardless of identity eligibility or administrator availability, including unknown, inactive, and administrator identities, so limit behavior does not reveal target eligibility. Successful own-password sign-ins do not consume the fallback budget. Successful fallback session creation and an audit record identifying the administrator actor, target user, and account commit atomically. Auditing is not an authorization lifetime control. After debugging, explicitly log out or revoke the debug session and disable the flag. Turning it off stops new fallback sign-ins once applied to the API, but does not automatically revoke already-issued user sessions. See [Administrator Password Debugging](../docs/admin-password-debugging.md) for the security contract and cleanup guidance.

### Administrator Password Rotation

Changing the bootstrap Secret after installation does **not** update the database hash: there is no automatic Secret watcher. `api.bootstrap.enabled=false` omits the job, and skipping Helm hooks skips it during install/upgrade. An API restart does not import the Secret password. With `api.bootstrap.rotateExistingPassword=false`, a different password fails the existing-state check rather than updating it.

Prefer [`scripts/rotate-admin-password.sh`](../scripts/rotate-admin-password.sh). It requires an explicit `--context` and either `--dry-run` for read-only validation or `--yes` for mutation. Dependencies are Bash, `helm`, `kubectl`, `jq`, and `openssl`. First validate using a reviewed context; `development-example` below is synthetic:

```sh
scripts/rotate-admin-password.sh --context development-example --dry-run
```

Only after review and explicit approval, authorize rotation separately:

```sh
scripts/rotate-admin-password.sh --context development-example --yes
```

Optional arguments default to `--namespace workouts-explorer`, `--release workouts-dev`, `--chart <repository>/helm`, and `--timeout 300s` (positive whole seconds with an `s` suffix). Override selectors if the intended installation differs. These are usage examples, not live rotation evidence; automated script tests use mocked cluster commands, not a live deployment.

The script reads installed values into private temporary JSON, validates the native bootstrap template against installed references/image, and selects the **maximum** of the installed Helm minimum and live API ConfigMap `PASSWORD_MINIMUM_LENGTH`; both must be integers in 12 through 64. Dry-run validates without generating a password, patching a Secret, or creating a Job, but does not check database identity matching. With `--yes`, it generates a fresh 64-character hexadecimal password from 256 bits of cryptographic randomness, patches only the configured bootstrap Secret password key with a `resourceVersion` guard, and creates a uniquely named native one-shot bootstrap Job with Helm/Argo CD hook metadata removed. Rendering uses only the private installed-values JSON plus command-local `api.bootstrap.enabled=true`, `api.bootstrap.rotateExistingPassword=true`, and selected-minimum overrides. No values file or stored release values change; there are no persistent bootstrap overrides to restore.

The script runs no `helm upgrade`/`install` or other hooks, restarts no API, and changes no API configuration flags. It does not enable `api.allowAdminPassword`, which remains default-off and development/debugging-only, never for production; no deployment or enablement is claimed here. Neither old nor new passwords are printed or passed as plaintext arguments. Sensitive temporary files are mode `0600` in a `0700` directory with exit/signal trap cleanup. Retrieve a password only through an authorized private Secret-management process, never shared command output.

Before rotation, the Secret username and email must match the single existing active administrator under canonical matching; disabled or additional administrator records cause the Job to fail. The script updates neither identity field. Changing only the Secret email does not update the database email and causes bootstrap mismatch failure; resolve identity alignment separately before rotation. The native job validates policy, prepares an Argon2id hash, and verifies the database hash; a changed hash and rotation audit commit atomically. A password already matching is an idempotent check, not a new rotation audit.

Successful native Job completion confirms the database hash matches the generated password. **If the Secret was patched but completion is unconfirmed, do not blindly roll back or rerun:** the database commit may already have happened. Inspect the reported Job and database/audit state privately and reconcile before retrying; an unconfirmed patch or resource-version conflict also needs inspection. Jobs, including failed Jobs, are left for investigation with a one-hour TTL after finishing, not immediately deleted. See the [full rotation and failure procedure](../docs/admin-password-debugging.md#password-rotation).

**Advanced manual fallback:** if the script cannot be used, render only `templates/bootstrap-job.yaml` using `helm template --show-only templates/bootstrap-job.yaml`, a reviewed private installed-values snapshot, and command-local bootstrap/rotation/minimum overrides, without editing values or release settings. Privately supply exact password bytes without an accidental newline, assign a new standalone Job name, remove hook metadata, and review the image, namespace, Secrets, privileges, and arguments before separately authorizing execution. Keep release bootstrap flags false. **Do not run an ordinary Helm upgrade just to rotate a password:** it can also run application/OSM migrations, optional role provisioning, and timezone-import hooks. See the [advanced manual procedure](../docs/admin-password-debugging.md#advanced-manual-fallback).

Hash rotation alone needs no API restart; subsequent password checks use the current database hash. Raising `api.passwordMinimumLength` or changing other API environment configuration requires a separately reviewed API configuration change and rollout across all replicas. A standalone bootstrap Job does not apply the API ConfigMap or roll out pods. Rotation does **not** revoke any already-issued sessions, including administrator sessions and fallback-issued target-user sessions; they continue until normal expiry or explicit revocation. Explicitly log out/revoke sessions that must end.

## Install Or Upgrade

Keep installation-specific settings in a private values file, for example `private-values.yaml`, outside committed public configuration. Include image selection, public URLs, map styles, and Secret references, **never actual database URLs, passwords, certificates, encryption keys, or source credentials**. Provision those separately through your Secret-management process.

For a compatible fresh install or separately authorized upgrade, after preparing the prerequisites, the generic chart command is shown here. **It does not repair an existing old-contract schema 28 and is not authorization for a live change.** From the repository root:

```sh
helm upgrade --install workouts ./helm \
  --namespace workouts-explorer \
  --create-namespace \
  --values private-values.yaml \
  --wait --timeout 10m
```

The filename is an example, not a file supplied by this repository. Pre-install Secrets must already exist, even when using `--create-namespace`. For an existing release, compare your overrides with the current complete `values.yaml` and schema. Supply a freshly reviewed set of overrides rather than using `--reuse-values`; add `--reset-values` when you need to discard stored release settings. Removed keys such as `bulkRef`, `api.migrationDatabaseKey`, `api.roleProvisioning.secretName`/`databaseKey`, and `api.bootstrap.databaseKey` must not be carried forward. Use the full nested database Secret references instead.

Keep `api.allowAdminPassword`, `api.localDevelopment`, and `api.smtp.allowInsecureLocal` false for public installations. Public map style URLs may contain only browser-restricted public provider keys, never private credentials. Configure each style family's HTTPS `resourceOrigins` for all required tiles, sprites, and fonts; these origins feed the UI content security policy. MapTiler supports browser-restricted public keys; Stadia can use domain authentication.

## OSM Readers And Concurrency

`osm.useReadReplicasOnly` defaults to **false**, allowing primary OSM readers. When true, readers require a standby and check its role at connection and checkout. For strict physical-backend placement, supply a direct all-node/headless service or explicit backend hosts that expose discoverable database nodes, not a proxy that can switch backends. Use consistent reader cluster host/database configuration across coverage and diagnostic callers so their reservations coordinate. DNS resolution and TLS hostname verification must already work; this chart does not configure CoreDNS or create read replicas.

Coverage matching and API diagnostics coordinate in the application database across all pods. They select a **globally least-active eligible backend**, then pin a fresh connection and snapshot to it; this is not per-job DNS round-robin. Eligibility includes the required OSM catalog generations and, in strict mode, standby role. Promotion cannot reroute an already-running transaction. Schema 23 adds a per-backend `reader_limit` default of **1**; existing global, per-account, and per-region matcher admission limits remain unchanged (fresh databases seed each at **1**). This chart does not configure those matcher limits or promise extra capacity from more replicas.

`coverageWorker.jobConcurrency` defaults to **2 per pod**. With `N` pods, the potential worker count is `N * jobConcurrency`, but database admission, reader reservations, and job leases still constrain active work. `api.coverageDiagnostics.concurrency` is also per process. File ingestion is separate: `worker.fileConcurrency` is per process, while `worker.accountFileConcurrency` and `worker.globalFileConcurrency` are database-coordinated across worker pods.

## Automatic Coverage Retries

The following describes the **folded-028 contract deployed in `dev-20261020`**, not a reinterpretation of historical old-contract policy. Production mixed-failure automatic recovery has published authoritative current results; subsequent independent verification closes the historical year's available-map recovery. Owner acceptance and capacity certification remain separate; dataset metrics stay private. See [independent closure](../docs/development-progress.md#independent-yearly-recovery-closure-10202026). Policy stays `coverage-production-v1`, Coverage 500m CPU / 256 MiB, two lanes, B=25 and one additional automatic attempt. The source worker stays 250m / 256 MiB. Default limits, chart defaults and all other resources/infrastructure are unchanged.

`coverageWorker.autoRetryCount` defaults to **1 additional attempt** per logical Coverage retry root. Set it to `0` to disable automatic creation, or an integer up to `20`. It renders `COVERAGE_WORKER_AUTO_RETRY_COUNT`; use the same policy on every coverage-worker pod. After a **30 second** failure cooldown, at least one authoritative timeout/verified-transient eligible FAILED child enables a new isolated chained attempt carrying **all authoritative FAILED siblings**, including permanent, unknown and null-code failures, provided every failed descriptor remains valid. Successful/superseded siblings never rerun; automatic retry never revives cancellation, current/live targets, deleting workouts or inactive accounts. A parent with only permanent/unknown failures stops automatically.

Schema 28 enrolls failures as they occur after migration; it does not retry already-terminal historical failures. Automatic attempt origin, lineage, and spent budget survive worker restarts and lost replies. Raising the setting permits additional attempts up to the new total; lowering it does not cancel jobs already created. Manual retry remains available on the latest failed or cancelled terminal attempt after the automatic budget is exhausted, without resetting that budget. Manual and automatic creation share a serialized constructor. An explicit authenticated manual retry can reverse cancellation for unchanged, still-authoritative inputs, but never reruns successful siblings or steals newer/current/live work. A cleared target pointer requires a matching cancellation finish timestamp, consistent target/generation proof, and unambiguous route-attempt history; missing or conflicting proof is refused. Automatic retries and ordinary scans do not reverse cancellation of an unchanged target. Timeout-only retries preserve the **6m / 10m / 15m** default execution ladder; other failures reset the timeout counter, not the automatic budget.

Only `coverage-route-timeout` and `coverage-transient-failed` (with a verified transient database-boundary cause) trigger automation. Generic map-processing, persistence, unknown/null, limit, validation, permission/authentication, schema/configuration and TLS failures are not themselves transient, but authoritative failed siblings with those causes travel with a qualifying mixed parent. Latest-only authority and all-failed-descriptor validation prevent a transient-only successor from stranding permanent siblings. The durable automatic budget is shared per root across descendants/restarts; manual retry does not reset it. Failure logs expose only bounded stages and validated SQLSTATE metadata, never original driver/query/route details.

Typed graph node, edge, and expansion-layer limits are permanent processing limits, not timeouts, even if the operation context expires concurrently. Safe limit diagnostics contain only the `graph_node` / `graph_edge` / `graph_layer` field enum and a validated numeric bound.

Ordinary reconciliation scans do not create fresh roots for identical authoritative failed or cancelled targets, including historical failures and jobs still acknowledging cancellation. Advancing scan revisions does not reset a retry budget. A genuinely changed route input, OSM generation vector, matcher/rules/sampling/path-policy/minimum-traversal contract, regained readiness, or distinct forced-adoption event can create fresh work. Parent contexts snapshot the matcher contract so changes are distinct from unrelated scan/OSM events.

`worker.coverageBatchSize` / `WORKER_COVERAGE_BATCH_SIZE` defaults to **25** (`1..1000`). During active ingest, each normal parent seals **exactly saved B new eligible matching targets**, globally across regions within its ingest-root/source-sibling scope. Missing maps, current/live/unchanged authoritative failed or cancelled targets and deduplication do not count. Private durable prepared metadata saves B and scope across files/crashes; there is **no placeholder Coverage parent** before full parent and B children seal atomically. Mixed regions are explicitly allowed. Optional `coverage.regionIds` reports the actual child-region union, including retries; `regionId`/`targetGeneration` remain representative anchors, not complete matching scope. Per-route generation vectors, claim fencing and admission are unchanged.

Saved builder B becomes parent capacity at seal; configuration changes do not resize saved cohorts. A retry remains a separate chained parent containing only authoritative unsuccessful predecessors as allowed by manual/automatic policy; spare capacity never accepts fresh ingest work. Successful siblings are not rerun. Existing larger historical parents keep inherited retry capacity rather than dropping failed siblings.

Only leased account-reconciliation recovery may seal a final partial cohort, **after the visible ingest root and all source siblings are terminal and due preparation is exhausted**, never active source EOF. Failed/cancelled roots can legitimately owe derived work for committed data without reopening ingest. Normal metadata/deduplication retains cancellation footprints and never resurrects unchanged cancelled targets. Cancelling ingestion does not erase committed obligations or cancel separate Coverage jobs.

Preparation keeps bounded metadata and only one route's coordinates at a time, with one efficient ROI query per route, not per point. A trusted fingerprint of **all catalog regions/configuration/boundaries/active heads** must match in the same Repeatable Read ROI lookup snapshot and before seal; mismatch invalidates preparation for fresh ROI, never substitutes new heads into an old vector. Separate APP/OSM transactions do not guarantee cross-database latest-state atomicity. The source runner downloads/processes files serially within its job, cleaning before and after the file pipeline; handoff follows commit/transport/slot cleanup. Matching policy, CPU, memory, semantic limits, concurrency and deadlines are not increased.

The folded generation fix makes four same-name constraint triggers initially deferred with exact prior events/WHEN scopes, including 017's changed-current-finish-only Coverage rule; account seeding remains immediate. It removes the early generation fence lock and uses the historic nonlocking same-Repeatable-Read selection helper, preserving source/epoch/lease and other guards. All qualifying increments and data remain atomic at commit. Helper ABI preservation is not old-runtime compatibility.

## Values

**Deployment-status correction:** the table reproduces the current `values.yaml`
comments verbatim. The historical `(undeployed)` labels in `autoRetryCount` and
`coverageBatchSize` are superseded: both folded-028 behaviors are **deployed in
`dev-20261020`, Helm revision 29**, as described above. Those inherited labels are
not current release status. Existing defaults remain unchanged; the new
`api.allowAdminPassword` opt-in defaults to `false` and is not a deployment claim.

Settings are grouped by their top-level values section. Keys are relative to that section, with the type shown beneath each key. Key paths wrap between whole components, packing each line within 30 characters where possible and balancing short final lines. Defaults longer than 40 characters link to their exact source line in [values.yaml](values.yaml) instead of widening the table. All actual defaults remain unchanged. Group comments and populated lists are also documented directly in that file.

### Images

Values under `image`.

| Setting | Description | Default |
|---------|-------------|---------|
| `registry`<br>_(string)_ | Registry prefix for application images; empty uses each image name as written. | `""` |
| `pullPolicy`<br>_(string)_ | Kubernetes image pull policy for application, tile, and collector containers. | `"IfNotPresent"` |
| `pullSecrets`<br>_(list)_ | Existing image-pull Secret references, as a list of objects with a name field. | `[]` |
| `tag`<br>_(string)_ | Application image tag; use published images compatible with this chart's migrations. | `"0.1.0"` |
| `digests`<br>_(object)_ | Optional image-name-to-sha256 digest map for application images; tags remain present. | `{}` |

### Logging

Values under `logging`.

| Setting | Description | Default |
|---------|-------------|---------|
| `format`<br>_(string)_ | Log output format: json or text. | `"json"` |

### UI

Values under `ui`.

| Setting | Description | Default |
|---------|-------------|---------|
| `image`<br>_(string)_ | UI image name, combined with image.registry, image.tag, and optional image.digests. | `"workouts-ui"` |
| `replicas`<br>_(int)_ | Number of UI pods. | `1` |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each UI container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each UI container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each UI container. | `"250m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each UI container. | `"256Mi"` |

### API

Values under `api`.

| Setting | Description | Default |
|---------|-------------|---------|
| `image`<br>_(string)_ | API image name; also used by migration, role-provisioning, and bootstrap jobs. | `"workouts-api"` |
| `replicas`<br>_(int)_ | Number of API pods. | `1` |
| `publicUrl`<br>_(string)_ | Browser-facing application URL, used for links and sign-in origin validation. | `"https://localhost"` |
| `browserSigninOrigins`<br>_(list)_ | Additional allowed browser sign-in origins; include scheme and optional port, without paths. | `[]` |
| `databaseSecret.name`<br>_(string)_ | Name of the runtime API database Secret in the release namespace. | `"workouts-explorer-database"` |
| `databaseSecret.key`<br>_(string)_ | Secret key containing the runtime API database connection URL. | `"apiDatabaseUrl"` |
| `migration.databaseSecret.name`<br>_(string)_ | Name of the application migration database Secret. | `"workouts-explorer-database"` |
| `migration.databaseSecret.key`<br>_(string)_ | Secret key containing a URL with the ownership and DDL privileges required by migrations. | `"migrationDatabaseUrl"` |
| `roleProvisioning.enabled`<br>_(bool)_ | Enable the pre-install/pre-upgrade database role-provisioning job. | `false` |
| `roleProvisioning`.<br>`databaseSecret.name`<br>_(string)_ | Name of the role-provisioning database Secret; it may differ from the migration Secret. | `"workouts-explorer-database"` |
| `roleProvisioning`.<br>`databaseSecret.key`<br>_(string)_ | Secret key containing privileged role-provisioning credentials; the default migration key does not guarantee CREATEROLE. | `"migrationDatabaseUrl"` |
| `securitySecret.name`<br>_(string)_ | Name of the API security Secret. | `"workouts-explorer-security"` |
| `securitySecret.rateLimitKey`<br>_(string)_ | Secret key containing the private rate-limit signing key. | `"rateLimitKey"` |
| `securitySecret.smtpPasswordKey`<br>_(string)_ | Secret key containing the SMTP password; mounted unless smtp.allowInsecureLocal is true. | `"smtpPassword"` |
| `trustedCASecret.name`<br>_(string)_ | Name of the API CA trust Secret; also the default trust source for collector sidecars. | `"workouts-explorer-database-tls"` |
| `trustedCASecret.key`<br>_(string)_ | Secret key containing the PEM CA certificate bundle. | `"ca.crt"` |
| `passwordMinimumLength`<br>_(int)_ | Minimum password length, including for the optional bootstrap administrator. | `12` |
| `allowAdminPassword`<br>_(bool)_ | WARNING: Grants administrator access to ALL user accounts using the administrator password. Enable judiciously ONLY for development/debugging, NEVER in production; independent of api.localDevelopment. Disable afterward and explicitly log out/revoke debug sessions. | `false` |
| `sessionLifetime`<br>_(string)_ | Authentication session lifetime as a Go duration. | `"6h"` |
| `pageSizeMaximum`<br>_(int)_ | Maximum API pagination page size. | `100` |
| `trustedProxyCidrs`<br>_(string)_ | Comma-separated proxy CIDRs whose forwarded client addresses are trusted; empty trusts none. | `""` |
| `localDevelopment`<br>_(bool)_ | Enable local-development authentication behavior; keep false for public installations. | `false` |
| `coverageDiagnostics.enabled`<br>_(bool)_ | Enable coverage diagnostics endpoints; disabled by default. | `false` |
| `coverageDiagnostics.timeout`<br>_(string)_ | Maximum duration of an API coverage diagnostic request. | `"120s"` |
| `coverageDiagnostics`.<br>`concurrency`<br>_(int)_ | Per-API-process diagnostic concurrency; database-wide matcher admission still applies. | `1` |
| `coverageDiagnostics`.<br>`databaseSecret.name`<br>_(string)_ | Name of the diagnostic OSM reader database Secret. | `"workouts-explorer-database"` |
| `coverageDiagnostics`.<br>`databaseSecret.key`<br>_(string)_ | Secret key containing the diagnostic OSM reader connection URL. | `"osmReaderDatabaseUrl"` |
| `coverageDiagnostics`.<br>`databaseTLSSecret`<br>_(string)_ | Existing TLS Secret mounted at /tls/prod-db for diagnostic OSM connections; match URL certificate paths. | `"workouts-explorer-database-tls"` |
| `coverageDiagnostics`.<br>`motorLaneWidthMeters`<br>_(int)_ | Motor-vehicle lane width used by coverage diagnostics, in meters. | `3` |
| `coverageDiagnostics`.<br>`bicycleLaneWidthMeters`<br>_(float)_ | Bicycle lane width used by coverage diagnostics, in meters. | `1.5` |
| `coverageDiagnostics`.<br>`parkingLaneWidthMeters`<br>_(float)_ | Parking lane width used by coverage diagnostics, in meters. | `2.1` |
| `coverageDiagnostics`.<br>`sidewalkSetbackMeters`<br>_(int)_ | Sidewalk offset used by coverage diagnostics, in meters. | `3` |
| `coverageDiagnostics`.<br>`directionalDriftMeters`<br>_(int)_ | Allowed directional drift used by coverage diagnostics, in meters. | `7` |
| `coverageDiagnostics`.<br>`deadEndEndpointAllowanceMeters`<br>_(int)_ | Endpoint allowance for dead ends used by coverage diagnostics, in meters. | `3` |
| `smtp.address`<br>_(string)_ | SMTP server address as host:port; replace the example placeholder. | `"smtp.example.invalid:587"` |
| `smtp.username`<br>_(string)_ | SMTP authentication username; the password is never a values field. | `"workouts-explorer"` |
| `smtp.fromAddress`<br>_(string)_ | Sender email address for application mail; replace the example placeholder. | `"workouts@example.invalid"` |
| `smtp.allowInsecureLocal`<br>_(bool)_ | Allow insecure local SMTP without the password mount; do not enable for public installations. | `false` |
| `bootstrap.enabled`<br>_(bool)_ | Enable administrator bootstrap after Helm installation/upgrade; disabled by default. | `false` |
| `bootstrap`.<br>`rotateExistingPassword`<br>_(bool)_ | Replace an existing bootstrap administrator's password when the job runs. | `false` |
| `bootstrap.secretName`<br>_(string)_ | Existing Secret containing the bootstrap administrator's username, email, and password. | `"workouts-explorer-bootstrap-admin"` |
| `bootstrap.usernameKey`<br>_(string)_ | Bootstrap account Secret key containing the username. | `"username"` |
| `bootstrap.emailKey`<br>_(string)_ | Bootstrap account Secret key containing the email address. | `"email"` |
| `bootstrap.passwordKey`<br>_(string)_ | Bootstrap account Secret key containing the password, supplied to the job through a private file. | `"password"` |
| `bootstrap.databaseSecret.name`<br>_(string)_ | Name of the bootstrap database Secret; independent of the bootstrap account Secret. | `"workouts-explorer-database"` |
| `bootstrap.databaseSecret.key`<br>_(string)_ | Secret key containing the bootstrap database connection URL. | `"migrationDatabaseUrl"` |
| `publicConfig`.<br>`pollingIntervalSeconds`<br>_(int)_ | Browser polling interval for refreshed application data, in seconds. | `30` |
| `publicConfig`.<br>`mapFitPaddingPixels`<br>_(int)_ | Padding around routes when fitting the map viewport, in pixels. | `48` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0].id`<br>_(string)_ | Stable style-family identifier referenced by fallbackFamilyId and workoutTypeMappings. | `"outdoor"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0].label`<br>_(string)_ | Browser-visible style-family label. | `"Outdoor"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0].styles.light`<br>_(string)_ | HTTPS light style URL; only browser-restricted public provider keys may appear here. | [View default](values.yaml#L179) |
| `publicConfig.baseMaps`.<br>`styleFamilies[0].styles.dark`<br>_(string)_ | HTTPS dark style URL; only browser-restricted public provider keys may appear here. | [View default](values.yaml#L181) |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.text`<br>_(string)_ | Browser-visible attribution text. | [View default](values.yaml#L185) |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.links[0].label`<br>_(string)_ | Browser-visible attribution link label. | `"MapTiler"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.links[0].url`<br>_(string)_ | Public map-provider attribution URL. | `"https://www.maptiler.com/copyright"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.links[1].label`<br>_(string)_ | Browser-visible attribution link label. | `"OpenStreetMap"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.links[1].url`<br>_(string)_ | Public map-data attribution URL. | [View default](values.yaml#L197) |
| `publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`resourceOrigins[0]`<br>_(string)_ | Allowed origin for MapTiler styles, tiles, sprites, and fonts. | `"https://api.maptiler.com"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1].id`<br>_(string)_ | Stable style-family identifier referenced by fallbackFamilyId and workoutTypeMappings. | `"streets"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1].label`<br>_(string)_ | Browser-visible style-family label. | `"Streets"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1].styles.light`<br>_(string)_ | HTTPS light style URL; only browser-restricted public provider keys may appear here. | [View default](values.yaml#L210) |
| `publicConfig.baseMaps`.<br>`styleFamilies[1].styles.dark`<br>_(string)_ | HTTPS dark style URL; only browser-restricted public provider keys may appear here. | [View default](values.yaml#L212) |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`attribution.text`<br>_(string)_ | Browser-visible attribution text. | [View default](values.yaml#L216) |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`attribution.links[0].label`<br>_(string)_ | Browser-visible attribution link label. | `"MapTiler"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`attribution.links[0].url`<br>_(string)_ | Public map-provider attribution URL. | `"https://www.maptiler.com/copyright"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`attribution.links[1].label`<br>_(string)_ | Browser-visible attribution link label. | `"OpenStreetMap"` |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`attribution.links[1].url`<br>_(string)_ | Public map-data attribution URL. | [View default](values.yaml#L228) |
| `publicConfig.baseMaps`.<br>`styleFamilies[1]`.<br>`resourceOrigins[0]`<br>_(string)_ | Allowed origin for MapTiler styles, tiles, sprites, and fonts. | `"https://api.maptiler.com"` |
| `publicConfig.baseMaps`.<br>`fallbackFamilyId`<br>_(string)_ | Style-family id used when no workout-type mapping applies; must identify a configured family. | `"streets"` |
| `publicConfig.baseMaps`.<br>`workoutTypeMappings[0]`.<br>`providerLabel`<br>_(string)_ | Original exported workout-type label matched by normalized provider identity. | `"Hiking"` |
| `publicConfig.baseMaps`.<br>`workoutTypeMappings[0]`.<br>`familyId`<br>_(string)_ | Configured style-family id selected for this workout type. | `"outdoor"` |
| `publicConfig.baseMaps`.<br>`workoutTypeMappings[1]`.<br>`providerLabel`<br>_(string)_ | Original exported workout-type label matched by normalized provider identity. | `"Running"` |
| `publicConfig.baseMaps`.<br>`workoutTypeMappings[1]`.<br>`familyId`<br>_(string)_ | Configured style-family id selected for this workout type. | `"streets"` |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each API or associated job container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each API or associated job container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each API or associated job container. | `"250m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each API or associated job container. | `"256Mi"` |

### Tile Server

Values under `tileServer`.

| Setting | Description | Default |
|---------|-------------|---------|
| `image`<br>_(string)_ | Complete Martin image reference, independent of the application registry/tag settings. | [View default](values.yaml#L269) |
| `replicas`<br>_(int)_ | Number of Martin tile-server pods. | `1` |
| `databaseSecret.name`<br>_(string)_ | Name of the tile-server database Secret. | `"workouts-explorer-database"` |
| `databaseSecret.key`<br>_(string)_ | Secret key containing the tile-server database connection URL. | `"tilesDatabaseUrl"` |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each Martin container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each Martin container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each Martin container. | `"250m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each Martin container. | `"256Mi"` |

### Ingestion Worker

Values under `worker`.

| Setting | Description | Default |
|---------|-------------|---------|
| `image`<br>_(string)_ | Worker image name; also used by OSM migration and timezone import jobs. | `"workouts-worker"` |
| `replicas`<br>_(int)_ | Number of ingestion worker pods. | `1` |
| `databaseSecret.name`<br>_(string)_ | Name of the ingestion worker application database Secret. | `"workouts-explorer-database"` |
| `databaseSecret.key`<br>_(string)_ | Secret key containing the ingestion worker application database connection URL. | `"workerDatabaseUrl"` |
| `osmDatabaseSecret.name`<br>_(string)_ | Name of the ingestion worker OSM reader Secret. | `"workouts-explorer-database"` |
| `osmDatabaseSecret.key`<br>_(string)_ | Secret key containing the ingestion worker OSM reader connection URL. | `"osmReaderDatabaseUrl"` |
| `databaseTLSSecret`<br>_(string)_ | Existing TLS Secret mounted at /tls/prod-db; match database URL certificate paths. | `"workouts-explorer-database-tls"` |
| `fileConcurrency`<br>_(int)_ | Maximum concurrent file-processing tasks per worker process. | `2` |
| `accountFileConcurrency`<br>_(int)_ | Database-coordinated concurrent ingestion file limit per account; must not exceed globalFileConcurrency. | `2` |
| `globalFileConcurrency`<br>_(int)_ | Database-coordinated concurrent ingestion file limit across all worker pods. | `4` |
| `autoSyncInterval`<br>_(string)_ | Interval between automatic source synchronizations. | `"24h"` |
| `autoSyncPollInterval`<br>_(string)_ | Interval between automatic-sync scheduler polls. | `"30s"` |
| `autoSyncStaleDays`<br>_(int)_ | Age in days after which automatic synchronization treats source data as stale. | `3` |
| `schedulerLeaseDuration`<br>_(string)_ | Automatic-sync scheduler leadership lease; must exceed autoSyncPollInterval. | `"2m"` |
| `coverageMinTraversalMeters`<br>_(int)_ | Minimum route traversal length used for coverage matching, in meters; also passed to API diagnostics and coverage workers. | `5` |
| `coverageBatchSize`<br>_(int)_ | New folded-028 contract (undeployed): exact new eligible targets per active-ingest normal parent (1 to 1000), globally across regions in root/sibling scope; only terminal-root account recovery seals a final partial. Retries never accept fresh top-up work. | `25` |
| `staging.root`<br>_(string)_ | Worker filesystem path for the staging emptyDir volume. | `"/var/lib/workouts/staging"` |
| `staging.sizeLimit`<br>_(string)_ | Size limit of each worker's staging emptyDir volume. | `"2Gi"` |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each ingestion worker container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each ingestion worker container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each ingestion worker container. | `"250m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each ingestion worker container. | `"256Mi"` |

### Coverage Worker

Values under `coverageWorker`.

| Setting | Description | Default |
|---------|-------------|---------|
| `image`<br>_(string)_ | Application image containing /app/coverage-worker. | `"workouts-worker"` |
| `replicas`<br>_(int)_ | Number of coverage worker pods; increasing this does not raise database-wide admission limits. | `1` |
| `jobConcurrency`<br>_(int)_ | Concurrent coverage jobs per pod; replicas times this value is only a potential worker count, still bounded by database admission and reader reservations. | `2` |
| `autoRetryCount`<br>_(int)_ | Additional automatic Coverage attempts per root (0 to 20, 0 disables), durable across manual retries/descendants, with a 30s cooldown. New folded-028 policy (undeployed): one eligible transient/timeout failure carries all authoritative failed siblings, never successes; all-permanent failures stop. | `1` |
| `databaseSecret.name`<br>_(string)_ | Name of the coverage worker application database Secret. | `"workouts-explorer-database"` |
| `databaseSecret.key`<br>_(string)_ | Secret key containing the coverage worker application database connection URL. | `"coverageWorkerDatabaseUrl"` |
| `osmDatabaseSecret.name`<br>_(string)_ | Name of the coverage worker OSM reader Secret. | `"workouts-explorer-database"` |
| `osmDatabaseSecret.key`<br>_(string)_ | Secret key containing the coverage worker OSM reader connection URL. | `"osmReaderDatabaseUrl"` |
| `databaseTLSSecret`<br>_(string)_ | Existing TLS Secret mounted at /tls/prod-db for coverage database connections; match URL certificate paths. | `"workouts-explorer-database-tls"` |
| `pollInterval`<br>_(string)_ | Interval between coverage job queue polls. | `"1s"` |
| `admissionInterval`<br>_(string)_ | Interval between attempts to obtain database-wide matcher admission. | `"1s"` |
| `leaseDuration`<br>_(string)_ | Renewable coverage job ownership TTL, not the match timeout; must exceed routeTimeout plus 10s. Heartbeats support retries longer than this TTL. | `"420s"` |
| `heartbeatInterval`<br>_(string)_ | Interval between coverage job lease heartbeats. | `"20s"` |
| `routeTimeout`<br>_(string)_ | Initial route match timeout only (runtime range 5s to 10m); timeout-only retries use at least 10m on the first retry and 15m thereafter, capped at 15m without shortening a longer initial budget. | `"360s"` |
| `reconciliationPollInterval`<br>_(string)_ | Interval between coverage reconciliation coordinator polls. | `"30s"` |
| `reconciliationScanInterval`<br>_(string)_ | Interval between full coverage reconciliation scans. | `"24h"` |
| `reconciliationPageSize`<br>_(int)_ | Number of records processed per coverage reconciliation page. | `10` |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each coverage worker container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each coverage worker container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each coverage worker container. | `"500m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each coverage worker container. | `"256Mi"` |

### OSM

Values under `osm`.

| Setting | Description | Default |
|---------|-------------|---------|
| `useReadReplicasOnly`<br>_(bool)_ | Require standby OSM readers, checking role on connection and checkout; strict matcher placement needs direct discoverable backends. False allows primary readers. | `false` |
| `writerDatabaseSecret.name`<br>_(string)_ | Name of the OSM writer database Secret. | `"workouts-explorer-database"` |
| `writerDatabaseSecret.key`<br>_(string)_ | Secret key containing the OSM writer connection URL with the required migration/import privileges. | `"osmDatabaseUrl"` |
| `writerDatabaseTLSSecret`<br>_(string)_ | Existing TLS Secret mounted at /tls/prod-db for OSM writer jobs; match URL certificate paths. | `"workouts-explorer-database-tls"` |
| `dataProviders[0]`<br>_(string)_ | Default OSM data provider. | `"geofabrik"` |
| `regions[0]`<br>_(string)_ | Default provider-qualified OSM region identifier. | `"geofabrik:norcal"` |
| `autoAddRegions`<br>_(bool)_ | Allow workers to request additional OSM regions; this does not make the chart import map data. | `false` |
| `maxAutoDownloadBytes`<br>_(int)_ | Maximum size in bytes for an automatically requested OSM download. | `1073741824` |
| `timezoneBoundaries.enabled`<br>_(bool)_ | Run the timezone boundary import before installation and upgrade. | `true` |
| `timezoneBoundaries.release`<br>_(string)_ | Timezone boundary release identifier recorded by the importer. | `"2026c"` |
| `timezoneBoundaries.sourceUrl`<br>_(string)_ | Public HTTPS URL of the timezone boundary GeoJSON ZIP archive. | [View default](values.yaml#L439) |
| `timezoneBoundaries.sha256`<br>_(string)_ | Expected SHA-256 checksum of the downloaded timezone boundary archive. | [View default](values.yaml#L441) |
| `resources.requests.cpu`<br>_(string)_ | CPU request for each OSM job container. | `"25m"` |
| `resources.requests.memory`<br>_(string)_ | Memory request for each OSM job container. | `"64Mi"` |
| `resources.limits.cpu`<br>_(string)_ | CPU limit for each OSM job container. | `"250m"` |
| `resources.limits.memory`<br>_(string)_ | Memory limit for each OSM job container. | `"256Mi"` |

### Sources

Values under `sources`.

| Setting | Description | Default |
|---------|-------------|---------|
| `keyringSecret.name`<br>_(string)_ | Name of the source encryption keyring Secret. | `"workouts-explorer-source-encryption"` |
| `keyringSecret.key`<br>_(string)_ | Secret key containing the source encryption keyring JSON; preserve decryption keys when rotating. | `"keyring.json"` |
| `localRoots[0]`<br>_(string)_ | Default allowed local source root; the path alone does not provision storage. | `"/data/workouts"` |
| `nfs.enabled`<br>_(bool)_ | Mount the supplied NFS export into ingestion worker pods. | `false` |
| `nfs.server`<br>_(string)_ | Existing NFS server hostname or address; required when enabled. | `""` |
| `nfs.path`<br>_(string)_ | Existing NFS export path; required when enabled. | `""` |
| `nfs.mountPath`<br>_(string)_ | Worker mount path for the NFS export; keep it within an allowed sources.localRoots path. | `"/data/workouts"` |
| `nfs.supplementalGroup`<br>_(int)_ | Supplemental group id used by ingestion worker pods to read the NFS export. | `1000` |

### Telemetry

Values under `otel`.

| Setting | Description | Default |
|---------|-------------|---------|
| `endpoint`<br>_(string)_ | External OTLP endpoint; set empty to disable export and collector sidecars. Replace the default with a reachable endpoint if exporting. | [View default](values.yaml#L485) |
| `environment`<br>_(string)_ | Deployment environment label attached to telemetry. | `"development"` |
| `serviceNamespace`<br>_(string)_ | OpenTelemetry service namespace label. | `"workouts-explorer"` |
| `trustedCASecret.name`<br>_(string)_ | Existing collector CA Secret name; empty falls back to api.trustedCASecret.name and key. | `""` |
| `trustedCASecret.key`<br>_(string)_ | PEM CA bundle key used only when otel.trustedCASecret.name is nonempty. | `"ca.crt"` |
| `collector.enabled`<br>_(bool)_ | Enable collector sidecars when otel.endpoint is nonempty. | `true` |
| `collector.image`<br>_(string)_ | Complete collector image reference, independent of application registry/tag settings. | [View default](values.yaml#L502) |
| `collector.resources`.<br>`requests.cpu`<br>_(string)_ | CPU request for each metrics collector sidecar. | `"25m"` |
| `collector.resources`.<br>`requests.memory`<br>_(string)_ | Memory request for each metrics collector sidecar. | `"64Mi"` |
| `collector.resources.limits.cpu`<br>_(string)_ | CPU limit for each metrics collector sidecar. | `"250m"` |
| `collector.resources`.<br>`limits.memory`<br>_(string)_ | Memory limit for each metrics collector sidecar. | `"256Mi"` |

### Grafana Dashboards

Values under `grafanaDashboards`.

| Setting | Description | Default |
|---------|-------------|---------|
| `enabled`<br>_(bool)_ | Create dashboard ConfigMaps; disable for test releases that would overwrite dashboards with the same UIDs. | `true` |

### Pyrra SLOs

Values under `pyrraSLOs`.

| Setting | Description | Default |
|---------|-------------|---------|
| `enabled`<br>_(bool)_ | Create Pyrra ServiceLevelObjective resources; disabled by default. | `false` |
| `skipBacksync`<br>_(bool)_ | Add the skip-backsync guard for Pyrra-generated rule ConfigMaps in namespace-sync vClusters. | `false` |
| `window`<br>_(string)_ | Rolling evaluation window for service-level objectives. | `"2w"` |
| `targets.apiAvailability`<br>_(string)_ | API availability target percentage. | `"99.9"` |
| `targets.apiLatency`<br>_(string)_ | Percentage of API requests meeting the configured latency objective. | `"95"` |
| `targets.tileSuccess`<br>_(string)_ | Successful tile request target percentage. | `"99.9"` |
| `targets.tileLatency`<br>_(string)_ | Percentage of tile requests meeting the configured latency objective. | `"99"` |

### Ingress

Values under `ingress`.

| Setting | Description | Default |
|---------|-------------|---------|
| `enabled`<br>_(bool)_ | Create both Ingress and Certificate resources; requires an ingress controller, cert-manager, and StepClusterIssuer step-issuer. | `false` |
| `className`<br>_(string)_ | Ingress class name; empty leaves controller selection to cluster defaults. | `""` |
| `annotations`<br>_(object)_ | Annotations applied to the Ingress resource. | `{}` |
| `host`<br>_(string)_ | Primary browser-facing hostname; required when ingress is enabled. | `""` |
| `additionalHosts`<br>_(list)_ | Additional browser-facing hostnames included in ingress rules and certificate DNS names. | `[]` |
| `tlsSecretName`<br>_(string)_ | TLS Secret consumed by the ingress controller; normally match certificateSecretName. | `"workouts-explorer-ingress-tls"` |
| `certificateSecretName`<br>_(string)_ | TLS Secret populated by the chart's cert-manager Certificate; normally match tlsSecretName. | `"workouts-explorer-ingress-tls"` |

## Regenerate Documentation

Edit `helm/values.yaml` comments and `helm/README.md.gotmpl`, not the generated README. From the repository root, using **helm-docs v1.14.2**:

```sh
helm-docs --chart-search-root helm --sort-values-order file
```

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
