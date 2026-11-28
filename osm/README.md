# OSM Maps and Coverage

Workouts Explorer uses OpenStreetMap (OSM) data to turn recorded workout routes
into Coverage: the roads and paths a person has actually travelled. This directory
contains the map importer, processing SQL, optional operator Job templates and
synthetic regression data.

There are two different map layers to keep in mind:

- **The base map** supplies the background streets, labels and terrain shown in
  the Map tab. It comes from the configured map-style provider.
- **The Coverage map data** is downloaded and processed by this application. The
  matcher uses it to interpret workout GPS, and the application renders the
  resulting covered geometry over the base map.

A street appearing on the background map does not by itself mean its current OSM
data has been processed here, that the workout travelled it, or that matching has
finished.

## From a Regional Download to Coverage

```text
Regional OSM extract (.osm.pbf)
    -> selective import into an isolated candidate build
    -> public residential context and road/path derivation
    -> boundary clipping, attribution, identities and indexes
    -> validation
    -> promotion as the region's active generation
    -> workout readiness checks and versioned matching
    -> durable Coverage, map overlays and visit statistics
```

### 1. Choose and Download a Region

The administrator's Regions page lists regional extracts discovered from the
configured providers. **Download region** obtains data for an unprocessed region;
**Reprocess region** builds a replacement for an already processed one.

Both operations download the provider's current PBF file. Reprocessing is not
simply a new matcher pass over the existing map tables. The source URL, content
hash, source timestamp and tool/processing versions identify the build's inputs.

A resumable job is different: **Resume processing** continues retained, compatible
checkpoints and does not repeat a completed source import. Canceling an
unpromoted build discards that candidate; deleting a region removes its active
map data and is a separate operation.

### 2. Keep the Relevant OSM Features

OSM represents points as **nodes**, ordered lines or closed outlines as **ways**,
and related features or complex areas as **relations**. Regional downloads package
these objects in a compact binary PBF file. The updater uses `osmium` to filter
that file and `osm2pgsql` with the Lua import definitions to load the selected
features into PostgreSQL/PostGIS.

The importer selects public map features needed by Coverage, retaining their
necessary node and relation references:

| Data | How the app uses it |
| --- | --- |
| Highway ways and ordered nodes | Road/path geometry, exact connections, direction and source lineage. |
| Road and access tags | Names, road classes, service subtypes, one-way information and movement-mode interpretation. |
| Administrative boundaries | Municipality/county attribution and grouping scope. |
| Parks and protected areas | Park attribution and display context. |
| Educational grounds | Shared campus identity for otherwise unnamed internal roads and paths. |
| Building footprints, residential land use, addresses and entrances | Evidence that an access road serves multiple homes rather than a single property. |

The PBF is a public map input, not a container for workout data. Private GPS and
durable workout Coverage live in the application database. Map-processing jobs
do not import private workout routes into the OSM database.

### 3. Build a Usable Road and Path Network

Processing runs in a candidate schema, leaving the previously promoted map in
service. It derives connected physical segments from source ways, splits geometry
where attribution boundaries require it, and assigns stable logical identities.

These distinctions matter:

- Sharing a name does not automatically join disconnected streets.
- Unnamed ways do not all become one street or path.
- Road/path class, exact connectivity, locality and park/campus scope affect
  attribution and grouping.
- A way crossing an area boundary contributes only the appropriate inside
  pieces; its entire length is not assigned to that area.
- OSM source IDs, source versions and segment lineage remain attached to the
  derived geometry.

Residential processing compiles public frontage/unit evidence into shared-access
classification. Garages, sheds and other non-residential structures must not
inflate the home count. Duplicate addresses and relation-member footprints are
deduplicated. Ambiguous access roads remain unclassified rather than being
accepted merely because they are long, named or marked private. Raw context
scratch is removed once its derived evidence has been recorded.

Expensive work uses bounded, checkpointed units. When storage monitoring is
configured and enabled, checks surround those units. A failed or paused candidate
can retain completed work without replacing the active map.

### 4. Validate and Promote

Validation checks geometry, source lineage, boundary attribution, logical
identities and required indexes. A successful candidate is promoted atomically
as the region's new active generation. Failed candidates do not replace the
previously usable generation.

Retired storage is cleaned up through the managed lifecycle. Maintenance and
cleanup are part of processing; a successful SQL derivation alone is not the
same as a fully completed region update.

### 5. Match Workouts Against Compatible Map Data

Matching is a separate operation from regional processing. It uses workout GPS
and an observed, compatible map-generation snapshot to determine positive-length
traversals. It should not color an entire street just because a route passed its
entrance or touched one point.

The basic policy includes these distinctions:

- Ordinary roads and independently useful paths can contribute Coverage for
  appropriate workout movement modes.
