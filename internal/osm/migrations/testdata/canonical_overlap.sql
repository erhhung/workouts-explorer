-- Execute against a migrated schema-4 scratch database. Everything rolls back.
BEGIN;

INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
VALUES
('fixture:region-a','fixture','region-a','Region A','https://example.test/catalog','https://example.test/a.pbf',ST_Multi(ST_GeomFromText('POLYGON((0 0,2 0,2 2,0 2,0 0))',4326))),
('fixture:region-b','fixture','region-b','Region B','https://example.test/catalog','https://example.test/b.pbf',ST_Multi(ST_GeomFromText('POLYGON((1 0,3 0,3 2,1 2,1 0))',4326)));

CREATE FUNCTION pg_temp.make_candidate(candidate_region text, variant integer)
RETURNS bigint LANGUAGE plpgsql AS $fixture$
DECLARE
    generation_id bigint;
    build_schema name;
    ways_name name;
    localities_name name;
    segments_name name;
BEGIN
    generation_id := nextval(pg_get_serial_sequence('osm_catalog.generations','id'));
    build_schema := ('osm_build_' || generation_id)::name;
    ways_name := ('ways_g' || generation_id)::name;
    localities_name := ('localities_g' || generation_id)::name;
    segments_name := ('path_segments_g' || generation_id)::name;
    INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version)
    OVERRIDING SYSTEM VALUE VALUES(generation_id,candidate_region,'building',build_schema,'https://example.test/source.pbf',1,1);
    EXECUTE format('CREATE SCHEMA %I',build_schema);
    EXECUTE format('CREATE TABLE %I.%I (LIKE osm_canonical.ways INCLUDING DEFAULTS INCLUDING CONSTRAINTS)',build_schema,ways_name);
    EXECUTE format('CREATE TABLE %I.%I (LIKE osm_canonical.localities INCLUDING DEFAULTS INCLUDING CONSTRAINTS)',build_schema,localities_name);
    EXECUTE format('CREATE TABLE %I.%I (LIKE osm_canonical.path_segments INCLUDING DEFAULTS INCLUDING CONSTRAINTS)',build_schema,segments_name);
    EXECUTE format('CREATE TABLE %I.logical_paths AS SELECT * FROM osm_active.logical_paths WITH NO DATA',build_schema);
    EXECUTE format('ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK(region_id=%L), ADD CONSTRAINT %I CHECK(generation_id=%s), ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,way_id)',build_schema,ways_name,'ways_region_g'||generation_id,candidate_region,'ways_generation_g'||generation_id,generation_id,'ways_pkey_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK(region_id=%L), ADD CONSTRAINT %I CHECK(generation_id=%s), ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,relation_id)',build_schema,localities_name,'localities_region_g'||generation_id,candidate_region,'localities_generation_g'||generation_id,generation_id,'localities_pkey_g'||generation_id);
    EXECUTE format('ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK(region_id=%L), ADD CONSTRAINT %I CHECK(generation_id=%s), ADD CONSTRAINT %I PRIMARY KEY(region_id,generation_id,segment_id)',build_schema,segments_name,'segments_region_g'||generation_id,candidate_region,'segments_generation_g'||generation_id,generation_id,'path_segments_pkey_g'||generation_id);
    EXECUTE format('CREATE INDEX %I ON %I.%I(way_id,version DESC,generation_id DESC,region_id DESC)','ways_identity_g'||generation_id,build_schema,ways_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(relation_id,relation_version DESC,generation_id DESC,region_id DESC)','localities_identity_g'||generation_id,build_schema,localities_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I USING gist((geom::geography))','ways_geography_g'||generation_id,build_schema,ways_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I USING gist(geom)','localities_geom_g'||generation_id,build_schema,localities_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(segment_id,generation_id DESC,region_id DESC)','path_segments_identity_g'||generation_id,build_schema,segments_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(source_way_id,source_way_version,generation_id DESC,region_id DESC)','path_segments_source_way_g'||generation_id,build_schema,segments_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I USING gist((geom::geography))','path_segments_geography_g'||generation_id,build_schema,segments_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(logical_path_id)','path_segments_logical_path_g'||generation_id,build_schema,segments_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(start_graph_node_id)','path_segments_start_graph_node_g'||generation_id,build_schema,segments_name);
    EXECUTE format('CREATE INDEX %I ON %I.%I(end_graph_node_id)','path_segments_end_graph_node_g'||generation_id,build_schema,segments_name);

    IF variant=1 THEN
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,10,3,NULL,''{}'',''[1,2]'',ST_GeomFromText(''LINESTRING(0 0,2 0)'',4326)),($1,$2,20,1,NULL,''{}'',''[3,4]'',ST_GeomFromText(''LINESTRING(1 1,2 1)'',4326))',build_schema,ways_name) USING candidate_region,generation_id;
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,''aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'',10,3,1,1,2,1,''10000000-0000-0000-0000-000000000001'',''10000000-0000-0000-0000-000000000002'',''Old Road'',''old road'',''residential'',''road'',''{}'',true,true,ST_GeomFromText(''LINESTRING(0 0,2 0)'',4326),NULL,''11111111-1111-1111-1111-111111111111'',2),($1,$2,''cccccccc-cccc-cccc-cccc-cccccccccccc'',20,1,1,1,2,1,''20000000-0000-0000-0000-000000000001'',''20000000-0000-0000-0000-000000000002'',NULL,NULL,''path'',''trail'',''{}'',true,true,ST_GeomFromText(''LINESTRING(1 1,2 1)'',4326),NULL,''33333333-3333-3333-3333-333333333333'',1)',build_schema,segments_name) USING candidate_region,generation_id;
    ELSIF variant=2 THEN
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,10,4,NULL,''{}'',''[1,2]'',ST_GeomFromText(''LINESTRING(0 0,3 0)'',4326)),($1,$2,20,1,NULL,''{}'',''[3,4]'',ST_GeomFromText(''LINESTRING(1 1,2 1)'',4326))',build_schema,ways_name) USING candidate_region,generation_id;
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,''bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'',10,4,1,1,2,1,''10000000-0000-0000-0000-000000000001'',''10000000-0000-0000-0000-000000000002'',''New Road'',''new road'',''residential'',''road'',''{}'',true,true,ST_GeomFromText(''LINESTRING(0 0,3 0)'',4326),NULL,''22222222-2222-2222-2222-222222222222'',3),($1,$2,''cccccccc-cccc-cccc-cccc-cccccccccccc'',20,1,1,1,2,1,''20000000-0000-0000-0000-000000000001'',''20000000-0000-0000-0000-000000000002'',NULL,NULL,''path'',''trail'',''{}'',true,true,ST_GeomFromText(''LINESTRING(1 1,2 1)'',4326),NULL,''33333333-3333-3333-3333-333333333333'',1)',build_schema,segments_name) USING candidate_region,generation_id;
    ELSE
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,30,1,NULL,''{}'',''[5,6]'',ST_GeomFromText(''LINESTRING(0 1,1 1)'',4326))',build_schema,ways_name) USING candidate_region,generation_id;
        EXECUTE format('INSERT INTO %I.%I VALUES($1,$2,''dddddddd-dddd-dddd-dddd-dddddddddddd'',30,1,1,1,2,1,''30000000-0000-0000-0000-000000000001'',''30000000-0000-0000-0000-000000000002'',''New Path'',''new path'',''footway'',''footway'',''{}'',true,true,ST_GeomFromText(''LINESTRING(0 1,1 1)'',4326),NULL,''44444444-4444-4444-4444-444444444444'',1)',build_schema,segments_name) USING candidate_region,generation_id;
    END IF;
    EXECUTE format('INSERT INTO %I.logical_paths SELECT logical_path_id,min(locality_relation_id),min(name),min(normalized_name),min(broad_class),count(*),sum(length_m) FROM %I.%I GROUP BY logical_path_id',build_schema,build_schema,segments_name);
    RETURN generation_id;
