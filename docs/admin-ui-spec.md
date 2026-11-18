# Administrator UI Specification

## Status And Decision

**Accepted M10 scope, 11/15/2026; implementation in progress.** The first
authentication/workspace slice was deployed at Helm 153; the Users review at
154 is extended with dates, usage and invitation lifecycle at Helm 155-157 and
disabled-action/usage-dialog presentation at Helm 158 and account disable/enable
at Helm 159,
at application schema/minimum runtime **32/32**. The separate owner Data Sources
rollout at Helm 167 advances the application to **33/32** without broadening admin
authority. This document defines
the complete target; remaining lifecycle detail, worker dispatch, Kubernetes
controller and other workflows are not claimed implemented. Password recovery
is self-service; an administrator-triggered reset-email action is out of scope.

### Implementation Status

| Area | Current status |
|---|---|
| Login and isolation | Shared login/reserved `admin`, independent cookies/CSRF/session checks, role routing and separate query caches implemented |
| Bootstrap adoption | Explicit `bootstrap-admin --adopt-admin-username --username=admin` implemented; live principal/hash preserved, sessions revoked and Secret username aligned |
| Workspace | Four tabs, guarded 1-4 shortcuts, Stats-style left navigation, deep links and retained category selection implemented; compact sign-out keeps its pending label/appearance stable |
| Accounts | Combined directory, Registered dates, bounded usage, invitation lifecycle, disable/enable, conditional session revocation and background account deletion implemented; richer lifecycle detail remains pending; password recovery is self-service only |
| Settings | Curated read-only effective configuration and identity/recovery link implemented; persistent console preferences, password change and session-management controls remain pending |
| OSM Maps / Operations | Navigation only; controller, maintenance/tasks, announcements, audit and monitoring/recovery completion remain pending |
| Verification | 985 UI tests, Go checks and native32 lifecycle/readiness/isolation checks pass; Chromium dark/WebKit light verify disable/enable, credential-verified sign-in feedback and desktop/tablet containment; earlier strict-TLS SMTP and invitation/usage receipts retain their scope |

