# Administrator Region Updates

Regions are managed in the administrator UI; no static region list is supplied by
Helm or used to bootstrap the application. Any successfully promoted active region
is eligible for Coverage. Catalog selection proofs include active-region membership
so a first promotion revisits previously unavailable routes automatically.

The Regions table's **Workouts** column counts distinct, non-deleting workouts
across all accounts whose stored route intersects the region boundary. A route
crossing multiple regions counts once in each. Its tooltip reports **n coverage
routes**: workouts with applied results for their current route input and this
region's latest promoted generation. A new generation resets that progress count,
not the workout total. Private spatial memberships are indexed and maintained on
route/boundary changes; the administrator receives aggregates, not workout details.

The Regions page lets an administrator download, process, pause, resume, cancel and delete
OpenStreetMap (OSM) updates. An update processes and validates new map data before
making it active. The region's previously promoted data stays available while an
update runs, and remains available if the update fails or is canceled.

## How An Update Runs

1. **Download region** or **Reprocess region** adds a request to the update queue.
   The status becomes **Waiting**. Repeated equivalent requests share the existing
   queued or active job instead of starting duplicate work.
2. A dedicated controller creates a Kubernetes Job to run the updater. The
   `osm.regionUpdates.concurrency` setting limits how many updater or cleanup Jobs
   can be dispatched at once; its default is **1**.
3. The updater must also acquire exclusive write access to the shared OSM
   database. A pod can be Running while its region still shows Waiting because
   another updater owns that database lock.
4. **Downloading** starts with source preparation, including pending cleanup and
   statistics maintenance, followed by the PBF download. A PBF is the compressed
   OSM source file.
5. **Processing** covers filtering, database import, derived map data, validation,
   promotion and final cleanup. Promotion makes the validated result active for
   that region.

The queue accepts region IDs from the configured provider catalog, such as
`geofabrik:us/us-virgin-islands`. Requests cannot supply arbitrary download URLs,
SQL, container images or Kubernetes specifications.

Updater and cleanup Jobs have a Kubernetes owner reference to the controller
Deployment, `workouts-explorer-osm-controller` in the development installation.
Its stable UID keeps Jobs independent of controller pod/ReplicaSet replacement.
The controller reads only its named Deployment to obtain this identity; it needs
no permission to modify or delete that Deployment. Database request labels and
Job UID checks remain the authority for progress and termination decisions.

## Updater Pod Logs And Job Retention

Completed and failed updater, cancellation-cleanup and region-deletion Jobs retain
their pods for **6 hours** by default. Configure this Kubernetes TTL in Helm:

```yaml
osm:
  regionUpdates:
    ttlSecondsAfterFinished: 21600
```

The TTL starts when Kubernetes marks the Job finished, not when processing starts.
Set `0` for immediate TTL cleanup. The setting applies to newly created Jobs;
older Jobs without a TTL retain their previous controller-cleanup behavior.
Terminal retained pods do not hold processing capacity. The controller still waits
for running/pending pods to stop before finalizing, and unresponsive active Jobs
may still be explicitly stopped. Each cleanup retry has a distinct Job name so
its failed predecessor's logs can remain available.

While the Job is retained, list it and read its updater container logs:

```sh
kubectl --context xdev -n workouts-explorer get jobs -l app.kubernetes.io/component=osm-update
kubectl --context xdev -n workouts-explorer logs job/<job-name> -c osm-update --timestamps --tail=200
```

Add `--follow` to watch a running updater. The application job/task history remains
available independently of Kubernetes Job/pod retention.

For Jobs whose pods have already been removed, use the archived logs at
**https://logs.fourteeners.local**. Sign in, open **Discover**, choose the
`fluent-bit-*` index pattern and set a time range covering the failed execution.
In this installation, filter `kubernetes.namespace_name.keyword` to
`xdev-workouts-explorer` and `kubernetes.pod_name.keyword` to the updater pod name
(or use the Job name as a prefix). The archived message field is `log`, and
`@timestamp` is the event time. Host logging labels differ from the virtual
cluster namespace used in the `kubectl` commands.