END
$fixture$;

DO $verify$
DECLARE
    generation_a bigint := pg_temp.make_candidate('fixture:region-a',1);
    generation_b bigint := pg_temp.make_candidate('fixture:region-b',2);
    replacement_a bigint;
    rollback_a bigint;
    active_before bigint;
BEGIN
    PERFORM osm_catalog.promote_region_generation('fixture:region-a',generation_a,('osm_build_'||generation_a)::name,jsonb_build_object('partitionPrepared',true,'preparedRegionId','fixture:region-a','preparedGenerationId',generation_a,'importerVersion',1,'derivationVersion',1,'provenanceMismatches',0,'sourceVersionMismatches',0,'logicalPathMismatches',0));
    PERFORM osm_catalog.promote_region_generation('fixture:region-b',generation_b,('osm_build_'||generation_b)::name,jsonb_build_object('partitionPrepared',true,'preparedRegionId','fixture:region-b','preparedGenerationId',generation_b,'importerVersion',1,'derivationVersion',1,'provenanceMismatches',0,'sourceVersionMismatches',0,'logicalPathMismatches',0));
    IF (SELECT version FROM osm_active.ways WHERE way_id=10)<>4 THEN RAISE EXCEPTION 'newer source way version did not win'; END IF;
    IF EXISTS(SELECT 1 FROM osm_active.path_segments WHERE segment_id='aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa') THEN RAISE EXCEPTION 'segment from superseded source way is visible'; END IF;
    IF (SELECT count(*) FROM osm_active.path_segments WHERE segment_id='cccccccc-cccc-cccc-cccc-cccccccccccc')<>1 THEN RAISE EXCEPTION 'exact segment identity was not deduplicated'; END IF;

    replacement_a := pg_temp.make_candidate('fixture:region-a',3);
    PERFORM osm_catalog.promote_region_generation('fixture:region-a',replacement_a,('osm_build_'||replacement_a)::name,jsonb_build_object('partitionPrepared',true,'preparedRegionId','fixture:region-a','preparedGenerationId',replacement_a,'importerVersion',1,'derivationVersion',1,'provenanceMismatches',0,'sourceVersionMismatches',0,'logicalPathMismatches',0));
    IF NOT EXISTS(SELECT 1 FROM osm_active.ways WHERE way_id=10 AND version=4) THEN RAISE EXCEPTION 'replacing one region removed the other region'; END IF;
    IF NOT EXISTS(SELECT 1 FROM osm_active.ways WHERE way_id=30) THEN RAISE EXCEPTION 'replacement region is not visible'; END IF;
    IF NOT EXISTS(SELECT 1 FROM osm_catalog.storage_gc WHERE generation_id=generation_a AND object_kind='relation' AND state='queued') THEN RAISE EXCEPTION 'retired region leaves were not queued for GC'; END IF;

    active_before := replacement_a;
    rollback_a := pg_temp.make_candidate('fixture:region-a',3);
    BEGIN
        PERFORM osm_catalog.promote_region_generation('fixture:region-a',rollback_a,('osm_build_'||rollback_a)::name,jsonb_build_object('partitionPrepared',true,'preparedRegionId','fixture:region-a','preparedGenerationId',rollback_a,'importerVersion',1,'derivationVersion',1,'provenanceMismatches',0,'sourceVersionMismatches',0,'logicalPathMismatches',0));
        RAISE EXCEPTION 'forced rollback';
    EXCEPTION WHEN raise_exception THEN
        NULL;
    END;
    IF (SELECT generation_id FROM osm_catalog.region_storage WHERE region_id='fixture:region-a')<>active_before THEN RAISE EXCEPTION 'forced rollback changed active storage'; END IF;
END
$verify$;

ROLLBACK;
