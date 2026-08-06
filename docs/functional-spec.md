# Functional Specification

This specification describes observable MVP behavior. Implementation structure belongs in `architecture.md`.

## Global Conventions

- User-facing product name: Workouts Explorer.
- Primary views: Summary and Map.
- Map modes: Routes and Coverage.
- Detailed coverage table: Path Coverage.
- User-facing synchronization term: Data Sync.
- API synchronization term: ingest.
- Dates use `YYYY-MM-DD` in APIs and localized display in the UI.
- General timestamps use RFC 3339 and may be serialized in UTC.
- Workout and route-point timestamps preserve their source offsets when available.
- API IDs accept dashed or compact UUIDs in either case and return compact uppercase UUIDs.
- API errors use RFC 9457 `application/problem+json`.

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

1. The Preferences dialog allows changing full display name, theme, units, default timezone, first weekday, clock format, workout columns, and page size.
2. Dark theme is the default; light theme is available.
3. Imperial units, Monday week start, and 12-hour time are defaults.
4. The last-used date-range preference is restored on a later session.
5. Username and email are visible but immutable.

### Validation

- Default timezone must be an IANA timezone name.
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

As a data owner, I want to configure source adapters through Swagger or REST so that my archived workouts can be synchronized.

### Behavior

1. The owner creates a source with a unique display name, immutable type, `autoSyncEnabled`, and adapter-specific `config`.
2. Supported MVP types are `health-auto-export-local` and
   `health-auto-export-icloud`; Milestone 3 introduces local/NFS and Milestone 8
   adds iCloud to the discriminated API union.
3. OpenAPI selects and validates the correct config schema from `type`.
4. Creation or update encrypts the complete adapter config, sets `checking-connection`, and enqueues a high-priority connection check.
5. A successful check sets `connected`.
6. A failed check sets `connection-failed` and creates an error notification.
7. A failed source cannot be selected for ingest until its config passes a later check.
8. Partial updates retain omitted settings and write-only secrets.
9. Required secret values cannot be cleared with `null`.

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
3. Incremental discovery processes only new or changed files.
4. A day without an export is a successful no-data result.
5. A source becomes stale after the configured number of calendar days without a newly discovered export, default three.
6. Staleness is a warning that may also mean the user did not work out; it is not an ingest-job failure.
7. A newly discovered export clears the stale condition and its reminder.
8. Dismissing an unresolved stale warning follows reminder behavior.
9. Scheduled success remains silent.
10. Partial or complete failure creates a warning or error notification.

### Validation

- Disconnected, checking, failed, deleted, or auto-sync-disabled sources are not scheduled.
- Equivalent queued or running jobs are reused rather than duplicated.

### Failure cases

- One failed source does not block other source children.
- Authentication expiry reports a source error and does not leak rclone output or credentials.
- Worker interruption leaves a retryable job and recoverable file state.

### Acceptance criteria

- Given two auto-sync sources, when the cadence fires, then one parent with two source children is created.
- Given no new file, when incremental sync completes, then the child succeeds with zero imported workouts.
- Given one successful and one failed child, when the parent finishes, then its status is partially_succeeded.

## Feature: Run Manual Sync

### User story

As a data owner, I want to synchronize selected sources and historical periods on demand.

### Behavior

1. Manual Sync is available from the avatar menu.
2. The dialog lists all current sources.
3. Auto-sync sources are checked by default; the user may change the selection.
4. New data only is the default mode.
5. Specific date range requires inclusive start and end dates.
6. New-data mode discovers only new or changed files.
7. Bounded mode deliberately reprocesses every matching file, including previously processed checksums.
8. The request creates one parent job and one child per selected source.
9. Exact equivalent active work returns the existing job.
10. Manual completion creates an info, warning, or error notification according to outcome.

### Validation

- At least one connected source must be selected.
- Start and end dates must both be omitted or both supplied.
- Start date cannot follow end date.

### Failure cases

- A connection-failed source cannot be submitted.
- A source deleted after submission cancels only its affected child.
- Partial completion reports successful and failed sources separately.

### Acceptance criteria

