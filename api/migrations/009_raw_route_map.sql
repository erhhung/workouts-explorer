-- +goose Up
-- PostGIS is an infrastructure prerequisite because application migrations run
-- without superuser privileges in production.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname='postgis') THEN
        RAISE EXCEPTION 'PostGIS must be installed before applying schema 9';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE app.workout_routes ADD COLUMN route geometry(LineString,4326);
ALTER TABLE app.workout_routes ADD CONSTRAINT workout_routes_route_check CHECK (
    route IS NULL OR (GeometryType(route)='LINESTRING' AND ST_SRID(route)=4326)
);

-- The migration role owns these tables, but forced RLS still applies to owners.
-- Relax it only for the transactional spatial backfill and restore it immediately.
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;
WITH route_geometry AS (
    SELECT point.account_id,point.workout_id,
           ST_SetSRID(ST_MakeLine(ST_MakePoint(point.longitude,point.latitude) ORDER BY point.sequence),4326) AS route
      FROM app.workout_route_points point
     GROUP BY point.account_id,point.workout_id
    HAVING count(*) >= 2
)
UPDATE app.workout_routes summary SET route=route_geometry.route
  FROM route_geometry
 WHERE summary.account_id=route_geometry.account_id AND summary.workout_id=route_geometry.workout_id;
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;

CREATE INDEX workout_routes_route_gist_idx ON app.workout_routes USING gist(route);

CREATE TABLE app.account_data_generations (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE CASCADE,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);
INSERT INTO app.account_data_generations(account_id)
SELECT id FROM app.accounts;

CREATE TABLE app.map_selections (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    session_id uuid NOT NULL REFERENCES app.sessions(id) ON DELETE CASCADE,
    generation bigint NOT NULL CHECK (generation > 0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (account_id,id),
    FOREIGN KEY (account_id) REFERENCES app.accounts(id) ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);
CREATE INDEX map_selections_expiry_idx ON app.map_selections(expires_at);
CREATE INDEX map_selections_session_idx ON app.map_selections(session_id,expires_at);

CREATE TABLE app.map_selection_workouts (
    account_id uuid NOT NULL,
    selection_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    sort_order integer NOT NULL CHECK (sort_order >= 0),
    PRIMARY KEY (account_id,selection_id,workout_id),
    UNIQUE (account_id,selection_id,sort_order),
    FOREIGN KEY (account_id,selection_id) REFERENCES app.map_selections(account_id,id) ON DELETE CASCADE,
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);

-- +goose StatementBegin
CREATE FUNCTION app.current_session_id()
RETURNS uuid
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
    SELECT NULLIF(current_setting('app.session_id',true),'')::uuid
$$;
-- +goose StatementEnd

ALTER TABLE app.account_data_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_data_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY account_data_generations_account_policy ON app.account_data_generations
    USING (account_id=app.current_account_id());
CREATE POLICY account_data_generations_owner_policy ON app.account_data_generations
    TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.map_selections ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.map_selections FORCE ROW LEVEL SECURITY;
CREATE POLICY map_selections_account_policy ON app.map_selections
    USING (account_id=app.current_account_id() AND session_id=app.current_session_id())
    WITH CHECK (account_id=app.current_account_id() AND session_id=app.current_session_id());
CREATE POLICY map_selections_owner_policy ON app.map_selections
    TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.map_selection_workouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.map_selection_workouts FORCE ROW LEVEL SECURITY;
CREATE POLICY map_selection_workouts_account_policy ON app.map_selection_workouts
    USING (account_id=app.current_account_id() AND EXISTS (
        SELECT 1 FROM app.map_selections selection
         WHERE selection.account_id=map_selection_workouts.account_id
           AND selection.id=map_selection_workouts.selection_id
           AND selection.session_id=app.current_session_id()
    ))
    WITH CHECK (account_id=app.current_account_id() AND EXISTS (
        SELECT 1 FROM app.map_selections selection
         WHERE selection.account_id=map_selection_workouts.account_id
           AND selection.id=map_selection_workouts.selection_id
           AND selection.session_id=app.current_session_id()
    ));
CREATE POLICY map_selection_workouts_owner_policy ON app.map_selection_workouts
    TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY workout_types_map_owner_policy ON app.workout_types
    FOR SELECT TO workouts_security_owner USING (true);
CREATE POLICY workout_routes_map_owner_policy ON app.workout_routes
    FOR SELECT TO workouts_security_owner USING (true);
CREATE POLICY workout_route_points_map_owner_policy ON app.workout_route_points
    FOR SELECT TO workouts_security_owner USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.advance_account_data_generation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
BEGIN
    IF TG_OP='DELETE' THEN target_account_id:=OLD.account_id; ELSE target_account_id:=NEW.account_id; END IF;
    UPDATE app.account_data_generations SET
        generation=CASE WHEN generation=9223372036854775807 THEN 1 ELSE generation+1 END,
        updated_at=transaction_timestamp()
     WHERE account_id=target_account_id;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- New accounts need a generation before their first map selection.
-- +goose StatementBegin
CREATE FUNCTION app.seed_account_data_generation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    INSERT INTO app.account_data_generations(account_id) VALUES(NEW.id) ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER accounts_data_generation_after_insert
AFTER INSERT ON app.accounts FOR EACH ROW EXECUTE FUNCTION app.seed_account_data_generation();

CREATE TRIGGER workout_routes_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_routes
FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation();
CREATE TRIGGER workouts_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workouts
FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation();
CREATE TRIGGER workout_types_data_generation_after_update
AFTER UPDATE OF type_key,provider_label ON app.workout_types
FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation();

-- The API inserts supplied UUIDs directly. This definer trigger binds each row to
-- a live owner session and to the current account generation without exposing sessions.
-- +goose StatementBegin
CREATE FUNCTION app.validate_map_selection()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        RAISE EXCEPTION 'map selections are immutable' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.sessions session_row
        JOIN app.authentication_principals principal ON principal.id=session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id=principal.id
        JOIN app.accounts account ON account.id=account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id=account.id
        WHERE session_row.id=NEW.session_id AND account_owner.account_id=NEW.account_id
          AND session_row.revoked_at IS NULL AND session_row.expires_at>transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state='active'
          AND NEW.expires_at<=session_row.expires_at AND NEW.expires_at>transaction_timestamp()
          AND data_generation.generation=NEW.generation
    ) THEN
        RAISE EXCEPTION 'invalid map selection scope' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER map_selections_validate_before_write
