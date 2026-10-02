-- +goose Up
CREATE TABLE app.coverage_parks (
    account_id uuid NOT NULL REFERENCES app.accounts (id) ON DELETE CASCADE,
    park_id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 4096),
    normalized_name text NOT NULL CHECK (length(normalized_name) BETWEEN 1 AND 4096),
    park_kind text NOT NULL CHECK (park_kind IN ('local_park', 'nature_reserve', 'protected_area', 'state_park', 'national_park')),
    locality_relation_id bigint,
    locality_relation_version integer,
    locality_name text,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, park_id),
    CHECK (
        (
            locality_relation_id IS NULL
            AND locality_relation_version IS NULL
            AND locality_name IS NULL
        )
        OR (
            locality_relation_id IS NOT NULL
            AND locality_relation_version > 0
            AND length(locality_name) BETWEEN 1 AND 4096
        )
    ),
    CHECK (
        park_kind IN ('state_park', 'national_park')
        OR locality_relation_id IS NOT NULL
    )
);

CREATE INDEX coverage_parks_locality_name_idx ON app.coverage_parks (account_id, locality_relation_id, normalized_name, park_id);

CREATE INDEX coverage_parks_kind_name_idx ON app.coverage_parks (account_id, park_kind, normalized_name, park_id);

CREATE TABLE app.workout_park_attributions (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    park_id uuid NOT NULL,
    first_traversed_at timestamptz NOT NULL,
    visit_date date NOT NULL,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, workout_id, park_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (account_id, park_id) REFERENCES app.coverage_parks (account_id, park_id) ON DELETE CASCADE
);

CREATE INDEX workout_park_attributions_park_idx ON app.workout_park_attributions (account_id, park_id, workout_id);

CREATE TABLE app.account_park_daily_rollups (
    account_id uuid NOT NULL,
    park_id uuid NOT NULL,
    visit_date date NOT NULL,
    workout_count integer NOT NULL CHECK (workout_count > 0),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, park_id, visit_date),
    FOREIGN KEY (account_id, park_id) REFERENCES app.coverage_parks (account_id, park_id) ON DELETE CASCADE
);

CREATE TABLE app.account_park_all_time (
    account_id uuid NOT NULL,
    park_id uuid NOT NULL,
    workout_count integer NOT NULL CHECK (workout_count > 0),
    first_visit_date date NOT NULL,
    latest_visit_date date NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, park_id),
    FOREIGN KEY (account_id, park_id) REFERENCES app.coverage_parks (account_id, park_id) ON DELETE CASCADE,
    CHECK (first_visit_date <= latest_visit_date)
);

ALTER TABLE app.coverage_parks ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_parks FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_park_attributions ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.workout_park_attributions FORCE ROW LEVEL SECURITY;

ALTER TABLE app.account_park_daily_rollups ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.account_park_daily_rollups FORCE ROW LEVEL SECURITY;

ALTER TABLE app.account_park_all_time ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.account_park_all_time FORCE ROW LEVEL SECURITY;

CREATE POLICY coverage_parks_account_policy ON app.coverage_parks USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY coverage_parks_owner_policy ON app.coverage_parks TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY workout_park_attributions_account_policy ON app.workout_park_attributions USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY workout_park_attributions_owner_policy ON app.workout_park_attributions TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY account_park_daily_rollups_account_policy ON app.account_park_daily_rollups USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY account_park_daily_rollups_owner_policy ON app.account_park_daily_rollups TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY account_park_all_time_account_policy ON app.account_park_all_time USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY account_park_all_time_owner_policy ON app.account_park_all_time TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE TABLE app.park_attribution_function_backup (definition text NOT NULL);

INSERT INTO
    app.park_attribution_function_backup (definition)
SELECT
    pg_get_functiondef('app.map_selection_path_counts(uuid,uuid,uuid,bigint)'::regprocedure);

REVOKE ALL ON app.park_attribution_function_backup
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.increment_park_rollups () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    INSERT INTO app.account_park_daily_rollups(account_id, park_id, visit_date, workout_count)
    VALUES(NEW.account_id, NEW.park_id, NEW.visit_date, 1)
    ON CONFLICT (account_id, park_id, visit_date) DO UPDATE
       SET workout_count = app.account_park_daily_rollups.workout_count + 1, updated_at = transaction_timestamp();
    INSERT INTO app.account_park_all_time(account_id, park_id, workout_count, first_visit_date, latest_visit_date)
    VALUES(NEW.account_id, NEW.park_id, 1, NEW.visit_date, NEW.visit_date)
    ON CONFLICT (account_id, park_id) DO UPDATE SET workout_count = app.account_park_all_time.workout_count + 1,
        first_visit_date = LEAST(app.account_park_all_time.first_visit_date, EXCLUDED.first_visit_date),
        latest_visit_date = GREATEST(app.account_park_all_time.latest_visit_date, EXCLUDED.latest_visit_date), updated_at = transaction_timestamp();
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_park_attributions_rollups_after_insert
AFTER INSERT ON app.workout_park_attributions FOR EACH ROW
EXECUTE FUNCTION app.increment_park_rollups ();

