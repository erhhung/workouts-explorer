-- +goose Up
ALTER TABLE app.map_selections ADD COLUMN focused_workout_id uuid;
ALTER TABLE app.map_selections ADD CONSTRAINT map_selections_focused_workout_fk
    FOREIGN KEY (focused_workout_id,account_id) REFERENCES app.workouts(id,account_id)
    ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE app.map_selections ADD CONSTRAINT map_selections_focused_selection_workout_fk
    FOREIGN KEY (account_id,id,focused_workout_id)
    REFERENCES app.map_selection_workouts(account_id,selection_id,workout_id)
    ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;

-- Selection workouts are populated after the parent row, so focus validity must
-- be checked at commit after both sides of the selection have been captured.
-- +goose StatementBegin
CREATE FUNCTION app.validate_map_selection_focus()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
BEGIN
    IF NEW.focused_workout_id IS NULL THEN RETURN NEW; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selection_workouts selected_workout
        JOIN app.workouts workout ON workout.id=selected_workout.workout_id
          AND workout.account_id=selected_workout.account_id
        JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
        WHERE selected_workout.account_id=NEW.account_id AND selected_workout.selection_id=NEW.id
          AND selected_workout.workout_id=NEW.focused_workout_id
          AND workout.local_start_date BETWEEN NEW.start_date AND NEW.end_date
          AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'invalid focused map selection workout' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER map_selections_focus_validate_after_write
AFTER INSERT OR UPDATE ON app.map_selections DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION app.validate_map_selection_focus();

-- Exact road-coverage rows deliberately ignore the checked-workout subset. The
-- subset controls aggregate map geometry only; all statistics use current,
-- routed workouts in the immutable selection date range.
-- +goose StatementBegin
CREATE FUNCTION app.map_selection_coverage_entities(target_account_id uuid,target_session_id uuid,
    target_selection_id uuid,target_generation bigint,target_search text DEFAULT NULL)