- Given no dates, when Manual Sync is submitted, then only new or changed files are processed.
- Given an explicit range, when Manual Sync is submitted, then every matching file is reprocessed.
- Given an equivalent active request, when submitted again, then the existing parent job ID is returned.

## Feature: Process Source Files Safely

### User story

As an operator, I want ingest to use bounded temporary storage so that historical imports cannot exhaust a worker volume.

### Behavior

1. Discovery records candidate metadata without downloading all files.
2. Each available processing slot handles one file through download, parse, commit, and cleanup.
3. Remote files are staged under a user- and job-scoped temporary directory.
4. A slot deletes its staged file on success, failure, or cancellation before claiming another.
5. Worker startup removes abandoned staging files.
6. File concurrency is limited per account, per worker, and globally.
7. Defaults are two files per account, two per worker, and four globally.
8. Connection checks and deletion jobs bypass ingest-file concurrency.

### Validation

- Filenames cannot escape the staging directory.
- Source files larger than an operator limit are rejected safely if such a limit is configured.
- Invalid JSON fails the file without committing a partial file result.

### Failure cases

- Cleanup failure is recorded in diagnostics and telemetry.
- A pod interruption leaves the file eligible for startup cleanup and job recovery.
- One malformed workout does not expose raw payload data in logs.

### Acceptance criteria

- Given a range containing thousands of files, when ingest runs, then no worker stages the whole range at once.
- Given three source children for one account, when all have files, then no more than two files for that account process concurrently by default.

## Feature: Normalize And Upsert Workouts

### User story

As a data owner, I want repeatable imports that preserve provider meaning without creating duplicates.

### Behavior

1. Source plus provider workout ID identifies an existing workout when available.
2. A fallback fingerprint is used only when the provider ID is absent.
3. Provider workout names map to normalized workout-type records while retaining labels such as Outdoor Run and Indoor Run.
4. Provider aggregate values take precedence over incomplete sample sums.
5. Provider duration and original timestamp offsets are preserved.
6. Changed content updates the existing workout transactionally.
7. Changed routes rebuild dependent coverage, elevation, timezone, and aggregate data.
8. Import events record created, updated, or matched_unchanged.
9. Overlapping workouts from distinct recording sources remain distinct.

### Validation

- Provider IDs are unique within one source.
- Metric units must be known or retained with a normalization warning.
- Missing optional route or metric data does not invalidate an otherwise valid workout.

### Failure cases

- An invalid workout is reported without corrupting previously committed workouts.
- Suspicious units or quality values create adapter warnings rather than silent reinterpretation.

### Acceptance criteria

- Given the same unchanged workout in a bounded reimport, when ingest finishes, then no duplicate is created and matched_unchanged provenance is appended.
- Given changed route content for the same provider ID, when reingested, then the existing workout and derived map data are updated.

## Feature: Retain And Export Route Data

### User story

As a data owner, I want complete normalized route data for display, analysis, and export.

### Behavior

1. Ingest retains ordered source points with provider sequence.
2. Retained fields include timestamp, longitude, latitude, altitude, speed, course, and all four accuracy values.
3. Duplicate timestamps do not collapse points.
4. GeoJSON is the default route representation.
5. GeoJSON contains one LineString plus workout metadata; it is 3D when altitude is available for the route and otherwise 2D.
6. Points format returns complete normalized per-point data in canonical units.
7. Downloads use `YYYY-MM-DD-workout-type.geojson` or `.json`.
8. Elevation minimum, maximum, and gain are derived whenever possible.

### Validation

- Latitude, longitude, and timestamp are required for a usable point.
- Invalid optional accuracy values are retained as unavailable or quality-flagged.
- Point order follows provider sequence, not timestamp uniqueness.

### Failure cases

- Missing route altitude produces a 2D LineString without failing the workout.
- Invalid optional point properties do not reject otherwise usable coordinates.

### Acceptance criteria

- Given duplicate point timestamps, when points are exported, then both appear in source order.
- Given route altitude, when GeoJSON is exported, then coordinates contain longitude, latitude, and altitude.
- Given any supported provider, when points are exported, then the same normalized JSON schema is used.

## Feature: Select Date Ranges

### User story

As a user, I want explicit dates and common shortcuts so that Summary and Map show the same period.