-- +goose StatementBegin
CREATE FUNCTION app.decrement_park_rollups () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    UPDATE app.account_park_daily_rollups SET workout_count = workout_count - 1, updated_at = transaction_timestamp()
     WHERE account_id = OLD.account_id AND park_id = OLD.park_id AND visit_date = OLD.visit_date AND workout_count > 1;
    IF NOT FOUND THEN DELETE FROM app.account_park_daily_rollups
        WHERE account_id = OLD.account_id AND park_id = OLD.park_id AND visit_date = OLD.visit_date; END IF;
    DELETE FROM app.account_park_all_time WHERE account_id = OLD.account_id AND park_id = OLD.park_id;
    INSERT INTO app.account_park_all_time(account_id, park_id, workout_count, first_visit_date, latest_visit_date)
    SELECT account_id, park_id, count( * )::integer, min(visit_date), max(visit_date) FROM app.workout_park_attributions
     WHERE account_id = OLD.account_id AND park_id = OLD.park_id GROUP BY account_id, park_id;
    DELETE FROM app.coverage_parks park WHERE park.account_id = OLD.account_id AND park.park_id = OLD.park_id
      AND NOT EXISTS (SELECT 1 FROM app.workout_park_attributions attribution
          WHERE attribution.account_id = park.account_id AND attribution.park_id = park.park_id);
    RETURN OLD;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_park_attributions_rollups_after_delete
AFTER DELETE ON app.workout_park_attributions FOR EACH ROW
EXECUTE FUNCTION app.decrement_park_rollups ();

-- +goose StatementBegin
CREATE FUNCTION app.attribute_workout_segment_to_park () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE segment record; workout_date date;
BEGIN
    SELECT copied.tags, copied.locality_relation_id, path.locality_relation_version, path.locality_name
      INTO segment FROM app.path_segments copied JOIN app.coverage_paths path
        ON path.account_id = copied.account_id AND path.logical_path_id = copied.logical_path_id
     WHERE copied.account_id = NEW.account_id AND copied.region_id = NEW.region_id AND copied.generation_id = NEW.generation_id
       AND copied.physical_segment_id = NEW.physical_segment_id;
    IF NOT FOUND OR NOT segment.tags ? 'workouts:park_id' THEN RETURN NEW; END IF;
    SELECT local_start_date INTO workout_date FROM app.workouts WHERE id = NEW.workout_id AND account_id = NEW.account_id;
    INSERT INTO app.coverage_parks(account_id, park_id, name, normalized_name, park_kind, locality_relation_id,
        locality_relation_version, locality_name)
    VALUES(NEW.account_id, (segment.tags->> 'workouts:park_id')::uuid, segment.tags->> 'workouts:park_name',
        segment.tags->> 'workouts:park_normalized_name', COALESCE(segment.tags->> 'workouts:park_kind', 'local_park'),
        CASE WHEN segment.tags->> 'workouts:park_kind' IN ('state_park', 'national_park') THEN NULL ELSE segment.locality_relation_id END,
        CASE WHEN segment.tags->> 'workouts:park_kind' IN ('state_park', 'national_park') THEN NULL ELSE segment.locality_relation_version END,
        CASE WHEN segment.tags->> 'workouts:park_kind' IN ('state_park', 'national_park') THEN NULL ELSE segment.locality_name END)
    ON CONFLICT (account_id, park_id) DO UPDATE SET name = EXCLUDED.name, normalized_name = EXCLUDED.normalized_name,
        park_kind = EXCLUDED.park_kind, locality_relation_id = EXCLUDED.locality_relation_id,
        locality_relation_version = EXCLUDED.locality_relation_version, locality_name = EXCLUDED.locality_name,
        updated_at = transaction_timestamp();
    INSERT INTO app.workout_park_attributions(account_id, workout_id, park_id, first_traversed_at, visit_date)
    VALUES(NEW.account_id, NEW.workout_id, (segment.tags->> 'workouts:park_id')::uuid, NEW.first_traversed_at, workout_date)
    ON CONFLICT (account_id, workout_id, park_id) DO UPDATE
       SET first_traversed_at = LEAST(app.workout_park_attributions.first_traversed_at, EXCLUDED.first_traversed_at);
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_segment_matches_park_after_insert
AFTER INSERT ON app.workout_segment_matches FOR EACH ROW
EXECUTE FUNCTION app.attribute_workout_segment_to_park ();