Failure summaries show the cause without an extra `<task> failed:` prefix. Generic
container/unknown-outcome messages say **Review the updater pod logs for this job.**
When final validation rejects a completed SQL report, the specific validation-gate
reason is retained for the controller and dialog instead of becoming a generic
container exit. Completed SQL checkpoints remain committed; a rejected candidate
is not promoted.

## Storage Monitoring Policy

External filesystem metrics are opt-in through
`osm.regionUpdates.storageMetrics`. Configure the URL, metric names and exact
selector labels there. Disabled/unconfigured metrics force the global monitoring
control off; configured-but-missing/stale metrics still block monitored work.
The configured path does not discover PostgreSQL exporter pods or assume replicas.
The optional Service IP overrides only the configured URL hostname for isolated
DNS environments. SQL-derived region and retained-build sizes are independent.

Settings > **OSM Processing** provides **Monitor available database storage**,
enabled by default. Save the global default in the application database. Every
new region request records an initial preview. Every actual updater start or
resume atomically captures the current value/revision for that execution. Saving
a new default does not change an already-running execution, but a queued start
or subsequent resume uses the latest setting. Completed history retains the last
policy given to the updater. Deletion retains its
existing cleanup behavior and does not use Prometheus storage monitoring.

The option covers optional database-space safeguards, not data correctness:

| Work unit | Monitoring enabled | Skipped when disabled? |
|---|---|---|
| Check the writer is a PostgreSQL primary and acquire exclusive updater ownership | Always | **No** |
| Check tool versions, source size/checksum/header, and scratch/download limits | Always | **No** |
| Read checkpoints and record progress/control heartbeats | Always | **No** |
| Replay pending temporary/retired-object cleanup | Always | **No** |
| Check the statistics receipt and repair missing/interrupted maintenance | When needed | **No** |
| Map the primary to its storage volume through Prometheus | Before guarded batches | **Yes** |
| Fetch available space/capacity and source timestamps; validate freshness | Before guarded batches | **Yes** |
| Retry failed/missing storage-metric queries | When required | **Yes** |
| Sample low-water storage during long batches | Every 30 seconds | **Yes** |
| Read storage after a batch | Recent valid readings are accepted with healthy headroom | **Yes** |
| Wait for newer post-batch storage readings | Only when a pre/in-batch/post reading is below 15% free | **Yes** |
| Request a second newer reading when free space decreases | Only on the low-headroom path | **Yes** |
| Extra settling polls below 10% free | Bounded by the existing settling timeout | **Yes** |
| Block further work when required storage observations show insufficient space | Existing 10% floor | **Yes** |
| Storage-triggered mid-processing vacuum of the temporary identity grouping table | Only below 15% free | **Yes** |
| Delete temporary tables/indexes at their task boundaries | Always | **No** |
| Required indexes, stage-local query-planner statistics and validation | Always | **No** |
| Atomic publication, retired-data cleanup and required final statistics | Always | **No** |
| PostgreSQL's independently configured autovacuum | Database-managed | **No; not controlled by this setting** |

Healthy monitoring must not impose a fresh-scrape barrier after every batch.
Recent valid readings and the background monitor retain visibility; fresh waits
are reserved for low headroom, where confirming reclaimed space matters. Missing,
stale or invalid required observations continue to fail closed while monitoring
is enabled. Disabling monitoring omits all updater Prometheus calls and settle
waits, while ordinary database errors, primary checks and transaction safeguards
remain effective.

The motivation is measured overhead rather than SQL execution cost: Monaco's
retained run had 111 long post-SQL gaps, 78 near 30 seconds and 33 near 60 seconds,
totaling about 71 minutes 48 seconds of its 78-minute 28-second active runtime.
Healthy monitoring should avoid those scrape-cadence barriers. Actual speedup
depends on the region, database work and cache state and must be measured rather
than inferred from the earlier incomplete work counters.

