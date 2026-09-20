-- +goose Up
ALTER TABLE app.workout_routes DROP CONSTRAINT workout_routes_route_check;
ALTER TABLE app.workout_routes ALTER COLUMN route TYPE geometry(MultiLineString,4326)
    USING ST_Multi(route);
ALTER TABLE app.workout_routes ADD CONSTRAINT workout_routes_route_check CHECK (
    route IS NULL OR (GeometryType(route)='MULTILINESTRING' AND ST_SRID(route)=4326)
);

-- A positive gap starts a new segment when it is at least three times the
-- running average for the current segment. Duplicate or backward timestamps
-- remain ordered points but do not distort the positive-delta baseline.
-- +goose StatementBegin
CREATE FUNCTION app.build_segmented_workout_route(target_account_id uuid,target_workout_id uuid)
RETURNS geometry
LANGUAGE sql
STABLE
SET search_path = pg_catalog, app, public
AS $$
    WITH RECURSIVE ordered AS (
        SELECT point.sequence,point.recorded_at,point.latitude,point.longitude,
               row_number() OVER (ORDER BY point.sequence) AS position
          FROM app.workout_route_points point
         WHERE point.account_id=target_account_id AND point.workout_id=target_workout_id
    ), segmented(position,sequence,recorded_at,latitude,longitude,segment_id,positive_delta_total,positive_delta_count) AS (
        SELECT point.position,point.sequence,point.recorded_at,point.latitude,point.longitude,
               0::bigint,interval '0',0::bigint
          FROM ordered point WHERE point.position=1
        UNION ALL
        SELECT point.position,point.sequence,point.recorded_at,point.latitude,point.longitude,
               prior.segment_id + CASE WHEN decision.starts_segment THEN 1 ELSE 0 END,
               CASE
                   WHEN decision.starts_segment THEN interval '0'
                   WHEN delta.value>interval '0' THEN prior.positive_delta_total+delta.value
                   ELSE prior.positive_delta_total
               END,
               CASE
                   WHEN decision.starts_segment THEN 0
                   WHEN delta.value>interval '0' THEN prior.positive_delta_count+1
                   ELSE prior.positive_delta_count
               END
          FROM segmented prior
          JOIN ordered point ON point.position=prior.position+1
          CROSS JOIN LATERAL (SELECT point.recorded_at-prior.recorded_at AS value) delta
          CROSS JOIN LATERAL (
              SELECT delta.value>interval '0'
                 AND prior.positive_delta_count>0
                 AND delta.value>=3*(prior.positive_delta_total/prior.positive_delta_count::double precision)
                 AS starts_segment
          ) decision
    ), segment_lines AS (
        SELECT segment_id,
               ST_SetSRID(ST_MakeLine(ST_MakePoint(longitude,latitude) ORDER BY sequence),4326) AS segment_line
          FROM segmented
         GROUP BY segment_id
        HAVING count(*)>=2
    )
    SELECT CASE WHEN count(*)=0 THEN NULL::geometry
                ELSE ST_Multi(ST_Collect(segment_line ORDER BY segment_id)) END
      FROM segment_lines
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.build_segmented_workout_route(uuid,uuid)
    FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;

-- Forced RLS applies to the migration owner. Relax it only for this
-- transactional rebuild, then immediately restore it.
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;
UPDATE app.workout_routes summary
   SET route=app.build_segmented_workout_route(summary.account_id,summary.workout_id)
 WHERE summary.point_count>=2;
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;

-- Keep the worker-facing signature stable while applying segmentation to all
-- future route replacements.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.replace_workout_route_summary(target_workout_id uuid, new_point_count integer,
    new_minimum_longitude double precision, new_minimum_latitude double precision,
    new_maximum_longitude double precision, new_maximum_latitude double precision,
    new_minimum_altitude double precision, new_maximum_altitude double precision,
    new_elevation_gain double precision, new_has_complete_altitude boolean)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $$
DECLARE
    target_account_id uuid;
    authorized boolean;
    new_route geometry;