-- +goose StatementBegin
CREATE FUNCTION app.remove_workout_segment_park_attribution () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE old_park_id uuid;
BEGIN
    SELECT NULLIF(tags->> 'workouts:park_id', '')::uuid INTO old_park_id FROM app.path_segments
     WHERE account_id = OLD.account_id AND region_id = OLD.region_id AND generation_id = OLD.generation_id
       AND physical_segment_id = OLD.physical_segment_id;
    IF old_park_id IS NULL THEN RETURN OLD; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.workout_segment_matches match JOIN app.path_segments segment
          ON segment.account_id = match.account_id AND segment.region_id = match.region_id
         AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE match.account_id = OLD.account_id AND match.workout_id = OLD.workout_id
          AND segment.tags->> 'workouts:park_id' = old_park_id::text
    ) THEN
        DELETE FROM app.workout_park_attributions WHERE account_id = OLD.account_id
          AND workout_id = OLD.workout_id AND park_id = old_park_id;
    END IF;
    RETURN OLD;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_segment_matches_park_after_delete
AFTER DELETE ON app.workout_segment_matches FOR EACH ROW
EXECUTE FUNCTION app.remove_workout_segment_park_attribution ();

-- User-facing coverage assigns local-park-attributed unnamed segments exclusively
-- to their park. State- and national-park roads and paths remain individual paths
-- and use the regional park as display context in migration 019 explorer reads.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_path_counts (target_account_id uuid, target_session_id uuid, target_selection_id uuid, target_generation bigint) RETURNS TABLE (
    logical_path_id uuid,
    name text,
    locality_name text,
    broad_class text,
    selected_workout_count bigint,
    all_time_workout_count bigint,
    first_visit_date date,
    latest_visit_date date,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision
) LANGUAGE sql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date, selection.end_date, selection.selection_kind
        FROM app.map_selections selection JOIN app.sessions session ON session.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
          AND selection.session_id = target_session_id AND selection.generation = target_generation
          AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
          AND session.revoked_at IS NULL AND session.expires_at > transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state = 'active' AND target_account_id = app.current_account_id()
    ), path_counts AS (
        SELECT rollup.logical_path_id, sum(rollup.workout_count)::bigint workout_count
        FROM selected JOIN app.account_path_daily_rollups rollup ON selected.selection_kind = 'complete_range'
          AND rollup.account_id = target_account_id AND rollup.visit_date BETWEEN selected.start_date AND selected.end_date
        GROUP BY rollup.logical_path_id
        UNION ALL
        SELECT attribution.logical_path_id, count( * )::bigint FROM selected
        JOIN app.map_selection_workouts selected_workout ON selected.selection_kind = 'explicit_subset'
          AND selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
        JOIN app.workout_path_attributions attribution ON attribution.account_id = selected_workout.account_id
          AND attribution.workout_id = selected_workout.workout_id GROUP BY attribution.logical_path_id
    ), path_bounds AS (
        SELECT match.logical_path_id, ST_Extent(match.geom)::box2d extent FROM app.map_selection_workouts selected_workout
        JOIN app.workout_segment_matches match ON match.account_id = selected_workout.account_id AND match.workout_id = selected_workout.workout_id
        WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id GROUP BY match.logical_path_id
    ), park_counts AS (
        SELECT rollup.park_id, sum(rollup.workout_count)::bigint workout_count FROM selected
        JOIN app.account_park_daily_rollups rollup ON selected.selection_kind = 'complete_range'
          AND rollup.account_id = target_account_id AND rollup.visit_date BETWEEN selected.start_date AND selected.end_date GROUP BY rollup.park_id
        UNION ALL
        SELECT attribution.park_id, count( * )::bigint FROM selected
        JOIN app.map_selection_workouts selected_workout ON selected.selection_kind = 'explicit_subset'
          AND selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
        JOIN app.workout_park_attributions attribution ON attribution.account_id = selected_workout.account_id
          AND attribution.workout_id = selected_workout.workout_id GROUP BY attribution.park_id
    ), park_bounds AS (
        SELECT (segment.tags->> 'workouts:park_id')::uuid park_id, ST_Extent(match.geom)::box2d extent
        FROM app.map_selection_workouts selected_workout JOIN app.workout_segment_matches match
          ON match.account_id = selected_workout.account_id AND match.workout_id = selected_workout.workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
          AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
          AND segment.tags ? 'workouts:park_id' GROUP BY (segment.tags->> 'workouts:park_id')::uuid
    )
    SELECT path.logical_path_id, path.name, path.locality_name, path.broad_class, counts.workout_count,
        all_time.workout_count::bigint, all_time.first_visit_date, all_time.latest_visit_date,
        ST_XMin(bounds.extent), ST_YMin(bounds.extent), ST_XMax(bounds.extent), ST_YMax(bounds.extent)
    FROM path_counts counts JOIN app.coverage_paths path ON path.account_id = target_account_id AND path.logical_path_id = counts.logical_path_id
    JOIN app.account_path_all_time all_time ON all_time.account_id = path.account_id AND all_time.logical_path_id = path.logical_path_id
    JOIN path_bounds bounds ON bounds.logical_path_id = path.logical_path_id
    WHERE counts.workout_count > 0 AND EXISTS (SELECT 1 FROM app.path_segments segment
        WHERE segment.account_id = path.account_id AND segment.logical_path_id = path.logical_path_id
          AND NOT (segment.highway = 'service' AND COALESCE(segment.tags->> 'service', '') IN ('driveway', 'parking_aisle'))
          AND segment.tags->> 'amenity' IS DISTINCT FROM 'parking')
    UNION ALL
    SELECT park.park_id, park.name, park.locality_name, 'park', counts.workout_count,
        all_time.workout_count::bigint, all_time.first_visit_date, all_time.latest_visit_date,
        ST_XMin(bounds.extent), ST_YMin(bounds.extent), ST_XMax(bounds.extent), ST_YMax(bounds.extent)
    FROM park_counts counts JOIN app.coverage_parks park ON park.account_id = target_account_id AND park.park_id = counts.park_id
    JOIN app.account_park_all_time all_time ON all_time.account_id = park.account_id AND all_time.park_id = park.park_id
    JOIN park_bounds bounds ON bounds.park_id = park.park_id WHERE counts.workout_count > 0
