-- +goose Up
-- No route child may cross the completion-authority change in this migration.
-- +goose StatementBegin
DO $block$
BEGIN
    LOCK TABLE app.jobs IN SHARE ROW EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM app.jobs WHERE kind = 'coverage_update_route' AND status IN ('queued', 'running')) THEN
        RAISE EXCEPTION 'cannot install durable coverage while route jobs are active' USING ERRCODE = '55006';
    END IF;
END;
$block$;

-- +goose StatementEnd

CREATE TABLE app.coverage_paths (
    account_id uuid NOT NULL REFERENCES app.accounts (id) ON DELETE CASCADE,
    logical_path_id uuid NOT NULL,
    locality_relation_id bigint,
    locality_relation_version integer,
    locality_name text,
    name text,
    normalized_name text,
    broad_class text NOT NULL CHECK (broad_class IN ('road', 'cycleway', 'footway', 'trail', 'other')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, logical_path_id),
    CHECK ((name IS NULL) = (normalized_name IS NULL)),
    CHECK (
        name IS NULL
        OR length(name) BETWEEN 1 AND 4096
    ),
    CHECK (
        normalized_name IS NULL
        OR length(normalized_name) BETWEEN 1 AND 4096
    ),
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
    )
);

CREATE INDEX coverage_paths_locality_name_idx ON app.coverage_paths (account_id, locality_relation_id, normalized_name, broad_class, logical_path_id);

CREATE TABLE app.path_segments (
    account_id uuid NOT NULL,
    region_id text NOT NULL CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    generation_id bigint NOT NULL CHECK (generation_id > 0),
    physical_segment_id uuid NOT NULL,
    logical_path_id uuid NOT NULL,
    derivation_version integer NOT NULL CHECK (derivation_version > 0),
    locality_relation_id bigint,
    source_way_id bigint NOT NULL,
    source_way_version integer NOT NULL CHECK (source_way_version > 0),
    name text,
    normalized_name text,
    highway text NOT NULL CHECK (length(highway) BETWEEN 1 AND 128),
    broad_class text NOT NULL CHECK (broad_class IN ('road', 'cycleway', 'footway', 'trail', 'other')),
    tags jsonb NOT NULL CHECK (
        jsonb_typeof(tags) = 'object'
        AND octet_length(tags::text) <= 65536
    ),
    segment_meters double precision NOT NULL CHECK (
        segment_meters > 0
        AND segment_meters < 'Infinity'::double precision
    ),
    geom geometry (LineString, 4326) NOT NULL CHECK (
        NOT ST_IsEmpty (geom)
        AND ST_IsValid (geom)
        AND ST_NPoints (geom) >= 2
        AND ST_Length (geom::geography) > 0
    ),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, region_id, generation_id, physical_segment_id),
    UNIQUE (account_id, region_id, generation_id, physical_segment_id, logical_path_id),
    FOREIGN KEY (account_id, logical_path_id) REFERENCES app.coverage_paths (account_id, logical_path_id),
    CHECK ((name IS NULL) = (normalized_name IS NULL))
);

CREATE INDEX path_segments_logical_path_idx ON app.path_segments (account_id, logical_path_id, physical_segment_id);

CREATE INDEX path_segments_source_idx ON app.path_segments (account_id, source_way_id, source_way_version);

CREATE TABLE app.workout_segment_matches (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    physical_segment_id uuid NOT NULL,
    region_id text NOT NULL,
    generation_id bigint NOT NULL CHECK (generation_id > 0),
    logical_path_id uuid NOT NULL,
    first_traversed_at timestamptz NOT NULL,
    first_route_order integer NOT NULL CHECK (first_route_order >= 0),
    covered_meters double precision NOT NULL CHECK (
        covered_meters > 0
        AND covered_meters < 'Infinity'::double precision
    ),
    geom geometry (MultiLineString, 4326) NOT NULL CHECK (
        NOT ST_IsEmpty (geom)
        AND ST_IsValid (geom)
        AND ST_NumGeometries (geom) >= 1
        AND ST_Length (geom::geography) > 0
    ),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, workout_id, physical_segment_id),
    UNIQUE (account_id, workout_id, logical_path_id, physical_segment_id, region_id, generation_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (account_id, region_id, generation_id, physical_segment_id, logical_path_id) REFERENCES app.path_segments (account_id, region_id, generation_id, physical_segment_id, logical_path_id)
);

