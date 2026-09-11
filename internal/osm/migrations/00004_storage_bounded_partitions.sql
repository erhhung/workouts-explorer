-- +goose Up
DROP FUNCTION osm_catalog.promote_region_generation(text, bigint, name, jsonb);

CREATE TABLE osm_catalog.region_storage (
    region_id text PRIMARY KEY REFERENCES osm_catalog.regions (id),
    generation_id bigint NOT NULL,
    ways_relation name NOT NULL,
    localities_relation name NOT NULL,
    path_segments_relation name NOT NULL,
    build_schema name NOT NULL,
    migration_bootstrap boolean NOT NULL DEFAULT false,
    recorded_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (generation_id),
    FOREIGN KEY (region_id, generation_id)
        REFERENCES osm_catalog.generations (region_id, id)
);

CREATE TABLE osm_catalog.storage_gc (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region_id text NOT NULL REFERENCES osm_catalog.regions (id),
    generation_id bigint NOT NULL REFERENCES osm_catalog.generations (id),
    object_kind text NOT NULL CHECK (object_kind IN ('relation', 'build_schema')),
    schema_name name NOT NULL,
    object_name name,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'dropping', 'dropped', 'failed')),
    queued_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    finished_at timestamptz,
    failure_summary text CHECK (failure_summary IS NULL OR length(failure_summary) <= 512),
    UNIQUE NULLS NOT DISTINCT (object_kind, schema_name, object_name),
    CHECK ((object_kind = 'relation') = (object_name IS NOT NULL)),
    CHECK ((state = 'dropped') = (finished_at IS NOT NULL))
);
CREATE INDEX storage_gc_queued_idx ON osm_catalog.storage_gc (id) WHERE state = 'queued';

-- Convert the expected deployed schema-3 heap in place. ATTACH PARTITION only
-- validates bounds; it does not rewrite or copy tuples.
-- +goose StatementBegin
DO $convert$
DECLARE
    active_count integer;
    active_region text;
    active_generation bigint;
    active_schema name;
    canonical_rows boolean;
    suffix text;
