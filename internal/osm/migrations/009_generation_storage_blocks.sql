-- +goose Up
-- Migration 005 was squashed into 004. A deployed pre-squash schema reports
-- Goose version 8 and therefore skips the renumbered migration 008; keep this
-- bridge idempotent so that upgrade still receives the resumable-stage objects.
CREATE TABLE IF NOT EXISTS osm_catalog.generation_stages (
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
CREATE OR REPLACE FUNCTION osm_catalog.checkpoint_generation_stage(
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
CREATE OR REPLACE FUNCTION osm_catalog.complete_generation_stage(
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

ALTER TABLE osm_catalog.generations
    DROP CONSTRAINT generations_state_check;

ALTER TABLE osm_catalog.generations
    ADD CONSTRAINT generations_state_check
    CHECK (state IN ('building', 'validating', 'active', 'retired', 'failed', 'evaluating'));

CREATE TABLE osm_catalog.generation_storage_blocks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    generation_id bigint NOT NULL REFERENCES osm_catalog.generations (id) ON DELETE CASCADE,
    stage text NOT NULL,
    batch_count bigint NOT NULL CHECK (batch_count >= 0),
    cursor jsonb NOT NULL,
    reason_code text NOT NULL,
    postgres_primary text,
    namespace text,
    pod_name text,
    pvc_name text,
    capacity_bytes bigint CHECK (capacity_bytes IS NULL OR capacity_bytes >= 0),
    free_bytes bigint CHECK (free_bytes IS NULL OR free_bytes >= 0),
    required_free_bytes bigint CHECK (required_free_bytes IS NULL OR required_free_bytes >= 0),
    blocked_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    cleared_at timestamptz,
    cleared_by text,
    clearance_note text,
    CHECK (
        (cleared_at IS NULL AND cleared_by IS NULL)
        OR (cleared_at IS NOT NULL AND NULLIF(btrim(cleared_by), '') IS NOT NULL)
    )
);

CREATE UNIQUE INDEX generation_storage_blocks_active
    ON osm_catalog.generation_storage_blocks (generation_id)
    WHERE cleared_at IS NULL;

REVOKE ALL ON osm_catalog.generation_storage_blocks FROM PUBLIC;

UPDATE osm_catalog.schema_metadata
SET
    schema_version = 9,
    minimum_runtime_version = 9
WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $guard_evaluations$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM osm_catalog.generations
        WHERE state = 'evaluating'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade while identity evaluations exist';
    END IF;
END
$guard_evaluations$;
-- +goose StatementEnd

ALTER TABLE osm_catalog.generations
    DROP CONSTRAINT generations_state_check;

ALTER TABLE osm_catalog.generations
    ADD CONSTRAINT generations_state_check
    CHECK (state IN ('building', 'validating', 'active', 'retired', 'failed'));

UPDATE osm_catalog.schema_metadata
SET
    schema_version = 8,
    minimum_runtime_version = 8
WHERE singleton;

DROP TABLE osm_catalog.generation_storage_blocks;