RETURNS TABLE(entity_id uuid,entity_kind text,broad_class text,name text,locality_name text,region_id text,region_name text,
    range_workout_count bigint,range_first_date date,range_first_workout_id uuid,
    range_latest_date date,range_latest_workout_id uuid,all_time_workout_count bigint,
    all_time_first_date date,all_time_first_workout_id uuid,all_time_latest_date date,
    all_time_latest_workout_id uuid,minimum_longitude double precision,minimum_latitude double precision,
    maximum_longitude double precision,maximum_latitude double precision)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app,public AS $$
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date,selection.end_date
        FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id=selection.session_id
        JOIN app.authentication_principals principal ON principal.id=session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id=principal.id
          AND account_owner.account_id=selection.account_id
        JOIN app.accounts account ON account.id=account_owner.account_id
        JOIN app.account_data_generations generation ON generation.account_id=selection.account_id
        WHERE selection.id=target_selection_id AND selection.account_id=target_account_id
          AND selection.session_id=target_session_id AND selection.generation=target_generation
          AND generation.generation=target_generation AND selection.expires_at>transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at>transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state='active'
          AND target_account_id=app.current_account_id()
    ), eligible AS MATERIALIZED (
        SELECT workout.id,workout.local_start_date,workout.started_at
        FROM app.workouts workout
        JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id=workout.id
          AND coverage.account_id=workout.account_id
        WHERE workout.account_id=target_account_id AND workout.deletion_requested_at IS NULL
          AND workout.local_start_date IS NOT NULL AND route.route IS NOT NULL
          AND coverage.processing_state='current'
    ), path_visits AS MATERIALIZED (
        SELECT DISTINCT eligible.id workout_id,eligible.local_start_date,eligible.started_at,
            match.logical_path_id entity_id,match.region_id
        FROM eligible JOIN app.workout_segment_matches match
          ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
        WHERE NOT (segment.highway='service' AND segment.tags->>'service' IN ('driveway','parking_aisle'))
          AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (segment.tags ? 'workouts:park_id'))
    ), park_visits AS MATERIALIZED (
        SELECT eligible.id workout_id,eligible.local_start_date,eligible.started_at,
            attribution.park_id entity_id,
            (SELECT min(match.region_id) FROM app.workout_segment_matches match
             JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
              AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
             WHERE match.account_id=target_account_id AND match.workout_id=eligible.id
               AND segment.tags->>'workouts:park_id'=attribution.park_id::text) region_id
        FROM eligible JOIN app.workout_park_attributions attribution
          ON attribution.account_id=target_account_id AND attribution.workout_id=eligible.id
    ), path_range AS (
        SELECT visit.entity_id,min(visit.region_id) region_id,count(DISTINCT visit.workout_id)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date,visit.started_at,visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC,visit.started_at,visit.workout_id))[1] latest_workout_id
        FROM path_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), path_all AS (
        SELECT visit.entity_id,count(DISTINCT visit.workout_id)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date,visit.started_at,visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC,visit.started_at,visit.workout_id))[1] latest_workout_id
        FROM path_visits visit GROUP BY visit.entity_id
    ), path_bounds AS (
        SELECT match.logical_path_id entity_id,ST_Extent(match.geom)::box2d extent
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
        WHERE NOT (segment.highway='service' AND segment.tags->>'service' IN ('driveway','parking_aisle'))
          AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (segment.tags ? 'workouts:park_id')) GROUP BY match.logical_path_id
    ), park_range AS (
        SELECT visit.entity_id,min(visit.region_id) region_id,count(DISTINCT visit.workout_id)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date,visit.started_at,visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC,visit.started_at,visit.workout_id))[1] latest_workout_id
        FROM park_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), park_all AS (
        SELECT visit.entity_id,count(DISTINCT visit.workout_id)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date,visit.started_at,visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC,visit.started_at,visit.workout_id))[1] latest_workout_id
        FROM park_visits visit GROUP BY visit.entity_id
    ), park_bounds AS (
        SELECT (segment.tags->>'workouts:park_id')::uuid entity_id,ST_Extent(match.geom)::box2d extent
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE segment.tags ? 'workouts:park_id' GROUP BY (segment.tags->>'workouts:park_id')::uuid
    ), national_path_context AS MATERIALIZED (
        SELECT match.logical_path_id,min(segment.tags->>'workouts:park_name') locality_name
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE segment.tags->>'workouts:park_kind'='national_park'
          AND NULLIF(segment.tags->>'workouts:park_name','') IS NOT NULL
        GROUP BY match.logical_path_id
    ), entities AS (
        SELECT path.logical_path_id entity_id,'path'::text entity_kind,path.broad_class,path.name,
            COALESCE(path.locality_name,national.locality_name) locality_name,
            range.region_id,catalog.display_name region_name,range.workout_count range_workout_count,range.first_date range_first_date,
            range.first_workout_id range_first_workout_id,range.latest_date range_latest_date,
            range.latest_workout_id range_latest_workout_id,all_time.workout_count all_time_workout_count,
            all_time.first_date all_time_first_date,all_time.first_workout_id all_time_first_workout_id,
            all_time.latest_date all_time_latest_date,all_time.latest_workout_id all_time_latest_workout_id,
            ST_XMin(bounds.extent) minimum_longitude,ST_YMin(bounds.extent) minimum_latitude,
            ST_XMax(bounds.extent) maximum_longitude,ST_YMax(bounds.extent) maximum_latitude
        FROM path_range range JOIN path_all all_time USING(entity_id) JOIN path_bounds bounds USING(entity_id)
        JOIN app.coverage_paths path ON path.account_id=target_account_id AND path.logical_path_id=range.entity_id
        LEFT JOIN national_path_context national ON national.logical_path_id=path.logical_path_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id=range.region_id
        UNION ALL
        SELECT park.park_id,'park'::text,'park'::text,park.name,park.locality_name,range.region_id,catalog.display_name,
            range.workout_count,range.first_date,range.first_workout_id,range.latest_date,range.latest_workout_id,
            all_time.workout_count,all_time.first_date,all_time.first_workout_id,all_time.latest_date,
            all_time.latest_workout_id,ST_XMin(bounds.extent),ST_YMin(bounds.extent),
            ST_XMax(bounds.extent),ST_YMax(bounds.extent)
        FROM park_range range JOIN park_all all_time USING(entity_id) JOIN park_bounds bounds USING(entity_id)
        JOIN app.coverage_parks park ON park.account_id=target_account_id AND park.park_id=range.entity_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id=range.region_id
    )
    SELECT * FROM entities entity WHERE NULLIF(target_search,'') IS NULL OR
        strpos(lower(concat_ws(' ',entity.entity_id::text,entity.entity_kind,entity.broad_class,
            entity.name,entity.locality_name,entity.region_name,entity.region_id)),lower(target_search))>0
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.map_selection_coverage_entity_detail(target_account_id uuid,target_session_id uuid,
    target_selection_id uuid,target_generation bigint,target_entity_kind text,target_entity_id uuid)