BEGIN
    SELECT EXISTS (SELECT 1 FROM osm_canonical.ways)
        OR EXISTS (SELECT 1 FROM osm_canonical.localities)
        OR EXISTS (SELECT 1 FROM osm_canonical.path_segments)
    INTO canonical_rows;

    SELECT count(*), min(region_id), min(id), min(schema_name::text)::name
    INTO active_count, active_region, active_generation, active_schema
    FROM osm_catalog.generations
    WHERE state = 'active';

    IF canonical_rows AND active_count <> 1 THEN
        RAISE EXCEPTION 'schema-4 preflight requires exactly one active region for nonempty schema-3 canonical heaps; found %', active_count;
    END IF;
    IF active_count > 1 THEN
        RAISE EXCEPTION 'schema-4 preflight supports at most one deployed schema-3 active region; found %', active_count;
    END IF;
    IF active_count = 1 AND (
        EXISTS (SELECT 1 FROM osm_canonical.ways WHERE region_id <> active_region OR generation_id <> active_generation)
        OR EXISTS (SELECT 1 FROM osm_canonical.localities WHERE region_id <> active_region OR generation_id <> active_generation)
        OR EXISTS (SELECT 1 FROM osm_canonical.path_segments WHERE region_id <> active_region OR generation_id <> active_generation)
    ) THEN
        RAISE EXCEPTION 'schema-3 canonical rows do not all match active region % generation %', active_region, active_generation;
    END IF;

    DROP VIEW osm_active.logical_paths;
    DROP VIEW osm_active.path_segments;
    DROP VIEW osm_active.localities;
    DROP VIEW osm_active.ways;

    IF active_count = 0 THEN
        DROP TABLE osm_canonical.path_segments;
        DROP TABLE osm_canonical.localities;
        DROP TABLE osm_canonical.ways;
    ELSE
        suffix := '_g' || active_generation;
        ALTER TABLE osm_canonical.path_segments
            DROP CONSTRAINT IF EXISTS path_segments_region_id_generation_id_source_way_id_fkey,
            DROP CONSTRAINT IF EXISTS path_segments_region_id_generation_id_fkey;
        ALTER TABLE osm_canonical.localities
            DROP CONSTRAINT IF EXISTS localities_region_id_generation_id_fkey;
        ALTER TABLE osm_canonical.ways
            DROP CONSTRAINT IF EXISTS ways_region_id_generation_id_fkey;
        ALTER TABLE osm_canonical.ways RENAME TO ways_bootstrap;
        ALTER TABLE osm_canonical.localities RENAME TO localities_bootstrap;
        ALTER TABLE osm_canonical.path_segments RENAME TO path_segments_bootstrap;
        EXECUTE format('ALTER TABLE osm_canonical.ways_bootstrap RENAME TO %I', 'ways' || suffix);
        EXECUTE format('ALTER TABLE osm_canonical.localities_bootstrap RENAME TO %I', 'localities' || suffix);
        EXECUTE format('ALTER TABLE osm_canonical.path_segments_bootstrap RENAME TO %I', 'path_segments' || suffix);

        FOR suffix IN SELECT unnest(ARRAY[
            'ways_pkey', 'localities_pkey', 'path_segments_pkey',
            'canonical_ways_identity_idx', 'canonical_ways_geography_gist',
            'canonical_localities_identity_idx', 'canonical_localities_geom_gist',
            'canonical_path_segments_identity_idx', 'canonical_path_segments_source_way_idx',
            'canonical_path_segments_geography_gist', 'canonical_path_segments_logical_path_idx'
        ]) LOOP
            IF to_regclass('osm_canonical.' || suffix) IS NOT NULL THEN
                EXECUTE format('ALTER INDEX osm_canonical.%I RENAME TO %I', suffix, suffix || '_g' || active_generation);
            END IF;
        END LOOP;
    END IF;

    CREATE TABLE osm_canonical.ways (
        region_id text NOT NULL, generation_id bigint NOT NULL, way_id bigint NOT NULL,
        version integer NOT NULL, osm_timestamp text, tags jsonb NOT NULL,
        node_ids jsonb NOT NULL, geom geometry(LineString, 4326) NOT NULL,
        PRIMARY KEY (region_id, generation_id, way_id)
    ) PARTITION BY LIST (region_id);
    CREATE INDEX canonical_ways_identity_idx ON osm_canonical.ways
        (way_id, version DESC, generation_id DESC, region_id DESC);
    CREATE INDEX canonical_ways_geography_gist ON osm_canonical.ways USING gist ((geom::geography));

    CREATE TABLE osm_canonical.localities (
        region_id text NOT NULL, generation_id bigint NOT NULL, relation_id bigint NOT NULL,
        relation_version integer NOT NULL, admin_level smallint, name text,
        normalized_name text, tags jsonb NOT NULL,
        geom geometry(MultiPolygon, 4326) NOT NULL,
        PRIMARY KEY (region_id, generation_id, relation_id)
    ) PARTITION BY LIST (region_id);
    CREATE INDEX canonical_localities_identity_idx ON osm_canonical.localities
        (relation_id, relation_version DESC, generation_id DESC, region_id DESC);
    CREATE INDEX canonical_localities_geom_gist ON osm_canonical.localities USING gist (geom);

    CREATE TABLE osm_canonical.path_segments (
        region_id text NOT NULL, generation_id bigint NOT NULL, segment_id uuid NOT NULL,
        source_way_id bigint NOT NULL, source_way_version integer NOT NULL,
        derivation_version integer NOT NULL, start_node_index integer NOT NULL,
        end_node_index integer NOT NULL, boundary_piece integer NOT NULL,
        start_graph_node_id uuid NOT NULL, end_graph_node_id uuid NOT NULL,
        name text, normalized_name text, highway text NOT NULL, broad_class text NOT NULL,
        tags jsonb NOT NULL, motor_forward_allowed boolean NOT NULL,
        motor_reverse_allowed boolean NOT NULL, geom geometry(LineString, 4326) NOT NULL,
        locality_relation_id bigint, logical_path_id uuid NOT NULL,
        length_m double precision NOT NULL,
        PRIMARY KEY (region_id, generation_id, segment_id)
    ) PARTITION BY LIST (region_id);
    CREATE INDEX canonical_path_segments_identity_idx ON osm_canonical.path_segments
        (segment_id, generation_id DESC, region_id DESC);
    CREATE INDEX canonical_path_segments_source_way_idx ON osm_canonical.path_segments
        (source_way_id, source_way_version, generation_id DESC, region_id DESC);
    CREATE INDEX canonical_path_segments_geography_gist ON osm_canonical.path_segments USING gist ((geom::geography));
    CREATE INDEX canonical_path_segments_logical_path_idx ON osm_canonical.path_segments (logical_path_id);

    IF active_count = 1 THEN
        EXECUTE format('ALTER TABLE osm_canonical.%I ADD CONSTRAINT %I CHECK (region_id = %L) NOT VALID',
            'ways_g' || active_generation, 'ways_region_g' || active_generation, active_region);
        EXECUTE format('ALTER TABLE osm_canonical.%I VALIDATE CONSTRAINT %I',
            'ways_g' || active_generation, 'ways_region_g' || active_generation);
        EXECUTE format('ALTER TABLE osm_canonical.%I ADD CONSTRAINT %I CHECK (region_id = %L) NOT VALID',
            'localities_g' || active_generation, 'localities_region_g' || active_generation, active_region);
        EXECUTE format('ALTER TABLE osm_canonical.%I VALIDATE CONSTRAINT %I',
            'localities_g' || active_generation, 'localities_region_g' || active_generation);
        EXECUTE format('ALTER TABLE osm_canonical.%I ADD CONSTRAINT %I CHECK (region_id = %L) NOT VALID',
            'path_segments_g' || active_generation, 'path_segments_region_g' || active_generation, active_region);
        EXECUTE format('ALTER TABLE osm_canonical.%I VALIDATE CONSTRAINT %I',
            'path_segments_g' || active_generation, 'path_segments_region_g' || active_generation);

        EXECUTE format('ALTER TABLE osm_canonical.ways ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)',
            'ways_g' || active_generation, active_region);
        EXECUTE format('ALTER TABLE osm_canonical.localities ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)',
            'localities_g' || active_generation, active_region);
        EXECUTE format('ALTER TABLE osm_canonical.path_segments ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)',
            'path_segments_g' || active_generation, active_region);

        INSERT INTO osm_catalog.region_storage (
            region_id, generation_id, ways_relation, localities_relation,
            path_segments_relation, build_schema, migration_bootstrap
        ) VALUES (
            active_region, active_generation, ('ways_g' || active_generation)::name,
            ('localities_g' || active_generation)::name,
            ('path_segments_g' || active_generation)::name, active_schema, true
        );
    END IF;
