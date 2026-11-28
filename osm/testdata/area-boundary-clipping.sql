\set OSM_BUILD_SCHEMA osm_build_1
\set OSM_GENERATION_ID 1
\set OSM_DERIVATION_VERSION 21
\set OSM_IMPORTER_VERSION 3
\set OSM_REGION_ID fixture:areas

CREATE SCHEMA osm_build_1;
SET search_path TO osm_build_1, public;
CREATE TABLE path_segments (
    segment_id uuid PRIMARY KEY,
    source_way_id bigint NOT NULL,
    source_way_version integer NOT NULL,
    derivation_version integer NOT NULL,
    start_node_index integer NOT NULL,
    end_node_index integer NOT NULL,
    boundary_piece integer NOT NULL,
    start_graph_node_id uuid NOT NULL,
    end_graph_node_id uuid NOT NULL,
    name text, normalized_name text,
    highway text NOT NULL, broad_class text NOT NULL, tags jsonb NOT NULL,
    motor_forward_allowed boolean NOT NULL, motor_reverse_allowed boolean NOT NULL,
    geom geometry(LineString,4326) NOT NULL,
    locality_relation_id bigint, logical_path_id uuid NOT NULL, length_m double precision NOT NULL,
    UNIQUE(source_way_id,source_way_version,start_node_index,end_node_index,boundary_piece)
);
CREATE TABLE logical_paths (
    logical_path_id uuid PRIMARY KEY, locality_relation_id bigint, name text, normalized_name text,
    broad_class text, member_segment_count bigint, member_length_m double precision
);
CREATE TABLE localities(relation_id bigint PRIMARY KEY,admin_level integer,name text,geom geometry);
INSERT INTO localities VALUES(1,8,'Fixture City',ST_GeomFromText('POLYGON((-.002 -1,.004 -1,.004 1,-.002 1,-.002 -1))',4326));
CREATE TABLE park_areas(source_type text,source_id bigint,version integer,name text,normalized_name text,
    park_kind text,area_m2 double precision,type_priority integer,geom geometry);
INSERT INTO park_areas VALUES
('way',1,1,'Crossing Park','crossing park','state_park',40000,1,ST_GeomFromText('POLYGON((0 -.0005,.003 -.0005,.003 .0005,0 .0005,0 -.0005))',4326)),
('way',2,1,'Hole Park','hole park','state_park',100000,1,ST_GeomFromText('POLYGON((0 .019,.004 .019,.004 .021,0 .021,0 .019),(.001 .0195,.001 .0205,.003 .0205,.003 .0195,.001 .0195))',4326)),
('way',3,1,'Outer Local Park','outer local park','local_park',100000,2,ST_GeomFromText('POLYGON((0 .0295,.004 .0295,.004 .0305,0 .0305,0 .0295))',4326)),
('way',4,1,'Inner State Park','inner state park','state_park',50000,1,ST_GeomFromText('POLYGON((.001 .0295,.003 .0295,.003 .0305,.001 .0305,.001 .0295))',4326)),
('way',5,1,'Shared Park','shared park','state_park',40000,1,ST_GeomFromText('POLYGON((0 .0695,.003 .0695,.003 .0705,0 .0705,0 .0695))',4326));
CREATE INDEX ON park_areas USING gist(geom);
CREATE TABLE education_areas(source_type text,source_id bigint,version integer,name text,normalized_name text,
    education_kind text,area_m2 double precision,type_priority integer,geom geometry);
INSERT INTO education_areas VALUES
('way',1,1,'Crossing School','crossing school','school',40000,1,ST_GeomFromText('POLYGON((0 .0095,.003 .0095,.003 .0105,0 .0105,0 .0095))',4326)),
('way',2,1,'Hole School','hole school','school',100000,1,ST_GeomFromText('POLYGON((0 .059,.004 .059,.004 .061,0 .061,0 .059),(.001 .0595,.001 .0605,.003 .0605,.003 .0595,.001 .0595))',4326)),
('way',3,1,'Shared School','shared school','school',40000,1,ST_GeomFromText('POLYGON((0 .0695,.003 .0695,.003 .0705,0 .0705,0 .0695))',4326));
CREATE INDEX ON education_areas USING gist(geom);

INSERT INTO path_segments
SELECT md5(label)::uuid,n,1,21,1,2,1,md5(label||'/start')::uuid,md5(label||'/end')::uuid,
    name,lower(name),'service','road',jsonb_build_object('fixture',label),true,true,
    ST_GeomFromText(wkt,4326),1,md5(label||'/path')::uuid,ST_Length(ST_GeomFromText(wkt,4326)::geography)
