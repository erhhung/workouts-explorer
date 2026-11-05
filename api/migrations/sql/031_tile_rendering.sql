-- +goose Up
-- Derived render storage only: GPS, match evidence, canonical identities and
-- counts remain authoritative. Serialize the snapshot with both producers.
LOCK TABLE app.workout_routes, app.workout_segment_matches IN SHARE ROW EXCLUSIVE MODE;

-- The status writer has a literal runtime fence. Advance only that fence when
-- publishing min31; otherwise every ingest phase would fail after this upgrade.
-- +goose StatementBegin
DO $block$
DECLARE definition text;
BEGIN
    IF (SELECT md5(prosrc) FROM pg_proc WHERE oid='app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)'::regprocedure)
        IS DISTINCT FROM '4a4c2df5b28b6287774bb61c67fa5d44' THEN
        RAISE EXCEPTION 'unexpected ingest phase definition before runtime31' USING ERRCODE = '55000';
    END IF;
    definition := pg_get_functiondef('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)'::regprocedure);
    definition := replace(definition, 'minimum_runtime_version <= 30', 'minimum_runtime_version <= 31');
    definition := replace(definition, 'ingest phase runtime 30 is required', 'ingest phase runtime 31 is required');
    definition := replace(definition, 'fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 30)',
        'fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 31)');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Consecutive pieces share one endpoint, never a timestamp/component gap.
-- Stationary components become points before any line clipping can erase them.
-- +goose StatementBegin
CREATE FUNCTION app.project_tile_chunks(input geometry)
RETURNS TABLE(component integer, piece integer, geom geometry)
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, public AS $function$
    WITH components AS MATERIALIZED (
        SELECT row_number() OVER (ORDER BY d.path)::integer component, ST_Transform(d.geom, 3857) geom
        FROM ST_Dump(input) d
    )
    SELECT c.component, 0, ST_StartPoint(c.geom) FROM components c WHERE ST_Length(c.geom) = 0
    UNION ALL
    SELECT c.component, ((start.vertex - 1) / 255)::integer,
        ST_MakeLine(ARRAY(SELECT ST_PointN(c.geom, vertex)
            FROM generate_series(start.vertex, least(start.vertex + 255, ST_NPoints(c.geom))) vertex ORDER BY vertex))
    FROM components c
    CROSS JOIN LATERAL generate_series(1, ST_NPoints(c.geom) - 1, 255) start(vertex)
    WHERE ST_Length(c.geom) > 0
$function$;
-- +goose StatementEnd

CREATE TABLE app.raw_route_tile_chunks (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    component integer NOT NULL,
    piece integer NOT NULL,
    geom geometry(Geometry,3857) NOT NULL,
    PRIMARY KEY(account_id, workout_id, component, piece),
    FOREIGN KEY(account_id, workout_id) REFERENCES app.workout_routes(account_id, workout_id) ON UPDATE CASCADE ON DELETE CASCADE,
    CHECK (component > 0 AND piece >= 0),
    CHECK (GeometryType(geom) IN ('LINESTRING','POINT') AND NOT ST_IsEmpty(geom) AND ST_NPoints(geom) <= 256)
);
CREATE INDEX raw_route_tile_chunks_geom_idx ON app.raw_route_tile_chunks USING gist(geom);

CREATE TABLE app.coverage_tile_chunks (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    physical_segment_id uuid NOT NULL,
    component integer NOT NULL,
    piece integer NOT NULL,
    geom geometry(Geometry,3857) NOT NULL,
    PRIMARY KEY(account_id, workout_id, physical_segment_id, component, piece),
    FOREIGN KEY(account_id, workout_id, physical_segment_id)
        REFERENCES app.workout_segment_matches(account_id, workout_id, physical_segment_id) ON UPDATE CASCADE ON DELETE CASCADE,
    CHECK (component > 0 AND piece >= 0),
    CHECK (GeometryType(geom) IN ('LINESTRING','POINT') AND NOT ST_IsEmpty(geom) AND ST_NPoints(geom) <= 256)
);
CREATE INDEX coverage_tile_chunks_geom_idx ON app.coverage_tile_chunks USING gist(geom);