END
$convert$;
-- +goose StatementEnd

-- These definitions intentionally preserve the schema-3 reader columns and
-- anti-join precedence exactly; only the canonical storage is partitioned.
CREATE VIEW osm_active.ways AS
SELECT contribution.way_id, contribution.version, contribution.osm_timestamp,
    contribution.tags, contribution.node_ids, contribution.geom
FROM osm_canonical.ways AS contribution
JOIN osm_catalog.generations AS generation
  ON generation.id = contribution.generation_id
 AND generation.region_id = contribution.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1 FROM osm_canonical.ways AS preferred
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred.generation_id
     AND preferred_generation.region_id = preferred.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred.way_id = contribution.way_id
      AND (preferred.version,preferred.generation_id,preferred.region_id)
          > (contribution.version,contribution.generation_id,contribution.region_id)
);

CREATE VIEW osm_active.localities AS
SELECT contribution.relation_id, contribution.relation_version,
    contribution.admin_level, contribution.name, contribution.normalized_name,
    contribution.tags, contribution.geom
FROM osm_canonical.localities AS contribution
JOIN osm_catalog.generations AS generation
  ON generation.id = contribution.generation_id
 AND generation.region_id = contribution.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1 FROM osm_canonical.localities AS preferred
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred.generation_id
     AND preferred_generation.region_id = preferred.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred.relation_id = contribution.relation_id
      AND (preferred.relation_version,preferred.generation_id,preferred.region_id)
          > (contribution.relation_version,contribution.generation_id,contribution.region_id)
);