RETURNS TABLE(entity_id uuid,entity_kind text,broad_class text,name text,locality_name text,region_id text,region_name text,
    range_workout_count bigint,range_first_date date,range_first_workout_id uuid,
    range_latest_date date,range_latest_workout_id uuid,all_time_workout_count bigint,
    all_time_first_date date,all_time_first_workout_id uuid,all_time_latest_date date,
    all_time_latest_workout_id uuid,minimum_longitude double precision,minimum_latitude double precision,
    maximum_longitude double precision,maximum_latitude double precision,geometry jsonb,
    fit_minimum_longitude double precision,fit_minimum_latitude double precision,
    fit_maximum_longitude double precision,fit_maximum_latitude double precision)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app,public AS $$
    WITH metadata AS MATERIALIZED (
        SELECT * FROM app.map_selection_coverage_entities(target_account_id,target_session_id,
            target_selection_id,target_generation,NULL)
        WHERE entity_kind=target_entity_kind AND entity_id=target_entity_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id FROM metadata CROSS JOIN app.map_selections selection
        JOIN app.workouts workout ON workout.account_id=selection.account_id
          AND workout.local_start_date BETWEEN selection.start_date AND selection.end_date
        JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id=workout.id
          AND coverage.account_id=workout.account_id
        WHERE selection.account_id=target_account_id AND selection.id=target_selection_id
          AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
          AND coverage.processing_state='current'
    ), pieces AS (
        SELECT match.geom FROM metadata JOIN eligible ON true
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE metadata.entity_kind='path' AND match.logical_path_id=metadata.entity_id
          AND NOT (segment.highway='service' AND segment.tags->>'service' IN ('driveway','parking_aisle'))
          AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (metadata.name IS NOT NULL OR NOT (segment.tags ? 'workouts:park_id'))
        UNION ALL
        SELECT match.geom FROM metadata JOIN eligible ON true
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE metadata.entity_kind='park' AND segment.tags->>'workouts:park_id'=metadata.entity_id::text
    ), aggregate AS (
        SELECT ST_UnaryUnion(ST_Collect(geom)) geom FROM pieces
    ), projected AS (
        SELECT geom,ST_Transform(geom,3857) web_mercator FROM aggregate WHERE geom IS NOT NULL
    ), fitted AS (
        SELECT geom,ST_Transform(ST_SetSRID(ST_Envelope(ST_Expand(ST_Extent(web_mercator)::box2d,
            greatest((500-ST_XMax(ST_Extent(web_mercator)::box2d)+ST_XMin(ST_Extent(web_mercator)::box2d))/2,0),
            greatest((500-ST_YMax(ST_Extent(web_mercator)::box2d)+ST_YMin(ST_Extent(web_mercator)::box2d))/2,0))),3857),4326) fit
        FROM projected GROUP BY geom
    )
    SELECT metadata.*,ST_AsGeoJSON(fitted.geom)::jsonb,
        ST_XMin(fitted.fit),ST_YMin(fitted.fit),ST_XMax(fitted.fit),ST_YMax(fitted.fit)
    FROM metadata JOIN fitted ON true
    WHERE target_entity_kind IN ('path','park')
