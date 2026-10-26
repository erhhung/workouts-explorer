-- +goose Up
-- Keep evidence and its projection in one migration snapshot. Eligibility,
-- workout dates, source-path metadata and park attributions remain live joins.
LOCK TABLE app.workouts, app.workout_segment_matches, app.path_segments IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE app.workout_path_display_partials (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    entity_id uuid NOT NULL,
    logical_path_id uuid NOT NULL,
    qualifies_without_name boolean NOT NULL,
    region_id text NOT NULL,
    education_key_present boolean NOT NULL,
    education_name text,
    minimum_longitude double precision NOT NULL,
    minimum_latitude double precision NOT NULL,
    maximum_longitude double precision NOT NULL,
    maximum_latitude double precision NOT NULL,
    PRIMARY KEY (account_id, workout_id, entity_id, logical_path_id, qualifies_without_name),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE
);

CREATE TABLE app.workout_path_display_context (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    entity_id uuid NOT NULL,
    locality_name text NOT NULL,
    PRIMARY KEY (account_id, workout_id, entity_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE
);

CREATE TABLE app.workout_park_display_bounds (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    park_id uuid NOT NULL,
    region_id text,
    minimum_longitude double precision NOT NULL,
    minimum_latitude double precision NOT NULL,
    maximum_longitude double precision NOT NULL,
    maximum_latitude double precision NOT NULL,
    PRIMARY KEY (account_id, workout_id, park_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE
);

-- No source FKs: tombstones must survive source pruning and account cascades
-- until the deferred callback can discard them or rebuild remaining evidence.
CREATE TABLE app.coverage_display_dirty (
    transaction_id bigint NOT NULL,
    account_id uuid NOT NULL,
    scope text NOT NULL CHECK (scope IN ('account', 'workout', 'segment')),
    workout_id uuid,
    region_id text,
    generation_id bigint,
    physical_segment_id uuid,
    CHECK (
        (scope = 'account' AND workout_id IS NULL AND region_id IS NULL
            AND generation_id IS NULL AND physical_segment_id IS NULL)
        OR (scope = 'workout' AND workout_id IS NOT NULL AND region_id IS NULL
            AND generation_id IS NULL AND physical_segment_id IS NULL)
        OR (scope = 'segment' AND workout_id IS NULL AND region_id IS NOT NULL
            AND generation_id IS NOT NULL AND physical_segment_id IS NOT NULL)
    ),
    UNIQUE NULLS NOT DISTINCT
        (transaction_id, account_id, scope, workout_id, region_id, generation_id, physical_segment_id)
);

ALTER TABLE app.workout_path_display_partials ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_path_display_partials FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_path_display_partials_owner_policy ON app.workout_path_display_partials
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.workout_path_display_context ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_path_display_context FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_path_display_context_owner_policy ON app.workout_path_display_context
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.workout_park_display_bounds ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_park_display_bounds FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_park_display_bounds_owner_policy ON app.workout_park_display_bounds
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_dirty ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_dirty FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_dirty_owner_policy ON app.coverage_display_dirty
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);

-- NULL account is used only for the set-based migration backfill. Runtime
-- callers first serialize the account and expand segment keys into workouts.
-- +goose StatementBegin
CREATE FUNCTION app.rebuild_coverage_display_precomputations(target_account_id uuid) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app, public AS $function$
BEGIN
    DELETE FROM app.workout_path_display_partials partial USING app.coverage_display_dirty dirty
    WHERE dirty.transaction_id = txid_current() AND dirty.scope = 'workout'
        AND (target_account_id IS NULL OR dirty.account_id = target_account_id)
        AND partial.account_id = dirty.account_id AND partial.workout_id = dirty.workout_id;
    DELETE FROM app.workout_path_display_context context USING app.coverage_display_dirty dirty
    WHERE dirty.transaction_id = txid_current() AND dirty.scope = 'workout'
        AND (target_account_id IS NULL OR dirty.account_id = target_account_id)
        AND context.account_id = dirty.account_id AND context.workout_id = dirty.workout_id;
    DELETE FROM app.workout_park_display_bounds bounds USING app.coverage_display_dirty dirty
    WHERE dirty.transaction_id = txid_current() AND dirty.scope = 'workout'
        AND (target_account_id IS NULL OR dirty.account_id = target_account_id)
        AND bounds.account_id = dirty.account_id AND bounds.workout_id = dirty.workout_id;

    WITH evidence AS MATERIALIZED (
        SELECT match.account_id, match.workout_id, match.logical_path_id, match.region_id,
            match.geom, segment.highway, segment.tags
        FROM app.coverage_display_dirty dirty
        JOIN app.workout_segment_matches match
            ON match.account_id = dirty.account_id AND match.workout_id = dirty.workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id
            AND segment.region_id = match.region_id AND segment.generation_id = match.generation_id
            AND segment.physical_segment_id = match.physical_segment_id
        WHERE dirty.transaction_id = txid_current() AND dirty.scope = 'workout'
            AND (target_account_id IS NULL OR dirty.account_id = target_account_id)
    ), path_partials AS (
        INSERT INTO app.workout_path_display_partials (
            account_id, workout_id, entity_id, logical_path_id, qualifies_without_name,
            region_id, education_key_present, education_name,
            minimum_longitude, minimum_latitude, maximum_longitude, maximum_latitude
        )
        SELECT account_id, workout_id,
            COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id), logical_path_id,
            (tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (tags ? 'workouts:park_id')) IS TRUE,
            min(region_id), bool_or(tags ? 'workouts:education_id'), min(tags->>'workouts:education_name'),
            ST_XMin(ST_Extent(geom)::box2d), ST_YMin(ST_Extent(geom)::box2d),
            ST_XMax(ST_Extent(geom)::box2d), ST_YMax(ST_Extent(geom)::box2d)
        FROM evidence
        WHERE NOT (highway = 'service' AND COALESCE(tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND tags->>'amenity' IS DISTINCT FROM 'parking'
        GROUP BY account_id, workout_id,
            COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id), logical_path_id,
            (tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (tags ? 'workouts:park_id')) IS TRUE
    ), path_context AS (
        INSERT INTO app.workout_path_display_context (account_id, workout_id, entity_id, locality_name)
        SELECT account_id, workout_id,
            COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id),
            min(tags->>'workouts:park_name')
        FROM evidence
        WHERE tags->>'workouts:park_kind' IN ('state_park', 'national_park')
            AND NULLIF(tags->>'workouts:park_name', '') IS NOT NULL
        GROUP BY account_id, workout_id,
            COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id)
    )
    INSERT INTO app.workout_park_display_bounds (
        account_id, workout_id, park_id, region_id,
        minimum_longitude, minimum_latitude, maximum_longitude, maximum_latitude
    )
    SELECT account_id, workout_id, (tags->>'workouts:park_id')::uuid,
        min(region_id) FILTER (WHERE tags->>'workouts:park_id' = ((tags->>'workouts:park_id')::uuid)::text),
        ST_XMin(ST_Extent(geom)::box2d), ST_YMin(ST_Extent(geom)::box2d),
        ST_XMax(ST_Extent(geom)::box2d), ST_YMax(ST_Extent(geom)::box2d)
    FROM evidence
    WHERE tags ? 'workouts:park_id' AND tags->>'workouts:park_id' IS NOT NULL
    GROUP BY account_id, workout_id, (tags->>'workouts:park_id')::uuid;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.flush_coverage_display_precomputations(target_account_id uuid) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.coverage_display_dirty
        WHERE transaction_id = txid_current() AND account_id = target_account_id AND scope = 'account') THEN
        RETURN;
    END IF;

    -- An actual row update serializes flushes even at REPEATABLE READ. Keep the
    -- public generation unchanged and take this barrier only at flush time.
    UPDATE app.account_data_generations SET generation = generation WHERE account_id = target_account_id;
    IF NOT FOUND THEN
        IF EXISTS (SELECT 1 FROM app.accounts WHERE id = target_account_id) THEN
            RAISE EXCEPTION 'coverage display generation is unavailable' USING ERRCODE = '55000';
        END IF;
        DELETE FROM app.coverage_display_dirty
        WHERE transaction_id = txid_current() AND account_id = target_account_id;
        RETURN;
    END IF;

    -- This separate command gets a fresh READ COMMITTED snapshot AFTER waiting.
    -- Eager expansion could miss a concurrent, previously uncommitted match.
    INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope, workout_id)
    SELECT DISTINCT txid_current(), match.account_id, 'workout', match.workout_id
    FROM app.coverage_display_dirty dirty
    JOIN app.workout_segment_matches match ON match.account_id = dirty.account_id
        AND match.region_id = dirty.region_id AND match.generation_id = dirty.generation_id
        AND match.physical_segment_id = dirty.physical_segment_id
    WHERE dirty.transaction_id = txid_current() AND dirty.account_id = target_account_id AND dirty.scope = 'segment'
    ON CONFLICT DO NOTHING;

    PERFORM app.rebuild_coverage_display_precomputations(target_account_id);
    DELETE FROM app.coverage_display_dirty
    WHERE transaction_id = txid_current() AND account_id = target_account_id;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.mark_coverage_display_dirty() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF TG_TABLE_NAME = 'path_segments' AND TG_OP = 'UPDATE' THEN
        IF (OLD.account_id, OLD.region_id, OLD.generation_id, OLD.physical_segment_id,
                OLD.logical_path_id, OLD.highway, OLD.tags) IS NOT DISTINCT FROM
            (NEW.account_id, NEW.region_id, NEW.generation_id, NEW.physical_segment_id,
                NEW.logical_path_id, NEW.highway, NEW.tags) THEN
            RETURN NULL;
        END IF;
    END IF;

    IF TG_OP <> 'INSERT' THEN
        IF TG_TABLE_NAME = 'workout_segment_matches' THEN
            INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope, workout_id)
            VALUES (txid_current(), OLD.account_id, 'workout', OLD.workout_id) ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO app.coverage_display_dirty
                (transaction_id, account_id, scope, region_id, generation_id, physical_segment_id)
            VALUES (txid_current(), OLD.account_id, 'segment', OLD.region_id, OLD.generation_id, OLD.physical_segment_id)
            ON CONFLICT DO NOTHING;
        END IF;
        INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope)
        VALUES (txid_current(), OLD.account_id, 'account') ON CONFLICT DO NOTHING;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        IF TG_TABLE_NAME = 'workout_segment_matches' THEN
            INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope, workout_id)
            VALUES (txid_current(), NEW.account_id, 'workout', NEW.workout_id) ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO app.coverage_display_dirty
                (transaction_id, account_id, scope, region_id, generation_id, physical_segment_id)
            VALUES (txid_current(), NEW.account_id, 'segment', NEW.region_id, NEW.generation_id, NEW.physical_segment_id)
            ON CONFLICT DO NOTHING;
        END IF;
        INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope)
        VALUES (txid_current(), NEW.account_id, 'account') ON CONFLICT DO NOTHING;
    END IF;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.flush_coverage_display_dirty_account() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM app.flush_coverage_display_precomputations(NEW.account_id);
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.workout_path_display_partials OWNER TO workouts_security_owner;
ALTER TABLE app.workout_path_display_context OWNER TO workouts_security_owner;
ALTER TABLE app.workout_park_display_bounds OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_dirty OWNER TO workouts_security_owner;
ALTER FUNCTION app.rebuild_coverage_display_precomputations(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.flush_coverage_display_precomputations(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.mark_coverage_display_dirty() OWNER TO workouts_security_owner;
ALTER FUNCTION app.flush_coverage_display_dirty_account() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

REVOKE ALL ON app.workout_path_display_partials, app.workout_path_display_context,
    app.workout_park_display_bounds, app.coverage_display_dirty
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
REVOKE ALL ON FUNCTION app.rebuild_coverage_display_precomputations(uuid),
    app.flush_coverage_display_precomputations(uuid), app.mark_coverage_display_dirty(),
    app.flush_coverage_display_dirty_account()
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- Backfill all evidence in one batch, including currently ineligible workouts.
INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope, workout_id)
SELECT DISTINCT txid_current(), account_id, 'workout', workout_id FROM app.workout_segment_matches;
SELECT app.rebuild_coverage_display_precomputations(NULL);
DELETE FROM app.coverage_display_dirty WHERE transaction_id = txid_current();

CREATE TRIGGER workout_segment_matches_display_dirty_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_segment_matches
FOR EACH ROW EXECUTE FUNCTION app.mark_coverage_display_dirty();
CREATE TRIGGER path_segments_display_dirty_after_change
AFTER UPDATE OR DELETE ON app.path_segments
FOR EACH ROW EXECUTE FUNCTION app.mark_coverage_display_dirty();
CREATE CONSTRAINT TRIGGER coverage_display_dirty_account_flush
AFTER INSERT ON app.coverage_display_dirty DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW WHEN (NEW.scope = 'account')
EXECUTE FUNCTION app.flush_coverage_display_dirty_account();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_entities (
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_search text DEFAULT NULL
) RETURNS TABLE (
    entity_id uuid,
    entity_kind text,
    broad_class text,
    name text,
    locality_name text,
    region_id text,
    region_name text,
    range_workout_count bigint,
    range_first_date date,
    range_first_workout_id uuid,
    range_latest_date date,
    range_latest_workout_id uuid,
    all_time_workout_count bigint,
    all_time_first_date date,
    all_time_first_workout_id uuid,
    all_time_latest_date date,
    all_time_latest_workout_id uuid,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision
) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off AS $function$
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date, selection.end_date
        FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id
            AND account_owner.account_id = selection.account_id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
            AND selection.session_id = target_session_id AND selection.generation = target_generation
            AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
            AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
            AND principal.disabled_at IS NULL AND account.state = 'active'
            AND target_account_id = app.current_account_id()
    ), eligible AS MATERIALIZED (
        SELECT workout.id, workout.local_start_date, workout.started_at
        FROM app.workouts workout
        JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id = workout.id
            AND coverage.account_id = workout.account_id
        WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND workout.local_start_date IS NOT NULL AND route.route IS NOT NULL
            AND coverage.applied_route_input_revision IS NOT NULL
    ), path_visits AS MATERIALIZED (
        -- Deduplicate source paths and qualification buckets BEFORE counts.
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at, partial.entity_id,
            min(partial.region_id) region_id, bool_or(partial.education_key_present) education_key_present,
            min(partial.education_name) education_name, min(path.broad_class) broad_class,
            min(path.name) name, min(path.locality_name) locality_name,
            min(partial.minimum_longitude) minimum_longitude, min(partial.minimum_latitude) minimum_latitude,
            max(partial.maximum_longitude) maximum_longitude, max(partial.maximum_latitude) maximum_latitude
        FROM eligible JOIN app.workout_path_display_partials partial
            ON partial.account_id = target_account_id AND partial.workout_id = eligible.id
        JOIN app.coverage_paths path ON path.account_id = partial.account_id AND path.logical_path_id = partial.logical_path_id
        WHERE path.name IS NOT NULL OR partial.qualifies_without_name
        GROUP BY eligible.id, eligible.local_start_date, eligible.started_at, partial.entity_id
    ), park_visits AS MATERIALIZED (
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at,
            attribution.park_id entity_id, bounds.region_id
        FROM eligible JOIN app.workout_park_attributions attribution
            ON attribution.account_id = target_account_id AND attribution.workout_id = eligible.id
        LEFT JOIN app.workout_park_display_bounds bounds ON bounds.account_id = attribution.account_id
            AND bounds.workout_id = attribution.workout_id AND bounds.park_id = attribution.park_id
    ), path_range AS (
        SELECT visit.entity_id, min(visit.region_id) region_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id,
            min(visit.minimum_longitude) minimum_longitude, min(visit.minimum_latitude) minimum_latitude,
            max(visit.maximum_longitude) maximum_longitude, max(visit.maximum_latitude) maximum_latitude
        FROM path_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), path_all AS (
        SELECT visit.entity_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM path_visits visit GROUP BY visit.entity_id
    ), park_range AS (
        SELECT visit.entity_id, min(visit.region_id) region_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM park_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), park_all AS (
        SELECT visit.entity_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM park_visits visit GROUP BY visit.entity_id
    ), park_bounds AS (
        SELECT bounds.park_id entity_id,
            min(bounds.minimum_longitude) minimum_longitude, min(bounds.minimum_latitude) minimum_latitude,
            max(bounds.maximum_longitude) maximum_longitude, max(bounds.maximum_latitude) maximum_latitude
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_park_display_bounds bounds
            ON bounds.account_id = target_account_id AND bounds.workout_id = eligible.id
        GROUP BY bounds.park_id
    ), regional_park_path_context AS MATERIALIZED (
        SELECT context.entity_id, min(context.locality_name) locality_name
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_path_display_context context
            ON context.account_id = target_account_id AND context.workout_id = eligible.id
        GROUP BY context.entity_id
    ), path_identity AS MATERIALIZED (
        SELECT visit.entity_id,
            CASE WHEN bool_or(visit.education_key_present) THEN 'other' ELSE min(visit.broad_class) END broad_class,
            COALESCE(min(visit.education_name), min(visit.name)) name, min(visit.locality_name) locality_name
        FROM path_visits visit GROUP BY visit.entity_id
    ), entities AS (
        SELECT identity.entity_id, 'path'::text entity_kind, identity.broad_class, identity.name,
            COALESCE(regional_park.locality_name, identity.locality_name) locality_name,
            range.region_id, catalog.display_name region_name, range.workout_count range_workout_count,
            range.first_date range_first_date, range.first_workout_id range_first_workout_id,
            range.latest_date range_latest_date, range.latest_workout_id range_latest_workout_id,
            all_time.workout_count all_time_workout_count, all_time.first_date all_time_first_date,
            all_time.first_workout_id all_time_first_workout_id, all_time.latest_date all_time_latest_date,
            all_time.latest_workout_id all_time_latest_workout_id,
            range.minimum_longitude, range.minimum_latitude, range.maximum_longitude, range.maximum_latitude
        FROM path_range range JOIN path_all all_time USING (entity_id) JOIN path_identity identity USING (entity_id)
        LEFT JOIN regional_park_path_context regional_park ON regional_park.entity_id = identity.entity_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = range.region_id
        UNION ALL
        SELECT park.park_id, 'park'::text, 'park'::text, park.name, park.locality_name,
            range.region_id, catalog.display_name, range.workout_count, range.first_date, range.first_workout_id,
            range.latest_date, range.latest_workout_id, all_time.workout_count, all_time.first_date,
            all_time.first_workout_id, all_time.latest_date, all_time.latest_workout_id,
            bounds.minimum_longitude, bounds.minimum_latitude, bounds.maximum_longitude, bounds.maximum_latitude
        FROM park_range range JOIN park_all all_time USING (entity_id) JOIN park_bounds bounds USING (entity_id)
        JOIN app.coverage_parks park ON park.account_id = target_account_id AND park.park_id = range.entity_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = range.region_id
    )
    SELECT * FROM entities entity WHERE NULLIF(target_search, '') IS NULL OR
        strpos(lower(concat_ws(' ', entity.entity_id::text, entity.entity_kind, entity.broad_class,
            entity.name, entity.locality_name, entity.region_name, entity.region_id)), lower(target_search)) > 0
