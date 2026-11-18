# Functional Specification

This specification describes observable MVP behavior. Implementation structure belongs in `architecture.md`.

## Global Conventions

- Cross-cutting visual and interaction conventions follow the [UI style guide](ui-style-guide.md). Feature sections retain their explicit labels, domain behavior and exceptions.
- User-facing product name: Workouts Explorer.
- Primary tab order: Summary, Map, Stats, Data Sync; Stats/New Coverage is the completed Milestone 9 implementation.
- Map modes: Routes and Coverage.
- Detailed coverage table: Road Coverage.
- User-facing synchronization term: Data Sync.
- API synchronization term: ingest.
- Dates use `YYYY-MM-DD` in APIs and localized display in the UI.
- General timestamps use RFC 3339 and may be serialized in UTC.
- Workout and route-point timestamps preserve their source offsets when available.
- API IDs accept dashed or compact UUIDs in either case and return compact uppercase UUIDs.
- API errors use RFC 9457 `application/problem+json`.

## Feature: Use The Administrator Workspace (Planned M10)

### Decision And User Story

As the bootstrap administrator, I want post-installation account, OSM and
operational workflows in a dedicated UI rather than a series of manual API/CLI
calls. **Accepted 11/15/2026**, this replaces the original no-admin-UI assumption.
The [admin UI specification](admin-ui-spec.md) owns the complete tab/category,
authentication, permission, task, Settings and verification contracts. The first
authentication/workspace slice is deployed; the full behavior below remains the
M10 target. See [implementation status](admin-ui-spec.md#implementation-status)
for the directory/invitation/configuration screens and remaining workflows.

### Behavior

1. Use the existing login screen. Reserved canonical username **admin** and the
   bootstrap administrator's password authenticate its separate stored identity;
   server-confirmed role routes it to **/admin**. Normal username/email sign-in
   routes data owners to the explorer. Never authorize by username or URL alone.
2. Reserve every canonical equivalent of **admin** from owner registration and
   provide an explicit audited transition for an existing differently named
   administrator. Unresolved owner-name collisions block transition rather than
   converting or silently renaming a private account. The current password hash
   remains authoritative; deployment startup does not reset it from a Secret.
3. Keep admin/user cookie sessions, CSRF tokens, caches and retained workspaces
   independent. Expiry/sign-out affects only the appropriate role. Cross-role
   return targets cannot grant access, and a shared login remains reachable while
   the other role is signed in.
4. Present **Accounts, OSM Maps, Operations, Settings**, in that order, with guarded
   **1/2/3/4** shortcuts and a
   Stats-style left navigation panel for each tab's second-level categories.
   Reuse existing themes, tables, menus, dialogs, paging, focus and scroll behavior.
5. Accounts combines users and persisted invitations in the sortable, unpaged
   **Users** table, reusing Workout log at 80% width. Its same-line **Invite a user**
   action opens the validated Cancel/Send email dialog; successful invitations
   appear as yellow bold **Invited** rows and become green **Active** owners after
   signup. There is no separate Invitations category, search field or pager.
   Registered dates and the bounded View usage dialog use local dates/time.
   Resend grants a fresh seven-day period; normal-style Revoke removes the row and
   permits re-invitation. Successful email delivery shows no extra message; title
   and error gaps are balanced. See the [Users composition](admin-ui-spec.md#users) for status/actions and
    fetch/sort/focus behavior. Disable/Enable is implemented with session revocation,
     cancellation without revival and retained user data. Accounts also provides
     delivery recovery, conditional Revoke sessions before Disable user, and asynchronous
    account deletion/progress/retry. Password recovery is the self-service login flow,
    not an administrator action. Suspension is reversible; accepted deletion
   cannot be undone or reactivated.
6. OSM Maps provides supported-region selection and initial preparation, refresh,
   stage/history/readiness, resume/retry, storage inspection/unblock, candidate
   abort and cleanup recovery through the existing resumable updater.
7. Operations provides health, administrative tasks/diagnostics, privacy-safe
   incidents/queue summaries, announcements, sanitized audit and recovery guidance.
   Administrator access never includes a private cross-account job/log browser.
8. Settings provides independent administrator theme/time-zone/clock/page-size
   preferences, password/session access controls and a sanitized **read-only**
   effective-configuration view. Helm/GitOps/Secrets own deployment configuration;
   the UI cannot shadow or write back those settings.
9. A separate internal OSM job controller launches approved updater Jobs with
   narrow namespace-scoped permissions and template/admission restrictions. The
   UI/public API and existing workers receive no Job-launching authority.
10. The owner-profile **Data Sources** action provides private source
    add/edit/check/delete workflows, separately delivered from administration.
    It does not grant administrators source access or add in-app iCloud 2FA.

### Acceptance Criteria

- Required routine post-installation journeys are operable through the UI; REST,
  CLI and runbooks remain automation/recovery interfaces under the same rules.
- The administrator cannot read owner workout/route rows, source configuration,
  credentials, notifications, job logs/files, exports, reports or private tiles.
- The expressly approved [usage projection](admin-ui-spec.md#registered-and-view-usage)
  exposes only last login, account workout/route counts and latest local workout
  date, never an owner data capability.
- Settings has no editable deployment fields or hidden infrastructure overrides;
  Helm upgrade/GitOps reconciliation preserves the documented ownership model.
- Account purge, OSM interruption/storage/promotion and controller restart flows
  pass the detailed [admin acceptance matrix](admin-ui-spec.md#acceptance-and-verification).

## Feature: Invite And Register A User

### User story

As an administrator, I want to invite a person by email so that accounts cannot be created publicly.

### Behavior

1. The administrator submits a unique email address.
2. The product sends a signup link containing a single-use, expiring token.
3. The invitee supplies a unique username, full display name, password, and password confirmation.
4. The invitation email becomes the account email and cannot be changed.
5. Public authentication operations are rate-limited and do not reveal whether an account exists.
6. Accepting the invitation creates one private personal workout account.

### Validation

- Email and username are globally unique, compared case-insensitively.
- Username, full name, password, and matching confirmation are required.
- M10 reservation rejects canonical **admin** for owner signup; only the
  separate bootstrap administrator can own that username.
- Password meets the configured policy.
- The invitation token must be valid, unused, unexpired, and resolve to the invitation's stored email.

### Failure cases

- Duplicate email or username returns a conflict without creating an account.
- Invalid, revoked, used, or expired token rejects signup.
- SMTP failure leaves a retryable invitation state and does not expose credentials.

### Acceptance criteria

- Given a valid invitation, when the invitee submits valid registration details, then a private account is created and the token cannot be reused.
- Given no invitation, when a person attempts signup, then registration is rejected.
- Given a duplicate username, when signup is attempted, then the API returns a field-specific validation problem.

## Feature: Authenticate And Recover Access

### User story

As a user, I want secure signin and password recovery so that I can access my account without administrator impersonation.

### Behavior

1. The login form labels fields Username and Password.
2. The Username input uses `Username or E-mail` as placeholder text.
3. Signin accepts either username or email plus password.
4. Successful signin creates a revocable session with the configured absolute lifetime, default two hours.
5. Browser clients receive an HTTP-only cookie and a CSRF token.
6. API clients may use the returned opaque session token as a bearer token.
7. Forgot-password sends a single-use reset link directly to the account email.
8. Resetting a password revokes existing sessions.
9. Repeated signin and recovery attempts are throttled without exposing account existence.

### Validation

- Cookie-authenticated mutations require the session's CSRF token.
- Bearer-authenticated mutations do not require CSRF.
- Reset tokens must be valid, unused, and unexpired.
- Signup and reset tokens are consumed atomically and cannot succeed twice.

### Failure cases

- Invalid signin returns a generic error that does not reveal whether the account exists.
- Correct owner credentials for a disabled account return the fixed account-disabled
  problem and display **Your account has been disabled.** below the login fields.
  Wrong credentials remain generic; no session is created in either case.
- Shared form-error summaries use heading **Well, this is a dead end... 🙁**.
  Generic sign-in feedback is **Sign-in failed. Check your details and try again.**;
  registration uses **Create account failed. Review the fields and try again.**;
  recovery/reset uses **Request failed. Please try again later.**. Preferences uses
  **Choose one of the available page sizes.** and **Choose how many date ranges to
  remember (1-5).** for its corresponding validation failures.
- Expired sessions require signin again.
- SMTP failure reports a safe error without revealing account existence or SMTP details.

### Acceptance criteria

- Given valid credentials, when the user signs in, then cookie and bearer authentication can access the same permitted resources.
- Given a cookie session without a CSRF header, when a mutation is attempted, then the API returns a forbidden Problem Details response.
- Given a completed password reset, when an old session is used, then access is denied.

## Feature: Manage Personal Preferences

### User story

As a user, I want display preferences to persist so that the product matches how I read workout data.

### Behavior

1. The Preferences dialog allows changing full display name, theme, units, default time zone, first weekday, clock format, workout columns, and table rows per page. The row preference applies to Summary, Road Coverage, Stats report tables and their detail dialogs.
2. Dark theme is the default; light theme is available.
3. Imperial units, Monday week start, and 12-hour time are defaults.
4. The last-used date-range preference is restored on a later session.
5. Username and email are visible but immutable.
6. **Custom date ranges** sets the per-account recent-range limit to 1, 2, 3, 4, or 5 (default 3). Applying a custom range moves it to the front of a persistent unique MRU list; shortening the list discards its oldest entries. Shortcuts do not enter the list.
7. Summary, Map and Stats show those ranges immediately below **Custom...**, without another divider, formatted like **Jan 1, 2022 to Nov 1, 2026**. Selecting the already applied range does nothing. For any explicit range, only **Custom...** carries the checkmark and remains available for editing.
8. Focus outlines follow keyboard navigation (Tab/Shift+Tab and navigation keys), not pointer clicks or text editing after a pointer click. Preferences uses accessible styled selects with the list aligned to the control's left and top edges and the same width; long lists scroll within the available viewport. Form submission, time zone ordering and keyboard selection retain their existing behavior.
9. Summary, Map and Stats date menus and the Map base-map menu match their trigger's width, align to its left edge, and open 12px below it. Lists scroll within available space rather than shifting the requested anchor.
10. Preferences reuses standard compact-dialog spacing around its header, scrolling body and Cancel/Save footer, including the action gap. Its header contains the title and close control without the explanatory subheading. Header/actions remain fixed while only the body scrolls; Save/Cancel and content-focus return retain their existing behavior.
11. Newly registered personal accounts start with Workout log columns **Date, Type, Duration, Distance, Calories**, in that order. Registration explicitly seeds these preferences; first browser initialization preserves them. Existing saved column selections are not backfilled or reset.

### Validation

- Default time zone must be an IANA time zone name.
- Page size cannot exceed the operator-configured maximum.
- Theme, units, week start, and clock format accept documented enum values only.

### Failure cases

- Invalid preferences are rejected without changing existing values.
- A stale or unsupported workout column is ignored safely when rendering and reported by validation when saved.

### Acceptance criteria

- Given a dark-theme preference, when the user signs in later, then dark theme is restored before the primary view renders.
- Given a changed full name, when the profile is requested, then the new name is returned while username and email are unchanged.

## Feature: Manage Invitations

### User story

As an administrator, I want to review, resend, and revoke invitations so that account creation remains controlled.

### Behavior

1. The administrator can list pending, accepted, expired, and revoked invitations without seeing token values.
2. Resending revokes the prior token and sends a newly expiring token to the same email.
3. Revoking an invitation prevents its token from being accepted.
4. Invitation actions retain a sanitized administrator audit.

### Validation

- Only administrators can list, resend, or revoke invitations.
- Accepted and revoked invitations cannot be resent as though still pending.

### Failure cases

- SMTP failure leaves the newly issued invitation retryable and reports a safe administrative error.
- Concurrent acceptance and revocation resolve atomically so only one outcome succeeds.

### Acceptance criteria

- Given a pending invitation, when it is resent, then the old token fails and the new token can be accepted.
- Given a revoked invitation, when its signup link is used, then signup is rejected.

## Feature: Delete A User Account

### User story

As an administrator, I want to delete an account without gaining access to its private data.

### Behavior

1. Accepting deletion immediately disables signin, revokes sessions, and prevents new scheduled or manual work.
2. All queued account jobs are cancelled and running jobs receive cooperative cancellation.
3. Purge waits until account jobs are terminal or their abandoned leases are recovered, then clears job-scoped source snapshots and staged files.
4. Only after the account is quiescent does the server capture and purge the owned private-data scope.
5. The purge removes sources and credentials, workouts, routes, provenance, coverage, preferences, notifications, private tile state, and owner-visible job diagnostics.
6. Administrator-visible progress contains counts and lifecycle state but no workout, route, source-config, or user-log details.
7. Failed cancellation or purge remains retryable and never re-enables the account.
8. A minimal sanitized audit records who requested deletion, when, and its final outcome.
9. Backup and WAL removal follows infrastructure retention and is not immediate physical erasure.
10. Users immediately shows accepted deletions as yellow **Deleting**. Active
    periodic polling accelerates to at most 10 seconds while pending, preserving
    table rows in place; completion removes only the deleted user from the list.
11. Delete all historical owner jobs/logs/events, source tombstones/inventories,
    raw/derived/tile/LOD route data, matching/display/diagnostic/retry/outbox state,
    private report/map capabilities, preferences/notifications/schedules, and
    identity/session/reset/invitation state. Release username/email and create new
    principal/account UUIDs on re-registration. Old suppression/checksum state must
    not prevent fresh imports under the new account.
12. Completion retains only an admin-owned task and sanitized audit with opaque old
    IDs, counts, requester and timestamps. Detach obsolete foreign-key references;
    retain no private workout/source/job detail. API instances retain no identity-
    keyed avatar memory cache. Existing owner browser/session caches follow the
    established confirmed-expiry/identity-change clearing protocol.
13. Source exports on operator server paths/iCloud and shared public OSM data are
    not account-owned application storage and are not deleted. Account deletion is
    not source deletion or workout deletion: historical tombstones are purged too.
14. A checked schema inventory and bounded durable batches make progress monotonic;
    interruption rolls back only its current batch and resumes without recreating
    deleted data. FK enforcement stays enabled. API mutations admitted just before
    revocation serialize against lifecycle state so they cannot recreate purge data.
15. Worker execution/staging receipts block purge independently of job terminal
    status. Clean Close/scavenging or verified disposal of the exact per-pod
    emptyDir is required; a live pod, denied metadata request or cleanup error is
    not cleanup evidence. The worker has namespace get-pods only, no pod mutation
    or Secret access. Progress reports only safe retry-needed codes.

### Validation

- Only administrators can request account deletion.
- Repeating a request for a deleted or unknown user returns not found.

### Failure cases

- A partial purge leaves the account disabled and creates an admin-owned failure job with redacted diagnostics.
- Worker interruption resumes from durable purge state without restoring private reads.
- Purge does not begin while an account job can still commit private data.

### Acceptance criteria

- Given an active user session, when account deletion is accepted, then the next authenticated request is denied.
- Given active ingest, when account deletion is accepted, then ingest is cancelled and drained before purge targets are captured.
- Given a purge in progress, when the administrator reviews it, then no private workout or source details are returned.
- Given purge failure, when the job is retried, then remaining targets are removed without recreating completed data.

## Feature: Configure A Data Source Through The API

### User story

As a data owner, I want to configure source adapters through the app or REST API so that my archived workouts can be synchronized.

### Behavior

1. The owner creates a source with a unique display name, immutable type, `autoSyncEnabled`, and adapter-specific `config`.
2. Supported MVP types are `health-auto-export-local` and
   `health-auto-export-icloud`; Milestone 3 introduces local/NFS and Milestone 8
   adds iCloud to the discriminated API union.
3. OpenAPI selects and validates the correct config schema from `type`.
4. Creation or a canonical config change encrypts the complete adapter config, sets `checking-connection`, and enqueues a high-priority connection check. Metadata-only or identical-config updates do not enqueue another check unless the owner explicitly requests `checkConnection=true`; that request increments generation and snapshots a fresh check even for unchanged configuration.
5. A successful check sets `connected`.
6. A failed check sets `connection-failed` and creates an error notification.
7. A failed source cannot be selected for ingest until its config passes a later check.
8. Partial updates retain omitted settings and write-only secrets.
9. Required secret values cannot be cleared with `null` or an empty string.
10. iCloud setup is external and owner-operated; the product never imports a host
    rclone config automatically or asks for 2FA in-app. Source configuration is
    also available through **Data Sources** below **Preferences** in the owner
    profile menu. It uses the same private APIs and supersedes the earlier
    permanent source-UI exclusion, separately from M10 administration. It never
    authorizes administrator source access.

### Owner Data Sources Dialog

- Title **Data Sources**; reuse Preferences' compact insets, fixed header/actions
  and independently scrolling body, but opt into content-sized height: the table-
  only view is compact, Add/Edit fits visible fields, and only viewport overflow
  scrolls. This exception does not change standard dialog sizing. The **Sources** section
  reuses the admin Users/Workout log table, sorting, ellipsis menu and empty-state
  typography, without pagination. Columns are **Name**, **Type**, **Auto Sync**,
  **Status** and row actions. Status labels are **Connecting**, **Connected** and
  **Connection failed**; API status values are unchanged. Empty copy is
  **No data sources configured.**.
- **Add a source** appears opposite Sources. Row actions are **Edit source**, then
  a divider and red **Delete source**. Deletion names the selected source in a
  compact confirmation; failed deletion retains the row. Imported workouts remain.
  Its prompt is **Delete <source-name>? Its connection credentials will be deleted
  and its active sync jobs cancelled. Imported workouts will be retained.**. A
  transparent second-level backdrop avoids dimming the base content twice.
- The editor is initially hidden. Add/Edit reveals **New source / Edit source**.
  Type choices, in order, are **Health Auto Export — iCloud Drive** and **Health
  Auto Export — Server Path**, using the shared Preferences select. Type is locked
  once persisted. Default names are **Health Auto Export iCloud Drive** and
  **Health Auto Export Server Path**; changing type updates an untouched default
  name, while retaining a custom name. Cancel clears and hides the editor.
- Name is limited to 200 characters. Path is full-width in New/Edit for both types,
  limited to 250 and fixed-width. Server Path styled help is exactly two lines:
  **Path in ingest worker container containing** and
  **HealthAutoExport-YYYY-MM-DD.json files**. iCloud path help retains its wording.
- iCloud additionally has write-only **Apple ID** (320), masked **Password** (4096),
  **Cookies** (4000), **Trust token** (250). All entered configuration values,
  including masked Password, use fixed-width text at the existing size. Styled
  input help identifies `apple_id`, `password`, `cookies`
  or `trust_token` from `rclone config show`. No native title tooltip or in-app
  Apple authentication is introduced.
- On iCloud forms, Path spans the form; Apple ID and Password share the next row;
  Cookies is a full-width three-visible-line textarea; Trust token is a full-width
  single-line input. All input help uses the shared styled cursor-follow tooltip,
  never native `title` attributes. The last visible fields and footer actions have
  equal standard spacing above/below the divider, without a blank reserved area.
- Editing leaves every write-only input empty with dimmed placeholder exactly
  **existing value retained unless provided**. Only placeholder glyphs use scoped
  proportional italic text; entered values stay normal fixed-width at unchanged
  size. Omit empty
  credential fields from PATCH, preserving their encrypted values and optional
  client identity. Do not return stored credentials or copy drafts into persistent
  storage, query-cache mutation variables, diagnostics or raw error output.
- A divider precedes Cancel/Save. Save waits in the dialog for the saved source
  generation's connection result, with a 30-second UI deadline and pending guards.
  Success hides/clears the editor and refreshes Sources/Data Sync. Failure retains
  the editor with a small **Connection check failed** frame and safe actionable
  copy. A newer generation cannot satisfy an older Save. A persisted new source
  keeps its identity for retries, using PATCH rather than duplicate POST.
- The existing API persists submitted configuration before asynchronous checking;
  a failed check retains that configuration and blocks ingest. Cancel discards
  the editor, not an already accepted server mutation. Submitted write-only values
  are cleared after acknowledgement. New UI-created sources default to Auto Sync disabled;
  edits load the stored flag. The last field, below Path or Trust token, is
  **Enable automated sync (<interval>)**. It uses the same shared cadence formatter
  and schedule response as the Data Sync Automated card, lowercased (for example,
  **once a day**). It maps directly to `autoSyncEnabled` on POST/PATCH and retains
  the chosen value while changing new-source type or retrying. No source-type
  mutation is exposed.
- Rclone credential inputs have source-specific names, form-level autocomplete
  suppression, Password's `new-password` hint, and password-manager ignore hints
  for Apple ID/Password. These are browser hints, not a guarantee against a browser
  extension that overrides them. No hidden decoys or read-only/focus tricks are used.
- Checks use an independent serial worker lane and sibling private staging root,
  so an active ingest does not occupy the check executor. Worker operations have
  a 25-second deadline and ordinary fenced completion/snapshot cleanup. API work
  remains authorization/schema validation and enqueueing; no remote I/O runs in
  an HTTP handler. The browser polls the accepted source generation for its result.

### iCloud Contract (M8 Owner Review)

`health-auto-export-icloud` accepts only a typed, closed version-1 configuration:
required `version`, `path`, `appleId`, `password`, `cookies` and `trustToken`, with
optional non-secret `clientId`. Apple ID and all credentials are write-only;
responses return only `version`, `path` and optional `clientId`, with no masked
secret placeholders. Password is rclone-obscured, not encrypted by obscuring;
cookies are the complete ready-session string, not selected cookie names.
The worker fixes native backend/service to `iclouddrive`/`drive`.
`description`, `_auth_session`, remote aliases and arbitrary flags are unsupported.

PATCH retains omitted credentials and rejects null/empty required values.
`clientId` alone permits an empty string to restore native identity. Non-secret
canonical config changes also require a new asynchronous check. Native 1.75.2
requires a trust token; the application additionally requires cookies from
completed external authentication. Exact limits and private owner setup are in
[iCloud Source Setup](icloud-source-setup.md).

At that checkpoint, the approved active sequence was **001-028**, with **no active
029**. Folded 028 combines the generation-lock fix, strict eligible cohorts and mixed-failure automatic
policy into the pipeline. Source `SupportedSchemaVersion`, schema and minimum
runtime were **28**, marker **27**. Generation locking, exact eligible cohorts,
mixed-failure automatic retry, exact parking repair and the UI follow-up are
**deployed**. Equal numeric versions do not establish compatibility.
Native Goose does not replay applied 028. Old-contract 28 requires a new private
guarded offline repair, explicit approval and protected actual-backup rehearsal
under the actual migration role with new source/catalog proof. Pause all old
API/ingest/Coverage runtimes, and tiles if affected by DDL constraints. Old
apply-10/defer-6 ABIs are absent: old runtime-28 readiness fails; new readiness
requires private builders. No rolling reuse of old images is supported. Preserve
ledger 0-28 IDs/timestamps without DML/rewrite, data, roots/budgets, sources and
credentials. Retain backups/artifacts; rollback requires the old database contract
and images, not images alone. The approved exact actual-profile repair passed
protected actual-backup rehearsal and live transactional application under the
real non-superuser migration login. New readiness and original-tuple/ledger
preservation passed. Available-map yearly recovery has independent closure;
capacity certification is not implied. See [migration recovery](database-migrations.md)
and [Milestone 8 closeout](development-progress.md#milestone-8-closeout) for current acceptance status.

### Validation

- Display names are case-insensitively unique within the account.
- `type` cannot change after creation.
- Local paths must be beneath an operator-approved root.
- Unknown config fields are rejected.
- Secrets never appear in source responses.

### Failure cases

- Invalid schema rejects the request synchronously.
- Connection failure preserves the submitted configuration but blocks ingest.
- A deleted or foreign source ID returns not found.

### Acceptance criteria

- Given valid local-source configuration, when the connection check succeeds, then the source becomes connected.
- Given an iCloud source with invalid credentials, when checking finishes, then the owner sees a connection-failed banner without credential details.
- Given a PATCH omitting a password, when the update succeeds, then the existing encrypted password remains in effect.

## Feature: Delete A Data Source

### User story

As a data owner, I want to delete an obsolete source without losing workouts already imported from it.

### Behavior

1. Deletion immediately removes the source from source APIs and sync selection.
2. The current encrypted configuration and credentials are cleared.
3. Active and queued ingest children using the source are cancelled.
4. Running jobs remove staged files and clear job-scoped config snapshots at a safe cancellation boundary.
5. Other source children in a parent ingest may continue.
6. Imported workouts remain visible with sanitized historical source provenance.
7. A minimal internal tombstone preserves referential integrity.
8. Deletion is a soft tombstone (`deleted_at`), not a NULL workout source. With the adoption contract requiring 26, only the live-to-deleted transition increments the account's workout ownership epoch; retained provider-backed workouts become eligible for another source to adopt.
9. Deletion does not immediately sync another source. Adoption waits for the next auto-sync or a new manual job; an already frozen ingest manifest is not reopened.

### Validation

- Only the owning data user may delete the source.
- Repeating deletion for the same ID returns not found.

### Failure cases

- Cleanup failure creates an error notification and retryable job without restoring the source.

### Acceptance criteria

- Given a running ingest using a source, when that source is deleted, then its child job is cancelled and no later file is processed from it.
- Given workouts imported by a deleted source, when Summary is queried, then those workouts remain available.
- Given a deleted source ID, when its CRUD endpoint is requested, then the API returns 404.

## Feature: Run Scheduled Data Sync

### User story

As a data owner, I want new workout exports imported automatically without daily interaction.

### Behavior

1. At the configured cadence, each account creates an ingest parent for all connected sources where `autoSyncEnabled` is true.
2. Each selected source runs as a child job.
3. Incremental discovery processes new or changed files and, with the contract requiring 26, previously ownership-blocked files whose captured epoch is stale.
4. A day without an export is a successful no-data result.
5. A source becomes stale after the configured number of calendar days without a newly discovered export, default three.
6. Staleness is a warning that may also mean the user did not work out; it is not an ingest-job failure.
7. A newly discovered export clears the stale condition and its reminder.
8. Dismissing an unresolved stale warning follows reminder behavior.
9. Scheduled success remains silent.
10. Partial or complete failure creates a warning or error notification.
11. The operator cadence defaults to `AUTO_SYNC_INTERVAL=24h` and may be set to `12h`; UI polling does not change sync cadence.
12. iCloud performs one application metadata discovery per sync. A successful object with unchanged remote ID, size and modification time skips without download unless its ownership-blocked epoch is stale. Same-metadata content overwrites remain undetectable absent forced reprocessing; native operations may make additional listings/HTTP requests.
13. Successful checksums remain valid for all-blocked and mixed files. Any `skipped_live_owner` event marks the file with the epoch captured before ownership decisions. Same-epoch sync skips it; the next fresh incremental job after source deletion rereads it even with identical metadata/bytes. Adoption and cookie/auth generation changes do not advance this epoch.

### Validation

- Disconnected, checking, failed, deleted, or auto-sync-disabled sources are not scheduled.
- Equivalent queued or running jobs are reused rather than duplicated.

### Failure cases

- One failed source does not block other source children.
- Authentication expiry reports a source error and does not leak rclone output or credentials.
- Worker interruption leaves a retryable job and recoverable file state.
- Known expired trust, failed authentication or explicit missing PCS approval during iCloud ingest sets `connection-failed`, notifies the owner and blocks later ingest. Transient upstream ingest errors fail the job but leave a connected source connected; transient explicit-check failures still follow the failed-check contract. Reauthentication is external, followed by a private owner PATCH and successful check.

### Acceptance criteria

- Given two auto-sync sources, when the cadence fires, then one parent with two source children is created.
- Given no new file, when incremental sync completes, then the child succeeds with zero imported workouts.
- Given a terminal ingest with a failed child and at least one created or updated workout, then its status is partially_succeeded; successful reads, skips and unchanged workouts alone do not qualify under migration 029.

## Feature: Run Manual Sync

### User story

As a data owner, I want to synchronize selected sources and historical periods on demand.

### Behavior

1. Manual Sync is available from the avatar menu.
2. The dialog lists all current sources.
3. On initial load, select the sole Connected source, regardless of source type or automated-sync setting. If zero or multiple sources are Connected, leave all sources unselected. Apply this default once; subsequent refreshes preserve the user's selection. Disconnected sources cannot be selected.
4. New data only is the default mode.
5. Specific date range requires inclusive start and end dates.
6. New-data mode discovers new or changed files and stale ownership-blocked files under the contract requiring 26.
7. New bounded runs default to the incremental metadata-skip policy within the inclusive export-file date range. Unchanged successful files skip unless ownership-blocked evidence is stale.
8. The request creates one parent job and one child per selected source.
9. Exact equivalent active work returns the existing job.
10. Manual completion creates an info, warning, or error notification according to outcome.
11. Only in Specific date range mode, an unchecked-by-default **Reprocess all files in this date range** checkbox appears below the Start date and End date inputs and above **Start sync**. Hovering the checkbox or label, or focusing the checkbox, shows the tooltip: **Download and process every eligible file in this date range, including previously successful files. This may be slow and transfer substantial data.** New data only mode hides the option and omits its API field.
12. `IngestCreate.reprocessAllFiles` is an optional boolean allowed only with both valid ordered dates, even when `false`. Omission on a new bounded request means `false`; new bounded parent/child parameters persist the explicit boolean. Existing bounded jobs with an absent flag retain force semantics without history rewrites or another contract marker.
13. Explicit `true` requests the former all-eligible-files behavior for a fresh run, without bypassing date bounds, ownership, workout deduplication or route identity, and without guaranteeing forced Coverage. Retry always skips valid same-metadata successes on its own source-child ancestor path regardless of initial force; non-forced retry may additionally use valid source inventory. Frozen same-job actions and terminal checkpoints remain unchanged. See [Ingest Reprocessing](ingest-reprocessing.md) for receipts, legacy proof and v2 coalescing.

### Validation

- At least one connected source must be selected.
- Start and end dates must both be omitted or both supplied.
- Start date cannot follow end date.
- Supplying `reprocessAllFiles` without both valid dates is invalid, including `false`; `null` and non-booleans are invalid.

### Failure cases

- A connection-failed source cannot be submitted.
- A source deleted after submission cancels only its affected child.
- Partial completion reports successful and failed sources separately.

### Acceptance criteria

- Given no dates, when a new Manual Sync is submitted, then new, changed or stale ownership-blocked files are processed; an equivalent active job retains its frozen manifest.
- Given an explicit range with the option unchecked or the API flag omitted, then unchanged successful files skip without download, subject to current ownership evidence.
- Given the option checked, when a new bounded run is submitted, then every eligible file in that range is processed, including prior successes; its retry skips valid unchanged successes from its own ancestor path.
- Given a forced retry with failed, unreached or changed files lacking valid ancestor success, then those files process even if unrelated older successful inventory exists.
- Given a non-forced retry, then valid unchanged global source inventory may still skip files intentionally skipped by the original run; retries are not chain-only downloads.
- Given an equivalent active request, when submitted again, then the existing parent job ID is returned.

## Feature: Process Source Files Safely

### User story

As an operator, I want ingest to use bounded temporary storage so that historical imports cannot exhaust a worker volume.

### Behavior

1. Discovery records candidate metadata without downloading all files.
2. Each available processing slot handles one file through download, parse, commit, and cleanup.
3. Remote files/configs are staged in a private random job directory beneath `WORKER_STAGING_ROOT/private`, scoped to that job/source and never shared across independent worker processes.
4. A slot removes its staged file on success, failure, or cancellation before claiming another. A local attempt's private-directory cleanup failure blocks its terminal writes and that worker's new claims/readiness until cleanup is confirmed; retries are bounded.
5. Worker startup removes abandoned staging files.
6. File concurrency is limited per account, per worker, and globally.
7. Defaults are two files per account, two per worker, and four globally.
8. Connection checks and deletion jobs bypass ingest-file concurrency.
9. Same-job candidate actions and metadata are frozen for recovery. Terminal file checkpoints never reopen; restored committed events/counters do not count adoptions or live-owner skips twice.
10. After file processing and transport cleanup, publish its ingest counters before draining post-file Coverage work. Parent and source progress must not wait for Coverage readiness lookups. Recovered files still drain their durable Coverage obligations without counting their outcomes twice.

### Validation

- Filenames cannot escape the staging directory.
- Source files larger than an operator limit are rejected safely if such a limit is configured.
- iCloud has hard bounds: 64 MiB per file, 100000 discovery entries, 10000 eligible exports, 32 MiB metadata stdout, 60-second metadata operations and five-minute downloads including metadata checks. Only flat dated HealthAutoExport JSON files are eligible.
- Invalid JSON fails the file without committing a partial file result.

### Failure cases

- Cleanup failure is recorded safely and leaves recoverable work. The gate/startup scavenging is worker/pod-local, not distributed cross-pod cleanup or a lost-volume guarantee. Bounded retries cannot guarantee deletion under permission, read-only filesystem or hardware faults; eventual pod-volume disposal is a recovery boundary, not a physical-erasure promise.
- Encryption/credential-refresh persistence failure cannot falsely succeed or discard a recoverable job snapshot. Refreshed cookies use a generation/lease fence; obsolete refreshes are harmless and do not mutate job snapshots.
- A pod interruption leaves the file eligible for startup cleanup and job recovery.
- One malformed workout does not expose raw payload data in logs.

### Acceptance criteria

- Given a range containing thousands of files, when ingest runs, then no worker stages the whole range at once.
- Given three source children for one account, when all have files, then no more than two files for that account process concurrently by default.

## Feature: Pipeline Committed Routes To Coverage

### Behavior

The following is the **folded-028 contract deployed in `dev-20261020`**.

1. With schema/runtime **28**, source workers hand off durable committed work after
   file and transport cleanup releases the file slot, while later files may still
   ingest. A file remains atomic; matching does not run inside its transaction.
   The runner downloads/processes files serially within a source job and performs
   cleanup before and after the file pipeline.
2. `worker.coverageBatchSize` / `WORKER_COVERAGE_BATCH_SIZE` sets threshold **B**,
   default **25**, valid **1-1000**. During active ingest, each normal parent requires
   **exactly B new eligible matching targets**, globally across regions within the
   ingest-root/source-sibling scope. Normal and adopted obligations share the
   cohort. Missing maps, current/live/unchanged authoritative failed or cancelled
   targets, non-GPS imports and deduplicated obligations do not count. No-GPS
   adoption is acknowledged without a matching job.
3. Private durable prepared metadata saves B and root/source-sibling scope across
   files/crashes. There is **no placeholder Coverage parent** while underfull;
   full parent and B children seal atomically. SQL counts all children, including
   terminal/superseded ones, against saved capacity. Configuration changes do not
   resize saved cohorts. Readiness precedes enqueue: unavailable normal work
   acknowledges without any matching job, but does not shrink an active-ingest
   cohort. Mixed regions are explicitly allowed; optional `coverage.regionIds`
   reports the actual child-region union, including retries. Existing `regionId`/
   `targetGeneration` are representative anchors, never complete matching scope.
   Per-route generation vectors, claim fencing and admission are unchanged.
4. Retry parents contain only the preceding attempt's eligible failed/cancelled
   routes, preserve their chain and inherited capacity, and never accept fresh
   routes to fill spare capacity. An unchanged normal import cannot reset a failed
   target's retry budget; a distinct adoption event alone supplies force-refresh
   authority for otherwise identical input.
5. Interruption, source failure or ingest cancellation does not erase committed
   derived-work obligations. A final partial cohort seals **only after the visible
   ingest root and all source siblings are terminal and due preparation is
   exhausted**, via leased account-reconciliation recovery, never active EOF.
   Failed/cancelled roots can legitimately retain committed-data obligations
   without reopening ingest. Active underfull cohorts remain protected; normal
   metadata updates/deduplication cannot revive cancelled route targets and retain
   their cancellation footprint. Coverage jobs have their own cancellation lifecycle.
6. Preparation keeps bounded metadata and one route's coordinates at a time, not
   unbounded payloads/metadata in memory. ROI uses one efficient query per route,
   not per point. A trusted fingerprint of **all catalog regions, configuration,
   boundaries and active heads** must match in the same Repeatable Read ROI lookup
   snapshot and before seal. Mismatch invalidates preparation for fresh ROI; it
   never blesses old readiness by substituting a new generation vector. Separate
   APP/OSM databases do not provide a cross-database latest-state atomicity claim.

### Failure Cases

- Readiness snapshots cover the full canonical raw input, including zero through
  250,000 points, without truncation. Matching still allows only 50,000 original
  points: overflow is a typed permanent Coverage processing failure, not a failed
  source import. Stored raw routes remain available.
- Only verified typed transient readiness errors durably defer the affected
  obligation for one hour. Permanent, cancelled or unknown error branches surface
  safely rather than being disguised as transient success; every joined error
  branch must qualify for deferral.
- Missing maps acknowledge normal readiness without a matching job; adoption
  retains a deferred refresh. Later map promotion uses ordinary reconciliation.
- Matching policy remains `coverage-production-v1`, with unchanged 500m CPU,
  256 MiB memory, two lanes, admission 2/2/2 and one slot per reader. Batching does
  not certify capacity or increase concurrency, retry or execution budgets.
  The folded lock fix likewise preserves 6/10/15-minute timeout steps and the
  default one additional automatic retry; it adds no worker transaction retry.

### Acceptance Criteria

- A synthetic committed file with 63 eligible new routes at B=25 seals 25/25
  during active ingest; 13 seal only through account recovery after the visible
  root/all source siblings are terminal and due preparation is exhausted. File
  commits remain atomic.
- A three-route retry from a 25-route parent remains a three-route chained retry;
  a fresh 25-route batch uses a normal root, never that retry's spare capacity.
- Recovery and concurrent enqueue preserve receipt idempotency, parent caps,
  ownership/lease/content fences and retry budgets. Ingest completion still does
  not certify Coverage completion.
- When readiness excludes candidates, unavailable receipts have no matching jobs;
  prepared eligible targets wait across files/crashes for B, including mixed
  regions. No smaller active-ingest normal parent or placeholder is created.
  A small retry cohort remains separate, not topped up with new inputs to reach B.
- With folded 028, concurrent ingest and Coverage retain atomic file/checkpoint,
  evidence/progress and map-generation commits. A failed deferred generation
  update rolls back the corresponding transaction rather than reporting partial
  success. Earlier isolated verification is not a fresh final-contract test,
  live rollout or capacity receipt.

## Feature: Normalize And Upsert Workouts

### User story

As a data owner, I want repeatable imports that preserve provider meaning without creating duplicates.

### Behavior

The user-approved identity/adoption rules **require schema and runtime 26**,
including migrations 024-026. See
[Development Progress](development-progress.md) for live-version evidence. These
criteria are not a deployment, live-cloud smoke or acceptance receipt.

1. Account plus provider namespace plus opaque provider workout ID identifies a workout globally within that account. Local and iCloud Health Auto Export transports share the `health-auto-export` namespace.
2. A fallback fingerprint is used only when the provider ID is absent; it remains conservative and source-local, without automatic cross-source adoption.
3. Provider workout names map to normalized workout-type records. App-facing names use Outdoor Run, Outdoor Walk, Outdoor Cycle, Indoor Run, Indoor Walk and Indoor Cycle for the corresponding qualified aliases. Unknown and unqualified names remain unchanged. Display normalization does not change provider-derived type keys, content hashes, fallback identities or deletion suppression.
4. Provider aggregate values take precedence over incomplete sample sums.
5. Provider duration and original timestamp offsets are preserved.
6. For the same mapped source, unchanged content records a match without replacing raw data; changed content updates the same UUID transactionally.
7. Changed routes rebuild dependent coverage, elevation, timezone, and aggregate data.
8. Private import events distinguish `created`, `updated`, `matched_unchanged`, `adopted` and `skipped_live_owner`. Aggregate progress counts `adopted` as Updated and `skipped_live_owner` as Unchanged, not as proof of identical incoming content.
9. Distinct provider identities remain distinct even when times/routes overlap. A matching identity owned by another live source is skipped without duplicate creation, payload update or ownership theft. Live means not deleted, regardless of auto-sync or connection status.
10. Exactly one matching provider-backed workout owned by a deleted source is adopted under the same UUID. Incoming payload is authoritative, including replacement of changed header, aggregates and route data; previous source/file/job import provenance remains historical and unchanged. Source type stays immutable in the API, and switching sources needs no workout-history wipe.
11. Every successful adoption durably requests Coverage in the same file transaction, including unchanged content/route. Post-commit ingest repair or leased reconciliation consumes the outbox; content and route hashes remain genuine, not manipulated to force rematching. Missing map data defers matching; no-route/deleted workouts need no match.
12. Pending/completed workout-deletion tombstones suppress resurrection through replacement sources. Source deletion alone retains workouts and permits eligible adoption; it is not workout deletion.

### Validation

- Provider-backed identities are unique by account, `health-auto-export` namespace and opaque provider ID, across local/iCloud sources.
- Legacy duplicate identities make migration fail closed; ambiguous identity resolution never silently merges UUIDs or picks a winner.
- Metric units must be known or retained with a normalization warning.
- Missing optional route or metric data does not invalidate an otherwise valid workout.

### Failure cases

- An invalid workout is reported without corrupting previously committed workouts.
- Suspicious units or quality values create adapter warnings rather than silent reinterpretation.
- Identity conflicts fail without leaking provider IDs or partially committing a file. No date/GPS similarity fallback authorizes adoption.

### Acceptance criteria

- Given the same unchanged workout in an explicitly forced bounded reimport through its mapped source, when processed, then no duplicate is created and matched_unchanged provenance is appended; file skipping alone does not append a new workout import event.
- Given changed route content for the same provider ID through its mapped source, when reingested, then the existing workout and derived map data are updated.
- Given the same provider identity owned by another non-deleted source, even disabled or disconnected, when imported, then no duplicate/update/adoption occurs and `skipped_live_owner` is recorded.
- Given a single deleted-source owner, when another source imports that identity, then the UUID and history survive, incoming content becomes authoritative and `adopted` plus a durable Coverage request commit together even if bytes are unchanged.
- Given an unchanged file blocked by Local A while Cloud B syncs, when A is deleted, then the epoch advances and the next fresh B sync rereads/adopts; a later B sync skips unchanged metadata. See the [worked example](icloud-source-setup.md#worked-source-switch-example).
- Given a pending/completed workout deletion or ambiguous identity, when a replacement source imports it, then it cannot resurrect or silently merge the workout.

## Feature: Retain And Export Route Data

### User story

As a data owner, I want preserved original route evidence and consistent filtered
route data for display, analysis, and export.

### Behavior

1. Ingest retains ordered source points with provider sequence.
2. Retained fields include timestamp, longitude, latitude, altitude, speed, course, and all four accuracy values.
3. Duplicate timestamps do not collapse points.
4. GeoJSON is the default route representation.
5. GeoJSON contains a LineString for one display component or a MultiLineString for several, plus workout metadata. It never connects across display gaps. Use consistently 3D coordinates only when every retained point has altitude, otherwise 2D.
6. Points format uses schema version 2 and `raw-route-display-v3`, returning normalized fields for exactly the retained displayed samples in canonical units. Preserve original sequence numbers, including holes, and include a positive `component` identifier; consumers must not connect different components. Original observations remain retained internally as evidence, rather than being included in these exports.
7. Downloads use `YYYY-MM-DD-workout-type.geojson` or `.json`.
8. Export bounds, point count and elevation describe the retained samples. Elevation gain does not cross component breaks.

### Validation

- Latitude, longitude, and timestamp are required for a usable point.
- Invalid optional accuracy values are retained as unavailable or quality-flagged.
- Point order follows provider sequence, not timestamp uniqueness.

### Failure cases

- Missing route altitude produces a 2D route without failing the workout.
- Invalid optional point properties do not reject otherwise usable coordinates.
- A route with no surviving renderable component returns a conflict from both exports; do not fall back to excluded evidence points.

### Acceptance criteria

- Given retained duplicate point timestamps, when points are exported, then both appear in source order.
- Given route altitude, when GeoJSON is exported, then coordinates contain longitude, latitude, and altitude.
- Given any supported provider, when points are exported, then the same normalized JSON schema is used.
- Given rejected GPS samples, map lines/dots, focused endpoints/direction markers and both exports exclude them consistently. Exported components and full-detail map geometry use identical retained samples; lower-zoom map simplification remains a display-only precision optimization.

## Feature: Select Date Ranges

### User story

As a user, I want explicit dates and common shortcuts so that Summary, Map and Stats show the same period.

### Behavior

1. Desktop uses a Grafana-inspired date control near the view selectors.
2. Supported shortcuts are This week, Last week, Last 7 days, Last 30 days, This month, Last month, This year, Last year, and **All time**, in that order. All time appears immediately below Last year and above the divider/Custom section, independently of recent custom ranges.
3. API reads require either explicit start/end dates or one `dateRangeEnum`.
4. `tz` overrides the user's default time zone for enum resolution.
5. `tz` is silently ignored with explicit dates.
6. Resolved dates and time zone are returned in response metadata.
7. Summary, Map and Stats share the same selection, including shortcuts, recent custom ranges and last-used preference behavior. Stats places the existing picker at the top of its left panel.
8. Explicit and shortcut boundaries are inclusive calendar dates.
9. Bounded workout membership uses the workout's local start date. All time omits date filtering, including for workouts/routes with no local date; it does not substitute an arbitrarily wide bounded/custom interval.
10. Last 7 days means today plus the preceding six dates; Last 30 days means today plus the preceding 29 dates.
11. This/Last week honor the user's first-weekday preference; month and year shortcuts use calendar boundaries.
12. Daylight-saving changes do not alter calendar-date membership.
13. Summary, Map and Stats use the same compact custom-range dialog, including
    Road Coverage-style content insets, balanced action-divider spacing,
    consistent Cancel/Apply gaps and an orange hover border on the close button.
14. All time display metadata starts at the account's earliest workout date and
    ends at the later of today in the selected time zone or the account's latest
    workout date. An unknown local date falls back to the recorded start in that
    zone for metadata only; an empty account displays today to today. Metadata
    and totals use the same snapshot, and those dates do not constrain All time
    queries. Future-dated and undated workouts remain included.
15. An All time Map capability captures every available routed workout without
    the bounded complete-range selection limit. Raw routes, Coverage detail/focus,
    and tiles retain normal ownership, checked membership, readiness and deletion
    rules while bypassing date predicates. Bounded and unfiltered contexts remain
    distinct even when their displayed date bounds are identical.

### Validation

- Explicit dates must appear together.
- Enum and explicit dates are mutually exclusive.
- Unknown enums and IANA time zones are rejected.

### Failure cases

- Missing all date selectors rejects date-bounded reads.
- Invalid ordering rejects the request without running a query.

### Acceptance criteria

- Given Monday week start, when thisWeek is resolved on Wednesday, then the range begins Monday.
- Given an explicit range and `tz`, when queried, then the explicit dates are used unchanged.
- Given older, undated and future-dated workouts, All time includes all owner
  workouts/routes and its metadata end is never earlier than local today.

## Feature: Open A Coverage Job Route On Map

### Behavior

1. Every available workout reference in a Coverage update's Route runs list is a
   real clickable link, including failed/pending route jobs. The entire type/date
   reference uses theme text until hover/focus turns it orange with a hand cursor.
   A missing reference remains the existing unavailable placeholder.
2. A reusable workout-to-Map action keeps the active date selection when it
   already contains the workout. Otherwise it selects the first covering shortcut
   in picker order, ending with All time; unknown dates use All time. Calendar
   evaluation honors the selected time zone and first weekday.
3. A changed range applies and persists like a deliberate picker selection,
   resetting the shared workout sort/page, Map catalog/checked scope/old focus and
   range-dependent report state. It does not enter recent custom-range history.
4. After the new capability/catalog loads, focus and check the actual target ID,
   fit its appropriate geometry and scroll it to the center of the route list.
   Prefer renderable Coverage; fall back to raw Routes when no Coverage exists.
   The failed state of a job is not itself proof that prior Coverage is absent.
5. Stats drill-downs use the existing preserve-range policy. In particular, a
   Previous visit outside the report range stays non-clickable: this new action
   must not silently expand that report or alter its statistics.

## Feature: Review Summary

Range totals retain their existing refresh fade when Summary revalidates on tab
return: cards fade from 30% to full opacity over 500ms using ten equal 50ms steps
(`steps(10, end)`, seven percentage points per step). It does not delay requests
or interactions, does not dim the Workout log, and becomes immediate under the
reduced-motion preference.

### User story

As a user, I want a concise statistical and tabular summary of workouts in a selected period.

### Behavior

1. Summary displays compact aggregate values above the table.
   Each value independently fits its shaped text to available width when needed,
   shrinking continuously and no further than needed below the normal CSS size.
   All cards and both Workouts/With routes counts share the same implementation.
   Keep the card grid unchanged, remeasure after value/width/font changes, and
   restore normal sizing for any line that fits without reduction. A long value
   cannot reduce the font size of another value, including its paired count.
2. Hovering or keyboard-focusing an aggregate shows values grouped by workout type.
3. The table defaults to newest workout first.
4. Clicking a header changes sorting and toggles direction.
5. The API supports multiple sort columns even though the UI normally uses one.
6. Null values sort last.
7. Desktop shows preferred columns.
8. Mobile shows date/time zone, workout type, and duration, with tap-to-expand details.
9. Elevation fields are available for all routes and shown by default for configured types such as Hiking.
10. Pagination keeps the current table mounted at full opacity while the next page loads, reserves its measured height so a short final page does not collapse the content viewport, swaps rows in place, and restores the captured owning content-pane scroll position.
11. The Workouts card pairs the total workout count with an equally prominent, right-aligned count labeled With routes. The labels By workout type and With routes share the footer row. These counts remain counts in averages mode; measured distance without GPS does not imply a route.
12. The Distance card and its type breakdown include every workout type with a valid measured distance, independently of GPS or activity name. Distance averages divide measured distance by the number of workouts with that measurement, globally and per type. Explicit measured zero counts; missing or unsupported-unit measurements do not become zero or enter the denominator. Other aggregate denominators are unchanged.
13. Each aggregate has exactly one sequential keyboard stop. Tab visits Workouts, Duration, Distance and Energy, and Shift+Tab reverses that order. Preview-only duration hints retain styled precision help without their own Tab stops; hidden previews are inert and their containers are not sequential focus targets. Escape closes the preview while retaining card focus. When a preview scrolls internally, Arrow Up/Down, Page Up/Down and Home/End scroll it from the card; modified browser shortcuts retain their normal behavior. Enter/Space retain the existing totals/averages toggle.
14. Ordinary mouse hover retains the original collapsed card's boundary: leaving
    it closes the breakdown. Holding Shift while a card is open extends the hover
    boundary to include its visible breakdown. Leaving both areas closes it even
    while Shift remains held. Releasing Shift after entering the breakdown closes
    it if the cursor is outside the original card; releasing over the original
    card keeps normal hover behavior. Keyboard-only Shift+Tab remains unaffected.
15. While Shift is held, clicking the original card, breakdown rows or expanded
    padding toggles all cards between totals and averages exactly once. Expanded
    clicking delegates to the existing card action and retains that button's
    focus; no extra sequential focus targets are added. A hovered breakdown row
    uses the same subtle theme-aware row-hover mixture as the Workout log, with
    readable type and value text.
16. One Summary-owned preview identity arbitrates keyboard and mouse opening.
    New hover/focus supersedes the old card, and an old blur cannot dismiss the
    new owner. Window blur, document hiding, outside focus, Escape, resize or
    retained-tab exit dismisses the preview and clears Shift/hint state. Window
    focus restoration cannot reopen the old keyboard preview without fresh
    pointer or Tab intent.
17. OS app switching may omit pointer-leave or modifier-keyup events, or restore
    a cached hover target. An open mouse preview therefore reconciles document
    pointer/mouse movement against its actual current bounds and observed Shift
    state. Moving over the Workout log dismisses an obsolete hover without
    requiring another card crossing. Window-focus return dismisses old previews;
    pressing Alt, Command/Meta or Ctrl alone does not. OS/browser shortcuts remain
    unconsumed; modified Tab
    does not count as fresh card-navigation intent. Keyboard-only previews keep
    their focus-based behavior.

### Validation

- Page size respects the configured maximum.
- Sort fields are allowlisted; duplicate or unknown fields are rejected.
- Required date-range rules apply.

### Failure cases

- No workouts produces a valid empty state with zero aggregates.
- Unsupported optional statistics display as unavailable rather than zero.

### Acceptance criteria

- Given workouts of several types, when an aggregate is hovered, then its type breakdown is shown.
- Given an open breakdown and Shift held, moving into a row keeps it open and
  highlights that row; expanded clicks toggle mode once. Releasing Shift outside
  the original card or leaving the expanded boundary closes it.
- Given mixed keyboard and mouse interaction, at most one aggregate preview is
  visible. Losing window focus dismisses it even if Shift keyup is not received.
- Given a mobile viewport, when a row is tapped, then additional workout details expand inline.
- Given equal primary sort values, then stable UUID tie-breaking prevents records moving between pages.
- Given route-less workouts with measured distance, when totals or averages are displayed, then their types appear in the Distance breakdown. Workouts without measured distance remain excluded even if they have GPS routes.
- Given a workout deletion, when the optimistic update hides it, then total, routed and measured-distance counts update independently and roll back together if enqueueing fails.

## Feature: Filter Workout Log Columns

### Behavior

- Date, Type, Duration, Distance, Pace, Calories, Heart rate and Elev gain each
  support a column filter. A short header click or Enter sorts as before; holding
  for the shared 500 ms opens its popup without sorting. Space or Arrow Down opens
  it from the focused header. The styled three-line help reads:
  `Click to sort or toggle sort direction; click`,
  `and hold to apply custom filter (use space`,
  `and down keys in keyboard mode)`.
- At most one column popup is open. It uses the Map workout-type popup's themed
  composition and explicit Cancel/Apply footer. Outside interaction, Escape,
  Cancel and retained-tab exit discard the draft. A superseded popup cannot
  restore focus over its replacement. Only Apply changes the committed filters.
  All column popups sit 0.7rem lower and to the right of their normal placement.
- Date lists all seven weekdays, beginning with the First weekday preference.
  Type lists only types represented anywhere in the complete selected date
  range, alphabetically, including workouts without routes. Both initially check
  every item. The heading's None link (Deselect all help) appears only when all
  items are checked; otherwise All (Select all help) selects every item. Empty
  selections match nothing. A fully checked selection disables that filter.
  All/None and Enable/Disable links use plain themed orange text and a hand cursor,
  without hover underlining. Their styled help dismisses when the pointer leaves,
  even if popup autofocus or earlier keyboard navigation keeps the button focused.
  Genuine keyboard focus still provides help until pointer interaction supersedes it.
  These links have a 3rem-wide, 1rem-tall clickable area with right-aligned text on
  the title's original baseline. Tooltip wrappers match the button's exact bounds
  and use the hand cursor; there is no extra help-cursor strip above or below it.
- Type choices use `ceil(count / 8)` columns and `ceil(count / columns)` rows,
  filling top-to-bottom before the next column: 12 becomes 6/6, 16 becomes 8/8
  and 19 becomes 7/7/5. Checkbox columns are equal-width and at least 16rem wide;
  the same minimum applies to the weekday column. Wide lists scroll inside the
  viewport-contained popup.
- Numeric popups have a Show duration/distance/pace/calorie/heart rate/elevation
  gain range title, with enough space before the heading's Enable/Disable link
  (Enable filter/Disable filter help). Disabled filters retain their draft
  operator/value; enabling, disabling and moving a slider commit only with Apply.
  Disabled styling also mutes the dropdown chevron and both minimum/maximum slider
  labels. Enabling restores their usual colors along with the existing controls.
  Comparison operators are `>`, `≥`, `<`, `≤`, initially `>`, except Pace uses
  `<`, `≤`, `>`, `≥`, initially `<`. Inclusive symbols keep the same API operators.
- An operator dropdown precedes the slider on the same row. Its current value is
  centered above the slider; minimum/maximum labels sit outside its 20rem track
  area. Narrow viewports can reduce that width to keep controls/actions reachable.
  Slider arrows adjust the value rather than paging the background table.
  Keep the native HTML range input for keyboard, touch and snapping behavior, but
  style it with a neutral track, no value/progress fill, and an elongated rounded
  rectangular handle. Disabled controls use the corresponding themed muted colors.

| Column | Minimum / initial value | Snap interval | Maximum |
|---|---|---|---|
| Duration | 0m | 15m | Range maximum rounded up, capped at 24h 0m |
| Distance | 0 mi or 0 km | 0.5 mi or 0.5 km | Range maximum rounded up; uncapped |
| Pace | Minimum 1m 0s; initially the maximum | 30s | Range maximum in preferred distance units rounded up, capped at 60m 0s |
| Calories | 0 kcal | 100 kcal | Range maximum rounded up; uncapped |
| Heart rate | 0 bpm | 10 bpm | Range maximum average heart rate rounded up; uncapped |
| Elev gain | 0 ft or 0 m | 100 ft or 25 m | Range maximum rounded up; uncapped |

- Slider domains and type choices ignore all column filters and pagination. An
  empty/very-small domain reserves at least one step above its minimum. Thresholds
  compare exact underlying measurements, not rounded table labels. Missing or
  unsupported measurements do not satisfy an enabled numeric comparison.
- Applied column filters combine with AND on the server before count, sort and
  pagination. Date compares the recorded local start weekday, not UTC or the
  account's current time zone. Unknown local dates cannot match a selected day.
- Each enabled filter has the Font Awesome classic filter SVG immediately before
  its column label in themed medium blue. Clear all filters replaces the redundant
  session/page heading text; it is gray and disabled without an enabled filter.
  Clearing resets selections/disables comparisons without changing sort.
  Clear all filters uses plain themed link text and a hand cursor when enabled,
  without an underline on hover; its disabled gray/default-cursor state is unchanged.
- Apply and clear return to page 1. Preserve existing rows at full opacity until
  the latest compatible response arrives, then replace positional cells in place.
  Reserve the unfiltered page capacity even for partial/zero results. A no-match
  result keeps headers, a centered message and stable pager placement. Pending or
  failed replacement disables old row actions; failure retains compatible rows
  read-only with Retry. Late responses cannot replace the current selection.
- Range totals/averages continue to describe the complete date range. The pager
  shows the filtered session count. Column filters are retained across tab changes
  within the current date/time-zone/weekday scope and reset at scope changes;
  unit changes preserve physical thresholds. Popups reopen on the current unit's
  nearest valid slider tick without committing until Apply. Narrow layouts expose
  the same column controls above the existing expandable workout cards.

### Acceptance Criteria

- A filter applied on page 2 searches all in-range workouts and returns page 1,
  while preserving sort and the unfiltered type choices/slider maxima.
- Combining weekday, workout type and numeric comparisons returns only records
  satisfying every enabled filter, with matching server totals/page counts.
- Cancel/outside/Escape leave committed criteria untouched; selecting all removes
  a checkbox filter, and clearing preserves the selected sort direction.
- Full, partial and zero-match results retain the same desktop table frame;
  replacement and error recovery do not erase compatible rows or retarget actions.

## Feature: Navigate Table Pages

In the authenticated shell, the app header and side controls remain bounded to
the viewport; only the active content pane scrolls. Stats has an independent fixed
sidebar/report split, stacked on narrow layouts. Summary/Data Sync scroll within
their retained workspaces; Map continues to size its stage and list to available
space. Preserve each owner position across tabs and use it for page/filter
anchoring rather than moving the document. The Task history bottom inset matches
Data Sync section gaps, except where a larger device safe area is required.

Centered table-state messages share a 1.1rem font, including Road Coverage,
New visits, New Coverage and Workout log loading/empty states. The shared viewport
applies that style to any message in the centered position without changing its
frozen row capacity or header placement.

Use the shared pager in Workout log, Road Coverage, Stats reports and visit
dialogs, Task history, and run-detail Files, Events and Logs.

1. Arrange six one-click buttons around `Page X of Y`: first page, back five,
   previous, then next, forward five and last page. Five-page jumps clamp at the
   first/last page. Disable all three left controls on page one and all three
   right controls on the last page; empty/single-page pagers disable all six.
2. Every button matches the compact Cancel/Apply height in Show workout types,
   with a width of **1.5 times that height**. Draw proportionately smaller
   bar/chevron symbols with **3px rounded SVG strokes**. First/last pipes and
   brackets touch at their stroke edges without overlapping. Keep equal button
   dimensions when controls wrap on narrow screens. Provide accessible action
   names, without visible text or native/styled tooltips.
3. Restore the shared theme-aware button surface for both enabled and disabled
   controls. Enabled symbols use the normal theme foreground and hand cursor;
   disabled symbols match the table's darker `n/a` gray and not-allowed cursor.
4. Server-paged jumps request only the destination page. Keep boundary states,
   colors and cursors steady during loading and same-page background refresh;
   ignore extra presses until the replacement renders, including immediate repeat
   clicks before the owner renders its pending state. Update boundary availability
   with the displayed result. Unavailable/stale failed results can still disable
   navigation until recovery. Retain existing rows and each owner's scroll,
   caching, error/retry and partial-page height behavior. Road Coverage continues
   paging its complete bounded client-side result.

## Feature: Keyboard Shortcuts

Authenticated application shortcuts are documented here and remain visually
undisclosed: do not add shortcut legends, key badges, label underlines or tooltip
suffixes. Letter commands are case-insensitive.

| Key | Action |
|---|---|
| `1` | Open Summary |
| `2` | Open Map |
| `3` | Open Stats |
| `4` | Open Data Sync |
| Left / Right arrow | Previous / next page in the active paged table/list |
| Shift + Left / Right arrow | Back / forward five pages, clamped at the boundary |
| `C` on Map | Select Coverage mode if available |
| `R` on Map | Select Routes mode |
| `F` on Map | Fit only the visible, persistently focused workout in the current mode |
| `M` on Stats | Select Month grouping |
| `W` on Stats / Consistency | Select Week grouping |
| `Y` on Stats / New Coverage | Select Year grouping |

1. A document-level handler receives the commands while Tab navigation focuses
   ordinary buttons, links or content. The web document must have keyboard focus;
   browser chrome, the address bar, DevTools and OS-reserved combinations remain
   outside application ownership. Tab/Shift+Tab, Enter/Space and Escape retain
   their established roles.
2. Switching application tabs by navigation or number key hands focus to the
   tab's content landmark, or the Map canvas once ready. Landmarks use a
   programmatic focus target, not an extra sequential stop. Preserve selected
   range, table state, route visibility/focus and camera contracts. Re-selecting
   the same shortcut destination focuses content without adding history entries.
   Do not outline the whole content landmark or canvas. Only Tab/Shift+Tab
   initiates keyboard-navigation highlighting: shortcut activation and arrow-key
   paging do not enable that mode. They preserve a mode already entered by Tab;
   pointer interaction exits it. Files/Events/Logs headings and other small
   controls show focus outlines only while that Tab-initiated mode is active.
3. Suppress all listed shortcuts while a text-entry surface is displayed in the
   active UI, even when another control has focus. This includes search/text,
   URL/email/password/tel, number/date/time fields, textareas and editable content.
   Hidden/inert surfaces do not block commands. Focused selects/comboboxes and
   widgets with their own editing keys keep native behavior; open menu/listbox
   typeahead is not intercepted.
   Modal table paging is the exception described next: only focused editing
   controls reserve its arrow keys; an unfocused visible search field does not.
4. A modal keeps ownership of its keyboard scope: application-tab and Map/Stats
   mode commands do not navigate behind it. Left/Right and Shift+Left/Right may
   target the modal's pager while its search field is visible but unfocused.
   When that field (or another editing widget) has focus, arrows retain native
   caret/selection behavior and never page either modal or background tables.
   Headers, pagination buttons, non-editing controls and dialog content can own
   modal paging. The exception applies only to paging; visible text entry still
   suppresses number/letter application shortcuts.
5. Page commands affect exactly one owner. Prefer the deepest table/list region
   containing focus, then the last interacted eligible region still on screen,
   then the tab's primary pager. Task history is Data Sync's primary pager;
   Files/Events/Logs use independent regions. Reuse normal destination clamping,
   pending-request fences and unavailable-state handling. Arrow repeats cannot
   bypass an in-flight request. Other held shortcut keys do not repeatedly
   navigate or refit.
6. Map `F` uses focused raw bounds in Routes, focused displayable Coverage bounds
   in production Coverage, or that workout's diagnostic bounds in diagnostic
   Coverage. With no visible focused workout, pending selection, missing bounds
   or an unavailable result, it does nothing. It never falls back to all checked
   routes and does not change checked membership or persistent focus.
7. Map mode and Stats grouping commands use the same actions as their controls.
   Map mode changes preserve the current viewport; fitting is an explicit action.
   A changed New Coverage grouping resets its page/sort; Consistency preserves its
   section state while changing periods. Requesting the already
   selected grouping is a no-op. Native Map canvas arrow navigation remains
   available when there is no eligible table pager.
8. Ignore Ctrl/Command/Alt combinations, IME composition and already-prevented
   events. Shortcut owners unregister on unmount so obsolete tabs/private scopes
   cannot receive commands.

## Feature: Explore Stats And New Coverage

**Milestone 9 is complete and accepted as of 11/12/2026.** Acceptance, verification
and the deployed checkpoint are recorded in
[M9 closeout](development-progress.md#milestone-9-closeout).
Later milestones are renumbered in the
[implementation plan](implementation-plan.md#milestone-sequence-update-1152026).

### User story

As a user, I want reports about workout, route and Coverage statistics and trends
so that I can see when I explored new places or returned after a long absence.

### Stats navigation and state

1. Add **Stats** between **Map** and **Data Sync** in the primary tab bar.
2. Use a left-side panel following Map's layout. Its top-most control is the same
   shared **Date range** picker, followed by the heading **Reports** and a list of
   selectable report titles. **Consistency** is first/default, followed by
   **New Coverage**.
   Reports uses the same all-caps typography as Summary's Range totals and Workout
   log headings. Every selectable report's whole row has a hand cursor.
3. Mark the selected report with the existing magenta focus/selection border,
   accessible selected-state semantics and normal keyboard focus treatment.
4. Store the active report in the parent UI frame, not only in the Stats child,
   so switching to Summary, Map or Data Sync and back restores it. Preserve each
   report's controls, sorting and pagination across tab switches as well.
5. The report area supports future tables and charts. **Reports** is the shared
   navigation term; report-specific controls belong above the report content.

### Consistency report

- Show **Show aggregate stats by** with exclusive Week/Month controls matching
  New Coverage; Week is initially selected. Guarded W/M shortcuts use the same
  actions. New Coverage retains M/Y. Editing, modifier, native-widget, composition
  and modal guards follow the shared keyboard contract.
- Align **Close all sections** at the control row's right, using the plain Clear
  all filters link treatment. Show **Open all sections** only when all seven are
  closed. Sections start open and their titled headers toggle them independently.
- Chart order: **Active days per week/month**, **Workout duration per week/month**,
  **Distance covered per week/month**, **Calories burned per week/month**,
  **Longest duration per workout per week/month**, **Longest distance per workout
  per week/month**, **Highest calories per workout per week/month**.
- Sections use Summary's shared maximum content width, bounded by the report
  area, and Data Sync card surfaces, heading treatment and interior padding. Each
  chart viewport is 1.5 times the responsive Range totals card height. Section
  titles have no divider line and use larger disclosure chevrons.
- Aggregate the complete shared range independently of Workout log filters/page.
  Active days count distinct recorded local dates, not workouts. Duration uses
  exact provider seconds; distance and total calories use the same normalized
  metrics as Workout log. Include empty calendar buckets; partial edges contain
  only in-range workouts. Unknown local dates are excluded and counted separately
  for All time. Missing measurements stay unavailable; empty bucket totals are
  zero and empty per-workout maxima are unavailable.
  Future calendar periods are omitted: use today in the resolved preferred time
  zone as the upper limit for dates and bucket generation, including the current
  partial week/month. Preserve the selected range itself; entirely future ranges
  have no plotted periods.
- Weeks start on the preferred weekday and use unpadded **M/D/YY** labels for that
  date; months use **Oct 2026**-style labels. Plot oldest-to-newest, left-to-right.
  Week and month bars use the same width, 75% of a common date-label slot width.
  Bars use **#bc067a**, 3px rounded top corners, and a bottom-to-top opacity
  gradient from 70% to 100%; overflow caps use **#d70d0d**. The chart fills its
  section even for short ranges. Pointer/trackpad/touch scrolling reaches the last
  period while the Y axis stays put. Native scrollbars are never shown. A hand
  cursor invites click-and-drag horizontal panning; vertical touch scrolling and
  pinch zoom remain available. Initial and Week/Month replacement views open at
  the right edge with the latest period fully visible. Later tab/report returns
  preserve a manually panned position.
  All charts share one horizontal position. Offset only the bar/date-label tracks
  to compensate for variable axis widths, keeping corresponding periods at the
  same screen X coordinates without changing Y-label/gridline origins. Dragging,
  trackpad and keyboard scrolling any chart updates the others; reopening a section
  joins the current shared position.
- Weekly active days uses domain 0-7, ticks/lines **2, 4, 6**, with no top line
  for 7 or 8. Monthly uses domain 0-31, ticks **7, 14, 21, 28**, with no top line
  at 31. Other charts have four equally spaced nonzero ticks. Their highest
  non-outlier value occupies 75%-100% of the top labeled value; prefer round steps
  within that bound rather than adding excessive headroom. Duration/sub-unit tick
  labels gain only the precision needed to remain distinct and nonzero; ordinary
  values retain Workout log formatting. Robust typical-value scaling prevents rare
  large values from dwarfing ordinary ones; excess appears as a short red segment
  above the top line, with the full actual value in shared delayed styled help.
  Week help reads `Week of 2/7/2022  ━  7 days`; month help reads
  `August 2022  ━  31 days` (full month). The single dash has a regular space plus
  a nonbreaking space on each side. Week tooltip dates are unpadded with
  four-digit years; values retain each chart's units. Suppress help during panning.
  After the shared initial delay, pointer help follows the cursor above it using
  the same 0.65rem gap as Summary's Range totals hint. Movement within the bar does
  not restart the delay or auto-hide visible help; leaving the bar, scrolling it
  away, dragging, changing context or losing the window dismisses it. Keyboard
  focus retains the normal element-anchored help path.
  Do not add an overflow-cap explanatory caption or reserve space for it. The
  chart-to-panel bottom inset equals the panel's other content insets.
  The bottom plot line always represents zero. Bars and their tooltip wrappers
  must share that baseline, including zero, very small and unavailable values;
  inline text-baseline alignment must not displace short bars below the axis.
  Use a 12px date-slot gap, reducing the previous inter-bar spacing by 4px without
  changing bar width. Measurement, virtualization and keyboard scrolling share
  that spacing definition.
  Active days per month alone awards a perfect calendar month with a gold star
  inside the top of its bar. Active days must equal that month's actual day count,
  including Gregorian leap-year rules. The star is 22px square; its equal
  left/top/right margins are half the remaining bar width. Partial months and weekly bars do not
  receive a star.
- Virtualize long timelines without truncating data. One roving bar stop per chart
  supports Left/Right and Home/End. Grouping, disclosures and chart scroll survive
  tab/report changes. Hidden reads/help pause. Compatible replacements retain old
  charts/labels at full opacity; errors keep accepted data with Retry. Account,
  range, time-zone, weekday and deletion boundaries clear incompatible pixels.

### New Coverage table

1. Center the report title in the main content area, with no subtitle. Below it,
   show **Show new visits by** at 1rem with exclusive Month/Year button-style
   controls matching Map's Routes/Coverage controls. Month is selected by default.
   Align the label/controls to the table's left edge. Use the same spacing token
   above the title, between title and controls, and between controls and the table.
2. Columns are **Month** (or **Year**), **New visits (all time)** and
   **New visits (past year)**. Month labels use full month and year,
   such as **November 2026**; year labels use four digits, such as **2026**.
3. Follow Summary's Workout log styling, sortable headers, sort indicators,
   pagination and the shared rows-per-page preference. All columns are sortable;
   the default is bucket date descending, using chronological dates rather than
   alphabetic month labels. Tied count sorts use bucket date descending.
   Center the table at one-half of Summary's shared Workout log content width,
   capped by available Stats content width; narrow layouts scroll inside the table.
   Month/Year receives 38% of the table width. Use the shared `.pagination` margin
   above page controls, without adding the report's section gap to that spacing.
4. Generate every calendar bucket intersecting the inclusive selected dates,
   even when no workouts or qualifying visits exist in it. Partial first/last
   buckets count only visits within the selected dates, not the entire month/year.
   3/25/2024 through 10/1/2026 therefore shows March 2024 through October 2026
   in Month mode, or 2024, 2025 and 2026 in Year mode.
5. Changing the range or Month/Year grouping returns to page 1 and the default
   bucket-descending sort, closes an obsolete detail dialog and retains the active
   report. A page-size change clamps the current page to the available pages.
6. Zero is plain, noninteractive text. For a nonzero count, the **entire table
   cell**, including its padding, activates the corresponding visit-detail dialog.
   Provide one keyboard-focusable link-style action per cell with an accessible
   name identifying the metric and bucket; Enter activates it.
   Clickable cells have hand cursors and normal text, changing only the value to
   orange when the pointer enters the cell. Use no underline or additional cell
   background; retain the standard row-level hover highlight.
7. Replace explanatory notices with the shared delayed, styled tooltip on the
   **New visits (past year)** column header, with exactly these two lines:

   ```text
   Includes first visits and return
   visits after more than one year
   ```

   "Past year" is relative to each candidate visit, not today, the beginning of
   the selected range, or the Last year shortcut. Do not use native HTML title help.
8. Paging retains the current rows at full opacity while fetching, swaps in the
   new rows only when ready, and preserves scroll position. Reserve
   `min(totalItems, pageSize)` row slots so the Previous/Next controls do not move
   on a short last page, including when restoring that page after a Map visit.
   Shorten the report table only when its total result fits less than one full page.
9. Month/Year changes retain the currently displayed rows and their matching
   bucket header/labels until replacement data arrives. Update those positional
   rows in place, then shrink/grow to the new grouping's result size. Disable old
   count actions during the request and ignore superseded grouping responses.
10. Searchable table dialogs freeze capacity from the unfiltered count, using
    `min(unfilteredCount, preferredPageSize, 25)` rows, with one message row for an
    empty unfiltered set. New visits uses the unfiltered bucket count/`totalVisits`,
    never filtered pagination totals. Loading, search, empty results and partial
    pages fill unused row space without moving the dialog/search line. Reserve
    the unfiltered pagination caption width so footer wrapping stays fixed.
    Retry feedback floats without adding layout height. Filtering resets internal
    vertical scroll to show the first matching rows, not the padding below them.
    Preserve the viewport containment limit; neither scope security nor result
    paging/count semantics change.
11. Incremental New visits searches retain the existing row/cell nodes and full
    opacity while debouncing/fetching. Admit only the latest compatible response,
    update values in place, and hide surplus rows for a partial result. Block
    old row actions without changing their text appearance; keep owner/date/
    bucket/metric boundaries and late-response fences intact. An accepted empty
    result has only the table header: no result/message rows. Center a slightly
    larger status message in the reserved body viewport, independent of horizontal
    scrolling. Initial loading is distinct from a zero result.

### Identity, history and qualification

1. Count named **canonical display identities** exactly as in Road Coverage with
   unnamed entities hidden, not OSM segments, source logical paths or raw GPS
   samples. Named road/path/park and named education identities follow the same
   display exclusions and canonical merging. One named park/path group or school
   spanning several source members must not become several visits per workout.
2. Use one canonical context resolved for the **full selected range** for the
   table and all drill-downs. Resolve its source membership using Road Coverage's
   historical rules; do not rebuild identities independently for each month/year.
   Report counts ignore Map's checked subset, focus and rendering/diagnostic mode.
3. Eligible visits have retained, applied Coverage evidence and satisfy the same
   workout visibility, route and local-date rules as Road Coverage. Pending-only
   and empty results contribute no visits. Retained stale evidence remains usable;
   a failed rematch does not discard the last successful applied visits.
4. Deduplicate each identity/workout before ordering its full eligible history by
   workout-local start date, workout start instant, then workout ID. The visit date
   is the workout's local start date, not import time, source-file date, GPS point
   time, or a user-time-zone conversion. Multiple same-date workouts remain visits,
   but only the earliest can qualify as a new visit on that date.
5. **New visits (all time)** counts the first-ever eligible visit to each identity
   if that visit falls within both the selected dates and the bucket. This is the
   identity's first attribution over all history, not its first visit in a bucket
   or merely its first visible visit after the range starts.
6. **New visits (past year)** includes those first-ever visits and any later visit
   whose immediately previous eligible visit to that identity is **more than one
   calendar year earlier**. Compare calendar dates using the previous visit plus
   one year. Clamp February 29 to February 28 in a non-leap anniversary year;
   exactly the anniversary does not qualify, but the next date does. Do not use
   a fixed 365-day threshold or measure from only the last qualifying visit.
7. Compute first/predecessor history **before filtering to selected dates or
   buckets**. Earlier out-of-range and intervening nonqualifying visits still
   disqualify repeats. Each column counts qualifying identity visits, not all
   visits to an identity that happened to qualify once in the bucket. Under the
   Month/Year grouping, an identity contributes at most once per bucket/column;
   the first qualifying event supplies the displayed date, never a later repeat.
8. Counts are derived from currently imported eligible history, not a promise of
   the user's first real-world visit. Historical backfills, logical deletion,
   corrected dates, canonical metadata changes or successful rematching can
   legitimately change first/predecessor classification and invalidate the report.

### Visit-detail dialogs

1. Use Road Coverage's modal/table/search treatment. Titles identify the metric
   and bucket, for example **New visits (all time) in November 2026** or
   **New visits (past year) in 2026**.
2. The all-time dialog columns are **Road/Path/Park**, **City/County/Region**,
   **New visit**. The past-year dialog adds **Previous visit** after **New visit**.
   Keep **New visit** consistently as the qualifying-event label; it must not use
   Road Coverage's Most recent visit field or a later repeat's date.
3. The default sort is **New visit descending**. All columns are sortable, with
   displayed name ascending, displayed place ascending and stable canonical ID
   as deterministic tie-breaks. Dates use Road Coverage's **Dec 20, 2025** format; missing previous
   dates sort last in either direction.
4. A first-ever visit's **Previous visit** displays grayed-out **n/a**, with the
   same styling as an unavailable Distance in Summary's Workout log. The API
   represents this as null; do not fabricate a date, zero, or a dash.
5. Preserve the Search bar and literal case-insensitive matching against displayed
   identity names and City/County/Region. Hide **Show unnamed roads and paths**:
   both metrics contain named entities only. Use preference-sized pagination.
   Do not show an additional "26 of 26 visits" caption below the table; the API's
   unsearched total still supplies the count/detail consistency check.
6. Make the dialog narrower than Road Coverage to fit three or four columns at
   approximately their existing individual widths; allow the variants to have
   different widths. Cap dimensions to the viewport and use heading size
   `clamp(1.4rem, 5vw, 2rem)`. Keep the table's top border visible above its sticky
   header. Retain shared ellipsis/overflow help. Keep rows mounted during paging,
   reserve full-page slots on the last page and preserve table/content-owner scroll;
   shorten the body only when the filtered total is smaller than a full page.
7. Provide the close control and Escape dismissal, trap modal focus, and restore
   focus to the invoking cell action on close. Count cells remain usable on touch
   and by keyboard even when the visible count is a single digit.
8. Both dialogs list exactly the qualifying events behind the clicked metric and
   partial bucket. Search and pagination only change the displayed subset. They
   must not retrieve the whole bucket outside the selected dates or substitute a
   different canonical context or newer data generation silently.
9. Identity names are whole-cell actions using Road Coverage's hand cursor and
   normal-text-to-orange hover treatment, without underlines or cell highlighting.
   Clicking a name closes the dialog, switches to Map production Coverage, fetches
   the exact canonical identity through the existing authorized Map capability,
   and fits/blinks its road/path/park geometry using the Road Coverage flow.
   Restore the previous Map camera during the handoff and suppress unrelated
   default/all-route fits. Once exact detail is available, use the same single
   direct fit/blink path as an identity clicked in Road Coverage. With no prior
   camera, wait for the exact bounds and initialize there without a world view or
   a second fit. Style-load replay cannot repeat an already-applied identity fit.
   Preserve the currently focused workout, checked route subset and effective
   base-map choice, including manual override and the last automatic family when
   no workout is focused. Identity highlighting adds temporary emphasis; it does
   not clear the catalog/focus or choose the default base map on tab reentry.
10. In-range New visit/Previous visit dates are similarly clickable and select the
    actual qualifying/predecessor workout on Map, focused, checked and centered in
    the list when possible. Use the returned workout IDs, not an identity's latest
    workout or a guessed workout for that date. Same-date ties choose the earliest
    new visit and immediate latest predecessor in deterministic history order.
11. **Out-of-range previous dates remain plain, noninteractive text**, even though
    their workout IDs are available. Do not expand or override the main range.
    All Stats-to-Map actions preserve that selection, report/table page and sort;
    returning to Stats restores them without reopening the dialog. An explicit
    Stats Coverage jump does not run diagnostics or persist a preference change.

### Validation and failure cases

- Apply shared date validation, allowlisted Month/Year grouping, sortable field
  validation and configured pagination limits. A requested detail bucket must
  intersect the resolved range, and the metric must be all-time or past-year.
- Authorize all report scopes and results to the signed-in data owner; administrator
  privileges do not expose private reports. Serve bounded results with explicit
  pagination/completeness metadata, never silent truncation or partial counts.
- Loading and retryable errors are visible in the report/dialog; failed or pending
  requests are not displayed as zero counts. No qualifying visits is a successful
  zero-filled bucket table, with no active drill-down cells.
- Range, grouping, account, generation or canonical-policy changes cancel obsolete
  requests and invalidate affected data. Late success/error responses cannot
  replace current results, reopen a dialog or change the selected report. Refresh
  an obsolete report scope before retrying a stale drill-down, so totals and rows
   are not mixed across snapshots.
- Visible Stats checks normally run every 30 seconds (configured interval clamped
  to 10-60 seconds); hidden tabs pause them. Report scopes last at most 30 minutes,
  clipped to the session deadline, independently of authentication validity.
  Renew expiring/stale scopes and retry transient preparation failures with bounded
  backoff, then retry on the normal visible cadence or focus/network restoration.
  Keep same-account/range rows mounted but disable stale actions during renewal;
  clear them on account/range/deletion-context changes. Grouping uses atomic
  in-place replacement with labels from the displayed response. Successful recovery
  clears the error without requiring a tab switch. Expired data scope is not logout.

### Acceptance criteria

- Switching Stats -> Map -> Stats restores New Coverage, its grouping, sorting
  and current page; the shared date range remains consistent across all views.
- For 3/25/2024 through 10/1/2026, a 3/24/2024 visit is excluded while visits on
  3/25/2024 and 10/1/2026 are included. All intersecting buckets still appear.
- An identity first visited on 11/3/2026 and again on 11/20/2026 contributes one
  to both November columns; both dialogs show New visit Nov 3, 2026, and the
  past-year dialog shows grayed-out n/a for Previous visit.
- Given visits on 4/10/2024, 4/10/2025 and 4/11/2026, only the first is all-time
  new. The second is not past-year new (exact anniversary); the third is, with
  Previous visit 4/10/2025, even if that date lies outside the selected range.
- After a 2/29/2024 visit, a 2/28/2025 visit alone does not qualify; a next visit
  on 3/1/2025 would qualify if no intervening visit occurred. An intervening
  2/28/2025 visit resets the predecessor, so a 3/1/2025 repeat then does not qualify.
- Canonical duplicates and repeated segments do not inflate counts; names merged
  by Road Coverage stay merged across the report and both drill-down variants.
- Every nonzero cell's complete unsearched detail total equals its count; the
  dialog supports name/place search, all-column sorting, preference-sized paging,
  compact width/height, Escape and return focus. Zero cells cannot open it.
- A range change, deletion, backfill or metadata/generation update cannot install
  stale counts/details; another account cannot read either report or detail scope.
- Both report/dialog pagination retain populated frames and fixed page-button
  positions through delayed requests and the last page. Heading typography,
  one-half width, equal title/control/table gaps, standard pagination spacing, pointer/hover treatment and
  the delayed two-line header tooltip match their shared UI contracts.
- Name/new/previous jumps use actual Map geometry or workout IDs, keep the main
  range unchanged and restore the report page. Out-of-range previous dates cannot
  navigate. Data-scope expiry followed by transient renewal failure recovers while
   the user remains on Stats, with no blank rows or authentication reset.

## Feature: Review Workout Details

### User story

As a user, I want to inspect one workout's available statistics without crowding the Summary table.

### Behavior

1. Selecting a workout row reveals a detail presentation appropriate to the viewport.
2. Mobile expands details beneath the three-column row. Build detail contents only when expanded, using the already-loaded complete record without an additional request; preserve the collapsed region's accessibility relationship.
3. Details include type, local start/end, duration, source-supported aggregates, route availability, and elevation values when applicable.
4. Missing optional statistics display as unavailable and are not represented as zero.
5. Row actions remain available from the detail presentation.

### Validation

- The server verifies account ownership for the selected workout.
- Units and time formatting follow current user preferences.

### Failure cases

- A workout removed in another session closes the detail presentation and refreshes Summary.

### Acceptance criteria

- Given a workout with optional metrics, when details open, then available values are shown in preferred units and absent values are marked unavailable.
- Given a mobile viewport, when the row is tapped again, then its details collapse.

## Feature: Use Workout Row Actions

The shared Workout log/Task history ellipsis popup shifts 1px left and 5px away
from its trigger: downward for bottom placement, upward for top placement.
Reserve corresponding top/bottom edge clearance and retain menu keyboard/focus behavior.

### User story

As a user, I want common workout actions available from each Summary row.

### Behavior

1. A right-aligned three-dot menu shows the actions listed below in fixed order.
2. Show on map switches to Map, checks and focuses that workout, and fits its extent while retaining other checked routes. It preserves the active mode when the target is usable there; if production Coverage is active and the target has no coverage result, it switches to raw Routes before fitting so the target is visible. A current or stale usable coverage result preserves Coverage mode.
3. View provenance shows the full chronological import-event history.
4. Delete workout asks for confirmation naming the workout date and type.
5. Summary provenance and deletion dialogs follow the shared compact-dialog
   treatment in the [UI style guide](ui-style-guide.md#dialogs-and-confirmation-flows).
   Use consistent content, divider and action insets; preserve initial focus,
   dismissal, pending-state guards and return focus.
6. Provenance event timestamps use the Workout log date's emphasis and minute
   precision, for example **Oct 30, 2026, 2:57 PM**, respecting the preferred
   time zone and clock format. Align their right edge with the close button,
   including when long history scrolls under the sticky dialog header.

| Order | Action | Availability |
|---|---|---|
| 1 | Show on map | Route exists |
| 2 | View provenance | Always |
| 3 | Export GeoJSON | Route exists |
| 4 | Export points | Route exists |
| 5 | Delete workout | Always |

### Validation

- Every action verifies account ownership on the server.
- Export and map actions reject workouts without routes.

### Failure cases

- A workout deleted in another session produces not found and refreshes the row.
- Export failure displays a safe error without creating a partial browser download.

### Acceptance criteria

- Given a routed workout, when Show on map is selected, then only that workout is highlighted in Routes mode.
- Given an unrouted workout, when its menu opens, then route-only actions are absent.
- Given a confirmed deletion, when accepted, then the row is optimistically removed and a deletion job is available.

## Feature: View Raw Routes

### User story

As a user, I want to compare the exact recorded paths of selected workouts.

### Behavior

1. The map fits selected route extents with configured padding.
2. Configured base-map style families each provide light and dark variants; the active variant follows the application theme.
3. With no focused workout, the initial automatic family is the configured fallback family, initially Alidade Smooth, regardless of which routes are checked.
4. Automatic family selection is sticky across checkbox visibility changes. Checking routes, unchecking the final route, or using the bulk checkbox never changes the active base map.
5. Workout-type mappings use provider labels normalized with the same Unicode and case-folding rules as ingest.
6. A user can select any configured family; that choice overrides automatic selection for the current Map visit but still follows theme changes. In automatic mode, focusing a workout by clicking its label/date area selects the family mapped to that workout type. Clearing focus retains that family until another workout is focused.
7. Leaving and reopening Map restores automatic family selection from the visible routed workouts.
8. A style or theme change preserves the camera, date range, workout filter, selected route, route order, hover state where still applicable, and private route overlay while only the public base-style resources change. Hovering over a route checkbox never activates route hover; only the label/date focus area and map geometry do.
9. Routes use easily distinguished colors by workout type.
10. Routes render oldest first and newest last.
11. Hovering near overlapping routes selects the topmost, most recent route.
12. The selected route is highlighted bright purple in full.
13. One compact Workouts list allows any subset within the period. The checkbox area changes only route/Coverage visibility and never changes hover, focus, base-map family, or camera position. Unchecking the focused workout clears focus. Clicking the remainder of a row makes the route visible, focuses and fits it in the viewport, and retains every other checked route.
14. Private route tiles require authenticated account access.
15. Changing route visibility replaces the immutable tile capability in the background without removing the map or public base style.
16. Hovering a route highlights the full route and matching visible list row. After the standard tooltip delay, an auto-sized two-line popup shows workout type and distance, then the start date and compact start/end times.
17. Workout-log sort column and direction survive Summary, Map, Stats and Data Sync tab changes for the current date range. Changing the range resets sorting to Date descending, and Map orders its workout-route list with the same active sort. Stats report sorting is separate and does not alter workout-log sorting.
18. Map preserves its mounted workspace, camera, base-map choice, checked routes and Routes or production Coverage mode across tab changes. When the diagnostics preference is already enabled, each new visit to Map starts in Routes mode so diagnostic review is never reopened implicitly. Changing that preference during an existing visit preserves the current mode: focused Coverage transitions directly between production Coverage and diagnostic review, retaining focus and camera. An explicit diagnostics preference change ends a Stats-origin production-only override.
18a. Closing Preferences by Save, Cancel, close button, Escape or outside dismissal returns focus to the active tab's content area after the dialog's close-focus handling. Map treats its canvas as the content area; a temporarily unavailable canvas uses the Map content focus handoff. Space immediately controls the map's existing hide behavior rather than reopening the profile menu.
18. A bulk checkbox directly above the workout checkboxes is checked only when all available workouts are checked. Clicking it when checked selects none; clicking it when unchecked, including for a partial selection, selects all.
19. Compact Routes and Coverage radio controls share the bulk-checkbox row and preserve the date range, workout subset, sort, base map and camera when the mode changes. Changing modes does not automatically refit, including when diagnostic results arrive.
19a. A normal bulk-checkbox click still toggles all routes. Holding the primary pointer for 500ms opens **Show workout types** without toggling routes; Arrow Down provides keyboard access. The panel's top edge aligns with the scrolling routes list's top edge and its left edge and width match the full Workout routes frame. Other dropdowns retain their 12px gap. The checkbox uses the shared styled tooltip, with larger text and three lines: `Toggle checkboxes to show or hide all routes;`, `click and hold to filter routes by workout types`, and `(use space and down keys in keyboard mode)`. The tooltip is linked to the checkbox, adds no extra tab stop, and hides while the picker is open. The popup includes only types in the complete current route list. A type starts checked only when every route of that type is checked. Compact **Cancel** and **Apply** actions use equal 12px top/bottom/right insets and a 12px gap, and discard or atomically apply the draft: all routes of checked types become checked, and all others become unchecked. Outside click and Escape dismiss without applying. Hiding the focused route clears its focus; changing the route catalog closes an obsolete popup.
20. The panel legend is Workouts. Coverage stats is always available beside Fit on map regardless of the active rendering mode.
21. When Summary accepts a workout deletion, Map removes that entry from its cached list and checked IDs, clears matching focus/hover/overlays, and renews the tile capability. Other checked routes, the date range and sort remain unchanged; a full workout-collection refresh is not required for a known deleted ID. Failed deletions retain Map state. Stale asynchronous results cannot restore removed entries, and new workouts imported on a deleted date remain eligible.

### Mode-Specific Route Fitting

- Each routed workout has independent raw GPS bounds (`bounds`) and nullable
  applied Coverage bounds (`coverageBounds`). Coverage bounds are precomputed
  from matched path and park evidence, respecting path-display exclusions and
  name qualification, rather than inferred from raw GPS or simplified tiles.
- A row/map focus action, restored focus, or single-workout deep link fits the
  active mode's bounds. Switching modes preserves the current camera; explicit
  fitting uses the persistent focused workout, not a transient hover preview.
  Fitting does not wait for exact geometry downloads
  or tile completion, and does not replace the tile capability.
- **Fit routes** uses the union of the checked workouts' bounds for the active
  mode. The diagnostic overlay retains its separate diagnostic-geometry fit.
- Empty/no-evidence Coverage has null bounds: Coverage fitting is disabled or
  skipped rather than substituting raw bounds. A retained stale applied result
  continues to use the bounds of that retained geometry until replacement commits.
- A completed result with no displayable matched geometry is durably classified
  as `no_matches` and exposed as `coverageReadiness.resultStatus: no_matches`.
  In production Coverage its row action is disabled, uses the neutral gray circle/X
  rather than a green checkmark, and shows **No map segments matched** in the
  shared styled tooltip. It cannot start hover/focus geometry requests or retain
  a misleading focused-row highlight. The raw route remains selectable in Routes;
  a single-workout Show on map request falls back to Routes for an empty result.
- First-time empty evaluations must record this status even when they write no
  match rows. Later successful matching changes it to `matched`; removing all
  displayable evidence changes it back to `no_matches`. Failed or cancelled
  replacements retain the previous applied result and classification. These are
  display-result statuses: an empty evaluation is still a successfully completed
  job and does not cause an automatic retry solely for being empty. Unprocessed,
  unavailable-map and failed-job tooltips retain their distinct existing meanings.
- Match replacement/removal and relevant source metadata changes maintain
  Coverage bounds atomically with the existing deferred display projections.
  Bounds span the workout's displayable path/park evidence; date-range canonical
  context can omit some of those pieces, but must never expose geometry outside
  the stored extent. Raw geometry, matching policy, and original observations are
  unchanged by bounds maintenance.
- Existing computed Coverage receives a one-time all-account bounds backfill;
  no source reimport or Coverage rematch is required.

### Space-Key Layer Visibility

With a workout focused and keyboard focus on the map, holding Space temporarily
isolates the relevant route or Coverage evidence. Releasing Space restores the
hidden layers; losing window focus also restores them. The shortcut does not
intercept typing or activation in inputs, buttons, links, or other editable
controls, and does not change the camera, selected workouts, or tile capability.

| Mode | Fades out while Space is held; fades in on release | Stays visible |
| --- | --- | --- |
| Routes | Non-focused raw routes, including dots representing zero-length segments | Focused magenta route, directional markers, and start/finish markers |
| Production Coverage | Non-focused coverage layers, including park coverage | Focused coverage and the current coverage highlight |
| Diagnostic Coverage | Raw route, directional markers, and start/finish markers | Diagnostic evidence overlay, including selected traversal evidence |

Zero-length route segments have no drawable line length, so they render as small
dots in the workout-type color. These dots are not markers for every GPS sample
and are distinct from directional triangles and start/finish markers.

Both fade directions take **400ms**, with no delay. Layers fade through paint
opacity rather than being removed or immediately setting layout visibility to
`none`. Directional-marker opacity transitions are enabled only in diagnostic
Coverage mode; Routes mode explicitly uses a zero-duration transition, including
after switching back from diagnostics, so its markers do not fade. In Routes
mode, delayed hover can temporarily take precedence over persistent focus for
the magenta route and its markers; Space does not hide that highlighted overlay.

Camera transitions have a separate readiness handoff: direction arrows hide
immediately, without a fade, during programmatic pan/zoom animations. They are spaced for the
final viewport and revealed only after the current marker data and dedicated
focus source are ready, independently of background tiles in source 031. This
prevents dense old-zoom arrows flashing during route focus changes.
Route lines and start/finish markers retain their normal rendering sequence;
they are not held back by direction-marker readiness.

### Tile And Focus Responsiveness (031)

- Background tiles process local projected chunks, with sub-half-CSS-pixel display
  error below zoom 15 and full piece geometry at higher zooms. Separate workouts,
  stationary dots, timestamp components, partial Coverage evidence and real gaps
  remain distinct; source GPS/export data is unchanged.
- Coverage tiles contain only `coverage` and `coverage_parks`. They deduplicate
  repeated local evidence by canonical and source identity before union, without
  changing whole-inclusive-date-range counts or rendering full unvisited OSM paths.
  Exact focused Coverage remains a separate, unchanged GeoJSON request.
- Raw focus lines/dots use the existing segmented raw-point request shared by
  markers. Focus-only changes do not renew the background capability. Focus and
  arrows do not wait for background tiles; a user pan with pending tiles retains the
  current line and ready arrows without repaint loops.
- Raw-route and Coverage-focus caches are separate **16-entry / 8 MiB serialized
  JSON** LRUs, each expiring five minutes from load, never extended by hits.
  Generation/context/deletion/cancellation guards prevent stale geometry from
  becoming another workout's focus or obsolete 404s from refreshing a selection.
  Focused Coverage hover fallback keeps canonical IDs and source kind.
- Every tile response, including validation errors, is `private, no-store` with
  `Vary: Cookie, Authorization, Accept-Encoding`. Bounded server payload reuse/coalescing never
  caches authorization; revoked, expired or stale scope is checked before lookup
  and again after waiting. Overload returns bounded retryable unavailability,
  not an unbounded render queue. Martin caching remains disabled.

### Tile Delivery (031)

**Implemented and deployed at Helm 67; scoped desktop/4K completeness verified,
not fast dense high-zoom raw completion.**
The [current rendering contract](architecture.md#tile-rendering-and-delivery-031)
retains public tile routes and all focus/count semantics. Complete raw components
are simplified before chunking at low zoom, with original geometry retained for
closed moving loops that would collapse; movement never becomes a stationary dot.
At zoom 15 and above the original exact 031 raw chunks are used. Coverage performs
display-precision deduplication without claiming unvisited OSM evidence.

The browser uses bounded per-tile scheduling, not a two-reload source-wide budget:
**4 active / 128 jobs / 64 waiters**, **60s initial queue** then **15s service/retry
time from first dispatch**, at most three attempts. Cancellation and generation/
style/unmount guards prevent obsolete tiles or retries taking over a new view.
401/404 stop scope dispatch with one callback; internal 410 protects against
MapLibre treating a 404 as successful empty content. The public API still returns
404 for unavailable selection scope. Legitimate empty tiles remain valid.

Negotiated streaming gzip applies only to successful sufficiently large tiles,
with bounded encoder admission and final authorization afterward. Server limits
and private/no-store policy remain unchanged. A loaded-tile indicator alone is
not proof of complete rendering: MapLibre can count failed tiles as loaded.
[Verification](coverage-acceptance-results.md#tile-delivery-status-031) keeps
the scoped error-free completeness result separate from slow dense raw completion,
compression-latency improvement and time-to-usable-render/capacity acceptance.

### Validation

- A map selection belongs to one session and account.
- Foreign, expired, or invalid selections are rejected.
- With new folded 028, selected data and generation use one Repeatable Read snapshot
  without a generation-row lock. A concurrent commit may make even a newly
  returned selection stale; existing current-generation validation rejects it
  instead of authorizing old selected data with a new generation. Focused-workout
  membership and account ownership constraints remain enforced.
- Tile requests cannot provide arbitrary account IDs.
- Base-map configuration rejects duplicate family IDs, missing theme variants, an unknown fallback or mapped family, unsafe URLs, and invalid attribution.
- Optional focus must belong to the created selection. Deleted, foreign, nonrouted or out-of-range focus returns not found, not temporary unavailability. Reauthentication into an unavailable focused route clears that focus and loads the current range with bounded recovery, preserving explicit checked-route scope and user preferences.
- Public provider credentials and resources are separate from private tile authorization.

### Feature: Retain Tab Workspaces

1. Summary, Map, Stats and Data Sync initialize lazily on first visit and retain
   their mounted UI resources for the current signed-in session. Switching tabs
   hides the old workspace rather than destroying it. The first Map visit still
   initializes its renderer; ordinary returns reuse its canvas, workers, loaded
   provider resources, sources and applicable caches.
2. Only the active workspace is visible, accessible or keyboard-interactive.
   Hidden tabs cannot handle Space/application commands, expose floating help or
   dialogs, redirect navigation, steal focus or change the visible tab's scroll.
   Polling and provider retry loops pause while inactive. Existing in-flight
   reads may finish under their normal scope/abort guards.
3. Retain tab controls, pages, compatible table DOM, disclosures and document/
   internal scroll. Focus returns to the active content area, with Map using its
   canvas. Only real size/pixel-ratio changes resize the retained map. Returning
   to an unchanged workspace does not re-fit the camera or replay prior intents.
4. Retention does not extend authorization or data-scope validity. Renew expired
   Map/report capabilities on return, revalidate sufficiently old Map scopes in
   the background, and preserve unchanged sources when generation/display context
   and membership remain valid. Fence report actions while refresh is pending.
   Date/time-zone changes and accepted deletions hide obsolete private pixels
   until the replacement scope is admitted; late responses cannot restore them.
5. Explicit workout and entity navigation into an already-mounted Map still
   focuses/fits the requested target, admits it to an explicit checked subset
   where required, and scrolls its list row. Keep existing range policies,
   readiness-based raw fallback and the explicit-entry diagnostic rule.
6. Confirmed logout, expiration and session/identity replacement dispose all
   retained workspaces and release the renderer and private caches. Tab retention
   is not persisted across browser reloads or between accounts.

### Map Layer Stack And Opacity

Use the following map-rendered order **from bottom to top**, retaining current
line widths, colors and geometry. Normal visible path geometry uses **90%
per-layer opacity** in every mode and focus state; overlapping paths can still
accumulate opacity. Existing provider labels are rendered once, above paths and directional arrows,
without an additional canvas or offscreen compositing pass. Preserve provider
label filters, zoom visibility and relative ordering. Visible road-name/shield
text and symbols use 100% paint opacity, subject to the renderer's normal symbol
placement/fade behavior.

| Order | Layer/group | Applicable modes | State / opacity |
|---|---|---|---|
| 1 — bottom | Provider map geometry: imagery, terrain/hillshade, roads, land, water and buildings | Routes, production Coverage, Diagnostic | Original provider geometry order and styling |
| 2 | Non-focused raw paths and stationary dots | Routes; temporary raw-tile handoff in Diagnostic | 90% while visible; 0% when hidden by the established visibility/Space behavior |
| 2 | Aggregate road/path Coverage and park-boundary Coverage | Production Coverage | 90% while visible; 0% when non-focused Coverage is hidden |
| 3 | Focused/hovered magenta raw paths and stationary dots | Routes | 90% when the focused/hover geometry is ready |
| 3 | Focused road/path and park-boundary Coverage | Production Coverage | 90%, above aggregate Coverage |
| 3 | Diagnostic focused raw path, matched/ambiguous evidence and selected/hovered evidence portion | Diagnostic | 90%; selected evidence remains above other focused path geometry |
| 3 — interaction only | Diagnostic wide hit-target path | Diagnostic | Always 0%; retains pointer lookup without visible paint |
| 4 | Directional arrows, directly above the focused path group | Routes and Diagnostic when focused route geometry/camera are ready | 95% while visible; existing hide/readiness transitions remain |
| 5 | Existing provider road names/shields and other base-map symbol/label layers | All modes | Above directional arrows; road names/shields at 100%; other symbols retain provider styling/visibility |
| 6 | Finish marker and start marker | Routes and Diagnostic when endpoint geometry is available/visible | 100%; preserve existing relative finish/start order for coincident endpoints |
| 7 — top | Temporary white road/path/park selection blink | Production Coverage, while an explicit identity highlight is active | Attention exception: pulse between 100% and 0%, above labels, arrows and endpoints |

HTML controls, panels, attribution and styled tooltips/popups are separate DOM
overlays and retain their established order above the map canvas. Reapply the
map policy after style/theme/family changes and private-layer recreation. Avoid
reordering already-correct layers on ordinary camera frames.

### Route Continuity And Display Membership (009/010)

Raw routes retain their original GPS observations, but do not infer a continuous
line between consecutive samples more than **200 metres** apart. This matches
Coverage's existing spatial hard-gap limit. Apply the split after the existing
timestamp-cadence segmentation, preserving every prior temporal component and
every non-gap edge. Do not join the samples on either side of an isolated outlier.
Singleton fragments cannot form a line; repeated-coordinate stationary runs stay
visible. Build derived geometry during ingest before exact/LOD chunk maintenance,
and use the same limit for focused raw lines and direction markers.

The shared `raw-route-display-v3` membership additionally excludes both endpoints
of a within-component positive-time edge above **12.5 m/s (45 km/h)** for the
explicit Walk/Walking/Outdoor Walk/Indoor Walk and Run/Running/Outdoor Run/Indoor
Run provider labels, compared case-insensitively. Other movement labels retain
their existing policy. This check uses measured geographic displacement and
elapsed time, not the provider's speed or accuracy estimate. Preserve the original
cadence baseline and existing time/spatial breaks. Never join across excluded
samples; discard isolated surviving fragments rather than inventing point dots.
Valid repeated-coordinate stationary runs remain visible.

Speed rejection alone can leave slower remnants of one failed GPS episode.
For walking/running, an impossible-speed seed plus deteriorated accuracy can
activate bounded recovery filtering. Establish the reference from at least eight
usable, non-rejected accuracy fixes among the preceding 32, no more than 300
seconds before the seed. The recovery accuracy ceiling is the greater of 3 m
and twice the best reference accuracy. Include the degraded lead-in when a recent
stable window identifies it. Require ten consecutive reliable fixes with positive
time deltas and plausible recovery motion (up to 3 m/s walking or 6 m/s running)
before retaining the recovery window. Recording gaps do not independently end
the episode. Limit lookahead to 600 fixes and 300 seconds since the latest
impossible-speed seed. Without a confirmed recovery inside those limits, retain
the endpoint-only rule. These recovery limits do not globally cap valid movement;
poor accuracy alone, legitimate fast running and other activity types are not
grounds for this additional rejection.

One owner-scoped database helper selects retained sequences and components for
ingest-derived geometry and both exports. Focused raw rendering honors those
authoritative components, including when the remaining timestamps would otherwise
look continuous. Start/finish and arrows use the same filtered points.

Existing workouts require an explicitly authorized, account-scoped scan and
guarded derived-data repair, not deletion of provider input or an automatic
global migration rewrite. Preserve workout totals, original points and valid
Coverage evidence. Coverage already rejects spatial gaps and retains its
additional time rules. See [ADR 0015](decisions/015-route-continuity-and-preserved-observations.md).

### Failure cases

- No routed workouts leaves the base map interactive and centers the browser's current location when permission and location are available; otherwise it fits the contiguous United States.
- A partial source route renders as recorded and is identified as partial where metadata permits.
- Failure to load a public base-map style does not expose private tile credentials or remove the user's route selection.

### Acceptance criteria

- Given overlapping routes, when the overlap is hovered, then the most recent visible workout is selected.
- Given a changed workout subset, when the map redraws, then only selected workouts appear.
- Given visible routed workouts mapped to one family, when Map opens, then that family uses the variant matching the application theme.
- Given mixed or unmapped visible workout types, when automatic style selection runs, then the configured Smooth fallback is used.
- Given a manual family selection, when the theme changes, then the corresponding family variant replaces the base map without resetting map or route state or reloading private route tiles.
- Given a manual family selection, when Map is left and reopened, then workout-type automatic selection resumes.
- Given an automatic family selection, when a workout row is clicked, then its mapped family becomes active and its route is fitted without hiding other checked routes.
- Given route visibility changes, when a replacement selection is prepared, then the existing map remains visible and interactive until the new private tile capability is installed in place.
- Given a route hover sustained for 750 milliseconds, when popup metadata is available, then the map shows type and distance on line one and compact start/end/duration on line two.

## Feature: View Road Coverage

### User story

As a user, I want to see which roads, trails, and other paths I have visited and how often.

### Behavior

1. Coverage generates nearby eligible segment candidates for ordered route points and sequence-decodes connected traversals; it does not independently attribute each point to its nearest segment.
2. All relevant path classes are candidates, including roads, trails, cycleways, unnamed paths, driveways, and parking aisles under the accepted matcher policy.
3. Deterministic OSM-derived segments remain the match-evidence and rendered-geometry units; they are not the user-facing counting or table-row identity.
4. Source logical paths group graph-connected named segments by normalized name within one attribution scope, including road/path transitions, branches, and source-way transitions. Named road segments within one scope/name also connect within 15 meters, extended to 50 meters only when both are explicitly one-way, so divided carriageways remain one attribution. The scope is the containing named park when attributed, otherwise authoritative municipality, otherwise county, otherwise provider region. In a synthetic example, Example Boulevard in Example Borough and in Sample Borough remain separate source paths. Source 030 adds display grouping over these unchanged source identities, as specified below.
5. Segment-to-locality assignment uses imported administrative boundaries and deterministic geometry rules. Segment derivation splits at locality boundaries where needed rather than assigning a cross-boundary geometry wholly to the wrong locality.
5a. The same source-ordered clipping splits roads/paths at qualifying park boundaries and formally unnamed roads/paths at qualifying education boundaries before attribution. Inside pieces receive the containing-area attribution; outside pieces retain normal identity. A partially overlapping original segment must not cause its entire inside portion to be discarded or its outside portion to be attributed. Holes, repeated crossings, original geometry and graph connectivity are preserved. Validation rejects material unsplit park/education crossings even when no attribution tag was assigned.
6. Connected unnamed segments share one logical identity within the same attribution scope and road/path class across simple two-segment continuations, even when source lineage changes. At a larger branch, one source way remains connected and the only two incident segments of one exact broad class may continue across source ways; other cross-source branches remain separate. Disconnected components always receive separate identities, including when they shared an incoming legacy ID. Driveway, parking-aisle, and parking-area logical paths remain rendered but are omitted from user-facing statistics/history.
6a. A non-road municipality piece of at most 25 meters is treated as a municipal-boundary sliver and reassigned to county scope only when the immediately adjacent pieces of the same clipped source segment both belong to that county and preserve the same name state and exact broad class. Park-attributed pieces and road pieces never use this exception.
7. Durable segment evidence contains at most one match per workout and physical segment. Overlapping or contiguous decoded traversal spans are dissolved regardless of direction, so repeated passes and GPS jitter neither create duplicate matches nor inflate covered length.
8. Genuinely disjoint visited spans on one physical segment remain separate components of that match's `MultiLineString`; persistence never fills the untraversed gap between them.
9. One logical path receives at most one attribution from a workout, even when the workout matches several member segments or traverses them repeatedly.
9a. A qualifying named local park receives at most one visit attribution from a workout when any matched segment is fully contained in that park and an authoritative named municipality. A qualifying named state or national park may receive the same attribution without a municipality and remains one region/source-scoped entity across municipal boundaries. State and national parks take precedence when park polygons overlap because they provide the regional context. The selected park is a separate connectivity scope, so contained paths never bridge to outside paths. Unnamed local-park geometry belongs only to the park in user-facing tiles and statistics; unnamed state- and national-park roads and paths retain their individual path identities in addition to the park visit.
9b. Named school, college, university, and generic education-ground polygons may attribute fully contained roads/paths only when the segment has no formal OSM name. The smallest containing education area wins. Education scope outranks park scope for those unnamed segments and exposes one educational-ground identity, visit history, bounds, and aggregate geometry keyed by `education_id`, while the municipality/county remains geographic context. Connectivity components and road/path classes do not create duplicate school rows. Formally named campus roads/paths retain their source name and normal path identity.
10. The path attribution retains the earliest positive-length decoded member-segment traversal from that workout. Point projections and detailed traversals remain available in diagnostics, raw routes support rematching, and dissolved segment evidence supports durable rendering.
11. Only decoded spans traversed by at least one checked workout render as covered; unvisited spans of a matched segment or logical path do not render as visited.
12. Every rendered entity uses its exact date-range distinct-workout count and bucket, independent of the checked subset. Coverage ignores workout type for coloring.
13. Fixed amber-orange-yellow count buckets represent ordinary road/path/park coverage; the focused workout's exact attributed geometry uses the corresponding purple-magenta-pink bucket.
14. A 750-millisecond map hover shows exact date-range count, earliest and most recent date-range visits, and first and last all-time visits. The popup uses numeric `M/D/YYYY` dates.
14a. After **250ms of stable hover**, an uncached entity requests `GET /api/map-selections/{mapSelectionId}/coverage/{entityKind}/{entityId}/stats` with the current `generation`. The popup remains hidden until both the full **750ms visibility dwell** and the lookup complete. It returns `RoadCoverageEntity` identity, counts, dates, navigation workout IDs and raw bounds, without geometry or expanded fit bounds. It does not fit, blink or install a geometry highlight; clicking an entity still uses the existing full-detail endpoint. The server adds no dwell delay. Range statistics include all eligible workouts in the selection's inclusive dates, independently of the checked subset or focused workout; all-time statistics include eligible account history.
14b. The canvas caches successful hover stats immediately on response, separately from Road Coverage table results: at most **128 least-recently-used entries**, expiring **five minutes after load**, scoped by selection ID, generation, display policy/revision, resolved dates, entity kind and ID. Cache hits still wait the full **750ms dwell** and do not extend expiry. Changing entities, leaving the canvas, window blur or a hidden document cancels pending popup visibility and removes the popup, but an already-started lookup can finish and populate the same-context cache. Returning to an entity reuses its cached result or in-flight lookup without another request. Inactive hovers cannot retry, recover a selection or restore a popup. Selection/generation/policy/revision/range changes clear the cache and abort obsolete lookups; late responses cannot affect a replacement context. Unmount aborts lookups, clears the cache and removes all listeners. Failed/aborted lookups are not cached. The existing response to server 503 remains one client retry after **250ms**; its retry budget is otherwise unchanged.
15. Road Coverage rows show the same four dates and retain the earliest workout on a tied date as their navigation target.
16. Unnamed entities display a type-specific fallback while retaining locality and stable identity: **Unnamed road**, **Bike path**, **Pedestrian path**, **Trail**, or **Unnamed path**. Road Coverage labels its context column **City/County/Region**. A road or path attributed to a state or national park uses that park as its display context ahead of an administrative locality; otherwise it uses municipality, county, then the authoritative user-facing region label such as `Northern California`, never a provider-qualified key such as `geofabrik:norcal`.
17. Coverage mode shows a compact Road coverage legend with **Fit** and **Coverage by road...** actions. Fit is disabled without a focused workout; otherwise it retains the legend, fits that workout's coverage, and returns focus to the map canvas. The legend is hidden while its modal dialog is open.
18. The dialog loads the complete date-range result with one bounded `bulk=true` request, not a server-page loop, then provides unchanged client-side literal case-insensitive search, sortable columns, and preference-sized pagination for roads, paths, and parks. Search matches only the displayed Road/Path/Park and City/County/Region text, uses the browser-native search cancel control, and suppresses autofill background changes. Equal primary sort values use displayed Road/Path/Park name ascending, then displayed City/County/Region ascending, before an invisible stable identity tie-break. The shell caches that complete result for five minutes by resolved date range and account data generation, so tab changes, capability replacement, entity navigation, and ordinary reopen do not reload unchanged data; same-range refresh preserves search, unnamed visibility, sort, and a clamped current page. A resolved date-range change clears search, unchecks unnamed visibility, and returns to page 1. **Show unnamed roads and paths** defaults off; when enabled, type-specific fallback names participate in search. Pagination displays at least `Page 1 of 1`. A load longer than half a second shows `Loading coverage...` in the framed table body; a failed load replaces the table dialog with a compact Retry/Close error dialog.
18a. Bulk delivery evaluates coverage once and is limited to **50,000 entities and 64 MiB of uncompressed JSON including metadata**. Exceeding either bound returns **422 Problem Details**, field `bulk`, code `limit`, with no partial results. The dialog asks the user to choose a smaller date range and never caches an oversized, failed, aborted or incomplete response. A complete bulk response reports page 1, page size 50,000, total items equal to the returned item count, and one total page (zero for an empty result). Valid page/pageSize inputs do not slice bulk results; server search/sort remain supported. Non-bulk API clients retain ordinary pagination capped at 100 rows.
18b. Every column value stays within its padded content width. Overflow uses an ellipsis; only actually truncated values expose the complete text through the shared styled, single-line tooltip. The tooltip is portaled above the modal dialog, not clipped by the scrolling table or hidden below its overlay/content. Resizing, font loading and changed text update truncation without replacing focused buttons. No native HTML title duplicates this help.
18c. Only one portaled tooltip owns an active hover/focus at a time. Wheel scrolling rechecks the element under the pointer: a disabled route row that moves away loses its tooltip before another row activates one. Merely repositioning or resizing a superseded tooltip cannot reopen it. Keyboard focus remains intact, while clipped or departed anchors do not leave floating help behind.
18d. Freeze table capacity from the complete **unfiltered** result set, including unnamed roads and paths even when their display checkbox is off. Use up to `min(preferredPageSize,25)` rows; a short unfiltered set can establish a shorter initial dialog. Search, unnamed visibility, zero matches and partial pages retain blank row space and cannot shrink/recenter the dialog or move its input. Reopening with a saved filter/page derives size from the full cached membership, so clearing that filter does not enlarge it. Reserve initial loading capacity until membership is known; if the user has already focused/edited the search or changed controls, retain that reservation for the open session. Keep the footer present and reserve its unfiltered caption width too. Smaller viewports retain a scrollable table with reachable heading, search and footer controls.
18e. Reserve viewport height independently of result rows. A zero-result Road Coverage or New visits table retains its header and an empty body, with slightly larger muted status text centered in the visible empty viewport. Do not render the message as the first row or add spacer result rows. Unused slots in a nonempty partial result are hidden. Viewport measurement uses untransformed layout metrics so dialog entry animations cannot alter its reserved size.
19. Closing Road Coverage reveals the existing mounted map. The X button or Escape dismisses it.
20. Clicking an entity name closes the dialog, fits its date-range geometry with a minimum 500-meter extent, and starts blinking it white immediately. The three-second highlight duration begins only after camera movement ends and MapLibre reports all requested base/Coverage tiles loaded; render and source events drive this check so the blink itself cannot prevent readiness, and a bounded fallback handles failed tile sources. Clicking an eligible visit date closes the dialog, selects and centers that workout in Coverage mode, and scrolls its route row into view.
20a. Identity fits use one direct camera transition without a flight-arc zoom-out.
The same exact-detail/fit/blink implementation handles Stats and Road Coverage
clicks. The signed-in frame retains center, zoom, bearing and pitch across Map
unmounts, captures the actual current camera on movement/exit and clears it with
private frame state at session/identity replacement. Explicit target navigation
does not start a default/geolocation or all-routes camera fit before detail arrives.
21. Fit on map replaces Fit routes and fits raw-route bounds in Routes mode or covered member-segment bounds in Coverage mode.
22. Preferences contains a **Diagnostics** section with an **Enable coverage diagnostics** checkbox, disabled by default.
23. With coverage diagnostics disabled, Coverage hides raw GPS route layers, displays durable checked-route coverage, and overlays only the focused workout's attributed coverage.
24. With coverage diagnostics enabled, the Coverage button retains the route-review workflow: it computes and displays diagnostic evidence for only the selected route and exposes the diagnostic labeling controls.
25. Ingested workouts and raw routes become available without waiting for asynchronous coverage matching. Production Coverage uses current durable results, identifies checked routes omitted as pending, failed, or unavailable, and refreshes after a committed coverage generation rather than running the matcher from a tile or statistics request.
25a. In production Coverage mode, each workout row replaces its workout-type color dash with an accessible status indicator. Current uses a success check, stale uses an amber clock icon, pending uses an activity ring, failed uses an exclamation mark, unavailable uses an x, and unprocessed uses a dash. A workout with no applied result is de-emphasized and cannot receive Coverage focus, but its checkbox remains available. After the standard tooltip delay, its disabled row action explains whether map data is unavailable, map data or Coverage processing is pending, or processing failed. These row indicators identify omitted checked workouts; a separate omission summary is not required. Checked transient no-result workouts are polled every ten seconds without showing foreground loading state; failed and unavailable no-result workouts alone do not keep polling active. Once every checked workout has a current or stale applied result, ten-second processing polling stops. While applied results are displayed, background generation checks continue at the public polling interval clamped to 10-60 seconds (normally 30 seconds), so committed replacements renew private capabilities even when readiness labels are unchanged. Both checks pause in hidden tabs and stop outside production Coverage mode. Unchanged capabilities are discarded without replacing map sources or resetting focus/camera state.
26. A failed, cancelled, timed-out, or stale rematch leaves prior valid coverage visible until a current replacement commits.
27. Coverage processing creates bounded account-owned parent runs with one child per eligible workout route. New ingest cohorts may mix regions, with per-route vectors authoritative. One child failure does not stop sibling routes; mixed success and failure makes the parent partially successful. See [committed-route pipelining](#feature-pipeline-committed-routes-to-coverage).
28. Display precomputations preserve exact results for named periods, all-time history and arbitrary custom dates without separate year/month datasets. Compact qualification partials are deduplicated by display entity/workout before counts; educational grounds spanning source paths still count once. Regional-park context and park bounds remain independent of path exclusions. Metadata, workout-local dates, visibility and applied-result eligibility remain live joins. Logical deletion hides a workout before physical cleanup; failed rematching preserves prior applied coverage. Deferred, transaction-batched maintenance commits evidence and projections atomically. See the [030 compact-fact design](architecture.md#coverage-display-precomputations-requires-30). The same combined migration defines final canonical grouping and cache scope below; matcher policy is unchanged. Earlier backend-42/UI-43 browser receipts remain historical, not fresh squash or current acceptance evidence. See [current status](development-progress.md#current-status) and [Milestone 8 closeout](development-progress.md#milestone-8-closeout).

### Background Coverage Reconciliation

**Deployed at Helm 62; initialization and bounded live unchanged-cycle acceptance PASS.**
[ADR 0014](decisions/014-metadata-driven-coverage-reconciliation.md)
defines the source-029 behavior, shipped with complete APP runtime 30 and
separate OSM 010. This refines the folded-028 fingerprint rule in
[pipeline behavior 6](#feature-pipeline-committed-routes-to-coverage), not its
historical deployment receipt.

1. Unchanged route input, catalog selection and effective matching target require
   no GPS scan/hash, region lookup, state rewrite or new Coverage job. The default
   30-second reconciler idle poll checks only catalog-head/claim metadata. A daily
   24-hour interval no longer triggers historical work. Executor/health control
   queries remain; this is not zero database traffic or UI 10-second job polling.
2. New or changed canonical route input triggers exact-route proof repair and
   eligible matching, including telemetry edits with unchanged point count/bounds.
   Catalog changes trigger metadata-filtered campaigns. Generation-only changes
   reuse exact selected IDs without GPS only when complete selection proof/policy
   remain equal; competing/unconfigured boundaries or configured-active presence
   changes invalidate selection and require full-route resolution.
3. Rules, sampling, path policy, minimum traversal and production matcher contract
   are an immutable complete job descriptor. Compare actual successful applied
   provenance, not preparation/enqueue or a later setting, before declaring a
   target current. A changed descriptor can reuse valid selection without GPS.
4. Persist real pending/unavailable selection proof so unchanged negative results
   do not repeatedly scan. Unknown history may need a bounded initial bootstrap;
   never infer an old negative catalog baseline from today's catalog. Exact retained
   successful immutable job history may prove applied work, not negative selection.
5. Interrupted ingest recovers committed handoffs and prepared tails through
   exact-account queue-only claims. Partial sealing still requires the visible
   root/all source siblings terminal and due preparation exhausted. Queue recovery
   preserves campaign cursors and causes no ordinary follow-up scan. It neither
   revives cancelled identical targets nor bypasses established retry budgets.
6. Lagged replica metadata, lost leases and changed input/descriptor CAS retain
   durable obligations without applying stale results or advancing progress.
   Acceptance combines absence of redundant work beyond the former daily interval
   with application-attributed CPU/query evidence after necessary work drains;
   no arbitrary five-minute settlement guarantee applies while updates are queued.

The [release record](development-progress.md#metadata-driven-coverage-reconciliation)
retains the revision-61 cutover and revision-62 bounded unchanged-cycle receipt.
Current live is **APP 31/minimum 31, OSM 10/minimum 10, Helm 67**, retaining 031.
Idle reconciliation acceptance is not map-scaling capacity proof.

### Canonical Display Identity (030)

1. A named displayed entity is unique by region plus normalized actual DISPLAY
   name and DISPLAY place. Hidden jurisdiction IDs, source kind/class, disconnected
   components and source UUIDs must not split equal named display keys. Preserve
   directions, accents, punctuation and suffixes. Normalize NFC/casefold/NFC under
   PostgreSQL 18 UTF8 built-in `pg_c_utf8`, with explicitly enumerated Unicode
   White_Space collapse/trim, not browser/session-locale rules. Versioned
   length-prefixed UTF-8 SHA-256 UUIDv8 identities retain normalized key fields and
   reject inconsistent collisions. Unnamed/blank sources retain kind/UUID
   discrimination; fallback labels must never merge unrelated unnamed entities.
2. Resolve membership using the selected date context over **all historically
   eligible sources**. Keep all-history names/base locality, in-range park-context
   precedence and current-range region minimum. A source with no in-range visit
   uses historical context/region fallback. When its key matches, include its
   entire eligible history. Union distinct workouts over all members before range
   and all-time summaries; return only groups with an in-range visit. First/latest
   navigation uses the date extremum, earliest start, then UUID. The same resolved
   key keeps its ID across ranges, but changed labels may change membership.
3. A named park and path with the same key form one group. Park-preferred
   representative kind/class is presentation only; retain source provenance.
   Table, hover, detail, focus and background tile layers agree on canonical ID,
   metadata, range count and bucket. Checked/focused workouts change rendered
   pieces only, never membership/statistics. Detail unions all actual in-range
   member geometry; do not invent connections across unvisited gaps.
4. Selection creation prepares a complete ready context scoped by account,
   generation, revision, dates, policy and locale. Required selection fields
   `coverageDisplayPolicy` and `coverageDisplayRevision` participate in bulk,
   hover and detail cache validity and stale-response guards. Read-only requests
   never rebuild. Keep eight reusable unreferenced server contexts as a soft
   limit plus all valid actively referenced contexts; deletion and rollback are
   atomic. Existing local filtering/sorting/paging, dwell, retry and click feedback
   remain. Server search applies after canonical grouping.
5. Every source input, including context-only evidence, metadata, dates/deletion,
   route/applied eligibility and region labels, defers invalidation and coalesces
   one revision/public-generation advance per affected account/transaction.
   Preserve both READ COMMITTED commit orders and stale Repeatable Read rejection,
   with no early ingest generation lock or eager rebuild of every date range.
   Coverage recomputation means warming/rebuilding this read model from existing
   evidence, not new route jobs. There is no OSM derivation, matcher or path-policy
   bump and **no OSM regeneration or rematching is needed**.

### Click-Highlight Follow-Up

**Implemented and deployed:** the following refines entity-name clicking in
behavior 20, retained by canonical display source 030. Historical backend-42/UI-43
receipts retain their checkpoint scope; current verification and
acceptance status belongs in [Milestone 8 closeout](development-progress.md#milestone-8-closeout).
Visit-date navigation and existing hover/table interactions remain;
their cache validity additionally includes display policy/revision.

1. An entity-name click must immediately close Road Coverage and focus the existing
   map canvas, even while detail is loading. The map must remain usable. An
   uncached load shows transient bottom-of-map feedback with **Cancel**, visible
   on desktop and mobile, above other map overlays at z-index **7** but below
   tooltips at **8**. A failure or **30-second** timeout shows map-level
   **Retry / Close**, not a modal detail error. Cancel/Close dismisses feedback and
   returns canvas focus. Detail requests have no hidden retry; Retry is manual.
2. The camera must wait for exact detail geometry and its expanded fit bounds,
   rather than fit approximate table/raw bounds while loading. Once available,
   retain the minimum **500-meter** extent, **350ms** fit animation and immediate
   white blink. The existing **three-second** highlight countdown begins only
   after camera movement ends and requested tiles are ready, retaining the bounded
   failed-tile fallback.
3. Repeated clicks may reuse a separate successful-detail LRU cache, scoped by
   selection ID, generation, resolved dates, display policy/revision, entity kind
   and canonical ID, with at most
   **8 entries / 8 MiB of UTF-8 serialized JSON** and a fixed **five-minute TTL
   from load**, never extended by a hit. Serialized size is not a heap-memory
   guarantee; oversized detail may display without caching. There must be no bulk
   geometry prefetch. The independent hover-stats cache, complete-table bulk cache
   and client-side paging remain unchanged.
4. Only the latest request in the current selection/generation/range, deletion,
   workout-subset and mode/diagnostics/refresh context may display, fit or cache
   detail or initiate recovery. Context changes invalidate detail state/cache;
   supersession, abort, Cancel and unmount must not surface stale UI errors or
   cache late responses. Only successful current responses are cacheable. Reusing
   the same installed geometry avoids redundant source `setData`, while a style
   or source replacement must reinstall it. Reopening Road Coverage must cancel
   pending detail work, not allow completion behind the dialog. Late 200, 503 or
   404 responses must not fit, cache, invalidate the bulk-table cache or
   automatically refresh the capability after reopen. Already-completed detail
   cache entries and highlights must be preserved.
5. A current 404 must invalidate detail and complete-table caches and leave visible
   **Retry / Close** feedback. Retry refreshes the map capability; the user then
   reopens Road Coverage and chooses again. It must not silently refresh and
   replay the stale click. Other failures/timeouts allow a manual detail retry.

### Matching Bounds And Diagnostic Parity

- Production Coverage and diagnostics share the same matching implementation and
  matching limits: at most **50,000 original GPS points** supplied to the matcher
  and **12,000 sampled GPS points per route** after sampling. The 12,000 limit is
  inclusive and is not a limit on stored raw-route points or imported workouts.
- Passing the sampled-point check does not guarantee completion: the route must
  also satisfy graph, evidence and execution bounds. An oversized matching input
  is rejected rather than silently truncated; the original stored route remains
  available.
- Matching normally uses overlapping 128-point windows. Typed graph-node or
  graph-edge exhaustion retries the same position with a 64-point window, then
  a 32-point window. Accepted windows overlap by one point without double-counting
  observations. Failure at the minimum window remains a processing-limit failure.
- Adaptive recovery retains the 2,048-node and 4,096-edge graph bounds, the same
  snapshot/deadline, and the existing connectivity/continuity rules. It neither
  discards arbitrary nodes nor fabricates coverage across gaps. Failed attempts
  publish no partial results. Other processing limits are specified in the
  [shared architectural contract](architecture.md#shared-matching-contract).
- A road split into finer pieces must not fail solely because it needs more than
  a fixed number of graph hops. Expansion settles distances through cached edges
  and loads each reachable node's adjacency at most once. Existing node/edge,
  distance, query-response and execution bounds govern work; repeated corrections
  do not consume an independent layer allowance. Final atomic persistence must
  not be cancelled by a competing heartbeat waiting on its own job-row lock.
- For identical input, OSM generations and effective matching parameters,
  diagnostics and production must make equivalent matching decisions and produce
  equivalent covered geometry, accounting for production's per-segment dissolution.
  Explicit diagnostic what-if parameters do not attest the unchanged production
  policy.
- Diagnostic request admission and HTTP deadlines remain operational safeguards,
  not alternative matching rules. A diagnostic request that expires before
  completion is inconclusive; it must not claim production matching would fail.
  Raising a point limit does not implicitly raise resource, concurrency or timeout
  budgets.
- Parking repair retains the established raw-support predicates, acceptance
  margins and candidate order. It may reject a certified cycling/growing
  continuation only when exact state/suffix equality and deterministic boundary
  choices establish nontermination. It must not replace that guard with a new
  preference for keeping already-supported middles. Ambiguous continuations keep
  the original choices and fail explicitly if bounded repair work is exhausted.
- A parking-repair state-byte bound can fail even below both point ceilings.
  `ParkingRepairLimitError` is a permanent processing limit; a generic stored
  `coverage-route-too-large` code does not identify which bound failed. A generic
  lifecycle `failed` log is consistent with that stored code, not contradictory
  evidence. Read-only core probes do not create jobs, persist diagnostics or prove
  successful production persistence. Dataset-specific probes belong only in
  private operator evidence; limits and matcher policy are unchanged.

### Validation

- Matching uses route quality information without discarding the source route.
- Sequence continuity, network connectivity, reliable heading, and later observations disambiguate intersections while preserving genuine turns.
- Account coverage cannot include another account's attribution.
- Segment identity, logical-path identity, locality identity, derivation version, and map-data version are retained for refresh diagnostics.
- Source attribution deduplicates by logical path/workout. Display statistics are
  independent of the checked/focused workout subset; source 030 unions distinct
  workouts across all context-resolved canonical members before summarizing.
- Road Coverage statistics use all eligible workouts in the resolved range,
  independent of checked routes; display-entity/workout deduplication precedes
  counts, including education identities combining multiple source paths.
- Bulk completion metadata must agree before caching. Limits and incomplete or
  failed responses never produce a partial table presented as complete.
- Stats validate account/session/selection liveness and current generation in the
  same database statement snapshot as the read; foreign, expired, stale or
  unavailable selections/entities return 404. The compact-fact foundation retains
  canonical-text matched-geometry availability and raw bounds independently;
  final canonical list/stats availability is defined by ready context metadata below.
- Source 030 list/stats share ready canonical metadata and an in-range-visit gate;
  detail separately requires actual matched geometry. A mixed park/path group's
  representative kind must not exclude path members or their geometry/history.

### Failure cases

- Unmatched points remain part of the raw route and do not create false path coverage.
- Ambiguous stretches may remain unmatched, and temporal or network gaps never create inferred connector coverage.
- Automatic region download is indefinitely deferred and disabled. Missing promoted map data leaves the raw route available in Routes and disables result-less Coverage focus with the tooltip **Map data not available**. Adding a provider-qualified region is an operator decision after storage preparation; successful promotion triggers reconciliation. Catalog diagnostics can identify unloaded regions, but production readiness is not a persisted inventory of every missing region. Any future auto-addition implementation must enforce download/storage bounds and single-flight updates before reporting a queued addition as pending.
- Matcher saturation delays asynchronous production processing. A synchronous diagnostic request fails quickly with retry guidance when cluster-wide matcher capacity is occupied.

### Acceptance criteria

- Given a workout traversing several segments of one locality-scoped path, including outbound and inbound travel, when coverage is calculated, then its path count contribution is one.
- Given El Camino Real geometry in Mountain View and Sunnyvale, when logical paths are derived, then the table exposes separate locality-scoped rows and attributions.
- Given only part of a locality-scoped path was visited, when Coverage renders, then only visited member-segment geometry appears.
- Given two disjoint portions of one locality-scoped path were visited, when Coverage renders, then both portions share path statistics while the unvisited middle remains absent.
- Given a workout traverses the same physical segment repeatedly in either direction, when segment evidence is persisted, then one workout/physical-segment match contains the dissolved geometry and unique covered length.
- Given a workout traverses two disjoint spans of one physical segment, when segment evidence is persisted, then one `MultiLineString` retains both components without covering the gap.
- Given a route proceeds straight through an intersection, when one GPS point is nearer the cross street, then the cross street receives no attribution without positive decoded traversal.
- Given a route genuinely turns at an intersection, when subsequent points support the connected turn, then both positively traversed roads receive attribution.
- Given a heavily visited logical path, when coverage renders, then fixed buckets do not force all low-count paths into one near-zero shade.
- Given an unnamed trail, when inspected with unnamed paths enabled, then it appears as **Trail** with its locality and stable identity rather than being omitted.
- Given named and unnamed paths wholly inside Yosemite National Park, when Coverage renders, then each path remains separately visible with `Yosemite National Park` as City/Region, unnamed paths use a type-specific fallback such as `Bike path`, and the park entity has no fabricated municipality.
- Given the date range or workout subset changes, when path details render, then first and latest visit remain the all-time date-only extrema.
- Given Coverage stats is open, when it is closed or Show on map is selected, then the already-mounted map is revealed without a new MapLibre instance.
- Given sustained Coverage hover, only the stats endpoint is requested after
  250ms; the popup still waits 750ms, including on cached returns. Moving across
  different entities for less than 250ms each makes no stats requests.
  Clicking its name in Road Coverage still retrieves geometry for fit/highlight.
- Given canvas exit or an entity change after a lookup starts, its successful
  response is cached without showing the old popup; returning reuses a cached
  result or in-flight lookup while restarting the full visibility dwell.
- Given a selection, generation, policy, revision or range change, or unmount
  during pending hover work, no late response can show a stale popup, cache stale
  stats or trigger stale selection recovery. Cache eviction and expiry cause a
  fresh request after the 250ms lookup delay.
- Given coverage diagnostics is disabled, when Coverage is selected, then covered geometry and statistics aggregate all checked routes.
- Given coverage diagnostics is enabled and one route is selected, when Coverage is selected, then only that route's diagnostic evidence and review controls appear.
- Given ingest commits routed workouts while coverage capacity is occupied, when Routes opens, then the new raw routes are available and Coverage identifies them as pending.
- Given rematching fails after prior coverage exists, when Coverage renders, then the prior valid result remains available and the failed update is identified.
- Given a route produces exactly 12,000 sampled points within the other bounds,
  both production and diagnostics pass the sampled-point check; 12,001 sampled
  points fail that check in both paths without truncation.
- Given more than 50,000 original points are supplied to matching, both paths
  reject the oversized input rather than matching a truncated prefix.
- Given a 128-point window exceeds a recoverable graph bound, successful 64- or
  32-point subdivision preserves observation accounting, route order/time and map
  provenance; failed larger attempts add no duplicate evidence.
- Given subdivision cannot satisfy the graph bound at the minimum window, the
  route fails explicitly without committing incomplete replacement Coverage.
- Given identical input, target generations and effective parameters, completed
  diagnostic evidence dissolves to the same covered geometry as production.
- Given the diagnostic HTTP budget expires first, the user receives an
  inconclusive diagnostic outcome rather than a production-matching verdict.
- Given an established parking repair terminates within the bookkeeping bounds,
  cycle prevention preserves its choices and result, including valid replacement
  of an already-supported middle.
- Given a deterministic parking repair recreates its exact continuation without
  consuming new input, the cycling/pumping candidate is rejected and later
  alternatives remain available; no fabricated partial Coverage is committed.
- Given boundary selection is ambiguous, recurrence alone cannot reject a
  candidate; cancellation or repair-work exhaustion remains an explicit error.

## Feature: Delete Workout Data

### User story

As a data owner, I want to delete one workout or an explicit period for privacy and pipeline retesting.

### Behavior

1. Individual deletion targets one workout ID.
2. Range deletion requires explicit inclusive start and end dates.
3. Relative date enums are not accepted for deletion.
4. Acceptance logically hides targets from Summary, Map, Coverage, Stats and exports; report first/predecessor history is recomputed without the hidden visits.
5. UI deletion removes visible rows and routes optimistically.
6. The worker purges the workout record, route points, attribution, detailed provenance, and affected aggregates after logical hiding.
7. A retry uses the original immutable workout-ID set.
8. A sanitized deletion audit retains requester, range, count, status, and timestamps.
9. The confirmation has two logical lines separated by one explicit line break.
   The first is **This workout will be hidden from view immediately.** or
   **These workouts will be hidden from view immediately.** The second is:
   **Workout data, including routes, workout-specific import and processing records,
   and coverage attributions, will be purged by a background task, and stats will
   be recalculated. This cannot be undone!**
10. Physical deletion removes workout-owned rows, including raw/derived routes,
    provenance, metrics, processing contexts/proofs and Coverage attributions.
    Shared stats lose the workout's contribution rather than being removed in
    their entirety. Task/job history and source-file bookkeeping remain, as do
    deletion tombstones that prevent resurrection. Stale canonical report/map
    contexts become unservable on generation/revision changes and are removed by
    later context garbage collection, not directly by the deletion task. Source
    export files and backups are outside this operation.

### Validation

- Only the account owner can delete targets.
- No `confirm` API parameter is required.
- The UI requires an explicit confirmation dialog.

### Failure cases

- Failed cleanup keeps targets hidden.
- Failure creates a Delete failed error notification and redacted logs.
- Retrying cannot include workouts ingested after the original request.

### Acceptance criteria

- Given a range deletion, when accepted, then later queries exclude the fixed target set.
- Given cleanup failure, when the user reviews Data Sync, then a retry and relevant logs are available.
- Given successful physical deletion and a later ingest of the same provider/fallback identity, the retained deletion tombstone suppresses resurrection, including replacement sources under the provider-identity contract.

## Feature: Monitor Jobs And Diagnostics

### User story

As a user, I want progress and safe diagnostics for asynchronous operations.

### Behavior

1. The SPA fetches immediately after commands. The selected Running job detail polls every 10 seconds, or faster if the configured interval is shorter. Queued job detail, Task history and the Data Sync summary retain the configured interval, default 30 seconds. Background detail polling pauses; terminal ingest detail stops interval polling. Terminal Coverage details that can change retry eligibility retain their configured-interval refresh behavior.
2. Job details show hierarchy, status, counts, attempts, safe events, and timestamps.
3. Parent cancellation cancels queued children and cooperatively stops running children.
4. Completed child data is not rolled back by parent cancellation.
5. Retry creates a new job linked to the failed job.
6. Owners can view security-redacted logs for their jobs.
7. Admins can view logs only for admin-owned system jobs.
8. A coverage Run detail shows routes total, processed, succeeded, failed, cancelled, and superseded, plus each authorized workout route's status, attempt, duration, and safe failure category. Persisted parent lifecycle remains monotonic internally. For the user-facing Task history and detail status, an unfinished Coverage parent is plain yellow `Running` only while it has a running child and plain light-blue `Queued` while it waits for the matcher; polling keeps this scheduler presentation current. No separate Matcher column is shown, and run-detail parent and route badges remain one-word badges. It never shows route coordinates.
9. Coverage retries form one linear parent chain. Run detail identifies the current attempt and provides ordinal navigation across First, Second, and every subsequent retry through the latest attempt.
10. Retrying a failed or partially successful coverage parent includes only eligible failed or cancelled routes from its immediately preceding attempt. Successful and superseded routes do not rerun, and a non-latest parent cannot fork the retry chain.
11. A failed or partially completed Coverage parent displays green **Superseded** when every historically unsuccessful route has proven current Coverage. This is a derived display status, not a rewrite of that attempt's stored execution status, results or timestamps. Individual routes still show the Failed or Canceled badges recorded by that attempt.
12. Coverage Run detail exposes advisory current retry eligibility and a safe explanation. When nothing remains eligible, Retry is disabled with a reason; fully resolved history explicitly says no action is needed. Current-Coverage links lead to the owned runs providing the route results, separately from retry-ancestry links.
13. A route has one authoritative current target, but may appear in several historical jobs/chains after genuine input, map-generation or matching-policy changes. Supersession does not authorize fabricated retry links or deletion of history. Incomplete, failed, foreign or unproven replacement work does not qualify for green Superseded presentation.
14. History status filtering uses the displayed status before counting and pagination. The Superseded filter selects resolved historical parents; Failed and Partially completed filters exclude them. Coverage detail and history, including empty filtered pages, continue refreshing when independent work can change terminal-parent eligibility/display state or totals.
15. Run-detail Files, Events and Logs pagination retains current rows and controls while a requested page loads. Full pages reserve the measured list height; only a loaded final partial page releases it. Cached page changes anchor the controls immediately even during background refetch. Deliberate scrolling overrides automatic anchoring. A failed request retains the displayed page and disables navigation until Try again succeeds, rather than skipping a page. Closing the disclosure or selecting another run cancels pending scroll restoration and isolates its page data.
15a. Each successful run-detail refresh also refreshes the open Files page, including when the detail response is unchanged. When the job or a source child has recorded ingest activity, also refresh the open Events page, including the final successful detail refresh after the job becomes terminal. Preserve disclosure pages, the Files failure filter and scroll position; mark closed Files and applicable Events pages stale without fetching them. Logs remain manually refreshed. Opening or paging Files on an active run also refreshes the run counters. Failed detail refreshes do not initiate Files or Events refreshes. These remain separately sampled HTTP responses, not an atomic multi-endpoint snapshot.
16. Manual and Automated sync show the actual current ingest activity after file counters and results, whether zero, one or many files were processed. File-counter completion does not imply job completion. Source details show their own activity; the root repeats the single active source's activity or labels each active source separately when several are active. Cancellation and terminal status take precedence over the last recorded phase.
17. Task history Operation and Status controls use the same spacing between controls as their top, bottom and right insets. Both selection panels share one anchor spanning the full filter row: Operation's left edge through Status's right edge, 12px below the row. Long lists scroll in place. Radio selection, arrow-key navigation, Escape dismissal and page-reset/filter semantics remain available.
17a. Order the Operation choices as All operations, Manual sync, Automated sync, Coverage update, Workout deletion.
17b. History rows match Workout log row height and use a right-aligned ellipsis
   action cell of the same width. Redistribute the saved link width proportionally
   among the other columns. Clicking a non-control part of the row or pressing
   Enter/Space on its focus target performs View detail once. Menu interactions
   must not activate the row. Mobile cards provide the same row/action choices.
17c. Reuse Workout log's ellipsis button and popup sizing/alignment/offset. Always
   put View detail first. Query authoritative parent details only when opening the
   menu, then use the same action policy as Run detail for Cancel run, Retry run
   or Retry deletion, including cancellation, supersession and retry-chain guards.
   Pending/failed reads cannot enable stale commands. Commands capture the chosen
   job ID, use session CSRF, refresh the relevant caches and cannot double-submit;
   inactive/unmounted completions cannot redirect or focus another workspace.
17d. Prepare authoritative menu choices before making the popup visible. Cached
   commands or a loading-only partial menu must not flash during a refresh. Pending
   preparation can be cancelled by outside interaction, Escape, blur or tab exit;
   a late response cannot reopen it. Freeze prepared choices for that open menu.
17e. On a requested run change, retain the accepted detail card at full size and
   make it inert until the new complete response is ready, then swap content in
   place. Do not erase it or insert an intermediate short loading panel. Pause old
   artifacts during a cross-run handoff without turning Logs into automatic reads.
   Once rendered, focus without native scroll; if the detail top is outside the
   content viewport, position it at the viewport top plus the inter-panel gap,
   clamping naturally when there is insufficient scrollable content. Background
   polling, inactive tabs and superseded requests must not repeat this landing.

### Ingest Activity Messages

The following wording is the user-facing contract. Phase names are internal keys,
not additional job lifecycle statuses. Imported-route analysis can occur between
files, so importing and analysis may repeat rather than advance once in a strict
sequence.

| State or phase | Exact message |
|---|---|
| Queued | Waiting for sync job to start. |
| `resuming` | Resuming incomplete sync job. |
| `preparing` | Preparing to read workout files. |
| `discovering` | Looking for workout files that need to be processed. |
| `importing` | Parsing workout files and importing new and changed workouts. |
| `analyzing_imported_routes` | Analyzing map data for imported routes. |
| `analyzing_existing_routes` | Analyzing map data for existing routes. |
| `updating_time_zones` | Determining and updating workout time zones. |
| `cleanup` | Cleaning up temporary files. |
| Succeeded with created or updated workouts | Sync job completed successfully. |
| Succeeded with no created or updated workouts | Sync job completed, but no new data were imported. |

- Existing-route analysis includes previously imported routes from the source,
  even for New data only, a restricted file-date range or a run with no imports.
  It checks map availability and prepares later coverage work; it is not street
  matching and does not promise that Coverage results are complete.
- Route-analysis activity shows `Routes checked: X`, or `Routes checked: X of Y`
  only when the total is known. Known zero is displayed; unknown totals never
  become fabricated percentages. Already-current routes can be checked from
  saved metadata; other routes count after their readiness outcome is committed.
- The final time zone step can update previously imported workouts across the
  account. Cleanup also covers closing the source and saving connection updates;
  its message is not a successful terminal result.
- Phases are recorded before their work starts, and detail shows the latest
  persisted phase from the current execution attempt. Brief phases can finish
  between polls; their persisted Events remain inspectable. Do not delay work to
  make a phase visible or replay completed phases as current activity. Historical
  jobs without phase records must not acquire guessed activity or event history.
- User-facing terminology is **time zone** or **time zones**, including errors.
  Code identifiers, field names and command names may retain `timezone`.

See [ingest activity state semantics](job-state-machine.md#ingest-activity-phases-requires-29)
and the [progress architecture](architecture.md#ingest-activity-and-refresh).

### Validation

- Terminal jobs cannot be cancelled.
- Only failed or cancelled jobs, or partially successful parents, can be manually
  retried where supported; only the latest job in a retry lineage may create the
  next attempt.
- Log access follows job ownership.
- GET retry eligibility is advisory. POST retains account/parent locking,
  latest-attempt and ordinal checks, and current-target revalidation. A transient
  busy conflict is not permanent ineligibility. A blocking conflict stays blocked
  until successful fresh metadata resolves it, even if the first refresh fails.
- New folded-028 automation requires **at least one timeout/verified-transient
  eligible FAILED child**, then carries **all authoritative FAILED siblings** with
  valid descriptors, including permanent, unknown and null-code failures, into
  the next chained attempt. It never reruns successes or revives cancellations.
  An all-permanent/unknown parent stops automatically. Latest-only authority
  remains required for all failed descriptors; no transient-only successor may
  strand permanent siblings behind a non-latest parent. The additional automatic
  count defaults to **1**, range **0-20**, shared durably per root; manual retry
  does not reset it. A failed automatic second attempt cannot create an automatic
  third with that budget already consumed. Normal unchanged failed-target scans
  cannot bypass the budget by creating fresh roots. This mixed policy is
  **deployed in `dev-20261020`**; historical release receipts retain their old policy.

### Failure cases

- Log capture failure does not hide the primary job error.
- Diagnostic output is bounded and never returns unredacted secrets or coordinates.

### Acceptance criteria

- Given a failed ingest child, when its parent is retried, then only failed children rerun.
- Given one failed coverage-route child and one successful child, when all siblings terminate, then the coverage parent is partially_succeeded and Run detail reports both outcomes.
- Given a failed or partially successful coverage parent is retried, when its failed routes remain eligible, then only those routes rerun using current matcher input and targets.
- Given a coverage parent has been selectively retried more than once, when any attempt's Run detail opens, then First, Second, and all later attempt ordinals navigate the same linear chain and identify the current and latest attempts.
- Given an earlier coverage attempt already has a retry, when retry is requested from that non-latest attempt, then no fork is created.
- Given a Failed or Partially completed Coverage parent has all unsuccessful
  routes resolved by current owned Coverage, history and its parent badge show
  green Superseded, Retry is disabled, and its original failed route badges remain.
- Given even one unsuccessful route lacks proof of current Coverage, the parent
  does not show Superseded; any current eligible failed targets remain retryable.
- Given independent newer runs resolve an older failed parent's routes, Run detail
  links those runs as current Coverage without inventing a linear retry relationship.
- Given 25 resolved failed histories, filtering Superseded returns all 25 with
  correct page totals while filtering Failed returns none; stored statuses remain failed.
- Given an already-current retry conflict followed by a failed detail refresh,
  cached eligible metadata does not re-enable Retry or expose private error text.
- Given a user ingest job, when an admin requests its logs, then access is denied.
- Given a selected Running job, detail refreshes after 10 seconds while Task
  history keeps its default 30-second cadence; a queued detail retains that
  cadence, a hidden tab pauses detail polling, and terminal ingest stops it.
- Given a complete file counter and no imported files, an active route-analysis
  phase still appears after the results; success appears only after the job's
  terminal commit.
- Given several phases complete between detail polls, the next refresh shows
  current activity and refreshes an open Events page with the recorded steps,
  including terminal catch-up, without fetching closed panels or refreshing Logs.
- Given multiple active sources in different phases, each source's current
  activity is identified rather than reporting one phase for the whole job.

## Feature: Receive And Dismiss Notifications

### User story

As a user, I want important sync and service messages without routine noise.

### Behavior

1. Notifications use info, warning, or error severity.
2. Status is display, dismissed, or remind.
3. Dismissal asks the server to choose the resulting status.
4. Broken-source dismissal becomes remind.
5. On next signin, the server checks persisted source state in the database.
6. An unresolved reminder returns to display.
7. A fixed or deleted source reminder becomes dismissed.
8. Manual sync success produces info; scheduled success produces nothing.
9. Partial ingest produces warning; failed ingest or deletion produces error.
10. The user avatar menu exposes Data Sync, Preferences, and Sign out.

### Validation

- Users can see and dismiss only their own notifications.
- Notification status cannot be assigned directly by the client.

### Failure cases

- A missing referenced source dismisses its reminder safely.
- Notification polling failure does not sign out the user or block map use.

### Acceptance criteria

- Given a dismissed broken-source banner, when the user signs in again and the source still fails, then the banner displays again.
- Given the source was fixed before signin, when reminders are evaluated, then no banner appears.

## Feature: Publish In-App Announcements

### User story

As an administrator, I want to notify all users of service information or planned disruption.

### Behavior

1. The administrator supplies a brief title, descriptive message, optional expiration, and optional severity.
2. Severity defaults to info and may be warning, but not error.
3. Every active data owner receives an independently dismissible notification.
4. Expired announcements stop displaying.
5. The administrator may retract an announcement before expiration.
6. Announcements are not emailed in the MVP.

### Validation

- Title and message are required and length-limited.
- Expiration uses RFC 3339.
- Only administrators may create or retract announcements.

### Failure cases

- Partial recipient creation is completed or rolled back consistently.
- Retraction remains auditable without displaying the announcement.

### Acceptance criteria

- Given a warning announcement, when users poll notifications, then each sees an independent warning.
- Given an expired or retracted announcement, when notifications are polled, then it is absent.

## Feature: Maintain Public Path Data

### User story

As an administrator, I want to inspect and refresh public path data without affecting access to private workout data.

### Behavior

1. The status API reports configured and promoted named regions, source versions, last successful refresh, active job, replica readiness, and safe failures.
2. The administrator can enqueue one bootstrap or refresh operation at a time.
3. OSM work runs below interactive and ingest priorities.
4. A successful refresh reconciles copied matched segments and rematches only affected private routes where needed.
5. A failed refresh leaves the last usable public and copied segment data active.
6. Status and diagnostics contain no private account routes or coverage details.
7. Region-update requests are globally coalesced by provider and region while queued or running.
8. A newly promoted region queues coverage for routed workouts lacking current coverage in that region.
9. Refreshing an existing region queues reconciliation for all routed workouts in that region, including workouts with existing coverage.
10. A coverage update skips a workout only when its applied OSM generation and matcher version are already current.

### Validation

- Only administrators can request status details or refresh.
- An equivalent queued or running refresh returns the existing job.

### Failure cases

- Public endpoint throttling or import failure produces an admin-owned failed job and safe diagnostics.
- Replica lag prevents promotion until required data is readable.

### Acceptance criteria

- Given a refresh already running, when another is requested, then no second refresh starts.
- Given a failed refresh, when a user opens existing Coverage, then previously reconciled segment data remains usable.

## Feature: Use The Responsive Interface

### User story

As a user, I want the product to remain usable on desktop and iPad-sized tablets
or larger.

The 10/11/2026 scope decision assigns tablet-focused UI/UX to what is now Milestone 11.
References to mobile layouts in this specification target iPad-class tablets,
not iPhone-sized screens; phone-focused development is explicitly out of scope.

### Behavior

1. Desktop shows a bold Workouts Explorer wordmark at upper left.
2. Primary tabs appear in Summary, Map, Stats, Data Sync order. Summary retains its date control; Map and Stats put the shared Date range picker first in their left panels.
3. Map mode uses compact Routes/Coverage radio controls beside the bulk workout checkbox in the Workouts panel.
4. Data Sync is a primary tab; the upper-right avatar opens Preferences and Sign out.
5. An About information control appears beside the avatar.
6. Mobile omits the wordmark to maximize content space.
7. Mobile Map overlays the avatar in the upper right.
8. Mobile view, filter, and base-map style controls live in a bottom slide sheet.
9. Dark and light themes preserve contrast for routes, coverage, controls, and tables.
10. Desktop Map controls expose the same base-map style families as mobile.
11. Map date and base-style pickers use the application's shared dropdown treatment; the desktop panel starts directly with those controls and contains no redundant Map hero.
12. The unified workout list consumes the remaining panel height above Coverage stats and Fit on map and scrolls independently.
13. Map legend/diagnostic cards and navigation controls share an outer inset of **0.75rem on mobile layouts and 1rem on desktop**; MapLibre's inner control margin is zero. The Road coverage legend reserves the zoom-control column and gutter rather than overlapping it on narrow maps. Existing viewport-bound map and sidebar scrolling remain unchanged.
14. Route and Coverage tooltips are passive canvas-level popups above the legend, navigation controls and attribution. They do not intercept pointer events or interrupt hover; the map host must not trap their stacking below sibling overlays.
15. Keyboard focus indicators remain visible on all sides for route-list actions, unavailable-route help, sorting headers and segmented choices. All shared sortable-table headers use the same cell-wide inset outline inside the cell borders, including Workout log, New Coverage and dialog tables. Use inset rings where overflow/rounded frames would clip an outward outline on other targets. Selected segments use a contrasting themed focus color, with system Highlight in forced-colors mode. Focus alone does not change route selection, activate a mode or sort the table. Narrow Stats layouts use available report width so a header target fits inside its scrolling frame; retain the centered half-width composition at the desktop breakpoint.

### Validation

- Keyboard and touch controls expose equivalent actions.
- Focus, hover, selected, warning, and error states are distinguishable without relying only on color.
- Attribution for the active public map style remains visible and current after style or theme changes.

### Failure cases

- Missing Gravatar uses a safe generated fallback.
- Gravatar failure does not block signin or primary content.

### Acceptance criteria

- Given a mobile viewport, when Map opens, then the map remains the dominant surface and filters are available from the bottom sheet.
- Given a theme change, when the page reloads, then the selected theme and readable map controls are preserved.

## Feature: Use The REST API And Swagger

### User story

As an owner or administrator, I want a discoverable and validated REST API for setup and operations without an MVP management UI.

### Behavior

1. Swagger UI is publicly readable at `/swagger`.
2. The OpenAPI 3.0.3 document is publicly readable at `/api/openapi.yaml`.
3. Protected operations remain authenticated regardless of public documentation.
4. Requests are validated against their schema and reject unknown JSON fields.
5. Responses do not use a universal data envelope.
6. Paginated responses place items and pagination fields together.
7. Multiple sort fields use `sort=field:direction,...`.
8. Response contracts are validated in CI.

### Validation

- Compact and dashed UUIDs in either case are accepted.
- UUID responses use compact uppercase form.
- Page size respects the operator maximum.
- Null sort values always appear last.

### Failure cases

- Validation and authorization failures return RFC 9457 responses with a safe request ID.
- The public OpenAPI document contains no credentials, private hostnames, or sensitive examples.

### Acceptance criteria

- Given a misspelled JSON field, when an API command is submitted, then it is rejected rather than ignored.
- Given a bearer session token in Swagger, when an owner operation is invoked, then normal account authorization still applies.

## Feature: Install And Use The Public Release

**Planned Milestone 13: GitHub Project Publication.** The owner's production
deployment, capacity and environment-specific hardening move to Milestone 14.

### User story

As a public self-hosting operator, I want published images, installable charts and
human-readable documentation so that I can run the product without access to the
developer's private infrastructure or development history.

### Publication and discovery

1. Publish the initial stable product as **GitHub Release v1.0.0**. Root `VERSION`
   becomes `1.0.0` during release preparation; chart `version`/`appVersion`, Git
   release tag and component images identify that same verified release.
2. Use **GitHub Actions** for public CI pipelines: PR build/test validation,
   trusted release builds, GHCR image publication, Helm publication, documentation
   validation and GitHub Pages deployment. Public build/test badges in README
   link to those actual workflows and never substitute static passing labels.
3. Release notes provide supported versions/architectures, prerequisites, artifact
   references, installation/upgrade documentation and known limitations. Publish
   license/notices, contribution and support/security reporting information.
4. Public GHCR image packages include the UI, API, shared worker and OSM operator.
   Release consumers can pull them anonymously. Stable version/digest references
   identify reproducible content; a `latest` alias identifies the newest completed
   stable release without changing existing version-pinned chart installations.

### Helm installation and database choice

1. Publish one versioned Helm package through both a conventional HTTPS Helm
   repository (`index.yaml`) and a public OCI chart repository in GHCR. Document
   both `helm repo add`/`helm install` and OCI `helm install` usage, including
   namespace, release values and required Secret setup.
2. Public chart defaults point to the newest compatible stable GHCR images at
   chart release time, with matching version/digest pins and documented overrides.
   No private Harbor access or manual image build is necessary.
3. Include a standard **single-server/standalone PostgreSQL chart dependency**,
   enabled by default. Document how to disable it and use **BYO PostgreSQL**.
   Both modes support separate application and public OSM databases/roles, the
   supported PostgreSQL/PostGIS versions and required UTF8/collation behavior.
4. Bundled mode persists database storage and credentials across ordinary chart
   upgrades. Explain storage classes, sizing, backup/restore, credential/key
   retention and uninstall/PVC behavior. External mode uses explicit connection/
   Secret and TLS settings without creating or replacing an external database server.
5. A clean install waits for a usable database, establishes required roles and
   schema, then bootstraps the application. Upgrades enforce supported migration
   compatibility before incompatible workloads serve traffic. Database/dependency
   failures stop initialization with actionable diagnostics rather than a Helm
   hook deadlock, a partially initialized usable application or ignored errors.
6. Document external requirements such as SMTP, ingress/TLS, base-map access and
   source/OSM setup separately from components supplied by the chart. Operator
   instructions cover keys/Secrets and administrator bootstrap before first signin.

### Human-facing documentation

1. Publish a **Docusaurus** documentation site built from Markdown/MDX as static
   HTML on **GitHub Pages**, using **GitHub Actions** for validation and deployment.
2. Provide distinct administrator and end-user navigation, search, stable links and
   version-aligned guides. README links to the site, release and tested quick start.
3. Administrator guides cover prerequisites, both installation/database modes,
   configuration/Secrets, SMTP, invitations/account lifecycle, sources, initial OSM
   import/refresh, operations, backup/restore, upgrades, recovery and uninstall.
4. User guides cover invitations/signin/recovery, Summary, workout detail/actions,
   Map/Routes/Coverage, Stats reports, Data Sync, notifications and preferences.
5. Provide API/Swagger and Helm values references and runnable, portable examples.
   Distinguish guide content from chronological developer implementation receipts;
   public installation requires no private hostnames, source archives or credentials.

### Validation and failure cases

- A release fails publication checks when VERSION/tag/chart/image versions disagree,
  required tests fail, referenced images are unavailable or docs/links fail to build.
- Untrusted PR validation cannot publish images, charts, Pages or release assets.
  Release reruns preserve existing immutable version artifacts or reject a conflict.
- Both Helm channels must contain the identical package and retain previous chart
  versions; users do not need to guess which package matches the release.
- Missing installation prerequisites, failed migrations or unsupported database
  extensions/collations produce actionable setup failures, not a false success.

### Acceptance criteria

- A new operator follows published guides on a clean cluster, pulls public images
  and installs 1.0.0 using either Helm channel without private registry access.
- Both bundled PostgreSQL and BYO paths complete bootstrap, import and core
  Summary/Map/Coverage/Stats/Data Sync journeys, with working documented upgrade
  and recovery paths and no pre-install dependency deadlock.
- GitHub Actions build/test badges reflect the linked jobs; the public GitHub
  Release, image manifests/digests, chart package and docs refer to the same source.
- The GitHub Pages site works under its project base path, has searchable admin/
  user guides and valid release-specific installation/API/Helm links.
- Upgrades preserve supported database data/credentials; uninstall documentation
  accurately states which data is retained and how explicit removal works.
