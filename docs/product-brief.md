# Product Brief

## Product Purpose

Workouts Explorer is a self-hosted web application for exploring recorded fitness
activities through workout summaries, GPS routes, geographic coverage and trends.
It helps people understand both their exercise history and the places they have
explored.

Fitness platforms record individual activities, but make it difficult to see a
lifetime of workouts as a connected geographic history. Workouts Explorer helps
users answer:

- Where have I exercised, during a particular period or across my entire history?
- Which streets, trails, paths and parks have I visited, and how often?
- Which nearby places have I not explored?
- How consistent has my activity been, and how has it changed over time?
- Which places did I visit for the first time or return to after a long absence?

The product brings archived workouts into one consistent view while keeping each
person's activity and source credentials private.

## Intended Users

- People who record fitness activities and want to explore their history beyond
  individual workout summaries.
- Walkers, runners, cyclists and others who intentionally explore new streets,
  trails and paths.
- Self-hosting individuals or small groups who want control over their fitness
  data and installation.
- Administrators who need approachable account, map-data and operational
  management without handling routine tasks through manual API calls or scripts.

The intended scale is a small private installation of roughly ten users, each
with a separate personal account. Each account can contain multiple years,
thousands of workouts and several gigabytes of source archives.

## In-Scope Features

### Private Accounts And Preferences

- Invitation-only registration, local sign-in and email-based password recovery.
- Independent personal accounts with private workouts, routes, sources,
  notifications and diagnostics.
- Saved preferences for appearance, units, time zone and workout-history display.
- Light and dark themes, with keyboard, pointer and touch interaction for desktop
  and iPad-sized tablet browsers.
- A separate administrative identity and workspace, independent of personal
  workout use.

### Data Sources And Synchronization

- Apple Watch workout history exported from Apple Fitness through Health Auto
  Export, including workout measurements and recorded GPS tracks.
- Source archives stored locally, on a network-mounted filesystem or in iCloud
  Drive.
- Owner-controlled source setup, credentials and connection checks through the
  **Data Sources** profile-menu dialog, with add/edit/delete workflows and retained
  imported workouts when a source is deleted.
- Automatic synchronization and manual imports, including selected sources and
  historical date ranges.
- Incremental imports that avoid duplicate workouts, with explicit reprocessing
  when needed.
- Visible progress, source freshness, cancellation, retry and actionable failure
  information.
- Import provenance that helps users understand where a workout came from and
  how it was updated.

External Apple authorization and two-factor approval remain outside the
application. Users manage those requirements before connecting an iCloud source.

### Workout History

- A consistent workout model across supported sources and activity types.
- Date-range summaries of workout count, duration, distance, energy and other
  available measurements, with breakdowns by workout type.
- Sortable and filterable workout history, including duration, distance, pace,
  calories, heart rate and elevation where available.
- Workout details with meaningful local dates, times and preferred units.
- Route and recorded-point exports.
- Individual and date-range deletion, including cleanup of associated activity
  data and recalculation of affected statistics.

### Maps And Geographic Coverage

- Recorded routes on an interactive map, distinguishable by workout type.
- A path-coverage view showing visited portions of streets, trails and paths.
- Exploration of nearby unvisited paths alongside recorded activity.
- Workout selection and date filtering to focus the map on relevant activity.
- Roads, paths and parks organized by locality, with visit counts and dates.
- Distinct-workout counts that do not inflate when one workout repeatedly follows
  the same logical road or path.
- Direct navigation between geographic entities, visits and the workouts that
  produced them.
- Configurable background-map styles with light and dark variants.
- Regional OpenStreetMap data preparation and maintenance for geographic coverage.

### Statistics And Trends

- Consistency reports showing active days, duration, distance, calories and
  per-workout maxima over weeks and months.
- New Coverage reports showing first-ever visits and returns after more than a
  year, grouped by month or year.
- Drill-down from report totals to the places and visits behind them, with a path
  back to the map or workout history.
- Correct historical context when evaluating first visits and long-gap returns,
  even when the displayed period covers only part of that history.
- Preserved exploration context when moving between reports, workouts and maps.

### Administration

- User invitations, resend and revocation, account suspension/reactivation,
  session revocation and account deletion. Password recovery is self-service.
- Registration information and a limited account-usage summary: last login,
  workout and route counts, and latest workout date.
- Regional map-data download, preparation, refresh and recovery, with visible
  progress and understandable intervention requirements.
- Service health, background-task monitoring, operational incidents and recovery
  guidance.
- In-app service announcements and an administrative audit trail.
- Administrator preferences and visibility into effective installation settings,
  while deployment-managed configuration remains under the operator's control.

Administration does not grant access to individual users' workout records, GPS
routes, source credentials, private notifications or diagnostic logs. The limited
usage summary supports account management without becoming a way to browse
personal fitness history.

### Self-Hosting And Integration

- Kubernetes installation with versioned packages and documented configuration,
  upgrades, backups and recovery.
- A supported installation path with a supplied database or an existing
  compatible database.
- Public release artifacts and administrator/user guides that do not depend on
  the product owner's private infrastructure.
- A documented API for automation and integration alongside the browser workflows.

## Out Of Scope

- Public registration, commercial multi-tenant hosting or large-scale service
  operation.
- Sharing workouts or routes between users, comparative account analysis, or
  geographic sharing permissions.
- Multiple or delegated administrator roles and administrator impersonation of
  personal accounts.
- External identity-provider sign-in.
- Fitness-provider adapters beyond Health Auto Export.
- Native mobile applications or a phone-first interface.
- Live workout recording and workout editing beyond deletion.
- Route planning, social networking, goals, challenges and competitive features.
- In-app Apple authentication or two-factor setup.
- Continuously replicated global map data or a general-purpose map-hosting service.

## Product Quality And Success Criteria

- An invited user can register, connect a source and import workout history
  without learning the underlying API.
- Users can move from a summary or trend to its underlying workouts and places
  without losing their exploration context.
- Repeated imports preserve workout identity and avoid duplicates.
- Summaries, visit counts and trend reports remain consistent with the imported
  history, including missing measurements and long-term records.
- Maps remain useful and interactive while imports or map maintenance run.
- Failures explain what happened and what the user or administrator can do next.
- Account boundaries protect fitness data and credentials, including in exports,
  diagnostics and operational monitoring.
- Administrators can complete routine onboarding, maintenance and recovery through
  understandable product workflows.
- Installation and upgrades are repeatable and preserve existing imported data.

## Detailed Specifications

- [Functional specification](functional-spec.md) for detailed user-facing behavior.
- [Administrator UI specification](admin-ui-spec.md) for administrative workflows.
- [Architecture](architecture.md) for technical design and implementation boundaries.