$function$;

-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.coverage_parks OWNER TO workouts_security_owner;

ALTER TABLE app.workout_park_attributions OWNER TO workouts_security_owner;

ALTER TABLE app.account_park_daily_rollups OWNER TO workouts_security_owner;

ALTER TABLE app.account_park_all_time OWNER TO workouts_security_owner;

ALTER FUNCTION app.increment_park_rollups () OWNER TO workouts_security_owner;

ALTER FUNCTION app.decrement_park_rollups () OWNER TO workouts_security_owner;

ALTER FUNCTION app.attribute_workout_segment_to_park () OWNER TO workouts_security_owner;

ALTER FUNCTION app.remove_workout_segment_park_attribution () OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON app.coverage_parks,
app.workout_park_attributions,
app.account_park_daily_rollups,
app.account_park_all_time
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
SELECT
    ON app.coverage_parks,
    app.workout_park_attributions,
    app.account_park_daily_rollups,
    app.account_park_all_time TO workouts_api;

REVOKE ALL ON FUNCTION app.increment_park_rollups (),
app.decrement_park_rollups (),
app.attribute_workout_segment_to_park (),
app.remove_workout_segment_park_attribution ()
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

UPDATE app.schema_metadata
SET
    schema_version = 18,
    minimum_runtime_version = 17
WHERE
    singleton;

-- +goose Down
UPDATE app.schema_metadata
SET
    schema_version = 17,
    minimum_runtime_version = 16
WHERE
    singleton;

-- +goose StatementBegin
DO $block$
DECLARE saved_definition text;
BEGIN
    SELECT definition INTO saved_definition FROM app.park_attribution_function_backup;
    EXECUTE saved_definition;
END;
$block$;

-- +goose StatementEnd

DROP TABLE app.park_attribution_function_backup;

DROP TRIGGER workout_segment_matches_park_after_delete ON app.workout_segment_matches;

DROP FUNCTION app.remove_workout_segment_park_attribution ();

DROP TRIGGER workout_segment_matches_park_after_insert ON app.workout_segment_matches;

DROP FUNCTION app.attribute_workout_segment_to_park ();

DROP TRIGGER workout_park_attributions_rollups_after_delete ON app.workout_park_attributions;

DROP FUNCTION app.decrement_park_rollups ();

DROP TRIGGER workout_park_attributions_rollups_after_insert ON app.workout_park_attributions;

DROP FUNCTION app.increment_park_rollups ();

DROP TABLE app.account_park_all_time;

DROP TABLE app.account_park_daily_rollups;

DROP TABLE app.workout_park_attributions;

DROP TABLE app.coverage_parks;