$function$;
-- +goose StatementEnd

-- CREATE OR REPLACE preserves the shipped reader ownership, signature and ACLs.

CREATE INDEX workout_path_display_partials_entity_idx
    ON app.workout_path_display_partials (account_id, entity_id, workout_id);
CREATE INDEX workout_path_display_context_entity_idx
    ON app.workout_path_display_context (account_id, entity_id, workout_id);
CREATE INDEX workout_park_display_bounds_park_idx
    ON app.workout_park_display_bounds (account_id, park_id, workout_id);

-- Entity detail metadata without assembling highlight geometry. Validate first,
-- then restrict every compact fact source before aggregation.
-- +goose StatementBegin
CREATE FUNCTION app.map_selection_coverage_entity_stats (
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_entity_kind text,
    target_entity_id uuid
) RETURNS TABLE (
    entity_id uuid,
    entity_kind text,
    broad_class text,
    name text,
    locality_name text,
    region_id text,
    region_name text,
    range_workout_count bigint,
    range_first_date date,
    range_first_workout_id uuid,
    range_latest_date date,
    range_latest_workout_id uuid,
    all_time_workout_count bigint,
    all_time_first_date date,
    all_time_first_workout_id uuid,
    all_time_latest_date date,
    all_time_latest_workout_id uuid,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision
) LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app
SET jit = off AS $function$
DECLARE
    selected_start_date date;
    selected_end_date date;