See [development progress](development-progress.md#admin-account-disable-and-enable) for
current rollout evidence. These checks do not close M10's full acceptance matrix.

The original MVP assumed that Swagger, CLI commands and runbooks were sufficient
for administration. The account, OSM and recovery workflows have since become
too numerous and stateful for manual calls to be the normal product experience.
Milestone 10 therefore includes an administrator UI served under **`/admin`**.
REST contracts, the operator CLI and runbooks remain available for automation and
recovery, with the same authorization and lifecycle rules.

The primary tabs are **Accounts, OSM Maps, Operations, Settings**, in that order.
Each tab uses a Stats-style left navigation panel for second-level categories.
The existing login screen serves both roles; there is no separate administrator
login form. The reserved username **`admin`** and the bootstrap administrator's
password authenticate the separate administrative identity.

## Scope And Ownership

- M10 covers post-installation account administration, manual public OSM region
  preparation/maintenance, announcements, operational monitoring/recovery and the
  bounded Settings surface described here.
- Helm values, GitOps, Kubernetes Secrets and infrastructure automation remain
  authoritative for deployment configuration. Initial base-map families,
  MapTiler keys, SMTP, databases, mounts, TLS, resources and monitoring integration
  are installed through that path. Release-oriented installation/user/admin
  documentation belongs to M13; production hardening remains M14.
- Private source configuration belongs to the data owner. A planned **Data
  Sources** action in the normal user profile menu will provide add/modify and
  connection-check workflows using the owner APIs. Its detailed implementation
  is separately scheduled and is not an admin permission or an M10 acceptance
  prerequisite. External iCloud authorization/2FA retains its existing contract.
- Administrator identity has no personal workout account. Management of account
  identity/lifecycle does not authorize private fitness or source access. The
  expressly approved **View usage** projection returns only last login, workout/
  route counts and latest local workout date; it grants no owner capability,
  workout rows, geometry, source metadata or individual activity list.
- Multi-administrator provisioning, delegated roles, impersonation, automatic
  region discovery/download and a general Kubernetes management console are
  outside this slice. Existing lifecycle engines are reused rather than forked.

## Login, Identity And Session Separation

### Shared Login And Reserved Username

1. Use the existing Username/Password login screen and its normal password
   validation, origin checks, rate limiting and generic error handling.
2. Reserve canonical username `admin` exclusively for the bootstrap administrator.
   Apply the existing Unicode/case canonicalization to reservation checks at
   registration, bootstrap and any future identity-change boundary. A normal user
   cannot register a canonical equivalent, including compatibility spellings.
3. Administrator browser sign-in uses `admin` plus the administrator's current
   database password hash, initially created from the configured bootstrap Secret.
   There is no hardcoded password and no runtime comparison against a mounted
   plaintext bootstrap password. The administrator email remains its recovery
   address. Normal users retain username-or-email sign-in.
4. Route a successfully authenticated administrator to `/admin`, and a data owner
   to the normal explorer. Use the authenticated server identity/role, never a
   client-supplied role, URL, username check alone or UI visibility, to authorize.
5. An unauthenticated `/admin` deep link opens the existing login screen with a
   validated return target. After authentication, retain only return paths for the
   authenticated role; reject external and cross-role return targets.
6. The shared login screen can be reached for the other role while one role is
   already signed in. A user session must not force every login visit back to the
   explorer and thereby prevent administrator sign-in.

The bootstrap identity and invitation-only registration contract must be updated
before this behavior is enabled. New bootstrap configuration uses username
`admin`. For an existing administrator with a different username, provide an
explicit, audited identity transition preserving its principal ID, password hash
and audit relationships, with session revocation and bootstrap configuration
alignment. Inspect canonical collisions first. An existing owner named `admin`
requires reviewed operator resolution; never convert, merge, delete or silently
rename that account. Normal startup never creates/re-enables an administrator or
reconciles a password from Helm on every rollout.

### Independent Sessions

- Preserve the durable session model and HTTP-only cookie/CSRF protocol. Add a
  distinct administrator cookie, `workouts_admin_session`, with the same Secure,
  HttpOnly, host-only, SameSite and absolute-expiry policy as the owner cookie.
  Keep both cookies on `/`; cookie paths are not an authorization boundary.
- The shared browser sign-in operation sets only the authenticated role's cookie.
  Owner session lookup/sign-out uses `/api/session`; administrator lookup/sign-out
  uses `/api/admin/session`. Update OpenAPI and auth middleware together so dual
  cookies are resolved deterministically by the endpoint's role, with independent
  CSRF values. The administrator session endpoint itself also requires admin role.
- Wrong-role credentials never fall back to the other cookie or elevate a data
  owner. Explicit bearer precedence and invalid-bearer no-fallback remain intact;
  API clients still receive only the authenticated identity's permissions.
- Signing out or expiring one role clears only its session and associated browser
  state. Both roles can coexist in separate tabs of the same browser. A password
  change/recovery revokes the affected identity's sessions, not another identity's.
- Separate role/session-owned query caches, notifications, preferences, retained
  workspaces and global handlers. Clear them on identity change or confirmed
  expiration; late replies cannot restore the old role's state. Do not mount the
  normal explorer or fetch its private endpoints in the admin workspace.
- `admin` sign-in is distinct from the existing default-off development
  administrator-password fallback. This UI does not enable that fallback or
  expose an impersonation action.

## Layout And Interaction

Reuse the [UI style guide](ui-style-guide.md), bundled fonts, theme tokens,
`ContentBackground`, shell/header treatment and light/dark behavior. The admin
workspace can be a lazy-loaded entry in the existing UI image; it does not require
a separate public origin or UI deployment. Its routing, data and authorization
remain separate from Summary, Map, Stats and Data Sync.

| Tab | Left navigation categories |
|---|---|
| Accounts | Users (including invitations and account deletion actions/progress) |
| OSM Maps | Regions; Update history; Storage and cleanup |
| Operations | Overview; System tasks; Incidents; Announcements; Audit; Recovery |
| Settings | Console preferences; Administrator access; Effective configuration |

- Default to Accounts / Users. Preserve visited tab/category filters, page, sort,
  selection, disclosures and owned scroll within the administrator session.
  Provide refresh-safe deep links to tab/category and account/region/task details.
- Accounts has no separate Account deletions category or placeholder. Legacy
  `/admin/accounts/deletions` links resolve to Users.
- Number keys **1, 2, 3, 4** select Accounts, OSM Maps, Operations and Settings
  through the shared guarded dispatcher, handing focus to active content without
  changing modality. Preserve modifier/IME/editing/menu/dialog ownership. The
  compact Sign out uses scoped padding, pale-red danger hover and an in-flight
  duplicate-submit guard without changing label, dimensions or disabled styling.
- Reuse Stats' left-nav hierarchy, selected treatment, fixed shell and independent
  content scrolling. Do not add broad categories as an unstructured settings page.
- Reuse Summary/Road Coverage tables, row actions, shared pagination and frozen
  dialog sizing; reuse Data Sync for task history, progress, events and logs.
  Fetch before swapping compatible rows/detail and prepare menu eligibility
  before revealing actions. Pending old actions are inert, not silently retargeted.
- Keep keyboard/focus-return, disabled/empty/error states, pointer/touch paths and
  reduced-motion behavior consistent. Large panels retain programmatic focus
  without acquiring giant outlines. Inactive views pause polling and close portals.
- Use the configured polling cadence for active lists; running detail may use the
  established faster cadence. Terminal immutable data stops unnecessary polling;
  changes to recovery eligibility can refresh at the normal cadence. Logs refresh
  only on explicit request, as in Data Sync.
- Mutations use CSRF, capture the actual selected identity, prevent duplicate
  submission and revalidate authority on the server. Show accepted task links and
  durable state rather than claiming completion when a command is merely queued.
- Destructive dialogs name the exact account, candidate generation or scope and
  initially focus the least destructive action. A late response cannot navigate
  an inactive workspace or reopen a dismissed menu/dialog.
- Support desktop and reachable iPad-class layouts in both themes. M11 retains
  the full tablet-device/accessibility acceptance matrix; phone-focused work is
  not added here. Use guarded, documented shortcuts only; explorer shortcuts must
  not execute against the admin workspace.

## Accounts

### Users

- The reviewed UI assumes roughly ten users: show all directory rows, without
  search or pagination, in a centered table **80% of Workout log's width**, clamped
  to available content width. Reuse Workout log's frame, headers, sort indicators,
  row height, hover, truncated-value help and right-aligned ellipsis actions.
- Replace the page-level Administration/Users heading pair with the uppercase
  **Users** section heading. Align **Invite a user** to the right edge on the same
  line, reusing the Workout log's Clear all filters link composition.
- Sort Username, Name, Email, Registered and Status locally, initially Username ascending,
  with stable tie-breaks and unavailable username/name values last. Preserve sort
  across tab returns and invitation insertion. Rows grow naturally; narrow views
  scroll the table horizontally within its frame, never the document.
- Combine owners with the latest relevant unaccepted invitation per unregistered
  email. `GET /api/admin/directory` reads one consistent snapshot, exposes no token
  or verifier, and fails explicitly above 1000 entries rather than truncating.
  The existing paginated `/api/admin/users` remains available for API clients.
- Status is bold, capitalized text styled like Task history: **Active** green,
  **Invited / Deleting** yellow and disabled/deleted/expired/revoked states red.
  Accepted invitations disappear in favor of their registered owner, without
  duplicate email identities. Invited username/name/date are unavailable, not fabricated.
- The invitation dialog is titled **Invite a user**, reusing the compact range-
  deletion dialog's field/footer composition with an **Enter e-mail address**
  prompt and **Cancel / Send**. Send requires a valid trimmed email and serializes
  requests. Success inserts the persisted Invited row immediately, closes the
  dialog, restores trigger focus and refreshes authoritative state. A stale poll
  cannot erase the accepted insertion. Successful SMTP delivery closes the dialog
  without a message; failure alone shows an error. Title top/bottom gaps and error
  top/bottom gaps are equal, scoped to this dialog and all its error messages.
  Persisted invitations can be retried through Resend after a transport failure.
- There is no separate Invitations category. Old invitation-category deep links
  resolve to Users. **View usage** is first; it is unavailable for invitations.
   Registered users next offer conditional **Revoke sessions**, followed by
   **Disable user / Enable user** and, below a divider,
   red danger **Delete user**. Disable/Enable are implemented for active/disabled
    owners; Delete accepts an irreversible background purge. Deleting/deleted owners cannot change access.
  While disabled, danger items are gray and show the blocked circle-slash cursor,
  just like other unavailable actions; their danger treatment applies only when enabled.
  Invitation **Resend invitation / Revoke invitation** are enabled, normal items,
  with no divider above Revoke. No placeholder explanatory menu item is shown.
  Detail contains only identity/lifecycle metadata; list APIs do not join private
  workout/source/job tables to construct convenient panels.
- Invite by unique email, showing accepted/delivered versus retryable SMTP failure
  accurately. Signup still creates the owner's separate personal account.
- Include **Suspend account**, **Reactivate account** and **Revoke sessions** as
  M10 account-management actions. Password recovery remains the existing
  self-service **Forgot password?** flow; the owner dropped an admin-triggered
  reset-email action on 11/17/2026. Suspension
  denies sign-in and existing sessions, prevents new manual/scheduled jobs and
  cooperatively drains active work without purging data. Reactivation is explicit,
  does not revive sessions or cancelled tasks, and resumes scheduling through the
  ordinary next-due policy without duplicating missed runs. Record every action.
- Revoking sessions includes cookie and bearer sessions for that user; it does not
  interrupt the administrator's session. Administrators do not send reset links,
  see reset tokens or set a user's password on their behalf.
- Show **Revoke sessions** only for an active user with at least one unrevoked,
  unexpired browser or bearer session. The directory exposes only the latest valid
  `activeSessionExpiresAt`, never session IDs, credentials or verifiers; null also
  covers invitations and inactive identities. A usable token is not online presence.
- Refresh directory availability on menu open, hide the action until that refresh
  completes, and remove it when the last session expires even with the menu open.
  Revocation is an immediate normal menu action, serializes duplicate submissions,
  preserves the row/account/password/jobs, and restores focus to its trigger.
- `DELETE /api/admin/users/{userId}/sessions` requires an administrator and its
  own CSRF token for cookie authentication. Recheck/lock the administrator session,
  serialize with the target principal's sign-in fence, revoke valid sessions and
  audit atomically. Avoid account locks so ingest file commits do not block this
  identity-only command. Unknown/admin targets return 404; an already expired or
  revoked target is a successful no-op without another audit. New sign-in is allowed.
- Account states distinguish active, suspended and deletion in progress/failed.
  Suspension and deletion cannot race to re-enable a deleting identity. Self/admin
  deletion, role promotion and changes to immutable username/email are not offered
  by this user-management page. Server state is authoritative across browser tabs.

### Disable And Enable

- Disable and Enable use the shared compact confirmation dialog, name the selected
  username and initially focus Cancel. Pending submissions are serialized; errors
  retain the dialog and row. Success updates the selected identity's status and
  restores row-trigger focus. Tab exit closes the dialog; late replies cannot reopen it.
- Enable confirmation copy is **Enable @user? The user will be able to sign in
  again and automatic sync will resume on schedule. Revoked sessions and cancelled
  jobs will not be restored.** Replace `@user` with the selected bold username.
- `PUT /api/admin/users/{userId}/access` accepts required boolean `enabled`.
  Administrator authorization and independent cookie CSRF are required. The fixed
  privileged function rechecks the active administrator session, locks account then
  principal and rejects administrator/unknown targets or deletion lifecycle conflicts.
  Repeated desired-state commands are idempotent and each accepted command is audited.
- Disable sets account state `disabled` and principal `disabled_at`, revokes all
  cookie/bearer sessions and invalidates an outstanding scheduler lease. The normal
  next-due time, source configuration/credentials and user data remain retained.
  Existing cancellation engines cancel queued jobs and cooperatively cancel running
  ingest/Coverage, including their ordinary terminal snapshot/staging cleanup.
  Completed imports and valid Coverage are not rolled back.
- New job insertion is fenced against the account lifecycle, including requests
  authorized just before disable. Sign-in retains an account/principal issuance
  fence through session commit so it cannot race a completed disable.
- Enable clears suspension only for active/disabled accounts. It restores sign-in
  and normal next-due scheduling, never old sessions or cancelled jobs. Unused
  automatic-retry allowance does not revive a cancelled Coverage run; an eligible
  manual retry or genuinely changed desired target follows the ordinary job policy.
- Correct owner credentials for a disabled account return `401` with problem type
  `urn:workouts-explorer:problem:account-disabled`. Show exactly **Your account has
  been disabled.** below Username/Password using the ordinary sign-in error summary.
  Incorrect/unknown credentials retain generic feedback; never display raw server
  details. Recovery does not reactivate the account. Existing API-401 confirmation
  and sign-in redirection remain the detection mechanism; no new session polling is added.
- At the owner's explicit request, the SQL is folded into `002_account_lifecycle.sql`.
  There is no active 033; source/runtime remain 32/32. The existing database receives
  a separately backed-up, rehearsed catalog upgrade preserving the exact Goose ledger.

### Registered And View Usage

- **Registered**, between Email and Status, is the immutable principal creation
  timestamp formatted in the browser's local time zone as `Oct 16, 2025`, without
  time. Invitations display unavailable. Date sorting compares instants and initially
  selects newest-first, with unavailable values last.
- **View usage** opens compact **Usage by <name>** with three fields and an
  upper-right X close control, without a footer or bottom action buttons.
  Its title is **2rem**; the close button reuses Provenance's icon-button and
  multiplication-sign glyph rather than a separately positioned SVG.
- Clicking any non-action area of a user row opens the same **View usage** dialog,
  with a hand cursor. The username is a native keyboard-operable button, and focus
  returns there after a row-initiated dialog closes. Row action menus remain
  independent. Invitations have no usage projection and are not clickable.
- **Last login** is local date/time, such as `Oct 20, 2026, 8:25 PM`. It tracks
  successful cookie/bearer session issuance, not failed login, and survives session
  expiry/revocation/cleanup. Schema 032 backfills available history and maintains
  a principal timestamp through a privileged trigger, not API timestamp writes.
- **Workouts** shows non-deletion-pending workout and route counts, such as
  `245 (145 routes)`. **Last workout** is the latest workout's date in the requested
  local time zone, such as `Oct 18, 2026`. Unavailable is not zero.
- `GET /api/admin/users/{userId}/usage?timezone=<IANA zone>` returns only name,
  last-login timestamp, counts and local date. The fixed SECURITY DEFINER function
  validates an active administrator session, scopes its internal read and restores
  logical account/session GUCs on success and exception. Owner API permissions,
  private rows and capability issuance do not expand.
- Return focus to the row trigger; cancel obsolete requests on dismissal/tab exit.
  Late responses cannot reopen the dialog or display another user's result.

### Invitations

- Manage invitations from Users row actions/detail, not another left-nav category.
  Future detail/history includes pending, accepted, expired and revoked invitations,
  with email, creation/
  latest issuance, expiration, delivery outcome and sanitized action history.
  Invitation/reset token values and verifiers never appear in responses or audit.
- Resend rotates the token and expiry to the same email. Accepted/revoked
  invitations cannot be resent as pending. Retryable delivery states remain
  visible; a failed email is not presented as a delivered invitation.
- Revoke prevents acceptance. Resend, revoke and acceptance resolve transactionally
  so concurrent operations cannot consume a token twice or create duplicate users.
- Implemented Resend posts an empty JSON object under the shared JSON POST/CSRF
  contract, invalidates the old unused token and grants exactly seven days from
  fresh issuance. Revoke removes the row, invalidates unused tokens and permits
  later re-invitation while retaining audit. Success shows no extra message;
  transport/action failure is an error, not an expected delivery-pending notice.
- Once the server acknowledges SMTP DATA, a later QUIT/connection-close failure
  is not a send failure. Failure to record an accepted delivery is logged without
  secrets and does not falsely report that email was unsent or invite duplicates.

### Account Deletions

**Implemented at Helm 173; folded to schema/minimum runtime 32/32 at Helm 174.**
Helm 177 adds bounded retries for deletion-acceptance lock contention and clickable
Users rows for usage. Native PostgreSQL contention/purge checks, 22 administrator
component tests, Go checks and candidate/deployed Chromium dark/WebKit light checks
pass. The owner's purge/fresh-registration expectations include historical data
and later schema additions, not only the rows currently visible in the explorer.

- **Delete user** opens the shared compact destructive confirmation naming the
  selected username and focusing Cancel first. On `DELETE /api/admin/users/{id}`
  acceptance, upsert that same row to yellow **Deleting**, retaining all other
  accepted rows and sort/scroll. Never optimistically remove an accepted deletion.
- A database lock timeout before acceptance returns the sanitized
  `account-deletion-busy` Problem Details type. Keep the confirmation pending and
  retry that condition at one-second intervals, at most 14 retries (approximately
  one minute including each three-second lock wait). Show waiting feedback; never
  retry unrelated errors automatically. Stop scheduled retries on admin-session
  replacement or tab exit. Exhausted contention can be retried with Delete in the
  same dialog, without reloading the page. Account/enqueue/sign-in fences are unchanged.
- Users polls at the configured active cadence, accelerated to at most 10 seconds
  whenever a row is Deleting. Compatible refreshes keep existing row nodes mounted;
  remove only the completed identity when authoritative directory data omits it.
  Inactive views pause polling and close transient dialogs. Late old-session/tab
  replies cannot reopen dialogs or restore identity state.
- Deleting rows offer **View deletion**, returning only sanitized phase/counts/error
  status. A dialog can be closed without cancelling the task. **Retry** nudges the
  same durable task; no undo/reactivation/task-cancel operation is offered.
- The account purge task is admin-owned and survives removal of all owner job
  rows. Original identity/invitation/reset/session state is removed, username/email
  are released and re-registration creates new principal/account UUIDs and defaults.
- Fixed, dependency-ordered batches cover every current private account table and
  verify zero remaining rows. Inventory drift fails closed. Transaction-bound private
  capabilities permit only the purge path to bypass normal append-only/maintenance
  callbacks; foreign keys remain enabled. No runtime receives raw DELETE authority.
- Private execution receipts are registered before worker work and cleared only
  after local cleanup. Restart scavenging acknowledges the same pod/root. Abandoned
  receipts remain blocking unless the exact chart-owned emptyDir pod UID has been
  disposed/replaced, verified through namespace-only read-only get-pods access.
- The API retains no identity-keyed avatar memory cache. App-owned selections,
  reports, tile/LOD state, deduplication/deletion markers, retry/outbox state and
  source inventories are removed. External Local/iCloud exports and public OSM
  data remain; backups/WAL/lost-volume media retain infrastructure boundaries.

- On acceptance, atomically disable authentication, revoke all user sessions and
  prevent new manual/scheduled work. Cancel queued account jobs and request
  cooperative cancellation of running jobs.
- Wait for jobs to become terminal or abandoned leases to be recovered; clear
  encrypted snapshots and staging before capturing the owned purge targets.
  Purge cannot race a worker that may still commit account data.
- Purge sources/credentials, workouts/routes/provenance, Coverage and aggregates,
  preferences, notifications, private tile/selection/report state and owner job
  diagnostics. Use the complete schema inventory, including later additions, to
  verify purge completeness rather than relying on this prose as a table list.
- Show cancellation/draining, purge progress, timestamps, safe counts and failures.
  A partial failure leaves the account disabled and has durable retry/resume state;
  completed work is not recreated. Preserve a minimal sanitized admin audit.
- An accepted account deletion has no Reactivate, Undo or generic Cancel action.
  Repeated requests cannot create competing purges. Completed/unknown targets
  follow the existing not-found contract. Backups/WAL follow infrastructure
  retention; task completion is not a claim of immediate physical erasure there.

## OSM Maps

This tab manages public OSM extracts used for Coverage, not background MapTiler
styles or private user routes. Reuse the implemented
[resumable updater](osm-resumable-rebuild.md) and promotion/reconciliation logic.

### Regions

- List configured, prepared and in-progress provider-qualified regions. Search
  the supported provider catalog by display name/ID to choose an initial region;
  show published extract metadata and available size/coverage information.
- Distinguish deployment-declared initial regions from the catalog's prepared
  regions. Preparing an additional region changes durable OSM catalog data, not
  Helm values. A later rollout cannot overwrite a promoted catalog generation.
- Start initial preparation or refresh, with source version/timestamp/checksum,
  current promoted generation, importer/derivation versions, last success, active
  task and safe failure status. Resolve provider URLs from the supported catalog;
  do not accept arbitrary download URLs, SQL, images or pod specifications.
- Show actual download/filter/import/derive/validate/promote stage activity,
  checkpointed batches, counters and timestamps. Unknown totals remain unknown.
  Download/import before the durable checkpoint has different recovery behavior
  from a retained post-import build; explain when an initial stage must restart.
- Coalesce equivalent requests across active region operations. Preserve the
  updater's stronger database-wide writer lock as well as regional coalescing;
  independent installations using the shared OSM database cannot launch competing
  writers merely because their application task queues are separate.
- Existing promoted data remains readable while a candidate builds or fails.
  Validation and reader readiness must pass before atomic promotion; no manual
  force-promote/skip-validation action is provided.

### Update History

- Provide administrative task history/detail, generation and stage history,
  bounded sanitized events/logs, retries, active execution and promotion outcome.
- Resume retained work only with matching source/tool/image/stage fences; show a
  safe explanation when a new candidate or retained compatible image is required.
- Offer cooperative execution cancellation where supported. Preserve resumable
  work after the durable boundary; cancellation is not an implicit candidate abort.
  An explicit **Abort candidate** separately fails and removes the retained build.
- A retry/resume of a blocked generation cannot bypass an operator clearance.
  Preserve existing work and avoid creating a second generation for that region.
- Show public promotion/reconciliation status through bounded operational
  summaries. Reconciliation remains private account-owned work and must not reveal
  account routes, workout identities, coordinates, visit dates or user job logs.
- If promotion succeeds but downstream handoff fails, retain the promoted result
  and retry the durable handoff; do not report a complete end-to-end success or
  rebuild the public dataset merely to recreate the missing obligation.

### Storage And Cleanup

- Show the authoritative storage preflight, measured available/capacity bytes,
  threshold, freshness, block reason and affected generation/stage. Unknown/stale
  storage readings block work rather than being interpreted as adequate space.
- Separate **Unblock** from **Resume**. Clearance records authenticated actor,
  timestamp and optional note; the resumed unit still runs a fresh preflight.
- Show candidate/retired-generation cleanup status and provide bounded retries for
  the updater's supported cleanup. Never remove active data to make a build fit.
- PVC expansion, snapshot deletion, broad database cleanup and unrelated Job
  deletion remain external operator actions. Link remediation guidance rather
  than granting those powers to the application.
- Show OSM replica readiness, validation/promotion blockers and the installed time
  zone boundary release/import outcome where available. Boundary import changes
  remain deployment-controlled; this slice adds no competing release selector.
- Automatic region discovery/download remains separately deferred. This UI may
  display its effective policy but cannot enable it in conflict with Helm.

## Operations

### Overview And Incidents

- Summarize API, general-worker, Coverage-worker and tile service health, relevant
  database dependency readiness, component versions, schema compatibility and
  migration failures. A healthy collector is not proof that its service is healthy.
- Show privacy-safe queue depth/oldest age, active capacity, lease/heartbeat
  recovery and storage/cleanup conditions from authorized operational read models.
  Do not make a generic cross-account job browser out of the owner task APIs.
- Complete M10 instrumentation and required source freshness, expired-credential,
  stuck-job, cleanup-failure, OSM-failure, API-latency and tile-latency alerts.
  Differentiate source-owner action from application incidents without exposing
  individual source configuration, source paths or private diagnostics.
- Show metric freshness/coverage and explicit unavailable/no-data states. Detailed
  Grafana/Pyrra/alert links supplement the overview; external tool authentication
  and access policy remain independent, without browser-held service credentials.
- Alert evaluation/routing and telemetry infrastructure remain deployment-managed.
  This page does not edit Alertmanager silences/rules, SLO targets or collector
  configuration. Do not claim complete lifecycle metrics until producers exist.

### System Tasks

- Unified filters/pagination/detail for admin-owned account deletion and OSM tasks,
  with deep links back to the owning tab. Reuse Data Sync's history/detail UX.
- Show durable execution state, attempts, safe counts, timestamps, cancellation
  intent, events and bounded redacted logs. User-owned logs/files/route children
  never become visible merely because the administrator manages their lifecycle.
- Provide only freshly eligible retry/resume/cancel controls, respecting linear
  attempts, shared operations and operation-specific restrictions. Administrative
  ownership and the requesting actor must remain recorded when requests coalesce.

### Announcements

- Create and preview a length-limited title/message, optional expiration and
  severity **info** or **warning**, default info. Error severity is not available.
- Deliver independently dismissible in-app notifications to every active data
  owner. Expiration/retraction removes visible messages even after a user refreshes
  or signs in again. Partial fan-out is atomic or durably completed without duplicates.
- List active, expired and retracted announcements and their administrative
  history; retract before expiration. Do not email announcements or expose users'
  unrelated notifications or individual acknowledgement activity to the admin.

### Audit

- Search/filter/page sanitized administrative actor, action, target, timestamp,
  request/task correlation and outcome. Cover invitations, account controls,
  deletion, announcements, OSM requests/unblock/abort and administrator access
  changes. Audit reads are bounded and do not grant editable/deletable audit rows.
- Keep minimal lifecycle evidence after account purge; exclude private activity,
  secrets, credentials, raw payloads, GPS and arbitrary exception strings. Audit
  retention/access follows the documented policy, not ad-hoc UI deletion.

### Recovery

- Provide context-sensitive account purge, SMTP delivery, worker interruption,
  storage block, failed OSM update and migration recovery guidance, plus current
  backup/restore and Argo CD rollout/rollback runbook links.
- Show externally supplied backup/restore status only when a supported integration
  provides authoritative evidence. Missing integration is unavailable, not proof
  that a backup exists. No browser-triggered database restore, schema downgrade,
  arbitrary command execution, cluster restart or GitOps mutation is added.
- M10 retains tested backup/restore/migration runbooks and migration-gated rollout.
  Public installation walkthroughs and production validation retain M13/M14 scope.

## Settings

Settings contains administrator-owned console/access state and a **read-only**
effective-configuration view. It is not a second deployment configuration editor.

### Console Preferences

- Persist the administrator's theme, display time zone, clock format and table
  rows-per-page preference independently from every owner's preferences. Use the
  deployment's page-size maximum and the shared formatting/selection controls.
- Apply these across admin tabs before initial presentation where possible.
  Session-only page/filter/navigation memory remains workspace state, not a new
  installation-wide preference. Do not borrow an owner's preference endpoint.

### Administrator Access

- Display the fixed reserved username and configured recovery email as read-only
  identity fields. Provide password change with current-password verification and
  confirmation, using the deployment's password policy; revoke the administrator's
  existing cookie/bearer sessions and return to the shared login screen afterward.
- Provide sign out everywhere for the administrator and the ordinary email
  recovery flow. Operator bootstrap-password rotation remains a recovery path.
- The bootstrap Secret initializes credentials; it is not continuous desired
  password state. An in-app password change updates the authoritative hash, never
  a Kubernetes Secret. A later explicit bootstrap/rotation operation must reconcile
  credentials deliberately, never reset them as a side effect of an upgrade.
- No additional administrators, self-disable/delete, role grants, impersonation,
  email/username edit or development debug-auth toggle are exposed here.

### Effective Configuration

Show a curated, sanitized snapshot and ownership labels such as **Managed by
Helm/GitOps** or **Administrator preference**, with retrieval time and unavailable
states. Useful read-only fields include application/component versions, effective
session/password/page-size/polling policy, declared initial OSM regions and
download limits, map-family labels, and configured integration readiness.

Never return raw environment/Helm values/Secret dumps, DSNs, private keys,
passwords, bootstrap material, tokens, owner source configuration or secret-bearing
URLs. Configured credential presence can be a boolean; it is not a readable value.
Preparing a catalog region is a domain operation in OSM Maps, not a config edit.

| State | Authority | Admin UI access |
|---|---|---|
| SMTP, URLs/TLS, DB/Secret references, mounts, service resources, image versions, network/RBAC, base-map families/keys/CSP, telemetry and alert policy | Helm/GitOps and infrastructure automation | Curated read-only status and deployment guidance |
| Initial OSM region/provider policy, auto-add flag, byte/resource limits, boundary release | Helm/GitOps | Read-only; no shadow override |
| Promoted OSM generations, retained builds, storage clearance, cleanup state | OSM catalog/updater | Authorized lifecycle commands in OSM Maps |
| Invitations, account lifecycle, announcements, administrative tasks/audit | Application domain data | Authorized commands in Accounts/Operations |
| Admin console preferences and current password hash | Administrator identity data | Bounded changes in Settings |
| Private sources/credentials, workouts/routes, owner preferences/notifications/logs | Data owner | No admin access |

Do not invent installation-wide tunables merely to populate this tab. Any future
editable application policy must have a documented owner, persistence/defaults,
authorization, audit and upgrade behavior, and cannot duplicate a Helm-controlled
setting. Changes made here must not be silently undone by Helm/Argo CD or write
back into their desired configuration.

## Backend, Jobs And Kubernetes Boundary

### API And Durable State

- Design admin resources under `/api/admin`, with role-specific session handling
  and narrowly authorized database operations. Specify list/filter/sort/pagination,
  detail, command acceptance/conflict and safe failure schemas in OpenAPI before
  implementation. Shared login and owner APIs retain explicit role boundaries.
- Extend the database/worker lifecycle for account suspension/purge, invitations,
  announcements, admin preferences, administrative tasks and audit. Current job
  enum/schema foundations are not a completed admin task API or purge runner.
  Use forward migrations; preserve identity uniqueness, RLS and retained history.
- Command acceptance records actor, target and a durable idempotent operation.
  Enqueue/claim/progress/outcome survive UI/API/controller restarts. Equivalent
  active OSM requests return their existing task; failed handoffs remain recoverable.
- APIs expose curated operational projections, not privileged private-table
  browsing. Admin identity has no account scope and cannot call owner endpoints,
  obtain map/report/tile capabilities or use owner artifact/export URLs.

### Dedicated OSM Job Controller

UI-initiated updater Jobs require Kubernetes API access. **Add a separate,
internal OSM job-controller pod/Deployment**, using a dedicated ServiceAccount
and namespace-scoped Role/RoleBinding. Reuse the existing worker/OSM code and
images where practical; do not give this access to the public API, UI, general
worker or Coverage worker.

```text
Shared login -> admin session -> /admin -> /api/admin
                                            |
                                     durable admin request
                                            |
                                  internal OSM job controller
                                            |
                                  approved Kubernetes Job
                                            |
                             existing workouts-osm updater/catalog
```

- The API validates domain commands and records them; it accepts no arbitrary
  Kubernetes manifests or shell commands. The controller claims only approved
  commands, launches immutable deployment-selected updater templates/images and
  reconciles Job outcomes with durable task/catalog state.
- Limit Kubernetes permissions to required `batch/jobs` create/get/list/watch/delete
  and, only as needed for status, pods get/list/watch in the installation namespace.
  No cluster-admin, RBAC mutation, Secret get/list/watch, pod exec/attach, arbitrary
  pod creation, deployment/PVC mutation or snapshot deletion. Do not add broad
  `pods/log` access for convenience; consume sanitized updater records/events.
- Label and deterministically name controlled Jobs; reconcile previously created
  Jobs after an ambiguous create or controller crash instead of launching duplicates.
  Delete only controller-owned updater executions after their outcome is retained;
  do not copy the CLI's broad historical-Job cleanup into the controller.
- RBAC cannot constrain `create jobs` to a safe embedded pod template. Treat the
  controller as a trusted launcher, validate templates server-side and enforce
  supported admission restrictions for its image, service account, Secret/mount
  allowlist and pod security. Labels alone are not a Kubernetes authorization
  boundary. Test this exposure explicitly rather than claiming RBAC solves it.
- Updater Jobs use dedicated runtime credentials and the configured writer/TLS
  Secret references mounted by Kubernetes; the controller does not fetch Secret
  contents. Jobs have no account-source keyring or private source mounts. Any
  application reconciliation handoff uses only narrow approved functions.
- Only the controller mounts the Job-launching Kubernetes credential. Updater Jobs
  have no Job-launching authority; if storage preflight needs Kubernetes metadata,
  give their separate identity only the necessary read-only discovery permissions.
  Metrics-only preflight does not justify mounting a Kubernetes token.
- Preserve public provider limits, storage thresholds, low-priority scheduling,
  global single-writer ownership, durable checkpoints, replica fences and atomic
  promotion. Use a narrow control-state DB role, not runtime owner impersonation.
- Helm owns controller enablement/resources, Role/ServiceAccount, approved image,
  Job templates/Secrets, scratch limits, network paths and health probes. Constrain
  controller-to-API-server access separately from browser/API traffic. Additional
  final production network/credential validation remains M14.
- Keep CLI recovery compatible with controller ownership. A manual operator cannot
  unknowingly race an active controller execution; use the same locks/catalog and
  durable status rather than two import lifecycles. Browser disconnect is never
  an implicit cancellation. Job/pod failure is not authority to abort a candidate.

## Acceptance And Verification

- Shared login routes `admin` to `/admin`; canonical reservation, bootstrap identity
  transition/collision handling and wrong-role return paths are covered. Invalid
  credentials retain uniform throttling/errors. No development impersonation is
  enabled. Admin/user sessions coexist and expire/sign out independently.
- Test a cross-role matrix over every owner/admin resource, item, log, artifact,
  export, notification, report and tile capability. Admin screens/caches/telemetry
  cannot leak owner data, including late replies and stale links after deletion.
- Exercise invitation delivery failure, resend/accept/revoke races, suspension and
  reactivation, session revocation and self-service recovery without token/password disclosure.
- Exercise deletion during active ingest/Coverage, worker interruption, lease
  recovery and partial purge retry. Verify complete schema purge scope while
  preserving only sanitized audit and infrastructure-retained backups.
- Exercise initial region preparation, duplicate requests, cross-installation writer
  contention, restart before/after checkpoint, storage block/unblock/resume, version
  mismatch, abort, validation failure, promotion and retryable reconciliation/GC.
  Existing public/private reads remain available when a candidate fails.
- Test controller duplicate/ambiguous Job creation, lost leases, Job failures,
  controller restart, CLI contention and owned-only cleanup. Verify positive and
  negative RBAC/admission cases with actual service identities; API/workers lack
  Job-launching authority and updater Jobs cannot acquire it.
- Verify announcement expiration/retraction and independent owner dismissal;
  admin audit/log projections remain bounded and private-data-free.
- Verify console preference isolation, password-change revocation, read-only
  effective configuration and Helm upgrade/Argo CD reconciliation without config
  drift or hidden overrides. No in-app Save control exists for deployment fields.
- Review every tab/category beside its established UI counterpart in light/dark,
  pointer/keyboard and iPad-class containment, including delayed/out-of-order
  requests, retained scrolling and action eligibility. Full device matrix is M11.
- Complete metric/alert rule tests, missing-data interpretation and required M10
  runbook rehearsals; do not substitute a status screenshot for recovery evidence.

## Related Contracts

- [Milestone 10 plan](implementation-plan.md#milestone-10-administration-and-operational-completion)
- [Functional specification](functional-spec.md)
- [Architecture](architecture.md)
- [Tenant isolation](decisions/002-tenant-isolation-and-database-roles.md)
- [Session protocol](decisions/004-session-cookie-bearer-and-csrf-protocol.md)
- [Authentication/bootstrap](decisions/005-public-authentication-and-email-delivery.md)
- [Job state machine](job-state-machine.md)
- [Telemetry and monitoring](telemetry-and-monitoring.md)
- [Resumable OSM rebuild](osm-resumable-rebuild.md)