Do not add blanket final vacuuming. The temporary grouping table used by the
conditional mid-processing vacuum is removed before finalization, so vacuuming
it later would serve no purpose. Required final ANALYZE remains distinct from
storage reclamation. The job history exposes its saved monitoring policy for
auditability and comparisons; no performance factor is promised without a
controlled comparison.

## Actions By Status

| Status | Job actions |
|---|---|
| Available | Download region |
| Ready / Stale | Reprocess region; Delete region |
| Waiting | Cancel download or Cancel processing, according to the queued operation |
| Downloading | Pause download; Cancel download |
| Processing | Pause processing; Cancel processing |
| Pausing or Canceling | Wait for the requested stop to finish |
| Paused | Resume processing; Cancel processing; Delete region |
| Failed | Resume processing; Cancel processing for an unfinished, unpromoted candidate; Clear job status; Delete region when data remains |
| Canceled | Download region; Clear job status; Delete region when data remains |
| Deleting | View deletion progress; wait for storage reclamation |

**View boundaries** is the first menu action. **View job progress** is second when
job history is available, followed by a divider before job commands. The Failed
menu orders Resume processing, eligible Cancel processing, then Clear job status; Canceled orders Download
region before Clear job status. Delete region is a separate danger action below
another divider. It is unavailable while an updater or cleanup is still running.

Clicking any status opens View job progress whenever that action is available.
This includes newly queued downloads/deletions before execution has started, as
well as Waiting after resume, Pausing and Canceling. The menu and status shortcut
both open progress immediately. Queued progress names the first step and shows
the existing waiting message with a spinner, then updates in place as work starts.
View boundaries is the fallback when progress is unavailable. A region created by
an older operator-only workflow may have active data without a managed job history.

Stale is yellow and shows exactly **Updated matcher policies require reprocessing**
through the shared delayed tooltip. It means the promoted generation's importer
or derivation contract predates current map-processing requirements, not that
the usable map is removed. Matcher-only changes stale workout Coverage results,
not region data. Cleared/absent run overlays reveal this base state; an older
successful run does not force an obsolete generation back to Ready.

Statuses use the application's semantic colors: Ready is green; waiting, running
and stopping states are yellow; Failed and Deleting are red; Paused and Canceled are muted gray.
Available uses normal text. Clickable status values use the shared accent on hover.

### Pause And Resume

Pause asks the updater to stop its current unfinished batch. The status stays
**Pausing** until the pod has stopped; it then becomes **Paused**.

Resume processing puts the request back into the queue behind work already
running. Completed database checkpoints are retained. A checkpoint records work
that can safely be reused after the updater process stops.

The first reusable import checkpoint is reached after `osm2pgsql`, task #4,
completes. A pause or failure before that point cannot retain the pod's temporary
source files, so resume restores the download and import prerequisites. Later
resumes continue from compatible completed checkpoints. The updater checks that
the saved pipeline versions and completed SQL stages still match before reusing
them, using the same recovery rules as the operator script.

A pause can arrive after a batch has committed but before its post-batch checks
finish. That committed work remains complete. Before continuing, the controller
reconciles paused task rows against saved checkpoints, so resume does not leave a
finished task marked Paused or rerun it unnecessarily. A lost completion report can
recover timing from the immediately preceding successful batch start and its
committed checkpoint. That increment preserves prior task and job counters; it
does not include the later pause or metrics wait. Ambiguous intervals remain
unavailable. While a resume is queued, the current-task line says Waiting to resume
processing and keeps prior task history separate from queue status.

### Corrected Processing Rules And Fresh Rebuilds

