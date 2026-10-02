-- Run after locality clipping. This is the only step that changes candidate
-- tables into attachable schema-4 leaves.
SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);
SELECT set_config('workouts_explorer.osm_generation_id', :'OSM_GENERATION_ID', false);
SELECT set_config('workouts_explorer.osm_build_schema', :'OSM_BUILD_SCHEMA', false);

\echo 'OSM progress: converting rewritten candidate segments to logged storage'
ALTER TABLE :"OSM_BUILD_SCHEMA".path_segments SET LOGGED;

DO $prepare$
DECLARE
    table_name name;
    constraint_name text;
    generation_id bigint := current_setting('workouts_explorer.osm_generation_id')::bigint;
    region_id text := current_setting('workouts_explorer.osm_region_id');
    build_schema name := current_setting('workouts_explorer.osm_build_schema')::name;
BEGIN
    IF build_schema::text <> 'osm_build_' || generation_id THEN
        RAISE EXCEPTION 'build schema % does not match generation %', build_schema, generation_id;
    END IF;

    FOREACH table_name IN ARRAY ARRAY['ways'::name, 'localities'::name, 'path_segments'::name] LOOP
        EXECUTE format('ALTER TABLE %I.%I ADD COLUMN region_id text NOT NULL DEFAULT %L', build_schema, table_name, region_id);
        EXECUTE format('ALTER TABLE %I.%I ADD COLUMN generation_id bigint NOT NULL DEFAULT %s', build_schema, table_name, generation_id);
        EXECUTE format('ALTER TABLE %I.%I ALTER COLUMN region_id DROP DEFAULT', build_schema, table_name);
        EXECUTE format('ALTER TABLE %I.%I ALTER COLUMN generation_id DROP DEFAULT', build_schema, table_name);

        FOR constraint_name IN
            SELECT conname FROM pg_constraint
            WHERE conrelid = format('%I.%I', build_schema, table_name)::regclass
              AND contype IN ('p', 'u')
        LOOP
            EXECUTE format('ALTER TABLE %I.%I DROP CONSTRAINT %I', build_schema, table_name, constraint_name);
        END LOOP;
        FOR constraint_name IN
            SELECT indexrelid::regclass::text::name
            FROM pg_index
            WHERE indrelid = format('%I.%I', build_schema, table_name)::regclass
              AND NOT indisprimary
              AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conindid = indexrelid)
        LOOP
            EXECUTE format('DROP INDEX %s', constraint_name);
        END LOOP;
        EXECUTE format('ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK(region_id=%L) NOT VALID',
            build_schema, table_name, table_name || '_region_g' || generation_id, region_id);
        EXECUTE format('ALTER TABLE %I.%I VALIDATE CONSTRAINT %I',
            build_schema, table_name, table_name || '_region_g' || generation_id);
        EXECUTE format('ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK(generation_id=%s) NOT VALID',
            build_schema, table_name, table_name || '_generation_g' || generation_id, generation_id);
        EXECUTE format('ALTER TABLE %I.%I VALIDATE CONSTRAINT %I',
            build_schema, table_name, table_name || '_generation_g' || generation_id);
    END LOOP;

    EXECUTE format('ALTER TABLE %I.ways ALTER COLUMN version SET NOT NULL, ALTER COLUMN tags SET NOT NULL, ALTER COLUMN node_ids SET NOT NULL, ALTER COLUMN geom SET NOT NULL', build_schema);
    EXECUTE format('ALTER TABLE %I.localities ALTER COLUMN relation_version SET NOT NULL, ALTER COLUMN tags SET NOT NULL, ALTER COLUMN geom SET NOT NULL', build_schema);

    EXECUTE format('ALTER TABLE %I.ways ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,way_id)',build_schema,'ways_pkey_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.localities ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,relation_id)',build_schema,'localities_pkey_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.path_segments ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,segment_id)',build_schema,'path_segments_pkey_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.path_segments ADD CONSTRAINT %I UNIQUE(region_id,generation_id,source_way_id,source_way_version,start_node_index,end_node_index,boundary_piece)',build_schema,'path_segments_source_piece_g'||generation_id);

    EXECUTE format('CREATE INDEX %I ON %I.ways(way_id,version DESC,generation_id DESC,region_id DESC)','ways_identity_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.ways USING gist((geom::geography))','ways_geography_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.localities(relation_id,relation_version DESC,generation_id DESC,region_id DESC)','localities_identity_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.localities USING gist(geom)','localities_geom_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments(segment_id,generation_id DESC,region_id DESC)','path_segments_identity_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments(source_way_id,source_way_version,generation_id DESC,region_id DESC)','path_segments_source_way_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments USING gist((geom::geography))','path_segments_geography_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments(logical_path_id)','path_segments_logical_path_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments(start_graph_node_id)','path_segments_start_graph_node_g'||generation_id,build_schema);
    EXECUTE format('CREATE INDEX %I ON %I.path_segments(end_graph_node_id)','path_segments_end_graph_node_g'||generation_id,build_schema);

    EXECUTE format('ALTER TABLE %I.ways RENAME TO %I',build_schema,'ways_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.localities RENAME TO %I',build_schema,'localities_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.path_segments RENAME TO %I',build_schema,'path_segments_g'||generation_id);
END
$prepare$;