BEGIN
    SELECT selection.start_date, selection.end_date INTO selected_start_date, selected_end_date
    FROM app.map_selections selection
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users account_owner ON account_owner.principal_id = principal.id
        AND account_owner.account_id = selection.account_id
    JOIN app.accounts account ON account.id = account_owner.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND target_account_id = app.current_account_id();
    IF NOT FOUND THEN
        RETURN;
    END IF;

    RETURN QUERY
    WITH selected AS MATERIALIZED (
        SELECT selected_start_date start_date, selected_end_date end_date
    ), candidate_workout_ids AS MATERIALIZED (
        SELECT partial.workout_id
        FROM app.workout_path_display_partials partial
        WHERE partial.account_id = target_account_id
            AND target_entity_kind = 'path' AND partial.entity_id = target_entity_id
        UNION
        SELECT context.workout_id
        FROM app.workout_path_display_context context
        WHERE context.account_id = target_account_id
            AND target_entity_kind = 'path' AND context.entity_id = target_entity_id
        UNION
        SELECT attribution.workout_id
        FROM app.workout_park_attributions attribution
        WHERE attribution.account_id = target_account_id
            AND target_entity_kind = 'park' AND attribution.park_id = target_entity_id
        UNION
        SELECT bounds.workout_id
        FROM app.workout_park_display_bounds bounds
        WHERE bounds.account_id = target_account_id
            AND target_entity_kind = 'park' AND bounds.park_id = target_entity_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id, workout.local_start_date, workout.started_at
        FROM candidate_workout_ids candidate
        JOIN app.workouts workout ON workout.id = candidate.workout_id
        JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id = workout.id
            AND coverage.account_id = workout.account_id
        WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND workout.local_start_date IS NOT NULL AND route.route IS NOT NULL
            AND coverage.applied_route_input_revision IS NOT NULL
    ), path_visits AS MATERIALIZED (
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at, partial.entity_id,
            min(partial.region_id) region_id, bool_or(partial.education_key_present) education_key_present,
            min(partial.education_name) education_name, min(path.broad_class) broad_class,
            min(path.name) name, min(path.locality_name) locality_name,
            min(partial.minimum_longitude) minimum_longitude, min(partial.minimum_latitude) minimum_latitude,
            max(partial.maximum_longitude) maximum_longitude, max(partial.maximum_latitude) maximum_latitude
        FROM eligible JOIN app.workout_path_display_partials partial
            ON partial.account_id = target_account_id AND partial.workout_id = eligible.id
            AND target_entity_kind = 'path' AND partial.entity_id = target_entity_id
        JOIN app.coverage_paths path ON path.account_id = partial.account_id AND path.logical_path_id = partial.logical_path_id
        WHERE path.name IS NOT NULL OR partial.qualifies_without_name
        GROUP BY eligible.id, eligible.local_start_date, eligible.started_at, partial.entity_id
    ), park_visits AS MATERIALIZED (
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at,
            attribution.park_id entity_id, bounds.region_id
        FROM eligible JOIN app.workout_park_attributions attribution
            ON attribution.account_id = target_account_id AND attribution.workout_id = eligible.id
            AND target_entity_kind = 'park' AND attribution.park_id = target_entity_id
        LEFT JOIN app.workout_park_display_bounds bounds ON bounds.account_id = attribution.account_id
            AND bounds.workout_id = attribution.workout_id AND bounds.park_id = attribution.park_id
    ), path_range AS (
        SELECT visit.entity_id, min(visit.region_id) region_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id,
            min(visit.minimum_longitude) minimum_longitude, min(visit.minimum_latitude) minimum_latitude,
            max(visit.maximum_longitude) maximum_longitude, max(visit.maximum_latitude) maximum_latitude
        FROM path_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), path_all AS (
        SELECT visit.entity_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM path_visits visit GROUP BY visit.entity_id
    ), park_range AS (
        SELECT visit.entity_id, min(visit.region_id) region_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM park_visits visit CROSS JOIN selected
        WHERE visit.local_start_date BETWEEN selected.start_date AND selected.end_date GROUP BY visit.entity_id
    ), park_all AS (
        SELECT visit.entity_id, count(*)::bigint workout_count,
            min(visit.local_start_date) first_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date, visit.started_at, visit.workout_id))[1] first_workout_id,
            max(visit.local_start_date) latest_date,
            (array_agg(visit.workout_id ORDER BY visit.local_start_date DESC, visit.started_at, visit.workout_id))[1] latest_workout_id
        FROM park_visits visit GROUP BY visit.entity_id
    ), park_bounds AS (
        SELECT bounds.park_id entity_id,
            min(bounds.minimum_longitude) minimum_longitude, min(bounds.minimum_latitude) minimum_latitude,
            max(bounds.maximum_longitude) maximum_longitude, max(bounds.maximum_latitude) maximum_latitude
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_park_display_bounds bounds
            ON bounds.account_id = target_account_id AND bounds.workout_id = eligible.id
            AND target_entity_kind = 'park' AND bounds.park_id = target_entity_id
        GROUP BY bounds.park_id
        -- 020 detail needs canonical-text geometry; list bounds still include
        -- every UUID-cast tag, including noncanonical tags in mixed evidence.
        HAVING bool_or(bounds.region_id IS NOT NULL)
    ), regional_park_path_context AS MATERIALIZED (
        SELECT context.entity_id, min(context.locality_name) locality_name
        FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date
        JOIN app.workout_path_display_context context
            ON context.account_id = target_account_id AND context.workout_id = eligible.id
            AND target_entity_kind = 'path' AND context.entity_id = target_entity_id
        GROUP BY context.entity_id
    ), path_identity AS MATERIALIZED (
        SELECT visit.entity_id,
            CASE WHEN bool_or(visit.education_key_present) THEN 'other' ELSE min(visit.broad_class) END broad_class,
            COALESCE(min(visit.education_name), min(visit.name)) name, min(visit.locality_name) locality_name
        FROM path_visits visit
        WHERE target_entity_kind = 'path' AND visit.entity_id = target_entity_id
        GROUP BY visit.entity_id
    ), entities AS (
        SELECT identity.entity_id, 'path'::text entity_kind, identity.broad_class, identity.name,
            COALESCE(regional_park.locality_name, identity.locality_name) locality_name,
            range.region_id, catalog.display_name region_name, range.workout_count range_workout_count,
            range.first_date range_first_date, range.first_workout_id range_first_workout_id,
            range.latest_date range_latest_date, range.latest_workout_id range_latest_workout_id,
            all_time.workout_count all_time_workout_count, all_time.first_date all_time_first_date,
            all_time.first_workout_id all_time_first_workout_id, all_time.latest_date all_time_latest_date,
            all_time.latest_workout_id all_time_latest_workout_id,
            range.minimum_longitude, range.minimum_latitude, range.maximum_longitude, range.maximum_latitude
        FROM path_range range JOIN path_all all_time USING (entity_id) JOIN path_identity identity USING (entity_id)
        LEFT JOIN regional_park_path_context regional_park ON regional_park.entity_id = identity.entity_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = range.region_id
        UNION ALL
        SELECT park.park_id, 'park'::text, 'park'::text, park.name, park.locality_name,
            range.region_id, catalog.display_name, range.workout_count, range.first_date, range.first_workout_id,
            range.latest_date, range.latest_workout_id, all_time.workout_count, all_time.first_date,
            all_time.first_workout_id, all_time.latest_date, all_time.latest_workout_id,
            bounds.minimum_longitude, bounds.minimum_latitude, bounds.maximum_longitude, bounds.maximum_latitude
        FROM park_range range JOIN park_all all_time USING (entity_id) JOIN park_bounds bounds USING (entity_id)
        JOIN app.coverage_parks park ON park.account_id = target_account_id AND park.park_id = range.entity_id
        LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = range.region_id
    )
    SELECT * FROM entities;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid) TO workouts_api;

UPDATE app.schema_metadata SET schema_version = 32, minimum_runtime_version = 32 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'coverage display precomputations are forward-only; restore the pre-migration backup to roll back'
        USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