Validation checks map-data correctness; rerunning them does not repair the map
data. If a producer step created invalid locality attribution, that earlier step
and its dependent identity/summary steps must run again with corrected rules.
Completed geometry checkpoint fingerprints intentionally prevent resuming a
candidate under changed processing SQL. A clear failure explains when fresh
processing is required instead of replaying validation indefinitely.

For a failed, resumable candidate that has not been promoted:

1. Choose **Cancel processing** and wait for **Canceled**. Its isolated candidate
   is discarded; any previously promoted region remains available.
2. Choose **Clear job status** to return to the region's baseline state.
3. Choose **Download region** or **Reprocess region** to run the corrected producer
   and all dependent work before the unchanged validation gate.

Clearing status alone does not discard an unfinished candidate. Do not substitute
region deletion for candidate cancellation when promoted map data should remain.
Failed maintenance after promotion must be resumed, not canceled.

### Cancel

Cancel requests both a stop and removal of the unfinished processing job's temporary database schema.
The status stays **Canceling** until the controller confirms that the updater pod
has stopped and the unfinished data has been removed. If the updater dies before
confirming cleanup, the controller starts a separate cleanup Job. The job's queue
capacity is not released until this is resolved.

Cancel never deletes the active generation. If promotion has already committed
when Cancel arrives, the promoted data stays active and final cleanup completes.
Likewise, a failure after promotion can leave valid active data with unfinished
maintenance; Resume processing finishes that maintenance.

### Clear Job Status

Clear job status removes the latest **Failed** or **Canceled** result from the
Regions status display. The region returns to **Available** if it has no promoted
data, or **Ready** if it does. Clearing the latest result does not reveal an older
failure underneath it.

Clearing preserves the job details, task history, audit events and region data.
It cannot clear active work or a job whose stop/cleanup is still in progress.
Repeating a clear request has no additional effect. Like other administrative
commands, it requires an authorized administrator. Commands authenticated through
browser cookies also require a CSRF token to prevent cross-site request forgery.

## Delete A Region

Delete region opens a confirmation naming the region and its provider-qualified
ID. Confirming queues irreversible cleanup and immediately shows **Deleting** in
red. The workflow survives closing the browser and uses the same dispatch limit
and exclusive database writer lock as updates. Deletion cannot be paused or canceled.

View job progress opens **Deleting <region ID>** with three tasks:

1. **detach-region** atomically removes the region's active partitions and active
   storage mapping, retires its promoted processing attempt and queues its stored resources.
2. **reclaim-storage** physically drops the detached tables, their indexes and
   TOAST storage, and all remaining temporary processing schemas belonging to that region.
3. **verify-cleanup** checks that no active mapping, region data, temporary schema or
   unfinished cleanup remains and records a durable completion receipt.

Dropping these relations returns their files to PostgreSQL's filesystem; deleting
rows alone would leave allocated table space behind. No whole-database `VACUUM
FULL` is needed. This reclaims database storage without resizing the database's
persistent volume.

The controller also waits for the cleanup pod to stop and independently checks
the receipt and absent resources. Only then does the region become **Available**,
with its download/generation/usage values cleared. Show all regions must be checked
to keep Available rows visible. Download region can then start a fresh processing job.

Completed deletion history remains available through View job progress. Its title
is **Deleted <region ID>**; an already-open **Deleting <region ID>** dialog changes
to that completed title when cleanup finishes.

Interrupted cleanup retries automatically and stays Deleting until verified.
Other regions and their active map data remain intact. The provider catalog and
boundary, job/audit records and successful promotion history are retained; a later
successful promotion continues the region's sequence. Deletion supersedes paused
or failed checkpoints, so an old request cannot resume removed data.

An unfinished processing job may leave temporary staging data and checkpoints,
for example after a pause, failure or interrupted cleanup. Deletion removes this
regional processing data while retaining job/audit history.

### Existing Workout Coverage