BEGIN
    target_account_id:=app.current_account_id();
    SELECT EXISTS (
        SELECT 1 FROM app.workouts workout
        JOIN app.source_files file ON file.id=workout.source_file_id AND file.account_id=workout.account_id
        JOIN app.ingest_write_capabilities capability
          ON capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
         AND capability.account_id=workout.account_id AND capability.source_id=workout.source_id
         AND capability.job_id=file.job_id
        WHERE workout.id=target_workout_id AND workout.account_id=target_account_id
    ) INTO authorized;
    IF NOT authorized THEN
        RAISE EXCEPTION 'route summary write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    IF new_point_count=0 THEN
        DELETE FROM app.workout_routes WHERE account_id=target_account_id AND workout_id=target_workout_id;
        RETURN true;
    END IF;
    IF new_point_count>=2 THEN
        SELECT app.build_segmented_workout_route(target_account_id,target_workout_id) INTO new_route;
    END IF;
    INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
        maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude,route)
    VALUES(target_account_id,target_workout_id,new_point_count,new_minimum_longitude,new_minimum_latitude,
        new_maximum_longitude,new_maximum_latitude,new_minimum_altitude,new_maximum_altitude,new_elevation_gain,
        new_has_complete_altitude,new_route)
    ON CONFLICT(account_id,workout_id) DO UPDATE SET
        point_count=EXCLUDED.point_count,minimum_longitude=EXCLUDED.minimum_longitude,minimum_latitude=EXCLUDED.minimum_latitude,
        maximum_longitude=EXCLUDED.maximum_longitude,maximum_latitude=EXCLUDED.maximum_latitude,
        minimum_altitude=EXCLUDED.minimum_altitude,maximum_altitude=EXCLUDED.maximum_altitude,
        elevation_gain=EXCLUDED.elevation_gain,has_complete_altitude=EXCLUDED.has_complete_altitude,
        route=EXCLUDED.route,updated_at=transaction_timestamp();
    RETURN true;
