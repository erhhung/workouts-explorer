# OSM Provider Catalog Refresh

Open **Administration / OSM Maps / Regions** and choose **Refresh catalogs**.
This fetches public indexes and HTTP headers, never the PBF extracts.
Geofabrik is the first supported provider. There is no automatic provider-refresh
schedule.

```yaml
osm:
  dataProviders:
    - geofabrik
  catalogRefresh:
    enabled: true
    metadataConcurrency: 5
    databaseSecret:
      name: workouts-explorer-database
      key: osmCatalogDatabaseUrl
```

Concurrency is an integer from 1 to 32, independent of ingest slots. The runtime
URL uses the worker's existing database TLS mount; certificate paths must match
that mount. Do not put credentials in Helm values.

## Setup

Run `scripts/osm-catalog-role.sql` on the OSM cluster using the provisioning
credential, supply a separate strong password through Secret management, then
apply OSM migration 011 with the migration credential. `workouts_osm_catalog`
has no elevated role attributes/memberships. Grants cover region INSERT/UPDATE,
public catalog/generation/storage SELECT and the bounded
`osm_catalog.region_database_bytes(text)` function, not generation changes,
region deletion, promotion or DDL. The API never receives this writer credential.

Application migration 033 adds the durable queue and public projections. Supply
the new runtime Secret key before upgrading with the default enabled lane.
`osm.catalogRefresh.enabled=false` explicitly disables the trigger/lane while
leaving cached regions readable. No additional Kubernetes RBAC is required.

For PostgreSQL HA, the catalog URL must select a writable server rather than a
load-balanced standby. Use a compatible primary-aware endpoint or a libpq/pgx
multi-host URL with `target_session_attrs=read-write`. When a pooler cannot
authenticate the additional restricted login, see
[the optional per-node Services manifest](../deploy/osm/catalog-ha-services.yaml). Its three
stable DNS names allow primary selection across failover while keeping
`sslmode=verify-full`; each name must match the server certificate. Do not pin a
current pod IP or weaken certificate verification. An isolated vcluster may also
need narrow DNS forwarding for these database-cluster names. The deployment
uses three exact-name CoreDNS stub zones, preserving existing DNS configuration.

## Progress And Recovery

Progress tooltips dismiss on mouse exit, outside click, Escape and window blur;
counter updates do not reopen them. Job completion updates the retained table
automatically without a page reload. The boundary outline remains 5px: its round
joins are not enlarged by thickening or smoothing the geographic boundary.

The API coalesces active requests and records one audit. The worker heartbeats a
two-minute lease; expired executions can be reclaimed up to three attempts, with
a 30-minute execution limit and a shared OSM catalog lock. Accepted work survives
browser closure/navigation. Regions polls at most every five seconds while a job
is active, pauses offscreen, and retains search/sort/scroll and row identities.

Names/boundaries are saved before HEAD metadata enrichment. Successful requests
update size/date together. Failures preserve last-known values/observation time
and record a safe failed attempt; jobs report partial completion rather than
fabricating measurements. Another on-demand refresh retries unavailable entries.
Unsupported configured providers fail safely without arbitrary network requests.

## Table And Boundaries

The table uses the Users/Workout log composition, Road Coverage search/footer,
25-row pages, stable page slots and an unchecked **Show all regions** filter.
The default view hides Available regions but retains a full 25-row viewport.
Checking it includes all catalog regions. Number/date values right-align within a shared
left-anchored column extent; missing values are muted `n/a`. Provider display
labels are plain text with HTML breaks removed and raw path slugs humanized.
Regions' aligned section/header/search/table/footer have a scoped 80rem maximum;
the shared Workout log maximum stays 90rem. Magenta boundary outlines use rounded
line joins while retaining their five-pixel width and label/overlay order.
Title casing applies generically to region words and hyphenated components, with
common prepositions/articles kept lowercase and acronyms preserved. It does not
match a list of specific region names; French Polynesia, voivodeship and kraj
labels follow the same rule. Database usage gives 25% of its initial allocation
to Name. The remaining Status track ends at the fixed ellipsis column's padding,
matching the Workout log header/action boundary.
Downloaded and Status each release a further 10% of their widths to Name. The
minor-word rules include mit/et/y/with/de/da/la/includes and the intact a.k.a.
abbreviation, with the first-word exception. Boundary rounding also explicitly
sets `line-round-limit: 0`, disabling the default shallow-angle miter shortcut.

Click a row or choose **Show boundaries** to inspect a cached region polygon on
the theme-appropriate Streets map. Its magenta fill is 25% opaque with a 5px
outline beneath labels/symbols, and opening fits the boundary extent. The same
map and WebGL canvas persist through dismissals, region changes and theme swaps;
the map is disposed only on actual workspace/session teardown. Geometry is
requested per region through the admin-only cached boundary endpoint and is
excluded from bulk list responses. Opening boundaries never contacts Geofabrik.
The canvas is square at the retained dialog width, with dialog scrolling on short
screens. Regions uses its own CSS hooks; Road Coverage checkbox placement is not
inherited. Admin 1-4 and arrow paging work while search is unfocused; typing,
menus and modal dialogs keep their keyboard guards. Refreshing replaces the
action with a yellow help label and a **completed of total regions cataloged**
tooltip. Polish voivodeship and Czech/Slovak kraj labels are title-cased while
provider-qualified IDs stay unchanged.

The initial boundary-only slice kept lifecycle entries disabled. Managed
Download/Reprocess/View progress and Pause/Resume/Cancel are now handled by the
[dedicated region update controller](osm-region-updates.md) when enabled. Delete
region remains disabled pending its separate public-data purge workflow.

## Metadata Meaning

- **PBF size / Date:** successful Content-Length/Last-Modified observation
  after the constrained provider redirect, separate from installed-generation data.
- **Downloaded:** completed-download date for the current generation; its tooltip
  gives exact downloaded bytes. Unknown legacy dates use muted `n/a`.
- **Generation:** currently promoted generation, with its promotion date in the
  tooltip. It is deliberately not sortable.
- **Database usage:** allocated ways/localities/path-segment partition bytes including
  indexes and TOAST. Shared metadata/timezones, WAL, backups, replicas, candidates
  and retired garbage are excluded. Public storage projections refresh every
  five minutes without contacting providers.
- A per-generation processed-size snapshot, source data-as-of timestamp and
  promotion time are also retained. None substitutes for download completion.

Refresh preserves active map generations even if a provider removes an entry;
mark it unavailable instead of deleting its data. Deployment-configured initial
regions remain distinct from prepared status. Hierarchical Geofabrik IDs retain
parent/original-ID metadata, with established norcal/socal installation IDs stable.