### Behavior

1. Desktop uses a Grafana-inspired date control near the view selectors.
2. Supported shortcuts are This week, Last week, Last 7 days, Last 30 days, This month, Last month, This year, and Last year.
3. API reads require either explicit start/end dates or one `dateRangeEnum`.
4. `tz` overrides the user's default timezone for enum resolution.
5. `tz` is silently ignored with explicit dates.
6. Resolved dates and timezone are returned in response metadata.
7. Summary and Map share the same selection.
8. Explicit and shortcut boundaries are inclusive calendar dates.
9. Workout membership uses the workout's local start date.
10. Last 7 days means today plus the preceding six dates; Last 30 days means today plus the preceding 29 dates.
11. This/Last week honor the user's first-weekday preference; month and year shortcuts use calendar boundaries.
12. Daylight-saving changes do not alter calendar-date membership.

### Validation

- Explicit dates must appear together.
- Enum and explicit dates are mutually exclusive.
- Unknown enums and IANA timezones are rejected.

### Failure cases

- Missing all date selectors rejects date-bounded reads.
- Invalid ordering rejects the request without running a query.

### Acceptance criteria

- Given Monday week start, when thisWeek is resolved on Wednesday, then the range begins Monday.
- Given an explicit range and `tz`, when queried, then the explicit dates are used unchanged.

## Feature: Review Summary

### User story

As a user, I want a concise statistical and tabular summary of workouts in a selected period.

### Behavior

1. Summary displays compact aggregate values above the table.
2. Hovering an aggregate shows values grouped by workout type.
3. The table defaults to newest workout first.
4. Clicking a header changes sorting and toggles direction.
5. The API supports multiple sort columns even though the UI normally uses one.
6. Null values sort last.
7. Desktop shows preferred columns.
8. Mobile shows date/timezone, workout type, and duration, with tap-to-expand details.
9. Elevation fields are available for all routes and shown by default for configured types such as Hiking.

### Validation

- Page size respects the configured maximum.
- Sort fields are allowlisted; duplicate or unknown fields are rejected.
- Required date-range rules apply.

### Failure cases

- No workouts produces a valid empty state with zero aggregates.
- Unsupported optional statistics display as unavailable rather than zero.

### Acceptance criteria

- Given workouts of several types, when an aggregate is hovered, then its type breakdown is shown.
- Given a mobile viewport, when a row is tapped, then additional workout details expand inline.
- Given equal primary sort values, then stable UUID tie-breaking prevents records moving between pages.

## Feature: Review Workout Details

### User story

As a user, I want to inspect one workout's available statistics without crowding the Summary table.

### Behavior

1. Selecting a workout row reveals a detail presentation appropriate to the viewport.
2. Mobile expands details beneath the three-column row.
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

### User story

As a user, I want common workout actions available from each Summary row.

### Behavior

1. A right-aligned three-dot menu shows the actions listed below in fixed order.
2. Show on map switches to Map, selects Routes, filters to that workout, and fits its extent.
3. View provenance shows the full chronological import-event history.
4. Delete workout asks for confirmation naming the workout date and type.

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
2. Routes use easily distinguished colors by workout type.
3. Routes render oldest first and newest last.
4. Hovering near overlapping routes selects the topmost, most recent route.
5. The selected route is highlighted bright purple in full.
6. A workout filter allows any subset within the period.
7. Private route tiles require authenticated account access.

### Validation

- A map selection belongs to one session and account.
- Foreign, expired, or invalid selections are rejected.
- Tile requests cannot provide arbitrary account IDs.

### Failure cases

- No routed workouts produces a helpful empty map state.
- A partial source route renders as recorded and is identified as partial where metadata permits.

### Acceptance criteria

- Given overlapping routes, when the overlap is hovered, then the most recent visible workout is selected.
- Given a changed workout subset, when the map redraws, then only selected workouts appear.

## Feature: View Path Coverage

### User story

As a user, I want to see which roads, trails, and other paths I have visited and how often.

### Behavior

