-- Public invented grid, 40 x 40 nodes, 80 named roads, 3120 physical edges.
-- Run only on the disposable OSM database after the real OSM migrations.
BEGIN;
INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary,configured)
VALUES('fixture:dense','fixture','dense','Synthetic Acceptance City','https://example.test/synthetic-catalog',
  'https://example.test/synthetic-grid',ST_Multi(ST_MakeEnvelope(-.01,-.01,.05,.05,4326)),true);
INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version,promoted_at)
OVERRIDING SYSTEM VALUE VALUES(1,'fixture:dense','active','osm_build_1','https://example.test/synthetic-grid',repeat('a',64),1,1,clock_timestamp());
CREATE TABLE osm_canonical.ways_acceptance PARTITION OF osm_canonical.ways FOR VALUES IN ('fixture:dense');
CREATE TABLE osm_canonical.localities_acceptance PARTITION OF osm_canonical.localities FOR VALUES IN ('fixture:dense');
CREATE TABLE osm_canonical.segments_acceptance PARTITION OF osm_canonical.path_segments FOR VALUES IN ('fixture:dense');
INSERT INTO osm_catalog.region_storage(region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema)
VALUES('fixture:dense',1,'ways_acceptance','localities_acceptance','segments_acceptance','osm_build_1');
INSERT INTO osm_canonical.localities(region_id,generation_id,relation_id,relation_version,admin_level,name,normalized_name,tags,geom)
VALUES('fixture:dense',1,1,1,8,'Synthetic Acceptance City','synthetic acceptance city','{"synthetic":true}',ST_Multi(ST_MakeEnvelope(-.01,-.01,.05,.05,4326)));
INSERT INTO osm_canonical.ways(region_id,generation_id,way_id,version,tags,node_ids,geom)
SELECT 'fixture:dense',1,r+1,1,jsonb_build_object('name',CASE WHEN r<40 THEN 'Street ' ELSE 'Avenue ' END||(r%40+1),'synthetic',true),
  '[1,2]'::jsonb,CASE WHEN r<40 THEN ST_MakeLine(ST_Point(0,r*.001,4326),ST_Point(.039,r*.001,4326))
  ELSE ST_MakeLine(ST_Point((r-40)*.001,0,4326),ST_Point((r-40)*.001,.039,4326)) END
FROM generate_series(0,79) r;
INSERT INTO osm_canonical.path_segments(region_id,generation_id,segment_id,source_way_id,source_way_version,derivation_version,
  start_node_index,end_node_index,boundary_piece,start_graph_node_id,end_graph_node_id,name,normalized_name,highway,broad_class,tags,
  motor_forward_allowed,motor_reverse_allowed,geom,locality_relation_id,logical_path_id,length_m)
SELECT 'fixture:dense',1,md5('acceptance-edge-'||r||'-'||e)::uuid,r+1,1,1,e,e+1,1,
  md5('acceptance-node-'||x1||'-'||y1)::uuid,md5('acceptance-node-'||x2||'-'||y2)::uuid,
  name,lower(name),'residential','road',jsonb_build_object('name',name,'surface','asphalt','synthetic',true),true,true,
  geom,1,md5('acceptance-path-'||r)::uuid,ST_Length(geom::geography)
FROM generate_series(0,79) r CROSS JOIN generate_series(0,38) e
CROSS JOIN LATERAL (SELECT CASE WHEN r<40 THEN e ELSE r-40 END x1,CASE WHEN r<40 THEN r ELSE e END y1,
  CASE WHEN r<40 THEN e+1 ELSE r-40 END x2,CASE WHEN r<40 THEN r ELSE e+1 END y2,
  CASE WHEN r<40 THEN 'Street ' ELSE 'Avenue ' END||(r%40+1) name) n
CROSS JOIN LATERAL (SELECT ST_MakeLine(ST_Point(x1*.001,y1*.001,4326),ST_Point(x2*.001,y2*.001,4326)) geom) g;
SELECT setval(pg_get_serial_sequence('osm_catalog.generations','id'),1);
GRANT USAGE ON SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;
GRANT SELECT ON ALL TABLES IN SCHEMA osm_catalog,osm_canonical,osm_active TO workouts_coverage_worker;
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA osm_catalog,osm_active FROM PUBLIC,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION osm_catalog.promotion_event_head(),osm_catalog.read_promotion_events(bigint,integer),
  osm_active.timezone_at(double precision,double precision) TO workouts_coverage_worker;
COMMIT;
ANALYZE;