CREATE VIEW osm_active.path_segments AS
SELECT segment.segment_id, segment.source_way_id, segment.source_way_version,
    segment.derivation_version, segment.start_node_index, segment.end_node_index,
    segment.boundary_piece, segment.start_graph_node_id, segment.end_graph_node_id,
    segment.name, segment.normalized_name, segment.highway, segment.broad_class,
    segment.tags, segment.motor_forward_allowed, segment.motor_reverse_allowed,
    segment.geom, segment.locality_relation_id, segment.logical_path_id, segment.length_m
FROM osm_canonical.path_segments AS segment
JOIN osm_catalog.generations AS generation
  ON generation.id = segment.generation_id
 AND generation.region_id = segment.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1 FROM osm_canonical.ways AS preferred_way
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred_way.generation_id
     AND preferred_generation.region_id = preferred_way.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred_way.way_id = segment.source_way_id
      AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
          > (segment.source_way_version,segment.generation_id,segment.region_id)
)
AND NOT EXISTS (
    SELECT 1 FROM osm_canonical.path_segments AS preferred_segment
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred_segment.generation_id
     AND preferred_generation.region_id = preferred_segment.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred_segment.segment_id = segment.segment_id
      AND (preferred_segment.generation_id,preferred_segment.region_id)
          > (segment.generation_id,segment.region_id)
);

CREATE VIEW osm_active.logical_paths AS
SELECT logical_path_id, min(locality_relation_id) AS locality_relation_id,
    min(name) AS name, min(normalized_name) AS normalized_name,
    min(broad_class) AS broad_class, count(*) AS member_segment_count,
    sum(length_m) AS member_length_m