CREATE INDEX workout_segment_matches_path_idx ON app.workout_segment_matches (account_id, logical_path_id, workout_id);

CREATE INDEX workout_segment_matches_segment_copy_idx ON app.workout_segment_matches (account_id, region_id, generation_id, physical_segment_id);

CREATE INDEX workout_segment_matches_geom_gist_idx ON app.workout_segment_matches USING gist (geom);

CREATE TABLE app.workout_path_attributions (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    logical_path_id uuid NOT NULL,
    first_physical_segment_id uuid NOT NULL,
    first_region_id text NOT NULL,
    first_generation_id bigint NOT NULL,
    first_traversed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, workout_id, logical_path_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (account_id, logical_path_id) REFERENCES app.coverage_paths (account_id, logical_path_id),
    FOREIGN KEY (
        account_id,
        workout_id,
        logical_path_id,
        first_physical_segment_id,
        first_region_id,
        first_generation_id
    ) REFERENCES app.workout_segment_matches (account_id, workout_id, logical_path_id, physical_segment_id, region_id, generation_id) ON DELETE CASCADE
);

CREATE INDEX workout_path_attributions_path_idx ON app.workout_path_attributions (account_id, logical_path_id, workout_id, first_traversed_at);

ALTER TABLE app.coverage_paths ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_paths FORCE ROW LEVEL SECURITY;

ALTER TABLE app.path_segments ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.path_segments FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_segment_matches ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.workout_segment_matches FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_path_attributions ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.workout_path_attributions FORCE ROW LEVEL SECURITY;

CREATE POLICY coverage_paths_account_policy ON app.coverage_paths USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY coverage_paths_owner_policy ON app.coverage_paths TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY path_segments_account_policy ON app.path_segments USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY path_segments_owner_policy ON app.path_segments TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY workout_segment_matches_account_policy ON app.workout_segment_matches USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY workout_segment_matches_owner_policy ON app.workout_segment_matches TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY workout_path_attributions_account_policy ON app.workout_path_attributions USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY workout_path_attributions_owner_policy ON app.workout_path_attributions TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

-- +goose StatementBegin
CREATE FUNCTION app.prune_unreferenced_coverage_copies () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    DELETE FROM app.path_segments segment WHERE segment.account_id = OLD.account_id
      AND segment.region_id = OLD.region_id AND segment.generation_id = OLD.generation_id
      AND segment.physical_segment_id = OLD.physical_segment_id
      AND NOT EXISTS (SELECT 1 FROM app.workout_segment_matches match
          WHERE match.account_id = segment.account_id AND match.region_id = segment.region_id
            AND match.generation_id = segment.generation_id AND match.physical_segment_id = segment.physical_segment_id);
    DELETE FROM app.coverage_paths path WHERE path.account_id = OLD.account_id AND path.logical_path_id = OLD.logical_path_id
      AND NOT EXISTS (SELECT 1 FROM app.path_segments segment
          WHERE segment.account_id = path.account_id AND segment.logical_path_id = path.logical_path_id)
      AND NOT EXISTS (SELECT 1 FROM app.workout_path_attributions attribution
          WHERE attribution.account_id = path.account_id AND attribution.logical_path_id = path.logical_path_id);
    RETURN OLD;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_segment_matches_prune_copies_after_delete
AFTER DELETE ON app.workout_segment_matches FOR EACH ROW
EXECUTE FUNCTION app.prune_unreferenced_coverage_copies ();

-- +goose StatementBegin
CREATE FUNCTION app.prune_unreferenced_coverage_path () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    DELETE FROM app.coverage_paths path WHERE path.account_id = OLD.account_id AND path.logical_path_id = OLD.logical_path_id
      AND NOT EXISTS (SELECT 1 FROM app.path_segments segment
          WHERE segment.account_id = path.account_id AND segment.logical_path_id = path.logical_path_id)
      AND NOT EXISTS (SELECT 1 FROM app.workout_path_attributions attribution
          WHERE attribution.account_id = path.account_id AND attribution.logical_path_id = path.logical_path_id);
    RETURN OLD;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_path_attributions_prune_path_after_delete