BEFORE INSERT OR UPDATE ON app.map_selections
FOR EACH ROW EXECUTE FUNCTION app.validate_map_selection();

-- +goose StatementBegin
CREATE FUNCTION app.validate_map_selection_workout()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        RAISE EXCEPTION 'selected workouts are immutable' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.workouts workout ON workout.id=NEW.workout_id AND workout.account_id=selection.account_id
        JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
        WHERE selection.id=NEW.selection_id AND selection.account_id=NEW.account_id
          AND selection.expires_at>transaction_timestamp()
          AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'invalid selected workout' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER map_selection_workouts_validate_before_write
BEFORE INSERT OR UPDATE ON app.map_selection_workouts
FOR EACH ROW EXECUTE FUNCTION app.validate_map_selection_workout();

-- +goose StatementBegin
CREATE FUNCTION app.cleanup_expired_map_selections()
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    deleted_count integer;
BEGIN
    DELETE FROM app.map_selections WHERE expires_at<=transaction_timestamp();
    GET DIAGNOSTICS deleted_count=ROW_COUNT;
    RETURN deleted_count;
END;
$$;
-- +goose StatementEnd

-- Serialize selection capture with ingest and deletion invalidation without
-- granting the API direct update authority over account generations.
-- +goose StatementBegin
CREATE FUNCTION app.lock_account_data_generation()
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    current_generation bigint;
BEGIN
    SELECT generation INTO current_generation
      FROM app.account_data_generations
     WHERE account_id=app.current_account_id()
     FOR UPDATE;
    IF current_generation IS NULL THEN
        RAISE EXCEPTION 'account data generation is unavailable' USING ERRCODE='55000';
    END IF;
    RETURN current_generation;
END;
$$;
-- +goose StatementEnd

-- Preserve the runtime-8 worker signature while atomically rebuilding map geometry.
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

-- pg_tileserv receives only API-approved opaque scope values. The function
-- validates all of them again before reading any private relation.
-- +goose StatementBegin
CREATE FUNCTION app.raw_route_mvt(z integer,x integer,y integer,target_account_id uuid,
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

REVOKE ALL ON app.account_data_generations,app.map_selections,app.map_selection_workouts
    FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;
REVOKE ALL ON FUNCTION app.advance_account_data_generation(),app.seed_account_data_generation(),
    app.validate_map_selection(),app.validate_map_selection_workout(),app.cleanup_expired_map_selections(),
    app.lock_account_data_generation(),app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)
    FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;
