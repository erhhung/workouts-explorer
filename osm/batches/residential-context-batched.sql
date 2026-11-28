-- Public-source-only classification. No private routes enter the OSM writer.
SET jit = off;
SET work_mem = '64MB';
SET maintenance_work_mem = '256MB';
SET max_parallel_workers_per_gather = 0;
SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE OR REPLACE PROCEDURE classify_residential_access(target_generation bigint)
LANGUAGE plpgsql AS $procedure$
DECLARE
    phase text;
    last_way bigint;
    next_way bigint;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint := 0;
    next_phase text;
    rounds integer;
    finished boolean := false;
BEGIN
    SELECT COALESCE(cursor->>'phase','init'),NULLIF(cursor->>'last_way_id','')::bigint,
        batch_count,rows_processed,COALESCE((cursor->>'rounds')::integer,0)
    INTO phase,last_way,expected_batch,processed,rounds
    FROM osm_catalog.generation_stages
    WHERE generation_id=target_generation AND stage='residential-context' AND state='running'
    FOR UPDATE;
    IF NOT FOUND OR NOT EXISTS(SELECT 1 FROM osm_catalog.generations
        WHERE id=target_generation AND importer_version=4 AND state='building') THEN
        RAISE EXCEPTION 'residential context requires an owned importer-v4 generation';
    END IF;
    next_phase:=phase;

    IF phase='init' THEN
        CREATE INDEX residential_building_ways_geography ON residential_building_ways USING gist((geom::geography));
        CREATE INDEX residential_building_relations_geography ON residential_building_relations USING gist((geom::geography));
        CREATE INDEX residential_building_relations_geometry ON residential_building_relations USING gist(geom);
        CREATE INDEX residential_landuse_ways_geometry ON residential_landuse_ways USING gist(geom);
        CREATE INDEX residential_landuse_relations_geometry ON residential_landuse_relations USING gist(geom);
        CREATE INDEX residential_address_nodes_geometry ON residential_address_nodes USING gist(geom);
        CREATE UNIQUE INDEX residential_context_source_way ON ways(way_id);
        CREATE UNLOGGED TABLE residential_access_roads(
            way_id bigint PRIMARY KEY,
            access_class text NOT NULL DEFAULT 'unclassified'
                CHECK(access_class IN('shared','single_house','unclassified')),
            home_units integer NOT NULL DEFAULT 0,
            building_count integer NOT NULL DEFAULT 0,
            reason text NOT NULL DEFAULT 'insufficient_residential_evidence',
            depth integer CHECK(depth>=0),
            evidence jsonb NOT NULL DEFAULT '[]'::jsonb
        );
        INSERT INTO residential_access_roads(way_id,access_class,reason)
        SELECT way_id,CASE WHEN tags->>'service'='shared_driveway' THEN 'shared' ELSE 'unclassified' END,
            CASE WHEN tags->>'service'='shared_driveway' THEN 'explicit_shared_driveway' ELSE 'insufficient_residential_evidence' END
        FROM ways WHERE tags->>'highway'='service' AND tags->>'service' IN('driveway','shared_driveway');
        CREATE UNLOGGED TABLE residential_access_nodes(way_id bigint,node_id bigint,PRIMARY KEY(way_id,node_id));
        CREATE INDEX residential_access_node_lookup ON residential_access_nodes(node_id,way_id);
        CREATE UNLOGGED TABLE residential_boundary_nodes(node_id bigint PRIMARY KEY);
        CREATE UNLOGGED TABLE residential_access_edges(way_id bigint,other_id bigint,PRIMARY KEY(way_id,other_id));
        CREATE VIEW residential_footprints AS
        SELECT 'way'::text source_type,w.way_id source_id,w.version,w.tags,w.geom
        FROM residential_building_ways w
        WHERE w.geom IS NOT NULL AND ST_IsValid(w.geom) AND NOT ST_IsEmpty(w.geom)
          AND NOT EXISTS(SELECT 1 FROM residential_building_relations r
            WHERE r.geom && w.geom AND ST_IsValid(r.geom) AND
              (r.members @> jsonb_build_array(jsonb_build_object('type','w','ref',w.way_id))
               OR r.members @> jsonb_build_array(jsonb_build_object('type','way','ref',w.way_id))))
        UNION ALL
        SELECT 'relation',relation_id,version,tags,geom FROM residential_building_relations
        WHERE geom IS NOT NULL AND ST_IsValid(geom) AND NOT ST_IsEmpty(geom);
        ANALYZE ways;
        ANALYZE residential_building_ways;
        ANALYZE residential_building_relations;
        ANALYZE residential_landuse_ways;
        ANALYZE residential_landuse_relations;
        ANALYZE residential_address_nodes;
        next_phase:='nodes';
    ELSIF phase='nodes' THEN
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS
        SELECT r.way_id FROM residential_access_roads r WHERE last_way IS NULL OR r.way_id>last_way ORDER BY r.way_id LIMIT 5000;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        INSERT INTO residential_access_nodes
        SELECT DISTINCT w.way_id,n.node_id::bigint FROM residential_batch b JOIN ways w USING(way_id)
        CROSS JOIN LATERAL jsonb_array_elements_text(w.node_ids) n(node_id) ON CONFLICT DO NOTHING;
        SELECT max(way_id) INTO next_way FROM residential_batch;
        IF batch_rows=0 THEN next_phase:='roots'; END IF;
    ELSIF phase='roots' THEN
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS
        SELECT way_id FROM ways WHERE (last_way IS NULL OR way_id>last_way)
          AND (tags->>'highway' IN('residential','living_street','unclassified','road','tertiary','secondary','primary')
            OR tags->>'highway'='service' AND COALESCE(tags->>'service','') NOT IN('driveway','shared_driveway','drive-through','emergency_access'))
        ORDER BY way_id LIMIT 10000;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        INSERT INTO residential_boundary_nodes
        SELECT DISTINCT n.node_id::bigint FROM residential_batch b JOIN ways w USING(way_id)
        CROSS JOIN LATERAL jsonb_array_elements_text(w.node_ids) n(node_id)
        WHERE EXISTS(SELECT 1 FROM residential_access_nodes a WHERE a.node_id=n.node_id::bigint)
        ON CONFLICT DO NOTHING;
        SELECT max(way_id) INTO next_way FROM residential_batch;
        IF batch_rows=0 THEN next_phase:='edges'; END IF;
    ELSIF phase='edges' THEN
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS
        SELECT way_id FROM residential_access_roads WHERE last_way IS NULL OR way_id>last_way ORDER BY way_id LIMIT 5000;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        INSERT INTO residential_access_edges
        SELECT DISTINCT a.way_id,b.way_id FROM residential_batch key JOIN residential_access_nodes a USING(way_id)
        JOIN residential_access_nodes b ON b.node_id=a.node_id AND b.way_id<>a.way_id
        WHERE (SELECT count(*) FROM residential_access_nodes degree WHERE degree.node_id=a.node_id)<=32
        ON CONFLICT DO NOTHING;
        UPDATE residential_access_roads r SET depth=0 FROM residential_batch key
        WHERE r.way_id=key.way_id AND EXISTS(SELECT 1 FROM residential_access_nodes n
            JOIN residential_boundary_nodes boundary USING(node_id) WHERE n.way_id=r.way_id);
        SELECT max(way_id) INTO next_way FROM residential_batch;
        IF batch_rows=0 THEN next_phase:='depths'; END IF;
    ELSIF phase='depths' THEN
        WITH improvements AS (
            SELECT parent.way_id,min(child.depth)+1 depth FROM residential_access_roads parent
            JOIN residential_access_edges edge ON edge.way_id=parent.way_id
            JOIN residential_access_roads child ON child.way_id=edge.other_id
            WHERE child.depth IS NOT NULL AND (parent.depth IS NULL OR parent.depth>child.depth+1)
            GROUP BY parent.way_id ORDER BY parent.way_id LIMIT 5000
        ) UPDATE residential_access_roads parent SET depth=i.depth FROM improvements i WHERE parent.way_id=i.way_id;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        rounds:=rounds+1;
        IF batch_rows=0 OR rounds>=512 THEN next_phase:='frontage'; END IF;
    ELSIF phase='frontage' THEN
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS
        SELECT way_id FROM residential_access_roads WHERE last_way IS NULL OR way_id>last_way ORDER BY way_id LIMIT 500;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        WITH evidence AS MATERIALIZED (
            SELECT road.way_id,b.source_type,b.source_id,b.version,
                homes.known,homes.units,COALESCE(addresses.single_address,b.source_type||':'||b.source_id::text) home_key
            FROM residential_batch key JOIN ways road USING(way_id)
            JOIN LATERAL (
                SELECT footprint.*,COALESCE((
                    SELECT n.geom FROM residential_address_nodes n
                    WHERE n.geom && footprint.geom AND ST_Covers(footprint.geom,n.geom)
                      AND (n.tags->>'entrance' IN ('main','home','yes') OR NULLIF(btrim(n.tags->>'addr:housenumber'),'') IS NOT NULL)
                    ORDER BY CASE n.tags->>'entrance' WHEN 'main' THEN 0 WHEN 'home' THEN 1 WHEN 'yes' THEN 2 ELSE 3 END,n.node_id
                    LIMIT 1),ST_PointOnSurface(footprint.geom)) center
                FROM residential_footprints footprint
                WHERE ST_DWithin(footprint.geom::geography,road.geom::geography,40)
                   AND lower(COALESCE(footprint.tags->>'building','')) NOT IN
                     ('garage','garages','shed','outbuilding','barn','roof','commercial','retail','industrial','warehouse','school','church','hospital','office')
                   AND ST_Area(footprint.geom::geography)>=35
                -- Keep the spatial lookup parameterized by this batch's road.
                -- Flattening it can scan every regional footprint for each batch.
                OFFSET 0
             ) b ON true
            JOIN LATERAL (
                SELECT w.way_id FROM ways w
                WHERE w.tags->>'highway' IN('residential','living_street','unclassified','road','service','tertiary','secondary','primary')
                ORDER BY w.geom::geography <-> b.center::geography,w.way_id LIMIT 1
            ) nearest ON nearest.way_id=road.way_id
            CROSS JOIN LATERAL (
                SELECT count(DISTINCT address)::integer address_count,
                    CASE WHEN count(DISTINCT address)=1 THEN min(address) END single_address FROM (
                    SELECT lower(btrim(COALESCE(b.tags->>'addr:street','')))||'/'||lower(btrim(b.tags->>'addr:housenumber'))||'/'||lower(btrim(COALESCE(b.tags->>'addr:unit',''))) address
                    WHERE NULLIF(btrim(b.tags->>'addr:housenumber'),'') IS NOT NULL
                    UNION ALL
                    SELECT lower(btrim(COALESCE(n.tags->>'addr:street','')))||'/'||lower(btrim(n.tags->>'addr:housenumber'))||'/'||lower(btrim(COALESCE(n.tags->>'addr:unit','')))
                    FROM residential_address_nodes n WHERE n.geom && b.geom AND ST_Covers(b.geom,n.geom)
                      AND NULLIF(btrim(n.tags->>'addr:housenumber'),'') IS NOT NULL
                ) addresses
            ) addresses
            CROSS JOIN LATERAL (
                SELECT lower(COALESCE(b.tags->>'building','')) IN
                    ('house','detached','semidetached_house','terrace','apartments','residential','bungalow','cabin') typed,
                    EXISTS(SELECT 1 FROM residential_landuse_ways area WHERE area.geom && b.center AND ST_Covers(area.geom,b.center)
                        UNION ALL SELECT 1 FROM residential_landuse_relations area WHERE area.geom && b.center AND ST_Covers(area.geom,b.center)) in_residential
            ) context
            CROSS JOIN LATERAL (
                SELECT context.typed OR addresses.address_count>0 known,
                    GREATEST(addresses.address_count,CASE WHEN COALESCE(b.tags->>'building:flats','') ~ '^[1-9][0-9]{0,2}$'
                        THEN (b.tags->>'building:flats')::integer
                        WHEN lower(b.tags->>'building')='apartments' THEN 2 ELSE 1 END) units
            ) homes
            WHERE context.typed OR context.in_residential
        ), dwellings AS (
            SELECT way_id,home_key,bool_or(known) known,max(units) units FROM evidence GROUP BY way_id,home_key
        ), units AS (
            SELECT way_id,COALESCE(sum(units) FILTER(WHERE known),0)::integer known_units,
                count(*) FILTER(WHERE NOT known)::integer unknown_buildings FROM dwellings GROUP BY way_id
        ), totals AS (
            SELECT way_id,count(*)::integer buildings,
                min(units.known_units) known_units,min(units.unknown_buildings) unknown_buildings,
                jsonb_agg(jsonb_build_object('type',source_type,'id',source_id,'version',version)
                    ORDER BY source_type,source_id) evidence FROM evidence JOIN units USING(way_id) GROUP BY way_id
        ) UPDATE residential_access_roads access SET
            home_units=t.known_units+t.unknown_buildings,building_count=t.buildings,
            access_class=CASE WHEN access.reason='explicit_shared_driveway' OR t.known_units>=2 OR t.unknown_buildings>=3
                THEN 'shared' WHEN t.known_units=1 AND t.unknown_buildings=0 THEN 'single_house' ELSE 'unclassified' END,
            reason=CASE WHEN access.reason='explicit_shared_driveway' THEN access.reason WHEN t.known_units>=2 THEN 'multiple_residential_units'
                WHEN t.unknown_buildings>=3 THEN 'multiple_residential_frontages' WHEN t.known_units=1 AND t.unknown_buildings=0 THEN 'single_residential_unit'
                ELSE 'insufficient_residential_evidence' END,evidence=t.evidence
        FROM totals t WHERE t.way_id=access.way_id;
        SELECT max(way_id) INTO next_way FROM residential_batch;
        IF batch_rows=0 THEN next_phase:='backbone'; END IF;
    ELSIF phase='backbone' THEN
        WITH parents AS (
            SELECT parent.way_id FROM residential_access_roads parent
            WHERE parent.access_class<>'shared' AND parent.depth IS NOT NULL AND EXISTS(
                SELECT 1 FROM residential_access_edges edge JOIN residential_access_roads child ON child.way_id=edge.other_id
                WHERE edge.way_id=parent.way_id AND child.access_class='shared' AND child.depth=parent.depth+1)
            ORDER BY parent.way_id LIMIT 5000
        ) UPDATE residential_access_roads parent SET access_class='shared',reason='shared_residential_backbone'
        FROM parents p WHERE p.way_id=parent.way_id;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        IF batch_rows=0 THEN next_phase:='publish'; END IF;
    ELSIF phase='publish' THEN
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS
        SELECT way_id FROM residential_access_roads WHERE last_way IS NULL OR way_id>last_way ORDER BY way_id LIMIT 5000;
        GET DIAGNOSTICS batch_rows=ROW_COUNT;
        UPDATE ways w SET tags=(w.tags-ARRAY['workouts:residential_access','workouts:residential_context_version',
            'workouts:residential_home_units','workouts:residential_access_reason','workouts:residential_context_evidence']) || jsonb_build_object(
                'workouts:residential_access',r.access_class,'workouts:residential_context_version','1',
                'workouts:residential_home_units',r.home_units::text,'workouts:residential_access_reason',r.reason,
                'workouts:residential_context_evidence',jsonb_path_query_array(r.evidence,'$[0 to 7]')::text)
        FROM residential_batch key JOIN residential_access_roads r USING(way_id) WHERE w.way_id=key.way_id;
        SELECT max(way_id) INTO next_way FROM residential_batch;
        IF batch_rows=0 THEN next_phase:='finalize'; END IF;
    ELSIF phase='finalize' THEN
        DROP VIEW residential_footprints;
        DROP TABLE residential_access_edges,residential_access_nodes,residential_boundary_nodes,residential_access_roads;
        DROP TABLE residential_building_ways,residential_building_relations,residential_landuse_ways,
            residential_landuse_relations,residential_address_nodes;
        ANALYZE ways;
        finished:=true;
    ELSE
        RAISE EXCEPTION 'unrecognized residential context phase %',phase;
    END IF;

    IF next_phase<>phase THEN next_way:=NULL; rounds:=0; END IF;
    IF finished THEN
        PERFORM osm_catalog.complete_generation_stage(target_generation,'residential-context',expected_batch,
            jsonb_build_object('version',1,'done',true,'phase','finalize','batch_index',expected_batch+1,
                'batch_total',expected_batch+1,'context_version',1),processed+batch_rows);
    ELSE
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'residential-context',expected_batch,
            jsonb_build_object('version',1,'done',false,'phase',next_phase,'last_way_id',next_way,'rounds',rounds,
                'batch_size',500,'batch_index',expected_batch+2,
                'batch_total',expected_batch+CASE WHEN next_phase='finalize' THEN 2 ELSE 3 END),processed+batch_rows);
    END IF;
END;
$procedure$;

CALL classify_residential_access(:'OSM_GENERATION_ID'::bigint);
DROP PROCEDURE classify_residential_access(bigint);
