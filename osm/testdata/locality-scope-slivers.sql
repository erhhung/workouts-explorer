\set OSM_REGION_ID geofabrik:sliver-fixture
\set OSM_GENERATION_ID 2
\set OSM_BUILD_SCHEMA osm_build_2
\set OSM_IMPORTER_VERSION 3
\set OSM_DERIVATION_VERSION 21

CREATE SCHEMA osm_build_2;
SET search_path TO osm_build_2,public;
INSERT INTO osm_catalog.generations VALUES(2,'building');
CREATE TABLE localities(relation_id bigint PRIMARY KEY,admin_level integer,geom geometry(MultiPolygon,4326));
INSERT INTO localities VALUES
 (6,6,ST_Multi(ST_GeomFromText('POLYGON((-.001 -.001,.001 -.001,.001 .001,-.001 .001,-.001 -.001),(.00004 -.00004,.00004 .00004,.00016 .00004,.00016 -.00004,.00004 -.00004))',4326))),
 (8,8,ST_Multi(ST_GeomFromText('POLYGON((.00004 -.00004,.00016 -.00004,.00016 .00004,.00004 .00004,.00004 -.00004))',4326))),
 (16,6,ST_Multi(ST_GeomFromText('POLYGON((-.001 .009,.001 .009,.001 .011,-.001 .011,-.001 .009))',4326))),
 (18,8,ST_Multi(ST_GeomFromText('POLYGON((.00004 .00996,.00030 .00996,.00030 .01004,.00004 .01004,.00004 .00996))',4326)));
CREATE TABLE path_segments(segment_id uuid PRIMARY KEY,source_way_id bigint,source_way_version integer,
 start_node_index integer,end_node_index integer,boundary_piece integer,locality_relation_id bigint,
 normalized_name text,broad_class text,length_m double precision,geom geometry(LineString,4326),tags jsonb);
INSERT INTO path_segments
SELECT md5(way::text||':'||piece)::uuid,way,1,0,1,piece,
 CASE WHEN piece=2 THEN municipality ELSE county END,NULL,broad_class,
 ST_Length(geometry::geography),geometry,'{}'::jsonb
FROM (VALUES(1,6,8,0::double precision,'trail',.00016),
 (2,16,18,.01,'trail',.00016),(3,16,18,.01,'road',.00016),
 (4,16,18,.01,'trail',.00030)) source(way,county,municipality,y,broad_class,right_edge)
CROSS JOIN generate_series(1,3) piece
CROSS JOIN LATERAL (SELECT ST_MakeLine(
 ST_SetSRID(ST_MakePoint(CASE piece WHEN 1 THEN -.0001 WHEN 2 THEN .00004 ELSE right_edge END,y),4326),
 ST_SetSRID(ST_MakePoint(CASE piece WHEN 1 THEN .00004 WHEN 2 THEN right_edge ELSE .0004 END,y),4326)) geometry) line;
CREATE UNIQUE INDEX source_piece ON path_segments(source_way_id,source_way_version,start_node_index,end_node_index,boundary_piece);
INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,cursor)
VALUES(2,'attribute-slivers',1,repeat('0',64),'running','{}');
\ir ../batches/attribute-slivers-batched.sql
\ir ../batches/attribute-slivers-batched.sql
DO $test$
BEGIN
 IF EXISTS(SELECT 1 FROM locality_scope_slivers WHERE segment_id=md5('1:2')::uuid)
 THEN RAISE EXCEPTION 'outside-county sliver was staged for invalid reassignment'; END IF;
 IF (SELECT count(*) FROM locality_scope_slivers)<>1
 THEN RAISE EXCEPTION 'valid nested sliver or existing length/road exclusions changed'; END IF;
END;
$test$;
\ir ../batches/attribute-slivers-batched.sql
\ir ../batches/attribute-slivers-batched.sql
DO $test$
BEGIN
 IF (SELECT locality_relation_id FROM path_segments WHERE segment_id=md5('1:2')::uuid)<>8
 THEN RAISE EXCEPTION 'outside-county sliver lost its valid municipality'; END IF;
 IF (SELECT locality_relation_id FROM path_segments WHERE segment_id=md5('2:2')::uuid)<>16
 THEN RAISE EXCEPTION 'valid nested sliver no longer absorbed'; END IF;
 IF EXISTS(SELECT 1 FROM path_segments WHERE source_way_id IN(3,4) AND boundary_piece=2 AND locality_relation_id<>18)
 THEN RAISE EXCEPTION 'road/long segment unexpectedly reassigned'; END IF;
 IF (SELECT absorbed_segments FROM attribution_scope_sliver_stats)<>1
 THEN RAISE EXCEPTION 'sliver statistics included a rejected reassignment'; END IF;
 IF NOT EXISTS(SELECT 1 FROM osm_catalog.generation_stages WHERE generation_id=2 AND stage='attribute-slivers' AND state='completed' AND batch_count=4)
 THEN RAISE EXCEPTION 'sliver discovery/apply resume boundaries changed'; END IF;
END;
$test$;
CREATE VIEW validation_segments AS SELECT * FROM path_segments;
CREATE VIEW validation_localities AS SELECT * FROM localities;
CREATE TABLE park_areas(geom geometry(MultiPolygon,4326));
CREATE TABLE education_areas(geom geometry(MultiPolygon,4326));
CREATE TABLE validation_metrics(metric text PRIMARY KEY,value jsonb NOT NULL);
INSERT INTO validation_metrics VALUES('materialLocalityResiduals','0'),
 ('materialParkBoundaryCrossings','0'),('materialEducationBoundaryCrossings','0');
INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,cursor)
VALUES(2,'validate',2,repeat('0',64),'running','{"phase":"locality-residuals"}');
\ir ../batches/validate.sql
DO $test$
BEGIN
 IF EXISTS(SELECT 1 FROM validation_metrics WHERE value::text::bigint<>0)
 THEN RAISE EXCEPTION 'sliver attribution failed the unchanged read-only validation predicate'; END IF;
END;
$test$;