Region deletion does not delete workouts, raw GPS routes or previously applied
Coverage. Matched geometry, road/path metadata, attributions and rendering chunks
are stored separately in the application database. Map overlays and Coverage
history read those saved results without fetching the deleted OSM region again.

Removing shared regional source data alone does not make an unchanged applied
Coverage result stale. In Map Coverage mode it remains enabled and displays
Coverage current, without a missing-map warning. Display freshness checks the
stored route/matching identity and the accepted regional generation heads;
prerequisites for a new calculation remain separate. A new route without applied
Coverage still needs active regional map data. A genuinely changed route or
matching policy still makes the older result stale.

Downloading and processing an Available region again promotes a generation above
that region's retained high-watermark. Once this newer promotion is observed,
previous Coverage correctly becomes stale and can be refreshed. Region deletion
reclaims shared OSM data, not retained user-specific Coverage records.

<a id="generations-and-processing-attempts"></a>

## Generations And Processing Job IDs

Regions shows a retained build's last completed post-cleanup allocation snapshot
in yellow, in addition to active-generation usage when present. Snapshots include
tables/indexes/TOAST in the build schema, not WAL, executor scratch/PBF files or
discarded per-batch objects. These values update at batch/task boundaries rather
than estimating in-flight work from row counts. Promotion/discard hides the
candidate component; recorded measurement metadata remains available for audit.

These identify different things:

| Term | Meaning | Example |
|---|---|---|
| Generation | A sequential number within one region, starting at 1. Successful promotion atomically increments a durable high-watermark that survives deletion. | NorCal generation 18; Virgin Islands generation 1 |
| Processing job ID | A globally unique internal identifier for one processing job/build, including failed or canceled jobs. It is not a retry count. | NorCal job #62; Virgin Islands job #65 |

The Regions column and public `generation` field use the regional promotion
number. Failed attempts consume no generation number. Deleting generation 1 and
processing that region again produces generation 2, never a reused generation 1.
Counter allocation does not depend on promotion timestamps or another region's
numbering. Previously promoted metadata and the counter remain after cleanup.

Implementation compatibility identifiers such as `buildId`, `generation_id`,
`osm_build_<id>` and the operator `--build` option continue to identify internal
processing jobs/builds. They do not represent the region-local generation number.

Preparation banners say **Preparing source update: performing PostgreSQL storage
cleanup** and **Preparing source update: ensuring stats partitions are healthy**.
Once a job has been reserved, batch banners use:

```text
====== Starting job #76 (generation #1, ruleset v21) batch "derive/node-counts" (3 of 5) ======
====== Job #76 (generation #1, ruleset v21) batch "derive/node-counts" (3 of 5) completed in ... ======
```

The generation is the region's next promotion number, allocated from its durable
high-watermark, not another rendering of the job ID. Failed jobs and resumes do
not consume an extra promotion number. The ruleset is the OSM derivation version.
Download logs also show **Downloading: <full source URL>** and **Wrote <bytes> bytes
to <full destination>**, such as `/scratch/job-76/source.osm.pbf`.

## Progress And Elapsed Time

The Regions table refreshes active work every ten seconds. View job progress also
refreshes every ten seconds while open. Progress comes from structured updater
events rather than parsing pod logs. Tasks stay in execution order, and dates and
times use the viewer's local time zone.

During execution the dialog title uses **Downloading <region ID>** or
**Processing <region ID>**. A successfully completed job uses
**Processed <region ID>**, including when an open dialog refreshes at completion.