REVOKE ALL ON FUNCTION app.current_session_id() FROM PUBLIC,workouts_api,workouts_worker,workouts_tiles;
GRANT SELECT ON app.account_data_generations TO workouts_api;
GRANT SELECT,INSERT,DELETE ON app.map_selections,app.map_selection_workouts TO workouts_api;
GRANT EXECUTE ON FUNCTION app.cleanup_expired_map_selections() TO workouts_api;
GRANT EXECUTE ON FUNCTION app.lock_account_data_generation() TO workouts_api;
GRANT EXECUTE ON FUNCTION app.current_session_id() TO workouts_api;
GRANT USAGE ON SCHEMA app TO workouts_tiles;
GRANT EXECUTE ON FUNCTION app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint) TO workouts_tiles;
GRANT SELECT,INSERT,UPDATE,DELETE ON app.account_data_generations,app.map_selections,app.map_selection_workouts TO workouts_security_owner;
GRANT SELECT(id,expires_at) ON app.sessions TO workouts_security_owner;
GRANT SELECT ON app.workout_types TO workouts_security_owner;
GRANT SELECT ON app.workout_route_points TO workouts_security_owner;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.current_session_id() OWNER TO workouts_security_owner;
ALTER FUNCTION app.advance_account_data_generation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.seed_account_data_generation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.validate_map_selection() OWNER TO workouts_security_owner;
ALTER FUNCTION app.validate_map_selection_workout() OWNER TO workouts_security_owner;
ALTER FUNCTION app.cleanup_expired_map_selections() OWNER TO workouts_security_owner;
ALTER FUNCTION app.lock_account_data_generation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

UPDATE app.schema_metadata SET schema_version=9,minimum_runtime_version=8 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version=8,minimum_runtime_version=8 WHERE singleton;

REVOKE EXECUTE ON FUNCTION app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint) FROM workouts_tiles;
REVOKE USAGE ON SCHEMA app FROM workouts_tiles;
REVOKE EXECUTE ON FUNCTION app.cleanup_expired_map_selections() FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.lock_account_data_generation() FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.current_session_id() FROM workouts_api;
REVOKE SELECT ON app.account_data_generations FROM workouts_api;
REVOKE SELECT,INSERT,DELETE ON app.map_selections,app.map_selection_workouts FROM workouts_api;
REVOKE SELECT(id,expires_at) ON app.sessions FROM workouts_security_owner;
REVOKE SELECT ON app.workout_types FROM workouts_security_owner;
REVOKE SELECT ON app.workout_route_points FROM workouts_security_owner;

DROP FUNCTION app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint);
DROP FUNCTION app.lock_account_data_generation();
DROP FUNCTION app.cleanup_expired_map_selections();
DROP TRIGGER map_selection_workouts_validate_before_write ON app.map_selection_workouts;
DROP FUNCTION app.validate_map_selection_workout();
DROP TRIGGER map_selections_validate_before_write ON app.map_selections;
DROP FUNCTION app.validate_map_selection();
DROP TABLE app.map_selection_workouts;
DROP TABLE app.map_selections;
DROP FUNCTION app.current_session_id();
DROP POLICY workout_route_points_map_owner_policy ON app.workout_route_points;
DROP POLICY workout_routes_map_owner_policy ON app.workout_routes;
DROP POLICY workout_types_map_owner_policy ON app.workout_types;

DROP TRIGGER workout_types_data_generation_after_update ON app.workout_types;
DROP TRIGGER workouts_data_generation_after_write ON app.workouts;
DROP TRIGGER workout_routes_data_generation_after_write ON app.workout_routes;
DROP TRIGGER accounts_data_generation_after_insert ON app.accounts;
DROP FUNCTION app.seed_account_data_generation();
DROP FUNCTION app.advance_account_data_generation();
DROP TABLE app.account_data_generations;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.replace_workout_route_summary(target_workout_id uuid, new_point_count integer,
    new_minimum_longitude double precision, new_minimum_latitude double precision,
    new_maximum_longitude double precision, new_maximum_latitude double precision,
    new_minimum_altitude double precision, new_maximum_altitude double precision,
    new_elevation_gain double precision, new_has_complete_altitude boolean)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
    authorized boolean;
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
    INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
        maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude)
    VALUES(target_account_id,target_workout_id,new_point_count,new_minimum_longitude,new_minimum_latitude,
        new_maximum_longitude,new_maximum_latitude,new_minimum_altitude,new_maximum_altitude,new_elevation_gain,new_has_complete_altitude)
    ON CONFLICT(account_id,workout_id) DO UPDATE SET
        point_count=EXCLUDED.point_count,minimum_longitude=EXCLUDED.minimum_longitude,minimum_latitude=EXCLUDED.minimum_latitude,
        maximum_longitude=EXCLUDED.maximum_longitude,maximum_latitude=EXCLUDED.maximum_latitude,
        minimum_altitude=EXCLUDED.minimum_altitude,maximum_altitude=EXCLUDED.maximum_altitude,
        elevation_gain=EXCLUDED.elevation_gain,has_complete_altitude=EXCLUDED.has_complete_altitude,
        updated_at=transaction_timestamp();
    RETURN true;
END;
$$;
-- +goose StatementEnd

DROP INDEX app.workout_routes_route_gist_idx;
ALTER TABLE app.workout_routes DROP COLUMN route;
-- PostGIS may be shared by later schemas and is intentionally not dropped.