FROM osm_active.path_segments
GROUP BY logical_path_id;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.promote_region_generation(
    promote_region_id text, promote_generation_id bigint,
    promote_schema_name name, promote_validation jsonb
)
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    candidate osm_catalog.generations%ROWTYPE;
    previous osm_catalog.region_storage%ROWTYPE;
    ways_name name := ('ways_g' || promote_generation_id)::name;
    localities_name name := ('localities_g' || promote_generation_id)::name;
    segments_name name := ('path_segments_g' || promote_generation_id)::name;
    relation_name name;
    required_index name;
    way_count bigint;
    segment_count bigint;
    logical_path_count bigint;
    expected_logical_path_count bigint;
    source_mismatch boolean;
    provenance_mismatch boolean;
    logical_path_mismatch boolean;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(promote_region_id, 0));

    SELECT * INTO candidate FROM osm_catalog.generations
    WHERE id = promote_generation_id AND region_id = promote_region_id
      AND schema_name = promote_schema_name AND state IN ('building', 'validating')
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'generation % is not promotable for region % and schema %',
            promote_generation_id, promote_region_id, promote_schema_name;
    END IF;
    IF promote_schema_name::text !~ '^osm_build_[0-9]+$'
       OR promote_schema_name::text <> 'osm_build_' || promote_generation_id
       OR to_regclass(format('%I.%I', promote_schema_name, ways_name)) IS NULL
       OR to_regclass(format('%I.%I', promote_schema_name, localities_name)) IS NULL
       OR to_regclass(format('%I.%I', promote_schema_name, segments_name)) IS NULL
       OR to_regclass(format('%I.logical_paths', promote_schema_name)) IS NULL THEN
        RAISE EXCEPTION 'generation % build schema % is not partition-prepared',
            promote_generation_id, promote_schema_name;
    END IF;
    IF promote_validation IS NULL OR jsonb_typeof(promote_validation) <> 'object'
       OR promote_validation @> jsonb_build_object(
            'partitionPrepared', true,
            'preparedRegionId', promote_region_id,
            'preparedGenerationId', promote_generation_id,
            'importerVersion', candidate.importer_version,
            'derivationVersion', candidate.derivation_version,
            'provenanceMismatches', 0,
            'sourceVersionMismatches', 0,
            'logicalPathMismatches', 0
          ) IS NOT TRUE THEN
        RAISE EXCEPTION 'generation % validation does not match its source versions and preparation', promote_generation_id;
    END IF;

    FOREACH relation_name IN ARRAY ARRAY[ways_name, localities_name, segments_name] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conrelid = format('%I.%I', promote_schema_name, relation_name)::regclass
              AND contype = 'c' AND convalidated
              AND pg_get_constraintdef(oid) LIKE '%region_id%'
        ) OR NOT EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conrelid = format('%I.%I', promote_schema_name, relation_name)::regclass
              AND contype = 'c' AND convalidated
              AND pg_get_constraintdef(oid) LIKE '%generation_id%'
        ) THEN
            RAISE EXCEPTION 'candidate relation %.% lacks validated provenance constraints',
                promote_schema_name, relation_name;
        END IF;
    END LOOP;
    FOREACH required_index IN ARRAY ARRAY[
        ('ways_pkey_g'||promote_generation_id)::name,
        ('ways_identity_g'||promote_generation_id)::name,
        ('ways_geography_g'||promote_generation_id)::name,
        ('localities_pkey_g'||promote_generation_id)::name,
        ('localities_identity_g'||promote_generation_id)::name,
        ('localities_geom_g'||promote_generation_id)::name,
        ('path_segments_pkey_g'||promote_generation_id)::name,
        ('path_segments_identity_g'||promote_generation_id)::name,
        ('path_segments_source_way_g'||promote_generation_id)::name,
        ('path_segments_geography_g'||promote_generation_id)::name,
        ('path_segments_logical_path_g'||promote_generation_id)::name,
        ('path_segments_start_graph_node_g'||promote_generation_id)::name,
        ('path_segments_end_graph_node_g'||promote_generation_id)::name
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_index
            WHERE indexrelid=to_regclass(format('%I.%I',promote_schema_name,required_index))
              AND indisvalid AND indisready
        ) THEN
            RAISE EXCEPTION 'candidate generation % lacks required valid index %',
                promote_generation_id, required_index;
        END IF;
    END LOOP;

    EXECUTE format(
        'SELECT EXISTS (
           SELECT 1 FROM %1$I.%2$I WHERE region_id<>$1 OR generation_id<>$2
           UNION ALL SELECT 1 FROM %1$I.%3$I WHERE region_id<>$1 OR generation_id<>$2
           UNION ALL SELECT 1 FROM %1$I.%4$I WHERE region_id<>$1 OR generation_id<>$2
         )',promote_schema_name,ways_name,localities_name,segments_name)
    INTO provenance_mismatch USING promote_region_id,promote_generation_id;
    IF provenance_mismatch THEN
        RAISE EXCEPTION 'generation % candidate provenance is not fixed to region %',
            promote_generation_id,promote_region_id;
    END IF;

    EXECUTE format('SELECT count(*) FROM %I.%I', promote_schema_name, ways_name) INTO way_count;
    EXECUTE format('SELECT count(*) FROM %I.%I', promote_schema_name, segments_name) INTO segment_count;
    IF way_count = 0 OR segment_count = 0 THEN
        RAISE EXCEPTION 'generation % is empty: % ways, % path segments', promote_generation_id, way_count, segment_count;
    END IF;
    EXECUTE format(
        'SELECT EXISTS (
           SELECT 1 FROM %1$I.%2$I segment
           LEFT JOIN %1$I.%3$I way
             ON way.region_id=segment.region_id AND way.generation_id=segment.generation_id
            AND way.way_id=segment.source_way_id AND way.version=segment.source_way_version
           WHERE way.way_id IS NULL OR segment.derivation_version <> $1
         )', promote_schema_name, segments_name, ways_name)
    INTO STRICT source_mismatch USING candidate.derivation_version;
    IF source_mismatch THEN
        RAISE EXCEPTION 'generation % has path segments without matching source way versions', promote_generation_id;
    END IF;
    EXECUTE format('SELECT count(DISTINCT logical_path_id) FROM %I.%I', promote_schema_name, segments_name)
        INTO logical_path_count;
    EXECUTE format('SELECT count(*) FROM %I.logical_paths', promote_schema_name)
        INTO expected_logical_path_count;
    IF logical_path_count <> expected_logical_path_count THEN
        RAISE EXCEPTION 'generation % logical path count does not match prepared segments', promote_generation_id;
    END IF;
    EXECUTE format(
        'SELECT EXISTS (
           SELECT 1 FROM (
               SELECT logical_path_id,min(locality_relation_id) locality_relation_id,
                      min(name) name,min(normalized_name) normalized_name,
                      min(broad_class) broad_class,count(*) member_segment_count,
                      sum(length_m) member_length_m
               FROM %1$I.%2$I GROUP BY logical_path_id
           ) derived
           FULL JOIN %1$I.logical_paths stored USING (logical_path_id)
           WHERE derived.logical_path_id IS NULL OR stored.logical_path_id IS NULL
              OR derived.locality_relation_id IS DISTINCT FROM stored.locality_relation_id
              OR derived.name IS DISTINCT FROM stored.name
              OR derived.normalized_name IS DISTINCT FROM stored.normalized_name
              OR derived.broad_class IS DISTINCT FROM stored.broad_class
              OR derived.member_segment_count <> stored.member_segment_count
              OR abs(derived.member_length_m-stored.member_length_m) > 0.01
          )',promote_schema_name,segments_name)
    INTO logical_path_mismatch;
    IF logical_path_mismatch THEN
        RAISE EXCEPTION 'generation % logical paths do not equal prepared segment aggregates', promote_generation_id;
    END IF;

    SELECT * INTO previous FROM osm_catalog.region_storage
    WHERE region_id = promote_region_id FOR UPDATE;
    IF FOUND THEN
        EXECUTE format('ALTER TABLE osm_canonical.path_segments DETACH PARTITION osm_canonical.%I', previous.path_segments_relation);
        EXECUTE format('ALTER TABLE osm_canonical.localities DETACH PARTITION osm_canonical.%I', previous.localities_relation);
        EXECUTE format('ALTER TABLE osm_canonical.ways DETACH PARTITION osm_canonical.%I', previous.ways_relation);
    END IF;

    EXECUTE format('ALTER TABLE %I.%I SET SCHEMA osm_canonical', promote_schema_name, ways_name);
    EXECUTE format('ALTER TABLE %I.%I SET SCHEMA osm_canonical', promote_schema_name, localities_name);
    EXECUTE format('ALTER TABLE %I.%I SET SCHEMA osm_canonical', promote_schema_name, segments_name);
    EXECUTE format('ALTER TABLE osm_canonical.ways ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)', ways_name, promote_region_id);
    EXECUTE format('ALTER TABLE osm_canonical.localities ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)', localities_name, promote_region_id);
    EXECUTE format('ALTER TABLE osm_canonical.path_segments ATTACH PARTITION osm_canonical.%I FOR VALUES IN (%L)', segments_name, promote_region_id);

    UPDATE osm_catalog.generations SET state='retired', retired_at=transaction_timestamp()
    WHERE region_id=promote_region_id AND state='active';
    UPDATE osm_catalog.generations
    SET state='active', validation=promote_validation,
        validated_at=transaction_timestamp(), promoted_at=transaction_timestamp(), retired_at=NULL
    WHERE id=promote_generation_id;

    IF previous.region_id IS NOT NULL THEN
        INSERT INTO osm_catalog.storage_gc(region_id,generation_id,object_kind,schema_name,object_name)
        VALUES
            (previous.region_id,previous.generation_id,'relation','osm_canonical',previous.path_segments_relation),
            (previous.region_id,previous.generation_id,'relation','osm_canonical',previous.localities_relation),
            (previous.region_id,previous.generation_id,'relation','osm_canonical',previous.ways_relation),
            (previous.region_id,previous.generation_id,'build_schema',previous.build_schema,NULL)
        ON CONFLICT (object_kind,schema_name,object_name) DO NOTHING;
    END IF;
    INSERT INTO osm_catalog.storage_gc(region_id,generation_id,object_kind,schema_name,object_name)
    VALUES(promote_region_id,promote_generation_id,'build_schema',promote_schema_name,NULL)
    ON CONFLICT (object_kind,schema_name,object_name) DO NOTHING;

    INSERT INTO osm_catalog.region_storage(
        region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema,migration_bootstrap
    ) VALUES(promote_region_id,promote_generation_id,ways_name,localities_name,segments_name,promote_schema_name,false)
    ON CONFLICT (region_id) DO UPDATE SET
        generation_id=EXCLUDED.generation_id, ways_relation=EXCLUDED.ways_relation,
        localities_relation=EXCLUDED.localities_relation,
        path_segments_relation=EXCLUDED.path_segments_relation,
        build_schema=EXCLUDED.build_schema, migration_bootstrap=false,
        recorded_at=transaction_timestamp();
