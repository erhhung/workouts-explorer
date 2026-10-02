-- +goose Up
CREATE TABLE osm_catalog.generation_stages (
    generation_id bigint NOT NULL REFERENCES osm_catalog.generations (id) ON DELETE CASCADE,
    stage text NOT NULL,
    stage_order integer NOT NULL CHECK (stage_order >= 0),
    fence text NOT NULL CHECK (length(fence) = 64),
    state text NOT NULL CHECK (state IN ('running', 'completed', 'failed')),
    started_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    finished_at timestamptz,
    failure_summary text,
    cursor jsonb NOT NULL DEFAULT '{}'::jsonb,
    rows_processed bigint NOT NULL DEFAULT 0 CHECK (rows_processed >= 0),
    batch_count bigint NOT NULL DEFAULT 0 CHECK (batch_count >= 0),
    checkpointed_at timestamptz,
    PRIMARY KEY (generation_id, stage)
);

REVOKE ALL ON osm_catalog.generation_stages FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.checkpoint_generation_stage(
    target_generation_id bigint,
    target_stage text,
    expected_batch_count bigint,
    target_cursor jsonb,
    target_rows_processed bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, osm_catalog
AS $function$
BEGIN
    UPDATE osm_catalog.generation_stages AS stage
    SET
        cursor = target_cursor,
        rows_processed = target_rows_processed,
        batch_count = batch_count + 1,
        checkpointed_at = transaction_timestamp()
    FROM osm_catalog.generations AS generation
    WHERE stage.generation_id = target_generation_id
        AND stage.stage = target_stage
        AND stage.state = 'running'
        AND stage.batch_count = expected_batch_count
        AND generation.id = stage.generation_id
        AND generation.state IN ('building', 'evaluating');

    IF NOT FOUND THEN
        RAISE EXCEPTION 'generation % stage % is not running',
            target_generation_id, target_stage;
    END IF;
END;
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION osm_catalog.checkpoint_generation_stage(bigint, text, bigint, jsonb, bigint)
FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.complete_generation_stage(
    target_generation_id bigint,
    target_stage text,
    expected_batch_count bigint,
    target_cursor jsonb,
    target_rows_processed bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, osm_catalog
AS $function$
BEGIN
    UPDATE osm_catalog.generation_stages AS stage
    SET
        state = 'completed',
        cursor = target_cursor,
        rows_processed = target_rows_processed,
        batch_count = batch_count + 1,
        checkpointed_at = transaction_timestamp(),
        finished_at = transaction_timestamp(),
        failure_summary = NULL
    FROM osm_catalog.generations AS generation
    WHERE stage.generation_id = target_generation_id
        AND stage.stage = target_stage
        AND stage.state = 'running'
        AND stage.batch_count = expected_batch_count
        AND generation.id = stage.generation_id
        AND generation.state IN ('building', 'evaluating');

    IF NOT FOUND THEN
        RAISE EXCEPTION 'generation % stage % checkpoint changed',
            target_generation_id, target_stage;
    END IF;
END;
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION osm_catalog.complete_generation_stage(bigint, text, bigint, jsonb, bigint)
FROM PUBLIC;

UPDATE osm_catalog.schema_metadata
SET
    schema_version = 8,
    minimum_runtime_version = 8
WHERE singleton;

-- +goose Down
UPDATE osm_catalog.schema_metadata
SET
    schema_version = 7,
    minimum_runtime_version = 7
WHERE singleton;

DROP FUNCTION osm_catalog.complete_generation_stage(bigint, text, bigint, jsonb, bigint);

DROP FUNCTION osm_catalog.checkpoint_generation_stage(bigint, text, bigint, jsonb, bigint);

DROP TABLE osm_catalog.generation_stages;