1. Coverage matches route points to the nearest eligible public-map path within the accepted threshold.
2. All relevant path classes are candidates, including roads, trails, cycleways, and unnamed paths.
3. One path segment receives at most one attribution from a workout.
4. Repeated traversal retains the earliest matching workout timestamp.
5. Coverage ignores workout type for coloring.
6. Fixed blue count buckets represent distinct workout counts.
7. Hover details show selected-period count, all-time count, first visit, latest visit, and path name.
8. Unnamed paths display N/A.
9. Path Coverage provides sortable, paginated rows and the description `Roads, trails, and other paths visited.`

### Validation

- Matching uses route quality information without discarding the source route.
- Account coverage cannot include another account's attribution.
- Segment identity and map-data version are retained for refresh diagnostics.

### Failure cases

- Unmatched points remain part of the raw route and do not create false path coverage.
- Missing public-map data queues bounded retrieval or reports matching as pending.

### Acceptance criteria

- Given a workout traversing the same segment outbound and inbound, when coverage is calculated, then its count contribution is one.
- Given a heavily visited segment, when coverage renders, then fixed buckets do not force all low-count segments into one near-zero shade.
- Given an unnamed trail, when inspected, then it appears with N/A rather than being omitted.

## Feature: Delete Workout Data

### User story

As a data owner, I want to delete one workout or an explicit period for privacy and pipeline retesting.

### Behavior

1. Individual deletion targets one workout ID.
2. Range deletion requires explicit inclusive start and end dates.
3. Relative date enums are not accepted for deletion.
4. Acceptance logically hides targets from Summary, Map, Coverage, and exports.
5. UI deletion removes visible rows and routes optimistically.
6. The worker purges the workout record, route points, attribution, detailed provenance, and affected aggregates after logical hiding.
7. A retry uses the original immutable workout-ID set.
8. A sanitized deletion audit retains requester, range, count, status, and timestamps.

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
- Given successful physical deletion and a later bounded ingest of the original files, then the workouts are recreated with new application IDs for testing.

## Feature: Monitor Jobs And Diagnostics

### User story

As a user, I want progress and safe diagnostics for asynchronous operations.

### Behavior

1. The SPA fetches immediately after commands and polls active jobs at the configured interval, default 30 seconds.
2. Job details show hierarchy, status, counts, attempts, safe events, and timestamps.
3. Parent cancellation cancels queued children and cooperatively stops running children.
4. Completed child data is not rolled back by parent cancellation.
5. Retry creates a new job linked to the failed job.
6. Owners can view security-redacted logs for their jobs.
7. Admins can view logs only for admin-owned system jobs.

### Validation

- Terminal jobs cannot be cancelled.
- Only failed or cancelled work can be manually retried where supported.
- Log access follows job ownership.

### Failure cases

- Log capture failure does not hide the primary job error.
- Diagnostic output is bounded and never returns unredacted secrets or coordinates.

### Acceptance criteria

- Given a failed ingest child, when its parent is retried, then only failed children rerun.
- Given a user ingest job, when an admin requests its logs, then access is denied.

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

1. The status API reports loaded regions, source version, last successful refresh, active job, replica readiness, and safe failures.
2. The administrator can enqueue one bootstrap or refresh operation at a time.
3. OSM work runs below interactive and ingest priorities.
4. A successful refresh reconciles copied matched segments and rematches only affected private routes where needed.
5. A failed refresh leaves the last usable public and copied segment data active.
6. Status and diagnostics contain no private account routes or coverage details.

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

As a user, I want the product to remain usable on desktop and mobile.

### Behavior

1. Desktop shows a bold Workouts Explorer wordmark at upper left.
2. Summary/Map controls appear at the top left with the date control to their right.
3. Map mode adds Routes/Coverage beside the view controls.
4. The upper-right avatar opens Data Sync, Preferences, and Sign out.
5. An About information control appears beside the avatar.
6. Mobile omits the wordmark to maximize content space.
7. Mobile Map overlays the avatar in the upper right.
8. Mobile view and filter controls live in a bottom slide sheet.
9. Dark and light themes preserve contrast for routes, coverage, controls, and tables.

### Validation

- Keyboard and touch controls expose equivalent actions.
- Focus, hover, selected, warning, and error states are distinguishable without relying only on color.
- Public map attribution remains visible.

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