END;
$function$;
-- +goose StatementEnd

UPDATE osm_catalog.schema_metadata SET schema_version = 4 WHERE singleton;
REVOKE ALL ON TABLE osm_catalog.region_storage FROM PUBLIC;
REVOKE ALL ON TABLE osm_catalog.storage_gc FROM PUBLIC;
REVOKE ALL ON SEQUENCE osm_catalog.storage_gc_id_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb) FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
DO $refuse$
BEGIN
    IF EXISTS (SELECT 1 FROM osm_catalog.storage_gc WHERE object_kind = 'relation') THEN
        RAISE EXCEPTION 'cannot downgrade schema 4 after the first partition replacement; retired leaves are queued for GC';
    END IF;
    IF (SELECT count(*) FROM osm_catalog.region_storage) > 1 THEN
        RAISE EXCEPTION 'cannot downgrade schema 4 with multiple stored regions';
    END IF;
END
$refuse$;
-- +goose StatementEnd

DROP VIEW osm_active.logical_paths;
DROP VIEW osm_active.path_segments;
DROP VIEW osm_active.localities;
DROP VIEW osm_active.ways;
DROP FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb);

-- +goose StatementBegin
DO $detach$
DECLARE storage osm_catalog.region_storage%ROWTYPE;
BEGIN
    SELECT * INTO storage FROM osm_catalog.region_storage;
    IF FOUND THEN
        EXECUTE format('ALTER TABLE osm_canonical.path_segments DETACH PARTITION osm_canonical.%I',storage.path_segments_relation);
        EXECUTE format('ALTER TABLE osm_canonical.localities DETACH PARTITION osm_canonical.%I',storage.localities_relation);
        EXECUTE format('ALTER TABLE osm_canonical.ways DETACH PARTITION osm_canonical.%I',storage.ways_relation);
    END IF;