AFTER DELETE ON app.workout_path_attributions FOR EACH ROW
EXECUTE FUNCTION app.prune_unreferenced_coverage_path ();

-- +goose StatementBegin
CREATE FUNCTION app.valid_coverage_generation_vector (value jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE
SET
    search_path = pg_catalog AS $function$
    SELECT jsonb_typeof(value) = 'array' AND jsonb_array_length(value) BETWEEN 1 AND 256
       AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(value) item
            WHERE jsonb_typeof(item) <> 'object' OR (SELECT count( * ) FROM jsonb_object_keys(item)) <> 2
               OR NOT (item ?& ARRAY['regionId', 'generation'])
               OR item->> 'regionId' !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'
               OR item->> 'generation' !~ '^[1-9][0-9]*$'
       ) AND (SELECT count( * ) = count(DISTINCT item->> 'regionId') FROM jsonb_array_elements(value) item)
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_generation_vectors_equal (left_value jsonb, right_value jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT app.valid_coverage_generation_vector(left_value) AND app.valid_coverage_generation_vector(right_value)
       AND NOT EXISTS ((SELECT item->> 'regionId', (item->> 'generation')::bigint FROM jsonb_array_elements(left_value) item)
                       EXCEPT
                       (SELECT item->> 'regionId', (item->> 'generation')::bigint FROM jsonb_array_elements(right_value) item))
       AND NOT EXISTS ((SELECT item->> 'regionId', (item->> 'generation')::bigint FROM jsonb_array_elements(right_value) item)
                       EXCEPT
                       (SELECT item->> 'regionId', (item->> 'generation')::bigint FROM jsonb_array_elements(left_value) item))
$function$;

-- +goose StatementEnd

ALTER TABLE app.coverage_route_job_contexts
DROP CONSTRAINT coverage_route_job_contexts_target_generations_check;

ALTER TABLE app.coverage_route_job_contexts
ADD CONSTRAINT coverage_route_job_contexts_target_generations_check CHECK (app.valid_coverage_generation_vector (target_generations));

-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_route (target_job_id uuid, claiming_worker text, current_lease_token uuid) RETURNS TABLE (
    sequence integer,
    recorded_at timestamptz,
    latitude double precision,
    longitude double precision,
    altitude double precision,
    speed double precision,
    course double precision,
    horizontal_accuracy double precision,
    vertical_accuracy double precision,
    speed_accuracy double precision,
    course_accuracy double precision,
    workout_started_at timestamptz,
    type_key text,
    provider_label text
) LANGUAGE sql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT point.sequence, point.recorded_at, point.latitude, point.longitude, point.altitude, point.speed, point.course,
           point.horizontal_accuracy, point.vertical_accuracy, point.speed_accuracy, point.course_accuracy,
           workout.started_at, workout_type.type_key, workout_type.provider_label
      FROM app.jobs job
      JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
      JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
      JOIN app.workouts workout ON workout.id = context.workout_id AND workout.account_id = context.account_id
      JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
      JOIN app.workout_route_points point ON point.workout_id = workout.id AND point.account_id = workout.account_id
     WHERE job.id = $1 AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
       AND job.status = 'running' AND job.worker_id = $2 AND job.lease_token = $3
       AND job.lease_expires_at >= clock_timestamp() AND job.cancel_requested_at IS NULL
       AND state.target_job_id = job.id AND state.route_input_revision = context.route_input_revision
       AND state.route_input_sha256 = context.route_input_sha256 AND workout.deletion_requested_at IS NULL
     ORDER BY point.sequence LIMIT 25001
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.persist_coverage_route (
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid,
    duration_milliseconds integer,
    observed_generations jsonb,
    target_matches jsonb
) RETURNS text LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE target record; item jsonb; match_count integer := 0; final_outcome text;
DECLARE segment_geometry geometry; covered_geometry geometry;
BEGIN
    IF duration_milliseconds < 0 OR NOT app.valid_coverage_generation_vector(observed_generations) OR
       jsonb_typeof(target_matches) <> 'array' OR jsonb_array_length(target_matches) > 4096 OR octet_length(target_matches::text) > 67108864 THEN
        RAISE EXCEPTION 'invalid coverage persistence payload' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id, context.workout_id, context.route_input_revision, context.route_input_sha256, context.target_generations
      INTO target FROM app.jobs job JOIN app.coverage_route_job_contexts context
        ON context.job_id = job.id AND context.account_id = job.account_id
     WHERE job.id = persist_coverage_route.target_job_id AND job.account_id = app.current_account_id()
       AND job.kind = 'coverage_update_route' AND job.status = 'running' AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp() FOR UPDATE OF job, context;
    IF NOT FOUND THEN RETURN NULL; END IF;
    IF NOT app.coverage_generation_vectors_equal(target.target_generations, observed_generations) THEN
        RAISE EXCEPTION 'coverage generation target changed' USING ERRCODE = '40001';
    END IF;
    PERFORM 1 FROM app.workout_coverage_states state
     WHERE state.account_id = target.account_id AND state.workout_id = target.workout_id
       AND state.target_job_id = persist_coverage_route.target_job_id AND state.route_input_revision = target.route_input_revision
       AND state.route_input_sha256 = target.route_input_sha256 FOR UPDATE;
    IF NOT FOUND THEN
        IF NOT app.finish_coverage_route(persist_coverage_route.target_job_id, claiming_worker, current_lease_token, 'superseded', duration_milliseconds) THEN
            RAISE EXCEPTION 'coverage superseded completion lost its lease' USING ERRCODE = '40001';
        END IF;
        RETURN 'superseded';
    END IF;
    DELETE FROM app.workout_path_attributions WHERE account_id = target.account_id AND workout_id = target.workout_id;
    DELETE FROM app.workout_segment_matches WHERE account_id = target.account_id AND workout_id = target.workout_id;
    FOR item IN SELECT value FROM jsonb_array_elements(target_matches) LOOP
        IF jsonb_typeof(item) <> 'object' OR NOT (item ?& ARRAY['physicalSegmentId', 'regionId', 'generation', 'derivationVersion',
            'logicalPathId', 'sourceWayId', 'sourceWayVersion', 'highway', 'broadClass', 'tags', 'segmentMeters', 'segmentGeometry',
            'firstTraversedAt', 'firstRouteOrder', 'coveredGeometry']) THEN
            RAISE EXCEPTION 'invalid coverage match item' USING ERRCODE = '22023';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements(observed_generations) generation
            WHERE generation->> 'regionId' = item->> 'regionId'
              AND (generation->> 'generation')::bigint = (item->> 'generation')::bigint) THEN
            RAISE EXCEPTION 'coverage match is outside the target generation vector' USING ERRCODE = '22023';
        END IF;
        segment_geometry := ST_SetSRID(ST_GeomFromGeoJSON(item->'segmentGeometry'), 4326);
        covered_geometry := ST_SetSRID(ST_GeomFromGeoJSON(item->'coveredGeometry'), 4326);
        INSERT INTO app.coverage_paths(account_id, logical_path_id, locality_relation_id, locality_relation_version, locality_name,
            name, normalized_name, broad_class)
        VALUES(target.account_id, (item->> 'logicalPathId')::uuid, NULLIF(item->> 'localityRelationId', '')::bigint,
            NULLIF(item->> 'localityRelationVersion', '')::integer, NULLIF(item->> 'localityName', ''), NULLIF(item->> 'pathName', ''),
            NULLIF(item->> 'pathNormalizedName', ''), item->> 'broadClass')
        ON CONFLICT (account_id, logical_path_id) DO UPDATE SET locality_relation_id = EXCLUDED.locality_relation_id,
            locality_relation_version = EXCLUDED.locality_relation_version, locality_name = EXCLUDED.locality_name,
            name = EXCLUDED.name, normalized_name = EXCLUDED.normalized_name, broad_class = EXCLUDED.broad_class,
            updated_at = transaction_timestamp();
        INSERT INTO app.path_segments(account_id, region_id, generation_id, physical_segment_id, logical_path_id,
            derivation_version, locality_relation_id, source_way_id, source_way_version, name, normalized_name, highway, broad_class,
            tags, segment_meters, geom)
        VALUES(target.account_id, item->> 'regionId', (item->> 'generation')::bigint, (item->> 'physicalSegmentId')::uuid,
            (item->> 'logicalPathId')::uuid, (item->> 'derivationVersion')::integer, NULLIF(item->> 'localityRelationId', '')::bigint,
            (item->> 'sourceWayId')::bigint, (item->> 'sourceWayVersion')::integer, NULLIF(item->> 'segmentName', ''),
            NULLIF(item->> 'segmentNormalizedName', ''), item->> 'highway', item->> 'broadClass', item->'tags',
            (item->> 'segmentMeters')::double precision, segment_geometry)
        ON CONFLICT (account_id, region_id, generation_id, physical_segment_id) DO NOTHING;
        INSERT INTO app.workout_segment_matches(account_id, workout_id, physical_segment_id, region_id, generation_id,
            logical_path_id, first_traversed_at, first_route_order, covered_meters, geom)
        VALUES(target.account_id, target.workout_id, (item->> 'physicalSegmentId')::uuid, item->> 'regionId',
            (item->> 'generation')::bigint, (item->> 'logicalPathId')::uuid, (item->> 'firstTraversedAt')::timestamptz,
            (item->> 'firstRouteOrder')::integer, ST_Length(covered_geometry::geography), covered_geometry);
        match_count := match_count + 1;
    END LOOP;
    INSERT INTO app.workout_path_attributions(account_id, workout_id, logical_path_id, first_physical_segment_id,
        first_region_id, first_generation_id, first_traversed_at)
    SELECT DISTINCT ON (match.logical_path_id) match.account_id, match.workout_id, match.logical_path_id,
        match.physical_segment_id, match.region_id, match.generation_id, match.first_traversed_at
      FROM app.workout_segment_matches match WHERE match.account_id = target.account_id AND match.workout_id = target.workout_id
     ORDER BY match.logical_path_id, match.first_route_order, match.first_traversed_at,
        match.region_id, match.generation_id, match.physical_segment_id;
    DELETE FROM app.path_segments segment WHERE segment.account_id = target.account_id AND NOT EXISTS (
        SELECT 1 FROM app.workout_segment_matches match WHERE match.account_id = segment.account_id
          AND match.region_id = segment.region_id AND match.generation_id = segment.generation_id
          AND match.physical_segment_id = segment.physical_segment_id);
    DELETE FROM app.coverage_paths path WHERE path.account_id = target.account_id AND NOT EXISTS (
        SELECT 1 FROM app.path_segments segment WHERE segment.account_id = path.account_id AND segment.logical_path_id = path.logical_path_id);
    final_outcome := CASE WHEN match_count = 0 THEN 'no_evidence' ELSE 'applied' END;
    IF NOT app.finish_coverage_route(persist_coverage_route.target_job_id, claiming_worker, current_lease_token, final_outcome, duration_milliseconds) THEN
        RAISE EXCEPTION 'coverage persistence lost its lease' USING ERRCODE = '40001';
    END IF;
    RETURN final_outcome;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.fail_coverage_route (
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid,
    duration_milliseconds integer,
    failure_code_value text,
    failure_summary_value text
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF failure_code_value IS NULL OR length(failure_code_value) NOT BETWEEN 1 AND 64 OR
       failure_summary_value IS NULL OR length(failure_summary_value) NOT BETWEEN 1 AND 512 THEN
        RAISE EXCEPTION 'invalid coverage failure' USING ERRCODE = '22023';
    END IF;
    RETURN app.finish_coverage_route(target_job_id, claiming_worker, current_lease_token, 'failed', duration_milliseconds,
        failure_code_value, failure_summary_value);
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON app.coverage_paths,
app.path_segments,
app.workout_segment_matches,
app.workout_path_attributions
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
SELECT
    ON app.coverage_paths,
    app.path_segments,
    app.workout_segment_matches,
    app.workout_path_attributions TO workouts_api;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.coverage_paths OWNER TO workouts_security_owner;

ALTER TABLE app.path_segments OWNER TO workouts_security_owner;

ALTER TABLE app.workout_segment_matches OWNER TO workouts_security_owner;

ALTER TABLE app.workout_path_attributions OWNER TO workouts_security_owner;

ALTER FUNCTION app.valid_coverage_generation_vector (jsonb) OWNER TO workouts_security_owner;

ALTER FUNCTION app.prune_unreferenced_coverage_copies () OWNER TO workouts_security_owner;

ALTER FUNCTION app.prune_unreferenced_coverage_path () OWNER TO workouts_security_owner;

ALTER FUNCTION app.coverage_generation_vectors_equal (jsonb, jsonb) OWNER TO workouts_security_owner;

ALTER FUNCTION app.read_coverage_route (uuid, text, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.persist_coverage_route (uuid, text, uuid, integer, jsonb, jsonb) OWNER TO workouts_security_owner;

ALTER FUNCTION app.fail_coverage_route (uuid, text, uuid, integer, text, text) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.valid_coverage_generation_vector (jsonb),
app.coverage_generation_vectors_equal (jsonb, jsonb),
app.prune_unreferenced_coverage_copies (),
app.prune_unreferenced_coverage_path (),
app.read_coverage_route (uuid, text, uuid),
app.persist_coverage_route (uuid, text, uuid, integer, jsonb, jsonb),
app.fail_coverage_route (uuid, text, uuid, integer, text, text),
app.finish_coverage_route (uuid, text, uuid, text, integer, text, text)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_coverage_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.valid_coverage_generation_vector (jsonb),
app.coverage_generation_vectors_equal (jsonb, jsonb) TO workouts_api;

GRANT
EXECUTE ON FUNCTION app.read_coverage_route (uuid, text, uuid),
app.persist_coverage_route (uuid, text, uuid, integer, jsonb, jsonb),
app.fail_coverage_route (uuid, text, uuid, integer, text, text) TO workouts_coverage_worker;

UPDATE app.schema_metadata
SET
    schema_version = 16,
    minimum_runtime_version = 15
WHERE
    singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    IF EXISTS (SELECT 1 FROM app.workout_segment_matches) OR EXISTS (SELECT 1 FROM app.workout_path_attributions) THEN
        RAISE EXCEPTION 'cannot downgrade while durable coverage exists' USING ERRCODE = '55006';
    END IF;
END;
$block$;

-- +goose StatementEnd

UPDATE app.schema_metadata
SET
    schema_version = 15,
    minimum_runtime_version = 14
WHERE
    singleton;

GRANT
EXECUTE ON FUNCTION app.finish_coverage_route (uuid, text, uuid, text, integer, text, text) TO workouts_coverage_worker;

DROP FUNCTION app.fail_coverage_route (uuid, text, uuid, integer, text, text);

DROP FUNCTION app.persist_coverage_route (uuid, text, uuid, integer, jsonb, jsonb);

DROP FUNCTION app.read_coverage_route (uuid, text, uuid);

ALTER TABLE app.coverage_route_job_contexts
DROP CONSTRAINT coverage_route_job_contexts_target_generations_check;

ALTER TABLE app.coverage_route_job_contexts
ADD CONSTRAINT coverage_route_job_contexts_target_generations_check CHECK (
    jsonb_typeof(target_generations) = 'array'
    AND jsonb_array_length(target_generations) BETWEEN 1 AND 256
);

DROP FUNCTION app.coverage_generation_vectors_equal (jsonb, jsonb);

DROP FUNCTION app.valid_coverage_generation_vector (jsonb);

DROP TRIGGER workout_path_attributions_prune_path_after_delete ON app.workout_path_attributions;

DROP FUNCTION app.prune_unreferenced_coverage_path ();

DROP TRIGGER workout_segment_matches_prune_copies_after_delete ON app.workout_segment_matches;

DROP FUNCTION app.prune_unreferenced_coverage_copies ();

DROP TABLE app.workout_path_attributions;

DROP TABLE app.workout_segment_matches;

DROP TABLE app.path_segments;

DROP TABLE app.coverage_paths;