FROM (VALUES
    (1,'park-crossing',NULL::text,'LINESTRING(-.001 0,.004 0)'),
    (2,'school-crossing',NULL,'LINESTRING(-.001 .01,.004 .01)'),
    (3,'named-school','Named Road','LINESTRING(-.001 .0101,.004 .0101)'),
    (4,'park-hole',NULL,'LINESTRING(-.001 .02,.005 .02)'),
    (5,'overlapping-parks',NULL,'LINESTRING(-.001 .03,.004 .03)'),
    (6,'locality-crossing',NULL,'LINESTRING(-.003 .04,.005 .04)'),
    (7,'outside',NULL,'LINESTRING(-.001 .05,.003 .05)'),
    (8,'tangent',NULL,'LINESTRING(-.001 .0015,0 .0005)'),
    (9,'school-hole',NULL,'LINESTRING(-.001 .06,.005 .06)'),
    (10,'park-and-school',NULL,'LINESTRING(-.001 .07,.004 .07)'),
    (11,'named-park','Named Park Road','LINESTRING(-.001 .0001,.004 .0001)'),
    (12,'retrace-park',NULL,'LINESTRING(-.001 .0002,.004 .0002,-.001 .0002)'),
    (13,'retrace-school',NULL,'LINESTRING(-.001 .0102,.004 .0102,-.001 .0102)')
) fixture(n,label,name,wkt);
CREATE TABLE originals AS SELECT * FROM path_segments;
INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state)
SELECT 1,stage,0,repeat('0',64),'running'
FROM unnest(ARRAY['clip-candidates','clip-replacements','clip-apply','clip-residual',
    'attribute-parks-tags','attribute-education']) stage;

-- Separate invocations reload durable cursors, as they do after process restart.
\ir ../batches/clip-localities-candidates.sql
\ir ../batches/clip-localities-candidates.sql
\ir ../batches/clip-localities-replacements.sql
\ir ../batches/clip-localities-replacements.sql
\ir ../batches/clip-localities-apply.sql
\ir ../batches/clip-localities-apply.sql
\ir ../batches/clip-localities-residual.sql
\ir ../batches/clip-localities-residual.sql
\ir ../batches/clip-localities-finalize.sql
\ir ../batches/attribute-parks-batched.sql
\ir ../batches/attribute-parks-batched.sql
\ir ../batches/attribute-education-batched.sql
\ir ../batches/attribute-education-batched.sql

DO $test$
DECLARE failure text;
BEGIN
    SELECT string_agg(label,', ') INTO failure
    FROM (VALUES ('park-crossing',3,1,0),('school-crossing',3,0,1),('named-school',1,0,0),
        ('park-hole',5,2,0),('overlapping-parks',4,3,0),('locality-crossing',3,0,0),
        ('outside',1,0,0),('tangent',1,0,0),('school-hole',5,0,2),('park-and-school',3,1,1),
        ('named-park',3,1,0),('retrace-park',6,2,0),('retrace-school',6,0,2)) expected(label,pieces,parks,schools)
    CROSS JOIN LATERAL (
        SELECT count(*) pieces,count(*) FILTER(WHERE tags ? 'workouts:park_id') parks,
            count(*) FILTER(WHERE tags ? 'workouts:education_id') schools
        FROM path_segments WHERE tags->>'fixture'=expected.label
    ) actual
    WHERE (actual.pieces,actual.parks,actual.schools) IS DISTINCT FROM (expected.pieces,expected.parks,expected.schools);
    IF failure IS NOT NULL THEN RAISE EXCEPTION 'incorrect pieces or attribution: %',failure; END IF;

    IF EXISTS (
        SELECT 1 FROM originals original
        CROSS JOIN LATERAL (
            SELECT ST_UnaryUnion(ST_Collect(piece.geom)) geom,sum(piece.length_m) meters
            FROM path_segments piece WHERE piece.source_way_id=original.source_way_id
        ) rebuilt
        WHERE ST_HausdorffDistance(ST_Transform(original.geom,3857),ST_Transform(rebuilt.geom,3857))>0.000001
            OR abs(original.length_m-rebuilt.meters)>0.01
    ) THEN RAISE EXCEPTION 'clipping lost, invented, or duplicated geometry'; END IF;

    IF EXISTS (
        SELECT 1 FROM (
            SELECT piece.*,lead(start_graph_node_id) OVER source_order next_start,
                lead(boundary_piece) OVER source_order next_piece
            FROM path_segments piece
            WINDOW source_order AS (PARTITION BY source_way_id ORDER BY boundary_piece)
        ) piece
        WHERE next_start IS NOT NULL AND (end_graph_node_id<>next_start OR next_piece<>boundary_piece+1)
    ) OR EXISTS (
        SELECT 1 FROM originals original
        JOIN LATERAL (SELECT * FROM path_segments WHERE source_way_id=original.source_way_id ORDER BY boundary_piece LIMIT 1) first ON true
        JOIN LATERAL (SELECT * FROM path_segments WHERE source_way_id=original.source_way_id ORDER BY boundary_piece DESC LIMIT 1) last ON true
        WHERE first.start_graph_node_id<>original.start_graph_node_id OR last.end_graph_node_id<>original.end_graph_node_id
    ) THEN RAISE EXCEPTION 'clipping broke graph connectivity or original endpoints'; END IF;

    IF EXISTS (
        SELECT 1 FROM path_segments segment JOIN park_areas park ON park.name=segment.tags->>'workouts:park_name'
        WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,park.geom),2)::geography)>0.01
    ) OR EXISTS (
        SELECT 1 FROM path_segments segment JOIN education_areas school ON school.name=segment.tags->>'workouts:education_name'
        WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,school.geom),2)::geography)>0.01
    ) THEN RAISE EXCEPTION 'outside geometry received facility attribution'; END IF;

    IF (SELECT count(*) FROM path_segments WHERE tags->>'fixture'='overlapping-parks'
        AND tags->>'workouts:park_name'='Inner State Park')<>1
    THEN RAISE EXCEPTION 'overlapping park precedence changed'; END IF;
    IF EXISTS (SELECT 1 FROM osm_catalog.generation_stages WHERE state<>'completed' OR batch_count<>2)
    THEN RAISE EXCEPTION 'durable batch cursor did not resume and complete'; END IF;