END
$detach$;
-- +goose StatementEnd

DROP TABLE osm_canonical.path_segments;
DROP TABLE osm_canonical.localities;
DROP TABLE osm_canonical.ways;

-- Reuse the sole current leaves as schema-3 heaps. IF NOT EXISTS creates empty
-- heaps only for a fresh database; neither path copies tuples.
-- +goose StatementBegin
DO $rename$
DECLARE storage osm_catalog.region_storage%ROWTYPE;
BEGIN
    SELECT * INTO storage FROM osm_catalog.region_storage;
    IF FOUND THEN
        EXECUTE format('ALTER TABLE osm_canonical.%I RENAME TO ways',storage.ways_relation);
        EXECUTE format('ALTER TABLE osm_canonical.%I RENAME TO localities',storage.localities_relation);
        EXECUTE format('ALTER TABLE osm_canonical.%I RENAME TO path_segments',storage.path_segments_relation);
    END IF;
END
$rename$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS osm_canonical.ways (
    region_id text NOT NULL, generation_id bigint NOT NULL, way_id bigint NOT NULL,
    version integer NOT NULL, osm_timestamp text, tags jsonb NOT NULL,
    node_ids jsonb NOT NULL, geom geometry(LineString,4326) NOT NULL,
    PRIMARY KEY(region_id,generation_id,way_id),
    FOREIGN KEY(region_id,generation_id) REFERENCES osm_catalog.generations(region_id,id)
);
CREATE TABLE IF NOT EXISTS osm_canonical.localities (
    region_id text NOT NULL, generation_id bigint NOT NULL, relation_id bigint NOT NULL,
    relation_version integer NOT NULL, admin_level smallint, name text, normalized_name text,
    tags jsonb NOT NULL, geom geometry(MultiPolygon,4326) NOT NULL,
    PRIMARY KEY(region_id,generation_id,relation_id),
    FOREIGN KEY(region_id,generation_id) REFERENCES osm_catalog.generations(region_id,id)
);
CREATE TABLE IF NOT EXISTS osm_canonical.path_segments (
    region_id text NOT NULL, generation_id bigint NOT NULL, segment_id uuid NOT NULL,
    source_way_id bigint NOT NULL, source_way_version integer NOT NULL,
    derivation_version integer NOT NULL, start_node_index integer NOT NULL,
    end_node_index integer NOT NULL, boundary_piece integer NOT NULL,
    start_graph_node_id uuid NOT NULL, end_graph_node_id uuid NOT NULL,
    name text, normalized_name text, highway text NOT NULL, broad_class text NOT NULL,
    tags jsonb NOT NULL, motor_forward_allowed boolean NOT NULL,
    motor_reverse_allowed boolean NOT NULL, geom geometry(LineString,4326) NOT NULL,
    locality_relation_id bigint, logical_path_id uuid NOT NULL, length_m double precision NOT NULL,
    PRIMARY KEY(region_id,generation_id,segment_id),
    FOREIGN KEY(region_id,generation_id) REFERENCES osm_catalog.generations(region_id,id),
    FOREIGN KEY(region_id,generation_id,source_way_id) REFERENCES osm_canonical.ways(region_id,generation_id,way_id)
);
CREATE INDEX IF NOT EXISTS canonical_ways_identity_idx ON osm_canonical.ways(way_id,version DESC,generation_id DESC,region_id DESC);
CREATE INDEX IF NOT EXISTS canonical_ways_geography_gist ON osm_canonical.ways USING gist((geom::geography));
CREATE INDEX IF NOT EXISTS canonical_localities_identity_idx ON osm_canonical.localities(relation_id,relation_version DESC,generation_id DESC,region_id DESC);
CREATE INDEX IF NOT EXISTS canonical_localities_geom_gist ON osm_canonical.localities USING gist(geom);
CREATE INDEX IF NOT EXISTS canonical_path_segments_identity_idx ON osm_canonical.path_segments(segment_id,generation_id DESC,region_id DESC);
CREATE INDEX IF NOT EXISTS canonical_path_segments_source_way_idx ON osm_canonical.path_segments(source_way_id,source_way_version,generation_id DESC,region_id DESC);
CREATE INDEX IF NOT EXISTS canonical_path_segments_geography_gist ON osm_canonical.path_segments USING gist((geom::geography));
CREATE INDEX IF NOT EXISTS canonical_path_segments_logical_path_idx ON osm_canonical.path_segments(logical_path_id);