- Shared neighborhood access roads can count whether named or unnamed. Private
  ownership alone does not make them individual-house driveways.
- Individual-house driveways remain excluded; shared-access classification needs
  evidence rather than a name or access tag alone.
- Genuinely traversed parking aisles can appear as covered geometry. An aisle
  without a formal name stays unnamed and follows the Road Coverage table's
  default unnamed-row hiding.
- Sidewalks, crossings and short connectors may help interpret a continuous
  route without being counted as a second copy of the adjacent road.
- Stationary jitter, nearby parallel streets, unsupported shortcuts and gaps in
  the recording must not create travelled geometry.

A matcher policy can require data introduced by a newer importer or derivation
contract. Work for that policy must wait until a compatible regional generation
is **promoted**; an unfinished candidate is not sufficient. Existing valid
Coverage remains usable while updated maps and replacement matches are pending.

Successful matching stores copied segment metadata and covered geometry in the
application database, along with its input, map and policy provenance. Display
uses the matcher's stored classification for current results. Date/workout scope,
grouping, tile clipping and unnamed-row visibility remain display concerns.
Retained older results keep their compatibility behavior until rematched.

## Generations and Versions

These values describe different things and are not interchangeable:

| Term | Meaning | When it changes |
| --- | --- | --- |
| **Region generation** | The numbered, successfully promoted map edition for one region. This is the Generation value shown in the administrator's Regions table. | Each successful promotion advances that region's number. Failed attempts do not consume a promoted generation number. |
| **Internal build ID** | The database identifier allocated to a processing attempt, before it succeeds. It identifies the candidate schema and its checkpoints. | A fresh build receives a new ID, including builds that later fail or are canceled. Resuming a retained build keeps its ID. |
| **Importer version** | The extraction/input contract: what the downloaded PBF retains and how that public source context is prepared for later processing. | Changes to required source features or their import/context contract. |
| **Derivation version** | The processing contract for turning imported ways and boundaries into the physical matching network, attribution and logical identities. | Changes to those derivation rules. |
| **Matcher policy version** | The rules used to interpret workout movement against map data and decide which traversals receive Coverage. | Changes to matching eligibility or traversal interpretation. |

For example, several regions can have the same generation number while using
different importer versions. A rebuild can advance a region's generation without
changing either processing version. An importer update can add required context
without changing physical segment derivation. A matcher-only change can require
rematching workouts without requiring another PBF download.

The internal build ID is globally allocated; the displayed generation number is
region-specific. Do not substitute one for the other when inspecting jobs.

## Ready, Stale and Pending Work

- **Ready** means a promoted map meets current map-processing requirements.
- **Stale** means the promoted map uses an older required importer/derivation
  contract. It is still usable, but needs reprocessing for the newer contract.
- A retained job can instead show Downloading, Processing, Failed, Paused or
  Canceled. That operation status takes precedence until it finishes or is
  cleared; it does not erase an existing promoted map.
- A matcher-only policy change affects workout Coverage currentness, not the
  region's processing status, unless it also introduces new map-data requirements.

Publishing a new application image does not rewrite processed maps or relabel old
workout results. Compatible regional processing and versioned workout matching
are explicit steps. Checkpoints also retain exact processing fences: a resume
must preserve the interpretation under which its completed work was produced.

## Files and Operating Instructions

| Path | Purpose |
| --- | --- |
| `import.lua` | Core road/boundary extraction and compatibility entry point for retained older builds. |
| `import-residential.lua` | Extends extraction with residential context for fresh builds. |
| `batches/` | Postprocessing, context classification, derivation, clipping, attribution, identity, partition and validation SQL. |
| `manual-update-job.yaml` | Optional operator-rendered wrapper for the same updater used by controller-managed jobs. |
| `manual-identity-eval-job.yaml` | Optional identity/attribution evaluation without promoting a replacement map. |
| `testdata/` | Small synthetic regression inputs. |
| `Containerfile` | Updater/evaluator image packaging. |

Controller-managed jobs use the application's admission, progress, pause/resume,
cancellation and audit workflow. For standalone operations,
[`scripts/osm-update-operator.sh`](../scripts/osm-update-operator.sh) renders the
manual Job template with `envsubst`; the Go updater does not read that YAML.

Further reading:

- [Region-update lifecycle and operations](../docs/osm-region-updates.md)
- [Provider catalog discovery](../docs/osm-catalog-refresh.md)
- [Read replicas and snapshot freshness](../docs/osm-read-replicas.md)
- [Coverage readiness and stale results](../docs/coverage-readiness.md)
- [Matching policies and cross-testing](../docs/decisions/009-coverage-matching-rules.md)
- [Application database contracts](../docs/database-migrations.md)