-- Cover the bounded probes: otherwise a fresh selection/account estimated as
-- one row can favor a sort/date index and repeatedly scan its whole scope.
CREATE INDEX map_selection_workouts_tile_lookup_idx ON app.map_selection_workouts(account_id, selection_id, workout_id) INCLUDE(sort_order);
CREATE INDEX workouts_tile_lookup_idx ON app.workouts(account_id, id) INCLUDE(workout_type_id, local_start_date, deletion_requested_at);

ALTER TABLE app.raw_route_tile_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.raw_route_tile_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY raw_route_tile_chunks_owner_policy ON app.raw_route_tile_chunks
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_tile_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_tile_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_tile_chunks_owner_policy ON app.coverage_tile_chunks
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);

-- +goose StatementBegin
CREATE FUNCTION app.maintain_raw_route_tile_chunks() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off AS $function$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.account_id = OLD.account_id AND NEW.workout_id = OLD.workout_id
        AND ST_AsEWKB(NEW.route) IS NOT DISTINCT FROM ST_AsEWKB(OLD.route) THEN RETURN NEW; END IF;
    IF TG_OP <> 'INSERT' THEN
        DELETE FROM app.raw_route_tile_chunks WHERE account_id = OLD.account_id AND workout_id = OLD.workout_id;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    -- A key move has already cascaded its chunk keys before this AFTER trigger.
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM app.raw_route_tile_chunks WHERE account_id = NEW.account_id AND workout_id = NEW.workout_id;
    END IF;
    INSERT INTO app.raw_route_tile_chunks(account_id, workout_id, component, piece, geom)
    SELECT NEW.account_id, NEW.workout_id, chunk.component, chunk.piece, chunk.geom
    FROM app.project_tile_chunks(NEW.route) chunk;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.maintain_coverage_tile_chunks() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off AS $function$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.account_id = OLD.account_id AND NEW.workout_id = OLD.workout_id
        AND NEW.physical_segment_id = OLD.physical_segment_id
        AND ST_AsEWKB(NEW.geom) IS NOT DISTINCT FROM ST_AsEWKB(OLD.geom) THEN RETURN NEW; END IF;
    IF TG_OP <> 'INSERT' THEN
        DELETE FROM app.coverage_tile_chunks WHERE account_id = OLD.account_id AND workout_id = OLD.workout_id
            AND physical_segment_id = OLD.physical_segment_id;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM app.coverage_tile_chunks WHERE account_id = NEW.account_id AND workout_id = NEW.workout_id
            AND physical_segment_id = NEW.physical_segment_id;
    END IF;
    INSERT INTO app.coverage_tile_chunks(account_id, workout_id, physical_segment_id, component, piece, geom)
    SELECT NEW.account_id, NEW.workout_id, NEW.physical_segment_id, chunk.component, chunk.piece, chunk.geom
    FROM app.project_tile_chunks(NEW.geom) chunk;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- Topology-preserving line simplification retains endpoints and short lines.
-- At 512 CSS pixels/tile the tolerance is 0.25px (plus <0.089px MVT rounding).
-- Simplify stable pieces BEFORE clipping so neighboring tiles share the LOD.
-- +goose StatementBegin
CREATE FUNCTION app.tile_display_geometry(input geometry, z integer) RETURNS geometry
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, public AS $function$
    SELECT CASE WHEN z >= 15 OR GeometryType(input) = 'POINT' THEN input
        ELSE ST_SimplifyPreserveTopology(input, 40075016.68557849 / power(2.0, z) / 2048.0) END
$function$;
-- +goose StatementEnd