$$;
-- +goose StatementEnd

CREATE TABLE app.coverage_explorer_function_backup(definition text NOT NULL);
INSERT INTO app.coverage_explorer_function_backup(definition)
SELECT pg_get_functiondef('app.coverage_mvt(integer,integer,integer,json)'::regprocedure);
REVOKE ALL ON app.coverage_explorer_function_backup FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.coverage_mvt(z integer,x integer,y integer,query_params json DEFAULT '{}'::json)
RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,app,public AS $$
DECLARE target_account_id uuid; target_session_id uuid; target_selection_id uuid;
DECLARE target_generation bigint; bounds geometry; tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x<0 OR y<0 OR x>=(1::bigint<<z) OR y>=(1::bigint<<z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE='22023';
    END IF;
    IF json_typeof(query_params) IS DISTINCT FROM 'object' OR
       (SELECT count(*) FROM json_object_keys(query_params))<>4 OR
       NOT query_params::jsonb ?& ARRAY['target_account_id','target_session_id','target_selection_id','target_generation'] THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE='22023';
    END IF;
    BEGIN
        target_account_id:=(query_params->>'target_account_id')::uuid;
        target_session_id:=(query_params->>'target_session_id')::uuid;
        target_selection_id:=(query_params->>'target_selection_id')::uuid;
        target_generation:=(query_params->>'target_generation')::bigint;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE='22023';
    END;
    IF target_account_id IS NULL OR target_session_id IS NULL OR target_selection_id IS NULL OR target_generation<1 THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id=selection.session_id
        JOIN app.authentication_principals principal ON principal.id=session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id=principal.id AND account_owner.account_id=selection.account_id
        JOIN app.accounts account ON account.id=account_owner.account_id
        JOIN app.account_data_generations generation ON generation.account_id=selection.account_id
        WHERE selection.id=target_selection_id AND selection.account_id=target_account_id
          AND selection.session_id=target_session_id AND selection.generation=target_generation
          AND generation.generation=target_generation AND selection.expires_at>transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at>transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state='active'
    ) THEN RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE='42501'; END IF;
    bounds:=ST_TileEnvelope(z,x,y);
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date,selection.end_date,selection.focused_workout_id
        FROM app.map_selections selection WHERE selection.account_id=target_account_id AND selection.id=target_selection_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id,workout.local_start_date FROM app.workouts workout
        JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
        JOIN app.workout_coverage_states coverage ON coverage.account_id=workout.account_id AND coverage.workout_id=workout.id
        CROSS JOIN selected WHERE workout.account_id=target_account_id AND workout.deletion_requested_at IS NULL
          AND route.route IS NOT NULL AND coverage.processing_state='current'
          AND workout.local_start_date BETWEEN selected.start_date AND selected.end_date
    ), path_counts AS MATERIALIZED (
        SELECT match.logical_path_id entity_id,count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=eligible.id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
        WHERE NOT (segment.highway='service' AND segment.tags->>'service' IN ('driveway','parking_aisle'))
          AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (segment.tags ? 'workouts:park_id')) GROUP BY match.logical_path_id
    ), park_counts AS MATERIALIZED (
        SELECT attribution.park_id entity_id,count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_park_attributions attribution
          ON attribution.account_id=target_account_id AND attribution.workout_id=eligible.id GROUP BY attribution.park_id
    ), checked AS MATERIALIZED (
        SELECT match.*,segment.highway,segment.tags FROM app.map_selection_workouts selected_workout
        JOIN eligible ON eligible.id=selected_workout.workout_id
        JOIN app.workout_segment_matches match ON match.account_id=selected_workout.account_id
          AND match.workout_id=selected_workout.workout_id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE selected_workout.account_id=target_account_id AND selected_workout.selection_id=target_selection_id
          AND match.geom && ST_Transform(bounds,4326)
    ), path_geometry AS MATERIALIZED (
        SELECT checked.logical_path_id entity_id,ST_UnaryUnion(ST_Collect(checked.geom)) geom FROM checked
        JOIN app.coverage_paths path ON path.account_id=checked.account_id AND path.logical_path_id=checked.logical_path_id
        WHERE NOT (checked.highway='service' AND checked.tags->>'service' IN ('driveway','parking_aisle'))
          AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (checked.tags ? 'workouts:park_id')) GROUP BY checked.logical_path_id
    ), park_geometry AS MATERIALIZED (
        SELECT (tags->>'workouts:park_id')::uuid entity_id,ST_UnaryUnion(ST_Collect(geom)) geom FROM checked
        WHERE tags ? 'workouts:park_id' GROUP BY (tags->>'workouts:park_id')::uuid
    ), path_identity AS MATERIALIZED (
        SELECT path.logical_path_id entity_id,path.broad_class,path.name,
            COALESCE(path.locality_name,min(checked.tags->>'workouts:park_name')
                FILTER (WHERE checked.tags->>'workouts:park_kind'='national_park')) locality_name,
            min(checked.region_id) region_id
        FROM app.coverage_paths path JOIN checked
          ON checked.account_id=path.account_id AND checked.logical_path_id=path.logical_path_id
        WHERE path.account_id=target_account_id
          AND NOT (checked.highway='service' AND checked.tags->>'service' IN ('driveway','parking_aisle'))
          AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (checked.tags ? 'workouts:park_id'))
        GROUP BY path.logical_path_id,path.broad_class,path.name,path.locality_name
    ), park_identity AS MATERIALIZED (
        SELECT park.park_id entity_id,'park'::text broad_class,park.name,park.locality_name,min(segment.region_id) region_id
        FROM app.coverage_parks park JOIN app.path_segments segment ON segment.account_id=park.account_id
          AND segment.tags->>'workouts:park_id'=park.park_id::text WHERE park.account_id=target_account_id
        GROUP BY park.park_id,park.name,park.locality_name
    ), focus_path_geometry AS MATERIALIZED (
        SELECT match.logical_path_id entity_id,ST_UnaryUnion(ST_Collect(match.geom)) geom FROM selected
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=selected.focused_workout_id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
        WHERE selected.focused_workout_id IS NOT NULL AND match.geom && ST_Transform(bounds,4326)
          AND NOT (segment.highway='service' AND segment.tags->>'service' IN ('driveway','parking_aisle'))
          AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
          AND (path.name IS NOT NULL OR NOT (segment.tags ? 'workouts:park_id')) GROUP BY match.logical_path_id
    ), focus_park_geometry AS MATERIALIZED (
        SELECT (segment.tags->>'workouts:park_id')::uuid entity_id,ST_UnaryUnion(ST_Collect(match.geom)) geom FROM selected
        JOIN app.workout_segment_matches match ON match.account_id=target_account_id AND match.workout_id=selected.focused_workout_id
        JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
          AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
        WHERE selected.focused_workout_id IS NOT NULL AND segment.tags ? 'workouts:park_id'
          AND match.geom && ST_Transform(bounds,4326) GROUP BY (segment.tags->>'workouts:park_id')::uuid
    )
    SELECT COALESCE((SELECT ST_AsMVT(rows,'coverage',4096,'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text,'-','')) entity_id,'path'::text entity_kind,
          identity.broad_class,identity.name,identity.locality_name,identity.region_id,counts.workout_count range_workout_count,
          app.coverage_count_bucket(counts.workout_count) count_bucket,
          ST_AsMVTGeom(ST_Transform(geometry.geom,3857),bounds::box2d,4096,64,true) geometry
        FROM path_geometry geometry JOIN path_counts counts USING(entity_id) JOIN path_identity identity USING(entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom,3857),bounds)) rows WHERE geometry IS NOT NULL),'') ||
      COALESCE((SELECT ST_AsMVT(rows,'coverage_parks',4096,'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text,'-','')) entity_id,'park'::text entity_kind,
          identity.broad_class,identity.name,identity.locality_name,identity.region_id,counts.workout_count range_workout_count,
          app.coverage_count_bucket(counts.workout_count) count_bucket,
          ST_AsMVTGeom(ST_Transform(geometry.geom,3857),bounds::box2d,4096,64,true) geometry
        FROM park_geometry geometry JOIN park_counts counts USING(entity_id) JOIN park_identity identity USING(entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom,3857),bounds)) rows WHERE geometry IS NOT NULL),'') ||
      COALESCE((SELECT ST_AsMVT(rows,'coverage_focus',4096,'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text,'-','')) entity_id,'path'::text entity_kind,
          identity.broad_class,identity.name,identity.locality_name,identity.region_id,counts.workout_count range_workout_count,
          app.coverage_count_bucket(counts.workout_count) count_bucket,
          ST_AsMVTGeom(ST_Transform(geometry.geom,3857),bounds::box2d,4096,64,true) geometry
        FROM focus_path_geometry geometry JOIN path_counts counts USING(entity_id) JOIN path_identity identity USING(entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom,3857),bounds)) rows WHERE geometry IS NOT NULL),'') ||
      COALESCE((SELECT ST_AsMVT(rows,'coverage_parks_focus',4096,'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text,'-','')) entity_id,'park'::text entity_kind,
          identity.broad_class,identity.name,identity.locality_name,identity.region_id,counts.workout_count range_workout_count,
          app.coverage_count_bucket(counts.workout_count) count_bucket,
          ST_AsMVTGeom(ST_Transform(geometry.geom,3857),bounds::box2d,4096,64,true) geometry
        FROM focus_park_geometry geometry JOIN park_counts counts USING(entity_id) JOIN park_identity identity USING(entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom,3857),bounds)) rows WHERE geometry IS NOT NULL),'') INTO tile;
    RETURN COALESCE(tile,''::bytea);