The [rebuild inventory](osm-resumable-rebuild.md#full-rebuild-batch-inventory) defines
the 58 tasks. Hovering or focusing a task name shows the description from that
document's **Work and completion boundary** column. The UI imports the document
directly so those descriptions stay in sync.

New download/reprocess requests have 58 steps. Step 1, **prepare-source**, validates
tools, acquires database ownership, replays pending cleanup, checks the statistics
receipt and reserves the processing workspace. Step 2, **download**, measures only
the source transfer, size/checksum/header handling and source metadata recording.
Healthy installed regions are not rescanned during preparation: a durable receipt
recognizes already-maintained active generations, including unchanged survivors
after a region is removed. Only genuinely missing/interrupted maintenance requires
recovery. Statistics are refreshed after a new promotion and recorded atomically.

Earlier requests retain their original 57-step numbering and combined preparation/
download history; new executors translate progress for those requests rather than
renumbering running work. Legacy download help explains the combined timing.

The current-task line reads, for example,
**22 of 58 — clip-apply — batch 2 of 24 — 34m 24s elapsed**. The live clock measures
active execution intervals, including bookkeeping and waits for storage-metric
observations between short batches. It excludes queued and paused intervals and
continues ticking when no SQL batch is currently running. Once the job finishes,
the same active elapsed duration becomes the total, for example
**58 of 58 — validate/finalize — 1h 25m 55s total**. It includes metrics settling,
bookkeeping, publication and final maintenance, not just SQL execution. Unknown
timings remain omitted.

Before any step starts, the status line is only an active spinner and **Waiting
for updater capacity** or **Waiting to start**, with no invented step counter.
Queued resumes similarly show only **Waiting to resume processing**, while the
table keeps its recorded task history. Once a task is active, its status icon
matches the corresponding table icon. The final
**validate/finalize** task stays Running, with Finished shown as `n/a`, until map
publication, old-data cleanup, required database maintenance and local temporary-file
cleanup have all succeeded. If maintenance fails or pauses, the task reflects that
outcome; resuming a published region reopens this final task until maintenance succeeds.
It does not turn green merely because the validation checks passed.

Download failures name the source file directly:
`Failed to download <URL> | <reason>`. Click the URL, or focus it and press Enter,
to copy only the URL to the clipboard. The tooltip and accessible feedback confirm
the copy; if clipboard access fails, select the URL to copy it manually or retry.

Final database maintenance can take longer than the regional processing itself.
On PostgreSQL 17, refreshing the shared partitioned-table statistics also refreshes
the installed region partitions, so a small region can finish its processing
quickly while the final task continues working on shared database maintenance.

The first table column is **Step**. Opening the dialog focuses its step list, so
Up/Down and Home/End scroll it immediately. Opening or changing the current task
follows that row: tasks 1–20 start at the top, tasks 21–57 align row `n-19` below
the sticky header, and the final task scrolls to the absolute bottom. Stopped and
completed history follows the same rule. Short/mobile viewports scroll far enough
to keep the current row visible when twenty rows cannot fit. Automatic following
past task 20 takes one second: accelerate for 250ms, cruise for 450ms, then decelerate
for 300ms, with continuous speed between phases. Reduced motion follows immediately.
Wheel, touch, scrollbar/pointer and scrolling-key input stop the animation without
blocking the user's action; a newly selected/current task can resume following.
Stable background
polls preserve manual scroll and focus. Repeated steps retain their recorded batch
counts after completion, such as **12 of 12**; single-batch or unknown counts show
`n/a`. The current-task line's batch suffix appears only
when the updater knows that the task has more than one batch. Some tasks revisit
their work until results converge, so a task number is not a percentage-complete
estimate for the whole job.

### Task Totals

Hovering or focusing Finished shows the task's active span from its first batch
start through its last batch completion, including storage-metric settling.
Finished is recorded after that settling boundary, not immediately after the SQL
commit. The durable checkpoint is reported before the wait so pause/resume still
preserves committed work. An interrupted wait records the time actually spent.

Queued and paused intervals are excluded. Without such gaps, subtracting Started
from Finished gives the duration, subject to displayed whole-second precision.
Revisited phases can overlap in wall time; do not add their task spans to derive
the job total. The job total independently covers active execution, including
publication, cleanup and statistics maintenance.

Totals below ten seconds show two decimal places, such as **2.74s total** or
**0.88s total**. Longer totals show whole seconds, minutes and hours as needed.

Elapsed totals are stored with the job and its tasks. Duplicate reports remain
idempotent. Retained execution/event boundaries and protected execution evidence
recover historical durations, including waits omitted by earlier accounting. A
last SQL-completion timestamp is not treated as the end of a later metrics wait.
If a reliable end boundary is unavailable, the tooltip explains that timing was
not recorded rather than presenting a partial-work total.

### Generation Totals

A Generation tooltip shows the promotion date and, when available, its completed
job's total: **November 8, 2026 | 16h 24m 23s total**. The history is matched by
processing attempt ID, so a newer failed or cleared attempt does not change the promoted
generation's duration. If that total is unavailable, the tooltip shows the date
alone.

## Failures And Resource Requirements

The controller reads the updater pod's termination status before deleting its Job.
The error summary retains the failed task and useful context, such as a container
exit code or an out-of-memory termination with the pod's memory limit. These
details remain available after the pod is removed. Raw database connection errors
and credentials are not copied into the UI.

The updater reserves **2 GiB** of memory and has a **4 GiB** limit by default,
configured through `osm.regionUpdates.jobResources`. A small compressed PBF can
still require substantial memory because filtering tracks OSM object references
and the importer uses its own cache.

Both the Go writer and the command-line database tools must select a writable
PostgreSQL primary. Multi-host connection settings must preserve each endpoint,
TLS settings and `target_session_attrs=read-write`. Connecting an importer to a
read replica causes writes to fail with a read-only transaction error.

Before heavy SQL, the updater checks fresh database disk-space measurements from
Prometheus. Missing, stale or insufficient-space readings block processing under
the [storage preflight policy](osm-resumable-rebuild.md). Clearing an operator
storage block requires the normal operator recovery procedure; the UI does not
resize database disks, delete snapshots or clear those blocks automatically.

## Configuration And Permissions

Enable the workflow after provisioning its database credentials and approved
updater image:

```yaml
osm:
  regionUpdates:
    enabled: true
    concurrency: 1
    controllerDatabaseSecret:
      name: workouts-explorer-database
      key: osmControllerDatabaseUrl
    executorDatabaseSecret:
      name: workouts-explorer-database
      key: osmExecutorDatabaseUrl
    writerDatabaseSecret:
      name: workouts-explorer-database
      key: osmUpdateDatabaseUrl
```

The responsibilities are deliberately separate:

- The API accepts administrator commands and reads job progress. The API, general
  worker and Coverage worker do not receive Kubernetes Job-launching credentials.
- The controller manages the queue, launches updater/cleanup Jobs and updates the
  public region metadata. Its Kubernetes permissions are limited to Job lifecycle
  operations and reading pod status in its namespace. It cannot browse Secrets,
  execute commands in pods, change Deployments or resize storage.
- The executor reports progress through a dedicated database function using a
  token restricted to its assigned execution. It cannot read private application
  data or query the control tables directly. Resume and cleanup executions replace
  the old token.
- The OSM writer processes map data in the OSM database. Each unfinished processing
  attempt is bound to its request, allowing cleanup to target its temporary data
  without deleting another request's data or the active generation.

The controller checks a Job's Kubernetes resource UID and approved specification
before adopting or deleting it; a matching name or label alone is insufficient.
If a Job-creation response is lost, it looks for the matching Job instead of
launching a duplicate.

Helm supplies the trusted image digest, temporary storage, resource limits,
deadline, database/TLS Secret references and metrics routing. The updater receives
no Kubernetes API token or private owner-source credentials. Database connections
must use full TLS verification. For settings and schema details, see the
[Helm reference](../helm/README.md) and
[database migration guide](database-migrations.md). The
[operator script](../scripts/osm-update-operator.sh) remains available for CLI
recovery using the same database writer lock.
