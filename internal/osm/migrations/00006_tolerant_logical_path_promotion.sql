-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION osm_catalog.promote_region_generation(
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

UPDATE osm_catalog.schema_metadata SET schema_version = 6 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION osm_catalog.promote_region_generation(
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
           (SELECT logical_path_id,min(locality_relation_id),min(name),min(normalized_name),
                   min(broad_class),count(*),sum(length_m)
            FROM %1$I.%2$I GROUP BY logical_path_id
            EXCEPT SELECT * FROM %1$I.logical_paths)
           UNION ALL
           (SELECT * FROM %1$I.logical_paths EXCEPT
            SELECT logical_path_id,min(locality_relation_id),min(name),min(normalized_name),
                   min(broad_class),count(*),sum(length_m)
            FROM %1$I.%2$I GROUP BY logical_path_id)
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

UPDATE osm_catalog.schema_metadata SET schema_version = 5 WHERE singleton;