END;
$test$;

-- Run the actual bounded validation phase, then prove it detects untagged
-- crossings if a future clipping regression leaves the original geometry.
CREATE TABLE validation_fixture AS SELECT * FROM path_segments;
CREATE VIEW validation_segments AS SELECT * FROM validation_fixture;
CREATE VIEW validation_localities AS SELECT * FROM localities;
CREATE TABLE validation_metrics(metric text PRIMARY KEY,value jsonb NOT NULL);
INSERT INTO validation_metrics VALUES('materialLocalityResiduals','0'),
    ('materialParkBoundaryCrossings','0'),('materialEducationBoundaryCrossings','0');
INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,cursor)
VALUES(1,'validate',0,repeat('0',64),'running','{"phase":"locality-residuals"}');
\ir ../batches/validate.sql
DO $test$
BEGIN
    IF EXISTS (SELECT 1 FROM validation_metrics WHERE value::text::bigint<>0)
    THEN RAISE EXCEPTION 'correctly clipped pieces failed boundary validation'; END IF;
END;
$test$;
INSERT INTO validation_fixture SELECT * FROM originals WHERE tags->>'fixture' IN ('park-crossing','school-crossing');
UPDATE osm_catalog.generation_stages SET cursor='{"phase":"locality-residuals"}' WHERE stage='validate';
\ir ../batches/validate.sql
DO $test$
BEGIN
    IF (SELECT value::text::bigint FROM validation_metrics WHERE metric='materialParkBoundaryCrossings')<>1
        OR (SELECT value::text::bigint FROM validation_metrics WHERE metric='materialEducationBoundaryCrossings')<>1
    THEN RAISE EXCEPTION 'boundary validation missed an untagged park or school crossing'; END IF;
END;
$test$;

-- A wholly boundary-aligned line can straddle the exact polygon numerically.
-- The tolerance must not hide a real, shallow crossing a centimeter away.
TRUNCATE validation_fixture;
INSERT INTO validation_fixture
SELECT original.segment_id,original.source_way_id,original.source_way_version,
    original.derivation_version,original.start_node_index,original.end_node_index,
    original.boundary_piece,original.start_graph_node_id,original.end_graph_node_id,
    original.name,original.normalized_name,original.highway,original.broad_class,
    original.tags,original.motor_forward_allowed,original.motor_reverse_allowed,
    geometry,original.locality_relation_id,original.logical_path_id,ST_Length(geometry::geography)
FROM originals original
CROSS JOIN LATERAL (SELECT CASE original.tags->>'fixture'
    WHEN 'park-crossing' THEN ST_GeomFromText('LINESTRING(.0001 -.00050000000001,.0029 -.00049999999999)',4326)
    WHEN 'school-crossing' THEN ST_GeomFromText('LINESTRING(.0001 .00949999999999,.0029 .00950000000001)',4326)
    WHEN 'retrace-park' THEN ST_GeomFromText('LINESTRING(.0001 -.0005002,.0029 -.0004998)',4326)
    ELSE ST_GeomFromText('LINESTRING(.0001 .0094998,.0029 .0095002)',4326) END geometry) fixture
WHERE original.tags->>'fixture' IN ('park-crossing','school-crossing','retrace-park','retrace-school');
UPDATE validation_metrics SET value='0';
UPDATE osm_catalog.generation_stages SET cursor='{"phase":"locality-residuals"}' WHERE stage='validate';
\ir ../batches/validate.sql
DO $test$
BEGIN
    IF (SELECT value::text::bigint FROM validation_metrics WHERE metric='materialParkBoundaryCrossings')<>1
        OR (SELECT value::text::bigint FROM validation_metrics WHERE metric='materialEducationBoundaryCrossings')<>1
    THEN RAISE EXCEPTION 'precision tolerance hid a real crossing or counted a boundary-aligned artifact'; END IF;
END;
$test$;