-- API cache authorization returns only an expiry, never geometry or a capability
-- for another session. Martin deliberately does not use this GUC-bound helper.
-- +goose StatementBegin
CREATE FUNCTION app.authorize_map_tile(target_account_id uuid, target_session_id uuid,
    target_selection_id uuid, target_generation bigint, coverage boolean) RETURNS timestamptz
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT LEAST(selection.expires_at, session_row.expires_at)
    FROM app.map_selections selection
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
    JOIN app.accounts account ON account.id = account_owner.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    WHERE target_account_id = app.current_account_id() AND target_session_id = app.current_session_id()
        AND target_generation >= 1 AND coverage IS NOT NULL
        AND selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND (NOT coverage OR EXISTS (
            SELECT 1 FROM app.coverage_display_contexts context
            JOIN app.coverage_display_revisions revision ON revision.account_id = context.account_id
            WHERE context.account_id = selection.account_id AND context.id = selection.coverage_display_context_id
                AND context.generation = target_generation AND context.revision = revision.revision
                AND context.start_date = selection.start_date AND context.end_date = selection.end_date
                AND context.ready AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en'))
$function$;
-- +goose StatementEnd



-- Aggregate tiles intentionally contain only coverage and coverage_parks.
-- Independent focus remains the exact map_selection_coverage_focus GeoJSON ABI;
-- no focused-workout union or dependency belongs in an aggregate tile/cache.


GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.raw_route_tile_chunks OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_tile_chunks OWNER TO workouts_security_owner;
ALTER FUNCTION app.project_tile_chunks(geometry) OWNER TO workouts_security_owner;
ALTER FUNCTION app.tile_display_geometry(geometry,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.maintain_raw_route_tile_chunks() OWNER TO workouts_security_owner;
ALTER FUNCTION app.maintain_coverage_tile_chunks() OWNER TO workouts_security_owner;
ALTER FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.raw_route_mvt(integer,integer,integer,json) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_mvt(integer,integer,integer,json) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON app.raw_route_tile_chunks, app.coverage_tile_chunks
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
REVOKE ALL ON FUNCTION app.project_tile_chunks(geometry), app.tile_display_geometry(geometry,integer),
    app.maintain_raw_route_tile_chunks(), app.maintain_coverage_tile_chunks(),
    app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) TO workouts_api;

CREATE TRIGGER workout_routes_tile_chunks_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_routes
    FOR EACH ROW EXECUTE FUNCTION app.maintain_raw_route_tile_chunks();
CREATE TRIGGER workout_matches_tile_chunks_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_segment_matches
    FOR EACH ROW EXECUTE FUNCTION app.maintain_coverage_tile_chunks();

-- The migrator inherits security-owner; use its forced-RLS policy for backfill,
-- without weakening source-table policies or granting runtime geometry access.
SET LOCAL ROLE workouts_security_owner;
SET LOCAL jit = off;
INSERT INTO app.raw_route_tile_chunks(account_id, workout_id, component, piece, geom)
SELECT route.account_id, route.workout_id, chunk.component, chunk.piece, chunk.geom
FROM app.workout_routes route CROSS JOIN LATERAL app.project_tile_chunks(route.route) chunk;
INSERT INTO app.coverage_tile_chunks(account_id, workout_id, physical_segment_id, component, piece, geom)
SELECT match.account_id, match.workout_id, match.physical_segment_id, chunk.component, chunk.piece, chunk.geom
FROM app.workout_segment_matches match CROSS JOIN LATERAL app.project_tile_chunks(match.geom) chunk;
RESET ROLE;


-- Whole-component detail levels and tile-local display-precision dissolves.
-- their exact functions, storage and ingest fence. No evidence is rewritten.


-- Simplify each complete component before introducing artificial chunk ends.
-- Three bands bound the error to 0.25 CSS pixels at 512px/tile. Choose the
-- finer band between checkpoints; z>=15 continues to use exact 031 chunks.
-- +goose StatementBegin
CREATE FUNCTION app.project_route_lod(input geometry)
RETURNS TABLE(zoom_band integer, component integer, piece integer, geom geometry)
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, public AS $function$
    WITH components AS MATERIALIZED (
        SELECT row_number() OVER (ORDER BY d.path)::integer component, ST_Transform(d.geom, 3857) geom
        FROM ST_Dump(input) d
    ), simplified AS MATERIALIZED (
        SELECT band.zoom_band, c.component, c.geom original_geom, ST_Length(c.geom) original_length,
            CASE WHEN ST_Length(c.geom) = 0 THEN ST_StartPoint(c.geom)
                ELSE ST_Simplify(c.geom, 40075016.68557849 / power(2.0, band.zoom_band) / 2048.0, true) END geom
        FROM components c CROSS JOIN (VALUES (10),(12),(14)) band(zoom_band)
    ), levels AS (
        -- A closed moving loop can simplify to coincident endpoints. Preserve
        -- that component, never relabel movement as a stationary point.
        SELECT zoom_band, component,
            CASE WHEN original_length > 0 AND ST_Length(geom) = 0 THEN original_geom ELSE geom END geom
        FROM simplified
    )
    SELECT c.zoom_band, c.component, 0, c.geom FROM levels c WHERE GeometryType(c.geom) = 'POINT'
    UNION ALL
    SELECT c.zoom_band, c.component, ((start.vertex - 1) / 255)::integer,
        ST_MakeLine(ARRAY(SELECT ST_PointN(c.geom, vertex)
            FROM generate_series(start.vertex, least(start.vertex + 255, ST_NPoints(c.geom))) vertex ORDER BY vertex))
    FROM levels c
    CROSS JOIN LATERAL generate_series(1, ST_NPoints(c.geom) - 1, 255) start(vertex)
    WHERE GeometryType(c.geom) = 'LINESTRING'
$function$;
-- +goose StatementEnd

CREATE TABLE app.raw_route_lod_chunks (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    zoom_band integer NOT NULL,
    component integer NOT NULL,
    piece integer NOT NULL,
    geom geometry(Geometry,3857) NOT NULL,
    PRIMARY KEY(account_id, workout_id, zoom_band, component, piece),
    FOREIGN KEY(account_id, workout_id) REFERENCES app.workout_routes(account_id, workout_id) ON UPDATE CASCADE ON DELETE CASCADE,
    CHECK (zoom_band IN (10,12,14) AND component > 0 AND piece >= 0),
    CHECK (GeometryType(geom) IN ('LINESTRING','POINT') AND NOT ST_IsEmpty(geom) AND ST_NPoints(geom) <= 256)
);
CREATE INDEX raw_route_lod_chunks_geom_idx ON app.raw_route_lod_chunks USING gist(geom);
ALTER TABLE app.raw_route_lod_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.raw_route_lod_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY raw_route_lod_chunks_owner_policy ON app.raw_route_lod_chunks
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);

-- +goose StatementBegin
CREATE FUNCTION app.maintain_raw_route_lod_chunks() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off AS $function$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.account_id = OLD.account_id AND NEW.workout_id = OLD.workout_id
        AND ST_AsEWKB(NEW.route) IS NOT DISTINCT FROM ST_AsEWKB(OLD.route) THEN RETURN NEW; END IF;
    IF TG_OP <> 'INSERT' THEN
        DELETE FROM app.raw_route_lod_chunks WHERE account_id = OLD.account_id AND workout_id = OLD.workout_id;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM app.raw_route_lod_chunks WHERE account_id = NEW.account_id AND workout_id = NEW.workout_id;
    END IF;
    INSERT INTO app.raw_route_lod_chunks(account_id, workout_id, zoom_band, component, piece, geom)
    SELECT NEW.account_id, NEW.workout_id, chunk.zoom_band, chunk.component, chunk.piece, chunk.geom
    FROM app.project_route_lod(NEW.route) chunk;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- Private to the two security-definer entrypoints. Martin uses explicit scope,
-- never ambient account/session GUCs; the API cache helper remains unchanged.
-- +goose StatementBegin
CREATE FUNCTION app.tile_scope(query_params json, coverage boolean)
RETURNS TABLE(account_id uuid, selection_id uuid, context_id uuid, start_date date, end_date date)
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_account_id uuid;
    target_session_id uuid;
    target_selection_id uuid;
    target_generation bigint;
BEGIN
    IF coverage IS NULL OR json_typeof(query_params) IS DISTINCT FROM 'object' OR
        (SELECT count(*) FROM json_object_keys(query_params)) <> 4 OR
        NOT query_params::jsonb ?& ARRAY['target_account_id', 'target_session_id', 'target_selection_id', 'target_generation'] THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    BEGIN
        target_account_id := (query_params->>'target_account_id')::uuid;
        target_session_id := (query_params->>'target_session_id')::uuid;
        target_selection_id := (query_params->>'target_selection_id')::uuid;
        target_generation := (query_params->>'target_generation')::bigint;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END;
    IF target_account_id IS NULL OR target_session_id IS NULL OR target_selection_id IS NULL
        OR target_generation IS NULL OR target_generation < 1 THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT selection.account_id, selection.id, selection.coverage_display_context_id, selection.start_date, selection.end_date
    FROM app.map_selections selection
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
    JOIN app.accounts account ON account.id = account_owner.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND (NOT coverage OR EXISTS (
            SELECT 1 FROM app.coverage_display_contexts context
            JOIN app.coverage_display_revisions revision ON revision.account_id = context.account_id
            WHERE context.account_id = selection.account_id AND context.id = selection.coverage_display_context_id
                AND context.generation = target_generation AND context.revision = revision.revision
                AND context.start_date = selection.start_date AND context.end_date = selection.end_date
                AND context.ready AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en'));
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501'; END IF;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.raw_route_mvt(z integer, x integer, y integer, query_params json) RETURNS bytea
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off AS $function$
DECLARE
    scope record;
    bounds geometry;
    clip_bounds geometry;
    band integer;
    tile bytea;
BEGIN
    IF z IS NULL OR x IS NULL OR y IS NULL OR z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0
        OR x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO scope FROM app.tile_scope(query_params, false);
    bounds := ST_TileEnvelope(z, x, y);
    clip_bounds := ST_Expand(bounds, (ST_XMax(bounds) - ST_XMin(bounds)) * 64.0 / 4096.0);
    band := CASE WHEN z <= 10 THEN 10 WHEN z <= 12 THEN 12 ELSE 14 END;
    WITH local_chunks AS MATERIALIZED (
        SELECT chunk.workout_id, chunk.component, chunk.piece, chunk.geom
        FROM app.raw_route_lod_chunks chunk
        WHERE z < 15 AND chunk.account_id = scope.account_id AND chunk.zoom_band = band AND chunk.geom && clip_bounds
        UNION ALL
        SELECT chunk.workout_id, chunk.component, chunk.piece, chunk.geom
        FROM app.raw_route_tile_chunks chunk
        WHERE z >= 15 AND chunk.account_id = scope.account_id AND chunk.geom && clip_bounds
    ), selected AS MATERIALIZED (
        SELECT selected.workout_id, selected.sort_order, workout_type.type_key, workout_type.provider_label
        FROM app.map_selection_workouts selected
        JOIN app.workouts workout ON workout.account_id = selected.account_id AND workout.id = selected.workout_id
        JOIN app.workout_types workout_type ON workout_type.account_id = workout.account_id AND workout_type.id = workout.workout_type_id
        WHERE selected.account_id = scope.account_id AND selected.selection_id = scope.selection_id
            AND workout.deletion_requested_at IS NULL
    ), local AS MATERIALIZED (
        SELECT chunk.workout_id, selected.sort_order, selected.type_key, selected.provider_label,
            chunk.component, chunk.piece, GeometryType(chunk.geom) = 'POINT' stationary, chunk.geom
        FROM local_chunks chunk JOIN selected USING(workout_id)
    ), shapes AS (
        SELECT workout_id, sort_order, type_key, provider_label,
            ST_CollectionExtract(ST_Collect(geom ORDER BY component, piece), 2) geom
        FROM local WHERE NOT stationary AND geom IS NOT NULL AND NOT ST_IsEmpty(ST_CollectionExtract(geom, 2))
        GROUP BY workout_id, sort_order, type_key, provider_label
        UNION ALL
        SELECT workout_id, sort_order, type_key, provider_label, geom FROM local WHERE stationary AND geom IS NOT NULL
    )
    SELECT ST_AsMVT(rows, 'routes', 4096, 'geometry') INTO tile FROM (
        SELECT upper(replace(workout_id::text, '-', '')) "workoutId", type_key "workoutTypeKey",
            provider_label "workoutType", sort_order "sortOrder",
            ST_AsMVTGeom(geom, bounds::box2d, 4096, 64, true) geometry FROM shapes
    ) rows WHERE geometry IS NOT NULL;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.coverage_mvt(z integer, x integer, y integer, query_params json DEFAULT '{}'::json)
RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off
-- Per-operation bound, not a connection/global setting. Dense evidence hashes
-- otherwise spill and select hundreds of thousands of indexed metadata probes.
SET work_mem = '32MB' AS $function$
DECLARE
    scope record;
    bounds geometry;
    clip_bounds geometry;
    tile bytea;
BEGIN
    IF z IS NULL OR x IS NULL OR y IS NULL OR z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0
        OR x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO scope FROM app.tile_scope(query_params, true);
    bounds := ST_TileEnvelope(z, x, y);
    clip_bounds := ST_Expand(bounds, (ST_XMax(bounds) - ST_XMin(bounds)) * 64.0 / 4096.0);
    WITH local_chunks AS MATERIALIZED (
        SELECT chunk.workout_id, chunk.physical_segment_id, chunk.component, chunk.piece, chunk.geom
        FROM app.coverage_tile_chunks chunk
        WHERE chunk.account_id = scope.account_id AND chunk.geom && clip_bounds
    ), eligible AS MATERIALIZED (
        -- Fresh selection IDs commonly estimate as one row. Probe eligibility
        -- once per local workout, never use that estimate to rescan all chunks.
        SELECT checked.workout_id
        FROM (SELECT DISTINCT workout_id FROM local_chunks) local_workout
        CROSS JOIN LATERAL (
            SELECT workout.id workout_id
            FROM app.map_selection_workouts selected
            JOIN app.workouts workout ON workout.account_id = selected.account_id AND workout.id = selected.workout_id
            JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
            JOIN app.workout_coverage_states state ON state.account_id = workout.account_id AND state.workout_id = workout.id
            WHERE selected.account_id = scope.account_id AND selected.selection_id = scope.selection_id
                AND selected.workout_id = local_workout.workout_id
                AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
                AND state.applied_route_input_revision IS NOT NULL
                AND workout.local_start_date BETWEEN scope.start_date AND scope.end_date
            OFFSET 0
        ) checked
    ), render_inputs AS MATERIALIZED (
        -- A short first piece is a complete component. Only long components
        -- need reassembly: MVT's tiny-geometry check must not erase a visible
        -- line because all its artificial storage pieces are subpixel.
        SELECT chunk.workout_id, chunk.physical_segment_id, chunk.geom
        FROM local_chunks chunk JOIN eligible USING(workout_id)
        WHERE chunk.piece = 0 AND ST_NPoints(chunk.geom) < 256
        UNION ALL
        SELECT chunk.workout_id, chunk.physical_segment_id,
            CASE WHEN count(*) = 1 THEN (array_agg(chunk.geom))[1]
                ELSE ST_CollectionExtract(ST_Collect(chunk.geom ORDER BY chunk.piece), 2) END geom
        FROM local_chunks chunk JOIN eligible USING(workout_id)
        WHERE chunk.piece > 0 OR ST_NPoints(chunk.geom) = 256
        GROUP BY chunk.workout_id, chunk.physical_segment_id, chunk.component
    ), evidence AS MATERIALIZED (
        SELECT DISTINCT match.region_id, match.generation_id, match.physical_segment_id, match.logical_path_id,
            ST_AsEWKB(ST_AsMVTGeom(input.geom, bounds::box2d, 4096, 64, true)) ewkb
        FROM render_inputs input
        JOIN app.workout_segment_matches match ON match.account_id = scope.account_id
            AND match.workout_id = input.workout_id AND match.physical_segment_id = input.physical_segment_id
    ), checked AS MATERIALIZED (
        SELECT evidence.logical_path_id, segment.highway, segment.tags, evidence.ewkb
        FROM evidence JOIN app.path_segments segment ON segment.account_id = scope.account_id
            AND segment.region_id = evidence.region_id AND segment.generation_id = evidence.generation_id
            AND segment.physical_segment_id = evidence.physical_segment_id
        WHERE evidence.ewkb IS NOT NULL
    ), pieces AS (
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.ewkb
        FROM checked
        JOIN app.coverage_paths path ON path.account_id = scope.account_id AND path.logical_path_id = checked.logical_path_id
        JOIN app.coverage_display_members member ON member.account_id = scope.account_id AND member.context_id = scope.context_id
            AND member.source_kind = 'path'
            AND member.source_id = COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
        WHERE NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        UNION ALL
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.ewkb FROM checked
        JOIN app.coverage_display_members member ON member.account_id = scope.account_id AND member.context_id = scope.context_id
            AND member.source_kind = 'park' AND member.source_id = (checked.tags->>'workouts:park_id')::uuid
        WHERE checked.tags ? 'workouts:park_id'
    ), unique_pieces AS MATERIALIZED (
        SELECT DISTINCT canonical_id, source_kind, source_id, ewkb FROM pieces
    ), shapes AS (
        SELECT canonical_id, source_kind, source_id,
            CASE WHEN count(*) = 1 THEN ST_GeomFromEWKB((array_agg(ewkb))[1])
                ELSE ST_SnapToGrid(ST_UnaryUnion(ST_Collect(ST_GeomFromEWKB(ewkb))), 1) END geom
        FROM unique_pieces GROUP BY canonical_id, source_kind, source_id
    ), features AS MATERIALIZED (
        SELECT shapes.source_kind,
            upper(replace(entity.canonical_id::text, '-', '')) "entityId", entity.entity_kind "entityKind",
            upper(replace(shapes.source_id::text, '-', '')) "sourceEntityId", shapes.source_kind "sourceEntityKind",
            entity.broad_class "broadClass", entity.name, entity.locality_name "localityName", entity.region_id "regionId",
            entity.range_workout_count "rangeWorkoutCount", app.coverage_count_bucket(entity.range_workout_count) "countBucket",
            ST_CollectionExtract(shapes.geom, 2) geometry
        FROM shapes JOIN app.coverage_display_entities entity ON entity.account_id = scope.account_id
            AND entity.context_id = scope.context_id AND entity.canonical_id = shapes.canonical_id
        WHERE entity.range_workout_count > 0
    )
    SELECT COALESCE((SELECT ST_AsMVT(rows, 'coverage', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry FROM features WHERE source_kind = 'path' AND NOT ST_IsEmpty(geometry)) rows), ''::bytea) ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_parks', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry FROM features WHERE source_kind = 'park' AND NOT ST_IsEmpty(geometry)) rows), ''::bytea) INTO tile;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.raw_route_lod_chunks OWNER TO workouts_security_owner;
ALTER FUNCTION app.project_route_lod(geometry) OWNER TO workouts_security_owner;
ALTER FUNCTION app.maintain_raw_route_lod_chunks() OWNER TO workouts_security_owner;
ALTER FUNCTION app.tile_scope(json,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.raw_route_mvt(integer,integer,integer,json) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_mvt(integer,integer,integer,json) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON app.raw_route_lod_chunks FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
REVOKE ALL ON FUNCTION app.project_route_lod(geometry), app.maintain_raw_route_lod_chunks(), app.tile_scope(json,boolean),
    app.raw_route_mvt(integer,integer,integer,json), app.coverage_mvt(integer,integer,integer,json)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.raw_route_mvt(integer,integer,integer,json), app.coverage_mvt(integer,integer,integer,json)
    TO workouts_tiles;

CREATE TRIGGER workout_routes_lod_chunks_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_routes
    FOR EACH ROW EXECUTE FUNCTION app.maintain_raw_route_lod_chunks();

SET LOCAL ROLE workouts_security_owner;
SET LOCAL jit = off;
INSERT INTO app.raw_route_lod_chunks(account_id, workout_id, zoom_band, component, piece, geom)
SELECT route.account_id, route.workout_id, chunk.zoom_band, chunk.component, chunk.piece, chunk.geom
FROM app.workout_routes route CROSS JOIN LATERAL app.project_route_lod(route.route) chunk;
ANALYZE app.raw_route_lod_chunks;
RESET ROLE;

UPDATE app.schema_metadata SET schema_version = 31, minimum_runtime_version = 31 WHERE singleton;

-- +goose Down

-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'tile-local geometry is forward-only; restore the pre-migration backup to roll back'
        USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