END;
$$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.validate_map_selection_focus() OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_mvt(integer,integer,integer,json) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.validate_map_selection_focus(),
    app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text),
    app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid),
    app.coverage_mvt(integer,integer,integer,json)
FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text),
    app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.coverage_mvt(integer,integer,integer,json) TO workouts_tiles;

UPDATE app.schema_metadata SET schema_version=19,minimum_runtime_version=18 WHERE singleton;

-- +goose Down
UPDATE app.schema_metadata SET schema_version=18,minimum_runtime_version=17 WHERE singleton;
-- +goose StatementBegin
DO $$
DECLARE saved_definition text;
BEGIN
    SELECT definition INTO saved_definition FROM app.coverage_explorer_function_backup;
    EXECUTE saved_definition;
END;
$$;
-- +goose StatementEnd
DROP TABLE app.coverage_explorer_function_backup;
DROP FUNCTION app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid);
DROP FUNCTION app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text);
DROP TRIGGER map_selections_focus_validate_after_write ON app.map_selections;
DROP FUNCTION app.validate_map_selection_focus();
ALTER TABLE app.map_selections DROP CONSTRAINT map_selections_focused_selection_workout_fk;
ALTER TABLE app.map_selections DROP CONSTRAINT map_selections_focused_workout_fk;
ALTER TABLE app.map_selections DROP COLUMN focused_workout_id;
