# OSM Import Spike

Milestone 7 uses a full-refresh, selective import. The source PBF is filtered
with Osmium to `highway=*` ways, administrative boundary relations, and their
required references before `osm2pgsql` flexible output loads a generation-specific
candidate schema.

The current assets implement the measured NorCal import and graph derivation.
Matcher thresholds remain pending ADR 0009 experiments.

Pinned tools:

- Osmium 1.19.0
- osm2pgsql 2.3.1

The candidate schema name must match `osm_build_<generation>` and is supplied
in `OSM_BUILD_SCHEMA`. Promotion is never performed by the importer itself.

`postprocess.sql` adds the meter-based geography index required by matching and
derives OSM administrative-level-8 municipal locality polygons. `validate.sql`
records the candidate's provenance, geometry, locality, and storage gates.
`promote.sql` atomically advances catalog state and stable active views only
after the caller has accepted those validation results.

`derive-compact.sql` builds the physical graph in bounded source-way batches to
avoid global sort, temporary-file, and shared-memory spikes. `clip-localities.sql`
rewrites only municipal-boundary candidates at ordered source-line fractions.
Pieces that cannot be covered by one locality within 1 cm retain matchable
geometry but abstain from municipal attribution.