CREATE VIEW osm_active.ways AS SELECT way_id,version,osm_timestamp,tags,node_ids,geom FROM osm_canonical.ways;
CREATE VIEW osm_active.localities AS SELECT relation_id,relation_version,admin_level,name,normalized_name,tags,geom FROM osm_canonical.localities;
CREATE VIEW osm_active.path_segments AS SELECT segment_id,source_way_id,source_way_version,derivation_version,start_node_index,end_node_index,boundary_piece,start_graph_node_id,end_graph_node_id,name,normalized_name,highway,broad_class,tags,motor_forward_allowed,motor_reverse_allowed,geom,locality_relation_id,logical_path_id,length_m FROM osm_canonical.path_segments;
CREATE VIEW osm_active.logical_paths AS SELECT logical_path_id,min(locality_relation_id) locality_relation_id,min(name) name,min(normalized_name) normalized_name,min(broad_class) broad_class,count(*) member_segment_count,sum(length_m) member_length_m FROM osm_active.path_segments GROUP BY logical_path_id;

-- Schema 3's copying promoter is intentionally not restored. Re-upgrading to
-- schema 4 restores promotion without risking an accidental unbounded copy.
-- +goose StatementBegin
CREATE FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb)
RETURNS void LANGUAGE plpgsql SECURITY INVOKER
SET search_path=pg_catalog,public AS $function$
BEGIN
    RAISE EXCEPTION 'schema-3 promotion is disabled after no-copy schema-4 downgrade; reapply migration 00004';
END
$function$;
-- +goose StatementEnd

DROP TABLE osm_catalog.storage_gc;
DROP TABLE osm_catalog.region_storage;
UPDATE osm_catalog.schema_metadata SET schema_version = 3 WHERE singleton;