END;
$$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.build_segmented_workout_route(uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

-- Preserve zero-length route components as point features. Line renderers cannot
-- paint repeated coordinates, and ST_AsMVTGeom drops them during quantization.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.raw_route_mvt(z integer,x integer,y integer,target_account_id uuid,
    target_session_id uuid,target_selection_id uuid,target_generation bigint)
RETURNS bytea
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $$
DECLARE
    bounds geometry;
    tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x<0 OR y<0 OR
       x>=(1::bigint<<z) OR y>=(1::bigint<<z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id=selection.session_id
        JOIN app.authentication_principals principal ON principal.id=session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id=principal.id AND account_owner.account_id=selection.account_id
        JOIN app.accounts account ON account.id=account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id=selection.account_id
        WHERE selection.id=target_selection_id AND selection.account_id=target_account_id
          AND selection.session_id=target_session_id AND selection.generation=target_generation
          AND data_generation.generation=target_generation
          AND selection.expires_at>transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at>transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state='active'
    ) THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE='42501';
    END IF;
    bounds:=ST_TileEnvelope(z,x,y);
    SELECT ST_AsMVT(tile_rows,'routes',4096,'geometry') INTO tile
      FROM (
        SELECT upper(replace(workout.id::text,'-','')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
               ST_AsMVTGeom(ST_Transform(route.route,3857),bounds,4096,64,true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id=selected.workout_id AND workout.account_id=selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id=workout.workout_type_id AND workout_type.account_id=workout.account_id
          JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
         WHERE selected.selection_id=target_selection_id AND selected.account_id=target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds,4326)
        UNION ALL
        SELECT upper(replace(workout.id::text,'-','')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
               ST_AsMVTGeom(ST_Transform(ST_StartPoint(component.geom),3857),bounds,4096,64,true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id=selected.workout_id AND workout.account_id=selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id=workout.workout_type_id AND workout_type.account_id=workout.account_id
          JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
          CROSS JOIN LATERAL ST_Dump(route.route) component
         WHERE selected.selection_id=target_selection_id AND selected.account_id=target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND ST_Length(component.geom)=0
           AND component.geom && ST_Transform(bounds,4326)
      ) tile_rows;
    RETURN COALESCE(tile,''::bytea);
END;
$$;
-- +goose StatementEnd

UPDATE app.schema_metadata SET schema_version=10,minimum_runtime_version=8 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version=9,minimum_runtime_version=8 WHERE singleton;

-- Restore schema 9's line-only tile output before reverting route geometry.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.raw_route_mvt(z integer,x integer,y integer,target_account_id uuid,
    target_session_id uuid,target_selection_id uuid,target_generation bigint)
RETURNS bytea
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $$
DECLARE
    bounds geometry;
    tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x<0 OR y<0 OR
       x>=(1::bigint<<z) OR y>=(1::bigint<<z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id=selection.session_id
        JOIN app.authentication_principals principal ON principal.id=session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id=principal.id AND account_owner.account_id=selection.account_id
        JOIN app.accounts account ON account.id=account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id=selection.account_id
        WHERE selection.id=target_selection_id AND selection.account_id=target_account_id
          AND selection.session_id=target_session_id AND selection.generation=target_generation
          AND data_generation.generation=target_generation
          AND selection.expires_at>transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at>transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state='active'
    ) THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE='42501';
    END IF;
    bounds:=ST_TileEnvelope(z,x,y);
    SELECT ST_AsMVT(tile_rows,'routes',4096,'geometry') INTO tile
      FROM (
        SELECT upper(replace(workout.id::text,'-','')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
               ST_AsMVTGeom(ST_Transform(route.route,3857),bounds,4096,64,true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id=selected.workout_id AND workout.account_id=selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id=workout.workout_type_id AND workout_type.account_id=workout.account_id
          JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
         WHERE selected.selection_id=target_selection_id AND selected.account_id=target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds,4326)
         ORDER BY selected.sort_order
      ) tile_rows;
    RETURN COALESCE(tile,''::bytea);
END;
$$;
-- +goose StatementEnd

-- Restore schema 9's contiguous geometry replacement before removing the
-- segmented geometry helper.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.replace_workout_route_summary(target_workout_id uuid, new_point_count integer,
    new_minimum_longitude double precision, new_minimum_latitude double precision,
    new_maximum_longitude double precision, new_maximum_latitude double precision,
    new_minimum_altitude double precision, new_maximum_altitude double precision,
    new_elevation_gain double precision, new_has_complete_altitude boolean)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $$
DECLARE
    target_account_id uuid;
    authorized boolean;
    new_route geometry;
BEGIN
    target_account_id:=app.current_account_id();
    SELECT EXISTS (
        SELECT 1 FROM app.workouts workout
        JOIN app.source_files file ON file.id=workout.source_file_id AND file.account_id=workout.account_id
        JOIN app.ingest_write_capabilities capability
          ON capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
         AND capability.account_id=workout.account_id AND capability.source_id=workout.source_id
         AND capability.job_id=file.job_id
        WHERE workout.id=target_workout_id AND workout.account_id=target_account_id
    ) INTO authorized;
    IF NOT authorized THEN
        RAISE EXCEPTION 'route summary write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    IF new_point_count=0 THEN
        DELETE FROM app.workout_routes WHERE account_id=target_account_id AND workout_id=target_workout_id;
        RETURN true;
    END IF;
    IF new_point_count>=2 THEN
        SELECT ST_SetSRID(ST_MakeLine(ST_MakePoint(point.longitude,point.latitude) ORDER BY point.sequence),4326)
          INTO new_route FROM app.workout_route_points point
         WHERE point.account_id=target_account_id AND point.workout_id=target_workout_id;
    END IF;
    INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
        maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude,route)
    VALUES(target_account_id,target_workout_id,new_point_count,new_minimum_longitude,new_minimum_latitude,
        new_maximum_longitude,new_maximum_latitude,new_minimum_altitude,new_maximum_altitude,new_elevation_gain,
        new_has_complete_altitude,new_route)
    ON CONFLICT(account_id,workout_id) DO UPDATE SET
        point_count=EXCLUDED.point_count,minimum_longitude=EXCLUDED.minimum_longitude,minimum_latitude=EXCLUDED.minimum_latitude,
        maximum_longitude=EXCLUDED.maximum_longitude,maximum_latitude=EXCLUDED.maximum_latitude,
        minimum_altitude=EXCLUDED.minimum_altitude,maximum_altitude=EXCLUDED.maximum_altitude,
        elevation_gain=EXCLUDED.elevation_gain,has_complete_altitude=EXCLUDED.has_complete_altitude,
        route=EXCLUDED.route,updated_at=transaction_timestamp();
    RETURN true;
END;
$$;
-- +goose StatementEnd

ALTER FUNCTION app.build_segmented_workout_route(uuid,uuid) OWNER TO workouts_migration;
DROP FUNCTION app.build_segmented_workout_route(uuid,uuid);

ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_routes DROP CONSTRAINT workout_routes_route_check;
UPDATE app.workout_routes SET route=NULL;
ALTER TABLE app.workout_routes ALTER COLUMN route TYPE geometry(LineString,4326)
    USING NULL::geometry(LineString,4326);
ALTER TABLE app.workout_routes ADD CONSTRAINT workout_routes_route_check CHECK (
    route IS NULL OR (GeometryType(route)='LINESTRING' AND ST_SRID(route)=4326)
);
WITH route_geometry AS (
    SELECT point.account_id,point.workout_id,
           ST_SetSRID(ST_MakeLine(ST_MakePoint(point.longitude,point.latitude) ORDER BY point.sequence),4326) AS route
      FROM app.workout_route_points point
     GROUP BY point.account_id,point.workout_id
    HAVING count(*)>=2
)
UPDATE app.workout_routes summary SET route=route_geometry.route
  FROM route_geometry
 WHERE summary.account_id=route_geometry.account_id AND summary.workout_id=route_geometry.workout_id;
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
